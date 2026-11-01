package container

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// countingReader records every range read through it.
type countingReader struct {
	r     io.ReaderAt
	mu    sync.Mutex
	reads [][2]int64
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	c.mu.Lock()
	c.reads = append(c.reads, [2]int64{off, off + int64(len(p))})
	c.mu.Unlock()
	return c.r.ReadAt(p, off)
}

// requireUntouched fails when any read overlapped [from, to).
func (c *countingReader) requireUntouched(t *testing.T, from, to int64) {
	t.Helper()
	for _, rd := range c.reads {
		require.False(t, rd[0] < to && rd[1] > from, "read %d-%d overlaps %d-%d", rd[0], rd[1], from, to)
	}
}

var errTooFar = errors.New("too far")

// limitReader refuses reads that reach past limit.
type limitReader struct {
	r     io.ReaderAt
	limit int64
}

func (l limitReader) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > l.limit {
		return 0, errTooFar
	}
	return l.r.ReadAt(p, off)
}

func probeBytes(b []byte) (Info, error) {
	return Probe(bytes.NewReader(b), int64(len(b)))
}

func TestUnknownFormat(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("hello, world, this is text"), {0x1A, 0x45}} {
		_, err := probeBytes(b)
		require.ErrorIs(t, err, ErrUnknownFormat, "%q", b)
	}
}

func TestReaderErrorWrapped(t *testing.T) {
	mkv, _ := mkvTracksAfterCluster()
	mp4, _ := mp4MdatFirst()
	for name, b := range map[string][]byte{"matroska": mkv, "mp4": mp4} {
		t.Run(name, func(t *testing.T) {
			_, err := Probe(limitReader{r: bytes.NewReader(b), limit: 4 << 10}, int64(len(b)))
			require.ErrorIs(t, err, errTooFar)
		})
	}
}

func TestTruncated(t *testing.T) {
	mkv, tracksEnd := mkvTracksBeforeCluster()
	mp4, moovEnd := mp4MoovFirst()
	for name, c := range map[string]struct {
		b   []byte
		end int
	}{"matroska": {mkv, tracksEnd}, "mp4": {mp4, moovEnd}} {
		t.Run(name, func(t *testing.T) {
			for n := range c.end {
				_, err := probeBytes(c.b[:n])
				require.Error(t, err, "truncated at %d", n)
			}
			for n := c.end; n <= len(c.b); n += 997 {
				_, err := probeBytes(c.b[:n])
				require.NoError(t, err, "truncated at %d", n)
			}
		})
	}
}

func FuzzProbe(f *testing.F) {
	mkv, _ := mkvTracksBeforeCluster()
	mp4, _ := mp4MoovFirst()
	f.Add(mkv[:min(len(mkv), 4096)])
	f.Add(mp4[:min(len(mp4), 4096)])
	f.Add(mkvUnknownSegment())
	f.Add(mp4MoovOnly())
	f.Add([]byte("not a movie"))
	f.Fuzz(func(t *testing.T, b []byte) {
		info, err := probeBytes(b)
		if err == nil && info.Format == "" {
			t.Fatalf("no error and no format for %x", b)
		}
	})
}
