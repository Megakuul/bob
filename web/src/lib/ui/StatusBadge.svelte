<script lang="ts">
	// Small square badge in the state colour; running states are a yellow fill (SPEC §6.10.2).
	let { state }: { state: string } = $props();

	const look = {
		run: 'border-accent bg-accent text-on-accent',
		ok: 'border-ok text-ok',
		fail: 'border-fail text-fail',
		warn: 'border-warn text-warn',
		idle: 'border-idle text-idle'
	};

	function tone(s: string): keyof typeof look {
		switch (s) {
			case 'RUNNING':
			case 'BUILDING':
			case 'CLAIMED':
			case 'PUBLISHING':
				return 'run';
			case 'SUCCEEDED':
			case 'CACHED':
			case 'ACTIVE':
			case 'PUBLISHED':
			case 'OK':
				return 'ok';
			case 'FAILED':
			case 'REVOKED':
				return 'fail';
			case 'DRAINING':
			case 'STALE':
			case 'RETIRING':
			case 'VERIFY_ONLY':
				return 'warn';
			default:
				return 'idle';
		}
	}
</script>

<span class="inline-block rounded-xs border px-1.5 text-xs leading-4 font-medium whitespace-nowrap {look[tone(state)]}">{state || 'UNKNOWN'}</span>
