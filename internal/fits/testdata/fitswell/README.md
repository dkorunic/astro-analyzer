# fits-well fixtures

Tile-compressed images copied unchanged from fits-well
(https://github.com/xorza/fits-well, `test_resources/`, commit 5bdb772),
MIT OR Apache-2.0, as its `src/compress/decode/tests.rs` uses them:

| File | Content | Expected |
|---|---|---|
| `comp_gzip_i16`, `comp_gzip2_i16`, `comp_rice_i16` | 24 × 16 int16, GZIP_1, GZIP_2, RICE_1 | `7·x − 5·y` |
| `comp_ricef_nodither` | float32, smooth: every tile stored losslessly in `GZIP_COMPRESSED_DATA` | `comp_ref_f32` |
| `comp_ricef_quant` | float32, NO_DITHER, RICE_1, per-tile ZSCALE/ZZERO | `comp_ref_quant_f32` |
| `comp_dither2_f32` | float32, SUBTRACTIVE_DITHER_2, ZDITHER0 3921 | `comp_ref_dither2_f32` |
| `comp_nan_f32` | float32, SUBTRACTIVE_DITHER_1, ZBLANK, two null pixels | `comp_ref_nan_f32` |
| `comp_hcomp_i16` | 24 × 16 int16, lossless HCOMPRESS_1 | `7·x − 5·y` |
| `comp_hcomp_odd_i16` | 17 × 17 int16, lossless HCOMPRESS_1 (`fpack -h -s 0`, CFITSIO 4.070) | `(7919·i + 13) mod 2001 − 1000` |
| `comp_plio_i32` | 24 × 16 int32, PLIO_1 | `(x + y) mod 7` |
| `comp_hcomp_lossy`, `comp_hcomp_lossy_odd` | lossy HCOMPRESS_1, SCALE 4 | `comp_ref_hcomp_lossy`, `comp_ref_hcomp_lossy_odd` (`funpack`) |
| `comp_hcomp_smooth` | 32 × 32 int32, SCALE 4, SMOOTH 1 | `comp_ref_hcomp_smooth` |
| `hcomp_wide_i32.huf` | a bare 100 × 100 HCOMPRESS stream, sum past 32 bits, over 31 bit planes | `hcomp_wide_i32.raw`, big-endian int32 |

The `comp_ref_*` files are plain FITS images of the reference decodes,
which `TestReferenceDecodes` matches bit for bit.
