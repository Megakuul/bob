---
title: Install bob-server
section: Server
order: 2
summary: Requirements, S3 buckets, the first orchestrator, the first admin and the first worker - a complete single-host setup.
---

You set up a working server on one host: an S3 backend with four buckets, one orchestrator,
the first admin user and one worker. Every command on this page was run on a test host with
RustFS as the S3 backend.

## Requirements

| Part | Needs |
|---|---|
| S3 | an S3 API with conditional writes (`If-Match`, `If-None-Match`). RustFS 1.0.3 is the tested backend; other AWS-compatible services with conditional writes should work |
| orchestrator | Linux, network access to S3; no special privileges; a TLS reverse proxy in front for real use (it serves plain HTTP) |
| worker | Linux, **root**, cgroup v2 with the memory, pids and cpu controllers delegated (a systemd service with `Delegate=yes`), Landlock ABI 4 or newer (kernel 6.7+), unprivileged user namespaces, disk for its Nix store |

A worker that cannot isolate builds refuses to start with `BOB-E-7010` and names the missing
piece. Builds themselves never run as root.

## Get the binary

`bob-server` is built from the same repository as `bob`:

```bash
CGO_ENABLED=0 go build -trimpath -o bob-server ./cmd/bob-server
```

```text
$ ./bob-server --help
usage: bob-server <mode>

modes:
  orchestrator   serve the API and run the controllers
  worker         run builds for the orchestrators
  admin          administrative commands: create-user --login <name> [--admin]
  store fsck     check that every record of the control and fleet buckets decodes and validates

Configuration comes from BOB_* environment variables (SPEC §05.11.1).
```

Configuration is environment variables only; the [configuration reference](03-configuration.md)
lists them all.

## Create the buckets

Create `bob-control`, `bob-fleet`, `bob-analytics` and `bob-artifacts` with any S3 client. With
the AWS CLI against a RustFS on `127.0.0.1:19000`:

```bash
export AWS_ACCESS_KEY_ID=bobadmin AWS_SECRET_ACCESS_KEY=bobadmin-secret-0123456789 AWS_REGION=us-east-1
for b in bob-control bob-fleet bob-analytics bob-artifacts; do
  aws --endpoint-url http://127.0.0.1:19000 s3 mb s3://$b
done
```

```text
make_bucket: bob-control
make_bucket: bob-fleet
make_bucket: bob-analytics
make_bucket: bob-artifacts
```

Credentials come from the standard AWS chain (environment, shared config file, instance
metadata, web identity); bob never creates credentials itself. For production, give the
orchestrators and the workers separate identities with the shipped policies, see
[S3 identities and policies](#s3-identities-and-policies).

## Start an orchestrator

```bash
export BOB_STORAGE_ENDPOINT=http://127.0.0.1:19000 BOB_STORAGE_REGION=us-east-1
export BOB_SERVER_NAME=orch-1 BOB_SERVER_LISTEN=127.0.0.1:18443
export BOB_SERVER_BASE_URL=http://127.0.0.1:18443
bob-server orchestrator
```

```text
time=… level=INFO msg="orchestrator started" mode=orchestrator name=orch-1 listen=127.0.0.1:18443 controllers=13
time=… level=INFO msg="controller leadership acquired" mode=orchestrator controller=registrar epoch=1 orchestrator=orch-1
…
time=… level=INFO msg="orchestrator joined fleet/cfg" mode=orchestrator orchestrator=orch-1 endpoint=http://127.0.0.1:18443
```

Check it: the admin listener answers health checks, the main listener serves the cache:

```text
$ curl -s 127.0.0.1:9090/healthz
ok
$ curl -s 127.0.0.1:18443/nix/nix-cache-info
StoreDir: /nix/store
WantMassQuery: 1
Priority: 30
```

`BOB_SERVER_BASE_URL` is the public URL users and forges see (links, OAuth redirects, the
OCI token realm). Put a TLS-terminating reverse proxy in front and set the base URL to its
`https://` address.

## Create the first admin

The first admin is created directly in the control bucket, with the orchestrator's
storage configuration. The password is read from stdin (12 to 256 bytes):

```bash
echo 'correct-horse-battery' | bob-server admin create-user --login admin --admin
```

```text
created user admin (u01m5px8h5qz5wwt15dwcvbe1dx) bound to admin@*
```

This is the only way to create the first admin. More users, roles and service accounts are
managed in the web UI or the API, see [Users and access](05-users-and-access.md).

## Start a worker

Workers need no registration step: a worker with fleet, analytics and artifacts access
registers itself, and an orchestrator approves it within seconds.

```bash
export BOB_STORAGE_ENDPOINT=http://127.0.0.1:19000 BOB_STORAGE_REGION=us-east-1
export BOB_SERVER_NAME=worker-1 BOB_ORCHESTRATOR_ADDRS=http://127.0.0.1:18443
export BOB_STATE_DIR=/var/lib/bob-worker BOB_WORKER_SLOTS=2
bob-server worker
```

```text
time=… level=INFO msg="worker started" mode=worker name=worker-1 slots=2 orchestrators=[http://127.0.0.1:18443]
time=… level=INFO msg="worker registered" mode=worker wid=wqg5i47ta7ktek5qdvodo5ym5b5 platform=aarch64-linux slots=2
```

A worker serves the jobs of its platform (`aarch64-linux` or `x86_64-linux`): its "class".
Run workers of both architectures to build for both natively; jobs of a class without
workers wait in the queue. `BOB_WORKER_SLOTS` is the number of concurrent jobs (default: half
the CPUs).

## Log in from the CLI

On your machine:

```bash
bob login --device http://127.0.0.1:18443
```

```text
Open http://127.0.0.1:18443/device and enter code TCSD-RHMF (expires in 10 min)
Waiting… logged in to 127.0.0.1:18443 as admin (token valid 60 minutes, refreshed automatically)
```

Open the URL in a browser, log in as `admin`, enter the code and allow the device. Without
`--device`, `bob login` opens a browser on the same machine and completes by itself. Then try
a remote build from any bob project:

```text
$ bob build --remote //cmd/hello:hello
remote build b-01M5PXB03VHB1K6D23P07ER9N7 queued on 127.0.0.1:18443
  http://127.0.0.1:18443/p/hello-ship/builds/b-01M5PXB03VHB1K6D23P07ER9N7
downloading 1 output
built   //cmd/hello:hello

bob: 1 built
```

The project (`hello-ship`, named after the repository directory) was created on the first
upload.

## S3 identities and policies

For production, use three S3 identities:

| Identity | Policy file | Used by |
|---|---|---|
| orchestrator | `deploy/policies/orchestrator.json` (all four buckets) | orchestrators, `bob-server admin`, `bob-server store fsck` |
| worker | `deploy/policies/worker.json` (no access to `bob-control`; writes only its fleet prefixes and `staging/`) | workers |
| bucket policy | `deploy/policies/control-bucket.json` on `bob-control` (denies everyone but the orchestrator principal) | the control bucket |

Apply them with your provider's tools (IAM on AWS; the admin API or console on RustFS). Then
set `BOB_STORAGE_WORKER_PROBE_CREDENTIAL=<access key>:<secret key>` of the worker identity on
the orchestrators: at start they check that this credential cannot read `bob-control` or write
`worker/` records, and refuse to start (exit 78) if it can.

> [!NOTE]
> The policy files are tested against RustFS by the harness (the S11 policy matrix). The
> setup on this page used one root credential for both roles, which is fine for a trial but
> gives workers access to the control bucket.

## Running as services

Run orchestrators and workers under your service manager. For workers, the unit must
delegate cgroups (`Delegate=yes` in systemd) and run as root. Give each process a stable
`BOB_SERVER_NAME` and, for workers, a persistent `BOB_STATE_DIR` so the Nix store survives
restarts.

## Next steps

- [Configuration reference](03-configuration.md).
- [Users and access](05-users-and-access.md).
- [Projects and forges](06-projects-and-forges.md).
