package player

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"
	"github.com/yxxbc/haruhikage-app/internal/domain"
	"github.com/yxxbc/haruhikage-app/pkg/lyrics"
)

type RepeatMode int

const (
	RepeatOff RepeatMode = iota
	RepeatAll
	RepeatOne
)

type State string

const (
	Idle      State = "idle"
	Resolving State = "resolving"
	Buffering State = "buffering"
	Playing   State = "playing"
	Paused    State = "paused"
	Seeking   State = "seeking"
	Failed    State = "failed"
)

// QueueEntryID identifies an occurrence, NOT a provider track. Duplicate
// tracks in one queue have distinct IDs, retained across shuffle/restore.
type QueueEntryID string
type QueueEntry struct {
	ID    QueueEntryID `json:"entryId"`
	Track domain.Track `json:"track"`
}

var entrySequence atomic.Uint64

func newEntryID() QueueEntryID {
	return QueueEntryID(time.Now().UTC().Format("20060102T150405.000000000") + "-" + base36(entrySequence.Add(1)))
}
func base36(n uint64) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var b [13]byte
	i := len(b)
	for {
		i--
		b[i] = digits[n%36]
		n /= 36
		if n == 0 {
			break
		}
	}
	return string(b[i:])
}

// Player's public methods and callbacks belong to the main thread. post
// must enqueue there; worker closures only read immutable load snapshots.
type Player struct {
	post        func(func())
	Resolve     func(context.Context, domain.Track, string) (*domain.Stream, error)
	LoadLyrics  func(context.Context, domain.Track) (domain.Lyrics, error)
	LoadArtwork func(context.Context, domain.Track) ([]byte, error)
	Started     func(domain.Track)
	Quality     func() string
	Normalize   func() bool
	Cache       *Cache
	// SourceIdentity snapshots the active script ID/revision on the main
	// thread. Set it to enable offline cache lookup before resolving.
	SourceIdentity func() (id, revision string)
	Equalizer      func() bool
	Preset         func() string
	EQ             func() []float64
	Changed        func()

	queue            []domain.Track
	entryIDs         []QueueEntryID
	order            []int
	at               int
	shuffle          bool
	repeat           RepeatMode
	volume           float64
	track            *domain.Track
	out              *audio.Player
	stream           stream
	info             *domain.Stream
	fade             fader
	gain             float64
	activeGains      []float64
	activeNormalize  bool
	autoGains        []float64
	analyses         map[string][]float64
	loading, playing bool
	err              string
	state            State
	gen              int
	cancel           context.CancelFunc
	closed           bool
	mediaOff         func()
	lyrics           []lyrics.Line
	lyricsLoaded     bool
	sleep            sleep
	pending          time.Duration
	seeking          bool
	seekTo           time.Duration
	seekGen          int
	seekTimer        *time.Timer
	started          bool
	decoded          bool
	// A result owns its decoder until accepted or canceled, even when
	// the application has stopped draining its main-thread post queue.
	result *loadResult
}

type loadOptions struct {
	quality                  string
	sourceID, sourceRevision string
	normalize                bool
	gains                    []float64
	auto                     bool
	analyses                 map[string][]float64
	cache                    *Cache
	resolve                  func(context.Context, domain.Track, string) (*domain.Stream, error)
	lyrics                   func(context.Context, domain.Track) (domain.Lyrics, error)
	artwork                  func(context.Context, domain.Track) ([]byte, error)
}

func (p *Player) options() loadOptions {
	o := loadOptions{quality: "320k", cache: p.Cache, resolve: p.Resolve, lyrics: p.LoadLyrics, artwork: p.LoadArtwork}
	if p.Quality != nil {
		if q := p.Quality(); q != "" {
			o.quality = q
		}
	}
	if p.SourceIdentity != nil {
		o.sourceID, o.sourceRevision = p.SourceIdentity()
	}
	if p.Normalize != nil {
		o.normalize = p.Normalize()
	}
	if p.EQOn() {
		preset := p.EQPreset()
		o.auto = preset == "auto"
		if !o.auto {
			o.gains = append([]float64(nil), eqPresetByID(preset).gains...)
		}
		if p.EQ != nil {
			if g := p.EQ(); len(g) == len(eqFreqs) {
				o.gains, o.auto = clampGains(g), false
			}
		}
	}
	o.analyses = make(map[string][]float64, len(p.analyses))
	for k, g := range p.analyses {
		o.analyses[k] = append([]float64(nil), g...)
	}
	return o
}

func New(post func(func())) *Player {
	p := &Player{post: post, at: -1, volume: 0.8, gain: 1, state: Idle}
	audio.SetLatency(audio.LatencyPlayback)
	p.mediaOff = mygo.NowPlaying.OnCommand(func(cmd mygo.MediaCommand, pos time.Duration) {
		p.post(func() {
			if p.closed {
				return
			}
			switch cmd {
			case mygo.MediaPlay:
				p.Play()
			case mygo.MediaPause:
				p.Pause()
			case mygo.MediaNext:
				p.Next()
			case mygo.MediaPrevious:
				p.Prev()
			case mygo.MediaSeek:
				p.Seek(pos)
			}
		})
	})
	return p
}

func (p *Player) sync() {
	if p.closed {
		return
	}
	if p.Changed != nil {
		defer p.Changed()
	}
	if p.track == nil {
		mygo.NowPlaying.Clear()
		return
	}
	state := mygo.PlaybackPaused
	if p.playing {
		state = mygo.PlaybackPlaying
	}
	mygo.NowPlaying.SetPlayback(state, p.Position())
}

func (p *Player) announce(t *domain.Track, artwork []byte) {
	state := mygo.PlaybackPaused
	if p.playing {
		state = mygo.PlaybackPlaying
	}
	mygo.NowPlaying.Set(mygo.NowPlayingInfo{Title: t.Name, Artist: t.ArtistNames(), Album: t.Album.Name, Artwork: artwork, Duration: p.Duration(), Position: p.Position(), State: state, Commands: mygo.MediaPlay | mygo.MediaPause | mygo.MediaNext | mygo.MediaPrevious | mygo.MediaSeek})
}

func (p *Player) ended() {
	if p.closed {
		return
	}
	if p.sleep.atEnd {
		p.CancelSleep()
		p.playing, p.state = false, Paused
		if p.out != nil {
			p.out.Pause()
		}
		p.sync()
		return
	}
	if p.repeat == RepeatOne && p.out != nil {
		p.Seek(0)
		if p.playing {
			p.out.Play()
		}
		return
	}
	if p.at+1 >= len(p.order) && p.repeat == RepeatOff {
		p.playing, p.state = false, Paused
		p.sync()
		return
	}
	p.Next()
}

func cloneTrack(t domain.Track) domain.Track {
	t.Artists = append([]domain.Artist(nil), t.Artists...)
	t.Alias = append([]string(nil), t.Alias...)
	return t
}
func cloneTracks(tracks []domain.Track) []domain.Track {
	out := make([]domain.Track, len(tracks))
	for i, t := range tracks {
		out[i] = cloneTrack(t)
	}
	return out
}

func (p *Player) setQueue(tracks []domain.Track) {
	p.queue = cloneTracks(tracks)
	p.order = make([]int, len(tracks))
	p.entryIDs = make([]QueueEntryID, len(tracks))
	for i := range tracks {
		p.order[i], p.entryIDs[i] = i, newEntryID()
	}
}

func (p *Player) PlayQueue(tracks []domain.Track, start int) {
	if p.closed || len(tracks) == 0 {
		return
	}
	start = min(max(start, 0), len(tracks)-1)
	p.setQueue(tracks)
	if p.shuffle {
		p.shuffleOrder(start)
		p.load(0)
	} else {
		p.load(start)
	}
}

func (p *Player) Entries() []QueueEntry {
	out := make([]QueueEntry, len(p.queue))
	for i, t := range p.queue {
		out[i] = QueueEntry{p.entryIDs[i], cloneTrack(t)}
	}
	return out
}
func (p *Player) CurrentEntryID() QueueEntryID {
	if p.at < 0 || p.at >= len(p.order) {
		return ""
	}
	return p.entryIDs[p.order[p.at]]
}
func (p *Player) shuffleOrder(first int) {
	rand.Shuffle(len(p.order), func(i, j int) { p.order[i], p.order[j] = p.order[j], p.order[i] })
	for i, v := range p.order {
		if v == first {
			p.order[0], p.order[i] = p.order[i], p.order[0]
			break
		}
	}
}
func (p *Player) ToggleShuffle() {
	if p.closed {
		return
	}
	p.shuffle = !p.shuffle
	if len(p.order) == 0 {
		return
	}
	cur := p.order[max(p.at, 0)]
	if p.shuffle {
		p.shuffleOrder(cur)
		p.at = 0
	} else {
		for i := range p.order {
			p.order[i] = i
		}
		p.at = cur
	}
	p.sync()
}
func (p *Player) CycleRepeat() {
	if !p.closed {
		p.repeat = (p.repeat + 1) % 3
		p.sync()
	}
}
func (p *Player) Current() *domain.Track  { return p.track }
func (p *Player) Track() *domain.Track    { return p.track }
func (p *Player) Queue() []domain.Track   { return p.queue }
func (p *Player) PlayOrder() ([]int, int) { return p.order, p.at }
func (p *Player) Playing() bool           { return p.playing }
func (p *Player) Loading() bool           { return p.loading }
func (p *Player) Ready() bool             { return p.out != nil }
func (p *Player) Err() string             { return p.err }
func (p *Player) State() State {
	if p.state == "" {
		return Idle
	}
	return p.state
}
func (p *Player) Stream() *domain.Stream        { return p.info }
func (p *Player) Info() *domain.Stream          { return p.info }
func (p *Player) Shuffle() bool                 { return p.shuffle }
func (p *Player) Repeat() RepeatMode            { return p.repeat }
func (p *Player) Lyrics() ([]lyrics.Line, bool) { return p.lyrics, p.lyricsLoaded }
func (p *Player) Downloaded() (float64, bool) {
	if p.stream == nil {
		return 0, false
	}
	dl, _ := p.stream.source()
	return dl.Progress(), dl.lengthKnown()
}

type Audio interface {
	SampleRate() int
	Channels() int
	Read([]float32) (int, error)
	SetPosition(int64) error
	Close() error
}

func (p *Player) OpenAudio() (Audio, error) {
	if p.closed || p.stream == nil || p.track == nil {
		return nil, errors.New("player: nothing loaded")
	}
	dl, _ := p.stream.source()
	return openShared(dl, p.track.Duration)
}
func (p *Player) Next() {
	if p.closed || len(p.order) == 0 {
		return
	}
	p.load((p.at + 1) % len(p.order))
}
func (p *Player) Prev() {
	if p.closed || len(p.order) == 0 {
		return
	}
	at := p.at - 1
	if at < 0 {
		at = len(p.order) - 1
	}
	p.load(at)
}
func (p *Player) Jump(i int) {
	if p.closed {
		return
	}
	for k, v := range p.order {
		if v == i {
			p.load(k)
			return
		}
	}
}
func (p *Player) Play() {
	if !p.playing {
		p.TogglePlay()
	}
}
func (p *Player) Pause() {
	if p.playing {
		p.TogglePlay()
	}
}
func (p *Player) TogglePlay() {
	if p.closed {
		return
	}
	if p.out == nil {
		if p.loading {
			p.playing = !p.playing
			p.sync()
		} else if len(p.order) > 0 {
			p.loadAt(max(p.at, 0), true, p.pending)
		}
		return
	}
	out := p.out
	if p.playing {
		p.playing, p.state = false, Paused
		p.fade.fade(0, fadeOut, func() {
			p.post(func() {
				if !p.closed && p.out == out && !p.playing {
					out.Pause()
					p.sync()
				}
			})
		})
		p.sync()
		return
	}
	p.playing, p.state = true, Playing
	if !p.seeking && !out.Playing() {
		if d := out.Duration(); d > 0 && out.Position() >= d-time.Second {
			out.Seek(0)
		}
		out.Play()
	}
	p.fade.fade(1, fadeIn, nil)
	p.notifyStarted()
	p.sync()
}

const seekGap = 30 * time.Millisecond

// The pinned MyGo audio/audio.go Seek calls stream.seek, which only
// enqueues. SetPosition and all network waits run in audio/source.go's
// decoder goroutine, not under the UI or mixer lock.
func (p *Player) Seek(t time.Duration) {
	if p.closed {
		return
	}
	t = max(t, 0)
	if d := p.Duration(); d > 0 {
		t = min(t, d)
	}
	if p.out == nil {
		p.pending = t
		return
	}
	out := p.out
	p.seekGen++
	gen := p.seekGen
	if p.seekTimer != nil {
		p.seekTimer.Stop()
	}
	if !out.Playing() && !p.seeking {
		out.Seek(t)
		p.sync()
		return
	}
	p.seeking, p.seekTo, p.state = true, t, Seeking
	out.Pause()
	p.seekTimer = time.AfterFunc(seekGap, func() {
		p.post(func() {
			if p.closed || gen != p.seekGen || p.out != out {
				return
			}
			p.seeking, p.seekTimer = false, nil
			out.Seek(t)
			if p.playing || !p.fade.silent() {
				out.Play()
			}
			if p.playing {
				p.state = Playing
			} else {
				p.state = Paused
			}
			p.sync()
		})
	})
	p.sync()
}

func (p *Player) SetVolume(v float64) {
	if math.IsNaN(v) {
		v = 0
	}
	p.volume = min(1, max(0, v))
	p.applyGain()
}
func (p *Player) applyGain() {
	g := p.volume * p.volume
	if p.activeNormalize {
		g *= p.gain
	}
	// Compensation follows the filters actually installed, not the next
	// track's newly edited preferences.
	g *= eqNowPlayingGain(p.activeGains)
	p.fade.setGain(g)
}

const normPreamp = 4

func normGain(st *domain.Stream) float64 {
	if st == nil || st.Gain == 0 && st.Peak == 0 || math.IsNaN(st.Gain) || math.IsInf(st.Gain, 0) || math.IsNaN(st.Peak) || math.IsInf(st.Peak, 0) {
		return 1
	}
	g := math.Pow(10, (st.Gain+normPreamp)/20)
	if st.Peak > 0 {
		g = min(g, 1/st.Peak)
	}
	return g
}
func (p *Player) Position() time.Duration {
	if p.out == nil {
		return p.pending
	}
	if p.seeking {
		return p.seekTo
	}
	return p.out.Position()
}
func (p *Player) Duration() time.Duration {
	if p.out != nil {
		if d := p.out.Duration(); d > 0 {
			return d
		}
	}
	if p.track == nil {
		return 0
	}
	return max(0, p.track.Length())
}
func (p *Player) Levels() ([Bands]float32, bool) {
	if p.stream == nil || p.out == nil || !p.playing {
		return [Bands]float32{}, false
	}
	_, spec := p.stream.source()
	return spec.at(int64(p.Position().Seconds() * float64(p.stream.SampleRate())))
}

func (p *Player) stop() {
	p.gen++
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	if p.result != nil {
		p.result.close()
		p.result = nil
	}
	p.seekGen++
	if p.seekTimer != nil {
		p.seekTimer.Stop()
		p.seekTimer = nil
	}
	if out := p.out; out != nil {
		out.Pause()
		// The mixer ramps a pause; leave its already buffered tail intact.
		time.AfterFunc(100*time.Millisecond, out.Close)
	}
	p.out, p.stream, p.info = nil, nil, nil
	p.seeking, p.decoded, p.started = false, false, false
	p.activeGains, p.autoGains = nil, nil
	p.fade.attach(nil, 1)
}
func (p *Player) load(at int) { p.loadAt(at, true, 0) }

type loadResult struct {
	mu     sync.Mutex
	s      stream
	closed bool
}

func (r *loadResult) put(s stream) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		_ = s.Close()
		return false
	}
	r.s = s
	return true
}
func (r *loadResult) close() {
	r.mu.Lock()
	s := r.s
	r.s = nil
	r.closed = true
	r.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

func (p *Player) loadAt(at int, play bool, from time.Duration) {
	if p.closed || at < 0 || at >= len(p.order) {
		return
	}
	p.stop()
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	gen := p.gen
	o := p.options() // All preference/application closures run HERE.
	p.at = at
	t := cloneTrack(p.queue[p.order[at]])
	p.track = &t
	p.loading, p.playing, p.err, p.state = true, play, "", Resolving
	p.lyrics, p.lyricsLoaded = nil, o.lyrics == nil
	p.pending = max(from, 0)
	p.announce(&t, nil)
	p.sync()
	post := p.post
	if o.artwork != nil {
		go func() {
			b, err := o.artwork(ctx, t)
			if err == nil && ctx.Err() == nil {
				post(func() {
					if !p.closed && gen == p.gen {
						p.announce(&t, b)
					}
				})
			}
		}()
	}
	if o.lyrics != nil {
		go func() {
			l, err := o.lyrics(ctx, t)
			var lines []lyrics.Line
			if err == nil && ctx.Err() == nil {
				lines = lyrics.Build(l.LRC, l.Translation, l.YRC, l.Romaji)
			}
			if ctx.Err() == nil {
				post(func() {
					if !p.closed && gen == p.gen {
						p.lyrics, p.lyricsLoaded = lines, true
						p.sync()
					}
				})
			}
		}()
	}
	r := &loadResult{}
	p.result = r
	context.AfterFunc(ctx, r.close)
	go func() {
		info, s, key, err := open(ctx, t, o, func() {
			post(func() {
				if !p.closed && gen == p.gen {
					p.state = Buffering
					p.sync()
				}
			})
		})
		if err != nil {
			if ctx.Err() == nil {
				post(func() {
					if !p.closed && gen == p.gen {
						p.fail(err)
					}
				})
			}
			return
		}
		if !r.put(s) {
			return
		}
		// Automatic analysis is optional and off the first-play path. Only
		// a previously measured rendition is used for this load.
		gains := o.gains
		if o.auto {
			gains = o.analyses[key]
		}
		post(func() {
			if p.closed || gen != p.gen || ctx.Err() != nil {
				r.close()
				return
			}
			p.loading = false
			p.stream, p.info = s, info
			p.activeNormalize, p.gain = o.normalize, normGain(info)
			p.autoGains = append([]float64(nil), gains...)
			filtered := stream(s)
			if len(gains) > 0 {
				filtered = newEQStream(s, gains, eqFreqs)
			}
			if filtered != s {
				p.activeGains = append([]float64(nil), gains...)
			}
			monitored := &observedStream{stream: filtered, onData: func() {
				post(func() {
					if !p.closed && gen == p.gen {
						p.decoded = true
						p.notifyStarted()
					}
				})
			}, onError: func(err error) {
				post(func() {
					if !p.closed && gen == p.gen {
						p.fail(err)
					}
				})
			}}
			p.out = audio.NewPlayer(monitored)
			level := 1.0
			if p.pending > 0 {
				level = 0
			}
			p.fade.attach(p.out, level)
			p.applyGain()
			p.out.OnEnd(func() {
				post(func() {
					if !p.closed && gen == p.gen && p.state != Failed && !monitored.failedFlag.Load() {
						p.ended()
					}
				})
			})
			if p.pending > 0 {
				p.out.Seek(p.pending)
				p.pending = 0
			}
			if p.playing {
				p.out.Play()
				p.fade.fade(1, fadeIn, nil)
				p.state = Playing
			} else {
				p.state = Paused
			}
			if o.auto && len(o.analyses[key]) == 0 {
				p.analyze(ctx, s, t, key, gen)
			}
			p.sync()
		})
	}()
}

// open uses a captured script identity for offline cache lookup when one
// is available. Otherwise Resolve supplies it, once, without quality fallback.
func open(ctx context.Context, t domain.Track, o loadOptions, buffering func()) (*domain.Stream, stream, string, error) {
	if o.sourceID != "" && o.sourceRevision != "" {
		identity := &domain.Stream{SourceID: o.sourceID, SourceRevision: o.sourceRevision}
		if dl, info, ok := o.cache.lookup(t, identity, o.quality); ok {
			buffering()
			s, err := openStream(dl, t.Duration)
			if err != nil {
				dl.reject(err)
				dl.Close()
				return nil, nil, "", err
			}
			return info, s, cacheKey(t, identity, o.quality), nil
		}
	}
	if o.resolve == nil {
		return nil, nil, "", errors.New("player: no music source configured")
	}
	st, err := o.resolve(ctx, t, o.quality)
	if err != nil {
		return nil, nil, "", err
	}
	if err = ctx.Err(); err != nil {
		return nil, nil, "", err
	}
	if st == nil || st.URL == "" {
		return nil, nil, "", errors.New("player: source returned no audio URL")
	}
	info := *st
	info.Headers = st.Headers.Clone()
	if o.sourceID != "" && (info.SourceID != o.sourceID || info.SourceRevision != o.sourceRevision) {
		return nil, nil, "", errors.New("player: active source changed during resolution")
	}
	key := cacheKey(t, &info, o.quality)
	buffering()
	dl, cached, ok := o.cache.lookup(t, &info, o.quality)
	if ok {
		info = *cached
	} else {
		dl = o.cache.download(ctx, t, &info, o.quality)
	}
	s, err := openStream(dl, t.Duration)
	if err != nil {
		dl.reject(err)
		dl.Close()
		return nil, nil, key, err
	}
	dl.mu.Lock()
	info.Type, info.Size = dl.codec, dl.size
	dl.mu.Unlock()
	return &info, s, key, nil
}

// observedStream bridges MyGo's decoder error/EOF conflation back to the
// player. A non-EOF error must neither advance the queue nor record a play.
type observedStream struct {
	stream
	onData              func()
	onError             func(error)
	dataOnce, errorOnce sync.Once
	failedFlag          atomic.Bool
}

func (s *observedStream) Read(b []float32) (int, error) {
	n, err := s.stream.Read(b)
	if err != nil && err != io.EOF {
		s.failed(err)
	} else if n > 0 {
		s.dataOnce.Do(s.onData)
	}
	if n == 0 && err == nil {
		err = io.ErrNoProgress
		s.failed(err)
	}
	return n, err
}
func (s *observedStream) SetPosition(frame int64) error {
	err := s.stream.SetPosition(frame)
	if err != nil {
		s.failed(err)
	}
	return err
}
func (s *observedStream) failed(err error) {
	s.failedFlag.Store(true)
	dl, _ := s.source()
	if dl != nil {
		dl.reject(err)
	}
	s.errorOnce.Do(func() { s.onError(err) })
}
func (p *Player) notifyStarted() {
	if p.started || !p.decoded || !p.playing || p.out == nil || p.state == Failed {
		return
	}
	p.started = true
	if p.Started != nil && p.track != nil {
		p.Started(cloneTrack(*p.track))
	}
}
func (p *Player) fail(err error) {
	pos := p.Position()
	p.stop()
	p.pending = pos
	p.loading, p.playing, p.state, p.err = false, false, Failed, err.Error()
	p.sync()
}
func (p *Player) analyze(ctx context.Context, s stream, t domain.Track, key string, gen int) {
	dl, _ := s.source()
	if !dl.share() {
		return
	}
	post := p.post
	go func() {
		defer dl.release()
		dl.waitFor(math.MaxInt64)
		if ctx.Err() != nil {
			return
		}
		g := analyzeAutoEQ(dl, t.Duration)
		if len(g) == 0 || ctx.Err() != nil {
			return
		}
		post(func() {
			if p.closed || ctx.Err() != nil || gen != p.gen {
				return
			}
			if p.analyses == nil {
				p.analyses = make(map[string][]float64)
			}
			if len(p.analyses) >= 32 {
				for k := range p.analyses {
					delete(p.analyses, k)
					break
				}
			}
			p.analyses[key] = append([]float64(nil), g...)
		})
	}()
}

// SourceChanged cancels every old-source job and unloads the output. The
// selected occurrence/position survive, paused; Play uses the new resolver.
func (p *Player) SourceChanged() {
	if p.closed {
		return
	}
	pos := p.Position()
	p.stop()
	p.pending = pos
	p.loading, p.playing = false, false
	p.lyrics, p.lyricsLoaded = nil, false
	p.err = ""
	if p.track != nil {
		p.state = Paused
	} else {
		p.state = Idle
	}
	p.sync()
}

func (p *Player) Close() error {
	if p.closed {
		return nil
	}
	out := p.out
	p.closed = true
	p.CancelSleep()
	p.stop()
	if out != nil {
		out.Close()
	}
	p.loading, p.playing, p.state = false, false, Idle
	if p.mediaOff != nil {
		p.mediaOff()
		p.mediaOff = nil
	}
	mygo.NowPlaying.Clear()
	return nil
}

type Session struct {
	Version  int            `json:"version"`
	Queue    []domain.Track `json:"queue"`
	EntryIDs []QueueEntryID `json:"entryIds,omitempty"`
	Order    []int          `json:"order"`
	At       int            `json:"at"`
	Shuffle  bool           `json:"shuffle"`
	Repeat   RepeatMode     `json:"repeat"`
	Position int64          `json:"position"`
}

func (p *Player) Session() (Session, bool) {
	if p.track == nil || p.at < 0 {
		return Session{}, false
	}
	return Session{Version: 2, Queue: cloneTracks(p.queue), EntryIDs: append([]QueueEntryID(nil), p.entryIDs...), Order: append([]int(nil), p.order...), At: p.at, Shuffle: p.shuffle, Repeat: p.repeat, Position: p.Position().Milliseconds()}, true
}

// Restore is entirely offline: not even artwork/lyrics callbacks run.
// The selected track is only resolved when the listener explicitly plays.
func (p *Player) Restore(s Session) {
	n := len(s.Queue)
	if p.closed || s.Version > 2 || n == 0 || len(s.Order) != n || s.At < 0 || s.At >= n {
		return
	}
	seen := make([]bool, n)
	for _, i := range s.Order {
		if i < 0 || i >= n || seen[i] {
			return
		}
		seen[i] = true
	}
	for _, t := range s.Queue {
		if !t.Ref().Valid() {
			return
		}
	}
	p.stop()
	p.setQueue(s.Queue)
	if len(s.EntryIDs) == n {
		ids := make(map[QueueEntryID]bool, n)
		valid := true
		for _, id := range s.EntryIDs {
			if id == "" || ids[id] {
				valid = false
				break
			}
			ids[id] = true
		}
		if valid {
			p.entryIDs = append([]QueueEntryID(nil), s.EntryIDs...)
		}
	}
	p.order = append([]int(nil), s.Order...)
	p.at, p.shuffle = s.At, s.Shuffle
	p.repeat = s.Repeat
	if p.repeat < RepeatOff || p.repeat > RepeatOne {
		p.repeat = RepeatOff
	}
	t := cloneTrack(p.queue[p.order[p.at]])
	p.track = &t
	p.pending = max(0, time.Duration(min(s.Position, int64((7*24*time.Hour)/time.Millisecond)))*time.Millisecond)
	if d := p.Duration(); d > 0 {
		p.pending = min(p.pending, d)
	}
	p.loading, p.playing, p.state, p.err = false, false, Paused, ""
	p.lyrics, p.lyricsLoaded = nil, false
	p.announce(&t, nil)
	p.sync()
}
