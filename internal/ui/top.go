package ui

import "time"

// Change classifies w's place against the previous window: "move" when it
// was within the top then; otherwise "re" for a return, "new" for a first
// entry, "entry" when w was in the top already as its history began, and
// "unknown" while the top has no history.
func (p TopPage) Change(w Work) string {
	switch {
	case w.PrevRank > 0 && w.PrevRank <= p.Limit:
		return "move"
	case w.Debut.IsZero():
		return "unknown"
	case w.Reentry:
		return "re"
	case p.AtStart(w):
		return "entry"
	}
	return "new"
}

// ChangeTip is the tooltip of w's change other than a move: how it came
// into the top and its place in the previous window.
func (p TopPage) ChangeTip(w Work) string {
	return p.Fmt("top.change_tip."+p.Change(w), p.PrevPlace(w.PrevRank), p.FullDate(p.Since))
}

// AtStart reports whether w was in the top already when its history began,
// so its debut, time in the top and peak count from then.
func (p TopPage) AtStart(w Work) bool {
	return !w.Debut.IsZero() && w.Debut.Equal(p.Since)
}

// ChartTip is the tooltip of the column header key about the top's kind,
// with when the top's history began once it has.
func (p TopPage) ChartTip(key string) string {
	s := p.Fmt(key, p.T("top.noun."+p.Kind))
	if !p.Since.IsZero() {
		s += " " + p.Fmt("top.history", p.FullDate(p.Since))
	}
	return s
}

// OnChart formats a time in the top: "< 1 дн" under a day, whole days up
// to 14, whole weeks after: "9 дн", "3 нед".
func (b Base) OnChart(d time.Duration) string {
	switch n := int64(d / (24 * time.Hour)); {
	case n < 1:
		return "< 1 " + b.T("top.d")
	case n <= 14:
		return b.Num(n) + " " + b.T("top.d")
	default:
		return b.Num(n/7) + " " + b.T("top.w")
	}
}

// OnChartTip is the tooltip of w's time in the top, in whole days.
func (p TopPage) OnChartTip(w Work) string {
	days := p.T("top.under_day")
	if n := int64(w.OnChart / (24 * time.Hour)); n > 0 {
		days = p.Count(n, "top.days")
	}
	if p.AtStart(w) {
		return p.Fmt("top.onchart_val_start", days, p.FullDate(p.Since))
	}
	return p.Fmt("top.onchart_val", days)
}

// FullDate formats a day with its year: "07.11.2026" / "Nov 7, 2026".
func (b Base) FullDate(t time.Time) string {
	if b.ru() {
		return t.In(b.loc()).Format("02.01.2006")
	}
	return t.In(b.loc()).Format("Jan 2, 2006")
}
