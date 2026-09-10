# OpenHands Runtime/Sandbox Spike: Official Sources

Date: 2026-08-05 (Asia/Shanghai)  
Retrieval window: 2026-08-05 04:13-04:29 CST (2026-08-04 20:13-20:29 UTC)  
Scope: OpenHands Software Agent SDK, Agent Server, and Docker/API Remote Workspace only  
Evidence policy: official OpenHands docs, `OpenHands/software-agent-sdk`, PyPI, and GHCR metadata only

This note is source research for the bounded Chora spike. It does not report a
runtime experiment and cannot by itself pass Chora's Interface, Isolation,
Lifecycle, or Cost gates.

## Evidence labels

- **Contract**: a statement in the official product documentation or generated
  API reference. It is the strongest public interface statement found, but is
  still subject to the repository's MIT warranty disclaimer.
- **Example**: official sample code. It demonstrates intended use; it is not a
  security or lifecycle guarantee.
- **Source fact**: behavior present in the pinned `v1.40.0` source at commit
  `2f27653959f7596769427ee4657247b32c94504e`.
- **Metadata fact**: PyPI, GitHub release/tag, or OCI registry metadata observed
  during the retrieval window.
- **Spike implication**: an inference or a test requirement for Chora. It is not
  an OpenHands claim.

## 1. Reproducible baseline

### 1.1 Package, tag, commit, and license

This spike's runtime baseline is the official
[`OpenHands/software-agent-sdk`](https://github.com/OpenHands/software-agent-sdk)
monorepo. It is distinct from the official
[`OpenHands/OpenHands`](https://github.com/OpenHands/OpenHands) application/UI
repository; version or license statements below apply only to the pinned runtime
components unless stated otherwise.

| Component | Exact package/version | Source pin | Python | License evidence |
|---|---|---|---|---|
| SDK | [`openhands-sdk==1.40.0`](https://pypi.org/project/openhands-sdk/1.40.0/) | [`v1.40.0`](https://github.com/OpenHands/software-agent-sdk/releases/tag/v1.40.0) -> [`2f27653959f7596769427ee4657247b32c94504e`](https://github.com/OpenHands/software-agent-sdk/commit/2f27653959f7596769427ee4657247b32c94504e) | `>=3.12` | Repository [`LICENSE`](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/LICENSE) is MIT |
| Agent Server | [`openhands-agent-server==1.40.0`](https://pypi.org/project/openhands-agent-server/1.40.0/) | same tag/commit | `>=3.12` | same repository MIT license |
| Docker/API Remote Workspace | [`openhands-workspace==1.40.0`](https://pypi.org/project/openhands-workspace/1.40.0/) | same tag/commit | `>=3.12` | same repository MIT license |

The three component `pyproject.toml` files declare the same version and source:
[SDK](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-sdk/pyproject.toml#L1-L28),
[Agent Server](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/pyproject.toml#L1-L29), and
[Workspace](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/pyproject.toml#L1-L15).

License qualification:

- **Metadata fact:** PyPI JSON for all three packages returned `license: null`,
  `license_expression: null`, and no license classifier. Their 1.40.0 source
  distributions' `PKG-INFO` also had no `License` field, and the source
  distributions did not list a `LICENSE` file.
- **Source fact:** the pinned repository root contains the MIT license, copyright
  2026 OpenHands contributors.
- **Spike implication:** record MIT as the source-repository license, but do not
  represent the complete container filesystem as MIT-only. The image includes
  upstream operating-system and toolchain components with their own licenses.
  The inspected Python image config has only an OCI `authors` label; it does not
  carry OCI `licenses`, `source`, or `revision` labels.

### 1.2 PyPI artifact hashes

| Artifact | SHA-256 |
|---|---|
| `openhands_sdk-1.40.0-py3-none-any.whl` | `1c252c27ea5a2d8310b3b3c4a1a119e1d9a8c764f7cee527e43cbcc90a53dbd8` |
| `openhands_sdk-1.40.0.tar.gz` | `5413dac766759ff48159a51fb6b50ab4ad3ce9bc77ce29c4d820adcdc66d815c` |
| `openhands_agent_server-1.40.0-py3-none-any.whl` | `8d81d66d8f338f0ffe0efa3f8830f0283ee829d892f49bf2388bbd13aec5503d` |
| `openhands_agent_server-1.40.0.tar.gz` | `42a0adb1de63135978e9b485d488d91082d9cf2d6e068457c13f770b676400f8` |
| `openhands_workspace-1.40.0-py3-none-any.whl` | `9e995262ac00a71b762f1fd69d9ce27cae37991e3be7a9c24cedba945e9d755c` |
| `openhands_workspace-1.40.0.tar.gz` | `ec60e42a77fa418247367259028e54c4ebe0146485f707b8175c4b361e952e9c` |

The direct authoritative machine endpoints are:

- <https://pypi.org/pypi/openhands-sdk/1.40.0/json>
- <https://pypi.org/pypi/openhands-agent-server/1.40.0/json>
- <https://pypi.org/pypi/openhands-workspace/1.40.0/json>

### 1.3 Official image and immutable digests

The pinned workflow publishes to `ghcr.io/openhands/agent-server`, builds
Python, Java, and Golang variants for `linux/amd64` and `linux/arm64`, and merges
multi-architecture manifests. See the official
[`server.yml`](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/.github/workflows/server.yml#L236-L368)
and its manifest merge step
([lines 423-506](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/.github/workflows/server.yml#L423-L506)).

| Official versioned tag | OCI index digest | amd64 image manifest | arm64 image manifest |
|---|---|---|---|
| `ghcr.io/openhands/agent-server:1.40.0-python` | `sha256:d71ee9b6d202205aebdcf0c6b0b4a42b40766b787223a86270ec20d3bcf6bfe7` | `sha256:b2326ac6d444f3f80f2fa3260ab21653a9b6dfd02d5331643921428d79b87cc6` | `sha256:997ab654dc39b9fd1faa0f1d007009dc032d2341eb333399eed222bdddc349e1` |
| `ghcr.io/openhands/agent-server:1.40.0-java` | `sha256:bc1a0265abbb164c3f90edfa71f4d3d320a04389c2fb1e83f8c74e0891eb2bd8` | `sha256:256efa83f3f13c0d7dc7c7a976d03938ff99c7b0815ad32240f6ca1a4c8641e6` | `sha256:bc86111784cc59dc996fa1893d7c72841d93ee40803927d8634f534dbe1de30b` |
| `ghcr.io/openhands/agent-server:1.40.0-golang` | `sha256:64874b8a99d5a2bde348afac067af82c2949a054383ccf644dd1311d192afa90` | `sha256:b0e265305dca79d3684064ae8e85febd395c64c6c481b5cfbd382ed72e2927e1` | `sha256:950929678ddeea3749f30ea7dacaa46f30762f0d020c107cfb40fe7d9693eed0` |

The Python amd64 config blob is
`sha256:bbe53552d1f6ab003368d19ac180cb70d154586ff035fc8c3f439372e69389b9`.
Registry metadata reports:

- `OPENHANDS_BUILD_GIT_SHA=2f27653959f7596769427ee4657247b32c94504e`
- `OPENHANDS_BUILD_GIT_REF=refs/tags/v1.40.0`
- entrypoint `tini -- /usr/local/bin/openhands-agent-server`
- created `2026-08-01T02:45:16.136081407Z`

The same OCI manifests contain 23 compressed layers totaling
1,598,437,406 bytes (1,524.39 MiB) for amd64 and 1,545,626,290 bytes
(1,474.02 MiB) for arm64. These are registry transfer-layer totals, not pulled
image size or runtime writable-disk usage; the Cost Gate must measure those
separately.

Therefore the baseline image reference for the spike is:

```text
ghcr.io/openhands/agent-server@sha256:d71ee9b6d202205aebdcf0c6b0b4a42b40766b787223a86270ec20d3bcf6bfe7
```

`latest-python` and `main-python` are intentionally excluded. At query time
they both resolved to the different, floating digest
`sha256:a7e74934a3b65457903dccb04c7664c3967e6568a28d072cd5653c4dce77f4b2`.
Official examples use floating tags, so they must be rewritten for a
reproducible Chora spike.

## 2. Official capability record

### 2.1 Sandbox and workspace

**Contract**

- The Workspace abstraction exposes command execution, file upload/download,
  git changes/diff, context-manager resource handling, and local versus remote
  implementations. `CommandResult` contains `command`, `stdout`, `stderr`,
  `exit_code`, and `timeout_occurred`; `FileOperationResult` contains source,
  destination, success, error, and size. See the official
  [Workspace API reference](https://docs.openhands.dev/sdk/api-reference/openhands.sdk.workspace)
  and [architecture page](https://docs.openhands.dev/sdk/arch/workspace).
- The Docker guide describes `DockerWorkspace` as an isolated Docker agent
  server and says the context manager pulls/builds the image, starts it, waits
  for readiness, and cleans up the container. See
  [Docker Sandbox](https://docs.openhands.dev/sdk/guides/agent-server/docker-sandbox).
- A remote workspace causes the `Conversation` factory to select
  `RemoteConversation`, using HTTP and WebSocket rather than in-process calls.
  See [Conversation architecture](https://docs.openhands.dev/sdk/arch/conversation).

**Example**

- The official Docker example uses `DockerWorkspace(...latest-python...)`, runs
  a command, creates a remote conversation, receives callbacks, calls `run()`,
  and closes the conversation. It demonstrates intended flow, but its floating
  tag is not acceptable for the spike.

**Source facts at 1.40.0**

- `DockerWorkspace` defaults to working directory `/workspace`, image
  `latest-python`, optional host volumes, optional extra VS Code/VNC ports,
  platform selection, optional GPU, optional image deletion, a Docker network
  name, and a 120-second health timeout. See
  [`docker/workspace.py` lines 49-121](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/openhands/workspace/docker/workspace.py#L49-L121).
- It invokes `docker run -d --platform ... --rm`, publishes container port 8000,
  adds explicitly requested volumes and network, starts the server on
  `0.0.0.0:8000`, then waits for health. See
  [`docker/workspace.py` lines 160-262](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/openhands/workspace/docker/workspace.py#L160-L262).
- Context exit calls `cleanup()`, which runs `docker stop`; `--rm` removes the
  container after it stops. `cleanup_image` optionally removes the local image.
  See
  [`docker/workspace.py` lines 325-360](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/openhands/workspace/docker/workspace.py#L325-L360).

**Spike implications**

- “Isolated” is an official architectural description, not proof of the exact
  host-read, mount-write, network, secret, or process-escape boundary.
- `volumes` explicitly grants host access. Attempt-only isolation must therefore
  use a newly created attempt directory as the only project mount and verify the
  effective Docker inspect output and actual reads/writes.
- OpenHands does not create an attempt-only host directory or enforce a
  one-attempt/one-workspace policy; `volumes` defaults empty and all host mounts
  are caller choices.
- The official image runs as user `openhands`, but its Dockerfile grants that
  user passwordless sudo. This is root capability inside the container, so it
  must not be described as strong in-container least privilege. See
  [`Dockerfile` lines 142-151](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/docker/Dockerfile#L142-L151).
- Constructor/context cleanup is not proof of crash recovery. A killed client
  may leave a still-running `--rm` container; a bounded external cleanup/reaper
  check is required.

### 2.2 Network

**Contract/source facts**

- `DockerWorkspace.network` is a single optional Docker network name. When set,
  source adds `--network <name>`; when unset, OpenHands adds no explicit network
  flag. It always publishes the Agent Server port and optionally VS Code/VNC
  ports. Source:
  [`docker/workspace.py` lines 91-121](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/openhands/workspace/docker/workspace.py#L91-L121)
  and
  [lines 197-240](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/openhands/workspace/docker/workspace.py#L197-L240).
- Because the value is passed through as a Docker CLI string, `network="none"`
  is expressible and becomes `docker run --network none`. OpenHands does not
  validate that policy or add a higher-level NetworkPolicy/egress contract.
- `APIRemoteWorkspace` exposes runtime URL/key, image, pull policy, session ID,
  resource factor, runtime class, timeouts, retention controls, and selected
  forwarded environment variables. It has no SDK field for an egress allowlist
  or denylist. See
  [`remote_api/workspace.py` lines 18-82](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/openhands/workspace/remote_api/workspace.py#L18-L82).
- The official API Sandbox guide explicitly says `runtime.all-hands.dev` is
  primarily designed for benchmark evaluation at scale, not production
  applications, and directs production SDK applications to OpenHands Cloud.
  See [API-based Sandbox](https://docs.openhands.dev/sdk/guides/agent-server/api-sandbox).

**What is not an official guarantee**

- No reviewed SDK source or documentation promises default network-off behavior,
  domain allowlisting, DNS filtering, or per-attempt egress accounting for these
  workspace classes.
- The Docker guide's isolation wording must not be interpreted as a network
  isolation guarantee.

**Spike implication**

- Verify three cases from inspect plus actual traffic: default, an explicitly
  disabled network (for example a caller-supplied Docker network mode if
  accepted), and any proposed restricted network. If the SDK cannot express the
  needed policy without out-of-band Docker setup, record that as adapter or
  deployment responsibility.

### 2.3 Secrets and API authentication

**Contract**

- `conversation.update_secrets()` accepts static values or dynamic secret
  sources. The official guide says referenced values are injected as environment
  variables and masked in command output. See
  [Secret Registry](https://docs.openhands.dev/sdk/guides/secrets).
- Agent Server starts without API authentication by default. Official docs say
  to configure `OH_SESSION_API_KEYS_*`, send `X-Session-API-Key`, keep
  `OH_SECRET_KEY` stable to decrypt persisted sensitive values, and never expose
  an unauthenticated server publicly. See
  [Agent Server security](https://docs.openhands.dev/sdk/arch/agent-server#secure-the-server).

**Source facts at 1.40.0**

- `SecretRegistry` finds a registered secret name by case-insensitive substring
  search in a command, resolves the value, injects it as an environment variable,
  and tracks the value. Output masking is literal string replacement with
  `<secret-hidden>`. See
  [`secret_registry.py` lines 21-106](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-sdk/openhands/sdk/conversation/secret_registry.py#L21-L106)
  and
  [lines 143-179](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-sdk/openhands/sdk/conversation/secret_registry.py#L143-L179).
- For opaque consumers such as ACP subprocesses, `get_all_secrets_as_env_vars()`
  can inject the whole registry. The source explicitly says least-privilege
  scoping is deferred. See
  [`secret_registry.py` lines 108-141](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-sdk/openhands/sdk/conversation/secret_registry.py#L108-L141).
- `DockerWorkspace` forwards only `DEBUG`, `SESSION_API_KEY`, and
  `OH_SESSION_API_KEYS_0` by default; `APIRemoteWorkspace` defaults its
  `forward_env` list to empty. This is not whole-host-environment forwarding,
  but callers can broaden either list. See
  [`docker/workspace.py` lines 83-90](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/openhands/workspace/docker/workspace.py#L83-L90)
  and
  [`remote_api/workspace.py` lines 79-82](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/openhands/workspace/remote_api/workspace.py#L79-L82).
- The Agent Server applies session-key authentication to `/api/*`, separately
  handles workspace-file cookies, and mounts WebSocket routes. See
  [`api.py` lines 373-430](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/api.py#L373-L430).
- The settings API's documented source trust model treats all clients sharing a
  session key as equally trusted: there is no role-based authorization for
  `X-Expose-Secrets: plaintext`, and a single-secret GET returns raw text. See
  [`settings_router.py` lines 108-138](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/settings_router.py#L108-L138)
  and
  [lines 411-441](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/settings_router.py#L411-L441).
- `Config.session_api_keys=[]` explicitly means unsecured. See
  [`config.py` lines 201-211](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/config.py#L201-L211).
- If no cipher is configured (normally through a stable `OH_SECRET_KEY`), the
  persisted conversation state replaces secret values with redacted placeholders
  and cannot restore them as usable credentials. See
  [`state.py` lines 420-437](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-sdk/openhands/sdk/conversation/state.py#L420-L437).

**Spike implications**

- Masking is not a proof that a secret never entered the workspace or left over
  the network. It is best-effort transformation of known resolved values.
- Run with a unique sentinel host secret that is not forwarded or registered,
  inspect container environment/mounts, search workspace/output/events, and test
  controlled exfiltration. Never use a real credential.
- Set an explicit session API key even on loopback during the spike; default
  unauthenticated behavior is an unsafe production baseline.
- A session key provides coarse trust-domain authentication, not per-client or
  per-secret authorization. Network access to the Agent Server must therefore be
  restricted independently.

### 2.4 Conversation and workspace lifecycle

**Contract/source facts**

- Conversation statuses are `idle`, `running`, `paused`,
  `waiting_for_confirmation`, `finished`, `error`, `stuck`, and `deleting`.
  Only `finished`, `error`, and `stuck` are terminal according to
  `is_terminal()`. See
  [`state.py` lines 48-79](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-sdk/openhands/sdk/conversation/state.py#L48-L79).
- `pause()` is cooperative between agent steps; the API reference warns that an
  in-flight LLM completion finishes first. `interrupt()` cancels the in-flight
  async request and moves the conversation to paused. The server exposes
  create/get/search/count, run, pause, interrupt, delete, secrets, and final
  response routes. See
  [`conversation_router.py` lines 187-290](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/conversation_router.py#L187-L290).
- `RemoteConversation.run(blocking=True, timeout=3600)` triggers the server and
  waits for authoritative WebSocket completion with REST fallback. Its return
  type is `None`. On client-side timeout it raises `ConversationRunError` and
  explicitly warns that the conversation may still be running on the server.
  See
  [`remote_conversation.py` lines 1136-1206](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-sdk/openhands/sdk/conversation/impl/remote_conversation.py#L1136-L1206)
  and
  [lines 1232-1241](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-sdk/openhands/sdk/conversation/impl/remote_conversation.py#L1232-L1241).
- `DockerWorkspace.pause/resume` call `docker pause/unpause`; context exit stops
  the container. See
  [`docker/workspace.py` lines 361-394](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/openhands/workspace/docker/workspace.py#L361-L394).
- `APIRemoteWorkspace` can reconnect by session ID, start/resume/pause a runtime,
  and on cleanup either keep it alive, pause it, or stop it. See
  [`remote_api/workspace.py` lines 146-267](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/openhands/workspace/remote_api/workspace.py#L146-L267)
  and
  [lines 360-395](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-workspace/openhands/workspace/remote_api/workspace.py#L360-L395).
- Agent Server conversation deletion closes the event service and removes only
  the conversation directory; source explicitly preserves the workspace. See
  [`conversation_service.py` lines 1526-1578](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/conversation_service.py#L1526-L1578).
- API remote cleanup catches stop/pause errors, logs a warning, and still clears
  its local runtime ID/URL/key. A returned cleanup call is therefore not proof
  that the remote resource was reclaimed.

**Spike implications**

- Treat timeout and cancel as different paths: `run(timeout=...)` does not prove
  server cancellation; call `interrupt()` and then verify status/resources.
- Verify cleanup after normal completion, interrupt, timeout, and force-killed
  client. No reviewed source establishes a DockerWorkspace TTL or independent
  orphan reaper.
- Deleting a conversation is not workspace cleanup. The spike must verify both
  records/resources independently.
- Conversation close and workspace cleanup are separate ownership boundaries;
  the caller must prove both happened.

### 2.5 Events

**Contract**

- The event system is documented as typed, immutable, and append-only. It
  includes message, action/tool-call, observation/tool-result, rejection,
  agent-error, system-prompt, condensation, state-update, and pause events. See
  [Events architecture](https://docs.openhands.dev/sdk/arch/events).
- A remote conversation streams real-time events by WebSocket and can also query
  event history over REST.

**Source facts at 1.40.0**

- Base `Event` is a frozen Pydantic model with `id`, `timestamp`, `source`, and
  optional `parent_id`. See
  [`event/base.py` lines 20-40](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-sdk/openhands/sdk/event/base.py#L20-L40).
- REST event endpoints support filtered search, count, get, batch get, message
  send, and confirmation response under
  `/api/conversations/{conversation_id}/events`. See
  [`event_router.py`](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/event_router.py).
- WebSocket `/sockets/events/{conversation_id}` authenticates and supports live
  events plus optional history resend (`all` or `since` a timestamp); incoming
  messages can start a run. See
  [`sockets.py` lines 205-331](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/sockets.py#L205-L331).
- Persistence stores `base_state.json` and one JSON file per event. Official docs
  list conversation history, execution state, tool outputs, statistics,
  workspace context, skills, secrets, and agent state as persisted material. See
  [Persistence](https://docs.openhands.dev/sdk/guides/convo-persistence).

**Spike implications**

- Preserve raw OpenHands event kind, ID, timestamp, source, parent ID, and payload
  before normalizing.
- Reconnect/resend can duplicate delivery; the Chora-side projection needs event
  ID deduplication and a defined ordering rule.
- OpenHands persisted conversation state is runtime evidence/input, not Chora
  canonical product state.

### 2.6 Results and error projection

**Contract/source facts**

- Workspace command/file operations return typed per-operation results, as
  described in section 2.1.
- Conversation `run()` returns `None`; completion is represented by conversation
  status and events, not a single structured terminal result object.
- Agent Server provides
  `GET /api/conversations/{conversation_id}/agent_final_response`, returning an
  `AgentResponseResult { response: str }`. It selects the last FinishAction text
  or last agent MessageEvent text, and returns an empty string if no response is
  available. See
  [`conversation_router.py` lines 150-167](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/conversation_router.py#L150-L167)
  and
  [`models.py` lines 589-600](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/models.py#L589-L600).
- `error` and `stuck` cause `RemoteConversation` to raise
  `ConversationRunError`; error detail is recovered from recent
  `ConversationErrorEvent` data. A wait timeout can occur while the remote run
  continues.

**What OpenHands does not supply as one object**

The reviewed public surface does not provide a single immutable terminal record
that combines conversation status, normalized exit reason, adapter/process exit
code, timeout/cancel classification, final text, artifacts, cleanup result, and
all errors.

**Spike implication**

Any Chora terminal result must be adapter-generated from multiple facts:

| Needed projection | Candidate OpenHands source | Adapter-owned decision |
|---|---|---|
| Run identity | conversation UUID; workspace/runtime/container ID | bind it to exactly one Chora Attempt |
| Status | `ConversationExecutionStatus` and terminal state event | normalize timeout/cancel/error/stuck/finished |
| Final text | final-response endpoint or terminal agent message | resolve empty/multiple/fallback cases |
| Errors | `ConversationErrorEvent`, `AgentErrorEvent`, `ConversationRunError` | precedence and stable error schema |
| Tool exit | per-command `CommandResult.exit_code` | do not confuse with run/adapter exit |
| Artifacts | attempt workspace plus file/git APIs | allowed paths, hashes, and collection time |
| Cleanup | no conversation result field | inspect and record container/runtime/workspace cleanup |

This is a source-based mapping hypothesis. The main spike must prove that one and
only one Chora terminal result can be emitted for completion, interrupt, timeout,
and abnormal-exit paths.

### 2.7 Agent Server API boundary

**Contract**

- Agent Server is a FastAPI HTTP/WebSocket service. Official docs list health,
  readiness, server info, interactive OpenAPI docs, and authenticated
  conversation/workspace/file/command/settings APIs. See
  [Agent Server Package](https://docs.openhands.dev/sdk/arch/agent-server).
- Default auth is off. With configured session keys, clients use
  `X-Session-API-Key`; WebSocket auth is also implemented.

**Pinned key endpoints**

| Purpose | Endpoint |
|---|---|
| server liveness/readiness/info | `GET /health`, `GET /ready`, `GET /server_info` |
| create/read/list/count conversation | `POST /api/conversations`, `GET /api/conversations/{id}`, `GET /api/conversations/search`, `GET /api/conversations/count` |
| run/pause/interrupt/delete | `POST /api/conversations/{id}/run`, `POST .../pause`, `POST .../interrupt`, `DELETE /api/conversations/{id}` |
| secret update | `POST /api/conversations/{id}/secrets` |
| final response | `GET /api/conversations/{id}/agent_final_response` |
| events | `/api/conversations/{id}/events...` and `WS /sockets/events/{id}` |
| command | `POST /api/bash/execute_bash_command` (plus async start/event APIs) |
| files | `POST /api/file/upload`, `GET /api/file/download`, archive/trajectory endpoints |
| git | `GET /api/git/changes`, `GET /api/git/diff`, commit APIs |

The route composition is visible in
[`api.py` lines 373-430](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/api.py#L373-L430),
with exact route declarations in
[`conversation_router.py`](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/conversation_router.py),
[`event_router.py`](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/event_router.py),
[`bash_router.py`](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/bash_router.py),
[`file_router.py`](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/file_router.py), and
[`git_router.py`](https://github.com/OpenHands/software-agent-sdk/blob/2f27653959f7596769427ee4657247b32c94504e/openhands-agent-server/openhands/agent_server/git_router.py).

The v1.40.0 release also publishes a generated
[`openapi.json`](https://github.com/OpenHands/software-agent-sdk/releases/download/v1.40.0/openapi.json)
and
[`SHA256SUMS`](https://github.com/OpenHands/software-agent-sdk/releases/download/v1.40.0/SHA256SUMS).
The official checksum for `openapi.json` is
`a3bca6afbfdbc4373e0c4a7a79931b0cdd1836a8f7a144a992089fbab2520658`.
This release asset is the preferred frozen HTTP schema input for the spike; it
does not replace behavior tests.

## 3. Reproduction commands

These commands query metadata and source only. They do not pull or execute the
OpenHands image.

### 3.1 PyPI current version and hashes

```bash
date -u '+UTC %Y-%m-%dT%H:%M:%SZ'
for p in openhands-sdk openhands-agent-server openhands-workspace; do
  curl -fsSL "https://pypi.org/pypi/$p/json" |
    jq '{name:.info.name, version:.info.version,
         requires_python:.info.requires_python,
         license:.info.license,
         license_expression:.info.license_expression,
         artifacts:[.releases[.info.version][] |
           {filename, upload_time_iso_8601, sha256:.digests.sha256}]}'
done
```

Observed at 2026-08-04T20:13:30Z: all three current versions were `1.40.0`.

### 3.2 Release/tag/commit

```bash
git ls-remote https://github.com/OpenHands/software-agent-sdk.git \
  'refs/tags/v1.40.0' 'refs/tags/v1.40.0^{}'

curl -fsSL \
  https://api.github.com/repos/OpenHands/software-agent-sdk/releases/tags/v1.40.0 |
  jq '{tag_name,target_commitish,published_at,html_url}'
```

Observed: tag and release target
`2f27653959f7596769427ee4657247b32c94504e`; release published
`2026-08-01T02:40:22Z`.

### 3.3 OCI index and platform digests

```bash
TOKEN="$({
  curl -fsSL \
    'https://ghcr.io/token?scope=repository%3Aopenhands%2Fagent-server%3Apull'
} | jq -r .token)"

for tag in 1.40.0-python 1.40.0-java 1.40.0-golang; do
  curl -sS -D - -o /dev/null \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Accept: application/vnd.oci.image.index.v1+json' \
    "https://ghcr.io/v2/openhands/agent-server/manifests/$tag" |
    tr -d '\r' | rg -i 'docker-content-digest|content-type'

  curl -fsSL \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Accept: application/vnd.oci.image.index.v1+json' \
    "https://ghcr.io/v2/openhands/agent-server/manifests/$tag" |
    jq '{mediaType, manifests:[.manifests[] | {digest,platform,annotations}]}'
done
```

Observed at 2026-08-04T20:15:38Z: the three index and platform digests listed
in section 1.3. The `unknown/unknown` descriptors in each index are attestation
manifests, not runnable architectures.

### 3.4 Verify image-to-source linkage without pulling layers

```bash
TOKEN="$({
  curl -fsSL \
    'https://ghcr.io/token?scope=repository%3Aopenhands%2Fagent-server%3Apull'
} | jq -r .token)"
MANIFEST='sha256:b2326ac6d444f3f80f2fa3260ab21653a9b6dfd02d5331643921428d79b87cc6'

CONFIG="$({
  curl -fsSL \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Accept: application/vnd.oci.image.manifest.v1+json' \
    "https://ghcr.io/v2/openhands/agent-server/manifests/$MANIFEST"
} | jq -r .config.digest)"

curl -fsSL \
  -H "Authorization: Bearer $TOKEN" \
  "https://ghcr.io/v2/openhands/agent-server/blobs/$CONFIG" |
  jq '{created,architecture,os,
       build_env:[.config.Env[] |
         select(startswith("OPENHANDS_BUILD_GIT_"))],
       entrypoint:.config.Entrypoint}'
```

Compressed layer totals can be derived without pulling layers:

```bash
for digest in \
  'sha256:b2326ac6d444f3f80f2fa3260ab21653a9b6dfd02d5331643921428d79b87cc6' \
  'sha256:997ab654dc39b9fd1faa0f1d007009dc032d2341eb333399eed222bdddc349e1'
do
  curl -fsSL \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Accept: application/vnd.oci.image.manifest.v1+json' \
    "https://ghcr.io/v2/openhands/agent-server/manifests/$digest" |
    jq '{layer_count:(.layers|length),
         compressed_layer_bytes:([.layers[].size]|add)}'
done
```

Observed at 2026-08-04T20:29:24Z: the layer counts and byte totals listed in
section 1.3.

### 3.5 Verify the release OpenAPI asset

```bash
curl -fsSL \
  https://github.com/OpenHands/software-agent-sdk/releases/download/v1.40.0/openapi.json |
  shasum -a 256

curl -fsSL \
  https://github.com/OpenHands/software-agent-sdk/releases/download/v1.40.0/SHA256SUMS |
  rg ' openapi.json$'
```

Both must report
`a3bca6afbfdbc4373e0c4a7a79931b0cdd1836a8f7a144a992089fbab2520658`.

## 4. Source-only conclusions for the bounded spike

1. **Use the 1.40.0 line, not latest.** Package, release, commit, and image
   metadata form a consistent pin chain, and the image embeds the same commit.
2. **The SDK provides useful execution primitives, not Chora's terminal
   contract.** Events, statuses, final text, operation results, errors, and
   cleanup evidence are separate facts; the Chora adapter would have to produce
   the unique terminal result.
3. **DockerWorkspace is lifecycle convenience, not proven attempt isolation.**
   It supports an attempt-only mount and normal context cleanup, but host access,
   default network behavior, secret non-leakage, and crash cleanup require real
   tests.
4. **Timeout is not cancel.** A remote wait timeout may leave the server run
   active. The spike must issue interrupt and verify eventual status and resource
   cleanup.
5. **Secret masking is not containment.** It injects resolved values and masks
   known literal values in output; it does not establish non-exfiltration.
6. **Remote Runtime API is not an official production recommendation.** The
   official guide scopes it mainly to benchmark evaluation. DockerWorkspace is
   the more bounded local candidate for this spike.
7. **OpenHands conversation persistence must remain runtime-owned evidence.**
   Nothing in the official API requires Chora to adopt OpenHands UI, database,
   or conversation state as canonical product state.

No ADOPT/REJECT decision follows from this note. That decision requires the
normal, interrupt, timeout, cleanup, abnormal-exit, host, network, secret,
workspace, event/result projection, and cost evidence from the actual spike.
