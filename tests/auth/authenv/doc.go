// Package authenv is the shared environment of the auth and rbac suites: the all-in-one
// devstack, a Connect JSON client for bob's API (cookie, token or anonymous), a scripted
// "browser" that follows redirects with its own cookie jar, and bobtest-idp, an in-process
// OIDC provider (R-AUTH-001).
//
// While bob-server is a stub the stack has no API URL; Start then fails the test as red
// ("not implemented"), which is the only acceptable reason for these suites to fail.
package authenv
