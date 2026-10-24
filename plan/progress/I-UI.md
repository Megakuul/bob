# I-UI — M18 web UI against the proto contract and the tests/ui mock (A78)

Owner: web/ only. Started 2026-10-22. Status: M18.1–M18.4 implemented (all 17 routes of SPEC
§6.10.3); verified headless against a proto-conforming copy of the harness (see VERIFY). The
protected tests/ui suite cannot go green as written: see TEST PROBLEM.

## Done
- web/src/lib/gen generated from proto/ with protoc-gen-es 2.16.0 (target=ts) via a local plugin
  template and buf 1.74.0 (tools/go.mod); identical in shape to proto/buf.gen.yaml's es plugin.
- package.json: SvelteKit 2.70.3 + adapter-static 3.0.10 (D-UI-1), vite 8.3.5,
  vite-plugin-svelte 7.3.1, tailwindcss + @tailwindcss/vite 4.3.3, @connectrpc/connect(-web)
  2.2.0, @bufbuild/protobuf 2.16.0 (all MIT/Apache-2.0, on the R-UI-005 allowlist).
  web/svelte.config.js (adapter-static, fallback 200.html), vite.config.ts (tailwind + kit).
- Tokens: web/src/app.css = SPEC §6.10.2 verbatim; Tailwind defaults for colour, radius, shadow,
  blur, text, font, breakpoints cleared so only tokens exist; md breakpoint = 900 px (nav
  collapse). Theme: inline script in app.html (Light default, bob.theme, data-theme before
  paint), lib/theme.svelte.ts, ui/ThemeSwitch (data-theme-choice).
- Components (web/src/lib/ui, hand-written): Button, Input, Select, Checkbox, Badge, StatusBadge,
  Table/Th/Td/Tr, FilterBar (URL state), Pagination, Tabs, Dialog (native <dialog>), Confirm,
  Toast, KeyValue, Mono (copy), CodeBlock, LogView (virtualised, ANSI→tokens, text nodes only),
  Graph (longest-path layered SVG, ≤300 nodes), Waterfall, BarChart, LineChart, EmptyState,
  IdLink, PageHeader, Section, StatStrip, ThemeSwitch.
- Data: lib/api.ts (one connect transport, /api, Bob-Csrf), lib/query.svelte.ts (Query, Paged:
  10 s refresh while visible, Unauthenticated → /login?next=), lib/log.svelte.ts (GetLog then
  FollowLog with cursor, restarts, retry), lib/format.ts, lib/build.ts, lib/fleet.ts, lib/ansi.ts.
- Pages: / · /projects · /p/<project> (builds, pipelines, releases, settings, permissions) ·
  /p/<project>/builds/<id> (packages table|graph, log with follow/search/package filter,
  waterfall, artifacts, cancel/retry/re-run; WatchBuild + FollowLog) · /fleet · /fleet/workers/<id>
  · /cache (stats, top packages, path lookup) · /registry (releases, images) · /registry/<repo...>
  · /p/<project>/releases/<name>/<version> · /access (users, roles+editor, matrix, service
  accounts+tokens, OIDC mappings) · /settings (general, forges, signing keys, retention,
  webhooks) · /analytics · /login (prerendered) · /device · /oauth/consent · /account.
- web/scripts/style-check.sh (R-UI-003 steward helper): clean.

## VERIFY (how it was checked)
- svelte-check --fail-on-warnings: 0 errors, 0 warnings; vite build OK; first load JS 35.6 KiB
  gz, CSS 4.3 KiB gz; overview FCP 196–264 ms [8c/15G, aarch64, headless chromium].
- out/uicheck/ (gitignored scratch, not a test of record): a copy of tests/ui with ONLY these
  changes: (1) mock also routes /api/bob.* (SPEC §6.2); (2) seed = out/uicheck/seed.json, a
  proto-conforming rewrite of tests/ui/seed.json (every message decodes with strict fromJson
  against the generated schemas); (3) page RPC lists follow the proto (overview GetOverview;
  build GetBuild+GetBuildGraph; release ResolveRelease; build test GetPath for sizes, 4 graph
  nodes, no image ref); (4) TLS + HTTP/2 server. Result: TestPages (17 pages × API/DOM/links/
  filter/URL, 68 screenshots), TestBuildPage (live line within budget), TestThemeDefault,
  TestFirstLoadBudget, TestHarnessDeterministic, TestStreamMock all PASS (update mode; the
  screenshots are candidates in out/uicheck/shots, reviewed by eye against the wireframes).

## DECISIONS
- D-UI-1: SvelteKit 2 instead of the skeleton's 3.1.0. SPEC §6.10.1 says SvelteKit 2 and the
  protected TestStaticBuild requires web/svelte.config.js; SvelteKit 3 refuses to start when
  svelte.config.js exists (kit/src/exports/vite/index.js `config_file_unsupported`).
- D-UI-2: the UI follows the SPEC API contract, not the mock's divergent shape: connect base
  `/api/` (SPEC §6.2, R-API-004), POST only (cookie-only Connect GET is refused, §6.3.2), header
  `Bob-Csrf: 1`, proto3 JSON exactly as generated from proto/.
- D-UI-3: overview data comes from GetOverview alone (it carries running builds and recent
  failures); one filter bar filters both tables (fleet: workers + orchestrators likewise).
- D-UI-4: release page looks the release up with ResolveRelease(project, name, exact version):
  GetRelease takes only an id and the route carries project/name/version.
- D-UI-5: install line is `bob install <project>/<release>@<version>` (SPEC §02 ref grammar); the
  seed's "acme/api@1.4.2" uses the repo owner, which the grammar does not.
- D-UI-6: build artifacts = outputs of the build's targets, else of the packages nothing in the
  graph depends on (≤ 20), sizes via CacheService.GetPath.
- D-UI-7: /device and /oauth/consent post plain same-site forms to /device and /oauth/consent
  (fields: user_code + decision approve|deny; original authorize params + decision allow|deny).
  SPEC names the pages but no approval endpoint; these paths are within R-API-004's allowed set.
- D-UI-8: every page marks data-page only after its first data arrived (or failed), so the
  harness never screenshots a loading state.
- D-UI-9: no bundled fonts (SPEC tokens are system stacks); nothing under web/static, so no
  ATTRIBUTION rows needed. Favicon is an inline data URI.

## NEEDS (other units / owner)
- proto (owner of proto/): SPEC §6.10.3 wireframe data the API does not carry: build artifacts
  with size and image ref (Build/PackageNode have only output store paths); cache hit-rate
  30-day series for the /cache line chart (only 24 h / 7 d rates exist); per-package p50/p90
  (PackageDurations has per-day avg_ms only); job ids have no detail route. The UI shows what the
  API has and leaves these out.
- server (internal/http): accept POST /device and POST /oauth/consent as in D-UI-7.
- internal/ui: serve web/build (R-UI-001 headers); unknown path → 200.html no-cache.

## TEST PROBLEM (tests/ui is protected; not edited)
1. seed.json does not follow proto/: 20 of 23 seeded responses fail strict proto3-JSON decoding
   against the generated schemas (e.g. GetBuild is flat instead of {build: …}; Build has
   "project", "trigger":"push", "state":"RUNNING", "done", "createTime"; Job has "label" and
   PackageState values; GetServerInfo "name"/"oidcProviders"; WorkerState "ACTIVE"; registry
   "public"; settings flat). A UI built on the generated clients cannot show this data, so
   TestPages/TestBuildPage fail on R-UI-002 content even though the pages work. Fix: rewrite
   the seed to the proto (out/uicheck/seed.json is a ready proposal).
2. The mock answers RPCs at `/bob.*`; SPEC §6.2/R-API-004 mount connect at `/api/`. Fix: route
   `/api/bob.*` (strip /api) in tests/ui/ui.go Handler.
3. Page RPC lists name RPCs the proto makes unnecessary or impossible: build page uses
   JobService/ListJobs for package rows (packages are GetBuildGraph nodes; jobs carry no label);
   release page GetRelease (needs an id the route lacks → ResolveRelease); overview ListBuilds
   (GetOverview has the lists); build test expects artifact size via GetBuild and an image ref
   (not in the proto, see NEEDS); install string "bob install acme/api@1.4.2" (grammar gives
   api/api@1.4.2).
4. The seed must answer every RPC a page calls, or the 501 shows up as a console error (chromedp
   Console includes Log.entryAdded). The UI additionally calls: WatchBuild (running build),
   GetBuildGraph, GetPath (artifact sizes), ResolveRelease, GetServerInfo (release page OCI host),
   ListRoleBindings (access users), ListMySessions (account), BuildHistory + RecentFailures +
   ListProjects (analytics). out/uicheck/seed.json seeds all of them.
5. Plain HTTP/1.1 fixture server + every screenshot tab kept open until the subtest ends: the
   build page holds two server streams (WatchBuild, FollowLog, as SPEC §6.10.1 says), so by the
   third tab Chromium's 6-connections-per-host limit is used up and navigation never fires load
   (`open ...: context deadline exceeded`). Production is HTTPS (Secure cookie) → HTTP/2, where
   this does not happen. Fix: serve the fixture with TLS + HTTP/2 (httptest StartTLS with
   EnableHTTP2, chromium --ignore-certificate-errors) or close each tab after its screenshot.
6. Fonts: fontconfig in browser_test.go lists DejaVu dirs without generic-family aliases, so
   `sans-serif`/`system-ui` resolve to DejaVu Serif and every screenshot is serif. Fix: add
   <alias> entries (sans-serif → DejaVu Sans, monospace → DejaVu Sans Mono) to the generated
   fonts.conf.
7. No goldens exist yet (tests/goldens/ui): every screenshot assertion fails "no golden yet"
   until the owner approves candidates (after 1–6 are fixed the candidates can be generated with
   BOB_GOLDEN_UPDATE=1).
