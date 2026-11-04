package player

// eqStream wraps a stream to run its samples through an equalizer before
// the player has them: a decoder for package audio, which filters each
// block as it decodes it, and the download and spectrum of the stream
// beneath, which the player asks for as it does of any stream.
//
// It goes around the decoder rather than into the mixer because the mixer
// is the framework's, and it mixes every sound in the app; these bands are
// this app's, over its music, and nowhere else.
type eqStream struct {
	stream
	// bands are in series, lowest first; none of them passes the sound
	// unchanged only in that it cannot be bit-exact, which is why an
	// equalizer that is off is not this at all.
	bands []biquad
	// channels is what the stream decodes, and rate its sample rate: the
	// bands were made for it, and are remade if the stream changes them.
	channels int
	rate     int
}

// newEQStream wraps s in bands made for its sample rate. A stream whose
// rate is not known yet, or has none, is not wrapped: there is nothing to
// compute coefficients against.
func newEQStream(s stream, gains []float64, freqs []float64) stream {
	rate := s.SampleRate()
	if rate <= 0 || len(gains) == 0 || len(gains) != len(freqs) {
		return s
	}
	e := &eqStream{stream: s, channels: max(1, s.Channels()), rate: rate}
	e.makeBands(gains, freqs)
	return e
}

// makeBands makes the bands in series for the stream's rate: the first
// and last shelves, the ones between peaks.
func (e *eqStream) makeBands(gains, freqs []float64) {
	e.bands = make([]biquad, len(gains))
	for i := range gains {
		shape := peaking
		switch i {
		case 0:
			shape = lowShelf
		case len(gains) - 1:
			shape = highShelf
		}
		e.bands[i].set(shape, freqs[i], float64(e.rate), gains[i], bandQ)
	}
}

// bandQ is how wide a band is, as Web Audio's equalizer graphs use: too
// narrow and the bands leave gaps between them, too wide and they overlap
// into one another's work.
const bandQ = 1.4

func (e *eqStream) Read(buf []float32) (int, error) {
	n, err := e.stream.Read(buf)
	if n > 0 {
		// The stream may have told us its rate by now, as a file that
		// states it late does.
		if r := e.stream.SampleRate(); r > 0 && r != e.rate {
			e.rate = r
		}
		for i := range e.bands {
			e.bands[i].run(buf[:n], e.channels)
		}
	}
	return n, err
}

func (e *eqStream) SetPosition(frame int64) error {
	// The bands' state belongs to where the stream was: a seek lands on a
	// new point in the song, and the old state would ring into it.
	for i := range e.bands {
		e.bands[i].reset()
	}
	return e.stream.SetPosition(frame)
}
