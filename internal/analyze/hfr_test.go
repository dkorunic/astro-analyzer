// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package analyze

import (
	"math"
	"slices"
	"testing"

	"github.com/dkorunic/astro-analyzer/internal/synth"
)

// TestHalfFlux works CalculateHalfFluxRadius by hand: fluxes 4, 3, 3 at
// radii 0, 1, 2 total 10, so half (5) falls between the pixels at 0 (4)
// and 1 (7), a third of the way: 1/3.
func TestHalfFlux(t *testing.T) {
	s := []hfrSample{{d: 2, f: 3}, {d: 1, f: 3}, {d: 0, f: 4}}
	if got := halfFlux(s, 10); math.Abs(got-1.0/3) > 1e-12 {
		t.Errorf("halfFlux = %v, want 1/3", got)
	}
}

// TestMeanHFR checks the radius cut: radii 3, 3, 3, 9 have mean 4.5 and
// population σ 2.6, so 9 lies 1.73σ out and its HFR (100) is dropped.
func TestMeanHFR(t *testing.T) {
	if got := meanHFR([]float64{1, 2, math.NaN(), 100}, []float64{3, 3, 3, 9}); got != 1.5 {
		t.Errorf("meanHFR = %v, want 1.5", got)
	}
}

// TestKept checks N.I.N.A.'s star test: the brightest synthetic star is
// measured against a noise of 1 ADU, but not when the noise is so high
// that its pixels no longer stand 1.5σ over the sky.
func TestKept(t *testing.T) {
	img := synth.Image(synth.StarField(4, 4))
	c := 0
	for i, v := range img.Pix {
		if v > img.Pix[c] {
			c = i
		}
	}
	sky := float64(slices.Min(img.Pix))
	var h hfrBuf
	if r := h.ninaHFR(img, c, 6, sky, 1); math.IsNaN(r) {
		t.Error("bright star rejected")
	}
	if r := h.ninaHFR(img, c, 6, sky, 1e6); !math.IsNaN(r) {
		t.Errorf("star under the noise measured: %v", r)
	}
}
