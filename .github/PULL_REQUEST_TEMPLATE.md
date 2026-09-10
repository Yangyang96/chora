## Outcome

Describe the user-visible or repository outcome.

## Scope and boundaries

- What changed?
- What intentionally did not change?
- Which product or security boundaries are relevant?

## Verification

List exact commands and results. For public-source changes, include:

```sh
npm run publication:test
npm run publication:check
make public-test
git diff --check
```

## Safety checklist

- [ ] I read `CONTRIBUTING.md` and `docs/publication-scope.md`.
- [ ] I have the right to submit this contribution under AGPL-3.0-only.
- [ ] I did not add credentials, private assets, personal paths, confidential data, raw evidence, vendored dependencies, or unreviewed experiments.
- [ ] I added or updated tests for observable behavior where applicable.
- [ ] I preserved fail-closed managed execution and did not add silent host fallback.
