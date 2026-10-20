package ui

import "time"

// Sample page models for tests; not for production.

func fp(f float64) *float64 { return &f }

const gb = int64(1) << 30
const mb = int64(1) << 20

func SampleSearch(b Base, q string) SearchPage {
	now := b.Now
	at := func(d time.Duration) time.Time { return now.Add(-d) }
	v := func(explicit bool, p *float64) []VoiceCell { return []VoiceCell{{Explicit: explicit, Prob: p}} }
	rows := []Row{
		{InfoHash: "8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52", Name: "Дюна. Часть вторая / Dune: Part Two (2024) WEB-DL 2160p HDR | D | Atmos", Size: 26629001216, Kind: "movie", Layer: "classifier", Confidence: fp(.93), Voice: v(true, fp(.97)), Source: "web", FirstSeen: at(41 * time.Hour), LastSeen: at(3 * time.Minute)},
		{InfoHash: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", Name: "Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb", Size: 41 * gb, Kind: "series", Layer: "rules", Voice: v(false, nil), Source: "web", FirstSeen: at(7 * time.Minute), LastSeen: at(4 * time.Minute)},
		{InfoHash: "b2c3d4e5f60718293a4b5c6d7e8f901234567890", Name: "Oppenheimer.2023.UHD.BluRay.2160p.REMUX.HEVC.DTS-HD.MA.5.1", Size: 78 * gb, Kind: "movie", Layer: "rules", Voice: v(false, fp(.71)), Source: "remux", FirstSeen: at(38 * 24 * time.Hour), LastSeen: at(5 * time.Minute)},
		{InfoHash: "c3d4e5f60718293a4b5c6d7e8f90123456789012", Name: "Pink Floyd - The Dark Side Of The Moon (50th Anniversary) [FLAC 24-96]", Size: 2 * gb, Kind: "music", Layer: "rules", Voice: v(false, nil), Source: "other", FirstSeen: at(14 * time.Minute), LastSeen: at(5 * time.Minute)},
		{InfoHash: "d4e5f60718293a4b5c6d7e8f9012345678901234", Name: "Shogun.2024.S01.2160p.DSNP.WEB-DL.DDP5.1.Atmos.DV.HDR", Size: 96 * gb, Kind: "series", Layer: "title_repeat", Voice: v(false, fp(.12)), Source: "web", FirstSeen: at(18 * time.Minute), LastSeen: at(6 * time.Minute)},
		{InfoHash: "e5f60718293a4b5c6d7e8f901234567890123456", Name: "Пелевин В. - Круть (2023) FB2, EPUB", Size: 6 * mb, Kind: "book", Layer: "classifier", Confidence: fp(.91), Voice: v(false, nil), Source: "other", FirstSeen: at(32 * time.Minute), LastSeen: at(9 * time.Minute)},
		{InfoHash: "f60718293a4b5c6d7e8f90123456789012345678", Name: "ubn2410_x7f.part03", Size: 4 * gb, Kind: "unknown", Layer: "classifier", Confidence: fp(.31), Voice: v(false, nil), Source: "other", FirstSeen: at(46 * time.Minute), LastSeen: at(12 * time.Minute)},
	}
	r := Results{Fresh: q == "", SelfURL: "/", Shown: len(rows), Total: 1284311, NextURL: "/?o=50", Rows: rows}
	if q != "" {
		r.SelfURL = "/?q=" + q
		r.Total = int64(len(rows))
		r.NextURL = ""
	}
	return SearchPage{Base: b, Q: q, Filters: Filters{VoiceLang: "ru", Voice: "any"}, Results: r}
}

func SampleTorrent(b Base, all bool) TorrentPage {
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
	return TorrentPage{Base: b, BackURL: "/", Torrent: Torrent{
		InfoHash: "8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52",
		Name:     "Дюна. Часть вторая / Dune: Part Two (2024) WEB-DL 2160p HDR | D | Atmos",
		Magnet:   "magnet:?xt=urn:btih:8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52",
		Size:     26629001216, FileCount: 14,
		FirstSeen: b.Now.Add(-41 * time.Hour), LastSeen: b.Now.Add(-3 * time.Minute),
		DiscoveredVia: "announce", TMDBQuery: "Dune Part Two 2024",
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
