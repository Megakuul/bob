<script lang="ts">
	import { repo } from '$lib/site';
	import Backdrop from '$lib/ui/Backdrop.svelte';
	import BuildGraph from '$lib/ui/BuildGraph.svelte';
	import CodePanel from '$lib/ui/CodePanel.svelte';
	import FileTree from '$lib/ui/FileTree.svelte';
	import SectionHead from '$lib/ui/SectionHead.svelte';
	import Terminal from '$lib/ui/Terminal.svelte';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	// Copy rule (owner, 2026-10-24): benefit-led headings and short copy for developers and
	// teams; technical detail only in the small supporting lines. Every claim must hold against
	// SPEC.md and plan/STATUS.md (AGENTS.md §5.8).

	const tags = ['one build for every language', 'same result on every machine', 'rebuilds only what changed'];

	const quick = ['# with Nix (flakes enabled)', '$ nix profile add github:megakuul/bob', '$ bob build //...'];

	const pains = [
		{
			t: 'CI that takes forever, and fails for no reason',
			was: 'Every pull request rebuilds the world on a cold runner, and a retry fixes it half the time.',
			now: 'Only what changed rebuilds. Every laptop and worker shares one cache.'
		},
		{
			t: '"Works on my machine"',
			was: 'A different compiler, a stray env var, an unpinned download, and the bug only exists in CI.',
			now: 'Every step runs sandboxed with pinned inputs, so your laptop and CI build the same thing.'
		},
		{
			t: 'Touch one file, rebuild everything',
			was: 'A comment in a .proto file and every service downstream compiles again.',
			now: 'If regenerated code comes out identical, nothing after it rebuilds.'
		},
		{
			t: 'A different tool for every language',
			was: 'go build, cargo, CMake and npm: four configs, four caches, glued together with shell scripts.',
			now: 'One bob.go describes all of them, in one graph.'
		},
		{
			t: 'Pipelines you can only test by pushing',
			was: 'Hundreds of lines of CI YAML, debugged one commit at a time.',
			now: 'Pipelines are Go in bob.go. Run them on your laptop with bob pipeline run.'
		},
		{
			t: 'Errors from three layers down',
			was: 'A wall of build-tool output that never names the line you got wrong.',
			now: 'Every error has a code and points at the line in your bob.go.'
		},
		{
			t: 'Learning another build language',
			was: 'Starlark, CMake, Groovy, or the Nix language, just to compile your code.',
			now: "It's Go. Your editor already knows it, and so does your team."
		},
		{
			t: 'Lock files that drift apart',
			was: 'go.sum, Cargo.lock and package-lock.json each pin their own little world.',
			now: 'One bob.lock pins every input. go.mod, Cargo.toml and package.json are generated.'
		},
		{
			t: 'Not knowing what you ship',
			was: 'Install scripts run with your credentials, and nobody can say what is in the image.',
			now: 'npm install scripts are off unless you allow them, and every image lists its licences.'
		},
		{
			t: 'Build infrastructure as a second job',
			was: 'A CI service, a cache service, a registry, and the database each of them needs.',
			now: 'bob-server is the CI, the cache and the registry. All it needs is S3.'
		}
	];

	const pitch = [
		{ t: 'Your editor already knows it.', d: 'bob.go is ordinary Go: completion, go-to-definition and type errors before anything runs.' },
		{ t: 'Nothing new to learn underneath.', d: 'Nix does the building, but you never write it and never read its errors.' },
		{ t: 'One source of truth.', d: 'go.mod, Cargo.toml, package.json and compile_commands.json are generated from it.' }
	];

	const release = `${repo}/releases/latest/download`;
	const installs = [
		{
			id: 'nix',
			t: 'Nix profile',
			d: 'Any Linux with Nix. Flakes must be enabled, or pass the flag shown.',
			lines: [
				"# without flakes enabled: nix --extra-experimental-features 'nix-command flakes' ...",
				'$ nix profile add github:megakuul/bob',
				'$ nix run github:megakuul/bob -- --help'
			]
		},
		{
			id: 'nixos',
			t: 'NixOS',
			d: 'Add the flake as an input and the package to your system; inputs reach the module through specialArgs.',
			lines: [
				'# flake.nix',
				'inputs.bob.url = "github:megakuul/bob";',
				' ',
				'# configuration.nix',
				'environment.systemPackages = [',
				'  inputs.bob.packages.${pkgs.system}.default',
				'];'
			]
		},
		{
			id: 'appimage',
			t: 'AppImage',
			d: 'One file, no Nix needed. Built and published by the GitHub release pipeline.',
			lines: [`$ curl -LO ${release}/bob-x86_64.AppImage`, '$ chmod +x bob-x86_64.AppImage', '$ ./bob-x86_64.AppImage --help'],
			links: [
				{ label: 'bob-x86_64.AppImage', href: `${release}/bob-x86_64.AppImage` },
				{ label: 'bob-aarch64.AppImage', href: `${release}/bob-aarch64.AppImage` }
			]
		}
	];

	const features = [
		{ t: 'Same bytes, every machine', d: 'Builds run sandboxed with pinned inputs and no network.', x: 'hermetic Nix builds, embedded rootless Nix, no daemon' },
		{ t: 'Every language, one graph', d: 'Your Go service, Rust core, C++ library and Svelte frontend build together.', x: 'Go · C · C++ · Rust · npm/TS · OCaml · proto · connect · GraphQL · OpenAPI' },
		{ t: 'From commit to shippable', d: 'Images, single-file apps and releases come straight out of the build. No Dockerfile.', x: 'OCI images · tarballs · AppImages · releases' },
		{ t: 'Rebuild only what changed', d: 'Small packages and content checks mean most changes are a cache hit.', x: 'fine-grained packages, early cutoff for generated code' },
		{ t: 'Your own build farm', d: 'Run CI, cache and registry on servers you own, with nothing to operate but S3.', x: 'HA orchestrators, zero-setup workers, S3 conditional writes' },
		{ t: 'Editors that just work', d: 'Step into a directory and your shell and editor use the exact toolchain of the build.', x: 'per-directory activation, bob dev, shims, LSP proxy' },
		{ t: 'Every platform from one laptop', d: 'Build for amd64 and arm64, debug or release, with sanitizers, from the same bob.go.', x: 'cross-compilation, profiles, sanitisers, named variants' },
		{ t: 'Errors that show your line', d: 'When something breaks, bob tells you where in bob.go and what to do.', x: 'BOB-E error codes with source positions' }
	];

	const steps = [
		{ t: 'Describe', d: 'Write what you build in bob.go.' },
		{ t: 'Plan', d: 'bob turns it into a precise build graph.' },
		{ t: 'Pin', d: 'bob.lock records every input.' },
		{ t: 'Build', d: 'Each piece builds once, locally or on workers.' },
		{ t: 'Ship', d: 'Binaries, images and releases, ready to deploy.' }
	];

	// bob vs the usual suspects. Cells stay conservative: only what each project documents,
	// "varies" / "with extra setup" where that is the honest answer. bob's column describes its
	// spec and current implementation (pre-release).
	const tools = ['bob', 'Bazel', 'Nx / Turborepo', 'Make / CMake', 'Docker builds', 'Nix flakes'];
	const compare: { row: string; cells: string[] }[] = [
		{ row: 'You describe the build in', cells: ['Go', 'Starlark', 'JSON config + scripts', 'Make / CMake language', 'Dockerfile', 'Nix language'] },
		{ row: 'Hermetic and reproducible', cells: ['yes, sandboxed', 'with hermetic toolchains', 'no, uses host tools', 'no', 'isolated; reproducible with extra care', 'yes'] },
		{ row: 'All languages in one graph', cells: ['yes', 'yes, via rules', 'JS first, others via plugins', 'via shell commands', 'per image', 'yes'] },
		{ row: 'Rebuilds only what changed', cells: ['per package, with early cutoff', 'per action', 'per task, by input hash', 'by file timestamps', 'per layer, in order', 'per derivation'] },
		{ row: 'Shared cache and CI included', cells: ['yes, bob-server', 'separate services', 'remote cache add-on', 'no', 'registry cache; CI elsewhere', 'binary caches; CI elsewhere'] },
		{ row: 'Laptop builds what CI builds', cells: ['yes, same graph', 'yes, when hermetic', 'depends on the host', 'depends on the host', 'yes, in the container', 'yes'] },
		{ row: 'Errors point at your code', cells: ['yes, your bob.go line', 'varies', 'varies', 'varies', 'varies', 'varies'] },
		{ row: 'Requires learning Nix', cells: ['no', 'no', 'no', 'no', 'no', 'yes'] }
	];

	const ecosystems = [
		{ name: 'Go', d: 'modules, binaries, tests and cgo' },
		{ name: 'C / C++', d: 'libraries and binaries, with clangd support' },
		{ name: 'Rust', d: 'crates resolved exactly as Cargo would' },
		{ name: 'TypeScript', d: 'npm packages, Vite and SvelteKit apps' },
		{ name: 'OCaml', d: 'opam packages and libraries' },
		{ name: 'APIs', d: 'protobuf, connect, GraphQL and OpenAPI code, generated' },
		{ name: 'System packages', d: 'anything in nixpkgs, as a typed dependency' },
		{ name: 'Outputs', d: 'container images, AppImages, tarballs, releases' }
	];

	const server = [
		{ k: 'Nothing else to run', d: 'S3 is the only dependency. No database, no queue, no coordinator.' },
		{ k: 'Scale by starting processes', d: 'Add orchestrators for availability and workers for speed. Workers need no setup.' },
		{ k: 'Push and it builds', d: 'GitHub, Forgejo and GitLab trigger the pipelines declared in bob.go.' },
		{ k: 'One cache for everyone', d: 'Laptops and CI pull from the same binary cache and container registry.' },
		{ k: 'Releases people can install', d: 'Publish releases that users install with one command, no build knowledge needed.' },
		{ k: 'Access you can reason about', d: 'Role-based access on every API call and single sign-on via OIDC.' }
	];

	// plan/STATUS.md, 2026-10-24. Framed as early access; implemented is not the same as released.
	const milestones: { id: string; t: string; s: 'done' | 'wip' | 'next' }[] = [
		{ id: 'M2–M7', t: 'engine, CLI, dev env', s: 'done' },
		{ id: 'M8–M11', t: 'languages', s: 'done' },
		{ id: 'M12', t: 'images, releases', s: 'done' },
		{ id: 'M13–M15', t: 'server core', s: 'done' },
		{ id: 'M16–M17', t: 'cache, pipelines', s: 'wip' },
		{ id: 'M18', t: 'web UI', s: 'done' },
		{ id: 'dist', t: 'flake, AppImages', s: 'wip' },
		{ id: 'M19', t: 'hardening, v1', s: 'next' }
	];

	const link = 'text-fg underline decoration-line underline-offset-4 hover:decoration-accent';
</script>

<svelte:head>
	<title>bob · one Go program describes your whole build</title>
	<meta
		name="description"
		content="Describe your whole build once, in Go. bob gives you reproducible builds for every language, rebuilds only what changed, and runs your CI, cache and registry on S3."
	/>
</svelte:head>

<!-- hero -->
<section class="relative overflow-hidden border-b border-line">
	<Backdrop size={32} />
	<div class="relative mx-auto max-w-[1240px] px-4 pt-14 pb-12 sm:px-6 md:pt-20">
		<p class="flex flex-wrap items-center gap-2 font-mono text-sm text-muted">
			{#each tags as t (t)}<span class="border border-line bg-bg px-2 py-0.5">{t}</span>{/each}
		</p>
		<h1 class="mt-6 max-w-[16ch] text-4xl leading-[1.02] font-extrabold tracking-[-0.035em] sm:text-5xl lg:max-w-none lg:text-6xl">
			One Go program describes your <span class="bg-accent px-2 text-on-accent [box-decoration-break:clone]">whole build.</span>
		</h1>
		<div class="mt-8 grid gap-10 lg:grid-cols-[1fr_1fr] lg:items-start">
			<div class="flex flex-col gap-6">
				<p class="max-w-[52ch] text-lg text-muted md:text-xl">
					Stop stitching together Makefiles, Dockerfiles and CI YAML. Write your build once, in Go, and get the same result on every laptop and in CI, from <span class="text-fg">git clone</span> to a running image.
				</p>
				<div class="flex flex-wrap gap-3">
					<a href="#install" class="rounded-xs border border-accent bg-accent px-5 py-2.5 font-mono text-base font-bold text-on-accent hover:border-fg">get bob</a>
					<a href="/docs" class="rounded-xs border border-line bg-bg px-5 py-2.5 font-mono text-base hover:border-fg">read the docs</a>
				</div>
			</div>
			<Terminal title="install" lines={quick} />
		</div>
		<div class="mt-12">
			<BuildGraph />
			<p class="mt-3 font-mono text-sm text-muted">One file changed. Everything else came from the cache, and the web app was skipped because its inputs came out identical.</p>
		</div>
	</div>
</section>

<!-- pain points -->
<section class="border-b border-line" aria-labelledby="pain">
	<div class="mx-auto max-w-[1240px] px-4 py-20 sm:px-6 lg:py-28">
		<SectionHead n="01" kicker="sound familiar?" id="pain" title="Your build should not be a second job.">
			{#snippet children()}The usual stack is Make, CMake or Bazel, a Dockerfile per service, a language tool per language and a few hundred lines of CI YAML holding it together. This is what that costs.{/snippet}
		</SectionHead>
		<ul class="mt-12 grid gap-px border border-line bg-line md:grid-cols-2">
			{#each pains as p (p.t)}
				<li class="flex flex-col gap-3 bg-bg p-6">
					<h3 class="text-2xl leading-tight font-bold tracking-tight">{p.t}</h3>
					<p class="text-lg text-muted">{p.was}</p>
					<p class="mt-auto flex gap-3 border-t border-line pt-3 text-lg">
						<span class="mt-[9px] h-2 w-2 shrink-0 bg-accent" aria-hidden="true"></span>
						<span><span class="sr-only">With bob: </span>{p.now}</span>
					</p>
				</li>
			{/each}
		</ul>
	</div>
</section>

<!-- what you write -->
<section class="border-b border-line" aria-labelledby="what">
	<div class="mx-auto grid max-w-[1240px] gap-12 px-4 py-20 sm:px-6 lg:grid-cols-2 lg:py-28">
		<div class="flex flex-col gap-10">
			<SectionHead n="02" kicker="what you write" id="what" title="Your whole build fits on one screen.">
				{#snippet children()}A binary and a container image for two platforms, in six lines of Go.{/snippet}
			</SectionHead>
			<ol class="flex flex-col">
				{#each pitch as p, i (p.t)}
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
		<div class="flex min-w-0 flex-col justify-end gap-4">
			<FileTree title="shop/" html={data.tree.html} text={data.tree.text} />
			<CodePanel file="shop/bob.go" code={data.sample.code} html={data.sample.html} />
			<p class="font-mono text-sm text-muted">$ bob build //... <span class="text-fg">→ the binary and an image for amd64 and arm64</span></p>
		</div>
	</div>
</section>

<!-- install -->
<section class="border-b border-line" aria-labelledby="install">
	<div class="mx-auto max-w-[1240px] px-4 py-20 sm:px-6 lg:py-28">
		<SectionHead n="03" kicker="install" id="install" title="One command to get started.">
			{#snippet children()}bob is a single executable. Pick the way that fits your machine.{/snippet}
		</SectionHead>
		<ul class="mt-12 grid gap-px border border-line bg-line lg:grid-cols-3">
			{#each installs as c, i (c.id)}
				<li class="flex min-w-0 flex-col gap-4 bg-bg p-5 sm:p-6">
					<p class="flex items-center gap-3 font-mono text-sm text-muted">
						<span class="border border-line px-1.5">{String.fromCharCode(97 + i)}</span><span class="h-px flex-1 bg-line" aria-hidden="true"></span>
					</p>
					<h3 class="text-2xl font-bold tracking-tight">{c.t}</h3>
					<p class="text-base text-muted">{c.d}</p>
					<div class="mt-auto flex flex-col gap-3">
						{#if c.links}
							<div class="flex flex-wrap gap-2">
								{#each c.links as l (l.href)}
									<a href={l.href} class="rounded-xs border border-accent bg-accent px-3 py-1.5 font-mono text-sm font-bold text-on-accent hover:border-fg" rel="external noopener">{l.label}</a>
								{/each}
							</div>
						{/if}
						<Terminal title={c.id === 'nixos' ? 'nix' : 'shell'} lines={c.lines} />
					</div>
				</li>
			{/each}
		</ul>
		<p class="mt-6 font-mono text-sm text-muted">building from source instead? see the <a href="/docs" class={link}>docs</a>.</p>
	</div>
</section>

<!-- features -->
<section class="border-b border-line" aria-labelledby="features">
	<div class="mx-auto max-w-[1240px] px-4 py-20 sm:px-6 lg:py-28">
		<SectionHead n="04" kicker="what you get" id="features" title="Everything between git push and a running image.">
			{#snippet children()}Compile, test, package and ship from one description, on your laptop or on your servers.{/snippet}
		</SectionHead>
		<ul class="mt-12 grid gap-px border border-line bg-line sm:grid-cols-2 lg:grid-cols-4">
			{#each features as f, i (f.t)}
				<li class="group relative flex flex-col gap-3 bg-bg p-6">
					<span class="absolute top-0 left-0 h-0.5 w-0 bg-accent transition-[width] duration-300 group-hover:w-full" aria-hidden="true"></span>
					<span class="font-mono text-sm text-muted">{String(i + 1).padStart(2, '0')}</span>
					<h3 class="text-xl font-bold">{f.t}</h3>
					<p class="text-base leading-relaxed text-muted">{f.d}</p>
					<p class="mt-auto border-t border-line pt-3 font-mono text-xs text-muted">{f.x}</p>
				</li>
			{/each}
		</ul>
	</div>
</section>

<!-- how -->
<section class="border-b border-line" aria-labelledby="how">
	<div class="mx-auto max-w-[1240px] px-4 py-20 sm:px-6 lg:py-28">
		<SectionHead n="05" kicker="how it works" id="how" title="Go in, artifacts out.">
			{#snippet children()}Same inputs, same output, every time. That is why a cache hit with bob is a certainty, not a guess.{/snippet}
		</SectionHead>
		<ol class="mt-12 grid gap-px border border-line bg-line md:grid-cols-5">
			{#each steps as s, i (s.t)}
				<li class="relative flex flex-col gap-2 bg-bg p-5 md:min-h-40">
					<div class="flex items-center gap-2 font-mono text-sm text-muted">
						<span class="grid h-6 w-6 place-items-center border border-line {i === 3 ? 'border-accent bg-accent text-on-accent' : ''}">{i + 1}</span>
						<span class="h-px flex-1 bg-line" aria-hidden="true"></span>
						{#if i < steps.length - 1}<span aria-hidden="true">→</span>{/if}
					</div>
					<h3 class="mt-2 text-xl font-bold">{s.t}</h3>
					<p class="text-base text-muted">{s.d}</p>
				</li>
			{/each}
		</ol>
		<p class="mt-6 max-w-[90ch] font-mono text-sm text-muted">
			under the hood: a sandboxed evaluator turns bob.go into a graph, each node becomes a small Nix derivation, and identical inputs give an identical store path. <a href="/docs" class={link}>details in the docs</a>
		</p>
	</div>
</section>

<!-- comparison -->
<section class="border-b border-line" aria-labelledby="compare">
	<div class="mx-auto max-w-[1240px] px-4 py-20 sm:px-6 lg:py-28">
		<SectionHead n="06" kicker="compare" id="compare" title="bob vs the usual suspects.">
			{#snippet children()}Great tools, each solving part of the problem. bob puts the parts in one place.{/snippet}
		</SectionHead>
		<div class="mt-12 overflow-x-auto border border-line">
			<table class="w-full min-w-[880px] border-collapse text-left text-base">
				<thead>
					<tr class="border-b border-line bg-surface">
						<th scope="col" class="w-[17%] px-4 py-3 font-mono text-sm font-normal text-muted"><span class="sr-only">question</span></th>
						{#each tools as t, i (t)}
							<th scope="col" class="px-4 py-3 font-mono text-sm {i === 0 ? 'bg-accent text-on-accent' : 'font-normal text-muted'}">{t}</th>
						{/each}
					</tr>
				</thead>
				<tbody>
					{#each compare as r (r.row)}
						<tr class="border-b border-line last:border-b-0">
							<th scope="row" class="px-4 py-3 font-semibold">{r.row}</th>
							{#each r.cells as c, i (i)}
								<td class="px-4 py-3 align-top {i === 0 ? 'border-x border-accent font-semibold text-fg' : 'text-muted'}">{c}</td>
							{/each}
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
		<p class="mt-4 max-w-[90ch] text-sm text-muted">
			Summarised from each project's public documentation as of October 2026, not from benchmarks; most of these tools can do more with plugins and extra setup, and corrections are welcome. bob's column describes its specification and current implementation: it is not released yet (see <a href="#status" class={link}>status</a>).
		</p>
	</div>
</section>

<!-- ecosystem -->
<section class="border-b border-line" aria-labelledby="ecosystem">
	<div class="mx-auto grid max-w-[1240px] gap-12 px-4 py-20 sm:px-6 lg:grid-cols-[1fr_1.4fr] lg:py-28">
		<SectionHead n="07" kicker="languages" id="ecosystem" title="Bring the languages you already ship.">
			{#snippet children()}Dependencies resolve exactly like the native tools would, then build piece by piece and land in the shared cache.{/snippet}
		</SectionHead>
		<ul class="grid border-t border-line sm:grid-cols-2">
			{#each ecosystems as e (e.name)}
				<li class="flex flex-col gap-1 border-b border-line py-4 sm:pr-6">
					<p class="font-bold">{e.name}</p>
					<p class="text-base text-muted">{e.d}</p>
				</li>
			{/each}
		</ul>
	</div>
</section>

<!-- server -->
<section class="relative overflow-hidden border-b border-line" aria-labelledby="server">
	<Backdrop size={32} />
	<div class="relative mx-auto max-w-[1240px] px-4 py-20 sm:px-6 lg:py-28">
		<SectionHead n="08" kicker="for teams" id="server" title="Your CI, cache and registry in one binary.">
			{#snippet children()}bob-server runs the pipelines you declared in bob.go on your own workers, and serves the cache, images and releases your team pulls from.{/snippet}
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
					<span class="text-fg">S3</span> <span class="text-muted">· state · logs · artifacts</span>
				</div>
				<div class="mt-3 grid grid-cols-3 gap-3 font-mono text-sm text-muted">
					<span class="border-t border-line pt-2">cache</span>
					<span class="border-t border-line pt-2">registry</span>
					<span class="border-t border-line pt-2">releases</span>
				</div>
			</div>
			<ul class="grid bg-bg sm:grid-cols-2">
				{#each server as s (s.k)}
					<li class="border-line p-5 not-last:border-b sm:nth-[odd]:border-r sm:nth-last-[2]:border-b-0">
						<p class="font-semibold text-fg">{s.k}</p>
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
		<SectionHead n="09" kicker="early access" id="status" title="Early access: try it now, help shape v1.">
			{#snippet children()}Most of bob is built and bob builds itself. The Nix flake and the AppImage release pipeline are new, and v1 is not out yet, so what you install today is a pre-release build. Implemented is not the same as verified: the full acceptance suite has not yet run green end to end.{/snippet}
		</SectionHead>
		<ol class="mt-12 grid grid-cols-2 gap-px border border-line bg-line sm:grid-cols-4 lg:grid-cols-8">
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
			<p><span class="font-mono text-sm text-fg">now ·</span> security review fixes, the last red acceptance tests, a full CI run on a quiet host, the first published flake and AppImages.</p>
			<p><span class="font-mono text-sm text-fg">next ·</span> performance budgets re-measured on a quiet machine, and installs tested end to end against a real server.</p>
			<p><span class="font-mono text-sm text-fg">v1 ·</span> hardening, examples and the first release. Follow along, or build from source, in the <a href={repo} class={link} rel="external noopener">repository</a>.</p>
		</div>
	</div>
</section>
