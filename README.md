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

## Responsibilities

The bridge handles:

- **WebSocket connection** to the control plane Durable Object, including
  reconnection.
- **Heartbeat loop** for connection health.
- **Event forwarding** from OpenCode to the control plane (tool calls, token
  streams, status updates).
- **Command handling** from the control plane (prompt, stop, snapshot).
- **Git identity configuration** per prompt author, so commits are attributed to
  the user who issued the prompt.

## Build & run

```sh
go build ./cmd/bridge

./bridge \
  --sandbox-id   "$SANDBOX_ID" \
  --session-id   "$SESSION_ID" \
  --control-plane "https://control-plane.example" \
  --token        "$AUTH_TOKEN" \
  --opencode-port 4096
```

Environment:

- `BRIDGE_SSE_INACTIVITY_TIMEOUT` — seconds of SSE silence before a prompt is
  aborted (default 120, clamped to [5, 3600]).
- `BRIDGE_LOG_LEVEL` — `debug` | `info` | `warn` | `error` (default `info`).

Logs are structured JSON (`log/slog`).

## Layout

```
cmd/bridge        entrypoint (flags, logging, signal handling)
internal/bridge   the bridge:
  bridge.go       struct, reconnect loop, shared state
  conn.go         WebSocket dial + read loop
  send.go         event send, buffering, pending-ACK tracking
  heartbeat.go    heartbeat + WebSocket ping
  command.go      inbound command dispatch
  prompt.go       prompt lifecycle
  stream.go       OpenCode SSE correlation state machine
  parts.go        part→event transforms, prompt request body
  opencode.go     OpenCode HTTP client
  session.go      session-id persistence
  git.go          git identity + push
  identifier.go   OpenCode-compatible ascending IDs
```

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

## License

[MIT](LICENSE) © Jehiah Czebotar
