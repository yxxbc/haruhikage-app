package player

import (
	"math"
	"testing"
)

// A song whose energy is all bass asks for bass cut and treble lift; one
// whose energy is all treble asks for the other way round. That is the
// whole idea: the curve goes where the sound is not.
func TestAutoEQGains(t *testing.T) {
	const rate = 48000

	// A song of a low tone, measured.
	bass := &autoEQAnalyzer{}
	tone := sine(60, rate, 48000*8)
	bass.listen(tone, rate, 2)
	low, _, high, ok := bass.shares()
	if !ok {
		t.Fatal("two seconds of sound did not measure")
	}
	if low <= high {
		t.Fatalf("a 60 Hz tone measured low %.3f, high %.3f", low, high)
	}
	// Too much bass: the curve cuts it.
	gains := autoEQGains(bass)
	if len(gains) != len(eqFreqs) {
		t.Fatalf("%d gains", len(gains))
	}
	if gains[0] >= 0 {
		t.Errorf("a bass-heavy song lifts its bass: %.2f dB", gains[0])
	}
	if gains[len(gains)-1] <= 0 {
		t.Errorf("a bass-heavy song does not lift its treble: %.2f dB", gains[len(gains)-1])
	}

	// A song of a high tone: the other way round. Its high share is lower
	// than a bass tone's low share, since a one-pole split rolls off gently
	// above its corner, so the check is on the direction, not the size.
	treble := &autoEQAnalyzer{}
	treble.listen(sine(12000, rate, 48000*8), rate, 2)
	_, _, trebleHigh, _ := treble.shares()
	if trebleHigh <= 0.1 {
		t.Fatalf("a 12 kHz tone measured high %.3f", trebleHigh)
	}
	gains = autoEQGains(treble)
	if gains[0] <= 0 {
		t.Errorf("a treble-heavy song does not lift its bass: %.2f dB", gains[0])
	}
	if gains[len(gains)-1] >= 0 {
		t.Errorf("a treble-heavy song lifts its treble: %.2f dB", gains[len(gains)-1])
	}
}

// An even mix asks for almost nothing: a song that was mastered well is
// left alone rather than remixed.
func TestAutoEQEven(t *testing.T) {
	const rate = 48000
	a := &autoEQAnalyzer{}
	// Noise spread over the whole band: what an even master sounds like.
	n := 48000 * 8
	buf := make([]float32, n*2)
	var seed uint32 = 12345
	for i := range buf {
		seed = seed*1664525 + 1013904223
		buf[i] = float32(seed>>16)/32768 - 1
	}
	a.listen(buf, rate, 2)
	gains := autoEQGains(a)
	if len(gains) != len(eqFreqs) {
		t.Fatalf("%d gains", len(gains))
	}
	for i, g := range gains {
		if math.Abs(g) > 3.5 {
			t.Errorf("band %d moved %.2f dB on an even mix", i, g)
		}
	}
}

// A song too short to say anything is not measured, and the player keeps
// the flat curve rather than one taken from a fragment.
func TestAutoEQTooShort(t *testing.T) {
	a := &autoEQAnalyzer{}
	a.listen(sine(100, 48000, 4800), 48000, 2)
	if _, _, _, ok := a.shares(); ok {
		t.Error("a tenth of a second measured as a song")
	}
	if g := autoEQGains(a); g != nil {
		t.Errorf("a fragment gave %v", g)
	}
}

// The automatic setting plays a song it did not measure flat: a curve from
// another song is worse than none.
func TestAutoEQUnmeasuredIsFlat(t *testing.T) {
	p := New(func(f func()) { f() })
	p.Equalizer = func() bool { return true }
	p.Preset = func() string { return "auto" }

	if p.autoGains != nil {
		t.Fatal("a new player has gains measured")
	}
	gains := p.EQGains()
	if len(gains) != len(eqFreqs) {
		t.Fatalf("%d gains", len(gains))
	}
	for i, g := range gains {
		if g != 0 {
			t.Errorf("band %d is %.2f dB before anything was measured", i, g)
		}
	}
}
