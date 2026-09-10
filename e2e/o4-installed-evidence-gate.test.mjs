import assert from 'node:assert/strict'
import {
  mkdir, mkdtemp, readFile, stat, unlink, writeFile,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'

import {
  INSTALLED_DOCTOR_REPORT_SCHEMA,
  PRIVATE_ENVIRONMENT_MARKER_SCHEMA,
  materializeInstalledDoctorEvidence,
  canonicalJSONStringify,
  deriveEnvironmentId,
  sha256Hex,
} from './o4-installed-doctor-record.mjs'
import {
  DOCKER_OPERATION_LEDGER_SCHEMA,
  RESOURCE_LEDGER_SCHEMA,
  SOURCE_A3_LEDGER_SCHEMA,
  buildImageReuseRecord,
  computeAuditSliceDigest,
  computeCommandAuditRecordDigest,
  computeExecutionIdentityDigest,
  computeImageTupleDigest,
} from './o4-image-reuse-record.mjs'
import {
  DISCLOSURE_RECORD_SCHEMA,
  O4_PUBLICATION_SCHEMA,
  PROFILE_JOURNEY_SCHEMA,
  RECOVERY_RECORD_SCHEMA,
  assertInstalledEvidencePublication,
  computeWebDirectoryAggregate,
  parseCLI,
  publishInstalledEvidence as publishInstalledEvidenceRaw,
  validateInstalledEvidence,
  validateSourceManifest,
} from './o4-installed-evidence-gate.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'

let repositoryCapture
let cleanupRepositoryCapture
test.before(async () => {
  ({ capture: repositoryCapture, cleanup: cleanupRepositoryCapture } =
    await buildTestRepositoryCapture())
})
test.after(async () => { await cleanupRepositoryCapture() })

function publishInstalledEvidence(inputs, outputPath) {
  return publishInstalledEvidenceRaw(inputs, outputPath, { repositoryCapture })
}

const roles = ['managed_pi_runtime', 'network_boundary', 'independent_verifier', 'capability_probe']
const managedAliasRoles = ['managed_pi_runtime', 'independent_verifier', 'capability_probe']
const digest = (label) => sha256Hex(`gate-test:${label}`)
const opaque = (prefix, token) => `${prefix}_${String(token).repeat(32)}`
const productID = (prefix, number) =>
  `${prefix}_01890f12-3456-7${number.toString(16).padStart(3, '0')}-8abc-${number.toString(16).padStart(12, '0')}`
const workspaceIdentity = (label) => `sha256:${digest(`workspace:${label}`)}`
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64')
const zeroResidue = () => ({
  ownedContainers: 0, ownedNetworks: 0, ownedVolumes: 0, ownedConfigs: 0,
  ownedWorkspaces: 0, ownedProcessGroups: 0, activeReferences: 0, recoverableReferences: 0,
})
const emptyInventory = () => Object.fromEntries(Object.keys(zeroResidue()).map((key) => [key, []]))
const zeroScan = () => ({
  credentialMatches: 0, genericSecretMatches: 0, privatePathMatches: 0, emailMatches: 0, urlMatches: 0,
})

async function writeJSON(path, value) {
  const encoded = `${JSON.stringify(value)}\n`
  await writeFile(path, encoded)
  return sha256Hex(encoded)
}

function withDigest(value) {
  return { ...value, digest: sha256Hex(canonicalJSONStringify(value)) }
}

function sealAudit(drafts) {
  let previousDigest = '0'.repeat(64)
  return drafts.map((draft, index) => {
    const safe = { sequence: index + 1, ...draft, previousDigest }
    const record = { ...safe, digest: computeCommandAuditRecordDigest(safe) }
    previousDigest = record.digest
    return record
  })
}

function auditDraft(phase, subsystem, operationClass, safeTargetSha256 = null) {
  return {
    phase, subsystem, invocation: operationClass === 'start' ? 'start' : 'run',
    operationClass, safeTargetSha256, result: 'succeeded',
  }
}

function buildCommandAudit(roleImages) {
  const drafts = [
    auditDraft('setup', 'installation', 'context'),
    auditDraft('setup', 'installation', 'load', roleImages.managed_pi_runtime.archiveSha256),
    auditDraft('setup', 'installation', 'inspect', roleImages.managed_pi_runtime.dockerConfigImageId.slice(7)),
    auditDraft('setup', 'installation', 'load', roleImages.network_boundary.archiveSha256),
    auditDraft('setup', 'installation', 'inspect', roleImages.network_boundary.dockerConfigImageId.slice(7)),
  ]
  const appendAttempt = () => drafts.push(
    auditDraft('attempt', 'managed', 'create'), auditDraft('attempt', 'managed', 'start'),
    auditDraft('attempt', 'managed', 'exec'), auditDraft('attempt', 'managed', 'remove'),
  )
  const appendVerifier = () => drafts.push(
    auditDraft('verifier', 'verifier', 'inspect', roleImages.independent_verifier.dockerConfigImageId.slice(7)),
    auditDraft('verifier', 'verifier', 'create'), auditDraft('verifier', 'verifier', 'exec'),
    auditDraft('verifier', 'verifier', 'remove'),
  )
  appendAttempt() // Minimal source A3 Attempt, not part of the selected Standard cohort.
  appendVerifier()
  drafts.push(auditDraft('retry', 'managed', 'list'))
  appendAttempt()
  appendVerifier()
  drafts.push(auditDraft('retry', 'managed', 'list'))
  appendAttempt()
  appendVerifier()
  drafts.push(auditDraft('restart', 'managed', 'list'))
  appendAttempt()
  appendVerifier()
  for (const scenario of ['failure', 'cancel', 'timeout', 'orphan']) {
    drafts.push(auditDraft('recovery', 'managed', 'list', digest(`source-scenario:${scenario}`)))
    appendAttempt()
  }
  drafts.push(auditDraft('recovery', 'managed', 'list', digest('source-scenario:retry')))
  appendAttempt()
  appendVerifier()
  drafts.push(auditDraft('recovery', 'managed', 'list'))
  return sealAudit(drafts)
}

function commandAuditAttemptWindows(audit) {
  const windows = []
  let current = []
  for (const record of audit) {
    if (record.phase === 'attempt') current.push(record)
    else if (current.length > 0) {
      windows.push(current)
      current = []
    }
  }
  if (current.length > 0) windows.push(current)
  return windows
}

function commandAuditPhaseWindows(audit, phase) {
  const windows = []
  let current = []
  for (const record of audit) {
    if (record.phase === phase) current.push(record)
    else if (current.length > 0) {
      windows.push(current)
      current = []
    }
  }
  if (current.length > 0) windows.push(current)
  return windows
}

function selectedAuditBinding(audit, standardWindows, standardVerifierWindows) {
  const specs = [
    ['setup', 1, commandAuditPhaseWindows(audit, 'setup')[0]],
    ...standardWindows.flatMap((records, index) => [
      ['standard_attempt', index + 1, records],
      ['standard_verifier', index + 1, standardVerifierWindows[index]],
    ]),
  ]
  const selected = specs.flatMap(([, , records]) => records)
  return {
    commandAudit: selected,
    selectedA3AuditRanges: specs.map(([kind, ordinal, records]) => ({
      kind, ordinal, startSequence: records[0].sequence, endSequence: records.at(-1).sequence,
      auditSliceDigest: computeAuditSliceDigest(records),
    })),
    selectedA3AuditSequenceList: selected.map(({ sequence }) => sequence),
  }
}

function sourceAttempt(projected, sourceAttemptOrdinal, profile, cohortSequence, {
  scenario = null, verifier = null, verifierRequired = verifier !== null,
} = {}) {
  const { sequence, ...attemptFields } = projected
  void sequence
  const truth = profile === 'scenario' ? {
    failure: { attemptState: 'failed', terminalReason: 'runtime_exit_nonzero' },
    cancel: { attemptState: 'cancelled', terminalReason: null },
    timeout: { attemptState: 'failed', terminalReason: 'attempt_timeout' },
    orphan: { attemptState: 'interrupted', terminalReason: null },
    retry: { attemptState: 'output_submitted', terminalReason: null },
  }[scenario] : { attemptState: 'output_submitted', terminalReason: null }
  assert(truth)
  return {
    sourceAttemptOrdinal, profile, cohortSequence, scenario, ...truth, verifierRequired,
    verifierStartSequence: verifier?.[0].sequence ?? null,
    verifierEndSequence: verifier?.at(-1).sequence ?? null,
    verifierSliceDigest: verifier === null ? null : computeAuditSliceDigest(verifier),
    ...attemptFields,
  }
}

async function rewriteRecord(path, mutate) {
  const value = JSON.parse(await readFile(path, 'utf8'))
  mutate(value)
  if (Object.hasOwn(value, 'digest')) {
    const { digest: ignored, ...safe } = value
    void ignored
    value.digest = sha256Hex(canonicalJSONStringify(safe))
  }
  await writeJSON(path, value)
}

const sourcePathsDigest = (paths) => sha256Hex(paths.map((path) => `${path}\n`).join(''))
const sourceEntriesAggregate = (entries) => sha256Hex(entries.map((entry) =>
  `${entry.path}\0${entry.mode}\0${entry.size}\0${entry.sha256}\n`).join(''))
const projectionIdentity = (entries) => ({
  files: entries.length,
  paths_sha256: sourcePathsDigest(entries.map((entry) => entry.path)),
  aggregate_sha256: sourceEntriesAggregate(entries),
})

async function createSourceBundle(root) {
  const sourceRoot = join(root, 'source')
  const declaredPaths = [
    '.npmrc', 'contracts/frozen.txt', 'distribution/v1/policies/source-path-policy.v1.json',
    'go.mod', 'package.json', 'vendor/module.txt',
  ]
  const installOnlyPaths = ['contracts/frozen.txt', 'vendor/module.txt']
  const modelReadablePaths = declaredPaths.filter((path) => !installOnlyPaths.includes(path))
  const policy = {
    schema_version: 'chora.source-path-policy.v1',
    roots: ['.npmrc', 'contracts', 'distribution', 'go.mod', 'package.json', 'vendor'],
    excluded_directory_names: ['node_modules'],
    excluded_file_suffixes: ['.tsbuildinfo'],
    declared_files: declaredPaths.length,
    declared_paths_sha256: sourcePathsDigest(declaredPaths),
    install_only_roots: ['vendor'],
    install_only_paths: ['contracts/frozen.txt'],
    install_only_files: installOnlyPaths.length,
    install_only_paths_sha256: sourcePathsDigest(installOnlyPaths),
    model_readable_files: modelReadablePaths.length,
    model_readable_paths_sha256: sourcePathsDigest(modelReadablePaths),
  }
  const files = new Map([
    ['.npmrc', 'engine-strict=true\n'],
    ['contracts/frozen.txt', 'frozen install-only input\n'],
    ['go.mod', 'module example.test/chora\n'],
    ['package.json', '{"private":true}\n'],
    ['vendor/module.txt', 'offline dependency\n'],
  ])
  for (const [path, body] of files) {
    const output = join(sourceRoot, ...path.split('/'))
    await mkdir(dirname(output), { recursive: true })
    await writeFile(output, body, { mode: 0o644 })
  }
  const sourcePolicyFile = join(sourceRoot, 'distribution/v1/policies/source-path-policy.v1.json')
  await mkdir(dirname(sourcePolicyFile), { recursive: true })
  await writeJSON(sourcePolicyFile, policy)

  const entries = []
  for (const path of declaredPaths) {
    const input = join(sourceRoot, ...path.split('/'))
    const bytes = await readFile(input)
    const info = await stat(input)
    entries.push({
      path, size: bytes.length, mode: (info.mode & 0o777).toString(8).padStart(4, '0'),
      sha256: sha256Hex(bytes),
    })
  }
  const installOnlySet = new Set(installOnlyPaths)
  const installOnlyEntries = entries.filter((entry) => installOnlySet.has(entry.path))
  const modelReadableEntries = entries.filter((entry) => !installOnlySet.has(entry.path))
  const sourceManifest = {
    schema_version: 'chora.local-alpha-source-bundle.v1',
    aggregate_sha256: sourceEntriesAggregate(entries),
    source_path_policy: {
      path: 'distribution/v1/policies/source-path-policy.v1.json',
      sha256: sha256Hex(await readFile(sourcePolicyFile)),
    },
    install_only_projection: projectionIdentity(installOnlyEntries),
    model_readable_projection: projectionIdentity(modelReadableEntries),
    files: entries,
  }
  const sourceManifestFile = join(root, 'source-manifest.json')
  const sourceManifestSha256 = await writeJSON(sourceManifestFile, sourceManifest)
  return {
    sourceBundleRoot: root, sourceManifestFile, sourceManifestSha256, sourceManifest,
    sourcePolicyFile, sourcePolicy: policy,
  }
}

async function fixture(seed = 5) {
  const root = await mkdtemp(join(tmpdir(), 'chora-o4-gate-'))
  const nonce = Buffer.alloc(32, seed).toString('base64url')
  const identity = {
    environmentId: deriveEnvironmentId(nonce),
    installId: opaque('ins', String(seed)),
    generationId: opaque('gen', String(seed + 1)),
  }

  const credentialCorpus = {
    schemaVersion: 'chora.m1-o4-private-credential-corpus.v1',
    corpusId: opaque('cor', String(seed + 2)),
    secrets: [
      // Match the 1,788-byte access-token boundary in the pinned private input.
      { name: 'model_key', value: `sk-${'A'.repeat(1784)}${seed}` },
      { name: 'session_secret', value: `private-session-${'B'.repeat(28)}-${seed}` },
    ],
  }
  const credentialCorpusFile = join(root, 'credential-corpus.json')
  const corpusSha256 = await writeJSON(credentialCorpusFile, credentialCorpus)
  const marker = {
    schemaVersion: PRIVATE_ENVIRONMENT_MARKER_SCHEMA,
    markerNonce: nonce,
    ...identity,
    credentialCorpusId: credentialCorpus.corpusId,
    credentialCorpusSha256: corpusSha256,
  }
  const privateEnvironmentMarkerFile = join(root, 'private-marker.json')
  await writeJSON(privateEnvironmentMarkerFile, marker)

  const source = await createSourceBundle(root)
  const { sourceBundleRoot, sourceManifestFile, sourceManifestSha256, sourceManifest } = source
  const sourceAggregate = sourceManifest.aggregate_sha256

  const binaryFile = join(root, 'chora-bin')
  const binaryBytes = Buffer.from(`synthetic-installed-binary-${seed}\n`)
  await writeFile(binaryFile, binaryBytes, { mode: 0o700 })
  const binarySha256 = sha256Hex(binaryBytes)
  const webDirectory = join(root, 'web')
  await mkdir(join(webDirectory, 'assets'), { recursive: true })
  await writeFile(join(webDirectory, 'index.html'), '<main>Chora</main>\n')
  await writeFile(join(webDirectory, 'assets', 'app.js'), 'globalThis.chora=true\n')
  const webAggregateSha256 = await computeWebDirectoryAggregate(webDirectory)

  const managedBinding = {
    artifactId: 'managed-pi-runtime', archiveSha256: digest(`managed-archive:${seed}`),
    archiveSize: 101, dockerConfigImageId: `sha256:${digest(`managed-config:${seed}`)}`,
  }
  const boundaryBinding = {
    artifactId: 'network-boundary', archiveSha256: digest(`boundary-archive:${seed}`),
    archiveSize: 103, dockerConfigImageId: `sha256:${digest(`boundary-config:${seed}`)}`,
  }
  const roleBindings = Object.fromEntries(roles.map((role, index) => [role, {
    ...(role === 'network_boundary' ? boundaryBinding : managedBinding),
    policyDigest: digest(`policy:${index}:${seed}`),
  }]))
  const roleArray = roles.map((role) => ({ role, ...roleBindings[role] }))
  const releaseSpec = {
    schemaVersion: 'chora.m1-o4-release-spec.v1', generationId: identity.generationId,
    platform: { os: 'darwin', architecture: 'arm64' }, roles: roleArray,
  }
  const releaseSpecFile = join(root, 'release-spec.json')
  const releaseSpecSha256 = await writeJSON(releaseSpecFile, releaseSpec)
  const releaseManifest = {
    schemaVersion: 'chora.m1-o4-release-manifest.v1', generationId: identity.generationId,
    releaseSpecSha256, roles: roleArray,
  }
  const releaseManifestFile = join(root, 'release-manifest.json')
  const releaseManifestSha256 = await writeJSON(releaseManifestFile, releaseManifest)
  const offlineBundleEvidence = {
    schemaVersion: 'chora.m1-o4-offline-release-bundle-evidence.v1',
    generationId: identity.generationId, releaseManifestSha256,
    platform: { os: 'linux', architecture: 'arm64' },
    artifacts: [
      { artifactId: 'managed-pi-runtime',
        roles: ['capability_probe', 'independent_verifier', 'managed_pi_runtime'],
        policyDigests: Object.fromEntries(['capability_probe', 'independent_verifier', 'managed_pi_runtime']
          .map((role) => [role, roleBindings[role].policyDigest])),
        archive: { path: 'archives/managed-pi-runtime.tar', format: 'docker-archive', size: 101,
          sha256: managedBinding.archiveSha256 },
        expectedDockerConfigImageId: managedBinding.dockerConfigImageId },
      { artifactId: 'network-boundary', roles: ['network_boundary'],
        policyDigests: { network_boundary: roleBindings.network_boundary.policyDigest },
        archive: { path: 'archives/network-boundary.tar', format: 'docker-archive', size: 103,
          sha256: boundaryBinding.archiveSha256 },
        expectedDockerConfigImageId: boundaryBinding.dockerConfigImageId },
    ],
  }
  const offlineBundleEvidenceFile = join(root, 'offline-bundle-evidence.json')
  const offlineBundleEvidenceSha256 = await writeJSON(offlineBundleEvidenceFile, offlineBundleEvidence)

  const pathPiProvenanceFile = join(root, 'path-pi.json')
  const pathPiProvenanceSha256 = await writeJSON(pathPiProvenanceFile, {
    schemaVersion: 'chora.m1-o4-pi-provenance.v1', kind: 'path', generationId: identity.generationId,
    sourceIdentitySha256: digest(`path-source:${seed}`), binarySha256: digest(`path-binary:${seed}`),
  })
  const privatePiProvenanceFile = join(root, 'private-pi.json')
  const privatePiProvenanceSha256 = await writeJSON(privatePiProvenanceFile, {
    schemaVersion: 'chora.m1-o4-pi-provenance.v1', kind: 'private', generationId: identity.generationId,
    sourceIdentitySha256: digest(`private-source:${seed}`), binarySha256: digest(`private-binary:${seed}`),
  })

  const engineEndpointEvidenceFile = join(root, 'engine-endpoint.json')
  const endpointEvidenceSha256 = await writeJSON(engineEndpointEvidenceFile, {
    schemaVersion: 'chora.m1-o4-engine-endpoint-evidence.v1', status: 'passed',
    generationId: identity.generationId, platform: { os: 'darwin', architecture: 'arm64' },
    contextIdentitySha256: digest(`context:${seed}`), endpointIdentitySha256: digest(`endpoint:${seed}`),
  })
  const qualificationFile = join(root, 'engine-qualification.json')
  const modelObservationFile = join(root, 'model-observation.json')
  const doctorRecordFile = join(root, 'installed-doctor-report.json')

  const candidateManifest = {
    schemaVersion: 'chora.m1-o4-candidate-manifest.v1', ...identity,
    platform: { os: 'darwin', architecture: 'arm64' },
    sourceManifestSha256, sourceAggregateSha256: sourceAggregate,
    binarySha256, webAggregateSha256, releaseSpecSha256, releaseManifestSha256,
    offlineBundleEvidenceSha256,
  }
  const candidateManifestFile = join(root, 'candidate-manifest.json')
  const candidateManifestSha256 = await writeJSON(candidateManifestFile, candidateManifest)

  const bindings = {
    candidate: { candidateManifestSha256, sourceManifestSha256, sourceAggregateSha256: sourceAggregate },
    product: { binarySha256, webAggregateSha256 },
    release: { releaseSpecSha256, releaseManifestSha256, offlineBundleEvidenceSha256 },
    pi: { selected: 'path', pathProvenanceSha256: pathPiProvenanceSha256, privateProvenanceSha256: privatePiProvenanceSha256 },
    engine: { qualificationSha256: '0'.repeat(64), endpointEvidenceSha256 },
    roles: roleBindings,
  }
  const modelAuthority = { authFileSha256: digest(`auth:${seed}`), caFileSha256: digest(`ca:${seed}`),
    proxyURLSha256: digest(`proxy:${seed}`), modelURLSha256: digest(`model-url:${seed}`) }
  const engineIdentity = { schema_version: 'chora.docker-engine-identity/v1', daemon_id: `daemon-${seed}`,
    api_version: '1.47', operating_system: 'linux', architecture: 'arm64',
    context_endpoint_digest: digest(`endpoint:${seed}`), provider_name: 'Docker', engine_version: '29.6.1',
    context_name: 'chora-o4', identity_digest: digest(`engine:${seed}`) }
  const capabilityContract = { schema_version: 'chora.docker-capability-probe-contract/v1', contract_version: 1,
    minimum_api_version: '1.44', operating_system: 'linux', architecture: 'arm64',
    probe_image_id: roleBindings.capability_probe.dockerConfigImageId,
    sandbox_policy_digest: roleBindings.capability_probe.policyDigest,
    capabilities: ['exact-preexisting-image', 'zero-residue'], contract_digest: digest(`contract:${seed}`) }
  const setupReceiptSha256 = digest(`setup-receipt:${seed}`)
  const modelRequestAuthoritySha256 = digest(`model-authority:${seed}`)
  const productProof = { schema_version: 'chora.installed-product-proof/v1', status: 'passed',
    generation_id: identity.generationId, release_id: 'o4-release', manifest_sha256: releaseManifestSha256,
    setup_receipt_sha256: setupReceiptSha256,
    model_request_authority_sha256: modelRequestAuthoritySha256,
    docker_operations_read_only: true, engine_mutations_attempted: 0, resources_created: false,
    model_authenticated: true,
    input_binding: { auth_file_sha256: modelAuthority.authFileSha256, ca_file_sha256: modelAuthority.caFileSha256,
      proxy_url_sha256: modelAuthority.proxyURLSha256, model_url_sha256: modelAuthority.modelURLSha256 },
    engine_identity: engineIdentity, capability_contract: capabilityContract,
    engine_qualification: { schema_version: 'chora.docker-engine-qualification/v1',
      engine_identity_digest: engineIdentity.identity_digest, probe_contract_digest: capabilityContract.contract_digest,
      probe_image_id: capabilityContract.probe_image_id,
      sandbox_policy_digest: capabilityContract.sandbox_policy_digest,
      completed_at: '2026-08-30T00:00:00Z', qualification_digest: digest(`qualification:${seed}`) },
    role_images: roles.map((role) => ({ role, artifact_id: roleBindings[role].artifactId,
      config_image_id: roleBindings[role].dockerConfigImageId,
      archive_sha256: roleBindings[role].archiveSha256, archive_size: roleBindings[role].archiveSize,
      policy_sha256: roleBindings[role].policyDigest })),
    doctor: { status: 'passed', resourcesCreated: false, elapsed: 1,
      inputFingerprint: digest(`doctor-fingerprint:${seed}`),
      modelProbe: { maxAttempts: 3, attempts: [{ attempt: 1, outcome: 'authenticated_schema',
        statusCode: 400, retryable: false, retried: false }] },
      inputEvidence: { authFileSha256: modelAuthority.authFileSha256,
        caFileSha256: modelAuthority.caFileSha256, proxyUrlSha256: modelAuthority.proxyURLSha256,
        modelUrlSha256: modelAuthority.modelURLSha256 } },
  }
  const dynamic = await materializeInstalledDoctorEvidence({ repositoryCapture, productProof, identity,
    platform: { os: 'darwin', architecture: 'arm64' }, bindings,
    setupReceiptSha256, modelRequestAuthoritySha256, modelRequestAuthority: modelAuthority,
    releaseId: 'o4-release', manifestSha256: releaseManifestSha256,
    endpointDigest: digest(`endpoint:${seed}`),
    outputs: { engineQualificationFile: qualificationFile, modelObservationFile,
      installedDoctorReportFile: doctorRecordFile } })
  const doctor = dynamic.installedDoctorReport

  const roleImages = Object.fromEntries(roles.map((role) => [role, {
    artifactId: roleBindings[role].artifactId,
    archiveSha256: roleBindings[role].archiveSha256,
    archiveSize: roleBindings[role].archiveSize,
    dockerConfigImageId: roleBindings[role].dockerConfigImageId,
  }]))
  const commandAudit = buildCommandAudit(roleImages)
  const auditWindows = commandAuditAttemptWindows(commandAudit)
  const verifierWindows = commandAuditPhaseWindows(commandAudit, 'verifier')
  assert.equal(auditWindows.length, 9)
  assert.equal(verifierWindows.length, 5)
  const tupleIdentity = digest(`b2-tuple:${seed}`)
  const resourceLedgerFiles = []
  const attempts = []
  for (let sequence = 1; sequence <= 3; sequence++) {
    const runtimeIdentitySha256 = digest(`runtime:${seed}:${sequence}`)
    const identityFields = {
      taskId: productID('task', seed * 100 + sequence),
      runId: productID('run', seed * 100 + sequence + 10),
      attemptId: productID('attempt', seed * 100 + sequence + 20),
      workspaceIdentity: workspaceIdentity(`${seed}:${sequence}`), tupleIdentity,
    }
    const executionIdentityDigest = computeExecutionIdentityDigest(identityFields)
    const resource = {
      schemaVersion: RESOURCE_LEDGER_SCHEMA, ...identityFields, executionIdentityDigest, runtimeIdentitySha256,
      terminalStatus: 'succeeded', inventory: emptyInventory(),
    }
    const resourceFile = join(root, `resource-${sequence}.json`)
    const resourceLedgerSha256 = await writeJSON(resourceFile, resource)
    resourceLedgerFiles.push(resourceFile)
    attempts.push({
      sequence,
      ...identityFields, executionIdentityDigest,
      status: 'succeeded', runtimeIdentitySha256, resourceLedgerSha256,
      roleTupleDigest: computeImageTupleDigest(roleImages), frameworkRetryCount: 0, hiddenRetryCount: 0,
      auditStartSequence: auditWindows[sequence][0].sequence,
      auditEndSequence: auditWindows[sequence].at(-1).sequence,
      auditSliceDigest: computeAuditSliceDigest(auditWindows[sequence]),
      operationCounts: { build: 0, pull: 0, load: 0 }, terminalResidue: zeroResidue(),
    })
  }
  const minimalIdentity = {
    taskId: productID('task', seed * 100 + 90), runId: productID('run', seed * 100 + 91),
    attemptId: productID('attempt', seed * 100 + 92),
    workspaceIdentity: workspaceIdentity(`source-minimal:${seed}`), tupleIdentity,
  }
  const minimalAttempt = {
    sequence: 1, ...minimalIdentity,
    executionIdentityDigest: computeExecutionIdentityDigest(minimalIdentity), status: 'succeeded',
    runtimeIdentitySha256: digest(`source-minimal-runtime:${seed}`),
    resourceLedgerSha256: digest(`source-minimal-resource:${seed}`),
    roleTupleDigest: computeImageTupleDigest(roleImages), frameworkRetryCount: 0, hiddenRetryCount: 0,
    auditStartSequence: auditWindows[0][0].sequence,
    auditEndSequence: auditWindows[0].at(-1).sequence,
    auditSliceDigest: computeAuditSliceDigest(auditWindows[0]),
    operationCounts: { build: 0, pull: 0, load: 0 }, terminalResidue: zeroResidue(),
  }
  const scenarioSpecs = [
    ['failure', 'failed'], ['cancel', 'cancelled'], ['timeout', 'timed_out'],
    ['orphan', 'orphaned'], ['retry', 'succeeded'],
  ]
  const scenarioAttempts = scenarioSpecs.map(([scenario, status], index) => {
    const scenarioIdentity = {
      taskId: productID('task', seed * 100 + 93 + index),
      runId: productID('run', seed * 100 + 103 + index),
      attemptId: productID('attempt', seed * 100 + 113 + index),
      workspaceIdentity: workspaceIdentity(`source-${scenario}:${seed}`), tupleIdentity,
    }
    const window = auditWindows[index + 4]
    return {
      sequence: index + 5, ...scenarioIdentity,
      executionIdentityDigest: computeExecutionIdentityDigest(scenarioIdentity), status,
      runtimeIdentitySha256: digest(`source-${scenario}-runtime:${seed}`),
      resourceLedgerSha256: digest(`source-${scenario}-resource:${seed}`),
      roleTupleDigest: computeImageTupleDigest(roleImages), frameworkRetryCount: 0, hiddenRetryCount: 0,
      auditStartSequence: window[0].sequence, auditEndSequence: window.at(-1).sequence,
      auditSliceDigest: computeAuditSliceDigest(window),
      operationCounts: { build: 0, pull: 0, load: 0 }, terminalResidue: zeroResidue(),
    }
  })
  const sourceA3LedgerFile = join(root, 'source-a3-ledger.json')
  const sourceA3Ledger = {
    schemaVersion: SOURCE_A3_LEDGER_SCHEMA, status: 'passed', ...identity,
    platform: { os: 'darwin', architecture: 'arm64' }, bindingDigest: doctor.bindingDigest,
    tupleIdentity, observationSource: 'exact-runner-command-audit', complete: true,
    engineEventsUsed: false, roleImages, commandAudit, auditRecordCount: commandAudit.length,
    auditFinalDigest: commandAudit.at(-1).digest, auditSealed: true,
    attempts: [
      sourceAttempt(minimalAttempt, 1, 'minimal', null, { verifier: verifierWindows[0] }),
      ...attempts.map((value, index) => sourceAttempt(value, index + 2, 'standard', index + 1, {
        verifier: verifierWindows[index + 1],
      })),
      ...scenarioAttempts.map((value, index) => sourceAttempt(value, index + 5, 'scenario', null, {
        scenario: scenarioSpecs[index][0],
        verifier: scenarioSpecs[index][0] === 'retry' ? verifierWindows[4] : null,
        verifierRequired: scenarioSpecs[index][0] === 'retry',
      })),
    ],
  }
  const sourceA3LedgerSha256 = await writeJSON(sourceA3LedgerFile, sourceA3Ledger)
  const projection = selectedAuditBinding(commandAudit, auditWindows.slice(1, 4), verifierWindows.slice(1, 4))
  const operationLedgerFile = join(root, 'operation-ledger.json')
  await writeJSON(operationLedgerFile, {
    schemaVersion: DOCKER_OPERATION_LEDGER_SCHEMA, status: 'passed', ...identity,
    platform: { os: 'darwin', architecture: 'arm64' }, bindingDigest: doctor.bindingDigest,
    tupleIdentity, observationSource: 'e-derived-sealed-a3-ledger-projection',
    complete: true, engineEventsUsed: false,
    sourceA3LedgerSha256,
    sourceA3AuditRecordCount: commandAudit.length,
    sourceA3AuditFinalDigest: commandAudit.at(-1).digest,
    ...projection,
    roleImages,
    setupOperations: [
      {
        sequence: 1, auditSequence: 2, inspectionAuditSequence: 3, phase: 'setup', operationClass: 'load',
        artifactId: 'managed-pi-runtime', roles: managedAliasRoles,
        ...roleImages.managed_pi_runtime, result: 'succeeded',
      },
      {
        sequence: 2, auditSequence: 4, inspectionAuditSequence: 5, phase: 'setup', operationClass: 'load',
        artifactId: 'network-boundary', roles: ['network_boundary'],
        ...roleImages.network_boundary, result: 'succeeded',
      },
    ],
    phaseOperationCounts: {
      setup: { build: 0, pull: 0, load: 2 }, attempt: { build: 0, pull: 0, load: 0 },
      retry: { build: 0, pull: 0, load: 0 }, restart: { build: 0, pull: 0, load: 0 },
      recovery: { build: 0, pull: 0, load: 0 }, verifier: { build: 0, pull: 0, load: 0 },
    },
    attempts,
  })
  const reuse = await buildImageReuseRecord({
    operationLedgerFile, sourceA3LedgerFile, resourceLedgerFiles, privateEnvironmentMarkerFile,
  })
  const imageReuseRecordFile = join(root, 'image-reuse-record.json')
  await writeJSON(imageReuseRecordFile, reuse)

  const profileJourneyDirectory = join(root, 'journeys')
  await mkdir(profileJourneyDirectory)
  const sourceA3Binding = {
    sourceA3LedgerSha256: reuse.sourceA3LedgerSha256,
    sourceA3AuditFinalDigest: reuse.sourceA3AuditFinalDigest,
  }
  const minimal = withDigest({
    schemaVersion: PROFILE_JOURNEY_SCHEMA, status: 'passed', sequence: 1, profile: 'minimal', ...identity,
    bindingDigest: doctor.bindingDigest, publicProductEntry: true,
    journeyId: opaque('jny', 'm'),
    taskId: minimalAttempt.taskId, runId: minimalAttempt.runId,
    attemptId: minimalAttempt.attemptId, workspaceIdentity: minimalAttempt.workspaceIdentity,
    tupleIdentity, ...sourceA3Binding,
    executionIdentityDigest: minimalAttempt.executionIdentityDigest,
    profileEvidence: {
      runtimeAdapterId: 'pi', runtimeSource: 'managed_pi_image', executionProvider: 'docker',
      capabilityPolicy: 'chora.minimal.v1', managedSandbox: true, bashObserved: true,
      editObserved: true, prohibitedCapabilityDenialCount: 1, prohibitedCapabilityDenied: true,
      prohibitedCapabilityEvidenceSha256: digest(`minimal-denial:${seed}`),
      attemptTerminalStatus: 'succeeded', independentVerification: true,
      independentVerificationEvidenceSha256: reuse.sourceMinimalVerifierSliceDigest,
      managedFallback: false,
    },
  })
  const standard = withDigest({
    schemaVersion: PROFILE_JOURNEY_SCHEMA, status: 'passed', sequence: 2, profile: 'standard', ...identity,
    bindingDigest: doctor.bindingDigest, publicProductEntry: true, journeyId: opaque('jny', 's'),
    taskId: attempts[0].taskId, runId: attempts[0].runId, attemptId: attempts[0].attemptId,
    workspaceIdentity: attempts[0].workspaceIdentity, tupleIdentity, ...sourceA3Binding,
    executionIdentityDigest: attempts[0].executionIdentityDigest,
    profileEvidence: {
      runtimeAdapterId: 'pi', runtimeSource: 'managed_pi_image', executionProvider: 'docker',
      capabilityPolicy: 'chora.standard.v1',
      fullRepository: true, securityPolicyEnforced: true, independentVerification: true,
      managedSandbox: true, taskOwnedApply: true,
    },
  })
  const trusted = withDigest({
    schemaVersion: PROFILE_JOURNEY_SCHEMA, status: 'passed', sequence: 3, profile: 'trusted_local', ...identity,
    bindingDigest: doctor.bindingDigest, publicProductEntry: true,
    journeyId: opaque('jny', 't'),
    taskId: productID('task', seed * 100 + 50), runId: productID('run', seed * 100 + 51),
    attemptId: productID('attempt', seed * 100 + 52),
    workspaceIdentity: workspaceIdentity(`trusted:${seed}`), tupleIdentity, ...sourceA3Binding,
    executionIdentityDigest: computeExecutionIdentityDigest({
      taskId: productID('task', seed * 100 + 50), runId: productID('run', seed * 100 + 51),
      attemptId: productID('attempt', seed * 100 + 52),
      workspaceIdentity: workspaceIdentity(`trusted:${seed}`), tupleIdentity,
    }),
    profileEvidence: {
      runtimeAdapterId: 'pi', runtimeSource: 'local_pi', capabilityPolicy: 'pi.native',
      acknowledgementCurrent: true, acknowledgementPolicySha256: digest(`ack:${seed}`),
      executionTarget: 'trusted-host', managedFallback: false, sandboxClaimed: false,
    },
  })
  await writeJSON(join(profileJourneyDirectory, 'minimal.json'), minimal)
  await writeJSON(join(profileJourneyDirectory, 'standard.json'), standard)
  await writeJSON(join(profileJourneyDirectory, 'trusted-local.json'), trusted)

  const scenarioNames = ['failure', 'cancel', 'timeout', 'retry', 'restart', 'orphan', 'reopen', 'apply']
  const statuses = ['failed', 'canceled', 'timed_out', 'succeeded', 'succeeded', 'recovered', 'succeeded', 'succeeded']
  const recovery = withDigest({
    schemaVersion: RECOVERY_RECORD_SCHEMA, status: 'passed', ...identity,
    bindingDigest: doctor.bindingDigest, imageReuseDigest: reuse.digest,
    scenarios: scenarioNames.map((name, index) => {
      const token = String.fromCharCode(97 + index)
      return {
        sequence: index + 1, name,
        taskId: productID('task', seed * 100 + 60 + index),
        runId: productID('run', seed * 100 + 70 + index),
        attemptId: productID('attempt', seed * 100 + 80 + index),
        workspaceId: opaque('wsp', token), targetId: opaque('tgt', token),
        terminalStatus: statuses[index], publicEntry: true, cleanupComplete: true, residue: zeroResidue(),
      }
    }),
    sharedImages: [
      {
        artifactId: 'managed-pi-runtime', roles: managedAliasRoles,
        ...roleImages.managed_pi_runtime, present: true,
      },
      {
        artifactId: 'network-boundary', roles: ['network_boundary'],
        ...roleImages.network_boundary, present: true,
      },
    ],
  })
  const recoveryRecordFile = join(root, 'recovery.json')
  await writeJSON(recoveryRecordFile, recovery)

  const disclosureDirectory = join(root, 'disclosure')
  await mkdir(disclosureDirectory)
  await writeFile(join(disclosureDirectory, 'execution.log'), 'operation completed without sensitive output\n')
  await writeFile(join(disclosureDirectory, 'state.db'), Buffer.from('SQLite format 3\0synthetic clean state'))
  await writeFile(join(disclosureDirectory, 'change.patch'), 'diff --git a/a.txt b/a.txt\n+safe\n')
  await writeJSON(join(disclosureDirectory, 'payload.json'), { status: 'passed', public: true })
  await writeFile(join(disclosureDirectory, 'screen.png'), png)
  const metadata = {
    schemaVersion: 'chora.m1-o4-screenshot-metadata-scan.v1', pngSha256: sha256Hex(png),
    completed: true, chunkTypes: ['IHDR', 'IDAT', 'IEND'], unsafeMatchCount: 0,
  }
  await writeJSON(join(disclosureDirectory, 'screen-metadata.json'), metadata)
  const ocrSafe = {
    schemaVersion: 'chora.m1-o4-system-managed-ocr-record.v2', completed: true,
    pngSha256: sha256Hex(png), engineBindingSha256: digest(`ocr-engine:${seed}`),
    executableSha256: digest(`ocr-executable:${seed}`),
    visionBundleIdentifier: 'com.apple.Vision', visionBundleVersion: '1.0',
    visionInfoPlistSha256: digest(`vision-info:${seed}`), actualRecognitionRevision: 1,
    recognitionLevel: 'accurate', recognitionLanguage: 'en-US', usesLanguageCorrection: true,
    confidenceThreshold: 0.5, observationCount: 1, recognizedText: 'Chora screen complete',
    recognizedTextSha256: sha256Hex('Chora screen complete'), minimumConfidence: 0.9,
    averageConfidence: 0.9, ...zeroScan(), modelBytes: 'opaque_unavailable', networkDisabled: true,
  }
  const ocr = { ...ocrSafe, recordDigest: sha256Hex(canonicalJSONStringify(ocrSafe)) }
  await writeJSON(join(disclosureDirectory, 'screen-ocr.json'), ocr)
  const artifactSpecs = [
    ['execution.log', 'log'], ['state.db', 'database'], ['change.patch', 'patch'], ['payload.json', 'json'],
    ['screen.png', 'png'], ['screen-metadata.json', 'png_metadata'], ['screen-ocr.json', 'png_ocr'],
  ]
  const artifactScans = []
  for (const [name, kind] of artifactSpecs) {
    const bytes = await readFile(join(disclosureDirectory, name))
    artifactScans.push({ name, kind, bytes: bytes.length, sha256: sha256Hex(bytes), ...zeroScan() })
  }
  const disclosure = withDigest({
    schemaVersion: DISCLOSURE_RECORD_SCHEMA, status: 'passed', ...identity,
    credentialCorpusId: credentialCorpus.corpusId, artifacts: artifactScans,
    screenshot: {
      pngSha256: sha256Hex(png), metadataSha256: sha256Hex(canonicalJSONStringify(metadata)),
      ocrSha256: sha256Hex(canonicalJSONStringify(ocr)), metadataUnsafeMatches: 0, ocrUnsafeMatches: 0,
    },
  })
  await writeJSON(join(disclosureDirectory, 'disclosure.json'), disclosure)

  const inputs = {
    doctorRecordFile, imageReuseRecordFile, sourceA3LedgerFile, privateEnvironmentMarkerFile,
    candidateManifestFile, sourceManifestFile, sourceBundleRoot, binaryFile, webDirectory,
    releaseSpecFile, releaseManifestFile, offlineBundleEvidenceFile,
    pathPiProvenanceFile, privatePiProvenanceFile, qualificationFile, modelObservationFile,
    engineEndpointEvidenceFile, profileJourneyDirectory, recoveryRecordFile,
    disclosureDirectory, credentialCorpusFile,
  }
  return {
    root, inputs, identity, marker, credentialCorpus, doctor, reuse, attempts, source,
    paths: { doctorRecordFile, imageReuseRecordFile, binaryFile, recoveryRecordFile, profileJourneyDirectory, disclosureDirectory },
  }
}

test('validates the fresh-installed O4 contract and publishes only safe counts/digests owner-only', async () => {
  const files = await fixture()
  const publication = await validateInstalledEvidence(files.inputs)
  assert.equal(publication.schemaVersion, O4_PUBLICATION_SCHEMA)
  assert.equal(publication.counts.managedAttempts, 3)
  assert.equal(publication.counts.setupLoads, 2)
  assert.equal(publication.counts.sharedExactImagesPresent, 2)
  assert.equal(publication.counts.verifierBuildPullLoad, 0)
  assert.equal(publication.counts.terminalOwnedResidue, 0)
  assert.equal(publication.bindings.pi.pathProvenanceSha256 === publication.bindings.pi.privateProvenanceSha256, false)
  assertInstalledEvidencePublication(publication)
  const encoded = JSON.stringify(publication)
  for (const forbidden of [files.marker.markerNonce, files.credentialCorpus.secrets[0].value,
    'credentialCorpusSha256', '/Users/', 'argv', 'daemonId', 'https://']) {
    assert.equal(encoded.includes(forbidden), false)
  }

  const output = join(files.root, 'o4-installed-evidence.json')
  const missingCapabilityOutput = join(files.root, 'o4-installed-evidence-missing-capability.json')
  await assert.rejects(publishInstalledEvidenceRaw(files.inputs, missingCapabilityOutput),
    /owned-path capability/)
  await assert.rejects(stat(missingCapabilityOutput), /ENOENT/)
  await publishInstalledEvidence(files.inputs, output)
  assert.equal((await stat(output)).mode & 0o777, 0o600)
  const before = await readFile(output, 'utf8')
  await assert.rejects(publishInstalledEvidence(files.inputs, output), /EEXIST/)
  assert.equal(await readFile(output, 'utf8'), before)
})

test('binds real Minimal, Standard, and Trusted Local product attempts to tuple and source identities', async () => {
  const files = await fixture(5)
  const minimal = JSON.parse(await readFile(join(files.inputs.profileJourneyDirectory, 'minimal.json'), 'utf8'))
  const standard = JSON.parse(await readFile(join(files.inputs.profileJourneyDirectory, 'standard.json'), 'utf8'))
  const trusted = JSON.parse(await readFile(join(files.inputs.profileJourneyDirectory, 'trusted-local.json'), 'utf8'))
  for (const journey of [minimal, standard, trusted]) {
    assert.match(journey.taskId, /^task_[0-9a-f-]+$/)
    assert.match(journey.runId, /^run_[0-9a-f-]+$/)
    assert.match(journey.attemptId, /^attempt_[0-9a-f-]+$/)
    assert.match(journey.workspaceIdentity, /^sha256:[0-9a-f]{64}$/)
    assert.equal(journey.tupleIdentity, files.reuse.tupleIdentity)
    assert.equal(journey.sourceA3LedgerSha256, files.reuse.sourceA3LedgerSha256)
    assert.equal(journey.executionIdentityDigest, computeExecutionIdentityDigest(journey))
  }
  assert.deepEqual({
    policy: minimal.profileEvidence.capabilityPolicy,
    source: minimal.profileEvidence.runtimeSource,
    provider: minimal.profileEvidence.executionProvider,
    sandbox: minimal.profileEvidence.managedSandbox,
    bash: minimal.profileEvidence.bashObserved,
    edit: minimal.profileEvidence.editObserved,
    denials: minimal.profileEvidence.prohibitedCapabilityDenialCount,
    denied: minimal.profileEvidence.prohibitedCapabilityDenied,
    verified: minimal.profileEvidence.independentVerification,
    fallback: minimal.profileEvidence.managedFallback,
  }, {
    policy: 'chora.minimal.v1', source: 'managed_pi_image', provider: 'docker', sandbox: true,
    bash: true, edit: true, denials: 1, denied: true, verified: true, fallback: false,
  })
  assert.equal(minimal.executionIdentityDigest, files.reuse.sourceMinimalExecutionIdentityDigest)
  assert.equal(standard.attemptId, files.reuse.managedAttempts[0].attemptId)
  assert.equal(standard.profileEvidence.capabilityPolicy, 'chora.standard.v1')
  assert.equal(trusted.profileEvidence.capabilityPolicy, 'pi.native')
  assert.equal(trusted.profileEvidence.runtimeSource, 'local_pi')
  assert.equal(trusted.profileEvidence.executionTarget, 'trusted-host')
})

test('rejects legacy synthetic profile semantics and tuple, source, or workspace substitution', async () => {
  const cases = [
    {
      name: 'legacy Task ID', file: 'minimal.json', pattern: /Task ID is invalid/,
      mutate: (value) => { value.taskId = `tsk_${'a'.repeat(32)}` },
    },
    {
      name: 'legacy Minimal denial', file: 'minimal.json', pattern: /fields drifted/,
      mutate: (value) => {
        value.profileEvidence = {
          denied: true, reasonCode: 'minimal_execution_denied', resourcesCreated: false, managedFallback: false,
        }
      },
    },
    {
      name: 'workspace substitution', file: 'standard.json', pattern: /Managed Attempt 1/,
      mutate: (value) => {
        value.workspaceIdentity = workspaceIdentity('substituted-standard')
        value.executionIdentityDigest = computeExecutionIdentityDigest(value)
      },
    },
    {
      name: 'tuple splice', file: 'minimal.json', pattern: /tuple identity drifted/,
      mutate: (value) => {
        value.tupleIdentity = digest('spliced-profile-tuple')
        value.executionIdentityDigest = computeExecutionIdentityDigest(value)
      },
    },
    {
      name: 'source A3 splice', file: 'trusted-local.json', pattern: /source A3 ledger identity drifted/,
      mutate: (value) => { value.sourceA3LedgerSha256 = digest('spliced-profile-source-a3') },
    },
  ]
  for (let index = 0; index < cases.length; index++) {
    const item = cases[index]
    const files = await fixture(30 + index)
    await rewriteRecord(join(files.inputs.profileJourneyDirectory, item.file), item.mutate)
    await assert.rejects(validateInstalledEvidence(files.inputs), item.pattern, item.name)
  }
})

test('fails closed for missing and duplicate profile evidence', async () => {
  const missing = await fixture(6)
  await unlink(join(missing.inputs.profileJourneyDirectory, 'minimal.json'))
  await assert.rejects(validateInstalledEvidence(missing.inputs), /entries drifted/)

  const duplicate = await fixture(7)
  const minimal = JSON.parse(await readFile(join(duplicate.inputs.profileJourneyDirectory, 'minimal.json'), 'utf8'))
  await rewriteRecord(join(duplicate.inputs.profileJourneyDirectory, 'trusted-local.json'),
    (value) => {
      value.taskId = minimal.taskId
      value.executionIdentityDigest = computeExecutionIdentityDigest(value)
    })
  await assert.rejects(validateInstalledEvidence(duplicate.inputs), /Task IDs|cross-record/)
})

test('fails closed for spliced environment records and wrong generations', async () => {
  const left = await fixture(8)
  const right = await fixture(9)
  left.inputs.imageReuseRecordFile = right.inputs.imageReuseRecordFile
  await assert.rejects(validateInstalledEvidence(left.inputs), /source A3 bytes|private marker|identity/)

  const sourceBytes = await fixture(19)
  await writeFile(sourceBytes.inputs.sourceA3LedgerFile,
    `${await readFile(sourceBytes.inputs.sourceA3LedgerFile, 'utf8')}\n`)
  await assert.rejects(validateInstalledEvidence(sourceBytes.inputs), /source A3 bytes digest drifted/)

  const generation = await fixture(10)
  await rewriteRecord(generation.inputs.recoveryRecordFile,
    (value) => { value.generationId = opaque('gen', 'z') })
  await assert.rejects(validateInstalledEvidence(generation.inputs), /identity\/generation/)
})

test('fails closed for hidden retries, record tampering, and non-zero residue', async () => {
  const retry = await fixture(11)
  await rewriteRecord(retry.inputs.imageReuseRecordFile,
    (value) => { value.managedAttempts[1].hiddenRetryCount = 1 })
  await assert.rejects(validateInstalledEvidence(retry.inputs), /framework or hidden retry/)

  const tamper = await fixture(12)
  await writeFile(tamper.inputs.binaryFile, 'tampered-binary')
  await assert.rejects(validateInstalledEvidence(tamper.inputs), /binary digest drifted/)

  const residue = await fixture(13)
  await rewriteRecord(residue.inputs.recoveryRecordFile,
    (value) => { value.scenarios[0].residue.ownedContainers = 1 })
  await assert.rejects(validateInstalledEvidence(residue.inputs), /ownedContainers is non-zero/)
})

test('fails closed for private paths, actual corpus secrets, and unsafe screenshot OCR', async () => {
  const privatePath = await fixture(14)
  await writeFile(join(privatePath.inputs.disclosureDirectory, 'execution.log'),
    'unexpected location /Users/alice/work\n')
  await assert.rejects(validateInstalledEvidence(privatePath.inputs), /private-path/)

  const secret = await fixture(15)
  await writeFile(join(secret.inputs.disclosureDirectory, 'change.patch'),
    `+${secret.credentialCorpus.secrets[0].value}\n`)
  await assert.rejects(validateInstalledEvidence(secret.inputs), /credential/)

  const screenshot = await fixture(16)
  await rewriteRecord(join(screenshot.inputs.disclosureDirectory, 'screen-ocr.json'),
    (value) => {
      value.recognizedText = 'contact top-secret@evil.example'
      value.recognizedTextSha256 = sha256Hex(value.recognizedText)
      value.emailMatches = 1
      const { recordDigest: ignored, ...safe } = value
      void ignored
      value.recordDigest = sha256Hex(canonicalJSONStringify(safe))
    })
  await assert.rejects(validateInstalledEvidence(screenshot.inputs), /email/)
})

test('does not publish an output when any raw input fails validation', async () => {
  const files = await fixture(17)
  await writeFile(join(files.inputs.disclosureDirectory, 'payload.json'),
    `{"unsafe":"${files.credentialCorpus.secrets[1].value}"}\n`)
  const output = join(files.root, 'must-not-exist.json')
  await assert.rejects(publishInstalledEvidence(files.inputs, output), /credential/)
  await assert.rejects(stat(output), /ENOENT/)
})

test('validates the real six-field source manifest against its authenticated policy and actual tree', async () => {
  const root = await mkdtemp(join(tmpdir(), 'chora-o4-source-contract-'))
  const source = await createSourceBundle(root)
  assert.equal(Object.keys(source.sourceManifest).length, 6)
  const aggregate = await validateSourceManifest({
    manifest: source.sourceManifest,
    sourceManifestFile: source.sourceManifestFile,
    sourceBundleRoot: source.sourceBundleRoot,
  })
  assert.equal(aggregate, source.sourceManifest.aggregate_sha256)
})

test('rejects missing or extra nested source manifest fields and the legacy three-field shape', async () => {
  for (const mutate of [
    (manifest) => { delete manifest.install_only_projection.paths_sha256 },
    (manifest) => { manifest.model_readable_projection.unexpected = 0 },
    (manifest) => {
      delete manifest.source_path_policy
      delete manifest.install_only_projection
      delete manifest.model_readable_projection
    },
  ]) {
    const root = await mkdtemp(join(tmpdir(), 'chora-o4-source-nested-'))
    const source = await createSourceBundle(root)
    const manifest = JSON.parse(JSON.stringify(source.sourceManifest))
    mutate(manifest)
    await assert.rejects(validateSourceManifest({
      manifest, sourceManifestFile: source.sourceManifestFile, sourceBundleRoot: source.sourceBundleRoot,
    }), /fields drifted/)
  }
})

test('rejects source path policy path and digest drift', async () => {
  for (const mutate of [
    (manifest) => { manifest.source_path_policy.path = 'distribution/v1/policies/other.json' },
    (manifest) => { manifest.source_path_policy.sha256 = digest('wrong-source-policy') },
  ]) {
    const root = await mkdtemp(join(tmpdir(), 'chora-o4-source-policy-'))
    const source = await createSourceBundle(root)
    const manifest = JSON.parse(JSON.stringify(source.sourceManifest))
    mutate(manifest)
    await assert.rejects(validateSourceManifest({
      manifest, sourceManifestFile: source.sourceManifestFile, sourceBundleRoot: source.sourceBundleRoot,
    }), /policy path drifted|policy digest drifted/)
  }
})

test('rejects projection count, path digest, and aggregate drift', async () => {
  for (const mutate of [
    (manifest) => { manifest.install_only_projection.files++ },
    (manifest) => { manifest.install_only_projection.paths_sha256 = digest('wrong-projection-paths') },
    (manifest) => { manifest.model_readable_projection.aggregate_sha256 = digest('wrong-projection-aggregate') },
  ]) {
    const root = await mkdtemp(join(tmpdir(), 'chora-o4-source-projection-'))
    const source = await createSourceBundle(root)
    const manifest = JSON.parse(JSON.stringify(source.sourceManifest))
    mutate(manifest)
    await assert.rejects(validateSourceManifest({
      manifest, sourceManifestFile: source.sourceManifestFile, sourceBundleRoot: source.sourceBundleRoot,
    }), /projection identity mismatch/)
  }
})

test('rejects projection overlap, file omission, and policy misclassification', async () => {
  const overlapRoot = await mkdtemp(join(tmpdir(), 'chora-o4-source-overlap-'))
  const overlap = await createSourceBundle(overlapRoot)
  const overlapManifest = JSON.parse(JSON.stringify(overlap.sourceManifest))
  overlapManifest.model_readable_projection = JSON.parse(JSON.stringify(overlapManifest.install_only_projection))
  await assert.rejects(validateSourceManifest({
    manifest: overlapManifest,
    sourceManifestFile: overlap.sourceManifestFile,
    sourceBundleRoot: overlap.sourceBundleRoot,
  }), /projection identity mismatch/)

  const omissionRoot = await mkdtemp(join(tmpdir(), 'chora-o4-source-omission-'))
  const omission = await createSourceBundle(omissionRoot)
  const omissionManifest = JSON.parse(JSON.stringify(omission.sourceManifest))
  omissionManifest.files.pop()
  omissionManifest.aggregate_sha256 = sourceEntriesAggregate(omissionManifest.files)
  await assert.rejects(validateSourceManifest({
    manifest: omissionManifest,
    sourceManifestFile: omission.sourceManifestFile,
    sourceBundleRoot: omission.sourceBundleRoot,
  }), /actual entries drifted|declared source coverage/)

  const classificationRoot = await mkdtemp(join(tmpdir(), 'chora-o4-source-classification-'))
  const classification = await createSourceBundle(classificationRoot)
  const classificationManifest = JSON.parse(JSON.stringify(classification.sourceManifest))
  const installOnly = classificationManifest.install_only_projection
  classificationManifest.install_only_projection = classificationManifest.model_readable_projection
  classificationManifest.model_readable_projection = installOnly
  await assert.rejects(validateSourceManifest({
    manifest: classificationManifest,
    sourceManifestFile: classification.sourceManifestFile,
    sourceBundleRoot: classification.sourceBundleRoot,
  }), /projection identity mismatch/)
})

test('CLI requires and preserves the exact source bundle root input', async () => {
  const files = await fixture(18)
  const output = join(files.root, 'cli-output.json')
  const pairs = [
    ['--doctor-record', 'doctorRecordFile'], ['--image-reuse-record', 'imageReuseRecordFile'],
    ['--source-a3-ledger', 'sourceA3LedgerFile'],
    ['--private-marker', 'privateEnvironmentMarkerFile'], ['--candidate-manifest', 'candidateManifestFile'],
    ['--source-manifest', 'sourceManifestFile'], ['--source-bundle-root', 'sourceBundleRoot'],
    ['--binary', 'binaryFile'], ['--web-directory', 'webDirectory'],
    ['--release-spec', 'releaseSpecFile'], ['--release-manifest', 'releaseManifestFile'],
    ['--offline-bundle-evidence', 'offlineBundleEvidenceFile'], ['--path-pi-provenance', 'pathPiProvenanceFile'],
    ['--private-pi-provenance', 'privatePiProvenanceFile'], ['--qualification', 'qualificationFile'],
    ['--model-observation', 'modelObservationFile'],
    ['--engine-endpoint', 'engineEndpointEvidenceFile'], ['--profile-journeys', 'profileJourneyDirectory'],
    ['--recovery-record', 'recoveryRecordFile'], ['--disclosure-directory', 'disclosureDirectory'],
    ['--credential-corpus', 'credentialCorpusFile'],
  ]
  const argv = pairs.flatMap(([flag, key]) => [flag, files.inputs[key]]).concat([
    '--service-controller', repositoryCapture.executableFile,
    '--service-controller-sha256', repositoryCapture.executableSha256,
    '--output', output,
  ])
  const parsed = parseCLI(argv)
  assert.equal(parsed.inputs.sourceBundleRoot, files.inputs.sourceBundleRoot)
  assert.deepEqual(parsed.repositoryCapture, repositoryCapture)
  const rootIndex = argv.indexOf('--source-bundle-root')
  const missingRoot = [...argv.slice(0, rootIndex), ...argv.slice(rootIndex + 2)]
  assert.throws(() => parseCLI(missingRoot), /argument is missing/)
})
