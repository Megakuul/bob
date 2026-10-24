package main

import (
	"strings"
	"testing"
)

func TestDocsIsSurface(t *testing.T) {
	for p, want := range map[string]bool{
		"pkg/cli/cli.go":                 true,
		"pkg/cli/args.go":                true,
		"pkg/cli/cmd_build.go":           false,
		"pkg/bob/diag/codes.go":          true,
		"pkg/golang/golang.go":           true,
		"pkg/golang/golang_test.go":      false,
		"pkg/golang/gen/plan.go":         false,
		"pkg/output/oci/oci.go":          true,
		"spec/02-cli-dev.md":             true,
		"SPEC.md":                        true,
		"cmd/bob-server/main.go":         true,
		"internal/server/config.go":      false, // judged by its changed lines
		"website/content/docs/x/01-a.md": false,
		"README.md":                      false,
	} {
		if got := isSurface(p); got != want {
			t.Errorf("isSurface(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestDocsCheck(t *testing.T) {
	cases := []struct {
		name string
		c    docsChange
		fail bool
	}{
		{"no surface", docsChange{files: []string{"pkg/cli/cmd_build.go", "internal/x/y.go"}}, false},
		{"surface without docs", docsChange{files: []string{"pkg/cli/cli.go"}}, true},
		{"surface with docs", docsChange{files: []string{"pkg/cli/cli.go", "website/content/docs/05-cli/01-commands.md"}}, false},
		{"surface exempt", docsChange{files: []string{"pkg/golang/golang.go"}, exempt: "comment typo"}, false},
		{"server variable", docsChange{envFiles: []string{"internal/server/config.go"}}, true},
		{"server variable with docs", docsChange{envFiles: []string{"internal/server/config.go"}, files: []string{"website/content/docs/06-server/03-configuration.md"}}, false},
	}
	for _, tc := range cases {
		err := tc.c.check()
		if (err != nil) != tc.fail {
			t.Errorf("%s: check() = %v, want failure %v", tc.name, err, tc.fail)
		}
	}
}

func TestDocsExemption(t *testing.T) {
	if got := exemption("M3: fix\n\ndocs-exempt: internal rename only\nCo-Authored-By: x"); got != "internal rename only" {
		t.Errorf("exemption = %q", got)
	}
	if got := exemption("docs-exempt:\nmore"); got != "" {
		t.Errorf("an exemption without a reason must not count, got %q", got)
	}
}

func TestDocsEnvChangedFiles(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/internal/server/config.go b/internal/server/config.go",
		"--- a/internal/server/config.go",
		"+++ b/internal/server/config.go",
		"@@ -1 +1 @@",
		`+	c.X = getenv("BOB_NEW_SETTING")`,
		"diff --git a/internal/server/config_test.go b/internal/server/config_test.go",
		"--- a/internal/server/config_test.go",
		"+++ b/internal/server/config_test.go",
		`+	t.Setenv("BOB_NEW_SETTING", "1")`,
		"diff --git a/internal/store/x.go b/internal/store/x.go",
		"--- a/internal/store/x.go",
		"+++ b/internal/store/x.go",
		"+	return nil",
	}, "\n")
	got := envChangedFiles(diff)
	if len(got) != 1 || got[0] != "internal/server/config.go" {
		t.Errorf("envChangedFiles = %v", got)
	}
}
