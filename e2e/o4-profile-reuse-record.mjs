import { createHash } from 'node:crypto'
import { spawn } from 'node:child_process'
import {
  lstat, mkdir, open, readFile, readdir,
} from 'node:fs/promises'
import { basename, dirname, isAbsolute, join, normalize, relative, resolve } from 'node:path'

import {
  canonicalJSONStringify,
  commitHandoffPhaseC,
  validateCandidateTuple,
} from './o4-candidate-install-orchestrator.mjs'
import {
  cleanupOwnedPath,
  ownedPathIdentity,
  validateOwnedPathCapability,
} from './o4-owned-path-cleanup.mjs'
import {
  O4_ROLES,
  assertDigest,
  assertEnvironmentID,
  assertImageDigest,
  assertSafePublicValue,
  validatePlatform,
} from './o4-installed-doctor-record.mjs'

export const PROFILE_REUSE_RECEIPT_SCHEMA = 'chora.m1-o4-profile-reuse-raw-receipt.v1'
export const PROFILE_REUSE_MANIFEST_SCHEMA = 'chora.m1-o4-profile-reuse-raw-manifest.v1'
export const PROFILE_REUSE_RESOURCE_SCHEMA = 'chora.m1-o4-profile-reuse-resource-ledger.v1'
export const PROFILE_REUSE_RECORD_SCHEMA = 'chora.m1-o4-profile-reuse-phase-c-record.v1'
export const PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA = 'chora.m1-o4-profile-reuse-observer-request.v1'
export const PROFILE_REUSE_OBSERVER_ACK_SCHEMA = 'chora.m1-o4-profile-reuse-observer-ack.v1'

const SOURCE_A3_SCHEMA = 'chora.m1-o4-sealed-a3-runner-ledger.v1'
const receiptNames = Object.freeze([
  '01-minimal.json',
  '02-standard-1.json',
  '03-standard-2.json',
  '04-standard-3.json',
  '05-trusted-local.json',
])
const resourceNames = Object.freeze([
  '01-minimal-resources.json',
  '02-standard-1-resources.json',
  '03-standard-2-resources.json',
  '04-standard-3-resources.json',
  '05-trusted-local-resources.json',
])
const journeys = Object.freeze([
  { sequence: 1, journey: 'minimal', profile: 'minimal', cohortSequence: null },
  { sequence: 2, journey: 'standard_1', profile: 'standard', cohortSequence: 1 },
  { sequence: 3, journey: 'standard_2', profile: 'standard', cohortSequence: 2 },
  { sequence: 4, journey: 'standard_3', profile: 'standard', cohortSequence: 3 },
  { sequence: 5, journey: 'trusted_local', profile: 'trusted_local', cohortSequence: null },
])
const managedProfiles = new Set(['minimal', 'standard'])
const digestRE = /^[0-9a-f]{64}$/
const imageDigestRE = /^sha256:[0-9a-f]{64}$/
const opaqueInstallRE = /^(?:ins|gen)_[A-Za-z0-9_-]{24,80}$/
const uuidV7 = '[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}'
const productIDs = Object.freeze({
  taskId: new RegExp(`^task_${uuidV7}$`),
  runId: new RegExp(`^run_${uuidV7}$`),
  attemptId: new RegExp(`^attempt_${uuidV7}$`),
  workspaceIdentity: /^sha256:[0-9a-f]{64}$/,
})
const zeroOperationKeys = Object.freeze(['build', 'pull', 'load'])
const zeroResidueKeys = Object.freeze([
  'ownedContainers', 'ownedNetworks', 'ownedVolumes', 'ownedConfigs', 'ownedWorkspaces',
  'ownedProcessGroups', 'activeReferences', 'recoverableReferences',
])
const receiptKeys = Object.freeze([
  'schemaVersion', 'status', 'sequence', 'journey', 'profile', 'cohortSequence',
  'environmentId', 'installId', 'generationId', 'platform', 'bindingDigest', 'tupleIdentity',
  'taskId', 'runId', 'attemptId', 'workspaceIdentity', 'executionIdentityDigest',
  'attemptSequence', 'attemptState', 'terminalReason', 'runtimeAdapterId', 'runtimeSource',
  'executionProvider', 'capabilityPolicy', 'sandboxState', 'managedSandbox',
  'managedFallbackUsed', 'frameworkRetryCount', 'hiddenRetryCount', 'attemptSource',
  'verifierSource', 'runtimeIdentitySha256', 'resourceLedgerSha256', 'roleTupleDigest',
  'resourceRequestDigest', 'resourceAckDigest', 'resourceObserverSha256',
  'operationCounts', 'terminalResidue', 'publicActionDigest', 'publicObservationDigest',
  'publicStartedAt', 'publicCompletedAt', 'profileEvidence', 'receiptDigest',
])
const manifestKeys = Object.freeze([
  'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
  'bindingDigest', 'tupleIdentity', 'sourceA3LedgerSha256', 'sourceA3AuditRecordCount',
  'sourceA3AuditFinalDigest', 'roleImages', 'roleTupleDigest', 'receipts', 'resourceLedgers',
  'observerAttestations', 'manifestDigest',
])
const maxJSON = 4 * 1024 * 1024

export function computeProfileExecutionIdentityDigest(value) {
  const identity = {
    tupleIdentity: value.tupleIdentity,
    taskId: value.taskId,
    runId: value.runId,
    attemptId: value.attemptId,
    workspaceIdentity: value.workspaceIdentity,
  }
  assertDigest(identity.tupleIdentity, 'profile execution tuple identity')
  for (const [field, pattern] of Object.entries(productIDs)) {
    assert(typeof identity[field] === 'string' && pattern.test(identity[field]),
      `profile execution ${field} is not a UUIDv7/sha256 identity`)
  }
  return sha256(canonicalJSONStringify(identity))
}

export function computeProfileRoleTupleDigest(roleImages) {
  validateRoleImages(roleImages)
  return sha256(canonicalJSONStringify(roleImages))
}

export function formProfileReuseReceipt(draft) {
  assertPlainObject(draft, 'profile reuse receipt draft')
  assertExactKeys(draft, receiptKeys.filter((key) => key !== 'receiptDigest'),
    'profile reuse receipt draft')
  const receipt = { ...clone(draft), receiptDigest: sha256(canonicalJSONStringify(draft)) }
  return Object.freeze(assertProfileReuseReceipt(receipt))
}

export function assertProfileReuseReceipt(receipt, expected) {
  assertPlainObject(receipt, 'profile reuse receipt')
  assertExactKeys(receipt, receiptKeys, 'profile reuse receipt')
  assert(receipt.schemaVersion === PROFILE_REUSE_RECEIPT_SCHEMA && receipt.status === 'passed',
    'profile reuse receipt schema/status drifted')
  const contract = expected ?? journeys[receipt.sequence - 1]
  assert(contract && receipt.sequence === contract.sequence && receipt.journey === contract.journey &&
    receipt.profile === contract.profile && receipt.cohortSequence === contract.cohortSequence,
  'profile reuse receipt order/profile drifted')
  validateSharedIdentity(receipt, 'profile reuse receipt')
  for (const [field, pattern] of Object.entries(productIDs)) {
    assert(typeof receipt[field] === 'string' && pattern.test(receipt[field]),
      `profile reuse receipt ${field} is not a UUIDv7/sha256 identity`)
  }
  assertDigest(receipt.executionIdentityDigest, 'profile execution identity digest')
  assert(receipt.executionIdentityDigest === computeProfileExecutionIdentityDigest(receipt),
    'profile execution identity digest drifted')
  assert(receipt.attemptSequence === 1 && receipt.attemptState === 'output_submitted' &&
    receipt.terminalReason === null, 'profile path does not prove successful Attempt 1 product state')
  assert(receipt.runtimeAdapterId === 'pi', 'profile path did not use Pi')
  assert(receipt.frameworkRetryCount === 0 && receipt.hiddenRetryCount === 0,
    'profile path contains a framework or hidden retry')
  validateSourceRange(receipt.verifierSource, 'Verifier source')
  assertDigest(receipt.runtimeIdentitySha256, 'profile runtime identity digest')
  assertDigest(receipt.resourceLedgerSha256, 'profile resource ledger digest')
  assertDigest(receipt.resourceRequestDigest, 'profile resource request digest')
  assertDigest(receipt.resourceAckDigest, 'profile resource acknowledgement digest')
  assertDigest(receipt.resourceObserverSha256, 'profile resource observer digest')
  validateZeroCounts(receipt.terminalResidue, zeroResidueKeys, 'profile terminal residue')
  assertDigest(receipt.publicActionDigest, 'profile public action digest')
  assertDigest(receipt.publicObservationDigest, 'profile public observation digest')
  validateTimeRange(receipt.publicStartedAt, receipt.publicCompletedAt, 'profile public execution')
  if (managedProfiles.has(receipt.profile)) validateManagedReceipt(receipt)
  else validateTrustedLocalReceipt(receipt)
  assertDigest(receipt.receiptDigest, 'profile reuse receipt digest')
  const { receiptDigest, ...safe } = receipt
  assert(receiptDigest === sha256(canonicalJSONStringify(safe)), 'profile reuse receipt digest drifted')
  if (managedProfiles.has(receipt.profile)) assertSafePublicValue(receipt)
  return receipt
}

export async function persistProfileReuseReceipt(receipt, outputPath, dependencies = undefined) {
  const checked = assertProfileReuseReceipt(receipt)
  const expectedName = receiptNames[checked.sequence - 1]
  assert(basename(outputPath) === expectedName, 'profile reuse receipt filename drifted')
  await writeExclusiveOwnerReadonlyJSON(checked, outputPath, 'profile reuse receipt',
    dependencies?.repositoryCapture, dependencies?.writeStep)
}

export function formProfileReuseManifest(draft) {
  assertPlainObject(draft, 'profile reuse manifest draft')
  assertExactKeys(draft, manifestKeys.filter((key) => key !== 'manifestDigest'),
    'profile reuse manifest draft')
  const manifest = { ...clone(draft), manifestDigest: sha256(canonicalJSONStringify(draft)) }
  return Object.freeze(assertProfileReuseManifest(manifest))
}

export function assertProfileReuseManifest(manifest) {
  assertPlainObject(manifest, 'profile reuse manifest')
  assertExactKeys(manifest, manifestKeys, 'profile reuse manifest')
  assert(manifest.schemaVersion === PROFILE_REUSE_MANIFEST_SCHEMA && manifest.status === 'complete',
    'profile reuse manifest schema/status drifted')
  validateSharedIdentity(manifest, 'profile reuse manifest')
  assertDigest(manifest.sourceA3LedgerSha256, 'profile reuse source A3 bytes digest')
  assert(Number.isSafeInteger(manifest.sourceA3AuditRecordCount) && manifest.sourceA3AuditRecordCount > 0,
    'profile reuse source A3 audit count is invalid')
  assertDigest(manifest.sourceA3AuditFinalDigest, 'profile reuse source A3 final digest')
  validateRoleImages(manifest.roleImages)
  assertDigest(manifest.roleTupleDigest, 'profile reuse role tuple digest')
  assert(manifest.roleTupleDigest === computeProfileRoleTupleDigest(manifest.roleImages),
    'profile reuse role tuple digest drifted')
  validateFileIndex(manifest.receipts, receiptNames, 'profile reuse receipt index')
  validateFileIndex(manifest.resourceLedgers,
    resourceNames,
    'profile reuse resource-ledger index')
  validateObserverAttestationIndex(manifest.observerAttestations, manifest.resourceLedgers)
  assertDigest(manifest.manifestDigest, 'profile reuse manifest digest')
  const { manifestDigest, ...safe } = manifest
  assert(manifestDigest === sha256(canonicalJSONStringify(safe)), 'profile reuse manifest digest drifted')
  assertSafePublicValue(manifest)
  return manifest
}

export async function persistProfileReuseManifest(manifest, outputPath, dependencies = undefined) {
  assertProfileReuseManifest(manifest)
  assert(basename(outputPath) === 'manifest.json', 'profile reuse manifest filename drifted')
  await writeExclusiveOwnerReadonlyJSON(manifest, outputPath, 'profile reuse manifest',
    dependencies?.repositoryCapture, dependencies?.writeStep)
}

export async function prepareProfileReuseEvidencePaths({
  tupleFile, sourceA3LedgerFile, handoffReceiptDirectory, observerFile, observerSha256,
  supervisorResourceDirectory, runnerProtocolDirectory, outputDirectory,
  observerTimeoutMs = 60_000, requireAbsentSourceA3 = false,
}) {
  const inputs = {
    tupleFile, sourceA3LedgerFile, handoffReceiptDirectory, observerFile,
    supervisorResourceDirectory, runnerProtocolDirectory,
  }
  for (const [label, path] of Object.entries(inputs)) exactAbsolutePath(path, label)
  exactAbsolutePath(outputDirectory, 'profile reuse output directory')
  for (const path of Object.values(inputs)) {
    assert(!pathInside(path, outputDirectory) && !pathInside(outputDirectory, path),
      'profile reuse input/output paths overlap')
  }
  await readExactFile(tupleFile, 'candidate B2 tuple preflight', maxJSON, 0o400)
  if (requireAbsentSourceA3) {
    try {
      await lstat(sourceA3LedgerFile)
      throw new Error('source A3 exists before the first product boundary')
    } catch (error) {
      if (error.message === 'source A3 exists before the first product boundary') throw error
      if (error.code !== 'ENOENT') throw error
    }
  } else {
    await readExactFile(sourceA3LedgerFile, 'current source A3 preflight', maxJSON)
  }
  await assertOwnerDirectory(handoffReceiptDirectory)
  assertDigest(observerSha256, 'profile reuse observer file digest')
  assert(Number.isSafeInteger(observerTimeoutMs) && observerTimeoutMs >= 100 && observerTimeoutMs <= 60_000,
    'profile reuse observer timeout is outside the bounded contract')
  const observerInput = await readExactFile(observerFile, 'profile reuse observer', 16 * 1024 * 1024, 0o500)
  assert(sha256(observerInput) === observerSha256, 'profile reuse observer file digest drifted')
  await assertOwnerDirectory(runnerProtocolDirectory)
  assert((await readdir(runnerProtocolDirectory)).length === 0,
    'Runner observer protocol directory is not clean')
  await assertOwnerDirectory(supervisorResourceDirectory)
  assert((await readdir(supervisorResourceDirectory)).length === 0,
    'Supervisor resource directory is not clean')
  await assertNoSymlinkAncestors(outputDirectory)
  try {
    await lstat(outputDirectory)
    throw new Error('profile reuse output directory already exists')
  } catch (error) {
    if (error.message === 'profile reuse output directory already exists') throw error
    if (error.code !== 'ENOENT') throw error
  }
  await assertOwnerDirectory(dirname(outputDirectory))
  await mkdir(outputDirectory, { recursive: false, mode: 0o700 })
  const receiptDirectory = join(outputDirectory, 'receipts')
  const resourceLedgerDirectory = join(outputDirectory, 'resources')
  const observerRequestDirectory = join(outputDirectory, 'observer-requests')
  const observerAckDirectory = join(outputDirectory, 'observer-acks')
  const runnerRequestDirectory = join(runnerProtocolDirectory, 'requests')
  const runnerAckDirectory = join(runnerProtocolDirectory, 'acks')
  const runnerResourceDirectory = join(runnerProtocolDirectory, 'attested-resources')
  await mkdir(receiptDirectory, { recursive: false, mode: 0o700 })
  await mkdir(resourceLedgerDirectory, { recursive: false, mode: 0o700 })
  await mkdir(observerRequestDirectory, { recursive: false, mode: 0o700 })
  await mkdir(observerAckDirectory, { recursive: false, mode: 0o700 })
  await mkdir(runnerRequestDirectory, { recursive: false, mode: 0o700 })
  await mkdir(runnerAckDirectory, { recursive: false, mode: 0o700 })
  await mkdir(runnerResourceDirectory, { recursive: false, mode: 0o700 })
  return Object.freeze({
    ...inputs, observerSha256, observerTimeoutMs, requireAbsentSourceA3, outputDirectory,
    receiptDirectory, resourceLedgerDirectory,
    observerRequestDirectory, observerAckDirectory, runnerRequestDirectory,
    runnerAckDirectory, runnerResourceDirectory,
  })
}

export async function collectProfileReuseResourceAtBoundary({
  paths, sequence, journey, profile, tupleIdentity, taskId, runId, attemptId,
  workspaceIdentity, publicObservationDigest, publicCompletedAt, runtimeFingerprint, requestNonce,
  repositoryCapture,
}) {
  repositoryCapture = validateOwnedPathCapability(
    repositoryCapture, 'profile observer owned-path capability')
  const expected = journeys[sequence - 1]
  assert(expected && expected.journey === journey && expected.profile === profile,
    'profile reuse observer request order/profile drifted')
  const identity = { tupleIdentity, taskId, runId, attemptId, workspaceIdentity }
  const executionIdentityDigest = computeProfileExecutionIdentityDigest(identity)
  assertDigest(publicObservationDigest, 'profile reuse observer public observation digest')
  assert(typeof publicCompletedAt === 'string' && Number.isFinite(Date.parse(publicCompletedAt)),
    'profile reuse observer public completion time is invalid')
  assert(typeof requestNonce === 'string' && /^[0-9a-f]{64}$/.test(requestNonce),
    'profile reuse observer request nonce is invalid')
  const expectedResources = resourceNames.slice(0, sequence)
  const actualResources = (await readdir(paths.supervisorResourceDirectory)).sort()
  assertSameJSON(actualResources, [...expectedResources].sort(),
    'Supervisor resource boundary contains missing, stale, or extra evidence')
  const rawResourceFile = join(paths.supervisorResourceDirectory, resourceNames[sequence - 1])
  const rawResourceBefore = await readJSONWithDigest(rawResourceFile,
    `supervisor raw resource ${sequence}`, 512 * 1024, 0o400)
  validateSupervisorResourceEnvelope(rawResourceBefore.value, {
    profile, tupleIdentity, taskId, runId, attemptId, workspaceIdentity,
    executionIdentityDigest,
  })
  if (profile === 'trusted_local') {
    assertDigest(runtimeFingerprint, 'Trusted Local public runtime fingerprint')
    assert(rawResourceBefore.value.trustedHost.runtimeFingerprint === runtimeFingerprint,
      'Trusted supervisor proof does not match the public runtime identity')
  }
  const sourceBefore = await readJSONWithDigest(paths.sourceA3LedgerFile,
    'source A3 before observer attestation', maxJSON)
  validateBoundarySourceA3(sourceBefore.value, {
    sequence, profile, tupleIdentity, taskId, runId, attemptId,
    resourceSha256: rawResourceBefore.sha256,
  })
  const requestedAt = new Date().toISOString()
  const requestDraft = {
    schemaVersion: PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA,
    status: 'requested', sequence, journey, profile, tupleIdentity, taskId, runId, attemptId,
    workspaceIdentity, executionIdentityDigest, publicObservationDigest, publicCompletedAt,
    rawResourceSha256: rawResourceBefore.sha256, requestedAt, requestNonce,
    observerSha256: paths.observerSha256, ownedPathController: repositoryCapture,
  }
  const request = { ...requestDraft, requestDigest: sha256(canonicalJSONStringify(requestDraft)) }
  const stem = `${String(sequence).padStart(2, '0')}-${journey.replaceAll('_', '-')}`
  const requestFile = join(paths.runnerRequestDirectory, `${stem}-request.json`)
  const resourceFile = join(paths.runnerResourceDirectory, `${stem}-resources.json`)
  const ackFile = join(paths.runnerAckDirectory, `${stem}-ack.json`)
  await writeExclusiveOwnerReadonlyJSON(request, requestFile,
    'profile reuse observer request', repositoryCapture)
  try {
    await validatePinnedObserver(paths.observerFile, paths.observerSha256)
    await runObserver(paths.observerFile, [
      '--protocol', PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA,
      '--request', requestFile,
      '--resource-input', rawResourceFile,
      '--attested-resource-output', resourceFile,
      '--ack-output', ackFile,
      '--tuple-file', paths.tupleFile,
      '--source-a3-ledger-file', paths.sourceA3LedgerFile,
    ], paths.observerTimeoutMs)
    await validatePinnedObserver(paths.observerFile, paths.observerSha256)
    const sourceAfter = await readJSONWithDigest(paths.sourceA3LedgerFile,
      'source A3 after observer attestation', maxJSON)
    assert(sourceAfter.sha256 === sourceBefore.sha256,
      'profile reuse observer mutated source A3')
    const rawResourceAfter = await readJSONWithDigest(rawResourceFile,
      `supervisor raw resource ${sequence} after attestation`, 512 * 1024, 0o400)
    assert(rawResourceAfter.sha256 === rawResourceBefore.sha256,
      'profile reuse observer mutated the supervisor raw resource')
    const resourceInput = await readJSONWithDigest(resourceFile,
      `profile reuse attested resource ${sequence}`, 512 * 1024, 0o400)
    assert(resourceInput.sha256 === rawResourceBefore.sha256,
      'profile reuse observer generated or substituted resource bytes')
    const ackInput = await readJSONWithDigest(ackFile,
      `profile reuse observer acknowledgement ${sequence}`, 128 * 1024, 0o400)
    validateObserverAck(ackInput.value, {
      sequence, journey, requestDigest: request.requestDigest,
      resourceSha256: rawResourceBefore.sha256, observerSha256: paths.observerSha256,
      requestedAt,
    })
    return Object.freeze({
      requestFile, resourceFile, ackFile, resourceSha256: resourceInput.sha256,
      requestSha256: sha256(await readExactFile(requestFile,
        `profile reuse observer request ${sequence}`, 128 * 1024, 0o400)),
      ackSha256: ackInput.sha256, observerSha256: paths.observerSha256,
      requestDigest: request.requestDigest, ackDigest: ackInput.value.ackDigest,
    })
  } catch (error) {
    try {
      await assertFailedObserverPairAbsent(resourceFile, ackFile)
    } catch (residueError) {
      throw new AggregateError([error, residueError],
        'profile observer failed and left unowned publication residue', { cause: error })
    }
    throw error
  }
}

export async function buildProfileReuseRecord({
  manifestFile, receiptDirectory, resourceLedgerDirectory, observerRequestDirectory,
  observerAckDirectory, sourceA3LedgerFile, tupleFile, handoffReceiptDirectory,
  recordFile, repositoryCapture,
}, dependencies = undefined) {
  repositoryCapture = validateOwnedPathCapability(
    repositoryCapture, 'profile reuse owned-path capability')
  exactAbsolutePath(recordFile, 'profile reuse phase-C record')
  assert(basename(recordFile) === 'phase-c-record.json',
    'profile reuse phase-C record filename drifted')
  const recordWriteStep = dependencies?.recordWriteStep ?? null
  const beforeReceiptPublish = dependencies?.beforeReceiptPublish ?? null
  const receiptWriteStep = dependencies?.receiptWriteStep ?? null
  assert(recordWriteStep === null || typeof recordWriteStep === 'function',
    'profile reuse phase-C record write-step dependency is invalid')
  assert(beforeReceiptPublish === null || typeof beforeReceiptPublish === 'function',
    'profile reuse phase-C before-receipt dependency is invalid')
  assert(receiptWriteStep === null || typeof receiptWriteStep === 'function',
    'profile reuse phase-C receipt write-step dependency is invalid')
  const manifestInput = await readJSONWithDigest(manifestFile, 'profile reuse manifest', maxJSON, 0o400)
  const manifest = assertProfileReuseManifest(manifestInput.value)
  const tupleInput = await readJSONWithDigest(tupleFile, 'candidate B2 tuple', maxJSON, 0o400)
  const tuple = validateCandidateTuple(tupleInput.value)
  assertSameIdentity(manifest, tuple, 'profile reuse manifest/B2 tuple')
  assert(manifest.tupleIdentity === tuple.identity, 'profile reuse B2 tuple identity drifted')

  const sourceInput = await readJSONWithDigest(sourceA3LedgerFile, 'current source A3 ledger', maxJSON)
  validateCurrentSourceA3(sourceInput.value, manifest)
  assert(sourceInput.sha256 === manifest.sourceA3LedgerSha256,
    'profile reuse source A3 bytes digest drifted')

  const receiptFiles = await exactIndexedFiles(receiptDirectory, receiptNames, 'profile reuse receipts')
  const resourceNames = manifest.resourceLedgers.map(({ file }) => file)
  const resourceFiles = await exactIndexedFiles(resourceLedgerDirectory, resourceNames,
    'profile reuse resource ledgers')
  const requestFiles = await exactIndexedFiles(observerRequestDirectory,
    manifest.observerAttestations.map(({ requestFile }) => requestFile),
    'profile reuse observer requests')
  const ackFiles = await exactIndexedFiles(observerAckDirectory,
    manifest.observerAttestations.map(({ ackFile }) => ackFile),
    'profile reuse observer acknowledgements')
  const receipts = []
  for (let index = 0; index < journeys.length; index++) {
    const receiptInput = await readJSONWithDigest(receiptFiles[index], `profile reuse receipt ${index + 1}`,
      512 * 1024, 0o400)
    assert(receiptInput.sha256 === manifest.receipts[index].sha256,
      `profile reuse receipt ${index + 1} bytes digest drifted`)
    const receipt = assertProfileReuseReceipt(receiptInput.value, journeys[index])
    validateReceiptAgainstManifest(receipt, manifest)
    validateReceiptSourceRanges(receipt, sourceInput.value.commandAudit)
    if (managedProfiles.has(receipt.profile)) {
      validateManagedSourceAttempt(receipt, sourceInput.value.attempts[index], index + 1)
    }
    const resourceInput = await readJSONWithDigest(resourceFiles[index],
      `profile reuse resource ledger ${index + 1}`, 512 * 1024, 0o400)
    assert(resourceInput.sha256 === manifest.resourceLedgers[index].sha256 &&
      resourceInput.sha256 === receipt.resourceLedgerSha256,
    `profile reuse resource ledger ${index + 1} bytes digest drifted`)
    validateResourceLedger(resourceInput.value, receipt)
    const attestation = manifest.observerAttestations[index]
    const requestInput = await readJSONWithDigest(requestFiles[index],
      `profile reuse observer request ${index + 1}`, 128 * 1024, 0o400)
    const ackInput = await readJSONWithDigest(ackFiles[index],
      `profile reuse observer acknowledgement ${index + 1}`, 128 * 1024, 0o400)
    assert(requestInput.sha256 === attestation.requestSha256 &&
      ackInput.sha256 === attestation.ackSha256,
    `profile reuse observer evidence ${index + 1} bytes drifted`)
    validateObserverEvidence(requestInput.value, ackInput.value, receipt, attestation)
    receipts.push(receipt)
  }
  validateCohort(receipts)

  const observation = {
    schemaVersion: PROFILE_REUSE_RECORD_SCHEMA,
    status: 'passed',
    environmentId: manifest.environmentId,
    installId: manifest.installId,
    generationId: manifest.generationId,
    platform: clone(manifest.platform),
    bindingDigest: manifest.bindingDigest,
    tupleIdentity: manifest.tupleIdentity,
    sourceA3LedgerSha256: manifest.sourceA3LedgerSha256,
    sourceA3AuditRecordCount: manifest.sourceA3AuditRecordCount,
    sourceA3AuditFinalDigest: manifest.sourceA3AuditFinalDigest,
    installedManagedRoleTupleDigest: manifest.roleTupleDigest,
    journeyReceiptDigests: receipts.map(({ receiptDigest }) => receiptDigest),
    executionIdentityDigests: receipts.map(({ executionIdentityDigest }) => executionIdentityDigest),
    resourceLedgerDigests: receipts.map(({ resourceLedgerSha256 }) => resourceLedgerSha256),
    resourceRequestDigests: receipts.map(({ resourceRequestDigest }) => resourceRequestDigest),
    resourceAckDigests: receipts.map(({ resourceAckDigest }) => resourceAckDigest),
    resourceObserverSha256: receipts[0].resourceObserverSha256,
    trustedHostEvidenceDigest: sha256(canonicalJSONStringify(receipts.at(-1).profileEvidence)),
    standardReuseCount: 3,
    frameworkRetryCount: 0,
    hiddenRetryCount: 0,
    a3SealedByPhaseC: false,
    finalProfileJourneysFormed: false,
    b1CompositeFormed: false,
  }
  const observationDigest = sha256(canonicalJSONStringify(observation))
  const operation = {
    phase: 'C',
    manifestSha256: manifestInput.sha256,
    manifestDigest: manifest.manifestDigest,
    tupleFileSha256: tupleInput.sha256,
    receiptCount: receipts.length,
  }
  const operationDigest = sha256(canonicalJSONStringify(operation))

  // Phase C is committed only after the cross-bound record has been created
  // and reopened byte-for-byte while the candidate phase-append lock is held.
  // The immutable 07-C receipt is the sole commit marker; a failed record
  // prepare may leave owned pre-content, but it can never leave that marker.
  await assertPhaseCReady(handoffReceiptDirectory, tuple.identity)
  const committed = await commitHandoffPhaseC({
    tupleFile,
    receiptDirectory: handoffReceiptDirectory,
    operationDigest,
    observationDigest,
  }, {
    repositoryCapture,
    prepareCommit: async ({ handoffReceipt }) => {
      const recordWithoutDigest = {
        ...observation,
        operationDigest,
        observationDigest,
        handoffReceiptDigest: handoffReceipt.receiptDigest,
      }
      const record = Object.freeze(assertProfileReuseRecord({
        ...recordWithoutDigest,
        recordDigest: sha256(canonicalJSONStringify(recordWithoutDigest)),
      }))
      await persistProfileReuseRecord(record, recordFile,
        { repositoryCapture, writeStep: recordWriteStep })
      await reopenExactProfileReuseRecord(record, recordFile)
      return record
    },
    beforeReceiptPublish: async ({ handoffReceipt, prepared }) => {
      if (beforeReceiptPublish !== null) {
        await beforeReceiptPublish(Object.freeze({
          handoffReceipt, record: prepared, recordFile,
        }))
      }
      await reopenExactProfileReuseRecord(prepared, recordFile)
    },
    receiptWriteStep,
  })
  return committed.prepared
}

export function assertProfileReuseRecord(record) {
  assertPlainObject(record, 'profile reuse phase-C record')
  assertExactKeys(record, [
    'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
    'bindingDigest', 'tupleIdentity', 'sourceA3LedgerSha256', 'sourceA3AuditRecordCount',
    'sourceA3AuditFinalDigest', 'installedManagedRoleTupleDigest', 'journeyReceiptDigests',
    'executionIdentityDigests', 'resourceLedgerDigests', 'trustedHostEvidenceDigest',
    'resourceRequestDigests', 'resourceAckDigests', 'resourceObserverSha256',
    'standardReuseCount', 'frameworkRetryCount', 'hiddenRetryCount', 'a3SealedByPhaseC',
    'finalProfileJourneysFormed', 'b1CompositeFormed', 'operationDigest',
    'observationDigest', 'handoffReceiptDigest', 'recordDigest',
  ], 'profile reuse phase-C record')
  assert(record.schemaVersion === PROFILE_REUSE_RECORD_SCHEMA && record.status === 'passed',
    'profile reuse phase-C record schema/status drifted')
  validateSharedIdentity(record, 'profile reuse phase-C record')
  for (const field of [
    'sourceA3LedgerSha256', 'sourceA3AuditFinalDigest', 'installedManagedRoleTupleDigest',
    'trustedHostEvidenceDigest', 'operationDigest', 'observationDigest', 'handoffReceiptDigest',
    'recordDigest', 'resourceObserverSha256',
  ]) assertDigest(record[field], `profile reuse phase-C ${field}`)
  assert(Number.isSafeInteger(record.sourceA3AuditRecordCount) &&
    record.sourceA3AuditRecordCount > 0, 'profile reuse phase-C A3 count is invalid')
  for (const [field, count] of [
    ['journeyReceiptDigests', 5], ['executionIdentityDigests', 5], ['resourceLedgerDigests', 5],
    ['resourceRequestDigests', 5], ['resourceAckDigests', 5],
  ]) {
    assert(Array.isArray(record[field]) && record[field].length === count,
      `profile reuse phase-C ${field} cardinality drifted`)
    for (const digest of record[field]) assertDigest(digest, `profile reuse phase-C ${field} digest`)
    assertUnique(record[field], `profile reuse phase-C ${field}`)
  }
  assert(record.standardReuseCount === 3 && record.frameworkRetryCount === 0 &&
    record.hiddenRetryCount === 0 && record.a3SealedByPhaseC === false &&
    record.finalProfileJourneysFormed === false && record.b1CompositeFormed === false,
  'profile reuse phase-C boundary predicates drifted')
  const { recordDigest, ...safe } = record
  assert(recordDigest === sha256(canonicalJSONStringify(safe)),
    'profile reuse phase-C record digest drifted')
  assertSafePublicValue(record)
  return record
}

export async function persistProfileReuseRecord(record, outputPath, dependencies = undefined) {
  assertProfileReuseRecord(record)
  assert(basename(outputPath) === 'phase-c-record.json',
    'profile reuse phase-C record filename drifted')
  await writeExclusiveOwnerReadonlyJSON(record, outputPath, 'profile reuse phase-C record',
    dependencies?.repositoryCapture, dependencies?.writeStep)
}

async function reopenExactProfileReuseRecord(record, recordFile) {
  const expected = Buffer.from(`${JSON.stringify(record, null, 2)}\n`)
  const bytes = await readExactFile(
    recordFile, 'profile reuse phase-C record', maxJSON, 0o400)
  assert(bytes.equals(expected),
    'profile reuse phase-C record bytes drifted after immutable creation')
  let reopened
  try { reopened = JSON.parse(bytes.toString('utf8')) }
  catch { throw new Error('profile reuse phase-C record is not JSON after immutable creation') }
  assertProfileReuseRecord(reopened)
  assertSameJSON(reopened, record,
    'profile reuse phase-C record value drifted after immutable creation')
  return reopened
}

async function assertPhaseCReady(directory, tupleIdentity) {
  await assertOwnerDirectory(directory)
  const names = (await readdir(directory)).filter((name) => /^\d{2}-[A-Za-z_]+\.json$/.test(name)).sort()
  const expected = ['preflight', 'install', 'product_doctor', 'setup', 'doctor', 'serve']
  assert(names.length === expected.length, 'phase C requires exactly the completed B2 install chain')
  let previous = null
  for (let index = 0; index < names.length; index++) {
    const input = await readJSONWithDigest(join(directory, names[index]), 'B2 phase receipt', 256 * 1024, 0o400)
    const receipt = input.value
    assertPlainObject(receipt, 'B2 phase receipt')
    assertExactKeys(receipt, [
      'schemaVersion', 'tupleIdentity', 'phase', 'sequence', 'status', 'operationDigest',
      'observationDigest', 'previousReceiptDigest', 'receiptDigest',
    ], 'B2 phase receipt')
    assert(receipt.schemaVersion === 'chora.m1-o4-candidate-phase-receipt.v1' &&
      receipt.status === 'passed' && receipt.tupleIdentity === tupleIdentity &&
      receipt.sequence === index + 1 && receipt.phase === expected[index] &&
      names[index] === `${String(index + 1).padStart(2, '0')}-${expected[index]}.json` &&
      receipt.previousReceiptDigest === previous,
    'B2 phase receipt chain/order drifted before C')
    const { receiptDigest, ...safe } = receipt
    assertDigest(receipt.operationDigest, 'B2 phase operation digest')
    if (receipt.observationDigest !== null) assertDigest(receipt.observationDigest, 'B2 phase observation digest')
    assert(receiptDigest === sha256(canonicalJSONStringify(safe)),
      'B2 phase receipt digest drifted before C')
    previous = receiptDigest
  }
}

function validateCurrentSourceA3(source, manifest) {
  assertPlainObject(source, 'current source A3 ledger')
  assertExactKeys(source, [
    'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
    'bindingDigest', 'tupleIdentity', 'observationSource', 'complete', 'engineEventsUsed',
    'roleImages', 'commandAudit', 'auditRecordCount', 'auditFinalDigest', 'auditSealed', 'attempts',
  ], 'current source A3 ledger')
  assert(source.schemaVersion === SOURCE_A3_SCHEMA && source.status === 'recording' &&
    source.observationSource === 'exact-runner-command-audit' && source.complete === false &&
    source.engineEventsUsed === false && source.auditSealed === false,
  'phase C requires the exact current unsealed source A3 ledger')
  assertSameIdentity(source, manifest, 'source A3/profile reuse manifest')
  assert(source.bindingDigest === manifest.bindingDigest && source.tupleIdentity === manifest.tupleIdentity,
    'source A3/profile reuse binding drifted')
  assertSameJSON(source.roleImages, manifest.roleImages, 'source A3/profile role images drifted')
  assert(Array.isArray(source.commandAudit) && source.commandAudit.length === source.auditRecordCount &&
    source.auditRecordCount === manifest.sourceA3AuditRecordCount,
  'source A3 audit count drifted')
  assertDigest(source.auditFinalDigest, 'source A3 final digest')
  assert(source.auditFinalDigest === manifest.sourceA3AuditFinalDigest,
    'source A3 final digest drifted')
  assert(source.commandAudit.length > 0 && source.commandAudit.at(-1)?.digest === source.auditFinalDigest,
    'source A3 command audit final record drifted')
  let previous = '0'.repeat(64)
  for (let index = 0; index < source.commandAudit.length; index++) {
    const record = source.commandAudit[index]
    assertPlainObject(record, 'source A3 command audit record')
    assertExactKeys(record, [
      'sequence', 'phase', 'subsystem', 'invocation', 'operationClass', 'safeTargetSha256',
      'result', 'previousDigest', 'digest',
    ], 'source A3 command audit record')
    assert(record.sequence === index + 1 && record.previousDigest === previous,
      'source A3 command audit chain drifted')
    const { digest, ...draft } = record
    assertDigest(digest, 'source A3 command record digest')
    assert(digest === sha256(canonicalJSONStringify(draft)), 'source A3 command record digest drifted')
    previous = digest
  }
  assert(Array.isArray(source.attempts) && source.attempts.length === 4,
    'phase C source A3 must contain exactly Minimal plus three Standard Attempts')
}

function validateManagedSourceAttempt(receipt, sourceAttempt, ordinal) {
  assertPlainObject(sourceAttempt, 'source A3 managed Attempt')
  assertExactKeys(sourceAttempt, [
    'sourceAttemptOrdinal', 'profile', 'cohortSequence', 'taskId', 'runId', 'attemptId',
    'scenario', 'attemptState', 'terminalReason', 'verifierRequired', 'verifierStartSequence',
    'verifierEndSequence', 'verifierSliceDigest', 'workspaceIdentity', 'tupleIdentity',
    'executionIdentityDigest', 'status', 'runtimeIdentitySha256', 'resourceLedgerSha256',
    'roleTupleDigest', 'frameworkRetryCount', 'hiddenRetryCount', 'auditStartSequence',
    'auditEndSequence', 'auditSliceDigest', 'operationCounts', 'terminalResidue',
  ], 'source A3 managed Attempt')
  assert(sourceAttempt.sourceAttemptOrdinal === ordinal &&
    sourceAttempt.profile === receipt.profile &&
    sourceAttempt.cohortSequence === receipt.cohortSequence &&
    sourceAttempt.scenario === null && sourceAttempt.attemptState === 'output_submitted' &&
    sourceAttempt.terminalReason === null && sourceAttempt.status === 'succeeded' &&
    sourceAttempt.verifierRequired === true,
  'source A3 managed Attempt profile/state order drifted')
  for (const field of [
    'taskId', 'runId', 'attemptId', 'workspaceIdentity', 'tupleIdentity',
    'executionIdentityDigest', 'runtimeIdentitySha256', 'resourceLedgerSha256', 'roleTupleDigest',
    'frameworkRetryCount', 'hiddenRetryCount',
  ]) assert(sourceAttempt[field] === receipt[field], `source A3 managed Attempt ${field} drifted`)
  assert(sourceAttempt.auditStartSequence === receipt.attemptSource.startSequence &&
    sourceAttempt.auditEndSequence === receipt.attemptSource.endSequence &&
    sourceAttempt.auditSliceDigest === receipt.attemptSource.sliceDigest,
  'source A3 managed Attempt range/digest drifted')
  assert(sourceAttempt.verifierStartSequence === receipt.verifierSource.startSequence &&
    sourceAttempt.verifierEndSequence === receipt.verifierSource.endSequence &&
    sourceAttempt.verifierSliceDigest === receipt.verifierSource.sliceDigest,
  'source A3 managed Verifier range/digest drifted')
  assertSameJSON(sourceAttempt.operationCounts, receipt.operationCounts,
    'source A3 managed Attempt image-operation counts drifted')
  assertSameJSON(sourceAttempt.terminalResidue, receipt.terminalResidue,
    'source A3 managed Attempt residue drifted')
}

function validateReceiptAgainstManifest(receipt, manifest) {
  assertSameIdentity(receipt, manifest, `profile reuse ${receipt.journey}/manifest`)
  assert(receipt.bindingDigest === manifest.bindingDigest && receipt.tupleIdentity === manifest.tupleIdentity,
    `profile reuse ${receipt.journey} tuple/binding drifted`)
  if (managedProfiles.has(receipt.profile)) {
    assert(receipt.roleTupleDigest === manifest.roleTupleDigest,
      `profile reuse ${receipt.journey} role tuple drifted`)
  } else {
    assert(receipt.roleTupleDigest === null,
      'Trusted Local must not bind the managed role image tuple')
  }
}

function validateCohort(receipts) {
  assert(receipts.length === journeys.length, 'profile reuse receipt cardinality drifted')
  for (const field of ['taskId', 'runId', 'attemptId', 'workspaceIdentity', 'executionIdentityDigest',
    'runtimeIdentitySha256', 'resourceLedgerSha256', 'publicActionDigest', 'publicObservationDigest']) {
    assertUnique(receipts.map((receipt) => receipt[field]), `profile reuse ${field} values`)
  }
  const standards = receipts.filter(({ profile }) => profile === 'standard')
  assert(standards.length === 3 && standards.every((receipt, index) => receipt.cohortSequence === index + 1),
    'Standard reuse cohort is not exactly sequence 1, 2, 3')
  const apply = standards.filter(({ profileEvidence }) => profileEvidence.taskOwnedApply)
  assert(apply.length === 1 && apply[0].cohortSequence === 1 &&
    apply[0].profileEvidence.acceptedReview === true,
  'only Standard 1 may prove accepted review and Task-owned Apply')
  const trusted = receipts.at(-1)
  assertUnique(receipts.map(({ resourceRequestDigest }) => resourceRequestDigest),
    'profile reuse observer request digests')
  assertUnique(receipts.map(({ resourceAckDigest }) => resourceAckDigest),
    'profile reuse observer acknowledgement digests')
  assert(new Set(receipts.map(({ resourceObserverSha256 }) => resourceObserverSha256)).size === 1,
    'profile reuse observer identity drifted within the cohort')
  for (let index = 1; index < 4; index++) {
    assert(receipts[index].attemptSource.startSequence > receipts[index - 1].verifierSource.endSequence,
      'Managed profile source ranges are out of order or overlap')
  }
  assert(trusted.verifierSource.startSequence > receipts.at(-2).verifierSource.endSequence,
    'Trusted Local Verifier range precedes the complete Standard cohort')
  for (let index = 1; index < receipts.length; index++) {
    assert(Date.parse(receipts[index].publicStartedAt) >=
      Date.parse(receipts[index - 1].publicCompletedAt),
    'public profile sequence overlaps or is out of order')
  }
}

function validateReceiptSourceRanges(receipt, commandAudit) {
  const fields = receipt.profile === 'trusted_local' ?
    [['verifierSource', 'verifier']] :
    [['attemptSource', 'attempt'], ['verifierSource', 'verifier']]
  for (const [field, expectedPhase] of fields) {
    const range = receipt[field]
    assert(range.endSequence <= commandAudit.length,
      `${receipt.journey} ${field} exceeds the current source A3 ledger`)
    const records = commandAudit.slice(range.startSequence - 1, range.endSequence)
    assert(records.length === range.endSequence - range.startSequence + 1 &&
      records.every((record) => record.phase === expectedPhase),
    `${receipt.journey} ${field} does not select one exact ${expectedPhase} range`)
    assert(range.sliceDigest === sha256(canonicalJSONStringify(records)),
      `${receipt.journey} ${field} digest drifted from source A3`)
  }
}

function validateManagedReceipt(receipt) {
  const expectedCapability = receipt.profile === 'minimal' ? 'chora.minimal.v1' : 'chora.standard.v1'
  assert(receipt.runtimeSource === 'managed_pi_image' && receipt.executionProvider === 'docker' &&
    receipt.capabilityPolicy === expectedCapability && receipt.sandboxState === 'managed' &&
    receipt.managedSandbox === true && receipt.managedFallbackUsed === false,
  `${receipt.profile} managed Pi/Sandbox contract drifted`)
  validateSourceRange(receipt.attemptSource, `${receipt.profile} Attempt source`)
  assert(receipt.attemptSource.endSequence < receipt.verifierSource.startSequence,
    `${receipt.profile} Attempt/Verifier source order drifted`)
  assertDigest(receipt.roleTupleDigest, `${receipt.profile} role tuple digest`)
  validateZeroCounts(receipt.operationCounts, zeroOperationKeys,
    `${receipt.profile} Attempt image operations`)
  assertPlainObject(receipt.profileEvidence, `${receipt.profile} profile evidence`)
  if (receipt.profile === 'minimal') {
    assertExactKeys(receipt.profileEvidence, [
      'bashObserved', 'editObserved', 'prohibitedCapabilityDenialCount',
      'prohibitedCapabilityDenialDigest', 'independentVerification',
    ], 'Minimal profile evidence')
    assert(receipt.profileEvidence.bashObserved === true && receipt.profileEvidence.editObserved === true &&
      receipt.profileEvidence.prohibitedCapabilityDenialCount === 1 &&
      receipt.profileEvidence.independentVerification === true,
    'Minimal capability/Verification predicates are incomplete')
    assertDigest(receipt.profileEvidence.prohibitedCapabilityDenialDigest,
      'Minimal safe prohibited-capability denial digest')
  } else {
    assertExactKeys(receipt.profileEvidence, [
      'fullRepositoryPolicy', 'securityPolicy', 'independentVerification', 'acceptedReview',
      'taskOwnedApply', 'patchDigest', 'applyTargetDigest',
    ], 'Standard profile evidence')
    assert(receipt.profileEvidence.fullRepositoryPolicy === true &&
      receipt.profileEvidence.securityPolicy === true &&
      receipt.profileEvidence.independentVerification === true,
    'Standard repository/security/Verification predicates are incomplete')
    const applies = receipt.cohortSequence === 1
    assert(receipt.profileEvidence.acceptedReview === applies &&
      receipt.profileEvidence.taskOwnedApply === applies,
    'Standard review/Apply sequence drifted')
    if (applies) {
      assertDigest(receipt.profileEvidence.patchDigest, 'Standard 1 Patch digest')
      assertDigest(receipt.profileEvidence.applyTargetDigest, 'Standard 1 Apply target digest')
    } else {
      assert(receipt.profileEvidence.patchDigest === null && receipt.profileEvidence.applyTargetDigest === null,
        'non-Apply Standard receipt carries Apply proof')
    }
  }
}

function validateTrustedLocalReceipt(receipt) {
  assert(receipt.runtimeSource === 'local_pi' && receipt.executionProvider === 'trusted_host' &&
    receipt.capabilityPolicy === 'pi.native' && receipt.sandboxState === 'none' &&
    receipt.managedSandbox === false && receipt.managedFallbackUsed === false,
  'Trusted Local runtime/No Sandbox contract drifted')
  assert(receipt.attemptSource === null && receipt.roleTupleDigest === null &&
    receipt.operationCounts === null,
  'Trusted Local falsely claims a Docker Attempt, image tuple, or image operations')
  assertPlainObject(receipt.profileEvidence, 'Trusted Local profile evidence')
  assertExactKeys(receipt.profileEvidence, [
    'resolutionOrder', 'selectedSource', 'acknowledgementPolicyVersion',
    'acknowledgementPolicySha256', 'acknowledgedAt', 'acknowledgementCurrent',
    'executionTarget', 'noSandbox', 'choraResultSemantics', 'selectedPiPath',
    'selectedPiSourceRoot', 'piExecutableSha256', 'piSourceProvenanceSha256',
    'runtimeFingerprint', 'processGroupIdentitySha256', 'sessionIdentitySha256',
    'processStartedAt', 'processExitedAt', 'processGroupTerminated', 'sessionClosed',
    'terminalCleanupDigest',
  ], 'Trusted Local profile evidence')
  assert(receipt.profileEvidence.resolutionOrder === 'path_first_private_fallback' &&
    ['path', 'private_fallback'].includes(receipt.profileEvidence.selectedSource) &&
    /^chora\.trusted-local-disclosure\.v[1-9][0-9]*$/.test(receipt.profileEvidence.acknowledgementPolicyVersion) &&
    receipt.profileEvidence.acknowledgementCurrent === true &&
    receipt.profileEvidence.executionTarget === 'trusted-host' &&
    receipt.profileEvidence.noSandbox === true && receipt.profileEvidence.choraResultSemantics === true,
  'Trusted Local acknowledgement/source/Result predicates are incomplete')
  exactAbsolutePath(receipt.profileEvidence.selectedPiPath, 'Trusted Local selected Pi')
  exactAbsolutePath(receipt.profileEvidence.selectedPiSourceRoot, 'Trusted Local Pi source root')
  assert(receipt.profileEvidence.selectedPiPath !== receipt.profileEvidence.selectedPiSourceRoot,
    'Trusted Local Pi executable/source topology is invalid')
  for (const field of [
    'piExecutableSha256', 'piSourceProvenanceSha256', 'runtimeFingerprint',
    'processGroupIdentitySha256', 'sessionIdentitySha256', 'terminalCleanupDigest',
  ]) assertDigest(receipt.profileEvidence[field], `Trusted Local ${field}`)
  assert(receipt.runtimeIdentitySha256 === sha256(canonicalJSONStringify({
    runtimeFingerprint: receipt.profileEvidence.runtimeFingerprint,
    processGroupIdentitySha256: receipt.profileEvidence.processGroupIdentitySha256,
    sessionIdentitySha256: receipt.profileEvidence.sessionIdentitySha256,
  })), 'Trusted Local runtime/process/session identity binding drifted')
  validateTimeRange(receipt.profileEvidence.processStartedAt,
    receipt.profileEvidence.processExitedAt, 'Trusted Local process lifecycle')
  assert(receipt.profileEvidence.processGroupTerminated === true &&
    receipt.profileEvidence.sessionClosed === true,
  'Trusted Local process group/session did not terminate cleanly')
  assertDigest(receipt.profileEvidence.acknowledgementPolicySha256,
    'Trusted Local acknowledgement policy digest')
  assert(receipt.profileEvidence.acknowledgementPolicySha256 ===
    sha256(receipt.profileEvidence.acknowledgementPolicyVersion),
  'Trusted Local acknowledgement policy digest drifted')
  assert(typeof receipt.profileEvidence.acknowledgedAt === 'string' &&
    Number.isFinite(Date.parse(receipt.profileEvidence.acknowledgedAt)),
  'Trusted Local acknowledgement time is invalid')
}

function validateResourceLedger(resource, receipt) {
  assertPlainObject(resource, 'profile reuse resource ledger')
  assertExactKeys(resource, [
    'schemaVersion', 'status', 'profile', 'taskId', 'runId', 'attemptId',
    'workspaceIdentity', 'tupleIdentity', 'executionIdentityDigest', 'runtimeIdentitySha256',
    'managedInventory', 'trustedHost',
  ], 'profile reuse resource ledger')
  assert(resource.schemaVersion === PROFILE_REUSE_RESOURCE_SCHEMA && resource.status === 'terminal',
    'profile reuse resource ledger schema/status drifted')
  for (const field of [
    'profile', 'taskId', 'runId', 'attemptId', 'workspaceIdentity', 'tupleIdentity',
    'executionIdentityDigest', 'runtimeIdentitySha256',
  ]) assert(resource[field] === receipt[field], `profile reuse resource ledger ${field} drifted`)
  if (managedProfiles.has(receipt.profile)) {
    assert(resource.trustedHost === null, 'Managed resource ledger carries Trusted Host evidence')
    validateEmptyInventory(resource.managedInventory, 'Managed resource inventory')
    return
  }
  assert(resource.managedInventory === null,
    'Trusted Local resource ledger falsely claims managed inventory')
  const trusted = resource.trustedHost
  assertPlainObject(trusted, 'Trusted Local resource evidence')
  assertExactKeys(trusted, [
    'selectedPiPath', 'selectedPiSourceRoot', 'selectedSource', 'piExecutableSha256',
    'piSourceProvenanceSha256', 'runtimeFingerprint', 'processGroupIdentitySha256',
    'sessionIdentitySha256', 'processStartedAt', 'processExitedAt',
    'processGroupTerminated', 'sessionClosed', 'terminalCleanupDigest', 'terminalInventory',
    'verifierSource',
  ], 'Trusted Local resource evidence')
  const proof = receipt.profileEvidence
  for (const field of [
    'selectedPiPath', 'selectedPiSourceRoot', 'selectedSource', 'piExecutableSha256',
    'piSourceProvenanceSha256', 'runtimeFingerprint', 'processGroupIdentitySha256',
    'sessionIdentitySha256', 'processStartedAt', 'processExitedAt',
    'processGroupTerminated', 'sessionClosed', 'terminalCleanupDigest',
  ]) assert(trusted[field] === proof[field], `Trusted Local resource ${field} drifted`)
  validateEmptyInventory(trusted.terminalInventory, 'Trusted Local terminal inventory')
  assertSameJSON(trusted.verifierSource, receipt.verifierSource,
    'Trusted Local resource Verifier range drifted')
  assert(trusted.terminalCleanupDigest ===
    sha256(canonicalJSONStringify(trusted.terminalInventory)),
  'Trusted Local terminal cleanup digest drifted')
}

function validateSupervisorResourceEnvelope(resource, expected) {
  assertPlainObject(resource, 'supervisor raw resource ledger')
  assertExactKeys(resource, [
    'schemaVersion', 'status', 'profile', 'taskId', 'runId', 'attemptId',
    'workspaceIdentity', 'tupleIdentity', 'executionIdentityDigest', 'runtimeIdentitySha256',
    'managedInventory', 'trustedHost',
  ], 'supervisor raw resource ledger')
  assert(resource.schemaVersion === PROFILE_REUSE_RESOURCE_SCHEMA && resource.status === 'terminal',
    'supervisor raw resource schema/status drifted')
  for (const field of [
    'profile', 'taskId', 'runId', 'attemptId', 'workspaceIdentity', 'tupleIdentity',
    'executionIdentityDigest',
  ]) assert(resource[field] === expected[field], `supervisor raw resource ${field} drifted`)
  assertDigest(resource.runtimeIdentitySha256, 'supervisor raw resource runtime identity')
  if (managedProfiles.has(expected.profile)) {
    assert(resource.trustedHost === null, 'managed supervisor resource carries Trusted Host proof')
    validateEmptyInventory(resource.managedInventory, 'managed supervisor raw resource inventory')
    return
  }
  assert(resource.managedInventory === null,
    'Trusted supervisor raw resource carries managed inventory')
  const trusted = resource.trustedHost
  assertPlainObject(trusted, 'Trusted supervisor raw resource proof')
  assertExactKeys(trusted, [
    'selectedPiPath', 'selectedPiSourceRoot', 'selectedSource', 'piExecutableSha256',
    'piSourceProvenanceSha256', 'runtimeFingerprint', 'processGroupIdentitySha256',
    'sessionIdentitySha256', 'processStartedAt', 'processExitedAt',
    'processGroupTerminated', 'sessionClosed', 'terminalCleanupDigest', 'terminalInventory',
    'verifierSource',
  ], 'Trusted supervisor raw resource proof')
  exactAbsolutePath(trusted.selectedPiPath, 'Trusted supervisor selected Pi')
  exactAbsolutePath(trusted.selectedPiSourceRoot, 'Trusted supervisor Pi source root')
  assert(['path', 'private_fallback'].includes(trusted.selectedSource),
    'Trusted supervisor selected source drifted')
  for (const field of [
    'piExecutableSha256', 'piSourceProvenanceSha256', 'runtimeFingerprint',
    'processGroupIdentitySha256', 'sessionIdentitySha256', 'terminalCleanupDigest',
  ]) assertDigest(trusted[field], `Trusted supervisor ${field}`)
  validateTimeRange(trusted.processStartedAt, trusted.processExitedAt,
    'Trusted supervisor process lifecycle')
  assert(trusted.processGroupTerminated === true && trusted.sessionClosed === true,
    'Trusted supervisor lifecycle is not terminal')
  validateEmptyInventory(trusted.terminalInventory, 'Trusted supervisor terminal inventory')
  assert(trusted.terminalCleanupDigest === sha256(canonicalJSONStringify(trusted.terminalInventory)),
    'Trusted supervisor cleanup digest drifted')
  assert(resource.runtimeIdentitySha256 === sha256(canonicalJSONStringify({
    runtimeFingerprint: trusted.runtimeFingerprint,
    processGroupIdentitySha256: trusted.processGroupIdentitySha256,
    sessionIdentitySha256: trusted.sessionIdentitySha256,
  })), 'Trusted supervisor runtime/process/session binding drifted')
  validateSourceRange(trusted.verifierSource, 'Trusted supervisor Verifier source')
}

function validateBoundarySourceA3(source, expected) {
  assertPlainObject(source, 'source A3 at resource boundary')
  assert(source.schemaVersion === SOURCE_A3_SCHEMA && source.status === 'recording' &&
    source.tupleIdentity === expected.tupleIdentity && Array.isArray(source.attempts),
  'source A3 boundary schema/status/tuple drifted')
  const expectedCount = expected.profile === 'trusted_local' ? 4 : expected.sequence
  assert(source.attempts.length === expectedCount,
    'source A3 managed Attempt boundary count drifted')
  if (managedProfiles.has(expected.profile)) {
    const attempt = source.attempts[expected.sequence - 1]
    assertPlainObject(attempt, 'source A3 boundary managed Attempt')
    assert(attempt.profile === expected.profile && attempt.taskId === expected.taskId &&
      attempt.runId === expected.runId && attempt.attemptId === expected.attemptId &&
      attempt.resourceLedgerSha256 === expected.resourceSha256,
    'source A3 did not pre-bind the supervisor raw resource bytes')
    return
  }
  assert(!source.attempts.some((attempt) => attempt.taskId === expected.taskId ||
    attempt.runId === expected.runId || attempt.attemptId === expected.attemptId),
  'Trusted Local was inserted into source A3 as a managed Attempt')
}

function validateObserverEvidence(request, ack, receipt, attestation) {
  assertPlainObject(request, 'profile reuse observer request')
  assertExactKeys(request, [
    'schemaVersion', 'status', 'sequence', 'journey', 'profile', 'tupleIdentity',
    'taskId', 'runId', 'attemptId', 'workspaceIdentity', 'executionIdentityDigest',
    'publicObservationDigest', 'publicCompletedAt', 'rawResourceSha256', 'requestedAt',
    'requestNonce', 'observerSha256', 'ownedPathController', 'requestDigest',
  ], 'profile reuse observer request')
  const expected = {
    sequence: receipt.sequence, journey: receipt.journey, profile: receipt.profile,
    tupleIdentity: receipt.tupleIdentity, taskId: receipt.taskId, runId: receipt.runId,
    attemptId: receipt.attemptId, workspaceIdentity: receipt.workspaceIdentity,
    executionIdentityDigest: receipt.executionIdentityDigest,
    publicObservationDigest: receipt.publicObservationDigest,
    publicCompletedAt: receipt.publicCompletedAt,
    rawResourceSha256: receipt.resourceLedgerSha256,
    observerSha256: receipt.resourceObserverSha256,
  }
  assert(request.schemaVersion === PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA &&
    request.status === 'requested', 'profile reuse observer request schema/status drifted')
  for (const [field, value] of Object.entries(expected)) {
    assert(request[field] === value, `profile reuse observer request ${field} drifted`)
  }
  assertDigest(request.requestNonce, 'profile reuse observer request nonce')
  validateOwnedPathCapability(request.ownedPathController,
    'profile reuse observer request owned-path capability')
  assert(typeof request.requestedAt === 'string' && Number.isFinite(Date.parse(request.requestedAt)) &&
    Date.parse(request.requestedAt) >= Date.parse(receipt.publicCompletedAt),
  'profile reuse observer request did not occur after the public terminal boundary')
  const { requestDigest, ...draft } = request
  assert(requestDigest === sha256(canonicalJSONStringify(draft)) &&
    requestDigest === receipt.resourceRequestDigest &&
    requestDigest === attestation.requestDigest,
  'profile reuse observer request digest binding drifted')
  validateObserverAck(ack, {
    sequence: receipt.sequence, journey: receipt.journey, requestDigest,
    resourceSha256: receipt.resourceLedgerSha256,
    observerSha256: receipt.resourceObserverSha256, requestedAt: request.requestedAt,
  })
  assert(ack.ackDigest === receipt.resourceAckDigest &&
    ack.ackDigest === attestation.ackDigest &&
    ack.observerSha256 === attestation.observerSha256 &&
    ack.resourceSha256 === attestation.resourceSha256,
  'profile reuse observer acknowledgement/receipt/manifest binding drifted')
}

function validateObserverAck(ack, expected) {
  assertPlainObject(ack, 'profile reuse observer acknowledgement')
  assertExactKeys(ack, [
    'schemaVersion', 'status', 'sequence', 'journey', 'requestDigest',
    'resourceSha256', 'observerSha256', 'observedAt', 'ackDigest',
  ], 'profile reuse observer acknowledgement')
  assert(ack.schemaVersion === PROFILE_REUSE_OBSERVER_ACK_SCHEMA && ack.status === 'observed' &&
    ack.sequence === expected.sequence && ack.journey === expected.journey &&
    ack.requestDigest === expected.requestDigest && ack.resourceSha256 === expected.resourceSha256 &&
    ack.observerSha256 === expected.observerSha256,
  'profile reuse observer acknowledgement binding drifted')
  for (const field of ['requestDigest', 'resourceSha256', 'observerSha256', 'ackDigest']) {
    assertDigest(ack[field], `profile reuse observer acknowledgement ${field}`)
  }
  assert(typeof ack.observedAt === 'string' && Number.isFinite(Date.parse(ack.observedAt)) &&
    Date.parse(ack.observedAt) >= Date.parse(expected.requestedAt),
  'profile reuse observer acknowledgement time drifted')
  const { ackDigest, ...draft } = ack
  assert(ackDigest === sha256(canonicalJSONStringify(draft)),
    'profile reuse observer acknowledgement digest drifted')
}

async function runObserver(observerFile, argumentsList, timeoutMs) {
  await new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(observerFile, argumentsList, {
      cwd: '/', shell: false, stdio: ['ignore', 'pipe', 'pipe'],
      env: { LANG: 'C', LC_ALL: 'C', PATH: `${dirname(process.execPath)}:/usr/bin:/bin` },
      detached: true,
    })
    let stdout = Buffer.alloc(0)
    let stderr = Buffer.alloc(0)
    let failure = null
    let finalized = false
    let timer
    const fail = (error) => {
      if (failure === null) failure = error
      try {
        killObserverProcessGroup(child)
      } catch (terminationError) {
        failure = terminationError
      }
    }
    const append = (current, chunk, label) => {
      const next = Buffer.concat([current, chunk])
      if (next.length > 64 * 1024) {
        fail(new Error(`profile reuse observer ${label} exceeded the bounded protocol`))
      }
      return next
    }
    child.stdout.on('data', (chunk) => { stdout = append(stdout, chunk, 'stdout') })
    child.stderr.on('data', (chunk) => { stderr = append(stderr, chunk, 'stderr') })
    child.on('error', fail)
    child.on('close', (code, signal) => {
      if (finalized) return
      finalized = true
      clearTimeout(timer)
      void (async () => {
        try {
          if (failure === null &&
            (code !== 0 || signal !== null || stdout.length !== 0 || stderr.length !== 0)) {
            failure = new Error('profile reuse observer did not complete the silent exit-zero protocol')
          }
          if (failure === null && observerProcessGroupExists(child.pid)) {
            failure = new Error('profile reuse observer left a descendant process')
          }
          if (failure !== null) killObserverProcessGroup(child)
          await waitForObserverProcessGroupExit(child.pid)
        } catch (terminationError) {
          failure = terminationError
        }
        if (failure !== null) rejectPromise(failure)
        else resolvePromise()
      })()
    })
    timer = setTimeout(() => {
      fail(new Error('profile reuse observer timed out'))
    }, timeoutMs)
  })
}

function killObserverProcessGroup(child) {
  if (!Number.isSafeInteger(child.pid) || child.pid <= 0) return
  try {
    process.kill(-child.pid, 'SIGKILL')
  } catch (error) {
    if (error.code !== 'ESRCH') throw error
  }
}

function observerProcessGroupExists(processGroupId) {
  if (!Number.isSafeInteger(processGroupId) || processGroupId <= 0) return false
  try {
    process.kill(-processGroupId, 0)
    return true
  } catch (error) {
    if (error.code === 'ESRCH') return false
    throw error
  }
}

async function waitForObserverProcessGroupExit(processGroupId) {
  if (!Number.isSafeInteger(processGroupId) || processGroupId <= 0) return
  const deadline = Date.now() + 2_000
  while (observerProcessGroupExists(processGroupId)) {
    if (Date.now() >= deadline) {
      throw new Error('profile reuse observer process group did not terminate')
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 10))
  }
}

async function assertFailedObserverPairAbsent(resourceFile, ackFile) {
  for (const path of [resourceFile, ackFile]) {
    try {
      await lstat(path)
      throw new Error('failed observer left a publishable resource/ACK path')
    } catch (error) {
      if (error.code !== 'ENOENT') throw error
    }
  }
}

function validateEmptyInventory(inventory, label) {
  assertPlainObject(inventory, label)
  assertExactKeys(inventory, zeroResidueKeys, label)
  for (const key of zeroResidueKeys) {
    assert(Array.isArray(inventory[key]) && inventory[key].length === 0,
      `${label} ${key} is non-empty`)
  }
}

function validateSharedIdentity(value, label) {
  assertEnvironmentID(value.environmentId, `${label} environment ID`)
  assert(typeof value.installId === 'string' && opaqueInstallRE.test(value.installId),
    `${label} install ID is invalid`)
  assert(typeof value.generationId === 'string' && opaqueInstallRE.test(value.generationId),
    `${label} generation ID is invalid`)
  validatePlatform(value.platform)
  assertDigest(value.bindingDigest, `${label} binding digest`)
  assertDigest(value.tupleIdentity, `${label} tuple identity`)
}

function validateSourceRange(value, label) {
  assertPlainObject(value, label)
  assertExactKeys(value, ['startSequence', 'endSequence', 'sliceDigest'], label)
  assert(Number.isSafeInteger(value.startSequence) && value.startSequence > 0 &&
    Number.isSafeInteger(value.endSequence) && value.endSequence >= value.startSequence,
  `${label} range is invalid`)
  assertDigest(value.sliceDigest, `${label} digest`)
}

function validateTimeRange(startedAt, completedAt, label) {
  assert(typeof startedAt === 'string' && typeof completedAt === 'string',
    `${label} timestamps are missing`)
  const start = Date.parse(startedAt)
  const end = Date.parse(completedAt)
  assert(Number.isFinite(start) && Number.isFinite(end) && end >= start,
    `${label} timestamps are invalid or reversed`)
}

function validateRoleImages(roleImages) {
  assertPlainObject(roleImages, 'profile reuse role images')
  assertExactKeys(roleImages, O4_ROLES, 'profile reuse role images')
  for (const role of O4_ROLES) {
    const image = roleImages[role]
    assertPlainObject(image, `profile reuse ${role} image`)
    assertExactKeys(image, ['artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId'],
      `profile reuse ${role} image`)
    assertDigest(image.archiveSha256, `profile reuse ${role} archive digest`)
    assert(Number.isSafeInteger(image.archiveSize) && image.archiveSize > 0,
      `profile reuse ${role} archive size is invalid`)
    assertImageDigest(image.dockerConfigImageId, `profile reuse ${role} Docker config image ID`)
  }
  const managed = roleImages.managed_pi_runtime
  for (const role of ['independent_verifier', 'capability_probe']) {
    for (const field of ['artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId']) {
      assert(roleImages[role][field] === managed[field], `profile reuse ${role} alias drifted`)
    }
  }
  assert(managed.artifactId === 'managed-pi-runtime' &&
    roleImages.network_boundary.artifactId === 'network-boundary',
  'profile reuse role artifact identity drifted')
  assert(roleImages.network_boundary.dockerConfigImageId !== managed.dockerConfigImageId,
    'profile reuse managed/network images are not distinct')
}

function validateFileIndex(index, expectedNames, label) {
  assert(Array.isArray(index) && index.length === expectedNames.length, `${label} cardinality drifted`)
  for (let offset = 0; offset < expectedNames.length; offset++) {
    const entry = index[offset]
    assertPlainObject(entry, `${label} entry`)
    assertExactKeys(entry, ['sequence', 'file', 'sha256'], `${label} entry`)
    assert(entry.sequence === offset + 1 && entry.file === expectedNames[offset], `${label} order/name drifted`)
    assertDigest(entry.sha256, `${label} digest`)
  }
  assertUnique(index.map(({ file }) => file), `${label} filenames`)
  assertUnique(index.map(({ sha256 }) => sha256), `${label} digests`)
}

function validateObserverAttestationIndex(index, resources) {
  assert(Array.isArray(index) && index.length === journeys.length,
    'profile reuse observer attestation index cardinality drifted')
  for (let offset = 0; offset < journeys.length; offset++) {
    const entry = index[offset]
    const stem = `${String(offset + 1).padStart(2, '0')}-${journeys[offset].journey.replaceAll('_', '-')}`
    assertPlainObject(entry, 'profile reuse observer attestation index entry')
    assertExactKeys(entry, [
      'sequence', 'requestFile', 'requestSha256', 'requestDigest', 'ackFile',
      'ackSha256', 'ackDigest', 'observerSha256', 'resourceSha256',
    ], 'profile reuse observer attestation index entry')
    assert(entry.sequence === offset + 1 && entry.requestFile === `${stem}-request.json` &&
      entry.ackFile === `${stem}-ack.json` && entry.resourceSha256 === resources[offset].sha256,
    'profile reuse observer attestation index order/name/resource drifted')
    for (const field of [
      'requestSha256', 'requestDigest', 'ackSha256', 'ackDigest',
      'observerSha256', 'resourceSha256',
    ]) assertDigest(entry[field], `profile reuse observer attestation ${field}`)
  }
  for (const field of ['requestFile', 'requestSha256', 'requestDigest', 'ackFile', 'ackSha256', 'ackDigest']) {
    assertUnique(index.map((entry) => entry[field]), `profile reuse observer attestation ${field}`)
  }
  assert(new Set(index.map(({ observerSha256 }) => observerSha256)).size === 1,
    'profile reuse observer attestation identity drifted')
}

function validateZeroCounts(value, keys, label) {
  assertPlainObject(value, label)
  assertExactKeys(value, keys, label)
  for (const key of keys) assert(value[key] === 0, `${label} ${key} is non-zero`)
}

async function exactIndexedFiles(directory, names, label) {
  await assertOwnerDirectory(directory)
  const actual = (await readdir(directory)).filter((name) => name.endsWith('.json')).sort()
  assertSameJSON(actual, [...names].sort(), `${label} contain a missing or extra JSON file`)
  return names.map((name) => join(directory, name))
}

async function readJSONWithDigest(path, label, maxBytes, mode) {
  const bytes = await readExactFile(path, label, maxBytes, mode)
  let value
  try { value = JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) }
  return { value, sha256: sha256(bytes) }
}

async function readExactFile(path, label, maxBytes, mode) {
  exactAbsolutePath(path, label)
  await assertNoSymlinkAncestors(path)
  const info = await lstat(path)
  assert(info.isFile() && !info.isSymbolicLink() && info.nlink === 1 && info.size > 0 && info.size <= maxBytes,
    `${label} must be one bounded non-linked regular file`)
  assert(info.uid === process.getuid(), `${label} must be owner-owned`)
  if (mode !== undefined) assert((info.mode & 0o777) === mode, `${label} mode drifted`)
  return readFile(path)
}

async function validatePinnedObserver(observerFile, observerSha256) {
  const bytes = await readExactFile(observerFile, 'profile reuse observer invocation',
    16 * 1024 * 1024, 0o500)
  assert(sha256(bytes) === observerSha256,
    'profile reuse observer changed before or during invocation')
}

async function writeExclusiveOwnerReadonlyJSON(
  value, path, label, capabilityValue, writeStep = null,
) {
  const capability = validateOwnedPathCapability(capabilityValue, `${label} owned-path capability`)
  assert(writeStep === null || typeof writeStep === 'function',
    `${label} write-step dependency is invalid`)
  exactAbsolutePath(path, label)
  await assertNoSymlinkAncestors(path)
  await ensureOwnerDirectory(dirname(path))
  let handle
  let createdIdentity = null
  try {
    handle = await open(path, 'wx', 0o600)
    createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true }))
    await handle.writeFile(`${JSON.stringify(value, null, 2)}\n`)
    await handle.sync()
    await handle.chmod(0o400)
    await handle.sync()
    createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true }))
    if (writeStep !== null) await writeStep({ stage: 'post-chmod', path })
    await handle.close()
    handle = undefined
    if (writeStep !== null) await writeStep({ stage: 'post-close', path })
    const info = await lstat(path, { bigint: true })
    assert(matchesOwnedFileIdentity(info, createdIdentity) && info.nlink === 1n &&
      Number(info.mode & 0o777n) === 0o400,
    `${label} immutable creation failed`)
  } catch (error) {
    const cleanupErrors = []
    if (handle) {
      try { createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true })) }
      catch (cleanupError) { cleanupErrors.push(cleanupError) }
      try { await handle.close() } catch (cleanupError) { cleanupErrors.push(cleanupError) }
      handle = undefined
    }
    if (createdIdentity !== null) {
      try {
        await cleanupOwnedPath({ capability, path, identity: createdIdentity,
          disposition: 'regular_file' })
      } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    }
    if (cleanupErrors.length > 0) throw new AggregateError(
      [error, ...cleanupErrors], `${label} write and native cleanup failed`, { cause: error })
    throw error
  }
}

function matchesOwnedFileIdentity(info, identity) {
  return identity !== null && info.isFile() && !info.isSymbolicLink() &&
    info.dev.toString() === identity.dev && info.ino.toString() === identity.ino &&
    Number(info.uid) === identity.uid && Number(info.gid) === identity.gid &&
    Number(info.mode) === identity.mode && identity.uid === process.getuid()
}

async function ensureOwnerDirectory(path) {
  try { await mkdir(path, { recursive: false, mode: 0o700 }) } catch (error) {
    if (error.code !== 'EEXIST') throw error
  }
  await assertOwnerDirectory(path)
}

async function assertOwnerDirectory(path) {
  exactAbsolutePath(path, 'profile reuse directory')
  await assertNoSymlinkAncestors(path)
  const info = await lstat(path)
  assert(info.isDirectory() && !info.isSymbolicLink() && info.uid === process.getuid() &&
    (info.mode & 0o777) === 0o700, 'profile reuse directory must be owner-owned 0700')
}

async function assertNoSymlinkAncestors(path) {
  let current = resolve(path)
  while (true) {
    try {
      const info = await lstat(current)
      assert(!info.isSymbolicLink(), 'profile reuse path has a symlink ancestor')
    } catch (error) {
      if (error.code !== 'ENOENT') throw error
    }
    const parent = dirname(current)
    if (parent === current) return
    current = parent
  }
}

function exactAbsolutePath(value, label) {
  assert(typeof value === 'string' && isAbsolute(value) && normalize(value) === value && resolve(value) === value,
    `${label} path must be exact and absolute`)
}

function pathInside(parent, child) {
  const value = relative(parent, child)
  return value === '' || (!value.startsWith('..') && !isAbsolute(value))
}

function assertSameIdentity(left, right, label) {
  for (const field of ['environmentId', 'installId', 'generationId']) {
    assert(left[field] === right[field], `${label} ${field} drifted`)
  }
  assertSameJSON(left.platform, right.platform, `${label} platform drifted`)
}

function assertUnique(values, label) {
  assert(new Set(values).size === values.length, `${label} are duplicated`)
}

function assertPlainObject(value, label) {
  assert(value !== null && typeof value === 'object' && !Array.isArray(value) &&
    Object.getPrototypeOf(value) === Object.prototype, `${label} must be a plain object`)
}

function assertExactKeys(value, expected, label) {
  const actual = Object.keys(value).sort()
  const wanted = [...expected].sort()
  assert(actual.length === wanted.length && actual.every((key, index) => key === wanted[index]),
    `${label} fields drifted`)
}

function assertSameJSON(actual, expected, message) {
  assert(canonicalJSONStringify(actual) === canonicalJSONStringify(expected), message)
}

function clone(value) { return JSON.parse(JSON.stringify(value)) }
function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function assert(condition, message) { if (!condition) throw new Error(message) }
