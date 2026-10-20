module github.com/dkorunic/astro-analyzer

go 1.27.2

require (
	codeberg.org/astrogo/fitsio v0.5.1
	github.com/klauspost/compress v1.20.3
	golang.org/x/image v0.47.0
)

replace codeberg.org/astrogo/fitsio => ./third_party/fitsio
