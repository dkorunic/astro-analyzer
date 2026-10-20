// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package report

import (
	"math"
	"testing"

	"github.com/dkorunic/astro-analyzer/internal/analyze"
	"github.com/dkorunic/astro-analyzer/internal/fits"
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

// TestChipOf checks chip matching by pixel pitch and size: binning,
// orientation, version and ambiguity.
func TestChipOf(t *testing.T) {
	for _, c := range []struct {
		m    fits.Meta
		want string
	}{
		{fits.Meta{Pixel: 3.76, Size: [2]int{6280, 4210}, CFA: true}, "IMX571"}, // QHY268C
		{fits.Meta{Pixel: 7.52, Size: [2]int{3124, 2088}}, "IMX571"},            // 2×2 binned mono
		{fits.Meta{Pixel: 3.76, Size: [2]int{4176, 6248}}, "IMX571"},            // portrait
		{fits.Meta{Pixel: 4.63, Size: [2]int{4144, 2822}, CFA: true}, "IMX294"}, // colour: not the IMX492
		{fits.Meta{Pixel: 4.63, Size: [2]int{4144, 2822}}, "IMX492"},            // mono
		{fits.Meta{Pixel: 2.9, Size: [2]int{1920, 1080}, CFA: true}, "IMX662"},  // not the IMX290's 1936 × 1096
		{fits.Meta{Pixel: 2.9, Size: [2]int{1936, 1096}, CFA: true}, ""},        // IMX290 or IMX462
		{fits.Meta{Pixel: 3.76, Size: [2]int{2000, 1500}, CFA: true}, ""},       // cropped
		{fits.Meta{Size: [2]int{6248, 4176}, CFA: true}, ""},                    // no XPIXSZ
	} {
		if got := chipOf(c.m); got != c.want {
			t.Errorf("chipOf(%v µm %v, CFA %v) = %q, want %q", c.m.Pixel, c.m.Size, c.m.CFA, got, c.want)
		}
	}
}
