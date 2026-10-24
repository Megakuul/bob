<script lang="ts">
	import { licence, repo } from '$lib/site';
	import Logo from './Logo.svelte';
</script>

<footer class="border-t border-line">
	<div class="mx-auto grid max-w-[1240px] gap-8 px-4 py-10 sm:px-6 md:grid-cols-[1fr_auto_auto]">
		<div class="flex flex-col gap-3">
			<Logo />
			<p class="max-w-[46ch] text-base text-muted">
				Typed Go build definitions on an invisible Nix engine, plus a build and cache server that needs only S3.
			</p>
		</div>
		<nav class="flex flex-col gap-2 font-mono text-sm" aria-label="Footer">
			<a href="/docs" class="text-muted hover:text-fg">docs</a>
			<a href="/#status" class="text-muted hover:text-fg">status</a>
			<a href={repo} class="text-muted hover:text-fg" rel="external noopener">source code</a>
		</nav>
		<div class="flex flex-col gap-2 font-mono text-sm text-muted">
			<span>licence <a href="https://www.gnu.org/licenses/agpl-3.0.html" class="text-fg underline decoration-line underline-offset-4 hover:decoration-accent" rel="external noopener">{licence}</a></span>
			<span>static site · no tracking · no cookies</span>
		</div>
	</div>
</footer>
