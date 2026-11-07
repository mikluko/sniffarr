package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
	// Debut, OnChart, Peak and Reentry are the work's history in this top
	// since TopSince, zero for a top computed before it: when it first
	// entered, its time in the top over every stay, its best place, and
	// whether its current stay follows an earlier one.
	Debut   time.Time
	OnChart time.Duration
	Peak    int
	Reentry bool
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

// topVoice is a voice in a language a top is computed for.
type topVoice struct {
	v    Voice
	lang string
}

// topRow is one entry of a top as ComputeTop computes it.
type topRow struct {
	kind, window string
	voice        topVoice
	rank         int
	work, peers  int64
	prev         int
	sum          Summary
}

// ComputeTop recomputes at now the tops of every kind, window, and voice in
// each of Options.Languages, TopSize works each with their summaries over
// the releases the voice admits, adding them to the tops' history, and the
// PeersWeek of every torrent and every work.
func (s *Store) ComputeTop(ctx context.Context, now time.Time) error {
	conn, err := s.r.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	voices := []topVoice{{v: VoiceAny}}
	for _, l := range s.opts.Languages {
		voices = append(voices, topVoice{VoiceLikely, l}, topVoice{VoiceMarker, l})
	}
	var rows []topRow
	var week, works map[int64]int64
	for _, w := range Windows {
		wr, wt, ww, err := windowTops(ctx, conn, w, now, voices, w == WindowWeek)
		if err != nil {
			return err
		}
		rows = append(rows, wr...)
		if w == WindowWeek {
			week, works = wt, ww
		}
	}
	if err := summarizeTops(ctx, conn, rows, s.opts.Languages); err != nil {
		return err
	}
	rc, err := peersWeekChanges(ctx, conn, "torrent_releases", week)
	if err != nil {
		return err
	}
	wc, err := peersWeekChanges(ctx, conn, "tmdb_works", works)
	if err != nil {
		return err
	}
	if err := s.writeTops(ctx, rows, now); err != nil {
		return err
	}
	if err := s.writePeersWeek(ctx, "torrent_releases", rc); err != nil {
		return err
	}
	return s.writePeersWeek(ctx, "tmdb_works", wc)
}

// windowTops returns the tops over the window ending at now of every kind
// and voice, computed on conn, and with peers the peers of every torrent and
// of every work over the window.
func windowTops(ctx context.Context, conn *sql.Conn, w Window, now time.Time, voices []topVoice, peers bool) (rows []topRow, torrents, works map[int64]int64, err error) {
	h, d := w.since(now)
	ph, pd := h-int64(w.Length/time.Second), d-int64(w.Length/time.Second)
	if _, err := conn.ExecContext(ctx, "DROP TABLE IF EXISTS temp.win; DROP TABLE IF EXISTS temp.prev"); err != nil {
		return nil, nil, nil, err
	}
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE win AS SELECT torrent_id, hll_union_agg(sketch) AS sketch FROM (
		SELECT torrent_id, sketch FROM stat_peers_by_hour WHERE hour >= ?1
		UNION ALL SELECT torrent_id, sketch FROM stat_peers_by_day WHERE day >= ?2) GROUP BY torrent_id`, h, d); err != nil {
		return nil, nil, nil, err
	}
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE prev AS SELECT torrent_id, hll_union_agg(sketch) AS sketch FROM (
		SELECT torrent_id, sketch FROM stat_peers_by_hour WHERE hour >= ?1 AND hour < ?2
		UNION ALL SELECT torrent_id, sketch FROM stat_peers_by_day WHERE day >= ?3 AND day < ?4) GROUP BY torrent_id`, ph, h, pd, d); err != nil {
		return nil, nil, nil, err
	}
	if peers {
		if torrents, err = counts(ctx, conn, "SELECT torrent_id, hll_card(sketch) FROM win"); err != nil {
			return nil, nil, nil, err
		}
	}
	for _, vc := range voices {
		vr, vw, err := voiceTops(ctx, conn, w, vc, peers && vc.v == VoiceAny)
		if err != nil {
			return nil, nil, nil, err
		}
		rows = append(rows, vr...)
		if vw != nil {
			works = vw
		}
	}
	if _, err := conn.ExecContext(ctx, "DROP TABLE win; DROP TABLE prev"); err != nil {
		return nil, nil, nil, err
	}
	return rows, torrents, works, nil
}

// voiceTops returns the tops of every kind under vc over the window whose
// peers by torrent are in temp tables win and prev, and with peers the
// peers of every work vc admits a release of.
func voiceTops(ctx context.Context, conn *sql.Conn, w Window, vc topVoice, peers bool) (rows []topRow, works map[int64]int64, err error) {
	cond, args := voiceTest(vc.v, vc.lang, "t")
	filter := ""
	if cond != "" {
		filter = " WHERE " + cond
	}
	for _, tbl := range []string{"win", "prev"} {
		if _, err := conn.ExecContext(ctx, "CREATE TEMP TABLE "+tbl+`w AS SELECT t.work AS work, k.type AS type, hll_card(hll_union_agg(s.sketch)) AS peers
			FROM `+tbl+` s JOIN torrent_releases t ON t.id = s.torrent_id JOIN tmdb_works k ON k.id = t.work`+filter+` GROUP BY t.work`, args...); err != nil {
			return nil, nil, err
		}
	}
	if peers {
		if works, err = counts(ctx, conn, "SELECT work, peers FROM winw"); err != nil {
			return nil, nil, err
		}
	}
	for _, kind := range TopKinds {
		top, err := kindTop(ctx, conn, kind, w, vc)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, top...)
	}
	if _, err := conn.ExecContext(ctx, "DROP TABLE winw; DROP TABLE prevw"); err != nil {
		return nil, nil, err
	}
	return rows, works, nil
}

// kindTop returns the top of the kind under vc over the window whose peers
// by work are in temp tables winw and prevw.
func kindTop(ctx context.Context, conn *sql.Conn, kind string, w Window, vc topVoice) ([]topRow, error) {
	var top []topRow
	err := eachOn(ctx, conn, func(rows *sql.Rows) error {
		r := topRow{kind: kind, window: w.Name, voice: vc, rank: len(top) + 1}
		if err := rows.Scan(&r.work, &r.peers); err != nil {
			return err
		}
		top = append(top, r)
		return nil
	}, "SELECT work, peers FROM winw WHERE type = ? ORDER BY peers DESC, work LIMIT ?", TypeOf(kind), TopSize)
	if err != nil || len(top) == 0 {
		return nil, err
	}
	before, err := prevRanks(ctx, conn, kind, top)
	if err != nil {
		return nil, err
	}
	for i := range top {
		top[i].prev = before[top[i].work]
	}
	return top, nil
}

// prevRanks returns, by work, the places of the works of top that had
// peers in the window before, among every work of the kind in temp table
// prevw, 1 first. It sorts the whole of prevw's kind but returns only the
// works of top.
func prevRanks(ctx context.Context, conn *sql.Conn, kind string, top []topRow) (map[int64]int, error) {
	args := []any{TypeOf(kind)}
	for _, r := range top {
		args = append(args, r.work)
	}
	out := map[int64]int{}
	err := eachOn(ctx, conn, func(rows *sql.Rows) error {
		var work int64
		var place int
		if err := rows.Scan(&work, &place); err != nil {
			return err
		}
		out[work] = place
		return nil
	}, `SELECT work, place FROM (
		SELECT work, row_number() OVER (ORDER BY peers DESC, work) AS place FROM prevw WHERE type = ? AND peers > 0)
		WHERE work IN (`+strings.TrimSuffix(strings.Repeat("?,", len(top)), ",")+")", args...)
	return out, err
}

// summarizeTops sets, on conn, the summary of every row over the releases
// its voice admits, one pass per voice over the works in any of its tops.
func summarizeTops(ctx context.Context, conn *sql.Conn, rows []topRow, langs []string) error {
	byVoice := map[topVoice]map[int64]Summary{}
	for _, r := range rows {
		if byVoice[r.voice] == nil {
			byVoice[r.voice] = map[int64]Summary{}
		}
		byVoice[r.voice][r.work] = emptySummary(Work{ID: r.work}, langs)
	}
	for vc, sums := range byVoice {
		if err := releaseSums(ctx, conn, sums, vc.v, vc.lang, langs); err != nil {
			return err
		}
	}
	for i := range rows {
		rows[i].sum = byVoice[rows[i].voice][rows[i].work]
	}
	return nil
}

// writeTops replaces stat_top with rows computed at now and adds them to
// the history of the tops.
func (s *Store) writeTops(ctx context.Context, rows []topRow, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var prev sql.NullInt64
		if err := tx.QueryRowContext(ctx, "SELECT max(computed_at) FROM stat_top").Scan(&prev); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM stat_top"); err != nil {
			return err
		}
		for _, r := range rows {
			sources, err := json.Marshal(r.sum.Sources)
			if err != nil {
				return err
			}
			audio, err := json.Marshal(r.sum.Audio)
			if err != nil {
				return err
			}
			var first any
			if !r.sum.FirstSeen.IsZero() {
				first = r.sum.FirstSeen.Unix()
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO stat_top (kind, window, voice, rank, work, peers, prev_rank, releases, first_seen, sources, audio, computed_at)
				VALUES (?, ?, ?, ?, ?, ?, nullif(?, 0), ?, ?, ?, ?, ?)`,
				r.kind, r.window, voiceKey(r.voice.v, r.voice.lang), r.rank, r.work, r.peers, r.prev, r.sum.Releases, first, string(sources), string(audio), now.Unix()); err != nil {
				return err
			}
		}
		return addHistory(ctx, tx, now, prev)
	})
}

// addHistory adds stat_top, as computed at now, to stat_top_history, where
// a work in the top computed at prev stays and any other enters. A stay
// lasts from the computation it entered at to the last one it was in.
func addHistory(ctx context.Context, tx *sql.Tx, now time.Time, prev sql.NullInt64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO stat_top_history (kind, window, voice, work, first_at, entered_at, last_at, prior, entries, peak)
		SELECT kind, window, voice, work, ?1, ?1, ?1, 0, 1, rank FROM stat_top WHERE true
		ON CONFLICT (kind, window, voice, work) DO UPDATE SET
			prior = CASE WHEN last_at IS ?2 THEN prior ELSE prior + last_at - entered_at END,
			entered_at = CASE WHEN last_at IS ?2 THEN entered_at ELSE ?1 END,
			entries = entries + (last_at IS NOT ?2),
			last_at = ?1,
			peak = min(peak, excluded.peak)`, now.Unix(), prev)
	return err
}

// TopSince returns when the history of the top of the kind, window and
// voice in the language began, the zero time while it has none.
func (s *Store) TopSince(ctx context.Context, kind string, w Window, v Voice, lang string) (time.Time, error) {
	var since sql.NullInt64
	if err := s.r.QueryRowContext(ctx, "SELECT min(first_at) FROM stat_top_history WHERE kind = ? AND window = ? AND voice = ?",
		kind, w.Name, voiceKey(v, lang)).Scan(&since); err != nil || !since.Valid {
		return time.Time{}, err
	}
	return time.Unix(since.Int64, 0).UTC(), nil
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
// was computed, the zero time while it never was. What an entry sums up of
// the releases is as of then; its TMDB texts, Rank and GrabsWeek are read
// now.
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
	var ids, unsummed []int64
	if err := s.each(ctx, func(rows *sql.Rows) error {
		e, summed, err := scanTopEntry(rows)
		if err != nil {
			return err
		}
		out = append(out, e)
		ids = append(ids, e.Work.Work.ID)
		if !summed {
			unsummed = append(unsummed, e.Work.Work.ID)
		}
		return nil
	}, `SELECT t.rank, t.work, t.peers, coalesce(t.prev_rank, 0), t.releases, t.first_seen, t.sources, t.audio,
			coalesce(h.first_at, 0), coalesce(h.prior + h.last_at - h.entered_at, 0), coalesce(h.peak, 0), coalesce(h.entries > 1, false)
		FROM stat_top t LEFT JOIN stat_top_history h USING (kind, window, voice, work)
		WHERE t.kind = ? AND t.window = ? AND t.voice = ? ORDER BY t.rank LIMIT ? OFFSET ?`,
		kind, w.Name, key, limit, offset); err != nil {
		return nil, 0, time.Time{}, err
	}
	works, err := s.workSummaries(ctx, ids)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	sums, err := s.summaries(ctx, unsummed, v, lang)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	for i := range out {
		id := out[i].Work.Work.ID
		if sum, ok := sums[id]; ok {
			out[i].Work = sum
		} else if sum, ok := works[id]; ok {
			sum.Releases, sum.FirstSeen, sum.Sources, sum.Audio = out[i].Work.Releases, out[i].Work.FirstSeen, out[i].Work.Sources, out[i].Work.Audio
			out[i].Work = sum
		}
	}
	return out, total, at, nil
}

// scanTopEntry scans a row of stat_top's rank, work, peers, prev_rank,
// releases, first_seen, sources and audio, then its history's first entry,
// time in the top in seconds, peak and re-entry, and reports whether it
// holds the summary, which a row stored before stat_top had those columns
// lacks.
func scanTopEntry(rows *sql.Rows) (TopEntry, bool, error) {
	var e TopEntry
	var first sql.NullInt64
	var debut, onChart int64
	var sources, audio sql.NullString
	if err := rows.Scan(&e.Rank, &e.Work.Work.ID, &e.Peers, &e.PrevRank, &e.Work.Releases, &first, &sources, &audio,
		&debut, &onChart, &e.Peak, &e.Reentry); err != nil {
		return TopEntry{}, false, err
	}
	if debut != 0 {
		e.Debut = time.Unix(debut, 0).UTC()
	}
	e.OnChart = time.Duration(onChart) * time.Second
	if !audio.Valid {
		return e, false, nil
	}
	if first.Valid {
		e.Work.FirstSeen = time.Unix(first.Int64, 0).UTC()
	}
	if err := json.Unmarshal([]byte(sources.String), &e.Work.Sources); err != nil {
		return TopEntry{}, false, err
	}
	if err := json.Unmarshal([]byte(audio.String), &e.Work.Audio); err != nil {
		return TopEntry{}, false, err
	}
	return e, true, nil
}
