# fitsy fixtures

Tile-compressed images written by astropy (cfitsio's compression code),
copied unchanged from fitsy (https://github.com/dahlend/fitsy,
`tests/data/`, commit 5081faa), MIT OR Apache-2.0. Expected pixels are
arithmetic, as fitsy's `tests/compression_reference.rs` computes them:

| File | Content | Expected |
|---|---|---|
| `ref_rice_i16` | 33 × 17 int16, RICE_1, 15 tiles | `lcg mod 1000 − 500` |
| `ref_rice_i32` | 40 × 12 int32, RICE_1, values near ±2³⁰ | `lcg − 2³⁰` |
| `ref_nocompress` | 8 × 6 int16, NOCOMPRESS, 2-row tiles (first HDU) | `37·i − 500` |
| `ref_hcomp_i32` | 64 × 64 int32, HCOMPRESS_1, values near ±2³⁰ | `lcg − 2³⁰` |

`lcg` is `x ← (1103515245·x + 12345) mod 2³¹` from x = 1, each output
the state after the step.
