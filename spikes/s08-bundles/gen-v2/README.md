# Spike S0a — generation strategy A′ vs B (throwaway)

- `fixture/` — the shared test sources (C++ libfoo with openssl, Rust staticlib rcore with
  itoa + hex, protobuf schema, Go cgo binary using both + uuid, x/sync, protobuf runtime).
- `ir/` — backend-neutral package graph (what bob's Go DSL would produce).
- `fixturegraph/` — the fixture graph written the way a `bob.go` would describe it, plus a
  synthetic graph generator (N small C libraries) for evaluation scaling.
- `cmd/graph` — writes a graph as JSON: `go run ./cmd/graph -fixture` or `-synthetic 100 -out DIR`.
- `cmd/lock` — computes `lock.json` (hashes of fetched crates and Go module zips).
- `aprime/` — backend A′ (JSON → bob-owned Nix library).
- `b/` — backend B (direct derivations).

Expected program output: `digest=<sha256("bob")> numhex=3432 proto_bytes=<n>`.
