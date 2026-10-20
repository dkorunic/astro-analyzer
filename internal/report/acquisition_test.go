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

// focusSession is four 8-frame runs at focuser positions that fall
// 20 steps per °C as the tube cools 1 °C per run, and 0.1 °C per frame
// within one. Each run starts at 15 − run − 0.1 °C (the median of its
// first 3 frames), so position = 5002 + 20·T.
func focusSession() []Frame {
	var frames []Frame
	for i := range 32 {
		run, k := i/8, i%8
		r := analyze.Result{FWHM: 3 * (1 + 0.01*float64(k)), HFR: 1.5}
		r.Exp, r.Temp = 60, 20
		r.FocusTemp = 15 - float64(run) - 0.1*float64(k)
		r.Focus = 5000 + 20*(15-float64(run))
		frames = append(frames, Frame{Result: r})
	}

	return frames
}

func TestFocusSlope(t *testing.T) {
	frames := focusSession()
	if s := focusSlope(frames); !strings.Contains(s, "+20.0 steps/°C over 4 positions and 3.0 °C") || !strings.Contains(s, "Slope 20.0, Intercept 5002") {
		t.Errorf("focusSlope: %q", s)
	}
	for i := range frames { // positions unrelated to temperature
		frames[i].Result.Focus = float64(5000 + 7*((i/8)%2))
	}
	if s := focusSlope(frames); !strings.Contains(s, "does not follow") {
		t.Errorf("focusSlope without a trend: %q", s)
	}
	for i := range frames {
		frames[i].Result.FocusTemp = math.NaN()
	}
	if s := focusSlope(frames); s != "" {
		t.Errorf("focusSlope without FOCTEMP: %q", s)
	}
}

// TestTempTrigger: between the medians of each run's first and last 3
// frames FWHM grows 1.01 → 1.06 (4.95%) while the tube cools 0.5 °C,
// 9.9%/°C, so 5% takes 0.5 °C.
func TestTempTrigger(t *testing.T) {
	if s := tempTrigger(focusSession()); !strings.Contains(s, "grows 9.9%/°C") || !strings.Contains(s, "at 0.5 °C keeps") {
		t.Errorf("tempTrigger: %q", s)
	}
}

// TestHFRFires: a flat HFR never fires; one rising 1% a frame passes 5%
// over the lowest once the trend line reads 1.05, after 6 frames.
func TestHFRFires(t *testing.T) {
	flat := []float64{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2}
	if n := hfrFires(flat); n != -1 {
		t.Errorf("flat HFR fires after %d", n)
	}
	rising := make([]float64, 12)
	for i := range rising {
		rising[i] = 1 + 0.01*float64(i)
	}
	if n := hfrFires(rising); n != 7 {
		t.Errorf("rising HFR fires after %d, want 7", n)
	}
	rising[0] = math.NaN() // skipped, as N.I.N.A. skips frames without stars
	if n := hfrFires(rising); n != 8 {
		t.Errorf("with a NaN first frame fires after %d, want 8", n)
	}
	frames := focusSession()
	for i := range frames {
		frames[i].Result.HFR = 1 + 0.01*float64(i%8)
	}
	if s := hfrTrigger(frames); !strings.Contains(s, "would refocus 4 of 4 runs, after a median of 7 frames (7 min)") {
		t.Errorf("hfrTrigger: %q", s)
	}
}

func TestCooler(t *testing.T) {
	var frames []Frame
	for i := range 10 {
		r := analyze.Result{}
		r.SetTemp, r.Temp = -10, -10.2
		if i >= 7 {
			r.Temp = -10 + float64(i-6) // +1 .. +3 °C off
		}
		frames = append(frames, Frame{Result: r})
	}
	if s := cooler(frames); !strings.Contains(s, "setpoint -10 °C by over 1 °C on 2 of 10 frames (worst +3.0 °C)") {
		t.Errorf("cooler: %q", s)
	}
	frames = frames[:7]
	if s := cooler(frames); !strings.Contains(s, "held its setpoint -10 °C within 1 °C on all 7") {
		t.Errorf("cooler held: %q", s)
	}
}

// TestDewPoint checks Magnus against WMO's tables: 20 °C at 50% has a dew
// point of 9.3 °C.
func TestDewPoint(t *testing.T) {
	m := fits.Meta{Ambient: 20, Humidity: 50, DewPoint: math.NaN()}
	if d := dewPoint(m); math.Abs(d-9.26) > 0.05 {
		t.Errorf("dew point %.2f, want 9.26", d)
	}
	var frames []Frame
	for i := range 6 {
		r := analyze.Result{}
		r.Ambient, r.DewPoint, r.Humidity = 5, 5-float64(i), math.NaN()
		f := Frame{Result: r}
		if i == 0 {
			f.Flags = []string{"FWHM px"}
		}
		frames = append(frames, f)
	}
	if s := dew(frames); !strings.Contains(s, "within 2 °C of its dew point on 2 of 6 frames (closest 0.0 °C, 1 of them flagged)") {
		t.Errorf("dew: %q", s)
	}
}

func TestCloud(t *testing.T) {
	var frames []Frame
	for i := range 10 {
		r := analyze.Result{Stars: 1000 - 50*i}
		r.Cloud, r.Ambient, r.SkyTemp = math.NaN(), 10, -25+2*float64(i)
		frames = append(frames, Frame{Result: r})
	}
	if s := cloud(frames); !strings.Contains(s, "sky temperature over ambient, rank correlation -1.00") {
		t.Errorf("cloud: %q", s)
	}
	for i := range frames {
		frames[i].Result.Stars = 1000
	}
	if s := cloud(frames); s != "" {
		t.Errorf("cloud with steady stars: %q", s)
	}
}

// solved returns a 1″/px solution of a 1000 × 800 frame with the target
// at pixel (500 + dx, 400 + dy), turned by flip·180°.
func solvedAt(dx, dy float64, flip bool) fits.WCS {
	s := 1.0 / 3600
	if flip {
		s = -s
	}

	return fits.WCS{
		CD: [2][2]float64{{-s, 0}, {0, s}}, CRVAL: [2]float64{180, 45},
		CRPIX: [2]float64{500.5 + dx, 400.5 + dy}, NAXIS: [2]int{1000, 800}, OK: true,
	}
}

// pointingSession is 40 one-minute frames drifting 0.6 px (36″/h) a frame
// in x, dithered 5 px every 4th frame, with a meridian flip at frame 20
// when flip is set.
func pointingSession(flip bool) []Frame {
	t0 := time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC)
	var frames []Frame
	dy := 0.0
	for i := range 40 {
		if i%4 == 0 && i > 0 {
			dy = 5 * float64((i/4)%2)
		}
		r := analyze.Result{
			Time: t0.Add(time.Duration(i) * time.Minute),
			WCS:  solvedAt(0.6*float64(i), dy, flip && i >= 20),
		}
		frames = append(frames, Frame{Path: "f" + string(rune('a'+i%26)) + ".fits", Result: r})
	}

	return frames
}

func TestPointing(t *testing.T) {
	var b bytes.Buffer
	Pointing(&b, pointingSession(false))
	out := b.String()
	for _, want := range []string{
		"40 plate-solved frames in 1 run\n",
		"every 4 frames (median), 9 dithers of 5.0 px (median), at most 4 frames without one",
		"0.6′/h (median of 1 run)",
		"would not fire",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Pointing lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Flip") {
		t.Errorf("flip without one:\n%s", out)
	}
	frames := pointingSession(false)
	for i := range frames {
		frames[i].Result.WCS = solvedAt(0.6*float64(i), 0, false)
	}
	if s := dithers(newPointing(frames)); !strings.HasPrefix(s, "none in 39 frame steps") {
		t.Errorf("undithered: %q", s)
	}
}

func TestFlip(t *testing.T) {
	frames := pointingSession(true)
	var b bytes.Buffer
	Pointing(&b, frames)
	if !strings.Contains(b.String(), "in 2 runs") || !strings.Contains(b.String(), "meridian flip before fu.fits (2026-10-20 21:20)") {
		t.Errorf("flip:\n%s", b.String())
	}
	sides := pierSides(frames)
	if sides[19] != "before the flip" || sides[20] != "after the flip" {
		t.Errorf("sides %q, %q", sides[19], sides[20])
	}
	b.Reset()
	Field(&b, frames)
	if !strings.Contains(b.String(), "Field (before the flip)") || !strings.Contains(b.String(), "Field (after the flip)") {
		t.Errorf("field not split:\n%s", b.String())
	}
	// An alt-az mount turning 10° a frame never flips, even through 180°.
	for i := range frames {
		w := solvedAt(0, 0, false)
		s, c := math.Sincos(float64(i) * 10 * math.Pi / 180)
		w.CD = [2][2]float64{{-c / 3600, s / 3600}, {s / 3600, c / 3600}}
		frames[i].Result.WCS = w
	}
	if p := newPointing(frames); len(p.flips) != 0 {
		t.Errorf("alt-az rotation read as %d flips", len(p.flips))
	}
	// PIERSIDE wins over rotation.
	frames = pointingSession(false)
	for i := range frames {
		frames[i].Result.Pier = map[bool]string{true: "EAST", false: "WEST"}[i < 30]
	}
	if s := pierSides(frames); s == nil || s[29] != "pier east" || s[30] != "pier west" {
		t.Errorf("PIERSIDE sides: %v", s)
	}
}

// TestMasterNoiseDithered: a 9-frame dark master at 1.5 e⁻ read noise adds
// 0.5 e⁻ to every sub. Over the 40 frames of pointingSession, 10 dither
// positions (1 run + 9 dithers) spread it to 0.5/√10 = 0.16 e⁻ against the
// stack's 10/√40 = 1.58 e⁻: +0.5%.
func TestMasterNoiseDithered(t *testing.T) {
	frames := pointingSession(false)
	for i := range frames {
		frames[i].Result.NoiseE = 10
	}
	darks := []Calib{{Image: fits.Image{Meta: fits.Meta{Stack: 9, Date: "2026-10-01T10:00:00"}}}}
	if s := masterNoise(frames, darks, nil, 1.5); !strings.Contains(s, "dithered over ≤ 10 positions, it averages to ≥ 0.16 e⁻, stack noise ≥ +0.5%") {
		t.Errorf("dithered: %q", s)
	}
	for i := range frames {
		frames[i].Result.WCS = solvedAt(0.6*float64(i), 0, false)
	}
	if s := masterNoise(frames, darks, nil, 1.5); !strings.Contains(s, "not dithered, so stack noise ≥ +5%") {
		t.Errorf("not dithered: %q", s)
	}
}
