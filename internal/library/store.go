// Package library stores local music metadata, playlists and playback state.
// Resolved streams and source scripts belong to other services, not this store.
package library

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

var (
	ErrClosed        = errors.New("library is closed")
	ErrNotFound      = errors.New("library item not found")
	ErrInvalid       = errors.New("invalid library data")
	ErrBackupVersion = errors.New("unsupported library backup version")
)

// Store serializes operations into short transactions on one SQLite connection.
// Close rejects new work and waits for an operation already using the database.
// Use one Store per application; separate processes still use SQLite's locking.
type Store struct {
	db        *sql.DB
	gate      chan struct{}
	closing   chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// Open creates the parent directory and applies all versioned migrations before
// returning a usable store. path is a filename, not a caller-supplied SQLite DSN.
// The special filename :memory: is supported, but ordinary files are persistent.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("open library: empty database path: %w", ErrInvalid)
	}
	dsn := ":memory:"
	if path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve library path %q: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return nil, fmt.Errorf("create library directory %q: %w", filepath.Dir(abs), err)
		}
		uriPath := filepath.ToSlash(abs)
		if !strings.HasPrefix(uriPath, "/") {
			uriPath = "/" + uriPath // file:///C:/... on Windows.
		}
		u := url.URL{Scheme: "file", Path: uriPath}
		dsn = u.String()
	}
	// DSN pragmas apply to every connection, including a replacement connection.
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	if dsn == ":memory:" {
		dsn = "file::memory:"
	}
	dsn += "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open library database %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fail := func(err error) (*Store, error) {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close failed library database: %w", closeErr))
		}
		return nil, fmt.Errorf("open library database %q: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		return fail(err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		return fail(fmt.Errorf("enable SQLite write-ahead logging: %w", err))
	}
	if err := migrate(ctx, db); err != nil {
		return fail(err)
	}
	s := &Store{db: db, gate: make(chan struct{}, 1), closing: make(chan struct{})}
	s.gate <- struct{}{}
	return s, nil
}

// Close is concurrent-safe and idempotent. Every caller observes the same error.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		close(s.closing)
		<-s.gate
		if err := s.db.Close(); err != nil {
			s.closeErr = fmt.Errorf("close library database: %w", err)
		}
		s.gate <- struct{}{}
	})
	return s.closeErr
}

func (s *Store) acquire(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, fmt.Errorf("library context is nil: %w", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-s.closing:
		return nil, ErrClosed
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closing:
		return nil, ErrClosed
	case <-s.gate:
	}
	release := func() { s.gate <- struct{}{} }
	select {
	case <-s.closing:
		release()
		return nil, ErrClosed
	default:
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func transact(ctx context.Context, db *sql.DB, opts *sql.TxOptions, fn func(*sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin library transaction: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("roll back library transaction: %w", rollbackErr))
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit library transaction: %w", err)
	}
	return nil
}

func closeRows(rows *sql.Rows, err *error) {
	if closeErr := rows.Close(); closeErr != nil {
		*err = errors.Join(*err, fmt.Errorf("close library query: %w", closeErr))
	}
}

func newID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate library identifier: %w", err)
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func validID(id string) bool {
	return utf8.ValidString(id) && strings.TrimSpace(id) != "" && !strings.ContainsRune(id, '\x00')
}

func requireID(id, kind string) error {
	if !validID(id) {
		return fmt.Errorf("%s identifier is empty or invalid: %w", kind, ErrInvalid)
	}
	return nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') {
		return "", fmt.Errorf("playlist name is empty or invalid: %w", ErrInvalid)
	}
	return name, nil
}

func timeValue(t time.Time) (int64, error) {
	if t.IsZero() || !time.Unix(0, t.UnixNano()).Equal(t) {
		return 0, fmt.Errorf("timestamp is outside the supported range: %w", ErrInvalid)
	}
	return t.UnixNano(), nil
}

func storedTime(ns int64) time.Time { return time.Unix(0, ns).UTC() }
