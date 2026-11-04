package player

import (
	"math"
	"testing"
)

// measure runs a sine of freq through f for n frames at rate, and returns
// the amplitude of what comes out over the last half: a band with gain
// g dB multiplies its own frequency by 10^(g/20).
func measure(f *biquad, freq, rate float64, n int) float64 {
	const channels = 2
	buf := make([]float32, n*channels)
	w := 2 * math.Pi * freq / rate
	for i := 0; i < n; i++ {
		v := float32(math.Sin(w * float64(i)))
		buf[i*channels] = v
		buf[i*channels+1] = v
	}
	f.run(buf, channels)

	// The amplitude over the last quarter, past the filter's start-up: an
	// RMS times root two, which does not depend on where in the cycle the
	// window happens to fall, as a peak does when the signal is short.
	start := n - n/4
	sum := 0.0
	for i := start; i < n; i++ {
		v := float64(buf[i*channels])
		sum += v * v
	}
	return math.Sqrt(2 * sum / float64(n-start))
}

// A peaking band lifts the frequency it is set to by its gain, and leaves
// a frequency well away from it alone: the thing an equalizer is for.
func TestBiquadPeaking(t *testing.T) {
	const rate = 48000
	for _, gain := range []float64{-12, -6, 0, 6, 12} {
		f := &biquad{}
		f.set(peaking, 1000, rate, gain, 1.4)
		got := measure(f, 1000, rate, rate/4)
		want := math.Pow(10, gain/20)
		if math.Abs(got-want) > want*0.02 {
			t.Errorf("%+.0f dB at 1 kHz: %.3f, want %.3f", gain, got, want)
		}
	}
	// A band at 1 kHz does not change a tone at 20 Hz or at 15 kHz.
	f := &biquad{}
	f.set(peaking, 1000, rate, 12, 1.4)
	for _, freq := range []float64{20, 15000} {
		if got := measure(f, freq, rate, rate/4); math.Abs(got-1) > 0.03 {
			t.Errorf("%v Hz through a 1 kHz band: %.3f, want 1", freq, got)
		}
	}
}

// A low shelf lifts what is below its frequency and leaves what is above;
// a high shelf the other way round.
func TestBiquadShelves(t *testing.T) {
	const rate = 48000
	low := &biquad{}
	low.set(lowShelf, 200, rate, 9, 0.7)
	if got := measure(low, 40, rate, rate/4); math.Abs(got-math.Pow(10, 9/20.0)) > 0.2 {
		t.Errorf("40 Hz below a 200 Hz shelf at +9 dB: %.3f", got)
	}
	if got := measure(low, 8000, rate, rate/4); math.Abs(got-1) > 0.1 {
		t.Errorf("8 kHz above a 200 Hz shelf: %.3f, want 1", got)
	}

	high := &biquad{}
	high.set(highShelf, 6000, rate, -9, 0.7)
	if got := measure(high, 14000, rate, rate/4); math.Abs(got-math.Pow(10, -9/20.0)) > 0.15 {
		t.Errorf("14 kHz above a 6 kHz shelf at -9 dB: %.3f", got)
	}
	if got := measure(high, 300, rate, rate/4); math.Abs(got-1) > 0.1 {
		t.Errorf("300 Hz below a 6 kHz shelf: %.3f, want 1", got)
	}
}

// A band at 0 dB passes the sound as it came, to within rounding: an
// equalizer switched on but flat must not colour anything.
func TestBiquadFlat(t *testing.T) {
	const rate = 48000
	const n, channels = 4096, 2
	orig := make([]float32, n*channels)
	for i := range orig {
		orig[i] = float32(math.Sin(float64(i) * 0.31))
	}
	buf := append([]float32(nil), orig...)

	// Every band an equalizer has, all at 0 dB, in series.
	for _, freq := range []float64{31, 62, 125, 250, 500, 1000, 2000, 4000, 8000, 16000} {
		f := &biquad{}
		f.set(peaking, freq, rate, 0, 1.4)
		f.run(buf, channels)
	}
	var worst float64
	for i := range orig {
		worst = math.Max(worst, math.Abs(float64(buf[i]-orig[i])))
	}
	if worst > 0.001 {
		t.Errorf("flat equalizer changed a sample by %v", worst)
	}
}

// A band asked for a frequency past half the sample rate does not ring at
// a frequency the sound cannot have: it passes, rather than folding.
func TestBiquadAboveNyquist(t *testing.T) {
	f := &biquad{}
	f.set(peaking, 30000, 48000, 12, 1.4)
	if math.IsNaN(f.b0) || math.IsInf(f.b0, 0) {
		t.Fatalf("coefficients %v %v %v", f.b0, f.b1, f.b2)
	}
	if got := measure(f, 1000, 48000, 48000/4); math.Abs(got-1) > 0.05 {
		t.Errorf("a band above Nyquist changed a 1 kHz tone to %.3f", got)
	}
}

// A band keeps its state from one block of samples to the next: the
// player feeds it a buffer at a time, and a filter that forgot what it had
// heard between them would click at every buffer's edge.
func TestBiquadAcrossBlocks(t *testing.T) {
	const rate, channels = 48000, 2
	const total, block = 8192, 512

	// One run, a block at a time.
	f := &biquad{}
	f.set(peaking, 400, rate, 15, 1.4)
	var blocked []float32
	for off := 0; off < total; off += block {
		buf := make([]float32, block*channels)
		for i := 0; i < block; i++ {
			v := float32(math.Sin(2 * math.Pi * 400 * float64(off+i) / rate))
			buf[i*channels], buf[i*channels+1] = v, v
		}
		f.run(buf, channels)
		blocked = append(blocked, buf...)
	}

	// The same run, all at once.
	g := &biquad{}
	g.set(peaking, 400, rate, 15, 1.4)
	whole := make([]float32, total*channels)
	for i := 0; i < total; i++ {
		v := float32(math.Sin(2 * math.Pi * 400 * float64(i) / rate))
		whole[i*channels], whole[i*channels+1] = v, v
	}
	g.run(whole, channels)

	var worst float64
	for i := range whole {
		worst = math.Max(worst, math.Abs(float64(blocked[i]-whole[i])))
	}
	if worst > 1e-6 {
		t.Errorf("a block at a time differs from all at once by %v", worst)
	}
}

// A band reset for a new song starts from silence rather than ringing out
// the song before it.
func TestBiquadReset(t *testing.T) {
	f := &biquad{}
	f.set(peaking, 1000, 48000, 12, 1.4)
	measure(f, 1000, 48000, 4096)
	f.reset()
	if f.z1 != [2]float64{} || f.z2 != [2]float64{} {
		t.Errorf("state after reset: %v %v", f.z1, f.z2)
	}
}
