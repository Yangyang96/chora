import { lstat, readdir } from 'node:fs/promises'
import { basename, join, posix, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  MANAGED_IMAGE_ALIAS_ROLES,
  NETWORK_BOUNDARY_ROLE,
  O4_ROLES,
  assertDigest,
  assertEnvironmentID,
  assertImageDigest,
  assertInstalledDoctorRecord,
  validateInstalledDoctorEvidenceFiles,
  assertMarkerIdentity,
  assertSafePublicValue,
  canonicalJSONStringify,
  exactAbsolutePath,
  readExactBoundedFile,
  readPrivateEnvironmentMarker,
  sha256Hex,
  validateBindings,
  validatePlatform,
  writeExclusiveOwnerOnlyJSON,
} from './o4-installed-doctor-record.mjs'
import {
  computeExecutionIdentityDigest,
  computeImageTupleDigest,
  validateImageReuseRecordSource,
} from './o4-image-reuse-record.mjs'
import { validateScreenshotOCR } from './o4-recovery-residue-record.mjs'

export const O4_PUBLICATION_SCHEMA = 'chora.m1-o4-fresh-installed-acceptance.v1'
export const PROFILE_JOURNEY_SCHEMA = 'chora.m1-o4-profile-journey.v1'
export const RECOVERY_RECORD_SCHEMA = 'chora.m1-o4-recovery-residue-record.v1'
export const DISCLOSURE_RECORD_SCHEMA = 'chora.m1-o4-disclosure-record.v1'

const candidateManifestSchema = 'chora.m1-o4-candidate-manifest.v1'
const releaseSpecSchema = 'chora.m1-o4-release-spec.v1'
const releaseManifestSchema = 'chora.m1-o4-release-manifest.v1'
const offlineBundleEvidenceSchema = 'chora.m1-o4-offline-release-bundle-evidence.v1'
const piProvenanceSchema = 'chora.m1-o4-pi-provenance.v1'
const endpointEvidenceSchema = 'chora.m1-o4-engine-endpoint-evidence.v1'
const sourceManifestSchema = 'chora.local-alpha-source-bundle.v1'
const sourcePathPolicySchema = 'chora.source-path-policy.v1'
const sourcePathPolicyRelativePath = 'distribution/v1/policies/source-path-policy.v1.json'
const credentialCorpusSchema = 'chora.m1-o4-private-credential-corpus.v1'
const screenshotMetadataSchema = 'chora.m1-o4-screenshot-metadata-scan.v1'

const profileFiles = Object.freeze(['minimal.json', 'standard.json', 'trusted-local.json'])
const disclosureArtifacts = Object.freeze([
  Object.freeze({ name: 'execution.log', kind: 'log' }),
  Object.freeze({ name: 'state.db', kind: 'database' }),
  Object.freeze({ name: 'change.patch', kind: 'patch' }),
  Object.freeze({ name: 'payload.json', kind: 'json' }),
  Object.freeze({ name: 'screen.png', kind: 'png' }),
  Object.freeze({ name: 'screen-metadata.json', kind: 'png_metadata' }),
  Object.freeze({ name: 'screen-ocr.json', kind: 'png_ocr' }),
])
const disclosureEntries = Object.freeze([...disclosureArtifacts.map(({ name }) => name), 'disclosure.json'].sort())
const recoveryScenarios = Object.freeze([
  'failure', 'cancel', 'timeout', 'retry', 'restart', 'orphan', 'reopen', 'apply',
])
const residueKeys = Object.freeze([
  'ownedContainers', 'ownedNetworks', 'ownedVolumes', 'ownedConfigs', 'ownedWorkspaces',
  'ownedProcessGroups', 'activeReferences', 'recoverableReferences',
])
const uuidV7 = '[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}'
const opaquePatterns = Object.freeze({
  journeyId: /^jny_[A-Za-z0-9_-]{24,80}$/,
  taskId: new RegExp(`^task_${uuidV7}$`),
  runId: new RegExp(`^run_${uuidV7}$`),
  attemptId: new RegExp(`^attempt_${uuidV7}$`),
  workspaceIdentity: /^sha256:[0-9a-f]{64}$/,
  workspaceId: /^wsp_[A-Za-z0-9_-]{24,80}$/,
  targetId: /^tgt_[A-Za-z0-9_-]{24,80}$/,
})
const pngSignature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])

export async function validateInstalledEvidence(inputs) {
  const marker = await readPrivateEnvironmentMarker(inputs.privateEnvironmentMarkerFile)
  const dynamic = await validateInstalledDoctorEvidenceFiles({
    engineQualificationFile: inputs.qualificationFile,
    modelObservationFile: inputs.modelObservationFile,
    installedDoctorReportFile: inputs.doctorRecordFile,
  })
  const doctor = assertInstalledDoctorRecord(dynamic.installedDoctorReport)
  const doctorInput = await readJSONWithDigest(inputs.doctorRecordFile, 'installed Doctor record', 1024 * 1024)
  const qualificationInput = await readJSONWithDigest(inputs.qualificationFile, 'Engine qualification', 2 * 1024 * 1024)
  const modelInput = await readJSONWithDigest(inputs.modelObservationFile, 'model observation', 2 * 1024 * 1024)
  const reuseInput = await readJSONWithDigest(inputs.imageReuseRecordFile, 'image reuse record', 2 * 1024 * 1024)
  const reuse = await validateImageReuseRecordSource(reuseInput.value, inputs.sourceA3LedgerFile, marker)
  assertMarkerIdentity(doctor, marker, 'installed Doctor record')
  assertMarkerIdentity(reuse, marker, 'image reuse record')
  assertSameIdentity(doctor, reuse, 'Doctor/image reuse')
  assert(reuse.bindingDigest === doctor.bindingDigest, 'Doctor/image reuse binding digest drifted')
  validateRoleReuseBinding(doctor.bindings.roles, reuse)

  const actualBindings = await validateBoundArtifactFiles(inputs, doctor, marker)
  const journeys = await readAndValidateProfileJourneys(inputs.profileJourneyDirectory, doctor, reuse)
  const recoveryInput = await readJSONWithDigest(inputs.recoveryRecordFile, 'recovery/residue record', 2 * 1024 * 1024)
  const recovery = validateRecoveryRecord(recoveryInput.value, doctor, reuse)
  validateCrossRecordIdentities(journeys, recovery, reuse)
  const disclosure = await validateDisclosureEvidence(inputs.disclosureDirectory,
    inputs.credentialCorpusFile, marker, doctor)

  const safe = {
    schemaVersion: O4_PUBLICATION_SCHEMA,
    status: 'passed',
    environmentId: marker.environmentId,
    installId: marker.installId,
    generationId: marker.generationId,
    platform: clone(doctor.platform),
    bindings: clone(actualBindings),
    counts: {
      managedAttempts: 3,
      profileJourneys: 3,
      recoveryScenarios: recoveryScenarios.length,
      setupLoads: 2,
      attemptBuildPullLoad: 0,
      retryBuildPullLoad: 0,
      restartBuildPullLoad: 0,
      recoveryBuildPullLoad: 0,
      verifierBuildPullLoad: 0,
      terminalOwnedResidue: 0,
      activeReferences: 0,
      recoverableReferences: 0,
      sharedExactImagesPresent: 2,
      disclosureArtifacts: disclosureArtifacts.length,
      credentialMatches: 0,
      genericSecretMatches: 0,
      privatePathMatches: 0,
      emailMatches: 0,
      urlMatches: 0,
      screenshotMetadataUnsafeMatches: 0,
      screenshotOCRUnsafeMatches: 0,
    },
    components: {
      installedDoctorRecordSha256: doctorInput.sha256,
      engineQualificationSha256: qualificationInput.sha256,
      modelObservationSha256: modelInput.sha256,
      imageReuseRecordSha256: reuseInput.sha256,
      profileJourneySha256: journeys.map(({ profile, sha256 }) => ({ profile, sha256 })),
      recoveryRecordSha256: recoveryInput.sha256,
      disclosureRecordSha256: disclosure.recordSha256,
    },
  }
  assertSafePublication(safe)
  return Object.freeze({ ...safe, aggregateDigest: sha256Hex(canonicalJSONStringify(safe)) })
}

export async function publishInstalledEvidence(inputs, outputPath, dependencies = undefined) {
  const publication = await validateInstalledEvidence(inputs)
  await writeExclusiveOwnerOnlyJSON(publication, outputPath, 'O4 installed evidence output',
    dependencies?.repositoryCapture)
  return Object.freeze({
    schemaVersion: O4_PUBLICATION_SCHEMA,
    status: 'passed',
    outputBasename: basename(outputPath),
    aggregateDigest: publication.aggregateDigest,
  })
}

export function assertInstalledEvidencePublication(record) {
  assertPlainObject(record, 'O4 installed evidence publication')
  assertExactKeys(record, [
    'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
    'bindings', 'counts', 'components', 'aggregateDigest',
  ], 'O4 installed evidence publication')
  assert(record.schemaVersion === O4_PUBLICATION_SCHEMA && record.status === 'passed',
    'O4 publication schema/status drifted')
  assertEnvironmentID(record.environmentId, 'O4 publication environment ID')
  assert(/^ins_[A-Za-z0-9_-]{24,80}$/.test(record.installId), 'O4 publication install ID is invalid')
  assert(/^gen_[A-Za-z0-9_-]{24,80}$/.test(record.generationId), 'O4 publication generation ID is invalid')
  validatePlatform(record.platform)
  validateBindings(record.bindings)
  validateAggregateCounts(record.counts)
  validateComponents(record.components)
  assertDigest(record.aggregateDigest, 'O4 aggregate digest')
  const { aggregateDigest, ...safe } = record
  assert(aggregateDigest === sha256Hex(canonicalJSONStringify(safe)), 'O4 aggregate digest drifted')
  assertSafePublication(record)
  return record
}

async function validateBoundArtifactFiles(inputs, doctor, marker) {
  const bindings = doctor.bindings
  const candidate = await readJSONWithDigest(inputs.candidateManifestFile, 'candidate manifest', 1024 * 1024)
  const source = await readJSONWithDigest(inputs.sourceManifestFile, 'source manifest', 8 * 1024 * 1024)
  const binary = await readExactBoundedFile(inputs.binaryFile, 'installed binary', 128 * 1024 * 1024)
  const webAggregate = await computeWebDirectoryAggregate(inputs.webDirectory)
  const releaseSpec = await readJSONWithDigest(inputs.releaseSpecFile, 'release spec', 2 * 1024 * 1024)
  const releaseManifest = await readJSONWithDigest(inputs.releaseManifestFile, 'release manifest', 2 * 1024 * 1024)
  const offlineBundle = await readJSONWithDigest(inputs.offlineBundleEvidenceFile,
    'offline bundle evidence', 2 * 1024 * 1024)
  const pathPi = await readJSONWithDigest(inputs.pathPiProvenanceFile, 'PATH Pi provenance', 1024 * 1024)
  const privatePi = await readJSONWithDigest(inputs.privatePiProvenanceFile, 'private Pi provenance', 1024 * 1024)
  const endpoint = await readJSONWithDigest(inputs.engineEndpointEvidenceFile, 'Engine endpoint evidence', 1024 * 1024)

  assert(candidate.sha256 === bindings.candidate.candidateManifestSha256, 'candidate manifest digest drifted')
  assert(source.sha256 === bindings.candidate.sourceManifestSha256, 'source manifest digest drifted')
  const sourceAggregate = await validateSourceManifest({
    manifest: source.value,
    sourceManifestFile: inputs.sourceManifestFile,
    sourceBundleRoot: inputs.sourceBundleRoot,
  })
  assert(sourceAggregate === bindings.candidate.sourceAggregateSha256, 'source aggregate drifted')
  assert(sha256Hex(binary) === bindings.product.binarySha256, 'installed binary digest drifted')
  assert(webAggregate === bindings.product.webAggregateSha256, 'installed web aggregate drifted')
  assert(releaseSpec.sha256 === bindings.release.releaseSpecSha256, 'release spec digest drifted')
  assert(releaseManifest.sha256 === bindings.release.releaseManifestSha256, 'release manifest digest drifted')
  assert(offlineBundle.sha256 === bindings.release.offlineBundleEvidenceSha256,
    'offline bundle evidence digest drifted')
  assert(pathPi.sha256 === bindings.pi.pathProvenanceSha256, 'PATH Pi provenance digest drifted')
  assert(privatePi.sha256 === bindings.pi.privateProvenanceSha256, 'private Pi provenance digest drifted')
  assert(pathPi.sha256 !== privatePi.sha256, 'PATH/private Pi provenance was spliced')
  assert(endpoint.sha256 === bindings.engine.endpointEvidenceSha256, 'Engine endpoint evidence digest drifted')

  validateCandidateManifest(candidate.value, doctor, {
    sourceManifestSha256: source.sha256, sourceAggregateSha256: sourceAggregate,
    binarySha256: sha256Hex(binary), webAggregateSha256: webAggregate,
    releaseSpecSha256: releaseSpec.sha256, releaseManifestSha256: releaseManifest.sha256,
    offlineBundleEvidenceSha256: offlineBundle.sha256,
  })
  validateReleaseSpec(releaseSpec.value, doctor)
  validateReleaseManifest(releaseManifest.value, doctor, releaseSpec.sha256)
  validateOfflineBundleEvidence(offlineBundle.value, doctor, releaseManifest.sha256)
  validatePiProvenance(pathPi.value, 'path', marker.generationId)
  validatePiProvenance(privatePi.value, 'private', marker.generationId)
  validateEndpointEvidence(endpoint.value, doctor)
  return clone(bindings)
}

function validateCandidateManifest(value, doctor, actual) {
  assertPlainObject(value, 'candidate manifest')
  assertExactKeys(value, [
    'schemaVersion', 'environmentId', 'installId', 'generationId', 'platform',
    'sourceManifestSha256', 'sourceAggregateSha256', 'binarySha256', 'webAggregateSha256',
    'releaseSpecSha256', 'releaseManifestSha256', 'offlineBundleEvidenceSha256',
  ], 'candidate manifest')
  assert(value.schemaVersion === candidateManifestSchema, 'candidate manifest schema drifted')
  assertSameIdentity(value, doctor, 'candidate/Doctor')
  assertSameJSON(value.platform, doctor.platform, 'candidate platform drifted')
  for (const [key, expected] of Object.entries(actual)) {
    assertDigest(value[key], `candidate ${key}`)
    assert(value[key] === expected, `candidate ${key} drifted`)
  }
  assertSafePublicValue(value)
}

export async function validateSourceManifest({ manifest, sourceManifestFile, sourceBundleRoot }) {
  assertPlainObject(manifest, 'source manifest')
  assertExactKeys(manifest, [
    'schema_version', 'aggregate_sha256', 'source_path_policy', 'install_only_projection',
    'model_readable_projection', 'files',
  ], 'source manifest')
  assert(manifest.schema_version === sourceManifestSchema, 'source manifest schema drifted')
  assertDigest(manifest.aggregate_sha256, 'source aggregate')
  assertPlainObject(manifest.source_path_policy, 'source path policy identity')
  assertExactKeys(manifest.source_path_policy, ['path', 'sha256'], 'source path policy identity')
  assert(manifest.source_path_policy.path === sourcePathPolicyRelativePath,
    'source manifest policy path drifted')
  assertDigest(manifest.source_path_policy.sha256, 'source manifest policy digest')

  const bundleRoot = await exactDirectory(sourceBundleRoot, 'source bundle root')
  assert(exactAbsolutePath(sourceManifestFile, 'source manifest file') === join(bundleRoot, 'source-manifest.json'),
    'source manifest is outside the exact source bundle root')
  const sourceRoot = await exactDirectory(join(bundleRoot, 'source'), 'source bundle content root')
  const policyFile = join(sourceRoot, ...sourcePathPolicyRelativePath.split('/'))
  const policyBytes = await readExactBoundedFile(policyFile, 'authenticated source path policy', 1024 * 1024)
  assert(sha256Hex(policyBytes) === manifest.source_path_policy.sha256,
    'source path policy digest drifted')
  const policy = parseJSON(policyBytes, 'authenticated source path policy')
  validateSourcePathPolicy(policy)

  assert(Array.isArray(manifest.files) && manifest.files.length > 0 && manifest.files.length <= 10000,
    'source manifest file set is empty or unbounded')
  const paths = []
  let framing = ''
  for (const [index, entry] of manifest.files.entries()) {
    assertPlainObject(entry, 'source manifest entry')
    assertExactKeys(entry, ['path', 'size', 'mode', 'sha256'], 'source manifest entry')
    assertSafeRelativePath(entry.path, 'source manifest path')
    assert(index === 0 || manifest.files[index - 1].path < entry.path,
      'source manifest paths are not sorted and unique')
    assert(Number.isSafeInteger(entry.size) && entry.size >= 0, 'source manifest size is invalid')
    assert(typeof entry.mode === 'string' && /^0[0-7]{3}$/.test(entry.mode), 'source manifest mode is invalid')
    const mode = Number.parseInt(entry.mode, 8)
    assert(mode <= 0o777 && (mode & 0o400) !== 0 && (mode & 0o022) === 0,
      'source manifest mode is unsafe')
    assertDigest(entry.sha256, 'source manifest entry digest')
    paths.push(entry.path)
    framing += `${entry.path}\0${entry.mode}\0${entry.size}\0${entry.sha256}\n`
  }
  assert(manifest.aggregate_sha256 === sha256Hex(framing), 'source manifest aggregate is non-canonical')

  const actualEntries = await inspectSourceTree(sourceRoot)
  assertSameJSON(actualEntries, manifest.files, 'source bundle actual entries drifted from manifest')
  const { installOnlyPaths, modelReadablePaths } = validatePolicyCoverage(policy, paths)
  const installOnlySet = new Set(installOnlyPaths)
  const installOnlyEntries = manifest.files.filter((entry) => installOnlySet.has(entry.path))
  const modelReadableEntries = manifest.files.filter((entry) => !installOnlySet.has(entry.path))
  assert(installOnlyEntries.length + modelReadableEntries.length === manifest.files.length,
    'source projections do not form a complete partition')
  assert(installOnlyEntries.every((entry) => !modelReadableEntries.includes(entry)),
    'source projections overlap')
  validateProjectionIdentity(manifest.install_only_projection, installOnlyEntries, 'install-only')
  validateProjectionIdentity(manifest.model_readable_projection, modelReadableEntries, 'model-readable')
  return manifest.aggregate_sha256
}

function validateSourcePathPolicy(policy) {
  assertPlainObject(policy, 'source path policy')
  assertExactKeys(policy, [
    'schema_version', 'roots', 'excluded_directory_names', 'excluded_file_suffixes',
    'declared_files', 'declared_paths_sha256', 'install_only_roots', 'install_only_paths',
    'install_only_files', 'install_only_paths_sha256', 'model_readable_files',
    'model_readable_paths_sha256',
  ], 'source path policy')
  assert(policy.schema_version === sourcePathPolicySchema, 'source path policy schema drifted')
  assertSortedUniqueStrings(policy.roots, 'source path policy roots',
    (value) => validPolicyPath(value) && !value.includes('/'))
  assertSortedUniqueStrings(policy.excluded_directory_names, 'source path policy directory exclusions',
    (value) => typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..' &&
      !value.includes('/') && !value.includes('\\') && !value.includes('\0'))
  assertSortedUniqueStrings(policy.excluded_file_suffixes, 'source path policy suffix exclusions',
    (value) => typeof value === 'string' && value.startsWith('.') && value.length > 1 &&
      !value.includes('/') && !value.includes('\\') && !value.includes('\0'))
  assertSortedUniqueStrings(policy.install_only_roots, 'install-only roots',
    (value) => validPolicyPath(value) && !value.includes('/'))
  assertSortedUniqueStrings(policy.install_only_paths, 'install-only paths', validPolicyPath)
  for (const root of ['.npmrc', 'distribution', 'go.mod', 'package.json', 'vendor']) {
    assert(policy.roots.includes(root), `source path policy omits required root: ${root}`)
  }
  for (const root of policy.install_only_roots) {
    assert(policy.roots.includes(root), `install-only root is outside source policy: ${root}`)
  }
  assert(policy.install_only_roots.includes('vendor'), 'vendor must be classified as install-only')
  for (const field of ['declared_files', 'install_only_files', 'model_readable_files']) {
    assert(Number.isSafeInteger(policy[field]) && policy[field] > 0,
      `source path policy ${field} is invalid`)
  }
  assert(policy.declared_files === policy.install_only_files + policy.model_readable_files,
    'source path policy classification counts drifted')
  for (const field of ['declared_paths_sha256', 'install_only_paths_sha256', 'model_readable_paths_sha256']) {
    assertDigest(policy[field], `source path policy ${field}`)
  }
  assert(pathDeclaredByPolicy(policy, sourcePathPolicyRelativePath) &&
    !pathExcludedByPolicy(policy, sourcePathPolicyRelativePath),
  'source path policy does not declare itself')
}

function validatePolicyCoverage(policy, paths) {
  assert(paths.length === policy.declared_files && pathsDigest(paths) === policy.declared_paths_sha256,
    'declared source coverage does not match source path policy')
  for (const [index, path] of paths.entries()) {
    assert(index === 0 || paths[index - 1] < path, 'declared source paths are not sorted and unique')
    assert(pathDeclaredByPolicy(policy, path) && !pathExcludedByPolicy(policy, path),
      `source path is outside exact policy: ${path}`)
  }
  const installOnlyPaths = paths.filter((path) => installOnlyPath(policy, path))
  const modelReadablePaths = paths.filter((path) => !installOnlyPath(policy, path))
  assert(installOnlyPaths.length === policy.install_only_files &&
    pathsDigest(installOnlyPaths) === policy.install_only_paths_sha256,
  'install-only source coverage does not match source path policy')
  assert(modelReadablePaths.length === policy.model_readable_files &&
    pathsDigest(modelReadablePaths) === policy.model_readable_paths_sha256,
  'model-readable source coverage does not match source path policy')
  return { installOnlyPaths, modelReadablePaths }
}

function validateProjectionIdentity(projection, entries, label) {
  assertPlainObject(projection, `${label} projection`)
  assertExactKeys(projection, ['files', 'paths_sha256', 'aggregate_sha256'], `${label} projection`)
  assert(Number.isSafeInteger(projection.files) && projection.files > 0,
    `${label} projection file count is invalid`)
  assertDigest(projection.paths_sha256, `${label} projection paths digest`)
  assertDigest(projection.aggregate_sha256, `${label} projection aggregate`)
  assert(projection.files === entries.length &&
    projection.paths_sha256 === pathsDigest(entries.map((entry) => entry.path)) &&
    projection.aggregate_sha256 === aggregateSourceEntries(entries),
  `${label} projection identity mismatch`)
}

async function inspectSourceTree(sourceRoot) {
  const entries = []
  const state = { files: 0, bytes: 0 }
  await collectSourceEntries(sourceRoot, sourceRoot, entries, state)
  assert(entries.length > 0 && entries.length <= 10000, 'source bundle actual file set is empty or unbounded')
  entries.sort((left, right) => left.path.localeCompare(right.path))
  return entries
}

async function collectSourceEntries(root, directory, entries, state) {
  const names = (await readdir(directory)).sort()
  for (const name of names) {
    assert(name !== '.' && name !== '..' && !name.includes('\0'), 'source bundle entry name is invalid')
    const path = join(directory, name)
    const info = await lstat(path)
    assert(!info.isSymbolicLink(), 'source bundle tree contains a symlink')
    if (info.isDirectory()) {
      await collectSourceEntries(root, path, entries, state)
      continue
    }
    assert(info.isFile() && info.size <= 128 * 1024 * 1024,
      'source bundle entry is not a bounded regular file')
    state.files++
    state.bytes += info.size
    assert(state.files <= 10000 && state.bytes <= 1024 * 1024 * 1024,
      'source bundle tree exceeds bounded limits')
    const bytes = await readExactBoundedFile(path, 'source bundle entry', 128 * 1024 * 1024)
    const entryPath = relative(root, path).split(sep).join('/')
    assertSafeRelativePath(entryPath, 'source bundle entry path')
    entries.push({
      path: entryPath,
      size: bytes.length,
      mode: (info.mode & 0o777).toString(8).padStart(4, '0'),
      sha256: sha256Hex(bytes),
    })
  }
}

function aggregateSourceEntries(entries) {
  let framing = ''
  for (const entry of entries) {
    framing += `${entry.path}\0${entry.mode}\0${entry.size}\0${entry.sha256}\n`
  }
  return sha256Hex(framing)
}

function pathsDigest(paths) {
  return sha256Hex(paths.map((path) => `${path}\n`).join(''))
}

function pathDeclaredByPolicy(policy, path) {
  return policy.roots.some((root) => path === root || path.startsWith(`${root}/`))
}

function pathExcludedByPolicy(policy, path) {
  const components = path.split('/')
  if (components.slice(0, -1).some((component) => policy.excluded_directory_names.includes(component))) return true
  return policy.excluded_file_suffixes.some((suffix) => path.endsWith(suffix))
}

function installOnlyPath(policy, path) {
  return policy.install_only_paths.includes(path) ||
    policy.install_only_roots.some((root) => path === root || path.startsWith(`${root}/`))
}

function validPolicyPath(value) {
  return typeof value === 'string' && value.length > 0 && !value.includes('\0') && !value.includes('\\') &&
    !value.startsWith('/') && !value.startsWith('../') && value !== '.' && value !== '..' &&
    posix.normalize(value) === value
}

function assertSortedUniqueStrings(values, label, predicate) {
  assert(Array.isArray(values) && values.length > 0, `${label} is empty or invalid`)
  for (const [index, value] of values.entries()) {
    assert(predicate(value) && (index === 0 || values[index - 1] < value),
      `${label} is not sorted, unique, and valid`)
  }
}

function validateReleaseSpec(value, doctor) {
  assertPlainObject(value, 'release spec')
  assertExactKeys(value, ['schemaVersion', 'generationId', 'platform', 'roles'], 'release spec')
  assert(value.schemaVersion === releaseSpecSchema && value.generationId === doctor.generationId,
    'release spec schema/generation drifted')
  assertSameJSON(value.platform, doctor.platform, 'release spec platform drifted')
  validateRoleArray(value.roles, doctor.bindings.roles)
  assertSafePublicValue(value)
}

function validateReleaseManifest(value, doctor, releaseSpecSha256) {
  assertPlainObject(value, 'release manifest')
  assertExactKeys(value, ['schemaVersion', 'generationId', 'releaseSpecSha256', 'roles'], 'release manifest')
  assert(value.schemaVersion === releaseManifestSchema && value.generationId === doctor.generationId &&
    value.releaseSpecSha256 === releaseSpecSha256, 'release manifest identity drifted')
  validateRoleArray(value.roles, doctor.bindings.roles)
  assertSafePublicValue(value)
}

function validateOfflineBundleEvidence(value, doctor, releaseManifestSha256) {
  assertPlainObject(value, 'offline bundle evidence')
  assertExactKeys(value, ['schemaVersion', 'generationId', 'releaseManifestSha256', 'platform', 'artifacts'],
    'offline bundle evidence')
  assert(value.schemaVersion === offlineBundleEvidenceSchema && value.generationId === doctor.generationId &&
    value.releaseManifestSha256 === releaseManifestSha256, 'offline bundle evidence identity drifted')
  assertSameJSON(value.platform, { os: 'linux', architecture: 'arm64' },
    'offline bundle platform drifted')
  assert(Array.isArray(value.artifacts) && value.artifacts.length === 2,
    'offline bundle must bind exactly two artifacts')
  let previousArtifact = ''
  const seenRoles = []
  for (const artifact of value.artifacts) {
    assertPlainObject(artifact, 'offline bundle artifact')
    assertExactKeys(artifact, ['artifactId', 'roles', 'policyDigests', 'archive',
      'expectedDockerConfigImageId'], 'offline bundle artifact')
    assert(['managed-pi-runtime', 'network-boundary'].includes(artifact.artifactId) &&
      artifact.artifactId > previousArtifact, 'offline bundle artifacts are unsorted or duplicated')
    previousArtifact = artifact.artifactId
    assert(Array.isArray(artifact.roles) && artifact.roles.length > 0 &&
      artifact.roles.every((role, index) => O4_ROLES.includes(role) &&
        (index === 0 || role > artifact.roles[index - 1])),
    'offline bundle roles are unsafe, unsorted, or duplicated')
    assertPlainObject(artifact.policyDigests, 'offline bundle policy digests')
    assertExactKeys(artifact.policyDigests, artifact.roles, 'offline bundle policy digests')
    assertPlainObject(artifact.archive, 'offline bundle archive')
    assertExactKeys(artifact.archive, ['path', 'format', 'size', 'sha256'], 'offline bundle archive')
    assertSafeRelativePath(artifact.archive.path, 'offline bundle archive path')
    assert(artifact.archive.format === 'docker-archive' &&
      Number.isSafeInteger(artifact.archive.size) && artifact.archive.size > 0,
    'offline bundle archive binding is invalid')
    assertDigest(artifact.archive.sha256, 'offline bundle archive digest')
    assertImageDigest(artifact.expectedDockerConfigImageId,
      'offline bundle expected Docker config image ID')
    for (const role of artifact.roles) {
      const expected = doctor.bindings.roles[role]
      assert(expected.artifactId === artifact.artifactId &&
        expected.archiveSha256 === artifact.archive.sha256 &&
        expected.archiveSize === artifact.archive.size &&
        expected.dockerConfigImageId === artifact.expectedDockerConfigImageId &&
        expected.policyDigest === artifact.policyDigests[role],
      `offline bundle role binding drifted for ${role}`)
      seenRoles.push(role)
    }
  }
  assertSameArray([...seenRoles].sort(), [...O4_ROLES].sort(),
    'offline bundle roles are incomplete or duplicated')
  assertSafePublicValue(value)
}

function validateRoleArray(roles, expected) {
  assert(Array.isArray(roles) && roles.length === O4_ROLES.length, 'release role set is incomplete')
  const seen = []
  for (const entry of roles) {
    assertPlainObject(entry, 'release role entry')
    assertExactKeys(entry, ['role', 'artifactId', 'archiveSha256', 'archiveSize',
      'dockerConfigImageId', 'policyDigest'], 'release role entry')
    assert(O4_ROLES.includes(entry.role), 'release role is invalid')
    assert(entry.artifactId === expected[entry.role].artifactId, 'release role artifact ID drifted')
    assertDigest(entry.archiveSha256, 'release role archive digest')
    assert(Number.isSafeInteger(entry.archiveSize) && entry.archiveSize > 0,
      'release role archive size is invalid')
    assertImageDigest(entry.dockerConfigImageId, 'release role Docker config image ID')
    assertDigest(entry.policyDigest, 'release role policy digest')
    assert(entry.archiveSha256 === expected[entry.role].archiveSha256 &&
      entry.archiveSize === expected[entry.role].archiveSize &&
      entry.dockerConfigImageId === expected[entry.role].dockerConfigImageId &&
      entry.policyDigest === expected[entry.role].policyDigest, 'release role binding drifted')
    seen.push(entry.role)
  }
  assertSameArray([...seen].sort(), [...O4_ROLES].sort(), 'release role set drifted')
}

function validatePiProvenance(value, kind, generationId) {
  assertPlainObject(value, `${kind} Pi provenance`)
  assertExactKeys(value,
    ['schemaVersion', 'kind', 'generationId', 'sourceIdentitySha256', 'binarySha256'], `${kind} Pi provenance`)
  assert(value.schemaVersion === piProvenanceSchema && value.kind === kind && value.generationId === generationId,
    `${kind} Pi provenance identity drifted`)
  assertDigest(value.sourceIdentitySha256, `${kind} Pi source identity digest`)
  assertDigest(value.binarySha256, `${kind} Pi binary digest`)
  assertSafePublicValue(value)
}

function validateEndpointEvidence(value, doctor) {
  assertPlainObject(value, 'Engine endpoint evidence')
  assertExactKeys(value, [
    'schemaVersion', 'status', 'generationId', 'platform', 'contextIdentitySha256', 'endpointIdentitySha256',
  ], 'Engine endpoint evidence')
  assert(value.schemaVersion === endpointEvidenceSchema && value.status === 'passed' &&
    value.generationId === doctor.generationId, 'Engine endpoint evidence identity drifted')
  assertSameJSON(value.platform, doctor.platform, 'Engine endpoint platform drifted')
  assertDigest(value.contextIdentitySha256, 'Engine context identity digest')
  assertDigest(value.endpointIdentitySha256, 'Engine endpoint identity digest')
  assert(value.contextIdentitySha256 !== value.endpointIdentitySha256,
    'Engine context and endpoint evidence must remain distinct')
  assertSafePublicValue(value)
}

async function readAndValidateProfileJourneys(directoryPath, doctor, reuse) {
  const directory = await exactDirectory(directoryPath, 'profile journey directory')
  const entries = (await readdir(directory)).sort()
  assertSameArray(entries, [...profileFiles].sort(), 'profile journey directory entries drifted')
  const journeys = []
  for (const name of profileFiles) {
    const input = await readJSONWithDigest(join(directory, name), `${name} profile journey`, 1024 * 1024)
    const profile = name === 'trusted-local.json' ? 'trusted_local' : name.slice(0, -5)
    validateProfileJourney(input.value, profile, doctor, reuse)
    journeys.push({ ...input.value, sha256: input.sha256 })
  }
  assertUnique(journeys.map((journey) => journey.journeyId), 'profile journey IDs')
  assertUnique(journeys.map((journey) => journey.taskId), 'profile journey Task IDs')
  return journeys
}

function validateProfileJourney(value, profile, doctor, reuse) {
  assertPlainObject(value, `${profile} profile journey`)
  assertExactKeys(value, [
    'schemaVersion', 'status', 'sequence', 'profile', 'environmentId', 'installId', 'generationId',
    'bindingDigest', 'publicProductEntry', 'journeyId', 'taskId', 'runId', 'attemptId',
    'workspaceIdentity', 'tupleIdentity', 'executionIdentityDigest', 'sourceA3LedgerSha256',
    'sourceA3AuditFinalDigest', 'profileEvidence', 'digest',
  ], `${profile} profile journey`)
  const sequence = ['minimal', 'standard', 'trusted_local'].indexOf(profile) + 1
  assert(value.schemaVersion === PROFILE_JOURNEY_SCHEMA && value.status === 'passed' &&
    value.sequence === sequence && value.profile === profile && value.publicProductEntry === true,
  `${profile} profile journey header drifted`)
  assertSameIdentity(value, doctor, `${profile}/Doctor`)
  assert(value.bindingDigest === doctor.bindingDigest, `${profile} profile binding drifted`)
  assertOpaque(value.journeyId, opaquePatterns.journeyId, `${profile} journey ID`)
  assertOpaque(value.taskId, opaquePatterns.taskId, `${profile} Task ID`)
  assertOpaque(value.runId, opaquePatterns.runId, `${profile} Run ID`)
  assertOpaque(value.attemptId, opaquePatterns.attemptId, `${profile} Attempt ID`)
  assertOpaque(value.workspaceIdentity, opaquePatterns.workspaceIdentity, `${profile} workspace identity`)
  assertDigest(value.tupleIdentity, `${profile} B2 tuple identity`)
  assert(value.tupleIdentity === reuse.tupleIdentity, `${profile} B2 tuple identity drifted`)
  assert(value.sourceA3LedgerSha256 === reuse.sourceA3LedgerSha256 &&
    value.sourceA3AuditFinalDigest === reuse.sourceA3AuditFinalDigest,
  `${profile} source A3 ledger identity drifted`)
  assertDigest(value.executionIdentityDigest, `${profile} execution identity digest`)
  assert(value.executionIdentityDigest === computeExecutionIdentityDigest(value),
    `${profile} execution identity digest drifted`)

  if (profile === 'minimal') {
    assertPlainObject(value.profileEvidence, 'Minimal profile evidence')
    assertExactKeys(value.profileEvidence, [
      'runtimeAdapterId', 'runtimeSource', 'executionProvider', 'capabilityPolicy', 'managedSandbox',
      'bashObserved', 'editObserved', 'prohibitedCapabilityDenialCount',
      'prohibitedCapabilityDenied', 'prohibitedCapabilityEvidenceSha256',
      'attemptTerminalStatus', 'independentVerification', 'independentVerificationEvidenceSha256',
      'managedFallback',
    ], 'Minimal profile evidence')
    assert(value.profileEvidence.runtimeAdapterId === 'pi' &&
      value.profileEvidence.runtimeSource === 'managed_pi_image' &&
      value.profileEvidence.executionProvider === 'docker' &&
      value.profileEvidence.capabilityPolicy === 'chora.minimal.v1' &&
      value.profileEvidence.managedSandbox === true && value.profileEvidence.bashObserved === true &&
      value.profileEvidence.editObserved === true && value.profileEvidence.prohibitedCapabilityDenialCount === 1 &&
      value.profileEvidence.prohibitedCapabilityDenied === true &&
      value.profileEvidence.attemptTerminalStatus === 'succeeded' &&
      value.profileEvidence.independentVerification === true && value.profileEvidence.managedFallback === false,
    'Minimal managed Pi Attempt predicates are incomplete')
    assertDigest(value.profileEvidence.prohibitedCapabilityEvidenceSha256,
      'Minimal prohibited-capability denial evidence digest')
    assertDigest(value.profileEvidence.independentVerificationEvidenceSha256,
      'Minimal independent Verification evidence digest')
    assert(value.executionIdentityDigest === reuse.sourceMinimalExecutionIdentityDigest,
      'Minimal profile journey is not bound to the source A3 Minimal Attempt')
    assert(value.profileEvidence.independentVerificationEvidenceSha256 ===
      reuse.sourceMinimalVerifierSliceDigest,
    'Minimal profile journey is not bound to the source A3 Minimal Verifier')
  } else {
    if (profile === 'standard') {
      assertPlainObject(value.profileEvidence, 'Standard profile evidence')
      assertExactKeys(value.profileEvidence, [
        'runtimeAdapterId', 'runtimeSource', 'executionProvider', 'capabilityPolicy',
        'fullRepository', 'securityPolicyEnforced', 'independentVerification',
        'managedSandbox', 'taskOwnedApply',
      ], 'Standard profile evidence')
      assert(value.profileEvidence.runtimeAdapterId === 'pi' &&
        value.profileEvidence.runtimeSource === 'managed_pi_image' &&
        value.profileEvidence.executionProvider === 'docker' &&
        value.profileEvidence.capabilityPolicy === 'chora.standard.v1' &&
        ['fullRepository', 'securityPolicyEnforced', 'independentVerification',
          'managedSandbox', 'taskOwnedApply'].every((field) => value.profileEvidence[field] === true),
        'Standard full-repository/security predicates are incomplete')
      const managed = reuse.managedAttempts[0]
      for (const field of ['taskId', 'runId', 'attemptId', 'workspaceIdentity',
        'tupleIdentity', 'executionIdentityDigest']) {
        assert(value[field] === managed[field], `Standard journey ${field} is not bound to Managed Attempt 1`)
      }
    } else {
      assertPlainObject(value.profileEvidence, 'Trusted Local profile evidence')
      assertExactKeys(value.profileEvidence, [
        'runtimeAdapterId', 'runtimeSource', 'capabilityPolicy',
        'acknowledgementCurrent', 'acknowledgementPolicySha256', 'executionTarget',
        'managedFallback', 'sandboxClaimed',
      ], 'Trusted Local profile evidence')
      assert(value.profileEvidence.runtimeAdapterId === 'pi' &&
        value.profileEvidence.runtimeSource === 'local_pi' &&
        value.profileEvidence.capabilityPolicy === 'pi.native' &&
        value.profileEvidence.acknowledgementCurrent === true &&
        value.profileEvidence.executionTarget === 'trusted-host' &&
        value.profileEvidence.managedFallback === false && value.profileEvidence.sandboxClaimed === false,
      'Trusted Local acknowledgement/no-fallback predicates are incomplete')
      assertDigest(value.profileEvidence.acknowledgementPolicySha256,
        'Trusted Local acknowledgement policy digest')
    }
  }
  assertDigest(value.digest, `${profile} journey digest`)
  const { digest, ...safe } = value
  assert(digest === sha256Hex(canonicalJSONStringify(safe)), `${profile} journey digest drifted`)
  assertSafePublicValue(value)
}

function validateRecoveryRecord(value, doctor, reuse) {
  assertPlainObject(value, 'recovery/residue record')
  assertExactKeys(value, [
    'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'bindingDigest',
    'imageReuseDigest', 'scenarios', 'sharedImages', 'digest',
  ], 'recovery/residue record')
  assert(value.schemaVersion === RECOVERY_RECORD_SCHEMA && value.status === 'passed',
    'recovery/residue record schema/status drifted')
  assertSameIdentity(value, doctor, 'recovery/Doctor')
  assert(value.bindingDigest === doctor.bindingDigest && value.imageReuseDigest === reuse.digest,
    'recovery record binding drifted')
  assert(Array.isArray(value.scenarios) && value.scenarios.length === recoveryScenarios.length,
    'recovery scenario matrix is incomplete')
  for (let index = 0; index < value.scenarios.length; index++) {
    const scenario = value.scenarios[index]
    assertPlainObject(scenario, 'recovery scenario')
    assertExactKeys(scenario, [
      'sequence', 'name', 'taskId', 'runId', 'attemptId', 'workspaceId', 'targetId',
      'terminalStatus', 'publicEntry', 'cleanupComplete', 'residue',
    ], 'recovery scenario')
    assert(scenario.sequence === index + 1 && scenario.name === recoveryScenarios[index] &&
      scenario.publicEntry === true && scenario.cleanupComplete === true,
    'recovery scenario order/public cleanup drifted')
    for (const field of ['taskId', 'runId', 'attemptId', 'workspaceId', 'targetId']) {
      assertOpaque(scenario[field], opaquePatterns[field], `recovery ${scenario.name} ${field}`)
    }
    const terminalStatuses = {
      failure: 'failed', cancel: 'canceled', timeout: 'timed_out', retry: 'succeeded',
      restart: 'succeeded', orphan: 'recovered', reopen: 'succeeded', apply: 'succeeded',
    }
    assert(scenario.terminalStatus === terminalStatuses[scenario.name],
      `recovery ${scenario.name} terminal status drifted`)
    validateZeroResidue(scenario.residue, `recovery ${scenario.name} residue`)
  }
  for (const field of ['taskId', 'runId', 'attemptId', 'workspaceId', 'targetId']) {
    assertUnique(value.scenarios.map((scenario) => scenario[field]), `recovery ${field} values`)
  }
  assert(Array.isArray(value.sharedImages) && value.sharedImages.length === 2,
    'recovery record lacks the exact shared images')
  const expectedSharedImages = [
    {
      artifactId: 'managed-pi-runtime', roles: MANAGED_IMAGE_ALIAS_ROLES,
      binding: doctor.bindings.roles.managed_pi_runtime,
    },
    {
      artifactId: 'network-boundary', roles: [NETWORK_BOUNDARY_ROLE],
      binding: doctor.bindings.roles.network_boundary,
    },
  ]
  for (let index = 0; index < value.sharedImages.length; index++) {
    const image = value.sharedImages[index]
    assertPlainObject(image, 'shared image retention')
    assertExactKeys(image, [
      'artifactId', 'roles', 'archiveSha256', 'archiveSize', 'dockerConfigImageId', 'present',
    ], 'shared image retention')
    const expected = expectedSharedImages[index]
    assert(image.artifactId === expected.artifactId && image.present === true,
      'shared exact image was removed or drifted')
    assertSameArray(image.roles, expected.roles, 'shared exact image alias roles drifted')
    for (const field of ['archiveSha256', 'archiveSize', 'dockerConfigImageId']) {
      assert(image[field] === expected.binding[field], `shared exact image ${field} drifted`)
    }
  }
  assertDigest(value.digest, 'recovery record digest')
  const { digest, ...safe } = value
  assert(digest === sha256Hex(canonicalJSONStringify(safe)), 'recovery record digest drifted')
  assertSafePublicValue(value)
  return value
}

function validateCrossRecordIdentities(journeys, recovery, reuse) {
  const managed = reuse.managedAttempts
  for (const field of ['taskId', 'runId', 'attemptId']) {
    assertUnique(managed.map((attempt) => attempt[field]), `Managed Attempt ${field} values`)
    const standard = journeys.find((journey) => journey.profile === 'standard')
    assert(standard[field] === managed[0][field], `Standard journey ${field} binding drifted`)
    const trusted = journeys.find((journey) => journey.profile === 'trusted_local')
    const minimal = journeys.find((journey) => journey.profile === 'minimal')
    const profileIndependent = [minimal[field], trusted[field]]
    const allIndependent = [...profileIndependent, ...managed.slice(1).map((attempt) => attempt[field]),
      ...recovery.scenarios.map((scenario) => scenario[field])]
    assertUnique(allIndependent, `cross-record ${field} values`)
    assert(!allIndependent.includes(managed[0][field]), `cross-record ${field} was spliced`)
  }
  assertUnique(managed.map((attempt) => attempt.workspaceIdentity),
    'Managed Attempt workspace identity values')
  const standard = journeys.find((journey) => journey.profile === 'standard')
  const minimal = journeys.find((journey) => journey.profile === 'minimal')
  const trusted = journeys.find((journey) => journey.profile === 'trusted_local')
  assert(standard.workspaceIdentity === managed[0].workspaceIdentity,
    'Standard journey workspace identity binding drifted')
  assertUnique([minimal.workspaceIdentity, trusted.workspaceIdentity,
    ...managed.map((attempt) => attempt.workspaceIdentity)], 'profile/Managed workspace identities')
  assertUnique([minimal.executionIdentityDigest, trusted.executionIdentityDigest,
    ...managed.map((attempt) => attempt.executionIdentityDigest)], 'execution identity digests')
}

async function validateDisclosureEvidence(directoryPath, credentialCorpusFile, marker, doctor) {
  const directory = await exactDirectory(directoryPath, 'disclosure evidence directory')
  const entries = (await readdir(directory)).sort()
  assertSameArray(entries, disclosureEntries, 'disclosure evidence directory entries drifted')

  const corpusBytes = await readExactBoundedFile(credentialCorpusFile, 'private credential corpus', 256 * 1024)
  assert(sha256Hex(corpusBytes) === marker.credentialCorpusSha256,
    'private credential corpus is not bound to the private marker')
  const corpus = parseJSON(corpusBytes, 'private credential corpus')
  validateCredentialCorpus(corpus, marker)
  const secrets = corpus.secrets.map(({ value }) => Buffer.from(value, 'utf8'))

  const artifactInputs = new Map()
  for (const artifact of disclosureArtifacts) {
    const bytes = await readExactBoundedFile(join(directory, artifact.name), `disclosure ${artifact.kind}`, 16 * 1024 * 1024)
    artifactInputs.set(artifact.name, { ...artifact, bytes, sha256: sha256Hex(bytes) })
  }
  const metadata = parseJSON(artifactInputs.get('screen-metadata.json').bytes, 'screenshot metadata scan')
  const ocr = parseJSON(artifactInputs.get('screen-ocr.json').bytes, 'screenshot OCR scan')
  validatePNGAndScans(artifactInputs.get('screen.png').bytes, metadata, ocr)

  const actualScans = disclosureArtifacts.map((artifact) => {
    const input = artifactInputs.get(artifact.name)
    const scan = scanBytes(input.bytes, secrets)
    assert(Object.values(scan).every((count) => count === 0),
      `disclosure ${artifact.kind} contains credential, secret, private-path, email, or URL material`)
    return { name: artifact.name, kind: artifact.kind, bytes: input.bytes.length, sha256: input.sha256, ...scan }
  })
  const recordInput = await readJSONWithDigest(join(directory, 'disclosure.json'), 'disclosure record', 1024 * 1024)
  validateDisclosureRecord(recordInput.value, marker, doctor, actualScans,
    artifactInputs.get('screen.png').sha256, metadata, ocr)
  return { recordSha256: recordInput.sha256 }
}

function validateCredentialCorpus(value, marker) {
  assertPlainObject(value, 'private credential corpus')
  assertExactKeys(value, ['schemaVersion', 'corpusId', 'secrets'], 'private credential corpus')
  assert(value.schemaVersion === credentialCorpusSchema && value.corpusId === marker.credentialCorpusId,
    'private credential corpus identity drifted')
  assert(Array.isArray(value.secrets) && value.secrets.length >= 2 && value.secrets.length <= 32,
    'private credential corpus is empty or unbounded')
  const names = []
  const values = []
  for (const secret of value.secrets) {
    assertPlainObject(secret, 'private credential corpus entry')
    assertExactKeys(secret, ['name', 'value'], 'private credential corpus entry')
    assert(typeof secret.name === 'string' && /^[a-z][a-z0-9_-]{1,63}$/.test(secret.name),
      'private credential corpus name is invalid')
    assert(typeof secret.value === 'string' && secret.value.length >= 16 && secret.value.length <= 4096,
      'private credential corpus value is invalid')
    names.push(secret.name)
    values.push(secret.value)
  }
  assertUnique(names, 'private credential corpus names')
  assertUnique(values, 'private credential corpus values')
}

function validatePNGAndScans(png, metadata, ocr) {
  assert(png.length >= 20 && png.subarray(0, 8).equals(pngSignature), 'screenshot is not a PNG')
  const chunkTypes = parsePNGChunkTypes(png)
  assert(!chunkTypes.some((type) => ['tEXt', 'zTXt', 'iTXt', 'eXIf'].includes(type)),
    'screenshot contains unsafe metadata chunks')
  const pngSha256 = sha256Hex(png)

  assertPlainObject(metadata, 'screenshot metadata scan')
  assertExactKeys(metadata,
    ['schemaVersion', 'pngSha256', 'completed', 'chunkTypes', 'unsafeMatchCount'],
    'screenshot metadata scan')
  assert(metadata.schemaVersion === screenshotMetadataSchema && metadata.pngSha256 === pngSha256 &&
    metadata.completed === true && metadata.unsafeMatchCount === 0,
  'screenshot metadata scan is incomplete or spliced')
  assertSameArray(metadata.chunkTypes, chunkTypes, 'screenshot metadata chunk audit drifted')

  // E consumes the exact system-managed Vision OCR v2 record already validated
  // and bound by phase D; accepting the superseded synthetic v1 scan here would
  // make the final disclosure contract impossible for a real run.
  validateScreenshotOCR(ocr, png)
}

function validateDisclosureRecord(value, marker, doctor, actualScans, pngSha256, metadata, ocr) {
  assertPlainObject(value, 'disclosure record')
  assertExactKeys(value, [
    'schemaVersion', 'status', 'environmentId', 'installId', 'generationId',
    'credentialCorpusId', 'artifacts', 'screenshot', 'digest',
  ], 'disclosure record')
  assert(value.schemaVersion === DISCLOSURE_RECORD_SCHEMA && value.status === 'passed',
    'disclosure record schema/status drifted')
  assertSameIdentity(value, doctor, 'disclosure/Doctor')
  assert(value.credentialCorpusId === marker.credentialCorpusId,
    'disclosure record credential corpus identity drifted')
  assertSameJSON(value.artifacts, actualScans, 'disclosure record artifact scans drifted')
  assertPlainObject(value.screenshot, 'disclosure screenshot binding')
  assertExactKeys(value.screenshot, [
    'pngSha256', 'metadataSha256', 'ocrSha256', 'metadataUnsafeMatches', 'ocrUnsafeMatches',
  ], 'disclosure screenshot binding')
  assert(value.screenshot.pngSha256 === pngSha256 &&
    value.screenshot.metadataSha256 === sha256Hex(canonicalJSONStringify(metadata)) &&
    value.screenshot.ocrSha256 === sha256Hex(canonicalJSONStringify(ocr)) &&
    value.screenshot.metadataUnsafeMatches === 0 && value.screenshot.ocrUnsafeMatches === 0,
  'disclosure screenshot scan binding drifted')
  assertDigest(value.digest, 'disclosure record digest')
  const { digest, ...safe } = value
  assert(digest === sha256Hex(canonicalJSONStringify(safe)), 'disclosure record digest drifted')
}

function scanBytes(bytes, secrets) {
  const text = bytes.toString('utf8')
  return {
    credentialMatches: secrets.reduce((count, secret) => count + countBuffer(bytes, secret), 0),
    genericSecretMatches: countPatterns(text, [
      /-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----/g,
      /\bsk-[A-Za-z0-9_-]{20,}\b/g,
      /\bgh[pousr]_[A-Za-z0-9]{30,}\b/g,
      /\bAKIA[0-9A-Z]{16}\b/g,
      /\bxox[baprs]-[0-9A-Za-z-]{20,}\b/g,
      /(?:password|token|cookie|api[_-]?key|authorization)\s*[:=]\s*["']?[^\s,"']{8,}/gi,
    ]),
    privatePathMatches: countPatterns(text, [/(?:\/Users\/|\/home\/)[^/\s]+/g, /[A-Za-z]:\\Users\\[^\\\s]+/g]),
    emailMatches: countPatterns(text, [/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/gi]),
    urlMatches: countPatterns(text, [/\bhttps?:\/\/[^\s"']+/gi]),
  }
}

function countBuffer(bytes, needle) {
  let count = 0
  let offset = 0
  while ((offset = bytes.indexOf(needle, offset)) !== -1) {
    count++
    offset += Math.max(needle.length, 1)
  }
  return count
}

function countPatterns(text, patterns) {
  let count = 0
  for (const pattern of patterns) count += [...text.matchAll(pattern)].length
  return count
}

function parsePNGChunkTypes(png) {
  const types = []
  let offset = 8
  while (offset + 12 <= png.length) {
    const length = png.readUInt32BE(offset)
    assert(Number.isSafeInteger(length) && length <= 16 * 1024 * 1024 && offset + 12 + length <= png.length,
      'screenshot PNG chunk is invalid or unbounded')
    const type = png.subarray(offset + 4, offset + 8).toString('ascii')
    assert(/^[A-Za-z]{4}$/.test(type), 'screenshot PNG chunk type is invalid')
    types.push(type)
    offset += 12 + length
    if (type === 'IEND') break
  }
  assert(types[0] === 'IHDR' && types.at(-1) === 'IEND' && offset === png.length,
    'screenshot PNG structure is incomplete')
  return types
}

export async function computeWebDirectoryAggregate(directoryPath) {
  const directory = await exactDirectory(directoryPath, 'installed web directory')
  const files = []
  await collectDirectoryFiles(directory, directory, files)
  assert(files.length > 0 && files.length <= 4096, 'installed web file set is empty or unbounded')
  files.sort((left, right) => left.path.localeCompare(right.path))
  let framing = ''
  let totalBytes = 0
  for (const file of files) {
    totalBytes += file.bytes.length
    assert(totalBytes <= 64 * 1024 * 1024, 'installed web directory exceeds the bounded size')
    framing += `${file.path}\0${file.bytes.length}\0${sha256Hex(file.bytes)}\n`
  }
  return sha256Hex(framing)
}

async function collectDirectoryFiles(root, directory, files) {
  const names = (await readdir(directory)).sort()
  for (const name of names) {
    assert(name !== '.' && name !== '..' && !name.includes('\0'), 'installed web entry name is invalid')
    const path = join(directory, name)
    const info = await lstat(path)
    assert(!info.isSymbolicLink(), 'installed web directory contains a symlink')
    if (info.isDirectory()) await collectDirectoryFiles(root, path, files)
    else {
      assert(info.isFile() && info.size <= 16 * 1024 * 1024, 'installed web entry is not a bounded regular file')
      const bytes = await readExactBoundedFile(path, 'installed web file', 16 * 1024 * 1024)
      const relativePath = relative(root, path).split(sep).join('/')
      assertSafeRelativePath(relativePath, 'installed web relative path')
      files.push({ path: relativePath, bytes })
    }
  }
}

async function exactDirectory(path, label) {
  const directory = exactAbsolutePath(path, label)
  const info = await lstat(directory)
  assert(info.isDirectory() && !info.isSymbolicLink(), `${label} must be one exact regular non-symlink directory`)
  return directory
}

async function readJSONWithDigest(path, label, maxBytes) {
  const bytes = await readExactBoundedFile(path, label, maxBytes)
  return { value: parseJSON(bytes, label), sha256: sha256Hex(bytes) }
}

function parseJSON(bytes, label) {
  try { return JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) }
}

function validateRoleReuseBinding(roles, reuse) {
  const expectedImages = Object.fromEntries(O4_ROLES.map((role) => [role, {
    artifactId: roles[role].artifactId,
    archiveSha256: roles[role].archiveSha256,
    archiveSize: roles[role].archiveSize,
    dockerConfigImageId: roles[role].dockerConfigImageId,
  }]))
  assertSameJSON(reuse.roleImages, expectedImages, 'Doctor/image reuse exact role images drifted')
  assert(reuse.roleTupleDigest === computeImageTupleDigest(expectedImages), 'image reuse tuple binding drifted')
}

function validateAggregateCounts(value) {
  assertPlainObject(value, 'O4 aggregate counts')
  assertExactKeys(value, [
    'managedAttempts', 'profileJourneys', 'recoveryScenarios', 'setupLoads',
    'attemptBuildPullLoad', 'retryBuildPullLoad', 'restartBuildPullLoad', 'recoveryBuildPullLoad',
    'verifierBuildPullLoad',
    'terminalOwnedResidue', 'activeReferences', 'recoverableReferences', 'sharedExactImagesPresent',
    'disclosureArtifacts', 'credentialMatches', 'genericSecretMatches', 'privatePathMatches',
    'emailMatches', 'urlMatches', 'screenshotMetadataUnsafeMatches', 'screenshotOCRUnsafeMatches',
  ], 'O4 aggregate counts')
  const expected = {
    managedAttempts: 3, profileJourneys: 3, recoveryScenarios: 8, setupLoads: 2,
    attemptBuildPullLoad: 0, retryBuildPullLoad: 0, restartBuildPullLoad: 0,
    recoveryBuildPullLoad: 0, verifierBuildPullLoad: 0, terminalOwnedResidue: 0, activeReferences: 0,
    recoverableReferences: 0, sharedExactImagesPresent: 2, disclosureArtifacts: 7,
    credentialMatches: 0, genericSecretMatches: 0, privatePathMatches: 0,
    emailMatches: 0, urlMatches: 0, screenshotMetadataUnsafeMatches: 0,
    screenshotOCRUnsafeMatches: 0,
  }
  assertSameJSON(value, expected, 'O4 aggregate counts drifted')
}

function validateComponents(value) {
  assertPlainObject(value, 'O4 publication components')
  assertExactKeys(value, [
    'installedDoctorRecordSha256', 'engineQualificationSha256', 'modelObservationSha256',
    'imageReuseRecordSha256', 'profileJourneySha256',
    'recoveryRecordSha256', 'disclosureRecordSha256',
  ], 'O4 publication components')
  for (const key of ['installedDoctorRecordSha256', 'engineQualificationSha256',
    'modelObservationSha256', 'imageReuseRecordSha256', 'recoveryRecordSha256',
    'disclosureRecordSha256']) assertDigest(value[key], `O4 component ${key}`)
  assert(Array.isArray(value.profileJourneySha256) && value.profileJourneySha256.length === 3,
    'O4 profile journey components are incomplete')
  const profiles = []
  for (const item of value.profileJourneySha256) {
    assertPlainObject(item, 'O4 profile journey component')
    assertExactKeys(item, ['profile', 'sha256'], 'O4 profile journey component')
    assert(['minimal', 'standard', 'trusted_local'].includes(item.profile),
      'O4 profile journey component profile is invalid')
    assertDigest(item.sha256, 'O4 profile journey component digest')
    profiles.push(item.profile)
  }
  assertSameArray(profiles, ['minimal', 'standard', 'trusted_local'], 'O4 profile component order drifted')
}

function assertSafePublication(value) {
  assertSafePublicValue(value)
  const serialized = JSON.stringify(value)
  assert(!/(?:markerNonce|credentialCorpusSha256|argv|daemonId|privateRoot|rawPath)/i.test(serialized),
    'O4 publication contains private/raw identity material')
}

function validateZeroResidue(value, label) {
  assertPlainObject(value, label)
  assertExactKeys(value, residueKeys, label)
  for (const key of residueKeys) assert(value[key] === 0, `${label} ${key} is non-zero`)
}

function assertSameIdentity(left, right, label) {
  assert(left.environmentId === right.environmentId && left.installId === right.installId &&
    left.generationId === right.generationId, `${label} identity/generation drifted`)
}

function assertOpaque(value, pattern, label) {
  assert(typeof value === 'string' && pattern.test(value), `${label} is invalid`)
}

function assertSafeRelativePath(value, label) {
  assert(typeof value === 'string' && value.length > 0 && !value.includes('\0') && !value.includes('\\') &&
    !value.startsWith('/') && posix.normalize(value) === value && value !== '.' && value !== '..' &&
    !value.startsWith('../'), `${label} is invalid`)
}

function assertPlainObject(value, label) {
  assert(value !== null && typeof value === 'object' && !Array.isArray(value) &&
    (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null),
  `${label} must be a plain object`)
}

function assertExactKeys(value, expected, label) {
  const actual = Object.keys(value).sort()
  const wanted = [...expected].sort()
  assert(actual.length === wanted.length && actual.every((item, index) => item === wanted[index]),
    `${label} fields drifted`)
}

function assertSameArray(actual, expected, message) {
  assert(Array.isArray(actual) && actual.length === expected.length &&
    actual.every((item, index) => item === expected[index]), message)
}

function assertSameJSON(actual, expected, message) {
  assert(canonicalJSONStringify(actual) === canonicalJSONStringify(expected), message)
}

function assertUnique(values, label) {
  assert(new Set(values).size === values.length, `${label} are not unique`)
}

function clone(value) {
  return JSON.parse(JSON.stringify(value))
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

export function parseCLI(argv) {
  const mapping = {
    '--doctor-record': 'doctorRecordFile', '--image-reuse-record': 'imageReuseRecordFile',
    '--source-a3-ledger': 'sourceA3LedgerFile',
    '--private-marker': 'privateEnvironmentMarkerFile', '--candidate-manifest': 'candidateManifestFile',
    '--source-manifest': 'sourceManifestFile', '--source-bundle-root': 'sourceBundleRoot',
    '--binary': 'binaryFile', '--web-directory': 'webDirectory',
    '--release-spec': 'releaseSpecFile', '--release-manifest': 'releaseManifestFile',
    '--offline-bundle-evidence': 'offlineBundleEvidenceFile', '--path-pi-provenance': 'pathPiProvenanceFile',
    '--private-pi-provenance': 'privatePiProvenanceFile', '--qualification': 'qualificationFile',
    '--model-observation': 'modelObservationFile',
    '--engine-endpoint': 'engineEndpointEvidenceFile', '--profile-journeys': 'profileJourneyDirectory',
    '--recovery-record': 'recoveryRecordFile', '--disclosure-directory': 'disclosureDirectory',
    '--credential-corpus': 'credentialCorpusFile', '--output': 'outputPath',
    '--service-controller': 'serviceControllerFile',
    '--service-controller-sha256': 'serviceControllerSha256',
  }
  const values = {}
  for (let index = 0; index < argv.length; index += 2) {
    const key = mapping[argv[index]]
    assert(key && index + 1 < argv.length && values[key] === undefined,
      'O4 installed evidence arguments are invalid')
    values[key] = argv[index + 1]
  }
  for (const key of Object.values(mapping)) assert(typeof values[key] === 'string', 'O4 installed evidence argument is missing')
  const { outputPath, serviceControllerFile, serviceControllerSha256, ...inputs } = values
  return { inputs, outputPath, repositoryCapture: {
    executableFile: serviceControllerFile, executableSha256: serviceControllerSha256,
  } }
}

async function main() {
  try {
    const { inputs, outputPath, repositoryCapture } = parseCLI(process.argv.slice(2))
    const result = await publishInstalledEvidence(inputs, outputPath, { repositoryCapture })
    process.stdout.write(`${JSON.stringify(result)}\n`)
  } catch {
    process.stdout.write(`${JSON.stringify({ schemaVersion: O4_PUBLICATION_SCHEMA, status: 'failed' })}\n`)
    process.exitCode = 1
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main()
