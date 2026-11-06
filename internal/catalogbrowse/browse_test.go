package catalogbrowse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Chart detail paging preserves long string IDs and requests omitted tracks.
func TestChartTracksCompletesDetails(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/api/v6/playlist/detail":
			if r.URL.Query().Get("id") != "42" {
				t.Errorf("chart id=%q", r.URL.Query().Get("id"))
			}
			_, _ = w.Write([]byte(`{"code":200,"playlist":{"name":"榜单","tracks":[{"id":9007199254740993123,"name":"一","dt":1000,"ar":[],"al":{}}],"trackIds":[{"id":9007199254740993123},{"id":23}]}}`))
		case "/api/v3/song/detail":
			if r.URL.Query().Get("c") != `[{"id":23}]` {
				t.Errorf("details query=%q", r.URL.Query().Get("c"))
			}
			_, _ = w.Write([]byte(`{"code":200,"songs":[{"id":23,"name":"二","dt":2000,"ar":[],"al":{}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s := &Service{Client: server.Client(), BaseURL: server.URL}
	d, err := s.ChartTracks(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(d.Tracks) != 2 || d.Tracks[0].ID != "9007199254740993123" || d.Tracks[1].Playback.ID != "23" {
		t.Fatalf("incomplete/incorrect chart: calls=%d tracks=%+v", calls, d.Tracks)
	}
}

// Cancellation prevents secondary catalog requests without issuing HTTP.
func TestBrowseCanceledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("canceled request reached server") }))
	defer server.Close()
	s := &Service{Client: server.Client(), BaseURL: server.URL}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Charts(ctx); err == nil {
		t.Fatal("canceled charts request succeeded")
	}
}

// A platform's unsuccessful response is an error, not an empty success list.
func TestChartsErrorNotEmptySuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"code":503,"list":[]}`)) }))
	defer server.Close()
	s := &Service{Client: server.Client(), BaseURL: server.URL}
	if _, err := s.Charts(context.Background()); err == nil {
		t.Fatal("business failure accepted")
	}
}
