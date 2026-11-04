package player

import "math"

// A biquad is the second-order filter an equalizer's band is: two poles and
// two zeros, run as a transposed direct form II, which holds what it has
// heard in two numbers per channel rather than filtering its own output
// twice over.
//
// Its coefficients come from the Audio EQ Cookbook's formulas, as Web
// Audio's BiquadFilterNode computes them, so a band set here sounds as the
// same band set there does.
type biquad struct {
	// b0, b1, b2 and a1, a2 are the coefficients; a0 is divided through,
	// as the cookbook writes them.
	b0, b1, b2, a1, a2 float64
	// z1, z2 are the filter's state, one pair per channel.
	z1, z2 [2]float64
}

// bandShape is how a band passes or cuts the frequencies around it, as
// BiquadFilterNode's type does.
type bandShape int

const (
	// lowShelf lifts or drops everything below its frequency.
	lowShelf bandShape = iota
	// peaking lifts or drops a range around its frequency, as wide as its Q.
	peaking
	// highShelf lifts or drops everything above its frequency.
	highShelf
)

// set makes the filter the shape's at frequency hertz, for a sample rate,
// by gainDB, Q wide where the shape has a width.
func (f *biquad) set(shape bandShape, freq, rate, gainDB, q float64) {
	// Above half the rate there is nothing to filter: a band there would
	// fold, and ring at a frequency the sound does not have.
	freq = math.Min(freq, rate*0.475)
	a := math.Pow(10, gainDB/40)
	w := 2 * math.Pi * freq / rate
	cos, sin := math.Cos(w), math.Sin(w)
	alpha := sin / (2 * q)

	switch shape {
	case lowShelf:
		// The cookbook's shelf, which needs the square root of a.
		s := 2 * math.Sqrt(a) * alpha
		f.b0 = a * ((a+1) - (a-1)*cos + s)
		f.b1 = 2 * a * ((a-1) - (a+1)*cos)
		f.b2 = a * ((a+1) - (a-1)*cos - s)
		f.a1 = -2 * ((a-1) + (a+1)*cos)
		f.a2 = (a+1) + (a-1)*cos - s
		a0 := (a+1) + (a-1)*cos + s
		f.normalize(a0)
	case highShelf:
		s := 2 * math.Sqrt(a) * alpha
		f.b0 = a * ((a+1) + (a-1)*cos + s)
		f.b1 = -2 * a * ((a-1) + (a+1)*cos)
		f.b2 = a * ((a+1) + (a-1)*cos - s)
		f.a1 = 2 * ((a-1) - (a+1)*cos)
		f.a2 = (a+1) - (a-1)*cos - s
		a0 := (a+1) - (a-1)*cos + s
		f.normalize(a0)
	default:
		f.b0 = 1 + alpha*a
		f.b1 = -2 * cos
		f.b2 = 1 - alpha*a
		f.a1 = -2 * cos
		f.a2 = 1 - alpha/a
		a0 := 1 + alpha/a
		f.normalize(a0)
	}
}

// normalize divides every coefficient by a0, which the formulas carry and
// the difference equation does not.
func (f *biquad) normalize(a0 float64) {
	if a0 == 0 || math.IsNaN(a0) || math.IsInf(a0, 0) {
		// A band at a frequency where the formulas divide by nothing: it
		// passes the sound instead of ringing.
		f.b0, f.b1, f.b2, f.a1, f.a2 = 1, 0, 0, 0, 0
		return
	}
	f.b0 /= a0
	f.b1 /= a0
	f.b2 /= a0
	f.a1 /= a0
	f.a2 /= a0
}

// run filters n frames of buf, interleaved, in place, at the sample rate
// the coefficients were made for.
func (f *biquad) run(buf []float32, channels int) {
	for i := 0; i+channels <= len(buf); i += channels {
		for c := 0; c < channels && c < 2; c++ {
			x := float64(buf[i+c])
			y := f.b0*x + f.z1[c]
			f.z1[c] = f.b1*x - f.a1*y + f.z2[c]
			f.z2[c] = f.b2*x - f.a2*y
			// A filter can overshoot what the samples come in at; the
			// player clips anyway, and clipping here would keep a loud
			// band from wrecking the ones after it.
			buf[i+c] = float32(y)
		}
	}
}

// reset clears what the filter has heard, for a new song: its last state
// would otherwise ring into the first samples of the next.
func (f *biquad) reset() { f.z1, f.z2 = [2]float64{}, [2]float64{} }
