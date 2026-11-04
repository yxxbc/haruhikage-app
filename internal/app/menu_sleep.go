package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo"
)

// sleepChoices are the delays the sleep timer offers, in minutes.
var sleepChoices = []int{15, 30, 45, 60, 90}

// sleepDesc describes the sleep timer set, for its row in the settings.
func (a *app) sleepDesc() string {
	atEnd, left := a.pl.Sleeping()
	switch {
	case atEnd:
		return "播完当前歌曲后停止播放"
	case left > 0:
		return fmt.Sprintf("%d 分钟后停止播放", left)
	}
	return "到时间或播完当前歌曲后停止播放"
}

// sleepMenu returns the sleep timer's items: the delays, stopping with the
// track playing, and turning it off. The one running is checked, so the menu
// says what is left, and choosing any of them sets the menu again to show it.
func (a *app) sleepMenu(run func(func()) func(*mygo.MenuItem, *mygo.Window)) []*mygo.MenuItem {
	atEnd, left := a.pl.Sleeping()
	var items []*mygo.MenuItem
	for _, m := range sleepChoices {
		m := m
		label := fmt.Sprintf("%d 分钟", m)
		// The one running reads as how long is left, so a timer set to an
		// hour and down to 7 minutes does not claim an hour.
		if !atEnd && left > 0 && left == m {
			label = fmt.Sprintf("%d 分钟后停止", m)
		}
		items = append(items, &mygo.MenuItem{
			Label:   label,
			Type:    mygo.MenuItemRadio,
			Checked: !atEnd && left > 0 && left == m,
			Click: run(func() {
				a.pl.SetSleep(time.Duration(m) * time.Minute)
				a.setMenu()
				a.toast = fmt.Sprintf("%d 分钟后停止播放", m)
			}),
		})
	}
	items = append(items, mygo.Separator(), &mygo.MenuItem{
		Label:   "播完当前歌曲后停止",
		Type:    mygo.MenuItemRadio,
		Checked: atEnd,
		Click: run(func() {
			a.pl.SetSleepAtEnd()
			a.setMenu()
			a.toast = "播完当前歌曲后停止播放"
		}),
	})
	// Only offered while something is set, where turning it off means
	// something.
	if atEnd || left > 0 {
		items = append(items, &mygo.MenuItem{
			Label: "关闭睡眠定时",
			Click: run(func() {
				a.pl.CancelSleep()
				a.setMenu()
				a.toast = "已关闭睡眠定时"
			}),
		})
	}
	return items
}
