package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func backupFixture(t *testing.T) (*Store, backupDocument) {
	t.Helper()
	ctx := context.Background()
	s := testStore(t)
	p := createPlaylist(t, s, "Backed up")
	a, b := song("wy", "same"), song("tx", "same")
	mustAdd(t, s, p.ID, b, a, b)
	createPlaylist(t, s, "Empty playlist")
	if err := s.Favorite(ctx, a, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Favorite(ctx, b, true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlay(ctx, a, 123456789); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveState(ctx, "session", json.RawMessage(`{"version":2,"paused":true,"position":123456789,"repeat":"one","shuffle":true,"queue":[{"provider":"tx","id":"same"},{"provider":"wy","id":"same"}]}`)); err != nil {
		t.Fatal(err)
	}
	var doc backupDocument
	if err := json.Unmarshal(exportBytes(t, s), &doc); err != nil {
		t.Fatal(err)
	}
	return s, doc
}

func encodeBackup(t *testing.T, doc backupDocument) []byte {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBackupRoundTripReplacesLibraryAndPreservesOrder(t *testing.T) {
	// A versioned backup restores stable entries and equal-time event/like order.
	ctx := context.Background()
	source, doc := backupFixture(t)
	a, b := song("wy", "same"), song("tx", "same")
	stamp := time.Date(2025, 4, 3, 2, 1, 0, 123, time.UTC)
	doc.Favorites = []backupFavorite{{Track: a.Ref(), AddedAt: stamp}, {Track: b.Ref(), AddedAt: stamp}}
	doc.History = []backupHistory{
		{Track: b.Ref(), Position: 3*time.Second + 17, PlayedAt: stamp},
		{Track: a.Ref(), Position: 2*time.Second + 13, PlayedAt: stamp},
		{Track: b.Ref(), Position: time.Second + 11, PlayedAt: stamp},
	}
	if err := source.Import(ctx, bytes.NewReader(encodeBackup(t, doc))); err != nil {
		t.Fatal(err)
	}
	before := exportBytes(t, source)
	path := filepath.Join(t.TempDir(), "restored.sqlite")
	dest := openTestStore(t, path)
	old := createPlaylist(t, dest, "Will be replaced")
	mustAdd(t, dest, old.ID, song("mg", "old"))
	if err := dest.SaveState(ctx, "old-session", json.RawMessage(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := dest.Import(ctx, bytes.NewReader(before)); err != nil {
		t.Fatal(err)
	}
	if after := exportBytes(t, dest); !bytes.Equal(before, after) {
		t.Fatal("backup round trip changed the user-visible snapshot")
	}
	if _, err := dest.Tracks(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("import merged rather than replaced old playlist: %v", err)
	}
	if _, err := dest.LoadState(ctx, "old-session"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("import retained old state: %v", err)
	}
	favorites, err := dest.Favorites(ctx)
	if err != nil || !reflect.DeepEqual(trackKeys(favorites), []string{a.Key(), b.Key()}) {
		t.Fatalf("equal-time favorite order = %+v, err=%v", favorites, err)
	}
	history, err := dest.History(ctx, 10)
	if err != nil || len(history) != 3 {
		t.Fatalf("history = %+v, err=%v", history, err)
	}
	for i, entry := range history {
		want := doc.History[i]
		if entry.Track.Key() != want.Track.Key() || entry.Position != want.Position || !entry.PlayedAt.Equal(want.PlayedAt) {
			t.Fatalf("history item %d = %+v, want %+v", i, entry, want)
		}
	}
	for _, playlist := range doc.Playlists {
		entries, err := dest.Entries(ctx, playlist.ID)
		if err != nil || len(entries) != len(playlist.Entries) {
			t.Fatalf("restored entries = %+v, err=%v", entries, err)
		}
		for i, entry := range entries {
			if entry.ID != playlist.Entries[i].ID || entry.Track.Key() != playlist.Entries[i].Track.Key() {
				t.Fatalf("entry identity/order changed: %+v", entries)
			}
		}
	}
	if err := dest.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	if after := exportBytes(t, reopened); !bytes.Equal(before, after) {
		t.Fatal("import was not persistent after reopening SQLite")
	}
}

func TestDamagedBackupLeavesExistingLibraryUntouched(t *testing.T) {
	// Truncated, inconsistent, future and secret-bearing backups cannot replace data.
	ctx := context.Background()
	s, doc := backupFixture(t)
	before := exportBytes(t, s)
	mutate := func(fn func(*backupDocument)) []byte {
		var copy backupDocument
		if err := json.Unmarshal(encodeBackup(t, doc), &copy); err != nil {
			t.Fatal(err)
		}
		fn(&copy)
		return encodeBackup(t, copy)
	}
	populated := func(d *backupDocument) *backupPlaylist {
		for i := range d.Playlists {
			if len(d.Playlists[i].Entries) > 0 {
				return &d.Playlists[i]
			}
		}
		t.Fatal("fixture has no populated playlist")
		return nil
	}
	withUnknown := func(name string, value json.RawMessage) []byte {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(before, &object); err != nil {
			t.Fatal(err)
		}
		object[name] = value
		data, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	cases := map[string][]byte{
		"truncated":             before[:len(before)/2],
		"trailing value":        append(append([]byte{}, before...), []byte(` {}`)...),
		"wrong format":          mutate(func(d *backupDocument) { d.Format = "other-app" }),
		"future version":        mutate(func(d *backupDocument) { d.Version++ }),
		"missing required data": mutate(func(d *backupDocument) { d.Favorites = nil }),
		"dangling reference":    mutate(func(d *backupDocument) { populated(d).Entries[0].Track.ID = "absent" }),
		"duplicate track":       mutate(func(d *backupDocument) { d.Tracks = append(d.Tracks, d.Tracks[0]) }),
		"duplicate entry":       mutate(func(d *backupDocument) { populated(d).Entries[1].ID = populated(d).Entries[0].ID }),
		"wrong entry count":     mutate(func(d *backupDocument) { populated(d).Count++ }),
		"negative position":     mutate(func(d *backupDocument) { d.History[0].Position = -1 }),
		"zero timestamp":        mutate(func(d *backupDocument) { d.History[0].PlayedAt = time.Time{} }),
		"invalid state":         mutate(func(d *backupDocument) { d.State[0].Value = json.RawMessage(`{"version":1,"version":2}`) }),
		"source script state":   mutate(func(d *backupDocument) { d.State[0].Value = json.RawMessage(`{"sourceScript":"fixture only"}`) }),
		"credential state": mutate(func(d *backupDocument) {
			d.State[0].Value = json.RawMessage(`{"credentials":{"token":"fixture only"}}`)
		}),
		"source namespace":            mutate(func(d *backupDocument) { d.State[0].Key = "sources.current" }),
		"unknown source section":      withUnknown("sources", json.RawMessage(`[{"script":"fixture only"}]`)),
		"unknown credentials section": withUnknown("credentials", json.RawMessage(`{"token":"fixture only"}`)),
		"duplicate version field":     bytes.Replace(before, []byte(`"version": 1`), []byte(`"version": 1, "version": 1`), 1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.Import(ctx, bytes.NewReader(data)); err == nil {
				t.Fatal("damaged backup was accepted")
			}
			if after := exportBytes(t, s); !bytes.Equal(before, after) {
				t.Fatal("rejected backup changed the existing database")
			}
		})
	}
}

func TestImportSQLiteWriteFailureRollsBackWholeReplacement(t *testing.T) {
	// A real SQLite failure after replacement starts restores the original data.
	ctx := context.Background()
	dest, _ := backupFixture(t)
	source, _ := backupFixture(t)
	createPlaylist(t, source, "New playlist to roll back")
	before := exportBytes(t, dest)
	incoming := exportBytes(t, source)
	// This trigger injects an engine error mid-transaction, not a SQL mock or an
	// assertion about query text. Public snapshots verify atomicity afterwards.
	if _, err := dest.db.ExecContext(ctx, `CREATE TRIGGER fixture_reject_import BEFORE INSERT ON favorites
		BEGIN SELECT RAISE(ABORT, 'fixture import failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := dest.Import(ctx, bytes.NewReader(incoming)); err == nil {
		t.Fatal("SQLite write failure was swallowed")
	}
	if after := exportBytes(t, dest); !bytes.Equal(before, after) {
		t.Fatal("failed transaction left a partial replacement")
	}
	if _, err := dest.db.ExecContext(ctx, "DROP TRIGGER fixture_reject_import"); err != nil {
		t.Fatal(err)
	}
	if err := dest.Import(ctx, bytes.NewReader(incoming)); err != nil {
		t.Fatalf("store not usable after rollback: %v", err)
	}
	if after := exportBytes(t, dest); !bytes.Equal(incoming, after) {
		t.Fatal("retry after rollback did not restore the complete backup")
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestBackupIOErrorsAndCancellationAreReported(t *testing.T) {
	// Broken readers/writers and canceled contexts report errors without data loss.
	ctx := context.Background()
	s, _ := backupFixture(t)
	before := exportBytes(t, s)
	failure := errors.New("fixture I/O failure")
	if err := s.Import(ctx, io.MultiReader(bytes.NewReader(before[:20]), failingReader{failure})); !errors.Is(err, failure) {
		t.Fatalf("import reader error = %v", err)
	}
	if err := s.Export(ctx, failingWriter{failure}); !errors.Is(err, failure) {
		t.Fatalf("export writer error = %v", err)
	}
	if err := s.Export(ctx, shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short export write = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Import(canceled, bytes.NewReader(before)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled import = %v", err)
	}
	if err := s.Export(canceled, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled export = %v", err)
	}
	if after := exportBytes(t, s); !bytes.Equal(before, after) {
		t.Fatal("I/O failure or cancellation changed the current library")
	}
}

type callbackReader struct {
	before func()
	r      io.Reader
}

func (r *callbackReader) Read(p []byte) (int, error) {
	if r.before != nil {
		fn := r.before
		r.before = nil
		fn()
	}
	return r.r.Read(p)
}

type callbackWriter struct {
	before func()
	bytes.Buffer
}

func (w *callbackWriter) Write(p []byte) (int, error) {
	if w.before != nil {
		fn := w.before
		w.before = nil
		fn()
	}
	return w.Buffer.Write(p)
}

func TestBackupExternalIODoesNotHoldDatabaseLifecycle(t *testing.T) {
	// Close can finish during external backup I/O, and an import then stays rejected.
	ctx := context.Background()
	s, _ := backupFixture(t)
	before := exportBytes(t, s)
	reader := &callbackReader{before: func() {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}, r: bytes.NewReader(before)}
	if err := s.Import(ctx, reader); !errors.Is(err, ErrClosed) {
		t.Fatalf("Import after reader closed store = %v", err)
	}
	exportStore, _ := backupFixture(t)
	want := exportBytes(t, exportStore)
	writer := &callbackWriter{before: func() {
		if err := exportStore.Close(); err != nil {
			t.Fatal(err)
		}
	}}
	if err := exportStore.Export(ctx, writer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, writer.Bytes()) {
		t.Fatal("closing during export corrupted its already acquired snapshot")
	}
}
