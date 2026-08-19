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
| `2bbd7772` add Claude Opus 5 support (#1105) | **Ported**, with a divergence. Upstream adds `claude-opus-5` to the exact-id `ANTHROPIC_ADAPTIVE_THINKING_MODELS` allowlist; this port instead matches the `claude-opus-` / `claude-sonnet-` / `claude-fable-` family prefixes (`usesAdaptiveThinking` in `internal/bridge/parts.go`), so a new release gets adaptive thinking without a code change, and names the pre-adaptive members (Opus 4/4.1/4.5, Sonnet 4/4.5) as the exceptions. A trailing `-YYYYMMDD` snapshot is ignored. This also closes a pre-existing gap: `claude-fable-5` (upstream `21c29172`, before the sync point) was never in the port's list. Explicit `provider/model` overrides are unaffected. |
| `c9a91364` require explicit child session requests (#1199) + `982102b0` rename child session tools to child terminology (#1200) | **Ported together** (one line of #1199 is rewritten again by #1200). Gating: `spawn-child` is invoked only when the user's request explicitly asks for a "child session", never inferred or suggested — the old substantial/parallelizable heuristic is gone. Rename: `spawn-task`/`get-task-status`/`cancel-task` → `spawn-child`/`get-child-status`/`cancel-child`, arg `taskId` → `childId`, and every agent-facing string moves to child wording (`Child spawned successfully.`, `Child ID:`, `No child sessions found.`, `N child session(s):`, `Child: <id>`, `Also cancelled N nested child session(s).`). Old names are removed, not aliased, as upstream. `internal/sandbox/taskstatus.go` is now `childstatus.go`; endpoints and wire fields are unchanged. Upstream relies on an image rebuild to clear the old tool files; this port has no such boundary, so `installTools` now prunes generated tool files it no longer writes (`pruneRenamedTools`) — see the divergence note. |
| `8cd3a46c` durable image build finalization (#1204) | Excluded — the whole in-scope change is in `entrypoint.py`, the boot orchestrator this port does not implement (see the divergence note). It bounds image-build mode with a new `OI_IMAGE_BUILD_EXECUTION_TIMEOUT_SECONDS` budget, splits `run()` into `_run_repository_boot` (returning a new `RepositoryBootResult`) plus `_run_image_build_execution`, keeps the sandbox alive on `shutdown_event.wait()` so the control plane can snapshot it out of band, and hardens every clone/fetch/checkout/hook subprocess with `start_new_session=True` plus a kill-the-process-group-on-cancel wrapper. This port has no image-build mode, repository sync, or hook runner. The process-group idea is worth revisiting if a Go supervisor is ever written — `exec.CommandContext` signals only the direct child. |
| `1daf6253` remove the legacy Modal image builder (#1208) | Excluded — follows from the `ec02a9a6` exclusion. In sandbox-runtime it only re-keys two `service_auth` test vectors and one tampering case off the retired `modal` service identity; that module was never ported, and the rest of the commit deletes the Modal image builder and its control-plane/terraform wiring. |
| `35c50aef` pass reasoning settings to child sessions (#1226) | **Ported.** `spawn-child` gains an optional `reasoning` arg forwarded to the control plane as `reasoningEffort`; absent means the child inherits the parent's effort. Upstream forwards only when `!== undefined`, so `""` is a real value — the Go request carries it as `*string` (nil omits) via a new `argStrPtr` helper. Upstream's new node test of the tool shim does not transfer (tools execute in Go here); `TestRunSpawnChildReasoning` asserts the posted body for set/empty/absent instead. |
| `18f9b902` derive prompt timeout from sandbox lifetime (#1232) | **Ported**, minus the env plumbing. The flat `promptMaxDuration = 5400s` is now derived from a sandbox lifetime (7200s) less a snapshot reserve of `min(900s, lifetime*0.25)`, giving a 6300s prompt budget and a 900s cleanup budget. Upstream reads the lifetime from `SANDBOX_TIMEOUT_SECONDS`, which its providers set; nothing supplies that here, so `sandboxLifetime` is a constant fed to `snapshotReserve` — see the divergence note. The deadline also becomes absolute: it covers the SSE handshake and every event wait instead of being checked after each event, so a stream that opens and goes silent now trips it. Cleanup after a timeout (abort + final-state flush) is bounded by the reserve. Upstream's `aiter`/`anext` restructuring has no analogue — Go has no generator to cancel at a suspended `yield`. |
| `68341779` SuperGrok subscription support (#1236) | **Partially ported** — only the prompt-body change. `xai` reasoning effort goes out as a top-level `variant` field rather than under `model.options` (`buildPromptRequestBody`), so a Grok model selected upstream does not silently lose its effort here. The rest is out of scope: `entrypoint.py`'s managed-OAuth setup (marker env vars `OPENAI_OAUTH_MANAGED`/`XAI_OAUTH_MANAGED`, merged `auth.json` entries, plugin deployment) belongs to the un-ported supervisor, and `xai-auth-plugin.js` is an image-baked OpenCode plugin that brokers tokens from a new control-plane route. This port ships no plugins and has neither the markers nor the route. |
| `693defa9` Grok 4.5 model support (#1238) | Excluded — the only in-scope production change empties the `variants` map on the synthetic `grok-build-0.1` entry in `xai-auth-plugin.js` (Grok Build reasons internally but rejects an effort), and this port ships no plugins. `xai/grok-4.5` itself is a shared-catalog entry resolved from OpenCode's own xAI catalog, and the guard against sending a stale effort to a variant-less model lives in the control plane. The `variant` behavior upstream re-tests here is already ported (#1236); its golden case was retargeted from `grok-build-0.1` to `grok-4.5` to match. |
| `41a37e4e` Luna max reasoning support (#1239) | Excluded — nothing under `src/sandbox_runtime` changed. The one in-scope line bumps an `@opencode-ai/plugin` fixture string (1.17.18 → 1.18.11) in a test for the un-ported deps-staging step, riding along with a repo-wide OpenCode version bump. The feature is a shared-catalog entry (`openai/gpt-5.6-luna` gains the `max` effort); no runtime change is needed there or here, since the openai branch of `buildPromptRequestBody` forwards any effort verbatim. |
| `97580a25` Modal image-build lifecycle fixes (#1249) | Excluded — entirely `entrypoint.py` and a new `modal_image_build_start.py`, both belonging to the un-ported boot orchestrator (same reason as `ec02a9a6`/`8cd3a46c`). Modal image builds now run in the sandbox's main process and take their callback token off stdin (`--await-modal-image-build-token-stdin-v1`) rather than from the environment, signal handling moves to a shutdown event that cancels the build cleanly, and hook `output_tail` is redacted in build mode. This port has no image-build mode and no supervisor. |
| `142fff4e` Nest child activity under Task calls (#1252) | **Ported** — the runtime half. A new `childActivityCorrelator` (`internal/bridge/childactivity.go`) owns the Task-call ↔ child-session binding, so `tool_call` and `error` events from a sub-task carry `childSessionId` and `taskCallId`, and the parent's own `task` tool_call carries `childSessionId`. Child activity seen before the parent's task part is now held (bounded at 2000) instead of forwarded uncorrelated, and released when that part names its Task call; anything still held is flushed at every stream exit. The tool dedupe key gained the child session so a task part re-emitted once its child is known is not suppressed. The control-plane and web nesting UI is out of scope. |
| `b1d98cd6` Distinguish subtasks from child sessions (#1258) | **Ported** — the `spawn-child` tool description now names 'sub-agent'/'subagent'/'sub-task'/'subtask' as *not* requests for a child session (they mean in-process task delegation), and says "suggest using a child session" instead of the ambiguous "using one". Text only; a `toolgen` test pins the wording. |
| `ca95ed36` Fix folding for delayed child task events (#1259) | **Ported.** A child session's closed Task call is now remembered by id, so activity that trails a completed task nests under it instead of being flushed uncorrelated, and an already-bound message stays bound when the child session is resumed under a new task. Most of this arrived early: `childActivityCorrelator` was written against upstream's post-`ca95ed36` file, so the `142fff4e` row above covers the correlator half. What landed here is the consumer change — the child `session.error` branch calls `taskForActivity` rather than `activeTask` — plus the correlator and stream tests for a late error and a resumed task. |
| `d66615c0` Fix Task folding metadata extraction (#1260) | **Ported** — the child session id is read from the task part's tool *state* (`state.metadata.sessionId`), not the part itself, which is where OpenCode actually puts it. Without this the `task_metadata` discovery path never fired and no child was ever bound to a Task call. Upstream's tests also pin the sequencing this exposes: a foreground task publishes the metadata only on its *completed* state, after the child's own activity has streamed, so correlation always runs through the hold-then-release path from `142fff4e`. The port's tests were updated to the real shape. |
| `f1e4318c` Remove dead legacy callback-signing token generator (#1282) | Excluded — deletes `generate_internal_token` from `sandbox_runtime/auth/internal.py` (and its re-export) now that image-build callbacks sign with the newer scheme. The whole `auth/` package is un-ported: this bridge authenticates to the control plane with its own credential, has no `MODAL_API_SECRET`, and serves no inbound endpoints to verify tokens for. The rest of the commit is `packages/modal-infra`. |
| `fbf38818` Require completion fields at image-build callback boundary (#1284) | Excluded — image-build mode only, in the un-ported boot orchestrator (same reason as `97580a25`). `RepoImageBuildCallback.from_env` now raises `RepoImageCallbackMisconfigured` when only some `OI_REPO_IMAGE_*` variables are set, so a partial build aborts at boot instead of running with completion reporting off and wedging the control-plane row in `building`; `base_sha` also leaves `report_success` and `RepositoryBootResult`, derived control-plane side from `repository_shas`. This port has no image-build mode and no supervisor. |
| `20ca3db4` Draft mode policy for pull requests (#1285) | **Partially ported** — the draft policy itself is a control-plane/web feature (org and repo SCM settings, a new settings UI). The `draft` tool arg was already ported from `80f986bc`; what landed here is the delta over it. The description now tells the agent to set `draft` only on an explicit request and otherwise omit it, since passing `false` fights a policy that requires drafts. And the success message reports the created PR's `state` — "in draft mode" vs "now ready for review" — instead of claiming ready-for-review unconditionally, which is wrong whenever policy forced a draft. |
| `a22e3fc1` Consolidate image-build sandbox env assembly (#1287) | Excluded — image-build only (same family as `97580a25`/`fbf38818`). The control plane and the Modal provider had each grown their own copy of the `OI_REPO_IMAGE_*` callback environment; this puts both on one assembly point. The only in-scope path is a new `image_build_callback_env.json`, a fixture pinning the key names that, by its own description, only tests read. This port has no image-build mode and none of those variables. |
| `01173a3f` Show context compaction in session timeline (#1328) | **Ported** — a `context_compacted` event is emitted when the parent session compacts, so the timeline can explain the gap where the assistant's context was rebuilt instead of leaving it unaccounted for. Everything else is consumer-side (shared event schema, web timeline row, reducer). |
| `1ab63510` Add parent-to-child follow-up prompts (#1214) | **Ported** — a new `send-child-prompt` tool queues a follow-up in a direct child (`POST /children/{id}/prompt`), reporting the queued message id and distinguishing not-found / not-resumable / rate-limited from other failures. `get-child-status` now labels the final response as "Latest completed response (newer prompt queued or running)" when the control plane reports `hasUnfinishedPrompt`, so a stale answer is not read as the answer to the follow-up. Control-plane-side queueing and admission are out of scope. |

| `746a8594` Sandbox desktop streaming with VNC (#1332) | Excluded — the in-scope change is an opt-in Xvfb/fluxbox/x11vnc/websockify sidecar stack in `entrypoint.py` (plus its port/password constants and `test_vnc_supervisor.py`), which belongs to the un-ported boot orchestrator. The supervisor pops `VNC_PASSWORD` from the environment before spawning any child, writes it 3DES-obfuscated to a mode-0600 rfbauth file, sets `DISPLAY` so agent-launched GUI processes land on the shared display, and restarts the stack best-effort. Everything else is control plane, providers, and web UI. This port runs no sidecars. |

| `4ff60aca` Remove implementation-coupled coverage (#1335) | Excluded — a test-pruning sweep across the repo. In scope it deletes `test_setup_script.py`, `test_types.py`, most of `test_spawn_child_tool.py`, and part of `test_bridge_git_identity.py`, and drops the now-unused `FALLBACK_GIT_USER` compatibility alias from `bridge.py`. No behaviour changes, and this port has no equivalent alias. |

| `6fb5e7ad` Validate child session reasoning effort (#1369) | **Ported** — the `spawn-child` tool's `reasoning` description now names the accepted values and pins the `xhigh` spelling (not `x-high`), which the model was guessing wrong. Upstream's other half is the control-plane route rejecting an unrecognized `reasoningEffort` with a 400 instead of dropping it silently; that validation lives outside this port. |

| `e89fb209` Scope post-compaction message acceptance to the active prompt (#1385) | **Ported** — compaction rewrites the message chain, so parentID correlation stops matching and acceptance falls back to any non-summary assistant message. OpenCode reports the session's whole history over SSE and from the message-list API, so that fallback was claiming prior turns: their text replayed as current output, and the final-state reconciliation pass backfilled a stale answer over this prompt's real one. All three sites now go through `compactionFallbackAccepts`, which requires the message id to sort after this prompt's user message — OpenCode ids ascend by creation time, so that is exactly "created during this prompt". An error on a prior-turn message is scoped out the same way. Tests build ids with the real `identifier` encoding rather than ad-hoc strings, so the boundary cases (including the same-millisecond counter tick) are meaningful. |

| `b3491348` Extract OpenCode message attribution (#1389) | **Ported** — a refactor with no behaviour change. The per-prompt acceptance rules (user-message ids, allowed assistants, correlated compaction summaries, the compacted flag, and the post-prompt id comparison) move out of `streamState` into a `messageAttribution` type in `internal/bridge/attribution.go`. Its `assistantDisposition` folds the duplicated conditions at both call sites — the live stream and the final-state reconciliation pass — into one three-way answer: reject, error-only (a correlated summary, whose error is surfaced but whose text is not), or output. `idIsAfter` sits next to `identifier` so the ordering comparison reads as intent. Worth taking rather than skipping: `e89fb209` had to fix the same condition in three places. |

| `0e6ed9a9` Split sandbox runtime entrypoint (#1345) | Excluded — a pure decomposition of `entrypoint.py`, the boot orchestrator this port does not implement: 2,523 lines become an 83-line CLI plus new `agent_bridge_process`, `boot_warnings`, `browser_desktop`, `code_server`, `opencode_server`, `repository_bootstrap`, `runtime_config`, and `supervisor` modules, with the three collaborators injected into `SandboxSupervisor`. No behaviour change and nothing that touches the bridge or the tools. |

| `062dec26` Add Claude Sonnet 5, Grok 4.6, Kimi K3, GLM 5.3 (#1433) | **Ported** — no production change was needed. Upstream's only in-scope edit adds `claude-sonnet-5` to its exact-id adaptive-thinking allowlist; this port matches by family prefix (`claude-sonnet-`) with a shrinking denylist of pre-adaptive ids, a deliberate divergence documented at `internal/bridge/parts.go:15`, so Sonnet 5 already sends `thinking: adaptive` + `outputConfig.effort` and `usesAdaptiveThinking("claude-sonnet-5")` was already asserted. Grok 4.6 rides the existing `xai` top-level `variant` path unchanged. Added the two end-to-end prompt-body cases upstream added. The rest is the control-plane/web model catalog. |

| `2c3c1e69` Order compaction attribution by creation time (#1431) | **Ported** — the post-compaction fallback scoping added in `e89fb209` compared OpenCode message IDs, but those IDs encode a 48-bit truncation of their creation time, so they wrap roughly every 795 days and IDs minted after a rollover sort below every ID from the window before it. Attribution now orders by the message's own `time.created` against the wall-clock instant taken before the prompt was posted; a message with no usable timestamp is rejected rather than guessed at. Dropped `idIsAfter` and the ID-fixture test helpers, and rewrote the boundary tests around timestamps. |

| `07264052` Allow multiple pull requests per session, one per head branch (#1434) | **Ported** — the guard change itself is control-plane (`pull-request-service.ts`); the sandbox side is the contract the agent sees. `PRResult` now carries `headBranch`/`baseBranch`/`updated`, and `prSuccessMessage` reports the resolved pair (`PR #12 (feature-x -> main)`) and says "updated with your latest commits" when an existing open PR on that head was reused. The 409 hint drops the misleading "A PR may already exist for this branch." for the actionable new-branch instruction, and the tool description plus `baseBranch` describe state the rule (same branch updates, `git checkout -b` opens another, pass the previous head as `baseBranch` to stack). Not ported: the matching `repository_boot.py` prose, which is the multi-repo `AGENTS.md` written by the un-ported boot orchestrator. |

| `41bc4ca6` Extract supervisor process handlers (#1443) | Excluded — a behaviour-preserving decomposition of `supervisor.py`, the process manager this port does not implement: per-service restart handling moves into handler objects, with restart limits, backoff, error reporting and shutdown unchanged. Nothing touches the bridge or the tools. |

| `0c93a127` Clarify child session tool routing (#1484) | **Ported** — the `spawn-child` description now leads with the opt-in ("Use this tool ONLY when the user's current request explicitly and affirmatively asks…"), widens the do-not-match list to include 'sub agent' and Task tool requests, names the in-process Task tool as the alternative, and adds that mentioning, comparing or rejecting child sessions does not authorize one. Prose only; `TestSpawnChildDescriptionExcludesSubtasks` extended to the new wording. |

| `ddae7390` Remove unused Python sig1 implementation (#1482) | Excluded — deletes `auth/service_auth.py` with its tests and golden-vector generator, leaving the TypeScript implementation as the sole authority. This port never had a sig1 signer (nothing in `internal/` references it); the bridge authenticates to the control plane with its bearer token. Nothing to delete or replace. |

### Pending review (after `5308371d`, not yet ported)

| Upstream | Notes |
| -------- | ----- |
| everything between `5308371d` and HEAD not listed above | Next in line, starting after `ddae7390`. |
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

- **Renamed tool files are pruned on install.** Upstream removes a renamed
  tool's old `.js` by rebuilding the sandbox image (the `#1200` rename came with
  a `CACHE_BUSTER` bump); this port writes tool files at boot into
  `~/.config/opencode/tools/` and has no such boundary, so a resumed sandbox
  would keep advertising `spawn-task` while `bridge tool` only answers to
  `spawn-child`. `installTools` therefore deletes any `.js` in that directory
  that carries the generated banner and is not in the current set. Files without
  the banner are never touched.

- **Adaptive thinking matches model families, not an exact id list.** Upstream
  keeps `ANTHROPIC_ADAPTIVE_THINKING_MODELS` as a frozenset of exact ids and
  edits it per release; `usesAdaptiveThinking` (`internal/bridge/parts.go`)
  instead treats every `claude-opus-` / `claude-sonnet-` / `claude-fable-` id as
  adaptive, listing only the pre-adaptive members (Opus 4/4.1/4.5, Sonnet 4/4.5)
  as exceptions, and ignoring a trailing `-YYYYMMDD` snapshot. A model released
  after this code therefore gets adaptive thinking rather than a stale fixed
  budget, which is the right default for those families; the cost is that a
  future Claude family member that drops adaptive thinking needs an entry in
  `anthropicFixedThinkingModels`. Other families (`claude-haiku-*`) and
  providers are unaffected, as are explicit `provider/model` overrides.

- **The sandbox lifetime is a constant, not an environment variable.** Upstream's
  prompt budget is derived from `SANDBOX_TIMEOUT_SECONDS`, which each provider
  (E2B, Vercel, Modal) sets on the sandbox it creates. No such variable reaches
  the sandbox here, so `sandboxLifetime` in `internal/bridge/config.go` holds
  upstream's default (7200s) and the derivation is otherwise identical:
  `promptCleanupTimeout = snapshotReserve(sandboxLifetime)`,
  `promptMaxDuration = sandboxLifetime - promptCleanupTimeout`. A deployment that
  does learn its own lifetime should feed it to `snapshotReserve` rather than
  re-tune the two budgets by hand. Related: upstream's deadline also had to cover
  the prompt POST because that call was unbounded; here it already carries
  `opencodeRequestTimeout`, so the deadline only matters for the SSE handshake
  and the event waits.

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
