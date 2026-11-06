package app

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// NM_FRAMES captures the existing pane layouts for visual inspection rather than asserting label positions.
func TestTabsPlace(t *testing.T) {
	dir := os.Getenv("NM_FRAMES")
	if dir == "" {
		t.Skip("set NM_FRAMES to inspect pane placement")
	}
	for _, p := range []struct {
		name string
		pane int
	}{{"lyrics", paneLyrics}, {"stage", paneStage}, {"queue", paneQueue}, {"wall", paneWall}} {
		a, tt, _ := playingApp()
		a.nowPlaying, a.npPane = true, p.pane
		tt.Frame()
		tt.Frame()
		f, err := os.Create(filepath.Join(dir, "tabs-"+p.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, tt.Image())
		closeErr := f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}
