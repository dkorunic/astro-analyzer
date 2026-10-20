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

	"github.com/dkorunic/astro-analyzer/internal/analyze"
	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/psf"
	"github.com/dkorunic/astro-analyzer/internal/report"
	"github.com/dkorunic/astro-analyzer/internal/synth"
	"github.com/dkorunic/astro-analyzer/third_party/fitsio"
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

// TestCollect: lights become frames, calibration files calibration
// frames, stacks are dropped, and a load error sets the exit status.
func TestCollect(t *testing.T) {
	files := []string{"light.fits", "bias.fits", "stack.fits", "bad.fits"}
	errs := []error{nil, fits.ErrNotLight, fits.ErrNotLight, errors.New("truncated")}
	res := make([]analyze.Result, 4)
	res[0].Stars = 7
	cals := make([]report.Calib, 4)
	cals[1].Image.Meta.Type, cals[2].Image.Meta.Type = fits.Bias, fits.Stack
	frames, calib, exit := collect(files, errs, res, cals)
	if len(frames) != 1 || frames[0].Path != "light.fits" || frames[0].Result.Stars != 7 {
		t.Errorf("frames = %+v, want light.fits", frames)
	}
	if len(calib) != 1 || calib[0].Image.Meta.Type != fits.Bias {
		t.Errorf("calibration frames = %+v, want the bias", calib)
	}
	if exit != 1 {
		t.Errorf("exit = %d, want 1", exit)
	}
	if _, _, exit := collect(files[:3], errs[:3], res[:3], cals[:3]); exit != 0 {
		t.Errorf("exit without load errors = %d, want 0", exit)
	}
}

// TestCalibFrame: a dark is measured as a light is, for its hot pixels,
// and every calibration frame's pixels are dropped.
func TestCalibFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dark.fits")
	pix := synth.StarField(4, 4)
	for _, x := range []int{110, 230, 350, 470} { // hot pixels, as in analyze's TestHotPixels
		for _, y := range []int{95, 185} {
			pix[y*synth.W+x] += 500
		}
	}
	if err := synth.WriteFITS(path, pix, fitsio.Card{Name: "IMAGETYP", Value: "DARK"}); err != nil {
		t.Fatal(err)
	}
	img, err := fits.Load(path)
	if !errors.Is(err, fits.ErrNotLight) || img.Pix == nil {
		t.Fatalf("dark: %v, %d pixels; want ErrNotLight with pixels", err, len(img.Pix))
	}
	want := analyze.Frame(img, 5, psf.Moffat4).Hot
	if !(want > 0) {
		t.Fatalf("dark hot pixel level %v, want > 0", want)
	}
	c := calibFrame(path, img, 5, psf.Moffat4)
	if c.Path != path || c.Hot != want || c.Image.Pix != nil || c.Image.Sat != nil {
		t.Errorf("calibFrame = %q, hot %v (want %v), %d pixels kept", c.Path, c.Hot, want, len(c.Image.Pix))
	}
	img.Meta.Type = fits.Bias
	if c := calibFrame(path, img, 5, psf.Moffat4); c.Hot != 0 {
		t.Errorf("bias hot = %v, want 0 (not measured)", c.Hot)
	}
}

// TestReadNoiseFor: the -read-noise flag wins; without it and without
// bias frames the preset decides (NaN).
func TestReadNoiseFor(t *testing.T) {
	if rn, src := readNoiseFor(1.2, nil, nil); rn != 1.2 || src != "-read-noise" {
		t.Errorf("flag: %v %q", rn, src)
	}
	if rn, src := readNoiseFor(0, nil, nil); !math.IsNaN(rn) || src != "" {
		t.Errorf("no flag, no bias: %v %q; want NaN", rn, src)
	}
}
