# Fault catalogue

One directory per fault: `tests/faults/<fault>/` (HARNESS.md §5). SPEC's
`fixtures/<fault>/<nixver>/golden.txt` means `tests/faults/<fault>/<nixver>/golden.txt`
(decision A22). Protected after Gate H.

- `expect.json`: the protected index, fault → `codes` (each must appear as `error[BOB-E-NNNN]`,
  as often as listed), `exit` (absent: any non-zero), optional `warnings`, `messages` (substrings)
  and `alt` (other allowed outcomes; fault 34 only).
- `<fault>/fault.json`: fixture repo, injection, command, expected code, position (`marker` =
  `//bob:fault <id>` in the fixture's `bob.go`; `expectPosition` for the recorded captures),
  excerpt/hint, Nix versions, and `forbid` (this fault's raw Nix texts, on top of the shared
  leak patterns of `tests/errors/leakcheck`).
- `<fault>/<nixver>/`: the S15 capture (`meta.json`, `stream.log[.gz]`, goldens), the replay
  input. bob reads it through the test hook `BOB_TEST_NIX_REPLAY=<dir>`.
- `01`–`48` are the S15 faults with S15's numbers, `ok-*` the S15 success scenarios, `49`–`81`
  the HARNESS.md §5.1 additions (no capture yet; they run live).

Runner: `go test ./tests/errors/...` (CI stage `errors`). An entry whose feature answers
`BOB-E-9008` (not implemented) is red: it fails in the parity catalogue, not in CI. Crashes, raw
Nix leaks and wrong answers fail the stage.
