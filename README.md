# portless-worktree-dev

Standalone Go CLI for worktree-aware Portless local development.

It bridges three layers:

- **Portless** owns local `.localhost` URL routing and readiness.
- **Git worktrees** provide branch/repo identity and shared state locations.
- **WorkTrunk** can orchestrate lifecycle through hooks, especially `wt step tether`.

The CLI remains usable outside WorkTrunk; WorkTrunk is the preferred lifecycle integration, not a hard dependency.

## Install

From a checkout:

```sh
go build -o ~/.local/bin/portless-worktree-dev ./cmd/portless-worktree-dev
```

After this repo is published:

```sh
go install github.com/adrianmross/portless-worktree-dev/cmd/portless-worktree-dev@latest
```

## WorkTrunk Example

```toml
[[pre-start]]
copy = "wt step copy-ignored --force"
install = "portless-worktree-dev install"

[post-start]
dev = "wt step tether -- portless-worktree-dev run"

[post-switch]
dev = "portless-worktree-dev start"

[post-remove]
dev = "portless-worktree-dev stop --branch '{{ branch }}'"

[aliases]
dev = "portless-worktree-dev start"
dev-url = "portless-worktree-dev url"
dev-status = "portless-worktree-dev status"
```

## Transport Integration

Transport-specific remote-preview tooling should consume machine-readable status
instead of parsing human output:

```sh
portless-worktree-dev status --json
```

The JSON includes app name, branch, repo root, state directory, Portless URL,
upstream target URL/port when Portless has an active route, PID state,
readiness, and dev command. `tunnelport` uses that as input before exposing an
already-running remote WorkTrunk preview over SSH.

Keep Tailscale-specific behavior out of this CLI. Reserve `tailport` for any
future Tailscale helper.

Use `chat-meshnet` for the Rhys-style coding-chat harness concept where each chat/session owns a VM or sandbox but feels like local `localhost:3000` development. Reserve bare `meshnet` for broader or future mesh concepts.

## Strict Default Proxy

By default, the CLI accepts whatever URL Portless resolves, including a fallback proxy port such as `:1355`. To enforce the no-port `.localhost` contract, set:

```sh
PORTLESS_REQUIRE_DEFAULT_PROXY=1 portless-worktree-dev url
```

When strict mode sees an explicit URL port, it fails with repair commands for starting or restarting the default HTTPS Portless proxy on port 443.

## Commands

```sh
portless-worktree-dev install
portless-worktree-dev run
portless-worktree-dev start
portless-worktree-dev status
portless-worktree-dev status --json
portless-worktree-dev url
portless-worktree-dev stop
portless-worktree-dev logs
```
