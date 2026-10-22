package lock

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/fetch"
)

const (
	rev     = "cd4ea8d57de16f4810bc5f14207bac45fbf19f67"
	sriHash = "sha256-uQTfBw5nXEUrOTW0UY9fbKzVnYOU5x1IwZwZnw2J9D0="
)

func sample() *File {
	return &File{
		LockVersion: 1,
		Nixpkgs:     NixpkgsPin{Channel: "nixos-26.05", NarHash: sriHash, Rev: rev, URL: NixpkgsURL(rev)},
		Sources: map[string]Source{"//third_party/zlib:zlib": {Hash: sriHash, Kind: "archive", Name: "zlib-1.3.1-src",
			NarHash: sriHash, Strip: 1, URL: "https://zlib.net/zlib-1.3.1.tar.gz"}},
		Sections: map[string]json.RawMessage{"go": json.RawMessage(`{"modules":[],"inputs":"` + strings.Repeat("a", 64) + `"}`)},
	}
}

func TestEncodeParseRoundTrip(t *testing.T) {
	b, err := sample().Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "go": {
    "inputs": "` + strings.Repeat("a", 64) + `",
    "modules": []
  },
  "lockVersion": 1,
  "nixpkgs": {
    "channel": "nixos-26.05",
    "narHash": "` + sriHash + `",
    "rev": "` + rev + `",
    "url": "https://github.com/NixOS/nixpkgs/archive/` + rev + `.tar.gz"
  },
  "sources": {
    "//third_party/zlib:zlib": {
      "hash": "` + sriHash + `",
      "kind": "archive",
      "name": "zlib-1.3.1-src",
      "narHash": "` + sriHash + `",
      "strip": 1,
      "url": "https://zlib.net/zlib-1.3.1.tar.gz"
    }
  }
}
`
	if string(b) != want {
		t.Fatalf("Encode:\n%s\nwant\n%s", b, want)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := f.Encode()
	if err != nil || string(b2) != want {
		t.Fatalf("re-encode differs (%v):\n%s", err, b2)
	}
	if f.Stale("go", strings.Repeat("a", 64)) || !f.Stale("go", strings.Repeat("b", 64)) || !f.Stale("rust", "") {
		t.Errorf("Stale is wrong")
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct{ name, data, reason string }{
		{"conflict", "{\n<<<<<<< HEAD\n  \"lockVersion\": 1\n=======\n}\n", `^merge conflict marker <<<<<<< \(line 2\)$`},
		{"syntax", "{\n  \"lockVersion\": 1,\n}\n", `\(line 3\)$`},
		{"version", "{\n  \"lockVersion\": 2\n}\n", `lockVersion 2 is not supported .*\(line 2\)$`},
		{"float", "{\n  \"lockVersion\": 1.0\n}\n", `\(line 2\)$`},
		{"missing version", "{}\n", `lockVersion is missing \(line 1\)$`},
		{"unknown", "{\n  \"lockVersion\": 1,\n  \"pip\": {}\n}\n", `unknown member "pip" \(line 3\)$`},
		{"twice", "{\"lockVersion\": 1,\n\"lockVersion\": 1}\n", `appears twice \(line 2\)$`},
		{"bad hash", "{\"lockVersion\": 1,\n\"nixpkgs\": {\"channel\":\"c\",\"narHash\":\"md5-x\",\"rev\":\"" + rev + "\",\"url\":\"https://x/y\"}}\n", `narHash .*\(line 2\)$`},
		{"trailing", "{\"lockVersion\": 1}\n{}\n", `\(line 2\)$`},
		{"not object", "[]\n", `not a JSON object`},
		{"empty", "", `end of file`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.data))
			var de *diag.Error
			if !errors.As(err, &de) || de.Code != 1019 {
				t.Fatalf("err = %v, want BOB-E-1019", err)
			}
			msg := strings.TrimPrefix(de.Error(), "bob.lock cannot be read: ")
			if !regexpMatch(c.reason, msg) {
				t.Errorf("message %q does not match %q", msg, c.reason)
			}
		})
	}
}

func TestWriteOnlyOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bob.lock")
	f := sample()
	if err := f.Write(path); err != nil {
		t.Fatal(err)
	}
	if wrote, err := WriteBytes(path, mustEncode(t, f)); err != nil || wrote {
		t.Errorf("WriteBytes of identical bytes: wrote=%v err=%v", wrote, err)
	}
	back, err := Read(path)
	if err != nil || back.Nixpkgs.Rev != rev {
		t.Fatalf("Read: %v %+v", err, back)
	}
	if _, err := Read(filepath.Join(t.TempDir(), "none")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
}

func TestSalvage(t *testing.T) {
	data := []byte("{\n<<<<<<< HEAD\n\"nixpkgs\": {\"rev\": \"" + rev + "\"}\n")
	if got := SalvageNixpkgsRev(data); got != rev {
		t.Errorf("SalvageNixpkgsRev = %q", got)
	}
}

func TestParseNixpkgs(t *testing.T) {
	for in, want := range map[string]NixpkgsSpec{
		"":                     {Channel: DefaultChannel},
		"nixos-25.11":          {Channel: "nixos-25.11"},
		"nixos-26.05@" + rev:   {Channel: "nixos-26.05", Rev: rev},
		"nixos-26.05@cd4ea8d5": {},
		"../x":                 {},
	} {
		got, err := ParseNixpkgs(in)
		if (err != nil) != (want == NixpkgsSpec{}) || got != want {
			t.Errorf("ParseNixpkgs(%q) = %+v, %v", in, got, err)
		}
	}
}

type fakeFetcher struct{ resolves int }

func (f *fakeFetcher) Ensure(context.Context, fetch.Request) (string, error) { return "", nil }
func (f *fakeFetcher) Resolve(_ context.Context, r fetch.Request) (string, string, error) {
	f.resolves++
	if r.Strip != 1 || r.Kind != fetch.Archive || r.URLs[0] != NixpkgsURL(rev) {
		return "", "", errors.New("unexpected request")
	}
	return sriHash, sriHash, nil
}

func TestPinNixpkgs(t *testing.T) {
	ctx := context.Background()
	ff := &fakeFetcher{}
	noChannel := func(context.Context, string) (string, error) { return "", errors.New("no network") }
	pin, err := PinNixpkgs(ctx, ff, "nixos-26.05@"+rev, NixpkgsPin{}, false, noChannel)
	if err != nil || pin.Rev != rev || pin.NarHash != sriHash || pin.Channel != "nixos-26.05" || ff.resolves != 1 {
		t.Fatalf("pinned %+v, %v (resolves %d)", pin, err, ff.resolves)
	}
	// The same pin again: no download, no channel lookup.
	again, err := PinNixpkgs(ctx, ff, "nixos-26.05", pin, false, noChannel)
	if err != nil || again.Rev != rev || ff.resolves != 1 {
		t.Fatalf("re-pin %+v, %v (resolves %d)", again, err, ff.resolves)
	}
	// --update asks the channel.
	ch := func(_ context.Context, c string) (string, error) { return rev, nil }
	if _, err := PinNixpkgs(ctx, ff, "nixos-26.05", pin, true, ch); err != nil {
		t.Fatal(err)
	}
	if _, err := PinNixpkgs(ctx, ff, "nixos-26.05", NixpkgsPin{}, true, noChannel); err == nil {
		t.Errorf("a failing channel lookup was ignored")
	}
}

func mustEncode(t *testing.T, f *File) []byte {
	t.Helper()
	b, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func regexpMatch(re, s string) bool { return regexp.MustCompile(re).MatchString(s) }
