package source

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/sourcehost"
)

// worker multiplexes calls on one hidden process. There is one pipe reader and
// one writer, and neither ever holds a Manager lock. Cancellation is by ID;
// closing/switching a worker immediately invalidates all of its outstanding IDs.
type worker struct {
	mu         sync.Mutex
	in         io.WriteCloser
	out        io.ReadCloser
	send       chan sourcehost.Request
	pending    map[uint64]chan sourcehost.Response
	next       uint64
	done       chan struct{}
	exited     <-chan struct{} // process reaping; nil for injected transports
	terminated bool
	stopOnce   sync.Once
	stop       func()
}

func startWorker(path string) (*worker, error) {
	if path == "" {
		return nil, errors.New("source-host executable is not configured")
	}
	// exec.Command directly executes the binary (no cmd.exe, PowerShell or
	// shell expansion). Windows additionally uses HideWindow + CREATE_NO_WINDOW.
	return startWorkerCommand(exec.Command(path))
}

func startWorkerCommand(cmd *exec.Cmd) (*worker, error) {
	hideProcess(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		in.Close()
		out.Close()
		return nil, errors.New("cannot start source-host; install the complete application package")
	}
	w := newWorker(in, out, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	exited := make(chan struct{})
	w.exited = exited
	go func() { _ = cmd.Wait(); close(exited); w.terminate() }()
	return w, nil
}

// newWorker is also used with pipes and an injected HTTP host in contract tests.
func newWorker(in io.WriteCloser, out io.ReadCloser, stop func()) *worker {
	w := &worker{in: in, out: out, send: make(chan sourcehost.Request, 128), pending: make(map[uint64]chan sourcehost.Response), done: make(chan struct{}), stop: stop}
	go w.readLoop()
	go w.writeLoop()
	return w
}

func (w *worker) terminate() {
	w.stopOnce.Do(func() {
		w.mu.Lock()
		w.terminated = true
		close(w.done)
		w.mu.Unlock()
		_ = w.in.Close()
		_ = w.out.Close()
		if w.stop != nil {
			w.stop()
		}
	})
}
func (w *worker) close() {
	if w != nil {
		w.terminate()
		if w.exited != nil {
			select {
			case <-w.exited:
			case <-time.After(2 * time.Second):
			}
		}
	}
}
func (w *worker) dead() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

func (w *worker) readLoop() {
	defer w.terminate()
	scanner := bufio.NewScanner(w.out)
	scanner.Buffer(make([]byte, 4096), sourcehost.MaxResponseBytes)
	for scanner.Scan() {
		var res sourcehost.Response
		if json.Unmarshal(scanner.Bytes(), &res) != nil || res.ID == 0 {
			return
		}
		w.mu.Lock()
		reply := w.pending[res.ID]
		w.mu.Unlock()
		// A canceled request can legitimately finish after its waiter is gone.
		if reply != nil {
			select {
			case reply <- res:
			default:
				return
			}
		}
	}
}
func (w *worker) writeLoop() {
	defer w.terminate()
	for {
		select {
		case <-w.done:
			return
		case req := <-w.send:
			data, err := json.Marshal(req)
			if err != nil || len(data) >= sourcehost.MaxMessageBytes {
				return
			}
			data = append(data, '\n')
			if _, err = w.in.Write(data); err != nil {
				return
			}
		}
	}
}

func (w *worker) call(ctx context.Context, req sourcehost.Request) (sourcehost.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return sourcehost.Response{}, err
	}
	w.mu.Lock()
	if w.terminated {
		w.mu.Unlock()
		return sourcehost.Response{}, sourcehost.ErrClosed
	}
	if len(w.pending) >= 64 {
		w.mu.Unlock()
		return sourcehost.Response{}, sourcehost.ErrLimit
	}
	w.next++
	req.ID = w.next
	if deadline, ok := ctx.Deadline(); ok {
		req.TimeoutMS = max(1, time.Until(deadline).Milliseconds())
	}
	reply := make(chan sourcehost.Response, 1)
	w.pending[req.ID] = reply
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.pending, req.ID); w.mu.Unlock() }()
	select {
	case w.send <- req:
	case <-ctx.Done():
		w.cancelCall(req.ID)
		return sourcehost.Response{}, ctx.Err()
	case <-w.done:
		return sourcehost.Response{}, sourcehost.ErrClosed
	}
	select {
	case res := <-reply:
		if err := ctx.Err(); err != nil {
			w.cancelCall(req.ID)
			return sourcehost.Response{}, err
		}
		if res.Error != "" {
			err := safeRemoteError(res.Error)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				w.cancelCall(req.ID)
			}
			return res, err
		}
		return res, nil
	case <-ctx.Done():
		w.cancelCall(req.ID)
		return sourcehost.Response{}, ctx.Err()
	case <-w.done:
		return sourcehost.Response{}, sourcehost.ErrClosed
	}
}

func (w *worker) cancelCall(id uint64) {
	w.mu.Lock()
	delete(w.pending, id)
	remaining := len(w.pending)
	w.next++
	cancelID := w.next
	w.mu.Unlock()
	if remaining == 0 {
		w.terminate()
		return
	}
	select {
	case w.send <- sourcehost.Request{ID: cancelID, Method: "cancel", CancelID: id}:
	case <-w.done:
	default:
		w.terminate() // A blocked/broken protocol cannot retain unbounded work.
	}
}

func safeRemoteError(code string) error {
	switch code {
	case "canceled":
		return context.Canceled
	case "deadline exceeded":
		return context.DeadlineExceeded
	case string(sourcehost.ErrInit), string(sourcehost.ErrRejected), string(sourcehost.ErrUnsupported), string(sourcehost.ErrIdentity), string(sourcehost.ErrURL), string(sourcehost.ErrClosed), string(sourcehost.ErrLimit), string(sourcehost.ErrProtocol):
		return sourcehost.Error(code)
	default:
		return errors.New("source operation failed")
	}
}
