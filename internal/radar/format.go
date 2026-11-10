package radar

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Ago is a short age such as "now", "45s", "12m", "3h" or "2d".
func Ago(t *time.Time, now time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	d := now.Sub(*t)
	switch {
	case d < 10*time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// StateText is how a state reads in a list: "Needs permission",
// "Has a question", "Done", and so on.
func (a Agent) StateText() string {
	switch a.State {
	case Blocked:
		switch a.Kind {
		case "permission":
			return "Needs permission"
		case "question":
			return "Has a question"
		case "auth":
			return "Needs a login"
		}
		return "Waiting on you"
	case Error:
		return "Failed"
	case Done:
		return "Done"
	case Working:
		return "Working"
	}
	return "Idle"
}

// AppName is the agent's program as people say it.
func (a Agent) AppName() string {
	switch a.App {
	case "claude-code":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "gemini":
		return "Gemini"
	case "aider":
		return "Aider"
	case "opencode":
		return "OpenCode"
	}
	return a.App
}

// ShortCWD shortens the home directory to ~.
func ShortCWD(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+"/") {
			return "~" + p[len(home):]
		}
	}
	return p
}

// Matches reports whether the agent matches a filter: every word of q
// appears, ignoring case, in its session, title, message, app, state or
// directory.
func (a Agent) Matches(q string) bool {
	hay := strings.ToLower(strings.Join([]string{
		a.Session, a.Title, a.Msg, a.App, a.AppName(), string(a.State), a.StateText(), a.Kind, ShortCWD(a.CWD), a.Label,
	}, "\x00"))
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}
