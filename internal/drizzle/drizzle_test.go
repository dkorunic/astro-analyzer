// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package drizzle

import (
	"math"
	"strings"
	"testing"

	"github.com/dkorunic/astro-analyzer/internal/fits"
)

// TestCorrelation checks eq. 9 against the paper's worked example.
func TestCorrelation(t *testing.T) {
	if r := correlation(0.6, 0.5); math.Abs(r-1.662) > 0.001 {
		t.Errorf("R(p=0.6, s=0.5) = %.4f, want 1.662 (Fruchter & Hook)", r)
	}
	if r := correlation(0, 0.5); r != 1 {
		t.Errorf("R(p=0) = %g, want 1: interlacing has no correlated noise", r)
	}
}

// TestCoverage: a four-point half-pixel interlace fills a 2x grid exactly;
// four frames at the same position leave holes.
func TestCoverage(t *testing.T) {
	interlace := [][2]float64{{0, 0}, {0.5, 0}, {0, 0.5}, {0.5, 0.5}}
	if rms := coverage(interlace, 1, 2, 0.5); rms > 1e-12 {
		t.Errorf("interlace rms = %g, want 0", rms)
	}
	same := [][2]float64{{0.25, 0.25}, {0.25, 0.25}, {0.25, 0.25}, {0.25, 0.25}}
	if rms := coverage(same, 1, 2, 0.5); rms < 1 {
		t.Errorf("undithered rms = %g, want large (holes)", rms)
	}
}

// TestCoverageGrid: phases are FITS coordinates, pixel centres at integers.
// At Scale 2 the output edges at i/2 and −½+i/2 coincide, so only Scale 3
// and CFA Scale 1 show whether the grid sits on the real pixel edges.
func TestCoverageGrid(t *testing.T) {
	// Even whole-pixel dithers put each colour on the same sensor pixel of
	// every 2x2 cell: ¾ of the CFA output gets nothing.
	even := [][2]float64{{1, 1}, {3, 1}, {1, 5}, {7, 3}}
	if rms := coverage(even, 2, 1, 1); !math.IsInf(rms, 1) {
		t.Errorf("CFA even dithers rms = %g, want +Inf", rms)
	}
	// Half-pixel offsets split every drop over four sensor pixels evenly.
	half := [][2]float64{{1.5, 1.5}, {3.5, 1.5}}
	if rms := coverage(half, 2, 1, 1); rms > 1e-12 {
		t.Errorf("CFA half-pixel rms = %g, want 0", rms)
	}
	// Scale 3, drop 1/3 centred on each output pixel centre: exact fill.
	var third [][2]float64
	for _, d := range []float64{-1.0 / 3, 0, 1.0 / 3} {
		for _, e := range []float64{-1.0 / 3, 0, 1.0 / 3} {
			third = append(third, [2]float64{1 + d, 1 + e})
		}
	}
	if rms := coverage(third, 1, 3, 1.0/3); rms > 1e-6 {
		t.Errorf("scale 3 interlace rms = %g, want 0", rms)
	}
}

// solved returns n plate-solved frames on a w×h sensor, shifted by
// (dx·i, dy·i) pixels and rotated by rot·i degrees.
func solved(n int, fwhm, dx, dy, rot float64, cfa bool) []Frame {
	frames := make([]Frame, n)
	for i := range frames {
		a := (10 + rot*float64(i)) * math.Pi / 180
		s, c := math.Sincos(a)
		const scale = 0.000342
		frames[i] = Frame{FWHM: fwhm, CFA: cfa, WCS: fits.WCS{
			OK:    true,
			NAXIS: [2]int{3856, 2180},
			CRVAL: [2]float64{350.2, 61.2},
			CRPIX: [2]float64{1928 + dx*float64(i), 1090 + dy*float64(i)},
			CD:    [2][2]float64{{-scale * c, -scale * s}, {-scale * s, scale * c}},
		}}
	}

	return frames
}

func TestRecommend(t *testing.T) {
	cases := []struct {
		name   string
		frames []Frame
		scale  int
		reason string // substring; empty when drizzle is recommended
	}{
		{"few frames", solved(10, 1.5, 0.37, 0.61, 0, false), 0, "only 10 frames"},
		{"not dithered", solved(40, 1.5, 0, 0, 0, false), 0, "not dithered"},
		{"well sampled mono", solved(40, 4, 0.37, 0.61, 0, false), 0, "well sampled"},
		{"below F&H's 2.5 px", solved(60, 2.2, 0.37, 0.61, 0, false), 2, ""},
		{"undersampled mono", solved(60, 1.5, 0.37, 0.61, 0, false), 2, ""},
		{"very undersampled mono", solved(200, 0.8, 0.37, 0.61, 0, false), 3, ""},
		{"few colour frames", solved(30, 1.5, 0.37, 0.61, 0, true), 0, "only 30 frames; drizzle needs 45+"},
		{"well sampled colour", solved(120, 5, 0.37, 0.61, 1.7, true), 1, ""}, // 60 frames: 22% at one corner
		{"undersampled colour", solved(300, 1.5, 0.37, 0.61, 0.3, true), 2, ""},
		{"no plate solutions", func() []Frame {
			f := solved(60, 1.5, 0, 0, 0, false)
			for i := range f {
				f[i].WCS = fits.WCS{}
			}

			return f
		}(), 2, ""},
	}
	for _, c := range cases {
		r := Recommend(c.frames)
		t.Logf("%s: %+v", c.name, r)
		if c.reason != "" {
			if !strings.Contains(r.Reason, c.reason) || r.Scale != 0 {
				t.Errorf("%s: reason %q scale %d, want %q", c.name, r.Reason, r.Scale, c.reason)
			}

			continue
		}
		if r.Reason != "" || r.Scale != c.scale || !(r.WeightRMS <= maxRMS) || r.DropShrink > 1 ||
			r.DropShrink < max(minDrop, minRatio/float64(r.Scale))-1e-9 && r.Scale > 1 {
			t.Errorf("%s: %+v, want scale %d with even coverage", c.name, r, c.scale)
		}
		if c.frames[0].CFA && r.Scale == 1 && r.DropShrink != 1 {
			t.Errorf("%s: CFA drizzle at scale 1 should use drop shrink 1, got %g", c.name, r.DropShrink)
		}
	}
}

// TestUnsolvedFrame: one frame whose WCS is a mount pointing guess 50 px off
// must not make an undithered session look dithered.
func TestUnsolvedFrame(t *testing.T) {
	f := solved(40, 1.5, 0, 0, 0, false)
	f[0].WCS.CRPIX[0] += 50
	f[0].WCS.OK = false
	if r := Recommend(f); !strings.Contains(r.Reason, "not dithered") || !strings.Contains(r.Dither, "1 unsolved") {
		t.Errorf("%+v, want not dithered with the unsolved frame left out", r)
	}
}

// TestScaleFor: drizzle keeps the detector pixel's blur (F&H eq. 1), so a
// 1.2 px star drizzled at Scale 2 already spans 2.5 output pixels; naive
// ⌈2.5/1.2⌉ = 3 would only add noise.
func TestScaleFor(t *testing.T) {
	for fwhm, want := range map[float64]int{2.4: 2, 1.2: 2, 0.8: 3, 0.3: 3} {
		if got := scaleFor(fwhm); got != want {
			t.Errorf("scaleFor(%g) = %d, want %d", fwhm, got, want)
		}
	}
}
