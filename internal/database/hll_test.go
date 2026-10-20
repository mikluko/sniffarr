package database

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/sketch"
)

// peers returns a sketch of n peers numbered from first.
func peers(first, n int) []byte {
	var k sketch.Key
	s := sketch.New()
	for i := first; i < first+n; i++ {
		s.Add(k.Hash(netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), 6881)))
	}
	return s.Bytes()
}

func TestHLLFunctions(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.W.ExecContext(ctx, "CREATE TABLE s (k INTEGER, sketch BLOB)")
	require.NoError(t, err)
	for i, b := range [][]byte{peers(0, 100), peers(50, 100), peers(100, 100), nil} {
		_, err = db.W.ExecContext(ctx, "INSERT INTO s (k, sketch) VALUES (?, ?)", i, b)
		require.NoError(t, err)
	}

	var card int64
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT hll_card(sketch) FROM s WHERE k = 0").Scan(&card))
	require.InDelta(t, 100, card, 10, "a sketch round-trips through the table")
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT hll_card(hll_union((SELECT sketch FROM s WHERE k = 0), (SELECT sketch FROM s WHERE k = 1)))").Scan(&card))
	require.InDelta(t, 150, card, 15, "hll_union unions")
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT hll_card(hll_union_agg(sketch)) FROM s").Scan(&card))
	require.InDelta(t, 200, card, 20, "hll_union_agg unions every row and skips NULL")
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT hll_card(hll_union(sketch, NULL)) FROM s WHERE k = 2").Scan(&card))
	require.InDelta(t, 100, card, 10, "hll_union with NULL is the other side")
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT hll_card(hll_union_agg(sketch)) FROM s WHERE k = 3").Scan(&card))
	require.Zero(t, card, "the union of nothing has no peers")
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT hll_card(NULL)").Scan(&card))
	require.Zero(t, card)

	var blob []byte
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT hll_union_agg(sketch) FROM s WHERE k = 3").Scan(&blob))
	require.Nil(t, blob, "the union of nothing is NULL")
	err = db.R.QueryRowContext(ctx, "SELECT hll_card(x'0102')").Scan(&card)
	require.Error(t, err, "garbage is not a sketch")
}
