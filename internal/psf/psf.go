// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

// Package psf fits elliptical star profiles (Gaussian or Moffat) to measure
// FWHM, eccentricity and orientation, following PixInsight's conventions.
package psf

import (
	"math"
	"slices"

	"github.com/dkorunic/astro-analyzer/internal/fits"
)

// PSF models share one elliptical shape q = a·dx² + 2b·dx·dy + c·dy²:
//
//	gaussian: B + A·exp(-q/2)
//	moffat4:  B + A·(1+q)^-4   (PixInsight's default; wings broader than a Gaussian)
//	moffat:   B + A·(1+q)^-β   (β fitted too; ~8x slower)
//
// Parameters: B, A, x0, y0, a, b, c[, β].
const (
	Gaussian = "gaussian"
	Moffat4  = "moffat4"
	Moffat   = "moffat"
)

// maxResid is the FWHMEccentricity script's fit-quality cut (psf_mad < 0.1,
// /Applications/PixInsight/src/scripts/mschuster/FWHMEccentritity/FWHMEccentricity.js:454).
// The residual is in units of the full sample range, so this drops only
// gross failures; SubframeSelector applies no cut at all.
const maxResid = 0.1

// sub are the sub-pixel offsets used to integrate the model over each pixel
// area, as a sensor does; without it undersampled stars fit too wide.
var sub = [3]float64{-1.0 / 3, 0, 1.0 / 3}

// Star is one star's fitted shape: FWHM in analysed pixels, eccentricity,
// major-axis angle in radians [0, π) counter-clockwise from +x, and the
// fit residual as a fraction of the full sample range. Signal is the
// star's light above the fitted background, in sample units (see signal),
// and Mean that signal per pixel of the profile's tenth-maximum ellipse.
type Star struct {
	FWHM, Ecc, Theta, Resid, Signal, Mean float64
}

// Valid reports whether model names a supported PSF model.
func Valid(model string) bool {
	return model == Gaussian || model == Moffat4 || model == Moffat
}

// Fit fits the chosen model to the (2r+1)² stamp around pixel c with
// Levenberg–Marquardt and returns the star's shape. FWHM is the geometric
// mean of both axes, as in PixInsight's FWHMEccentricity script. fwhm0 is the
// half-maximum estimate that seeds the fit and bounds its result.
func Fit(img fits.Image, c, r int, sky, fwhm0 float64, model string) (Star, bool) {
	f, ok := newFitter(img, c, r, model)
	if !ok {
		return Star{}, false
	}
	p := seed(model, sky, float64(img.Pix[c])-sky, fwhm0)
	if !f.minimise(p) {
		return Star{}, false
	}
	s, ok := shape(p, model)
	// The half-max seed measures the same geometric-mean width (≤~20% high
	// when undersampled); a fit far from it has run off into a broad, flat
	// profile the stamp cannot constrain.
	if !ok || s.FWHM < fwhm0/2 || s.FWHM > 2*fwhm0 {
		return Star{}, false
	}
	s.Resid = f.residual(p) / img.Range
	var area float64
	s.Signal, area = f.signal(p)
	s.Mean = s.Signal / area

	return s, s.Resid < maxResid
}

// fitter holds one star's stamp and scratch space for the model gradient.
type fitter struct {
	model string
	stamp []px
	tmp   []float64
}

type px struct{ x, y, v float64 }

func newFitter(img fits.Image, c, r int, model string) (*fitter, bool) {
	cx, cy := c%img.W, c/img.W
	if cx-r < 0 || cy-r < 0 || cx+r >= img.W || cy+r >= img.H {
		return nil, false
	}
	stamp := make([]px, 0, (2*r+1)*(2*r+1))
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			stamp = append(stamp, px{float64(x), float64(y), float64(img.Pix[(cy+y)*img.W+cx+x])})
		}
	}

	return &fitter{model: model, stamp: stamp}, true
}

// seed returns starting parameters: centred on the peak pixel, round, with
// the width implied by the half-maximum estimate fwhm0.
func seed(model string, sky, amp, fwhm0 float64) []float64 {
	switch model {
	case Moffat, Moffat4:
		beta := 4.0
		if model == Moffat {
			beta = 3 // typical for seeing-limited stars
		}
		alpha := fwhm0 / (2 * math.Sqrt(math.Pow(2, 1/beta)-1))
		p := []float64{sky, amp, 0, 0, 1 / (alpha * alpha), 0, 1 / (alpha * alpha)}
		if model == Moffat {
			p = append(p, beta)
		}

		return p
	default:
		s := fwhm0 / (2 * math.Sqrt(2*math.Ln2))

		return []float64{sky, amp, 0, 0, 1 / (s * s), 0, 1 / (s * s)}
	}
}

// pixel returns the model integrated over the pixel at (x, y) and, unless g
// is nil, fills g with its gradient.
func (f *fitter) pixel(p []float64, x, y float64, g []float64) float64 {
	clear(g)
	var v float64
	for _, oy := range sub {
		for _, ox := range sub {
			if g == nil {
				v += profile(p, f.model, x+ox, y+oy, nil)

				continue
			}
			v += profile(p, f.model, x+ox, y+oy, f.tmp)
			for i := range g {
				g[i] += f.tmp[i]
			}
		}
	}
	k := float64(len(sub) * len(sub))
	for i := range g {
		g[i] /= k
	}

	return v / k
}

func (f *fitter) chi2(p []float64) float64 {
	var s float64
	for _, q := range f.stamp {
		d := q.v - f.pixel(p, q.x, q.y, nil)
		s += d * d
	}

	return s
}

func (f *fitter) valid(p []float64) bool {
	ok := p[1] > 0 && p[4] > 0 && p[6] > 0 && p[4]*p[6] > p[5]*p[5] &&
		math.Abs(p[2]) < 2 && math.Abs(p[3]) < 2
	if f.model == Moffat {
		ok = ok && p[7] > 0.5 && p[7] < 50 // β→∞ is a Gaussian
	}

	return ok
}

// minimise runs Levenberg–Marquardt on p in place and reports whether the
// result is a physically valid profile.
func (f *fitter) minimise(p []float64) bool {
	n := len(p)
	f.tmp = make([]float64, n)
	g := make([]float64, n)
	jtj, m := matrix(n), matrix(n)
	jtr, next := make([]float64, n), make([]float64, n)
	cur, lambda := f.chi2(p), 1e-3
	for range 100 {
		for i := range jtj {
			clear(jtj[i])
		}
		clear(jtr)
		for _, q := range f.stamp {
			d := q.v - f.pixel(p, q.x, q.y, g)
			for i := range n {
				jtr[i] += g[i] * d
				for j := range n {
					jtj[i][j] += g[i] * g[j]
				}
			}
		}
		improved := false
		for lambda < 1e10 {
			for i := range n {
				copy(m[i], jtj[i])
				m[i][i] *= 1 + lambda
			}
			step, solved := solve(m, append([]float64(nil), jtr...))
			for i := range n {
				next[i] = p[i] + step[i]
			}
			if solved && f.valid(next) {
				if s := f.chi2(next); s < cur {
					improved = cur-s > 1e-6*cur // FWHM is settled long before χ² stops creeping
					copy(p, next)
					cur, lambda = s, lambda/10

					break
				}
			}
			lambda *= 10
		}
		if !improved {
			break
		}
	}

	return f.valid(p)
}

// residual is PixInsight's PSFData.mad (PCL PSFFit.cpp, PSFWinsorizedMean):
// the mean |data − model| over the stamp with 10% of each tail Winsorized,
// in sample units; Fit divides it by the full range.
func (f *fitter) residual(p []float64) float64 {
	d := make([]float64, len(f.stamp))
	for i, q := range f.stamp {
		d[i] = math.Abs(q.v - f.pixel(p, q.x, q.y, nil))
	}
	n := len(d)
	if m := n / 10; m > 0 && n-m-1 > m {
		slices.Sort(d)
		for i := range m {
			d[i], d[n-1-i] = d[m], d[n-1-m]
		}
	}
	var s float64
	for _, x := range d {
		s += x
	}

	return s / float64(n)
}

// signal is PixInsight's PSFData.signal (PCL PSFFit.cpp, GoodnessOfFit):
// the sum of data above the fitted background inside the ellipse where the
// profile falls to a tenth of its peak, each radius widened by one pixel.
// PixInsight keeps that ellipse axis-aligned; this one follows the fitted
// rotation, which is the same for round stars.
// It also returns the area of the tenth-maximum ellipse without that
// widening, which PSFSignalEstimator divides the signal by for the star's
// mean flux (PSFSignalEstimator.cpp:363-367: π·(FWTM/2)²·sx·sy).
// ponytail: summed over the stamp only, which can be narrower than the
// ellipse for small stars (stamp radius ≥ 3, ellipse ~FWHM+1).
func (f *fitter) signal(p []float64) (float64, float64) {
	half, root := (p[4]+p[6])/2, math.Hypot((p[4]-p[6])/2, p[5])
	// Radius at a tenth of the peak, in units of the profile's scale.
	r10 := math.Sqrt(2 * math.Ln10)
	switch f.model {
	case Moffat4:
		r10 = math.Sqrt(math.Pow(10, 0.25) - 1)
	case Moffat:
		r10 = math.Sqrt(math.Pow(10, 1/p[7]) - 1)
	}
	rMin, rMaj := r10/math.Sqrt(half+root)+1, r10/math.Sqrt(half-root)+1
	phi := 0.5 * math.Atan2(2*p[5], p[4]-p[6]) // along the minor axis
	cos, sin := math.Cos(phi), math.Sin(phi)
	var s float64
	for _, q := range f.stamp {
		dx, dy := q.x-p[2], q.y-p[3]
		u, v := dx*cos+dy*sin, dy*cos-dx*sin
		if d := q.v - p[0]; d > 0 && u*u/(rMin*rMin)+v*v/(rMaj*rMaj) <= 1 {
			s += d
		}
	}
	// The semi-axes are r10/√λ; their product r10²/√(λmin·λmax) = r10²/√(ac−b²).
	return s, math.Pi * r10 * r10 / math.Sqrt(p[4]*p[6]-p[5]*p[5])
}

// shape turns fitted parameters into FWHM, eccentricity and orientation.
func shape(p []float64, model string) (Star, bool) {
	// Eigenvalues of [[a b] [b c]] are 1/scale² along the minor and major axes.
	half, root := (p[4]+p[6])/2, math.Hypot((p[4]-p[6])/2, p[5])
	lMax, lMin := half+root, half-root
	if lMin <= 0 {
		return Star{}, false
	}
	width := 2 * math.Sqrt(2*math.Ln2) // FWHM per unit Gaussian sigma
	switch model {                     // FWHM per unit Moffat alpha
	case Moffat4:
		width = 2 * math.Sqrt(math.Pow(2, 0.25)-1)
	case Moffat:
		width = 2 * math.Sqrt(math.Pow(2, 1/p[7])-1)
	}

	return Star{
		FWHM: width / math.Sqrt(math.Sqrt(lMax*lMin)),
		Ecc:  math.Sqrt(1 - lMin/lMax),
		// ½·atan2(2b, a−c) points along the narrowest axis (largest
		// eigenvalue); the major axis is perpendicular to it.
		Theta: math.Mod(0.5*math.Atan2(2*p[5], p[4]-p[6])+1.5*math.Pi, math.Pi),
	}, true
}

func matrix(n int) [][]float64 {
	m := make([][]float64, n)
	for i := range m {
		m[i] = make([]float64, n)
	}

	return m
}

// profile evaluates the model at (x, y) and, unless g is nil, writes its
// gradient into g.
func profile(p []float64, model string, x, y float64, g []float64) float64 {
	dx, dy := x-p[2], y-p[3]
	a, b, c := p[4], p[5], p[6]
	q := a*dx*dx + 2*b*dx*dy + c*dy*dy

	var f, dfdq float64 // dfdq: derivative of the star term with respect to q
	switch model {
	case Moffat4:
		u := 1 + q
		u2 := u * u
		pw := 1 / (u2 * u2) // no transcendental calls: as cheap as the Gaussian
		f = p[1] * pw
		if g == nil {
			return p[0] + f
		}
		dfdq = -4 * f / u
		g[1] = pw
	case Moffat:
		lnu := math.Log1p(q) // shared by value and β-gradient; cheaper than math.Pow
		pw := math.Exp(-p[7] * lnu)
		f = p[1] * pw
		if g == nil {
			return p[0] + f
		}
		dfdq = -p[7] * f / (1 + q)
		g[1], g[7] = pw, -f*lnu
	default:
		e := math.Exp(-q / 2)
		f = p[1] * e
		if g == nil {
			return p[0] + f
		}
		dfdq = -f / 2
		g[1] = e
	}
	g[0] = 1
	g[2] = dfdq * -2 * (a*dx + b*dy)
	g[3] = dfdq * -2 * (b*dx + c*dy)
	g[4] = dfdq * dx * dx
	g[5] = dfdq * 2 * dx * dy
	g[6] = dfdq * dy * dy

	return p[0] + f
}

// solve solves m·x = b in place by Gaussian elimination with partial pivoting.
func solve(m [][]float64, b []float64) ([]float64, bool) {
	n := len(b)
	for col := range n {
		piv := col
		for r := col + 1; r < n; r++ {
			if math.Abs(m[r][col]) > math.Abs(m[piv][col]) {
				piv = r
			}
		}
		if m[piv][col] == 0 {
			return b, false
		}
		m[col], m[piv] = m[piv], m[col]
		b[col], b[piv] = b[piv], b[col]
		for r := col + 1; r < n; r++ {
			f := m[r][col] / m[col][col]
			for k := col; k < n; k++ {
				m[r][k] -= f * m[col][k]
			}
			b[r] -= f * b[col]
		}
	}
	x := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		s := b[r]
		for k := r + 1; k < n; k++ {
			s -= m[r][k] * x[k]
		}
		x[r] = s / m[r][r]
	}

	return x, true
}
