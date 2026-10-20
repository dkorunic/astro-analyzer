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
// structural keywords. RICE_1, GZIP_1, GZIP_2 and NOCOMPRESS are read,
// with quantized floats restored; HCOMPRESS_1 and PLIO_1 are not.

// tiled is what openTiled reads from a compressed table's header.
type tiled struct {
	tbl          *fitsio.Table
	cmp, quant   string
	axes, tile   []int
	bitpix       int
	block, bytes int // Rice BLOCKSIZE and BYTEPIX
	dither0      int
	zblank       float64 // NaN when absent
}

// openTiled makes the image header from a compressed table's and reads
// its tiles on read.
func openTiled(tbl *fitsio.Table) (source, error) {
	th := tbl.Header()
	n := int(first(th, "ZNAXIS"))
	t := tiled{
		tbl:     tbl,
		cmp:     strings.ToUpper(str(th, "ZCMPTYPE")),
		quant:   strings.ToUpper(str(th, "ZQUANTIZ")),
		bitpix:  int(first(th, "ZBITPIX")),
		block:   32,
		dither0: 1,
		zblank:  first(th, "ZBLANK"),
	}
	if n < 2 {
		return source{}, fmt.Errorf("%w: ZNAXIS %d", ErrTiled, n)
	}
	for i := 1; i <= n; i++ {
		t.axes = append(t.axes, int(first(th, fmt.Sprintf("ZNAXIS%d", i))))
		z := first(th, fmt.Sprintf("ZTILE%d", i))
		if math.IsNaN(z) { // default: one row per tile
			z = 1
			if i == 1 {
				z = float64(t.axes[0])
			}
		}
		t.tile = append(t.tile, int(z))
	}
	if d, ok := num(th, "ZDITHER0"); ok {
		t.dither0 = int(d)
	}
	t.bytes = 4
	if t.bitpix == 8 || t.bitpix == 16 {
		t.bytes = t.bitpix / 8
	}
	for i := 1; ; i++ {
		name := str(th, fmt.Sprintf("ZNAME%d", i))
		if name == "" {
			break
		}
		v, _ := num(th, fmt.Sprintf("ZVAL%d", i))
		switch strings.ToUpper(name) {
		case "BLOCKSIZE":
			t.block = int(v)
		case "BYTEPIX":
			t.bytes = int(v)
		}
	}
	switch t.cmp {
	case "RICE_1", "RICE_ONE", "GZIP_1", "GZIP_2", "NOCOMPRESS":
	default:
		return source{}, fmt.Errorf("%w: ZCMPTYPE %q", ErrTiled, t.cmp)
	}
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
	var data, raw []byte
	var zscale, zzero float64
	vals[cols["COMPRESSED_DATA"]] = &data
	if i, ok := cols["UNCOMPRESSED_DATA"]; ok {
		vals[i] = &raw
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
		data, raw = data[:0], raw[:0]
		if err := rows.Scan(vals...); err != nil {
			return nil, 0, err
		}
		// The tile's position and size along each axis.
		lo, size, k, npix := make([]int, len(t.axes)), make([]int, len(t.axes)), row, 1
		for i := range t.axes {
			lo[i] = k % nt[i] * t.tile[i]
			k /= nt[i]
			size[i] = min(t.tile[i], t.axes[i]-lo[i])
			npix *= size[i]
		}
		v, err := t.decode(data, raw, npix)
		if err != nil {
			return nil, 0, err
		}
		dq := t.dequantize(row, zscale, zzero)
		for p := range npix {
			x, y := lo[0]+p%size[0], lo[1]+p/size[0]%size[1] // planes add into one pixel
			var f float64
			switch {
			case v[p] == blank:
				f = math.NaN()
			case dq != nil:
				f = dq(v[p])
			default:
				f = v[p]*bscale + bzero
			}
			pix[y*w+x] += float32(f / float64(planes))
		}
	}

	return pix, planes, rows.Err()
}

// decode returns one tile's stored integers (or floats, for unquantized
// float tiles: GZIP of the raw values, or UNCOMPRESSED_DATA).
func (t tiled) decode(data, raw []byte, n int) ([]float64, error) {
	out := make([]float64, n)
	if len(data) == 0 && len(raw) > 0 { // a tile fpack could not compress
		return out, bigEndian(raw, t.bitpix, out)
	}
	switch t.cmp {
	case "RICE_1", "RICE_ONE":
		return out, rice(data, t.bytes, t.block, out)
	case "NOCOMPRESS":
		return out, bigEndian(data, t.storedBitpix(), out)
	}
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(r)
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
		r[i] = seed / 2147483647
	}

	return r
}()

// dequantize returns the function restoring a quantized float tile's
// pixels (in order), or nil for integer images and unquantized floats.
func (t tiled) dequantize(row int, scale, zero float64) func(float64) float64 {
	if t.storedBitpix() == t.bitpix {
		return nil
	}
	if !strings.HasPrefix(t.quant, "SUBTRACTIVE_DITHER") {
		return func(v float64) float64 {
			if v == nullValue {
				return math.NaN()
			}

			return v*scale + zero
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
		case v == nullValue:
			return math.NaN()
		case dither2 && v == zeroValue:
			return 0
		}

		return (v-r+0.5)*scale + zero
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
