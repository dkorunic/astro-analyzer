// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package stats

import (
	"math"
	"testing"

	"github.com/dkorunic/astro-analyzer/internal/synth"
)

// tilted returns a star field whose sky rises by rx across the frame along
// +x and ry along +y, plus a bright nebula of the given radius at (cx, cy).
func tilted(rx, ry, cx, cy, radius float64) []float32 {
	pix := synth.StarField(4, 4)
	out := make([]float32, len(pix))
	for i, v := range pix {
		x, y := float64(i%synth.W), float64(i/synth.W)
		v += rx*(x/synth.W-0.5) + ry*(y/synth.H-0.5)
		if math.Hypot(x-cx, y-cy) < radius {
			v += 400
		}
		out[i] = float32(v)
	}

	return out
}

// TestGradient: a sky rising 200 ADU toward +x and 100 toward −y is
// recovered with its direction; stars and a nebula over a tenth of the
// frame do not move it, and a frame the nebula fills is left unknown.
func TestGradient(t *testing.T) {
	// Direction of steepest rise per pixel: (200/W, −100/H).
	want := math.Atan2(-100.0/synth.H, 200.0/synth.W)*180/math.Pi + 360
	for _, radius := range []float64{0, 120} {
		g, a := Gradient(tilted(200, -100, 450, 280, radius), synth.W, synth.H)
		if !(math.Abs(g-300) < 15) || !(math.Abs(a-want) < 3) {
			t.Errorf("nebula radius %.0f: gradient %.1f toward %.1f°, want 300 toward %.1f°", radius, g, a, want)
		}
	}
	if g, a := Gradient(tilted(200, -100, 300, 200, 400), synth.W, synth.H); !math.IsNaN(g) && !(math.Abs(g-300) < 15) {
		t.Errorf("nebula filling the frame: gradient %.1f toward %.1f°, want unknown or right", g, a)
	}
	if g, _ := Gradient(tilted(0, 0, 0, 0, 0), synth.W, synth.H); !(g < 5) {
		t.Errorf("flat sky gradient %.1f, want ~0", g)
	}
}
