package devenv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
)

// Rewrite rewrites the store paths of one LSP message body (SPEC §02.7.4 rules 3 to 6): every
// string value and object key has from+"/" replaced by to+"/", as a file URI (with URI
// percent-encoding of the paths) and as a bare path at a boundary. A body that contains no
// pattern is returned byte for byte; a body that is not valid JSON is returned unchanged.
func Rewrite(body []byte, from, to string) ([]byte, error) {
	r := newRewriter(from, to)
	if !r.mayMatch(body) {
		return body, nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return body, nil
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return body, nil // trailing data: not one JSON value
	}
	out, changed := r.value(v)
	if !changed {
		return body, nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return body, nil
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

type rewriter struct {
	from, to         string // "<path>/"
	uriFrom, uriTo   string // "file://<esc(path)>/"
	escFrom, jsonEsc string // fast-path forms: percent-encoded and JSON-escaped from
}

func newRewriter(from, to string) *rewriter {
	from, to = strings.TrimSuffix(from, "/"), strings.TrimSuffix(to, "/")
	esc := func(p string) string { return (&url.URL{Path: p}).EscapedPath() }
	return &rewriter{
		from: from + "/", to: to + "/",
		uriFrom: "file://" + esc(from) + "/", uriTo: "file://" + esc(to) + "/",
		escFrom: esc(from) + "/", jsonEsc: strings.ReplaceAll(from+"/", "/", `\/`),
	}
}

// mayMatch is the fast path of rule 3: no form of from appears in the body.
func (r *rewriter) mayMatch(body []byte) bool {
	return bytes.Contains(body, []byte(r.from)) || bytes.Contains(body, []byte(r.jsonEsc)) ||
		bytes.Contains(body, []byte(r.escFrom))
}

func (r *rewriter) value(v any) (any, bool) {
	switch x := v.(type) {
	case string:
		s := r.str(x)
		return s, s != x
	case []any:
		changed := false
		for i, e := range x {
			var c bool
			x[i], c = r.value(e)
			changed = changed || c
		}
		return x, changed
	case map[string]any:
		changed := false
		out := make(map[string]any, len(x))
		for k, e := range x {
			nk := r.str(k)
			ne, c := r.value(e)
			changed = changed || c || nk != k
			out[nk] = ne
		}
		return out, changed
	}
	return v, false
}

// str is rule 5: one left-to-right pass; replaced text is never rescanned.
func (r *rewriter) str(s string) string {
	if !strings.Contains(s, r.from) && !strings.Contains(s, r.uriFrom) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], r.uriFrom):
			b.WriteString(r.uriTo)
			i += len(r.uriFrom)
		case strings.HasPrefix(s[i:], r.from) && (i == 0 || !pathByte(s[i-1])):
			b.WriteString(r.to)
			i += len(r.from)
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// pathByte reports whether c may continue a path: a bare store path preceded by one is not at
// a boundary (the host store "…/share/bob/nix/store/" must never be rewritten again).
func pathByte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("/._+~-", c) >= 0
}

// maxFrame bounds one LSP message body and its header block.
const (
	maxFrame  = 256 << 20
	maxHeader = 64 << 10
)

// Pump copies LSP frames from r to w (SPEC §02.7.4 rule 2): the output frame has only a
// Content-Length header, and message order is kept. Bodies go through Rewrite(from, to). It
// returns nil at the end of r between frames.
func Pump(ctx context.Context, r io.Reader, w io.Writer, from, to string) error {
	br := bufio.NewReaderSize(r, 64<<10)
	tp := textproto.NewReader(br)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := readHeader(tp)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(br, body); err != nil {
			return fmt.Errorf("lsp: short body: %w", err)
		}
		out, err := Rewrite(body, from, to)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(out)); err != nil {
			return err
		}
		if _, err := w.Write(out); err != nil {
			return err
		}
		if f, ok := w.(interface{ Flush() error }); ok {
			if err := f.Flush(); err != nil {
				return err
			}
		}
	}
}

// readHeader reads one header block and returns Content-Length.
func readHeader(tp *textproto.Reader) (int, error) {
	n, total := -1, 0
	for first := true; ; first = false {
		line, err := tp.ReadLine()
		if err != nil {
			if first && errors.Is(err, io.EOF) && line == "" {
				return 0, io.EOF
			}
			return 0, fmt.Errorf("lsp: header: %w", err)
		}
		total += len(line)
		if total > maxHeader {
			return 0, errors.New("lsp: header too large")
		}
		if line == "" {
			if first {
				continue // tolerate a stray blank line between frames
			}
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			x, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || x < 0 || x > maxFrame {
				return 0, fmt.Errorf("lsp: bad Content-Length %q", v)
			}
			n = x
		}
	}
	if n < 0 {
		return 0, errors.New("lsp: frame without Content-Length")
	}
	return n, nil
}
