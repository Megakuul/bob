---
title: Projects and forges
section: Server
order: 6
summary: Projects, and connecting them to GitHub, GitLab or Forgejo repositories so pushes, pull requests and tags run pipelines.
---

A project is the unit of ownership on a bob server: builds, cache paths, images and releases
belong to a project, and roles are bound per project. This page shows how projects are
created and how to connect one to a forge repository.

## Projects

A project has an id (lower case letters, digits and `-`, starting with a letter) that appears
in image names (`<project>/<image>`) and download URLs (`/r/<project>/…`). There are two ways
to create one:

- `bob build --remote` creates it on the first upload, with the repository directory's name as
  id (override with `BOB_SERVER_PROJECT`);
- the web UI (Projects page) or `ProjectService/CreateProject`, which can also link a forge
  repository.

## Forge integrations

An integration holds the credentials for one forge installation. Create it in Settings →
Forges, or with `ForgeService/CreateForgeIntegration`:

| Forge | Credentials | Clone credentials for jobs |
|---|---|---|
| GitHub (`https://api.github.com`) | a GitHub App: app id, installation id, private key | an installation token limited to the repository, `contents: read`, 1 hour |
| Forgejo | a bot account login and password | a repository-scoped read token, revoked when the job ends |
| GitLab | a project or group access token with role Maintainer and scope `api` | a deploy token `read_repository` that expires with the job |

The answer contains the integration's webhook URL (`<base URL>/hooks/<integration id>`) and its
webhook secret, shown once. `TestForgeIntegration` checks the credentials; the integration's
health shows `AUTH_FAILED` when the forge rejects them.

## Link a repository

Create or update a project with the integration id and the repository's full name
(`acme/api`). bob looks the repository up, records its id and default branch, and makes sure
the webhook exists. One repository belongs to one project; events of repositories that no
project links are ignored.

## What happens on an event

1. The forge calls the webhook. bob checks the signature (GitHub `X-Hub-Signature-256`,
   Forgejo `X-Forgejo-Signature`, GitLab `webhook-signature` with a ±5 minute window), ignores
   duplicate deliveries, and answers within seconds.
2. A build is queued. Its first job evaluates `bob.go` at the commit and picks the pipelines
   whose triggers match (`bob.OnPush`, `bob.OnPullRequest`, `bob.OnTag`).
3. bob reports commit statuses: `bob/evaluate`, then one `bob/<pipeline>` per matched
   pipeline (pending, running, success, failure), linking to the build in the web UI.

Pull requests from forks are built with the fork's commit. A tag `v1.4.2` gives release
pipelines the version `1.4.2`.

Recent deliveries and their results are listed per integration
(`ListForgeDeliveries`), which helps when a push does not start a build. Rotate a webhook
secret with `RotateWebhookSecret`; the old secret stays valid for 24 hours.

> [!NOTE]
> The forge adapters are tested against recorded GitHub, Forgejo and GitLab deliveries and a
> live Forgejo in the harness. They were not connected to a forge for this documentation.

## Next steps

- [Pipelines](../04-guides/06-pipelines.md): declaring triggers.
- [CI and remote builds](07-ci-and-remote-builds.md).
