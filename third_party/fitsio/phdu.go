// Copyright 2015 The astrogo Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fitsio

import (
	"reflect"
	"slices"
)

type primaryHDU struct {
	ImageHDU
}

// Name returns the value of the 'EXTNAME' Card (or "PRIMARY" if none).
func (hdu *primaryHDU) Name() string {
	if card := hdu.hdr.Get("EXTNAME"); card != nil {
		if name, ok := card.Value.(string); ok {
			return name
		}
	}

	return "PRIMARY"
}

// Version returns the value of the 'EXTVER' Card (or 1 if none).
func (hdu *primaryHDU) Version() int {
	card := hdu.hdr.Get("EXTVER")
	if card == nil {
		return 1
	}
	rv := reflect.ValueOf(card.Value)

	return int(rv.Int())
}

// NewPrimaryHDU creates a new PrimaryHDU with Header hdr.
// If hdr is nil, a default Header will be created.
//
//nolint:ireturn // HDUs differ in type: the interface is the API
func NewPrimaryHDU(hdr *Header) (Image, error) {
	var err error

	if hdr == nil {
		hdr = NewDefaultHeader()
	}

	// add default cards (SIMPLE, BITPIX, NAXES, AXIS1, AXIS2)
	keys := make(map[string]struct{}, len(hdr.cards))
	for i := range hdr.cards {
		card := &hdr.cards[i]
		k := card.Name
		keys[k] = struct{}{}
	}

	cards := make([]Card, 0, 3)
	if _, ok := keys[kwSimple]; !ok {
		cards = append(cards, Card{
			Name:    kwSimple,
			Value:   true,
			Comment: "primary HDU",
		})
	}

	if _, ok := keys[kwBitpix]; !ok {
		cards = append(cards, Card{
			Name:    kwBitpix,
			Value:   hdr.Bitpix(),
			Comment: cmBitpix,
		})
	}

	if _, ok := keys[kwNaxis]; !ok {
		cards = append(cards, Card{
			Name:    kwNaxis,
			Value:   len(hdr.Axes()),
			Comment: cmNaxis,
		})
	}

	phdr := *hdr
	phdr.cards = slices.Concat(cards, hdr.cards)
	hdu := &primaryHDU{
		ImageHDU{
			hdr: phdr,
			raw: make([]byte, 0),
		},
	}

	return hdu, err
}
