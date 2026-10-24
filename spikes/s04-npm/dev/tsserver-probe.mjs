// tsserver-probe.mjs <tsserver-cmd> <file> <line> <offset> : drive tsserver over stdio like an editor.
import { spawn } from "node:child_process";
import fs from "node:fs";
const [cmd, file, line, offset] = process.argv.slice(2);
const p = spawn(cmd, ["--disableAutomaticTypingAcquisition"], { stdio: ["pipe", "pipe", "inherit"] });
let buf = "", seq = 0;
const pending = new Map();
p.stdout.on("data", (d) => {
  buf += d;
  for (;;) {
    const m = buf.match(/Content-Length: (\d+)\r\n\r\n/);
    if (!m) break;
    const start = m.index + m[0].length, len = Number(m[1]);
    if (Buffer.byteLength(buf.slice(start)) < len) break;
    const body = Buffer.from(buf.slice(start)).subarray(0, len).toString();
    buf = Buffer.from(buf.slice(start)).subarray(len).toString();
    const msg = JSON.parse(body);
    if (msg.type === "response" && pending.has(msg.request_seq)) { pending.get(msg.request_seq)(msg); pending.delete(msg.request_seq); }
  }
});
const req = (command, args) => new Promise((res) => { const s = ++seq; pending.set(s, res); p.stdin.write(JSON.stringify({ seq: s, type: "request", command, arguments: args }) + "\n"); });
const t0 = performance.now();
p.stdin.write(JSON.stringify({ seq: ++seq, type: "request", command: "open", arguments: { file, fileContent: fs.readFileSync(file, "utf8") } }) + "\n");
const diag = await req("semanticDiagnosticsSync", { file });
const t1 = performance.now();
const qi = await req("quickinfo", { file, line: +line, offset: +offset });
const def = await req("definition", { file, line: +line, offset: +offset });
const t2 = performance.now();
console.log(JSON.stringify({ firstDiagnosticsMs: Math.round(t1 - t0), quickinfoDefMs: Math.round(t2 - t1), diagnostics: diag.body?.map((d) => d.text), quickinfo: qi.body?.displayString?.slice(0, 80), definition: def.body?.[0]?.file, defExistsHere: def.body?.[0] ? fs.existsSync(def.body[0].file) : null }));
p.kill();
