package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// White reads over a dark cover's backdrop and not over a pale one's: a
// pale sleeve was the case where the words vanished into it.
func TestLyricInk(t *testing.T) {
	dark := ui.RGB(30, 34, 60)
	pale := ui.RGB(226, 224, 240)

	if backdropIsLight(dark) {
		t.Error("a dark cover reads as light")
	}
	if !backdropIsLight(pale) {
		t.Error("a pale cover reads as dark")
	}
	// Over a dark backdrop the words stay white.
	if got := lyricInk(dark); got != white {
		t.Errorf("dark backdrop: ink %v, want white", got)
	}
	// Over a pale one they are dark, and still carry the cover's hue.
	ink := lyricInk(pale)
	if luminance(ink) > 0.2 {
		t.Errorf("pale backdrop: ink luminance %.3f, too light to read", luminance(ink))
	}
	// The shade is the cover's own, not gray: a violet sleeve gives violet
	// words.
	if ink.R >= ink.B {
		t.Errorf("pale violet backdrop: ink %v is not violet", ink)
	}
}

// The scrim dims a pale backdrop where the words sit, and not a dark one,
// which the words already read over.
func TestBackdropScrim(t *testing.T) {
	if _, s := backdropScrim(ui.RGB(30, 34, 60)); s != 0 {
		t.Errorf("a dark backdrop scrimmed at %v", s)
	}
	_, s := backdropScrim(ui.RGB(240, 238, 250))
	if s <= 0 || s > 0.3 {
		t.Errorf("a very pale backdrop scrimmed at %v", s)
	}
	// The paler the backdrop, the more it needs.
	_, pale := backdropScrim(ui.RGB(200, 200, 210))
	_, paler := backdropScrim(ui.RGB(250, 250, 252))
	if paler <= pale {
		t.Errorf("paler backdrop scrimmed less: %v against %v", paler, pale)
	}
}
