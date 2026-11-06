package library

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxStateBytes = 1 << 20

// SaveState stores a JSON snapshot, not a stream/source/credential container.
// RawMessage is validated before SQLite is changed and copied by the database.
// Public track metadata such as picUrl/webUrl may appear in a player session;
// transient stream url/headers and credentials may not.
func (s *Store) SaveState(ctx context.Context, key string, value json.RawMessage) error {
	if err := validateState(key, value); err != nil {
		return err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return transact(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO state(key, value_json) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json`, key, string(value)); err != nil {
			return fmt.Errorf("save library JSON state: %w", err)
		}
		return nil
	})
}

// LoadState returns ErrNotFound for a key that has never been saved. JSON null
// is a valid saved value and is distinct from a missing key.
func (s *Store) LoadState(ctx context.Context, key string) (json.RawMessage, error) {
	if err := validateStateKey(key); err != nil {
		return nil, err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var value string
	err = s.db.QueryRowContext(ctx, "SELECT value_json FROM state WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load library JSON state: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("load library JSON state: %w", err)
	}
	if err := validateState(key, json.RawMessage(value)); err != nil {
		return nil, fmt.Errorf("invalid stored JSON state: %w", err)
	}
	return json.RawMessage(value), nil
}

func validateStateKey(key string) error {
	if err := requireID(key, "state"); err != nil {
		return err
	}
	// Reserved namespaces prevent source management or account data being
	// smuggled into a general state bucket and later included in a backup.
	for _, part := range strings.FieldsFunc(key, func(r rune) bool { return r == '.' || r == '/' || r == ':' }) {
		if restrictedField(part) {
			return fmt.Errorf("state key belongs to a transient or credential namespace: %w", ErrInvalid)
		}
	}
	return nil
}

func validateState(key string, value json.RawMessage) error {
	if err := validateStateKey(key); err != nil {
		return err
	}
	if len(value) == 0 || len(value) > maxStateBytes {
		return fmt.Errorf("JSON state must contain between 1 and %d bytes: %w", maxStateBytes, ErrInvalid)
	}
	if err := validateJSON(value, true); err != nil {
		return fmt.Errorf("unsafe or invalid JSON state: %w", errors.Join(ErrInvalid, err))
	}
	return nil
}

func restrictedField(name string) bool {
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, name)
	switch name {
	case "url", "streamurl", "playbackurl", "resolvedurl", "stream", "streams",
		"header", "headers", "cookie", "cookies", "setcookie", "authorization", "proxyauthorization",
		"password", "passwd", "token", "accesstoken", "refreshtoken", "apikey", "key", "keys",
		"secret", "clientsecret", "credential", "credentials", "auth",
		"script", "scripts", "scriptcode", "sourcecode", "sourcescript", "sourcescripts", "source", "sources":
		return true
	}
	return false
}

// validateJSON rejects duplicate object fields (encoding/json otherwise uses
// last-wins semantics), excess nesting and trailing values. State additionally
// rejects fields reserved for secrets, source code and resolved streams.
func validateJSON(data []byte, protectState bool) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("JSON is not valid UTF-8: %w", ErrInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := walkJSON(dec, 0, protectState); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err != nil {
			return fmt.Errorf("read trailing JSON: %w", err)
		}
		return fmt.Errorf("multiple JSON values: %w", ErrInvalid)
	}
	return nil
}

func walkJSON(dec *json.Decoder, depth int, protectState bool) error {
	if depth > 64 {
		return fmt.Errorf("JSON nesting exceeds 64 levels: %w", ErrInvalid)
	}
	token, err := dec.Token()
	if err != nil {
		return fmt.Errorf("read JSON value: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for dec.More() {
			token, err := dec.Token()
			if err != nil {
				return fmt.Errorf("read JSON field: %w", err)
			}
			name, ok := token.(string)
			if !ok {
				return fmt.Errorf("JSON object field is not a string: %w", ErrInvalid)
			}
			if seen[name] {
				return fmt.Errorf("duplicate JSON object field: %w", ErrInvalid)
			}
			seen[name] = true
			if protectState && restrictedField(name) {
				return fmt.Errorf("JSON state contains a transient, source or credential field: %w", ErrInvalid)
			}
			if err := walkJSON(dec, depth+1, protectState); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("unterminated JSON object: %w", ErrInvalid)
		}
	case '[':
		for dec.More() {
			if err := walkJSON(dec, depth+1, protectState); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("unterminated JSON array: %w", ErrInvalid)
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter: %w", ErrInvalid)
	}
	return nil
}
