package ui

import (
	"encoding/hex"
	"fmt"
	"html/template"
	"maps"
	"math"
	"net/url"
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
	Method     string  // "search" (unique search hit) | "classifier" (picked among candidates) | "arr" (Radarr or Sonarr took a release)
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
	// ResetURL is the page with the query and the sort but no filter.
	ResetURL string
}

// LayerTip says who decided the row's kind, with the classifier's
// confidence; a row no layer decided says it is undetermined.
func (p TorrentsPage) LayerTip(r Row) string {
	switch {
	case r.Layer == "classifier" && r.Confidence != nil:
		return p.Fmt("torrents.layer_tip.classifier", p.Prob(r.Confidence))
	case r.Layer == "classifier":
		return p.T("torrents.layer_tip.classifier_plain")
	case slices.Contains(Layers, r.Layer):
		return p.T("torrents.layer_tip." + r.Layer)
	}
	return p.T("torrents.layer_tip.none")
}

// LayerOpts are the layers the filter offers: the classifier only while it
// runs or is the one selected.
func (p TorrentsPage) LayerOpts() []string {
	return slices.DeleteFunc(slices.Clone(Layers), func(l string) bool {
		return l == "classifier" && !p.Classifier && p.Filters.Layer != l
	})
}

// Filters is the torrents page's filters and sort.
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

// VoiceSet reports whether the voice filter narrows the results.
func (f Filters) VoiceSet() bool { return f.Voice != "" && f.Voice != "any" }

// Set is how many filters narrow the results; the sort and the voice
// filter's language are none.
func (f Filters) Set() int {
	n := 0
	for _, v := range []string{f.Kind, f.Matched, f.Quality, f.Source, f.Layer, f.MinSizeGB} {
		if v != "" {
			n++
		}
	}
	if f.VoiceSet() {
		n++
	}
	return n
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
	Since    time.Time // when the history of this top began; zero = none yet
	Limit    int       // 100
	All      bool      // Works holds all Limit entries; otherwise the first 25
	Works    []Work
}

// Work is one row of the top. The row links to the title page.
type Work struct {
	WorkSummary               // Rank here is the place in this top (window and filter)
	PrevRank    int           // place in the previous window among every work, 0 = had no peers then
	Debut       time.Time     // first entry into this top since TopPage.Since; zero = no history yet
	OnChart     time.Duration // time in this top over every stay
	Peak        int           // best place in this top; 0 = no history yet
	Reentry     bool          // the current stay follows an earlier one
}

// ---------- not found ----------

// NotFoundPage answers a path the UI does not serve; Asked is that path,
// as the client sent it.
type NotFoundPage struct {
	Base
	Asked string
}

// ---------- title page (GET /movie/{id}, GET /tv/{id}) ----------

// WorkPage is the page of one movie or series: TMDB on top, our releases below.
type WorkPage struct {
	Base
	Work     WorkSummary
	Ranks    [3]int     // place in the top of its kind for 24h, 7d, 30d; 0 = outside
	Peaks    [3]int     // best place in each of those tops since its history began; 0 = never in it
	Peers    PeerCounts // all releases
	Audio    []WorkAudio
	Releases ReleaseTable
	Back     string // the page the visitor came from, see BackTarget; "" shows no back link
}

// Places is a title's places in the tops over the windows, N[i] for
// Windows[i], 0 = none; the window at index Bold is shown in bold.
type Places struct {
	B    Base
	N    [3]int
	Bold int
}

// RankPlaces is the title's places now, the week's in bold.
func (p WorkPage) RankPlaces() Places { return Places{B: p.Base, N: p.Ranks, Bold: 1} }

// PeakPlaces is the title's best places since the tops' history began.
func (p WorkPage) PeakPlaces() Places { return Places{B: p.Base, N: p.Peaks, Bold: -1} }

// ReleaseURL is the title page under the release filters with the query
// parameter key set to value, from the first release.
func (p WorkPage) ReleaseURL(key, value string) string {
	return p.releaseURL(0, key, value)
}

// ReleasePageURL is the title page under the release filters from the
// release at offset.
func (p WorkPage) ReleasePageURL(offset int) string {
	return p.releaseURL(offset, "", "")
}

// Segment is one option of a release filter: a link to the page with the
// option chosen, On when it is.
type Segment struct {
	URL, Label string
	On         bool
}

// Seg is the option value of the filter key, shown as label.
func (p WorkPage) Seg(key, value, label string, on bool) Segment {
	return Segment{URL: p.ReleaseURL(key, value), Label: label, On: on}
}

// WorkDub is the title's dub in one language among its N releases.
type WorkDub struct {
	B Base
	A WorkAudio
	N int
}

// Dub is the title's dub in lang.
func (p WorkPage) Dub(lang string) WorkDub {
	return WorkDub{B: p.Base, A: WorkAudioOf(p.Audio, lang), N: p.Work.Releases}
}

// FiltersSet counts the release filters set; the order is not a filter.
func (p WorkPage) FiltersSet() int {
	f := p.Releases.Filters
	n := 0
	for _, v := range []string{f.Quality, f.MinGB, f.MaxGB, f.Season, f.Episode} {
		if v != "" {
			n++
		}
	}
	return n
}

// releaseURL leaves out every parameter at its default: sort "peers",
// offset 0, the rest "".
func (p WorkPage) releaseURL(offset int, key, value string) string {
	f := p.Releases.Filters
	pairs := [][3]string{
		{"quality", f.Quality, ""}, {"min", f.MinGB, ""}, {"max", f.MaxGB, ""},
		{"season", f.Season, ""}, {"episode", f.Episode, ""}, {"sort", f.Sort, "peers"},
	}
	v := url.Values{}
	for _, x := range pairs {
		if x[0] == key {
			x[1] = value
		}
		if x[1] != "" && x[1] != x[2] {
			v.Set(x[0], x[1])
		}
	}
	if offset > 0 {
		v.Set("o", strconv.Itoa(offset))
	}
	if p.Back != "" {
		v.Set("from", p.Back)
	}
	if len(v) == 0 {
		return p.Releases.Path
	}
	return p.Releases.Path + "?" + v.Encode()
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
	Quality string // Qualities value, "" = any
	MinGB   string // raw inputs, echoed back
	MaxGB   string
	Season  string // "" = all, else a number; series only
	Episode string // "" = all, else a number; series only
	Sort    string // ReleaseSorts value, "" = peers
}

type Release struct {
	InfoHash  string
	Name      string
	Peers     int64 // unique peers, 7 days
	Size      int64
	Quality   string // Qualities value, "" = unknown
	Season    int    // series: 0 = unknown
	Episode   int    // series: 0 = whole season or unknown
	FirstSeen time.Time
}

// ---------- torrent card ----------

// Requested answers a card's button: Action, "peers" or "probe", was asked
// for Releases releases; or "match" looked Titles title keys up, finding
// Work, or failed for Failure, "budget", "off" or "unavailable". It is the
// htmx fragment "act": the line under the buttons, and the pressed button,
// disabled.
type Requested struct {
	Base
	Action   string
	Releases int
	Titles   int
	Work     *WorkSummary
	Failure  string
	// NoProbers marks a probe asked for while probes are on and no prober
	// is connected: the answer says it waits for one, in amber.
	NoProbers bool
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
	// JournalTotal is how many entries the journal holds in all.
	JournalTotal int64
	// JournalOlder is how many entries are older than Journal's.
	JournalOlder int64
	// JournalAll has the card show every row of Journal, not only the newest
	// JournalCardRows.
	JournalAll bool
	// JournalAfter is the day, "2006-01-02" in the zone of Loc, of the oldest
	// row shown above a page of older entries; "" for the first page.
	JournalAfter string

	Files FileList
}

// Matchable reports whether the card offers to look the torrent's title up
// on TMDB now: a movie or series with a title, matched to no work.
func (t Torrent) Matchable() bool {
	return t.Work == nil && t.TMDBQuery != "" && (t.Kind.Value == "movie" || t.Kind.Value == "series")
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
	// ID is the entry's own, unique across the journal.
	ID      int64
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
// a key by "journal.key.<kind>.<key>" or else "journal.key.<key>", or as it
// is without a message; a value as journalValue writes its typed form.
func (b Base) JournalDetail(e JournalEntry) []DetailPair {
	return b.journalDetail(e, "")
}

// journalDetail is JournalDetail without the particular of the key skip, and
// without the audio probabilities while no dubbing language is configured.
func (b Base) journalDetail(e JournalEntry, skip string) []DetailPair {
	out := make([]DetailPair, 0, len(e.Detail))
	for _, f := range e.Fields() {
		if f.Key != skip && !b.hiddenKey(f.Key) {
			out = append(out, b.journalPair(e, f))
		}
	}
	return out
}

// hiddenKey reports whether the journal leaves the particular of the key out:
// the audio probabilities are past data of a feature that is off.
func (b Base) hiddenKey(key string) bool {
	return key == "audio" && len(b.VoiceLangs) == 0
}

// journalPair is the particular of the entry the field is, as JournalDetail
// writes it.
func (b Base) journalPair(e JournalEntry, f JournalField) DetailPair {
	p := DetailPair{Key: b.journalKey(e.Kind, f.Key)}
	p.Value, p.Href = b.journalValue(e, f.Key, f.Value)
	return p
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

// journalValue writes the typed value of the key of the entry and the page
// it links to, "" for none: a moment by its clock alone on the entry's day in
// the zone of Loc, else by TimeFull; an Infohash as its first 8 characters
// linking to its card; a Link by its label; a list by commas, unlinked; an
// object as "key value" by commas, a probability in audio as a percentage;
// any other as journalScalar writes it.
func (b Base) journalValue(e JournalEntry, key string, v any) (string, string) {
	switch x := v.(type) {
	case time.Time:
		return b.journalTime(e.At, x), ""
	case Infohash:
		return string(x[:8]), "/t/" + string(x)
	case Link:
		return x.Label, x.URL
	case []any:
		parts := make([]string, len(x))
		for i, el := range x {
			parts[i], _ = b.journalValue(e, key, el)
		}
		return strings.Join(parts, ", "), ""
	case map[string]any:
		return b.journalObject(e, key, x), ""
	}
	return b.journalScalar(e.Kind, key, v), ""
}

// journalScalar writes a value of the key of an entry of the kind: Bytes by
// Size; Times as how many times; an ID as it is; an integer grouped by Num; a
// fraction with the locale's decimal mark; a boolean by
// "journal.bool.<value>"; a Track by track; a string by journalString.
func (b Base) journalScalar(kind, key string, v any) string {
	switch x := v.(type) {
	case string:
		return b.journalString(kind, key, x)
	case Bytes:
		return b.Size(int64(x))
	case Times:
		return b.times(int64(x))
	case ID:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return b.Num(x)
	case float64:
		return b.dec(x, -1)
	case bool:
		return b.T("journal.bool." + strconv.FormatBool(x))
	case Track:
		return b.track(x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// journalString writes a string of the key of an entry of the kind: a
// search's ID as its provider and the ID, a value journalValues maps by its
// message, any other as it is.
func (b Base) journalString(kind, key, s string) string {
	if kind == "probe_wanted" && key == "id" {
		return b.searchID(s)
	}
	prefix, ok := journalValues[kind+"."+key]
	if !ok {
		prefix, ok = journalValues[key]
	}
	if m := prefix + s; ok {
		if a, aliased := journalAliases[m]; aliased {
			m = a
		}
		if b.has(m) {
			return b.T(m)
		}
	}
	return s
}

// journalObject writes an object under the key of the entry, as journalValue
// does.
func (b Base) journalObject(e JournalEntry, key string, m map[string]any) string {
	parts := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if p, ok := m[k].(float64); ok && key == "audio" {
			parts = append(parts, b.LangName(k)+" "+strconv.FormatFloat(math.Round(p*100), 'f', 0, 64)+"%")
			continue
		}
		s, _ := b.journalValue(e, k, m[k])
		parts = append(parts, k+" "+s)
	}
	return strings.Join(parts, ", ")
}

// journalTime writes a moment of an entry at the time by its clock alone when
// both fall on one day in the zone of Loc, else by TimeFull.
func (b Base) journalTime(at, t time.Time) string {
	loc := b.Loc
	if loc == nil {
		loc = time.Local
	}
	y, m, d := t.In(loc).Date()
	if ay, am, ad := at.In(loc).Date(); !at.IsZero() && y == ay && m == am && d == ad {
		return t.In(loc).Format("15:04")
	}
	return b.TimeFull(t)
}

// track writes a track as its kind in words, its language and its codec,
// leaving out an empty one.
func (b Base) track(t Track) string {
	kind := t.Type
	if m := "journal.track." + kind; b.has(m) {
		kind = b.T(m)
	}
	words := []string{kind}
	for _, w := range []string{t.Lang, t.Codec} {
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
	// Features are the optional features, one per FeatureKeys in its
	// order; none leaves the block of them out.
	Features []Feature
}

// FeatureKeys are the optional features in the order the data page lists
// them; labels come from i18n ("feat.<key>").
var FeatureKeys = []string{"classifier", "probes", "audio", "feeds"}

// Feature is the state of an optional feature: whether it is On, whether it
// Works now, the flag or variable Setting that turns it on, and the Values
// it is on with, the audio languages or the feeds' names.
type Feature struct {
	Key     string
	On      bool
	Works   bool
	Setting string
	Values  []string
}

// Feature returns the state of the feature with the key, off when
// Features has none.
func (p DataPage) Feature(key string) Feature {
	for _, f := range p.Features {
		if f.Key == key {
			return f
		}
	}
	return Feature{Key: key}
}

// ClassifierSpent reports whether the classifier is on and has spent the
// day's token limit.
func (p DataPage) ClassifierSpent() bool {
	f := p.Feature("classifier")
	return f.On && !f.Works
}

// NoProbers reports whether probes are on and no prober is connected.
func (p DataPage) NoProbers() bool {
	f := p.Feature("probes")
	return f.On && !f.Works
}

// FeatureState is the state the block of features writes for f: "off",
// "works" or "fails", the last in amber.
func (p DataPage) FeatureState(f Feature) string {
	switch {
	case !f.On:
		return "off"
	case f.Works:
		return "works"
	}
	return "fails"
}

// FeatureNote is the line beside a feature's state: how to turn it on while
// it is off, what it does or why it fails while it is on.
func (p DataPage) FeatureNote(f Feature) string {
	state := p.FeatureState(f)
	switch {
	case state == "off":
		return p.Fmt("feat.how."+f.Key, f.Setting)
	case f.Key == "classifier" && state == "works" && p.Stats.Classifier.LimitDay > 0:
		return p.Fmt("feat.why.classifier_limit", p.Pct(p.Stats.Classifier.DoneDay, p.Stats.Classifier.LimitDay))
	case f.Key == "probes" && state == "fails":
		return p.Fmt("feat.why.probes_fails", p.Count(p.probeTasks(), "q.tasks_n"))
	}
	return p.T("feat.why." + f.Key + "_" + state)
}

// FeatureLabel is a feature's state in words, with the values it is on with.
func (p DataPage) FeatureLabel(f Feature) string {
	s := p.T("feat.state." + f.Key + "." + p.FeatureState(f))
	if f.On && len(f.Values) > 0 {
		s += ": " + strings.Join(f.Values, ", ")
	}
	return s
}

// probeTasks is every probe queued, 0 without the queue.
func (p DataPage) probeTasks() int64 {
	for _, q := range p.Stats.Queue {
		if q.Kind == "probe" {
			return q.Tasks()
		}
	}
	return 0
}

// Refresh is how often the data page fetches itself again while it is
// visible.
const Refresh = 30 * time.Second

// RefreshSeconds is Refresh in whole seconds.
func (p DataPage) RefreshSeconds() int { return int(Refresh / time.Second) }

// Stale reports whether at Now the counts are older than two of the
// intervals they are refreshed at, the longer of Refresh and
// Stats.Interval.
func (p DataPage) Stale() bool {
	return p.Now.Sub(p.Stats.Computed) > 2*max(Refresh, p.Stats.Interval)
}

// RefreshTip is the refresh button's tooltip: when the counts were
// computed, how long ago, and how often the page refreshes itself.
func (p DataPage) RefreshTip() string {
	return p.Fmt("d.refresh_tip", p.Time(p.Stats.Computed), p.Ago(p.Stats.Computed), strconv.Itoa(p.RefreshSeconds()))
}

// ClassifierTip is the tooltip of the classifier's figure: its tokens on
// the UTC day against the limit, their pace, and the torrents pending; or,
// with the limit spent, what decides the kind until midnight.
func (p DataPage) ClassifierTip() string {
	c := p.Stats.Classifier
	if p.ClassifierSpent() {
		return p.Fmt("d.tokens_limit_tip", p.Num(c.DoneDay), p.Num(c.LimitDay)) + " " + p.T("d.spent_tip")
	}
	s := p.Fmt("d.tokens_tip", p.Num(c.DoneDay))
	if c.LimitDay > 0 {
		s = p.Fmt("d.tokens_limit_tip", p.Num(c.DoneDay), p.Num(c.LimitDay))
		if c.PaceDay > 0 {
			s += " " + p.Fmt("d.pace_tip", p.Pct(c.PaceDay, c.LimitDay))
		}
	}
	return s + " " + p.Fmt("d.pending_tip", p.Num(c.Pending))
}

// ProbeTip is the tooltip of the probes' figure: piece data downloaded on
// the UTC day against the limit, uploaded back, and the pace.
func (p DataPage) ProbeTip() string {
	r := p.Stats.Probe
	if r.LimitDay == 0 {
		return p.Fmt("d.probe_tip", p.Size(r.RxDay), p.Size(r.TxDay))
	}
	s := p.Fmt("d.probe_limit_tip", p.Size(r.RxDay), p.Size(r.LimitDay), p.Size(r.TxDay))
	if r.PaceDay > 0 {
		s += " " + p.Fmt("d.pace_tip", p.Pct(r.PaceDay, r.LimitDay))
	}
	return s
}

type Stats struct {
	Computed     time.Time     // when the API computed the counts
	Interval     time.Duration // how often the API computes the counts, 0 when at every request
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
	Queue      []TaskQueue // one per TaskKinds, in its order, the last left out while probes are off; none when the API has no queue

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

// ProbeStats is the piece data of probes on the UTC day, in bytes: RxDay
// downloaded, against LimitDay, 0 for none, and TxDay uploaded back.
type ProbeStats struct {
	RxDay    int64
	TxDay    int64
	LimitDay int64
	PaceDay  int64 // what RxDay comes to by the end of the UTC day at its pace so far, 0 for none
}

// Magnet returns the torrent's magnet link, built from InfoHash and Name with
// both query-escaped, so marking it template.URL cannot inject markup or
// parameters.
func (t Torrent) Magnet() template.URL {
	dn := strings.ReplaceAll(url.QueryEscape(t.Name), "+", "%20")
	return template.URL("magnet:?xt=urn:btih:" + url.QueryEscape(t.InfoHash) + "&dn=" + dn)
}
