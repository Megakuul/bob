package authenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Client calls bob's connect API as one principal. A zero credential is anonymous.
type Client struct {
	Server *Server
	Base   string // defaults to Server.URL
	Token  string // sent as Authorization: Bearer
	Cookie string // bob_session value; sent with Bob-Csrf: 1
	NoCSRF bool   // omit Bob-Csrf even with a cookie (R-AUTH-151 case 3)
	Header http.Header
	HTTP   *http.Client
}

// Reply is one answered call.
type Reply struct {
	HTTP    int
	Code    string // connect code: "not_found", "permission_denied", … ("" = OK)
	Message string
	Detail  string // ErrorDetail.code, e.g. BOB-API-3001
	Details int    // number of error details attached (SPEC §6.1: exactly one ErrorDetail)
	Field   string // ErrorDetail.field when the server's JSON carries the decoded detail
	Body    map[string]any
	Raw     []byte
	Header  http.Header
}

// OK reports a successful call.
func (r *Reply) OK() bool { return r.HTTP == 200 && r.Code == "" }

func (r *Reply) String() string {
	if r.OK() {
		return fmt.Sprintf("ok %s", r.Raw)
	}
	return fmt.Sprintf("http %d %s %q (%s)", r.HTTP, r.Code, r.Message, r.Detail)
}

// Admin returns a client holding the bootstrap admin token.
func (s *Server) Admin() *Client { return &Client{Server: s, Token: s.AdminToken} }

// Anon returns an anonymous client.
func (s *Server) Anon() *Client { return &Client{Server: s} }

// WithToken returns an API client for token.
func (s *Server) WithToken(token string) *Client { return &Client{Server: s, Token: token} }

func (c *Client) base() string {
	if c.Base != "" {
		return c.Base
	}
	return c.Server.URL
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return HTTPClient()
}

func (c *Client) apply(req *http.Request) {
	for k, v := range c.Header {
		req.Header[k] = v
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Cookie != "" {
		req.AddCookie(&http.Cookie{Name: "bob_session", Value: c.Cookie})
		if !c.NoCSRF {
			req.Header.Set("Bob-Csrf", "1")
		}
	}
}

// Call posts req (any JSON-marshalable value, protojson field names) to /api/<service>/<method>
// and decodes the answer. service is "bob.build.v1.BuildService". Fatal on transport errors.
func (c *Client) Call(t testing.TB, service, method string, req any) *Reply {
	t.Helper()
	r, err := c.Do(service, method, req, false)
	if err != nil {
		t.Fatalf("harness error: %s/%s: %v", service, method, err)
	}
	return r
}

// CallStream calls a streaming method with the Connect envelope protocol and returns the first
// message or the error of the stream.
func (c *Client) CallStream(t testing.TB, service, method string, req any) *Reply {
	t.Helper()
	r, err := c.Do(service, method, req, true)
	if err != nil {
		t.Fatalf("harness error: %s/%s: %v", service, method, err)
	}
	return r
}

// Do is Call without the testing hook.
func (c *Client) Do(service, method string, req any, stream bool) (*Reply, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ct := "application/json"
	if stream {
		ct = "application/connect+json"
		env := make([]byte, 5, 5+len(body))
		binary.BigEndian.PutUint32(env[1:], uint32(len(body)))
		body = append(env, body...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/api/"+service+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", ct)
	hr.Header.Set("Connect-Protocol-Version", "1")
	c.apply(hr)
	resp, err := c.http().Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil && !stream {
		return nil, err
	}
	return parseReply(resp, raw, stream), nil
}

func parseReply(resp *http.Response, raw []byte, stream bool) *Reply {
	r := &Reply{HTTP: resp.StatusCode, Raw: raw, Header: resp.Header}
	payload := raw
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/connect+") {
		// Envelopes: flag byte, 4 byte length, payload. Flag bit 1 = end of stream.
		payload = nil
		for len(raw) >= 5 {
			flag, n := raw[0], int(binary.BigEndian.Uint32(raw[1:5]))
			if 5+n > len(raw) {
				break
			}
			msg := raw[5 : 5+n]
			raw = raw[5+n:]
			if flag&2 != 0 {
				var end struct {
					Error json.RawMessage `json:"error"`
				}
				if json.Unmarshal(msg, &end) == nil && len(end.Error) > 0 {
					payload = end.Error
					r.fill(payload, true)
					return r
				}
				continue
			}
			if payload == nil {
				payload = msg
			}
		}
		if payload != nil {
			r.fill(payload, false)
		}
		return r
	}
	r.fill(payload, resp.StatusCode != 200)
	return r
}

func (r *Reply) fill(payload []byte, isErr bool) {
	var m map[string]any
	_ = json.Unmarshal(payload, &m)
	if !isErr {
		r.Body = m
		return
	}
	r.Code, _ = m["code"].(string)
	r.Message, _ = m["message"].(string)
	if r.Code == "" {
		r.Code = fmt.Sprintf("http_%d", r.HTTP)
	}
	details, _ := m["details"].([]any)
	r.Details = len(details)
	for _, d := range details {
		dm, _ := d.(map[string]any)
		if dbg, ok := dm["debug"].(map[string]any); ok {
			if c, ok := dbg["code"].(string); ok && r.Detail == "" {
				r.Detail = c
			}
			if f, ok := dbg["field"].(string); ok && r.Field == "" {
				r.Field = f
			}
			continue
		}
		if v, ok := dm["value"].(string); ok && r.Detail == "" {
			if b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(v, "=")); err == nil {
				r.Detail = errorDetailCode(b)
			}
		}
	}
}

// errorDetailCode reads field 1 (string) of a serialized bob.common.v1.ErrorDetail.
func errorDetailCode(b []byte) string {
	if len(b) < 2 || b[0] != 0x0a {
		return ""
	}
	n, k := binary.Uvarint(b[1:])
	if k <= 0 || 1+k+int(n) > len(b) {
		return ""
	}
	return string(b[1+k : 1+k+int(n)])
}

// Get fetches an HTTP path (not an RPC) with the client's credential.
func (c *Client) Get(t testing.TB, path string, hdr map[string]string) (int, http.Header, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.apply(req)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		t.Fatalf("harness error: GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, resp.Header, b
}

// BasicGet fetches path with HTTP Basic credentials (netrc style).
func (c *Client) BasicGet(t testing.TB, path, user, password string) (int, http.Header, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, password)
	resp, err := c.http().Do(req)
	if err != nil {
		t.Fatalf("harness error: GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, resp.Header, b
}

// Str digs a string out of a decoded JSON body by a dotted path ("user.id").
func Str(m map[string]any, path string) string {
	var cur any = m
	for _, p := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[p]
	}
	s, _ := cur.(string)
	return s
}

// List digs a JSON array out of a decoded body.
func List(m map[string]any, key string) []any {
	l, _ := m[key].([]any)
	return l
}
