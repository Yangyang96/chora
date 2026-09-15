# Contributing to Chora

[简体中文](CONTRIBUTING.zh-CN.md)

Chora is a source-checkout Local Alpha. Apple Silicon macOS is the only validated
host; other hosts need equivalent evidence before being described as supported.
See the [Roadmap](ROADMAP.md) for current capabilities and pending milestones.

## Scope and publication

- Contributions use [AGPL-3.0](LICENSE). Submit only work you have the right to
  distribute under that license.
- Keep changes focused and preserve unrelated work.
- Managed execution must fail closed when isolation or identity cannot be
  proved, with no silent host fallback. The legacy Codex Runtime stays disabled.
- Local Connected uses user-configured Pi, requires no private M1 image and
  makes no Sandbox claim. Alpha acceptance does not establish release readiness
  or support for other platforms.
- Follow the [publication policy](.github/publication-policy.json). Keep secrets,
  private assets/history, maintainer state, acceptance records, release checklists,
  generated artifacts, machine paths and private proxy endpoints outside this
  repository. Do not add vendored dependencies or unreviewed experiments.

## Documentation language

English is authoritative for public documentation. Files such as `README.md`
and `ROADMAP.md` are the primary versions; `.zh-CN.md` files are translations.

- **Read:** use English documents as the baseline for requirements, design,
  references and current status. Translations are supplementary.
- **Write:** update English first, including plans, roadmap milestones and
  status records; synchronize existing translations in the same change.
- Keep meaning, status, dates, commands, links and support limits aligned.
  Resolve differences against English and correct translations.
- Update the roadmap when milestones change. Link English documents first
  when reporting results; translated links may be added.

## Development and verification

Use Node.js 22.12+, npm 11+ and Go 1.26+. Install dependencies with `npm ci`
and `go mod download all`.

Run focused checks while iterating. Cover observable behavior with relevant
tests; run E2E for user-visible, persistence, API or cross-layer changes.
The complete code acceptance gate on the validated host is:

```sh
make test
make verify
make vet
npm run e2e
git diff --check
```

For documentation-only changes, check accuracy, translation consistency, links
and diff formatting; run the full suite only when the task requires it.
Source tests use self-contained fixtures and do not certify historical private
release artifacts. State checks passed, skipped or blocked, including real-Agent
environment limits. Explain behavior changes, compatibility effects and known risks.

## Git

Use English Conventional Commits: `<type>[(<scope>)][!]: <subject>`.

- Types: `build`, `chore`, `ci`, `docs`, `feat`, `fix`, `perf`, `refactor`,
  `revert`, `style`, `test`. Optional scope names a stable subsystem.
- Keep each commit focused. Use a concrete lowercase imperative title, aim for
  50 characters, at most 72, with no final period or AI attribution.
- Non-trivial commits need a body after a blank line, wrapped at 72 characters:
  explain why, the outcome, compatibility effects and actual validation.
- Breaking changes need `!` and a `BREAKING CHANGE:` footer. Cite only real issues.

Maintainers validate, commit and push normally to `main`; external contributors
use PRs. CI runs on `main`. Before pushing, check repository, remote, branch,
identity and staged diff; commit only task files within the publication boundary.
Report the commit SHA, target branch, push result and verification limits.

Do not force-push or rewrite published history without explicit authorization
for that operation and the required repository permissions. Branch deletion
remains disabled. Tags, releases and packages require separate authorization.
