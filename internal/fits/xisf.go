// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package fits

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"codeberg.org/astrogo/fitsio"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
)

// XISF errors.
var (
	ErrXISF      = errors.New("not an XISF 1.0 file")
	ErrXISFData  = errors.New("unsupported XISF image")
	ErrLZ4       = errors.New("corrupt LZ4 block")
	errShortRead = errors.New("decompressed size differs from the header's")
)

// xisfImage is the first Image element of an XISF header (XISF 1.0
// specification, §8.5 and §11.5): what is needed to decode its pixels,
// and its FITS keywords.
type xisfImage struct {
	Geometry     string `xml:"geometry,attr"`
	SampleFormat string `xml:"sampleFormat,attr"`
	Location     string `xml:"location,attr"`
	Compression  string `xml:"compression,attr"`
	Subblocks    string `xml:"subblocks,attr"`
	ByteOrder    string `xml:"byteOrder,attr"`
	PixelStorage string `xml:"pixelStorage,attr"`
	Keywords     []struct {
		Name    string `xml:"name,attr"`
		Value   string `xml:"value,attr"`
		Comment string `xml:"comment,attr"`
	} `xml:"FITSKeyword"`
	CFA struct {
		Pattern string `xml:"pattern,attr"`
		Width   int    `xml:"width,attr"`
		Height  int    `xml:"height,attr"`
	} `xml:"ColorFilterArray"`
}

// openXISF reads an XISF file's header; its pixels are decoded on read.
// The FITS keywords PixInsight keeps become the header, and a 2×2
// ColorFilterArray becomes BAYERPAT when it has none. Rows stay in stored
// order, top row first, as PixInsight reads FITS (its default origin is the
// upper left), so a FITS file and its XISF copy measure the same.
func openXISF(path string) (source, error) {
	f, err := os.Open(path)
	if err != nil {
		return source{}, err
	}
	var lead [16]byte
	if _, err := io.ReadFull(f, lead[:]); err != nil || string(lead[:8]) != "XISF0100" {
		f.Close()

		return source{}, fmt.Errorf("%w: %s", ErrXISF, path)
	}
	head := make([]byte, binary.LittleEndian.Uint32(lead[8:12]))
	if _, err := io.ReadFull(f, head); err != nil {
		f.Close()

		return source{}, err
	}
	var doc struct {
		Images []xisfImage `xml:"Image"`
	}
	dec := xml.NewDecoder(bytes.NewReader(head))
	// XISF headers are UTF-8 (§11.1); writers label it "utf8" as well as
	// "UTF-8", which encoding/xml only accepts with a CharsetReader.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := dec.Decode(&doc); err != nil {
		f.Close()

		return source{}, err
	}
	if len(doc.Images) == 0 {
		f.Close()

		return source{}, ErrNoImage
	}
	im := doc.Images[0]
	var w, h, ch int
	if n, _ := fmt.Sscanf(im.Geometry, "%d:%d:%d", &w, &h, &ch); n != 3 || w <= 0 || h <= 0 || ch <= 0 {
		f.Close()

		return source{}, fmt.Errorf("%w: geometry %q", ErrXISFData, im.Geometry)
	}
	bitpix, ok := map[string]int{"UInt8": 8, "UInt16": 16, "UInt32": 32, "Float32": -32, "Float64": -64}[im.SampleFormat]
	if !ok {
		f.Close()

		return source{}, fmt.Errorf("%w: sample format %q", ErrXISFData, im.SampleFormat)
	}
	cards := make([]fitsio.Card, 0, len(im.Keywords)+1)
	// Capture software (N.I.N.A.) writes FITS keywords into XISF subs;
	// an image with none is a processed one (PixInsight drops them along
	// the way), so it is skipped rather than measured as a light.
	if len(im.Keywords) == 0 {
		cards = append(cards, fitsio.Card{Name: "IMAGETYP", Value: Processed})
	}
	for _, k := range im.Keywords {
		cards = append(cards, card(k.Name, k.Value, k.Comment))
	}
	if p := strings.ToUpper(im.CFA.Pattern); im.CFA.Width == 2 && im.CFA.Height == 2 && len(p) == 4 && !hasCard(cards, "BAYERPAT") {
		cards = append(cards, fitsio.Card{Name: "BAYERPAT", Value: p})
	}
	axes := []int{w, h}
	if ch > 1 {
		axes = append(axes, ch)
	}
	read := func() ([]float32, int, error) {
		raw, err := xisfBlock(f, im, w*h*ch*abs(bitpix)/8)
		if err != nil {
			return nil, 0, err
		}

		return xisfPixels(raw, im, bitpix, w*h, ch), ch, nil
	}

	return source{hdr: header(cards, bitpix, axes), read: read, done: func() { f.Close() }}, nil
}

// xisfBlock reads the image's attachment and undoes its compression and
// byte shuffling. Inline and embedded data blocks are not supported:
// PixInsight attaches images.
func xisfBlock(f *os.File, im xisfImage, size int) ([]byte, error) {
	var pos, n int64
	if c, _ := fmt.Sscanf(im.Location, "attachment:%d:%d", &pos, &n); c != 2 {
		return nil, fmt.Errorf("%w: location %q", ErrXISFData, im.Location)
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, pos); err != nil {
		return nil, err
	}
	if im.Compression == "" {
		if len(buf) != size {
			return nil, errShortRead
		}

		return buf, nil
	}
	// codec[+sh]:uncompressed-size[:item-size]
	parts := strings.Split(im.Compression, ":")
	codec, shuffle := strings.CutSuffix(parts[0], "+sh")
	item := 1
	if shuffle && len(parts) > 2 {
		item, _ = strconv.Atoi(parts[2])
	}
	// Sub-blocks (compressed,uncompressed:...) are compressed one by one.
	blocks := [][2]int{{len(buf), size}}
	if im.Subblocks != "" {
		blocks = blocks[:0]
		for b := range strings.SplitSeq(im.Subblocks, ":") {
			var c, u int
			if k, _ := fmt.Sscanf(b, "%d,%d", &c, &u); k != 2 {
				return nil, fmt.Errorf("%w: subblocks %q", ErrXISFData, im.Subblocks)
			}
			blocks = append(blocks, [2]int{c, u})
		}
	}
	out := make([]byte, 0, size)
	for _, b := range blocks {
		if b[0] > len(buf) {
			return nil, errShortRead
		}
		d, err := decompress(codec, buf[:b[0]], b[1])
		if err != nil {
			return nil, err
		}
		out, buf = append(out, d...), buf[b[0]:]
	}
	if len(out) != size {
		return nil, errShortRead
	}
	if shuffle && item > 1 {
		out = unshuffle(out, item)
	}

	return out, nil
}

// zstdDec is shared: DecodeAll is safe for concurrent use.
var zstdDec, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))

// decompress undoes one XISF codec: zlib, lz4, lz4hc or zstd.
func decompress(codec string, src []byte, size int) ([]byte, error) {
	switch codec {
	case "zlib":
		r, err := zlib.NewReader(bytes.NewReader(src))
		if err != nil {
			return nil, err
		}
		out := make([]byte, size)
		_, err = io.ReadFull(r, out)

		return out, err
	case "lz4", "lz4hc": // one block format, two encoders
		return lz4Block(src, size)
	case "zstd":
		return zstdDec.DecodeAll(src, make([]byte, 0, size))
	}

	return nil, fmt.Errorf("%w: codec %q", ErrXISFData, codec)
}

// unshuffle undoes XISF byte shuffling: the first bytes of every item come
// first, then the second bytes... Bytes past the last whole item are left
// as they are.
func unshuffle(in []byte, item int) []byte {
	out := make([]byte, len(in))
	n := len(in) / item
	for i := range n {
		for j := range item {
			out[i*item+j] = in[j*n+i]
		}
	}
	copy(out[n*item:], in[n*item:])

	return out
}

// lz4Block decodes one LZ4 block (lz4 block format, Collet), which XISF
// uses without the frame format around it.
func lz4Block(src []byte, size int) ([]byte, error) {
	out := make([]byte, 0, size)
	// length reads LZ4's 4-bit length, extended by bytes while they are 255.
	length := func(i int, n int) (int, int, bool) {
		if n < 15 {
			return n, i, true
		}
		for i < len(src) {
			b := int(src[i])
			i++
			n += b
			if b != 255 {
				return n, i, true
			}
		}

		return 0, i, false
	}
	for i := 0; i < len(src); {
		token := int(src[i])
		lit, j, ok := length(i+1, token>>4)
		if !ok || j+lit > len(src) {
			return nil, ErrLZ4
		}
		out = append(out, src[j:j+lit]...)
		i = j + lit
		if i == len(src) { // the last sequence is literals only
			break
		}
		if i+2 > len(src) {
			return nil, ErrLZ4
		}
		off := int(src[i]) | int(src[i+1])<<8
		ml, k, ok := length(i+2, token&15)
		if !ok || off == 0 || off > len(out) {
			return nil, ErrLZ4
		}
		i = k
		for range ml + 4 { // byte by byte: a match may overlap its own output
			out = append(out, out[len(out)-off])
		}
	}
	if len(out) != size {
		return nil, errShortRead
	}

	return out, nil
}

// xisfPixels decodes n pixels of ch channels, averaging the channels.
// Planar storage (the default) keeps each channel whole; normal storage
// interleaves them per pixel.
func xisfPixels(raw []byte, im xisfImage, bitpix, n, ch int) []float32 {
	var order binary.ByteOrder = binary.LittleEndian
	if im.ByteOrder == "big" {
		order = binary.BigEndian
	}
	size := abs(bitpix) / 8
	sample := func(k int) float32 {
		b := raw[k*size:]
		switch bitpix {
		case 8:
			return float32(b[0])
		case 16:
			return float32(order.Uint16(b))
		case 32:
			return float32(order.Uint32(b))
		case -32:
			return math.Float32frombits(order.Uint32(b))
		}

		return float32(math.Float64frombits(order.Uint64(b)))
	}
	pix := make([]float32, n)
	planar := im.PixelStorage != "Normal"
	for c := range ch {
		for i := range pix {
			k := i*ch + c
			if planar {
				k = c*n + i
			}
			pix[i] += sample(k) / float32(ch)
		}
	}

	return pix
}

// structural cards describe the stored data, which other formats replace:
// header writes its own BITPIX and NAXIS, and samples arrive scaled.
var structural = map[string]bool{
	"SIMPLE": true, "BITPIX": true, "NAXIS": true, "NAXIS1": true, "NAXIS2": true, "NAXIS3": true,
	"EXTEND": true, "BZERO": true, "BSCALE": true, "BLANK": true, "END": true,
}

// header builds a FITS image header from another format's keywords.
func header(cards []fitsio.Card, bitpix int, axes []int) *fitsio.Header {
	keep := make([]fitsio.Card, 0, len(cards))
	for _, c := range cards {
		if c.Name != "" && !structural[c.Name] {
			keep = append(keep, c)
		}
	}

	return fitsio.NewHeader(keep, fitsio.IMAGE_HDU, bitpix, axes)
}

// card turns a FITS keyword written as text (XISF FITSKeyword, a TIFF
// description) into a typed card, as fitsio would decode it: quoted
// strings, T/F, integers, then reals.
func card(name, value, comment string) fitsio.Card {
	c := fitsio.Card{Name: strings.ToUpper(strings.TrimSpace(name)), Comment: comment}
	v := strings.TrimSpace(value)
	if s, ok := strings.CutPrefix(v, "'"); ok {
		s, _, _ = strings.Cut(strings.ReplaceAll(s, "''", "\x00"), "'")
		c.Value = strings.TrimRight(strings.ReplaceAll(s, "\x00", "'"), " ")

		return c
	}
	if v == "T" || v == "F" {
		c.Value = v == "T"

		return c
	}
	if i, err := strconv.Atoi(v); err == nil {
		c.Value = i

		return c
	}
	if f, err := strconv.ParseFloat(strings.Replace(v, "D", "E", 1), 64); err == nil {
		c.Value = f

		return c
	}
	c.Value = v

	return c
}

func hasCard(cards []fitsio.Card, name string) bool {
	for _, c := range cards {
		if c.Name == name {
			return true
		}
	}

	return false
}

func abs(x int) int {
	if x < 0 {
		return -x
	}

	return x
}
