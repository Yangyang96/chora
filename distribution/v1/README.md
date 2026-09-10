# Chora offline release assets v1

This directory is the deterministic source layout for the two image artifacts
used by four independently policy-bound roles:

| Role | Artifact | Sharing contract |
| --- | --- | --- |
| `managed_pi_runtime` | `managed-pi-runtime` | primary |
| `network_boundary` | `network-boundary` | primary |
| `independent_verifier` | `managed-pi-runtime` | explicit alias of `managed_pi_runtime` |
| `capability_probe` | `managed-pi-runtime` | explicit alias of `managed_pi_runtime` |

`release-spec.v1.json` binds every recipe, complete build context, role policy,
license inventory, SBOM, Pi package integrity, Node identity, and configured
image entrypoint by SHA-256. The context digest algorithm is implemented by
`internal/releaseassets.DirectoryDigest`.

The installable release manifest is generated locally from an offline archive
resolution. Each sorted artifact entry has exactly this acquisition identity:

```json
{
  "artifact_id": "managed-pi-runtime",
  "local_docker_config_image_id": "sha256:<64 lowercase hex>",
  "archive": {
    "format": "docker-archive",
    "path": "<release-root-relative path>",
    "size": 12345,
    "sha256": "<64 lowercase hex>"
  }
}
```

Archive paths must themselves be sorted and unique. Every archive must be an
owner-controlled, non-group/world-writable, single-link regular file below the
release root with no symlink ancestor. Validation pins its inode, byte length,
and SHA-256; rejects path escape, symlink/hardlink, duplicate members, tags,
extra images/configs, and incomplete layers; and proves the exact config
filename/content ID and config platform are `linux/arm64`.

Release preparation creates a new standalone release root outside this source
tree, exports the two exact local image IDs into its `archives/` directory as
tagless Docker archives, and writes their content-pinned resolution there. The
release-assets tool then verifies source inputs against the repository, archive
inputs against the release root, copies the exact manifest-bound source
closure into that root, and writes the manifest last:

```sh
go run ./tools/releaseassets verify-spec
go run ./tools/releaseassets generate \
  -source-root . \
  -release-root /absolute/path/to/chora-m1-alpha \
  -resolution offline-resolution.v1.json \
  -out release-manifest.v1.json
go run ./tools/releaseassets verify-manifest \
  -release-root /absolute/path/to/chora-m1-alpha \
  -manifest release-manifest.v1.json
```

These commands do not export images and perform no build, pull, load,
publication, or network operation. They validate only already-present local
bytes and create a portable closure plus an exclusive, read-only manifest.
The O4 acceptance materializer consumes those pre-exported archives and the
generated release manifest to form the remaining owner-private execution
closure; it never claims to have exported Docker bytes. Setup is the only phase
allowed to load a validated archive; Attempt, Retry, Restart, Recovery, and
Verifier do not acquire images.

The Dockerfiles remain release-construction recipes. Their immutable OCI base
image pull references are build-time recipe provenance only. They are never
Setup or Attempt acquisition authority.
