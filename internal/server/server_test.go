package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/auth"
	"github.com/mikluko/sniffarr/internal/rot13"
	"github.com/mikluko/sniffarr/internal/store"
)

func start(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	srv := httptest.NewUnstartedServer(New(Config{Store: s, IngestToken: "ingest", SearchToken: "search", Log: slog.New(slog.DiscardHandler)}))
	srv.Config.Protocols = Protocols()
	srv.Start()
	t.Cleanup(srv.Close)
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return srv, &http.Client{Transport: &http.Transport{Protocols: &p}}
}

func hash(b byte) []byte {
	h := make([]byte, 20)
	h[0] = b
	return h
}

func movie(b byte, name string, ext string) *sniffarrv1.Torrent {
	return &sniffarrv1.Torrent{
		Infohash: hash(b),
		Name:     name,
		Size:     8 << 30,
		Files:    []*sniffarrv1.File{{Path: name + "/" + name + "." + ext, Size: 8 << 30}},
		SeenAt:   timestamppb.New(time.Now().Add(-time.Minute)),
		Source:   sniffarrv1.Source_SOURCE_ANNOUNCE,
	}
}

func submit(t *testing.T, c sniffarrv1connect.IngestServiceClient, ts ...*sniffarrv1.Torrent) (*sniffarrv1.SubmitResponse, error) {
	t.Helper()
	stream := c.Submit(context.Background())
	for _, tr := range ts {
		if err := stream.Send(&sniffarrv1.SubmitRequest{Torrent: tr}); err != nil {
			break
		}
	}
	res, err := stream.CloseAndReceive()
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func TestIngestAndSearch(t *testing.T) {
	t.Run("connect", func(t *testing.T) { testIngestAndSearch(t, nil) })
	t.Run("grpc", func(t *testing.T) { testIngestAndSearch(t, connect.WithGRPC()) })
}

func testIngestAndSearch(t *testing.T, protocol connect.ClientOption) {
	srv, hc := start(t)
	ctx := context.Background()
	opts := func(token string) []connect.ClientOption {
		o := []connect.ClientOption{connect.WithInterceptors(auth.Client(token))}
		if protocol != nil {
			o = append(o, protocol)
		}
		return o
	}
	ingest := sniffarrv1connect.NewIngestServiceClient(hc, srv.URL, opts("ingest")...)
	search := sniffarrv1connect.NewSearchServiceClient(hc, srv.URL, opts("search")...)

	res, err := submit(t, ingest,
		movie(1, "The.Thing.1982.1080p.BluRay.x264.Rus.Eng", "mkv"),
		movie(2, rot13.String("Ovt.Cnpxntr.KKK.1080c"), "mp4"),
		movie(3, "Some.Tool.Portable", "exe"),
		movie(4, "Mad.Max.Fury.Road.2015.2160p.WEB-DL", "mkv"),
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), res.GetAccepted())
	require.Equal(t, int64(2), res.GetRejected())

	res, err = submit(t, ingest, movie(1, "The.Thing.1982.1080p.BluRay.x264.Rus.Eng", "mkv"), movie(2, rot13.String("Ovt.Cnpxntr.KKK.1080c"), "mp4"))
	require.NoError(t, err)
	require.Equal(t, int64(2), res.GetDuplicate())

	unknown, err := ingest.Unknown(ctx, connect.NewRequest(&sniffarrv1.UnknownRequest{Infohashes: [][]byte{hash(1), hash(2), hash(3), hash(5)}}))
	require.NoError(t, err)
	require.Equal(t, [][]byte{hash(5)}, unknown.Msg.GetInfohashes())

	_, err = ingest.Unknown(ctx, connect.NewRequest(&sniffarrv1.UnknownRequest{Infohashes: [][]byte{{1, 2, 3}}}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	found, err := search.Search(ctx, connect.NewRequest(&sniffarrv1.SearchRequest{Query: "the thing 1982"}))
	require.NoError(t, err)
	require.Len(t, found.Msg.GetReleases(), 1)
	r := found.Msg.GetReleases()[0]
	require.Equal(t, hash(1), r.GetInfohash())
	require.Equal(t, int64(1), r.GetFileCount())
	require.Equal(t, sniffarrv1.Kind_KIND_UNSPECIFIED, r.GetKind())
	require.InDelta(t, 1.0, r.GetRussianAudio(), 1e-9, "the explicit rus marker")
	require.Equal(t, "magnet:?xt=urn:btih:0100000000000000000000000000000000000000&dn=The.Thing.1982.1080p.BluRay.x264.Rus.Eng", r.GetMagnet())

	found, err = search.Search(ctx, connect.NewRequest(&sniffarrv1.SearchRequest{MinRussianAudio: new(float32(0.5))}))
	require.NoError(t, err)
	require.Len(t, found.Msg.GetReleases(), 1)

	got, err := search.Get(ctx, connect.NewRequest(&sniffarrv1.GetRequest{Infohash: hash(4)}))
	require.NoError(t, err)
	require.Equal(t, "Mad.Max.Fury.Road.2015.2160p.WEB-DL", got.Msg.GetRelease().GetName())
	require.Nil(t, got.Msg.GetRelease().RussianAudio)

	_, err = search.Get(ctx, connect.NewRequest(&sniffarrv1.GetRequest{Infohash: hash(2)}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "rejected torrents are not stored")
}

func TestAuth(t *testing.T) {
	srv, hc := start(t)
	ctx := context.Background()
	for name, token := range map[string]string{"none": "", "wrong": "nope", "other group": "search"} {
		t.Run(name, func(t *testing.T) {
			var opts []connect.ClientOption
			if token != "" {
				opts = append(opts, connect.WithInterceptors(auth.Client(token)))
			}
			ingest := sniffarrv1connect.NewIngestServiceClient(hc, srv.URL, opts...)
			_, err := ingest.Unknown(ctx, connect.NewRequest(&sniffarrv1.UnknownRequest{}))
			require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
			_, err = submit(t, ingest, movie(1, "The.Thing.1982", "mkv"))
			require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		})
	}
	search := sniffarrv1connect.NewSearchServiceClient(hc, srv.URL, connect.WithInterceptors(auth.Client("ingest")))
	_, err := search.Search(ctx, connect.NewRequest(&sniffarrv1.SearchRequest{}))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	res, err := hc.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
}
