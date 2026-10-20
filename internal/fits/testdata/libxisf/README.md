# libXISF corpus

40 XISF files written by libXISF,
copied unchanged from the xisf Rust crate's test corpus
(https://github.com/cruzzil/xisf, `corpus/generated/`, commit 21c4aac),
MIT License, Copyright (c) 2026 The xisf-rs Developers.

Each is one image (Gray 17 × 11, or RGB 5 × 7 planar) in a sample format
(`UInt8`, `UInt16`, `UInt32`, `Float32`, `Float64`) and codec (none, zlib,
zlib+sh, lz4, lz4+sh, lz4hc, zstd) named by the file. The raw little-endian
image bytes are `(i·37 + (i / bytes per sample)·11 + 1) & 0xff` for byte i;
`TestLibXISF` recomputes them. The generator is that repository's
`tools/corpus-gen/generate.cpp` (GPL-3.0, not copied here); its output
carries no licence obligation of its own.
