# Upstream Sync Status

This repository is a Go port of the Python **agent bridge** from the upstream
[background-agents](https://github.com/ColeMurray/background-agents/) project,
specifically the `packages/sandbox-runtime` package.

To keep the port in sync we track the upstream commit that this repository has
been reconciled against. When reviewing upstream changes, only the following
paths are relevant:

- `packages/sandbox-runtime/src/sandbox_runtime`
- `packages/sandbox-runtime/tests`

## In sync through

```
5308371d17089dfee46e3eb804d231072ae93983
feat(slack): attach generated media to completion threads (#1022)
2026-07-16
```

All upstream changes to the relevant paths **at or before** this commit have
been reflected in (or deliberately excluded from) this Go port.

> **This port is out of sync.** Roughly 56 upstream commits touch the relevant
> paths after `5308371d`, and features are being ported out of order as they are
> needed (see *Ported ahead of the sync point*) — most recently managed skills
> (`97f6aeb8`/`b8c757b2`), which is far ahead of the sync point. Treat the hash
> above as the last *exhaustive* review, not as the port's feature level. A
> follow-up pass will walk the intervening commits and re-sync the rest.

## Reviewed commits

Commits since the previous sync point (`7c0a3ab`), with disposition:

| Upstream | Disposition |
| -------- | ----------- |
| `dc678ed2` repo-less context cleanup (#859) | Folded in — push log reason is now `no_repo_configured` via the #899 refactor. |
| `daff292a` list-native repo sync, manifest, push targeting (#899) | **Ported.** Manifest-based push targeting, repo-identity validation/echo, always-`branchName` on `push_error`, boot-warnings drain, per-checkout git identity. Supervisor-side sync/hooks/manifest-writing are out of scope (entrypoint.py not ported). |
| `f7f1afc3` harden log forwarding / quiet cred warning (#907) | Excluded — supervisor log forwarder + `python -m` runpy warning; no equivalent surface in the Go port. |
| `681b7102` environment image builds (#917) | Excluded — modal image build / provenance, not ported. |
| `99aefc56` tag tunnel env file (#930) | Excluded — supervisor tunnel-file lifecycle, not ported. |
| `f97149c7` GPT-5.6 models (#937) | Excluded — model catalog in un-ported JS plugin; Go has no model allowlist. |
| `10087057` remove GPT-5.2 models (#939) | Excluded — same as above. |
| `4571bf83` failure callback URL to build worker (#956) | Excluded — image-build callback, not ported. |
| `7f4e058b` OpenCode 1.17.18 (#994) | Excluded — version pin / test fixture; Go pins the OpenCode version elsewhere. |
| `e012f3dc` accept nested-namespace repo owners (#991) | Ported via #1000 — nested owners handled in `resolveRepositoryTarget`. |
| `66d0bd3b` discourage unnecessary child sessions (#999) | **Ported** — `spawn-task` description updated. |
| `886e2585` nested repository identities end to end (#1000) | **Ported** (sandbox-runtime scope) — `create-pull-request` `repo` arg with last-`/` nested-owner parsing + manifest canonicalization. |
| `80f986bc` PR request draft mode setting | **Ported** — optional `draft` arg forwarded to `/pr` only when set. |
| `f20cdf11` preserve output when SSE stream drops (#1009) | **Ported** — `onStreamTransportError` in `internal/bridge/stream.go` flushes `fetchFinalMessageState` before failing, and the failure is now the stable `sseDisconnectError` message instead of the raw read error (logged as `bridge.sse_transport_error`). Applies to both the event-stream connect failure and a mid-stream drop. |
| `26a4c77c` terminal observability gaps (#1017) | **Ported** (sandbox-runtime scope) — connection aggregates (`markConnected`/`finalizeConnection`/`logDisconnect`) in `internal/bridge/bridge.go`, one `bridge.disconnect` per connection with `ws_close_code` and durations, `reconnect_attempt_count` on `bridge.reconnect`, counters on `bridge.connect`, the new `bridge.connect_error`, and the terminal `bridge.run_complete`. The image-build (`image_build.complete`), control-plane spawn/restore and start-callback halves of the commit are out of scope. |
| `5f5d54fb` portable session image attachments (#1019) | **Ported** — see `internal/bridge/attachments.go` (was ported ahead of the sync point). |
| `5308371d` slack: attach generated media to completion threads (#1022) | Excluded — sandbox-runtime scope is one prose file, `skills/upload-screenshot/SKILL.md` (broadened from screenshots to generated charts/diagrams). This port does not ship the bundled skill docs; `internal/skills` only scans the image's skills tree for name collisions. The tool itself (`image-upload`) is unchanged, and the rest of the commit is slack-bot / Cloudflare Queue work. |

### Ported ahead of the sync point

These landed upstream **after** `5308371d` but were ported out of order (so the
"in sync through" hash above is not yet bumped past them — the commits between
still need review):

| Upstream | Disposition |
| -------- | ----------- |
| `97f6aeb8` managed skills (#1449) + `b8c757b2` managed-skills rollout cleanup (#1459) | **Ported.** `internal/skills` is a port of `managed_skills.py` at upstream HEAD (both commits folded together): fetch (bearer, session-scoped URL, 3 attempts, 15 s, 32 MiB cap), local re-validation of the installation DTO, discovery-root name-collision scan, and the journalled same-filesystem swap install with `0400`/`0500` modes. Wired into `bridge run-opencode` (see the divergence note below), which is this port's stand-in for the supervisor step `await self.managed_skills.materialize(...)`. |

### Pending review (after `5308371d`, not yet ported)

| Upstream | Notes |
| -------- | ----- |
| `4df9a705` durable session diff viewer (#1036) | Next in line. |
| `4147972b` PR request draft mode setting | Off-branch re-commit of the draft feature already ported; no action. |

## Reviewing new changes

To list upstream commits touching the relevant paths since the synced commit:

```sh
git -C ../background-agents log --reverse --oneline \
  7c0a3abb393e26d52f5f0e8c843a307961ac0d16..HEAD -- \
  packages/sandbox-runtime/src/sandbox_runtime \
  packages/sandbox-runtime/tests
```

After reconciling a batch of commits, bump the **In sync through** hash above to
the newest reviewed commit and note any deliberate divergences below.

## Divergence notes

- **Supervisor (`entrypoint.py`) is not ported.** The Go port covers the bridge,
  git-credential helper, control-plane client, install, and native tools — not
  the boot orchestrator. Consequently the repo manifest (`/tmp/oi-repo-manifest.json`)
  and boot warnings (`/tmp/oi-boot-warnings.jsonl`) are *consumed* by the Go
  bridge (push targeting, `create-pull-request` repo resolution, warning drain)
  but nothing in this repo *writes* them yet. Multi-repo push targeting and the
  boot-warnings drain are therefore inert until a supervisor (Go or otherwise)
  produces those files. The consumers are wired and tested so they light up as
  soon as the files appear.
- **Managed skills run from `bridge run-opencode`**, not from a supervisor.
  Upstream composes the materializer in `entrypoint.build_supervisor` and calls
  `materialize(boot_result.repositories, boot_result.workdir)` after repository
  boot; the Go port has no boot orchestrator, so `run-opencode` — the only mode
  that owns the opencode process lifecycle — does it just before launching
  opencode, using the repo manifest for the repository list and its resolved
  `--workdir`. Two consequences: `connect-opencode` alone never materializes
  (whatever starts opencode there must have installed the tree already), and the
  collision scan sees only the checkouts the manifest lists, so it is as complete
  as the manifest is. Skip/fatal semantics match upstream: no
  `CONTROL_PLANE_URL`/session id means skip, any other failure aborts before
  opencode starts.
- **`bridge.run_complete` carries a `detail` field** for the `session_terminated`,
  `fatal_error` and `connection_error` outcomes. Upstream #1017 dropped the
  per-reason `bridge.disconnect` logs in the run loop, which left the terminating
  error's text unlogged anywhere for the first two outcomes; the Go port keeps it
  on the terminal summary rather than reintroducing a log line. Everything else
  about the event set matches upstream.
- **`create-pull-request` description** still names `__BRIDGE_DEFAULT_REPO_DIR__`
  (the primary checkout), whereas upstream dropped the path from the description.
  Kept as a Go-port-specific hint; the `repo` arg overrides it for multi-repo
  sessions.
- **Git identity** uses `git config --local` per member checkout (matching #899);
  the previous `--global` single-config behavior is replaced.
- **Sole-checkout resolution** keeps the Go port's existing `REPO_NAME`
  preference (see `findRepoDir`) rather than upstream's plain sorted-glob, so the
  push and PR tools stay pinned to the same tree OpenCode edits.
