#!/bin/sh
# Style checklist helper for the code steward (SPEC R-UI-003, D45; not a CI gate). Prints every
# line under web/src that uses something the visual rules ban: gradients, radii above
# rounded-xs, shadows, blur, glow, @apply, class-composition helpers, emoji. Exit 1 on findings.
cd "$(dirname "$0")/../src" || exit 2
status=0
check() {
	label=$1
	shift
	out=$(grep -rnE "$@" --include='*.svelte' --include='*.ts' --include='*.css' --exclude-dir=gen . 2>/dev/null)
	if [ -n "$out" ]; then
		printf '%s\n%s\n\n' "== $label" "$out"
		status=1
	fi
}
check 'gradient' 'gradient|bg-linear|bg-radial|bg-conic'
check 'radius above rounded-xs' 'rounded(-(sm|md|lg|xl|2xl|3xl|full)|[" ])|border-radius'
check 'shadow' '(^|[ "])(shadow|drop-shadow|inset-shadow)(-[a-z0-9]+)?([ "]|$)|box-shadow'
check 'blur or glass' '(^|[ "])(backdrop-)?blur(-[a-z0-9]+)?([ "]|$)|backdrop-filter|filter: *blur'
check '@apply' '@apply'
check 'class-composition helper' "clsx|class-variance-authority|tailwind-merge|twMerge|cva\\("
check 'emoji' -P '[\x{1F300}-\x{1FAFF}\x{2600}-\x{27BF}]'
exit $status
