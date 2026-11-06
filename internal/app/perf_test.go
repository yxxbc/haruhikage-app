package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yxxbc/haruhikage-app/internal/domain"
	"github.com/yxxbc/haruhikage-app/pkg/lyrics"
)

// playingApp is an app playing a track with lyrics, at a position the test
// sets with pl.Seek, and the number of times its view was built.
func playingApp() (*app, *ui.Tester, *int) {
	a := newTestApp()
	a.settings = defaultSettings()
	tr := domain.Track{Provider: "wy", ID: "7", Name: "宝船", Artists: []domain.Artist{{Name: "DOUDOU"}}, Duration: 200000}
	a.pl.Cue([]domain.Track{tr})
	// A line every 10 seconds, sung over the first 5 of them.
	var lrc string
	for i := range 12 {
		lrc += "[0" + string(rune('0'+i*10/60)) + ":" + string(rune('0'+i*10%60/10)) + "0.00]一二三四五六\n"
	}
	lines := lyrics.Build(lrc, "", "", "")
	for i := range lines {
		l := &lines[i]
		l.End = l.At + 5*time.Second
		l.Words = lyrics.SpreadWords(l.Text, l.At, l.End)
	}
	a.pl.SetLyrics(lines)
	a.pl.Hold(0)
	builds := 0
	tt := ui.NewTester(func(c *ui.Context) { builds++; a.view(c) }, 1200, 800)
	return a, tt, &builds
}

// framesAt counts actual builds, including ones driven by After timers:
// Tester.Advance runs those itself, while FramePending alone misses them.
// Painter-only GPU work still needs MYGO_FRAME_STATS in the native app.
func framesAt(a *app, tt *ui.Tester, builds *int, pos time.Duration) int {
	a.pl.Seek(pos)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	before := *builds
	tt.Advance(time.Second)
	return *builds - before
}

// Idle lyric intervals avoid display-rate view builds in every preserved pane;
// native paint-only animation budgets are measured separately with MYGO_FRAME_STATS.
func TestNowPlayingFrameBudget(t *testing.T) {
	a, tt, builds := playingApp()
	if n := framesAt(a, tt, builds, 12*time.Second); n > 4 {
		t.Errorf("a page under the player: %d frames a second", n)
	}
	// Its rows still, the wall asks for no more than the lyrics beside it
	// do: it moves only as it is moved.
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	a.nowPlaying, a.npPane = true, paneLyrics
	lyricsFrames := framesAt(a, tt, builds, 12*time.Second)
	a.npPane = paneWall
	if n := framesAt(a, tt, builds, 12*time.Second); n > lyricsFrames+4 {
		t.Errorf("the wall, still: %d frames a second, the lyrics %d", n, lyricsFrames)
	}
	tt.SetPreferences(ui.Preferences{})
	a.npPane = paneStage
	a.settings.StageSparks = true
	for _, s := range stageStyles {
		a.settings.StageStyle = s.id
		if n := framesAt(a, tt, builds, 18*time.Second); n > 12 {
			t.Errorf("the stage between lines, in %s: %d frames a second", s.label, n)
		}
	}
}

// Another song's backdrop fades in over the last one's, which is then let
// go, so that the backdrop stops asking for frames.
func TestBackdropFadeEnds(t *testing.T) {
	a, tt, _ := coverApp(t)
	tt.SetPreferences(ui.Preferences{})
	a.art.art["a#backdrop"] = &artwork{tint: ui.RGB(200, 40, 40)}
	a.art.art["b#backdrop"] = &artwork{tint: ui.RGB(40, 40, 200)}
	q := a.pl.Queue()
	q[1].Album.PicURL, q[2].Album.PicURL = "a", "b"
	tt.Frame()
	a.pl.Next()
	tt.Frame()
	if a.backdropCache.before == nil {
		t.Fatal("the backdrop changed songs without fading")
	}
	tt.Advance(backdropFade + 100*time.Millisecond)
	tt.Frame()
	if a.backdropCache.before != nil {
		t.Error("the last song's backdrop still fades after the fade")
	}
}

// Hidden windows and ReduceMotion stop lyric/stage effects without pausing playback.
func TestVisualInactivityStopsBuildTimers(t *testing.T) {
	for _, hidden := range []bool{true, false} {
		for _, pane := range []int{paneLyrics, paneStage, paneWall, paneQueue} {
			a, tt, builds := playingApp()
			a.nowPlaying, a.npPane = true, pane
			a.settings.StageSparks, a.settings.Smoke = true, true
			a.visualActive = !hidden
			tt.SetPreferences(ui.Preferences{ReduceMotion: !hidden})
			a.pl.Seek(2 * time.Second) // middle of a word, not a quiet gap
			tt.Frame()
			tt.Advance(2 * time.Second) // consume any timer armed before hiding
			before := *builds
			tt.Advance(2 * time.Second)
			if got := *builds - before; got > 1 {
				t.Errorf("hidden=%v pane=%d: idle effects rebuilt %d times", hidden, pane, got)
			}
			if !a.pl.Playing() {
				t.Errorf("hidden=%v pane=%d paused playback", hidden, pane)
			}
		}
	}
}
