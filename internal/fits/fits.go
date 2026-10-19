// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

// Package fits loads FITS light frames into a float32 luminance image ready
// for star analysis, along with the header context the analysis needs.
package fits

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
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
	Bayer              string
	WCS                WCS    // plate solution, for drizzle dither analysis
	Size               [2]int // sensor pixels, before Bayer binning
	Exp, Temp, Alt     float64
	Plate              float64 // arcsec per original pixel, 0 if unknown
	Aperture, FocalLen float64 // mm
	Lat, Lon           float64 // site, degrees, east positive
	RA, Dec            float64 // target (frame centre), degrees
	CFA                bool    // one-shot-colour (Bayer) sensor
	BayerFlip          bool
}

// Load reads the first 2D/3D image HDU, applies BSCALE/BZERO, averages colour
// planes and 2x2-bins Bayer (CFA) data. Gain comes from EGAIN and plate scale
// from XPIXSZ/FOCALLEN (or CDELT2); either is left 0 when the header lacks it.
func Load(path string) (Image, error) {
	fh, err := os.Open(path)
	if err != nil {
		return Image{}, err
	}
	defer fh.Close()
	f, err := fitsio.Open(bufio.NewReader(fh))
	if err != nil {
		return Image{}, err
	}
	defer f.Close()

	for _, hdu := range f.HDUs() {
		im, ok := hdu.(fitsio.Image)
		axes := hdu.Header().Axes()
		if !ok || len(axes) < 2 || axes[0]*axes[1] == 0 {
			continue
		}
		hdr := hdu.Header()
		// N.I.N.A. writes LIGHT, MaxIm DL "Light Frame"; no IMAGETYP means assume light.
		if t := str(hdr, "IMAGETYP"); t != "" && !strings.Contains(strings.ToUpper(t), "LIGHT") {
			return Image{}, ErrNotLight
		}
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

		w, h := axes[0], axes[1]
		img := Image{W: w, H: h, Pix: make([]float32, w*h), Scale: 1}
		planes, err := readPixels(im, hdr.Bitpix(), img.Pix, bscale, bzero, blank)
		if err != nil {
			return Image{}, err
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
		var flip bool
		if planes == 1 && hdr.Get("BAYERPAT") != nil {
			var colour [3][]float32
			colour, flip = cfaSamples(img.Pix, w, h, hdr)
			img = Bin2(img)
			img.Colour = colour
		}
		g, _ := num(hdr, "EGAIN")
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

		img.Meta = Meta{
			Date:      str(hdr, "DATE-OBS"),
			Time:      obsTime(hdr),
			Filter:    str(hdr, "FILTER"),
			Object:    str(hdr, "OBJECT"),
			Telescope: str(hdr, "TELESCOP"),
			Camera:    str(hdr, "INSTRUME", "CAMERA"),
			Exp:       first(hdr, "EXPTIME", "EXPOSURE"),
			Temp:      first(hdr, "CCD-TEMP"),
			// Not ALTITUDE: that is the observing site's elevation in metres.
			Alt:      first(hdr, "CENTALT", "OBJCTALT"),
			Aperture: first(hdr, "APTDIA", "APERTURE"),
			FocalLen: first(hdr, "FOCALLEN"),
			// ponytail: numeric degrees only; sexagesimal strings
			// ("+45 49 38", MaxIm DL) read as unknown.
			Lat:       first(hdr, "SITELAT", "LATITUDE", "OBSGEO-B"),
			Lon:       first(hdr, "SITELONG", "LONGITUD", "OBSGEO-L"),
			RA:        first(hdr, "CRVAL1", "RA"),
			Dec:       first(hdr, "CRVAL2", "DEC"),
			Size:      [2]int{w, h},
			WCS:       readWCS(hdr),
			CFA:       planes == 1 && hdr.Get("BAYERPAT") != nil,
			Bayer:     strings.ToUpper(str(hdr, "BAYERPAT")),
			BayerFlip: flip,
		}
		// XPIXSZ already includes binning per the FITS convention (MaxIm DL, N.I.N.A.).
		px, _ := num(hdr, "XPIXSZ")
		fl, _ := num(hdr, "FOCALLEN")
		cd := img.Meta.WCS.CD
		det := cd[0][0]*cd[1][1] - cd[0][1]*cd[1][0]
		// A plate solve measures the scale; FOCALLEN is nominal and often off
		// by 1–2%. An unsolved CD (mount pointing) is nominal too, so it
		// ranks after XPIXSZ/FOCALLEN. The CD matrix ranks before a bare
		// CDELT2: with CD cards present, CDELT is ignored (Greisen &
		// Calabretta 2002, §2.1.2), so it may be stale.
		if img.Meta.WCS.OK && det != 0 {
			img.Meta.Plate = math.Sqrt(math.Abs(det)) * 3600
		} else if px > 0 && fl > 0 {
			img.Meta.Plate = 206.265 * px / fl
		} else if det != 0 {
			img.Meta.Plate = math.Sqrt(math.Abs(det)) * 3600
		} else if c2, ok := num(hdr, "CDELT2"); ok {
			img.Meta.Plate = math.Abs(c2) * 3600
		}
		// ponytail: plane-parallel airmass, inaccurate below ~20° altitude
		if am, ok := num(hdr, "AIRMASS"); math.IsNaN(img.Meta.Alt) && ok && am >= 1 {
			img.Meta.Alt = math.Asin(1/am) * 180 / math.Pi
		}

		return img, nil
	}

	return Image{}, ErrNoImage
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
