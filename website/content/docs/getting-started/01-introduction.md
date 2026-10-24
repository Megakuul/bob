---
title: Introduction
section: Getting started
order: 1
summary: What bob is and how the docs are organised.
placeholder: true
---

# Introduction

> [!NOTE]
> This is a placeholder page written by the website unit. The real docs replace it; any page
> without `placeholder: true` hides all placeholders.

bob is a typed Go framework that makes Nix an invisible build engine. You describe your
software in `bob.go` files; bob turns them into fine-grained, cached, hermetic builds.

## What you get

- One **typed** build description, checked by the Go compiler and visible to gopls.
- Hermetic builds without writing any Nix.
- A separate build and cache server that only needs S3.

## Where to go next

Read [Install](02-install.md) to build bob from source, then
[The build graph](../concepts/01-build-graph.md).
