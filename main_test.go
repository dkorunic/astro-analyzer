// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"

	"codeberg.org/astrogo/fitsio"
	"github.com/dkorunic/astro-analyzer/internal/analyze"
	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/psf"
	"github.com/dkorunic/astro-analyzer/internal/synth"
)

// TestEndToEnd scans a directory, skipping non-FITS and calibration files,
// then loads and measures the light frame it finds.
func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	light := filepath.Join(dir, "sub", "light.FITS")
	err := synth.WriteFITS(light, synth.StarField(4, 4),
		fitsio.Card{Name: "XPIXSZ", Value: 3.76},
		fitsio.Card{Name: "FOCALLEN", Value: 400.0})
	if err != nil {
		t.Fatal(err)
	}
	bias := filepath.Join(dir, "bias.fit")
	if err := synth.WriteFITS(bias, make([]float64, synth.W*synth.H), fitsio.Card{Name: "IMAGETYP", Value: "BIAS"}); err != nil {
		t.Fatal(err)
	}

	files, _, err := expand([]string{dir}, nil)
	if err != nil || len(files) != 2 || files[0] != bias || files[1] != light {
		t.Fatalf("expand = %v, %v; want [%s %s]", files, err, bias, light)
	}
	if _, err := fits.Load(bias); err != fits.ErrNotLight {
		t.Errorf("bias: %v, want ErrNotLight", err)
	}
	img, err := fits.Load(light)
	if err != nil {
		t.Fatal(err)
	}
	r := analyze.Frame(img, 5, psf.Moffat4)
	if r.Stars != synth.NStars || math.Abs(r.FWHMArc-r.FWHM*206.265*3.76/400) > 1e-9 {
		t.Errorf("stars %d fwhm %.3f px / %.3f arcsec", r.Stars, r.FWHM, r.FWHMArc)
	}
}

// TestExpandHiddenAndDuplicates: macOS AppleDouble files ("._x.fits") and
// hidden directories are skipped, and a frame named twice is kept once.
func TestExpandHiddenAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	light := filepath.Join(dir, "light.fits")
	for _, p := range []string{light, filepath.Join(dir, "._light.fits"), filepath.Join(dir, ".Trashes", "old.fits")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	files, _, err := expand([]string{dir, light, dir + "/./light.fits"}, nil)
	if err != nil || len(files) != 1 || files[0] != light {
		t.Fatalf("expand = %v, %v; want [%s]", files, err, light)
	}
}

// TestExpandUnreadable: an unreadable directory below the named one, such as
// a volume root's .DocumentRevisions-V100, is skipped and reported, and the
// rest of the tree is still searched.
func TestExpandUnreadable(t *testing.T) {
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	light := filepath.Join(dir, "light.fits")
	if err := os.WriteFile(light, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("directory permissions not enforced (running as root?)")
	}

	files, unread, err := expand([]string{dir}, nil)
	if err != nil || len(files) != 1 || files[0] != light || len(unread) != 1 || !errors.Is(unread[0], fs.ErrPermission) {
		t.Fatalf("expand = %v, unread %v, %v; want [%s] and one permission error", files, unread, err, light)
	}
}
