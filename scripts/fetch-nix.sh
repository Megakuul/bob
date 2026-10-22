#!/bin/sh
# fetch-nix.sh — fetch the static Nix that bob embeds (SPEC R-NIX-020, R-ARCH-005, ADR 0002).
#
#   scripts/fetch-nix.sh [arm64|amd64 ...]      default: the Go host architecture
#
# For each architecture it downloads the NAR of nixpkgs `nixStatic` 2.34.9 at the pinned nixpkgs
# revision from cache.nixos.org, verifies the pinned sha256 of the download, the NAR hash and the
# sha256 of bin/nix, strips the binary when the host's strip understands it, and writes
#
#   pkg/engine/store/nixbin/<arch>/nix.gz       gzip -9 of the binary (Go compress/gzip)
#   pkg/engine/store/nixbin/<arch>/nix.sha256   hex sha256 of the uncompressed binary
#
# which `go build ./cmd/bob` embeds. Nothing is downloaded without a pinned hash; the download is
# cached as ~/.cache/bob-harness/blobs/<sha256> (HARNESS.md §2). Needs: curl, zstd, go.
#
# Licences (ATTRIBUTION.md, D52): Nix is LGPL-2.1-or-later and contains busybox (GPL-2.0) as its
# sandbox shell; source: https://github.com/NixOS/nix/tree/2.34.9 and the pinned derivation below
# (nix log / nix derivation show of the deriver), shipped with every release.
set -eu

# Pins, tools/pins.json style: nixpkgs github:NixOS/nixpkgs/cd4ea8d57de16f4810bc5f14207bac45fbf19f67,
# attribute legacyPackages.<system>.nixStatic (its bin/nix lives in the referenced output below).
pin() {
	case "$1" in
	arm64)
		STORE_PATH=/nix/store/2si9fs89xks87v6xvwvcqvmw6hg5814h-nix-static-aarch64-unknown-linux-musl-2.34.9
		DERIVER=s55n25gmlcyyhvchm0bdcfdqf2nqyxxp-nix-static-aarch64-unknown-linux-musl-2.34.9.drv
		URL=https://cache.nixos.org/nar/09zqq39nh3i8h8zhwy2hyakgh4190lf6j4hji3iajrbgmzyf4cid.nar.zst
		SHA256=6c95821c0ca67ea730bcb36047fe6fd447ffc50fb3672629fda57868e4a06ccf
		SIZE=14883430
		NAR_SHA256=2d32e2fcaf6f65a9e2881212691c052910f8a6f250780e3f82280e68d3c0f827
		BIN_SHA256=5ad65543237da8d625b531327fd29cf9f82742b2b51d2b8853ace2a81beb418b
		STRIPPED_SHA256=03c583b542e05231de45f1199fb0a6d412aa93c627955679c686c960e0090537 # GNU strip 2.46
		;;
	amd64)
		STORE_PATH=/nix/store/f272yi4gkasbxnpjsjn9c3zwdd58cjs7-nix-static-x86_64-unknown-linux-musl-2.34.9
		DERIVER=yxd2ay3ha18vcx4n0gw7f5d1f7xhps3w-nix-static-x86_64-unknown-linux-musl-2.34.9.drv
		URL=https://cache.nixos.org/nar/0qaxflk61412m47j81znk8zxlfnxcjlq55q5v4x612fmlwva1kk4.nar.zst
		SHA256=10731c2e9132896840bccd91bf6efba48bddc3ecf75a2422ddbbad5e702de014
		SIZE=15575572
		NAR_SHA256=64cea036a7d589603ad9059782a964dd3ada3f9af607240fa922906026755d61
		BIN_SHA256=1da6accaa8229f50ae5e07f229b13bfb1fcd52c539384da41b05e7d328a04258
		STRIPPED_SHA256= # not measured: no x86_64 strip on the arm64 build host
		;;
	*)
		echo "fetch-nix.sh: unknown architecture $1 (want arm64 or amd64)" >&2
		exit 2
		;;
	esac
}

here=$(cd "$(dirname "$0")/.." && pwd)
for tool in curl zstd go; do
	command -v "$tool" >/dev/null 2>&1 || {
		echo "fetch-nix.sh: $tool is required" >&2
		exit 2
	}
done
cache=${XDG_CACHE_HOME:-$HOME/.cache}/bob-harness/blobs
mkdir -p "$cache"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

# The NAR reader and gzip writer are plain Go (standard library), kept here so the script has no
# other dependency.
mkdir -p "$work/narx"
cat >"$work/narx/main.go" <<'EOF'
// narx reads a NAR on stdin, checks its sha256, and writes the regular file at the given path.
package main

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type reader struct {
	r   io.Reader
	off int64
}

func (r *reader) bytes(max int64) ([]byte, error) {
	var n [8]byte
	if _, err := io.ReadFull(r.r, n[:]); err != nil {
		return nil, err
	}
	l := int64(binary.LittleEndian.Uint64(n[:]))
	if l < 0 || l > max {
		return nil, fmt.Errorf("NAR string of %d bytes", l)
	}
	b := make([]byte, (l+7)/8*8)
	if _, err := io.ReadFull(r.r, b); err != nil {
		return nil, err
	}
	return b[:l], nil
}

func (r *reader) str() (string, error) { b, err := r.bytes(4096); return string(b), err }

func (r *reader) expect(want ...string) error {
	for _, w := range want {
		got, err := r.str()
		if err != nil {
			return err
		}
		if got != w {
			return fmt.Errorf("NAR: got %q, want %q", got, w)
		}
	}
	return nil
}

// node parses one node at path p; the file at want is returned in *found.
func (r *reader) node(p, want string, found *[]byte) error {
	if err := r.expect("(", "type"); err != nil {
		return err
	}
	t, err := r.str()
	if err != nil {
		return err
	}
	switch t {
	case "regular":
		tag, err := r.str()
		if err != nil {
			return err
		}
		if tag == "executable" {
			if err := r.expect("", "contents"); err != nil {
				return err
			}
		} else if tag != "contents" {
			return fmt.Errorf("NAR: unexpected %q", tag)
		}
		b, err := r.bytes(1 << 31)
		if err != nil {
			return err
		}
		if p == want {
			*found = b
		}
		return r.expect(")")
	case "symlink":
		if err := r.expect("target"); err != nil {
			return err
		}
		if _, err := r.str(); err != nil {
			return err
		}
		return r.expect(")")
	case "directory":
		for {
			tag, err := r.str()
			if err != nil {
				return err
			}
			if tag == ")" {
				return nil
			}
			if tag != "entry" {
				return fmt.Errorf("NAR: unexpected %q", tag)
			}
			if err := r.expect("(", "name"); err != nil {
				return err
			}
			name, err := r.str()
			if err != nil {
				return err
			}
			if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
				return fmt.Errorf("NAR: bad entry name %q", name)
			}
			if err := r.expect("node"); err != nil {
				return err
			}
			if err := r.node(strings.TrimPrefix(p+"/"+name, "/"), want, found); err != nil {
				return err
			}
			if err := r.expect(")"); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("NAR: unknown node type %q", t)
}

func run() error {
	if len(os.Args) != 4 {
		return errors.New("usage: narx <nar-sha256> <path> <out>")
	}
	h := sha256.New()
	r := &reader{r: io.TeeReader(bufio.NewReaderSize(os.Stdin, 1<<20), h)}
	if err := r.expect("nix-archive-1"); err != nil {
		return err
	}
	var found []byte
	if err := r.node("", os.Args[2], &found); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, r.r); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != os.Args[1] {
		return fmt.Errorf("NAR sha256 %s, pinned %s", got, os.Args[1])
	}
	if found == nil {
		return fmt.Errorf("NAR has no regular file %s", os.Args[2])
	}
	return os.WriteFile(os.Args[3], found, 0o755)
}

// gz compresses stdin to stdout with gzip level 9 and no name or mtime (reproducible).
func gz() error {
	w, err := gzip.NewWriterLevel(os.Stdout, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, bufio.NewReaderSize(os.Stdin, 1<<20)); err != nil {
		return err
	}
	return w.Close()
}

func main() {
	var err error
	if len(os.Args) == 2 && os.Args[1] == "gzip" {
		err = gz()
	} else {
		err = run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "narx:", err)
		os.Exit(1)
	}
}
EOF
(cd "$work/narx" && printf 'module narx\n\ngo 1.24\n' >go.mod && GOWORK=off CGO_ENABLED=0 go build -o "$work/narx-bin" .)

sha256() { sha256sum "$1" | cut -d' ' -f1; }

archs=${*:-$(go env GOARCH)}
for arch in $archs; do
	pin "$arch"
	blob=$cache/$SHA256
	if [ ! -f "$blob" ] || [ "$(sha256 "$blob")" != "$SHA256" ]; then
		echo "fetch-nix.sh: downloading $URL ($SIZE bytes)" >&2
		curl -fsSL --retry 3 -o "$work/dl" "$URL"
		got=$(sha256 "$work/dl")
		if [ "$got" != "$SHA256" ]; then
			echo "fetch-nix.sh: $URL has sha256 $got, pinned $SHA256" >&2
			exit 1
		fi
		mv "$work/dl" "$blob"
	fi
	zstd -dcq "$blob" | "$work/narx-bin" "$NAR_SHA256" bin/nix "$work/nix"
	got=$(sha256 "$work/nix")
	if [ "$got" != "$BIN_SHA256" ]; then
		echo "fetch-nix.sh: bin/nix of $STORE_PATH has sha256 $got, pinned $BIN_SHA256" >&2
		exit 1
	fi
	# Strip with the host's strip (deterministic for a given binutils); a strip that does not know
	# the architecture leaves the binary as it is. STRIP=<tool> selects another one.
	if ${STRIP:-strip} -o "$work/nix.stripped" "$work/nix" 2>/dev/null; then
		mv "$work/nix.stripped" "$work/nix"
		got=$(sha256 "$work/nix")
		if [ -n "$STRIPPED_SHA256" ] && [ "$got" != "$STRIPPED_SHA256" ]; then
			echo "fetch-nix.sh: warning: stripped bin/nix has sha256 $got, recorded $STRIPPED_SHA256 (other strip version)" >&2
		fi
	else
		echo "fetch-nix.sh: warning: ${STRIP:-strip} cannot strip the $arch binary; embedding it unstripped" >&2
	fi
	out=$here/pkg/engine/store/nixbin/$arch
	mkdir -p "$out"
	"$work/narx-bin" gzip <"$work/nix" >"$work/nix.gz"
	sha256 "$work/nix" >"$work/nix.sha256"
	mv "$work/nix.gz" "$out/nix.gz"
	mv "$work/nix.sha256" "$out/nix.sha256"
	echo "fetch-nix.sh: $arch: $out/nix.gz ($(wc -c <"$out/nix.gz") bytes, binary sha256 $(cat "$out/nix.sha256"), from $DERIVER)" >&2
done
