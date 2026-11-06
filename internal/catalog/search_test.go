package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fixtureService keeps every request on httptest, including multi-step lyrics
// and any accidental request to a different platform host.
func fixtureService(t *testing.T, handler http.HandlerFunc) *Service {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := server.Client().Transport
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		u := *r.URL
		r.URL = &u
		r.Host = r.URL.Host
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return base.RoundTrip(r)
	})}
	return NewWithClient(client)
}
func jsonResponse(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func searchFixture(provider, id string, total int) string {
	switch provider {
	case "wy":
		return fmt.Sprintf(`{"code":200,"result":{"songCount":%d,"songs":[{"id":%s,"name":"Song & Title","dt":180000,"ar":[{"id":"9007199254740999","name":"Artist"}],"al":{"id":"album","name":"Album","picUrl":"https://img.example/a.jpg"}}]}}`, total, id)
	case "tx":
		return fmt.Sprintf(`{"code":0,"req":{"code":0,"data":{"meta":{"sum":%d,"is_filter":0},"body":{"song":{"list":[{"id":%s,"mid":"mid_%s","title":"Song & Title","interval":180,"singer":[{"mid":"singer","name":"Artist"}],"album":{"mid":"album","name":"Album"}}]}}}}}`, total, id, id)
	case "kg":
		return fmt.Sprintf(`{"error_code":0,"data":{"total":%d,"lists":[{"FileHash":"%s","Audioid":42,"OriSongName":"Song &amp; Title","Duration":180,"AlbumID":"album","AlbumName":"Album","Singers":[{"id":"9007199254740999","name":"Artist"}],"trans_param":{"union_cover":"https://img.example/{size}/a.jpg"},"Grp":[{"FileHash":"not-a-primary-edition"}]}]}}`, total, id)
	case "kw":
		return fmt.Sprintf(`{"TOTAL":"%d","SHOW":"1","abslist":[{"MUSICRID":"MUSIC_%s","SONGNAME":"Song &amp; Title","DURATION":"180","ARTIST":"Artist","ALBUM":"Album","ALBUMID":"album"}]}`, total, id)
	case "mg":
		return fmt.Sprintf(`{"code":"000000","songResultData":{"totalCount":"%d","resultList":[[{"songId":"%s","copyrightId":"copyright-%s","name":"Song & Title","duration":180,"singerList":[{"name":"Artist"}],"album":"Album","albumId":"album","img3":"/a.jpg"}]]}}`, total, id, id)
	}
	panic(provider)
}

// Search keeps provider identity, millisecond durations and server page offsets
// independent of visible/deduplicated track counts, without a privilege fetch.
func TestSearchPaginationAndIdentity(t *testing.T) {
	for _, provider := range []string{"wy", "tx", "kg", "kw", "mg"} {
		t.Run(provider, func(t *testing.T) {
			const longID = "900719925474099312345"
			calls := 0
			s := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Cookie") != "" {
					t.Error("anonymous catalogue sent cookies")
				}
				page := calls
				q := r.URL.Query()
				if provider == "tx" {
					body, _ := io.ReadAll(r.Body)
					// Regression: QQ returned 2001 for an otherwise identical
					// anonymous body with query serialized after page_num.
					want := fmt.Sprintf(`{"comm":{"ct":"19","cv":"2151","uin":"0"},"req":{"module":"music.search.SearchCgiService","method":"DoSearchForQQMusicDesktop","param":{"query":"hello & world","num_per_page":30,"page_num":%d,"search_type":0,"remoteplace":"txt.newclient.top"}}}`, page)
					// encoding/json HTML-escapes ampersands in both signed body
					// and wire bytes; compare decoded content as well as order.
					want = strings.ReplaceAll(want, "&", `\u0026`)
					if string(body) != want {
						t.Errorf("desktop body = %s", body)
					}
					if q.Get("sign") != txSign(body) {
						t.Error("signature did not cover exact request bytes")
					}
					if r.Header.Get("Referer") != "https://y.qq.com/" {
						t.Error("missing desktop referer")
					}
				} else {
					key, limitKey, queryKey := "page", "pagesize", "keyword"
					wantPage := page
					switch provider {
					case "wy":
						key, limitKey, queryKey = "offset", "limit", "s"
						wantPage = (page - 1) * 30
					case "kw":
						key, limitKey, queryKey = "pn", "rn", "all"
						wantPage = page - 1
					case "mg":
						key, limitKey, queryKey = "pageNo", "pageSize", "text"
					}
					if q.Get(key) != strconv.Itoa(wantPage) || q.Get(limitKey) != strconv.Itoa(pageSize(provider)) || q.Get(queryKey) != "hello & world" {
						t.Errorf("wrong pagination/query: %s", r.URL.RawQuery)
					}
					if provider == "mg" && r.Header.Get("Sign") != mgSign(r.Header.Get("Timestamp"), "hello & world") {
						t.Error("invalid Migu signature")
					}
				}
				id := longID
				if provider == "kg" {
					id = "0123456789ABCDEF0123456789ABCDEF"
				}
				_, _ = io.WriteString(w, searchFixture(provider, id, pageSize(provider)+1))
			})
			for _, page := range []int{0, 2} {
				result, err := s.Search(context.Background(), provider, " hello & world ", page)
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Tracks) != 1 || result.Total != pageSize(provider)+1 || result.More != (page == 0) {
					t.Fatalf("bad page: %+v", result)
				}
				track := result.Tracks[0]
				if track.Provider != provider || track.Name != "Song & Title" || track.Duration != 180000 || len(track.Artists) != 1 || track.Artists[0].Name != "Artist" || track.Album.Name != "Album" {
					t.Fatalf("lost metadata: %+v", track)
				}
				if provider != "kg" && track.ID != longID {
					t.Fatalf("rounded or substituted identity: %q", track.ID)
				}
				switch provider {
				case "tx":
					if track.Playback.Songmid != "mid_"+longID || track.Playback.ID != "" || track.Playback.Hash != "" {
						t.Fatalf("wrong QQ identity: %+v", track.Playback)
					}
				case "kg":
					if track.ID != "0123456789ABCDEF0123456789ABCDEF" || track.Playback.Hash != track.ID || track.Playback.ID != "" || track.Playback.Songmid != "" || !strings.Contains(track.Album.PicURL, "/500/") {
						t.Fatalf("wrong Kugou identity/cover: %+v", track)
					}
				case "mg":
					if track.Playback.Songmid != longID || track.Playback.ID != "" || !strings.Contains(track.WebURL, "copyright-") {
						t.Fatalf("confused songId and copyrightId: %+v", track)
					}
				default:
					if track.Playback.ID != longID || track.Playback.Songmid != "" || track.Playback.Hash != "" {
						t.Fatalf("wrong platform ID: %+v", track.Playback)
					}
				}
			}
			if calls != 2 {
				t.Fatalf("unexpected extra requests: %d", calls)
			}
		})
	}
}

// Only a valid, explicitly successful empty page is treated as no matches;
// platform rejection, missing result containers and malformed JSON are errors.
func TestSearchEmptyVersusFailure(t *testing.T) {
	cases := []struct{ provider, empty, rejected string }{
		{"wy", `{"code":200,"result":{"songCount":0,"songs":[]}}`, `{"code":403}`},
		{"tx", `{"code":0,"req":{"code":0,"data":{"meta":{"sum":0},"body":{"song":{"list":[]}}}}}`, `{"code":0,"req":{"code":2001,"data":{"meta":{"sum":0},"body":{"song":{"list":[]}}}}}`},
		{"kg", `{"error_code":0,"data":{"total":0,"lists":[]}}`, `{"error_code":30014}`},
		{"kw", `{"TOTAL":"0","SHOW":"0","abslist":[]}`, `{"TOTAL":"20","SHOW":"0","abslist":[]}`},
		{"mg", `{"code":"000000","songResultData":{"totalCount":"0","resultList":[]}}`, `{"code":"000001"}`},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			response := tc.empty
			s := fixtureService(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, response) })
			out, err := s.Search(context.Background(), tc.provider, "not found", 1)
			if err != nil || len(out.Tracks) != 0 || out.More || out.Total != 0 {
				t.Fatalf("empty page: %+v %v", out, err)
			}
			for _, body := range []string{tc.rejected, `{}`, `<html>blocked</html>`, `{"code":0} garbage`} {
				response = body
				if _, err := s.Search(context.Background(), tc.provider, "query", 1); err == nil {
					t.Fatalf("failure was presented as no matches: %s", body)
				}
			}
		})
	}
}

// A filtered QQ response sometimes has top-level and request code zero but no
// songs; it must not be displayed as a genuine zero-result keyword search.
func assertKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	var got *Error
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if got.Kind != want {
		t.Fatalf("error kind = %q, want %q", got.Kind, want)
	}
}

func TestQQFilteredSuccessIsRemoteFailure(t *testing.T) {
	s := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"req":{"code":0,"data":{"meta":{"sum":0,"is_filter":-12},"body":{"song":{"list":[]}}}}}`)
	})
	_, err := s.Search(context.Background(), "tx", "query", 1)
	assertKind(t, err, Remote)
}

// Duplicate platform editions collapse in server order without changing total
// or using the deduplicated count to compute the next page.
func TestSearchDeduplicatesByProviderIdentity(t *testing.T) {
	s := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":200,"result":{"songCount":100,"songs":[{"id":1,"name":"first"},{"id":"1","name":"duplicate"},{"id":2,"name":"second"}]}}`)
	})
	out, err := s.Search(context.Background(), "wy", "query", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tracks) != 2 || out.Tracks[0].Name != "first" || out.Tracks[1].ID != "2" || out.Total != 100 || !out.More {
		t.Fatalf("dedup changed page: %+v", out)
	}
}
