// Copyright 2015 The astrogo Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fitsio

import (
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
)

// Header describes a Header-Data Unit of a FITS file.
type Header struct {
	axes   []int   // dimensions of image data array
	cards  []Card  // content of the Header
	htype  HDUType // type of the HDU
	bitpix int     // character information
}

// newHeader creates a new Header.
// no test on the content of the cards is performed.
func newHeader(cards []Card, htype HDUType, bitpix int, axes []int) *Header {
	hdr := &Header{
		htype:  htype,
		bitpix: bitpix,
		axes:   make([]int, len(axes)),
		cards:  make([]Card, 0, len(cards)),
	}
	copy(hdr.axes, axes)
	err := hdr.Append(cards...)
	if err != nil {
		panic(err)
	}

	return hdr
}

// NewHeader creates a new Header from a set of Cards, HDU Type, bitpix and axes.
func NewHeader(cards []Card, htype HDUType, bitpix int, axes []int) *Header {
	// short circuit: too many axes violates the FITS standard
	if len(axes) > 999 {
		panic(fmt.Errorf("%w: too many axes (got=%d > 999)", ErrFITS, len(axes)))
	}
	hdr := newHeader(cards, htype, bitpix, axes)

	// add (some) mandatory cards (BITPIX, NAXES, AXIS1, AXIS2)
	keys := make(map[string]struct{}, len(cards))
	for i := range hdr.cards {
		card := &hdr.cards[i]
		k := card.Name
		keys[k] = struct{}{}
	}

	dcards := make([]Card, 0, 3)
	if _, ok := keys[kwBitpix]; !ok {
		dcards = append(dcards, Card{
			Name:    kwBitpix,
			Value:   hdr.Bitpix(),
			Comment: cmBitpix,
		})
	}

	if _, ok := keys[kwNaxis]; !ok {
		dcards = append(dcards, Card{
			Name:    kwNaxis,
			Value:   len(hdr.Axes()),
			Comment: cmNaxis,
		})
		nax := len(hdr.Axes())
		for i := range nax {
			axis := strconv.Itoa(i + 1) // index from 0, hdu starts at NAXIS1
			key := kwNaxis + axis
			if _, ok := keys[key]; !ok {
				dcards = append(dcards, Card{
					Name:    key,
					Value:   hdr.Axes()[i],
					Comment: "length of data axis " + axis,
				})
			}
		}
	}

	err := hdr.prepend(dcards...)
	if err != nil {
		panic(err)
	}

	return hdr
}

// NewDefaultHeader creates a Header with CFITSIO default Cards, of type HDUImage and
// bitpix=8, no axes.
func NewDefaultHeader() *Header {
	return NewHeader(
		[]Card{
			{
				Name:    kwSimple,
				Value:   true,
				Comment: "file does conform to FITS standard",
			},
			{
				Name:    kwBitpix,
				Value:   8,
				Comment: cmBitpix,
			},
			{
				Name:    kwNaxis,
				Value:   0,
				Comment: cmNaxis,
			},
			{
				Name:    "NAXIS1",
				Value:   0,
				Comment: "length of data axis 1",
			},
			{
				Name:    "NAXIS2",
				Value:   0,
				Comment: "length of data axis 2",
			},
		},
		HDUImage,
		8,
		[]int{},
	)
}

// Type returns the Type of this Header.
func (hdr *Header) Type() HDUType {
	return hdr.htype
}

// Text returns the header's cards content as 80-byte lines.
func (hdr *Header) Text() string {
	const kLINE = 80
	buf := make([]byte, 0, kLINE*len(hdr.cards))
	for i := range hdr.cards {
		card := &hdr.cards[i]
		line, err := makeHeaderLine(card)
		if err != nil {
			panic(fmt.Errorf("%w: %w", ErrFITS, err))
		}
		buf = append(buf, line...)
	}

	return string(buf)
}

// Append appends a set of Cards to this Header.
func (hdr *Header) Append(cards ...Card) error {
	var err error
	keys := make(map[string]struct{}, len(hdr.cards))
	for i := range hdr.cards {
		card := &hdr.cards[i]
		k := card.Name
		keys[k] = struct{}{}
	}

	for _, card := range cards {
		_, dup := keys[card.Name]
		if dup {
			switch card.Name {
			case kwComment, kwHistory, "":
				hdr.cards = append(hdr.cards, card)

				continue
			case kwEnd:
				continue
			default:
				return fmt.Errorf("%w: duplicate Card [%s] (value=%v)", ErrFITS, card.Name, card.Value)
			}
		}
		rv := reflect.ValueOf(card.Value)
		if rv.IsValid() {
			switch rv.Type().Kind() { //nolint:exhaustive // the default case handles the other values
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				card.Value = int(rv.Int())
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				u := rv.Uint()
				if u > math.MaxInt { // (astro-analyzer fork)
					return fmt.Errorf("%w: card [%s] value %d overflows int", ErrFITS, card.Name, u)
				}
				card.Value = int(u)
			case reflect.Float32, reflect.Float64:
				card.Value = rv.Float()
			case reflect.Complex64, reflect.Complex128:
				card.Value = rv.Complex()
			case reflect.String:
				card.Value = rv.String() // named string types too
			case reflect.Bool:
				card.Value = rv.Bool()
			case reflect.Struct:
				switch card.Value.(type) {
				case big.Int:
					// ok
				default:
					return fmt.Errorf(
						"%w: invalid value type (%T) for card [%s] (kind=%v)", ErrFITS,
						card.Value, card.Name, rv.Type().Kind(),
					)
				}
			default:
				return fmt.Errorf(
					"%w: invalid value type (%T) for card [%s] (kind=%v)", ErrFITS,
					card.Value, card.Name, rv.Type().Kind(),
				)
			}
		}
		hdr.cards = append(hdr.cards, card)
	}

	return err
}

// Clear resets the Header to the default state.
func (hdr *Header) Clear() {
	hdr.cards = make([]Card, 0)
	hdr.bitpix = 0
	hdr.axes = make([]int, 0)
}

// Get returns the Card with name n or nil if it doesn't exist.
// If multiple cards with the same name exist, the first one is returned.
func (hdr *Header) Get(n string) *Card {
	_, card := hdr.get(n)

	return card
}

// Card returns the i-th card.
// Card panics if the index is out of range.
func (hdr *Header) Card(i int) *Card {
	return &hdr.cards[i]
}

// Comment returns the whole comment string for this Header.
func (hdr *Header) Comment() string {
	if card := hdr.Get(kwComment); card != nil {
		comment, _ := card.Value.(string)

		return comment
	}

	return ""
}

// History returns the whole history string for this Header.
func (hdr *Header) History() string {
	if card := hdr.Get(kwHistory); card != nil {
		history, _ := card.Value.(string)

		return history
	}

	return ""
}

// Bitpix returns the bitpix value.
func (hdr *Header) Bitpix() int {
	return hdr.bitpix
}

// Axes returns the axes for this Header.
func (hdr *Header) Axes() []int {
	return hdr.axes
}

// Index returns the index of the Card with name n, or -1 if it doesn't exist.
func (hdr *Header) Index(n string) int {
	idx, _ := hdr.get(n)

	return idx
}

// Keys returns the name of all the Cards of this Header.
func (hdr *Header) Keys() []string {
	keys := make([]string, 0, len(hdr.cards))
	for i := range hdr.cards {
		key := hdr.cards[i].Name
		switch key {
		case kwEnd, kwComment, kwHistory, "":
			continue
		default:
			keys = append(keys, key)
		}
	}

	return keys
}

// Set modifies the value and comment of a Card with name n.
// It returns Append's error when it adds the card. (astro-analyzer fork).
func (hdr *Header) Set(n string, v interface{}, comment string) error {
	card := hdr.Get(n)
	if card == nil {
		return hdr.Append(Card{
			Name:    n,
			Value:   v,
			Comment: comment,
		})
	}
	card.Value = v
	card.Comment = comment

	return nil
}

// prepend prepends a (set of) cards to this Header.
func (hdr *Header) prepend(cards ...Card) error {
	var err error
	keys := make(map[string]struct{}, len(hdr.cards))
	for i := range hdr.cards {
		card := &hdr.cards[i]
		k := card.Name
		keys[k] = struct{}{}
	}

	hcards := make([]Card, 0, len(cards))
	for _, card := range cards {
		_, dup := keys[card.Name]
		if dup {
			switch card.Name {
			case kwComment, kwHistory, "":
				hdr.cards = append(hdr.cards, card)

				continue
			case kwEnd:
				continue
			default:
				return fmt.Errorf("%w: duplicate Card [%s] (value=%v)", ErrFITS, card.Name, card.Value)
			}
		}
		rv := reflect.ValueOf(card.Value)
		if rv.IsValid() {
			switch rv.Type().Kind() { //nolint:exhaustive // the default case handles the other values
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				card.Value = int(rv.Int())
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				u := rv.Uint()
				if u > math.MaxInt { // (astro-analyzer fork)
					return fmt.Errorf("%w: card [%s] value %d overflows int", ErrFITS, card.Name, u)
				}
				card.Value = int(u)
			case reflect.Float32, reflect.Float64:
				card.Value = rv.Float()
			case reflect.Complex64, reflect.Complex128:
				card.Value = rv.Complex()
			case reflect.String:
				card.Value = rv.String() // named string types too
			case reflect.Bool:
				card.Value = rv.Bool()
			default:
				return fmt.Errorf(
					"%w: invalid value type (%T) for card [%s]", ErrFITS,
					card.Value, card.Name,
				)
			}
		}
		hcards = append(hcards, card)
	}

	hdr.cards = append(hcards, hdr.cards...)

	return err
}

// get returns the Card (and its index) with name n if it exists.
func (hdr *Header) get(n string) (int, *Card) {
	for i := range hdr.cards {
		c := &hdr.cards[i]
		if n == c.Name {
			return i, c
		}
	}

	return -1, nil
}
