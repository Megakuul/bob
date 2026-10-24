// Command bob-gocacheprog is a GOCACHEPROG backed by Nix store paths (spike S2, variant V3).
//
// Read-only cache layers come from BOB_CACHE_IN (colon-separated output paths of dependency
// derivations); new entries go to BOB_CACHE_OUT (this derivation's output). Layout per layer:
//
//	a/<actionID hex>  -> "<outputID hex> <size>\n"
//	o/<outputID hex>  -> body
//
// Protocol: cmd/go/internal/cacheprog (Go 1.24+). Requests are handled one at a time.
package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type request struct {
	ID       int64
	Command  string
	ActionID []byte `json:",omitempty"`
	OutputID []byte `json:",omitempty"`
	BodySize int64  `json:",omitempty"`
}

type response struct {
	ID            int64
	Err           string     `json:",omitempty"`
	KnownCommands []string   `json:",omitempty"`
	Miss          bool       `json:",omitempty"`
	OutputID      []byte     `json:",omitempty"`
	Size          int64      `json:",omitempty"`
	Time          *time.Time `json:",omitempty"`
	DiskPath      string     `json:",omitempty"`
}

var epoch = time.Unix(1, 0).UTC()

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bob-gocacheprog:", err)
		os.Exit(1)
	}
}

func run() error {
	out := os.Getenv("BOB_CACHE_OUT")
	var layers []string
	if out != "" {
		layers = append(layers, out)
		for _, d := range []string{"a", "o"} {
			if err := os.MkdirAll(filepath.Join(out, d), 0o755); err != nil {
				return err
			}
		}
	}
	for _, l := range strings.Split(os.Getenv("BOB_CACHE_IN"), ":") {
		if l != "" {
			layers = append(layers, l)
		}
	}
	w := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(w)
	send := func(r response) error {
		if err := enc.Encode(r); err != nil {
			return err
		}
		return w.Flush()
	}
	if err := send(response{KnownCommands: []string{"get", "put", "close"}}); err != nil {
		return err
	}
	dec := json.NewDecoder(bufio.NewReader(os.Stdin))
	hits, misses, puts := 0, 0, 0
	for {
		var req request
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var body []byte
		if req.Command == "put" && req.BodySize > 0 {
			if err := dec.Decode(&body); err != nil {
				return fmt.Errorf("put body: %w", err)
			}
		}
		res := response{ID: req.ID}
		switch req.Command {
		case "get":
			if p, oid, size, ok := lookup(layers, hex.EncodeToString(req.ActionID)); ok {
				hits++
				t := epoch
				res.OutputID, res.Size, res.DiskPath, res.Time = oid, size, p, &t
			} else {
				misses++
				res.Miss = true
			}
		case "put":
			puts++
			if out == "" {
				res.Err = "no BOB_CACHE_OUT"
				break
			}
			oh := hex.EncodeToString(req.OutputID)
			op := filepath.Join(out, "o", oh)
			if err := os.WriteFile(op, body, 0o644); err != nil {
				res.Err = err.Error()
				break
			}
			meta := fmt.Sprintf("%s %d\n", oh, len(body))
			if err := os.WriteFile(filepath.Join(out, "a", hex.EncodeToString(req.ActionID)), []byte(meta), 0o644); err != nil {
				res.Err = err.Error()
				break
			}
			res.DiskPath = op
		case "close":
			if os.Getenv("BOB_CACHE_STATS") != "" {
				fmt.Fprintf(os.Stderr, "bob-gocacheprog: hits=%d misses=%d puts=%d layers=%d\n", hits, misses, puts, len(layers))
			}
			return send(res)
		default:
			res.Err = "unknown command " + req.Command
		}
		if err := send(res); err != nil {
			return err
		}
	}
}

func lookup(layers []string, ah string) (string, []byte, int64, bool) {
	for _, l := range layers {
		b, err := os.ReadFile(filepath.Join(l, "a", ah))
		if err != nil {
			continue
		}
		f := strings.Fields(string(b))
		if len(f) != 2 {
			continue
		}
		oid, err1 := hex.DecodeString(f[0])
		size, err2 := strconv.ParseInt(f[1], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		return filepath.Join(l, "o", f[0]), oid, size, true
	}
	return "", nil, 0, false
}
