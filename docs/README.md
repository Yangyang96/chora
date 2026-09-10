# Chora documentation

[English](README.md) | [简体中文](README.zh-CN.md)

This index separates current product guidance from retained plans, decisions,
research, and validation evidence. A document's presence does not make every
path it describes an accepted product path. Start with the repository
[README](../README.md) for the current milestone and development entry point.

## Current product and acceptance guidance

- [Public roadmap](../ROADMAP.md) — accepted slice versus planned foundations,
  SCM, public usability and exact candidate release gates.
- [Current local Pi Workbench](../README.md#local-pi-workbench-and-retained-m1-path)
  — multi-repository Task entry, automatic/named/no-check selection, managed or
  PATH Pi, model provenance and Task-branch delivery, distinct from retained
  private M1 inputs.
- [Workbench maintenance, backup/restore, and redacted diagnostics](workbench-maintenance.md)
  — stopped whole-data-root maintenance, ownership boundaries, update limits,
  and the read-only Workbench doctor. The restore recipe still requires exact
  frozen-candidate qualification.

- [Local Alpha product boundary and developer validation](local-alpha-development-validation.md)
  — retained M1 isolated maintainer evidence, not the current Workbench startup
  guide. Installed lifecycle acceptance has not passed.
- [Task-owned managed Git worktrees decision](decisions/2026-08-24-task-owned-worktrees.md)
  — accepted product decision.
- [UI redesign E2E scenario mapping](ui-redesign-e2e-scenarios.md) — accepted
  scenario mapping and the boundary for tests that still require the real
  environment.
- [Open-source readiness](open-source-readiness.md) — current publication
  checklist; it is not a release declaration.
- [Public source scope](publication-scope.md) — exact first-candidate inclusion,
  exclusion, and automated boundary.
- [Public source release process](release-process.md) — O5 v2 sequencing for
  safe-now, candidate-frozen, and repository-created work.
- [Dependency metadata preflight](dependency-metadata.md) — temporary npm SBOM,
  Go module inventory, and deterministic public-candidate export checks.

The maintained credential-free public browser journey is `npm run e2e:public`
or `make public-e2e`. Real Pi validation remains a separate explicit
`npm run test:e2e:pi-local-connected` run; a skipped real case is not acceptance.

## Plans and implementation records

- [Task-owned managed worktrees plan](plans/2026-08-24-task-owned-worktrees.md)
  — implementation plan retained alongside its accepted decision. Milestone
  labels in the plan are records, not independent current authority.

## Historical decisions and research

These files explain rejected alternatives and preserve their contemporary
evidence. They must not override current product guidance.

- [OpenAI Agents SDK Sandbox Agents decision](decisions/2026-08-05-openai-agents-sandbox-decision.md)
  — rejected spike decision.
- [OpenHands Runtime/Sandbox decision](decisions/2026-08-05-openhands-runtime-sandbox-decision.md)
  — rejected spike decision.
- [OpenAI Agents SDK Sandbox Agents source audit](research/2026-08-05-openai-agents-sandbox-spike-sources.md)
  — historical research for the rejected spike.
- [OpenHands Runtime/Sandbox source audit](research/2026-08-05-openhands-runtime-sandbox-spike-sources.md)
  — historical research for the rejected spike.

## Local-only validation evidence

`docs/validation/` is currently excluded from version control. It contains
version-pinned local evidence, not current runtime-selection authority. Do not
link or publish it until the owner explicitly chooses a sanitized disposition.
