package app

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/yxxbc/haruhikage-app/internal/player"
	"strconv"
	"time"
)

func (a *app) openSettings() { a.showMain(); a.open(route{page: pageSettings}) }
func (a *app) changed() {
	if err := a.settings.save(); err != nil {
		a.toast = "设置保存失败"
	}
	a.settings.applyAppearance()
	a.redraw()
}
func (a *app) setting(c *ui.Context, title, detail string, control func()) {
	ui.Row(c).PaddingY(14).Gap(24).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(a.pal.hairline).Children(func() {
		ui.Column(c).Grow(1).Gap(4).Children(func() {
			ui.Text(c, title).FontWeight(600).FontSize(14)
			if detail != "" {
				ui.Text(c, detail).FontSize(12).TextColor(a.pal.muted)
			}
		})
		control()
	})
}
func (a *app) settingsPage(c *ui.Context) {
	a.scrollPage(c, func() {
		a.heading(c, "设置", "设置可恢复默认；歌单和音源不受影响")
		a.section(c, "播放")
		a.setting(c, "默认平台", "搜索歌曲的平台", func() {
			labels := []string{}
			choice := providerName(a.settings.Provider)
			for _, p := range a.catalog.Providers() {
				labels = append(labels, p.Name)
			}
			if ui.Select(c, &choice, labels).Label("默认平台").Width(160).Changed() {
				for _, p := range a.catalog.Providers() {
					if p.Name == choice {
						a.settings.Provider = p.ID
					}
				}
				a.changed()
			}
		})
		a.setting(c, "优先音质", providerName(a.settings.Provider)+"；仅显示当前音源声明的音质，下一次解析生效", func() {
			support, ok := a.sourceSupport(a.settings.Provider)
			if !ok {
				ui.Text(c, "请先启用音源").TextColor(a.pal.muted)
				return
			}
			choice := a.settings.Qualities[a.settings.Provider]
			if choice == "" {
				choice = a.settings.Quality
			}
			if ui.Select(c, &choice, support.Qualitys).Label("优先音质").Width(150).Changed() {
				a.settings.Qualities[a.settings.Provider] = choice
				a.settings.Quality = choice
				a.changed()
			}
		})
		a.setting(c, "音量均衡", "仅在解析结果提供真实响度数据时生效", func() {
			if ui.Switch(c, &a.settings.Normalize).Label("音量均衡").Changed() {
				a.changed()
			}
		})
		a.setting(c, "均衡器", "开关和曲线在下一首生效，当前歌曲保持原音色", func() {
			if ui.Switch(c, &a.settings.Equalizer).Label("均衡器").Changed() {
				a.changed()
			}
		})
		a.setting(c, "均衡曲线", "自动分析不阻挡首播；无分析结果时按原声播放", func() {
			presets := player.EQPresets()
			labels := []string{}
			choice := ""
			for _, p := range presets {
				labels = append(labels, p.Label)
				if p.ID == a.settings.EQPreset {
					choice = p.Label
				}
			}
			if choice == "" && len(labels) > 0 {
				choice = labels[0]
			}
			if ui.Select(c, &choice, labels).Label("均衡曲线").Width(160).Disabled(!a.settings.Equalizer).Changed() {
				for _, p := range presets {
					if p.Label == choice {
						a.settings.EQPreset = p.ID
					}
				}
				a.changed()
			}
		})
		a.setting(c, "睡眠定时", a.sleepDesc(), func() {
			ui.Button(c, "选择定时").Menu(func(m *ui.Menu) {
				for _, mins := range sleepChoices {
					if m.Item(fmt.Sprintf("%d 分钟", mins)).Chosen() {
						a.pl.SetSleep(time.Duration(mins) * time.Minute)
					}
				}
				if m.Item("播完本首").Chosen() {
					a.pl.SetSleepAtEnd()
				}
				if m.Item("取消定时").Chosen() {
					a.pl.CancelSleep()
				}
			})
		})
		a.section(c, "外观与动态效果")
		a.setting(c, "外观", "跟随系统，或手动选择", func() {
			choice := map[string]string{"system": "跟随系统", "light": "浅色", "dark": "深色"}[a.settings.Appearance]
			if ui.Select(c, &choice, []string{"跟随系统", "浅色", "深色"}).Label("外观").Width(150).Changed() {
				for k, v := range map[string]string{"system": "跟随系统", "light": "浅色", "dark": "深色"} {
					if choice == v {
						a.settings.Appearance = k
					}
				}
				a.changed()
			}
		})
		a.setting(c, "减少动态效果", "同时遵循系统的减少动态效果偏好", func() {
			if ui.Switch(c, &a.settings.ReduceMotion).Label("减少动态效果").Changed() {
				a.changed()
			}
		})
		a.setting(c, "进度条烟雾", "可选效果；隐藏窗口后停止绘制", func() {
			if ui.Switch(c, &a.settings.Smoke).Label("进度条烟雾").Changed() {
				a.changed()
			}
		})
		a.setting(c, "逐字律动火花", "可选歌词效果", func() {
			if ui.Switch(c, &a.settings.StageSparks).Label("逐字律动火花").Changed() {
				a.changed()
			}
		})
		a.setting(c, "播放页背景", "静态背景不需要持续刷新", func() {
			choice := a.settings.Backdrop
			labels := []string{"静态", "雾", "流动"}
			display := map[string]string{"": "静态", "fog": "雾", "latent": "流动"}[choice]
			if ui.Select(c, &display, labels).Label("播放页背景").Width(150).Changed() {
				for k, v := range map[string]string{"": "静态", "fog": "雾", "latent": "流动"} {
					if display == v {
						a.settings.Backdrop = k
					}
				}
				a.changed()
			}
		})
		a.setting(c, "背景斑驳", "仅适用于流动背景", func() {
			if ui.Switch(c, &a.settings.BackdropPatches).Label("背景斑驳").Disabled(a.settings.Backdrop != "latent").Changed() {
				a.changed()
			}
		})
		a.section(c, "缓存与资料库")
		a.setting(c, "音频缓存上限", "关闭持久缓存仍使用有界临时文件，播放后回收", func() {
			labels := []string{}
			choice := fmt.Sprintf("%d MB", a.settings.CacheMB)
			for _, v := range cacheLimits {
				labels = append(labels, v.label)
				if v.mb == a.settings.CacheMB {
					choice = v.label
				}
			}
			if ui.Select(c, &choice, labels).Label("缓存上限").Width(160).Changed() {
				for _, v := range cacheLimits {
					if v.label == choice {
						a.settings.CacheMB = v.mb
					}
				}
				a.applyCacheLimit()
				a.changed()
			}
		})
		a.setting(c, "自定义缓存上限", "单位 MB，0 关闭持久缓存", func() {
			if a.cacheLimitText == "" {
				a.cacheLimitText = strconv.Itoa(a.settings.CacheMB)
			}
			in := ui.TextInput(c, &a.cacheLimitText).Width(120).Label("自定义缓存上限")
			if in.Submitted() {
				n, e := strconv.Atoi(a.cacheLimitText)
				if e != nil || n < 0 || n > 102400 {
					a.toast = "请输入 0–102400 MB"
				} else {
					a.settings.CacheMB = n
					a.applyCacheLimit()
					a.changed()
				}
			}
		})
		ui.Row(c).Gap(12).MarginY(16).Children(func() {
			if ui.Button(c, "清空音频缓存").Clicked() {
				a.clearCache()
			}
			if ui.Button(c, "导出资料库").Disabled(a.lib == nil).Clicked() {
				a.exportLibrary()
			}
			if ui.Button(c, "导入备份").Disabled(a.lib == nil).Clicked() {
				a.importLibrary()
			}
		})
		a.setting(c, "搜索历史", "只保存在本机", func() {
			if ui.Button(c, "清空搜索历史").Clicked() {
				a.clearSearchHistory()
			}
		})
		if ui.Button(c, "恢复全部默认设置").MarginY(24).Clicked() {
			path := a.settings.path
			a.settings = defaultSettings()
			a.settings.path = path
			a.cacheLimitText = ""
			a.volume = a.settings.Volume
			a.pl.SetVolume(a.volume)
			a.applyCacheLimit()
			a.changed()
			a.toast = "已恢复默认设置，歌单和音源已保留"
		}
	})
}
