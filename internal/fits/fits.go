// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

// Package fits loads FITS light frames into a float32 luminance image ready
// for star analysis, along with the header context the analysis needs.
package fits

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"codeberg.org/astrogo/fitsio"
)

// Load errors. ErrNotLight marks bias, dark and flat frames, which callers
// skip quietly.
var (
	ErrNotLight = errors.New("not a light frame")
	ErrNoImage  = errors.New("no image HDU found")
	ErrBitpix   = errors.New("unsupported BITPIX")
	ErrSize     = errors.New("frames differ in size")
)

// Image is one frame's pixels plus what the analysis needs from its header.
type Image struct {
	// Colour holds samples of the raw R, G and B pixels of a CFA frame,
	// taken before binning mixes them; nil for mono and RGB data.
	Colour [3][]float32
	Pix    []float32
	Sat    []bool // saturated pixels, nil when nothing is clipped
	Meta   Meta
	W, H   int
	Scale  float64 // original pixels per analysed pixel (2 for debayered CFA)
	// Range is the sample value PixInsight reads as 1: it normalises
	// pixels to [0, 1], and its PSF residual is in those units.
	Range float64
	Gain  float64 // e-/ADU, 0 if unknown
	// PixNoise is the noise of one raw pixel in ADU, measured before
	// binning: see pixNoise.
	PixNoise float64
	// Cal is what Load measures of a calibration frame; zero for lights.
	Cal CalStats
}

// CalStats describes a calibration frame's pixels, which Load keeps only
// for darks (to find their hot pixels the way lights' are found).
type CalStats struct {
	// Level is the median raw sample (bias, dark, flat).
	Level float64
	// Rows and Cols are a bias frame's banding: the σ of the step between
	// neighbouring row (column) means in ADU, less what pixel noise alone
	// gives a mean of that many pixels.
	Rows, Cols float64
	// Clipped is the fraction of a flat's pixels at its maximum, when that
	// maximum is within 2% of the sample range: an overexposed flat.
	Clipped float64
	// Dust counts a flat's dust shadows, and DustDepth is the deepest's
	// fraction below its surroundings: see dust.
	Dust      int
	DustDepth float64
}

// Meta is observing context from the FITS header; NaN/""/zero when absent.
type Meta struct {
	// Time is the exposure start. It is UTC unless the header has TIMEZONE,
	// in which case it is in that zone, so it also gives local clock time.
	Time                      time.Time
	Date, Filter              string
	Object, Telescope, Camera string
	// Bayer is BAYERPAT as written. BayerFlip is set when the data matches
	// it read from the other end (the top row), not the first stored row:
	// see cfaSamples.
	Bayer string
	// Type is the frame type (Light, Bias, Dark...): see frameType.
	Type string
	// Pier is the mount's pier side (PIERSIDE: EAST or WEST, as SGP and
	// ASIAIR write it), "" if absent. N.I.N.A. does not write it.
	Pier           string
	WCS            WCS    // plate solution, for drizzle dither analysis
	Size           [2]int // sensor pixels, before Bayer binning
	Exp, Temp, Alt float64
	// SetTemp is the cooler's setpoint (SET-TEMP), NaN without a cooler.
	SetTemp float64
	Setting float64 // camera gain setting: GAIN, else ISOSPEED; NaN if absent
	Plate   float64 // arcsec per original pixel, 0 if unknown
	Pixel   float64 // XPIXSZ, µm, binning included; 0 if unknown
	// Focus is the focuser position (FOCUSPOS, FOCPOS) and FocusTemp its
	// temperature (FOCTEMP, FOCUSTEM), as N.I.N.A. and ASTAP read them; NaN
	// if absent.
	Focus, FocusTemp float64
	// SQM is a sky brightness the capture software wrote, mag/arcsec²
	// (SQM: ASTAP's photometric, zenith-corrected, or N.I.N.A.'s from a
	// meter; MPSAS); NaN if absent.
	SQM float64
	// Weather station readings N.I.N.A. writes (ObservingConditions):
	// ambient temperature and dew point in °C, relative humidity and cloud
	// cover in %, and the cloud sensor's sky temperature in °C; NaN if
	// absent.
	Ambient, DewPoint, Humidity, Cloud, SkyTemp float64
	Aperture, FocalLen                          float64 // mm
	Lat, Lon                                    float64 // site, degrees, east positive
	RA, Dec                                     float64 // target (frame centre), degrees
	// Stack is how many frames an integrated image combines (NCOMBINE,
	// NSTACK, STACKCNT); NaN for a single exposure. Celestron Origin's bias
	// and dark files are 9-frame masters.
	Stack     float64
	CFA       bool // one-shot-colour (Bayer) sensor
	BayerFlip bool
	// Master is set when IMAGETYP says Master (PixInsight's integrations).
	Master bool
}

// Load reads the first 2D/3D image HDU, applies BSCALE/BZERO, averages colour
// planes and 2x2-bins Bayer (CFA) data. Gain comes from EGAIN and plate scale
// from XPIXSZ/FOCALLEN (or CDELT2); either is left 0 when the header lacks it.
//
// Other frame types return ErrNotLight with the header's Meta, and bias and
// flats their PixNoise, Range and Cal without pixels. A dark is loaded in
// full, as a light would be, plus its Cal.Level.
func Load(path string) (Image, error) {
	src, err := open(path)
	if err != nil {
		return Image{}, err
	}
	defer src.done()
	hdr, axes := src.hdr, src.hdr.Axes()
	w, h := axes[0], axes[1]
	t := frameType(filepath.Base(path), hdr)
	if t != Light && t != Dark {
		img := Image{Meta: meta(hdr, axes, t), Range: math.Exp2(float64(hdr.Bitpix())) - 1}
		if t == Bias || t == Flat {
			pix, _, err := src.read()
			if err != nil {
				return Image{}, err
			}
			img.Range = toADU(pix, hdr.Bitpix())
			img.PixNoise, img.Cal = pixNoise(pix, w, h), calStats(t, pix, w, h, img.Range, img.Meta.CFA)
		}

		return img, ErrNotLight
	}
	pix, planes, err := src.read()
	if err != nil {
		return Image{}, err
	}
	img := Image{W: w, H: h, Pix: pix, Scale: 1}
	var level float64 // a dark's, before binning makes a new Image
	if t == Dark {
		toADU(pix, hdr.Bitpix())
		level = float64(rough(pix))
	}
	blankBorder(img.Pix, w, h)
	// Celestron Origin stores each ADC count as several ADU (25 at ISO
	// 1000: every pixel a multiple of 25) but writes EGAIN per count.
	// Photon transfer on a real session gives 0.0095 e-/ADU against
	// EGAIN/25 = 0.0112. ponytail: only Origin is verified; other
	// software scales 12-bit data by 16 and may write EGAIN either way.
	adu := 1.0
	if strings.HasPrefix(str(hdr, "CREATOR"), "Origin") && hdr.Bitpix() > 0 {
		adu = float64(step(img.Pix))
	}
	// Before binning: a clipped core averaged with unclipped neighbours no
	// longer looks saturated. ponytail: RGB planes are averaged first, same dilution.
	img.Sat = Saturation(img.Pix)
	noise := pixNoise(img.Pix, w, h)
	var flip bool
	if planes == 1 && hdr.Get("BAYERPAT") != nil {
		var colour [3][]float32
		colour, flip = cfaSamples(img.Pix, w, h, hdr)
		img = Bin2(img)
		img.Colour = colour
	}
	img.PixNoise = noise
	g, _ := num(hdr, "EGAIN")
	// The original Origin's values step by 6.25 ADU (gaps of 6 and 7), so
	// step finds 1, and its EGAIN is 4.5× the photon-transfer gain: 0.0742
	// at ISO 200 against 0.0162 (consecutive raw lights, unit 7a8cfd) and
	// 0.0165 (originstack camera_profile.py, 31 sessions, unit 932f56); at
	// ISO 500 0.0297 against 0.0064. ponytail: one ratio for every ISO,
	// 3% high at ISO 500.
	if strings.HasPrefix(str(hdr, "CAMERA"), "Origin178") {
		g /= 4.5
	}
	img.Gain = max(g, 0) / adu
	img.Range = math.Exp2(float64(hdr.Bitpix())) - 1
	// Float data normalised to [0, 1] (PixInsight) is no longer in ADU,
	// so EGAIN no longer converts it to electrons.
	if hdr.Bitpix() < 0 {
		img.Range = 65535 // ponytail: float ADU data assumed 16-bit
		if maxOf(img.Pix) <= 1 {
			img.Gain, img.Range = 0, 1
		}
	}

	img.Meta = meta(hdr, axes, t)
	img.Meta.BayerFlip = flip
	if t == Dark {
		img.Cal.Level = level

		return img, ErrNotLight
	}

	return img, nil
}

// toADU puts a calibration frame's samples normalized to [0, 1] (a
// PixInsight master) back on the 16-bit scale PixInsight divided them by,
// so they compare with raw frames' ADU, and returns the sample range.
// Lights keep their scale: nothing is compared with them in ADU.
//
// ponytail: assumes 16-bit data, as Load does for float lights.
func toADU(pix []float32, bitpix int) float64 {
	if bitpix > 0 {
		return math.Exp2(float64(bitpix)) - 1
	}
	if maxOf(pix) <= 1 {
		for i := range pix {
			pix[i] *= 65535
		}
	}

	return 65535
}

// calStats measures a bias or flat frame's pixels (see CalStats).
func calStats(t string, pix []float32, w, h int, rng float64, cfa bool) CalStats {
	c := CalStats{Level: float64(rough(pix))}
	switch t {
	case Bias:
		c.Rows, c.Cols = banding(pix, w, h)
	case Flat:
		if top := maxOf(pix); float64(top) >= 0.98*rng {
			n := 0
			for _, v := range pix {
				if v == top {
					n++
				}
			}
			c.Clipped = float64(n) / float64(len(pix))
		}
		img := Image{W: w, H: h, Pix: pix, Scale: 1}
		if cfa {
			img = Bin2(img) // superpixels, so the Bayer grid is no texture
		}
		c.Dust, c.DustDepth = dust(img.Pix, img.W, img.H)
	}

	return c
}

// banding is the σ of the step between neighbouring row means and between
// neighbouring column means, robust (16th–84th percentile) and over √2 as
// in pixNoise, with the part pixel noise alone gives a mean of w (h)
// pixels removed in quadrature.
func banding(pix []float32, w, h int) (float64, float64) {
	rows, cols := make([]float64, h), make([]float64, w)
	for y := range h {
		for x, v := range pix[y*w : (y+1)*w] {
			rows[y] += float64(v)
			cols[x] += float64(v)
		}
	}
	noise := pixNoise(pix, w, h)
	steps := func(means []float64, n int) float64 {
		d := make([]float64, 0, len(means))
		for i := 1; i < len(means); i++ {
			d = append(d, (means[i]-means[i-1])/float64(n))
		}
		slices.Sort(d)
		k := len(d) - 1
		sd := (d[k*841/1000] - d[k*159/1000]) / 2 / math.Sqrt2

		return math.Sqrt(max(sd*sd-noise*noise/float64(n), 0))
	}

	return steps(rows, w), steps(cols, h)
}

// Dust shadows: a flat's 16-pixel blocks whose median sits over dustDip
// below the median of the 7×7 blocks around them, counted as connected
// groups. Vignetting changes far more slowly than that, and the 3-block
// margin keeps the frame edge out. ponytail: dust rings far wider than 3
// blocks (48 px) lower their whole neighbourhood and are missed.
const (
	dustBlock = 16
	dustDip   = 0.01
)

// dust returns the number of dust shadows in a flat and the deepest one's
// depth below its surroundings (0 when none).
func dust(pix []float32, w, h int) (int, float64) {
	nx, ny := w/dustBlock, h/dustBlock
	if nx < 7 || ny < 7 {
		return 0, 0
	}
	bm := make([]float64, nx*ny)
	v := make([]float32, 0, dustBlock*dustBlock)
	for by := range ny {
		for bx := range nx {
			v = v[:0]
			for y := by * dustBlock; y < (by+1)*dustBlock; y++ {
				v = append(v, pix[y*w+bx*dustBlock:y*w+(bx+1)*dustBlock]...)
			}
			v = slices.DeleteFunc(v, func(x float32) bool { return x != x })
			slices.Sort(v)
			if len(v) > 0 {
				bm[by*nx+bx] = float64(v[len(v)/2])
			}
		}
	}
	dip := make([]float64, nx*ny) // 0 = no dip
	around := make([]float64, 0, 49)
	for by := 3; by < ny-3; by++ {
		for bx := 3; bx < nx-3; bx++ {
			around = around[:0]
			for yy := by - 3; yy <= by+3; yy++ {
				around = append(around, bm[yy*nx+bx-3:yy*nx+bx+4]...)
			}
			slices.Sort(around)
			if m := around[len(around)/2]; m > 0 {
				if d := 1 - bm[by*nx+bx]/m; d > dustDip {
					dip[by*nx+bx] = d
				}
			}
		}
	}
	n, deepest := 0, 0.0
	for i := range dip {
		if dip[i] == 0 {
			continue
		}
		n++
		stack := []int{i} // flood the group so it counts once
		for len(stack) > 0 {
			j := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if dip[j] == 0 {
				continue
			}
			deepest = max(deepest, dip[j])
			dip[j] = 0
			for _, k := range []int{j - 1, j + 1, j - nx, j + nx} { // the margin stops row wrap
				if k >= 0 && k < len(dip) && dip[k] > 0 {
					stack = append(stack, k)
				}
			}
		}
	}

	return n, deepest
}

// source is one image in any supported format: its FITS header (made up
// from the file's metadata for other formats) and a reader for its
// samples, which returns them with BSCALE/BZERO applied and colour planes
// averaged, and the plane count. done releases the file.
type source struct {
	hdr  *fitsio.Header
	read func() ([]float32, int, error)
	done func()
}

// Supported reports whether a directory walk should load name. TIFF is
// left out, though Load reads it: in a session folder it is most often a
// processed export, with no header to mark it as one. Name TIFF files on
// the command line instead.
func Supported(name string) bool {
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".fits", ".fit", ".fts", ".fz", ".xisf":
		return true
	default:
		return rawExt[ext]
	}
}

// open reads path by its extension; anything else is taken for FITS.
func open(path string) (source, error) {
	switch ext := strings.ToLower(filepath.Ext(path)); {
	case ext == ".xisf":
		return openXISF(path)
	case ext == ".tif", ext == ".tiff":
		return openTIFF(path)
	case rawExt[ext]:
		return openRaw(path)
	}

	return openFITS(path)
}

// openFITS opens the first 2D/3D image HDU of path, or the first
// tile-compressed (fpack) image.
func openFITS(path string) (source, error) {
	fh, err := os.Open(path)
	if err != nil {
		return source{}, err
	}
	f, err := fitsio.Open(bufio.NewReader(fh))
	if err != nil {
		fh.Close()

		return source{}, err
	}
	done := func() {
		f.Close()
		fh.Close()
	}
	for _, hdu := range f.HDUs() {
		switch hdu := hdu.(type) {
		case fitsio.Image:
			if axes := hdu.Header().Axes(); len(axes) >= 2 && axes[0]*axes[1] != 0 {
				return source{hdr: hdu.Header(), read: func() ([]float32, int, error) { return samples(hdu) }, done: done}, nil
			}
		case *fitsio.Table:
			if c := hdu.Header().Get("ZIMAGE"); c != nil && c.Value == true {
				src, err := openTiled(hdu)
				if err != nil {
					done()

					return source{}, err
				}
				src.done = done

				return src, nil
			}
		}
	}
	done()

	return source{}, ErrNoImage
}

// samples reads im's pixels with BSCALE/BZERO applied and colour planes
// averaged, and returns the plane count.
func samples(im fitsio.Image) ([]float32, int, error) {
	hdr := im.Header()
	bscale, ok := num(hdr, "BSCALE")
	if !ok {
		bscale = 1
	}
	bzero, _ := num(hdr, "BZERO")
	// BLANK marks undefined integer pixels; float data uses NaN for that.
	blank, hasBlank := num(hdr, "BLANK")
	if !hasBlank || hdr.Bitpix() < 0 {
		blank = math.NaN() // equal to nothing
	}
	axes := hdr.Axes()
	pix := make([]float32, axes[0]*axes[1])
	planes, err := readPixels(im, hdr.Bitpix(), pix, bscale, bzero, blank)

	return pix, planes, err
}

// pair reads two frames' samples, which must be the same size, and the
// row width.
func pair(a, b string) ([2][]float32, int, error) {
	var pix [2][]float32
	var w int
	for i, p := range []string{a, b} {
		src, err := open(p)
		if err != nil {
			return pix, 0, err
		}
		pix[i], _, err = src.read()
		w = src.hdr.Axes()[0]
		src.done()
		if err != nil {
			return pix, 0, err
		}
	}
	if len(pix[0]) != len(pix[1]) {
		return pix, 0, fmt.Errorf("%w: %s, %s", ErrSize, a, b)
	}

	return pix, w, nil
}

// PairNoise is the read noise in ADU from two bias frames: the robust σ of
// their difference over √2. Differencing cancels the fixed pattern that a
// single frame's pixel-to-pixel noise still holds. Every 16th row and 3rd
// pixel keep it cheap, as in pixNoise.
func PairNoise(a, b string) (float64, error) {
	pix, w, err := pair(a, b)
	if err != nil {
		return math.NaN(), err
	}
	var d []float64
	for y := 0; y < len(pix[0])/w; y += 16 {
		for x := 0; x < w; x += 3 {
			if v := float64(pix[0][y*w+x] - pix[1][y*w+x]); !math.IsNaN(v) {
				d = append(d, v)
			}
		}
	}
	if len(d) < 100 {
		return math.NaN(), nil
	}

	return spread(d) / math.Sqrt2, nil
}

// PairGain is the gain in e⁻/ADU by two-point photon transfer on two
// consecutive raw light frames (Janesick, Photon Transfer; originstack
// camera_profile.raw_pair_gain): their difference cancels everything
// static (sky structure, vignetting, the sensor's fixed pattern), so half
// its variance is one frame's temporal noise, and gain = (signal −
// pedestal) / variance. Pixels are binned by signal from the 5th to the
// 60th percentile, the sky end where stars are rare, and the bins' median
// gain returned. Read noise stays in the variance, so it reads low by
// RN²/σ²: under 3% on skies thousands of ADU above the pedestal.
func PairGain(a, b string, pedestal float64) (float64, error) {
	pix, _, err := pair(a, b)
	if err != nil {
		return math.NaN(), err
	}
	type sample struct{ s, d float64 }
	var v []sample
	for i := 0; i < len(pix[0]); i += 7 {
		p, q := float64(pix[0][i]), float64(pix[1][i])
		if !math.IsNaN(p) && !math.IsNaN(q) {
			v = append(v, sample{s: (p + q) / 2, d: p - q})
		}
	}
	slices.SortFunc(v, func(x, y sample) int { return cmp.Compare(x.s, y.s) })
	var gains []float64
	for pct := 5; pct < 60; pct += 5 {
		bin := v[len(v)*pct/100 : len(v)*(pct+5)/100]
		if len(bin) < 1000 {
			continue
		}
		d := make([]float64, len(bin))
		for i, x := range bin {
			d[i] = x.d
		}
		sd := spread(d)
		if sig := bin[len(bin)/2].s - pedestal; sig > 0 && sd > 0 {
			gains = append(gains, sig/(sd*sd/2))
		}
	}

	if len(gains) == 0 {
		return math.NaN(), nil
	}
	slices.Sort(gains)

	return gains[len(gains)/2], nil
}

// spread is the robust σ of d (half the 16th–84th percentile span); it
// sorts d.
func spread(d []float64) float64 {
	slices.Sort(d)
	n := len(d) - 1

	return (d[n*841/1000] - d[n*159/1000]) / 2
}

// Frame types from frameType.
const (
	Light    = "LIGHT"
	Bias     = "BIAS"
	Dark     = "DARK"
	Flat     = "FLAT"
	DarkFlat = "DARKFLAT"
	Stack    = "STACK" // an integrated image, not a sub: a master light or a live stack
	// Processed is an XISF image without FITS keywords: see openXISF.
	Processed = "PROCESSED"
)

// frameType reads IMAGETYP in its usual spellings (N.I.N.A. "LIGHT", MaxIm DL
// "Light Frame", "Offset", "ZERO", IRAF "object", PixInsight "Master Dark");
// no IMAGETYP means a light. A light that combines several frames (NCOMBINE,
// NSTACK, STACKCNT) is a stack, and a light with zero exposure a bias. Some
// Celestron Origin flats say IMAGETYP = 'LIGHT', so on Origin the file name
// (flat.fits, dark0009.fits, bias0009.fits) decides first (originstack
// frame_discovery.py:133). An unknown IMAGETYP is returned upper-cased.
func frameType(name string, hdr *fitsio.Header) string {
	t := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r
		}

		return -1
	}, strings.ToUpper(str(hdr, "IMAGETYP")))
	master := strings.HasPrefix(t, "MASTER")
	t = strings.TrimPrefix(t, "MASTER")
	if strings.HasPrefix(str(hdr, "CREATOR"), "Origin") {
		name = strings.ToUpper(name)
		for _, k := range []string{Bias, Dark, Flat} {
			if strings.HasPrefix(name, k) {
				t = k
			}
		}
	}
	switch {
	case t == "", strings.HasPrefix(t, "LIGHT"), strings.HasPrefix(t, "OBJECT"), strings.HasPrefix(t, "SCIENCE"):
		if master || first(hdr, "NCOMBINE", "NSTACK", "STACKCNT") > 1 {
			return Stack
		}
		if first(hdr, "EXPTIME", "EXPOSURE") == 0 {
			return Bias
		}

		return Light
	case strings.HasPrefix(t, "DARKFLAT"), strings.HasPrefix(t, "FLATDARK"):
		return DarkFlat
	case strings.HasPrefix(t, Dark):
		return Dark
	case strings.HasPrefix(t, Flat):
		return Flat
	case strings.HasPrefix(t, Bias), strings.HasPrefix(t, "OFFSET"), strings.HasPrefix(t, "ZERO"):
		return Bias
	}

	return t
}

// meta reads the observing context from the header; BayerFlip needs the
// pixels and is left to the caller.
func meta(hdr *fitsio.Header, axes []int, typ string) Meta {
	planes := 1
	for _, a := range axes[2:] {
		planes *= a
	}
	m := Meta{
		Type:      typ,
		Stack:     first(hdr, "NCOMBINE", "NSTACK", "STACKCNT"),
		Date:      str(hdr, "DATE-OBS"),
		Time:      obsTime(hdr),
		Filter:    str(hdr, "FILTER"),
		Object:    str(hdr, "OBJECT"),
		Telescope: str(hdr, "TELESCOP"),
		Camera:    str(hdr, "INSTRUME", "CAMERA"),
		Exp:       first(hdr, "EXPTIME", "EXPOSURE"),
		Setting:   first(hdr, "GAIN", "ISOSPEED"),
		Temp:      first(hdr, "CCD-TEMP"),
		SetTemp:   first(hdr, "SET-TEMP"),
		Ambient:   first(hdr, "AMBTEMP"),
		DewPoint:  first(hdr, "DEWPOINT"),
		Humidity:  first(hdr, "HUMIDITY"),
		Cloud:     first(hdr, "CLOUDCVR"),
		SkyTemp:   first(hdr, "SKYTEMP"),
		Pier:      strings.ToUpper(str(hdr, "PIERSIDE")),
		Focus:     first(hdr, "FOCUSPOS", "FOCPOS"),
		FocusTemp: first(hdr, "FOCTEMP", "FOCUSTEM"),
		SQM:       first(hdr, "SQM", "MPSAS"),
		// Not ALTITUDE: that is the observing site's elevation in metres.
		Alt:      first(hdr, "CENTALT", "OBJCTALT"),
		Aperture: first(hdr, "APTDIA", "APERTURE"),
		FocalLen: first(hdr, "FOCALLEN"),
		// ponytail: numeric degrees only; sexagesimal strings
		// ("+45 49 38", MaxIm DL) read as unknown.
		Lat:   first(hdr, "SITELAT", "LATITUDE", "OBSGEO-B"),
		Lon:   first(hdr, "SITELONG", "LONGITUD", "OBSGEO-L"),
		RA:    first(hdr, "CRVAL1", "RA"),
		Dec:   first(hdr, "CRVAL2", "DEC"),
		Size:  [2]int{axes[0], axes[1]},
		WCS:   readWCS(hdr),
		CFA:   planes == 1 && hdr.Get("BAYERPAT") != nil,
		Bayer: strings.ToUpper(str(hdr, "BAYERPAT")),
		// The prefix frameType strips.
		Master: strings.HasPrefix(strings.ToUpper(str(hdr, "IMAGETYP")), "MASTER"),
	}
	// XPIXSZ already includes binning per the FITS convention (MaxIm DL, N.I.N.A.).
	px, _ := num(hdr, "XPIXSZ")
	m.Pixel = max(px, 0)
	fl, _ := num(hdr, "FOCALLEN")
	cd := m.WCS.CD
	det := cd[0][0]*cd[1][1] - cd[0][1]*cd[1][0]
	// A plate solve measures the scale; FOCALLEN is nominal and often off
	// by 1–2%. An unsolved CD (mount pointing) is nominal too, so it
	// ranks after XPIXSZ/FOCALLEN. The CD matrix ranks before a bare
	// CDELT2: with CD cards present, CDELT is ignored (Greisen &
	// Calabretta 2002, §2.1.2), so it may be stale.
	if m.WCS.OK && det != 0 {
		m.Plate = math.Sqrt(math.Abs(det)) * 3600
	} else if px > 0 && fl > 0 {
		m.Plate = 206.265 * px / fl
	} else if det != 0 {
		m.Plate = math.Sqrt(math.Abs(det)) * 3600
	} else if c2, ok := num(hdr, "CDELT2"); ok {
		m.Plate = math.Abs(c2) * 3600
	}
	// ponytail: plane-parallel airmass, inaccurate below ~20° altitude
	if am, ok := num(hdr, "AIRMASS"); math.IsNaN(m.Alt) && ok && am >= 1 {
		m.Alt = math.Asin(1/am) * 180 / math.Pi
	}

	return m
}

// pixNoise is the noise of one raw pixel in ADU: the robust σ (16th–84th
// percentile span) of differences between pixels two apart in a row, which
// share a CFA colour, over √2. Differencing cancels gradients, nebulae and
// row banding; the percentiles ignore stars. Every 16th row and 3rd pixel
// keep it cheap. NaN when too few pixels are valid.
func pixNoise(pix []float32, w, h int) float64 {
	var d []float64
	for y := 0; y < h; y += 16 {
		row := pix[y*w : (y+1)*w]
		for x := 0; x+2 < w; x += 3 {
			if v := float64(row[x] - row[x+2]); !math.IsNaN(v) {
				d = append(d, v)
			}
		}
	}
	if len(d) < 100 {
		return math.NaN()
	}
	slices.Sort(d)
	n := len(d) - 1

	return (d[n*841/1000] - d[n*159/1000]) / 2 / math.Sqrt2
}

// step is the ADU per ADC count when software scales the data up: the
// largest k ≤ 64 that divides at least 99% of the valid pixels, as a few
// are not multiples (Origin: 1 in 5000, interpolated hot pixels, which an
// exact GCD turns 25 into 5). 1 when nothing is scaled.
func step(pix []float32) int {
	var v []int
	for i := 0; i < len(pix) && len(v) < 20000; i += 97 {
		if p := pix[i]; p == p && p != 0 {
			v = append(v, int(math.Abs(float64(p))))
		}
	}
	for k := 64; k > 1; k-- {
		n := 0
		for _, x := range v {
			if x%k == 0 {
				n++
			}
		}
		if len(v) > 0 && n >= len(v)*99/100 {
			return k
		}
	}

	return 1
}

// readPixels decodes the image into pix, averaging its colour planes, and
// returns the plane count. Samples convert straight to float32: a float64
// copy of a raw frame would be the largest allocation of the whole analysis.
func readPixels(im fitsio.Image, bitpix int, pix []float32, bscale, bzero, blank float64) (int, error) {
	switch bitpix {
	case 8:
		return readAs[uint8](im, pix, bscale, bzero, blank)
	case 16:
		return readAs[int16](im, pix, bscale, bzero, blank)
	case 32:
		return readAs[int32](im, pix, bscale, bzero, blank)
	case 64:
		return readAs[int64](im, pix, bscale, bzero, blank)
	case -32:
		return readAs[float32](im, pix, bscale, bzero, blank)
	case -64:
		return readAs[float64](im, pix, bscale, bzero, blank)
	}

	return 0, fmt.Errorf("%w %d", ErrBitpix, bitpix)
}

func readAs[T uint8 | int16 | int32 | int64 | float32 | float64](im fitsio.Image, pix []float32, bscale, bzero, blank float64) (int, error) {
	n := 1
	for _, a := range im.Header().Axes() {
		n *= a
	}
	px := make([]T, 0, n) // fitsio Read only SetLen()s, caller must provide capacity
	if err := im.Read(&px); err != nil {
		return 0, err
	}
	planes := len(px) / len(pix)
	for p := range planes {
		for i, t := range px[p*len(pix) : (p+1)*len(pix)] {
			if v := float64(t); v == blank {
				pix[i] = float32(math.NaN()) // stays NaN as the other planes add in
			} else {
				pix[i] += float32((v*bscale + bzero) / float64(planes))
			}
		}
	}

	return planes, nil
}

func num(h *fitsio.Header, key string) (float64, bool) {
	c := h.Get(key)
	if c == nil {
		return 0, false
	}
	switch v := c.Value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	}

	return 0, false
}

// first returns the first numeric header value among keys, or NaN.
func first(h *fitsio.Header, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := num(h, k); ok {
			return v
		}
	}

	return math.NaN()
}

// str returns the first non-empty string header value among keys.
func str(h *fitsio.Header, keys ...string) string {
	for _, k := range keys {
		if c := h.Get(k); c != nil {
			if s, ok := c.Value.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}

	return ""
}

// obsTime parses DATE-OBS, zero when absent or unparsable. The standard
// makes it UTC, but Celestron Origin writes local time and the offset in
// TIMEZONE ('+0200'); the result then keeps that zone.
//
// ponytail: only TIMEZONE in ±hhmm form; numeric hour offsets differ in
// sign between programs, so they are not trusted.
func obsTime(h *fitsio.Header) time.Time {
	zone := time.UTC
	if tz, err := time.Parse("-0700", str(h, "TIMEZONE")); err == nil {
		_, off := tz.Zone()
		zone = time.FixedZone(tz.Format("-0700"), off)
	}
	// Parse accepts fractional seconds the layout does not mention.
	t, err := time.ParseInLocation("2006-01-02T15:04:05", str(h, "DATE-OBS"), zone)
	if err != nil {
		return time.Time{}
	}

	return t
}

// blankBorder sets zero pixels connected to the frame edge to NaN.
// Registration (PixInsight StarAlignment, Siril) fills the area no longer
// covered with 0, not NaN; counted as sky it drags the background and noise
// down. Zeros inside the frame, such as calibration clipping, stay data.
//
// ponytail: a frame whose sky is clipped to 0 right up to the edge loses
// that sky too; such a frame is unmeasurable anyway.
func blankBorder(pix []float32, w, h int) {
	var stack []int
	push := func(i int) {
		if pix[i] == 0 {
			pix[i] = float32(math.NaN()) // NaN != 0, so never pushed twice
			stack = append(stack, i)
		}
	}
	for x := range w {
		push(x)
		push((h-1)*w + x)
	}
	for y := range h {
		push(y * w)
		push(y*w + w - 1)
	}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x := i % w
		if x > 0 {
			push(i - 1)
		}
		if x < w-1 {
			push(i + 1)
		}
		if i >= w {
			push(i - w)
		}
		if i+w < len(pix) {
			push(i + w)
		}
	}
}

// cfaSamples returns every other 2x2 cell's R, G and B pixels (both greens),
// a quarter of each colour: plenty for sky statistics. nil when BAYERPAT is
// not a 2x2 pattern of R, G and B.
//
// Programs disagree on which row BAYERPAT starts from: the first stored row
// or the top one, which FITS stores last. Siril, for one, reads it from the
// top unless ROWORDER says otherwise (src/algos/demosaicing.c:75-99), but on
// Celestron Origin frames that pairs green sites 13% apart. The two greens
// see the same sky, so the data decides: the pattern applies from the first
// stored row, shifted by XBAYROFF/YBAYROFF, unless its row mirror (RGGB ↔
// GBRG), which is what the other reading amounts to, makes the greens agree
// at least twice as well; flip reports that. Read the wrong way, red and
// blue sample green.
func cfaSamples(pix []float32, w, h int, hdr *fitsio.Header) ([3][]float32, bool) {
	pat := strings.ToUpper(str(hdr, "BAYERPAT"))
	if len(pat) != 4 || strings.Trim(pat, "RGB") != "" || strings.Count(pat, "G") != 2 {
		return [3][]float32{}, false
	}
	xoff, _ := num(hdr, "XBAYROFF")
	yoff, _ := num(hdr, "YBAYROFF")
	var site [4][]float32 // storage sites (y%2)*2 + x%2, after the offsets
	for s := range site {
		site[s] = make([]float32, 0, (h/4+1)*(w/4+1))
	}
	for y := 0; y+1 < h; y += 4 {
		for x := 0; x+1 < w; x += 4 {
			for _, d := range [4][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				if v := pix[(y+d[1])*w+x+d[0]]; v == v { // skip NaN (blank)
					s := ((y+d[1]+int(yoff))&1)*2 + ((x + d[0] + int(xoff)) & 1)
					site[s] = append(site[s], v)
				}
			}
		}
	}
	flip := false
	// greenGap is how far apart the two sites a pattern calls green are.
	greenGap := func(p string) float32 {
		var g []float32
		for s := range 4 {
			if p[s] == 'G' {
				g = append(g, rough(site[s]))
			}
		}

		return max(g[0]-g[1], g[1]-g[0])
	}
	if mirror := pat[2:] + pat[:2]; greenGap(mirror) < greenGap(pat)/2 {
		pat, flip = mirror, true
	}
	// R and B keep their site's slice; only green joins two.
	var out [3][]float32
	for s, v := range site {
		c := strings.IndexByte("RGB", pat[s])
		if out[c] == nil {
			out[c] = v
		} else {
			out[c] = append(out[c], v...)
		}
	}

	return out, flip
}

// rough is the median of every 16th value: enough to compare sky levels.
func rough(v []float32) float32 {
	var s []float32
	for i := 0; i < len(v); i += 16 {
		s = append(s, v[i])
	}
	if len(s) == 0 {
		return float32(math.NaN())
	}
	slices.Sort(s)

	return s[len(s)/2]
}

// Bin2 averages 2x2 blocks, turning a raw CFA frame into a luminance
// superpixel image so the Bayer grid does not fragment star profiles. A
// superpixel is saturated if any of its four pixels is.
func Bin2(in Image) Image {
	out := Image{W: in.W / 2, H: in.H / 2, Scale: in.Scale * 2, Range: in.Range}
	out.Pix = make([]float32, out.W*out.H)
	if in.Sat != nil {
		out.Sat = make([]bool, out.W*out.H)
	}
	for y := range out.H {
		for x := range out.W {
			i := 2*y*in.W + 2*x
			out.Pix[y*out.W+x] = (in.Pix[i] + in.Pix[i+1] + in.Pix[i+in.W] + in.Pix[i+in.W+1]) / 4
			if in.Sat != nil {
				out.Sat[y*out.W+x] = in.Sat[i] || in.Sat[i+1] || in.Sat[i+in.W] || in.Sat[i+in.W+1]
			}
		}
	}

	return out
}

// Saturation marks pixels within 2% of the image maximum, but only when the
// frame is actually clipped: a plateau of pixels sitting at exactly the max.
// Calibrated frames have no such plateau; analyze catches their clipped
// cores by shape instead.
func Saturation(pix []float32) []bool {
	top := maxOf(pix)
	n := 0
	for _, v := range pix {
		if v == top {
			n++
		}
	}
	if n < 5 {
		return nil
	}
	sat := make([]bool, len(pix))
	for i, v := range pix {
		sat[i] = v >= 0.98*top
	}

	return sat
}

// maxOf is the largest non-NaN value, or -Inf when there is none.
func maxOf(pix []float32) float32 {
	top := float32(math.Inf(-1))
	for _, v := range pix {
		if v > top { // false for NaN, which slices.Max would return
			top = v
		}
	}

	return top
}
