// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package analyze

import (
	"math"
	"testing"

	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/psf"
	"github.com/dkorunic/astro-analyzer/internal/synth"
)

// TestTrails counts trails in a crowded synthetic field: none, one faint
// one at 2σ per pixel broken by a gap (one line), and a bright second one
// that does not cross it (crossing trails join into one blob: see trails).
func TestTrails(t *testing.T) {
	const w, h = 1200, 800
	// line adds a trail of Gaussian profile (σ 1.5 px), amp per pixel at
	// its core, through (x0, y0) at angle a, skipping |s| < gap along it.
	line := func(pix []float64, amp, x0, y0, a, gap float64) {
		sin, cos := math.Sincos(a)
		for y := range h {
			for x := range w {
				dx, dy := float64(x)-x0, float64(y)-y0
				if s := dx*cos + dy*sin; math.Abs(s) < gap {
					continue
				}
				d := -dx*sin + dy*cos
				pix[y*w+x] += amp * math.Exp(-d*d/(2*1.5*1.5))
			}
		}
	}
	count := func(pix []float64) float64 {
		img := fits.Image{W: w, H: h, Pix: make([]float32, len(pix)), Scale: 1, Range: 65535}
		for i, v := range pix {
			img.Pix[i] = float32(v)
		}

		return Frame(img, 5, psf.Moffat4).Trails
	}
	pix := synth.Crowded(w, h, 3000, 4, 0.8)
	if n := count(pix); n != 0 {
		t.Errorf("star field: %v trails, want 0", n)
	}
	line(pix, 500, 600, 400, 0.3, 40) // twice the 250 ADU noise
	if n := count(pix); n != 1 {
		t.Errorf("one broken trail: %v, want 1", n)
	}
	line(pix, 1000, 600, 100, 0.5, 0) // meets the first at x = 1865, off the frame
	if n := count(pix); n != 2 {
		t.Errorf("two trails: %v, want 2", n)
	}
}
