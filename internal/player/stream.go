package player

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/cmplx"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/audio"
	gomp3 "github.com/hajimehoshi/go-mp3"
	"github.com/yxxbc/haruhikage-app/internal/domain"
)

const (
	maxSpoolBytes      int64 = 512 << 20
	maxTotalSpoolBytes int64 = 1 << 30
	maxMetadataBytes   int64 = 16 << 20
	seekBudget               = 5 * time.Second
)

var (
	spooled            atomic.Int64
	audioHTTP          = &http.Client{Timeout: 2 * time.Minute}
	ErrSeekUnavailable = errors.New("player: seek target is not buffered")
)

// download has one primary owner, optional shared decoders and a writer.
// Only explicit in-memory fixtures use data. Network downloads use bounded
// disk staging; failure never promotes an entire song into RAM.
type download struct {
	mu        sync.Mutex
	cond      sync.Cond
	file      *os.File
	data      []byte
	n, size   int64
	done      bool
	err       error
	codec     string
	validated bool
	rejected  bool
	cancel    context.CancelFunc
	part      string
	keep      func(string, int64, string) bool
	onRelease func()
	users     int
	closed    bool
	reserved  int64
	released  chan struct{}
}

func startDownload(parent context.Context, st *domain.Stream, part *os.File) *download {
	ctx, cancel := context.WithCancel(parent)
	d := &download{size: -1, cancel: cancel, users: 2, released: make(chan struct{})}
	d.cond.L = &d.mu
	if part == nil {
		var err error
		part, err = os.CreateTemp("", "audio-*.part")
		if err != nil {
			cancel()
			return failedDownload(err)
		}
	}
	d.file, d.part = part, part.Name()
	go d.run(ctx, st)
	return d
}

func failedDownload(err error) *download {
	d := downloaded(nil)
	d.err, d.rejected = err, true
	return d
}

func downloaded(b []byte) *download {
	d := &download{data: b, n: int64(len(b)), size: int64(len(b)), done: true, cancel: func() {}, users: 1, released: make(chan struct{})}
	d.cond.L = &d.mu
	return d
}

func opened(f *os.File, size int64) *download {
	d := downloaded(nil)
	d.file, d.n, d.size = f, size, size
	return d
}

func (d *download) finish(err error) {
	d.mu.Lock()
	// A primary Close/cancellation cannot be overwritten by a late EOF.
	if !d.done {
		d.done, d.err = true, err
		if err == nil {
			d.size = d.n
		}
		if d.rejected && d.err == nil {
			d.err = errors.New("player: invalid audio stream")
		}
	}
	d.cond.Broadcast()
	d.mu.Unlock()
}

func (d *download) run(ctx context.Context, st *domain.Stream) {
	defer d.release()
	// Wake decoders immediately even if a transport takes time to return.
	stop := context.AfterFunc(ctx, func() { d.finish(ctx.Err()) })
	defer stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, st.URL, nil)
	if err != nil {
		d.finish(errors.New("player: invalid audio URL"))
		return
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		d.finish(errors.New("player: audio URL must be HTTP(S)"))
		return
	}
	req.Header = st.Headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Accept-Encoding", "identity")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Haruhikage/1.0")
	}
	res, err := audioHTTP.Do(req)
	if err != nil {
		// url.Error contains the signed URL; do not surface it in UI/logs.
		if ctx.Err() != nil {
			d.finish(ctx.Err())
		} else {
			d.finish(errors.New("player: audio request failed"))
		}
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		d.finish(fmt.Errorf("player: audio HTTP %d", res.StatusCode))
		return
	}
	expected := res.ContentLength
	if expected > maxSpoolBytes {
		d.finish(errors.New("player: audio exceeds staging limit"))
		return
	}
	d.mu.Lock()
	d.size = expected // -1 stays unknown, regardless of resolver guesses.
	d.mu.Unlock()
	buf := make([]byte, 64<<10)
	for {
		n, readErr := res.Body.Read(buf)
		if n > 0 {
			if err := d.write(buf[:n]); err != nil {
				d.finish(err)
				return
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				d.mu.Lock()
				got := d.n
				d.mu.Unlock()
				if expected >= 0 && got != expected {
					readErr = io.ErrUnexpectedEOF
				} else {
					readErr = nil
				}
			}
			d.finish(readErr)
			return
		}
		if n == 0 {
			d.finish(io.ErrNoProgress)
			return
		}
	}
}

func (d *download) write(b []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return context.Canceled
	}
	n := int64(len(b))
	if d.n+n > maxSpoolBytes {
		return errors.New("player: audio exceeds staging limit")
	}
	if spooled.Add(n) > maxTotalSpoolBytes {
		spooled.Add(-n)
		return errors.New("player: concurrent audio staging limit reached")
	}
	d.reserved += n
	got, err := d.file.Write(b)
	d.n += int64(got)
	d.cond.Broadcast()
	if err != nil {
		return err
	}
	if got != len(b) {
		return io.ErrShortWrite
	}
	return nil
}

func (d *download) Close() {
	d.cancel()
	d.mu.Lock()
	if !d.done {
		d.done, d.err = true, context.Canceled
	}
	d.cond.Broadcast()
	closed := d.closed
	d.closed = true
	d.mu.Unlock()
	if !closed {
		d.release()
	}
}

func (d *download) share() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.users <= 0 {
		return false
	}
	d.users++
	return true
}

// release closes the last reader first. Validation/rename/JSON/pruning
// happen outside mu, so decoding never waits on cache directory I/O.
func (d *download) release() {
	d.mu.Lock()
	d.users--
	if d.users > 0 {
		d.mu.Unlock()
		return
	}
	f, part, n, codec := d.file, d.part, d.n, d.codec
	keep, onRelease := d.keep, d.onRelease
	valid := d.done && d.err == nil && d.validated && !d.rejected
	reserved := d.reserved
	d.file, d.data, d.part, d.n = nil, nil, "", 0
	d.done = true
	if d.err == nil {
		d.err = context.Canceled
	}
	d.cond.Broadcast()
	d.mu.Unlock()
	go func() {
		if f != nil {
			_ = f.Close()
		}
		kept := false
		if part != "" && valid && keep != nil {
			kept = keep(part, n, codec)
		}
		if part != "" && !kept {
			_ = os.Remove(part)
		}
		spooled.Add(-reserved)
		if onRelease != nil {
			onRelease()
		}
		close(d.released)
	}()
}

func (d *download) Progress() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done && d.err == nil {
		return 1
	}
	if d.size <= 0 {
		return 0
	}
	return min(1, float64(d.n)/float64(d.size))
}

func (d *download) lengthKnown() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.size > 0 || d.done && d.err == nil
}

func (d *download) snapshot() (n, size int64, done bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n, d.size, d.done, d.err
}

func (d *download) reject(err error) {
	d.mu.Lock()
	d.rejected = true
	if d.err == nil {
		d.err = err
	}
	d.mu.Unlock()
}

// decoderEOF must not hide a transport failure behind a decoder's EOF.
func (d *download) decoderEOF() error {
	d.waitFor(math.MaxInt64)
	d.mu.Lock()
	err, codec, part, validated := d.err, d.codec, d.part, d.validated
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if !validated && codec == "mp3" && part != "" {
		if err := validateMP3(part); err != nil {
			d.reject(err)
			return err
		}
	}
	d.mu.Lock()
	d.validated = true
	d.mu.Unlock()
	return io.EOF
}

func (d *download) waitFor(n int64) int64 {
	got, _ := d.waitUntil(n, time.Time{})
	return got
}

func (d *download) waitUntil(n int64, deadline time.Time) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var timer *time.Timer
	if !deadline.IsZero() {
		timer = time.AfterFunc(time.Until(deadline), func() { d.mu.Lock(); d.cond.Broadcast(); d.mu.Unlock() })
		defer timer.Stop()
	}
	for d.n < n && !d.done {
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return d.n, ErrSeekUnavailable
		}
		d.cond.Wait()
	}
	if d.n < n && d.err != nil {
		return d.n, d.err
	}
	return d.n, nil
}

func (d *download) readAt(b []byte, off int64) (int, error) {
	n := min(int64(len(b)), d.n-off)
	if off < 0 || n <= 0 {
		return 0, nil
	}
	if d.file == nil {
		return copy(b, d.data[off:off+n]), nil
	}
	return d.file.ReadAt(b[:n], off)
}

func (d *download) peek(off int64, n int) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	b := make([]byte, n)
	got, _ := d.readAt(b, off)
	return b[:got]
}

type window struct {
	dl  *download
	off int64
	b   []byte
}

func (w *window) at(off int64, n int) []byte {
	if off < w.off || off+int64(n) > w.off+int64(len(w.b)) {
		w.off, w.b = off, w.dl.peek(off, max(n, 32<<10))
	}
	start := off - w.off
	if start >= int64(len(w.b)) {
		return nil
	}
	return w.b[start:min(int64(len(w.b)), start+int64(n))]
}

// No io.Seeker: go-mp3 must not scan the whole network stream on open.
type downloadReader struct {
	d        *download
	pos      int64
	deadline time.Time
}

func (r *downloadReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	d := r.d
	if _, err := d.waitUntil(r.pos+1, r.deadline); err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.n <= r.pos {
		if d.err != nil {
			return 0, d.err
		}
		return 0, io.EOF
	}
	n, err := d.readAt(p, r.pos)
	if n == 0 && err == nil {
		err = io.ErrUnexpectedEOF
	}
	r.pos += int64(n)
	return n, err
}

type stream interface {
	audio.Decoder
	Close() error
	source() (*download, *spectrum)
}

func openStream(dl *download, durationMs int64) (stream, error) {
	off := id3Size(dl)
	if off > maxMetadataBytes {
		return nil, errors.New("player: audio metadata exceeds limit")
	}
	if dl.waitFor(off+4) < off+4 {
		_, _, _, err := dl.snapshot()
		if err != nil {
			return nil, err
		}
		return nil, io.ErrUnexpectedEOF
	}
	head := dl.peek(off, 4)
	var s stream
	var err error
	codec := ""
	switch {
	case string(head) == "fLaC":
		codec = "flac"
		s, err = openFLACStream(dl)
	case frameSize(head) > 0:
		codec = "mp3"
		s, err = openMP3Stream(dl, durationMs)
	default:
		err = errors.New("player: unsupported audio format (MP3/FLAC required)")
	}
	if err == nil {
		dl.mu.Lock()
		dl.codec = codec
		dl.mu.Unlock()
	}
	return s, err
}

func openShared(dl *download, durationMs int64) (stream, error) {
	if !dl.share() {
		return nil, errors.New("player: the song is gone")
	}
	s, err := openStream(dl, durationMs)
	if err != nil {
		dl.release()
		return nil, err
	}
	switch s := s.(type) {
	case *mp3Stream:
		s.spec, s.drop = nil, dl.release
	case *flacStream:
		s.spec, s.drop = nil, dl.release
	}
	return s, nil
}

type mp3Stream struct {
	dl                 *download
	dec                *gomp3.Decoder
	rate               int
	frames, start, pos int64
	skip               int
	buf                []byte
	spec               *spectrum
	drop               func()
	closeMu            sync.Once
}

func openMP3Stream(dl *download, durationMs int64) (*mp3Stream, error) {
	s := &mp3Stream{dl: dl, drop: dl.Close}
	dec, err := gomp3.NewDecoder(&downloadReader{d: dl})
	if err != nil {
		return nil, err
	}
	s.dec, s.rate = dec, dec.SampleRate()
	dl.mu.Lock()
	dl.codec = "mp3"
	dl.mu.Unlock()
	// Untrusted catalog duration must not overflow frame arithmetic.
	if durationMs > 0 && durationMs <= int64((7*24*time.Hour)/time.Millisecond) {
		s.frames = durationMs * int64(s.rate) / 1000
	} else {
		s.frames = -1
	}
	s.start = id3Size(dl)
	s.spec = newSpectrum(s.rate, s.frames)
	return s, nil
}

func id3Size(dl *download) int64 {
	dl.waitFor(10)
	b := dl.peek(0, 10)
	if len(b) < 10 || string(b[:3]) != "ID3" {
		return 0
	}
	size := int64(b[6]&0x7f)<<21 | int64(b[7]&0x7f)<<14 | int64(b[8]&0x7f)<<7 | int64(b[9]&0x7f)
	if b[5]&0x10 != 0 {
		size += 10
	}
	return 10 + size
}

func (s *mp3Stream) SampleRate() int { return s.rate }
func (s *mp3Stream) Channels() int   { return 2 }
func (s *mp3Stream) Len() int64      { return s.frames }

func (s *mp3Stream) Read(dst []float32) (int, error) {
	n := len(dst) / 2 * 4
	if n == 0 {
		return 0, nil
	}
	if cap(s.buf) < n {
		s.buf = make([]byte, n)
	}
	b := s.buf[:n]
	for s.skip > 0 {
		got, err := io.ReadFull(s.dec, b[:min(n, s.skip)])
		s.skip -= got
		if err != nil {
			s.skip = 0
			return 0, s.terminal(err)
		}
	}
	got, err := io.ReadFull(s.dec, b)
	got -= got % 4
	samples := got / 2
	for i := range samples {
		dst[i] = float32(int16(binary.LittleEndian.Uint16(b[2*i:]))) / (1 << 15)
	}
	s.spec.feed(s.pos, dst[:samples])
	s.pos += int64(samples / 2)
	if err != nil {
		err = s.terminal(err)
	}
	return samples, err
}

func (s *mp3Stream) terminal(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return s.dl.decoderEOF()
	}
	s.dl.reject(err)
	return err
}

const preroll = 4 * 1152

// MyGo audio.Player.Seek enqueues this on its decoder goroutine. Scan
// compressed frame headers rather than estimating CBR offsets: this also
// works for VBR and unknown HTTP lengths. Every wait has one seek budget.
func (s *mp3Stream) SetPosition(target int64) error {
	target = max(target, 0)
	deadline := time.Now().Add(seekBudget)
	off, at := s.start, int64(0)
	from := max(target-preroll, 0)
	for at < from {
		if !time.Now().Before(deadline) {
			return ErrSeekUnavailable
		}
		if n, err := s.dl.waitUntil(off+4, deadline); err != nil {
			return err
		} else if n < off+4 {
			return io.ErrUnexpectedEOF
		}
		h := s.dl.peek(off, 4)
		size := frameSize(h)
		if size == 0 {
			return errors.New("player: invalid MP3 frame during seek")
		}
		frames := int64(1152)
		if (h[1]>>3)&3 != 3 {
			frames = 576
		}
		if at+frames > from {
			break
		}
		off += int64(size)
		at += frames
	}
	r := &downloadReader{d: s.dl, pos: off, deadline: deadline}
	dec, err := gomp3.NewDecoder(r)
	if err != nil {
		return err
	}
	r.deadline = time.Time{}
	s.dec, s.pos, s.skip = dec, target, int(target-at)*4
	return nil
}

func (s *mp3Stream) source() (*download, *spectrum) { return s.dl, s.spec }
func (s *mp3Stream) Close() error                   { s.closeMu.Do(s.drop); return nil }

func findFrame(dl *download, off int64) int64 {
	w := window{dl: dl}
	deadline := time.Now().Add(seekBudget)
	for tries := 0; tries < 64<<10; tries++ {
		if n, err := dl.waitUntil(off+4, deadline); err != nil || n < off+4 {
			return off
		}
		size := frameSize(w.at(off, 4))
		if size > 0 {
			_, _ = dl.waitUntil(off+int64(size)+4, deadline)
			next := w.at(off+int64(size), 4)
			if len(next) < 4 || frameSize(next) > 0 {
				return off
			}
		}
		off++
	}
	return off
}

var (
	bitratesV1 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	bitratesV2 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	rates      = [4]int{44100, 48000, 32000, 0}
)

func frameSize(b []byte) int {
	if len(b) < 4 || b[0] != 0xff || b[1]&0xe0 != 0xe0 {
		return 0
	}
	version, layer := (b[1]>>3)&3, (b[1]>>1)&3
	if version == 1 || layer != 1 {
		return 0
	}
	bi, ri := b[2]>>4, (b[2]>>2)&3
	if bi == 0 || bi == 15 || ri == 3 {
		return 0
	}
	pad, rate := int(b[2]>>1)&1, rates[ri]
	switch version {
	case 3:
		return 144*bitratesV1[bi]*1000/rate + pad
	case 2:
		return 72*bitratesV2[bi]*1000/(rate/2) + pad
	default:
		return 72*bitratesV2[bi]*1000/(rate/4) + pad
	}
}

// go-mp3 converts truncated MPEG frames into io.EOF. Validate their full
// byte boundaries before admitting a file to the persistent cache.
func validateMP3(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size, off := fi.Size(), int64(0)
	var h [10]byte
	if _, err = f.ReadAt(h[:], 0); err != nil {
		return err
	}
	if string(h[:3]) == "ID3" {
		off = 10 + int64(h[6]&127)<<21 + int64(h[7]&127)<<14 + int64(h[8]&127)<<7 + int64(h[9]&127)
		if h[5]&0x10 != 0 {
			off += 10
		}
	}
	frames := 0
	for off < size {
		remain := size - off
		if _, err = f.ReadAt(h[:min(int64(len(h)), remain)], off); err != nil {
			return err
		}
		if remain == 128 && string(h[:3]) == "TAG" {
			off = size
			break
		}
		n := frameSize(h[:min(int64(4), remain)])
		if n == 0 || int64(n) > remain {
			return io.ErrUnexpectedEOF
		}
		off += int64(n)
		frames++
	}
	if frames == 0 || off != size {
		return io.ErrUnexpectedEOF
	}
	return nil
}

const (
	fftSize        = 1024
	Bands          = 24
	spectrumChunks = 8192
)

type spectrum struct {
	mu     sync.RWMutex
	rate   int
	chunks [][Bands]float32
	have   []bool
	tags   []int64
	window [fftSize]float32
	acc    []float32
	accAt  int64
	edges  [Bands + 1]int
	fft    []complex128
}

// A bounded ring, independent of untrusted duration (about three minutes).
func newSpectrum(rate int, frames int64) *spectrum {
	s := &spectrum{rate: rate, chunks: make([][Bands]float32, spectrumChunks), have: make([]bool, spectrumChunks), tags: make([]int64, spectrumChunks), accAt: -1}
	for i := range s.window {
		s.window[i] = float32(0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fftSize-1)))
	}
	lo, hi := math.Log(40), math.Log(14000)
	for i := range s.edges {
		f := math.Exp(lo + (hi-lo)*float64(i)/Bands)
		s.edges[i] = max(1, min(fftSize/2-Bands+i-1, int(f*fftSize/float64(max(rate, 1)))))
	}
	for i := 1; i < len(s.edges); i++ {
		s.edges[i] = max(s.edges[i], s.edges[i-1]+1)
	}
	s.fft = make([]complex128, fftSize)
	return s
}

func (s *spectrum) feed(at int64, stereo []float32) {
	if s == nil {
		return
	}
	if s.accAt < 0 || at != s.accAt+int64(len(s.acc)) {
		s.acc = s.acc[:0]
		skip := (fftSize - at%fftSize) % fftSize
		if int(skip)*2 >= len(stereo) {
			s.accAt = -1
			return
		}
		stereo = stereo[skip*2:]
		at += skip
		s.accAt = at
	}
	for i := 0; i+1 < len(stereo); i += 2 {
		s.acc = append(s.acc, (stereo[i]+stereo[i+1])/2)
		if len(s.acc) == fftSize {
			s.analyze(s.accAt / fftSize)
			s.accAt += fftSize
			s.acc = s.acc[:0]
		}
	}
}

func (s *spectrum) analyze(chunk int64) {
	if chunk < 0 {
		return
	}
	for i, v := range s.acc {
		s.fft[i] = complex(float64(v*s.window[i]), 0)
	}
	fft(s.fft)
	var bands [Bands]float32
	for b := range Bands {
		var sum float64
		for k := s.edges[b]; k < s.edges[b+1]; k++ {
			sum += cmplx.Abs(s.fft[k])
		}
		mag := sum / float64(s.edges[b+1]-s.edges[b]) / (fftSize / 4)
		mag *= 1 + float64(b)/6
		db := 20 * math.Log10(mag+1e-9)
		bands[b] = float32(math.Max(0, math.Min(1, (db+60)/52)))
	}
	i := chunk % int64(len(s.chunks))
	s.mu.Lock()
	s.chunks[i], s.have[i], s.tags[i] = bands, true, chunk
	s.mu.Unlock()
}

func (s *spectrum) at(frame int64) ([Bands]float32, bool) {
	if s == nil || frame < 0 {
		return [Bands]float32{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	chunk := frame / fftSize
	i := chunk % int64(len(s.chunks))
	if !s.have[i] || s.tags[i] != chunk {
		return [Bands]float32{}, false
	}
	return s.chunks[i], true
}

func fft(x []complex128) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			for k := range size / 2 {
				a, b := x[start+k], x[start+k+size/2]*wk
				x[start+k], x[start+k+size/2] = a+b, a-b
				wk *= w
			}
		}
	}
}
