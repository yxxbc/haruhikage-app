// Package source manages user-owned LX scripts and their isolated execution.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/yxxbc/haruhikage-app/internal/domain"
	"github.com/yxxbc/haruhikage-app/internal/sourcehost"
)

type Support = sourcehost.Support

type Info struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Version  string             `json:"version"`
	Author   string             `json:"author"`
	Revision string             `json:"revision"`
	Active   bool               `json:"active"`
	Enabled  bool               `json:"enabled"`
	Sources  map[string]Support `json:"sources"`
}

// Manager never holds mu during JS/RPC/network work. opMu serializes only source
// mutations/host initialization. Existing resolutions don't block source switches.
type Manager struct {
	opMu                     sync.Mutex
	mu                       sync.Mutex
	dir, hostPath            string
	items                    []Info
	worker                   *worker
	workerID, workerRevision string
	epoch                    uint64
	closed                   bool
	ctx                      context.Context
	cancel                   context.CancelFunc
	start                    func() (*worker, error)
}

func NewManager(dir, hostPath string) (*Manager, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	m := &Manager{dir: dir, hostPath: hostPath, ctx: root, cancel: cancel}
	m.start = func() (*worker, error) { return startWorker(m.hostPath) }
	// Startup only reads the index. No worker, script execution, resolve, or
	// implicit health check occurs until an explicit operation needs it.
	f, err := os.Open(filepath.Join(dir, "index.json"))
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		cancel()
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &m.items) != nil || validateIndex(m.items) != nil {
		cancel()
		return nil, errors.New("source index is damaged; restore its backup")
	}
	return m, nil
}

func cloneInfo(info Info) Info {
	info.Sources = sourcehost.CloneCapabilities(sourcehost.Capabilities{Sources: info.Sources}).Sources
	return info
}
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, len(m.items))
	for i, v := range m.items {
		out[i] = cloneInfo(v)
	}
	return out
}
func (m *Manager) activeLocked() (Info, bool) {
	for _, v := range m.items {
		if v.Active && v.Enabled {
			return v, true
		}
	}
	return Info{}, false
}
func (m *Manager) Active() (Info, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.activeLocked()
	return cloneInfo(info), ok
}
func (m *Manager) Capabilities() map[string]Support {
	info, ok := m.Active()
	if !ok {
		return nil
	}
	return info.Sources
}

func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	w := m.detachLocked()
	m.mu.Unlock()
	w.close()
	// Wait for staging operations to see cancellation and discard candidates.
	m.opMu.Lock()
	m.opMu.Unlock()
	return nil
}
func (m *Manager) detachLocked() *worker {
	w := m.worker
	m.worker = nil
	m.workerID = ""
	m.workerRevision = ""
	m.epoch++
	return w
}
func (m *Manager) operationContext(ctx context.Context) (context.Context, func()) {
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.ctx, cancel)
	return child, func() { stop(); cancel() }
}

func (m *Manager) Import(ctx context.Context, path string) (Info, error) {
	return m.install(ctx, "", path)
}

// Check explicitly reinitializes the persisted script without changing its identity or active state.
func (m *Manager) Check(ctx context.Context, id string) (Info, error) {
	m.mu.Lock()
	var info Info
	for _, item := range m.items {
		if item.ID == id {
			info = cloneInfo(item)
			break
		}
	}
	m.mu.Unlock()
	if info.ID == "" {
		return Info{}, errors.New("source does not exist")
	}
	data, err := m.loadRevision(info)
	if err != nil {
		return Info{}, err
	}
	w, err := m.start()
	if err != nil {
		return Info{}, err
	}
	defer w.close()
	res, err := w.call(ctx, sourcehost.Request{Method: "init", Script: string(data)})
	if err != nil {
		return Info{}, err
	}
	if res.Caps == nil || sourcehost.ValidateCapabilities(*res.Caps) != nil {
		return Info{}, sourcehost.ErrInit
	}
	if b, _ := json.Marshal(res.Caps.Sources); string(b) != string(mustJSON(info.Sources)) {
		return Info{}, errors.New("source capabilities changed; replace the script to accept changes")
	}
	return info, nil
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// Replace atomically installs a revision under the same SourceID. Initialization
// or persistence failure leaves the old revision, activation and worker intact.
// Import never guesses replacement identity from a script's untrusted name.
func (m *Manager) Replace(ctx context.Context, id, path string) (Info, error) {
	if id == "" {
		return Info{}, errors.New("source ID is required")
	}
	return m.install(ctx, id, path)
}

func (m *Manager) install(ctx context.Context, replaceID, path string) (Info, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	ctx, cancel := m.operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Info{}, sourcehost.ErrClosed
	}
	at := -1
	if replaceID != "" {
		for i, v := range m.items {
			if v.ID == replaceID {
				at = i
				break
			}
		}
		if at < 0 {
			m.mu.Unlock()
			return Info{}, errors.New("source does not exist")
		}
	}
	m.mu.Unlock()
	data, err := readScript(path)
	if err != nil {
		return Info{}, err
	}
	revision := digest(data)
	m.mu.Lock()
	for _, info := range m.items {
		if info.Revision == revision && (replaceID == "" || info.ID == replaceID) {
			m.mu.Unlock()
			return cloneInfo(info), nil
		}
	}
	m.mu.Unlock()
	w, err := m.start()
	if err != nil {
		return Info{}, err
	}
	keep := false
	defer func() {
		if !keep {
			w.close()
		}
	}()
	res, err := w.call(ctx, sourcehost.Request{Method: "init", Script: string(data)})
	if err != nil {
		return Info{}, err
	}
	if res.Caps == nil || sourcehost.ValidateCapabilities(*res.Caps) != nil {
		return Info{}, sourcehost.ErrInit
	}
	info := Info{ID: revision[:24], Revision: revision, Name: metadata(data, "name"), Version: metadata(data, "version"), Author: metadata(data, "author"), Enabled: true, Sources: sourcehost.CloneCapabilities(*res.Caps).Sources}
	if info.Name == "" {
		info.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if len(info.Name) > 256 {
			info.Name = "LX source"
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return Info{}, err
	}
	if m.closed {
		return Info{}, sourcehost.ErrClosed
	}
	items := append([]Info(nil), m.items...)
	var old Info
	if at >= 0 {
		old = items[at]
		info.ID = old.ID
		info.Active = old.Active
		info.Enabled = old.Enabled
		items[at] = info
	} else {
		// The first import is activated after successful initialization only.
		info.Active = len(items) == 0
		for _, v := range items {
			if v.ID == info.ID {
				return Info{}, errors.New("source identity collision")
			}
		}
		items = append(items, info)
	}
	created, err := m.storeRevision(info, data)
	if err != nil {
		return Info{}, err
	}
	if err = m.save(items); err != nil {
		if created {
			_ = os.Remove(m.scriptPath(info))
		}
		return Info{}, err
	}
	m.items = items
	if info.Active && info.Enabled {
		previous := m.detachLocked()
		m.worker = w
		m.workerID = info.ID
		m.workerRevision = info.Revision
		keep = true
		// close doesn't acquire Manager locks or wait for RPC; old results check
		// epoch before they can be returned to the player.
		previous.close()
	}
	if old.Revision != "" && old.Revision != info.Revision {
		_ = os.Remove(m.scriptPath(old))
	}
	return cloneInfo(info), nil
}

func (m *Manager) Activate(ctx context.Context, id string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	ctx, cancel := m.operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return sourcehost.ErrClosed
	}
	at := -1
	for i, v := range m.items {
		if v.ID == id {
			at = i
			break
		}
	}
	if at < 0 {
		m.mu.Unlock()
		return errors.New("source does not exist")
	}
	info := m.items[at]
	if info.Active && info.Enabled && m.worker != nil && !m.worker.dead() {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	w, err := m.initWorker(ctx, info)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			w.close()
		}
	}()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	if m.closed {
		return sourcehost.ErrClosed
	}
	items := append([]Info(nil), m.items...)
	for i := range items {
		items[i].Active = i == at
	}
	items[at].Enabled = true
	if err = m.save(items); err != nil {
		return err
	}
	old := m.detachLocked()
	m.items = items
	m.worker = w
	m.workerID = id
	m.workerRevision = info.Revision
	keep = true
	old.close()
	return nil
}

func (m *Manager) Disable(ctx context.Context, id string) error { return m.changeState(ctx, id, false) }
func (m *Manager) Remove(ctx context.Context, id string) error  { return m.changeState(ctx, id, true) }
func (m *Manager) changeState(ctx context.Context, id string, remove bool) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return sourcehost.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	items := append([]Info(nil), m.items...)
	at := -1
	for i, v := range items {
		if v.ID == id {
			at = i
			break
		}
	}
	if at < 0 {
		return errors.New("source does not exist")
	}
	info := items[at]
	if remove {
		items = append(items[:at], items[at+1:]...)
	} else {
		items[at].Active = false
		items[at].Enabled = false
	}
	if err := m.save(items); err != nil {
		return err
	}
	m.items = items
	if m.workerID == id {
		m.detachLocked().close()
	}
	if remove {
		if err := os.Remove(m.scriptPath(info)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("source removed from index; script cleanup failed: %w", err)
		}
	}
	return nil
}

func (m *Manager) Export(id, destination string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return sourcehost.ErrClosed
	}
	var info Info
	for _, v := range m.items {
		if v.ID == id {
			info = v
			break
		}
	}
	m.mu.Unlock()
	if info.ID == "" {
		return errors.New("source does not exist")
	}
	// Export is explicit and includes the script's own credentials. Prevent a
	// destination inside manager storage from corrupting the index/revisions.
	root, err := filepath.Abs(m.dir)
	if err != nil {
		return err
	}
	dest, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, dest)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("export destination must be outside source storage")
	}
	data, err := m.loadRevision(info)
	if err != nil {
		return err
	}
	return atomicWrite(destination, data)
}

func (m *Manager) initWorker(ctx context.Context, info Info) (*worker, error) {
	data, err := m.loadRevision(info)
	if err != nil {
		return nil, err
	}
	w, err := m.start()
	if err != nil {
		return nil, err
	}
	res, err := w.call(ctx, sourcehost.Request{Method: "init", Script: string(data)})
	if err == nil && (res.Caps == nil || sourcehost.ValidateCapabilities(*res.Caps) != nil) {
		err = sourcehost.ErrInit
	}
	if err == nil {
		// Capabilities are part of the validated persisted revision contract.
		a, _ := json.Marshal(info.Sources)
		b, _ := json.Marshal(res.Caps.Sources)
		if string(a) != string(b) {
			err = errors.New("source capabilities changed; import a new revision")
		}
	}
	if err != nil {
		w.close()
		return nil, err
	}
	return w, nil
}

func (m *Manager) resolverWorker(ctx context.Context) (*worker, Info, uint64, error) {
	// Fast path allows overlapping resolutions and the script's own dedup/cache.
	m.mu.Lock()
	info, ok := m.activeLocked()
	if m.closed {
		m.mu.Unlock()
		return nil, Info{}, 0, sourcehost.ErrClosed
	}
	if !ok {
		m.mu.Unlock()
		return nil, Info{}, 0, errors.New("import and activate an LX source before playing")
	}
	if m.worker != nil && m.workerID == info.ID && m.workerRevision == info.Revision && !m.worker.dead() {
		w, epoch := m.worker, m.epoch
		m.mu.Unlock()
		return w, info, epoch, nil
	}
	m.mu.Unlock()
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	current, ok := m.activeLocked()
	if m.closed {
		m.mu.Unlock()
		return nil, Info{}, 0, sourcehost.ErrClosed
	}
	if !ok || current.ID != info.ID || current.Revision != info.Revision {
		m.mu.Unlock()
		return nil, Info{}, 0, sourcehost.ErrChanged
	}
	if m.worker != nil && !m.worker.dead() {
		w, epoch := m.worker, m.epoch
		m.mu.Unlock()
		return w, current, epoch, nil
	}
	m.mu.Unlock()
	w, err := m.initWorker(ctx, current)
	if err != nil {
		return nil, Info{}, 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || ctx.Err() != nil {
		w.close()
		if ctx.Err() != nil {
			return nil, Info{}, 0, ctx.Err()
		}
		return nil, Info{}, 0, sourcehost.ErrClosed
	}
	old := m.detachLocked()
	m.worker = w
	m.workerID = current.ID
	m.workerRevision = current.Revision
	old.close()
	return w, current, m.epoch, nil
}

func (m *Manager) Resolve(ctx context.Context, t domain.Track, quality string) (*domain.Stream, error) {
	ctx, cancel := m.operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w, info, epoch, err := m.resolverWorker(ctx)
	if err != nil {
		return nil, err
	}
	res, err := w.call(ctx, sourcehost.Request{Method: "resolve", Track: t, Quality: quality})
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, sourcehost.ErrClosed
	}
	if m.epoch != epoch || m.worker != w {
		return nil, sourcehost.ErrChanged
	}
	if err != nil {
		if w.dead() || errors.Is(err, sourcehost.ErrClosed) {
			m.detachLocked().close()
		}
		return nil, err
	}
	if res.Stream == nil || !sourcehost.ValidURL(res.Stream.URL) || res.Stream.Size != -1 {
		m.detachLocked().close()
		return nil, sourcehost.ErrProtocol
	}
	stream := res.Stream.Domain()
	stream.SourceID = info.ID
	stream.SourceRevision = info.Revision
	// Served quality, codec and expiry are unknown. Never invent them from the
	// requested quality or this script's internal URL-cache TTL.
	return stream, nil
}

func readScript(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("source must be a regular JavaScript file")
	}
	data, err := io.ReadAll(io.LimitReader(f, sourcehost.MaxScriptBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > sourcehost.MaxScriptBytes || !utf8.Valid(data) {
		return nil, errors.New("source must be UTF-8 JavaScript of at most 2 MiB")
	}
	return data, nil
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (m *Manager) scriptPath(info Info) string {
	return filepath.Join(m.dir, info.ID+"-"+info.Revision+".js")
}
func (m *Manager) loadRevision(info Info) ([]byte, error) {
	data, err := readScript(m.scriptPath(info))
	if err != nil {
		return nil, err
	}
	if digest(data) != info.Revision {
		return nil, errors.New("source revision checksum mismatch; reimport the script")
	}
	return data, nil
}
func (m *Manager) storeRevision(info Info, data []byte) (bool, error) {
	path := m.scriptPath(info)
	old, err := readScript(path)
	if err == nil {
		if digest(old) != info.Revision {
			return false, errors.New("stored source revision is damaged")
		}
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, err
	}
	return true, atomicWrite(path, data)
}
func (m *Manager) save(items []Info) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("source index exceeds limit")
	}
	return atomicWrite(filepath.Join(m.dir, "index.json"), append(data, '\n'))
}
func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".source-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func validateIndex(items []Info) error {
	seen := make(map[string]bool)
	active := false
	for _, v := range items {
		id, e1 := hex.DecodeString(v.ID)
		rev, e2 := hex.DecodeString(v.Revision)
		if e1 != nil || len(id) != 12 || v.ID != hex.EncodeToString(id) || e2 != nil || len(rev) != 32 || v.Revision != hex.EncodeToString(rev) || seen[v.ID] || len(v.Name) > 256 || len(v.Author) > 256 || len(v.Version) > 256 {
			return sourcehost.ErrProtocol
		}
		if v.Active {
			if active || !v.Enabled {
				return sourcehost.ErrProtocol
			}
			active = true
		}
		if sourcehost.ValidateCapabilities(sourcehost.Capabilities{Status: true, Sources: v.Sources}) != nil {
			return sourcehost.ErrProtocol
		}
		seen[v.ID] = true
	}
	return nil
}
func metadata(data []byte, key string) string {
	// Only the leading LX metadata comment, never arbitrary matching code lines.
	text := strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff"))
	if !strings.HasPrefix(text, "/*") {
		return ""
	}
	end := strings.Index(text, "*/")
	if end < 0 {
		return ""
	}
	text = text[:end]
	re := regexp.MustCompile(`(?m)^\s*\*?\s*@` + key + `\s+([^\r\n]+)`)
	match := re.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	value := strings.TrimSpace(match[1])
	if len(value) > 256 {
		return ""
	}
	return value
}
