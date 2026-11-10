package view

import (
	"testing"
	"time"

	"github.com/rynf1t/agent-radar/internal/radar"
	"github.com/rynf1t/agent-radar/internal/rex"
)

func agentAt(block string, st radar.State, at time.Time, seen bool) radar.Agent {
	return radar.Agent{BlockID: block, State: st, Since: &at, Seen: seen}
}

func TestFresh(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	want := []radar.State{radar.Blocked, radar.Error, radar.Done}

	news, prev := fresh(nil, []radar.Agent{agentAt("a", radar.Blocked, t0, false)}, want)
	if len(news) != 0 {
		t.Fatalf("the first snapshot is old news, got %v", news)
	}
	news, prev = fresh(prev, []radar.Agent{agentAt("a", radar.Blocked, t0, false)}, want)
	if len(news) != 0 {
		t.Errorf("nothing changed, got %v", news)
	}
	steps := []struct {
		a    radar.Agent
		news bool
		why  string
	}{
		{agentAt("a", radar.Working, t0.Add(time.Second), false), false, "working earns no toast"},
		{agentAt("a", radar.Blocked, t0.Add(2*time.Second), false), true, "blocked again"},
		{agentAt("a", radar.Blocked, t0.Add(3*time.Second), false), true, "a new question while blocked"},
		{agentAt("a", radar.Done, t0.Add(4*time.Second), false), true, "done"},
		{agentAt("a", radar.Done, t0.Add(4*time.Second), true), false, "the user looked"},
	}
	for _, s := range steps {
		news, prev = fresh(prev, []radar.Agent{s.a}, want)
		if (len(news) == 1) != s.news {
			t.Errorf("%s: news %v", s.why, news)
		}
	}
	news, _ = fresh(prev, []radar.Agent{agentAt("a", radar.Idle, t0, false), agentAt("b", radar.Error, t0, false)}, want)
	if len(news) != 1 || news[0].BlockID != "b" {
		t.Errorf("a new agent that has failed is news, got %v", news)
	}
}

func TestToastBounds(t *testing.T) {
	size := rex.Size{Columns: 200, Rows: 60, CellWidth: 14, CellHeight: 28}
	first, ok := toastBounds(size, 0)
	if !ok || first[0]+first[2] > 1 || first[1] <= 0 || first[3] <= 0 {
		t.Fatalf("first toast %v %v", first, ok)
	}
	second, _ := toastBounds(size, 1)
	if second[1] < first[1]+first[3] {
		t.Errorf("the second toast %v overlaps the first %v", second, first)
	}
	if _, ok := toastBounds(size, 20); ok {
		t.Error("toasts past the bottom of the window should be skipped")
	}
}
