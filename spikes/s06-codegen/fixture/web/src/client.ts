// Calls the Go connect server through the protoc-gen-es generated service descriptor and
// queries the gqlgen endpoint with graphql-codegen generated operation types.
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { GreetService } from "../gen/proto/greet/v1/greet_pb.js";
import type { HelloQuery, HelloQueryVariables } from "../gen/graphql/graphql.js";

const base = process.argv[2] ?? "http://127.0.0.1:20600";
const client = createClient(GreetService, createConnectTransport({ baseUrl: base, httpVersion: "1.1" }));

const res = await client.greet({ name: "ts-client" });
console.log(`ts client: ${res.greeting} (request_id=${res.meta?.requestId})`);

const HELLO = "query Hello($name: String!) { hello(name: $name) { greeting length } }";
const variables: HelloQueryVariables = { name: "ts-gql" };
const r = await fetch(base + "/graphql", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ query: HELLO, variables }),
});
const body = (await r.json()) as { data: HelloQuery };
console.log(`ts graphql: ${body.data.hello.greeting} length=${body.data.hello.length}`);
