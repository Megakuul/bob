#!/usr/bin/env bash
# merlin checks on ws/demo: errors (should be none), type of Geom.Vec.hash and Yojson.Safe.to_string.
cd "$(dirname "$0")/ws/demo"
M=.bob/bin/ocamlmerlin
e() { $M single errors -filename $1 < $1 | grep -o '"value":\[[^]]*\]' | cut -c1-200; }
ty() { $M single type-enclosing -position $2 -filename $1 < $1 | grep -o '"type":"[^"]*"' | head -1 | cut -c1-110; }
echo "app errors: $(e app/main.ml)"
echo "geom/show errors: $(e geom/show.ml)"
echo "type Geom.Vec.hash: $(ty app/main.ml 5:84)"
echo "type Yojson.Safe.to_string: $(ty app/main.ml 6:45)"
echo "type Fmt.pf in geom: $(ty geom/show.ml 1:28)"
