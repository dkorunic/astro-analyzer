// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package stats

import (
	"math"
	"slices"
)

// Chauvenet sorts v and returns the range v[i:j] that survives PixInsight's
// robust Chauvenet rejection (include/pcl/RobustChauvenetRejection.h) in
// bulk mode, SetLargeSampleSize(1), as PSFSignalEstimator runs it. Three
// phases trade robustness for precision: median with a line-fit deviation,
// median with a sample deviation, then mean and standard deviation. Each
// pass drops, from both ends, every value whose expected count n·Q(z) is
// below ½, until a pass drops none.
func Chauvenet(v []float64) (int, int) {
	slices.Sort(v)
	i, j := 0, len(v)
	for phase := range 3 {
		for {
			var mean, sigma float64
			switch phase {
			case 0:
				mean = Median(v[i:j])
				sigma = lineFitDeviation(v[i:j], mean)
			case 1:
				mean = Median(v[i:j])
				sigma = sampleDeviation(v[i:j], mean)
			default:
				mean, sigma = meanStd(v[i:j])
			}
			n := j - i
			if 1+sigma == 1 || n < 3 {
				return i, j
			}
			// With a large sample size of 1, each side may drop up to n values
			// a pass: all that fail the criterion.
			c := 0
			for it := 0; it < n && i < j && float64(n)*qf((mean-v[i])/sigma) < 0.5; it++ {
				i, c = i+1, c+1
			}
			for it := 0; it < n && i < j && float64(n)*qf((v[j-1]-mean)/sigma) < 0.5; it++ {
				j, c = j-1, c+1
			}
			if c == 0 {
				break
			}
		}
	}

	return i, j
}

// qf is the upper tail probability of the standard normal distribution.
func qf(x float64) float64 { return 0.5 * (1 - math.Erf(x/math.Sqrt2)) }

// fn corrects the deviation estimators' small-sample bias.
func fn(n int) float64 { return 1 / (1 - 2.9442*math.Pow(float64(n), -1.073)) }

// sampleDeviation is the 68.3rd percentile of |v − m|, bias corrected.
func sampleDeviation(v []float64, m float64) float64 {
	n := len(v)
	if n < 2 {
		return 0
	}
	d := make([]float64, n)
	for k, x := range v {
		d[k] = math.Abs(x - m)
	}

	return fn(n) * Select(d, int(0.683*float64(n)))
}

// lineFitDeviation fits the lowest 68.3% of |v − m| against their expected
// half-normal quantiles and reads sigma off the line at one.
func lineFitDeviation(v []float64, m float64) float64 {
	nn := len(v)
	n := int(0.683*float64(nn) + 0.317)
	if n < 8 {
		return sampleDeviation(v, m)
	}
	y := make([]float64, nn)
	for k, x := range v {
		y[k] = math.Abs(x - m)
	}
	slices.Sort(y)
	y = y[:n]
	x := make([]float64, n)
	for k := range x {
		x[k] = math.Sqrt2 * math.Erfinv((float64(k)+1-0.317)/float64(nn))
	}
	a, b := medfit(x, y)
	if math.IsInf(b, 0) || math.IsNaN(b) {
		return sampleDeviation(v, m)
	}

	return fn(nn) * (a + b)
}

// meanStd returns the mean and the sample (n−1) standard deviation.
func meanStd(v []float64) (float64, float64) {
	var mean float64
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))
	if len(v) < 2 {
		return mean, 0
	}
	var sq float64
	for _, x := range v {
		sq += (x - mean) * (x - mean)
	}

	return mean, math.Sqrt(sq / float64(len(v)-1))
}

// medfit is PCL's LinearFit (src/pcl/LinearFitReference.cpp), Numerical
// Recipes' medfit: the line y = a + b·x minimising Σ|y − a − b·x|, by
// bisection on the slope from the least squares solution. len(x) ≥ 3.
func medfit(x, y []float64) (float64, float64) {
	n := float64(len(x))
	var sx, sy, sxy, sxx float64
	for k := range x {
		sx, sy = sx+x[k], sy+y[k]
		sxy, sxx = sxy+x[k]*y[k], sxx+x[k]*x[k]
	}
	del := n*sxx - sx*sx
	a := (sxx*sy - sx*sxy) / del
	b := (n*sxy - sx*sy) / del
	var chisq float64
	for k := range x {
		t := y[k] - (a + b*x[k])
		chisq += t * t
	}
	sigb := math.Sqrt(chisq / del)
	if !(sigb > 0) {
		return a, b
	}
	f := make([]float64, len(x))
	// rofunc is the derivative of the absolute deviation in b at the best
	// intercept for b, which it leaves in a, as PCL does.
	rofunc := func(b float64) float64 {
		for k := range x {
			f[k] = y[k] - b*x[k]
		}
		slices.Sort(f)
		a = Median(f)
		var sum float64
		for k := range x {
			d := y[k] - (b*x[k] + a)
			if y[k] != 0 {
				d /= math.Abs(y[k])
			}
			if math.Abs(d) > 2.5e-16 {
				if d >= 0 {
					sum += x[k]
				} else {
					sum -= x[k]
				}
			}
		}

		return sum
	}
	f1 := rofunc(b)
	b2 := b + 3*sigb
	if f1 < 0 {
		b2 = b - 3*sigb
	}
	if b2 == b {
		return a, b
	}
	f2 := rofunc(b2)
	b1 := b
	for f1*f2 > 0 {
		b = b2 + 1.6*(b2-b1)
		b1, f1, b2 = b2, f2, b
		f2 = rofunc(b2)
	}
	sigb *= 0.01
	for math.Abs(b2-b1) > sigb {
		b = b1 + (b2-b1)/2
		if b == b1 || b == b2 {
			break
		}
		if v := rofunc(b); v*f1 >= 0 {
			f1, b1 = v, b
		} else {
			b2 = b
		}
	}

	return a, b
}
