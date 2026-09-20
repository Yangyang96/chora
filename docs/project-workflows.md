# Project workflows and execution settings

**English** | [简体中文](project-workflows.zh-CN.md)

Accepted product direction, 2026-09-20. S8-1 execution terminology is implemented;
reusable Project defaults and S9 remain planned.
[The roadmap](../ROADMAP.md#next-personal-workspace-slices) owns their delivery
order, scope and completion criteria.

## Software-project collaboration

Chora is a workspace for people and Agents to develop software together. Its next
expansion covers research, design decisions and documentation within a software
Project. Coding remains a supported workflow. Work unrelated to software projects,
general office applications and a generic workflow platform are outside this scope.

A Task expresses a bounded objective, selected resources and an expected outcome.
The outcome can eventually be a sourced finding, a reviewed design, a document
revision or a code change. These are examples, not a mandatory task-type form.
Repository changes use the existing check, Review and delivery contracts. A
research or design outcome should not require a fabricated Diff, Commit or PR.

Project -> Room -> Task ownership stays intact. A Project may already exist
without repositories, but current Task creation still requires repository scope.
Repository-optional Tasks and non-code completion are planned in M2-S9. The first
slice uses explicitly supplied project material and existing authorized Provider
capabilities; it does not require a new document connector, editor suite or
Contexere implementation.

## Separate the user's choices

| Concern | Meaning | Product treatment |
| --- | --- | --- |
| Task | Objective, resource scope and expected result | The main entry point; avoid mandatory workflow classification. |
| Agent and model | Who executes the work | Reuse qualified Pi and Runtime-owned model discovery; no additional Runtime is implied. |
| Execution environment | Where execution occurs and what isolation is claimed | Two current choices: local execution and isolated execution. |
| Capabilities | The tools, Skills and MCP resources actually available there | Reuse native capabilities and Project configuration; disclose unavailable resources. |

These distinctions do not require four selectors in every Task. A Project keeps
defaults; the Task composer shows a short effective-configuration summary and
offers adjustments when needed. Resource registration and configuration inheritance
do not by themselves grant execution authority or additional resource access.

The user-facing labels are **Local execution · No Sandbox** and
**Isolated execution**. The persisted technical IDs remain `trusted_local` and
`isolated_local`; commands, file paths and policy identifiers keep their current
names. The labels describe the same two existing routes, not additional modes.

## Disposition of existing profiles

| Existing profile | Decision |
| --- | --- |
| `minimal` | Keep an internal comparison configuration only when a real test needs it; no separate user-facing mode or mandatory release qualification. |
| `standard` | Useful tools belong in the default Agent capability configuration; no separate product tier called Standard. |
| `trusted_local` | Retain the local execution route and its explicit, persistent No Sandbox disclosure. |
| `isolated_local` | Retain the isolated execution route, readiness checks and declared isolation policy. |

Minimal/Standard are tool-composition choices, not levels of reasoning effort or
security. They no longer define a required product matrix. M2-S4-2 is withdrawn
as a scheduled qualification milestone; it is not marked complete or PASS. A new
environment or capability preset needs a demonstrated user need and its own
bounded scope before entering the roadmap.

Minimal/Standard comparison fixtures may remain when they provide useful internal
coverage. They are not a product compatibility promise, and S8-1 does not add
migration, reopening or qualification requirements for old Minimal/Standard
history. No compatibility adapter is introduced. Persisted execution IDs,
commands, file paths and policy identifiers are unchanged.

## Reusable settings with explicit execution authority

M2-S8 reuses the existing model controls and native Skills/MCP configuration to
provide Project defaults and deliberate Task overrides. Defaults must not silently
choose host execution: the local disclosure still requires acknowledgement.
Show the selected environment, effective model choice and relevant capability
availability before launch. A Runtime default is distinct from an observed model;
record actual-use provenance after execution as today.

Resolve Project defaults and Task overrides when creating the Task's execution
settings. Freeze the effective settings and their source/version in execution
records. Changing a Project default affects later Tasks, not an existing Task's
retries, resume or history. Explicit changes use the supported successor-execution
contract; when no compatible switch exists, require a new Task. Do not widen the
existing model/profile switch contracts as a side effect of adding defaults.

Model, credentials and capabilities must be supported by the chosen environment.
Today [Project Skills/MCP](native-capabilities.md) applies only to local execution;
the isolated route has its own model and credential limitations described in the
[isolated execution guide](isolated-local.md). Inheriting settings must not project
host extensions, files or credentials into isolation. Unsupported selections
stay unavailable and explain how to recover; they do not trigger model or host
fallback. Cancellation, restart, immutable repository bases and reviewed results
retain their existing guarantees.

## The first project collaboration journey

The next workflow to prove is:

```text
Project question + explicitly selected material
-> sourced research and a proposed design
-> human feedback and revision
-> accepted conclusion or project document revision
-> explicit reference from a later implementation Task
```

M2-S9 first proves this journey for a single user with the existing Agent. It
reuses Task, Result, Artifact, Decision, Review and context-selection mechanisms
where they fit. It does not create a parallel issue, approval or knowledge engine.
Missing evidence remains unknown; an Agent proposal is distinct from a human
decision. Approval of a finding or document does not authorize repository writes,
external publication or a later implementation Task.

Persist source references, revision identity, feedback and review state so work
can reopen after a restart. A later Task explicitly selects an accepted revision;
subsequent edits must not change its frozen input. Material stays in its owning
Room until deliberately referenced or shared within the Project. Existing code
Tasks retain their Diff/check/SCM path, and the Task Board must truthfully display
both workflows without inventing code-delivery phases for a document result.

Team authority and delegation remain M3; background and remote work remain M4.
Formal macOS distribution is a separate release track. None of these is required
to prove the next personal project workflow.
