# Local Alpha product boundary and developer validation

[Documentation index](README.md) | [Open-source readiness](open-source-readiness.md)

Status: the Source-checkout Local Alpha described in the repository
[README](../README.md) is the current validated product path. It is validated
only in the declared development environment and is not a public release or a
publicly reproducible installation. Private assets, owner-provided OAuth,
publication, and authenticated OCI distribution remain outside the accepted
boundary.

The installed lifecycle described below is implemented design and reference
material. The former installed-product O4 milestone was superseded without
acceptance, so these sections do not establish an accepted installed-product
path. The `tools/localalpha` source-bundle flow later in this document is a
separate historical developer-validation fixture. Nothing here expands support
beyond those exact classifications.

## Product path

The current product path starts the local UI from the source checkout with
`go run ./cmd/chora source-checkout` and the exact preflight inputs documented
in the repository README. It fails closed when its declared Engine, Sandbox,
images, source baseline, repository, data root, or OAuth input is unavailable
or invalid.

The lifecycle API and CLI described next are implemented reference behavior,
not the accepted startup path. The later `tools/localalpha` workflow is a
historical appendix and is neither the current source-checkout command nor an
accepted installed-product experience.

### Agent execution profiles

`Standard` is the default real-Agent profile. It binds the managed Pi Runtime,
Docker execution provider, and `chora.standard.v1` capability policy to the
Task and every resulting Attempt. It does not silently fall back to the host.

Skill comparison is conditional. A complete comparison can return `PASS` with
an approved projection or the explicit terminal result `NO_SKILL`. The current
Standard runtime remains deliberately `--no-skills`; `NO_SKILL` therefore does
not weaken the launch contract or create implicit Skill authority.

`Trusted Local · No Sandbox` is a separate profile that runs a supported local
Pi process directly on the host. It can inherit local Pi configuration, Skills,
Rules, MCP servers, Hooks, Extensions, login state, shell environment, and
credentials, and can have broader filesystem and network access than Standard.

Trusted Local requires acknowledgement of the exact current disclosure policy,
`chora.trusted-local-disclosure.v1`. The product reads the persisted current
acknowledgement before allowing selection. A missing record, a previous policy
version, an acknowledgement conflict, or an unreadable acknowledgement state
fails closed and keeps Standard selected. The acknowledgement is append-only,
actor/session attributable, and idempotent for the same request.

Public acknowledgement routes are:

```text
GET  /api/agent-execution/trusted-local-acknowledgements/current
POST /api/agent-execution/trusted-local-acknowledgements
```

The POST body contains only the exact `policyVersion` and requires an
`Idempotency-Key` header.

### Read-only Doctor and lifecycle routes

An installed process can start in installation-only mode even when there is no
active generation. Doctor is read-only: it observes the explicitly configured
Engine and installation state and does not pull, load, build, install, activate,
remove, or allocate anything.

The product-only HTTP surface is:

```text
GET  /api/product-installation/doctor
POST /api/product-installation/setup
POST /api/product-installation/upgrade
POST /api/product-installation/gc
POST /api/product-installation/uninstall
```

Every POST requires an `Idempotency-Key` header and an exact action confirmation.
Unknown JSON fields and trailing JSON values are rejected. The accepted bodies
are exactly:

```json
{"confirmation":"setup","confirmPinnedEngineTools":true,"confirmColimaVM":true}
{"confirmation":"upgrade"}
{"confirmation":"gc","limit":8}
{"confirmation":"uninstall"}
```

The GC limit is an integer from 1 through 64. The HTTP boundary never accepts
filesystem paths, image digests or references, Engine endpoints, authority
grants, credentials, or executable commands. Those materials are preconfigured
by the installed product controller, which derives action-bound local authority
from the request actor, session, action, and idempotency key.

The equivalent explicit operator commands are named:

```text
chora product doctor
chora product setup
chora product upgrade
chora product gc
chora product uninstall
```

`chora setup` is an alias for product Setup. The CLI requires the exact installed
paths, identities, Engine target, action authorization, actor/session/grant, and
idempotency key appropriate to the action. These are operator inputs, not fields
accepted by the browser API.

### Safe public status

Doctor and mutation responses use
`chora.product-installation-status/v1`. The public fields are limited to:

- `status` and a stable `reasonCode`;
- Engine readiness plus API version, operating system, architecture, and
  context name;
- active and candidate generation IDs;
- allowed Setup, Upgrade, GC, and Uninstall actions;
- `replayed` and `restartRequired`.

The public response excludes the Docker CLI path, context endpoint, endpoint
digest, daemon ID, local roots, asset references or digests, grants, commands,
credentials, and raw private errors. Invalid or hostile controller output is
rejected instead of being forwarded.

### Engine selection and qualification

The installed lifecycle preserves an explicitly selected compatible Engine. It
does not replace an existing compatible Engine merely because the supported
fallback is Colima.

The installation transaction is provider-neutral. Its Engine target is one
exact absolute CLI path, context name, and context-endpoint digest. Qualification
binds the declared capability contract to one observation containing daemon ID,
API version, operating system, architecture, context, and endpoint digest. The
same Runner observes the Engine immediately before and after the disposable
Probe; identity drift, incomplete cleanup, or an unsuccessful forced-termination
proof yields no qualification. Upgrade, GC, Uninstall, installed startup, and
Attempt preparation revalidate the persisted target and Engine evidence and
fail closed on drift.

The disposable Probe uses a pre-existing exact image, an internal network,
read-only mount, non-root user, dropped capabilities, resource limits, and
`--pull=never`. Managed Agent and Verifier Attempt containers also use
`--pull=never`. Setup and Upgrade are the only lifecycle phases allowed to
acquire manifest-bound OCI assets through authenticated exact pull or install
the authenticated exact private Pi fallback. Attempt preparation cannot build,
pull, or load an image.

### Explicit Colima fallback

If the declared Engine is unavailable, Setup can offer the exact private
fallback only after two separate confirmations:

1. install the pinned Colima `0.10.3` and Docker CLI `29.6.1` tools from their
   authenticated exact sources; and
2. allocate/start the exact named Colima VM with 2 CPU, 4 GiB memory, and
   20 GiB disk.

Tool installation and VM allocation use distinct exact grants. The private
Docker CLI path, named Colima profile, and endpoint digest remain frozen; PATH
or a process-global current Docker context is never substituted. A compatible
Engine leaves this fallback untouched.

This implementation constraint does not mean that public downloads or
authenticated OCI distribution have been accepted. It also does not establish
installed-product O4 acceptance.

### PATH-first Pi ownership

Setup first inspects PATH for the exact compatible Pi release. A compatible PATH
Pi is reused as an external, user-owned asset; Chora neither copies it into the
generation nor removes it.

If no compatible PATH Pi exists, Setup may install only the authenticated,
manifest-bound private Pi closure. If PATH was compatible during Setup but later
drifts, installed startup may create that same exact private fallback outside
Attempt preparation. Private bytes and their removal journal are Chora-owned.
Full Uninstall resumes any interrupted private removal and proves it absent.

Uninstall never removes the user's PATH Pi or other user Pi state. Trusted Local
also remains a distinct host-execution choice and is never silently selected as
a fallback for Standard.

### Setup, Upgrade, GC, and Uninstall semantics

All mutation authority is action-specific and checked before the cross-process
installation lock, durable journal write, or external mutation. Reusing the same
idempotency key with the same action and request replays its stored result;
reusing it for different input fails with `idempotency_conflict`. Only one
incomplete installation operation may proceed.

- **Setup** acquires only missing exact Chora-owned assets, reuses exact existing
  assets, runs and cleans the capability Probe, verifies the complete candidate,
  then atomically activates the first generation. It cannot replace a distinct
  active generation.
- **Upgrade** requires a distinct active predecessor, validates the candidate
  before atomic activation, and preserves the prior active generation until the
  switch is proved. Failure rolls back only newly acquired, unreferenced,
  Chora-owned assets.
- **Bounded GC** considers only superseded generations and removes at most the
  requested number of eligible asset identities. It skips active, candidate,
  active-Attempt, recoverable-Attempt, shared, external, and unrelated assets.
- **Uninstall** refuses referenced generations, atomically deactivates an active
  target, and removes only exact Chora-owned assets no surviving generation
  needs. It preserves user Pi, repositories and worktrees, Task/Run evidence,
  credentials, and unrelated Engine resources.

Setup, Upgrade, and Uninstall set `restartRequired`. The currently running
process does not hot-swap a generation or Runtime; restart is required to load
the resulting installed state. GC alone does not require a restart unless a
previous lifecycle action already latched the requirement.

The operation journal checkpoints acquisition, Probe, verification, activation,
deactivation, removal, and rollback. After interruption, an exact replay resumes
from the durable phase. Ambiguous asset identity, Engine drift, a missing or
invalid complete active/recoverable generation-reference snapshot, or unproven
cleanup remains blocked rather than being guessed healthy.

## Preservation and nonclaims

The lifecycle preserves:

- any compatible pre-existing Engine and all unrelated Engine assets;
- PATH/user Pi and Trusted Local user configuration;
- repositories, Task worktrees, patches, and product evidence;
- active, candidate, active-Attempt, and recoverable-Attempt generations;
- the disabled legacy Codex Runtime and the no-direct-host-fallback rule.

The current candidate does **not** claim:

- a public release, published package, or publicly reproducible install;
- accepted authenticated OCI acquisition or distribution;
- installed packaging/O4 acceptance;
- support for Intel Macs, Linux or Windows product hosts, other container
  engines, arbitrary Docker contexts, images, Pi versions, models, proxies,
  credential sources, Runtimes, or Sandboxes;
- production isolation, production SLOs, zero residue, or lossless recovery from
  arbitrary faults.

## Developer validation appendix: source-bundle flow

> Historical installed-fixture note: the source-bundle flow below is retained
> as evidence and tooling context. It is not the active Source-checkout Local
> Alpha startup path and is not required for the current milestone.

This appendix preserves the older `tools/localalpha` workflow for maintainers
who need to validate a source tree and the legacy manifest-bound development
fixture. It is not the product Setup/Upgrade/GC/Uninstall path and does not prove
the nonclaims above.

The historical validation fixture is bounded to macOS Apple Silicon, Docker
`29.6.1` through Colima `0.10.3`, the pinned Linux/ARM64 Agent and Verifier
images, Go `1.26+`, Node.js `22.12+`, npm `11+`, and Pi `0.84.2`. Its declared
model route is `openai-codex/gpt-5.6-sol` through
`http://host.docker.internal:9981` to
`https://chatgpt.com/backend-api/codex/responses` with the pinned StarPoint
public CA. The upstream can inspect authentication and Task traffic; the model
route is not local or private from that operator.

Choose new absolute `SOURCE`, `BUNDLE`, `INSTALL`, and `DATA` paths. `INSTALL`
and `DATA` must not exist. `AUTH` must be an owner-only Pi OAuth file outside all
four roots. Choose an unused loopback `PORT`.

Create and verify the source bundle:

```sh
go run ./tools/localalpha bundle --source "$SOURCE" --bundle "$BUNDLE"
go run ./tools/localalpha verify --bundle "$BUNDLE"
```

Copy the reported `aggregateSHA256` into `AGGREGATE`, then run the legacy
read-only preflight and record its `inputFingerprint` as `FINGERPRINT`:

```sh
go run ./cmd/chora doctor \
  --source "$BUNDLE" \
  --source-manifest "$BUNDLE/source-manifest.json" \
  --bundle-aggregate "$AGGREGATE" \
  --install "$INSTALL" \
  --data "$DATA" \
  --auth "$AUTH" \
  --ca "$BUNDLE/source/contracts/g2-m1a/starpoint-root-ca-2048-g2.pem" \
  --proxy http://host.docker.internal:9981 \
  --model-url https://chatgpt.com/backend-api/codex/responses \
  --port "$PORT" --json
```

A passing report has `status: "passed"` and `resourcesCreated: false`. Any
failure requires its exact reported action; do not reuse a stale fingerprint.

Stage the historical development installation without modifying
`INSTALL/source`:

```sh
go run ./tools/localalpha install --bundle "$BUNDLE" --root "$INSTALL"
go run ./tools/localalpha stage-web \
  --root "$INSTALL" \
  --stage "$INSTALL/build/web-workspace" \
  --expected-aggregate "$AGGREGATE"
(cd "$INSTALL/build/web-workspace" && npm ci --ignore-scripts && npm run web:build)
mkdir -p "$INSTALL/bin" "$INSTALL/build/go-cache" "$INSTALL/build/go-tmp"
(cd "$INSTALL/source" && \
  GOCACHE="$INSTALL/build/go-cache" \
  GOTMPDIR="$INSTALL/build/go-tmp" \
  go build -o "$INSTALL/bin/chora" ./cmd/chora)
go run ./tools/localalpha init-data \
  --root "$INSTALL" --data "$DATA" --expected-aggregate "$AGGREGATE"
go run ./tools/localalpha verify --bundle "$INSTALL"
```

This source-tree workflow stops at developer validation. Starting an installed
product now additionally requires the exact generation, release manifest,
Engine target, capability qualification, private-Pi source, and product-state
inputs owned by the product lifecycle. Do not synthesize those values from the
legacy bundle or treat this appendix as a substitute for Setup.

After stopping Chora and proving no real Agent or Verifier is active, the
legacy cleanup command removes only the exact marker-bound installation and
data roots:

```sh
go run ./tools/localalpha cleanup \
  --root "$INSTALL" --data "$DATA" --expected-aggregate "$AGGREGATE"
```

It refuses unknown, nested, unowned, or identity-drifted roots. `BUNDLE` is not
part of this cleanup command.
