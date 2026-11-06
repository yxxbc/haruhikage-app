package sourcehost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// Serve is the stdin/stdout protocol used by cmd/source-host. The HTTP client is
// injectable so the complete real-script contract can be tested without ever
// connecting to its service. input is closed on shutdown to unblock a pipe read.
func Serve(ctx context.Context, input io.ReadCloser, output io.Writer, client *http.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopInput := context.AfterFunc(ctx, func() {
		input.Close()
		if closer, ok := output.(io.Closer); ok {
			closer.Close()
		}
	})
	defer stopInput()
	var mu, writeMu sync.Mutex
	var runtime *Runtime
	initializing := false
	calls := make(map[uint64]context.CancelFunc)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		mu.Lock()
		rt := runtime
		for _, c := range calls {
			c()
		}
		mu.Unlock()
		if rt != nil {
			rt.Close()
		}
		wg.Wait()
	}()
	write := func(res Response) {
		data, err := json.Marshal(res)
		if err != nil || len(data) > MaxResponseBytes {
			cancel()
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if ctx.Err() != nil {
			return
		}
		data = append(data, '\n')
		if n, e := output.Write(data); e != nil || n != len(data) {
			cancel()
		}
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), MaxMessageBytes)
	for scanner.Scan() {
		var req Request
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.ID == 0 {
			return ErrProtocol
		}
		if req.Method == "shutdown" {
			return nil
		}
		if req.Method == "cancel" {
			mu.Lock()
			c := calls[req.CancelID]
			mu.Unlock()
			if c != nil {
				c()
			}
			continue
		}
		if req.Method != "init" && req.Method != "resolve" {
			write(Response{ID: req.ID, Error: string(ErrProtocol)})
			continue
		}
		mu.Lock()
		if _, exists := calls[req.ID]; exists {
			mu.Unlock()
			return ErrProtocol
		}
		if len(calls) >= maxPending {
			mu.Unlock()
			write(Response{ID: req.ID, Error: string(ErrLimit)})
			continue
		}
		d := taskTimeout
		if req.TimeoutMS > 0 && req.TimeoutMS < int64(d/time.Millisecond) {
			d = time.Duration(req.TimeoutMS) * time.Millisecond
		}
		callCtx, callCancel := context.WithTimeout(ctx, d)
		calls[req.ID] = callCancel
		if req.Method == "init" {
			if initializing || runtime != nil {
				delete(calls, req.ID)
				callCancel()
				mu.Unlock()
				write(Response{ID: req.ID, Error: string(ErrInit)})
				continue
			}
			initializing = true
		}
		mu.Unlock()
		wg.Add(1)
		go func(req Request) {
			defer wg.Done()
			defer func() { callCancel(); mu.Lock(); delete(calls, req.ID); mu.Unlock() }()
			res := Response{ID: req.ID}
			var err error
			if req.Method == "init" {
				var rt *Runtime
				rt, err = New(callCtx, req.Script, client)
				mu.Lock()
				initializing = false
				if err == nil && ctx.Err() == nil {
					runtime = rt
				} else if rt != nil {
					rt.Close()
				}
				mu.Unlock()
				if err == nil {
					caps := rt.Capabilities()
					res.Caps = &caps
				}
			} else {
				mu.Lock()
				rt := runtime
				mu.Unlock()
				if rt == nil {
					err = ErrInit
				} else {
					var value string
					value, err = rt.Resolve(callCtx, req.Track, req.Quality)
					if err == nil {
						res.Stream = &StreamWire{URL: value, Size: -1}
					}
				}
			}
			if err != nil {
				switch {
				case errors.Is(err, context.Canceled):
					res.Error = "canceled"
				case errors.Is(err, context.DeadlineExceeded):
					res.Error = "deadline exceeded"
				default:
					res.Error = errorCode(err)
				}
			}
			write(res)
		}(req)
	}
	if scanner.Err() != nil && ctx.Err() == nil {
		return ErrProtocol
	}
	return ctx.Err()
}
