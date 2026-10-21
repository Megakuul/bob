# bob-owned mapping from the IR (see spikes/gen/ir) to nixpkgs builders.
# Contract: given a graph that passed ir.Validate, nothing in here may throw an evaluation error;
# any error that can happen is checked up front by `checks` (see bottom) and reported by the Go driver.
{ pkgs, graph }:
let
  inherit (pkgs) lib stdenv;

  # Store names allow [A-Za-z0-9+-._?=]; labels look like //dir:name.
  sanitize = s: lib.replaceStrings [ "/" ":" "@" " " ] [ "" "-" "-" "-" ] (lib.removePrefix "//" s);

  byLabel = builtins.listToAttrs (
    map (p: {
      name = p.label;
      value = mk p;
    }) graph.packages
  );
  rawByLabel = builtins.listToAttrs (
    map (p: {
      name = p.label;
      value = p;
    }) graph.packages
  );
  dep = l: byLabel.${l};
  depsOf =
    p:
    map (l: {
      pkg = rawByLabel.${l};
      drv = dep l;
    }) (p.deps or [ ]);
  nixpkgsOf = p: map (a: lib.getAttrFromPath (lib.splitString "." a) pkgs) (p.nixpkgs or [ ]);

  localSrc =
    p:
    builtins.path {
      path = /. + "${graph.root}/${p.src.path}";
      name = "${sanitize p.label}-src";
    };
  srcOf =
    p:
    if p.src ? fromLabel && p.src.fromLabel != "" then
      "${dep p.src.fromLabel}/${p.src.subdir or ""}"
    else
      localSrc p;

  # ---- fetch: fixed-output download, optionally unpacked -------------------------------------
  mkFetch =
    p:
    let
      f = p.fetch;
      file = pkgs.fetchurl {
        inherit (f) url hash;
        name = f.name;
      };
      unpackCmd =
        if f.unpack == "zip" then
          "${pkgs.unzip}/bin/unzip -q ${file}"
        else if f.unpack == "tar.gz" then
          "tar xzf ${file}"
        else
          "cp ${file} ./";
    in
    (pkgs.runCommand "${sanitize p.label}" { } ''
      mkdir $out && cd $out && ${unpackCmd}
    '').overrideAttrs
      (_: {
        passthru = { inherit file; };
      });

  # ---- cc_library ---------------------------------------------------------------------------
  mkCC =
    p:
    let
      c = p.cc;
      cc = if c.lang == "c++" then "$CXX" else "$CC";
      ccDeps = map (d: d.drv) (lib.filter (d: d.pkg.kind == "cc_library") (depsOf p));
      np = nixpkgsOf p;
    in
    stdenv.mkDerivation {
      name = sanitize p.label;
      src = srcOf p;
      nativeBuildInputs = lib.optional (np != [ ]) pkgs.pkg-config;
      buildInputs = np ++ ccDeps;
      dontConfigure = true;
      # Link flags of nixpkgs deps come from pkg-config modules named like the attribute (IR gap:
      # the IR has no explicit link libraries for nixpkgs deps).
      buildPhase = ''
        runHook preBuild
        pcflags=""
        for m in ${lib.concatStringsSep " " (p.nixpkgs or [ ])}; do
          if pkg-config --exists "$m" 2>/dev/null; then pcflags="$pcflags $(pkg-config --cflags --libs "$m")"; fi
        done
        objs=""
        for s in ${lib.concatStringsSep " " c.srcs}; do
          o="$(basename "$s").o"
          ${cc} -std=${c.std} -O2 -fPIC -I${c.hdrs} -c "$s" -o "$o" $pcflags
          objs="$objs $o"
        done
        ${
          if c.shared then
            ''${cc} -shared -Wl,-soname,lib${c.lib}.so -o lib${c.lib}.so $objs $pcflags''
          else
            ''$AR rcs lib${c.lib}.a $objs''
        }
        runHook postBuild
      '';
      installPhase = ''
        runHook preInstall
        mkdir -p $out/lib $out/include
        cp lib${c.lib}.* $out/lib/
        cp -r ${c.hdrs}/. $out/include/
        runHook postInstall
      '';
      passthru.bobLabel = p.label;
    };

  # ---- rust_crate (nixpkgs buildRustCrate, one derivation per crate) -------------------------
  mkRust =
    p:
    let
      r = p.rust;
      rustDeps = map (d: d.drv) (lib.filter (d: d.pkg.kind == "rust_crate") (depsOf p));
      crate = pkgs.buildRustCrate {
        crateName = r.crateName;
        version = r.version;
        src = srcOf p;
        edition = r.edition;
        features = r.features or [ ];
        dependencies = rustDeps;
        type = [ r.crateType ];
        libPath = r.root;
        # Without a Cargo.toml buildRustCrate still works from the attributes above.
      };
    in
    if r.crateType == "staticlib" then
      crate.overrideAttrs (old: {
        postInstall = (old.postInstall or "") + ''
          for a in $lib/lib/lib${r.crateName}-*.a; do ln -s "$a" $lib/lib/lib${r.crateName}.a; done
          ${lib.optionalString (r ? hdrs && r.hdrs != "") "mkdir -p $lib/include && cp -r ${r.hdrs}/. $lib/include/"}
        '';
      })
    else
      crate;

  # ---- proto_go -----------------------------------------------------------------------------
  mkProtoGo =
    p:
    pkgs.runCommand (sanitize p.label)
      {
        nativeBuildInputs = nixpkgsOf p;
        src = srcOf p;
      }
      ''
        mkdir $out
        cd $src
        protoc -I . --go_out=$out --go_opt=paths=source_relative ${lib.concatStringsSep " " p.proto.files}
      '';

  # ---- go_binary ----------------------------------------------------------------------------
  # Third-party modules are served to `go` through a file:// GOPROXY assembled from the
  # per-module fixed-output downloads, so no vendorHash is needed and every module is cached alone.
  goProxy =
    p:
    let
      reqs = p.go.requires;
      entry = r: ''
        d=$out/${r.path}/@v
        mkdir -p $d
        cp ${(dep r.label).file} $d/${r.version}.zip
        if [ -f "${dep r.label}/${r.path}@${r.version}/go.mod" ]; then
          cp "${dep r.label}/${r.path}@${r.version}/go.mod" $d/${r.version}.mod
        else
          echo "module ${r.path}" > $d/${r.version}.mod
        fi
        echo '{"Version":"${r.version}"}' > $d/${r.version}.info
        echo ${r.version} >> $d/list
      '';
    in
    pkgs.runCommand "${sanitize p.label}-goproxy" { } (lib.concatMapStrings entry reqs);

  mkGo =
    p:
    let
      g = p.go;
      ds = depsOf p;
      linkDeps = map (d: if d.pkg.kind == "rust_crate" then d.drv.lib else d.drv) (
        lib.filter (
          d: d.pkg.kind == "cc_library" || (d.pkg.kind == "rust_crate" && d.pkg.rust.crateType == "staticlib")
        ) ds
      );
      gens = map (x: "mkdir -p ${x.subdir} && cp ${dep x.label}/*.go ${x.subdir}/") (g.generated or [ ]);
      requireLines = lib.concatMapStrings (r: "\t${r.path} ${r.version}\n") g.requires;
      goMod = pkgs.writeText "go.mod" ''
        module ${g.modulePath}

        go ${g.goVersion}

        require (
        ${requireLines})
      '';
    in
    stdenv.mkDerivation {
      name = sanitize p.label;
      src = srcOf p;
      nativeBuildInputs = nixpkgsOf p;
      buildInputs = linkDeps;
      dontConfigure = true;
      buildPhase = ''
        runHook preBuild
        export HOME=$TMPDIR GOCACHE=$TMPDIR/gocache GOPATH=$TMPDIR/gopath
        export GOPROXY=file://${goProxy p} GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local
        export CGO_ENABLED=${if g.cgo then "1" else "0"}
        cp ${goMod} go.mod && chmod u+w go.mod
        ${lib.concatStringsSep "\n" gens}
        go build -trimpath -o ${g.binary} ${g.main}
        runHook postBuild
      '';
      installPhase = ''
        runHook preInstall
        install -Dm755 ${g.binary} $out/bin/${g.binary}
        runHook postInstall
      '';
      passthru.bobLabel = p.label;
    };

  mk =
    p:
    {
      fetch = mkFetch;
      cc_library = mkCC;
      rust_crate = mkRust;
      proto_go = mkProtoGo;
      go_binary = mkGo;
    }
    .${p.kind} p;

  # ---- checks: everything that could make evaluation fail, evaluated defensively -------------
  attrCheck =
    a:
    let
      path = lib.splitString "." a;
      exists = lib.hasAttrByPath path pkgs;
      v = lib.getAttrFromPath path pkgs;
      meta = if exists then (builtins.tryEval (v.meta or { })).value or { } else { };
      insecure = (meta.knownVulnerabilities or [ ]) != [ ];
    in
    {
      inherit exists;
      broken = exists && (meta.broken or false);
      inherit insecure;
      unfree = exists && !(meta.license.free or true);
      platformOk = !exists || lib.meta.availableOn pkgs.stdenv.hostPlatform v;
      version = if exists then (builtins.tryEval (v.version or "")).value or "" else "";
    };
in
{
  packages = byLabel;
  # label -> drvPath (instantiates the derivations; used for error mapping and eval timing)
  drvPaths = lib.mapAttrs (_: d: d.drvPath) byLabel;
  allDrvPaths = lib.concatStringsSep "\n" (lib.mapAttrsToList (_: d: d.drvPath) byLabel);
  # every derivation bob itself creates, with the package it belongs to (for error mapping)
  drvIndex = lib.concatMap (
    p:
    let
      d = byLabel.${p.label};
    in
    [
      {
        drv = d.drvPath;
        label = p.label;
        role = "package";
      }
    ]
    ++ lib.optional (p.kind == "fetch") {
      drv = d.file.drvPath;
      label = p.label;
      role = "download";
    }
    ++ lib.optional (p.kind == "go_binary") {
      drv = (goProxy p).drvPath;
      label = p.label;
      role = "go module proxy";
    }
  ) graph.packages;
  checks = builtins.listToAttrs (
    map (a: {
      name = a;
      value = attrCheck a;
    }) (lib.unique (lib.concatMap (p: p.nixpkgs or [ ]) graph.packages))
  );
}
