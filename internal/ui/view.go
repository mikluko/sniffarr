// Package ui renders the web UI: page models, templates, translations and
// static files. Handlers fill the models; ui holds no data of its own.
package ui

import "time"

// Enumerations as the UI knows them. Values are stable keys; labels come from i18n
// ("kind.<v>", "source.<v>", "layer.<v>", "voicemode.<v>", "drop.<v>", "voiceband.<v>").
var (
	Kinds       = []string{"movie", "series", "music", "book", "software", "other", "unknown"}
	Sources     = []string{"disc", "remux", "rip", "web", "other"}
	Layers      = []string{"rules", "title_repeat", "classifier"}
	VoiceModes  = []string{"any", "p50", "explicit"}
	DropReasons = []string{"adult", "software", "course", "script", "filetype", "size"}
	// VoiceBands are the rows of the dubbing table on the data page, in order.
	VoiceBands = []string{"explicit", "likely", "unlikely", "unscored"}
)

type SearchPage struct {
	Base
	Q       string
	Filters Filters
	Results Results
}

type Filters struct {
	Kind      string // "" = all
	VoiceLang string // one of Base.VoiceLangs
	Voice     string // VoiceModes value, "" = any
	Source    string
	Layer     string
	MinSizeGB string // raw input, echoed back
}

type Results struct {
	Fresh   bool   // no query, first page: sorted by last seen, auto-refreshed
	SelfURL string // current URL with all params; used for polling
	Offset  int
	Shown   int
	Total   int64
	PrevURL string // "" = none
	NextURL string // "" = none
	Rows    []Row
}

type Row struct {
	InfoHash   string
	Name       string
	Size       int64
	Kind       string
	Layer      string
	Confidence *float64    // set only when Layer == "classifier"
	Voice      []VoiceCell // aligned with Base.VoiceLangs
	Source     string
	FirstSeen  time.Time
	LastSeen   time.Time
}

type VoiceCell struct {
	Explicit bool
	Prob     *float64 // nil = not scored
}

type TorrentPage struct {
	Base
	BackURL string // search URL to return to, "" = "/"
	Torrent Torrent
}

type Torrent struct {
	InfoHash      string
	Name          string
	Magnet        string
	Size          int64
	FileCount     int // files in the torrent
	FirstSeen     time.Time
	LastSeen      time.Time
	DiscoveredVia string // "announce" | "sample"
	TMDBQuery     string // cleaned title, used for a plain TMDB search link

	Kind   Answer
	Source Answer
	Voice  []VoiceDetail // aligned with Base.VoiceLangs
	Rules  []RuleHit

	Files FileList
}

type Answer struct {
	Value      string
	Layer      string
	Confidence *float64
}

type VoiceDetail struct {
	Lang       string
	Mark       string   // studio or tag that matched, "" = none
	ScriptHint string   // e.g. "cyrillic", "" = none
	Prob       *float64 // classifier probability, nil = not scored
}

type RuleHit struct {
	Rule  string // rule id, e.g. "source.web"
	Match string // matched fragment of the name
}

type FileList struct {
	InfoHash string
	Files    []File
	Stored   int  // files stored in DB (top 20 by size)
	All      bool // Files holds every stored file
}

type File struct {
	Path string
	Size int64
}

type DataPage struct {
	Base
	Stats Stats
}

type Stats struct {
	At           time.Time
	Total        int64
	NewDay       int64
	NewWeek      int64
	DroppedTotal int64

	ByKind   []Count // Key from Kinds
	ByLayer  []Count // Key from Layers
	BySource []Count // Key from Sources
	Voice    []VoiceStats
	Dropped  []Count // Key from DropReasons

	Classifier ClassifierStats

	Daily  Chart // new torrents per day
	Weekly Chart // new torrents per 7 days
}

type Count struct {
	Key string
	N   int64
}

type VoiceStats struct {
	Lang  string
	Bands [4]int64 // indexed like VoiceBands
}

type ClassifierStats struct {
	Pending  int64
	DoneDay  int64
	LimitDay int64
}
