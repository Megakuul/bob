# I-M3a — M3.1 labels, M3.2 pkg/bob core API, M3.3 ext + IR + engine registry

Owner of: pkg/bob (not diag), pkg/bob/ext, pkg/engine/ir, pkg/engine/engine.go registry.

## Done
- M3.1: pkg/bob/label.go (Label, ParseLabel, Parse, Pattern{Valid,Match}, Position, bounds G9),
  platform.go (Configured, ParseConfigured, variants). tests/core TestRFW015LabelGrammar and
  TestRFW050ParseConfigured green. R-FW-016 tests need the evaluator (M3.4) / CLI.
- M3.2: core.go, define.go, eval.go (Main + evaluation), node.go, sources.go (Run, Step.File, Fetch,
  Glob/Files/Dir), repo.go (Repo, Pipeline, NewShell), services.go (ServiceFrom), irout.go,
  internal/graph. Unit tests pkg/bob/*_test.go + golden pkg/bob/testdata/ir.golden.json.
  gopls completion check done by hand (lspc of S1 against a scratch repo: `foo.` → Lib, `bob.` →
  72 items incl. Define, definition → libs/foo/bob.go:8, "missing method LinkInfo" diagnostic).

- M3.3: pkg/bob/ext (Register, Lookup, Config with structural axis checks, New, ConfigOf),
  pkg/engine/ir (Parse with streaming bound scan: 64 MiB, 64 KiB strings, 1e6 nodes, depth 128;
  DisallowUnknownFields; irVersion; bobVersion → internal 9004 (exit 70); ids, kinds, deps
  (1007), roots, iterative cycle check (1006 with chain + definedAt); Graph.Canonical; KnownKind;
  RegisterEcosystem), pkg/engine/engine.go registry (Register, Ecosystems, Lookup, ForKind; bad or
  duplicate registrations reported by Ecosystems, no panic). Unit tests in pkg/engine and
  pkg/engine/ir. tests/core TestRFW047IRValidation, FuzzIRParse (seeds + 2×30 s local fuzz),
  TestRFW047SchemaFile, TestRFW044IRVersionLock green.
- Evidence end-to-end: with M3.4's in-progress eval.Evaluate, tests/core TestRFW011Memoised
  passed once (14:10); a hand-built -trimpath evaluator in a scratch repo produced the IR,
  definedAt `libs/foo/bob.go:9` and `bob: libs/foo/bob.go:12: Lib was called before evaluation…`.

## Still red (other milestones)
- Need eval.Evaluate (M3.4) / bob CLI (M4.6): R-FW-011 error cases, R-FW-015 duplicate label,
  R-FW-016 both, R-FW-046 both, R-FW-051, R-FW-047 fixture/golden IR (tests/goldens/ir/* do not
  exist yet; they come from the evaluator and need owner approval).
- TestAPISurface: only other units' constructors lack Example tests (pkg/bob, ext complete).

## Evaluator protocol (for M3.4, pkg/engine/eval)
- Generated main: `func main() { bob.Main() }`; run with cwd = repo root (bob reads the
  `var X = bob.Define(` line of a bob.go to name constructors in messages).
- Request = os.Args[1], JSON `{"targets":["//...","//api:api@linux-amd64+debug"],"host":"linux-arm64","root":"."}`
  (unknown fields rejected). A target is a pattern with an optional `@<platform>[+variant...]`;
  without it: host, release. CLI `--platform/--variant` become that suffix.
- Output: IR on fd 3 (stdout if fd 3 is not open); diagnostics on stderr, one per line
  `bob:: {"type":"diag","severity":"error","code":"BOB-E-1006","label":..,"file":..,"line":..,"msg":..}`
  (code/label/file/line optional). Main never exits non-zero: success ⇔ IR written to fd 3.
- bobVersion in the IR = version of module github.com/megakuul/bob in the evaluator's build info.

## DECISIONS
- D-M3a-1 (SPEC "panics", AGENTS §5.3/no-panic lint): Define-before-evaluation, Define inside a
  constructor and ext axis misuse do not panic; they record a definition error with the exact SPEC
  text and evaluation fails at the end (all errors together). User panics inside constructors are
  recovered and reported as definition errors of that constructor.
- D-M3a-2 Errors are reported only for constructors relevant to the targets: those that created a
  reachable node, the constructors they called, all constructors below a wildcard's directory, and
  those in the directory of a label target that matched nothing. Unrelated broken definitions do
  not fail `bob build //x:y` (needed by tests/core toy: //toy:bad must not break //toy:b).
- D-M3a-3 Node identity: id = canonical configured label of the node's projection = its kind's
  declared axes (from its P; p.Host() for Host kinds) plus the axes its dependencies carry
  (a node built from different inputs is a different node). Without projected axes the id is the
  plain label; without platform but with tokens `//x:y@+debug`; Rust features add `+f-<hash8>`.
  IR `proj` holds exactly these axes. Identical nodes from several configurations are one node.
- D-M3a-4 Tree/Tool/Test markers are methods of Node (SPEC's `nixpkgs.Package struct{bob.Node}`
  implements Tool and Tree, which other packages can only get by embedding); Executable and
  Linkable are told apart by their exported methods.
- D-M3a-5 Core kinds: bob.run (platform), bob.file (Step.File; none), bob.fetch (none),
  bob.devshell (host, platform). Kinds named `nixpkgs.<x>`: label `nixpkgs:<name>`, spec = IR `nix`.
- D-M3a-6 `spec` must encode to a JSON object (schema says object); other values are a
  definition error. (pkg/output/oci, tarball, release stubs pass non-objects today.)
- D-M3a-7 Package name "all" is reserved (`//dir:all` is a pattern); ParseLabel rejects it.
- D-M3a-8 SPEC names type and constructor `NamedVariant`; Go cannot declare both. Type is `Named`
  (as the Gate H stub), constructor `NamedVariant`.
- D-M3a-9 Sources IR: `{"dir": <bob.go dir>, "patterns": [...]}`; Files() entries are glob-escaped
  with `\`, Dir(d) is `d/**`.

- D-M3a-10 IR-invalid errors use BOB-E-9004 (internal; label "the evaluator output"); cycles 1006,
  unknown dependency 1007. A named variant may shadow a built-in token (polyglot's "asan").
- D-M3a-11 ir.Parse: a dependency or root that is not a node id but the label of exactly one node
  resolves to it and is rewritten to the id (see TEST PROBLEM 2). Kinds are known if registered
  with ext.Register in this binary (incl. bob's core kinds) or if their ecosystem is registered
  with engine.Register.

## NEEDS
- M3.4 (eval): run the evaluator with cwd = repository root (messages read the `var X =` line);
  map `bob::` diag lines to error events; treat "no IR on fd 3" as failure.
- SPEC/diag: a dedicated IR-invalid code instead of 9004 (G12 only says "structured").
- Error codes: evaluation definition errors without a SPEC code (duplicate label, Define misuse,
  early call, axis misuse, constructor panic) are emitted without "code"; §02.3 should assign one
  (e.g. a new 1026 `{pos}: {message}`) — coordinator/SPEC.
- tools/lint/allow.txt (protected): nothing needed (no panic used).

## TEST PROBLEM
1. tests/fixtures/repos/polyglot/bob.go and tests/core/platform_test.go (s7Repo) write
  `Variants: []bob.NamedVariant{ bob.NamedVariant("asan", …) }`: one identifier used as a type and
  as a multi-argument call cannot compile in Go (SPEC §1.4.4/§1.9 has the same clash). With the
  type `bob.Named` the fixture must say `[]bob.Named{…}`. Every test evaluating polyglot fails to
  compile until the fixture changes.
2. tests/core/ir_test.go irDoc: node ids are "a:one" while deps name "//a:one" (the label), and
   the valid document must be accepted although SPEC says deps are ids. Handled by D-M3a-11
   (label fallback when unambiguous); owner may prefer fixing irDoc to ids.
3. tests/core/ir_test.go TestRFW046ProjectionIgnoresUndeclaredAxes calls ir.Parse in the test
   process, which does not link the toy kinds (registered only inside the evaluator and in
   tests/core/toyeco, which tests/core does not import), while TestRFW047IRValidation requires
   ir.Parse to reject unknown kinds ("nosuch.kind"). Both cannot pass: "toy.thing" and
   "nosuch.kind" are equally unknown in that process. Fix options: tests/core blank-imports
   tests/core/toyeco (registers ecosystem "toy"), or the unknown-kind check moves from ir.Parse to
   the engine. The same applies to every tests/core evalIR of fixtures whose ecosystems are not
   linked into the test binary.
