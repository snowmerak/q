package qdriver

import (
	"math"
	"testing"
	"time"
)

func TestCurveBoundsAndTargetDependentTiming(t *testing.T) {
	timing := InputTiming{MouseSteps: 12, MouseDuration: 180 * time.Millisecond, MouseCurve: true}
	narrow := planPointer(timing, 20, 20, 600, 250, 10, 10, 800, 600)
	wide := planPointer(timing, 20, 20, 600, 250, 200, 200, 800, 600)
	if narrow[len(narrow)-1].x != 600 || narrow[len(narrow)-1].y != 250 {
		t.Fatal("curve missed exact target")
	}
	var deviation float64
	for _, point := range narrow {
		if point.x < 0 || point.x >= 800 || point.y < 0 || point.y >= 600 {
			t.Fatalf("curve escaped viewport: %+v", point)
		}
		deviation = math.Max(deviation, math.Abs((point.x-20)*230-(point.y-20)*580))
	}
	if deviation < 1 {
		t.Fatal("curved path remained a straight line")
	}
	if narrow[1].delay*time.Duration(len(narrow)-1) <= wide[1].delay*time.Duration(len(wide)-1) {
		t.Fatal("narrow target did not affect movement duration")
	}
	fast := planPointer(InputTiming{}, 20, 20, 600, 250, 10, 10, 800, 600)
	if len(fast) != 1 || fast[0].delay != 0 {
		t.Fatalf("fast mode added trajectory/timing: %+v", fast)
	}
}

func TestQWERTYTransitionsAndOverlapEligibility(t *testing.T) {
	key := func(r rune) keyStroke {
		k, ok := characterKey(r)
		if !ok {
			t.Fatalf("missing key %q", r)
		}
		return k
	}
	base := 65 * time.Millisecond
	if transitionInterval(key('f'), key('g'), base) <= transitionInterval(key('f'), key('j'), base) {
		t.Fatal("same-finger transition should cost more than hand alternation")
	}
	if transitionInterval(key('f'), key('t'), base) <= transitionInterval(key('f'), key('g'), base) {
		t.Fatal("row/distance cost was not applied")
	}
	if transitionInterval(key('a'), key('B'), base) <= transitionInterval(key('a'), key('b'), base) {
		t.Fatal("Shift transition cost was not applied")
	}
	if !overlapEligible(key('f'), key('j')) || overlapEligible(key('f'), key('f')) || overlapEligible(key('f'), key('J')) {
		t.Fatal("unsafe overlap eligibility")
	}
	for r := rune(32); r <= 126; r++ {
		if _, ok := qwertyLocation(key(r).code); !ok {
			t.Fatalf("missing physical geometry for %q", r)
		}
	}
	if _, err := inputTiming(&InputTiming{KeyOverlap: math.NaN()}); err == nil {
		t.Fatal("NaN overlap accepted")
	}
}
