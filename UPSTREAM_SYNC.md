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
80f986bc34200313b971ef42f5c97fcd05d9ee23
feat: PR request draft mode setting
2026-07-14
```

All upstream changes to the relevant paths **at or before** this commit have
been reflected in (or deliberately excluded from) this Go port.

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

### Ported ahead of the sync point

These landed upstream **after** `80f986bc` but were ported out of order (so the
"in sync through" hash above is not yet bumped past them — the commits between
still need review):

| Upstream | Disposition |
| -------- | ----------- |
| `5f5d54fb` portable session image attachments (#1019) | **Ported.** `internal/bridge/attachments.go` parses `cmd.attachments`, hydrates images from `GET /sessions/{id}/attachments/{id}` (bearer auth, ≤6/msg, ≤10 MiB, no-redirect, concurrency 2) and appends OpenCode `file` parts after the text part; invalid/failed items surface a `warning`/`media` event. |

### Pending review (after `80f986bc`, not yet ported)

| Upstream | Notes |
| -------- | ----- |
| `f20cdf11` preserve output when SSE stream drops (#1009) | Bridge SSE handling — likely relevant to `internal/bridge/stream.go`; **review**. |
| `26a4c77c` fix terminal observability gaps (#1017) | Check for bridge-relevant logging changes. |
| `5308371d` slack: attach generated media to completion threads (#1022) | Control-plane / slack-bot outbound media — not the sandbox bridge path; likely **excluded**. |
| `4147972b` PR request draft mode setting | Same draft feature already ported (branch re-commit); no action. |

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
- **`create-pull-request` description** still names `__BRIDGE_DEFAULT_REPO_DIR__`
  (the primary checkout), whereas upstream dropped the path from the description.
  Kept as a Go-port-specific hint; the `repo` arg overrides it for multi-repo
  sessions.
- **Git identity** uses `git config --local` per member checkout (matching #899);
  the previous `--global` single-config behavior is replaced.
- **Sole-checkout resolution** keeps the Go port's existing `REPO_NAME`
  preference (see `findRepoDir`) rather than upstream's plain sorted-glob, so the
  push and PR tools stay pinned to the same tree OpenCode edits.
