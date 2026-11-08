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

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/bus/bustest"
	"sarak.sh/mikluko/sniffarr/internal/orders"
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

// newIntakeFixture serves a store and a bus, with the fetch orders' queue
// started on it, whose clock moves a second at every reading.
func newIntakeFixture(t *testing.T) *intakeFixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	f := &intakeFixture{st: st, db: db, nc: bustest.Conn(t), now: time.Now().UTC().Truncate(time.Second)}
	f.js, err = jetstream.New(f.nc)
	require.NoError(t, err)
	fetches := orders.New(orders.Config{Stream: bus.StreamFetchOrders, Lanes: bus.FetchLanes})
	require.NoError(t, fetches.Start(ctx, f.js))
	f.s = &Service{Store: st, Fetches: fetches, Log: slog.New(slog.DiscardHandler), IntakeWait: 50 * time.Millisecond, Now: f.clock}
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

// order publishes a fetch order of each hash at the priority.
func (f *intakeFixture) order(t *testing.T, priority int, bs ...byte) {
	t.Helper()
	l, ok := f.s.Fetches.Lane(store.TaskFetch, priority)
	require.True(t, ok)
	items := make([]orders.Item, len(bs))
	for i, b := range bs {
		items[i] = orders.Item{Hash: store.Hash{b}}
	}
	_, err := f.s.Fetches.Publish(t.Context(), l, items)
	require.NoError(t, err)
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

// queuedOrder is an order on the fetch orders' stream.
type queuedOrder struct {
	hash     byte
	subject  string
	attempt  int
	schedule string
}

// queued returns the messages of the fetch orders' stream in the order they
// were published, the first byte of each hash.
func (f *intakeFixture) queued(t *testing.T) []queuedOrder {
	t.Helper()
	stream, err := f.js.Stream(t.Context(), bus.StreamFetchOrders)
	require.NoError(t, err)
	info, err := stream.Info(t.Context())
	require.NoError(t, err)
	var out []queuedOrder
	for seq := info.State.FirstSeq; info.State.Msgs > 0 && seq <= info.State.LastSeq; seq++ {
		m, err := stream.GetMsg(t.Context(), seq)
		if err != nil {
			continue
		}
		out = append(out, queuedOrder{hash: m.Data[0], subject: m.Subject, attempt: bus.Attempt(m.Header), schedule: m.Header.Get(jetstream.ScheduleHeader)})
	}
	return out
}

func TestIntakeAnnouncedFirst(t *testing.T) {
	f := newIntakeFixture(t)
	f.s.IntakeBatch = 1
	f.sight(t, store.Sample, 1, 2)
	f.sight(t, store.Announce, 3, 4)
	f.run(t)

	require.Equal(t, []queuedOrder{{hash: 3, subject: bus.SubjectFetchP2}, {hash: 4, subject: bus.SubjectFetchP2},
		{hash: 1, subject: bus.SubjectFetchP3}, {hash: 2, subject: bus.SubjectFetchP3}}, f.queued(t),
		"the announced batches are taken before the sampled ones published earlier")
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
	f.sight(t, store.Sample, 1, 2, 3, 5)
	f.sight(t, store.Announce, 4)
	_, err = f.js.Publish(ctx, bus.SubjectSightSample, []byte("short"))
	require.NoError(t, err)
	f.run(t)

	require.Equal(t, []queuedOrder{{hash: 4, subject: bus.SubjectFetchP2}, {hash: 5, subject: bus.SubjectFetchP3}}, f.queued(t),
		"1 is stored, 2 rejected, 3 dead and only sampled; an announce revives the dead 4")

	r, err := f.st.Get(ctx, store.Hash{1})
	require.NoError(t, err)
	require.True(t, r.LastSeen.After(seen), "the stored hash's last sighting moves")

	var revived int
	require.NoError(t, f.db.R.QueryRowContext(ctx, "SELECT count(*) FROM torrent_journal WHERE infohash = ? AND outcome = 'revived'", infohash(4)).Scan(&revived))
	require.Equal(t, 1, revived)
	held, err := f.st.Screen(ctx, []store.Hash{{3}, {4}}, f.now)
	require.NoError(t, err)
	require.Equal(t, map[store.Hash]store.Screened{{3}: {Dead: true}}, held, "the revived 4 leaves the dead set")
}

func TestIntakeRedelivered(t *testing.T) {
	f := newIntakeFixture(t)
	ctx := t.Context()
	fresh := []store.Queued{{Hash: store.Hash{1}, Discovery: store.Announce}, {Hash: store.Hash{2}, Discovery: store.Sample}}
	n, err := f.s.queue(ctx, fresh, f.now)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	n, err = f.s.queue(ctx, fresh, f.now)
	require.NoError(t, err)
	require.Zero(t, n, "a batch redelivered after a failure publishes its orders again under the same IDs")
	require.Len(t, f.queued(t), 2)
}

func TestEnqueueArr(t *testing.T) {
	f := newIntakeFixture(t)
	ctx := t.Context()
	_, err := f.db.W.ExecContext(ctx, "INSERT INTO task_dead (key, until) VALUES (task_dead_key(?), ?)", infohash(1), f.now.Add(time.Hour).UnixMilli())
	require.NoError(t, err)
	_, err = f.st.Reject(ctx, store.Hash{2}, rules.Software, "setup.exe", 1, f.now)
	require.NoError(t, err)
	n, err := f.s.Enqueue(ctx, []store.Sighting{{Hash: store.Hash{1}, Discovery: store.Arr}, {Hash: store.Hash{2}, Discovery: store.Arr}})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, []queuedOrder{{hash: 1, subject: bus.SubjectFetchP1}}, f.queued(t), "Radarr and Sonarr revive a dead hash, not a rejected one")
	require.Equal(t, []string{"sighted/revived"}, journal(t, f.st, store.Hash{1}))
}

func TestEnqueueTorznab(t *testing.T) {
	f := newIntakeFixture(t)
	ctx := t.Context()
	n, err := f.s.Enqueue(ctx, []store.Sighting{{Hash: store.Hash{1}, Discovery: store.Torznab}})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, []queuedOrder{{hash: 1, subject: bus.SubjectFetchP2}}, f.queued(t), "with the announced")
	got := f.take(t, 1, 1)
	require.Len(t, got, 1)
	require.NoError(t, f.s.result(ctx, 1, torrentResult(1, "Heat.1995.1080p.BluRay")))
	require.Equal(t, store.Torznab, f.discovery(t, 1), "the torrent keeps the discovery its order named")
	e, err := f.st.Journal(ctx, store.Hash{1}, 10)
	require.NoError(t, err)
	require.Equal(t, store.JournalSighted, e[0].Kind)
	require.JSONEq(t, `{"discovery":"torznab"}`, e[0].Detail)
}

func TestIntakePromotes(t *testing.T) {
	f := newIntakeFixture(t)
	f.sight(t, store.Sample, 1, 2)
	f.run(t)
	f.sight(t, store.Announce, 2)
	f.run(t)

	got, err := f.s.Fetches.Take(1, 3, store.PrioritySample)
	require.NoError(t, err)
	require.Len(t, got, 2, "the sample's order of 2 is a second order of one held")
	require.Equal(t, [2]byte{2, 1}, [2]byte{got[0].Hash[0], got[1].Hash[0]}, "an announce of a queued sample moves it ahead")
	require.Equal(t, store.PriorityAnnounce, got[0].Priority)
	require.Equal(t, []string{"sighted/queued"}, journal(t, f.st, store.Hash{2}))
}

func TestIntakeMissingConsumer(t *testing.T) {
	f := newIntakeFixture(t)
	require.NoError(t, f.js.DeleteConsumer(t.Context(), bus.StreamSightings, bus.ConsumerSample))
	require.ErrorIs(t, f.s.Intake(t.Context(), f.js), jetstream.ErrConsumerNotFound)
}
