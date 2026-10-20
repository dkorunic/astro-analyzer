// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package report

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dkorunic/astro-analyzer/internal/analyze"
	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/stats"
)

// N.I.N.A.'s sequencer trigger defaults (NINA.Sequencer/Trigger, commit
// 4742afa), which the acquisition checks translate the session into.
const (
	ninaHFRAmount  = 5  // Autofocus/AutofocusAfterHFRIncreaseTrigger.cs:86, %
	ninaHFRSamples = 10 // ibid. :90, frames in the trend
	ninaTempAmount = 5  // Autofocus/AutofocusAfterTemperatureChangeTrigger.cs:83, °C
	ninaAFMinutes  = 30 // Autofocus/AutofocusAfterTimeTrigger.cs:86, min
	ninaAFFrames   = 5  // Autofocus/AutofocusAfterExposures.cs:83, light frames
	ninaDrift      = 10 // Platesolving/CenterAfterDriftTrigger.cs:106, arcmin
)

// windShake is the smallest rank correlation between wind and
// eccentricity reported. ponytail: as cloud's −0.5, a round number.
const windShake = 0.5

const (
	// coolerTol is how far the sensor may sit from the cooler's setpoint.
	// ponytail: a guess; regulated coolers hold 0.1–0.5 °C once settled.
	coolerTol = 1
	// dewMargin: ponytail: a rule of thumb, as a tube radiating to a clear
	// sky runs 1–3 °C below the air around it.
	dewMargin = 2
	// ditherMin is the smallest jump, in sensor pixels, off the drift
	// around it that counts as a dither. ponytail: guided drift and seeing
	// move a plate solution's centre well under a pixel per frame.
	ditherMin = 1.5
	// ditherWindow is the steps either side whose median is the drift.
	ditherWindow = 5
	// pointingGap splits the session into runs: a longer pause is a
	// slew, a cloud break or a new night.
	pointingGap = 10 * time.Minute
	// flipTol is how close to 180° a jump in field rotation must be to be
	// a meridian flip, and flipSteady how little the field may turn
	// otherwise: an alt-az mount turns far more and never flips.
	flipTol    = 15
	flipSteady = 5
)

// focusRun is a stretch of consecutive frames at one focuser position,
// frames[start:end]. moved is set when the frame before it was at another
// position: the focuser moved, usually an autofocus.
type focusRun struct {
	start, end int
	moved      bool
}

// focusRuns splits frames at focuser moves (FOCUSPOS); frames without a
// position belong to no run.
func focusRuns(frames []Frame) []focusRun {
	var runs []focusRun
	for i := 0; i < len(frames); {
		j := i + 1
		for j < len(frames) && frames[j].Result.Focus == frames[i].Result.Focus {
			j++
		}
		if !math.IsNaN(frames[i].Result.Focus) {
			runs = append(runs, focusRun{start: i, end: j, moved: i > 0 && !math.IsNaN(frames[i-1].Result.Focus)})
		}
		i = j
	}

	return runs
}

// ends is the median of get over a run's first and last refocusEnds
// frames, NaN where they have none.
func ends(run []Frame, get func(analyze.Result) float64) (float64, float64) {
	first, last := make([]float64, 0, refocusEnds), make([]float64, 0, refocusEnds)
	for k := range min(refocusEnds, len(run)) {
		first, last = append(first, get(run[k].Result)), append(last, get(run[len(run)-1-k].Result))
	}

	return stats.MedianOf(slices.DeleteFunc(first, math.IsNaN)), stats.MedianOf(slices.DeleteFunc(last, math.IsNaN))
}

func fwhmOf(r analyze.Result) float64      { return r.FWHM }
func focusTempOf(r analyze.Result) float64 { return r.FocusTemp }

// focusSlope fits the focuser position against its temperature at the
// start of each run, where the autofocus left it: the slope and intercept
// N.I.N.A.'s Move Focuser By Temperature takes (position = Slope·T +
// Intercept, SequenceItem/Focuser/MoveFocuserByTemperature.cs:80). The
// fit is least absolute deviation, so one failed autofocus cannot tilt it.
// "" with fewer than 3 positions or a temperature span under 1 °C.
//
// ponytail: filter offsets are not modelled; each filter is fitted alone.
func focusSlope(frames []Frame) string {
	var temp, pos []float64
	for _, run := range focusRuns(frames) {
		if t, _ := ends(frames[run.start:run.end], focusTempOf); !math.IsNaN(t) {
			temp, pos = append(temp, t), append(pos, frames[run.start].Result.Focus)
		}
	}
	if len(temp) < 3 || slices.Max(temp)-slices.Min(temp) < 1 {
		return ""
	}
	span := slices.Max(temp) - slices.Min(temp)
	r, _ := stats.Spearman(pos, temp)
	if !(math.Abs(r) > 0.5) {
		return fmt.Sprintf("Focus position does not follow focuser temperature (rank correlation %+.2f over %d positions and %.1f °C): temperature compensation would not help",
			r, len(temp), span)
	}
	a, b := stats.LineFit(temp, pos)

	return fmt.Sprintf("Focus follows focuser temperature: %+.1f steps/°C over %d positions and %.1f °C (rank correlation %+.2f); N.I.N.A. Move Focuser By Temperature: Slope %.1f, Intercept %.0f",
		b, len(temp), span, r, b, a)
}

// tempTrigger turns FWHM growth at a fixed focuser position into a
// temperature step: the median, over runs whose focuser temperature moved
// at least 0.5 °C, of growth per °C, and the change that grows FWHM by
// N.I.N.A.'s default HFR trigger, the threshold for its Autofocus After
// Temperature Change. "" with fewer than 3 such runs or no growth.
//
// ponytail: as in refocus, seeing and altitude changes count as drift.
func tempTrigger(frames []Frame) string {
	var rates []float64
	for _, run := range focusRuns(frames) {
		r := frames[run.start:run.end]
		if len(r) < refocusRun {
			continue
		}
		f0, f1 := ends(r, fwhmOf)
		t0, t1 := ends(r, focusTempOf)
		if dt := math.Abs(t1 - t0); dt >= 0.5 { // false for NaN
			if g := (f1/f0 - 1) / dt; !math.IsNaN(g) {
				rates = append(rates, g)
			}
		}
	}
	rate := stats.MedianOf(rates)
	if len(rates) < 3 || !(rate > 0) {
		return ""
	}

	return fmt.Sprintf("FWHM grows %.1f%%/°C at a fixed focuser position (median of %d runs): Autofocus After Temperature Change at %.1f °C keeps growth under %d%% (N.I.N.A. default %d °C)",
		100*rate, len(rates), ninaHFRAmount/100.0/rate, ninaHFRAmount, ninaTempAmount)
}

// timeTrigger is tempTrigger against the clock: the median, over runs at
// one focuser position, of FWHM growth per hour between the medians of the
// first and last refocusEnds frames, and the time, and frames at the
// session's cadence, that grow FWHM by N.I.N.A.'s default HFR trigger: the
// thresholds for its Autofocus After Time and After Exposures. "" with
// fewer than 3 such runs or no growth.
//
// ponytail: as in refocus, seeing and altitude changes count as drift.
func timeTrigger(frames []Frame) string {
	var rates, cadence []float64
	for _, run := range focusRuns(frames) {
		r := frames[run.start:run.end]
		if len(r) < refocusRun {
			continue
		}
		// The median times of the end frames, as frames run in time order.
		a, b := refocusEnds/2, len(r)-1-refocusEnds/2
		t0, t1 := r[a].Result.Time, r[b].Result.Time
		if t0.IsZero() || !t1.After(t0) {
			continue
		}
		h := t1.Sub(t0).Hours()
		f0, f1 := ends(r, fwhmOf)
		if g := (f1/f0 - 1) / h; !math.IsNaN(g) {
			rates, cadence = append(rates, g), append(cadence, 60*h/float64(b-a))
		}
	}
	rate := stats.MedianOf(rates)
	if len(rates) < 3 || !(rate > 0) {
		return ""
	}
	minutes := 60 * ninaHFRAmount / 100.0 / rate

	return fmt.Sprintf("FWHM grows %.1f%%/h at a fixed focuser position (median of %d runs): Autofocus After Time at %.0f min, or After Exposures at %d, keeps growth under %d%% (N.I.N.A. defaults %d min, %d)",
		100*rate, len(rates), minutes, max(1, int(minutes/stats.MedianOf(cadence))), ninaHFRAmount, ninaAFMinutes, ninaAFFrames)
}

// hfrTrigger replays N.I.N.A.'s Autofocus After HFR Increase at its
// defaults on each run at one focuser position: how many runs it would
// have cut short, and after how many frames. "" without runs or HFR.
func hfrTrigger(frames []Frame) string {
	var after, exps []float64
	runs := 0
	for _, run := range focusRuns(frames) {
		hfr := make([]float64, 0, run.end-run.start)
		for _, f := range frames[run.start:run.end] {
			hfr, exps = append(hfr, f.Result.HFR), append(exps, f.Result.Exp)
		}
		if !slices.ContainsFunc(hfr, func(h float64) bool { return h > 0 }) {
			continue
		}
		runs++
		if n := hfrFires(hfr); n >= 0 {
			after = append(after, float64(n))
		}
	}
	if runs == 0 {
		return ""
	}
	head := fmt.Sprintf("N.I.N.A. Autofocus After HFR Increase (%d%%, %d frames) would refocus ", ninaHFRAmount, ninaHFRSamples)
	if len(after) == 0 {
		return head + fmt.Sprintf("none of %d runs", runs)
	}
	n := stats.MedianOf(after)
	s := fmt.Sprintf("%d of %d runs, after a median of %.0f frames", len(after), runs, n)
	if e := stats.MedianOf(slices.DeleteFunc(exps, math.IsNaN)); e > 0 {
		s += fmt.Sprintf(" (%.0f min)", n*e/60)
	}

	return head + s
}

// hfrFires is N.I.N.A.'s HFR trigger (AutofocusAfterHFRIncreaseTrigger.cs:
// 148-208) checked after each frame since the autofocus: the least-squares
// line through the last ninaHFRSamples HFRs, read at the newest, against
// the lowest HFR so far. It returns how many frames were taken when it
// fires, -1 if it never does. Frames without an HFR are skipped, as there.
func hfrFires(hfr []float64) int {
	var hist []float64
	for k, h := range hfr {
		if !(h > 0) {
			continue
		}
		hist = append(hist, h)
		data := hist[max(0, len(hist)-ninaHFRSamples):]
		if len(data) < 3 {
			continue
		}
		var sx, sy, sxy, sxx float64
		for i, y := range data {
			x := float64(i + 1)
			sx, sy, sxy, sxx = sx+x, sy+y, sxy+x*y, sxx+x*x
		}
		n := float64(len(data))
		b := (n*sxy - sx*sy) / (n*sxx - sx*sx)
		trend := (sy-b*sx)/n + b*n
		if (trend/slices.Min(hist)-1)*100 > ninaHFRAmount {
			return k + 1
		}
	}

	return -1
}

// cooler checks the sensor held the cooler's setpoint (SET-TEMP): a
// cooler at full power on a warm night cannot, and those frames no longer
// match darks taken at the setpoint. "" without setpoints.
func cooler(frames []Frame) string {
	var sets []float64
	n, off, worst := 0, 0, 0.0
	for _, f := range frames {
		s, t := f.Result.SetTemp, f.Result.Temp
		if math.IsNaN(s) || math.IsNaN(t) {
			continue
		}
		n++
		sets = appendKnown(sets, s)
		if d := t - s; math.Abs(d) > coolerTol {
			off++
			if math.Abs(d) > math.Abs(worst) {
				worst = d
			}
		}
	}
	if n == 0 {
		return ""
	}
	if off == 0 {
		return fmt.Sprintf("Cooler held its setpoint %s °C within %d °C on all %d frames", values(sets), coolerTol, n)
	}

	return fmt.Sprintf("Cooler missed its setpoint %s °C by over %d °C on %d of %d frames (worst %+.1f °C): darks at the setpoint do not match them",
		values(sets), coolerTol, off, n, worst)
}

// dewPoint is the header's DEWPOINT, else the Magnus formula on ambient
// temperature and humidity with Sonntag's (1990) coefficients, as WMO
// No. 8 gives them; NaN without either.
func dewPoint(m fits.Meta) float64 {
	if !math.IsNaN(m.DewPoint) {
		return m.DewPoint
	}
	if !(m.Humidity > 0) {
		return math.NaN()
	}
	const b, c = 17.62, 243.12
	g := math.Log(m.Humidity/100) + b*m.Ambient/(c+m.Ambient)

	return c * g / (b - g)
}

// dew notes frames taken with the air within dewMargin of its dew point,
// from a weather station's AMBTEMP and DEWPOINT (or HUMIDITY), and how
// many of them were flagged. "" when none came that close.
func dew(frames []Frame) string {
	n, near, flagged := 0, 0, 0
	closest := math.Inf(1)
	for _, f := range frames {
		m := f.Result.Ambient - dewPoint(f.Result.Meta)
		if math.IsNaN(m) {
			continue
		}
		n++
		closest = min(closest, m)
		if m < dewMargin {
			near++
			if len(f.Flags) > 0 {
				flagged++
			}
		}
	}
	if near == 0 {
		return ""
	}

	return fmt.Sprintf("Dew: the air was within %d °C of its dew point on %d of %d frames (closest %.1f °C, %d of them flagged); optics run colder than the air, so check the dew heaters",
		dewMargin, near, n, closest, flagged)
}

// cloud notes when the star count falls as a cloud sensor reads cloudier:
// N.I.N.A.'s CLOUDCVR (%), else the sky temperature over the ambient one,
// which a clear sky keeps far below. "" under 8 frames or a rank
// correlation above −0.5.
func cloud(frames []Frame) string {
	stars, sky := make([]float64, len(frames)), make([]float64, len(frames))
	cover := slices.ContainsFunc(frames, func(f Frame) bool { return !math.IsNaN(f.Result.Cloud) })
	src := "cloud cover"
	if !cover {
		src = "sky temperature over ambient"
	}
	for i, f := range frames {
		stars[i], sky[i] = float64(f.Result.Stars), f.Result.Cloud
		if !cover {
			sky[i] = f.Result.SkyTemp - f.Result.Ambient
		}
	}
	r, n := stats.Spearman(stars, sky)
	if n < 8 || !(r <= -0.5) {
		return ""
	}

	return fmt.Sprintf("Star count falls as the cloud sensor reads cloudier (%s, rank correlation %+.2f): low star counts are cloud, not focus or seeing", src, r)
}

// wind notes when stars elongate as the wind rises (WINDGUST, else
// WINDSPD): gusts shake the tube along one axis during an exposure. ""
// under 8 frames or a rank correlation under windShake.
func wind(frames []Frame) string {
	ecc, speed := make([]float64, len(frames)), make([]float64, len(frames))
	var known []float64
	for i, f := range frames {
		ecc[i], speed[i] = f.Result.Ecc, f.Result.Wind
		known = appendKnown(known, f.Result.Wind)
	}
	r, n := stats.Spearman(ecc, speed)
	if n < 8 || !(r >= windShake) {
		return ""
	}

	return fmt.Sprintf("Eccentricity rises with the wind (%.0f–%.0f km/h, rank correlation %+.2f over %d frames): elongated stars are wind shake, not guiding or tilt",
		slices.Min(known), slices.Max(known), r, n)
}

// solvedFrame is a plate-solved frame with a time, by index into frames.
type solvedFrame struct {
	t   time.Time
	wcs fits.WCS
	i   int
}

// centre is a solution's frame centre in its 1-based pixel coordinates.
func centre(w fits.WCS) (float64, float64) {
	return (float64(w.NAXIS[0]) + 1) / 2, (float64(w.NAXIS[1]) + 1) / 2
}

// shift is where frame a's centre lands in frame b, relative to b's
// centre, in sensor pixels.
func shift(a, b fits.WCS) [2]float64 {
	ra, dec := a.World(centre(a))
	x, y := b.Pixel(ra, dec)
	cx, cy := centre(b)

	return [2]float64{x - cx, y - cy}
}

// rotDiff is the smallest angle between two field rotations, 0–180°.
func rotDiff(a, b fits.WCS) float64 {
	return math.Abs(math.Mod(b.Rotation()-a.Rotation()+540, 360) - 180)
}

// pointing holds the session's plate-solved frames split into runs at
// pauses and meridian flips, and the flips between them.
type pointing struct {
	runs  [][]solvedFrame
	flips []solvedFrame // first frame after each flip
}

// newPointing splits the plate-solved frames, in time order, at pauses
// longer than pointingGap and at meridian flips. A flip is a jump of 180°
// ± flipTol in field rotation, or a PIERSIDE change; rotation jumps only
// count when the field otherwise holds within flipSteady, as on an
// equatorial mount.
func newPointing(frames []Frame) pointing {
	var all []solvedFrame
	for i, f := range frames {
		if w := f.Result.WCS; w.OK && w.NAXIS[0] > 0 && !f.Result.Time.IsZero() {
			all = append(all, solvedFrame{wcs: w, t: f.Result.Time, i: i})
		}
	}
	if len(all) == 0 {
		return pointing{}
	}
	jump := func(k int) bool {
		a, b := frames[all[k-1].i].Result.Pier, frames[all[k].i].Result.Pier
		if a != "" && b != "" {
			return a != b
		}

		return rotDiff(all[k-1].wcs, all[k].wcs) > 180-flipTol
	}
	// Equatorial: off the jumps, the field keeps within flipSteady of
	// where each side started.
	steady, ref := true, all[0].wcs
	for k := 1; k < len(all); k++ {
		if jump(k) {
			ref = all[k].wcs
		} else if rotDiff(ref, all[k].wcs) > flipSteady {
			steady = false
		}
	}
	var p pointing
	run := []solvedFrame{all[0]}
	for k := 1; k < len(all); k++ {
		flip := jump(k) && (steady || frames[all[k].i].Result.Pier != "")
		if flip {
			p.flips = append(p.flips, all[k])
		}
		if flip || all[k].t.Sub(all[k-1].t) > pointingGap {
			p.runs = append(p.runs, run)
			run = nil
		}
		run = append(run, all[k])
	}
	p.runs = append(p.runs, run)

	return p
}

// Pointing prints how the mount held the target, from the plate
// solutions: dither cadence, pointing drift and meridian flips, with the
// N.I.N.A. trigger settings they bear on. Nothing under 3 solved frames.
func Pointing(w io.Writer, frames []Frame) {
	p := newPointing(frames)
	n := 0
	for _, r := range p.runs {
		n += len(r)
	}
	if n < 3 {
		return
	}
	fmt.Fprintf(w, "\nPointing: %d plate-solved frames in %s\n", n, count(len(p.runs), "run"))
	if s := dithers(p); s != "" {
		fmt.Fprintf(w, "  Dither  %s\n", s)
	}
	if s := drift(p); s != "" {
		fmt.Fprintf(w, "  Drift   %s\n", s)
	}
	for _, f := range p.flips {
		fmt.Fprintf(w, "  Flip    meridian flip before %s (%s): the field map is split by pier side\n",
			filepath.Base(frames[f.i].Path), f.t.Format("2006-01-02 15:04"))
	}
}

// ditherSteps holds the dithers between consecutive frames of each run:
// their sizes in pixels, the steps between them, the most frames without
// one, and how many steps there were.
type ditherSteps struct {
	size, gaps     []float64
	steps, longest int
}

// measureDithers classifies each step between consecutive frames of a
// run: a dither when it lands at least ditherMin pixels off the median
// step of the ditherWindow steps either side, which is the drift; dithers
// are random, so they do not move that median.
func measureDithers(p pointing) ditherSteps {
	var d ditherSteps
	for _, run := range p.runs {
		v := make([][2]float64, 0, len(run))
		for k := 1; k < len(run); k++ {
			v = append(v, shift(run[k-1].wcs, run[k].wcs))
		}
		d.steps += len(v)
		last := -1 // step index of the previous dither
		for k := range v {
			lo, hi := max(0, k-ditherWindow), min(len(v), k+ditherWindow+1)
			xs, ys := make([]float64, 0, hi-lo), make([]float64, 0, hi-lo)
			for _, s := range v[lo:hi] {
				xs, ys = append(xs, s[0]), append(ys, s[1])
			}
			off := math.Hypot(v[k][0]-stats.MedianOf(xs), v[k][1]-stats.MedianOf(ys))
			if off < ditherMin {
				continue
			}
			d.size = append(d.size, off)
			if last >= 0 {
				d.gaps = append(d.gaps, float64(k-last))
			}
			d.longest = max(d.longest, k-last)
			last = k
		}
		d.longest = max(d.longest, len(v)-last)
	}

	return d
}

// positions is how many places on the sensor the frames sat: each run
// starts one and each dither adds one. An upper bound, as random dithers
// may return near an earlier spot.
func (d ditherSteps) positions(runs int) int { return runs + len(d.size) }

// dithers describes the dither cadence; "" without steps.
func dithers(p pointing) string {
	d := measureDithers(p)
	if d.steps == 0 {
		return ""
	}
	if len(d.size) == 0 {
		return fmt.Sprintf("none in %d frame steps: hot pixels and pattern noise stay on one sky position and survive rejection, and walk across the stack where the field drifts; N.I.N.A.: Dither After Exposures (default 1)",
			d.steps)
	}
	every := "once per run"
	if len(d.gaps) > 0 {
		every = "every frame"
		if g := stats.MedianOf(d.gaps); g > 1 {
			every = fmt.Sprintf("every %.0f frames (median)", g)
		}
	}

	return fmt.Sprintf("%s, %d dithers of %.1f px (median), at most %d frames without one", every, len(d.size), stats.MedianOf(d.size), d.longest)
}

// drift is the pointing drift: per run of at least 5 frames over 15 min,
// the least-squares rate of the frame centre in arcmin per hour on each
// axis, combined; the median over runs, the farthest any frame got from
// its run's first, and how often N.I.N.A.'s Center After Drift would
// re-centre at its default. "" without such runs or a plate scale.
//
// ponytail: dithers are taken as part of the drift; random ones barely
// tilt a fit over a run.
func drift(p pointing) string {
	var rates []float64
	far, fires := 0.0, 0
	for _, run := range p.runs {
		ref := run[0].wcs
		cd := ref.CD
		scale := math.Sqrt(math.Abs(cd[0][0]*cd[1][1]-cd[0][1]*cd[1][0])) * 60 // arcmin/px
		if !(scale > 0) {
			continue
		}
		var st, sx, sy, stt, stx, sty float64
		fired := false
		for _, f := range run {
			s := shift(f.wcs, ref)
			x, y := s[0]*scale, s[1]*scale
			t := f.t.Sub(run[0].t).Hours()
			st, sx, sy, stt, stx, sty = st+t, sx+x, sy+y, stt+t*t, stx+t*x, sty+t*y
			d := math.Hypot(x, y)
			far = max(far, d)
			if d >= ninaDrift && !fired {
				fires++
				fired = true // re-centring resets the reference; count once
			}
		}
		n := float64(len(run))
		den := n*stt - st*st
		if len(run) < 5 || run[len(run)-1].t.Sub(run[0].t) < 15*time.Minute || den == 0 {
			continue
		}
		rates = append(rates, math.Hypot((n*stx-st*sx)/den, (n*sty-st*sy)/den))
	}
	if len(rates) == 0 {
		return ""
	}
	rate := stats.MedianOf(rates)
	s := fmt.Sprintf("%.1f′/h (median of %s), at most %.1f′ from a run's first frame; N.I.N.A. Center After Drift at %d′ (default) ",
		rate, count(len(rates), "run"), far, ninaDrift)
	if fires == 0 {
		return s + "would not fire"
	}

	return s + "would fire in " + count(fires, "run")
}

// count is n with the noun, plural unless n is 1.
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}

	return fmt.Sprintf("%d %ss", n, noun)
}

// pierSides labels each frame by pier side when the session crosses a
// meridian flip: PIERSIDE if the header has it, else before and after the
// flip (sides 1 and 2 when it flips more than once). Unsolved frames take
// the side of the solved frame before them. nil without a flip.
func pierSides(frames []Frame) []string {
	p := newPointing(frames)
	if len(p.flips) == 0 {
		return nil
	}
	labels := make([]string, len(frames))
	side, next := 0, 0
	for i := range frames {
		if next < len(p.flips) && i >= p.flips[next].i {
			side, next = 1-side, next+1
		}
		switch {
		case frames[i].Result.Pier != "":
			labels[i] = "pier " + strings.ToLower(frames[i].Result.Pier)
		case len(p.flips) == 1:
			labels[i] = [...]string{"before the flip", "after the flip"}[side]
		default:
			labels[i] = fmt.Sprintf("pier side %d", side+1)
		}
	}

	return labels
}
