// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package fits_test

import (
	"errors"
	"math"
	"math/rand/v2"
	"path/filepath"
	"testing"

	"codeberg.org/astrogo/fitsio"
	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/synth"
)

// TestLoadOriginGain checks that Celestron Origin's EGAIN, per ADC count,
// is divided by the 25 ADU each count is stored as; other software keeps it.
func TestLoadOriginGain(t *testing.T) {
	pix := synth.StarField(4, 4)
	for i, v := range pix {
		pix[i] = 25 * math.Round(v/25)
		if i%500 == 0 {
			pix[i] += 10 // interpolated hot pixels break an exact GCD
		}
	}
	for _, c := range []struct {
		creator string
		want    float64
	}{{"Origin 1.5.6240", 0.2793 / 25}, {"N.I.N.A.", 0.2793}} {
		path := filepath.Join(t.TempDir(), "light.fits")
		err := synth.WriteFITS(path, pix,
			fitsio.Card{Name: "CREATOR", Value: c.creator},
			fitsio.Card{Name: "EGAIN", Value: 0.2793})
		if err != nil {
			t.Fatal(err)
		}
		img, err := fits.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(img.Gain-c.want) > 1e-12 {
			t.Errorf("%s: gain %g, want %g", c.creator, img.Gain, c.want)
		}
	}
}

// TestLoad round-trips a 16-bit unsigned (BZERO=32768) frame and checks the
// pixel scaling and header context.
func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "light.fits")
	pix := synth.StarField(4, 4)
	err := synth.WriteFITS(path, pix,
		fitsio.Card{Name: "EGAIN", Value: 1.5},
		fitsio.Card{Name: "XPIXSZ", Value: 3.76},
		fitsio.Card{Name: "FOCALLEN", Value: 400.0},
		fitsio.Card{Name: "DATE-OBS", Value: "2026-10-16T21:16:00"},
		fitsio.Card{Name: "EXPTIME", Value: 15.0},
		fitsio.Card{Name: "ALTITUDE", Value: 120.0}, // site elevation, must be ignored
		fitsio.Card{Name: "AIRMASS", Value: 2.0},
	)
	if err != nil {
		t.Fatal(err)
	}
	img, err := fits.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if img.W != synth.W || img.H != synth.H || img.Scale != 1 || img.Sat != nil {
		t.Fatalf("image %dx%d scale %g sat %v", img.W, img.H, img.Scale, img.Sat != nil)
	}
	if d := float64(img.Pix[0]) - math.Round(pix[0]); d != 0 {
		t.Errorf("pixel 0 = %g, want %g (BZERO not applied?)", img.Pix[0], math.Round(pix[0]))
	}
	if img.Gain != 1.5 || math.Abs(img.Meta.Plate-206.265*3.76/400) > 1e-12 {
		t.Errorf("gain %g plate %g", img.Gain, img.Meta.Plate)
	}
	m := img.Meta
	if m.Date != "2026-10-16T21:16:00" || m.Exp != 15 || math.Abs(m.Alt-30) > 1e-9 || !math.IsNaN(m.Temp) {
		t.Errorf("meta = %+v, want date, 15 s, 30° altitude from AIRMASS 2, unknown temp", m)
	}
}

func TestFrameType(t *testing.T) {
	typ := func(v string) fitsio.Card { return fitsio.Card{Name: "IMAGETYP", Value: v} }
	origin := fitsio.Card{Name: "CREATOR", Value: "Origin 1.3.5343"}
	for _, c := range []struct {
		name, want string
		cards      []fitsio.Card
	}{
		{name: "a.fits", want: fits.Light},
		{name: "a.fits", want: fits.Light, cards: []fitsio.Card{typ("Light Frame")}},
		{name: "a.fits", want: fits.Light, cards: []fitsio.Card{typ("object")}},
		{name: "a.fits", want: fits.Bias, cards: []fitsio.Card{typ("BIAS")}},
		{name: "a.fits", want: fits.Bias, cards: []fitsio.Card{typ("Offset")}},
		{name: "a.fits", want: fits.Bias, cards: []fitsio.Card{{Name: "EXPTIME", Value: 0.0}}},
		{name: "a.fits", want: fits.Dark, cards: []fitsio.Card{typ("Master Dark"), {Name: "NCOMBINE", Value: 20}}},
		{name: "a.fits", want: fits.DarkFlat, cards: []fitsio.Card{typ("Dark Flat")}},
		{name: "a.fits", want: fits.Stack, cards: []fitsio.Card{typ("LIGHT"), {Name: "NCOMBINE", Value: 20}}},
		{name: "a.fits", want: fits.Stack, cards: []fitsio.Card{typ("Master Light")}},
		{name: "a.fits", want: "TRICOLOR", cards: []fitsio.Card{typ("Tricolor")}},
		// Origin flats may say LIGHT; the name decides, but only on Origin.
		{name: "flat.fits", want: fits.Flat, cards: []fitsio.Card{origin, typ("LIGHT")}},
		{name: "flat.fits", want: fits.Light, cards: []fitsio.Card{typ("LIGHT")}},
		{name: "Light0001.fits", want: fits.Light, cards: []fitsio.Card{origin, typ("LIGHT")}},
	} {
		path := filepath.Join(t.TempDir(), c.name)
		if err := synth.WriteFITS(path, synth.StarField(4, 4), c.cards...); err != nil {
			t.Fatal(err)
		}
		img, err := fits.Load(path)
		if got := img.Meta.Type; got != c.want || (err == nil) != (c.want == fits.Light) ||
			(err != nil && !errors.Is(err, fits.ErrNotLight)) {
			t.Errorf("%s %v: type %q, error %v; want %q", c.name, c.cards, got, err, c.want)
		}
	}
}

// TestBin2Saturation checks one clipped sub-pixel marks its whole superpixel,
// even though the averaged value no longer looks clipped.
func TestBin2Saturation(t *testing.T) {
	const c = 65535 // five pixels at exactly the max: a clipped plateau
	img := fits.Image{W: 6, H: 2, Scale: 1, Pix: []float32{
		c, 100, c, c, 100, 100,
		c, 100, c, 100, 100, 100,
	}}
	img.Sat = fits.Saturation(img.Pix)
	out := fits.Bin2(img)
	if out.W != 3 || out.H != 1 || out.Scale != 2 || !out.Sat[0] || !out.Sat[1] || out.Sat[2] {
		t.Errorf("bin2 = %+v, want 3x1 at scale 2, first two superpixels saturated", out)
	}
	if out.Pix[0] > 0.98*c {
		t.Errorf("superpixel 0 = %g; the test needs it to look unclipped after averaging", out.Pix[0])
	}
	if fits.Saturation([]float32{1, 2, 3, 3}) != nil {
		t.Error("an unclipped frame must have no saturation mask")
	}
}

// TestWCS round-trips pixels through the sky with the CDELT/CROTA2 form used
// by Celestron Origin and checks the reference pixel maps to CRVAL.
func TestWCS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "solved.fits")
	err := synth.WriteFITS(path, make([]float64, synth.W*synth.H),
		fitsio.Card{Name: "CTYPE1", Value: "RA---TAN"},
		fitsio.Card{Name: "CRVAL1", Value: 350.2},
		fitsio.Card{Name: "CRVAL2", Value: 61.2},
		fitsio.Card{Name: "CRPIX1", Value: 300.0},
		fitsio.Card{Name: "CRPIX2", Value: 200.0},
		fitsio.Card{Name: "CDELT1", Value: -0.000342},
		fitsio.Card{Name: "CDELT2", Value: 0.000342},
		fitsio.Card{Name: "CROTA2", Value: 132.1},
		fitsio.Card{Name: "BAYERPAT", Value: "GBRG"},
	)
	if err != nil {
		t.Fatal(err)
	}
	img, err := fits.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	w := img.Meta.WCS
	if !w.OK || w.NAXIS != [2]int{synth.W, synth.H} || !img.Meta.CFA {
		t.Fatalf("wcs %+v cfa %v", w, img.Meta.CFA)
	}
	if ra, dec := w.World(300, 200); ra != 350.2 || dec != 61.2 {
		t.Errorf("reference pixel -> %g, %g", ra, dec)
	}
	for _, p := range [][2]float64{{1, 1}, {600, 400}, {123.4, 321.9}} {
		ra, dec := w.World(p[0], p[1])
		if x, y := w.Pixel(ra, dec); math.Abs(x-p[0]) > 1e-6 || math.Abs(y-p[1]) > 1e-6 {
			t.Errorf("round trip %v -> %g, %g", p, x, y)
		}
	}
	// One pixel along +x moves 0.000342° on the sky.
	ra0, dec0 := w.World(300, 200)
	ra1, dec1 := w.World(301, 200)
	if d := math.Hypot((ra1-ra0)*math.Cos(dec0*math.Pi/180), dec1-dec0); math.Abs(d-0.000342) > 1e-8 {
		t.Errorf("pixel scale %g deg, want 0.000342", d)
	}
}

// TestBlank checks integer pixels equal to BLANK (the raw stored value)
// load as NaN, the float convention for undefined pixels.
func TestBlank(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blank.fits")
	pix := synth.StarField(4, 4)
	pix[0] = 0 // stored as -32768 under BZERO = 32768
	if err := synth.WriteFITS(path, pix, fitsio.Card{Name: "BLANK", Value: -32768}); err != nil {
		t.Fatal(err)
	}
	img, err := fits.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(float64(img.Pix[0])) || math.IsNaN(float64(img.Pix[1])) {
		t.Errorf("pixels 0, 1 = %g, %g; want NaN, a value", img.Pix[0], img.Pix[1])
	}
}

// TestUnsolved: ASTRSOLV = F means the WCS is the mount's pointing guess. It
// must not count as a plate solution, but its CD matrix still gives the
// plate scale.
func TestUnsolved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unsolved.fits")
	err := synth.WriteFITS(path, make([]float64, synth.W*synth.H),
		fitsio.Card{Name: "CTYPE1", Value: "RA---TAN"},
		fitsio.Card{Name: "CRVAL1", Value: 350.2},
		fitsio.Card{Name: "CRVAL2", Value: 61.2},
		fitsio.Card{Name: "CRPIX1", Value: 300.0},
		fitsio.Card{Name: "CRPIX2", Value: 200.0},
		fitsio.Card{Name: "CD1_1", Value: 0.0},
		fitsio.Card{Name: "CD1_2", Value: 0.000342},
		fitsio.Card{Name: "CD2_1", Value: -0.000342},
		fitsio.Card{Name: "CD2_2", Value: 0.0},
		fitsio.Card{Name: "ASTRSOLV", Value: false},
	)
	if err != nil {
		t.Fatal(err)
	}
	img, err := fits.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if img.Meta.WCS.OK || math.Abs(img.Meta.Plate-0.000342*3600) > 1e-9 {
		t.Errorf("wcs ok %v plate %g; want no solution, plate %g", img.Meta.WCS.OK, img.Meta.Plate, 0.000342*3600)
	}
}

// TestPCMatrix: CDELT with a PC rotation matrix is the same solution as
// CDELT with CROTA2 (Calabretta & Greisen 2002, eq. 189).
func TestPCMatrix(t *testing.T) {
	const rot, c1, c2 = 132.1, -0.000342, 0.000342
	s, c := math.Sincos(rot * math.Pi / 180)
	load := func(name string, cards ...fitsio.Card) fits.WCS {
		path := filepath.Join(t.TempDir(), name)
		cards = append([]fitsio.Card{
			{Name: "CTYPE1", Value: "RA---TAN"},
			{Name: "CRVAL1", Value: 350.2},
			{Name: "CRVAL2", Value: 61.2},
			{Name: "CRPIX1", Value: 300.0},
			{Name: "CRPIX2", Value: 200.0},
			{Name: "CDELT1", Value: c1},
			{Name: "CDELT2", Value: c2},
		}, cards...)
		if err := synth.WriteFITS(path, synth.StarField(4, 4), cards...); err != nil {
			t.Fatal(err)
		}
		img, err := fits.Load(path)
		if err != nil {
			t.Fatal(err)
		}

		return img.Meta.WCS
	}
	want := load("crota.fits", fitsio.Card{Name: "CROTA2", Value: rot})
	got := load("pc.fits",
		fitsio.Card{Name: "PC1_1", Value: c},
		fitsio.Card{Name: "PC1_2", Value: -s * c2 / c1},
		fitsio.Card{Name: "PC2_1", Value: s * c1 / c2},
		fitsio.Card{Name: "PC2_2", Value: c},
	)
	for i := range 2 {
		for j := range 2 {
			if math.Abs(got.CD[i][j]-want.CD[i][j]) > 1e-12 {
				t.Fatalf("PC CD = %v, CROTA2 CD = %v", got.CD, want.CD)
			}
		}
	}
}

// TestZeroBorder: the 0-filled border a registered frame gets is blank,
// while a zero pixel inside the frame is data.
func TestZeroBorder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registered.fits")
	pix := synth.StarField(4, 4)
	for i := range pix {
		if i%synth.W < 20 || i/synth.W >= synth.H-15 {
			pix[i] = 0
		}
	}
	inner := 300*synth.W + 300
	pix[inner] = 0
	if err := synth.WriteFITS(path, pix); err != nil {
		t.Fatal(err)
	}
	img, err := fits.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(float64(img.Pix[0])) || !math.IsNaN(float64(img.Pix[(synth.H-1)*synth.W+synth.W-1])) ||
		img.Pix[inner] != 0 || math.IsNaN(float64(img.Pix[300*synth.W+20])) {
		t.Errorf("corner %g, far corner %g, inner zero %g, first column past border %g; want NaN, NaN, 0, a value",
			img.Pix[0], img.Pix[(synth.H-1)*synth.W+synth.W-1], img.Pix[inner], img.Pix[300*synth.W+20])
	}
}

// TestPlateCDOverCDELT: a solved CD matrix sets the plate scale. FOCALLEN is
// nominal (here 2% long), and with CD present the standard ignores CDELT, so
// a stale placeholder CDELT2 must not set it either.
func TestPlateCDOverCDELT(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cd.fits")
	if err := synth.WriteFITS(path, synth.StarField(4, 4),
		fitsio.Card{Name: "CTYPE1", Value: "RA---TAN"},
		fitsio.Card{Name: "CRVAL1", Value: 350.2},
		fitsio.Card{Name: "CRVAL2", Value: 61.2},
		fitsio.Card{Name: "CRPIX1", Value: 300.0},
		fitsio.Card{Name: "CRPIX2", Value: 200.0},
		fitsio.Card{Name: "CD1_1", Value: -0.000342},
		fitsio.Card{Name: "CD2_2", Value: 0.000342},
		fitsio.Card{Name: "CDELT2", Value: 1.0},
		fitsio.Card{Name: "XPIXSZ", Value: 3.76},
		fitsio.Card{Name: "FOCALLEN", Value: 1.02 * 206.265 * 3.76 / (0.000342 * 3600)},
	); err != nil {
		t.Fatal(err)
	}
	img, err := fits.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := 0.000342 * 3600; math.Abs(img.Meta.Plate-want) > 1e-9 {
		t.Errorf("plate = %g\"/px, want %g from CD", img.Meta.Plate, want)
	}
}

// TestPlateCDWithoutReference: a CD matrix without CRPIX still gives the
// plate scale, though the WCS cannot place anything on the sky.
func TestPlateCDWithoutReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cd.fits")
	if err := synth.WriteFITS(path, synth.StarField(4, 4),
		fitsio.Card{Name: "CTYPE1", Value: "RA---TAN"},
		fitsio.Card{Name: "CD1_1", Value: -0.000342},
		fitsio.Card{Name: "CD2_2", Value: 0.000342},
	); err != nil {
		t.Fatal(err)
	}
	img, err := fits.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := 0.000342 * 3600; math.Abs(img.Meta.Plate-want) > 1e-9 || img.Meta.WCS.OK {
		t.Errorf("plate = %g\"/px, wcs ok %v; want %g from CD and no solution", img.Meta.Plate, img.Meta.WCS.OK, want)
	}
}

// TestOriginMeta: Celestron Origin writes DATE-OBS in local time with the
// offset in TIMEZONE, against the standard's UTC, and names the camera in
// CAMERA rather than INSTRUME.
func TestOriginMeta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "origin.fits")
	if err := synth.WriteFITS(path, synth.StarField(4, 4),
		fitsio.Card{Name: "DATE-OBS", Value: "2026-10-13T21:51:54"},
		fitsio.Card{Name: "TIMEZONE", Value: "+0200"},
		fitsio.Card{Name: "OBJECT", Value: "Bubble Nebula"},
		fitsio.Card{Name: "TELESCOP", Value: "RASA6F2"},
		fitsio.Card{Name: "CAMERA", Value: "Origin678C"},
		fitsio.Card{Name: "APERTURE", Value: 152.4},
		fitsio.Card{Name: "FOCALLEN", Value: 335.0},
		fitsio.Card{Name: "LATITUDE", Value: 45.83},
		fitsio.Card{Name: "LONGITUD", Value: 16.07},
		fitsio.Card{Name: "RA", Value: 350.17},
		fitsio.Card{Name: "DEC", Value: 61.21},
	); err != nil {
		t.Fatal(err)
	}
	img, err := fits.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := img.Meta
	if got := m.Time.UTC().Format("2006-01-02T15:04:05"); got != "2026-10-13T19:51:54" || m.Time.Format("15:04") != "21:51" {
		t.Errorf("time %v (UTC %s), want 21:51 local, 19:51 UTC", m.Time, got)
	}
	if m.Object != "Bubble Nebula" || m.Telescope != "RASA6F2" || m.Camera != "Origin678C" ||
		m.Aperture != 152.4 || m.FocalLen != 335 || m.Lat != 45.83 || m.Lon != 16.07 ||
		m.RA != 350.17 || m.Dec != 61.21 || m.Size != [2]int{synth.W, synth.H} {
		t.Errorf("meta %+v", m)
	}
}

// TestCFAColour: the Bayer pattern applies from the first stored row
// unless its row mirror makes the two green sites agree far better, which
// Meta.BayerFlip reports. (Celestron Origin's GBRG reads as written: on real
// frames its greens agree within 0.4%, read from the top 12–17% apart.)
// XBAYROFF shifts it a column. Greens differ slightly, as real ones do.
func TestCFAColour(t *testing.T) {
	// RGGB in storage order: R 1000, G 2000 | G 2050, B 3000.
	pix := make([]float64, synth.W*synth.H)
	for y := range synth.H {
		for x := range synth.W {
			pix[y*synth.W+x] = [4]float64{1000, 2000, 2050, 3000}[(y%2)*2+x%2]
		}
	}
	want := [3]float32{1000, 2025, 3000}
	for i, cards := range [][]fitsio.Card{
		{{Name: "BAYERPAT", Value: "RGGB"}},
		{{Name: "BAYERPAT", Value: "GBRG"}}, // the mirror: greens 2000 apart as written
		{{Name: "BAYERPAT", Value: "RGGB"}, {Name: "ROWORDER", Value: "BOTTOM-UP"}},
		{{Name: "BAYERPAT", Value: "GRBG"}, {Name: "XBAYROFF", Value: 1}},
	} {
		path := filepath.Join(t.TempDir(), "cfa.fits")
		if err := synth.WriteFITS(path, pix, cards...); err != nil {
			t.Fatal(err)
		}
		img, err := fits.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		var got [3]float32
		for i, v := range img.Colour {
			var sum float64
			for _, x := range v {
				sum += float64(x)
			}
			got[i] = float32(sum / float64(len(v)))
		}
		if got != want {
			t.Errorf("%v: R, G, B = %v, want %v", cards, got, want)
		}
		if m := img.Meta; m.Bayer != cards[0].Value || m.BayerFlip != (i == 1) {
			t.Errorf("%v: Bayer %q, flip %v", cards, m.Bayer, m.BayerFlip)
		}
	}
}

// TestPairNoise checks two bias frames sharing a fixed pattern: Load's
// single-frame noise includes the pattern, their difference does not.
func TestPairNoise(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	pattern := make([]float64, synth.W*synth.H)
	for i := range pattern {
		pattern[i] = 1000 + 20*rng.NormFloat64() // offset non-uniformity
	}
	var paths [2]string
	for k := range paths {
		pix := make([]float64, len(pattern))
		for i, p := range pattern {
			pix[i] = p + 10*rng.NormFloat64() // read noise
		}
		paths[k] = filepath.Join(t.TempDir(), "b.fits")
		if err := synth.WriteFITS(paths[k], pix, fitsio.Card{Name: "IMAGETYP", Value: "BIAS"}); err != nil {
			t.Fatal(err)
		}
	}
	img, err := fits.Load(paths[0])
	if !errors.Is(err, fits.ErrNotLight) || !(math.Abs(img.Level-1000) < 2) || !(img.PixNoise > 20) {
		t.Errorf("bias: level %v, pixel noise %v (pattern included), error %v", img.Level, img.PixNoise, err)
	}
	if rn, err := fits.PairNoise(paths[0], paths[1]); err != nil || !(math.Abs(rn-10) < 0.5) {
		t.Errorf("PairNoise = %v, %v; want 10", rn, err)
	}
}
