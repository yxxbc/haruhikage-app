package player

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/audio"
	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// A paused decoder may buffer, but history starts only once Play is asked
// for and actual decoded samples are available; resume does not record twice.
func TestStartedWaitsForPlayAndDecodedAudio(t *testing.T) {
	b, err := os.ReadFile("testdata/seek.flac")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(b) }))
	defer srv.Close()
	ch := make(chan func(), 128)
	p := New(func(fn func()) { ch <- fn })
	defer p.Close()
	p.Resolve = func(context.Context, domain.Track, string) (*domain.Stream, error) {
		return &domain.Stream{URL: srv.URL, SourceID: "test", SourceRevision: "1"}, nil
	}
	count := 0
	p.Started = func(domain.Track) { count++ }
	p.Cue([]domain.Track{track("1")})
	p.loadAt(0, false, 0)
	pumpUntil(t, ch, func() bool { return p.decoded })
	if count != 0 || p.Playing() {
		t.Fatal("paused prebuffer recorded a play")
	}
	p.Play()
	if count != 1 {
		t.Fatal("first decoded playback not recorded")
	}
	p.Pause()
	p.Play()
	if count != 1 {
		t.Fatal("resume recorded a second play")
	}
}

// MP3 and FLAC UI seeks enqueue on the fixed MyGo decoder worker even
// when the HTTP stream is unknown-length and the target has not arrived.
func TestSeekDoesNotWaitForNetworkOnUI(t *testing.T) {
	for _, codec := range []string{"mp3", "flac"} {
		t.Run(codec, func(t *testing.T) {
			b, err := os.ReadFile("testdata/seek." + codec)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.(http.Flusher).Flush()
				_, _ = w.Write(b[:len(b)/2])
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer srv.Close()
			ch := make(chan func(), 128)
			p := New(func(fn func()) { ch <- fn })
			defer p.Close()
			p.Resolve = func(context.Context, domain.Track, string) (*domain.Stream, error) {
				return &domain.Stream{URL: srv.URL, Size: -1}, nil
			}
			tr := track("1")
			tr.Duration = 5000
			p.PlayQueue([]domain.Track{tr}, 0)
			pumpUntil(t, ch, func() bool { return p.Ready() })
			// Make the paused branch exercise audio.Player.Seek immediately,
			// without merely testing the player's fade-delay timer.
			p.out.Pause()
			p.playing = false
			returned := make(chan struct{})
			go func() { p.Seek(4 * time.Second); close(returned) }()
			select {
			case <-returned:
			case <-time.After(2 * time.Second):
				t.Fatal("UI Seek waited for network")
			}
			p.SourceChanged()
			if p.Ready() || p.Playing() {
				t.Fatal("old seek survived source cancellation")
			}
		})
	}
}

// A seek wait can be cut off by its budget or cancellation without waiting
// for an unknown total file length. The test uses an already-expired budget.
func TestSeekWaitIsBoundedAndCancelable(t *testing.T) {
	d := downloaded(nil)
	d.done = false
	if _, err := d.waitUntil(1, time.Unix(1, 0)); !errors.Is(err, ErrSeekUnavailable) {
		t.Fatal("seek ignored its budget", err)
	}
	d.Close()
	if _, err := d.waitUntil(1, time.Time{}); !errors.Is(err, context.Canceled) {
		t.Fatal("seek ignored cancellation", err)
	}
	<-d.released
}

// A transport's premature EOF propagates even if the decoder itself would
// otherwise classify a partially received frame as a normal end of song.
func TestHTTPShortBodyIsNotSuccessfulEOF(t *testing.T) {
	b, err := os.ReadFile("testdata/seek.mp3")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		_, _ = w.Write(b[:len(b)/2])
		w.(http.Flusher).Flush()
	}))
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
		t.Fatal("short HTTP body became EOF")
	}
	if c.Size() != 0 {
		t.Fatal("short response committed")
	}
}

// Unsupported containers fail explicitly, rather than being guessed as MP3
// from the requested quality, filename or Content-Type.
func TestUnsupportedFormatIsNotGuessed(t *testing.T) {
	d := downloaded([]byte("\x00\x00\x00\x20ftypM4A this is not an MP3 stream"))
	defer d.Close()
	if s, err := openStream(d, 1000); err == nil {
		_ = s.Close()
		t.Fatal("unknown container opened as audio")
	}
}

// A decoder error cannot be mistaken for a normal queue end by MyGo's
// source layer, which intentionally collapses both into its OnEnd signal.
func TestObservedDecoderErrorAndNoProgress(t *testing.T) {
	for _, err := range []error{errors.New("decode failure"), nil} {
		failures, data := 0, 0
		s := &observedStream{stream: &errorStream{err: err}, onData: func() { data++ }, onError: func(error) { failures++ }}
		if _, got := s.Read(make([]float32, 16)); got == nil || got == io.EOF {
			t.Fatal("decoder failure became EOF")
		}
		_, _ = s.Read(make([]float32, 16))
		if failures != 1 || data != 0 || !s.failedFlag.Load() {
			t.Fatal("failed decoder emitted data/start or repeated error")
		}
	}
}

type errorStream struct{ err error }

func (s *errorStream) SampleRate() int                { return 44100 }
func (s *errorStream) Channels() int                  { return 2 }
func (s *errorStream) Len() int64                     { return -1 }
func (s *errorStream) Read([]float32) (int, error)    { return 0, s.err }
func (s *errorStream) SetPosition(int64) error        { return s.err }
func (s *errorStream) Close() error                   { return nil }
func (s *errorStream) source() (*download, *spectrum) { return nil, nil }

// RepeatOne restarts a finished output, including the already-paused branch
// of Seek; it must not leave the listener at the beginning in silence.
func TestRepeatOneRestartsOutput(t *testing.T) {
	p := New(func(func()) {})
	defer p.Close()
	p.Cue([]domain.Track{track("1")})
	p.out = audio.NewPlayer(silence{})
	p.playing = true
	p.repeat = RepeatOne
	p.ended()
	if !p.out.Playing() {
		t.Fatal("RepeatOne sought but did not restart")
	}
}

// Spectrum storage is a bounded window, even for maliciously large durations.
func TestSpectrumHasBoundedHistory(t *testing.T) {
	s := newSpectrum(44100, math.MaxInt64)
	if len(s.chunks) != spectrumChunks {
		t.Fatal("untrusted duration allocated whole-song spectrum")
	}
	s.acc = make([]float32, fftSize)
	s.analyze(0)
	if _, ok := s.at(0); !ok {
		t.Fatal("first spectrum missing")
	}
	s.analyze(spectrumChunks)
	if _, ok := s.at(0); ok {
		t.Fatal("overwritten spectrum returned for wrong position")
	}
	if _, ok := s.at(spectrumChunks * fftSize); !ok {
		t.Fatal("new spectrum missing")
	}
}
