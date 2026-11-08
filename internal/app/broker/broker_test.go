package broker

import (
	"context"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"sarak.sh/mikluko/sniffarr/internal/bus"
)

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
	require.Nil(t, b.srv.Addr(), "no listener")
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

func TestOptions(t *testing.T) {
	opts := options(Config{Dir: "d"}, "p")
	require.True(t, opts.DontListen)
	require.True(t, opts.NoSigs)
	require.Zero(t, opts.HTTPPort, "no monitoring port")
	require.Len(t, opts.Users, 1)
	require.Equal(t, Domain, opts.JetStreamDomain)
	require.Empty(t, opts.LeafNode.Remotes, "standalone")

	up := &url.URL{Scheme: "tls", User: url.User("leaf"), Host: "hub:7422"}
	opts = options(Config{Dir: "d", Upstream: up, Password: "pw"}, "p")
	require.Len(t, opts.LeafNode.Remotes, 1)
	r := opts.LeafNode.Remotes[0]
	require.Equal(t, "tls://leaf:pw@hub:7422", r.URLs[0].String())
	require.True(t, r.TLS)
	require.Equal(t, "tls://leaf@hub:7422", up.String(), "the configured URL keeps no password")

	opts = options(Config{Dir: "d", Upstream: &url.URL{Scheme: "nats-leaf", Host: "hub:7422"}, Creds: "/run/leaf.creds"}, "p")
	r = opts.LeafNode.Remotes[0]
	require.Equal(t, "nats-leaf://hub:7422", r.URLs[0].String())
	require.Equal(t, "/run/leaf.creds", r.Credentials)
	require.False(t, r.TLS)
}

// startHub starts a server with JetStream and a leafnode listener on
// loopback for the user leaf with password pw, and returns its leafnode
// URL with that user.
func startHub(t *testing.T) (*server.Server, *url.URL) {
	t.Helper()
	hub, err := server.NewServer(&server.Options{
		ServerName: "hub", Host: "127.0.0.1", Port: server.RANDOM_PORT, NoSigs: true,
		JetStream: true, StoreDir: t.TempDir(),
		LeafNode: server.LeafNodeOpts{Host: "127.0.0.1", Port: server.RANDOM_PORT, Username: "leaf", Password: "pw"},
	})
	require.NoError(t, err)
	go hub.Start()
	t.Cleanup(func() {
		hub.Shutdown()
		hub.WaitForShutdown()
	})
	require.True(t, hub.ReadyForConnections(10*time.Second))
	v, err := hub.Varz(nil)
	require.NoError(t, err)
	return hub, &url.URL{Scheme: "nats-leaf", User: url.User("leaf"), Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(v.LeafNode.Port))}
}

func TestBrokerLeaf(t *testing.T) {
	hub, up := startHub(t)
	b := startBroker(t, Config{Dir: t.TempDir(), Upstream: up, Password: "pw"})
	require.Eventually(t, func() bool { return hub.NumLeafNodes() == 1 }, 10*time.Second, 10*time.Millisecond, "the leaf connects")
	nc, err := nats.Connect(hub.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	api, err := nc.SubscribeSync("$JS.API.>")
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	probe, err := nc.SubscribeSync("probe")
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	require.Eventually(t, func() bool {
		_ = b.Conn.Publish("probe", nil)
		_, err := probe.NextMsg(10 * time.Millisecond)
		return err == nil
	}, 10*time.Second, time.Millisecond, "the hub's interest reaches the leaf")

	_, err = b.JetStream.Publish(t.Context(), bus.SubjectFetchP1, []byte("order"))
	require.NoError(t, err)
	s, err := b.JetStream.Stream(t.Context(), bus.StreamFetchOrders)
	require.NoError(t, err)
	info, err := s.Info(t.Context())
	require.NoError(t, err)
	require.Equal(t, uint64(1), info.State.Msgs, "the streams answer locally")
	require.ElementsMatch(t, wantStreams(), streamNames(t, b.JetStream))
	_, err = api.NextMsg(100 * time.Millisecond)
	require.ErrorIs(t, err, nats.ErrTimeout, "no JetStream request reaches the hub")

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	require.Empty(t, streamNames(t, js), "the hub's JetStream has none of the streams")
	stopBroker(t, b)
}

func TestBrokerLeafWrongPassword(t *testing.T) {
	hub, up := startHub(t)
	b := startBroker(t, Config{Dir: t.TempDir(), Upstream: up, Password: "wrong"})
	require.Never(t, func() bool { return hub.NumLeafNodes() > 0 }, 300*time.Millisecond, 10*time.Millisecond)
	_, err := b.JetStream.Stream(t.Context(), bus.StreamFetchOrders)
	require.NoError(t, err, "the API runs without its upstream")
	stopBroker(t, b)
}

func TestReadyWait(t *testing.T) {
	require.Equal(t, RecoveryBudget, readyWait(context.Background()), "no deadline")
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	require.Equal(t, RecoveryBudget, readyWait(ctx), "a later deadline")
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.LessOrEqual(t, readyWait(ctx), time.Second, "an earlier deadline")
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
