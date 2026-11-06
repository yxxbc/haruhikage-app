package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

// An animated WebP cover, as some playlists have, shows its first frame,
// in the bitmap too, which decodes its data again once it drops its pixels.
func TestAnimatedWebPCover(t *testing.T) {
	b, err := os.ReadFile("testdata/animated.webp")
	if err != nil {
		t.Fatal(err)
	}
	bmp, img, err := decodeArt(b)
	if err != nil {
		t.Fatal(err)
	}
	if r := img.Bounds(); r.Dx() != 24 || r.Dy() != 24 {
		t.Errorf("first frame is %v, want 24×24", r)
	}
	if w, h := bmp.Size(); w != 24 || h != 24 {
		t.Errorf("bitmap is %d×%d, want 24×24", w, h)
	}
}

// Covers not asked for in a while go, so that a long session does not
// keep every cover it showed; the ones still showing stay.
func TestArtCacheDropsUnusedCovers(t *testing.T) {
	now := time.Unix(1e9, 0)
	c := newArtCache(func(func()) {})
	defer c.Close()
	c.now, c.swept = func() time.Time { return now }, now
	c.art["shown"] = &artwork{used: now}
	c.art["gone"] = &artwork{used: now}
	for range 7 {
		now = now.Add(time.Minute)
		c.get("shown", false)
	}
	if _, ok := c.art["gone"]; ok {
		t.Error("kept a cover not asked for in 7 minutes")
	}
	if _, ok := c.art["shown"]; !ok {
		t.Error("dropped a cover asked for every minute")
	}
}

// The covers showing when the window went idle stay for its next frame.
// They went: nothing asks for covers while the window draws nothing, so
// after 5 minutes away the first frame, as the window was focused again,
// dropped every cover on the page, which flashed as they loaded again.
func TestArtCacheKeepsCoversWhileIdle(t *testing.T) {
	now := time.Unix(1e9, 0)
	c := newArtCache(func(func()) {})
	defer c.Close()
	c.now, c.swept = func() time.Time { return now }, now
	c.art["shown"] = &artwork{used: now}
	c.get("shown", false)
	now = now.Add(10 * time.Minute)
	if c.get("shown", false) == nil {
		t.Error("dropped the covers showing after 10 minutes idle")
	}
}

// LRU eviction honors decoded-memory and entry budgets, including failed URLs.
func TestArtCacheBoundedLRU(t *testing.T) {
	c := newArtCache(func(func()) {})
	defer c.Close()
	c.budget = 100
	now := time.Unix(100, 0)
	c.art["old"] = &artwork{used: now.Add(-2 * time.Second), cost: 50}
	c.art["recent"] = &artwork{used: now.Add(-time.Second), cost: 40}
	c.art["new"] = &artwork{used: now, cost: 40}
	c.bytes = 130
	c.trim("new")
	if c.art["old"] != nil || c.art["recent"] == nil || c.art["new"] == nil || c.bytes != 80 {
		t.Fatalf("LRU kept %v at %d bytes", c.art, c.bytes)
	}
	for i := range artEntries + 20 {
		c.art[strconv.Itoa(i)] = &artwork{used: now, failed: true}
	}
	c.trim("new")
	if len(c.art) > artEntries || c.bytes > c.budget {
		t.Fatalf("cache kept %d entries and %d bytes", len(c.art), c.bytes)
	}
}

// Artwork requests preserve signed URLs and publish their decoded cost on the UI thread.
func TestArtCacheOriginalURLAndPublication(t *testing.T) {
	data := artPNG(t)
	query := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query <- r.URL.RawQuery
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	updates := make(chan func(), artPending)
	c := newArtCache(func(fn func()) { updates <- fn })
	defer c.Close()
	url := srv.URL + "/cover?signature=a%2Bb&size=original"
	if c.get(url, true) != nil {
		t.Fatal("a new request unexpectedly returned an image")
	}
	fn := nextArtUpdate(t, updates)
	if len(c.art) != 0 {
		t.Fatal("worker mutated the UI cache before publication")
	}
	fn()
	if got := <-query; got != "signature=a%2Bb&size=original" {
		t.Fatalf("changed the provider's signed URL: %s", got)
	}
	a := c.get(url, true)
	if a == nil || a.failed || a.bmp == nil || a.backdrop == nil || c.bytes <= 0 || c.bytes > c.budget {
		t.Fatalf("bad published artwork: %+v, %d bytes", a, c.bytes)
	}
}

// Closing cancels an in-flight HTTP request and rejects subsequent work.
func TestArtCacheCloseCancelsHTTP(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer srv.Close()
	c := newArtCache(func(func()) {})
	defer c.Close()
	c.get(srv.URL, false)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP request did not start")
	}
	c.Close()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not cancel HTTP")
	}
	if c.get(srv.URL, false) != nil || len(c.busy) != 0 || len(c.art) != 0 {
		t.Fatal("closed cache accepted more work")
	}
}

// A completion queued before shutdown must not recreate bitmaps/cache entries after Close.
func TestArtCacheDiscardsLatePublication(t *testing.T) {
	data := artPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer srv.Close()
	updates := make(chan func(), artPending)
	c := newArtCache(func(fn func()) { updates <- fn })
	defer c.Close()
	c.get(srv.URL, true)
	fn := nextArtUpdate(t, updates)
	c.Close()
	fn()
	if len(c.art) != 0 || c.bytes != 0 {
		t.Fatal("late publication resurrected a closed image cache")
	}
}

// An untrusted image cannot bypass encoded-length or decoded-pixel limits.
func TestArtworkImageBudgets(t *testing.T) {
	client := &http.Client{Transport: artRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: io.NopCloser(io.LimitReader(artZeros{}, artMaxBytes+1))}, nil
	})}
	if _, err := fetchArt(context.Background(), client, "https://art.invalid/cover"); err == nil {
		t.Fatal("accepted an oversized unknown-length response")
	}
	b := artPNG(t)
	binary.BigEndian.PutUint32(b[16:20], 4096)
	binary.BigEndian.PutUint32(b[20:24], 4096)
	binary.BigEndian.PutUint32(b[29:33], crc32.ChecksumIEEE(b[12:29]))
	if _, _, err := decodeArt(b); err == nil {
		t.Fatal("decoded an image whose pixels exceed the memory budget")
	}
}

func artPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func nextArtUpdate(t *testing.T, updates <-chan func()) func() {
	t.Helper()
	select {
	case fn := <-updates:
		return fn
	case <-time.After(3 * time.Second):
		t.Fatal("artwork worker did not complete")
		return nil
	}
}

type artRoundTrip func(*http.Request) (*http.Response, error)

func (f artRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type artZeros struct{}

func (artZeros) Read(b []byte) (int, error) { clear(b); return len(b), nil }
