package search

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/database"
	"sarak.sh/mikluko/sniffarr/internal/latest"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/store"
	"sarak.sh/mikluko/sniffarr/internal/version"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func hash(b byte) store.Hash { return store.Hash{b} }

// browser holds a rules movie with a Russian marker and two files, a
// classified web series, a torrent of unknown kind seen a day later, an
// album, and an adult one; two hashes are rejected as small and 250 tokens
// are spent today.
func browser(t *testing.T) *Browser {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	for _, tr := range []store.Torrent{
		{Hash: hash(1), Name: "Alien.1979.Rus.1080p", Size: 8 << 30, Discovery: "announce", SeenAt: now.Add(-time.Hour), Kind: "movie", KindLayer: store.LayerRules, Quality: "1080p",
			Files: []rules.File{{Path: "Alien.mkv", Size: 8 << 30}, {Path: "Alien.srt", Size: 1 << 10}},
			Audio: []store.Audio{{Language: "ru", Marker: true, Mark: "Rus", Probability: new(1.0), Layer: store.LayerRules}}},
		{Hash: hash(2), Name: "Severance S02", Size: 40 << 30, Discovery: "sample", SeenAt: now.Add(-30 * time.Hour), Audio: []store.Audio{{Language: "ru"}}},
		{Hash: hash(3), Name: "Чужой", Size: 2 << 30, Discovery: "sample", SeenAt: now.Add(-2 * time.Hour), Audio: []store.Audio{{Language: "ru", ScriptHint: true}}},
		{Hash: hash(4), Name: "Moon Safari", Size: 1 << 30, Discovery: "sample", SeenAt: now.Add(-3 * time.Hour), Kind: "music", KindLayer: store.LayerRules},
		{Hash: hash(5), Name: "Something", Size: 90 << 30, Discovery: "sample", SeenAt: now, Kind: "adult", KindLayer: store.LayerRules},
	} {
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	r, err := s.Get(ctx, hash(2))
	require.NoError(t, err)
	_, err = s.Classify(ctx, r.ID, store.Classification{Kind: "series", KindConfidence: 0.9, Source: "web", SourceConfidence: 0.8, Audio: map[string]float64{"ru": 0.7}}, now)
	require.NoError(t, err)
	for _, h := range []store.Hash{hash(10), hash(11)} {
		_, err := s.Reject(ctx, h, rules.Small, "small", 1<<20, now)
		require.NoError(t, err)
	}
	require.NoError(t, s.AddTokens(ctx, now.Format(time.DateOnly), 250))
	require.NoError(t, s.AddProbeTraffic(ctx, now.Format(time.DateOnly), store.ProbeTraffic{Rx: 3 << 30, Tx: 1 << 30}))
	return &Browser{Store: s, Log: slog.New(slog.DiscardHandler), DailyTokens: 1000, ProbeDailyBytes: 100 << 30, Languages: []string{"ru"}, Now: func() time.Time { return now }}
}

// tasks is an empty queue in a database of its own.
func tasks(t *testing.T) *store.Tasks {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "tasks.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	return q
}

func relNames(rs []*sniffarrv1.Release) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.GetName())
	}
	return out
}

func rowNames(rs []*sniffarrv1.TorrentRow) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.GetRelease().GetName())
	}
	return out
}

func TestBrowserTorrents(t *testing.T) {
	b := browser(t)
	ctx := context.Background()
	for _, tt := range []struct {
		name  string
		req   *sniffarrv1.TorrentsRequest
		want  []string
		total int64
	}{
		{name: "latest first without adult", req: &sniffarrv1.TorrentsRequest{}, want: []string{"Alien.1979.Rus.1080p", "Чужой", "Moon Safari", "Severance S02"}, total: 4},
		{name: "a page", req: &sniffarrv1.TorrentsRequest{Offset: 1, Limit: 1}, want: []string{"Чужой"}, total: 4},
		{name: "text", req: &sniffarrv1.TorrentsRequest{Query: "alien mkv"}, want: []string{"Alien.1979.Rus.1080p"}, total: 1},
		{name: "infohash", req: &sniffarrv1.TorrentsRequest{Query: "0300000000000000000000000000000000000000"}, want: []string{"Чужой"}, total: 1},
		{name: "kind", req: &sniffarrv1.TorrentsRequest{Kind: sniffarrv1.Kind_KIND_SERIES}, want: []string{"Severance S02"}, total: 1},
		{name: "other takes music in", req: &sniffarrv1.TorrentsRequest{Kind: sniffarrv1.Kind_KIND_OTHER}, want: []string{"Moon Safari"}, total: 1},
		{name: "adult never", req: &sniffarrv1.TorrentsRequest{Kind: sniffarrv1.Kind_KIND_ADULT}, total: 0},
		{name: "unknown kind", req: &sniffarrv1.TorrentsRequest{UnknownKind: true}, want: []string{"Чужой"}, total: 1},
		{name: "marker", req: &sniffarrv1.TorrentsRequest{AudioLanguage: new("ru"), Voice: sniffarrv1.Voice_VOICE_MARKER}, want: []string{"Alien.1979.Rus.1080p"}, total: 1},
		{name: "likely includes markers", req: &sniffarrv1.TorrentsRequest{AudioLanguage: new("ru"), Voice: sniffarrv1.Voice_VOICE_LIKELY},
			want: []string{"Alien.1979.Rus.1080p", "Severance S02"}, total: 2},
		{name: "quality", req: &sniffarrv1.TorrentsRequest{Quality: sniffarrv1.Quality_QUALITY_1080P}, want: []string{"Alien.1979.Rus.1080p"}, total: 1},
		{name: "unmatched", req: &sniffarrv1.TorrentsRequest{Matched: new(false), Limit: 1}, want: []string{"Alien.1979.Rus.1080p"}, total: 4},
		{name: "matched", req: &sniffarrv1.TorrentsRequest{Matched: new(true)}, total: 0},
		{name: "source", req: &sniffarrv1.TorrentsRequest{Source: sniffarrv1.VideoSource_VIDEO_SOURCE_WEB}, want: []string{"Severance S02"}, total: 1},
		{name: "layer", req: &sniffarrv1.TorrentsRequest{Layer: sniffarrv1.Layer_LAYER_CLASSIFIER}, want: []string{"Severance S02"}, total: 1},
		{name: "size", req: &sniffarrv1.TorrentsRequest{MinSize: 5 << 30}, want: []string{"Alien.1979.Rus.1080p", "Severance S02"}, total: 2},
		{name: "sort by size", req: &sniffarrv1.TorrentsRequest{Sort: sniffarrv1.Sort_SORT_SIZE, Limit: 1}, want: []string{"Severance S02"}, total: 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := b.Torrents(ctx, connect.NewRequest(tt.req))
			require.NoError(t, err)
			require.Equal(t, tt.want, rowNames(res.Msg.GetRows()))
			require.Equal(t, tt.total, res.Msg.GetTotal())
			for _, r := range res.Msg.GetRows() {
				require.Nil(t, r.GetWork(), "none is matched")
			}
		})
	}
	for name, req := range map[string]*sniffarrv1.TorrentsRequest{
		"negative limit":       {Limit: -1},
		"negative offset":      {Offset: -1},
		"unknown kind and one": {UnknownKind: true, Kind: sniffarrv1.Kind_KIND_MOVIE},
		"voice without lang":   {Voice: sniffarrv1.Voice_VOICE_MARKER},
		"unknown source":       {Source: 99},
		"unknown quality":      {Quality: 99},
		"unknown sort":         {Sort: 99},
		"unknown lang":         {Lang: "xx"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := b.Torrents(ctx, connect.NewRequest(req))
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
	res, err := b.Torrents(ctx, connect.NewRequest(&sniffarrv1.TorrentsRequest{Query: "alien"}))
	require.NoError(t, err)
	r := res.Msg.GetRows()[0].GetRelease()
	require.Equal(t, sniffarrv1.Quality_QUALITY_1080P, r.GetQuality())
	require.Equal(t, "Rus", r.GetAudio()[0].GetMark(), "the marker's mark")
	require.Empty(t, r.GetWork())
}

func TestBrowserCard(t *testing.T) {
	b := browser(t)
	ctx := context.Background()
	h := hash(1)
	res, err := b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: h[:]}))
	require.NoError(t, err)
	require.Equal(t, "Alien.1979.Rus.1080p", res.Msg.GetRelease().GetName())
	require.Equal(t, sniffarrv1.Source_SOURCE_ANNOUNCE, res.Msg.GetDiscovery())
	require.Len(t, res.Msg.GetFiles(), 2)
	require.Equal(t, "Alien.mkv", res.Msg.GetFiles()[0].GetPath())
	require.True(t, res.Msg.GetRelease().GetAudio()[0].GetMarker())
	require.Nil(t, res.Msg.GetWork(), "unmatched")
	require.Zero(t, res.Msg.GetRankInWork())
	require.Equal(t, []*sniffarrv1.RuleHit{
		{Rule: "year", Match: "1979"}, {Rule: "quality.1080p", Match: "1080p"}, {Rule: "audio.ru", Match: "Rus"},
	}, res.Msg.GetRules())
	require.Len(t, res.Msg.GetJournal(), 1)
	require.Equal(t, store.JournalFetched, res.Msg.GetJournal()[0].GetKind())
	require.Equal(t, "stored", res.Msg.GetJournal()[0].GetOutcome())
	require.Equal(t, now.Add(-time.Hour), res.Msg.GetJournal()[0].GetAt().AsTime())
	require.Contains(t, res.Msg.GetJournal()[0].GetDetail(), `"name":"Alien.1979.Rus.1080p"`)
	require.NotZero(t, res.Msg.GetJournal()[0].GetId())
	require.Equal(t, int64(1), res.Msg.GetJournalTotal())

	h = hash(3)
	res, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: h[:]}))
	require.NoError(t, err)
	require.Empty(t, res.Msg.GetRules(), "nothing recognised")

	h = hash(9)
	_, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: h[:]}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: []byte{1}}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestBrowserJournal(t *testing.T) {
	b := browser(t)
	ctx := context.Background()
	h := hash(2)
	res, err := b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Infohash: h[:]}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetEntries(), 2)
	require.Equal(t, []string{"fetched/stored", "classified/classifier"}, []string{
		res.Msg.GetEntries()[0].GetKind() + "/" + res.Msg.GetEntries()[0].GetOutcome(), res.Msg.GetEntries()[1].GetKind() + "/" + res.Msg.GetEntries()[1].GetOutcome()}, "oldest first")
	require.Equal(t, int64(2), res.Msg.GetTotal())
	res, err = b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Infohash: h[:], Limit: 1}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetEntries(), 1)
	require.Equal(t, store.JournalClassified, res.Msg.GetEntries()[0].GetKind(), "the newest within the limit")
	require.Equal(t, int64(2), res.Msg.GetTotal(), "the total of all")

	h = hash(10)
	res, err = b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Infohash: h[:]}))
	require.NoError(t, err)
	require.Equal(t, []string{store.JournalFetched}, []string{res.Msg.GetEntries()[0].GetKind()}, "a rejected hash has a journal though no card")
	h = hash(9)
	res, err = b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Infohash: h[:]}))
	require.NoError(t, err, "a hash never seen is no error")
	require.Empty(t, res.Msg.GetEntries())
	_, err = b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Infohash: []byte{1}}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Infohash: h[:], Limit: -1}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "a negative limit")

	h = hash(12)
	for i := range MaxJournalLimit + 1 {
		_, err := b.Store.RecordArr(ctx, store.ArrEvent{App: store.AppRadarr, Event: store.EventGrab, Key: strconv.Itoa(i), Hash: &h, At: now}, now)
		require.NoError(t, err)
	}
	res, err = b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Infohash: h[:], Limit: MaxJournalLimit + 1}))
	require.NoError(t, err, "a limit above the most is no error")
	require.Equal(t, MaxJournalLimit, len(res.Msg.GetEntries()), "it returns the most")
	require.Equal(t, int64(1), res.Msg.GetOlder(), "and how many are older")

	oldest := res.Msg.GetEntries()[0].GetId()
	res, err = b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Infohash: h[:], Before: oldest}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetEntries(), 1, "the page before the oldest")
	require.Less(t, res.Msg.GetEntries()[0].GetId(), oldest)
	require.Equal(t, int64(MaxJournalLimit+1), res.Msg.GetTotal())
	require.Zero(t, res.Msg.GetOlder())
	h = hash(2)
	res, err = b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Infohash: h[:], Before: oldest}))
	require.NoError(t, err)
	require.Empty(t, res.Msg.GetEntries(), "before an entry of another infohash, none")
}

func TestBrowserJournalFiltered(t *testing.T) {
	b := browser(t)
	ctx := context.Background()
	res, err := b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{}))
	require.NoError(t, err, "no infohash is every infohash's")
	require.Greater(t, len(res.Msg.GetEntries()), 2)
	hashes := map[string]bool{}
	for _, e := range res.Msg.GetEntries() {
		hashes[string(e.GetInfohash())] = true
	}
	require.Greater(t, len(hashes), 1, "each entry names its infohash")
	require.Zero(t, res.Msg.GetTotal(), "no total without an infohash")

	res, err = b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Kind: store.JournalClassified}))
	require.NoError(t, err)
	require.NotEmpty(t, res.Msg.GetEntries())
	for _, e := range res.Msg.GetEntries() {
		require.Equal(t, store.JournalClassified, e.GetKind())
	}
	h := hash(2)
	res, err = b.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Infohash: h[:], Kind: store.JournalFetched, Outcome: "stored"}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetEntries(), 1)
	require.Equal(t, int64(1), res.Msg.GetTotal(), "the infohash's entries of the kind and outcome")
	require.Equal(t, h[:], res.Msg.GetEntries()[0].GetInfohash())

	for _, req := range []*sniffarrv1.JournalRequest{
		{Kind: "nonsense"},
		{Outcome: "stored"},
		{Kind: store.JournalFetched, Outcome: "tracks"},
	} {
		_, err = b.Journal(ctx, connect.NewRequest(req))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "%v", req)
	}
}

func TestBrowserStats(t *testing.T) {
	b := browser(t)
	ctx := context.Background()
	res, err := b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{Hours: 4}))
	require.NoError(t, err)
	m := res.Msg
	require.EqualValues(t, 5, m.GetTotal())
	require.Equal(t, []*sniffarrv1.KindCount{
		{Kind: sniffarrv1.Kind_KIND_UNSPECIFIED, Count: 1}, {Kind: sniffarrv1.Kind_KIND_MOVIE, Count: 1}, {Kind: sniffarrv1.Kind_KIND_SERIES, Count: 1},
		{Kind: sniffarrv1.Kind_KIND_ADULT, Count: 1}, {Kind: sniffarrv1.Kind_KIND_OTHER, Count: 1},
	}, m.GetKinds(), "music counts as other")
	require.Equal(t, []*sniffarrv1.LayerCount{
		{Layer: sniffarrv1.Layer_LAYER_UNSPECIFIED, Count: 1}, {Layer: sniffarrv1.Layer_LAYER_CLASSIFIER, Count: 1}, {Layer: sniffarrv1.Layer_LAYER_RULES, Count: 3},
	}, m.GetLayers())
	require.Equal(t, []*sniffarrv1.SourceCount{
		{Source: sniffarrv1.VideoSource_VIDEO_SOURCE_UNSPECIFIED, Count: 4}, {Source: sniffarrv1.VideoSource_VIDEO_SOURCE_WEB, Count: 1},
	}, m.GetSources())
	require.Equal(t, []*sniffarrv1.AudioCounts{{Language: "ru", Marker: 1, Likely: 1}}, m.GetAudio())
	require.Equal(t, []*sniffarrv1.RejectionCount{{Reason: sniffarrv1.Rejection_REJECTION_SMALL, Count: 2}}, m.GetRejected())
	require.EqualValues(t, 1, m.GetPending(), "the script-hinted torrent of unknown kind")
	require.Equal(t, []int64{1, 1, 1, 1}, m.GetHours(), "one an hour, the current hour first")
	require.EqualValues(t, 250, m.GetTokensUsedDay())
	require.EqualValues(t, 1000, m.GetTokensLimitDay())
	require.EqualValues(t, 3<<30, m.GetProbeBytesRxDay())
	require.EqualValues(t, 1<<30, m.GetProbeBytesTxDay())
	require.EqualValues(t, 100<<30, m.GetProbeBytesLimitDay())

	_, err = b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{Hours: MaxStatsHours + 1}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestBrowserSettings(t *testing.T) {
	ctx := context.Background()
	res, err := (&Browser{}).Settings(ctx, connect.NewRequest(&sniffarrv1.SettingsRequest{}))
	require.NoError(t, err)
	require.Empty(t, res.Msg.GetAudioLanguages(), "the extension is off")
	b := &Browser{Languages: []string{"ru", "en"}}
	res, err = b.Settings(ctx, connect.NewRequest(&sniffarrv1.SettingsRequest{}))
	require.NoError(t, err)
	require.Equal(t, []string{"ru", "en"}, res.Msg.GetAudioLanguages(), "the configured tags in their order")
	require.False(t, res.Msg.GetProbes(), "probes are off")
	res, err = (&Browser{Probes: true}).Settings(ctx, connect.NewRequest(&sniffarrv1.SettingsRequest{}))
	require.NoError(t, err)
	require.True(t, res.Msg.GetProbes())
	require.Nil(t, res.Msg.GetJournalRetention(), "no retention, none reported")
	res, err = (&Browser{JournalRetention: 30 * 24 * time.Hour}).Settings(ctx, connect.NewRequest(&sniffarrv1.SettingsRequest{}))
	require.NoError(t, err)
	require.Equal(t, 30*24*time.Hour, res.Msg.GetJournalRetention().AsDuration())
	require.Nil(t, res.Msg.GetLatestRelease(), "no release check, none reported")
}

// knownRelease is a LatestRelease that knows r when ok.
type knownRelease struct {
	r  latest.Release
	ok bool
}

func (k knownRelease) Latest() (latest.Release, bool) { return k.r, k.ok }

func TestBrowserSettingsLatestRelease(t *testing.T) {
	ctx := context.Background()
	published := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	r := latest.Release{Version: "0.46.0", Published: published, URL: "https://sarak.sh/mikluko/sniffarr/releases/tag/v0.46.0"}
	res, err := (&Browser{Releases: knownRelease{r, true}}).Settings(ctx, connect.NewRequest(&sniffarrv1.SettingsRequest{}))
	require.NoError(t, err)
	got := res.Msg.GetLatestRelease()
	require.Equal(t, "0.46.0", got.GetVersion())
	require.Equal(t, published, got.GetPublished().AsTime())
	require.Equal(t, r.URL, got.GetUrl())
	require.Equal(t, sniffarrv1.ReleaseNotice_RELEASE_NOTICE_LAZY, res.Msg.GetReleaseNotice())

	res, err = (&Browser{Releases: knownRelease{r, true}, ReleaseNotice: version.Eager}).Settings(ctx, connect.NewRequest(&sniffarrv1.SettingsRequest{}))
	require.NoError(t, err)
	require.Equal(t, sniffarrv1.ReleaseNotice_RELEASE_NOTICE_EAGER, res.Msg.GetReleaseNotice())

	res, err = (&Browser{Releases: knownRelease{}}).Settings(ctx, connect.NewRequest(&sniffarrv1.SettingsRequest{}))
	require.NoError(t, err)
	require.Nil(t, res.Msg.GetLatestRelease(), "a check that knows none reports none")
}

func TestBrowserSettingsFeatures(t *testing.T) {
	ctx := context.Background()
	type state struct {
		Feature   sniffarrv1.Feature
		On, Works bool
		Setting   string
		Values    []string
	}
	features := func(b *Browser) []state {
		t.Helper()
		res, err := b.Settings(ctx, connect.NewRequest(&sniffarrv1.SettingsRequest{}))
		require.NoError(t, err)
		var out []state
		for _, f := range res.Msg.GetFeatures() {
			out = append(out, state{f.GetFeature(), f.GetOn(), f.GetWorks(), f.GetSetting(), f.GetValues()})
		}
		return out
	}
	off := []state{
		{Feature: sniffarrv1.Feature_FEATURE_CLASSIFIER, Setting: "SNIFFARR_CLASSIFY_API_KEY"},
		{Feature: sniffarrv1.Feature_FEATURE_PROBES, Setting: "-probe"},
		{Feature: sniffarrv1.Feature_FEATURE_AUDIO, Setting: "SNIFFARR_AUDIO_LANGUAGES"},
	}
	require.Equal(t, off, features(&Browser{}), "every feature off, each with its setting")

	b := browser(t)
	b.Tasks, b.Classifier, b.Probes = tasks(t), true, true
	on := []state{
		{Feature: sniffarrv1.Feature_FEATURE_CLASSIFIER, On: true, Works: true, Setting: "SNIFFARR_CLASSIFY_API_KEY"},
		{Feature: sniffarrv1.Feature_FEATURE_PROBES, On: true, Setting: "-probe"},
		{Feature: sniffarrv1.Feature_FEATURE_AUDIO, On: true, Works: true, Setting: "SNIFFARR_AUDIO_LANGUAGES", Values: []string{"ru"}},
	}
	require.Equal(t, on, features(b), "probes do not work without a prober")

	b.Tasks.ProberSeen("a", now)
	on[1].Works = true
	require.Equal(t, on, features(b), "a prober seen")

	require.NoError(t, b.Store.AddTokens(ctx, now.Format(time.DateOnly), 750))
	on[0].Works = false
	require.Equal(t, on, features(b), "the classifier's budget is spent")

	b.DailyTokens = 0
	on[0].Works = true
	require.Equal(t, on, features(b), "no limit is never spent")

	b.ClassifierSetting = "SNIFFARR_CLASSIFY_URL"
	on[0].Setting = "SNIFFARR_CLASSIFY_URL"
	require.Equal(t, on, features(b), "the setting that turned the classifier on")
}

func TestBrowserStatsProbers(t *testing.T) {
	ctx := context.Background()
	b := browser(t)
	probers := func() int32 {
		t.Helper()
		res, err := b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{}))
		require.NoError(t, err)
		return res.Msg.GetProbers()
	}
	require.Zero(t, probers(), "no queue")
	b.Tasks = tasks(t)
	b.Tasks.ProberSeen("a", now.Add(-store.ProberWindow))
	b.Tasks.ProberSeen("b", now)
	require.Zero(t, probers(), "probes are off")
	b.Probes = true
	require.EqualValues(t, 2, probers(), "both within the window")
	b.Now = func() time.Time { return now.Add(time.Second) }
	require.EqualValues(t, 1, probers(), "one gone past it")
}
