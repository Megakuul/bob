# S4 measure results (indicative, busy host, 2026-10-21)

Note: the "dependency added" lines inside measure-8c.txt / measure-2c.txt come from the first script version (hoisted number included a vite build; the 2c run hit an already built variant). Use the *-depchange files for that row.

## tmp/measure-8c.txt
```
== resolve (8c): cold registry cache / warm (ETag revalidation) / offline cache
vite-react packages=119 cold=2.52s warm=1.99s offline=0.33s
ts-lib packages=206 cold=2.79s warm=2.60s offline=0.76s
fastify packages=94 cold=1.45s warm=1.15s offline=0.16s
native packages=69 cold=1.27s warm=1.66s offline=0.09s
mono packages=88 cold=2.15s warm=1.60s offline=0.16s
stress packages=411 cold=1.53s warm=1.07s offline=0.35s
== install: isolated (one drv per package) vs hoisted (one drv), vite-react; tarballs already fetched
web cold(build all package drvs + node_modules + tsc + vite build)=13.27s noop=1.37s node_modules=326.4MiB storePaths=66
web-hoisted cold(build all package drvs + node_modules + tsc + vite build)=10.01s noop=0.47s node_modules=60.9MiB storePaths=1
== cold install including downloads (vite-react isolated)
web cold+fetch=13.18s
== node_modules only (materialise), vite-react: cold / no-op
materialise cold=4.23s noop=0.58s
== one source file changed -> rebuild
vite-react App.tsx changed: isolated=5.58s
vite-react App.tsx changed: hoisted=5.74s
ts-lib index.ts changed (tsc lib)=8.15s
fastify server.ts changed (tsc + esbuild bundle)=5.81s
== dependency added (clsx) -> relock + rebuild node_modules
relock=2.16s isolated materialise=0.97s hoisted rebuild(+vite build)=7.39s
done
```

## tmp/measure-8c-depchange.txt
```
== dependency added (clsx) -> relock + rebuild node_modules
derivations that change: 4
relock=2.10s isolated materialise=0.81s hoisted node_modules rebuild=2.31s
done
```

## tmp/measure-2c.txt
```
== resolve (2c): cold registry cache / warm (ETag revalidation) / offline cache
vite-react packages=119 cold=3.35s warm=2.74s offline=0.71s
ts-lib packages=206 cold=4.74s warm=3.44s offline=1.88s
fastify packages=94 cold=2.01s warm=1.40s offline=0.35s
native packages=69 cold=1.28s warm=1.41s offline=0.15s
mono packages=88 cold=1.92s warm=3.73s offline=0.33s
stress packages=411 cold=2.13s warm=1.58s offline=0.94s
== install: isolated (one drv per package) vs hoisted (one drv), vite-react; tarballs already fetched
web cold(build all package drvs + node_modules + tsc + vite build)=15.63s noop=0.84s node_modules=326.4MiB storePaths=66
web-hoisted cold(build all package drvs + node_modules + tsc + vite build)=13.33s noop=0.87s node_modules=60.9MiB storePaths=1
== cold install including downloads (vite-react isolated)
web cold+fetch=18.02s
== node_modules only (materialise), vite-react: cold / no-op
materialise cold=4.23s noop=0.42s
== one source file changed -> rebuild
vite-react App.tsx changed: isolated=5.92s
vite-react App.tsx changed: hoisted=7.22s
ts-lib index.ts changed (tsc lib)=14.68s
fastify server.ts changed (tsc + esbuild bundle)=11.37s
== dependency added (clsx) -> relock + rebuild node_modules
relock=2.44s isolated materialise=1.13s hoisted rebuild(+vite build)=0.91s
done
```

## tmp/measure-2c-depchange.txt
```
== dependency added (clsx) -> relock + rebuild node_modules
derivations that change: 4
relock=2.51s isolated materialise=0.69s hoisted node_modules rebuild=2.32s
done
```
