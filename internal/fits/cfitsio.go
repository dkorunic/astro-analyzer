// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

// HCOMPRESS_1 and PLIO_1 tile decoders, ported from cfitsio
// (fits_hdecompress.c: fits_hdecompress64, R. White's H-transform codec;
// pliocomp.c: pl_l2pi, IRAF's line lists), whose licence asks for this
// notice and disclaimer in copies:
//
// Copyright (Unpublished--all rights reserved under the copyright laws of
// the United States), U.S. Government as represented by the Administrator
// of the National Aeronautics and Space Administration. No copyright is
// claimed in the United States under Title 17, U.S. Code.
//
// Permission to freely use, copy, modify, and distribute this software
// and its documentation without fee is hereby granted, provided that this
// copyright notice and disclaimer of warranty appears in all copies.
//
// DISCLAIMER: THE SOFTWARE IS PROVIDED 'AS IS' WITHOUT ANY WARRANTY OF ANY
// KIND, EITHER EXPRESSED, IMPLIED, OR STATUTORY, INCLUDING, BUT NOT LIMITED
// TO, ANY WARRANTY THAT THE SOFTWARE WILL CONFORM TO SPECIFICATIONS, ANY
// IMPLIED WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE,
// AND FREEDOM FROM INFRINGEMENT, AND ANY WARRANTY THAT THE DOCUMENTATION
// WILL CONFORM TO THE SOFTWARE, OR ANY WARRANTY THAT THE SOFTWARE WILL BE
// ERROR FREE. IN NO EVENT SHALL NASA BE LIABLE FOR ANY DAMAGES, INCLUDING,
// BUT NOT LIMITED TO, DIRECT, INDIRECT, SPECIAL OR CONSEQUENTIAL DAMAGES,
// ARISING OUT OF, RESULTING FROM, OR IN ANY WAY CONNECTED WITH THIS
// SOFTWARE, WHETHER OR NOT BASED UPON WARRANTY, CONTRACT, TORT, OR
// OTHERWISE, WHETHER OR NOT INJURY WAS SUSTAINED BY PERSONS OR PROPERTY OR
// OTHERWISE, AND WHETHER OR NOT LOSS WAS SUSTAINED FROM, OR AROSE OUT OF
// THE RESULTS OF, OR USE OF, THE SOFTWARE OR SERVICES PROVIDED HEREUNDER.
//
// The H-transform code is Copyright (c) 1993 Association of Universities
// for Research in Astronomy. All rights reserved. Produced under National
// Aeronautics and Space Administration Contract No. NAS5-26555.

package fits

import (
	"encoding/binary"
	"errors"
	"math"
)

// Codec errors.
var (
	ErrHcompress = errors.New("corrupt HCOMPRESS data")
	ErrPLIO      = errors.New("corrupt PLIO data")
)

// hbits reads HCOMPRESS's bit stream: cfitsio's nextchar, buffer2 and
// bits_to_go. Reads past the end give zeros and set short, which the
// decoder checks after each bit plane: cfitsio reads past its buffer.
type hbits struct {
	b              []byte
	pos, buf, togo int
	short          bool
}

func (r *hbits) next() int {
	if r.pos >= len(r.b) {
		r.short = true
		r.pos++

		return 0
	}
	r.pos++

	return int(r.b[r.pos-1])
}

func (r *hbits) bit() int {
	if r.togo == 0 {
		r.buf, r.togo = r.next(), 8
	}
	r.togo--

	return r.buf >> r.togo & 1
}

// nbits reads n ≤ 8 bits.
func (r *hbits) nbits(n int) int {
	if r.togo < n {
		r.buf = r.buf<<8 | r.next()
		r.togo += 8
	}
	r.togo -= n

	return r.buf >> r.togo & (1<<n - 1)
}

// nnybble reads len(a) nybbles as input_nnybble does. Its one-byte
// backspace when 8 bits are buffered is left out: no read leaves more than
// 7 (nbits refills only below n ≤ 8, then takes n).
func (r *hbits) nnybble(a []byte) {
	n := len(a)
	if n == 1 {
		a[0] = byte(r.nbits(4) & 15)

		return
	}
	s1, s2 := r.togo+4, r.togo
	for k := 0; k+1 < n; k += 2 {
		r.buf = r.buf<<8 | r.next()
		a[k], a[k+1] = byte(r.buf>>s1&15), byte(r.buf>>s2&15)
	}
	if n%2 == 1 {
		a[n-1] = byte(r.nbits(4) & 15)
	}
}

// huffman decodes HCOMPRESS's fixed code for the values 0–15.
func (r *hbits) huffman() byte {
	c := r.nbits(3)
	if c < 4 {
		return 1 << c
	}
	c = r.bit() | c<<1
	switch c {
	case 8:
		return 3
	case 9:
		return 5
	case 10:
		return 10
	case 11:
		return 12
	case 12:
		return 15
	}
	c = r.bit() | c<<1
	switch c {
	case 26:
		return 6
	case 27:
		return 7
	case 28:
		return 9
	case 29:
		return 11
	case 30:
		return 13
	}
	if r.bit()|c<<1 == 62 {
		return 0
	}

	return 14
}

// log2Ceil is log2 of n rounded up, by cfitsio's floating-point method;
// 0 for n ≤ 1, where cfitsio's is undefined.
func log2Ceil(n int) int {
	if n <= 1 {
		return 0
	}
	k := int(math.Log(float64(float32(n)))/math.Log(2) + 0.5)
	if n > 1<<k {
		k++
	}

	return k
}

// hdecompress decodes one HCOMPRESS_1 tile of n pixels into out (FITS
// order: its stream's ny is the fastest axis). Unlike cfitsio, the
// stream's tile size must match the tile's, and bit planes stay within 63.
//
//nolint:gosec // the header's words are two's complement
func hdecompress(data []byte, smooth bool, out []float64) error {
	if len(data) < 2+3*4+8+3 || data[0] != 0xDD || data[1] != 0x99 {
		return ErrHcompress
	}
	nx := int(int32(binary.BigEndian.Uint32(data[2:])))
	ny := int(int32(binary.BigEndian.Uint32(data[6:])))
	scale := int(int32(binary.BigEndian.Uint32(data[10:])))
	sum := int64(binary.BigEndian.Uint64(data[14:]))
	planes := data[22:25]
	if nx <= 0 || ny <= 0 || int64(nx)*int64(ny) != int64(len(out)) || planes[0] > 63 || planes[1] > 63 || planes[2] > 63 {
		return ErrHcompress
	}
	a := make([]int64, len(out))
	r := &hbits{b: data, pos: 25}
	if err := dodecode(r, a, nx, ny, planes); err != nil {
		return err
	}
	a[0] = sum
	if scale > 1 { // undigitize
		for i := range a {
			a[i] *= int64(scale)
		}
	}
	hinv(a, nx, ny, smooth, scale)
	for i, v := range a {
		out[i] = float64(v)
	}

	return nil
}

// dodecode reads the four quadrants' bit planes, then the sign bits.
func dodecode(r *hbits, a []int64, nx, ny int, planes []byte) error {
	nx2, ny2 := (nx+1)/2, (ny+1)/2
	r.togo = 0
	for _, q := range [4][4]int{
		{0, nx2, ny2, int(planes[0])},
		{ny2, nx2, ny / 2, int(planes[1])},
		{ny * nx2, nx / 2, ny2, int(planes[1])},
		{ny*nx2 + ny2, nx / 2, ny / 2, int(planes[2])},
	} {
		if err := qtreeDecode(r, a, q[0], ny, q[1], q[2], q[3]); err != nil {
			return err
		}
	}
	if r.nbits(4) != 0 || r.short { // the end-of-planes code
		return ErrHcompress
	}
	r.togo = 0
	for i, v := range a {
		if v != 0 && r.bit() != 0 {
			a[i] = -v
		}
	}
	if r.short {
		return ErrHcompress
	}

	return nil
}

// qtreeDecode reads a quadrant's bit planes, nqx × nqy at a[off:] in rows
// of n, each written directly or quadtree-coded.
func qtreeDecode(r *hbits, a []int64, off, n, nqx, nqy, planes int) error {
	log2n := log2Ceil(max(nqx, nqy))
	nqx2, nqy2 := (nqx+1)/2, (nqy+1)/2
	// An empty quadrant (a 1-pixel-wide stream) still expands one code
	// row or column wide: nqx2·nqy2 alone left one byte for it.
	scratch := make([]byte, max(1, nqx2)*max(1, nqy2))
	for bit := planes - 1; bit >= 0; bit-- {
		switch r.nbits(4) {
		case 0: // written directly
			r.nnybble(scratch[:nqx2*nqy2])
		case 0xf:
			scratch[0] = r.huffman()
			// n[k-1] = (n[k]+1)/2, with n[log2n] = nqx or nqy
			nx, ny, nfx, nfy, c := 1, 1, nqx, nqy, 1<<log2n
			for range max(0, log2n-1) {
				c >>= 1
				nx, ny = nx<<1, ny<<1
				if nfx <= c {
					nx--
				} else {
					nfx -= c
				}
				if nfy <= c {
					ny--
				} else {
					nfy -= c
				}
				qtreeExpand(r, scratch, nx, ny)
			}
		default:
			return ErrHcompress
		}
		if r.short {
			return ErrHcompress
		}
		qtreeBitins(scratch, nqx, nqy, a, off, n, bit)
	}

	return nil
}

// qtreeExpand spreads a's (nx+1)/2 × (ny+1)/2 codes over nx × ny in
// place, then reads a new code for every non-zero one.
func qtreeExpand(r *hbits, a []byte, nx, ny int) {
	qtreeCopy(a, nx, ny)
	for i := nx*ny - 1; i >= 0; i-- {
		if a[i] != 0 {
			a[i] = r.huffman()
		}
	}
}

// qtreeCopy expands each 4-bit code of b's first (nx+1)/2 × (ny+1)/2 to
// its 2×2 block of nx × ny, in place: bit 3 goes to [i,j], 2 to [i,j+1],
// 1 to [i+1,j] and 0 to [i+1,j+1]. Edge blocks keep what fits.
func qtreeCopy(b []byte, nx, ny int) {
	nx2, ny2 := (nx+1)/2, (ny+1)/2
	k := ny2*(nx2-1) + ny2 - 1
	for i := nx2 - 1; i >= 0; i-- { // from the end: source and target overlap
		s00 := 2 * (ny*i + ny2 - 1)
		for range ny2 {
			b[s00] = b[k]
			k--
			s00 -= 2
		}
	}
	i := 0
	for ; i < nx-1; i += 2 {
		s00, s10 := ny*i, ny*(i+1)
		j := 0
		for ; j < ny-1; j += 2 {
			v := b[s00]
			b[s10+1], b[s10], b[s00+1], b[s00] = v&1, v>>1&1, v>>2&1, v>>3&1
			s00, s10 = s00+2, s10+2
		}
		if j < ny {
			b[s10], b[s00] = b[s00]>>1&1, b[s00]>>3&1
		}
	}
	if i < nx {
		s00 := ny * i
		j := 0
		for ; j < ny-1; j += 2 {
			b[s00+1], b[s00] = b[s00]>>2&1, b[s00]>>3&1
			s00 += 2
		}
		if j < ny {
			b[s00] >>= 3
		}
	}
}

// qtreeBitins sets bit plane bit of the nx × ny block at b[off:] (rows of
// n) from a's 4-bit codes, one per 2×2 block, as qtreeCopy lays them out.
func qtreeBitins(a []byte, nx, ny int, b []int64, off, n, bit int) {
	v := int64(1) << bit
	set := func(i int, c, mask byte) {
		if c&mask != 0 {
			b[i] |= v
		}
	}
	k, i := 0, 0
	for ; i < nx-1; i += 2 {
		s00 := off + n*i
		j := 0
		for ; j < ny-1; j += 2 {
			c := a[k]
			set(s00+n+1, c, 1)
			set(s00+n, c, 2)
			set(s00+1, c, 4)
			set(s00, c, 8)
			s00 += 2
			k++
		}
		if j < ny {
			set(s00+n, a[k], 2)
			set(s00, a[k], 8)
			k++
		}
	}
	if i < nx {
		s00 := off + n*i
		j := 0
		for ; j < ny-1; j += 2 {
			set(s00+1, a[k], 4)
			set(s00, a[k], 8)
			s00 += 2
			k++
		}
		if j < ny {
			set(s00, a[k], 8)
		}
	}
}

// round moves h to a multiple of the mask's step, rounding half away from
// zero (p for h ≥ 0, n below).
func round(h, p, n, mask int64) int64 {
	if h >= 0 {
		return (h + p) & mask
	}

	return (h + n) & mask
}

// hinv inverts the H-transform of the nx × ny coefficients in a, in place
// (hinv64). Overflow on corrupt data wraps; it cannot index out of a.
//
//nolint:gocognit,cyclop,funlen // ported as written, to compare line by line
func hinv(a []int64, nx, ny int, smooth bool, scale int) {
	nmax := max(nx, ny)
	if nmax <= 1 { // a[0] is the sum, the only pixel
		return
	}
	log2n := log2Ceil(nmax)
	tmp := make([]int64, (nmax+1)/2)
	shift := 1
	bit0 := int64(1) << (log2n - 1)
	bit1, bit2 := bit0<<1, bit0<<2
	mask0 := -bit0
	mask1, mask2 := mask0<<1, mask0<<2
	prnd0, prnd1, prnd2 := bit0>>1, bit1>>1, bit2>>1
	nrnd0, nrnd1 := prnd0-1, prnd1-1
	a[0] = round(a[0], prnd2, prnd2-1, mask2)
	nxtop, nytop, nxf, nyf, c := 1, 1, nx, ny, 1<<log2n
	for k := log2n - 1; k >= 0; k-- {
		// ntop[k-1] = (ntop[k]+1)/2, with ntop[log2n] = n
		c >>= 1
		nxtop, nytop = nxtop<<1, nytop<<1
		if nxf <= c {
			nxtop--
		} else {
			nxf -= c
		}
		if nyf <= c {
			nytop--
		} else {
			nyf -= c
		}
		if k == 0 { // double shift, and prnd0 = 0, on the last pass
			nrnd0, shift = 0, 2
		}
		for i := range nxtop {
			hunshuffle(a, ny*i, nytop, 1, tmp)
		}
		for j := range nytop {
			hunshuffle(a, j, nxtop, ny, tmp)
		}
		if smooth {
			hsmooth(a, nxtop, nytop, ny, scale)
		}
		oddx, oddy := nxtop%2, nytop%2
		i := 0
		for ; i < nxtop-oddx; i += 2 {
			s00, s10 := ny*i, ny*(i+1)
			for j := 0; j < nytop-oddy; j += 2 {
				h0, hx, hy, hc := a[s00], a[s10], a[s00+1], a[s10+1]
				// hx, hy to multiples of bit1, hc of bit0; h0 is of bit2
				hx = round(hx, prnd1, nrnd1, mask1)
				hy = round(hy, prnd1, nrnd1, mask1)
				hc = round(hc, prnd0, nrnd0, mask0)
				// bit0 of hc goes to hx and hy
				lowbit0 := hc & bit0
				if hx >= 0 {
					hx -= lowbit0
				} else {
					hx += lowbit0
				}
				if hy >= 0 {
					hy -= lowbit0
				} else {
					hy += lowbit0
				}
				// bits 0 and 1 of hc, hx, hy go to h0, lossless for
				// negative pixels too
				lowbit1 := (hc ^ hx ^ hy) & bit1
				switch {
				case h0 >= 0:
					h0 += lowbit0 - lowbit1
				case lowbit0 == 0:
					h0 += lowbit1
				default:
					h0 += lowbit0 - lowbit1
				}
				a[s10+1] = (h0 + hx + hy + hc) >> shift
				a[s10] = (h0 + hx - hy - hc) >> shift
				a[s00+1] = (h0 - hx + hy - hc) >> shift
				a[s00] = (h0 - hx - hy + hc) >> shift
				s00, s10 = s00+2, s10+2
			}
			if oddy == 1 { // the row's last element: s00+1, s10+1 are off the edge
				h0, hx := a[s00], round(a[s10], prnd1, nrnd1, mask1)
				lowbit1 := hx & bit1
				if h0 >= 0 {
					h0 -= lowbit1
				} else {
					h0 += lowbit1
				}
				a[s10] = (h0 + hx) >> shift
				a[s00] = (h0 - hx) >> shift
			}
		}
		if oddx == 1 { // the last row, i = nxtop-1: s10 is off the edge
			s00 := ny * i
			j := 0
			for ; j < nytop-oddy; j += 2 {
				h0, hy := a[s00], round(a[s00+1], prnd1, nrnd1, mask1)
				lowbit1 := hy & bit1
				if h0 >= 0 {
					h0 -= lowbit1
				} else {
					h0 += lowbit1
				}
				a[s00+1] = (h0 + hy) >> shift
				a[s00] = (h0 - hy) >> shift
				s00 += 2
			}
			if oddy == 1 { // the corner
				a[s00] >>= shift
			}
		}
		bit1, bit0 = bit0, bit0>>1
		mask1, mask0 = mask0, mask0>>1
		prnd1, prnd0 = prnd0, prnd0>>1
		nrnd1, nrnd0 = nrnd0, prnd0-1
	}
}

// hunshuffle interleaves the n elements a[base], a[base+n2]...: the first
// half to the even places, the second (via tmp) to the odd ones.
func hunshuffle(a []int64, base, n, n2 int, tmp []int64) {
	nhalf := (n + 1) >> 1
	for i := nhalf; i < n; i++ {
		tmp[i-nhalf] = a[base+n2*i]
	}
	for i := nhalf - 1; i >= 0; i-- {
		a[base+2*n2*i] = a[base+n2*i]
	}
	for i, t := 1, 0; i < n; i, t = i+2, t+1 {
		a[base+n2*i] = tmp[t]
	}
}

// hsmooth adjusts coefficients toward values interpolated from their
// neighbours, by at most scale/2 (hsmooth64); edge coefficients stay.
//
//nolint:gocognit,funlen // ported as written
func hsmooth(a []int64, nxtop, nytop, ny, scale int) {
	smax := int64(scale >> 1)
	if smax <= 0 {
		return
	}
	ny2 := ny << 1
	// limit is the change s/2^k towards diff, rounded toward zero and
	// clamped to ±smax.
	limit := func(diff, h int64, k uint) int64 {
		s := diff - h<<k
		if s >= 0 {
			s >>= k
		} else {
			s = (s + 1<<k - 1) >> k
		}

		return max(min(s, smax), -smax)
	}
	// slope adjusts a difference coefficient given the means before (hm),
	// at (h0) and after (hp) it, under monotonicity constraints.
	slope := func(hm, h0, hp, h int64) int64 {
		dmax := max(min(hp-h0, h0-hm), 0) << 2
		dmin := min(max(hp-h0, h0-hm), 0) << 2
		if dmin >= dmax { // monotonicity would set the slope to 0
			return h
		}

		return h + limit(max(min(hp-hm, dmax), dmin), h, 3)
	}
	for i := 2; i < nxtop-2; i += 2 { // x differences hx
		s00, s10 := ny*i, ny*(i+1)
		for j := 0; j < nytop; j += 2 {
			a[s10] = slope(a[s00-ny2], a[s00], a[s00+ny2], a[s10])
			s00, s10 = s00+2, s10+2
		}
	}
	for i := 0; i < nxtop; i += 2 { // y differences hy
		s00 := ny*i + 2
		for j := 2; j < nytop-2; j += 2 {
			a[s00+1] = slope(a[s00-2], a[s00], a[s00+2], a[s00+1])
			s00 += 2
		}
	}
	for i := 2; i < nxtop-2; i += 2 { // curvature hc
		s00, s10 := ny*i+2, ny*(i+1)+2
		for j := 2; j < nytop-2; j += 2 {
			hmm, hpm := a[s00-ny2-2], a[s00+ny2-2]
			hmp, hpp := a[s00-ny2+2], a[s00+ny2+2]
			h0 := a[s00]
			diff := hpp + hmm - hmp - hpm
			hx2, hy2 := a[s10]<<1, a[s00+1]<<1
			m1 := min(max(hpp-h0, 0)-hx2-hy2, max(h0-hpm, 0)+hx2-hy2)
			m2 := min(max(h0-hmp, 0)-hx2+hy2, max(hmm-h0, 0)+hx2+hy2)
			dmax := min(m1, m2) << 4
			m1 = max(min(hpp-h0, 0)-hx2-hy2, min(h0-hpm, 0)+hx2-hy2)
			m2 = max(min(h0-hmp, 0)-hx2+hy2, min(hmm-h0, 0)+hx2+hy2)
			dmin := max(m1, m2) << 4
			if dmin < dmax {
				a[s10+1] += limit(max(min(diff, dmax), dmin), a[s10+1], 6)
			}
			s00, s10 = s00+2, s10+2
		}
	}
}

// plio decodes an IRAF PLIO line list (pl_l2pi, with xs = 1) of
// big-endian signed 16-bit words into out, zeros where it ends early; ll
// is 1-based as there. An instruction's top 4 bits are its opcode, the low 12 its data.
//
//nolint:gocognit,cyclop,gosec // ported as written; words are two's complement
func plio(data []byte, out []float64) error {
	srclen := len(data) / 2
	ll := make([]int, srclen+1)
	for i := range srclen {
		ll[i+1] = int(int16(binary.BigEndian.Uint16(data[2*i:])))
	}
	if srclen < 7 {
		return ErrPLIO
	}
	lllen, first := ll[3], 4
	if lllen <= 0 { // the long format: a 30-bit length, a header of ll[2] words
		lllen, first = ll[5]<<15+ll[4], ll[2]+1
	}
	if lllen <= 0 {
		return ErrPLIO
	}
	clear(out)
	xe, op, x1, pv := len(out), 1, 1, 1
	skip := false
	for ip := first; ip <= lllen && x1 <= xe; ip++ {
		if skip {
			skip = false

			continue
		}
		if ip < 1 || ip > srclen {
			return ErrPLIO
		}
		opcode, data := ll[ip]/4096, ll[ip]&4095
		switch opcode {
		case 0, 4, 5: // a run of zeros, of pv, or of zeros ending in pv
			x2 := x1 + data - 1
			i1, i2 := max(x1, 1), min(x2, xe)
			if np := i2 - i1 + 1; np > 0 {
				otop := op + np - 1
				if opcode == 4 {
					for i := op; i <= otop; i++ {
						out[i-1] = float64(pv)
					}
				} else if opcode == 5 && i2 == x2 {
					out[otop-1] = float64(pv)
				}
				op = otop + 1
			}
			x1 = x2 + 1
		case 1: // set pv from this word's data and the next word
			if ip+1 > srclen {
				return ErrPLIO
			}
			pv = ll[ip+1]<<12 + data
			skip = true
		case 2:
			pv += data
		case 3:
			pv -= data
		case 6, 7: // change pv, then one pixel of it
			if opcode == 6 {
				pv += data
			} else {
				pv -= data
			}
			if x1 >= 1 && x1 <= xe {
				out[op-1] = float64(pv)
				op++
			}
			x1++
		}
	}

	return nil
}
