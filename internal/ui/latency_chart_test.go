package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
)

func TestLatencyChart_EmptyRendersBackgroundOnly(t *testing.T) {
	test.NewTempApp(t)
	c := newLatencyChart()
	r := c.CreateRenderer()
	r.Layout(fyne.NewSize(200, 90))
	// Empty series: just the background rectangle, no line segments — an
	// honest "nothing measured yet".
	if got := lineCount(r.Objects()); got != 0 {
		t.Fatalf("empty chart drew %d line segments, want 0", got)
	}
}

func TestLatencyChart_SeriesDrawsSegments(t *testing.T) {
	test.NewTempApp(t)
	c := newLatencyChart()
	r := c.CreateRenderer()
	c.setData([]float64{5, 9, 4, 12})
	r.Layout(fyne.NewSize(200, 90))
	// n points → n-1 segments.
	if got := lineCount(r.Objects()); got != 3 {
		t.Fatalf("4-point series drew %d segments, want 3", got)
	}
}

func TestLatencyChart_SinglePointNoSegments(t *testing.T) {
	test.NewTempApp(t)
	c := newLatencyChart()
	r := c.CreateRenderer()
	c.setData([]float64{7})
	r.Layout(fyne.NewSize(200, 90))
	if got := lineCount(r.Objects()); got != 0 {
		t.Fatalf("single-point series drew %d segments, want 0", got)
	}
}

func lineCount(objs []fyne.CanvasObject) int {
	n := 0
	for _, o := range objs {
		if _, ok := o.(*canvas.Line); ok {
			n++
		}
	}
	return n
}
