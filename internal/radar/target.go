package radar

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rynf1t/craftkit/cliout"
)

// Resolve finds one agent by, in order: full block ID, position in the
// sorted list (1 is the most urgent), session or pane label, and block ID
// suffix. An ambiguous target matches nothing and says what it matched,
// with exit code NotFound.
func Resolve(agents []Agent, target string) (Agent, error) {
	t := strings.TrimPrefix(strings.TrimSpace(target), "#")
	if t == "" {
		return Agent{}, cliout.Errorf(cliout.Usage, "empty target")
	}
	for _, a := range agents {
		if a.BlockID == t || a.BlockID == "block:"+t {
			return a, nil
		}
	}
	if n, err := strconv.Atoi(t); err == nil {
		if n >= 1 && n <= len(agents) {
			return agents[n-1], nil
		}
		return Agent{}, cliout.Errorf(cliout.NotFound, "no agent at position %d. There are %d", n, len(agents))
	}
	pick := func(how string, match func(Agent) bool) (Agent, bool, error) {
		var hits []Agent
		for _, a := range agents {
			if match(a) {
				hits = append(hits, a)
			}
		}
		switch len(hits) {
		case 0:
			return Agent{}, false, nil
		case 1:
			return hits[0], true, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%q matches %d agents by %s. Use a block ID suffix:", target, len(hits), how)
		for _, a := range hits {
			fmt.Fprintf(&b, "\n  %s  %s  %s", Short(a.BlockID), a.Session, a.Title)
		}
		return Agent{}, true, cliout.Errorf(cliout.NotFound, "%s", b.String())
	}
	lt := strings.ToLower(t)
	for _, step := range []struct {
		how   string
		match func(Agent) bool
	}{
		{"label", func(a Agent) bool {
			return strings.ToLower(a.Session) == lt || (a.Label != "shell" && strings.ToLower(a.Label) == lt)
		}},
		{"ID suffix", func(a Agent) bool { return len(t) >= 4 && strings.HasSuffix(a.BlockID, lt) }},
	} {
		if a, ok, err := pick(step.how, step.match); ok {
			return a, err
		}
	}
	return Agent{}, cliout.Errorf(cliout.NotFound, "no agent matches %q. Run `radar ls` to see them", target)
}

// Short is the last six characters of an ID, which Resolve accepts.
func Short(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[len(id)-6:]
}
