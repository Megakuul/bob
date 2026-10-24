<script lang="ts">
	import { repo } from '$lib/site';
	import Backdrop from '$lib/ui/Backdrop.svelte';
	import BuildGraph from '$lib/ui/BuildGraph.svelte';
	import CodePanel from '$lib/ui/CodePanel.svelte';
	import SectionHead from '$lib/ui/SectionHead.svelte';
	import Terminal from '$lib/ui/Terminal.svelte';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	const install = [
		'# no release yet: build from source',
		'$ git clone https://github.com/megakuul/bob',
		'$ cd bob && scripts/bootstrap.sh',
		'# prints the path of the bob it built'
	];

	const pitch = [
		{ k: 'typed', t: 'Go, not a build language.', d: 'bob.go files are ordinary Go. The compiler checks them, gopls completes them, and errors point at your source line.' },
		{ k: 'engine', t: 'Nix underneath, invisible on top.', d: 'Pinned, sandboxed, reproducible builds from an embedded rootless Nix. You never write Nix and never read a raw Nix error.' },
		{ k: 'replace', t: 'One graph instead of five build tools.', d: 'go.mod, Cargo.toml, package.json and compile_commands.json become generated files. bob.go is the source of truth.' }
	];

	const features = [
		{ t: 'Hermetic via Nix', d: 'Every step runs in a sandbox with pinned inputs and no network. The static Nix is embedded in bob: no system install, no daemon.' },
		{ t: 'Polyglot', d: 'Go with cgo, C and C++, Rust, npm and TypeScript, OCaml, and code generators for protobuf, connect, GraphQL and OpenAPI, in one graph.' },
		{ t: 'Outputs', d: 'OCI images layered by store path, tarballs, AppImages and versioned releases come out of the same graph. No Dockerfile.' },
		{ t: 'HA build and cache server', d: 'Orchestrators and workers coordinate through S3 conditional writes alone. Nix binary cache, OCI registry and release host in one.' },
		{ t: 'Early cutoff', d: 'Generated code is added to the store by content: when a regenerated output is bit-identical, nothing downstream rebuilds.' },
		{ t: 'Dev environments', d: 'Per-directory activation, bob dev, shims and an LSP proxy, so editors and terminals use exactly the toolchains the build uses.' },
		{ t: 'Fine-grained cache', d: 'Software is split into many small packages, so a one-file change rebuilds one package and reuses the rest locally and on the server.' },
		{ t: 'Cross and variants', d: 'linux/amd64 and linux/arm64 from either host, release and debug profiles, sanitisers and named variants as typed values.' }
	];

	const steps = [
		{ t: 'bob.go', d: 'Typed Go declares modules, binaries, images and pipelines.' },
		{ t: 'evaluate', d: 'A sandboxed evaluator turns them into a deterministic graph.' },
		{ t: 'lock', d: 'bob.lock pins every input; native files are generated.' },
		{ t: 'build', d: 'Small Nix derivations build locally or on workers, cache first.' },
		{ t: 'ship', d: 'Binaries, OCI images, AppImages, tarballs, releases.' }
	];

	const ecosystems = [
		{ name: 'Go', pkg: 'pkg/golang', d: 'modules, binaries, test units, cgo; go.mod and go.sum generated' },
		{ name: 'C / C++', pkg: 'pkg/cc', d: 'libraries and binaries, compile_commands.json for clangd' },
		{ name: 'Rust', pkg: 'pkg/rust', d: 'crates.io resolution matching Cargo, Cargo.toml generated' },
		{ name: 'npm / TypeScript', pkg: 'pkg/npm', d: 'npm-equal resolution, Vite and SvelteKit sites, svelte-check tests' },
		{ name: 'OCaml', pkg: 'pkg/ocaml', d: 'opam resolution and libraries; .merlin generated, dune files on request' },
		{ name: 'protobuf · connect', pkg: 'pkg/proto · pkg/connect', d: 'Go and TypeScript code generation with early cutoff' },
		{ name: 'GraphQL · OpenAPI', pkg: 'pkg/graphql · pkg/openapi', d: 'gqlgen and oapi-codegen for Go, typed TypeScript clients, from schemas' },
		{ name: 'nixpkgs', pkg: 'pkg/nixpkgs', d: 'any nixpkgs package as a typed dependency, the only place Nix names appear' },
		{ name: 'Outputs', pkg: 'pkg/output · pkg/release', d: 'OCI images, AppImages, tarballs, releases and install profiles' }
	];

	const server = [
		{ k: 'storage', d: 'S3 is the only dependency: state, logs and artifacts live in buckets.' },
		{ k: 'fleet', d: 'Any number of orchestrators, zero-setup workers, leases and requeue.' },
		{ k: 'forges', d: 'GitHub, Forgejo and GitLab webhooks run pipelines declared in bob.go.' },
		{ k: 'cache', d: 'Nix binary cache and OCI registry; outputs are verified and signed before they are published.' },
		{ k: 'releases', d: 'Release hosting and bob install for consumers, no build knowledge needed.' },
		{ k: 'access', d: 'RBAC on every RPC, OIDC login, worker credentials split from control state.' }
	];

	// plan/STATUS.md, 2026-10-24. Keep this honest: implemented is not the same as released.
	const milestones: { id: string; t: string; s: 'done' | 'wip' | 'next' }[] = [
		{ id: 'M2–M7', t: 'engine, CLI, dev env', s: 'done' },
		{ id: 'M8–M11', t: 'ecosystems', s: 'done' },
		{ id: 'M12', t: 'outputs', s: 'done' },
		{ id: 'M13–M15', t: 'server core', s: 'done' },
		{ id: 'M16–M17', t: 'cache, pipelines', s: 'wip' },
		{ id: 'M18', t: 'web UI', s: 'done' },
		{ id: 'M19', t: 'hardening, v1', s: 'next' }
	];
</script>

<svelte:head>
	<title>bob · one Go program describes your whole build</title>
	<meta name="description" content="bob is a typed Go framework that makes Nix an invisible build engine, with an HA build and cache server that needs only S3." />
</svelte:head>

<!-- hero -->
<section class="relative overflow-hidden border-b border-line">
	<Backdrop size={32} />
	<div class="relative mx-auto max-w-[1240px] px-4 pt-14 pb-12 sm:px-6 md:pt-20">
		<p class="flex flex-wrap items-center gap-2 font-mono text-sm text-muted">
			<span class="border border-line bg-bg px-2 py-0.5">typed go</span>
			<span class="border border-line bg-bg px-2 py-0.5">invisible nix</span>
			<span class="border border-line bg-bg px-2 py-0.5">s3-only server</span>
		</p>
		<h1 class="mt-6 max-w-[16ch] text-4xl leading-[1.02] font-extrabold tracking-[-0.035em] sm:text-5xl lg:max-w-none lg:text-6xl">
			One Go program describes your <span class="bg-accent px-2 text-on-accent [box-decoration-break:clone]">whole build.</span>
		</h1>
		<div class="mt-8 grid gap-10 lg:grid-cols-[1fr_1fr] lg:items-start">
			<div class="flex flex-col gap-6">
				<p class="max-w-[54ch] text-lg text-muted md:text-xl">
					bob turns typed <code class="font-mono text-fg">bob.go</code> files into hermetic, fine-grained builds for every language you ship, and caches every step. Nix does the work; you never see it.
				</p>
				<div class="flex flex-wrap gap-3">
					<a href="/docs" class="rounded-xs border border-accent bg-accent px-5 py-2.5 font-mono text-base font-bold text-on-accent hover:border-fg">read the docs</a>
					<a href={repo} class="rounded-xs border border-line bg-bg px-5 py-2.5 font-mono text-base hover:border-fg" rel="external noopener">view source</a>
				</div>
			</div>
			<Terminal title="build from source" lines={install} />
		</div>
		<div class="mt-12">
			<BuildGraph />
		</div>
	</div>
</section>

<!-- what -->
<section class="border-b border-line" aria-labelledby="what">
	<div class="mx-auto grid max-w-[1240px] gap-12 px-4 py-20 sm:px-6 lg:grid-cols-2 lg:py-28">
		<div class="flex flex-col gap-10">
			<SectionHead n="01" kicker="what bob is" id="what" title="A build framework you write in Go." />
			<ol class="flex flex-col">
				{#each pitch as p, i (p.k)}
					<li class="grid grid-cols-[3rem_1fr] gap-x-4 border-t border-line py-5 last:border-b">
						<span class="font-mono text-sm text-muted">0{i + 1}</span>
						<div>
							<h3 class="text-xl font-bold">{p.t}</h3>
							<p class="mt-1 text-muted">{p.d}</p>
						</div>
					</li>
				{/each}
			</ol>
		</div>
		<div class="flex min-w-0 flex-col justify-end">
			<CodePanel file="api/bob.go" code={data.sample.code} html={data.sample.html} />
			<p class="mt-3 font-mono text-sm text-muted">$ bob build //api:Image <span class="text-fg">→ two platform images, one layer per store path</span></p>
		</div>
	</div>
</section>

<!-- features -->
<section class="border-b border-line" aria-labelledby="features">
	<div class="mx-auto max-w-[1240px] px-4 py-20 sm:px-6 lg:py-28">
		<SectionHead n="02" kicker="features" id="features" title="Everything between git push and a running image.">
			{#snippet children()}One description covers compiling, testing, packaging and shipping, on a laptop or on the server.{/snippet}
		</SectionHead>
		<ul class="mt-12 grid gap-px border border-line bg-line sm:grid-cols-2 lg:grid-cols-4">
			{#each features as f, i (f.t)}
				<li class="group relative flex flex-col gap-3 bg-bg p-6">
					<span class="absolute top-0 left-0 h-0.5 w-0 bg-accent transition-[width] duration-300 group-hover:w-full" aria-hidden="true"></span>
					<span class="font-mono text-sm text-muted">{String(i + 1).padStart(2, '0')}</span>
					<h3 class="text-xl font-bold">{f.t}</h3>
					<p class="text-base leading-relaxed text-muted">{f.d}</p>
				</li>
			{/each}
		</ul>
	</div>
</section>

<!-- how -->
<section class="relative overflow-hidden border-b border-line" aria-labelledby="how">
	<div class="mx-auto max-w-[1240px] px-4 py-20 sm:px-6 lg:py-28">
		<SectionHead n="03" kicker="how it works" id="how" title="From Go values to store paths.">
			{#snippet children()}bob evaluates your bob.go files into a graph, pins it in bob.lock, and builds each node as a small Nix derivation. Identical inputs mean an identical store path, so a cache hit is a lookup, not a guess.{/snippet}
		</SectionHead>
		<ol class="mt-12 grid gap-px border border-line bg-line md:grid-cols-5">
			{#each steps as s, i (s.t)}
				<li class="relative flex flex-col gap-2 bg-bg p-5 md:min-h-44">
					<div class="flex items-center gap-2 font-mono text-sm text-muted">
						<span class="grid h-6 w-6 place-items-center border border-line {i === 3 ? 'border-accent bg-accent text-on-accent' : ''}">{i + 1}</span>
						<span class="h-px flex-1 bg-line" aria-hidden="true"></span>
						{#if i < steps.length - 1}<span aria-hidden="true">→</span>{/if}
					</div>
					<h3 class="mt-2 font-mono text-xl font-bold">{s.t}</h3>
					<p class="text-base text-muted">{s.d}</p>
				</li>
			{/each}
		</ol>
		<div class="mt-6 grid gap-6 font-mono text-sm md:grid-cols-3">
			<p class="border-l-2 border-accent pl-3 text-muted"><span class="text-fg">no user Nix.</span> bob fetches sources itself; there are no user-written fixed-output derivations.</p>
			<p class="border-l-2 border-accent pl-3 text-muted"><span class="text-fg">errors in Go terms.</span> Build failures carry a BOB-E code and the bob.go line that caused them.</p>
			<p class="border-l-2 border-accent pl-3 text-muted"><span class="text-fg">same graph everywhere.</span> Local builds, remote builds and CI evaluate the same bob.go.</p>
		</div>
	</div>
</section>

<!-- ecosystem -->
<section class="border-b border-line" aria-labelledby="ecosystem">
	<div class="mx-auto grid max-w-[1240px] gap-12 px-4 py-20 sm:px-6 lg:grid-cols-[1fr_1.4fr] lg:py-28">
		<SectionHead n="04" kicker="ecosystem" id="ecosystem" title="Premade building blocks for production languages.">
			{#snippet children()}Each ecosystem resolves its own registry with results equal to the native tool, then builds package by package.{/snippet}
		</SectionHead>
		<ul class="border-t border-line">
			{#each ecosystems as e (e.name)}
				<li class="grid gap-1 border-b border-line py-4 sm:grid-cols-[11rem_1fr] sm:gap-6">
					<div>
						<p class="font-bold">{e.name}</p>
						<p class="font-mono text-xs text-muted">{e.pkg}</p>
					</div>
					<p class="text-muted">{e.d}</p>
				</li>
			{/each}
		</ul>
	</div>
</section>

<!-- server -->
<section class="relative overflow-hidden border-b border-line" aria-labelledby="server">
	<Backdrop size={32} />
	<div class="relative mx-auto max-w-[1240px] px-4 py-20 sm:px-6 lg:py-28">
		<SectionHead n="05" kicker="server and ci" id="server" title="A build farm that is just S3 and processes.">
			{#snippet children()}bob-server is a separate binary from the same repository. Pipelines are declared in bob.go and run on a worker fleet; the server is also the cache, the registry and the release host.{/snippet}
		</SectionHead>
		<div class="mt-12 grid gap-px border border-line bg-line lg:grid-cols-[1.1fr_1fr]">
			<div class="bg-bg p-6" aria-hidden="true">
				<div class="grid grid-cols-3 items-center gap-3 font-mono text-sm">
					<div class="flex flex-col gap-2">
						<span class="border border-line px-2 py-2 text-center">github</span>
						<span class="border border-line px-2 py-2 text-center">forgejo</span>
						<span class="border border-line px-2 py-2 text-center">gitlab</span>
					</div>
					<div class="flex flex-col gap-2">
						<span class="border border-accent bg-accent px-2 py-2 text-center text-on-accent">orchestrator</span>
						<span class="border border-accent bg-accent px-2 py-2 text-center text-on-accent">orchestrator</span>
						<span class="border border-line px-2 py-2 text-center text-muted">+ n</span>
					</div>
					<div class="flex flex-col gap-2">
						<span class="border border-line bg-surface px-2 py-2 text-center">worker</span>
						<span class="border border-line bg-surface px-2 py-2 text-center">worker</span>
						<span class="border border-line bg-surface px-2 py-2 text-center">worker</span>
					</div>
				</div>
				<div class="mt-3 border border-dashed border-line px-3 py-3 text-center font-mono text-sm">
					<span class="text-fg">S3</span> <span class="text-muted">· control · logs · artifacts</span>
				</div>
				<div class="mt-3 grid grid-cols-3 gap-3 font-mono text-sm text-muted">
					<span class="border-t border-line pt-2">nix cache</span>
					<span class="border-t border-line pt-2">oci registry</span>
					<span class="border-t border-line pt-2">releases</span>
				</div>
			</div>
			<ul class="grid bg-bg sm:grid-cols-2">
				{#each server as s (s.k)}
					<li class="border-line p-5 not-last:border-b sm:nth-[odd]:border-r sm:nth-last-[2]:border-b-0">
						<p class="font-mono text-sm text-fg">{s.k}</p>
						<p class="mt-1 text-base text-muted">{s.d}</p>
					</li>
				{/each}
			</ul>
		</div>
	</div>
</section>

<!-- status -->
<section aria-labelledby="status">
	<div class="mx-auto max-w-[1240px] px-4 py-20 sm:px-6 lg:py-28">
		<SectionHead n="06" kicker="status" id="status" title="Honest status: built, not yet released.">
			{#snippet children()}bob is in its implementation stage. Most features are implemented and bob builds itself, but there is no v1 release and no prebuilt binary yet. Implemented does not mean verified: the full acceptance suite has not yet run green end to end.{/snippet}
		</SectionHead>
		<ol class="mt-12 grid grid-cols-2 gap-px border border-line bg-line sm:grid-cols-4 lg:grid-cols-7">
			{#each milestones as m (m.id)}
				<li class="flex flex-col gap-2 bg-bg p-4">
					<span class="h-1.5 {m.s === 'done' ? 'bg-accent' : m.s === 'wip' ? 'border border-accent' : 'border border-line'}" aria-hidden="true"></span>
					<span class="font-mono text-sm font-bold">{m.id}</span>
					<span class="text-base text-muted">{m.t}</span>
					<span class="font-mono text-xs {m.s === 'next' ? 'text-muted' : 'text-fg'}">{m.s === 'done' ? 'implemented' : m.s === 'wip' ? 'finishing' : 'planned'}</span>
				</li>
			{/each}
		</ol>
		<div class="mt-6 grid gap-4 text-base text-muted md:grid-cols-3">
			<p><span class="font-mono text-sm text-fg">open now ·</span> security review fixes, remaining red acceptance tests, full CI run on a quiet host.</p>
			<p><span class="font-mono text-sm text-fg">then ·</span> budgets re-measured on a quiet machine, bob install end to end against a real server.</p>
			<p><span class="font-mono text-sm text-fg">v1 ·</span> hardening, examples and the first release. Follow along in the <a href={repo} class="text-fg underline decoration-line underline-offset-4 hover:decoration-accent" rel="external noopener">repository</a>.</p>
		</div>
	</div>
</section>
