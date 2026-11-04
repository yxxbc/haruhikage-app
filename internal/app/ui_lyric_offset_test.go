package app

import (
	"testing"
	"time"

	"github.com/yxxbc/haruhikage-app/pkg/lyrics"
)

// An offset moves a song's lyrics against its music, and is kept by track:
// a correction made once holds the next time the song plays, and other
// songs keep their own.
func TestLyricOffsetRemembers(t *testing.T) {
	s := defaultSettings()
	if d := s.lyricOffset(7); d != 0 {
		t.Fatalf("a song never moved reads as %v", d)
	}
	s.setLyricOffset(7, -500*time.Millisecond)
	s.setLyricOffset(8, 100*time.Millisecond)
	if d := s.lyricOffset(7); d != -500*time.Millisecond {
		t.Errorf("song 7 moved by %v", d)
	}
	if d := s.lyricOffset(8); d != 100*time.Millisecond {
		t.Errorf("song 8 moved by %v, song 7's offset", d)
	}
	// Back where it was, the entry goes: a map of zeros would only grow.
	s.setLyricOffset(7, 0)
	if d := s.lyricOffset(7); d != 0 {
		t.Errorf("song 7 reads %v after being put back", d)
	}
	if _, ok := s.LyricOffsets[7]; ok {
		t.Error("the entry of a song put back is still there")
	}
	if d := s.lyricOffset(8); d != 100*time.Millisecond {
		t.Errorf("putting song 7 back moved song 8 to %v", d)
	}
}

// The lyrics show where the offset says, the music plays on where it is:
// the whole reason the offset exists.
func TestLyricOffsetShows(t *testing.T) {
	a, tt, _ := playingApp()
	a.nowPlaying, a.npPane = true, paneLyrics
	a.pl.Seek(2 * time.Second)
	// A line 3 seconds in, so moving the lyrics forward by 2 puts it on
	// screen with playback still at 2.
	a.pl.SetLyrics(lyrics.Build("[00:03.00]第一句\n[00:30.00]第二句", "", "", ""))
	tt.Frame()
	// At 2s the first line, 3s in, is not the one sung.
	if got := a.lyricAt; got == 0 {
		t.Fatal("the line 3s in is sung at 2s")
	}

	tr := a.pl.Current()
	a.settings.setLyricOffset(tr.ID, 2000*time.Millisecond)
	tt.Frame()
	tt.Frame()
	// 2s of music and 2s of offset put the lyrics at 4s, where the first
	// line (3s) is the one sung.
	if got := a.lyricAt; got != 0 {
		t.Errorf("with the lyrics 2s ahead, line %d is sung at 2s", got)
	}
	// The music is where it was: the offset moves the words, not the song.
	if got := a.pl.Position(); got < time.Second || got > 4*time.Second {
		t.Errorf("playback at %v, the offset moved the song", got)
	}
}

// Choosing a step from the button's menu moves the lyrics by it, and the
// button says by how much; 复位 puts them back.
func TestLyricOffsetMenu(t *testing.T) {
	a, tt, _ := playingApp()
	a.nowPlaying, a.npPane = true, paneLyrics
	a.pl.Seek(2 * time.Second)
	tt.Frame()
	tr := a.pl.Current()

	if err := tt.Click("歌词偏移"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("+100 ms"); err != nil {
		t.Fatalf("%v; the menu shows %q", err, tt.Menu())
	}
	tt.Frame()
	if d := a.settings.lyricOffset(tr.ID); d != 100*time.Millisecond {
		t.Fatalf("after +100ms: %v", d)
	}
	// The button now says what it did, so a listener who forgot can see.
	if !tt.HasText("+0.1 秒") {
		t.Errorf("the button does not say the offset: %q", tt.Texts())
	}

	// Steps add up, rather than replacing.
	if err := tt.Click("+0.1 秒"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("+100 ms"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d := a.settings.lyricOffset(tr.ID); d != 200*time.Millisecond {
		t.Errorf("after a second +100ms: %v, want 0.2s", d)
	}

	if err := tt.Click("+0.2 秒"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复位"); err != nil {
		t.Fatalf("%v; the menu shows %q", err, tt.Menu())
	}
	tt.Frame()
	if d := a.settings.lyricOffset(tr.ID); d != 0 {
		t.Errorf("after 复位: %v", d)
	}
	if !tt.HasText("歌词偏移") {
		t.Errorf("the button does not name itself again: %q", tt.Texts())
	}
}
