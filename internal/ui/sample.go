package ui

import "time"

// Sample page models for tests; not for production.

func fp(f float64) *float64 { return &f }

const gb = int64(1) << 30
const mb = int64(1) << 20

func SampleHome(b Base) HomePage {
	return HomePage{Base: b, Summary: Summary{Total: 1284311, NewDay: 9506, Pending: 3412}}
}

func SampleSearch(b Base, q string) SearchPage {
	now := b.Now
	at := func(d time.Duration) time.Time { return now.Add(-d) }
	v := func(explicit bool, p *float64) []VoiceCell { return []VoiceCell{{Explicit: explicit, Prob: p}} }
	day := 24 * time.Hour
	rows := []Row{
		{InfoHash: "8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52", Name: "Дюна. Часть вторая / Dune: Part Two (2024) WEB-DL 2160p HDR | D | Atmos", Peers: 12480, Size: 26629001216, Kind: "movie", Layer: "classifier", Confidence: fp(.93), Voice: v(true, fp(.97)), Source: "web", FirstSeen: at(41 * time.Hour), LastSeen: at(3 * time.Minute)},
		{InfoHash: "91a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4", Name: "Dune.Part.Two.2024.UHD.BluRay.2160p.REMUX.HEVC.Rus.Eng", Peers: 11020, Size: 74 * gb, Kind: "movie", Layer: "rules", Voice: v(true, fp(.95)), Source: "remux", FirstSeen: at(475 * day), LastSeen: at(4 * time.Minute)},
		{InfoHash: "a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5", Name: "Дюна: Часть вторая (2024) BDRip 1080p | D", Peers: 9870, Size: 14 * gb, Kind: "movie", Layer: "title_repeat", Voice: v(true, fp(.98)), Source: "rip", FirstSeen: at(478 * day), LastSeen: at(6 * time.Minute)},
		{InfoHash: "b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6", Name: "Dune.Part.Two.2024.1080p.WEB-DL.DDP5.1.H.264", Peers: 6430, Size: 10 * gb, Kind: "movie", Layer: "rules", Voice: v(false, fp(.12)), Source: "web", FirstSeen: at(524 * day), LastSeen: at(7 * time.Minute)},
		{InfoHash: "c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f7", Name: "Dune.Prophecy.S01.2160p.MAX.WEB-DL.DDP5.1.DV.HDR", Peers: 2940, Size: 41 * gb, Kind: "series", Layer: "rules", Voice: v(false, fp(.05)), Source: "web", FirstSeen: at(302 * day), LastSeen: at(11 * time.Minute)},
		{InfoHash: "d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708", Name: "Herbert F. - Dune (1965) EPUB", Peers: 410, Size: 1 * mb, Kind: "book", Layer: "classifier", Confidence: fp(.89), Voice: v(false, nil), Source: "other", FirstSeen: at(597 * day), LastSeen: at(25 * time.Minute)},
	}
	var works []WorkHit
	for _, i := range []int{0, 4, 3, 9} { // poster, no poster, unmatched, unchecked
		x := sampleMovies[i]
		works = append(works, WorkHit{Ref: x.ref(i + 1), Peers: x.peers, Releases: x.rels, TMDB: x.m(b.Lang)})
	}
	r := Results{Shown: len(rows), Total: 23, NextURL: "/?q=" + q + "&o=50", Rows: rows}
	return SearchPage{Base: b, Q: q, Works: works, Filters: Filters{VoiceLang: "ru", Voice: "any", Sort: "peers"}, Results: r}
}

func SampleTop(b Base, kind string, open string, all bool) TopPage {
	v := func(explicit bool, p *float64) []VoiceCell { return []VoiceCell{{Explicit: explicit, Prob: p}} }
	day := 24 * time.Hour
	list := sampleMovies
	if kind == "series" {
		list = sampleSeries
	} else {
		kind = "movie"
	}
	p := TopPage{Base: b, Kind: kind, Window: "7d", VoiceMode: "any", Computed: b.Now.Add(-5 * time.Minute), Limit: 100, All: all}
	for i, x := range list {
		wk := Work{Ref: x.ref(i + 1), Rank: i + 1, Peers: x.peers, Delta: x.delta, Releases: x.rels,
			Voice: []VoiceCell{x.voice}, Sources: x.sources, FirstSeen: b.Now.Add(-time.Duration(x.ageDays) * day), TMDB: x.m(b.Lang)}
		if wk.Ref.ID == open {
			wk.Open = true
			wk.Items = []Release{
				{InfoHash: "a1c0ffee00000000000000000000000000000001", Name: "Igra.na.ponizhenie.2015.WEB-DL.1080p.Open.Matte.mkv", Peers: 18400, Size: 9 * gb, Voice: v(true, fp(.97)), Source: "web", FirstSeen: b.Now.Add(-56 * day)},
				{InfoHash: "a1c0ffee00000000000000000000000000000002", Name: "The.Big.Short.2015.1080p.BluRay.x264.Rus.Eng", Peers: 14100, Size: 12 * gb, Voice: v(true, fp(.95)), Source: "rip", FirstSeen: b.Now.Add(-400 * day)},
				{InfoHash: "a1c0ffee00000000000000000000000000000003", Name: "The.Big.Short.2015.720p.WEB-DL.H264", Peers: 9260, Size: 4 * gb, Voice: v(false, fp(.12)), Source: "web", FirstSeen: b.Now.Add(-700 * day)},
			}
		}
		p.Works = append(p.Works, wk)
	}
	return p
}

// SampleOpenID is the work SampleTop opens when asked to.
var SampleOpenID = sampleID(sampleMovies[0])

// SampleTorrent cases: "poster", "noposter", "unmatched", "unchecked".
func SampleTorrent(b Base, all bool, cs string) TorrentPage {
	pick := map[string]int{"poster": 0, "noposter": 4, "unmatched": 3, "unchecked": 9}
	idx, ok := pick[cs]
	if !ok {
		idx = 0
	}
	x := sampleMovies[idx]
	ref := x.ref(idx + 1)
	names := []string{"Игра на понижение.2015.WEB-DL.1080р.Open Matte.mkv", "", "", "Narcissists.Playbook.2026.1080p.WEB-DL.AAC2.0.H264-Maraschino.mkv", "Одиссея [The Odyssey] (2026)"}
	name := "Дюна. Часть вторая / Dune: Part Two (2024) WEB-DL 2160p HDR | D | Atmos"
	query := "Dune Part Two 2024"
	switch idx {
	case 0:
		name, query = names[0], "Игра на понижение 2015"
	case 3:
		name, query = names[3], "Narcissists Playbook 2026"
	case 4:
		name, query = names[4], "Одиссея 2026"
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
	return TorrentPage{Base: b, BackURL: "/?q=" + query, Torrent: Torrent{
		InfoHash: "8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52",
		Name:     name,
		Magnet:   "magnet:?xt=urn:btih:8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52",
		Size:     26629001216, FileCount: 14,
		FirstSeen: b.Now.Add(-41 * time.Hour), LastSeen: b.Now.Add(-3 * time.Minute),
		DiscoveredVia: "announce", TMDBQuery: query,
		TMDB:     x.m(b.Lang),
		Peers:    PeerCounts{Day: 1960, Week: 12480, Month: 41300},
		Work:     &ref,
		WorkRels: x.rels, RankInWork: 1,
		Kind:   Answer{Value: "movie", Layer: "classifier", Confidence: fp(.93)},
		Source: Answer{Value: "web", Layer: "rules"},
		Voice:  []VoiceDetail{{Lang: "ru", Mark: "D", ScriptHint: "cyrillic", Prob: fp(.97)}},
		Rules:  []RuleHit{{"source.web", "WEB-DL"}, {"voice.ru.tag", "| D |"}, {"voice.ru.script", "Дюна. Часть вторая"}},
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
		ByKind:     []Count{{"movie", 312940}, {"series", 248117}, {"music", 301552}, {"book", 98403}, {"software", 41220}, {"other", 187009}, {"unknown", 95070}},
		ByLayer:    []Count{{"rules", 702118}, {"title_repeat", 211540}, {"classifier", 370653}},
		BySource:   []Count{{"disc", 18204}, {"remux", 41377}, {"rip", 233510}, {"web", 251096}, {"other", 740124}},
		Voice:      []VoiceStats{{Lang: "ru", Bands: [4]int64{96412, 71905, 412330, 703664}}},
		Dropped:    []Count{{"adult", 2114820}, {"filetype", 1507332}, {"size", 611290}, {"software", 388401}, {"course", 92115}, {"script", 41008}},
		Classifier: ClassifierStats{Pending: 3412, DoneDay: 41280, LimitDay: 50000},
		Daily:      NewBarChart(daily, dl, 7),
		Weekly:     NewBarChart(weekly, wl, 4),
	}}
}
