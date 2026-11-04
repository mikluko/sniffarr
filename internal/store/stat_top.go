package store

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"
)

// TopSize is how many works a top holds.
const TopSize = 100

// TopKinds are the kinds that have tops.
var TopKinds = []string{"movie", "series"}

// Voice narrows a work's releases by their audio in one language.
type Voice string

const (
	// VoiceAny admits every release.
	VoiceAny Voice = "any"
	// VoiceLikely admits releases with a marker of the language, and those
	// whose probability of an audio track in it is known and at least 0.5.
	VoiceLikely Voice = "p50"
	// VoiceMarker admits releases with a marker of the language.
	VoiceMarker Voice = "explicit"
)

// Voices are the voices tops are computed for.
var Voices = []Voice{VoiceAny, VoiceLikely, VoiceMarker}

// TopEntry is one work of a top.
type TopEntry struct {
	Rank int
	// Work counts, sums up and dates the releases the voice admits.
	Work  Summary
	Peers int64
	// PrevRank is the work's place in the window of the same length before,
	// under the same kind and voice, among every work with a peer then; 0
	// when it had none.
	PrevRank int
}

// voiceTest returns the SQL condition admitting the torrents of alias t
// under v in the language, "" for every torrent, with its arguments.
func voiceTest(v Voice, lang, t string) (string, []any) {
	var test string
	switch v {
	case VoiceLikely:
		test = "(a.marker OR a.probability >= 0.5)"
	case VoiceMarker:
		test = "a.marker"
	default:
		return "", nil
	}
	return "EXISTS (SELECT 1 FROM torrent_audio a WHERE a.torrent_id = " + t + ".id AND a.language = ? AND " + test + ")", []any{lang}
}

// voiceKey is stat_top's voice of v in the language.
func voiceKey(v Voice, lang string) string {
	if v == VoiceAny || v == "" {
		return string(VoiceAny)
	}
	return lang + ":" + string(v)
}

// ComputeTop recomputes at now the tops of every kind, window, and voice in
// each of Options.Languages, TopSize works each, and the PeersWeek of every
// torrent and every work.
func (s *Store) ComputeTop(ctx context.Context, now time.Time) error {
	conn, err := s.r.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	type row struct {
		kind     string
		window   string
		voice    string
		rank     int
		work     int64
		peers    int64
		prev     int
		releases int64
	}
	type voice struct {
		v    Voice
		lang string
	}
	voices := []voice{{v: VoiceAny}}
	for _, l := range s.opts.Languages {
		voices = append(voices, voice{VoiceLikely, l}, voice{VoiceMarker, l})
	}
	var rows []row
	var week, works map[int64]int64
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
			if week, err = counts(ctx, conn, "SELECT torrent_id, hll_card(sketch) FROM win"); err != nil {
				return err
			}
			if works, err = counts(ctx, conn, `SELECT t.work, hll_card(hll_union_agg(w.sketch)) FROM win w JOIN torrent_releases t ON t.id = w.torrent_id
				WHERE t.work IS NOT NULL GROUP BY t.work`); err != nil {
				return err
			}
		}
		for _, kind := range TopKinds {
			for _, vc := range voices {
				cond, args := voiceTest(vc.v, vc.lang, "t")
				rcond, rargs := voiceTest(vc.v, vc.lang, "r")
				res, err := conn.QueryContext(ctx, `SELECT t.work, hll_card(hll_union_agg(w.sketch)) AS peers,
						(SELECT count(*) FROM torrent_releases r WHERE r.work = t.work`+and(rcond)+`)
					FROM win w JOIN torrent_releases t ON t.id = w.torrent_id JOIN tmdb_works k ON k.id = t.work
					WHERE k.type = ?`+and(cond)+`
					GROUP BY t.work ORDER BY peers DESC, t.work LIMIT ?`, slices.Concat(rargs, []any{TypeOf(kind)}, args, []any{TopSize})...)
				if err != nil {
					return err
				}
				var top []row
				for res.Next() {
					r := row{kind: kind, window: w.Name, voice: voiceKey(vc.v, vc.lang)}
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
				before, err := ranks(ctx, conn, `SELECT t.work FROM prev p JOIN torrent_releases t ON t.id = p.torrent_id JOIN tmdb_works k ON k.id = t.work
					WHERE k.type = ?`+and(cond)+`
					GROUP BY t.work HAVING hll_card(hll_union_agg(p.sketch)) > 0 ORDER BY hll_card(hll_union_agg(p.sketch)) DESC, t.work`,
					slices.Concat([]any{TypeOf(kind)}, args)...)
				if err != nil {
					return err
				}
				for i := range top {
					top[i].prev = before[top[i].work]
				}
				rows = append(rows, top...)
			}
		}
		if _, err := conn.ExecContext(ctx, "DROP TABLE win; DROP TABLE prev"); err != nil {
			return err
		}
	}
	rc, err := peersWeekChanges(ctx, conn, "torrent_releases", week)
	if err != nil {
		return err
	}
	wc, err := peersWeekChanges(ctx, conn, "tmdb_works", works)
	if err != nil {
		return err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM stat_top"); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, "INSERT INTO stat_top (kind, window, voice, rank, work, peers, prev_rank, releases, computed_at) VALUES (?, ?, ?, ?, ?, ?, nullif(?, 0), ?, ?)",
				r.kind, r.window, r.voice, r.rank, r.work, r.peers, r.prev, r.releases, now.Unix()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.writePeersWeek(ctx, "torrent_releases", rc); err != nil {
		return err
	}
	return s.writePeersWeek(ctx, "tmdb_works", wc)
}

// ranks returns, by the IDs the query selects on conn, their places in its
// order, 1 first.
func ranks(ctx context.Context, conn *sql.Conn, query string, args ...any) (map[int64]int, error) {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = len(out) + 1
	}
	return out, rows.Err()
}

// counts returns the pairs of IDs and counts the query selects on conn.
func counts(ctx context.Context, conn *sql.Conn, query string, args ...any) (map[int64]int64, error) {
	rows, err := conn.QueryContext(ctx, query, args...)
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

// peersWeekChanges returns, by id, the peers_week each row of the table
// should hold and does not: the count by id, or 0 for a row counted no more.
func peersWeekChanges(ctx context.Context, conn *sql.Conn, table string, week map[int64]int64) (map[int64]int64, error) {
	changes := maps.Clone(week)
	if changes == nil {
		changes = map[int64]int64{}
	}
	rows, err := conn.QueryContext(ctx, "SELECT id, peers_week FROM "+table+" WHERE peers_week != 0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, c int64
		if err := rows.Scan(&id, &c); err != nil {
			return nil, err
		}
		if w, ok := week[id]; !ok {
			changes[id] = 0
		} else if w == c {
			delete(changes, id)
		}
	}
	return changes, rows.Err()
}

// writePeersWeek sets peers_week of the table's rows by id, 500 rows a
// transaction: a reader meanwhile sees some rows written and some not yet.
func (s *Store) writePeersWeek(ctx context.Context, table string, changes map[int64]int64) error {
	for c := range slices.Chunk(slices.Sorted(maps.Keys(changes)), 500) {
		args := make([]any, 0, 2*len(c))
		for _, id := range c {
			args = append(args, id, changes[id])
		}
		err := s.tx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "WITH pw (id, c) AS (VALUES "+strings.TrimSuffix(strings.Repeat("(?, ?),", len(c)), ",")+
				") UPDATE "+table+" SET peers_week = pw.c FROM pw WHERE "+table+".id = pw.id", args...)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Top returns, from offset, up to limit entries of the top of the kind,
// window and voice in the language, how many entries it has, and when it
// was computed, the zero time while it never was.
func (s *Store) Top(ctx context.Context, kind string, w Window, v Voice, lang string, limit, offset int) ([]TopEntry, int64, time.Time, error) {
	key := voiceKey(v, lang)
	var total int64
	var computed sql.NullInt64
	if err := s.r.QueryRowContext(ctx, "SELECT count(*), max(computed_at) FROM stat_top WHERE kind = ? AND window = ? AND voice = ?", kind, w.Name, key).Scan(&total, &computed); err != nil {
		return nil, 0, time.Time{}, err
	}
	var at time.Time
	if computed.Valid {
		at = time.Unix(computed.Int64, 0).UTC()
	}
	var out []TopEntry
	var ids []int64
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var e TopEntry
		if err := rows.Scan(&e.Rank, &e.Work.Work.ID, &e.Peers, &e.PrevRank, &e.Work.Releases); err != nil {
			return err
		}
		out = append(out, e)
		ids = append(ids, e.Work.Work.ID)
		return nil
	}, "SELECT rank, work, peers, coalesce(prev_rank, 0), releases FROM stat_top WHERE kind = ? AND window = ? AND voice = ? ORDER BY rank LIMIT ? OFFSET ?",
		kind, w.Name, key, limit, offset); err != nil {
		return nil, 0, time.Time{}, err
	}
	sums, err := s.summaries(ctx, ids, v, lang)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	for i := range out {
		if sum, ok := sums[out[i].Work.Work.ID]; ok {
			out[i].Work = sum
		}
	}
	return out, total, at, nil
}
