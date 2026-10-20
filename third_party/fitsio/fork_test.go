// Copyright 2026 Dinko Korunic. Use of this source code is governed by a
// BSD-style license that can be found in the LICENSE file.

package fitsio

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// fitsBlocks pads 80-column cards, END added, to whole 2880-byte blocks.
func fitsBlocks(cards ...string) []byte {
	var b []byte
	for _, c := range append(cards, "END") {
		b = append(b, fmt.Sprintf("%-80s", c)...)
	}
	for len(b)%2880 != 0 {
		b = append(b, ' ')
	}
	return b
}

func kv(k string, v any) string {
	switch v := v.(type) {
	case string:
		return fmt.Sprintf("%-8s= %-20s", k, v)
	case bool:
		return fmt.Sprintf("%-8s= %20s", k, map[bool]string{true: "T", false: "F"}[v])
	}
	return fmt.Sprintf("%-8s= %20v", k, v)
}

// TestForkCorruptHeaders: each fork change turns a corrupt header that
// panicked, or allocated what it claimed, into an error.
func TestForkCorruptHeaders(t *testing.T) {
	primary := fitsBlocks(kv("SIMPLE", true), kv("BITPIX", 8), kv("NAXIS", 0))
	for _, c := range []struct {
		name, want string
		data       []byte
	}{
		{name: "NAXIS a string", want: "invalid NAXIS", data: fitsBlocks(kv("SIMPLE", true), kv("BITPIX", 16), kv("NAXIS", "'X'"))},
		{name: "NAXIS out of range", want: "invalid NAXIS", data: fitsBlocks(kv("SIMPLE", true), kv("BITPIX", 16), kv("NAXIS", -1))},
		{name: "negative NAXIS1", want: "invalid NAXIS1", data: fitsBlocks(kv("SIMPLE", true), kv("BITPIX", 16), kv("NAXIS", 2), kv("NAXIS1", -4), kv("NAXIS2", 4))},
		{name: "BITPIX a string", want: "invalid BITPIX", data: fitsBlocks(kv("SIMPLE", true), kv("BITPIX", "'16'"), kv("NAXIS", 0))},
		{name: "CONTINUE first", want: "CONTINUE card without", data: fitsBlocks("CONTINUE  'abc'", kv("SIMPLE", true), kv("BITPIX", 8), kv("NAXIS", 0))},
		// 20 GB claimed by a 2880-byte file: refused, not allocated.
		{name: "image larger than the file", want: "more than the file holds", data: fitsBlocks(kv("SIMPLE", true), kv("BITPIX", 16), kv("NAXIS", 2), kv("NAXIS1", 100000), kv("NAXIS2", 100000))},
		{name: "image size overflows", want: "overflows", data: fitsBlocks(kv("SIMPLE", true), kv("BITPIX", 64), kv("NAXIS", 3), kv("NAXIS1", 1<<40), kv("NAXIS2", 1<<40), kv("NAXIS3", 1<<40))},
		{name: "table with NAXIS 0", want: "want 2", data: append(primary, fitsBlocks(kv("XTENSION", "'BINTABLE'"), kv("BITPIX", 8), kv("NAXIS", 0), kv("PCOUNT", 0), kv("GCOUNT", 1), kv("TFIELDS", 0))...)},
		{name: "table TFIELDS a string", want: "invalid TFIELDS", data: append(primary, fitsBlocks(kv("XTENSION", "'BINTABLE'"), kv("BITPIX", 8), kv("NAXIS", 2), kv("NAXIS1", 0), kv("NAXIS2", 0), kv("PCOUNT", 0), kv("GCOUNT", 1), kv("TFIELDS", "'1'"))...)},
		{name: "table larger than the file", want: "more than the file holds", data: append(primary, fitsBlocks(kv("XTENSION", "'BINTABLE'"), kv("BITPIX", 8), kv("NAXIS", 2), kv("NAXIS1", 8), kv("NAXIS2", 1<<40), kv("PCOUNT", 0), kv("GCOUNT", 1), kv("TFIELDS", 0))...)},
		{name: "THEAP past the table", want: "THEAP", data: append(primary, fitsBlocks(kv("XTENSION", "'BINTABLE'"), kv("BITPIX", 8), kv("NAXIS", 2), kv("NAXIS1", 0), kv("NAXIS2", 0), kv("PCOUNT", 0), kv("GCOUNT", 1), kv("TFIELDS", 0), kv("THEAP", 99999))...)},
		{name: "TFORM a number", want: "TFORM1", data: append(primary, fitsBlocks(kv("XTENSION", "'BINTABLE'"), kv("BITPIX", 8), kv("NAXIS", 2), kv("NAXIS1", 4), kv("NAXIS2", 0), kv("PCOUNT", 0), kv("GCOUNT", 1), kv("TFIELDS", 1), kv("TFORM1", 4))...)},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panic %v", c.name, r)
				}
			}()
			if _, err := Open(&io.LimitedReader{R: bytes.NewReader(c.data), N: int64(len(c.data))}); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: %v, want an error saying %q", c.name, err, c.want)
			}
		}()
	}
}

// TestForkHeapDescriptor: a variable-length array descriptor pointing
// past the heap is an error, not a slice past it.
func TestForkHeapDescriptor(t *testing.T) {
	primary := fitsBlocks(kv("SIMPLE", true), kv("BITPIX", 8), kv("NAXIS", 0))
	// One row: a 1PB descriptor of 100 bytes at offset 0, over a 4-byte heap.
	hdr := fitsBlocks(kv("XTENSION", "'BINTABLE'"), kv("BITPIX", 8), kv("NAXIS", 2), kv("NAXIS1", 8), kv("NAXIS2", 1),
		kv("PCOUNT", 4), kv("GCOUNT", 1), kv("TFIELDS", 1), kv("TTYPE1", "'DATA'"), kv("TFORM1", "'1PB(100)'"))
	data := make([]byte, 2880)
	copy(data, []byte{0, 0, 0, 100, 0, 0, 0, 0, 1, 2, 3, 4})
	file := append(append(primary, hdr...), data...)
	f, err := Open(&io.LimitedReader{R: bytes.NewReader(file), N: int64(len(file))})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.HDU(1).(*Table).Read(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b []byte
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic %v", r)
		}
	}()
	if !rows.Next() {
		t.Fatal("no row")
	}
	if err := rows.Scan(&b); err == nil {
		t.Errorf("descriptor past the heap read %v, want an error", b)
	}
}
