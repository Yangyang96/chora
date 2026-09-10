import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { chmod, mkdtemp, mkdir, readFile, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'

import { auditDisclosureBoundary, assertNoCredentialLeakInTree } from './disclosure-gate.mjs'

test('disclosure gate admits only exact Chora content and checks actual OAuth scalars', async () => {
  const fixture = await disclosureFixture()
  const result = await auditDisclosureBoundary(fixture.input)
  assert.equal(result.status, 'passed')
  assert.equal(result.sourceFiles, 9)
  assert.equal(result.installOnlyFiles, 4)
  assert.equal(result.modelReadableFiles, 5)
  assert.equal(result.vendorRuntimeDependencyFiles, 1)
  assert.equal(result.actualOAuthCredentialScalarsChecked, 3)
  assert.equal(result.actualOAuthCredentialLeakFiles, 0)
  assert.equal(result.nonPlaceholderPrivacyMatches, 0)
  assert.equal(result.modelReadablePrivateHomePathOccurrences, 0)

  const residue = join(fixture.root, 'data')
  await mkdir(residue)
  await writeFile(join(residue, 'safe.db'), 'no credential here')
  const scan = await assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile })
  assert.equal(scan.actualOAuthCredentialLeakFiles, 0)
  assert.equal(scan.genericSecretRuleMatches, 0)
})

for (const [name, path, content, pattern] of [
  ['actual OAuth value', 'internal/leak.go', 'package leak\nconst value = "test-access-credential"\n', /actual OAuth credential value/],
  ['private key', 'internal/leak.go', ['-----BEGIN ', 'PRIVATE KEY-----\n'].join(''), /private_key material/],
  ['privacy email', 'internal/leak.go', `package leak\n// ${['person', 'company.example'].join('@')}\n`, /non-placeholder email/],
  ['private home path', 'internal/leak.go', 'package leak\n// /Users/alice/private.go\n', /private home path/],
  ['enterprise marker', 'internal/leak.go', `package leak\n// copied from ${['ali', 'pay'].join('')} service\n`, /unrelated enterprise marker/],
  ['sensitive filename', 'internal/.env', 'TOKEN=not-a-real-token\n', /sensitive filename/],
]) {
  test(`disclosure gate fails closed on ${name}`, async () => {
    const fixture = await disclosureFixture({ path, content })
    await assert.rejects(() => auditDisclosureBoundary(fixture.input), pattern)
  })
}

test('disclosure gate permits only placeholder and local historical home paths while keeping them off the model-readable surface', async () => {
  const fixture = await disclosureFixture({
    path: 'internal/placeholder_test.go',
    content: 'package safe\n// /Users/example/private.txt\n',
  })
  const result = await auditDisclosureBoundary(fixture.input)
  assert.equal(result.localOnlyHistoricalHomePathOccurrences, 3)
  assert.equal(result.placeholderHomePathOccurrences, 1)
  assert.equal(result.modelReadablePrivateHomePathOccurrences, 0)
})

test('disclosure gate independently rejects aggregate, order, and entry tamper', async t => {
  await t.test('aggregate', async () => {
    const fixture = await disclosureFixture()
    const manifest = JSON.parse(await readFile(fixture.manifestPath, 'utf8'))
    manifest.aggregate_sha256 = '0'.repeat(64)
    await writeFile(fixture.manifestPath, `${JSON.stringify(manifest)}\n`)
    fixture.input.expectedAggregate = manifest.aggregate_sha256
    await assert.rejects(() => auditDisclosureBoundary(fixture.input), /source manifest aggregate mismatch/)
  })
  await t.test('order', async () => {
    const fixture = await disclosureFixture()
    const manifest = JSON.parse(await readFile(fixture.manifestPath, 'utf8'))
    manifest.files.reverse()
    await writeFile(fixture.manifestPath, `${JSON.stringify(manifest)}\n`)
    await assert.rejects(() => auditDisclosureBoundary(fixture.input), /source manifest paths are not sorted/)
  })
  await t.test('entry', async () => {
    const fixture = await disclosureFixture()
    const manifest = JSON.parse(await readFile(fixture.manifestPath, 'utf8'))
    manifest.files.find(entry => entry.path === 'internal/safe.go').sha256 = '0'.repeat(64)
    refreshManifestIdentities(manifest, fixture.policy)
    fixture.input.expectedAggregate = manifest.aggregate_sha256
    await writeFile(fixture.manifestPath, `${JSON.stringify(manifest)}\n`)
    await assert.rejects(() => auditDisclosureBoundary(fixture.input), /disclosed file digest drift: internal\/safe\.go/)
  })
})

test('disclosure gate rejects an innocuous unmanifested file inside source scope', async () => {
  const fixture = await disclosureFixture()
  await writeFile(join(fixture.sourceRoot, 'internal/undeclared.txt'), 'innocuous\n')
  await assert.rejects(() => auditDisclosureBoundary(fixture.input), /unmanifested or missing files/)
})

test('disclosure gate rejects npm authentication fields after content identity passes', async () => {
  const fixture = await disclosureFixture({
    path: '.npmrc',
    content: 'engine-strict=true\nregistry=https://registry.npmjs.org/\n//registry.npmjs.org/:_authToken=secret\n',
  })
  await assert.rejects(() => auditDisclosureBoundary(fixture.input), /npm configuration contains an unsafe or unknown field/)
})

test('disclosure gate verifies vendor and historical residue stay outside the model-readable projection', async () => {
  const fixture = await disclosureFixture()
  const manifest = JSON.parse(await readFile(fixture.manifestPath, 'utf8'))
  assert.equal(manifest.install_only_projection.files, 4)
  assert.equal(manifest.model_readable_projection.files, 5)
  manifest.model_readable_projection.aggregate_sha256 = 'f'.repeat(64)
  await writeFile(fixture.manifestPath, `${JSON.stringify(manifest)}\n`)
  await assert.rejects(() => auditDisclosureBoundary(fixture.input), /model-readable projection identity mismatch/)
})

test('credential residue scan fails without printing the credential', async () => {
  const fixture = await disclosureFixture()
  const residue = join(fixture.root, 'data-leak')
  await mkdir(residue)
  await writeFile(join(residue, 'runtime.db'), 'prefix test-access-credential suffix')
  await assert.rejects(() => assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile }), /persisted file: runtime\.db/)
})

for (const [rule, material] of [
  ['private_key', ['-----BEGIN ', 'PRIVATE KEY-----'].join('')],
  ['openai_key', ['sk-', 'S'.repeat(24)].join('')],
  ['github_token', ['ghp_', 'G'.repeat(32)].join('')],
  ['aws_access_key', ['AKIA', 'A'.repeat(16)].join('')],
  ['google_api_key', ['AIza', 'Z'.repeat(32)].join('')],
  ['slack_token', ['xoxb-', '7'.repeat(24)].join('')],
  ['jwt', ['eyJ', 'a'.repeat(12), '.', 'b'.repeat(12), '.', 'c'.repeat(12)].join('')],
]) {
  test(`credential residue scan rejects ${rule} material outside frozen Baseline vendor without echoing it`, async () => {
    const fixture = await disclosureFixture()
    const residue = join(fixture.root, `data-${rule}`)
    await mkdir(residue)
    await writeFile(join(residue, 'runtime.db'), `synthetic-prefix ${material} synthetic-suffix\n`)
    await assertRejectsWithoutMaterial(
      () => assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile }),
      new RegExp(`${rule} material leaked into persisted file: runtime\\.db`),
      material,
    )
  })
}

test('sensitive residue scan fails closed on private paths without echoing them', async () => {
  const fixture = await disclosureFixture()
  const residue = join(fixture.root, 'data-private')
  await mkdir(residue)
  await writeFile(join(residue, 'runtime.db'), 'private path /Users/alice/enterprise/project')
  await assert.rejects(() => assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile }), /private home path leaked into persisted file: runtime\.db/)
})

test('residue scan permits only exact frozen-baseline placeholders and registered local Chora history', async () => {
  const fixture = await disclosureFixture()
  const residue = join(fixture.root, 'data-safe-history')
  const testFile = join(residue, 'runtime/verification/baseline-v5/internal/agent/pi/adapter_test.go')
  const historicalFile = join(residue, 'runtime/verification/baseline-v5/web/src/App.tsx')
  await mkdir(dirname(testFile), { recursive: true })
  await mkdir(dirname(historicalFile), { recursive: true })
  await writeFile(testFile, 'const placeholder = "/Users/example/private.txt"\nconst user = "/home/bob/test.txt"\n')
  await writeFile(historicalFile, 'const historical = "/Users/build-user/chora"\n')
  const result = await assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile })
  assert.equal(result.placeholderHomePathOccurrences, 2)
  assert.equal(result.localOnlyHistoricalHomePathOccurrences, 1)
  assert.equal(result.privateHomePathLeakFiles, 0)
})

test('residue scan treats generic privacy metadata as local-only only under the exact frozen Baseline vendor prefix', async () => {
  const fixture = await disclosureFixture()
  const residue = join(fixture.root, 'data-vendor-metadata')
  const license = join(residue, 'runtime/verification/baseline-v5/vendor/example.org/module/LICENSE')
  await mkdir(dirname(license), { recursive: true })
  await writeFile(license, `Upstream author <${['author', 'upstream.example'].join('@')}>\n` +
    `Built at /Users/alice/source for ${['ali', 'pay'].join('')} compatibility\n`)
  const result = await assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile })
  assert.equal(result.frozenBaselineVendorFilesScanned, 1)
  assert.equal(result.actualOAuthCredentialLeakFiles, 0)
})

test('residue scan preserves the exact frozen Baseline v4 vendor compatibility prefix', async () => {
  const fixture = await disclosureFixture()
  const residue = join(fixture.root, 'data-v4-vendor-metadata')
  const license = join(residue, 'runtime/verification/baseline-v4/vendor/example.org/module/LICENSE')
  await mkdir(dirname(license), { recursive: true })
  await writeFile(license, `Upstream author <${['author', 'upstream.example'].join('@')}>\n`)
  const result = await assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile })
  assert.equal(result.frozenBaselineVendorFilesScanned, 1)
})

test('residue scan keeps exact OAuth byte comparison active inside frozen Baseline vendor', async () => {
  const fixture = await disclosureFixture()
  const residue = join(fixture.root, 'data-vendor-credential')
  const license = join(residue, 'runtime/verification/baseline-v5/vendor/example.org/module/LICENSE')
  await mkdir(dirname(license), { recursive: true })
  const material = 'test-access-credential'
  await writeFile(license, `upstream text ${material} suffix\n`)
  await assertRejectsWithoutMaterial(
    () => assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile }),
    /actual OAuth credential leaked into persisted file: runtime\/verification\/baseline-v5\/vendor\/example\.org\/module\/LICENSE/,
    material,
  )
})

test('residue scan rejects symlinks inside frozen Baseline vendor', async () => {
  const fixture = await disclosureFixture()
  const residue = join(fixture.root, 'data-vendor-symlink')
  const vendorRoot = join(residue, 'runtime/verification/baseline-v5/vendor')
  await mkdir(vendorRoot, { recursive: true })
  await symlink(fixture.authFile, join(vendorRoot, 'LICENSE'))
  await assert.rejects(
    () => assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile }),
    /symlink is forbidden during credential residue scan: runtime\/verification\/baseline-v5\/vendor\/LICENSE/,
  )
})

test('residue scan does not broaden the vendor exception to adjacent prefixes', async () => {
  const fixture = await disclosureFixture()
  const residue = join(fixture.root, 'data-vendor-near-miss')
  const license = join(residue, 'runtime/verification/baseline-v4/vendor-copy/example.org/LICENSE')
  await mkdir(dirname(license), { recursive: true })
  await writeFile(license, `Upstream author <${['author', 'upstream.example'].join('@')}>\n`)
  await assert.rejects(
    () => assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile }),
    /non-placeholder email leaked into persisted file: runtime\/verification\/baseline-v4\/vendor-copy\/example\.org\/LICENSE/,
  )
})

test('residue scan exempts generic-secret rules only under the exact frozen Baseline vendor prefix', async () => {
  const fixture = await disclosureFixture()
  const residue = join(fixture.root, 'data-vendor-secret-prefix')
  const material = ['sk-', 'V'.repeat(24)].join('')
  const exact = join(residue, 'runtime/verification/baseline-v5/vendor/example.org/module/LICENSE')
  await mkdir(dirname(exact), { recursive: true })
  await writeFile(exact, `synthetic upstream fixture ${material}\n`)
  const result = await assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile })
  assert.equal(result.frozenBaselineVendorFilesScanned, 1)
  assert.equal(result.genericSecretRuleMatches, 0)

  const adjacent = join(residue, 'runtime/verification/baseline-v4/vendor-copy/example.org/module/LICENSE')
  await mkdir(dirname(adjacent), { recursive: true })
  await writeFile(adjacent, `synthetic adjacent fixture ${material}\n`)
  await assertRejectsWithoutMaterial(
    () => assertNoCredentialLeakInTree({ root: residue, authFile: fixture.authFile }),
    /openai_key material leaked into persisted file: runtime\/verification\/baseline-v4\/vendor-copy\/example\.org\/module\/LICENSE/,
    material,
  )
})

async function assertRejectsWithoutMaterial(operation, pattern, material) {
  let failure
  try {
    await operation()
  } catch (error) {
    failure = error
  }
  assert(failure instanceof Error, 'operation unexpectedly succeeded')
  assert.match(failure.message, pattern)
  assert.equal(failure.message.includes(material), false)
}

async function disclosureFixture(extra) {
  const root = await mkdtemp(join(tmpdir(), 'chora-disclosure-'))
  const bundleRoot = join(root, 'bundle')
  const sourceRoot = join(bundleRoot, 'source')
  const authFile = join(root, 'auth.json')
  await mkdir(join(sourceRoot, 'internal'), { recursive: true })
  await writeFile(authFile, JSON.stringify({
    'openai-codex': { access: 'test-access-credential', refresh: 'test-refresh-credential', accountId: 'acct-test' },
  }), { mode: 0o600 })
  await chmod(authFile, 0o600)
  const files = new Map([
    ['.npmrc', 'engine-strict=true\nregistry=https://registry.npmjs.org/\n'],
    ['contracts/g2-m4/source-baseline-v4/delta/web/src/App.tsx', 'const historical = "/Users/build-user/chora"\n'],
    ['contracts/g2-m4/source-baseline-v5/delta/web/src/App.tsx', 'const historical = "/Users/build-user/chora"\n'],
    ['contracts/g2-m4/source-baseline-v6/delta/web/src/App.tsx', 'const historical = "/Users/build-user/chora"\n'],
    ['go.mod', 'module github.com/Yangyang96/chora\n\ngo 1.26.5\n'],
    ['package.json', '{"name":"chora","private":true}\n'],
    ['internal/safe.go', 'package safe\n// chora@example.test is a test-only placeholder.\n'],
    ['vendor/example.org/module/LICENSE', 'fixture vendor metadata\n'],
  ])
  if (extra) files.set(extra.path, extra.content)
  const policyPath = 'distribution/v1/policies/source-path-policy.v1.json'
  files.set(policyPath, '')
  const policy = {
    schema_version: 'chora.source-path-policy.v1',
    roots: ['.npmrc', 'contracts', 'distribution', 'go.mod', 'internal', 'package.json', 'vendor'],
    excluded_directory_names: ['.vite', 'coverage', 'dist', 'node_modules', 'test-results'],
    excluded_file_suffixes: ['.tsbuildinfo'],
    declared_files: 0,
    declared_paths_sha256: '0'.repeat(64),
    install_only_roots: ['vendor'],
    install_only_paths: [
      'contracts/g2-m4/source-baseline-v4/delta/web/src/App.tsx',
      'contracts/g2-m4/source-baseline-v5/delta/web/src/App.tsx',
      'contracts/g2-m4/source-baseline-v6/delta/web/src/App.tsx',
    ],
    install_only_files: 0,
    install_only_paths_sha256: '0'.repeat(64),
    model_readable_files: 0,
    model_readable_paths_sha256: '0'.repeat(64),
  }
  const paths = [...files.keys()].sort(compareStrings)
  const installOnlyPaths = paths.filter(path => isInstallOnly(policy, path))
  const modelReadablePaths = paths.filter(path => !isInstallOnly(policy, path))
  policy.declared_files = paths.length
  policy.declared_paths_sha256 = pathSetDigest(paths)
  policy.install_only_files = installOnlyPaths.length
  policy.install_only_paths_sha256 = pathSetDigest(installOnlyPaths)
  policy.model_readable_files = modelReadablePaths.length
  policy.model_readable_paths_sha256 = pathSetDigest(modelReadablePaths)
  files.set(policyPath, `${JSON.stringify(policy, null, 2)}\n`)
  for (const [path, body] of files) {
    const target = join(sourceRoot, ...path.split('/'))
    await mkdir(dirname(target), { recursive: true })
    await writeFile(target, body)
  }
  const entries = [...files].map(([path, body]) => ({
    path, size: Buffer.byteLength(body), mode: '0644', sha256: createHash('sha256').update(body).digest('hex'),
  })).sort((left, right) => compareStrings(left.path, right.path))
  const manifest = {
    schema_version: 'chora.local-alpha-source-bundle.v1',
    aggregate_sha256: aggregateDigest(entries),
    source_path_policy: { path: policyPath, sha256: createHash('sha256').update(files.get(policyPath)).digest('hex') },
    install_only_projection: projectionIdentity(entries.filter(entry => isInstallOnly(policy, entry.path))),
    model_readable_projection: projectionIdentity(entries.filter(entry => !isInstallOnly(policy, entry.path))),
    files: entries,
  }
  const manifestPath = join(bundleRoot, 'source-manifest.json')
  await writeFile(manifestPath, `${JSON.stringify(manifest)}\n`)
  return {
    root, authFile, bundleRoot, sourceRoot, manifestPath, policy,
    input: { bundleRoot, manifestPath, expectedAggregate: manifest.aggregate_sha256, authFile },
  }
}

function refreshManifestIdentities(manifest, policy) {
  const sorted = [...manifest.files].sort((left, right) => compareStrings(left.path, right.path))
  manifest.aggregate_sha256 = aggregateDigest(sorted)
  manifest.install_only_projection = projectionIdentity(sorted.filter(entry => isInstallOnly(policy, entry.path)))
  manifest.model_readable_projection = projectionIdentity(sorted.filter(entry => !isInstallOnly(policy, entry.path)))
}

function projectionIdentity(entries) {
  return {
    files: entries.length,
    paths_sha256: pathSetDigest(entries.map(entry => entry.path)),
    aggregate_sha256: aggregateDigest(entries),
  }
}

function aggregateDigest(entries) {
  const hash = createHash('sha256')
  for (const entry of entries) {
    hash.update(entry.path)
    hash.update('\0')
    hash.update(entry.mode)
    hash.update('\0')
    hash.update(String(entry.size))
    hash.update('\0')
    hash.update(entry.sha256)
    hash.update('\n')
  }
  return hash.digest('hex')
}

function pathSetDigest(paths) {
  return createHash('sha256').update(paths.map(path => `${path}\n`).join('')).digest('hex')
}

function isInstallOnly(policy, path) {
  return policy.install_only_paths.includes(path) ||
    policy.install_only_roots.some(root => path === root || path.startsWith(`${root}/`))
}

function compareStrings(left, right) { return left < right ? -1 : left > right ? 1 : 0 }
