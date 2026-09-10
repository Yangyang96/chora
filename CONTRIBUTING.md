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
