{
  # Install bob with Nix (ADR 0002a): `nix profile add github:megakuul/bob`, or on NixOS
  # `environment.systemPackages = [ inputs.bob.packages.${pkgs.system}.default ];`.
  #
  # This build does not embed Nix: it is compiled with -tags nixsystem and records the absolute
  # path of a static Nix from its own closure (pkg/engine/store/nixsystem.go). bob copies that
  # binary into its state directory on first use and runs it exactly like the embedded one (same
  # private rootless store, same nix.conf). The AppImage and scripts/bootstrap.sh keep the
  # embedded Nix (ADR 0002). This file is bob's distribution recipe; bob itself is still built
  # by bob from bob.go (R-ARCH-005), and no user project ever sees a .nix file.
  description = "bob: typed Go build definitions with Nix as an invisible build engine";

  inputs = {
    # Go 1.26.9 (go.mod's go directive). It must be the Go that builds cmd/bob-builder bit for
    # bit as pkg/engine/store/builders.go records it (R-NIX-015): the build below regenerates
    # the builder and fails when its store path differs. Bump only together with that table.
    nixpkgs.url = "github:NixOS/nixpkgs/e513db7b0a65def6f6e17bb55195dfe510871944";
    # nixStatic 2.34.9, the same Nix scripts/fetch-nix.sh embeds (tools/pins.json nixpkgs pin;
    # cached on cache.nixos.org for both systems). bob refuses a Nix outside the 2.34 series.
    nixpkgs-nix.url = "github:NixOS/nixpkgs/cd4ea8d57de16f4810bc5f14207bac45fbf19f67";
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-nix,
    }:
    let
      lib = nixpkgs.lib;
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = f: lib.genAttrs systems (system: f system nixpkgs.legacyPackages.${system});
      fs = lib.fileset;

      # The Go sources of the three binaries. Generated binaries are left out on purpose, so a
      # `path:` build of a working tree equals a build from git: the static Nix that
      # scripts/fetch-nix.sh writes is not used by this variant, the builder is regenerated
      # below, and the web UI is built from web/.
      goSrc = fs.toSource {
        root = ./.;
        fileset = fs.difference (fs.unions [
          ./go.mod
          ./go.sum
          ./cmd
          ./pkg
          ./internal
          # `go mod vendor` resolves the imports of every package's tests, some of which live here.
          ./tests
          ./LICENSE
          ./ATTRIBUTION.md
        ]) (fs.unions [
          ./pkg/engine/store/nixbin
          (fs.fileFilter (f: f.name == "bob-builder.gz") ./pkg/engine/store/builderbin)
          (fs.fileFilter (f: f.name != ".gitignore") ./internal/ui/dist)
        ]);
      };

      webSrc = fs.toSource {
        root = ./web;
        fileset = fs.difference ./web (fs.unions [
          (fs.maybeMissing ./web/node_modules)
          (fs.maybeMissing ./web/build)
          (fs.maybeMissing ./web/.svelte-kit)
          ./web/bob.go
        ]);
      };

      # Go packages whose tests pass inside the Nix build sandbox (the hermetic list of
      # bob.go's ci pipeline).
      hermetic = [
        "cmd/bob-builder/genbuilders"
        "internal/api"
        "internal/auth"
        "internal/build/jobspec"
        "internal/forge/github"
        "internal/forge/gitlab"
        "internal/http"
        "internal/isolation"
        "internal/safe"
        "internal/server"
        "internal/ui"
        "pkg/bob/ext"
        "pkg/cc/bop"
        "pkg/cc/gen"
        "pkg/cli/devenv"
        "pkg/cli/remote"
        "pkg/connect/gen"
        "pkg/engine"
        "pkg/engine/drv"
        "pkg/engine/eval/evalbox"
        "pkg/engine/fetch"
        "pkg/engine/ir"
        "pkg/engine/lock"
        "pkg/engine/materialise"
        "pkg/engine/nar"
        "pkg/golang"
        "pkg/golang/bop"
        "pkg/golang/resolve"
        "pkg/graphql"
        "pkg/graphql/gen"
        "pkg/nixpkgs"
        "pkg/npm/bop"
        "pkg/npm/gen"
        "pkg/ocaml/bop"
        "pkg/ocaml/gen"
        "pkg/ocaml/resolve"
        "pkg/openapi"
        "pkg/openapi/gen"
        "pkg/output/appimage"
        "pkg/output/appimage/bop"
        "pkg/output/oci/bop"
        "pkg/output/oci/gen"
        "pkg/output/tarball/gen"
        "pkg/proto"
        "pkg/proto/gen"
        "pkg/release/bop"
        "pkg/rust"
        "pkg/rust/bop"
        "pkg/rust/gen"
        "pkg/services/postgres"
      ];

      mkPackages =
        system: pkgs:
        let
          go = pkgs.go_1_26;
          buildGoModule = pkgs.buildGoModule.override { inherit go; };
          # Only the static `nix` binary of nixStatic. Its own store path is written into it
          # (default bin/libexec dirs, unused by bob: bob runs a copy in <home>/bin inside its own
          # store, where that path does not exist either, exactly like the embedded binary), and
          # that output references nixStatic's -dev outputs (~440 MiB). Dropping the
          # self-reference keeps bob's runtime closure at the binary (~40 MiB) plus two small data
          # paths.
          nixStatic =
            pkgs.runCommand "bob-nix-static-${nixpkgs-nix.legacyPackages.${system}.nixStatic.version}"
              {
                nativeBuildInputs = [ pkgs.removeReferencesTo ];
                src = nixpkgs-nix.legacyPackages.${system}.nixStatic;
              }
              ''
                real=$(readlink -f "$src/bin/nix")
                install -Dm555 "$real" "$out/bin/nix"
                remove-references-to -t "$(dirname "$(dirname "$real")")" -t "$src" "$out/bin/nix"
              '';
          version = "0-unstable-" + (self.shortRev or self.dirtyShortRev or "dirty");
          meta = {
            homepage = "https://github.com/megakuul/bob";
            license = lib.licenses.agpl3Only;
            platforms = systems;
          };

          common = {
            inherit version;
            src = goSrc;
            # The module's dependencies (go.sum); recompute with `nix build .#bob` after a
            # go.sum change (the error prints the new hash).
            vendorHash = "sha256-xKaWwSZwF1ty7IZTZzWj13q1Vn3NunLVlTYMGr+XyPI=";
            env = {
              CGO_ENABLED = "0";
              GOWORK = "off";
            };
            doCheck = false;
            # The preBuild hooks below need the vendored modules; fetching them needs neither.
            overrideModAttrs = _: { preBuild = ""; };
          };

          # cmd/bob-builder for both systems and the {system → store path} table, as
          # scripts/bootstrap.sh does it: the table must equal the committed builders.go.
          generateBuilder = ''
            cp pkg/engine/store/builders.go "$TMPDIR/builders.go.committed"
            go generate ./pkg/engine/store
            if ! cmp -s pkg/engine/store/builders.go "$TMPDIR/builders.go.committed"; then
              echo "flake: the bob-builder built here is not the one pkg/engine/store/builders.go records" >&2
              echo "flake: (the nixpkgs input's Go must be the Go that generated the table)" >&2
              diff "$TMPDIR/builders.go.committed" pkg/engine/store/builders.go >&2 || true
              exit 1
            fi
          '';

          bob = buildGoModule (
            common
            // {
              pname = "bob";
              subPackages = [ "cmd/bob" ];
              tags = [ "nixsystem" ];
              ldflags = [
                "-s"
                "-w"
                "-X github.com/megakuul/bob/pkg/engine/store.systemNix=${nixStatic}/bin/nix"
              ];
              preBuild = generateBuilder;
              postInstall = ''
                install -Dm644 LICENSE ATTRIBUTION.md -t "$out/share/doc/bob"
              '';
              passthru = { inherit nixStatic; };
              meta = meta // {
                description = "Typed Go build definitions with Nix as an invisible build engine";
                mainProgram = "bob";
              };
            }
          );

          # The static web UI that internal/ui embeds (R-UI-001), built from web/'s lock file
          # with lifecycle scripts off.
          web = pkgs.buildNpmPackage {
            pname = "bob-web";
            inherit version;
            src = webSrc;
            nodejs = pkgs.nodejs_22;
            npmDepsHash = "sha256-UTEcLjvMuwivskM5oeUwgMzUFfE8lUflHKZGGN/oUes=";
            npmFlags = [ "--ignore-scripts" ];
            buildPhase = ''
              runHook preBuild
              node node_modules/vite/bin/vite.js build
              runHook postBuild
            '';
            installPhase = ''
              runHook preInstall
              cp -r build "$out"
              runHook postInstall
            '';
          };

          bob-server = buildGoModule (
            common
            // {
              pname = "bob-server";
              subPackages = [ "cmd/bob-server" ];
              # Workers run builds with the same store package as bob, so the server is the same
              # nixsystem variant with the same Nix.
              tags = [ "nixsystem" ];
              ldflags = [
                "-s"
                "-w"
                "-X github.com/megakuul/bob/pkg/engine/store.systemNix=${nixStatic}/bin/nix"
              ];
              preBuild = generateBuilder + ''
                cp -r ${web}/. internal/ui/dist/
                chmod -R u+w internal/ui/dist
              '';
              passthru = { inherit nixStatic web; };
              postInstall = ''
                install -Dm644 LICENSE ATTRIBUTION.md -t "$out/share/doc/bob-server"
              '';
              meta = meta // {
                description = "bob's build and cache server (S3 only)";
                mainProgram = "bob-server";
              };
            }
          );

          # Hermetic unit tests, plus the nixsystem variant's tests of pkg/engine/store that need
          # no namespace.
          unit = bob.overrideAttrs (_: {
            buildPhase = ''
              runHook preBuild
              # Three tests depend on bob's own build sandbox rather than nixpkgs': tools/pins.json
              # (TestRestoreAdmin), coreutils on PATH (TestScriptsRunAndKeepOnlyUsedAddons) and a
              # writable module cache at cleanup (TestBuildListEqualsGoCommand). They run in
              # `bob pipeline run ci`.
              go test -count=1 -tags nixsystem \
                -skip 'TestRestoreAdmin|TestScriptsRunAndKeepOnlyUsedAddons|TestBuildListEqualsGoCommand' \
                ${lib.concatMapStringsSep " " (p: "./" + p) hermetic}
              go test -count=1 -tags nixsystem ./pkg/engine/store \
                -run 'TestParseNixVersion|TestSystemNixRefusals|TestSystemNixVersionGate'
              runHook postBuild
            '';
            installPhase = "touch $out";
            postInstall = "";
          });
        in
        {
          inherit
            bob
            bob-server
            web
            unit
            ;
        };

      perSystem = forAllSystems mkPackages;
    in
    {
      packages = forAllSystems (
        system: _: {
          default = perSystem.${system}.bob;
          inherit (perSystem.${system}) bob bob-server;
        }
      );

      apps = forAllSystems (
        system: _: {
          default = {
            type = "app";
            program = lib.getExe perSystem.${system}.bob;
            meta.description = "bob, the build tool";
          };
          bob-server = {
            type = "app";
            program = lib.getExe perSystem.${system}.bob-server;
            meta.description = "bob's build and cache server";
          };
        }
      );

      # Adds bob and bob-server, built as above (with this flake's pinned Go and Nix, which the
      # builder table and the Nix version gate need), to a nixpkgs instance.
      overlays.default = final: _prev: {
        bob = self.packages.${final.stdenv.hostPlatform.system}.bob;
        bob-server = self.packages.${final.stdenv.hostPlatform.system}.bob-server;
      };

      # Tools for working on bob: the Go that reproduces bob-builder, node for web/ and
      # website/, and what scripts/fetch-nix.sh and scripts/bootstrap.sh need.
      devShells = forAllSystems (
        system: pkgs: {
          default = pkgs.mkShell {
            packages = [
              pkgs.go_1_26
              pkgs.nodejs_22
              pkgs.curl
              pkgs.zstd
              pkgs.git
              pkgs.binutils
            ];
            env.CGO_ENABLED = "0";
          };
        }
      );

      checks = forAllSystems (
        system: _: {
          inherit (perSystem.${system}) bob bob-server unit;
        }
      );
    };
}
