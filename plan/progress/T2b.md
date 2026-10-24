# T2b progress
DONE R-CLI-001 → tests/cli/help_test.go::TestRCLI001CommandSet,TestRCLI001UnknownCommand (stub: did-you-mean in pkg/cli/cli.go; goldens tests/goldens/cli/help)
DONE R-CLI-005 → tests/cli/nosrv_test.go::TestRCLI005NoServer (testdata repos exits,pipe added)
DONE R-CLI-006 → tests/cli/exitcodes_test.go (4 tests; fault-catalogue live exit check is tests/errors TestFaults which already Covers R-CLI-006)
DONE R-CLI-002 → tests/cli/output_test.go::TestRCLI002JSONStream,NoopBuild,VerboseAddsLines (validator events_test.go)
DONE R-CLI-007 → tests/cli/output_test.go::TestRCLI007HumanOutput + TestRCLI007Colour (pty helper pty_test.go)
DONE R-CLI-009 → tests/cli/commands_test.go::TestRCLI009Check,TestRCLI009CheckLock
DONE R-CLI-010 → tests/cli/commands_test.go::TestRCLI010Lock
DONE R-CLI-011 → tests/cli/commands_test.go::TestRCLI011Graph
DONE R-CLI-012 → tests/cli/commands_test.go::TestRCLI012PipelineRun (stub: hidden __complete in pkg/cli)
DONE R-CLI-004 → tests/cli/completion_test.go::TestRCLI004CompleteLabels,TestRCLI004Scripts
DONE R-CLI-008 → tests/cli/logs_test.go::TestRCLI008LocalLog,TestRCLI008ServerLog (needs devstack.json api_url/admin_token; testdata repo lines)
NOTE fakeserver (tests/cli/fakeserver: oauth, connect, signed /nix cache) done+self-tested; next R-CLI-003 login_test.go, then tests/install
DONE R-CLI-003 → tests/cli/login_test.go (4 tests, fakeserver)
DONE R-INST-001 → tests/install/lifecycle_test.go (Lifecycle, KillPoints)
DONE R-INST-002 → tests/install/nonix_test.go (3 tests)
DONE R-INST-003 → tests/install/auth_test.go (2 tests; server-side §6.8 half left to server suites)
DONE R-INST-004 → tests/install/registry_test.go (3 tests)
DONE R-INST-005 → tests/install/profile_test.go (3 tests; goldens tests/goldens/install/* do not exist yet: created at implementation via golden review)
DONE R-INST-006 → tests/install/isolation_test.go
