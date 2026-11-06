package sourcehost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
func mockResponse(req *http.Request, status int, body string) (*http.Response, error) {
	return &http.Response{Request: req, StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
func fixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/lx-contract-fixture.js")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func newRuntime(t *testing.T, script string, transport roundTripFunc) *Runtime {
	t.Helper()
	if transport == nil {
		transport = func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected HTTP; no network permitted")
		}
	}
	r, err := New(context.Background(), script, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}
func track(provider, id string) domain.Track { return domain.Track{Provider: provider, ID: id} }

const initSource = `lx.send(lx.EVENT_NAMES.inited,{status:true,sources:{wy:{name:'wy',actions:['musicUrl'],qualitys:['320k']}}});`

func TestFixtureAllPlatformQualitiesAndCache(t *testing.T) {
	// The unmodified, credential-sanitized script resolves all 32 combinations through injected HTTP and preserves exact IDs.
	var mu sync.Mutex
	type parameters struct{ source, id, quality string }
	var calls []parameters
	r := newRuntime(t, fixture(t), func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "source.shiqianjiang.cn" || req.URL.Path != "/api/music/url" {
			return nil, errors.New("unexpected service target")
		}
		q := req.URL.Query()
		mu.Lock()
		calls = append(calls, parameters{q.Get("source"), q.Get("songId"), q.Get("quality")})
		mu.Unlock()
		return mockResponse(req, 200, `{"code":200,"url":"https://audio.invalid/test.mp3?signature=transient"}`)
	})
	caps := r.Capabilities()
	if len(caps.Sources) != 5 || len(calls) != 0 {
		t.Fatal("initialization must only advertise five providers, without HTTP")
	}
	count := 0
	for provider, support := range caps.Sources {
		if len(support.Actions) != 1 || support.Actions[0] != "musicUrl" {
			t.Fatal("unexpected actions")
		}
		for _, quality := range support.Qualitys {
			id := "900719925474099312345678-" + provider + "-" + quality
			tr := track(provider, id)
			switch provider {
			case "tx":
				tr.Playback.Songmid = id
			case "kg":
				tr.Playback.Hash = id
			default:
				tr.Playback.ID = id
			}
			value, err := r.Resolve(context.Background(), tr, quality)
			if err != nil {
				t.Fatal(err)
			}
			if !ValidURL(value) {
				t.Fatal("missing playback URL")
			}
			mu.Lock()
			last := calls[len(calls)-1]
			n := len(calls)
			mu.Unlock()
			if last != (parameters{provider, id, quality}) {
				t.Fatal("platform parameters or string identity changed")
			}
			cached, err := r.Resolve(context.Background(), tr, quality)
			if err != nil || cached != value {
				t.Fatal("script URL cache did not survive Promise completion")
			}
			mu.Lock()
			after := len(calls)
			mu.Unlock()
			if after != n {
				t.Fatal("cache hit sent HTTP")
			}
			count++
		}
	}
	if count != 32 || len(calls) != 32 {
		t.Fatal("expected 32 distinct platform-quality requests")
	}
	caps.Sources["wy"].Qualitys[0] = "mutated"
	if r.Capabilities().Sources["wy"].Qualitys[0] == "mutated" {
		t.Fatal("capability snapshot aliases runtime state")
	}
}

func TestFixtureFailureBoundaries(t *testing.T) {
	// HTTP statuses override business success and the script's own retry budget remains authoritative.
	for _, tc := range []struct {
		name             string
		status, attempts int
		success          bool
	}{
		{"forbidden", 403, 2, true}, {"limited", 429, 2, true}, {"server", 500, 2, false}, {"all-invalid", 401, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			var previousKey string
			keyChanged := false
			r := newRuntime(t, fixture(t), func(req *http.Request) (*http.Response, error) {
				n := int(count.Add(1))
				key := req.Header.Get("X-API-Key")
				if n > 1 {
					keyChanged = keyChanged || key != previousKey
				}
				previousKey = key
				status := tc.status
				if tc.success && n > 1 {
					status = 200
				}
				resp, err := mockResponse(req, status, `{"code":200,"url":"https://audio.invalid/ok"}`)
				resp.Header.Set("Retry-After", "60")
				return resp, err
			})
			_, err := r.Resolve(context.Background(), track("wy", "id"), "320k")
			if (err == nil) != tc.success || int(count.Load()) != tc.attempts {
				t.Fatalf("success=%v attempts=%d", err == nil, count.Load())
			}
			if !keyChanged {
				t.Fatal("script did not rotate credentials")
			}
		})
	}
}

func TestAsyncInitializationTimersAndMicrotasks(t *testing.T) {
	// Promise jobs, asynchronous inited, timer arguments and interval cancellation all execute on the owner loop.
	script := `let n=0; let interval=setInterval(()=>{n++;clearInterval(interval)},1);
	setTimeout(()=>{` + initSource + `},0);
	lx.on('request',async()=>{
	 if (typeof require!=='undefined'||typeof process!=='undefined'||typeof fetch!=='undefined') throw Error('unsafe capability');
	 const events=[];queueMicrotask(()=>events.push('micro'));
	 await new Promise(resolve=>setTimeout(resolve,0));
	 return await new Promise(resolve=>setTimeout((suffix)=>resolve('https://audio.invalid/'+events.join('')+suffix),0,'-args'));
	});`
	r := newRuntime(t, script, nil)
	value, err := r.Resolve(context.Background(), track("wy", "id"), "320k")
	if err != nil || value != "https://audio.invalid/micro-args" {
		t.Fatalf("async dispatch failed: %v", err)
	}
}

func TestSharedPromiseConsumerCancellation(t *testing.T) {
	// Canceling one waiter does not cancel a transport shared by another Promise consumer.
	started := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	r := newRuntime(t, fixture(t), func(req *http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return mockResponse(req, 200, `{"code":200,"url":"https://audio.invalid/shared"}`)
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { _, err := r.Resolve(ctx, track("wy", "same"), "320k"); first <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP did not start")
	}
	go func() { _, err := r.Resolve(context.Background(), track("wy", "same"), "320k"); second <- err }()
	// A barrier on the loop proves the second waiter has been registered; don't
	// use sleeps or assume goroutine scheduling order.
	registered := make(chan struct{})
	var check func() error
	check = func() error {
		if len(r.pending) >= 2 {
			close(registered)
		} else {
			r.post(check)
		}
		return nil
	}
	r.post(check)
	select {
	case <-registered:
	case <-time.After(3 * time.Second):
		t.Fatal("second consumer did not register")
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal("canceled consumer succeeded")
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("shared Promise lost deduplication")
	}
}

func TestCancelLastConsumerClosesTransportAndLoop(t *testing.T) {
	// A last-consumer cancellation retires pending JS state and cancels its HTTP rather than poisoning later work.
	started := make(chan struct{})
	stopped := make(chan struct{})
	r := newRuntime(t, fixture(t), func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		close(stopped)
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := r.Resolve(ctx, track("wy", "cancel"), "320k"); result <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP did not start")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("expected context cancellation")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP leaked after cancellation")
	}
	select {
	case <-r.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("event loop leaked after cancellation")
	}
}

func TestLXHTTPBodyStatusHeadersAndCancel(t *testing.T) {
	// The callback receives status, lowercase headers and body while its callable cancel handle aborts the underlying HTTP.
	var seen atomic.Bool
	script := initSource + `lx.on('request',()=>new Promise((resolve,reject)=>{
	 const cancel=lx.request('https://service.invalid/first',{method:'POST',json:{song:'id'},timeout:1000},(err,resp,body)=>{
	   if(err)return reject(err);
	   if(resp.statusCode!==201||resp.headers['x-test']!=='ok'||resp.body!==body)throw Error('bad callback');
	   let abort=lx.request('https://service.invalid/cancel',{},(err)=>err?resolve('https://audio.invalid/canceled'):reject(Error('not canceled')));
	   abort();
	 });
	 if(typeof cancel!=='function')reject(Error('bad handle'));
	}));`
	r := newRuntime(t, script, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/cancel" {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		body, err := io.ReadAll(req.Body)
		if err != nil || req.Method != "POST" || req.Header.Get("Content-Type") != "application/json" || string(body) != `{"song":"id"}` {
			return nil, errors.New("unexpected HTTP body")
		}
		seen.Store(true)
		resp, err := mockResponse(req, 201, "payload")
		resp.Header.Set("X-Test", "ok")
		return resp, err
	})
	_, err := r.Resolve(context.Background(), track("wy", "id"), "320k")
	if err != nil || !seen.Load() {
		t.Fatal("HTTP callback contract failed", err)
	}
}

func TestErrorsNeverExposeScriptSecrets(t *testing.T) {
	// Exceptions, Promise rejection and bad URL results only return fixed host categories.
	secret := "private-test-marker"
	for _, body := range []string{`throw Error('` + secret + `')`, `return Promise.reject(new Error('` + secret + `'))`, `return 'file:///` + secret + `'`, `return {url:'https://audio.invalid/` + secret + `'}`} {
		r := newRuntime(t, initSource+`lx.on('request',()=>{`+body+`});`, nil)
		_, err := r.Resolve(context.Background(), track("wy", "id"), "320k")
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("unsafe error boundary")
		}
	}
}

func TestUnsupportedAndConflictingIdentityNeverSendHTTP(t *testing.T) {
	// Invalid provider/quality and mixed platform identities fail before a script transport can run.
	var count atomic.Int32
	r := newRuntime(t, fixture(t), func(req *http.Request) (*http.Response, error) {
		count.Add(1)
		return nil, errors.New("unexpected transport")
	})
	for _, tc := range []struct {
		tr domain.Track
		q  string
	}{
		{track("unknown", "id"), "320k"}, {track("wy", "id"), "not-a-quality"}, {track("wy", ""), "320k"},
		{domain.Track{Provider: "tx", ID: "id", Playback: domain.PlaybackIdentity{ID: "id", Songmid: "mid"}}, "320k"},
		{domain.Track{Provider: "kg", ID: "id", Playback: domain.PlaybackIdentity{ID: "wrong"}}, "320k"},
	} {
		if _, err := r.Resolve(context.Background(), tc.tr, tc.q); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if count.Load() != 0 {
		t.Fatal("invalid request sent HTTP")
	}
}

func TestInitializationCancellationInterruptsInfiniteJS(t *testing.T) {
	// An infinite initialization is interruptible and doesn't leave an event-loop goroutine behind.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(ctx, `for(;;){}`, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled init accepted")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := New(ctx, `for(;;){}`, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("infinite init was not bounded")
	}
}

func TestCloseInterruptsSynchronousRequest(t *testing.T) {
	// Close interrupts CPU-bound JavaScript even when the event loop cannot consume a cancel event.
	r := newRuntime(t, initSource+`lx.on('request',()=>{for(;;){}});`, nil)
	result := make(chan error, 1)
	go func() { _, err := r.Resolve(context.Background(), track("wy", "id"), "320k"); result <- err }()
	r.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("closed runtime succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close did not interrupt JS")
	}
}

func TestStreamWireDoesNotUseDomainJSON(t *testing.T) {
	// Domain deliberately omits transient URLs, while the pipe must round-trip the URL explicitly.
	value := "https://audio.invalid/track?signature=transient"
	ordinary, _ := json.Marshal(domain.Stream{URL: value, Size: -1})
	if strings.Contains(string(ordinary), value) {
		t.Fatal("domain persisted a playback URL")
	}
	data, err := json.Marshal(Response{ID: 7, Stream: &StreamWire{URL: value, Size: -1}})
	if err != nil {
		t.Fatal(err)
	}
	var res Response
	if json.Unmarshal(data, &res) != nil || res.Stream == nil || res.Stream.Domain().URL != value {
		t.Fatal("RPC lost the transient URL")
	}
}

func TestPublicTransportRejectsLocalNetwork(t *testing.T) {
	// The production dialer rejects private/loopback destinations before any TCP connection is attempted.
	transport := publicTransport()
	for _, address := range []string{"127.0.0.1:80", "[::1]:80", "192.168.1.1:80", "169.254.169.254:80"} {
		conn, err := transport.DialContext(context.Background(), "tcp", address)
		if conn != nil {
			conn.Close()
			t.Fatal("local network connection escaped")
		}
		if err == nil {
			t.Fatal("local network accepted")
		}
	}
}
