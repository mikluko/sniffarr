package container

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

const maxMoov = 64 << 20

// topLevelBox reports whether typ may open an ISO BMFF or QuickTime file.
func topLevelBox(typ string) bool {
	switch typ {
	case "ftyp", "moov", "mdat", "free", "skip", "wide":
		return true
	}
	return false
}

func probeMP4(r io.ReaderAt, size int64) ([]Track, error) {
	for pos := int64(0); size-pos >= 8; {
		b, err := readAt(r, pos, min(16, size-pos), size)
		if err != nil {
			return nil, err
		}
		typ, hdr, boxSize, err := boxHeader(b, uint64(size-pos))
		if err != nil {
			return nil, fmt.Errorf("%w at %d", err, pos)
		}
		if typ == "moov" {
			if boxSize-hdr > maxMoov || boxSize > uint64(size-pos) {
				return nil, fmt.Errorf("%w: moov of %d bytes at %d", errMalformed, boxSize, pos)
			}
			body, err := readAt(r, pos+int64(hdr), int64(boxSize-hdr), size)
			if err != nil {
				return nil, err
			}
			return parseMoov(body)
		}
		if boxSize > uint64(size-pos) {
			break
		}
		pos += int64(boxSize)
	}
	return nil, ErrNoTracks
}

// boxHeader decodes the box header at the start of b; a size of 0 extends the
// box over the remaining bytes.
func boxHeader(b []byte, remaining uint64) (typ string, hdr, size uint64, err error) {
	if len(b) < 8 {
		return "", 0, 0, fmt.Errorf("%w: short box header", errMalformed)
	}
	size, typ, hdr = uint64(binary.BigEndian.Uint32(b)), string(b[4:8]), 8
	switch size {
	case 0:
		size = remaining
	case 1:
		if len(b) < 16 {
			return "", 0, 0, fmt.Errorf("%w: short box header", errMalformed)
		}
		size, hdr = binary.BigEndian.Uint64(b[8:]), 16
	}
	if size < hdr {
		return "", 0, 0, fmt.Errorf("%w: box %q of size %d", errMalformed, typ, size)
	}
	return typ, hdr, size, nil
}

// boxes calls fn for each box in b; fewer than 8 trailing bytes are ignored.
func boxes(b []byte, fn func(typ string, body []byte) error) error {
	for len(b) >= 8 {
		typ, hdr, size, err := boxHeader(b, uint64(len(b)))
		if err != nil {
			return err
		}
		if size > uint64(len(b)) {
			return fmt.Errorf("%w: box %q of size %d", errMalformed, typ, size)
		}
		if err := fn(typ, b[hdr:size]); err != nil {
			return err
		}
		b = b[size:]
	}
	return nil
}

func parseMoov(b []byte) ([]Track, error) {
	tracks := []Track{}
	err := boxes(b, func(typ string, b []byte) error {
		if typ != "trak" {
			return nil
		}
		t, ok, err := parseTrak(b)
		if ok {
			tracks = append(tracks, t)
		}
		return err
	})
	return tracks, err
}

// mp4Track collects what a trak box declares before it is classified.
type mp4Track struct {
	enabled              bool
	width, height        int
	language, bcp47      string
	handler, handlerName string
	userName             string
	entryType            string
	entry                []byte
}

// parseTrak decodes a trak body; ok is false for handlers other than video,
// sound and subtitles.
func parseTrak(b []byte) (t Track, ok bool, err error) {
	var m mp4Track
	err = boxes(b, func(typ string, b []byte) error {
		switch typ {
		case "tkhd":
			m.parseTkhd(b)
		case "mdia":
			return boxes(b, m.parseMdia)
		case "udta":
			return boxes(b, func(typ string, b []byte) error {
				if typ == "name" {
					m.userName = cString(b)
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return Track{}, false, err
	}
	t = Track{
		Language:      m.language,
		LanguageBCP47: m.bcp47,
		Name:          m.userName,
		Codec:         m.entryType,
		Default:       m.enabled,
	}
	if t.Name == "" && !genericHandlerName(m.handlerName) {
		t.Name = m.handlerName
	}
	switch m.handler {
	case "vide":
		t.Kind = Video
		err = m.parseVisualEntry(&t)
	case "soun":
		t.Kind = Audio
		t.Channels = m.channels()
	case "sbtl", "subt", "text":
		t.Kind = Subtitle
	default:
		return Track{}, false, nil
	}
	if err != nil {
		return Track{}, false, err
	}
	return t, true, nil
}

func (m *mp4Track) parseTkhd(b []byte) {
	if len(b) < 4 {
		return
	}
	m.enabled = b[3]&1 != 0
	if (b[0] == 0 && len(b) >= 84) || (b[0] == 1 && len(b) >= 96) {
		m.width = int(binary.BigEndian.Uint32(b[len(b)-8:]) >> 16)
		m.height = int(binary.BigEndian.Uint32(b[len(b)-4:]) >> 16)
	}
}

func (m *mp4Track) parseMdia(typ string, b []byte) error {
	switch typ {
	case "mdhd":
		m.language = mdhdLanguage(b)
	case "hdlr":
		if len(b) >= 24 {
			m.handler = string(b[8:12])
			m.handlerName = hdlrName(b[24:])
		}
	case "elng":
		if len(b) >= 4 {
			m.bcp47 = cString(b[4:])
		}
	case "minf":
		return boxes(b, func(typ string, b []byte) error {
			if typ != "stbl" {
				return nil
			}
			return boxes(b, m.parseStbl)
		})
	}
	return nil
}

func (m *mp4Track) parseStbl(typ string, b []byte) error {
	if typ != "stsd" || len(b) < 16 {
		return nil
	}
	b = b[8:]
	typ, hdr, size, err := boxHeader(b, uint64(len(b)))
	if err != nil {
		return err
	}
	if size > uint64(len(b)) {
		return fmt.Errorf("%w: sample entry %q of size %d", errMalformed, typ, size)
	}
	m.entryType, m.entry = typ, b[hdr:size]
	return nil
}

// channels returns the channel count of an AudioSampleEntry, reading the
// QuickTime version 2 layout where the entry declares it.
func (m *mp4Track) channels() int {
	b := m.entry
	if len(b) < 28 {
		return 0
	}
	if binary.BigEndian.Uint16(b[8:]) == 2 && len(b) >= 44 {
		return toInt(uint64(binary.BigEndian.Uint32(b[40:])))
	}
	return int(binary.BigEndian.Uint16(b[16:]))
}

// parseVisualEntry fills t's size and HDR from the VisualSampleEntry, falling
// back to the tkhd size when the entry declares none.
func (m *mp4Track) parseVisualEntry(t *Track) error {
	t.Width, t.Height = m.width, m.height
	dolbyVision := m.entryType == "dvh1" || m.entryType == "dvhe" || m.entryType == "dva1" || m.entryType == "dvav"
	var transfer uint64
	var mastered bool
	b := m.entry
	if len(b) >= 78 {
		if w, h := int(binary.BigEndian.Uint16(b[24:])), int(binary.BigEndian.Uint16(b[26:])); w > 0 && h > 0 {
			t.Width, t.Height = w, h
		}
		err := boxes(b[78:], func(typ string, b []byte) error {
			switch {
			case typ == "colr" && len(b) >= 10 && (string(b[:4]) == "nclx" || string(b[:4]) == "nclc"):
				transfer = uint64(binary.BigEndian.Uint16(b[6:]))
			case typ == "mdcv":
				mastered = true
			case dolbyVisionConfig(typ):
				dolbyVision = true
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	t.HDR = hdr(dolbyVision, transfer, mastered)
	return nil
}

// mdhdLanguage decodes the packed ISO 639-2 language of an mdhd body, "" for
// "und" or a QuickTime Macintosh language code.
func mdhdLanguage(b []byte) string {
	off := 20
	if len(b) > 0 && b[0] == 1 {
		off = 32
	}
	if len(b) < off+2 {
		return ""
	}
	v := binary.BigEndian.Uint16(b[off:])
	lang := []byte{byte(v>>10&0x1F) + 0x60, byte(v>>5&0x1F) + 0x60, byte(v&0x1F) + 0x60}
	for _, c := range lang {
		if c < 'a' || c > 'z' {
			return ""
		}
	}
	if string(lang) == "und" {
		return ""
	}
	return string(lang)
}

// hdlrName decodes an hdlr name, NUL-terminated in ISO BMFF and a counted
// string in QuickTime.
func hdlrName(b []byte) string {
	if len(b) > 0 && int(b[0]) == len(b)-1 {
		return cString(b[1:])
	}
	return cString(b)
}

// genericHandlerName reports whether name is a muxer's stock handler name
// rather than a title someone gave the track.
func genericHandlerName(name string) bool {
	return name == "" || strings.HasSuffix(name, "Handler") || strings.HasPrefix(name, "Core Media ")
}
