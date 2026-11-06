package app

import (
	"context"
	"github.com/yxxbc/haruhikage-app/internal/domain"
	"github.com/yxxbc/haruhikage-app/internal/library"
	"github.com/yxxbc/haruhikage-app/internal/player"
	"testing"
)

// Tests use an isolated, in-memory library and synchronous scheduling.
func newTestApp(ts ...*testing.T) *app {
	a := newApp()
	a.settings = defaultSettings()
	a.runTask = func(f func()) { f() }
	a.testPost = func(f func()) { f() }
	a.lib, _ = library.Open(":memory:")
	a.art = newArtCache(a.post)
	a.pl = player.New(a.post)
	a.pl.Resolve = func(context.Context, domain.Track, string) (*domain.Stream, error) { return nil, context.Canceled }
	a.volume = .8
	a.sourceList = nil
	if len(ts) > 0 {
		ts[0].Cleanup(func() {
			a.quitting = true
			a.cancel()
			if a.art != nil {
				a.art.Close()
			}
			if a.pl != nil {
				_ = a.pl.Close()
			}
			if a.sessionWriter != nil {
				a.sessionWriter.Close()
			}
			if a.lib != nil {
				_ = a.lib.Close()
			}
		})
	}
	return a
}
