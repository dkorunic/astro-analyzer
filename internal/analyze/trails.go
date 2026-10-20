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
// contour's extent, so a round star or a blob of them never passes, and a
// Hough transform for blobs that are not thin, where trails cross.
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
	// A blob that is not thin is split into up to trailLines lines, each
	// the pixels within trailOffset of a Hough peak, which must account for
	// trailExplained of it: a star halo or nebula has chords, not lines.
	trailLines     = 4
	trailExplained = 0.8
	houghAngles    = 180 // 1° steps
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
// a nebula's filament or a bright star's halo (the blob is then mostly not
// line), is missed. A straight, narrow filament (a galaxy seen edge-on that
// fills the frame) counts as one, the same in every frame, so it flags
// nothing.
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
	add := func(l [3]float64) {
		if !slices.ContainsFunc(lines, func(m [3]float64) bool { return collinear(l, m) }) {
			lines = append(lines, l)
		}
	}
	seen := make([]bool, len(res))
	var stack, blob []int
	for start, v := range res {
		if seen[start] || !(v > thr) {
			continue
		}
		// 8-connected flood fill.
		seen[start] = true
		stack, blob = append(stack[:0], start), blob[:0]
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			blob = append(blob, p)
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
		if float64(len(blob)) < minLen {
			continue
		}
		if l, ok := segment(blob, bw, minLen); ok {
			add(l)

			continue
		}
		for _, l := range hough(blob, bw, minLen) {
			add(l)
		}
	}

	return len(lines)
}

// segment returns the line (angle, centroid x and y) of the pixels px of a
// bw-wide image, if they are long and thin: at least minLen long and
// trailAspect times longer than wide, by their second moments.
func segment(px []int, bw int, minLen float64) ([3]float64, bool) {
	var sx, sy, sxx, syy, sxy float64
	for _, p := range px {
		x, y := float64(p%bw), float64(p/bw)
		sx, sy, sxx, syy, sxy = sx+x, sy+y, sxx+x*x, syy+y*y, sxy+x*y
	}
	n := float64(len(px))
	mx, my := sx/n, sy/n
	cxx, cyy, cxy := sxx/n-mx*mx, syy/n-my*my, sxy/n-mx*my
	// Eigenvalues of the covariance; a uniform line of length L has
	// variance L²/12 along it.
	mid, d := (cxx+cyy)/2, math.Hypot((cxx-cyy)/2, cxy)
	length, width := math.Sqrt(12*(mid+d)), math.Sqrt(12*max(mid-d, 0))
	if length < minLen || length < trailAspect*max(width, 1) {
		return [3]float64{}, false
	}

	return [3]float64{math.Atan2(2*cxy, cxx-cyy) / 2, mx, my}, true // the major axis, −π/2..π/2
}

// hough splits a blob that is not thin, such as two trails crossing, into
// lines: the accumulator's peak, then its pixels within trailOffset, which
// must pass segment; those are removed and the next peak taken, up to
// trailLines. None unless the lines hold trailExplained of the blob.
func hough(px []int, bw int, minLen float64) [][3]float64 {
	var cx, cy float64
	for _, p := range px {
		cx, cy = cx+float64(p%bw), cy+float64(p/bw)
	}
	cx, cy = cx/float64(len(px)), cy/float64(len(px))
	reach := 0.0
	for _, p := range px {
		reach = max(reach, math.Hypot(float64(p%bw)-cx, float64(p/bw)-cy))
	}
	r := int(math.Ceil(reach))
	nrho := 2*r + 1
	var sin, cos [houghAngles]float64
	for t := range houghAngles {
		sin[t], cos[t] = math.Sincos(math.Pi * float64(t) / houghAngles)
	}
	rho := func(p, t int) float64 { return (float64(p%bw)-cx)*cos[t] + (float64(p/bw)-cy)*sin[t] }

	acc := make([]int32, houghAngles*nrho)
	left := slices.Clone(px)
	var out [][3]float64
	for range trailLines {
		clear(acc)
		for _, p := range left {
			for t := range houghAngles {
				acc[t*nrho+int(math.Round(rho(p, t)))+r]++
			}
		}
		peak := 0
		for i, v := range acc {
			if v > acc[peak] {
				peak = i
			}
		}
		t, at := peak/nrho, float64(peak%nrho-r)
		var band, rest []int
		for _, p := range left {
			if math.Abs(rho(p, t)-at) <= trailOffset {
				band = append(band, p)
			} else {
				rest = append(rest, p)
			}
		}
		l, ok := segment(band, bw, minLen)
		if !ok {
			break
		}
		out, left = append(out, l), rest
		if len(left) == 0 {
			break
		}
	}
	if float64(len(px)-len(left)) < trailExplained*float64(len(px)) {
		return nil
	}

	return out
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
