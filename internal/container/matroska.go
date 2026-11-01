package container

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
)

// Matroska element IDs, as RFC 9559 assigns them.
const (
	idEBML                    = 0x1A45DFA3
	idDocType                 = 0x4282
	idSegment                 = 0x18538067
	idSeekHead                = 0x114D9B74
	idSeek                    = 0x4DBB
	idSeekID                  = 0x53AB
	idSeekPosition            = 0x53AC
	idTracks                  = 0x1654AE6B
	idCluster                 = 0x1F43B675
	idTrackEntry              = 0xAE
	idTrackType               = 0x83
	idName                    = 0x536E
	idLanguage                = 0x22B59C
	idLanguageBCP47           = 0x22B59D
	idCodecID                 = 0x86
	idFlagDefault             = 0x88
	idFlagForced              = 0x55AA
	idVideo                   = 0xE0
	idPixelWidth              = 0xB0
	idPixelHeight             = 0xBA
	idColour                  = 0x55B0
	idTransferCharacteristics = 0x55BA
	idMasteringMetadata       = 0x55D0
	idAudio                   = 0xE1
	idChannels                = 0x9F
	idBlockAdditionMapping    = 0x41E4
	idBlockAddIDType          = 0x41E7
)

const (
	maxEBMLHeader = 4 << 10
	maxSeekHead   = 1 << 20
	maxTracks     = 16 << 20
	maxSeekHeads  = 8
)

// element is the header of an EBML element whose body starts at file offset data.
type element struct {
	id      uint32
	data    int64
	size    uint64
	unknown bool
}

// end returns the offset past the element's body, clamped to limit.
func (e element) end(limit int64) int64 {
	if e.unknown || e.size > uint64(limit-e.data) {
		return limit
	}
	return e.data + int64(e.size)
}

// body reads the element's body, refusing one of unknown size, larger than
// max, or running past limit.
func (e element) body(r io.ReaderAt, limit, max int64) ([]byte, error) {
	if e.unknown || e.size > uint64(max) || e.size > uint64(limit-e.data) {
		return nil, fmt.Errorf("%w: element %#x of size %d at %d", errMalformed, e.id, e.size, e.data)
	}
	return readAt(r, e.data, int64(e.size), limit)
}

// readElement reads the header of the element at off, which ends before end.
func readElement(r io.ReaderAt, off, end int64) (element, error) {
	b, err := readAt(r, off, min(12, end-off), end)
	if err != nil {
		return element{}, err
	}
	id, n, ok := parseID(b)
	if !ok {
		return element{}, fmt.Errorf("%w: element ID at %d", errMalformed, off)
	}
	size, m, unknown, ok := parseSize(b[n:])
	if !ok {
		return element{}, fmt.Errorf("%w: element size at %d", errMalformed, off)
	}
	return element{id: id, data: off + int64(n+m), size: size, unknown: unknown}, nil
}

// parseID decodes the EBML element ID at the start of b, keeping its marker bits.
func parseID(b []byte) (id uint32, n int, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	n = bits.LeadingZeros8(b[0]) + 1
	if n > 4 || len(b) < n {
		return 0, 0, false
	}
	for _, c := range b[:n] {
		id = id<<8 | uint32(c)
	}
	return id, n, true
}

// parseSize decodes the EBML data size at the start of b; unknown reports the
// reserved all-ones value.
func parseSize(b []byte) (size uint64, n int, unknown bool, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false, false
	}
	n = bits.LeadingZeros8(b[0]) + 1
	if len(b) < n {
		return 0, 0, false, false
	}
	size = uint64(b[0] & (0xFF >> n))
	for _, c := range b[1:n] {
		size = size<<8 | uint64(c)
	}
	return size, n, size == 1<<(7*n)-1, true
}

// walk calls fn for each EBML element in b, which must hold whole elements of known size.
func walk(b []byte, fn func(id uint32, body []byte) error) error {
	for len(b) > 0 {
		id, n, ok := parseID(b)
		if !ok {
			return fmt.Errorf("%w: element ID", errMalformed)
		}
		size, m, unknown, ok := parseSize(b[n:])
		if !ok || unknown || size > uint64(len(b)-n-m) {
			return fmt.Errorf("%w: size of element %#x", errMalformed, id)
		}
		b = b[n+m:]
		if err := fn(id, b[:size]); err != nil {
			return err
		}
		b = b[size:]
	}
	return nil
}

// ebmlUint decodes an EBML unsigned integer body.
func ebmlUint(b []byte) (uint64, error) {
	if len(b) > 8 {
		return 0, fmt.Errorf("%w: %d-byte integer", errMalformed, len(b))
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

func probeMatroska(r io.ReaderAt, size int64) ([]Track, error) {
	head, err := readElement(r, 0, size)
	if err != nil {
		return nil, err
	}
	body, err := head.body(r, size, maxEBMLHeader)
	if err != nil {
		return nil, err
	}
	var docType string
	err = walk(body, func(id uint32, b []byte) error {
		if id == idDocType {
			docType = cString(b)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if docType != "matroska" && docType != "webm" {
		return nil, fmt.Errorf("%w: EBML DocType %q", ErrUnknownFormat, docType)
	}
	for pos := head.end(size); pos < size; {
		e, err := readElement(r, pos, size)
		if err != nil {
			return nil, err
		}
		if e.id == idSegment {
			s := segment{r: r, size: size, data: e.data, end: e.end(size)}
			return s.tracks()
		}
		if e.unknown {
			break
		}
		pos = e.end(size)
	}
	return nil, ErrNoTracks
}

// segment is a Matroska Segment whose body spans data to end in a file of size bytes.
type segment struct {
	r         io.ReaderAt
	size      int64
	data, end int64
}

// seek is one SeekHead entry, its position relative to the Segment body.
type seek struct {
	id  uint32
	pos uint64
}

// tracks finds the Segment's Tracks, reading top-level headers up to the first
// Cluster and following SeekHead entries from there.
func (s segment) tracks() ([]Track, error) {
	var seeks []seek
	visited := map[uint64]bool{}
	for pos := s.data; pos < s.end; {
		e, err := readElement(s.r, pos, s.end)
		if err != nil {
			return nil, err
		}
		if e.id == idTracks {
			return s.readTracks(e)
		}
		if e.id == idCluster {
			break
		}
		if e.id == idSeekHead {
			visited[uint64(pos-s.data)] = true
			more, err := s.readSeekHead(e)
			if err != nil {
				return nil, err
			}
			seeks = append(seeks, more...)
		}
		if e.unknown {
			break
		}
		pos = e.end(s.end)
	}
	for len(visited) <= maxSeekHeads {
		for _, sk := range seeks {
			if sk.id == idTracks {
				e, err := s.elementAt(sk.pos, idTracks)
				if err != nil {
					return nil, err
				}
				return s.readTracks(e)
			}
		}
		next := -1
		for i, sk := range seeks {
			if sk.id == idSeekHead && !visited[sk.pos] {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		pos := seeks[next].pos
		visited[pos] = true
		e, err := s.elementAt(pos, idSeekHead)
		if err != nil {
			return nil, err
		}
		more, err := s.readSeekHead(e)
		if err != nil {
			return nil, err
		}
		seeks = append(seeks, more...)
	}
	return nil, ErrNoTracks
}

// elementAt reads the header of the element at pos within the Segment body and checks its ID.
func (s segment) elementAt(pos uint64, id uint32) (element, error) {
	if pos >= uint64(s.end-s.data) {
		return element{}, fmt.Errorf("%w: SeekPosition %d past the Segment", errMalformed, pos)
	}
	e, err := readElement(s.r, s.data+int64(pos), s.end)
	if err != nil {
		return element{}, err
	}
	if e.id != id {
		return element{}, fmt.Errorf("%w: SeekHead points at %#x, not %#x", errMalformed, e.id, id)
	}
	return e, nil
}

func (s segment) readSeekHead(e element) ([]seek, error) {
	b, err := e.body(s.r, s.end, maxSeekHead)
	if err != nil {
		return nil, err
	}
	var seeks []seek
	err = walk(b, func(id uint32, b []byte) error {
		if id != idSeek {
			return nil
		}
		var sk seek
		err := walk(b, func(id uint32, b []byte) error {
			var err error
			switch id {
			case idSeekID:
				var v uint64
				v, err = ebmlUint(b)
				sk.id = uint32(v)
			case idSeekPosition:
				sk.pos, err = ebmlUint(b)
			}
			return err
		})
		seeks = append(seeks, sk)
		return err
	})
	return seeks, err
}

func (s segment) readTracks(e element) ([]Track, error) {
	b, err := e.body(s.r, s.end, maxTracks)
	if err != nil {
		return nil, err
	}
	tracks := []Track{}
	err = walk(b, func(id uint32, b []byte) error {
		if id != idTrackEntry {
			return nil
		}
		t, ok, err := parseTrackEntry(b)
		if ok {
			tracks = append(tracks, t)
		}
		return err
	})
	return tracks, err
}

// parseTrackEntry decodes a TrackEntry body; ok is false for track types other
// than video, audio and subtitle.
func parseTrackEntry(b []byte) (t Track, ok bool, err error) {
	t = Track{Language: "eng", Default: true}
	var (
		trackType, transfer   uint64
		channels              uint64 = 1
		dolbyVision, mastered bool
	)
	err = walk(b, func(id uint32, b []byte) error {
		var err error
		var v uint64
		switch id {
		case idTrackType:
			trackType, err = ebmlUint(b)
		case idName:
			t.Name = cString(b)
		case idLanguage:
			t.Language = cString(b)
		case idLanguageBCP47:
			t.LanguageBCP47 = cString(b)
		case idCodecID:
			t.Codec = cString(b)
		case idFlagDefault:
			v, err = ebmlUint(b)
			t.Default = v != 0
		case idFlagForced:
			v, err = ebmlUint(b)
			t.Forced = v != 0
		case idVideo:
			err = walk(b, func(id uint32, b []byte) error {
				var err error
				var v uint64
				switch id {
				case idPixelWidth:
					v, err = ebmlUint(b)
					t.Width = toInt(v)
				case idPixelHeight:
					v, err = ebmlUint(b)
					t.Height = toInt(v)
				case idColour:
					err = walk(b, func(id uint32, b []byte) error {
						var err error
						switch id {
						case idTransferCharacteristics:
							transfer, err = ebmlUint(b)
						case idMasteringMetadata:
							mastered = true
						}
						return err
					})
				}
				return err
			})
		case idAudio:
			err = walk(b, func(id uint32, b []byte) error {
				var err error
				if id == idChannels {
					channels, err = ebmlUint(b)
				}
				return err
			})
		case idBlockAdditionMapping:
			err = walk(b, func(id uint32, b []byte) error {
				if id != idBlockAddIDType {
					return nil
				}
				v, err := ebmlUint(b)
				if err == nil && v <= 0xFFFFFFFF {
					var fourcc [4]byte
					binary.BigEndian.PutUint32(fourcc[:], uint32(v))
					dolbyVision = dolbyVision || dolbyVisionConfig(string(fourcc[:]))
				}
				return err
			})
		}
		return err
	})
	if err != nil {
		return Track{}, false, err
	}
	switch trackType {
	case 1:
		t.Kind = Video
		t.HDR = hdr(dolbyVision, transfer, mastered)
	case 2:
		t.Kind = Audio
		t.Channels = toInt(channels)
		t.Width, t.Height = 0, 0
	case 17:
		t.Kind = Subtitle
		t.Width, t.Height = 0, 0
	default:
		return Track{}, false, nil
	}
	return t, true, nil
}
