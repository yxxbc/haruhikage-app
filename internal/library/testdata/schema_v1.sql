-- Frozen version-1 database fixture for forward-migration tests.
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_migrations VALUES (1, 1700000000000000000);
CREATE TABLE tracks (
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
CREATE INDEX play_history_recent ON play_history(played_at DESC, sequence DESC);
INSERT INTO tracks VALUES ('wy', '900719925474099312345', '{"provider":"wy","id":"900719925474099312345","name":"Existing song","artists":[],"album":{"name":"Existing album"},"duration":123000,"playback":{"id":"900719925474099312345"}}');
INSERT INTO playlists VALUES ('existing-playlist', 'Existing playlist', 1700000000000000000, 1700000000000000000);
INSERT INTO playlist_entries VALUES ('existing-entry', 'existing-playlist', 'wy', '900719925474099312345', 0, 1700000000000000000);
INSERT INTO favorites(provider, platform_id, added_at) VALUES ('wy', '900719925474099312345', 1700000000000000000);
INSERT INTO play_history(provider, platform_id, position_ns, played_at) VALUES ('wy', '900719925474099312345', 123456789, 1700000000000000000);
