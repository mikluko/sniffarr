package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/mikluko/sniffarr/internal/names"
)

// Work is a work with what is known of its releases together.
type Work struct {
	Key string
	// Kind is the kind of the work's releases, "" while undecided.
	Kind string
	// Title is the title of the release with the most PeersWeek, as its name
	// gives it; Year and Season are that release's.
	Title  string
	Year   int
	Season int
	// Rank is the work's place in the 7-day top of its kind among all
	// voices, 0 when it is not in it.
	Rank int
	// Releases counts the stored releases the voice admits.
	Releases int64
	// Audio holds, per language of Options.Languages, the highest
	// probability among the releases and whether any has the marker or the
	// script hint.
	Audio []Audio
	// Sources are the releases' sources, each once, in enum order.
	Sources   []string
	FirstSeen time.Time
}

// WorkMatch is a work among the releases a query matches.
type WorkMatch struct {
	Work  Work
	Peers int64
}

// WorkRelease is a release of a work with its peers over a window.
type WorkRelease struct {
	Release
	Peers int64
}

// WorkView is a work as Work returns it for one window and voice.
type WorkView struct {
	Work Work
	// Peers are the distinct peers of the admitted releases over the window.
	Peers int64
	// Rank and Delta are the work's TopEntry in the top of its kind for the
	// window and voice; Rank is 0 and Delta nil when it is not in it.
	Rank  int
	Delta *float64
	// Releases are the admitted releases, most Peers first.
	Releases []WorkRelease
}

// sourceOrder is the order of Work.Sources.
var sourceOrder = []string{"disc_image", "remux", "encode", "web", "other"}

// works returns the works with the keys, among the torrents of the kind, or
// of any kind when kind is "", that v admits; a key with no such torrent is
// absent.
func (s *Store) works(ctx context.Context, kind string, keys []string, v Voice) (map[string]Work, error) {
	out := map[string]Work{}
	if len(keys) == 0 {
		return out, nil
	}
	cond, cargs := s.voiceCond(v, "t")
	in := make([]any, len(keys))
	for i, k := range keys {
		in[i] = k
	}
	where := " WHERE t.work_key IN (" + placeholders(len(keys)) + ")" + and(cond)
	args := slices.Concat(in, cargs)
	if kind != "" {
		where += " AND t.kind = ?"
		args = append(args, kind)
	}
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var key, name, source string
		var year, season int
		var first int64
		if err := rows.Scan(&key, &name, &year, &season, &first, &source); err != nil {
			return err
		}
		w, ok := out[key]
		if !ok {
			w = Work{Key: key, Kind: kind, Year: year, Season: season, FirstSeen: time.Unix(first, 0).UTC()}
			if t := names.Titles(name); len(t) > 0 {
				w.Title = t[0]
			} else {
				w.Title = name
			}
		}
		w.Releases++
		if at := time.Unix(first, 0).UTC(); at.Before(w.FirstSeen) {
			w.FirstSeen = at
		}
		if source != "" && !slices.Contains(w.Sources, source) {
			w.Sources = append(w.Sources, source)
		}
		out[key] = w
		return nil
	}, `SELECT t.work_key, t.name, coalesce(t.year, 0), coalesce(t.season, 0), t.first_seen, coalesce(t.source, '')
		FROM torrents t`+where+` ORDER BY t.work_key, t.peers_week DESC, t.id DESC`, args...); err != nil {
		return nil, err
	}
	for k, w := range out {
		slices.SortFunc(w.Sources, func(a, b string) int { return slices.Index(sourceOrder, a) - slices.Index(sourceOrder, b) })
		out[k] = w
	}
	if len(s.opts.Languages) > 0 {
		langs := make([]any, len(s.opts.Languages))
		for i, l := range s.opts.Languages {
			langs[i] = l
		}
		if err := s.each(ctx, func(rows *sql.Rows) error {
			var key string
			var a Audio
			var p sql.NullFloat64
			if err := rows.Scan(&key, &a.Language, &p, &a.Marker, &a.ScriptHint); err != nil {
				return err
			}
			if p.Valid {
				a.Probability = &p.Float64
			}
			w := out[key]
			w.Audio = append(w.Audio, a)
			out[key] = w
			return nil
		}, `SELECT t.work_key, a.language, max(a.probability), max(a.marker), max(a.script_hint)
			FROM torrents t JOIN audio a ON a.torrent_id = t.id`+where+` AND a.language IN (`+placeholders(len(langs))+`)
			GROUP BY t.work_key, a.language ORDER BY t.work_key, a.language`, slices.Concat(args, langs)...); err != nil {
			return nil, err
		}
	}
	ranks, err := s.rankIn(ctx, kind, in)
	if err != nil {
		return nil, err
	}
	for k, r := range ranks {
		if w, ok := out[k]; ok {
			w.Rank = r
			out[k] = w
		}
	}
	return out, nil
}

// workKind returns the kind most of the key's torrents are, among TopKinds,
// or "" when none is.
func (s *Store) workKind(ctx context.Context, key string) (string, error) {
	var kind string
	err := s.r.QueryRowContext(ctx, "SELECT kind FROM torrents WHERE work_key = ? AND kind IN ('movie', 'series', 'music') GROUP BY kind ORDER BY count(*) DESC, kind LIMIT 1", key).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return kind, err
}

// Work returns the work with the key over the window ending at now, with
// the releases v admits; it returns ErrNotFound when no torrent has the key.
func (s *Store) Work(ctx context.Context, key string, w Window, v Voice, now time.Time) (WorkView, error) {
	var exists bool
	if err := s.r.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM torrents WHERE work_key = ?)", key).Scan(&exists); err != nil {
		return WorkView{}, err
	}
	if !exists {
		return WorkView{}, ErrNotFound
	}
	kind, err := s.workKind(ctx, key)
	if err != nil {
		return WorkView{}, err
	}
	works, err := s.works(ctx, kind, []string{key}, v)
	if err != nil {
		return WorkView{}, err
	}
	work, ok := works[key]
	if !ok {
		work = Work{Key: key, Kind: kind}
		if all, err := s.works(ctx, kind, []string{key}, VoiceAny); err != nil {
			return WorkView{}, err
		} else if w, ok := all[key]; ok {
			work.Title, work.Year, work.Season, work.Rank = w.Title, w.Year, w.Season, w.Rank
		}
	}
	view := WorkView{Work: work}
	if kind != "" {
		var delta sql.NullFloat64
		err := s.r.QueryRowContext(ctx, "SELECT rank, delta FROM top WHERE kind = ? AND window = ? AND voice = ? AND work_key = ?", kind, w.Name, string(v), key).Scan(&view.Rank, &delta)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return WorkView{}, err
		}
		if delta.Valid {
			view.Delta = &delta.Float64
		}
	}
	cond, cargs := s.voiceCond(v, "t")
	where := " WHERE t.work_key = ?" + and(cond)
	args := slices.Concat([]any{key}, cargs)
	if kind != "" {
		where += " AND t.kind = ?"
		args = append(args, kind)
	}
	h, d := w.since(now)
	if err := s.r.QueryRowContext(ctx, `SELECT coalesce(hll_card(hll_union_agg(sketch)), 0) FROM (
			SELECT p.sketch FROM peers_hour p JOIN torrents t ON t.id = p.torrent_id`+where+` AND p.hour >= ?
			UNION ALL SELECT p.sketch FROM peers_day p JOIN torrents t ON t.id = p.torrent_id`+where+` AND p.day >= ?)`,
		slices.Concat(args, []any{h}, args, []any{d})...).Scan(&view.Peers); err != nil {
		return WorkView{}, err
	}
	rows, err := s.r.QueryContext(ctx, "SELECT "+releaseColumns+", "+peersOf+" AS peers FROM torrents t"+where+" ORDER BY peers DESC, t.id DESC",
		slices.Concat([]any{h, d}, args)...)
	if err != nil {
		return WorkView{}, err
	}
	var rs []Release
	for rows.Next() {
		var r WorkRelease
		var err error
		if r.Release, r.Peers, err = scanReleasePeers(rows); err != nil {
			rows.Close()
			return WorkView{}, err
		}
		view.Releases = append(view.Releases, r)
		rs = append(rs, r.Release)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return WorkView{}, err
	}
	if err := errors.Join(s.audio(ctx, rs), s.titles(ctx, rs)); err != nil {
		return WorkView{}, err
	}
	for i := range view.Releases {
		view.Releases[i].Release = rs[i]
	}
	return view, nil
}

// scanReleasePeers scans releaseColumns followed by a peer count.
func scanReleasePeers(rows *sql.Rows) (Release, int64, error) {
	var r Release
	var hash []byte
	var first, last, peers int64
	err := rows.Scan(&r.ID, &hash, &r.Name, &r.Size, &r.FileCount, &r.Kind, &r.KindLayer, &r.KindConfidence, &r.Source, &r.SourceConfidence,
		&r.Classified, &first, &last, &r.TitleKey, &r.WorkKey, &r.Year, &r.Season, &r.PeersWeek, &peers)
	if err != nil {
		return Release{}, 0, err
	}
	r.Hash = Hash(hash)
	r.FirstSeen = time.Unix(first, 0).UTC()
	r.LastSeen = time.Unix(last, 0).UTC()
	return r, peers, nil
}

// matchedWorks is how many best text matches Works groups into works.
const matchedWorks = 500

// Works returns up to limit works of TopKinds among the matchedWorks best
// matches of the text, most distinct peers over the week ending at now
// first; none for a text without words.
func (s *Store) Works(ctx context.Context, text string, now time.Time, limit int) ([]WorkMatch, error) {
	m := matchExpr(text)
	if m == "" {
		return nil, nil
	}
	h, _ := WindowWeek.since(now)
	type match struct {
		key, kind string
		peers     int64
	}
	var found []match
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var f match
		if err := rows.Scan(&f.key, &f.kind, &f.peers); err != nil {
			return err
		}
		found = append(found, f)
		return nil
	}, `WITH m AS (SELECT t.work_key, t.kind FROM torrents_fts f JOIN torrents t ON t.id = f.rowid
			WHERE torrents_fts MATCH ? AND t.work_key IS NOT NULL AND t.kind IN ('movie', 'series', 'music') ORDER BY f.rank LIMIT ?),
		w AS (SELECT DISTINCT work_key, kind FROM m)
		SELECT w.work_key, w.kind, coalesce(hll_card(hll_union_agg(p.sketch)), 0) AS peers
		FROM w JOIN torrents t ON t.work_key = w.work_key AND t.kind = w.kind
		LEFT JOIN peers_hour p ON p.torrent_id = t.id AND p.hour >= ?
		GROUP BY w.work_key, w.kind ORDER BY peers DESC, w.work_key LIMIT ?`, m, matchedWorks, h, limit); err != nil {
		return nil, err
	}
	byKind := map[string][]string{}
	for _, f := range found {
		byKind[f.kind] = append(byKind[f.kind], f.key)
	}
	works := map[string]map[string]Work{}
	for kind, keys := range byKind {
		var err error
		if works[kind], err = s.works(ctx, kind, keys, VoiceAny); err != nil {
			return nil, err
		}
	}
	out := make([]WorkMatch, 0, len(found))
	for _, f := range found {
		if w, ok := works[f.kind][f.key]; ok {
			out = append(out, WorkMatch{Work: w, Peers: f.peers})
		}
	}
	return out, nil
}

// Home is what the home page shows.
type Home struct {
	Total int64
	// NewDay counts the torrents first seen in the last 24 hours.
	NewDay int64
	// Pending counts the torrents Unclassified selects.
	Pending int64
}

// Home returns the home counts at now.
func (s *Store) Home(ctx context.Context, now time.Time) (Home, error) {
	var h Home
	if err := s.r.QueryRowContext(ctx, "SELECT count(*), coalesce(sum(first_seen > ?), 0) FROM torrents", now.Add(-24*time.Hour).Unix()).Scan(&h.Total, &h.NewDay); err != nil {
		return Home{}, err
	}
	var err error
	h.Pending, err = s.pendingCount(ctx)
	return h, err
}

// pendingCount counts the torrents Unclassified selects.
func (s *Store) pendingCount(ctx context.Context) (int64, error) {
	if len(s.opts.Languages) == 0 {
		return 0, nil
	}
	args := make([]any, len(s.opts.Languages))
	for i, l := range s.opts.Languages {
		args[i] = l
	}
	var n int64
	err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM torrents t
		WHERE t.classified_at IS NULL AND t.kind IS NULL AND EXISTS (SELECT 1 FROM audio a WHERE a.torrent_id = t.id
			AND a.language IN (`+placeholders(len(args))+`) AND (a.marker OR a.script_hint))`, args...).Scan(&n)
	return n, err
}
