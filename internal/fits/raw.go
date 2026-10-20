// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package fits

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/dkorunic/astro-analyzer/third_party/fitsio"
)

// ErrRaw marks a camera RAW file dcraw could not read, or no dcraw.
var (
	ErrRaw = errors.New("cannot read camera RAW")
	errPGM = errors.New("not a binary PGM")
)

// rawExt lists camera RAW extensions dcraw reads.
var rawExt = map[string]bool{
	".cr2": true, ".crw": true, ".nef": true, ".nrw": true, ".arw": true, ".srf": true, ".sr2": true,
	".raf": true, ".orf": true, ".rw2": true, ".pef": true, ".dng": true, ".srw": true, ".3fr": true,
	".erf": true, ".kdc": true, ".mrw": true, ".x3f": true, ".iiq": true, ".mef": true, ".mos": true,
}

// openRaw reads a camera RAW file with dcraw, as ASTAP does (convert_raw):
// -D gives the undemosaiced, unscaled CFA mosaic of the visible area, -4
// 16 bits, -t 0 no rotation; dcraw -i -v gives the metadata. Rows stay
// top first, so BAYERPAT is dcraw's pattern as it prints it.
//
// ponytail: Coffin's dcraw 9.28 (2018) predates Canon CR3 and some newer
// bodies; LibRaw's tools read more but write only full-sensor files,
// masked borders included. The timestamp is the camera's clock, taken
// for UTC. DSLR frames carry no frame type: calibration frames read as
// lights (bias by their zero exposure).
func openRaw(path string) (source, error) {
	info, err := exec.CommandContext(context.Background(), "dcraw", "-i", "-v", path).Output()
	if err != nil {
		return source{}, fmt.Errorf("%w %s: dcraw -i: %w", ErrRaw, path, err)
	}
	cards := rawCards(info)
	data, err := exec.CommandContext(context.Background(), "dcraw", "-D", "-4", "-t", "0", "-c", path).Output()
	if err != nil {
		return source{}, fmt.Errorf("%w %s: dcraw: %w", ErrRaw, path, err)
	}
	pix, w, h, err := pgm(data)
	if err != nil {
		return source{}, fmt.Errorf("%w %s: %w", ErrRaw, path, err)
	}
	read := func() ([]float32, int, error) { return pix, 1, nil }

	return source{hdr: header(cards, 16, []int{w, h}), read: read, done: func() {}}, nil
}

// rawCards turns dcraw -i -v output into FITS cards.
func rawCards(info []byte) []fitsio.Card {
	var cards []fitsio.Card
	add := func(k string, v any) { cards = append(cards, fitsio.Card{Name: k, Value: v}) }
	for line := range strings.Lines(string(info)) {
		key, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch key {
		case "Timestamp":
			if t, err := time.Parse("Mon Jan _2 15:04:05 2006", v); err == nil {
				add("DATE-OBS", t.Format("2006-01-02T15:04:05"))
			}
		case "Camera":
			add("INSTRUME", v)
		case "ISO speed":
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				add("ISOSPEED", f)
			}
		case "Shutter": // "30.0 sec" or "1/200.0 sec"
			s := strings.TrimSuffix(v, " sec")
			num, den, frac := strings.Cut(s, "/")
			n, err := strconv.ParseFloat(num, 64)
			if frac {
				d, err2 := strconv.ParseFloat(den, 64)
				if err2 != nil || d == 0 {
					continue
				}
				n /= d
			}
			if err == nil {
				add("EXPTIME", n)
			}
		case "Focal length": // the lens's; 0 on a telescope
			if f, err := strconv.ParseFloat(strings.TrimSuffix(v, " mm"), 64); err == nil && f > 0 {
				add("FOCALLEN", f)
			}
		case "Filter pattern": // "RG/GB", or 16 letters for 8 rows of 2
			if p := strings.ReplaceAll(v, "/", ""); len(p) >= 4 && strings.Trim(p[:4], "RGB") == "" {
				add("BAYERPAT", p[:4])
			}
		}
	}

	return cards
}

// pgm decodes a binary PGM (P5), 8 or 16 bits.
func pgm(b []byte) ([]float32, int, int, error) {
	r := bufio.NewReader(bytes.NewReader(b))
	var magic string
	var w, h, maxv int
	if _, err := fmt.Fscan(r, &magic, &w, &h, &maxv); err != nil || magic != "P5" || w <= 0 || h <= 0 {
		return nil, 0, 0, fmt.Errorf("%w: %q", errPGM, magic)
	}
	if _, err := r.ReadByte(); err != nil { // the one whitespace before the data
		return nil, 0, 0, err
	}
	size := 1
	if maxv > 255 {
		size = 2
	}
	data := make([]byte, w*h*size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, 0, 0, err
	}
	pix := make([]float32, w*h)
	for i := range pix {
		if size == 2 {
			pix[i] = float32(uint16(data[2*i])<<8 | uint16(data[2*i+1]))
		} else {
			pix[i] = float32(data[i])
		}
	}

	return pix, w, h, nil
}
