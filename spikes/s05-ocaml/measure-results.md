# S5 measure results (indicative, other spikes running), 2026-10-21

```
[8c] direct cold demo (11 opam + C + lib + exe): 16992 ms
[8c] direct no-op: 328 ms
[8c] direct incr-app: 1416 ms
[8c] direct incr-lib: 1976 ms
[8c] dune cold demo (11 opam + C + lib + exe): 16721 ms
[8c] dune no-op: 325 ms
[8c] dune incr-app: 1445 ms
[8c] dune incr-lib: 2171 ms
[8c] in-tree dune cold (own code only): 796 ms
[8c] in-tree dune incr-lib: 127 ms
[2c] direct cold demo (11 opam + C + lib + exe): 26692 ms
[2c] direct no-op: 305 ms
[2c] direct incr-app: 1353 ms
[2c] direct incr-lib: 1766 ms
[2c] dune cold demo (11 opam + C + lib + exe): 27310 ms
[2c] dune no-op: 280 ms
[2c] dune incr-app: 1426 ms
[2c] dune incr-lib: 2110 ms
[2c] in-tree dune cold (own code only): 814 ms
[2c] in-tree dune incr-lib: 155 ms
[8c] cold opam:cohttp-lwt-unix graph (60 opam packages, 56 fetches incl. download): 61 s

cmdliner fmt yojson lwt                  bob= 11 opam= 11 bob 0.03s opam 7.56s IDENTICAL
ppx_deriving ppx_deriving_yojson logs    bob= 11 opam= 11 bob 0.04s opam 7.53s IDENTICAL
cohttp-lwt-unix                          bob= 60 opam= 60 bob 0.08s opam 7.56s IDENTICAL
dream                                    bob=109 opam= 90 bob 0.15s opam 7.92s < asn1-combinators.0.3.3 < caqti-lwt.2.3.
core core_unix                           bob= 70 opam= 70 bob 0.07s opam 7.66s IDENTICAL
irmin-fs                                 bob= 36 opam= 36 bob 0.09s opam 8.17s IDENTICAL
ocaml-lsp-server                         bob= 32 opam= 30 bob 0.06s opam 10.13s < dune-rpc.3.21.1 < dyn.3.21.1 > dune-rp
eio_main                                 bob= 21 opam= 21 bob 0.06s opam 8.60s IDENTICAL
```
