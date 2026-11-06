package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/egoist/mygo"
	"github.com/yxxbc/haruhikage-app/internal/domain"
	"github.com/yxxbc/haruhikage-app/internal/library"
	"github.com/yxxbc/haruhikage-app/internal/player"
	"os"
	"sync"
	"time"
)

const sessionEvery = 30 * time.Second
const playbackStateKey = "playback-session-v2"

// The UI copies the queue once; encoding and SQLite writes happen here.
type stateWriter struct {
	jobs  chan player.Session
	done  chan struct{}
	once  sync.Once
	store *library.Store
}

func newStateWriter(store *library.Store) *stateWriter {
	w := &stateWriter{jobs: make(chan player.Session, 1), done: make(chan struct{}), store: store}
	go func() {
		defer close(w.done)
		var last []byte
		for job := range w.jobs {
			b, e := json.Marshal(job)
			if e != nil || bytes.Equal(last, b) {
				continue
			}
			if w.store.SaveState(context.Background(), playbackStateKey, b) == nil {
				last = b
			}
		}
	}()
	return w
}
func (w *stateWriter) Submit(s player.Session) {
	select {
	case w.jobs <- s:
	default:
		select {
		case <-w.jobs:
		default:
		}
		w.jobs <- s
	}
}
func (w *stateWriter) Close() { w.once.Do(func() { close(w.jobs); <-w.done }) }
func (a *app) saveSession() {
	if a.pl == nil || a.lib == nil {
		return
	}
	s, ok := a.pl.Session()
	if !ok {
		return
	}
	if a.sessionWriter == nil {
		a.sessionWriter = newStateWriter(a.lib)
	}
	a.sessionWriter.Submit(s)
}
func (a *app) keepSession() {
	if a.sessionDue || a.quitting {
		return
	}
	a.sessionDue = true
	sessionAfter(sessionEvery, func() {
		a.sessionDue = false
		if a.quitting {
			return
		}
		a.saveSession()
		if a.pl.Playing() {
			a.keepSession()
		}
	})
}

var sessionAfter = func(d time.Duration, fn func()) { time.AfterFunc(d, func() { mygo.RunOnMain(fn) }) }

func (a *app) restoreSession() {
	if a.restored || a.lib == nil {
		return
	}
	a.restored = true
	a.run(func() {
		b, e := a.lib.LoadState(a.ctx, playbackStateKey)
		if e != nil || len(b) == 0 {
			return
		}
		var s player.Session
		if json.Unmarshal(b, &s) != nil {
			return
		}
		a.post(func() {
			if a.pl.Current() == nil {
				a.pl.Restore(s)
			}
		})
	})
}

// Legacy import reads, never moves or writes, the original session file.
func migrateLegacySession(b []byte) (player.Session, error) {
	var current player.Session
	if json.Unmarshal(b, &current) == nil && current.Version == 2 {
		return current, nil
	}
	var old struct {
		Queue []struct {
			ID       json.Number `json:"id"`
			Name     string      `json:"name"`
			Duration int64       `json:"dt"`
			Artists  []struct {
				ID   json.Number `json:"id"`
				Name string      `json:"name"`
			} `json:"ar"`
			Album struct {
				ID     json.Number `json:"id"`
				Name   string      `json:"name"`
				PicURL string      `json:"picUrl"`
			} `json:"al"`
		} `json:"queue"`
		Order    []int             `json:"order"`
		At       int               `json:"at"`
		Shuffle  bool              `json:"shuffle"`
		Repeat   player.RepeatMode `json:"repeat"`
		Position int64             `json:"position"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if e := dec.Decode(&old); e != nil {
		return player.Session{}, e
	}
	if len(old.Queue) == 0 {
		return player.Session{}, errors.New("旧会话没有可恢复的歌曲")
	}
	s := player.Session{Version: 2, Order: old.Order, At: old.At, Shuffle: old.Shuffle, Repeat: old.Repeat, Position: old.Position}
	for _, v := range old.Queue {
		if v.ID.String() == "" {
			return player.Session{}, errors.New("歌曲 ID 缺失")
		}
		t := domain.Track{Provider: "wy", ID: v.ID.String(), Name: v.Name, Duration: v.Duration, Playback: domain.PlaybackIdentity{ID: v.ID.String()}, Album: domain.Album{ID: v.Album.ID.String(), Name: v.Album.Name, PicURL: v.Album.PicURL}}
		for _, artist := range v.Artists {
			t.Artists = append(t.Artists, domain.Artist{ID: artist.ID.String(), Name: artist.Name})
		}
		s.Queue = append(s.Queue, t)
	}
	if len(s.Order) != len(s.Queue) || s.At < 0 || s.At >= len(s.Order) {
		return player.Session{}, errors.New("旧会话队列无效")
	}
	seen := map[int]bool{}
	for _, i := range s.Order {
		if i < 0 || i >= len(s.Queue) || seen[i] {
			return player.Session{}, errors.New("旧会话顺序无效")
		}
		seen[i] = true
	}
	return s, nil
}
func (a *app) importLegacySession() {
	parent := a.win
	a.run(func() {
		paths, e := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: "导入旧版 session.json", Filters: []mygo.FileFilter{{Name: "JSON 会话", Extensions: []string{"json"}}}})
		if e != nil || len(paths) == 0 {
			return
		}
		b, e := os.ReadFile(paths[0])
		if e != nil {
			return
		}
		s, e := migrateLegacySession(b)
		a.post(func() {
			if e != nil {
				a.toast = "旧会话无效，当前资料库未修改"
				return
			}
			a.pl.Pause()
			a.pl.Restore(s)
			a.saveSession()
			a.toast = "旧队列已恢复，点击播放后才会联网"
		})
	})
}
