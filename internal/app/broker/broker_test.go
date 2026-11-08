package broker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"sarak.sh/mikluko/sniffarr/internal/bus"
)

const testPassword = "worker-secret"

// startBroker starts a broker on cfg and stops it at cleanup unless the test
// already did.
func startBroker(t *testing.T, cfg Config) *Broker {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	b := &Broker{}
	require.NoError(t, b.start(ctx, cfg, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() {
		if b.srv.Running() {
			require.NoError(t, b.stop(context.Background()))
		}
	})
	return b
}

func stopBroker(t *testing.T, b *Broker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, b.stop(ctx))
	require.True(t, b.Conn.IsClosed())
	require.False(t, b.srv.Running())
}

// streamNames lists the streams js has.
func streamNames(t *testing.T, js jetstream.JetStream) []string {
	t.Helper()
	var names []string
	l := js.StreamNames(t.Context())
	for n := range l.Name() {
		names = append(names, n)
	}
	require.NoError(t, l.Err())
	return names
}

func wantStreams() []string {
	var names []string
	for _, cfg := range bus.Streams() {
		names = append(names, cfg.Name)
	}
	return names
}

func TestBrokerTopology(t *testing.T) {
	b := startBroker(t, Config{Dir: t.TempDir()})
	require.Nil(t, b.srv.Addr(), "no listener without an address")
	require.ElementsMatch(t, wantStreams(), streamNames(t, b.JetStream))
	require.NoError(t, bus.Ensure(t.Context(), b.JetStream), "ensuring twice")
	require.ElementsMatch(t, wantStreams(), streamNames(t, b.JetStream))
	for stream, cfgs := range bus.Consumers() {
		for _, cfg := range cfgs {
			_, err := b.JetStream.Consumer(t.Context(), stream, cfg.Durable)
			require.NoError(t, err, "%s of %s", cfg.Durable, stream)
		}
	}

	msg := &nats.Msg{Subject: bus.SubjectSightAnnounce, Header: nats.Header{}, Data: []byte("x")}
	msg.Header.Set(jetstream.MsgIDHeader, "a")
	ack, err := b.JetStream.PublishMsg(t.Context(), msg)
	require.NoError(t, err)
	require.False(t, ack.Duplicate)
	ack, err = b.JetStream.PublishMsg(t.Context(), msg)
	require.NoError(t, err)
	require.True(t, ack.Duplicate, "a sighting again within the window")
	stopBroker(t, b)
}

func TestBrokerRestart(t *testing.T) {
	dir := t.TempDir()
	b := startBroker(t, Config{Dir: dir})
	_, err := b.JetStream.Publish(t.Context(), bus.SubjectFetchP1, []byte("order"))
	require.NoError(t, err)
	stopBroker(t, b)

	b = startBroker(t, Config{Dir: dir})
	require.ElementsMatch(t, wantStreams(), streamNames(t, b.JetStream))
	s, err := b.JetStream.Stream(t.Context(), bus.StreamFetchOrders)
	require.NoError(t, err)
	info, err := s.Info(t.Context())
	require.NoError(t, err)
	require.Equal(t, uint64(1), info.State.Msgs, "the order outlives the restart")
	stopBroker(t, b)
}

// worker connects to b as bus.WorkerUser and collects its async errors.
func worker(t *testing.T, b *Broker, password string, opts ...nats.Option) (*nats.Conn, <-chan error, error) {
	t.Helper()
	errs := make(chan error, 16)
	opts = append(opts, nats.UserInfo(bus.WorkerUser, password), nats.NoReconnect(),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errs <- err }))
	nc, err := nats.Connect(b.srv.ClientURL(), opts...)
	if err == nil {
		t.Cleanup(nc.Close)
	}
	return nc, errs, err
}

// refused waits for the permissions violation naming subject.
func refused(t *testing.T, errs <-chan error, subject string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case err := <-errs:
			if errors.Is(err, nats.ErrPermissionViolation) && strings.Contains(err.Error(), `"`+subject+`"`) {
				return
			}
		case <-timeout:
			t.Fatalf("no permissions violation for %s", subject)
		}
	}
}

func TestBrokerWorker(t *testing.T) {
	b := startBroker(t, Config{Dir: t.TempDir(), Addr: "127.0.0.1:0", Password: testPassword})
	require.NotNil(t, b.srv.Addr())

	_, _, err := worker(t, b, "wrong")
	require.ErrorIs(t, err, nats.ErrAuthorization)
	nc, errs, err := worker(t, b, testPassword, nats.CustomInboxPrefix(bus.WorkerInbox))
	require.NoError(t, err)
	js, err := jetstream.New(nc)
	require.NoError(t, err)

	t.Run("allowed", func(t *testing.T) {
		for _, subject := range []string{bus.SubjectSightAnnounce, bus.SubjectSightSample, bus.SubjectFetchResults, bus.SubjectProbeResults} {
			_, err := js.Publish(t.Context(), subject, []byte("x"))
			require.NoError(t, err, subject)
		}
		for _, orders := range []struct {
			stream   string
			subjects [bus.Priorities]string
		}{{bus.StreamFetchOrders, bus.FetchOrders}, {bus.StreamProbeOrders, bus.ProbeOrders}} {
			for p, subject := range orders.subjects {
				_, err := b.JetStream.Publish(t.Context(), subject, []byte(subject))
				require.NoError(t, err)
				c, err := js.Consumer(t.Context(), orders.stream, bus.OrderConsumers[p])
				require.NoError(t, err, subject)
				batch, err := c.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
				require.NoError(t, err, subject)
				var got []string
				for m := range batch.Messages() {
					got = append(got, string(m.Data()))
					require.NoError(t, m.InProgress(), subject)
					require.NoError(t, m.DoubleAck(t.Context()), subject)
				}
				require.NoError(t, batch.Error(), subject)
				require.Equal(t, []string{subject}, got)
			}
			s, err := b.JetStream.Stream(t.Context(), orders.stream)
			require.NoError(t, err)
			info, err := s.Info(t.Context())
			require.NoError(t, err)
			require.Zero(t, info.State.Msgs, "acked orders leave the work queue")
		}
		stored, err := nc.SubscribeSync(bus.SubjectStored)
		require.NoError(t, err)
		require.NoError(t, nc.Flush())
		require.NoError(t, b.Conn.Publish(bus.SubjectStored, []byte("hashes")))
		m, err := stored.NextMsg(5 * time.Second)
		require.NoError(t, err)
		require.Equal(t, "hashes", string(m.Data))
		select {
		case err := <-errs:
			t.Fatalf("worker error: %v", err)
		default:
		}
	})

	t.Run("refused", func(t *testing.T) {
		for _, subject := range []string{
			bus.SubjectFetchP0,
			bus.SubjectProbeP3,
			"$JS.API.STREAM.CREATE.X",
			"$JS.API.STREAM.DELETE." + bus.StreamFetchOrders,
			"$JS.API.STREAM.PURGE." + bus.StreamSightings,
			"$JS.API.STREAM.MSG.GET." + bus.StreamSightings,
			"$JS.API.CONSUMER.CREATE." + bus.StreamFetchOrders + ".x",
			"$JS.API.CONSUMER.DELETE." + bus.StreamFetchOrders + ".p0",
			"$JS.API.CONSUMER.MSG.NEXT." + bus.StreamSightings + "." + bus.ConsumerAnnounce,
			"$JS.API.INFO",
		} {
			require.NoError(t, nc.Publish(subject, nil))
			require.NoError(t, nc.Flush())
			refused(t, errs, subject)
		}
		for _, subject := range []string{"_INBOX.>", bus.SubjectSightAnnounce, bus.SubjectFetchP0, ">"} {
			_, err := nc.SubscribeSync(subject)
			require.NoError(t, err)
			require.NoError(t, nc.Flush())
			refused(t, errs, subject)
		}
	})
	stopBroker(t, b)
}

func TestOptions(t *testing.T) {
	_, err := options(Config{Dir: "d", Addr: ":4222"}, "p")
	require.Error(t, err, "a listener needs the worker password")
	opts, err := options(Config{Dir: "d", Addr: "127.0.0.1:0", Password: "w"}, "p")
	require.NoError(t, err)
	require.Equal(t, -1, opts.Port, "port 0 picks a free one")
	require.True(t, opts.NoSigs)
	require.Zero(t, opts.HTTPPort, "no monitoring port")
	opts, err = options(Config{Dir: "d"}, "p")
	require.NoError(t, err)
	require.True(t, opts.DontListen)
	require.Len(t, opts.Users, 1)
}

func TestModule(t *testing.T) {
	var b *Broker
	app := fxtest.New(t,
		fx.Supply(Config{Dir: t.TempDir()}, slog.New(slog.DiscardHandler)),
		Module,
		fx.Populate(&b),
	)
	app.RequireStart()
	require.True(t, b.Conn.IsConnected())
	app.RequireStop()
	require.False(t, b.srv.Running())
}
