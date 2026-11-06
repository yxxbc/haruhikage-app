package player

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math"
	"sync"
	"time"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

// flacStream decodes a FLAC download for package audio: the lossless and
// Hi-Res streams. Its frames stand alone, borrowing nothing from those
// before, so a seek lands on the very sample: decoding from the frame that
// holds it, found by its header near where the bytes say it is.
type flacStream struct {
	dl    *download
	info  *meta.StreamInfo
	start int64 // the first frame's byte, after the metadata
	r     *bufio.Reader
	pos   int64     // the frame Read returns next
	skip  int64     // frames to drop before it, after a seek
	pend  []float32 // decoded, and not yet read: stereo
	spec  *spectrum
	drop  func() // lets the download go, as the stream closes
	once  sync.Once
}

func openFLACStream(dl *download) (*flacStream, error) {
	start, err := flacDataStart(dl)
	if err != nil {
		return nil, err
	}
	from := id3Size(dl)
	st, err := flac.New(bytes.NewReader(dl.peek(from, int(start-from))))
	if err != nil {
		return nil, err
	}
	s := &flacStream{dl: dl, info: st.Info, start: start, drop: dl.Close}
	if s.info == nil || s.info.NChannels == 0 || s.info.SampleRate == 0 || s.info.BitsPerSample == 0 || s.info.BitsPerSample > 32 {
		return nil, errors.New("flac: no stream info")
	}
	dl.mu.Lock()
	dl.codec = "flac"
	dl.mu.Unlock()
	s.spec = newSpectrum(int(s.info.SampleRate), int64(s.info.NSamples))
	s.r = bufio.NewReaderSize(&downloadReader{d: dl, pos: start}, 64<<10)
	return s, nil
}

// flacDataStart returns where a FLAC file's first frame starts, after its
// ID3 tag, if any, and its metadata blocks.
func flacDataStart(dl *download) (int64, error) {
	off := id3Size(dl)
	if dl.waitFor(off+4) < off+4 || string(dl.peek(off, 4)) != "fLaC" {
		return 0, errors.New("flac: not a FLAC file")
	}
	off += 4
	for {
		if dl.waitFor(off+4) < off+4 {
			return 0, io.ErrUnexpectedEOF
		}
		h := dl.peek(off, 4)
		if len(h) < 4 {
			return 0, io.ErrUnexpectedEOF
		}
		last := h[0]&0x80 != 0
		off += 4 + (int64(h[1])<<16 | int64(h[2])<<8 | int64(h[3]))
		if off > maxMetadataBytes {
			return 0, errors.New("flac: metadata exceeds limit")
		}
		if last {
			if dl.waitFor(off) < off {
				return 0, io.ErrUnexpectedEOF
			}
			return off, nil
		}
	}
}

func (s *flacStream) SampleRate() int { return int(s.info.SampleRate) }
func (s *flacStream) Channels() int   { return 2 }

func (s *flacStream) Len() int64 {
	if s.info.NSamples == 0 {
		return -1
	}
	return int64(s.info.NSamples)
}

// parse parses the frame r is at: its header, then its samples, taking
// what the header leaves to the stream info from there.
func (s *flacStream) parse(r io.Reader) (f *frame.Frame, err error) {
	// The dependency may panic on corrupt subframe/header combinations.
	// Treat malformed media as a decoder failure, not a process crash.
	defer func() {
		if recover() != nil {
			f, err = nil, errors.New("flac: corrupt frame")
		}
	}()
	f, err = frame.New(r)
	if err != nil {
		return nil, err
	}
	if f.BitsPerSample == 0 {
		f.BitsPerSample = s.info.BitsPerSample
	}
	if f.SampleRate == 0 {
		f.SampleRate = s.info.SampleRate
	}
	if err := f.Parse(); err != nil {
		return nil, err
	}
	return f, nil
}

func (s *flacStream) Read(dst []float32) (int, error) {
	for len(s.pend) == 0 {
		f, err := s.parse(s.r)
		if err != nil {
			if err == io.EOF && (s.info.NSamples == 0 || s.pos >= int64(s.info.NSamples)) {
				err = s.dl.decoderEOF()
			} else {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				s.dl.reject(err)
			}
			return 0, err
		}
		s.decode(f)
	}
	n := copy(dst[:len(dst)/2*2], s.pend)
	s.pend = s.pend[n:]
	s.spec.feed(s.pos, dst[:n])
	s.pos += int64(n / 2)
	return n, nil
}

// decode adds a frame's samples to those pending, in stereo, but those to
// skip.
func (s *flacStream) decode(f *frame.Frame) {
	n := int64(f.BlockSize)
	from := min(s.skip, n)
	s.skip -= from
	if from == n {
		return
	}
	left, right := f.Subframes[0].Samples, f.Subframes[0].Samples
	if len(f.Subframes) > 1 {
		right = f.Subframes[1].Samples
	}
	scale := float32(1 / math.Exp2(float64(f.BitsPerSample-1)))
	if cap(s.pend) < int(n)*2 {
		s.pend = make([]float32, 0, n*2)
	}
	out := s.pend[:0]
	for i := from; i < n; i++ {
		out = append(out, float32(left[i])*scale, float32(right[i])*scale)
	}
	s.pend = out
}

// SetPosition goes to frame: to the frame of FLAC holding it, then drops
// what comes before it there. It finds that frame by the secant method,
// guessing where in the bytes it is from the frames found on either side,
// until one is found less than a second before it.
func (s *flacStream) SetPosition(target int64) error {
	target = max(target, 0)
	deadline := time.Now().Add(seekBudget)
	total := int64(s.info.NSamples)
	if total > 0 {
		target = min(target, total)
	}
	_, size, done, downloadErr := s.dl.snapshot()
	if downloadErr != nil {
		return downloadErr
	}
	if !done || size <= 0 || total <= 0 {
		// Unknown/partial length cannot be used as a byte/time ratio.
		// Scan actual frames on MyGo's decode worker, within one budget.
		raw := &downloadReader{d: s.dl, pos: s.start, deadline: deadline}
		r := bufio.NewReaderSize(raw, 64<<10)
		at := int64(0)
		for {
			if !time.Now().Before(deadline) {
				return ErrSeekUnavailable
			}
			f, err := s.parse(r)
			if err != nil {
				return err
			}
			n := int64(f.BlockSize)
			if at+n > target {
				s.pos, s.skip, s.pend = target, target-at, s.pend[:0]
				s.decode(f)
				raw.deadline = time.Time{}
				s.r = r
				return nil
			}
			at += n
			if at == target {
				raw.deadline = time.Time{}
				s.r, s.pos, s.skip, s.pend = r, target, 0, s.pend[:0]
				return nil
			}
		}
	}
	loOff, lo := s.start, int64(0)
	hiOff, hi := size, total
	for tries := 0; tries < 8 && target-lo > int64(s.info.SampleRate) && hi > lo; tries++ {
		guess := loOff + int64(float64(hiOff-loOff)*float64(target-lo)/float64(hi-lo))
		if !time.Now().Before(deadline) {
			return ErrSeekUnavailable
		}
		off, at, ok := s.frameAfter(max(guess, loOff+1), hiOff, deadline)
		if !ok {
			break
		}
		if at > target {
			hiOff, hi = off, at
		} else {
			loOff, lo = off, at
		}
	}
	s.r = bufio.NewReaderSize(&downloadReader{d: s.dl, pos: loOff}, 64<<10)
	s.pos, s.skip, s.pend = target, target-lo, s.pend[:0]
	return nil
}

// frameAfter returns where the first whole frame at or after off and
// before end starts, and the frame of sound it starts with. Bytes that
// only look like a frame's header within the audio fail its checksums.
func (s *flacStream) frameAfter(off, end int64, deadline time.Time) (int64, int64, bool) {
	w := window{dl: s.dl}
	for ; off+2 < end; off++ {
		if !time.Now().Before(deadline) {
			return 0, 0, false
		}
		if n, err := s.dl.waitUntil(off+2, deadline); err != nil || n < off+2 {
			return 0, 0, false
		}
		b := w.at(off, 2)
		if len(b) < 2 {
			return 0, 0, false
		}
		if b[0] != 0xff || b[1]&0xfe != 0xf8 {
			continue
		}
		if at, ok := s.frameAt(off, deadline); ok {
			return off, at, true
		}
	}
	return 0, 0, false
}

// frameAt returns the frame of sound the frame of FLAC at off starts with,
// if a whole one is there. The decoder may panic on bytes that only
// passed the header's checksum.
func (s *flacStream) frameAt(off int64, deadline time.Time) (at int64, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	f, err := s.parse(bufio.NewReaderSize(&downloadReader{d: s.dl, pos: off, deadline: deadline}, 16<<10))
	if err != nil || f.Channels.Count() != int(s.info.NChannels) {
		return 0, false
	}
	return int64(f.SampleNumber()), true
}

func (s *flacStream) Close() error {
	s.once.Do(s.drop)
	return nil
}

func (s *flacStream) source() (*download, *spectrum) { return s.dl, s.spec }
