// Copyright 2015 The astrogo Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fitsio

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// Column represents a column in a FITS table.
type Column struct {
	// read function (binary/ascii)
	read func(table *Table, icol int, irow int64, ptr interface{}) error

	// write function (binary/ascii)
	write   func(table *Table, icol int, irow int64, ptr interface{}) error
	Name    string // column name, corresponding to ``TTYPE`` keyword
	Format  string // column format, corresponding to ``TFORM`` keyword
	Unit    string // column unit, corresponding to ``TUNIT`` keyword
	Null    string // null value, corresponding to ``TNULL`` keyword
	Display string // display format, corresponding to ``TDISP`` keyword
	txtfmt  string // go-based fmt string for ASCII table

	Dim   []int64 // column dimension corresponding to ``TDIM`` keyword
	dtype Type    // type of the value held by the column

	Bscale float64 // bscale value, corresponding to ``TSCAL`` keyword
	Bzero  float64 // bzero value, corresponding to ``TZERO`` keyword
	Start  int64   // column starting position, corresponding to ``TBCOL`` keyword

	offset int // offset in bytes (from start of data-pad or heap-pad) to get at the column data
}

// NewColumn creates a new Column with name `name` and Format inferred from the type of value.
func NewColumn(_ string, _ interface{}) (Column, error) {
	return Column{}, fmt.Errorf("%w: NewColumn is not implemented", ErrFITS)
}

// Type returns the Go reflect.Type associated with this Column.
func (col *Column) Type() reflect.Type {
	return col.dtype.gotype
}

// readBin reads the value at column number icol and row irow, into ptr.
//
//nolint:gocyclo,maintidx // one branch per Go kind and FITS type
func (col *Column) readBin(table *Table, _ int, irow int64, ptr interface{}) error {
	var err error

	rv := reflect.Indirect(reflect.ValueOf(ptr))
	rt := reflect.TypeOf(rv.Interface())

	switch rt.Kind() { //nolint:exhaustive // the default case handles the other values
	case reflect.Slice:

		beg := table.rowsz*int(irow) + col.offset
		end := beg + col.dtype.dsize
		row := table.data[beg:end]
		r := newReader(row)
		slice := reflect.ValueOf(ptr).Elem()
		nmax := 0

		switch col.dtype.dsize {
		case 8:
			var n int32
			var offset int32
			r.readI32(&n)
			r.readI32(&offset)
			beg = int(offset)
			end = beg + int(n)*int(col.dtype.gotype.Elem().Size())
			nmax = int(n)

		case 16:
			var n int64
			var offset int64
			r.readI64(&n)
			r.readI64(&offset)
			beg = int(offset)
			end = beg + int(n)*int(col.dtype.gotype.Elem().Size())
			nmax = int(n)
		}
		// A descriptor of a corrupt file may point anywhere: keep it within
		// the heap before allocating or slicing. (astro-analyzer fork)
		if size := int(col.dtype.gotype.Elem().Size()); nmax < 0 || beg < 0 || size > 0 && nmax > (len(table.heap)-beg)/size || beg > len(table.heap) {
			return fmt.Errorf("%w: column %q row %d: descriptor (%d, %d) outside the %d-byte heap", ErrFITS, col.Name, irow, nmax, beg, len(table.heap))
		}
		if slice.Len() < nmax {
			slice = reflect.MakeSlice(rt, nmax, nmax)
		}

		r = newReader(table.heap[beg:end])
		switch slice := slice.Interface().(type) {
		case []bool:
			r.readBools(slice[:nmax])

		case []byte:
			copy(slice[:nmax], r.p)

		case []int8:
			r.readI8s(slice[:nmax])

		case []int16:
			r.readI16s(slice[:nmax])

		case []int32:
			r.readI32s(slice[:nmax])

		case []int64:
			r.readI64s(slice[:nmax])

		case []int:
			r.readInts(slice[:nmax])

		case []uint16:
			r.readU16s(slice[:nmax])

		case []uint32:
			r.readU32s(slice[:nmax])

		case []uint64:
			r.readU64s(slice[:nmax])

		case []uint:
			r.readUints(slice[:nmax])

		case []float32:
			r.readF32s(slice[:nmax])

		case []float64:
			r.readF64s(slice[:nmax])

		case []complex64:
			r.readC64s(slice[:nmax])

		case []complex128:
			r.readC128s(slice[:nmax])

		default:
			panic(fmt.Errorf("%w: not implemented %T", ErrFITS, slice))
		}
		rv.Set(slice)

	case reflect.Array:

		beg := table.rowsz*int(irow) + col.offset
		end := beg + (col.dtype.dsize * col.dtype.len)
		row := table.data[beg:end]
		r := newReader(row)
		switch slice := rv.Slice(0, rv.Len()).Interface().(type) {
		case []bool:
			r.readBools(slice)

		case []byte:
			copy(slice, r.p)

		case []int8:
			r.readI8s(slice)

		case []int16:
			r.readI16s(slice)

		case []int32:
			r.readI32s(slice)

		case []int64:
			r.readI64s(slice)

		case []int:
			r.readInts(slice)

		case []uint16:
			r.readU16s(slice)

		case []uint32:
			r.readU32s(slice)

		case []uint64:
			r.readU64s(slice)

		case []uint:
			r.readUints(slice)

		case []float32:
			r.readF32s(slice)

		case []float64:
			r.readF64s(slice)

		case []complex64:
			r.readC64s(slice)

		case []complex128:
			r.readC128s(slice)

		default:
			panic(fmt.Errorf("%w: not implemented %T", ErrFITS, slice))
		}

	case reflect.Bool, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Int,
		reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uint,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		// A type switch, not ptr.(*bool) per kind: a named type of that
		// kind returns an error instead of panicking. (astro-analyzer fork)
		beg := table.rowsz*int(irow) + col.offset
		r := newReader(table.data[beg : beg+col.dtype.dsize])
		switch v := ptr.(type) {
		case *bool:
			r.readBool(v)
		case *int8:
			r.readI8(v)
		case *int16:
			r.readI16(v)
		case *int32:
			r.readI32(v)
		case *int64:
			r.readI64(v)
		case *int:
			r.readInt(v)
		case *uint8:
			r.readU8(v)
		case *uint16:
			r.readU16(v)
		case *uint32:
			r.readU32(v)
		case *uint64:
			r.readU64(v)
		case *uint:
			r.readUint(v)
		case *float32:
			r.readF32(v)
		case *float64:
			r.readF64(v)
		case *complex64:
			r.readC64(v)
		case *complex128:
			r.readC128(v)
		default:
			return fmt.Errorf("%w: binary-table can not read/write %T", ErrFITS, ptr)
		}

	case reflect.String:

		beg := table.rowsz*int(irow) + col.offset
		end := beg + col.dtype.dsize
		row := table.data[beg:end]
		var str string
		if row[0] == '\x00' {
			str = string(row[1:])
			str = strings.TrimRight(str, string([]byte("\x00")))
		} else {
			str = string(row)
		}

		rv.SetString(str)

	default:
		return fmt.Errorf("%w: binary-table can not read/write %v", ErrFITS, rt.Kind())
	}

	return err
}

// writeBin writes the value at column number icol and row irow, from ptr.
//
//nolint:gocyclo,maintidx // one branch per Go kind and FITS type
func (col *Column) writeBin(table *Table, _ int, irow int64, ptr interface{}) error {
	var (
		err error
		rv  = reflect.Indirect(reflect.ValueOf(ptr))
		rvi = rv.Interface()
		rt  = reflect.TypeOf(rvi)
	)

	switch rt.Kind() { //nolint:exhaustive // the default case handles the other values
	case reflect.Slice:

		beg := table.rowsz*int(irow) + col.offset
		end := beg + col.dtype.dsize

		w := newWriter(table.data[beg:end])
		nmax := rv.Len()

		switch col.dtype.dsize {
		case 8: // 1P descriptors are 32-bit (astro-analyzer fork)
			if nmax > math.MaxInt32 || len(table.heap) > math.MaxInt32 {
				return fmt.Errorf("%w: column %q: %d elements at heap offset %d overflow a 1P descriptor", ErrFITS, col.Name, nmax, len(table.heap))
			}
			w.writeI32(int32(nmax))            //nolint:gosec // bounded above
			w.writeI32(int32(len(table.heap))) //nolint:gosec // bounded above

		case 16:
			w.writeI64(int64(nmax))
			w.writeI64(int64(len(table.heap)))
		}

		{
			w := newWriter(make([]byte, nmax*col.dtype.hsize))
			switch slice := rvi.(type) {
			case []bool:
				w.writeBools(slice)

			case []byte:
				copy(w.p, slice)

			case []int8:
				w.writeI8s(slice)

			case []int16:
				w.writeI16s(slice)

			case []int32:
				w.writeI32s(slice)

			case []int64:
				w.writeI64s(slice)

			case []int:
				w.writeInts(slice)

			case []uint16:
				w.writeU16s(slice)

			case []uint32:
				w.writeU32s(slice)

			case []uint64:
				w.writeU64s(slice)

			case []uint:
				w.writeUints(slice)

			case []float32:
				w.writeF32s(slice)

			case []float64:
				w.writeF64s(slice)

			case []complex64:
				w.writeC64s(slice)

			case []complex128:
				w.writeC128s(slice)

			default:
				panic(fmt.Errorf("%w: not implemented %T", ErrFITS, slice))
			}
			table.heap = append(table.heap, w.bytes()...)
		}

	case reflect.Array:

		beg := table.rowsz*int(irow) + col.offset
		end := beg + (col.dtype.dsize * col.dtype.len)

		w := newWriter(table.data[beg:end])
		switch slice := rv.Slice(0, rv.Len()).Interface().(type) {
		case []bool:
			w.writeBools(slice)

		case []byte:
			copy(w.p, slice)

		case []int8:
			w.writeI8s(slice)

		case []int16:
			w.writeI16s(slice)

		case []int32:
			w.writeI32s(slice)

		case []int64:
			w.writeI64s(slice)

		case []int:
			w.writeInts(slice)

		case []uint16:
			w.writeU16s(slice)

		case []uint32:
			w.writeU32s(slice)

		case []uint64:
			w.writeU64s(slice)

		case []uint:
			w.writeUints(slice)

		case []float32:
			w.writeF32s(slice)

		case []float64:
			w.writeF64s(slice)

		case []complex64:
			w.writeC64s(slice)

		case []complex128:
			w.writeC128s(slice)

		default:
			panic(fmt.Errorf("%w: not implemented %T", ErrFITS, slice))
		}

	case reflect.Bool, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Int,
		reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uint,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		// A type switch, not ptr.(*bool) per kind: a named type of that
		// kind returns an error instead of panicking. (astro-analyzer fork)
		beg := table.rowsz*int(irow) + col.offset
		w := newWriter(table.data[beg : beg+col.dtype.dsize])
		switch v := rvi.(type) {
		case bool:
			w.writeBool(v)
		case int8:
			w.writeI8(v)
		case int16:
			w.writeI16(v)
		case int32:
			w.writeI32(v)
		case int64:
			w.writeI64(v)
		case int:
			w.writeInt(v)
		case uint8:
			w.writeU8(v)
		case uint16:
			w.writeU16(v)
		case uint32:
			w.writeU32(v)
		case uint64:
			w.writeU64(v)
		case uint:
			w.writeUint(v)
		case float32:
			w.writeF32(v)
		case float64:
			w.writeF64(v)
		case complex64:
			w.writeC64(v)
		case complex128:
			w.writeC128(v)
		default:
			return fmt.Errorf("%w: binary-table can not read/write %T", ErrFITS, rvi)
		}

	case reflect.String:

		beg := table.rowsz*int(irow) + col.offset
		end := beg + col.dtype.dsize

		buf := newWriter(table.data[beg:end])
		str := rv.String()
		data := make([]byte, 0, len(str)+1)
		data = append(data, '\x00')
		data = append(data, []byte(str)...)
		n := min(len(data), end-beg)
		if n < end-beg {
			data = append(data, bytes.Repeat([]byte("\x00"), end-beg-n)...)
			n = len(data)
		}
		_, err = buf.Write(data[:n])
		if err != nil {
			return fmt.Errorf("%w: %w", ErrFITS, err)
		}

	default:
		return fmt.Errorf("%w: binary-table can not read/write %v", ErrFITS, rt.Kind())
	}

	return err
}

// readTxt reads the value at column number icol and row irow, into ptr.
func (col *Column) readTxt(table *Table, _ int, irow int64, ptr interface{}) error {
	var err error

	rv := reflect.Indirect(reflect.ValueOf(ptr))
	rt := reflect.TypeOf(rv.Interface())

	beg := table.rowsz*int(irow) + col.offset
	end := beg + col.dtype.dsize
	buf := table.data[beg:end]
	str := strings.TrimSpace(string(buf))

	switch rt.Kind() { //nolint:exhaustive // the default case handles the other values
	case reflect.Slice:

		return fmt.Errorf("%w: ASCII-table can not read/write slices", ErrFITS)

	case reflect.Array:

		return fmt.Errorf("%w: ASCII-table can not read/write arrays", ErrFITS)

	case reflect.Bool:

		return fmt.Errorf("%w: ASCII-table can not read/write booleans", ErrFITS)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := strconv.ParseUint(str, 10, 64)
		if err != nil {
			return fmt.Errorf("%w: error parsing %q into a uint: %w", ErrFITS, str, err)
		}
		rv.SetUint(v)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := strconv.ParseInt(str, 10, 64)
		if err != nil {
			return fmt.Errorf("%w: error parsing %q into an int: %w", ErrFITS, str, err)
		}
		rv.SetInt(v)

	case reflect.Float32, reflect.Float64:

		v, err := strconv.ParseFloat(str, 64)
		if err != nil {
			return fmt.Errorf("%w: error parsing %q into a float: %w", ErrFITS, str, err)
		}
		rv.SetFloat(v)

	case reflect.Complex64, reflect.Complex128:

		return fmt.Errorf("%w: ASCII-table can not read/write complexes", ErrFITS)

	case reflect.String:
		rv.SetString(str)

	default:
		return fmt.Errorf("%w: ASCII-table can not read/write %v", ErrFITS, rt.Kind())
	}

	return err
}

// writeTxt writes the value at column number icol and row irow, from ptr.
func (col *Column) writeTxt(table *Table, _ int, irow int64, ptr interface{}) error {
	var err error

	beg := table.rowsz*int(irow) + col.offset
	end := beg + col.dtype.dsize
	w := newWriter(table.data[beg:end])

	rv := reflect.Indirect(reflect.ValueOf(ptr))
	rt := reflect.TypeOf(rv.Interface())

	switch rt.Kind() { //nolint:exhaustive // the default case handles the other values
	case reflect.Slice:

		return fmt.Errorf("%w: ASCII-table can not read/write slices", ErrFITS)

	case reflect.Array:

		return fmt.Errorf("%w: ASCII-table can not read/write arrays", ErrFITS)

	case reflect.Bool:

		return fmt.Errorf("%w: ASCII-table can not read/write booleans", ErrFITS)

	case reflect.Complex64, reflect.Complex128:

		return fmt.Errorf("%w: ASCII-table can not read/write complexes", ErrFITS)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Float32, reflect.Float64,
		reflect.String:

		str := fmt.Sprintf(col.txtfmt, rv.Interface())
		var n int
		n, err = fmt.Fprint(w, str) // not Fprintf: a cell may hold %. (astro-analyzer fork)
		if err != nil {
			return fmt.Errorf("%w: error writing '%#v': %w", ErrFITS, rv.Interface(), err)
		}
		if n != len(w.p) {
			return fmt.Errorf(
				"%w: error writing '%#v'. expected %d bytes. wrote %d", ErrFITS,
				rv.Interface(), len(w.p), n,
			)
		}

	default:
		return fmt.Errorf("%w: ASCII-table can not read/write %v", ErrFITS, rt.Kind())
	}

	return err
}
