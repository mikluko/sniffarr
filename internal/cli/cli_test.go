package cli

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatus(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	ok := func(context.Context, int, *slog.Logger) error { return nil }
	fail := func(context.Context, int, *slog.Logger) error { return errors.New("boom") }
	loaded := func() (int, error) { return 1, nil }

	require.Equal(t, 0, Status(loaded, ok, log))
	require.Equal(t, 1, Status(loaded, fail, log))
	require.Equal(t, 2, Status(func() (int, error) { return 0, errors.New("bad") }, ok, log))
	require.Equal(t, 0, Status(func() (int, error) { return 0, flag.ErrHelp }, fail, log))
}
