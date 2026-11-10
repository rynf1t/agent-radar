package radar

import (
	"strings"
	"testing"
	"time"

	"github.com/rynf1t/agent-radar/internal/rex"
	"github.com/rynf1t/craftkit/cliout"
)

var t0 = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

func pane(session, block, state string, at time.Duration, procName string) *Pane {
	p := &Pane{SessionID: "session:" + session, Session: session, BlockID: "block:" + block, Label: "shell"}
	if state != "" {
		p.Records = []rex.Record{{State: state, EffectiveApp: "claude-code", UpdatedAt: t0.Add(at)}}
	}
	if procName != "" {
		p.Process.Foreground = &rex.Proc{Name: procName, CWD: "/tmp/" + block}
	}
	return p
}

func TestPaneAgent(t *testing.T) {
	if _, ok := pane("s", "shell1", "", 0, "zsh").Agent(); ok {
		t.Error("a plain shell is not an agent")
	}
	a, ok := pane("s", "codex1", "", 0, "codex").Agent()
	if !ok || a.App != "codex" || a.State != Idle || a.Title != "codex1" {
		t.Errorf("a known agent with no status: %+v %v", a, ok)
	}
	p := pane("s", "c1", "", 0, "claude.exe")
	p.Title = "✳ Fix the build"
	p.Records = []rex.Record{
		{ID: "", State: "working", EffectiveApp: "claude-code", Msg: "Reading", UpdatedAt: t0},
		{ID: "sub", State: "blocked", Kind: "permission", Msg: "Run make?", UpdatedAt: t0.Add(time.Second)},
	}
	a, _ = p.Agent()
	if a.State != Blocked || a.Kind != "permission" || a.Msg != "Run make?" {
		t.Errorf("the most urgent record should speak for the pane: %+v", a)
	}
	if a.Title != "Fix the build" {
		t.Errorf("title %q, want the spinner glyph dropped", a.Title)
	}
	p.Records = []rex.Record{{State: "sleeping", UpdatedAt: t0}}
	if a, _ := p.Agent(); a.State != Idle {
		t.Errorf("unknown state should read as idle, got %s", a.State)
	}
}

func TestApplyStatus(t *testing.T) {
	p := &Pane{}
	p.ApplyStatus("", &rex.Record{State: "working"})
	p.ApplyStatus("build", &rex.Record{State: "working"})
	p.ApplyStatus("build/test", &rex.Record{State: "blocked"})
	if len(p.Records) != 3 || p.Records[0].ID != "" || p.Records[2].ID != "build/test" {
		t.Fatalf("records %+v", p.Records)
	}
	p.ApplyStatus("", &rex.Record{State: "done"})
	if len(p.Records) != 3 || p.Records[0].State != "done" {
		t.Fatalf("a report should replace its record: %+v", p.Records)
	}
	p.ApplyStatus("build", nil)
	if len(p.Records) != 1 {
		t.Fatalf("clearing build should drop build/test too: %+v", p.Records)
	}
	p.ApplyStatus("", nil)
	if len(p.Records) != 0 {
		t.Fatalf("clearing the root should drop everything: %+v", p.Records)
	}
}

func TestOrder(t *testing.T) {
	panes := []*Pane{
		pane("quiet", "idle1", "idle", 5*time.Minute, "claude"),
		pane("busy", "work1", "working", 1*time.Minute, "claude"),
		pane("busy", "work2", "working", 3*time.Minute, "claude"),
		pane("busy", "done1", "done", 2*time.Minute, "claude"),
		pane("help", "block2", "blocked", 4*time.Minute, "claude"),
		pane("help", "block1", "blocked", 1*time.Minute, "claude"),
		pane("busy", "err1", "error", 6*time.Minute, "claude"),
	}
	var got []string
	for _, a := range Agents(panes) {
		got = append(got, a.BlockID[len("block:"):])
	}
	// Blocked first, longest waiting first. Then the session with the next
	// most urgent agent, its agents in urgency order, busy ones newest first.
	want := []string{"block1", "block2", "err1", "done1", "work2", "work1", "idle1"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	groups := Groups(Agents(panes))
	if len(groups) != 3 || groups[0].Session != "help" || groups[1].Session != "busy" {
		t.Errorf("groups %+v", groups)
	}
}

func TestResolve(t *testing.T) {
	agents := Agents([]*Pane{
		pane("web", "aaaa11", "blocked", 0, "claude"),
		pane("api", "bbbb22", "working", 0, "claude"),
		pane("api", "cccc33", "idle", 0, "claude"),
	})
	ok := map[string]string{
		"1": "block:aaaa11", "#2": "block:bbbb22", "web": "block:aaaa11",
		"cccc33": "block:cccc33", "block:bbbb22": "block:bbbb22", "c33": "",
	}
	for target, want := range ok {
		a, err := Resolve(agents, target)
		if want == "" {
			if cliout.Code(err) != cliout.NotFound {
				t.Errorf("%q: want not found, got %v", target, err)
			}
			continue
		}
		if err != nil || a.BlockID != want {
			t.Errorf("%q: got %s %v, want %s", target, a.BlockID, err, want)
		}
	}
	_, err := Resolve(agents, "api")
	if cliout.Code(err) != cliout.NotFound {
		t.Fatalf("an ambiguous label should exit 3: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "bbbb22") || !strings.Contains(msg, "cccc33") {
		t.Errorf("ambiguity should list the matches: %s", msg)
	}
	if _, err := Resolve(agents, "9"); cliout.Code(err) != cliout.NotFound {
		t.Errorf("position past the end: %v", err)
	}
}

func TestAgoAndMatches(t *testing.T) {
	now := t0.Add(time.Hour)
	cases := map[time.Duration]string{0: "now", 30 * time.Second: "30s", 5 * time.Minute: "5m", 3 * time.Hour: "3h", 72 * time.Hour: "3d"}
	for d, want := range cases {
		at := now.Add(-d)
		if got := Ago(&at, now); got != want {
			t.Errorf("Ago(%v) = %q, want %q", d, got, want)
		}
	}
	a, _ := pane("craft-web", "x1", "blocked", 0, "claude").Agent()
	a.Kind = "question"
	if !a.Matches("web QUEST") || a.Matches("api") {
		t.Error("Matches should match every word, ignoring case")
	}
}
