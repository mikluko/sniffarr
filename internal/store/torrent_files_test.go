package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/rules"
)

func TestMaxFiles(t *testing.T) {
	ctx := context.Background()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	defer db.Close()
	s.opts.MaxFiles = 2
	tr := torrent(1, "Collection")
	tr.Audio = []Audio{{Language: "ru", ScriptHint: true}}
	for i, size := range []int64{10, 30, 20, 5} {
		tr.Files = append(tr.Files, rules.File{Path: fmt.Sprintf("f%d.mkv", i), Size: size})
	}
	_, _, err := s.Insert(ctx, tr)
	require.NoError(t, err)

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.EqualValues(t, 4, r.FileCount, "the count stays true")
	p, err := s.Unclassified(ctx, 0, 1, 10)
	require.NoError(t, err)
	require.Equal(t, []rules.File{{Path: "f1.mkv", Size: 30}, {Path: "f2.mkv", Size: 20}}, p[0].Files, "the largest, largest first")
	got, err := s.Search(ctx, Query{Text: "f0", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, got, "a file not stored is not searched")
}
