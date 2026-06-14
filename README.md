# background_agents_bridge

A Go implementation of the **agent bridge** that connects
[OpenCode](https://opencode.ai) to
[Background Agents (Open-Inspect)](https://github.com/ColeMurray/background-agents/).

## Overview

Background Agents runs a coding agent (OpenCode) inside a sandbox and streams its
work back to a control plane. The bridge is the sandbox-side process that sits
between the agent and the control plane, providing bidirectional communication
between the two.

In the upstream project the bridge ships as a Python module
([`sandbox_runtime/bridge.py`](https://github.com/ColeMurray/background-agents/blob/main/packages/sandbox-runtime/src/sandbox_runtime/bridge.py)).
This repository is a Go port of that component.

```
┌────────────┐    ┌────────────┐    ┌────────────┐
│ Supervisor │───▶│  OpenCode  │───▶│   Bridge   │───────────▶ Control Plane
└────────────┘    └────────────┘    └────────────┘            (WebSocket)
```

## Scope

The bridge is **everything in the sandbox that talks to the control plane**. It
runs in one of several modes, selected by the first argument:

| Mode | Purpose |
| ---- | ------- |
| `bridge connect` | The long-running service: connect OpenCode to the control plane over a WebSocket. On startup it **self-installs** the two modes below. |
| `bridge git-credential <get\|store\|erase>` | A [git credential helper](https://git-scm.com/docs/gitcredentials#_custom_helpers): brokers a fresh SCM token from the control plane on each git op (no caching). |
| `bridge tool <name>` | Execute one OpenCode tool — reads JSON args on stdin, proxies to the control plane, writes the agent-facing result on stdout. |
| `bridge install` | Run the self-install steps only (credential helper + tool files). |

Invoking `bridge` with flags and no subcommand is treated as `connect` for
backwards compatibility.

### `connect`

- **WebSocket connection** to the control plane Durable Object, including
  reconnection, **heartbeat**, **event forwarding** (tool calls, token streams,
  status updates), and **command handling** (prompt, stop, snapshot).
- **Git identity configuration** per prompt author.
- **Self-install** on startup:
  - registers `bridge git-credential` as git's `credential.helper`
    (`git config --system credential.helper "bridge git-credential"`)
  - writes an OpenCode tool definition for each tool into
    `~/.config/opencode/tools/` — a thin `.js` shim that shells back into
    `bridge tool <name>`.

### Tools

`bridge tool <name>` and the generated shims cover: `create-pull-request`,
`spawn-task`, `get-task-status`, `cancel-task`, `slack-notify`, and
`image-upload`. The Go binary is the single source of truth for both the tool
definitions (name, description, args schema) and their execution.

## Build & run

```sh
go build ./cmd/bridge

./bridge connect \
  --sandbox-id          "$SANDBOX_ID" \
  --session-id          "$SESSION_ID" \
  --control-plane-url   "https://control-plane.example" \
  --sandbox-auth-token  "$SANDBOX_AUTH_TOKEN" \
  --opencode-port        4096
```

The short-lived modes (`git-credential`, `tool`) are spawned by git and OpenCode
rather than run by hand; they resolve their configuration from the inherited
environment (`CONTROL_PLANE_URL`, `SANDBOX_AUTH_TOKEN`, `SESSION_ID` /
`SESSION_CONFIG`) with a GCE metadata fallback.

Environment:

- `BRIDGE_SSE_INACTIVITY_TIMEOUT` — seconds of SSE silence before a prompt is
  aborted (default 120, clamped to [5, 3600]).
- `BRIDGE_LOG_LEVEL` — `debug` | `info` | `warn` | `error` (default `info`).
- `GCE_METADATA_HOST` — override the metadata host (see below).

Logs are structured JSON (`log/slog`).

## Configuration via GCE metadata

Any flag left empty falls back to a [Google Compute Engine instance
attribute](https://cloud.google.com/compute/docs/metadata/overview) (see the
table below for the attribute name each flag maps to). The bridge queries the
metadata server directly
(`http://metadata.google.internal/computeMetadata/v1/instance/attributes/<key>`
with the `Metadata-Flavor: Google` header) — no cloud SDK dependency.

| Flag                    | Metadata attribute    | Notes                         |
| ----------------------- | --------------------- | ----------------------------- |
| `--sandbox-id`          | `sandbox_id`          |                               |
| `--session-id`          | `session_id`          |                               |
| `--control-plane-url`   | `control_plane_url`   |                               |
| `--sandbox-auth-token`  | `sandbox_auth_token`  |                               |
| `--opencode-port`       | `opencode_port`       | falls back to `4096` if unset |

Metadata is queried only when a flag is missing. A flag passed on the command
line always wins, an absent attribute is treated as unset, and the probe fails
fast (and is skipped) when not running on GCE. Set `GCE_METADATA_HOST` (host or
`host:port`, no scheme) to point at a different metadata endpoint, e.g. for
local testing.

For example, to provision an instance:

```sh
gcloud compute instances create bridge-vm \
  --metadata sandbox_id=sb-123,session_id=ses-abc,control_plane_url=https://cp.example,sandbox_auth_token=...
```

## Layout

- `cmd/bridge` — subcommand dispatch (`connect` | `git-credential` | `tool` | `install`).
- `internal/config` — flag→env→GCE-metadata resolution shared by every mode.
- `internal/gcpmeta` — minimal GCE metadata client.
- `internal/controlplane` — the typed control-plane client: one method per
  endpoint, with named request/response structs and no exported HTTP transport
  types. The interface is the control-plane endpoint allowlist:

  ```go
  SCMCredentials(ctx, host) (Credentials, error)
  CreatePR(ctx, CreatePRRequest) (PRResult, error)
  SpawnChild(ctx, SpawnChildRequest) (SpawnChildResult, error)
  ListChildren(ctx) ([]ChildSummary, error)
  GetChild(ctx, childID, ChildDetailOptions) (ChildDetail, error)
  CancelChild(ctx, childID) (CancelResult, error)
  SlackNotify(ctx, SlackNotifyRequest) (SlackNotifyResult, error)
  UploadMedia(ctx, UploadMediaRequest) (MediaResult, error)
  ```

- `internal/sandbox` — sandbox-side glue: the credential helper, the
  `bridge tool` dispatch and agent-facing formatting, and the self-install of the
  credential helper and OpenCode tool files.
- `internal/bridge` — the `connect`-mode WebSocket bridge (reconnect loop,
  heartbeat, event forwarding, command handling, git identity + push).

## Design notes

- **Wire-compatible** with the Python bridge: event JSON, ack IDs, ascending
  message IDs, and OpenCode request bodies are byte-for-byte identical (locked by
  golden tests in `wire_test.go`).
- Idiomatic Go internals: `context` for cancellation/shutdown, goroutines for the
  heartbeat and the in-flight prompt (which survives WebSocket reconnects, as in
  the original).
- Dependencies: [`coder/websocket`](https://github.com/coder/websocket) for the
  control-plane connection and [`tmaxmax/go-sse`](https://github.com/tmaxmax/go-sse)
  for consuming OpenCode's event stream; everything else is the standard library.

## Status

Work in progress — a faithful Go reimplementation of the upstream Python bridge.
A TypeScript implementation (for Cloudflare sandboxes) also exists and was used
as a secondary reference, but the Python bridge is the source of truth.

## Related

- [Background Agents / Open-Inspect](https://github.com/ColeMurray/background-agents/)
- [OpenCode](https://opencode.ai)
