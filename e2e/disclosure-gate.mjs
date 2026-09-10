import { createHash } from 'node:crypto'
import { lstat, readFile, readdir } from 'node:fs/promises'
import { join, posix, relative, resolve, sep } from 'node:path'

const sourcePathPolicyRelativePath = 'distribution/v1/policies/source-path-policy.v1.json'
const sourcePathPolicySchema = 'chora.source-path-policy.v1'

const forbiddenTopLevel = new Set([
  '.agent', '.git', '.playwright-cli', '.vscode', 'node_modules', 'output', 'spikes',
])

const sensitivePath = /(^|\/)(?:\.env(?:\..*)?|auth\.json|credentials?(?:\.[^/]*)?|id_(?:rsa|ed25519)|[^/]+\.(?:key|p12|pfx))$/i
const homePath = /\/(?:Users|home)\/[^/\s]+\//g
const universalPlaceholderHomePaths = new Set([
  ['Users', 'example'], ['home', 'example'],
].map(([root, user]) => `/${root}/${user}/`))
const testPlaceholderHomePaths = new Set([
  ['Users', 'alice'], ['home', 'bob'], ['Users', 'build-user'], ['Users', 'device-owner'],
].map(([root, user]) => `/${root}/${user}/`))
const localOnlyHistoricalResiduePaths = new Set([
  'runtime/verification/baseline-v4/web/src/App.tsx',
  'runtime/verification/baseline-v5/web/src/App.tsx',
  'runtime/verification/baseline-v6/web/src/App.tsx',
])
const frozenBaselineVendorResiduePrefixes = [
  'runtime/verification/baseline-v4/vendor/',
  'runtime/verification/baseline-v5/vendor/',
  'runtime/verification/baseline-v6/vendor/',
]
const emailAddress = /[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g
const allowedPlaceholderEmails = new Set(['chora@example.test', 'top-secret@evil.example'])
const enterpriseNames = [
  ['ant', 'group'].join(''), ['ali', 'pay'].join(''), ['ali', 'baba'].join(''), ['tao', 'bao'].join(''),
  ['蚂蚁', '集团'].join(''), ['支付', '宝'].join(''), ['阿里', '巴巴'].join(''), ['淘', '宝'].join(''),
]
const unrelatedEnterpriseMarker = new RegExp(`(?:^|[^A-Za-z])(?:${enterpriseNames.join('|')})(?:[^A-Za-z]|$)`, 'i')

const secretRules = [
  ['private_key', /-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----/],
  ['openai_key', /\bsk-[A-Za-z0-9_-]{20,}\b/],
  ['github_token', /\bgh[pousr]_[A-Za-z0-9]{30,}\b/],
  ['aws_access_key', /\bAKIA[0-9A-Z]{16}\b/],
  ['google_api_key', /\bAIza[0-9A-Za-z_-]{30,}\b/],
  ['slack_token', /\bxox[baprs]-[0-9A-Za-z-]{20,}\b/],
  ['jwt', /\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b/],
]

export async function auditDisclosureBoundary({ bundleRoot, manifestPath, expectedAggregate, authFile }) {
  const root = exactAbsolute(bundleRoot, 'bundleRoot')
  const manifestFile = exactAbsolute(manifestPath, 'manifestPath')
  const auth = exactAbsolute(authFile, 'authFile')
  const sourceRoot = join(root, 'source')
  assert(manifestFile === join(root, 'source-manifest.json'), 'source manifest is outside the exact bundle root')
  assert(!insideOrSame(root, auth), 'OAuth source must remain outside the disclosure bundle')

  const manifestBytes = await readFile(manifestFile)
  const manifest = strictJSON(manifestBytes, 'source manifest')
  validateManifestDocument(manifest)
  assert(manifest.schema_version === 'chora.local-alpha-source-bundle.v1', 'source manifest schema drift')
  assert(lowerDigest(expectedAggregate) === expectedAggregate, 'expected bundle aggregate is invalid')
  assert(manifest.aggregate_sha256 === expectedAggregate, 'source manifest aggregate drift')
  assert(Array.isArray(manifest.files) && manifest.files.length > 0, 'source manifest has no files')

  const policyFile = join(sourceRoot, ...sourcePathPolicyRelativePath.split('/'))
  const policyBytes = await readFile(policyFile)
  const policy = strictJSON(policyBytes, 'source path policy')
  validateSourcePathPolicy(policy)
  assert(manifest.source_path_policy.path === sourcePathPolicyRelativePath,
    'source manifest policy path drift')
  assert(manifest.source_path_policy.sha256 === sha256(policyBytes),
    'source manifest policy digest drift')

  for (const entry of manifest.files) validateManifestEntry(entry)
  const sortedEntries = [...manifest.files].sort((left, right) => compareStrings(left.path, right.path))
  for (let index = 0; index < manifest.files.length; index++) {
    assert(manifest.files[index].path === sortedEntries[index].path, 'source manifest paths are not sorted')
  }
  assert(aggregateEntries(sortedEntries) === manifest.aggregate_sha256, 'source manifest aggregate mismatch')

  const declaredPaths = sortedEntries.map(entry => entry.path)
  const { installOnlyPaths, modelReadablePaths } = assertPolicyCoverage(policy, declaredPaths)
  const installOnlyPathSet = new Set(installOnlyPaths)
  const installOnlyEntries = sortedEntries.filter(entry => installOnlyPathSet.has(entry.path))
  const modelReadableEntries = sortedEntries.filter(entry => !installOnlyPathSet.has(entry.path))
  assertProjectionIdentity(manifest.install_only_projection, installOnlyEntries, 'install-only')
  assertProjectionIdentity(manifest.model_readable_projection, modelReadableEntries, 'model-readable')

  const credentialBuffers = await readCredentialBuffers(auth)
  const manifestPaths = new Set()
  const topLevelCounts = {}
  let vendorFiles = 0
  let placeholderEmailOccurrences = 0
  let placeholderHomePathOccurrences = 0
  let localOnlyHistoricalHomePathOccurrences = 0
  try {
    for (const entry of sortedEntries) {
      validateManifestEntry(entry)
      assert(!manifestPaths.has(entry.path), `duplicate disclosed path: ${entry.path}`)
      manifestPaths.add(entry.path)
      const top = entry.path.split('/')[0]
      assert(policy.roots.includes(top) && !forbiddenTopLevel.has(top), `path is outside the Chora disclosure policy: ${entry.path}`)
      assert(!sensitivePath.test(entry.path), `sensitive filename is forbidden from disclosure: ${entry.path}`)
      topLevelCounts[top] = (topLevelCounts[top] ?? 0) + 1
      if (top === 'vendor') vendorFiles++

      const fullPath = resolve(sourceRoot, ...entry.path.split('/'))
      assert(insideOrSame(sourceRoot, fullPath) && fullPath !== sourceRoot, `manifest path escaped source root: ${entry.path}`)
      const info = await lstat(fullPath)
      assert(info.isFile() && !info.isSymbolicLink(), `disclosed entry is not one regular file: ${entry.path}`)
      assert((info.mode & 0o777).toString(8).padStart(4, '0') === entry.mode,
        `disclosed file mode drift: ${entry.path}`)
      const body = await readFile(fullPath)
      try {
        assert(body.length === entry.size, `disclosed file size drift: ${entry.path}`)
        assert(sha256(body) === entry.sha256, `disclosed file digest drift: ${entry.path}`)
        for (const credential of credentialBuffers) {
          assert(!body.includes(credential), `actual OAuth credential value appears in disclosure file: ${entry.path}`)
        }
        const text = body.toString('utf8')
        if (installOnlyPathSet.has(entry.path)) {
          if (policy.install_only_paths.includes(entry.path)) {
            localOnlyHistoricalHomePathOccurrences += [...text.matchAll(homePath)].length
          }
        } else {
          for (const [rule, pattern] of secretRules) assert(!pattern.test(text), `${rule} material appears in disclosure file: ${entry.path}`)
          for (const match of text.matchAll(homePath)) {
            if (universalPlaceholderHomePaths.has(match[0]) ||
                testPlaceholderHomePaths.has(match[0]) && isTestFixturePath(entry.path)) {
              placeholderHomePathOccurrences++
              continue
            }
            assert(false, `private home path appears in model-readable Chora disclosure file: ${entry.path}`)
          }
          assert(!unrelatedEnterpriseMarker.test(text), `unrelated enterprise marker appears in Chora-owned disclosure file: ${entry.path}`)
          for (const match of text.matchAll(emailAddress)) {
            placeholderEmailOccurrences++
            assert(allowedPlaceholderEmails.has(match[0]), `non-placeholder email appears in Chora-owned disclosure file: ${entry.path}`)
          }
        }
      } finally {
        body.fill(0)
      }
    }
    await assertExactSourceTree(sourceRoot, manifestPaths)
    await assertChoraIdentity(sourceRoot)
    await assertSafeNPMRC(join(sourceRoot, '.npmrc'))
  } finally {
    manifestBytes.fill(0)
    policyBytes.fill(0)
    for (const credential of credentialBuffers) credential.fill(0)
  }

  return {
    schemaVersion: 'chora.u3-disclosure-audit.v1',
    status: 'passed',
    aggregateSha256: expectedAggregate,
    manifestSha256: sha256(await readFile(manifestFile)),
    sourceFiles: manifestPaths.size,
    choraOwnedFiles: modelReadableEntries.length,
    installOnlyFiles: installOnlyEntries.length,
    modelReadableFiles: modelReadableEntries.length,
    installOnlyAggregateSha256: manifest.install_only_projection.aggregate_sha256,
    modelReadableAggregateSha256: manifest.model_readable_projection.aggregate_sha256,
    vendorRuntimeDependencyFiles: vendorFiles,
    topLevelCounts: Object.fromEntries(Object.entries(topLevelCounts).sort(([left], [right]) => left.localeCompare(right))),
    actualOAuthCredentialScalarsChecked: credentialBuffers.length,
    actualOAuthCredentialLeakFiles: 0,
    genericSecretRuleMatches: 0,
    nonPlaceholderPrivacyMatches: 0,
    placeholderHomePathOccurrences,
    localOnlyHistoricalHomePathOccurrences,
    modelReadablePrivateHomePathOccurrences: 0,
    placeholderEmailOccurrences,
    unrelatedEnterpriseMarkerMatches: 0,
    forbiddenPathMatches: 0,
    disclosurePolicy: {
      approved: 'Chora project Task, Plan, selected Room context, Chora source needed by the bounded change, diagnostics, and Patch',
      prohibited: 'credentials, secrets, personal information, non-Chora content, enterprise-project code, vendor metadata, and unrelated host content',
      vendor: 'present only for the pinned offline build; model inspection or disclosure is prohibited',
      privacyProjection: 'historical Chora inputs remain local; every absolute home path is removed from the model workspace and frozen prompt before launch',
    },
  }
}

export async function assertNoCredentialLeakInTree({ root, authFile }) {
  const scanRoot = exactAbsolute(root, 'scan root')
  const credentialBuffers = await readCredentialBuffers(exactAbsolute(authFile, 'authFile'))
  let files = 0
  let placeholderHomePathOccurrences = 0
  let localOnlyHistoricalHomePathOccurrences = 0
  let frozenBaselineVendorFilesScanned = 0
  try {
    await walk(scanRoot, async (path, entry) => {
      const relativePath = slash(relative(scanRoot, path))
      assert(!entry.isSymbolicLink(), `symlink is forbidden during credential residue scan: ${relativePath}`)
      if (!entry.isFile()) return
      files++
      const body = await readFile(path)
      try {
        for (const credential of credentialBuffers) {
          assert(!body.includes(credential), `actual OAuth credential leaked into persisted file: ${relativePath}`)
        }
        if (frozenBaselineVendorResiduePrefixes.some(prefix => relativePath.startsWith(prefix))) {
          frozenBaselineVendorFilesScanned++
          return
        }
        const text = body.toString('utf8')
        for (const [rule, pattern] of secretRules) {
          assert(!pattern.test(text), `${rule} material leaked into persisted file: ${relativePath}`)
        }
        for (const match of text.matchAll(homePath)) {
          if (universalPlaceholderHomePaths.has(match[0]) || testPlaceholderHomePaths.has(match[0]) && isTestFixturePath(relativePath)) {
            placeholderHomePathOccurrences++
            continue
          }
          if (localOnlyHistoricalResiduePaths.has(relativePath)) {
            localOnlyHistoricalHomePathOccurrences++
            continue
          }
          assert(false, `private home path leaked into persisted file: ${relativePath}`)
        }
        assert(!unrelatedEnterpriseMarker.test(text), `unrelated enterprise marker leaked into persisted file: ${relativePath}`)
        for (const match of text.matchAll(emailAddress)) {
          assert(allowedPlaceholderEmails.has(match[0]), `non-placeholder email leaked into persisted file: ${relativePath}`)
        }
      } finally {
        body.fill(0)
      }
    })
  } finally {
    for (const credential of credentialBuffers) credential.fill(0)
  }
  return {
    status: 'passed', filesScanned: files, actualOAuthCredentialLeakFiles: 0,
    genericSecretRuleMatches: 0,
    privateHomePathLeakFiles: 0, nonPlaceholderEmailLeakFiles: 0, unrelatedEnterpriseMarkerLeakFiles: 0,
    placeholderHomePathOccurrences, localOnlyHistoricalHomePathOccurrences, frozenBaselineVendorFilesScanned,
  }
}

async function assertExactSourceTree(sourceRoot, manifestPaths) {
  const actual = new Set()
  await walk(sourceRoot, async (path, entry) => {
    const rel = slash(relative(sourceRoot, path))
    assert(!entry.isSymbolicLink(), `source bundle contains a symlink: ${rel}`)
    if (entry.isFile()) actual.add(rel)
  })
  assert(actual.size === manifestPaths.size, 'source tree contains unmanifested or missing files')
  for (const path of actual) assert(manifestPaths.has(path), `source tree contains unmanifested file: ${path}`)
}

async function assertChoraIdentity(sourceRoot) {
  const goMod = await readFile(join(sourceRoot, 'go.mod'), 'utf8')
  const packageDocument = strictJSON(await readFile(join(sourceRoot, 'package.json')), 'package.json')
  assert(/^module github\.com\/Yangyang96\/chora$/m.test(goMod), 'Go module is not Chora')
  assert(packageDocument.name === 'chora' && packageDocument.private === true, 'Node workspace is not private Chora')
}

async function readCredentialBuffers(authFile) {
  const bytes = await readFile(authFile)
  try {
    const document = strictJSON(bytes, 'OAuth source')
    const output = []
    collectCredentialBuffers(document, output)
    assert(output.length > 0, 'OAuth source contains no auditable credential scalar')
    return output
  } finally {
    bytes.fill(0)
  }
}

function collectCredentialBuffers(value, output, key = '') {
  if (Array.isArray(value)) {
    for (const item of value) collectCredentialBuffers(item, output, key)
    return
  }
  if (value && typeof value === 'object') {
    for (const [name, item] of Object.entries(value)) collectCredentialBuffers(item, output, name)
    return
  }
  if (/^(access|refresh|account[_-]?id|user[_-]?id|email|organization|org[_-]?id)$|(?:access|refresh|id)[_-]?token|token|secret|password|cookie|credential|authorization/i.test(key) &&
      typeof value === 'string' && Buffer.byteLength(value) >= 8) output.push(Buffer.from(value))
}

function validateManifestEntry(entry) {
  assert(entry && typeof entry === 'object' && !Array.isArray(entry), 'invalid source manifest entry')
  assertExactKeys(entry, ['mode', 'path', 'sha256', 'size'], 'source manifest entry')
  assert(typeof entry.path === 'string' && entry.path.length > 0 && entry.path === posix.normalize(entry.path) &&
    !entry.path.startsWith('/') && !entry.path.startsWith('../') && !entry.path.includes('\\') && !entry.path.includes('\0'),
  'invalid source manifest path')
  assert(Number.isSafeInteger(entry.size) && entry.size >= 0, `invalid source manifest size: ${entry.path}`)
  assert(lowerDigest(entry.sha256) === entry.sha256, `invalid source manifest digest: ${entry.path}`)
  const mode = typeof entry.mode === 'string' && /^0[0-7]{3}$/.test(entry.mode) ? Number.parseInt(entry.mode, 8) : -1
  assert(mode >= 0 && (mode & 0o400) !== 0 && (mode & 0o022) === 0, `invalid source manifest mode: ${entry.path}`)
}

function validateManifestDocument(manifest) {
  assert(manifest && typeof manifest === 'object' && !Array.isArray(manifest), 'source manifest is not an object')
  assertExactKeys(manifest, [
    'aggregate_sha256', 'files', 'install_only_projection', 'model_readable_projection',
    'schema_version', 'source_path_policy',
  ], 'source manifest')
  assertExactKeys(manifest.source_path_policy, ['path', 'sha256'], 'source path policy identity')
  assert(lowerDigest(manifest.source_path_policy.sha256) === manifest.source_path_policy.sha256,
    'source path policy identity digest is invalid')
}

function validateSourcePathPolicy(policy) {
  assert(policy && typeof policy === 'object' && !Array.isArray(policy), 'source path policy is not an object')
  assertExactKeys(policy, [
    'declared_files', 'declared_paths_sha256', 'excluded_directory_names', 'excluded_file_suffixes',
    'install_only_files', 'install_only_paths', 'install_only_paths_sha256', 'install_only_roots',
    'model_readable_files', 'model_readable_paths_sha256', 'roots', 'schema_version',
  ], 'source path policy')
  assert(policy.schema_version === sourcePathPolicySchema, 'source path policy schema drift')
  assertSortedUniqueStrings(policy.roots, 'source path policy roots', value => validPolicyPath(value) && !value.includes('/'))
  assertSortedUniqueStrings(policy.excluded_directory_names, 'source path policy directory exclusions', validPolicyComponent)
  assertSortedUniqueStrings(policy.excluded_file_suffixes, 'source path policy suffix exclusions', value =>
    typeof value === 'string' && value.startsWith('.') && !value.includes('/') && !value.includes('\0'))
  assertSortedUniqueStrings(policy.install_only_roots, 'install-only roots', value => validPolicyPath(value) && !value.includes('/'))
  assertSortedUniqueStrings(policy.install_only_paths, 'install-only paths', validPolicyPath)
  for (const root of ['.npmrc', 'distribution', 'go.mod', 'package.json', 'vendor']) {
    assert(policy.roots.includes(root), `source path policy omits required root: ${root}`)
  }
  for (const root of policy.install_only_roots) {
    assert(policy.roots.includes(root), `install-only root is outside source policy: ${root}`)
  }
  assert(policy.install_only_roots.includes('vendor'), 'vendor must be classified as install-only')
  for (const field of ['declared_files', 'install_only_files', 'model_readable_files']) {
    assert(Number.isSafeInteger(policy[field]) && policy[field] > 0, `source path policy ${field} is invalid`)
  }
  assert(policy.declared_files === policy.install_only_files + policy.model_readable_files,
    'source path policy classification counts drift')
  for (const field of ['declared_paths_sha256', 'install_only_paths_sha256', 'model_readable_paths_sha256']) {
    assert(lowerDigest(policy[field]) === policy[field], `source path policy ${field} is invalid`)
  }
  assert(pathDeclaredByPolicy(policy, sourcePathPolicyRelativePath) &&
    !pathExcludedByPolicy(policy, sourcePathPolicyRelativePath), 'source path policy does not declare itself')
}

function assertPolicyCoverage(policy, paths) {
  assert(paths.length === policy.declared_files && pathsDigest(paths) === policy.declared_paths_sha256,
    'declared source coverage does not match source path policy')
  for (let index = 0; index < paths.length; index++) {
    assert(index === 0 || compareStrings(paths[index - 1], paths[index]) < 0, 'declared source paths are not unique and sorted')
    assert(pathDeclaredByPolicy(policy, paths[index]) && !pathExcludedByPolicy(policy, paths[index]),
      `source path is outside exact policy: ${paths[index]}`)
  }
  const installOnlyPaths = paths.filter(path => installOnlyPath(policy, path))
  const modelReadablePaths = paths.filter(path => !installOnlyPath(policy, path))
  assert(installOnlyPaths.length === policy.install_only_files &&
    pathsDigest(installOnlyPaths) === policy.install_only_paths_sha256,
  'install-only source coverage does not match source path policy')
  assert(modelReadablePaths.length === policy.model_readable_files &&
    pathsDigest(modelReadablePaths) === policy.model_readable_paths_sha256,
  'model-readable source coverage does not match source path policy')
  return { installOnlyPaths, modelReadablePaths }
}

function assertProjectionIdentity(projection, entries, label) {
  assert(projection && typeof projection === 'object' && !Array.isArray(projection), `${label} projection is invalid`)
  assertExactKeys(projection, ['aggregate_sha256', 'files', 'paths_sha256'], `${label} projection`)
  assert(projection.files === entries.length && projection.paths_sha256 === pathsDigest(entries.map(entry => entry.path)) &&
    projection.aggregate_sha256 === aggregateEntries(entries), `${label} projection identity mismatch`)
}

function pathDeclaredByPolicy(policy, path) {
  return policy.roots.some(root => path === root || path.startsWith(`${root}/`))
}

function pathExcludedByPolicy(policy, path) {
  const components = path.split('/')
  if (components.slice(0, -1).some(component => policy.excluded_directory_names.includes(component))) return true
  return policy.excluded_file_suffixes.some(suffix => path.endsWith(suffix))
}

function installOnlyPath(policy, path) {
  return policy.install_only_paths.includes(path) ||
    policy.install_only_roots.some(root => path === root || path.startsWith(`${root}/`))
}

async function assertSafeNPMRC(path) {
  const body = await readFile(path, 'utf8')
  assert(Buffer.byteLength(body) > 0 && Buffer.byteLength(body) <= 4096 && !body.includes('\0'),
    'npm configuration is malformed')
  const expected = new Map([
    ['engine-strict', 'true'],
    ['registry', 'https://registry.npmjs.org/'],
  ])
  const seen = new Set()
  for (const line of body.split('\n')) {
    if (line === '') continue
    assert(line.trim() === line && !line.startsWith('#') && !line.startsWith(';'),
      'npm configuration contains unsupported syntax')
    const separator = line.indexOf('=')
    assert(separator > 0, 'npm configuration contains an unsafe or unknown field')
    const key = line.slice(0, separator)
    const value = line.slice(separator + 1)
    assert(expected.has(key) && expected.get(key) === value,
      'npm configuration contains an unsafe or unknown field')
    assert(!seen.has(key), 'npm configuration contains a duplicate field')
    seen.add(key)
  }
  assert(seen.size === expected.size, 'npm configuration omits a required safe field')
}

function aggregateEntries(entries) {
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

function pathsDigest(paths) {
  const hash = createHash('sha256')
  for (const path of paths) hash.update(`${path}\n`)
  return hash.digest('hex')
}

function assertSortedUniqueStrings(values, label, validator) {
  assert(Array.isArray(values) && values.length > 0, `${label} is empty`)
  for (let index = 0; index < values.length; index++) {
    assert(validator(values[index]), `${label} contains an invalid value`)
    assert(index === 0 || compareStrings(values[index - 1], values[index]) < 0, `${label} is not unique and sorted`)
  }
}

function validPolicyPath(value) {
  return typeof value === 'string' && value.length > 0 && value === posix.normalize(value) &&
    !value.startsWith('/') && !value.startsWith('../') && !value.includes('\\') && !value.includes('\0')
}

function validPolicyComponent(value) {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..' &&
    !value.includes('/') && !value.includes('\\') && !value.includes('\0')
}

function assertExactKeys(value, expected, label) {
  assert(value && typeof value === 'object' && !Array.isArray(value), `${label} is invalid`)
  const actual = Object.keys(value).sort(compareStrings)
  const wanted = [...expected].sort(compareStrings)
  assert(actual.length === wanted.length && actual.every((key, index) => key === wanted[index]), `${label} fields drift`)
}

function compareStrings(left, right) { return left < right ? -1 : left > right ? 1 : 0 }

async function walk(root, visitor) {
  for (const entry of await readdir(root, { withFileTypes: true })) {
    const path = join(root, entry.name)
    await visitor(path, entry)
    if (entry.isDirectory()) await walk(path, visitor)
  }
}

function strictJSON(bytes, label) {
  let value
  try { value = JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) }
  return value
}

function exactAbsolute(value, label) {
  assert(typeof value === 'string' && value.length > 0 && resolve(value) === value, `${label} must be an absolute normalized path`)
  return value
}

function lowerDigest(value) { return typeof value === 'string' && /^[0-9a-f]{64}$/.test(value) ? value : '' }
function isTestFixturePath(value) {
  return /(?:^|\/)(?:[^/]+_test\.go|[^/]+\.(?:test|spec)\.(?:mjs|js|ts|tsx))$/.test(value)
}
function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function slash(value) { return sep === '/' ? value : value.split(sep).join('/') }
function insideOrSame(root, child) {
  const rel = relative(resolve(root), resolve(child))
  return rel === '' || rel !== '..' && !rel.startsWith(`..${sep}`) && !rel.startsWith(sep)
}
function assert(condition, message) { if (!condition) throw new Error(message) }
