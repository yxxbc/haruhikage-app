package app

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/yxxbc/haruhikage-app/internal/domain"
)

var copyText = func(text string) error { return mygo.Clipboard.Write(transfer.TextData(text)) }

func (a *app) playNext(t domain.Track) {
	if t.Ref().Valid() {
		q := a.pl.Queue()
		at := 0
		if a.pl.Current() != nil {
			_, at = a.pl.PlayOrder()
			at++
		}
		if at > len(q) {
			at = len(q)
		}
		q = append(append([]domain.Track(nil), q[:at]...), append([]domain.Track{t}, q[at:]...)...)
		a.pl.PlayQueue(q, at)
	}
}
func (a *app) songMore(c *ui.Context, t *domain.Track, size float32, color ui.Color) {
	iconButton(c, iconMore, size, color, "歌曲菜单").Menu(a.songItems(t, ""))
}
func (a *app) songItems(t *domain.Track, pid string) func(*ui.Menu) {
	track := *t
	return func(m *ui.Menu) {
		if m.Item("立即播放").Chosen() {
			a.pl.PlayQueue([]domain.Track{track}, 0)
		}
		if m.Item("下一首播放").Chosen() {
			a.playNext(track)
		}
		label := "喜欢"
		if a.liked[track.Key()] {
			label = "取消喜欢"
		}
		if m.Item(label).Chosen() {
			a.toggleLike(&track)
		}
		m.Submenu("添加到歌单", func(sub *ui.Menu) {
			for _, pl := range a.mine {
				if sub.Item(pl.Name).Chosen() {
					a.addTracks(pl.ID, []domain.Track{track})
				}
			}
			if sub.Item("新建歌单…").Chosen() {
				a.newPlaylist(&track)
			}
		})
		if pid != "" {
			m.Separator()
			if m.Item("从歌单移除").Chosen() {
				a.run(func() {
					err := a.lib.RemoveTrack(a.ctx, pid, track.Ref())
					a.post(func() {
						if err != nil {
							a.toast = "移除歌曲失败"
							return
						}
						a.refreshLibrary()
						if a.route.page == pagePlaylist && a.route.id == pid {
							a.load(a.route)
						}
					})
				})
			}
		}
		m.Separator()
		if m.Item("复制歌曲名称").Chosen() {
			_ = copyText(track.Name + " - " + track.ArtistNames())
		}
		if track.URL() != "" && m.Item("复制歌曲链接").Chosen() {
			_ = copyText(track.URL())
		}
		if track.Provider == "wy" && track.Album.ID != "" && m.Item("查看专辑").Chosen() {
			a.open(route{page: pageAlbum, provider: "wy", id: track.Album.ID})
		}
		if track.Provider == "wy" {
			for _, artist := range track.Artists {
				if artist.ID != "" && m.Item("歌手："+artist.Name).Chosen() {
					a.open(route{page: pageArtist, provider: "wy", id: artist.ID})
				}
			}
		}
	}
}
func (a *app) artistLinks(c *ui.Context, t *domain.Track, color, hover ui.Color) ui.Element {
	return ui.Text(c, t.ArtistNames()).TextColor(color)
}
func (a *app) link(c *ui.Context, label string, color, hover ui.Color, r route) {
	b := ui.ButtonBase(c).Label(label).TextColor(color)
	if b.Hovered() {
		b.TextColor(hover)
	}
	b.Children(func() { ui.Text(c, label) })
	if b.Clicked() {
		a.open(r)
	}
}
