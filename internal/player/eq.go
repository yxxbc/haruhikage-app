package player

import (
	"math"

	"github.com/egoist/mygo/audio"

)

// The equalizer's ten bands, and the presets it ships, as Folia's
// (src/utils/audioEqualizer.ts, audioPresets.ts): the same frequencies, so
// that a curve chosen here shapes the sound the way the same curve does
// there.
var eqFreqs = []float64{31, 62, 125, 250, 500, 1000, 2000, 4000, 8000, 16000}

// eqBandLabels name the bands for the settings, as an equalizer draws them.
var eqBandLabels = []string{"31", "62", "125", "250", "500", "1k", "2k", "4k", "8k", "16k"}

// eqPreset is a curve the listener can choose: ten gains in decibels, one
// per band, in the order of eqFreqs.
type eqPreset struct {
	id, label string
	gains     []float64
}

// eqPresets are the curves, the flat one first: "flat" is what a listener
// who never chose one has, and switching it on must not change a thing.
//
// They are Folia's gains (src/utils/audioPresets.ts), less its effect
// chains: those add hiss, drive and bit crush, which live in its own audio
// graph and not in a filter.
var eqPresets = []eqPreset{
	{"flat", "平直", []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
	{"bass", "低音增强", []float64{6, 5, 3, 1, 0, -1, -1, 0, 1, 2}},
	{"vocal", "人声", []float64{-2, -1, 0, 2, 4, 4, 3, 1, 0, -1}},
	{"hall", "厅堂", []float64{1, 1, 0, 0, 0, 0, 1, 1, 1, 0}},
	{"radio", "收音机", []float64{-12, -8, -3, 2, 4, 4, 2, -3, -8, -12}},
	{"lofi", "低保真", []float64{-4, -2, 0, 2, 3, 2, 0, -3, -7, -10}},
}

// eqPresetByID returns the preset with id, or the flat one for an id no
// preset has: a settings file may name one this version dropped.
func eqPresetByID(id string) eqPreset {
	for _, p := range eqPresets {
		if p.id == id {
			return p
		}
	}
	return eqPresets[0]
}

// eqGains returns the gains the equalizer is set to: the preset's, or the
// listener's own. A band's gain is clamped to the range the filter was
// tested over.
func (p *Player) eqGains() []float64 {
	if !p.EQOn() {
		return nil
	}
	if p.EQ != nil {
		if g := p.EQ(); len(g) == len(eqFreqs) {
			return clampGains(g)
		}
	}
	return eqPresetByID(p.EQPreset()).gains
}

// EQOn reports whether the equalizer is switched on, as the settings say.
func (p *Player) EQOn() bool { return p.Equalizer != nil && p.Equalizer() }

// EQPreset returns the preset chosen, as the settings say.
func (p *Player) EQPreset() string {
	if p.Preset == nil {
		return "flat"
	}
	return p.Preset()
}

// clampGains holds each gain to the range an equalizer offers: past it, a
// band is loud enough to clip everything after it.
func clampGains(g []float64) []float64 {
	out := make([]float64, len(g))
	for i, v := range g {
		if math.IsNaN(v) {
			v = 0
		}
		out[i] = math.Min(12, math.Max(-12, v))
	}
	return out
}

// equalized wraps s in the equalizer's bands, if it is on: the player gets
// a decoder that filters, while p.stream keeps the stream itself, for the
// download's progress and for decoding again, unfiltered, to transcribe.
func (p *Player) equalized(s stream) audio.Decoder {
	gains := p.eqGains()
	if len(gains) == 0 {
		return s
	}
	return newEQStream(s, gains, eqFreqs)
}

// EQGains returns the gains the equalizer is set to now, for the settings
// to draw: the preset's curve, or the listener's own.
func (p *Player) EQGains() []float64 {
	if g := p.eqGains(); len(g) > 0 {
		return g
	}
	return eqPresetByID(p.EQPreset()).gains
}

// eqNowPlayingGain is the gain the equalizer's curve takes from a track as
// loud as the others, so that switching a preset on does not make the
// music jump: the curve's own loudest band, which a flat mix is lifted or
// cut by. A curve that only cuts leaves the volume as it was.
func eqNowPlayingGain(gains []float64) float64 {
	peak := 0.0
	for _, g := range gains {
		peak = math.Max(peak, g)
	}
	if peak <= 0 {
		return 1
	}
	// An equalizer that lifts a band by 6 dB makes the mix louder by about
	// that much; taking it back keeps the volume where the listener left
	// it, which is what automatic gain is for.
	return math.Pow(10, -peak/20)
}

// EQPresetList is the curves the listener can choose, in the order they
// are shown: their ids and names, without the gains, which only the
// player needs.
type EQPresetInfo struct{ ID, Label string }

// EQPresets returns the curves the settings offer.
func EQPresets() []EQPresetInfo {
	out := make([]EQPresetInfo, len(eqPresets))
	for i, p := range eqPresets {
		out[i] = EQPresetInfo{ID: p.id, Label: p.label}
	}
	return out
}
