<script lang="ts">
  import { greet } from "@fix/ts-lib";

  let name = $state("bob");
</script>

<input bind:value={name} />
<p>{greet({ name, age: 1 })}</p>
