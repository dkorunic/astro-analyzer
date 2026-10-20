// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package fits

import (
	"errors"
	"math"
	"testing"
)

// pattern is the fixtures' pixel at (x, y): see testdata/gen.py.
func pattern(x, y int) float64 { return float64((37*x+101*y)%4000 + 1000) }

// TestFormats reads fixtures written by other software (sergio-dr/xisf,
// astropy's tile compression, tifffile) and compares every pixel with the
// pattern they were made from, and the header with what was written.
func TestFormats(t *testing.T) {
	for _, c := range []struct {
		file   string
		offset float64 // added to the pattern
		scale  float64 // the stored value is (pattern + offset) · scale
		tol    float64
		noise  bool // gen.py's ±20 ripple added
	}{
		{file: "xisf_None.xisf", scale: 1},
		{file: "xisf_zlib.xisf", scale: 1},
		{file: "xisf_lz4hc.xisf", scale: 1},
		{file: "xisf_zstd.xisf", scale: 1},
		{file: "xisf_rgb.xisf", offset: 300, scale: 1 / 65535.0, tol: 1e-6}, // the channels' mean
		{file: "rice16.fits.fz", scale: 1},
		{file: "rice32.fits.fz", offset: -3000, scale: 1},
		{file: "gzip2.fits.fz", scale: 1},
		// Subtractive dithering keeps the error within half a step
		// (ZSCALE ≤ 2.14 here); a wrong dither sequence doubles it.
		{file: "float.fits.fz", scale: 1, tol: 1.07, noise: true},
		{file: "gray16.tif", scale: 1},
	} {
		src, err := open("testdata/" + c.file)
		if err != nil {
			t.Errorf("%s: %v", c.file, err)

			continue
		}
		pix, _, err := src.read()
		src.done()
		if err != nil {
			t.Errorf("%s: %v", c.file, err)

			continue
		}
		if a := src.hdr.Axes(); a[0] != 40 || a[1] != 30 || len(pix) != 40*30 {
			t.Errorf("%s: axes %v, %d pixels; want 40 × 30", c.file, a, len(pix))

			continue
		}
		worst := 0.0
		for i, v := range pix {
			want := (pattern(i%40, i/40) + c.offset) * c.scale
			if c.noise {
				want += float64(float32(math.Sin(float64(i%40)*1.3) * math.Cos(float64(i/40)*0.7) * 20))
			}
			if d := math.Abs(float64(v) - want); !(d <= worst) {
				worst = d
			}
		}
		if !(worst <= c.tol) {
			t.Errorf("%s: pixels off by up to %g, want ≤ %g", c.file, worst, c.tol)
		}
		if got := str(src.hdr, "OBJECT"); got != "It's M 31" {
			t.Errorf("%s: OBJECT %q", c.file, got)
		}
		if got := frameType(c.file, src.hdr); got != Light || first(src.hdr, "EXPTIME") != 15 {
			t.Errorf("%s: type %s, EXPTIME %g; want a 15 s light", c.file, got, first(src.hdr, "EXPTIME"))
		}
	}
}

// TestLZ4Block decodes a hand-made block: three literals, a match copying
// them twice over (overlapping its own output), then a last literal.
func TestLZ4Block(t *testing.T) {
	block := []byte{0x32, 'a', 'b', 'c', 3, 0, 0x10, 'Z'}
	if out, err := lz4Block(block, 10); err != nil || string(out) != "abcabcabcZ" {
		t.Errorf("lz4Block = %q, %v; want abcabcabcZ", out, err)
	}
	block[4] = 0 // offset 0 is invalid
	if _, err := lz4Block(block, 10); !errors.Is(err, ErrLZ4) {
		t.Errorf("offset 0: %v, want ErrLZ4", err)
	}
}

// TestRawCards parses dcraw -i -v output.
func TestRawCards(t *testing.T) {
	info := "Timestamp: Sun Jan  8 21:18:13 2017\nCamera: Canon EOS 6D\nISO speed: 1600\n" +
		"Shutter: 1/200.0 sec\nFocal length: 0.0 mm\nFilter pattern: GB/RG\n"
	h := header(rawCards([]byte(info)), 16, []int{4, 2})
	m := meta(h, h.Axes(), Light)
	if m.Date != "2017-01-08T21:18:13" || m.Camera != "Canon EOS 6D" || m.Setting != 1600 ||
		m.Exp != 0.005 || !math.IsNaN(m.FocalLen) || m.Bayer != "GBRG" || !m.CFA {
		t.Errorf("meta %+v", m)
	}
	pix, w, hh, err := pgm([]byte("P5\n2 1\n65535\n\x01\x02\xff\xfe"))
	if err != nil || w != 2 || hh != 1 || pix[0] != 258 || pix[1] != 65534 {
		t.Errorf("pgm = %v %d×%d %v", pix, w, hh, err)
	}
}

func TestSupported(t *testing.T) {
	for name, want := range map[string]bool{
		"a.FITS": true, "a.fits.fz": true, "a.xisf": true, "a.CR2": true, "a.tif": false, "a.jpg": false,
	} {
		if Supported(name) != want {
			t.Errorf("Supported(%q) = %v", name, !want)
		}
	}
}

// TestToADU: PixInsight's [0, 1] masters go back to 16-bit ADU; integer
// and float ADU data stay as they are.
func TestToADU(t *testing.T) {
	for _, c := range []struct {
		pix    []float32
		bitpix int
		want   float32
	}{{pix: []float32{0.5, 1}, bitpix: -32, want: 32767.5}, {pix: []float32{0.5, 900}, bitpix: -32, want: 0.5}, {pix: []float32{0.5, 1}, bitpix: 16, want: 0.5}} {
		if r := toADU(c.pix, c.bitpix); c.pix[0] != c.want || r != 65535 {
			t.Errorf("BITPIX %d: %v, range %v; want %v, 65535", c.bitpix, c.pix[0], r, c.want)
		}
	}
}
