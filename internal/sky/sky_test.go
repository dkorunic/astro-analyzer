// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package sky

import (
	"math"
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}

	return t
}

// TestSun: at the March 2024 equinox the Sun crosses the equator at RA 0;
// at local apparent noon on that day it stands 90° − latitude high.
func TestSun(t *testing.T) {
	if ra, dec := Sun(utc("2024-03-20T03:06:00Z")); !(math.Abs(dec) < 0.02) || !(math.Min(ra, 360-ra) < 0.05) {
		t.Errorf("equinox sun at %.3f, %.3f; want 0, 0", ra, dec)
	}
	// Equation of time is −7.5 min on 20 March: apparent noon at lon 0 is 12:07:30 UTC.
	noon := utc("2024-03-20T12:07:30Z")
	ra, dec := Sun(noon)
	if alt := Altitude(noon, 45, 0, ra, dec); !(math.Abs(alt-45.1) < 0.1) {
		t.Errorf("equinox noon sun altitude at 45°N = %.2f, want ~45.1", alt)
	}
}

// TestEclipse: during the total solar eclipse of 8 April 2024 the Moon is
// new and, seen from Mazatlán in totality, covers the Sun exactly, which
// only holds for the topocentric (parallax-corrected) Moon.
func TestEclipse(t *testing.T) {
	newMoon := utc("2024-04-08T18:21:00Z")
	sra, sdec := Sun(newMoon)
	mra, mdec, _ := Moon(newMoon)
	if sep := Separation(sra, sdec, mra, mdec); !(sep < 1) {
		t.Errorf("new moon %.2f° from the Sun, want under 1°", sep)
	}
	if k := Illumination(newMoon); !(k < 0.01) {
		t.Errorf("new moon illumination %.3f", k)
	}

	totality := utc("2024-04-08T18:09:00Z")
	lat, lon := 23.22, -106.42
	sra, sdec = Sun(totality)
	sun := Altitude(totality, lat, lon, sra, sdec)
	moon := MoonAltitude(totality, lat, lon)
	if !(math.Abs(moon-sun) < 0.15) || !(sun > 65 && sun < 72) {
		t.Errorf("Mazatlán totality: sun alt %.2f, moon alt %.2f; want equal, ~69°", sun, moon)
	}
}

func TestFullMoon(t *testing.T) {
	if k := Illumination(utc("2024-04-23T23:49:00Z")); !(k > 0.99) {
		t.Errorf("full moon illumination %.3f", k)
	}
	if k := Illumination(utc("2024-04-15T19:13:00Z")); !(math.Abs(k-0.5) < 0.03) { // first quarter
		t.Errorf("first quarter illumination %.3f", k)
	}
}
