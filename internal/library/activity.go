package library

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

func (s *Store) Favorite(ctx context.Context, t domain.Track, liked bool) error {
	if err := validateRef(t.Ref()); err != nil {
		return err
	}
	var data string
	var err error
	if liked {
		data, err = encodeTrack(t)
		if err != nil {
			return err
		}
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return transact(ctx, s.db, nil, func(tx *sql.Tx) error {
		if !liked {
			if _, err := tx.ExecContext(ctx, "DELETE FROM favorites WHERE provider = ? AND platform_id = ?", t.Provider, t.ID); err != nil {
				return fmt.Errorf("remove local favorite: %w", err)
			}
			return nil
		}
		if err := upsertTrack(ctx, tx, t, data); err != nil {
			return err
		}
		// Liking an already liked song refreshes its metadata, not its position.
		if _, err := tx.ExecContext(ctx, `INSERT INTO favorites(provider, platform_id, added_at) VALUES (?, ?, ?)
			ON CONFLICT(provider, platform_id) DO NOTHING`, t.Provider, t.ID, time.Now().UTC().UnixNano()); err != nil {
			return fmt.Errorf("save local favorite: %w", err)
		}
		return nil
	})
}

// Favorites returns most recently liked first, with an explicit insertion-order
// tie breaker when timestamps are equal.
func (s *Store) Favorites(ctx context.Context) ([]domain.Track, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := s.db.QueryContext(ctx, `SELECT t.track_json FROM favorites f
		JOIN tracks t ON t.provider = f.provider AND t.platform_id = f.platform_id
		ORDER BY f.added_at DESC, f.sequence DESC`)
	if err != nil {
		return nil, fmt.Errorf("list local favorites: %w", err)
	}
	return scanTracks(rows)
}

func (s *Store) IsFavorite(ctx context.Context, ref domain.TrackRef) (bool, error) {
	if err := validateRef(ref); err != nil {
		return false, err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	var exists bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM favorites WHERE provider = ? AND platform_id = ?)", ref.Provider, ref.ID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check local favorite: %w", err)
	}
	return exists, nil
}

type HistoryEntry struct {
	Track    domain.Track  `json:"track"`
	Position time.Duration `json:"position"`
	PlayedAt time.Time     `json:"playedAt"`
}

// RecordPlay is an explicit successful-start event input, not a play attempt or
// progress update. The composition root must call it only from Player.Started;
// playlist edits, favorite edits and state saving never synthesize history.
// Each call records a new event, including repeated starts of the same song.
func (s *Store) RecordPlay(ctx context.Context, t domain.Track, position time.Duration) error {
	if position < 0 {
		return fmt.Errorf("playback position must not be negative: %w", ErrInvalid)
	}
	data, err := encodeTrack(t)
	if err != nil {
		return err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return transact(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := upsertTrack(ctx, tx, t, data); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO play_history(provider, platform_id, position_ns, played_at) VALUES (?, ?, ?, ?)", t.Provider, t.ID, int64(position), time.Now().UTC().UnixNano()); err != nil {
			return fmt.Errorf("record successful playback start: %w", err)
		}
		return nil
	})
}

// History returns newest first. limit == 0 selects the default of 100 events;
// negative limits are invalid. It is not a distinct-song list.
func (s *Store) History(ctx context.Context, limit int) (history []HistoryEntry, err error) {
	if limit < 0 {
		return nil, fmt.Errorf("history limit must not be negative: %w", ErrInvalid)
	}
	if limit == 0 {
		limit = 100
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := s.db.QueryContext(ctx, `SELECT t.track_json, h.position_ns, h.played_at FROM play_history h
		JOIN tracks t ON t.provider = h.provider AND t.platform_id = h.platform_id
		ORDER BY h.played_at DESC, h.sequence DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list local playback history: %w", err)
	}
	defer closeRows(rows, &err)
	history = make([]HistoryEntry, 0)
	for rows.Next() {
		var entry HistoryEntry
		var data string
		var position, played int64
		if err := rows.Scan(&data, &position, &played); err != nil {
			return nil, fmt.Errorf("read local playback history: %w", err)
		}
		entry.Track, err = decodeTrack(data)
		if err != nil {
			return nil, err
		}
		if position < 0 {
			return nil, fmt.Errorf("stored playback position is negative: %w", ErrInvalid)
		}
		entry.Position, entry.PlayedAt = time.Duration(position), storedTime(played)
		history = append(history, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list local playback history: %w", err)
	}
	return history, nil
}
