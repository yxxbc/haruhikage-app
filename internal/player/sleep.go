package player

import "time"

// stopper is a timer that can be stopped, as time.AfterFunc's is.
type stopper interface{ Stop() bool }

// sleepAfter and sleepNow are the clock the sleep timer reads, which tests
// swap for one they hold.
var (
	sleepAfter = func(d time.Duration, fn func()) stopper { return time.AfterFunc(d, fn) }
	sleepNow   = time.Now
)

// sleep ends playback after a while, or when the track playing ends.
type sleep struct {
	timer stopper
	// atEnd stops playback when the current track ends, rather than at a
	// deadline: the listener may pick another song before it does.
	atEnd bool
	// until is when the timer ends playback, zero with no timer.
	until time.Time
}

// SetSleep ends playback after d, replacing any sleep set. A d of zero or
// less cancels it, as CancelSleep does.
func (p *Player) SetSleep(d time.Duration) {
	p.CancelSleep()
	if d <= 0 {
		return
	}
	p.sleep.until = sleepNow().Add(d)
	p.sleep.timer = sleepAfter(d, func() {
		p.post(func() {
			// Nothing is left of it: the menu shows it gone, and the next
			// track plays as it would.
			p.sleep.until, p.sleep.timer = time.Time{}, nil
			p.Pause()
			if p.Changed != nil {
				p.Changed()
			}
		})	})
}

// SetSleepAtEnd stops playback when the track playing ends, rather than
// playing the next.
func (p *Player) SetSleepAtEnd() {
	p.CancelSleep()
	p.sleep.atEnd = true
}

// CancelSleep ends a sleep timer set, if any.
func (p *Player) CancelSleep() {
	if p.sleep.timer != nil {
		p.sleep.timer.Stop()
		p.sleep.timer = nil
	}
	p.sleep.atEnd = false
	p.sleep.until = time.Time{}
}

// Sleeping reports the sleep timer set: whether playback ends with the
// track playing, and how many minutes the timer has left, 0 for none.
func (p *Player) Sleeping() (atEnd bool, minutes int) {
	if p.sleep.atEnd {
		return true, 0
	}
	if p.sleep.until.IsZero() {
		return false, 0
	}
	// Rounded up: a timer with 20 seconds to go still reads as a minute,
	// rather than as none at all.
	left := p.sleep.until.Sub(sleepNow())
	if left <= 0 {
		return false, 0
	}
	return false, int((left + time.Minute - 1) / time.Minute)
}
