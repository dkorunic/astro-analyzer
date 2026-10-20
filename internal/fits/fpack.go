// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package fits

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"reflect"
	"slices"
	"strings"

	"codeberg.org/astrogo/fitsio"
	"github.com/klauspost/compress/gzip"
)

// fpack errors.
var (
	ErrTiled = errors.New("unsupported tile-compressed image")
	ErrRice  = errors.New("corrupt Rice data")
)

// Tile-compressed images (fpack, the FITS tiled image compression
// convention, Pence et al. 2010, FITS standard 4.0 §10): a binary table
// with one row per tile, the original header in its cards with Z-prefixed
// structural keywords. Every ZCMPTYPE is read: RICE_1, GZIP_1, GZIP_2,
// NOCOMPRESS, and HCOMPRESS_1 and PLIO_1 (cfitsio.go), with quantized
// floats restored.

const plio1 = "PLIO_1"

// tileExpansion is the ZCMPTYPEs read and the most an image can exceed
// its file for each, with room to spare: DEFLATE ~1032:1; Rice, whose all-zero
// block of 32 pixels costs 3–5 bits, at most 205:1; NOCOMPRESS 1:1. A
// quantized float64 image doubles its 32-bit integers.
var tileExpansion = map[string]float64{
	"GZIP_1": 4096, "GZIP_2": 4096, "RICE_1": 512, "RICE_ONE": 512, "NOCOMPRESS": 2,
	// A constant tile is a 26-byte HCOMPRESS stream, or one PLIO word per
	// 4095 pixels, whatever its size: these only stop absurd claims.
	"HCOMPRESS_1": 1 << 20, plio1: 1 << 16,
}

// tiled is what openTiled reads from a compressed table's header.
type tiled struct {
	tbl          *fitsio.Table
	cmp, quant   string
	axes, tile   []int
	bitpix       int
	block, bytes int // Rice BLOCKSIZE and BYTEPIX
	dither0      int
	smooth       bool    // HCOMPRESS_1 SMOOTH
	zblank       float64 // NaN when absent
}

// openTiled makes the image header from a compressed table's and reads
// its tiles on read.
//
// fileSize bounds the image: a header claiming more pixels than the codec
// could expand the file into is corrupt (see tileExpansion).
func openTiled(tbl *fitsio.Table, fileSize int64) (source, error) {
	th := tbl.Header()
	n := int(first(th, "ZNAXIS"))
	t := tiled{
		tbl:     tbl,
		cmp:     strings.ToUpper(str(th, "ZCMPTYPE")),
		quant:   strings.ToUpper(str(th, "ZQUANTIZ")),
		bitpix:  int(first(th, "ZBITPIX")),
		dither0: 1,
		zblank:  first(th, "ZBLANK"),
	}
	if n < 2 || n > 999 {
		return source{}, fmt.Errorf("%w: ZNAXIS %d", ErrTiled, n)
	}
	switch t.bitpix {
	case 8, 16, 32, 64, -32, -64:
	default:
		return source{}, fmt.Errorf("%w: ZBITPIX %d", ErrTiled, t.bitpix)
	}
	// Every size is checked before it is used: a corrupt header must fail,
	// not divide by zero, index out of range or allocate what it claims.
	size := float64(abs(t.bitpix) / 8)
	for i := 1; i <= n; i++ {
		a := first(th, fmt.Sprintf("ZNAXIS%d", i))
		z := first(th, fmt.Sprintf("ZTILE%d", i))
		if math.IsNaN(z) { // default: one row per tile
			z = 1
			if i == 1 {
				z = a
			}
		}
		if !(a >= 1 && z >= 1) {
			return source{}, fmt.Errorf("%w: ZNAXIS%d %g, ZTILE%d %g", ErrTiled, i, a, i, z)
		}
		size *= a
		t.axes, t.tile = append(t.axes, int(a)), append(t.tile, int(min(z, a)))
	}
	switch r, ok := tileExpansion[t.cmp]; {
	case !ok:
		return source{}, fmt.Errorf("%w: ZCMPTYPE %q", ErrTiled, t.cmp)
	case size > float64(fileSize)*r:
		return source{}, fmt.Errorf("%w: %s image of %v pixels from %d bytes", ErrTiled, t.cmp, t.axes, fileSize)
	}
	// Quantized floats need ZSCALE, a column or a keyword (FITS standard
	// 4.0 §10.2); without one the tiles hold the raw floats, as astropy
	// writes GZIP_1 with quantize_level=0 and no ZQUANTIZ.
	if t.bitpix < 0 && th.Get("ZSCALE") == nil && !slices.ContainsFunc(tbl.Cols(), func(c fitsio.Column) bool {
		return strings.EqualFold(c.Name, "ZSCALE")
	}) {
		t.quant = "NONE"
	}
	if d, ok := num(th, "ZDITHER0"); ok {
		if !(d >= 1 && d <= nRandom) { // cfitsio's range
			return source{}, fmt.Errorf("%w: ZDITHER0 %g", ErrTiled, d)
		}
		t.dither0 = int(d)
	}
	t.block, t.bytes, t.smooth = params(th, t.bitpix)
	// The original header: the table's cards less its own structure.
	// Restored floats are physical values; integers keep BZERO/BSCALE.
	var cards []fitsio.Card
	for i := range len(th.Keys()) {
		c := th.Card(i)
		if c.Name == "" || tableKey(c.Name) || t.bitpix < 0 && (c.Name == "BZERO" || c.Name == "BSCALE" || c.Name == "BLANK") {
			continue
		}
		cards = append(cards, *c)
	}
	hdr := fitsio.NewHeader(cards, fitsio.IMAGE_HDU, t.bitpix, t.axes)

	return source{hdr: hdr, read: func() ([]float32, int, error) { return t.read(hdr) }}, nil
}

// params reads the codec's ZNAMEn/ZVALn parameters: Rice's BLOCKSIZE (32
// by default) and BYTEPIX (by default the sample size, 4 for quantized
// floats) and HCOMPRESS's SMOOTH.
func params(th *fitsio.Header, bitpix int) (int, int, bool) {
	block, bytes, smooth := 32, 4, false
	if bitpix == 8 || bitpix == 16 {
		bytes = bitpix / 8
	}
	for i := 1; ; i++ {
		name := str(th, fmt.Sprintf("ZNAME%d", i))
		if name == "" {
			return block, bytes, smooth
		}
		v, _ := num(th, fmt.Sprintf("ZVAL%d", i))
		switch strings.ToUpper(name) {
		case "BLOCKSIZE":
			block = int(v)
		case "BYTEPIX":
			bytes = int(v)
		case "SMOOTH": // astropy writes a logical (T), fpack a number (1)
			c := th.Get(fmt.Sprintf("ZVAL%d", i))
			smooth = v != 0 || c != nil && c.Value == true
		}
	}
}

// read decodes every tile into the image, BSCALE/BZERO applied and planes
// averaged, as samples does for an uncompressed image.
func (t tiled) read(hdr *fitsio.Header) ([]float32, int, error) {
	bscale, ok := num(hdr, "BSCALE")
	if !ok {
		bscale = 1
	}
	bzero, _ := num(hdr, "BZERO")
	blank := t.zblank
	if b, ok := num(hdr, "BLANK"); ok && math.IsNaN(blank) {
		blank = b
	}
	w, h := t.axes[0], t.axes[1]
	planes := 1
	for _, a := range t.axes[2:] {
		planes *= a
	}
	pix := make([]float32, w*h)
	cols := map[string]int{}
	for i, c := range t.tbl.Cols() {
		cols[strings.ToUpper(c.Name)] = i
	}
	if _, ok := cols["COMPRESSED_DATA"]; !ok {
		return nil, 0, fmt.Errorf("%w: no COMPRESSED_DATA column", ErrTiled)
	}
	rows, err := t.tbl.Read(0, t.tbl.NumRows())
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	// Tiles run along axis 1 first, then 2, then 3.
	nt := make([]int, len(t.axes))
	for i := range t.axes {
		nt[i] = (t.axes[i] + t.tile[i] - 1) / t.tile[i]
	}
	vals := make([]any, t.tbl.NumCols())
	var data, raw, gz []byte
	// ZSCALE and ZZERO are per-tile columns, or keywords when constant.
	zscale, zzero := first(t.tbl.Header(), "ZSCALE"), first(t.tbl.Header(), "ZZERO")
	if math.IsNaN(zzero) {
		zzero = 0
	}
	// PLIO_1 line lists are 16-bit words (TFORM 1PI): a byte target would
	// get half of them. They go on as big-endian bytes, as fpack wrote them.
	var words []int16
	vals[cols["COMPRESSED_DATA"]] = &data
	if t.cmp == plio1 {
		vals[cols["COMPRESSED_DATA"]] = &words
	}
	if i, ok := cols["UNCOMPRESSED_DATA"]; ok {
		vals[i] = &raw
	}
	if i, ok := cols["GZIP_COMPRESSED_DATA"]; ok {
		vals[i] = &gz
	}
	if i, ok := cols["ZSCALE"]; ok {
		vals[i] = &zscale
	}
	if i, ok := cols["ZZERO"]; ok {
		vals[i] = &zzero
	}
	for i, c := range t.tbl.Cols() { // columns we do not use still need a target
		if vals[i] == nil {
			vals[i] = reflect.New(c.Type()).Interface()
		}
	}
	for row := 0; rows.Next(); row++ {
		data, raw, gz, words = data[:0], raw[:0], gz[:0], words[:0]
		if err := rows.Scan(vals...); err != nil {
			return nil, 0, err
		}
		for _, w := range words {
			data = binary.BigEndian.AppendUint16(data, uint16(w)) //nolint:gosec // the word's bits, as stored
		}
		// The tile's position and size along each axis.
		lo, size, k, npix := make([]int, len(t.axes)), make([]int, len(t.axes)), row, 1
		for i := range t.axes {
			lo[i] = k % nt[i] * t.tile[i]
			k /= nt[i]
			size[i] = min(t.tile[i], t.axes[i]-lo[i])
			npix *= size[i]
		}
		v, stored, err := t.decode(data, raw, gz, npix)
		if err != nil {
			return nil, 0, err
		}
		var dq func(float64) float64
		if !stored {
			dq = t.dequantize(row, zscale, zzero, blank)
		}
		for p := range npix {
			x, y := lo[0]+p%size[0], lo[1]+p/size[0]%size[1] // planes add into one pixel
			var f float64
			switch {
			case dq != nil: // first: nulls advance the dither sequence too
				f = dq(v[p])
			case v[p] == blank:
				f = math.NaN()
			default:
				f = v[p]*bscale + bzero
			}
			pix[y*w+x] += float32(f / float64(planes))
		}
	}

	return pix, planes, rows.Err()
}

// decode returns one tile's stored integers, or its floats for unquantized
// float tiles (GZIP of the raw values). stored is set for a tile in a
// fallback column, which holds the image's own samples, not quantized
// ones: UNCOMPRESSED_DATA, or GZIP_COMPRESSED_DATA, where cfitsio puts a
// float tile it cannot quantize (FITS standard 4.0 §10.4.1).
func (t tiled) decode(data, raw, gz []byte, n int) ([]float64, bool, error) {
	out := make([]float64, n)
	switch {
	case len(data) > 0:
	case len(gz) > 0:
		b, err := gunzip(gz)
		if err != nil {
			return nil, false, err
		}

		return out, true, bigEndian(b, t.bitpix, out)
	case len(raw) > 0: // a tile fpack could not compress
		return out, true, bigEndian(raw, t.bitpix, out)
	}
	out, err := t.decodeCompressed(data, out)

	return out, false, err
}

func gunzip(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	return io.ReadAll(r)
}

// decodeCompressed decodes a tile of the image's ZCMPTYPE into out.
func (t tiled) decodeCompressed(data []byte, out []float64) ([]float64, error) {
	switch t.cmp {
	case "RICE_1", "RICE_ONE":
		return out, rice(data, t.bytes, t.block, out)
	case "NOCOMPRESS":
		return out, bigEndian(data, t.storedBitpix(), out)
	case "HCOMPRESS_1":
		return out, hdecompress(data, t.smooth, out)
	case plio1:
		return out, plio(data, out)
	}
	b, err := gunzip(data)
	if err != nil {
		return nil, err
	}
	bp := t.storedBitpix()
	if t.cmp == "GZIP_2" {
		b = unshuffle(b, abs(bp)/8)
	}

	return out, bigEndian(b, bp, out)
}

// storedBitpix is the BITPIX of the values in a tile: quantized floats are
// stored as 32-bit integers.
func (t tiled) storedBitpix() int {
	if t.bitpix < 0 && t.quant != "NONE" && t.quant != "LOSSLESS" {
		return 32
	}

	return t.bitpix
}

// bigEndian decodes FITS samples of the given BITPIX.
//
//nolint:gosec // two's complement: the unsigned words are signed samples
func bigEndian(b []byte, bitpix int, out []float64) error {
	size := abs(bitpix) / 8
	if len(b) < len(out)*size {
		return fmt.Errorf("%w: short tile", ErrTiled)
	}
	for i := range out {
		s := b[i*size:]
		switch bitpix {
		case 8:
			out[i] = float64(s[0])
		case 16:
			out[i] = float64(int16(binary.BigEndian.Uint16(s)))
		case 32:
			out[i] = float64(int32(binary.BigEndian.Uint32(s)))
		case 64:
			out[i] = float64(int64(binary.BigEndian.Uint64(s)))
		case -32:
			out[i] = float64(math.Float32frombits(binary.BigEndian.Uint32(s)))
		case -64:
			out[i] = math.Float64frombits(binary.BigEndian.Uint64(s))
		}
	}

	return nil
}

// Quantized float tiles: cfitsio imcompress.c unquantize_i4r4 and
// fits_init_randoms. Subtractive dithering adds a uniform random offset
// before rounding and the reader takes the same one away; the sequence is
// the Park–Miller generator, restarted per tile from ZDITHER0.
const (
	nRandom   = 10000
	nullValue = -2147483647 // a NaN pixel
	zeroValue = -2147483646 // an exact 0, SUBTRACTIVE_DITHER_2
)

var randoms = func() [nRandom]float64 {
	var r [nRandom]float64
	seed := 1.0
	for i := range r {
		t := 16807 * seed
		seed = t - 2147483647*math.Trunc(t/2147483647)
		r[i] = float64(float32(seed / 2147483647)) // cfitsio keeps a float table
	}

	return r
}()

// dequantize returns the function restoring a quantized float tile's
// pixels (in order), or nil for integer images and unquantized floats.
//
// blank is ZBLANK, nullValue when NaN (absent). Every pixel, null or not,
// takes the next random number, as cfitsio's unquantize_i4r4 does. The
// explicit float64 conversions keep Go from fusing the multiply and add
// (allowed on arm64): cfitsio rounds each, so a fused 0·scale + zero read
// 3e-16 where cfitsio gives 0.
func (t tiled) dequantize(row int, scale, zero, blank float64) func(float64) float64 {
	if t.storedBitpix() == t.bitpix {
		return nil
	}
	if math.IsNaN(blank) {
		blank = nullValue
	}
	if !strings.HasPrefix(t.quant, "SUBTRACTIVE_DITHER") {
		return func(v float64) float64 {
			if v == blank {
				return math.NaN()
			}

			return float64(v*scale) + zero
		}
	}
	dither2 := t.quant == "SUBTRACTIVE_DITHER_2"
	seed := (row + t.dither0 - 1) % nRandom
	next := int(randoms[seed] * 500)

	return func(v float64) float64 {
		r := randoms[next]
		next++
		if next == nRandom {
			seed = (seed + 1) % nRandom
			next = int(randoms[seed] * 500)
		}
		switch {
		case v == blank:
			return math.NaN()
		case dither2 && v == zeroValue:
			return 0
		}

		return float64((v-r+0.5)*scale) + zero
	}
}

// rice decodes Rice-compressed differences (cfitsio ricecomp.c
// fits_rdecomp, fits_rdecomp_short, fits_rdecomp_byte): the first pixel
// raw, then blocks of nblock differences, each with a code selecting all
// zeros, raw values or a Rice code of that many low bits.
//
//nolint:gosec // bit fields of at most bbits, and two's complement samples
func rice(c []byte, bytepix, nblock int, out []float64) error {
	fs := map[int][2]int{1: {3, 6}, 2: {4, 14}, 4: {5, 25}}[bytepix]
	fsbits, fsmax := fs[0], fs[1]
	if fsbits == 0 {
		return fmt.Errorf("%w: BYTEPIX %d", ErrRice, bytepix)
	}
	bbits := 8 * bytepix
	mask := uint64(1)<<bbits - 1
	if len(c) < bytepix+1 {
		return ErrRice
	}
	var last uint64
	for _, b := range c[:bytepix] {
		last = last<<8 | uint64(b)
	}
	i := bytepix
	next := func() uint64 {
		if i >= len(c) {
			i++

			return 0
		}
		i++

		return uint64(c[i-1])
	}
	signed := func(v uint64) float64 {
		switch bytepix {
		case 1:
			return float64(uint8(v)) // BITPIX 8 is unsigned
		case 2:
			return float64(int16(v))
		}

		return float64(int32(v))
	}
	b := next()
	nbits := 8
	for k := 0; k < len(out); {
		nbits -= fsbits
		for nbits < 0 {
			b = b<<8 | next()
			nbits += 8
		}
		fs := int(b>>nbits) - 1
		b &= 1<<nbits - 1
		end := min(k+nblock, len(out))
		for ; k < end; k++ {
			var diff uint64
			switch {
			case fs < 0: // all differences zero
			case fs == fsmax: // raw differences
				shift := bbits - nbits
				diff = b << shift
				for shift -= 8; shift >= 0; shift -= 8 {
					b = next()
					diff |= b << shift
				}
				if nbits > 0 {
					b = next()
					diff |= b >> -shift
					b &= 1<<nbits - 1
				} else {
					b = 0
				}
			default: // leading zeros (the quotient) then fs bits
				for b == 0 {
					if i > len(c) { // next pads with zeros: corrupt data never ends
						return ErrRice
					}
					nbits += 8
					b = next()
				}
				nzero := nbits - bits.Len64(b)
				nbits -= nzero + 1
				b ^= 1 << nbits
				nbits -= fs
				for nbits < 0 {
					b = b<<8 | next()
					nbits += 8
				}
				diff = uint64(nzero)<<fs | b>>nbits
				b &= 1<<nbits - 1
			}
			// Undo the mapping of signed differences to unsigned.
			if diff&1 == 0 {
				diff >>= 1
			} else {
				diff = ^(diff >> 1)
			}
			last = (diff + last) & mask
			out[k] = signed(last)
		}
		if i > len(c)+1 {
			return ErrRice
		}
	}

	return nil
}

// tableKey reports whether a compressed table's keyword describes the table
// or its compression, not the image.
func tableKey(k string) bool {
	switch k {
	case "XTENSION", "BITPIX", "NAXIS", "NAXIS1", "NAXIS2", "PCOUNT", "GCOUNT", "TFIELDS", "THEAP", "EXTNAME", "END":
		return true
	}
	for _, p := range []string{
		"TTYPE", "TFORM", "TUNIT", "TDIM", "TNULL", "TSCAL", "TZERO", "TDISP",
		"ZIMAGE", "ZBITPIX", "ZNAXIS", "ZTILE", "ZCMPTYPE", "ZNAME", "ZVAL", "ZQUANTIZ", "ZDITHER", "ZBLANK",
		"ZSIMPLE", "ZEXTEND", "ZTENSION", "ZPCOUNT", "ZGCOUNT", "ZHECKSUM", "ZDATASUM", "ZMASKCMP",
	} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}

	return false
}
