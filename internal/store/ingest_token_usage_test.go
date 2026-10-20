package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokens(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	n, err := s.TokensUsed(ctx, "2026-10-01")
	require.NoError(t, err)
	require.Zero(t, n)
	require.NoError(t, s.AddTokens(ctx, "2026-10-01", 300))
	require.NoError(t, s.AddTokens(ctx, "2026-10-01", 200))
	n, err = s.TokensUsed(ctx, "2026-10-01")
	require.NoError(t, err)
	require.EqualValues(t, 500, n)
}
