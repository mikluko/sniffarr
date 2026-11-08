// Package bustest runs a NATS server with the bus topology for tests.
package bustest

import (
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/bus"
)

// Conn starts a server with JetStream in a temporary directory, with the bus
// topology ensured, and returns an in-process connection to it; both are
// closed at cleanup.
func Conn(t testing.TB) *nats.Conn {
	t.Helper()
	srv, err := server.NewServer(&server.Options{JetStream: true, StoreDir: t.TempDir(), DontListen: true, NoSigs: true})
	require.NoError(t, err)
	go srv.Start()
	t.Cleanup(func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	})
	require.True(t, srv.ReadyForConnections(10*time.Second))
	nc, err := nats.Connect("", nats.InProcessServer(srv))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	require.NoError(t, bus.Ensure(t.Context(), js))
	return nc
}
