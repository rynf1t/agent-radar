package view

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/rynf1t/agent-radar/internal/radar"
	"github.com/rynf1t/agent-radar/internal/rex"
)

// live keeps the radar's picture of every pane current. It scans once, then
// follows the server's event stream: status reports update a pane in place,
// and layout changes trigger a rescan. A slow periodic rescan catches titles
// and working directories, which have no event of their own.
type live struct {
	c       *rex.Client
	changed chan struct{} // signalled after any change, never blocks

	mu        sync.Mutex
	panes     map[string]*radar.Pane
	scanned   bool
	connected bool
	scanReq   chan struct{}
}

const (
	rescanEvery    = 20 * time.Second
	rescanDebounce = 400 * time.Millisecond
)

func newLive(c *rex.Client) *live {
	return &live{c: c, changed: make(chan struct{}, 1), scanReq: make(chan struct{}, 1), panes: map[string]*radar.Pane{}}
}

func (l *live) notify() {
	select {
	case l.changed <- struct{}{}:
	default:
	}
}

// rescan asks for a scan soon. Requests close together share one scan.
func (l *live) rescan() {
	select {
	case l.scanReq <- struct{}{}:
	default:
	}
}

// snapshot returns the agents in display order and whether the radar can
// reach Rex.
func (l *live) snapshot() (agents []radar.Agent, scanned, connected bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	panes := make([]*radar.Pane, 0, len(l.panes))
	for _, p := range l.panes {
		panes = append(panes, p)
	}
	return radar.Agents(panes), l.scanned, l.connected
}

func (l *live) run(ctx context.Context) {
	go l.scanLoop(ctx)
	go l.eventLoop(ctx)
	l.rescan()
}

func (l *live) scanLoop(ctx context.Context) {
	tick := time.NewTicker(rescanEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-l.scanReq:
			// Let a burst of layout events settle into one scan.
			select {
			case <-time.After(rescanDebounce):
			case <-ctx.Done():
				return
			}
			select {
			case <-l.scanReq:
			default:
			}
		}
		panes, err := radar.Scan(ctx, l.c)
		if ctx.Err() != nil {
			return
		}
		l.mu.Lock()
		if err == nil {
			next := make(map[string]*radar.Pane, len(panes))
			for _, p := range panes {
				next[p.BlockID] = p
			}
			l.panes, l.scanned, l.connected = next, true, true
		} else {
			l.connected = false
		}
		l.mu.Unlock()
		l.notify()
	}
}

func (l *live) eventLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		events := make(chan rex.Event, 64)
		done := make(chan error, 1)
		sctx, cancel := context.WithCancel(ctx)
		go func() { done <- l.c.Events(sctx, events) }()
		started := time.Now()
	stream:
		for {
			select {
			case ev := <-events:
				l.handle(ctx, ev)
			case <-done:
				break stream
			case <-ctx.Done():
				cancel()
				return
			}
		}
		cancel()
		if time.Since(started) > 10*time.Second {
			backoff = time.Second
		}
		l.mu.Lock()
		l.connected = false
		l.mu.Unlock()
		l.notify()
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 5*time.Second)
		l.rescan() // catch up on what the gap missed
	}
}

// ignored are events that never change what the radar shows. Every rex call
// the radar makes connects a client, so reacting to these would loop.
var ignored = map[string]bool{
	"client_connected": true, "client_disconnected": true, "client_joined": true,
	"client_left": true, "client_changed": true,
}

func (l *live) handle(ctx context.Context, ev rex.Event) {
	if ignored[ev.Type] {
		return
	}
	if ev.Type != "block_event" {
		l.rescan() // windows, panes, labels and sessions came or went
		return
	}
	l.mu.Lock()
	p, known := l.panes[ev.BlockID]
	l.mu.Unlock()
	switch {
	case ev.Name == "program_status_changed":
		if !known {
			l.rescan() // a pane that just became an agent
			return
		}
		l.mu.Lock()
		p.ApplyStatus(ev.RecordID, ev.Record)
		l.mu.Unlock()
		l.notify()
	case known && strings.Contains(ev.Name, "title"):
		go l.refresh(ctx, p, "title")
	case known && (strings.Contains(ev.Name, "pwd") || strings.Contains(ev.Name, "process") || strings.Contains(ev.Name, "exit")):
		go l.refresh(ctx, p, "process")
	}
}

func (l *live) refresh(ctx context.Context, p *radar.Pane, what string) {
	switch what {
	case "title":
		t, err := l.c.Title(ctx, p.SessionID, p.BlockID)
		if err != nil {
			return
		}
		l.mu.Lock()
		p.Title = t
		l.mu.Unlock()
	case "process":
		pr, err := l.c.Process(ctx, p.SessionID, p.BlockID)
		if err != nil {
			return
		}
		l.mu.Lock()
		p.Process = pr
		l.mu.Unlock()
	}
	l.notify()
}
