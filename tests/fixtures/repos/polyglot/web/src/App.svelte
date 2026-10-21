<script lang="ts">
  import { create, toBinary } from "@bufbuild/protobuf";
  import { GreetingSchema } from "../gen/proto/greeting/v1/greeting_pb.js";

  const g = create(GreetingSchema, { id: "web", text: "hello from svelte" });
  const size = toBinary(GreetingSchema, g).length;
</script>

<h1>{g.text}</h1>
<p>proto_bytes={size}</p>
