// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package report

import (
	"cmp"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/stats"
)

// twilight is the Sun altitude below which the sky is astronomically dark.
const twilight = -18

// Overview prints the equipment line, then per filter the integration time
// and one row per observing night: nights differ in seeing, transparency and
// moonlight, which a whole-session median hides.
//
// ponytail: equipment comes from the first frame; a session mixing
// telescopes or cameras shows only that one.
func Overview(w io.Writer, frames []Frame) {
	if len(frames) == 0 {
		return
	}
	if line := equipment(frames[0].Result.Meta); line != "" {
		fmt.Fprintf(w, "\n%s\n", line)
	}
	groups := byFilter(frames)
	for _, idx := range groups {
		sub := subset(frames, idx)
		overview(w, sub, filterLabel(sub, groups))
	}
}

// equipment describes target, optics, camera, CFA pattern, plate scale and
// field of view.
func equipment(m fits.Meta) string {
	var parts []string
	if m.Object != "" {
		parts = append(parts, m.Object)
	}
	scope := m.Telescope
	if m.Aperture > 0 && m.FocalLen > 0 { // false for NaN
		scope += fmt.Sprintf(" %.0f mm f/%.1f", m.Aperture, m.FocalLen/m.Aperture)
	} else if m.FocalLen > 0 {
		scope += fmt.Sprintf(" %.0f mm focal length", m.FocalLen)
	}
	if s := strings.TrimSpace(scope); s != "" {
		parts = append(parts, s)
	}
	if m.Camera != "" {
		parts = append(parts, m.Camera)
	}
	if m.Bayer != "" {
		cfa := "CFA " + m.Bayer
		if m.BayerFlip {
			cfa += " read from the top row (" + m.Bayer[2:] + m.Bayer[:2] + " from the first stored row)"
		}
		parts = append(parts, cfa)
	}
	if m.Plate > 0 {
		parts = append(parts, fmt.Sprintf("%.2f\"/px, field %.0f′ × %.0f′",
			m.Plate, float64(m.Size[0])*m.Plate/60, float64(m.Size[1])*m.Plate/60))
	}

	return strings.Join(parts, " · ")
}

func overview(w io.Writer, frames []Frame, label string) {
	var total, kept float64
	exps := map[float64]bool{}
	for _, f := range frames {
		if e := f.Result.Exp; !math.IsNaN(e) {
			total += e
			exps[e] = true
			if len(f.Flags) == 0 {
				kept += e
			}
		}
	}
	nights := byNight(frames)
	fmt.Fprintf(w, "\nIntegration%s: %d frames", label, len(frames))
	if len(exps) == 1 {
		fmt.Fprintf(w, " × %g s", frames[0].Result.Exp)
	}
	if total > 0 {
		fmt.Fprintf(w, " = %s, %s unflagged", hours(total), hours(kept))
	}
	if len(nights) > 1 {
		fmt.Fprintf(w, ", over %d nights", len(nights))
	}
	fmt.Fprintln(w)
	if len(nights) == 1 && nights[0].key == "" {
		return // no DATE-OBS
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  Night\tFrames\tTime\tFWHM px\tBackground\tSky mag\tBortle\tGradient\tMoon lit\tMoon up\tMoon dist\tTwilight")
	for _, n := range nights {
		sub := subset(frames, n.idx)
		col := func(get func(f Frame) float64) []float64 {
			v := make([]float64, 0, len(sub))
			for _, f := range sub {
				if x := get(f); !math.IsNaN(x) {
					v = append(v, x)
				}
			}

			return v
		}
		up, dusk := 0, 0
		for _, f := range sub {
			if f.Result.MoonAlt > 0 {
				up++
			}
			if f.Result.SunAlt > twilight {
				dusk++
			}
		}
		moonUp, twi := "-", "-"
		if !math.IsNaN(sub[0].Result.MoonAlt) {
			moonUp, twi = fmt.Sprintf("%.0f%%", 100*float64(up)/float64(len(sub))), strconv.Itoa(dusk)
		}
		fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", cmp.Or(n.key, "-"), len(sub), n.span,
			format(stats.MedianOf(col(func(f Frame) float64 { return f.Result.FWHM })), 2),
			format(stats.MedianOf(col(func(f Frame) float64 { return f.Result.Bg })), 1),
			format(stats.MedianOf(col(func(f Frame) float64 { return f.SkyMag })), 2),
			bortle(stats.MedianOf(col(func(f Frame) float64 { return zenith(f.SkyMag, f.Result.Alt) }))),
			gradient(sub),
			percent(stats.MedianOf(col(func(f Frame) float64 { return f.Result.MoonIllum }))),
			moonUp,
			degrees(stats.MedianOf(col(func(f Frame) float64 { return f.Result.MoonSep }))),
			twi)
	}
	tw.Flush()
}

// gradient is the night's median sky gradient with an arrow toward the
// brighter side: the strength-weighted mean direction, so frames with a
// negligible gradient cannot swing it.
func gradient(frames []Frame) string {
	var v []float64
	var x, y float64
	for _, f := range frames {
		if g := f.Result.Gradient; !math.IsNaN(g) {
			v = append(v, g)
			s, c := math.Sincos(f.Result.GradAngle * math.Pi / 180)
			x, y = x+g*c, y+g*s
		}
	}
	if len(v) == 0 {
		return "-"
	}

	return format(stats.MedianOf(v), 1) + " " + Arrow(math.Atan2(y, x)*180/math.Pi)
}

// Arrow draws a direction in degrees counter-clockwise from +x, with +y up
// as in the field map (top = last FITS row).
func Arrow(deg float64) string {
	i := int(math.Round(math.Mod(deg+360, 360)/45)) % 8

	return string([]rune("→↗↑↖←↙↓↘")[i])
}

type night struct {
	key  string // observing night's starting date, "" when unknown
	span string // local clock time of the first and last frame
	idx  []int
}

// byNight groups frames by observing night, earliest first. A night runs
// from local noon to noon, so a session crossing midnight stays one night.
// Local solar time comes from the site longitude, else from the header's
// time zone (DATE-OBS's own clock).
func byNight(frames []Frame) []night {
	var nights []night
	first, last := map[string]time.Time{}, map[string]time.Time{}
	for i, f := range frames {
		m := f.Result.Meta
		key := ""
		if !m.Time.IsZero() {
			t := m.Time
			if !math.IsNaN(m.Lon) {
				t = t.UTC().Add(time.Duration(m.Lon / 15 * float64(time.Hour)))
			}
			key = t.Add(-12 * time.Hour).Format("2006-01-02")
			if a, ok := first[key]; !ok || m.Time.Before(a) {
				first[key] = m.Time
			}
			if b, ok := last[key]; !ok || m.Time.After(b) {
				last[key] = m.Time
			}
		}
		j := slices.IndexFunc(nights, func(n night) bool { return n.key == key })
		if j < 0 {
			j = len(nights)
			nights = append(nights, night{key: key})
		}
		nights[j].idx = append(nights[j].idx, i)
	}
	for i := range nights {
		if k := nights[i].key; k != "" {
			nights[i].span = first[k].Format("15:04") + "–" + last[k].Format("15:04")
		}
	}
	slices.SortStableFunc(nights, func(a, b night) int { return strings.Compare(a.key, b.key) })

	return nights
}

func hours(s float64) string {
	m := int(math.Round(s / 60))

	return fmt.Sprintf("%d h %02d min", m/60, m%60)
}

func percent(v float64) string {
	if math.IsNaN(v) {
		return "-"
	}

	return fmt.Sprintf("%.0f%%", 100*v)
}

func degrees(v float64) string {
	if math.IsNaN(v) {
		return "-"
	}

	return fmt.Sprintf("%.0f°", v)
}

// Coverage prints, per filter, how much of the registered stack gets every
// frame: a rotating field (alt-az mounts turn 100°+ a night) or drifting
// pointing leaves the edges with fewer frames and more noise. It tells how
// much to crop. Flagged frames are left out, as they are not stacked.
func Coverage(w io.Writer, frames []Frame) {
	groups := byFilter(frames)
	for _, idx := range groups {
		sub := subset(frames, idx)
		var ws []fits.WCS
		for _, f := range sub {
			if wcs := f.Result.WCS; len(f.Flags) == 0 && wcs.OK && wcs.NAXIS[0] > 0 {
				ws = append(ws, wcs)
			}
		}
		if len(ws) < 2 {
			continue
		}
		rot, full, most, half := coverage(ws)
		fmt.Fprintf(w, "\nStack coverage%s: %d plate-solved frames, field rotates %.0f°\n", filterLabel(sub, groups), len(ws), rot)
		fmt.Fprintf(w, "  every frame over %.0f%% of the reference frame, 90%% of frames over %.0f%%, half over %.0f%%\n",
			100*full, 100*most, 100*half)
	}
}

// coverage samples a grid over the first frame, the registration reference,
// and counts how many frames see each point on the sky. It returns the
// rotation span in degrees and the fractions of the reference covered by
// all, 90% and half of the frames.
//
// ponytail: the reference is the first solved frame; any frame of a session
// rotating about its target gives about the same answer.
func coverage(ws []fits.WCS) (float64, float64, float64, float64) {
	ref := ws[0]
	counts, nx, ny := coverCounts(ws)
	var full, most, half int
	for _, n := range counts {
		if n == len(ws) {
			full++
		}
		if float64(n) >= 0.9*float64(len(ws)) {
			most++
		}
		if 2*n >= len(ws) {
			half++
		}
	}
	lo, hi := 0.0, 0.0
	for _, f := range ws {
		d := math.Mod(f.Rotation()-ref.Rotation()+540, 360) - 180
		lo, hi = min(lo, d), max(hi, d)
	}
	cells := float64(nx * ny)

	return hi - lo, float64(full) / cells, float64(most) / cells, float64(half) / cells
}

// coverCounts is the number of frames that see each point of a grid over
// the first frame, row-major from its first FITS row, nx = 64 points wide.
func coverCounts(ws []fits.WCS) ([]int, int, int) {
	ref := ws[0]
	w, h := float64(ref.NAXIS[0]), float64(ref.NAXIS[1])
	nx := 64
	ny := max(1, int(math.Round(float64(nx)*h/w)))
	counts := make([]int, 0, nx*ny)
	for j := range ny {
		for i := range nx {
			// 1-based FITS pixel coordinates, pixel centres at integers.
			ra, dec := ref.World(0.5+(float64(i)+0.5)*w/float64(nx), 0.5+(float64(j)+0.5)*h/float64(ny))
			n := 0
			for _, f := range ws {
				x, y := f.Pixel(ra, dec)
				if x >= 0.5 && x <= float64(f.NAXIS[0])+0.5 && y >= 0.5 && y <= float64(f.NAXIS[1])+0.5 {
					n++
				}
			}
			counts = append(counts, n)
		}
	}

	return counts, nx, ny
}
