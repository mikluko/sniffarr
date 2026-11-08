package search

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/latest"
	"sarak.sh/mikluko/sniffarr/internal/names"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/store"
	"sarak.sh/mikluko/sniffarr/internal/version"
)

// MaxStatsHours is the most clock hours Browser.Stats counts: 3660 days.
const MaxStatsHours = 3660 * 24

// DefaultJournalLimit is how many journal entries Card carries and Journal
// returns asked for none in particular; MaxJournalLimit the most Journal
// returns.
const (
	DefaultJournalLimit = 200
	MaxJournalLimit     = 1000
)

// TitleSearch searches TMDB for works and stores what it finds;
// tmdb.Matcher is one.
type TitleSearch interface {
	SearchWorks(ctx context.Context, typ, query, lang string) ([]int64, error)
}

// TitleMatcher looks title keys up on TMDB when asked, recording the
// trigger; tmdb.Matcher is one.
type TitleMatcher interface {
	Matchable(ctx context.Context, keys []string) ([]store.TitleKey, error)
	Match(ctx context.Context, k store.TitleKey, trigger string) error
}

// LatestRelease knows the project's latest stable release;
// *latest.Checker is one.
type LatestRelease interface {
	Latest() (latest.Release, bool)
}

// Browser implements sniffarrv1connect.BrowseServiceHandler.
type Browser struct {
	Store *store.Store
	// Tasks, when set, has Stats report the probe queue.
	Tasks *store.Tasks
	// Fetches, when set, has Stats report the fetch orders and peer lookups,
	// and RefreshPeers order the lookups; without it RefreshPeers answers
	// FAILED_PRECONDITION.
	Fetches *orders.Queue
	// TMDB, when set, has SearchTitles search TMDB as well as the store.
	TMDB TitleSearch
	// Matcher, when set, serves RequestMatch.
	Matcher TitleMatcher
	Log     *slog.Logger
	// DailyTokens is the classifier's daily token limit Stats reports; 0 is
	// none.
	DailyTokens int64
	// ProbeDailyBytes is the probes' daily download limit Stats reports; 0
	// is none.
	ProbeDailyBytes int64
	// Probes has RequestProbe queue probes, and Settings report them on;
	// without it RequestProbe answers FAILED_PRECONDITION.
	Probes bool
	// Languages are the audio languages Settings reports, tags in configured
	// order; nil when the audio extension is off.
	Languages []string
	// Classifier has Settings report the classifier on.
	Classifier bool
	// ClassifierSetting is the setting Settings names for the classifier;
	// SNIFFARR_CLASSIFY_API_KEY when empty.
	ClassifierSetting string
	// JournalRetention is how long the journal keeps an entry, which
	// Settings reports when above 0.
	JournalRetention time.Duration
	// Releases, when set, has Settings report the latest stable release
	// it knows.
	Releases LatestRelease
	// ReleaseNotice is how far behind that release a stable build is before
	// the web UI announces it, which Settings reports beside it.
	ReleaseNotice version.Policy
	// Counts, when set, holds the counts Stats and Home serve; nil computes
	// them at every call.
	Counts *Counts
	// Now is time.Now when nil.
	Now func() time.Time
}

// counts returns the snapshot of Counts, or one computed now without it.
func (b *Browser) counts(ctx context.Context) (*Snapshot, error) {
	if b.Counts != nil {
		return b.Counts.Latest(ctx)
	}
	now := b.now()
	st, err := b.Store.Stats(ctx, now, MaxStatsHours)
	if err != nil {
		return nil, err
	}
	return &Snapshot{At: now, Stats: st}, nil
}

func (b *Browser) Settings(ctx context.Context, _ *connect.Request[sniffarrv1.SettingsRequest]) (*connect.Response[sniffarrv1.SettingsResponse], error) {
	spent, err := b.budgetSpent(ctx)
	if err != nil {
		return nil, b.internal(ctx, "settings", err)
	}
	feature := func(f sniffarrv1.Feature, on, works bool, setting string, values []string) *sniffarrv1.FeatureState {
		return &sniffarrv1.FeatureState{Feature: f, On: on, Works: on && works, Setting: setting, Values: slices.Clone(values)}
	}
	res := &sniffarrv1.SettingsResponse{AudioLanguages: slices.Clone(b.Languages), Probes: b.Probes, Features: []*sniffarrv1.FeatureState{
		feature(sniffarrv1.Feature_FEATURE_CLASSIFIER, b.Classifier, !spent, cmp.Or(b.ClassifierSetting, "SNIFFARR_CLASSIFY_API_KEY"), nil),
		feature(sniffarrv1.Feature_FEATURE_PROBES, b.Probes, b.probers() > 0, "-probe", nil),
		feature(sniffarrv1.Feature_FEATURE_AUDIO, len(b.Languages) > 0, true, "SNIFFARR_AUDIO_LANGUAGES", b.Languages),
	}}
	if b.JournalRetention > 0 {
		res.JournalRetention = durationpb.New(b.JournalRetention)
	}
	if b.Releases != nil {
		if r, ok := b.Releases.Latest(); ok {
			res.LatestRelease = &sniffarrv1.ProjectRelease{Version: r.Version, Published: timestamppb.New(r.Published), Url: r.URL}
			res.ReleaseNotice = releaseNotice(b.ReleaseNotice)
		}
	}
	return connect.NewResponse(res), nil
}

// releaseNotice is p as Settings reports it.
func releaseNotice(p version.Policy) sniffarrv1.ReleaseNotice {
	if p == version.Eager {
		return sniffarrv1.ReleaseNotice_RELEASE_NOTICE_EAGER
	}
	return sniffarrv1.ReleaseNotice_RELEASE_NOTICE_LAZY
}

// budgetSpent reports whether the classifier is on and has spent the
// day's token limit.
func (b *Browser) budgetSpent(ctx context.Context) (bool, error) {
	if !b.Classifier || b.DailyTokens <= 0 {
		return false, nil
	}
	used, err := b.Store.TokensUsed(ctx, b.now().UTC().Format(time.DateOnly))
	return used >= b.DailyTokens, err
}

// probers returns the probers seen within store.ProberWindow, 0 while
// probes are off or without Tasks.
func (b *Browser) probers() int {
	if !b.Probes || b.Tasks == nil {
		return 0
	}
	return b.Tasks.Probers(b.now())
}

func (b *Browser) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

var discoveries = map[string]sniffarrv1.Source{
	"sample":   sniffarrv1.Source_SOURCE_SAMPLE,
	"announce": sniffarrv1.Source_SOURCE_ANNOUNCE,
	"arr":      sniffarrv1.Source_SOURCE_ARR,
}

var rejections = map[rules.Reason]sniffarrv1.Rejection{
	rules.Adult:    sniffarrv1.Rejection_REJECTION_ADULT,
	rules.Software: sniffarrv1.Rejection_REJECTION_SOFTWARE,
	rules.Book:     sniffarrv1.Rejection_REJECTION_BOOK,
	rules.Course:   sniffarrv1.Rejection_REJECTION_COURSE,
	rules.Small:    sniffarrv1.Rejection_REJECTION_SMALL,
	rules.NoMedia:  sniffarrv1.Rejection_REJECTION_NO_MEDIA,
	rules.Script:   sniffarrv1.Rejection_REJECTION_SCRIPT,
}

// others are the kinds the UI shows as other.
var others = []string{"other", "music", "book", "software"}

// name returns the key of m whose value is v.
func name[K comparable, V comparable](m map[K]V, v V) (K, bool) {
	for k, x := range m {
		if x == v {
			return k, true
		}
	}
	var zero K
	return zero, false
}

// enum returns the store's name of the API's value v by m, "" for the
// unspecified value, or an INVALID_ARGUMENT error naming what for a value
// absent from m.
func enum[V comparable](m map[string]V, v V, what string) (string, error) {
	var zero V
	if v == zero {
		return "", nil
	}
	n, ok := name(m, v)
	if !ok {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown %s %v", what, v))
	}
	return n, nil
}

// pageLimit reads a page's limit and offset: DefaultLimit for none, at most
// MaxLimit.
func pageLimit(limit, offset int32) (int, int, error) {
	if limit < 0 || offset < 0 {
		return 0, 0, connect.NewError(connect.CodeInvalidArgument, errors.New("negative limit or offset"))
	}
	if limit == 0 {
		limit = DefaultLimit
	}
	return min(int(limit), MaxLimit), int(offset), nil
}

// unfiltered reports whether b selects every release but the excluded
// kinds, so the counts snapshot has its total and Page need not count.
func unfiltered(b store.Browse) bool {
	return b.Text == "" && b.Kinds == nil && b.Matched == nil && b.Quality == "" && b.Source == "" && b.KindLayer == "" &&
		b.MinSize == 0 && (b.Voice == "" || b.Voice == store.VoiceAny)
}

// voiceOf reads a voice and the language it applies to, which a narrowing
// voice requires.
func voiceOf(v sniffarrv1.Voice, lang *string) (store.Voice, string, error) {
	voice, ok := voices[v]
	if !ok {
		return "", "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown voice %d", v))
	}
	if voice != store.VoiceAny && (lang == nil || *lang == "") {
		return "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("voice without audio_language"))
	}
	if lang == nil {
		return voice, "", nil
	}
	return voice, *lang, nil
}

func (b *Browser) Torrents(ctx context.Context, req *connect.Request[sniffarrv1.TorrentsRequest]) (*connect.Response[sniffarrv1.TorrentsResponse], error) {
	m := req.Msg
	lang, err := language(m.GetLang())
	if err != nil {
		return nil, err
	}
	q := store.Browse{Text: m.GetQuery(), MinSize: m.GetMinSize(), Exclude: []string{"adult"}, Matched: m.Matched}
	if q.Limit, q.Offset, err = pageLimit(m.GetLimit(), m.GetOffset()); err != nil {
		return nil, err
	}
	switch k := m.GetKind(); {
	case m.GetUnknownKind() && k != sniffarrv1.Kind_KIND_UNSPECIFIED:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown_kind with a kind"))
	case m.GetUnknownKind():
		q.Kinds = []string{""}
	case k == sniffarrv1.Kind_KIND_OTHER:
		q.Kinds = others
	case k != sniffarrv1.Kind_KIND_UNSPECIFIED:
		kind, ok := kinds[k]
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown kind %d", k))
		}
		q.Kinds = []string{kind}
	}
	if q.Voice, q.AudioLanguage, err = voiceOf(m.GetVoice(), m.AudioLanguage); err != nil {
		return nil, err
	}
	if q.Quality, err = enum(qualities, m.GetQuality(), "quality"); err != nil {
		return nil, err
	}
	if q.Source, err = enum(sources, m.GetSource(), "source"); err != nil {
		return nil, err
	}
	if q.KindLayer, err = enum(layers, m.GetLayer(), "layer"); err != nil {
		return nil, err
	}
	sort, ok := sorts[m.GetSort()]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown sort %d", m.GetSort()))
	}
	q.Sort = sort
	q.NoTotal = unfiltered(q)
	found, total, err := b.Store.Page(ctx, q)
	if err != nil {
		return nil, b.internal(ctx, "torrents", err)
	}
	if q.NoTotal {
		snap, err := b.counts(ctx)
		if err != nil {
			return nil, b.internal(ctx, "torrents", err)
		}
		total = snap.Stats.Total - snap.Stats.Kinds["adult"]
	}
	var ids []int64
	for _, r := range found {
		if r.Work != 0 && !slices.Contains(ids, r.Work) {
			ids = append(ids, r.Work)
		}
	}
	sums, err := b.Store.Summaries(ctx, ids)
	if err != nil {
		return nil, b.internal(ctx, "torrents", err)
	}
	res := &sniffarrv1.TorrentsResponse{Total: total}
	for _, r := range found {
		row := &sniffarrv1.TorrentRow{Release: release(r, lang)}
		if sum, ok := sums[r.Work]; ok {
			row.Work = summary(sum, lang)
		}
		res.Rows = append(res.Rows, row)
	}
	return connect.NewResponse(res), nil
}

func (b *Browser) Card(ctx context.Context, req *connect.Request[sniffarrv1.CardRequest]) (*connect.Response[sniffarrv1.CardResponse], error) {
	lang, err := language(req.Msg.GetLang())
	if err != nil {
		return nil, err
	}
	h := req.Msg.GetInfohash()
	if len(h) != len(store.Hash{}) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("infohash of %d bytes, want 20", len(h)))
	}
	c, err := b.Store.Card(ctx, store.Hash(h), math.MaxInt32, b.now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err != nil {
		return nil, b.internal(ctx, "card", err)
	}
	res := &sniffarrv1.CardResponse{Release: release(c.Release, lang), Discovery: discoveries[c.Discovery],
		Peers: &sniffarrv1.PeerCounts{Day: c.Peers.Day, Week: c.Peers.Week, Month: c.Peers.Month}}
	for _, f := range c.Files {
		res.Files = append(res.Files, &sniffarrv1.File{Path: f.Path, Size: f.Size})
	}
	res.Rules = ruleHits(c.Name, c.Files, b.Languages)
	hash := store.Hash(h)
	j, err := b.journal(ctx, store.JournalFilter{Hash: &hash}, 0, DefaultJournalLimit)
	if err != nil {
		return nil, err
	}
	res.Journal, res.JournalTotal = j.GetEntries(), j.GetTotal()
	if c.Work != nil {
		res.Work, res.RankInWork = summary(*c.Work, lang), int32(c.RankInWork)
		if t := res.GetRelease().GetTmdb(); t != nil {
			res.Work.Tmdb.Layer, res.Work.Tmdb.Confidence, res.Work.Tmdb.CheckedAt = t.GetLayer(), t.GetConfidence(), t.GetCheckedAt()
		}
	}
	return connect.NewResponse(res), nil
}

func (b *Browser) Journal(ctx context.Context, req *connect.Request[sniffarrv1.JournalRequest]) (*connect.Response[sniffarrv1.JournalResponse], error) {
	f, err := journalFilter(req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	limit := int(req.Msg.GetLimit())
	if limit < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("limit %d", limit))
	}
	if limit == 0 {
		limit = DefaultJournalLimit
	}
	res, err := b.journal(ctx, f, req.Msg.GetBefore(), min(limit, MaxJournalLimit))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(res), nil
}

// journalFilter is the part of the journal msg asks for.
func journalFilter(msg *sniffarrv1.JournalRequest) (store.JournalFilter, error) {
	f := store.JournalFilter{Kind: msg.GetKind(), Outcome: msg.GetOutcome()}
	switch h := msg.GetInfohash(); len(h) {
	case 0:
	case len(store.Hash{}):
		f.Hash = (*store.Hash)(h)
	default:
		return f, fmt.Errorf("infohash of %d bytes, want 20", len(h))
	}
	outcomes, ok := store.JournalOutcomes[f.Kind]
	switch {
	case f.Kind != "" && !ok:
		return f, fmt.Errorf("unknown kind %q", f.Kind)
	case f.Outcome != "" && !slices.Contains(outcomes, f.Outcome):
		return f, fmt.Errorf("outcome %q not of kind %q", f.Outcome, f.Kind)
	}
	return f, nil
}

// journal returns the newest entries f admits before the entry of ID
// before, or of all for 0, up to limit, oldest first, with how many f
// admits and how many are older when f names a hash.
func (b *Browser) journal(ctx context.Context, f store.JournalFilter, before int64, limit int) (*sniffarrv1.JournalResponse, error) {
	p, err := b.Store.JournalPage(ctx, f, before, limit)
	if err != nil {
		return nil, b.internal(ctx, "journal", err)
	}
	res := &sniffarrv1.JournalResponse{Entries: make([]*sniffarrv1.JournalEntry, len(p.Entries)), Total: int64(p.Total), Older: int64(p.Older)}
	for i, e := range p.Entries {
		res.Entries[i] = &sniffarrv1.JournalEntry{Id: e.ID, Infohash: e.Hash[:], At: timestamppb.New(e.At), Kind: e.Kind, Outcome: e.Outcome, Detail: e.Detail}
	}
	return res, nil
}

// ruleHits are the features names.Explain finds in name, then the audio
// markers of the languages with tags that name or a file path has.
func ruleHits(name string, files []rules.File, tags []string) []*sniffarrv1.RuleHit {
	var out []*sniffarrv1.RuleHit
	for _, h := range names.Explain(name) {
		out = append(out, &sniffarrv1.RuleHit{Rule: h.Rule, Match: h.Match})
	}
	for _, tag := range tags {
		if l, ok := audio.Lookup(tag); ok {
			if m := l.Mark(name, files); m != "" {
				out = append(out, &sniffarrv1.RuleHit{Rule: "audio." + l.Tag, Match: m})
			}
		}
	}
	return out
}

func (b *Browser) Stats(ctx context.Context, req *connect.Request[sniffarrv1.StatsRequest]) (*connect.Response[sniffarrv1.StatsResponse], error) {
	hours := int(req.Msg.GetHours())
	if hours < 0 || hours > MaxStatsHours {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("hours %d, want 0 to %d", hours, MaxStatsHours))
	}
	snap, err := b.counts(ctx)
	if err != nil {
		return nil, b.internal(ctx, "stats", err)
	}
	st := snap.Stats
	day := b.now().UTC().Format(time.DateOnly)
	used, err := b.Store.TokensUsed(ctx, day)
	if err != nil {
		return nil, b.internal(ctx, "stats", err)
	}
	traffic, err := b.Store.ProbeTraffic(ctx, day)
	if err != nil {
		return nil, b.internal(ctx, "stats", err)
	}
	res := &sniffarrv1.StatsResponse{Total: st.Total, Pending: st.Pending, Hours: slices.Clone(st.Hours[:hours]), TokensUsedDay: used,
		TokensLimitDay: b.DailyTokens, ProbeBytesRxDay: traffic.Rx, ProbeBytesTxDay: traffic.Tx, ProbeBytesLimitDay: b.ProbeDailyBytes,
		ComputedAt: timestamppb.New(snap.At), Probers: int32(b.probers())}
	if b.Counts != nil && b.Counts.Interval > 0 {
		res.CountsInterval = durationpb.New(b.Counts.Interval)
	}
	if err = b.queue(ctx, res); err != nil {
		return nil, b.internal(ctx, "stats", err)
	}
	byKind := map[sniffarrv1.Kind]int64{}
	for kind, n := range st.Kinds {
		if slices.Contains(others, kind) {
			kind = "other"
		}
		k, _ := name(kinds, kind)
		byKind[k] += n
	}
	for _, k := range slices.Sorted(maps.Keys(byKind)) {
		res.Kinds = append(res.Kinds, &sniffarrv1.KindCount{Kind: k, Count: byKind[k]})
	}
	for _, layer := range slices.Sorted(maps.Keys(st.Layers)) {
		res.Layers = append(res.Layers, &sniffarrv1.LayerCount{Layer: layers[layer], Count: st.Layers[layer]})
	}
	for _, source := range slices.Sorted(maps.Keys(st.Sources)) {
		res.Sources = append(res.Sources, &sniffarrv1.SourceCount{Source: sources[source], Count: st.Sources[source]})
	}
	for _, lang := range slices.Sorted(maps.Keys(st.Audio)) {
		a := st.Audio[lang]
		res.Audio = append(res.Audio, &sniffarrv1.AudioCounts{Language: lang, Marker: a.Marker, Likely: a.Likely, Unlikely: a.Unlikely})
	}
	for _, reason := range slices.Sorted(maps.Keys(st.Rejected)) {
		res.Rejected = append(res.Rejected, &sniffarrv1.RejectionCount{Reason: rejections[rules.Reason(reason)], Count: st.Rejected[reason]})
	}
	return connect.NewResponse(res), nil
}

// queue sets the queue fields of res from Fetches and Tasks, leaving those
// of either unset without it.
func (b *Browser) queue(ctx context.Context, res *sniffarrv1.StatsResponse) error {
	if b.Fetches != nil {
		queues, err := b.Fetches.Levels(ctx, b.now())
		if err != nil {
			return err
		}
		res.QueueFetches = taskQueue(queues[store.TaskFetch])
		res.QueueLookups = taskQueue(queues[store.TaskPeers])
	}
	if b.Tasks != nil {
		queues, err := b.Tasks.Queues(ctx, b.now())
		if err != nil {
			return err
		}
		res.QueueProbes = taskQueue(queues[store.TaskProbe])
	}
	return nil
}

// taskQueue returns q as the API gives it, its pace per minute.
func taskQueue(q store.Queue) *sniffarrv1.TaskQueue {
	out := &sniffarrv1.TaskQueue{PerMinute: q.PerSecond * 60, Levels: make([]*sniffarrv1.TaskLevel, len(q.Levels))}
	for i, l := range q.Levels {
		out.Levels[i] = &sniffarrv1.TaskLevel{Priority: int32(l.Priority), Tasks: l.Tasks}
		if l.Known {
			out.Levels[i].Eta = durationpb.New(l.ETA)
		}
	}
	return out
}

// internal logs a failure of the operation and returns the INTERNAL error
// the client gets for it.
func (b *Browser) internal(ctx context.Context, op string, err error) error {
	b.Log.ErrorContext(ctx, op, "err", err)
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s failed", op))
}
