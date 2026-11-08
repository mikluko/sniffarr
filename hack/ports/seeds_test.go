package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
)

// journalStub pages through entries, oldest first, as BrowseService does.
type journalStub struct {
	sniffarrv1connect.UnimplementedBrowseServiceHandler
	entries []*sniffarrv1.JournalEntry
	limit   int
}

func (s *journalStub) Journal(_ context.Context, req *connect.Request[sniffarrv1.JournalRequest]) (*connect.Response[sniffarrv1.JournalResponse], error) {
	if req.Msg.GetKind() != "probed" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("kind %q", req.Msg.GetKind()))
	}
	end := len(s.entries)
	if b := req.Msg.GetBefore(); b != 0 {
		end = int(b) - 1
	}
	start := max(end-s.limit, 0)
	return connect.NewResponse(&sniffarrv1.JournalResponse{Entries: s.entries[start:end]}), nil
}

func hash(b byte) [20]byte { return [20]byte{b} }

func TestSeeds(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	stub := &journalStub{limit: 2}
	add := func(h byte, ago time.Duration, detail string) {
		hb := hash(h)
		stub.entries = append(stub.entries, &sniffarrv1.JournalEntry{
			Id: int64(len(stub.entries) + 1), At: timestamppb.New(now.Add(-ago)),
			Kind: "probed", Detail: detail, Infohash: hb[:],
		})
	}
	add(1, 3*time.Hour, `{"downloaded":10}`)
	add(2, 2*time.Hour, `{"downloaded":0}`)
	add(3, 2*time.Hour, `{}`)
	add(4, 90*time.Minute, `{"downloaded":5}`)
	add(1, time.Hour, `{"downloaded":7}`)
	add(5, 10*time.Minute, `{"downloaded":7}`)

	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewBrowseServiceHandler(stub))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := sniffarrv1connect.NewBrowseServiceClient(srv.Client(), srv.URL)

	got, err := seeds(t.Context(), c, now.Add(-30*time.Minute), 10)
	require.NoError(t, err)
	require.Equal(t, [][20]byte{hash(1), hash(4)}, got, "newest first, once each, old enough, downloaded")

	got, err = seeds(t.Context(), c, now.Add(-30*time.Minute), 1)
	require.NoError(t, err)
	require.Equal(t, [][20]byte{hash(1)}, got)

	stub.entries[0].Detail = "{"
	_, err = seeds(t.Context(), c, now, 10)
	require.ErrorContains(t, err, "journal entry 1")
}
