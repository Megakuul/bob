// hmr-probe.mjs <base-url> <file> <n> : edit <file> n times and measure save -> HMR "update" message.
import fs from "node:fs";
const [base, file, n = "10", mode = "write"] = process.argv.slice(2);
const orig = fs.readFileSync(file, "utf8");
for (const p of ["/", "/@vite/client", "/src/main.tsx", "/src/App.tsx", "/src/main.js", "/src/App.svelte"]) {
  try { await fetch(base + p).then((r) => r.text()); } catch {}
}
const ws = new WebSocket(base.replace("http", "ws") + "/", "vite-hmr");
const msgs = [];
let waiter = null;
ws.onmessage = (e) => { const m = JSON.parse(e.data); msgs.push(m); if (waiter) waiter(m); };
await new Promise((res) => (waiter = (m) => m.type === "connected" && res()));
const lat = [];
try {
  for (let i = 1; i <= Number(n); i++) {
    const marker = `marker-v${i + 1}`;
    const got = new Promise((res, rej) => {
      const t = setTimeout(() => rej(new Error("no HMR update within 10s")), 10000);
      waiter = (m) => { if (m.type === "update" || m.type === "full-reload") { clearTimeout(t); res(m); } };
    });
    const t0 = performance.now();
    const text = orig.replace(/marker-v1|svelte-marker/, marker);
    if (mode === "rename") { fs.writeFileSync(file + ".tmp~", text); fs.renameSync(file + ".tmp~", file); } // editor-style atomic save
    else fs.writeFileSync(file, text);
    const m = await got;
    const t1 = performance.now();
    const path = m.updates?.[0]?.path ?? m.path;
    const body = await fetch(base + (path ?? "/src/App.tsx") + "?t=" + Date.now()).then((r) => r.text());
    lat.push(t1 - t0);
    if (!body.includes(marker)) throw new Error("served module does not contain " + marker);
    await new Promise((r) => setTimeout(r, 300));
  }
} finally {
  fs.writeFileSync(file, orig);
  ws.close();
}
lat.sort((a, b) => a - b);
console.log(JSON.stringify({ n: lat.length, type: msgs.filter((m) => m.type !== "connected").map((m) => m.type)[0], median_ms: +lat[lat.length >> 1].toFixed(1), max_ms: +lat[lat.length - 1].toFixed(1), min_ms: +lat[0].toFixed(1) }));
