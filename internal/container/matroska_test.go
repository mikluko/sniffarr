package container

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

const clusterBodySize = 1 << 20

func idBytes(id uint32) []byte {
	b := binary.BigEndian.AppendUint32(nil, id)
	return bytes.TrimLeft(b, "\x00")
}

// el encodes an EBML element with an 8-byte size.
func el(id uint32, body ...[]byte) []byte {
	data := bytes.Join(body, nil)
	out := idBytes(id)
	out = append(out, 0x01)
	out = append(out, binary.BigEndian.AppendUint64(nil, uint64(len(data)))[1:]...)
	return append(out, data...)
}

// elUnknown encodes an EBML element of unknown size.
func elUnknown(id uint32, body ...[]byte) []byte {
	out := append(idBytes(id), 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
	return append(out, bytes.Join(body, nil)...)
}

func elUint(id uint32, v uint64) []byte {
	b := bytes.TrimLeft(binary.BigEndian.AppendUint64(nil, v), "\x00")
	if len(b) == 0 {
		b = []byte{0}
	}
	return el(id, b)
}

// elUint8 encodes an unsigned integer in 8 bytes, so its length does not depend on v.
func elUint8(id uint32, v uint64) []byte {
	return el(id, binary.BigEndian.AppendUint64(nil, v))
}

func elStr(id uint32, s string) []byte {
	return el(id, []byte(s))
}

func ebmlHead(docType string) []byte {
	return el(idEBML, elUint(0x4286, 1), elUint(0x42F7, 1), elStr(idDocType, docType), elUint(0x4287, 4), elUint(0x4285, 2))
}

// segmentData is the offset of the body of a Segment that follows head.
func segmentData(head []byte) int {
	return len(head) + 4 + 8
}

func seekHead(id uint32, pos int) []byte {
	return el(idSeekHead, el(idSeek, el(idSeekID, idBytes(id)), elUint8(idSeekPosition, uint64(pos))))
}

func cluster() []byte {
	return el(idCluster, make([]byte, clusterBodySize))
}

func mkvTrackList() []byte {
	return el(idTracks,
		el(0xBF, []byte{1, 2, 3, 4}),
		el(idTrackEntry,
			elUint(0xD7, 1), elUint(idTrackType, 1), elStr(idCodecID, "V_MPEGH/ISO/HEVC"), elStr(idLanguage, "und"),
			el(idVideo, elUint(idPixelWidth, 3840), elUint(idPixelHeight, 2160),
				el(idColour, elUint(idTransferCharacteristics, 16))),
			el(idBlockAdditionMapping, elUint(0x41F0, 4), elStr(0x41A4, "Dolby Vision configuration"), elUint(idBlockAddIDType, 0x64766343), el(0x41ED, make([]byte, 24))),
		),
		el(idTrackEntry,
			elUint(0xD7, 2), elUint(idTrackType, 1), elStr(idCodecID, "V_MPEGH/ISO/HEVC"), elUint(idFlagDefault, 0),
			el(idVideo, elUint(idPixelWidth, 1920), elUint(idPixelHeight, 1080),
				el(idColour, elUint(idTransferCharacteristics, 16))),
		),
		el(idTrackEntry,
			elUint(0xD7, 3), elUint(idTrackType, 1), elStr(idCodecID, "V_MPEG4/ISO/AVC"), elUint(idFlagDefault, 0),
			el(idVideo, elUint(idPixelWidth, 1280), elUint(idPixelHeight, 720),
				el(idColour, el(idMasteringMetadata, el(0x55D9, []byte{0x47, 0x43, 0x50, 0x00})))),
		),
		el(idTrackEntry,
			elUint(0xD7, 4), elUint(idTrackType, 1), elStr(idCodecID, "V_AV1"), elUint(idFlagDefault, 0),
			el(idVideo, elUint(idPixelWidth, 1920), elUint(idPixelHeight, 1080),
				el(idColour, elUint(idTransferCharacteristics, 18))),
		),
		el(idTrackEntry,
			elUint(0xD7, 5), elUint(idTrackType, 2), elStr(idCodecID, "A_EAC3"), elStr(idLanguage, "rus"), elStr(idName, "Дубляж"),
			el(idAudio, el(0xB5, binary.BigEndian.AppendUint64(nil, 0x40E7700000000000)), elUint(idChannels, 6)),
		),
		el(0xEC, make([]byte, 7)),
		el(idTrackEntry,
			elUint(0xD7, 6), elUint(idTrackType, 2), elStr(idCodecID, "A_AC3"), elStr(idLanguage, "eng"), elStr(idName, "Original"), elUint(idFlagDefault, 0),
			el(idAudio, elUint(idChannels, 6)),
		),
		el(idTrackEntry,
			elUint(0xD7, 7), elUint(idTrackType, 17), elStr(idCodecID, "S_TEXT/UTF8"), elStr(idLanguage, "rus"), elStr(idName, "Надписи"), elUint(idFlagForced, 1),
		),
		el(idTrackEntry, elUint(0xD7, 8), elUint(idTrackType, 0x21), elStr(idCodecID, "D_WEBVTT/METADATA")),
	)
}

var mkvTrackListWant = []Track{
	{Kind: Video, Language: "und", Codec: "V_MPEGH/ISO/HEVC", Width: 3840, Height: 2160, HDR: "DV", Default: true},
	{Kind: Video, Language: "eng", Codec: "V_MPEGH/ISO/HEVC", Width: 1920, Height: 1080, HDR: "HDR10"},
	{Kind: Video, Language: "eng", Codec: "V_MPEG4/ISO/AVC", Width: 1280, Height: 720, HDR: "HDR10"},
	{Kind: Video, Language: "eng", Codec: "V_AV1", Width: 1920, Height: 1080, HDR: "HLG"},
	{Kind: Audio, Language: "rus", Name: "Дубляж", Codec: "A_EAC3", Channels: 6, Default: true},
	{Kind: Audio, Language: "eng", Name: "Original", Codec: "A_AC3", Channels: 6},
	{Kind: Subtitle, Language: "rus", Name: "Надписи", Codec: "S_TEXT/UTF8", Default: true, Forced: true},
}

// mkvTracksBeforeCluster returns a file whose Tracks precede its Cluster, and
// the offset where Tracks ends.
func mkvTracksBeforeCluster() ([]byte, int) {
	head := ebmlHead("matroska")
	void := el(0xEC, make([]byte, 10))
	info := el(0x1549A966, elUint(0x2AD7B1, 1000000))
	tracks := mkvTrackList()
	file := append(head, el(idSegment, void, info, tracks, cluster())...)
	return file, segmentData(head) + len(void) + len(info) + len(tracks)
}

// mkvTracksAfterCluster returns a file whose Tracks follow its Cluster and are
// reached through SeekHead, and the range of the Cluster body.
func mkvTracksAfterCluster() ([]byte, [2]int64) {
	head := ebmlHead("matroska")
	sh := seekHead(idTracks, 0)
	cl := cluster()
	sh = seekHead(idTracks, len(sh)+len(cl))
	file := append(head, el(idSegment, sh, cl, mkvTrackList())...)
	bodyStart := int64(segmentData(head) + len(sh) + len(cl) - clusterBodySize)
	return file, [2]int64{bodyStart, bodyStart + clusterBodySize}
}

func mkvUnknownSegment() []byte {
	return append(ebmlHead("matroska"), elUnknown(idSegment, mkvTrackList(), elUnknown(idCluster, make([]byte, 64)))...)
}

func TestMatroskaTracksBeforeCluster(t *testing.T) {
	file, tracksEnd := mkvTracksBeforeCluster()
	r := &countingReader{r: bytes.NewReader(file)}
	info, err := Probe(r, int64(len(file)))
	require.NoError(t, err)
	require.Equal(t, Matroska, info.Format)
	require.Equal(t, mkvTrackListWant, info.Tracks)
	r.requireUntouched(t, int64(tracksEnd)+12, int64(len(file)))
}

func TestMatroskaUnknownSizeSegment(t *testing.T) {
	info, err := probeBytes(mkvUnknownSegment())
	require.NoError(t, err)
	require.Equal(t, mkvTrackListWant, info.Tracks)
}

func TestMatroskaSeekHeadAfterCluster(t *testing.T) {
	file, body := mkvTracksAfterCluster()
	r := &countingReader{r: bytes.NewReader(file)}
	info, err := Probe(r, int64(len(file)))
	require.NoError(t, err)
	require.Equal(t, mkvTrackListWant, info.Tracks)
	r.requireUntouched(t, body[0], body[1])
}

func TestMatroskaSecondSeekHead(t *testing.T) {
	head := ebmlHead("matroska")
	tracks := mkvTrackList()
	cl := cluster()
	first := seekHead(idSeekHead, 0)
	second := seekHead(idTracks, len(first)+len(cl))
	first = seekHead(idSeekHead, len(first)+len(cl)+len(tracks))
	file := append(head, el(idSegment, first, cl, tracks, second)...)

	r := &countingReader{r: bytes.NewReader(file)}
	info, err := Probe(r, int64(len(file)))
	require.NoError(t, err)
	require.Equal(t, mkvTrackListWant, info.Tracks)
	bodyStart := int64(segmentData(head) + len(first) + len(cl) - clusterBodySize)
	r.requireUntouched(t, bodyStart, bodyStart+clusterBodySize)
}

func TestMatroskaSpecDefaults(t *testing.T) {
	file := append(ebmlHead("matroska"), el(idSegment, el(idTracks,
		el(idTrackEntry, elUint(0xD7, 1), elUint(idTrackType, 2), elStr(idCodecID, "A_OPUS"), elStr(idLanguageBCP47, "pt-BR")),
	))...)
	info, err := probeBytes(file)
	require.NoError(t, err)
	require.Equal(t, []Track{{Kind: Audio, Language: "eng", LanguageBCP47: "pt-BR", Codec: "A_OPUS", Channels: 1, Default: true}}, info.Tracks)
}

func TestWebM(t *testing.T) {
	file := append(ebmlHead("webm"), el(idSegment, el(idTracks,
		el(idTrackEntry, elUint(0xD7, 1), elUint(idTrackType, 1), elStr(idCodecID, "V_VP9"), el(idVideo, elUint(idPixelWidth, 640), elUint(idPixelHeight, 360))),
		el(idTrackEntry, elUint(0xD7, 2), elUint(idTrackType, 2), elStr(idCodecID, "A_OPUS"), el(idAudio, elUint(idChannels, 2))),
	))...)
	info, err := probeBytes(file)
	require.NoError(t, err)
	require.Equal(t, Matroska, info.Format)
	require.Equal(t, []Track{
		{Kind: Video, Language: "eng", Codec: "V_VP9", Width: 640, Height: 360, Default: true},
		{Kind: Audio, Language: "eng", Codec: "A_OPUS", Channels: 2, Default: true},
	}, info.Tracks)
}

func TestMatroskaOtherDocType(t *testing.T) {
	file := append(ebmlHead("divx"), el(idSegment, mkvTrackList())...)
	_, err := probeBytes(file)
	require.ErrorIs(t, err, ErrUnknownFormat)
}

func TestMatroskaNoTracks(t *testing.T) {
	file := append(ebmlHead("matroska"), el(idSegment, el(0x1549A966, elUint(0x2AD7B1, 1000000)), cluster())...)
	_, err := probeBytes(file)
	require.ErrorIs(t, err, ErrNoTracks)
}
