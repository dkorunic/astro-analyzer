// Copyright 2015 The astrogo Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fitsio

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Mode defines a FITS file access mode (r/w).
type Mode int

const (
	ReadOnly  Mode = Mode(os.O_RDONLY) // open the file read-only
	WriteOnly      = Mode(os.O_WRONLY) // open the file write-only
	ReadWrite      = Mode(os.O_RDWR)   // open the file read-write
)

// File represents a FITS file.
type File struct {
	dec  Decoder
	enc  Encoder
	name string
	hdus []HDU
	mode Mode
}

// Open opens a FITS file in read-only mode.
func Open(r io.Reader) (*File, error) {
	var err error

	type namer interface {
		Name() string
	}
	name := ""
	if r, ok := r.(namer); ok {
		name = r.Name()
	}

	f := &File{
		dec:  NewDecoder(r),
		name: name,
		mode: ReadOnly,
		hdus: make([]HDU, 0, 1),
	}

	for {
		var hdu HDU
		hdu, err = f.dec.DecodeHDU()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			err = nil

			break
		}
		f.hdus = append(f.hdus, hdu)
	}

	return f, err
}

// Create creates a new FITS file in write-only mode.
func Create(w io.Writer) (*File, error) {
	var err error
	type namer interface {
		Name() string
	}
	name := ""
	if w, ok := w.(namer); ok {
		name = w.Name()
	}

	f := &File{
		enc:  NewEncoder(w),
		name: name,
		mode: WriteOnly,
		hdus: make([]HDU, 0, 1),
	}

	return f, err
}

// Close releases resources held by a FITS file.
//
// It does not close the underlying io.Reader or io.Writer.
func (f *File) Close() error {
	f.enc = nil
	f.dec = nil
	f.hdus = nil

	return nil
}

// Mode returns the access-mode of this FITS file.
func (f *File) Mode() Mode {
	return f.mode
}

// Name returns the name of the FITS file.
func (f *File) Name() string {
	return f.name
}

// HDUs returns the list of all Header-Data Unit blocks in the file.
func (f *File) HDUs() []HDU {
	return f.hdus
}

// HDU returns the i-th HDU.
//
//nolint:ireturn // HDUs differ in type: the interface is the API
func (f *File) HDU(i int) HDU {
	return f.hdus[i]
}

// Get returns the HDU with name `name` or nil.
//
//nolint:ireturn // HDUs differ in type: the interface is the API
func (f *File) Get(name string) HDU {
	i, hdu := f.gethdu(name)
	if i < 0 {
		return nil
	}

	return hdu
}

// Has returns whether the File has a HDU with name `name`.
func (f *File) Has(name string) bool {
	i, _ := f.gethdu(name)

	return i >= 0
}

// Write writes a HDU to file.
func (f *File) Write(hdu HDU) error {
	var err error
	if f.mode != WriteOnly && f.mode != ReadWrite {
		return fmt.Errorf("%w: file not open for write", ErrFITS)
	}

	if len(f.hdus) == 0 { //nolint:nestif // upstream's parser: one branch per FITS type or keyword form
		if hdu.Type() != HDUImage {
			return fmt.Errorf("%w: file has no primary header. create one first", ErrFITS)
		}

		hdr := hdu.Header()
		if hdr.Get(kwSimple) == nil {
			err = hdr.prepend(Card{
				Name:    kwSimple,
				Value:   true,
				Comment: "primary HDU",
			})
			if err != nil {
				return err
			}
		}
	} else {
		switch hdu.Type() { //nolint:exhaustive // the default case handles the other values
		case HDUImage:
			img, ok := hdu.(Image)
			if !ok {
				return fmt.Errorf("%w: image HDU of type %T", ErrFITS, hdu)
			}
			err = img.freeze()
			if err != nil {
				return err
			}

		case HDUASCIITable, HDUBinaryTable:
			tbl, ok := hdu.(*Table)
			if !ok {
				return fmt.Errorf("%w: table HDU of type %T", ErrFITS, hdu)
			}
			err = tbl.freeze()
			if err != nil {
				return err
			}
		}
	}

	err = f.enc.EncodeHDU(hdu)
	if err != nil {
		return err
	}

	err = f.append(hdu)
	if err != nil {
		return err
	}

	return err
}

// get returns the index and HDU of HDU with name `name`.
//
//nolint:ireturn // HDUs differ in type: the interface is the API
func (f *File) gethdu(name string) (int, HDU) {
	for i, hdu := range f.hdus {
		if hdu.Name() == name {
			return i, hdu
		}
	}

	return -1, nil
}

// append appends an HDU to the list of Header-Data Unit blocks.
func (f *File) append(hdu HDU) error {
	var err error
	if f.mode != WriteOnly && f.mode != ReadWrite {
		return fmt.Errorf("%w: file not open for write", ErrFITS)
	}

	// mare sure there is only one primary-hdu
	if _, ok := hdu.(*primaryHDU); ok && len(f.hdus) != 0 {
		return fmt.Errorf("%w: file has already a Primary HDU", ErrFITS)
	}

	f.hdus = append(f.hdus, hdu)

	return err
}
