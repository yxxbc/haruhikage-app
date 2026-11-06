package catalog

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// Protocol: LX musicSdk/tx/musicSearch.js and tx/utils/{index,crypto}.js.
// Keep the exact bytes being signed as the POST body (JSON whitespace matters).
func txSign(body []byte) string {
	h := sha1.Sum(body)
	hash := hex.EncodeToString(h[:])
	pick := func(indexes []int) string {
		var out strings.Builder
		for _, i := range indexes {
			if i < len(hash) {
				out.WriteByte(hash[i])
			}
		}
		return out.String()
	}
	scramble := []byte{89, 39, 179, 150, 218, 82, 58, 252, 177, 52, 186, 123, 120, 64, 242, 133, 143, 161, 121, 179}
	for i := range scramble {
		scramble[i] ^= h[i]
	}
	b64 := strings.NewReplacer("/", "", "+", "", "=", "").Replace(base64.StdEncoding.EncodeToString(scramble))
	return strings.ToLower("zzc" + pick([]int{23, 14, 6, 36, 16, 40, 7, 19}) + b64 + pick([]int{16, 1, 32, 12, 19, 27, 8, 5}))
}
func (s *Service) searchTX(ctx context.Context, query string, page int) (SearchPage, error) {
	// QQ's anonymous gateway is sensitive to serialized parameter order:
	// a live request with query after page_num returned 2001/is_filter=-12;
	// the ordered desktop shape below returned real pages without cookies.
	// Do not replace these structs with maps or re-marshal after signing.
	var data struct {
		Comm struct {
			CT  string `json:"ct"`
			CV  string `json:"cv"`
			UIN string `json:"uin"`
		} `json:"comm"`
		Req struct {
			Module string `json:"module"`
			Method string `json:"method"`
			Param  struct {
				Query       string `json:"query"`
				Limit       int    `json:"num_per_page"`
				Page        int    `json:"page_num"`
				Type        int    `json:"search_type"`
				Remoteplace string `json:"remoteplace"`
			} `json:"param"`
		} `json:"req"`
	}
	data.Comm.CT, data.Comm.CV, data.Comm.UIN = "19", "2151", "0"
	data.Req.Module, data.Req.Method = "music.search.SearchCgiService", "DoSearchForQQMusicDesktop"
	data.Req.Param.Query, data.Req.Param.Limit, data.Req.Param.Page = query, 30, page
	data.Req.Param.Remoteplace = "txt.newclient.top"
	body, _ := json.Marshal(data)
	b, err := s.request(ctx, "tx", "search", http.MethodPost, "https://u.y.qq.com/cgi-bin/musics.fcg?sign="+txSign(body), body, http.Header{"Content-Type": {"application/json"}, "User-Agent": {"QQMusic 14090508(android 12)"}, "Referer": {"https://y.qq.com/"}})
	if err != nil {
		return SearchPage{}, err
	}
	type result struct {
		Code scalar `json:"code"`
		Data *struct {
			Meta struct {
				Sum    scalar `json:"sum"`
				Filter scalar `json:"is_filter"`
			} `json:"meta"`
			Body struct {
				Song struct {
					List []struct {
						ID       scalar `json:"id"`
						MID      string `json:"mid"`
						Title    string `json:"title"`
						Interval scalar `json:"interval"`
						Singer   []struct {
							MID  string `json:"mid"`
							Name string `json:"name"`
						} `json:"singer"`
						Album struct {
							MID  string `json:"mid"`
							Name string `json:"name"`
						} `json:"album"`
					} `json:"list"`
				} `json:"song"`
			} `json:"body"`
		} `json:"data"`
	}
	var res struct {
		Code   scalar  `json:"code"`
		Search *result `json:"music.search.SearchCgiService"`
		Req    *result `json:"req"`
	}
	if err := decode("tx", "search", b, &res); err != nil {
		return SearchPage{}, err
	}
	if res.Code == "" {
		return SearchPage{}, failure("tx", "search", Protocol, nil)
	}
	if res.Code != "0" {
		return SearchPage{}, remoteError("tx", "search", string(res.Code))
	}
	r := res.Search
	if r == nil {
		r = res.Req
	}
	if r == nil || r.Code == "" {
		return SearchPage{}, failure("tx", "search", Protocol, nil)
	}
	if r.Code != "0" {
		return SearchPage{}, remoteError("tx", "search", string(r.Code))
	}
	if r.Data == nil {
		return SearchPage{}, failure("tx", "search", Protocol, nil)
	}
	if r.Data.Meta.Filter.int64() < 0 {
		return SearchPage{}, remoteError("tx", "search", "filtered")
	}
	total, err := count(r.Data.Meta.Sum)
	if err != nil || (total > (page-1)*30 && r.Data.Body.Song.List == nil) {
		return SearchPage{}, failure("tx", "search", Protocol, nil)
	}
	out := SearchPage{Total: total}
	for _, row := range r.Data.Body.Song.List {
		if row.MID == "" || !decimalID(string(row.ID)) {
			return SearchPage{}, failure("tx", "search", Protocol, nil)
		}
		t := domain.Track{Provider: "tx", ID: string(row.ID), Name: row.Title, Duration: secondsMS(row.Interval), Playback: domain.PlaybackIdentity{Songmid: row.MID}, Album: domain.Album{ID: row.Album.MID, Name: row.Album.Name}, WebURL: "https://y.qq.com/n/ryqq/songDetail/" + url.PathEscape(row.MID)}
		if row.Album.MID != "" && row.Album.MID != "空" {
			t.Album.PicURL = "https://y.gtimg.cn/music/photo_new/T002R500x500M000" + url.PathEscape(row.Album.MID) + ".jpg"
		}
		for _, a := range row.Singer {
			t.Artists = append(t.Artists, domain.Artist{ID: a.MID, Name: a.Name})
		}
		if t.Album.PicURL == "" && len(row.Singer) > 0 {
			t.Album.PicURL = "https://y.gtimg.cn/music/photo_new/T001R500x500M000" + url.PathEscape(row.Singer[0].MID) + ".jpg"
		}
		out.Tracks = append(out.Tracks, t)
	}
	return out, nil
}

func (s *Service) lyricsTX(ctx context.Context, t domain.Track) (domain.Lyrics, error) {
	// The PlayLyricInfo API requires the numeric song ID, not songmid. Search
	// retains that original ID while Playback contains only songmid for LX.
	if !decimalID(t.ID) {
		return domain.Lyrics{}, failure("tx", "lyrics", InvalidRequest, nil)
	}
	param := map[string]any{"format": "json", "crypt": 1, "ct": 19, "cv": 1873, "interval": 0, "lrc_t": 0, "qrc": 1, "qrc_t": 0, "roma": 1, "roma_t": 0, "songID": json.Number(t.ID), "trans": 1, "trans_t": 0, "type": -1}
	data := map[string]any{"comm": map[string]any{"ct": "19", "cv": "1859", "uin": "0"}, "req": map[string]any{"module": "music.musichallSong.PlayLyricInfo", "method": "GetPlayLyricInfo", "param": param}}
	b, err := s.postJSON(ctx, "tx", "lyrics", "https://u.y.qq.com/cgi-bin/musicu.fcg", data, http.Header{"Referer": {"https://y.qq.com"}})
	if err != nil {
		return domain.Lyrics{}, err
	}
	var res struct {
		Code scalar `json:"code"`
		Req  *struct {
			Code scalar `json:"code"`
			Data *struct {
				Lyric string `json:"lyric"`
				Trans string `json:"trans"`
				Roma  string `json:"roma"`
			} `json:"data"`
		} `json:"req"`
	}
	if err := decode("tx", "lyrics", b, &res); err != nil {
		return domain.Lyrics{}, err
	}
	if res.Code == "" || res.Req == nil || res.Req.Code == "" {
		return domain.Lyrics{}, failure("tx", "lyrics", Protocol, nil)
	}
	if res.Code != "0" {
		return domain.Lyrics{}, remoteError("tx", "lyrics", string(res.Code))
	}
	if res.Req.Code != "0" {
		return domain.Lyrics{}, remoteError("tx", "lyrics", string(res.Req.Code))
	}
	if res.Req.Data == nil {
		return domain.Lyrics{}, failure("tx", "lyrics", Protocol, nil)
	}
	main, err := decodeQRCContext(ctx, res.Req.Data.Lyric)
	if err != nil {
		return domain.Lyrics{}, failure("tx", "lyrics", Protocol, err)
	}
	trans, err := decodeQRCContext(ctx, res.Req.Data.Trans)
	if err != nil {
		return domain.Lyrics{}, failure("tx", "lyrics", Protocol, err)
	}
	roma, err := decodeQRCContext(ctx, res.Req.Data.Roma)
	if err != nil {
		return domain.Lyrics{}, failure("tx", "lyrics", Protocol, err)
	}
	out, err := parseQRC(main)
	if err != nil {
		return domain.Lyrics{}, failure("tx", "lyrics", Protocol, err)
	}
	tx, err := parseQRC(trans)
	if err != nil {
		return domain.Lyrics{}, failure("tx", "lyrics", Protocol, err)
	}
	rx, err := parseQRC(roma)
	if err != nil {
		return domain.Lyrics{}, failure("tx", "lyrics", Protocol, err)
	}
	out.Translation, out.Romaji = tx.LRC, rx.LRC
	return out, nil
}
