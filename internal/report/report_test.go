// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package report

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/dkorunic/astro-analyzer/internal/analyze"
	"github.com/dkorunic/astro-analyzer/internal/fits"
)

func TestSparkline(t *testing.T) {
	if s := sparkline([]float64{1, 2, 3, 4, 5, 6, 7, 8}); s != "▁▂▃▄▅▆▇█" {
		t.Errorf("sparkline = %q", s)
	}
	if s := []rune(sparkline(make([]float64, 500))); len(s) != sparkWidth || !strings.ContainsRune(string(s), '▄') {
		t.Errorf("sparkline bucketing = %q", string(s))
	}
	if s := sparkline([]float64{1, math.NaN(), 3}); s != "▁ █" {
		t.Errorf("sparkline with NaN = %q", s)
	}
}

func TestAxisGlyph(t *testing.T) {
	for deg, want := range map[float64]string{0: "─", 45: "╱", 90: "│", 135: "╲", 170: "─"} {
		a := 2 * deg * math.Pi / 180
		if g := AxisGlyph(0.7*math.Cos(a), 0.7*math.Sin(a)); g != want {
			t.Errorf("AxisGlyph(%g°) = %q, want %q", deg, g, want)
		}
	}
	if g := AxisGlyph(0.01, 0.01); g != "·" {
		t.Errorf("round stars glyph = %q, want ·", g)
	}
}

// TestFlags checks flags land on the right frames and reach CSV and JSON,
// with unknown values left empty rather than NaN.
func TestFlags(t *testing.T) {
	var frames []Frame
	for i := range 6 {
		r := analyze.Result{Stars: 4000, FWHM: 4, Ecc: 0.4, Resid: 0.03, SNR: 8, Bg: 1000, FWHMArc: math.NaN()}
		if i == 4 {
			r.FWHM = 7 // bad seeing
		}
		frames = append(frames, Frame{Path: "f.fits", Result: r})
	}
	FlagOutliers(frames, 3, 0.1)
	for i, f := range frames {
		if got, want := strings.Join(f.Flags, ","), map[bool]string{true: "FWHM px"}[i == 4]; got != want {
			t.Errorf("frame %d flags = %q, want %q", i, got, want)
		}
	}
	var csv, js bytes.Buffer
	if err := CSV(&csv, frames); err != nil {
		t.Fatal(err)
	}
	if err := JSON(&js, frames); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csv.String(), ",FWHM px\n") || strings.Contains(csv.String(), "NaN") {
		t.Errorf("CSV:\n%s", csv.String())
	}
	if !strings.Contains(js.String(), `"FWHM px"`) || strings.Contains(js.String(), "fwhm_arcsec") {
		t.Errorf("JSON:\n%s", js.String())
	}
}

// TestDrizzleUnknownFWHM: without a measured FWHM the sampling is unknown,
// not undersampled.
func TestDrizzleUnknownFWHM(t *testing.T) {
	var b bytes.Buffer
	Drizzle(&b, []Frame{{Path: "f.fits", Result: analyze.Result{FWHM: math.NaN()}}})
	if !strings.Contains(b.String(), "- px, unknown") {
		t.Errorf("drizzle report:\n%s", b.String())
	}
}

// TestFlagsByFilter: L and Ha frames differ tenfold in background. Pooled,
// the two populations hide the one L frame with a bright sky; per filter it
// stands out, and neither filter is flagged wholesale.
func TestFlagsByFilter(t *testing.T) {
	var frames []Frame
	for i := range 12 {
		r := analyze.Result{Stars: 4000, FWHM: 4, Ecc: 0.4, Resid: 0.03, SNR: 8, Bg: 1000 + float64(i%3), Filter: "L"}
		if i%2 == 1 {
			r.Bg, r.Filter = 100+float64(i%3), "Ha"
		}
		if i == 4 {
			r.Bg = 1300 // moonrise
		}
		frames = append(frames, Frame{Path: "f.fits", Result: r})
	}
	FlagOutliers(frames, 3, 0.1)
	for i, f := range frames {
		if got, want := strings.Join(f.Flags, ","), map[bool]string{true: "Background"}[i == 4]; got != want {
			t.Errorf("frame %d (%s) flags = %q, want %q", i, f.Result.Filter, got, want)
		}
	}
	var b bytes.Buffer
	Summary(&b, frames)
	Drizzle(&b, frames)
	for _, s := range []string{"Session (filter L): 6 frames, 1 flagged", "Session (filter Ha): 6 frames", "Drizzle (filter Ha):"} {
		if !strings.Contains(b.String(), s) {
			t.Errorf("report lacks %q:\n%s", s, b.String())
		}
	}
}

// TestFieldTilt: a tilted sensor makes one corner sharp and the opposite one
// soft. The corners' median against the centre hides it; the tilt figure
// does not. Each filter gets its own map, and a missing corner leaves tilt
// unknown rather than understated.
func TestFieldTilt(t *testing.T) {
	cells := func(f [9]float64) [9]analyze.Cell {
		var c [9]analyze.Cell
		for i, v := range f {
			c[i] = analyze.Cell{FWHM: v, Ecc: math.NaN(), Axis: [2]float64{math.NaN(), math.NaN()}}
		}

		return c
	}
	nan := math.NaN()
	frames := []Frame{
		{Path: "l.fits", Result: analyze.Result{Filter: "L", Field: cells([9]float64{4, 5, 6, 5, 6, 7, 6, 7, 9})}},
		{Path: "ha.fits", Result: analyze.Result{Filter: "Ha", Field: cells([9]float64{nan, 3, 3, 3, 3, 3, 3, 3, 3})}},
	}
	var b bytes.Buffer
	Field(&b, frames)
	for _, s := range []string{
		"Field (filter L):",
		"FWHM tilt 83% (corners 4.00–9.00 px), curvature +0% (corners vs centre)",
		"Field (filter Ha):",
		"FWHM tilt -, curvature +0%",
	} {
		if !strings.Contains(b.String(), s) {
			t.Errorf("field map lacks %q:\n%s", s, b.String())
		}
	}
}

// TestScoreAndBest: wFWHM penalises lost stars per filter (Siril's
// formula), -best keeps the best share per filter by it with unknowns
// last, and the summary names the best unflagged frame as reference.
func TestScoreAndBest(t *testing.T) {
	frames := []Frame{
		{Path: "a.fits", Result: analyze.Result{Stars: 1000, FWHM: 4}},
		{Path: "b.fits", Result: analyze.Result{Stars: 500, FWHM: 3}}, // sharp but hazy
		{Path: "c.fits", Result: analyze.Result{Stars: 1000, FWHM: 5}},
		{Path: "d.fits", Result: analyze.Result{Stars: 0, FWHM: math.NaN()}},
		{Path: "h.fits", Result: analyze.Result{Stars: 100, FWHM: 6, Meta: fits.Meta{Filter: "Ha"}}},
	}
	Score(frames)
	for i, want := range []float64{4, 3 * (1 + 2*500.0/1000), 5, math.NaN(), 6} {
		if got := frames[i].WFWHM; !(got == want || math.IsNaN(got) && math.IsNaN(want)) {
			t.Errorf("%s wFWHM = %v, want %v", frames[i].Path, got, want)
		}
	}
	FlagWorst(frames, 50) // keep 2 of 4 L frames: a (4), c (5); b (6) and d (unknown) go
	for i, want := range []bool{false, true, false, true, false} {
		if got := len(frames[i].Flags) > 0; got != want {
			t.Errorf("%s flagged %v, want %v", frames[i].Path, got, want)
		}
	}
	var b bytes.Buffer
	Summary(&b, frames)
	if !strings.Contains(b.String(), "Reference frame for registration: a.fits (best wFWHM 4.00 px, 1000 stars)") {
		t.Errorf("summary:\n%s", b.String())
	}
}
