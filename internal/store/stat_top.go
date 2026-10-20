package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mikluko/sniffarr/internal/names"
)

// TopSize is how many works a top holds.
const TopSize = 100

// TopKinds are the kinds that have tops.
var TopKinds = []string{"movie", "series", "music"}

// Voice narrows a work's releases by their audio in the first language of
// Options.Languages.
type Voice string

const (
	// VoiceAny admits every release.
	VoiceAny Voice = "any"
	// VoiceLikely admits releases whose probability of an audio track in the
	// language is known and at least 0.5.
	VoiceLikely Voice = "p50"
	// VoiceMarker admits releases with a marker of the language.
	VoiceMarker Voice = "explicit"
)

// Voices are the voices tops are computed for.
var Voices = []Voice{VoiceAny, VoiceLikely, VoiceMarker}

// TopEntry is one work of a top.
type TopEntry struct {
	Rank  int
	Work  Work
	Peers int64
	// Delta is the change of Peers against the window before, as a fraction
	// of the earlier count; nil when the work had no peers then.
	Delta *float64
}

// voiceCond returns the SQL condition admitting the torrents of alias t
// under v, "" for every torrent, with its arguments. Without a language the
// narrowing voices admit none.
func (s *Store) voiceCond(v Voice, t string) (string, []any) {
	var test string
	switch v {
	case VoiceAny:
		return "", nil
	case VoiceLikely:
		test = "a.probability >= 0.5"
	case VoiceMarker:
		test = "a.marker"
	}
	if len(s.opts.Languages) == 0 {
		return "0", nil
	}
	return "EXISTS (SELECT 1 FROM torrent_audio a WHERE a.torrent_id = " + t + ".id AND a.language = ? AND " + test + ")", []any{s.opts.Languages[0]}
}

// ComputeTop recomputes the tops of every kind, window and voice at now,
// TopSize works each, and the PeersWeek of every torrent.
func (s *Store) ComputeTop(ctx context.Context, now time.Time) error {
	conn, err := s.r.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	type row struct {
		kind     string
		window   string
		voice    Voice
		rank     int
		work     string
		peers    int64
		delta    *float64
		releases int64
	}
	var rows []row
	var week map[int64]int64
	for _, w := range Windows {
		h, d := w.since(now)
		ph, pd := h-int64(w.Length/time.Second), d-int64(w.Length/time.Second)
		if _, err := conn.ExecContext(ctx, "DROP TABLE IF EXISTS temp.win; DROP TABLE IF EXISTS temp.prev"); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE win AS SELECT torrent_id, hll_union_agg(sketch) AS sketch FROM (
			SELECT torrent_id, sketch FROM stat_peers_by_hour WHERE hour >= ?1
			UNION ALL SELECT torrent_id, sketch FROM stat_peers_by_day WHERE day >= ?2) GROUP BY torrent_id`, h, d); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE prev AS SELECT torrent_id, hll_union_agg(sketch) AS sketch FROM (
			SELECT torrent_id, sketch FROM stat_peers_by_hour WHERE hour >= ?1 AND hour < ?2
			UNION ALL SELECT torrent_id, sketch FROM stat_peers_by_day WHERE day >= ?3 AND day < ?4) GROUP BY torrent_id`, ph, h, pd, d); err != nil {
			return err
		}
		if w == WindowWeek {
			if week, err = weekPeers(ctx, conn); err != nil {
				return err
			}
		}
		for _, kind := range TopKinds {
			for _, v := range Voices {
				cond, args := s.voiceCond(v, "t")
				rcond, rargs := s.voiceCond(v, "r")
				q := `SELECT t.work_key, hll_card(hll_union_agg(w.sketch)) AS peers,
						(SELECT count(*) FROM torrent_releases r WHERE r.work_key = t.work_key AND r.kind = t.kind` + and(rcond) + `)
					FROM win w JOIN torrent_releases t ON t.id = w.torrent_id
					WHERE t.kind = ? AND t.work_key IS NOT NULL` + and(cond) + `
					GROUP BY t.work_key ORDER BY peers DESC, t.work_key LIMIT ?`
				res, err := conn.QueryContext(ctx, q, slices.Concat(rargs, []any{kind}, args, []any{TopSize})...)
				if err != nil {
					return err
				}
				var top []row
				for res.Next() {
					r := row{kind: kind, window: w.Name, voice: v}
					if err := res.Scan(&r.work, &r.peers, &r.releases); err != nil {
						res.Close()
						return err
					}
					r.rank = len(top) + 1
					top = append(top, r)
				}
				if err := errors.Join(res.Err(), res.Close()); err != nil {
					return err
				}
				if len(top) == 0 {
					continue
				}
				keys := make([]any, len(top))
				for i, r := range top {
					keys[i] = r.work
				}
				before := map[string]int64{}
				res, err = conn.QueryContext(ctx, `SELECT t.work_key, hll_card(hll_union_agg(p.sketch)) FROM prev p JOIN torrent_releases t ON t.id = p.torrent_id
					WHERE t.kind = ? AND t.work_key IN (`+placeholders(len(keys))+`)`+and(cond)+` GROUP BY t.work_key`,
					slices.Concat([]any{kind}, keys, args)...)
				if err != nil {
					return err
				}
				for res.Next() {
					var k string
					var n int64
					if err := res.Scan(&k, &n); err != nil {
						res.Close()
						return err
					}
					before[k] = n
				}
				if err := errors.Join(res.Err(), res.Close()); err != nil {
					return err
				}
				for i := range top {
					if b := before[top[i].work]; b > 0 {
						top[i].delta = new(float64(top[i].peers-b) / float64(b))
					}
				}
				rows = append(rows, top...)
			}
		}
		if _, err := conn.ExecContext(ctx, "DROP TABLE win; DROP TABLE prev"); err != nil {
			return err
		}
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM stat_top"); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, "INSERT INTO stat_top (kind, window, voice, rank, work_key, peers, delta, releases, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
				r.kind, r.window, string(r.voice), r.rank, r.work, r.peers, r.delta, r.releases, now.Unix()); err != nil {
				return err
			}
		}
		return writeWeekPeers(ctx, tx, week)
	})
}

// weekPeers returns the cardinality of every torrent's sketch in the win
// table of conn.
func weekPeers(ctx context.Context, conn *sql.Conn) (map[int64]int64, error) {
	rows, err := conn.QueryContext(ctx, "SELECT torrent_id, hll_card(sketch) FROM win")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// writeWeekPeers sets peers_week to week's counts and to 0 for every other
// torrent, touching only the rows that change.
func writeWeekPeers(ctx context.Context, tx *sql.Tx, week map[int64]int64) error {
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE pw (id INTEGER PRIMARY KEY, c INTEGER NOT NULL)"); err != nil {
		return err
	}
	ids := make([]int64, 0, len(week))
	for id := range week {
		ids = append(ids, id)
	}
	for c := range slices.Chunk(ids, 500) {
		args := make([]any, 0, 2*len(c))
		for _, id := range c {
			args = append(args, id, week[id])
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO pw (id, c) VALUES "+strings.TrimSuffix(strings.Repeat("(?, ?),", len(c)), ","), args...); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET peers_week = 0 WHERE peers_week != 0 AND id NOT IN (SELECT id FROM pw)"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET peers_week = pw.c FROM pw WHERE torrent_releases.id = pw.id AND torrent_releases.peers_week != pw.c"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DROP TABLE pw")
	return err
}

// Top returns, from offset, up to limit entries of the top of the kind,
// window and voice, how many entries it has, and when it was computed, the
// zero time while it never was.
func (s *Store) Top(ctx context.Context, kind string, w Window, v Voice, limit, offset int) ([]TopEntry, int64, time.Time, error) {
	var total int64
	var computed sql.NullInt64
	if err := s.r.QueryRowContext(ctx, "SELECT count(*), max(computed_at) FROM stat_top WHERE kind = ? AND window = ? AND voice = ?", kind, w.Name, string(v)).Scan(&total, &computed); err != nil {
		return nil, 0, time.Time{}, err
	}
	var at time.Time
	if computed.Valid {
		at = time.Unix(computed.Int64, 0).UTC()
	}
	rows, err := s.r.QueryContext(ctx, "SELECT rank, work_key, peers, delta, releases FROM stat_top WHERE kind = ? AND window = ? AND voice = ? ORDER BY rank LIMIT ? OFFSET ?",
		kind, w.Name, string(v), limit, offset)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	var out []TopEntry
	var keys []string
	for rows.Next() {
		var e TopEntry
		var delta sql.NullFloat64
		if err := rows.Scan(&e.Rank, &e.Work.Key, &e.Peers, &delta, &e.Work.Releases); err != nil {
			rows.Close()
			return nil, 0, time.Time{}, err
		}
		if delta.Valid {
			e.Delta = &delta.Float64
		}
		out = append(out, e)
		keys = append(keys, e.Work.Key)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, 0, time.Time{}, err
	}
	works, err := s.works(ctx, kind, keys, v)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	for i := range out {
		if w, ok := works[out[i].Work.Key]; ok {
			out[i].Work = w
		}
	}
	return out, total, at, nil
}

// rankIn returns the rank of each key in the 7-day top of the kind among all
// voices.
func (s *Store) rankIn(ctx context.Context, kind string, keys []any) (map[string]int, error) {
	ranks := map[string]int{}
	if kind == "" || len(keys) == 0 {
		return ranks, nil
	}
	err := s.each(ctx, func(rows *sql.Rows) error {
		var k string
		var r int
		if err := rows.Scan(&k, &r); err != nil {
			return err
		}
		ranks[k] = r
		return nil
	}, fmt.Sprintf("SELECT work_key, rank FROM stat_top WHERE kind = ? AND window = ? AND voice = ? AND work_key IN (%s)", placeholders(len(keys))),
		slices.Concat([]any{kind, WindowWeek.Name, string(VoiceAny)}, keys)...)
	return ranks, err
}

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
		FROM torrent_releases t`+where+` ORDER BY t.work_key, t.peers_week DESC, t.id DESC`, args...); err != nil {
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
			FROM torrent_releases t JOIN torrent_audio a ON a.torrent_id = t.id`+where+` AND a.language IN (`+placeholders(len(langs))+`)
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
	err := s.r.QueryRowContext(ctx, "SELECT kind FROM torrent_releases WHERE work_key = ? AND kind IN ('movie', 'series', 'music') GROUP BY kind ORDER BY count(*) DESC, kind LIMIT 1", key).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return kind, err
}

// Work returns the work with the key over the window ending at now, with
// the releases v admits; it returns ErrNotFound when no torrent has the key.
func (s *Store) Work(ctx context.Context, key string, w Window, v Voice, now time.Time) (WorkView, error) {
	var exists bool
	if err := s.r.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM torrent_releases WHERE work_key = ?)", key).Scan(&exists); err != nil {
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
		err := s.r.QueryRowContext(ctx, "SELECT rank, delta FROM stat_top WHERE kind = ? AND window = ? AND voice = ? AND work_key = ?", kind, w.Name, string(v), key).Scan(&view.Rank, &delta)
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
			SELECT p.sketch FROM stat_peers_by_hour p JOIN torrent_releases t ON t.id = p.torrent_id`+where+` AND p.hour >= ?
			UNION ALL SELECT p.sketch FROM stat_peers_by_day p JOIN torrent_releases t ON t.id = p.torrent_id`+where+` AND p.day >= ?)`,
		slices.Concat(args, []any{h}, args, []any{d})...).Scan(&view.Peers); err != nil {
		return WorkView{}, err
	}
	rows, err := s.r.QueryContext(ctx, "SELECT "+releaseColumns+", "+peersOf+" AS peers FROM torrent_releases t"+where+" ORDER BY peers DESC, t.id DESC",
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
	if err := errors.Join(s.audio(ctx, rs), s.tmdb(ctx, rs)); err != nil {
		return WorkView{}, err
	}
	for i := range view.Releases {
		view.Releases[i].Release = rs[i]
	}
	return view, nil
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
	}, `WITH m AS (SELECT t.work_key, t.kind FROM torrent_releases_fts f JOIN torrent_releases t ON t.id = f.rowid
			WHERE torrent_releases_fts MATCH ? AND t.work_key IS NOT NULL AND t.kind IN ('movie', 'series', 'music') ORDER BY f.rank LIMIT ?),
		w AS (SELECT DISTINCT work_key, kind FROM m)
		SELECT w.work_key, w.kind, coalesce(hll_card(hll_union_agg(p.sketch)), 0) AS peers
		FROM w JOIN torrent_releases t ON t.work_key = w.work_key AND t.kind = w.kind
		LEFT JOIN stat_peers_by_hour p ON p.torrent_id = t.id AND p.hour >= ?
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
	if err := s.r.QueryRowContext(ctx, "SELECT count(*), coalesce(sum(first_seen > ?), 0) FROM torrent_releases", now.Add(-24*time.Hour).Unix()).Scan(&h.Total, &h.NewDay); err != nil {
		return Home{}, err
	}
	var err error
	h.Pending, err = s.pendingCount(ctx)
	return h, err
}
