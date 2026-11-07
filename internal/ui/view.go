package ui

import (
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

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
	PrevRank    int         // place in the previous window, 0 = had no peers then ("new")
	Audio       []WorkAudio // one entry per configured language, in Base.VoiceLangs order
	Sources     []string    // Sources values present among releases
	FirstSeen   time.Time   // earliest release
}

// ---------- title page (GET /movie/{id}, GET /tv/{id}) ----------

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
	Path    string // the title page, see WorkPath
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

// Requested answers a card's button: Action, "peers" or "probe", was asked
// for Releases releases. It is the htmx fragment "act".
type Requested struct {
	Base
	Action   string
	Releases int
}

type TorrentPage struct {
	Base
	BackURL string // URL to return to, "" = the title page or the top
	Torrent Torrent
}

type Torrent struct {
	InfoHash string
	// JournalOnly marks an infohash the store keeps no torrent of: only
	// InfoHash and Journal are set.
	JournalOnly   bool
	Name          string
	Magnet        string
	Size          int64
	FileCount     int // files in the torrent
	FirstSeen     time.Time
	LastSeen      time.Time
	DiscoveredVia string // "announce" | "sample" | "arr" | "feed"
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

	// Journal is the newest events of the torrent's life, oldest first.
	Journal []JournalEntry

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

// JournalEntry is one event of a torrent's life; labels come from i18n
// ("journal.kind.<kind>", "journal.outcome.<kind>.<outcome>").
type JournalEntry struct {
	At      time.Time
	Kind    string
	Outcome string
	// Detail is the event's particulars as the API's JSON object decodes:
	// numbers are float64, a time is its Unix seconds.
	Detail map[string]any
}

// DetailPair is one particular of an entry as the card writes it.
type DetailPair struct {
	Key   string
	Value string
	// Href is the page Value links to, "" for none.
	Href string
}

// journalValues maps a detail key, or an entry's kind and a key joined by
// ".", to the i18n prefix of the key's values; the kind's mapping wins.
var journalValues = map[string]string{
	"kind":              "kind.",
	"kind_layer":        "journal.layer.",
	"trigger":           "journal.trigger.",
	"discovery":         "via.",
	"reason":            "journal.reason.",
	"type":              "journal.type.",
	"sighted.from":      "via.",
	"sighted.to":        "via.",
	"classified.source": "source.",
	"arr.source":        "journal.arrsource.",
	"arr.app":           "journal.app.",
	"matched.layer":     "journal.matchlayer.",
	"probe_wanted.t":    "journal.searchtype.",
}

// journalAliases maps a message key a value would take to the key of the
// message the card already has for it: the classifier's names for a video
// source to the card's own.
var journalAliases = map[string]string{
	"source.disc_image": "source.disc",
	"source.encode":     "source.rip",
}

// searchIDProviders names the providers of a search's ID parameter.
var searchIDProviders = map[string]string{"tmdbid": "TMDB", "imdbid": "IMDb", "tvdbid": "TVDB"}

// JournalDetail returns the entry's particulars in the order of their keys:
// a key by "journal.key.<kind>.<key>" or else "journal.key.<key>"; a moment
// by TimeFull, a size by Size, a probability in audio as a percentage, a
// boolean by "journal.bool.<value>", and a value journalValues maps by its
// message. A track reads as its kind, language and codec; the leases of an
// attempt as how many times it was leased; a search's ID as its provider and
// the ID; the infohash a classification took its kind from as a link to its
// page. A key or a string value without a message is written as it is.
func (b Base) JournalDetail(e JournalEntry) []DetailPair {
	out := make([]DetailPair, 0, len(e.Detail))
	for _, k := range slices.Sorted(maps.Keys(e.Detail)) {
		v := e.Detail[k]
		p := DetailPair{Key: b.journalKey(e.Kind, k), Value: b.journalValue(e.Kind, k, v)}
		if s, ok := v.(string); ok && e.Kind == "classified" && k == "from" && isInfohash(s) {
			p.Value, p.Href = s[:8], "/t/"+s
		}
		out = append(out, p)
	}
	return out
}

// isInfohash reports whether s is the hex form of an infohash.
func isInfohash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 20
}

// journalKey is the label of the detail key of an entry of the kind.
func (b Base) journalKey(kind, key string) string {
	for _, m := range []string{"journal.key." + kind + "." + key, "journal.key." + key} {
		if b.has(m) {
			return b.T(m)
		}
	}
	return key
}

// journalValue formats the decoded JSON value of the key of an entry of the
// kind, as JournalDetail does; a list by commas, an object as "key value"
// by commas, a number with the locale's decimal mark.
func (b Base) journalValue(kind, key string, v any) string {
	switch x := v.(type) {
	case string:
		if kind == "probe_wanted" && key == "id" {
			return b.searchID(x)
		}
		prefix, ok := journalValues[kind+"."+key]
		if !ok {
			prefix, ok = journalValues[key]
		}
		if m := prefix + x; ok {
			if a, aliased := journalAliases[m]; aliased {
				m = a
			}
			if b.has(m) {
				return b.T(m)
			}
		}
		return x
	case float64:
		switch {
		case key == "leases" && x == math.Trunc(x):
			return b.times(int64(x))
		case detailTime(key):
			return b.TimeFull(time.Unix(int64(x), 0))
		case detailSize(key):
			return b.Size(int64(x))
		case x == math.Trunc(x) && math.Abs(x) < 1<<53:
			return strconv.FormatInt(int64(x), 10)
		}
		return b.dec(x, -1)
	case bool:
		return b.T("journal.bool." + strconv.FormatBool(x))
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if s, ok := e.(string); ok && key == "tracks" {
				parts[i] = b.track(s)
				continue
			}
			parts[i] = b.journalValue(kind, key, e)
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		parts := make([]string, 0, len(x))
		for _, k := range slices.Sorted(maps.Keys(x)) {
			if p, ok := x[k].(float64); ok && key == "audio" {
				parts = append(parts, b.LangName(k)+" "+strconv.FormatFloat(math.Round(p*100), 'f', 0, 64)+"%")
				continue
			}
			parts = append(parts, k+" "+b.journalValue(kind, k, x[k]))
		}
		return strings.Join(parts, ", ")
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// track writes a track the store journals as "kind:language:codec" as its
// kind in words, its language and its codec, leaving out an empty one; a
// string of another form is written as it is.
func (b Base) track(s string) string {
	kind, rest, _ := strings.Cut(s, ":")
	lang, codec, ok := strings.Cut(rest, ":")
	if !ok {
		return s
	}
	if m := "journal.track." + kind; b.has(m) {
		kind = b.T(m)
	}
	words := []string{kind}
	for _, w := range []string{lang, codec} {
		if w != "" {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

// searchID writes the ID parameter of a Torznab search, "tmdbid:949", as
// its provider and ID, "TMDB 949"; one of a provider it does not name is
// written as it is.
func (b Base) searchID(s string) string {
	param, id, ok := strings.Cut(s, ":")
	if name, known := searchIDProviders[param]; ok && known {
		return name + " " + id
	}
	return s
}

// times writes how many times a lease was taken: "3 times", "3 раза".
func (b Base) times(n int64) string {
	form := "many"
	switch m10, m100 := n%10, n%100; {
	case !b.ru() && n == 1, b.ru() && m10 == 1 && m100 != 11:
		form = "one"
	case b.ru() && m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		form = "few"
	}
	return b.Fmt("journal.times."+form, b.Num(n))
}

// detailTime reports whether the store writes the key as a moment.
func detailTime(key string) bool {
	return strings.HasSuffix(key, "_at") || strings.HasSuffix(key, "_until") || key == "last_seen"
}

// detailSize reports whether the store writes the key as bytes.
func detailSize(key string) bool {
	return key == "size" || key == "downloaded" || key == "max_bytes"
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
	Computed     time.Time // when the API computed the counts
	Total        int64
	NewDay       int64 // first seen on the current calendar day
	NewWeek      int64 // first seen on the last 7 calendar days, today included
	New24h       int64 // first seen in the last 24 clock hours, the current one included
	DroppedTotal int64

	ByKind   []Count // Key from Kinds
	ByLayer  []Count // Key from Layers
	BySource []Count // Key from Sources
	Voice    []VoiceStats
	Dropped  []Count // Key from DropReasons

	Classifier ClassifierStats
	Probe      ProbeStats
	Queue      []TaskQueue // one per TaskKinds, in its order; none when the API has no queue

	Hourly Chart // new torrents per clock hour, 48 hours
	Daily  Chart // new torrents per calendar day, 30 days
	Weekly Chart // new torrents per calendar week, 26 weeks
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
	PaceDay  int64 // what DoneDay comes to by the end of the UTC day at its pace so far, 0 for none
}

// ProbeStats is the piece data of probes on the UTC day, in bytes: RxDay
// downloaded, against LimitDay, 0 for none, and TxDay uploaded back.
// TaskQueue is the tasks of one kind queued, by priority.
type TaskQueue struct {
	Kind   string // a TaskKinds key
	Paced  bool   // whether the kind has a pace over the last 15 minutes
	Levels []QueueLevel
}

// TaskKinds are the task kinds a TaskQueue is of, in the order the data
// page shows them.
var TaskKinds = []string{"fetch", "peers", "probe"}

// QueueLevel is the tasks of one kind queued at one priority.
type QueueLevel struct {
	Priority int
	Tasks    int64
	// ETA is how long until this priority and those ahead are done, when
	// Known.
	ETA   time.Duration
	Known bool
}

// Tasks is every task queued at the levels.
func (q TaskQueue) Tasks() int64 {
	var n int64
	for _, l := range q.Levels {
		n += l.Tasks
	}
	return n
}

// QueueTasks is every task queued in the queues.
func QueueTasks(queues []TaskQueue) int64 {
	var n int64
	for _, q := range queues {
		n += q.Tasks()
	}
	return n
}

type ProbeStats struct {
	RxDay    int64
	TxDay    int64
	LimitDay int64
	PaceDay  int64 // what RxDay comes to by the end of the UTC day at its pace so far, 0 for none
}
