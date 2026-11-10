---
name: radar
description: See every coding agent across the user's Rex sessions and which ones need them. Use when the user asks "which agents need me", "what are my other agents doing", "show the radar", "is anything blocked", "agent status", or wants to jump to an agent that is waiting. Drive it with the `radar` CLI.
---

# radar: every agent at a glance

`radar` finds the coding agents running in Rex panes and orders them by what
needs the user first. An agent is a pane whose program reports its status
with OSC 7501 (Claude Code does), or one running a known agent such as
`claude` or `codex`.

States, most urgent first:

| State | Meaning |
|---|---|
| `blocked` | Waiting on the user. `kind` is `permission`, `question` or `auth`, and `msg` says what. |
| `error` | Failed and stopped. |
| `done` | Finished. `seen` is true once the user has looked at the pane since. |
| `working` | Busy on its own. |
| `idle` | Waiting for the next instruction. |

## Show the radar

```bash
radar show
```

This opens the radar in a pane beside yours without taking focus. Calling it
again does nothing and prints the same pane ID, so it is safe on every turn.
`radar hide` closes it. Leave it open unless the user asks you to close it.

The user may also have Cmd+Shift+U, which floats the radar over any session
and closes it after a jump. That key is theirs. Don't open the float for them.

In the pane, the user moves with the arrow keys or `j` and `k`, presses enter
to jump to that agent, `/` to filter and `q` to quit.

## Check on other agents

```bash
radar ls --json                 # every agent, in display order
radar ls --state blocked --json # only the ones waiting on the user
```

Each agent has `block_id`, `session`, `state`, `kind`, `title`, `msg`, `app`,
`cwd`, `since` and `seen`. Use the JSON, never the text table, in scripts.

## Jump to an agent

```bash
radar focus            # the most urgent agent
radar focus 2          # position in `radar ls`
radar focus web        # a session label
radar focus 9f0f3a     # a block ID suffix
```

This switches what the user is looking at. Run it only when the user asks to
go somewhere. An ambiguous target changes nothing, lists the matches and
exits 3. Switching sessions needs Remote Control turned on in Rex Settings,
under Rex Server. Without it the pane is focused in its session, but the app
stays where it is, and `radar focus` says so and exits 1.

## Rules

- Report what you find in the user's words: "craft-web-style is waiting on a
  question about committing to main", not the raw JSON.
- A `done` or `error` with `seen: false` is news to the user. With `seen: true`
  they have already looked, so don't bring it up as new.
- Exit codes: 0 ok, 1 failure, 2 bad command line, 3 target not found or
  ambiguous, 4 Rex unreachable.
- Outside Rex, `radar show` fails with a message. `radar ls` still needs a
  reachable Rex server.
- To see what the pane shows, run `radar render -o /tmp/radar.png` and read
  the image.
- To test a state without waiting for a real one, `radar demo blocked --kind
  question --msg "Ship it?"` reports it from the current terminal until
  interrupted, and `radar demo cycle` steps through every state.
