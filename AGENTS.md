# Chora Public Repository Guidance

## Scope

- This directory is the complete open-source Chora repository.
- Keep maintainer workspace files, private history, acceptance evidence, secrets,
  machine-specific paths and generated artifacts outside this tree.
- Preserve unrelated user changes and follow the repository's existing structure.

## References

- Read `CONTRIBUTING.md` for contribution, compatibility and publication rules.
- Read `docs/` only when the task touches the relevant product, architecture or
  public contract.
- Use the current code and accepted public documentation as the source of truth;
  historical notes do not activate work or require repeating old approvals.

## Implementation and verification

- Choose the smallest change that satisfies the request.
- Run checks appropriate to the changed behavior; run E2E for user-visible,
  persistence, API or cross-layer changes. Do not run the full suite for a
  documentation-only change unless the task requires it.
- Continue through implementation, relevant verification and fixes for failures
  caused by the change. Report remaining limitations clearly.

## Git

- Follow the commit format and maintainer/external-contributor workflow in
  `CONTRIBUTING.md`.
- Stage only task-related files. Never force-push or rewrite published history.
- Publishing tags, releases and packages is separate from ordinary development.
