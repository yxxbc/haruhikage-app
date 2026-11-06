// source-host has no dependency on MyGo, audio, the UI or the user's library.
// stdout is exclusively a bounded JSON pipe; script console output is discarded.
package main

import (
	"context"
	"os"

	"github.com/yxxbc/haruhikage-app/internal/sourcehost"
)

func main() {
	if err := sourcehost.Serve(context.Background(), os.Stdin, os.Stdout, nil); err != nil {
		// Do not print the underlying error: it may originate in untrusted JS.
		os.Exit(1)
	}
}
