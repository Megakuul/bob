// bins.mjs <bindir> <bash> <node> <pkgdir>... : write .bin wrappers for the packages' "bin" entries.
// Wrappers call node/bash by absolute store path, so no /usr/bin/env is needed in the sandbox.
import fs from "node:fs";
import path from "node:path";
const [bindir, bash, node, ...dirs] = process.argv.slice(2);
fs.mkdirSync(bindir, { recursive: true });
for (const d of dirs) {
  let pj;
  try { pj = JSON.parse(fs.readFileSync(path.join(d, "package.json"), "utf8")); } catch { continue; }
  let bin = pj.bin;
  if (typeof bin === "string") bin = { [pj.name.split("/").pop()]: bin };
  if (!bin && pj.directories?.bin) {
    bin = {};
    for (const f of fs.readdirSync(path.join(d, pj.directories.bin))) bin[f] = path.join(pj.directories.bin, f);
  }
  for (const [name, rel] of Object.entries(bin || {})) {
    if (!/^[A-Za-z0-9@._-]+$/.test(name)) continue;
    const target = fs.realpathSync(path.join(d, rel));
    const head = fs.readFileSync(target).subarray(0, 128).toString("latin1");
    let interp = "";
    if (head.startsWith("#!")) {
      const line = head.split("\n")[0];
      interp = /\bnode\b/.test(line) ? node : /\b(ba)?sh\b/.test(line) ? bash : "";
    } else if (!head.startsWith("\x7fELF")) interp = node; // npm runs extension-less JS bins with node
    const w = path.join(bindir, name);
    fs.rmSync(w, { force: true });
    fs.writeFileSync(w, `#!${bash}\nexec ${interp ? interp + " " : ""}${JSON.stringify(target)} "$@"\n`, { mode: 0o755 });
  }
}
