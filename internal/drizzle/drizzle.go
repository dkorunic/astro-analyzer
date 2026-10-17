// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

// Package drizzle recommends PixInsight DrizzleIntegration settings (Scale
// and Drop shrink) for a session.
//
// Sources: Fruchter & Hook 2002, PASP 114, 144, "Drizzle: A Method for the
// Linear Reconstruction of Undersampled Images" (well sampled is ≳ 2.5 px per
// FWHM; scale s, pixfrac p, noise correlation R); the DrizzlePac Handbook v3,
// §7 (weight image rms under 20% of its median; pixfrac 0.5–1.0, slightly
// larger than the output pixel); and PixInsight: WBPP warns below 15 frames
// and defaults CFA drizzle to Scale 1, Drop shrink 1
// (BatchPreprocessing/BPP-Diagnostic.js:526, BPP-Global.js:347-349), and Bayer
// drizzle needs three to four times as many frames as monochrome
// (doc/tools/DrizzleIntegration, §2.2).
//
// PixInsight's Scale is 1/s: Scale 2 makes output pixels half an input pixel.
package drizzle

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"

	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/stats"
)

const (
	minFrames = 15  // WBPP: drizzle "requires more than 15 frames"
	cfaFrames = 3   // PixInsight: Bayer drizzle needs 3–4× the frames of mono
	Nyquist   = 2.5 // F&H §1: a well-sampled detector has ≳ 2.5 px across the FWHM
	maxScale  = 3
	maxRMS    = 0.2 // DrizzlePac §7: weight image rms under 20% of its median
	minRatio  = 1.2 // drop ≥ 1.2× output pixel: F&H's typical p = 0.6, s = 0.5
	minDrop   = 0.5 // DrizzlePac §7: routine pixfrac "between 0.5 and 1.0"
	step      = 0.05
	// box is the squared Gaussian FWHM of a unit box, 8·ln2/12: the
	// pixel, drop and output-pixel convolutions add in quadrature (F&H §1).
	box = 0.4621
)

// Frame is what the recommendation needs from one analysed light frame.
type Frame struct {
	WCS  fits.WCS
	FWHM float64 // sensor pixels
	CFA  bool
}

// Recommendation is the advice for one session. Reason is set when drizzle
// is not recommended; Scale and DropShrink are then zero.
type Recommendation struct {
	Reason     string
	Dither     string  // how sub-pixel coverage was established
	FWHM       float64 // median, sensor pixels
	DropShrink float64
	WeightRMS  float64 // simulated weight image rms/median at the recommendation
	R          float64 // noise correlation ratio, Fruchter & Hook eq. 9–10
	Scale      int
	Frames     int
	CFA        bool
}

// Recommend picks the largest useful Scale and the smallest Drop shrink that
// still covers the output grid evenly, given the measured sub-pixel dither.
func Recommend(frames []Frame) Recommendation {
	fw := make([]float64, 0, len(frames))
	for _, f := range frames {
		if !math.IsNaN(f.FWHM) {
			fw = append(fw, f.FWHM)
		}
	}
	rec := Recommendation{Frames: len(frames), FWHM: stats.MedianOf(fw)}
	if len(frames) > 0 {
		rec.CFA = frames[0].CFA
	}
	need := minFrames
	if rec.CFA {
		need *= cfaFrames
	}
	if len(frames) < need {
		rec.Reason = fmt.Sprintf("only %d frames; drizzle needs %d+ well-dithered frames", len(frames), need)
		if rec.CFA {
			rec.Reason += " for a colour camera"
		}

		return rec
	}
	if math.IsNaN(rec.FWHM) {
		rec.Reason = "no star FWHM measured"

		return rec
	}

	ph, dither, ok := newPhases(frames)
	rec.Dither = dither
	if !ok {
		rec.Reason = "frames are not dithered, so drizzle cannot recover resolution"

		return rec
	}

	want := 1
	if rec.FWHM < Nyquist {
		want = scaleFor(rec.FWHM)
	}
	if want == 1 && !rec.CFA {
		rec.Reason = fmt.Sprintf("stars are well sampled (FWHM ≥ %.1f px): drizzle adds no resolution, integrate normally", Nyquist)

		return rec
	}
	// A CFA colour occupies a 2x2 lattice, so its drops are four times
	// sparser than a monochrome sensor's.
	lattice := 1
	if rec.CFA {
		lattice = 2
	}
	for k := want; k >= 1; k-- {
		for _, p := range drops(k, rec.CFA) {
			if rms := ph.worst(lattice, k, p); rms <= maxRMS {
				rec.Scale, rec.DropShrink, rec.WeightRMS = k, p, rms
				rec.R = correlation(p, 1/float64(k))

				return rec
			}
		}
	}
	rec.Reason = "dither coverage too uneven even at Drop shrink 1; take more or better-dithered frames"

	return rec
}

// scaleFor returns the smallest Scale whose output pixels sample the drizzled
// star at Nyquist. fwhm is the optics alone (the PSF fit integrates over the
// pixel), but the drizzled image is still convolved with the detector pixel,
// the drop and the output pixel (F&H eq. 1 with P replaced by p), so a finer
// Scale than Nyquist/fwhm only adds noise.
//
// ponytail: boxes approximated by Gaussians of equal variance, as F&H do.
func scaleFor(fwhm float64) int {
	for k := 2; k < maxScale; k++ {
		s := 1 / float64(k)
		p := max(minDrop, minRatio*s) // the sharpest drop drops() tries
		if float64(k)*math.Sqrt(fwhm*fwhm+box*(1+p*p+s*s)) >= Nyquist {
			return k
		}
	}

	return maxScale
}

// drops lists candidate Drop shrink values, sharpest first.
func drops(k int, cfa bool) []float64 {
	if k == 1 {
		if cfa {
			return []float64{1} // PixInsight's CFA drizzle setting
		}

		return nil
	}
	var ps []float64
	for i := int(math.Ceil(max(minDrop, minRatio/float64(k)) / step)); float64(i)*step <= 1+1e-9; i++ {
		ps = append(ps, math.Round(float64(i)*step*100)/100)
	}

	return ps
}

// correlation is Fruchter & Hook's noise correlation ratio R for a uniformly
// filled dither pattern (eq. 9–10): the factor by which pixel-to-pixel noise
// in the drizzled image underestimates the true noise on larger scales.
func correlation(p, s float64) float64 {
	r := p / s
	if r >= 1 {
		return r / (1 - 1/(3*r))
	}

	return 1 / (1 - r/3)
}

// phases holds, per sample point across the field, where each frame's
// nearest pixel centre lands on the reference frame's grid. Values are in
// reference pixels; coverage reduces them modulo the lattice period.
type phases struct {
	points [][][2]float64
	random bool // no plate solutions: phases drawn uniformly
}

// newPhases measures sub-pixel dither from plate solutions at five points
// (centre and near the corners), so field rotation is captured too. Frames
// without a solution are left out: their pointing is unknown, and a mount's
// guess would pass for a dither. With fewer than minFrames solutions it
// assumes random dithers. ok is false when the frames never move by a pixel.
func newPhases(all []Frame) (phases, string, bool) {
	frames := slices.DeleteFunc(slices.Clone(all), func(f Frame) bool { return !f.WCS.OK || f.WCS.NAXIS[0] == 0 })
	if len(frames) < minFrames {
		rng := rand.New(rand.NewPCG(7, 8)) //nolint:gosec // deterministic stand-in for unknown dithers
		pts := make([][2]float64, len(all))
		for i := range pts {
			pts[i] = [2]float64{rng.Float64() * 2, rng.Float64() * 2}
		}

		return phases{points: [][][2]float64{pts}, random: true}, "assumed random (too few plate solutions in headers)", true
	}

	ref := frames[0].WCS
	w, h := float64(ref.NAXIS[0]), float64(ref.NAXIS[1])
	var ph phases
	var spread, rot float64
	for _, f := range [][2]float64{{0.5, 0.5}, {0.2, 0.2}, {0.8, 0.2}, {0.2, 0.8}, {0.8, 0.8}} {
		ra, dec := ref.World(f[0]*w, f[1]*h)
		pts := make([][2]float64, len(frames))
		xs, ys := make([]float64, len(frames)), make([]float64, len(frames))
		for i, fr := range frames {
			x, y := fr.WCS.Pixel(ra, dec)
			xs[i], ys[i] = x, y
			// The nearest pixel centre on an even (CFA colour) lattice,
			// carried to the reference grid. Both lattices are tracked by
			// taking the period-2 position; the mono lattice uses it mod 1.
			cx, cy := 2*math.Round((x-1)/2)+1, 2*math.Round((y-1)/2)+1
			pra, pdec := fr.WCS.World(cx, cy)
			pts[i][0], pts[i][1] = ref.Pixel(pra, pdec)
		}
		ph.points = append(ph.points, pts)
		spread = max(spread, slices.Max(xs)-slices.Min(xs), slices.Max(ys)-slices.Min(ys))
	}
	for _, f := range frames {
		d := math.Abs(math.Mod(f.WCS.Rotation()-ref.Rotation()+540, 360) - 180)
		rot = max(rot, d)
	}
	desc := fmt.Sprintf("measured from plate solutions: stars move up to %.0f px", spread)
	if rot >= 1 {
		desc += fmt.Sprintf(", field rotates %.0f°", rot)
	}
	if n := len(all) - len(frames); n > 0 {
		desc += fmt.Sprintf(" (%d unsolved frames left out)", n)
	}

	return ph, desc, spread >= 1
}

// worst returns the highest weight rms/median across the sample points.
func (ph phases) worst(lattice, k int, p float64) float64 {
	var w float64
	for _, pts := range ph.points {
		w = max(w, coverage(pts, lattice, k, p))
	}

	return w
}

// coverage simulates the drizzle weight image over one lattice period: each
// frame rains a square drop of side p (input pixels) centred on its phase,
// repeated every `lattice` input pixels, onto output pixels 1/k input pixels
// wide. Phases are FITS coordinates, where pixel centres are integers, so
// output pixel edges lie at −½ + i/k; shifting phases by +½ puts them at
// i/k. Without it Scale 3 and CFA Scale 1 judge coverage half a pixel off:
// a whole-pixel dither, which leaves ¾ of a CFA grid empty, looks perfect.
// It returns the weights' rms divided by their median, the DrizzlePac
// statistic; it is +Inf when most output pixels get nothing.
//
// ponytail: drops stay axis-aligned even when frames are rotated; rotation
// only smears coverage further, so this errs on the cautious side.
func coverage(pts [][2]float64, lattice, k int, p float64) float64 {
	n := lattice * k
	period := float64(lattice)
	wx, wy := make([]float64, n), make([]float64, n)
	weights := make([]float64, n*n)
	for _, pt := range pts {
		overlap(wx, math.Mod(math.Mod(pt[0]+0.5, period)+period, period), p, period, k)
		overlap(wy, math.Mod(math.Mod(pt[1]+0.5, period)+period, period), p, period, k)
		for y := range n {
			for x := range n {
				weights[y*n+x] += wx[x] * wy[y]
			}
		}
	}
	var sum, sq float64
	for _, v := range weights {
		sum += v
		sq += v * v
	}
	mean := sum / float64(len(weights))
	med := stats.MedianOf(weights)
	if med == 0 {
		return math.Inf(1)
	}

	return math.Sqrt(max(0, sq/float64(len(weights))-mean*mean)) / med
}

// overlap fills w[i] with the length of output pixel i (spanning
// [i/k, (i+1)/k) input pixels) covered by drops of width p centred at c and
// repeated every period input pixels.
func overlap(w []float64, c, p, period float64, k int) {
	for i := range w {
		a0, a1 := float64(i)/float64(k), float64(i+1)/float64(k)
		w[i] = 0
		for m := -1.0; m <= 1; m++ {
			lo, hi := c+m*period-p/2, c+m*period+p/2
			w[i] += max(0, min(a1, hi)-max(a0, lo))
		}
	}
}
