package ui

import "math"

// Chart is a server-rendered bar chart. The SVG uses a fixed viewBox with
// preserveAspectRatio="none", so it stretches to the container width; axis
// labels live in HTML around it, so text never distorts.
type Chart struct {
	W, H    int
	Bars    []Bar
	YTicks  []int64 // top to bottom, last is 0
	XLabels []XLabel
	Max     Bar
	Min     Bar
}

// XLabel names a bar under the chart, at X percent of its width: the
// bar's centre, or 0 and 100 for the first and the last label, which the
// stylesheet aligns to the edges.
type XLabel struct {
	Text string
	X    float64
}

type Bar struct {
	X, Y, W, H float64
	Label      string // shown in the native tooltip (<title>)
	Value      int64
	Partial    bool // the period is not over: drawn lighter, out of Max and Min
	// Projected is what the unfinished period comes to at its pace so far,
	// 0 for none; PY and PH outline it.
	Projected int64
	PY, PH    float64
}

// NewBarChart lays out one bar per value. labels[i] names values[i];
// every labelEvery-th label (and the last one) goes to the x axis. With
// partial the last bar is the current, unfinished period, outlined up to
// projected when that is above 0.
func NewBarChart(values []int64, labels []string, labelEvery int, partial bool, projected int64) Chart {
	const w, h = 1100, 160
	c := Chart{W: w, H: h}
	if len(values) == 0 {
		return c
	}
	top := projected
	minI, maxI := -1, -1
	for i, v := range values {
		if v > top {
			top = v
		}
		if partial && i == len(values)-1 {
			break
		}
		if maxI < 0 || v > values[maxI] {
			maxI = i
		}
		if minI < 0 || v < values[minI] {
			minI = i
		}
	}
	if maxI < 0 {
		maxI, minI = 0, 0
	}
	ymax := niceCeil(top)
	c.YTicks = []int64{ymax, ymax / 2, 0}

	slot := float64(w) / float64(len(values))
	gap := math.Max(2, slot*0.15)
	for i, v := range values {
		bh := 0.0
		if ymax > 0 {
			bh = float64(v) / float64(ymax) * h
		}
		lbl := ""
		if i < len(labels) {
			lbl = labels[i]
		}
		b := Bar{
			X: r1(float64(i)*slot + gap/2), Y: r1(h - bh),
			W: r1(slot - gap), H: r1(bh),
			Label: lbl, Value: v, Partial: partial && i == len(values)-1,
		}
		if b.Partial && projected > 0 {
			ph := float64(projected) / float64(ymax) * h
			b.Projected, b.PY, b.PH = projected, r1(h-ph), r1(ph)
		}
		c.Bars = append(c.Bars, b)
	}
	c.Max, c.Min = c.Bars[maxI], c.Bars[minI]
	if labelEvery < 1 {
		labelEvery = 1
	}
	for i := 0; i < len(labels); i += labelEvery {
		c.XLabels = append(c.XLabels, XLabel{labels[i], r1((float64(i) + 0.5) * slot / w * 100)})
	}
	if last := len(labels) - 1; last >= 0 && (len(c.XLabels) == 0 || c.XLabels[len(c.XLabels)-1].Text != labels[last]) {
		c.XLabels = append(c.XLabels, XLabel{labels[last], 100})
	}
	if len(c.XLabels) > 0 {
		c.XLabels[0].X = 0
		c.XLabels[len(c.XLabels)-1].X = 100
	}
	return c
}

// niceCeil rounds up to 1, 2 or 5 × 10^k.
func niceCeil(v int64) int64 {
	if v <= 0 {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(float64(v))))
	for _, m := range []float64{1, 2, 5, 10} {
		if c := m * p; c >= float64(v) {
			return int64(c)
		}
	}
	return int64(10 * p)
}

func r1(f float64) float64 { return math.Round(f*10) / 10 }
