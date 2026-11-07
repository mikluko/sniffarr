package ui

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestJournalView(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 10, 20, 14, 5, 0, 0, loc)
	b := r.Base(httptest.NewRequest("GET", "/t/x", nil), "ru", "", nil, loc)
	b.Now, b.JournalRetention = now, 30*24*time.Hour
	page := func(only bool, es ...JournalEntry) JournalView {
		return TorrentPage{Base: b, Torrent: Torrent{InfoHash: "x", JournalOnly: only, Journal: es}}.JournalView()
	}
	sighted := JournalEntry{ID: 1, At: now.Add(-3 * time.Hour), Kind: "sighted", Outcome: "queued", Detail: map[string]any{"discovery": "announce"}}
	timeout := JournalEntry{ID: 2, At: now.Add(-2 * time.Hour), Kind: "fetch_failed", Outcome: "timeout", Detail: map[string]any{"attempt": 1.0, "retry_at": 1.0}}
	rejected := JournalEntry{ID: 3, At: now.Add(-time.Hour), Kind: "fetched", Outcome: "rejected", Detail: map[string]any{"reason": "adult", "name": "xxx", "size": 1073741824.0}}

	v := page(false, sighted, timeout, rejected)
	if v.Retries != 1 || v.Finals != 1 || v.Gone != nil || v.Cut {
		t.Errorf("a stored torrent's: %d retries, %d finals, gone %v, cut %v", v.Retries, v.Finals, v.Gone, v.Cut)
	}
	if len(v.Days) != 1 || v.Days[0].Label != "20 октября" || len(v.Days[0].Rows) != 2 || v.Days[0].Rows[0].ID != 3 {
		t.Errorf("days %+v", v.Days)
	}
	card := TorrentPage{Base: b, Torrent: Torrent{Journal: sampleJournal(now.Add(-41*time.Hour), "x")}}
	if v = card.JournalView(); v.Rows != 8 || v.Shown != JournalCardRows || len(v.Days[0].Rows)+len(v.Days[1].Rows) != JournalCardRows {
		t.Errorf("the card: %d of %d rows shown", v.Shown, v.Rows)
	}
	card.Torrent.JournalAll = true
	if v = card.JournalView(); v.Shown != 8 {
		t.Errorf("the whole journal: %d of %d rows shown", v.Shown, v.Rows)
	}

	older := TorrentPage{Base: b, Torrent: Torrent{JournalOnly: true, JournalAll: true, Journal: []JournalEntry{sighted, timeout},
		JournalTotal: 403, JournalOlder: 201, JournalAfter: "2026-10-20"}}
	v = older.JournalView()
	if v.Days[0].Label != "" || v.Gone != nil || v.Loaded != 202 || v.Next != JournalPageSize || v.Before != 1 || v.Last != "2026-10-20" {
		t.Errorf("a page of older entries: label %q, gone %v, %d loaded, next %d before %d, last %s", v.Days[0].Label, v.Gone, v.Loaded, v.Next, v.Before, v.Last)
	}

	v = page(true, sighted, timeout, rejected)
	want := JournalGone{Severity: SeverityFinal, Final: true, ID: 3, Lead: "Раздачи нет: отброшена 20.10 13:05", Rest: ", причина: для взрослых.",
		Note: "Раздача не сохранялась, поэтому от неё остался только журнал. Метаданные: «xxx», 1,0 ГБ."}
	if v.Gone == nil || *v.Gone != want {
		t.Errorf("ended by a final failure: %+v, want %+v", v.Gone, want)
	}
	if v.Finals != 0 || v.Retries != 1 {
		t.Errorf("the failure the card states is not counted again: %d finals, %d retries", v.Finals, v.Retries)
	}

	v = page(true, sighted, timeout)
	want = JournalGone{Severity: SeverityRetry, ID: 2, Lead: "Раздачи нет.", Event: "метаданные не получены, таймаут, 20.10 12:05",
		Note: "Раздача не сохранялась, поэтому от неё остался только журнал."}
	if v.Gone == nil || *v.Gone != want {
		t.Errorf("a last event that is no end: %+v, want %+v", v.Gone, want)
	}

	old := sighted
	old.At = now.Add(-29 * 24 * time.Hour)
	if v = page(true, old, rejected); !v.Cut {
		t.Error("an entry 29 days old under a 30-day retention: the start may be gone")
	}
	old.At = now.Add(-28 * 24 * time.Hour)
	if v = page(true, old, rejected); v.Cut {
		t.Error("28 days old: nothing deleted yet")
	}
	if got := b.Fmt("journal.cut", b.Retention(b.JournalRetention, true)); got != "Записи старше 30 дней удалены, начало журнала могло пропасть." {
		t.Errorf("cut note %q", got)
	}
}

func TestJournalFormats(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 10, 20, 14, 5, 0, 0, loc)
	for _, c := range []struct {
		lang, got, want string
	}{
		{"ru", "day", "19 октября"}, {"en", "day", "Oct 19"}, {"ru", "day-year", "31 декабря 2025"}, {"en", "day-year", "Dec 31, 2025"},
		{"ru", "span", "18.10 20:57 – 19.10 00:07"}, {"ru", "span-day", "20.10 11:05 – 12:05"},
		{"ru", "stamp", "19.10.2026 00:07:09"}, {"en", "stamp", "Oct 19, 2026, 00:07:09"},
		{"ru", "retention", "30 дней"}, {"ru", "retention-hours", "36 часов"}, {"en", "retention", "30 days"},
		{"ru", "title", "Метаданные получены"}, {"ru", "group", "Метаданные"},
	} {
		b := r.Base(httptest.NewRequest("GET", "/t/x", nil), c.lang, "", nil, loc)
		b.Now = now
		at := time.Date(2026, 10, 19, 0, 7, 9, 0, loc)
		got := map[string]func() string{
			"day":             func() string { return b.DayLabel(at) },
			"day-year":        func() string { return b.DayLabel(time.Date(2025, 12, 31, 12, 0, 0, 0, loc)) },
			"span":            func() string { return b.JournalSpan(time.Date(2026, 10, 18, 20, 57, 0, 0, loc), at) },
			"span-day":        func() string { return b.JournalSpan(now.Add(-3*time.Hour), now.Add(-2*time.Hour)) },
			"stamp":           func() string { return b.Stamp(at) },
			"retention":       func() string { return b.Retention(30*24*time.Hour, false) },
			"retention-hours": func() string { return b.Retention(36*time.Hour, true) },
			"title":           func() string { return b.RowTitle(JournalRow{Kind: "fetched"}) },
			"group":           func() string { return b.RowTitle(JournalRow{Kind: "fetch", Steps: make([]JournalStep, 2)}) },
		}[c.got]()
		if got != c.want {
			t.Errorf("%s %s: %q, want %q", c.lang, c.got, got, c.want)
		}
	}
}
