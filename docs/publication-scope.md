# Public source scope

[English](publication-scope.md) | [简体中文](publication-scope.zh-CN.md) |
[Documentation index](README.md)

This document defines the intended first public source candidate for
`github.com/Yangyang96/chora`. It does not publish the repository, create a release,
or make the private real-Agent inputs publicly reproducible.

## Included

- Chora product source, Web UI, migrations, schemas, and development tools.
- Tests and fixtures needed to explain and verify the supported source tree.
- Current product documentation, selected historical design decisions, and
  community policy files.
- Lockfiles and checksums that are reproducibility inputs rather than secrets.
- The two `spikes/runtime-boundary/` probe files used directly by product tests;
  they are reviewed test helpers despite their historical directory name.

## Excluded from the first candidate

- `vendor/`; dependencies are restored from `go.mod`, `go.sum`, and npm
  lockfiles, and are reviewed through dependency automation.
- Unreviewed `spikes/`, raw validation evidence, local run output, browser
  captures, generated reports, and developer orchestration state.
- Repository-external Agent orchestration instructions such as the local
  implementation `AGENTS.md`; public contributor guidance lives in
  `CONTRIBUTING.md` instead.
- Frozen source-baseline snapshots and generated distribution artifacts.
- Certificates, keys, credentials, OAuth material, private Runtime images, and
  tooling bound to a developer-specific local acceptance path.

Exclusion means “not part of the public source candidate.” It does not delete
the maintainer's local files or change the accepted Local Alpha evidence.

## Automated boundary

`.github/publication-policy.json` is the machine-readable policy. Run:

```sh
npm run publication:test
npm run publication:check
npm run publication:export:dry-run
npm run dependency-metadata:check
make public-test
```

The check evaluates the Git-tracked and non-ignored candidate files that exist
in the working tree. It fails on excluded paths, unreviewed spikes,
certificate/key filenames, representative secret formats, a maintainer home
path, missing public-policy files, repository-identity drift, or license
metadata drift.

The check is deliberately bounded and is not a substitute for reviewing the
exact staged file list or running a dedicated secret scanner before the first
push. The final candidate must still be prepared in a clean, publication-only
branch or worktree.

`make public-test` runs the independently reproducible Go, Web, disclosure, and
publication checks. The complete maintainer gate also exercises frozen
Source-bundle and Baseline fixtures that are intentionally absent from the
public candidate; CI does not pretend those private-input checks are public.

## Private product inputs

M1 isolated source-checkout images and maintainer OAuth stay private. The current
Local Connected Workbench uses user-installed/configured Pi without those inputs;
Fake remains the credential-free demo. The public candidate must separately
prove this local Pi route under the [Roadmap](../ROADMAP.md) and release process.
Maintainer acceptance is not candidate PASS; missing private M1 inputs are not a
reason to substitute Fake for the public real-Pi claim.

## Historical test applicability

`e2e/public-test-applicability.json` names the exact tests that require deliberately
unpublished frozen M1/installed fixtures or the historical enterprise CA. The
public Go runner validates every package/name against the enumerated tests before
execution, and records those entries as **not applicable**, never as passes.
Other tests in the same packages, including newly added tests, remain eligible.
Missing, duplicate or ambiguous inventory entries fail the gate.

The complete maintainer `make test` and `make verify` gates still execute all of
these historical tests with their original inputs. The public result ledger is
`output/public-go-tests.json`; runtime skips are reported separately and do not
qualify the real Pi, installer, browser or private hosting journeys. Candidate
acceptance must include their applicable separate evidence.
