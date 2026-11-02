package sniff

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
)

const (
	// DefaultSeedMaxBytes is the default SeederConfig.MaxBytes, 200 GiB.
	DefaultSeedMaxBytes = 200 << 30
	// DefaultSeedMaxAge is the default SeederConfig.MaxAge.
	DefaultSeedMaxAge = 7 * 24 * time.Hour
)

const (
	// seedCheck is how often a Seeder looks for torrents to remove.
	seedCheck = time.Minute
	// seedAnnounce is how often a Seeder announces each torrent on the DHT.
	seedAnnounce = 30 * time.Minute
)

// Why a Seeder removes a torrent.
const (
	seedRatio = "ratio"
	seedAge   = "age"
	seedSize  = "size"
)

// seedTorrent is a torrent a Seeder keeps in its swarm.
type seedTorrent interface {
	// uploaded is the piece data uploaded since the torrent was opened.
	uploaded() int64
	// announce announces the client on the DHT as a peer of the torrent and
	// connects to the peers the DHT names.
	announce()
	// remove leaves the swarm and deletes the torrent's data.
	remove() error
}

// seedOpener opens a torrent of the info dictionary for seeding again.
type seedOpener func(h Hash, info []byte) (seedTorrent, error)

// seedState is a seeded torrent as the state file keeps it.
type seedState struct {
	Infohash   string    `json:"infohash"`
	Added      time.Time `json:"added"`
	Downloaded int64     `json:"downloaded"`
	Uploaded   int64     `json:"uploaded"`
}

type seedEntry struct {
	hash  Hash
	added time.Time
	// downloaded is the piece data downloaded, uploadedBefore what was
	// uploaded before the torrent was opened this time.
	downloaded, uploadedBefore int64
	t                          seedTorrent
}

func (e *seedEntry) uploaded() int64 { return e.uploadedBefore + e.t.uploaded() }

// SeederConfig tunes a Seeder. Zero fields take the default named in their
// doc.
type SeederConfig struct {
	// Dir holds the state file and the info of each torrent; required.
	Dir string
	// MaxBytes caps the piece data kept, the oldest torrents removed first;
	// DefaultSeedMaxBytes.
	MaxBytes int64
	// MaxAge caps how long a torrent is seeded; DefaultSeedMaxAge.
	MaxAge time.Duration
	// Ingest, when set, is told by ProbeUpload what the torrents uploaded,
	// once a minute.
	Ingest sniffarrv1connect.IngestServiceClient
	// Log discards everything when nil.
	Log *slog.Logger
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider
	Now   func() time.Time
}

// Seeder keeps torrents a probe downloaded pieces of in their swarms until
// each has uploaded as much as it downloaded, removing it then, or earlier
// once it is MaxAge old or the oldest beyond MaxBytes. It is safe for
// concurrent use.
type Seeder struct {
	cfg SeederConfig

	mu      sync.Mutex
	entries []*seedEntry
	// reported is the upload each entry's counter reached, by hash.
	reported map[Hash]int64
	// unsent is the upload Ingest has not been told of; Run alone uses it.
	unsent int64

	downloaded, uploaded, removals metric.Int64Counter
}

// NewSeeder returns a Seeder for cfg.
func NewSeeder(cfg SeederConfig) *Seeder {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultSeedMaxBytes
	}
	orDefault(&cfg.MaxAge, DefaultSeedMaxAge)
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	m := meter(cfg.Meter)
	s := &Seeder{cfg: cfg, reported: map[Hash]int64{},
		downloaded: counter(m, "sniffarr.seed.downloaded", "By", "Piece data probes downloaded."),
		uploaded:   counter(m, "sniffarr.seed.uploaded", "By", "Piece data uploaded to the swarms of probed torrents."),
		removals:   counter(m, "sniffarr.seed.removals", "{torrent}", "Seeded torrents removed, by why."),
	}
	_, err := m.Int64ObservableGauge("sniffarr.seed.torrents", metric.WithUnit("{torrent}"),
		metric.WithDescription("Probed torrents seeded."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			n, _ := s.Size()
			o.Observe(int64(n))
			return nil
		}))
	if err != nil {
		otel.Handle(err)
	}
	_, err = m.Int64ObservableGauge("sniffarr.seed.stored", metric.WithUnit("By"),
		metric.WithDescription("Piece data of the seeded torrents kept on disk."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			_, b := s.Size()
			o.Observe(b)
			return nil
		}))
	if err != nil {
		otel.Handle(err)
	}
	return s
}

// Size returns how many torrents are seeded and the piece data they keep.
func (s *Seeder) Size() (torrents int, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		bytes += e.downloaded
	}
	return len(s.entries), bytes
}

func (s *Seeder) statePath() string { return filepath.Join(s.cfg.Dir, "seeds.json") }

func (s *Seeder) infoPath(h Hash) string {
	return filepath.Join(s.cfg.Dir, "info", hex.EncodeToString(h[:]))
}

// Restore reopens with open the torrents the state file lists, keeping what
// they uploaded before. A torrent that fails to open is forgotten; a
// missing state file restores none.
func (s *Seeder) Restore(open seedOpener) error {
	b, err := os.ReadFile(s.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var states []seedState
	if err := json.Unmarshal(b, &states); err != nil {
		return fmt.Errorf("%s: %w", s.statePath(), err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range states {
		raw, err := hex.DecodeString(st.Infohash)
		if err != nil || len(raw) != len(Hash{}) {
			continue
		}
		h := Hash(raw)
		info, err := os.ReadFile(s.infoPath(h))
		var t seedTorrent
		if err == nil {
			t, err = open(h, info)
		}
		if err != nil {
			s.cfg.Log.Warn("seed not restored", "infohash", st.Infohash, "err", err)
			_ = os.Remove(s.infoPath(h))
			continue
		}
		s.entries = append(s.entries, &seedEntry{hash: h, added: st.Added, downloaded: st.Downloaded, uploadedBefore: st.Uploaded, t: t})
		s.reported[h] = st.Uploaded
	}
	slices.SortStableFunc(s.entries, func(a, b *seedEntry) int { return a.added.Compare(b.added) })
	return s.saveLocked()
}

// Add seeds t, of which downloaded bytes of piece data were downloaded,
// keeping its info dictionary for Restore. A torrent seeded already gets
// the bytes added.
func (s *Seeder) Add(ctx context.Context, h Hash, t seedTorrent, downloaded int64, info []byte) error {
	s.downloaded.Add(ctx, downloaded)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.hash == h {
			e.downloaded += downloaded
			return s.saveLocked()
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.infoPath(h)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.infoPath(h), info, 0o644); err != nil {
		return err
	}
	s.entries = append(s.entries, &seedEntry{hash: h, added: s.cfg.Now(), downloaded: downloaded, t: t})
	return s.saveLocked()
}

// Has reports whether h is seeded.
func (s *Seeder) Has(h Hash) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(s.entries, func(e *seedEntry) bool { return e.hash == h })
}

// check removes the torrents plan says to, saves the state, and returns
// the upload since the last check, which it also counts.
func (s *Seeder) check(ctx context.Context) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var uploaded int64
	status := make([]seedStatus, len(s.entries))
	for i, e := range s.entries {
		up := e.uploaded()
		if d := up - s.reported[e.hash]; d > 0 {
			s.uploaded.Add(ctx, d)
			uploaded += d
		}
		s.reported[e.hash] = up
		status[i] = seedStatus{added: e.added, downloaded: e.downloaded, uploaded: up}
	}
	why := plan(status, s.cfg.Now(), s.cfg.MaxBytes, s.cfg.MaxAge)
	kept := s.entries[:0]
	for i, e := range s.entries {
		if why[i] == "" {
			kept = append(kept, e)
			continue
		}
		s.removals.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.seed.reason", why[i])))
		s.cfg.Log.Debug("seed removed", "infohash", hex.EncodeToString(e.hash[:]), "reason", why[i],
			"downloaded", e.downloaded, "uploaded", status[i].uploaded)
		if err := e.t.remove(); err != nil {
			s.cfg.Log.Warn("remove seed", "infohash", hex.EncodeToString(e.hash[:]), "err", err)
		}
		_ = os.Remove(s.infoPath(e.hash))
		delete(s.reported, e.hash)
	}
	clear(s.entries[len(kept):])
	s.entries = kept
	if err := s.saveLocked(); err != nil {
		s.cfg.Log.Warn("save seeds", "err", err)
	}
	return uploaded
}

// report tells Ingest of uploaded, and of what earlier reports failed to
// send; nothing without Ingest.
func (s *Seeder) report(ctx context.Context, uploaded int64) {
	s.unsent += uploaded
	if s.cfg.Ingest == nil || s.unsent == 0 {
		return
	}
	if _, err := s.cfg.Ingest.ProbeUpload(ctx, connect.NewRequest(&sniffarrv1.ProbeUploadRequest{Uploaded: s.unsent})); err != nil {
		if ctx.Err() == nil {
			s.cfg.Log.Warn("report upload", "err", err)
		}
		return
	}
	s.unsent = 0
}

// announce announces every seeded torrent.
func (s *Seeder) announce() {
	s.mu.Lock()
	ts := make([]seedTorrent, len(s.entries))
	for i, e := range s.entries {
		ts[i] = e.t
	}
	s.mu.Unlock()
	for _, t := range ts {
		t.announce()
	}
}

// Run checks the torrents every minute, reporting their upload to Ingest,
// and announces them every 30 minutes, until ctx is done.
func (s *Seeder) Run(ctx context.Context) error {
	checkTick := time.NewTicker(seedCheck)
	defer checkTick.Stop()
	announceTick := time.NewTicker(seedAnnounce)
	defer announceTick.Stop()
	s.announce()
	for {
		select {
		case <-ctx.Done():
			last := context.WithoutCancel(ctx)
			s.report(last, s.check(last))
			return ctx.Err()
		case <-checkTick.C:
			s.report(ctx, s.check(ctx))
		case <-announceTick.C:
			s.announce()
		}
	}
}

// saveLocked writes the state file, replacing it whole.
func (s *Seeder) saveLocked() error {
	states := make([]seedState, len(s.entries))
	for i, e := range s.entries {
		states[i] = seedState{Infohash: hex.EncodeToString(e.hash[:]), Added: e.added, Downloaded: e.downloaded, Uploaded: e.uploaded()}
	}
	b, err := json.Marshal(states)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.cfg.Dir, 0o755); err != nil {
		return err
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}

// seedStatus is what plan weighs of a seeded torrent.
type seedStatus struct {
	added                time.Time
	downloaded, uploaded int64
}

// plan returns, for each torrent of seeds in the order they were added,
// why to remove it at now, or "" to keep it: seedRatio once it uploaded as
// much as it downloaded, else seedAge once older than maxAge, else
// seedSize while the torrents kept, oldest first, download more than
// maxBytes.
func plan(seeds []seedStatus, now time.Time, maxBytes int64, maxAge time.Duration) []string {
	why := make([]string, len(seeds))
	var kept int64
	for i, st := range seeds {
		switch {
		case st.uploaded >= st.downloaded:
			why[i] = seedRatio
		case now.Sub(st.added) >= maxAge:
			why[i] = seedAge
		default:
			kept += st.downloaded
		}
	}
	for i, st := range seeds {
		if kept <= maxBytes {
			break
		}
		if why[i] == "" {
			why[i] = seedSize
			kept -= st.downloaded
		}
	}
	return why
}
