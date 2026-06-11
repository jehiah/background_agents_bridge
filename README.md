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

## Status

Early work in progress — a faithful Go reimplementation of the upstream Python
bridge.

## Related

- [Background Agents / Open-Inspect](https://github.com/ColeMurray/background-agents/)
- [OpenCode](https://opencode.ai)

## License

[MIT](LICENSE) © Jehiah Czebotar
