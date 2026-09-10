# `releaseassets`

`releaseassets` validates and materializes Chora's local offline release
manifest. It does not call Docker and has no network or Registry operation.

- `digest-context` computes the deterministic build-context digest.
- `verify-spec` validates source recipes, policies, licenses, SBOMs, and build
  provenance.
- `generate` keeps the source repository and standalone release root separate.
  It consumes a complete offline Docker archive resolution from the release
  root, validates every archive, copies the exact manifest-bound policy,
  recipe, provenance, license, SBOM, and context closure into that root, and
  writes one new manifest without overwriting an existing path.
- `verify-manifest` revalidates the manifest, archives, and copied source
  closure using only the standalone release root.

The resolution schema is `chora.offline-release-assets-resolution.v1`. Its
artifact order must exactly match the release spec. Its archive paths must be
sorted, unique, release-root-relative, and use format `docker-archive`.

Tests create tiny deterministic tagless Docker archives locally. Those fixtures
exercise the same config-ID, platform, layer-closure, path, ownership, size, and
SHA-256 checks used for real release bytes; they are not release evidence.
