package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"
)

// Kinds of journal entries, each with its outcomes.
const (
	// JournalSighted is a hash queued, promoted to an earlier discovery's
	// queue, or revived from the dead set: queued, promoted, revived.
	JournalSighted = "sighted"
	// JournalLeased is a hash leased to a fetcher: fetch.
	JournalLeased = "leased"
	// JournalFetched is metadata that came back: stored, rejected, duplicate.
	JournalFetched = "fetched"
	// JournalFetchFailed is a fetch that brought none: no_peers, timeout,
	// dropped.
	JournalFetchFailed = "fetch_failed"
	// JournalEvicted is a hash Trim evicted: aged, oversize.
	JournalEvicted = "evicted"
	// JournalClassified is a classification recorded: jev, reuse, failed.
	JournalClassified = "classified"
	// JournalMatched is a title key match the release took: tmdb, jev, arr,
	// none, error; or earlier, the key's match from before the release was
	// stored.
	JournalMatched = "matched"
	// JournalArr is an event Radarr or Sonarr reported: grab, import.
	JournalArr = "arr"
	// JournalFeed is a tracker feed's first listing of an item: stored,
	// queued, already_queued, rejected.
	JournalFeed = "feed"
	// JournalProbeWanted is a probe asked for: search, request.
	JournalProbeWanted = "probe_wanted"
	// JournalProbeLeased is a probe task handed to a prober: request,
	// retry, search, background.
	JournalProbeLeased = "probe_leased"
	// JournalProbed is a probe's result: tracks, unknown, no_peers, timeout.
	JournalProbed = "probed"
	// JournalDeleted is the torrent swept from the store: idle, expired.
	JournalDeleted = "deleted"
)

// JournalOutcomes maps every kind of journal entry to the outcomes the store
// writes for it; the store's tests fail on an entry written outside it.
var JournalOutcomes = map[string][]string{
	JournalSighted:     {"queued", "promoted", "revived"},
	JournalLeased:      {"fetch"},
	JournalFetched:     {"stored", "rejected", "duplicate"},
	JournalFetchFailed: {"no_peers", "timeout", "dropped"},
	JournalEvicted:     {"aged", "oversize"},
	JournalClassified:  {LayerJev, LayerReuse, "failed"},
	JournalMatched:     {LayerTMDB, LayerJev, LayerArr, MatchEarlier, "none", "error"},
	JournalArr:         {EventGrab, EventImport},
	JournalFeed:        {"stored", "queued", "already_queued", "rejected"},
	JournalProbeWanted: {"search", "request"},
	JournalProbeLeased: {"request", "retry", "search", "background"},
	JournalProbed:      {ProbeTracks, ProbeUnknown, ProbeNoPeers, ProbeTimeout},
	JournalDeleted:     {"idle", "expired"},
}

// MatchEarlier is the outcome of a matched entry for a release that took the
// match its title key had when the release was stored.
const MatchEarlier = "earlier"

// Triggers of a classification or a match, as the journal names who asked.
const (
	TriggerWorker  = "worker"
	TriggerSearch  = "search"
	TriggerTorznab = "torznab"
	TriggerArr     = "arr"
)

// Probe failures, as the journal names them.
const (
	ProbeNoPeers = "no_peers"
	ProbeTimeout = "timeout"
)

// journalFanOut is the most releases of one title key a match is journaled
// on; a match of a bigger key is journaled on none.
const journalFanOut = 500

// journalReuse is the most siblings a kind copied by reuse is journaled on;
// a copy to more is journaled on none.
const journalReuse = 50

// journalNoMatch is the most releases of one title key a match that found
// nothing, or failed, is journaled on; a bigger key's is journaled on none.
const journalNoMatch = 50

// journalTracks is the most tracks a probed/tracks entry lists.
const journalTracks = 32

// journalWantedFor is how long after a search's probe_wanted entry a search
// that returns the release again is not journaled.
const journalWantedFor = 24 * time.Hour

// JournalEntry is one event of a torrent's life.
type JournalEntry struct {
	Hash Hash
	// At is when the store recorded the event; for a grab or an import
	// Radarr or Sonarr reports, when it happened.
	At      time.Time
	Kind    string
	Outcome string
	// Detail is a JSON object of the event's particulars, or "".
	Detail string
}

// maxDetailString is the most bytes of a string a journal entry keeps.
const maxDetailString = 1 << 10

// Detail builds an entry's particulars from key-value pairs, leaving out a
// value that is "", 0, nil, empty, the zero time or a float JSON cannot
// hold (NaN, ±Inf), which would otherwise fail the change the entry rides
// with; a time is written in Unix seconds.
func Detail(kv ...any) map[string]any {
	d := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		v := kv[i+1]
		switch x := v.(type) {
		case nil:
			continue
		case string:
			if x == "" {
				continue
			}
		case int:
			if x == 0 {
				continue
			}
		case int64:
			if x == 0 {
				continue
			}
		case float64:
			if x == 0 || !finite(x) {
				continue
			}
		case time.Time:
			if x.IsZero() {
				continue
			}
			v = x.Unix()
		case map[string]float64:
			m := make(map[string]float64, len(x))
			for k, f := range x {
				if finite(f) {
					m[k] = f
				}
			}
			if len(m) == 0 {
				continue
			}
			v = m
		case []string:
			if len(x) == 0 {
				continue
			}
		}
		d[kv[i].(string)] = v
	}
	return d
}

func finite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

// clipped returns the detail as decoded JSON with clip applied to every
// string in it, the keys of its objects among them. A detail is cut
// through its JSON, so the cut holds for whatever types a caller puts in.
func clipped(detail map[string]any) (any, error) {
	b, err := json.Marshal(detail)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return clipValue(v), nil
}

// clipValue applies clip to every string of v, a value decoded from JSON.
func clipValue(v any) any {
	switch x := v.(type) {
	case string:
		return clip(x)
	case []any:
		for i, e := range x {
			x[i] = clipValue(e)
		}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[clip(k)] = clipValue(e)
		}
		return out
	}
	return v
}

// clip returns s if it has at most maxDetailString bytes, else its longest
// prefix of whole runes within them followed by "…": at most 1 KiB and the
// marker's 3 bytes.
func clip(s string) string {
	if len(s) <= maxDetailString {
		return s
	}
	end := 0
	for i := range s {
		if i > maxDetailString {
			break
		}
		end = i
	}
	return s[:end] + "…"
}

// journal writes one entry of the hash inside tx, its detail clipped; an
// empty detail writes NULL.
func journal(ctx context.Context, tx *sql.Tx, h Hash, at time.Time, kind, outcome string, detail map[string]any) error {
	var d any
	if len(detail) > 0 {
		cut, err := clipped(detail)
		if err != nil {
			return err
		}
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(cut); err != nil {
			return err
		}
		d = strings.TrimSuffix(b.String(), "\n")
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO torrent_journal (infohash, at, kind, outcome, detail) VALUES (?, ?, ?, ?, ?)", h[:], at.Unix(), kind, outcome, d)
	return err
}

// journalEach writes the entry for every hash, unless there are more than
// limit, when it writes none.
func journalEach(ctx context.Context, tx *sql.Tx, hashes []Hash, limit int, at time.Time, kind, outcome string, detail map[string]any) error {
	if len(hashes) > limit {
		return nil
	}
	for _, h := range hashes {
		if err := journal(ctx, tx, h, at, kind, outcome, detail); err != nil {
			return err
		}
	}
	return nil
}

// manyLeases is n, the leases an attempt took, when it took more than one;
// else 0, which Detail leaves out.
func manyLeases(n int) int {
	if n > 1 {
		return n
	}
	return 0
}

// hashesOf reads the infohash column of every row.
func hashesOf(rows *sql.Rows, err error) ([]Hash, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hash
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, Hash(b))
	}
	return out, rows.Err()
}

// wantedSince reports whether the hash has a probe_wanted entry of a search
// younger than journalWantedFor at the time.
func wantedSince(ctx context.Context, tx *sql.Tx, h Hash, at time.Time) (bool, error) {
	var recent bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM torrent_journal WHERE infohash = ? AND kind = ? AND outcome = 'search' AND at > ?)`,
		h[:], JournalProbeWanted, at.Add(-journalWantedFor).Unix()).Scan(&recent)
	return recent, err
}

// journalQuery selects, given a hash and a limit, the hash's newest entries.
const journalQuery = "SELECT at, kind, outcome, detail FROM torrent_journal WHERE infohash = ? ORDER BY at DESC, id DESC LIMIT ?"

// Journal returns the newest entries of the hash, up to limit, oldest
// first, in the order of their times and, at one time, of their writing.
func (s *Store) Journal(ctx context.Context, h Hash, limit int) ([]JournalEntry, error) {
	var out []JournalEntry
	err := s.each(ctx, func(rows *sql.Rows) error {
		e := JournalEntry{Hash: h}
		var at int64
		var d sql.NullString
		if err := rows.Scan(&at, &e.Kind, &e.Outcome, &d); err != nil {
			return err
		}
		e.At, e.Detail = time.Unix(at, 0).UTC(), d.String
		out = append(out, e)
		return nil
	}, journalQuery, h[:], limit)
	slices.Reverse(out)
	return out, err
}

// JournalFeed records what a tracker feed's listing of the hash came to.
func (s *Store) JournalFeed(ctx context.Context, h Hash, at time.Time, outcome string, detail map[string]any) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return journal(ctx, tx, h, at, JournalFeed, outcome, detail) })
}

// journalSweep selects, given a time and a limit, the oldest entries before
// the time.
const journalSweep = "SELECT id FROM torrent_journal WHERE at < ? ORDER BY at LIMIT ?"

// SweepJournal deletes the entries older than before, in transactions of
// at most batch each, and returns how many.
func (s *Store) SweepJournal(ctx context.Context, before time.Time, batch int) (int, error) {
	total := 0
	for {
		var n int
		err := s.tx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, "DELETE FROM torrent_journal WHERE id IN ("+journalSweep+")", before.Unix(), batch)
			if err != nil {
				return err
			}
			d, err := res.RowsAffected()
			n = int(d)
			return err
		})
		if err != nil {
			return total, err
		}
		total += n
		s.journalDeleted.Add(ctx, int64(n))
		if n < batch {
			return total, nil
		}
	}
}
