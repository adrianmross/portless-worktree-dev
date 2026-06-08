# Repository Guidelines

## Project Scope

`portless-worktree-dev` is a standalone Go CLI that bridges Portless local preview routing with git worktree and WorkTrunk lifecycle conventions. Keep it usable without WorkTrunk, but design the primary integration path around WorkTrunk hooks such as `wt step tether`.

## Build and Test

- `go test ./...`: run all tests.
- `go build ./cmd/portless-worktree-dev`: verify the CLI builds.
- `go build -o ~/.local/bin/portless-worktree-dev ./cmd/portless-worktree-dev`: install locally for manual validation.

## Design Rules

- Keep Portless startup/readiness local to this CLI.
- Keep mesh or tailnet exposure out of this CLI; expose machine-readable state with `status --json` for transport-specific tools to consume.
- Prefer explicit error propagation with actionable remediation over silent fallback.
- Do not hide privileged Portless proxy setup inside automatic hooks; strict default-proxy mode should fail with repair commands.

## Git and Releases

Use conventional commits. If this repo gets published, prefer tagged releases and `go install github.com/adrianmross/portless-worktree-dev/cmd/portless-worktree-dev@latest` as the install path.
