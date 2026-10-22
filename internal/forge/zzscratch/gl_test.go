package zzscratch

import (
	"context"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv/gitlab"
)

func TestGL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	gl, err := gitlab.Start(ctx, gitlab.Options{Dir: "/var/tmp/tmp-I-B10/gl"})
	if err != nil {
		t.Fatal(err)
	}
	_ = gl.Stop()
}
