package view

import (
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"time"

	"github.com/rynf1t/agent-radar/internal/radar"
	"github.com/rynf1t/agent-radar/internal/rex"
)

// WatchOptions configure Watch.
type WatchOptions struct {
	Bin    string        // the radar binary each toast runs
	States []radar.State // the states that earn a toast
	Log    io.Writer     // one line per toast, and why one was skipped
}

const (
	toastLabel = "Radar alert"
	toastAgain = 20 * time.Second // the least time between two toasts for one agent
	toastWidth = 360.0            // points
)

// Watch follows every agent and, when one reaches a state in opt.States in
// a session the Rex app isn't showing, opens a toast in the session it is
// showing. Rex sends its own macOS notifications only while it is in the
// background. This covers the time you are in Rex, looking elsewhere.
func Watch(ctx context.Context, c *rex.Client, opt WatchOptions) error {
	lv := newLive(c)
	lv.run(ctx)
	var prev map[string]string
	last := map[string]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-lv.changed:
		}
		agents, scanned, connected := lv.snapshot()
		if !scanned || !connected {
			continue
		}
		var news []radar.Agent
		news, prev = fresh(prev, agents, opt.States)
		for _, a := range news {
			if time.Since(last[a.BlockID]) < toastAgain {
				continue
			}
			if err := toast(ctx, c, opt, a); err != nil {
				logf(opt.Log, "no toast for %s: %v", a.Session, err)
				continue
			}
			last[a.BlockID] = time.Now()
			logf(opt.Log, "%s in %s: %s", a.StateText(), a.Session, a.Title)
		}
	}
}

func logf(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "%s radar: %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// fresh returns the agents that have just reached one of states, and the
// picture to compare the next snapshot with. The first snapshot only sets
// the picture, so starting the watcher doesn't announce old news.
func fresh(prev map[string]string, agents []radar.Agent, states []radar.State) ([]radar.Agent, map[string]string) {
	cur := make(map[string]string, len(agents))
	var news []radar.Agent
	for _, a := range agents {
		k := string(a.Urgency())
		if a.Since != nil {
			k += "@" + a.Since.Format(time.RFC3339Nano)
		}
		cur[a.BlockID] = k
		if prev != nil && prev[a.BlockID] != k && slices.Contains(states, a.Urgency()) {
			news = append(news, a)
		}
	}
	return news, cur
}

// toast opens a's card in the top right of the window the app shows,
// below any toasts already there.
func toast(ctx context.Context, c *rex.Client, opt WatchOptions, a radar.Agent) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	shown, err := rex.ShownSession(ctx)
	if err != nil {
		return err
	}
	view, err := c.View(ctx, shown)
	if err != nil {
		return fmt.Errorf("reading the shown session: %w", err)
	}
	var win *rex.Window
	for i := range view.Windows {
		if view.Windows[i].ID == view.ActiveWindowID {
			win = &view.Windows[i]
		}
	}
	if win == nil {
		return fmt.Errorf("the shown session has no active window")
	}
	if a.SessionID == shown && a.WindowID == win.ID {
		return fmt.Errorf("its tab is on screen")
	}
	size, err := c.Size(ctx, shown, win.FocusedBlockID)
	if err != nil || size.Rows == 0 || size.CellHeight == 0 {
		return fmt.Errorf("can't size the window")
	}
	bounds, ok := toastBounds(size, countToasts(win))
	if !ok {
		return fmt.Errorf("no room for another toast")
	}
	command := []string{opt.Bin, "view", "--toast", a.BlockID, "--back", win.FocusedBlockID}
	_, err = c.NewLayer(ctx, shown, win.ID, bounds, toastLabel, command)
	return err
}

func countToasts(win *rex.Window) int {
	n := 0
	for _, l := range win.Layers {
		for _, b := range l.Blocks {
			if b.Label == toastLabel {
				n++
			}
		}
	}
	return n
}

// toastBounds places toast number n (0 first) in the top right corner, as
// fractions of the window. size is the focused pane's, which is the
// window's height unless panes are stacked, and at most its width, so a
// toast can come out a little large but never cut off.
func toastBounds(size rex.Size, n int) ([4]float64, bool) {
	scale := max(1, math.Round(float64(size.CellHeight)/17*2)/2) // as the viewer picks it
	rows := math.Ceil(toastH*scale/float64(size.CellHeight)) + 1
	cols := math.Ceil(toastWidth*scale/float64(size.CellWidth)) + 2
	h := math.Min(0.45, rows/float64(size.Rows))
	w := math.Min(0.5, math.Max(0.25, cols/float64(max(size.Columns, 1))))
	const margin = 0.015
	y := margin + float64(n)*(h+margin)
	if y+h > 1 {
		return [4]float64{}, false
	}
	return [4]float64{1 - w - margin, y, w, h}, true
}
