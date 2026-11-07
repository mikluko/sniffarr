// Package container reads the track list a Matroska, WebM, MP4 or MOV file
// declares in its header, without reading the media data.
package container

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
)

// Format is a container format Probe recognises.
type Format string

const (
	Matroska Format = "matroska"
	MP4      Format = "mp4"
)

// Kind is the kind of media a track carries.
type Kind string

const (
	Video    Kind = "video"
	Audio    Kind = "audio"
	Subtitle Kind = "subtitle"
)

// Track is one track of a file as its container header declares it.
type Track struct {
	Kind Kind
	// Language is the Matroska Language, "eng" when absent, or the MP4 mdhd
	// language, "" when it is "und" or not an ISO 639-2 code.
	Language string
	// LanguageBCP47 is the Matroska LanguageBCP47 or the MP4 elng box, "" when absent.
	LanguageBCP47 string
	// Name is the Matroska Name, or the MP4 udta name or hdlr name; generic
	// handler names such as "SoundHandler" yield "".
	Name string
	// Codec is the Matroska CodecID or the MP4 sample entry fourcc.
	Codec string
	// Channels is the audio channel count.
	Channels int
	// Width and Height are the video size in pixels.
	Width, Height int
	// HDR is "DV", "HDR10", "HLG" or "" for a video track; Dolby Vision wins over the base layer's transfer.
	HDR string
	// Default is the Matroska FlagDefault or the MP4 tkhd enabled flag.
	Default bool
	// Forced is the Matroska FlagForced; always false for MP4.
	Forced bool
}

// TrackLimit is the most tracks Probe reads: a real file carries a few
// dozen, and a header can declare millions of tracks a few bytes each.
const TrackLimit = 128

// addTrack appends t to tracks, or fails with ErrTooManyTracks once they
// hold TrackLimit.
func addTrack(tracks []Track, t Track) ([]Track, error) {
	if len(tracks) >= TrackLimit {
		return tracks, ErrTooManyTracks
	}
	return append(tracks, t), nil
}

// Info is what Probe learns of a file.
type Info struct {
	Format Format
	Tracks []Track
}

var (
	// ErrUnknownFormat reports a file that is neither Matroska/WebM nor ISO BMFF.
	ErrUnknownFormat = errors.New("container: unknown format")
	// ErrNoTracks reports a file of a known format whose header points to no track list.
	ErrNoTracks = errors.New("container: no track list")
	// ErrTooManyTracks reports a track list of more than TrackLimit tracks.
	ErrTooManyTracks = fmt.Errorf("container: more than %d tracks", TrackLimit)

	errMalformed = errors.New("container: malformed header")
)

// Probe reads the tracks of a file of size bytes through r.
// Errors from r other than io.EOF are wrapped, so errors.Is sees them.
func Probe(r io.ReaderAt, size int64) (Info, error) {
	head, err := readAt(r, 0, min(size, 12), size)
	if err != nil {
		return Info{}, err
	}
	switch {
	case bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		tracks, err := probeMatroska(r, size)
		return Info{Format: Matroska, Tracks: tracks}, err
	case len(head) >= 8 && topLevelBox(string(head[4:8])):
		tracks, err := probeMP4(r, size)
		return Info{Format: MP4, Tracks: tracks}, err
	}
	return Info{}, ErrUnknownFormat
}

// readAt reads the n bytes at off, which must lie within the first size bytes.
func readAt(r io.ReaderAt, off, n, size int64) ([]byte, error) {
	if off < 0 || n < 0 || off > size || n > size-off {
		return nil, fmt.Errorf("%w: %d bytes at %d past the end at %d", errMalformed, n, off, size)
	}
	buf := make([]byte, n)
	m, err := r.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("container: read %d bytes at %d: %w", n, off, err)
	}
	if m < len(buf) {
		return nil, fmt.Errorf("container: read %d bytes at %d: %w", n, off, io.ErrUnexpectedEOF)
	}
	return buf, nil
}

// hdr classifies a video track's dynamic range.
func hdr(dolbyVision bool, transfer uint64, mastering bool) string {
	switch {
	case dolbyVision:
		return "DV"
	case transfer == 18:
		return "HLG"
	case transfer == 16 || mastering:
		return "HDR10"
	}
	return ""
}

// dolbyVisionConfig reports whether fourcc names a Dolby Vision configuration record.
func dolbyVisionConfig(fourcc string) bool {
	return fourcc == "dvcC" || fourcc == "dvvC" || fourcc == "dvwC"
}

// toInt converts v to int, saturating at math.MaxInt32.
func toInt(v uint64) int {
	return int(min(v, math.MaxInt32))
}

// cString returns b up to its first NUL.
func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}
