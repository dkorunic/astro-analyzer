// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

// Package stats holds the robust statistics shared by frame analysis and
// session reporting.
package stats

import (
	"cmp"
	"math"
	"slices"
	"sort"
)

// Median returns the median of sorted, non-empty s.
func Median(s []float64) float64 {
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}

	return (s[n/2-1] + s[n/2]) / 2
}

// MedianOf sorts v in place and returns its median, or NaN when empty.
func MedianOf(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	slices.Sort(v)

	return Median(v)
}

// Select reorders s and returns its k-th smallest element, the value
// slices.Sort would leave at s[k], in O(n) instead of O(n log n). s must hold
// no NaNs. Taking each aperture's sky median this way instead of sorting
// cut BenchmarkPipeline by 11%.
func Select(s []float64, k int) float64 {
	lo, hi := 0, len(s)-1
	for lo < hi {
		// Hoare partition around the middle element; sorted and constant
		// annuli (flat sky) stay O(n).
		p := s[lo+(hi-lo)/2]
		i, j := lo, hi
		for i <= j {
			for s[i] < p {
				i++
			}
			for s[j] > p {
				j--
			}
			if i <= j {
				s[i], s[j] = s[j], s[i]
				i, j = i+1, j-1
			}
		}
		switch {
		case k <= j:
			hi = j
		case k >= i:
			lo = i
		default:
			return s[k]
		}
	}

	return s[k]
}

// Robust returns the median and the MAD scaled to a Gaussian sigma,
// ignoring NaNs. A few bad values cannot inflate it the way stddev would.
func Robust(v []float64) (float64, float64, int) {
	s := slices.DeleteFunc(slices.Clone(v), math.IsNaN)
	if len(s) == 0 {
		return math.NaN(), math.NaN(), 0
	}
	slices.Sort(s)
	med := Median(s)
	for i, x := range s {
		s[i] = math.Abs(x - med)
	}
	slices.Sort(s)

	return med, 1.4826 * Median(s), len(s)
}

// Spearman is the rank correlation of x and y over the pairs where both are
// known, and the number of such pairs; NaN with fewer than 3. Ranks make a
// few extreme frames (cloud, wind) count no more than any other.
func Spearman(x, y []float64) (float64, int) {
	var a, b []float64
	for i := range x {
		if !math.IsNaN(x[i]) && !math.IsNaN(y[i]) {
			a, b = append(a, x[i]), append(b, y[i])
		}
	}
	if len(a) < 3 {
		return math.NaN(), len(a)
	}
	ra, rb := ranks(a), ranks(b)
	m := float64(len(a)+1) / 2
	var sab, saa, sbb float64
	for i := range ra {
		da, db := ra[i]-m, rb[i]-m
		sab, saa, sbb = sab+da*db, saa+da*da, sbb+db*db
	}

	return sab / math.Sqrt(saa*sbb), len(a) // NaN when either is constant
}

// ranks returns 1-based ranks, ties sharing their mean rank.
func ranks(v []float64) []float64 {
	idx := make([]int, len(v))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(i, j int) int { return cmp.Compare(v[i], v[j]) })
	r := make([]float64, len(v))
	for i := 0; i < len(idx); {
		j := i
		for j < len(idx) && v[idx[j]] == v[idx[i]] {
			j++
		}
		for k := i; k < j; k++ {
			r[idx[k]] = float64(i+j+1) / 2
		}
		i = j
	}

	return r
}

// Outliers marks values that deviate in the bad direction (+1 high, -1 low)
// by more than k robust sigmas and by more than minDev times the median.
func Outliers(v []float64, bad, k, minDev float64) []bool {
	out := make([]bool, len(v))
	med, sigma, n := Robust(v)
	if bad == 0 || n < 3 {
		return out
	}
	lim := max(k*sigma, minDev*math.Abs(med))
	for i, x := range v {
		out[i] = bad*(x-med) > lim // false for NaN
	}

	return out
}

// SkyStats returns the sigma-clipped (3σ) median and standard deviation of
// the non-NaN pixels, or NaN when there are none.
func SkyStats(pix []float32) (float64, float64) {
	// ponytail: subsample ~1M px, plenty for sky stats. The stride is odd so
	// on even-width sensors it walks across columns instead of down a few.
	step := max(1, len(pix)/1_000_000) | 1
	keys := make([]uint32, 0, len(pix)/step+1)
	for i := 0; i < len(pix); i += step {
		if v := pix[i]; v == v { // skip NaN (blank) pixels
			keys = append(keys, sortKey(v))
		}
	}
	if len(keys) == 0 {
		return math.NaN(), math.NaN()
	}
	// The clipped sums below run in sorted order, so the order is part of
	// the result: radix sorting gives slices.Sort's order (up to ±0) and
	// cut BenchmarkPipeline by 20%.
	keys = radixSort(keys, make([]uint32, len(keys)))
	s := make([]float64, len(keys))
	for i, k := range keys {
		s[i] = float64(fromKey(k))
	}
	var med, std float64
	for range 10 {
		med = s[len(s)/2]
		var sum, sq float64
		for _, v := range s {
			sum += v
			sq += v * v
		}
		mean := sum / float64(len(s))
		std = math.Sqrt(max(0, sq/float64(len(s))-mean*mean))
		lo := sort.SearchFloat64s(s, med-3*std)
		hi := sort.SearchFloat64s(s, math.Nextafter(med+3*std, math.Inf(1)))
		if lo == 0 && hi == len(s) || hi <= lo {
			break
		}
		s = s[lo:hi]
	}

	return med, std
}

// sortKey maps a non-NaN float32 to a uint32 that orders like it: negatives
// get all bits flipped, positives only the sign bit.
func sortKey(v float32) uint32 {
	b := math.Float32bits(v)

	return b ^ (-(b >> 31) | 1<<31)
}

// fromKey inverts sortKey.
func fromKey(k uint32) float32 { return math.Float32frombits(k ^ (-(^k >> 31) | 1<<31)) }

// radixSort sorts a ascending, a byte per pass, using buf (len(a)) as
// scratch, and returns whichever of the two holds the result.
func radixSort(a, buf []uint32) []uint32 {
	for shift := 0; shift < 32; shift += 8 {
		var pos [256]int
		for _, k := range a {
			pos[k>>shift&0xff]++
		}
		if pos[a[0]>>shift&0xff] == len(a) {
			continue // every key shares this byte (the exponent, on sky)
		}
		sum := 0
		for d, c := range pos {
			pos[d], sum = sum, sum+c
		}
		for _, k := range a {
			d := k >> shift & 0xff
			buf[pos[d]] = k
			pos[d]++
		}
		a, buf = buf, a
	}

	return a
}

// FillNaN returns pix with NaN (blank) pixels set to v. It copies only when
// there is something to fill.
func FillNaN(pix []float32, v float64) []float32 {
	i := slices.IndexFunc(pix, func(x float32) bool { return x != x })
	if i < 0 {
		return pix
	}
	out := slices.Clone(pix)
	for ; i < len(out); i++ {
		if out[i] != out[i] {
			out[i] = float32(v)
		}
	}

	return out
}
