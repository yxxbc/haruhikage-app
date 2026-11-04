package player

import (
	"io"
	"math"
	"testing"
)

// fakeStream is a stream of a sine, for the equalizer's wrapper: it
// decodes what it is given, block by block, and records the spectrum and
// download it was asked for, which the wrapper must pass through.
type fakeStream struct {
	samples  []float32
	channels int
	rate     int
	at       int
	closed   bool
	// mu is not needed: the wrapper is run by one goroutine here.
	spectrumAsked bool
	downloadAsked bool
}

func (f *fakeStream) SampleRate() int { return f.rate }
func (f *fakeStream) Channels() int   { return f.channels }
func (f *fakeStream) Len() int64      { return int64(len(f.samples) / f.channels) }

func (f *fakeStream) Read(buf []float32) (int, error) {
	n := copy(buf, f.samples[f.at:])
	f.at += n
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (f *fakeStream) SetPosition(frame int64) error {
	f.at = int(frame) * f.channels
	return nil
}

func (f *fakeStream) Close() error { f.closed = true; return nil }

func (f *fakeStream) source() (*download, *spectrum) {
	f.downloadAsked, f.spectrumAsked = true, true
	return nil, nil
}

// sine makes a stereo sine of freq over frames.
func sine(freq, rate float64, frames int) []float32 {
	out := make([]float32, frames*2)
	w := 2 * math.Pi * freq / rate
	for i := 0; i < frames; i++ {
		v := float32(math.Sin(w * float64(i)))
		out[i*2], out[i*2+1] = v, v
	}
	return out
}

// A wrapped stream decodes what the stream beneath it does, filtered: the
// wrapper is a decoder for the player, and nothing of what it wraps is
// lost.
func TestEQStreamDecodes(t *testing.T) {
	const rate = 48000
	raw := &fakeStream{samples: sine(1000, rate, 8192), channels: 2, rate: rate}
	e := newEQStream(raw, []float64{0, 0, 0}, []float64{100, 1000, 10000})

	if e.SampleRate() != rate || e.Channels() != 2 {
		t.Fatalf("rate %d, channels %d", e.SampleRate(), e.Channels())
	}
	if e.Len() != raw.Len() {
		t.Errorf("length %d, want %d", e.Len(), raw.Len())
	}
	var got int
	buf := make([]float32, 1024)
	for {
		n, err := e.Read(buf)
		got += n
		if err != nil {
			break
		}
	}
	if got != len(raw.samples) {
		t.Errorf("read %d frames, want %d", got, len(raw.samples))
	}
}

// The wrapper hands the player the download and the spectrum of the stream
// beneath: the cache, the progress and the visualizers read them as they
// did, through it.
func TestEQStreamPassesSource(t *testing.T) {
	raw := &fakeStream{samples: sine(440, 48000, 1024), channels: 2, rate: 48000}
	e := newEQStream(raw, []float64{0}, []float64{1000})
	s, ok := e.(*eqStream)
	if !ok {
		t.Fatalf("wrapped to %T", e)
	}
	s.source()
	if !raw.downloadAsked || !raw.spectrumAsked {
		t.Error("the wrapper answered for the stream instead of passing it on")
	}
	// Closing the wrapper closes the stream beneath.
	if err := e.Close(); err != nil || !raw.closed {
		t.Errorf("close: %v, closed %v", err, raw.closed)
	}
}

// A stream that cannot be equalized is not wrapped at all: an unknown rate
// has no coefficients to compute, and a wrapper that did nothing but pass
// the sound would still colour it by rounding.
func TestEQStreamSkips(t *testing.T) {
	raw := &fakeStream{samples: sine(440, 44100, 128), channels: 2, rate: 0}
	if e := newEQStream(raw, []float64{0}, []float64{1000}); e != stream(raw) {
		t.Errorf("a stream with no rate wrapped to %T", e)
	}
	raw2 := &fakeStream{samples: sine(440, 44100, 128), channels: 2, rate: 44100}
	if e := newEQStream(raw2, nil, nil); e != stream(raw2) {
		t.Errorf("no bands wrapped to %T", e)
	}
}

// A seek clears what the bands have heard, so the new point in the song is
// not rung into by the old one.
func TestEQStreamSeekResets(t *testing.T) {
	const rate = 48000
	raw := &fakeStream{samples: sine(1000, rate, 8192), channels: 2, rate: rate}
	e := newEQStream(raw, []float64{12, 0, 0}, []float64{100, 1000, 10000})
	s := e.(*eqStream)

	buf := make([]float32, 4096)
	if _, err := e.Read(buf); err != nil {
		t.Fatal(err)
	}
	// The bands have state now.
	if s.bands[0].z1 == [2]float64{} {
		t.Fatal("the bands held nothing after reading")
	}
	if err := e.SetPosition(0); err != nil {
		t.Fatal(err)
	}
	for i := range s.bands {
		if s.bands[i].z1 != [2]float64{} || s.bands[i].z2 != [2]float64{} {
			t.Errorf("band %d kept its state through a seek", i)
		}
	}
}

// The wrapper filters as the bands say: a band at +12 dB makes the tone at
// its frequency louder than the stream beneath it had it.
func TestEQStreamFilters(t *testing.T) {
	const rate = 48000
	const frames = 16384
	quiet := &fakeStream{samples: sine(1000, rate, frames), channels: 2, rate: rate}
	loud := newEQStream(&fakeStream{samples: sine(1000, rate, frames), channels: 2, rate: rate},
		[]float64{0, 12, 0}, []float64{100, 1000, 10000})

	level := func(s stream) float64 {
		buf := make([]float32, frames*2)
		n := 0
		for n < len(buf) {
			k, _ := s.Read(buf[n:])
			if k == 0 {
				break
			}
			n += k
		}
		// The last quarter, past the start-up.
		start := n - n/4
		sum := 0.0
		for i := start; i < n; i += 2 {
			sum += float64(buf[i]) * float64(buf[i])
		}
		return math.Sqrt(sum / float64((n-start)/2))
	}

	before, after := level(quiet), level(loud)
	ratio := after / before
	// +12 dB is about 4x in amplitude.
	if math.Abs(ratio-4) > 0.15 {
		t.Errorf("a +12 dB band made the tone %.2fx, want about 4x", ratio)
	}
}

// A wrapper that is never asked for an equalizer is not there at all: the
// player gets the stream it made, and plays it as it always did. A wrapper
// that passed the sound through would still colour it, at every sample.
func TestEQStreamOffIsTheSameStream(t *testing.T) {
	// The real streams need a download; the wrapper's own decision is what
	// this checks, on a stream that answers for itself.
	fake := &fakeStream{samples: sine(440, 44100, 256), channels: 2, rate: 44100}
	if got := newEQStream(fake, nil, nil); got != stream(fake) {
		t.Errorf("no bands gave %T, want the stream itself", got)
	}
	var empty []float64
	if got := newEQStream(fake, empty, empty); got != stream(fake) {
		t.Errorf("empty bands gave %T, want the stream itself", got)
	}
}
