// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package psf

import "testing"

// TestValid pins the -psf flag values documented in the help text.
func TestValid(t *testing.T) {
	for _, m := range []string{"gaussian", "moffat4", "moffat"} {
		if !Valid(m) {
			t.Errorf("Valid(%q) = false", m)
		}
	}
	if Valid("Moffat4") || Valid("") {
		t.Error("Valid accepts names that are not documented")
	}
}
