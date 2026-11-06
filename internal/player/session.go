package player

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// UnmarshalJSON accepts the former numeric-ID session without importing
// its platform package. IDs never pass through float64 (including >2^53).
// Radio/account/privilege fields are intentionally not migrated.
func (s *Session) UnmarshalJSON(b []byte) error {
	var wire struct {
		Version  int               `json:"version"`
		Queue    []json.RawMessage `json:"queue"`
		EntryIDs []QueueEntryID    `json:"entryIds"`
		Order    []int             `json:"order"`
		At       int               `json:"at"`
		Shuffle  bool              `json:"shuffle"`
		Repeat   RepeatMode        `json:"repeat"`
		Position int64             `json:"position"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	out := Session{Version: wire.Version, EntryIDs: wire.EntryIDs, Order: wire.Order, At: wire.At, Shuffle: wire.Shuffle, Repeat: wire.Repeat, Position: wire.Position}
	for _, raw := range wire.Queue {
		var identity struct {
			Provider string `json:"provider"`
		}
		if err := json.Unmarshal(raw, &identity); err != nil {
			return err
		}
		if identity.Provider != "" {
			var t domain.Track
			if err := json.Unmarshal(raw, &t); err != nil {
				return err
			}
			out.Queue = append(out.Queue, t)
			continue
		}
		if wire.Version >= 2 {
			return errors.New("player: session track has no provider")
		}
		var old struct {
			ID      json.RawMessage `json:"id"`
			Name    string          `json:"name"`
			Artists []struct {
				ID   json.RawMessage `json:"id"`
				Name string          `json:"name"`
			} `json:"ar"`
			Album struct {
				ID     json.RawMessage `json:"id"`
				Name   string          `json:"name"`
				PicURL string          `json:"picUrl"`
			} `json:"al"`
			Duration int64    `json:"dt"`
			Alias    []string `json:"alia"`
		}
		if err := json.Unmarshal(raw, &old); err != nil {
			return err
		}
		id, err := legacyID(old.ID)
		if err != nil {
			return err
		}
		albumID, _ := legacyID(old.Album.ID)
		t := domain.Track{Provider: "wy", ID: id, Name: old.Name, Duration: old.Duration, Alias: old.Alias, Album: domain.Album{ID: albumID, Name: old.Album.Name, PicURL: old.Album.PicURL}, Playback: domain.PlaybackIdentity{ID: id}}
		for _, a := range old.Artists {
			id, _ := legacyID(a.ID)
			t.Artists = append(t.Artists, domain.Artist{ID: id, Name: a.Name})
		}
		out.Queue = append(out.Queue, t)
	}
	*s = out
	return nil
}

func legacyID(raw json.RawMessage) (string, error) {
	s := strings.TrimSpace(string(raw))
	if strings.HasPrefix(s, "\"") {
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
	}
	if s == "" {
		return "", errors.New("player: missing legacy track ID")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return "", errors.New("player: invalid legacy track ID")
		}
	}
	return s, nil
}
