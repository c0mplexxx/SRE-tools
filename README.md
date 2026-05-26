# SRE Tools

Public, anonymized SRE utilities: small operational bots, scripts, runbooks,
and experiments that are useful outside one private environment.

This repository is intentionally practical. Tools should be easy to inspect,
easy to run, and conservative by default. Public examples use neutral names and
placeholder endpoints so the repository can be shared without exposing private
infrastructure details.

## Contents

| Path | What lives there |
| --- | --- |
| `bot/` | `alert-list-bot`, a Go Telegram bot for compact Alertmanager and node_exporter operator views. |
| `NIC_tuning/` | Linux network interface tuning helpers and experiments. |

More tools may be added as separate top-level directories when they are useful
as standalone SRE building blocks.

## Repository Style

- Keep each tool self-contained, with its own README, config example, tests, and
  build or validation commands.
- Prefer plain defaults, explicit environment variables, and copy-pasteable
  commands over hidden local assumptions.
- Keep output operator-focused: short, deterministic, and useful during an
  incident or maintenance window.
- Avoid committing generated binaries, local state, host-specific env files, or
  personal operator notes.
- Keep public examples anonymized. Use placeholders such as `node-01`,
  `alerts-primary`, `vmselect.example.net`, `example.service`, and fake IDs.

## Security And Privacy

This is a public repository. Tracked files must not contain real tokens,
passwords, private keys, Telegram chat IDs, internal IP addresses, real
hostnames, private topology, customer names, or environment-specific runbook
details.

If sensitive material ever reaches public history, rotate the affected
credential first, then rewrite public history and tags with care.

## Versioning

Deployable tools use component-prefixed SemVer tags, for example:

```text
alert-list-bot/v1.1.1
```

Use:

- MAJOR for breaking command, config, API, or output contracts.
- MINOR for backward-compatible commands and features.
- PATCH for fixes, documentation, formatting, and packaging changes.

## Current Tools

### alert-list-bot

`bot/` contains a Go service that polls Telegram, reads Alertmanager state, and
renders compact operator responses for active alerts, silences, acknowledgements,
and node_exporter checks.

See [`bot/README.md`](bot/README.md) for command details and build steps.

### NIC tuning helpers

`NIC_tuning/` contains shell helpers for Linux IRQ and interface tuning
workflows. Scripts should stay readable, auditable, and safe to validate before
running on production hosts.
