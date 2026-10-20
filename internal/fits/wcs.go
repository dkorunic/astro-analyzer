// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package fits

import (
	"math"
	"strings"

	"github.com/dkorunic/astro-analyzer/third_party/fitsio"
)

// WCS is a gnomonic (TAN) world coordinate system from a plate-solved
// header: enough to map sky positions between frames. Pixel coordinates are
// 1-based on the full sensor, as in the header.
type WCS struct {
	CD    [2][2]float64 // degrees per pixel
	CRVAL [2]float64    // reference RA, Dec in degrees
	CRPIX [2]float64    // reference pixel
	NAXIS [2]int        // sensor size the solution applies to
	OK    bool          // false when the header has no usable solution
}

// readWCS accepts a CD matrix, CDELT with a PC matrix, or the older
// CDELT/CROTA2 form.
func readWCS(h *fitsio.Header) WCS {
	if t := str(h, "CTYPE1"); t != "" && !strings.Contains(t, "TAN") {
		return WCS{}
	}
	ra, ok1 := num(h, "CRVAL1")
	dec, ok2 := num(h, "CRVAL2")
	x, ok3 := num(h, "CRPIX1")
	y, ok4 := num(h, "CRPIX2")
	// Without a reference point the CD matrix still gives the plate scale.
	w := WCS{CRVAL: [2]float64{ra, dec}, CRPIX: [2]float64{x, y}, OK: ok1 && ok2 && ok3 && ok4}
	if axes := h.Axes(); len(axes) >= 2 {
		w.NAXIS = [2]int{axes[0], axes[1]}
	}
	if cd11, ok := num(h, "CD1_1"); ok {
		cd12, _ := num(h, "CD1_2")
		cd21, _ := num(h, "CD2_1")
		cd22, _ := num(h, "CD2_2")
		w.CD = [2][2]float64{{cd11, cd12}, {cd21, cd22}}
	} else {
		w.CD = cdelt(h) // zero, so rejected below, without CDELT
	}
	if w.CD[0][0]*w.CD[1][1]-w.CD[0][1]*w.CD[1][0] == 0 {
		return WCS{}
	}
	// Celestron Origin writes ASTRSOLV = F when the WCS is only the mount's
	// pointing, often off by arcminutes: keep its scale, not its position.
	if c := h.Get("ASTRSOLV"); c != nil && c.Value == false {
		w.OK = false
	}

	return w
}

// cdelt builds the CD matrix from CDELT and a PC matrix (Calabretta &
// Greisen 2002: CDi_j = CDELTi·PCi_j, PC defaulting to the identity) or,
// without PC cards, the older CROTA2 rotation. It is zero without CDELT.
func cdelt(h *fitsio.Header) [2][2]float64 {
	c1, ok1 := num(h, "CDELT1")
	c2, ok2 := num(h, "CDELT2")
	if !ok1 || !ok2 {
		return [2][2]float64{}
	}
	pc, hasPC := [2][2]float64{{1, 0}, {0, 1}}, false
	for i, row := range [2][2]string{{"PC1_1", "PC1_2"}, {"PC2_1", "PC2_2"}} {
		for j, key := range row {
			if v, ok := num(h, key); ok {
				pc[i][j], hasPC = v, true
			}
		}
	}
	if !hasPC {
		rot, _ := num(h, "CROTA2")
		s, c := math.Sincos(rot * math.Pi / 180)

		return [2][2]float64{{c1 * c, -c2 * s}, {c1 * s, c2 * c}}
	}

	return [2][2]float64{{c1 * pc[0][0], c1 * pc[0][1]}, {c2 * pc[1][0], c2 * pc[1][1]}}
}

// Pixel returns the pixel position of sky point (ra, dec) in degrees.
func (w WCS) Pixel(ra, dec float64) (float64, float64) {
	a0, d0 := w.CRVAL[0]*math.Pi/180, w.CRVAL[1]*math.Pi/180
	a, d := ra*math.Pi/180, dec*math.Pi/180
	sd0, cd0 := math.Sincos(d0)
	sd, cdd := math.Sincos(d)
	sa, ca := math.Sincos(a - a0)
	cosc := sd0*sd + cd0*cdd*ca
	xi := cdd * sa / cosc * 180 / math.Pi
	eta := (cd0*sd - sd0*cdd*ca) / cosc * 180 / math.Pi
	m := w.CD
	det := m[0][0]*m[1][1] - m[0][1]*m[1][0]

	return w.CRPIX[0] + (m[1][1]*xi-m[0][1]*eta)/det, w.CRPIX[1] + (-m[1][0]*xi+m[0][0]*eta)/det
}

// World returns the sky position in degrees of pixel (x, y).
func (w WCS) World(x, y float64) (float64, float64) {
	dx, dy := x-w.CRPIX[0], y-w.CRPIX[1]
	xi := (w.CD[0][0]*dx + w.CD[0][1]*dy) * math.Pi / 180
	eta := (w.CD[1][0]*dx + w.CD[1][1]*dy) * math.Pi / 180
	rho := math.Hypot(xi, eta)
	if rho == 0 {
		return w.CRVAL[0], w.CRVAL[1]
	}
	a0, d0 := w.CRVAL[0]*math.Pi/180, w.CRVAL[1]*math.Pi/180
	sd0, cd0 := math.Sincos(d0)
	sc, cc := math.Sincos(math.Atan(rho))
	dec := math.Asin(cc*sd0 + eta*sc*cd0/rho)
	ra := a0 + math.Atan2(xi*sc, rho*cd0*cc-eta*sd0*sc)

	return math.Mod(ra*180/math.Pi+360, 360), dec * 180 / math.Pi
}

// Rotation is the sky position angle of the pixel grid in degrees.
func (w WCS) Rotation() float64 {
	return math.Atan2(w.CD[1][0], w.CD[0][0]) * 180 / math.Pi
}
