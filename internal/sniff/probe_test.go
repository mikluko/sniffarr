package sniff

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"sarak.sh/mikluko/sniffarr/internal/container"
)

func TestMainFile(t *testing.T) {
	i, ok := mainFile([]File{
		{Path: "Show/Sample/sample.mkv", Size: 50 << 20},
		{Path: "Show/Show.S01E01.MKV", Size: 2 << 30},
		{Path: "Show/Show.S01E02.mkv", Size: 3 << 30},
		{Path: "Show/Extras/making-of.avi", Size: 9 << 30},
		{Path: "Show/Rus Sound/Show.S01E02.mka", Size: 1 << 30},
	})
	require.True(t, ok)
	require.Equal(t, 2, i, "the largest Matroska or MP4 file, any case")
	_, ok = mainFile([]File{{Path: "Movie/BDMV/STREAM/00001.m2ts", Size: 30 << 30}, {Path: "Movie.avi", Size: 1 << 30}})
	require.False(t, ok)
}

// fakePieces is a torrent of data cut into pieces of pl bytes, of which
// those in have are complete.
type fakePieces struct {
	mu        sync.Mutex
	data      []byte
	pl        int64
	have      map[int]bool
	requested []int
}

func (f *fakePieces) pieceLength() int64 { return f.pl }
func (f *fakePieces) numPieces() int     { return int((int64(len(f.data)) + f.pl - 1) / f.pl) }
func (f *fakePieces) pieceSize(i int) int64 {
	return min(f.pl, int64(len(f.data))-int64(i)*f.pl)
}
func (f *fakePieces) complete(i int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.have[i]
}
func (f *fakePieces) request(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requested = append(f.requested, i)
}
func (f *fakePieces) wait(_ context.Context, i int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.have[i] = true
	return nil
}
func (f *fakePieces) readAt(_ context.Context, p []byte, off int64) (int, error) {
	for i := int(off / f.pl); int64(i)*f.pl < off+int64(len(p)); i++ {
		if !f.complete(i) {
			return 0, errors.New("read of a piece not complete")
		}
	}
	return copy(p, f.data[off:]), nil
}

func TestPieceReader(t *testing.T) {
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}
	src := &fakePieces{data: data, pl: 16, have: map[int]bool{}}
	r := &pieceReader{ctx: context.Background(), src: src, base: 20, size: 70, max: 48}

	b := make([]byte, 8)
	n, err := r.ReadAt(b, 0)
	require.NoError(t, err)
	require.Equal(t, 8, n)
	require.Equal(t, data[20:28], b, "offsets are the file's, from base")
	require.Equal(t, []int{1}, src.requested)
	require.EqualValues(t, 16, r.downloaded)

	n, err = r.ReadAt(b, 8)
	require.NoError(t, err)
	require.Equal(t, data[28:36], b[:n])
	require.EqualValues(t, 32, r.downloaded, "a read across two pieces downloads the one missing")

	n, err = r.ReadAt(make([]byte, 10), 66)
	require.ErrorIs(t, err, io.EOF, "reads stop at the end of the file")
	require.Equal(t, 4, n)
	require.EqualValues(t, 48, r.downloaded)

	_, err = r.ReadAt(b, 50)
	require.ErrorIs(t, err, errTooFar, "another piece would pass max")
	require.EqualValues(t, 48, r.downloaded)

	_, err = r.ReadAt(b, 70)
	require.ErrorIs(t, err, io.EOF)
}

type fakeProbeSource struct {
	probed Probed
	err    error
	got    chan int64
}

func (f *fakeProbeSource) Probe(_ context.Context, _ Hash, maxBytes int64) (Probed, error) {
	f.got <- maxBytes
	return f.probed, f.err
}

// probeIngest hands out tasks, one per ProbeTask until they run out, and
// collects the results.
type probeIngest struct {
	sniffarrv1connect.UnimplementedIngestServiceHandler
	mu      sync.Mutex
	tasks   []*sniffarrv1.ProbeTask
	results chan *sniffarrv1.ProbeResultRequest
	probers []string
}

func (p *probeIngest) ProbeTask(_ context.Context, req *connect.Request[sniffarrv1.ProbeTaskRequest]) (*connect.Response[sniffarrv1.ProbeTaskResponse], error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probers = append(p.probers, req.Msg.GetProber())
	res := &sniffarrv1.ProbeTaskResponse{}
	if len(p.tasks) > 0 {
		res.Task, p.tasks = p.tasks[0], p.tasks[1:]
	}
	return connect.NewResponse(res), nil
}

func (p *probeIngest) ProbeResult(_ context.Context, req *connect.Request[sniffarrv1.ProbeResultRequest]) (*connect.Response[sniffarrv1.ProbeResultResponse], error) {
	p.results <- req.Msg
	return connect.NewResponse(&sniffarrv1.ProbeResultResponse{}), nil
}

func (p *probeIngest) client(t *testing.T) sniffarrv1connect.IngestServiceClient {
	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(p))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return sniffarrv1connect.NewIngestServiceClient(srv.Client(), srv.URL)
}

func TestProberReportsTracks(t *testing.T) {
	ingest := &probeIngest{tasks: []*sniffarrv1.ProbeTask{{Infohash: []byte{1, 19: 0}, MaxBytes: 64 << 20}}, results: make(chan *sniffarrv1.ProbeResultRequest, 1)}
	src := &fakeProbeSource{got: make(chan int64, 1), probed: Probed{Path: "Coraline.2009.mkv", Format: container.Matroska, Downloaded: 16 << 20,
		Tracks: []container.Track{{Kind: container.Audio, Language: "rus", Name: "Дубляж\xff", Codec: "A_AC3", Channels: 6, Default: true}}}}
	p := NewProber(ProberConfig{Ingest: ingest.client(t), Workers: 1, Poll: time.Hour}, src)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()
	require.EqualValues(t, 64<<20, <-src.got, "the task's cap goes to the source")
	r := <-ingest.results
	require.Equal(t, []byte{1, 19: 0}, r.GetInfohash())
	require.EqualValues(t, 16<<20, r.GetDownloaded())
	require.Equal(t, "matroska", r.GetFile().GetFormat())
	require.Len(t, r.GetFile().GetTracks(), 1)
	tr := r.GetFile().GetTracks()[0]
	require.Equal(t, sniffarrv1.TrackKind_TRACK_KIND_AUDIO, tr.GetKind())
	require.Equal(t, "Дубляж�", tr.GetName(), "invalid UTF-8 is replaced")
	require.EqualValues(t, 6, tr.GetChannels())
	require.True(t, tr.GetDefault())
}

func TestProberID(t *testing.T) {
	ingest := &probeIngest{}
	client := ingest.client(t)
	a := NewProber(ProberConfig{Ingest: client}, &fakeProbeSource{})
	b := NewProber(ProberConfig{Ingest: client}, &fakeProbeSource{})
	ctx := context.Background()
	require.False(t, a.once(ctx))
	require.False(t, a.once(ctx))
	require.False(t, b.once(ctx))
	require.Len(t, ingest.probers, 3)
	require.NotEmpty(t, ingest.probers[0])
	require.Equal(t, ingest.probers[0], ingest.probers[1], "a prober sends its ID with every request")
	require.NotEqual(t, ingest.probers[0], ingest.probers[2], "two probers send two")
}

func TestProbeResultFailures(t *testing.T) {
	for _, c := range []struct {
		err  error
		want sniffarrv1.ProbeFailure
	}{
		{ErrNoPeers, sniffarrv1.ProbeFailure_PROBE_FAILURE_NO_PEERS},
		{ErrNoMetadata, sniffarrv1.ProbeFailure_PROBE_FAILURE_TIMEOUT},
		{context.DeadlineExceeded, sniffarrv1.ProbeFailure_PROBE_FAILURE_TIMEOUT},
		{errors.Join(ErrTracksUnknown, errTooFar), sniffarrv1.ProbeFailure_PROBE_FAILURE_UNKNOWN},
	} {
		r, _ := probeResult(Hash{1}, Probed{Downloaded: 5}, c.err)
		require.Equal(t, c.want, r.GetFailure(), "%v", c.err)
		require.EqualValues(t, 5, r.GetDownloaded(), "what a failed probe downloaded counts too")
	}
}
