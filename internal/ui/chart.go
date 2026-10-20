package ui

import "math"

// Chart is a server-rendered bar chart. The SVG uses a fixed viewBox with
// preserveAspectRatio="none", so it stretches to the container width; axis
// labels live in HTML around it, so text never distorts.
type Chart struct {
	W, H    int
	Bars    []Bar
	YTicks  []int64  // top to bottom, last is 0
	XLabels []string // spread evenly under the chart
	Max     Bar
	Min     Bar
}

type Bar struct {
	X, Y, W, H float64
	Label      string // shown in the native tooltip (<title>)
	Value      int64
}

// NewBarChart lays out one bar per value. labels[i] names values[i];
// every labelEvery-th label (and the last one) goes to the x axis.
func NewBarChart(values []int64, labels []string, labelEvery int) Chart {
	const w, h = 1100, 160
	c := Chart{W: w, H: h}
	if len(values) == 0 {
		return c
	}
	var top int64
	minI, maxI := 0, 0
	for i, v := range values {
		if v > top {
			top = v
		}
		if v > values[maxI] {
			maxI = i
		}
		if v < values[minI] {
			minI = i
		}
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
		c.Bars = append(c.Bars, Bar{
			X: r1(float64(i)*slot + gap/2), Y: r1(h - bh),
			W: r1(slot - gap), H: r1(bh),
			Label: lbl, Value: v,
		})
	}
	c.Max, c.Min = c.Bars[maxI], c.Bars[minI]
	if labelEvery < 1 {
		labelEvery = 1
	}
	for i := 0; i < len(labels); i += labelEvery {
		c.XLabels = append(c.XLabels, labels[i])
	}
	if last := labels[len(labels)-1]; len(c.XLabels) == 0 || c.XLabels[len(c.XLabels)-1] != last {
		c.XLabels = append(c.XLabels, last)
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
