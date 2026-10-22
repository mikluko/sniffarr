package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/store"
)

// blockingClassifier signals each classification on started and holds it
// until release closes, then signals it on done.
type blockingClassifier struct {
	started, done chan int64
	release       chan struct{}
}

func (c *blockingClassifier) Classify(ctx context.Context, p store.Pending) error {
	c.started <- p.ID
	select {
	case <-c.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	c.done <- p.ID
	return nil
}

func TestTorznabDoesNotWait(t *testing.T) {
	jev := &blockingClassifier{started: make(chan int64, 1), done: make(chan int64, 1), release: make(chan struct{})}
	lazy := &search.Lazy{Classifier: jev, Wait: time.Minute, Log: slog.New(slog.DiscardHandler)}
	srv, hc := start(t, Config{Poll: 20 * time.Millisecond, Languages: russian, TorznabToken: "tz", Lazy: lazy})

	ingest := ingestClient(srv, hc)
	_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1)
	require.NoError(t, err)
	f := openFetch(t, ingest)
	f.credit(1)
	f.expect(1)
	f.torrent(1, "The.Thing.1982.1080p.BluRay.x264", "The.Thing.1982.mkv")
	f.finish()

	begin := time.Now()
	res, err := hc.Get(srv.URL + "/torznab/api?t=search&q=the+thing&apikey=tz")
	require.NoError(t, err)
	b, err := io.ReadAll(res.Body)
	res.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Less(t, time.Since(begin), 10*time.Second, "answered before the classification, held, or Lazy.Wait")
	require.Contains(t, string(b), "The.Thing.1982.1080p.BluRay.x264", "with what the store holds")

	select {
	case <-jev.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the classification did not start")
	}
	close(jev.release)
	select {
	case <-jev.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the classification did not run past the answer")
	}
}
