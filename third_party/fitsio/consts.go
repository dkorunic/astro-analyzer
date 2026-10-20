// Copyright 2015 The astrogo Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fitsio

const (
	blockSize = 2880 // size in bytes of a FITS block
)

// FITS keywords and card comments used across the package.
// (astro-analyzer fork).
const (
	kwSimple   = "SIMPLE"
	kwBitpix   = "BITPIX"
	kwNaxis    = "NAXIS"
	kwXtension = "XTENSION"
	kwComment  = "COMMENT"
	kwHistory  = "HISTORY"
	kwEnd      = "END"
	xtBintable = "BINTABLE"
	cmBitpix   = "number of bits per data pixel"
	cmNaxis    = "number of data axes"
)
