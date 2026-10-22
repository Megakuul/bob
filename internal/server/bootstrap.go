//go:build !bobtest

package server

// bootstrapToken reports whether BOB_ADMIN_TOKEN is honoured. Release builds refuse it: the
// first admin is created with `bob-server admin create-user` (SPEC §6.3.2), and a bearer
// token from the environment is a credential no production server should accept.
const bootstrapToken = false
