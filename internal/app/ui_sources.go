package app

import (
	"context"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/yxxbc/haruhikage-app/internal/source"
	"sort"
	"strings"
	"time"
)

func (a *app) sourceSupport(provider string) (source.Support, bool) {
	for _, info := range a.sourceList {
		if info.Active && info.Enabled {
			s, ok := info.Sources[provider]
			return s, ok
		}
	}
	return source.Support{}, false
}
func (a *app) sourceAction(action func(context.Context) error) {
	if a.sourceBusy || a.sources == nil {
		return
	}
	a.sourceBusy = true
	a.run(func() {
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		err := action(ctx)
		snapshot := a.sources.List()
		a.post(func() {
			a.sourceBusy = false
			a.sourceList = snapshot
			if err != nil {
				a.toast = "音源操作失败，请检查脚本或重试"
			} else {
				a.toast = "音源已更新"
			}
		})
	})
}
func (a *app) importSource() {
	if a.sources == nil || a.sourceBusy {
		return
	}
	parent := a.win
	a.run(func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: "导入 LX 音源脚本", Filters: []mygo.FileFilter{{Name: "JavaScript 音源", Extensions: []string{"js"}}}})
		a.post(func() {
			if err != nil {
				a.toast = "无法打开文件选择器"
				return
			}
			if len(paths) > 0 {
				path := paths[0]
				a.sourceAction(func(ctx context.Context) error { _, err := a.sources.Import(ctx, path); return err })
			}
		})
	})
}
func (a *app) exportSource(info source.Info) {
	parent := a.win
	a.run(func() {
		result, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Title: "导出音源脚本", Message: "导出的 JS 包含脚本原文及其中的凭证，请自行保管。", Buttons: []string{"取消", "导出"}, CancelButton: 0})
		if err != nil || result.Button != 1 {
			return
		}
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: parent, Title: "导出音源", DefaultPath: "music-source.js", Filters: []mygo.FileFilter{{Name: "JavaScript", Extensions: []string{"js"}}}})
		if err != nil || path == "" {
			return
		}
		err = a.sources.Export(info.ID, path)
		a.post(func() {
			if err != nil {
				a.toast = "导出失败"
			} else {
				a.toast = "音源已导出"
			}
		})
	})
}
func (a *app) removeSource(info source.Info) {
	parent := a.win
	a.run(func() {
		res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Title: "删除音源", Message: "删除“" + info.Name + "”？本地歌单和歌曲不会删除。", Buttons: []string{"取消", "删除"}, CancelButton: 0})
		if err == nil && res.Button == 1 {
			a.post(func() { a.sourceAction(func(ctx context.Context) error { return a.sources.Remove(ctx, info.ID) }) })
		}
	})
}
func (a *app) sourcesPage(c *ui.Context) {
	a.scrollPage(c, func() {
		a.heading(c, "音源管理", "曲库负责搜索和歌词；导入的脚本负责解析播放地址")
		if ui.Button(c, "导入 JS 音源").Disabled(a.sourceBusy || a.sources == nil).Clicked() {
			a.importSource()
		}
		ui.Text(c, "初始化会执行脚本声明的联网操作；不会自动批量解析歌曲。脚本与 Key 只保存本机。").FontSize(12).TextColor(a.pal.muted).MarginY(14)
		if a.sourceBusy {
			ui.Text(c, "正在检查音源…").TextColor(a.pal.muted)
		}
		if len(a.sourceList) == 0 {
			a.empty(c, "还没有音源。导入你的 LX JavaScript 音源即可开始使用。")
		}
		for _, info := range a.sourceList {
			info := info
			ui.Column(c.Key(info.ID)).Padding(20).Gap(10).MarginY(8).Radius(14).Background(a.pal.hover).Children(func() {
				status := "已禁用"
				if info.Enabled {
					status = "可用"
				}
				if info.Active {
					status = "当前音源"
				}
				ui.Row(c).Gap(12).Children(func() {
					ui.Text(c, info.Name).FontSize(17).FontWeight(700).Grow(1)
					ui.Text(c, status).TextColor(a.pal.accent).FontSize(12)
				})
				ui.Text(c, "版本 "+info.Version+" · "+info.Author).FontSize(12).TextColor(a.pal.muted)
				ids := make([]string, 0, len(info.Sources))
				for id := range info.Sources {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					s := info.Sources[id]
					ui.Text(c, providerName(id)+"："+strings.Join(s.Qualitys, ", ")).FontSize(12).TextColor(a.pal.muted)
				}
				ui.Row(c).Gap(8).Wrap().Children(func() {
					if ui.Button(c, "设为当前").Disabled(a.sourceBusy || info.Active).Clicked() {
						a.sourceAction(func(ctx context.Context) error { return a.sources.Activate(ctx, info.ID) })
					}
					if ui.Button(c, "重新检查").Disabled(a.sourceBusy).Clicked() {
						a.sourceAction(func(ctx context.Context) error { _, err := a.sources.Check(ctx, info.ID); return err })
					}
					if ui.Button(c, "替换脚本").Disabled(a.sourceBusy).Clicked() {
						a.replaceSource(info)
					}
					if ui.Button(c, "禁用").Disabled(a.sourceBusy || !info.Enabled).Clicked() {
						a.sourceAction(func(ctx context.Context) error { return a.sources.Disable(ctx, info.ID) })
					}
					if ui.Button(c, "导出").Disabled(a.sourceBusy).Clicked() {
						a.exportSource(info)
					}
					if ui.Button(c, "删除").Disabled(a.sourceBusy).Clicked() {
						a.removeSource(info)
					}
				})
			})
		}
	})
}
func (a *app) replaceSource(info source.Info) {
	parent := a.win
	a.run(func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: "替换音源脚本", Filters: []mygo.FileFilter{{Name: "JavaScript", Extensions: []string{"js"}}}})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() {
			a.sourceAction(func(ctx context.Context) error { _, err := a.sources.Replace(ctx, info.ID, paths[0]); return err })
		})
	})
}
