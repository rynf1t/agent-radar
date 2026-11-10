// Package view draws the radar in a terminal with kitty graphics and
// handles its keys.
package view

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/rynf1t/agent-radar/internal/radar"
	"github.com/rynf1t/agent-radar/internal/rex"
	"github.com/rynf1t/craftkit/termcolor"
)

// Options configure the viewer.
type Options struct {
	Theme string // auto follows the terminal. light or dark pin a palette.
	Popup bool   // a glance-and-go float: esc closes it, and so does a jump
	Toast string // show only this block's agent as a notification card
	Back  string // the block a popup or a clicked toast focuses again on close, unless it jumped within its session
}

// toastFor is how long a toast stays when nobody touches it.
const toastFor = 30 * time.Second

func (o Options) closesOnJump() bool { return o.Popup || o.Toast != "" }

type winSize struct{ cols, rows, xpix, ypix int }

func getWinSize(fd int) winSize {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil || ws.Row == 0 || ws.Col == 0 {
		return winSize{cols: 40, rows: 40, xpix: 40 * 16, ypix: 40 * 34}
	}
	w := winSize{cols: int(ws.Col), rows: int(ws.Row), xpix: int(ws.Xpixel), ypix: int(ws.Ypixel)}
	if w.xpix == 0 || w.ypix == 0 {
		w.xpix, w.ypix = w.cols*16, w.rows*34 // no pixel size reported: assume a Retina cell
	}
	return w
}

// pickScale sizes points to the terminal's cells: a 13pt font gives cells
// about 17pt tall, so 34px cells mean 2x. Half steps keep a Mac at exactly 2.
func pickScale(ws winSize) float64 {
	if v, err := strconv.ParseFloat(os.Getenv("RADAR_SCALE"), 64); err == nil && v > 0 {
		return v
	}
	return max(1, math.Round(float64(ws.ypix)/float64(ws.rows)/17*2)/2)
}

func newViewer(opt Options) *viewer {
	return &viewer{opt: opt, byKey: map[string]item{}, ys: map[string]*tween{}, alphas: map[string]*tween{},
		ghosts: map[string]item{}, selY: still(math.NaN()), scroll: still(0)}
}

// viewer is the state of a running view.
type viewer struct {
	opt   Options
	theme termcolor.Theme
	pal   palette

	agents    []radar.Agent // all of them, display order
	scanned   bool
	connected bool

	items  []item
	byKey  map[string]item
	height float64 // content height in points
	ys     map[string]*tween
	alphas map[string]*tween
	ghosts map[string]item // removed items fading out
	sel    string          // selected item key
	selY   *tween
	scroll *tween

	focused bool // the pane has keyboard focus
	query   string
	filter  bool // the filter bar is open
	typing  bool // keys go to the filter field
	note    string
	noteEnd time.Time

	ws    winSize
	scale float64
	imgID int
	start bool
}

// Run shows the radar until the user quits or ctx ends.
func Run(ctx context.Context, c *rex.Client, opt Options) (err error) {
	in, out := int(os.Stdin.Fd()), os.Stdout
	if !term.IsTerminal(in) {
		return fmt.Errorf("radar view needs a terminal. Use `radar ls` in scripts")
	}
	old, err := term.MakeRaw(in)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(out, 1<<16)
	// Alternate screen, hidden cursor, focus reports, SGR mouse clicks and
	// wheel (no motion tracking), bracketed paste.
	fmt.Fprint(w, "\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J\x1b[?1004h\x1b[?1000h\x1b[?1006h\x1b[?2004h")
	w.Flush()
	restore := func() {
		fmt.Fprint(w, "\x1b_Ga=d,d=A,q=2\x1b\\\x1b[?2004l\x1b[?1006l\x1b[?1000l\x1b[?1004l\x1b[?25h\x1b[?1049l")
		w.Flush()
		term.Restore(in, old)
	}
	defer restore() // deferred calls run while a panic unwinds too

	// Rex picks some other pane when a float closes, so a popup hands focus
	// back to the pane it was opened over, unless it jumped within its own
	// session. A toast opens without focus, so it only does this once
	// touched.
	home := os.Getenv("REX_SESSION")
	jumpedHome, touched := false, opt.Popup
	if opt.closesOnJump() && opt.Back != "" {
		defer func() {
			if touched && !jumpedHome {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				c.Focus(ctx, home, opt.Back)
			}
		}()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	inputs := make(chan input, 64)
	go func() {
		var d decoder
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				close(inputs)
				return
			}
			d.feed(buf[:n], func(i input) { inputs <- i })
		}
	}()

	lv := newLive(c)
	lv.run(ctx)

	v := newViewer(opt)
	v.focused = opt.Popup // a popup opens focused, before any focus report

	// The first frame waits for the theme so it has the right colors. A new
	// pane may not be attached yet, so termcolor.Wait asks again until it is.
	var pending []input
	if opt.Theme == "auto" {
		replies := make(chan string)
		stop := make(chan struct{})
		var mu sync.Mutex
		var fwd sync.WaitGroup
		fwd.Go(func() {
			for {
				select {
				case <-stop:
					return
				case i, ok := <-inputs:
					if !ok {
						close(replies)
						return
					}
					if i.osc != "" {
						select {
						case replies <- i.osc:
						case <-stop:
							return
						}
						continue
					}
					mu.Lock()
					pending = append(pending, i)
					mu.Unlock()
				}
			}
		})
		werr := termcolor.Wait(ctx, writerFunc(func(p []byte) (int, error) {
			n, err := w.Write(p)
			w.Flush()
			return n, err
		}), replies, &v.theme, nil, termcolor.Red, termcolor.Yellow, termcolor.Blue)
		close(stop)
		fwd.Wait()
		if werr != nil {
			return nil
		}
	}
	v.repalette()

	frames := time.NewTicker(frameEvery)
	frames.Stop()
	animating := false
	startFrames := func() {
		if !animating {
			frames.Reset(frameEvery)
			animating = true
		}
	}
	redraw := func(now time.Time) {
		v.draw(w, now)
		w.Flush()
		if v.moving(now) {
			startFrames()
		} else if animating {
			frames.Stop()
			animating = false
		}
	}

	ages := time.NewTicker(10 * time.Second)
	defer ages.Stop()
	recolor := time.NewTicker(time.Second)
	defer recolor.Stop()
	var repaint <-chan time.Time
	focusDone := make(chan error, 1)
	var noteTimer <-chan time.Time

	v.resize()
	v.rebuild(time.Now())
	redraw(time.Now())
	lastSig := ""
	var jumpTo radar.Agent
	jump := func(a radar.Agent) {
		jumpTo = a
		go func() { focusDone <- Focus(ctx, c, a) }()
	}
	handle := func(i input) bool {
		now := time.Now()
		switch {
		case i.osc != "":
			if v.theme.Apply(i.osc) && repaint == nil {
				repaint = time.After(30 * time.Millisecond) // a theme change answers in several replies
			}
			return false
		case i.focus != 0:
			v.focused = i.focus > 0
			v.rebuildSel(now)
		case i.click != nil && opt.Toast != "":
			touched = true
			if shown := v.visible(); len(shown) == 1 {
				jump(shown[0])
			}
			return false
		case i.click != nil:
			v.focused = true
			if a, ok := v.agentAtCell(i.click.row); ok {
				v.sel = "b:" + a.BlockID
				v.rebuildSel(now)
				jump(a)
			}
		case i.wheel != 0:
			v.move(i.wheel, now)
		case i.paste != "" && v.typing:
			v.query += oneLine(i.paste)
			v.rebuild(now)
		case i.key != keyNone && i.key != 0:
			v.focused, touched = true, true
			quit, wantJump := v.key(i, now)
			if quit {
				return true
			}
			if a, ok := v.selected(); ok && wantJump {
				jump(a)
			}
		default:
			return false
		}
		redraw(now)
		return false
	}
	for _, i := range pending {
		if handle(i) {
			return nil
		}
	}

	// A toast leaves when its agent moves on: answered, looked at, closed.
	var toastEnd <-chan time.Time
	var toastState radar.State
	if opt.Toast != "" {
		toastEnd = time.After(toastFor)
	}
	toastOver := func() bool {
		if opt.Toast == "" || !v.scanned {
			return false
		}
		shown := v.visible()
		if len(shown) == 0 {
			return true
		}
		a := shown[0]
		if toastState == "" {
			toastState = a.Urgency()
		}
		return a.Urgency() != toastState
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-hup:
			return nil
		case i, ok := <-inputs:
			if !ok || handle(i) {
				return nil
			}
		case <-lv.changed:
			// Rescans report the same picture most of the time: draw only
			// when something the view shows has changed.
			now := time.Now()
			v.agents, v.scanned, v.connected = lv.snapshot()
			if toastOver() {
				return nil
			}
			if sig := v.signature(now); sig != lastSig {
				lastSig = sig
				v.rebuild(now)
				redraw(now)
			}
		case now := <-frames.C:
			redraw(now)
		case <-toastEnd:
			return nil
		case <-ages.C:
			if sig := v.signature(time.Now()); sig != lastSig {
				lastSig = sig
				redraw(time.Now())
			}
		case <-recolor.C:
			if v.opt.Theme == "auto" {
				fmt.Fprint(w, termcolor.Query(termcolor.Red, termcolor.Yellow, termcolor.Blue))
				w.Flush()
			}
		case <-repaint:
			repaint = nil
			if v.repalette() {
				redraw(time.Now())
			}
		case <-winch:
			v.resize()
			v.rebuild(time.Now())
			redraw(time.Now())
		case err := <-focusDone:
			if err == nil && opt.closesOnJump() {
				jumpedHome = jumpTo.SessionID == home
				return nil
			}
			if err != nil {
				v.note = firstSentence(err.Error())
				if strings.Contains(err.Error(), "Remote Control") {
					v.note = "Turn on Remote Control in Rex Settings to jump"
				}
				v.noteEnd = time.Now().Add(6 * time.Second)
				noteTimer = time.After(6 * time.Second)
				redraw(time.Now())
			}
		case <-noteTimer:
			noteTimer = nil
			redraw(time.Now())
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// signature is everything the list shows that can change on its own, so a
// rescan that finds the same picture draws nothing.
func (v *viewer) signature(now time.Time) string {
	var b strings.Builder
	fmt.Fprint(&b, v.scanned, v.connected, "\x00")
	for _, a := range v.agents {
		prog := ""
		for _, r := range a.Records {
			if r.Progress != nil {
				prog += strconv.Itoa(*r.Progress) + ","
			}
		}
		fmt.Fprint(&b, a.BlockID, a.SessionID, a.Session, a.State, a.Seen, a.Kind, a.Title, a.Msg, a.App, a.CWD,
			radar.Ago(a.Since, now), prog, "\x00")
	}
	return b.String()
}

// repalette recomputes the palette and reports whether it changed.
func (v *viewer) repalette() bool {
	var p palette
	switch v.opt.Theme {
	case "light":
		p = paletteFor(fallbackLight)
	case "dark":
		p = paletteFor(fallbackDark)
	default:
		p = paletteFor(v.theme)
	}
	changed := p != v.pal
	v.pal = p
	return changed
}

func (v *viewer) visible() []radar.Agent {
	if v.opt.Toast != "" {
		for _, a := range v.agents {
			if a.BlockID == v.opt.Toast {
				return []radar.Agent{a}
			}
		}
		return nil
	}
	if strings.TrimSpace(v.query) == "" {
		return v.agents
	}
	var out []radar.Agent
	for _, a := range v.agents {
		if a.Matches(v.query) {
			out = append(out, a)
		}
	}
	return out
}

func (v *viewer) resize() {
	v.ws = getWinSize(int(os.Stdout.Fd()))
	v.scale = pickScale(v.ws)
}

// rebuild lays the list out again and points every item at its new place.
func (v *viewer) rebuild(now time.Time) {
	items, height := layout(v.visible())
	first := !v.start
	v.start = v.start || v.scanned
	next := make(map[string]item, len(items))
	for _, it := range items {
		next[it.key] = it
		if t, ok := v.ys[it.key]; ok && !first {
			t.toward(it.y, now, durMove)
			delete(v.ghosts, it.key)
			if a := v.alphas[it.key]; a.to < 1 {
				a.toward(1, now, durFadeIn)
			}
			continue
		}
		v.ys[it.key] = still(it.y)
		if first {
			v.alphas[it.key] = still(1)
		} else {
			v.alphas[it.key] = still(0)
			v.alphas[it.key].toward(1, now, durFadeIn)
		}
	}
	for key, it := range v.byKey {
		if _, ok := next[key]; !ok {
			v.ghosts[key] = it
			v.alphas[key].toward(0, now, durFadeOut)
		}
	}
	// The selection stays on its agent. If that agent left, it takes the
	// row that is now where it was.
	if _, ok := next[v.sel]; !ok {
		old := slices.IndexFunc(v.rows(v.items), func(it item) bool { return it.key == v.sel })
		rows := v.rows(items)
		v.sel = ""
		if len(rows) > 0 {
			v.sel = rows[min(max(old, 0), len(rows)-1)].key
		}
	}
	v.items, v.byKey, v.height = items, next, height
	v.rebuildSel(now)
}

func (v *viewer) rows(items []item) []item {
	var out []item
	for _, it := range items {
		if it.kind == kindRow {
			out = append(out, it)
		}
	}
	return out
}

// rebuildSel moves the selection and scroll to follow the selected row.
func (v *viewer) rebuildSel(now time.Time) {
	it, ok := v.byKey[v.sel]
	if !ok {
		v.selY = still(math.NaN())
		return
	}
	if math.IsNaN(v.selY.to) {
		v.selY = still(it.y)
	} else {
		v.selY.toward(it.y, now, durSelect)
	}
	viewH := v.listHeight()
	maxOff := math.Max(0, v.height-viewH)
	off := v.scroll.to
	switch {
	case it.y-headerH-headerGap < off:
		off = it.y - headerH - headerGap
	case it.y+rowH+rowGap > off+viewH:
		off = it.y + rowH + rowGap - viewH
	}
	v.scroll.toward(math.Max(0, math.Min(off, maxOff)), now, durScroll)
}

func (v *viewer) listHeight() float64 {
	h := float64(v.ws.ypix/v.ws.rows*v.ws.rows) / v.scale
	if v.filter {
		return h - barH
	}
	if v.focused {
		return h - 24
	}
	return h
}

func (v *viewer) selected() (radar.Agent, bool) {
	it, ok := v.byKey[v.sel]
	if !ok || it.kind != kindRow {
		return radar.Agent{}, false
	}
	return it.agent, true
}

func (v *viewer) move(d int, now time.Time) {
	rows := v.rows(v.items)
	if len(rows) == 0 {
		return
	}
	i := slices.IndexFunc(rows, func(it item) bool { return it.key == v.sel })
	switch {
	case d == math.MinInt:
		i = 0
	case d == math.MaxInt:
		i = len(rows) - 1
	default:
		i = min(max(i+d, 0), len(rows)-1)
	}
	v.sel = rows[i].key
	v.rebuildSel(now)
}

// key handles a key press. It reports whether to quit and whether to jump
// to the selected agent.
func (v *viewer) key(i input, now time.Time) (quit, jump bool) {
	k := i.key
	if i.ctrl && k == 'c' {
		if v.filter {
			v.closeFilter(now)
			return false, false
		}
		return true, false
	}
	switch k {
	case keyUp:
		v.move(-1, now)
		return
	case keyDown:
		v.move(1, now)
		return
	case keyPageUp:
		v.move(-5, now)
		return
	case keyPageDown:
		v.move(5, now)
		return
	case keyEnter:
		if v.typing && strings.TrimSpace(v.query) == "" {
			v.closeFilter(now)
			return false, false
		}
		return false, true
	case keyEscape:
		if v.filter {
			v.closeFilter(now)
			return false, false
		}
		return v.opt.closesOnJump(), false
	}
	if v.typing {
		switch {
		case k == keyBackspace:
			if r := []rune(v.query); len(r) > 0 {
				v.query = string(r[:len(r)-1])
				v.rebuild(now)
			}
		case i.ctrl && k == 'u':
			v.query = ""
			v.rebuild(now)
		case !i.ctrl && k >= ' ':
			v.query += string(rune(k))
			v.rebuild(now)
		}
		return
	}
	if i.ctrl {
		switch k {
		case 'n':
			v.move(1, now)
		case 'p':
			v.move(-1, now)
		case 'l':
			v.rebuild(now)
		}
		return
	}
	switch k {
	case 'q':
		return true, false
	case 'j':
		v.move(1, now)
	case 'k':
		v.move(-1, now)
	case 'g', keyHome:
		v.move(math.MinInt, now)
	case 'G', keyEnd:
		v.move(math.MaxInt, now)
	case '/':
		v.filter, v.typing = true, true
		v.rebuild(now)
	case ' ':
		return false, true
	}
	return
}

func (v *viewer) closeFilter(now time.Time) {
	v.filter, v.typing, v.query = false, false, ""
	v.rebuild(now)
}

// agentAtCell is the agent whose row covers terminal row r (1-based).
func (v *viewer) agentAtCell(r int) (radar.Agent, bool) {
	cellH := float64(v.ws.ypix / v.ws.rows)
	y := (float64(r-1)*cellH+cellH/2)/v.scale + v.scroll.at(time.Now())
	for _, it := range v.items {
		if it.kind == kindRow && y >= it.y && y < it.y+it.h {
			return it.agent, true
		}
	}
	return radar.Agent{}, false
}

func (v *viewer) moving(now time.Time) bool {
	if v.selY.moving(now) || v.scroll.moving(now) {
		return true
	}
	for _, t := range v.ys {
		if t.moving(now) {
			return true
		}
	}
	for _, t := range v.alphas {
		if t.moving(now) {
			return true
		}
	}
	return false
}

// frameAt is the picture at now, with every eased value at its point.
func (v *viewer) frameAt(now time.Time) frame {
	cellW, cellH := v.ws.xpix/v.ws.cols, v.ws.ypix/v.ws.rows
	f := frame{
		pal: v.pal, scale: v.scale, widthPx: cellW * v.ws.cols, heightPx: cellH * v.ws.rows,
		alpha: map[string]float64{}, now: now, selY: math.NaN(),
	}
	px := func(pt float64) float64 { return math.Round(pt*v.scale) / v.scale } // whole pixels
	f.offset = px(v.scroll.at(now))
	f.contentH = v.height
	for _, it := range v.items {
		it.y = px(v.ys[it.key].at(now))
		f.items = append(f.items, it)
		f.alpha[it.key] = v.alphas[it.key].at(now)
	}
	for key, it := range v.ghosts {
		a := v.alphas[key].at(now)
		if a <= 0 {
			delete(v.ghosts, key)
			delete(v.alphas, key)
			delete(v.ys, key)
			continue
		}
		it.y = px(v.ys[key].at(now))
		f.items = append(f.items, it)
		f.alpha[key] = a
	}
	if v.focused && !math.IsNaN(v.selY.to) {
		f.selY = px(v.selY.at(now))
	}
	f.summary = summarySpans(v.visible(), v.pal)
	if !v.scanned {
		f.summary = []span{{"Looking for agents…", v.pal.faint}}
	}
	switch {
	case !v.scanned:
	case len(v.agents) == 0:
		f.empty = "No agents in Rex right now"
	case len(v.items) == 0:
		f.empty = "Nothing matches “" + strings.TrimSpace(v.query) + "”"
	}
	if v.filter {
		f.filter = &filterBar{query: v.query, typing: v.typing}
		if strings.TrimSpace(v.query) != "" {
			f.filter.count = fmt.Sprintf("%d of %d", len(v.rows(v.items)), len(v.agents))
		}
	} else if v.focused {
		f.hint = "↑↓ move    enter jump    / filter    q quit"
		if v.opt.Popup {
			f.hint = "↑↓ move    enter jump    / filter    esc close"
		}
	}
	if v.scanned && !v.connected {
		f.status, f.dim = "Can't reach Rex. Retrying…", true
	} else if v.note != "" && now.Before(v.noteEnd) {
		f.status = v.note
	}
	return f
}

func (v *viewer) draw(w *bufio.Writer, now time.Time) {
	f := v.frameAt(now)
	var img *image.RGBA
	if shown := v.visible(); v.opt.Toast != "" && len(shown) == 1 {
		img = renderToast(f, shown[0])
	} else {
		img = render(f)
	}
	prev := v.imgID
	v.imgID = v.imgID%(1<<20) + 1 // IDs wrap, and prev is always the image on screen
	swap(w, img, prev, v.imgID, v.ws.cols, v.ws.rows)
}

// RenderToastPNG draws a as the toast shows it, widthPx wide, to path.
func RenderToastPNG(path string, a radar.Agent, widthPx int, scale float64, theme string) error {
	th := fallbackDark
	if theme == "light" {
		th = fallbackLight
	}
	f := frame{pal: paletteFor(th), scale: scale, widthPx: widthPx, heightPx: int(math.Ceil(toastH * scale)),
		opaque: true, now: time.Now()}
	return writePNG(path, renderToast(f, a))
}

func writePNG(path string, img image.Image) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// RenderPNG draws agents as the pane would show them, focused with the first
// row selected, to path. A height of 0 fits the content.
func RenderPNG(path string, agents []radar.Agent, widthPx, heightPx int, scale float64, theme string) error {
	th := fallbackDark
	if theme == "light" {
		th = fallbackLight
	}
	v := newViewer(Options{Theme: theme})
	v.agents, v.scanned, v.connected, v.focused = agents, true, true, true
	v.pal = paletteFor(th)
	v.scale = scale
	items, height := layout(agents)
	if heightPx == 0 {
		heightPx = int(math.Ceil((height + 24) * scale))
	}
	cell := int(math.Round(17 * scale))
	v.ws = winSize{cols: max(1, widthPx/cell), rows: max(1, heightPx/cell), xpix: widthPx, ypix: heightPx}
	v.rebuild(time.Now())
	if rows := v.rows(items); len(rows) > 0 {
		v.sel = rows[0].key
		v.selY = still(rows[0].y)
	}
	f := v.frameAt(time.Now())
	f.widthPx, f.heightPx, f.opaque = widthPx, heightPx, true
	return writePNG(path, render(f))
}
