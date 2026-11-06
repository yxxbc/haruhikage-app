package library

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

//go:embed testdata/schema_v1.sql
var schemaV1 string

func seedSQLite(t *testing.T, path, script string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), script); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close fixture database: %v", closeErr)
		}
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVersionOneMigrationPreservesExistingLibrary(t *testing.T) {
	// Opening an older real database adds state storage without losing its data.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.sqlite")
	seedSQLite(t, path, schemaV1)
	s := openTestStore(t, path)
	entries, err := s.Entries(ctx, "existing-playlist")
	if err != nil || len(entries) != 1 || entries[0].ID != "existing-entry" || entries[0].Track.ID != "900719925474099312345" {
		t.Fatalf("migrated entries = %+v, err=%v", entries, err)
	}
	favorites, err := s.Favorites(ctx)
	if err != nil || len(favorites) != 1 || favorites[0].Key() != entries[0].Track.Key() {
		t.Fatalf("migrated favorites = %+v, err=%v", favorites, err)
	}
	history, err := s.History(ctx, 10)
	if err != nil || len(history) != 1 || history[0].Position != 123456789 {
		t.Fatalf("migrated history = %+v, err=%v", history, err)
	}
	if err := s.SaveState(ctx, "session", json.RawMessage(`{"version":1,"paused":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	if _, err := reopened.LoadState(ctx, "session"); err != nil {
		t.Fatalf("migration was not persistent: %v", err)
	}
}

func TestFailedMigrationRollsBackAndCanBeRetried(t *testing.T) {
	// A migration collision rolls back its ledger and succeeds after repair.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "blocked-migration.sqlite")
	seedSQLite(t, path, schemaV1+"\nCREATE TABLE state (unexpected TEXT);\n")
	if s, err := Open(path); err == nil {
		if closeErr := s.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatal("migration collision was silently accepted")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	readErr := db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version)
	if readErr != nil {
		if closeErr := db.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatal(readErr)
	}
	if version != 1 {
		t.Errorf("failed migration advanced the ledger to %d", version)
	}
	_, repairErr := db.ExecContext(ctx, "DROP TABLE state")
	closeErr := db.Close()
	if repairErr != nil || closeErr != nil {
		t.Fatalf("repair fixture: %v; close: %v", repairErr, closeErr)
	}
	s := openTestStore(t, path)
	if err := s.SaveState(ctx, "session", json.RawMessage(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	assertTrackOrder(t, s, "existing-playlist", song("wy", "900719925474099312345"))
}

func TestFutureAndBrokenMigrationLedgersAreRejected(t *testing.T) {
	// Unsupported or incomplete schema histories cannot be treated as current.
	for name, ledger := range map[string]string{
		"future": "INSERT INTO schema_migrations VALUES (2, 1700000000000000000), (3, 1700000000000000000);",
		"gap":    "INSERT INTO schema_migrations VALUES (3, 1700000000000000000);",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unsupported.sqlite")
			seedSQLite(t, path, schemaV1+ledger)
			if s, err := Open(path); err == nil {
				if closeErr := s.Close(); closeErr != nil {
					t.Error(closeErr)
				}
				t.Fatal("unsupported migration ledger was accepted")
			}
		})
	}
}

func TestOpenReportsDirectoryAndDatabaseErrors(t *testing.T) {
	// Invalid paths and non-SQLite files produce actionable errors, not empty stores.
	if _, err := Open(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty path = %v", err)
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, "a-file")
	if err := os.WriteFile(blocked, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(filepath.Join(blocked, "library.sqlite")); err == nil {
		if closeErr := s.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatal("file used as parent directory was accepted")
	}
	if s, err := Open(blocked); err == nil {
		if closeErr := s.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatal("non-SQLite database file was silently replaced")
	}
	data, err := os.ReadFile(blocked)
	if err != nil || string(data) != "fixture" {
		t.Fatalf("failed open changed the non-database file: %q, %v", data, err)
	}
}
