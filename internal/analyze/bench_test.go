// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package analyze_test

import (
	"path/filepath"
	"testing"

	"codeberg.org/astrogo/fitsio"
	"github.com/dkorunic/astro-analyzer/internal/analyze"
	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/psf"
	"github.com/dkorunic/astro-analyzer/internal/synth"
)

// BenchmarkPipeline measures one realistic frame end to end: FITS decoding,
// Bayer binning, wavelet noise, detection, aperture photometry and PSF fits.
// `task pgo` profiles it to produce default.pgo, so keep it representative
// of real sessions: a full-size raw colour frame with thousands of stars.
func BenchmarkPipeline(b *testing.B) {
	const w, h = 3856, 2180 // raw one-shot-colour frame (IMX678-class sensor)
	path := filepath.Join(b.TempDir(), "light.fits")
	// 7500 stars with brightness slope 1.5 match a real Bubble Nebula frame:
	// ~4500 detected, 15% of CPU in PSF fits (~440 ms per frame before the
	// wavelet and aperture-median speedups, ~325 ms after).
	err := synth.WriteFITSSize(path, w, h, synth.Crowded(w, h, 7500, 5, 1.5),
		fitsio.Card{Name: "BAYERPAT", Value: "GBRG"},
		fitsio.Card{Name: "EGAIN", Value: 0.28})
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		img, err := fits.Load(path)
		if err != nil {
			b.Fatal(err)
		}
		r := analyze.Frame(img, 5, psf.Moffat4)
		if r.Stars == 0 {
			b.Fatal("no stars detected")
		}
		b.ReportMetric(float64(r.Stars), "stars")
	}
}
