package app

import (
	"fmt"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/yxxbc/haruhikage-app/internal/player"
)

// settingsSection is one category of the settings window's sidebar.
type settingsSection int

const (
	settingsGeneral settingsSection = iota
	settingsPlayback
	settingsAppearance
	settingsPodcasts
	settingsStorage
	settingsAccount
	settingsAbout
)

// settingsTabInfo is a category as the sidebar shows it.
type settingsTabInfo struct {
	id    settingsSection
	label string
	icon  *ui.SVG
}

// settingsTabs are the categories the window shows, in the sidebar's
// order. A category with nothing to set, as podcasts on a system that
// cannot write captions, is left out.
func (a *app) settingsTabs() []settingsTabInfo {
	tabs := make([]settingsTabInfo, 0, 7)
	// The app icon and the notch player are macOS's alone.
	if runtime.GOOS == "darwin" {
		tabs = append(tabs, settingsTabInfo{settingsGeneral, "通用", iconSettings})
	}
	tabs = append(tabs,
		settingsTabInfo{settingsPlayback, "播放", iconHeadset},
		settingsTabInfo{settingsAppearance, "外观", iconSun},
	)
	if a.captionsShown() {
		tabs = append(tabs, settingsTabInfo{settingsPodcasts, "播客", iconPodcast})
	}
	tabs = append(tabs,
		settingsTabInfo{settingsStorage, "存储", iconDrive},
		settingsTabInfo{settingsAccount, "账号", iconUser},
	)
	if a.updates {
		tabs = append(tabs, settingsTabInfo{settingsAbout, "关于", iconInfo})
	}
	return tabs
}

// settingsChosen returns the category chosen, or the first one when what
// was chosen is not in the sidebar now.
func (a *app) settingsChosen(tabs []settingsTabInfo) settingsSection {
	for _, t := range tabs {
		if t.id == a.settingsTab {
			return t.id
		}
	}
	return tabs[0].id
}

// openSettings shows the settings window, opening it the first time.
func (a *app) openSettings() {
	if a.settingsWin != nil {
		a.settingsWin.Show()
		a.settingsWin.Focus()
		a.measureCache()
		return
	}
	a.measureCache()
	a.settingsWin = mygo.NewWindow(mygo.WindowOptions{
		Title:           "设置",
		Width:           760,
		Height:          620,
		MinWidth:        680,
		MinHeight:       480,
		StateKey:        "settings-sidebar", // a size saved before the sidebar cannot fit it
		DisableMaximize: true,
		DisableMinimize: true,
		// The app's menu has nothing for this window: on Linux and
		// Windows it stays out of sight until Alt shows it.
		AutoHideMenuBar: true,
		BackgroundColor: "light-dark(#f4f4f6, #1c1c1f)",
		Content:         ui.View(a.settingsView),
	})
	a.settingsWin.OnClosed(func() {
		a.settingsWin = nil
		a.syncDock()
	})
	a.syncDock()
}

// changed applies the settings everywhere and keeps them.
func (a *app) changed() {
	a.settings.save()
	a.settings.applyAppearance()
	if a.notch != nil {
		a.notch.setEnabled(a.settings.Notch)
	}
	a.redraw()
}

// settingsView draws the settings window: a sidebar of categories, and
// the rows of the one chosen.
func (a *app) settingsView(c *ui.Context) {
	t := *c.Theme()
	p := paletteFor(&t)
	t.Accent, t.AccentText = p.accent, p.onAccent
	c.SetTheme(&t)
	c.Root().Background(p.sidebar)
	c.OnShortcut(ui.Cmd, ui.KeyW, func() { a.settingsWin.Close() })

	tabs := a.settingsTabs()
	tab := a.settingsChosen(tabs)
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		tab = a.settingsSidebar(c, tabs, tab, p)
		// The line is the border of the page, so that the sidebar needs no
		// width of its own for it.
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).BorderWidth(0, 0, 0, 1).BorderColor(p.hairline).Children(func() {
			a.settingsPane(c, tab, p)
		})
	})
}

// settingsSidebar draws the categories, marking the one shown, and
// returns it.
func (a *app) settingsSidebar(c *ui.Context, tabs []settingsTabInfo, active settingsSection, p palette) settingsSection {
	ui.Column(c).Width(200).Padding(14, 12).Gap(3).Children(func() {
		for _, tab := range tabs {
			b := ui.ButtonBase(c.Key(tab.id)).Height(34).PaddingX(10).Gap(10).Radius(8).AlignItems(ui.Center).Justify(ui.Start).FocusRing(false).Label(tab.label)
			// The click comes before the colors do, so that the category
			// it shows is marked in the frame it is chosen.
			if b.Clicked() {
				a.settingsTab = tab.id
				active = tab.id
			}
			selected := tab.id == active
			bg := ui.Transparent
			switch {
			case selected:
				bg = p.press
			case b.Hovered():
				bg = p.hover
			}
			b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond}))
			color := p.text
			if selected {
				color = p.accent
			}
			b.Children(func() {
				ui.Icon(c, tab.icon).Size(17, 17).TextColor(color)
				ui.Text(c, tab.label).FontSize(13.5).FontWeight(500).TextColor(p.text)
			})
		}
	})
	return active
}

// settingsPane draws the rows of the category shown.
func (a *app) settingsPane(c *ui.Context, tab settingsSection, p palette) {
	s := &a.settings
	ui.Scroll(c.Key(tab)).Fill().Padding(24, 28, 32, 28).Gap(8).Children(func() {
		heading := func(label string) {
			ui.Text(c, label).FontSize(20).FontWeight(700).LetterSpacing(-0.3).Margin(0, 2, 6, 2)
		}
		group := func(title string, rows func()) {
			if title != "" {
				ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(p.muted).Margin(14, 4, 4, 4)
			}
			ui.Column(c).Radius(12).Background(p.bg).Border(1, p.hairline).Dividers(1, p.hairline).Children(rows)
		}
		row := func(label, desc string, control func()) {
			ui.Row(c).Padding(12, 16).Gap(16).AlignItems(ui.Center).MinHeight(48).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, label).FontSize(13.5).FontWeight(500)
					if desc != "" {
						ui.Text(c, desc).FontSize(12).TextColor(p.muted).LineHeight(1.4)
					}
				})
				control()
			})
		}

		switch tab {
		case settingsGeneral:
			heading("通用")
			// Only on macOS: elsewhere the icon is always in the
			// notification area.
			if runtime.GOOS == "darwin" {
				group("", func() {
					row("应用图标", "显示在菜单栏时，关闭窗口后可从菜单栏图标打开", func() {
						labels := make([]string, len(appIcons))
						chosen := appIcons[0].label
						for i, ic := range appIcons {
							labels[i] = ic.label
							if ic.id == s.AppIcon {
								chosen = ic.label
							}
						}
						if ui.Select(c.Key("app-icon"), &chosen, labels).Label("应用图标").Width(150).Changed() {
							for _, ic := range appIcons {
								if ic.label == chosen {
									s.AppIcon = ic.id
								}
							}
							a.changed()
							a.changeAppIcon()
						}
					})
				})
			}

			// Only on macOS, the one with a notch player.
			if a.notch != nil {
				group("刘海播放器", func() {
					row("显示刘海播放器", "在屏幕顶部的刘海处显示迷你播放器，鼠标悬停即可展开", func() {
						if ui.Switch(c.Key("notch"), &s.Notch).Label("显示刘海播放器").Changed() {
							a.changed()
						}
					})
					row("播放时显示实时活动", "在刘海两侧显示封面与音频波形，可能遮挡刘海旁的菜单栏图标", func() {
						if ui.Switch(c.Key("activity"), &s.NotchActivity).Label("播放时显示实时活动").Disabled(!s.Notch).Changed() {
							a.changed()
						}
					})
					row("在无刘海的屏幕上常驻显示", "关闭时，将鼠标移至屏幕顶部中央即可唤出，不会遮挡菜单栏", func() {
						if ui.Switch(c.Key("virtual"), &s.NotchVirtual).Label("在无刘海的屏幕上常驻显示").Disabled(!s.Notch).Changed() {
							a.changed()
						}
					})
				})
			}

		case settingsPlayback:
			heading("播放")
			group("", func() {
				row("音质", "无损与 Hi-Res 需要黑胶 VIP，没有时播放所能播放的最高音质；下一首歌曲开始生效", func() {
					labels := make([]string, len(qualityLevels))
					chosen := qualityLevels[2].label
					for i, q := range qualityLevels {
						labels[i] = q.label
						if q.level == s.Quality {
							chosen = q.label
						}
					}
					if ui.Select(c.Key("quality"), &chosen, labels).Label("音质").Width(150).Changed() {
						for _, q := range qualityLevels {
							if q.label == chosen {
								s.Quality = q.level
							}
						}
						a.changed()
					}
				})
				row("间奏时显示热评", "前奏、间奏与尾奏时，歌词处换成这首歌的热门评论", func() {
					if ui.Switch(c.Key("hot-comments"), &s.HotComments).Label("间奏时显示热评").Changed() {
						a.changed()
					}
				})
				row("音量均衡", "按歌曲测得的响度调整音量，让每首歌听起来一样响", func() {
					if ui.Switch(c.Key("normalize"), &s.Normalize).Label("音量均衡").Changed() {
						a.pl.SetVolume(a.volume)
						a.changed()
					}
				})
				row("均衡器", "十段曲线，用来补足笔记本外放的低音或压低刺耳的高频；下一首开始生效", func() {
					if ui.Switch(c.Key("equalizer"), &s.Equalizer).Label("均衡器").Changed() {
						// The bands are made as the next song loads, so the
						// sound playing is left as it is, as the quality is.
						a.changed()
						a.toast = "下一首开始生效"
					}
				})
				row("均衡曲线", "选中后立即对正在播放的歌曲生效", func() {
					presets := player.EQPresets()
					labels := make([]string, len(presets))
					chosen := presets[0].Label
					for i, p := range presets {
						labels[i] = p.Label
						if p.ID == s.EQPreset {
							chosen = p.Label
						}
					}
					if ui.Select(c.Key("eq-preset"), &chosen, labels).Label("均衡曲线").Width(130).Disabled(!s.Equalizer).Changed() {
						for _, p := range presets {
							if p.Label == chosen {
								s.EQPreset = p.ID
							}
						}
						a.changed()
						a.toast = "下一首开始生效"
					}
				})
				row("睡眠定时", a.sleepDesc(), func() {
					labels := make([]string, 0, len(sleepChoices)+2)
					chosen := ""
					atEnd, left := a.pl.Sleeping()
					for _, m := range sleepChoices {
						label := fmt.Sprintf("%d 分钟", m)
						labels = append(labels, label)
						if !atEnd && left > 0 && left == m {
							chosen = label
						}
					}
					labels = append(labels, "播完当前歌曲后停止")
					if atEnd {
						chosen = "播完当前歌曲后停止"
					}
					labels = append(labels, "关闭")
					if chosen == "" {
						chosen = "关闭"
					}
					if ui.Select(c.Key("sleep"), &chosen, labels).Label("睡眠定时").Width(150).Changed() {
						switch chosen {
						case "关闭":
							a.pl.CancelSleep()
						case "播完当前歌曲后停止":
							a.pl.SetSleepAtEnd()
						default:
							for _, m := range sleepChoices {
								if chosen == fmt.Sprintf("%d 分钟", m) {
									a.pl.SetSleep(time.Duration(m) * time.Minute)
								}
							}
						}
						a.setMenu()
						a.redraw()
					}
				})
			})

		case settingsAppearance:
			heading("外观")
			group("", func() {
				row("外观", "", func() {
					modes := []string{"system", "light", "dark"}
					i := 0
					for k, m := range modes {
						if m == s.Appearance {
							i = k
						}
					}
					if segmented(c.Key("appearance"), &i, "跟随系统", "浅色", "深色") {
						s.Appearance = modes[i]
						a.changed()
					}
				})
			})
			group("实验性功能", func() {
				row("进度条烟雾特效", "播放时进度条化作拖着烟雾的光点，随音乐起伏；会多用一些电量", func() {
					if ui.Switch(c.Key("smoke"), &s.Smoke).Label("进度条烟雾特效").Changed() {
						a.changed()
					}
				})
				row("播放页背景", a.backdropDesc(), func() {
					labels := make([]string, len(backdropStyles))
					chosen := backdropStyles[0].label
					for i, b := range backdropStyles {
						labels[i] = b.label
						if b.id == s.Backdrop {
							chosen = b.label
						}
					}
					if ui.Select(c.Key("backdrop"), &chosen, labels).Label("播放页背景").Width(120).Changed() {
						for _, b := range backdropStyles {
							if b.label == chosen {
								s.Backdrop = b.id
							}
						}
						a.changed()
					}
				})
				row("背景斑驳", "隐现背景上随低音起伏的深色斑块与细密颗粒；关闭后是平缓流动的封面色，也更省电", func() {
					if ui.Switch(c.Key("backdrop-patches"), &s.BackdropPatches).Label("背景斑驳").Disabled(s.Backdrop != "latent").Changed() {
						a.changed()
					}
				})
			})

		case settingsPodcasts:
			heading("播客")
			if a.captionsShown() {
				group("", func() { a.captionSettings(row, c) })
			}

		case settingsStorage:
			heading("存储")
			group("", func() {
				row("歌曲缓存", "听过的歌曲保存在本机，再次播放无需等待，也不耗流量；"+a.cacheUsage(), func() {
					labels := make([]string, len(cacheLimits))
					chosen := cacheLimits[2].label
					for i, l := range cacheLimits {
						labels[i] = l.label
						if l.mb == s.CacheMB {
							chosen = l.label
						}
					}
					if ui.Select(c.Key("cache"), &chosen, labels).Label("歌曲缓存").Width(150).Changed() {
						for _, l := range cacheLimits {
							if l.label == chosen {
								s.CacheMB = l.mb
							}
						}
						a.changed()
						a.applyCacheLimit()
					}
				})
				row("清除缓存", "删除保存在本机的全部歌曲", func() {
					if ui.Button(c.Key("clear-cache"), "清除").Disabled(a.cacheUsed <= 0).Clicked() {
						a.clearCache()
					}
				})
			})

		case settingsAccount:
			heading("账号")
			group("", func() {
				if a.profile == nil {
					row("未登录", "登录后，每日推荐与歌单将根据你的口味生成", func() {
						if ui.PrimaryButton(c.Key("login"), "扫码登录").Clicked() {
							a.showMain()
							a.openLogin()
							a.win.Invalidate()
						}
					})
					return
				}
				prof := a.profile
				ui.Row(c).Padding(12, 16).Gap(12).AlignItems(ui.Center).Children(func() {
					a.avatar(c, prof.AvatarURL, 36, p.press)
					ui.Column(c).Grow(1).Gap(2).Children(func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Text(c, prof.Nickname).FontSize(13.5).FontWeight(600)
							accountBadge(c, p, prof)
						})
						ui.Text(c, "已登录").FontSize(12).TextColor(p.muted)
					})
					if ui.Button(c.Key("logout"), "退出登录").Clicked() {
						a.signOut()
					}
				})
			})

		case settingsAbout:
			heading("关于")
			if a.updates {
				group("", func() {
					desc := "每天检查一次是否有新版本"
					if t := lastUpdateCheck(); !t.IsZero() {
						desc += "；上次检查：" + commentTime(t, time.Now())
					}
					row("自动检查更新", desc, func() {
						on := updateChecks()
						if ui.Switch(c.Key("update-checks"), &on).Label("自动检查更新").Changed() {
							setUpdateChecks(on)
						}
					})
					row("自动下载并安装", "在后台下载新版本，下次打开时生效", func() {
						on := updateDownloads()
						if ui.Switch(c.Key("update-downloads"), &on).Label("自动下载并安装").Disabled(!updateChecks()).Changed() {
							setUpdateDownloads(on)
						}
					})
					row("当前版本 "+mygo.App.Version(), "", func() {
						if ui.Button(c.Key("check-updates"), "检查更新").Clicked() {
							checkForUpdates()
						}
					})
				})
			}
		}
	})
}
