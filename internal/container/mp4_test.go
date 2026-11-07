package container

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

const mdatBodySize = 1 << 20

func be16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

func box(typ string, body ...[]byte) []byte {
	data := bytes.Join(body, nil)
	return append(append(be32(uint32(8+len(data))), typ...), data...)
}

func fullBox(typ string, version byte, flags uint32, body ...[]byte) []byte {
	vf := be32(flags)
	vf[0] = version
	return box(typ, append([][]byte{vf}, body...)...)
}

func ftyp() []byte {
	return box("ftyp", []byte("isom"), be32(0x200), []byte("isomiso2mp41"))
}

func tkhd(enabled bool, w, h uint32) []byte {
	var flags uint32 = 2
	if enabled {
		flags |= 1
	}
	return fullBox("tkhd", 0, flags, make([]byte, 20), make([]byte, 8), make([]byte, 8), make([]byte, 36), be32(w<<16), be32(h<<16))
}

func packLang(lang string) []byte {
	return be16(uint16(lang[0]-0x60)<<10 | uint16(lang[1]-0x60)<<5 | uint16(lang[2]-0x60))
}

func mdhd(lang string) []byte {
	return fullBox("mdhd", 0, 0, make([]byte, 16), packLang(lang), be16(0))
}

func mdhdV1(lang string) []byte {
	return fullBox("mdhd", 1, 0, make([]byte, 28), packLang(lang), be16(0))
}

func hdlr(handler, name string) []byte {
	return fullBox("hdlr", 0, 0, be32(0), []byte(handler), make([]byte, 12), []byte(name), []byte{0})
}

// hdlrQuickTime encodes an hdlr whose name is a counted string.
func hdlrQuickTime(handler, name string) []byte {
	return fullBox("hdlr", 0, 0, []byte("mhlr"), []byte(handler), make([]byte, 12), []byte{byte(len(name))}, []byte(name))
}

func visualEntry(fourcc string, w, h uint16, children ...[]byte) []byte {
	return box(fourcc, append([][]byte{
		make([]byte, 6), be16(1), make([]byte, 16), be16(w), be16(h),
		be32(0x00480000), be32(0x00480000), be32(0), be16(1), make([]byte, 32), be16(0x18), be16(0xFFFF),
	}, children...)...)
}

func audioEntry(fourcc string, channels uint16) []byte {
	return box(fourcc, make([]byte, 6), be16(1), make([]byte, 8), be16(channels), be16(16), be32(0), be32(48000<<16))
}

func colr(transfer uint16) []byte {
	return box("colr", []byte("nclx"), be16(9), be16(transfer), be16(9), []byte{0})
}

func trak(tk []byte, mdia []byte, udta ...[]byte) []byte {
	return box("trak", append([][]byte{tk, mdia}, udta...)...)
}

func mdia(entry []byte, boxes ...[]byte) []byte {
	stbl := box("minf", box("stbl", fullBox("stsd", 0, 0, be32(1), entry), fullBox("stts", 0, 0, be32(0))))
	return box("mdia", append(boxes, stbl)...)
}

func mp4Moov() []byte {
	return box("moov",
		fullBox("mvhd", 0, 0, make([]byte, 96)),
		trak(tkhd(true, 3840, 2160), mdia(visualEntry("hvc1", 3840, 2160, box("hvcC", make([]byte, 23)), colr(16)), mdhd("und"), hdlr("vide", "VideoHandler"))),
		trak(tkhd(true, 0, 0), mdia(audioEntry("mp4a", 2), mdhd("rus"), hdlr("soun", "Дубляж"), fullBox("elng", 0, 0, []byte("ru\x00")))),
		trak(tkhd(false, 0, 0), mdia(audioEntry("ac-3", 6), mdhd("eng"), hdlr("soun", "SoundHandler"))),
		trak(tkhd(false, 0, 0), mdia(box("tx3g", make([]byte, 8)), mdhd("rus"), hdlr("sbtl", "Core Media Text"))),
		trak(tkhd(false, 0, 0), mdia(box("tmcd", make([]byte, 8)), mdhd("und"), hdlr("tmcd", "TimeCodeHandler"))),
	)
}

var mp4MoovWant = []Track{
	{Kind: Video, Codec: "hvc1", Width: 3840, Height: 2160, HDR: "HDR10", Default: true},
	{Kind: Audio, Language: "rus", LanguageBCP47: "ru", Name: "Дубляж", Codec: "mp4a", Channels: 2, Default: true},
	{Kind: Audio, Language: "eng", Codec: "ac-3", Channels: 6},
	{Kind: Subtitle, Language: "rus", Codec: "tx3g"},
}

// mp4MoovFirst returns a file whose moov precedes its mdat, and the offset where moov ends.
func mp4MoovFirst() ([]byte, int) {
	head := append(ftyp(), mp4Moov()...)
	return append(head, box("mdat", make([]byte, mdatBodySize))...), len(head)
}

func mp4MoovOnly() []byte {
	return mp4Moov()
}

// mp4MdatFirst returns a file whose 64-bit-sized mdat precedes its moov, and the range of the mdat body.
func mp4MdatFirst() ([]byte, [2]int64) {
	mdat := append(append(be32(1), "mdat"...), binary.BigEndian.AppendUint64(nil, 16+mdatBodySize)...)
	mdat = append(mdat, make([]byte, mdatBodySize)...)
	moov := box("moov",
		trak(tkhd(true, 1920, 800), mdia(visualEntry("hvc1", 1920, 1080, colr(16), box("dvcC", make([]byte, 24))), mdhd("und"), hdlr("vide", "L-SMASH Video Handler"))),
		trak(tkhd(false, 1920, 1080), mdia(visualEntry("dvh1", 0, 0), mdhd("und"), hdlr("vide", ""))),
		trak(tkhd(true, 0, 0), mdia(audioEntry("ec-3", 6), mdhdV1("eng"), hdlrQuickTime("soun", "English")), box("udta", box("name", []byte("Commentary")))),
	)
	start := int64(len(ftyp()) + 16)
	return append(append(ftyp(), mdat...), moov...), [2]int64{start, start + mdatBodySize}
}

func TestMP4MoovFirst(t *testing.T) {
	file, moovEnd := mp4MoovFirst()
	r := &countingReader{r: bytes.NewReader(file)}
	info, err := Probe(r, int64(len(file)))
	require.NoError(t, err)
	require.Equal(t, MP4, info.Format)
	require.Equal(t, mp4MoovWant, info.Tracks)
	r.requireUntouched(t, int64(moovEnd), int64(len(file)))
}

func TestMP4MdatFirst(t *testing.T) {
	file, body := mp4MdatFirst()
	r := &countingReader{r: bytes.NewReader(file)}
	info, err := Probe(r, int64(len(file)))
	require.NoError(t, err)
	require.Equal(t, []Track{
		{Kind: Video, Codec: "hvc1", Width: 1920, Height: 1080, HDR: "DV", Default: true},
		{Kind: Video, Codec: "dvh1", Width: 1920, Height: 1080, HDR: "DV"},
		{Kind: Audio, Language: "eng", Name: "Commentary", Codec: "ec-3", Channels: 6, Default: true},
	}, info.Tracks)
	r.requireUntouched(t, body[0], body[1])
}

func TestMP4QuickTimeHandlerName(t *testing.T) {
	file := append(ftyp(), box("moov",
		trak(tkhd(true, 0, 0), mdia(audioEntry("ac-3", 6), mdhd("eng"), hdlrQuickTime("soun", "English"))),
	)...)
	info, err := probeBytes(file)
	require.NoError(t, err)
	require.Equal(t, []Track{{Kind: Audio, Language: "eng", Name: "English", Codec: "ac-3", Channels: 6, Default: true}}, info.Tracks)
}

func TestMP4TrackLimit(t *testing.T) {
	file := func(n int) []byte {
		traks := make([][]byte, n)
		for i := range traks {
			traks[i] = trak(tkhd(true, 0, 0), mdia(audioEntry("ac-3", 6), mdhd("eng"), hdlr("soun", "")))
		}
		return append(ftyp(), box("moov", traks...)...)
	}
	info, err := probeBytes(file(TrackLimit))
	require.NoError(t, err)
	require.Len(t, info.Tracks, TrackLimit)
	_, err = probeBytes(file(TrackLimit + 1))
	require.ErrorIs(t, err, ErrTooManyTracks)
}

func TestMP4NoMoov(t *testing.T) {
	file := append(ftyp(), box("mdat", make([]byte, 64))...)
	_, err := probeBytes(file)
	require.ErrorIs(t, err, ErrNoTracks)
}
