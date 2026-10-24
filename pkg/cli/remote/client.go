// Package remote is the bob CLI's client of a bob server: the Connect protocol (JSON codec)
// over plain HTTP, the OAuth flows of `bob login` (SPEC §06 §6.3.2), the source upload
// (§6.9.7) and the remote executor of `bob build --remote` (engine.RemoteExecutor, §02.6).
//
// It speaks the public wire protocol only. The framework never imports server code
// (R-ARCH-002), so the generated server stubs are not used here; the few messages the CLI
// needs are mirrored as JSON structs (proto3 JSON names).
package remote

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxMessage bounds one response message (hostile input, AGENTS.md §5.4).
const maxMessage = 16 << 20

// Server is one bob server as seen by the CLI.
type Server struct {
	URL       string // base URL without a trailing slash: https://bob.example.com
	Token     string // bearer token ("" = anonymous)
	UserAgent string
	HTTP      *http.Client // nil: a client with a 30 s timeout (unary) and none for streams
}

// Host is the server's host name (the {server} of the error templates).
func (s *Server) Host() string { return Host(s.URL) }

// Host returns the host[:port] of a server URL, or the URL itself when it does not parse.
func Host(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return u
	}
	return p.Host
}

// Error is a failed call: a Connect error (Code set), an HTTP error, or no answer at all
// (Transport set).
type Error struct {
	Method    string
	Status    int    // HTTP status, 0 when the server did not answer
	Code      string // Connect code: unauthenticated, permission_denied, not_found, …
	Message   string
	Transport error
}

func (e *Error) Error() string {
	if e.Transport != nil {
		return e.Method + ": " + e.Transport.Error()
	}
	if e.Message != "" {
		return fmt.Sprintf("%s: %s: %s", e.Method, e.Code, e.Message)
	}
	return fmt.Sprintf("%s: HTTP %d", e.Method, e.Status)
}

func (e *Error) Unwrap() error { return e.Transport }

// Unreachable reports whether the server did not answer or said it is unavailable (BOB-E-4009).
func (e *Error) Unreachable() bool {
	return e.Transport != nil || e.Code == "unavailable" || e.Code == "resource_exhausted" ||
		e.Status == http.StatusBadGateway || e.Status == http.StatusServiceUnavailable || e.Status == http.StatusGatewayTimeout
}

// codeOf maps an HTTP status to the Connect code when the body carried none.
func codeOf(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_argument"
	case http.StatusUnauthorized:
		return "unauthenticated"
	case http.StatusForbidden:
		return "permission_denied"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "aborted"
	case http.StatusTooManyRequests:
		return "resource_exhausted"
	case http.StatusNotImplemented:
		return "unimplemented"
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "unavailable"
	}
	return "unknown"
}

// AsError returns err as an *Error, if it is one.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

func (s *Server) client(stream bool) *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	if stream {
		return &http.Client{}
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *Server) header(h http.Header, contentType string) {
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	h.Set("Connect-Protocol-Version", "1")
	if s.Token != "" {
		h.Set("Authorization", "Bearer "+s.Token)
	}
	if s.UserAgent != "" {
		h.Set("User-Agent", s.UserAgent)
	}
}

// Call makes one unary Connect call (POST, JSON codec). method is "<package.Service>/<Method>".
func (s *Server) Call(ctx context.Context, method string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+"/api/"+method, bytes.NewReader(body))
	if err != nil {
		return &Error{Method: method, Transport: err}
	}
	s.header(hr.Header, "application/json")
	return s.do(hr, method, resp)
}

// Get makes one unary Connect GET call (methods with idempotency NO_SIDE_EFFECTS).
func (s *Server) Get(ctx context.Context, method string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	q := url.Values{"connect": {"v1"}, "encoding": {"json"}, "message": {string(body)}}
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/api/"+method+"?"+q.Encode(), nil)
	if err != nil {
		return &Error{Method: method, Transport: err}
	}
	s.header(hr.Header, "")
	return s.do(hr, method, resp)
}

func (s *Server) do(hr *http.Request, method string, resp any) error {
	r, err := s.client(false).Do(hr)
	if err != nil {
		return &Error{Method: method, Transport: err}
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, maxMessage+1))
	if err != nil {
		return &Error{Method: method, Transport: err}
	}
	if len(b) > maxMessage {
		return &Error{Method: method, Status: r.StatusCode, Code: "resource_exhausted", Message: "response too large"}
	}
	if r.StatusCode != http.StatusOK {
		return httpError(method, r.StatusCode, b)
	}
	if resp == nil {
		return nil
	}
	if err := json.Unmarshal(b, resp); err != nil {
		return &Error{Method: method, Status: r.StatusCode, Code: "internal", Message: "invalid response: " + err.Error()}
	}
	return nil
}

// httpError decodes a Connect error body ({"code","message"}); other bodies give the code of
// the HTTP status.
func httpError(method string, status int, body []byte) *Error {
	var ce struct{ Code, Message string }
	e := &Error{Method: method, Status: status}
	if json.Unmarshal(body, &ce) == nil && ce.Code != "" {
		e.Code, e.Message = ce.Code, ce.Message
		return e
	}
	e.Code = codeOf(status)
	if t := strings.TrimSpace(string(body)); t != "" && len(t) < 300 && !strings.ContainsAny(t, "\n<") {
		e.Message = t
	}
	return e
}

// Stream makes one server-streaming Connect call and hands every message to fn until the
// stream ends. An error in the end-of-stream message is returned as *Error.
func (s *Server) Stream(ctx context.Context, method string, req any, fn func(json.RawMessage) error) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	env := make([]byte, 5, 5+len(body))
	binary.BigEndian.PutUint32(env[1:], uint32(len(body)))
	env = append(env, body...)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+"/api/"+method, bytes.NewReader(env))
	if err != nil {
		return &Error{Method: method, Transport: err}
	}
	s.header(hr.Header, "application/connect+json")
	r, err := s.client(true).Do(hr)
	if err != nil {
		return &Error{Method: method, Transport: err}
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		return httpError(method, r.StatusCode, b)
	}
	var head [5]byte
	for {
		if _, err := io.ReadFull(r.Body, head[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return &Error{Method: method, Transport: errors.New("stream ended without an end message")}
			}
			return &Error{Method: method, Transport: err}
		}
		n := binary.BigEndian.Uint32(head[1:])
		if n > maxMessage {
			return &Error{Method: method, Code: "resource_exhausted", Message: "stream message too large"}
		}
		msg := make([]byte, n)
		if _, err := io.ReadFull(r.Body, msg); err != nil {
			return &Error{Method: method, Transport: err}
		}
		if head[0]&0x02 != 0 { // end of stream
			var end struct {
				Error *struct{ Code, Message string } `json:"error"`
			}
			if json.Unmarshal(msg, &end) == nil && end.Error != nil {
				return &Error{Method: method, Status: http.StatusOK, Code: end.Error.Code, Message: end.Error.Message}
			}
			return nil
		}
		if head[0]&0x01 != 0 {
			return &Error{Method: method, Code: "internal", Message: "compressed stream messages are not supported"}
		}
		if err := fn(msg); err != nil {
			return err
		}
	}
}

// Int64 is a proto3 JSON int64: a string on the wire, a number from tolerant servers.
type Int64 int64

// UnmarshalJSON accepts "123" and 123.
func (i *Int64) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(bytes.Trim(b, `"`), &n); err != nil {
		return err
	}
	v, err := n.Int64()
	*i = Int64(v)
	return err
}
