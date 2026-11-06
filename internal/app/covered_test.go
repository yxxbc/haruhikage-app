package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// A covered library page is not built, and closing Now Playing restores its scroll position.
func TestCoveredPageKeepsItsPlace(t *testing.T) {
	for _, r := range []route{{page: pageFavorites}, {page: pageHome}} {
		a := newTestApp()
		a.settings = defaultSettings()
		a.route = r
		for i := range 200 {
			a.tracks = append(a.tracks, domain.Track{Provider: "wy", ID: fmt.Sprint(i + 1), Name: fmt.Sprintf("曲目%d", i+1)})
		}
		for i := range 40 {
			a.mine = append(a.mine, domain.Playlist{ID: fmt.Sprint(100 + i), Name: fmt.Sprintf("歌单%d", i+1)})
		}
		a.pl.Cue([]domain.Track{{Provider: "wy", ID: "9999", Name: "一张相片"}})
		tt := ui.NewTester(a.view, 1200, 800)
		tt.Frame()
		tt.Wheel(700, 400, 0, 2400)
		tt.Advance(time.Second)
		tt.Frame()
		before := shown(tt)

		a.nowPlaying = true
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
		if _, ok := tt.Find("搜索歌曲"); ok {
			t.Errorf("%+v: the sidebar is built under Now Playing", r)
		}

		a.nowPlaying = false
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
		if after := shown(tt); after != before {
			t.Errorf("%+v: shows\n%s\nafter Now Playing, was\n%s", r, after, before)
		}
		if before == firstShown(a) {
			t.Errorf("%+v: the wheel did not scroll it", r)
		}
	}
}

// shown returns visible text and committed geometry for scroll restoration checks.
func shown(tt *ui.Tester) string {
	var s string
	for _, text := range tt.Texts() {
		if r, ok := tt.Find(text); ok {
			s += fmt.Sprintf("%s@%.0f,%.0f ", text, r.X, r.Y)
		}
	}
	return s
}

// firstShown returns the same page without a retained scrolling offset.
func firstShown(a *app) string {
	b := *a
	b.trackList, b.pageScroll, b.navScroll = ui.ListState{}, ui.ScrollState{}, ui.ScrollState{}
	tt := ui.NewTester(b.view, 1200, 800)
	tt.Frame()
	return shown(tt)
}
