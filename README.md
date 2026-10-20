# astro-analyzer

[![GitHub release](https://img.shields.io/github/v/release/dkorunic/astro-analyzer)](https://github.com/dkorunic/astro-analyzer/releases/latest)
[![Go version](https://img.shields.io/github/go-mod/go-version/dkorunic/astro-analyzer)](go.mod)
[![License: MIT](https://img.shields.io/github/license/dkorunic/astro-analyzer)](LICENSE)

A fast, local command-line tool that measures the quality of astrophotography
subframes (FITS, fpack `.fz`, XISF, TIFF and camera RAW): star count, FWHM,
ASTAP's HFD, N.I.N.A.'s HFR, eccentricity, SNR, sky background and
noise, gradient, hot pixels and PixInsight's PSF SNR, PSF Signal Weight
and SNRW frame weights. It flags bad frames, shows how a session evolved over the
night (and warns of focus drift with temperature), sums integration time
per filter and night with the Moon, twilight and the sky brightness
(mag/arcsec² and Bortle class, from the frames' own noise), checks the
session's bias, dark and flat frames against the lights (settings, banding,
dark pedestal and hot pixels, dust, photon-transfer gain, master noise),
maps how star shapes vary across the field to diagnose tilt, coma and
tracking errors, and shows how much of a rotating stack keeps full depth.
It also recommends PixInsight DrizzleIntegration settings (Scale and Drop
shrink) from the measured sampling and dithering, predicts what
PixInsight's WBPP will decide for the session, compares sub lengths by
read-noise cost and field-rotation smear, and turns the session into
N.I.N.A. sequencer settings: focuser temperature compensation, autofocus
triggers, dither cadence and re-centring on drift, with checks of the
cooler, dew and cloud from the headers.

Nothing is uploaded, converted or sent anywhere: frames are read, measured
and reported locally. The measurement conventions follow PixInsight
(SubframeSelector, the FWHMEccentricity script and the AperturePhotometry
script), so the numbers are comparable to what you see there.

## Install

Download a prebuilt binary for Linux, macOS or Windows from the
[Releases](https://github.com/dkorunic/astro-analyzer/releases) page.

Or with Go:

```sh
go install github.com/dkorunic/astro-analyzer@latest
```

On macOS or Linux, Homebrew users can install it from the tap:

```sh
brew tap dkorunic/tap
brew install astro-analyzer
```

On macOS the tap also has it as a cask:

```sh
brew install --cask dkorunic/tap/astro-analyzer
```

Or from a checkout, using [Task](https://taskfile.dev/):

```sh
task build   # formats, then builds a static, PGO-optimised binary (profile: default.pgo)
task pgo     # regenerates default.pgo from the full-frame pipeline benchmark
task test
task lint    # golangci-lint
```

Plain `go build .` works too. It needs Go 1.27+.

## Usage

```sh
astro-analyzer [flags] file|dir...
```

Pass files, directories (searched recursively; unreadable subdirectories
are skipped with a warning, hidden files such as macOS `._*` AppleDouble
files are ignored, and a file reached twice is measured once), or both.
Only light subs are measured: bias, dark, flat and dark-flat frames
(`IMAGETYP` in its usual spellings, or `EXPTIME` 0), stacks (`Master Light`,
or `NCOMBINE`/`NSTACK`/`STACKCNT` above 1) and processed XISF images (no
FITS keywords at all) are skipped and counted on stderr.

| Format | Extensions | Notes |
|---|---|---|
| FITS | `.fits`, `.fit`, `.fts` | The first image HDU |
| fpack | `.fz` (`.fits.fz`) | Tile-compressed FITS: `RICE_1`, `GZIP_1`, `GZIP_2` and `NOCOMPRESS`, quantized floats restored with their subtractive dithering. Not `HCOMPRESS_1` or `PLIO_1` |
| XISF | `.xisf` | PixInsight's format, uncompressed or `zlib`, `lz4`, `lz4hc` or `zstd`, with or without byte shuffling. Its FITS keywords are the header; a 2×2 `ColorFilterArray` gives `BAYERPAT`. Rows are read top first, as PixInsight reads FITS |
| TIFF | `.tif`, `.tiff` | 8- and 16-bit grey or RGB (no float samples). A FITS header in `ImageDescription`, as ASTAP saves one, is read. Only when named: directory searches skip TIFF, as in a session folder it is usually a processed export |
| Camera RAW | `.cr2`, `.nef`, `.arw`, `.raf`, `.orf`, `.rw2`, `.pef`, `.dng` and more | Through `dcraw` on the `PATH`, as ASTAP does: the undemosaiced mosaic (`-D -4 -t 0`) and its timestamp, camera, ISO, exposure and CFA pattern. dcraw 9.28 does not read Canon CR3. DSLR frames carry no frame type, so calibration frames read as lights |

PixInsight's master bias, dark and flat (`IMAGETYP = 'Master …'`, as WBPP
writes into its `master` folder) count as calibration frames, their [0, 1]
samples put back on the 16-bit scale. When the session also holds the raw
calibration files, the masters are only listed: WBPP built them from those
files, and counting both would count them twice. Celestron Origin labels some flats `LIGHT`,
so on Origin frames a file name starting with bias, dark or flat decides.
Frames are measured in parallel on up to 6 CPU cores (about 100 MB each;
`GOMAXPROCS` lowers it; `GOGC` defaults to 50); on an interactive terminal
a spinner shows the directory scan and then the frames measured so far.

| Flag | Default | Meaning |
|---|---|---|
| `-psf` | `moffat4` | PSF model: `moffat4` (PixInsight's default), `moffat` (free β, ~8× slower) or `gaussian` |
| `-sigma` | `5` | Star detection threshold, in noise sigmas above the local background |
| `-gain` | from `EGAIN` | Camera gain in e⁻/ADU. Without it the photon-noise term is left out of SNR, as PixInsight does |
| `-read-noise` | camera preset | Read noise in e⁻ for the sub-length comparison. Without it, read from the session's bias frames, else a preset: Celestron Origin and Origin Mark II, 26 ZWO cameras (the Seestar S50 writes its ASI462MC) and the Player One Neptune-C II at their deep-sky gain (`GAIN` header) |
| `-reject-sigma` | `3` | Flag a frame when it is worse than the session median by this many robust sigmas… |
| `-reject-min` | `0.1` | …and by at least this fraction of the median, so stable sessions are not over-flagged |
| `-best` | `100` | Also flag frames outside the best this percent per filter by wFWHM, as Siril's `stack -filter-wfwhm=N%` keeps them (e.g. `-best=90`) |
| `-reject` | | Print only the paths of flagged frames, one per line |
| `-csv` | | Write CSV instead of the table |
| `-json` | | Write JSON instead of the table |
| `-version` | | Print version and build information |

Move flagged frames aside (paths may contain spaces):

```sh
astro-analyzer -reject ~/astro/M27 | xargs -I{} mv {} ~/astro/M27/rejected/
```

## Output

One row per frame, sorted by `DATE-OBS`:

```
File            Date                 Filter  Exp   Temp  Alt   Moon alt  Sun alt  Stars  FWHM px  wFWHM  FWHM "  HFD px  HFR px  Ecc   Resid   SNR  PSF SNR   PSFSW     SNRW   Background  StdDev  Noise   Gradient  Sky R   Sky G   Sky B   Sky mag  Hot     Trails  Flags
Light0001.fits  2026-10-13T20:06:10  Nebula  15.0  25.9  62.1  -10       -20      4120   4.15     4.88   5.10    5.74    3.20    0.75  0.0047  3.9  0.000166  0.000375  0.631  6518.8      297.11  299.86  85.6      5775.0  6575.0  6975.0  19.72    7436.3  0       
Light0004.fits  2026-10-13T20:06:57  Nebula  15.0  25.7  62.2  -10       -20      3817   7.32     9.60   9.01    5.97    3.62    0.94  0.0058  3.7  0.000292  0.000188  0.633  6512.5      296.86  298.63  89.4      5775.0  6575.0  6975.0  19.72    7427.5  0       Resid
```

Then the equipment and integration time per filter, with one row per
observing night (noon to noon, local solar time): its median FWHM,
background, sky brightness (Sky mag, with the Bortle class it gives at the
zenith) and sky gradient (with an arrow toward the brighter side, top =
last FITS row), how much of the Moon is lit, the share of frames with the Moon
up, its median distance from the target, and frames taken in twilight (Sun
above −18°). Nights differ in seeing and moonlight, which a session median
hides. For colour cameras the equipment line shows the `BAYERPAT` CFA
pattern. If the two green sites only agree when it is read from the top row
instead of the first stored one, it says so and gives the pattern from the
first stored row. PixInsight reads files without `ROWORDER` from the first
stored row, so its CFA pattern Auto would be wrong there: set that pattern
explicitly in WBPP or Debayer.

```
Bubble Nebula · RASA6F2 152 mm f/2.2 · Origin678C-643c1983c23be8af7 (IMX678) · CFA GBRG · 1.23"/px, field 79′ × 45′

Integration: 1340 frames × 15 s = 5 h 35 min, 5 h 27 min unflagged, over 2 nights
  Night       Frames  Time         FWHM px  Background  Sky mag  Bortle  Gradient  Moon lit  Moon up  Moon dist  Twilight
  2026-10-13  658     20:06–00:06  5.43     6165.6      20.08    6       74.9 ←    10%       0%       124°       0
  2026-10-16  682     20:11–00:20  4.76     6262.5      20.04    6       80.1 ←    33%       24%      108°       0
```

Below the equipment line, two checks print when the headers allow them. A
cooler's setpoint (`SET-TEMP`) is compared with `CCD-TEMP`: frames over
1 °C off, as a cooler at full power on a warm night leaves them, no longer
match darks taken at the setpoint. And with a weather station (`AMBTEMP`,
plus `DEWPOINT` or `HUMIDITY`, from which the Magnus formula gives the dew
point), frames taken with the air within 2 °C of its dew point are counted,
with how many were flagged: optics radiating to a clear sky run colder than
the air, so that is when dew forms.

When the frames carry a sky brightness from the capture software (`SQM`,
which ASTAP measures photometrically and N.I.N.A. reads from a meter, or
`MPSAS`), an `SQM header` column after Bortle shows its median, to check
Sky mag against.

When the directories hold calibration frames, their headers are checked
against the lights': gain or ISO for bias and darks, dark exposure and a
sensor temperature 5 °C or more away, the flat's filter, and any frame
size the lights lack. Dark flats are held to the flats' gain, exposure and
temperature instead (as ASTAP checks them).
Files sharing one `DATE-OBS` are copies of one capture, as the Origin saves
its masters. Then what their pixels show:

- **Bias**: row and column banding, against the pixel noise. In a master it
  is fixed, so subtracting the master removes it.
- **Dark**: a dark whose median level sits below the bias's at the same gain
  was recorded with another offset, so dark − bias (which scaling or
  optimising a dark needs) is negative, as on every Celestron Origin
  session checked; above it, the difference gives the dark current. Its hot
  pixels are measured exactly as the lights' (the Hot column) and compared,
  also scaled to the lights' exposure: above 1 the dark subtracts more than
  the lights hold and leaves dark pits, below 1 hot pixels remain. Hot
  pixels follow sensor temperature, so a warmer dark reads higher.
- **Flat**: level as a share of the sample range, clipped pixels, and dust
  shadows (16-pixel blocks over 1% darker than their surroundings).
- **Gain**: photon transfer on three pairs of neighbouring lights: half the
  variance of their difference is one frame's temporal noise, and gain =
  (signal − pedestal) / variance. The pedestal is the bias level, or the
  dark's when it sits below the bias (Origin); the dark level holds dark
  current, so that gain is a lower bound. It flags a gain in use more than
  15% off (more than 2× above a lower bound), the way the original Origin's
  `EGAIN` is 4.6× too high.
- **Masters**: a master of N frames carries read noise/√N of its own, the
  same in every sub, so more subs cannot average it away; only dithering,
  or an alt-az mount's field rotation, moves it onto different sky. It is
  compared with the stack's own noise. With plate solutions the dithers
  (see Pointing) are counted: over k positions the master averages to
  1/√k of itself, k being at most the runs plus the dithers, so the
  figure stays a lower bound.

```
Calibration frames against the lights (20 s, gain/ISO 200, 15.6 °C, filter Nebula)
  Type  Files                          Taken       Exp s   Gain/ISO  Temp °C  Filter  Issues
  BIAS  5, 1 distinct, 9-frame master  2025-10-17  0.001   200       30.6     -       -
  DARK  5, 1 distinct, 9-frame master  2025-10-17  30      200       30.4     -       none at 20 s, so scaled by exposure; 14.8 °C warmer
  FLAT  1, 30-frame master             2025-10-23  0.0589  23        27.1     Clear   none for filter Nebula
  Bias     banding 1.7 ADU between rows, 1.2 between columns (6% of the 30 ADU pixel noise)
  Dark     123 ADU below the bias, so dark − bias (to scale or optimise it) is negative; hot pixels 1.50× the lights', 1.00× scaled to 20 s
  Flat     level 58% of range, no dust shadows
  Gain     ≥ 0.0164 e⁻/ADU by photon transfer on 3 pairs of neighbouring lights (pedestal from the dark: its dark current counts as noise); the gain in use, 0.0165, is consistent
  Masters  the 9-frame dark adds ≥ 0.5 e⁻ per pixel, the same in every sub, to the 225-sub stack's 0.71 e⁻: dithered over ≤ 81 positions, it averages to ≥ 0.056 e⁻, stack noise ≥ +0.3%
```

Then a session summary per filter, naming the best unflagged frame by wFWHM
as the registration reference (as Siril picks one), with a trend line per
metric in time order. When FWHM follows sensor temperature (rank correlation
beyond ±0.5 over at least 1 °C and 8 frames, after dividing out the seeing's
airmass^0.6), a line suggests focus drift. With a focuser temperature
(`FOCTEMP`, `FOCUSTEM`) that is used instead, as the tube's. With a focuser
position (`FOCUSPOS`, `FOCPOS`) that changes during the session, another line
gives how much FWHM grows between moves: the median, over runs of at least
6 frames at one position, of the last 3 frames' FWHM against the first 3.
Growth means focus drifts faster than the autofocus runs. With both, the
summary turns the session into N.I.N.A. sequencer settings:

- **Move Focuser By Temperature**: the focuser position against its
  temperature at the start of each run (where the autofocus left it), as a
  least-absolute-deviation line, so one failed autofocus cannot tilt it.
  Its slope and intercept are the instruction's Slope and Intercept
  (position = Slope·T + Intercept). Needs 3 positions over 1 °C; when the
  rank correlation is under 0.5 it says temperature compensation would not
  help.
- **Autofocus After Temperature Change**: FWHM growth per °C within runs
  whose focuser temperature moved at least 0.5 °C, and the step that grows
  FWHM 5% (N.I.N.A.'s default HFR trigger; its own default is 5 °C).
- **Autofocus After HFR Increase**: N.I.N.A.'s trigger replayed at its
  defaults (5% over a 10-frame least-squares trend, against the lowest HFR
  since the autofocus) on the HFR column of each run, with how many runs it
  would have cut short and after how many frames.

A cloud sensor's `CLOUDCVR` (or `SKYTEMP` over `AMBTEMP`, which a clear sky
keeps far below) that rises as the star count falls (rank correlation under
−0.5) gets a line saying low star counts are cloud.


```
Session: 1340 frames, 31 flagged
Reference frame for registration: Light1135.fits (best wFWHM 3.46 px, 4445 stars)

Metric      Median    σ (MAD)  Min       Max      Trend
Stars       3997      458.1    3196      4521     ▅▄▃▂▂▂▂▂▂▃▃▃▄▄▅▅▆▆▆▇▇▇▇██████▅▂▂▁▁▁▁▂▂▂▃▃▅▅▅▆▆▆▇▇▇████▇▆▇▇▆▇
FWHM "      6.25      1.293    4.12      11.45    ▅▇███▇▆▆▆▆▅▅▄▄▄▄▄▄▃▃▃▃▃▂▂▂▃▃▃▃▅▅▅▅▅▅▄▄▄▃▃▃▂▂▂▃▂▁▂▁▁▁▁▂▂▄▃▃▄▃
HFD px      6.34      0.861    5.07      9.69     ▅▇███▇▇▆▇▆▆▆▅▄▄▄▄▄▄▃▂▂▂▂▂▂▂▃▃▄▆▅▆▅▆▅▅▅▅▃▃▃▃▃▃▃▂▁▁▁▁▁▁▂▂▃▃▃▄▂
HFR px      3.37      0.148    2.99      3.84     ▇▇████▇▇▇▇▆▇▆▅▆▆▅▆▅▄▃▄▄▃▃▄▄▅▄▅▆▆▆▆▆▆▆▅▅▄▃▄▄▃▃▄▃▁▂▁▁▂▂▂▄▆▅▄▆▄
PSF SNR     0.000411  0.00012  0.000166  0.00115  ▁▂▃▃▄▃▃▃▃▃▃▅▅▄▃▃▄▅▄▄▄▆▆▆▅▅▆█▇▄▁▂▂▂▂▂▂▂▂▂▂▂▁▂▂▃▂▁▃▃▃▃▃▃▄▇█▄▆▄
PSFSW       0.000579  0.00036  0.000156  0.00112  ▁▁▁▁▁▁▂▂▂▂▂▃▃▃▄▄▅▅▅▅▆▇▇█▇▇▇██▄▁▂▂▂▂▂▂▂▃▃▃▄▄▄▅▅▅▅▆▇██▇▆▆▆▇▆▅▆
Background  6225.0    222.39   5968.8    6800.0   ▆▆▆▆▆▅▅▄▄▄▄▄▃▃▃▂▂▂▂▂▂▂▂▁▁▁▁▁▁▅██▇▇▇▇▇▇▆▆▅▄▄▄▃▃▃▃▃▃▂▂▂▂▂▂▂▂▂▂
```

And a field map: median FWHM, eccentricity and the dominant elongation axis
in each ninth of the frame, over the whole session (one map per filter):

```
Field: median FWHM px, eccentricity and elongation axis (top = last FITS row)
        left          centre        right
top     4.07  0.85 │  5.01  0.69 │  6.86  0.60 ╱
middle  4.48  0.78 │  5.75  0.60 ·  7.98  0.59 ╱
bottom  5.24  0.73 ╲  6.68  0.59 ─  9.35  0.63 ╱
FWHM tilt 92% extreme (corners 4.07–9.35 px), curvature +5% (corners vs centre), right vs left +78%, top vs bottom -25%
```

Across a meridian flip each pier side gets its own map, labelled by
`PIERSIDE` when the header has it (SGP, ASIAIR; N.I.N.A. does not write
it), else before and after the flip: the optics turn with the tube, and
mirror flop or a loose focuser can change the tilt.

Reading the field map:

- **Tilt**: one side or corner is consistently wider (above: the bottom right).
  The tilt figure is the best-to-worst corner spread relative to the median
  FWHM, classed as ASTAP does (none under 5%, then almost none, mild,
  moderate, severe from 20% and extreme from 30%); curvature cannot show
  it, as opposite corners cancel there.
- **Coma or field curvature**: all corners worse than the centre, with elongation pointing radially.
- **Tracking, guiding or wind**: every cell elongated in the same direction.

Then how much of the stack gets every frame. Alt-az mounts rotate the field
through the night, and drifting pointing shifts it, so after registration
only the middle of the reference frame has full depth; crop to it. Plate
solutions (`ASTRSOLV` not false) of the unflagged frames give the answer:

```
Stack coverage: 1305 plate-solved frames, field rotates 132°
  every frame over 42% of the reference frame, 90% of frames over 47%, half over 71%
```

Then how the mount held the target, from consecutive plate solutions. A
step that lands at least 1.5 px off the median step of the 5 either side
(the drift) is a dither; none means hot pixels and pattern noise stay on
one sky position. Drift is the frame centre's least-squares rate per run
(runs split at pauses over 10 min and at meridian flips), with whether
N.I.N.A.'s Center After Drift would re-centre at its default 10′. A
meridian flip is a `PIERSIDE` change or, on an equatorial mount, a 180°
jump in field rotation; the Celestron Origin dithers every frame:

```
Pointing: 1336 plate-solved frames in 2 runs
  Dither  every frame, 1095 dithers of 7.9 px (median), at most 5 frames without one
  Drift   0.3′/h (median of 2 runs), at most 2.3′ from a run's first frame; N.I.N.A. Center After Drift at 10′ (default) would not fire
```

Finally, a recommendation for PixInsight's DrizzleIntegration, based on the
frames that were not flagged:

```
Drizzle: 1309 unflagged frames, dither measured from plate solutions: stars move up to 2480 px, field rotates 132° (4 unsolved frames left out)
  Sampling        median FWHM 5.04 px, well sampled (drizzle adds resolution below 2.5 px)
  Recommendation  CFA drizzle, Scale 1, Drop shrink 1.00
                  recovers full colour resolution without debayer interpolation
  Expected        weights image rms 11% of median (keep under 20%), noise correlation R 1.50
```

### Drizzle recommendation

- **Whether to drizzle**: it needs more than 15 frames (PixInsight's WBPP
  warns below that), three times as many for colour cameras (PixInsight:
  Bayer drizzle needs three to four times the frames of monochrome), and
  they must actually have moved between exposures. Dither is measured from each frame's plate solution (`CRVAL`,
  `CRPIX`, and `CD`, `CDELT`/`PC` or `CDELT`/`CROTA2`). Rotation counts, so alt-az field
  rotation is recognised. Frames whose WCS is only the mount's pointing
  (`ASTRSOLV = F`) are left out. With fewer than 15 plate-solved frames,
  random dithering is assumed and the report says so.
- **Scale**: stars narrower than 2.5 px FWHM are undersampled (Fruchter &
  Hook 2002: a well-sampled detector has ≳ 2.5 px across the FWHM). Scale is
  the smallest, at most 3, at which the drizzled star spans 2.5 output
  pixels. The measured FWHM is the optics alone, but drizzle still blurs it
  with the detector pixel, the drop and the output pixel; these add in
  quadrature, so stars around 1 px get Scale 2, not 3. Well-sampled
  monochrome data needs no drizzle.
  Well-sampled one-shot-colour data gets CFA drizzle at Scale 1 and Drop
  shrink 1, which PixInsight recommends for colour cameras.
- **Drop shrink** is the smallest value that still gives an even weights
  image. The tool simulates drizzle with your frames' measured sub-pixel
  positions at five points across the field. It accepts a weights rms below
  20% of the median (the DrizzlePac Handbook rule) and starts no lower than
  0.5 or 1.2× the output pixel, whichever is larger (Fruchter & Hook 2002:
  drops "slightly greater than" output pixels, typically p = 0.6 at s = 0.5). For colour cameras each colour is
  checked separately, since one colour fills only a quarter of the pixels.
- **Noise correlation R** (Fruchter & Hook eq. 9–10) is how much
  pixel-to-pixel noise in the drizzled image understates the true noise.
- After integrating, check PixInsight's weights image: it should look
  uniform apart from the edges and, for colour data, the CFA pattern.

### WBPP recommendation

After the drizzle advice, a `WBPP` section per filter predicts the choices
PixInsight's WeightedBatchPreprocessing makes for the unflagged frames, so
you can see them before a multi-hour run:

- **Exposure tolerance**: WBPP integrates lights whose exposures differ by
  more than 2 s as separate masters (`BPP-FrameGroup.js:1230`). Mixed
  exposures print the tolerance that keeps them in one master.
- **Fast integration**: at 150 frames or more WBPP switches to
  FastIntegration (`BPP-FrameGroup.js:344`), which has no minimum weight,
  rejection algorithm or local normalization.
- **Autocrop**: WBPP crops each master to the largest rectangle whose low
  rejection map is at most 0.25 (`BPP-Processing.js:250`). Registration
  fills pixels outside a frame with 0, which are rejected low, so that is
  the largest rectangle that 75% of the frames overlap, found from the plate
  solutions. On a rotating alt-az stack it is often a third of the frame.
- **Local normalization**: PixInsight calls it crucial when gradients change
  over time. Each frame's sky gradient is carried onto the reference frame
  through the plate solutions, since a light dome fixed to the horizon turns
  with an alt-az field even on a steady night. If the gradients differ
  between frames by more than the stack's noise (frame noise / √N), the
  difference survives integration and LN should stay on; below that it can
  be turned off to save time. Under fast integration WBPP skips LN
  altogether, and the report says so.
- **Weighting**: the stack SNR, as a share of the best any weighting can
  give (inverse-variance weights, from each frame's PSF SNR), and an
  approximate stack FWHM for each weighting WBPP offers on that path: PSF
  Signal Weight (the default) and PSF SNR, or, under fast integration, none
  (its default), Resolution and PSF SNR. Weighting that favours sharp
  frames costs SNR, so when the methods disagree the report names the one
  for depth and the one for detail. Under fast integration it recommends
  turning weighting on when it gains more than one more frame would.
- **Minimum weight**: WBPP drops frames whose weight is under 5% of the
  best in the group (`BPP-Operations.js:1611`). With the default PSF Signal
  Weight, the frames whose PSFSW falls that low are listed.
- **Rejection**: Auto picks percentile clipping up to 6 frames, Winsorized
  sigma clipping up to 14 and Generalized ESD above
  (`BPP-FrameGroup.js:1411`), counted after the minimum-weight drops. This
  is the rule since WBPP 3.1.1; earlier versions use Linear Fit Clipping
  above 15 frames. WBPP's comment on the change: at its default thresholds
  linear fit rejected 18.5× more good samples than GESD on a 183-frame set,
  and its aggressiveness grows with the number of frames, while GESD's
  significance level means the same at any count. WBPP no longer offers
  sigma clipping or averaged sigma clipping; Robust Chauvenet Rejection
  (15+ frames) is available but never picked automatically.

```
WBPP: 1309 unflagged frames
  Integration     fast integration, which WBPP enables at 150+ frames: no minimum weight or rejection algorithm to set
  Autocrop        keeps about 1687 × 1635 px, 33% of the reference frame (where 75%+ of frames overlap)
  Normalization   gradients differ between frames by 54.0 ADU across the field (median), 7.7× the stack's noise;
                  WBPP skips local normalization under fast integration: turn that off to apply it, or remove the gradient from the master
  Weighting       none (default)  SNR  96.3% of best, stack FWHM ≈ 5.34 px
                  Resolution      SNR  92.8%, ≈ 4.99 px
                  PSF SNR         SNR 100.0%, ≈ 5.32 px
  Recommendation  enable weighting: up to +3.9% SNR, worth 7.9% more exposure, at almost no cost
                  PSF SNR for depth, Resolution for detail
```

### Sub-length comparison

Last, a `Sub length` section per filter asks whether shorter (or longer)
subs would do better, for the same total time:

- **Read noise** is paid once per sub, shot noise per second. Each raw
  pixel's noise in electrons (same-colour neighbours two apart in a row,
  differenced, so gradients and nebulae cancel) gives the shot-noise rate
  B = (σ² − RN²)/t, and a sub of t seconds keeps √(B / (B + RN²/t)) of
  the SNR. Read noise comes from `-read-noise`, else the session's bias
  frames at the lights' gain setting, else a preset for the camera at that
  setting. Two separate bias frames give it exactly: the noise of their
  difference over √2, which cancels the sensor's fixed pattern. A master
  of N frames (`NCOMBINE`/`NSTACK`) only bounds it, at √N times its pixel
  noise, pattern included. The Origin Mark II's preset,
  0.97 e⁻ at ISO 1000, is the higher of two sessions' upper bounds from
  their 9-frame bias masters (0.93 and 0.97 e⁻; astro-recommender uses a
  vendor sheet's 2.15 e⁻); the original Origin's, 1.5 e⁻ at ISO 200, comes
  the same way. The rule of thumb is
  that 5% is an acceptable cost (Robin Glover, 2019). With `CCD-TEMP`
  it also estimates the dark current's part: the chip's at 25 °C from its
  maker's chart (0.16 e⁻/s, the charted chips' median, where none is
  published, as for the IMX678), doubling every 5 °C. A camera without a
  preset (QHY, ToupTek, another Player One...) gets its chip from its
  pixels: the closest pitch (`XPIXSZ`) and size (`NAXIS`, either
  orientation, at any binning) within 2% among the chips of its version
  (colour or mono), none when two match equally (IMX290 and IMX462). It
  gives the chip's QE and dark current, not its read noise: set
  `-read-noise` or add bias frames. The overview shows the chip after the
  camera, "by its pixels" when matched this way.
- **Field rotation** of an alt-az mount turns the field during each sub,
  smearing corner stars tangentially by r·ω·t. The rate ω comes from
  consecutive plate solutions; equatorial mounts show none.

```
Sub length: 15 s subs
  Noise           5.5 e⁻ per pixel, read noise 0.92 e⁻ (≤, from this session's 9-frame bias master)
                  shot noise 2 e⁻/s (sky, target, dark): read noise costs ≤ 5% SNR from 4 s
                  dark current ≈ 0.06 e⁻/s at 18 °C (a typical chip's, IMX678 uncharted, doubling every 5 °C)
  Field rotation  0.0076°/s median, 0.0121°/s p90; corner stars smear tangentially (median FWHM 5.08 px)
  Sub      SNR vs no read noise  Corner smear median / p90
  5 s      95.9%                 1.5 / 2.3 px
  10 s     97.9%                 2.9 / 4.7 px
  15 s     98.6%                 4.4 / 7.0 px              ← now
  20 s     98.9%                 5.9 / 9.4 px
  30 s     99.3%                 8.8 / 14.1 px
  60 s     99.6%                 17.6 / 28.2 px
```

### Columns

| Column | Meaning |
|---|---|
| Exp, Temp, Alt | `EXPTIME`, `CCD-TEMP`, and target altitude from `CENTALT`/`OBJCTALT`, else derived from `AIRMASS`. `ALTITUDE` is ignored: it is the site's elevation |
| Moon alt, Sun alt | Altitudes in degrees at the exposure start, from `DATE-OBS` and the site (`SITELAT`/`SITELONG`, `LATITUDE`/`LONGITUD` or `OBSGEO-B`/`OBSGEO-L`). `DATE-OBS` is UTC unless the header has `TIMEZONE` (Celestron Origin writes local time). The Moon is topocentric; positions are accurate to ~0.3° |
| Stars | Stars detected above `-sigma` × noise over the local background |
| FWHM px, FWHM " | Median FWHM of fitted stars, in original sensor pixels and arcseconds. Arcseconds use the plate solve's scale, else `XPIXSZ` and `FOCALLEN`, else `CDELT2` |
| wFWHM | Siril's weighted FWHM, as its default global registration computes it: FWHM·(1 + 2·(N_ref − N)/N_ref), with N the frame's stars and N_ref the most of any frame through the same filter. A frame that lost stars to haze or cloud ranks worse than its FWHM alone says; `-best` ranks by it |
| HFD px | ASTAP's half-flux diameter in original pixels, the median over the stars ASTAP's analysis accepts (HFD 0.8–30 px, its SNR over 10): twice the flux-weighted mean distance from the star's centroid, summed out to where its pixels fall below 3σ (Miyashita's method, ASTAP's `HFD`). It needs no PSF model, so it holds for defocused doughnut stars, and it is the figure ASTAP and SGP show. It reads above FWHM: by 7–15% on a Gaussian star of 3–6 px, as ASTAP's bilinear sampling and the pixel's width add to it, and more on stars with Moffat wings |
| HFR px | N.I.N.A.'s half-flux radius in original pixels, so its image history and Autofocus After HFR Increase trigger can be compared: a centroid weighted by a Gaussian window (σ half the measurement radius, as SExtractor's windowed positions), then the radius at which the enclosed flux, pixel by pixel outward, reaches half of the flux within 1.5× the star's radius. Only stars N.I.N.A. would keep count: a mean over the sky of at least the noise and enough pixels 1.5σ over it, as fainter ones measure their measurement circle, not the star. Unlike the other columns it is a **mean**, over stars whose radius lies within 1.5σ of the mean radius, as N.I.N.A. takes it (`StarDetection.cs`). Each pixel counts at its centre's distance, so it reads low on undersampled stars (9% at FWHM 4 px); with Moffat wings it sits near 0.6× FWHM. It follows FWHM (rank correlation 0.94 over the Bubble session below) over a narrower range, 2.99–3.84 px against FWHM's 3.35–9.30, as N.I.N.A.'s curve of growth on flux clipped at zero does. The star's radius here is the aperture radius, not N.I.N.A.'s edge-detected blob, so expect a few per cent difference from N.I.N.A.'s own figure |
| Ecc | Median eccentricity: 0 is round, above ~0.5 is visibly elongated |
| Resid | Median PSF fit residual, as PixInsight's StarResidual: the Winsorized mean \|data − model\| as a fraction of the full sample range (65535 for 16-bit data); high values mean doubled or distorted stars |
| SNR | Median per-star aperture SNR. Not PixInsight's SNR, which is SNRW |
| PSF SNR | PixInsight's PSF SNR: 1.316·10⁻⁷·S²/(4.987·10⁶·σ²), with S the fitted stars' total signal and σ the Noise. It drops with haze, cloud and fewer stars. Saturated and SNR < 20 stars are not fitted, so compare it within a session, not with PixInsight's figure |
| PSFSW | PixInsight's PSF Signal Weight, WBPP's default frame weight: 5.326·10⁻⁶·S·M/(9·10⁶·σ·M*), with M the sum of each star's signal per pixel of its tenth-maximum ellipse (after PixInsight's robust Chauvenet rejection) and M* the median depth of sky pixels below the large-scale background. It rewards sharp, bright stars on a quiet sky. Same caveat as PSF SNR, and M* comes from 256 px block medians where PixInsight uses a multiscale median transform |
| SNRW | PixInsight's SNR (SubframeSelector's, the SNR weight of WBPP's presets): (two-sided background scale / Noise)², the scale being the standard deviation each side of the sky median after two 4σ clips. Pure noise gives 0.28; gradients and nebulosity raise it, as they widen the sky's spread beyond the pixel noise |
| Background, StdDev | Sigma-clipped sky median and standard deviation |
| Noise | Wavelet (multiresolution support) noise estimate, which nebulosity and gradients do not inflate. Matches PixInsight's, including its 2% bias correction |
| Gradient | Range in ADU across the frame of a plane fitted to the sky (median of a 16-wide grid of cells). Cells well above the plane, mostly nebula, are left out; `-` when nebula fills most of the frame. Moonlight, light domes and twilight raise it |
| Sky R, Sky G, Sky B | Colour cameras only: sigma-clipped sky level in the raw red, green and blue pixels, before 2×2 binning mixes them. Moonlight and twilight raise blue most, sodium light pollution red. Which row `BAYERPAT` starts from differs between programs, so the loader takes the reading under which the two green pixels agree |
| Hot | Hot pixel level: median excess over background, in ADU, of the 500th to 1500th brightest single-pixel maxima. It follows sensor temperature (dark current), so compare it between frames, not cameras. Not a count, which would follow the sky noise |
| Sky mag | Sky brightness in V mag/arcsec² implied by the photon noise: each raw pixel's noise less read noise and the chip's dark current is the sky's shot noise, which the camera preset's peak QE and colour response (astro-recommender's, from the makers' charts), the aperture (`APTDIA`), central obstruction and throughput (0.81 for the Origins' RASA, else 0.85) and the plate scale turn into photons per arcsec², taking the sky as flat over 400–700 nm and a V = 0 source as 1005 photons s⁻¹ cm⁻² Å⁻¹ (Bessell 1998). It counts target light as sky and is good to about ±0.3 mag: compare nights, not an SQM. Needs a camera preset or a chip matched by its pixels, gain and read noise. The integration table's Bortle class moves it to the zenith (Krisciunas & Schaefer 1991, k = 0.2) and uses DarkSkySites' bands; moonlight counts |
| Trails | Satellite, aircraft and meteor trails: in the frame binned 4×4, blobs over a 16×16-bin median sky by 3× the noise that are at least a tenth of the shorter side long and 8× longer than wide (second moments); segments on one line count once. A blob that is not thin, such as trails crossing, is split by a Hough transform into up to 4 lines, each of which must pass the same test and which together must hold 80% of the blob, so a star halo or nebula yields none. After ASTAP's (length² / area > 3), but with moments a star or a blob of stars never passes. It finds trails twice the pixel noise or brighter; close parallel trails (a satellite train) can merge into one. A frame with more trails than its filter's median is flagged: WBPP's fast integration (150+ frames) has no rejection to remove them |
| Flags | Metrics where the frame is an outlier among its filter's frames: high FWHM, Ecc, Resid, Background, Gradient or Trails, or low Stars, SNR, PSF SNR, PSFSW or SNRW |

Unknown values print as `-`, are empty in CSV and are omitted keys in JSON.

`-csv` and `-json` write one record per frame with only these per-frame
metrics, not the session sections. Keys: `file`, `date`, `filter`,
`exptime`, `ccd_temp`, `altitude`, `moon_alt`, `sun_alt`, `stars`,
`fwhm_px`, `wfwhm`, `fwhm_arcsec`, `hfd_px`, `hfr_px`, `eccentricity`, `psf_residual`, `snr`,
`psf_snr`, `psf_signal_weight`, `snr_weight`, `background`, `stddev`,
`noise_mrs`, `gradient`, `sky_r`, `sky_g`, `sky_b`, `sky_mag`, `hot_pixel_level`,
`trails` and `flags`.

## How it measures

- **Loading**: BZERO/BSCALE are applied (other formats are read into the
  same header and samples: see Usage). One-shot-colour frames (`BAYERPAT`)
  are averaged into 2×2 superpixels so the Bayer grid does not fragment
  stars, and FWHM is reported back in original pixels. RGB cubes are averaged.
  Integer pixels equal to `BLANK`, NaN float pixels, and zero pixels
  connected to the frame edge (the border registration leaves) are treated
  as missing: they count as sky and are left out of every statistic.
- **Saturation** is detected on the raw pixels before binning. A star whose
  core touches a clipped pixel is excluded. On calibrated frames, where
  clipped pixels no longer share one value, a flat-topped core (five or
  more pixels within 1% of the peak) is excluded instead.
- **Noise and background** use the multiresolution support method (Starck &
  Murtagh), as PixInsight does: an à trous B3-spline wavelet transform over
  four layers, with noise measured only on pixels that are not significant
  at any scale, divided by 0.974 for the method's 2% low bias. As in
  SubframeSelector, it falls back to three or two layers when fewer than 1%
  of pixels are left, and to a k-sigma estimate after that. The large-scale
  layer serves as the local background, so stars on bright nebulae and
  gradients are still found.
- **Detection**: local maxima above the threshold, brightest first, rejecting
  hot pixels, blends and saturated cores.
- **SNR** uses PixInsight's AperturePhotometry error model, with a local sky
  annulus: `SNR = flux / √(n·σ² + n²·σ²/n_sky + flux/gain)`. Gain is per raw
  pixel, so a 2×2 superpixel uses four times the gain. Float data normalised
  to [0, 1] is no longer in ADU, so its `EGAIN` is ignored. The Celestron
  Origin Mark II writes `EGAIN` per ADC count but stores each count as 25
  ADU (every pixel a multiple of 25), so its gain is divided by that step.
  The original Origin's (`Origin178`) `EGAIN` is 4.5× its measured gain
  (0.0742 against 0.0162–0.0165 e⁻/ADU at ISO 200), so it is divided by 4.5.
- **PSF fitting**: stars with SNR ≥ 20 are fitted with an elliptical, rotated
  profile by Levenberg–Marquardt. The model is integrated over each pixel's
  area, so undersampled stars of ~2 px are measured correctly. FWHM is the
  geometric mean of both axes. As in PixInsight's FWHMEccentricity script,
  fits with a residual of 0.1 of the full range or more are rejected; that
  drops only failed fits (SubframeSelector rejects none).
- **Per frame**, FWHM and residual are medians over fitted stars, as in the
  FWHMEccentricity script, and SNR is the median over detected stars.
  SubframeSelector instead averages after Chauvenet rejection, which reads a
  few percent higher on the usual tail of wide stars. Eccentricity is the median over fitted
  stars with SNR ≥ 50: noise alone makes round stars measure ~0.4 at SNR 20,
  so a fainter frame would otherwise look worse tracked. It is `-` when no
  star reaches SNR 50.
- **Outliers** are judged against the median and MAD of the session's frames
  through the same filter, so a run of cloudy frames cannot hide itself by
  inflating the spread, and an L frame is never compared with an Ha one.
  The session summary and drizzle recommendation are also per filter.

## Comparing with Siril

Siril's FWHM is the star's major axis (FWHMx), averaged over unsaturated
stars, and its roundness is FWHMy/FWHMx. This tool follows PixInsight: FWHM
is the geometric mean √(FWHMx·FWHMy), so for elongated stars Siril's figure
is larger. Siril also fits a Gaussian by default, not Moffat4. Convert
eccentricity to Siril's roundness with roundness = √(1 − Ecc²). wFWHM
follows Siril's global registration formula, on this tool's FWHM and with
the session's best star count as reference, so it ranks frames the same way
but its values differ.

Both PixInsight and Siril evaluate the PSF model at pixel centres; this tool
integrates it over each pixel. Their FWHM therefore includes the pixel's own
width: for a true FWHM F it reads about √(F² + 0.46), +6% at 2 px, +2.5% at
3 px and under 1% from 5 px.

## Limitations

- fpack's `HCOMPRESS_1` and `PLIO_1`, float TIFF, and RAW formats dcraw
  9.28 predates (Canon CR3) are not read.
- A directory search also measures the calibrated or registered XISF subs
  WBPP leaves in its output folder, alongside the raw ones: point it at the
  raw frames, or delete WBPP's intermediate folders first.
- Brighter stars measure wider on many real systems; FWHM therefore depends
  somewhat on the SNR ≥ 20 cut-off. Compare frames within a session rather
  than absolute values between different tools.
- PSF SNR and PSFSW follow PixInsight's formulas (`PSFSignalEstimator.h`)
  but sum only the stars this tool fits, on 2×2-binned colour frames, so
  they rank frames as PixInsight does without matching its values.

## License

MIT, see [LICENSE](LICENSE).
