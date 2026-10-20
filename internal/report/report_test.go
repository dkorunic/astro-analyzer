// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package report

import (
	"bytes"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
		"FWHM tilt 83% extreme (corners 4.00–9.00 px), curvature +0% (corners vs centre)",
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

// TestWBPP: a frame under 5% of the best PSFSW is dropped, leaving 14 for
// Winsorized sigma clipping; mixed exposures ask for a wider tolerance; an
// unknown weight is not dropped; 150 frames switch to fast integration.
func TestWBPP(t *testing.T) {
	var frames []Frame
	for i := range 16 {
		r := analyze.Result{Exp: 60, PSFSW: 1}
		switch i {
		case 3:
			r.PSFSW = 0.04
		case 5:
			r.PSFSW = math.NaN()
		case 7:
			r.Exp = 120
		}
		frames = append(frames, Frame{Path: "/s/f" + strconv.Itoa(i) + ".fits", Result: r})
	}
	frames[9].Flags = []string{"FWHM px"}
	var b bytes.Buffer
	WBPP(&b, frames)
	for _, want := range []string{"15 unflagged", "tolerance to 60 s", "1 frames under 5%", ": f3.fits\n", "Winsorized sigma clipping for 14 frames"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
	b.Reset()
	WBPP(&b, slices.Repeat(frames[:1], 150))
	if s := b.String(); !strings.Contains(s, "fast integration") || strings.Contains(s, "Rejection") {
		t.Errorf("150 frames:\n%s", s)
	}
}

func TestMaxRect(t *testing.T) {
	g := []bool{
		true, true, false, false,
		true, true, true, true,
		true, true, true, true,
	}
	if w, h := maxRect(g, 4); w != 4 || h != 2 {
		t.Errorf("maxRect = %d × %d, want 4 × 2", w, h)
	}
}

// TestNormalization: on a field rotating 95°, a gradient fixed to the sky
// (moonlight high above) registers onto itself and needs no local
// normalization; one fixed to the sensor, as a light dome is on an alt-az
// mount, turns across the stack and does.
func TestNormalization(t *testing.T) {
	const s, k = 1.0 / 3600, 0.5 // deg/px; ADU/px slope
	ref := fits.WCS{CD: [2][2]float64{{-s, 0}, {0, s}}, CRVAL: [2]float64{10, 20}, CRPIX: [2]float64{100.5, 100.5}, NAXIS: [2]int{200, 200}, OK: true}
	run := func(skyFixed bool) string {
		var frames []Frame
		for i := range 20 {
			th := float64(5*i) * math.Pi / 180
			c, sn := math.Cos(th), math.Sin(th)
			wcs := ref
			// ref.CD·Rot(θ): the sensor turned by θ on the sky.
			wcs.CD = [2][2]float64{{-s * c, s * sn}, {s * sn, s * c}}
			angle := 0.0
			if skyFixed {
				angle = -float64(5 * i) // the sky's +x seen from the turned sensor
			}
			a := angle * math.Pi / 180
			r := analyze.Result{Gradient: k * 200 * (math.Abs(math.Cos(a)) + math.Abs(math.Sin(a))), GradAngle: angle, Noise: 1}
			r.WCS, r.Size = wcs, [2]int{200, 200}
			frames = append(frames, Frame{Result: r})
		}
		var b bytes.Buffer
		normalization(&b, frames, false)

		return b.String()
	}
	if got := run(true); !strings.Contains(got, "can be off") {
		t.Errorf("sky-fixed gradient:\n%s", got)
	}
	if got := run(false); !strings.Contains(got, "keep local normalization on") {
		t.Errorf("sensor-fixed gradient:\n%s", got)
	}
}

// TestWeighting: when the sharp frames are the faint ones, PSF SNR wins on
// depth and PSFSW on detail; identical frames make the choice moot; and
// FastIntegration weighting is not worth enabling for identical frames.
func TestWeighting(t *testing.T) {
	var frames []Frame
	for i := range 20 {
		r := analyze.Result{PSFSNR: 4, FWHM: 5, PSFSW: 1, Noise: 1}
		if i%2 == 0 {
			r.PSFSNR, r.FWHM, r.PSFSW = 1, 3, 3 // sharp but faint
		}
		frames = append(frames, Frame{Result: r})
	}
	var b bytes.Buffer
	weighting(&b, frames, false)
	if !strings.Contains(b.String(), "PSF SNR for depth, PSF Signal Weight for detail") {
		t.Errorf("mixed frames:\n%s", b.String())
	}
	same := slices.Repeat(frames[1:2], 20)
	b.Reset()
	weighting(&b, same, false)
	if !strings.Contains(b.String(), "either will do") {
		t.Errorf("identical frames:\n%s", b.String())
	}
	b.Reset()
	weighting(&b, same, true)
	if !strings.Contains(b.String(), "leave weighting off") {
		t.Errorf("identical frames, fast integration:\n%s", b.String())
	}
}

// TestSubLength checks the SNR and smear arithmetic: 2 e⁻/s of shot noise
// and 0.97 e⁻ read noise leave √(2/(2 + 0.9409/10)) = 97.7% at 10 s, and
// 0.005°/s turns the 2214.6 px corner radius by 1.93 px in 10 s. At 20 °C
// a chip without a chart makes 0.16·2⁻¹ = 0.08 e⁻/s of dark current.
func TestSubLength(t *testing.T) {
	start := time.Date(2026, 10, 2, 21, 0, 0, 0, time.UTC)
	var frames []Frame
	for i := range 20 {
		th := float64(i) * 0.075 * math.Pi / 180
		r := analyze.Result{FWHM: 5, NoiseE: math.Sqrt(2*15 + 0.97*0.97)}
		r.Exp, r.Camera, r.Setting, r.Size, r.Temp = 15, "Origin678C-x", 1000, [2]int{3856, 2180}, 20
		r.Time = start.Add(time.Duration(i) * 15 * time.Second)
		r.WCS = fits.WCS{OK: true, CD: [2][2]float64{{math.Cos(th), -math.Sin(th)}, {math.Sin(th), math.Cos(th)}}}
		frames = append(frames, Frame{Result: r})
	}
	var b bytes.Buffer
	SubLength(&b, frames, 0, "")
	for _, want := range []string{"read noise 0.97 e⁻ (≤ Celestron", "  10 s     97.7%                 1.9 / 1.9 px\n", "15 s", "← now", "dark current ≈ 0.08 e⁻/s at 20 °C (a typical chip's, IMX678 uncharted"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
	b.Reset()
	frames[0].Result.Setting = 120 // another gain: the preset no longer applies
	SubLength(&b, frames[:1], 0, "")
	if !strings.Contains(b.String(), "set -read-noise") {
		t.Errorf("unknown read noise:\n%s", b.String())
	}
}

func TestCalibration(t *testing.T) {
	light := analyze.Result{}
	light.Exp, light.Setting, light.Temp, light.Filter, light.Size = 20, 200, 15, "Nebula", [2]int{3056, 2048}
	light.Offset, light.Readout = 30, "High Gain"
	frames := []Frame{{Result: light}, {Result: light}}
	cal := func(typ string, exp, setting, temp float64, filter string, w int) Calib {
		return Calib{Image: fits.Image{Meta: fits.Meta{
			Type: typ, Exp: exp, Setting: setting, Temp: temp, Filter: filter, Size: [2]int{w, 2048},
			Date: "2025-10-17T21:32:13", Stack: 9,
		}}}
	}
	calib := []Calib{
		cal(fits.Bias, 0.001, 200, 30, "", 3056), cal(fits.Bias, 0.001, 200, 30, "", 3056),
		cal(fits.Dark, 30, 500, 30.4, "", 3056),
		cal(fits.Flat, 0.0589399, 23, 27, "Clear", 3000),
		cal(fits.DarkFlat, 0.1, 50, 34, "", 3000), // matches the flats' size, not their gain or exposure
	}
	for i := range 2 { // the bias matches the lights' offset and readout mode, the dark neither
		calib[i].Image.Meta.Offset, calib[i].Image.Meta.Readout = 30, "High Gain"
	}
	calib[2].Image.Meta.Offset, calib[2].Image.Meta.Readout = 10, "Low Noise"
	var b bytes.Buffer
	Calibration(&b, frames, calib, math.NaN(), nil)
	for _, want := range []string{
		"2, 1 distinct, 9-frame master  2025-10-17  0.001",
		"none at gain/ISO 200; none at offset 30; none in readout mode High Gain; none at 20 s, so scaled by exposure; 15.4 °C warmer",
		"0.0589", "size 3000 × 2048; none for filter Nebula",
		"size 3000 × 2048; none at the flats' gain/ISO 23; none at the flats' 0.0589 s; 7.0 °C warmer than the flats",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
	b.Reset()
	Calibration(&b, frames, nil, math.NaN(), nil) // lights only: no section
	if b.Len() != 0 {
		t.Errorf("no calibration frames printed:\n%s", b.String())
	}
}

func TestFocusDrift(t *testing.T) {
	var frames []Frame
	for i := range 12 {
		r := analyze.Result{FWHM: 3 + 0.1*float64(i)}
		r.Temp, r.Alt, r.FocusTemp = 20-0.5*float64(i), 60, math.NaN() // cooling 0.5 °C per frame

		frames = append(frames, Frame{Result: r})
	}
	if s := focusDrift(frames); !strings.Contains(s, "rank correlation -1.00 over 5.5 °C") {
		t.Errorf("FWHM rising as it cools: %q", s)
	}
	// The target setting from 70° to 37°, FWHM growing exactly as
	// airmass^0.6: seeing, not focus.
	for i := range frames {
		frames[i].Result.Alt = 70 - 3*float64(i)
		frames[i].Result.FWHM = 3 * math.Pow(1/math.Sin(frames[i].Result.Alt*math.Pi/180), 0.6)
	}
	if s := focusDrift(frames); s != "" {
		t.Errorf("seeing reported as focus drift: %q", s)
	}
}

func TestPedestal(t *testing.T) {
	frame := func(typ string, level, noise float64) Calib {
		return Calib{Image: fits.Image{Cal: fits.CalStats{Level: level}, PixNoise: noise, Meta: fits.Meta{Type: typ, Exp: 30, Setting: 200}}}
	}
	biases := []Calib{frame(fits.Bias, 4990, 30)}
	if got := pedestal([]Calib{frame(fits.Dark, 4868, 71)}, biases, 0.0165); !strings.HasPrefix(got, "122 ADU below the bias") {
		t.Errorf("dark below its bias: %q", got)
	}
	// 30 ADU over 30 s at 0.0165 e⁻/ADU.
	if got := pedestal([]Calib{frame(fits.Dark, 5020, 71)}, biases, 0.0165); got != "dark current 0.017 e⁻/s per pixel" {
		t.Errorf("dark above its bias: %q", got)
	}
	// Below by less than the bias noise: nothing to say.
	if got := pedestal([]Calib{frame(fits.Dark, 4980, 71)}, biases, 0.0165); got != "" {
		t.Errorf("dark at its bias: %q", got)
	}
	other := frame(fits.Dark, 4000, 71) // another ISO: no bias to compare
	other.Image.Meta.Setting = 500
	if got := pedestal([]Calib{other}, biases, 0.0165); got != "" {
		t.Errorf("dark at another ISO: %q", got)
	}
}

func TestBiasReadNoise(t *testing.T) {
	light := analyze.Result{
		Gain:    0.0165,
		Setting: 200,
	}
	frames := []Frame{{Result: light}}
	bias := func(path, date string, stack, noise float64) Calib {
		return Calib{Path: path, Image: fits.Image{PixNoise: noise, Meta: fits.Meta{Type: fits.Bias, Setting: 200, Date: date, Stack: stack}}}
	}
	pair := func(a, b string) (float64, error) {
		if a == "a" && b == "b" {
			return 60, nil
		}

		return math.NaN(), nil
	}
	nan := math.NaN()
	// Two separate frames: their difference, not the noisier single frames.
	rn, src := BiasReadNoise(frames, []Calib{bias("a", "d1", nan, 70), bias("b", "d2", nan, 70)}, pair)
	if !(math.Abs(rn-60*0.0165) < 1e-12) || !strings.Contains(src, "difference") {
		t.Errorf("pair: %v e⁻ %q", rn, src)
	}
	// Copies of a 9-frame master (one DATE-OBS): √9 × its pixel noise.
	rn, src = BiasReadNoise(frames, []Calib{bias("a", "d1", 9, 30.4), bias("b", "d1", 9, 30.4)}, pair)
	if !(math.Abs(rn-3*30.4*0.0165) < 1e-12) || src != "≤, from this session's 9-frame bias master" {
		t.Errorf("master: %v e⁻ %q", rn, src)
	}
	// A PixInsight master of five copies of that master: its NCOMBINE
	// overcounts, so the raw files' bound stands; alone, it is used.
	wbpp := bias("m", "d2", 5, 30.4)
	wbpp.Image.Meta.Master = true
	rn, _ = BiasReadNoise(frames, []Calib{bias("a", "d1", 9, 30.4), wbpp}, pair)
	if !(math.Abs(rn-3*30.4*0.0165) < 1e-12) {
		t.Errorf("raw and master: %v e⁻, want the raw files' %v", rn, 3*30.4*0.0165)
	}
	if rn, src = BiasReadNoise(frames, []Calib{wbpp}, pair); !(math.Abs(rn-math.Sqrt(5)*30.4*0.0165) < 1e-12) {
		t.Errorf("master alone: %v e⁻ %q", rn, src)
	}
	other := bias("a", "d1", 9, 30.4) // bias at another ISO: unknown
	other.Image.Meta.Setting = 500
	if rn, _ := BiasReadNoise(frames, []Calib{other}, pair); !math.IsNaN(rn) {
		t.Errorf("other ISO: %v", rn)
	}
	frames[0].Result.Gain = nan // no gain: unknown
	if rn, _ := BiasReadNoise(frames, []Calib{bias("a", "d1", 9, 30.4)}, pair); !math.IsNaN(rn) {
		t.Errorf("no gain: %v", rn)
	}
}

func TestFindings(t *testing.T) {
	var frames []Frame
	for i := range 100 {
		r := analyze.Result{Gain: 0.0165, NoiseE: 10, Hot: 3000}
		r.Exp, r.Setting, r.Filter = 20, 200, "Nebula"
		frames = append(frames, Frame{Path: strconv.Itoa(i), Result: r})
	}
	frames[0].Flags = []string{"fwhm"} // flagged: not in the stack
	meta := func(typ string, exp float64) fits.Meta {
		return fits.Meta{Type: typ, Exp: exp, Setting: 200, Date: "2025-10-17T21:32:13", Stack: 9}
	}
	calib := []Calib{
		{Image: fits.Image{PixNoise: 30, Meta: meta(fits.Bias, 0.001), Cal: fits.CalStats{Level: 4990, Rows: 1.8, Cols: 1.2}}},
		{Image: fits.Image{PixNoise: 70, Meta: meta(fits.Dark, 30), Cal: fits.CalStats{Level: 4868}}, Hot: 4500},
		{Image: fits.Image{Range: 65535, Meta: meta(fits.Flat, 0.06), Cal: fits.CalStats{Level: 32768, Dust: 2, DustDepth: 0.031}}},
	}
	var pedestals []float64
	pairGain := func(a, b string, pedestal float64) (float64, error) {
		pedestals = append(pedestals, pedestal)

		return 0.016, nil
	}
	var b bytes.Buffer
	Calibration(&b, frames, calib, 1.5, pairGain)
	for _, want := range []string{
		"Bias     banding 1.8 ADU between rows, 1.2 between columns (6% of the 30 ADU pixel noise)",
		"Dark     122 ADU below the bias, so dark − bias (to scale or optimise it) is negative; hot pixels 1.50× the lights', 1.00× scaled to 20 s",
		"Flat     level 50% of range, 2 dust shadows, deepest 3.1%",
		// The dark sits below the bias, so it is the pedestal and the gain a bound.
		"Gain     ≥ 0.016 e⁻/ADU by photon transfer on 3 pairs of neighbouring lights (pedestal from the dark: its dark current counts as noise); the gain in use, 0.0165, is consistent",
		// 1.5/√9 = 0.5 e⁻ against 10/√99 = 1.005 e⁻: √(1 + 0.2475) − 1 = 11.7%.
		"Masters  the 9-frame dark adds ≥ 0.5 e⁻ per pixel, the same in every sub, to the 99-sub stack's 1 e⁻: undithered, stack noise ≥ +12%",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
	if !slices.Equal(pedestals, []float64{4868, 4868, 4868}) {
		t.Errorf("pedestals %v, want the dark's level three times", pedestals)
	}
}

func TestGainCheck(t *testing.T) {
	frames := make([]Frame, 8)
	for i := range frames {
		frames[i].Result.Gain = 0.0742 // the raw Origin178 EGAIN
	}
	bias := []Calib{{Image: fits.Image{Cal: fits.CalStats{Level: 5000}}}}
	measured := func(string, string, float64) (float64, error) { return 0.0162, nil }
	for _, c := range []struct {
		darks []Calib
		want  string
	}{
		// Bias pedestal: a plain measurement; 4.6× off.
		{want: "; the gain in use, 0.0742, is 4.6× that: run with -gain 0.0162"},
		// Dark pedestal: a lower bound, but 4.6× is beyond what dark current hides.
		{darks: []Calib{{Image: fits.Image{Cal: fits.CalStats{Level: 4800}}}}, want: "is 4.6× that"},
		// A dark above the bias leaves the bias as pedestal.
		{darks: []Calib{{Image: fits.Image{Cal: fits.CalStats{Level: 5100}}}}, want: "neighbouring lights; the gain"},
	} {
		if got := gainCheck(frames, bias, c.darks, measured); !strings.Contains(got, c.want) {
			t.Errorf("darks %v: %q, want %q", c.darks, got, c.want)
		}
	}
	for i := range frames { // the corrected gain agrees
		frames[i].Result.Gain = 0.0165
	}
	if got := gainCheck(frames, bias, nil, measured); !strings.HasSuffix(got, "the gain in use, 0.0165, agrees") {
		t.Errorf("agreeing gain: %q", got)
	}
}

// TestRefocus checks FWHM growth between focuser moves: three runs of 8
// frames, each growing from 3.0 to 3.3 px: the last three frames' median
// 3.257 against the first three's 3.043 is +7%. Also focuser temperature
// taking over from the sensor's in focusDrift.
func TestRefocus(t *testing.T) {
	var frames []Frame
	for i := range 24 {
		r := analyze.Result{FWHM: 3 + 0.3*float64(i%8)/7}
		r.Focus, r.Temp, r.FocusTemp = float64(1000+10*(i/8)), 20, math.NaN()
		frames = append(frames, Frame{Result: r})
	}
	if s := refocus(frames); !strings.Contains(s, "moved 2 times; FWHM grows +7%") || !strings.Contains(s, "median of 3 runs") {
		t.Errorf("refocus: %q", s)
	}
	for i := range frames {
		frames[i].Result.Focus = math.NaN()
	}
	if s := refocus(frames); s != "" {
		t.Errorf("no focuser: %q", s)
	}
	for i := range frames { // the tube cools as FWHM grows; the sensor stays at 20 °C
		frames[i].Result.FWHM, frames[i].Result.FocusTemp = 3+0.05*float64(i), 15-0.2*float64(i)
	}
	if s := focusDrift(frames); !strings.Contains(s, "focuser temperature (rank correlation -1.00") {
		t.Errorf("focusDrift with FOCTEMP: %q", s)
	}
}

// TestTiltClass: a bound starts the class above it, as ASTAP's p < bound.
func TestTiltClass(t *testing.T) {
	for _, c := range []struct {
		corner float64
		want   string
	}{{2.09, "none"}, {2.11, "almost none"}, {2.36, "moderate"}, {2.5, "severe"}, {3, "extreme"}} {
		f := [9]float64{2, 2, 2, 2, 2, 2, 2, 2, c.corner}
		if got := tilt(f); !strings.Contains(got, "% "+c.want+" (") {
			t.Errorf("corner %.2f: %q, want %s", c.corner, got, c.want)
		}
	}
}

// TestCommonDir: the deepest directory holding every frame, matched on
// whole path components ("/s/ab" is not inside "/s/a").
func TestCommonDir(t *testing.T) {
	for _, c := range []struct {
		want  string
		paths []string
	}{
		{want: "."},
		{paths: []string{"/s/a/f1.fits"}, want: "/s/a"},
		{paths: []string{"/s/a/f1.fits", "/s/a/f2.fits"}, want: "/s/a"},
		{paths: []string{"/s/a/f1.fits", "/s/ab/f2.fits"}, want: "/s"},
		{paths: []string{"/s/a/x/f1.fits", "/t/f2.fits"}, want: "/"},
	} {
		frames := make([]Frame, len(c.paths))
		for i, p := range c.paths {
			frames[i].Path = p
		}
		if got := commonDir(frames); got != c.want {
			t.Errorf("commonDir(%v) = %q, want %q", c.paths, got, c.want)
		}
	}
}

// TestTable: paths print relative to their common directory, unknown
// values as "-", and flags comma-joined.
func TestTable(t *testing.T) {
	r := analyze.Result{FWHM: 3.25, Stars: 120, Ecc: math.NaN()}
	r.Date, r.Filter = "2026-10-02T21:00:00", "L"
	frames := []Frame{
		{Path: "/s/a/f1.fits", Result: r, Flags: []string{"FWHM px", "Stars"}},
		{Path: "/s/b/f2.fits", Result: analyze.Result{FWHM: math.NaN()}},
	}
	var b bytes.Buffer
	Table(&b, frames)
	lines := strings.Split(b.String(), "\n")
	if lines[0] != "Directory: /s" || !strings.HasPrefix(lines[2], "File ") {
		t.Fatalf("header:\n%s", b.String())
	}
	if f := strings.Fields(lines[3]); f[0] != "a/f1.fits" || f[1] != r.Date || f[2] != "L" || f[len(f)-1] != "px,Stars" || !strings.Contains(lines[3], "3.25") {
		t.Errorf("row 1: %q", lines[3])
	}
	if f := strings.Fields(lines[4]); f[0] != "b/f2.fits" || f[1] != "-" || f[2] != "-" {
		t.Errorf("row 2: %q", lines[4])
	}
}

// TestAutocrop: of a 200 × 100 frame and its copy turned 90°, only the
// central 100 × 100 square has both, so WBPP's autocrop keeps it.
func TestAutocrop(t *testing.T) {
	s := 1.0 / 3600
	ref := fits.WCS{CD: [2][2]float64{{-s, 0}, {0, s}}, CRVAL: [2]float64{10, 20}, CRPIX: [2]float64{100.5, 50.5}, NAXIS: [2]int{200, 100}, OK: true}
	turned := ref
	turned.CD = [2][2]float64{{0, s}, {s, 0}}
	frames := make([]Frame, 2)
	frames[0].Result.WCS, frames[1].Result.WCS = ref, turned
	var b bytes.Buffer
	autocrop(&b, frames)
	if !strings.Contains(b.String(), "keeps about 100 × 100 px, 50% of the reference frame") {
		t.Errorf("autocrop: %q", b.String())
	}
	b.Reset()
	autocrop(&b, frames[:1])
	if b.Len() != 0 {
		t.Errorf("one solved frame: %q", b.String())
	}
}
