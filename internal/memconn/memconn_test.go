package memconn

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/auth"
)

// echo is an IngestService that counts sightings and answers every unit of
// credit with a task.
type echo struct {
	sniffarrv1connect.UnimplementedIngestServiceHandler
}

func (echo) Sight(_ context.Context, stream *connect.ClientStream[sniffarrv1.SightRequest]) (*connect.Response[sniffarrv1.SightResponse], error) {
	var n int64
	for stream.Receive() {
		n++
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return connect.NewResponse(&sniffarrv1.SightResponse{New: n}), nil
}

func (echo) Fetch(_ context.Context, stream *connect.BidiStream[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse]) error {
	for {
		req, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if c := req.GetCredit(); c != nil {
			for range c.GetN() {
				if err := stream.Send(&sniffarrv1.FetchResponse{Task: &sniffarrv1.Task{Infohash: make([]byte, 20)}}); err != nil {
					return err
				}
			}
		}
	}
}

// serve serves the ingest API behind the token "secret" on a Listener.
func serve(t *testing.T) *Listener {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(echo{}, connect.WithInterceptors(auth.NewBearer("secret"))))
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: mux, Protocols: &p}
	l := Listen()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	t.Cleanup(func() {
		require.NoError(t, srv.Shutdown(context.Background()))
		require.ErrorIs(t, <-done, http.ErrServerClosed)
	})
	return l
}

func TestConnectRoundTrip(t *testing.T) {
	l := serve(t)
	ctx := context.Background()
	client := sniffarrv1connect.NewIngestServiceClient(l.Client(), URL, connect.WithInterceptors(auth.Client("secret")))

	sight := client.Sight(ctx)
	for range 3 {
		require.NoError(t, sight.Send(&sniffarrv1.SightRequest{Infohash: make([]byte, 20)}))
	}
	res, err := sight.CloseAndReceive()
	require.NoError(t, err)
	require.EqualValues(t, 3, res.Msg.GetNew())

	fetch := client.Fetch(ctx)
	require.NoError(t, fetch.Send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Credit{Credit: &sniffarrv1.Credit{N: 2}}}))
	for range 2 {
		task, err := fetch.Receive()
		require.NoError(t, err)
		require.Len(t, task.GetTask().GetInfohash(), 20)
	}
	require.NoError(t, fetch.CloseRequest())
	_, err = fetch.Receive()
	require.ErrorIs(t, err, io.EOF)
	require.NoError(t, fetch.CloseResponse())
}

func TestTokenChecked(t *testing.T) {
	l := serve(t)
	client := sniffarrv1connect.NewIngestServiceClient(l.Client(), URL, connect.WithInterceptors(auth.Client("wrong")))
	sight := client.Sight(context.Background())
	_, err := sight.CloseAndReceive()
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestClosedListener(t *testing.T) {
	l := Listen()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := l.DialContext(ctx, "tcp", URL)
	require.ErrorIs(t, err, context.DeadlineExceeded, "a dial nobody accepts ends with its context")

	require.NoError(t, l.Close())
	require.NoError(t, l.Close(), "closing twice is harmless")
	_, err = l.Accept()
	require.ErrorIs(t, err, net.ErrClosed)
	_, err = l.DialContext(context.Background(), "tcp", URL)
	require.ErrorIs(t, err, net.ErrClosed)
	require.Equal(t, Network, l.Addr().Network())
}
