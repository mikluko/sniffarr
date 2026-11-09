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

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
)

const (
	// DefaultSeedMaxBytes is the default SeederConfig.MaxBytes, 200 GiB.
	DefaultSeedMaxBytes = 200 << 30
	// DefaultSeedMaxAge is the default SeederConfig.MaxAge.
	DefaultSeedMaxAge = 7 * 24 * time.Hour
	// DefaultSeedIdle is the default SeederConfig.Idle.
	DefaultSeedIdle = 15 * time.Minute
)

const (
	// seedCheck is how often a Seeder looks for torrents to remove.
	seedCheck = time.Minute
	// seedAnnounce is how often a Seeder announces each torrent on the DHT.
	seedAnnounce = 30 * time.Minute
)

// Why a Seeder removes a torrent.
const (
	seedRatio   = "ratio"
	seedAge     = "age"
	seedSize    = "size"
	seedUseless = "useless"
	seedIdle    = "idle"
)

// seedTorrent is a torrent a Seeder keeps in its swarm.
type seedTorrent interface {
	// uploaded is the piece data uploaded since the torrent was opened.
	uploaded() int64
	// needed reports whether some connected peer lacks a piece the torrent
	// has complete.
	needed() bool
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
	// fresh holds until the first check after Add; active is when a peer
	// last needed the torrent or it last uploaded, or when it was added or
	// restored.
	fresh  bool
	active time.Time
	t      seedTorrent
}

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
	// Idle caps how long a torrent no connected peer needs is seeded
	// without uploading; DefaultSeedIdle.
	Idle time.Duration
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
// once it is MaxAge old, when no connected peer needs it at the first check
// after Add or for Idle without an upload, or when it is the oldest beyond
// MaxBytes. It is safe for concurrent use.
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
	orDefault(&cfg.Idle, DefaultSeedIdle)
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
// missing state file restores none. A restored torrent starts its Idle at
// the restore and, its peers yet to connect, is not judged at the first
// check as an added one is.
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
	now := s.cfg.Now()
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
		s.entries = append(s.entries, &seedEntry{hash: h, added: st.Added, downloaded: st.Downloaded, uploadedBefore: st.Uploaded, active: now, t: t})
		s.reported[h] = st.Uploaded
	}
	slices.SortStableFunc(s.entries, func(a, b *seedEntry) int { return a.added.Compare(b.added) })
	return s.saveLocked()
}

// Add seeds t, of which downloaded bytes of piece data were downloaded,
// keeping its info dictionary for Restore. A torrent seeded already gets
// the bytes added and t in place of the one it had, and is judged at the
// next check as an added one is.
func (s *Seeder) Add(ctx context.Context, h Hash, t seedTorrent, downloaded int64, info []byte) error {
	s.downloaded.Add(ctx, downloaded)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	for _, e := range s.entries {
		if e.hash == h {
			e.downloaded += downloaded
			e.t, e.fresh, e.active = t, true, now
			return s.saveLocked()
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.infoPath(h)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.infoPath(h), info, 0o644); err != nil {
		return err
	}
	s.entries = append(s.entries, &seedEntry{hash: h, added: now, downloaded: downloaded, fresh: true, active: now, t: t})
	return s.saveLocked()
}

// Has reports whether h is seeded.
func (s *Seeder) Has(h Hash) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(s.entries, func(e *seedEntry) bool { return e.hash == h })
}

// seedActivity is what a check reads of a seeded torrent.
type seedActivity struct {
	uploaded int64
	needed   bool
}

// read returns the activity of each torrent seeded, by entry, reading the
// torrents without holding the mutex.
func (s *Seeder) read() map[*seedEntry]seedActivity {
	type seen struct {
		e      *seedEntry
		t      seedTorrent
		before int64
	}
	s.mu.Lock()
	seeds := make([]seen, len(s.entries))
	for i, e := range s.entries {
		seeds[i] = seen{e: e, t: e.t, before: e.uploadedBefore}
	}
	s.mu.Unlock()
	read := make(map[*seedEntry]seedActivity, len(seeds))
	for _, sd := range seeds {
		read[sd.e] = seedActivity{uploaded: sd.before + sd.t.uploaded(), needed: sd.t.needed()}
	}
	return read
}

// check removes the torrents plan says to, saves the state, and returns
// the upload since the last check, which it also counts. A torrent added
// while it reads the torrents is kept, as one a peer needs.
func (s *Seeder) check(ctx context.Context) int64 {
	read := s.read()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	var uploaded int64
	status := make([]seedStatus, len(s.entries))
	for i, e := range s.entries {
		a, ok := read[e]
		if !ok {
			status[i] = seedStatus{added: e.added, downloaded: e.downloaded, uploaded: s.reported[e.hash], needed: true, active: now}
			continue
		}
		d := a.uploaded - s.reported[e.hash]
		if d > 0 {
			s.uploaded.Add(ctx, d)
			uploaded += d
		}
		s.reported[e.hash] = a.uploaded
		if a.needed || d > 0 {
			e.active = now
		}
		status[i] = seedStatus{added: e.added, downloaded: e.downloaded, uploaded: a.uploaded,
			fresh: e.fresh, needed: a.needed, active: e.active}
		e.fresh = false
	}
	why := plan(status, now, s.cfg)
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

// saveLocked writes the state file, replacing it whole, with the upload
// each torrent reached at the last check.
func (s *Seeder) saveLocked() error {
	states := make([]seedState, len(s.entries))
	for i, e := range s.entries {
		states[i] = seedState{Infohash: hex.EncodeToString(e.hash[:]), Added: e.added, Downloaded: e.downloaded, Uploaded: s.reported[e.hash]}
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
	// fresh marks the first check after Add, needed a connected peer
	// lacking a piece the torrent has, and active when a peer last needed
	// it or it last uploaded.
	fresh, needed bool
	active        time.Time
}

// plan returns, for each torrent of seeds in the order they were added,
// why to remove it at now, or "" to keep it: seedRatio once it uploaded as
// much as it downloaded, else seedAge once older than cfg.MaxAge, else
// seedUseless when fresh and not needed, else seedIdle once inactive for
// cfg.Idle, else seedSize while the torrents kept, oldest first, download
// more than cfg.MaxBytes.
func plan(seeds []seedStatus, now time.Time, cfg SeederConfig) []string {
	why := make([]string, len(seeds))
	var kept int64
	for i, st := range seeds {
		switch {
		case st.uploaded >= st.downloaded:
			why[i] = seedRatio
		case now.Sub(st.added) >= cfg.MaxAge:
			why[i] = seedAge
		case st.fresh && !st.needed:
			why[i] = seedUseless
		case now.Sub(st.active) >= cfg.Idle:
			why[i] = seedIdle
		default:
			kept += st.downloaded
		}
	}
	for i, st := range seeds {
		if kept <= cfg.MaxBytes {
			break
		}
		if why[i] == "" {
			why[i] = seedSize
			kept -= st.downloaded
		}
	}
	return why
}
