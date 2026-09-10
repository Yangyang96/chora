# Open-source readiness

[English](open-source-readiness.md) | [简体中文](open-source-readiness.zh-CN.md) |
[Documentation index](README.md)

Status: **not ready for public release**. M2-S1 local Pi product acceptance
passed; daily-use foundations, public onboarding, exact candidate and release
checks remain pending under the [Roadmap](../ROADMAP.md). Private M1 isolated
inputs and unaccepted installed lifecycle are not Local Connected prerequisites.

This is a readiness checklist, not a replacement for the repository license,
security policy, support promise, or publication approval. It records the
accepted owner decisions and the publication operations that still remain.
Follow the [public source release process](release-process.md) for their order.

## Safe fixes before publication

- [x] Add ignore rules for generated browser captures, local output, and test
  executables; keep dependency and source lockfiles tracked.
- [x] Exclude developer-bound tooling that contains a fixed personal absolute
  path from the first public candidate.
- [x] Exclude browser captures, unreviewed spikes, raw evidence, generated
  records, and frozen source snapshots from the first public candidate.
- [ ] Review the exact staged file list from a clean, publication-specific
  branch or worktree. Do not broadly stage the current dirty worktree.
- [ ] Run repository tests, verification, vetting, E2E coverage relevant to the
  published path, link validation, and a secret scan against the exact release
  candidate.
- [ ] Enable and test GitHub private vulnerability reporting after the public
  repository exists; the policy document alone cannot enable this repository
  setting.
- [x] Provide bilingual documentation navigation and label live guidance,
  historical records, and version-pinned evidence.

## Pending product and public-route gates

- [ ] M2-S2-P1..P4 HTTP/baseline/file/check foundations accepted.
- [ ] M2-S3 documented startup, repeated tasks, data maintenance and public CI accepted.
- [ ] Included Commit/Push accepted, or external Git handoff tested and claims narrowed.
- [ ] Exact exported candidate reproduces its documented local Pi route without private M1 inputs.

These are planned gates, not assertions that every implementation item is absent.

## Accepted owner decisions

- [x] Adopt GNU Affero General Public License v3.0 for the repository; see
  [`LICENSE`](../LICENSE).
- [x] Use `github.com/Yangyang96/chora`; keep the first publication labeled Local
  Alpha rather than a stable release.
- [x] Exclude vendored dependencies and restore them from module manifests and
  lockfiles. Dependency Review and Dependabot cover incoming changes.
- [x] Exclude unreviewed spikes, raw evidence, full source-baseline snapshots,
  generated distribution artifacts, and certificates. Retain only the reviewed
  Runtime-boundary probes required by product tests.
- [x] Add bilingual contribution, conduct, security, and support boundary
  documents.
- [x] Use GitHub private vulnerability reporting for security and GitHub Issues
  for best-effort public support; add issue/PR templates, CI, secret scanning,
  Dependency Review, and Dependabot policy.
- [x] Do not provide a private conduct-reporting channel yet. Keep this
  limitation explicit until the owner makes a later decision.
- [x] Keep M1 Runtime assets and maintainer authentication private. Public
  Local Connected Pi reproducibility is a separate candidate gate, not a ban.

## Retained risks and intentional boundaries

- The retained M1 isolated route depends on private assets/OAuth. The public
  Local Connected route uses the user's Pi configuration instead.
- Installed Setup, Upgrade, GC, Uninstall, and authenticated distribution are
  implemented design/reference, not accepted public product claims.
- Selected historical decisions and research may remain as clearly labeled
  context. Raw evidence and unreviewed experiments are excluded by the
  [public source scope](publication-scope.md).
- Lockfiles, checksums, source fixtures, and intentional UI images are normally
  source-controlled reproducibility inputs, not generated-output cleanup
  targets.
- Certificates and keys are excluded from the first public candidate even when
  an individual certificate would not itself be secret.

See the [documentation index](README.md) for the live/historical classification
and the repository [README](../README.md) for the current startup boundary.
