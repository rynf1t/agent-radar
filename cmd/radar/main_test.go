package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rynf1t/craftkit/cliout"
)

func runArgs(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	err := run(context.Background(), args, &out, &errb)
	return out.String(), cliout.Code(err)
}

func TestUsageErrors(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cases := [][]string{
		{"nope"},
		{"ls", "--state", "sleeping"},
		{"ls", "--bogus"},
		{"render"},
		{"show", "--split", "diagonal"},
		{"show", "--ratio", "5"},
		{"view", "--theme", "neon"},
		{"demo"},
		{"demo", "sleeping"},
		{"skill"},
		{"skill", "frobnicate"},
	}
	for _, args := range cases {
		if _, code := runArgs(t, args...); code != cliout.Usage {
			t.Errorf("radar %s: exit %d, want %d", strings.Join(args, " "), code, cliout.Usage)
		}
	}
}

func TestHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{}, {"help"}, {"--help"}} {
		out, code := runArgs(t, args...)
		if code != cliout.OK || !strings.Contains(out, "radar show") {
			t.Errorf("radar %v: exit %d", args, code)
		}
	}
	if out, _ := runArgs(t, "version"); !strings.HasPrefix(out, "radar ") {
		t.Errorf("version: %q", out)
	}
}

// show outside Rex fails with a message that names the alternatives.
func TestShowOutsideRex(t *testing.T) {
	t.Setenv("REX_SERVER", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out, errb bytes.Buffer
	err := run(context.Background(), []string{"show"}, &out, &errb)
	if cliout.Code(err) != cliout.Unreachable || !strings.Contains(err.Error(), "radar ls") {
		t.Errorf("got %v", err)
	}
}

func TestSkillInstall(t *testing.T) {
	dir := t.TempDir()
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"skill", "install", "--dir", dir, "--dry-run"}, "would create"},
		{[]string{"skill", "install", "--dir", dir}, "created"},
		{[]string{"skill", "install", "--dir", dir}, "unchanged"},
		{[]string{"skill", "remove", "--dir", dir}, "removed"},
	}
	for _, s := range steps {
		out, code := runArgs(t, s.args...)
		if code != cliout.OK || !strings.HasPrefix(out, s.want) {
			t.Fatalf("radar %s: exit %d, %q", strings.Join(s.args, " "), code, out)
		}
	}
	path := filepath.Join(dir, "radar", "SKILL.md")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("mine\n"), 0o644)
	out, code := runArgs(t, "skill", "install", "--dir", dir)
	if code != cliout.Failure || !strings.HasPrefix(out, "foreign") {
		t.Errorf("foreign: exit %d, %q", code, out)
	}
	if b, _ := os.ReadFile(path); string(b) != "mine\n" {
		t.Error("install replaced a foreign file")
	}
	out, _ = runArgs(t, "skill", "show")
	if !strings.HasPrefix(out, "---\nname: radar") || !strings.Contains(out, "Installed by \"radar skill install\"") {
		t.Errorf("show: %q", out[:min(80, len(out))])
	}
}

func TestWatchPlist(t *testing.T) {
	out, code := runArgs(t, "watch", "--plist", "--states", "blocked")
	if code != cliout.OK {
		t.Skipf("exit %d (rex not on PATH?)", code)
	}
	if err := xml.Unmarshal([]byte(out), new(struct{})); err != nil {
		t.Errorf("not XML: %v", err)
	}
	for _, want := range []string{watchLabel, "<string>watch</string>", "<string>--states</string>", "<string>blocked</string>"} {
		if !strings.Contains(out, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	if _, code := runArgs(t, "watch", "--states", "working"); code != cliout.Usage {
		t.Errorf("--states working: exit %d, want %d", code, cliout.Usage)
	}
}
