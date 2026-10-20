// Copyright 2015 The astrogo Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fitsio

import (
	"fmt"
)

// HDUType is the type of a Header-Data Unit.
type HDUType int

const (
	HDUImage       HDUType = iota // Primary Array or IMAGE HDU
	HDUASCIITable                 // ASCII table HDU
	HDUBinaryTable                // Binary table HDU
	HDUAny                        // matches any HDU type
)

func (htype HDUType) String() string {
	switch htype {
	case HDUImage:
		return "IMAGE"
	case HDUASCIITable:
		return "TABLE"
	case HDUBinaryTable:
		return xtBintable
	case HDUAny:
		return "ANY_HDU"
	default:
		panic(fmt.Errorf("%w: invalid HDU Type value (%v)", ErrFITS, int(htype)))
	}
}

// HDU is a "Header-Data Unit" block.
type HDU interface {
	Close() error
	Type() HDUType
	Name() string
	Version() int
	Header() *Header
}

// CopyHDU copies the i-th HDU from the src FITS file into the dst one.
func CopyHDU(dst, src *File, i int) error {
	// The HDU is decoded and re-encoded; copying its raw bytes would be
	// faster.
	hdu := src.HDU(i)

	return dst.Write(hdu)
}
