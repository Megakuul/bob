// Package cache serves the Nix binary cache of an orchestrator (SPEC §6.6, ADR 0014): the
// /nix/ endpoints, the per-object visibility documents (D58) and the streaming download
// proxy (D61). It also holds the Nix formats bob writes: narinfo, Nix base32, signatures
// (byte-identical to `nix store sign`), NAR walking and `.ls` listings. It never fetches from
// another cache (R-REG-005): the only network peer is the artifacts bucket.
package cache
