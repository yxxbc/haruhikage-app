package app

import (
	"context"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/yxxbc/haruhikage-app/internal/domain"
	"github.com/yxxbc/haruhikage-app/internal/player"
	"time"
)

const pagePad = 40

func (a *app) columns(c *ui.Context, minWidth float32) int {
	w, _ := c.Size()
	return max(2, int((w-sidebarWidth-2*pagePad+20)/(minWidth+20)))
}
func (a *app) heading(c *ui.Context, title, sub string) {
	ui.Column(c).Gap(4).Margin(0, 0, 18, 0).Children(func() {
		if sub != "" {
			ui.Text(c, sub).FontSize(12).FontWeight(600).TextColor(a.pal.muted)
		}
		ui.Text(c, title).FontSize(30).FontWeight(700).LetterSpacing(-.6)
	})
}
func (a *app) section(c *ui.Context, title string) ui.Element {
	return ui.Text(c, title).FontSize(19).FontWeight(700).Margin(24, 0, 14, 0)
}
func (a *app) empty(c *ui.Context, message string) {
	ui.Text(c, message).TextColor(a.pal.muted).MarginY(20)
}
func firstPlayable(tracks []domain.Track) int {
	for i, t := range tracks {
		if t.Playable() {
			return i
		}
	}
	return 0
}
func (a *app) homePage(c *ui.Context) {
	a.scrollPage(c, func() {
		a.heading(c, "我的音乐", fmt.Sprintf("%d 首喜欢的歌曲 · %d 张本地歌单", len(a.favorites), len(a.mine)))
		ui.Row(c).Gap(12).Wrap().Children(func() {
			if pillButton(c, iconSearch, "搜索音乐", a.pal.accent, a.pal.onAccent).Clicked() {
				a.searchBox.Focus(c)
			}
			if pillButton(c, iconPlus, "新建歌单", a.pal.hover, a.pal.text).Clicked() {
				a.newPlaylist(nil)
			}
			if pillButton(c, iconNote, "管理音源", a.pal.hover, a.pal.text).Clicked() {
				a.open(route{page: pageSources})
			}
		})
		if len(a.sourceList) == 0 {
			a.empty(c, "先导入 LX 音源脚本，再搜索并播放。收藏和歌单保存在本机。")
		}
		a.section(c, "我的歌单")
		if len(a.mine) == 0 {
			a.empty(c, "还没有歌单，可从歌曲菜单创建并添加。")
		}
		ui.Grid(c).Columns(a.columns(c, 180)).Gap(16).Children(func() {
			for _, pl := range a.mine {
				b := ui.ButtonBase(c.Key(pl.ID)).Column().Padding(22).Gap(10).Radius(14).Background(a.pal.hover).Label(pl.Name)
				b.Children(func() {
					ui.Icon(c, iconNote).Size(30, 30).TextColor(a.pal.accent)
					ui.Text(c, pl.Name).SingleLine().FontWeight(600)
					ui.Textf(c, "%d 首歌曲", pl.Count).FontSize(12).TextColor(a.pal.muted)
				})
				if b.Clicked() {
					a.open(route{page: pagePlaylist, id: pl.ID})
				}
			}
		})
		a.section(c, "资料库备份")
		ui.Row(c).Gap(10).Children(func() {
			if ui.Button(c, "导出资料库").Disabled(a.lib == nil).Clicked() {
				a.exportLibrary()
			}
			if ui.Button(c, "导入备份").Disabled(a.lib == nil).Clicked() {
				a.importLibrary()
			}
		})
		ui.Text(c, "备份包含本地歌单、喜欢和历史，不包含音源脚本或 Key。").FontSize(12).TextColor(a.pal.muted)
	})
}
func (a *app) libraryPage(c *ui.Context) {
	title := map[pageKind]string{pageFavorites: "我喜欢", pageRecent: "最近播放", pagePlaylist: "歌单"}[a.route.page]
	var playlist *domain.Playlist
	if a.route.page == pagePlaylist {
		for i := range a.mine {
			if a.mine[i].ID == a.route.id {
				playlist = &a.mine[i]
				title = playlist.Name
			}
		}
	}
	header := func(c *ui.Context) {
		a.heading(c, title, fmt.Sprintf("%d 首歌曲 · 本地资料库", len(a.tracks)))
		ui.Row(c).Gap(10).Children(func() {
			if pillButton(c, iconPlay, "播放全部", a.pal.accent, a.pal.onAccent).Disabled(len(a.tracks) == 0).Clicked() {
				a.pl.PlayQueue(a.tracks, firstPlayable(a.tracks))
			}
			if playlist != nil {
				if ui.Button(c, "重命名").Clicked() {
					a.startRename(playlist)
				}
				if ui.Button(c, "删除歌单").Clicked() {
					a.confirmDelete(playlist)
				}
			}
		})
		if a.dataErr != "" {
			a.empty(c, a.dataErr)
			if ui.Button(c, "重试").Clicked() {
				a.load(a.route)
			}
		}
		if !a.dataLoading && len(a.tracks) == 0 {
			a.empty(c, "暂无歌曲")
		}
	}
	if a.dataLoading {
		a.trackSkeleton(c, a.pageShimmer(c), true, header)
		return
	}
	a.trackRows(c, a.tracks, true, header)
}
func (a *app) load(r route) {
	if a.dataCancel != nil {
		a.dataCancel()
	}
	a.dataGen++
	a.dataErr = ""
	a.tracks = nil
	if r.page == pageSearch {
		a.loadSearch(r, true)
		return
	}
	if r.page == pageCharts || r.page == pageArtist || r.page == pageAlbum {
		a.loadBrowse(r)
		return
	}
	if r.page != pageFavorites && r.page != pageRecent && r.page != pagePlaylist {
		a.dataLoading = false
		return
	}
	if a.lib == nil {
		a.dataErr = "本地资料库不可用"
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.dataCancel = cancel
	gen := a.dataGen
	a.dataLoading = true
	a.run(func() {
		var tracks []domain.Track
		var err error
		switch r.page {
		case pageFavorites:
			tracks, err = a.lib.Favorites(ctx)
		case pagePlaylist:
			tracks, err = a.lib.Tracks(ctx, r.id)
		case pageRecent:
			entries, e := a.lib.History(ctx, 500)
			err = e
			for _, v := range entries {
				tracks = append(tracks, v.Track)
			}
		}
		a.post(func() {
			if a.dataGen != gen || a.route != r {
				return
			}
			a.dataLoading = false
			if err != nil {
				if ctx.Err() == nil {
					a.dataErr = "资料库读取失败"
				}
				return
			}
			a.tracks = tracks
		})
	})
}
func (a *app) refreshLibrary() {
	if a.lib == nil {
		return
	}
	a.run(func() {
		mine, e := a.lib.Playlists(a.ctx)
		favorites, fe := a.lib.Favorites(a.ctx)
		if e != nil || fe != nil {
			a.post(func() { a.toast = "资料库刷新失败" })
			return
		}
		liked := map[string]bool{}
		for _, t := range favorites {
			liked[t.Key()] = true
		}
		a.post(func() { a.mine = mine; a.favorites = favorites; a.liked = liked })
	})
}

// trackRows only builds visible rows; occurrence keys permit repeated songs.
func (a *app) trackRows(c *ui.Context, tracks []domain.Track, covers bool, header func(*ui.Context), footer ...func(*ui.Context)) {
	p := a.pal
	cur := a.pl.Current()
	pid := ""
	if a.route.page == pagePlaylist {
		pid = a.route.id
	}
	w, _ := c.Size()
	wide := w-sidebarWidth > 820
	top, bottom := a.insets(c)
	a.underTop, a.underBottom = true, true
	rows := len(tracks) + 1 + len(footer)
	ui.List(c.Key(a.route), &a.trackList, rows, func(i int) {
		if i == 0 {
			ui.Column(c).Children(func() { a.startMark(c); header(c) })
			return
		}
		if i == rows-1 {
			defer a.endMark(c)
		}
		if i > len(tracks) {
			footer[i-len(tracks)-1](c)
			return
		}
		i--
		tr := &tracks[i]
		playing := cur != nil && cur.Key() == tr.Key()
		row := ui.ButtonBase(c.Key(struct {
			Key   string
			Index int
		}{tr.Key(), i})).Height(54).PaddingX(12).Gap(14).Radius(10).AlignItems(ui.Center).FocusRing(false).Label(tr.Name).ContextMenu(a.songItems(tr, pid))
		hovered := row.Hovered()
		bg := ui.Transparent
		if playing {
			bg = p.accent.Alpha(.09)
		} else if hovered {
			bg = p.hover
		}
		row.Background(row.AnimateColor("bg", bg, ui.Animation{Duration: 100 * time.Millisecond}))
		row.Children(func() {
			ui.Box(c).Width(26).Center().Children(func() {
				if playing {
					a.nowPlayingBars(c)
				} else if hovered {
					ui.Icon(c, iconPlay).Size(13, 13).TextColor(p.text)
				} else {
					ui.Textf(c, "%d", i+1).FontSize(13).FontFeatures("tnum").TextColor(p.faint)
				}
			})
			if covers {
				a.cover(c, tr.Album.PicURL, 96, 38, 6)
			}
			ui.Column(c).Grow(3).Basis(0).MinWidth(0).Gap(3).Children(func() {
				color := p.text
				if playing {
					color = p.accent
				}
				ui.Text(c, tr.Name).SingleLine().FontSize(14).FontWeight(500).TextColor(color)
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, providerName(tr.Provider)).FontSize(9).TextColor(p.accent)
					a.artistLinks(c, tr, p.muted, p.text).FontSize(12.5).SingleLine().Shrink(1).MinWidth(0)
				})
			})
			if wide {
				ui.Text(c, tr.Album.Name).Grow(2).Basis(0).MinWidth(0).FontSize(12.5).TextColor(p.muted).SingleLine()
			}
			ui.Box(c).Size(32, 32).Center().Children(func() {
				if a.liked[tr.Key()] || hovered {
					a.likeButton(c.Key("like"), tr, 15, p.muted)
				}
			})
			ui.Text(c, clock(tr.Length())).FontSize(12.5).FontFeatures("tnum").TextColor(p.faint).Width(44).TextAlign(ui.End)
		})
		if row.Clicked() && tr.Playable() {
			a.pl.PlayQueue(tracks, i)
		}
	}).Grow(1).Padding(top+pageGap, pagePad-12, bottom+32, pagePad-12).ScrollbarInsets(top, 0, bottom, 0)
}
func (a *app) nowPlayingBars(c *ui.Context) {
	color := a.pal.accent
	ui.Box(c).Size(16, 14).Draw(func(p *ui.Painter, r ui.Rect) {
		lv, live := a.pl.Levels()
		live = live && a.visualActive
		a.rowBars.step(p.Now(), lv, live)
		drawBars(p, r, &a.rowBars, 4, color, 2.5)
		if a.visualActive && (live || a.rowBars.group(0, player.Bands) > .01) {
			p.AnimationFrame()
		}
	})
}
func weekday(t time.Time) string {
	return [...]string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}[t.Weekday()]
}
