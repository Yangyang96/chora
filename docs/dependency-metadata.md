# Dependency metadata preflight

[English](dependency-metadata.md) | [简体中文](dependency-metadata.zh-CN.md) |
[Documentation index](README.md)

This preflight keeps future SBOM work executable while Chora's product and
dependency graph are still changing. It validates recipes in memory and does
not write or approve a final SBOM or third-party license notice.

## Current development identity

The private root npm workspace uses version `0.0.0` so standards-based package
identifiers can be generated. This is an unreleased development placeholder,
not Chora's first public version. `private: true` continues to prevent accidental
npm publication, and the license metadata remains `AGPL-3.0-only`.

## Run the preflight

```sh
npm run dependency-metadata:test
npm run dependency-metadata:check
```

The check performs no dependency installation, disables Go proxy and sumdb
access, and sets `GOWORK=off` so an ambient parent or external workspace cannot
change the inventory. It:

- asks the installed npm CLI to build an in-memory CycloneDX application SBOM
  from `package-lock.json`;
- requires the root package identity `chora@0.0.0` and
  `pkg:npm/chora@0.0.0`;
- builds a read-only Go module inventory from the local module cache and
  committed module files; and
- prints counts and schema identity only, not the full candidate-derived
  documents.

This is not the final cross-ecosystem SBOM. After candidate freeze, maintainers
must select and pin the final generator, cover both npm and Go dependencies,
collect the applicable license texts/notices, review exceptions, and retain
outputs bound to the exact candidate digest.

## Public candidate dry run

```sh
npm run publication:export:test
npm run publication:export:dry-run
```

The dry run copies the checked candidate through descriptor-relative,
no-follow writes into a private staging tree, publishes the target exclusively,
verifies that the path set and source bytes/modes remain stable during export,
computes a deterministic SHA-256 content identity, and removes the temporary
directory.
For a retained inspection copy, call the exporter with a new target whose
parent already exists:

```sh
node e2e/publication-candidate-export.mjs /absolute/new/candidate-directory
```

The exporter refuses an existing target and any target inside the source
repository. A successful dry run is preparation evidence, not final candidate
acceptance or publication authorization.
