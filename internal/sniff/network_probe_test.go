package sniff

import (
	"context"
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/container"
)

// element is an EBML element of the ID, its size in 8 bytes.
func element(id uint32, body ...[]byte) []byte {
	var out []byte
	for shift := 24; shift >= 0; shift -= 8 {
		if b := byte(id >> shift); b != 0 || len(out) > 0 {
			out = append(out, b)
		}
	}
	var n int
	for _, b := range body {
		n += len(b)
	}
	size := make([]byte, 8)
	binary.BigEndian.PutUint64(size, uint64(n)|1<<56)
	out = append(out, size...)
	for _, b := range body {
		out = append(out, b...)
	}
	return out
}

// testMKV is a Matroska file of size bytes whose Tracks has a Russian and an
// English audio track.
func testMKV(size int) []byte {
	b := append(element(0x1A45DFA3, element(0x4282, []byte("matroska"))),
		element(0x18538067,
			element(0x1654AE6B,
				element(0xAE, element(0x83, []byte{2}), element(0x22B59C, []byte("rus")), element(0x536E, []byte("Дубляж")), element(0x86, []byte("A_AC3"))),
				element(0xAE, element(0x83, []byte{2}), element(0x22B59C, []byte("eng")), element(0x86, []byte("A_DTS"))),
			),
		)...)
	return append(b, make([]byte, size-len(b))...)
}

// dataSeed starts a client without DHT on localhost that seeds a torrent of
// one file, and returns its infohash and the client's address.
func dataSeed(t *testing.T, name string, data []byte, pieceLength int64) (Hash, torrent.PeerInfo) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o644))
	info := metainfo.Info{PieceLength: pieceLength}
	require.NoError(t, info.BuildFromFilePath(filepath.Join(dir, name)))
	b, err := bencode.Marshal(info)
	require.NoError(t, err)
	mi := &metainfo.MetaInfo{InfoBytes: b}
	c := torrent.NewDefaultClientConfig()
	c.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir, PieceCompletion: storage.NewMapPieceCompletion()})
	c.DisableIPv6 = true
	c.ListenHost = func(string) string { return "127.0.0.1" }
	c.ListenPort = 0
	c.NoDHT = true
	c.NoDefaultPortForwarding = true
	c.Seed = true
	cl, err := torrent.NewClient(c)
	require.NoError(t, err)
	t.Cleanup(func() { cl.Close() })
	st, err := cl.AddTorrent(mi)
	require.NoError(t, err)
	require.NoError(t, st.VerifyData())
	require.Eventually(t, func() bool { return st.Complete().Bool() }, 5*time.Second, 10*time.Millisecond)
	return Hash(mi.HashInfoBytes()), torrent.PeerInfo{Addr: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(cl.LocalPort()))}
}

// peerSwarm is a swarm whose lookup names one peer.
type peerSwarm struct {
	swarm
	peer netip.AddrPort
}

func (s peerSwarm) lookup(Hash) (<-chan dht.PeersValues, func(), error) {
	ch := make(chan dht.PeersValues, 1)
	ch <- dht.PeersValues{Peers: []krpc.NodeAddr{{IP: s.peer.Addr().AsSlice(), Port: int(s.peer.Port())}}}
	close(ch)
	return ch, func() {}, nil
}

func TestProbeDownloadsTheHeadAndSeeds(t *testing.T) {
	const pieceLength = 1 << 14
	h, peer := dataSeed(t, "Coraline.2009.2160p.mkv", testMKV(16*pieceLength), pieceLength)
	dir := t.TempDir()
	n := started(t, NetworkConfig{Port: freePort(t), Passive: true, FetchTimeout: 10 * time.Second, Seed: SeederConfig{Dir: dir}}, [20]byte{})
	n.swarm = peerSwarm{swarm: n.swarm, peer: peer.Addr.(netip.AddrPort)}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := n.Probe(ctx, h, pieceLength-1)
	require.ErrorIs(t, err, ErrTracksUnknown, "the first piece is beyond the cap")
	require.False(t, n.seeder.Has(h), "nothing downloaded, nothing seeded")

	probed, err := n.Probe(ctx, h, 4*pieceLength)
	require.NoError(t, err)
	require.Equal(t, "Coraline.2009.2160p.mkv", probed.Path)
	require.Equal(t, container.Matroska, probed.Format)
	require.EqualValues(t, pieceLength, probed.Downloaded, "one piece holds the header")
	require.Len(t, probed.Tracks, 2)
	require.Equal(t, "rus", probed.Tracks[0].Language)
	require.Equal(t, "Дубляж", probed.Tracks[0].Name)
	require.True(t, n.seeder.Has(h))
	torrents, bytes := n.seeder.Size()
	require.Equal(t, 1, torrents)
	require.EqualValues(t, pieceLength, bytes)
	_, err = os.Stat(filepath.Join(dir, "data", metainfo.Hash(h).HexString(), "Coraline.2009.2160p.mkv"))
	require.NoError(t, err, "the pieces stay on disk")

	again, err := n.Probe(ctx, h, 4*pieceLength)
	require.NoError(t, err)
	require.Zero(t, again.Downloaded, "a seeded torrent's pieces are not downloaded again")
}

// announceRecorder records the port and impliedPort of each announce.
type announceRecorder struct {
	torrent.DhtServer
	port    int
	implied bool
}

func (r *announceRecorder) Announce(_ [20]byte, port int, implied bool) (torrent.DhtAnnounce, error) {
	r.port, r.implied = port, implied
	return nil, nil
}

func TestExplicitPortAnnouncesTheListeningPort(t *testing.T) {
	r := &announceRecorder{}
	_, err := explicitPort{r}.Announce([20]byte{1}, 30070, true)
	require.NoError(t, err)
	require.Equal(t, 30070, r.port)
	require.False(t, r.implied, "a NAT may rewrite the source port")

	_, err = explicitPort{r}.Announce([20]byte{1}, 0, true)
	require.NoError(t, err)
	require.True(t, r.implied, "no port to name")
}
