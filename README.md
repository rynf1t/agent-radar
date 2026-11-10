# radar

Every coding agent across your Rex sessions in one pane: who's working, who's
blocked on you, and who finished something you haven't seen. Enter jumps
there.

**Project Status:** early and in daily use by one person. Works with Rex Beta
0.1 on macOS. The CLI and JSON shape may still change.

## Why?

I run a lot of Claude Code and Codex agents at once, each in its own Rex
session. The expensive moment is an agent sitting on a permission prompt for
twenty minutes because I was looking somewhere else. Rex already knows each
agent's state, because the agents report it with the
[program status protocol](https://www.superlogical.com/rex/docs/build/program-status).
radar puts all of it in one list, ordered by what needs me first.

## Install

```sh
go install github.com/rynf1t/agent-radar/cmd/radar@latest
radar skill install   # teach Claude Code to use it
```

This needs Go 1.26 or later. radar builds on
[craftkit](https://github.com/rynf1t/craftkit). To work on both at once, add
`replace github.com/rynf1t/craftkit => ../craftkit` to `go.mod` and keep it
out of commits.

## Quick start

Inside a Rex terminal:

```sh
radar show
```

The radar opens in a pane beside you without taking focus. Run it again and
it prints the same pane ID instead of opening another.

| Key | Does |
|---|---|
| `↑` `↓` or `j` `k` | Move |
| `enter` | Jump to that agent's pane |
| `/` | Filter by session, title, message, state or folder. `esc` clears it. |
| `g` `G` | First, last |
| `q` | Quit |

Clicking a row jumps there too.

## What it shows

Agents are grouped by session. Sessions and agents are ordered by urgency,
then by how long they've waited.

| State | Mark | Meaning |
|---|---|---|
| blocked | yellow dot | Waiting on you: a permission, a question or a login |
| error | red dot | Failed and stopped |
| done | blue dot | Finished, and you haven't looked yet |
| working | ring | Busy. The ring fills if the agent reports progress. |
| idle | small grey dot | Waiting for its next instruction |

Each row shows the title, what the agent says it's doing or asking, the app,
the folder and how long ago the state changed. Colours come from your
terminal theme, and the view follows a theme switch within a second. Nothing
moves unless something changed: rows glide to a new order in 150ms, and
macOS Reduce Motion turns that off.

An agent is any Rex pane whose program reports a status, or that runs a known
agent (`claude`, `codex`, `aider`, `gemini`, `opencode` and a few more).

## Commands

```
radar show [--split right|below] [--ratio PCT] [--theme auto|light|dark]
radar hide
radar ls [--json] [--state STATE]
radar focus [TARGET]
radar view
radar render -o FILE.png [--width PX] [--height PX] [--theme light|dark]
radar demo STATE [--kind KIND] [--msg TEXT] | radar demo cycle
radar skill install|show|remove [--dir DIR] [--dry-run] [--force]
```

`radar focus` takes a position from `radar ls` (1 is the most urgent), a
session label or a block ID suffix. Without one it goes to the most urgent
agent. An ambiguous target changes nothing and lists what it matched.

Exit codes: 0 ok, 1 failure, 2 bad command line, 3 target not found or
ambiguous, 4 Rex unreachable, 130 interrupted.

| Variable | Does |
|---|---|
| `RADAR_PANE` | Keys the pane `show` and `hide` manage. Defaults to `REX_BLOCK`, so each agent pane gets its own radar. Set the same value in several panes to share one. |
| `RADAR_MOTION=off` | Turns off motion. Reduce Motion in macOS does the same. |
| `RADAR_SCALE` | Device pixels per point, when the cell size guess is wrong. |
| `RADAR_FONT` | A `.ttf` or `.otf` to draw with instead of the system font. |

## Limits

- **Jumping needs Remote Control.** radar focuses the pane in its session,
  then asks the Rex app to show that session. The second step needs Remote
  Control on in Rex Settings, under Rex Server. Without it the pane says so.
- **done means unseen until the agent says otherwise.** Rex keeps a done
  record until the program replaces it, so an agent you've already read stays
  done until you give it new work.
- **One machine.** radar reads the local Rex server. Agents on other hosts
  don't appear yet.
- **Titles and folders refresh every 20 seconds.** Status changes arrive
  straight away from the event stream.

## Recipes

Check from a script whether anything is waiting on you:

```sh
radar ls --state blocked --json | jq -r '.agents[] | "\(.session): \(.msg)"'
```

Jump to whatever needs you, from a key binding:

```sh
radar focus
```

See a state without waiting for an agent to reach it:

```sh
radar demo blocked --kind question --msg "Ship it?"
```

## Read this, agents

- `cmd/radar` is the CLI. Every command is a function in `main.go`.
- `internal/rex` runs the `rex` CLI (`rex api call`, `rex events`). radar
  never speaks the socket protocol directly.
- `internal/radar` turns panes into agents, orders them and resolves targets.
  Display order is the order `radar focus N` uses.
- `internal/view` is the pane: `live.go` keeps state from one scan plus the
  event stream, `render.go` draws a frame, `view.go` runs the loop,
  `motion.go` holds every duration.
- `internal/pane` makes `radar show` idempotent. The pane's block ID is
  stored under `$XDG_STATE_HOME/agent-radar`, keyed on `REX_BLOCK`, behind a
  file lock.
- The agent skill is `skills/radar/SKILL.md`, embedded in the binary.
- Test with `go test ./...`. `radar render -o out.png` shows what the pane
  draws.
