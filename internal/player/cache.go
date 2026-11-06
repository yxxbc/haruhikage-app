package player

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// Cache keeps complete, validated audio, never resolutions or credentials.
// mu serializes commits and Clear. A lease protects an open file on Windows.
// None of these operations run under a download's decoder/read lock.
type Cache struct {
	dir     string
	mu      sync.Mutex
	limit   int64
	epoch   uint64
	leases  map[string]int
	removed map[string]bool
}

func NewCache(dir string, limit int64) *Cache {
	c := &Cache{dir: dir, limit: max(limit, 0), leases: make(map[string]int), removed: make(map[string]bool)}
	// The application owns this directory; remove abandoned temporary files
	// only at startup, not while another download may still be writing one.
	parts, _ := filepath.Glob(filepath.Join(dir, "*.part"))
	for _, path := range parts {
		_ = os.Remove(path)
	}
	return c
}

func cacheKey(t domain.Track, st *domain.Stream, quality string) string {
	b, _ := json.Marshal([]string{t.Provider, t.ID, st.SourceID, st.SourceRevision, quality})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (c *Cache) SetLimit(limit int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limit = max(limit, 0)
	if c.limit == 0 {
		c.epoch++
	}
	c.pruneLocked("")
}

// lookup is exact: a different quality or source revision is not a hit.
func (c *Cache) lookup(t domain.Track, st *domain.Stream, quality string) (*download, *domain.Stream, bool) {
	if c == nil {
		return nil, nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.limit == 0 {
		return nil, nil, false
	}
	key := cacheKey(t, st, quality)
	b, err := os.ReadFile(filepath.Join(c.dir, key+".json"))
	if err != nil {
		return nil, nil, false
	}
	var meta domain.Stream
	if json.Unmarshal(b, &meta) != nil || (meta.Type != "mp3" && meta.Type != "flac") || meta.Size <= 0 || meta.SourceID != st.SourceID || meta.SourceRevision != st.SourceRevision {
		return nil, nil, false
	}
	path := filepath.Join(c.dir, key+"."+meta.Type)
	if c.removed[path] {
		return nil, nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, false
	}
	fi, err := f.Stat()
	if err != nil || fi.Size() != meta.Size {
		_ = f.Close()
		return nil, nil, false
	}
	c.leases[path]++
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	d := opened(f, meta.Size)
	d.onRelease = func() {
		d.mu.Lock()
		bad := d.rejected
		d.mu.Unlock()
		if bad {
			c.mu.Lock()
			c.removed[path] = true
			c.mu.Unlock()
		}
		c.release(path)
	}
	return d, &meta, true
}

func (c *Cache) release(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.leases[path]--
	if c.leases[path] == 0 {
		delete(c.leases, path)
		if c.removed[path] && c.removeLocked(path) {
			delete(c.removed, path)
		}
	}
	c.pruneLocked("")
}

// download always uses bounded disk staging, even with Cache=nil/limit=0.
// A disk error is a playback error, not an unbounded RAM fallback.
func (c *Cache) download(ctx context.Context, t domain.Track, st *domain.Stream, quality string) *download {
	var epoch uint64
	dir := ""
	if c != nil {
		c.mu.Lock()
		epoch, dir = c.epoch, c.dir
		c.mu.Unlock()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return failedDownload(err)
		}
	}
	part, err := os.CreateTemp(dir, "audio-*.part")
	if err != nil {
		return failedDownload(err)
	}
	meta := *st
	meta.Headers = st.Headers.Clone()
	d := startDownload(ctx, &meta, part)
	if c != nil {
		d.keep = func(path string, n int64, codec string) bool {
			meta.Type, meta.Size = codec, n
			return c.keep(t, &meta, quality, path, epoch)
		}
	}
	return d
}

// keep receives a CLOSED temporary file, after both HTTP and decoder EOF.
// Holding mu across the rename+metadata transaction makes Clear a barrier.
func (c *Cache) keep(t domain.Track, st *domain.Stream, quality, part string, epoch uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if epoch != c.epoch || c.limit <= 0 || st.SourceID == "" || st.SourceRevision == "" || st.Size <= 0 || st.Size > c.limit/4 || (st.Type != "mp3" && st.Type != "flac") {
		return false
	}
	key := cacheKey(t, st, quality)
	path := filepath.Join(c.dir, key+"."+st.Type)
	if c.leases[path] > 0 {
		return false
	}
	// URLs, request headers and expiry are excluded by domain.Stream's JSON.
	meta, err := json.Marshal(st)
	if err != nil {
		return false
	}
	if err = os.Rename(part, path); err != nil {
		return false
	}
	tmp, err := os.CreateTemp(c.dir, "meta-*.part")
	if err != nil {
		_ = os.Remove(path)
		return false
	}
	name := tmp.Name()
	_, err = tmp.Write(meta)
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(c.dir, key+".json"))
	}
	if err != nil {
		_ = os.Remove(name)
		_ = os.Remove(path)
		return false
	}
	delete(c.removed, path)
	c.pruneLocked(path)
	return true
}

func (c *Cache) Size() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int64
	for _, f := range c.songs() {
		n += f.size
	}
	return n
}

// Clear hides leased files immediately and deletes them when their last
// reader closes. Earlier downloads cannot repopulate the cleared cache.
func (c *Cache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	for _, f := range c.songs() {
		c.removed[f.path] = true
		if c.leases[f.path] == 0 && c.removeLocked(f.path) {
			delete(c.removed, f.path)
		}
	}
}

type cachedSong struct {
	path string
	size int64
	used time.Time
}

// songs is called with mu held (or by tests without concurrent mutations).
func (c *Cache) songs() []cachedSong {
	entries, _ := os.ReadDir(c.dir)
	var out []cachedSong
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if ext != ".mp3" && ext != ".flac" {
			continue
		}
		info, err := e.Info()
		if err == nil {
			out = append(out, cachedSong{filepath.Join(c.dir, e.Name()), info.Size(), info.ModTime()})
		}
	}
	return out
}

func (c *Cache) removeLocked(path string) bool {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false
	}
	_ = os.Remove(strings.TrimSuffix(path, filepath.Ext(path)) + ".json")
	return true
}

func (c *Cache) pruneLocked(keep string) {
	songs := c.songs()
	slices.SortFunc(songs, func(a, b cachedSong) int { return a.used.Compare(b.used) })
	var total int64
	for _, f := range songs {
		total += f.size
	}
	for _, f := range songs {
		if total <= c.limit {
			break
		}
		if f.path != keep && c.leases[f.path] == 0 && c.removeLocked(f.path) {
			total -= f.size
		}
	}
}
