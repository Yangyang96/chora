# Public source release process

[English](release-process.md) | [简体中文](release-process.zh-CN.md) |
[Documentation index](README.md)

Chora is not publicly released. This maintainer process separates stable
preparation from checks that are meaningful only after product candidate bytes
are frozen. It implements the current O5 v2 plan; it does not revive the
superseded installed-profile O4 gate.

## Current product prerequisites and scope

The [public Roadmap](../ROADMAP.md) requires current multi-repository foundations,
full Task-branch Commit/Push/PR/Merge/cleanup and S3 technical gates before automatic
local candidate preparation/verification, followed by final user acceptance.
Apply remains compatible; this candidate does not select an SCM-excluding scope.
Record exact features/source identity; ongoing repairs are not verified frozen bytes.
Public Local Connected reproduction does not require private M1 images/maintainer OAuth.

`PREPUBLICATION_READY_FOR_USER_ACCEPTANCE` means prepublication technical work is
complete. `PUBLIC_DEVELOPER_ALPHA_READY` additionally requires the user to accept
the exact candidate. Local freeze/verification is not publication; the user performs
public Push/Merge, remote release tags, Release/package/site publication, visibility
changes and announcements. Feedback changes rerun affected gates and update the package.

## What can be done when

| Work | When | Why |
| --- | --- | --- |
| Maintain bilingual README and community policies | Now | Product details can change without changing the policy structure. |
| Validate repository/license identity, excluded paths, local links, and workflow syntax | Now | These checks protect every later candidate. |
| Maintain CI, Dependabot, Dependency Review, and secret-scan configuration | Now | Configuration can be reviewed before a repository is published. |
| Exercise temporary SBOM/module-inventory recipes and candidate export | Now | The tooling stays testable without freezing candidate-derived output. |
| Freeze screenshots, compatibility claims, version, release notes, and checksums | After candidate freeze | They describe exact product bytes and become stale when those bytes change. |
| Generate the final SBOM and third-party license inventory | After candidate freeze | Dependency output must match the exact candidate. |
| Run clean-clone installation, complete gates, and dedicated history-aware secret scanning | After candidate freeze | Results are valid only for the reviewed tree and history. |
| Enable vulnerability reporting, branch protection, required checks, and repository scanning | After repository creation | These are GitHub repository settings, not source files. |
| Local candidate commits and dedicated private test-repository delivery | Within explicit preparation-Goal authority | Produce verifiable candidate and journey evidence before final user acceptance. |
| Public product push/merge, remote release tags, release, visibility change or announcement | User publication after acceptance | Preparation is not publication authorization. |

## Candidate-freeze checklist

1. Create a clean publication-only branch or worktree. Do not broadly stage the
   current mixed worktree.
2. Materialize the exact public tree from the machine-readable
   [publication policy](../.github/publication-policy.json), then review every
   included and excluded path.
3. Clone that candidate into a clean location and restore dependencies only
   from committed manifests and lockfiles.
4. Run:

   ```sh
   npm ci
   npm run docs:links
   make public-test
   git diff --check
   ```

   Also execute the included public browser journeys and explicit real-Pi/macOS
   acceptance against the exported candidate. The current `make public-test`
   alone omits browser E2E and cannot qualify that real route. Record selected
   cases and skips; no private M1 fixture substitution. Reuse unchanged scoped
   product evidence only when exact candidate inputs match.

5. Generate the final SBOM and third-party license inventory from the exact
   dependency graph. Review exceptions manually; do not reuse an older report.
6. Run a dedicated secret scanner against both the exact candidate and the
   history intended for publication. The bounded publication check is an
   additional guard, not a replacement.
7. Finalize candidate-specific status, supported environment, limitations,
   screenshots, version, release notes, and checksums.
8. Obtain one consolidated independent readiness review with no unresolved
   P0–P3 finding.

Any change to included product or dependency bytes after steps 2–8 invalidates
the affected evidence and must rerun the smallest relevant checks.

## Repository-creation checklist

After the GitHub repository exists:

- enable and test private vulnerability reporting;
- set least-privilege default permissions for GitHub Actions;
- configure branch protection and the required public CI checks;
- confirm Dependabot, Dependency Review, and secret scanning actually run;
- verify Issue and Pull Request forms in the GitHub UI;
- keep the absence of a private conduct-reporting channel explicit until the
  owner makes a later decision.

Only then may the owner separately decide whether to publish. See the
[open-source readiness checklist](open-source-readiness.md) and
[public source scope](publication-scope.md) for the current boundaries. The
[dependency metadata preflight](dependency-metadata.md) documents the reusable
commands and their limits.

## Prepublication handoff package

Provide a runnable acceptance entry, short user checklist, frozen candidate path/
version/commit or SHA256, feature matrix, all checks and independent review results,
licenses/SBOM/secret-scan reports, backup/recovery instructions and exact release
targets, commands, order and release-time checks. Read-only check existing targets
early. Mark absent-target or genuinely post-publication state unverified and supply
the steps; do not skip other pre-executable gates. Local candidate commits and
dedicated private test-repository operations may follow explicit preparation-Goal
authority; they do not constitute product publication.
