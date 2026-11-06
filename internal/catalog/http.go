package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxResponse = 8 << 20
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36"

// scalar reads decimal IDs directly from JSON tokens, never through float64.
// Platforms send both JSON numbers and strings, including IDs beyond 2^53.
type scalar string

func (s *scalar) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		*s = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var text string
		if err := json.Unmarshal(b, &text); err != nil {
			return err
		}
		*s = scalar(text)
		return nil
	}
	for i, c := range b {
		if (c < '0' || c > '9') && !(i == 0 && c == '-') {
			return errors.New("expected string or decimal integer")
		}
	}
	if len(b) == 0 || string(b) == "-" {
		return errors.New("empty integer")
	}
	*s = scalar(b)
	return nil
}
func (s scalar) int64() int64 { n, _ := strconv.ParseInt(string(s), 10, 64); return n }
func count(s scalar) (int, error) {
	n, err := strconv.Atoi(string(s))
	if err != nil || n < 0 {
		return 0, errors.New("invalid count")
	}
	return n, nil
}
func decimalID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func requestURL(base string, query url.Values) string {
	if len(query) == 0 {
		return base
	}
	return base + "?" + query.Encode()
}
func (s *Service) get(ctx context.Context, p, op, base string, q url.Values, headers http.Header) ([]byte, error) {
	return s.request(ctx, p, op, http.MethodGet, requestURL(base, q), nil, headers)
}
func (s *Service) postJSON(ctx context.Context, p, op, endpoint string, data any, headers http.Header) ([]byte, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, failure(p, op, InvalidRequest, err)
	}
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "application/json")
	return s.request(ctx, p, op, http.MethodPost, endpoint, body, headers)
}
func (s *Service) request(ctx context.Context, p, op, method, endpoint string, body []byte, headers http.Header) ([]byte, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return nil, failure(p, op, Protocol, nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, failure(p, op, InvalidRequest, nil)
	}
	req.Header.Set("User-Agent", userAgent)
	for k, values := range headers {
		if strings.EqualFold(k, "Cookie") {
			continue
		}
		req.Header[k] = append([]string(nil), values...)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, transportError(p, op, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		kind := HTTP
		if res.StatusCode == http.StatusTooManyRequests {
			kind = RateLimited
		} else if res.StatusCode == http.StatusNotFound {
			kind = NotFound
		}
		e := failure(p, op, kind, nil)
		e.StatusCode = res.StatusCode
		return nil, e
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil {
		return nil, transportError(p, op, err)
	}
	if len(b) > maxResponse {
		return nil, failure(p, op, Protocol, errors.New("response exceeds limit"))
	}
	return b, nil
}
func decode(p, op string, body []byte, out any) error {
	if !utf8.Valid(body) || json.Unmarshal(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}), out) != nil {
		return failure(p, op, Protocol, nil)
	}
	return nil
}
func secondsMS(n scalar) int64 {
	// Catalogue durations are seconds; don't allow an untrusted value to wrap.
	v := n.int64()
	if v < 0 || v > 7*24*3600 {
		return 0
	}
	return v * 1000
}
