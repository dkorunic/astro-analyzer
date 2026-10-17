# CLAUDE.md

Go CLI that measures FITS astrophotography subframes (stars, FWHM,
eccentricity, SNR, sky background, wavelet noise), flags outlier frames and
maps star shape across the field. Runs fully locally. User-facing docs are in
README.md.

## Commands

```sh
go test ./...                    # all tests, ~2 s
go test -race ./...
golangci-lint run ./...          # must report 0 issues (.golangci.yml enables all linters)
task fmt                         # gci + gofumpt + go fix + betteralign; run before committing
go build -o astro-analyzer .
task build                       # fmt, then static release binary with -pgo=auto and version ldflags
task pgo                         # regenerate default.pgo from BenchmarkPipeline (~15 s)
```

`default.pgo` is a CPU profile of `BenchmarkPipeline` (internal/analyze), a
synthetic full-size colour frame tuned to match a real one: ~4500 detected
stars, 15% of CPU in PSF fits. Regenerate it after changing hot code paths;
it is worth ~2% on real sessions.

## Layout

Dependencies only point downward in this list:

| Package | Role |
|---|---|
| `main.go` | Flags, directory walk (`expand`), worker pool, output selection |
| `internal/report` | Metric table, outlier flags, overview (equipment, integration per night), session summary + sparklines, field map, stack coverage, CSV/JSON |
| `internal/drizzle` | DrizzleIntegration Scale / Drop shrink recommendation from sampling and plate-solved dither |
| `internal/analyze` | Per-frame pipeline: detection, half-max seed, aperture SNR, PSF fits, medians, 3×3 field cells |
| `internal/psf` | Levenberg–Marquardt fit of `gaussian`, `moffat4` (default) and `moffat` models |
| `internal/sky` | Low-precision Sun and Moon positions (Astronomical Almanac), altitude, illumination |
| `internal/stats` | Median/MAD, outlier rule, sigma-clipped sky, wavelet MRS noise + background map |
| `internal/fits` | FITS loading, BZERO/BSCALE, Bayer 2×2 binning, saturation mask, header metadata, TAN WCS |
| `internal/synth` | Test-only helpers: synthetic star fields and a FITS writer |

To add a per-frame metric: add the field to `analyze.Result`, compute it in
`analyze.Frame`, then add one entry to `metrics` in `internal/report`. A
score that depends on the session (like wFWHM) goes on `report.Frame`
instead, set before `FlagOutliers`. That
entry drives the table column, CSV/JSON key, summary row and outlier flag.

## Conventions

- **NaN means unknown** for every measurement. It prints as `-`, is an empty
  CSV field and an omitted JSON key, and is skipped by all statistics.
  Comparisons with NaN are false, so write test assertions as `!(good)`
  (see `near` in `analyze_test.go`); `x > tol` silently passes on NaN.
- **PixInsight is the reference.** FWHM is the geometric mean of the axes,
  eccentricity is `√(1−(minor/major)²)`, per-frame values are medians (the
  FWHMEccentricity script; SubframeSelector uses a Chauvenet-trimmed mean),
  the residual is PSFData.mad (Winsorized mean |data − model| over the full
  sample range, `fits.Image.Range`) and fits with residual ≥ 0.1 are
  rejected, MRS noise is divided by 0.974, PSF SNR and PSFSW take that noise as σ
  (as SubframeSelector passes it), and Moffat4 is the default model.
  Cite the PixInsight source when changing a convention: scripts at
  `/Applications/PixInsight/src/scripts/...:line`, C++ (PSFFit,
  SubframeSelector, PSFSignalEstimator) from gitlab.com/pixinsight/PCL.
  PixInsight and Siril point-sample the PSF; we integrate over the pixel,
  so their FWHM reads higher on undersampled stars.
- **Synthetic stars must be pixel-integrated** (`synth.Span`, 10×10 sub-grid
  for Moffat). Point-sampled stars hide undersampling bias.
- `// ponytail:` comments mark deliberate simplifications and their limits.
- Keep `metric` and other struct literals keyed: `betteralign` (in `task fmt`)
  reorders fields and skips structs with positional literals.

## Verifying changes

Unit tests use synthetic frames. For any refactor, also compare real output
byte for byte before and after:

```sh
./astro-analyzer <session dir> > before.txt   # with the old binary
./astro-analyzer <session dir> > after.txt    # with the new binary
cmp before.txt after.txt
```

The table output is deterministic. A refactor that changes it is not a
refactor. Run each `-psf` model and `-csv` too; flag values are not covered
by the default run.

## Gotchas

- `fitsio` (codeberg.org/astrogo/fitsio) does **not** apply BZERO/BSCALE, and
  `Image.Read` panics unless the destination slice already has capacity for
  every pixel (it only calls `SetLen`).
- Header numbers decode as `int` or `float64`; strings keep trailing padding.
- `ALTITUDE` is the observing site's elevation in metres, not target
  altitude. Altitude comes from `CENTALT`/`OBJCTALT` or `AIRMASS`.
- Saturation must be detected **before** Bayer binning. Averaging a clipped
  pixel with its neighbours hides the clipping and inflates FWHM.
- FITS rows run bottom-up: field cell 0 is the first FITS row, printed at
  the bottom.
- Untyped integer constants divide as integers: `const beta = 3; 1/beta == 0`.
- Free-β `moffat` costs ~8× `moffat4` because of `exp`/`log` per sub-pixel;
  `moffat4` uses `1/u⁴` and is as fast as the Gaussian.
- Siril's wFWHM differs by registration mode: global (the default,
  `global.c:822`) divides by N_ref, manual 1–3 stars (`3stars.c:274`) by N.
  We follow global.
- **Drizzle rules are sourced**: Fruchter & Hook 2002 (≳ 2.5 px per FWHM is
  well sampled; scale s, pixfrac p, R eq. 9–10; convolutions add in
  quadrature), DrizzlePac Handbook §7 (weights rms < 20% of median; pixfrac
  0.5–1.0, slightly > s) and PixInsight (WBPP: > 15 frames, CFA Scale 1 Drop
  shrink 1; Bayer drizzle 3–4× the frames). PixInsight's Scale is 1/s.
  Constants in `internal/drizzle` name their source; keep it that way.
- `report.Frame.Result` is a named field, not embedded: `betteralign` moves
  embedded structs to the end and `embeddedstructfieldcheck` wants them first.
- Celestron Origin (alt-az) frames rotate 100°+ per session (`CROTA2`); the
  first frame may have `ASTRSOLV = F` (mount pointing, not a plate solve).
  `readWCS` keeps its `CD` (plate scale) but sets `OK = false`.
- `img.Gain` is per raw pixel; after `Bin2` the effective gain is
  `Gain·Scale²`. Apply it where the binned flux is used, not in `Load`, so
  the `-gain` flag gets it too.
- `local` (the wavelet background map) contains part of each star's light;
  measure star-relative quantities against the aperture annulus sky.
- `IMAGETYP` other than LIGHT returns `fits.ErrNotLight`, which callers count
  and skip silently. Frames without `IMAGETYP` are treated as lights.
- WCS pixel coordinates put pixel **centres** at integers, so drizzle output
  edges sit at `−½ + i/k`. At Scale 2 that coincides with `i/k`, so test
  grid alignment at Scale 3 or CFA Scale 1.
- Session statistics (outlier flags, summary, drizzle) are per `FILTER`; a
  mixed L/Ha directory is two populations, which pooled MAD cannot handle.
- `DATE-OBS` is UTC by the standard, but Celestron Origin writes local time
  with the offset in `TIMEZONE` ('+0200'). `Meta.Time` keeps that zone, so
  `.Format` shows local clock time and `.UTC()` feeds the ephemeris.
- Observing nights run noon to noon in local solar time (UTC + lon/15 h),
  not by calendar date: nearly every session crosses midnight.
- Counting hot pixels measures the sky, not the sensor: the detection
  threshold is 5× the frame's noise, so a brighter sky hides warm pixels.
  `hotLevel` uses a brightness rank window instead (r = 0.97 with CCD-TEMP
  on a real uncooled session; the brightest hot pixels sit near full well).
- Sky gradients: a plane fit leans toward a nebula and inflates the MAD, so
  symmetric clipping rejects nothing. Clip high residuals harder (2.5σ vs
  4σ): emission only adds light.
- `BAYERPAT` row origin differs between programs (first stored row vs top
  row). Siril's default reads from the top; on Celestron Origin frames that
  pairs green sites 13% apart. `cfaSamples` picks the reading under which
  the two greens agree. A misread pattern makes R and B sample green, not
  each other.
- `metric.get` takes the whole `report.Frame`, so session-relative scores
  (wFWHM, set by `report.Score`) sit beside per-frame `analyze.Result` values.
- Zero pixels connected to the frame edge load as NaN: registration fills
  its border with 0. Interior zeros (calibration clipping) stay data.
