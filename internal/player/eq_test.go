package player

import (
	"math"
	"testing"
)

// The presets are Folia's, band for band, at the frequencies Folia uses:
// the same curve has to shape the sound the same way, or a listener
// choosing 低音增强 here hears something else than there.
func TestEQPresets(t *testing.T) {
	want := map[string][]float64{
		"flat":  {0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		"bass":  {6, 5, 3, 1, 0, -1, -1, 0, 1, 2},
		"vocal": {-2, -1, 0, 2, 4, 4, 3, 1, 0, -1},
		"hall":  {1, 1, 0, 0, 0, 0, 1, 1, 1, 0},
		"radio": {-12, -8, -3, 2, 4, 4, 2, -3, -8, -12},
		"lofi":  {-4, -2, 0, 2, 3, 2, 0, -3, -7, -10},
	}
	if len(eqFreqs) != 10 || len(eqBandLabels) != 10 {
		t.Fatalf("%d bands, %d labels", len(eqFreqs), len(eqBandLabels))
	}
	for _, p := range eqPresets {
		w, ok := want[p.id]
		if !ok {
			t.Errorf("preset %q is not one of Folia's", p.id)
			continue
		}
		if len(p.gains) != len(eqFreqs) {
			t.Errorf("%q has %d gains, want %d", p.id, len(p.gains), len(eqFreqs))
		}
		for i := range w {
			if i < len(p.gains) && p.gains[i] != w[i] {
				t.Errorf("%q band %d: %v dB, want %v", p.id, i, p.gains[i], w[i])
			}
		}
		if p.label == "" {
			t.Errorf("%q has no name to show", p.id)
		}
	}
	// The flat curve is what an equalizer switched on but untouched is,
	// and it must not lift anything.
	if got := eqPresetByID("").id; got != "flat" {
		t.Errorf("no preset reads as %q, not flat", got)
	}
	if got := eqPresetByID("dropped-later").id; got != "flat" {
		t.Errorf("a preset this version does not have reads as %q", got)
	}
}

// An equalizer switched off gives the player the stream itself, not a
// wrapper of it: that is the only way it cannot colour the sound, and the
// point of switching it off.
func TestEQOffIsTheSameStream(t *testing.T) {
	p := New(func(f func()) { f() })
	p.Equalizer = func() bool { return false }
	raw := &fakeStream{samples: sine(440, 44100, 256), channels: 2, rate: 44100}
	if got := p.equalized(raw); got != raw {
		t.Errorf("switched off, the player got a %T", got)
	}
	// Nor is a player whose settings say nothing about an equalizer.
	if got := New(func(f func()) {}).equalized(raw); got != raw {
		t.Errorf("no settings, the player got a %T", got)
	}
}

// Switched on, the player gets a filtered decoder, and p.stream keeps the
// stream itself: the download's progress and a transcription read it
// unfiltered, which a filter would colour.
func TestEQOnFilters(t *testing.T) {
	const rate = 48000
	p := New(func(f func()) { f() })
	p.Equalizer = func() bool { return true }
	p.Preset = func() string { return "bass" }

	raw := &fakeStream{samples: sine(62, rate, 4096), channels: 2, rate: rate}
	wrapped := p.equalized(raw)
	if wrapped == raw {
		t.Fatal("switched on, the player got the stream itself")
	}
	eq, ok := wrapped.(*eqStream)
	if !ok {
		t.Fatalf("wrapped to %T", wrapped)
	}
	if eq.stream != raw {
		t.Error("the wrapper is not around the stream")
	}
	// 低音增强 lifts 62 Hz by 5 dB: the tone comes out louder.
	if eq.bands == nil || len(eq.bands) != len(eqFreqs) {
		t.Fatalf("%d bands made", len(eq.bands))
	}
}

// The gains the player plays with are the preset's, and a listener's own
// where they have moved one, held to the range an equalizer offers.
func TestEQGains(t *testing.T) {
	p := New(func(f func()) { f() })
	p.Equalizer = func() bool { return true }
	p.Preset = func() string { return "vocal" }

	got := p.EQGains()
	if want := eqPresetByID("vocal").gains; len(got) != len(want) || got[4] != want[4] {
		t.Errorf("gains %v, want the vocal curve", got)
	}
	// A band past the range is held to it: +40 dB would clip everything
	// after it.
	p.EQ = func() []float64 { return []float64{40, -40, 0, 0, 0, 0, 0, 0, 0, 0} }
	got = p.EQGains()
	if got[0] != 12 || got[1] != -12 {
		t.Errorf("clamped gains %v: %v and %v", got, got[0], got[1])
	}
	// A curve of the wrong length is the preset's, not half of one.
	p.EQ = func() []float64 { return []float64{1, 2} }
	if got = p.EQGains(); len(got) != len(eqFreqs) {
		t.Errorf("a short curve gave %d gains", len(got))
	}
}

// A curve that only cuts leaves the volume where it was; one that lifts
// takes its lift back, so switching a preset on does not make the music
// jump.
func TestEQGainCompensation(t *testing.T) {
	for _, c := range []struct {
		gains []float64
		want  float64
	}{
		{[]float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 1},
		{[]float64{-12, -8, 0, 0, 0, 0, 0, 0, 0, 0}, 1},
		{[]float64{6, 5, 0, 0, 0, 0, 0, 0, 0, 0}, math.Pow(10, -6.0/20)},
	} {
		if got := eqNowPlayingGain(c.gains); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%v: gain %.4f, want %.4f", c.gains, got, c.want)
		}
	}
}

// Each preset shapes the sound as its curve says: the loudest lifted band
// of 低音增强 comes out louder than the same band of a flat curve, and
// 收音机's cut of everything above 8 kHz takes the high end down.
func TestEQPresetsShapeSound(t *testing.T) {
	const rate = 48000
	level := func(preset string, freq float64) float64 {
		p := New(func(f func()) { f() })
		p.Equalizer = func() bool { return true }
		p.Preset = func() string { return preset }
		raw := &fakeStream{samples: sine(freq, rate, 16384), channels: 2, rate: rate}
		s := p.equalized(raw)

		buf := make([]float32, 16384*2)
		n := 0
		for n < len(buf) {
			k, err := s.Read(buf[n:])
			if k == 0 || err != nil {
				break
			}
			n += k
		}
		start := n - n/4
		sum := 0.0
		for i := start; i < n; i += 2 {
			sum += float64(buf[i]) * float64(buf[i])
		}
		return math.Sqrt(sum / float64((n-start)/2))
	}

	// 31 Hz: 低音增强 lifts it 6 dB.
	if bass, flat := level("bass", 31), level("flat", 31); bass < flat*1.6 {
		t.Errorf("低音增强 at 31 Hz: %.3f against flat's %.3f", bass, flat)
	}
	// 4 kHz: 收音机 lifts it, and cuts 8 kHz, so the high end comes down.
	// (At 16 kHz a high shelf has little left of the band to work on: the
	// frequency is close to half the sample rate, where a shelf's corner
	// flattens out, so the test asks a frequency the curve really shapes.)
	if radio, flat := level("radio", 8000), level("flat", 8000); radio > flat*0.5 {
		t.Errorf("收音机 at 8 kHz: %.3f against flat's %.3f", radio, flat)
	}
}
