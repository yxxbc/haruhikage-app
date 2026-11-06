package sourcehost

import (
	"errors"
	"net/url"
	"strings"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

const (
	MaxScriptBytes   = 2 << 20
	MaxMessageBytes  = 16 << 20 // JSON escaping can expand a script by six times.
	MaxResponseBytes = 1 << 20
)

type Support struct {
	Name     string   `json:"name"`
	Actions  []string `json:"actions"`
	Qualitys []string `json:"qualitys"`
}

type Capabilities struct {
	Status  bool               `json:"status"`
	Sources map[string]Support `json:"sources"`
}

// Error contains only a host-defined category. Never expose JS exceptions, HTTP
// errors, request headers or URLs: any of those may contain script credentials.
type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrInit        Error = "source initialization failed"
	ErrRejected    Error = "source rejected resolution"
	ErrUnsupported Error = "source does not support provider or quality"
	ErrIdentity    Error = "invalid playback identity"
	ErrURL         Error = "source returned an invalid playback URL"
	ErrClosed      Error = "source host closed"
	ErrLimit       Error = "source host resource limit reached"
	ErrProtocol    Error = "invalid source host message"
	ErrChanged     Error = "active source changed"
)

// Request/Response are private pipe protocol data, not persistence models.
// IDs are per worker; CancelID only refers to an outstanding call in that worker.
type Request struct {
	ID        uint64       `json:"id"`
	Method    string       `json:"method"`
	Script    string       `json:"script,omitempty"`
	Track     domain.Track `json:"track,omitempty"`
	Quality   string       `json:"quality,omitempty"`
	CancelID  uint64       `json:"cancelId,omitempty"`
	TimeoutMS int64        `json:"timeoutMs,omitempty"`
}

type Response struct {
	ID     uint64        `json:"id"`
	Error  string        `json:"error,omitempty"`
	Caps   *Capabilities `json:"caps,omitempty"`
	Stream *StreamWire   `json:"stream,omitempty"`
}

// StreamWire deliberately does not embed domain.Stream: its URL has json:"-".
// No StreamWire is written to an index, session, cache metadata, or a log.
type StreamWire struct {
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

func (s StreamWire) Domain() *domain.Stream { return &domain.Stream{URL: s.URL, Size: s.Size} }

func ValidURL(value string) bool {
	if len(value) == 0 || len(value) > 64<<10 || strings.ContainsAny(value, " \t\r\n\x00") {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

func CloneCapabilities(c Capabilities) Capabilities {
	out := Capabilities{Status: c.Status, Sources: make(map[string]Support, len(c.Sources))}
	for key, s := range c.Sources {
		s.Actions = append([]string(nil), s.Actions...)
		s.Qualitys = append([]string(nil), s.Qualitys...)
		out.Sources[key] = s
	}
	return out
}

func ValidateCapabilities(c Capabilities) error {
	if !c.Status || len(c.Sources) == 0 || len(c.Sources) > 32 {
		return ErrInit
	}
	for id, s := range c.Sources {
		if len(id) == 0 || len(id) > 32 || len(s.Name) > 256 || len(s.Actions) == 0 || len(s.Actions) > 16 || len(s.Qualitys) == 0 || len(s.Qualitys) > 64 {
			return ErrInit
		}
		if !contains(s.Actions, "musicUrl") {
			return ErrInit
		}
		for _, item := range append(append([]string(nil), s.Actions...), s.Qualitys...) {
			if item == "" || len(item) > 64 {
				return ErrInit
			}
		}
	}
	return nil
}

func contains(items []string, item string) bool {
	for _, v := range items {
		if v == item {
			return true
		}
	}
	return false
}

func errorCode(err error) string {
	var safe Error
	if errors.As(err, &safe) {
		return string(safe)
	}
	return "source operation failed"
}
