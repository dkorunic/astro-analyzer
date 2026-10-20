# CLAUDE.md

Go CLI that measures astrophotography subframes (FITS, fpack `.fz`, XISF,
TIFF, camera RAW via dcraw: stars, FWHM, ASTAP's HFD, N.I.N.A.'s HFR,
eccentricity, SNR and PixInsight's PSF SNR / PSFSW / SNRW weights, sky
background, brightness and gradient, wavelet noise, hot pixels, satellite
trails), flags
outlier frames, maps star shape across the field and reports per session:
integration per night with Moon, twilight and Bortle class,
calibration-frame checks (headers, bias banding, dark pedestal and hot
pixels, flats, photon-transfer gain, master noise), stack coverage,
drizzle and WBPP settings, and a sub-length comparison. Runs fully
locally. User-facing docs are in README.md.

## Commands

```sh
go test ./...                    # all tests, ~5 s (the fitsio fork's too)
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
| `internal/report` | Metric table, outlier flags, overview (equipment, integration per night), calibration checks: headers, banding, dark pedestal and hot pixels, flats, photon-transfer gain, master noise, bias read noise (`calib.go`), session summary + focus drift + sparklines, field map, stack coverage, WBPP predictions (`wbpp.go`), sub-length comparison and camera presets (`subs.go`), chip QE, colour response and dark current, sky brightness and Bortle class (`sky.go`), acquisition checks and N.I.N.A. trigger settings: focus temperature slope, HFR/temperature/time triggers, cooler, dew, cloud, wind, dither cadence, pointing drift, meridian flips (`acquisition.go`), CSV/JSON |
| `internal/drizzle` | DrizzleIntegration Scale / Drop shrink recommendation from sampling and plate-solved dither |
| `internal/analyze` | Per-frame pipeline: detection, half-max seed, aperture SNR, PSF fits, ASTAP HFD (`hfd.go`), N.I.N.A. HFR (`hfr.go`), medians, PSF SNR/PSFSW/SNRW, gradient, hot level, trails (`trails.go`), raw pixel noise, Sun/Moon, 3×3 field cells |
| `internal/psf` | Levenberg–Marquardt fit of `gaussian`, `moffat4` (default) and `moffat` models |
| `internal/sky` | Low-precision Sun and Moon positions (Astronomical Almanac), altitude, illumination |
| `internal/stats` | Median/MAD, outlier rule, Spearman, Chauvenet (RCR), sigma-clipped sky, wavelet MRS noise + background map, sky gradient plane, M* and two-sided scale (PSFSW, SNRW) |
| `internal/fits` | Loading behind `source` (header + sample reader): FITS, fpack tiles (`fpack.go`: Rice, GZIP_1/2, NOCOMPRESS, dithered and lossless floats; HCOMPRESS_1 and PLIO_1 ported from cfitsio in `cfitsio.go`, which keeps NASA's notice), XISF (`xisf.go`: zlib/lz4/zstd via klauspost, own LZ4 block decoder), TIFF (`tiff.go`, x/image), RAW (`raw.go`, dcraw); fixtures in `testdata` from independent writers (`gen.py`; libXISF, fits-well and fitsy corpora in subdirectories, each with a README). FITS loading, BZERO/BSCALE, frame type, Origin ADU step and gain fixes, Bayer 2×2 binning and CFA sky, saturation mask, header metadata, TAN WCS, calibration pixel stats (`CalStats`), `PairNoise`/`PairGain` |
| `internal/synth` | Test-only helpers: synthetic star fields and a FITS writer |
| `third_party/fitsio` | Fork of codeberg.org/astrogo/fitsio v0.5.1 (BSD-3, `FORK.md`): FITS headers, images and binary tables, failing on corrupt files instead of panicking, lint-clean under our config |

To add a per-frame metric: add the field to `analyze.Result`, compute it in
`analyze.Frame`, then add one entry to `metrics` in `internal/report`. A
score that depends on the session (like wFWHM, or SkyMag, which needs the
session's read noise) goes on `report.Frame`
instead, set before `FlagOutliers`. That
entry drives the table column, CSV/JSON key, summary row and outlier flag.

The default output runs, in order (`main.go`): `Table`, `Overview`,
`Calibration`, `Summary`, `Field`, `Coverage`, `Pointing`, `Drizzle`, `WBPP`,
`SubLength`. README shows each section's output from a real Bubble Nebula
session (Origin Mark II, 1340 frames); refresh those examples when a
section's format changes, and list a new metric's CSV/JSON key there.

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

- `fitsio` is forked into this module as `third_party/fitsio` (import
  path `github.com/dkorunic/astro-analyzer/third_party/fitsio`, `FORK.md`):
  upstream panics or allocates what a corrupt header claims. No `replace`
  in go.mod: `go install …@version` refuses a module that has one.
  `openFITS` passes an `*io.LimitedReader` of the file size so the fork
  can refuse sizes past the file. It is our code now: `go test ./...`,
  `task fmt` and golangci-lint cover it like the rest. Its errors wrap
  `fitsio.ErrFITS`.
- Quantized fpack floats match cfitsio bit for bit (`TestReferenceDecodes`):
  every pixel, null or not, takes the next dither random; the random
  table is float32; `float64(v*scale) + zero` stops Go fusing the
  multiply-add on arm64; tiles in `GZIP_COMPRESSED_DATA` or
  `UNCOMPRESSED_DATA` hold raw floats and are not dequantized.
- HCOMPRESS_1 uses cfitsio's 64-bit decoder for every BITPIX
  (`TestHcompressWide`: sums past 32 bits). astropy writes `SMOOTH` as a
  logical (`ZVAL2 = T`), fpack as a number. PLIO_1 tiles are 16-bit
  words (`1PI`): scanned as `[]int16`, a `[]byte` target gets half.
- `fitsio` does **not** apply BZERO/BSCALE, and
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
- Celestron Origin Mark II (`Origin678C`) `EGAIN` is per ADC count, but stored pixels are counts
  × 25 (ISO 1000). `Load` divides by the step 99% of pixel values share (`step`; an exact GCD gives 5), on
  Origin only: photon transfer on a real session confirmed it (0.0095 vs
  0.0112 e⁻/ADU). Its bias and dark files are 9-frame masters (`NSTACK`),
  saved as identical copies.
- The original Origin (`Origin178`) steps by 6.25 ADU (gaps of 6 and 7), so
  `step` finds 1, and its `EGAIN` is 4.5× the two-point gain from consecutive
  raw lights (0.0162 measured here; 0.0165 in originstack's
  `camera_profile.py` over 31 sessions of another unit). `Load` divides by
  4.5. Its bias/dark files cannot give read noise from pairs: identical copies.
- `fits.Load` returns the header `Meta` (with `Type`) alongside
  `ErrNotLight`, plus `PixNoise` and `Level` for bias and darks (pixels are
  not kept); `report.Calibration` compares them with the lights'. Origin
  darks sit 120–410 ADU *below* their bias: no dark current is measurable.
  Their 9-frame bias masters bound read noise at 0.93–0.97 e⁻ (Mark II,
  two sessions) and 1.50 e⁻ (Origin178); the presets in `subs.go` take the
  highest, so they stay upper bounds.
  Bias read noise uses the lights' `Result.Gain` (bias headers lack EGAIN).
  Darks are loaded in full and run through `analyze.Frame` (`calibFrame`)
  so their `Hot` compares with the lights'; set `Cal` after `Bin2`, which
  returns a fresh `Image`.
- Photon-transfer gain (`fits.PairGain`) needs the lights' pedestal. On
  Origin the bias pedestal is wrong (darks sit below it), so the dark level
  is used: that gain is a lower bound (dark current counts as noise). Mark
  II: 0.0091–0.0101 on cool sessions, 0.0076 at 32 °C, against EGAIN/25 =
  0.0112; Origin178: 0.0161–0.0164 against 0.0165. The bias pedestal read
  0.0055–0.0076 on nebula fields: always check the pedestal first. `Stack` (master lights, `NCOMBINE` > 1) is skipped, not listed.
  PixInsight writes `IMAGETYP = 'Master Light'`: matching on "LIGHT" alone
  measured a WBPP master as a sub.
- `pixNoise` samples rows 0, 16, 32…: on a CFA only the first stored
  row's two colours (green and blue on the Origin's GBRG, red and green
  when `BayerFlip`). `response` in `sky.go` weights the chip's colour
  response the same way; change both together.
- Camera and chip constants (QE, colour response, dark current) come from
  astro-recommender's `internal/config/cameras.go`; keep them in step.
  Seestar S50 frames say `INSTRUME = 'ZWO ASI462MC'`, so the ASI462
  preset covers it. A camera with no preset gets its chip from `XPIXSZ`
  and `NAXIS` (`chipOf`, closest within 2%, ties give none); effective
  pixels sit up to 1.5% off nominal (Origin178 3056 × 2048 vs 3096 × 2080).
- Per-pixel noise in electrons comes from raw same-colour neighbours
  (`pixNoise`, before binning), not the binned MRS noise: on Origin frames
  2×2 binning lowers the noise only 1.7×, not 2×, so neighbouring raw
  pixels share noise and binned σ·2 would overstate it.
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
- WBPP's fast integration (auto at 150+ frames) skips local normalization,
  the minimum-weight cut and the rejection algorithm
  (`BPP-Pipeline.js:626,662`, `BPP-Operations.js:1578`). Gradient
  comparisons for LN must be made on the reference frame's grid: an alt-az
  light dome is fixed to the sensor and turns on the sky.
- Trails (`trails.go`) use a block-median sky, not the wavelet map
  `local`: around bright stars `local` is off by thousands of ADU per
  4×4 bin, which fragments a trail. The threshold uses the MRS noise, not
  the binned spread, which faint stars widen. Verified on the Western Veil
  session (413 frames, every frame inspected): both trails found, no false
  ones on the filaments or cloud gradients. Blobs that fail the thin test
  go to `hough`, which must explain 80% of the blob: a disc's chord through
  its centre passes a peak test but not that one (`TestHough`).
- **Other formats become a FITS header** (`header`, `card`): structural
  cards are dropped and samples arrive physical. XISF, TIFF and RAW rows
  stay in stored order (top first), as PixInsight reads FITS, so a FITS
  file and its XISF copy measure byte for byte the same; checked with
  every codec and fpack type on real Origin frames. XISF header sizes
  are numbers, not facts: attachments are bounded by the file and
  decompressed sizes by each codec's best ratio (`expansion`), so a
  corrupt header fails instead of panicking (`TestXISFHostile`).
- PixInsight calibration masters are [0, 1] floats: `toADU` multiplies
  calibration frames (not lights) by 65535, else read noise came out
  10⁻⁶ e⁻. WBPP's masters are built from the session's own files, so
  `raws` drops masters when raw files of that type exist; on Origin the
  WBPP master bias averages 5 *copies*, and its NCOMBINE gave a read-noise
  "bound" 25% under the truth. An XISF with no FITS keywords is PixInsight
  output (`Processed`), skipped like a stack. Directory walks skip TIFF.
- `hfd` scales pixels to ASTAP's 0–65535 (`65535/Range`): its noise floor
  of 1 and its SNR are in those units, so [0, 1] data gave no HFD at all.
  On pixel-integrated Gaussians it reads 2σ_eff√(π/2) with σ_eff² = σ² + ¼
  (pixel 1/12 + bilinear 1/6), as ASTAP does: `TestHFD`. The annulus
  offsets are precomputed; scanning a 31×31 box per star cost 10%.
- RAW: Coffin's `dcraw -D -4 -t 0 -c` (visible area, to stdout). LibRaw's
  `unprocessed_raw` includes the masked borders and `dcraw_emu` has no
  `-D`; dcraw 9.28 cannot read CR3.
- **N.I.N.A. is the reference for HFR and its triggers** (`NINA.Image`,
  `NINA.Sequencer` at commit 4742afa; cite `file.cs:line`). Its HFR is a
  curve-of-growth radius averaged with a **mean** after a 1.5σ cut on star
  radius, unlike every other column's median: that is what its image
  history and Autofocus After HFR Increase use. Trigger defaults (5%, 10
  frames, 5 °C, 30 min, 5 exposures, 10′) are constants in `acquisition.go`. NINA writes no
  `PIERSIDE`; flips are 180° ± 15° rotation jumps, counted only when the
  field otherwise holds within 5° (equatorial), so alt-az rotation never
  reads as a flip.
- Dithers are steps between consecutive plate solutions at least 1.5 px
  off the median step of the 5 either side (the drift). Celestron Origin
  dithers every frame, 8 px (Bubble) to 30 px (Mizar, Whirlpool).
