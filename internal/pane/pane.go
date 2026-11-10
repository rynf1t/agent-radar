// Package pane opens the radar viewer in a Rex split beside the caller, once.
// The viewer's block ID is stored under a key (the caller's REX_BLOCK), so
// `show` is idempotent and `hide` closes the right pane.
package pane

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/rynf1t/craftkit/cliout"
)

// StateDir is where the radar keeps its state.
func StateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "agent-radar")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "agent-radar")
}

func idPath(key string) string {
	safe := strings.NewReplacer("/", "_", ":", "_").Replace(key)
	return filepath.Join(StateDir(), "viewers", safe+".id")
}

// lock serializes show and hide for one key, so two quick calls can't open
// two panes.
func lock(key string) (func(), error) {
	p := idPath(key) + ".lock"
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// Open returns the stored viewer's block ID and whether that block still
// exists.
func Open(key string) (string, bool) {
	data, err := os.ReadFile(idPath(key))
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", false
	}
	return id, exec.Command("rex", "block", "inspect", id).Run() == nil
}

// Options places the viewer.
type Options struct {
	Split string   // right, below, left or above
	Ratio int      // the viewer's share in percent
	Args  []string // arguments for the radar binary in the new pane
}

// Show opens the viewer unless the one stored under key is still open, and
// returns its block ID either way.
func Show(key string, opt Options) (string, error) {
	unlock, err := lock(key)
	if err != nil {
		return "", err
	}
	defer unlock()
	if id, ok := Open(key); ok {
		return id, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	args := []string{"split", "--focus=false", "--split", opt.Split, "--ratio", strconv.Itoa(opt.Ratio),
		"--shell=none", "--label", "Radar", "--json"}
	anchor := os.Getenv("REX_BLOCK")
	if anchor != "" {
		args = append(args, "-b", anchor)
	}
	args = append(args, "--")
	args = append(args, self)
	args = append(args, opt.Args...)
	cmd := exec.Command("rex", args...)
	cmd.Env = append(os.Environ(), "RADAR_PANE="+key)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", cliout.Errorf(cliout.Unreachable, "rex split failed: %s", strings.TrimSpace(stderr.String()))
	}
	var res struct {
		BlockIDs []string `json:"block_ids"`
	}
	if err := json.Unmarshal(out, &res); err != nil || len(res.BlockIDs) == 0 {
		return "", fmt.Errorf("rex split returned no block ID: %s", strings.TrimSpace(string(out)))
	}
	id := res.BlockIDs[0]
	// Re-place the pane at the ratio in case the split landed at an even split.
	if anchor != "" {
		exec.Command("rex", "move", id, opt.Split, anchor, "--ratio", strconv.Itoa(opt.Ratio)).Run()
	}
	if err := os.WriteFile(idPath(key), []byte(id+"\n"), 0o644); err != nil {
		return id, err
	}
	return id, nil
}

// Hide closes the viewer stored under key, if it is open.
func Hide(key string) error {
	unlock, err := lock(key)
	if err != nil {
		return err
	}
	defer unlock()
	id, ok := Open(key)
	os.Remove(idPath(key))
	if !ok {
		return nil
	}
	if out, err := exec.Command("rex", "block", "close", id).CombinedOutput(); err != nil {
		return fmt.Errorf("rex block close %s: %s", id, strings.TrimSpace(string(out)))
	}
	return nil
}

// Single takes the lock called name for as long as this process runs, so
// only one of something runs at a time. It fails when another process
// holds it.
func Single(name string) (release func(), err error) {
	p := filepath.Join(StateDir(), name+".lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another radar %s is already running", name)
	}
	return func() { f.Close() }, nil
}
