package app

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/yxxbc/haruhikage-app/internal/catalog"
	"github.com/yxxbc/haruhikage-app/internal/catalogbrowse"
	"github.com/yxxbc/haruhikage-app/internal/domain"
	"github.com/yxxbc/haruhikage-app/internal/library"
	"github.com/yxxbc/haruhikage-app/internal/player"
	"github.com/yxxbc/haruhikage-app/internal/source"
)

type pageKind int

const (
	pageHome pageKind = iota
	pageFavorites
	pageRecent
	pagePlaylist
	pageSearch
	pageSources
	pageSettings
	pageCharts
	pageArtist
	pageAlbum
)

type route struct {
	page                pageKind
	id, provider, query string
}

// app owns UI state on the main thread. Services only return immutable snapshots.
type app struct {
	win, settingsWin       *mygo.Window
	notch                  *notch
	tray                   *mygo.Tray
	art                    *artCache
	pl                     *player.Player
	pal                    palette
	ctx                    context.Context
	cancel                 context.CancelFunc
	lib                    *library.Store
	catalog                *catalog.Service
	browse                 *catalogbrowse.Service
	charts                 []catalogbrowse.Chart
	browseTitle            string
	cacheLimitText         string
	sources                *source.Manager
	sourceList             []source.Info
	sourceBusy             bool
	sourcePath             string
	dataDir                string
	route                  route
	history, forward       []route
	mine                   []domain.Playlist
	liked                  map[string]bool
	favorites              []domain.Track
	recent                 []domain.Track
	tracks                 []domain.Track
	dataLoading            bool
	dataErr                string
	dataCancel             context.CancelFunc
	dataGen                uint64
	trackList              ui.ListState
	pageScroll, navScroll  ui.ScrollState
	scrolled               route
	query                  string
	searchBox              ui.Handle
	searched               route
	searchData             *searchData
	nowPlaying, npOpen     bool
	npChangedAt            time.Time
	npPane                 int
	npPaneTop, npTabsH     float32
	lyricScroll            ui.ScrollState
	lyricGlide             lyricGlide
	lyricAt                int
	lyricTrack             string
	queueList              ui.ListState
	queueAt                int
	scrub                  float64
	scrubbing              bool
	volume                 float64
	volumeOpen             bool
	mutedFrom              float64
	rowBars                barState
	underTop, underBottom  bool
	barScrub, npScrub      scrubState
	backdropCache          backdropCache
	npArt                  *artwork
	npCoverState           coverState
	wallState              wallState
	sig                    playerSig
	quitting, visualActive bool
	settings               settings
	cacheUsed              int64
	restored               bool
	savedSession           string
	sessionDue             bool
	sessionWriter          *stateWriter
	toast                  string
	naming                 nameDialog
	deleting               deleteDialog
	// Tests replace scheduling, never the live UI-thread entry point.
	runTask  func(func())
	testPost func(func())
}

func (a *app) run(fn func()) {
	if a.runTask != nil {
		a.runTask(fn)
	} else {
		go fn()
	}
}
func (a *app) post(fn func()) {
	if a.testPost != nil {
		a.testPost(fn)
		return
	}
	if a.win != nil {
		a.win.Update(func() {
			if !a.quitting {
				fn()
				a.redraw()
			}
		})
	}
}
func (a *app) redraw() {
	if a.win != nil {
		a.win.Invalidate()
	}
	if a.notch != nil && a.notch.win != nil {
		a.notch.win.Invalidate()
	}
}

var shotHook func(*app)

func (a *app) showMain() {
	if a.win != nil {
		if a.win.IsMinimized() {
			a.win.Restore()
		}
		a.win.Show()
		a.win.Focus()
		mygo.App.Focus()
	}
}
func (a *app) visualState() {
	a.visualActive = a.win != nil && a.win.IsVisible() && !a.win.IsMinimized()
	a.syncDock()
	a.redraw()
}

func newApp() *app {
	ctx, cancel := context.WithCancel(context.Background())
	return &app{ctx: ctx, cancel: cancel, liked: map[string]bool{}, catalog: catalog.New(), browse: catalogbrowse.New(), lyricAt: -1, queueAt: -1, cacheUsed: -1, visualActive: true}
}
func Run() error {
	a := newApp()
	mygo.App.WhenReady(func() {
		a.settings = loadSettings()
		a.settings.applyAppearance()
		a.npPane = a.lyricsPaneChosen()
		dir, err := mygo.App.Path(mygo.PathUserData)
		if err == nil {
			a.dataDir = dir
			a.lib, err = library.Open(filepath.Join(dir, "library.sqlite"))
		}
		if err != nil {
			a.toast = "本地资料库打开失败，请检查数据目录"
		}
		exe, _ := os.Executable()
		host := "source-host"
		if runtime.GOOS == "windows" {
			host += ".exe"
		}
		a.sources, err = source.NewManager(filepath.Join(dir, "sources"), filepath.Join(filepath.Dir(exe), host))
		if err != nil {
			a.toast = "音源目录打开失败"
		} else {
			a.sourceList = a.sources.List()
		}
		a.art = newArtCache(a.post)
		a.pl = player.New(a.post)
		a.pl.Quality = func() string { return a.settings.quality(a.pl.Current()) }
		a.pl.Normalize = func() bool { return a.settings.Normalize }
		a.pl.Equalizer = func() bool { return a.settings.Equalizer }
		a.pl.Preset = func() string { return a.settings.EQPreset }
		a.pl.Cache = newSongCache(a.settings.CacheMB)
		if a.sources != nil {
			a.pl.Resolve = a.sources.Resolve
		}
		a.pl.LoadLyrics = a.catalog.Lyrics
		a.pl.LoadArtwork = fetchArtwork
		a.pl.Started = func(t domain.Track) {
			if a.lib != nil {
				a.run(func() {
					err := a.lib.RecordPlay(a.ctx, t, 0)
					a.post(func() {
						if err != nil {
							a.toast = "播放历史保存失败"
						}
						a.refreshLibrary()
					})
				})
			}
		}
		a.pl.Changed = a.keepSession
		a.volume = a.settings.Volume
		a.pl.SetVolume(a.volume)
		a.win = mygo.NewWindow(mygo.WindowOptions{Title: mygo.App.Name(), Width: 1180, Height: 780, MinWidth: 880, MinHeight: 580, StateKey: "main", TitleBarStyle: mygo.TitleBarHidden, TitleBarHeight: buttonsHeight, TrafficLightPosition: &mygo.Point{X: 20, Y: 20}, Vibrancy: mygo.VibrancySidebar, BackgroundColor: "light-dark(#ffffff, #141416)", Content: ui.View(a.view)})
		a.setMenu()
		a.restoreSession()
		a.refreshLibrary()
		if runtime.GOOS == "darwin" {
			a.notch = newNotch(a)
		}
		a.win.OnShow(a.visualState)
		a.win.OnHide(a.visualState)
		a.win.OnMinimize(a.visualState)
		a.win.OnRestore(a.visualState)
		a.applyTray()
		a.visualState()
		if shotHook != nil {
			shotHook(a)
		}
		a.win.OnClose(func(e *mygo.CloseEvent) {
			if a.quitting {
				return
			}
			e.PreventDefault()
			if runtime.GOOS == "darwin" || a.tray != nil {
				a.win.Hide()
			} else {
				mygo.App.Quit()
			}
		})
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		a.quitting = true
		a.settings.Volume = a.volume
		_ = a.settings.save()
		a.saveSession()
		a.cancel()
		if a.dataCancel != nil {
			a.dataCancel()
		}
		if a.pl != nil {
			_ = a.pl.Close()
		}
		if a.sources != nil {
			_ = a.sources.Close()
		}
		if a.art != nil {
			a.art.Close()
		}
		if a.sessionWriter != nil {
			a.sessionWriter.Close()
		}
		if a.lib != nil {
			_ = a.lib.Close()
		}
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if a.tray != nil {
			a.tray.Destroy()
		}
	})
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnActivate(func(bool) { a.showMain() })
	return mygo.App.Run()
}

var artworkClient = &http.Client{Timeout: 12 * time.Second}

func fetchArtwork(ctx context.Context, t domain.Track) ([]byte, error) {
	if t.Album.PicURL == "" {
		return nil, nil
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, t.Album.PicURL, nil)
	if e != nil {
		return nil, e
	}
	res, e := artworkClient.Do(req)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(res.Body, 8<<20))
}
