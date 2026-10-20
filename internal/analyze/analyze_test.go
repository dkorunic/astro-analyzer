// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package analyze_test

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/dkorunic/astro-analyzer/internal/analyze"
	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/psf"
	"github.com/dkorunic/astro-analyzer/internal/report"
	"github.com/dkorunic/astro-analyzer/internal/synth"
)

// near reports whether got is within tol (relative) of want. Written so that
// NaN, meaning no successful fits, is never near anything.
func near(got, want, tol float64) bool { return math.Abs(got-want)/want <= tol }

func TestRoundStars(t *testing.T) {
	r := analyze.Frame(synth.Image(synth.StarField(4, 4)), 5, psf.Gaussian)
	t.Logf("%+v", r)
	if r.Stars != synth.NStars || !near(r.FWHM, 4, 0.02) || !(r.Ecc < 0.3) || !(r.SNR > 50) {
		t.Errorf("stars %d fwhm %.3f ecc %.2f snr %.1f; want %d round 4 px stars", r.Stars, r.FWHM, r.Ecc, r.SNR, synth.NStars)
	}
	if !near(r.Bg, synth.Bg, 0.002) || !near(r.Std, synth.Noise, 0.1) || !near(r.Noise, synth.Noise, 0.05) {
		t.Errorf("bg %.1f std %.2f noise %.2f, want ~%.0f/%.0f", r.Bg, r.Std, r.Noise, synth.Bg, synth.Noise)
	}
	if !math.IsNaN(r.FWHMArc) {
		t.Errorf("FWHMArc = %g without a plate scale, want NaN", r.FWHMArc)
	}
}

// TestElongated checks trailed stars (FWHM 6x3, true eccentricity 0.866).
func TestElongated(t *testing.T) {
	r := analyze.Frame(synth.Image(synth.StarField(6, 3)), 5, psf.Gaussian)
	if r.Stars != synth.NStars || !near(r.Ecc, 0.866, 0.02) {
		t.Errorf("stars = %d, ecc = %.3f; want %d stars of ecc 0.866", r.Stars, r.Ecc, synth.NStars)
	}
}

// TestUndersampled checks FWHM for stars only ~2 px wide, where counting
// pixels above half maximum overestimates by ~20%.
func TestUndersampled(t *testing.T) {
	for _, fwhm := range []float64{1.6, 1.85, 2.5} {
		r := analyze.Frame(synth.Image(synth.StarField(fwhm, fwhm)), 5, psf.Gaussian)
		t.Logf("fwhm %.2f: measured %.3f ecc %.2f", fwhm, r.FWHM, r.Ecc)
		if !near(r.FWHM, fwhm, 0.02) || !(r.Ecc < 0.3) {
			t.Errorf("fwhm %.2f: measured %.3f (ecc %.2f), want within 2%%", fwhm, r.FWHM, r.Ecc)
		}
	}
}

// TestMoffat checks Moffat fits recover FWHM of broad-winged stars, where a
// Gaussian fit is pulled wide by the wings.
func TestMoffat(t *testing.T) {
	for _, c := range []struct {
		beta  float64
		model string
	}{{2.5, psf.Moffat}, {4, psf.Moffat4}} {
		for _, fwhm := range []float64{1.85, 4} {
			img := synth.MoffatField(fwhm, c.beta)
			r, g := analyze.Frame(img, 5, c.model), analyze.Frame(img, 5, psf.Gaussian)
			t.Logf("β %g fwhm %.2f: %s %.3f, gaussian %.3f", c.beta, fwhm, c.model, r.FWHM, g.FWHM)
			if r.Stars != synth.NStars || !near(r.FWHM, fwhm, 0.03) || !(r.Ecc < 0.3) {
				t.Errorf("β %g %s: stars %d fwhm %.3f ecc %.2f, want %.2f px", c.beta, c.model, r.Stars, r.FWHM, r.Ecc, fwhm)
			}
		}
	}
}

// TestSaturatedBayer adds clipped stars to a raw CFA frame. After 2x2 binning
// their cores no longer sit at the max, so they must be caught before binning
// or their flat tops inflate FWHM.
func TestSaturatedBayer(t *testing.T) {
	const fwhm = 4.0 // raw pixels
	pix := synth.StarField(fwhm, fwhm)
	s := fwhm / (2 * math.Sqrt(2*math.Ln2))
	for i := range 5 {
		cx, cy := 60+float64(i)*120+0.5, 372.5 // offset so clipping straddles superpixels
		for y := int(cy) - 15; y <= int(cy)+15; y++ {
			for x := int(cx) - 15; x <= int(cx)+15; x++ {
				// Just over full well, so only the core clips, as on real frames.
				pix[y*synth.W+x] += 100000 * synth.Span(float64(x)-cx, s) * synth.Span(float64(y)-cy, s)
			}
		}
	}
	for i, v := range pix {
		pix[i] = min(v, 65535)
	}
	img := synth.Image(pix)
	img.Sat = fits.Saturation(img.Pix)
	r := analyze.Frame(fits.Bin2(img), 5, psf.Gaussian)
	if r.Stars != synth.NStars || !near(r.FWHM, fwhm, 0.05) {
		t.Errorf("stars = %d, fwhm = %.2f; want %d unsaturated stars of %.1f px", r.Stars, r.FWHM, synth.NStars, fwhm)
	}
}

// TestNebula checks stars on a steep gradient and a bright nebula are all
// found and measured against their local background.
func TestNebula(t *testing.T) {
	pix := synth.StarField(4, 4)
	for i := range pix {
		x, y := float64(i%synth.W), float64(i/synth.W)
		pix[i] += 2*x + 400*math.Exp(-((x-300)*(x-300)+(y-200)*(y-200))/(2*80*80))
	}
	r := analyze.Frame(synth.Image(pix), 5, psf.Moffat4)
	if r.Stars != synth.NStars || !near(r.Noise, synth.Noise, 0.05) {
		t.Errorf("stars %d noise %.2f; want %d stars and noise ~%.0f", r.Stars, r.Noise, synth.NStars, synth.Noise)
	}
}

// TestFieldAxis checks the elongation direction survives into the field map
// and that clean fits have a small residual.
func TestFieldAxis(t *testing.T) {
	for _, c := range []struct {
		fx, fy float64
		want   string
	}{{6, 3, "─"}, {3, 6, "│"}} {
		r := analyze.Frame(synth.Image(synth.StarField(c.fx, c.fy)), 5, psf.Moffat4)
		for i, cl := range r.Field {
			if g := report.AxisGlyph(cl.Axis[0], cl.Axis[1]); !math.IsNaN(cl.FWHM) && g != c.want {
				t.Errorf("%gx%g stars: cell %d axis %q, want %q", c.fx, c.fy, i, g, c.want)
			}
		}
		// Residual is in full-range units; a clean fit sits near the noise.
		if resid := r.Resid * 65535 / synth.Noise; !(resid < 4) {
			t.Errorf("resid = %.1f× noise, want a clean fit", resid)
		}
	}
}

// TestBinnedGain: EGAIN is per raw pixel, so a 2x2-binned frame must get the
// same SNR as an unbinned one whose gain is already four times higher.
func TestBinnedGain(t *testing.T) {
	img := synth.Image(synth.StarField(4, 4))
	img.Gain = 4
	want := analyze.Frame(img, 5, psf.Gaussian).SNR
	img.Gain, img.Scale = 1, 2
	if got := analyze.Frame(img, 5, psf.Gaussian).SNR; !near(got, want, 1e-9) {
		t.Errorf("binned SNR = %.2f, want %.2f", got, want)
	}
}

// TestBlank checks NaN pixels, like the border of a registered frame, are
// skipped instead of turning every measurement NaN.
func TestBlank(t *testing.T) {
	pix := synth.StarField(4, 4)
	for i := range pix {
		if i%synth.W < 20 || i/synth.W >= synth.H-15 {
			pix[i] = math.NaN()
		}
	}
	r := analyze.Frame(synth.Image(pix), 5, psf.Gaussian)
	if r.Stars != synth.NStars || !near(r.FWHM, 4, 0.03) || !near(r.Bg, synth.Bg, 0.002) || !near(r.Noise, synth.Noise, 0.05) {
		t.Errorf("stars %d fwhm %.2f bg %.1f noise %.2f; want %d 4 px stars, sky %.0f±%.0f",
			r.Stars, r.FWHM, r.Bg, r.Noise, synth.NStars, synth.Bg, synth.Noise)
	}
}

// TestCalibratedClipping clips bright stars, then dark-subtracts and
// flat-divides the frame: no pixel sits at the max any more, so the
// saturation mask is empty, and the flat cores must be caught by shape.
func TestCalibratedClipping(t *testing.T) {
	const fwhm = 4.0
	pix := synth.StarField(fwhm, fwhm)
	s := fwhm / (2 * math.Sqrt(2*math.Ln2))
	for i := range 5 {
		cx, cy := 110+float64(i)*120+0.3, 320.7 // between the regular stars
		for y := int(cy) - 15; y <= int(cy)+15; y++ {
			for x := int(cx) - 15; x <= int(cx)+15; x++ {
				pix[y*synth.W+x] += 200000 * synth.Span(float64(x)-cx, s) * synth.Span(float64(y)-cy, s)
			}
		}
	}
	rng := rand.New(rand.NewPCG(11, 12)) //nolint:gosec // repeatable dark noise
	for i, v := range pix {
		dark := 100 + 5*rng.NormFloat64()
		flat := 0.8 + 0.4*float64(i%synth.W)/synth.W // vignetting-like ramp
		pix[i] = (min(v, 65535) - dark) / flat
	}
	img := synth.Image(pix)
	if fits.Saturation(img.Pix) != nil {
		t.Fatal("the test needs clipping the saturation mask cannot see")
	}
	r := analyze.Frame(img, 5, psf.Gaussian)
	if r.Stars != synth.NStars || !near(r.FWHM, fwhm, 0.03) {
		t.Errorf("stars = %d, fwhm = %.2f; want %d unsaturated stars of %.1f px", r.Stars, r.FWHM, synth.NStars, fwhm)
	}
}

// TestSNRMedian: a few very bright stars must not set the frame's SNR, as
// they would a mean (here over 200 times the median).
func TestSNRMedian(t *testing.T) {
	const fwhm = 4.0
	want := analyze.Frame(synth.Image(synth.StarField(fwhm, fwhm)), 5, psf.Gaussian).SNR
	pix := synth.StarField(fwhm, fwhm)
	s := fwhm / (2 * math.Sqrt(2*math.Ln2))
	for i := range 3 {
		cx, cy := 110+float64(i)*120+0.3, 320.7 // between the regular stars
		for y := int(cy) - 15; y <= int(cy)+15; y++ {
			for x := int(cx) - 15; x <= int(cx)+15; x++ {
				pix[y*synth.W+x] += 5e6 * synth.Span(float64(x)-cx, s) * synth.Span(float64(y)-cy, s)
			}
		}
	}
	if got := analyze.Frame(synth.Image(pix), 5, psf.Gaussian).SNR; !near(got, want, 0.05) {
		t.Errorf("SNR with 3 bright stars = %.1f, want %.1f", got, want)
	}
}

// TestNoisyRoundStars: noise alone makes round stars at SNR ~30 measure
// ecc ~0.26, so eccentricity comes only from stars bright enough to trust,
// and is unknown without any. FWHM is unbiased and still uses every fit.
func TestNoisyRoundStars(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 9))
	pix := synth.StarField(3, 3)
	for i := range pix {
		pix[i] += 100 * rng.NormFloat64()
	}
	r := analyze.Frame(synth.Image(pix), 5, psf.Moffat4)
	if !math.IsNaN(r.Ecc) || !near(r.FWHM, 2.8, 0.05) || !(r.SNR < 50) {
		t.Errorf("snr %.0f: ecc %.2f fwhm %.2f, want unknown ecc and fwhm ~2.8", r.SNR, r.Ecc, r.FWHM)
	}
	// The field axis follows the same floor: noise headings are not a direction.
	for i, c := range r.Field {
		if !math.IsNaN(c.Axis[0]) {
			t.Errorf("cell %d axis %v from faint stars, want unknown", i, c.Axis)
		}
	}
}

// TestEphemeris: Sun and Moon need DATE-OBS; altitudes also the site and
// the Moon's distance the target. Missing inputs leave them unknown.
func TestEphemeris(t *testing.T) {
	img := synth.Image(synth.StarField(4, 4))
	img.Meta.Lat, img.Meta.Lon, img.Meta.RA, img.Meta.Dec = math.NaN(), math.NaN(), math.NaN(), math.NaN()
	r := analyze.Frame(img, 5, psf.Moffat4)
	if !math.IsNaN(r.SunAlt) || !math.IsNaN(r.MoonAlt) || !math.IsNaN(r.MoonIllum) || !math.IsNaN(r.MoonSep) {
		t.Errorf("no DATE-OBS: sun %v moon %v lit %v sep %v, want unknown", r.SunAlt, r.MoonAlt, r.MoonIllum, r.MoonSep)
	}
	// Full moon of 2024-04-23 at midnight UTC, seen from 45°N on the
	// Greenwich meridian: the Sun is far below the horizon.
	img.Meta.Time = time.Date(2024, 4, 23, 23, 49, 0, 0, time.UTC)
	img.Meta.Lat, img.Meta.Lon = 45, 0
	r = analyze.Frame(img, 5, psf.Moffat4)
	if !(r.MoonIllum > 0.99) || !(r.SunAlt < -25) || !(r.MoonAlt > 0) || !math.IsNaN(r.MoonSep) {
		t.Errorf("full moon: sun %.1f moon %.1f lit %.2f sep %v", r.SunAlt, r.MoonAlt, r.MoonIllum, r.MoonSep)
	}
}

// TestHotPixels: single bright pixels between the stars give the hot pixel
// level, are not taken for stars, and leave star measurements as without
// them. SNR may move a
// hair: the hot pixels raise the frame's measured noise.
func TestHotPixels(t *testing.T) {
	clean := analyze.Frame(synth.Image(synth.StarField(4, 4)), 5, psf.Moffat4)
	pix := synth.StarField(4, 4)
	for _, x := range []int{110, 230, 350, 470} {
		for _, y := range []int{95, 185} {
			pix[y*synth.W+x] += 500
		}
	}
	r := analyze.Frame(synth.Image(pix), 5, psf.Moffat4)
	if !math.IsNaN(clean.Hot) || !near(r.Hot, 500, 0.05) {
		t.Errorf("hot pixel level %.1f (clean frame %.1f), want ~500 (unknown)", r.Hot, clean.Hot)
	}
	if r.Stars != clean.Stars || r.FWHM != clean.FWHM || !near(r.SNR, clean.SNR, 0.001) {
		t.Errorf("stars %d fwhm %v snr %v; want %d, %v, %v as without hot pixels",
			r.Stars, r.FWHM, r.SNR, clean.Stars, clean.FWHM, clean.SNR)
	}
}

// TestPSFSNR checks each star's signal against the Moffat flux enclosed by
// PixInsight's ellipse (radius at a tenth of the peak, plus a pixel):
// πα²A/(β−1)·(1 − (1+R²/α²)^(1−β)), and the PSFSNR formula built on it.
func TestPSFSNR(t *testing.T) {
	const fwhm, beta, amp = 4.0, 4.0, 3000.0
	r := analyze.Frame(synth.MoffatField(fwhm, beta), 5, psf.Moffat4)
	alpha := fwhm / (2 * math.Sqrt(math.Pow(2, 1/beta)-1))
	rad := alpha*math.Sqrt(math.Pow(10, 1/beta)-1) + 1
	s := synth.NStars * math.Pi * alpha * alpha * amp / (beta - 1) * (1 - math.Pow(1+rad*rad/(alpha*alpha), 1-beta))
	want := 1.316e-7 * s * s / (4.987e6 * r.Noise * r.Noise)
	t.Logf("PSFSNR %.1f, want %.1f (ratio %.3f)", r.PSFSNR, want, r.PSFSNR/want)
	if !near(r.PSFSNR, want, 0.05) {
		t.Errorf("PSFSNR = %.1f, want %.1f", r.PSFSNR, want)
	}
}

// TestPSFSW checks PixInsight's PSF Signal Weight and SNR weight against
// the analytic Moffat field: equal stars leave nothing for the Chauvenet
// rejection, each star's mean flux is its signal over the tenth-maximum
// ellipse πα²(10^(1/β)−1) and M* of Gaussian noise is 0.6745σ.
func TestPSFSW(t *testing.T) {
	const fwhm, beta, amp = 4.0, 4.0, 3000.0
	r := analyze.Frame(synth.MoffatField(fwhm, beta), 5, psf.Moffat4)
	alpha := fwhm / (2 * math.Sqrt(math.Pow(2, 1/beta)-1))
	r10 := math.Pow(10, 1/beta) - 1
	rad := alpha*math.Sqrt(r10) + 1
	s := math.Pi * alpha * alpha * amp / (beta - 1) * (1 - math.Pow(1+rad*rad/(alpha*alpha), 1-beta))
	mean := s / (math.Pi * alpha * alpha * r10)
	want := 5.326e-6 * synth.NStars * s * synth.NStars * mean / (9e6 * r.Noise * 0.6745 * synth.Noise)
	t.Logf("PSFSW %.4g, want %.4g (ratio %.3f)", r.PSFSW, want, r.PSFSW/want)
	if !near(r.PSFSW, want, 0.05) {
		t.Errorf("PSFSW = %.4g, want %.4g", r.PSFSW, want)
	}
}

// TestSNRW: on a starless frame the two-sided background scale is 0.5309σ
// (see stats.TestTwoSidedScale), so SNRW is (0.5309σ/noise)². Stars widen
// the upper side, in PixInsight too, so a star field reads higher.
func TestSNRW(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	pix := make([]float64, synth.W*synth.H)
	for i := range pix {
		pix[i] = synth.Bg + synth.Noise*rng.NormFloat64()
	}
	r := analyze.Frame(synth.Image(pix), 5, psf.Moffat4)
	want := math.Pow(0.5309*synth.Noise/r.Noise, 2)
	if !near(r.SNRW, want, 0.03) {
		t.Errorf("SNRW = %.4f, want %.4f", r.SNRW, want)
	}
}

// TestHFD: Miyashita's HFD of a Gaussian star is twice its mean radius,
// 2σ√(π/2). Integrating over the pixel adds 1/12 px² to σ², and ASTAP's
// bilinear sampling around an off-pixel centroid 1/6 more (the mean of
// f(1−f) per axis), so it reads 7–15% over FWHM on these stars, as ASTAP does.
func TestHFD(t *testing.T) {
	for _, fwhm := range []float64{3, 4, 6} {
		r := analyze.Frame(synth.Image(synth.StarField(fwhm, fwhm)), 5, psf.Gaussian)
		sigma := fwhm / (2 * math.Sqrt(2*math.Ln2))
		want := 2 * math.Sqrt(sigma*sigma+0.25) * math.Sqrt(math.Pi/2)
		if !near(r.HFD, want, 0.02) {
			t.Errorf("fwhm %.1f: hfd %.3f, want %.3f", fwhm, r.HFD, want)
		}
	}
}
