package source

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/sourcehost"
)

func barePipeWorker(t *testing.T) (*worker, <-chan sourcehost.Request, chan<- sourcehost.Response) {
	t.Helper()
	input, writer := io.Pipe()
	reader, output := io.Pipe()
	w := newWorker(writer, reader, func() { input.Close(); output.Close() })
	requests := make(chan sourcehost.Request, 16)
	responses := make(chan sourcehost.Response, 16)
	go func() {
		defer close(requests)
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			var req sourcehost.Request
			if json.Unmarshal(scanner.Bytes(), &req) != nil {
				return
			}
			select {
			case requests <- req:
			case <-w.done:
				return
			}
		}
	}()
	go func() {
		for {
			select {
			case res := <-responses:
				if json.NewEncoder(output).Encode(res) != nil {
					return
				}
			case <-w.done:
				return
			}
		}
	}()
	t.Cleanup(w.close)
	return w, requests, responses
}
func receiveRequest(t *testing.T, requests <-chan sourcehost.Request) sourcehost.Request {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(3 * time.Second):
		t.Fatal("RPC did not arrive")
		return sourcehost.Request{}
	}
}

func TestWorkerCorrelatesOutOfOrderReplies(t *testing.T) {
	// Responses are routed by request ID, not read order, even with concurrent resolutions.
	w, requests, responses := barePipeWorker(t)
	type result struct {
		res sourcehost.Response
		err error
	}
	first := make(chan result, 1)
	second := make(chan result, 1)
	go func() {
		res, err := w.call(context.Background(), sourcehost.Request{Method: "resolve"})
		first <- result{res, err}
	}()
	one := receiveRequest(t, requests)
	go func() {
		res, err := w.call(context.Background(), sourcehost.Request{Method: "resolve"})
		second <- result{res, err}
	}()
	two := receiveRequest(t, requests)
	responses <- sourcehost.Response{ID: two.ID, Stream: &sourcehost.StreamWire{URL: "https://audio.invalid/two", Size: -1}}
	responses <- sourcehost.Response{ID: one.ID, Stream: &sourcehost.StreamWire{URL: "https://audio.invalid/one", Size: -1}}
	if got := <-first; got.err != nil || got.res.Stream.URL != "https://audio.invalid/one" {
		t.Fatal("first reply misrouted")
	}
	if got := <-second; got.err != nil || got.res.Stream.URL != "https://audio.invalid/two" {
		t.Fatal("second reply misrouted")
	}
}

func TestWorkerCancellationPreservesAnotherCall(t *testing.T) {
	// Cancel sends the target ID, ignores its late result, and does not close another active call.
	w, requests, responses := barePipeWorker(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { _, err := w.call(ctx, sourcehost.Request{Method: "resolve"}); first <- err }()
	one := receiveRequest(t, requests)
	go func() { _, err := w.call(context.Background(), sourcehost.Request{Method: "resolve"}); second <- err }()
	two := receiveRequest(t, requests)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal("canceled call succeeded")
	}
	cancelReq := receiveRequest(t, requests)
	if cancelReq.Method != "cancel" || cancelReq.CancelID != one.ID {
		t.Fatal("cancel targeted another call")
	}
	responses <- sourcehost.Response{ID: one.ID, Stream: &sourcehost.StreamWire{URL: "https://audio.invalid/late", Size: -1}}
	responses <- sourcehost.Response{ID: two.ID, Stream: &sourcehost.StreamWire{URL: "https://audio.invalid/current", Size: -1}}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if w.dead() {
		t.Fatal("canceling one call killed an unrelated consumer")
	}
}

func TestWorkerCloseUnblocksCall(t *testing.T) {
	// Worker shutdown unblocks an RPC even when the host never responds.
	w, requests, _ := barePipeWorker(t)
	result := make(chan error, 1)
	go func() { _, err := w.call(context.Background(), sourcehost.Request{Method: "resolve"}); result <- err }()
	receiveRequest(t, requests)
	w.close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("closed host succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close left RPC blocked")
	}
}

func TestWorkerDoesNotTrustRemoteErrors(t *testing.T) {
	// Only allowlisted host categories may cross the process boundary as an error.
	w, requests, responses := barePipeWorker(t)
	result := make(chan error, 1)
	go func() { _, err := w.call(context.Background(), sourcehost.Request{Method: "resolve"}); result <- err }()
	req := receiveRequest(t, requests)
	responses <- sourcehost.Response{ID: req.ID, Error: "private-test-marker https://audio.invalid/token"}
	if err := <-result; err == nil || err.Error() != "source operation failed" {
		t.Fatal("untrusted worker error escaped")
	}
}
