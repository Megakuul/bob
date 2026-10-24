| Measurement (backend B) | [8c/15G] | [2c/4G] |
|---|---|---|
| fixture cold | 11.77 s / 279 MB | 21.96 s / 273 MB |
| fixture no-op | 0.33 s / 51 MB | 0.45 s / 53 MB |
| fixture incr (foo.cpp) | 12.48 s / 287 MB | 22.81 s / 268 MB |
| graph→drvs N=10 (new) | 0.44 s / 49 MB | 0.44 s / 52 MB |
| graph→drvs N=10 (again) | 0.26 s / 48 MB | 0.38 s / 54 MB |
| graph→drvs N=100 (new) | 0.72 s / 50 MB | 0.74 s / 57 MB |
| graph→drvs N=100 (again) | 0.35 s / 51 MB | 0.45 s / 55 MB |
| graph→drvs N=1000 (new) | 3.03 s / 55 MB | 2.96 s / 66 MB |
| graph→drvs N=1000 (again) | 1.11 s / 59 MB | 1.17 s / 58 MB |
| graph→drvs N=10000 (new) | 24.90 s / 175 MB | 26.15 s / 217 MB |
| graph→drvs N=10000 (again) | 7.72 s / 132 MB | 8.72 s / 150 MB |
| synthetic N=100 cold build | 10.01 s / 96 MB | 9.40 s / 94 MB |
| synthetic N=100 no-op | 0.47 s / 84 MB | 0.59 s / 83 MB |
| index cold / warm | 1.55 s / 130 MB / 0.02 s / 7 MB | |

Run 2026-10-20T19:56:49Z on vibe-engine, keep-fetches=1. Cells: wall time / peak RSS.
