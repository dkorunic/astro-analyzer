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
// one at 2σ per pixel broken by a gap (one line), a bright second one that
// does not cross it, and a third crossing both, which the Hough transform
// splits from the blob they join into.
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
	line(pix, 1000, 600, 300, 2, 0)
	if n := count(pix); n != 3 {
		t.Errorf("a third trail crossing both: %v, want 3", n)
	}
}

// TestHough splits an X of two 2-px-wide lines, 100 px long, into two
// lines, and finds none in a filled disc of the same size: its chords
// through the centre are not lines.
func TestHough(t *testing.T) {
	const bw = 200
	var x, disc []int
	for y := range bw {
		for xx := range bw {
			dx, dy := float64(xx-100), float64(y-100)
			if math.Abs(dx) < 50 && math.Abs(dy) < 50 && (math.Abs(dx-dy) < 1.5 || math.Abs(dx+0.5*dy) < 1.5) {
				x = append(x, y*bw+xx)
			}
			if dx*dx+dy*dy < 50*50 {
				disc = append(disc, y*bw+xx)
			}
		}
	}
	if _, ok := segment(x, bw, 20); ok {
		t.Fatal("the X passes as one thin segment: the test does not reach hough")
	}
	if got := hough(x, bw, 20); len(got) != 2 {
		t.Errorf("X: %d lines, want 2", len(got))
	}
	if got := hough(disc, bw, 20); len(got) != 0 {
		t.Errorf("disc: %d lines, want 0", len(got))
	}
}
