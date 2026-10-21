package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"
)

// Summary is a work with what is known of its releases together.
type Summary struct {
	Work Work
	// Releases counts the stored releases.
	Releases int64
	// Rank is the work's place in the 7-day top of its kind among all
	// voices, 0 when it is not in it.
	Rank int
	// Audio holds an entry per language of Options.Languages, in order.
	Audio []WorkAudio
	// Sources are the releases' sources, each once, in sourceOrder.
	Sources []string
	// FirstSeen is the first sighting of the earliest release; zero without
	// releases.
	FirstSeen time.Time
}

// WorkAudio sums up the audio in one language of a work's releases.
type WorkAudio struct {
	Language string
	// Marked counts the releases with a marker of the language; Marks are
	// their marks, each once, most frequent first.
	Marked int64
	Marks  []string
	// Likely counts the releases without a marker whose probability of an
	// audio track in the language is at least 0.5.
	Likely int64
}

// sourceOrder is the order of Summary.Sources.
var sourceOrder = []string{"disc_image", "remux", "encode", "web", "other"}

// Summaries returns the summaries of the stored works with the IDs over all
// their releases; an ID with no work is absent.
func (s *Store) Summaries(ctx context.Context, ids []int64) (map[int64]Summary, error) {
	return s.summaries(ctx, ids, VoiceAny, "")
}

// summaries returns the summaries of the stored works with the IDs over the
// releases v admits in the language.
func (s *Store) summaries(ctx context.Context, ids []int64, v Voice, lang string) (map[int64]Summary, error) {
	works, err := s.Works(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]Summary, len(works))
	for id, w := range works {
		sum := Summary{Work: w}
		for _, l := range s.opts.Languages {
			sum.Audio = append(sum.Audio, WorkAudio{Language: l})
		}
		out[id] = sum
	}
	if len(out) == 0 {
		return out, nil
	}
	cond, cargs := voiceTest(v, lang, "t")
	for c := range slices.Chunk(ids, 500) {
		in := placeholders(len(c))
		args := slices.Concat(int64Args(c), cargs)
		if err := s.each(ctx, func(rows *sql.Rows) error {
			var id, n, first int64
			var sources string
			if err := rows.Scan(&id, &n, &first, &sources); err != nil {
				return err
			}
			sum, ok := out[id]
			if !ok {
				return nil
			}
			sum.Releases, sum.FirstSeen = n, time.Unix(first, 0).UTC()
			for src := range strings.SplitSeq(sources, ",") {
				if src != "" && !slices.Contains(sum.Sources, src) {
					sum.Sources = append(sum.Sources, src)
				}
			}
			slices.SortFunc(sum.Sources, func(a, b string) int { return slices.Index(sourceOrder, a) - slices.Index(sourceOrder, b) })
			out[id] = sum
			return nil
		}, `SELECT t.work, count(*), min(t.first_seen), coalesce(group_concat(DISTINCT t.source), '') FROM torrent_releases t
			WHERE t.work IN (`+in+`)`+and(cond)+` GROUP BY t.work`, args...); err != nil {
			return nil, err
		}
		if err := s.each(ctx, func(rows *sql.Rows) error {
			var id int64
			var r int
			if err := rows.Scan(&id, &r); err != nil {
				return err
			}
			if sum, ok := out[id]; ok {
				sum.Rank = r
				out[id] = sum
			}
			return nil
		}, "SELECT work, rank FROM stat_top WHERE window = ? AND voice = ? AND work IN ("+in+")",
			slices.Concat([]any{WindowWeek.Name, string(VoiceAny)}, int64Args(c))...); err != nil {
			return nil, err
		}
		if len(s.opts.Languages) == 0 {
			continue
		}
		langs := make([]any, len(s.opts.Languages))
		for i, l := range s.opts.Languages {
			langs[i] = l
		}
		audio := func(id int64, l string, f func(*WorkAudio)) {
			sum, ok := out[id]
			if !ok {
				return
			}
			if i := slices.IndexFunc(sum.Audio, func(a WorkAudio) bool { return a.Language == l }); i >= 0 {
				f(&sum.Audio[i])
			}
		}
		if err := s.each(ctx, func(rows *sql.Rows) error {
			var id, marked, likely int64
			var l string
			if err := rows.Scan(&id, &l, &marked, &likely); err != nil {
				return err
			}
			audio(id, l, func(a *WorkAudio) { a.Marked, a.Likely = marked, likely })
			return nil
		}, `SELECT t.work, a.language, coalesce(sum(a.marker), 0), coalesce(sum(NOT a.marker AND a.probability >= 0.5), 0)
			FROM torrent_releases t JOIN torrent_audio a ON a.torrent_id = t.id
			WHERE t.work IN (`+in+`) AND a.language IN (`+placeholders(len(langs))+`)`+and(cond)+` GROUP BY 1, 2`,
			slices.Concat(int64Args(c), langs, cargs)...); err != nil {
			return nil, err
		}
		if err := s.each(ctx, func(rows *sql.Rows) error {
			var id int64
			var l, mark string
			if err := rows.Scan(&id, &l, &mark); err != nil {
				return err
			}
			audio(id, l, func(a *WorkAudio) { a.Marks = append(a.Marks, mark) })
			return nil
		}, `SELECT t.work, a.language, a.mark FROM torrent_releases t JOIN torrent_audio a ON a.torrent_id = t.id
			WHERE t.work IN (`+in+`) AND a.language IN (`+placeholders(len(langs))+`) AND a.marker AND a.mark IS NOT NULL`+and(cond)+`
			GROUP BY 1, 2, 3 ORDER BY 1, 2, count(*) DESC, 3`, slices.Concat(int64Args(c), langs, cargs)...); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// matchedTitles is how many best text matches of titles, and of release
// names and paths, FindWorks takes works from.
const matchedTitles = 500

// FindWorks returns the IDs of the stored works of the TMDB type, or of
// either when typ is "", whose titles contain every word of the text, or
// that one of the matchedTitles best matching releases belongs to; none for
// a text without words.
func (s *Store) FindWorks(ctx context.Context, text, typ string) ([]int64, error) {
	m := matchExpr(text)
	if m == "" {
		return nil, nil
	}
	var out []int64
	err := s.each(ctx, func(rows *sql.Rows) error {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		out = append(out, id)
		return nil
	}, `WITH c AS (
			SELECT rowid AS id FROM (SELECT rowid FROM tmdb_titles_fts WHERE tmdb_titles_fts MATCH ?1 ORDER BY rank LIMIT ?2)
			UNION
			SELECT t.work FROM (SELECT rowid FROM torrent_releases_fts WHERE torrent_releases_fts MATCH ?1 ORDER BY rank LIMIT ?2) f
				JOIN torrent_releases t ON t.id = f.rowid WHERE t.work IS NOT NULL)
		SELECT w.id FROM tmdb_works w JOIN c ON c.id = w.id WHERE ?3 = '' OR w.type = ?3 ORDER BY w.id`, m, matchedTitles, typ)
	return out, err
}

// Ranks returns the work's places in the top of its kind among all voices
// over each of Windows, 0 where it is not in it.
func (s *Store) Ranks(ctx context.Context, work int64) ([3]int, error) {
	var out [3]int
	err := s.each(ctx, func(rows *sql.Rows) error {
		var w string
		var r int
		if err := rows.Scan(&w, &r); err != nil {
			return err
		}
		if i := slices.IndexFunc(Windows, func(x Window) bool { return x.Name == w }); i >= 0 {
			out[i] = r
		}
		return nil
	}, "SELECT window, rank FROM stat_top WHERE work = ? AND voice = ?", work, string(VoiceAny))
	return out, err
}

// WorkPeers returns the distinct peers of all the work's releases over each
// window ending at now.
func (s *Store) WorkPeers(ctx context.Context, work int64, now time.Time) (PeerCounts, error) {
	var out [3]int64
	for i, w := range Windows {
		h, d := w.since(now)
		if err := s.r.QueryRowContext(ctx, `SELECT coalesce(hll_card(hll_union_agg(sketch)), 0) FROM (
				SELECT p.sketch FROM stat_peers_by_hour p JOIN torrent_releases t ON t.id = p.torrent_id WHERE t.work = ?1 AND p.hour >= ?2
				UNION ALL SELECT p.sketch FROM stat_peers_by_day p JOIN torrent_releases t ON t.id = p.torrent_id WHERE t.work = ?1 AND p.day >= ?3)`,
			work, h, d).Scan(&out[i]); err != nil {
			return PeerCounts{}, err
		}
	}
	return PeerCounts{Day: out[0], Week: out[1], Month: out[2]}, nil
}

// ReleaseFilter selects releases of a work for WorkReleases.
type ReleaseFilter struct {
	// Quality and Source, unless "", select releases of the quality and of
	// the source.
	Quality string
	Source  string
	// MinSize and MaxSize, unless 0, bound the releases' size in bytes.
	MinSize, MaxSize int64
	// Voice narrows the releases by their audio in AudioLanguage.
	AudioLanguage string
	Voice         Voice
	// Season and Episode, unless 0, select releases of the season and the
	// episode; a whole season has no episode.
	Season, Episode int
	// Sort is SortPeers, the default, SortFirstSeen or SortSize.
	Sort          string
	Offset, Limit int
}

// ReleasePage is a page of a work's releases.
type ReleasePage struct {
	Releases []Release
	// Total counts the work's releases, Matched those the filter selects.
	Total, Matched int64
	// Seasons are the seasons among the work's releases, in order.
	Seasons []int
}

// WorkReleases returns the page of the work's releases f selects, with their
// audio.
func (s *Store) WorkReleases(ctx context.Context, work int64, f ReleaseFilter) (ReleasePage, error) {
	var p ReleasePage
	if err := s.r.QueryRowContext(ctx, "SELECT count(*) FROM torrent_releases WHERE work = ?", work).Scan(&p.Total); err != nil {
		return ReleasePage{}, err
	}
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var n int
		if err := rows.Scan(&n); err != nil {
			return err
		}
		p.Seasons = append(p.Seasons, n)
		return nil
	}, "SELECT DISTINCT season FROM torrent_releases WHERE work = ? AND season IS NOT NULL ORDER BY season", work); err != nil {
		return ReleasePage{}, err
	}
	where := []string{"t.work = ?"}
	args := []any{work}
	add := func(cond string, a ...any) {
		where = append(where, cond)
		args = append(args, a...)
	}
	if f.Quality != "" {
		add("t.quality = ?", f.Quality)
	}
	if f.Source != "" {
		add("t.source = ?", f.Source)
	}
	if f.MinSize > 0 {
		add("t.size >= ?", f.MinSize)
	}
	if f.MaxSize > 0 {
		add("t.size <= ?", f.MaxSize)
	}
	if f.Season > 0 {
		add("t.season = ?", f.Season)
	}
	if f.Episode > 0 {
		add("t.episode = ?", f.Episode)
	}
	if cond, cargs := voiceTest(f.Voice, f.AudioLanguage, "t"); cond != "" {
		add(cond, cargs...)
	}
	cond := " WHERE " + strings.Join(where, " AND ")
	if err := s.r.QueryRowContext(ctx, "SELECT count(*) FROM torrent_releases t"+cond, args...).Scan(&p.Matched); err != nil {
		return ReleasePage{}, err
	}
	order, ok := orders[f.Sort]
	if !ok || f.Sort == SortLastSeen {
		order = orders[SortPeers]
	}
	if err := s.each(ctx, func(rows *sql.Rows) error {
		r, err := scanRelease(rows)
		if err != nil {
			return err
		}
		p.Releases = append(p.Releases, r)
		return nil
	}, "SELECT "+releaseColumns+" FROM torrent_releases t"+cond+" ORDER BY "+order+" LIMIT ? OFFSET ?", append(args, f.Limit, f.Offset)...); err != nil {
		return ReleasePage{}, err
	}
	return p, s.audio(ctx, p.Releases)
}
