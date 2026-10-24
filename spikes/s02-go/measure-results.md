| Workload | Mode | Measurement | [8c/15G] | [2c/4G] |
|---|---|---|---|---|
| fixture | package | cold incl. std | 44.68 s / 268 MB / 33 | 66.72 s / 266 MB / 33 |
| fixture | cacheprog | cold incl. std | 60.26 s / 280 MB / 33 | 62.42 s / 262 MB / 33 |
| fixture | single | derivations (go / all) | 1 / 13 | 1 / 13 |
| fixture | single | cold | 22.68 s / 272 MB / 1 | 31.31 s / 269 MB / 1 |
| fixture | single | no-op | 0.47 s / 36 MB / 0 | 0.35 s / 34 MB / 0 |
| fixture | single | incr main.go | 26.22 s / 303 MB / 1 | 41.90 s / 282 MB / 1 |
| fixture | single | incr internal/msg | 19.70 s / 286 MB / 1 | 42.66 s / 286 MB / 1 |
| fixture | module | derivations (go / all) | 4 / 21 | 4 / 21 |
| fixture | module | cold | 7.47 s / 154 MB / 4 | 10.49 s / 156 MB / 4 |
| fixture | module | no-op | 0.26 s / 34 MB / 0 | 0.29 s / 35 MB / 0 |
| fixture | module | incr main.go | 3.50 s / 109 MB / 1 | 3.65 s / 108 MB / 1 |
| fixture | module | incr internal/msg | 3.84 s / 111 MB / 1 | 3.64 s / 111 MB / 1 |
| fixture | hybrid | derivations (go / all) | 6 / 23 | 6 / 23 |
| fixture | hybrid | cold | 9.09 s / 150 MB / 6 | 11.88 s / 159 MB / 6 |
| fixture | hybrid | no-op | 0.22 s / 36 MB / 0 | 0.58 s / 35 MB / 0 |
| fixture | hybrid | incr main.go | 3.48 s / 110 MB / 1 | 4.31 s / 109 MB / 1 |
| fixture | hybrid | incr internal/msg | 4.75 s / 109 MB / 2 | 3.97 s / 110 MB / 2 |
| fixture | package | derivations (go / all) | 32 / 49 | 32 / 49 |
| fixture | package | cold | 27.74 s / 154 MB / 32 | 12.67 s / 156 MB / 32 |
| fixture | package | no-op | 0.95 s / 35 MB / 0 | 0.50 s / 34 MB / 0 |
| fixture | package | incr main.go | 6.43 s / 111 MB / 1 | 3.06 s / 109 MB / 1 |
| fixture | package | incr internal/msg | 8.09 s / 111 MB / 2 | 4.62 s / 108 MB / 2 |
| fixture | cacheprog | derivations (go / all) | 32 / 49 | 32 / 49 |
| fixture | cacheprog | cold | 41.43 s / 165 MB / 32 | 19.52 s / 157 MB / 32 |
| fixture | cacheprog | no-op | 1.02 s / 36 MB / 0 | 0.58 s / 34 MB / 0 |
| fixture | cacheprog | incr main.go | 16.66 s / 111 MB / 1 | 4.30 s / 109 MB / 1 |
| fixture | cacheprog | incr internal/msg | 9.87 s / 109 MB / 2 | 4.93 s / 111 MB / 2 |
| syn200 | single | derivations (go / all) | 1 / 1 | 1 / 1 |
| syn200 | single | cold | 16.79 s / 290 MB / 1 | 24.58 s / 286 MB / 1 |
| syn200 | single | no-op | 0.27 s / 34 MB / 0 | 0.27 s / 34 MB / 0 |
| syn200 | single | incr main | 19.42 s / 283 MB / 1 | 27.26 s / 300 MB / 1 |
| syn200 | single | incr top layer (1 dependent) | 14.42 s / 274 MB / 1 | 25.82 s / 289 MB / 1 |
| syn200 | single | incr middle layer | 8.37 s / 276 MB / 1 | 25.55 s / 291 MB / 1 |
| syn200 | single | incr bottom layer | 12.10 s / 276 MB / 1 | 26.06 s / 288 MB / 1 |
| syn200 | module | derivations (go / all) | 1 / 3 | 1 / 3 |
| syn200 | module | cold | 3.53 s / 74 MB / 1 | 8.92 s / 71 MB / 1 |
| syn200 | module | no-op | 0.44 s / 34 MB / 0 | 0.22 s / 34 MB / 0 |
| syn200 | module | incr main | 3.59 s / 72 MB / 1 | 5.75 s / 71 MB / 1 |
| syn200 | module | incr top layer (1 dependent) | 4.33 s / 73 MB / 1 | 5.84 s / 74 MB / 1 |
| syn200 | module | incr middle layer | 4.83 s / 73 MB / 1 | 5.70 s / 71 MB / 1 |
| syn200 | module | incr bottom layer | 5.90 s / 73 MB / 1 | 5.68 s / 71 MB / 1 |
| syn200 | package | derivations (go / all) | 201 / 203 | 201 / 203 |
| syn200 | package | cold | 35.16 s / 143 MB / 201 | 12.66 s / 133 MB / 201 |
| syn200 | package | no-op | 2.27 s / 34 MB / 0 | 0.38 s / 33 MB / 0 |
| syn200 | package | incr main | 3.01 s / 85 MB / 1 | 1.19 s / 90 MB / 1 |
| syn200 | package | incr top layer (1 dependent) | 4.08 s / 91 MB / 2 | 1.32 s / 93 MB / 2 |
| syn200 | package | incr middle layer | 7.34 s / 108 MB / 16 | 2.04 s / 101 MB / 16 |
| syn200 | package | incr bottom layer | 14.59 s / 73 MB / 36 | 3.00 s / 100 MB / 36 |
| syn200 | cacheprog | derivations (go / all) | 201 / 203 | 201 / 203 |
| syn200 | cacheprog | cold | 85.07 s / 293 MB / 202 | 32.80 s / 141 MB / 201 |
| syn200 | cacheprog | no-op | 1.70 s / 34 MB / 0 | 0.63 s / 35 MB / 0 |
| syn200 | cacheprog | incr main | 3.34 s / 94 MB / 1 | 2.44 s / 94 MB / 1 |
| syn200 | cacheprog | incr top layer (1 dependent) | 4.83 s / 98 MB / 2 | 2.86 s / 96 MB / 2 |
| syn200 | cacheprog | incr middle layer | 5.90 s / 79 MB / 16 | 5.20 s / 104 MB / 16 |
| syn200 | cacheprog | incr bottom layer | 8.29 s / 116 MB / 36 | 7.82 s / 103 MB / 36 |
| eval | package | graph→drvs N=200 (new) | 1.62 s / 35 MB / 201 | 0.98 s / 35 MB / 201 |
| eval | package | graph→drvs N=200 (again) | 0.37 s / 32 MB / 201 | 0.33 s / 33 MB / 201 |
| eval | package | graph→drvs N=1000 (new) | 6.76 s / 39 MB / 1001 | 3.68 s / 39 MB / 1001 |
| eval | package | graph→drvs N=1000 (again) | 2.10 s / 34 MB / 1001 | 1.14 s / 34 MB / 1001 |

Run 2026-10-20T22:08:47Z on vibe-engine. Cells: wall / peak RSS / derivations built.
