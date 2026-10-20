// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package report

import (
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/stats"
)

// Sub-length comparison: read noise is paid once per sub, shot noise per
// second, so for a fixed total time a sub of t seconds keeps
// √(B / (B + RN²/t)) of the SNR an ideal (noiseless-readout) camera gets,
// B being the shot-noise rate per pixel. An alt-az mount also turns the
// field during each sub, smearing stars at radius r by r·ω·t.
const (
	// The rule of thumb (Robin Glover, "Deep Sky CMOS Imaging", 2019):
	// read noise may cost 5% of the SNR, i.e. B·t ≈ 10·RN².
	readCost = 0.05
	// Consecutive frames further apart than subGap·exp + 30 s straddle a
	// pause (meridian, refocus) and do not measure the rotation rate.
	subGap   = 3
	minPairs = 10 // rotation rates needed for the smear columns
)

// subLengths are the sub lengths compared, seconds; the session's own is
// added, and only those within a factor of 4 of it are shown.
var subLengths = []float64{2, 5, 10, 15, 20, 30, 60, 120, 180, 300, 600}

// readNoisePreset is a camera's read noise at one gain setting. Presets
// match INSTRUME/CAMERA by substring and only at that setting, as read
// noise changes with gain.
type readNoisePreset struct {
	camera, label string
	setting, rn   float64
}

// readNoisePresets are from astro-recommender's camera presets (ZWO's
// charts at its deep-sky gain, where high conversion gain switches on; the
// colour and mono versions agree), October 2026. Longer names come first
// where one contains another. The Origin Mark II's is measured: its
// 9-frame bias master has a spatial σ of 32 ADU, and with 25 ADU per count
// and EGAIN 0.2793 that bounds one frame's read noise at 3·32·0.0112 =
// 1.1 e⁻ (astro-recommender's 2.15 is a vendor sheet's guess). The original
// Origin's the same way: σ 32.2 ADU (biasStandardDeviation in its
// dark0009.json), 3·32.2·0.0165 = 1.6 e⁻.
var readNoisePresets = []readNoisePreset{
	{camera: "Origin678C", label: "≤ Celestron Origin Mark II at ISO 1000, from its bias master", setting: 1000, rn: 1.1},
	{camera: "Origin178", label: "≤ Celestron Origin at ISO 200, from its bias master", setting: 200, rn: 1.6},
	{camera: "ASI294MM", label: "IMX492 at gain 120", setting: 120, rn: 1.8},
	{camera: "ASI294", label: "IMX294 at gain 120", setting: 120, rn: 1.7},
	{camera: "ASI2600", label: "IMX571 at gain 100", setting: 100, rn: 1.4},
	{camera: "ASI2400", label: "IMX410 at gain 140", setting: 140, rn: 1.5},
	{camera: "ASI6200", label: "IMX455 at gain 100", setting: 100, rn: 1.35},
	{camera: "ASI533", label: "IMX533 at gain 100", setting: 100, rn: 1.5},
	{camera: "ASI585", label: "IMX585 at gain 200", setting: 200, rn: 1.0},
	{camera: "ASI183", label: "IMX183 at gain 120", setting: 120, rn: 2.1},
	{camera: "ASI678", label: "IMX678 at gain 182", setting: 182, rn: 0.9},
	{camera: "ASI662", label: "IMX662 at gain 200", setting: 200, rn: 1.2},
	{camera: "ASI664", label: "IMX664 at gain 252", setting: 252, rn: 0.8},
	{camera: "ASI676", label: "IMX676 at gain 180", setting: 180, rn: 0.75},
	{camera: "ASI461", label: "IMX461 at gain 100", setting: 100, rn: 1.45},
	{camera: "ASI432", label: "IMX432 at gain 140", setting: 140, rn: 4.3},
	{camera: "ASI174", label: "IMX174 at gain 300", setting: 300, rn: 3.5},
	{camera: "ASI462", label: "IMX462 at gain 80", setting: 80, rn: 1.05},
	{camera: "ASI178", label: "IMX178 at gain 100", setting: 100, rn: 1.7},
	{camera: "ASI485", label: "IMX485 at gain 80", setting: 80, rn: 2.3},
	{camera: "ASI482", label: "IMX482 at gain 80", setting: 80, rn: 4.5},
	{camera: "ASI224", label: "IMX224 at gain 60", setting: 60, rn: 1.6},
	{camera: "Neptune-C II", label: "IMX464 at gain 83", setting: 83, rn: 1.2},
}

// presetReadNoise returns the read noise preset for the frame's camera and
// gain setting, or NaN.
func presetReadNoise(m fits.Meta) (float64, string) {
	cam := strings.ToUpper(m.Camera)
	for _, p := range readNoisePresets {
		if strings.Contains(cam, strings.ToUpper(p.camera)) && m.Setting == p.setting {
			return p.rn, p.label
		}
	}

	return math.NaN(), ""
}

// SubLength compares sub lengths per filter: the SNR read noise leaves at
// each, from the measured per-pixel noise, and the corner smear of field
// rotation, from consecutive plate solutions. Read noise rn (e⁻), from src
// (-read-noise or bias frames), overrides the camera preset when positive.
func SubLength(w io.Writer, frames []Frame, rn float64, src string) {
	groups := byFilter(frames)
	for _, idx := range groups {
		sub := subset(frames, idx)
		subLength(w, sub, filterLabel(sub, groups), rn, src)
	}
}

func subLength(w io.Writer, frames []Frame, label string, rn float64, src string) {
	var exps, vars, fwhm []float64
	for _, f := range frames {
		r := f.Result
		if r.Exp > 0 {
			exps = append(exps, r.Exp)
		}
		if r.NoiseE > 0 {
			vars = append(vars, r.NoiseE*r.NoiseE)
		}
		if r.FWHM > 0 {
			fwhm = append(fwhm, r.FWHM)
		}
	}
	if len(exps) == 0 {
		return
	}
	t := stats.MedianOf(exps)
	if !(rn > 0) {
		rn, src = presetReadNoise(frames[0].Result.Meta)
	}
	fmt.Fprintf(w, "\nSub length%s: %g s subs\n", label, t)

	shot := math.NaN() // e⁻/s per pixel
	switch {
	case !(rn > 0):
		fmt.Fprintln(w, "  Read noise      unknown for this camera and gain: set -read-noise (e⁻) to compare SNR")
	case len(vars) == 0:
		fmt.Fprintf(w, "  Read noise      %.2g e⁻ (%s), but the gain is unknown: set -gain\n", rn, src)
	default:
		v := stats.MedianOf(vars)
		shot = (v - rn*rn) / t
		fmt.Fprintf(w, "  Noise           %.1f e⁻ per pixel, read noise %.2g e⁻ (%s)\n", math.Sqrt(v), rn, src)
		if shot > 0 {
			fmt.Fprintf(w, "                  shot noise %.2g e⁻/s (sky, target, dark): read noise costs ≤ %.0f%% SNR from %.0f s\n",
				shot, readCost*100, rn*rn/(shot*(1/((1-readCost)*(1-readCost))-1)))
		} else {
			fmt.Fprintln(w, "                  no more than the read noise: check -gain and -read-noise")
			shot = math.NaN()
		}
	}

	rates := rotationRates(frames, t)
	smear := len(rates) >= minPairs
	if math.IsNaN(shot) && !smear {
		return
	}
	var p50, p90, corner float64
	if smear {
		slices.Sort(rates)
		p50, p90 = stats.Median(rates), rates[(len(rates)-1)*9/10]
		size := frames[0].Result.Size
		corner = math.Hypot(float64(size[0]), float64(size[1])) / 2 * math.Pi / 180
		fmt.Fprintf(w, "  Field rotation  %.4f°/s median, %.4f°/s p90; corner stars smear tangentially (median FWHM %.2f px)\n",
			p50, p90, stats.MedianOf(fwhm))
	}

	lengths := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(subLengths), t))))
	head := "  Sub      "
	if !math.IsNaN(shot) {
		head += "SNR vs no read noise  "
	}
	if smear {
		head += "Corner smear median / p90"
	}
	fmt.Fprintln(w, strings.TrimRight(head, " "))
	for _, s := range lengths {
		if s < t/4 || s > 4*t {
			continue
		}
		row := fmt.Sprintf("  %-6s   ", fmt.Sprintf("%g s", s))
		if !math.IsNaN(shot) {
			row += fmt.Sprintf("%-20s  ", fmt.Sprintf("%.1f%%", 100*math.Sqrt(shot/(shot+rn*rn/s))))
		}
		if smear {
			row += fmt.Sprintf("%-25s", fmt.Sprintf("%.1f / %.1f px", p50*s*corner, p90*s*corner))
		}
		if s == t {
			row += " ← now"
		}
		fmt.Fprintln(w, strings.TrimRight(row, " "))
	}
}

// rotationRates returns |dθ/dt| in degrees per second between consecutive
// plate-solved frames, skipping pauses longer than subGap subs.
func rotationRates(frames []Frame, exp float64) []float64 {
	var rates []float64
	for i := 1; i < len(frames); i++ {
		a, b := frames[i-1].Result, frames[i].Result
		dt := b.Time.Sub(a.Time).Seconds()
		if !a.WCS.OK || !b.WCS.OK || a.Time.IsZero() || !(dt > 0 && dt <= subGap*exp+30) {
			continue
		}
		d := math.Mod(b.WCS.Rotation()-a.WCS.Rotation()+540, 360) - 180
		rates = append(rates, math.Abs(d)/dt)
	}

	return rates
}
