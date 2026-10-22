# Protected-test corrections pending owner OK (2026-10-22)

The permission system blocks agents and the coordinator from editing protected tests. Each item
is a defect found while implementing, resolved with AGENTS.md section 2 (rulings A65-A67 in
plan/decisions.md). Fixing them is not loosening: each makes the test match the SPEC. Needed edits:

1. A65 `tests/nix/drv_test.go` TestRNIX006ATermEscapes: swap the two inputDrv entries in the
   expected ATerm (sorted by path, as SPEC R-NIX-006 and real Nix; evidence plan/progress/I-M2a.md).
2. A66 `bob.NamedVariant` is both a type and a function in SPEC section 1.9, which Go forbids.
   Ruling: type `bob.Named`, function `bob.NamedVariant(name, opts...) Named`. Edits:
   `tests/fixtures/repos/polyglot/bob.go` lines 17-18 and `tests/core/platform_test.go` lines 40-43:
   `[]bob.NamedVariant{` becomes `[]bob.Named{` (calls stay `bob.NamedVariant(...)`). SPEC 1.9 text
   updated by the coordinator.
3. A67 `tests/core/ir_test.go` helper irDoc: dependencies are written as labels while ids are
   "a:one"; SPEC says dependencies are ids. Fix the helper to emit ids; then remove the
   label-to-id leniency from `ir.Parse` (implementation, coordinator task).
4. A68 `TestRFW046ProjectionIgnoresUndeclaredAxes` vs `TestRFW047IRValidation`: `ir.Parse` must
   reject an unregistered kind (R-FW-047); the toy-kind test must therefore link the toy
   ecosystem: blank-import `github.com/megakuul/bob/tests/core/toyeco` in tests/core (non-test
   file or helpers_test.go), registering the toy kinds in the test binary.
5. A70 `tests/sandbox` layer-removal build `-tags bob_noseccomp`, case `sockets`: `AF_VSOCK socket`
   leaks with seccomp removed. No other layer can refuse socket families (Landlock has no socket
   family control, checked in kernel headers; vsock is loaded on the host); S13 never ran this case
   without seccomp. Ruling: seccomp is the only layer for socket families, so the layer-removal
   expectation for that one check must be "leaks when seccomp is removed" (the other layers still
   hold for the rest of the case). Evidence: plan/progress/I-M3b.md TEST PROBLEM.
6. A72 `tests/nix/builder_test.go:61` TestRNIX016FuzzedJobsStayContained builds raw JSON by string
   concatenation; the fuzz tokens `a\..\b` and `\x00` make the JSON invalid for every
   implementation (seed 16). Fix: build the job with `raw(t, map[string]string{"path": path()})`
   (proper JSON encoding). Evidence: plan/progress/I-M4a.md.
