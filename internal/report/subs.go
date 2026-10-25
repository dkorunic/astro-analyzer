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
	// snrDepth is the Rose criterion: SNR 5 tells a feature from noise.
	snrDepth = 5
	// nightGain: past the knee another night adds less than 10% SNR.
	nightGain = 0.10
)

// subLengths are the sub lengths compared, seconds; the session's own is
// added, and only those within a factor of 4 of it are shown.
var subLengths = []float64{2, 5, 10, 15, 20, 30, 60, 120, 180, 300, 600}

// readNoisePreset is a camera's read noise at one gain setting, its chip
// (chips) and, for a smart telescope, what the header lacks: its central
// obstruction (mm) and throughput. Presets match INSTRUME/CAMERA by
// substring; the read noise only at that setting, as it changes with gain.
type readNoisePreset struct {
	camera, label, chip     string
	setting, rn             float64
	obstruction, throughput float64
}

// readNoisePresets are from astro-recommender's camera presets (ZWO's
// charts at its deep-sky gain, where high conversion gain switches on; the
// colour and mono versions agree), October 2026. Longer names come first
// where one contains another. The Origin Mark II's is measured: its
// 9-frame bias masters have a neighbouring-pixel σ of 27.6–29.0 ADU over
// two sessions, and with 25 ADU per count and EGAIN 0.2793 that bounds one
// frame's read noise at 3·29.0·0.0112 = 0.97 e⁻ (astro-recommender's 2.15
// is a vendor sheet's guess). The original Origin's the same way: σ 30.4
// ADU, 3·30.4·0.0165 = 1.5 e⁻. Both Origins are RASAs, a mirror at prime
// focus behind a 77 mm (Mark II) or 63 mm obstruction (Celestron). The
// Seestar S50 writes INSTRUME 'ZWO ASI462MC' at its gain 80, so the ASI462
// preset covers it.
var readNoisePresets = []readNoisePreset{
	{camera: "Origin678C", label: "≤ Celestron Origin Mark II at ISO 1000, from its bias master", setting: 1000, rn: 0.97, chip: imx678, obstruction: 77, throughput: 0.81},
	{camera: "Origin178", label: "≤ Celestron Origin at ISO 200, from its bias master", setting: 200, rn: 1.5, chip: imx178, obstruction: 63, throughput: 0.81},
	{camera: "ASI294MM", label: "IMX492 at gain 120", setting: 120, rn: 1.8, chip: "IMX492"},
	{camera: "ASI294", label: "IMX294 at gain 120", setting: 120, rn: 1.7, chip: "IMX294"},
	{camera: "ASI2600", label: "IMX571 at gain 100", setting: 100, rn: 1.4, chip: "IMX571"},
	{camera: "ASI2400", label: "IMX410 at gain 140", setting: 140, rn: 1.5, chip: "IMX410"},
	{camera: "ASI6200", label: "IMX455 at gain 100", setting: 100, rn: 1.35, chip: "IMX455"},
	{camera: "ASI533", label: "IMX533 at gain 100", setting: 100, rn: 1.5, chip: "IMX533"},
	{camera: "ASI585", label: "IMX585 at gain 200", setting: 200, rn: 1.0, chip: "IMX585"},
	{camera: "ASI183", label: "IMX183 at gain 120", setting: 120, rn: 2.1, chip: "IMX183"},
	{camera: "ASI678", label: "IMX678 at gain 182", setting: 182, rn: 0.9, chip: imx678},
	{camera: "ASI662", label: "IMX662 at gain 200", setting: 200, rn: 1.2, chip: "IMX662"},
	{camera: "ASI664", label: "IMX664 at gain 252", setting: 252, rn: 0.8, chip: "IMX664"},
	{camera: "ASI676", label: "IMX676 at gain 180", setting: 180, rn: 0.75, chip: "IMX676"},
	{camera: "ASI461", label: "IMX461 at gain 100", setting: 100, rn: 1.45, chip: "IMX461"},
	{camera: "ASI432", label: "IMX432 at gain 140", setting: 140, rn: 4.3, chip: "IMX432"},
	{camera: "ASI174", label: "IMX174 at gain 300", setting: 300, rn: 3.5, chip: "IMX174"},
	{camera: "ASI462", label: "IMX462 at gain 80", setting: 80, rn: 1.05, chip: "IMX462"},
	{camera: "ASI178", label: "IMX178 at gain 100", setting: 100, rn: 1.7, chip: imx178},
	{camera: "ASI485", label: "IMX485 at gain 80", setting: 80, rn: 2.3, chip: "IMX485"},
	{camera: "ASI482", label: "IMX482 at gain 80", setting: 80, rn: 4.5, chip: "IMX482"},
	{camera: "ASI224", label: "IMX224 at gain 60", setting: 60, rn: 1.6, chip: "IMX224"},
	{camera: "ASI715", label: "IMX715 at gain 100", setting: 100, rn: 1.2, chip: "IMX715"},
	{camera: "ASI4400", label: "IMX366 at gain 136", setting: 136, rn: 1.7, chip: "IMX366"},
	{camera: "ASI1600", label: "MN34230 at gain 139", setting: 139, rn: 1.8, chip: "MN34230"},
	{camera: "ASI290", label: "IMX290 at gain 60", setting: 60, rn: 1.65, chip: "IMX290"},
	{camera: "ASI220", label: "SC2210 at gain 106", setting: 106, rn: 1.03, chip: "SC2210"},
	{camera: "ASI120", label: "AR0130 at gain 33", setting: 33, rn: 3.9, chip: "AR0130"},
	{camera: "Neptune-C II", label: "IMX464 at gain 83", setting: 83, rn: 1.2, chip: "IMX464"},
}

// preset returns the frame's camera preset, at any gain setting. A camera
// without one (QHY, ToupTek, Player One...) gets its chip from its pixels,
// with no read noise.
func preset(m fits.Meta) (readNoisePreset, bool) {
	cam := strings.ToUpper(m.Camera)
	for _, p := range readNoisePresets {
		if strings.Contains(cam, strings.ToUpper(p.camera)) {
			return p, true
		}
	}
	if c := chipOf(m); c != "" {
		return readNoisePreset{chip: c, label: c + " by its pixels", setting: math.NaN(), rn: math.NaN()}, true
	}

	return readNoisePreset{}, false
}

// geometryTol is how far a frame's pitch and size may sit from a chip's:
// effective pixels differ from the nominal by up to 1.5% (Origin 3056 ×
// 2048 on a 3096 × 2080 IMX178, QHY268 6280 × 4210 on a 6248 × 4176
// IMX571).
const geometryTol = 0.02

// chipOf is the chip whose pitch and size are closest to the frame's
// XPIXSZ and NAXIS, at some binning, among those within geometryTol that
// ship in its version (colour or mono); "" when none is, or two match
// equally (the IMX290 and IMX462 share their geometry).
//
// ponytail: a cropped (ROI) frame matches nothing; the IMX492 only at
// ZWO's default 2×2 binning, as charted.
func chipOf(m fits.Meta) string {
	if !(m.Pixel > 0) {
		return ""
	}
	size := [2]float64{float64(max(m.Size[0], m.Size[1])), float64(min(m.Size[0], m.Size[1]))}
	found, best, tie := "", geometryTol, false
	for name, c := range chips {
		if c.pixel == 0 || (m.CFA && c.qe == 0) || (!m.CFA && c.monoQE == 0) {
			continue
		}
		bin := max(math.Round(m.Pixel/c.pixel), 1)
		d := max(math.Abs(m.Pixel/(bin*c.pixel)-1),
			math.Abs(size[0]*bin/float64(c.px[0])-1), math.Abs(size[1]*bin/float64(c.px[1])-1))
		switch {
		case d < best:
			found, best, tie = name, d, false
		case d == best && found != "":
			tie = true
		}
	}
	if tie {
		return ""
	}

	return found
}

// presetReadNoise returns the read noise preset for the frame's camera and
// gain setting, or NaN.
func presetReadNoise(m fits.Meta) (float64, string) {
	if p, ok := preset(m); ok && m.Setting == p.setting {
		return p.rn, p.label
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

	shot, v := math.NaN(), math.NaN() // e⁻/s and e⁻² per pixel
	switch {
	case !(rn > 0):
		fmt.Fprintln(w, "  Read noise      unknown for this camera and gain: set -read-noise (e⁻) to compare SNR")
	case len(vars) == 0:
		fmt.Fprintf(w, "  Read noise      %.2g e⁻ (%s), but the gain is unknown: set -gain\n", rn, src)
	default:
		v = stats.MedianOf(vars)
		shot = (v - rn*rn) / t
		fmt.Fprintf(w, "  Noise           %.1f e⁻ per pixel, read noise %.2g e⁻ (%s)\n", math.Sqrt(v), rn, src)
		if shot > 0 {
			fmt.Fprintf(w, "                  shot noise %.2g e⁻/s (sky, target, dark): read noise costs ≤ %.0f%% SNR from %.0f s\n",
				shot, readCost*100, rn*rn/(shot*(1/((1-readCost)*(1-readCost))-1)))
			if d := darkLine(frames); d != "" {
				fmt.Fprintln(w, "                  "+d)
			}
		} else {
			fmt.Fprintln(w, "                  no more than the read noise: check -gain and -read-noise")
			shot = math.NaN()
		}
	}

	if !math.IsNaN(shot) {
		depth(w, frames, t, v, shot)
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

// depth prints the faintest surface brightness the stack reaches at SNR
// snrDepth per arcsec², and how it grows with more integration. Per sub a
// signal S (e⁻/s per arcsec²) gives S·t against √(v/plate²) of noise, so
// N subs reach S/sky = snrDepth·√v·plate / (t·√N·B), B being the shot
// rate: depth grows 1.25·log₁₀ mag per factor of time. N is the subs'
// effective count under WBPP's PSF Signal Weight, (Σw)²/Σw².
//
// ponytail: dark current counts as sky and target light as sky (as in
// skyMag); no flat-field or pattern-noise floor, which would end the √N
// growth sooner. Add one from the master flat's noise when it matters.
func depth(w io.Writer, frames []Frame, t, v, shot float64) {
	plate := frames[0].Result.Plate
	if !(plate > 0) {
		return
	}
	var sum, sum2 float64
	var sky []float64
	known := 0
	for _, f := range frames {
		if wt := f.Result.PSFSW; wt > 0 {
			sum, sum2, known = sum+wt, sum2+wt*wt, known+1
		}
		if f.SkyMag > 0 { // also false for NaN
			sky = append(sky, f.SkyMag)
		}
	}
	n := float64(len(frames))
	weighted := ""
	if 2*known >= len(frames) { // ponytail: frames without a weight count as average ones
		n *= sum * sum / sum2 / float64(known)
		weighted = fmt.Sprintf(", %s by PSF Signal Weight", hours(n*t))
	}
	under := -2.5 * math.Log10(snrDepth*math.Sqrt(v)*plate/(t*math.Sqrt(n)*shot))
	mu := math.NaN()
	if len(sky) > 0 {
		mu = stats.MedianOf(sky) + under
	}
	at := func(m float64) string { // depth after m times the integration
		d := 1.25 * math.Log10(m)
		if math.IsNaN(mu) {
			return fmt.Sprintf("sky + %.2f mag", under+d)
		}

		return fmt.Sprintf("%.2f mag/arcsec²", mu+d)
	}
	fmt.Fprintf(w, "  Depth           %s in %d subs%s: SNR %d per arcsec² at %s\n",
		hours(float64(len(frames))*t), len(frames), weighted, snrDepth, at(1))

	nights := len(byNight(frames))
	if k := knee(n * t / float64(nights)); k > 0 {
		clock := "" // the subs to shoot, at this session's share of weight
		if weighted != "" {
			clock = fmt.Sprintf("≈ %s of subs, ", hours(k*float64(len(frames))/n))
		}
		fmt.Fprintf(w, "                  diminishing returns past %s (%s%s)\n", hours(k), clock, at(k/(n*t)))
	}
	fmt.Fprintln(w, "  Time         Depth              SNR")
	for _, m := range []float64{0.5, 1, 2, 4} {
		row := fmt.Sprintf("  %-11s  %-17s  %.0f%%", hours(m*n*t), at(m), 100*math.Sqrt(m))
		if m == 1 {
			row += " ← now"
		}
		fmt.Fprintln(w, row)
	}
}

// knee returns the effective integration (seconds) past which more is
// diminishing returns, given night, the effective integration one night
// of this session gathers (seconds): another such night adds less than
// nightGain of SNR, at 1/((1+g)²−1) = 4.8 nights.
func knee(night float64) float64 {
	// At T = k·night another night multiplies SNR by √(1+1/k).
	return night / ((1+nightGain)*(1+nightGain) - 1)
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

// darkLine estimates the dark current's part of the shot noise from the
// camera chip's chart at the median sensor temperature, "" when either is
// unknown.
func darkLine(frames []Frame) string {
	p, ok := preset(frames[0].Result.Meta)
	if !ok {
		return ""
	}
	var temps []float64
	for _, f := range frames {
		if !math.IsNaN(f.Result.Temp) {
			temps = append(temps, f.Result.Temp)
		}
	}
	t := stats.MedianOf(temps)
	if math.IsNaN(t) {
		return ""
	}
	c := chips[p.chip]
	src := p.chip + "'s chart"
	if c.dark == 0 {
		src = "a typical chip's, " + p.chip + " uncharted"
	}

	return fmt.Sprintf("dark current ≈ %.2g e⁻/s at %.0f °C (%s, doubling every 5 °C)", darkRate(c, t), t, src)
}
