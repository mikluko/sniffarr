package sniff

import (
	"crypto/sha256"
	"testing"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/require"
)

// TestV2InfoWithEmptyFiles sets the info of a v2 torrent with empty files
// after a piece, as a fetch does on metadata from a peer. The library panicked
// there until anacrolix/torrent#1194; go.mod replaces it until that lands.
func TestV2InfoWithEmptyFiles(t *testing.T) {
	const pieceLength = 1 << 14
	root := sha256.Sum256(make([]byte, pieceLength))
	file := metainfo.FileTree{File: metainfo.FileTreeFile{Length: pieceLength, PiecesRoot: string(root[:])}}
	empty := metainfo.FileTree{File: metainfo.FileTreeFile{Length: 0}}
	info := metainfo.Info{
		Name:        "x",
		PieceLength: pieceLength,
		MetaVersion: 2,
		FileTree:    metainfo.FileTree{Dir: map[string]metainfo.FileTree{"a": file, "b": empty, "c": empty, "d": file, "e": empty}},
	}
	ib, err := bencode.Marshal(&info)
	require.NoError(t, err)

	cfg := torrent.NewDefaultClientConfig()
	cfg.DefaultStorage = noStorage{}
	cfg.DisableIPv6 = true
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.ListenPort = 0
	cfg.NoDHT = true
	cfg.NoDefaultPortForwarding = true
	cl, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()

	tt, err := cl.AddTorrent(&metainfo.MetaInfo{InfoBytes: ib})
	require.NoError(t, err)
	<-tt.GotInfo()
	require.Equal(t, 2, tt.NumPieces())
}
