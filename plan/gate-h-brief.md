# Gate H rulings brief (read after AGENTS.md; then plan/gate-h-rulings.md, binding)

Gate H is not recorded yet, so protected paths may still change. Apply YOUR rulings (G-numbers in
plan/gate-h-rulings.md) exactly; touch only your own files (disjoint from the other agents). SPEC.md
is generated from spec/*.md: only the SPEC agent edits spec/ and SPEC.md; everyone else changes
tests, proto, fixtures, pkg stubs. Never weaken a test below what the SPEC (after the rulings)
states. Tests stay red only with "red: not implemented yet" (harness.Red/RedFeature).
Rules: no `git commit`/`git add` (coordinator commits); use `\rm`/`\cp`; own GOCACHE/XDG_CACHE_HOME
(`export GOCACHE=/tmp/gc-<unit> XDG_CACHE_HOME=/tmp/xdg-<unit>`); checkpoint every ~15 min in
plan/progress/<unit>.md (done / next / files touched); before DONE run gofmt and
`GOWORK=off go vet` for the packages you touched plus the affected tests (`go test` of your dirs;
expect only red-marker failures). Report <= 25 lines: files changed, what you verified, anything
you could not do.
