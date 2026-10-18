// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package report

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/stats"
)

// WBPP defaults and rules, from PixInsight's BatchPreprocessing scripts.
const (
	fastFrames = 150  // BPP-Global.js:359, applied at BPP-FrameGroup.js:344
	minWeight  = 0.05 // BPP-Global.js:251: weight/max below this is dropped, BPP-Operations.js:1611
	expTol     = 2.0  // BPP-Global.js:253: |Δexposure| ≤ 2 s shares a master, BPP-FrameGroup.js:1230
	maxListed  = 8    // frame names printed before "and N more"
	// BPP-FrameGroup.js:1411 bestRejectionMethod, since WBPP 3.1.1
	// (change-log-WBPP.txt; Linear Fit above 15 frames before). WBPP offers
	// percentile, Winsorized sigma, linear fit, GESD and RCR; plain and
	// averaged sigma clipping were removed (BPP-StackEngine.js:1224).
	percentileN = 6
	winsorizedN = 14
	// Autocrop keeps the largest rectangle whose low rejection map is at
	// most 0.25 (BPP-Processing.js:250, TOLERANCE). Registration fills
	// pixels outside a frame with 0, which are rejected low, so that is
	// where at least 75% of the frames overlap.
	cropCover = 0.75
	// ponytail: weightings closer than 1% in SNR and stack FWHM are
	// called equivalent; the measurements are not finer than that.
	sameWeights = 0.01
)

// WBPP prints the WeightedBatchPreprocessing settings and automatic choices
// the data decides, per filter, for the unflagged frames: those are the ones
// that get stacked, and WBPP groups lights by filter.
func WBPP(w io.Writer, frames []Frame) {
	groups := byFilter(frames)
	for _, idx := range groups {
		sub := subset(frames, idx)
		wbppFor(w, sub, filterLabel(sub, groups))
	}
}

func wbppFor(w io.Writer, frames []Frame, label string) {
	in := slices.DeleteFunc(slices.Clone(frames), func(f Frame) bool { return len(f.Flags) > 0 })
	fmt.Fprintf(w, "\nWBPP%s: %d unflagged frames\n", label, len(in))
	if len(in) == 0 {
		return
	}

	var exps []float64
	for _, f := range in {
		if !math.IsNaN(f.Result.Exp) {
			exps = append(exps, f.Result.Exp)
		}
	}
	if len(exps) > 0 {
		lo, hi := slices.Min(exps), slices.Max(exps)
		if hi-lo > expTol {
			fmt.Fprintf(w, "  Exposure        %g–%g s: set Exposure tolerance to %g s for one master (the %g s default splits them)\n",
				lo, hi, math.Ceil(hi-lo), expTol)
		}
	}

	fast := len(in) >= fastFrames
	if fast {
		fmt.Fprintf(w, "  Integration     fast integration, which WBPP enables at %d+ frames: no minimum weight or rejection algorithm to set\n", fastFrames)
	}
	autocrop(w, in)
	normalization(w, in, fast)
	weighting(w, in, fast)
	if fast {
		return
	}

	// ponytail: one group per filter, as if the exposure tolerance above is
	// applied; WBPP normalises weights within each of its groups.
	best := math.Inf(-1)
	for _, f := range in {
		if !math.IsNaN(f.Result.PSFSW) {
			best = max(best, f.Result.PSFSW)
		}
	}
	var low []string
	if best > 0 {
		for _, f := range in {
			if f.Result.PSFSW/best < minWeight { // NaN compares false: unknown is not dropped
				low = append(low, filepath.Base(f.Path))
			}
		}
	}
	if len(low) > 0 {
		names := strings.Join(low[:min(len(low), maxListed)], ", ")
		if len(low) > maxListed {
			names += fmt.Sprintf(" and %d more", len(low)-maxListed)
		}
		fmt.Fprintf(w, "  Minimum weight  %d frames under %.0f%% of the best PSF Signal Weight; WBPP's %.2f default drops them: %s\n",
			len(low), minWeight*100, minWeight, names)
	}

	n := len(in) - len(low)
	alg := "Generalized ESD"
	switch {
	case n <= percentileN:
		alg = "Percentile clipping"
	case n <= winsorizedN:
		alg = "Winsorized sigma clipping"
	}
	fmt.Fprintf(w, "  Rejection       Auto picks %s for %d frames (WBPP 3.1.1+)\n", alg, n)
}

// solved returns the plate solutions of frames, in order.
func solved(frames []Frame) []fits.WCS {
	var ws []fits.WCS
	for _, f := range frames {
		if wcs := f.Result.WCS; wcs.OK && wcs.NAXIS[0] > 0 {
			ws = append(ws, wcs)
		}
	}

	return ws
}

// autocrop predicts WBPP's autocrop: the largest rectangle of the reference
// frame that at least cropCover of the frames overlap.
func autocrop(w io.Writer, frames []Frame) {
	ws := solved(frames)
	if len(ws) < 2 {
		return
	}
	counts, nx, ny := coverCounts(ws)
	keep := make([]bool, len(counts))
	for i, n := range counts {
		keep[i] = float64(n) >= cropCover*float64(len(ws))
	}
	cw, ch := maxRect(keep, nx)
	ref := ws[0].NAXIS
	pw, ph := float64(cw)*float64(ref[0])/float64(nx), float64(ch)*float64(ref[1])/float64(ny)
	fmt.Fprintf(w, "  Autocrop        keeps about %.0f × %.0f px, %.0f%% of the reference frame (where %.0f%%+ of frames overlap)\n",
		pw, ph, 100*float64(cw*ch)/float64(nx*ny), 100*cropCover)
}

// maxRect returns the width and height, in cells, of the largest rectangle
// of true cells in a row-major grid nx wide.
func maxRect(cells []bool, nx int) (int, int) {
	ny := len(cells) / nx
	var bw, bh int
	col := make([]bool, nx)
	for top := range ny {
		for x := range col {
			col[x] = true
		}
		for bot := top; bot < ny; bot++ {
			run := 0
			for x := range nx {
				col[x] = col[x] && cells[bot*nx+x]
				if run++; !col[x] {
					run = 0
				}
				if h := bot - top + 1; run*h > bw*bh {
					bw, bh = run, h
				}
			}
		}
	}

	return bw, bh
}

// normalization judges WBPP's local normalization from how much the sky
// gradients differ between frames once registered. PixInsight calls LN
// "crucial … especially when there are time-varying light gradients"
// (doc/tools/LocalNormalization, introduction). A difference above the stack's noise
// (frame noise / √N) survives integration: seams where frames stop
// overlapping and biased pixel rejection, which background extraction
// cannot fix. Gradients are carried onto the reference frame's grid, so a
// light dome fixed to the horizon turns on an alt-az mount's stack.
//
// ponytail: plane gradients only, and Noise is measured on the 2×2-binned
// image of CFA data, about half a debayered pixel's: errs toward LN.
func normalization(w io.Writer, frames []Frame, fast bool) {
	ws := solved(frames)
	var ref fits.WCS
	if len(ws) > 0 {
		ref = ws[0]
	}
	var gx, gy, noise []float64
	var dims [2]int
	for _, f := range frames {
		r := f.Result
		if ref.OK && !r.WCS.OK {
			continue // its rotation is unknown
		}
		if g, ok := skyGradient(r.Gradient, r.GradAngle, r.Size, r.WCS, ref); ok {
			gx, gy = append(gx, g[0]), append(gy, g[1])
			dims = r.Size
			if !math.IsNaN(r.Noise) {
				noise = append(noise, r.Noise)
			}
		}
	}
	if ref.OK {
		dims = ref.NAXIS
	}
	if len(gx) < 2 || len(noise) == 0 {
		return
	}
	mx, my := stats.MedianOf(gx), stats.MedianOf(gy)
	diff := make([]float64, len(gx))
	for i := range gx {
		diff[i] = math.Abs(gx[i]-mx)*float64(dims[0]) + math.Abs(gy[i]-my)*float64(dims[1])
	}
	d := stats.MedianOf(diff)
	ratio := d / (stats.MedianOf(noise) / math.Sqrt(float64(len(noise))))
	what := fmt.Sprintf("gradients differ between frames by %.1f ADU across the field (median), %.1f× the stack's noise", d, ratio)
	switch {
	case ratio < 1:
		fmt.Fprintf(w, "  Normalization   %s: local normalization can be off to save time\n", what)
	case fast:
		// BPP-Pipeline.js:626,662: no LN steps for fast-integrated groups.
		fmt.Fprintf(w, "  Normalization   %s;\n"+
			"                  WBPP skips local normalization under fast integration: turn that off to apply it, or remove the gradient from the master\n", what)
	default:
		fmt.Fprintf(w, "  Normalization   %s: keep local normalization on\n", what)
	}
}

// skyGradient turns a frame's plane gradient (range across the frame
// corner to corner, rising toward angle degrees from +x) into its slope in
// ADU per pixel of the reference frame's grid. Size is in sensor pixels,
// as the WCS is. Without plate solutions the frame's own grid is used.
func skyGradient(rangeADU, angle float64, size [2]int, wcs, ref fits.WCS) ([2]float64, bool) {
	if math.IsNaN(rangeADU) || size[0] == 0 {
		return [2]float64{}, false
	}
	c, s := math.Cos(angle*math.Pi/180), math.Sin(angle*math.Pi/180)
	// Per pixel the slope is k·(cos, sin), spanning k·(W|cos| + H|sin|).
	k := rangeADU / (float64(size[0])*math.Abs(c) + float64(size[1])*math.Abs(s))
	g := [2]float64{k * c, k * s}
	if !wcs.OK || !ref.OK {
		return g, true
	}
	// Frame pixel offsets map to reference ones by A = ref.CD⁻¹·CD, so the
	// slope, a gradient, maps by A⁻ᵀ = (CD⁻¹·ref.CD)ᵀ.
	f := wcs.CD
	det := f[0][0]*f[1][1] - f[0][1]*f[1][0]
	if det == 0 {
		return [2]float64{}, false
	}
	inv := [2][2]float64{{f[1][1] / det, -f[0][1] / det}, {-f[1][0] / det, f[0][0] / det}}
	var m [2][2]float64
	for i := range 2 {
		for j := range 2 {
			m[i][j] = inv[i][0]*ref.CD[0][j] + inv[i][1]*ref.CD[1][j]
		}
	}

	return [2]float64{m[0][0]*g[0] + m[1][0]*g[1], m[0][1]*g[0] + m[1][1]*g[1]}, true
}

// weighting compares the frame weightings WBPP offers on its integration
// path by the stack they would give: SNR against the best possible, and an
// approximate FWHM. Frames are taken as normalised to equal signal, so a
// frame's noise is 1/s with s = √(PSF SNR), and weights w give
// SNR = Σw / √(Σ w²/s²), largest, √(Σ s²), at w = s² (inverse variance).
// Regular integration uses PSF Signal Weight by default (BPP-Global.js:273)
// or PSF SNR; FastIntegration weights by nothing (its default), PSF SNR or
// Resolution, flux/(FWHMx·FWHMy) (doc/tools/FastIntegration, §2.13).
//
// ponytail: the stack FWHM is √(Σw·FWHM²/Σw), exact for Gaussians of equal
// flux; Resolution uses all fitted stars' flux, S ∝ s·Noise, where
// FastIntegration takes its four brightest stars.
func weighting(w io.Writer, frames []Frame, fast bool) {
	var s, fw, sw, res []float64
	for _, f := range frames {
		r := f.Result
		if !(r.PSFSNR > 0 && r.FWHM > 0 && r.PSFSW > 0 && r.Noise > 0) { // also false for NaN
			continue
		}
		si := math.Sqrt(r.PSFSNR)
		s, fw, sw = append(s, si), append(fw, r.FWHM), append(sw, r.PSFSW)
		res = append(res, si*r.Noise/(r.FWHM*r.FWHM))
	}
	if len(s) < 2 {
		return
	}
	var best float64
	for _, si := range s {
		best += si * si
	}
	best = math.Sqrt(best)
	stack := func(wt func(int) float64) (float64, float64) {
		var sum, sq, f2 float64
		for i, si := range s {
			x := wt(i)
			sum, sq, f2 = sum+x, sq+x*x/(si*si), f2+x*fw[i]*fw[i]
		}

		return sum / math.Sqrt(sq) / best, math.Sqrt(f2 / sum)
	}
	type option struct {
		name     string
		snr, fwh float64
	}
	opt := func(name string, wt func(int) float64) option {
		snr, fwh := stack(wt)

		return option{name: name, snr: snr, fwh: fwh}
	}
	psfSNR := opt("PSF SNR", func(i int) float64 { return s[i] * s[i] })
	var a, b option // the default and its alternative
	if fast {
		eq := opt("none (default)", func(int) float64 { return 1 })
		a, b = opt("Resolution", func(i int) float64 { return res[i] }), psfSNR
		fmt.Fprintf(w, "  Weighting       %-15s SNR %5.1f%% of best, stack FWHM ≈ %.2f px\n", eq.name, 100*eq.snr, eq.fwh)
		fmt.Fprintf(w, "                  %-15s SNR %5.1f%%, ≈ %.2f px\n", a.name, 100*a.snr, a.fwh)
		fmt.Fprintf(w, "                  %-15s SNR %5.1f%%, ≈ %.2f px\n", b.name, 100*b.snr, b.fwh)
		gain := 1 / eq.snr
		if gain*gain-1 < 1/float64(len(s)) {
			fmt.Fprintln(w, "  Recommendation  leave weighting off: it gains less than one more frame would")

			return
		}
		fmt.Fprintf(w, "  Recommendation  enable weighting: up to +%.1f%% SNR, worth %.1f%% more exposure, at almost no cost\n",
			100*(gain-1), 100*(gain*gain-1))
	} else {
		a, b = opt("PSF Signal Weight (default)", func(i int) float64 { return sw[i] }), psfSNR
		fmt.Fprintf(w, "  Weighting       %-27s SNR %5.1f%% of best, stack FWHM ≈ %.2f px\n", a.name, 100*a.snr, a.fwh)
		fmt.Fprintf(w, "                  %-27s SNR %5.1f%%, ≈ %.2f px\n", b.name, 100*b.snr, b.fwh)
		fmt.Fprint(w, "  Recommendation  ")
	}
	if fast {
		fmt.Fprint(w, "                  ")
	}
	an, bn := strings.TrimSuffix(a.name, " (default)"), b.name
	switch {
	case math.Abs(a.snr-b.snr) < sameWeights && math.Abs(a.fwh-b.fwh) < sameWeights*b.fwh:
		fmt.Fprintf(w, "%s and %s give the same stack here: either will do\n", an, bn)
	case a.snr >= b.snr && a.fwh <= b.fwh:
		fmt.Fprintf(w, "%s: deeper and sharper than %s\n", an, bn)
	case b.snr >= a.snr && b.fwh <= a.fwh:
		fmt.Fprintf(w, "%s: deeper and sharper than %s\n", bn, an)
	case a.snr > b.snr:
		fmt.Fprintf(w, "%s for depth, %s for detail\n", an, bn)
	default:
		fmt.Fprintf(w, "%s for depth, %s for detail\n", bn, an)
	}
}
