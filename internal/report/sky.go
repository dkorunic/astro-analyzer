// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package report

import (
	"cmp"
	"math"
	"strconv"
	"strings"

	"github.com/dkorunic/astro-analyzer/internal/analyze"
)

// chip is a sensor's light response and dark current, from
// astro-recommender's camera presets (internal/config/cameras.go, October
// 2026: makers' QE charts digitized, Sony's per-watt charts divided by λ).
type chip struct {
	// broad is the colour version's mean R, G and B response over
	// 400–700 nm, relative to its peak QE qe; monoBroad the mono's to
	// monoQE. A QE of 0: no such version ships, or its QE is unpublished.
	broad      [3]float64
	qe, monoQE float64
	monoBroad  float64
	// dark is e⁻/s per pixel at 25 °C, read off the maker's chart (the
	// IMX366's and MN34230's from charts astro-recommender leaves out of
	// its typicalDark); 0: none published.
	dark float64
}

// typicalDark is the median of the charted CMOS chips' dark current at
// 25 °C (astro-recommender's typicalDark), for chips without a chart.
const typicalDark = 0.16

// starvis2 is the IMX662's and IMX664's colour response (one ZWO chart),
// sonyMono the IMX585's mono one, which the IMX678's takes.
var (
	starvis2 = [3]float64{0.425, 0.540, 0.315}
	sonyMono = 0.954
)

// Chips two cameras share.
const (
	imx678 = "IMX678"
	imx178 = "IMX178"
)

var chips = map[string]chip{
	imx678:    {broad: [3]float64{0.397, 0.565, 0.394}, qe: 0.83, monoQE: 0.83, monoBroad: sonyMono},
	imx178:    {broad: [3]float64{0.299, 0.431, 0.319}, qe: 0.70, monoQE: 0.81, monoBroad: 0.833, dark: 0.95},
	"IMX492":  {monoQE: 0.90, monoBroad: 0.859, dark: 0.37},
	"IMX294":  {broad: [3]float64{0.321, 0.445, 0.321}, qe: 0.75, dark: 0.37},
	"IMX571":  {broad: [3]float64{0.292, 0.437, 0.324}, qe: 0.80, monoQE: 0.91, monoBroad: 0.836, dark: 0.065},
	"IMX410":  {broad: [3]float64{0.324, 0.403, 0.315}, qe: 0.80},
	"IMX455":  {broad: [3]float64{0.284, 0.396, 0.300}, qe: 0.80, monoQE: 0.91, monoBroad: 0.836},
	"IMX533":  {broad: [3]float64{0.288, 0.437, 0.328}, qe: 0.80, monoQE: 0.91, monoBroad: 0.836, dark: 0.063},
	"IMX585":  {broad: [3]float64{0.382, 0.461, 0.310}, qe: 0.91, monoQE: 0.91, monoBroad: sonyMono, dark: 0.25},
	"IMX183":  {monoQE: 0.84, monoBroad: 0.844},
	"IMX662":  {broad: starvis2, qe: 0.91, monoQE: 0.91, monoBroad: 0.929},
	"IMX664":  {broad: starvis2, qe: 0.91},
	"IMX676":  {broad: [3]float64{0.373, 0.559, 0.368}, qe: 0.83, monoQE: 0.83, monoBroad: 0.939, dark: 0.060},
	"IMX461":  {monoQE: 0.91, monoBroad: 0.836},
	"IMX432":  {monoQE: 0.79, monoBroad: 0.909},
	"IMX174":  {monoQE: 0.77, monoBroad: 0.823},
	"IMX462":  {broad: [3]float64{0.414, 0.502, 0.391}, qe: 0.81, monoQE: 0.89, monoBroad: 0.887},
	"IMX485":  {broad: [3]float64{0.380, 0.485, 0.342}, qe: 0.85},
	"IMX482":  {broad: [3]float64{0.380, 0.485, 0.342}, qe: 0.85}, // the IMX485's chart
	"IMX224":  {broad: [3]float64{0.377, 0.465, 0.314}, qe: 0.78},
	"IMX464":  {broad: [3]float64{0.359, 0.482, 0.366}, qe: 0.90},
	"IMX715":  {broad: [3]float64{0.376, 0.554, 0.360}, qe: 0.80},
	"IMX366":  {broad: [3]float64{0.320, 0.459, 0.329}, qe: 0.80, dark: 0.126},
	"IMX290":  {broad: [3]float64{0.354, 0.462, 0.306}, qe: 0.81, monoQE: 0.81, monoBroad: 0.887},
	"MN34230": {broad: [3]float64{0.359, 0.410, 0.292}, qe: 0.60, monoQE: 0.60, monoBroad: 0.829, dark: 0.40},
	"SC2210":  {monoQE: 0.92, monoBroad: 0.942},
	"AR0130":  {monoQE: 0.78, monoBroad: 0.895},
}

// darkRate is the chip's dark current at sensor temperature t (°C), e⁻/s
// per pixel: it doubles every 5 °C, as the IMX585's and IMX676's charts do
// between 15 and 30 °C. NaN without a temperature.
//
// ponytail: below 0 °C the charts flatten; there it is negligible anyway.
func darkRate(c chip, t float64) float64 {
	return cmp.Or(c.dark, typicalDark) * math.Exp2((t-25)/5)
}

// response is the chip's peak QE times its mean relative response in the
// pixels pixNoise samples: every second column of the first stored row,
// so both colours of that row of a CFA. NaN when the version is unknown.
func response(c chip, cfa bool, bayer string, flip bool) float64 {
	if !cfa {
		return nan(c.monoQE * c.monoBroad)
	}
	if len(bayer) != 4 {
		return nan(c.qe * (c.broad[0] + 2*c.broad[1] + c.broad[2]) / 4)
	}
	if flip { // the pattern starts at the top row: the first stored row is its second
		bayer = bayer[2:]
	}
	q := 0.0
	for _, ch := range []byte(bayer[:2]) {
		i := strings.IndexByte("RGB", ch)
		if i < 0 {
			return math.NaN()
		}
		q += c.broad[i] / 2
	}

	return nan(c.qe * q)
}

// nan is v, or NaN for 0 (unknown).
func nan(v float64) float64 {
	if v == 0 {
		return math.NaN()
	}

	return v
}

// Sky brightness constants, astro-recommender's (MODELS.md, Sub-exposure
// estimate): a V = 0 source gives 1005 photons s⁻¹ cm⁻² Å⁻¹ at 550 nm
// (Bessell 1998) over the V band's 88 nm, and the sky is taken as flat per
// nm over a camera's 400–700 nm. Throughput is the optics' and IR-cut
// window's: 0.81 for a mirror at prime focus, 0.85 for an unknown telescope.
const (
	photonsV0  = 1005 * 10 * 88 * 300.0 / 88 // photons s⁻¹ cm⁻² over 400–700 nm
	throughput = 0.85
	extinction = 0.2 // V mag per airmass, for zenith
)

// bortleFloor is each Bortle class's least dark zenith, V mag/arcsec²,
// classes 1–8: DarkSkySites' map bands, as astro-recommender takes them.
var bortleFloor = [...]float64{21.96, 21.7, 21.3, 20.8, 20.3, 19.6, 18.9, 18.0}

// SkyBrightness sets each frame's SkyMag. Read noise rn (e⁻) overrides
// the camera preset's when positive.
func SkyBrightness(frames []Frame, rn float64) {
	for i := range frames {
		frames[i].SkyMag = skyMag(frames[i].Result, rn)
	}
}

// skyMag is the sky brightness in V mag/arcsec² that the frame's photon
// noise implies: its per-pixel noise less read noise and dark current is
// the sky's shot noise, which the camera preset's QE and the optics turn
// into photons per arcsec². Target light counts as sky, so a field full of
// nebula reads bright. NaN without a camera preset, gain, read noise,
// aperture or plate scale.
//
// ponytail: flat spectrum and fixed throughput, about ±0.3 mag; good for
// comparing nights with one camera, not for an absolute SQM.
func skyMag(r analyze.Result, rn float64) float64 {
	p, ok := preset(r.Meta)
	if !ok {
		return math.NaN()
	}
	if !(rn > 0) {
		rn, _ = presetReadNoise(r.Meta)
	}
	c := chips[p.chip]
	dark := darkRate(c, r.Temp)
	if math.IsNaN(dark) {
		dark = 0 // ponytail: no CCD-TEMP; dark current reads as sky
	}
	rate := (r.NoiseE*r.NoiseE-rn*rn)/r.Exp - dark
	d, o := r.Aperture/10, p.obstruction/10 // cm
	photons := photonsV0 * math.Pi / 4 * (d*d - o*o) * cmp.Or(p.throughput, throughput) *
		response(c, r.CFA, r.Bayer, r.BayerFlip) * r.Plate * r.Plate
	if !(rate > 0 && photons > 0) {
		return math.NaN()
	}

	return -2.5 * math.Log10(rate/photons)
}

// zenith moves sky brightness mag, measured at altitude alt (degrees), to
// the zenith: a dark sky's glow grows with airmass X as X·10^(−0.4k(X−1))
// (Krisciunas & Schaefer 1991). Light domes do not follow it. Without an
// altitude mag is taken as the zenith's.
func zenith(mag, alt float64) float64 {
	if !(alt > 0) {
		return mag
	}
	x := 1 / math.Sin(alt*math.Pi/180)

	return mag + 2.5*math.Log10(x*math.Pow(10, -0.4*extinction*(x-1)))
}

// bortle is the Bortle class of a zenith sky brightness, "-" for NaN.
func bortle(mag float64) string {
	if math.IsNaN(mag) {
		return "-"
	}
	for i, lower := range bortleFloor {
		if mag >= lower {
			return strconv.Itoa(i + 1)
		}
	}

	return "9"
}
