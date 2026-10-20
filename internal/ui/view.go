// Package ui renders the web UI: page models, templates, translations and
// static files. Handlers fill the models; ui holds no data of its own.
package ui

import "time"

// Enumerations as the UI knows them. Values are stable keys; labels come from i18n
// ("kind.<v>", "source.<v>", "layer.<v>", "voicemode.<v>", "drop.<v>", "voiceband.<v>",
// "topkind.<v>", "win.<v>", "sort.<v>").
var (
	Kinds       = []string{"movie", "series", "music", "book", "software", "other", "unknown"}
	Sources     = []string{"disc", "remux", "rip", "web", "other"}
	Layers      = []string{"rules", "title_repeat", "classifier"}
	VoiceModes  = []string{"any", "p50", "explicit"}
	DropReasons = []string{"adult", "software", "book", "course", "script", "filetype", "size"}
	// VoiceBands are the rows of the dubbing table on the data page, in order.
	VoiceBands = []string{"explicit", "likely", "unlikely", "unscored"}
	// TopKinds are the tabs of the top page.
	TopKinds = []string{"movie", "series", "music"}
	// Windows are the popularity windows.
	Windows = []string{"24h", "7d", "30d"}
	// Sorts are the orders of search results.
	Sorts = []string{"peers", "last_seen", "size"}
)

// PeerCounts are unique peers seen in each window.
type PeerCounts struct {
	Day, Week, Month int64
}

// ---------- TMDB ----------

// TMDB match states. A nil *TMDBWork renders like TMDBUnchecked.
const (
	TMDBMatched   = "matched"   // matched; PosterPath may still be empty
	TMDBUnmatched = "unmatched" // checked, nothing suitable found
	TMDBUnchecked = "unchecked" // not checked yet
)

// TMDBWork is what the UI shows from TMDB for a title key. Text fields come
// already localized for Base.Lang (TMDB translations); OriginalTitle is TMDB's
// original_title / original_name.
type TMDBWork struct {
	State     string    // TMDBMatched | TMDBUnmatched | TMDBUnchecked
	CheckedAt time.Time // last lookup, zero = never

	// Set when State == TMDBMatched.
	ID            int64
	Type          string // "movie" | "tv"
	IMDbID        string // "tt15239678", "" = unknown
	Title         string
	OriginalTitle string
	Year          int    // release year; first air year for tv
	PosterPath    string // "/1pdfLvkbY9ohJlCjQH2CZjjYVvJ.jpg", "" = no poster
	Overview      string
	Genres        []string // localized names
	Rating        float64  // vote_average, 0–10
	Votes         int      // vote_count
	Runtime       int      // minutes; movie runtime, typical episode runtime for tv
	Status        string   // tv: TMDB status, keyed in i18n as tvstatus.<value>
	Seasons       int      // tv: number_of_seasons
	Countries     []string // ISO 3166-1 alpha-2, origin/production
	Language      string   // original_language, ISO 639-1

	// How the match was made.
	Method     string  // "search" (unique search hit) | "classifier" (picked among candidates)
	Confidence float64 // 0–1
}

// ---------- home (GET / without query or filters) ----------

type HomePage struct {
	Base
	Summary Summary
}

type Summary struct {
	Total   int64 // torrents stored
	NewDay  int64 // new in the last 24 h
	Pending int64 // classifier queue
}

// ---------- search results (GET / with q or any filter) ----------

type SearchPage struct {
	Base
	Q       string
	Filters Filters
	Works   []WorkHit // works whose title matches q, best first; at most a handful
	Results Results
}

type Filters struct {
	Kind      string // "" = all
	VoiceLang string // one of Base.VoiceLangs
	Voice     string // VoiceModes value, "" = any
	Source    string
	Layer     string
	MinSizeGB string // raw input, echoed back
	Sort      string // Sorts value, "" = peers
}

// WorkHit is a work matched by the search query, shown above the torrents.
type WorkHit struct {
	Ref      WorkRef
	Peers    int64 // in the 7-day window
	Releases int
	TMDB     *TMDBWork
}

type Results struct {
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
	Peers      int64 // unique peers, 7 days
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

// ---------- top 100 (GET /top) ----------

type TopPage struct {
	Base
	Kind      string // TopKinds value
	Window    string // Windows value
	VoiceMode string // VoiceModes value; filters works by their best release
	Computed  time.Time
	Limit     int  // 100
	All       bool // Works holds all Limit entries; otherwise the first 25
	Works     []Work
}

// WorkRef names a work: a movie (title + year), a series season (title + season),
// an album (artist + album), a book (author + title), a program (name + version).
type WorkRef struct {
	ID    string // opaque; safe in a URL path segment and in an HTML id
	Kind  string
	Title string // "Дюна: Часть вторая", "Сёгун", "Pink Floyd"
	Sub   string // "2024", "S01", "The Dark Side Of The Moon"
	Rank  int    // place in the top of its kind for the 7-day window, 0 = outside
}

type Work struct {
	Ref       WorkRef
	Rank      int // place in this top (window and filter)
	Peers     int64
	Delta     *float64 // change against the previous window, nil = was absent ("new")
	Releases  int
	Voice     []VoiceCell // best release per language, aligned with Base.VoiceLangs
	Sources   []string    // Sources values present among releases
	FirstSeen time.Time   // earliest release
	Open      bool        // Items is filled and shown
	Items     []Release   // releases by peers, filled only when Open
	TMDB      *TMDBWork
}

type Release struct {
	InfoHash  string
	Name      string
	Peers     int64
	Size      int64
	Voice     []VoiceCell
	Source    string
	FirstSeen time.Time
}

// ---------- torrent card ----------

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
	TMDBQuery     string // cleaned title, used for TMDB/IMDb search links when not matched
	Peers         PeerCounts
	TMDB          *TMDBWork // match of the title key; shown only when Work != nil

	Work       *WorkRef // nil = not grouped into a work
	WorkRels   int      // releases of the work
	RankInWork int      // place of this torrent among them by 7-day peers

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

// ---------- data ----------

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
