<script>
  import { Badge } from "@demo/ui";
  let n = $state(0);
</script>
<button onclick={() => n++}>svelte-marker {n}</button>
<Badge label="mono" />
