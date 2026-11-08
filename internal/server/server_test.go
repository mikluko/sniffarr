package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/libarr/version"
	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"sarak.sh/mikluko/sniffarr/internal/arr"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/bus/bustest"
	"sarak.sh/mikluko/sniffarr/internal/memconn"
	"sarak.sh/mikluko/sniffarr/internal/rot13"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/search"
	"sarak.sh/mikluko/sniffarr/internal/sketch"
	"sarak.sh/mikluko/sniffarr/internal/sniff"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

func start(t *testing.T, cfg Config, qopts ...store.TaskOptions) (*httptest.Server, *http.Client) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{Languages: audio.Tags(cfg.Languages)})
	require.NoError(t, err)
	var qo store.TaskOptions
	if len(qopts) > 0 {
		qo = qopts[0]
	}
	qo.Meter = cfg.Meter
	q, err := store.NewTasks(ctx, db, qo)
	require.NoError(t, err)
	cfg.Tasks = q
	cfg.Store, cfg.Log = s, slog.New(slog.DiscardHandler)
	if cfg.Arr != nil {
		cfg.Arr.Store, cfg.Arr.Tasks, cfg.Arr.Log = s, q, cfg.Log
	}
	if cfg.Counts != nil {
		cfg.Counts.Store = s
	}
	js := sightings(t)
	cfg.JetStream = js
	cfg.Ingest = cfg.ingestService()
	cfg.Ingest.IntakeWait = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cfg.Ingest.Intake(ctx, js) }()
	t.Cleanup(func() {
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	})
	h, err := New(cfg)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(drainedSight(t, h, js))
	srv.Config.Protocols = Protocols()
	srv.Start()
	t.Cleanup(srv.Close)
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return srv, &http.Client{Transport: &http.Transport{Protocols: &p}}
}

// dedupe is the sightings stream's duplicates window in these tests.
const dedupe = 100 * time.Millisecond

// sightings returns a connection to a server with the bus topology whose
// sightings stream has the duplicates window dedupe.
func sightings(t *testing.T) jetstream.JetStream {
	t.Helper()
	js, err := jetstream.New(bustest.Conn(t))
	require.NoError(t, err)
	stream, err := js.Stream(t.Context(), bus.StreamSightings)
	require.NoError(t, err)
	cfg := stream.CachedInfo().Config
	cfg.Duplicates = dedupe
	_, err = js.UpdateStream(t.Context(), cfg)
	require.NoError(t, err)
	return js
}

// pastDedupe waits until the sightings published so far are out of the
// duplicates window.
func pastDedupe() { time.Sleep(3 * dedupe) }

// drainedSight has h answer a Sight only once the intake has taken every
// sighting on js, so a test sees the queue as Sight left it.
func drainedSight(t *testing.T, h http.Handler, js jetstream.JetStream) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sniffarrv1connect.IngestServiceSightProcedure {
			w = &drainingWriter{ResponseWriter: w, drain: func() { drained(t, js) }}
		}
		h.ServeHTTP(w, r)
	})
}

// drainingWriter calls drain before every write.
type drainingWriter struct {
	http.ResponseWriter
	drain func()
}

func (w *drainingWriter) WriteHeader(code int) {
	w.drain()
	w.ResponseWriter.WriteHeader(code)
}

func (w *drainingWriter) Write(b []byte) (int, error) {
	w.drain()
	return w.ResponseWriter.Write(b)
}

func (w *drainingWriter) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

func (w *drainingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// drained waits until neither sightings consumer has a message pending or
// unacknowledged.
func drained(t *testing.T, js jetstream.JetStream) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		idle := true
		for _, name := range []string{bus.ConsumerAnnounce, bus.ConsumerSample} {
			c, err := js.Consumer(ctx, bus.StreamSightings, name)
			if err != nil {
				t.Errorf("consumer %s: %v", name, err)
				return
			}
			info := c.CachedInfo()
			idle = idle && info.NumPending == 0 && info.NumAckPending == 0
		}
		if idle {
			return
		}
		select {
		case <-ctx.Done():
			t.Error("the intake did not take the sightings")
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func hash(b byte) []byte {
	h := make([]byte, 20)
	h[0] = b
	return h
}

func ingestClient(srv *httptest.Server, hc *http.Client, opts ...connect.ClientOption) sniffarrv1connect.IngestServiceClient {
	return sniffarrv1connect.NewIngestServiceClient(hc, srv.URL, opts...)
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
	search := sniffarrv1connect.NewSearchServiceClient(hc, srv.URL, opts...)

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
			"The.Thing.1982.BDRip.Rus":      {{Language: "ru", Marker: true, Mark: "Rus", Probability: new(float32(1))}},
			"The.Thing.1982.BDRip":          {{Language: "ru"}},
			"The.Thing.1982.BDRip.LostFilm": {{Language: "ru", Marker: true, Mark: "LostFilm", Studios: []string{"LostFilm"}, Probability: new(float32(1))}},
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

			search := sniffarrv1connect.NewSearchServiceClient(hc, srv.URL)
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

func TestTorznab(t *testing.T) {
	srv, hc := start(t, Config{})
	res, err := hc.Get(srv.URL + "/torznab/api?t=caps&apikey=")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusNotFound, res.StatusCode, "without a token there is no endpoint")

	srv, hc = start(t, Config{TorznabToken: "tz"})
	res, err = hc.Get(srv.URL + "/torznab/api?t=caps&apikey=tz")
	require.NoError(t, err)
	b, err := io.ReadAll(res.Body)
	res.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, string(b), `<server title="sniffarr">`, "no bearer token needed, only the apikey")
}

func TestWebhook(t *testing.T) {
	ctx := context.Background()
	grab := func(srv *httptest.Server, hc *http.Client) *http.Response {
		t.Helper()
		body := `{"eventType":"Grab","movie":{"id":1,"title":"Alien","year":1979,"tmdbId":348,"imdbId":"tt0078748"},
			"release":{"releaseTitle":"Alien.1979.1080p.BluRay.x264","indexer":"tracker","size":8589934592},
			"downloadClient":"qBittorrent","downloadClientType":"qBittorrent","downloadId":"0700000000000000000000000000000000000000"}`
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/webhooks/arr", strings.NewReader(body))
		require.NoError(t, err)
		req.SetBasicAuth("radarr", "wh")
		res, err := hc.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	srv, hc := start(t, Config{Arr: &arr.Recorder{}})
	require.Equal(t, http.StatusNotFound, grab(srv, hc).StatusCode, "without a token there is no endpoint")

	rec := &arr.Recorder{}
	srv, hc = start(t, Config{WebhookToken: "wh", Arr: rec})
	res := grab(srv, hc)
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, arr.Queued+"\n", string(b))

	f := openFetch(t, ingestClient(srv, hc))
	f.credit(1)
	f.expect(7)
	f.torrent(7, "Alien.1979.1080p.BluRay.x264", "Alien.1979.1080p.BluRay.x264.mkv")
	f.finish()
	c, err := rec.Store.Card(ctx, store.Hash(hash(7)), 1, time.Now())
	require.NoError(t, err)
	require.Equal(t, store.Arr, c.Discovery)
	require.NotZero(t, c.Work, "the fetched torrent takes the work of the grab")
	require.Equal(t, 348, c.Work.Work.TMDBID)
	require.Equal(t, int64(1), c.Work.GrabsWeek)
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
	srv, hc := start(t, Config{Attempts: 2, Backoff: 100 * time.Millisecond, Poll: 20 * time.Millisecond}, store.TaskOptions{DeadFor: 300 * time.Millisecond})
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
	pastDedupe()
	res, err = sight(t, ingest, sniffarrv1.Source_SOURCE_ANNOUNCE, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), res.GetNew(), "an announce revives it")
	f.expect(1)
}

func TestNoCredentials(t *testing.T) {
	srv, hc := start(t, Config{})
	ctx := context.Background()
	_, err := sight(t, ingestClient(srv, hc), sniffarrv1.Source_SOURCE_SAMPLE, 1)
	require.NoError(t, err)
	_, err = sniffarrv1connect.NewSearchServiceClient(hc, srv.URL).Search(ctx, connect.NewRequest(&sniffarrv1.SearchRequest{}))
	require.NoError(t, err)
	_, err = sniffarrv1connect.NewBrowseServiceClient(hc, srv.URL).Torrents(ctx, connect.NewRequest(&sniffarrv1.TorrentsRequest{}))
	require.NoError(t, err)

	res, err := hc.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
}

func TestReadyAfterCounts(t *testing.T) {
	counts := &search.Counts{}
	srv, hc := start(t, Config{Counts: counts})
	res, err := hc.Get(srv.URL + "/readyz")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, res.StatusCode, "no counts snapshot yet")
	res, err = hc.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode, "alive all the same")
	_, err = counts.Refresh(context.Background())
	require.NoError(t, err)
	res, err = hc.Get(srv.URL + "/readyz")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
}

func TestServerHeader(t *testing.T) {
	srv, hc := start(t, Config{Counts: &search.Counts{}})
	want := version.Get().Server("sniffarr")
	for path, code := range map[string]int{
		"/healthz":                          http.StatusOK,
		"/readyz":                           http.StatusServiceUnavailable,
		"/nowhere":                          http.StatusNotFound,
		"/sniffarr.v1.SearchService/Search": http.StatusMethodNotAllowed,
	} {
		res, err := hc.Get(srv.URL + path)
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		require.Equal(t, code, res.StatusCode, path)
		require.Equal(t, want, res.Header.Get("Server"), path)
	}
}

func TestKindLayers(t *testing.T) {
	srv, hc := start(t, Config{Poll: 20 * time.Millisecond})
	ingest := ingestClient(srv, hc)
	search := sniffarrv1connect.NewSearchServiceClient(hc, srv.URL)
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

// value returns the int64 data point of the named metric whose attributes
// include kv, summed over the matching points.
func value(t *testing.T, rm metricdata.ResourceMetrics, name string, kv ...attribute.KeyValue) int64 {
	t.Helper()
	var sum int64
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			var points []metricdata.DataPoint[int64]
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				points = d.DataPoints
			case metricdata.Gauge[int64]:
				points = d.DataPoints
			}
			for _, p := range points {
				match := true
				for _, want := range kv {
					if got, ok := p.Attributes.Value(want.Key); !ok || got != want.Value {
						match = false
					}
				}
				if match {
					sum += p.Value
					found = true
				}
			}
		}
	}
	require.True(t, found, "no %s %v", name, kv)
	return sum
}

func TestMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	srv, hc := start(t, Config{Meter: mp, Poll: 20 * time.Millisecond})
	ingest := ingestClient(srv, hc)

	_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1, 2, 3, 4, 1)
	require.NoError(t, err)
	f := openFetch(t, ingest)
	f.credit(4)
	f.expect(1, 2, 3, 4)
	f.torrent(1, "The.Thing.1982.1080p.BluRay.x264", "The.Thing.1982.mkv")
	f.torrent(2, rot13.String("Ovt.Cnpxntr.KKK.1080c"), "video.mp4")
	f.failure(3, sniffarrv1.Failure_FAILURE_NO_PEERS)
	f.torrent(4, "進撃の巨人 S04", "video.mkv")
	f.finish()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	result := func(v string) attribute.KeyValue { return attribute.String("sniffarr.sighting.result", v) }
	outcome := func(v string) attribute.KeyValue { return attribute.String("sniffarr.fetch.outcome", v) }
	require.EqualValues(t, 4, value(t, rm, "sniffarr.ingest.sightings", result("new")))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.ingest.sightings", result("known")))
	require.EqualValues(t, 3, value(t, rm, "sniffarr.ingest.results", outcome("torrent")))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.ingest.results", outcome("no_peers")))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.ingest.stored"))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.ingest.rejections", attribute.String("sniffarr.rules.reason", "adult")))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.ingest.rejections", attribute.String("sniffarr.rules.reason", "script")))
	require.EqualValues(t, 0, value(t, rm, "sniffarr.queue.depth", attribute.String("sniffarr.discovery", "sample")))
	require.EqualValues(t, 0, value(t, rm, "sniffarr.queue.leases"))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.queue.dead"), "the one without peers is dead")
	require.EqualValues(t, 1, value(t, rm, "sniffarr.queue.evictions", attribute.String("sniffarr.queue.eviction", "no_peers")))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.classify.classifications",
		attribute.String("sniffarr.classify.layer", "rules"), attribute.String("sniffarr.kind", "movie")))
}

func TestReuseMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	srv, hc := start(t, Config{Meter: mp, Poll: 20 * time.Millisecond})
	ingest := ingestClient(srv, hc)

	_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1, 2, 3)
	require.NoError(t, err)
	f := openFetch(t, ingest)
	f.credit(3)
	f.expect(1, 2, 3)
	f.torrent(1, "Heat (1995) [Rus]", "Heat.avi")
	f.torrent(2, "Heat (1995) [Eng]", "Heat.mkv")
	f.torrent(3, "Heat.1995.1080p.BluRay.x264", "Heat.1995.mkv")
	f.finish()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	layer := func(v string) attribute.KeyValue { return attribute.String("sniffarr.classify.layer", v) }
	movie := attribute.String("sniffarr.kind", "movie")
	require.EqualValues(t, 1, value(t, rm, "sniffarr.classify.classifications", layer("rules"), movie))
	require.EqualValues(t, 2, value(t, rm, "sniffarr.classify.classifications", layer("reuse"), movie),
		"the kind 3 lends to 1 and 2")
}

func TestScripts(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	scripts, err := rules.ParseScripts("Latin,Greek")
	require.NoError(t, err)
	srv, hc := start(t, Config{Meter: mp, Poll: 20 * time.Millisecond, Scripts: scripts})
	ingest := ingestClient(srv, hc)

	_, err = sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1, 2)
	require.NoError(t, err)
	f := openFetch(t, ingest)
	f.credit(2)
	f.expect(1, 2)
	f.torrent(1, "Ελληνικά.2020.1080p", "video.mkv")
	f.torrent(2, "Мандалорец.S03.WEB-DL.Rus", "video.mkv")
	f.finish()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.ingest.stored"))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.ingest.rejections", attribute.String("sniffarr.rules.reason", "script")),
		"Cyrillic is not among the configured scripts")
}

// serveMem serves the handler over memconn with Alien stored, and returns
// the store and an ingest client of the in-process API.
func serveMem(t *testing.T) (*store.Store, sniffarrv1connect.IngestServiceClient) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	_, _, err = s.Insert(ctx, store.Torrent{Hash: store.Hash{1}, Name: "Alien.1979.1080p", Size: 8 << 30, Discovery: "sample", SeenAt: time.Now(),
		Files: []rules.File{{Path: "Alien.mkv", Size: 8 << 30}}})
	require.NoError(t, err)
	h, err := New(Config{Store: s, Tasks: q, Log: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	mem := memconn.Listen()
	srv := &http.Server{Handler: h, Protocols: Protocols()}
	go func() { _ = srv.Serve(mem) }()
	t.Cleanup(func() { _ = srv.Close() })
	return s, sniffarrv1connect.NewIngestServiceClient(mem.Client(), memconn.URL)
}

func TestPeersEndToEnd(t *testing.T) {
	ctx := context.Background()
	s, client := serveMem(t)
	now := time.Now()
	p := sniff.NewPeers(sniff.PeersConfig{Ingest: client, Flush: time.Hour, Backoff: time.Millisecond, Now: func() time.Time { return now }})
	pctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(pctx) }()
	key, err := s.HashKey(ctx)
	require.NoError(t, err)
	res, err := client.HashKey(ctx, connect.NewRequest(&sniffarrv1.HashKeyRequest{}))
	require.NoError(t, err)
	require.Equal(t, key[:], res.Msg.GetKey(), "the client gets the store's key")
	id, err := s.NodeID(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, id[:], res.Msg.GetNodeId(), "and the store's node ID")

	peers := make([]netip.AddrPort, 200)
	for i := range peers {
		peers[i] = netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), 6881)
	}
	select {
	case <-p.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("the key never came")
	}
	p.Add(sniff.Hash{1}, peers[:100])
	p.Add(sniff.Hash{1}, peers[100:])
	p.Add(sniff.Hash{2}, peers[:10])
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	r, err := s.Get(ctx, store.Hash{1})
	require.NoError(t, err)
	c, err := s.Peers(ctx, r.ID, now)
	require.NoError(t, err)
	require.InDelta(t, 200, c.Day, 20, "the flushed sketch is stored under the torrent and the hour")
	require.Equal(t, c.Day, c.Week)

	stream := client.Peers(ctx)
	sk := sketch.New()
	for _, a := range peers[:50] {
		sk.Add(key.Hash(a))
	}
	require.NoError(t, stream.Send(&sniffarrv1.PeersRequest{Infohash: hash(1), Hour: now.Add(-time.Hour).Unix(), Sketch: sk.Bytes()}))
	require.NoError(t, stream.Send(&sniffarrv1.PeersRequest{Infohash: hash(9), Hour: now.Unix(), Sketch: sk.Bytes()}))
	pres, err := stream.CloseAndReceive()
	require.NoError(t, err)
	require.Equal(t, int64(1), pres.Msg.GetMerged())
	require.Equal(t, int64(1), pres.Msg.GetDropped(), "a sketch of an unknown infohash is dropped")
	c, err = s.Peers(ctx, r.ID, now)
	require.NoError(t, err)
	require.InDelta(t, 200, c.Day, 20, "the earlier hour's peers are among the day's already")

	stream = client.Peers(ctx)
	require.NoError(t, stream.Send(&sniffarrv1.PeersRequest{Infohash: hash(1), Hour: now.Unix(), Sketch: []byte{1, 2, 3}}))
	_, err = stream.CloseAndReceive()
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "garbage is not a sketch")
	stream = client.Peers(ctx)
	require.NoError(t, stream.Send(&sniffarrv1.PeersRequest{Infohash: []byte{1}, Hour: now.Unix(), Sketch: sk.Bytes()}))
	_, err = stream.CloseAndReceive()
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "an infohash is 20 bytes")
}

func TestSightStored(t *testing.T) {
	srv, hc := start(t, Config{Poll: 20 * time.Millisecond})
	ingest := ingestClient(srv, hc)
	res, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1, 2, 3)
	require.NoError(t, err)
	require.Equal(t, []bool{false, false, false}, res.GetStored(), "nothing is stored yet")

	f := openFetch(t, ingest)
	f.credit(3)
	f.expect(1, 2, 3)
	f.torrent(1, "The.Thing.1982.1080p.BluRay.x264", "The.Thing.1982.mkv")
	f.torrent(2, rot13.String("Ovt.Cnpxntr.KKK.1080c"), "video.mp4")
	f.finish()

	pastDedupe()
	res, err = sight(t, ingest, sniffarrv1.Source_SOURCE_ANNOUNCE, 1, 2, 3, 9, 1)
	require.NoError(t, err)
	require.Equal(t, []bool{true, false, false, false, true}, res.GetStored(), "the stored torrent is flagged, the rejected, queued and new ones are not")
	require.Equal(t, int64(2), res.GetNew(), "the queued 3 and the new 9")
	require.Equal(t, int64(3), res.GetKnown(), "the stored 1, twice, and the rejected 2")
}

// blockingClassifier signals each classification on started and holds it
// until release closes, then signals it on done.
type blockingClassifier struct {
	started, done chan int64
	release       chan struct{}
}

func (c *blockingClassifier) Classify(ctx context.Context, p store.Pending, _ string) error {
	c.started <- p.ID
	select {
	case <-c.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	c.done <- p.ID
	return nil
}

func TestTorznabProbes(t *testing.T) {
	for _, probes := range []bool{false, true} {
		srv, hc := start(t, Config{Poll: 20 * time.Millisecond, TorznabToken: "tz", Probes: probes})
		ingest := ingestClient(srv, hc)
		_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1)
		require.NoError(t, err)
		f := openFetch(t, ingest)
		f.credit(1)
		f.expect(1)
		f.torrent(1, "The.Thing.1982.1080p.BluRay.x264", "The.Thing.1982.mkv")
		f.finish()

		res, err := hc.Get(srv.URL + "/torznab/api?t=search&q=the+thing&apikey=tz")
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		require.Equal(t, http.StatusOK, res.StatusCode)

		browse := sniffarrv1connect.NewBrowseServiceClient(hc, srv.URL)
		st, err := browse.Stats(context.Background(), connect.NewRequest(&sniffarrv1.StatsRequest{}))
		require.NoError(t, err)
		var queued int64
		for _, l := range st.Msg.GetQueueProbes().GetLevels() {
			queued += l.GetTasks()
		}
		settings, err := browse.Settings(context.Background(), connect.NewRequest(&sniffarrv1.SettingsRequest{}))
		require.NoError(t, err)
		require.Equal(t, probes, settings.Msg.GetProbes())
		if probes {
			require.EqualValues(t, 1, queued, "the search's release is queued for a probe")
		} else {
			require.Zero(t, queued, "probes are off: the search queues none")
		}
	}
}

func TestTorznabDoesNotWait(t *testing.T) {
	cl := &blockingClassifier{started: make(chan int64, 1), done: make(chan int64, 1), release: make(chan struct{})}
	lazy := &search.Lazy{Classifier: cl, Wait: time.Minute, Log: slog.New(slog.DiscardHandler)}
	srv, hc := start(t, Config{Poll: 20 * time.Millisecond, Languages: russian, TorznabToken: "tz", Lazy: lazy})

	ingest := ingestClient(srv, hc)
	_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1)
	require.NoError(t, err)
	f := openFetch(t, ingest)
	f.credit(1)
	f.expect(1)
	f.torrent(1, "The.Thing.1982.1080p.BluRay.x264", "The.Thing.1982.mkv")
	f.finish()

	begin := time.Now()
	res, err := hc.Get(srv.URL + "/torznab/api?t=search&q=the+thing&apikey=tz")
	require.NoError(t, err)
	b, err := io.ReadAll(res.Body)
	res.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Less(t, time.Since(begin), 10*time.Second, "answered before the classification, held, or Lazy.Wait")
	require.Contains(t, string(b), "The.Thing.1982.1080p.BluRay.x264", "with what the store holds")

	select {
	case <-cl.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the classification did not start")
	}
	close(cl.release)
	select {
	case <-cl.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the classification did not run past the answer")
	}
}

const (
	callerTrace       = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpan        = "00f067aa0ba902b7"
	callerTraceparent = "00-" + callerTrace + "-" + callerSpan + "-01"
)

// recordSpans installs a recorder as the global TracerProvider and
// TraceContext as the global propagator for the rest of the test.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	tp, prop := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	rec := tracetest.NewSpanRecorder()
	sdk := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(sdk)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(prop)
		require.NoError(t, sdk.Shutdown(context.Background()))
	})
	return rec
}

// requireServerSpan requires the ended span named name to be a server span
// continuing the caller's trace, with a span under it. The span may end
// after the client has read the response, so it waits for the span.
func requireServerSpan(t *testing.T, rec *tracetest.SpanRecorder, name string) {
	t.Helper()
	var span sdktrace.ReadOnlySpan
	require.Eventually(t, func() bool {
		for _, s := range rec.Ended() {
			if s.Name() == name {
				span = s
			}
		}
		return span != nil
	}, 5*time.Second, 10*time.Millisecond, "no span %q", name)
	require.Equal(t, trace.SpanKindServer, span.SpanKind())
	require.Equal(t, callerTrace, span.Parent().TraceID().String())
	require.Equal(t, callerSpan, span.Parent().SpanID().String())
	require.True(t, span.Parent().IsRemote())
	var children int
	for _, s := range rec.Ended() {
		if s.Parent().SpanID() == span.SpanContext().SpanID() {
			children++
		}
	}
	require.NotZero(t, children, "the handler's store calls nest under %q", name)
}

func TestTorznabTrace(t *testing.T) {
	rec := recordSpans(t)
	srv, hc := start(t, Config{TorznabToken: "tz"})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/torznab/api?t=search&q=alien&apikey=tz", nil)
	require.NoError(t, err)
	req.Header.Set("traceparent", callerTraceparent)
	res, err := hc.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	requireServerSpan(t, rec, "GET /torznab/api")
}

func TestWebhookTrace(t *testing.T) {
	rec := recordSpans(t)
	srv, hc := start(t, Config{WebhookToken: "wh", Arr: &arr.Recorder{}})
	body := `{"eventType":"Grab","movie":{"id":1,"title":"Alien","year":1979,"tmdbId":348},
		"release":{"releaseTitle":"Alien.1979.1080p.BluRay.x264","indexer":"tracker","size":8589934592},
		"downloadClient":"qBittorrent","downloadClientType":"qBittorrent","downloadId":"0700000000000000000000000000000000000000"}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/webhooks/arr", strings.NewReader(body))
	require.NoError(t, err)
	req.SetBasicAuth("radarr", "wh")
	req.Header.Set("traceparent", callerTraceparent)
	res, err := hc.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	requireServerSpan(t, rec, "POST /webhooks/arr")
}
