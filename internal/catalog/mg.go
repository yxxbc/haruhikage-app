package catalog

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// These are the public client protocol constants in LX mg/musicSearch.js,
// not account credentials. No browser/login state is consulted.
const mgDevice = "963B7AA0D21511ED807EE5846EC87D20"

func mgSign(timestamp, query string) string {
	h := md5.Sum([]byte(query + "6cdc72a439cef99a3418d2a78aa28c73yyapp2d16148780a1dcc7408e06336b98cfd50" + mgDevice + timestamp))
	return hex.EncodeToString(h[:])
}
func (s *Service) searchMG(ctx context.Context, query string, page int) (SearchPage, error) {
	stamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	q := url.Values{"isCorrect": {"0"}, "isCopyright": {"1"}, "searchSwitch": {`{"song":1,"album":0,"singer":0,"tagSong":1,"mvSong":0,"bestShow":1,"songlist":0,"lyricSong":0}`}, "pageSize": {"20"}, "text": {query}, "pageNo": {strconv.Itoa(page)}, "sort": {"0"}, "sid": {"USS"}}
	headers := http.Header{"uiVersion": {"A_music_3.6.1"}, "deviceId": {mgDevice}, "timestamp": {stamp}, "sign": {mgSign(stamp, query)}, "channel": {"0146921"}}
	b, err := s.get(ctx, "mg", "search", "https://jadeite.migu.cn/music_search/v3/search/searchAll", q, headers)
	if err != nil {
		return SearchPage{}, err
	}
	var res struct {
		Code scalar `json:"code"`
		Data *struct {
			Total scalar `json:"totalCount"`
			List  [][]struct {
				SongID      scalar `json:"songId"`
				CopyrightID scalar `json:"copyrightId"`
				Name        string `json:"name"`
				Album       string `json:"album"`
				AlbumID     scalar `json:"albumId"`
				Duration    scalar `json:"duration"`
				Singers     []struct {
					ID   scalar `json:"id"`
					Name string `json:"name"`
				} `json:"singerList"`
				Image1 string `json:"img1"`
				Image2 string `json:"img2"`
				Image3 string `json:"img3"`
			} `json:"resultList"`
		} `json:"songResultData"`
	}
	if err := decode("mg", "search", b, &res); err != nil {
		return SearchPage{}, err
	}
	if res.Code == "" {
		return SearchPage{}, failure("mg", "search", Protocol, nil)
	}
	if res.Code != "000000" {
		return SearchPage{}, remoteError("mg", "search", string(res.Code))
	}
	if res.Data == nil {
		return SearchPage{}, failure("mg", "search", Protocol, nil)
	}
	total, err := count(res.Data.Total)
	if err != nil || (total > (page-1)*20 && res.Data.List == nil) {
		return SearchPage{}, failure("mg", "search", Protocol, nil)
	}
	out := SearchPage{Total: total}
	for _, group := range res.Data.List {
		for _, row := range group {
			// LX songmid and resourceinfo.resourceId both use songId. The
			// copyrightId is different and is only used in the public page URL.
			if row.CopyrightID == "" || row.SongID == "" {
				return SearchPage{}, failure("mg", "search", Protocol, nil)
			}
			image := row.Image3
			if image == "" {
				image = row.Image2
			}
			if image == "" {
				image = row.Image1
			}
			if image != "" && !strings.HasPrefix(image, "http://") && !strings.HasPrefix(image, "https://") {
				image = "http://d.musicapp.migu.cn" + image
			}
			t := domain.Track{Provider: "mg", ID: string(row.SongID), Name: row.Name, Duration: secondsMS(row.Duration), Album: domain.Album{ID: string(row.AlbumID), Name: row.Album, PicURL: image}, Playback: domain.PlaybackIdentity{Songmid: string(row.SongID)}, WebURL: "https://music.migu.cn/v3/music/song/" + url.PathEscape(string(row.CopyrightID))}
			for _, a := range row.Singers {
				t.Artists = append(t.Artists, domain.Artist{ID: string(a.ID), Name: a.Name})
			}
			out.Tracks = append(out.Tracks, t)
		}
	}
	return out, nil
}
func mgLyricHeaders() http.Header {
	return http.Header{"Referer": {"https://app.c.nf.migu.cn/"}, "channel": {"0146921"}}
}

// Use the resource lookup from LX mg/musicInfo.js so lyrics still work after
// persisting/restoring a Track (no volatile search-response URL cache required).
// When LRC is available it is intentionally preferred to encrypted MRC.
func (s *Service) lyricsMG(ctx context.Context, t domain.Track) (domain.Lyrics, error) {
	form := url.Values{"resourceId": {t.ID}}
	b, err := s.request(ctx, "mg", "lyrics", http.MethodPost, "https://c.musicapp.migu.cn/MIGUM2.0/v1.0/content/resourceinfo.do?resourceType=2", []byte(form.Encode()), http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
	if err != nil {
		return domain.Lyrics{}, err
	}
	type resource struct {
		SongID      scalar `json:"songId"`
		LRC         string `json:"lrcUrl"`
		MRC         string `json:"mrcUrl"`
		Translation string `json:"trcUrl"`
	}
	var res struct {
		Code       scalar     `json:"code"`
		ReturnCode scalar     `json:"returnCode"`
		Resource   []resource `json:"resource"`
		Data       *struct {
			Resource []resource `json:"resource"`
		} `json:"data"`
	}
	if err := decode("mg", "lyrics", b, &res); err != nil {
		return domain.Lyrics{}, err
	}
	code := res.Code
	if code == "" {
		code = res.ReturnCode
	}
	if code == "" {
		return domain.Lyrics{}, failure("mg", "lyrics", Protocol, nil)
	}
	if code != "000000" {
		return domain.Lyrics{}, remoteError("mg", "lyrics", string(code))
	}
	resources := res.Resource
	if res.Data != nil {
		resources = res.Data.Resource
	}
	if resources == nil {
		return domain.Lyrics{}, failure("mg", "lyrics", Protocol, nil)
	}
	if len(resources) == 0 {
		return domain.Lyrics{}, failure("mg", "lyrics", NotFound, nil)
	}
	info := resources[0]
	if info.SongID != "" && string(info.SongID) != t.ID {
		return domain.Lyrics{}, failure("mg", "lyrics", Protocol, nil)
	}
	endpoint := info.LRC
	if endpoint == "" {
		endpoint = info.MRC
	}
	if endpoint == "" {
		return domain.Lyrics{}, failure("mg", "lyrics", NotFound, nil)
	}
	text, err := s.get(ctx, "mg", "lyrics", endpoint, nil, mgLyricHeaders())
	if err != nil {
		return domain.Lyrics{}, err
	}
	var out domain.Lyrics
	if info.LRC != "" {
		out.LRC = strings.TrimPrefix(string(text), "\ufeff")
	} else {
		decrypted, err := decodeMRC(string(text))
		if err != nil {
			return domain.Lyrics{}, failure("mg", "lyrics", Protocol, err)
		}
		out, err = parseQRC(decrypted) // MRC uses the same text(start,duration) timing.
		if err != nil {
			return domain.Lyrics{}, failure("mg", "lyrics", Protocol, err)
		}
	}
	if info.Translation != "" {
		translation, err := s.get(ctx, "mg", "lyrics", info.Translation, nil, mgLyricHeaders())
		if err != nil {
			return domain.Lyrics{}, err
		}
		out.Translation = string(translation)
	}
	return out, nil
}
