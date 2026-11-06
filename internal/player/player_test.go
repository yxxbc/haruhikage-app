package player

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/audio"
	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// An empty queue leaves playback untouched.
func TestPlayEmptyQueue(t *testing.T) {
	p := &Player{at: -1}
	p.PlayQueue(nil, 0)
	if p.Current() != nil {
		t.Error("playing from an empty queue")
	}
}

type silence struct{}

func (silence) SampleRate() int               { return 48000 }
func (silence) Channels() int                 { return 2 }
func (silence) Len() int64                    { return 48000 * 600 }
func (silence) SetPosition(int64) error       { return nil }
func (silence) Read(b []float32) (int, error) { clear(b); return len(b), nil }

func track(id string) domain.Track {
	return domain.Track{Provider: "wy", ID: id, Duration: 600000, Playback: domain.PlaybackIdentity{ID: id}}
}
func posted() (chan func(), *Player) {
	ch := make(chan func(), 128)
	p := New(func(fn func()) { ch <- fn })
	p.Cue([]domain.Track{track("1")})
	out := audio.NewPlayer(silence{})
	out.SetVolume(0)
	out.Play()
	p.Attach(out)
	p.fade.attach(out, 1)
	return ch, p
}
func next(t *testing.T, ch chan func()) {
	t.Helper()
	select {
	case fn := <-ch:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("nothing posted")
	}
}
func pumpUntil(t *testing.T, ch chan func(), done func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for !done() {
		select {
		case fn := <-ch:
			fn()
		case <-deadline.C:
			t.Fatal("player did not reach expected state")
		}
	}
}

// Pausing fades out before music stops; playing again fades it in.
func TestPauseFades(t *testing.T) {
	ch, p := posted()
	defer p.Close()
	p.TogglePlay()
	if p.Playing() || !p.out.Playing() {
		t.Fatal("paused at once, or not at all")
	}
	next(t, ch)
	if p.out.Playing() || !p.fade.silent() {
		t.Fatal("fade did not finish silently")
	}
	p.TogglePlay()
	if !p.Playing() || !p.out.Playing() {
		t.Fatal("did not play again")
	}
	time.Sleep(fadeIn + 50*time.Millisecond)
	p.fade.mu.Lock()
	level := p.fade.level(time.Now())
	p.fade.mu.Unlock()
	if level != 1 {
		t.Errorf("faded in to %v", level)
	}
}

// A playing seek pauses around the jump and exposes its target meanwhile.
func TestSeekWithoutClick(t *testing.T) {
	ch, p := posted()
	defer p.Close()
	p.Seek(90 * time.Second)
	if p.out.Playing() {
		t.Error("jumped while playing")
	}
	if p.Position() != 90*time.Second {
		t.Errorf("position %v", p.Position())
	}
	next(t, ch)
	if !p.out.Playing() || p.out.Position() < 90*time.Second {
		t.Fatal("did not resume at target")
	}
}

// Repeated seeks coalesce to the last released progress-bar position.
func TestSeekTwicePlaysOn(t *testing.T) {
	ch, p := posted()
	defer p.Close()
	p.Seek(90 * time.Second)
	p.Seek(95 * time.Second)
	pumpUntil(t, ch, func() bool { return !p.seeking })
	if !p.out.Playing() || p.out.Position() < 95*time.Second {
		t.Fatal("did not resume at last target")
	}
}

// Missing loudness data is neutral, and known peaks limit normalization.
func TestNormGain(t *testing.T) {
	for _, c := range []struct {
		st   *domain.Stream
		want float64
	}{
		{nil, 1}, {&domain.Stream{}, 1}, {&domain.Stream{Gain: -6.6, Peak: 1}, math.Pow(10, -2.6/20)},
		{&domain.Stream{Gain: 3, Peak: 0.8}, 1.25}, {&domain.Stream{Gain: -4}, 1},
	} {
		if g := normGain(c.st); math.Abs(g-c.want) > 1e-9 {
			t.Errorf("%+v: %v, want %v", c.st, g, c.want)
		}
	}
}

// Queue occurrence identity survives shuffle/session even for duplicates.
func TestQueueEntriesAndOfflineRestore(t *testing.T) {
	p := New(func(func()) {})
	defer p.Close()
	tx := track("1")
	tx.Provider = "tx"
	p.Cue([]domain.Track{track("1"), track("1"), tx})
	entries := p.Entries()
	if entries[0].ID == entries[1].ID || entries[1].Track.Key() == entries[2].Track.Key() {
		t.Fatal("queue/platform identities collided")
	}
	id := p.CurrentEntryID()
	p.ToggleShuffle()
	if p.CurrentEntryID() != id {
		t.Fatal("shuffle changed current occurrence")
	}
	s, _ := p.Session()
	s.Position = 12345
	p.Resolve = func(context.Context, domain.Track, string) (*domain.Stream, error) {
		t.Error("Restore resolved audio")
		return nil, errors.New("offline")
	}
	p.LoadLyrics = func(context.Context, domain.Track) (domain.Lyrics, error) {
		t.Error("Restore loaded lyrics")
		return domain.Lyrics{}, nil
	}
	p.LoadArtwork = func(context.Context, domain.Track) ([]byte, error) {
		t.Error("Restore loaded artwork")
		return nil, nil
	}
	p.Restore(s)
	if p.Playing() || p.Loading() || p.Ready() || p.Position() != 12345*time.Millisecond {
		t.Fatal("restore did not remain offline/paused")
	}
	if p.CurrentEntryID() != id {
		t.Fatal("restore changed queue identity")
	}
	// Snapshot slices, including nested artist slices, are independent.
	s.Queue[0].Name = "mutated"
	s.Order[0] = 99
	if p.queue[0].Name == "mutated" || p.order[0] == 99 {
		t.Fatal("session aliases player state")
	}
}

// Invalid order permutations are rejected before replacing a good queue.
func TestRestoreRejectsInvalidOrder(t *testing.T) {
	p := New(func(func()) {})
	defer p.Close()
	p.Cue([]domain.Track{track("keep")})
	p.Restore(Session{Version: 2, Queue: []domain.Track{track("a"), track("b")}, Order: []int{0, 0}})
	if p.Current().ID != "keep" {
		t.Fatal("invalid session replaced queue")
	}
}

// Legacy numeric IDs migrate without floating point loss or account state.
func TestLegacySession(t *testing.T) {
	var s Session
	err := json.Unmarshal([]byte(`{"queue":[{"id":9007199254740993,"name":"song","dt":5000,"ar":[{"id":42,"name":"artist"}],"al":{"id":2,"name":"album"},"fee":1}],"order":[0],"at":0,"radio":"fm:DEFAULT"}`), &s)
	if err != nil {
		t.Fatal(err)
	}
	if s.Queue[0].ID != "9007199254740993" || s.Queue[0].Provider != "wy" || s.Queue[0].Playback.ID != s.Queue[0].ID || s.Queue[0].Artists[0].ID != "42" {
		t.Fatalf("bad migration: %+v", s.Queue)
	}
	b, _ := json.Marshal(s)
	var round Session
	if err := json.Unmarshal(b, &round); err != nil || round.Queue[0].Key() != s.Queue[0].Key() {
		t.Fatal("migration not round-trippable", err)
	}
}

// Volume changes compensate the installed curve, not newly edited settings.
func TestGainUsesActualDSP(t *testing.T) {
	p := New(func(func()) {})
	defer p.Close()
	p.Equalizer = func() bool { return true }
	preset := "bass"
	p.Preset = func() string { return preset }
	p.activeGains = append([]float64(nil), eqPresetByID("bass").gains...)
	preset = "flat"
	p.SetVolume(0.5)
	want := 0.25 * eqNowPlayingGain(eqPresetByID("bass").gains)
	if math.Abs(p.fade.gain-want) > 1e-12 {
		t.Fatal("compensation changed without changing filters")
	}
}

// Switching songs, sources and closing cancel the exact in-flight context.
func TestLoadCancellationAndClose(t *testing.T) {
	ch := make(chan func(), 128)
	p := New(func(fn func()) { ch <- fn })
	requests := make(chan context.Context, 4)
	p.Resolve = func(ctx context.Context, _ domain.Track, _ string) (*domain.Stream, error) {
		requests <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p.PlayQueue([]domain.Track{track("1"), track("2")}, 0)
	first := <-requests
	p.Next()
	second := <-requests
	select {
	case <-first.Done():
	default:
		t.Fatal("previous song still active")
	}
	p.SourceChanged()
	select {
	case <-second.Done():
	default:
		t.Fatal("previous source still active")
	}
	if p.Playing() || p.Loading() || p.Ready() {
		t.Fatal("source change did not unload")
	}
	p.Play()
	third := <-requests
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-third.Done():
	default:
		t.Fatal("Close did not cancel")
	}
	_ = p.Close()
	for len(ch) > 0 {
		(<-ch)()
	}
	if p.Playing() || p.Ready() || p.Loading() {
		t.Fatal("late callback revived closed player")
	}
}

// Automatic EQ cannot delay first output for the remainder of a download.
func TestAutoEQDoesNotBlockFirstPlay(t *testing.T) {
	b, err := os.ReadFile("testdata/seek.mp3")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush() // chunked, unknown length
		_, _ = w.Write(b[:len(b)/2])
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = w.Write(b[len(b)/2:])
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	ch := make(chan func(), 128)
	p := New(func(fn func()) { ch <- fn })
	defer p.Close()
	p.Equalizer = func() bool { return true }
	p.Preset = func() string { return "auto" }
	p.Resolve = func(context.Context, domain.Track, string) (*domain.Stream, error) {
		return &domain.Stream{URL: srv.URL, SourceID: "fixture", SourceRevision: "1", Size: -1}, nil
	}
	started := 0
	p.Started = func(domain.Track) { started++ }
	p.PlayQueue([]domain.Track{track("1")}, 0)
	pumpUntil(t, ch, func() bool { return p.Ready() })
	if !p.Playing() || p.autoGains != nil {
		t.Fatal("first play waited for analysis or borrowed another curve")
	}
	if _, known := p.Downloaded(); known {
		t.Fatal("unknown Content-Length reported as known")
	}
	close(release)
	pumpUntil(t, ch, func() bool { return started > 0 || p.Err() != "" })
	if started != 1 || p.Err() != "" {
		t.Fatalf("started %d, error %q", started, p.Err())
	}
}
