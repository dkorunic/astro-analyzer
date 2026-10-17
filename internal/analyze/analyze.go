// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

// Package analyze measures one frame: star detection, aperture SNR, PSF
// shape and sky statistics, aggregated per frame and per field cell.
package analyze

import (
	"cmp"
	"math"
	"slices"

	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/psf"
	"github.com/dkorunic/astro-analyzer/internal/sky"
	"github.com/dkorunic/astro-analyzer/internal/stats"
)

// Result holds per-frame measurements; NaN marks values that could not be measured.
type Result struct {
	fits.Meta

	Stars         int
	FWHM, FWHMArc float64 // median over fitted stars, original pixels / arcsec
	Ecc, Resid    float64 // median over fitted stars
	SNR           float64 // median over all detected stars
	// PSFSNR and PSFSW are PixInsight's PSF SNR and PSF Signal Weight,
	// WBPP's default weight (BPP-Global.js:273): see psfSNR and psfSW.
	PSFSNR, PSFSW float64
	// SNRW is SubframeSelector's SNR, the SNR weight of WBPP's presets:
	// the squared ratio of the background's two-sided scale to its noise.
	SNRW    float64
	Bg, Std float64 // sigma-clipped sky median and stddev
	Noise   float64 // wavelet (MRS) noise
	// Sky gradient: the fitted plane's range across the frame in ADU, and
	// the direction it rises toward, degrees counter-clockwise from +x.
	Gradient, GradAngle float64
	// Hot is the typical excess over the background, in ADU, of hot
	// pixels: it follows dark current, so sensor temperature. Compare it
	// between frames, not cameras. See hotLevel.
	Hot float64
	// Sky is the sigma-clipped sky level in raw R, G and B pixels of a
	// colour (CFA) frame, NaN otherwise. Moonlight is blue, sodium light
	// pollution orange, so the colour of a background change names it.
	Sky [3]float64
	// Sun and Moon at the exposure start, degrees: altitudes need the site,
	// the separation needs the target. They explain sky background changes.
	SunAlt, MoonAlt float64
	MoonIllum       float64 // lit fraction of the disc, 0..1
	MoonSep         float64 // Moon to target
	Field           [9]Cell // 3x3 grid, row-major from the first FITS row
}

// Cell summarises fitted stars in one ninth of the frame. Axis is the mean
// of ecc·(cos 2θ, sin 2θ): doubling the angle makes 0° and 180° the same
// direction, and weighting by ecc stops round stars adding random headings.
type Cell struct {
	FWHM, Ecc float64
	Axis      [2]float64
}

// Frame analyses img, detecting stars at sigma times the noise above the
// local background and fitting the given PSF model to the brighter ones.
func Frame(img fits.Image, sigma float64, model string) Result {
	// PSF fits need a well-measured profile; faint stars give noisy, biased fits.
	const minFitSNR = 20
	// Eccentricity is √(1−ratio²), steep at ratio 1, so noise always makes
	// round stars look elongated: ~0.4 at SNR 20, ~0.25 at 50. Faint stars
	// dominate the counts, so a frame's median would follow its SNR (cloud,
	// sky brightness) rather than its tracking. FWHM, a geometric mean, is
	// not biased this way and keeps every fit.
	// ponytail: 50, not higher: short subs have only one or two stars over
	// 100, and a median of two is noisier than the bias it removes.
	const minShapeSNR = 50

	bg, std := stats.SkyStats(img.Pix)
	// Wavelet noise and the large-scale background map ignore nebulosity and
	// gradients, so faint stars on bright nebula are still detected and
	// measured against their local sky.
	noise, local := stats.NoiseMRS(img.Pix, img.W, img.H, bg, std)
	gradient, gradAngle := stats.Gradient(img.Pix, img.W, img.H)
	// PixInsight's background scale is 256 image pixels.
	mstar := stats.MStar(img.Pix, img.W, img.H, int(256/img.Scale))
	snrw := snrWeight(img, noise)
	// Blank (NaN) pixels, such as registration borders, read as flat sky from
	// here on: nothing is detected there and no sum turns NaN.
	img.Pix = stats.FillNaN(img.Pix, bg)
	// EGAIN counts electrons per raw ADU; a binned pixel averages Scale² of
	// them, so its photon noise is Scale² times smaller.
	gain := img.Gain * img.Scale * img.Scale
	nan := math.NaN()
	r := Result{
		Meta: img.Meta, Bg: bg, Std: std, Noise: noise,
		FWHM: nan, FWHMArc: nan, Ecc: nan, Resid: nan, SNR: nan, PSFSNR: nan, PSFSW: nan,
		SNRW: snrw, Gradient: gradient, GradAngle: gradAngle,
	}
	r.SunAlt, r.MoonAlt, r.MoonIllum, r.MoonSep = ephemeris(img.Meta)
	r.Sky = skyColour(img.Colour)

	w, h := img.W, img.H
	k := float32(sigma * noise)
	var cands []int
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			v := img.Pix[i]
			if v <= local[i]+k {
				continue
			}
			if v > img.Pix[i-1] && v >= img.Pix[i+1] && v > img.Pix[i-w-1] && v > img.Pix[i-w] &&
				v > img.Pix[i-w+1] && v >= img.Pix[i+w-1] && v >= img.Pix[i+w] && v >= img.Pix[i+w+1] {
				cands = append(cands, i)
			}
		}
	}
	// Brightest first, so fainter maxima sitting on a star's profile are rejected as blends.
	slices.SortFunc(cands, func(a, b int) int { return cmp.Compare(img.Pix[b], img.Pix[a]) })

	visited := make([]bool, len(img.Pix))
	var stars []star
	var snrs, hot []float64
	for _, c := range cands {
		if visited[c] {
			continue
		}
		if onePixel(img, c, float64(local[c])) {
			// A clipped hot pixel no longer follows its dark current.
			if img.Sat == nil || !img.Sat[c] {
				hot = append(hot, float64(img.Pix[c]-local[c]))
			}

			continue
		}
		region, fwhm0, ra, snr, sky, ok := seed(img, c, float64(local[c]), noise, gain, visited)
		if !ok {
			continue
		}
		for _, p := range region {
			visited[p] = true
		}
		r.Stars++
		snrs = append(snrs, snr)
		if snr < minFitSNR {
			continue
		}
		if f, ok := psf.Fit(img, c, int(math.Ceil(ra)), sky, fwhm0, model); ok {
			stars = append(stars, star{Star: f, snr: snr, cell: min(2, 3*(c%w)/w) + 3*min(2, 3*(c/w)/h)})
		}
	}

	// Per-frame and per-cell medians, as PixInsight: a few blends or very
	// bright stars cannot drag them the way they drag a mean. SNR spans
	// orders of magnitude, so its mean would follow the brightest few stars.
	r.SNR = stats.MedianOf(snrs)
	r.PSFSNR = psfSNR(stars, noise)
	r.PSFSW = psfSW(stars, noise, mstar)
	r.Hot = hotLevel(hot)
	pick := func(cell int, minSNR float64, get func(psf.Star) float64) []float64 {
		var v []float64
		for _, s := range stars {
			if (cell < 0 || s.cell == cell) && s.snr >= minSNR {
				v = append(v, get(s.Star))
			}
		}

		return v
	}
	fwhmOf := func(f psf.Star) float64 { return f.FWHM * img.Scale }
	eccOf := func(f psf.Star) float64 { return f.Ecc }
	r.FWHM = stats.MedianOf(pick(-1, 0, fwhmOf))
	r.Ecc = stats.MedianOf(pick(-1, minShapeSNR, eccOf))
	r.Resid = stats.MedianOf(pick(-1, 0, func(f psf.Star) float64 { return f.Resid }))
	if img.Meta.Plate > 0 {
		r.FWHMArc = r.FWHM * img.Meta.Plate
	}
	for i := range r.Field {
		c := &r.Field[i]
		c.FWHM, c.Ecc = stats.MedianOf(pick(i, 0, fwhmOf)), stats.MedianOf(pick(i, minShapeSNR, eccOf))
		// Same SNR floor as Ecc: noise-elongated faint stars point every way
		// and would shrink a real elongation below the glyph's cutoff.
		var n float64
		for _, s := range stars {
			if s.cell == i && s.snr >= minShapeSNR {
				c.Axis[0] += s.Ecc * math.Cos(2*s.Theta)
				c.Axis[1] += s.Ecc * math.Sin(2*s.Theta)
				n++
			}
		}
		c.Axis[0], c.Axis[1] = c.Axis[0]/n, c.Axis[1]/n // NaN when empty
	}

	return r
}

// star is one fitted star with its aperture SNR and field cell.
type star struct {
	psf.Star

	snr  float64
	cell int
}

// psfSNR is PixInsight's PSFSNR estimator (PSFSignalEstimator.h:282):
// 1.316e-7·S²/(4.987e6·σ²), with S the summed signal of all fitted stars
// and σ the MRS noise, as SubframeSelector passes it. S/σ does not depend
// on the sample scale, so ADU serve as well as PixInsight's [0, 1]. NaN
// without fitted stars.
//
// ponytail: PixInsight sums every detected star, saturated ones too; this
// sums only the fitted ones (SNR ≥ 20, unsaturated), and on binned colour
// frames, so compare values within a session, not with PixInsight's.
func psfSNR(stars []star, noise float64) float64 {
	var s float64
	for _, st := range stars {
		s += st.Signal
	}
	if s <= 0 || !(noise > 0) {
		return math.NaN()
	}

	return 1.316e-7 * s * s / (4.987e6 * noise * noise)
}

// psfSW is PixInsight's PSF Signal Weight (PSFSignalEstimator.h:265):
// 5.326e-6·S·M/(9e6·σ·M*), with S the summed signal of the fitted stars,
// M the sum of their mean fluxes after robust Chauvenet rejection with the
// rejected tails Winsorized (PSFSignalEstimator.cpp:370-395), σ the MRS
// noise as SubframeSelector passes it, and M* from stats.MStar. Like
// psfSNR it does not depend on the sample scale, and carries the same
// caveat: only fitted stars count. NaN without fitted stars.
func psfSW(stars []star, noise, mstar float64) float64 {
	var s float64
	means := make([]float64, len(stars))
	for k, st := range stars {
		s += st.Signal
		means[k] = st.Mean
	}
	if s <= 0 || !(noise > 0) || !(mstar > 0) {
		return math.NaN()
	}
	i, j := stats.Chauvenet(means) // sorts means
	var m float64
	for k := range means {
		m += means[min(max(k, i), j-1)]
	}

	return 5.326e-6 * s * m / (9e6 * noise * mstar)
}

// snrWeight is SubframeSelector's SNR (SubframeSelectorInstance.cpp:704):
// (two-sided background scale / MRS noise)², with samples within 2 DN of
// either end of the range ignored. NaN when unknown, where PixInsight
// leaves 0.
func snrWeight(img fits.Image, noise float64) float64 {
	clip := 2 / 65535.0 * img.Range
	e := stats.TwoSidedScale(img.Pix, clip, img.Range-clip) / noise
	if math.IsInf(e, 0) || math.IsNaN(e) || 1+e*e == 1 {
		return math.NaN()
	}

	return e * e
}

// skyColour is the sigma-clipped sky level of each CFA colour sample, NaN
// where there is none (mono and RGB frames).
func skyColour(samples [3][]float32) [3]float64 {
	var sky [3]float64
	for c, v := range samples {
		sky[c] = math.NaN()
		if len(v) > 0 {
			sky[c], _ = stats.SkyStats(v)
		}
	}

	return sky
}

// ephemeris returns the Sun's and Moon's altitudes, the Moon's illuminated
// fraction and its distance from the target, NaN where the header lacks the
// time, site or target.
func ephemeris(m fits.Meta) (float64, float64, float64, float64) {
	nan := math.NaN()
	if m.Time.IsZero() {
		return nan, nan, nan, nan
	}
	sunAlt, moonAlt, sep := nan, nan, nan
	if !math.IsNaN(m.Lat) && !math.IsNaN(m.Lon) {
		ra, dec := sky.Sun(m.Time)
		sunAlt = sky.Altitude(m.Time, m.Lat, m.Lon, ra, dec)
		moonAlt = sky.MoonAltitude(m.Time, m.Lat, m.Lon)
	}
	if !math.IsNaN(m.RA) && !math.IsNaN(m.Dec) {
		ra, dec, _ := sky.Moon(m.Time)
		sep = sky.Separation(ra, dec, m.RA, m.Dec)
	}

	return sunAlt, moonAlt, sky.Illumination(m.Time), sep
}

// seed measures the star at c: its half-max region, the FWHM estimate that
// region implies, the aperture radius ra, and the aperture SNR and sky. The
// FWHM estimate only seeds the PSF fit and sizes the aperture.
//
// The background map is a heavily smoothed image, so it holds some of the
// star's own light: about 2πσ²/534 of the peak, 14% for an 8 px star. That
// raises the half-max level and shrinks the region, so the region is measured
// a second time against the annulus sky, which lies beyond the star. If the
// wider region then runs into a neighbour, the first measurement stands:
// the fix sharpens the seed, it does not redefine what counts as a star.
func seed(img fits.Image, c int, bg, noise, gain float64, visited []bool) ([]int, float64, float64, float64, float64, bool) {
	var region []int
	var fwhm0, ra, snr float64
	for pass := range 2 {
		r, ok := halfMax(img, c, bg, visited)
		ok = ok && len(r) >= 2 // single pixel = hot pixel
		f := 2 * math.Sqrt(float64(len(r))/math.Pi)
		a := max(3, 1.5*f)
		var s, sky float64
		if ok {
			s, sky, ok = aperture(img, c, a, noise, gain)
		}
		if !ok {
			return region, fwhm0, ra, snr, bg, pass > 0
		}
		region, fwhm0, ra, snr, bg = r, f, a, s, sky
	}

	return region, fwhm0, ra, snr, bg, true
}

// hotLevel is the median of the 500th to 1500th brightest hot pixels;
// hot holds their excesses, brightest first (candidates run in that order).
// Not a count: how many clear the detection threshold follows the sky
// noise, not the sensor. Nor the hottest: they sit near full well and
// barely respond. On a real uncooled session this window tracks CCD-TEMP
// with correlation 0.97. With fewer hot pixels (cooled sensors) the window
// shrinks to the fainter two thirds.
func hotLevel(hot []float64) float64 {
	lo, hi := min(500, len(hot)/3), min(1500, len(hot))

	return stats.MedianOf(hot[lo:hi])
}

// onePixel reports whether the maximum at c is a single pixel: all four
// neighbours sit below its half-max over bg. No star is that narrow, so it
// is a hot pixel or a cosmic ray. halfMax rejects exactly these too, with
// the same rounding, so counting them changes no measurement.
func onePixel(img fits.Image, c int, bg float64) bool {
	half := float32(bg + (float64(img.Pix[c])-bg)/2)
	p := img.Pix

	return p[c-1] < half && p[c+1] < half && p[c-img.W] < half && p[c+img.W] < half
}

// halfMax flood-fills the pixels above half of the peak (over background)
// connected to c. It fails on blends, saturated cores, oversized sources and
// image edges.
func halfMax(img fits.Image, c int, bg float64, visited []bool) ([]int, bool) {
	const maxR = 25
	peak := img.Pix[c]
	half := float32(bg + (float64(peak)-bg)/2)
	cx, cy := c%img.W, c/img.W
	seen := map[int]bool{c: true}
	stack, region := []int{c}, []int{}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		region = append(region, p)
		if img.Sat != nil && img.Sat[p] {
			return nil, false // clipped core: width is meaningless
		}
		x, y := p%img.W, p/img.W
		if x == 0 || y == 0 || x == img.W-1 || y == img.H-1 || abs(x-cx) > maxR || abs(y-cy) > maxR {
			return nil, false
		}
		for _, n := range [4]int{p - 1, p + 1, p - img.W, p + img.W} {
			if seen[n] || img.Pix[n] < half {
				continue
			}
			if img.Pix[n] > peak || visited[n] {
				return nil, false
			}
			seen[n] = true
			stack = append(stack, n)
		}
	}
	// Calibration (dark subtracted, flat divided) leaves a clipped core
	// uneven, so the saturation mask misses it, but the core stays flat. An
	// unclipped star centred on a pixel corner has four near-equal pixels;
	// a fifth within 1% of the peak needs FWHM > 23 px, so five is a plateau.
	// ponytail: cores clipped over fewer than five (super)pixels pass; their
	// FWHM is barely inflated.
	tol, flat := 0.01*(float64(peak)-bg), 0
	for _, p := range region {
		if float64(img.Pix[p]) >= float64(peak)-tol {
			flat++
		}
	}

	return region, flat < 5
}

// aperture measures the flux in a circle of radius ra around c, minus the
// median of a local sky annulus (2ra..3ra), and returns its SNR using the
// same error model as PixInsight's AperturePhotometry script:
//
//	var = n·σ² + n²·σ²/nSky + flux/gain   (photon term only when gain is known)
//
// The local sky level is returned too, to seed the PSF fit.
func aperture(img fits.Image, c int, ra, std, gain float64) (float64, float64, bool) {
	cx, cy := c%img.W, c/img.W
	r := int(math.Ceil(3 * ra))
	if cx-r < 0 || cy-r < 0 || cx+r >= img.W || cy+r >= img.H {
		return 0, 0, false
	}
	at := func(x, y int) float64 { return float64(img.Pix[(cy+y)*img.W+cx+x]) }

	ring := make([]float64, 0, (2*r+1)*(2*r+1))
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if d2 := float64(x*x + y*y); d2 >= 4*ra*ra && d2 <= 9*ra*ra {
				ring = append(ring, at(x, y))
			}
		}
	}
	sky := stats.Select(ring, len(ring)/2)

	var flux, n float64
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if float64(x*x+y*y) <= ra*ra {
				flux += at(x, y) - sky
				n++
			}
		}
	}
	if flux <= 0 {
		return 0, 0, false
	}
	variance := n*std*std + n*n*std*std/float64(len(ring))
	if gain > 0 {
		variance += flux / gain
	}

	return flux / math.Sqrt(variance), sky, true
}

func abs(x int) int {
	if x < 0 {
		return -x
	}

	return x
}
