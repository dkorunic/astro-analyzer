// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

// Command astro-analyzer reports star count, FWHM, eccentricity, SNR and sky
// background statistics for FITS light frames, with session trends, outlier
// flags and a field map. Everything runs locally.
package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/dkorunic/astro-analyzer/internal/analyze"
	"github.com/dkorunic/astro-analyzer/internal/fits"
	"github.com/dkorunic/astro-analyzer/internal/psf"
	"github.com/dkorunic/astro-analyzer/internal/report"
)

// Build metadata, set with -ldflags "-X main.GitTag=..." by Taskfile.yml and
// .goreleaser.yml.
var (
	GitTag    = "dev"
	GitCommit = "unknown"
	GitDirty  = ""
	BuildTime = "unknown"
)

func main() {
	sigma := flag.Float64("sigma", 5, "detection threshold in background sigmas")
	model := flag.String("psf", psf.Moffat4, "PSF model for FWHM and eccentricity: moffat4 (PixInsight default), moffat (free β, slower) or gaussian")
	gain := flag.Float64("gain", 0, "camera gain in e-/ADU (default: EGAIN header; unknown gain omits photon noise, as PixInsight does)")
	k := flag.Float64("reject-sigma", 3, "flag frames deviating more than this many robust sigmas (MAD) from the session median")
	minDev := flag.Float64("reject-min", 0.1, "...and by at least this fraction of the median (guards very stable sessions)")
	readNoise := flag.Float64("read-noise", 0, "camera read noise in e- for the sub-length comparison (default: a preset for the camera at its GAIN)")
	best := flag.Float64("best", 100, "also flag frames outside the best this percent per filter by wFWHM (Siril's -filter-wfwhm=N%)")
	reject := flag.Bool("reject", false, "print only the paths of flagged frames, one per line")
	csvOut := flag.Bool("csv", false, "write CSV instead of the table")
	jsonOut := flag.Bool("json", false, "write JSON instead of the table")
	version := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] file.fits|dir...\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if *version {
		fmt.Printf("astro-analyzer %s%s (%s, built %s)\n", GitTag, strings.TrimSpace(GitDirty), GitCommit, BuildTime)

		return
	}
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if !(*best > 0 && *best <= 100) {
		fmt.Fprintln(os.Stderr, "-best must be a percentage in (0, 100]")
		os.Exit(2)
	}
	if !psf.Valid(*model) {
		fmt.Fprintf(os.Stderr, "-psf must be %s, %s or %s\n", psf.Moffat4, psf.Moffat, psf.Gaussian)
		os.Exit(2)
	}
	// Results only print once every frame is done, so show progress on an
	// interactive stderr; large sessions otherwise look hung for minutes.
	fi, _ := os.Stderr.Stat()
	tty := fi != nil && fi.Mode()&os.ModeCharDevice != 0
	var found, done, measured atomic.Int64
	stop := func() {}
	if tty {
		stop = spin(func() string { return fmt.Sprintf("scanning: %d FITS files", found.Load()) })
	}
	files, unread, err := expand(flag.Args(), func(n int) { found.Store(int64(n)) })
	stop()
	if tty && err == nil {
		fmt.Fprintf(os.Stderr, "✓ found %d FITS files\n", len(files))
	}
	for _, e := range unread {
		fmt.Fprintf(os.Stderr, "skipped %v\n", e)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	res := make([]analyze.Result, len(files))
	errs := make([]error, len(files))
	cals := make([]report.Calib, len(files))
	sem := make(chan struct{}, runtime.NumCPU())
	start := time.Now()
	if tty {
		stop = spin(func() string { return fmt.Sprintf("measuring: %d/%d frames", done.Load(), len(files)) })
	}
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			defer done.Add(1)
			img, err := fits.Load(f)
			if errors.Is(err, fits.ErrNotLight) {
				errs[i], cals[i] = err, calibFrame(f, img, *sigma, *model)

				return
			}
			if err != nil {
				errs[i] = err

				return
			}
			if *gain > 0 {
				img.Gain = *gain
			}
			res[i] = analyze.Frame(img, *sigma, *model)
			measured.Add(1)
		})
	}
	wg.Wait()
	stop()
	if tty {
		// Light frames only: calibration frames and load errors are reported below.
		fmt.Fprintf(os.Stderr, "✓ measured %d frames in %.1f s\n", measured.Load(), time.Since(start).Seconds())
	}

	frames, calib, exit := collect(files, errs, res, cals)
	// Chronological order makes the trend lines meaningful; ISO dates sort as strings.
	if !slices.ContainsFunc(frames, func(f report.Frame) bool { return f.Result.Date == "" }) {
		slices.SortStableFunc(frames, func(a, b report.Frame) int { return cmp.Compare(a.Result.Date, b.Result.Date) })
	}
	report.Score(frames)
	report.FlagOutliers(frames, *k, *minDev)
	report.FlagWorst(frames, *best)

	switch {
	case *reject:
		for _, f := range frames {
			if len(f.Flags) > 0 {
				fmt.Println(f.Path)
			}
		}
	case *jsonOut:
		err = report.JSON(os.Stdout, frames)
	case *csvOut:
		err = report.CSV(os.Stdout, frames)
	default:
		report.Table(os.Stdout, frames)
		report.Overview(os.Stdout, frames)
		rn, src := readNoiseFor(*readNoise, frames, calib)
		report.Calibration(os.Stdout, frames, calib, rn, fits.PairGain)
		report.Summary(os.Stdout, frames)
		report.Field(os.Stdout, frames)
		report.Coverage(os.Stdout, frames)
		report.Drizzle(os.Stdout, frames)
		report.WBPP(os.Stdout, frames)
		report.SubLength(os.Stdout, frames, rn, src)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exit = 1
	}
	os.Exit(exit)
}

// collect sorts the per-file results into light frames and calibration
// frames (stacks are dropped), printing load errors and a count of the
// skipped frames by type to stderr. exit is 1 when a file failed to load.
func collect(files []string, errs []error, res []analyze.Result, cals []report.Calib) ([]report.Frame, []report.Calib, int) {
	exit := 0
	skipped := map[string]int{}
	var frames []report.Frame
	var calib []report.Calib
	for i, f := range files {
		if errors.Is(errs[i], fits.ErrNotLight) {
			skipped[cals[i].Image.Meta.Type]++
			if cals[i].Image.Meta.Type != fits.Stack {
				calib = append(calib, cals[i])
			}

			continue
		}
		if errs[i] != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, errs[i])
			exit = 1

			continue
		}
		frames = append(frames, report.Frame{Path: f, Result: res[i]})
	}
	if len(skipped) > 0 {
		parts := make([]string, 0, len(skipped))
		for _, t := range slices.Sorted(maps.Keys(skipped)) {
			parts = append(parts, fmt.Sprintf("%d %s", skipped[t], t))
		}
		fmt.Fprintf(os.Stderr, "skipped frames that are not light subs: %s\n", strings.Join(parts, ", "))
	}

	return frames, calib, exit
}

// calibFrame keeps what the report needs of a calibration frame. A dark
// goes through the light analysis, so its hot pixels are measured exactly
// as the lights' are; then the pixels are dropped.
func calibFrame(path string, img fits.Image, sigma float64, model string) report.Calib {
	c := report.Calib{Path: path}
	if img.Meta.Type == fits.Dark && img.Pix != nil {
		c.Hot = analyze.Frame(img, sigma, model).Hot
	}
	img.Pix, img.Sat, img.Colour = nil, nil, [3][]float32{}
	c.Image = img

	return c
}

// readNoiseFor picks the read noise for the sub-length comparison: the
// -read-noise flag, else the session's bias frames; NaN leaves it to the
// camera preset.
func readNoiseFor(flagged float64, frames []report.Frame, calib []report.Calib) (float64, string) {
	if flagged > 0 {
		return flagged, "-read-noise"
	}

	return report.BiasReadNoise(frames, calib, fits.PairNoise)
}

// spin animates a spinner in front of status() on stderr until the returned
// stop is called, which clears the line. It redraws every 80 ms on its own
// goroutine, so it keeps turning while a slow directory read or a long frame
// blocks the work it reports on.
func spin(status func() string) func() {
	done := make(chan struct{})
	var (
		wg    sync.WaitGroup
		width int // runes on the line now, read by stop after wg.Wait
	)
	wg.Go(func() {
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			line := fmt.Sprintf("%c %s", frames[i%len(frames)], status())
			n := utf8.RuneCountInString(line)
			// Spaces, not ESC[K: the legacy Windows console prints escapes as ←[K.
			fmt.Fprintf(os.Stderr, "\r%s%s", line, strings.Repeat(" ", max(width-n, 0)))
			width = n
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	})

	return func() {
		close(done)
		wg.Wait()
		fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", width))
	}
}

// expand replaces directories with the FITS files found beneath them.
// Explicitly named files are kept regardless of extension. progress, unless
// nil, is called with the running file count at every directory entry.
// Unreadable directories below a named one, such as a volume root's system
// folders, are skipped and their errors returned in unread; an unreadable
// named path is an error. Hidden entries below a named directory are skipped:
// macOS writes a "._name.fits" AppleDouble file beside every file on exFAT
// and FAT media. A path reached twice (overlapping arguments) is kept once,
// so it does not weigh twice in the session statistics.
//
// ponytail: duplicates are matched by cleaned path; a symlink to a frame
// found elsewhere still counts twice.
func expand(args []string, progress func(found int)) ([]string, []error, error) {
	var files []string
	var unread []error
	seen := map[string]bool{}
	add := func(p string) {
		if c := filepath.Clean(p); !seen[c] {
			seen[c] = true
			files = append(files, p)
		}
	}
	for _, a := range args {
		fi, err := os.Stat(a)
		if err != nil {
			return nil, nil, err
		}
		if !fi.IsDir() {
			add(a)

			continue
		}
		err = filepath.WalkDir(a, func(p string, d fs.DirEntry, err error) error {
			if err != nil && p != a && d != nil && d.IsDir() {
				unread = append(unread, err)

				return fs.SkipDir
			}
			if err != nil {
				return err
			}
			if p != a && strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return fs.SkipDir
				}

				return nil
			}
			if progress != nil {
				progress(len(files))
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".fits", ".fit", ".fts":
				if !d.IsDir() {
					add(p)
				}
			}

			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}

	return files, unread, nil
}
