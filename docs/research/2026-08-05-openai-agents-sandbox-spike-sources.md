# OpenAI Agents SDK Sandbox Agents Spike: Official-source audit

Date: 2026-08-05  
Scope: `openai-agents==0.18.3`, self-hosted `DockerSandboxClient`  
Evidence boundary: OpenAI documentation, OpenAI's official GitHub repository,
PyPI provenance/metadata, and official Python container metadata only.

## Locked provenance

| Item | Locked value | Evidence class |
| --- | --- | --- |
| Package | `openai-agents==0.18.3` | Official PyPI metadata |
| Release commit | `3e788a46f180ebf120fdc39e3dfe48c98627be50` | PyPI Trusted Publishing provenance and official Git tag |
| Wheel SHA-256 | `c6ed971fdeb34d39a9931787bd3960c1e84dc5d7345705794cc5cab8a1158d07` | Official PyPI file metadata |
| Source archive SHA-256 | `e637f5f5a50692ccbedb0e4f7f2e4f8e2facfcddd41142f35faf90c89b700fc3` | Official PyPI file metadata |
| License | MIT | Official repository `LICENSE` and package metadata |
| Supported Python | `>=3.10` | Official package metadata/source |
| Spike host Python | `3.12.13` | Measured experiment environment |
| Docker extra | `docker>=6.1`; resolved `docker==7.2.0` | Official source plus locked experiment environment |
| SDK default image | `python:3.14-slim` | Official SDK source |
| Pulled image | `python:3.14-slim@sha256:b0c4ec81396588a94b99052caf2f786e6e92e03111991d3d40c68762ee48d2ab` (`linux/arm64`) | Official image metadata plus local Docker inspect |
| Requested model | `gpt-5.6-sol` | Frozen Spike input; also the default in the official Docker example at this commit |
| Actual model identity | Not observed | Experiment fact: no credential and zero model requests |

Primary records are the [PyPI release page](https://pypi.org/project/openai-agents/0.18.3/),
[PyPI release JSON](https://pypi.org/pypi/openai-agents/0.18.3/json),
[official source at the pinned commit](https://github.com/openai/openai-agents-python/tree/3e788a46f180ebf120fdc39e3dfe48c98627be50),
[package metadata](https://github.com/openai/openai-agents-python/blob/3e788a46f180ebf120fdc39e3dfe48c98627be50/pyproject.toml),
[MIT license](https://github.com/openai/openai-agents-python/blob/3e788a46f180ebf120fdc39e3dfe48c98627be50/LICENSE),
and the [official Python image page](https://hub.docker.com/_/python).
The exact installed dependency version set is retained in
`spikes/openai-agents-sandbox/requirements.lock` and the authoritative run's
`environment.json`. The lock does not include transitive artifact hashes and
installation does not use `pip --require-hashes`; it supports exact version-set
verification, not artifact-hash-locked or cryptographically reproducible
installation.

## What OpenAI officially documents

OpenAI's [Sandbox Agents guide](https://developers.openai.com/api/docs/guides/agents/sandboxes)
labels Sandbox Agents for Python and TypeScript as **Beta** and warns that API
details, defaults, and capabilities may change. It describes a split in which
the Agents SDK owns the agent loop, model requests, tool routing, handoffs,
approvals, tracing, recovery, and run state while a sandbox provides compute.
It documents Docker for local container isolation, a workspace-relative
`Manifest` that rejects absolute and parent-traversal paths, and distinct state
concepts for a live sandbox session, `RunState` session state, and snapshots.

Those are product/documentation statements. The guide does not promise that
Docker defaults enforce no egress, CPU/memory/PID limits, a read-only root,
capability removal, process-death confirmation after timeout/cancel, or
Chora-compatible orphan reconciliation.

The official
[Docker example](https://github.com/openai/openai-agents-python/blob/3e788a46f180ebf120fdc39e3dfe48c98627be50/examples/sandbox/docker/docker_runner.py#L103-L162)
shows create/resume/delete ownership around `DockerSandboxClient` and defaults
its example model to `gpt-5.6-sol`. An example demonstrates intended use; it is
not a security or lifecycle guarantee.

## Pinned source facts

At the release commit:

- [`SandboxClientConfig`](https://github.com/openai/openai-agents-python/blob/3e788a46f180ebf120fdc39e3dfe48c98627be50/src/agents/sandbox/config.py#L8-L15)
  defaults the Docker image to `python:3.14-slim`.
- [`DockerSandboxClientOptions`](https://github.com/openai/openai-agents-python/blob/3e788a46f180ebf120fdc39e3dfe48c98627be50/src/agents/sandbox/sandboxes/docker.py#L173-L189)
  exposes image and port configuration, but no public network-disable,
  resource-limit, read-only-root, capability, security-option, or label fields.
- [Docker create/resume/delete](https://github.com/openai/openai-agents-python/blob/3e788a46f180ebf120fdc39e3dfe48c98627be50/src/agents/sandbox/sandboxes/docker.py#L1446-L1607)
  creates the container with the SDK entrypoint, environment, optional mounts
  and ports; resume creates a new container when the recorded one is missing.
- [Timeout handling](https://github.com/openai/openai-agents-python/blob/3e788a46f180ebf120fdc39e3dfe48c98627be50/src/agents/sandbox/sandboxes/docker.py#L493-L532)
  attempts a best-effort `pkill -f ... || true`, swallows cleanup errors, then
  raises `ExecTimeoutError`.
- [Instrumentation events](https://github.com/openai/openai-agents-python/blob/3e788a46f180ebf120fdc39e3dfe48c98627be50/src/agents/sandbox/session/events.py#L32-L82)
  include session ID, wrapper sequence number, operation, and phase.
- The [session wrapper](https://github.com/openai/openai-agents-python/blob/3e788a46f180ebf120fdc39e3dfe48c98627be50/src/agents/sandbox/session/sandbox_session.py#L220-L234)
  initializes its sequence counter at zero; a new wrapper after resume can
  therefore repeat earlier sequence values.
- [`RunState`](https://github.com/openai/openai-agents-python/blob/3e788a46f180ebf120fdc39e3dfe48c98627be50/src/agents/result.py#L118-L122)
  can carry sandbox/session state. Chora need not and must not persist that as
  its canonical run state.

## Spike-only experimental facts

These are reproducible inferences from the retained run, not OpenAI promises:

- Normal command execution, manifest upload, file read/write, and cleanup
  worked in self-hosted Docker.
- Native SDK defaults failed every inspected hardening control. A Spike-owned
  Docker client proxy successfully imposed no network, 256 MiB memory, half a
  CPU, 64 PIDs, read-only root, all capabilities dropped, no-new-privileges,
  an init process, and tmpfs workspaces. `/tmp` had to remain executable because
  the SDK runs its workspace resolver helper there.
- A one-second timeout raised `ExecTimeoutError`, but the process survived and
  wrote a late marker. The default image did not provide the assumed `pkill`
  path, and the source treats cleanup as best effort.
- Cancelling the awaiting Python coroutine also left the command process alive.
  Removing the entire container stopped it, which is an external supervisor
  behavior rather than native command cancellation.
- A missing/dead container required a Docker preflight to preserve a `dead`
  meaning; calling SDK resume alone created a new container.
- SDK `seq` repeated after resume. A small projection can retain raw events and
  assign a Chora-owned monotonic offset, but source sequence cannot be the
  canonical ordering key.
- Exactly one strict `chora.agent-result.v1` document was generated by the
  Spike projection. It recorded the unexecuted Agent check as `UNKNOWN`.

The authoritative local-only evidence identifier is `20260805T083654Z`. Its raw
evidence remains in the maintainer workspace and is intentionally excluded from
the public source candidate.

## Beta and upgrade compatibility risk

The Beta warning makes all of the following recurring upgrade costs rather
than one-time integration work:

1. Re-audit the public Docker option surface and underlying create/timeout/
   resume implementation on every SDK upgrade.
2. Re-pin and re-inspect the moving default image by platform digest, including
   helper availability and `/tmp` execution assumptions.
3. Re-run timeout, cancellation, orphan, event-order, and terminal-result gates;
   they depend on implementation details, not documented guarantees.
4. Revalidate serialized session and snapshot compatibility without allowing
   SDK state to become canonical product state.
5. Re-qualify the chosen model independently from Sandbox behavior and record
   the response's actual model identity and measured usage.

This risk is material because the controls needed by Chora are currently
outside the SDK's public Docker configuration surface.

## Cost boundary

The pinned arm64 image inspected at 43,681,100 bytes. With the image already
present, measured session creation was 0.515-0.761 seconds; the first measured
creation was 0.640 seconds and the second 0.572 seconds. Image-pull time was not
measured. These numbers are materially smaller than the retained OpenHands
Spike's image and warm-run footprint, but they are not an end-to-end Agent cost.

Model calls, tokens, and model dollars are unmeasured because no credential was
available. OpenAI Hosted Containers were not tested, so their managed-service
dependency and container charges are not adoption evidence. No numerical model
price is asserted without an observed model run and a matching official billing
record.

## Canonical-state conclusion

SDK `RunState`, live sessions, serialized session state, snapshots, and Docker
container IDs remain external runtime facts. If this candidate were ever
revisited, Chora would still own Attempt/Run identity, workspace digest, event
offsets, one terminal result, artifacts, review, and handoff. The Spike did not
write SDK state into Chora's store or alter any product execution interface.
