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
// frame size, gain or ISO, dark exposure and temperature, flat filter. It
// reads no calibration pixels. Files with one DATE-OBS are copies of one
// capture: Celestron Origin saves each bias and dark master several times.
func Calibration(w io.Writer, frames []Frame, calib []fits.Meta) {
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

	byType := map[string][]fits.Meta{}
	for _, m := range calib {
		byType[m.Type] = append(byType[m.Type], m)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  Type\tFiles\tTaken\tExp s\tGain/ISO\tTemp °C\tFilter\tIssues")
	for _, t := range slices.Sorted(maps.Keys(byType)) {
		c := summarise(byType[t])
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
			format(c.temp, 1), cmp.Or(strings.Join(c.filters, "/"), "-"), cmp.Or(strings.Join(issues(t, c, light), "; "), "-"))
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
