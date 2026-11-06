package player

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

import "github.com/yxxbc/haruhikage-app/internal/domain"

func rendition() *domain.Stream {
	return &domain.Stream{SourceID: "fixture", SourceRevision: "r1", Type: "mp3", Level: "320k"}
}
func storeCache(t *testing.T, c *Cache, tr domain.Track, st *domain.Stream, q string, n int) string {
	t.Helper()
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(c.dir, "test-*.part")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(make([]byte, n))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	meta := *st
	meta.Size = int64(n)
	if !c.keep(tr, &meta, q, f.Name(), c.epoch) {
		t.Fatal("cache commit failed")
	}
	return filepath.Join(c.dir, cacheKey(tr, &meta, q)+"."+meta.Type)
}

// Persistent identity includes platform, resolver ID/revision and EXACT
// requested quality; no source/quality substitute may satisfy a lookup.
func TestCacheIdentityIsolation(t *testing.T) {
	c := NewCache(t.TempDir(), 400)
	st := rendition()
	st.URL = "https://invalid.example/audio?credential=not-persisted"
	st.Headers = make(http.Header)
	st.Headers.Set("Authorization", "not-persisted")
	path := storeCache(t, c, track("1"), st, "320k", 100)
	meta, err := os.ReadFile(strings.TrimSuffix(path, ".mp3") + ".json")
	if err != nil || strings.Contains(string(meta), "not-persisted") {
		t.Fatal("persisted a resolution credential", err)
	}
	var stored domain.Stream
	if json.Unmarshal(meta, &stored) != nil || stored.URL != "" || stored.Headers != nil {
		t.Fatal("cache persisted URL/headers")
	}
	for _, variant := range []struct{ provider, id, source, revision, q string }{
		{"tx", "1", "fixture", "r1", "320k"}, {"wy", "2", "fixture", "r1", "320k"},
		{"wy", "1", "other", "r1", "320k"}, {"wy", "1", "fixture", "r2", "320k"},
		{"wy", "1", "fixture", "r1", "flac"},
	} {
		tr := track(variant.id)
		tr.Provider = variant.provider
		info := *st
		info.SourceID, info.SourceRevision = variant.source, variant.revision
		if dl, _, ok := c.lookup(tr, &info, variant.q); ok {
			dl.Close()
			t.Fatal("cross-rendition cache hit", variant)
		}
	}
	dl, info, ok := c.lookup(track("1"), st, "320k")
	if !ok || info.Size != 100 {
		t.Fatal("exact rendition not found")
	}
	dl.Close()
	<-dl.released
}

// Clear hides an in-use file without invalidating its reader, and deletes
// it after the lease closes (including on Windows).
func TestCacheClearLeasedFile(t *testing.T) {
	c := NewCache(t.TempDir(), 400)
	st := rendition()
	path := storeCache(t, c, track("1"), st, "320k", 100)
	dl, _, ok := c.lookup(track("1"), st, "320k")
	if !ok {
		t.Fatal("missing lease")
	}
	c.Clear()
	if _, _, ok := c.lookup(track("1"), st, "320k"); ok {
		t.Fatal("cleared file still discoverable")
	}
	r := &downloadReader{d: dl}
	buf := make([]byte, 100)
	if n, err := r.Read(buf); n != 100 || err != nil {
		t.Fatalf("lease broken: %d, %v", n, err)
	}
	dl.Close()
	<-dl.released
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file survived last lease", err)
	}
}

// Cache eviction skips active readers and accounts only successful deletes.
func TestCachePrunesOldFiles(t *testing.T) {
	c := NewCache(t.TempDir(), 400)
	st := rendition()
	old := storeCache(t, c, track("1"), st, "320k", 100)
	dl, _, _ := c.lookup(track("1"), st, "320k")
	for _, id := range []string{"2", "3", "4", "5"} {
		storeCache(t, c, track(id), st, "320k", 100)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("evicted active file", err)
	}
	dl.Close()
	<-dl.released
	if c.Size() > 400 {
		t.Fatal("cache remains above limit")
	}
	c.Clear()
	if c.Size() != 0 {
		t.Fatal("clear left unleased cache files")
	}
}

func drainStream(s stream) error {
	buf := make([]float32, 4096)
	for {
		_, err := s.Read(buf)
		if err != nil {
			return err
		}
	}
}

// Clear is a commit barrier: a complete in-flight download cannot silently
// recreate files after the user has cleared the cache.
func TestClearBlocksInflightCommit(t *testing.T) {
	b, err := os.ReadFile("testdata/seek.mp3")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(b[:len(b)/2])
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = w.Write(b[len(b)/2:])
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	c := NewCache(t.TempDir(), 1<<30)
	st := rendition()
	st.URL = srv.URL
	dl := c.download(context.Background(), track("1"), st, "320k")
	defer dl.Close()
	s, err := openStream(dl, 5000)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	c.Clear()
	close(release)
	if err := drainStream(s); err != io.EOF {
		t.Fatal(err)
	}
	_ = s.Close()
	<-dl.released
	if c.Size() != 0 {
		t.Fatal("old download repopulated cleared cache")
	}
}

// Cache-disabled playback still stages on disk and removes it at last close.
func TestCacheDisabledUsesDisk(t *testing.T) {
	b, err := os.ReadFile("testdata/seek.flac")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(b) }))
	defer srv.Close()
	c := NewCache(t.TempDir(), 0)
	st := rendition()
	st.URL = srv.URL
	dl := c.download(context.Background(), track("1"), st, "flac")
	dl.waitFor(math.MaxInt64)
	dl.mu.Lock()
	file, part, ram := dl.file, dl.part, len(dl.data)
	dl.mu.Unlock()
	if file == nil || part == "" || ram != 0 {
		t.Fatal("disabled cache used RAM fallback")
	}
	dl.Close()
	<-dl.released
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatal("staging file not removed", err)
	}
	if c.Size() != 0 {
		t.Fatal("disabled cache persisted audio")
	}
}

// Unknown HTTP length is complete only at actual EOF, then its real byte
// count and detected format are stored rather than resolver guesses.
func TestUnknownLengthAndFormat(t *testing.T) {
	b, err := os.ReadFile("testdata/seek.flac")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.(http.Flusher).Flush()
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	c := NewCache(t.TempDir(), 1<<30)
	st := rendition()
	st.URL = srv.URL + "/misleading.mp3"
	st.Size = 1 << 40
	st.Type = "mp3"
	dl := c.download(context.Background(), track("1"), st, "flac24bit")
	s, err := openStream(dl, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*flacStream); !ok {
		t.Fatal("trusted extension/type instead of bytes")
	}
	if err := drainStream(s); err != io.EOF {
		t.Fatal(err)
	}
	_ = s.Close()
	<-dl.released
	kept, info, ok := c.lookup(track("1"), st, "flac24bit")
	if !ok || info.Size != int64(len(b)) || info.Type != "flac" {
		t.Fatalf("actual metadata not stored: %+v, %v", info, ok)
	}
	kept.Close()
	<-kept.released
}

// A short body, a partial MPEG frame or a truncated FLAC frame must be an
// error, not EOF, and must never be admitted as a successful cache entry.
func TestTruncatedAudioNeverCached(t *testing.T) {
	for _, codec := range []string{"mp3", "flac"} {
		t.Run(codec, func(t *testing.T) {
			b, err := os.ReadFile("testdata/seek." + codec)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.(http.Flusher).Flush(); _, _ = w.Write(b[:len(b)-37]) }))
			defer srv.Close()
			c := NewCache(t.TempDir(), 1<<30)
			st := rendition()
			st.URL = srv.URL
			dl := c.download(context.Background(), track("1"), st, "320k")
			s, err := openStream(dl, 5000)
			if err == nil {
				err = drainStream(s)
				_ = s.Close()
			} else {
				dl.Close()
			}
			<-dl.released
			if err == nil || err == io.EOF {
				t.Fatal("truncation reported success")
			}
			if c.Size() != 0 {
				t.Fatal("truncated audio cached")
			}
		})
	}
}

// Disk failure ends the transfer without reading previous bytes into RAM.
func TestDiskWriteFailureHasNoMemoryFallback(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed-*.part")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	d := opened(f, 0)
	d.done = false
	if err := d.write(make([]byte, 4096)); err == nil {
		t.Fatal("write to closed disk file succeeded")
	}
	if len(d.data) != 0 {
		t.Fatal("disk failure fell back to memory")
	}
	d.Close()
	<-d.released
}
