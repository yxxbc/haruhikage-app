package player

import (
	"testing"
	"time"
)

// fakeClock holds the sleep timer's clock and its timers, so a test can
// move the minute hand and fire one without waiting.
type fakeClock struct {
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	clock *fakeClock
	d     time.Duration
	fn    func()
	dead  bool
}

func (t *fakeTimer) Stop() bool {
	if t.dead {
		return false
	}
	t.dead = true
	return true
}

// advance moves the clock forward and fires the timers due, as the real
// afterFunc would.
func (c *fakeClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
	for _, t := range c.timers {
		if !t.dead && t.d <= d {
			t.dead = true
			t.fn()
		}
	}
}

func (c *fakeClock) install(t *testing.T) {
	oldAfter, oldNow := sleepAfter, sleepNow
	t.Cleanup(func() { sleepAfter, sleepNow = oldAfter, oldNow })
	sleepNow = func() time.Time { return c.now }
	sleepAfter = func(d time.Duration, fn func()) stopper {
		ft := &fakeTimer{clock: c, d: d, fn: fn}
		c.timers = append(c.timers, ft)
		return ft
	}
}

// A sleep timer set for a while pauses when it is up, and reads as the
// minutes left until then; a new one replaces it, and it can be cancelled.
func TestSleepTimer(t *testing.T) {
	c := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	c.install(t)
	p := New(func(f func()) { f() })

	if atEnd, left := p.Sleeping(); atEnd || left != 0 {
		t.Fatalf("nothing set reads as %v, %d", atEnd, left)
	}
	p.SetSleep(30 * time.Minute)
	if atEnd, left := p.Sleeping(); atEnd || left != 30 {
		t.Fatalf("30 minutes reads as %v, %d", atEnd, left)
	}
	// 20 seconds to go is still the minute it is in.
	c.advance(29*time.Minute + 40*time.Second)
	if _, left := p.Sleeping(); left != 1 {
		t.Fatalf("%d minutes left, want 1", left)
	}

	// Replaced: the first one is stopped, so only the second ends it.
	p.SetSleep(60 * time.Minute)
	if _, left := p.Sleeping(); left != 60 {
		t.Fatalf("replaced reads as %d minutes", left)
	}
	c.advance(60 * time.Minute)
	if atEnd, left := p.Sleeping(); atEnd || left != 0 {
		t.Fatalf("after the timer: %v, %d", atEnd, left)
	}

	p.SetSleep(15 * time.Minute)
	p.CancelSleep()
	if atEnd, left := p.Sleeping(); atEnd || left != 0 {
		t.Fatalf("cancelled reads as %v, %d", atEnd, left)
	}
}

// An already queued timer callback cannot pause or clear its replacement.
// Stopping the timer alone used to leave this main-thread callback alive.
func TestSleepQueuedCallbackEpoch(t *testing.T) {
	c := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	c.install(t)
	var posts []func()
	p := New(func(fn func()) { posts = append(posts, fn) })
	defer p.Close()
	p.Hold(0)
	p.SetSleep(time.Minute)
	c.advance(time.Minute)
	if len(posts) != 1 {
		t.Fatal("timer did not enqueue")
	}
	p.SetSleep(30 * time.Minute)
	posts[0]()
	if _, left := p.Sleeping(); left != 30 || !p.Playing() {
		t.Fatal("old callback replaced the new sleep or paused playback")
	}
	p.CancelSleep()
	posts[0]()
	if !p.Playing() {
		t.Fatal("canceled callback paused playback")
	}
}

// A sleep set to stop with the track playing does, whatever the repeat
// mode, and is gone once it has: the next track plays as it would.
func TestSleepAtEnd(t *testing.T) {
	p := New(func(f func()) { f() })
	p.repeat = RepeatAll
	p.order = []int{0, 1}
	p.at = 0
	p.sleep.atEnd = true

	p.ended()
	if p.playing != false {
		t.Error("still playing after the last track")
	}
	if p.at != 0 {
		t.Errorf("at %d: went on to the next track", p.at)
	}
	if atEnd, _ := p.Sleeping(); atEnd {
		t.Error("the sleep is still set")
	}
}
