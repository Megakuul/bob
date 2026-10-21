| Workload | Mode | Measurement | [8c/15G] | [2c/4G] |
|---|---|---|---|---|
| fixture | package | cold incl. std | 25.04 s / 277 MB / 35 | 48.65 s / 262 MB / 35 |
| fixture | cacheprog | cold incl. std | 25.25 s / 282 MB / 35 | 52.66 s / 268 MB / 35 |
| fixture | single | derivations (go / all) | 1 / 13 | 1 / 13 |
| fixture | single | cold | 11.40 s / 278 MB / 3 | 20.57 s / 273 MB / 3 |
| fixture | single | no-op | 0.22 s / 35 MB / 2 | 0.22 s / 34 MB / 2 |
| fixture | single | incr main.go | 11.59 s / 268 MB / 3 | 20.90 s / 275 MB / 3 |
| fixture | single | incr internal/msg | 11.75 s / 279 MB / 3 | 21.47 s / 267 MB / 3 |
| fixture | module | derivations (go / all) | 4 / 21 | 4 / 21 |
| fixture | module | cold | 6.25 s / 150 MB / 6 | 6.73 s / 148 MB / 6 |
| fixture | module | no-op | 0.28 s / 34 MB / 2 | 0.27 s / 35 MB / 2 |
| fixture | module | incr main.go | 3.06 s / 109 MB / 3 | 2.94 s / 107 MB / 3 |
| fixture | module | incr internal/msg | 3.23 s / 110 MB / 3 | 2.91 s / 106 MB / 3 |
| fixture | hybrid | derivations (go / all) | 6 / 23 | 6 / 23 |
| fixture | hybrid | cold | 6.11 s / 149 MB / 8 | 7.15 s / 151 MB / 8 |
| fixture | hybrid | no-op | 0.26 s / 35 MB / 2 | 0.27 s / 35 MB / 2 |
| fixture | hybrid | incr main.go | 2.87 s / 108 MB / 3 | 2.89 s / 106 MB / 3 |
| fixture | hybrid | incr internal/msg | 3.28 s / 108 MB / 4 | 2.99 s / 106 MB / 4 |
| fixture | package | derivations (go / all) | 32 / 49 | 32 / 49 |
| fixture | package | cold | 6.62 s / 146 MB / 34 | 7.51 s / 152 MB / 34 |
| fixture | package | no-op | 0.27 s / 34 MB / 2 | 0.29 s / 34 MB / 2 |
| fixture | package | incr main.go | 3.02 s / 109 MB / 3 | 2.85 s / 106 MB / 3 |
| fixture | package | incr internal/msg | 3.47 s / 107 MB / 4 | 3.06 s / 107 MB / 4 |
| fixture | cacheprog | derivations (go / all) | 32 / 49 | 32 / 49 |
| fixture | cacheprog | cold | 9.25 s / 163 MB / 34 | 11.15 s / 152 MB / 34 |
| fixture | cacheprog | no-op | 0.39 s / 35 MB / 2 | 0.36 s / 35 MB / 2 |
| fixture | cacheprog | incr main.go | 3.21 s / 109 MB / 3 | 3.44 s / 108 MB / 3 |
| fixture | cacheprog | incr internal/msg | 3.73 s / 109 MB / 4 | 3.58 s / 108 MB / 4 |
| fixture | cutoff | derivations (go / all) | 5 / 22 | 5 / 22 |
| fixture | cutoff | cold | 7.48 s / 148 MB / 3 | 8.10 s / 152 MB / 3 |
| fixture | cutoff | no-op | 0.24 s / 34 MB / 2 | 0.27 s / 34 MB / 2 |
| fixture | cutoff | incr main.go | 3.48 s / 110 MB / 3 | 3.36 s / 108 MB / 3 |
| fixture | cutoff | incr internal/msg | 2.62 s / 108 MB / 3 | 2.54 s / 107 MB / 3 |
| syn200 | single | derivations (go / all) | 1 / 1 | 1 / 1 |
| syn200 | single | cold | 7.19 s / 283 MB / 1 | 14.29 s / 273 MB / 1 |
| syn200 | single | no-op | 0.18 s / 35 MB / 0 | 0.19 s / 33 MB / 0 |
| syn200 | single | incr main | 7.20 s / 288 MB / 1 | 14.21 s / 272 MB / 1 |
| syn200 | single | incr top layer (1 dependent) | 7.27 s / 284 MB / 1 | 14.25 s / 266 MB / 1 |
| syn200 | single | incr middle layer | 7.14 s / 284 MB / 1 | 14.18 s / 263 MB / 1 |
| syn200 | single | incr bottom layer | 7.32 s / 289 MB / 1 | 14.45 s / 276 MB / 1 |
| syn200 | module | derivations (go / all) | 1 / 3 | 1 / 3 |
| syn200 | module | cold | 2.32 s / 73 MB / 1 | 5.97 s / 71 MB / 1 |
| syn200 | module | no-op | 0.22 s / 34 MB / 0 | 0.26 s / 34 MB / 0 |
| syn200 | module | incr main | 2.34 s / 72 MB / 1 | 6.18 s / 71 MB / 1 |
| syn200 | module | incr top layer (1 dependent) | 2.36 s / 72 MB / 1 | 5.97 s / 72 MB / 1 |
| syn200 | module | incr middle layer | 2.39 s / 72 MB / 1 | 6.07 s / 72 MB / 1 |
| syn200 | module | incr bottom layer | 2.34 s / 73 MB / 1 | 6.06 s / 71 MB / 1 |
| syn200 | package | derivations (go / all) | 201 / 203 | 201 / 203 |
| syn200 | package | cold | 10.22 s / 136 MB / 201 | 13.24 s / 136 MB / 201 |
| syn200 | package | no-op | 0.43 s / 34 MB / 0 | 0.41 s / 34 MB / 0 |
| syn200 | package | incr main | 1.22 s / 90 MB / 1 | 1.15 s / 91 MB / 1 |
| syn200 | package | incr top layer (1 dependent) | 1.38 s / 92 MB / 2 | 1.36 s / 93 MB / 2 |
| syn200 | package | incr middle layer | 2.07 s / 107 MB / 16 | 2.08 s / 97 MB / 16 |
| syn200 | package | incr bottom layer | 3.03 s / 111 MB / 36 | 3.12 s / 101 MB / 36 |
| syn200 | cacheprog | derivations (go / all) | 201 / 203 | 201 / 203 |
| syn200 | cacheprog | cold | 16.42 s / 150 MB / 201 | 35.16 s / 140 MB / 201 |
| syn200 | cacheprog | no-op | 0.70 s / 35 MB / 0 | 0.64 s / 35 MB / 0 |
| syn200 | cacheprog | incr main | 3.04 s / 93 MB / 1 | 2.45 s / 94 MB / 1 |
| syn200 | cacheprog | incr top layer (1 dependent) | 3.45 s / 98 MB / 2 | 2.84 s / 98 MB / 2 |
| syn200 | cacheprog | incr middle layer | 4.91 s / 114 MB / 16 | 5.32 s / 101 MB / 16 |
| syn200 | cacheprog | incr bottom layer | 6.41 s / 113 MB / 36 | 8.33 s / 105 MB / 36 |
| syn200 | cutoff | derivations (go / all) | 20 / 22 | 20 / 22 |
| syn200 | cutoff | cold | 13.86 s / 125 MB / 1 | 15.99 s / 104 MB / 1 |
| syn200 | cutoff | no-op | 0.41 s / 35 MB / 0 | 0.39 s / 35 MB / 0 |
| syn200 | cutoff | incr main | 1.74 s / 91 MB / 1 | 1.66 s / 90 MB / 1 |
| syn200 | cutoff | incr top layer (1 dependent) | 1.70 s / 89 MB / 1 | 1.65 s / 91 MB / 1 |
| syn200 | cutoff | incr middle layer | 1.70 s / 89 MB / 1 | 1.60 s / 90 MB / 1 |
| syn200 | cutoff | incr bottom layer | 1.76 s / 89 MB / 1 | 1.63 s / 90 MB / 1 |
| eval | package | graph→drvs N=200 (new) | 1.03 s / 35 MB / 201 | 1.14 s / 36 MB / 201 |
| eval | package | graph→drvs N=200 (again) | 0.38 s / 33 MB / 201 | 0.37 s / 32 MB / 201 |
| eval | package | graph→drvs N=1000 (new) | 3.53 s / 40 MB / 1001 | 3.43 s / 41 MB / 1001 |
| eval | package | graph→drvs N=1000 (again) | 1.33 s / 33 MB / 1001 | 1.19 s / 34 MB / 1001 |

Run 2026-10-21T08:51:05Z on vibe-engine. Cells: wall / peak RSS / derivations built.
