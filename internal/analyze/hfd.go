// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package analyze

import (
	"math"

	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/stats"
)

// HFD follows ASTAP's procedure HFD (astap_main.pas:17027, full-flux
// branch) and analyse_image (unit_stack.pas:1734): a star counts when its
// HFD is 0.8–30 px and its SNR over 10, the minimum the stack tab's
// analysis passes.
const (
	hfdBox    = 14 // detection box half-size, the annulus inside it
	hfdMinSNR = 10
	hfdMax    = 30
)

// hfd is ASTAP's half-flux diameter of the star near c, in analysed
// pixels; NaN when ASTAP would reject the star. It is Miyashita's
// estimator, twice the flux-weighted mean distance from the centroid, over
// the square out to where the pixels 3σ over the annulus sky give out. It
// needs no model, so it holds for defocused doughnuts, and it is the number
// SGP and ASTAP show. N.I.N.A. measures its own HFR: see ninaHFR.
//
// ponytail: summed over a square, as ASTAP does: corners add a little sky
// noise on faint stars.
func hfd(img fits.Image, c int) float64 {
	s := hfdStar{img: img, x: c % img.W, y: c / img.W, k: 1}
	if s.x-hfdBox-1 <= 0 || s.x+hfdBox+1 >= img.W-1 || s.y-hfdBox-1 <= 0 || s.y+hfdBox+1 >= img.H-1 {
		return math.NaN()
	}
	// ASTAP loads every image on a 0–65535 scale, and its noise floor of
	// 1 and its SNR (photon noise in those units) assume that scale.
	if img.Range > 0 {
		s.k = 65535 / img.Range
	}
	s.sky()
	xc, yc, rs, ok := s.centroid()
	if !ok {
		return math.NaN()
	}
	ra, ok := s.edge(xc, yc, rs)
	if !ok {
		return math.NaN()
	}
	var flux, fr float64
	for j := -ra; j <= ra; j++ {
		for i := -ra; i <= ra; i++ {
			v := s.sub(xc+float64(i), yc+float64(j)) - s.bg
			flux += v
			fr += v * math.Hypot(float64(i), float64(j))
		}
	}
	d := 2 * fr / max(flux, 1e-5)
	// ASTAP's SNR, in ADU: its acceptance, not our aperture SNR.
	if flux < 1 || flux/math.Sqrt(flux+float64(ra*ra)*math.Pi*s.sd*s.sd) <= hfdMinSNR || d <= 0.8 || d > hfdMax {
		return math.NaN()
	}

	return d
}

// hfdStar is one star being measured: its peak pixel, the scale k to
// ASTAP's units, and the annulus sky and its σ.
type hfdStar struct {
	img       fits.Image
	x, y      int
	k, bg, sd float64
}

func (s *hfdStar) at(x, y int) float64 { return s.k * float64(s.img.Pix[y*s.img.W+x]) }

// sub samples bilinearly, as the centroid sits between pixels; 0 off the
// frame.
func (s *hfdStar) sub(x, y float64) float64 {
	xt, yt := int(x), int(y)
	if xt <= 0 || xt >= s.img.W-2 || yt <= 0 || yt >= s.img.H-2 {
		return 0
	}
	fx, fy := x-float64(xt), y-float64(yt)

	return s.at(xt, yt)*(1-fx)*(1-fy) + s.at(xt+1, yt)*fx*(1-fy) + s.at(xt, yt+1)*(1-fx)*fy + s.at(xt+1, yt+1)*fx*fy
}

// hfdRing holds the offsets of the one-pixel annulus just outside the box.
var hfdRing = func() [][2]int {
	var o [][2]int
	for j := -hfdBox - 1; j <= hfdBox+1; j++ {
		for i := -hfdBox - 1; i <= hfdBox+1; i++ {
			if d := i*i + j*j; d > hfdBox*hfdBox && d <= (hfdBox+1)*(hfdBox+1) {
				o = append(o, [2]int{i, j})
			}
		}
	}

	return o
}()

// sky sets bg and sd from the annulus: median and MAD, σ at least 1.
func (s *hfdStar) sky() {
	ring := make([]float64, len(hfdRing))
	for k, o := range hfdRing {
		ring[k] = s.at(s.x+o[0], s.y+o[1])
	}
	s.bg = stats.SelectMedian(ring)
	for k := range ring {
		ring[k] = math.Abs(ring[k] - s.bg)
	}
	s.sd = max(stats.SelectMedian(ring)*1.4826, 1)
}

// centroid is the flux-weighted centre of the pixels 3σ over the sky,
// shrinking the box until they fill 2/9 of it, which drops neighbouring
// stars; it returns the box half-size plus 2 for the next step. The 3σ
// pixels are found once, in scan order, so each box sums them as ASTAP's
// rescan does.
func (s *hfdStar) centroid() (float64, float64, int, bool) {
	type px struct {
		i, j int
		v    float64
	}
	lit := make([]px, 0, 64)
	for j := -hfdBox; j <= hfdBox; j++ {
		for i := -hfdBox; i <= hfdBox; i++ {
			if v := s.at(s.x+i, s.y+j) - s.bg; v > 3*s.sd {
				lit = append(lit, px{i: i, j: j, v: v})
			}
		}
	}
	w, h := s.img.W, s.img.H
	for rs := hfdBox; ; {
		var sum, sx, sy float64
		n := 0
		for _, p := range lit {
			if p.i >= -rs && p.i <= rs && p.j >= -rs && p.j <= rs {
				sum, sx, sy, n = sum+p.v, sx+p.v*float64(p.i), sy+p.v*float64(p.j), n+1
			}
		}
		if sum <= 12*s.sd || n <= 1 {
			return 0, 0, 0, false
		}
		xc, yc, r := float64(s.x)+sx/sum, float64(s.y)+sy/sum, float64(rs)
		if xc-r < 0 || xc+r > float64(w-1) || yc-r < 0 || yc+r > float64(h-1) {
			return 0, 0, 0, false
		}
		if float64(n) >= 2.0/9*float64((2*rs+1)*(2*rs+1)) || rs <= 1 {
			return xc, yc, rs + 2, true
		}
		if rs > 4 {
			rs -= 2
		} else {
			rs--
		}
	}
}

// edge is the star's radius: the first ring after the signal starts with
// under 10% of the fullest ring's 3σ pixels. A doughnut's dark centre is
// skipped, not taken for the edge. False when the star fills the box or
// its pixels are too sparse for a disc (scattered stars).
func (s *hfdStar) edge(xc, yc float64, rs int) (int, bool) {
	hist := make([]int, rs+1)
	for j := -rs; j <= rs; j++ {
		for i := -rs; i <= rs; i++ {
			if d := int(math.Round(math.Sqrt(float64(i*i + j*j)))); d <= rs && s.sub(xc+float64(i), yc+float64(j))-s.bg > 3*s.sd {
				hist[d]++
			}
		}
	}
	ra, top, lit, started := -1, 0, 0, false
	for {
		ra++
		lit += hist[ra]
		started = started || hist[ra] > 0
		top = max(top, hist[ra])
		if ra >= rs || started && float64(hist[ra]) <= 0.1*float64(top) {
			break
		}
	}

	return ra, ra < rs && (ra <= 2 || float64(lit) >= 0.35*float64((2*ra-2)*(2*ra-2)))
}
