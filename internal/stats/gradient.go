// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package stats

import (
	"math"
	"slices"
)

// Gradient fits a plane to the sky and returns its range across the frame
// (pixel units, corner to corner) and the direction it rises toward, in
// degrees counter-clockwise from +x. Moonlight, light domes and twilight
// show up this way and leave background extraction to clean up.
//
// The sky is the median of each cell of a 16-wide grid, which stars barely
// move. Cells well above the plane, mostly nebula, are dropped and the plane
// refitted, as PixInsight's ABE rejects samples. When fewer than half the
// cells survive, the frame is mostly nebula and both results are NaN.
//
// ponytail: a plane, not ABE's higher-order surface; a light dome that
// curves across the frame reads as its average slope.
func Gradient(pix []float32, w, h int) (float64, float64) {
	const nx = 16
	ny := max(1, int(math.Round(float64(nx*h)/float64(w))))
	var cells []skyCell
	var buf []float64
	for j := range ny {
		for i := range nx {
			buf = buf[:0]
			// Every second pixel each way: medians of ~1000 samples a cell.
			for y := j * h / ny; y < (j+1)*h/ny; y += 2 {
				for x := i * w / nx; x < (i+1)*w/nx; x += 2 {
					if v := pix[y*w+x]; v == v { // skip NaN (blank) pixels
						buf = append(buf, float64(v))
					}
				}
			}
			if len(buf) < 16 {
				continue
			}
			// Cell centres in frame units, −½..½ each way.
			cells = append(cells, skyCell{(float64(i)+0.5)/nx - 0.5, (float64(j)+0.5)/float64(ny) - 0.5, Select(buf, len(buf)/2)})
		}
	}
	n := len(cells)
	nan := math.NaN()
	if n < 3 {
		return nan, nan
	}

	var a, b float64
	keep := cells
	for range 10 {
		p, ok := plane(keep)
		if !ok {
			return nan, nan
		}
		a, b = p[0], p[1]
		resid := func(c skyCell) float64 { return c.z - (p[0]*c.u + p[1]*c.v + p[2]) }
		res := make([]float64, len(keep))
		for k, c := range keep {
			res[k] = resid(c)
		}
		_, sigma, _ := Robust(res)
		// Nebulae and star halos only add light, so cells high above the
		// plane go at 2.5σ and low ones only at 4σ. Each pass judges every
		// cell again, so sky dropped while the plane still leaned toward a
		// nebula comes back.
		next := slices.DeleteFunc(slices.Clone(cells), func(c skyCell) bool {
			r := resid(c)

			return sigma > 0 && (r > 2.5*sigma || r < -4*sigma)
		})
		if 2*len(next) < n {
			return nan, nan
		}
		if len(next) == len(keep) && slices.Equal(next, keep) {
			break
		}
		keep = next
	}

	// Per pixel the slope is (a/w, b/h); across the frame it spans |a|+|b|.
	return math.Abs(a) + math.Abs(b), math.Mod(math.Atan2(b/float64(h), a/float64(w))*180/math.Pi+360, 360)
}

// skyCell is one grid cell's sky level z at frame position (u, v).
type skyCell struct{ u, v, z float64 }

// plane least-squares fits z = a·u + b·v + c and returns {a, b, c}.
func plane(cells []skyCell) ([3]float64, bool) {
	var m [3][3]float64
	var r [3]float64
	for _, c := range cells {
		x := [3]float64{c.u, c.v, 1}
		for i := range 3 {
			for j := range 3 {
				m[i][j] += x[i] * x[j]
			}
			r[i] += x[i] * c.z
		}
	}
	// Cramer's rule: a 3×3 system needs nothing more.
	det := func(m [3][3]float64) float64 {
		return m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
			m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
			m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
	}
	d := det(m)
	if d == 0 {
		return [3]float64{}, false
	}
	var p [3]float64
	for k := range 3 {
		mk := m
		for i := range 3 {
			mk[i][k] = r[i]
		}
		p[k] = det(mk) / d
	}

	return p, true
}
