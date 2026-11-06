package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// Providers are catalog platforms, never resolver SourceIDs. local reserves an
// identity for local media without treating an imported script as a platform.
func validateRef(ref domain.TrackRef) error {
	switch ref.Provider {
	case "wy", "tx", "kg", "kw", "mg", "local":
	default:
		return fmt.Errorf("song provider must be a catalog platform: %w", ErrInvalid)
	}
	if !validID(ref.ID) {
		return fmt.Errorf("song platform identifier is empty or invalid: %w", ErrInvalid)
	}
	return nil
}

func encodeTrack(t domain.Track) (string, error) {
	if err := validateRef(t.Ref()); err != nil {
		return "", err
	}
	if t.Duration < 0 {
		return "", fmt.Errorf("song duration must not be negative: %w", ErrInvalid)
	}
	// Catalog chooses the LX identifier; in particular both QQ and Migu use
	// songmid. Do not reinterpret or replace it with the public platform ID.
	// Multiple fields would let LX's hash/songmid/id priority hide a mismatch.
	identities := 0
	for _, s := range []string{t.Playback.ID, t.Playback.Hash, t.Playback.Songmid} {
		if s != "" && !validID(s) {
			return "", fmt.Errorf("invalid song playback identifier: %w", ErrInvalid)
		}
		if s != "" {
			identities++
		}
	}
	if identities > 1 {
		return "", fmt.Errorf("song playback identity mixes identifiers: %w", ErrInvalid)
	}
	b, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("encode song metadata: %w", err)
	}
	return string(b), nil
}

func decodeTrack(data string) (domain.Track, error) {
	var t domain.Track
	if err := json.Unmarshal([]byte(data), &t); err != nil {
		return t, fmt.Errorf("decode stored song metadata: %w", err)
	}
	if _, err := encodeTrack(t); err != nil {
		return t, fmt.Errorf("invalid stored song metadata: %w", err)
	}
	return t, nil
}

func upsertTrack(ctx context.Context, tx *sql.Tx, t domain.Track, data string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tracks(provider, platform_id, track_json) VALUES (?, ?, ?)
		ON CONFLICT(provider, platform_id) DO UPDATE SET track_json = excluded.track_json`, t.Provider, t.ID, data)
	if err != nil {
		return fmt.Errorf("save song metadata for platform %s: %w", t.Provider, err)
	}
	return nil
}

func scanTracks(rows *sql.Rows) (tracks []domain.Track, err error) {
	defer closeRows(rows, &err)
	tracks = make([]domain.Track, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("read song metadata: %w", err)
		}
		t, err := decodeTrack(data)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read songs: %w", err)
	}
	return tracks, nil
}
