package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTabsPlace(t *testing.T) {
	const w, h = 1180, 780
	for _, p := range []struct {
		name string
		pane int
	}{{"歌词", paneLyrics}, {"律动", paneStage}, {"待播清单", paneQueue}} {
		a, tt, _ := playingApp()
		a.nowPlaying = true
		a.npPane = p.pane
		tt.Frame()
		tt.Frame()
		r, ok := tt.Find("评论")
		if !ok {
			t.Logf("%s: no 评论 tab", p.name)
			continue
		}
		t.Logf("%s: 评论 tab x=%.1f..%.1f (window %d)", p.name, r.X, r.X+r.W, w)
		_ = ui.Rect{}
	}
}
