// Package radar finds the coding agents running in Rex terminals and orders
// them by what needs the user first.
package radar

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rynf1t/agent-radar/internal/rex"
)

// State is an agent's state, from the most to the least urgent.
type State string

const (
	Blocked State = "blocked" // waiting on the user: a permission, a question, a login
	Error   State = "error"   // failed and stopped
	Done    State = "done"    // finished, and the user has not looked yet
	Working State = "working"
	Idle    State = "idle"
)

// States lists every state in urgency order.
var States = []State{Blocked, Error, Done, Working, Idle}

// Rank is a state's place in urgency order, 0 first.
func (s State) Rank() int {
	if i := slices.Index(States, s); i >= 0 {
		return i
	}
	return len(States)
}

// Agent is one agent in one Rex pane.
type Agent struct {
	BlockID   string       `json:"block_id"`
	SessionID string       `json:"session_id"`
	Session   string       `json:"session"`
	WindowID  string       `json:"window_id"`
	Window    string       `json:"window"`
	Label     string       `json:"label"` // the pane's label
	State     State        `json:"state"`
	Kind      string       `json:"kind,omitempty"` // for blocked: permission, question or auth
	App       string       `json:"app"`
	Title     string       `json:"title"`
	Msg       string       `json:"msg,omitempty"`
	CWD       string       `json:"cwd,omitempty"`
	Since     *time.Time   `json:"since,omitempty"` // when the state was reported
	Focused   bool         `json:"focused"`         // the focused pane of its session's visible tab
	Seen      bool         `json:"seen"`            // done or error, and the user has looked since
	Records   []rex.Record `json:"records"`
}

// Pane is everything the radar knows about one terminal pane, agent or not.
type Pane struct {
	SessionID, Session string
	WindowID, Window   string
	BlockID, Label     string
	Focused            bool
	Records            []rex.Record
	Process            rex.Process
	Title              string
	Viewed             time.Time // when an app last showed the pane
}

// knownAgents are process names that are coding agents even before they
// report a status.
var knownAgents = map[string]string{
	"claude": "claude-code", "codex": "codex", "aider": "aider", "gemini": "gemini",
	"opencode": "opencode", "amp": "amp", "goose": "goose", "cursor-agent": "cursor-agent",
	"crush": "crush", "pi": "pi", "droid": "droid", "qwen": "qwen",
}

func procApp(p *rex.Proc) string {
	if p == nil {
		return ""
	}
	for _, n := range []string{p.Name, filepath.Base(strings.TrimPrefix(p.Argv, "-"))} {
		n = strings.ToLower(strings.TrimSuffix(n, ".exe"))
		if app, ok := knownAgents[n]; ok {
			return app
		}
	}
	return ""
}

// Agent turns a pane into an agent. A pane is an agent when a program in it
// reported a status, or its foreground program is a known agent.
func (p *Pane) Agent() (Agent, bool) {
	app := procApp(p.Process.Foreground)
	if app == "" && len(p.Records) == 0 {
		return Agent{}, false
	}
	a := Agent{
		BlockID: p.BlockID, SessionID: p.SessionID, Session: p.Session,
		WindowID: p.WindowID, Window: p.Window, Label: p.Label, Focused: p.Focused,
		State: Idle, App: app, Records: p.Records,
	}
	if a.Records == nil {
		a.Records = []rex.Record{}
	}
	if fg := p.Process.Foreground; fg != nil {
		a.CWD = fg.CWD
	}
	// The most urgent record speaks for the pane. Between equals, the newest wins.
	var top *rex.Record
	for i := range p.Records {
		r := &p.Records[i]
		if top == nil || recordState(r).Rank() < recordState(top).Rank() ||
			recordState(r) == recordState(top) && r.UpdatedAt.After(top.UpdatedAt) {
			top = r
		}
	}
	if top != nil {
		a.State, a.Kind, a.Msg = recordState(top), top.Kind, top.Msg
		if a.State != Blocked {
			a.Kind = ""
		}
		if top.EffectiveApp != "" {
			a.App = top.EffectiveApp
		}
		t := top.UpdatedAt
		a.Since = &t
		a.Title = top.Title
		// Seen means what Cmd+U in next_ready.lua means: an app showed
		// the pane after the report.
		a.Seen = (a.State == Done || a.State == Error) && p.Viewed.After(t)
	}
	if a.App == "" {
		a.App = "agent"
	}
	if a.Title == "" {
		a.Title = cleanTitle(p.Title)
	}
	if a.Title == "" && p.Label != "" && p.Label != "shell" {
		a.Title = p.Label
	}
	if a.Title == "" && a.CWD != "" {
		a.Title = filepath.Base(a.CWD)
	}
	return a, true
}

func recordState(r *rex.Record) State {
	switch s := State(r.State); s {
	case Blocked, Error, Done, Working, Idle:
		return s
	}
	return Idle // the spec says to treat an unknown state as idle
}

// cleanTitle drops the spinner or status glyph some agents put before their
// title, such as "✳ Fix the build".
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	for s != "" {
		r := []rune(s)[0]
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '~' || r == '/' || r == '.' {
			break
		}
		s = strings.TrimSpace(s[len(string(r)):])
	}
	return s
}

// ApplyStatus updates the pane's records from a program_status_changed
// event: a record replaces the one with its ID, and no record removes that
// ID and everything beneath it (all of them for the root).
func (p *Pane) ApplyStatus(id string, rec *rex.Record) {
	keep := p.Records[:0:0]
	for _, r := range p.Records {
		if r.ID == id || (rec == nil && (id == "" || strings.HasPrefix(r.ID, id+"/"))) {
			continue
		}
		keep = append(keep, r)
	}
	if rec != nil {
		r := *rec
		r.ID = id
		keep = append(keep, r)
	}
	slices.SortFunc(keep, func(a, b rex.Record) int { return cmp.Compare(a.ID, b.ID) })
	p.Records = keep
}

// --- ordering -----------------------------------------------------------------

// Urgency is the state that decides where an agent sorts and how loudly it
// shows. A done or error the user has seen is no more urgent than idle.
func (a Agent) Urgency() State {
	if a.Seen {
		return Idle
	}
	return a.State
}

// waitKey orders agents within a state: the one that has waited on the user
// longest comes first, and among busy ones the most recently active.
func less(a, b Agent) int {
	if c := cmp.Compare(a.Urgency().Rank(), b.Urgency().Rank()); c != 0 {
		return c
	}
	at, bt := since(a), since(b)
	if u := a.Urgency(); u == Working || u == Idle {
		at, bt = bt, at
	}
	if c := at.Compare(bt); c != 0 {
		return c
	}
	return cmp.Compare(a.BlockID, b.BlockID)
}

func since(a Agent) time.Time {
	if a.Since == nil {
		return time.Time{}
	}
	return *a.Since
}

// Sort orders agents by what needs the user first.
func Sort(agents []Agent) { slices.SortStableFunc(agents, less) }

// Group is one session's agents.
type Group struct {
	SessionID string  `json:"session_id"`
	Session   string  `json:"session"`
	Agents    []Agent `json:"agents"`
}

// Groups sorts agents and groups them by session. A session goes where its
// most urgent agent would.
func Groups(agents []Agent) []Group {
	agents = slices.Clone(agents)
	Sort(agents)
	var out []Group
	at := map[string]int{}
	for _, a := range agents {
		i, ok := at[a.SessionID]
		if !ok {
			i = len(out)
			at[a.SessionID] = i
			out = append(out, Group{SessionID: a.SessionID, Session: a.Session})
		}
		out[i].Agents = append(out[i].Agents, a)
	}
	return out
}

// Counts tallies agents by urgency, so a seen done counts as idle.
func Counts(agents []Agent) map[State]int {
	n := map[State]int{}
	for _, a := range agents {
		n[a.Urgency()]++
	}
	return n
}

// --- scanning -----------------------------------------------------------------

// parallel is how many rex calls run at once. Each is a short process.
const parallel = 16

// Scan reads every terminal pane in every session.
func Scan(ctx context.Context, c *rex.Client) ([]*Pane, error) {
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	sem := make(chan struct{}, parallel)
	run := func(f func()) func() {
		return func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			f()
		}
	}
	var (
		mu    sync.Mutex
		panes []*Pane
		wg    sync.WaitGroup
	)
	views := make([]rex.View, len(sessions))
	for i, s := range sessions {
		wg.Go(run(func() {
			v, err := c.View(ctx, s.ID)
			if err == nil {
				views[i] = v
			}
		}))
	}
	wg.Wait()
	for i, v := range views {
		if v.SessionID == "" {
			continue // the session went away while scanning
		}
		for _, w := range v.Windows {
			for _, l := range w.Layers {
				for _, b := range l.Blocks {
					if b.Creator != rex.Terminal {
						continue
					}
					p := &Pane{
						SessionID: v.SessionID, Session: sessions[i].Label,
						WindowID: w.ID, Window: w.Label, BlockID: b.ID, Label: b.Label,
						Focused: w.ID == v.ActiveWindowID && w.FocusedBlockID == b.ID,
					}
					panes = append(panes, p)
				}
			}
		}
	}
	for _, p := range panes {
		wg.Go(run(func() {
			recs, err := c.ProgramStatus(ctx, p.SessionID, p.BlockID)
			mu.Lock()
			if err == nil {
				p.Records = recs
			}
			mu.Unlock()
		}))
		wg.Go(run(func() {
			pr, err := c.Process(ctx, p.SessionID, p.BlockID)
			mu.Lock()
			if err == nil {
				p.Process = pr
			}
			mu.Unlock()
		}))
		wg.Go(run(func() {
			t, err := c.Viewed(ctx, p.SessionID, p.BlockID)
			mu.Lock()
			if err == nil {
				p.Viewed = t
			}
			mu.Unlock()
		}))
		wg.Go(run(func() {
			t, err := c.Title(ctx, p.SessionID, p.BlockID)
			mu.Lock()
			if err == nil {
				p.Title = t
			}
			mu.Unlock()
		}))
	}
	wg.Wait()
	return panes, ctx.Err()
}

// Agents returns the agents among panes in display order: grouped by
// session, sessions and agents ordered by what needs the user first. A
// position in this list is what `radar focus N` takes.
func Agents(panes []*Pane) []Agent {
	var all []Agent
	for _, p := range panes {
		if a, ok := p.Agent(); ok {
			all = append(all, a)
		}
	}
	out := []Agent{}
	for _, g := range Groups(all) {
		out = append(out, g.Agents...)
	}
	return out
}
