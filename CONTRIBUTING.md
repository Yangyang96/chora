# Contributing to Chora

[简体中文](CONTRIBUTING.zh-CN.md)

Chora is currently a source-checkout Local Alpha, not a public release. The only
validated development host is Apple Silicon macOS. Work on other hosts is
welcome as investigation, but it must not be described as supported without
equivalent evidence.

## Before contributing

Chora is licensed under the GNU Affero General Public License v3.0. By
submitting a contribution, you agree that it may be distributed under the same
license and represent that you have the right to submit it.

Read the [public source scope](docs/publication-scope.md) before adding files.
Do not add vendored dependencies, private assets, raw evidence, credentials, or
unreviewed experiments.

Agree on a narrow scope before starting. Preserve unrelated and uncommitted
work, and do not weaken these product boundaries:

- managed execution fails closed when isolation or identity cannot be proved;
- it never falls back silently to direct host execution;
- the legacy Codex Runtime stays disabled;
- private release assets, credentials, and acceptance evidence are not public;
- a Local Alpha result is not a release or cross-platform support claim.

The [Roadmap](ROADMAP.md) distinguishes the current public target and
pending gates. Local Connected uses user-installed/configured Pi, does not
require private M1 images, and makes no Sandbox-isolation claim.

## Development workflow

Use Node.js 22.12 or newer, npm 11 or newer, and Go 1.26 or newer. Install the
JavaScript dependencies before running focused checks:

```sh
npm ci
go mod download all
```

Run the smallest relevant Go, Web, and end-to-end checks while iterating. Add or
update tests for observable behavior, persistence, API, or security-boundary
changes. Before a change is accepted on the validated host, the complete gate is:

```sh
make test
make verify
make vet
npm run e2e
git diff --check
```

Some real-Agent checks need the private, pre-provisioned Local Alpha environment.
If you cannot run one, state exactly what you ran and what remains unverified;
do not replace it with a weaker support claim.

Keep changes focused, explain user-visible and contract effects, and document
known risks. Never include credentials, private endpoints, machine-specific
paths, or generated acceptance evidence in a contribution.


## Commit messages and maintainer pushes

Write commit messages entirely in English using Conventional Commits:

```text
<type>[(<scope>)][!]: <subject>

<body>

<footer>
```

- Allowed types: `build`, `chore`, `ci`, `docs`, `feat`, `fix`, `perf`,
  `refactor`, `revert`, `style`, and `test`.
- Use a scope only for a stable subsystem, never a temporary project phase.
- Use a lowercase imperative subject. Aim for 50 characters, never exceed
  72 characters, and omit the final period. Describe the outcome; avoid
  vague subjects such as `WIP`, `misc`, or `updates` and AI attribution.
- Non-trivial commits need a body, separated by a blank line and wrapped at
  72 characters. Explain the reason and primary outcome, then material
  compatibility or migration effects and actual validation. Keep it concise;
  group by capability rather than listing files. Fixed headings are optional.
- Mark breaking changes with `!` and a `BREAKING CHANGE:` footer. Reference
  only real issues. Keep each commit focused on one coherent change.

For example:

```text
docs: make bilingual onboarding easy to follow

Lead with startup commands and the first task workflow so new users can
start without reading maintainer history. Keep both languages aligned.

Validate documentation links and matching shell examples.
```

The maintainer's development workflow is validate, commit, then push directly
and without force to `main`; a separate PR is not required for maintainer work.
External contributors should use pull requests. CI continues to run on `main`.
Review the staged diff, preserve unrelated work, and exclude private history,
secrets, local agent state, and generated artifacts. Only repository
administrators have permission to force-push `main`; an active ruleset blocks
force pushes for every other role. Routine development still uses normal
pushes. Agents must obtain explicit authorization for a specific history rewrite
before using that permission. Branch deletion remains disabled. Tag, release,
and package publication remain separate actions.
