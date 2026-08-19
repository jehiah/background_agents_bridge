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
> needed (see *Reviewed ahead of the sync point*) — most recently the durable
> session diff viewer (`4df9a705` and its follow-ups) and managed skills
> (`97f6aeb8`/`b8c757b2`), both ahead of the sync point. Treat the hash
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

### Reviewed ahead of the sync point

These landed upstream **after** `5308371d` but were dispositioned out of order
(so the "in sync through" hash above is not yet bumped past them — the commits
between still need review):

| Upstream | Disposition |
| -------- | ----------- |
| `4df9a705` durable session diff viewer (#1036) + `5c351a8a` exclude runtime assets (#1044) + `d2d2a075`/`33db62d4`/`836fd351` collector refactors (#1048/#1049/#1051) | **Ported.** `internal/sessiondiff` is a port of `diff_collector.py` / `diff_capture.py` / `git_excludes.py` at upstream HEAD (all five commits folded together): the git-backed collector (baseline reachability check, raw/numstat parsing, untracked and index-delete overlay handling, submodule and mode metadata, binary/too-large/metadata-only render states, per-file and per-bundle byte ceilings with largest-patch-first shedding), the `git_excludes` filter for runtime-owned untracked assets, the coalescing `Worker` (generation bookkeeping, prompt idle gate, staleness discard, 404 → permanently unsupported, failure reporting), and the `Client` for `PUT /sessions/{id}/diff` + `POST .../diff/failure`. Wired into the bridge: `refresh_diff` command, prompt start/finish hooks, `repositories` on the ready event, and a 5 s bounded flush on shutdown. The install side of `git_excludes` (which writes the managed block) belongs to the un-ported boot orchestrator; baseline resolution is a port-specific addition (see the divergence note). |
| `97f6aeb8` managed skills (#1449) + `b8c757b2` managed-skills rollout cleanup (#1459) | **Ported.** `internal/skills` is a port of `managed_skills.py` at upstream HEAD (both commits folded together): fetch (bearer, session-scoped URL, 3 attempts, 15 s, 32 MiB cap), local re-validation of the installation DTO, discovery-root name-collision scan, and the journalled same-filesystem swap install with `0400`/`0500` modes. Wired into `bridge run-opencode` (see the divergence note below), which is this port's stand-in for the supervisor step `await self.managed_skills.materialize(...)`. |
| `01a77eda` extract `BufferedEventForwarder` from the bridge (#1058) | Excluded — a behavior-preserving Python refactor (send/buffer/flush/ack machinery moves from `bridge.py` into `event_forwarder.py`; log names, ack-id scheme, eviction policy and the 1000-event cap all unchanged). The Go port already has that seam in `internal/bridge/send.go`, covered by `send_test.go`. It stays a file rather than a type because the buffer shares `b.mu` with the connection — coder/websocket permits a single writer, so giving the forwarder its own lock would be a regression. |
| `d7955427` extract `OpenCodePromptStream` from the bridge (#1060) | Excluded — the same kind of behavior-preserving Python refactor: the 544-line `_stream_opencode_response_sse` and its helpers move into `prompt_stream.py`, the two nested closures become methods over a per-call `_PromptState`, and the thinking-budget / pending-part / default-title constants become module `Final`s. Nothing about the wire, the log names or the timeouts changes. The Go port never had the god-method — the same decomposition already exists as `internal/bridge/stream.go` plus `parts.go`, `attachments.go`, `opencode.go`, `identifier.go` and `prompt.go`, with per-prompt state in a function-local `streamState` and the cross-prompt session-title dedupe on the bridge (`lastForwardedSessionTitle`), matching upstream's instance-level scope. |
| `2914de18` extract `OpenCodeClient` from the prompt stream (#1062) | Excluded — the transport/translator split promised in the #1060 review, again with no wire or behavior change: `opencode_client.py` takes the base URL, `open_event_stream`, `post_prompt`, `request_stop`, `get_messages` and `parse_sse_stream`, `opencode_identifier.py` takes `OpenCodeIdentifier`, and the stream keeps the state machine, reconciliation, title dedupe, request-body construction and the prompt-lifecycle timeouts. The Go port already draws that boundary: `internal/bridge/opencode.go` (`opencodeGet`, `createOpencodeSession`, `opencodeSessionExists`, `requestOpencodeStop`, `listMessages`) is the transport, `identifier.go` is the standalone ID module, and `stream.go` is the translator, with the inactivity deadline stream-side as upstream now has it. Two seams sit differently by design: `postPrompt` stays in `stream.go`, and SSE framing is `tmaxmax/go-sse` so there is no `parse_sse_stream` to relocate. |
| `c37d1572` isolate the test suite from live runtime file paths (#1082) | Excluded — a `tests/conftest.py`-only fix for a hazard specific to the Python suite: three tests drive a real `SandboxSupervisor.run()`, which overwrote the live `/tmp/oi-repo-manifest.json` with a fixture repo and unlinked `/tmp/oi-boot-warnings.jsonl` when the suite ran inside a live sandbox, breaking push targeting and `create-pull-request` for that session (observed in production on 2026-07-20). The autouse fixture monkeypatches `REPO_MANIFEST_FILE_PATH`, `BOOT_WARNINGS_FILE_PATH` and `TUNNEL_ENV_FILE_PATH` to `tmp_path`; no runtime source changed. This port has no supervisor, so nothing here writes the manifest except `sessiondiff.ResolveBaselines`, which takes the path as an argument and is only ever tested against `t.TempDir()`. Both paths are injectable fields (`repoManifestPath`, `bootWarningsPath`) rather than module constants, and the one test that builds a real bridge (`observability_test.go`) overrides both plus `sessionIDFile` — upstream's stated uncovered follow-up. The remaining readers of `repomanifest.ManifestPath` only read it, and `toolgen_test.go` calls `defaultRepoDir()` on both sides of its assertion, so it is manifest-independent. There is no tunnel-env file in this port. |
| `cd377d60` delegated commit signing with user attribution (#1030) | Ported, in two commits. Attribution: `commandAuthor.gitIdentity` with `agent-only` / `attributed-user` modes (`promptGitAuthor` in `internal/bridge/git.go`), an unparseable identity failing the prompt with upstream's `Invalid prompt Git identity`. Signing: `internal/bridge/gitsigning.go` fetches `GET /sessions/{id}/commit-signing` before each prompt, validates the committer identity and the `ssh-ed25519` public key, and installs `author.*` / `committer.* `/ `gpg.format` / `gpg.ssh.program` / `user.signingkey` / `commit.gpgsign` (unsetting all of them again when signing is turned off mid-session); `internal/sandbox/gitsign.go` is `oi-git-sign`, which POSTs the unsigned commit buffer and writes back the SSHSIG armor. The private key never reaches the sandbox, the author stays the requesting user and the committer/signer is the deployment identity, as upstream. See the divergence notes for the global config, the legacy `scmName`/`scmEmail` fallback, the 404 tolerance and the shim. |
| `5caac824` keep the prompt stream alive during compaction (#1081) | **Ported.** A parent `session.error` whose `error.name` is `ContextOverflowError` is OpenCode announcing automatic compaction, not a failure: it is now swallowed and logged as `bridge.context_overflow_compacting`, and `session.compacted` clears it — the old code emitted an error and tore the stream down mid-recovery. Both idle paths call `emitUnrecoveredOverflow`, so an overflow whose promised compaction never came still fails the prompt (`bridge.context_overflow_unrecovered`) instead of reporting silent success. A child session's overflow is dropped outright. `message.updated` now surfaces `info.error` for messages belonging to the prompt (`bridge.message_error`), in order with the surrounding token and step events, and `parentErrorEventOnce` deduplicates the parent error across that path and `session.error` via `emittedErrorMessages`. Finally, the compaction summary is never assistant output on either the live path or the reconciliation pass — its `parentID` is the compaction user message, so `parentID` alone could not exclude it, and its text was leaking into the transcript; `correlatedCompactionSummaryIDs` keeps its *errors* in scope. All of it lands in `internal/bridge/stream.go`, which already had the matching seams; the only structural change is extracting `newStreamState` so the tests share the production constructor. |
| `3f8a6b7b` cascade cancellation to nested tasks (#1083) | **Ported** (sandbox-runtime scope). `cancel-task` gains a `cancelNested` boolean defaulting to `true`, sent as the `POST /children/{id}/cancel` body (the call previously had none), and the result's `cancelledDescendantIds` becomes an "Also cancelled N nested task(s)." clause. `controlplane.CancelChild` takes the flag and `CancelResult` carries the id list; `runCancelTask` defaults to cascading even when the arg is absent, which the zod schema normally fills but a direct `bridge tool cancel-task` invocation does not. The control-plane half — recursive descendant discovery, deepest-first cancellation, conflict tolerance, request validation — is behind the endpoint and out of scope. |
| `ce2be05f` discourage premature child task cancellation (#1086) | **Ported.** Prompt text only — no logic, wire or log change. `cancel-task` now tells the agent to cancel only on user request or when the work is clearly obsolete, and not because a task is slow, the parent is finished, or as cleanup; `get-task-status` and the `spawn-task` result string both say to check status only when the result is needed rather than poll; `spawn-task` adds that the child keeps running after the parent responds. The `cancel-task` text composes with the `cancelNested` sentence from `3f8a6b7b`, which upstream wrote it on top of. No test changes: the existing assertions match on the task id and the "Task spawned successfully" prefix. |
| `f791bc7c` authenticate gh in E2B sandboxes (#1093) | Excluded — not relevant to this port. The fix is for E2B/opencomputer images, where the sandbox runs non-root and so could never write the wrapper to `/usr/local/bin/gh`; the failure was swallowed by a debug-level `gh_wrapper.install_failed`. Upstream extracts the inline `GH_WRAPPER_BODY` string into a `gh-wrapper.sh` package data file so the image builders can bake the same bytes in, adds `GH_WRAPPER_INSTALL_PATH`, checks the real gh for executability rather than existence, rewrites a byte-identical-but-non-executable wrapper, and raises `RuntimeError("Cannot install authenticated gh wrapper at …")` instead of logging. This port already keeps the script as a separate `internal/sandbox/gh_wrapper.sh` embedded with `//go:embed`, and installs it into the first directory on `$PATH` rather than `/usr/local/bin` — precisely so a non-root sandbox can write it — so the bug cannot occur. The only transferable piece is the fail-loud posture; `installGHWrapper` still logs `install.gh_wrapper_error` and continues, which is deliberate here: unlike upstream's supervisor boot, it runs alongside the credential-helper and tool-file installs and a wrapper failure should not abort the bridge. |
| `5dd4a62c` cover sandbox signer preflight failures (#1079) | **Ported**, test-only. Both cases already existed in `gitsign_test.go` but were weaker than upstream's: they are now tightened to assert the signer refuses *before* the POST (an `httptest` server that counts requests) and leaves no `.sig` behind, and the session-config case gains upstream's malformed-`SESSION_CONFIG` table (not-json, array, blank/absent `sessionId`) alongside the no-credentials case. Here the JSON parse lives in `config.Resolve`, not the signer. |
| `ec02a9a6` per-service HMAC request signing (subject is literally `ColeMurray`; no PR number) | Excluded — dead code in the sandbox. Adds `auth/service_auth.py` plus tests and a vector generator: a Python mirror of `shared/src/service-auth.ts` implementing the `sig1.<timestampMs>.<nonce>.<signature>` scheme that replaces the shared `INTERNAL_CALLBACK_SECRET` bearer. Nothing in sandbox-runtime calls it (it is not even re-exported from `auth/__init__.py`) — it is staged for the control-plane-facing services. The sandbox still authenticates with `SANDBOX_AUTH_TOKEN` as a bearer, unchanged. Port it if a later commit makes the sandbox sign its requests. |

### Pending review (after `5308371d`, not yet ported)

| Upstream | Notes |
| -------- | ----- |
| everything between `5308371d` and HEAD not listed above | Next in line, starting after `ec02a9a6`. |
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
- **Session diff baselines are resolved by the bridge, not the supervisor.**
  Upstream carries `base_sha` on every `RepoEntry`, written by `entrypoint.py`
  when it boots the checkouts. This port has no boot orchestrator, so the
  baseline arrives through the VM instead: the control plane's
  `SESSION_CONFIG.repositories` is persisted verbatim to
  `/etc/oi/repositories.json` and passed through into `/tmp/oi-repo-manifest.json`
  (extra keys survive the jq transform), and `repomanifest.Entry` accepts either
  `base_sha` or `baseSha`, dropping anything that is not a full object name. When
  an entry still has no baseline, `bridge run-opencode` resolves the checkout's
  `HEAD` once at boot (`sessiondiff.ResolveBaselines`) and writes it back to both
  files, so a resumed session keeps the original baseline instead of re-anchoring
  to the agent's own commits. The startup script only passes the list along; all
  of the logic lives here.
- **The diff worker is one long-lived goroutine**, not a respawned asyncio task.
  Upstream creates a task per request and re-creates it if a request lands during
  task teardown; the Go worker is woken by a buffered channel, which removes that
  race by construction. Shutdown work runs on a context detached from the
  bridge's, so `Close`'s 5 s window can actually finish an in-flight upload.
- **Bundle JSON is not ASCII-escaped.** The Python encoder uses
  `ensure_ascii=True`; the Go encoder emits UTF-8 directly, so the bundle-size
  ceiling is measured against the bytes actually uploaded. Patch text is decoded
  from git with per-invalid-byte U+FFFD replacement, matching Python's
  `errors="replace"`, so the two ports agree on patch content and length for
  valid UTF-8.
- **Git identity is written with `git config --global`**, not `--local` per
  member checkout. Upstream loops over the manifest checkouts (#899, and again
  in `git_signing.py`), but the identity is session-wide, nothing in this
  sandbox writes a local `user.name`/`user.email` that could shadow the global
  value, and the global one also covers repositories the agent clones or creates
  outside the manifest. It matches how `install` already configures
  `credential.helper`. One write per key replaces two per checkout, and the
  "repository is unavailable for Git configuration" error path disappears.
- **An absent `author.gitIdentity` falls back to the legacy `scmName`/`scmEmail`
  pair.** Upstream #1030 made the explicit mode mandatory and raises on anything
  else. This port keeps accepting the pre-#1030 shape so it still works against
  a control plane that has not shipped the change; a `gitIdentity` that *is*
  present but unparseable fails the prompt exactly as upstream does.
- **A 404 from `/sessions/{id}/commit-signing` disables signing** instead of
  failing the prompt, on the same reasoning and matching how the diff worker
  already tolerates a control plane without the diff endpoint. Every *other*
  failure is fail-closed as upstream is: an unreachable endpoint, a non-404
  status, an unparseable body, a committer name/email or public key that fails
  validation, or signing enabled while `oi-git-sign` is missing from `$PATH` all
  fail the prompt rather than producing a commit signed by — or attributed to —
  an identity the bridge could not confirm.
- **The agent's own Git identity is configurable** through `openinspect.name`
  and `openinspect.email` in git config. Upstream hard-codes
  `OpenInspect <open-inspect@noreply.github.com>` in both runtimes; this port
  keeps that as the default but lets a sandbox image or repository setup script
  name the deployment's own bot, so agent-only commits are not attributed to
  Open-Inspect upstream in a fork. It applies wherever the constant did — the
  author of an agent-only commit when signing is off, and the filler for a
  legacy prompt that carries only one of `scmName`/`scmEmail` — and never
  overrides a control-plane committer identity, which stays authoritative when
  signing is on. Each field falls back independently, a blank value counts as
  unset, and the read is unscoped (`git config --get`), so a checkout-local
  value wins over the global one.
- **`gpg.ssh.program` points at an `oi-git-sign` shell shim**, not at the Go
  binary. Git execs the program directly (no shell, no extra argv), so a
  `bridge git-sign` subcommand cannot be named there; the shim is a two-line
  `exec` script written by `sandbox.Install` into the first `$PATH` directory,
  the same pattern as the existing `gh` wrapper, and it stands in for upstream's
  `bin/oi-git-sign`. The signer resolves its own endpoint through
  `config.Resolve` (env → `SESSION_CONFIG` → GCE metadata) rather than upstream's
  env-only read, because git spawns it several processes below the bridge.
- **`create-pull-request` description** still names `__BRIDGE_DEFAULT_REPO_DIR__`
  (the primary checkout), whereas upstream dropped the path from the description.
  Kept as a Go-port-specific hint; the `repo` arg overrides it for multi-repo
  sessions.
- **Sole-checkout resolution** keeps the Go port's existing `REPO_NAME`
  preference (see `findRepoDir`) rather than upstream's plain sorted-glob, so the
  push and PR tools stay pinned to the same tree OpenCode edits.
