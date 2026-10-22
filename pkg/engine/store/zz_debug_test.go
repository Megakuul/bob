package store

import (
	"bytes"
	"io"
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

func TestZZDebugConcurrent(t *testing.T) {
	home := filepath.Join(t.TempDir(), "bobhome")
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			s, err := Open(context.Background(), Config{Home: home})
			if err != nil {
				errs <- err
				return
			}
			nar := flatNAR([]byte{byte(i), 'x'})
			p := sourcePathForTest(t, sha256.Sum256(nar), "concurrent")
			shared := flatNAR([]byte("shared"))
			q := sourcePathForTest(t, sha256.Sum256(shared), "shared")
			var buf bytes.Buffer
			if err := WriteExport(&buf, []ExportItem{{Path: p, NAR: nar}, {Path: q, NAR: shared}}); err != nil {
				errs <- err
				return
			}
			b := buf.Bytes()
			_ = b
			_ = io.EOF
			err = s.Import(context.Background(), []ExportItem{{Path: p, NAR: nar}, {Path: q, NAR: shared}})
			errs <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			var de *diag.Error
			errors.As(err, &de)
			t.Errorf("%d: %v raw=%q", i, err, de.Raw)
		}
	}
}
