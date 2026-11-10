// Command radar shows every coding agent across your Rex sessions: who is
// working, who is blocked on you, and who finished something you have not
// seen. `radar show` opens it in a pane beside you.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	agentradar "github.com/rynf1t/agent-radar"
	"github.com/rynf1t/agent-radar/internal/pane"
	"github.com/rynf1t/agent-radar/internal/radar"
	"github.com/rynf1t/agent-radar/internal/rex"
	"github.com/rynf1t/agent-radar/internal/view"
	"github.com/rynf1t/craftkit/cliout"
	"github.com/rynf1t/craftkit/skillkit"
	"github.com/rynf1t/craftkit/status"
)

const usage = `radar shows every coding agent across your Rex sessions.

An agent is a Rex pane whose program reports its status (OSC 7501) or runs a
known agent such as claude or codex. Its state is one of blocked, error,
done (finished and not yet seen), working or idle, and the list puts what
needs you first.

Usage:
  radar show [--split right|below] [--ratio PCT] [--theme auto|light|dark]
                        open the radar in a pane beside this one, without
                        taking focus. Safe to repeat: prints the open pane's ID.
  radar hide            close the pane that show opened
  radar ls [--json] [--state STATE]
                        list agents, most urgent first
  radar focus [TARGET]  focus an agent's pane in the Rex app. TARGET is a
                        position (1 is the most urgent), a session label or a
                        block ID suffix. Without one, the most urgent agent.
  radar view [--popup [--back BLOCK]]
                        run the viewer in this terminal (what show runs).
                        --popup closes on esc and after a jump, and focuses
                        BLOCK again unless it jumped within this session.
                        The Rex plugin's Cmd+Shift+U float runs this.
  radar watch [--states blocked,error,done] [--plist]
                        while you're in Rex, show a toast in the session you
                        are looking at when an agent elsewhere reaches one of
                        these states. Click the toast to jump there. --plist
                        prints a LaunchAgent that runs it at login.
  radar render -o FILE.png [--width PX] [--height PX] [--theme light|dark]
                        draw the viewer to a PNG, to see what the pane shows
  radar skill install [--dir DIR] [--dry-run] [--force]
  radar skill show | remove [--dir DIR] [--dry-run]
                        the agent skill that ships inside radar
  radar demo STATE [--kind KIND] [--msg TEXT] [--title TEXT]
  radar demo cycle      report a status from this terminal until interrupted,
                        to see a state on the radar without waiting for one.
                        cycle steps through every state every 3 seconds.
  radar version

Exit codes: 0 ok, 1 failure, 2 bad command line, 3 target not found or
ambiguous, 4 Rex unreachable, 130 interrupted.
`

var version = "0.3.0"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(cliout.OK)
	}
	os.Exit(cliout.Fail(os.Stderr, "radar", err))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	case "version", "--version":
		fmt.Fprintln(stdout, "radar", version)
		return nil
	case "ls", "list":
		return cmdLs(ctx, rest, stdout)
	case "focus":
		return cmdFocus(ctx, rest, stdout)
	case "show":
		return cmdShow(rest, stdout)
	case "hide":
		return pane.Hide(viewerKey())
	case "view":
		return cmdView(ctx, rest)
	case "watch":
		return cmdWatch(ctx, rest, stdout, stderr)
	case "render":
		return cmdRender(ctx, rest, stdout)
	case "skill":
		return cmdSkill(rest, stdout)
	case "demo":
		return cmdDemo(ctx, rest, stdout, stderr)
	}
	return cliout.Errorf(cliout.Usage, "unknown command %q. Run `radar help`", cmd)
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("radar "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(usage)
			return err
		}
		return cliout.Errorf(cliout.Usage, "%v. Run `radar help`", err)
	}
	return nil
}

func client() (*rex.Client, error) {
	c := rex.New()
	return c, c.Available()
}

func scan(ctx context.Context, c *rex.Client) ([]radar.Agent, error) {
	panes, err := radar.Scan(ctx, c)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, cliout.Errorf(cliout.Unreachable, "can't reach the Rex server (%v). Open Rex, or run `rex whoami` to check the connection", err)
	}
	return radar.Agents(panes), nil
}

func cmdLs(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("ls")
	asJSON := fs.Bool("json", false, "")
	state := fs.String("state", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *state != "" && radar.State(*state).Rank() == len(radar.States) {
		return cliout.Errorf(cliout.Usage, "unknown state %q. Use blocked, error, done, working or idle", *state)
	}
	c, err := client()
	if err != nil {
		return err
	}
	agents, err := scan(ctx, c)
	if err != nil {
		return err
	}
	if *state != "" {
		var keep []radar.Agent
		for _, a := range agents {
			if string(a.State) == *state {
				keep = append(keep, a)
			}
		}
		agents = keep
	}
	if *asJSON {
		if agents == nil {
			agents = []radar.Agent{}
		}
		return cliout.JSON(stdout, map[string]any{"agents": agents})
	}
	printList(stdout, agents)
	return nil
}

// printList is the plain text list: one line per agent, grouped by session.
func printList(w io.Writer, agents []radar.Agent) {
	if len(agents) == 0 {
		fmt.Fprintln(w, "No agents running in Rex.")
		return
	}
	now := time.Now()
	pos := map[string]int{}
	for i, a := range agents {
		pos[a.BlockID] = i + 1
	}
	for gi, g := range radar.Groups(agents) {
		if gi > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, g.Session)
		for _, a := range g.Agents {
			fmt.Fprintf(w, "  %2d  %-16s %-32s %-12s %4s  %s  %s\n", pos[a.BlockID], a.StateText(), clip(a.Title, 32),
				a.AppName(), radar.Ago(a.Since, now), radar.Short(a.BlockID), radar.ShortCWD(a.CWD))
			if a.Msg != "" {
				fmt.Fprintf(w, "      %16s %s\n", "", clip(a.Msg, 72))
			}
		}
	}
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func cmdFocus(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("focus")
	if err := parse(fs, args); err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	agents, err := scan(ctx, c)
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		return cliout.Errorf(cliout.NotFound, "no agents running in Rex")
	}
	a := agents[0]
	if fs.NArg() > 0 {
		if a, err = radar.Resolve(agents, strings.Join(fs.Args(), " ")); err != nil {
			return err
		}
	}
	if err := view.Focus(ctx, c, a); err != nil {
		return err
	}
	fmt.Fprintln(stdout, a.BlockID)
	return nil
}

func viewerKey() string {
	if b := os.Getenv("RADAR_PANE"); b != "" {
		return b
	}
	if b := os.Getenv("REX_BLOCK"); b != "" {
		return b
	}
	return "default"
}

func cmdShow(args []string, stdout io.Writer) error {
	fs := newFlags("show")
	split := fs.String("split", "right", "")
	ratio := fs.Int("ratio", 30, "")
	theme := fs.String("theme", "auto", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *split != "right" && *split != "below" && *split != "left" && *split != "above" {
		return cliout.Errorf(cliout.Usage, "--split must be right, below, left or above")
	}
	if *ratio < 10 || *ratio > 90 {
		return cliout.Errorf(cliout.Usage, "--ratio must be between 10 and 90")
	}
	if err := checkTheme(*theme); err != nil {
		return err
	}
	if !rex.InRex() {
		return cliout.Errorf(cliout.Unreachable, "not in a Rex terminal. Run `radar view` in a terminal with kitty graphics, or `radar ls`")
	}
	id, err := pane.Show(viewerKey(), pane.Options{Split: *split, Ratio: *ratio, Args: []string{"view", "--theme", *theme}})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, id)
	return nil
}

func checkTheme(t string) error {
	switch t {
	case "auto", "light", "dark":
		return nil
	}
	return cliout.Errorf(cliout.Usage, "--theme must be auto, light or dark")
}

func cmdView(ctx context.Context, args []string) error {
	fs := newFlags("view")
	theme := fs.String("theme", "auto", "")
	popup := fs.Bool("popup", false, "")
	toast := fs.String("toast", "", "")
	back := fs.String("back", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := checkTheme(*theme); err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	return view.Run(ctx, c, view.Options{Theme: *theme, Popup: *popup, Toast: *toast, Back: *back})
}

// watchLabel names the watcher's LaunchAgent.
const watchLabel = "io.github.rynf1t.radar.watch"

const watchPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>watch</string>%s
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>30</integer>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>%s</string>
  </dict>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`

func cmdWatch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("watch")
	states := fs.String("states", "blocked,error,done", "")
	plist := fs.Bool("plist", false, "")
	if err := parse(fs, args); err != nil {
		return err
	}
	var want []radar.State
	for _, s := range strings.Split(*states, ",") {
		st := radar.State(strings.TrimSpace(s))
		if st.Rank() == len(radar.States) || st == radar.Working || st == radar.Idle {
			return cliout.Errorf(cliout.Usage, "--states takes blocked, error and done, not %q", s)
		}
		want = append(want, st)
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if *plist {
		return printPlist(stdout, bin, *states)
	}
	release, err := pane.Single("watch")
	if err != nil {
		return cliout.Wrap(cliout.Failure, err)
	}
	defer release()
	// Rex may not be running yet, at login say. The watcher waits for it
	// rather than failing or starting a server.
	c := rex.New()
	c.NoStart = true
	fmt.Fprintf(stderr, "radar: watching for %s in sessions you aren't looking at. Press ctrl-c to stop.\n", strings.Join(strings.Split(*states, ","), ", "))
	return view.Watch(ctx, c, view.WatchOptions{Bin: bin, States: want, Log: stderr})
}

// printPlist writes a LaunchAgent that runs the watcher at login. Its PATH
// holds the rex this shell finds, since launchd starts with almost none.
func printPlist(w io.Writer, bin, states string) error {
	rexBin, err := exec.LookPath("rex")
	if err != nil {
		return cliout.Errorf(cliout.Unreachable, "rex is not on PATH, so the LaunchAgent couldn't find it either")
	}
	extra := ""
	if states != "blocked,error,done" {
		extra = "\n    <string>--states</string>\n    <string>" + html.EscapeString(states) + "</string>"
	}
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, "Library", "Logs", "radar-watch.log")
	path := filepath.Dir(rexBin) + ":/usr/bin:/bin:/usr/sbin:/sbin"
	_, err = fmt.Fprintf(w, watchPlist, watchLabel, html.EscapeString(bin), extra, html.EscapeString(path), logPath, logPath)
	return err
}

func cmdRender(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("render")
	out := fs.String("o", "", "")
	width := fs.Int("width", 640, "")
	height := fs.Int("height", 0, "")
	theme := fs.String("theme", "dark", "")
	scale := fs.Float64("scale", 2, "")
	toast := fs.String("toast", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *out == "" {
		return cliout.Errorf(cliout.Usage, "render needs -o FILE.png")
	}
	if *theme != "light" && *theme != "dark" {
		return cliout.Errorf(cliout.Usage, "--theme must be light or dark")
	}
	c, err := client()
	if err != nil {
		return err
	}
	agents, err := scan(ctx, c)
	if err != nil {
		return err
	}
	if *toast != "" {
		a, err := radar.Resolve(agents, *toast)
		if err != nil {
			return err
		}
		if err := view.RenderToastPNG(*out, a, *width, *scale, *theme); err != nil {
			return err
		}
		fmt.Fprintln(stdout, *out)
		return nil
	}
	if err := view.RenderPNG(*out, agents, *width, *height, *scale, *theme); err != nil {
		return err
	}
	fmt.Fprintln(stdout, *out)
	return nil
}

func cmdSkill(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return cliout.Errorf(cliout.Usage, "usage: radar skill install|show|remove [--dir DIR] [--dry-run]")
	}
	s := skillkit.Skill{Name: "radar", Tool: "radar", Content: agentradar.SkillMD}
	fs := newFlags("skill " + args[0])
	dir := fs.String("dir", skillkit.DefaultDir(), "")
	dry := fs.Bool("dry-run", false, "")
	force := fs.Bool("force", false, "")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "show":
		_, err := stdout.Write(s.Rendered())
		return err
	case "install":
		res, err := s.Install(*dir, *dry, *force)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, s.Describe(res, false))
		if res.State == skillkit.Foreign && !res.Wrote {
			return cliout.Errorf(cliout.Failure, "the skill was not installed")
		}
		return nil
	case "remove":
		res, err := s.Remove(*dir, *dry)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, s.Describe(res, true))
		return nil
	}
	return cliout.Errorf(cliout.Usage, "unknown skill command %q. Use install, show or remove", args[0])
}

func cmdDemo(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return cliout.Errorf(cliout.Usage, "usage: radar demo blocked|error|done|working|idle|cycle [--kind K] [--msg TEXT]")
	}
	fs := newFlags("demo")
	kind := fs.String("kind", "permission", "")
	msg := fs.String("msg", "", "")
	title := fs.String("title", "radar demo", "")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	report := func(st status.State, k status.Kind, m string) error {
		r := status.Report{State: st, App: "radar-demo", Msg: m, Title: *title}
		if st == status.Blocked {
			r.Kind = k
		}
		return r.Write(stdout)
	}
	if args[0] == "cycle" {
		steps := []struct {
			st   status.State
			kind status.Kind
			msg  string
		}{
			{status.Working, "", "Reading the brief"},
			{status.Blocked, status.Permission, "Run go test ./...?"},
			{status.Working, "", "Running the tests"},
			{status.Blocked, status.Question, "Commit to main?"},
			{status.Done, "", "Tests pass"},
			{status.Error, "", "The build failed"},
			{status.Idle, "", ""},
		}
		fmt.Fprintln(stderr, "radar: cycling demo states every 3 seconds. Press ctrl-c to stop.")
		for i := 0; ; i++ {
			s := steps[i%len(steps)]
			if err := report(s.st, s.kind, s.msg); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return report(status.Clear, "", "")
			case <-time.After(3 * time.Second):
			}
		}
	}
	st := status.State(args[0])
	if st == status.Clear || radar.State(st).Rank() == len(radar.States) {
		return cliout.Errorf(cliout.Usage, "unknown state %q. Use blocked, error, done, working, idle or cycle", args[0])
	}
	if err := report(st, status.Kind(*kind), *msg); err != nil {
		return cliout.Wrap(cliout.Usage, err)
	}
	fmt.Fprintf(stderr, "radar: reporting %s. Press ctrl-c to stop.\n", st)
	<-ctx.Done()
	if st == status.Working || st == status.Blocked {
		report(status.Idle, "", "")
	}
	return nil
}
