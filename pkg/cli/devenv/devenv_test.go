package devenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine"
)

// apply runs a bash-style script against env (export K='V'; unset K;) and returns the result.
func apply(t *testing.T, env map[string]string, script string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range env {
		out[k] = v
	}
	for _, l := range strings.Split(strings.TrimSpace(script), "\n") {
		l = strings.TrimSuffix(l, ";")
		switch {
		case strings.HasPrefix(l, "export "):
			k, v, _ := strings.Cut(strings.TrimPrefix(l, "export "), "=")
			out[k] = strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(v, "'"), "'"), `'\''`, "'")
		case strings.HasPrefix(l, "unset "):
			delete(out, strings.TrimPrefix(l, "unset "))
		case l == "":
		default:
			t.Fatalf("unexpected line %q", l)
		}
	}
	return out
}

func envList(m map[string]string) []string {
	var l []string
	for k, v := range m {
		l = append(l, k+"="+v)
	}
	return l
}

func TestEnterLeaveRestoresExactly(t *testing.T) {
	a := &File{Outside: Outside{Set: map[string]string{"CGO_ENABLED": "0", "ACME_ENV": "it's"}}}
	b := &File{Outside: Outside{Set: map[string]string{"B_ENV": "b"}}}
	for _, before := range []map[string]string{
		{"PATH": "/usr/bin:/bin", "HOME": "/h"},
		{"PATH": "/usr/bin:/bin", "CGO_ENABLED": "7", "HOME": "/h"},
		{"HOME": "/h"},
	} {
		s := NewScript("bash", envList(before))
		s.Enter("/p/a", a, "1111111111111111")
		inA := apply(t, before, s.String())
		if inA["CGO_ENABLED"] != "0" || inA["ACME_ENV"] != "it's" || !strings.HasPrefix(inA["PATH"], "/p/a/.bob/bin") || inA[VarProject] != "/p/a" {
			t.Fatalf("enter: %v", inA)
		}
		// switch to b, then leave b
		s = NewScript("bash", envList(inA))
		s.Leave()
		s.Enter("/p/a/b", b, "2222222222222222")
		inB := apply(t, inA, s.String())
		if _, ok := inB["ACME_ENV"]; ok || inB["B_ENV"] != "b" || strings.Contains(inB["PATH"], "/p/a/.bob/bin:") {
			t.Fatalf("switch: %v", inB)
		}
		s = NewScript("bash", envList(inB))
		s.Leave()
		after := apply(t, inB, s.String())
		if len(after) != len(before) {
			t.Fatalf("leave: %v, want %v", after, before)
		}
		for k, v := range before {
			if after[k] != v {
				t.Errorf("leave: %s = %q, want %q", k, after[k], v)
			}
		}
	}
}

func TestFishScript(t *testing.T) {
	s := NewScript("fish", []string{"PATH=/usr/bin:/bin"})
	s.Enter("/p/a", &File{Outside: Outside{Set: map[string]string{"X": `a'b\c`}}}, "h")
	got := s.String()
	for _, want := range []string{"set -gx PATH '/p/a/.bob/bin' '/usr/bin' '/bin';", `set -gx X 'a\'b\\c';`, "set -gx BOB_PROJECT '/p/a';"} {
		if !strings.Contains(got, want) {
			t.Errorf("fish script lacks %q:\n%s", want, got)
		}
	}
}

func TestHookUnknownShell(t *testing.T) {
	if _, err := Hook("tcsh", "/bin/bob"); err == nil {
		t.Fatal("tcsh accepted")
	}
	s, err := Hook("bash", "/opt/b$ob/bob")
	if err != nil || !strings.Contains(s, `"/opt/b\$ob/bob" env --hook=bash`) {
		t.Fatalf("bash hook: %v\n%s", err, s)
	}
}

func TestComposeOutsideRules(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "nix", "store", "aaa-go-1.26.8", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"go", "gopls", "not executable"} {
		if err := os.WriteFile(filepath.Join(bin, n), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string { return filepath.Join(dir, p) }
	f := Compose(Spec{Root: "/r", Shell: "default", Packages: []engine.DevPackage{{Name: "go", Path: "/nix/store/aaa-go-1.26.8", Drv: "/nix/store/x.drv"}},
		Env:       map[string]string{"CGO_ENABLED": "0", "EDITOR_GO": "/nix/store/aaa-go-1.26.8/bin/go", "CFLAGS": "-I/nix/store/bbb/include", "PATH": "/x"},
		InsideEnv: map[string]string{"GOROOT": "/nix/store/aaa-go-1.26.8/share/go", "GOTOOLCHAIN": "local"},
		ToolEnv:   map[string]map[string]string{"gopls": {"GOFLAGS": "-tags=bob"}}}, read)
	if f.Outside.Set["CGO_ENABLED"] != "0" || f.Outside.Set["EDITOR_GO"] != "/r/.bob/bin/go" {
		t.Errorf("outside.set = %v", f.Outside.Set)
	}
	if strings.Join(f.InsideOnly, ",") != "CFLAGS,GOROOT" {
		t.Errorf("insideOnly = %v", f.InsideOnly)
	}
	if _, ok := f.Outside.Set["GOTOOLCHAIN"]; ok {
		t.Errorf("an inside-only ecosystem variable was exported")
	}
	if !f.Tools["gopls"].LSP || f.Tools["go"].LSP || f.Tools["gopls"].Env["GOFLAGS"] != "-tags=bob" || len(f.Tools) != 2 {
		t.Errorf("tools = %+v", f.Tools)
	}
	if f.Inside["PATH"] != "/nix/store/aaa-go-1.26.8/bin" || f.Drvs["/nix/store/aaa-go-1.26.8"] != "/nix/store/x.drv" {
		t.Errorf("inside PATH %q, drvs %v", f.Inside["PATH"], f.Drvs)
	}
}

func TestShimsOnlyTools(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(ShimDir(root), "stale")
	if err := os.MkdirAll(ShimDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &File{Tools: map[string]Tool{"gopls": {LSP: true}, "go": {}}}
	if err := WriteShims(root, "/usr/local/bin/bob", f); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("a stale shim survived")
	}
	b, err := os.ReadFile(filepath.Join(ShimDir(root), "gopls"))
	want := "#!/bin/sh\n# Code generated by bob. DO NOT EDIT.\nexec \"${BOB:-/usr/local/bin/bob}\" exec --lsp --project '" + root + "' -- gopls \"$@\"\n"
	if err != nil || string(b) != want {
		t.Errorf("gopls shim %q, want %q (%v)", b, want, err)
	}
}

func TestAllowList(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state", "bob", "allow.json")
	if IsAllowed(p, "/a") {
		t.Fatal("allowed without a list")
	}
	for _, step := range []struct {
		root  string
		allow bool
	}{{"/a", true}, {"/b", true}, {"/a", false}} {
		if err := SetAllowed(p, step.root, step.allow); err != nil {
			t.Fatal(err)
		}
	}
	if IsAllowed(p, "/a") || !IsAllowed(p, "/b") {
		t.Errorf("roots = %v", Allowed(p))
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("allow.json mode: %v %v", fi, err)
	}
	b, _ := os.ReadFile(p)
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil || len(m) != 2 {
		t.Errorf("allow.json = %s", b)
	}
}

func TestStampsNoticeNewBobGo(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"bob.lock", "bob.go", "go.mod", "lib/x.go"} {
		p := filepath.Join(root, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	allow := filepath.Join(t.TempDir(), "allow.json")
	s, err := CollectStamps(root, allow, "v1", "h")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Fresh("v1", "h") || s.Fresh("v2", "h") || s.Fresh("v1", "other") {
		t.Fatal("freshness of an unchanged tree")
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, "lib", "bob.go"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s.Fresh("v1", "h") {
		t.Error("a new bob.go in a subdirectory was not noticed")
	}
	s, _ = CollectStamps(root, allow, "v1", "h")
	if err := SetAllowed(allow, root, true); err != nil {
		t.Fatal(err)
	}
	if s.Fresh("v1", "h") || !s.FreshIgnoring("v1", "h", allow) {
		t.Error("a trust list change: Fresh must fail, FreshIgnoring must not")
	}
}

func TestEditorMerge(t *testing.T) {
	root := t.TempDir()
	s, err := EditorSettings("vscode", []string{"go"})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, ".vscode", "settings.json")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(`{"editor.fontSize": 14, "gopls": {"x": 1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Merge(root); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(p)
	if changed, err := s.Merge(root); err != nil || changed {
		t.Errorf("second merge: changed %v, %v", changed, err)
	}
	if !strings.Contains(string(first), `"editor.fontSize": 14`) || strings.Contains(string(first), "rust-analyzer") || strings.Contains(string(first), `"x"`) {
		t.Errorf("merged settings:\n%s", first)
	}
	if err := os.WriteFile(p, []byte("{\n // c\n}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Merge(root); err == nil || !strings.Contains(err.Error(), "settings.json") {
		t.Errorf("a commented file: %v", err)
	}
}

// TestResultPrependsShimDir: the `bob env` result data has the SPEC §02.4 shape, with the shim
// directory as outside.prepend.PATH (what the hook's enter script puts in front of PATH).
func TestResultPrependsShimDir(t *testing.T) {
	f := &File{V: 1, Root: "/r", Shell: "default", Outside: Outside{Set: map[string]string{"CGO_ENABLED": "0"}},
		InsideOnly: []string{"GOROOT"}, Tools: map[string]Tool{"gopls": {}, "go": {}}}
	b, err := json.Marshal(f.Result())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"root":"/r","shell":"default","outside":{"set":{"CGO_ENABLED":"0"},"prepend":{"PATH":["/r/.bob/bin"]}},"insideOnly":["GOROOT"],"tools":["go","gopls"]}`
	if string(b) != want {
		t.Errorf("result = %s\nwant      %s", b, want)
	}
	s := NewScript("sh", []string{"PATH=/usr/bin"})
	s.Enter("/r", f, "h")
	if !strings.Contains(s.String(), "export PATH='"+f.Result().Outside.Prepend["PATH"][0]+":/usr/bin'") {
		t.Errorf("enter script does not prepend outside.prepend.PATH:\n%s", s.String())
	}
	empty, _ := json.Marshal((&File{Root: "/r"}).Result())
	if !strings.Contains(string(empty), `"set":{}`) || !strings.Contains(string(empty), `"insideOnly":[]`) || !strings.Contains(string(empty), `"tools":[]`) {
		t.Errorf("empty result has null fields: %s", empty)
	}
}
