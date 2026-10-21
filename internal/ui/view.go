package ui

import "time"

// Enumerations as the UI knows them. Values are stable keys; labels come from i18n
// ("kind.<v>", "source.<v>", "layer.<v>", "voicemode.<v>", "drop.<v>", "voiceband.<v>",
// "topkind.<v>", "win.<v>", "sort.<v>").
var (
	// Kinds a torrent can be classified as. Only movies and series are titles;
	// "other" and "unknown" remain visible on the torrents tab.
	Kinds       = []string{"movie", "series", "other", "unknown"}
	Sources     = []string{"disc", "remux", "rip", "web", "other"}
	Layers      = []string{"rules", "title_repeat", "classifier"}
	VoiceModes  = []string{"any", "p50", "explicit"}
	DropReasons = []string{"adult", "software", "book", "course", "script", "filetype", "size"}
	// VoiceBands are the rows of the dubbing table on the data page, in order.
	VoiceBands = []string{"explicit", "likely", "unlikely", "unscored"}
	// TopKinds are the kinds of titles: tabs of the top, filter of the titles tab.
	TopKinds = []string{"movie", "series"}
	// Windows are the popularity windows.
	Windows = []string{"24h", "7d", "30d"}
	// Sorts are the orders of the torrents tab of search.
	Sorts = []string{"peers", "last_seen", "size"}
	// ReleaseSorts are the orders of a title's release table.
	ReleaseSorts = []string{"peers", "first_seen", "size"}
	// Qualities of a release, by resolution; "" = unknown.
	Qualities = []string{"2160p", "1080p", "720p", "sd"}
	// ReleaseSources are the sources offered by the release table filter.
	ReleaseSources = []string{"web", "rip", "remux", "disc"}
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

// ---------- titles ----------

// WorkSummary is one movie or series as the list views show it: TMDB data plus
// our numbers. A title found only in TMDB has Releases == 0.
type WorkSummary struct {
	ID       string    // URL id, see WorkID
	Kind     string    // "movie" | "series"
	TMDB     *TMDBWork // always State == TMDBMatched here
	Releases int       // our releases, all spellings of the title merged
	Peers    int64     // unique peers, 7 days
	Rank     int       // place in the top of its kind, 7 days; 0 = outside
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

// ---------- search (GET / with q or any filter) ----------

// SearchPage lists movies and series only; raw torrents live on TorrentsPage.
type SearchPage struct {
	Base
	Q      string
	Titles WorkResults
}

// WorkResults is the list of titles. Items come ordered: ours by 7-day peers,
// then TMDB-only by TMDB popularity.
type WorkResults struct {
	Kind     string // "" = movies and series, else a TopKinds value
	Items    []WorkSummary
	Ours     int // items with Releases > 0
	TMDBOnly int // items with Releases == 0
}

// ---------- torrents (GET /torrents) ----------

// TorrentsPage searches and filters torrents by recognized features, matched to
// a title or not. Fragment "torrents".
type TorrentsPage struct {
	Base
	Q       string
	Filters Filters
	Results Results
}

type Filters struct {
	Kind      string // "" = all, else a Kinds value
	Matched   string // "" = all, "yes" = matched to a title, "no" = not matched
	Quality   string // Qualities value, "" = any
	VoiceLang string // one of Base.VoiceLangs; default DefaultVoiceLang
	Voice     string // VoiceModes value for VoiceLang, "" = any
	Source    string
	Layer     string
	MinSizeGB string // raw input, echoed back
	Sort      string // Sorts value, "" = peers
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
	Work       *WorkSummary // the title; nil = not matched (placeholder instead of the poster)
	Peers      int64        // unique peers, 7 days
	Size       int64
	Kind       string
	Layer      string
	Confidence *float64 // set only when Layer == "classifier"
	Quality    string   // Qualities value, "" = unknown
	Audio      []Audio  // one entry per configured language that has any signal
	Source     string
	FirstSeen  time.Time
	LastSeen   time.Time
}

// Audio is the dub signal of one release for one configured language.
type Audio struct {
	Lang        string   // configured dub language, ISO 639-1
	Probability *float64 // classifier, 0–1; nil = not scored
	Marker      bool     // explicit marker found in the name
	Mark        string   // the marker: a word or a studio ("Дубляж", "MVO", "LostFilm", "seleZen"); "" if unnamed
	Script      string   // optional script hint for the torrent card, e.g. "cyrillic"
}

// Likely reports a probability of at least 0.5.
func (a Audio) Likely() bool { return a.Probability != nil && *a.Probability >= 0.5 }

// WorkAudio summarizes one language over all releases of a title.
type WorkAudio struct {
	Lang   string
	Marked int      // releases with an explicit marker
	Marks  []string // distinct marks among them, most frequent first
	Likely int      // releases without a marker but with probability >= 0.5
}

// ---------- top 100 (GET /top) ----------

type TopPage struct {
	Base
	Kind      string // TopKinds value
	Window    string // Windows value
	VoiceLang string // language the voice filter applies to; "" when none configured
	VoiceMode string // VoiceModes value. With "explicit" or "p50" Peers count only
	//                  matching releases: explicit = Marker; p50 = Marker or Probability >= 0.5
	Computed time.Time
	Limit    int  // 100
	All      bool // Works holds all Limit entries; otherwise the first 25
	Works    []Work
}

// Work is one row of the top. The row links to the title page.
type Work struct {
	WorkSummary             // Rank here is the place in this top (window and filter)
	Delta       *float64    // change against the previous window, nil = was absent ("new")
	Audio       []WorkAudio // one entry per configured language, in Base.VoiceLangs order
	Sources     []string    // Sources values present among releases
	FirstSeen   time.Time   // earliest release
}

// ---------- title page (GET /w/{id}) ----------

// WorkPage is the page of one movie or series: TMDB on top, our releases below.
type WorkPage struct {
	Base
	Work     WorkSummary
	Ranks    [3]int     // place in the top of its kind for 24h, 7d, 30d; 0 = outside
	Peers    PeerCounts // all releases
	Audio    []WorkAudio
	Releases ReleaseTable
}

// ReleaseTable is the filterable table of a title's releases; it is also the
// htmx fragment "releases".
type ReleaseTable struct {
	WorkID  string
	Kind    string // "series" shows season and episode
	Filters ReleaseFilters
	Seasons []int // seasons present among releases, for the season filter
	Total   int   // all releases of the title
	Matched int   // releases passing Filters
	Offset  int
	Shown   int
	PrevURL string
	NextURL string
	Rows    []Release
}

type ReleaseFilters struct {
	Quality   string // Qualities value, "" = any
	Source    string // ReleaseSources value, "" = any
	MinGB     string // raw inputs, echoed back
	MaxGB     string
	VoiceLang string // one of Base.VoiceLangs
	Voice     string // VoiceModes value, "" = any
	Season    string // "" = all, else a number; series only
	Episode   string // "" = all, else a number; series only
	Sort      string // ReleaseSorts value, "" = peers
}

type Release struct {
	InfoHash  string
	Name      string
	Peers     int64 // unique peers, 7 days
	Size      int64
	Quality   string // Qualities value, "" = unknown
	Source    string // Sources value
	Season    int    // series: 0 = unknown
	Episode   int    // series: 0 = whole season or unknown
	Audio     []Audio
	FirstSeen time.Time
}

// ---------- torrent card ----------

type TorrentPage struct {
	Base
	BackURL string // URL to return to, "" = the title page or the top
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

	// Work is the movie or series this torrent belongs to; nil when the title
	// is not matched to TMDB. TMDB then carries the state (unmatched / unchecked).
	Work       *WorkSummary
	TMDB       *TMDBWork
	RankInWork int // place of this torrent among the title's releases by 7-day peers

	Kind   Answer
	Source Answer
	Audio  []Audio // card shows every configured language; missing ones render as "—"
	Rules  []RuleHit

	Files FileList
}

type Answer struct {
	Value      string
	Layer      string
	Confidence *float64
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
