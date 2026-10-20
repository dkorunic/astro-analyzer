// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package analyze

import (
	"math"
	"slices"

	"github.com/dkorunic/astro-analyzer/internal/stats"
)

// Trail detection, after ASTAP's (unit_contour.pas:107: blur, threshold,
// keep blobs with length² / area > 3), with second moments instead of the
// contour's extent, so a round star or a blob of them never passes.
const (
	trailBin    = 4   // pixels binned per side: a faint trail sums along its width
	trailSigma  = 3   // threshold over the local sky, binned noise σ
	trailBlock  = 16  // local sky: the median of trailBlock² binned pixels
	trailAspect = 8   // length / width: a star halo is 1, a nebula filament wide
	trailLen    = 0.1 // shortest trail, of the frame's shorter side
	// Segments of one trail, broken where it fades, lie on one line: within
	// trailAngle radians of each other, each centroid within trailOffset
	// binned pixels of the other's line plus trailAngle/2 of their distance.
	// A segment's angle is good to about width / length, 1/trailAspect.
	trailAngle  = 1.0 / trailAspect
	trailOffset = 3
)

// trails counts satellite, aircraft and meteor trails: long, thin, straight
// blobs over the local sky in an image binned trailBin×trailBin, each bin
// summed. Collinear segments count once. The local sky is a block median,
// which stars and a narrow trail leave alone; the wavelet background map
// holds bright stars' halos. The threshold takes the frame's noise σ, summed
// over a bin σ·trailBin, not the binned image's spread: faint stars widen
// that in a crowded field.
//
// ponytail: a trail fainter than twice the pixel noise, or crossing under
// a nebula's filament, is missed, and two trails crossing join into one
// blob that is not thin: a Hough transform would split it. A straight,
// narrow filament (a galaxy seen edge-on that fills the frame) counts as
// one, the same in every frame, so it flags nothing.
func trails(pix []float32, w, h int, noise float64) int {
	bw, bh := w/trailBin, h/trailBin
	if bw < 2*trailBlock || bh < 2*trailBlock || !(noise > 0) {
		return 0
	}
	res := make([]float64, bw*bh)
	for y := range bh * trailBin {
		for x := range bw * trailBin {
			res[(y/trailBin)*bw+x/trailBin] += float64(pix[y*w+x])
		}
	}
	skyBlocks(res, bw, bh)
	thr := trailSigma * noise * trailBin
	minLen := trailLen * float64(min(bw, bh))

	var lines [][3]float64 // angle, centroid x and y
	seen := make([]bool, len(res))
	var stack []int
	for start, v := range res {
		if seen[start] || !(v > thr) {
			continue
		}
		// 8-connected flood fill, accumulating the blob's moments.
		var n, sx, sy, sxx, syy, sxy float64
		seen[start] = true
		stack = append(stack[:0], start)
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := float64(p%bw), float64(p/bw)
			n, sx, sy, sxx, syy, sxy = n+1, sx+x, sy+y, sxx+x*x, syy+y*y, sxy+x*y
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					qx, qy := p%bw+dx, p/bw+dy
					if qx < 0 || qy < 0 || qx >= bw || qy >= bh {
						continue
					}
					if q := qy*bw + qx; !seen[q] && res[q] > thr {
						seen[q] = true
						stack = append(stack, q)
					}
				}
			}
		}
		if n < minLen {
			continue
		}
		mx, my := sx/n, sy/n
		cxx, cyy, cxy := sxx/n-mx*mx, syy/n-my*my, sxy/n-mx*my
		// Eigenvalues of the covariance; a uniform line of length L has
		// variance L²/12 along it.
		mid, d := (cxx+cyy)/2, math.Hypot((cxx-cyy)/2, cxy)
		length, width := math.Sqrt(12*(mid+d)), math.Sqrt(12*max(mid-d, 0))
		if length < minLen || length < trailAspect*max(width, 1) {
			continue
		}
		l := [3]float64{math.Atan2(2*cxy, cxx-cyy) / 2, mx, my} // the major axis, −π/2..π/2
		if !slices.ContainsFunc(lines, func(m [3]float64) bool { return collinear(l, m) }) {
			lines = append(lines, l)
		}
	}

	return len(lines)
}

// skyBlocks subtracts from each pixel of the bw×bh image res the median of
// its trailBlock×trailBlock block; the last row and column of blocks take
// the remainder.
func skyBlocks(res []float64, bw, bh int) {
	nx, ny := bw/trailBlock, bh/trailBlock
	block := make([]float64, 0, 4*trailBlock*trailBlock)
	for by := range ny {
		y0, y1 := by*trailBlock, (by+1)*trailBlock
		if by == ny-1 {
			y1 = bh
		}
		for bx := range nx {
			x0, x1 := bx*trailBlock, (bx+1)*trailBlock
			if bx == nx-1 {
				x1 = bw
			}
			block = block[:0]
			for y := y0; y < y1; y++ {
				block = append(block, res[y*bw+x0:y*bw+x1]...)
			}
			sky := stats.MedianOf(block)
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					res[y*bw+x] -= sky
				}
			}
		}
	}
}

// collinear reports whether segments a and b (angle, centroid x and y) lie
// on one line.
func collinear(a, b [3]float64) bool {
	da := math.Abs(a[0] - b[0])
	da = min(da, math.Pi-da) // angles wrap at ±π/2
	dx, dy := b[1]-a[1], b[2]-a[2]
	tol := trailOffset + math.Hypot(dx, dy)*trailAngle/2
	off := func(theta float64) float64 { return math.Abs(-dx*math.Sin(theta) + dy*math.Cos(theta)) }

	return da < trailAngle && off(a[0]) < tol && off(b[0]) < tol
}
