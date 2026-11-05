package player

import (
	"errors"
	"testing"

	"github.com/yxxbc/haruhikage-app/internal/netease"
)

// A song with no file at the level asked for streams at the best lower
// one instead of not at all.
func TestFetchStreamFallsBack(t *testing.T) {
	old := streamURL
	defer func() { streamURL = old }()
	var asked []string
	streamURL = func(_ int64, level string) (*netease.Stream, error) {
		asked = append(asked, level)
		if level == "exhigh" {
			return nil, netease.ErrUnavailable
		}
		return &netease.Stream{Level: level, Type: "mp3", Size: 1}, nil
	}
	p := New(func(func()) {})
	st, err := p.fetchStream(1, "exhigh")
	if err != nil || st.Level != "higher" {
		t.Fatalf("stream %+v, %v", st, err)
	}
	if len(asked) != 2 || asked[0] != "exhigh" || asked[1] != "higher" {
		t.Errorf("asked for %v", asked)
	}
}

// A song no level has is unavailable, and another error stops the fall.
func TestFetchStreamErrors(t *testing.T) {
	old := streamURL
	defer func() { streamURL = old }()
	streamURL = func(int64, string) (*netease.Stream, error) { return nil, netease.ErrUnavailable }
	p := New(func(func()) {})
	if _, err := p.fetchStream(1, "standard"); !errors.Is(err, netease.ErrUnavailable) {
		t.Errorf("unavailable gave %v", err)
	}

	offline := errors.New("offline")
	streamURL = func(int64, string) (*netease.Stream, error) { return nil, offline }
	if _, err := p.fetchStream(1, "exhigh"); err != offline {
		t.Errorf("offline gave %v", err)
	}
}
