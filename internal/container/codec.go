package container

// codecFamilies maps a video track's Codec, a Matroska CodecID or an MP4
// sample entry type, to the name of its codec family.
var codecFamilies = map[string]string{
	"V_MPEG4/ISO/AVC":  "H.264",
	"V_MPEGH/ISO/HEVC": "HEVC",
	"V_MPEGI/ISO/VVC":  "VVC",
	"V_AV1":            "AV1",
	"V_VP9":            "VP9",
	"V_VP8":            "VP8",
	"V_MPEG4/ISO/SP":   "MPEG-4 Part 2",
	"V_MPEG4/ISO/ASP":  "MPEG-4 Part 2",
	"V_MPEG4/ISO/AP":   "MPEG-4 Part 2",
	"V_MPEG2":          "MPEG-2",
	"V_MPEG1":          "MPEG-1",
	"V_THEORA":         "Theora",
	"V_PRORES":         "ProRes",
	"V_MJPEG":          "Motion JPEG",

	"avc1": "H.264",
	"avc2": "H.264",
	"avc3": "H.264",
	"avc4": "H.264",
	"dva1": "H.264",
	"dvav": "H.264",
	"hvc1": "HEVC",
	"hev1": "HEVC",
	"dvh1": "HEVC",
	"dvhe": "HEVC",
	"vvc1": "VVC",
	"vvi1": "VVC",
	"av01": "AV1",
	"dav1": "AV1",
	"vp09": "VP9",
	"vp08": "VP8",
	"mp4v": "MPEG-4 Part 2",
	"mp2v": "MPEG-2",
	"vc-1": "VC-1",
	"s263": "H.263",
	"h263": "H.263",
	"apch": "ProRes",
	"apcn": "ProRes",
	"apcs": "ProRes",
	"apco": "ProRes",
	"ap4h": "ProRes",
	"ap4x": "ProRes",
	"jpeg": "Motion JPEG",
	"mjpa": "Motion JPEG",
	"mjpb": "Motion JPEG",
}

// CodecFamily names the codec family of a video track's Codec, such as
// "HEVC" for both "V_MPEGH/ISO/HEVC" and "hvc1"; a codec it does not know is
// returned as it is.
func CodecFamily(codec string) string {
	if f, ok := codecFamilies[codec]; ok {
		return f
	}
	return codec
}

// pictureCodecs are the video track codecs of still or motion images, which
// carry cover art rather than the film.
var pictureCodecs = map[string]bool{
	"V_MJPEG": true,
	"jpeg":    true,
	"mjpa":    true,
	"mjpb":    true,
	"png ":    true,
	"gif ":    true,
	"tiff":    true,
	"bmp ":    true,
}

// Picture reports whether a video track of the codec carries images, such as
// cover art, rather than the film.
func Picture(codec string) bool {
	return pictureCodecs[codec]
}
