// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package report

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/stats"
)

// warmerDark is how far a dark's sensor temperature may sit from the
// lights' before it is reported, °C. ponytail: a round number; dark current
// roughly doubles every 5–7 °C on CMOS sensors, so this is about 2×.
const warmerDark = 5

// Calib is a calibration frame: its path and what fits.Load read of it
// (header, and for bias and darks PixNoise and Level), without pixels.
type Calib struct {
	Path  string
	Image fits.Image
}

// headers summarises one set of frames: distinct exposures, gain or ISO
// settings, filters and DATE-OBS values, the median sensor temperature and
// frame sizes.
type headers struct {
	exps, settings, stacks []float64
	filters, dates         []string
	sizes                  [][2]int
	temp                   float64
}

func summarise(ms []fits.Meta) headers {
	var h headers
	temps := make([]float64, 0, len(ms))
	for _, m := range ms {
		h.exps, h.settings, h.stacks = appendKnown(h.exps, m.Exp), appendKnown(h.settings, m.Setting), appendKnown(h.stacks, m.Stack)
		temps = append(temps, m.Temp)
		for _, add := range []struct {
			to *[]string
			v  string
		}{{to: &h.filters, v: m.Filter}, {to: &h.dates, v: m.Date}} {
			if add.v != "" && !slices.Contains(*add.to, add.v) {
				*add.to = append(*add.to, add.v)
			}
		}
		if !slices.Contains(h.sizes, m.Size) {
			h.sizes = append(h.sizes, m.Size)
		}
	}
	slices.Sort(h.filters)
	slices.Sort(h.dates)
	h.temp = stats.MedianOf(slices.DeleteFunc(temps, math.IsNaN))

	return h
}

// Calibration compares the calibration frames' headers with the lights':
// frame size, gain or ISO, dark exposure and temperature, flat filter; and
// the darks' level with the bias's (see pedestal). Files with one DATE-OBS
// are copies of one capture: Celestron Origin saves each bias and dark
// master several times.
func Calibration(w io.Writer, frames []Frame, calib []Calib) {
	if len(frames) == 0 || len(calib) == 0 {
		return
	}
	ms := make([]fits.Meta, len(frames))
	for i, f := range frames {
		ms[i] = f.Result.Meta
	}
	light := summarise(ms)
	fmt.Fprintf(w, "\nCalibration frames against the lights (%s s, gain/ISO %s, %s °C", values(light.exps), values(light.settings), format(light.temp, 1))
	if len(light.filters) > 0 {
		fmt.Fprintf(w, ", filter %s", strings.Join(light.filters, "/"))
	}
	fmt.Fprintln(w, ")")

	byType := map[string][]Calib{}
	for _, c := range calib {
		byType[c.Image.Meta.Type] = append(byType[c.Image.Meta.Type], c)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  Type\tFiles\tTaken\tExp s\tGain/ISO\tTemp °C\tFilter\tNotes")
	for _, t := range slices.Sorted(maps.Keys(byType)) {
		c := summarise(metas(byType[t]))
		notes := issues(t, c, light)
		if t == fits.Dark {
			gain := math.NaN()
			if slices.Equal(c.settings, light.settings) {
				gain = lightGain(frames)
			}
			if n := pedestal(byType[fits.Dark], byType[fits.Bias], gain); n != "" {
				notes = append(notes, n)
			}
		}
		files := strconv.Itoa(len(byType[t]))
		if len(c.dates) < len(byType[t]) {
			files += fmt.Sprintf(", %d distinct", len(c.dates))
		}
		if len(c.stacks) > 0 {
			files += ", " + values(c.stacks) + "-frame master"
		}
		taken := "-"
		if len(c.dates) > 0 {
			taken = day(c.dates[0])
			if last := day(c.dates[len(c.dates)-1]); last != taken {
				taken += "–" + last
			}
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t, files, taken, values(c.exps), values(c.settings),
			format(c.temp, 1), cmp.Or(strings.Join(c.filters, "/"), "-"), cmp.Or(strings.Join(notes, "; "), "-"))
	}
	tw.Flush()
	var missing []string
	for _, t := range []string{fits.Bias, fits.Dark, fits.Flat} {
		if byType[t] == nil {
			missing = append(missing, strings.ToLower(t))
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(w, "  No %s frames\n", strings.Join(missing, " or "))
	}
}

// issues lists how calibration frames of type t fail to match the lights.
// Gain or ISO matters for bias and darks (offset and dark current change
// with it), exposure and temperature for darks, the filter for flats (its
// dust and vignetting are what they correct), size for all.
func issues(t string, c, light headers) []string {
	var out []string
	for _, s := range c.sizes {
		if !slices.Contains(light.sizes, s) {
			out = append(out, fmt.Sprintf("size %d × %d", s[0], s[1]))
		}
	}
	missing := func(have, want []float64, msg string) {
		for _, v := range want {
			if len(have) > 0 && !slices.Contains(have, v) {
				out = append(out, fmt.Sprintf(msg, v))
			}
		}
	}
	if t == fits.Bias || t == fits.Dark {
		missing(c.settings, light.settings, "none at gain/ISO %g")
	}
	if t == fits.Dark {
		missing(c.exps, light.exps, "none at %g s, so scaled by exposure")
		if d := c.temp - light.temp; math.Abs(d) >= warmerDark { // false for NaN
			out = append(out, fmt.Sprintf("%.1f °C %s", math.Abs(d), map[bool]string{true: "warmer", false: "colder"}[d > 0]))
		}
	}
	if t == fits.Flat && len(c.filters) > 0 {
		for _, f := range light.filters {
			if !slices.Contains(c.filters, f) {
				out = append(out, "none for filter "+f)
			}
		}
	}

	return out
}

// pedestal compares the darks' median level with that of the bias frames
// at the same gain or ISO. Dark current only adds, so a dark below its bias
// was recorded with another offset, and dark − bias, which scaling or
// optimising a dark needs, is negative (Celestron Origin: 120–410 ADU below,
// as originstack also found). Above it, the difference over the exposure is
// a typical pixel's dark current, given the gain in e⁻/ADU.
func pedestal(darks, biases []Calib, gain float64) string {
	var dl, de, bl, bn []float64
	setting := darks[0].Image.Meta.Setting
	for _, d := range darks {
		if d.Image.Level != 0 && same(d.Image.Meta.Setting, setting) {
			dl, de = append(dl, d.Image.Level), append(de, d.Image.Meta.Exp)
		}
	}
	for _, b := range biases {
		if b.Image.Level != 0 && same(b.Image.Meta.Setting, setting) {
			bl, bn = append(bl, b.Image.Level), append(bn, b.Image.PixNoise)
		}
	}
	if len(dl) == 0 || len(bl) == 0 {
		return ""
	}
	d := stats.MedianOf(dl) - stats.MedianOf(bl)
	switch t := stats.MedianOf(de); {
	case d < -stats.MedianOf(bn):
		return fmt.Sprintf("%.0f ADU below the bias, so dark − bias (to scale or optimise it) is negative", -d)
	case d > 0 && gain > 0 && t > 0:
		return fmt.Sprintf("dark current %.2g e⁻/s per pixel", d*gain/t)
	}

	return ""
}

// BiasReadNoise is the read noise in e⁻ from the session's bias frames at
// the lights' gain or ISO, converted with the lights' gain, and where it
// came from; NaN without such frames or a gain. Two separate frames give it
// exactly (fits.PairNoise, called as pair). A master of N frames bounds it
// at √N times its pixel noise, which still holds the fixed pattern: the
// Celestron Origin presets came this way.
func BiasReadNoise(frames []Frame, calib []Calib, pair func(a, b string) (float64, error)) (float64, string) {
	gain := lightGain(frames)
	if len(frames) == 0 || !(gain > 0) {
		return math.NaN(), ""
	}
	setting := frames[0].Result.Setting
	var singles []Calib
	bound, n := math.Inf(1), 0.0
	for _, c := range calib {
		m := c.Image.Meta
		if m.Type != fits.Bias || !same(m.Setting, setting) || !(c.Image.PixNoise > 0) {
			continue
		}
		k := 1.0
		if m.Stack > 1 {
			k = m.Stack
		} else if m.Date != "" && !slices.ContainsFunc(singles, func(s Calib) bool { return s.Image.Meta.Date == m.Date }) {
			singles = append(singles, c)
		}
		if b := math.Sqrt(k) * c.Image.PixNoise; b < bound {
			bound, n = b, k
		}
	}
	if len(singles) >= 2 {
		if rn, err := pair(singles[0].Path, singles[1].Path); err == nil && rn > 0 {
			return rn * gain, "from the difference of two bias frames"
		}
	}
	if math.IsInf(bound, 1) {
		return math.NaN(), ""
	}
	if n > 1 {
		return bound * gain, fmt.Sprintf("≤, from this session's %g-frame bias master", n)
	}

	return bound * gain, "≤, from this session's bias frame"
}

// lightGain is the lights' median gain in e⁻/ADU, NaN when unknown.
func lightGain(frames []Frame) float64 {
	var g []float64
	for _, f := range frames {
		if f.Result.Gain > 0 {
			g = append(g, f.Result.Gain)
		}
	}

	return stats.MedianOf(g)
}

// same compares gain settings; two unknown ones match.
func same(a, b float64) bool { return a == b || math.IsNaN(a) && math.IsNaN(b) }

func metas(cs []Calib) []fits.Meta {
	ms := make([]fits.Meta, len(cs))
	for i, c := range cs {
		ms[i] = c.Image.Meta
	}

	return ms
}

// appendKnown appends v unless it is NaN or already present.
func appendKnown(s []float64, v float64) []float64 {
	if math.IsNaN(v) || slices.Contains(s, v) {
		return s
	}

	return append(s, v)
}

// values lists distinct values in order to 3 significant digits, without
// exponents (ISO 1000, not 1e+03); "-" when there are none.
func values(v []float64) string {
	if len(v) == 0 {
		return "-"
	}
	parts := make([]string, len(v))
	for i, x := range slices.Sorted(slices.Values(v)) {
		r, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'g', 3, 64), 64)
		parts[i] = strconv.FormatFloat(r, 'f', -1, 64)
	}

	return strings.Join(parts, "/")
}

// day is the date part of a DATE-OBS value.
func day(d string) string {
	date, _, _ := strings.Cut(d, "T")

	return date
}
