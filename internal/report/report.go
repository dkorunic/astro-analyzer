// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

// Package report turns per-frame results into session output: the frame
// table, outlier flags, trend summary, field map, CSV and JSON.
package report

import (
	"cmp"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/dkorunic/astro-analyzer/internal/analyze"
	"github.com/dkorunic/astro-analyzer/internal/drizzle"
	"github.com/dkorunic/astro-analyzer/internal/stats"
)

// Frame is one analysed file with the outlier flags raised against it.
type Frame struct {
	Path   string
	Flags  []string
	Result analyze.Result
	// WFWHM is Siril's weighted FWHM, set by Score: it depends on the
	// whole session, so it is not part of the per-frame Result.
	WFWHM float64
}

// metric is one numeric column. It drives the table, CSV/JSON keys, the
// session summary and outlier flagging.
type metric struct {
	get       func(Frame) float64
	name, key string
	prec      int     // decimals; negative: significant digits, for tiny values
	bad       float64 // +1: high values are bad, -1: low values are bad, 0: informational
}

var metrics = []metric{
	{name: "Exp", key: "exptime", prec: 1, bad: 0, get: func(f Frame) float64 { return f.Result.Exp }},
	{name: "Temp", key: "ccd_temp", prec: 1, bad: 0, get: func(f Frame) float64 { return f.Result.Temp }},
	{name: "Alt", key: "altitude", prec: 1, bad: 0, get: func(f Frame) float64 { return f.Result.Alt }},
	{name: "Moon alt", key: "moon_alt", prec: 0, bad: 0, get: func(f Frame) float64 { return f.Result.MoonAlt }},
	{name: "Sun alt", key: "sun_alt", prec: 0, bad: 0, get: func(f Frame) float64 { return f.Result.SunAlt }},
	{name: "Stars", key: "stars", prec: 0, bad: -1, get: func(f Frame) float64 { return float64(f.Result.Stars) }},
	{name: "FWHM px", key: "fwhm_px", prec: 2, bad: +1, get: func(f Frame) float64 { return f.Result.FWHM }},
	{name: "wFWHM", key: "wfwhm", prec: 2, bad: 0, get: func(f Frame) float64 { return f.WFWHM }},                  // ranks frames; FWHM and Stars flag them
	{name: "FWHM \"", key: "fwhm_arcsec", prec: 2, bad: 0, get: func(f Frame) float64 { return f.Result.FWHMArc }}, // same signal as FWHM px
	{name: "Ecc", key: "eccentricity", prec: 2, bad: +1, get: func(f Frame) float64 { return f.Result.Ecc }},
	{name: "Resid", key: "psf_residual", prec: 4, bad: +1, get: func(f Frame) float64 { return f.Result.Resid }},
	{name: "SNR", key: "snr", prec: 1, bad: -1, get: func(f Frame) float64 { return f.Result.SNR }},
	{name: "PSF SNR", key: "psf_snr", prec: -3, bad: -1, get: func(f Frame) float64 { return f.Result.PSFSNR }},
	{name: "PSFSW", key: "psf_signal_weight", prec: -3, bad: -1, get: func(f Frame) float64 { return f.Result.PSFSW }},
	{name: "SNRW", key: "snr_weight", prec: -3, bad: -1, get: func(f Frame) float64 { return f.Result.SNRW }},
	{name: "Background", key: "background", prec: 1, bad: +1, get: func(f Frame) float64 { return f.Result.Bg }},
	{name: "StdDev", key: "stddev", prec: 2, bad: 0, get: func(f Frame) float64 { return f.Result.Std }},
	{name: "Noise", key: "noise_mrs", prec: 2, bad: 0, get: func(f Frame) float64 { return f.Result.Noise }},
	{name: "Gradient", key: "gradient", prec: 1, bad: +1, get: func(f Frame) float64 { return f.Result.Gradient }},
	{name: "Sky R", key: "sky_r", prec: 1, bad: 0, get: func(f Frame) float64 { return f.Result.Sky[0] }},
	{name: "Sky G", key: "sky_g", prec: 1, bad: 0, get: func(f Frame) float64 { return f.Result.Sky[1] }},
	{name: "Sky B", key: "sky_b", prec: 1, bad: 0, get: func(f Frame) float64 { return f.Result.Sky[2] }},
	{name: "Hot", key: "hot_pixel_level", prec: 1, bad: 0, get: func(f Frame) float64 { return f.Result.Hot }},
}

// byFilter splits frames by filter, in order of first appearance. Filters
// differ in background, FWHM and star count, so each one is its own
// population for session statistics.
func byFilter(frames []Frame) [][]int {
	var keys []string
	var groups [][]int
	if len(frames) == 0 {
		return [][]int{nil} // one empty group, so the reports still print
	}
	for i, f := range frames {
		g := slices.Index(keys, f.Result.Filter)
		if g < 0 {
			g = len(keys)
			keys = append(keys, f.Result.Filter)
			groups = append(groups, nil)
		}
		groups[g] = append(groups[g], i)
	}

	return groups
}

// subset returns the frames at idx, sharing their Flags.
func subset(frames []Frame, idx []int) []Frame {
	out := make([]Frame, len(idx))
	for j, i := range idx {
		out[j] = frames[i]
	}

	return out
}

// filterLabel names a filter group in headings, empty when the session has
// only one filter.
func filterLabel(frames []Frame, groups [][]int) string {
	if len(groups) < 2 {
		return ""
	}

	return " (filter " + cmp.Or(frames[0].Result.Filter, "-") + ")"
}

func series(frames []Frame, m metric) []float64 {
	v := make([]float64, len(frames))
	for i, f := range frames {
		v[i] = m.get(f)
	}

	return v
}

// FlagOutliers records, on each frame, the metrics where it is an outlier
// among the session's frames taken through the same filter.
func FlagOutliers(frames []Frame, k, minDev float64) {
	for _, idx := range byFilter(frames) {
		sub := subset(frames, idx)
		for _, m := range metrics {
			for j, bad := range stats.Outliers(series(sub, m), m.bad, k, minDev) {
				if bad {
					frames[idx[j]].Flags = append(frames[idx[j]].Flags, m.name)
				}
			}
		}
	}
}

// Score sets each frame's wFWHM, Siril's weighted FWHM as its default
// global registration computes it (src/registration/global.c:822):
// FWHM·(1 + 2·(N_ref − N)/N_ref), so a frame that lost stars to cloud or
// haze ranks worse than its FWHM alone says, by at most 3×. (Manual 1–3
// star registration, 3stars.c:274, divides by N instead.) Siril takes N_ref
// from its registration reference; here it is the most stars any frame
// through the same filter got. Siril's FWHM is the mean major axis; this
// uses the geometric-mean FWHM of the other columns.
func Score(frames []Frame) {
	for _, idx := range byFilter(frames) {
		ref := 0
		for _, i := range idx {
			ref = max(ref, frames[i].Result.Stars)
		}
		for _, i := range idx {
			r := frames[i].Result
			frames[i].WFWHM = math.NaN()
			if r.Stars > 0 {
				frames[i].WFWHM = r.FWHM * (1 + 2*float64(ref-r.Stars)/float64(ref))
			}
		}
	}
}

// FlagWorst flags, per filter, the frames outside the best pct percent by
// wFWHM, as Siril's stack -filter-wfwhm=pct% keeps them. Unlike the outlier
// flags it always cuts: it answers "which frames do I want", not "which are
// abnormal". Unknown wFWHM ranks last.
func FlagWorst(frames []Frame, pct float64) {
	if pct >= 100 {
		return
	}
	for _, idx := range byFilter(frames) {
		order := slices.Clone(idx)
		slices.SortStableFunc(order, func(a, b int) int { return byWFWHM(frames[a], frames[b]) })
		keep := int(math.Ceil(pct / 100 * float64(len(order))))
		for _, i := range order[keep:] {
			frames[i].Flags = append(frames[i].Flags, "wFWHM")
		}
	}
}

// byWFWHM orders frames by wFWHM, best first and unknown last; cmp.Compare
// alone would put NaN first.
func byWFWHM(a, b Frame) int {
	key := func(v float64) float64 {
		if math.IsNaN(v) {
			return math.Inf(1)
		}

		return v
	}

	return cmp.Compare(key(a.WFWHM), key(b.WFWHM))
}

// sparkWidth caps sparklines so long sessions still fit a terminal line.
const sparkWidth = 60

// sparkline draws v as Unicode blocks, averaging into at most width buckets.
// NaN-only buckets are blank.
func sparkline(v []float64) string {
	const width = sparkWidth
	n := min(len(v), width)
	b := make([]float64, n)
	for j := range n {
		var sum float64
		var c int
		for _, x := range v[j*len(v)/n : (j+1)*len(v)/n] {
			if !math.IsNaN(x) {
				sum += x
				c++
			}
		}
		b[j] = sum / float64(c) // NaN when c == 0
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, x := range b {
		if !math.IsNaN(x) {
			lo, hi = min(lo, x), max(hi, x)
		}
	}
	ticks := []rune("▁▂▃▄▅▆▇█")
	var sb strings.Builder
	for _, x := range b {
		switch {
		case math.IsNaN(x):
			sb.WriteRune(' ')
		case hi > lo:
			sb.WriteRune(ticks[int((x-lo)/(hi-lo)*7+0.5)])
		default:
			sb.WriteRune(ticks[3])
		}
	}

	return sb.String()
}

func format(v float64, prec int) string {
	if math.IsNaN(v) {
		return "-"
	}
	if prec < 0 {
		return strconv.FormatFloat(v, 'g', -prec, 64)
	}

	return strconv.FormatFloat(v, 'f', prec, 64)
}

// commonDir is the deepest directory containing every path, so the table can
// show short relative names.
func commonDir(frames []Frame) string {
	if len(frames) == 0 {
		return "."
	}
	dir := filepath.Dir(frames[0].Path)
	for _, f := range frames {
		for !strings.HasPrefix(f.Path, dir+string(filepath.Separator)) && dir != filepath.Dir(dir) {
			dir = filepath.Dir(dir)
		}
	}

	return dir
}

// Table prints one row per frame, with paths relative to their common directory.
func Table(w io.Writer, frames []Frame) {
	dir := commonDir(frames)
	if dir != "." {
		fmt.Fprintf(w, "Directory: %s\n\n", dir)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "File\tDate\tFilter")
	for _, m := range metrics {
		fmt.Fprintf(tw, "\t%s", m.name)
	}
	fmt.Fprintln(tw, "\tFlags")
	for _, f := range frames {
		fmt.Fprintf(tw, "%s\t%s\t%s", strings.TrimPrefix(f.Path, dir+string(filepath.Separator)),
			cmp.Or(f.Result.Date, "-"), cmp.Or(f.Result.Filter, "-"))
		for _, m := range metrics {
			fmt.Fprintf(tw, "\t%s", format(m.get(f), m.prec))
		}
		fmt.Fprintf(tw, "\t%s\n", strings.Join(f.Flags, ","))
	}
	tw.Flush()
}

// Summary prints session statistics and trend sparklines per metric, one
// block per filter.
func Summary(w io.Writer, frames []Frame) {
	groups := byFilter(frames)
	for _, idx := range groups {
		sub := subset(frames, idx)
		summary(w, sub, filterLabel(sub, groups))
	}
}

func summary(w io.Writer, frames []Frame, label string) {
	if len(frames) < 2 {
		return
	}
	flagged := 0
	for _, f := range frames {
		if len(f.Flags) > 0 {
			flagged++
		}
	}
	fmt.Fprintf(w, "\nSession%s: %d frames, %d flagged\n", label, len(frames), flagged)
	// The best unflagged frame by wFWHM, as Siril picks its registration
	// reference: sharp and transparent, so the most stars to match.
	var best *Frame
	for i, f := range frames {
		if len(f.Flags) == 0 && !math.IsNaN(f.WFWHM) && (best == nil || f.WFWHM < best.WFWHM) {
			best = &frames[i]
		}
	}
	if best != nil {
		fmt.Fprintf(w, "Reference frame for registration: %s (best wFWHM %.2f px, %d stars)\n",
			filepath.Base(best.Path), best.WFWHM, best.Result.Stars)
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Metric\tMedian\tσ (MAD)\tMin\tMax\tTrend")
	for _, m := range metrics {
		v := series(frames, m)
		med, sigma, n := stats.Robust(v)
		if n == 0 {
			continue
		}
		valid := slices.DeleteFunc(slices.Clone(v), math.IsNaN)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", m.name, format(med, m.prec), format(sigma, m.prec+1),
			format(slices.Min(valid), m.prec), format(slices.Max(valid), m.prec), sparkline(v))
	}
	tw.Flush()
}

// CSV writes one row per frame at full precision; unknown values are empty.
func CSV(w io.Writer, frames []Frame) error {
	cw := csv.NewWriter(w)
	head := make([]string, 0, 3+len(metrics)+1)
	head = append(head, "file", "date", "filter")
	for _, m := range metrics {
		head = append(head, m.key)
	}
	if err := cw.Write(append(head, "flags")); err != nil {
		return err
	}
	for _, f := range frames {
		row := []string{f.Path, f.Result.Date, f.Result.Filter}
		for _, m := range metrics {
			v := m.get(f)
			if math.IsNaN(v) {
				row = append(row, "")
			} else {
				row = append(row, strconv.FormatFloat(v, 'f', -1, 64))
			}
		}
		if err := cw.Write(append(row, strings.Join(f.Flags, ";"))); err != nil {
			return err
		}
	}
	cw.Flush()

	return cw.Error()
}

// JSON writes an array of frames; unknown values are omitted keys.
func JSON(w io.Writer, frames []Frame) error {
	out := make([]map[string]any, len(frames))
	for i, f := range frames {
		o := map[string]any{"file": f.Path, "date": f.Result.Date, "filter": f.Result.Filter, "flags": append([]string{}, f.Flags...)}
		for _, m := range metrics {
			if v := m.get(f); !math.IsNaN(v) { // JSON has no NaN; absent key = unknown
				o[m.key] = v
			}
		}
		out[i] = o
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	return enc.Encode(out)
}

// Field prints the 3x3 field map per filter: median FWHM and eccentricity
// per cell and the dominant elongation axis. Optics leave spatial
// fingerprints: coma elongates the corners radially, tilt makes one side
// wider, tracking or wind elongates every cell the same way. Filters focus
// differently, so each gets its own map.
func Field(w io.Writer, frames []Frame) {
	groups := byFilter(frames)
	for _, idx := range groups {
		sub := subset(frames, idx)
		field(w, sub, filterLabel(sub, groups))
	}
}

func field(w io.Writer, frames []Frame, label string) {
	var fwhm [9]float64
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "\nField%s: median FWHM px, eccentricity and elongation axis (top = last FITS row)\n", label)
	fmt.Fprintln(tw, "\tleft\tcentre\tright")
	for row, name := range []string{"top", "middle", "bottom"} {
		fmt.Fprint(tw, name)
		for col := range 3 {
			i := (2-row)*3 + col // FITS rows run bottom-up
			var f, e []float64
			var ax, ay, n float64
			for _, fr := range frames {
				c := fr.Result.Field[i]
				if !math.IsNaN(c.FWHM) {
					f = append(f, c.FWHM)
				}
				if !math.IsNaN(c.Ecc) { // brighter stars only, so it can be unknown alone
					e = append(e, c.Ecc)
				}
				if !math.IsNaN(c.Axis[0]) {
					ax, ay, n = ax+c.Axis[0], ay+c.Axis[1], n+1
				}
			}
			fwhm[i] = stats.MedianOf(f)
			fmt.Fprintf(tw, "\t%s  %s %s", format(fwhm[i], 2), format(stats.MedianOf(e), 2), AxisGlyph(ax/n, ay/n))
		}
		fmt.Fprintln(tw)
	}
	tw.Flush()

	pct := func(a, b []int) string {
		x, y := make([]float64, 0, len(a)), make([]float64, 0, len(b))
		for _, i := range a {
			x = append(x, fwhm[i])
		}
		for _, i := range b {
			y = append(y, fwhm[i])
		}
		d := (stats.MedianOf(slices.DeleteFunc(x, math.IsNaN))/stats.MedianOf(slices.DeleteFunc(y, math.IsNaN)) - 1) * 100
		if math.IsNaN(d) {
			return "-"
		}

		return fmt.Sprintf("%+.0f%%", d)
	}
	fmt.Fprintf(w, "FWHM %s, curvature %s (corners vs centre), right vs left %s, top vs bottom %s\n",
		tilt(fwhm), pct([]int{0, 2, 6, 8}, []int{4}), pct([]int{2, 5, 8}, []int{0, 3, 6}), pct([]int{6, 7, 8}, []int{0, 1, 2}))
}

// tilt is the spread between the best and worst corner relative to the
// field's median FWHM. Tilt makes one corner soft and the opposite one sharp,
// which the corners' median against the centre averages away. All four
// corners are needed: a missing one could be the worst.
func tilt(fwhm [9]float64) string {
	corners := []float64{fwhm[0], fwhm[2], fwhm[6], fwhm[8]}
	if slices.ContainsFunc(corners, math.IsNaN) {
		return "tilt -"
	}
	lo, hi := slices.Min(corners), slices.Max(corners)
	med := stats.MedianOf(slices.DeleteFunc(slices.Clone(fwhm[:]), math.IsNaN))

	return fmt.Sprintf("tilt %.0f%% (corners %.2f–%.2f px)", (hi-lo)/med*100, lo, hi)
}

// AxisGlyph draws the mean doubled-angle vector (x, y) as a line in the
// direction of elongation, or "·" when stars are round or point every way.
func AxisGlyph(x, y float64) string {
	if math.IsNaN(x) || math.Hypot(x, y) < 0.15 {
		return "·"
	}
	deg := math.Mod(math.Atan2(y, x)/2*180/math.Pi+180, 180)

	return string([]rune("─╱│╲─")[int((deg+22.5)/45)])
}

// Drizzle prints the recommended DrizzleIntegration settings per filter,
// judged on the frames that were not flagged: those are the ones that get
// stacked, and each filter is integrated on its own.
func Drizzle(w io.Writer, frames []Frame) {
	groups := byFilter(frames)
	for _, idx := range groups {
		sub := subset(frames, idx)
		drizzleFor(w, sub, filterLabel(sub, groups))
	}
}

func drizzleFor(w io.Writer, frames []Frame, label string) {
	in := make([]drizzle.Frame, 0, len(frames))
	for _, f := range frames {
		if len(f.Flags) == 0 {
			in = append(in, drizzle.Frame{WCS: f.Result.WCS, FWHM: f.Result.FWHM, CFA: f.Result.CFA})
		}
	}
	r := drizzle.Recommend(in)

	fmt.Fprintf(w, "\nDrizzle%s: %d unflagged frames", label, r.Frames)
	if r.Dither != "" {
		fmt.Fprintf(w, ", dither %s", r.Dither)
	}
	sampling := "undersampled"
	switch {
	case math.IsNaN(r.FWHM):
		sampling = "unknown"
	case r.FWHM >= drizzle.Nyquist:
		sampling = "well sampled"
	}
	fmt.Fprintf(w, "\n  Sampling        median FWHM %s px, %s (drizzle adds resolution below %.1f px)\n",
		format(r.FWHM, 2), sampling, drizzle.Nyquist)
	if r.Reason != "" {
		fmt.Fprintf(w, "  Recommendation  no drizzle: %s\n", r.Reason)

		return
	}
	mode := "Drizzle"
	if r.CFA {
		mode = "CFA drizzle"
	}
	fmt.Fprintf(w, "  Recommendation  %s, Scale %d, Drop shrink %.2f\n", mode, r.Scale, r.DropShrink)
	if r.CFA && r.Scale == 1 {
		fmt.Fprintln(w, "                  recovers full colour resolution without debayer interpolation")
	}
	fmt.Fprintf(w, "  Expected        weights image rms %.0f%% of median (keep under 20%%), noise correlation R %.2f\n",
		r.WeightRMS*100, r.R)
}
