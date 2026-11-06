// Package catalog implements anonymous, read-only platform search and lyrics.
// Playback URL resolution belongs to source, not to these adapters.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

type ProviderInfo struct{ ID, Name string }

type SearchPage struct {
	Tracks []domain.Track
	Total  int
	More   bool
}

// ErrorKind separates an empty catalogue result from transport, platform and
// protocol failures. Cancellation and deadlines also support errors.Is.
type ErrorKind string

const (
	InvalidRequest      ErrorKind = "invalid_request"
	UnsupportedProvider ErrorKind = "unsupported_provider"
	Canceled            ErrorKind = "canceled"
	Timeout             ErrorKind = "timeout"
	Network             ErrorKind = "network"
	HTTP                ErrorKind = "http"
	RateLimited         ErrorKind = "rate_limited"
	Remote              ErrorKind = "remote"
	Protocol            ErrorKind = "protocol"
	NotFound            ErrorKind = "not_found"
)

type Error struct {
	Provider, Op string
	Kind         ErrorKind
	StatusCode   int
	Code         string
	cause        error
}

// Error deliberately omits URLs, response bodies and platform lyric access keys.
func (e *Error) Error() string {
	s := fmt.Sprintf("catalog %s %s: %s", e.Provider, e.Op, e.Kind)
	if e.StatusCode != 0 {
		s += fmt.Sprintf(" (HTTP %d)", e.StatusCode)
	}
	return s
}
func (e *Error) Unwrap() error { return e.cause }

// Service is safe for concurrent use; it owns no account, cookies or unbounded
// per-song cache. Its HTTP client and per-operation deadline cover every step of
// a multi-request lyrics operation, including body reads.
type Service struct {
	client  *http.Client
	timeout time.Duration
}

func New() *Service { return NewWithClient(nil) }

// NewWithClient permits custom transports, proxies and httptest fixtures. The
// client is copied, its cookie jar is discarded, and a missing timeout gets the
// same bounded default as New. Configure the client before calling this method.
func NewWithClient(client *http.Client) *Service {
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	copy.Jar = nil
	if copy.Timeout <= 0 {
		copy.Timeout = 15 * time.Second
	}
	return &Service{client: &copy, timeout: copy.Timeout}
}

func (s *Service) Providers() []ProviderInfo {
	return []ProviderInfo{{"wy", "网易云音乐"}, {"tx", "QQ 音乐"}, {"kg", "酷狗音乐"}, {"kw", "酷我音乐"}, {"mg", "咪咕音乐"}}
}

func pageSize(provider string) int {
	switch provider {
	case "mg":
		return 20
	default:
		return 30
	}
}
func knownProvider(p string) bool {
	switch p {
	case "wy", "tx", "kg", "kw", "mg":
		return true
	}
	return false
}

func (s *Service) Search(ctx context.Context, provider, query string, page int) (SearchPage, error) {
	if !knownProvider(provider) {
		return SearchPage{}, failure(provider, "search", UnsupportedProvider, nil)
	}
	query = strings.TrimSpace(query)
	if page <= 0 {
		page = 1
	}
	// Bound arithmetic and pathological input without silently wrapping offsets.
	if query == "" || len(query) > 4096 || page > 1_000_000 {
		return SearchPage{}, failure(provider, "search", InvalidRequest, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return SearchPage{}, transportError(provider, "search", err)
	}
	var out SearchPage
	var err error
	switch provider {
	case "wy":
		out, err = s.searchWY(ctx, query, page)
	case "tx":
		out, err = s.searchTX(ctx, query, page)
	case "kg":
		out, err = s.searchKG(ctx, query, page)
	case "kw":
		out, err = s.searchKW(ctx, query, page)
	case "mg":
		out, err = s.searchMG(ctx, query, page)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return SearchPage{}, transportError(provider, "search", contextErr)
	}
	if err != nil {
		return SearchPage{}, err
	}
	// Remove duplicate identities in server order, never inventing page offsets
	// from the number of visible songs (which differs after platform grouping).
	seen := make(map[string]bool, len(out.Tracks))
	tracks := make([]domain.Track, 0, len(out.Tracks))
	for _, t := range out.Tracks {
		if !t.Ref().Valid() || t.Name == "" {
			return SearchPage{}, failure(provider, "search", Protocol, nil)
		}
		if !seen[t.Key()] {
			seen[t.Key()] = true
			tracks = append(tracks, t)
		}
	}
	out.Tracks = tracks
	out.More = page*pageSize(provider) < out.Total && len(tracks) > 0
	return out, nil
}

func (s *Service) Lyrics(ctx context.Context, t domain.Track) (domain.Lyrics, error) {
	if !knownProvider(t.Provider) {
		return domain.Lyrics{}, failure(t.Provider, "lyrics", UnsupportedProvider, nil)
	}
	if !t.Ref().Valid() {
		return domain.Lyrics{}, failure(t.Provider, "lyrics", InvalidRequest, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return domain.Lyrics{}, transportError(t.Provider, "lyrics", err)
	}
	var out domain.Lyrics
	var err error
	switch t.Provider {
	case "wy":
		out, err = s.lyricsWY(ctx, t)
	case "tx":
		out, err = s.lyricsTX(ctx, t)
	case "kg":
		out, err = s.lyricsKG(ctx, t)
	case "kw":
		out, err = s.lyricsKW(ctx, t)
	case "mg":
		out, err = s.lyricsMG(ctx, t)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return domain.Lyrics{}, transportError(t.Provider, "lyrics", contextErr)
	}
	if err != nil {
		return domain.Lyrics{}, err
	}
	if strings.TrimSpace(out.LRC) == "" && strings.TrimSpace(out.YRC) == "" {
		return domain.Lyrics{}, failure(t.Provider, "lyrics", NotFound, nil)
	}
	if !timedLyrics(out) {
		kind := Protocol
		if strings.Contains(out.LRC, "暂无") || strings.Contains(out.LRC, "纯音乐") {
			kind = NotFound
		}
		return domain.Lyrics{}, failure(t.Provider, "lyrics", kind, nil)
	}
	return out, nil
}

func failure(p, op string, kind ErrorKind, err error) *Error {
	return &Error{Provider: p, Op: op, Kind: kind, cause: err}
}
func transportError(p, op string, err error) *Error {
	kind := Network
	var timeout interface{ Timeout() bool }
	switch {
	case errors.Is(err, context.Canceled):
		kind = Canceled
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		kind = Timeout
	}
	return failure(p, op, kind, err)
}
func remoteError(p, op, code string) *Error {
	kind := Remote
	if code == "429" || code == "-429" {
		kind = RateLimited
	}
	e := failure(p, op, kind, nil)
	e.Code = code
	return e
}
