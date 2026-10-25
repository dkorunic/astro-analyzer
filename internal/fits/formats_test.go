// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package fits

import (
	"crypto/sha1" //nolint:gosec // XISF names SHA-1 among its checksums
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dkorunic/astro-analyzer/third_party/fitsio"
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
		scale  float64 // the stored value is ((pattern + offset) mod mod) · scale
		mod    float64 // 256 for 8-bit samples, 0 for none
		tol    float64
		noise  bool // gen.py's ±20 ripple added
	}{
		{file: "xisf_None.xisf", scale: 1},
		{file: "xisf_zlib.xisf", scale: 1},
		{file: "xisf_lz4hc.xisf", scale: 1},
		{file: "xisf_zstd.xisf", scale: 1},
		{file: "xisf_rgb.xisf", offset: 300, scale: 1 / 65535.0, tol: 1e-6}, // the channels' mean
		{file: "xisf_uint8.xisf", scale: 1, mod: 256},
		{file: "xisf_uint32.xisf", offset: 100000, scale: 1},
		{file: "xisf_float64.xisf", scale: 1},
		{file: "rice16.fits.fz", scale: 1},
		{file: "rice32.fits.fz", offset: -3000, scale: 1},
		{file: "gzip2.fits.fz", scale: 1},
		{file: "gzip1.fits.fz", scale: 1},
		{file: "nocompress.fits.fz", scale: 1},
		{file: "rice8.fits.fz", scale: 1, mod: 256},
		{file: "gzip1f64.fits.fz", offset: 0.25, scale: 1}, // lossless float
		// Subtractive dithering keeps the error within half a step
		// (ZSCALE ≤ 2.14 here); a wrong dither sequence doubles it.
		{file: "float.fits.fz", scale: 1, tol: 1.07, noise: true},
		{file: "gray16.tif", scale: 1},
		{file: "gray8.tif", scale: 1, mod: 256},
		{file: "rgb16.tif", offset: 300, scale: 1}, // the channels' mean
		{file: "rgb8.tif", scale: 1, mod: 256},
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
			want := pattern(i%40, i/40) + c.offset
			if c.mod > 0 {
				want = math.Mod(want, c.mod)
			}
			want *= c.scale
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

// TestLibXISF reads libXISF's files (testdata/libxisf: every sample
// format against every codec, plus planar RGB) and compares each pixel with
// the byte pattern they were written from, channels averaged as read does.
func TestLibXISF(t *testing.T) {
	files, err := filepath.Glob("testdata/libxisf/*.xisf")
	if err != nil || len(files) != 40 {
		t.Fatalf("%d libXISF files, %v; want 40", len(files), err)
	}
	for _, f := range files {
		name := filepath.Base(f)
		format, _, _ := strings.Cut(name, "_")
		size := map[string]int{"UInt8": 1, "UInt16": 2, "UInt32": 4, "Float32": 4, "Float64": 8}[format]
		n, ch := 17*11, 1
		if strings.Contains(name, "_RGB_") {
			n, ch = 5*7, 3
		}
		raw := make([]byte, n*ch*size)
		for i := range raw {
			raw[i] = byte(i*37 + i/size*11 + 1)
		}
		want := make([]float32, n)
		for c := range ch {
			for i := range want {
				b := raw[(c*n+i)*size:]
				var v float32
				switch format {
				case "UInt8":
					v = float32(b[0])
				case "UInt16":
					v = float32(binary.LittleEndian.Uint16(b))
				case "UInt32":
					v = float32(binary.LittleEndian.Uint32(b))
				case "Float32":
					v = math.Float32frombits(binary.LittleEndian.Uint32(b))
				case "Float64":
					v = float32(math.Float64frombits(binary.LittleEndian.Uint64(b)))
				}
				want[i] += v / float32(ch)
			}
		}
		src, err := open(f)
		if err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}
		pix, planes, err := src.read()
		src.done()
		if err != nil || planes != ch || len(pix) != n {
			t.Errorf("%s: %d pixels in %d planes, %v; want %d in %d", name, len(pix), planes, err, n, ch)

			continue
		}
		for i := range want {
			if pix[i] != want[i] && !(math.IsNaN(float64(pix[i])) && math.IsNaN(float64(want[i]))) {
				t.Errorf("%s: pixel %d = %g, want %g", name, i, pix[i], want[i])

				break
			}
		}
	}
}

// TestReferenceDecodes reads tile-compressed images from fits-well and
// fitsy (testdata/*/README.md): integer codecs against the arithmetic they
// were written from, quantized floats against cfitsio's own decode, bit for
// bit. A null pixel must still advance the dither sequence, a tile in
// GZIP_COMPRESSED_DATA holds raw floats, and Go must not fuse the
// unquantize multiply-add.
func TestReferenceDecodes(t *testing.T) {
	read := func(name string) []float32 {
		t.Helper()
		src, err := open("testdata/" + name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		defer src.done()
		pix, _, err := src.read()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		return pix
	}
	same := func(name string, got, want []float32) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: %d pixels, want %d", name, len(got), len(want))

			return
		}
		for i := range want {
			if math.Float32bits(got[i]) != math.Float32bits(want[i]) && !(got[i] != got[i] && want[i] != want[i]) {
				t.Errorf("%s: pixel %d = %v, want %v", name, i, got[i], want[i])

				return
			}
		}
	}
	lcg := func(n int, f func(int64) float32) []float32 {
		out, x := make([]float32, n), int64(1)
		for i := range out {
			x = (x*1103515245 + 12345) % (1 << 31)
			out[i] = f(x)
		}

		return out
	}
	ramp := make([]float32, 24*16)
	for i := range ramp {
		ramp[i] = float32(i%24*7 - i/24*5)
	}
	for _, name := range []string{"fitswell/comp_gzip_i16.fits", "fitswell/comp_gzip2_i16.fits", "fitswell/comp_rice_i16.fits", "fitswell/comp_hcomp_i16.fits"} {
		same(name, read(name), ramp)
	}
	// 17 × 17: both halves odd at every level of the inverse H-transform.
	odd := make([]float32, 17*17)
	for i := range odd {
		odd[i] = float32((i*7919+13)%2001 - 1000)
	}
	same("comp_hcomp_odd_i16", read("fitswell/comp_hcomp_odd_i16.fits"), odd)
	mask := make([]float32, 24*16)
	for i := range mask {
		mask[i] = float32((i%24 + i/24) % 7)
	}
	same("comp_plio_i32", read("fitswell/comp_plio_i32.fits"), mask)
	same("ref_hcomp_i32", read("fitsy/ref_hcomp_i32.fits"), lcg(64*64, func(x int64) float32 { return float32(x - 1<<30) }))
	same("ref_rice_i16", read("fitsy/ref_rice_i16.fits"), lcg(33*17, func(x int64) float32 { return float32(x%1000 - 500) }))
	same("ref_rice_i32", read("fitsy/ref_rice_i32.fits"), lcg(40*12, func(x int64) float32 { return float32(x - 1<<30) }))
	nocompress := make([]float32, 48)
	for i := range nocompress {
		nocompress[i] = float32(37*i - 500)
	}
	same("ref_nocompress", read("fitsy/ref_nocompress.fits"), nocompress)
	for _, c := range [][2]string{
		{"comp_ricef_nodither", "comp_ref_f32"},
		{"comp_ricef_quant", "comp_ref_quant_f32"},
		{"comp_dither2_f32", "comp_ref_dither2_f32"},
		{"comp_nan_f32", "comp_ref_nan_f32"},
		// Lossy HCOMPRESS (SCALE 4), also on an odd 17 × 17 tile, and SMOOTH.
		{"comp_hcomp_lossy", "comp_ref_hcomp_lossy"},
		{"comp_hcomp_lossy_odd", "comp_ref_hcomp_lossy_odd"},
		{"comp_hcomp_smooth", "comp_ref_hcomp_smooth"},
	} {
		got, want := read("fitswell/"+c[0]+".fits"), read("fitswell/"+c[1]+".fits")
		same(c[0], got, want)
		if nan := slices.IndexFunc(want, func(v float32) bool { return v != v }); c[0] == "comp_nan_f32" && nan < 0 {
			t.Errorf("%s: reference has no null pixels", c[1])
		}
	}
}

// TestAstropyCodecs reads gen.py's HCOMPRESS_1 and PLIO_1 files against
// astropy's own decode of them (cfitsio): noise on odd tiles, written as
// direct bit planes; lossy HCOMPRESS with SMOOTH, which astropy writes as
// a logical, on noise and on a smooth field, where smoothing moves
// coefficients down as well as up; PLIO line lists with every opcode.
func TestAstropyCodecs(t *testing.T) {
	for _, name := range []string{"hcomp_noise", "hcomp_lossy", "hcomp_smooth", "plio"} {
		got, err := loadPix("testdata/" + name + ".fits.fz")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want, err := loadPix("testdata/" + name + "_ref.fits")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d pixels, want %d", name, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: pixel %d = %v, want %v", name, i, got[i], want[i])

				break
			}
		}
	}
}

// loadPix reads a file's samples.
func loadPix(path string) ([]float32, error) {
	src, err := open(path)
	if err != nil {
		return nil, err
	}
	defer src.done()
	pix, _, err := src.read()

	return pix, err
}

// TestHcompressWide decodes fits-well's 100 × 100 golden stream, whose
// sum exceeds 32 bits and whose coefficients take over 31 bit planes: the
// 64-bit transform (fits_hdecompress64) is needed.
func TestHcompressWide(t *testing.T) {
	stream, err := os.ReadFile("testdata/fitswell/hcomp_wide_i32.huf")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/fitswell/hcomp_wide_i32.raw")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float64, 100*100)
	if err := hdecompress(stream, false, out); err != nil {
		t.Fatal(err)
	}
	for i := range out {
		if want := float64(int32(binary.BigEndian.Uint32(raw[4*i:]))); out[i] != want {
			t.Fatalf("pixel %d = %v, want %v", i, out[i], want)
		}
	}
	if err := hdecompress(stream, false, out[:99*100]); !errors.Is(err, ErrHcompress) {
		t.Errorf("a tile of another size: %v, want ErrHcompress", err)
	}
}

// TestHcompressThin: a stream one pixel wide has empty quadrants, which
// once got a one-byte quadtree buffer, and a quadtree code (0xf) for one
// of them wrote past it. Whatever such a stream decodes to, it must not
// panic.
func TestHcompressThin(t *testing.T) {
	for _, dims := range [][2]uint32{{1, 8}, {8, 1}, {1, 7}, {5, 1}} {
		for b := range 256 {
			s := binary.BigEndian.AppendUint32([]byte{0xDD, 0x99}, dims[0])
			s = binary.BigEndian.AppendUint32(s, dims[1])
			s = binary.BigEndian.AppendUint32(s, 0) // scale
			s = binary.BigEndian.AppendUint64(s, 0) // sum
			s = append(s, 1, 1, 1, 0x00, byte(b), 0xff, 0xff, 0xff, 0xff)
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%d × %d, byte %#02x: panic %v", dims[0], dims[1], b, r)
					}
				}()
				_ = hdecompress(s, false, make([]float64, dims[0]*dims[1]))
			}()
		}
	}
}

// TestRiceRunaway: a Rice code whose quotient runs past the data (all zero
// bits from there) used to spin forever, as reading past the end pads with
// zeros. Code 00001 is a split of 0 bits, then nothing but zeros.
func TestRiceRunaway(t *testing.T) {
	out := make([]float64, 4)
	if err := rice([]byte{0, 0, 0, 0, 0x08, 0, 0, 0}, 4, 32, out); !errors.Is(err, ErrRice) {
		t.Errorf("rice = %v, want ErrRice", err)
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

// TestPairNoiseNormalised: bias frames normalised to [0, 1] give their
// read noise in ADU, as Load gives their PixNoise; read raw it came out
// 65535× too small. Stored v loads as 2v + 10 (writeImage).
func TestPairNoiseNormalised(t *testing.T) {
	const w, h = 600, 256 // 6400 differences: σ good to ~2%
	rng := rand.New(rand.NewPCG(7, 8))
	var paths [2]string
	for k := range paths {
		data := fill[float32](w*h, func(int) float64 {
			v := (1000 + 10*rng.NormFloat64()) / 65535 // 10 ADU of read noise

			return (v - 10) / 2
		})
		paths[k] = filepath.Join(t.TempDir(), "b.fits")
		if err := writeImage(paths[k], -32, w, h, data); err != nil {
			t.Fatal(err)
		}
	}
	if rn, err := PairNoise(paths[0], paths[1]); err != nil || !(math.Abs(rn-10) < 1) {
		t.Errorf("PairNoise = %v, %v; want 10 ADU", rn, err)
	}
}

// TestBitpix round-trips a 4 × 3 frame through every FITS sample type
// with BSCALE = 2 and BZERO = 10: stored v loads as 2v + 10.
func TestBitpix(t *testing.T) {
	const w, h = 4, 3
	stored := func(i int) float64 { return float64(3*i + 1) }
	for _, c := range []struct {
		data   any
		bitpix int
	}{
		{bitpix: 8, data: fill[uint8](w*h, stored)},
		{bitpix: 16, data: fill[int16](w*h, stored)},
		{bitpix: 32, data: fill[int32](w*h, stored)},
		{bitpix: 64, data: fill[int64](w*h, stored)},
		{bitpix: -32, data: fill[float32](w*h, stored)},
		{bitpix: -64, data: fill[float64](w*h, stored)},
	} {
		path := filepath.Join(t.TempDir(), "f.fits")
		if err := writeImage(path, c.bitpix, w, h, c.data); err != nil {
			t.Fatalf("BITPIX %d: %v", c.bitpix, err)
		}
		src, err := open(path)
		if err != nil {
			t.Fatalf("BITPIX %d: %v", c.bitpix, err)
		}
		pix, _, err := src.read()
		src.done()
		if err != nil || len(pix) != w*h {
			t.Fatalf("BITPIX %d: %d pixels, %v", c.bitpix, len(pix), err)
		}
		for i, v := range pix {
			if want := 2*stored(i) + 10; float64(v) != want {
				t.Errorf("BITPIX %d: pixel %d = %g, want %g", c.bitpix, i, v, want)

				break
			}
		}
	}
}

func fill[T uint8 | int16 | int32 | int64 | float32 | float64](n int, v func(int) float64) []T {
	out := make([]T, n)
	for i := range out {
		out[i] = T(v(i))
	}

	return out
}

// writeImage writes one w × h image of the given BITPIX, data being a
// slice of the matching Go type, with BSCALE = 2 and BZERO = 10.
func writeImage(path string, bitpix, w, h int, data any) error {
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	f, err := fitsio.Create(fh)
	if err != nil {
		return err
	}
	img := fitsio.NewImage(bitpix, []int{w, h})
	if err := img.Header().Append(fitsio.Card{Name: "BSCALE", Value: 2}, fitsio.Card{Name: "BZERO", Value: 10}); err != nil {
		return err
	}
	if err := img.Write(data); err != nil {
		return err
	}
	if err := f.Write(img); err != nil {
		return err
	}

	return f.Close()
}

// TestXISFCFA reads hand-made uncompressed XISF headers: a 2×2
// ColorFilterArray becomes BAYERPAT unless a FITS keyword already gives
// one, and an image without FITS keywords is PixInsight output.
func TestXISFCFA(t *testing.T) {
	for _, c := range []struct {
		keywords, bayer, typ string
	}{
		{keywords: `<FITSKeyword name="IMAGETYP" value="'LIGHT'" comment=""/>`, bayer: "RGGB", typ: Light},
		{keywords: `<FITSKeyword name="BAYERPAT" value="'GBRG'" comment=""/>`, bayer: "GBRG", typ: Light},
		{bayer: "RGGB", typ: Processed},
	} {
		path := filepath.Join(t.TempDir(), "cfa.xisf")
		if err := writeXISF(path, xisf2x2, c.keywords+`<ColorFilterArray pattern="rggb" width="2" height="2"/>`); err != nil {
			t.Fatal(err)
		}
		src, err := open(path)
		if err != nil {
			t.Fatal(err)
		}
		pix, _, err := src.read()
		src.done()
		if err != nil || len(pix) != 4 || pix[3] != 4 {
			t.Errorf("pixels %v, %v; want 1 2 3 4", pix, err)
		}
		if got, n := str(src.hdr, "BAYERPAT"), strings.Count(strings.Join(src.hdr.Keys(), " "), "BAYERPAT"); got != c.bayer || n != 1 {
			t.Errorf("%s: BAYERPAT %q in %d cards, want %q in one", c.keywords, got, n, c.bayer)
		}
		if got := frameType(path, src.hdr); got != c.typ {
			t.Errorf("%s: type %s, want %s", c.keywords, got, c.typ)
		}
	}
}

// xisf2x2 is the attributes of writeXISF's 2 × 2 UInt16 image.
const xisf2x2 = `geometry="2:2:1" sampleFormat="UInt16" location="attachment:4096:8"`

// writeXISF writes an Image element with the given attributes and child
// elements, and an 8-byte block holding UInt16 1, 2, 3, 4 attached after a
// 4096-byte header (XISF 1.0, §9: signature, header length, reserved, XML).
func writeXISF(path, attrs, children string) error {
	const at = 4096
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><xisf version="1.0"><Image %s>%s</Image></xisf>`, attrs, children)
	buf := make([]byte, at+8)
	copy(buf, "XISF0100")
	binary.LittleEndian.PutUint32(buf[8:], uint32(len(xml)))
	copy(buf[16:], xml)
	for i := range 4 {
		binary.LittleEndian.PutUint16(buf[at+2*i:], uint16(i+1))
	}

	return os.WriteFile(path, buf, 0o644)
}

// TestXISFHostile: sizes in a corrupt header are numbers, not facts. Each
// must fail to decode rather than panic or allocate what it claims (the
// xisf Rust crate's tests/hostile.rs and codec.rs cases).
func TestXISFHostile(t *testing.T) {
	for _, attrs := range []string{
		`geometry="2:2:1" sampleFormat="UInt16" location="attachment:4096:-8"`,
		`geometry="2:2:1" sampleFormat="UInt16" location="attachment:-1:8"`,
		`geometry="2:2:1" sampleFormat="UInt16" location="attachment:4096:999999999999"`,
		`geometry="2:2:1" sampleFormat="UInt16" location="attachment:9999999999999:8"`,
		`geometry="99999999:99999999:3" sampleFormat="Float64" location="attachment:4096:8"`,
		`geometry="2:2:1" sampleFormat="UInt16" location="attachment:4096:8" compression="zlib:8" subblocks="-1,8"`,
		`geometry="2:2:1" sampleFormat="UInt16" location="attachment:4096:8" compression="zlib:8" subblocks="8,-1"`,
		`geometry="2:2:1" sampleFormat="UInt16" location="attachment:4096:8" compression="zlib:8" subblocks="8,999999999999"`,
		// 18 MB from 8 bytes: past even zlib's best ratio.
		`geometry="3000:3000:1" sampleFormat="UInt16" location="attachment:4096:8" compression="zlib:18000000"`,
		`geometry="2:2:1" sampleFormat="UInt16" location="attachment:4096:8" compression="rot13:8"`,
	} {
		path := filepath.Join(t.TempDir(), "h.xisf")
		if err := writeXISF(path, attrs, ""); err != nil {
			t.Fatal(err)
		}
		if err := openRead(path); err == nil {
			t.Errorf("%s: no error", attrs)
		}
	}
	// A header length past the end of the file.
	path := filepath.Join(t.TempDir(), "h.xisf")
	if err := writeXISF(path, xisf2x2, ""); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(b[8:], 0xffffffff)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := openRead(path); !errors.Is(err, ErrXISF) {
		t.Errorf("header length past the file: %v, want ErrXISF", err)
	}
}

// TestXISFCorrupt truncates a real fixture at every length through its
// header and every 97th byte of its data, and sets every header byte to
// '-' (a negative number) and '9' (a larger one): each must load or fail,
// never panic.
func TestXISFCorrupt(t *testing.T) {
	b, err := os.ReadFile("testdata/xisf_zlib.xisf")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "c.xisf")
	try := func(what string, data []byte) {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s: panic %v", what, r)
			}
		}()
		_ = openRead(path)
	}
	head := 16 + int(binary.LittleEndian.Uint32(b[8:]))
	for n := 0; n < len(b); n += map[bool]int{true: 1, false: 97}[n < head] {
		try(fmt.Sprintf("truncated to %d bytes", n), b[:n])
	}
	for i := 16; i < head; i++ {
		for _, v := range []byte{'-', '9'} {
			c := slices.Clone(b)
			c[i] = v
			try(fmt.Sprintf("byte %d set to %c", i, v), c)
		}
	}
}

// openRead opens path and reads its samples.
func openRead(path string) error {
	src, err := open(path)
	if err != nil {
		return err
	}
	defer src.done()
	_, _, err = src.read()

	return err
}

// TestXISFChecksum: a block's checksum covers its bytes as stored, in any
// of XISF's algorithms and either digit case; a mismatch or an unknown
// algorithm is an error.
func TestXISFChecksum(t *testing.T) {
	block := []byte{1, 0, 2, 0, 3, 0, 4, 0}                                       // writeXISF's samples
	s1, s256, s512 := sha1.Sum(block), sha256.Sum256(block), sha512.Sum512(block) //nolint:gosec // as XISF allows
	s3, s3l := sha3.Sum256(block), sha3.Sum512(block)
	bad := slices.Clone(s256[:])
	bad[0] ^= 1
	for _, c := range []struct {
		checksum string
		want     error
	}{
		{checksum: "sha-1:" + hex.EncodeToString(s1[:])},
		{checksum: "sha1:" + hex.EncodeToString(s1[:])},
		{checksum: "sha256:" + strings.ToUpper(hex.EncodeToString(s256[:]))},
		{checksum: "sha-512:" + hex.EncodeToString(s512[:])},
		{checksum: "sha3-256:" + hex.EncodeToString(s3[:])},
		{checksum: "sha3-512:" + hex.EncodeToString(s3l[:])},
		{checksum: "sha256:" + hex.EncodeToString(bad), want: ErrChecksum},
		{checksum: "sha256:", want: ErrChecksum},
		{checksum: "md5:00", want: ErrXISFData},
	} {
		path := filepath.Join(t.TempDir(), "c.xisf")
		if err := writeXISF(path, xisf2x2+` checksum="`+c.checksum+`"`, ""); err != nil {
			t.Fatal(err)
		}
		if err := openRead(path); !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
			t.Errorf("%s: %v, want %v", c.checksum, err, c.want)
		}
	}
}

// TestFpackCorrupt sets the value field of every card in a compressed
// table's header to '-' and '9', zeroes every 7th data byte (Rice codes
// that run past the data) and truncates every 97th length: each must load
// or fail, never panic or hang. Corrupt headers reach fitsio too, whose
// fork (third_party/fitsio) bounds sizes by the file.
func TestFpackCorrupt(t *testing.T) {
	for _, name := range []string{"rice32.fits.fz", "float.fits.fz", "hcomp_noise.fits.fz", "plio.fits.fz"} {
		fpackCorrupt(t, name)
	}
}

func fpackCorrupt(t *testing.T, name string) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	try := func(what string, data []byte) {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		done := make(chan any, 1)
		go func() {
			defer func() { done <- recover() }()
			_ = openRead(path)
		}()
		select {
		case r := <-done:
			if r != nil {
				t.Errorf("%s: panic %v", what, r)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: no result after 5 s", what)
		}
	}
	for n := 0; n < len(b); n += 97 {
		try(fmt.Sprintf("truncated to %d bytes", n), b[:n])
	}
	const table = 2880 // the table's header follows the empty primary's block
	for i := table; i < 2*table; i++ {
		if col := i % 80; col < 10 || col >= 30 {
			continue
		}
		for _, v := range []byte{'-', '9'} {
			c := slices.Clone(b)
			c[i] = v
			try(fmt.Sprintf("byte %d set to %c", i, v), c)
		}
	}
	for i := 2 * table; i < len(b); i += 7 {
		c := slices.Clone(b)
		c[i] = 0
		try(fmt.Sprintf("data byte %d zeroed", i), c)
	}
}
