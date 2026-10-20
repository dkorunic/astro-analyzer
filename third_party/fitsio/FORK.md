# astro-analyzer fork of fitsio

codeberg.org/astrogo/fitsio v0.5.1 (BSD-3-Clause, see LICENSE), used via
`replace` in astro-analyzer's go.mod. `cmd/` and `ci/` are dropped. The
changes make a corrupt file fail with an error instead of panicking or
allocating what its header claims; each is marked "astro-analyzer fork"
and covered by `fork_test.go`:

- `DecodeHDU`: checked type assertions for `NAXIS`, `NAXISn`, `BITPIX` and
  `CONTINUE`; `NAXIS` within 0–999, `NAXISn` non-negative.
- `loadImage`, `loadTable`: sizes multiplied with overflow checks, and
  allocated only when the reader, an `*io.LimitedReader`, has that many
  bytes left (`streamDecoder.alloc`).
- `loadTable`: two axes, `TFIELDS`/`PCOUNT`/`THEAP` non-negative integers,
  `TTYPEn` optional, `TFORMn` a string. `THEAP` is the heap's offset from
  the table (FITS 4.0 §7.3.5); values below the table size keep fitsio's
  own "gap" reading, as its writer emits 0. The heap ends at `PCOUNT`,
  not at the block padding.
- `Column.readBin`: a variable-length array descriptor must lie within
  the heap.

Upstream's tests pass with `go test -vet=off ./...`: Go 1.27's vet rejects
their non-constant format strings, unchanged here.
