// Copyright 2015 The astrogo Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fitsio

import (
	"bytes"
	"fmt"
	"io"
)

type Encoder interface {
	EncodeHDU(hdu HDU) error
}

// NewEncoder creates a new Encoder according to the capabilities of the underlying io.Writer.
//
//nolint:ireturn // HDUs differ in type: the interface is the API
func NewEncoder(w io.Writer) Encoder {
	return &streamEncoder{w: w}
}

// streamEncoder is a encoder which can not perform random access
// into the underlying Writer.
type streamEncoder struct {
	w io.Writer
}

func (enc *streamEncoder) EncodeHDU(hdu HDU) error {
	var err error
	const (
		valind  = "= "
		nKEY    = 8
		nVALIND = 2
		nVAL    = 30
		nCOM    = 40
		nLINE   = 80
	)

	hdr := hdu.Header()
	nkeys := len(hdr.cards)
	buf := new(bytes.Buffer)

	buf.Grow(nkeys * nLINE)

	for i := range hdr.cards {
		card := &hdr.cards[i]
		bline, err := makeHeaderLine(card)
		if err != nil {
			return err
		}
		_, err = buf.Write(bline)
		if err != nil {
			return err
		}
	}

	{ // END
		bline, err := makeHeaderLine(&Card{Name: kwEnd})
		if err != nil {
			return err
		}
		_, err = buf.Write(bline)
		if err != nil {
			return err
		}
	}

	padsz := padBlock(buf.Len())
	if padsz > 0 {
		n, err := buf.Write(bytes.Repeat([]byte(" "), padsz))
		if err != nil {
			return fmt.Errorf("%w: error while padding header block: %w", ErrFITS, err)
		}
		if n != padsz {
			return fmt.Errorf("%w: wrote %d bytes. expected %d. (padding)", ErrFITS, n, padsz)
		}
	}

	alignsz := alignBlock(buf.Len())
	if alignsz != buf.Len() {
		return fmt.Errorf("%w: header not aligned (%d), expected %d", ErrFITS, buf.Len(), alignsz)
	}

	n, err := io.Copy(enc.w, buf)
	if err != nil {
		return fmt.Errorf("%w: error writing header block: %w", ErrFITS, err)
	}
	if n != int64(alignsz) {
		return fmt.Errorf("%w: wrote %d bytes. expected %d", ErrFITS, n, alignsz)
	}

	// write payload
	switch hdr.Type() {
	case HDUImage:
		img, ok := hdu.(Image)
		if !ok {
			return fmt.Errorf("%w: image HDU of type %T", ErrFITS, hdu)
		}
		err = enc.saveImage(img)
		if err != nil {
			return fmt.Errorf("%w: error encoding image: %w", ErrFITS, err)
		}

	case HDUBinaryTable:
		tbl, ok := hdu.(*Table)
		if !ok {
			return fmt.Errorf("%w: table HDU of type %T", ErrFITS, hdu)
		}
		err = enc.saveTable(tbl)
		if err != nil {
			return fmt.Errorf("%w: error encoding binary table: %w", ErrFITS, err)
		}

	case HDUASCIITable:
		tbl, ok := hdu.(*Table)
		if !ok {
			return fmt.Errorf("%w: table HDU of type %T", ErrFITS, hdu)
		}
		err = enc.saveTable(tbl)
		if err != nil {
			return fmt.Errorf("%w: error encoding ascii table: %w", ErrFITS, err)
		}

	case HDUAny:
		fallthrough
	default:
		return fmt.Errorf("%w: encoding for HDU [%v] not implemented", ErrFITS, hdr.Type())
	}

	return err
}

func (enc *streamEncoder) saveImage(img Image) error {
	raw := img.Raw()
	n, err := enc.w.Write(raw)
	if err != nil {
		return err
	}
	if n != len(raw) {
		return fmt.Errorf("%w: wrote %d bytes. expected %d", ErrFITS, n, len(raw))
	}

	padsz := padBlock(n)
	if padsz > 0 {
		n, err := enc.w.Write(make([]byte, padsz))
		if err != nil {
			return fmt.Errorf("%w: error while padding data-image block: %w", ErrFITS, err)
		}
		if n != padsz {
			return fmt.Errorf("%w: wrote %d bytes. expected %d. (padding)", ErrFITS, n, padsz)
		}
	}

	return err
}

func (enc *streamEncoder) saveTable(table *Table) error {
	ndata, err := enc.w.Write(table.data)
	if err != nil {
		return fmt.Errorf("%w: error writing table-data: %w", ErrFITS, err)
	}
	if ndata != len(table.data) {
		return fmt.Errorf("%w: wrote %d bytes. expected %d", ErrFITS, ndata, len(table.data))
	}

	nheap, err := enc.w.Write(table.heap)
	if err != nil {
		return fmt.Errorf("%w: error writing table-heap: %w", ErrFITS, err)
	}
	if nheap != len(table.heap) {
		return fmt.Errorf("%w: wrote %d bytes. expected %d", ErrFITS, nheap, len(table.heap))
	}

	// align to FITS block
	padsz := padBlock(ndata + nheap)
	if padsz > 0 {
		var n int

		if table.binary {
			n, err = enc.w.Write(make([]byte, padsz))
		} else {
			n, err = enc.w.Write(bytes.Repeat([]byte(" "), padsz))
		}
		if err != nil {
			return fmt.Errorf("%w: error while padding table-data block: %w", ErrFITS, err)
		}
		if n != padsz {
			return fmt.Errorf("%w: wrote %d bytes. expected %d. (padding)", ErrFITS, n, padsz)
		}
	}

	return err
}
