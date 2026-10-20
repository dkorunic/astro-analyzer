// Copyright 2015 The astrogo Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fitsio

import (
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
)

type Decoder interface {
	DecodeHDU() (HDU, error)
}

// NewDecoder creates a new Decoder according to the capabilities of the underlying io.Reader.
//
//nolint:ireturn // HDUs differ in type: the interface is the API
func NewDecoder(r io.Reader) Decoder {
	return &streamDecoder{r: r}
}

// streamDecoder is a decoder which can not perform random access
// into the underlying Reader.
type streamDecoder struct {
	r io.Reader
}

// mulSize multiplies sizes, false on a negative operand or overflow.
// (astro-analyzer fork).
func mulSize(a, b int) (int, bool) {
	if a < 0 || b < 0 || b != 0 && a > math.MaxInt/b {
		return 0, false
	}

	return a * b, true
}

//nolint:gocyclo,ireturn // one branch per FITS keyword form; HDUs differ in type, the interface is the API
func (dec *streamDecoder) DecodeHDU() (HDU, error) {
	var err error
	var hdu HDU

	cards := make(map[string]int, 30)
	slice := make([]Card, 0, 1)

	getCard := func(k string) (Card, bool) {
		i, ok := cards[k]
		if ok {
			return slice[i], ok
		}

		return Card{}, ok
	}

	addCard := func(c *Card) {
		n := c.Name
		if n == kwComment || n == kwHistory || n == "" {
			slice = append(slice, *c)

			return
		}
		// For compatibility with C FITSIO, silently swallow duplicate card keys.
		// See:
		//  https://codeberg.org/astrogo/fitsio/issues/38
		//
		// _, dup := cards[n]
		// if dup {
		// 	panic(fmt.Errorf("%w: duplicate keyword [%s]", ErrFITS, n))
		// }
		cards[n] = len(slice)
		slice = append(slice, *c)
	}

	axes := []int{}
	buf := make([]byte, blockSize)

	iblock := -1
blocks_loop:
	for {
		iblock++
		_, err = io.ReadFull(dec.r, buf)
		if err != nil {
			return nil, err
		}

		// each FITS header block is comprised of up to 36 80-byte lines
		const maxlines = 36
		for i := range maxlines {
			card, err := parseHeaderLine(buf[i*80 : (i+1)*80])
			if err != nil {
				return nil, err
			}
			if card.Name == "CONTINUE" {
				idx := len(slice) - 1
				if idx < 0 {
					return nil, fmt.Errorf("%w: CONTINUE card without a card to continue", ErrFITS)
				}
				last := slice[idx]
				str, ok := last.Value.(string)
				if !ok {
					return nil, fmt.Errorf("%w: CONTINUE card after non-string card %q", ErrFITS, last.Name)
				}
				if len(str) > 0 {
					last.Value = str[:len(str)-1] + card.Comment
				}
				slice[idx] = last

				continue
			}
			addCard(card)
			if card.Name == kwEnd {
				break
			}
		}

		_, ends := getCard(kwEnd)
		if ends { //nolint:nestif // upstream's parser: one branch per FITS type or keyword form
			card, ok := getCard(kwNaxis)
			if ok {
				n, ok := card.Value.(int)
				if !ok || n < 0 || n > 999 {
					return nil, fmt.Errorf("%w: invalid NAXIS %v", ErrFITS, card.Value)
				}
				axes = make([]int, n)
				for i := range n {
					k := fmt.Sprintf("NAXIS%d", i+1)
					c, ok := getCard(k)
					if !ok {
						return nil, fmt.Errorf("%w: missing '%s' key", ErrFITS, k)
					}
					if axes[i], ok = c.Value.(int); !ok || axes[i] < 0 {
						return nil, fmt.Errorf("%w: invalid %s %v", ErrFITS, k, c.Value)
					}
				}
			}

			break blocks_loop
		}
	}

	htype, primary, err := hduTypeFrom(slice)
	if err != nil {
		return nil, err
	}

	var bitpix int
	if card, ok := getCard(kwBitpix); ok {
		v := reflect.ValueOf(card.Value)
		if !v.CanInt() {
			return nil, fmt.Errorf("%w: invalid BITPIX %v", ErrFITS, card.Value)
		}
		bitpix = int(v.Int())
	} else {
		return nil, fmt.Errorf("%w: missing 'BITPIX' card", ErrFITS)
	}

	hdr := NewHeader(slice, htype, bitpix, axes)
	switch htype {
	case HDUImage:
		var data []byte
		data, err = dec.loadImage(hdr)
		if err != nil {
			return nil, fmt.Errorf("%w: error loading image: %w", ErrFITS, err)
		}

		switch primary {
		case true:
			hdu = &primaryHDU{
				hdr: *hdr,
				raw: data,
			}
		case false:
			hdu = &ImageHDU{
				hdr: *hdr,
				raw: data,
			}
		}

	case HDUBinaryTable:
		hdu, err = dec.loadTable(hdr, htype)
		if err != nil {
			return nil, fmt.Errorf("%w: error loading binary table: %w", ErrFITS, err)
		}

	case HDUASCIITable:
		hdu, err = dec.loadTable(hdr, htype)
		if err != nil {
			return nil, fmt.Errorf("%w: error loading ascii table: %w", ErrFITS, err)
		}

	case HDUAny:
		fallthrough
	default:
		return nil, fmt.Errorf("%w: invalid HDU Type (%v)", ErrFITS, htype)
	}

	return hdu, err
}

// alloc returns a buffer of n bytes for data still to be read, or an
// error when n is negative or, with an *io.LimitedReader, more than the
// reader has left: a corrupt header must fail, not allocate what it
// claims. (astro-analyzer fork).
func (dec *streamDecoder) alloc(n int) ([]byte, error) {
	if lr, ok := dec.r.(*io.LimitedReader); n < 0 || ok && int64(n) > lr.N {
		return nil, fmt.Errorf("%w: header asks for %d bytes, more than the file holds", ErrFITS, n)
	}

	return make([]byte, n), nil
}

func (dec *streamDecoder) loadImage(hdr *Header) ([]byte, error) {
	var err error
	var buf []byte

	nelmts := 1
	for _, dim := range hdr.Axes() {
		var ok bool
		if nelmts, ok = mulSize(nelmts, dim); !ok {
			return nil, fmt.Errorf("%w: image size %v overflows", ErrFITS, hdr.Axes())
		}
	}

	if len(hdr.Axes()) == 0 {
		nelmts = 0
	}

	pixsz := hdr.Bitpix() / 8
	if pixsz < 0 {
		pixsz = -pixsz
	}

	size, ok := mulSize(nelmts, pixsz)
	if !ok {
		return nil, fmt.Errorf("%w: image size %v overflows", ErrFITS, hdr.Axes())
	}
	buf, err = dec.alloc(size)
	if err != nil {
		return nil, err
	}
	if nelmts == 0 {
		return buf, nil
	}

	n, err := io.ReadFull(dec.r, buf)
	if err != nil {
		return nil, fmt.Errorf("%w: error reading %d bytes (got %d): %w", ErrFITS, len(buf), n, err)
	}

	// data array is also aligned at 2880-bytes blocks
	pad := padBlock(n)
	if pad > 0 {
		if n, err := io.CopyN(io.Discard, dec.r, int64(pad)); err != nil {
			return nil, fmt.Errorf("%w: error reading %d bytes (got %d): %w", ErrFITS, pad, n, err)
		}
	}

	return buf, err
}

//nolint:gocyclo,maintidx // upstream's parser: one branch per FITS type or keyword form
func (dec *streamDecoder) loadTable(hdr *Header, htype HDUType) (*Table, error) {
	var err error
	var table *Table

	var isbinary bool
	switch htype { //nolint:exhaustive // the default case handles the other values
	case HDUASCIITable:
		isbinary = false
	case HDUBinaryTable:
		isbinary = true
	default:
		return nil, fmt.Errorf("%w: invalid HDU type (%v)", ErrFITS, htype)
	}

	if len(hdr.Axes()) != 2 {
		return nil, fmt.Errorf("%w: table with NAXIS %d, want 2", ErrFITS, len(hdr.Axes()))
	}
	rowsz := hdr.Axes()[0]
	nrows := int64(hdr.Axes()[1])
	// intCard is an optional non-negative integer card, 0 when absent.
	intCard := func(name string) (int, error) {
		card := hdr.Get(name)
		if card == nil || card.Value == nil {
			return 0, nil
		}
		v, ok := card.Value.(int)
		if !ok || v < 0 {
			return 0, fmt.Errorf("%w: invalid %s %v", ErrFITS, name, card.Value)
		}

		return v, nil
	}
	ncols, err := intCard("TFIELDS")
	if err != nil {
		return nil, err
	}
	if ncols > 999 {
		return nil, fmt.Errorf("%w: invalid TFIELDS %d", ErrFITS, ncols)
	}

	datasz, ok := mulSize(int(nrows), rowsz)
	if !ok {
		return nil, fmt.Errorf("%w: table size %d × %d overflows", ErrFITS, rowsz, nrows)
	}
	heapsz, err := intCard("PCOUNT")
	if err != nil {
		return nil, err
	}
	if datasz > math.MaxInt-heapsz-blockSize {
		return nil, fmt.Errorf("%w: table size %d + %d overflows", ErrFITS, datasz, heapsz)
	}

	blocksz := alignBlock(datasz + heapsz)

	block, err := dec.alloc(blocksz)
	if err != nil {
		return nil, err
	}
	n, err := io.ReadFull(dec.r, block)
	if err != nil {
		return nil, fmt.Errorf("%w: error reading %d bytes (got %d): %w", ErrFITS, len(block), n, err)
	}

	// THEAP is the heap's offset from the table's start (FITS standard 4.0
	// §7.3.5), so at least datasz; fitsio itself writes 0 for "no gap",
	// read as before. The heap is PCOUNT bytes less the gap, not the
	// block's padding after it. (astro-analyzer fork)
	theap, err := intCard("THEAP")
	if err != nil {
		return nil, err
	}
	if theap < datasz {
		theap += datasz
	}
	if theap > datasz+heapsz {
		return nil, fmt.Errorf("%w: THEAP %d past the table's %d bytes", ErrFITS, theap, datasz+heapsz)
	}

	data := block[:datasz]
	heap := block[theap : datasz+heapsz]

	cols := make([]Column, ncols)
	colidx := make(map[string]int, ncols)

	get := func(str string, ii int) *Card {
		return hdr.Get(fmt.Sprintf(str+"%d", ii+1))
	}

	offset := 0
	for i := range ncols {
		col := &cols[i]

		switch htype { //nolint:exhaustive // the default case handles the other values
		case HDUBinaryTable:
			col.write = col.writeBin
			col.read = col.readBin
		case HDUASCIITable:
			col.write = col.writeTxt
			col.read = col.readTxt
		default:
			return nil, fmt.Errorf("%w: invalid HDUType (%v)", ErrFITS, htype)
		}

		col.offset = offset

		// TTYPE is optional (FITS standard 4.0 §7.3.2); TFORM must be a
		// string, as must TUNIT. (astro-analyzer fork)
		card := get("TTYPE", i)
		if card != nil {
			col.Name, _ = card.Value.(string)
		}

		card = get("TFORM", i)
		if card == nil {
			return nil, fmt.Errorf("%w: missing 'TFORM%d' for column '%s'", ErrFITS, i+1, col.Name)
		} else if col.Format, ok = card.Value.(string); !ok {
			return nil, fmt.Errorf("%w: TFORM%d %v is not a string", ErrFITS, i+1, card.Value)
		}

		card = get("TUNIT", i)
		if card != nil && card.Value != nil {
			col.Unit, _ = card.Value.(string)
		}

		card = get("TNULL", i)
		if card != nil && card.Value != nil {
			col.Null = fmt.Sprintf("%v", card.Value)
		}

		card = get("TSCAL", i)
		if card != nil && card.Value != nil {
			switch vv := card.Value.(type) {
			case float64:
				col.Bscale = vv
			case int64:
				col.Bscale = float64(vv)
			case int:
				col.Bscale = float64(vv)
			default:
				return nil, fmt.Errorf("%w: unhandled type [%T]", ErrFITS, vv)
			}
		} else {
			col.Bscale = 1.0
		}

		card = get("TZERO", i)
		if card != nil && card.Value != nil {
			switch vv := card.Value.(type) {
			case float64:
				col.Bzero = vv
			case int64:
				col.Bzero = float64(vv)
			case int:
				col.Bzero = float64(vv)
			default:
				return nil, fmt.Errorf("%w: unhandled type [%T]", ErrFITS, vv)
			}
		} else {
			col.Bzero = 0.0
		}

		card = get("TDISP", i)
		if card != nil && card.Value != nil {
			col.Display, _ = card.Value.(string)
		}

		card = get("TDIM", i)
		if card != nil && card.Value != nil {
			dims, _ := card.Value.(string)
			dims = strings.ReplaceAll(dims, "(", "")
			dims = strings.ReplaceAll(dims, ")", "")
			toks := make([]string, 0)
			for tok := range strings.SplitSeq(dims, ",") {
				tok = strings.Trim(tok, " \t\n")
				if tok == "" {
					continue
				}
				toks = append(toks, tok)
			}
			col.Dim = make([]int64, 0, len(toks))
			for _, tok := range toks {
				dim, err := strconv.ParseInt(tok, 10, 64)
				if err != nil {
					return nil, err
				}
				col.Dim = append(col.Dim, dim)
			}
		}

		card = get("TBCOL", i)
		if card != nil && card.Value != nil {
			start, ok := card.Value.(int)
			if !ok {
				return nil, fmt.Errorf("%w: TBCOL%d %v is not an integer", ErrFITS, i+1, card.Value)
			}
			col.Start = int64(start)
		}

		col.dtype, err = typeFromForm(col.Format, htype)
		if err != nil {
			return nil, err
		}
		offset += col.dtype.dsize * col.dtype.len
		if htype == HDUASCIITable {
			col.txtfmt = txtfmtFromForm(col.Format)
		}
		colidx[col.Name] = i
	}

	table = &Table{
		hdr:    *hdr,
		binary: isbinary,
		data:   data,
		heap:   heap,
		rowsz:  rowsz,
		nrows:  nrows,
		cols:   cols,
		colidx: colidx,
	}

	return table, err
}

func hduTypeFrom(cards []Card) (HDUType, bool, error) {
	var err error
	var htype HDUType = -1
	var primary bool

	keys := make([]string, 0, len(cards))
	for _, card := range cards {
		keys = append(keys, card.Name)
		switch card.Name {
		case kwSimple:
			primary = true

			return HDUImage, primary, nil
		case kwXtension:
			str, ok := card.Value.(string)
			if !ok {
				return htype, primary, fmt.Errorf("%w: XTENSION %v is not a string", ErrFITS, card.Value)
			}
			switch str {
			case "IMAGE":
				htype = HDUImage
			case "TABLE":
				htype = HDUASCIITable
			case xtBintable:
				htype = HDUBinaryTable
			case "ANY", "ANY_HDU":
				htype = HDUAny
			default:
				return htype, primary, fmt.Errorf("%w: invalid 'XTENSION' value: %q", ErrFITS, str)
			}

			return htype, primary, err
		}
	}

	return htype, primary, fmt.Errorf("%w: invalid header (missing 'SIMPLE' or 'XTENSION' card): keys=%v", ErrFITS, keys)
}
