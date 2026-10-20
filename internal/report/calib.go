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

// Calib is a calibration frame: its path, what fits.Load read of it
// without the pixels, and for darks the lights' hot-pixel level (Hot) found
// in it by analyze.Frame; 0 otherwise.
type Calib struct {
	Path  string
	Image fits.Image
	Hot   float64
}

// gainTolerance is how far photon-transfer gain may differ from the gain in
// use before it is reported: originstack's verify_gain allows 15%. When the
// pedestal is the dark's, the measured gain is only a lower bound, and the
// gain in use may sit up to darkGainSpan above it. ponytail: a guess at
// the most dark current can hide; Mark II summer sessions at 32 °C read
// 1.5× low, the raw Origin178 EGAIN was 4.6× off.
const (
	gainTolerance = 0.15
	darkGainSpan  = 2
)

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
// frame size, gain or ISO, dark exposure and temperature, flat filter.
// Files with one DATE-OBS are copies of one capture: Celestron Origin saves
// each bias and dark master several times. Then what their pixels show:
// see findings. rn is the read noise in e⁻ (NaN if unknown) and pairGain
// fits.PairGain.
func Calibration(w io.Writer, frames []Frame, calib []Calib, rn float64, pairGain func(a, b string, pedestal float64) (float64, error)) {
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
	flat := summarise(metas(byType[fits.Flat]))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  Type\tFiles\tTaken\tExp s\tGain/ISO\tTemp °C\tFilter\tIssues")
	for _, t := range slices.Sorted(maps.Keys(byType)) {
		c := summarise(metas(byType[t]))
		notes := issues(t, c, light, flat)
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
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range findings(frames, byType, rn, pairGain) {
		fmt.Fprintf(tw, "  %s\t%s\n", f[0], f[1])
	}
	tw.Flush()
}

// findings are what the calibration frames' pixels show, as (topic, text)
// lines: bias banding, the dark's pedestal and hot pixels, flat exposure
// and dust, photon-transfer gain and the noise the masters add.
func findings(frames []Frame, byType map[string][]Calib, rn float64, pairGain func(a, b string, pedestal float64) (float64, error)) [][2]string {
	var out [][2]string
	add := func(topic, text string) {
		if text != "" {
			out = append(out, [2]string{topic, text})
		}
	}
	setting := frames[0].Result.Setting
	atSetting := func(cs []Calib) []Calib {
		return slices.DeleteFunc(slices.Clone(cs), func(c Calib) bool { return !same(c.Image.Meta.Setting, setting) })
	}
	biases, darks := atSetting(raws(byType[fits.Bias])), atSetting(raws(byType[fits.Dark]))
	if len(biases) > 0 {
		add("Bias", biasBanding(biases))
	}
	if len(darks) > 0 {
		notes := slices.DeleteFunc([]string{pedestal(darks, biases, lightGain(frames)), darkHot(frames, darks)},
			func(s string) bool { return s == "" })
		add("Dark", strings.Join(notes, "; "))
	}
	for _, f := range raws(byType[fits.Flat]) {
		add("Flat", flatNote(f))

		break // ponytail: the first flat; sessions hold one master per filter
	}
	add("Gain", gainCheck(frames, biases, darks, pairGain))
	add("Masters", masterNoise(frames, darks, biases, rn))

	return out
}

// issues lists how calibration frames of type t fail to match the lights.
// Gain or ISO matters for bias and darks (offset and dark current change
// with it), exposure and temperature for darks, the filter for flats (its
// dust and vignetting are what they correct), size for all. Dark flats
// calibrate the flats, so they are held to the flats' gain, exposure and
// temperature instead (ASTAP's FD_ issues, unit_stack.pas:12726).
func issues(t string, c, light, flat headers) []string {
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
	ref, whose, than := light, "", ""
	if t == fits.DarkFlat {
		ref, whose, than = flat, "the flats' ", " than the flats"
	}
	if t == fits.Bias || t == fits.Dark || t == fits.DarkFlat {
		missing(c.settings, ref.settings, "none at "+whose+"gain/ISO %g")
	}
	switch t {
	case fits.Dark:
		missing(c.exps, light.exps, "none at %g s, so scaled by exposure")
	case fits.DarkFlat:
		missing(c.exps, flat.exps, "none at "+whose+"%.3g s")
	}
	if t == fits.Dark || t == fits.DarkFlat {
		if d := c.temp - ref.temp; math.Abs(d) >= warmerDark { // false for NaN
			out = append(out, fmt.Sprintf("%.1f °C %s%s", math.Abs(d), map[bool]string{true: "warmer", false: "colder"}[d > 0], than))
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
		if d.Image.Cal.Level != 0 && same(d.Image.Meta.Setting, setting) {
			dl, de = append(dl, d.Image.Cal.Level), append(de, d.Image.Meta.Exp)
		}
	}
	for _, b := range biases {
		if b.Image.Cal.Level != 0 && same(b.Image.Meta.Setting, setting) {
			bl, bn = append(bl, b.Image.Cal.Level), append(bn, b.Image.PixNoise)
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

// biasBanding describes the bias's row and column pattern against its
// pixel noise. In a master it is fixed, so subtracting the master removes
// it; it only matters where the lights are calibrated without a bias.
func biasBanding(biases []Calib) string {
	b := biases[0].Image
	if !(b.PixNoise > 0) {
		return ""
	}

	return fmt.Sprintf("banding %.1f ADU between rows, %.1f between columns (%.0f%% of the %.0f ADU pixel noise)",
		b.Cal.Rows, b.Cal.Cols, 100*max(b.Cal.Rows, b.Cal.Cols)/b.PixNoise, b.PixNoise)
}

// darkHot compares the darks' hot pixels with the lights', both measured
// by analyze.Frame (Result.Hot), and scaled to the lights' exposure when
// that differs. Above 1 the dark subtracts more than the lights hold and
// leaves dark pits; below 1 hot pixels remain. Hot pixels follow sensor
// temperature, so a warmer dark reads higher.
func darkHot(frames []Frame, darks []Calib) string {
	var dh, de, lh, le []float64
	for _, d := range darks {
		if d.Hot > 0 {
			dh, de = append(dh, d.Hot), append(de, d.Image.Meta.Exp)
		}
	}
	for _, f := range frames {
		if f.Result.Hot > 0 {
			lh, le = append(lh, f.Result.Hot), append(le, f.Result.Exp)
		}
	}
	if len(dh) == 0 || len(lh) == 0 {
		return ""
	}
	r := stats.MedianOf(dh) / stats.MedianOf(lh)
	s := fmt.Sprintf("hot pixels %.2f× the lights'", r)
	if td, tl := stats.MedianOf(de), stats.MedianOf(le); td > 0 && tl > 0 && td != tl {
		s += fmt.Sprintf(", %.2f× scaled to %g s", r*tl/td, tl)
	}

	return s
}

// flatNote reports a flat's level as a share of the sample range, clipped
// pixels and dust shadows. Float flats (normalised) have no range to share.
func flatNote(f Calib) string {
	c := f.Image.Cal
	var parts []string
	if f.Image.Range > 1 && c.Level > 0 {
		parts = append(parts, fmt.Sprintf("level %.0f%% of range", 100*c.Level/f.Image.Range))
	}
	if c.Clipped > 1e-4 { // a stray full-scale pixel is no overexposure
		parts = append(parts, fmt.Sprintf("%.2g%% of pixels clipped", 100*c.Clipped))
	}
	switch {
	case c.Dust > 0:
		parts = append(parts, fmt.Sprintf("%d dust shadows, deepest %.1f%%", c.Dust, 100*c.DustDepth))
	case c.Level > 0:
		parts = append(parts, "no dust shadows")
	}

	return strings.Join(parts, ", ")
}

// gainCheck measures the gain by photon transfer on up to three pairs of
// neighbouring lights (same filter and exposure) at the quartiles of the
// session and compares it with the gain in use (EGAIN or -gain). The
// pedestal is the bias level, or the darks' when they sit below it: lights
// are taken as darks are, and on Celestron Origin the bias pedestal is
// 120–410 ADU higher (see pedestal). With the bias, Mark II sessions read
// 0.0055–0.0094 e⁻/ADU, lower the less sky above the pedestal; with the
// dark, 0.0076–0.0101. The dark level holds the dark current, whose shot
// noise then counts against no signal, so that gain is a lower bound:
// 0.0076 on the two 32 °C sessions, 0.0091–0.0101 on cooler ones.
func gainCheck(frames []Frame, biases, darks []Calib, pairGain func(a, b string, pedestal float64) (float64, error)) string {
	level := func(cs []Calib) float64 {
		var v []float64
		for _, c := range cs {
			if c.Image.Cal.Level > 0 {
				v = append(v, c.Image.Cal.Level)
			}
		}

		return stats.MedianOf(v)
	}
	pedestal, bound := level(biases), false
	if d := level(darks); d < pedestal { // false for NaN
		pedestal, bound = d, true
	}
	if math.IsNaN(pedestal) || len(frames) < 2 {
		return ""
	}
	var gains []float64
	for _, q := range []int{1, 2, 3} {
		i := min(len(frames)*q/4, len(frames)-2)
		a, b := frames[i].Result, frames[i+1].Result
		if a.Filter != b.Filter || a.Exp != b.Exp {
			continue
		}
		if g, err := pairGain(frames[i].Path, frames[i+1].Path, pedestal); err == nil && g > 0 {
			gains = append(gains, g)
		}
	}
	if len(gains) == 0 {
		return ""
	}
	g := stats.MedianOf(gains)
	s := fmt.Sprintf("%.3g e⁻/ADU by photon transfer on %d pairs of neighbouring lights", g, len(gains))
	hi := 1 + gainTolerance
	if bound {
		s = "≥ " + s + " (pedestal from the dark: its dark current counts as noise)"
		hi = darkGainSpan
	}
	switch used := lightGain(frames); {
	case !(used > 0):
		return s + fmt.Sprintf("; no gain in the header: run with -gain %.3g", g)
	case used/g < 1-gainTolerance || used/g > hi:
		return s + fmt.Sprintf("; the gain in use, %.3g, is %.2g× that: run with -gain %.3g", used, used/g, g)
	case bound:
		return s + fmt.Sprintf("; the gain in use, %.3g, is consistent", used)
	default:
		return s + fmt.Sprintf("; the gain in use, %.3g, agrees", used)
	}
}

// masterNoise is the noise calibration adds: a master of N frames carries
// read noise/√N of its own, the same in every sub, so stacking more subs
// cannot average it away; only dithering (or an alt-az mount's field
// rotation) moves it to different sky. With plate solutions the dithers
// are counted: over k sensor positions the master's pattern lands on k
// places of the registered stack and averages to 1/√k. k is an upper
// bound (positions), so that stays a lower bound. The darks carry the
// bias, so they are the master when present. A lower bound: the dark's own
// shot noise is left out, as Origin darks sit below their bias and do not
// show it.
//
// ponytail: field rotation alone is not counted, and flagged frames'
// positions are, so k comes from all frames, capped at the stacked subs.
func masterNoise(frames []Frame, darks, biases []Calib, rn float64) string {
	master, kind := darks, "dark"
	if len(master) == 0 {
		master, kind = biases, "bias"
	}
	if len(master) == 0 || !(rn > 0) {
		return ""
	}
	h := summarise(metas(master))
	n := float64(max(len(h.dates), 1))
	if len(h.stacks) > 0 {
		n *= slices.Min(h.stacks)
	}
	var noise []float64
	subs := 0
	for _, f := range frames {
		if len(f.Flags) == 0 {
			subs++
			if f.Result.NoiseE > 0 {
				noise = append(noise, f.Result.NoiseE)
			}
		}
	}
	sub := stats.MedianOf(noise)
	if subs == 0 || !(sub > 0) {
		return ""
	}
	cal, stack := rn/math.Sqrt(n), sub/math.Sqrt(float64(subs))
	head := fmt.Sprintf("the %g-frame %s adds ≥ %.2g e⁻ per pixel, the same in every sub, to the %d-sub stack's %.2g e⁻: ",
		n, kind, cal, subs, stack)
	p := newPointing(frames)
	d := measureDithers(p)
	if d.steps == 0 {
		return head + fmt.Sprintf("undithered, stack noise ≥ +%.0f%%", 100*(math.Hypot(1, cal/stack)-1))
	}
	if len(d.size) == 0 {
		return head + fmt.Sprintf("not dithered, so stack noise ≥ +%.0f%%", 100*(math.Hypot(1, cal/stack)-1))
	}
	k := min(d.positions(len(p.runs)), subs)
	mixed := cal / math.Sqrt(float64(k))

	return head + fmt.Sprintf("dithered over ≤ %d positions, it averages to ≥ %.2g e⁻, stack noise ≥ +%.1f%%",
		k, mixed, 100*(math.Hypot(1, mixed/stack)-1))
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
	biases := raws(slices.DeleteFunc(slices.Clone(calib), func(c Calib) bool {
		m := c.Image.Meta

		return m.Type != fits.Bias || !same(m.Setting, setting) || !(c.Image.PixNoise > 0)
	}))
	var singles []Calib
	bound, n := math.Inf(1), 0.0
	for _, c := range biases {
		m := c.Image.Meta
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

// raws drops PixInsight masters (IMAGETYP Master...) when cs holds other
// frames: WBPP builds its masters from the session's own files, so they
// count those twice, and their NCOMBINE overcounts when the files were
// copies (Origin saves one master several times).
func raws(cs []Calib) []Calib {
	if !slices.ContainsFunc(cs, func(c Calib) bool { return !c.Image.Meta.Master }) {
		return cs
	}

	return slices.DeleteFunc(slices.Clone(cs), func(c Calib) bool { return c.Image.Meta.Master })
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
