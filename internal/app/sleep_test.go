package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo"
)

// The sleep menu offers the delays, the one running is checked and reads as
// the minutes left, and stopping with the track or turning it off are there
// when they mean something.
func TestSleepMenu(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	// The menu's items only need somewhere to click to; what they set is
	// the player's.
	run := func(fn func()) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) { fn() }
	}

	items := a.sleepMenu(run)
	// The delays, a separator, and stopping with the track.
	if len(items) != len(sleepChoices)+2 {
		t.Fatalf("%d items before anything is set: want %d", len(items), len(sleepChoices)+2)
	}
	for _, it := range items[:len(sleepChoices)] {
		if it.Checked {
			t.Errorf("%q is checked with no timer", it.Label)
		}
	}

	a.pl.SetSleep(30 * time.Minute)
	items = a.sleepMenu(run)
	var checked string
	for _, it := range items {
		if it.Checked {
			checked = it.Label
		}
	}
	if checked != "30 分钟后停止" {
		t.Errorf("checked %q, want the 30 minute one", checked)
	}
	// 关闭 is offered only while something is set.
	var off bool
	for _, it := range items {
		if it.Label == "关闭睡眠定时" {
			off = true
		}
	}
	if !off {
		t.Error("no way to turn a running timer off")
	}

	a.pl.SetSleepAtEnd()
	for _, it := range a.sleepMenu(run) {
		if it.Checked && it.Label != "播完当前歌曲后停止" {
			t.Errorf("checked %q with the end of the track chosen", it.Label)
		}
	}
}
