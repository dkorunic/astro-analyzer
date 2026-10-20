// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package analyze

import (
	"cmp"
	"math"
	"slices"

	"github.com/dkorunic/astro-analyzer/internal/fits"
)

// N.I.N.A.'s star measurement constants (NINA.Image/ImageAnalysis/
// StarDetection.cs:131-139, commit 4742afa): the measurement radius is
// 1.5× the star's radius, and the windowed centroid runs up to 3 times,
// until it moves under 0.01 px.
const (
	hfrScale    = 1.5
	hfrIter     = 3
	hfrConverge = 0.01
	// hfrClip is N.I.N.A.'s cut on star radii: stars outside the mean
	// ± 1.5σ are dropped before HFR is averaged (StarDetection.cs:777).
	hfrClip = 1.5
)

// hfrSample is one lit pixel's distance from the centroid and its flux
// over the sky.
type hfrSample struct{ d, f float64 }

// hfrBuf holds one frame's scratch space, reused from star to star.
type hfrBuf struct {
	box, wx, wy []float64
	samples     []hfrSample
}

// ninaHFR is N.I.N.A.'s half-flux radius of the star at c, in analysed
// pixels (Star.Calculate, StarDetection.cs:177-198): a centroid weighted by
// a Gaussian window of σ = max(radius/2, 1) (SExtractor's windowed
// position), then the curve of growth, interpolated to the radius that
// encloses half the flux within the measurement radius. NaN without flux.
// Each pixel counts at its centre's distance, so undersampled stars read
// low (9% at FWHM 4 px, TestHFR), in N.I.N.A. as here.
//
// Only stars N.I.N.A. would keep are measured (StarDetection.cs:756): the
// mean within radius at least min(10% of the sky, σ) over the sky, and
// more than one pixel per 1000 of the frame's long side 1.5σ over it. On
// fainter stars the flux, clipped at 0, is mostly sky noise spread evenly
// over the measurement circle, and the radius follows that circle, not the
// star. σ is the frame's MRS noise, where N.I.N.A. takes the sigma of the
// box around the blob.
//
// ponytail: radius is the aperture radius (1.5× the half-max FWHM), not
// N.I.N.A.'s Canny-edge blob radius, and sky is the aperture annulus
// median, not the sigma-clipped median of the box around the blob. Both
// only set how far out flux is counted, which a curve of growth barely
// feels once most of the star is inside.
func (h *hfrBuf) ninaHFR(img fits.Image, c int, radius, sky, noise float64) float64 {
	m := hfrScale * radius
	b := int(math.Ceil(m)) + 2 // box half-size: room for the centroid to move
	x0, y0 := c%img.W, c/img.W
	if x0-b < 0 || y0-b < 0 || x0+b >= img.W || y0+b >= img.H {
		return math.NaN()
	}
	n := 2*b + 1
	h.box, h.wx, h.wy = slices.Grow(h.box[:0], n*n)[:n*n], slices.Grow(h.wx[:0], n)[:n], slices.Grow(h.wy[:0], n)[:n]
	for j := range n {
		row := img.Pix[(y0-b+j)*img.W+x0-b:]
		for i := range n {
			h.box[j*n+i] = float64(row[i]) - sky
		}
	}

	// Box coordinates from here on: pixel (i, j) is (x0-b+i, y0-b+j).
	cx, cy := float64(b), float64(b)
	if !h.kept(n, cx, cy, radius, sky, noise, img) {
		return math.NaN()
	}
	k := -0.5 / (max(m/2, 1) * max(m/2, 1))
	for range hfrIter {
		// The window is separable: exp(k(dx² + dy²)) = exp(k·dx²)·exp(k·dy²).
		for i := range n {
			dx, dy := float64(i)-cx, float64(i)-cy
			h.wx[i], h.wy[i] = math.Exp(k*dx*dx), math.Exp(k*dy*dy)
		}
		var sw, sx, sy float64
		for j := range n {
			lo, hi := chord(float64(j)-cy, cx, m, n)
			for i := lo; i <= hi; i++ {
				if f := h.box[j*n+i]; f > 0 {
					w := f * h.wx[i] * h.wy[j]
					sw, sx, sy = sw+w, sx+w*float64(i), sy+w*float64(j)
				}
			}
		}
		if sw <= 0 {
			return math.NaN()
		}
		nx, ny := sx/sw, sy/sw
		moved := math.Hypot(nx-cx, ny-cy)
		cx, cy = nx, ny
		if moved <= hfrConverge {
			break
		}
	}
	// The box edge limits the radius, as the blob's surroundings do in N.I.N.A.
	m = max(1, min(m, cx, float64(n-1)-cx, cy, float64(n-1)-cy)-0.5)

	// Pixels without flux add nothing to the curve of growth: they are
	// left out of the sort.
	h.samples = h.samples[:0]
	var total float64
	for j := range n {
		dy := float64(j) - cy
		lo, hi := chord(dy, cx, m, n)
		for i := lo; i <= hi; i++ {
			if f := h.box[j*n+i]; f > 0 {
				dx := float64(i) - cx
				h.samples = append(h.samples, hfrSample{d: math.Sqrt(dx*dx + dy*dy), f: f})
				total += f
			}
		}
	}
	if total <= 0 {
		return math.NaN()
	}

	return halfFlux(h.samples, total)
}

// kept is N.I.N.A.'s test that a blob is a star, on the box pixels within
// radius of (cx, cy). Its minimum count is of original pixels: binned ones
// each hold Scale² of them.
func (h *hfrBuf) kept(n int, cx, cy, radius, sky, noise float64, img fits.Image) bool {
	var sum float64
	area, lit := 0, 0
	for j := range n {
		lo, hi := chord(float64(j)-cy, cx, radius, n)
		for i := lo; i <= hi; i++ {
			f := h.box[j*n+i]
			sum += f
			area++
			if f > 1.5*noise {
				lit++
			}
		}
	}
	minLit := math.Ceil(float64(max(img.W, img.H)) * img.Scale / 1000)

	return area > 0 && sum/float64(area) >= min(0.1*sky, noise) && float64(lit)*img.Scale*img.Scale > minLit
}

// chord is the columns of a box row n wide, dy from the centre (cx, ·),
// that lie within radius m: i from lo to hi, empty when lo > hi.
func chord(dy, cx, m float64, n int) (int, int) {
	r2 := m*m - dy*dy
	if r2 < 0 {
		return 1, 0
	}
	h := math.Sqrt(r2)

	return max(0, int(math.Ceil(cx-h))), min(n-1, int(math.Floor(cx+h)))
}

// halfFlux interpolates the radius where the enclosed flux reaches half of
// total, as CalculateHalfFluxRadius (StarDetection.cs:391), over the lit
// pixels only: N.I.N.A. skips the others, so the step runs between the
// last two lit pixels.
func halfFlux(samples []hfrSample, total float64) float64 {
	// N.I.N.A.'s sort is stable; pixels exactly as far from a fractional
	// centroid are too rare to tell.
	slices.SortFunc(samples, func(a, b hfrSample) int { return cmp.Compare(a.d, b.d) })
	var sum, prevSum, prevD float64
	for _, s := range samples {
		sum += s.f
		if sum < total/2 {
			prevSum, prevD = sum, s.d

			continue
		}
		if sum == prevSum || s.d <= prevD {
			return s.d
		}

		return prevD + (total/2-prevSum)/(sum-prevSum)*(s.d-prevD)
	}

	return samples[len(samples)-1].d
}

// meanHFR is N.I.N.A.'s frame HFR: the mean over stars whose radius lies
// within hfrClip population σ of the mean radius, NaN-valued stars left
// out. N.I.N.A. averages where this tool's other columns take medians;
// it is kept so the number matches N.I.N.A.'s image history and its
// Autofocus After HFR Increase trigger.
func meanHFR(hfr, radius []float64) float64 {
	if len(radius) == 0 {
		return math.NaN()
	}
	// Two passes: N.I.N.A.'s Σr² − n·avg² rounds σ to 0 when the radii
	// agree, and then the cut drops stars one ulp off the mean.
	var s, sq float64
	for _, r := range radius {
		s += r
	}
	n := float64(len(radius))
	avg := s / n
	for _, r := range radius {
		sq += (r - avg) * (r - avg)
	}
	sd := math.Sqrt(sq / n)
	var sum float64
	var c int
	for i, h := range hfr {
		if !math.IsNaN(h) && math.Abs(radius[i]-avg) <= hfrClip*sd {
			sum += h
			c++
		}
	}
	if c == 0 {
		return math.NaN()
	}

	return sum / float64(c)
}
