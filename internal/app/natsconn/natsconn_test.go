package natsconn

import (
	"log/slog"
	"net"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"sarak.sh/mikluko/sniffarr/internal/app/broker"
	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

var discard = slog.New(slog.DiscardHandler)

func TestNone(t *testing.T) {
	var c *Conn
	app := fxtest.New(t, fx.Supply(Config{}, discard), Module, fx.Populate(&c))
	app.RequireStart()
	require.Nil(t, c.NATS())
	app.RequireStop()
}

func TestInProcess(t *testing.T) {
	var c *Conn
	var b *broker.Broker
	app := fxtest.New(t,
		fx.Supply(Config{URL: "nats://elsewhere:4222", Password: "pw"}, broker.Config{Dir: t.TempDir()}, discard),
		broker.Module, Module, fx.Populate(&c, &b))
	app.RequireStart()
	require.Same(t, b.Conn, c.NATS(), "the API's own connection, whatever the URL")
	app.RequireStop()
}

func TestRemote(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	api := fxtest.New(t, fx.Supply(broker.Config{Dir: t.TempDir(), Addr: addr, Password: "pw"}, discard), broker.Module, fx.Invoke(func(*broker.Broker) {}))
	api.RequireStart()
	defer api.RequireStop()
	url := "nats://" + addr

	var c *Conn
	app := fxtest.New(t, fx.Supply(Config{URL: url, Password: "pw"}, discard), Module, fx.Populate(&c))
	app.RequireStart()
	js, err := jetstream.New(c.NATS())
	require.NoError(t, err)
	m, _ := bus.SightingMsg([20]byte{1}, store.Sample)
	_, err = js.PublishMsg(t.Context(), m)
	require.NoError(t, err, "the worker publishes a sighting, its acknowledgement under the worker's inbox")
	app.RequireStop()
	require.True(t, c.NATS().IsClosed())
}

func TestRemoteUnreachable(t *testing.T) {
	var c *Conn
	app := fxtest.New(t, fx.Supply(Config{URL: "nats://127.0.0.1:1", Password: "pw"}, discard), Module, fx.Populate(&c))
	app.RequireStart()
	require.False(t, c.NATS().IsConnected(), "the start does not wait for the server")
	app.RequireStop()
}
