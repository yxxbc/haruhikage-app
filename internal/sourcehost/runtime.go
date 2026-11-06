// Package sourcehost implements the LX musicUrl ABI in an isolated Goja loop.
// Only loop() touches JavaScript values. HTTP/timer goroutines enqueue plain Go
// data; the sole exception is Goja's documented thread-safe Interrupt on shutdown.
package sourcehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/yxxbc/haruhikage-app/internal/domain"
)

const (
	taskTimeout = 12 * time.Second
	turnTimeout = 2 * time.Second
	maxPending  = 64
	maxTimers   = 1024
	maxHTTP     = 64
)

type resolution struct {
	ctx     context.Context
	track   domain.Track
	quality string
	promise *goja.Promise // loop-owned
	reply   chan resolveResult
}
type resolveResult struct {
	url string
	err error
}
type timerEntry struct {
	timer    *time.Timer
	fn       goja.Callable
	args     []goja.Value
	interval time.Duration
}

// Runtime is safe for concurrent Resolve and Close. Its JS runtime is never
// exposed. A canceled consumer doesn't abort HTTP shared with other consumers.
// If the last consumer cancels, the runtime is retired, including its JS caches,
// timers and HTTP. A new worker is needed; no stranded pending Promise is reused.
type Runtime struct {
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	events    chan func() error
	client    *http.Client
	ownClient bool
	caps      Capabilities // immutable after New returns

	// Everything below is confined to loop().
	vm          *goja.Runtime
	handler     goja.Callable
	inited      bool
	pending     map[*resolution]struct{}
	timers      map[int]*timerEntry
	nextTimer   int
	httpCancels map[int]context.CancelFunc
	nextHTTP    int
}

func New(ctx context.Context, script string, client *http.Client) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(script) == 0 || len(script) > MaxScriptBytes || !utf8.ValidString(script) {
		return nil, ErrInit
	}
	ownClient := client == nil
	if client == nil {
		client = &http.Client{Transport: publicTransport(), Timeout: 10 * time.Second}
	}
	// Copy, rather than mutate, an injected client. Restrict every redirect too.
	copyClient := *client
	originalRedirect := client.CheckRedirect
	copyClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !ValidURL(req.URL.String()) || len(via) >= 5 {
			return errors.New("source redirect denied")
		}
		// Don't forward credentials to a different origin on redirects.
		if len(via) > 0 && !sameOrigin(req.URL, via[0].URL) {
			req.Header = make(http.Header)
		}
		if originalRedirect != nil {
			return originalRedirect(req, via)
		}
		return nil
	}
	root, cancel := context.WithCancel(context.Background())
	r := &Runtime{ctx: root, cancel: cancel, done: make(chan struct{}), events: make(chan func() error, 256), client: &copyClient, ownClient: ownClient}
	ready := make(chan error, 1)
	go r.loop(script, ready)
	initCtx, stop := context.WithTimeout(ctx, taskTimeout)
	defer stop()
	select {
	case err := <-ready:
		if err != nil {
			r.Close()
			return nil, err
		}
		if err = initCtx.Err(); err != nil {
			r.Close()
			return nil, err
		}
		return r, nil
	case <-initCtx.Done():
		r.Close()
		return nil, initCtx.Err()
	}
}

func (r *Runtime) Capabilities() Capabilities { return CloneCapabilities(r.caps) }
func (r *Runtime) Done() <-chan struct{}      { return r.done }
func (r *Runtime) Close() error               { r.cancel(); <-r.done; return nil }

func (r *Runtime) post(fn func() error) bool {
	select {
	case <-r.ctx.Done():
		return false
	default:
	}
	select {
	case r.events <- fn:
		return true
	case <-r.ctx.Done():
		return false
	}
}

func (r *Runtime) loop(script string, ready chan<- error) {
	r.vm = goja.New()
	r.pending = make(map[*resolution]struct{})
	r.timers = make(map[int]*timerEntry)
	r.httpCancels = make(map[int]context.CancelFunc)
	interrupted := make(chan struct{})
	stopInterrupt := context.AfterFunc(r.ctx, func() { r.vm.Interrupt(ErrClosed); close(interrupted) })
	initialized := false
	defer func() {
		r.cancel()
		if !stopInterrupt() {
			<-interrupted
		}
		for _, t := range r.timers {
			t.timer.Stop()
		}
		for _, cancel := range r.httpCancels {
			cancel()
		}
		if r.ownClient {
			r.client.CloseIdleConnections()
		}
		for p := range r.pending {
			p.reply <- resolveResult{err: ErrClosed}
		}
		if !initialized {
			ready <- ErrInit
		}
		close(r.done)
	}()
	if err := r.turn(func() error { r.install(); _, err := r.vm.RunString(script); return err }); err != nil {
		return
	}
	for {
		if !initialized && r.inited {
			if r.handler == nil || ValidateCapabilities(r.caps) != nil {
				return
			}
			initialized = true
			ready <- nil
		}
		select {
		case <-r.ctx.Done():
			return
		case fn := <-r.events:
			if err := r.turn(fn); err != nil {
				return
			}
		}
	}
}

func (r *Runtime) turn(fn func() error) error {
	// This watchdog also covers infinite loops inside HTTP/timer callbacks and
	// Promise continuations. Interrupt is never cleared/reused after a timeout.
	watchdog := time.AfterFunc(turnTimeout, r.cancel)
	defer watchdog.Stop()
	err := fn()
	if err == nil {
		r.settle()
	}
	return err
}

func (r *Runtime) install() {
	vm := r.vm
	vm.SetMaxCallStackSize(1024)
	lx := vm.NewObject()
	_ = lx.Set("EVENT_NAMES", map[string]string{"request": "request", "inited": "inited", "updateAlert": "updateAlert"})
	_ = lx.Set("env", "desktop")
	_ = lx.Set("version", "2.0.0")
	_ = lx.Set("on", func(call goja.FunctionCall) goja.Value {
		if call.Argument(0).String() == "request" {
			fn, ok := goja.AssertFunction(call.Argument(1))
			if !ok {
				panic(vm.NewTypeError("request handler must be a function"))
			}
			r.handler = fn
		}
		return goja.Undefined()
	})
	_ = lx.Set("send", func(call goja.FunctionCall) goja.Value {
		if call.Argument(0).String() == "inited" && !r.inited {
			data, err := json.Marshal(call.Argument(1).Export())
			if err != nil || len(data) > 64<<10 || json.Unmarshal(data, &r.caps) != nil {
				panic(vm.NewTypeError("invalid initialization"))
			}
			r.inited = true
		}
		return goja.Undefined()
	})
	_ = lx.Set("request", r.request)
	_ = vm.Set("lx", lx)
	// Script output is deliberately dropped, not heuristically redacted. Even a
	// seemingly innocuous error may embed a Key or a complete signed URL.
	noop := func(goja.FunctionCall) goja.Value { return goja.Undefined() }
	_ = vm.Set("console", map[string]any{"log": noop, "info": noop, "warn": noop, "error": noop, "debug": noop, "trace": noop})
	_ = vm.Set("setTimeout", func(call goja.FunctionCall) goja.Value { return r.setTimer(call, false) })
	_ = vm.Set("setInterval", func(call goja.FunctionCall) goja.Value { return r.setTimer(call, true) })
	clear := func(id int) {
		if t := r.timers[id]; t != nil {
			t.timer.Stop()
			delete(r.timers, id)
		}
	}
	_ = vm.Set("clearTimeout", clear)
	_ = vm.Set("clearInterval", clear)
	_ = vm.Set("queueMicrotask", func(call goja.FunctionCall) goja.Value {
		_, ok := goja.AssertFunction(call.Argument(0))
		if !ok {
			panic(vm.NewTypeError("callback required"))
		}
		// Promise.then, rather than a goroutine or immediate invocation, preserves
		// microtask ordering and Goja's job queue ownership.
		p, resolve, _ := vm.NewPromise()
		then, _ := goja.AssertFunction(vm.ToValue(p).ToObject(vm).Get("then"))
		if _, err := then(vm.ToValue(p), call.Argument(0)); err != nil {
			panic(err)
		}
		if err := resolve(goja.Undefined()); err != nil {
			panic(err)
		}
		return goja.Undefined()
	})
}

func (r *Runtime) setTimer(call goja.FunctionCall, repeat bool) goja.Value {
	fn, ok := goja.AssertFunction(call.Argument(0))
	if !ok {
		panic(r.vm.NewTypeError("timer callback required"))
	}
	if len(r.timers) >= maxTimers {
		panic(r.vm.NewTypeError(string(ErrLimit)))
	}
	ms := call.Argument(1).ToInteger()
	if ms < 0 {
		ms = 0
	}
	if ms > int64(taskTimeout/time.Millisecond) {
		ms = int64(taskTimeout / time.Millisecond)
	}
	d := time.Duration(ms) * time.Millisecond
	if repeat && d < time.Millisecond {
		d = time.Millisecond
	}
	r.nextTimer++
	id := r.nextTimer
	var args []goja.Value
	if len(call.Arguments) > 2 {
		args = append([]goja.Value(nil), call.Arguments[2:]...)
	}
	t := &timerEntry{fn: fn, args: args}
	if repeat {
		t.interval = d
	}
	r.timers[id] = t
	r.armTimer(id, t, d)
	return r.vm.ToValue(id)
}

func (r *Runtime) armTimer(id int, t *timerEntry, d time.Duration) {
	t.timer = time.AfterFunc(d, func() {
		r.post(func() error {
			if r.timers[id] != t {
				return nil
			}
			if t.interval == 0 {
				delete(r.timers, id)
			}
			_, err := t.fn(goja.Undefined(), t.args...)
			if err == nil && t.interval > 0 && r.timers[id] == t {
				r.armTimer(id, t, t.interval)
			}
			return err
		})
	})
}

func (r *Runtime) Resolve(ctx context.Context, t domain.Track, quality string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, taskTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p := &resolution{ctx: ctx, track: t, quality: quality, reply: make(chan resolveResult, 1)}
	if !r.post(func() error { return r.start(p) }) {
		return "", ErrClosed
	}
	stop := context.AfterFunc(ctx, func() {
		r.post(func() error {
			if _, ok := r.pending[p]; ok {
				delete(r.pending, p)
				// Other consumers may be sharing this script's in-flight Promise.
				// Retire only when none remain; don't strand its JS cache on reuse.
				if len(r.pending) == 0 {
					r.cancel()
				}
			}
			return nil
		})
	})
	defer stop()
	select {
	case result := <-p.reply:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return result.url, result.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-r.done:
		return "", ErrClosed
	}
}

func (r *Runtime) start(p *resolution) error {
	fail := func(err error) error { p.reply <- resolveResult{err: err}; return nil }
	if err := p.ctx.Err(); err != nil {
		return fail(err)
	}
	if len(r.pending) >= maxPending {
		return fail(ErrLimit)
	}
	s, ok := r.caps.Sources[p.track.Provider]
	if !ok || !contains(s.Actions, "musicUrl") || !contains(s.Qualitys, p.quality) {
		return fail(ErrUnsupported)
	}
	identity, err := playbackIdentity(p.track)
	if err != nil {
		return fail(err)
	}
	r.pending[p] = struct{}{}
	result, err := r.handler(goja.Undefined(), r.vm.ToValue(map[string]any{
		"action": "musicUrl", "source": p.track.Provider, "info": map[string]any{"type": p.quality, "musicInfo": identity},
	}))
	if err != nil {
		delete(r.pending, p)
		p.reply <- resolveResult{err: ErrRejected}
		var interrupt *goja.InterruptedError
		if errors.As(err, &interrupt) {
			return err
		}
		return nil
	}
	if promise, ok := result.Export().(*goja.Promise); ok {
		p.promise = promise
	} else {
		r.finish(p, result, nil)
	}
	return nil
}

func (r *Runtime) settle() {
	for p := range r.pending {
		if p.promise == nil {
			continue
		}
		switch p.promise.State() {
		case goja.PromiseStateFulfilled:
			r.finish(p, p.promise.Result(), nil)
		case goja.PromiseStateRejected:
			r.finish(p, nil, ErrRejected)
		}
	}
}

func (r *Runtime) finish(p *resolution, value goja.Value, err error) {
	delete(r.pending, p)
	var result string
	if err == nil {
		var ok bool
		result, ok = value.Export().(string)
		if !ok || !ValidURL(result) {
			result = ""
			err = ErrURL
		}
	}
	if e := p.ctx.Err(); e != nil {
		result = ""
		err = e
	}
	p.reply <- resolveResult{url: result, err: err}
}

func playbackIdentity(t domain.Track) (map[string]string, error) {
	if !t.Ref().Valid() || len(t.ID) > 1024 {
		return nil, ErrIdentity
	}
	p := t.Playback
	// A Track from Catalog carries exactly one platform-specific playback ID.
	// Don't inject unrelated IDs (LX prefers hash, then songmid, then id).
	count := 0
	for _, v := range []string{p.ID, p.Hash, p.Songmid} {
		if v != "" {
			count++
		}
		if len(v) > 1024 {
			return nil, ErrIdentity
		}
	}
	if count > 1 {
		return nil, ErrIdentity
	}
	switch t.Provider {
	case "kg":
		if p.ID != "" || p.Songmid != "" {
			return nil, ErrIdentity
		}
		if p.Hash == "" {
			p.Hash = t.ID
		}
		return map[string]string{"hash": p.Hash}, nil
	case "tx":
		if p.ID != "" || p.Hash != "" {
			return nil, ErrIdentity
		}
		if p.Songmid == "" {
			p.Songmid = t.ID
		}
		return map[string]string{"songmid": p.Songmid}, nil
	default:
		if p.Hash != "" || p.Songmid != "" {
			return nil, ErrIdentity
		}
		if p.ID == "" {
			p.ID = t.ID
		}
		return map[string]string{"id": p.ID}, nil
	}
}
