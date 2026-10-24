---
title: Troubleshooting
section: Server
order: 13
summary: What common server and remote-build errors mean and how to fix them.
---

This page lists the problems you are most likely to meet with a bob server, with the message
you see and the fix.

## Start-up

| Symptom | Cause | Fix |
|---|---|---|
| `✗ storage.endpoint is not set (BOB_STORAGE_ENDPOINT)`, exit 78 | no S3 endpoint | set `BOB_STORAGE_ENDPOINT` |
| `BOB-E-7007 storage backend … is not supported` | the S3 service lacks conditional writes | use a backend with `If-Match`/`If-None-Match` support (RustFS, AWS S3) |
| `BOB-E-7008 … bucket policy is too permissive` or `control bucket is readable with the worker credential`, exit 78 | the probe credential can read `bob-control` | apply `deploy/policies/` correctly |
| `BOB-E-7009 the worker must run as root` | worker started as a normal user | run workers as root (builds still run unprivileged) |
| `BOB-E-7010 worker cannot isolate builds: …` | no cgroup v2 delegation, old kernel (Landlock), or user namespaces missing | the message names the missing piece; run the worker as a systemd service with `Delegate=yes` on a recent kernel |
| `BOB_ADMIN_TOKEN is honoured only by test builds …` | `BOB_ADMIN_TOKEN` set on a release build | unset it; create admins with `bob-server admin create-user` |
| forge calls fail for a forge on a private network | outgoing requests to private addresses are refused by default | list the forge's host in `BOB_SERVER_OUTBOUND_ALLOW` |
| a tuning value is ignored | malformed value | the log shows `invalid setting ignored` with the key |

## The CLI and the server

| Error | Meaning | Fix |
|---|---|---|
| `BOB-E-4009 no bob server configured` | `--remote` without a login | `bob login <url>`, or set `BOB_SERVER` and `BOB_TOKEN` |
| `BOB-E-4009 bob server … is unreachable` | network or URL problem | check the URL; local builds still work without the cache |
| `BOB-E-4008 your login to … has expired or is invalid` | token expired, revoked or wrong | `bob login <url>` (or a new service-account token) |
| `BOB-E-4010 … denied CreateBuild on project …` | missing role binding | ask an admin for the named permission on that project |
| `BOB-E-4012 remote build … failed on the server: …` | the build failed on a worker | `bob log --build <id> <label>`; the build page in the UI |
| `BOB-E-4012 … promotion: failed: artifact verification` for an image | known issue with images of already published programs | see [OCI registry](09-oci-registry.md) |
| `BOB-E-9008 bob pipeline run --remote is not implemented yet` | not built yet | start pipelines from a forge, the UI or the API |

## Builds that do not start

- **A build stays queued.** No worker serves its class (platform), or all slots are busy.
  Check the Fleet page; start a worker of that platform.
- **A forge push starts nothing.** Check the integration's recent deliveries
  (`ListForgeDeliveries`): `invalid signature` (wrong webhook secret), `ignored: repository not
  linked` (link the repository to a project), or no pipeline trigger matched (the build is
  `SKIPPED`).
- **Workers do not register.** The worker needs write access to the fleet bucket's
  registration prefixes and an orchestrator must be running (its `registrar` controller
  approves workers).

## Downloads

- **A cache path or release asset answers 404.** The caller has no permission on any project
  that owns it, or it does not exist; the server does not tell the two apart. Check the token
  with `bob whoami`.
- **`podman pull` fails with an HTTP/HTTPS error.** The server speaks plain HTTP; use
  `--tls-verify=false` or put a TLS proxy in front.
- **503 with `Retry-After`.** The orchestrator is at `BOB_MAX_PARALLEL_DOWNLOADS`; clients
  retry.

## Next steps

- [Error codes](../05-cli/02-errors.md).
- [Limitations and status](../07-reference/03-status.md).
