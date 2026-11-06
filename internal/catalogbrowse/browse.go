// Package catalogbrowse contains anonymous secondary catalog capabilities.
package catalogbrowse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yxxbc/haruhikage-app/internal/domain"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Chart struct{ ID, Name, Cover string }
type Detail struct {
	Title  string
	Tracks []domain.Track
}
type Service struct {
	Client  *http.Client
	BaseURL string
}

func New() *Service {
	return &Service{Client: &http.Client{Timeout: 12 * time.Second}, BaseURL: "https://music.163.com"}
}
func (s *Service) get(ctx context.Context, path string, q url.Values, out any) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+path+"?"+q.Encode(), nil)
	if e != nil {
		return e
	}
	req.Header.Set("Referer", "https://music.163.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	res, e := s.Client.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("catalog request failed")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if e != nil || len(b) > 16<<20 {
		return errors.New("catalog response exceeds limit")
	}
	return json.Unmarshal(b, out)
}

type rawTrack struct {
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
}

func tracks(raw []rawTrack) []domain.Track {
	out := make([]domain.Track, 0, len(raw))
	for _, v := range raw {
		if v.ID == "" {
			continue
		}
		t := domain.Track{Provider: "wy", ID: v.ID.String(), Name: v.Name, Duration: v.Duration, Playback: domain.PlaybackIdentity{ID: v.ID.String()}, WebURL: "https://music.163.com/#/song?id=" + v.ID.String(), Album: domain.Album{ID: v.Album.ID.String(), Name: v.Album.Name, PicURL: v.Album.PicURL}}
		for _, ar := range v.Artists {
			t.Artists = append(t.Artists, domain.Artist{ID: ar.ID.String(), Name: ar.Name})
		}
		out = append(out, t)
	}
	return out
}
func validID(id string) bool { n, e := strconv.ParseUint(id, 10, 64); return e == nil && n > 0 }
func (s *Service) Charts(ctx context.Context) ([]Chart, error) {
	var res struct {
		Code int `json:"code"`
		List []struct {
			ID    json.Number `json:"id"`
			Name  string      `json:"name"`
			Cover string      `json:"coverImgUrl"`
		} `json:"list"`
	}
	if e := s.get(ctx, "/api/toplist", nil, &res); e != nil {
		return nil, e
	}
	if res.Code != 200 {
		return nil, errors.New("charts unavailable")
	}
	out := []Chart{}
	for _, v := range res.List {
		out = append(out, Chart{ID: v.ID.String(), Name: v.Name, Cover: v.Cover})
	}
	return out, nil
}
func (s *Service) ChartTracks(ctx context.Context, id string) (Detail, error) {
	if !validID(id) {
		return Detail{}, errors.New("invalid chart ID")
	}
	var res struct {
		Code     int `json:"code"`
		Playlist struct {
			Name   string     `json:"name"`
			Tracks []rawTrack `json:"tracks"`
			IDs    []struct {
				ID json.Number `json:"id"`
			} `json:"trackIds"`
		} `json:"playlist"`
	}
	e := s.get(ctx, "/api/v6/playlist/detail", url.Values{"id": {id}, "n": {"1000"}}, &res)
	if e != nil {
		return Detail{}, e
	}
	if res.Code != 200 {
		return Detail{}, errors.New("chart unavailable")
	}
	p := res.Playlist
	for offset := len(p.Tracks); offset < len(p.IDs); {
		end := min(offset+400, len(p.IDs))
		ids := []map[string]json.Number{}
		for _, v := range p.IDs[offset:end] {
			ids = append(ids, map[string]json.Number{"id": v.ID})
		}
		b, _ := json.Marshal(ids)
		var more struct {
			Code  int        `json:"code"`
			Songs []rawTrack `json:"songs"`
		}
		if e = s.get(ctx, "/api/v3/song/detail", url.Values{"c": {string(b)}}, &more); e != nil {
			return Detail{}, e
		}
		if more.Code != 200 {
			return Detail{}, errors.New("chart details unavailable")
		}
		p.Tracks = append(p.Tracks, more.Songs...)
		offset = end
	}
	return Detail{Title: p.Name, Tracks: tracks(p.Tracks)}, nil
}
func (s *Service) Artist(ctx context.Context, id string) (Detail, error) {
	if !validID(id) {
		return Detail{}, errors.New("invalid artist ID")
	}
	var res struct {
		Code   int `json:"code"`
		Artist struct {
			Name string `json:"name"`
		} `json:"artist"`
		Songs []rawTrack `json:"hotSongs"`
	}
	if e := s.get(ctx, "/api/v1/artist/"+id, nil, &res); e != nil {
		return Detail{}, e
	}
	if res.Code != 200 {
		return Detail{}, fmt.Errorf("artist unavailable: %d", res.Code)
	}
	return Detail{Title: res.Artist.Name, Tracks: tracks(res.Songs)}, nil
}
func (s *Service) Album(ctx context.Context, id string) (Detail, error) {
	if !validID(id) {
		return Detail{}, errors.New("invalid album ID")
	}
	var res struct {
		Code  int `json:"code"`
		Album struct {
			Name string `json:"name"`
		} `json:"album"`
		Songs []rawTrack `json:"songs"`
	}
	if e := s.get(ctx, "/api/album/"+id, nil, &res); e != nil {
		return Detail{}, e
	}
	if res.Code != 200 {
		return Detail{}, errors.New("album unavailable")
	}
	return Detail{Title: res.Album.Name, Tracks: tracks(res.Songs)}, nil
}
