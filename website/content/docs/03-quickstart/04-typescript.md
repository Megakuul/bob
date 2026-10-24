---
title: TypeScript and npm quickstart
section: Quickstart
order: 4
summary: Build a TypeScript library with vitest tests, a Node program and a Vite site from npm dependencies, without running npm install.
---

In this quickstart you build three npm "importers" with bob: a TypeScript library with a
vitest test, a Node program that uses the library, and a static Vite site. bob resolves the
npm packages itself, pins them in `bob.lock`, and writes `package.json`, `tsconfig.json` and
`node_modules` for your editor. You never run `npm install`.

You need [bob installed](../02-install/02-install-bob.md). You do not need Node.js installed.

## The project

```text
hello-ts/
├── go.mod               needed to evaluate bob.go
├── bob.go               nixpkgs pin
├── greet/               the library @hello/greet
│   ├── bob.go
│   └── src/index.ts, src/index.test.ts
├── app/                 a Node program
│   ├── bob.go
│   └── src/main.ts
└── web/                 a Vite site
    ├── bob.go
    ├── index.html
    ├── vite.config.ts
    └── src/main.ts
```

```bash
mkdir hello-ts && cd hello-ts
git init
```

## The library

`greet/src/index.ts` validates its input with zod:

```ts file=greet/src/index.ts
import { z } from "zod";

export const User = z.object({ name: z.string().min(1) });
export type User = z.infer<typeof User>;

export function greet(u: User): string {
  return `hello, ${User.parse(u).name}`;
}
```

`greet/src/index.test.ts`:

```ts file=greet/src/index.test.ts
import { expect, test } from "vitest";
import { greet } from "./index.js";

test("greet", () => {
  expect(greet({ name: "bob" })).toBe("hello, bob");
});
```

`greet/bob.go`. `npm.Dev()` marks tooling: it is available to builds and tests but never
reaches a runtime closure.

```go file=greet/bob.go
//go:build bob

package greet

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// Lib is the TypeScript library @hello/greet (label //greet:greet).
var Lib = bob.Define(func(p *bob.P) *npm.Lib {
	return npm.Library(p, npm.LibOpts{
		Name: "@hello/greet",
		Deps: []npm.Dependency{
			npm.Dep("zod", "^4.0.0"),
			npm.Dep("typescript", "~5.9.2", npm.Dev()),
			npm.Dep("@types/node", "^24", npm.Dev()),
			npm.Dep("vitest", "^3.2.0", npm.Dev()),
		},
	})
})

// Test runs vitest over src/**/*.test.ts (label //greet:test).
var Test = bob.Define(func(p *bob.P) *npm.Test {
	return npm.NewTest(p, npm.TestOpts{Of: Lib(p)})
})
```

## The Node program

`app/src/main.ts` imports the library by its package name:

```ts file=app/src/main.ts
import { greet } from "@hello/greet";

const name = process.argv[2] ?? "world";
console.log(greet({ name }));
```

`app/bob.go` adds the library as a dependency, by value. bob bundles the program with esbuild
and wraps it in `bin/app`, which calls the pinned Node:

```go file=app/bob.go
//go:build bob

package app

import (
	"example.com/hellots/greet"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// App is a Node program (label //app:app).
var App = bob.Define(func(p *bob.P) *npm.App {
	return npm.NewApp(p, npm.AppOpts{
		Entry: "src/main.ts",
		Deps: []npm.Dependency{
			greet.Lib(p),
			npm.Dep("@types/node", "^24", npm.Dev()),
		},
	})
})
```

## The Vite site

`web/index.html`, `web/vite.config.ts` and `web/src/main.ts` are ordinary Vite files. The
Vite config stays yours: bob does not generate it.

```html file=web/index.html
<!doctype html>
<html>
  <body>
    <p id="msg"></p>
    <script type="module" src="/src/main.ts"></script>
  </body>
</html>
```

```ts file=web/vite.config.ts
import { defineConfig } from "vite";

export default defineConfig({});
```

```ts file=web/src/main.ts
import { greet } from "@hello/greet";

document.getElementById("msg")!.textContent = greet({ name: "web" });
```

```go file=web/bob.go
//go:build bob

package web

import (
	"example.com/hellots/greet"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// Site is a static Vite site (label //web:web).
var Site = bob.Define(func(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{
		Deps: []npm.Dependency{
			greet.Lib(p),
			npm.Dep("vite", "^7.1.0", npm.Dev()),
		},
	})
})
```

## The root files

```go file=bob.go
//go:build bob

package hellots

import "github.com/megakuul/bob/pkg/bob"

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})
```

```text file=go.mod
module example.com/hellots

go 1.26.0
```

## Lock, build, test, run

`bob lock` resolves every importer's dependencies against the npm registry (semver ranges,
peer and optional dependencies, platform packages) and pins each package with its integrity
hash:

```bash run
bob lock
```

```text expect
bob.lock: … changes
  + nixpkgs  nixpkgs   …
…
  + npm      zod   4.…
```

```bash run
bob build
```

```text expect
built   //app:app
built   //greet:greet
built   //web:web
bob: 3 built
```

```bash run
bob test
```

```text expect
PASS    //greet:test (…s)
bob: 1 passed, 0 failed, 0 cached
```

```bash run
bob run //app:app -- bob
```

```text expect
hello, bob
```

The site's output is the static `dist` directory of `vite build`. `--print-paths` shows where
it is:

```bash run
bob build //web:web --print-paths
```

```text expect
//web:web …/nix/store/…-web
```

## Lifecycle scripts

bob does not run npm `install`/`postinstall` scripts unless you allow them per package.
A package that needs one fails with `BOB-E-2013` and a hint. Allow it in the importer's
options; the script then runs sandboxed and offline:

```go
Options: []npm.Option{npm.AllowScripts("esbuild", "better-sqlite3")},
```

## Editor support and the dev server

bob writes `package.json`, `tsconfig.json` and `node_modules` (links into bob's store) in each
importer directory, so the TypeScript language server resolves every import. Commands of your
direct dependencies (`tsc`, `vite`, `vitest`) are on `PATH` inside the dev shell:

```bash run
bob dev -- sh -c 'node --version; command -v vite'
```

```text expect
bob: dev shell default (… node 24.… · …
v24.…
…/web/node_modules/.bin/vite
```

Run the Vite dev server with hot reload from the dev shell:

```bash
bob dev -- sh -c 'cd web && vite'
```

To check it from a script, start it in the background on a fixed port and fetch a module:

```bash run
bob dev -- sh -c 'cd web && exec vite --port 5199 --strictPort' > vite.log 2>&1 &
sleep 15
curl -s http://localhost:5199/src/main.ts | head -1
```

```text expect
import { greet } from "/node_modules/.vite/deps/@hello_greet.js…";
```

> [!WARNING]
> Declaring dev servers in a dev shell (`bob.DevShell{Servers: …}`) is accepted, but
> `bob dev` does not start them yet: it reports `dev shell default has no dev servers`. Start
> Vite yourself inside `bob dev`, as above.

## Next steps

- All npm options (Bun runtime, hoisted layout, phantom dependencies, test runners):
  [bob.go reference](../04-guides/01-bob-go-reference.md#typescript-and-npm).
- Generate a typed API client: [Code generation quickstart](06-codegen.md).
- Ship the site or the program: [Images, tarballs and AppImages](07-outputs.md).
