package container

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodecFamily(t *testing.T) {
	for _, tc := range []struct{ codec, want string }{
		{"V_MPEG4/ISO/AVC", "H.264"},
		{"avc1", "H.264"},
		{"avc3", "H.264"},
		{"V_MPEGH/ISO/HEVC", "HEVC"},
		{"hvc1", "HEVC"},
		{"hev1", "HEVC"},
		{"dvh1", "HEVC"},
		{"dvhe", "HEVC"},
		{"V_AV1", "AV1"},
		{"av01", "AV1"},
		{"V_VP9", "VP9"},
		{"vp09", "VP9"},
		{"V_MPEG4/ISO/ASP", "MPEG-4 Part 2"},
		{"mp4v", "MPEG-4 Part 2"},
		{"V_MPEG2", "MPEG-2"},
		{"vc-1", "VC-1"},
		{"V_MS/VFW/FOURCC", "V_MS/VFW/FOURCC"},
		{"xyz1", "xyz1"},
		{"", ""},
	} {
		require.Equal(t, tc.want, CodecFamily(tc.codec), tc.codec)
	}
}

func TestPicture(t *testing.T) {
	for _, tc := range []struct {
		codec string
		want  bool
	}{
		{"V_MJPEG", true},
		{"jpeg", true},
		{"png ", true},
		{"V_MPEGH/ISO/HEVC", false},
		{"avc1", false},
		{"", false},
	} {
		require.Equal(t, tc.want, Picture(tc.codec), tc.codec)
	}
}
