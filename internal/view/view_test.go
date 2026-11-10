package view

import (
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/rynf1t/agent-radar/internal/radar"
	"github.com/rynf1t/craftkit/termcolor"
)

func decode(s string) []input {
	var d decoder
	var out []input
	d.feed([]byte(s), func(i input) { out = append(out, i) })
	return out
}

func TestDecoder(t *testing.T) {
	got := decode("jk\x1b[A\x1b[B\x1bOA\r\x7f/\x03")
	want := []keyCode{'j', 'k', keyUp, keyDown, keyUp, keyEnter, keyBackspace, '/', 'c'}
	if len(got) != len(want) {
		t.Fatalf("got %d inputs: %+v", len(got), got)
	}
	for i, k := range want {
		if got[i].key != k {
			t.Errorf("input %d: got %v, want %v", i, got[i].key, k)
		}
	}
	if !got[len(got)-1].ctrl {
		t.Error("0x03 should be ctrl+c")
	}

	if in := decode("\x1b"); len(in) != 1 || in[0].key != keyEscape {
		t.Errorf("a lone ESC is the Escape key: %+v", in)
	}
	if in := decode("\x1b]11;rgb:1e1e/1f1f/2222\x07"); len(in) != 1 || in[0].osc != "11;rgb:1e1e/1f1f/2222" {
		t.Errorf("OSC reply: %+v", in)
	}
	if in := decode("\x1b]10;rgb:ffff/ffff/ffff\x1b\\"); len(in) != 1 || in[0].osc == "" {
		t.Errorf("ST-terminated OSC reply: %+v", in)
	}
	if in := decode("\x1b[I\x1b[O"); len(in) != 2 || in[0].focus != 1 || in[1].focus != -1 {
		t.Errorf("focus reports: %+v", in)
	}
	if in := decode("\x1b[<0;5;12M\x1b[<0;5;12m\x1b[<65;1;1M"); len(in) != 2 || in[0].click == nil || in[0].click.row != 12 || in[1].wheel != 1 {
		t.Errorf("mouse: %+v", in)
	}
	if in := decode("\x1b[200~web\x1b[201~"); len(in) != 1 || in[0].paste != "web" {
		t.Errorf("paste: %+v", in)
	}
	if in := decode("é"); len(in) != 1 || in[0].key != 'é' {
		t.Errorf("UTF-8: %+v", in)
	}
}

// A sequence split across reads still decodes.
func TestDecoderSplit(t *testing.T) {
	var d decoder
	var out []input
	emit := func(i input) { out = append(out, i) }
	d.feed([]byte("\x1b]11;rgb:00"), emit)
	d.feed([]byte("00/0000/0000\x07"), emit)
	if len(out) != 1 || out[0].osc != "11;rgb:0000/0000/0000" {
		t.Fatalf("got %+v", out)
	}
}

func TestTween(t *testing.T) {
	saved := reduceMotion
	reduceMotion = false
	defer func() { reduceMotion = saved }()
	now := time.Now()
	tw := still(0)
	tw.toward(100, now, durMove)
	if !tw.moving(now) {
		t.Fatal("should be moving")
	}
	mid := tw.at(now.Add(durMove / 2))
	if mid < 90 || mid >= 100 {
		t.Errorf("expo ease-out should be most of the way at half time: %v", mid)
	}
	if got := tw.at(now.Add(durMove)); got != 100 {
		t.Errorf("should land exactly: %v", got)
	}
	if tw.moving(now.Add(durMove)) {
		t.Error("should stop when done")
	}
	reduceMotion = true
	tw.toward(0, now, durMove)
	if tw.moving(now) || tw.at(now) != 0 {
		t.Error("reduce motion should jump")
	}
}

func TestLegible(t *testing.T) {
	white, black := color.RGBA{255, 255, 255, 255}, color.RGBA{0, 0, 0, 255}
	pale := color.RGBA{250, 230, 120, 255} // a yellow that vanishes on white
	got := legible(pale, black, white)
	if termcolor.Contrast(got, white) < 3.2 {
		t.Errorf("legible gave %v with contrast %.2f", got, termcolor.Contrast(got, white))
	}
}

func agentsForRender() []radar.Agent {
	at := time.Now().Add(-3 * time.Minute)
	return []radar.Agent{
		{BlockID: "block:1", SessionID: "session:a", Session: "web", State: radar.Blocked, Kind: "question", App: "claude-code", Title: "Ship it", Msg: "Commit?", Since: &at},
		{BlockID: "block:2", SessionID: "session:a", Session: "web", State: radar.Working, App: "codex", Title: "Tests", Since: &at},
		{BlockID: "block:3", SessionID: "session:b", Session: "api", State: radar.Done, App: "claude-code", Title: "Refactor", Since: &at},
	}
}

// The first row's selection and state dot land where the layout says.
func TestRenderProbe(t *testing.T) {
	v := &viewer{scanned: true, connected: true, focused: true, byKey: map[string]item{}, ys: map[string]*tween{},
		alphas: map[string]*tween{}, ghosts: map[string]item{}, selY: still(math.NaN()), scroll: still(0)}
	v.pal = paletteFor(fallbackDark)
	v.agents = agentsForRender()
	v.scale = 2
	v.ws = winSize{cols: 40, rows: 40, xpix: 40 * 16, ypix: 40 * 34}
	v.rebuild(time.Now())
	f := v.frameAt(time.Now())
	f.opaque = true
	img := render(f)

	rows := v.rows(v.items)
	if len(rows) != 3 || v.sel != rows[0].key {
		t.Fatalf("rows %d, sel %q", len(rows), v.sel)
	}
	px := func(x, y float64) color.RGBA { return img.RGBAAt(int(x*2), int(y*2)) }
	// The blocked dot is the theme's yellow.
	dot := px(padX+glyphR+1, rows[0].y+15)
	if dot != v.pal.blocked {
		t.Errorf("blocked dot is %v, want %v", dot, v.pal.blocked)
	}
	// The selection tints the background under the first row, not the second.
	sel := px(padX+textX/2, rows[0].y+rowH-6)
	if sel == v.pal.bg {
		t.Error("no selection under the first row")
	}
	if px(padX+textX/2, rows[1].y+rowH-6) != v.pal.bg {
		t.Error("selection under the second row")
	}
	// The done dot is the accent.
	if got := px(padX+glyphR+1, rows[2].y+15); got != v.pal.accent {
		t.Errorf("done dot is %v, want %v", got, v.pal.accent)
	}
}

// When an agent becomes blocked it moves to the top, and its row glides
// there instead of jumping.
func TestReorderGlides(t *testing.T) {
	saved := reduceMotion
	reduceMotion = false
	defer func() { reduceMotion = saved }()
	v := &viewer{scanned: true, connected: true, byKey: map[string]item{}, ys: map[string]*tween{},
		alphas: map[string]*tween{}, ghosts: map[string]item{}, selY: still(math.NaN()), scroll: still(0)}
	v.pal = paletteFor(fallbackDark)
	v.scale = 2
	v.ws = winSize{cols: 40, rows: 40, xpix: 40 * 16, ypix: 40 * 34}
	agents := agentsForRender()[1:] // working, done
	v.agents = agents
	now := time.Now()
	v.rebuild(now)
	before := v.ys["b:block:2"].to
	agents[0].State = radar.Blocked
	v.agents = []radar.Agent{agents[0], agents[1]}
	v.rebuild(now)
	tw := v.ys["b:block:2"]
	if tw.to == before && !v.moving(now) {
		t.Skip("order did not change")
	}
	if !v.moving(now.Add(durMove/3)) || v.moving(now.Add(durFadeOut+time.Millisecond)) {
		t.Error("rows should glide for the move duration, then stop")
	}
}
