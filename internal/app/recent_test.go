package app

import (
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yxxbc/haruhikage-app/internal/netease"
)

// The recent page groups plays under the day they were heard and queues
// only the ones that can play, in the order they show.
func TestRecentRows(t *testing.T) {
	zone := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 11, 5, 21, 30, 0, 0, zone)
	plays := []netease.Play{
		{Track: netease.Track{ID: 1, Name: "今天"}, At: now.Add(-5 * time.Minute), Device: "iphone"},
		{Track: netease.Track{ID: 2, Name: "付费", Fee: 1}, At: now.Add(-time.Hour)},
		{Track: netease.Track{ID: 3, Name: "昨天"}, At: now.AddDate(0, 0, -1)},
		{Track: netease.Track{ID: 4, Name: "去年"}, At: time.Date(2025, 12, 31, 23, 0, 0, 0, zone)},
	}
	items, tracks := recentRows(plays, now)

	want := []struct {
		day   string
		count int
		id    int64
		track int
	}{
		{day: "今天", count: 2},
		{id: 1, track: 0},
		{id: 2, track: -1},
		{day: "昨天", count: 1},
		{id: 3, track: 1},
		{day: "2025年12月31日", count: 1},
		{id: 4, track: 2},
	}
	if len(items) != len(want) {
		t.Fatalf("%d rows, want %d", len(items), len(want))
	}
	for i, w := range want {
		it := items[i]
		if w.day != "" {
			if it.play != nil || it.day != w.day || it.count != w.count {
				t.Errorf("row %d: %q count %d play %v, want day %q count %d", i, it.day, it.count, it.play, w.day, w.count)
			}
			continue
		}
		if it.play == nil || it.play.Track.ID != w.id || it.track != w.track {
			t.Errorf("row %d: play %v track %d, want id %d track %d", i, it.play, it.track, w.id, w.track)
		}
	}
	if len(tracks) != 3 {
		t.Fatalf("%d tracks to play, want 3", len(tracks))
	}
	for i, id := range []int64{1, 3, 4} {
		if tracks[i].ID != id {
			t.Errorf("track %d is %d, want %d", i, tracks[i].ID, id)
		}
	}
	if got := playedAt(plays[0]); got != "21:25 · 手机" {
		t.Errorf("playedAt = %q", got)
	}
	if got := recentDay(time.Time{}, now); got != "更早" {
		t.Errorf("zero time is %q", got)
	}
}

// The sidebar's 最近播放 opens the page and asks for the latest 100 plays.
func TestRecentPageLoads(t *testing.T) {
	asked := make(chan int, 1)
	recentPlays = func(limit int) ([]netease.Play, error) {
		asked <- limit
		return nil, errors.New("offline")
	}
	defer func() { recentPlays = netease.RecentPlays }()
	a := newTestApp()
	a.settings = defaultSettings()
	a.profile = &netease.Profile{UserID: 42}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if err := tt.Click("最近播放"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.route.page != pageRecent {
		t.Fatalf("opened %+v", a.route)
	}
	if got := wait(t, asked, "the recent plays"); got != 100 {
		t.Fatalf("asked for %d plays", got)
	}
}
