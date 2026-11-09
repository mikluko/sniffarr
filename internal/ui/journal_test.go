package ui

import (
	"log/slog"
	"maps"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	libui "sarak.sh/mikluko/libarr/ui"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

// The journal's messages are the same in both languages.
func TestJournalMessages(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	keys := func(lang string) []string {
		var out []string
		for k := range r.Base(httptest.NewRequest("GET", "/t/x", nil), lang, "", nil, time.UTC).Messages {
			if strings.HasPrefix(k, "journal.") {
				out = append(out, k)
			}
		}
		slices.Sort(out)
		return out
	}
	en, ru := keys("en"), keys("ru")
	for _, k := range en {
		if !slices.Contains(ru, k) {
			t.Errorf("%s is not in Russian", k)
		}
	}
	for _, k := range ru {
		if !slices.Contains(en, k) {
			t.Errorf("%s is not in English", k)
		}
	}
}

// Every kind and outcome journalSpec marks up has its labels and those of its
// summary keys in both languages.
func TestJournalSpecMessages(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "ru"} {
		for _, m := range journalSpecMissing(r.Base(httptest.NewRequest("GET", "/t/x", nil), lang, "", nil, time.UTC)) {
			t.Errorf("%s: %s", lang, m)
		}
	}
}

// journalSpecMissing lists the labels of journalSpec the language lacks.
func journalSpecMissing(b Base) []string {
	var out []string
	for kind, outcomes := range journalSpec {
		if m := "journal.kind." + kind; !b.has(m) {
			out = append(out, m)
		}
		for outcome, o := range outcomes {
			if m := "journal.outcome." + kind + "." + outcome; !b.has(m) {
				out = append(out, m)
			}
			for _, k := range o.summary {
				if !b.has("journal.key."+kind+"."+k) && !b.has("journal.key."+k) {
					out = append(out, "journal.key."+k+" of "+kind+"/"+outcome)
				}
			}
		}
	}
	return out
}

func TestJournalLevel(t *testing.T) {
	var log strings.Builder
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))

	for _, c := range []struct {
		name   string
		e      JournalEntry
		want   Severity
		logged bool
	}{
		{"stored", JournalEntry{Kind: "fetched", Outcome: "stored"}, SeverityNormal, false},
		{"rejected", JournalEntry{Kind: "fetched", Outcome: "rejected"}, SeverityFinal, false},
		{"timeout", JournalEntry{Kind: "fetch_failed", Outcome: "timeout", Detail: map[string]any{"retry_at": 1.0}}, SeverityRetry, false},
		{"dropped", JournalEntry{Kind: "fetch_failed", Outcome: "dropped", Detail: map[string]any{"dead_until": 1.0}}, SeverityFinal, false},
		{"no peers", JournalEntry{Kind: "fetch_failed", Outcome: "no_peers", Detail: map[string]any{"dead_until": 1.0}}, SeverityFinal, false},
		{"a timeout kept out", JournalEntry{Kind: "fetch_failed", Outcome: "timeout", Detail: map[string]any{"dead_until": 1.0}}, SeverityRetry, true},
		{"no peers with a retry", JournalEntry{Kind: "fetch_failed", Outcome: "no_peers", Detail: map[string]any{"dead_until": 1.0, "retry_at": 1.0}}, SeverityFinal, true},
		{"dropped with neither", JournalEntry{Kind: "fetch_failed", Outcome: "dropped"}, SeverityFinal, true},
		{"classification failed", JournalEntry{Kind: "classified", Outcome: "failed"}, SeverityFinal, false},
		{"nothing matched", JournalEntry{Kind: "matched", Outcome: "none"}, SeverityRetry, false},
		{"a match failed", JournalEntry{Kind: "matched", Outcome: "error"}, SeverityRetry, false},
		{"a final probe", JournalEntry{Kind: "probed", Outcome: "timeout", Detail: map[string]any{"final": true}}, SeverityFinal, false},
		{"a probe to retry", JournalEntry{Kind: "probed", Outcome: "no_peers", Detail: map[string]any{"final": false}}, SeverityRetry, false},
		{"a probe without the flag", JournalEntry{Kind: "probed", Outcome: "timeout"}, SeverityFinal, true},
		{"tracks not read", JournalEntry{Kind: "probed", Outcome: "unknown"}, SeverityFinal, false},
		{"tracks read", JournalEntry{Kind: "probed", Outcome: "tracks"}, SeverityNormal, false},
		{"deleted", JournalEntry{Kind: "deleted", Outcome: "idle"}, SeverityFinal, false},
		{"unknown", JournalEntry{Kind: "fetched", Outcome: "novel"}, SeverityNormal, true},
	} {
		log.Reset()
		if got := c.e.Level(); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if logged := log.Len() > 0; logged != c.logged {
			t.Errorf("%s: logged %q, want a data error logged: %v", c.name, log.String(), c.logged)
		}
	}
}

func TestJournalSummary(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	en := r.Base(httptest.NewRequest("GET", "/t/x", nil), "en", "", nil, time.UTC)
	ru := r.Base(httptest.NewRequest("GET", "/t/x", nil), "ru", "", nil, time.UTC)
	const sibling = "3c1e5a07d94b28f6a1c0e7b35d29f48a6b0c7d13"
	for _, c := range []struct {
		name string
		b    Base
		e    JournalEntry
		want []string
	}{
		{"a count, then a value alone", en, JournalEntry{Kind: "fetched", Outcome: "stored", Detail: map[string]any{"name": "x", "size": 26629001216.0, "files": 14.0, "discovery": "announce"}},
			[]string{"14 files", "24.8 GB"}},
		{"labels before values, and a key with its colon", en, JournalEntry{Kind: "matched", Outcome: "arr", Detail: map[string]any{"tmdb_id": 693134.0, "type": "movie", "trigger": "arr"}},
			[]string{"TMDB 693134", "movie", "asked by: Radarr/Sonarr"}},
		{"a label of the kind's own", ru, JournalEntry{Kind: "classified", Outcome: "reuse", Detail: map[string]any{"kind": "movie", "from": sibling}},
			[]string{"фильм", "у 3c1e5a07"}},
		{"tracks read", ru, JournalEntry{Kind: "probed", Outcome: "tracks", Detail: map[string]any{"tracks": []any{"video::hevc", "audio:rus:eac3"}}},
			[]string{"2 дорожки"}},
		{"tracks listed in part", ru, JournalEntry{Kind: "probed", Outcome: "tracks", Detail: map[string]any{"tracks": []any{"video::hevc"}, "tracks_total": 40.0}},
			[]string{"40 дорожек"}},
	} {
		var got []string
		for _, p := range c.b.JournalSummary(c.e) {
			got = append(got, p.Text())
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestJournalFields(t *testing.T) {
	e := JournalEntry{Kind: "probed", Detail: map[string]any{
		"retry_at": 1762500000.0, "downloaded": 4194304.0, "leases": 2.0, "attempt": 3.0, "tmdb_id": 949.0, "share": 0.5,
		"tracks": []any{"video::hevc", "audio:rus:eac3", "odd"}, "audio": map[string]any{"ru": 1.0},
	}}
	want := []JournalField{
		{"attempt", int64(3)},
		{"audio", map[string]any{"ru": 1.0}},
		{"downloaded", Bytes(4194304)},
		{"leases", Times(2)},
		{"retry_at", time.Unix(1762500000, 0)},
		{"share", 0.5},
		{"tmdb_id", ID(949)},
		{"tracks", []any{Track{"video", "", "hevc"}, Track{"audio", "rus", "eac3"}, "odd"}},
	}
	if got := e.Fields(); !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
	if got, want := e.Tracks(), []Track{{"video", "", "hevc"}, {"audio", "rus", "eac3"}}; !slices.Equal(got, want) {
		t.Errorf("tracks %v, want %v", got, want)
	}
}

func TestJournalDetail(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("CEST", 2*3600)
	base := func(lang string) Base {
		return r.Base(httptest.NewRequest("GET", "/t/x", nil), lang, "", []string{"ru"}, loc)
	}
	var missing []string
	libui.OnMissing = func(lang, key string) { missing = append(missing, lang+":"+key) }
	defer func() { libui.OnMissing = nil }()

	for _, c := range []struct {
		name, lang string
		e          JournalEntry
		want       []DetailPair
	}{
		{"probed", "en", JournalEntry{Kind: "probed", Detail: map[string]any{"retry_at": 1762500000.0, "attempt": 2.0, "final": true, "downloaded": 4194304.0,
			"tracks": []any{"video::hevc", "audio:rus:eac3"}, "audio": map[string]any{"ru": 0.88, "en": 0.0}, "path": "a.mkv", "novel": "x"}},
			[]DetailPair{
				{"attempt", "2", ""},
				{"audio track probability", "English 0%, Russian 88%", ""},
				{"downloaded", "4.0\u00a0MB", ""},
				{"attempts ran out", "yes", ""},
				{"novel", "x", ""},
				{"file", "a.mkv", ""},
				{"next try", "Nov 7, 2025, 09:20", ""},
				{"tracks", "video hevc, audio rus eac3", ""},
			}},
		{"probed in Russian", "ru", JournalEntry{Kind: "probed", Detail: map[string]any{"final": false, "downloaded": 4194304.0, "audio": map[string]any{"ru": 0.88},
			"tracks": []any{"video::hevc", "audio:rus:eac3", "subtitle:eng:subrip", "other:x:y"}}},
			[]DetailPair{{"вероятность звуковой дорожки", "русская 88%", ""}, {"скачано", "4,0\u00a0МБ", ""}, {"попытки исчерпаны", "нет", ""},
				{"дорожки", "видео hevc, звук rus eac3, субтитры eng subrip, other x y", ""}}},
		{"probed quality", "ru", JournalEntry{Kind: "probed", Detail: map[string]any{"quality": "sd"}}, []DetailPair{{"качество", "SD", ""}}},
		{"a codec with colons", "en", JournalEntry{Kind: "probed", Detail: map[string]any{"tracks": []any{"audio:eng:A:B"}}}, []DetailPair{{"tracks", "audio eng A:B", ""}}},
		{"stored", "en", JournalEntry{Kind: "fetched", Detail: map[string]any{"kind": "movie", "kind_layer": "reuse", "discovery": "announce", "size": 26629001216.0, "leases": 3.0}},
			[]DetailPair{{"discovered by", "announce", ""}, {"kind", "movie", ""}, {"kind decided by", "same title", ""}, {"leased", "3 times", ""}, {"size", "24.8\u00a0GB", ""}}},
		{"leased twice in Russian", "ru", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 2.0}}, []DetailPair{{"выдана", "2 раза", ""}}},
		{"leased 5 times in Russian", "ru", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 5.0}}, []DetailPair{{"выдана", "5 раз", ""}}},
		{"leased 12 times in Russian", "ru", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 12.0}}, []DetailPair{{"выдана", "12 раз", ""}}},
		{"leased 21 times in Russian", "ru", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 21.0}}, []DetailPair{{"выдана", "21 раз", ""}}},
		{"leased 22 times in Russian", "ru", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 22.0}}, []DetailPair{{"выдана", "22 раза", ""}}},
		{"leased once", "en", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 1.0}}, []DetailPair{{"leased", "1 time", ""}}},
		{"an unknown value", "en", JournalEntry{Kind: "fetched", Detail: map[string]any{"kind": "opera", "reason": "nomedia"}},
			[]DetailPair{{"kind", "opera", ""}, {"reason", "no video or audio files", ""}}},
		{"small in the unit the card prints", "en", JournalEntry{Kind: "fetched", Detail: map[string]any{"reason": "small", "size": 1048576.0}},
			[]DetailPair{{"reason", "under 50 MB", ""}, {"size", "1.0\u00a0MB", ""}}},
		{"classified", "ru", JournalEntry{Kind: "classified", Detail: map[string]any{"source": "web", "source_confidence": 0.8, "trigger": "search"}},
			[]DetailPair{{"источник", "web", ""}, {"уверенность в источнике", "0,8", ""}, {"кто запросил", "поиск", ""}}},
		{"the classifier's disc image", "en", JournalEntry{Kind: "classified", Detail: map[string]any{"source": "disc_image"}}, []DetailPair{{"source", "disc image", ""}}},
		{"the classifier's encode", "ru", JournalEntry{Kind: "classified", Detail: map[string]any{"source": "encode"}}, []DetailPair{{"источник", "rip", ""}}},
		{"arr", "en", JournalEntry{Kind: "arr", Detail: map[string]any{"app": "sonarr", "source": "history", "recorded_at": 1762500000.0}},
			[]DetailPair{{"app", "Sonarr", ""}, {"recorded", "Nov 7, 2025, 09:20", ""}, {"came by", "history import", ""}}},
		{"listed", "en", JournalEntry{Kind: "listed", Detail: map[string]any{"source": "spidarr", "url": "https://tracker.example/t/1", "categories": []any{5040.0, 5000.0},
			"published_at": 1762500000.0, "imdb": "tt0078748", "tvdb_id": 121361.0}},
			[]DetailPair{{"categories", "5040, 5000", ""}, {"IMDb ID", "tt0078748", ""}, {"published", "Nov 7, 2025, 09:20", ""}, {"Torznab source", "spidarr", ""},
				{"TVDB ID", "121361", ""}, {"page", "spidarr", "https://tracker.example/t/1"}}},
		{"a listing's page that is no web page", "en", JournalEntry{Kind: "listed", Detail: map[string]any{"url": "ftp://x"}}, []DetailPair{{"page", "ftp://x", ""}}},
		{"an arr match", "en", JournalEntry{Kind: "matched", Detail: map[string]any{"event_at": 1762500000.0, "trigger": "arr"}},
			[]DetailPair{{"time of the Radarr/Sonarr event", "Nov 7, 2025, 09:20", ""}, {"asked by", "Radarr/Sonarr", ""}}},
		{"an earlier match", "en", JournalEntry{Kind: "matched", Detail: map[string]any{"layer": "tmdb", "key_matched_at": 1762500000.0}},
			[]DetailPair{{"match made", "Nov 7, 2025, 09:20", ""}, {"matched by", "TMDB search", ""}}},
		{"an earlier match in Russian", "ru", JournalEntry{Kind: "matched", Detail: map[string]any{"layer": "arr", "key_matched_at": 1762500000.0}},
			[]DetailPair{{"сопоставлено", "07.11.2025 09:20", ""}, {"чем сопоставлено", "Radarr/Sonarr", ""}}},
		{"promoted", "ru", JournalEntry{Kind: "sighted", Detail: map[string]any{"from": "sample", "to": "arr"}}, []DetailPair{{"откуда", "sample", ""}, {"куда", "Radarr/Sonarr", ""}}},
		{"reuse", "en", JournalEntry{Kind: "classified", Detail: map[string]any{"from": "8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52"}},
			[]DetailPair{{"kind taken from", "8f3c2a91", "/t/8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52"}}},
		{"reuse from what is no infohash", "en", JournalEntry{Kind: "classified", Detail: map[string]any{"from": "3c1e"}}, []DetailPair{{"kind taken from", "3c1e", ""}}},
		{"a search", "en", JournalEntry{Kind: "probe_wanted", Detail: map[string]any{"t": "tvsearch", "id": "tmdbid:949", "query": "heat"}},
			[]DetailPair{{"searched by", "TMDB 949", ""}, {"query", "heat", ""}, {"search type", "series", ""}}},
		{"a search by IMDb and TVDB", "ru", JournalEntry{Kind: "probe_wanted", Detail: map[string]any{"t": "movie", "id": "imdbid:tt0113277"}},
			[]DetailPair{{"искали по", "IMDb tt0113277", ""}, {"тип поиска", "фильм", ""}}},
		{"a search by text", "en", JournalEntry{Kind: "probe_wanted", Detail: map[string]any{"t": "search", "id": "kpid:5"}},
			[]DetailPair{{"searched by", "kpid:5", ""}, {"search type", "text", ""}}},
		{"numbers grouped, IDs not", "ru", JournalEntry{Kind: "matched", Detail: map[string]any{"tmdb_id": 693134.0, "previous": 41000.0, "peers_week": 1284.0}},
			[]DetailPair{{"пиров за 7 дней", "1 284", ""}, {"ID прежнего произведения", "41000", ""}, {"TMDB ID", "693134", ""}}},
		{"numbers grouped in English", "en", JournalEntry{Kind: "fetched", Detail: map[string]any{"files": 1284.0}}, []DetailPair{{"files", "1,284", ""}}},
		{"a time on the entry's day", "en", JournalEntry{Kind: "leased", At: time.Date(2026, 10, 18, 19, 0, 0, 0, time.UTC),
			Detail: map[string]any{"lease_until": float64(time.Date(2026, 10, 18, 19, 1, 0, 0, time.UTC).Unix())}}, []DetailPair{{"leased until", "21:01", ""}}},
		{"a time on the entry's day in the zone, not in UTC", "en", JournalEntry{Kind: "leased", At: time.Date(2026, 10, 18, 22, 30, 0, 0, time.UTC),
			Detail: map[string]any{"lease_until": float64(time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC).Unix())}}, []DetailPair{{"leased until", "03:00", ""}}},
		{"a time on the next day in the zone", "en", JournalEntry{Kind: "leased", At: time.Date(2026, 10, 18, 21, 30, 0, 0, time.UTC),
			Detail: map[string]any{"lease_until": float64(time.Date(2026, 10, 18, 22, 30, 0, 0, time.UTC).Unix())}}, []DetailPair{{"leased until", "Oct 19, 00:30", ""}}},
		{"an infohash under any key", "en", JournalEntry{Kind: "fetched", Detail: map[string]any{"sibling": "8F3C2A91D07E4B6C5A1E9F20B37D4C8E6A0F1B52"}},
			[]DetailPair{{"sibling", "8f3c2a91", "/t/8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52"}}},
	} {
		got := base(c.lang).JournalDetail(c.e)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if len(missing) > 0 {
		t.Errorf("untranslated keys: %v", missing)
	}
}

// With no dubbing language configured the journal drops the audio
// probabilities, in a row's summary and in its details; the tracks stay.
func TestJournalAudioOff(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	classified := JournalEntry{Kind: "classified", Outcome: "classifier", Detail: map[string]any{"audio": map[string]any{"ru": 0.88}, "source": "web"}}
	probed := JournalEntry{Kind: "probed", Outcome: "tracks", Detail: map[string]any{"audio": map[string]any{"ru": 0.88}, "tracks": []any{"audio:rus:eac3"}}}
	for _, c := range []struct {
		name  string
		langs []string
		audio bool
	}{{"on", []string{"ru"}, true}, {"off", nil, false}} {
		b := r.Base(httptest.NewRequest("GET", "/t/x", nil), "en", "", c.langs, time.UTC)
		shows := func(ps []DetailPair) bool {
			for _, p := range ps {
				if strings.Contains(p.Value, "88%") {
					return true
				}
			}
			return false
		}
		var summary []DetailPair
		for _, s := range b.JournalSummary(classified) {
			summary = append(summary, s.DetailPair)
		}
		if got := shows(summary); got != c.audio {
			t.Errorf("%s: summary shows audio %v, want %v", c.name, got, c.audio)
		}
		if got := shows(b.JournalDetail(classified)); got != c.audio {
			t.Errorf("%s: classified details show audio %v, want %v", c.name, got, c.audio)
		}
		rows := b.JournalRows([]JournalEntry{probed})
		if len(rows) != 1 || len(rows[0].Tracks) != 1 {
			t.Fatalf("%s: rows %+v, want one row with its track", c.name, rows)
		}
		if got := shows(rows[0].Details); got != c.audio {
			t.Errorf("%s: probed row details show audio %v, want %v", c.name, got, c.audio)
		}
	}
}

// journalSpec marks up exactly the kinds and outcomes the store journals.
func TestJournalSpecMatchesStore(t *testing.T) {
	spec := JournalPairs()
	for kind, outcomes := range store.JournalOutcomes {
		want := slices.Sorted(slices.Values(outcomes))
		if got := spec[kind]; !slices.Equal(got, want) {
			t.Errorf("journalSpec[%q] has %v, the store writes %v", kind, got, want)
		}
	}
	for kind := range spec {
		if _, ok := store.JournalOutcomes[kind]; !ok {
			t.Errorf("journalSpec has %q, which the store does not write", kind)
		}
	}
}

// JournalPairs is every kind journalSpec marks up with its outcomes, sorted.
func JournalPairs() map[string][]string {
	out := make(map[string][]string, len(journalSpec))
	for kind, outcomes := range journalSpec {
		out[kind] = slices.Sorted(maps.Keys(outcomes))
	}
	return out
}
