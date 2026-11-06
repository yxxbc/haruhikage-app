package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// Scrolling a long local library page moves its top and bottom edge coverage with the content.
func TestScrollEdgesFollowContent(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	for i := range 40 {
		a.mine = append(a.mine, domain.Playlist{ID: fmt.Sprint(i + 1), Name: fmt.Sprintf("歌单%d", i+1)})
	}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if a.underTop {
		t.Error("top edge shows before scrolling")
	}
	if !a.underBottom {
		t.Error("no bottom edge over a long page")
	}
	tt.Scroll(800, 400, 0, 100000)
	tt.Advance(time.Second)
	tt.Frame()
	tt.Frame()
	if !a.underTop {
		t.Error("no top edge after scrolling")
	}
	if a.underBottom {
		t.Error("bottom edge shows at the end of the page")
	}
}

// A short local library page needs neither top nor bottom edge coverage.
func TestShortPageHasNoEdges(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	a.mine = []domain.Playlist{{ID: "1", Name: "歌单"}}
	tt := ui.NewTester(a.view, 1200, 1400)
	tt.Frame()
	if a.underTop || a.underBottom {
		t.Errorf("edges on a page that fits: top %v bottom %v", a.underTop, a.underBottom)
	}
}
