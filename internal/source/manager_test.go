package source

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/domain"
	"github.com/yxxbc/haruhikage-app/internal/sourcehost"
)

type mockTransport func(*http.Request) (*http.Response, error)

func (f mockTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func pipeWorker(t *testing.T, transport mockTransport) *worker {
	t.Helper()
	if transport == nil {
		transport = func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected HTTP; no external access")
		}
	}
	input, writer := io.Pipe()
	reader, output := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer output.Close()
		_ = sourcehost.Serve(ctx, input, output, &http.Client{Transport: transport})
	}()
	w := newWorker(writer, reader, func() { cancel(); input.Close(); output.Close() })
	t.Cleanup(func() {
		w.close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("pipe host did not close")
		}
	})
	return w
}
func testManager(t *testing.T, transport mockTransport) (*Manager, *atomic.Int32) {
	t.Helper()
	m, err := NewManager(t.TempDir(), "not-executed")
	if err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	m.start = func() (*worker, error) { starts.Add(1); return pipeWorker(t, transport), nil }
	t.Cleanup(func() { m.Close() })
	return m, &starts
}
func scriptFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".js")
	data := `/*!` + "\n * @name " + name + "\n * @version 1.0\n * @author test\n */\n" + body
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

const sourceInit = `lx.send('inited',{status:true,sources:{wy:{name:'wy',actions:['musicUrl'],qualitys:['320k']}}});`

func simpleScript(t *testing.T, name, url string) string {
	return scriptFile(t, name, sourceInit+`lx.on('request',()=>Promise.resolve('`+url+`'));`)
}

var testTrack = domain.Track{Provider: "wy", ID: "900719925474099312345678", Playback: domain.PlaybackIdentity{ID: "900719925474099312345678"}}

func TestImportResolveExportAndOfflineRestart(t *testing.T) {
	// An initialized import becomes active, returns its transient URL across RPC and restarts without executing JS.
	m, starts := testManager(t, nil)
	path := simpleScript(t, "original", "https://audio.invalid/test?signature=transient")
	info, err := m.Import(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Active || !info.Enabled || info.Name != "original" || info.Version != "1.0" || info.Author != "test" {
		t.Fatal("imported metadata/state missing")
	}
	stream, err := m.Resolve(context.Background(), testTrack, "320k")
	if err != nil {
		t.Fatal(err)
	}
	if stream.URL != "https://audio.invalid/test?signature=transient" || stream.Size != -1 || stream.SourceID != info.ID || stream.SourceRevision != info.Revision {
		t.Fatal("resolution did not cross the wire intact")
	}
	if stream.Type != "" || stream.Level != "" || !stream.ExpiresAt.IsZero() {
		t.Fatal("host invented served metadata")
	}
	if starts.Load() != 1 {
		t.Fatal("initialized import worker was not reused")
	}
	duplicate, err := m.Import(context.Background(), path)
	if err != nil || duplicate.ID != info.ID || starts.Load() != 1 {
		t.Fatal("identical import restarted or duplicated")
	}
	index, err := os.ReadFile(filepath.Join(m.dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), "signature") || strings.Contains(string(index), "audio.invalid") {
		t.Fatal("index persisted a signed playback URL")
	}
	export := filepath.Join(t.TempDir(), "export.js")
	if err = m.Export(info.ID, export); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(path)
	got, _ := os.ReadFile(export)
	if string(want) != string(got) {
		t.Fatal("export modified the user script")
	}
	if err = m.Export(info.ID, filepath.Join(m.dir, "index.json")); err == nil {
		t.Fatal("export can overwrite manager storage")
	}
	m.Close()
	reopened, err := NewManager(m.dir, "missing-host-must-not-execute")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current, ok := reopened.Active()
	if !ok || current.ID != info.ID {
		t.Fatal("active source was not persisted")
	}
	current.Sources["wy"].Qualitys[0] = "mutated"
	if reopened.Capabilities()["wy"].Qualitys[0] != "320k" {
		t.Fatal("manager leaked mutable index data")
	}
}

func TestReplacementRollsBackAndKeepsIdentity(t *testing.T) {
	// A failed revision preserves the old worker/current; a successful explicit replacement keeps SourceID but changes Revision.
	m, _ := testManager(t, nil)
	original := simpleScript(t, "same-name", "https://audio.invalid/old")
	old, err := m.Import(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	bad := scriptFile(t, "same-name", `throw Error('private-test-marker');`)
	if _, err = m.Replace(context.Background(), old.ID, bad); err == nil || strings.Contains(err.Error(), "private-test-marker") {
		t.Fatal("bad revision was installed or leaked exception")
	}
	current, _ := m.Active()
	if current.Revision != old.Revision {
		t.Fatal("failed init replaced active revision")
	}
	stream, err := m.Resolve(context.Background(), testTrack, "320k")
	if err != nil || stream.URL != "https://audio.invalid/old" {
		t.Fatal("failed init destroyed old worker")
	}
	newPath := simpleScript(t, "same-name", "https://audio.invalid/new")
	updated, err := m.Replace(context.Background(), old.ID, newPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != old.ID || updated.Revision == old.Revision || !updated.Active || len(m.List()) != 1 {
		t.Fatal("revision identity or activation was lost")
	}
	stream, err = m.Resolve(context.Background(), testTrack, "320k")
	if err != nil || stream.URL != "https://audio.invalid/new" || stream.SourceRevision != updated.Revision {
		t.Fatal("new revision was not used")
	}
	if _, err = os.Stat(m.scriptPath(old)); !os.IsNotExist(err) {
		t.Fatal("old credential-bearing revision was left behind")
	}
}

func TestIndexCommitFailureKeepsCurrentRevision(t *testing.T) {
	// An index rename failure must not publish a staged revision or retire the current worker.
	m, _ := testManager(t, nil)
	old, err := m.Import(context.Background(), simpleScript(t, "old", "https://audio.invalid/old"))
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(m.dir, "index.json")
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(indexPath, 0700); err != nil {
		t.Fatal(err)
	}
	_, err = m.Replace(context.Background(), old.ID, simpleScript(t, "new", "https://audio.invalid/new"))
	if err == nil {
		t.Fatal("blocked index commit succeeded")
	}
	current, _ := m.Active()
	if current.Revision != old.Revision {
		t.Fatal("failed commit changed active index")
	}
	stream, resolveErr := m.Resolve(context.Background(), testTrack, "320k")
	if resolveErr != nil || stream.URL != "https://audio.invalid/old" {
		t.Fatal("failed commit closed the current worker")
	}
	if err = os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(indexPath, index, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSwitchDoesNotWaitForOldHTTPOrReturnItsResult(t *testing.T) {
	// Source switching cancels an old in-flight resolve without serializing on its network request or leaking its result.
	started := make(chan struct{})
	canceled := make(chan struct{})
	m, _ := testManager(t, func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		close(canceled)
		return nil, req.Context().Err()
	})
	oldPath := scriptFile(t, "old", sourceInit+`lx.on('request',()=>new Promise((resolve,reject)=>lx.request('https://mock.invalid/old',{},(err)=>err?reject(err):resolve('https://audio.invalid/old'))));`)
	if _, err := m.Import(context.Background(), oldPath); err != nil {
		t.Fatal(err)
	}
	newInfo, err := m.Import(context.Background(), simpleScript(t, "new", "https://audio.invalid/new"))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := m.Resolve(context.Background(), testTrack, "320k"); result <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("old request did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = m.Activate(ctx, newInfo.ID); err != nil {
		t.Fatal("switch blocked on old HTTP", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, sourcehost.ErrChanged) {
			t.Fatal("old request result not invalidated", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old resolve blocked")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("old transport leaked")
	}
	stream, err := m.Resolve(context.Background(), testTrack, "320k")
	if err != nil || stream.URL != "https://audio.invalid/new" {
		t.Fatal("old task affected the new source")
	}
}

func TestDisableRemoveAndCanceledMutation(t *testing.T) {
	// Disable/remove persist their state, never select another source, and honor cancellation before mutating it.
	m, _ := testManager(t, nil)
	first, err := m.Import(context.Background(), simpleScript(t, "first", "https://audio.invalid/first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Import(context.Background(), simpleScript(t, "second", "https://audio.invalid/second"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = m.Disable(ctx, first.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled mutation accepted")
	}
	if err = m.Disable(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Active(); ok {
		t.Fatal("disable automatically switched sources")
	}
	if _, err = m.Resolve(context.Background(), testTrack, "320k"); err == nil {
		t.Fatal("disabled source resolved")
	}
	if err = m.Activate(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.Remove(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Active(); ok {
		t.Fatal("remove automatically switched sources")
	}
	if len(m.List()) != 1 || m.List()[0].ID != second.ID {
		t.Fatal("remove modified another source")
	}
	if _, err = os.Stat(m.scriptPath(first)); !os.IsNotExist(err) {
		t.Fatal("deleted source script remains")
	}
}

func TestTamperedRevisionAndUnsafeIndexAreRejected(t *testing.T) {
	// Checksum verification and index validation stop tampered scripts and traversal IDs before execution.
	m, starts := testManager(t, nil)
	info, err := m.Import(context.Background(), simpleScript(t, "safe", "https://audio.invalid/safe"))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Disable(context.Background(), info.ID); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(m.scriptPath(info), []byte(`throw Error('tampered')`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.Activate(context.Background(), info.ID); err == nil || starts.Load() != 1 {
		t.Fatal("tampered revision was executed")
	}
	badDir := t.TempDir()
	if err = os.WriteFile(filepath.Join(badDir, "index.json"), []byte(`[{"id":"../../escape","revision":"bad","active":true,"enabled":true}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewManager(badDir, "unused"); err == nil {
		t.Fatal("unsafe index accepted")
	}
}

func TestManagerFixtureEndToEnd(t *testing.T) {
	// Import the exact sanitized fixture, initialize Goja, resolve via injected HTTP, and receive its signed URL through the manager's JSON pipe.
	var count atomic.Int32
	m, _ := testManager(t, func(req *http.Request) (*http.Response, error) {
		count.Add(1)
		if req.URL.Query().Get("songId") != testTrack.ID || req.URL.Query().Get("quality") != "320k" {
			return nil, errors.New("playback parameters changed")
		}
		return &http.Response{Request: req, StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":200,"url":"https://audio.invalid/fixture?signature=temporary"}`))}, nil
	})
	path := filepath.Join("..", "sourcehost", "testdata", "lx-contract-fixture.js")
	info, err := m.Import(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Sources) != 5 || count.Load() != 0 {
		t.Fatal("fixture init sent HTTP or lost capabilities")
	}
	stream, err := m.Resolve(context.Background(), testTrack, "320k")
	if err != nil || stream.URL != "https://audio.invalid/fixture?signature=temporary" || count.Load() != 1 {
		t.Fatal("fixture end-to-end resolution failed", err)
	}
}
