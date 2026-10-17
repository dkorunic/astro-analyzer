// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package stats

import (
	"math"
	"slices"
)

// b3 is the B3-spline kernel of the à trous wavelet transform.
var b3 = [5]float32{1.0 / 16, 4.0 / 16, 6.0 / 16, 4.0 / 16, 1.0 / 16}

// layerNoise is the standard deviation of à trous layers 1..4 for unit
// Gaussian white noise (Starck & Murtagh), to PixInsight's four digits
// (s_5x5B3Spline_kj, SubframeSelectorInstance.cpp:70); TestLayerNoise
// re-derives it.
var layerNoise = [4]float64{0.8907, 0.2007, 0.0856, 0.0413}

// NoiseMRS estimates Gaussian pixel noise with the multiresolution support
// method (Starck & Murtagh 1998), as PixInsight does: only pixels that are
// not significant (> 3σ) in any of the first four wavelet layers count, so
// stars, nebulosity and gradients do not inflate it. It also returns the
// smooth large-scale residual layer, which serves as a local background map.
// NaN (blank) pixels are taken as sky for the transform and left out of the
// noise estimate: flat filler would otherwise pass as noiseless sky.
//
// As SubframeSelector does (SubframeSelectorInstance.cpp:867-891), when
// fewer than 1% of the pixels count as noise, or the clipping does not
// converge, it retries with three and then two layers, and finally falls
// back to a k-sigma estimate on the first layer. The background map always
// comes from four layers.
func NoiseMRS(pix []float32, w, h int, sky, sigma0 float64) (float64, []float32) {
	filled := FillNaN(pix, sky)
	var background []float32
	for layers := len(layerNoise); layers >= 2; layers-- {
		sigma, n, cJ := mrs(filled, pix, w, h, layers, sigma0)
		if background == nil {
			background = cJ
		}
		if sigma > 0 && n >= len(pix)/100 {
			return sigma, background
		}
	}

	return kSigmaNoise(filled, w, h), background
}

// mrs is one multiresolution support estimate over the given number of
// layers: the noise, the size of the noise set and the residual layer cJ.
// The noise is 0 when the clipping does not converge. PixInsight divides
// the result by 0.974 to correct a 2% low bias (ATrousWaveletTransform.cpp,
// NoiseEstimate); on pure Gaussian noise this one returns 0.979σ without it.
//
// Rather than keep the layers, each pixel keeps the clipping level kσ below
// which some layer is significant (see threshold), so the clipping passes
// read 12 B/px instead of 24 and test once per pixel.
func mrs(c, pix []float32, w, h, layers int, sigma0 float64) (float64, int, []float32) {
	const k = 3.0
	n := len(pix)
	sig, tmp := make([]float64, n), make([]float32, n)
	// Two scale buffers alternate; neither aliases pix, which may be c.
	bufs := [2][]float32{make([]float32, n), make([]float32, n)}
	for j, sj := range layerNoise[:layers] {
		next := bufs[j%2]
		smooth(next, tmp, c, w, h, 1<<j)
		for i := range c {
			sig[i] = max(sig[i], threshold(math.Abs(float64(c[i]-next[i])), sj))
		}
		c = next
	}
	// Residual against the background map; NaN marks blank pixels.
	d := tmp
	for i, v := range pix {
		d[i] = v - c[i]
	}

	// PixInsight's loop: converge to 1e-4 within 18 passes, or fail.
	sigma := sigma0
	for range 18 {
		var sum, sq float64
		var m int
		lim := k * sigma
		for i, v := range d {
			if v != v || lim < sig[i] {
				continue
			}
			x := float64(v)
			sum, sq, m = sum+x, sq+x*x, m+1
		}
		if m < 2 {
			return 0, 0, c
		}
		mean := sum / float64(m)
		next := math.Sqrt(max(0, sq/float64(m)-mean*mean))
		done := math.Abs(next-sigma) < 1e-4*next
		sigma = next
		if done {
			return sigma / 0.974, m, c
		}
	}

	return 0, 0, c
}

// kSigmaNoise is PixInsight's last resort (NoiseKSigma(0, 3, 0.01, 10)):
// the 3σ-clipped (about zero) standard deviation of the first wavelet
// layer, scaled by that layer's noise.
func kSigmaNoise(c []float32, w, h int) float64 {
	next, tmp := make([]float32, len(c)), make([]float32, len(c))
	smooth(next, tmp, c, w, h, 1)
	a := tmp[:0]
	for i, v := range c {
		a = append(a, v-next[i]) // overwrites tmp only behind the reader
	}
	var prev float64
	for it := 1; ; it++ {
		if len(a) < 2 {
			return 0
		}
		var sum, sq float64
		for _, x := range a {
			sum, sq = sum+float64(x), sq+float64(x)*float64(x)
		}
		mean := sum / float64(len(a))
		s := math.Sqrt(max(0, sq/float64(len(a))-mean*mean))
		if s == 0 || it == 10 || it > 1 && (prev-s)/prev < 0.01 {
			return s / layerNoise[0]
		}
		prev = s
		lim := float32(3 * s)
		a = slices.DeleteFunc(a, func(x float32) bool { return x <= -lim || x >= lim })
	}
}

// threshold returns the least x with fl(x·s) ≥ a, for a ≥ 0 and s > 0.
// Rounding is monotone, so for x ≥ 0 the significance test a > fl(x·s) holds
// exactly when x < threshold(a, s): the per-layer test |wⱼ| > kσ·σⱼ, bit for
// bit, folded into one comparison per pixel. NaN gives 0: a NaN layer is
// never significant, and it keeps NaN out of the caller's max.
//
// It runs 8M times per frame on random data, so it avoids data-dependent
// branches. r = fl(a/s) is within half an ulp of a/s, so r's upper neighbour
// always qualifies; the answer is r−1ulp, r or r+1ulp, picked by adjusting
// r's bits. Only if r−2ulp qualifies too, which needs a much larger rounding
// error, does it fall back to stepping down.
func threshold(a, s float64) float64 {
	if a != a {
		return 0
	}
	// 0 and +Inf need no case: comparisons with 0's wrapped neighbours
	// (NaNs) and with Inf·s are false, so both return themselves.
	x := a / s
	b := math.Float64bits(x)
	if math.Float64frombits(b-2)*s >= a {
		return thresholdBelow(a, s, math.Float64frombits(b-2))
	}
	d := uint64(0)
	if x*s < a {
		d = 1
	}
	if math.Float64frombits(b-1)*s >= a {
		d = ^uint64(0) // −1 ulp; then r qualifies too, as rounding is monotone
	}

	return math.Float64frombits(b + d)
}

// thresholdBelow is threshold's rare case, kept out of line: x qualifies,
// so step down until its lower neighbour does not.
//
//go:noinline
func thresholdBelow(a, s, x float64) float64 {
	for {
		p := math.Float64frombits(math.Float64bits(x) - 1)
		if p*s < a {
			return x
		}
		x = p
	}
}

// smooth writes in convolved with the B3 kernel dilated by step ("holes") to
// out, separably, mirroring at the edges; tmp is scratch. Taps are summed in
// kernel order everywhere, so the fast interior path rounds as the edges do.
func smooth(out, tmp, in []float32, w, h, step int) {
	// Columns at least 2·step from either edge need no mirroring.
	lo, hi := min(2*step, w), max(min(2*step, w), w-2*step)
	for y := range h {
		row, dst := in[y*w:(y+1)*w], tmp[y*w:(y+1)*w]
		for x := range lo {
			dst[x] = tapsMirrored(row, x, step)
		}
		// Unrolled over five shifted views of the row: Go does not unroll
		// loops, and the indexed taps cost a bounds check each.
		if lo < hi {
			r0, r1, r2 := row[lo-2*step:hi-2*step], row[lo-step:hi-step], row[lo:hi]
			r3, r4 := row[lo+step:hi+step], row[lo+2*step:hi+2*step]
			taps(dst[lo:hi], r0, r1, r2, r3, r4)
		}
		for x := hi; x < w; x++ {
			dst[x] = tapsMirrored(row, x, step)
		}
	}
	// Vertically the mirrored source rows are the same for the whole row.
	for y := range h {
		var src [len(b3)][]float32
		for k := range b3 {
			r := mirror(y+(k-2)*step, h) * w
			src[k] = tmp[r : r+w]
		}
		taps(out[y*w:(y+1)*w], src[0], src[1], src[2], src[3], src[4])
	}
}

// taps sets dst[i] to the B3 kernel over s0[i]..s4[i], summed in kernel
// order like tapsMirrored. Unrolled over fixed views it cut BenchmarkPipeline
// by 9%; reslicing to len(dst) lets the compiler drop the bounds checks.
func taps(dst, s0, s1, s2, s3, s4 []float32) {
	s0, s1, s2, s3, s4 = s0[:len(dst)], s1[:len(dst)], s2[:len(dst)], s3[:len(dst)], s4[:len(dst)]
	for i := range dst {
		var s float32
		s += b3[0] * s0[i]
		s += b3[1] * s1[i]
		s += b3[2] * s2[i]
		s += b3[3] * s3[i]
		s += b3[4] * s4[i]
		dst[i] = s
	}
}

// tapsMirrored is one horizontal kernel sum at x with mirrored indices.
func tapsMirrored(row []float32, x, step int) float32 {
	var s float32
	for k, c := range b3 {
		s += c * row[mirror(x+(k-2)*step, len(row))]
	}

	return s
}

// mirror reflects index i into [0, n).
func mirror(i, n int) int {
	if n == 1 {
		return 0
	}
	for i < 0 || i >= n {
		if i < 0 {
			i = -i
		}
		if i >= n {
			i = 2*(n-1) - i
		}
	}

	return i
}
