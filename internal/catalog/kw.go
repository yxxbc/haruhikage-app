package catalog

import (
	"bytes"
	"context"
	"encoding/base64"
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// Protocol: LX musicSdk/kw/{musicSearch,lyric,util}.js. No account token
// scraping or CSRF cookie is needed by these client catalogue endpoints.
func (s *Service) searchKW(ctx context.Context, query string, page int) (SearchPage, error) {
	q := url.Values{"client": {"kt"}, "all": {query}, "pn": {strconv.Itoa(page - 1)}, "rn": {"30"}, "uid": {"794762570"}, "ver": {"kwplayer_ar_9.2.2.1"}, "vipver": {"1"}, "show_copyright_off": {"1"}, "newver": {"1"}, "ft": {"music"}, "cluster": {"0"}, "strategy": {"2012"}, "encoding": {"utf8"}, "rformat": {"json"}, "vermerge": {"1"}, "mobi": {"1"}, "issubtitle": {"1"}}
	b, err := s.get(ctx, "kw", "search", "http://search.kuwo.cn/r.s", q, nil)
	if err != nil {
		return SearchPage{}, err
	}
	var res struct {
		Total scalar `json:"TOTAL"`
		Show  scalar `json:"SHOW"`
		List  []struct {
			RID      string `json:"MUSICRID"`
			Name     string `json:"SONGNAME"`
			Artist   string `json:"ARTIST"`
			ArtistID scalar `json:"ARTISTID"`
			AlbumID  scalar `json:"ALBUMID"`
			Album    string `json:"ALBUM"`
			Duration scalar `json:"DURATION"`
			Pic      string `json:"pic"`
		} `json:"abslist"`
	}
	if err := decode("kw", "search", b, &res); err != nil {
		return SearchPage{}, err
	}
	total, err := count(res.Total)
	if err != nil || (total > (page-1)*30 && res.List == nil) {
		return SearchPage{}, failure("kw", "search", Protocol, nil)
	}
	if total != 0 && res.Show == "0" {
		return SearchPage{}, remoteError("kw", "search", "SHOW=0")
	}
	out := SearchPage{Total: total}
	for _, row := range res.List {
		id := strings.TrimPrefix(row.RID, "MUSIC_")
		if !decimalID(id) {
			return SearchPage{}, failure("kw", "search", Protocol, nil)
		}
		t := domain.Track{Provider: "kw", ID: id, Name: html.UnescapeString(row.Name), Duration: secondsMS(row.Duration), Playback: domain.PlaybackIdentity{ID: id}, Album: domain.Album{ID: string(row.AlbumID), Name: html.UnescapeString(row.Album), PicURL: row.Pic}, WebURL: "https://www.kuwo.cn/play_detail/" + id}
		for _, name := range strings.Split(html.UnescapeString(row.Artist), "&") {
			if name = strings.TrimSpace(name); name != "" {
				t.Artists = append(t.Artists, domain.Artist{Name: name})
			}
		}
		if len(t.Artists) == 1 {
			t.Artists[0].ID = string(row.ArtistID)
		}
		out.Tracks = append(out.Tracks, t)
	}
	return out, nil
}
func (s *Service) lyricsKW(ctx context.Context, t domain.Track) (domain.Lyrics, error) {
	if !decimalID(t.ID) {
		return domain.Lyrics{}, failure("kw", "lyrics", InvalidRequest, nil)
	}
	q := url.Values{"f": {"web"}, "type": {"lyric"}, "lrcx": {"1"}, "rid": {t.ID}, "encode": {"utf8"}}
	b, err := s.get(ctx, "kw", "lyrics", "http://mlyric.kuwo.cn/mobi.s", q, nil)
	if err != nil {
		return domain.Lyrics{}, err
	}
	text, err := decodeKW(b)
	if err != nil {
		return domain.Lyrics{}, failure("kw", "lyrics", Protocol, err)
	}
	return parseKW(text)
}
func decodeKW(raw []byte) (string, error) {
	if len(raw) < 10 || strings.ToLower(string(raw[:10])) != "tp=content" {
		return "", errLyricFormat
	}
	separator := bytes.Index(raw, []byte("\r\n\r\n"))
	if separator < 0 {
		return "", errLyricFormat
	}
	text, err := inflate(raw[separator+4:])
	if err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(text)))
	if err != nil {
		return "", err
	}
	key := []byte("yeelion")
	for i := range data {
		data[i] ^= key[i%len(key)]
	}
	return string(data), nil
}

// Kuwo's paired numbers are not plain offsets: the octal [kuwo:] tag
// determines their scale. Convert them to absolute YRC word times.
func parseKW(text string) (domain.Lyrics, error) {
	a, b := int64(1), int64(1)
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "[kuwo:") {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return domain.Lyrics{}, failure("kw", "lyrics", Protocol, nil)
			}
			n, err := strconv.ParseInt(strings.TrimSpace(line[6:end]), 8, 64)
			if err != nil || n/10 == 0 || n%10 == 0 {
				return domain.Lyrics{}, failure("kw", "lyrics", Protocol, nil)
			}
			a, b = n/10, n%10
		}
	}
	type timed struct {
		at    int64
		text  string
		words []lyricWord
	}
	var entries []timed
	for _, line := range lines {
		m := lrcStamp.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		at, err := lrcMillis(line[m[2]:m[3]])
		if err != nil {
			return domain.Lyrics{}, failure("kw", "lyrics", Protocol, err)
		}
		content := html.UnescapeString(line[m[1]:])
		words := angleWords.FindAllStringSubmatchIndex(content, -1)
		entry := timed{at: at, text: angleTag.ReplaceAllString(content, "")}
		for _, w := range words {
			x, _ := strconv.ParseInt(content[w[2]:w[3]], 10, 64)
			y, _ := strconv.ParseInt(content[w[4]:w[5]], 10, 64)
			start := abs64((x + y) / (a * 2))
			duration := abs64((x - y) / (b * 2))
			if len(entry.words) > 0 {
				prev := &entry.words[len(entry.words)-1]
				if prev.start+prev.length > at+start {
					prev.length = max(int64(0), at+start-prev.start)
				}
			}
			entry.words = append(entry.words, lyricWord{start: at + start, length: duration, text: content[w[6]:w[7]]})
		}
		entries = append(entries, entry)
	}
	var lrc, yrc, translation []string
	var primary []timed
	// LX translation streams place a translation just before a repeated-time
	// next line. Move that preceding entry to the previous primary line's time.
	seen := map[int64]bool{}
	for _, entry := range entries {
		if seen[entry.at] && len(primary) >= 2 {
			tr := primary[len(primary)-1]
			primary = primary[:len(primary)-1]
			translation = append(translation, lrcTime(primary[len(primary)-1].at)+tr.text)
		}
		primary = append(primary, entry)
		seen[entry.at] = true
	}
	for _, entry := range primary {
		lrc = append(lrc, lrcTime(entry.at)+entry.text)
		if len(entry.words) > 0 {
			end := entry.at
			for _, w := range entry.words {
				end = max(end, w.start+w.length)
			}
			yrc = append(yrc, yrcText(entry.at, end-entry.at, entry.words))
		}
	}
	return domain.Lyrics{LRC: strings.Join(lrc, "\n"), YRC: strings.Join(yrc, "\n"), Translation: strings.Join(translation, "\n")}, nil
}
func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
