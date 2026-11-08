package ingest

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/bus/bustest"
	"sarak.sh/mikluko/sniffarr/internal/database"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

type intakeFixture struct {
	s  *Service
	st *store.Store
	db *database.DB
	nc *nats.Conn
	js jetstream.JetStream

	mu  sync.Mutex
	now time.Time
}

// newIntakeFixture serves a store and a bus whose clock moves a second at
// every reading.
func newIntakeFixture(t *testing.T) *intakeFixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close(context.Background())) })
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	f := &intakeFixture{st: st, db: db, nc: bustest.Conn(t), now: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	f.js, err = jetstream.New(f.nc)
	require.NoError(t, err)
	f.s = &Service{Store: st, Tasks: q, Log: slog.New(slog.DiscardHandler), IntakeWait: 50 * time.Millisecond, Now: f.clock}
	return f
}

func (f *intakeFixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(time.Second)
	return f.now
}

func (f *intakeFixture) sight(t *testing.T, discovery string, bs ...byte) {
	t.Helper()
	for _, b := range bs {
		m, ok := bus.SightingMsg(store.Hash{b}, discovery)
		require.True(t, ok)
		_, err := f.js.PublishMsg(t.Context(), m)
		require.NoError(t, err)
	}
}

// run runs Intake until the sightings stream is empty, then stops it.
func (f *intakeFixture) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.s.Intake(ctx, f.js) }()
	stream, err := f.js.Stream(t.Context(), bus.StreamSightings)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		info, err := stream.Info(t.Context())
		return err == nil && info.State.Msgs == 0
	}, 10*time.Second, 10*time.Millisecond, "every sighting is acknowledged or terminated")
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

// queued returns the fetch tasks queued, by first byte of the hash, with
// their priority and the time they were queued.
func (f *intakeFixture) queued(t *testing.T) map[byte][2]int64 {
	t.Helper()
	rows, err := f.db.R.QueryContext(t.Context(), "SELECT infohash, priority, queued_at FROM task_queue WHERE kind = 'fetch'")
	require.NoError(t, err)
	defer rows.Close()
	out := map[byte][2]int64{}
	for rows.Next() {
		var h []byte
		var p, at int64
		require.NoError(t, rows.Scan(&h, &p, &at))
		out[h[0]] = [2]int64{p, at}
	}
	require.NoError(t, rows.Err())
	return out
}

func TestIntakeAnnouncedFirst(t *testing.T) {
	f := newIntakeFixture(t)
	f.s.IntakeBatch = 1
	f.sight(t, store.Sample, 1, 2)
	f.sight(t, store.Announce, 3, 4)
	f.run(t)

	q := f.queued(t)
	require.Len(t, q, 4)
	require.Equal(t, int64(store.PriorityAnnounce), q[3][0])
	require.Equal(t, int64(store.PrioritySample), q[1][0])
	require.Less(t, q[3][1], q[4][1])
	require.Less(t, q[4][1], q[1][1], "the announced batches are taken before the sampled ones published earlier")
	require.Less(t, q[1][1], q[2][1])
}

func TestIntakeDrops(t *testing.T) {
	f := newIntakeFixture(t)
	ctx := t.Context()
	seen := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	_, _, err := f.st.Insert(ctx, store.Torrent{Hash: store.Hash{1}, Name: "Heat.1995.1080p", Size: 1 << 30,
		Files: []rules.File{{Path: "Heat.1995.mkv", Size: 1 << 30}}, Discovery: store.Sample, SeenAt: seen})
	require.NoError(t, err)
	_, err = f.st.Reject(ctx, store.Hash{2}, rules.Software, "setup.exe", 1, seen)
	require.NoError(t, err)
	for _, b := range []byte{3, 4} {
		_, err = f.db.W.ExecContext(ctx, "INSERT INTO task_dead (key, until) VALUES (task_dead_key(?), ?)", infohash(b), f.now.Add(time.Hour).UnixMilli())
		require.NoError(t, err)
	}
	stored, err := f.nc.SubscribeSync(bus.SubjectStored)
	require.NoError(t, err)

	f.sight(t, store.Sample, 1, 2, 3, 5)
	f.sight(t, store.Announce, 4)
	_, err = f.js.Publish(ctx, bus.SubjectSightSample, []byte("short"))
	require.NoError(t, err)
	f.run(t)

	q := f.queued(t)
	require.Len(t, q, 2, "1 is stored, 2 rejected, 3 dead and only sampled")
	require.Equal(t, int64(store.PriorityAnnounce), q[4][0], "an announce revives the dead 4")
	require.Equal(t, int64(store.PrioritySample), q[5][0])

	m, err := stored.NextMsg(5 * time.Second)
	require.NoError(t, err)
	require.Equal(t, infohash(1), m.Data, "the stored hash is named to the sniffers")
	r, err := f.st.Get(ctx, store.Hash{1})
	require.NoError(t, err)
	require.True(t, r.LastSeen.After(seen), "and its last sighting moves")

	var revived int
	require.NoError(t, f.db.R.QueryRowContext(ctx, "SELECT count(*) FROM torrent_journal WHERE infohash = ? AND outcome = 'revived'", infohash(4)).Scan(&revived))
	require.Equal(t, 1, revived)
}

func TestIntakeMissingConsumer(t *testing.T) {
	f := newIntakeFixture(t)
	require.NoError(t, f.js.DeleteConsumer(t.Context(), bus.StreamSightings, bus.ConsumerSample))
	require.ErrorIs(t, f.s.Intake(t.Context(), f.js), jetstream.ErrConsumerNotFound)
}
