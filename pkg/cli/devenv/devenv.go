package devenv

import (
	"context"
	"io"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Hook returns the shell hook script of SPEC §02.7.1 for shell ("bash", "zsh" or "fish");
// bobPath is the absolute path of the running bob that the script embeds.
func Hook(shell, bobPath string) (string, error) {
	return "", diag.NotImplemented("bob hook " + shell)
}

// Rewrite rewrites the store paths of one LSP message body (SPEC §02.7.4 rules 3 to 6): every
// string value and object key has from+"/" replaced by to+"/", as a file URI (with URI
// percent-encoding of the paths) and as a bare path at a boundary. A body that contains no
// pattern is returned byte for byte; a body that is not valid JSON is returned unchanged.
func Rewrite(body []byte, from, to string) ([]byte, error) {
	return nil, diag.NotImplemented("LSP proxy rewrite")
}

// Pump copies LSP frames from r to w (SPEC §02.7.4 rule 2): the output frame has only a
// Content-Length header, and message order is kept. Bodies go through Rewrite(from, to).
func Pump(ctx context.Context, r io.Reader, w io.Writer, from, to string) error {
	return diag.NotImplemented("LSP proxy")
}
