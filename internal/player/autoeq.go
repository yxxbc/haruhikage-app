package player

import "math"

// The automatic curve: where a song's own sound is short of something,
// the equalizer gives it back, and where it already has too much, takes a
// little off. It is measured from the decoded audio as it plays, so a
// quiet, bass-light master and a loud, bass-heavy one get different bands
// without anything being known about the song but what it sounds like.

// autoEQAnalyzer measures the energy of the bands an equalizer works on,
// over the samples played, through cascaded one-pole filters: four of them
// in a row on each side roll off steeply enough to tell a treble-led
// master from an even one, which one alone cannot.
type autoEQAnalyzer struct {
	rate     float64
	channels int
	// low, mid and high hold the running energy of each range, and all of
	// what came in, so the shares can be taken.
	low, mid, high, all float64
	// frames counts what has been measured, so a short sample of a song
	// can be told from a long one.
	frames int64

	// Four one-pole low passes in a row, and the same for the high pass,
	// per channel: the cascade is what makes the split steep.
	lowState  [2][4]float64
	highState [2][4]float64
	kLow      float64
	kHigh     float64
}

// autoEQMinFrames is how much of a song is measured before its curve is
// used: a few seconds say where its energy lies, and the first samples of
// a song are not all of it.
const autoEQMinFrames = 6 * 44100

// listen measures buf, interleaved, of the given rate and channels.
func (a *autoEQAnalyzer) listen(buf []float32, rate, channels int) {
	if rate <= 0 || channels <= 0 {
		return
	}
	if channels > 2 {
		channels = 2
	}
	if a.rate != float64(rate) || a.channels != channels {
		a.rate, a.channels = float64(rate), channels
		a.kLow = 1 - math.Exp(-2*math.Pi*200/a.rate)
		a.kHigh = 1 - math.Exp(-2*math.Pi*4000/a.rate)
	}

	for i := 0; i+channels <= len(buf); i += channels {
		for c := 0; c < channels; c++ {
			x := float64(buf[i+c])

			// Four one-pole low passes in a row at the bass corner, and the
			// same at the treble one: each stage takes 6 dB an octave, so
			// four take 24, which is steep enough to separate the bands.
			lo := x
			for s := 0; s < 4; s++ {
				a.lowState[c][s] += a.kLow * (lo - a.lowState[c][s])
				lo = a.lowState[c][s]
			}
			hiSource := x
			left := x
			for s := 0; s < 4; s++ {
				a.highState[c][s] += a.kHigh * (left - a.highState[c][s])
				left = a.highState[c][s]
			}
			hi := hiSource - left
			// What the treble cut left, less the bass: the middle.
			mid := left - lo

			a.low += lo * lo
			a.mid += mid * mid
			a.high += hi * hi
			a.all += x * x
		}
		a.frames++
	}
}

// shares returns the low, middle and high shares of the energy measured,
// 0 to 1 each, and whether enough has been measured to say.
func (a *autoEQAnalyzer) shares() (low, mid, high float64, ok bool) {
	if a.all <= 0 || a.frames < autoEQMinFrames {
		return 0, 0, 0, false
	}
	// Each band's share of the three together: the bands' filters add
	// gain of their own, so the raw input's energy is not the total.
	sum := a.low + a.mid + a.high
	if sum <= 0 {
		return 0, 0, 0, false
	}
	return a.low / sum, a.mid / sum, a.high / sum, true
}

// autoEQGains returns the curve a song's own sound asks for: the bands
// lifted where its energy is short of even, cut where it has too much. A
// song whose energy is evenly spread asks for the flat curve, which is
// what the listener gets for one that measures evenly.
//
// The shares are compared against what an even mix has, a third each, and
// the difference becomes decibels, held to a gentle range: this is meant
// to even out a master, not to remix it.
func autoEQGains(analyzer *autoEQAnalyzer) []float64 {
	low, _, high, ok := analyzer.shares()
	if !ok {
		return nil
	}
	// How far each range is from its even third, as a factor: -1 when it
	// has none of it, +1 when it has all.
	excess := func(share float64) float64 {
		return math.Max(-1, math.Min(1, (share-1.0/3)/(2.0/3)))
	}
	// A range short of its third is lifted, one over it is cut, at up to
	// 5 dB: enough to hear on a laptop's speakers, too little to wreck a
	// master that was right already.
	const reach = 5.0
	lo := -excess(low) * reach
	hi := -excess(high) * reach
	_ = lo
	_ = hi

	// The ten bands, shaped from those two: the lowest bands get the low
	// range's lift, the highest the high range's, and the middle of the
	// curve is left to fall between them, so the curve is smooth rather
	// than a step at each corner.
	gains := make([]float64, len(eqFreqs))
	for i := range gains {
		// Where the band sits, 0 at the lowest, 1 at the highest.
		t := float64(i) / float64(len(gains)-1)
		switch {
		case t < 0.3:
			gains[i] = lo * (1 - t/0.3*0.5)
		case t > 0.7:
			gains[i] = hi * ((t - 0.7) / 0.3 * 0.5)
		}
	}
	return clampGains(gains)
}

// analyzeAutoEQ decodes the start of a song apart from what plays, and
// returns the curve its own sound asks for: the equalizer's automatic
// setting, measured from the audio rather than guessed from a genre tag
// the app never had.
//
// It reads through a stream of its own, as transcribing an episode does,
// so nothing of what plays is disturbed, and it stops once enough has
// been measured.
func analyzeAutoEQ(dl *download, durationMs int64) []float64 {
	s, err := openShared(dl, durationMs)
	if err != nil {
		return nil
	}
	defer s.Close()

	rate, channels := s.SampleRate(), max(1, s.Channels())
	if rate <= 0 {
		return nil
	}
	// Enough of the song to say where its energy lies: about ten seconds,
	// which is a few bars of anything.
	need := int64(10 * rate)
	if need <= 0 {
		return nil
	}
	analyzer := &autoEQAnalyzer{}
	buf := make([]float32, 4096)
	var read int64
	for read < need {
		n, err := s.Read(buf)
		if n > 0 {
			analyzer.listen(buf[:n], rate, channels)
			read += int64(n / channels)
		}
		if err != nil || n == 0 {
			break
		}
	}
	return autoEQGains(analyzer)
}
