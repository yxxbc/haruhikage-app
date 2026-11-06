package app

import (
	"context"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/yxxbc/haruhikage-app/internal/domain"
	"strings"
)

type searchData struct {
	tracks        []domain.Track
	total, page   int
	loading, more bool
	err           string
}

const searchHistoryLimit = 20

func providerName(id string) string {
	switch id {
	case "wy":
		return "网易云"
	case "tx":
		return "QQ 音乐"
	case "kg":
		return "酷狗"
	case "kw":
		return "酷我"
	case "mg":
		return "咪咕"
	}
	return id
}
func (a *app) search(query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}
	a.rememberSearch(query)
	r := route{page: pageSearch, query: query, provider: a.settings.Provider}
	if a.route == r {
		a.loadSearch(r, true)
	} else {
		a.open(r)
	}
}
func (a *app) rememberSearch(q string) {
	history := []string{q}
	for _, v := range a.settings.SearchHistory {
		if v != q && len(history) < searchHistoryLimit {
			history = append(history, v)
		}
	}
	a.settings.SearchHistory = history
	_ = a.settings.save()
}
func (a *app) clearSearchHistory() { a.settings.SearchHistory = nil; _ = a.settings.save() }
func (a *app) loadSearch(r route, again bool) {
	if a.dataCancel != nil {
		a.dataCancel()
	}
	a.dataGen++
	a.searchData = &searchData{more: true}
	a.moreResults(r, a.searchData)
}
func (a *app) moreResults(r route, d *searchData) {
	if d == nil || d.loading || !d.more {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.dataCancel = cancel
	gen := a.dataGen
	page := d.page + 1
	d.loading = true
	d.err = ""
	a.run(func() {
		result, err := a.catalog.Search(ctx, r.provider, r.query, page)
		a.post(func() {
			if a.dataGen != gen || a.route != r || a.searchData != d {
				return
			}
			d.loading = false
			if err != nil {
				if ctx.Err() == nil {
					d.err = "搜索失败，请重试或切换平台"
				}
				return
			}
			d.tracks = append(d.tracks, result.Tracks...)
			d.total = result.Total
			d.more = result.More && len(result.Tracks) > 0
			d.page = page
		})
	})
}
func (a *app) searchPage(c *ui.Context) {
	d := a.searchData
	if d == nil {
		a.centered(c, "请输入关键词搜索", nil)
		return
	}
	header := func(c *ui.Context) {
		a.heading(c, a.route.query, fmt.Sprintf("%s · 已加载 %d 首歌曲", providerName(a.route.provider), len(d.tracks)))
		ui.Row(c).Gap(6).Wrap().Children(func() {
			for _, p := range a.catalog.Providers() {
				if ui.Button(c, p.Name).Disabled(a.route.provider == p.ID).Clicked() {
					a.settings.Provider = p.ID
					_ = a.settings.save()
					a.open(route{page: pageSearch, query: a.route.query, provider: p.ID})
				}
			}
		})
	}
	if d.loading && len(d.tracks) == 0 {
		a.trackSkeleton(c, a.pageShimmer(c), true, header)
		return
	}
	a.trackRows(c, d.tracks, true, header, func(c *ui.Context) {
		ui.Column(c).PaddingY(16).Gap(12).Children(func() {
			if d.err != "" {
				ui.Text(c, d.err).TextColor(a.pal.accent)
				if ui.Button(c, "重试搜索").Clicked() {
					a.moreResults(a.route, d)
				}
			} else if len(d.tracks) == 0 {
				a.empty(c, "没有找到歌曲")
			}
			if d.loading {
				ui.Text(c, "正在加载…").TextColor(a.pal.muted)
			} else if d.more {
				if ui.Button(c, "加载更多").Clicked() {
					a.moreResults(a.route, d)
				}
			}
		})
	})
}
