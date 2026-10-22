// Package api implements the connect services of the orchestrator (SPEC §6.1, §6.4, §6.5).
//
// Server mounts every service of proto/ under /api/. One interceptor guards every RPC: it
// recovers panics, authenticates the caller (Authenticator, WorkerAuthenticator), enforces the
// method's bob.authz.v1.rule (Authorizer, Resolver) and turns every error into the §6.1 error
// model (a connect code plus exactly one bob.common.v1.ErrorDetail). New refuses to build a
// server in which any method lacks a valid rule.
//
// Service implementations live with their features and are passed in Services; list RPCs
// read §05 index documents by known key (store primitives), never S3 LIST (R-API-151).
package api
