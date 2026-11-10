package view

import (
	"math"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Durations for every movement the view makes. Expo ease-out throughout.
const (
	durSelect  = 150 * time.Millisecond // the selection gliding to a row
	durMove    = 150 * time.Millisecond // rows sliding to a new order
	durFadeIn  = 150 * time.Millisecond
	durFadeOut = 200 * time.Millisecond
	durScroll  = 150 * time.Millisecond
	frameEvery = 16 * time.Millisecond // while something moves. Each frame is a whole picture.
)

// tween eases a value from one target to the next.
type tween struct {
	from, to float64
	start    time.Time
	dur      time.Duration
}

func still(v float64) *tween { return &tween{from: v, to: v} }

// expoOut is 1 - 2^(-10t), scaled to land exactly on 1.
func expoOut(t float64) float64 {
	if t >= 1 {
		return 1
	}
	return (1 - math.Pow(2, -10*t)) / (1 - math.Pow(2, -10))
}

func (t *tween) at(now time.Time) float64 {
	if t.dur <= 0 || !now.Before(t.start.Add(t.dur)) {
		return t.to
	}
	p := float64(now.Sub(t.start)) / float64(t.dur)
	return t.from + (t.to-t.from)*expoOut(p)
}

func (t *tween) moving(now time.Time) bool {
	return t.dur > 0 && now.Before(t.start.Add(t.dur)) && t.from != t.to
}

// toward starts easing from wherever the value is now to v.
func (t *tween) toward(v float64, now time.Time, dur time.Duration) {
	if v == t.to {
		return
	}
	t.from, t.to, t.start, t.dur = t.at(now), v, now, dur
	if reduceMotion {
		t.dur = 0
	}
}

// reduceMotion is set from the system setting, or RADAR_MOTION=off.
var reduceMotion = detectReduceMotion()

func detectReduceMotion() bool {
	switch strings.ToLower(os.Getenv("RADAR_MOTION")) {
	case "off", "0", "false", "reduce":
		return true
	case "on", "1", "true":
		return false
	}
	out, err := exec.Command("defaults", "read", "com.apple.universalaccess", "reduceMotion").Output()
	return err == nil && strings.TrimSpace(string(out)) == "1"
}
