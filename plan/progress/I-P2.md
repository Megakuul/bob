# I-P2 progress (A109 loopback gate, harness -tags bobtest)

## done
1. pkg/engine/fetch: `testLoopbackHTTP()` is now a func. loopback_bobtest.go (-tags bobtest)
   reads BOB_TEST_HTTP_LOOPBACK=1 (the only place the env is read); loopback.go (release)
   returns false. Tests: loopback_bobtest_test.go (allowed with env, refused without env),
   loopback_release_test.go (sets the env, still refused). TestFileFlow and
   tests/nix TestRNIX014HTTPAndSizeCap pass with and without -tags bobtest (env unset there).
2. tests/serverenv/buildtags.go: BobtestTagsFlag() = `-tags=bobtest[,<tags of GOFLAGS>]`.
   tests/harness (bob, bob-server, bob-builder) and tests/serverenv/stack (bob-server) use it.
3. BOB_TEST_HTTP_LOOPBACK=1 set for bob processes in: tests/eco/cc (bobEnv, runAs, registryEnv),
   ecotest.Registry (npm/opam mirror consumers), eco/golang and eco/rust registryEnv, eco/npm
   faultRegistry test. file:// GOPROXY users and tests/eco/gen (fixture servers, not bob
   fetches) unchanged.
4. tests/platforms/platform_test.go restrictedAppGo: cc.Binary(...) call -> cc.NewBinary.
   No permission block hit.

## notes
- tests/errors builds its own bob/bob-server (errors_test.go:58) with the caller's GOFLAGS; not
  changed (not harness-built).
