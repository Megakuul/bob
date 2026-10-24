---
title: TypeScript quickstart
section: Quickstart
order: 4
summary: Build, test and run a tiny TypeScript program on Node with a vitest test - bob resolves npm packages itself, no npm install.
---

In this quickstart you build a one-file TypeScript program for Node, with a vitest test. bob
resolves the npm packages, pins them in `bob.lock`, and writes `package.json`,
`tsconfig.json` and `node_modules` for your editor. You never run `npm install`.

You need [bob installed](../02-install/02-install-bob.md). You do not need Node.js installed.

## The project

```tree
📁 hello-ts/
  📄 bob.go
  📁 src/
    📄 main.ts
    📄 greeting.ts
    📄 greeting.test.ts
```

```bash
mkdir hello-ts && cd hello-ts
git init
```

## Write the code

```ts file=src/greeting.ts
export function greeting(name: string): string {
  return `hello, ${name}`;
}
```

```ts file=src/main.ts
import { greeting } from "./greeting.js";

console.log(greeting("bob"));
```

```ts file=src/greeting.test.ts
import { expect, test } from "vitest";
import { greeting } from "./greeting.js";

test("greeting", () => {
  expect(greeting("bob")).toBe("hello, bob");
});
```

## Describe the build

```go file=bob.go
//go:build bob

package hellots

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

var Hello = bob.Define(func(p *bob.P) *npm.App {
	return npm.NewApp(p, npm.AppOpts{
		Name:  "hello",
		Entry: "src/main.ts",
		Deps:  []npm.Dependency{npm.Dep("vitest", "^3.2.0", npm.Dev())},
	})
})

var Test = bob.Define(func(p *bob.P) *npm.Test {
	return npm.NewTest(p, npm.TestOpts{Of: Hello(p)})
})
```

`npm.Dev()` marks a tool: tests and builds see it, the program's runtime does not. bob
bundles the program with esbuild and wraps it in `bin/hello`, which runs the pinned Node.

## Lock, build, test, run

`bob lock` resolves vitest and everything it needs from the npm registry and pins each
package with its integrity hash:

```bash run
bob lock
```

```text expect
bob.lock: … changes
  + nixpkgs  nixpkgs   …
…
  + npm      vitest   3.…
```

```bash run
bob build
bob test
bob run //:hello
```

```text expect
built   //:hello
bob: 1 built
PASS    //:test (…s)
bob: 1 passed, 0 failed, 0 cached
hello, bob
```

## Editor support

bob writes `package.json`, `tsconfig.json` and `node_modules` (links into bob's store), so the
TypeScript language server resolves every import. Node, `tsc` and the commands of your direct
dependencies are in the dev shell:

```bash run
bob dev -- sh -c 'node --version; vitest --version'
```

```text expect
bob: dev shell default (… node 24.… · …
v24.…
vitest/3.…
```

## Libraries, sites and scripts

- `npm.Library` builds a TypeScript library that other importers depend on by value.
- `npm.Vite` builds a static site with `vite build`. Run its dev server inside the dev shell
  (`bob dev -- vite`); `bob dev` does not start dev servers by itself yet.
- Packages with install scripts must be allowed: `npm.AllowScripts("esbuild")` (otherwise
  `BOB-E-2013`).

See the [bob.go reference](../04-guides/01-bob-go-reference.md#typescript-and-npm).

## Next steps

- [Code generation quickstart](06-codegen.md).
- Ship it: [Images, tarballs and AppImages](07-outputs.md).
