// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package analyze

import (
	"math"
	"testing"

	"github.com/dkorunic/astro-analyzer/internal/stats"
	"github.com/dkorunic/astro-analyzer/internal/synth"
)

// TestSeed checks the half-max FWHM estimate of large, bright stars is not
// shrunk by the star's own light in the smoothed background map.
func TestSeed(t *testing.T) {
	const fwhm = 10.0
	pix := synth.StarField(fwhm, fwhm)
	for i := range pix {
		pix[i] += 9 * (pix[i] - synth.Bg) // 30000 peak: the map holds ~3500 of it
	}
	img := synth.Image(pix)
	bg, std := stats.SkyStats(img.Pix)
	noise, local := stats.NoiseMRS(img.Pix, img.W, img.H, bg, std)
	c := 231*synth.W + 290 // nearest pixel to star 12 at (290.3, 230.7)
	_, fwhm0, _, _, _, ok := seed(img, c, float64(local[c]), noise, 0, make([]bool, len(img.Pix)))
	if !ok || math.Abs(fwhm0-fwhm)/fwhm > 0.05 {
		t.Errorf("seed fwhm = %.2f (ok %v), want %.0f within 5%%", fwhm0, ok, fwhm)
	}
}
