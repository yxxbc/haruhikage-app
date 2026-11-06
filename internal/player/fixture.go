package player

import (
	"time"

	"github.com/egoist/mygo/audio"

	"github.com/yxxbc/haruhikage-app/internal/domain"
	"github.com/yxxbc/haruhikage-app/pkg/lyrics"
)

// These set a player up as it would be after loading, without loading
// anything, for the tests of the views that show it.

// Cue makes tracks the queue, in order, with the first current.
func (p *Player) Cue(tracks []domain.Track) {
	p.stop()
	p.setQueue(tracks)
	p.at, p.track = -1, nil
	if len(tracks) > 0 {
		p.at, p.track = 0, &p.queue[0]
	}
}

// SetLyrics gives the current track lines, as loaded.
func (p *Player) SetLyrics(lines []lyrics.Line) {
	p.lyrics, p.lyricsLoaded = lines, true
}

// Hold makes the player play at pos with nothing to play: its position
// stays there.
func (p *Player) Hold(pos time.Duration) {
	p.pending = pos
	p.playing = true
}

// Attach makes out the current track's audio, playing.
func (p *Player) Attach(out *audio.Player) {
	p.out = out
	p.playing = true
}

// SetStream makes st what the current track streams.
func (p *Player) SetStream(st *domain.Stream) { p.info = st }
