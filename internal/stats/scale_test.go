// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package stats

import (
	"math"
	"math/rand/v2"
	"testing"
)

// TestChauvenet: gross outliers on either side go, the Gaussian body stays.
func TestChauvenet(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	v := make([]float64, 0, 1006)
	for range 1000 {
		v = append(v, 100+10*rng.NormFloat64())
	}
	v = append(v, 400, 500, 600, -300, -200, 900)
	i, j := Chauvenet(v)
	if v[i] < 0 || v[j-1] > 200 || j-i < 990 {
		t.Errorf("kept [%d, %d) = %.1f..%.1f, want the 1000 Gaussian values", i, j, v[i], v[j-1])
	}
}

// TestMedfit: the least absolute deviation line ignores one wild point,
// which would tilt a least squares fit.
func TestMedfit(t *testing.T) {
	x := []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	y := make([]float64, len(x))
	for k := range x {
		y[k] = 2 + 3*x[k]
	}
	y[9] = 500
	// PCL stops bisecting at 1% of the slope's least squares error, which
	// the wild point inflates, so the slope is close, not exact.
	a, b := medfit(x, y)
	if math.Abs(a-2) > 0.5 || math.Abs(b-3) > 0.1 {
		t.Errorf("medfit = %.4f + %.4f·x, want 2 + 3·x (least squares slope 29)", a, b)
	}
}

// noiseFrame is Gaussian noise (sigma 10) on a sky with a gradient.
func noiseFrame(pedestal float64) ([]float32, int, int) {
	const w, h = 1024, 768
	rng := rand.New(rand.NewPCG(3, 4))
	pix := make([]float32, w*h)
	for i := range pix {
		pix[i] = float32(pedestal + 0.2*float64(i%w) + 10*rng.NormFloat64())
	}

	return pix, w, h
}

// TestMStar: on Gaussian noise M* is the half-normal median 0.6745σ, the
// gradient is followed and a pedestal changes nothing.
func TestMStar(t *testing.T) {
	pix, w, h := noiseFrame(1000)
	got := MStar(pix, w, h, 128)
	if math.Abs(got-6.745)/6.745 > 0.02 {
		t.Errorf("M* = %.3f, want 6.745", got)
	}
	pix, w, h = noiseFrame(5000)
	if p := MStar(pix, w, h, 128); math.Abs(p-got) > 1e-3 {
		t.Errorf("M* with a pedestal = %.4f, want %.4f", p, got)
	}
}

// TestTwoSidedScale against the truncated normal: the standard deviation
// of the half below the mean, clipped twice at 4σ of the previous pass,
// is 0.5309σ.
func TestTwoSidedScale(t *testing.T) {
	const w, h = 1024, 768
	rng := rand.New(rand.NewPCG(5, 6))
	pix := make([]float32, w*h)
	for i := range pix {
		pix[i] = float32(1000 + 10*rng.NormFloat64())
	}
	if got := TwoSidedScale(pix, 2, 65533); math.Abs(got-5.309)/5.309 > 0.01 {
		t.Errorf("two-sided scale = %.3f, want 5.309", got)
	}
}
