package catalog

import (
	"context"
	"encoding/base64"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// Protocol: LX musicSdk/kg/musicSearch.js and kg/lyric.js.
func (s *Service) searchKG(ctx context.Context, query string, page int) (SearchPage, error) {
	q := url.Values{"platform": {"AndroidFilter"}, "iscorrection": {"1"}, "keyword": {query}, "hifiquality": {"0"}, "pagesize": {"30"}, "PrivilegeFilter": {"0"}, "page": {strconv.Itoa(page)}}
	b, err := s.get(ctx, "kg", "search", "http://songsearch.kugou.com/song_search_v2", q, nil)
	if err != nil {
		return SearchPage{}, err
	}
	var res struct {
		Code scalar `json:"error_code"`
		Data *struct {
			Total scalar `json:"total"`
			Lists []struct {
				Hash    string `json:"FileHash"`
				Name    string `json:"OriSongName"`
				Suffix  string `json:"Suffix"`
				Singer  string `json:"SingerName"`
				Singers []struct {
					ID   scalar `json:"id"`
					Name string `json:"name"`
				} `json:"Singers"`
				AlbumID  scalar `json:"AlbumID"`
				Album    string `json:"AlbumName"`
				Duration scalar `json:"Duration"`
				Image    string `json:"Image"`
				Trans    struct {
					Cover string `json:"union_cover"`
				} `json:"trans_param"`
			} `json:"lists"`
		} `json:"data"`
	}
	if err := decode("kg", "search", b, &res); err != nil {
		return SearchPage{}, err
	}
	if res.Code == "" {
		return SearchPage{}, failure("kg", "search", Protocol, nil)
	}
	if res.Code != "0" {
		return SearchPage{}, remoteError("kg", "search", string(res.Code))
	}
	if res.Data == nil {
		return SearchPage{}, failure("kg", "search", Protocol, nil)
	}
	total, err := count(res.Data.Total)
	if err != nil || (total > (page-1)*30 && res.Data.Lists == nil) {
		return SearchPage{}, failure("kg", "search", Protocol, nil)
	}
	out := SearchPage{Total: total}
	for _, row := range res.Data.Lists {
		if !validHash(row.Hash) {
			return SearchPage{}, failure("kg", "search", Protocol, nil)
		}
		if row.Image == "" {
			row.Image = row.Trans.Cover
		}
		name := row.Name
		if row.Suffix != "" {
			name += " " + row.Suffix
		}
		t := domain.Track{Provider: "kg", ID: row.Hash, Name: html.UnescapeString(name), Duration: secondsMS(row.Duration), Playback: domain.PlaybackIdentity{Hash: row.Hash}, Album: domain.Album{ID: string(row.AlbumID), Name: html.UnescapeString(row.Album), PicURL: strings.ReplaceAll(row.Image, "{size}", "500")}, WebURL: "https://www.kugou.com/song/#hash=" + url.QueryEscape(row.Hash)}
		for _, a := range row.Singers {
			t.Artists = append(t.Artists, domain.Artist{ID: string(a.ID), Name: html.UnescapeString(a.Name)})
		}
		if len(t.Artists) == 0 && row.Singer != "" {
			t.Artists = []domain.Artist{{Name: html.UnescapeString(row.Singer)}}
		}
		// Keep the server's primary group order. Expanding Grp would make
		// page boundaries and totals depend on variable nested editions.
		out.Tracks = append(out.Tracks, t)
	}
	return out, nil
}
func validHash(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
func kgLyricHeaders() http.Header {
	return http.Header{"KG-RC": {"1"}, "KG-THash": {"expand_search_manager.cpp:852736169:451"}, "User-Agent": {"KuGou2012-9020-ExpandSearchManager"}}
}
func (s *Service) lyricsKG(ctx context.Context, t domain.Track) (domain.Lyrics, error) {
	hash := t.Playback.Hash
	if hash == "" {
		hash = t.ID
	}
	if !validHash(hash) {
		return domain.Lyrics{}, failure("kg", "lyrics", InvalidRequest, nil)
	}
	q := url.Values{"ver": {"1"}, "man": {"yes"}, "client": {"pc"}, "keyword": {t.Name}, "hash": {hash}, "timelength": {strconv.FormatInt(t.Duration/1000, 10)}, "lrctxt": {"1"}}
	b, err := s.get(ctx, "kg", "lyrics", "http://lyrics.kugou.com/search", q, kgLyricHeaders())
	if err != nil {
		return domain.Lyrics{}, err
	}
	var res struct {
		Status     scalar `json:"status"`
		Candidates *[]struct {
			ID          scalar `json:"id"`
			AccessKey   string `json:"accesskey"`
			KRCType     scalar `json:"krctype"`
			ContentType scalar `json:"contenttype"`
		} `json:"candidates"`
	}
	if err := decode("kg", "lyrics", b, &res); err != nil {
		return domain.Lyrics{}, err
	}
	// The lyrics service uses status=200, not the song search error_code.
	if res.Status != "" && res.Status != "200" {
		return domain.Lyrics{}, remoteError("kg", "lyrics", string(res.Status))
	}
	if res.Candidates == nil {
		return domain.Lyrics{}, failure("kg", "lyrics", Protocol, nil)
	}
	if len(*res.Candidates) == 0 {
		return domain.Lyrics{}, failure("kg", "lyrics", NotFound, nil)
	}
	candidate := (*res.Candidates)[0]
	if candidate.ID == "" || candidate.AccessKey == "" {
		return domain.Lyrics{}, failure("kg", "lyrics", Protocol, nil)
	}
	format := "lrc"
	if candidate.KRCType == "1" && candidate.ContentType != "1" {
		format = "krc"
	}
	q = url.Values{"ver": {"1"}, "client": {"pc"}, "id": {string(candidate.ID)}, "accesskey": {candidate.AccessKey}, "fmt": {format}, "charset": {"utf8"}}
	b, err = s.get(ctx, "kg", "lyrics", "http://lyrics.kugou.com/download", q, kgLyricHeaders())
	if err != nil {
		return domain.Lyrics{}, err
	}
	var download struct {
		Status  scalar `json:"status"`
		Format  string `json:"fmt"`
		Content string `json:"content"`
	}
	if err := decode("kg", "lyrics", b, &download); err != nil {
		return domain.Lyrics{}, err
	}
	if download.Status != "" && download.Status != "200" && download.Status != "0" {
		return domain.Lyrics{}, remoteError("kg", "lyrics", string(download.Status))
	}
	switch download.Format {
	case "krc":
		out, err := decodeKRC(download.Content)
		if err != nil {
			return domain.Lyrics{}, failure("kg", "lyrics", Protocol, err)
		}
		return out, nil
	case "lrc":
		text, err := base64.StdEncoding.DecodeString(download.Content)
		if err != nil {
			return domain.Lyrics{}, failure("kg", "lyrics", Protocol, nil)
		}
		return domain.Lyrics{LRC: string(text)}, nil
	default:
		return domain.Lyrics{}, failure("kg", "lyrics", Protocol, nil)
	}
}
