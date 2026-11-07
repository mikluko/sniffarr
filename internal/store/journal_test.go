package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// entries returns the journal of the hash as kind/outcome, oldest first.
func entries(t *testing.T, s *Store, h Hash) []string {
	t.Helper()
	got, err := s.Journal(context.Background(), h, 1000)
	require.NoError(t, err)
	out := make([]string, len(got))
	for i, e := range got {
		out[i] = e.Kind + "/" + e.Outcome
	}
	return out
}

// entry returns the one entry of the hash of the kind and outcome.
func entry(t *testing.T, s *Store, h Hash, kind, outcome string) JournalEntry {
	t.Helper()
	got, err := s.Journal(context.Background(), h, 1000)
	require.NoError(t, err)
	var found []JournalEntry
	for _, e := range got {
		if e.Kind == kind && e.Outcome == outcome {
			found = append(found, e)
		}
	}
	require.Len(t, found, 1, "%s/%s of %x", kind, outcome, h[:1])
	return found[0]
}

func TestJournalOrderAndLimit(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	require.NoError(t, s.tx(ctx, func(tx *sql.Tx) error {
		for i, o := range []string{"queued", "promoted", "revived"} {
			if err := journal(ctx, tx, hash(1), t0.Add(time.Duration(i)*time.Second), JournalSighted, o, detail("discovery", Announce, "priority", 0, "none", "")); err != nil {
				return err
			}
		}
		return journal(ctx, tx, hash(2), t0, JournalFetched, "duplicate", nil)
	}))
	got, err := s.Journal(ctx, hash(1), 10)
	require.NoError(t, err)
	require.Equal(t, []JournalEntry{
		{Hash: hash(1), At: t0, Kind: JournalSighted, Outcome: "queued", Detail: `{"discovery":"announce"}`},
		{Hash: hash(1), At: t0.Add(time.Second), Kind: JournalSighted, Outcome: "promoted", Detail: `{"discovery":"announce"}`},
		{Hash: hash(1), At: t0.Add(2 * time.Second), Kind: JournalSighted, Outcome: "revived", Detail: `{"discovery":"announce"}`},
	}, got, "oldest first, zero values left out of the detail")
	got, err = s.Journal(ctx, hash(1), 2)
	require.NoError(t, err)
	require.Equal(t, []string{"promoted", "revived"}, []string{got[0].Outcome, got[1].Outcome}, "the newest ones within the limit")
	got, err = s.Journal(ctx, hash(2), 10)
	require.NoError(t, err)
	require.Equal(t, []JournalEntry{{Hash: hash(2), At: t0, Kind: JournalFetched, Outcome: "duplicate"}}, got, "no detail reads as empty")
	got, err = s.Journal(ctx, hash(3), 10)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestSweepJournal(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	require.NoError(t, s.tx(ctx, func(tx *sql.Tx) error {
		for i := range 3 {
			if err := journal(ctx, tx, hash(byte(i+1)), t0, JournalFetched, "stored", nil); err != nil {
				return err
			}
		}
		return journal(ctx, tx, hash(1), t0.Add(48*time.Hour), JournalDeleted, "idle", nil)
	}))
	n, err := s.SweepJournal(ctx, t0.Add(24*time.Hour), 2)
	require.NoError(t, err)
	require.Equal(t, 3, n, "the rows at t0, over two batches")
	require.Equal(t, []string{"deleted/idle"}, entries(t, s, hash(1)))
	require.Empty(t, entries(t, s, hash(2)))
}

func TestJournalPlans(t *testing.T) {
	s := open(t)
	for query, want := range map[string]string{
		journalSweep: "INDEX torrent_journal_at (at<?)",
		"SELECT at, kind, outcome, detail FROM torrent_journal WHERE infohash = ? ORDER BY id DESC LIMIT ?": "INDEX torrent_journal_infohash (infohash=?)",
	} {
		var plan []string
		require.NoError(t, s.each(context.Background(), func(rows *sql.Rows) error {
			var id, parent, unused int
			var d string
			if err := rows.Scan(&id, &parent, &unused, &d); err != nil {
				return err
			}
			plan = append(plan, d)
			return nil
		}, "EXPLAIN QUERY PLAN "+query, 0, 1))
		require.Len(t, plan, 1, query)
		require.Contains(t, plan[0], want)
	}
}
