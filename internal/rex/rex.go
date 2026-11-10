// Package rex talks to the Rex server through the documented rex CLI
// (`rex api call`, `rex events`). Going through the CLI keeps the radar on
// the public contract instead of the socket's wire format.
package rex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rynf1t/craftkit/cliout"
)

// Terminal is the creator name of terminal blocks.
const Terminal = "com.superlogical.terminal"

// Client runs rex commands.
type Client struct {
	Bin     string        // the rex executable, "rex" by default
	Server  string        // a host label for -S, empty for this machine
	Timeout time.Duration // per call
	// NoStart stops the rex CLI from starting a server when none is
	// running, for a watcher that must not open Rex by itself.
	NoStart bool
}

// New returns a client for the local server.
func New() *Client { return &Client{Bin: "rex", Timeout: 10 * time.Second} }

// Available reports whether the rex command exists and a server is
// configured, with an error that names the fix.
func (c *Client) Available() error {
	if _, err := exec.LookPath(c.Bin); err != nil {
		return cliout.Errorf(cliout.Unreachable, "rex is not installed or not on PATH; install Rex from https://www.superlogical.com/rex")
	}
	return nil
}

// InRex reports whether this process runs in a Rex terminal.
func InRex() bool { return os.Getenv("REX_SERVER") != "" }

func (c *Client) args(rest ...string) []string {
	var pre []string
	if c.Server != "" {
		pre = append(pre, "-S", c.Server)
	}
	if c.NoStart {
		pre = append(pre, "--autostart=false")
	}
	return append(pre, rest...)
}

// Run runs rex with args and returns stdout. A failure carries rex's stderr.
func (c *Client) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.Bin, c.args(args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, cliout.Errorf(cliout.Unreachable, "rex %s timed out", strings.Join(args, " "))
		}
		msg := strings.TrimSpace(stderr.String())
		msg = strings.TrimPrefix(msg, "Error: ")
		if msg == "" {
			msg = err.Error()
		}
		return nil, &CallError{Args: args, Msg: msg}
	}
	return out, nil
}

// CallError is a rex command that failed.
type CallError struct {
	Args []string
	Msg  string
}

func (e *CallError) Error() string { return "rex " + strings.Join(e.Args, " ") + ": " + e.Msg }

// NotFound reports whether a call failed because its target is gone.
func NotFound(err error) bool {
	var ce *CallError
	return errors.As(err, &ce) && (strings.Contains(ce.Msg, "not_found") || strings.Contains(ce.Msg, "not found") ||
		strings.Contains(ce.Msg, "no such") || strings.Contains(ce.Msg, "does not exist"))
}

// Call runs an API method with args and decodes the result into out.
func (c *Client) Call(ctx context.Context, method string, args, out any) error {
	in, err := json.Marshal(args)
	if err != nil {
		return err
	}
	raw, err := c.Run(ctx, in, "api", "call", method, "-")
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("rex api call %s: decoding: %w", method, err)
	}
	return nil
}

// BlockCall runs a terminal block method, such as "program_status".
func (c *Client) BlockCall(ctx context.Context, sessionID, blockID, method string, out any) error {
	return c.Call(ctx, Terminal+"."+method, map[string]any{
		"session_id": sessionID, "block_id": blockID, "args": map[string]any{},
	}, out)
}

// --- API types, only the fields the radar reads ------------------------------

type Session struct {
	ID    string `json:"session_id"`
	Label string `json:"label"`
}

type View struct {
	SessionID      string   `json:"session_id"`
	Label          string   `json:"label"`
	ActiveWindowID string   `json:"active_window_id"`
	Windows        []Window `json:"windows"`
}

type Window struct {
	ID             string  `json:"window_id"`
	Label          string  `json:"label"`
	FocusedBlockID string  `json:"focused_block_id"`
	Layers         []Layer `json:"layers"`
}

type Layer struct {
	Blocks []Block `json:"blocks"`
}

type Block struct {
	ID      string `json:"block_id"`
	Label   string `json:"label"`
	Creator string `json:"creator_name"`
}

type Record struct {
	ID           string    `json:"id,omitempty"`
	State        string    `json:"state"`
	App          string    `json:"app,omitempty"`
	EffectiveApp string    `json:"effective_app,omitempty"`
	Kind         string    `json:"kind,omitempty"`
	Msg          string    `json:"msg,omitempty"`
	Title        string    `json:"title,omitempty"`
	Progress     *int      `json:"progress,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Proc struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
	Argv string `json:"argv0"`
	CWD  string `json:"cwd"`
}

type Process struct {
	Foreground *Proc `json:"foreground"`
	Child      *Proc `json:"child"`
	LastExit   *struct {
		Code int `json:"exit_code"`
	} `json:"last_exit"`
}

func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var out struct {
		Sessions []Session `json:"sessions"`
	}
	err := c.Call(ctx, "session.list", map[string]any{}, &out)
	return out.Sessions, err
}

func (c *Client) View(ctx context.Context, sessionID string) (View, error) {
	var v View
	err := c.Call(ctx, "session.view", map[string]any{"session_id": sessionID}, &v)
	return v, err
}

func (c *Client) ProgramStatus(ctx context.Context, sessionID, blockID string) ([]Record, error) {
	var out struct {
		Records []Record `json:"records"`
	}
	err := c.BlockCall(ctx, sessionID, blockID, "program_status", &out)
	return out.Records, err
}

func (c *Client) Process(ctx context.Context, sessionID, blockID string) (Process, error) {
	var p Process
	err := c.BlockCall(ctx, sessionID, blockID, "process", &p)
	return p, err
}

func (c *Client) Title(ctx context.Context, sessionID, blockID string) (string, error) {
	var out struct {
		Title string `json:"title"`
	}
	err := c.BlockCall(ctx, sessionID, blockID, "title", &out)
	return out.Title, err
}

// Size is a terminal's grid and the sizes clients asked it for.
type Size struct {
	Columns    int `json:"columns"`
	Rows       int `json:"rows"`
	CellWidth  int `json:"cell_width_px"`
	CellHeight int `json:"cell_height_px"`
	Desired    []struct {
		UpdatedAt time.Time `json:"updated_at"`
	} `json:"desired"`
}

// Viewed is the last time an app showed the pane: the newest size a client
// asked for. It is zero when no client ever has.
func (s Size) Viewed() time.Time {
	var last time.Time
	for _, d := range s.Desired {
		if d.UpdatedAt.After(last) {
			last = d.UpdatedAt
		}
	}
	return last
}

func (c *Client) Size(ctx context.Context, sessionID, blockID string) (Size, error) {
	var out Size
	err := c.BlockCall(ctx, sessionID, blockID, "size", &out)
	return out, err
}

// NewLayer opens a terminal running command in a floating layer of a
// window, at bounds given as [x, y, w, h] fractions of the window, without
// taking focus. It returns the new block's ID.
func (c *Client) NewLayer(ctx context.Context, sessionID, windowID string, bounds [4]float64, label string, command []string) (string, error) {
	args := map[string]any{
		"session_id": sessionID, "window_id": windowID, "bounds": bounds, "focus": false,
		"layout": map[string]any{"block": map[string]any{
			"flavor": "com.superlogical.terminal.shell", "label": label,
			"options": map[string]any{"command": command, "shell": "none"},
		}},
	}
	var out struct {
		BlockIDs []string `json:"block_ids"`
	}
	if err := c.Call(ctx, "session.new_layer", args, &out); err != nil {
		return "", err
	}
	if len(out.BlockIDs) == 0 {
		return "", fmt.Errorf("session.new_layer made no block")
	}
	return out.BlockIDs[0], nil
}

// ShownSession is the session the Rex app on this Mac shows, which the app
// keeps in its preferences. It fails when the app has never shown one.
func ShownSession(ctx context.Context) (string, error) {
	prefs, err := exec.CommandContext(ctx, "defaults", "export", "com.superlogical.rex", "-").Output()
	if err != nil {
		return "", fmt.Errorf("reading the Rex app's preferences: %w", err)
	}
	cmd := exec.CommandContext(ctx, "plutil", "-extract", `Workspace\.lastFocusedSession`, "raw", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(prefs)
	raw, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("the Rex app hasn't recorded a session")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return "", err
	}
	var v struct {
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(data, &v); err != nil || v.SessionID == "" {
		return "", fmt.Errorf("the Rex app's last session is unreadable")
	}
	return v.SessionID, nil
}

// Focus focuses a block and makes its window active in its session.
func (c *Client) Focus(ctx context.Context, sessionID, blockID string) error {
	return c.Call(ctx, "session.focus_block", map[string]any{"session_id": sessionID, "block_id": blockID}, nil)
}

// ShowSession asks the Rex app to show a session. It needs exactly one
// connected app, or REX_CLIENT, and Remote Control turned on in the app.
func (c *Client) ShowSession(ctx context.Context, sessionID, windowID string) error {
	args := []string{"do", "session.select", "session_id=" + sessionID}
	if windowID != "" {
		args = append(args, "window_id="+windowID)
	}
	_, err := c.Run(ctx, nil, args...)
	return err
}

// --- events -----------------------------------------------------------------

// Event is one server event, reduced to what the radar reacts to.
type Event struct {
	Type      string // the inner event type, such as "block_event" or "session_created"
	SessionID string
	BlockID   string
	Name      string // for block events, such as "program_status_changed"
	RecordID  string
	Record    *Record // for program_status_changed with a record
	Reason    string
}

type rawEvent struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// ParseEvent decodes one line of `rex events --all --json`.
func ParseEvent(line []byte) (Event, bool) {
	var outer rawEvent
	if json.Unmarshal(line, &outer) != nil {
		return Event{}, false
	}
	ev := Event{Type: outer.Type}
	if outer.Type != "session_event" {
		var p struct {
			SessionID string `json:"session_id"`
		}
		json.Unmarshal(outer.Payload, &p)
		ev.SessionID = p.SessionID
		return ev, true
	}
	var se struct {
		SessionID string   `json:"session_id"`
		Event     rawEvent `json:"event"`
	}
	if json.Unmarshal(outer.Payload, &se) != nil {
		return Event{}, false
	}
	ev.SessionID, ev.Type = se.SessionID, se.Event.Type
	if se.Event.Type == "block_event" {
		var be struct {
			BlockID string `json:"block_id"`
			Name    string `json:"name"`
			Payload struct {
				ID     string  `json:"id"`
				Record *Record `json:"record"`
				Reason string  `json:"reason"`
			} `json:"payload"`
		}
		if json.Unmarshal(se.Event.Payload, &be) == nil {
			ev.BlockID, ev.Name = be.BlockID, be.Name
			ev.RecordID, ev.Record, ev.Reason = be.Payload.ID, be.Payload.Record, be.Payload.Reason
		}
	}
	return ev, true
}

// Events streams server events until ctx ends or the stream breaks. It
// returns the error that ended the stream.
func (c *Client) Events(ctx context.Context, out chan<- Event) error {
	cmd := exec.CommandContext(ctx, c.Bin, c.args("events", "--all", "--json")...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		if ev, ok := ParseEvent(sc.Bytes()); ok {
			select {
			case out <- ev:
			case <-ctx.Done():
			}
		}
	}
	io.Copy(io.Discard, stdout)
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return errors.New(msg)
	}
	if err == nil {
		err = io.EOF
	}
	return err
}
