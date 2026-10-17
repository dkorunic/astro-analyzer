// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

// Package synth renders synthetic star fields with known properties for
// tests. Stars integrate their profile over each pixel's area, like a real
// sensor; that matters for undersampled stars.
package synth

import (
	"math"
	"math/rand/v2"
	"os"

	"codeberg.org/astrogo/fitsio"
	"github.com/dkorunic/astro-analyzer/internal/fits"
)

// Field geometry: NStars stars on a W×H frame with background Bg and
// Gaussian noise of standard deviation Noise.
const (
	W      = 600
	H      = 400
	Bg     = 1000.0
	Noise  = 10.0
	NStars = 20
)

// StarField renders NStars Gaussian stars with per-axis FWHM fx, fy and peak
// 3000 above background.
func StarField(fx, fy float64) []float64 {
	k := 2 * math.Sqrt(2*math.Ln2)
	sx, sy := fx/k, fy/k
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // seeded on purpose: tests need repeatable noise
	pix := make([]float64, W*H)
	for i := range pix {
		pix[i] = Bg + rng.NormFloat64()*Noise
	}
	for k := range NStars {
		cx, cy := 50+float64(k%5)*120+0.3, 50+float64(k/5)*90+0.7
		for y := int(cy) - 15; y <= int(cy)+15; y++ {
			for x := int(cx) - 15; x <= int(cx)+15; x++ {
				pix[y*W+x] += 3000 * Span(float64(x)-cx, sx) * Span(float64(y)-cy, sy)
			}
		}
	}

	return pix
}

// Span is the integral of exp(-t²/2s²) over [d-½, d+½], normalised to 1 at
// the peak of a well-sampled star.
func Span(d, s float64) float64 {
	k := s * math.Sqrt2

	return s * math.Sqrt(math.Pi/2) * (math.Erf((d+0.5)/k) - math.Erf((d-0.5)/k))
}

// MoffatField renders NStars circular Moffat stars, each pixel integrated on
// a 10x10 sub-grid.
func MoffatField(fwhm, beta float64) fits.Image {
	alpha := fwhm / (2 * math.Sqrt(math.Pow(2, 1/beta)-1))
	img := fits.Image{W: W, H: H, Pix: make([]float32, W*H), Scale: 1, Range: 65535}
	rng := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // seeded on purpose: tests need repeatable noise
	for i := range img.Pix {
		img.Pix[i] = float32(Bg + rng.NormFloat64()*Noise)
	}
	for k := range NStars {
		cx, cy := 50+float64(k%5)*120+0.3, 50+float64(k/5)*90+0.7
		for y := int(cy) - 25; y <= int(cy)+25; y++ {
			for x := int(cx) - 25; x <= int(cx)+25; x++ {
				var s float64
				for j := range 10 {
					for i := range 10 {
						dx := float64(x) - 0.45 + float64(i)/10 - cx
						dy := float64(y) - 0.45 + float64(j)/10 - cy
						s += math.Pow(1+(dx*dx+dy*dy)/(alpha*alpha), -beta)
					}
				}
				img.Pix[y*W+x] += float32(3000 * s / 100)
			}
		}
	}

	return img
}

// Image wraps W×H pixel values as an unbinned fits.Image.
func Image(pix []float64) fits.Image {
	img := fits.Image{W: W, H: H, Pix: make([]float32, len(pix)), Scale: 1, Range: 65535}
	for i, v := range pix {
		img.Pix[i] = float32(v)
	}

	return img
}

// WriteFITS stores pix as a 16-bit unsigned frame (BZERO=32768, as cameras
// write) with the given extra header cards.
func WriteFITS(path string, pix []float64, cards ...fitsio.Card) error {
	return WriteFITSSize(path, W, H, pix, cards...)
}

// WriteFITSSize is WriteFITS for a w×h frame. Values are clipped to the
// 16-bit range, as a sensor saturates.
func WriteFITSSize(path string, w, h int, pix []float64, cards ...fitsio.Card) error {
	data := make([]int16, len(pix))
	for i, v := range pix {
		data[i] = int16(math.Round(min(max(v, 0), 65535)) - 32768)
	}
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	f, err := fitsio.Create(fh)
	if err != nil {
		return err
	}
	img := fitsio.NewImage(16, []int{w, h})
	if err := img.Header().Append(append([]fitsio.Card{{Name: "BZERO", Value: 32768}, {Name: "BSCALE", Value: 1}}, cards...)...); err != nil {
		return err
	}
	if err := img.Write(&data); err != nil {
		return err
	}
	if err := f.Write(img); err != nil {
		return err
	}

	return f.Close()
}

// Crowded renders a realistic w×h field: n stars of the given FWHM at random
// positions, on a sky like a light-polluted 15 s one-shot-colour frame.
// Brightness follows a power law, N(>A) ∝ A^-gamma, as real star counts do:
// most stars are near the detection limit and a few saturate.
func Crowded(w, h, n int, fwhm, gamma float64) []float64 {
	const sky, noise = 6000.0, 250.0
	rng := rand.New(rand.NewPCG(9, 10)) //nolint:gosec // seeded on purpose: repeatable benchmark input
	pix := make([]float64, w*h)
	for i := range pix {
		pix[i] = sky + rng.NormFloat64()*noise
	}
	s := fwhm / (2 * math.Sqrt(2*math.Ln2))
	r := int(math.Ceil(4 * s))
	for range n {
		cx := float64(r) + rng.Float64()*float64(w-2*r-1)
		cy := float64(r) + rng.Float64()*float64(h-2*r-1)
		amp := min(200000, 2*noise*math.Pow(1-rng.Float64(), -1/gamma))
		for y := int(cy) - r; y <= int(cy)+r; y++ {
			for x := int(cx) - r; x <= int(cx)+r; x++ {
				pix[y*w+x] += amp * Span(float64(x)-cx, s) * Span(float64(y)-cy, s)
			}
		}
	}

	return pix
}
