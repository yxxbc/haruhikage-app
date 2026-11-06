package app

import (
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/yxxbc/haruhikage-app/internal/domain"
	"runtime"
	"time"
)

const sidebarWidth = 232
const topBarHeight = 40
const topEdgePad = 8
const buttonsHeight = topBarHeight + topEdgePad
const pageGap = 20

func topBar(c *ui.Context) float32 {
	if runtime.GOOS != "darwin" {
		return buttonsHeight
	}
	return max(c.TitleBar().Height, topBarHeight)
}
func (a *app) insets(c *ui.Context) (float32, float32) {
	bottom := float32(0)
	if a.pl.Current() != nil {
		bottom = playerBarHeight
	}
	return topBar(c), bottom
}

func (a *app) view(c *ui.Context) {
	t := *c.Theme()
	a.pal = paletteFor(&t)
	t.Accent, t.AccentText = a.pal.accent, a.pal.onAccent
	t.Selection = a.pal.accent.Alpha(.3)
	c.SetTheme(&t)
	a.shortcuts(c)
	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	}
	root := ui.Box(c).Fill()
	if !c.Focused(a.searchBox) {
		root.OnShortcut(0, ui.KeySpace, a.pl.TogglePlay)
		c.OnShortcut(0, ui.KeySpace, a.pl.TogglePlay)
	}
	opened, settled := a.npState(c)
	root.Children(func() {
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
			if opened && settled {
				return
			}
			side := ui.Column(c).Width(sidebarWidth)
			if !c.Vibrancy() {
				side.Background(a.pal.sidebar)
			}
			side.Children(func() { a.sidebar(c) })
			ui.Box(c.Key("main")).Grow(1).Background(a.pal.bg).BorderWidth(0, 0, 0, 1).BorderColor(a.pal.hairline).Children(func() { a.content(c); a.pageTop(c); a.playerBar(c) })
		})
		a.nowPlayingLayer(c, opened, settled)
	})
	a.playlistNameDialog(c)
	a.deletePlaylistDialog(c)
	if a.toast != "" {
		c.Toast(a.toast)
		a.toast = ""
	}
	a.syncWindows()
}
func (a *app) shortcuts(c *ui.Context) {
	c.OnShortcut(0, ui.KeyBack, a.back)
	c.OnShortcut(0, ui.KeyForward, a.goForward)
	c.OnShortcut(ui.Cmd, ui.KeyBracketLeft, a.back)
	c.OnShortcut(ui.Cmd, ui.KeyBracketRight, a.goForward)
	c.OnShortcut(ui.Cmd, ui.KeyF, func() { a.nowPlaying = false; a.searchBox.Focus(c) })
	if a.nowPlaying {
		c.OnShortcut(0, ui.KeyEscape, func() { a.nowPlaying = false })
		if a.pl.Current() != nil {
			for i, t := range a.npTabs() {
				c.OnShortcut(ui.Cmd, ui.Key1+ui.Key(i), func() { a.choosePane(t.pane) })
			}
		}
	}
}
func (a *app) open(r route) {
	a.nowPlaying = false
	if r == a.route {
		return
	}
	a.history = append(a.history, a.route)
	if len(a.history) > 100 {
		a.history = a.history[1:]
	}
	a.forward = nil
	a.show(r)
}
func (a *app) show(r route) {
	a.route = r
	if r.page == pageSearch {
		a.query = r.query
		a.searched = r
	}
	a.load(r)
}
func (a *app) back() {
	if a.nowPlaying {
		a.nowPlaying = false
		return
	}
	if len(a.history) == 0 {
		return
	}
	a.forward = append(a.forward, a.route)
	r := a.history[len(a.history)-1]
	a.history = a.history[:len(a.history)-1]
	a.show(r)
}
func (a *app) goForward() {
	if len(a.forward) == 0 {
		return
	}
	a.history = append(a.history, a.route)
	r := a.forward[len(a.forward)-1]
	a.forward = a.forward[:len(a.forward)-1]
	a.nowPlaying = false
	a.show(r)
}
func (a *app) sidebar(c *ui.Context) {
	top := float32(14)
	if c.TitleBar().Left > 0 {
		top = topBar(c) + 4
	}
	ui.Column(c).Fill().Padding(top, 14, 14, 14).Gap(4).DragWindow().Children(func() {
		input := ui.TextInput(c.Key("search"), &a.query).Bind(&a.searchBox).Placeholder("搜索歌曲、歌手").Label("搜索歌曲").OnSubmit(func() { a.search(a.query) })
		_ = input
		ui.Scroll(c.Key("nav")).TrackScroll(&a.navScroll).Grow(1).Margin(8, -6).Padding(0, 6).Gap(4).Children(func() {
			a.navItem(c, iconHome, "我的音乐", route{page: pageHome})
			a.navItem(c, iconHeart, "我喜欢", route{page: pageFavorites})
			a.navItem(c, iconHistory, "最近播放", route{page: pageRecent})
			if a.searched.query != "" {
				a.navItem(c, iconSearch, "搜索结果", a.searched)
			}
			a.navItem(c, iconCharts, "排行榜", route{page: pageCharts, provider: "wy"})
			a.navItem(c, iconNote, "音源管理", route{page: pageSources})
			ui.Row(c).Padding(10, 10, 4, 10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "我的歌单").Grow(1).FontSize(12).TextColor(a.pal.muted)
				if iconButton(c, iconPlus, 12, a.pal.muted, "新建歌单").Clicked() {
					a.newPlaylist(nil)
				}
			})
			for _, pl := range a.mine {
				a.navItem(c.Key(pl.ID), iconNote, pl.Name, route{page: pagePlaylist, id: pl.ID})
			}
		})
		if t := a.pl.Current(); t != nil {
			a.sidebarNowPlaying(c, t)
		}
		a.navItem(c, iconSettings, "设置", route{page: pageSettings})
	})
}
func (a *app) navItem(c *ui.Context, icon *ui.SVG, label string, r route) {
	selected := a.route == r && !a.nowPlaying
	b := ui.ButtonBase(c.Key(label)).Height(34).PaddingX(10).Gap(10).Radius(8).AlignItems(ui.Center).Justify(ui.Start).FocusRing(false).Label(label)
	bg := ui.Transparent
	if selected {
		bg = a.pal.press
	} else if b.Hovered() {
		bg = a.pal.hover
	}
	b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond}))
	b.Children(func() {
		ui.Icon(c, icon).Size(17, 17).TextColor(a.pal.muted)
		ui.Text(c, label).FontSize(13.5).FontWeight(500).TextColor(a.pal.text).SingleLine()
	})
	if b.Clicked() {
		a.open(r)
	}
}
func (a *app) sidebarNowPlaying(c *ui.Context, t *domain.Track) {
	b := ui.ButtonBase(c.Key("np-card")).Padding(8).Gap(10).Radius(12).AlignItems(ui.Center).Label("正在播放").Background(a.pal.hover)
	b.Children(func() {
		a.cover(c, t.Album.PicURL, 96, 42, 7)
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, t.Name).SingleLine().FontSize(13).FontWeight(600)
			ui.Text(c, t.ArtistNames()).SingleLine().FontSize(11).TextColor(a.pal.muted)
		})
	})
	if b.Clicked() {
		a.nowPlaying = true
	}
}
func edgeShown(el ui.Element, under bool) float32 {
	v := float32(0)
	if under {
		v = 1
	}
	return el.AnimateTo("shown", v, ui.Animation{Duration: 180 * time.Millisecond})
}
func (a *app) startMark(c *ui.Context) {
	b := ui.Box(c.Key("start-mark")).Height(0).Bounds()
	if b.W > 0 {
		top, _ := a.insets(c)
		a.underTop = b.Y < top+pageGap-.5
	}
}
func (a *app) endMark(c *ui.Context) {
	b := ui.Box(c.Key("end-mark")).Height(0).Bounds()
	if b.W > 0 {
		_, h := c.Size()
		_, bottom := a.insets(c)
		a.underBottom = b.Y > h-bottom+.5
	}
}
func (a *app) pageTop(c *ui.Context) {
	top, _ := a.insets(c)
	edge := ui.Box(c.Key("top-edge")).Absolute().Top(0).Left(0).Right(0).Height(top).DragWindow().Material(glass.ScrollEdge{Hard: true, Background: a.pal.bg})
	edge.Opacity(edgeShown(edge, a.underTop))
	if len(a.history) > 0 || len(a.forward) > 0 {
		ui.Row(c).Absolute().Top(0).Left(20).Height(topBar(c)).AlignItems(ui.Center).Children(func() {
			if iconButton(c.Key("back"), iconBack, 18, a.pal.muted, "返回").Disabled(len(a.history) == 0).Clicked() {
				a.back()
			}
			if iconButton(c.Key("forward"), iconForward, 18, a.pal.muted, "前进").Disabled(len(a.forward) == 0).Clicked() {
				a.goForward()
			}
		})
	}
}
func (a *app) content(c *ui.Context) {
	if a.scrolled != a.route {
		a.trackList = ui.ListState{}
		a.pageScroll = ui.ScrollState{}
		a.scrolled = a.route
	}
	a.underTop, a.underBottom = false, false
	switch a.route.page {
	case pageHome:
		a.homePage(c)
	case pageFavorites, pageRecent, pagePlaylist:
		a.libraryPage(c)
	case pageSearch:
		a.searchPage(c)
	case pageSources:
		a.sourcesPage(c)
	case pageSettings:
		a.settingsPage(c)
	case pageCharts, pageArtist, pageAlbum:
		a.browsePage(c)
	}
}
func (a *app) centered(c *ui.Context, message string, retry func()) {
	top, bottom := a.insets(c)
	ui.Column(c).Grow(1).Padding(top, 0, bottom, 0).Center().Gap(12).Children(func() {
		ui.Text(c, message).TextColor(a.pal.muted).FontSize(13)
		if retry != nil && ui.Button(c, "重试").Clicked() {
			retry()
		}
	})
}
func (a *app) scrollPage(c *ui.Context, body func()) {
	top, bottom := a.insets(c)
	ui.Scroll(c.Key(a.route)).TrackScroll(&a.pageScroll).Grow(1).Padding(top+pageGap, pagePad, bottom+32, pagePad).ScrollbarInsets(top, 0, bottom, 0).Children(func() { a.startMark(c); body(); a.endMark(c) })
}
