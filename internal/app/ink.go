package app

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// The backdrop takes its colors from the cover, so its brightness follows
// the cover's: a pale sleeve gives a pale backdrop, and white lyrics over
// it all but vanish. These return what reads over the backdrop a cover of
// tint makes, so that a pale song still shows its words.

// backdropIsLight reports whether the backdrop of a cover of tint is light
// enough that white would not read over it: the cover's own brightness,
// weighted as the eye sees it, and lifted the way the backdrop lifts it.
func backdropIsLight(tint ui.Color) bool {
	return luminance(tint) > 0.58
}

// luminance is how bright a color looks, as the eye weights the channels:
// green carries most of it, blue least.
func luminance(c ui.Color) float64 {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// lyricInk is the color the stage and Now Playing set their words in over
// a backdrop of tint: white on a dark one, and a deep shade of the cover's
// own hue on a light one, where white would be lost.
func lyricInk(tint ui.Color) ui.Color {
	if !backdropIsLight(tint) {
		return white
	}
	h, s, _ := hsv(float64(tint.R)/255, float64(tint.G)/255, float64(tint.B)/255)
	// Nearly black, carrying the cover's own hue and a share of its
	// saturation: a pale sleeve has little saturation to carry, so taking
	// a share keeps a violet sleeve's words violet and a gray one's gray,
	// rather than inventing a color the cover never had.
	c := hsvColor(h, min(max(s*2.2, 0.12), 0.42), 0.16)
	return c
}

// inkFaded is v, the ink at the opacity alpha: the same choice as
// lyricInk, faded for the lines not being sung, which read at a lower
// opacity over either color.
func inkFaded(ink ui.Color, alpha float32) ui.Color { return ink.Alpha(alpha) }

// backdropScrim is a wash of the ink's opposite laid over the backdrop,
// where the words sit, to keep them apart from it: on a far-too-bright
// cover, even a dark ink needs the backdrop dimmed under it. It returns
// the color and how strong to lay it, 0 for a backdrop the ink already
// reads over.
func backdropScrim(tint ui.Color) (ui.Color, float32) {
	if !backdropIsLight(tint) {
		return ui.Color{}, 0
	}
	// A pale backdrop behind dark words still needs help where the cover is
	// busiest: a light but not white wash keeps the words' own shade ahead
	// of it.
	h, s, _ := hsv(float64(tint.R)/255, float64(tint.G)/255, float64(tint.B)/255)
	lum := luminance(tint)
	strength := float32(math.Min(0.26, (lum-0.58)*0.9))
	if strength <= 0 {
		return ui.Color{}, 0
	}
	return hsvColor(h, s*0.5, 0.97), strength
}
