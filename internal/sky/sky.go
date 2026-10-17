// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

// Package sky computes where the Sun and Moon are, to explain sky
// background changes across a session: moonlight, twilight, altitude.
//
// Positions use the low-precision formulas of the Astronomical Almanac
// (section C for the Sun, ~0.01°; section D for the Moon, ~0.3° in
// position): ample for altitudes and phase, not for occultations.
// Coordinates are of date, compared directly with J2000 targets; precession
// adds well under a degree this century.
package sky

import (
	"math"
	"time"
)

const rad = math.Pi / 180

// days returns days since J2000.0 (2000-01-01 12:00 TT, taken as UTC).
func days(t time.Time) float64 {
	return float64(t.UTC().Sub(time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC))) / float64(24*time.Hour)
}

// obliquity is the mean obliquity of the ecliptic in degrees.
func obliquity(d float64) float64 { return 23.439 - 0.0000004*d }

// equatorial converts ecliptic longitude and latitude to RA and Dec, all
// in degrees.
func equatorial(lambda, beta, eps float64) (float64, float64) {
	sl, cl := math.Sincos(lambda * rad)
	sb, cb := math.Sincos(beta * rad)
	se, ce := math.Sincos(eps * rad)
	ra := math.Atan2(ce*cb*sl-se*sb, cb*cl) / rad

	return math.Mod(ra+360, 360), math.Asin(se*cb*sl+ce*sb) / rad
}

// Sun returns the Sun's geocentric RA and Dec in degrees.
func Sun(t time.Time) (float64, float64) {
	d := days(t)
	l := 280.460 + 0.9856474*d
	g := (357.528 + 0.9856003*d) * rad
	lambda := l + 1.915*math.Sin(g) + 0.020*math.Sin(2*g)

	return equatorial(lambda, 0, obliquity(d))
}

// Moon returns the Moon's geocentric RA and Dec and its horizontal
// parallax, all in degrees.
func Moon(t time.Time) (float64, float64, float64) {
	d := days(t)
	c := d / 36525 // Julian centuries
	sin := func(a, b float64) float64 { return math.Sin((a + b*c) * rad) }
	cos := func(a, b float64) float64 { return math.Cos((a + b*c) * rad) }
	lambda := 218.32 + 481267.881*c +
		6.29*sin(135.0, 477198.87) - 1.27*sin(259.3, -413335.36) +
		0.66*sin(235.7, 890534.22) + 0.21*sin(269.9, 954397.74) -
		0.19*sin(357.5, 35999.05) - 0.11*sin(186.5, 966404.03)
	beta := 5.13*sin(93.3, 483202.02) + 0.28*sin(228.2, 960400.89) -
		0.28*sin(318.3, 6003.15) - 0.17*sin(217.6, -407332.21)
	parallax := 0.9508 + 0.0518*cos(135.0, 477198.87) + 0.0095*cos(259.3, -413335.36) +
		0.0078*cos(235.7, 890534.22) + 0.0028*cos(269.9, 954397.74)
	ra, dec := equatorial(lambda, beta, obliquity(d))

	return ra, dec, parallax
}

// Altitude returns the geometric altitude in degrees of (ra, dec) seen from
// latitude lat and east longitude lon at t, ignoring refraction.
func Altitude(t time.Time, lat, lon, ra, dec float64) float64 {
	gmst := 280.46061837 + 360.98564736629*days(t)
	h := (gmst + lon - ra) * rad
	sp, cp := math.Sincos(lat * rad)
	sd, cd := math.Sincos(dec * rad)

	return math.Asin(sp*sd+cp*cd*math.Cos(h)) / rad
}

// MoonAltitude is the Moon's topocentric altitude: seen from the surface
// rather than the Earth's centre, it sits up to a degree lower.
func MoonAltitude(t time.Time, lat, lon float64) float64 {
	ra, dec, parallax := Moon(t)
	alt := Altitude(t, lat, lon, ra, dec)

	return alt - parallax*math.Cos(alt*rad)
}

// Separation is the angle in degrees between two RA/Dec positions.
func Separation(ra1, dec1, ra2, dec2 float64) float64 {
	s1, c1 := math.Sincos(dec1 * rad)
	s2, c2 := math.Sincos(dec2 * rad)
	cosd := s1*s2 + c1*c2*math.Cos((ra1-ra2)*rad)

	return math.Acos(max(-1, min(1, cosd))) / rad
}

// Illumination is the lit fraction of the Moon's disc, 0 (new) to 1
// (full). The Sun is taken as infinitely far, so the phase angle is the
// supplement of the Moon's elongation; the error is under 1%.
func Illumination(t time.Time) float64 {
	sra, sdec := Sun(t)
	mra, mdec, _ := Moon(t)

	return (1 - math.Cos(Separation(sra, sdec, mra, mdec)*rad)) / 2
}
