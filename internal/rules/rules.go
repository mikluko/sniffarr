// Package rules is the first-level filter for submitted torrents: it rejects
// by name and file extensions what is never worth classifying, and detects
// explicit markers of Russian audio.
package rules

import (
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/mikluko/sniffarr/internal/rot13"
)

// MinSize is the smallest torrent, in bytes, that Check accepts.
const MinSize = 50 << 20

// Reason is why Check rejected a torrent; the empty Reason accepts it.
type Reason string

const (
	Accept   Reason = ""
	Adult    Reason = "adult"
	Software Reason = "software"
	Book     Reason = "book"
	Course   Reason = "course"
	Small    Reason = "small"
	NoMedia  Reason = "nomedia"
)

// File is one file of a torrent.
type File struct {
	Path string
	Size int64
}

type category int

const (
	other category = iota
	video
	audio
	book
	software
	discImage
)

var extensions = map[string]category{"iso": discImage}

func init() {
	for c, exts := range map[category]string{
		video:    "mkv mp4 avi m2ts ts wmv mov m4v vob webm flv",
		audio:    "flac mp3 m4a ogg opus wav ape dsf wv aac alac",
		book:     "pdf epub fb2 djvu cbr cbz mobi azw3 doc docx txt",
		software: "exe apk dmg msi pkg appimage deb rpm bin cab dll xapk nsp xci",
	} {
		for e := range strings.FieldsSeq(exts) {
			extensions[e] = c
		}
	}
}

var (
	adultWords = words(rot13.String(`kkk|cbea|zvys|nany|sp2|wni|baylsnaf|uragnv|oenmmref|oynpxrq|ghful|ivkra|fklcea|onatoebf|ernyvglxvatf|cbeaevcf|kik`))
	videoDisc  = words(`dvd\d*|blu-?ray|bd(?:25|50|66|100)?|bdmv|video_ts|uhd|remux|720p|1080[pi]|2160p`)
	adultCode  = regexp.MustCompile(`\b[A-Z]{2,6}-\d{3,5}\b`)
	warez      = words(`repack|fitgirl|dodi|crack(?:ed)?|portable|keygen|elamigos|codex|skidrow|gog|v\d+\.\d+`)
	course     = words(`udemy|course|tutorial|masterclass|skillshare|lynda|pluralsight`)
	russian    = words(`rus|дубляж|дублированный|многоголосый|двухголосый|одноголосый|` + studios)
)

// studios are Russian dubbing and voice-over studios and release groups.
const studios = `lostfilm|newstudio|jaskier|hdrezka|кубик в кубе|alexfilm|baibako|coldfilm|tvshows|novamedia|amedia|кураж-бамбей|red head sound|rhs|гоблин|anilibria|anidub|shiza|anistar|dream cast|selezen|exkinoray|scarabey|hellywood|generalfilm`

// words matches any of the alternatives as a whole word, case-insensitively.
// Underscores and dots separate words, as they do in release names.
func words(alternatives string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:` + alternatives + `)(?:$|[^\p{L}\p{N}])`)
}

// Check returns why the torrent should be rejected, or Accept. A torrent
// without files is judged as a single file named name of the given size. An
// .iso counts as video when the torrent or file name names a DVD or Blu-ray
// copy, and as software otherwise.
func Check(name string, size int64, files []File) Reason {
	if len(files) == 0 {
		files = []File{{Path: name, Size: size}}
	}
	if isAdult(name) || anyPath(files, isAdult) {
		return Adult
	}
	if warez.MatchString(name) {
		return Software
	}
	if course.MatchString(name) {
		return Course
	}
	var bytes [software + 1]int64
	hasMedia := false
	for _, f := range files {
		c := categorize(f.Path)
		if c == discImage {
			c = software
			if videoDisc.MatchString(name) || videoDisc.MatchString(f.Path) {
				c = video
			}
		}
		bytes[c] += f.Size
		hasMedia = hasMedia || c == video || c == audio
	}
	media := bytes[video] + bytes[audio]
	switch {
	case bytes[software] > media:
		return Software
	case bytes[book] > media:
		return Book
	case !hasMedia:
		return NoMedia
	case size < MinSize:
		return Small
	}
	return Accept
}

// RussianMarker reports whether the name or a file path explicitly names a
// Russian audio track: the word "rus", a Russian dubbing term, or a Russian
// dubbing studio.
func RussianMarker(name string, files []File) bool {
	return russian.MatchString(name) || anyPath(files, russian.MatchString)
}

// Cyrillic reports whether the name or a file path has a Cyrillic letter.
func Cyrillic(name string, files []File) bool {
	return cyrillic(name) || anyPath(files, cyrillic)
}

func cyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// Studios lists the studios RussianMarker recognizes.
func Studios() []string {
	return strings.Split(studios, "|")
}

func isAdult(s string) bool {
	return adultWords.MatchString(s) || adultCode.MatchString(s)
}

func anyPath(files []File, match func(string) bool) bool {
	for _, f := range files {
		if match(f.Path) {
			return true
		}
	}
	return false
}

func categorize(p string) category {
	return extensions[strings.ToLower(strings.TrimPrefix(path.Ext(p), "."))]
}
