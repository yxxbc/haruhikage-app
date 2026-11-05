package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yxxbc/haruhikage-app/internal/netease"
)

// recentItem is a row of the recent page: a day's heading, or a play.
type recentItem struct {
	day   string
	count int
	play  *netease.Play
	// track is the play's place among the plays that can be played, or -1
	// for one that cannot.
	track int
}

// recentPage shows the songs the listener played on any device signed in
// as them, latest first, under the day they were heard.
func (a *app) recentPage(c *ui.Context) {
	if a.profile == nil {
		a.recentLogin(c)
		return
	}
	if len(a.recent) == 0 {
		switch {
		case a.recentLoading:
			a.centered(c, "正在读取最近播放…", nil)
		case a.recentErr != "":
			a.centered(c, a.recentErr, a.loadRecent)
		default:
			a.centered(c, "还没有最近播放", nil)
		}
		return
	}

	p := a.pal
	cur := a.pl.Current()
	now := time.Now()
	w, _ := c.Size()
	// A narrow page leaves the album out, as a playlist's rows do.
	wide := w-sidebarWidth > 820
	items, tracks := recentRows(a.recent, now)
	sub := fmt.Sprintf("在手机、电脑和网页上听过的歌 · %d 首", len(a.recent))
	top, bottom := a.insets(c)
	// Rows out of view are not built, their marks with them.
	a.underTop, a.underBottom = true, true
	rows := len(items) + 1
	ui.List(c.Key(a.route), &a.trackList, rows, func(i int) {
		if i == 0 {
			ui.Column(c).Children(func() {
				a.startMark(c)
				a.heading(c, "最近播放", sub)
			})
			return
		}
		if i == rows-1 {
			defer a.endMark(c)
		}
		item := &items[i-1]
		if item.play == nil {
			ui.Row(c).Height(50).Padding(0, 12, 8, 12).AlignItems(ui.End).Gap(8).Children(func() {
				ui.Text(c, item.day).FontSize(14).FontWeight(700)
				ui.Textf(c, "%d 首", item.count).FontSize(11.5).TextColor(p.faint)
			})
			return
		}
		play := item.play
		tr := &play.Track
		playing := cur != nil && cur.ID == tr.ID
		row := ui.ButtonBase(c.Key(i)).Height(54).PaddingX(12).Gap(14).Radius(10).AlignItems(ui.Center).FocusRing(false).Label(tr.Name).
			ContextMenu(a.songItems(tr, 0))
		hovered := row.Hovered()
		bg := ui.Transparent
		switch {
		case playing:
			bg = p.accent.Alpha(0.09)
		case hovered:
			bg = p.hover
		}
		row.Background(row.AnimateColor("bg", bg, ui.Animation{Duration: 100 * time.Millisecond}))
		if !tr.Playable() {
			row.Opacity(0.42)
		}
		row.Children(func() {
			a.cover(c, tr.Album.PicURL, 96, 38, 6)
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(3).Children(func() {
				name := p.text
				if playing {
					name = p.accent
				}
				spans := []ui.Span{{Text: tr.Name, Weight: 500, Color: name}}
				if len(tr.Alias) > 0 {
					spans = append(spans, ui.Span{Text: "（" + tr.Alias[0] + "）", Color: p.faint})
				}
				ui.RichText(c, spans...).FontSize(14).SingleLine()
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					if !tr.Playable() {
						ui.Text(c, "VIP").FontSize(9).FontWeight(800).TextColor(p.accent).Padding(0, 4).Radius(3).Border(1, p.accent.Alpha(0.6))
					}
					a.artistLinks(c, tr, p.muted, p.text).FontSize(12.5).SingleLine().Shrink(1).MinWidth(0)
					if tr.Reason != "" {
						ui.Text(c, tr.Reason).FontSize(10.5).FontWeight(600).TextColor(p.accent).Padding(1, 6).Radius(999).Background(p.accent.Alpha(0.1)).SingleLine()
					}
				})
			})
			if wide && tr.Album.Name != "" {
				album := ui.RichText(c).Grow(2).Basis(0).MinWidth(0).FontSize(12.5).TextColor(p.muted).SingleLine()
				album.Children(func() {
					if tr.Album.ID == 0 {
						ui.Text(c, tr.Album.Name)
					} else {
						a.link(c.Key("album"), tr.Album.Name, p.muted, p.text, route{page: pageAlbum, id: tr.Album.ID})
					}
				})
			}
			if when := playedAt(*play); when != "" {
				ui.Text(c, when).FontSize(11.5).TextColor(p.faint).Width(90).TextAlign(ui.End).SingleLine()
			}
			// A liked song's heart shows; the others' as the pointer is on them.
			ui.Box(c).Size(32, 32).Center().Children(func() {
				if a.liked[tr.ID] || hovered {
					a.likeButton(c.Key("like"), tr, 15, p.muted)
				}
			})
			ui.Text(c, clock(tr.Length())).FontSize(12.5).FontFeatures("tnum").TextColor(p.faint).Width(44).TextAlign(ui.End)
		})
		if row.Clicked() && item.track >= 0 {
			a.pl.PlayQueue(tracks, item.track)
		}
	}).Grow(1).Padding(top+pageGap, pagePad-12, bottom+32, pagePad-12).ScrollbarInsets(top, 0, bottom, 0)
}

// recentLogin asks the listener to sign in, so that the plays of their
// devices have a name to belong to.
func (a *app) recentLogin(c *ui.Context) {
	p := a.pal
	top, bottom := a.insets(c)
	ui.Column(c).Grow(1).Padding(top, pagePad, bottom, pagePad).Center().Gap(12).Children(func() {
		ui.Icon(c, iconHistory).Size(34, 34).TextColor(p.faint)
		ui.Text(c, "登录后，这里会列出你在手机、电脑和网页上听过的歌").FontSize(13).TextColor(p.muted).TextAlign(ui.Center)
		if ui.PrimaryButton(c.Key("login"), "扫码登录").Clicked() {
			a.showMain()
			a.openLogin()
			if a.win != nil {
				a.win.Invalidate()
			}
		}
	})
}

// loadRecent loads the songs the listener played last. A signed out
// listener has none.
func (a *app) loadRecent() {
	if a.profile == nil || a.recentLoading {
		return
	}
	uid := a.profile.UserID
	a.recentLoading, a.recentErr = true, ""
	go func() {
		plays, err := recentPlays(100)
		a.post(func() {
			if a.profile == nil || a.profile.UserID != uid {
				return
			}
			a.recentLoading = false
			if err != nil {
				if len(a.recent) == 0 {
					a.recentErr = "无法获取最近播放，请检查网络"
				} else {
					a.toast = "刷新最近播放失败，请检查网络"
				}
				return
			}
			a.recent = plays
		})
	}()
}

// recentRows groups plays by the day they were heard, with a heading row
// before each day, and returns the rows and the tracks they can play, in
// the order they show.
func recentRows(plays []netease.Play, now time.Time) ([]recentItem, []netease.Track) {
	tracks := make([]netease.Track, 0, len(plays))
	at := make([]int, len(plays))
	for i := range plays {
		at[i] = -1
		if plays[i].Track.Playable() {
			at[i] = len(tracks)
			tracks = append(tracks, plays[i].Track)
		}
	}
	items := make([]recentItem, 0, len(plays)+8)
	for i := 0; i < len(plays); {
		day := recentDay(plays[i].At, now)
		j := i
		for j < len(plays) && recentDay(plays[j].At, now) == day {
			j++
		}
		items = append(items, recentItem{day: day, count: j - i})
		for k := i; k < j; k++ {
			items = append(items, recentItem{play: &plays[k], track: at[k]})
		}
		i = j
	}
	return items, tracks
}

// recentDay names the day a play was heard on.
func recentDay(t, now time.Time) string {
	if t.IsZero() {
		return "更早"
	}
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "今天"
	case t.Format("20060102") == now.AddDate(0, 0, -1).Format("20060102"):
		return "昨天"
	case y1 == y2:
		return fmt.Sprintf("%d月%d日", m1, d1)
	default:
		return fmt.Sprintf("%d年%d月%d日", y1, m1, d1)
	}
}

// playedAt says when, and on what, a play was heard.
func playedAt(p netease.Play) string {
	if p.At.IsZero() {
		return ""
	}
	when := p.At.Format("15:04")
	if d := deviceName(p.Device); d != "" {
		when += " · " + d
	}
	return when
}
