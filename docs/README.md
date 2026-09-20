# Chora documentation

**English** | [简体中文](README.zh-CN.md)

Start with [Quick start](../README.md#quick-start) to run Chora and
[Your first task](../README.md#your-first-task) to try a code change.
For ongoing use, see [backup, recovery, and diagnostics](workbench-maintenance.md).
The references below cover current operation and public-source checks.

[Isolated execution](isolated-local.md): public preparation, supported scope, boundaries and real acceptance entry.

[Optional app preview](app-preview.md): inspect a Task Web app on demand without changing code acceptance.

[macOS application](macos-application.md) — packaged runtime, authentication, migration, updates and distribution limits.

## Accepted product direction

- [Project workflows and execution settings](project-workflows.md) — two execution
  environments, reusable settings, historical profile compatibility and planned
  research/design/document collaboration within software projects.
- [Next personal workspace slices](../ROADMAP.md#next-personal-workspace-slices)
  — M2-S8 then M2-S9, start conditions and observable completion criteria.

## Current product and acceptance guidance

- [Public roadmap](../ROADMAP.md) — accepted slice versus planned foundations,
  SCM, public usability and exact candidate release gates.
- [Current local Pi Workbench](../README.md#quick-start)
  — multi-repository Task entry, automatic/named/no-check selection, managed or
  PATH Pi, model provenance and Task-branch delivery, distinct from retained
  private M1 inputs.
- [Workbench maintenance, backup/restore, and redacted diagnostics](workbench-maintenance.md)
  — stopped whole-data-root maintenance, ownership boundaries, update limits,
  and the read-only Workbench doctor. The restore recipe still requires exact
  frozen-candidate qualification.

- [Dependency metadata preflight](dependency-metadata.md) — temporary npm SBOM,
  Go module inventory, and deterministic public-candidate export checks.

The maintained credential-free public browser journey is `npm run e2e:public`
or `make public-e2e`. Real Pi validation remains a separate explicit
`npm run test:e2e:pi-local-connected` run; a skipped real case is not acceptance.

## Skills and MCP

- [Skills and MCP in local execution](native-capabilities.md) — Project configuration,
  native capability discovery, failure states and connection verification.

## Task board implementation

- [Task board guide](task-board.md) — Project/Room Board/List, attention,
  filters, lifecycle outcomes and freshness.
- [Native Task Board V0 — M2-S6A](task-board-plan.md) — **COMPLETE**.
  Product boundaries, lifecycle projection, read API and acceptance matrix;
  [Chinese translation](task-board-plan.zh-CN.md).

## Local-only validation evidence

`docs/validation/` is currently excluded from version control. It contains
version-pinned local evidence, not current runtime-selection authority. Do not
link or publish it until the owner explicitly chooses a sanitized disposition.
