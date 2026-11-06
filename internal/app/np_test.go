package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yxxbc/haruhikage-app/internal/domain"
	"github.com/yxxbc/haruhikage-app/pkg/lyrics"
)

// Closing Now Playing keeps one opaque layer over the page and sidebar until it leaves.
func TestNowPlayingClosesAsOneLayer(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	tr := domain.Track{Provider: "wy", ID: "1", Name: "宝船", Artists: []domain.Artist{{Name: "DOUDOU"}}, Duration: 200000}
	a.pl.Cue([]domain.Track{tr})
	a.pl.SetLyrics(nil)
	tt := ui.NewTester(a.view, 1200, 800)
	tt.SetDark(false)
	a.nowPlaying = true
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if _, ok := tt.Find("关闭"); !ok {
		t.Fatal("now playing did not open")
	}
	a.nowPlaying = false
	tt.Frame()
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	img := tt.Image()
	sx, sy := img.Bounds().Dx(), img.Bounds().Dy()
	dark := func(x, y float32) bool {
		r, g, b, _ := img.At(int(x*float32(sx)/1200), int(y*float32(sy)/800)).RGBA()
		return (r+g+b)/3 < 0x9000
	}
	if !dark(900, 400) || !dark(100, 400) {
		t.Errorf("page %v, sidebar %v: the closing layer should cover both", dark(900, 400), dark(100, 400))
	}
	tt.Advance(2 * time.Second)
	tt.Frame()
	if _, ok := tt.Find("关闭"); ok {
		t.Error("now playing still shows after closing")
	}
}

// Choosing a queue row changes playback without replacing or slicing its queue.
func TestQueueStaysAsATrackIsPlayed(t *testing.T) {
	a := newTestApp()
	var tracks []domain.Track
	for i := range 12 {
		tracks = append(tracks, domain.Track{Provider: "wy", ID: fmt.Sprint(i + 1), Name: fmt.Sprintf("歌%02d", i), Duration: 200000})
	}
	a.pl.Cue(tracks)
	a.nowPlaying, a.npPane = true, paneQueue
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("歌05"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if got := a.pl.Current().Name; got != "歌05" {
		t.Fatalf("playing %q, want 歌05", got)
	}
	if got := a.pl.Queue(); len(got) != len(tracks) || got[0].Key() != tracks[0].Key() || got[11].Key() != tracks[11].Key() {
		t.Fatalf("choosing a queue row changed the queue: %+v", got)
	}
}

// Reading the saved lyrics pane before the player exists must not crash startup.
func TestLyricsPaneBeforePlayer(t *testing.T) {
	a := &app{settings: settings{LyricsStage: true}}
	if pane := a.lyricsPaneChosen(); pane != paneStage {
		t.Errorf("pane %d", pane)
	}
}

// Previous plays the preceding song even well into this song, rather than seeking to its start.
func TestPrevGoesBackASong(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	a.pl.Cue([]domain.Track{{Provider: "wy", ID: "1", Name: "一", Duration: 200000}, {Provider: "wy", ID: "2", Name: "二", Duration: 200000}})
	a.pl.SetLyrics(nil)
	a.pl.Hold(30 * time.Second)
	a.nowPlaying = true
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if err := tt.Click("上一首"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if id := a.pl.Current().ID; id != "2" {
		t.Errorf("playing %s, want the one before, 2", id)
	}
}

// Losing stream metadata during loading must not move the cover or controls.
func TestNowPlayingHoldsStillWithoutBadge(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	a.pl.Cue([]domain.Track{{Provider: "wy", ID: "1", Name: "一", Duration: 200000}})
	a.pl.SetLyrics(nil)
	a.nowPlaying = true
	tt := ui.NewTester(a.view, 1200, 800)
	var at []float32
	for _, st := range []*domain.Stream{{Type: "flac", Level: "flac"}, nil} {
		a.pl.SetStream(st)
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
		r, ok := tt.Find("专辑封面")
		if !ok {
			t.Fatal("no cover")
		}
		at = append(at, r.Y)
	}
	if at[0] != at[1] {
		t.Errorf("the cover moved from %v to %v as the badge went", at[0], at[1])
	}
}

// Number shortcuts and tab clicks choose the same panes and retain the lyrics preference.
func TestCmdNumberChoosesTab(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	a.pl.Cue([]domain.Track{{Provider: "wy", ID: "7", Name: "宝船", Duration: 200000}})
	a.pl.SetLyrics(nil)
	a.nowPlaying = true
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	for _, step := range []struct{ key, pane int }{{4, paneWall}, {3, paneQueue}, {2, paneStage}, {1, paneLyrics}} {
		tt.Key(ui.Cmd, ui.Key1+ui.Key(step.key-1))
		tt.Frame()
		if a.npPane != step.pane {
			t.Errorf("shortcut %d chose pane %d, want %d", step.key, a.npPane, step.pane)
		}
	}
	if err := tt.Click("律动"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.npPane != paneStage || !a.settings.LyricsStage {
		t.Fatal("the stage click did not retain the chosen lyrics pane")
	}
}

// A requested quality must never be presented as verified codec, bitrate or Hi-Res.
func TestQualityBadgeUsesDetectedCodec(t *testing.T) {
	for _, st := range []*domain.Stream{{Type: "mp3", Level: "hires"}, {Level: "flac24bit"}} {
		label := qualityLabel(st)
		if !strings.Contains(label, st.Level) || strings.Contains(label, "Hi-Res") || strings.Contains(label, "无损") {
			t.Fatalf("unverified requested quality is misrepresented: %q", label)
		}
		if st.Type != "" && !strings.Contains(label, "MP3") {
			t.Fatalf("detected codec was lost: %q", label)
		}
		if st.Type == "" && strings.Contains(label, "FLAC") {
			t.Fatalf("a request was used as a detected codec: %q", label)
		}
	}
}

// A long lyric builds a bounded visible window, follows distant seeks and still seeks on click.
func TestLongLyricsVirtualWindowAndSeek(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	tr := domain.Track{Provider: "wy", ID: "7", Name: "长歌词", Duration: 6000000}
	a.pl.Cue([]domain.Track{tr})
	lines := make([]lyrics.Line, 5000)
	for i := range lines {
		at := time.Duration(i) * time.Second
		lines[i] = lyrics.Line{At: at, End: at + time.Second, Text: fmt.Sprintf("line-%04d", i), Translation: "翻译", Words: []lyrics.Word{{Text: fmt.Sprintf("line-%04d", i), Start: at, End: at + time.Second}}}
	}
	a.pl.SetLyrics(lines)
	a.pl.Hold(2500 * time.Second)
	tt := ui.NewTester(func(c *ui.Context) { a.lyricsPane(c, &tr, 800, white) }, 600, 800)
	settle := func() {
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
		tt.Frame()
		if n := len(a.lyricGlide.rows); n == 0 || n > 40 {
			t.Fatalf("built %d lyric rows for a visible window", n)
		}
	}
	settle()
	if err := tt.Click("line-2501"); err != nil {
		t.Fatal(err)
	}
	if got := a.pl.Position(); got != 2501*time.Second {
		t.Fatalf("line click sought to %v", got)
	}
	a.pl.Seek(4500 * time.Second)
	settle()
	if _, ok := tt.Find("line-4500"); !ok {
		t.Fatal("a distant seek left the active line outside the virtual window")
	}
}

// Interrupted open/close transitions must remain reversible and not leave the overlay intercepting input.
func TestNowPlayingTransitionCanReverse(t *testing.T) {
	a, tt, _ := coverApp(t)
	tt.SetPreferences(ui.Preferences{})
	for _, open := range []bool{false, true, false, true, false} {
		a.nowPlaying = open
		tt.Frame()
		tt.Advance(70 * time.Millisecond)
		tt.Frame()
	}
	tt.Advance(npSettle + time.Second)
	tt.Frame()
	if _, ok := tt.Find("专辑封面"); ok {
		t.Fatal("the reversed closing overlay still intercepts input")
	}
	if err := tt.Click("打开正在播放"); err != nil {
		t.Fatal(err)
	}
	if !a.nowPlaying {
		t.Fatal("the page under the closed overlay did not receive the click")
	}
}
