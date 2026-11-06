package app

import (
	"context"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/yxxbc/haruhikage-app/internal/catalogbrowse"
)

func (a *app) loadBrowse(r route) {
	ctx, cancel := context.WithCancel(a.ctx)
	a.dataCancel = cancel
	gen := a.dataGen
	a.dataLoading = true
	a.browseTitle = ""
	a.charts = nil
	a.run(func() {
		var charts []catalogbrowse.Chart
		var detail catalogbrowse.Detail
		var err error
		switch r.page {
		case pageCharts:
			if r.id == "" {
				charts, err = a.browse.Charts(ctx)
			} else {
				detail, err = a.browse.ChartTracks(ctx, r.id)
			}
		case pageArtist:
			detail, err = a.browse.Artist(ctx, r.id)
		case pageAlbum:
			detail, err = a.browse.Album(ctx, r.id)
		}
		a.post(func() {
			if a.dataGen != gen || a.route != r {
				return
			}
			a.dataLoading = false
			if err != nil {
				if ctx.Err() == nil {
					a.dataErr = "曲库读取失败，请重试"
				}
				return
			}
			a.charts = charts
			a.tracks = detail.Tracks
			a.browseTitle = detail.Title
		})
	})
}
func (a *app) browsePage(c *ui.Context) {
	if a.dataErr != "" {
		a.centered(c, a.dataErr, func() { a.load(a.route) })
		return
	}
	if a.route.page == pageCharts && a.route.id == "" {
		a.scrollPage(c, func() {
			a.heading(c, "排行榜", "网易云匿名榜单")
			if a.dataLoading {
				a.empty(c, "正在加载榜单…")
				return
			}
			ui.Grid(c).Columns(a.columns(c, 180)).Gap(18).Children(func() {
				for _, chart := range a.charts {
					b := ui.ButtonBase(c.Key(chart.ID)).Column().Gap(10).Label(chart.Name)
					b.Children(func() { a.cover(c, chart.Cover, 300, 150, 12); ui.Text(c, chart.Name).FontWeight(600).SingleLine() })
					if b.Clicked() {
						a.open(route{page: pageCharts, provider: "wy", id: chart.ID})
					}
				}
			})
		})
		return
	}
	header := func(c *ui.Context) {
		a.heading(c, a.browseTitle, fmt.Sprintf("%d 首歌曲", len(a.tracks)))
		if pillButton(c, iconPlay, "播放全部", a.pal.accent, a.pal.onAccent).Disabled(len(a.tracks) == 0).Clicked() {
			a.pl.PlayQueue(a.tracks, 0)
		}
	}
	if a.dataLoading {
		a.trackSkeleton(c, a.pageShimmer(c), true, header)
		return
	}
	a.trackRows(c, a.tracks, true, header)
}
