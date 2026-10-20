package sniff

import (
	"context"
	"errors"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

var _ storage.ClientImplCloser = noStorage{}

// errNoStorage is returned by every read and write of noStorage.
var errNoStorage = errors.New("sniffarr stores no torrent data")

// noStorage is torrent storage that does no I/O: every piece is incomplete,
// and reads, writes and marks fail.
type noStorage struct{}

func (noStorage) OpenTorrent(context.Context, *metainfo.Info, metainfo.Hash) (storage.TorrentImpl, error) {
	return storage.TorrentImpl{
		Piece: func(metainfo.Piece) storage.PieceImpl { return noPiece{} },
		Close: func() error { return nil },
	}, nil
}

func (noStorage) Close() error { return nil }

type noPiece struct{}

func (noPiece) ReadAt([]byte, int64) (int, error)  { return 0, errNoStorage }
func (noPiece) WriteAt([]byte, int64) (int, error) { return 0, errNoStorage }
func (noPiece) MarkComplete() error                { return errNoStorage }
func (noPiece) MarkNotComplete() error             { return errNoStorage }

func (noPiece) Completion() storage.Completion {
	return storage.Completion{Ok: true}
}
