package sourcehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dop251/goja"
)

const maxHTTPBody = 8 << 20

type requestOptions struct {
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
	JSON    json.RawMessage   `json:"json"`
	Form    map[string]string `json:"form"`
	Timeout int64             `json:"timeout"`
}

type httpResult struct {
	status  int
	headers map[string]string
	body    []byte
	err     error
}

func (r *Runtime) request(call goja.FunctionCall) goja.Value {
	vm := r.vm
	cb, ok := goja.AssertFunction(call.Argument(2))
	if !ok {
		panic(vm.NewTypeError("lx.request callback required"))
	}
	if len(r.httpCancels) >= maxHTTP {
		panic(vm.NewTypeError(string(ErrLimit)))
	}
	rawURL := call.Argument(0).String()
	var opts requestOptions
	data, err := json.Marshal(call.Argument(1).Export())
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &opts) != nil {
		panic(vm.NewTypeError("invalid HTTP options"))
	}
	method := strings.ToUpper(opts.Method)
	if method == "" {
		method = http.MethodGet
	}
	var body []byte
	if len(opts.Body) > 0 && string(opts.Body) != "null" {
		var text string
		if json.Unmarshal(opts.Body, &text) == nil {
			body = []byte(text)
		} else {
			body = opts.Body
		}
	}
	parseJSON := string(opts.JSON) == "true"
	if len(opts.JSON) > 0 && string(opts.JSON) != "false" && string(opts.JSON) != "true" && string(opts.JSON) != "null" {
		body = opts.JSON
		if opts.Headers == nil {
			opts.Headers = make(map[string]string)
		}
		opts.Headers["Content-Type"] = "application/json"
	}
	if opts.Form != nil {
		form := make(url.Values, len(opts.Form))
		for k, v := range opts.Form {
			form.Set(k, v)
		}
		body = []byte(form.Encode())
		if opts.Headers == nil {
			opts.Headers = make(map[string]string)
		}
		opts.Headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	if len(body) > 1<<20 {
		panic(vm.NewTypeError("HTTP body exceeds limit"))
	}
	timeout := 10 * time.Second
	if opts.Timeout > 0 && opts.Timeout < int64(timeout/time.Millisecond) {
		timeout = time.Duration(opts.Timeout) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(r.ctx, timeout)
	r.nextHTTP++
	id := r.nextHTTP
	r.httpCancels[id] = cancel
	// These goroutines never inspect cb or VM values. They only capture cb for
	// a future event-loop closure; response conversion is on that loop.
	go func() {
		defer cancel()
		result := httpResult{}
		if !ValidURL(rawURL) {
			result.err = errors.New("request URL denied")
		} else {
			req, e := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
			if e != nil {
				result.err = e
			} else {
				for k, v := range opts.Headers {
					req.Header.Set(k, v)
				}
				resp, e := r.client.Do(req)
				if e != nil {
					result.err = e
				} else {
					result.status = resp.StatusCode
					result.headers = make(map[string]string, len(resp.Header))
					for k, values := range resp.Header {
						result.headers[strings.ToLower(k)] = strings.Join(values, ", ")
					}
					result.body, result.err = io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
					resp.Body.Close()
					if len(result.body) > maxHTTPBody {
						result.body = nil
						result.err = errors.New("response exceeds limit")
					}
				}
			}
		}
		r.post(func() error {
			if _, live := r.httpCancels[id]; !live {
				return nil
			}
			delete(r.httpCancels, id)
			if result.err != nil {
				message := "network request failed"
				errorValue := vm.NewGoError(errors.New(message))
				if errors.Is(result.err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					_ = errorValue.Set("name", "TimeoutError")
					_ = errorValue.Set("code", "ETIMEDOUT")
				}
				_, e := cb(goja.Undefined(), errorValue, goja.Null(), goja.Null())
				return e
			}
			var responseBody any = string(result.body)
			if parseJSON {
				var v any
				if json.Unmarshal(result.body, &v) == nil {
					responseBody = v
				}
			}
			bodyValue := vm.ToValue(responseBody)
			response := map[string]any{"statusCode": result.status, "status": result.status, "headers": result.headers, "body": bodyValue}
			_, e := cb(goja.Undefined(), goja.Null(), vm.ToValue(response), bodyValue)
			return e
		})
	}()
	// LX returns a callable transport-cancel handle, not an AbortController.
	return vm.ToValue(func() { cancel() })
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// Production scripts cannot probe the local network. A deliberately injected
// client is the explicit opt-in for local HTTP/test transports. No shell, file,
// environment or other application capability is exposed to JS.
func publicTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid HTTP address")
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, errors.New("HTTP DNS failed")
		}
		var last error
		for _, ip := range ips {
			if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() {
				return nil, errors.New("local HTTP address denied")
			}
		}
		for _, ip := range ips {
			conn, e := (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		if last == nil {
			last = errors.New("HTTP DNS returned no addresses")
		}
		return nil, last
	}
	return t
}
