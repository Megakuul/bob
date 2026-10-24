// lifecycle.mjs <pkgdir> : print the install-time scripts npm would run, one per line (JSON strings).
import fs from "node:fs";
import path from "node:path";
const d = process.argv[2];
const pj = JSON.parse(fs.readFileSync(path.join(d, "package.json"), "utf8"));
const s = pj.scripts || {};
const out = [];
for (const k of ["preinstall", "install", "postinstall"]) if (s[k]) out.push(s[k]);
if (!s.install && !s.preinstall && fs.existsSync(path.join(d, "binding.gyp"))) out.splice(0, 0, "node-gyp rebuild");
for (const x of out) console.log(JSON.stringify(x));
