# Context Snapshot

## Task

- ID: `task_018f0000-0000-7002-8000-000000000002`
- Room: `room_018f0000-0000-7001-8000-000000000001`
- Title: Build deterministic context
- Goal: Assemble a frozen context snapshot

### Briefs

- **Project brief** (`context_revision_018f0000-0000-7020-8000-000000000020`): Chora is a human-agent workspace. [docs/brief.md]

## Run Charter

- ID: `charter_018f0000-0000-7005-8000-000000000005`
- Task ID: `task_018f0000-0000-7002-8000-000000000002`
- Task Goal: Assemble a frozen context snapshot
- Workspace Root: `/tmp/chora`
- Adapter ID: `codex`
- Sandbox Mode: `workspace-write`
- Expected Output: verified context snapshot
- Responsible Human: Yang Yang
- Initiator: human
- Created At: `2026-07-29T00:00:00Z`
- Confirmed Sensitive Revision IDs:
  - None.
- Sensitive Exclusions:
  - None.
- Capabilities:
  - `network`: denied
  - `read_workspace`: allowed

## Acceptance Criteria

1. **deterministic** (`criterion_018f0000-0000-7003-8000-000000000003`): same input has the same bytes
2. **reviewed** (`criterion_018f0000-0000-7004-8000-000000000004`): promotion requires review

## Decisions

- **Storage** (`context_revision_018f0000-0000-7021-8000-000000000021`): Use canonical JSON. [docs/design.md]

## Constraints

- **Boundary** (`context_revision_018f0000-0000-7022-8000-000000000022`): Do not add a fact system.

## References

- **Blueprint** (`context_revision_018f0000-0000-7023-8000-000000000023`): Architecture source. [BLUEPRINT.md]

## Unknowns

- **Future adapter** (`context_revision_018f0000-0000-7024-8000-000000000024`): SQLite arrives in Task 5.

## Excluded

None.

## Delta

None.
