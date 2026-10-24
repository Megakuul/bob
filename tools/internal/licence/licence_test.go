package licence

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPolicy = `
allow MIT
allow BSD-3-Clause
allow Apache-2.0
except example.com/lgpl LGPL-2.1 D52 static Nix
`

func policy(t *testing.T) *Policy {
	t.Helper()
	p, err := ParsePolicy(strings.NewReader(testPolicy))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParsePolicyRejects(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"gpl wholesale", "allow GPL-2.0"},
		{"agpl wholesale", "allow AGPL-3.0"},
		{"lgpl wholesale", "allow LGPL-2.1"},
		{"exception without approval", "except example.com/x GPL-2.0 because"},
		{"garbage", "permit MIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParsePolicy(strings.NewReader(tc.in)); err == nil {
				t.Fatalf("policy %q accepted", tc.in)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"Permission is hereby granted, free of charge, to any person", "MIT"},
		{"GNU GENERAL PUBLIC LICENSE\n Version 2, June 1991", "GPL-2.0"},
		{"GNU GENERAL PUBLIC LICENSE\n Version 3, 29 June 2007", "GPL-3.0"},
		{"GNU AFFERO GENERAL PUBLIC LICENSE Version 3 ... GNU General Public License", "AGPL-3.0"},
		{"GNU LESSER GENERAL PUBLIC LICENSE\nVersion 2.1, February 1999", "LGPL-2.1"},
		{"Apache License\n Version 2.0, January 2004", "Apache-2.0"},
		{"Redistribution and use in source and binary forms ... endorse or promote products", "BSD-3-Clause"},
		{"Redistribution and use in source and binary forms, with or without", "BSD-2-Clause"},
		{"Mozilla Public License Version 2.0 ... GNU Affero General Public License ... GNU General Public License", "MPL-2.0"},
		{"all rights reserved", Unknown},
	} {
		if got := Classify(tc.text); got != tc.want {
			t.Errorf("Classify(%.30q) = %s, want %s", tc.text, got, tc.want)
		}
	}
}

func TestJudgeSPDX(t *testing.T) {
	p := policy(t)
	for _, tc := range []struct {
		subject, expr string
		ok            bool
	}{
		{"npm:a", "MIT", true},
		{"npm:a", "(MIT OR GPL-3.0)", true},
		{"npm:a", "MIT AND GPL-3.0", false},
		{"npm:a", "GPL-3.0", false},
		{"npm:a", "", false},
		{"example.com/lgpl", "LGPL-2.1", true},
		{"example.com/other", "LGPL-2.1", false},
	} {
		if _, ok, reason := p.judgeSPDX(tc.subject, tc.expr); ok != tc.ok {
			t.Errorf("%s %q: ok=%v (%s), want %v", tc.subject, tc.expr, ok, reason, tc.ok)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPlantedGPLModuleFails is M0.3's extra test: a GPL dependency in the
// Go build list makes the check fail, an MIT one passes.
func TestPlantedGPLModuleFails(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "main", "go.mod"), `module example.com/main

go 1.22

require (
	example.com/gpl v1.0.0
	example.com/mit v1.0.0
)

replace example.com/gpl => ../gpl

replace example.com/mit => ../mit
`)
	write(t, filepath.Join(dir, "gpl", "go.mod"), "module example.com/gpl\n\ngo 1.22\n")
	write(t, filepath.Join(dir, "gpl", "COPYING"), "GNU GENERAL PUBLIC LICENSE\nVersion 2, June 1991\n")
	write(t, filepath.Join(dir, "mit", "go.mod"), "module example.com/mit\n\ngo 1.22\n")
	write(t, filepath.Join(dir, "mit", "LICENSE"), "MIT License\n\nPermission is hereby granted, free of charge, to any person\n")

	got, err := CheckGoModule(context.Background(), filepath.Join(dir, "main"), policy(t))
	if err != nil {
		t.Fatal(err)
	}
	verdict := map[string]bool{}
	for _, f := range got {
		verdict[f.Subject] = f.OK
	}
	if ok, seen := verdict["example.com/gpl"]; !seen || ok {
		t.Errorf("GPL module: seen=%v ok=%v, want a failure (findings %+v)", seen, ok, got)
	}
	if ok := verdict["example.com/mit"]; !ok {
		t.Errorf("MIT module did not pass (findings %+v)", got)
	}
}

func TestCheckNPMLock(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "package-lock.json")
	write(t, lock, `{"lockfileVersion":3,"packages":{
		"":{"name":"web"},
		"node_modules/ok":{"version":"1.0.0","license":"MIT"},
		"node_modules/a/node_modules/bad":{"version":"2.0.0","license":"GPL-3.0-only"},
		"node_modules/none":{"version":"3.0.0"}}}`)
	got, err := CheckNPMLock(lock, policy(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"npm:bad": false, "npm:none": false, "npm:ok": true}
	if len(got) != len(want) {
		t.Fatalf("findings %+v", got)
	}
	for _, f := range got {
		if f.OK != want[f.Subject] {
			t.Errorf("%s: ok=%v, want %v", f.Subject, f.OK, want[f.Subject])
		}
	}
}

func TestCheckAttribution(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "ATTRIBUTION.md"), "| `assets/fonts/` | OFL |\n| `web/static/logo.svg` | own |\n")
	write(t, filepath.Join(root, "assets", "fonts", "a.woff2"), "x")
	write(t, filepath.Join(root, "web", "static", "logo.svg"), "x")
	write(t, filepath.Join(root, "web", "static", "icon.png"), "x")
	missing, err := CheckAttribution(root, []string{"assets", "web/static", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "web/static/icon.png" {
		t.Fatalf("missing = %v, want [web/static/icon.png]", missing)
	}
}
