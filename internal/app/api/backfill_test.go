package api

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

func TestBackfill(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	defer db.Close()
	s, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	h := store.Hash{1}
	_, _, err = s.Insert(ctx, store.Torrent{Hash: h, Name: "Severance.S02E05.1080p.WEB-DL.Rus", Size: 1 << 30, Discovery: "sample", SeenAt: time.Now(),
		Audio: []store.Audio{{Language: "ru", Marker: true}}})
	require.NoError(t, err)

	backfill(ctx, s, slog.New(slog.DiscardHandler))
	r, err := s.Get(ctx, h)
	require.NoError(t, err)
	require.Equal(t, 5, r.Episode)
	require.Equal(t, "Rus", r.Audio[0].Mark)
}
