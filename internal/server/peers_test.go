package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/auth"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/memconn"
	"github.com/mikluko/sniffarr/internal/queue"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/sketch"
	"github.com/mikluko/sniffarr/internal/sniff"
	"github.com/mikluko/sniffarr/internal/store"
)

// serveMem serves the handler over memconn with Alien stored, and returns
// the store and an ingest client of the in-process API.
func serveMem(t *testing.T) (*store.Store, sniffarrv1connect.IngestServiceClient) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	q, err := queue.New(ctx, db, queue.Options{})
	require.NoError(t, err)
	_, _, err = s.Insert(ctx, store.Torrent{Hash: store.Hash{1}, Name: "Alien.1979.1080p", Size: 8 << 30, Discovery: "sample", SeenAt: time.Now(),
		Files: []rules.File{{Path: "Alien.mkv", Size: 8 << 30}}})
	require.NoError(t, err)
	h, err := New(Config{Store: s, Queue: q, IngestToken: "ingest", SearchToken: "search", Log: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	mem := memconn.Listen()
	srv := &http.Server{Handler: h, Protocols: Protocols()}
	go func() { _ = srv.Serve(mem) }()
	t.Cleanup(func() { _ = srv.Close() })
	return s, sniffarrv1connect.NewIngestServiceClient(mem.Client(), memconn.URL, connect.WithInterceptors(auth.Client("ingest")))
}

func TestPeersEndToEnd(t *testing.T) {
	ctx := context.Background()
	s, client := serveMem(t)
	now := time.Now()
	p := sniff.NewPeers(sniff.PeersConfig{Ingest: client, Flush: time.Hour, Backoff: time.Millisecond, Now: func() time.Time { return now }})
	pctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(pctx) }()
	key, err := s.HashKey(ctx)
	require.NoError(t, err)
	res, err := client.HashKey(ctx, connect.NewRequest(&sniffarrv1.HashKeyRequest{}))
	require.NoError(t, err)
	require.Equal(t, key[:], res.Msg.GetKey(), "the client gets the store's key")

	peers := make([]netip.AddrPort, 200)
	for i := range peers {
		peers[i] = netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), 6881)
	}
	select {
	case <-p.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("the key never came")
	}
	p.Add(sniff.Hash{1}, peers[:100])
	p.Add(sniff.Hash{1}, peers[100:])
	p.Add(sniff.Hash{2}, peers[:10])
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	r, err := s.Get(ctx, store.Hash{1})
	require.NoError(t, err)
	c, err := s.Peers(ctx, r.ID, now)
	require.NoError(t, err)
	require.InDelta(t, 200, c.Day, 20, "the flushed sketch is stored under the torrent and the hour")
	require.Equal(t, c.Day, c.Week)

	stream := client.Peers(ctx)
	sk := sketch.New()
	for _, a := range peers[:50] {
		sk.Add(key.Hash(a))
	}
	require.NoError(t, stream.Send(&sniffarrv1.PeersRequest{Infohash: hash(1), Hour: now.Add(-time.Hour).Unix(), Sketch: sk.Bytes()}))
	require.NoError(t, stream.Send(&sniffarrv1.PeersRequest{Infohash: hash(9), Hour: now.Unix(), Sketch: sk.Bytes()}))
	pres, err := stream.CloseAndReceive()
	require.NoError(t, err)
	require.Equal(t, int64(1), pres.Msg.GetMerged())
	require.Equal(t, int64(1), pres.Msg.GetDropped(), "a sketch of an unknown infohash is dropped")
	c, err = s.Peers(ctx, r.ID, now)
	require.NoError(t, err)
	require.InDelta(t, 200, c.Day, 20, "the earlier hour's peers are among the day's already")

	stream = client.Peers(ctx)
	require.NoError(t, stream.Send(&sniffarrv1.PeersRequest{Infohash: hash(1), Hour: now.Unix(), Sketch: []byte{1, 2, 3}}))
	_, err = stream.CloseAndReceive()
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "garbage is not a sketch")
	stream = client.Peers(ctx)
	require.NoError(t, stream.Send(&sniffarrv1.PeersRequest{Infohash: []byte{1}, Hour: now.Unix(), Sketch: sk.Bytes()}))
	_, err = stream.CloseAndReceive()
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "an infohash is 20 bytes")
}
