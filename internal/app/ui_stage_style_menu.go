package app

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// stageStyleButton names the stage's style, and opens the menu of the
// others.
func (a *app) stageStyleButton(c *ui.Context) {
	chosen := stageStyleByID(a.settings.StageStyle)
	b := ui.ButtonBase(c.Key("stage-style")).Padding(6, 10, 6, 14).Radius(999).Gap(4).AlignItems(ui.Center).
		FocusRing(false).Label("律动效果").Tooltip("律动效果")
	bg, fg := ui.Transparent, white.Alpha(0.55)
	if b.Hovered() {
		bg, fg = white.Alpha(0.06), white.Alpha(0.8)
	}
	b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond}))
	b.Children(func() {
		ui.Text(c, chosen.label).FontSize(13).FontWeight(600).TextColor(fg)
		ui.Icon(c, iconDown).Size(12, 12).TextColor(fg)
	})
	b.Menu(func(m *ui.Menu) {
		for _, s := range stageStyles {
			if m.Item(s.label).Checked(a.settings.StageStyle == s.id).Chosen() && a.settings.StageStyle != s.id {
				a.settings.StageStyle = s.id
				a.settings.save()
			}
		}
		m.Separator()
		if m.Item("火花").Checked(a.settings.StageSparks).Chosen() {
			a.settings.StageSparks = !a.settings.StageSparks
			a.settings.save()
		}
	})
}
