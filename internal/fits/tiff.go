// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package fits

import (
	"bytes"
	"encoding/binary"
	"image"
	"os"
	"strings"

	"codeberg.org/astrogo/fitsio"
	"golang.org/x/image/tiff"
)

// openTIFF reads an 8- or 16-bit grey or RGB TIFF (x/image/tiff: no float
// samples). ASTAP stores the whole FITS header in ImageDescription
// (astap_main.pas:18316); a description of FITS cards becomes the header.
// Rows stay in stored order, top row first, as with XISF.
func openTIFF(path string) (source, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return source{}, err
	}
	m, err := tiff.Decode(bytes.NewReader(b))
	if err != nil {
		return source{}, err
	}
	r := m.Bounds()
	w, h := r.Dx(), r.Dy()
	pix := make([]float32, w*h)
	bitpix, planes := 16, 3
	switch m := m.(type) {
	case *image.Gray16:
		planes = 1
		for i := range pix {
			pix[i] = float32(uint16(m.Pix[2*i])<<8 | uint16(m.Pix[2*i+1]))
		}
	case *image.Gray:
		bitpix, planes = 8, 1
		for i, v := range m.Pix {
			pix[i] = float32(v)
		}
	default: // colour: At gives 16 bits, which 8-bit data repeats in both bytes
		div := float32(3)
		if _, ok := m.(*image.RGBA); ok {
			bitpix, div = 8, 3*257
		} else if _, ok := m.(*image.NRGBA); ok {
			bitpix, div = 8, 3*257
		}
		for y := range h {
			for x := range w {
				cr, cg, cb, _ := m.At(r.Min.X+x, r.Min.Y+y).RGBA()
				pix[y*w+x] = float32(cr+cg+cb) / div
			}
		}
	}
	var cards []fitsio.Card
	for line := range strings.Lines(tiffDescription(b)) {
		cards = append(cards, cardLine(line)...)
	}
	axes := []int{w, h}
	if planes > 1 {
		axes = append(axes, planes)
	}
	read := func() ([]float32, int, error) { return pix, planes, nil }

	return source{hdr: header(cards, bitpix, axes), read: read, done: func() {}}, nil
}

// tiffDescription returns the first IFD's ImageDescription (tag 270), ""
// when there is none.
func tiffDescription(b []byte) string {
	if len(b) < 8 {
		return ""
	}
	var o binary.ByteOrder = binary.LittleEndian
	if string(b[:2]) == "MM" {
		o = binary.BigEndian
	}
	ifd := int(o.Uint32(b[4:]))
	if ifd+2 > len(b) {
		return ""
	}
	n := int(o.Uint16(b[ifd:]))
	for i := range n {
		e := b[ifd+2+12*i:]
		if len(e) < 12 {
			return ""
		}
		if o.Uint16(e) != 270 || o.Uint16(e[2:]) != 2 { // ASCII
			continue
		}
		size := int(o.Uint32(e[4:]))
		start := 8 // the value itself, when it fits in 4 bytes
		if size > 4 {
			start = int(o.Uint32(e[8:]))
			e = b
		}
		if start+size > len(e) {
			return ""
		}

		return strings.TrimRight(string(e[start:start+size]), "\x00")
	}

	return ""
}

// cardLine parses a FITS card written as a text line, "KEY = value /
// comment"; nothing for other text. Comments are dropped: card cuts a
// quoted string at its closing quote, so a slash inside one is safe.
func cardLine(line string) []fitsio.Card {
	key, rest, ok := strings.Cut(line, "=")
	key = strings.TrimSpace(key)
	if !ok || len(key) > 8 || key == "" || strings.ContainsAny(key, " '/") {
		return nil
	}
	if !strings.HasPrefix(strings.TrimSpace(rest), "'") {
		rest, _, _ = strings.Cut(rest, "/")
	}

	return []fitsio.Card{card(key, rest, "")}
}
