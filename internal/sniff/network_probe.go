package sniff

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"sarak.sh/mikluko/sniffarr/internal/container"
)

// piecePoll is how often a probe looks whether a requested piece is done.
const piecePoll = 250 * time.Millisecond

// announceFor bounds one DHT announce of a seeded torrent.
const announceFor = 2 * time.Minute

// openSeeds opens the storage of probed torrents in Seed.Dir, with the piece
// completion kept beside it, and restores the seeded torrents.
func (n *Network) openSeeds() error {
	dir := n.cfg.Seed.Dir
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		return err
	}
	pc, err := storage.NewBoltPieceCompletion(dir)
	if err != nil {
		return fmt.Errorf("piece completion: %w", err)
	}
	n.completion = pc
	n.seedStorage = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: filepath.Join(dir, "data"),
		TorrentDirMaker: func(base string, _ *metainfo.Info, ih metainfo.Hash) string {
			return filepath.Join(base, ih.HexString())
		},
		PieceCompletion: pc,
		UsePartFiles:    g.Some(false),
	})
	return n.seeder.Restore(func(h Hash, info []byte) (seedTorrent, error) {
		t, _ := n.client.AddTorrentOpt(torrent.AddTorrentOpts{InfoHash: metainfo.Hash(h), InfoBytes: info, Storage: n.seedStorage})
		if t.Info() == nil {
			n.dropSeed(t, h)
			return nil, errors.New("info does not match the infohash")
		}
		t.CancelPieces(0, t.NumPieces())
		return clientSeed{t: t, n: n, h: h}, nil
	})
}

// dropSeed drops the probed torrent t of h and deletes its data and piece
// completion.
func (n *Network) dropSeed(t *torrent.Torrent, h Hash) error {
	pieces := 0
	if t.Info() != nil {
		pieces = t.NumPieces()
	}
	t.Drop()
	var errs []error
	for i := range pieces {
		errs = append(errs, n.completion.Set(metainfo.PieceKey{InfoHash: metainfo.Hash(h), Index: i}, g.None[bool]()))
	}
	errs = append(errs, os.RemoveAll(filepath.Join(n.cfg.Seed.Dir, "data", metainfo.Hash(h).HexString())))
	return errors.Join(errs...)
}

// Probe reads the tracks of the torrent's main video file, the largest of a
// format container reads, downloading the pieces it needs from the peers the
// DHT names and keeping the torrent seeded when it downloaded any. It needs
// NetworkConfig.Seed.Dir.
func (n *Network) Probe(ctx context.Context, h Hash, maxBytes int64) (Probed, error) {
	if n.seeder == nil {
		return Probed{}, errors.New("probing needs a seed directory")
	}
	if err := n.wait(ctx); err != nil {
		return Probed{}, err
	}
	seeded := n.seeder.Has(h)
	peers := n.findPeers(ctx, h, max(n.cfg.MinPeers, enoughPeers))
	if len(peers) < n.cfg.MinPeers && !seeded {
		return Probed{}, ErrNoPeers
	}
	t, added := n.client.AddTorrentOpt(torrent.AddTorrentOpts{InfoHash: metainfo.Hash(h), Storage: n.seedStorage})
	t.AddPeers(peers)
	keep := !added
	defer func() {
		if !keep {
			_ = n.dropSeed(t, h)
		}
	}()
	timer := time.NewTimer(n.cfg.FetchTimeout)
	defer timer.Stop()
	select {
	case <-t.GotInfo():
	case <-timer.C:
		return Probed{}, ErrNoMetadata
	case <-ctx.Done():
		return Probed{}, ctx.Err()
	}
	tfs := t.Files()
	files := make([]File, len(tfs))
	for i, f := range tfs {
		files[i] = File{Path: f.DisplayPath(), Size: f.Length()}
	}
	i, ok := mainFile(files)
	if !ok {
		return Probed{}, fmt.Errorf("%w: no Matroska or MP4 file", ErrTracksUnknown)
	}
	f := tfs[i]
	r := &pieceReader{ctx: ctx, src: torrentPieces{t}, base: f.Offset(), size: f.Length(), max: maxBytes}
	info, err := container.Probe(r, f.Length())
	t.CancelPieces(0, t.NumPieces())
	probed := Probed{Path: files[i].Path, Downloaded: r.downloaded}
	if r.downloaded > 0 {
		if serr := n.seeder.Add(ctx, h, clientSeed{t: t, n: n, h: h}, r.downloaded, t.Metainfo().InfoBytes); serr != nil {
			n.seeder.cfg.Log.Warn("seed", "err", serr)
		} else {
			keep = true
		}
	}
	switch {
	case err == nil:
		probed.Format, probed.Tracks = info.Format, info.Tracks
		return probed, nil
	case ctx.Err() != nil:
		return probed, ctx.Err()
	}
	return probed, fmt.Errorf("%w: %w", ErrTracksUnknown, err)
}

// torrentPieces is the piece layer of a client's torrent with its info.
type torrentPieces struct{ t *torrent.Torrent }

func (p torrentPieces) pieceLength() int64    { return p.t.Info().PieceLength }
func (p torrentPieces) numPieces() int        { return p.t.NumPieces() }
func (p torrentPieces) pieceSize(i int) int64 { return p.t.Info().Piece(i).Length() }
func (p torrentPieces) complete(i int) bool   { return p.t.Piece(i).State().Complete }
func (p torrentPieces) request(i int)         { p.t.Piece(i).SetPriority(torrent.PiecePriorityNow) }

func (p torrentPieces) wait(ctx context.Context, i int) error {
	tick := time.NewTicker(piecePoll)
	defer tick.Stop()
	for !p.complete(i) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	return nil
}

func (p torrentPieces) readAt(ctx context.Context, b []byte, off int64) (int, error) {
	r := p.t.NewReader()
	defer r.Close()
	r.SetReadahead(0)
	r.SetContext(ctx)
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(r, b)
}

// clientSeed is a probed torrent the client seeds.
type clientSeed struct {
	t *torrent.Torrent
	n *Network
	h Hash
}

func (s clientSeed) uploaded() int64 {
	st := s.t.Stats()
	return st.BytesWrittenData.Int64()
}

func (s clientSeed) announce() {
	done, stop, err := s.t.AnnounceToDht(torrent.AnacrolixDhtServerWrapper{Server: s.n.dht})
	if err != nil {
		return
	}
	go func() {
		defer stop()
		select {
		case <-done:
		case <-time.After(announceFor):
		case <-s.n.stop:
		}
	}()
}

func (s clientSeed) remove() error { return s.n.dropSeed(s.t, s.h) }
