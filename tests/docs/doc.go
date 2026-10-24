// Package docs is the documentation guardrail (HARNESS.md §17): the user documentation in
// website/content/docs must stay upstream of the implementation. Its tests fail when
//
//   - a page has invalid frontmatter, a relative link or an anchor that does not resolve;
//   - a command or flag of the CLI command table (pkg/cli) is missing from, or stale in, the
//     CLI reference page;
//   - an exported function of a public bob.go API package is not mentioned in the guides, or
//     the generated API index is out of date;
//   - the generated error-code table differs from the diag code table (pkg/bob/diag);
//   - a BOB_* environment variable read by bob-server is missing from the server
//     configuration page, or one read by bob is missing from the documentation;
//   - a bob.go snippet marked for verification does not type-check against the framework.
//
// The tests read the source tree and the markdown only (no network, no Nix); one test runs
// `go vet -tags=bob` on the snippets in a scratch module with the framework replaced by this
// checkout. TestQuickstartsE2E (opt-in, BOB_DOCS_E2E=<path of a bob binary>) runs the
// quickstart pages end to end. The marker syntax is documented in HARNESS.md §17.
// BOB_DOCS_UPDATE=1 rewrites the generated blocks instead of comparing them.
package docs
