package app

import (
	"github.com/egoist/mygo/ui"
	"github.com/yxxbc/haruhikage-app/internal/domain"
	"strings"
)

type nameDialog struct {
	open, busy   bool
	name, rename string
	add          *domain.Track
}
type deleteDialog struct {
	open, busy bool
	id, name   string
}

func (a *app) newPlaylist(t *domain.Track) {
	d := nameDialog{open: true}
	if t != nil {
		copy := *t
		d.add = &copy
	}
	a.naming = d
}
func (a *app) startRename(pl *domain.Playlist) {
	a.naming = nameDialog{open: true, name: pl.Name, rename: pl.ID}
}
func (a *app) confirmDelete(pl *domain.Playlist) {
	a.deleting = deleteDialog{open: true, id: pl.ID, name: pl.Name}
}
func (a *app) addTracks(id string, tracks []domain.Track) {
	if a.lib == nil {
		return
	}
	snapshot := append([]domain.Track(nil), tracks...)
	a.run(func() {
		err := a.lib.AddTracks(a.ctx, id, snapshot)
		a.post(func() {
			if err != nil {
				a.toast = "添加歌曲失败"
				return
			}
			a.toast = "已添加到歌单"
			a.refreshLibrary()
			if a.route.page == pagePlaylist && a.route.id == id {
				a.load(a.route)
			}
		})
	})
}
func (a *app) submitName() {
	d := a.naming
	if !d.open || d.busy || a.lib == nil || strings.TrimSpace(d.name) == "" {
		return
	}
	a.naming.busy = true
	a.run(func() {
		id := d.rename
		var err error
		if id != "" {
			err = a.lib.RenamePlaylist(a.ctx, id, d.name)
		} else {
			var pl domain.Playlist
			pl, err = a.lib.CreatePlaylist(a.ctx, d.name)
			id = pl.ID
			if err == nil && d.add != nil {
				err = a.lib.AddTracks(a.ctx, id, []domain.Track{*d.add})
			}
		}
		a.post(func() {
			a.naming.busy = false
			if err != nil {
				a.toast = "歌单保存失败"
				return
			}
			a.naming.open = false
			a.refreshLibrary()
			a.open(route{page: pagePlaylist, id: id})
		})
	})
}
func (a *app) deleteChosen() {
	d := a.deleting
	if !d.open || d.busy || a.lib == nil {
		return
	}
	a.deleting.busy = true
	a.run(func() {
		err := a.lib.DeletePlaylist(a.ctx, d.id)
		a.post(func() {
			a.deleting.busy = false
			if err != nil {
				a.toast = "歌单删除失败"
				return
			}
			a.deleting.open = false
			a.refreshLibrary()
			if a.route.page == pagePlaylist && a.route.id == d.id {
				a.open(route{page: pageHome})
			}
		})
	})
}
func (a *app) playlistNameDialog(c *ui.Context) {
	d := &a.naming
	ui.DialogBase(c.Key("playlist-name"), &d.open, func(bg, panel ui.Element) {
		bg.Background(ui.RGBA(0, 0, 0, .35))
		panel.Width(360).Padding(24).Radius(18).Gap(16).Background(a.pal.bg)
		title, action := "新建歌单", "创建"
		if d.rename != "" {
			title, action = "重命名歌单", "保存"
		}
		ui.Text(c, title).FontSize(17).FontWeight(700)
		in := ui.TextInput(c.Key("name"), &d.name).Placeholder("歌单名称").Label("歌单名称").AutoFocus().Disabled(d.busy)
		if in.Submitted() {
			a.submitName()
		}
		ui.Row(c).Gap(10).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Disabled(d.busy).Clicked() {
				d.open = false
			}
			if pillButton(c, nil, action, a.pal.accent, a.pal.onAccent).Disabled(d.busy || strings.TrimSpace(d.name) == "").Clicked() {
				a.submitName()
			}
		})
	})
}
func (a *app) deletePlaylistDialog(c *ui.Context) {
	d := &a.deleting
	ui.DialogBase(c.Key("playlist-delete"), &d.open, func(bg, panel ui.Element) {
		bg.Background(ui.RGBA(0, 0, 0, .35))
		panel.Width(360).Padding(24).Radius(18).Gap(12).Background(a.pal.bg)
		ui.Text(c, "删除歌单").FontSize(17).FontWeight(700)
		ui.Text(c, "删除“"+d.name+"”？可用之前导出的备份恢复。").FontSize(13)
		ui.Row(c).Gap(10).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Disabled(d.busy).Clicked() {
				d.open = false
			}
			if ui.Button(c, "删除").Disabled(d.busy).Clicked() {
				a.deleteChosen()
			}
		})
	})
}
