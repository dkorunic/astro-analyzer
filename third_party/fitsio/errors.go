// Copyright 2026 Dinko Korunic. Use of this source code is governed by a
// BSD-style license that can be found in the LICENSE file.

package fitsio

import "errors"

// ErrFITS is wrapped by every error fitsio returns: errors.Is(err,
// ErrFITS) tells a malformed or unsupported file. (astro-analyzer fork).
var ErrFITS = errors.New("fitsio")
