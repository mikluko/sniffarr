package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/auth"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/queue"
	"github.com/mikluko/sniffarr/internal/rot13"
	"github.com/mikluko/sniffarr/internal/store"
)

func start(t *testing.T, cfg Config, qopts ...queue.Options) (*httptest.Server, *http.Client) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{Languages: audio.Tags(cfg.Languages)})
	require.NoError(t, err)
	var qo queue.Options
	if len(qopts) > 0 {
		qo = qopts[0]
	}
	qo.Meter = cfg.Meter
	q, err := queue.New(ctx, db, qo)
	require.NoError(t, err)
	cfg.Queue = q
	cfg.Store, cfg.IngestToken, cfg.SearchToken, cfg.Log = s, "ingest", "search", slog.New(slog.DiscardHandler)
	h, err := New(cfg)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(h)
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

func ingestClient(srv *httptest.Server, hc *http.Client, opts ...connect.ClientOption) sniffarrv1connect.IngestServiceClient {
	return sniffarrv1connect.NewIngestServiceClient(hc, srv.URL, append(opts, connect.WithInterceptors(auth.Client("ingest")))...)
}

// droppable returns a client on connections of its own, and a func that
// breaks them as a network failure would.
func droppable(srv *httptest.Server) (sniffarrv1connect.IngestServiceClient, func()) {
	var mu sync.Mutex
	var conns []net.Conn
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	tr := &http.Transport{Protocols: &p, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err == nil {
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
		return c, err
	}}
	return ingestClient(srv, &http.Client{Transport: tr}), func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	}
}

func sight(t *testing.T, c sniffarrv1connect.IngestServiceClient, src sniffarrv1.Source, bs ...byte) (*sniffarrv1.SightResponse, error) {
	t.Helper()
	stream := c.Sight(context.Background())
	for _, b := range bs {
		if err := stream.Send(&sniffarrv1.SightRequest{Infohash: hash(b), Source: src}); err != nil {
			break
		}
	}
	res, err := stream.CloseAndReceive()
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// fetchStream is one Fetch stream with its tasks pumped into a channel.
type fetchStream struct {
	t      *testing.T
	stream *connect.BidiStreamForClient[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse]
	tasks  chan []byte
	done   chan error
}

func openFetch(t *testing.T, c sniffarrv1connect.IngestServiceClient) *fetchStream {
	t.Helper()
	f := &fetchStream{t: t, stream: c.Fetch(context.Background()), tasks: make(chan []byte, 100), done: make(chan error, 1)}
	t.Cleanup(func() { _ = f.stream.CloseResponse() })
	go func() {
		for {
			m, err := f.stream.Receive()
			if err != nil {
				f.done <- err
				return
			}
			f.tasks <- m.GetTask().GetInfohash()
		}
	}()
	return f
}

func (f *fetchStream) send(m *sniffarrv1.FetchRequest) {
	f.t.Helper()
	require.NoError(f.t, f.stream.Send(m))
}

func (f *fetchStream) credit(n int32) {
	f.t.Helper()
	f.send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Credit{Credit: &sniffarrv1.Credit{N: n}}})
}

func (f *fetchStream) torrent(b byte, name, file string) {
	f.t.Helper()
	f.send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Result{Result: &sniffarrv1.Result{
		Infohash: hash(b),
		Outcome: &sniffarrv1.Result_Torrent{Torrent: &sniffarrv1.Torrent{
			Name: name, Size: 8 << 30, Files: []*sniffarrv1.File{{Path: file, Size: 8 << 30}},
		}},
	}}})
}

func (f *fetchStream) failure(b byte, why sniffarrv1.Failure) {
	f.t.Helper()
	f.send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Result{Result: &sniffarrv1.Result{
		Infohash: hash(b), Outcome: &sniffarrv1.Result_Failure{Failure: why},
	}}})
}

// expect waits for the next tasks and requires them to be bs, in order.
func (f *fetchStream) expect(bs ...byte) {
	f.t.Helper()
	for _, b := range bs {
		select {
		case got := <-f.tasks:
			require.Equal(f.t, hash(b), got)
		case err := <-f.done:
			f.t.Fatalf("stream ended waiting for task %d: %v", b, err)
		case <-time.After(5 * time.Second):
			f.t.Fatalf("no task %d", b)
		}
	}
}

// none requires no task to arrive for a while.
func (f *fetchStream) none() {
	f.t.Helper()
	select {
	case got := <-f.tasks:
		f.t.Fatalf("unexpected task %x", got)
	case <-time.After(150 * time.Millisecond):
	}
}

// finish closes the stream's requests and waits until the server has handled
// every one of them.
func (f *fetchStream) finish() {
	f.t.Helper()
	require.NoError(f.t, f.stream.CloseRequest())
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		f.t.Fatal("the server did not end the stream")
	}
}

func TestIngestAndSearch(t *testing.T) {
	t.Run("connect", func(t *testing.T) { testIngestAndSearch(t, nil) })
	t.Run("grpc", func(t *testing.T) { testIngestAndSearch(t, connect.WithGRPC()) })
}

func testIngestAndSearch(t *testing.T, protocol connect.ClientOption) {
	srv, hc := start(t, Config{Poll: 20 * time.Millisecond, Languages: russian})
	ctx := context.Background()
	var opts []connect.ClientOption
	if protocol != nil {
		opts = append(opts, protocol)
	}
	ingest := ingestClient(srv, hc, opts...)
	search := sniffarrv1connect.NewSearchServiceClient(hc, srv.URL, append(opts, connect.WithInterceptors(auth.Client("search")))...)

	res, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1, 2, 3, 3)
	require.NoError(t, err)
	require.Equal(t, int64(3), res.GetNew())
	require.Equal(t, int64(1), res.GetKnown())
	_, err = sight(t, ingest, sniffarrv1.Source_SOURCE_ANNOUNCE, 4)
	require.NoError(t, err)
	bad := ingest.Sight(ctx)
	_ = bad.Send(&sniffarrv1.SightRequest{Infohash: []byte{1, 2, 3}, Source: sniffarrv1.Source_SOURCE_SAMPLE})
	_, err = bad.CloseAndReceive()
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	f := openFetch(t, ingest)
	f.credit(4)
	f.expect(4, 1, 2, 3)
	f.torrent(1, "The.Thing.1982.1080p.BluRay.x264.Rus.Eng", "The.Thing.1982.mkv")
	f.torrent(2, rot13.String("Ovt.Cnpxntr.KKK.1080c"), "video.mp4")
	f.torrent(3, "Some.Tool.Portable", "tool.exe")
	f.torrent(4, "Mad.Max.Fury.Road.2015.2160p.WEB-DL", "Mad.Max.Fury.Road.2015.2160p.WEB-DL.mkv")
	f.finish()

	res, err = sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1, 2, 3, 4)
	require.NoError(t, err)
	require.Equal(t, int64(4), res.GetKnown(), "stored and rejected hashes are known")

	found, err := search.Search(ctx, connect.NewRequest(&sniffarrv1.SearchRequest{Query: "the thing 1982"}))
	require.NoError(t, err)
	require.Len(t, found.Msg.GetReleases(), 1)
	r := found.Msg.GetReleases()[0]
	require.Equal(t, hash(1), r.GetInfohash())
	require.Equal(t, int64(1), r.GetFileCount())
	require.Equal(t, sniffarrv1.Kind_KIND_MOVIE, r.GetKind(), "the name tells")
	require.Len(t, r.GetAudio(), 1)
	require.True(t, r.GetAudio()[0].GetMarker())
	require.InDelta(t, 1.0, r.GetAudio()[0].GetProbability(), 1e-9, "the explicit rus marker")
	require.Equal(t, "magnet:?xt=urn:btih:0100000000000000000000000000000000000000&dn=The.Thing.1982.1080p.BluRay.x264.Rus.Eng", r.GetMagnet())

	found, err = search.Search(ctx, connect.NewRequest(&sniffarrv1.SearchRequest{AudioLanguage: new("ru"), MinAudioProbability: new(float32(0.5))}))
	require.NoError(t, err)
	require.Len(t, found.Msg.GetReleases(), 1)

	got, err := search.Get(ctx, connect.NewRequest(&sniffarrv1.GetRequest{Infohash: hash(4)}))
	require.NoError(t, err)
	require.Equal(t, "Mad.Max.Fury.Road.2015.2160p.WEB-DL", got.Msg.GetRelease().GetName())
	require.Len(t, got.Msg.GetRelease().GetAudio(), 1)
	require.Nil(t, got.Msg.GetRelease().GetAudio()[0].Probability, "no marker, no answer yet")

	_, err = search.Get(ctx, connect.NewRequest(&sniffarrv1.GetRequest{Infohash: hash(2)}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "rejected torrents are not stored")
}

var russian, _ = audio.Parse("ru")

func TestAudioHints(t *testing.T) {
	for _, tt := range []struct {
		name  string
		langs []audio.Language
		want  map[string][]*sniffarrv1.Audio
	}{
		{"ru", russian, map[string][]*sniffarrv1.Audio{
			"Нечто (1982) BDRip":            {{Language: "ru", ScriptHint: true}},
			"The.Thing.1982.BDRip.Rus":      {{Language: "ru", Marker: true, Probability: new(float32(1))}},
			"The.Thing.1982.BDRip":          {{Language: "ru"}},
			"The.Thing.1982.BDRip.LostFilm": {{Language: "ru", Marker: true, Probability: new(float32(1))}},
		}},
		{"none", nil, map[string][]*sniffarrv1.Audio{
			"Нечто (1982) BDRip":            nil,
			"The.Thing.1982.BDRip.Rus":      nil,
			"The.Thing.1982.BDRip":          nil,
			"The.Thing.1982.BDRip.LostFilm": nil,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, hc := start(t, Config{Poll: 20 * time.Millisecond, Languages: tt.langs})
			ingest := ingestClient(srv, hc)
			_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1, 2, 3, 4)
			require.NoError(t, err)
			f := openFetch(t, ingest)
			f.credit(4)
			f.expect(1, 2, 3, 4)
			f.torrent(1, "Нечто (1982) BDRip", "Nechto.avi")
			f.torrent(2, "The.Thing.1982.BDRip.Rus", "The.Thing.avi")
			f.torrent(3, "The.Thing.1982.BDRip", "The.Thing.avi")
			f.torrent(4, "The.Thing.1982.BDRip.LostFilm", "The.Thing.avi")
			f.finish()

			search := sniffarrv1connect.NewSearchServiceClient(hc, srv.URL, connect.WithInterceptors(auth.Client("search")))
			res, err := search.Search(context.Background(), connect.NewRequest(&sniffarrv1.SearchRequest{}))
			require.NoError(t, err)
			got := map[string][]*sniffarrv1.Audio{}
			for _, r := range res.Msg.GetReleases() {
				got[r.GetName()] = r.GetAudio()
			}
			require.Len(t, got, len(tt.want))
			for name, want := range tt.want {
				require.Len(t, got[name], len(want), name)
				for i := range want {
					require.True(t, proto.Equal(want[i], got[name][i]), "%s: %v", name, got[name][i])
				}
			}
		})
	}
}

func TestFetchCreditAndLeases(t *testing.T) {
	srv, hc := start(t, Config{Poll: 20 * time.Millisecond})
	ingest := ingestClient(srv, hc)
	_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1, 2, 3)
	require.NoError(t, err)

	dropped, drop := droppable(srv)
	a := openFetch(t, dropped)
	a.credit(1)
	a.expect(1)
	a.none()
	a.credit(1)
	a.expect(2)

	b := openFetch(t, ingest)
	b.credit(3)
	b.expect(3)
	b.none()

	drop()
	b.expect(1, 2)

	_, err = sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 4)
	require.NoError(t, err)
	b.none()
	b.credit(1)
	b.expect(4)
	b.finish()
}

func TestFetchLeaseExpiry(t *testing.T) {
	srv, hc := start(t, Config{Lease: 300 * time.Millisecond, Poll: 20 * time.Millisecond})
	ingest := ingestClient(srv, hc)
	_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1)
	require.NoError(t, err)

	a := openFetch(t, ingest)
	a.credit(1)
	a.expect(1)
	b := openFetch(t, ingest)
	b.credit(1)
	b.none()
	b.expect(1)
}

func TestFetchRetryAndForget(t *testing.T) {
	srv, hc := start(t, Config{Attempts: 2, Backoff: 100 * time.Millisecond, Poll: 20 * time.Millisecond}, queue.Options{DeadFor: 300 * time.Millisecond})
	ingest := ingestClient(srv, hc)
	_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1)
	require.NoError(t, err)

	f := openFetch(t, ingest)
	f.credit(1)
	f.expect(1)
	f.failure(1, sniffarrv1.Failure_FAILURE_TIMEOUT)
	f.credit(1)
	f.expect(1)
	f.failure(1, sniffarrv1.Failure_FAILURE_TIMEOUT)
	f.credit(1)
	f.none()

	res, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), res.GetKnown(), "a forgotten hash stays dead for a while")
	time.Sleep(300 * time.Millisecond)
	res, err = sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), res.GetNew(), "then queues again: it was not rejected")
	f.expect(1)
}

func TestFetchNoPeers(t *testing.T) {
	srv, hc := start(t, Config{Attempts: 2, Poll: 20 * time.Millisecond})
	ingest := ingestClient(srv, hc)
	_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1)
	require.NoError(t, err)

	f := openFetch(t, ingest)
	f.credit(1)
	f.expect(1)
	f.failure(1, sniffarrv1.Failure_FAILURE_NO_PEERS)
	f.credit(1)
	f.none()

	res, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), res.GetKnown(), "dropped at the first report, and a sample leaves it dead")
	res, err = sight(t, ingest, sniffarrv1.Source_SOURCE_ANNOUNCE, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), res.GetNew(), "an announce revives it")
	f.expect(1)
}

func TestAuth(t *testing.T) {
	srv, hc := start(t, Config{})
	ctx := context.Background()
	for name, token := range map[string]string{"none": "", "wrong": "nope", "other group": "search"} {
		t.Run(name, func(t *testing.T) {
			var opts []connect.ClientOption
			if token != "" {
				opts = append(opts, connect.WithInterceptors(auth.Client(token)))
			}
			ingest := sniffarrv1connect.NewIngestServiceClient(hc, srv.URL, opts...)
			_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1)
			require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
			stream := ingest.Fetch(ctx)
			_ = stream.Send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Credit{Credit: &sniffarrv1.Credit{N: 1}}})
			_, err = stream.Receive()
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

func TestKindLayers(t *testing.T) {
	srv, hc := start(t, Config{Poll: 20 * time.Millisecond})
	ingest := ingestClient(srv, hc)
	search := sniffarrv1connect.NewSearchServiceClient(hc, srv.URL, connect.WithInterceptors(auth.Client("search")))
	_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1, 2, 3)
	require.NoError(t, err)

	f := openFetch(t, ingest)
	f.credit(3)
	f.expect(1, 2, 3)
	f.torrent(1, "Heat.1995.1080p.BluRay.x264", "Heat.1995.mkv")
	f.torrent(2, "Heat (1995) [Rus]", "Heat.avi")
	f.torrent(3, "Unnamed Collection", "a.mkv")
	f.finish()

	res, err := search.Search(context.Background(), connect.NewRequest(&sniffarrv1.SearchRequest{}))
	require.NoError(t, err)
	got := map[string]*sniffarrv1.Release{}
	for _, r := range res.Msg.GetReleases() {
		got[r.GetName()] = r
	}
	require.Equal(t, sniffarrv1.Kind_KIND_MOVIE, got["Heat.1995.1080p.BluRay.x264"].GetKind())
	require.Equal(t, sniffarrv1.Layer_LAYER_RULES, got["Heat.1995.1080p.BluRay.x264"].GetLayer())
	require.Equal(t, sniffarrv1.Kind_KIND_MOVIE, got["Heat (1995) [Rus]"].GetKind(), "the same title's kind")
	require.Equal(t, sniffarrv1.Layer_LAYER_REUSE, got["Heat (1995) [Rus]"].GetLayer())
	require.Equal(t, sniffarrv1.Kind_KIND_UNSPECIFIED, got["Unnamed Collection"].GetKind())
	require.Equal(t, sniffarrv1.Layer_LAYER_UNSPECIFIED, got["Unnamed Collection"].GetLayer())
}
