package worker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/promote"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/engine/nar"
)

type memArtifacts struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (a *memArtifacts) Get(_ context.Context, key, _, _ string) (*cache.Object, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.m[key]
	if !ok {
		return nil, cache.ErrNotFound
	}
	return &cache.Object{Body: io.NopCloser(bytes.NewReader(b)), Length: int64(len(b))}, nil
}

func (a *memArtifacts) Put(_ context.Context, key string, body []byte, _ string, _ cache.Cond) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m[key] = bytes.Clone(body)
	return `"e"`, nil
}

func (a *memArtifacts) PutFile(_ context.Context, key string, r io.ReadSeeker, size int64, _ string) error {
	b := make([]byte, size)
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m[key] = b
	return nil
}

const testPath = "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-hello"

func TestStageWritesPromotableStaging(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "store", "store", strings.TrimPrefix(testPath, "/nix/store/"))
	if err := os.MkdirAll(filepath.Join(host, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(host, "bin", "hello"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum, size, err := nar.Hash(host, nil)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(filepath.Join(work, workOut), 0o755); err != nil {
		t.Fatal(err)
	}
	res := RunResult{Failed: nil, Paths: []PathInfo{{Path: testPath, NarHash: "sha256:" + cache.Nix32(sum[:]), NarSize: uint64(size)}}}
	b, _ := json.Marshal(res)
	_ = os.WriteFile(filepath.Join(work, workOut, resultFile), b, 0o644)
	rep := []byte(`{"v":1}`)
	_ = os.WriteFile(filepath.Join(work, workOut, reportFile), rep, 0o644)

	art := &memArtifacts{m: map[string][]byte{}}
	p := &Process{store: filepath.Join(dir, "store")}
	p.UseArtifacts(art, "")
	spec := store.JobSpec{V: 1, JobID: "b.j0001", BuildID: "bid", Project: "acme", Staging: "staging/bid/b.j0001/"}
	o := Outcome{Status: "ok"}
	p.stage(context.Background(), Claim{JobID: "b.j0001", Attempt: 2}, spec, work, &o)
	staging := "staging/bid/b.j0001/2/"
	if o.Status != "ok" || o.Complete != staging+"COMPLETE.json" || o.EvalSHA != jobspec.SHA256(rep) {
		t.Fatalf("outcome %+v", o)
	}
	body := art.m[o.Complete]
	if h := sha256.Sum256(body); jobspec.SHA256(body) != o.CompSHA || h == [32]byte{} {
		t.Fatalf("CompSHA")
	}
	c, err := promote.ParseComplete(body, "b.j0001", "bid", "acme")
	if err != nil || len(c.Paths) != 1 || c.Paths[0] != testPath {
		t.Fatalf("COMPLETE.json %s: %v", body, err)
	}
	hash := cache.HashOf(testPath)
	ni, err := cache.ParseNarInfo(art.m[staging+"narinfo/"+hash+".narinfo"])
	if err != nil || ni.NarHash != res.Paths[0].NarHash || ni.NarSize != uint64(size) {
		t.Fatalf("claim %+v %v", ni, err)
	}
	zr, _ := zstd.NewReader(bytes.NewReader(art.m[staging+"nar/"+hash+".nar.zst"]))
	got, err := io.ReadAll(zr)
	if err != nil || sha256.Sum256(got) != sum {
		t.Fatalf("staged NAR does not hash to the claim: %v", err)
	}
	if !bytes.Equal(art.m[jobspec.ReportKey(staging)], rep) {
		t.Errorf("report not staged")
	}
}

// TestStageFailedJobStagesBuiltPaths: a failed job stages the closure of the packages that
// built (A200) and stays failed; a failed job without such paths stages no COMPLETE.json.
func TestStageFailedJobStagesBuiltPaths(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "store", "store", strings.TrimPrefix(testPath, "/nix/store/"))
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(host, "lib.a"), []byte("built before the failure\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, size, err := nar.Hash(host, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		paths []PathInfo
	}{
		{"with built packages", []PathInfo{{Path: testPath, NarHash: "sha256:" + cache.Nix32(sum[:]), NarSize: uint64(size)}}},
		{"nothing built", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := filepath.Join(t.TempDir(), "work")
			if err := os.MkdirAll(filepath.Join(work, workOut), 0o755); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(RunResult{Failed: []string{"//cmd/api:test"}, Paths: tc.paths,
				Outputs: []BuiltOutput{{Label: "//lib:lib", Path: testPath}}})
			_ = os.WriteFile(filepath.Join(work, workOut, resultFile), b, 0o644)
			_ = os.WriteFile(filepath.Join(work, workOut, reportFile), []byte(`{"v":1}`), 0o644)
			art := &memArtifacts{m: map[string][]byte{}}
			p := &Process{store: filepath.Join(dir, "store")}
			p.UseArtifacts(art, "")
			spec := store.JobSpec{V: 1, JobID: "b.j0001", BuildID: "bid", Project: "acme", Staging: "staging/bid/b.j0001/"}
			o := Outcome{Status: "failed", Error: "BOB-E-2005 test failed"}
			p.stage(context.Background(), Claim{JobID: "b.j0001", Attempt: 1}, spec, work, &o)
			if o.Status != "failed" || o.Error == "" || len(o.Failed) != 1 || o.EvalSHA == "" {
				t.Fatalf("outcome %+v", o)
			}
			staging := "staging/bid/b.j0001/1/"
			if tc.paths == nil {
				if o.Complete != "" || art.m[staging+"COMPLETE.json"] != nil {
					t.Fatalf("COMPLETE.json staged for a failed job that built nothing: %+v", o)
				}
				return
			}
			if o.Complete != staging+"COMPLETE.json" || jobspec.SHA256(art.m[o.Complete]) != o.CompSHA {
				t.Fatalf("outcome %+v", o)
			}
			c, err := promote.ParseComplete(art.m[o.Complete], "b.j0001", "bid", "acme")
			if err != nil || len(c.Paths) != 1 || c.Paths[0] != testPath || len(c.Images) != 0 || len(c.Assets) != 0 {
				t.Fatalf("COMPLETE.json %s: %v", art.m[o.Complete], err)
			}
			if art.m[staging+"nar/"+cache.HashOf(testPath)+".nar.zst"] == nil {
				t.Errorf("NAR not staged")
			}
		})
	}
}

func snapshot(t *testing.T, entries map[string]string) []byte {
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	for name, body := range entries {
		h := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if strings.HasPrefix(body, "->") {
			h = &tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: strings.TrimPrefix(body, "->")}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(body))
		}
	}
	_ = tw.Close()
	var zb bytes.Buffer
	zw, _ := zstd.NewWriter(&zb)
	_, _ = zw.Write(tb.Bytes())
	_ = zw.Close()
	return zb.Bytes()
}

func TestUnpackSnapshot(t *testing.T) {
	art := &memArtifacts{m: map[string][]byte{}}
	art.m["ok"] = snapshot(t, map[string]string{"go.mod": "module x\n", "a/b.go": "package a\n", "a/link": "->b.go"})
	dst := filepath.Join(t.TempDir(), "src")
	if err := unpackSnapshot(context.Background(), art, "ok", dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "a", "link")); err != nil || string(b) != "package a\n" {
		t.Fatalf("%q %v", b, err)
	}
	for name, entries := range map[string]map[string]string{
		"dotdot":   {"../x": "evil"},
		"abs-link": {"l": "->/etc/passwd"},
		"up-link":  {"a/l": "->../../x"},
	} {
		art.m[name] = snapshot(t, entries)
		err := unpackSnapshot(context.Background(), art, name, filepath.Join(t.TempDir(), "src"))
		var jf *jobFailed
		if !errors.As(err, &jf) || !strings.Contains(jf.msg, "source snapshot contains an unsafe path") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParsePathInfo(t *testing.T) {
	sri := "sha256-" + "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="
	arr := `[{"path":"` + testPath + `","narHash":"` + sri + `","narSize":3,"references":["` + testPath + `"]}]`
	obj := `{"` + testPath + `":{"narHash":"` + sri + `","narSize":3,"references":["0123456789abcdfghijklmnpqrsvwxyz-hello"]}}`
	for _, in := range []string{arr, obj} {
		got, err := parsePathInfo([]byte(in))
		if err != nil || len(got) != 1 || !strings.HasPrefix(got[0].NarHash, "sha256:") || len(got[0].NarHash) != 59 || got[0].References[0] != testPath {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
}
