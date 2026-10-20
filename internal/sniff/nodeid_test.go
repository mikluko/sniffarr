package sniff

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// levels is a slog.Handler that counts records by level.
type levels struct {
	mu    sync.Mutex
	count map[slog.Level]int
}

func (l *levels) Enabled(context.Context, slog.Level) bool { return true }

func (l *levels) Handle(_ context.Context, rec slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count == nil {
		l.count = map[slog.Level]int{}
	}
	l.count[rec.Level]++
	return nil
}

func (l *levels) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *levels) WithGroup(string) slog.Handler      { return l }

func TestObtainNodeIDRetries(t *testing.T) {
	ingest := newFake()
	ingest.failKey = 3
	log := &levels{}
	id, err := ObtainNodeID(context.Background(), ingest.client(t), time.Millisecond, slog.New(log))
	require.NoError(t, err)
	require.Equal(t, ingest.nodeID, id)
	log.mu.Lock()
	defer log.mu.Unlock()
	require.Equal(t, 2, log.count[slog.LevelWarn], "every failure after the first warns")
	require.Equal(t, 1, log.count[slog.LevelDebug], "the first does not")
}

func TestObtainNodeIDStopsWithContext(t *testing.T) {
	ingest := newFake()
	ingest.failKey = 1 << 20
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := ObtainNodeID(ctx, ingest.client(t), time.Millisecond, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestObtainNodeIDRejectsShortID(t *testing.T) {
	ingest := newFake()
	ingest.nodeID = [20]byte{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := ObtainNodeID(ctx, ingest.client(t), time.Millisecond, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded, "a zero ID is retried, not used")
}
