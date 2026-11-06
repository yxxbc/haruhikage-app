package source

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yxxbc/haruhikage-app/internal/sourcehost"
)

func TestSourceHostHelperProcess(t *testing.T) {
	// The helper runs exactly the production pipe server with an injected, non-network HTTP implementation.
	if os.Getenv("HARUHIKAGE_SOURCE_HOST_TEST_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	client := &http.Client{Transport: mockTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{Request: req, StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":200,"url":"https://audio.invalid/process?signature=temporary"}`))}, nil
	})}
	if sourcehost.Serve(context.Background(), os.Stdin, os.Stdout, client) != nil {
		os.Exit(1)
	}
	os.Exit(0) // Never let Go's test status text contaminate stdout RPC.
}

func TestHiddenProcessFixtureRPC(t *testing.T) {
	// The real pipe/process boundary initializes the exact fixture and returns a URL that domain JSON would omit.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestSourceHostHelperProcess$")
	cmd.Env = append(os.Environ(), "HARUHIKAGE_SOURCE_HOST_TEST_HELPER=1")
	// startWorkerCommand always sets HideWindow and CREATE_NO_WINDOW on Windows;
	// it starts the test executable directly and never invokes a shell/toolchain.
	w, err := startWorkerCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	data, err := os.ReadFile(filepath.Join("..", "sourcehost", "testdata", "lx-contract-fixture.js"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.call(context.Background(), sourcehost.Request{Method: "init", Script: string(data)})
	if err != nil || res.Caps == nil || len(res.Caps.Sources) != 5 {
		t.Fatal("isolated initialization failed", err)
	}
	res, err = w.call(context.Background(), sourcehost.Request{Method: "resolve", Track: testTrack, Quality: "320k"})
	if err != nil || res.Stream == nil || res.Stream.URL != "https://audio.invalid/process?signature=temporary" {
		t.Fatal("isolated URL resolution failed", err)
	}
	w.close()
	if !w.dead() {
		t.Fatal("worker did not close")
	}
}
