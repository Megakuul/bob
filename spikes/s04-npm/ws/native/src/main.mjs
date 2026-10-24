import Database from "better-sqlite3";
import { buildSync } from "esbuild";
import _ from "lodash4";
const db = new Database(":memory:");
db.exec("create table t(x int)"); db.prepare("insert into t values (?)").run(41);
const r = buildSync({ stdin: { contents: "export const a: number = 1" , loader: "ts" }, write: false, format: "esm" });
console.log(JSON.stringify({ sqlite: db.prepare("select sqlite_version() v, x+1 y from t").get(), esbuild: r.outputFiles[0].text.trim(), lodash: _.camelCase("bob npm") }));
