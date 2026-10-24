package nix

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	errorsuite "github.com/megakuul/bob/tests/errors"
	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// The error-quality suite (tests/errors, HARNESS.md §5) is owned elsewhere and runs the whole
// fault catalogue. The tests below map the R-NIX requirements whose acceptance text is a
// selection of that catalogue to the entries the SPEC names, so the parity catalogue sees them.

var (
	catOnce  sync.Once
	catItems []errorsuite.Entry
	catErr   error
	catMu    sync.Mutex
	catRes   = map[string]errorsuite.Result{}
)

func catalogue(t testing.TB) []errorsuite.Entry {
	t.Helper()
	catOnce.Do(func() { catItems, catErr = errorsuite.Load(harness.Root(t)) })
	if catErr != nil {
		t.Fatalf("fault catalogue: %v", catErr)
	}
	return catItems
}

// runEntry runs one catalogue entry (all its modes) once per test binary.
func runEntry(t *testing.T, e errorsuite.Entry) errorsuite.Result {
	t.Helper()
	catMu.Lock()
	defer catMu.Unlock()
	if r, ok := catRes[e.ID]; ok {
		return r
	}
	runner := &errorsuite.Runner{
		Root:    harness.Root(t),
		Bins:    map[string]string{"bob": harness.Bin(t, "bob"), "bob-server": harness.Bin(t, "bob-server")},
		Scratch: t.TempDir(),
	}
	r := runner.Run(context.Background(), e)
	catRes[e.ID] = r
	return r
}

// checkEntries requires every selected entry to pass: red entries (not implemented) and failing
// entries both fail the test, the first with the "red" marker.
func checkEntries(t *testing.T, sel func(errorsuite.Entry) bool, req string, minEntries int) {
	t.Helper()
	n := 0
	for _, e := range catalogue(t) {
		if !sel(e) {
			continue
		}
		n++
		t.Run(e.ID, func(t *testing.T) {
			res := runEntry(t, e)
			switch res.Status {
			case errorsuite.Fail:
				for _, m := range res.Reasons {
					t.Error(m)
				}
			case errorsuite.Red:
				harness.RedFeature(t, strings.Join(res.Red, "; "))
			case errorsuite.Pass:
				for _, g := range res.Golden {
					golden.Assert(t, g.Path, g.Got, golden.Human(), golden.Reqs(req))
				}
			}
		})
	}
	if n < minEntries {
		t.Errorf("only %d catalogue entries selected, want at least %d", n, minEntries)
	}
}

func ids(want ...string) func(errorsuite.Entry) bool {
	return func(e errorsuite.Entry) bool {
		for _, w := range want {
			if e.ID == w || (len(w) == 2 && strings.HasPrefix(e.ID, w+"-")) {
				return true
			}
		}
		return false
	}
}

func hasReplay(e errorsuite.Entry) bool {
	for _, nv := range e.NixVer {
		if e.ReplayDir(nv) != "" {
			return true
		}
	}
	return false
}

// TestRNIX010NoRawNixText replays every captured fault (at least 48) through the translator; the
// default human and --json outputs must contain none of the leak patterns, -v shows the raw text
// as nix: lines, and an unknown Nix message yields BOB-E-9999 with exit 70 (fault 74).
func TestRNIX010NoRawNixText(t *testing.T) {
	parity.Covers(t, "R-NIX-010")
	checkEntries(t, func(e errorsuite.Entry) bool { return hasReplay(e) || e.ID == "74-unknown-nix-message" }, "R-NIX-010", 48)
}

// specHints reads the SPEC §01 error table (SPEC.md, "Code | Class | Message template | Default
// hint") and returns the default hint of each code; a hint cell that is empty or starts with an
// em dash means the row has no hint.
func specHints(t testing.TB) map[string]string {
	t.Helper()
	b := mustRead(t, filepath.Join(harness.Root(t), "SPEC.md"))
	row := regexp.MustCompile("^\\| (\\d{4})(?: \\*\\*new\\*\\*)? \\| [a-z]+ \\| `.*` \\| (.*) \\|$")
	out := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		m := row.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		h := strings.TrimSpace(m[2])
		if strings.HasPrefix(h, "—") || strings.HasPrefix(h, "(") {
			h = ""
		}
		out["BOB-E-"+m[1]] = h
	}
	if len(out) < 60 {
		t.Fatalf("harness: parsed %d rows of the SPEC error table, want at least 60", len(out))
	}
	return out
}

// noExcerptDocumented are recorded goldens that carry `full log:` (a builder log exists) but no
// excerpt line. The R-NIX-012 rule is not loosened for them: they are documented exceptions and a
// finding for the owner (ruling G4, plan/gate-h-rulings.md), keyed by fault number.
var noExcerptDocumented = map[string]string{
	"26": "BOB-E-9004: bob rejects the derivation before the builder ran, yet prints `full log:`",
	"28": "BOB-E-9001: the builder binary is missing, yet `full log:` is printed without an excerpt",
	"30": "BOB-E-9003: the disallowed reference is found after the build, no excerpt line",
	"33": "BOB-E-6002: killed by SIGKILL, no log excerpt",
}

// TestRNIX012FailuresInBobTerms covers faults 6–15 and 22–33. Ruling G4 (R-NIX-012, conditional
// rule): every recorded golden has `error[BOB-E-` and `package defined at`; the hint line is
// present iff the SPEC §01 error table row of its code has a hint; excerpt lines and `full log:`
// go together (a failure bob detects without a builder run has neither). Faults with `full log:`
// but no excerpt are the documented exceptions above.
func TestRNIX012FailuresInBobTerms(t *testing.T) {
	parity.Covers(t, "R-NIX-012")
	sel := ids("06", "07", "08", "09", "10", "11", "12", "13", "14", "15",
		"22", "23", "24", "25", "26", "27", "28", "29", "30", "31", "32", "33")
	hints := specHints(t)
	blockRe := regexp.MustCompile(`error\[(BOB-E-\d{4})\]`)
	asserted := 0
	var exceptions []string // logged in their own passing subtest (parent logs would hide red markers from CI)
	for _, e := range catalogue(t) {
		if !sel(e) {
			continue
		}
		fault := strings.SplitN(e.ID, "-", 2)[0]
		for _, nv := range e.NixVer {
			d := e.ReplayDir(nv)
			if d == "" {
				t.Errorf("%s/%s: no recorded golden", e.ID, nv)
				continue
			}
			g := string(mustRead(t, filepath.Join(d, "golden.txt")))
			asserted++
			// Split into error blocks (a block runs to the next `error[` or the summary line).
			idx := blockRe.FindAllStringSubmatchIndex(g, -1)
			if len(idx) == 0 {
				t.Errorf("%s/%s golden lacks %q", e.ID, nv, "error[BOB-E-")
				continue
			}
			for i, m := range idx {
				end := len(g)
				if i+1 < len(idx) {
					end = idx[i+1][0]
				}
				code, block := g[m[2]:m[3]], g[m[0]:end]
				name := e.ID + "/" + nv + "/" + code
				if !strings.Contains(block, "package defined at ") {
					t.Errorf("%s golden lacks %q", name, "package defined at ")
				}
				tableHint, known := hints[code]
				if !known {
					t.Errorf("%s: code is not in the SPEC error table", name)
				}
				hasHint := strings.Contains(block, "\n  hint: ")
				switch {
				case tableHint != "" && !hasHint:
					t.Errorf("%s golden lacks the hint line, the error table gives %q", name, tableHint)
				case tableHint == "" && hasHint:
					t.Errorf("%s golden has a hint line but the error table row has none", name)
				case hasHint && !strings.Contains(tableHint, "{"):
					if !strings.Contains(block, "\n  hint: "+tableHint) {
						t.Errorf("%s: hint line does not start with the table hint %q:\n%s", name, tableHint, block)
					}
				}
				hasLog := strings.Contains(block, "\n  full log: bob log ")
				hasExcerpt := regexp.MustCompile(`(?m)^   \| `).MatchString(block)
				switch {
				case hasLog && !hasExcerpt:
					if why, ok := noExcerptDocumented[fault]; ok {
						exceptions = append(exceptions, fmt.Sprintf("documented exception (fault %s, finding for the owner): `full log:` without an excerpt: %s", fault, why))
					} else {
						t.Errorf("%s golden has `full log:` but no excerpt line", name)
					}
				case !hasLog && hasExcerpt:
					t.Errorf("%s golden has excerpt lines but no `full log:` (no builder log, so no excerpt)", name)
				case hasLog && hasExcerpt:
					if _, ok := noExcerptDocumented[fault]; ok {
						exceptions = append(exceptions, fmt.Sprintf("fault %s is listed as an exception but its golden now has an excerpt; remove it from noExcerptDocumented", fault))
					}
				}
			}
		}
	}
	if asserted < 22 {
		t.Errorf("only %d goldens asserted, want 22 (faults 6-15, 22-33)", asserted)
	}
	t.Run("documented-exceptions", func(t *testing.T) {
		for _, x := range exceptions {
			t.Log(x)
		}
	})
	checkEntries(t, sel, "R-NIX-012", 22)
}

// TestRNIX017BuilderExitCodesInRealRuns: S15 fault row 28 (builder missing → 9001) and the 2xxx
// rows pass with the real builder, and a step printing an `@nix {"action":"msg"…}` line does not
// change the result of a successful build (ok-structured: warning only).
func TestRNIX017BuilderExitCodesInRealRuns(t *testing.T) {
	parity.Covers(t, "R-NIX-017")
	checkEntries(t, func(e errorsuite.Entry) bool {
		if e.ID == "28-builder-missing" || e.ID == "ok-structured" {
			return true
		}
		for _, c := range e.Expect.Codes {
			if strings.HasPrefix(c, "BOB-E-2") {
				return true
			}
		}
		return false
	}, "R-NIX-017", 12)
}

// TestRNIX018SuccessIsStructural: the full-disk fault (a partial output file) is reported as
// failure 6001 although the builder printed success, and injected fake error messages in a
// successful build give exit 0 with a warning.
func TestRNIX018SuccessIsStructural(t *testing.T) {
	parity.Covers(t, "R-NIX-018")
	checkEntries(t, ids("40-disk-full", "ok-structured", "29-no-output"), "R-NIX-018", 3)
}

// TestRNIX011PreChecksInGo covers the gen-b fault rows: unknown attribute with suggestion,
// broken, insecure, cycle, Go version and unbuildable system print their template with definedAt
// and exit 3, before any Nix build starts.
func TestRNIX011PreChecksInGo(t *testing.T) {
	parity.Covers(t, "R-NIX-011")
	checkEntries(t, ids("01", "02", "03", "04", "05", "31"), "R-NIX-011", 6)
	for _, e := range catalogue(t) {
		if e.ID != "01-unknown-attr" {
			continue
		}
		res := runEntry(t, e)
		if res.Status != errorsuite.Pass {
			continue
		}
		for _, g := range res.Golden {
			if !strings.Contains(string(g.Got), "openssl") {
				t.Errorf("%s: the suggestions for opensll do not include openssl:\n%s", e.ID, g.Got)
			}
		}
	}
}

// TestRNIX022UserNamespacesBlockedFault is the catalogue row for BOB-E-7005 with the fix text.
func TestRNIX022UserNamespacesBlockedFault(t *testing.T) {
	parity.Covers(t, "R-NIX-022")
	checkEntries(t, ids("71-userns-blocked"), "R-NIX-022", 1)
}

// TestRNIX019DisallowedReferenceFault is the catalogue row for BOB-E-9003 (a forbidden
// reference found by the fixup).
func TestRNIX019DisallowedReferenceFault(t *testing.T) {
	parity.Covers(t, "R-NIX-019")
	checkEntries(t, ids("30-disallowed-ref"), "R-NIX-019", 1)
}

// TestRNIX028CodeTableIsAppendOnly compares the code table with the protected codes.lock.
func TestRNIX028CodeTableIsAppendOnly(t *testing.T) {
	parity.Covers(t, "R-NIX-028")
	lock := string(mustRead(t, filepath.Join(harness.Root(t), "pkg", "bob", "diag", "codes.lock")))
	cur := codeLines(t)
	have := map[string]string{}
	for _, l := range cur {
		have[lockKey(l)] = l
	}
	n := 0
	for _, l := range strings.Split(lock, "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		n++
		got, ok := have[lockKey(l)]
		switch {
		case !ok:
			t.Errorf("code removed: %s", l)
		case got != l:
			t.Errorf("code changed:\n lock: %s\n now:  %s", l, got)
		}
	}
	if n == 0 {
		t.Fatal("codes.lock is empty")
	}
	if _, err := os.Stat(filepath.Join(harness.Root(t), "pkg", "bob", "diag", "codes.lock")); err != nil {
		t.Fatal(err)
	}
}

// codeLines renders the code table like codes.lock: code, class, variant, message args, hint args.
func codeLines(t testing.TB) []string {
	t.Helper()
	list := func(xs []string) string {
		if len(xs) == 0 {
			return "-"
		}
		return strings.Join(xs, ",")
	}
	var out []string
	for _, c := range diag.Codes() {
		e, ok := diag.Lookup(c)
		if !ok {
			t.Fatalf("diag.Codes lists %d but Lookup does not know it", c)
		}
		for _, v := range e.Variants {
			var h []string
			for _, hint := range v.Hints {
				h = append(h, diag.TemplateArgs(hint)...)
			}
			sort.Strings(h)
			h = slices.Compact(h)
			name := v.Name
			if name == "" {
				name = "-"
			}
			out = append(out, fmt.Sprintf("%04d %s %s args=%s hint=%s", c, c.Class(), name, list(diag.TemplateArgs(v.Message)), list(h)))
		}
	}
	return out
}

func lockKey(line string) string {
	f := strings.Fields(line)
	if len(f) < 3 {
		return line
	}
	return f[0] + " " + f[2]
}

// TestRNIX028NewEveryCode calls diag.New for every code with its arguments, and checks that it
// panics on an unknown code and on a missing argument.
func TestRNIX028NewEveryCode(t *testing.T) {
	parity.Covers(t, "R-NIX-028")
	panics := func(f func()) (p bool) {
		defer func() { p = recover() != nil }()
		f()
		return false
	}
	for _, c := range diag.Codes() {
		e, _ := diag.Lookup(c)
		for _, v := range e.Variants {
			var kv []string
			if v.Name != "" {
				kv = append(kv, "variant", v.Name)
			}
			all := diag.TemplateArgs(v.Message)
			for _, h := range v.Hints {
				all = append(all, diag.TemplateArgs(h)...)
			}
			for _, a := range all {
				kv = append(kv, a, "x")
			}
			var err *diag.Error
			if panics(func() { err = diag.New(c, kv...) }) {
				t.Errorf("%s: New panicked with all arguments given", c.ID())
				continue
			}
			if strings.Contains(err.Error(), "{") {
				t.Errorf("%s: unfilled template: %s", c.ID(), err.Error())
			}
			if len(diag.TemplateArgs(v.Message)) > 0 {
				kv2 := []string{}
				if v.Name != "" {
					kv2 = append(kv2, "variant", v.Name)
				}
				if !panics(func() { diag.New(c, kv2...) }) {
					t.Errorf("%s: New did not panic with a missing argument", c.ID())
				}
			}
		}
	}
	if !panics(func() { diag.New(diag.Code(65000)) }) {
		t.Errorf("New did not panic on an unknown code")
	}
}
