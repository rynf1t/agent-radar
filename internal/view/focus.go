package view

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rynf1t/agent-radar/internal/radar"
	"github.com/rynf1t/agent-radar/internal/rex"
	"github.com/rynf1t/craftkit/cliout"
)

// Focus focuses the agent's pane in its session, then asks the Rex app to
// show that session. The second step needs Remote Control in the app.
func Focus(ctx context.Context, c *rex.Client, a radar.Agent) error {
	if err := c.Focus(ctx, a.SessionID, a.BlockID); err != nil {
		if rex.NotFound(err) {
			return cliout.Errorf(cliout.NotFound, "the pane %s is gone", radar.Short(a.BlockID))
		}
		return err
	}
	if err := c.ShowSession(ctx, a.SessionID, a.WindowID); err != nil {
		msg := err.Error()
		var ce *rex.CallError
		if errors.As(err, &ce) {
			msg = ce.Msg
		}
		if strings.Contains(msg, "Remote Control") || strings.Contains(msg, "not accepting actions") {
			return fmt.Errorf("Rex didn't switch to %s. Turn on Remote Control in Rex Settings, under Rex Server", a.Session)
		}
		return fmt.Errorf("Rex didn't switch to %s: %s", a.Session, strings.TrimSuffix(msg, "."))
	}
	return nil
}
