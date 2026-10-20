// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package report

import (
	"math"
	"testing"

	"github.com/dkorunic/astro-analyzer/internal/analyze"
)

// TestSkyMag checks the photon arithmetic on an Origin Mark II frame: 2
// e⁻/s of sky per raw pixel is 2 / (1005·10·300 · π/4·(15.2² − 7.7²) cm² ·
// 0.81 · 0.83 · (0.565 + 0.394)/2 · 1.23² arcsec²) of a V = 0 source per
// arcsec², 19.99 mag. GBRG samples green and blue in the first stored row;
// read from the top row it is red and green.
func TestSkyMag(t *testing.T) {
	var r analyze.Result
	r.Camera, r.Setting, r.CFA, r.Bayer = "Origin678C-x", 1000, true, "GBRG"
	r.Aperture, r.Plate, r.Exp, r.Temp = 152, 1.23, 15, math.NaN()
	r.NoiseE = math.Sqrt(2*15 + 0.97*0.97)
	if m := skyMag(r, 0); !(math.Abs(m-19.991) < 0.001) {
		t.Errorf("skyMag = %v, want 19.991", m)
	}
	r.BayerFlip = true
	want := 19.991 - 2.5*math.Log10((0.565+0.394)/(0.397+0.565))
	if m := skyMag(r, 0); !(math.Abs(m-want) < 0.001) {
		t.Errorf("flipped skyMag = %v, want %v", m, want)
	}
	// 20 °C adds 0.16·2⁻¹ = 0.08 e⁻/s of dark current, which is not sky.
	r.BayerFlip, r.Temp, r.NoiseE = false, 20, math.Sqrt(2.08*15+0.97*0.97)
	if m := skyMag(r, 0); !(math.Abs(m-19.991) < 0.001) {
		t.Errorf("skyMag with dark current = %v, want 19.991", m)
	}
	r.Camera = "Unknown"
	if m := skyMag(r, 1); !math.IsNaN(m) {
		t.Errorf("unknown camera: skyMag = %v, want NaN", m)
	}
}

// TestBortle checks the zenith correction (at 30°, X = 2: 2.5·log10(2) −
// 0.2 = 0.553 mag) and the class bands.
func TestBortle(t *testing.T) {
	if z := zenith(20, 30); !(math.Abs(z-20.553) < 0.001) {
		t.Errorf("zenith(20, 30) = %v", z)
	}
	if z := zenith(20, math.NaN()); z != 20 {
		t.Errorf("zenith without altitude = %v", z)
	}
	for mag, want := range map[float64]string{22: "1", 21.96: "1", 20.553: "5", 20.29: "6", 17: "9", math.NaN(): "-"} {
		if got := bortle(mag); got != want {
			t.Errorf("bortle(%v) = %s, want %s", mag, got, want)
		}
	}
}

// TestPresetChips checks that every camera preset names a known chip.
func TestPresetChips(t *testing.T) {
	for _, p := range readNoisePresets {
		if _, ok := chips[p.chip]; !ok {
			t.Errorf("%s: chip %q has no entry in chips", p.camera, p.chip)
		}
	}
}
