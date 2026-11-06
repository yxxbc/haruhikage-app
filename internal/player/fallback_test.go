package player

import (
	"context"
	"errors"
	"testing"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// A source owns quality negotiation: failures never trigger hidden retries
// or the former platform-specific sequence of lower-quality requests.
func TestResolveDoesNotFallback(t *testing.T) {
	unavailable := errors.New("unsupported rendition")
	for _, quality := range []string{"320k", "flac24bit", "atmos_plus"} {
		calls := 0
		o := loadOptions{quality: quality, resolve: func(ctx context.Context, tr domain.Track, q string) (*domain.Stream, error) {
			calls++
			if q != quality || tr.Provider != "wy" || tr.ID != "1" {
				t.Fatal("resolution identity/quality changed")
			}
			return nil, unavailable
		}}
		_, _, _, err := open(context.Background(), track("1"), o, func() {})
		if calls != 1 || !errors.Is(err, unavailable) {
			t.Fatalf("%s: %d calls, %v", quality, calls, err)
		}
	}
}

// Default quality and all mutable preference closures are captured before
// launching a worker; changing preferences cannot mutate that snapshot.
func TestLoadOptionsSnapshot(t *testing.T) {
	p := New(func(func()) {})
	defer p.Close()
	if p.options().quality != "320k" {
		t.Fatal("default is not the LX quality")
	}
	g := []float64{6, 5, 3, 1, 0, -1, -1, 0, 1, 2}
	calls := 0
	p.Equalizer = func() bool { calls++; return true }
	p.Preset = func() string { calls++; return "bass" }
	p.EQ = func() []float64 { calls++; return g }
	o := p.options()
	captured := calls
	g[0] = 12
	if o.gains[0] != 6 || calls != captured {
		t.Fatal("snapshot shares mutable EQ data")
	}
	p.Quality = func() string { return "flac24bit" }
	if o.quality != "320k" || p.options().quality != "flac24bit" {
		t.Fatal("quality snapshot changed")
	}
}

// Resolution errors leave a retryable failed state and never emit Started.
func TestFailedLoadDoesNotRecordStart(t *testing.T) {
	ch := make(chan func(), 16)
	p := New(func(fn func()) { ch <- fn })
	defer p.Close()
	p.Resolve = func(context.Context, domain.Track, string) (*domain.Stream, error) { return nil, errors.New("offline") }
	started := 0
	p.Started = func(domain.Track) { started++ }
	p.PlayQueue([]domain.Track{track("1")}, 0)
	pumpUntil(t, ch, func() bool { return p.State() == Failed })
	if p.Playing() || p.Loading() || p.Ready() || p.Err() == "" || started != 0 {
		t.Fatal("failed load looked successful")
	}
}
