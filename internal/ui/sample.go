package ui

import (
	"sort"
	"time"
)

// Sample models used by tests and cmd/preview. Not for production.

func fp(f float64) *float64 { return &f }

const gb = int64(1) << 30
const mb = int64(1) << 20

func SampleHome(b Base) HomePage {
	return HomePage{Base: b, Summary: Summary{Total: 1284311, NewDay: 9506, Pending: 3412}}
}

// SampleSearch lists titles: ours by peers, then TMDB-only.
func SampleSearch(b Base, q, kind string) SearchPage {
	p := SearchPage{Base: b, Q: q}
	p.Titles.Kind = kind
	ours := []titleSample{spiderMan, sampleMovies[1], sampleSeries[0]}
	for i, s := range ours {
		if kind != "" && s.kind != kind {
			continue
		}
		p.Titles.Items = append(p.Titles.Items, s.summary(b.Lang, []int{1, 2, 1}[i]))
		p.Titles.Ours++
	}
	for _, s := range sampleTMDBOnly {
		if kind != "" && s.kind != kind {
			continue
		}
		p.Titles.Items = append(p.Titles.Items, s.summary(b.Lang, 0))
		p.Titles.TMDBOnly++
	}
	return p
}

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
	return TorrentsPage{Base: b, Q: q, Filters: f, Results: Results{Total: int64(len(rows)), Shown: len(rows), Rows: rows}}
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
		wk := Work{WorkSummary: x.summary(b.Lang, i+1), Delta: x.delta,
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
	p := WorkPage{Base: b, Work: w, Audio: sampleAudio(s, i, b.VoiceLangs)}
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
	t := ReleaseTable{WorkID: w.ID, Kind: s.kind, Filters: f, Seasons: seasons, Total: len(all), Matched: len(rows)}
	if len(rows) > 50 {
		t.NextURL = "/w/" + w.ID + "?o=50"
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

// SampleTorrent cases: "poster", "noposter" (matched titles), "unmatched", "unchecked".
func SampleTorrent(b Base, all bool, cs string) TorrentPage {
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
		Magnet:   "magnet:?xt=urn:btih:8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52",
		Size:     26629001216, FileCount: 14,
		FirstSeen: b.Now.Add(-41 * time.Hour), LastSeen: b.Now.Add(-3 * time.Minute),
		DiscoveredVia: "announce", TMDBQuery: query,
		TMDB:  tmdb,
		Peers: PeerCounts{Day: 1960, Week: 12480, Month: 41300},
		Work:  work, RankInWork: 1,
		Kind:   Answer{Value: "movie", Layer: "classifier", Confidence: fp(.93)},
		Source: Answer{Value: "web", Layer: "rules"},
		Audio:  []Audio{{Lang: "ru", Script: "cyrillic", Probability: fp(.88)}},
		Rules:  []RuleHit{{"source.web", "WEB-DL"}, {"quality.1080p", "1080р"}},
		Files:  FileList{InfoHash: "8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52", Files: shown, Stored: len(files), All: all},
	}}
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
	return DataPage{Base: b, Stats: Stats{
		At: b.Now, Total: 1284311, NewDay: 9506, NewWeek: 86060, DroppedTotal: 4754966,
		ByKind:     []Count{{"movie", 312940}, {"series", 248117}, {"other", 628184}, {"unknown", 95070}},
		ByLayer:    []Count{{"rules", 702118}, {"title_repeat", 211540}, {"classifier", 370653}},
		BySource:   []Count{{"disc", 18204}, {"remux", 41377}, {"rip", 233510}, {"web", 251096}, {"other", 740124}},
		Voice:      []VoiceStats{{Lang: "ru", Bands: [4]int64{96412, 71905, 412330, 703664}}},
		Dropped:    []Count{{"adult", 2114820}, {"filetype", 1507332}, {"size", 611290}, {"software", 388401}, {"course", 92115}, {"script", 41008}},
		Classifier: ClassifierStats{Pending: 3412, DoneDay: 41280, LimitDay: 50000},
		Daily:      NewBarChart(daily, dl, 7),
		Weekly:     NewBarChart(weekly, wl, 4),
	}}
}

func dottedSE(se string) string {
	if se == "" {
		return ""
	}
	return "." + dotted(se)
}
