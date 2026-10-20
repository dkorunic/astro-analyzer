// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package report

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/dkorunic/astro-analyzer/internal/analyze"
	"github.com/dkorunic/astro-analyzer/internal/fits"
)

// TestOverviewNights: frames before and after local midnight belong to one
// observing night; a night two days later is another. Local solar time
// comes from the longitude, so UTC timestamps group correctly too.
func TestOverviewNights(t *testing.T) {
	var frames []Frame
	for _, s := range []string{"2026-10-13T18:30:00Z", "2026-10-13T23:30:00Z", "2026-10-16T19:00:00Z"} {
		ts, _ := time.Parse(time.RFC3339, s)
		r := analyze.Result{
			Time: ts, Exp: 300, Lon: 16, Lat: 45.8, Object: "M1", Telescope: "RASA6F2",
			Aperture: 152.4, FocalLen: 335, Plate: 1.23, Size: [2]int{3856, 2180},
			FWHM: 4, Bg: 1000, MoonAlt: -5, SunAlt: -30, MoonIllum: 0.5, MoonSep: 90,

			SQM: math.NaN(),
		}
		frames = append(frames, Frame{Path: "f.fits", Result: r})
	}
	frames[2].Flags = []string{"FWHM px"}
	var b bytes.Buffer
	Overview(&b, frames)
	if strings.Contains(b.String(), "SQM") {
		t.Errorf("SQM column without an SQM header:\n%s", b.String())
	}
	for i := range frames {
		frames[i].Result.SQM = 20.5 + float64(i)
	}
	b.Reset()
	Overview(&b, frames)
	for _, s := range []string{
		"M1 · RASA6F2 152 mm f/2.2 · 1.23\"/px, field 79′ × 45′",
		"3 frames × 300 s = 0 h 15 min, 0 h 10 min unflagged, over 2 nights",
		"2026-10-13  2       18:30–23:30",
		"2026-10-16  1       19:00–19:00",
		"50%       0%       90°",
		"Bortle  SQM header", "0.00     9       22.50",
	} {
		if !strings.Contains(b.String(), s) {
			t.Errorf("overview lacks %q:\n%s", s, b.String())
		}
	}
}

// TestCoverage: a 2:1 frame turned 90° about its centre overlaps the
// reference only in the central square, half of it.
func TestCoverage(t *testing.T) {
	s := 1.0 / 3600
	ref := fits.WCS{CD: [2][2]float64{{-s, 0}, {0, s}}, CRVAL: [2]float64{10, 20}, CRPIX: [2]float64{100.5, 50.5}, NAXIS: [2]int{200, 100}, OK: true}
	turned := ref
	turned.CD = [2][2]float64{{0, s}, {s, 0}} // same sensor, axes turned on the sky
	rot, full, most, half := coverage([]fits.WCS{ref, turned})
	if !(math.Abs(rot-90) < 1e-9) || !(math.Abs(full-0.5) < 0.02) || full != most || half != 1 {
		t.Errorf("rotation %.1f, coverage full %.3f, 90%% %.3f, half %.3f; want 90, 0.5, 0.5, 1", rot, full, most, half)
	}
	if _, full, _, _ := coverage([]fits.WCS{ref, ref}); full != 1 {
		t.Errorf("identical frames cover %.3f, want 1", full)
	}
}

// TestEquipmentCFA: the Bayer pattern is shown as written, plus how it
// reads from the first stored row when the data says it starts at the top.
func TestEquipmentCFA(t *testing.T) {
	if s := equipment(fits.Meta{Bayer: "GBRG"}); s != "CFA GBRG" {
		t.Errorf("equipment = %q", s)
	}
	if s := equipment(fits.Meta{Bayer: "GBRG", BayerFlip: true}); !strings.Contains(s, "(RGGB from the first stored row)") {
		t.Errorf("flipped equipment = %q", s)
	}
}
