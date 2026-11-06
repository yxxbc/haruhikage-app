package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

// Entry is a stable playlist item identity. A playlist may contain the same
// platform song more than once; its occurrences always have different IDs.
type Entry struct {
	ID      string       `json:"id"`
	Track   domain.Track `json:"track"`
	AddedAt time.Time    `json:"addedAt"`
}

func (s *Store) Playlists(ctx context.Context) (playlists []domain.Playlist, err error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.name, COUNT(e.entry_id), p.created_at, p.updated_at
		FROM playlists p LEFT JOIN playlist_entries e ON e.playlist_id = p.id
		GROUP BY p.id, p.name, p.created_at, p.updated_at ORDER BY p.created_at, p.id`)
	if err != nil {
		return nil, fmt.Errorf("list local playlists: %w", err)
	}
	defer closeRows(rows, &err)
	playlists = make([]domain.Playlist, 0)
	for rows.Next() {
		var p domain.Playlist
		var created, updated int64
		if err := rows.Scan(&p.ID, &p.Name, &p.Count, &created, &updated); err != nil {
			return nil, fmt.Errorf("read local playlist: %w", err)
		}
		p.CreatedAt, p.UpdatedAt = storedTime(created), storedTime(updated)
		playlists = append(playlists, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list local playlists: %w", err)
	}
	return playlists, nil
}

func (s *Store) CreatePlaylist(ctx context.Context, name string) (domain.Playlist, error) {
	name, err := cleanName(name)
	if err != nil {
		return domain.Playlist{}, err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return domain.Playlist{}, err
	}
	defer release()
	id, err := newID("pl_")
	if err != nil {
		return domain.Playlist{}, err
	}
	now := time.Now().UTC()
	err = transact(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO playlists(id, name, created_at, updated_at) VALUES (?, ?, ?, ?)", id, name, now.UnixNano(), now.UnixNano()); err != nil {
			return fmt.Errorf("create local playlist: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.Playlist{}, err
	}
	return domain.Playlist{ID: id, Name: name, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) RenamePlaylist(ctx context.Context, id, name string) error {
	if err := requireID(id, "playlist"); err != nil {
		return err
	}
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return transact(ctx, s.db, nil, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE playlists SET name = ?, updated_at = ? WHERE id = ?", name, time.Now().UTC().UnixNano(), id)
		return changed(result, err, "rename local playlist")
	})
}

func (s *Store) DeletePlaylist(ctx context.Context, id string) error {
	if err := requireID(id, "playlist"); err != nil {
		return err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return transact(ctx, s.db, nil, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM playlists WHERE id = ?", id)
		return changed(result, err, "delete local playlist")
	})
}

// Tracks returns every occurrence in the playlist's explicit item order.
func (s *Store) Tracks(ctx context.Context, id string) ([]domain.Track, error) {
	entries, err := s.Entries(ctx, id)
	if err != nil {
		return nil, err
	}
	tracks := make([]domain.Track, len(entries))
	for i, entry := range entries {
		tracks[i] = entry.Track
	}
	return tracks, nil
}

func (s *Store) Entries(ctx context.Context, id string) (entries []Entry, err error) {
	if err := requireID(id, "playlist"); err != nil {
		return nil, err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	// A transaction also keeps the existence check and list consistent with a
	// different process deleting the playlist at the same time.
	err = transact(ctx, s.db, &sql.TxOptions{ReadOnly: true}, func(tx *sql.Tx) error {
		if err := requirePlaylist(ctx, tx, id); err != nil {
			return err
		}
		var queryErr error
		entries, queryErr = readEntries(ctx, tx, id)
		return queryErr
	})
	return entries, err
}

func readEntries(ctx context.Context, tx *sql.Tx, id string) (entries []Entry, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.entry_id, t.track_json, e.added_at
		FROM playlist_entries e JOIN tracks t ON t.provider = e.provider AND t.platform_id = e.platform_id
		WHERE e.playlist_id = ? ORDER BY e.order_key, e.entry_id`, id)
	if err != nil {
		return nil, fmt.Errorf("list local playlist items: %w", err)
	}
	defer closeRows(rows, &err)
	entries = make([]Entry, 0)
	for rows.Next() {
		var e Entry
		var data string
		var added int64
		if err := rows.Scan(&e.ID, &data, &added); err != nil {
			return nil, fmt.Errorf("read local playlist item: %w", err)
		}
		e.Track, err = decodeTrack(data)
		if err != nil {
			return nil, err
		}
		e.AddedAt = storedTime(added)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list local playlist items: %w", err)
	}
	return entries, nil
}

// AddTracks appends in input order, including repeated songs. The entire batch
// (metadata, entries and playlist timestamp) succeeds or rolls back together.
func (s *Store) AddTracks(ctx context.Context, id string, tracks []domain.Track) error {
	if err := requireID(id, "playlist"); err != nil {
		return err
	}
	data := make([]string, len(tracks))
	ids := make([]string, len(tracks))
	for i, t := range tracks {
		var err error
		data[i], err = encodeTrack(t)
		if err != nil {
			return fmt.Errorf("playlist item %d: %w", i+1, err)
		}
		ids[i], err = newID("entry_")
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
		if err := requirePlaylist(ctx, tx, id); err != nil {
			return err
		}
		var start int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(order_key), -1) + 1 FROM playlist_entries WHERE playlist_id = ?", id).Scan(&start); err != nil {
			return fmt.Errorf("find playlist append position: %w", err)
		}
		now := time.Now().UTC().UnixNano()
		for i, t := range tracks {
			if err := upsertTrack(ctx, tx, t, data[i]); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO playlist_entries(entry_id, playlist_id, provider, platform_id, order_key, added_at)
				VALUES (?, ?, ?, ?, ?, ?)`, ids[i], id, t.Provider, t.ID, start+int64(i), now); err != nil {
				return fmt.Errorf("append local playlist item %d: %w", i+1, err)
			}
		}
		if len(tracks) == 0 {
			return nil
		}
		return touchPlaylist(ctx, tx, id, now)
	})
}

// RemoveTrack removes all occurrences of ref. Use RemoveEntry to remove just
// one occurrence. Removing an absent song in an existing playlist is a no-op.
func (s *Store) RemoveTrack(ctx context.Context, id string, ref domain.TrackRef) error {
	if err := requireID(id, "playlist"); err != nil {
		return err
	}
	if err := validateRef(ref); err != nil {
		return err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return transact(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := requirePlaylist(ctx, tx, id); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM playlist_entries WHERE playlist_id = ? AND provider = ? AND platform_id = ?", id, ref.Provider, ref.ID)
		if err != nil {
			return fmt.Errorf("remove song from local playlist: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read removed playlist item count: %w", err)
		}
		if count == 0 {
			return nil
		}
		return touchPlaylist(ctx, tx, id, time.Now().UTC().UnixNano())
	})
}

func (s *Store) RemoveEntry(ctx context.Context, id, entryID string) error {
	if err := requireID(id, "playlist"); err != nil {
		return err
	}
	if err := requireID(entryID, "playlist item"); err != nil {
		return err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return transact(ctx, s.db, nil, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM playlist_entries WHERE playlist_id = ? AND entry_id = ?", id, entryID)
		if err := changed(result, err, "remove local playlist item"); err != nil {
			return err
		}
		return touchPlaylist(ctx, tx, id, time.Now().UTC().UnixNano())
	})
}

// MoveEntry moves one occurrence to a zero-based index in the resulting list.
// Other occurrences retain their relative order and all entry IDs remain stable.
func (s *Store) MoveEntry(ctx context.Context, id, entryID string, index int) error {
	if err := requireID(id, "playlist"); err != nil {
		return err
	}
	if err := requireID(entryID, "playlist item"); err != nil {
		return err
	}
	if index < 0 {
		return fmt.Errorf("playlist item index must not be negative: %w", ErrInvalid)
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return transact(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := requirePlaylist(ctx, tx, id); err != nil {
			return err
		}
		entries, err := readEntries(ctx, tx, id)
		if err != nil {
			return err
		}
		from := -1
		for i, e := range entries {
			if e.ID == entryID {
				from = i
				break
			}
		}
		if from == -1 {
			return fmt.Errorf("move local playlist item: %w", ErrNotFound)
		}
		if index >= len(entries) {
			return fmt.Errorf("playlist item index is out of range: %w", ErrInvalid)
		}
		if from == index {
			return nil
		}
		entry := entries[from]
		if from < index {
			copy(entries[from:index], entries[from+1:index+1])
		} else {
			copy(entries[index+1:from+1], entries[index:from])
		}
		entries[index] = entry
		// Temporarily use the negative key space inside this transaction so the
		// unique playlist/order constraint never collides while renumbering.
		if _, err := tx.ExecContext(ctx, "UPDATE playlist_entries SET order_key = -order_key - 1 WHERE playlist_id = ?", id); err != nil {
			return fmt.Errorf("prepare playlist reordering: %w", err)
		}
		for i, e := range entries {
			if _, err := tx.ExecContext(ctx, "UPDATE playlist_entries SET order_key = ? WHERE playlist_id = ? AND entry_id = ?", i, id, e.ID); err != nil {
				return fmt.Errorf("reorder local playlist item: %w", err)
			}
		}
		return touchPlaylist(ctx, tx, id, time.Now().UTC().UnixNano())
	})
}

func requirePlaylist(ctx context.Context, tx *sql.Tx, id string) error {
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM playlists WHERE id = ?", id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("local playlist: %w", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("find local playlist: %w", err)
	}
	return nil
}

func changed(result sql.Result, err error, action string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s result: %w", action, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", action, ErrNotFound)
	}
	return nil
}

func touchPlaylist(ctx context.Context, tx *sql.Tx, id string, ns int64) error {
	result, err := tx.ExecContext(ctx, "UPDATE playlists SET updated_at = ? WHERE id = ?", ns, id)
	return changed(result, err, "update local playlist timestamp")
}
