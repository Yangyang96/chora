# Isolated Local in Workbench

**English** | [简体中文](isolated-local.zh-CN.md)

Isolated Local runs Pi in Docker and returns validated changes to the Task's
worktrees. Select it explicitly when creating a Task. An unavailable image,
engine, credential or isolation boundary stops execution; it never selects
Local Connected automatically.

## Supported environment

- Apple Silicon macOS host; a local Linux arm64 Docker Engine with Docker API
  1.44 or later. The preparation probe must qualify the exact engine and image.
- Pi `@earendil-works/pi-coding-agent@0.85.1`, Node.js `22.19.0`,
  Git `2.39.5` (Debian `1:2.39.5-0+deb12u3`), native Pi
  `deepseek` provider and `deepseek-v4-flash` model. Bring your own DeepSeek
  API key with model access. This mode does not use the host Pi model selection or models.json.
- Git repositories with at least one commit. The initial supported project
  scope is Node.js standard-library code/tests (`node --test`), without package
  installation, native builds, external databases or dependency services.
- Each Task retains its immutable repository bases, write/reference roles,
  protected paths and check policy. Input copies must fit 4,096 ordinary entries
  and 100 MiB; the container workspace has a separate 256 MiB hard limit.

Build Chora using the [quick start](../README.md#quick-start). Install Docker
and start its local engine (for example, Colima on Apple Silicon). Select the
intended Docker context with `docker context use`; preparation records that
endpoint explicitly. Remote Docker endpoints are unsupported.

## Prepare and run

In the New Task panel, select **Isolated Local**, then **Prepare Isolated Local**.
Preparation downloads public npm inputs, verifies the frozen Pi tarball's SHA-512
integrity, installs the committed consumer lock with lifecycle scripts disabled,
and compiles the public check helper. It uses this official public Node image:

```text
node@sha256:4a4884e8a44826194dff92ba316264f392056cbe243dcc9fd3551e71cea02b90
```

Git and its Debian dependencies come from the signed Debian snapshot dated
`20260901T000000Z`, with the Git package version fixed. Snapshot index expiry is
disabled for that historical snapshot; archive signature verification remains enabled.

No private M1 image, enterprise CA, maintainer credential or registry account is
required. An exact local cache is reusable. Preparation uses a small generated
build context, never the complete checkout or your Pi home. It does not use model
credentials. A failed download/build/probe leaves the mode unavailable.

The same preparation is available from the public source checkout:

```sh
go run ./cmd/chora workbench prepare-isolated \
  --source "$(pwd -P)" --data "$HOME/.chora/data"
```

Restart Workbench with the same `--data` directory after preparation. Image,
helper, observer, engine and policy identities are checked before execution.
Replacing the prepared environment requires another restart; existing Tasks
retain their original execution contract and cannot silently bind a new image.

Before running, configure DeepSeek through native Pi so your user-owned
`~/.pi/agent/auth.json` (mode `0600`) contains a `deepseek` entry of type
`api_key` with a literal key. Shell commands and environment-variable references
are rejected. Never put the key in a repository, image or command-line argument.
Only that selected key is projected through stdin into owner-only container
memory; other providers and Git credentials are not injected. The projection
is removed with the container. This does not revoke or rotate the original
DeepSeek API key: manage expiry, billing and revocation with DeepSeek. Chora
never copies container credentials back over your native Pi login.

Create a Project/Room Task, choose repositories, roles and check policy, and
start. After execution, inspect activity, grouped diffs and observed checks.
Checks remain **Agent-reported**; Docker isolation is not an independent test
verifier. Review → Commit → Push → PR → Merge → cleanup uses the existing
per-repository delivery flow. Legacy Apply remains compatible. Git hosting
credentials stay in the host delivery flow and are never injected into Pi.

## Boundaries and recovery

| Boundary | Policy |
| --- | --- |
| Files | Private standalone copies; no original checkout, Task worktree, Docker socket or unrelated host directory is mounted writable. Git metadata, reference repositories and context are read-only. |
| Writes | Only ordinary scoped text changes are imported, after all repositories validate and the container is proven dead. Source drift, links, metadata changes and out-of-scope writes fail closed. Import uses a rollback journal. |
| Process/resources | Non-root uid 1000; read-only rootfs; all capabilities dropped; no-new-privileges; 2 CPUs; 4 GiB RAM without extra swap; 256 PIDs; 20 minutes. An additional 256 MiB task-owned workspace tmpfs lives in the Docker VM; `/tmp` 64 MiB and credential tmpfs 16 MiB live in the container. The workspace allocation is separate from the 4 GiB container limit. |
| Network | Outbound Docker bridge networking. No domain allowlist, host network namespace, published ports or Docker socket. Host network services may be reachable. |
| Credentials | Only the selected DeepSeek API key; stdin injection into owner-only tmpfs. Credentials disappear with the container. Provider requests necessarily use the network. |
| Results | Freeze the container before export; prove death before import. Bound logs to 10 MiB and reviewable artifacts/changes to 100 MiB. No partial output is imported on cancellation or timeout. |

Cancel stops the selected attempt and requires proof of termination before a
successor. After a Workbench restart, recovery cleans up only owned Docker
resources. Resume continues the persisted Task with a **fresh isolated attempt**;
it does not restore an in-container Pi conversation. Review feedback and the
Task's existing worktree changes carry forward under the same frozen authority.
An unproven old process blocks execution. Fix Docker availability and restart
before retrying; do not delete ownership records to bypass recovery.

Use Workbench's explicit cleanup after delivery or closing unwanted results.
Cleanup does not delete the original repositories. The shared prepared image is
kept for later Tasks. To remove application data, follow the stopped-service
[backup and restore procedure](workbench-maintenance.md); never delete data while
Workbench or another owner is using it.

## Real acceptance entry

Use a dedicated, prepared data directory, separate from daily-use Workbench:

```sh
CHORA_ISOLATED_ACCEPTANCE_DATA=/absolute/prepared-test-data make isolated-acceptance
```

To include actual GitHub PR → Merge → cleanup, set
`CHORA_ISOLATED_ACCEPTANCE_GITHUB_REPO=owner/disposable-test-repository` and
authenticate host Git/`gh` for that repository. This explicitly creates fixture
and Task branches, opens and merges a PR into the unique fixture branch, and
retains remote branches/PR evidence. It does not target the default branch.
Without that variable, delivery uses a local bare remote and closes the result;
that run does not qualify actual GitHub PR/Merge.

This entry requires real Docker and real Pi model access; missing prerequisites
fail instead of skipping. It creates temporary Git fixtures and verifies the
multi-repository execution, check evidence, Review result and unchanged original
checkouts. It is a real integration gate, not a claim that all historical M1 or
installed-release tests were rerun. The matching boundary gate uses the same prepared image without model requests:

```sh
CHORA_ISOLATED_METADATA=/absolute/prepared-test-data/isolated-environment.json \
TMPDIR=/absolute/docker-shared-test-tmp \
go test -tags isolated_acceptance ./internal/dockersupervisor \
  -run TestRealWorkbench -count=1 -v
```

Use a temporary directory shared with the local Docker VM. This gate checks
read-only mounts, credentials and resource limits, exact volume policy,
cancellation, recovery and zero owned residue. Its synthetic key verifies the
credential boundary only; the Workbench gate above supplies real-model evidence.

Acceptance checklist:

- Prepare from public inputs; select Isolated Local explicitly.
- Cancel, restart and Resume; modify one repository using another as reference.
- Verify final-content checks, reject in Review, repair and accept the new result.
- Commit, Push, PR, Merge and cleanup against an owned disposable GitHub target.
- Verify denied writes and invalid boundaries fail closed, with no host fallback.
- Regress Local Connected and legacy Apply; retain skips and historical exclusions
  separately from passing tests.

Minimal/Standard configurations, other Providers, remote execution, team features
and installers are outside this mode's scope.
