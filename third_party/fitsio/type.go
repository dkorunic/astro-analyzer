// Copyright 2015 The astrogo Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fitsio

import (
	"reflect"
)

// typecode represents FITS types.
type typecode int

const (
	tcInvalid typecode = iota
	tcByte
	tcBool
	tcString
	tcUint16
	tcUint32
	tcUint64
	tcInt8
	tcInt16
	tcInt32
	tcInt64
	tcFloat32
	tcFloat64
	tcComplex64
	tcComplex128
)

// Type describes a FITS type and its associated Go type.
type Type struct {
	gotype reflect.Type // associated go type
	tc     typecode     // FITS typecode
	len    int          // number of elements (slice or array)
	dsize  int          // type size in bytes in main data table
	hsize  int          // type size in bytes in heap area
}
