package qdriver

import (
	"math"
	"math/rand/v2"
	"time"
)

type pointerStep struct {
	x, y  float64
	delay time.Duration
}

// This is a geometric approximation inspired by cursor generators, not a
// recorded human trace. The endpoint remains the exact inspected hit target.
func planPointer(timing InputTiming, fromX, fromY, x, y, targetW, targetH, viewportW, viewportH float64) []pointerStep {
	steps := max(1, timing.MouseSteps)
	duration := timing.MouseDuration
	distance := math.Hypot(x-fromX, y-fromY)
	c1x, c1y, c2x, c2y := fromX, fromY, x, y
	if timing.MouseCurve && distance > 1 {
		width := math.Max(4, math.Min(targetW, targetH))
		factor := math.Max(0.6, math.Min(3.5, 0.5+math.Log2(1+distance/width)/3))
		duration = time.Duration(float64(duration) * factor)
		steps = min(120, max(steps, int(math.Ceil(distance/25))))
		bend := math.Min(100, distance*0.16) * (0.5 + rand.Float64()*0.5)
		if rand.IntN(2) == 0 {
			bend = -bend
		}
		nx, ny := -(y-fromY)/distance, (x-fromX)/distance
		c1x, c1y = fromX+(x-fromX)*0.25+nx*bend, fromY+(y-fromY)*0.25+ny*bend
		c2x, c2y = fromX+(x-fromX)*0.75+nx*bend*0.7, fromY+(y-fromY)*0.75+ny*bend*0.7
		if viewportW > 0 {
			c1x = math.Max(0, math.Min(viewportW-1, c1x))
			c2x = math.Max(0, math.Min(viewportW-1, c2x))
		}
		if viewportH > 0 {
			c1y = math.Max(0, math.Min(viewportH-1, c1y))
			c2y = math.Max(0, math.Min(viewportH-1, c2y))
		}
	}
	duration = jitterDelay(duration, timing.Jitter)
	result := make([]pointerStep, 0, steps)
	for i := 1; i <= steps; i++ {
		px, py := pointerPoint(fromX, fromY, x, y, i, steps)
		if timing.MouseCurve && distance > 1 {
			t := float64(i) / float64(steps)
			t = t * t * (3 - 2*t)
			u := 1 - t
			px = u*u*u*fromX + 3*u*u*t*c1x + 3*u*t*t*c2x + t*t*t*x
			py = u*u*u*fromY + 3*u*u*t*c1y + 3*u*t*t*c2y + t*t*t*y
		}
		if i == steps {
			px, py = x, y
		}
		result = append(result, pointerStep{px, py, duration / time.Duration(max(1, steps-1))})
	}
	return result
}
