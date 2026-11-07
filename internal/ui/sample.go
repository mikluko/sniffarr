package ui

import (
	"net/url"
	"sort"
	"time"
)

// Sample models used by tests and cmd/preview. Not for production.

func fp(f float64) *float64 { return &f }

const gb = int64(1) << 30
const mb = int64(1) << 20

// SampleTorrents mixes matched and unmatched torrents and applies f roughly.
func SampleTorrents(b Base, q string, f Filters) TorrentsPage {
	if f.VoiceLang == "" {
		f.VoiceLang = DefaultVoiceLang(b.VoiceLangs, b.Lang)
	}
	if f.Voice == "" {
		f.Voice = "any"
	}
	if f.Sort == "" {
		f.Sort = "peers"
	}
	at := func(d time.Duration) time.Time { return b.Now.Add(-d) }
	day := 24 * time.Hour
	ru := func(marker bool, mark string, pr *float64) []Audio {
		return []Audio{{Lang: "ru", Marker: marker, Mark: mark, Probability: pr}}
	}
	spider := spiderMan.summary(b.Lang, 1)
	short := sampleMovies[1].summary(b.Lang, 2)
	fallout := sampleSeries[0].summary(b.Lang, 1)
	odyssey := sampleMovies[4].summary(b.Lang, 5)
	all := []Row{
		{InfoHash: "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b001", Name: "Spider-Man.Brand.New.Day.2026.2160p.WEB-DL.DDP5.1.HDR", Work: &spider, Peers: 15310, Size: 22 * gb, Kind: "movie", Layer: "rules", Quality: "2160p", Audio: ru(false, "", fp(.03)), Source: "web", FirstSeen: at(30 * day), LastSeen: at(2 * time.Minute)},
		{InfoHash: "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b002", Name: "Человек-паук: Новый день (2026) WEB-DL 1080p", Work: &spider, Peers: 9800, Size: 9 * gb, Kind: "movie", Layer: "title_repeat", Quality: "1080p", Audio: ru(false, "", fp(.88)), Source: "web", FirstSeen: at(28 * day), LastSeen: at(3 * time.Minute)},
		{InfoHash: "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b003", Name: "Fallout.S02E05.1080p.WEB.h264-ETHEL[EZTVx.to].mkv", Work: &fallout, Peers: 7420, Size: 3 * gb, Kind: "series", Layer: "rules", Quality: "1080p", Audio: ru(true, "LostFilm", fp(.95)), Source: "web", FirstSeen: at(3 * day), LastSeen: at(3 * time.Minute)},
		{InfoHash: "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b004", Name: "Игра на понижение.2015.WEB-DL.1080р.Open Matte.mkv", Work: &short, Peers: 4630, Size: 9 * gb, Kind: "movie", Layer: "classifier", Confidence: fp(.93), Quality: "1080p", Audio: ru(false, "", fp(.88)), Source: "web", FirstSeen: at(56 * day), LastSeen: at(5 * time.Minute)},
		{InfoHash: "e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e001", Name: "Narcissists.Playbook.2026.1080p.WEB-DL.AAC2.0.H264-Maraschino.mkv", Peers: 2950, Size: 3 * gb, Kind: "movie", Layer: "rules", Quality: "1080p", Audio: ru(false, "", fp(.04)), Source: "web", FirstSeen: at(9 * day), LastSeen: at(3 * time.Minute)},
		{InfoHash: "e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e002", Name: "Law.and.Order.SVU.S28E02.720p.x264-FENiX", Peers: 2410, Size: 1 * gb, Kind: "series", Layer: "rules", Quality: "720p", Audio: ru(false, "", fp(.1)), Source: "web", FirstSeen: at(30 * day), LastSeen: at(4 * time.Minute)},
		{InfoHash: "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b005", Name: "Одиссея [The Odyssey] (2026)", Work: &odyssey, Peers: 1940, Size: 6 * gb, Kind: "movie", Layer: "classifier", Confidence: fp(.88), Audio: ru(false, "", fp(.91)), Source: "web", FirstSeen: at(4 * day), LastSeen: at(6 * time.Minute)},
		{InfoHash: "e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e003", Name: "El final de Oak Street (2026) [Bluray 1080p][Esp]", Peers: 1180, Size: 9 * gb, Kind: "movie", Layer: "title_repeat", Quality: "1080p", Audio: ru(false, "", fp(.02)), Source: "rip", FirstSeen: at(12 * day), LastSeen: at(9 * time.Minute)},
		{InfoHash: "e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e004", Name: "Сборник - Новогодние хиты (2024) MP3", Peers: 410, Size: 400 * mb, Kind: "other", Layer: "classifier", Confidence: fp(.79), Source: "other", FirstSeen: at(2 * day), LastSeen: at(25 * time.Minute)},
		{InfoHash: "e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e005", Name: "ubn2410_x7f.part03", Peers: 95, Size: 4 * gb, Kind: "unknown", Layer: "classifier", Confidence: fp(.31), Source: "other", FirstSeen: at(1 * day), LastSeen: at(31 * time.Minute)},
		{InfoHash: "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b006", Name: "Человек-паук. Новый день (2026) BDRip 1080p seleZen", Work: &spider, Peers: 11, Size: 10 * gb, Kind: "movie", Layer: "title_repeat", Quality: "1080p", Audio: ru(true, "seleZen", fp(.97)), Source: "rip", FirstSeen: at(21 * day), LastSeen: at(40 * time.Minute)},
	}
	var rows []Row
	for _, r := range all {
		if f.Kind != "" && r.Kind != f.Kind || f.Quality != "" && r.Quality != f.Quality || f.Source != "" && r.Source != f.Source || f.Layer != "" && r.Layer != f.Layer {
			continue
		}
		if f.Matched == "yes" && r.Work == nil || f.Matched == "no" && r.Work != nil {
			continue
		}
		if f.Voice == "explicit" || f.Voice == "p50" {
			a, _ := AudioOf(r.Audio, f.VoiceLang)
			if !(a.Marker || f.Voice == "p50" && a.Likely()) {
				continue
			}
		}
		rows = append(rows, r)
	}
	return TorrentsPage{Base: b, Q: q, Filters: f, Results: Results{Total: int64(len(rows)), Shown: len(rows), Rows: rows}, ResetURL: sampleReset(q)}
}

// sampleReset is the torrents page of the query q with no filter.
func sampleReset(q string) string {
	if q == "" {
		return "/torrents"
	}
	return "/torrents?q=" + url.QueryEscape(q)
}

func SampleTop(b Base, kind string, all bool) TopPage {
	return SampleTopFiltered(b, kind, "", "any", all)
}

// SampleTopFiltered applies the dub filter the way the backend is expected to:
// with "explicit" or "p50" only matching releases count, titles re-rank or drop out.
func SampleTopFiltered(b Base, kind, vl, mode string, all bool) TopPage {
	day := 24 * time.Hour
	list := sampleMovies
	if kind == "series" {
		list = sampleSeries
	} else {
		kind = "movie"
	}
	if mode == "" {
		mode = "any"
	}
	p := TopPage{Base: b, Kind: kind, Window: "7d", VoiceLang: vl, VoiceMode: mode, Computed: b.Now.Add(-5 * time.Minute), Limit: 100, All: all}
	if p.VoiceLang == "" {
		p.VoiceLang = DefaultVoiceLang(b.VoiceLangs, b.Lang)
	}
	if len(b.VoiceLangs) == 0 {
		p.VoiceLang, p.VoiceMode = "", "any"
	}
	for i, x := range list {
		wk := Work{WorkSummary: x.summary(b.Lang, i+1), PrevRank: x.prev,
			Audio: sampleAudio(x, i, b.VoiceLangs), Sources: x.sources, FirstSeen: b.Now.Add(-time.Duration(x.ageDays) * day)}
		if p.VoiceMode == "explicit" || p.VoiceMode == "p50" {
			a := WorkAudioOf(wk.Audio, p.VoiceLang)
			n := a.Marked
			if p.VoiceMode == "p50" {
				n += a.Likely
			}
			if n == 0 {
				continue
			}
			if x.rels == 95 && p.VoiceMode == "explicit" {
				wk.Peers = 16 // the two marked releases: 11 + 5
			} else {
				wk.Peers = wk.Peers * int64(n) / int64(wk.Releases) * 7 / 10
			}
		}
		p.Works = append(p.Works, wk)
	}
	sort.SliceStable(p.Works, func(i, j int) bool { return p.Works[i].Peers > p.Works[j].Peers })
	for i := range p.Works {
		p.Works[i].Rank = i + 1
	}
	return p
}

// SampleWork builds a title page for id (SampleSpiderID, SampleFalloutID,
// SampleTMDBOnlyID, or any catalog title) with the release filters applied.
func SampleWork(b Base, id string, f ReleaseFilters) WorkPage {
	s, i, ok := findTitle(id)
	if !ok {
		s, i = spiderMan, 0
	}
	if f.VoiceLang == "" {
		f.VoiceLang = DefaultVoiceLang(b.VoiceLangs, b.Lang)
	}
	if f.Voice == "" {
		f.Voice = "any"
	}
	if f.Sort == "" {
		f.Sort = "peers"
	}
	w := s.summary(b.Lang, s.ranks[1])
	if w.Rank == 0 && s.rels > 0 {
		w.Rank = i + 1
	}
	p := WorkPage{Base: b, Back: "/top?kind=" + w.Kind, Work: w, Audio: sampleAudio(s, i, b.VoiceLangs)}
	if s.rels == 0 {
		return p
	}
	p.Ranks = s.ranks
	if p.Ranks == [3]int{} {
		p.Ranks = [3]int{i + 2, i + 1, i + 1}
	}
	p.Peers = PeerCounts{Day: s.peers / 6, Week: s.peers, Month: s.peers * 3}
	all := sampleReleases(b, s)
	var seasons []int
	seen := map[int]bool{}
	for _, r := range all {
		if r.Season > 0 && !seen[r.Season] {
			seen[r.Season] = true
			seasons = append(seasons, r.Season)
		}
	}
	sort.Ints(seasons)
	rows := filterReleases(all, f)
	t := ReleaseTable{Path: w.Path(), Kind: s.kind, Filters: f, Seasons: seasons, Total: len(all), Matched: len(rows)}
	if len(rows) > 50 {
		t.NextURL = w.Path() + "?o=50"
		rows = rows[:50]
	}
	t.Rows, t.Shown = rows, len(rows)
	p.Releases = t
	return p
}

func sampleReleases(b Base, s titleSample) []Release {
	day := 24 * time.Hour
	t := s.m("en")
	var out []Release
	quals := []string{"1080p", "2160p", "1080p", "720p", "1080p", "sd", "2160p", "720p", ""}
	srcs := []string{"web", "web", "rip", "rip", "web", "rip", "remux", "web", "disc"}
	sizes := map[string]int64{"2160p": 22 * gb, "1080p": 9 * gb, "720p": 4 * gb, "sd": 1 * gb, "": 46 * gb}
	peers := s.peers / 4
	for k := 0; k < s.rels; k++ {
		q, src := quals[k%len(quals)], srcs[k%len(srcs)]
		if src == "disc" {
			q = ""
		}
		r := Release{InfoHash: "a1c0ffee" + pad(k), Quality: q, Source: src, Size: sizes[q] + int64(k%5)*gb/3, Peers: peers,
			FirstSeen: b.Now.Add(-time.Duration(3+k*5%s.ageDays+1) * day)}
		peers = peers * 83 / 100
		if min := int64(s.rels - k); peers < min {
			peers = min
		}
		ru := Audio{Lang: "ru"}
		switch {
		case k%11 == 3:
			ru.Probability = fp(.88)
		case k%4 == 1:
			ru.Probability = fp(.12)
		case k%4 == 2:
			ru.Probability = fp(.03)
		}
		if s.kind == "series" {
			r.Season = 1 + k%t.Seasons
			if t.Seasons == 0 {
				r.Season = 1 + k%2
			}
			if k%6 != 0 {
				r.Episode = 1 + k%8
			}
		}
		r.Name = sampleName(s, t, b, k, r)
		r.Audio = []Audio{ru}
		for _, l := range b.VoiceLangs {
			if l == "uk" && k%5 == 0 {
				r.Audio = append(r.Audio, Audio{Lang: "uk", Probability: fp(.71)})
			}
		}
		out = append(out, r)
	}
	// The 95-release title: the two explicitly marked releases have 16 peers between them.
	if s.rels == 95 {
		out[93].Audio[0] = Audio{Lang: "ru", Marker: true, Mark: "seleZen", Probability: fp(.97)}
		out[93].Peers, out[93].Name = 11, "Человек-паук. Новый день (2026) BDRip 1080p seleZen"
		out[94].Audio[0] = Audio{Lang: "ru", Marker: true, Mark: "ExKinoRay", Probability: fp(.96)}
		out[94].Peers, out[94].Name = 5, "Chelovek-pauk.Novyj.den.2026.HDRip.ExKinoRay"
	} else if s.voice.Explicit {
		for k := 0; k < len(out); k += 3 {
			out[k].Audio[0] = Audio{Lang: "ru", Marker: true, Mark: markPool[k%len(markPool)], Probability: fp(.95)}
		}
	}
	return out
}

func pad(k int) string {
	s := "00000000000000000000000000000000"
	n := []byte(s)
	d := []byte(strconvItoa(k))
	copy(n[len(n)-len(d):], d)
	return string(n)
}

func strconvItoa(k int) string {
	if k == 0 {
		return "0"
	}
	var b []byte
	for k > 0 {
		b = append([]byte{byte('0' + k%10)}, b...)
		k /= 10
	}
	return string(b)
}

// sampleName alternates the spellings of a title, the point of merging.
func sampleName(s titleSample, t *TMDBWork, b Base, k int, r Release) string {
	ru := s.m("ru").Title
	q := r.Quality
	if q == "" {
		q = "BD50"
	}
	se := ""
	if s.kind == "series" {
		se = " " + b.SE(r.Season, r.Episode)
	}
	switch k % 3 {
	case 0:
		return dotted(t.Title) + dottedSE(se) + "." + strconvItoa(t.Year) + "." + q + "." + srcTag(r.Source)
	case 1:
		return ru + se + " (" + strconvItoa(t.Year) + ") " + srcTag(r.Source) + " " + q
	}
	return t.Title + se + " " + strconvItoa(t.Year) + " " + q + " " + srcTag(r.Source)
}

func dotted(s string) string {
	out := []byte{}
	for _, c := range []byte(s) {
		switch c {
		case ' ':
			if len(out) > 0 {
				out = append(out, '.')
			}
		case ':', ',':
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

func srcTag(src string) string {
	return map[string]string{"web": "WEB-DL", "rip": "BDRip", "remux": "REMUX", "disc": "Blu-ray"}[src]
}

func filterReleases(all []Release, f ReleaseFilters) []Release {
	var out []Release
	for _, r := range all {
		if f.Quality != "" && r.Quality != f.Quality || f.Source != "" && r.Source != f.Source {
			continue
		}
		if f.Season != "" && strconvItoa(r.Season) != f.Season || f.Episode != "" && strconvItoa(r.Episode) != f.Episode {
			continue
		}
		if f.Voice == "explicit" || f.Voice == "p50" {
			a, _ := AudioOf(r.Audio, f.VoiceLang)
			if !(a.Marker || f.Voice == "p50" && a.Likely()) {
				continue
			}
		}
		out = append(out, r)
	}
	switch f.Sort {
	case "size":
		sort.SliceStable(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	case "first_seen":
		sort.SliceStable(out, func(i, j int) bool { return out[i].FirstSeen.After(out[j].FirstSeen) })
	default:
		sort.SliceStable(out, func(i, j int) bool { return out[i].Peers > out[j].Peers })
	}
	return out
}

// SampleTorrent cases: "poster", "noposter" (matched titles), "nopeers" (a
// matched title without peers over 7 days), "unmatched", "unchecked",
// "journal" (a torrent the store keeps only the journal of).
func SampleTorrent(b Base, all bool, cs string) TorrentPage {
	if cs == "journal" {
		const h = "3c1e0d9a7b2f48e6a5c4d3b2a1f0e9d8c7b6a5f4"
		return TorrentPage{Base: b, Torrent: Torrent{InfoHash: h, JournalOnly: true, Journal: []JournalEntry{
			{At: b.Now.Add(-3 * time.Hour), Kind: "sighted", Outcome: "queued", Detail: map[string]any{"discovery": "announce"}},
			{At: b.Now.Add(-3*time.Hour + time.Minute), Kind: "leased", Outcome: "fetch", Detail: map[string]any{"owner": 2.0, "attempt": 1.0}},
			{At: b.Now.Add(-2 * time.Hour), Kind: "fetched", Outcome: "rejected", Detail: map[string]any{"reason": "adult", "name": "xxx", "size": 1073741824.0}},
		}}}
	}
	name, query := "Игра на понижение.2015.WEB-DL.1080р.Open Matte.mkv", "Игра на понижение 2015"
	var work *WorkSummary
	var tmdb *TMDBWork
	switch cs {
	case "noposter":
		w := sampleMovies[4].summary(b.Lang, 5)
		work, name, query = &w, "Одиссея [The Odyssey] (2026)", "Одиссея 2026"
	case "unmatched":
		tmdb = &TMDBWork{State: TMDBUnmatched, CheckedAt: checkedAt}
		name, query = "Narcissists.Playbook.2026.1080p.WEB-DL.AAC2.0.H264-Maraschino.mkv", "Narcissists Playbook 2026"
	case "unchecked":
		name, query = "Fresh.Title.2026.1080p.WEB-DL.H264", "Fresh Title 2026"
	default:
		w := sampleMovies[1].summary(b.Lang, 2)
		work = &w
	}
	if work != nil {
		tmdb = work.TMDB
	}
	peers := PeerCounts{Day: 1960, Week: 12480, Month: 41300}
	if cs == "nopeers" {
		peers = PeerCounts{}
	}
	files := []File{
		{"Dune.Part.Two.2024.2160p.WEB-DL.HDR.mkv", 26411 * mb},
		{"Sample/Dune.Part.Two.sample.mkv", 118 * mb},
		{"Covers/poster.jpg", 2 * mb}, {"Covers/back.jpg", 2 * mb},
		{"Subs/Rus.Full.srt", 100352}, {"Subs/Eng.SDH.srt", 106496},
		{"Subs/Eng.Full.srt", 98304}, {"Subs/Ukr.Full.srt", 99328},
		{"Subs/Kaz.Full.srt", 97280}, {"Subs/Ger.Full.srt", 95232},
		{"Subs/Spa.Full.srt", 94208}, {"Subs/Fre.Full.srt", 93184},
		{"Subs/Rus.Forced.srt", 12288}, {"info.nfo", 6144},
	}
	shown := files
	if !all {
		shown = files[:8]
	}
	back := "/?tab=torrents&q=" + query
	if work != nil {
		back = work.Path()
	}
	return TorrentPage{Base: b, BackURL: back, Torrent: Torrent{
		InfoHash: "8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52",
		Name:     name,
		Size:     26629001216, FileCount: 14,
		FirstSeen: b.Now.Add(-41 * time.Hour), LastSeen: b.Now.Add(-3 * time.Minute),
		DiscoveredVia: "announce", TMDBQuery: query,
		TMDB:  tmdb,
		Peers: peers,
		Work:  work, RankInWork: 1,
		Kind:    Answer{Value: "movie", Layer: "classifier", Confidence: fp(.93)},
		Source:  Answer{Value: "web", Layer: "rules"},
		Audio:   []Audio{{Lang: "ru", Script: "cyrillic", Probability: fp(.88)}},
		Rules:   []RuleHit{{"source.web", "WEB-DL"}, {"quality.1080p", "1080р"}},
		Journal: sampleJournal(b.Now.Add(-41*time.Hour), name),
		Files:   FileList{InfoHash: "8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52", Files: shown, Stored: len(files), All: all},
	}}
}

// sampleJournal is a torrent's life from its metadata at the time: numbers
// are float64 and moments Unix seconds, as the API's JSON decodes.
func sampleJournal(at time.Time, name string) []JournalEntry {
	sec := func(t time.Time) float64 { return float64(t.Unix()) }
	const sibling = "3c1e5a07d94b28f6a1c0e7b35d29f48a6b0c7d13"
	return []JournalEntry{
		{At: at.Add(-8 * time.Minute), Kind: "feed", Outcome: "queued", Detail: map[string]any{"source": "rutracker", "title": name, "url": "https://rutracker.example/t/6543210",
			"category": "f-1457", "size": 26629001216.0, "published_at": sec(at.Add(-9 * time.Minute))}},
		{At: at.Add(-7 * time.Minute), Kind: "feed", Outcome: "already_queued", Detail: map[string]any{"source": "rutor", "title": name, "url": "https://rutor.example/torrent/987654",
			"size": 26629001216.0, "published_at": sec(at.Add(-7 * time.Minute))}},
		{At: at.Add(-6 * time.Minute), Kind: "sighted", Outcome: "queued", Detail: map[string]any{"discovery": "announce"}},
		{At: at.Add(-5 * time.Minute), Kind: "leased", Outcome: "fetch", Detail: map[string]any{"attempt": 1.0, "lease_until": sec(at.Add(-4 * time.Minute))}},
		{At: at.Add(-3 * time.Minute), Kind: "fetch_failed", Outcome: "timeout", Detail: map[string]any{"attempt": 1.0, "retry_at": sec(at.Add(-2 * time.Minute)), "leases": 2.0}},
		{At: at.Add(-2 * time.Minute), Kind: "leased", Outcome: "fetch", Detail: map[string]any{"attempt": 2.0, "lease_until": sec(at.Add(-time.Minute))}},
		{At: at, Kind: "fetched", Outcome: "stored", Detail: map[string]any{"name": name, "size": 26629001216.0, "files": 14.0, "discovery": "announce",
			"queued_at": sec(at.Add(-6 * time.Minute)), "leases": 2.0}},
		{At: at.Add(30 * time.Second), Kind: "classified", Outcome: "reuse", Detail: map[string]any{"kind": "movie", "from": sibling}},
		{At: at.Add(time.Minute), Kind: "classified", Outcome: "jev", Detail: map[string]any{"source": "web", "source_confidence": 0.8,
			"audio": map[string]any{"ru": 0.88}, "trigger": "worker"}},
		{At: at.Add(2 * time.Minute), Kind: "matched", Outcome: "earlier", Detail: map[string]any{"tmdb_id": 318846.0, "type": "movie", "confidence": 0.8,
			"layer": "tmdb", "key_matched_at": sec(at.Add(-30 * time.Hour))}},
		{At: at.Add(time.Hour), Kind: "matched", Outcome: "arr", Detail: map[string]any{"tmdb_id": 693134.0, "type": "movie", "trigger": "arr", "previous": 41.0,
			"event_at": sec(at.Add(-2 * time.Hour))}},
		{At: at.Add(2 * time.Hour), Kind: "probe_wanted", Outcome: "search", Detail: map[string]any{"t": "movie", "query": "dune part two", "id": "tmdbid:693134"}},
		{At: at.Add(3 * time.Hour), Kind: "probe_leased", Outcome: "search", Detail: map[string]any{"attempt": 1.0, "max_bytes": 67108864.0, "lease_until": sec(at.Add(3*time.Hour + 10*time.Minute))}},
		{At: at.Add(3*time.Hour + 2*time.Minute), Kind: "probed", Outcome: "tracks", Detail: map[string]any{"path": "Dune.Part.Two.2024.2160p.WEB-DL.HDR.mkv", "format": "matroska",
			"downloaded": 4194304.0, "tracks": []any{"video::V_MPEGH/ISO/HEVC", "audio:rus:A_EAC3", "audio:eng:A_EAC3", "subtitle:rus:S_TEXT/UTF8"}, "audio": map[string]any{"ru": 1.0}}},
	}
}

func SampleData(b Base) DataPage {
	daily := []int64{12890, 11380, 13590, 15000, 10440, 10660, 14880, 10860, 13330, 15300, 10540, 14630, 11960, 10350, 10780, 13950, 13800, 10640, 12190, 10830, 15010, 13870, 4100, 15150, 11120, 12040, 15740, 15710, 15310, 9506}
	var dl []string
	for i := range daily {
		dl = append(dl, b.Now.AddDate(0, 0, i-len(daily)+1).Format("02.01"))
	}
	weekly := []int64{88910, 89187, 82998, 71624, 77244, 71526, 88240, 74363, 79489, 83734, 74726, 86060}
	var wl []string
	for i := range weekly {
		wl = append(wl, b.Now.AddDate(0, 0, 7*(i-len(weekly)+1)).Format("02.01"))
	}
	hourly := []int64{512, 488, 530, 601, 577, 640, 702, 655, 590, 544, 498, 470, 455, 431, 420, 462, 503, 548, 591, 622, 604, 570, 533, 508,
		495, 471, 520, 589, 566, 630, 688, 641, 582, 537, 489, 463, 449, 426, 417, 455, 497, 541, 584, 613, 598, 561, 526, 212}
	var hl []string
	for i := range hourly {
		hl = append(hl, b.Now.Truncate(time.Hour).Add(time.Duration(i-len(hourly)+1)*time.Hour).Format("02.01 15:04"))
	}
	return DataPage{Base: b, Stats: Stats{
		Computed: b.Now.Add(-3 * time.Minute), Interval: 5 * time.Minute, Total: 1284311, NewDay: 9506, NewWeek: 86060, New24h: 12721, DroppedTotal: 4754966,
		ByKind:     []Count{{"movie", 312940}, {"series", 248117}, {"other", 628184}, {"unknown", 95070}},
		ByLayer:    []Count{{"rules", 702118}, {"title_repeat", 211540}, {"classifier", 370653}},
		BySource:   []Count{{"disc", 18204}, {"remux", 41377}, {"rip", 233510}, {"web", 251096}, {"other", 740124}},
		Voice:      []VoiceStats{{Lang: "ru", Bands: [4]int64{96412, 71905, 412330, 703664}}},
		Dropped:    []Count{{"adult", 2114820}, {"filetype", 1507332}, {"size", 611290}, {"software", 388401}, {"course", 92115}, {"script", 41008}},
		Classifier: ClassifierStats{Pending: 3412, DoneDay: 41280, LimitDay: 50000, PaceDay: 53600},
		Probe:      ProbeStats{RxDay: 61 << 30, TxDay: 38 << 30, LimitDay: 100 << 30, PaceDay: 79 << 30},
		Queue: []TaskQueue{
			{Kind: "fetch", Paced: true, Levels: []QueueLevel{{Priority: 0, Known: true},
				{Priority: 1, Tasks: 14, ETA: 20 * time.Second, Known: true},
				{Priority: 2, Tasks: 1486, ETA: 33 * time.Minute, Known: true},
				{Priority: 3, Tasks: 48210, ETA: 18*time.Hour + 20*time.Minute, Known: true},
				{Priority: 4, Tasks: 611392, ETA: 10*24*time.Hour + 4*time.Hour, Known: true}}},
			{Kind: "peers", Levels: []QueueLevel{{Priority: 0, Tasks: 2}, {Priority: 1}, {Priority: 2}, {Priority: 3}, {Priority: 4}}},
			{Kind: "probe", Paced: true, Levels: []QueueLevel{{Priority: 0, Tasks: 1, ETA: 3 * time.Minute, Known: true},
				{Priority: 1, Tasks: 63, ETA: 3*time.Hour + 10*time.Minute, Known: true},
				{Priority: 2, ETA: 3*time.Hour + 10*time.Minute, Known: true},
				{Priority: 3, ETA: 3*time.Hour + 10*time.Minute, Known: true},
				{Priority: 4, Tasks: 2, ETA: 3*time.Hour + 16*time.Minute, Known: true}}},
		},
		Hourly: NewBarChart(hourly, hl, 12, true, 530),
		Daily:  NewBarChart(daily, dl, 7, true, 14260),
		Weekly: NewBarChart(weekly, wl, 4, true, 92370),
	}, Features: SampleFeatures("all")}
}

// SampleFeatures are the optional features all on and working, "all"; all
// off, "off"; or all on with the classifier's limit spent and no prober,
// "degraded".
func SampleFeatures(state string) []Feature {
	fs := []Feature{
		{Key: "classifier", On: true, Works: true, Setting: "TYPESAFE_API_KEY"},
		{Key: "probes", On: true, Works: true, Setting: "-probe"},
		{Key: "audio", On: true, Works: true, Setting: "SNIFFARR_AUDIO_LANGUAGES", Values: []string{"ru"}},
		{Key: "feeds", On: true, Works: true, Setting: "SNIFFARR_FEEDS", Values: []string{"rutracker", "rutor"}},
	}
	for i := range fs {
		switch state {
		case "off":
			fs[i].On, fs[i].Works, fs[i].Values = false, false, nil
		case "degraded":
			fs[i].Works = fs[i].Key != "classifier" && fs[i].Key != "probes"
		}
	}
	return fs
}

// SampleFeatureBase is b as the UI sets it up with the optional features
// in the state SampleFeatures names.
func SampleFeatureBase(b Base, state string) Base {
	on := state != "off"
	b.Probes, b.Classifier, b.Feeds = on, on, on
	if !on {
		b.VoiceLangs = nil
	}
	return b
}

// SampleDataFeatures is SampleData with the optional features in the state
// SampleFeatures names, counted as the API counts them then.
func SampleDataFeatures(b Base, state string) DataPage {
	p := SampleData(SampleFeatureBase(b, state))
	p.Features = SampleFeatures(state)
	switch state {
	case "off":
		p.Stats.Voice = nil
		p.Stats.Queue = p.Stats.Queue[:2]
	case "degraded":
		c := &p.Stats.Classifier
		c.DoneDay, c.PaceDay = c.LimitDay, 0
		probes := &p.Stats.Queue[2]
		probes.Paced = false
		for i, n := range []int64{55, 2090, 0, 0, 85} {
			probes.Levels[i] = QueueLevel{Priority: i, Tasks: n}
		}
	}
	return p
}

// SampleDataStale is SampleData computed 12 minutes before Now, past two
// of its 5-minute intervals.
func SampleDataStale(b Base) DataPage {
	p := SampleData(b)
	p.Stats.Computed = b.Now.Add(-12 * time.Minute)
	return p
}

func dottedSE(se string) string {
	if se == "" {
		return ""
	}
	return "." + dotted(se)
}
