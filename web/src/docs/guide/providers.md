---
title: Choose a provider
description: Connect a model provider and assign models to q's native roles.
sectionLabel: Guide
toc:
  - id: supported-providers
    label: Supported providers
  - id: configure-the-gateway
    label: Configure the Gateway
  - id: assign-model-roles
    label: Assign model roles
  - id: fallback-groups
    label: Fallback groups
---

## Supported providers

q's managed Gateway supports:

- OpenAI-compatible HTTP APIs, including local servers
- OpenRouter
- xAI and Grok
- Anthropic's native Messages API
- the local Codex App Server using the current Codex login

## Configure the Gateway

Open `/gateway` inside q to add providers and listener settings. An ordinary q session supervises its own managed Gateway child on an ephemeral loopback port.

Provider changes are activated by starting a replacement first. If the replacement cannot start, q keeps the currently running configuration instead of switching to a broken one.

Use environment-variable references for provider credentials whenever possible. Global provider definitions live in `~/.q/providers.json`.

## Assign model roles

Open `/model` to assign a model to the main chat and specialized roles such as `griller`, `scout`, `planner`, `executor`, `coder`, `commit`, `thinker`, and `librarian`.

Press `a` in the assignment table to create a reusable custom role. Custom subagents can select that role while keeping their own tools and delegation grants.

Workspace overrides are stored in `.q/model.json`; global assignments remain in `~/.q/config.yaml`.

## Fallback groups

A role may reference an ordered model group. q tries the next candidate after a timeout or transient HTTP 5xx response.

User cancellation, tool failure, and validation errors do not trigger fallback. Those outcomes need a changed request, tool state, or configuration rather than a different model endpoint.
