# Project workflows and execution settings

**English** | [简体中文](project-workflows.zh-CN.md)

Accepted product direction, 2026-09-20. S8 execution terminology and reusable Project defaults are implemented;
S9 remains planned.
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

Open **Default execution settings** in a Project to choose its execution
environment and either an explicit model or **Use runtime default**. An
unconfigured Project uses isolated execution and the Runtime default. Saving
local execution as a default does not acknowledge host access or start anything.
New Tasks show the inherited settings. **Override Project defaults for this task**
changes only that Task; turning it off restores the Project values.

Creation resolves the selected Project version and Task overrides, then stores
an immutable snapshot with the source of each choice. A stale form is rejected;
reopen the composer to load the new defaults. Task details show the frozen initial
settings after a restart. Later Project edits do not change an existing Task's
retries, resumed session or history. Explicit successor model/environment switches
retain their existing contracts and provenance; the initial Task snapshot remains
unchanged. Runtime default is a selection policy, not an observed model identity.

Skills/MCP use the existing enabled native configuration. There is no Task-level
capability picker: name a Skill in the task or let the Agent select appropriate
capabilities. Project capability references are captured with the Task and reused
for later attempts. Chora does not copy referenced contents or credentials, freeze
global Pi files, or promise a live MCP connection. Existing resume fingerprint
checks still reject changed loaded resources. Configure a new Task when Project
capability references need to change.

Before launch, the composer discloses the environment, model selection and whether
native Skills/MCP are supported. Local execution still requires the full
**No Sandbox** acknowledgement. An unavailable saved model stays selected and
blocks launch until explicitly repaired; there is no automatic model fallback.
Isolation must be ready and never falls back to host execution. Saving defaults
does not install tools, copy credentials or grant resource access.

[Project Skills/MCP](native-capabilities.md) applies only to local execution;
isolated execution does not load host capabilities. Its model/credential limits
remain those in the [isolated execution guide](isolated-local.md).

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
