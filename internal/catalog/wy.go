package catalog

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// Reuses the anonymous cloudsearch route and ar/al/dt schema from
// the legacy NetEase search model. That function cannot itself be called here:
// it has no context/client injection and makes an extra privilege request.
// Catalogue reads do not need account privileges or playback authorization.
func (s *Service) searchWY(ctx context.Context, query string, page int) (SearchPage, error) {
	q := url.Values{"s": {query}, "type": {"1"}, "limit": {"30"}, "offset": {strconv.Itoa((page - 1) * 30)}, "total": {"true"}}
	b, err := s.get(ctx, "wy", "search", "https://music.163.com/api/cloudsearch/pc", q, wyHeaders())
	if err != nil {
		return SearchPage{}, err
	}
	var res struct {
		Code   scalar `json:"code"`
		Result *struct {
			Count scalar `json:"songCount"`
			Songs []struct {
				ID      scalar `json:"id"`
				Name    string `json:"name"`
				Artists []struct {
					ID   scalar `json:"id"`
					Name string `json:"name"`
				} `json:"ar"`
				Album struct {
					ID   scalar `json:"id"`
					Name string `json:"name"`
					Pic  string `json:"picUrl"`
				} `json:"al"`
				Duration scalar   `json:"dt"`
				Alias    []string `json:"alia"`
			} `json:"songs"`
		} `json:"result"`
	}
	if err := decode("wy", "search", b, &res); err != nil {
		return SearchPage{}, err
	}
	if res.Code == "" {
		return SearchPage{}, failure("wy", "search", Protocol, nil)
	}
	if res.Code != "200" {
		return SearchPage{}, remoteError("wy", "search", string(res.Code))
	}
	if res.Result == nil {
		return SearchPage{}, failure("wy", "search", Protocol, nil)
	}
	total, err := count(res.Result.Count)
	if err != nil || (total > (page-1)*30 && res.Result.Songs == nil) {
		return SearchPage{}, failure("wy", "search", Protocol, nil)
	}
	out := SearchPage{Total: total}
	for _, row := range res.Result.Songs {
		id := string(row.ID)
		if !decimalID(id) {
			return SearchPage{}, failure("wy", "search", Protocol, nil)
		}
		t := domain.Track{Provider: "wy", ID: id, Name: row.Name, Duration: row.Duration.int64(), Alias: row.Alias,
			Album:    domain.Album{ID: string(row.Album.ID), Name: row.Album.Name, PicURL: row.Album.Pic},
			Playback: domain.PlaybackIdentity{ID: id}, WebURL: "https://music.163.com/song?id=" + id}
		for _, a := range row.Artists {
			t.Artists = append(t.Artists, domain.Artist{ID: string(a.ID), Name: a.Name})
		}
		out.Tracks = append(out.Tracks, t)
	}
	return out, nil
}
func wyHeaders() http.Header { return http.Header{"Referer": {"https://music.163.com/"}} }

// The public LRC route is sufficient without the optional account-carrying
// desktop EAPI call. If YRC is returned it is already in the shared format.
func (s *Service) lyricsWY(ctx context.Context, t domain.Track) (domain.Lyrics, error) {
	if !decimalID(t.ID) {
		return domain.Lyrics{}, failure("wy", "lyrics", InvalidRequest, nil)
	}
	q := url.Values{"id": {t.ID}, "lv": {"-1"}, "tv": {"-1"}, "rv": {"-1"}, "yv": {"-1"}}
	b, err := s.get(ctx, "wy", "lyrics", "https://music.163.com/api/song/lyric", q, wyHeaders())
	if err != nil {
		return domain.Lyrics{}, err
	}
	var res struct {
		Code scalar `json:"code"`
		LRC  struct {
			Lyric string `json:"lyric"`
		} `json:"lrc"`
		Translation struct {
			Lyric string `json:"lyric"`
		} `json:"tlyric"`
		YRC struct {
			Lyric string `json:"lyric"`
		} `json:"yrc"`
		Romaji struct {
			Lyric string `json:"lyric"`
		} `json:"romalrc"`
	}
	if err := decode("wy", "lyrics", b, &res); err != nil {
		return domain.Lyrics{}, err
	}
	if res.Code == "" {
		return domain.Lyrics{}, failure("wy", "lyrics", Protocol, nil)
	}
	if res.Code != "200" {
		return domain.Lyrics{}, remoteError("wy", "lyrics", string(res.Code))
	}
	return domain.Lyrics{LRC: res.LRC.Lyric, Translation: res.Translation.Lyric, YRC: res.YRC.Lyric, Romaji: res.Romaji.Lyric}, nil
}
