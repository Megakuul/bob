import { Hono } from "hono";
import { z } from "zod";
const app = new Hono();
app.get("/hi/:n", (c) => c.json({ hi: z.string().parse(c.req.param("n")), bun: Bun.version }));
const port = Number(process.env.PORT ?? 20420);
Bun.serve({ port, hostname: "127.0.0.1", fetch: app.fetch });
console.log("bun listening", port);
