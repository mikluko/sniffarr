package sniff

import (
	"context"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/require"
)

// seed starts a client without DHT that serves the info's metadata on
// localhost, and returns the infohash and the client's address. The client
// wants the torrent's data, since a client wanting nothing refuses peers.
func seed(t *testing.T, info metainfo.Info) (Hash, torrent.PeerInfo) {
	t.Helper()
	b, err := bencode.Marshal(info)
	require.NoError(t, err)
	mi := &metainfo.MetaInfo{InfoBytes: b}
	c := torrent.NewDefaultClientConfig()
	c.DefaultStorage = noStorage{}
	c.DisableIPv6 = true
	c.ListenHost = func(string) string { return "127.0.0.1" }
	c.ListenPort = 0
	c.NoDHT = true
	c.NoDefaultPortForwarding = true
	cl, err := torrent.NewClient(c)
	require.NoError(t, err)
	t.Cleanup(func() { cl.Close() })
	st, err := cl.AddTorrent(mi)
	require.NoError(t, err)
	st.DownloadAll()
	return Hash(mi.HashInfoBytes()), torrent.PeerInfo{Addr: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(cl.LocalPort()))}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestMetadataTouchesNoFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	long := strings.Repeat("Очень длинное имя каталога ", 20)
	require.Greater(t, len(long), 300)
	h, peer := seed(t, metainfo.Info{
		Name:        "Collection",
		PieceLength: 1 << 14,
		Pieces:      make([]byte, 20),
		Files: []metainfo.FileInfo{
			{Path: []string{long, "video.mkv"}, Length: 1 << 13},
			{Path: []string{long, "_____padding_file_1_____"}, Length: 1 << 13},
		},
	})
	n, err := Start(NetworkConfig{Port: freePort(t), FetchTimeout: 10 * time.Second}, nil)
	require.NoError(t, err)
	t.Cleanup(n.Close)

	m, err := n.swarm.fetch(context.Background(), h, []torrent.PeerInfo{peer})
	require.NoError(t, err)
	require.Equal(t, "Collection", m.Name)
	require.Len(t, m.Files, 2)
	require.Equal(t, long+"/video.mkv", m.Files[0].Path)

	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Empty(t, entries, "nothing is created under the temporary directory")
	wd, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, e := range wd {
		require.NotEqual(t, "Collection", e.Name(), "nor under the working directory")
	}
}
