import Fastify from "fastify";
import cors from "@fastify/cors";
const app = Fastify({ logger: false });
await app.register(cors);
app.get("/hello", async () => ({ hello: "bob", node: process.version }));
const port = Number(process.env.PORT ?? 20410);
await app.listen({ port, host: "127.0.0.1" });
console.log(`listening ${port}`);
