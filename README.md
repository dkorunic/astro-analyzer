# astro-analyzer

[![GitHub release](https://img.shields.io/github/v/release/dkorunic/astro-analyzer)](https://github.com/dkorunic/astro-analyzer/releases/latest)
[![Go version](https://img.shields.io/github/go-mod/go-version/dkorunic/astro-analyzer)](go.mod)
[![License: MIT](https://img.shields.io/github/license/dkorunic/astro-analyzer)](LICENSE)

A fast, local command-line tool that measures the quality of astrophotography
FITS subframes: star count, FWHM, eccentricity, SNR, sky background and
noise. It flags bad frames, shows how a session evolved over the night,
sums integration time per filter and night with the Moon and twilight
conditions, maps how star shapes vary across the field to diagnose tilt,
coma and tracking errors, and shows how much of a rotating stack keeps full
depth. It also recommends PixInsight DrizzleIntegration settings
(Scale and Drop shrink) from the measured sampling and dithering, and
predicts what PixInsight's WBPP will decide for the session.

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
astro-analyzer [flags] file.fits|dir...
```

Pass files, directories (searched recursively for `.fits`, `.fit` and `.fts`;
unreadable subdirectories are skipped with a warning, hidden files such as
macOS `._*` AppleDouble files are ignored, and a file reached twice is
measured once), or both. Only light subs are measured: bias, dark, flat and
dark-flat frames (`IMAGETYP` in its usual spellings, or `EXPTIME` 0) and
stacks (`Master Light`, or `NCOMBINE`/`NSTACK`/`STACKCNT` above 1) are
skipped and counted on stderr. Celestron Origin labels some flats `LIGHT`,
so on Origin frames a file name starting with bias, dark or flat decides. Frames are measured in parallel on all CPU cores; on an
interactive terminal a spinner shows the directory scan and then the frames
measured so far.

| Flag | Default | Meaning |
|---|---|---|
| `-psf` | `moffat4` | PSF model: `moffat4` (PixInsight's default), `moffat` (free β, ~8× slower) or `gaussian` |
| `-sigma` | `5` | Star detection threshold, in noise sigmas above the local background |
| `-gain` | from `EGAIN` | Camera gain in e⁻/ADU. Without it the photon-noise term is left out of SNR, as PixInsight does |
| `-read-noise` | camera preset | Read noise in e⁻ for the sub-length comparison. Presets cover the Celestron Origin Mark II and ZWO cameras at their deep-sky gain (`GAIN` header) |
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
File            Date                 Filter  Exp   Temp  Alt   Moon alt  Sun alt  Stars  FWHM px  wFWHM  FWHM "  Ecc   Resid   SNR  PSF SNR   Background  StdDev  Noise   Gradient  Sky R   Sky G   Sky B   Hot     Flags
Light0001.fits  2026-10-13T20:06:10  Nebula  15.0  25.9  62.1  -10       -20      4136   4.14     4.86   5.10    0.73  0.0047  4.0  0.000186  6518.8      297.11  299.46  85.6      5775.0  6575.0  6975.0  7436.3
Light0004.fits  2026-10-13T20:06:57  Nebula  15.0  25.7  62.2  -10       -20      3823   7.32     9.60   9.01    0.94  0.0056  3.8  0.000307  6512.5      296.86  298.30  89.4      5775.0  6575.0  6975.0  7427.5  Resid
```

Then the equipment and integration time per filter, with one row per
observing night (noon to noon, local solar time): its median FWHM,
background and sky gradient (with an arrow toward the brighter side, top =
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
Bubble Nebula · RASA6F2 152 mm f/2.2 · Origin678C-643c1983c23be8af7 · CFA GBRG · 1.23"/px, field 79′ × 45′

Integration: 1340 frames × 15 s = 5 h 35 min, 5 h 28 min unflagged, over 2 nights
  Night       Frames  Time         FWHM px  Background  Gradient  Moon lit  Moon up  Moon dist  Twilight
  2026-10-13  658     20:06–00:06  5.43     6165.6      74.9 ←    10%       0%       124°       0
  2026-10-16  682     20:11–00:20  4.75     6262.5      80.1 ←    33%       24%      108°       0
```

When the directories hold calibration frames, their headers are checked
against the lights' (no calibration pixels are read): gain or ISO for bias
and darks, dark exposure and a sensor temperature 5 °C or more away, the
flat's filter and frame size. Files sharing one `DATE-OBS` are copies of one
capture, as the Origin saves its masters:

```
Calibration frames against the lights (20 s, gain/ISO 200, 15.6 °C, filter Nebula)
  Type  Files                          Taken       Exp s   Gain/ISO  Temp °C  Filter  Issues
  BIAS  5, 1 distinct, 9-frame master  2025-10-17  0.001   200       30.6     -       -
  DARK  5, 1 distinct, 9-frame master  2025-10-17  30      200       30.4     -       none at 20 s, so scaled by exposure; 14.8 °C warmer
  FLAT  1, 30-frame master             2025-10-23  0.0589  23        27.1     Clear   none for filter Nebula
```

Then a session summary per filter, naming the best unflagged frame by wFWHM
as the registration reference (as Siril picks one), with a trend line per
metric in time order. When FWHM follows sensor temperature (rank correlation
beyond ±0.5 over at least 1 °C and 8 frames, after dividing out the seeing's
airmass^0.6), a line suggests focus drift:

```
Session: 1340 frames, 28 flagged

Metric      Median   σ (MAD)  Min       Max      Trend
Stars       4004     457.4    3203      4531     ▅▄▃▂▂▂▂▂▂▃▃▃▄▄▅▅▆▆▆▇▇▇▇██████▅▂▂▁▁▁▁▁▂▂▃▃▅▅▅▆▆▆▇▇▇████▇▆▇▇▆▇
FWHM "      6.22     1.289    4.14      11.40    ▅▇███▇▆▆▆▆▅▅▄▄▄▄▄▄▃▃▃▃▃▂▂▂▃▃▃▃▅▅▅▅▅▅▄▄▄▃▃▃▂▂▂▂▂▁▂▁▁▁▁▂▂▃▃▃▄▃
PSF SNR     0.00044  0.00013  0.000186  0.00119  ▁▂▃▃▃▃▃▃▃▃▃▅▄▄▃▄▄▅▄▄▄▆▆▆▅▅▆█▇▄▁▂▂▂▂▂▂▂▂▂▂▂▁▂▃▃▂▁▃▃▄▄▃▃▄▇▇▅▆▄
Background  6225.0   222.39   5968.8    6800.0   ▆▆▆▆▆▅▅▄▄▄▄▄▃▃▃▂▂▂▂▂▂▂▂▁▁▁▁▁▁▅██▇▇▇▇▇▇▆▆▅▄▄▄▃▃▃▃▃▃▂▂▂▂▂▂▂▂▂▂
```

And a field map: median FWHM, eccentricity and the dominant elongation axis
in each ninth of the frame, over the whole session (one map per filter):

```
Field: median FWHM px, eccentricity and elongation axis (top = last FITS row)
        left          centre        right
top     4.06  0.85 │  5.01  0.69 │  6.84  0.60 ╱
middle  4.48  0.78 │  5.74  0.60 ·  7.97  0.59 ╱
bottom  5.24  0.73 ╲  6.67  0.59 ─  9.30  0.63 ╱
FWHM tilt 91% (corners 4.06–9.30 px), curvature +5% (corners vs centre), right vs left +78%, top vs bottom -25%
```

Reading the field map:

- **Tilt**: one side or corner is consistently wider (above: the bottom right).
  The tilt figure is the best-to-worst corner spread relative to the median
  FWHM; curvature cannot show it, as opposite corners cancel there.
- **Coma or field curvature**: all corners worse than the centre, with elongation pointing radially.
- **Tracking, guiding or wind**: every cell elongated in the same direction.

Then how much of the stack gets every frame. Alt-az mounts rotate the field
through the night, and drifting pointing shifts it, so after registration
only the middle of the reference frame has full depth; crop to it. Plate
solutions (`ASTRSOLV` not false) of the unflagged frames give the answer:

```
Stack coverage: 1308 plate-solved frames, field rotates 132°
  every frame over 42% of the reference frame, 90% of frames over 47%, half over 71%
```

Finally, a recommendation for PixInsight's DrizzleIntegration, based on the
frames that were not flagged:

```
Drizzle: 1312 unflagged frames, dither measured from plate solutions: stars move up to 2480 px, field rotates 132° (4 unsolved frames left out)
  Sampling        median FWHM 5.03 px, well sampled (drizzle adds resolution below 2.5 px)
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
WBPP: 1312 unflagged frames
  Integration     fast integration, which WBPP enables at 150+ frames: no minimum weight or rejection algorithm to set
  Autocrop        keeps about 1687 × 1635 px, 33% of the reference frame (where 75%+ of frames overlap)
  Normalization   gradients differ between frames by 53.8 ADU across the field (median), 7.7× the stack's noise;
                  WBPP skips local normalization under fast integration: turn that off to apply it, or remove the gradient from the master
  Weighting       none (default)  SNR  96.3% of best, stack FWHM ≈ 5.32 px
                  Resolution      SNR  93.1%, ≈ 4.97 px
                  PSF SNR         SNR 100.0%, ≈ 5.29 px
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
  the SNR. Read noise comes from `-read-noise` or a preset, which applies
  only at the gain setting it was measured at. The Origin Mark II's preset,
  1.1 e⁻ at ISO 1000, is an upper bound from its 9-frame bias master
  (astro-recommender uses a vendor sheet's 2.15 e⁻); the original Origin's,
  1.6 e⁻ at ISO 200, comes the same way. The rule of thumb is
  that 5% is an acceptable cost (Robin Glover, 2019).
- **Field rotation** of an alt-az mount turns the field during each sub,
  smearing corner stars tangentially by r·ω·t. The rate ω comes from
  consecutive plate solutions; equatorial mounts show none.

```
Sub length: 15 s subs
  Noise           5.5 e⁻ per pixel, read noise 1.1 e⁻ (≤ Celestron Origin Mark II at ISO 1000, from its bias master)
                  shot noise 2 e⁻/s (sky, target, dark): read noise costs ≤ 5% SNR from 6 s
  Field rotation  0.0076°/s median, 0.0121°/s p90; corner stars smear tangentially (median FWHM 5.08 px)
  Sub      SNR vs no read noise  Corner smear median / p90
  5 s      94.3%                 1.5 / 2.3 px
  10 s     97.0%                 2.9 / 4.7 px
  15 s     98.0%                 4.4 / 7.0 px              ← now
  20 s     98.5%                 5.9 / 9.4 px
  30 s     99.0%                 8.8 / 14.1 px
  60 s     99.5%                 17.6 / 28.2 px
```

### Columns

| Column | Meaning |
|---|---|
| Exp, Temp, Alt | `EXPTIME`, `CCD-TEMP`, and target altitude from `CENTALT`/`OBJCTALT`, else derived from `AIRMASS`. `ALTITUDE` is ignored: it is the site's elevation |
| Moon alt, Sun alt | Altitudes in degrees at the exposure start, from `DATE-OBS` and the site (`SITELAT`/`SITELONG`, `LATITUDE`/`LONGITUD` or `OBSGEO-B`/`OBSGEO-L`). `DATE-OBS` is UTC unless the header has `TIMEZONE` (Celestron Origin writes local time). The Moon is topocentric; positions are accurate to ~0.3° |
| Stars | Stars detected above `-sigma` × noise over the local background |
| FWHM px, FWHM " | Median FWHM of fitted stars, in original sensor pixels and arcseconds. Arcseconds use the plate solve's scale, else `XPIXSZ` and `FOCALLEN`, else `CDELT2` |
| wFWHM | Siril's weighted FWHM, as its default global registration computes it: FWHM·(1 + 2·(N_ref − N)/N_ref), with N the frame's stars and N_ref the most of any frame through the same filter. A frame that lost stars to haze or cloud ranks worse than its FWHM alone says; `-best` ranks by it |
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
| Flags | Metrics where the frame is an outlier among its filter's frames: high FWHM, Ecc, Resid, Background or Gradient, or low Stars, SNR, PSF SNR, PSFSW or SNRW |

Unknown values print as `-`, are empty in CSV and are omitted keys in JSON.

## How it measures

- **Loading**: BZERO/BSCALE are applied. One-shot-colour frames (`BAYERPAT`)
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

- Only uncompressed image HDUs are read (no tile-compressed `.fits.fz`).
- Brighter stars measure wider on many real systems; FWHM therefore depends
  somewhat on the SNR ≥ 20 cut-off. Compare frames within a session rather
  than absolute values between different tools.
- PSF SNR and PSFSW follow PixInsight's formulas (`PSFSignalEstimator.h`)
  but sum only the stars this tool fits, on 2×2-binned colour frames, so
  they rank frames as PixInsight does without matching its values.

## License

MIT, see [LICENSE](LICENSE).
