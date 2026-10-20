# astro-analyzer fork of fitsio

codeberg.org/astrogo/fitsio v0.5.1 (BSD-3-Clause, see LICENSE), part of
astro-analyzer's module as `github.com/dkorunic/astro-analyzer/third_party/fitsio`
(no `go.mod` of its own and no `replace`, which `go install …@version`
refuses). `cmd/` and `ci/` are dropped; import paths, `fltimg`'s
included, are rewritten. It is formatted and linted with astro-analyzer's
code (`task fmt`, golangci-lint with every linter), so it no longer diffs
cleanly against upstream. The changes make a corrupt file fail with an
error instead of panicking or allocating what its header claims; each is
marked "astro-analyzer fork" and covered by `fork_test.go`:

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

Lint clean-up, behaviour kept unless noted:

- Errors wrap the exported `ErrFITS` (`errors.Is`), with unchanged text.
- No unchecked type assertions: per-kind `ptr.(*bool)` and the like
  became type switches, so a named type of that kind (`type Flag bool`)
  returns an error instead of panicking; header getters fall back to
  their defaults on a card of the wrong type.
- Renamed: `IMAGE_HDU`, `ASCII_TBL`, `BINARY_TBL`, `ANY_HDU` are
  `HDUImage`, `HDUASCIITable`, `HDUBinaryTable`, `HDUAny`; `imageHDU` is
  the exported `ImageHDU`, which `NewImage` returns.
- `Header.Set` returns `Append`'s error; `NewColumn` returns an error
  instead of panicking; `Image.Read` with no data returns an error.
- Unsigned card values beyond `int` and 1P descriptors beyond 32 bits are
  errors, not silent overflow.
- Dead code dropped (`seekDecoder`, unused VLA typecodes and buffer
  methods), `io/ioutil` replaced, functions reordered (`funcorder`).
- `nolint` remains only with a reason: upstream's large parsers
  (`gocyclo`, `maintidx`, `nestif`), interface-returning API (`ireturn`),
  switches with a deliberate default (`exhaustive`) and two's-complement
  conversions (`gosec`).

Go 1.27's vet rejects non-constant format strings: the tests'
`t.Fatalf(err.Error())` became `t.Fatal(err)`, and `Column.writeTxt` uses
`Fprint`, as `Fprintf` mangled a cell holding `%`. Upstream's tests pass
with astro-analyzer's `go test ./...`.
