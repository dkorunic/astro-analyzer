// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package stats

import (
	"math"
	"slices"
)

// sampleStep is an odd stride that leaves about a million samples, plenty
// for a median; odd, so on even-width sensors it walks across columns.
func sampleStep(n int) int { return max(1, n/1_000_000) | 1 }

// MStar is PixInsight's M* (src/pcl/PSFSignalEstimator.cpp, the residual
// thread and EstimateSignal): the median of m − v over the nonzero pixels v
// below the large-scale background m. It measures how far the sky dips
// below its background, a noise-like scale, not the background level, so
// pedestals do not change it. block is the background scale in analysed
// pixels; PixInsight's is 256 image pixels. NaN when nothing is below.
//
// ponytail: m is a bilinear map of block medians, where PixInsight uses a
// six-layer multiscale median transform. Both follow structure larger than
// the block and ignore stars; they part only on nebulae near block size.
func MStar(pix []float32, w, h, block int) float64 {
	nx, ny := max(1, (w+block/2)/block), max(1, (h+block/2)/block)
	grid := make([]float64, nx*ny)
	var buf []float64
	for j := range ny {
		for i := range nx {
			buf = buf[:0]
			for y := j * h / ny; y < (j+1)*h/ny; y++ {
				for _, v := range pix[y*w+i*w/nx : y*w+(i+1)*w/nx] {
					if v == v && v != 0 {
						buf = append(buf, float64(v))
					}
				}
			}
			grid[j*nx+i] = math.NaN() // blank: pixels near it are skipped
			if len(buf) > 0 {
				grid[j*nx+i] = SelectMedian(buf)
			}
		}
	}
	// at interpolates the grid between cell centres and extrapolates the
	// outermost two beyond them, so a gradient reaches the frame edges.
	at := func(u float64, n int) (int, int, float64) {
		if n == 1 {
			return 0, 0, 0
		}
		u -= 0.5
		k := min(max(int(math.Floor(u)), 0), n-2)

		return k, k + 1, u - float64(k)
	}
	step := sampleStep(len(pix))
	r := make([]float64, 0, len(pix)/step/2+1)
	for p := 0; p < len(pix); p += step {
		v := float64(pix[p])
		if v != v || v == 0 {
			continue
		}
		x0, x1, fx := at((float64(p%w)+0.5)*float64(nx)/float64(w), nx)
		y0, y1, fy := at((float64(p/w)+0.5)*float64(ny)/float64(h), ny)
		m := (1-fy)*((1-fx)*grid[y0*nx+x0]+fx*grid[y0*nx+x1]) + fy*((1-fx)*grid[y1*nx+x0]+fx*grid[y1*nx+x1])
		if v < m { // false for NaN
			r = append(r, m-v)
		}
	}
	if len(r) == 0 {
		return math.NaN()
	}

	return SelectMedian(r)
}

// SelectMedian is Median without sorting: it reorders v, which must be
// non-empty and hold no NaNs.
func SelectMedian(v []float64) float64 {
	k := len(v) / 2
	m := Select(v, k)
	if len(v)%2 == 0 {
		m = (m + slices.Max(v[:k])) / 2 // Select leaves the k smallest before k
	}

	return m
}

// TwoSidedScale is SubframeSelector's background scale for its SNR weight
// (SubframeSelectorInstance.cpp:676-706):
// the standard deviation of the pixels below the median and of those above
// it, each clipped twice at 4σ from the median, averaged. Samples at or
// beyond lo and hi (2 DN from either end of the range) are ignored, as are
// NaN (blank) pixels.
//
// ponytail: the median comes from about a million samples, not every pixel.
func TwoSidedScale(pix []float32, lo, hi float64) float64 {
	step := sampleStep(len(pix))
	s := make([]float64, 0, len(pix)/step+1)
	for p := 0; p < len(pix); p += step {
		if v := float64(pix[p]); v > lo && v < hi {
			s = append(s, v)
		}
	}
	if len(s) == 0 {
		return math.NaN()
	}
	c := SelectMedian(s)
	// Three passes, as PixInsight sets the range clipping three times; each
	// range is built from the deviation the previous pass measured.
	loA, hiB := lo, hi
	var sdLo, sdHi float64
	for range 3 {
		var nl, nh, sl, sh, ql, qh float64
		for _, v32 := range pix {
			// Half the pixels sit on each side at random, so a branch
			// mispredicts; selects compile to FCSEL instead. Adding an exact
			// 0 leaves these sums (never −0) bit for bit the same.
			v := float64(v32)
			var l, h, bl, bh float64
			if v > loA && v < c {
				l, bl = v, 1
			}
			if v > c && v < hiB {
				h, bh = v, 1
			}
			nl, sl, ql = nl+bl, sl+l, ql+l*l
			nh, sh, qh = nh+bh, sh+h, qh+h*h
		}
		sdLo, sdHi = sampleSD(nl, sl, ql), sampleSD(nh, sh, qh)
		loA, hiB = max(lo, c-4*sdLo), min(hi, c+4*sdHi)
	}
	// TwoSidedEstimate's double conversion: the mean, or the one side known.
	switch {
	case sdLo == 0:
		return sdHi
	case sdHi == 0:
		return sdLo
	}

	return (sdLo + sdHi) / 2
}

// sampleSD is the n−1 standard deviation from count, sum and sum of squares.
func sampleSD(n, s, q float64) float64 {
	if n < 2 {
		return 0
	}

	return math.Sqrt(max(0, (q-s*s/n)/(n-1)))
}
