// SPDX-FileCopyrightText: 2026 Dinko Korunic
// SPDX-License-Identifier: MIT

package stats

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/dkorunic/astro-analyzer/internal/synth"
)

func TestOutliers(t *testing.T) {
	v := []float64{4, 4.1, 3.9, 4, 4.05, 6, 2, math.NaN()}
	got := Outliers(v, +1, 3, 0.1)
	want := []bool{false, false, false, false, false, true, false, false} // low FWHM is fine
	for i := range v {
		if got[i] != want[i] {
			t.Fatalf("Outliers(+1) = %v, want %v", got, want)
		}
	}
	// A stable session: 3 robust sigmas is tiny, the 10% floor must hold.
	if Outliers([]float64{4, 4.01, 3.99, 4, 4.2}, +1, 3, 0.1)[4] {
		t.Error("a 5% deviation was flagged despite the 10% minimum")
	}
}

// TestSelect checks Select against a full sort for every rank, on random,
// duplicate-heavy and already sorted input.
func TestSelect(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for n := 1; n < 60; n++ {
		inputs := [][]float64{make([]float64, n), make([]float64, n), make([]float64, n)}
		for i := range n {
			inputs[0][i] = rng.NormFloat64()
			inputs[1][i] = float64(rng.IntN(3))
			inputs[2][i] = float64(i)
		}
		for _, in := range inputs {
			want := slices.Sorted(slices.Values(in))
			for k := range n {
				if got := Select(slices.Clone(in), k); got != want[k] {
					t.Fatalf("Select(%v, %d) = %v, want %v", in, k, got, want[k])
				}
			}
		}
	}
}

// TestRadixSort checks radixSort against slices.Sort on keys spanning
// negatives, zero, sky-like values sharing their exponent and extremes.
func TestRadixSort(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for _, n := range []int{1, 2, 7, 1000} {
		in := make([]float32, n)
		for i := range in {
			switch i % 4 {
			case 0:
				in[i] = float32(rng.NormFloat64())
			case 1:
				in[i] = 1000 + float32(rng.IntN(500))/4
			case 2:
				in[i] = float32(rng.NormFloat64() * 1e30)
			}
		}
		keys := make([]uint32, n)
		for i, v := range in {
			keys[i] = sortKey(v)
		}
		keys = radixSort(keys, make([]uint32, n))
		slices.Sort(in)
		for i, k := range keys {
			if got := fromKey(k); got != in[i] {
				t.Fatalf("n=%d: sorted[%d] = %v, want %v", n, i, got, in[i])
			}
		}
	}
}

// TestLayerNoise re-derives the per-layer noise constants from unit white
// noise; NoiseMRS's 3σ significance test depends on them.
func TestLayerNoise(t *testing.T) {
	const n = 512
	rng := rand.New(rand.NewPCG(5, 6))
	c := make([]float32, n*n)
	for i := range c {
		c[i] = float32(rng.NormFloat64())
	}
	tmp := make([]float32, n*n)
	for j, want := range layerNoise {
		next := make([]float32, n*n)
		smooth(next, tmp, c, n, n, 1<<j)
		var sq float64
		for i := range c {
			d := float64(c[i] - next[i])
			sq += d * d
		}
		got := math.Sqrt(sq / float64(len(c)))
		if math.Abs(got-want)/want > 0.03 {
			t.Errorf("layer %d noise = %.4f, want %.3f", j+1, got, want)
		}
		c = next
	}
}

// TestSmoothEdges compares smooth bit for bit with mirroring every tap, on
// frames narrower and wider than the kernel, so the unmirrored interior path
// and its bounds cannot drift.
func TestSmoothEdges(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	for _, sz := range [][2]int{{1, 1}, {3, 2}, {17, 5}, {37, 23}, {64, 40}} {
		w, h := sz[0], sz[1]
		in := make([]float32, w*h)
		for i := range in {
			in[i] = float32(rng.NormFloat64())
		}
		for step := 1; step <= 8; step *= 2 {
			got, tmp := make([]float32, w*h), make([]float32, w*h)
			smooth(got, tmp, in, w, h, step)
			for y := range h {
				for x := range w {
					var s float32
					for k, c := range b3 {
						var v float32
						for k2, c2 := range b3 {
							v += c2 * in[mirror(y+(k-2)*step, h)*w+mirror(x+(k2-2)*step, w)]
						}
						s += c * v
					}
					if got[y*w+x] != s {
						t.Fatalf("%dx%d step %d at (%d,%d): %v, want %v", w, h, step, x, y, got[y*w+x], s)
					}
				}
			}
		}
	}
}

// TestNoiseMRS puts stars on a steep gradient and a bright nebula: the
// sigma-clipped stddev is inflated by them, wavelet noise is not.
func TestNoiseMRS(t *testing.T) {
	pix := make([]float32, synth.W*synth.H)
	for i, v := range synth.StarField(4, 4) {
		x, y := float64(i%synth.W), float64(i/synth.W)
		neb := 400 * math.Exp(-((x-300)*(x-300)+(y-200)*(y-200))/(2*80*80))
		pix[i] = float32(v + 2*x + neb)
	}
	bg, std := SkyStats(pix)
	noise, local := NoiseMRS(pix, synth.W, synth.H, bg, std)
	t.Logf("noise %.2f, sigma-clipped std %.2f", noise, std)
	if math.Abs(noise-synth.Noise)/synth.Noise > 0.05 || std < 2*synth.Noise {
		t.Errorf("noise = %.2f (std %.2f), want ~%.0f", noise, std, synth.Noise)
	}
	// The background map follows the gradient and nebula, not the stars.
	i := 200*synth.W + 300 // nebula centre, between stars
	if want := synth.Bg + 600 + 400; math.Abs(float64(local[i])-want) > 20 {
		t.Errorf("local background at nebula centre = %.1f, want ~%.0f", local[i], want)
	}
}

// TestThreshold checks that x < threshold(a, s) agrees with a > x·s for x
// at, and one ulp around, the boundary a/s, where rounding decides.
func TestThreshold(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for range 100_000 {
		a := float64(float32(rng.ExpFloat64() * 100))
		s := layerNoise[rng.IntN(len(layerNoise))]
		th := threshold(a, s)
		for _, x := range []float64{a / s, math.Nextafter(a/s, 0), math.Nextafter(a/s, math.Inf(1)), th, math.Nextafter(th, 0)} {
			if (x < th) != (a > x*s) {
				t.Fatalf("a=%v s=%v x=%v: x < threshold %v is %v, a > x·s is %v", a, s, x, th, x < th, a > x*s)
			}
		}
	}
	if th := threshold(math.Inf(1), 0.2); !(th > math.MaxFloat64) {
		t.Errorf("threshold(+Inf) = %v, want +Inf", th)
	}
	if th := threshold(math.NaN(), 0.2); th != 0 {
		t.Errorf("threshold(NaN) = %v, want 0", th)
	}
}

// TestNoiseUnbiased: on pure Gaussian noise both estimators return σ. MRS
// clipping alone reads ~2% low; PixInsight's /0.974 corrects that.
func TestNoiseUnbiased(t *testing.T) {
	const w, h, sigma = 512, 512, 10.0
	r := rand.New(rand.NewPCG(1, 2))
	pix := make([]float32, w*h)
	for i := range pix {
		pix[i] = float32(1000 + sigma*r.NormFloat64())
	}
	bg, std := SkyStats(pix)
	mrs, _ := NoiseMRS(pix, w, h, bg, std)
	ks := kSigmaNoise(pix, w, h)
	if !(math.Abs(mrs-sigma) < 0.01*sigma) || !(math.Abs(ks-sigma) < 0.02*sigma) {
		t.Errorf("MRS %.3f, k-sigma %.3f, want %.0f", mrs, ks, sigma)
	}
}
