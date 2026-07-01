package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// latencyChart is a minimal sparkline for a peer's recent round-trip
// times. It plots a series of millisecond samples (oldest → newest,
// left → right), autoscaling the y-axis to the observed maximum. Empty
// data renders just the background — honest "nothing measured yet"
// rather than a fabricated flat line.
type latencyChart struct {
	widget.BaseWidget
	data []float64 // ms samples, oldest first
}

func newLatencyChart() *latencyChart {
	c := &latencyChart{}
	c.ExtendBaseWidget(c)
	return c
}

// setData replaces the plotted series and redraws.
func (c *latencyChart) setData(d []float64) {
	c.data = d
	c.Refresh()
}

func (c *latencyChart) CreateRenderer() fyne.WidgetRenderer {
	r := &latencyChartRenderer{chart: c}
	r.bg = canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground))
	r.bg.CornerRadius = theme.Size(theme.SizeNameInputRadius)
	r.refreshLines(c.Size())
	return r
}

type latencyChartRenderer struct {
	chart   *latencyChart
	bg      *canvas.Rectangle
	objects []fyne.CanvasObject
}

func (r *latencyChartRenderer) MinSize() fyne.Size { return fyne.NewSize(220, 90) }

func (r *latencyChartRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.refreshLines(size)
}

func (r *latencyChartRenderer) Refresh() {
	r.bg.FillColor = theme.Color(theme.ColorNameInputBackground)
	r.bg.Refresh()
	r.refreshLines(r.chart.Size())
	canvas.Refresh(r.chart)
}

func (r *latencyChartRenderer) Objects() []fyne.CanvasObject { return r.objects }
func (r *latencyChartRenderer) Destroy()                     {}

// refreshLines recomputes the polyline for the given draw size and
// rebuilds the object list (background first, then line segments).
func (r *latencyChartRenderer) refreshLines(size fyne.Size) {
	data := r.chart.data
	lines := make([]*canvas.Line, 0, maxInt(0, len(data)-1))

	if len(data) >= 2 && size.Width > 0 && size.Height > 0 {
		const pad = 4
		w := size.Width - 2*pad
		h := size.Height - 2*pad

		maxY := 0.0
		for _, v := range data {
			if v > maxY {
				maxY = v
			}
		}
		if maxY <= 0 {
			maxY = 1 // avoid div-by-zero; a flat all-zero series sits on the floor
		}

		lineColor := theme.Color(theme.ColorNamePrimary)
		n := len(data)
		px := func(i int) float32 { return pad + w*float32(i)/float32(n-1) }
		py := func(v float64) float32 { return pad + h - h*float32(v/maxY) }

		for i := 0; i < n-1; i++ {
			ln := canvas.NewLine(lineColor)
			ln.StrokeWidth = 2
			ln.Position1 = fyne.NewPos(px(i), py(data[i]))
			ln.Position2 = fyne.NewPos(px(i+1), py(data[i+1]))
			lines = append(lines, ln)
		}
	}

	r.objects = make([]fyne.CanvasObject, 0, len(lines)+1)
	r.objects = append(r.objects, r.bg)
	for _, ln := range lines {
		r.objects = append(r.objects, ln)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
