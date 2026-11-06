package library

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

const (
	BackupVersion  = 1
	backupFormat   = "haruhikage-library"
	maxBackupBytes = 64 << 20
)

// Arrays carry the user-visible order; playlist entry IDs survive a round trip.
// Sources, scripts, account data and resolved streams have no backup fields.
type backupDocument struct {
	Format    string           `json:"format"`
	Version   int              `json:"version"`
	Tracks    []domain.Track   `json:"tracks"`
	Playlists []backupPlaylist `json:"playlists"`
	Favorites []backupFavorite `json:"favorites"`
	History   []backupHistory  `json:"history"`
	State     []backupState    `json:"state"`
}

type backupPlaylist struct {
	domain.Playlist
	Entries []backupEntry `json:"entries"`
}

type backupEntry struct {
	ID      string          `json:"id"`
	Track   domain.TrackRef `json:"track"`
	AddedAt time.Time       `json:"addedAt"`
}

type backupFavorite struct {
	Track   domain.TrackRef `json:"track"`
	AddedAt time.Time       `json:"addedAt"`
}

type backupHistory struct {
	Track    domain.TrackRef `json:"track"`
	Position time.Duration   `json:"position"` // nanoseconds, like HistoryEntry
	PlayedAt time.Time       `json:"playedAt"`
}

type backupState struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// Export takes a consistent database snapshot, then releases the database before
// writing to the caller's writer. It never closes the writer. A failed write
// returns an error; callers exporting to a file should use a temporary file and
// rename it only after Export and their file Close both succeed.
func (s *Store) Export(ctx context.Context, w io.Writer) error {
	if w == nil {
		return fmt.Errorf("export writer is nil: %w", ErrInvalid)
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	doc := backupDocument{
		Format: backupFormat, Version: BackupVersion,
		Tracks: make([]domain.Track, 0), Playlists: make([]backupPlaylist, 0),
		Favorites: make([]backupFavorite, 0), History: make([]backupHistory, 0), State: make([]backupState, 0),
	}
	err = transact(ctx, s.db, &sql.TxOptions{ReadOnly: true}, func(tx *sql.Tx) error {
		return readBackup(ctx, tx, &doc)
	})
	release()
	if err != nil {
		return err
	}
	if _, err := validateBackup(&doc); err != nil {
		return fmt.Errorf("cannot export invalid library snapshot: %w", err)
	}
	enc := json.NewEncoder(&contextWriter{ctx: ctx, w: w, remaining: maxBackupBytes})
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("write library backup: %w", err)
	}
	return nil
}

func readBackup(ctx context.Context, tx *sql.Tx, doc *backupDocument) error {
	rows, err := tx.QueryContext(ctx, "SELECT track_json FROM tracks ORDER BY provider, platform_id")
	if err != nil {
		return fmt.Errorf("export song metadata: %w", err)
	}
	doc.Tracks, err = scanTracks(rows)
	if err != nil {
		return err
	}
	doc.Playlists, err = readBackupPlaylists(ctx, tx)
	if err != nil {
		return err
	}
	for i := range doc.Playlists {
		p := &doc.Playlists[i]
		entries, err := readEntries(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		p.Count = len(entries)
		p.Entries = make([]backupEntry, len(entries))
		for j, e := range entries {
			p.Entries[j] = backupEntry{ID: e.ID, Track: e.Track.Ref(), AddedAt: e.AddedAt}
		}
	}
	if err := readBackupFavorites(ctx, tx, doc); err != nil {
		return err
	}
	if err := readBackupHistory(ctx, tx, doc); err != nil {
		return err
	}
	return readBackupState(ctx, tx, doc)
}

func readBackupPlaylists(ctx context.Context, tx *sql.Tx) (playlists []backupPlaylist, err error) {
	rows, err := tx.QueryContext(ctx, "SELECT id, name, created_at, updated_at FROM playlists ORDER BY created_at, id")
	if err != nil {
		return nil, fmt.Errorf("export local playlists: %w", err)
	}
	defer closeRows(rows, &err)
	playlists = make([]backupPlaylist, 0)
	for rows.Next() {
		var p backupPlaylist
		var created, updated int64
		if err := rows.Scan(&p.ID, &p.Name, &created, &updated); err != nil {
			return nil, fmt.Errorf("export local playlist: %w", err)
		}
		p.CreatedAt, p.UpdatedAt = storedTime(created), storedTime(updated)
		playlists = append(playlists, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export local playlists: %w", err)
	}
	return playlists, nil
}

func readBackupFavorites(ctx context.Context, tx *sql.Tx, doc *backupDocument) (err error) {
	rows, err := tx.QueryContext(ctx, "SELECT provider, platform_id, added_at FROM favorites ORDER BY added_at DESC, sequence DESC")
	if err != nil {
		return fmt.Errorf("export local favorites: %w", err)
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var f backupFavorite
		var added int64
		if err := rows.Scan(&f.Track.Provider, &f.Track.ID, &added); err != nil {
			return fmt.Errorf("export local favorite: %w", err)
		}
		f.AddedAt = storedTime(added)
		doc.Favorites = append(doc.Favorites, f)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("export local favorites: %w", err)
	}
	return nil
}

func readBackupHistory(ctx context.Context, tx *sql.Tx, doc *backupDocument) (err error) {
	rows, err := tx.QueryContext(ctx, "SELECT provider, platform_id, position_ns, played_at FROM play_history ORDER BY played_at DESC, sequence DESC")
	if err != nil {
		return fmt.Errorf("export local playback history: %w", err)
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var h backupHistory
		var position, played int64
		if err := rows.Scan(&h.Track.Provider, &h.Track.ID, &position, &played); err != nil {
			return fmt.Errorf("export local playback event: %w", err)
		}
		h.Position, h.PlayedAt = time.Duration(position), storedTime(played)
		doc.History = append(doc.History, h)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("export local playback history: %w", err)
	}
	return nil
}

func readBackupState(ctx context.Context, tx *sql.Tx, doc *backupDocument) (err error) {
	rows, err := tx.QueryContext(ctx, "SELECT key, value_json FROM state ORDER BY key")
	if err != nil {
		return fmt.Errorf("export library state: %w", err)
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var state backupState
		var value string
		if err := rows.Scan(&state.Key, &value); err != nil {
			return fmt.Errorf("export library state value: %w", err)
		}
		state.Value = json.RawMessage(value)
		doc.State = append(doc.State, state)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("export library state: %w", err)
	}
	return nil
}

// Import replaces music metadata, playlists, favorites, history and state in
// one transaction, retaining the database's own migration ledger. The entire
// input must be a supported, valid backup before any database writes start.
// It never reads, installs, enables or restores source scripts or credentials.
func (s *Store) Import(ctx context.Context, r io.Reader) error {
	if r == nil {
		return fmt.Errorf("import reader is nil: %w", ErrInvalid)
	}
	// Don't hold the database hostage while an external reader is delivering
	// data. Reacquiring below also rejects an Import whose store closed meanwhile.
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	release()
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: r}, maxBackupBytes+1))
	if err != nil {
		return fmt.Errorf("read library backup: %w", err)
	}
	if len(data) > maxBackupBytes {
		return fmt.Errorf("library backup exceeds %d bytes: %w", maxBackupBytes, ErrInvalid)
	}
	if err := validateJSON(data, false); err != nil {
		return fmt.Errorf("invalid library backup JSON: %w", errors.Join(ErrInvalid, err))
	}
	var doc backupDocument
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("decode library backup: %w", errors.Join(ErrInvalid, err))
	}
	trackData, err := validateBackup(&doc)
	if err != nil {
		return err
	}
	release, err = s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return transact(ctx, s.db, nil, func(tx *sql.Tx) error {
		return replaceBackup(ctx, tx, &doc, trackData)
	})
}

func validateBackup(doc *backupDocument) ([]string, error) {
	if doc.Format != backupFormat {
		return nil, fmt.Errorf("not a Haruhikage library backup: %w", ErrInvalid)
	}
	if doc.Version != BackupVersion {
		return nil, fmt.Errorf("library backup version %d, supported version %d: %w", doc.Version, BackupVersion, ErrBackupVersion)
	}
	if doc.Tracks == nil || doc.Playlists == nil || doc.Favorites == nil || doc.History == nil || doc.State == nil {
		return nil, fmt.Errorf("library backup is missing a required array: %w", ErrInvalid)
	}
	trackData := make([]string, len(doc.Tracks))
	tracks := make(map[string]bool, len(doc.Tracks))
	for i, t := range doc.Tracks {
		data, err := encodeTrack(t)
		if err != nil {
			return nil, fmt.Errorf("backup song %d: %w", i+1, err)
		}
		if tracks[t.Key()] {
			return nil, fmt.Errorf("duplicate song identity in backup: %w", ErrInvalid)
		}
		tracks[t.Key()] = true
		trackData[i] = data
	}
	checkRef := func(ref domain.TrackRef) error {
		if err := validateRef(ref); err != nil {
			return err
		}
		if !tracks[ref.Key()] {
			return fmt.Errorf("backup refers to missing song metadata: %w", ErrInvalid)
		}
		return nil
	}
	playlists := make(map[string]bool, len(doc.Playlists))
	entries := make(map[string]bool)
	for _, p := range doc.Playlists {
		if err := requireID(p.ID, "backup playlist"); err != nil {
			return nil, err
		}
		if playlists[p.ID] {
			return nil, fmt.Errorf("duplicate playlist identifier in backup: %w", ErrInvalid)
		}
		playlists[p.ID] = true
		name, err := cleanName(p.Name)
		if err != nil || name != p.Name {
			return nil, fmt.Errorf("backup playlist name is empty or untrimmed: %w", ErrInvalid)
		}
		if _, err := timeValue(p.CreatedAt); err != nil {
			return nil, err
		}
		if _, err := timeValue(p.UpdatedAt); err != nil {
			return nil, err
		}
		if p.Entries == nil || p.Count != len(p.Entries) {
			return nil, fmt.Errorf("backup playlist count does not match its entries: %w", ErrInvalid)
		}
		for _, e := range p.Entries {
			if err := requireID(e.ID, "backup playlist item"); err != nil {
				return nil, err
			}
			if entries[e.ID] {
				return nil, fmt.Errorf("duplicate playlist entry identifier in backup: %w", ErrInvalid)
			}
			entries[e.ID] = true
			if err := checkRef(e.Track); err != nil {
				return nil, err
			}
			if _, err := timeValue(e.AddedAt); err != nil {
				return nil, err
			}
		}
	}
	favorites := make(map[string]bool, len(doc.Favorites))
	for _, f := range doc.Favorites {
		if err := checkRef(f.Track); err != nil {
			return nil, err
		}
		if favorites[f.Track.Key()] {
			return nil, fmt.Errorf("duplicate favorite in backup: %w", ErrInvalid)
		}
		favorites[f.Track.Key()] = true
		if _, err := timeValue(f.AddedAt); err != nil {
			return nil, err
		}
	}
	for _, h := range doc.History {
		if err := checkRef(h.Track); err != nil {
			return nil, err
		}
		if h.Position < 0 {
			return nil, fmt.Errorf("negative playback position in backup: %w", ErrInvalid)
		}
		if _, err := timeValue(h.PlayedAt); err != nil {
			return nil, err
		}
	}
	states := make(map[string]bool, len(doc.State))
	for _, state := range doc.State {
		if states[state.Key] {
			return nil, fmt.Errorf("duplicate state key in backup: %w", ErrInvalid)
		}
		states[state.Key] = true
		if err := validateState(state.Key, state.Value); err != nil {
			return nil, err
		}
	}
	return trackData, nil
}

func replaceBackup(ctx context.Context, tx *sql.Tx, doc *backupDocument, trackData []string) error {
	for _, table := range []string{"playlist_entries", "favorites", "play_history", "playlists", "tracks", "state"} {
		// Table names are a fixed internal list, never backup-controlled SQL.
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("clear library %s during import: %w", table, err)
		}
	}
	for i, t := range doc.Tracks {
		if err := upsertTrack(ctx, tx, t, trackData[i]); err != nil {
			return err
		}
	}
	for _, p := range doc.Playlists {
		if _, err := tx.ExecContext(ctx, "INSERT INTO playlists(id, name, created_at, updated_at) VALUES (?, ?, ?, ?)", p.ID, p.Name, p.CreatedAt.UnixNano(), p.UpdatedAt.UnixNano()); err != nil {
			return fmt.Errorf("import local playlist: %w", err)
		}
		for i, e := range p.Entries {
			if _, err := tx.ExecContext(ctx, `INSERT INTO playlist_entries(entry_id, playlist_id, provider, platform_id, order_key, added_at)
				VALUES (?, ?, ?, ?, ?, ?)`, e.ID, p.ID, e.Track.Provider, e.Track.ID, i, e.AddedAt.UnixNano()); err != nil {
				return fmt.Errorf("import local playlist item: %w", err)
			}
		}
	}
	// Reverse insertion preserves the explicit newest-first array order even
	// for events/likes with identical timestamps; row IDs are not backup IDs.
	for i := len(doc.Favorites) - 1; i >= 0; i-- {
		f := doc.Favorites[i]
		if _, err := tx.ExecContext(ctx, "INSERT INTO favorites(provider, platform_id, added_at) VALUES (?, ?, ?)", f.Track.Provider, f.Track.ID, f.AddedAt.UnixNano()); err != nil {
			return fmt.Errorf("import local favorite: %w", err)
		}
	}
	for i := len(doc.History) - 1; i >= 0; i-- {
		h := doc.History[i]
		if _, err := tx.ExecContext(ctx, "INSERT INTO play_history(provider, platform_id, position_ns, played_at) VALUES (?, ?, ?, ?)", h.Track.Provider, h.Track.ID, int64(h.Position), h.PlayedAt.UnixNano()); err != nil {
			return fmt.Errorf("import playback history: %w", err)
		}
	}
	for _, state := range doc.State {
		if _, err := tx.ExecContext(ctx, "INSERT INTO state(key, value_json) VALUES (?, ?)", state.Key, string(state.Value)); err != nil {
			return fmt.Errorf("import library JSON state: %w", err)
		}
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type contextWriter struct {
	ctx       context.Context
	w         io.Writer
	remaining int
}

func (w *contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > w.remaining {
		return 0, fmt.Errorf("library backup exceeds %d bytes: %w", maxBackupBytes, ErrInvalid)
	}
	n, err := w.w.Write(p)
	w.remaining -= n
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}
