package ingest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

func TestSight(t *testing.T) {
	f := newIntakeFixture(t)
	ctx := t.Context()
	seen := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	_, _, err := f.st.Insert(ctx, store.Torrent{Hash: store.Hash{1}, Name: "Heat.1995.1080p", Size: 8 << 30, Discovery: store.Sample, SeenAt: seen})
	require.NoError(t, err)
	_, err = f.st.Reject(ctx, store.Hash{2}, rules.Adult, "xxx", 1<<30, seen)
	require.NoError(t, err)
	_, err = f.db.W.ExecContext(ctx, "INSERT INTO task_dead (key, until) VALUES (task_dead_key(?), ?)", infohash(4), f.now.Add(time.Hour).UnixMilli())
	require.NoError(t, err)
	f.s.JetStream = f.js

	batch := []store.Sighting{{Hash: store.Hash{3}, Discovery: store.Sample}, {Hash: store.Hash{1}, Discovery: store.Sample},
		{Hash: store.Hash{2}, Discovery: store.Sample}, {Hash: store.Hash{1}, Discovery: store.Announce},
		{Hash: store.Hash{4}, Discovery: store.Sample}, {Hash: store.Hash{3}, Discovery: store.Sample}}
	known, stored, err := f.s.sight(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, 5, known, "stored, rejected, dead and only sampled, or a duplicate in the stream's window")
	require.Equal(t, []bool{false, true, false, true, false, false}, stored, "stored torrents are flagged, in order")
	require.Empty(t, f.queued(t), "Sight queues nothing itself")
	r, err := f.st.Get(ctx, store.Hash{1})
	require.NoError(t, err)
	require.Equal(t, seen.Unix(), r.LastSeen.Unix(), "nor moves a last sighting")
	stream, err := f.js.Stream(ctx, bus.StreamSightings)
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(5), info.State.Msgs, "every sighting but the repeated sample of 3 is on the stream; the announce of 1 is not a duplicate of its sample")

	f.run(t)
	q := f.queued(t)
	require.Len(t, q, 1)
	require.Contains(t, q, byte(3), "the intake queues what Sight counted as new")
}
