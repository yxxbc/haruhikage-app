package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// lyricOffsets remembers how far each song's lyrics are moved against its
// music, in milliseconds, by track: a song whose lyrics run ahead of the
// voice is moved back by as much as it is ahead, and remembered, so that
// the correction a listener makes once holds the next time they play it.
// They are kept with the settings.

// lyricOffset returns the offset of track id, 0 for a song not moved.
func (s *settings) lyricOffset(id string) time.Duration {
	if s.LyricOffsets == nil {
		return 0
	}
	return time.Duration(s.LyricOffsets[id]) * time.Millisecond
}

// setLyricOffset moves track id's lyrics by d, forgetting the offset for a
// song back where it was: a map of zeros would only grow.
func (s *settings) setLyricOffset(id string, d time.Duration) {
	ms := int(d / time.Millisecond)
	if ms == 0 {
		delete(s.LyricOffsets, id)
		return
	}
	if s.LyricOffsets == nil {
		s.LyricOffsets = map[string]int{}
	}
	s.LyricOffsets[id] = ms
}

// lyricOffsets are the steps the lyrics are moved by, in milliseconds:
// the fine ones a listener uses to line them up, and the coarse ones to
// get there quickly.
var lyricOffsetSteps = []int{-500, -100, -50, 50, 100, 500}

// lyricOffsetButton moves the song playing's lyrics against its music, and
// shows how far they are moved, if they are. Choosing a step adds it to the
// offset, so two presses of 50 ms move the lyrics by 100 ms.
func (a *app) lyricOffsetButton(c *ui.Context) {
	tr := a.pl.Current()
	if tr == nil {
		return
	}
	d := a.settings.lyricOffset(tr.Key())
	b := ui.ButtonBase(c.Key("lyric-offset")).Padding(6, 10, 6, 10).Radius(999).Gap(4).AlignItems(ui.Center).
		FocusRing(false).Label("歌词偏移").Tooltip("歌词比音乐快时，把歌词往后退")
	fg := white.Alpha(0.55)
	bg := ui.Transparent
	if d != 0 {
		// Moved: it says by how much, in the accent, so a listener who
		// forgot they moved it can see why it is off.
		fg = paletteFor(c.Theme()).accent
	}
	if b.Hovered() {
		bg, fg = white.Alpha(0.06), white.Alpha(0.85)
	}
	b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond}))
	b.Children(func() {
		if d == 0 {
			ui.Text(c, "歌词偏移").FontSize(12).TextColor(fg)
			return
		}
		ui.Text(c, offsetLabel(d)).FontSize(12).FontWeight(600).TextColor(fg)
	})
	b.Menu(func(m *ui.Menu) {
		for _, step := range lyricOffsetSteps {
			label := fmt.Sprintf("%+d ms", step)
			if m.Item(label).Chosen() {
				a.settings.setLyricOffset(tr.Key(), a.settings.lyricOffset(tr.Key())+time.Duration(step)*time.Millisecond)
				a.settings.save()
				a.redraw()
			}
		}
		if d != 0 {
			m.Separator()
			if m.Item("复位").Chosen() {
				a.settings.setLyricOffset(tr.Key(), 0)
				a.settings.save()
				a.redraw()
			}
		}
	})
}

// offsetLabel says an offset the way the button shows it: the seconds it
// moves the lyrics, to a tenth.
func offsetLabel(d time.Duration) string {
	ms := d / time.Millisecond
	sign := "+"
	if ms < 0 {
		sign, ms = "−", -ms
	}
	return fmt.Sprintf("%s%.1f 秒", sign, float64(ms)/1000)
}
