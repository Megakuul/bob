# Coverage of opam packages by nixpkgs ocamlPackages (name = pname), all and per graph.
import json, sys
nix = json.load(open(sys.argv[1]))
byp = {}
for attr, v in nix.items():
    byp.setdefault(v['pn'], v)
def stats(pairs):
    have = [p for p in pairs if p[0] in byp and not byp[p[0]]['br']]
    same = [p for p in have if byp[p[0]]['v'] == p[1]]
    return len(pairs), len(have), len(same)
allp = [l.split() for l in open(sys.argv[2]) if l.strip()]
n, h, s = stats(allp)
print(f"all opam packages (latest): {n}, in ocamlPackages: {h} ({100*h//n}%), same version: {s} ({100*s//n}%)")
for f in sys.argv[3:]:
    pairs = [l.strip().split('.', 1) for l in open(f) if l.strip()]
    n, h, s = stats(pairs)
    print(f"{f.split('/')[-1]}: {n} pkgs, in ocamlPackages {h}, exact version {s}")
