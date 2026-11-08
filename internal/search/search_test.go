package search

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

func ExampleMagnet() {
	h := store.Hash{0xde, 0xad, 0xbe, 0xef}
	fmt.Println(Magnet(h, "Чужой (1979) & Aliens+"))
	// Output: magnet:?xt=urn:btih:deadbeef00000000000000000000000000000000&dn=%D0%A7%D1%83%D0%B6%D0%BE%D0%B9%20%281979%29%20%26%20Aliens%2B
}

func TestSearchSeason(t *testing.T) {
	s, _ := setup(t,
		store.Torrent{Name: "Fallout.S01E03", Season: 1, Episode: 3},
		store.Torrent{Name: "Fallout.S01", Season: 1},
		store.Torrent{Name: "Fallout.S02E03", Season: 2, Episode: 3},
		store.Torrent{Name: "Fallout.2024"},
	)
	svc := &Service{Store: s, Log: slog.New(slog.DiscardHandler)}
	require.ElementsMatch(t, []string{"Fallout.S01E03", "Fallout.S01"}, keys(search(t, svc, &sniffarrv1.SearchRequest{Season: new(int32(1))})))
	require.ElementsMatch(t, []string{"Fallout.S02E03"}, keys(search(t, svc, &sniffarrv1.SearchRequest{Season: new(int32(2)), Episode: new(int32(3))})))
	for _, req := range []*sniffarrv1.SearchRequest{
		{Season: new(int32(-1))},
		{Episode: new(int32(3))},
		{Season: new(int32(1)), Episode: new(int32(0))},
	} {
		_, err := svc.Search(context.Background(), connect.NewRequest(req))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), req.String())
	}
}

func TestWorkID(t *testing.T) {
	require.Equal(t, "m348", WorkID(store.Work{TMDBID: 348, Details: store.Details{Type: store.TypeMovie}}))
	require.Equal(t, "tv1399", WorkID(store.Work{TMDBID: 1399, Details: store.Details{Type: store.TypeTV}}))
	for id, want := range map[string]struct {
		typ string
		n   int
		ok  bool
	}{
		"m348": {store.TypeMovie, 348, true}, "tv1399": {store.TypeTV, 1399, true},
		"348": {}, "m": {}, "tv": {}, "m0": {}, "m-1": {}, "m+1": {}, "m01": {}, "x348": {},
	} {
		typ, n, ok := ParseWorkID(id)
		require.Equal(t, want, struct {
			typ string
			n   int
			ok  bool
		}{typ, n, ok}, id)
	}
}
