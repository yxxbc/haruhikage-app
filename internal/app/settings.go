package app

import (
	"encoding/json"
	"github.com/egoist/mygo"
	"github.com/yxxbc/haruhikage-app/internal/domain"
	"os"
	"path/filepath"
)

// Settings are user choices; restoring defaults never deletes music or sources.
type settings struct {
	Notch, NotchActivity, NotchVirtual, NotchGreeted bool
	Quality                                          string            `json:"quality"`
	Provider                                         string            `json:"provider"`
	Qualities                                        map[string]string `json:"qualities,omitempty"`
	Appearance                                       string            `json:"appearance"`
	Smoke, LyricsStage, StageSparks                  bool
	StageStyle                                       string  `json:"stageStyle,omitempty"`
	Volume                                           float64 `json:"volume"`
	Normalize, Equalizer                             bool
	EQPreset                                         string `json:"eqPreset,omitempty"`
	Backdrop                                         string `json:"backdrop,omitempty"`
	BackdropPatches                                  bool
	ReduceMotion                                     bool           `json:"reduceMotion"`
	AppIcon                                          string         `json:"appIcon"`
	CacheMB                                          int            `json:"cacheMB"`
	SearchHistory                                    []string       `json:"searchHistory,omitempty"`
	LyricOffsets                                     map[string]int `json:"lyricOffsets,omitempty"`
	path                                             string
}

func defaultSettings() settings {
	return settings{Provider: "wy", Quality: "320k", Qualities: map[string]string{}, Appearance: "system", Volume: .8, AppIcon: "dock-and-menubar", CacheMB: 2048, Backdrop: "latent", BackdropPatches: true, NotchActivity: true}
}
func settingsPath() (string, error) {
	dir, e := mygo.App.Path(mygo.PathUserData)
	return filepath.Join(dir, "settings.json"), e
}
func loadSettings() settings {
	s := defaultSettings()
	if p, e := settingsPath(); e == nil {
		s.path = p
		if b, e := os.ReadFile(p); e == nil {
			_ = json.Unmarshal(b, &s)
		}
	}
	if s.Provider == "" {
		s.Provider = "wy"
	}
	if s.Quality == "" {
		s.Quality = "320k"
	}
	if s.Volume < 0 || s.Volume > 1 {
		s.Volume = .8
	}
	if s.CacheMB < 0 {
		s.CacheMB = 0
	}
	if s.Qualities == nil {
		s.Qualities = map[string]string{}
	}
	return s
}
func (s settings) save() error {
	if s.path == "" {
		return nil
	}
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(s.path), 0700); e != nil {
		return e
	}
	tmp := s.path + ".tmp"
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, s.path)
}
func (s settings) applyAppearance() {
	switch s.Appearance {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}
func (s settings) quality(t *domain.Track) string {
	if t != nil {
		if q := s.Qualities[t.Provider]; q != "" {
			return q
		}
	}
	return s.Quality
}

var cacheLimits = []struct {
	mb    int
	label string
}{{0, "关闭持久缓存"}, {1024, "1 GB"}, {2048, "2 GB"}, {5120, "5 GB"}, {10240, "10 GB"}}
