package player

import (
	"context"
	"os"
	"testing"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// A deliberately supplied public fixture URL exercises the real transport;
// this test never resolves a song or calls a paid/account service.
func TestStreamLive(t *testing.T) {
	url := os.Getenv("PLAYER_TEST_AUDIO_URL")
	if os.Getenv("LIVE") == "" || url == "" {
		t.Skip("set LIVE and PLAYER_TEST_AUDIO_URL for explicit audio transport testing")
	}
	dl := startDownload(context.Background(), &domain.Stream{URL: url, Size: -1}, nil)
	defer dl.Close()
	s, err := openStream(dl, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	buf := make([]float32, 4096)
	n, err := s.Read(buf)
	if n == 0 || err != nil {
		t.Fatalf("first audio buffer: %d, %v", n, err)
	}
	if err := s.SetPosition(int64(s.SampleRate())); err != nil {
		t.Fatal(err)
	}
}
