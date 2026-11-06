package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite"))
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close test store: %v", err)
		}
	})
	return s
}

func song(provider, id string) domain.Track {
	t := domain.Track{
		Provider: provider, ID: id, Name: provider + " song " + id,
		Artists:  []domain.Artist{{ID: "artist", Name: "Artist"}},
		Album:    domain.Album{ID: "album", Name: "Album", PicURL: "https://art.example/cover.jpg"},
		Duration: 183456, Alias: []string{"Alias"}, WebURL: "https://music.example/song/" + id,
	}
	switch provider {
	case "kg":
		t.Playback.Hash = id
	case "tx", "mg":
		t.Playback.Songmid = id
	default:
		t.Playback.ID = id
	}
	return t
}

func createPlaylist(t *testing.T, s *Store, name string) domain.Playlist {
	t.Helper()
	p, err := s.CreatePlaylist(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustAdd(t *testing.T, s *Store, id string, tracks ...domain.Track) {
	t.Helper()
	if err := s.AddTracks(context.Background(), id, tracks); err != nil {
		t.Fatal(err)
	}
}

func trackKeys(tracks []domain.Track) []string {
	keys := make([]string, len(tracks))
	for i, t := range tracks {
		keys[i] = t.Key()
	}
	return keys
}

func assertTrackOrder(t *testing.T, s *Store, id string, want ...domain.Track) {
	t.Helper()
	got, err := s.Tracks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(trackKeys(got), trackKeys(want)) {
		t.Fatalf("playlist order = %q, want %q", trackKeys(got), trackKeys(want))
	}
}

func exportBytes(t *testing.T, s *Store) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := s.Export(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReopenPersistsMetadataEntriesActivityAndState(t *testing.T) {
	// Closing and reopening a real file preserves the complete local library.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "资料库 #%.sqlite")
	s := openTestStore(t, path)
	p := createPlaylist(t, s, "  Evening  ")
	a, b := song("wy", "900719925474099312345"), song("tx", "same-mid")
	mustAdd(t, s, p.ID, a, b, a)
	if err := s.RenamePlaylist(ctx, p.ID, "夜间"); err != nil {
		t.Fatal(err)
	}
	if err := s.Favorite(ctx, b, true); err != nil {
		t.Fatal(err)
	}
	position := 13*time.Second + 123*time.Nanosecond
	if err := s.RecordPlay(ctx, a, position); err != nil {
		t.Fatal(err)
	}
	state := json.RawMessage(`{"version":1,"queue":[{"provider":"wy","id":"900719925474099312345"}],"position":13000000000,"paused":true}`)
	if err := s.SaveState(ctx, "playback.session", state); err != nil {
		t.Fatal(err)
	}
	before := exportBytes(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	if after := exportBytes(t, reopened); !bytes.Equal(before, after) {
		t.Fatal("reopen changed the stored library snapshot")
	}
	assertTrackOrder(t, reopened, p.ID, a, b, a)
	got, err := reopened.Tracks(ctx, p.ID)
	if err != nil || !reflect.DeepEqual(got[0], a) {
		t.Fatalf("song metadata round trip: tracks=%+v err=%v", got, err)
	}
	playlists, err := reopened.Playlists(ctx)
	if err != nil || len(playlists) != 1 || playlists[0].Name != "夜间" || playlists[0].Count != 3 {
		t.Fatalf("playlists = %+v, err=%v", playlists, err)
	}
	liked, err := reopened.IsFavorite(ctx, b.Ref())
	if err != nil || !liked {
		t.Fatalf("favorite = %v, err=%v", liked, err)
	}
	history, err := reopened.History(ctx, 10)
	if err != nil || len(history) != 1 || history[0].Position != position || history[0].Track.Key() != a.Key() {
		t.Fatalf("history = %+v, err=%v", history, err)
	}
	gotState, err := reopened.LoadState(ctx, "playback.session")
	if err != nil || !bytes.Equal(gotState, state) {
		t.Fatalf("state = %s, err=%v", gotState, err)
	}
}

func TestCompositeIdentitySeparatesPlatforms(t *testing.T) {
	// Equal platform IDs never alias across playlist, favorite or history writes.
	ctx := context.Background()
	s := testStore(t)
	p := createPlaylist(t, s, "Platforms")
	a, b := song("wy", "123"), song("tx", "123")
	mustAdd(t, s, p.ID, a, b)
	if err := s.Favorite(ctx, a, true); err != nil {
		t.Fatal(err)
	}
	if liked, err := s.IsFavorite(ctx, b.Ref()); err != nil || liked {
		t.Fatalf("other platform was liked: %v, %v", liked, err)
	}
	if err := s.Favorite(ctx, b, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Favorite(ctx, a, false); err != nil {
		t.Fatal(err)
	}
	favorites, err := s.Favorites(ctx)
	if err != nil || len(favorites) != 1 || !reflect.DeepEqual(favorites[0], b) {
		t.Fatalf("favorites = %+v, err=%v", favorites, err)
	}
	if err := s.RemoveTrack(ctx, p.ID, a.Ref()); err != nil {
		t.Fatal(err)
	}
	assertTrackOrder(t, s, p.ID, b)
	if err := s.RecordPlay(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlay(ctx, b, time.Second); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(ctx, 10)
	if err != nil || len(history) != 2 {
		t.Fatalf("history = %+v, err=%v", history, err)
	}
	seen := make(map[string]bool)
	for _, h := range history {
		seen[h.Track.Key()] = true
	}
	if !seen[a.Key()] || !seen[b.Key()] {
		t.Fatalf("history lost platform identity: %+v", history)
	}
	if err := s.DeletePlaylist(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tracks(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted playlist: %v", err)
	}
	if liked, err := s.IsFavorite(ctx, b.Ref()); err != nil || !liked {
		t.Fatalf("deleting playlist removed its favorite: %v, %v", liked, err)
	}
}

func TestCatalogPlaybackIdentityIsPreservedForEveryPlatform(t *testing.T) {
	// Platform IDs stay distinct from catalog-selected LX hash/songmid/id values.
	ctx := context.Background()
	s := testStore(t)
	p := createPlaylist(t, s, "LX identities")
	tracks := []domain.Track{song("wy", "42"), song("tx", "42"), song("kg", "42"), song("kw", "42"), song("mg", "42")}
	tracks[1].Playback.Songmid = "qq-mid-not-public-id"
	tracks[2].Playback.Hash = "KG-HASH-NOT-PUBLIC-ID"
	tracks[4].Playback.Songmid = "migu-song-id"
	mustAdd(t, s, p.ID, tracks...)
	dest := testStore(t)
	if err := dest.Import(ctx, bytes.NewReader(exportBytes(t, s))); err != nil {
		t.Fatal(err)
	}
	got, err := dest.Tracks(ctx, p.ID)
	if err != nil || !reflect.DeepEqual(got, tracks) {
		t.Fatalf("catalog identities were changed: got=%+v err=%v", got, err)
	}
}

func TestPlaylistEntryOrderSurvivesMovesRemovalAndAppend(t *testing.T) {
	// Editing repeated songs preserves occurrence IDs and explicit playlist order.
	ctx := context.Background()
	s := testStore(t)
	p := createPlaylist(t, s, "Order")
	a, b, c, d := song("wy", "a"), song("kg", "b"), song("mg", "c"), song("kw", "d")
	mustAdd(t, s, p.ID, a, b, a, c)
	entries, err := s.Entries(ctx, p.ID)
	if err != nil || len(entries) != 4 {
		t.Fatalf("entries = %+v, err=%v", entries, err)
	}
	if entries[0].ID == entries[2].ID {
		t.Fatal("repeated songs have the same occurrence ID")
	}
	if err := s.MoveEntry(ctx, p.ID, entries[3].ID, 1); err != nil {
		t.Fatal(err)
	}
	assertTrackOrder(t, s, p.ID, a, c, b, a)
	if err := s.MoveEntry(ctx, p.ID, entries[0].ID, 3); err != nil {
		t.Fatal(err)
	}
	assertTrackOrder(t, s, p.ID, c, b, a, a)
	if err := s.RemoveEntry(ctx, p.ID, entries[2].ID); err != nil {
		t.Fatal(err)
	}
	mustAdd(t, s, p.ID, d)
	assertTrackOrder(t, s, p.ID, c, b, a, d)
	after, err := s.Entries(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].ID != entries[3].ID || after[1].ID != entries[1].ID || after[2].ID != entries[0].ID {
		t.Fatalf("editing changed surviving entry IDs: before=%+v after=%+v", entries, after)
	}
	mustAdd(t, s, p.ID, a)
	if err := s.RemoveTrack(ctx, p.ID, a.Ref()); err != nil {
		t.Fatal(err)
	}
	assertTrackOrder(t, s, p.ID, c, b, d)
	if err := s.MoveEntry(ctx, p.ID, after[0].ID, 50); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out-of-range move: %v", err)
	}
	assertTrackOrder(t, s, p.ID, c, b, d)
}

func TestHistoryHasOnlyExplicitSuccessfulStartInputs(t *testing.T) {
	// Queue/state edits and failed inputs do not masquerade as Started events.
	ctx := context.Background()
	s := testStore(t)
	p := createPlaylist(t, s, "Not played")
	a := song("wy", "a")
	mustAdd(t, s, p.ID, a)
	if err := s.Favorite(ctx, a, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveState(ctx, "playback", json.RawMessage(`{"version":1,"queue":[{"provider":"wy","id":"a"}],"paused":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlay(ctx, a, -time.Second); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative position: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.RecordPlay(canceled, a, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start: %v", err)
	}
	history, err := s.History(ctx, 10)
	if err != nil || len(history) != 0 {
		t.Fatalf("history before Started = %+v, err=%v", history, err)
	}
	// This adapter models the composition root's Player.Started callback. No
	// player package is imported, keeping SQLite tests independent of audio/UI.
	started := func(track domain.Track) error { return s.RecordPlay(ctx, track, 2*time.Second+7) }
	if err := started(a); err != nil {
		t.Fatal(err)
	}
	if err := started(a); err != nil {
		t.Fatal(err)
	}
	history, err = s.History(ctx, 10)
	if err != nil || len(history) != 2 {
		t.Fatalf("history after Started = %+v, err=%v", history, err)
	}
	for _, h := range history {
		if h.Track.Key() != a.Key() || h.Position != 2*time.Second+7 || h.PlayedAt.IsZero() {
			t.Fatalf("incorrect start event: %+v", h)
		}
	}
	limited, err := s.History(ctx, 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("history limit = %+v, err=%v", limited, err)
	}
}

func TestInvalidBatchAndStateDoNotChangeSavedData(t *testing.T) {
	// A malformed song batch or JSON update leaves the prior snapshot untouched.
	ctx := context.Background()
	s := testStore(t)
	p := createPlaylist(t, s, "Atomic")
	a := song("wy", "a")
	mustAdd(t, s, p.ID, a)
	initial := json.RawMessage(`{"version":1,"paused":true,"position":123456789,"repeat":"all","shuffle":false}`)
	if err := s.SaveState(ctx, "session", initial); err != nil {
		t.Fatal(err)
	}
	before := exportBytes(t, s)
	invalid := song("wy", "")
	if err := s.AddTracks(ctx, p.ID, []domain.Track{song("tx", "new"), invalid}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid batch: %v", err)
	}
	for _, data := range []string{`{`, `{"position":1,"position":2}`, `{"session":{"headers":{"Authorization":"fixture"}}}`, `{"sourceScript":"fixture only"}`, `{"url":"https://stream.example/temporary"}`} {
		if err := s.SaveState(ctx, "session", json.RawMessage(data)); err == nil {
			t.Fatalf("accepted invalid or sensitive state %s", data)
		}
	}
	if err := s.SaveState(ctx, "sources.current", json.RawMessage(`{"id":"fixture"}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted a source state namespace: %v", err)
	}
	if after := exportBytes(t, s); !bytes.Equal(before, after) {
		t.Fatal("invalid input changed the saved library")
	}
	if _, err := s.LoadState(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing state: %v", err)
	}
	if err := s.SaveState(ctx, "optional", json.RawMessage(`null`)); err != nil {
		t.Fatal(err)
	}
	value, err := s.LoadState(ctx, "optional")
	if err != nil || string(value) != "null" {
		t.Fatalf("null state = %s, err=%v", value, err)
	}
	value[0] = 'x'
	value, err = s.LoadState(ctx, "optional")
	if err != nil || string(value) != "null" {
		t.Fatalf("mutating a returned RawMessage changed stored state: %s, %v", value, err)
	}
}

func TestScriptIdentityCannotBecomeSongProvider(t *testing.T) {
	// Source script IDs and ambiguous LX identities cannot enter stored tracks.
	s := testStore(t)
	p := createPlaylist(t, s, "Identity")
	for _, track := range []domain.Track{
		song("source_script_revision", "123"),
		{Provider: "kg", ID: "123", Playback: domain.PlaybackIdentity{ID: "123", Hash: "hash"}},
		{Provider: "tx", ID: "123", Playback: domain.PlaybackIdentity{ID: "123", Songmid: "mid"}},
	} {
		if err := s.AddTracks(context.Background(), p.ID, []domain.Track{track}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted a mixed provider/playback identity: %v", err)
		}
	}
	assertTrackOrder(t, s, p.ID)
}

func TestCanceledWaitAndConcurrentClose(t *testing.T) {
	// A waiting operation can cancel and Close safely drains an acquired operation.
	ctx := context.Background()
	s := testStore(t)
	release, err := s.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithCancel(ctx)
	waitResult := make(chan error, 1)
	go func() {
		_, err := s.Playlists(waitCtx)
		waitResult <- err
	}()
	cancel()
	if err := <-waitResult; !errors.Is(err, context.Canceled) {
		release()
		t.Fatalf("waiting cancellation = %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	<-s.closing
	if _, err := s.Playlists(ctx); !errors.Is(err, ErrClosed) {
		release()
		t.Fatalf("operation during Close = %v", err)
	}
	select {
	case err := <-closed:
		release()
		t.Fatalf("Close returned before the active operation drained: %v", err)
	default:
	}
	release()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}

func TestConcurrentWritesAndCloseRemainReopenable(t *testing.T) {
	// Concurrent writes racing with Close leave a valid, reopenable SQLite file.
	path := filepath.Join(t.TempDir(), "concurrent.sqlite")
	s := openTestStore(t, path)
	ctx := context.Background()
	const workers = 8
	var ready, done sync.WaitGroup
	ready.Add(workers)
	done.Add(workers)
	start := make(chan struct{})
	errorsOut := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer done.Done()
			p, err := s.CreatePlaylist(ctx, fmt.Sprintf("worker %d", i))
			ready.Done()
			if err != nil {
				errorsOut <- err
				return
			}
			<-start
			for j := 0; j < 20; j++ {
				err := s.AddTracks(ctx, p.ID, []domain.Track{song("wy", fmt.Sprintf("%d-%d", i, j))})
				if errors.Is(err, ErrClosed) {
					return
				}
				if err != nil {
					errorsOut <- err
					return
				}
			}
		}(i)
	}
	ready.Wait()
	close(start)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	done.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
	reopened := openTestStore(t, path)
	playlists, err := reopened.Playlists(ctx)
	if err != nil || len(playlists) != workers {
		t.Fatalf("committed playlists = %+v, err=%v", playlists, err)
	}
	_ = exportBytes(t, reopened)
}
