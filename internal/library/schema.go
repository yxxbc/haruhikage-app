package library

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Each migration and its ledger entry commit together. Never edit a released
// migration: append a new one so an older database can be upgraded in place.
var migrations = []string{
	`CREATE TABLE tracks (
		provider TEXT NOT NULL,
		platform_id TEXT NOT NULL,
		track_json TEXT NOT NULL,
		PRIMARY KEY (provider, platform_id)
	);
	CREATE TABLE playlists (
		id TEXT PRIMARY KEY NOT NULL,
		name TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE playlist_entries (
		entry_id TEXT PRIMARY KEY NOT NULL,
		playlist_id TEXT NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
		provider TEXT NOT NULL,
		platform_id TEXT NOT NULL,
		order_key INTEGER NOT NULL,
		added_at INTEGER NOT NULL,
		FOREIGN KEY (provider, platform_id) REFERENCES tracks(provider, platform_id),
		UNIQUE (playlist_id, order_key)
	);
	CREATE INDEX playlist_entries_track ON playlist_entries(provider, platform_id);
	CREATE TABLE favorites (
		sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		provider TEXT NOT NULL,
		platform_id TEXT NOT NULL,
		added_at INTEGER NOT NULL,
		FOREIGN KEY (provider, platform_id) REFERENCES tracks(provider, platform_id),
		UNIQUE (provider, platform_id)
	);
	CREATE TABLE play_history (
		sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		provider TEXT NOT NULL,
		platform_id TEXT NOT NULL,
		position_ns INTEGER NOT NULL CHECK (position_ns >= 0),
		played_at INTEGER NOT NULL,
		FOREIGN KEY (provider, platform_id) REFERENCES tracks(provider, platform_id)
	);
	CREATE INDEX play_history_recent ON play_history(played_at DESC, sequence DESC);`,
	`CREATE TABLE state (
		key TEXT PRIMARY KEY NOT NULL,
		value_json TEXT NOT NULL
	);`,
}

func migrate(ctx context.Context, db *sql.DB) error {
	return transact(ctx, db, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`); err != nil {
			return fmt.Errorf("create library migration ledger: %w", err)
		}
		version, err := migrationVersion(ctx, tx)
		if err != nil {
			return err
		}
		for i := version; i < len(migrations); i++ {
			if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
				return fmt.Errorf("apply library schema migration %d: %w", i+1, err)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)", i+1, time.Now().UTC().UnixNano()); err != nil {
				return fmt.Errorf("record library schema migration %d: %w", i+1, err)
			}
		}
		return nil
	})
}

func migrationVersion(ctx context.Context, tx *sql.Tx) (version int, err error) {
	rows, err := tx.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, fmt.Errorf("read library migration ledger: %w", err)
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return 0, fmt.Errorf("read library migration version: %w", err)
		}
		if v != version+1 {
			return 0, fmt.Errorf("library migration ledger is not contiguous at version %d: %w", v, ErrInvalid)
		}
		if v > len(migrations) {
			return 0, fmt.Errorf("library schema %d is newer than supported schema %d", v, len(migrations))
		}
		version = v
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read library migration ledger: %w", err)
	}
	return version, nil
}
