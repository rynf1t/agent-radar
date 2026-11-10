package view

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"

	"github.com/rynf1t/agent-radar/internal/radar"
	"github.com/rynf1t/craftkit/termcolor"
)

// Metrics in points. One file of numbers so the layout stays consistent.
const (
	padX      = 14.0 // left and right content edge
	padTop    = 12.0
	summaryH  = 26.0 // the counts line at the top
	headerGap = 12.0 // space above a session header
	headerH   = 22.0
	rowH      = 60.0 // three lines
	rowGap    = 4.0
	glyphR    = 4.5 // state dot radius
	textX     = 20.0
	selRadius = 8.0
	selInset  = 6.0  // the selection reaches this far past the content edge
	barH      = 34.0 // the filter bar

	sizeTitle  = 13.5
	sizeBody   = 12.0
	sizeHeader = 11.5
)

// palette is every color the view draws with, derived from the terminal.
type palette struct {
	bg, fg                  color.RGBA
	body, faint, header     color.RGBA // text greys
	idle, ring              color.RGBA // idle dot, working ring
	accent, blocked, failed color.RGBA
	blockedText, failedText color.RGBA // the state colors where they are text
	doneText                color.RGBA
	sel                     color.RGBA // selection fill, premultiplied with alpha
	dark                    bool
}

var fallbackDark = termcolor.Theme{
	FG: rgb(226, 228, 231), BG: rgb(30, 31, 34), HasFG: true, HasBG: true,
	Palette: map[int]color.RGBA{termcolor.Red: rgb(236, 103, 96), termcolor.Yellow: rgb(229, 181, 72), termcolor.Blue: rgb(92, 142, 240)},
}

var fallbackLight = termcolor.Theme{
	FG: rgb(24, 26, 29), BG: rgb(255, 255, 255), HasFG: true, HasBG: true,
	Palette: map[int]color.RGBA{termcolor.Red: rgb(204, 52, 44), termcolor.Yellow: rgb(190, 130, 10), termcolor.Blue: rgb(40, 96, 200)},
}

func rgb(r, g, b uint8) color.RGBA { return color.RGBA{r, g, b, 255} }

// paletteFor derives the view's colors from a theme. Greys are the
// foreground mixed into the background. State colors come from the
// terminal's red, yellow and blue, nudged toward the foreground where they
// are text and too faint to read.
func paletteFor(t termcolor.Theme) palette {
	base := fallbackLight
	if t.Dark() {
		base = fallbackDark
	}
	fg, bg := base.FG, base.BG
	if t.HasFG {
		fg = t.FG
	}
	if t.HasBG {
		bg = t.BG
	}
	dark := termcolor.Luminance(bg) < 0.18
	accent := t.Color(termcolor.Blue, base.Palette[termcolor.Blue])
	yellow := t.Color(termcolor.Yellow, base.Palette[termcolor.Yellow])
	red := t.Color(termcolor.Red, base.Palette[termcolor.Red])
	selA := 0.14
	if dark {
		selA = 0.22
	}
	return palette{
		bg: bg, fg: fg, dark: dark,
		body:        termcolor.Mix(fg, bg, 0.30),
		faint:       termcolor.Mix(fg, bg, 0.52),
		header:      termcolor.Mix(fg, bg, 0.42),
		idle:        termcolor.Mix(fg, bg, 0.68),
		ring:        termcolor.Mix(fg, bg, 0.40),
		accent:      accent,
		blocked:     yellow,
		failed:      red,
		blockedText: legible(yellow, fg, bg),
		failedText:  legible(red, fg, bg),
		doneText:    legible(accent, fg, bg),
		sel:         premul(accent, selA),
	}
}

// legible moves c toward fg until it reads as text on bg.
func legible(c, fg, bg color.RGBA) color.RGBA {
	for t := 0.0; t <= 1; t += 0.1 {
		m := termcolor.Mix(c, fg, t)
		if termcolor.Contrast(m, bg) >= 3.2 {
			return m
		}
	}
	return fg
}

func premul(c color.RGBA, a float64) color.RGBA {
	return color.RGBA{uint8(float64(c.R)*a + .5), uint8(float64(c.G)*a + .5), uint8(float64(c.B)*a + .5), uint8(255*a + .5)}
}

func withAlpha(c color.RGBA, a float64) color.RGBA {
	if a >= 1 {
		return c
	}
	return premul(color.RGBA{c.R, c.G, c.B, 255}, a*float64(c.A)/255)
}

// --- fonts ----------------------------------------------------------------------

// The view uses the system font. macOS ships SF as a variable font, which
// x/image draws at its default instance (Regular). One weight is enough:
// hierarchy comes from size and color.
var fontCandidates = []string{
	"/System/Library/Fonts/SFNS.ttf",
	"/usr/share/fonts/TTF/Inter-Regular.ttf",
	"/usr/share/fonts/truetype/inter/Inter-Regular.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
}

var (
	fontOnce sync.Once
	theFont  *opentype.Font
	facesMu  sync.Mutex
	faces    = map[float64]font.Face{}
)

func loadFont() *opentype.Font {
	fontOnce.Do(func() {
		cands := fontCandidates
		if p := os.Getenv("RADAR_FONT"); p != "" {
			cands = append([]string{p}, cands...)
		}
		for _, p := range cands {
			if data, err := os.ReadFile(p); err == nil {
				if f, err := opentype.Parse(data); err == nil {
					theFont = f
					return
				}
			}
		}
		theFont, _ = opentype.Parse(goregular.TTF)
	})
	return theFont
}

func face(px float64) font.Face {
	px = math.Round(px*4) / 4
	facesMu.Lock()
	defer facesMu.Unlock()
	if f, ok := faces[px]; ok {
		return f
	}
	f, err := opentype.NewFace(loadFont(), &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err)
	}
	faces[px] = f
	return f
}

// --- layout -----------------------------------------------------------------------

type itemKind int

const (
	kindHeader itemKind = iota
	kindRow
)

// item is one thing in the list: a session header or an agent row. Its key
// stays the same while it exists, so it can glide when the order changes.
type item struct {
	key   string
	kind  itemKind
	y, h  float64 // top and height, in points
	agent radar.Agent
	label string // header text
}

// layout positions headers and rows for agents (already in display order).
func layout(agents []radar.Agent) (items []item, height float64) {
	y := padTop + summaryH
	session := ""
	for i, a := range agents {
		if i == 0 || a.SessionID != session {
			if i > 0 {
				y += headerGap
			}
			session = a.SessionID
			items = append(items, item{key: "s:" + a.SessionID, kind: kindHeader, y: y, h: headerH, label: a.Session})
			y += headerH
		}
		items = append(items, item{key: "b:" + a.BlockID, kind: kindRow, y: y, h: rowH, agent: a})
		y += rowH + rowGap
	}
	return items, y + padTop
}

// --- drawing ----------------------------------------------------------------------

// frame is everything one picture shows.
type frame struct {
	pal      palette
	scale    float64
	widthPx  int
	heightPx int
	opaque   bool // paint the background for PNG output. A pane shows its own.
	items    []item
	alpha    map[string]float64 // per item, for fading in and out
	offset   float64            // scroll, in points
	contentH float64            // the whole list's height, in points
	selY     float64            // selection top in points, NaN for none
	summary  []span
	filter   *filterBar
	hint     string // shown at the bottom while the pane has focus
	empty    string // shown when there are no rows
	status   string // a strip at the top, such as "Reconnecting to Rex"
	dim      bool   // fade everything under the status strip
	now      time.Time
}

type filterBar struct {
	query  string
	count  string // "3 of 7"
	typing bool
}

// span is a run of text in one color.
type span struct {
	text string
	col  color.RGBA
}

type canvas struct {
	img   *image.RGBA
	scale float64
}

func (c *canvas) px(pt float64) float64 { return pt * c.scale }

// render draws f.
func render(f frame) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, f.widthPx, f.heightPx))
	if f.opaque {
		draw.Draw(img, img.Bounds(), image.NewUniform(f.pal.bg), image.Point{}, draw.Src)
	}
	c := &canvas{img: img, scale: f.scale}
	w := float64(f.widthPx) / f.scale
	h := float64(f.heightPx) / f.scale
	listBottom := h
	if f.filter != nil {
		listBottom -= barH
	} else if f.hint != "" {
		listBottom -= 24
	}
	dimA := 1.0
	if f.dim {
		dimA = 0.4
	}

	// Content, clipped above the filter bar.
	content := image.NewRGBA(img.Bounds())
	cc := &canvas{img: content, scale: f.scale}
	if !math.IsNaN(f.selY) {
		y := f.selY - f.offset
		cc.roundRect(padX-selInset, y, w-padX+selInset, y+rowH, selRadius, f.pal.sel)
	}
	cc.spans(padX, padTop+16-f.offset, sizeBody, w-2*padX, f.summary, 1)
	for _, it := range f.items {
		a := f.alpha[it.key]
		if a <= 0 {
			continue
		}
		y := it.y - f.offset
		if y+it.h < 0 || y > listBottom {
			continue
		}
		switch it.kind {
		case kindHeader:
			cc.text(padX, y+15, sizeHeader, it.label, withAlpha(f.pal.header, a), w-2*padX)
		case kindRow:
			cc.row(f, it.agent, y, w, a)
		}
	}
	if len(f.items) == 0 && f.empty != "" {
		tw := cc.measure(f.empty, sizeBody)
		cc.text((w-tw)/2, listBottom/2, sizeBody, f.empty, f.pal.faint, w)
	}
	clipH := int(math.Round(listBottom * f.scale))
	dst := img.SubImage(image.Rect(0, 0, f.widthPx, clipH)).(*image.RGBA)
	draw.DrawMask(dst, dst.Bounds(), content, image.Point{}, fadeMask(f, clipH, dimA), image.Point{}, draw.Over)

	switch {
	case f.filter != nil:
		c.filterBar(f, w, h)
	case f.hint != "":
		tw := c.measure(f.hint, sizeBody-1)
		c.text((w-tw)/2, h-9, sizeBody-1, f.hint, f.pal.faint, w)
	}
	if f.status != "" {
		c.roundRect(padX-selInset, 6, w-padX+selInset, 30, 7, premul(f.pal.fg, 0.08))
		c.text(padX, 23, sizeBody, f.status, f.pal.body, w-2*padX)
	}
	return img
}

// fadeEdge is how far the list fades out at an edge it can scroll past.
const fadeEdge = 28.0

// fadeMask is the list's opacity: dimA everywhere, fading to nothing at an
// edge only when there is more list beyond it.
func fadeMask(f frame, clipH int, dimA float64) image.Image {
	listH := float64(clipH) / f.scale
	top, bottom := f.offset > 0.5, f.offset+listH < f.contentH-0.5
	if !top && !bottom {
		return image.NewUniform(color.Alpha{uint8(255 * dimA)})
	}
	m := image.NewAlpha(image.Rect(0, 0, f.widthPx, clipH))
	fadePx := fadeEdge * f.scale
	for y := 0; y < clipH; y++ {
		a := 1.0
		if top && float64(y) < fadePx {
			a = float64(y) / fadePx
		}
		if d := float64(clipH - 1 - y); bottom && d < fadePx {
			a = math.Min(a, d/fadePx)
		}
		v := uint8(math.Round(255 * dimA * a * a)) // eased so the fade reads as an edge, not a band
		row := m.Pix[y*m.Stride : y*m.Stride+f.widthPx]
		for i := range row {
			row[i] = v
		}
	}
	return m
}

func (c *canvas) row(f frame, a radar.Agent, y, w, alpha float64) {
	p := f.pal
	left := padX + textX
	right := w - padX
	// Line 1: state mark, title, age.
	cy := y + 15
	switch a.Urgency() {
	case radar.Blocked:
		c.circle(padX+glyphR+1, cy, glyphR, withAlpha(p.blocked, alpha))
	case radar.Error:
		c.circle(padX+glyphR+1, cy, glyphR, withAlpha(p.failed, alpha))
	case radar.Done:
		c.circle(padX+glyphR+1, cy, glyphR, withAlpha(p.accent, alpha))
	case radar.Working:
		prog := -1.0
		for _, r := range a.Records {
			if r.Progress != nil && r.State == "working" {
				prog = float64(*r.Progress) / 100
			}
		}
		c.ring(padX+glyphR+1, cy, glyphR, 1.4, withAlpha(p.ring, alpha))
		if prog > 0 {
			c.pie(padX+glyphR+1, cy, glyphR-2.2, prog, withAlpha(p.ring, alpha))
		}
	default:
		c.circle(padX+glyphR+1, cy, 2.5, withAlpha(p.idle, alpha))
	}
	titleCol := p.fg
	if a.Urgency() == radar.Idle {
		titleCol = p.body
	}
	age := radar.Ago(a.Since, f.now)
	ageW := 0.0
	if age != "" {
		ageW = c.measure(age, sizeBody)
		c.text(right-ageW, cy+4.5, sizeBody, age, withAlpha(p.faint, alpha), ageW+1)
		ageW += 10
	}
	c.text(left, cy+4.8, sizeTitle, a.Title, withAlpha(titleCol, alpha), right-ageW-left)

	// Line 2: what it is doing or waiting for.
	stateCol := p.body
	switch a.Urgency() {
	case radar.Blocked:
		stateCol = p.blockedText
	case radar.Error:
		stateCol = p.failedText
	case radar.Done:
		stateCol = p.doneText
	}
	line2 := []span{{a.StateText(), withAlpha(stateCol, alpha)}}
	if a.Msg != "" {
		line2 = append(line2, span{"  " + oneLine(a.Msg), withAlpha(p.body, alpha)})
	}
	c.spans(left, y+34, sizeBody, right-left, line2, 1)

	// Line 3: which program, where.
	meta := a.AppName()
	if a.CWD != "" {
		meta += "  " + radar.ShortCWD(a.CWD)
	}
	c.text(left, y+51, sizeBody, meta, withAlpha(p.faint, alpha), right-left)
}

func (c *canvas) filterBar(f frame, w, h float64) {
	p := f.pal
	top := h - barH
	c.roundRect(padX-selInset, top+4, w-padX+selInset, h-6, 8, premul(p.fg, 0.07))
	base := top + barH/2 + 4
	c.text(padX+2, base, sizeBody+1, "/", p.faint, 20)
	x := padX + 14
	countW := 0.0
	if f.filter.count != "" {
		countW = c.measure(f.filter.count, sizeBody)
		c.text(w-padX-countW, base, sizeBody, f.filter.count, p.faint, countW+1)
		countW += 10
	}
	q := f.filter.query
	col := p.fg
	if q == "" && !f.filter.typing {
		q, col = "Filter", p.faint
	}
	end := c.text(x, base, sizeBody+1, q, col, w-padX-countW-x-6)
	if f.filter.typing {
		c.rect(end+1, base-12, end+2.5, base+3, p.accent)
	}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// text draws s with its baseline at y, cut with an ellipsis to fit maxW,
// and returns where it ended.
func (c *canvas) text(x, y, size float64, s string, col color.RGBA, maxW float64) float64 {
	fc := face(size * c.scale)
	s = fit(fc, s, maxW*c.scale)
	d := font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: fc,
		Dot: fixed.Point26_6{X: fixed.Int26_6(math.Round(x * c.scale * 64)), Y: fixed.Int26_6(math.Round(y*c.scale) * 64)}}
	d.DrawString(s)
	return float64(d.Dot.X) / 64 / c.scale
}

// spans draws runs of text one after another on a line, cutting the last
// one that does not fit.
func (c *canvas) spans(x, y, size, maxW float64, runs []span, _ float64) {
	end := x + maxW
	for _, r := range runs {
		if x >= end-4 {
			return
		}
		x = c.text(x, y, size, r.text, r.col, end-x)
	}
}

func (c *canvas) measure(s string, size float64) float64 {
	return float64(font.MeasureString(face(size*c.scale), s)) / 64 / c.scale
}

func fit(fc font.Face, s string, maxPx float64) string {
	if float64(font.MeasureString(fc, s))/64 <= maxPx {
		return s
	}
	r := []rune(s)
	lo, hi := 0, len(r)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if float64(font.MeasureString(fc, strings.TrimRight(string(r[:mid]), " ")+"…"))/64 <= maxPx {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 {
		return ""
	}
	return strings.TrimRight(string(r[:lo]), " ") + "…"
}

// --- shapes ---------------------------------------------------------------------

func (c *canvas) fill(pts [][2]float64, col color.RGBA) {
	if len(pts) < 3 || col.A == 0 {
		return
	}
	b := c.img.Bounds()
	r := vector.NewRasterizer(b.Dx(), b.Dy())
	r.MoveTo(float32(pts[0][0]*c.scale), float32(pts[0][1]*c.scale))
	for _, p := range pts[1:] {
		r.LineTo(float32(p[0]*c.scale), float32(p[1]*c.scale))
	}
	r.ClosePath()
	r.DrawOp = draw.Over
	r.Draw(c.img, b, image.NewUniform(col), image.Point{})
}

func arcPts(pts [][2]float64, cx, cy, rad, a0, a1 float64) [][2]float64 {
	n := max(8, int(math.Abs(a1-a0)*rad*2))
	for i := 0; i <= n; i++ {
		a := a0 + (a1-a0)*float64(i)/float64(n)
		pts = append(pts, [2]float64{cx + rad*math.Cos(a), cy + rad*math.Sin(a)})
	}
	return pts
}

func (c *canvas) circle(cx, cy, rad float64, col color.RGBA) {
	c.fill(arcPts(nil, cx, cy, rad, 0, 2*math.Pi), col)
}

func (c *canvas) ring(cx, cy, rad, stroke float64, col color.RGBA) {
	outer := arcPts(nil, cx, cy, rad, 0, 2*math.Pi)
	inner := arcPts(nil, cx, cy, rad-stroke, 2*math.Pi, 0)
	c.fill(append(outer, inner...), col)
}

func (c *canvas) pie(cx, cy, rad, frac float64, col color.RGBA) {
	a0 := -math.Pi / 2
	pts := [][2]float64{{cx, cy}}
	c.fill(arcPts(pts, cx, cy, rad, a0, a0+2*math.Pi*math.Min(frac, 1)), col)
}

func (c *canvas) rect(x0, y0, x1, y1 float64, col color.RGBA) {
	c.fill([][2]float64{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}, col)
}

func (c *canvas) roundRect(x0, y0, x1, y1, rad float64, col color.RGBA) {
	rad = math.Min(rad, math.Min(x1-x0, y1-y0)/2)
	var p [][2]float64
	p = arcPts(p, x1-rad, y0+rad, rad, -math.Pi/2, 0)
	p = arcPts(p, x1-rad, y1-rad, rad, 0, math.Pi/2)
	p = arcPts(p, x0+rad, y1-rad, rad, math.Pi/2, math.Pi)
	p = arcPts(p, x0+rad, y0+rad, rad, math.Pi, 3*math.Pi/2)
	c.fill(p, col)
}

// summarySpans is the counts line: "1 needs you  2 done  3 working".
func summarySpans(agents []radar.Agent, p palette) []span {
	n := radar.Counts(agents)
	var out []span
	add := func(count int, word string, col color.RGBA) {
		if count == 0 {
			return
		}
		if len(out) > 0 {
			out = append(out, span{"   ", p.faint})
		}
		out = append(out, span{fmt.Sprintf("%d %s", count, word), col})
	}
	needs := "need you"
	if n[radar.Blocked] == 1 {
		needs = "needs you"
	}
	add(n[radar.Blocked], needs, p.blockedText)
	add(n[radar.Error], "failed", p.failedText)
	add(n[radar.Done], "done", p.doneText)
	add(n[radar.Working], "working", p.body)
	add(n[radar.Idle], "idle", p.faint)
	if len(out) == 0 {
		return []span{{"No agents", p.faint}}
	}
	return out
}
