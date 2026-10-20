package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
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
	return "EXISTS (SELECT 1 FROM audio a WHERE a.torrent_id = " + t + ".id AND a.language = ? AND " + test + ")", []any{s.opts.Languages[0]}
}

// and returns " AND cond" or "".
func and(cond string) string {
	if cond == "" {
		return ""
	}
	return " AND " + cond
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
			SELECT torrent_id, sketch FROM peers_hour WHERE hour >= ?1
			UNION ALL SELECT torrent_id, sketch FROM peers_day WHERE day >= ?2) GROUP BY torrent_id`, h, d); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE prev AS SELECT torrent_id, hll_union_agg(sketch) AS sketch FROM (
			SELECT torrent_id, sketch FROM peers_hour WHERE hour >= ?1 AND hour < ?2
			UNION ALL SELECT torrent_id, sketch FROM peers_day WHERE day >= ?3 AND day < ?4) GROUP BY torrent_id`, ph, h, pd, d); err != nil {
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
						(SELECT count(*) FROM torrents r WHERE r.work_key = t.work_key AND r.kind = t.kind` + and(rcond) + `)
					FROM win w JOIN torrents t ON t.id = w.torrent_id
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
				res, err = conn.QueryContext(ctx, `SELECT t.work_key, hll_card(hll_union_agg(p.sketch)) FROM prev p JOIN torrents t ON t.id = p.torrent_id
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
		if _, err := tx.ExecContext(ctx, "DELETE FROM top"); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, "INSERT INTO top (kind, window, voice, rank, work_key, peers, delta, releases, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
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
	if _, err := tx.ExecContext(ctx, "UPDATE torrents SET peers_week = 0 WHERE peers_week != 0 AND id NOT IN (SELECT id FROM pw)"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE torrents SET peers_week = pw.c FROM pw WHERE torrents.id = pw.id AND torrents.peers_week != pw.c"); err != nil {
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
	if err := s.r.QueryRowContext(ctx, "SELECT count(*), max(computed_at) FROM top WHERE kind = ? AND window = ? AND voice = ?", kind, w.Name, string(v)).Scan(&total, &computed); err != nil {
		return nil, 0, time.Time{}, err
	}
	var at time.Time
	if computed.Valid {
		at = time.Unix(computed.Int64, 0).UTC()
	}
	rows, err := s.r.QueryContext(ctx, "SELECT rank, work_key, peers, delta, releases FROM top WHERE kind = ? AND window = ? AND voice = ? ORDER BY rank LIMIT ? OFFSET ?",
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
	}, fmt.Sprintf("SELECT work_key, rank FROM top WHERE kind = ? AND window = ? AND voice = ? AND work_key IN (%s)", placeholders(len(keys))),
		slices.Concat([]any{kind, WindowWeek.Name, string(VoiceAny)}, keys)...)
	return ranks, err
}
