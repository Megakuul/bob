# S14 — Scheduling unit: whole closure vs per derivation

Status: done (model + small real run). Date: 2026-10-21. Code: `spikes/s14-sched/`.

## 1. Question and requirement ids

What is one job? (a) the whole closure of the requested target on one worker, (b) one
derivation, outputs passed between workers through the cache, or (c) a hybrid (a subgraph
chunk, with worker affinity). Requirements: R-FLEET-003/004/005, R-SRV-003/004, R-REG-002
(staging → promotion), R-NFR-004/005/007/008. Builds on S9 (job protocol), S12 (promotion),
S13 (per-job overlay), S2/S3 (graph sizes, per-derivation cost), gen-b.

## 2. What was built

- `graph/graph.go`: the six workloads and the chunking algorithm (convex chunks, critical-path
  merge, cost cap).
- `cmd/sim`: a discrete-event model of the fleet. It models S9's job protocol (claim, lease
  renewal, 5 s ± 20 % polling with re-poll after a job, requeue after lease expiry + reconcile),
  a global claim rate limit (S9 drain rate), S12's staging upload and promotion, Nix
  substitution of inputs, drv-closure transport, per-job overlay start, local list scheduling
  inside a job, live dedupe of identical units across builds, fair share across projects and
  affinity (prefer the worker holding most inputs, among the first 50 queued, as in S9).
  Modes: `a`, `b`, `c` (static chunks) and `d` (dynamic ready-set batches, a second hybrid
  added after `c` lost; see §3.3).
- `cmd/real`: a real run with W local "workers" (separate chroot Nix stores
  `local?root=…`), a binary cache served over HTTP on 127.0.0.1:21400 (request counter), and a
  small Go scheduler. Each node is a real derivation (static busybox builder, `sleep` = compute
  on a separate machine, random output bytes). Modes a/b/c, cold and incremental.
- `measure.sh`: reruns everything. Results in `spikes/s14-sched/tmp/*.txt`.

## 3. Results

All real-run numbers are [8c/15G] on a host shared with other spikes: **indicative, rerun
`measure.sh` on a quiet machine**. Sim numbers are model output from the parameters in §3.1.

### 3.1 Real run: per-job cost of a derivation job (`tmp/real-layer.txt`, `tmp/real-chain.txt`)

Layered graph, 73 drvs, 0.5 s work each, 4 workers × 2 slots [8c/15G]:

| Mode | cold wall | incremental (42 of 73 rebuilt) | jobs | cache GET+HEAD (cold) |
|---|---|---|---|---|
| a, one closure job | 21.3 s | 12.3 s | 1 | 365 |
| b, one job per drv | **14.2 s** | **8.9 s** | 73 | 864 |
| c, static chunks (cap 6 s) | 25.9 s | 12.5 s | 24 / 12 | 645 |

Per-job phases in mode b (mean): drv-closure fetch 0.13 s, input substitution 0.10 s (56 of 73
jobs), `nix build` 0.76 s (0.5 s work → **0.26 s Nix overhead**), upload 0.10 s. So a
derivation job costs about **0.55–0.6 s on top of its work**, before claim latency (S9: 28–90 ms)
and promotion. Inside one `nix build` the cost is the known ~0.14 s per drv (S3: 0.13–0.16 s).
The eval-side upload of the drv closure took 0.77–1.6 s for 73 drvs; in mode a it is not needed
when evaluation runs in the same job (§4).

Model parameters (`cmd/sim` `defaults()`): claim+complete 0.12 s, enqueue 0.06 s, poll 5 s,
job start 0.12 s, drv fetch 0.12 s + 2 ms/drv, substitution 0.09 s + 10 ms/path at 60 MiB/s
(S12: 49–91), per drv 0.14 s, upload 0.08 s + 5 ms/path at 50 MiB/s (S12: 42–84), promotion
0.05 s + 10 ms/path at 80 MiB/s (S12), lease 30 s, reconcile 10 s, **global claim rate 30/s**
(S9 RustFS drain 32/s), 2 slots per worker (reference 2c machine). S3 requests per job:
12 (S9) + renewals; per output path 2 PUT staging, 4 PUT + 2 GET promotion, 1 GET negative
lookup; per substituted path 3 GET; per transported drv 3 GET. AWS-equivalent prices as in S9.

Workloads (`graph.Workloads()`): fixture 39 drvs / 20 slot-s of work; go200 201 / 22 slot-s
(calibrated to S2 package mode); rust49 120 / 196 slot-s; rust129 300 / 558 slot-s (S3: 3 min at
4 jobs — the model gives 181 s with one 8-slot worker); wide2000 1,999 × 50 ms; bigcpp 32 drvs
with one 900 s compile.

### 3.2 Cold build, one project (`tmp/sim-cold.txt`, `tmp/sim-push.txt`, `tmp/sim-push-8slots.txt`)

Wall time to result (promoted). 2-slot workers, polling as in S9:

| workload | crit. path | W | a | b | c | d |
|---|---|---|---|---|---|---|
| fixture | 10.6 s | 4 | **15.8** | 25.7 | 16.0 | 25.7 |
| fixture | | 100 | **14.7** | 19.4 | 14.9 | 19.4 |
| go200 | 4.7 s | 4 | 31.1 | **27.8** | 34.4 | 30.3 |
| go200 | | 100 | 30.0 | **16.4** | 32.5 | **16.4** |
| rust49 | 66 s | 4 | 120.9 | **117.2** | 147.6 | 119.9 |
| rust49 | | 100 | 119.8 | **95.6** | 142.7 | **95.6** |
| rust129 | 154 s | 4 | 328.5 | **244.4** | 353.7 | **244.4** |
| rust129 | | 100 | 327.4 | **217.8** | 380.4 | **217.8** |
| wide2000 | 0.8 s | 4 | 224.7 | 174.1 | 107.9 | **74.5** |
| wide2000 | | 100 | 223.6 | 73.3 | 76.3 | **16.6** |
| bigcpp | 936 s | 4–100 | 981 | 960–970 | 977–989 | 960–970 |

With 1 worker every split mode is slower than a (go200 32 → 82 s in b; wide2000 226 → 645 s).
With **8-slot workers** (push dispatch) a wins or ties every workload except wide2000:
fixture 12.5 vs 17.6 s (b), go200 12.3 vs 14.9 s, rust49 77 vs 87 s, rust129 181 vs 194 s,
wide2000 81 vs 20 s (d, W=20).

Cost per cold build at W=20 (S3 requests excluding idle polling, AWS-priced; MiB uploaded /
substituted+drvs):

| workload | a | b | d |
|---|---|---|---|
| go200 | 1,831 req · $0.006 · 47/0 MiB | 12,731 · $0.019 · 47/93 | 12,814 · $0.019 · 47/93 |
| rust129 | 2,780 · $0.010 · 672/0 | 22,591 · $0.030 · 672/1,483 | 22,591 · $0.030 · 672/1,483 |
| wide2000 | 18,048 · $0.063 · 98/0 | 81,545 · $0.172 · 98/167 | 50,243 · $0.096 · 98/135 |
| bigcpp | 500 · $0.002 · 540/0 | 1,640 · $0.003 · 540/2,036 | same as b |

Polling alone (independent of mode): W/5 LIST/s, $10 / $52 / $259 per month for 4 / 20 / 100
workers on AWS prices (as S9).

Push dispatch (worker notified over its orchestrator connection instead of polling) lowers b/d:
rust49 W=4 117 → 89 s, rust129 244 → 199 s, go200 W=100 16.4 → 15.2 s. It does not change a.

### 3.3 Why the static chunker (c) lost

`ChunkGraph` produces convex chunks (checked: `Convex` = true for every workload and cap) by
merging each node into its critical predecessor's chunk under a cap (`max(30 s, work/(2W))`).
It cuts the graph along the critical path, so chunks form chains; every chain hop costs a job
(claim, drv fetch, substitution, upload, promotion, poll) and a chunk holds a whole worker
while it has fewer ready drvs than slots. Measured in the real run: 25.9 s vs 21.3 s (a) and
14.2 s (b). In the model c is never the best except wide2000 at W=4 (108 s, beaten by d at
75 s). **Static chunking is rejected.** Mode d (batch the currently ready drvs of one build,
about `ready / idle workers` per job, ≤ 64 drvs, ≤ 60 slot-s) keeps b's parallelism and divides
the job count by up to 25 (wide2000 W=20: 79 jobs instead of 1,999).

### 3.4 The claim rate is a hard ceiling for b (`tmp/sim-cold.txt`)

S9 measured 32 claims/s on RustFS (173/s SeaweedFS) with 20 claimers. wide2000 in b needs
2,000 claims: at 30/s that is ≥ 67 s however many workers exist (b W=100: 73 s; without the
limit the model gives 15 s). d needs 79 jobs and reaches 16.6 s. A per-derivation unit on
dynamitedb also means ~6 PUT per drv on the live-job table plus 4 PUT per path for promotion:
~10 writes/drv, i.e. ~300 writes/s at the claim ceiling. Per-drv jobs must therefore be
batched (d), not one record per drv.

### 3.5 Incremental build, bottom node changed (`tmp/sim-inc.txt`)

Second build after the cold one, node 0 changed (worst case: for rust49/rust129 every crate
depends on it, so all rebuild). W=4, affinity on / off:

| workload | a | b | d |
|---|---|---|---|
| fixture | 2.3 / 2.3 s | 3.4 / 3.7 s | 3.4 / 3.7 s |
| go200 | 21.9 / 22.6 s (0 / 10 MiB substituted) | 22.2 / 21.4 s (33 / 55 MiB) | 23.0 / 24.4 s |
| rust129 | 328 s | 242 s (903 MiB) | 247 / 241 s |
| wide2000 | 29.7 s (69 MiB) | 20.7 / 24.5 s | 18.1 / 19.6 s |

Affinity matters for bytes, little for wall time on these sizes: a with a warm store
substitutes nothing; on a cold worker it substitutes the unchanged inputs (go200 10 MiB,
wide2000 69 MiB). Real run: incremental 12.3 s (a) vs 8.9 s (b) on 4 workers.

### 3.6 Worker death at 50 % of the build (`tmp/sim-death.txt`)

Worst case over which worker dies; wall undisturbed → with death (slot-s of work lost), W=20:

| workload | a | b | d |
|---|---|---|---|
| fixture | 14.7 → 51.9 s (15) | 20.4 → 51.7 s (1) | 20.4 → 51.7 s (1) |
| go200 | 30.0 → 75.1 s (30) | 17.8 → 49.4 s (1) | 17.8 → 48.4 s (1) |
| rust129 | 327 → 521 s (327) | 221 → 257 s (4) | 221 → 257 s (4) |
| wide2000 | 224 → 366 s (223) | 73 → 74 s (1) | 28 → 55 s (8) |
| bigcpp | 981 → 1,502 s (981) | 963 → 1,471 s (478) | same as b |

The fixed cost is the lease: ~35 s until the job is requeued (TTL 30 s after the last renewal,
plus half a reconcile interval). In a the dead worker's whole closure is redone, because its
staging has no `COMPLETE.json` (S12) and nothing was promoted. One long drv (bigcpp) loses its
work in every mode.

### 3.7 Fairness (`tmp/sim-fair.txt`)

p1 starts rust129 at t = 0; p2..p5 each start the fixture at t = 5 s. rust129 wall / fixture
mean (max):

| W | policy | a | b | d |
|---|---|---|---|---|
| 4 | FIFO | 329 / 21.1 (30.9) s | 236 / 37.3 (41.7) s | 261 / 37.4 (40.0) s |
| 4 | fair share | 329 / 21.1 (30.9) s | 251 / 34.6 (36.3) s | 283 / 35.3 (38.9) s |
| 20 | either | 328 / 15.3 s | 220 / 21.1–21.5 s | 220 / 21.1–21.5 s |

In a a large build can never take more than one worker, so small projects are isolated by
construction. In b/d a large build fills every free slot; fair share only helps at the next
free slot (no preemption), so small builds on a full fleet wait up to the length of the
running drvs. Fair share also costs the big build 6–8 % at W=4.

### 3.8 Duplicate builds of one commit (push + PR, 2 s apart, W=20, `tmp/sim-dup.txt`)

With live dedupe by unit key (input-addressed outputs), the second build finishes with the
first in every mode (a: second wall = first − 2 s). In a this works only when the whole target
set is identical; two builds that share most but not all drvs build the shared part twice.
In b/d sharing is per drv.

## 4. Answer

1. **A derivation is too small to be the scheduling unit.** Each derivation job costs about
   0.6 s of Nix and transport work plus claim and promotion, measured; at 0.1–0.5 s of work
   per Go/Rust drv that doubles the slot time. On one worker, or on 8-slot workers, the whole
   closure job is as fast or faster for every workload except a very wide graph. One record
   per drv also hits the dynamitedb claim ceiling (~30/s on RustFS) and costs 3–7× the S3
   requests of a closure job.
2. **The whole closure is the right default, but not the only unit.** Splitting pays off for
   wide graphs on small workers: go200 1.8×, rust129 1.5×, wide2000 13× at W=100 with 2-slot
   workers. It also cuts the cost of a worker death from "redo everything" to "redo one batch".
3. **Hybrid = dynamic ready-set batches, not static chunks.** Static chunking along the
   critical path was worse than both pure options (real run and model). Batching the ready
   drvs of a build into jobs of about `ready / idle workers` drvs keeps the parallelism of b at
   ~1/25 of the job count.
4. **One job record schema covers all three**: a job is "realise this set of derivations on one
   worker". A closure job is the set {targets}; a batch job is a set of ready drvs. R-FLEET-005's
   "allow the finer model later" is satisfied by the schema, not by a second protocol.
5. **Promotion becomes per job.** For split builds, dependents on other workers must
   substitute signed outputs, so each job's outputs are promoted when the job completes
   (S12 protocol, unchanged, keyed by job). Workers never substitute from `staging/`
   (unverified bytes, R-REG-002). Paths of a build that later fails are therefore already
   published; S12 accepted this ("partial publication of complete sub-closures"). Visibility
   markers keep them private to the project.
6. **Store integrity:** every job runs in its own overlay `/nix` (S13); new outputs move to the
   worker's store after the job. This is what makes affinity work and costs ~9 ms per job, so
   it does not favour either unit.
7. **Evaluation runs in the closure job** when the build is not split: the drvs never leave
   the worker (saves the 0.8–1.6 s drv-closure upload and all drv fetches). A split build needs
   a separate eval job that uploads the drv closure to staging; batch jobs fetch it.

## 5. SPEC input

### 5.1 Scheduling unit [proposal]

A **build job** realises a set of derivations (`targets`, ≤ 64) on one worker, with Nix's
local scheduler (`max-jobs` = worker slots). Inputs not in the worker's store are substituted
from the bob cache (signed, promoted paths) or cache.nixos.org. Outputs go to
`staging/<build>/<job>/<attempt>/`, then `COMPLETE.json`; the job's outputs are promoted when
the job completes.

Planner (build controller, after evaluation), per build:

```
est(v)   = last duration of the same drv name+system from analytics (lake), else
           ecosystem default (Go pkg 0.3 s, Rust crate 2.5 s, C/C++ unit 2 s, other 5 s)
S        = slots of the median eligible worker (platform, caps, labels)
I        = eligible workers idle or finishing within 30 s (fleet records)
T1       = list-schedule makespan of the graph on S slots, + 0.14 s per drv
Tsplit   = max(critical path + depth × 1.0 s, (Σ est + 0.6 s × drvs) / (S × max(I,1)))
split    = I ≥ 2 and T1 − Tsplit ≥ max(60 s, 0.3 × T1)
not split → one closure job: kind=evalbuild, targets = requested targets
split     → one eval job (uploads drv closure), then batch jobs as below
```

Batching (controller loop, event-driven on job completion, plus every reconcile pass):

```
ready   = drvs of the build whose inputs are all promoted and that are not assigned
order   = by bottom level (longest est. path to the end of the graph), descending
k       = ceil(|ready| / max(idle eligible workers, 1)), capped at 64 drvs and 60 est. slot-s
repeat: take the next k drvs → one batch job; prefer = the worker holding most of their inputs
limit   : ≤ 2 × (eligible workers) queued+running batch jobs per build (fair share, §5.4)
```

### 5.2 Job record (fleet bucket, extends S9 `live/<platform>/j/<jobID>`) [proposal]

| Field (CBOR) | Type | Meaning |
|---|---|---|
| `l` | lease | S9 lease {state, owner, epoch, expires, attempts, changed} |
| `b` / `p` | string | buildID / projectID |
| `k` | enum | `evalbuild` (eval + closure build), `eval` (eval, upload drv closure), `batch` |
| `t` | []string | target drv paths (≤ 64); for `evalbuild` the requested targets by name |
| `n` | int | drv count to build (estimate) |
| `e` | float | estimated slot-seconds |
| `c` | string | caps hash (`<platform>.<capsHash>` partition, S9) |
| `pw` | string | preferred workerID (affinity); others may claim after `pt` |
| `pt` | time | affinity deadline: created + 2 s |
| `sg` | string | staging prefix `staging/<build>/<job>/<attempt>/` |
| `pr` | []string | promoted outputs (written by the orchestrator on success, for the controller) |

Build-side node state (control bucket, written only by the build controller under its leader
lease, one CAS per job transition, not per drv): `buildnodes/<buildID>` = {drv count, bitmaps
`assigned`, `promoted`, `failed`, map job → drv index range}. Graph itself:
`staging/<build>/graph.cbor` (eval job output, ≤ 10,000 nodes).

### 5.3 State machines [proposal]

Job: S9's machine unchanged (`queued → claimed → running → succeeded|failed|cancelled`,
requeue on lease expiry). Added: `succeeded → promoted` is a separate field on the build-node
record, not a job state.

Derivation inside a build (bitmaps in `buildnodes`):

```
pending --all inputs promoted--> ready --batched into job J--> assigned(J)
assigned(J) --J succeeded + promotion done--> promoted
assigned(J) --J requeued (lease lost)--> assigned(J)            (same job, attempt+1)
assigned(J) --J failed--> failed  → build: cancel queued jobs, let running jobs finish
                                    (their outputs may still be promoted), build = failed
```

Build: S9's build machine; `running` covers all jobs; `succeeded` when every requested target
is promoted (so "promote only after the whole build" becomes "result = last promotion").

### 5.4 Rules [proposal]

1. Dispatch: S9 polling stays the fallback; workers that hold an orchestrator connection get
   a "job available" notification (push) — worth 10–25 % on split builds (§3.2).
2. Fair share: a worker takes jobs from the project with the fewest running jobs first, then
   FIFO; affinity only among jobs of that project. A closure job holds one worker.
3. Dedupe: job key = hash of sorted target drv paths; a new build whose job key is queued or
   running joins it instead of enqueueing; drvs already promoted are never scheduled.
4. Never substitute from `staging/`. Dependencies between jobs pass only through promoted paths.
5. Worker death in a closure job: workers upload finished outputs to staging as they finish
   (with a per-path done marker); on requeue the reconciler promotes the complete
   sub-closures before the retry, so the retry substitutes them **[proposal, not verified]**.

### 5.5 Budgets [proposal; model numbers, rerun on a quiet machine]

| Budget | Value | Basis |
|---|---|---|
| Per-job overhead on the worker (excl. work, claim, promotion) | ≤ 1 s | real run 0.55–0.6 s [8c/15G] |
| Hop overhead between dependent jobs (complete → promote → claim → start) | ≤ 1.5 s push, ≤ interval + 1.5 s polling | model |
| Claims per platform table | ≤ 30/s sustained (RustFS) — planner keeps ≤ 10 jobs/s per build | S9 32/s |
| S3 requests per build | ≤ 15 per drv (closure job ~9, batch ~25 incl. promotion) | §3.2 |
| Worker death, extra wall time | ≤ lease TTL + reconcile + lost batch (≤ 60 slot-s) for split builds | §3.6 |
| Split speedup threshold | ≥ 60 s and ≥ 30 % predicted | §3.2 |

## 6. Decisions, risks, open points

### Agent decisions
- Test graphs in the real run use a Nix expression with a busybox builder, not B: the question
  is scheduling cost, not drv generation; per-drv cost matches S3/B (0.14 s).
- `sleep` stands for compute: the 4 "workers" share one host; sleeping keeps them from
  competing for CPU, like separate machines.
- File-system staging instead of S3 in the real run; S3 costs come from S9/S12 measurements.
- Promotion per job, not per drv and not per build: per drv multiplies orchestrator work by the
  batch size; per build makes split builds impossible.
- Dynamic batches (d) instead of static chunks (c): measured better (§3.3).

### Owner decision needed

**O-S14-1: scheduling unit (R-FLEET-005).**
- **a)** Whole closure per job only. Simplest: one job per build, eval in the same job, no drv
  transport, promotion once, 3–7× fewer S3 requests, large builds can't starve small ones.
  A single build never uses more than one worker; a worker death redoes the whole build.
- **b)** One derivation per job. Fastest for wide graphs on many small workers, but 0.6 s per
  drv overhead, slower than a on 1 worker and on 8-slot workers, ~30 claims/s ceiling on
  RustFS (wide2000 ≥ 67 s regardless of fleet size), 3–7× the S3 requests and 2–4× the bytes.
- **c)** Hybrid: one schema (job = set of ≤ 64 drvs). A closure job by default; the planner
  splits a build into ready-set batch jobs only when it predicts ≥ 60 s and ≥ 30 % gain
  (§5.1). Promotion per job.

Recommendation: **c**. It is a with the same costs for most builds (fixture, small Go and Rust
graphs, single long compiles), and gives 1.5–13× on large wide builds when idle workers exist.
If the owner wants the smallest v1: ship a with the §5.2 schema (targets as a set) so c is a
planner change, not a protocol change.

### Open risks
- The model is only as good as its parameters. The per-job overhead was measured on a busy
  host with 8 concurrent Nix processes; the claim ceiling comes from S9's 20-claimer test.
- Cost estimates for the planner need history; the first build of a project uses defaults
  and may split wrongly (cost: at most the hop overheads, §3.2).
- Per-job promotion publishes outputs of builds that later fail (accepted in S12; private by
  visibility markers).
- Fair share without preemption: a fleet full of long drvs (bigcpp-like) still blocks small
  projects for the length of those drvs in b/c.
- Split builds need the drv closure in staging; for 10,000-node graphs that is ~10,000 small
  objects per build (not measured).

### Still to verify
- Mode d in a real run (only modelled; the real run covers a, b and static c).
- Real S3 (RustFS) instead of the file-system cache in the real run.
- Rule 5.4.5 (promote partial sub-closures of a dead closure job before retry).
- Push notification latency over the worker connection.
- Rerun `spikes/s14-sched/measure.sh` on a quiet machine, also at [2c/4G].
