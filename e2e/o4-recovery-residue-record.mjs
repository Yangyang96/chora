import { createHash, randomBytes } from 'node:crypto'
import { spawn } from 'node:child_process'
import { constants as fsConstants } from 'node:fs'
import {
  chmod, lstat, mkdir, open, readFile, readdir, realpath, stat,
} from 'node:fs/promises'
import { basename, dirname, isAbsolute, join, normalize, relative, resolve } from 'node:path'

import {
  appendHandoffReceipt,
  canonicalJSONStringify,
  validateCandidateTuple,
} from './o4-candidate-install-orchestrator.mjs'
import {
  cleanupOwnedPath,
  ownedPathIdentity,
  validateOwnedPathCapability,
} from './o4-owned-path-cleanup.mjs'

export const RECOVERY_RESIDUE_RECEIPT_SCHEMA = 'chora.m1-o4-recovery-residue-raw-receipt.v1'
export const RECOVERY_RESIDUE_MANIFEST_SCHEMA = 'chora.m1-o4-recovery-residue-manifest.v1'
export const RECOVERY_RESIDUE_PHASE_D_RECORD_SCHEMA = 'chora.m1-o4-recovery-residue-phase-d-record.v1'
export const RECOVERY_RESIDUE_RECORD_SCHEMA = 'chora.m1-o4-recovery-residue-record.v1'
export const DISCLOSURE_SOURCE_MANIFEST_SCHEMA = 'chora.m1-o4-disclosure-source-manifest.v1'
export const DISCLOSURE_EXPORTER_PROTOCOL = 'chora.m1-o4-readonly-disclosure-exporter.v1'
export const DISCLOSURE_EXPORTER_REQUEST_SCHEMA = 'chora.m1-o4-disclosure-export-request.v2'
export const DISCLOSURE_EXPORTER_ACK_SCHEMA = 'chora.m1-o4-disclosure-export-ack.v1'
export const RECOVERY_RESIDUE_RESOURCE_SCHEMA = 'chora.m1-o4-recovery-residue-resource-ledger.v1'
export const RECOVERY_RESIDUE_GENERATION_SCHEMA = 'chora.m1-o4-recovery-generation-reference.v1'
export const RECOVERY_RESIDUE_SERVICE_CONTROL_SCHEMA = 'chora.m1-o4-service-control.v1'
export const RECOVERY_RESIDUE_OCR_PROTOCOL = 'chora.m1-o4-system-managed-ocr.v2'
export const SCREENSHOT_METADATA_SCHEMA = 'chora.m1-o4-screenshot-metadata-scan.v1'
export const SCREENSHOT_OCR_SCHEMA = 'chora.m1-o4-system-managed-ocr-record.v2'
export const OCR_ENGINE_BINDING_SCHEMA = 'chora.m1-o4-system-managed-ocr-engine-binding.v1'

export const RECOVERY_RESIDUE_SCENARIOS = Object.freeze([
  Object.freeze({ sequence: 1, name: 'failure', terminalStatus: 'failed', attemptState: 'failed', terminalReason: 'runtime_exit_nonzero' }),
  Object.freeze({ sequence: 2, name: 'cancel', terminalStatus: 'canceled', attemptState: 'canceled', terminalReason: 'user_cancelled' }),
  Object.freeze({ sequence: 3, name: 'timeout', terminalStatus: 'timed_out', attemptState: 'failed', terminalReason: 'attempt_timeout' }),
  Object.freeze({ sequence: 4, name: 'retry', terminalStatus: 'succeeded', attemptState: 'output_submitted', terminalReason: null }),
  Object.freeze({ sequence: 5, name: 'restart', terminalStatus: 'succeeded', attemptState: 'output_submitted', terminalReason: null }),
  Object.freeze({ sequence: 6, name: 'orphan', terminalStatus: 'recovered', attemptState: 'output_submitted', terminalReason: null }),
  Object.freeze({ sequence: 7, name: 'reopen', terminalStatus: 'succeeded', attemptState: 'output_submitted', terminalReason: null }),
  Object.freeze({ sequence: 8, name: 'apply', terminalStatus: 'succeeded', attemptState: 'output_submitted', terminalReason: null }),
])

export const RECOVERY_RESIDUE_RECEIPT_NAMES = Object.freeze(
  RECOVERY_RESIDUE_SCENARIOS.map(({ sequence, name }) => `${String(sequence).padStart(2, '0')}-${name}.json`),
)
export const RECOVERY_RESIDUE_RESOURCE_NAMES = Object.freeze(
  RECOVERY_RESIDUE_SCENARIOS.map(({ sequence, name }) => `${String(sequence).padStart(2, '0')}-${name}-resources.json`),
)
export const RECOVERY_RESIDUE_SCREENSHOT_NAMES = Object.freeze(
  RECOVERY_RESIDUE_SCENARIOS.map(({ sequence, name }) => `${String(sequence).padStart(2, '0')}-${name}.png`),
)
export const DISCLOSURE_SOURCE_ARTIFACTS = Object.freeze([
  Object.freeze({ name: 'execution.log', kind: 'log', sourceKind: 'apply_attempt_stdout_stderr', maxBytes: 16 * 1024 * 1024 }),
  Object.freeze({ name: 'state.db', kind: 'database', sourceKind: 'sqlite_online_backup', maxBytes: 64 * 1024 * 1024 }),
  Object.freeze({ name: 'change.patch', kind: 'patch', sourceKind: 'accepted_applied_patch', maxBytes: 16 * 1024 * 1024 }),
  Object.freeze({ name: 'payload.json', kind: 'json', sourceKind: 'apply_response_body', maxBytes: 4 * 1024 * 1024 }),
])

const digestRE = /^[0-9a-f]{64}$/
const imageDigestRE = /^sha256:[0-9a-f]{64}$/
const opaqueInstallRE = /^(?:ins|gen)_[A-Za-z0-9_-]{24,80}$/
const environmentRE = /^env_[A-Za-z0-9_-]{24,80}$/
const uuidV7 = '[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}'
const productIDs = Object.freeze({
  taskId: new RegExp(`^task_${uuidV7}$`),
  runId: new RegExp(`^run_${uuidV7}$`),
  attemptId: new RegExp(`^attempt_${uuidV7}$`),
  workspaceId: /^wsp_[A-Za-z0-9_-]{24,80}$/,
  targetId: /^tgt_[A-Za-z0-9_-]{24,80}$/,
})
const zeroResidueKeys = Object.freeze([
  'ownedContainers', 'ownedNetworks', 'ownedVolumes', 'ownedConfigs', 'ownedWorkspaces',
  'ownedProcessGroups', 'activeReferences', 'recoverableReferences',
])
const sourceRangeKeys = Object.freeze(['startSequence', 'endSequence', 'sliceDigest'])
const fileIndexKeys = Object.freeze(['file', 'rawSha256', 'semanticDigest'])
const maxJSON = 4 * 1024 * 1024
const maxPNG = 16 * 1024 * 1024
const pngSignature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])
const safeReadFlags = fsConstants.O_RDONLY | (fsConstants.O_NOFOLLOW ?? 0) |
  (fsConstants.O_NONBLOCK ?? 0) | (fsConstants.O_NOCTTY ?? 0)

const receiptKeys = Object.freeze([
  'schemaVersion', 'status', 'sequence', 'name', 'environmentId', 'installId', 'generationId',
  'bindingDigest', 'tupleIdentity', 'taskId', 'runId', 'attemptId', 'workspaceId', 'targetId',
  'attemptState', 'terminalReason', 'terminalStatus', 'publicEntry', 'cleanupComplete',
  'patchState', 'checkState', 'reviewState', 'sourceA3', 'verifierSource', 'residue',
  'scenarioProof', 'publicActionDigest', 'publicObservationDigest', 'completedAt', 'receiptDigest',
])

const manifestKeys = Object.freeze([
  'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'bindingDigest',
  'tupleIdentity', 'candidateTuple', 'phaseCReceipt', 'authority', 'authorizedRestart',
  'scenarioReceipts', 'sourceA3', 'verifier', 'authorityConsumptions', 'resourceLedgers',
  'generationReferences', 'publicResidue', 'serviceControl', 'disclosureSources', 'screenshots', 'sharedImages',
  'aggregateDigest',
])

const phaseDRecordKeys = Object.freeze([
  'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'bindingDigest',
  'tupleIdentity', 'scenarios', 'sharedImages', 'imageReuseFormed', 'finalRecoveryFormed',
  'recordDigest',
])

const finalRecordKeys = Object.freeze([
  'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'bindingDigest',
  'imageReuseDigest', 'scenarios', 'sharedImages', 'digest',
])

const disclosureSourceManifestKeys = Object.freeze([
  'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'bindingDigest',
  'tupleIdentity', 'applyBinding', 'exporter', 'artifacts', 'screenshot', 'manifestDigest',
])
const disclosureApplyBindingKeys = Object.freeze([
  'taskId', 'runId', 'attemptId', 'workspaceId', 'targetId', 'publicObservationDigest',
  'patchDigest', 'payloadSha256',
])
const disclosureExporterKeys = Object.freeze([
  'protocol', 'runnerModuleSha256', 'requestSha256', 'ackSha256', 'networkDisabled',
])
const disclosureArtifactKeys = Object.freeze(['name', 'kind', 'sourceKind', 'bytes', 'rawSha256'])

const manifestIndexNames = Object.freeze({
  scenarioReceipts: RECOVERY_RESIDUE_RECEIPT_NAMES,
  authorityConsumptions: ['failure.json', 'timeout.json'],
  resourceLedgers: RECOVERY_RESIDUE_RESOURCE_NAMES,
  generationReferences: RECOVERY_RESIDUE_SCENARIOS.map(({ sequence, name }) => `${String(sequence).padStart(2, '0')}-${name}-generation.json`),
})

/**
 * Create the sole owner-private phase-D publication tree after proving that every
 * input is a canonical absolute path, no input is linked, and no input/output
 * path overlaps. The function deliberately creates no publishable JSON file.
 */
export async function prepareRecoveryResidueEvidencePaths(input) {
  assertPlainObject(input, 'recovery/residue path input')
  assertExactKeys(input, [
    'tupleFile', 'phaseCReceiptFile', 'authorityFile', 'authorizedRestartReceiptFile',
    'orphanRestartReceiptFile',
    'sourceA3LedgerFile', 'verifierLedgerFile', 'authorityConsumptionDirectory',
    'supervisorResourceDirectory', 'generationReferenceDirectory', 'publicResidueFile',
    'serviceControlFile', 'screenshotDirectory', 'screenshotMetadataDirectory',
    'screenshotOCRDirectory', 'serviceControllerExecutable', 'serviceControllerSha256',
    'ocrExecutable', 'ocrExecutableSha256', 'ocrEngineBindingFile', 'ocrEngineBindingSha256',
    'ocrSandboxExecutable', 'ocrSandboxExecutableSha256', 'ocrSandboxProfileFile',
    'ocrSandboxProfileSha256', 'ocrLanguage', 'b2RunnerModuleFile', 'b2RunnerModuleSha256',
    'outputDirectory',
  ], 'recovery/residue path input')

  const fileInputs = [
    input.tupleFile, input.phaseCReceiptFile, input.authorityFile,
    input.authorizedRestartReceiptFile, input.orphanRestartReceiptFile,
    input.sourceA3LedgerFile, input.publicResidueFile,
    input.serviceControlFile, input.serviceControllerExecutable, input.ocrExecutable,
    input.ocrEngineBindingFile, input.ocrSandboxExecutable, input.ocrSandboxProfileFile,
    input.b2RunnerModuleFile,
  ]
  if (input.verifierLedgerFile !== null) fileInputs.push(input.verifierLedgerFile)
  const directoryInputs = [
    input.authorityConsumptionDirectory, input.supervisorResourceDirectory,
    input.generationReferenceDirectory, input.screenshotDirectory,
    input.screenshotMetadataDirectory, input.screenshotOCRDirectory,
  ]
  for (const value of [...fileInputs, ...directoryInputs, input.outputDirectory]) {
    exactAbsolutePath(value, 'recovery/residue path')
  }
  assertPairwiseNonOverlapping([...fileInputs, ...directoryInputs, input.outputDirectory])
  const identities = []
  const largePinned = new Map([
    [input.serviceControllerExecutable, 64 * 1024 * 1024],
    [input.ocrExecutable, 64 * 1024 * 1024],
    [input.ocrEngineBindingFile, maxJSON],
    [input.ocrSandboxProfileFile, maxJSON],
    [input.b2RunnerModuleFile, 4 * 1024 * 1024],
  ])
  for (const path of fileInputs) {
    if (path !== input.ocrSandboxExecutable) identities.push(await assertExactRegularFile(path,
      'recovery/residue input', largePinned.get(path) ?? maxJSON))
  }
  for (const path of directoryInputs) identities.push(await assertOwnerDirectory(path))
  assertUniqueFileIdentities(identities, 'recovery/residue inputs')
  await validatePinnedExecutable(input.serviceControllerExecutable, input.serviceControllerSha256,
    'service controller')
  await validatePinnedExecutable(input.ocrExecutable, input.ocrExecutableSha256,
    'system-managed OCR executable')
  identities.push(await validatePinnedSystemExecutable(input.ocrSandboxExecutable,
    input.ocrSandboxExecutableSha256, 'OCR sandbox executable'))
  assertUniqueFileIdentities(identities, 'recovery/residue inputs including OCR sandbox')
  const engineBytes = await readExactFile(input.ocrEngineBindingFile,
    'system-managed OCR engine binding', maxJSON, 0o400)
  assert(sha256(engineBytes) === input.ocrEngineBindingSha256,
    'system-managed OCR engine binding digest drifted')
  validateOCREngineBinding(parseJSON(engineBytes, 'system-managed OCR engine binding'), input)
  const profileBytes = await readExactFile(input.ocrSandboxProfileFile,
    'OCR sandbox profile', maxJSON, 0o400)
  assert(sha256(profileBytes) === input.ocrSandboxProfileSha256,
    'OCR sandbox profile digest drifted')
  validateOCRSandboxProfile(profileBytes)
  assert(input.ocrLanguage === 'eng', 'system-managed OCR language drifted')
  const runnerBytes = await readExactFile(input.b2RunnerModuleFile, 'B2 disclosure Runner module',
    4 * 1024 * 1024, 0o400)
  assertDigest(input.b2RunnerModuleSha256, 'B2 disclosure Runner module expected digest')
  assert(sha256(runnerBytes) === input.b2RunnerModuleSha256,
    'B2 disclosure Runner module digest drifted')
  await assertNoSymlinkAncestors(input.outputDirectory)
  await assertAbsent(input.outputDirectory, 'recovery/residue output directory')
  await assertOwnerDirectory(dirname(input.outputDirectory))
  await mkdir(input.outputDirectory, { recursive: false, mode: 0o700 })
  const receiptDirectory = join(input.outputDirectory, 'receipts')
  const disclosureSourceDirectory = join(input.outputDirectory, 'disclosure-sources')
  const manifestFile = join(input.outputDirectory, 'manifest.json')
  const recordFile = join(input.outputDirectory, 'record.json')
  const disclosureExporterRequestFile = join(input.outputDirectory, 'disclosure-export-request.json')
  const disclosureExporterAckFile = join(input.outputDirectory, 'disclosure-export-ack.json')
  await mkdir(receiptDirectory, { recursive: false, mode: 0o700 })
  await mkdir(disclosureSourceDirectory, { recursive: false, mode: 0o700 })
  await assertOwnerDirectory(input.outputDirectory)
  await assertOwnerDirectory(receiptDirectory)
  await assertOwnerDirectory(disclosureSourceDirectory)
  return Object.freeze({
    ...input, receiptDirectory, disclosureSourceDirectory, manifestFile, recordFile,
    disclosureExporterRequestFile, disclosureExporterAckFile,
    disclosureExporterCoreAckFile: join(input.outputDirectory, '.disclosure-export-core.json'),
    disclosureSourceManifestFile: join(disclosureSourceDirectory, 'manifest.json'),
    executionLogFile: join(disclosureSourceDirectory, 'execution.log'),
    stateDatabaseFile: join(disclosureSourceDirectory, 'state.db'),
    changePatchFile: join(disclosureSourceDirectory, 'change.patch'),
    responsePayloadFile: join(disclosureSourceDirectory, 'payload.json'),
  })
}

/**
 * Execute the one externally supplied B2 disclosure-export boundary. The Runner
 * owns product access and process supervision; this harness owns the immutable
 * request, bounded timeout, byte/schema authentication, and fail-closed cleanup.
 */
export async function runPinnedDisclosureExporter({
  runner, paths, applyBinding, responseBody, sources, repositoryCapture, timeoutMs = 30_000,
}) {
  repositoryCapture = validateOwnedPathCapability(
    repositoryCapture, 'disclosure exporter owned-path capability')
  assertPlainObject(runner, 'B2 disclosure Runner')
  assert(typeof runner.exportApplyDisclosureSources === 'function' &&
    typeof runner.abortApplyDisclosureExporter === 'function',
  'B2 disclosure Runner methods are unavailable')
  assertPreparedDisclosurePaths(paths)
  await assertPinnedDisclosureRunnerModule(paths)
  await assertOwnerDirectory(paths.outputDirectory)
  await assertOwnerDirectory(paths.disclosureSourceDirectory)
  await assertNoSymlinkAncestors(paths.outputDirectory)
  await assertExactDirectoryEntries(paths.outputDirectory, ['receipts', 'disclosure-sources'],
    'pre-export phase-D output directory')
  await assertExactDirectoryEntries(paths.disclosureSourceDirectory, [],
    'pre-export disclosure source directory')
  for (const path of [
    paths.disclosureExporterRequestFile, paths.disclosureExporterAckFile,
    paths.disclosureExporterCoreAckFile,
    paths.disclosureSourceManifestFile, paths.executionLogFile, paths.stateDatabaseFile,
    paths.changePatchFile, paths.responsePayloadFile, paths.manifestFile, paths.recordFile,
  ]) await assertAbsent(path, 'pre-export disclosure output')
  validateDisclosureApplyBinding(applyBinding)
  validateDisclosureSources(sources)
  assert(Buffer.isBuffer(responseBody) && responseBody.length > 0 && responseBody.length <= 4 * 1024 * 1024,
    'Apply Response.body bytes are missing or oversized')
  assert(sha256(responseBody) === applyBinding.payloadSha256,
    'Apply Response.body bytes drifted from the public payload digest')
  assert(Number.isSafeInteger(timeoutMs) && timeoutMs >= 50 && timeoutMs <= 120_000,
    'disclosure exporter timeout is invalid')
  const requestDraft = {
    schemaVersion: DISCLOSURE_EXPORTER_REQUEST_SCHEMA, status: 'authorized',
    protocol: DISCLOSURE_EXPORTER_PROTOCOL,
    runnerModuleSha256: paths.b2RunnerModuleSha256, networkDisabled: true,
    applyBinding: clone(applyBinding),
    sources: clone(sources),
    outputs: {
      executionLogFile: paths.executionLogFile, stateDatabaseFile: paths.stateDatabaseFile,
      changePatchFile: paths.changePatchFile, responsePayloadFile: paths.responsePayloadFile,
      coreAcknowledgementFile: paths.disclosureExporterCoreAckFile,
      acknowledgementFile: paths.disclosureExporterAckFile,
    },
    limits: { timeoutMs, maxTotalBytes: DISCLOSURE_SOURCE_ARTIFACTS.reduce((sum, value) => sum + value.maxBytes, 0) },
  }
  const request = { ...requestDraft, requestDigest: sha256(canonicalJSONStringify(requestDraft)) }
  validateDisclosureExporterRequest(request, paths, applyBinding, sources)
  const requestIdentity = await writeExclusiveOwnerReadonlyJSON(request,
    paths.disclosureExporterRequestFile, 'disclosure exporter request', repositoryCapture)
  const requestSha256 = sha256(await readExactFile(paths.disclosureExporterRequestFile,
    'disclosure exporter request', maxJSON, 0o400))
  let timer
  try {
    const result = await Promise.race([
      Promise.resolve().then(() => runner.exportApplyDisclosureSources({
        requestFile: paths.disclosureExporterRequestFile,
        requestSha256,
        responseBody: Buffer.from(responseBody),
      })),
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error('disclosure exporter timed out')), timeoutMs)
      }),
    ])
    clearTimeout(timer)
    assertPlainObject(result, 'disclosure exporter result')
    assertExactKeys(result, ['status', 'requestSha256'], 'disclosure exporter result')
    assert(result.status === 'completed' && result.requestSha256 === requestSha256,
      'disclosure exporter completion acknowledgement drifted')
    await preflightDisclosureArtifactBounds(paths)
    const ackInput = await semanticJSONIndex(paths.disclosureExporterAckFile,
      'disclosure exporter acknowledgement', (value) => validateDisclosureExporterAck(value, {
        paths, applyBinding, requestSha256,
      }))
    await validateDisclosureArtifactFiles(paths, applyBinding, ackInput.value)
    await assertPinnedDisclosureRunnerModule(paths)
    return deepFreeze({ requestSha256, ackSha256: ackInput.index.rawSha256,
      ackDigest: ackInput.value.ackDigest })
  } catch (error) {
    clearTimeout(timer)
    let abortError
    try {
      const aborted = await boundedRunnerAbort(runner, requestSha256)
      assertPlainObject(aborted, 'disclosure exporter abort result')
      assertExactKeys(aborted, ['status', 'requestSha256', 'processGroupDead', 'descendantsDead'],
        'disclosure exporter abort result')
      assert(aborted.status === 'aborted' && aborted.requestSha256 === requestSha256 &&
        aborted.processGroupDead === true && aborted.descendantsDead === true,
      'disclosure exporter abort did not prove full process-group death')
    } catch (abortFailure) {
      abortError = abortFailure
    }
    const cleanupErrors = []
    try {
      await cleanupOwnedPath({ capability: repositoryCapture,
        path: paths.disclosureExporterRequestFile, identity: requestIdentity,
        disposition: 'regular_file' })
    } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 300))
    try { await assertDisclosureExporterOutputsAbsent(paths) }
    catch (cleanupError) { cleanupErrors.push(cleanupError) }
    if (abortError) cleanupErrors.push(abortError)
    if (cleanupErrors.length > 0) throw new AggregateError(
      [error, ...cleanupErrors], 'disclosure exporter and fail-closed cleanup failed', { cause: error })
    throw error
  }
}

export async function buildDisclosureSourceManifest({ paths, identity, applyBinding }) {
  assertPreparedDisclosurePaths(paths)
  await assertPinnedDisclosureRunnerModule(paths)
  validateSharedIdentity(identity, 'disclosure source identity')
  validateDisclosureApplyBinding(applyBinding)
  const requestBytes = await readExactFile(paths.disclosureExporterRequestFile,
    'disclosure exporter request', maxJSON, 0o400)
  const requestSha256 = sha256(requestBytes)
  validateDisclosureExporterRequest(parseJSON(requestBytes, 'disclosure exporter request'), paths, applyBinding)
  const ackInput = await semanticJSONIndex(paths.disclosureExporterAckFile,
    'disclosure exporter acknowledgement', (value) => validateDisclosureExporterAck(value, {
      paths, applyBinding, requestSha256,
    }))
  const artifacts = await validateDisclosureArtifactFiles(paths, applyBinding, ackInput.value)
  const screenshot = await readApplyScreenshotIndex(paths, applyBinding)
  const draft = {
    schemaVersion: DISCLOSURE_SOURCE_MANIFEST_SCHEMA, status: 'complete', ...clone(identity),
    applyBinding: clone(applyBinding),
    exporter: {
      protocol: DISCLOSURE_EXPORTER_PROTOCOL, runnerModuleSha256: paths.b2RunnerModuleSha256,
      requestSha256, ackSha256: ackInput.index.rawSha256, networkDisabled: true,
    },
    artifacts, screenshot,
  }
  return formDisclosureSourceManifest(draft)
}

export function formDisclosureSourceManifest(draft) {
  assertPlainObject(draft, 'disclosure source manifest draft')
  assertExactKeys(draft, disclosureSourceManifestKeys.filter((key) => key !== 'manifestDigest'),
    'disclosure source manifest draft')
  const value = { ...clone(draft), manifestDigest: sha256(canonicalJSONStringify(draft)) }
  return deepFreeze(assertDisclosureSourceManifest(value))
}

export function assertDisclosureSourceManifest(value) {
  assertPlainObject(value, 'disclosure source manifest')
  assertExactKeys(value, disclosureSourceManifestKeys, 'disclosure source manifest')
  assert(value.schemaVersion === DISCLOSURE_SOURCE_MANIFEST_SCHEMA && value.status === 'complete',
    'disclosure source manifest schema/status drifted')
  validateSharedIdentity(value, 'disclosure source manifest')
  validateDisclosureApplyBinding(value.applyBinding)
  assertPlainObject(value.exporter, 'disclosure source exporter')
  assertExactKeys(value.exporter, disclosureExporterKeys, 'disclosure source exporter')
  assert(value.exporter.protocol === DISCLOSURE_EXPORTER_PROTOCOL && value.exporter.networkDisabled === true,
    'disclosure source exporter protocol/network drifted')
  for (const field of ['runnerModuleSha256', 'requestSha256', 'ackSha256']) {
    assertDigest(value.exporter[field], `disclosure source exporter ${field}`)
  }
  validateDisclosureArtifactIndexes(value.artifacts, value.applyBinding)
  validateApplyScreenshotTriple(value.screenshot, value.applyBinding.publicObservationDigest)
  assertDigest(value.manifestDigest, 'disclosure source manifest digest')
  const { manifestDigest, ...safe } = value
  assert(manifestDigest === sha256(canonicalJSONStringify(safe)),
    'disclosure source manifest digest drifted')
  assertSafeEvidenceValue(value, 'disclosure source manifest')
  return value
}

export async function persistDisclosureSourceManifest(value, outputPath, dependencies = undefined) {
  value = assertDisclosureSourceManifest(value)
  exactAbsolutePath(outputPath, 'disclosure source manifest output')
  assert(basename(outputPath) === 'manifest.json', 'disclosure source manifest filename drifted')
  await assertExactDirectoryEntries(dirname(outputPath), DISCLOSURE_SOURCE_ARTIFACTS.map(({ name }) => name),
    'pre-manifest disclosure source directory')
  await writeExclusiveOwnerReadonlyJSON(value, outputPath, 'disclosure source manifest',
    dependencies?.repositoryCapture)
  await assertExactDirectoryEntries(dirname(outputPath),
    [...DISCLOSURE_SOURCE_ARTIFACTS.map(({ name }) => name), 'manifest.json'],
    'disclosure source directory')
  return fileIndexFor(outputPath, value.manifestDigest)
}

function assertPreparedDisclosurePaths(paths) {
  assertPlainObject(paths, 'prepared disclosure paths')
  for (const key of [
    'b2RunnerModuleFile', 'outputDirectory', 'manifestFile', 'recordFile',
    'disclosureSourceDirectory', 'disclosureExporterRequestFile',
    'disclosureExporterAckFile', 'disclosureExporterCoreAckFile',
    'disclosureSourceManifestFile', 'executionLogFile',
    'stateDatabaseFile', 'changePatchFile', 'responsePayloadFile', 'screenshotDirectory',
    'screenshotMetadataDirectory', 'screenshotOCRDirectory',
  ]) exactAbsolutePath(paths[key], `prepared disclosure ${key}`)
  assertDigest(paths.b2RunnerModuleSha256, 'prepared disclosure Runner module digest')
  assert(dirname(paths.disclosureSourceManifestFile) === paths.disclosureSourceDirectory &&
    dirname(paths.disclosureSourceDirectory) === paths.outputDirectory &&
    dirname(paths.disclosureExporterRequestFile) === paths.outputDirectory &&
    dirname(paths.disclosureExporterAckFile) === paths.outputDirectory &&
    dirname(paths.disclosureExporterCoreAckFile) === paths.outputDirectory &&
    paths.executionLogFile === join(paths.disclosureSourceDirectory, 'execution.log') &&
    paths.stateDatabaseFile === join(paths.disclosureSourceDirectory, 'state.db') &&
    paths.changePatchFile === join(paths.disclosureSourceDirectory, 'change.patch') &&
    paths.responsePayloadFile === join(paths.disclosureSourceDirectory, 'payload.json'),
  'prepared disclosure artifact layout drifted')
}

function validateDisclosureApplyBinding(value) {
  assertPlainObject(value, 'disclosure Apply binding')
  assertExactKeys(value, disclosureApplyBindingKeys, 'disclosure Apply binding')
  validateProductIDs(value, 'disclosure Apply binding')
  for (const field of ['publicObservationDigest', 'patchDigest', 'payloadSha256']) {
    assertDigest(value[field], `disclosure Apply binding ${field}`)
  }
  return value
}

function validateDisclosureExporterRequest(value, paths, applyBinding, sources = undefined) {
  assertPlainObject(value, 'disclosure exporter request')
  assertExactKeys(value, [
    'schemaVersion', 'status', 'protocol', 'runnerModuleSha256', 'networkDisabled',
    'applyBinding', 'sources', 'outputs', 'limits', 'requestDigest',
  ], 'disclosure exporter request')
  assert(value.schemaVersion === DISCLOSURE_EXPORTER_REQUEST_SCHEMA && value.status === 'authorized' &&
    value.protocol === DISCLOSURE_EXPORTER_PROTOCOL && value.networkDisabled === true &&
    value.runnerModuleSha256 === paths.b2RunnerModuleSha256,
  'disclosure exporter request authority/pin drifted')
  validateDisclosureApplyBinding(value.applyBinding)
  assertSameJSON(value.applyBinding, applyBinding, 'disclosure exporter request Apply binding drifted')
  validateDisclosureSources(value.sources)
  if (sources !== undefined) assertSameJSON(value.sources, sources,
    'disclosure exporter source path drifted')
  assertPlainObject(value.outputs, 'disclosure exporter outputs')
  assertExactKeys(value.outputs, [
    'executionLogFile', 'stateDatabaseFile', 'changePatchFile', 'responsePayloadFile',
    'coreAcknowledgementFile', 'acknowledgementFile',
  ], 'disclosure exporter outputs')
  assert(value.outputs.executionLogFile === paths.executionLogFile &&
    value.outputs.stateDatabaseFile === paths.stateDatabaseFile &&
    value.outputs.changePatchFile === paths.changePatchFile &&
    value.outputs.responsePayloadFile === paths.responsePayloadFile &&
    value.outputs.coreAcknowledgementFile === paths.disclosureExporterCoreAckFile &&
    value.outputs.acknowledgementFile === paths.disclosureExporterAckFile,
  'disclosure exporter output path drifted')
  assertPlainObject(value.limits, 'disclosure exporter limits')
  assertExactKeys(value.limits, ['timeoutMs', 'maxTotalBytes'], 'disclosure exporter limits')
  assert(Number.isSafeInteger(value.limits.timeoutMs) && value.limits.timeoutMs >= 50 &&
    value.limits.timeoutMs <= 120_000 && Number.isSafeInteger(value.limits.maxTotalBytes) &&
    value.limits.maxTotalBytes === DISCLOSURE_SOURCE_ARTIFACTS.reduce((sum, item) => sum + item.maxBytes, 0),
  'disclosure exporter bounds drifted')
  assertDigest(value.requestDigest, 'disclosure exporter request digest')
  const { requestDigest, ...safe } = value
  assert(requestDigest === sha256(canonicalJSONStringify(safe)),
    'disclosure exporter request digest drifted')
  return value
}

function validateDisclosureSources(value) {
  assertPlainObject(value, 'disclosure exporter sources')
  assertExactKeys(value, ['executionLogFile', 'stateDatabaseFile', 'changePatchFile'],
    'disclosure exporter sources')
  const paths = Object.values(value)
  for (const path of paths) exactAbsolutePath(path, 'disclosure exporter source path')
  assert(new Set(paths).size === paths.length, 'disclosure exporter sources overlap')
}

function validateDisclosureExporterAck(value, expected) {
  assertPlainObject(value, 'disclosure exporter acknowledgement')
  assertExactKeys(value, [
    'schemaVersion', 'status', 'protocol', 'runnerModuleSha256', 'requestSha256',
    'networkDisabled', 'applyBinding', 'artifacts', 'databaseSnapshot', 'processGroupDead',
    'descendantsDead', 'completedAt', 'ackDigest',
  ], 'disclosure exporter acknowledgement')
  assert(value.schemaVersion === DISCLOSURE_EXPORTER_ACK_SCHEMA && value.status === 'completed' &&
    value.protocol === DISCLOSURE_EXPORTER_PROTOCOL && value.networkDisabled === true &&
    value.runnerModuleSha256 === expected.paths.b2RunnerModuleSha256 &&
    value.requestSha256 === expected.requestSha256 && value.processGroupDead === true &&
    value.descendantsDead === true,
  'disclosure exporter acknowledgement authority/pin/death proof drifted')
  validateDisclosureApplyBinding(value.applyBinding)
  assertSameJSON(value.applyBinding, expected.applyBinding,
    'disclosure exporter acknowledgement Apply binding drifted')
  validateDisclosureArtifactIndexes(value.artifacts, value.applyBinding)
  assertPlainObject(value.databaseSnapshot, 'disclosure exporter database snapshot')
  assertExactKeys(value.databaseSnapshot, [
    'method', 'sourceIdentitySha256', 'backupIdentitySha256',
    'sourceAndBackupInodesDistinct', 'walConsistent',
  ], 'disclosure exporter database snapshot')
  assert(value.databaseSnapshot.method === 'sqlite_online_backup' &&
    value.databaseSnapshot.sourceAndBackupInodesDistinct === true &&
    value.databaseSnapshot.walConsistent === true &&
    value.databaseSnapshot.sourceIdentitySha256 !== value.databaseSnapshot.backupIdentitySha256,
  'disclosure exporter did not prove a distinct SQLite online backup')
  assertDigest(value.databaseSnapshot.sourceIdentitySha256,
    'disclosure exporter source database identity')
  assertDigest(value.databaseSnapshot.backupIdentitySha256,
    'disclosure exporter backup database identity')
  assertTimestamp(value.completedAt, 'disclosure exporter acknowledgement completion')
  assertDigest(value.ackDigest, 'disclosure exporter acknowledgement digest')
  const { ackDigest, ...safe } = value
  assert(value.ackDigest === sha256(canonicalJSONStringify(safe)),
    'disclosure exporter acknowledgement digest drifted')
  return value
}

async function validateDisclosureArtifactFiles(paths, applyBinding, ack, allowManifest = false) {
  const expectedEntries = DISCLOSURE_SOURCE_ARTIFACTS.map(({ name }) => name)
  if (allowManifest) expectedEntries.push('manifest.json')
  await assertExactDirectoryEntries(paths.disclosureSourceDirectory, expectedEntries,
    'disclosure source directory')
  const result = []
  for (let index = 0; index < DISCLOSURE_SOURCE_ARTIFACTS.length; index++) {
    const contract = DISCLOSURE_SOURCE_ARTIFACTS[index]
    const file = disclosureArtifactPath(paths, contract.name)
    const bytes = await readExactFile(file, `disclosure source ${contract.name}`, contract.maxBytes, 0o400)
    const artifact = {
      name: contract.name, kind: contract.kind, sourceKind: contract.sourceKind,
      bytes: bytes.length, rawSha256: sha256(bytes),
    }
    assertSameJSON(artifact, ack.artifacts[index],
      `disclosure source ${contract.name} acknowledgement drifted`)
    result.push(artifact)
    if (contract.name === 'state.db') {
      assert(bytes.subarray(0, 16).equals(Buffer.from('SQLite format 3\0', 'binary')),
        'disclosure state.db is not a SQLite online backup')
      const info = await lstat(file)
      assert(ack.databaseSnapshot.backupIdentitySha256 === filesystemIdentityDigest(info),
        'disclosure state.db backup inode identity drifted')
    } else if (contract.name === 'change.patch') {
      assert(artifact.rawSha256 === applyBinding.patchDigest,
        'disclosure change.patch drifted from the public Apply patch digest')
    } else if (contract.name === 'payload.json') {
      assert(artifact.rawSha256 === applyBinding.payloadSha256,
        'disclosure payload.json drifted from exact Apply Response.body bytes')
      parseJSON(bytes, 'disclosure Apply response payload')
    }
  }
  return result
}

async function preflightDisclosureArtifactBounds(paths) {
  for (const contract of DISCLOSURE_SOURCE_ARTIFACTS) {
    const file = disclosureArtifactPath(paths, contract.name)
    const info = await lstat(file)
    assert(info.isFile() && !info.isSymbolicLink() && info.nlink === 1 && info.uid === process.getuid() &&
      info.size > 0 && info.size <= contract.maxBytes && (info.mode & 0o777) === 0o400,
    `disclosure source ${contract.name} is not one bounded owner-only non-linked 0400 file`)
  }
}

function validateDisclosureArtifactIndexes(value, applyBinding) {
  assert(Array.isArray(value) && value.length === DISCLOSURE_SOURCE_ARTIFACTS.length,
    'disclosure source artifact count drifted')
  for (let index = 0; index < DISCLOSURE_SOURCE_ARTIFACTS.length; index++) {
    const artifact = value[index]
    const contract = DISCLOSURE_SOURCE_ARTIFACTS[index]
    assertPlainObject(artifact, `disclosure source artifact ${index + 1}`)
    assertExactKeys(artifact, disclosureArtifactKeys, `disclosure source artifact ${index + 1}`)
    assert(artifact.name === contract.name && artifact.kind === contract.kind &&
      artifact.sourceKind === contract.sourceKind && Number.isSafeInteger(artifact.bytes) &&
      artifact.bytes > 0 && artifact.bytes <= contract.maxBytes,
    `disclosure source artifact ${index + 1} contract drifted`)
    assertDigest(artifact.rawSha256, `disclosure source artifact ${contract.name}`)
  }
  assert(value[2].rawSha256 === applyBinding.patchDigest,
    'disclosure source patch digest binding drifted')
  assert(value[3].rawSha256 === applyBinding.payloadSha256,
    'disclosure source payload digest binding drifted')
}

async function readApplyScreenshotIndex(paths, applyBinding) {
  const pngName = '08-apply.png'
  const pngFile = join(paths.screenshotDirectory, pngName)
  const pngBytes = await readExactFile(pngFile, 'Apply disclosure screenshot', maxPNG, 0o400)
  const metadata = await semanticJSONIndex(join(paths.screenshotMetadataDirectory, '08-apply-metadata.json'),
    'Apply disclosure screenshot metadata', (value) => validateScreenshotMetadata(value, pngBytes))
  const ocr = await semanticJSONIndex(join(paths.screenshotOCRDirectory, '08-apply-ocr.json'),
    'Apply disclosure screenshot OCR', (value) => validateScreenshotOCR(value, pngBytes))
  assert(ocr.value.recognizedText.includes('apply'),
    'Apply disclosure screenshot OCR does not bind the visible Apply scenario')
  const value = {
    png: { file: pngName, rawSha256: sha256(pngBytes), semanticDigest: applyBinding.publicObservationDigest },
    metadata: metadata.index, ocr: ocr.index,
  }
  validateApplyScreenshotTriple(value, applyBinding.publicObservationDigest)
  return value
}

function validateApplyScreenshotTriple(value, publicObservationDigest) {
  assertPlainObject(value, 'Apply disclosure screenshot index')
  assertExactKeys(value, ['png', 'metadata', 'ocr'], 'Apply disclosure screenshot index')
  validateSingleIndex(value.png, '08-apply.png', 'Apply disclosure screenshot PNG index')
  validateSingleIndex(value.metadata, '08-apply-metadata.json',
    'Apply disclosure screenshot metadata index')
  validateSingleIndex(value.ocr, '08-apply-ocr.json', 'Apply disclosure screenshot OCR index')
  assert(value.png.semanticDigest === publicObservationDigest,
    'Apply disclosure screenshot public observation binding drifted')
}

async function readDisclosureSourcesIndex(paths, applyReceipt, applyScreenshot) {
  await assertPinnedDisclosureRunnerModule(paths)
  await assertExactDirectoryEntries(paths.outputDirectory, [
    'receipts', 'disclosure-sources', 'disclosure-export-request.json',
    'disclosure-export-ack.json',
  ], 'pre-publication phase-D output directory')
  const input = await semanticJSONIndex(paths.disclosureSourceManifestFile,
    'disclosure source manifest', assertDisclosureSourceManifest)
  const value = input.value
  assertSameIdentity(value, applyReceipt, 'disclosure source/Apply receipt')
  assert(value.tupleIdentity === applyReceipt.tupleIdentity &&
    value.exporter.runnerModuleSha256 === paths.b2RunnerModuleSha256,
  'disclosure source tuple/Runner binding drifted')
  const expectedBinding = disclosureApplyBindingFromReceipt(applyReceipt)
  assertSameJSON(value.applyBinding, expectedBinding,
    'disclosure source Apply receipt binding drifted')
  assert(value.manifestDigest === applyReceipt.scenarioProof.disclosureSourceManifestDigest,
    'disclosure source manifest was not bound before the Apply receipt')
  const requestBytes = await readExactFile(paths.disclosureExporterRequestFile,
    'disclosure exporter request at D boundary', maxJSON, 0o400)
  assert(sha256(requestBytes) === value.exporter.requestSha256,
    'disclosure exporter request raw digest drifted')
  validateDisclosureExporterRequest(parseJSON(requestBytes, 'disclosure exporter request at D boundary'),
    paths, expectedBinding)
  const ack = await semanticJSONIndex(paths.disclosureExporterAckFile,
    'disclosure exporter acknowledgement at D boundary', (candidate) =>
      validateDisclosureExporterAck(candidate, {
        paths, applyBinding: expectedBinding, requestSha256: value.exporter.requestSha256,
      }))
  assert(ack.index.rawSha256 === value.exporter.ackSha256,
    'disclosure exporter acknowledgement raw digest drifted')
  const artifacts = await validateDisclosureArtifactFiles(paths, expectedBinding, ack.value, true)
  assertSameJSON(artifacts, value.artifacts, 'disclosure source artifact manifest drifted')
  assertSameJSON(value.screenshot, {
    png: applyScreenshot.png, metadata: applyScreenshot.metadata, ocr: applyScreenshot.ocr,
  }, 'disclosure source Apply screenshot drifted from D screenshot index')
  return {
    manifest: input.index,
    artifacts: artifacts.map((artifact) => ({
      file: artifact.name, rawSha256: artifact.rawSha256, semanticDigest: artifact.rawSha256,
    })),
    screenshot: clone(value.screenshot),
  }
}

function disclosureApplyBindingFromReceipt(receipt) {
  return {
    taskId: receipt.taskId, runId: receipt.runId, attemptId: receipt.attemptId,
    workspaceId: receipt.workspaceId, targetId: receipt.targetId,
    publicObservationDigest: receipt.publicObservationDigest,
    patchDigest: receipt.scenarioProof.patchDigest,
    payloadSha256: receipt.scenarioProof.payloadSha256,
  }
}

function validateDisclosureSourcesIndex(value) {
  assertPlainObject(value, 'recovery disclosure source index')
  assertExactKeys(value, ['manifest', 'artifacts', 'screenshot'], 'recovery disclosure source index')
  validateSingleIndex(value.manifest, 'manifest.json', 'recovery disclosure source manifest index')
  validateFileIndex(value.artifacts, DISCLOSURE_SOURCE_ARTIFACTS.map(({ name }) => name),
    'recovery disclosure artifact index')
  validateApplyScreenshotTriple(value.screenshot, value.screenshot.png.semanticDigest)
}

function disclosureArtifactPath(paths, name) {
  if (name === 'execution.log') return paths.executionLogFile
  if (name === 'state.db') return paths.stateDatabaseFile
  if (name === 'change.patch') return paths.changePatchFile
  if (name === 'payload.json') return paths.responsePayloadFile
  throw new Error('unknown disclosure artifact')
}

async function assertPinnedDisclosureRunnerModule(paths) {
  const bytes = await readExactFile(paths.b2RunnerModuleFile, 'pinned B2 disclosure Runner module',
    4 * 1024 * 1024, 0o400)
  assert(sha256(bytes) === paths.b2RunnerModuleSha256,
    'pinned B2 disclosure Runner module changed at the exporter boundary')
}

async function boundedRunnerAbort(runner, requestSha256) {
  let timer
  try {
    return await Promise.race([
      Promise.resolve().then(() => runner.abortApplyDisclosureExporter({ requestSha256 })),
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error('disclosure exporter abort timed out')), 2_000)
      }),
    ])
  } finally {
    clearTimeout(timer)
  }
}

async function assertDisclosureExporterOutputsAbsent(paths) {
  for (const path of [
    paths.disclosureSourceManifestFile, paths.executionLogFile, paths.stateDatabaseFile,
    paths.changePatchFile, paths.responsePayloadFile, paths.disclosureExporterAckFile,
    paths.disclosureExporterCoreAckFile, paths.disclosureExporterRequestFile,
    paths.manifestFile, paths.recordFile,
  ]) await assertAbsent(path, 'failed disclosure exporter output')
  await assertExactDirectoryEntries(paths.disclosureSourceDirectory, [],
    'failed disclosure source directory')
}

async function assertExactDirectoryEntries(path, expected, label) {
  await assertOwnerDirectory(path)
  const actual = (await readdir(path)).sort()
  const wanted = [...expected].sort()
  assert(actual.length === wanted.length && actual.every((name, index) => name === wanted[index]),
    `${label} entries drifted`)
}

function filesystemIdentityDigest(info) {
  return sha256(canonicalJSONStringify({ device: String(info.dev), inode: String(info.ino) }))
}

export function formRecoveryResidueScenarioReceipt(draft) {
  assertPlainObject(draft, 'recovery/residue receipt draft')
  assertExactKeys(draft, receiptKeys.filter((key) => key !== 'receiptDigest'),
    'recovery/residue receipt draft')
  const receipt = { ...clone(draft), receiptDigest: sha256(canonicalJSONStringify(draft)) }
  return deepFreeze(assertRecoveryResidueScenarioReceipt(receipt))
}

export function assertRecoveryResidueScenarioReceipt(receipt, expected) {
  assertPlainObject(receipt, 'recovery/residue receipt')
  assertExactKeys(receipt, receiptKeys, 'recovery/residue receipt')
  const contract = expected ?? RECOVERY_RESIDUE_SCENARIOS[receipt.sequence - 1]
  assert(contract && receipt.schemaVersion === RECOVERY_RESIDUE_RECEIPT_SCHEMA && receipt.status === 'passed' &&
    receipt.sequence === contract.sequence && receipt.name === contract.name,
  'recovery/residue receipt schema/status/order drifted')
  validateSharedIdentity(receipt, 'recovery/residue receipt')
  validateProductIDs(receipt, `recovery ${receipt.name}`)
  assert(receipt.attemptState === contract.attemptState && receipt.terminalReason === contract.terminalReason &&
    receipt.terminalStatus === contract.terminalStatus,
  `recovery ${receipt.name} terminal state/reason drifted`)
  assert(receipt.publicEntry === true && receipt.cleanupComplete === true,
    `recovery ${receipt.name} lacks public entry or cleanup proof`)
  validateScenarioStates(receipt, contract)
  validateSourceRange(receipt.sourceA3, `recovery ${receipt.name} A3 source`)
  if (receipt.verifierSource === null) {
    assert(['failure', 'cancel', 'timeout'].includes(receipt.name),
      `recovery ${receipt.name} forbids absent Verifier evidence`)
  } else {
    validateSourceRange(receipt.verifierSource, `recovery ${receipt.name} Verifier source`)
  }
  validateZeroResidue(receipt.residue, `recovery ${receipt.name} residue`)
  validateScenarioProof(receipt)
  assertDigest(receipt.publicActionDigest, `recovery ${receipt.name} public action digest`)
  assertDigest(receipt.publicObservationDigest, `recovery ${receipt.name} public observation digest`)
  assertTimestamp(receipt.completedAt, `recovery ${receipt.name} completion`)
  assertDigest(receipt.receiptDigest, `recovery ${receipt.name} receipt digest`)
  const { receiptDigest, ...safe } = receipt
  assert(receiptDigest === sha256(canonicalJSONStringify(safe)),
    `recovery ${receipt.name} receipt digest drifted`)
  assertSafeEvidenceValue(receipt, `recovery ${receipt.name} receipt`)
  return receipt
}

export async function collectRecoveryResidueScenario({ receipt, outputPath, repositoryCapture }) {
  const checked = assertRecoveryResidueScenarioReceipt(receipt)
  exactAbsolutePath(outputPath, 'recovery/residue receipt output')
  assert(basename(outputPath) === RECOVERY_RESIDUE_RECEIPT_NAMES[checked.sequence - 1],
    'recovery/residue receipt filename drifted')
  await writeExclusiveOwnerReadonlyJSON(checked, outputPath, 'recovery/residue receipt',
    repositoryCapture)
  return fileIndexFor(outputPath, checked.receiptDigest)
}

export async function persistRecoveryResidueScenarioReceipt(receipt, outputPath, dependencies = undefined) {
  return collectRecoveryResidueScenario({ receipt, outputPath,
    repositoryCapture: dependencies?.repositoryCapture })
}

export function formRecoveryResidueManifest(draft) {
  assertPlainObject(draft, 'recovery/residue manifest draft')
  assertExactKeys(draft, manifestKeys.filter((key) => key !== 'aggregateDigest'),
    'recovery/residue manifest draft')
  const manifest = { ...clone(draft), aggregateDigest: sha256(canonicalJSONStringify(draft)) }
  return deepFreeze(assertRecoveryResidueManifest(manifest))
}

export function assertRecoveryResidueManifest(manifest) {
  assertPlainObject(manifest, 'recovery/residue manifest')
  assertExactKeys(manifest, manifestKeys, 'recovery/residue manifest')
  assert(manifest.schemaVersion === RECOVERY_RESIDUE_MANIFEST_SCHEMA && manifest.status === 'complete',
    'recovery/residue manifest schema/status drifted')
  validateSharedIdentity(manifest, 'recovery/residue manifest')
  validateSingleIndex(manifest.candidateTuple, 'tuple.json', 'candidate tuple index')
  validateSingleIndex(manifest.phaseCReceipt, '07-C.json', 'phase-C receipt index')
  validateSingleIndex(manifest.authority, 'o4-fault-authority.json', 'fault authority index')
  validateSingleIndex(manifest.authorizedRestart, 'restart-01.json', 'authorized restart index')
  validateFileIndex(manifest.scenarioReceipts, manifestIndexNames.scenarioReceipts,
    'recovery scenario receipt index')
  validateRangeIndex(manifest.sourceA3, 'recovery source A3 index', false)
  validateRangeIndex(manifest.verifier, 'recovery Verifier index', true)
  validateFileIndex(manifest.authorityConsumptions, manifestIndexNames.authorityConsumptions,
    'authority consumption index')
  validateFileIndex(manifest.resourceLedgers, manifestIndexNames.resourceLedgers,
    'recovery resource-ledger index')
  validateFileIndex(manifest.generationReferences, manifestIndexNames.generationReferences,
    'recovery generation-reference index')
  validateSingleIndex(manifest.publicResidue, 'residue.json', 'public residue index')
  validateSingleIndex(manifest.serviceControl, 'service-control.json', 'service-control index')
  validateDisclosureSourcesIndex(manifest.disclosureSources)
  validateScreenshotIndex(manifest.screenshots)
  validateSharedImages(manifest.sharedImages)
  assertDigest(manifest.aggregateDigest, 'recovery/residue manifest aggregate digest')
  const { aggregateDigest, ...safe } = manifest
  assert(manifest.aggregateDigest === sha256(canonicalJSONStringify(safe)),
    'recovery/residue manifest aggregate digest drifted')
  assertSafeEvidenceValue(manifest, 'recovery/residue manifest')
  return manifest
}

export async function buildRecoveryResidueManifest(input) {
  assertPlainObject(input, 'recovery/residue manifest build input')
  assertExactKeys(input, [
    'paths', 'environmentId', 'installId', 'generationId', 'bindingDigest', 'tupleIdentity',
    'sharedImages',
  ], 'recovery/residue manifest build input')
  const { paths } = input
  assertPlainObject(paths, 'recovery/residue prepared paths')
  const shared = {
    environmentId: input.environmentId, installId: input.installId, generationId: input.generationId,
    bindingDigest: input.bindingDigest, tupleIdentity: input.tupleIdentity,
  }
  validateSharedIdentity(shared, 'recovery/residue manifest build identity')

  const tuple = await semanticJSONIndex(paths.tupleFile, 'candidate tuple', validateCandidateTuple)
  assertTupleBinding(tuple.value, shared)
  const cReceipt = await semanticJSONIndex(paths.phaseCReceiptFile, 'phase-C receipt', validatePhaseCReceipt)
  assert(cReceipt.value.tupleIdentity === shared.tupleIdentity, 'phase-C receipt tuple drifted')
  const authority = await semanticJSONIndex(paths.authorityFile, 'O4 authority', validateFaultAuthority)
  assertAuthorityBinding(authority.value, shared)
  const restart = await semanticJSONIndex(paths.authorizedRestartReceiptFile,
    'authorized restart receipt', (value) => validateRestartReceipt(value, 1))
  assert(restart.value.tupleIdentity === shared.tupleIdentity && restart.value.setupReplayed === false,
    'authorized restart receipt tuple/no-Setup binding drifted')

  const receiptFiles = await exactIndexedFiles(paths.receiptDirectory,
    manifestIndexNames.scenarioReceipts, 'recovery receipt directory')
  const receiptInputs = []
  for (let index = 0; index < receiptFiles.length; index++) {
    const receiptInput = await semanticJSONIndex(receiptFiles[index], `recovery receipt ${index + 1}`,
      (value) => assertRecoveryResidueScenarioReceipt(value, RECOVERY_RESIDUE_SCENARIOS[index]))
    assertReceiptBinding(receiptInput.value, shared)
    receiptInputs.push(receiptInput)
  }
  validateWholeScenarioCohort(receiptInputs.map(({ value }) => value))

  const sourceA3 = await semanticLedgerIndex(paths.sourceA3LedgerFile, 'source A3 ledger', receiptInputs, false)
  let verifier
  if (paths.verifierLedgerFile === null) {
    assert(receiptInputs.every(({ value }) => value.verifierSource === null),
      'Verifier ledger is absent but a receipt cites Verifier evidence')
    verifier = { absent: true, reason: 'no_verifier_for_terminal_pre_patch_scenarios' }
  } else {
    verifier = await semanticLedgerIndex(paths.verifierLedgerFile, 'Verifier ledger', receiptInputs, true)
  }

  const consumptionInputs = await readSemanticDirectory(paths.authorityConsumptionDirectory,
    manifestIndexNames.authorityConsumptions, 'authority consumption', validateAuthorityConsumption)
  assertConsumptionBindings(consumptionInputs.map(({ value }) => value), authority.value, receiptInputs.map(({ value }) => value))

  const resourceInputs = await readSemanticDirectory(paths.supervisorResourceDirectory,
    manifestIndexNames.resourceLedgers, 'recovery resource ledger', validateResourceLedger)
  assertResourceBindings(resourceInputs.map(({ value }) => value), shared, receiptInputs.map(({ value }) => value))

  const generationInputs = await readSemanticDirectory(paths.generationReferenceDirectory,
    manifestIndexNames.generationReferences, 'recovery generation reference', validateGenerationReference)
  assertGenerationBindings(generationInputs.map(({ value }) => value), shared, receiptInputs.map(({ value }) => value))

  const publicResidue = await semanticJSONIndex(paths.publicResidueFile, 'public O4 residue', validatePublicResidue)
  assert(publicResidue.value.tupleIdentity === shared.tupleIdentity &&
    publicResidue.value.authoritySha256 === authority.index.rawSha256,
  'public residue authority/tuple drifted')
  const orphanRestart = await semanticJSONIndex(paths.orphanRestartReceiptFile,
    'orphan restart receipt', (value) => validateRestartReceipt(value, 3))
  const serviceControl = await semanticJSONIndex(paths.serviceControlFile,
    'authenticated service control', validateServiceControl)
  await validatePinnedExecutable(paths.serviceControllerExecutable, paths.serviceControllerSha256,
    'service controller at manifest boundary')
  assert(serviceControl.value.controllerSha256 === paths.serviceControllerSha256 &&
    serviceControl.value.controllerPathSha256 === sha256(paths.serviceControllerExecutable),
  'service-control executable pin/path drifted')
  assertServiceControlBinding(serviceControl.value, shared, restart, orphanRestart)
  await validatePinnedExecutable(paths.ocrExecutable, paths.ocrExecutableSha256,
    'system-managed OCR executable at manifest boundary')
  await validatePinnedSystemExecutable(paths.ocrSandboxExecutable,
    paths.ocrSandboxExecutableSha256, 'OCR sandbox executable at manifest boundary')
  const engineBytes = await readExactFile(paths.ocrEngineBindingFile,
    'OCR engine binding at manifest boundary', maxJSON, 0o400)
  assert(sha256(engineBytes) === paths.ocrEngineBindingSha256 && paths.ocrLanguage === 'eng',
    'OCR engine binding/language pin drifted')
  validateOCREngineBinding(parseJSON(engineBytes, 'OCR engine binding at manifest boundary'), paths)
  const sandboxProfile = await readExactFile(paths.ocrSandboxProfileFile,
    'OCR sandbox profile at manifest boundary', maxJSON, 0o400)
  assert(sha256(sandboxProfile) === paths.ocrSandboxProfileSha256,
    'OCR sandbox profile pin drifted')
  validateOCRSandboxProfile(sandboxProfile)
  const screenshots = await readScreenshotIndexes(paths, receiptInputs.map(({ value }) => value))
  const disclosureSources = await readDisclosureSourcesIndex(paths, receiptInputs.at(-1).value,
    screenshots.at(-1))

  const draft = {
    schemaVersion: RECOVERY_RESIDUE_MANIFEST_SCHEMA, status: 'complete', ...shared,
    candidateTuple: tuple.index, phaseCReceipt: cReceipt.index, authority: authority.index,
    authorizedRestart: restart.index,
    scenarioReceipts: receiptInputs.map(({ index }) => index),
    sourceA3: sourceA3.index, verifier: verifier.index ?? verifier,
    authorityConsumptions: consumptionInputs.map(({ index }) => index),
    resourceLedgers: resourceInputs.map(({ index }) => index),
    generationReferences: generationInputs.map(({ index }) => index),
    publicResidue: publicResidue.index, serviceControl: serviceControl.index, disclosureSources,
    screenshots, sharedImages: clone(input.sharedImages),
  }
  return formRecoveryResidueManifest(draft)
}

export async function persistRecoveryResidueManifest(manifest, outputPath, dependencies = undefined) {
  const checked = assertRecoveryResidueManifest(manifest)
  exactAbsolutePath(outputPath, 'recovery/residue manifest output')
  assert(basename(outputPath) === 'manifest.json', 'recovery/residue manifest filename drifted')
  await writeExclusiveOwnerReadonlyJSON(checked, outputPath, 'recovery/residue manifest',
    dependencies?.repositoryCapture)
  return fileIndexFor(outputPath, checked.aggregateDigest)
}

export async function buildRecoveryResiduePhaseDRecord(input) {
  assertPlainObject(input, 'phase-D recovery/residue record build input')
  assertExactKeys(input, ['manifest', 'receiptDirectory'],
    'phase-D recovery/residue record build input')
  let { manifest } = input
  const { receiptDirectory } = input
  manifest = assertRecoveryResidueManifest(manifest)
  const files = await exactIndexedFiles(receiptDirectory,
    manifestIndexNames.scenarioReceipts, 'recovery receipt directory')
  const receipts = []
  for (let index = 0; index < files.length; index++) {
    const input = await semanticJSONIndex(files[index], `recovery receipt ${index + 1}`,
      (value) => assertRecoveryResidueScenarioReceipt(value, RECOVERY_RESIDUE_SCENARIOS[index]))
    assertIndexMatches(input.index, manifest.scenarioReceipts[index], `recovery receipt ${index + 1}`)
    receipts.push(input.value)
  }
  validateWholeScenarioCohort(receipts)
  const scenarios = receipts.map((receipt) => ({
    sequence: receipt.sequence, name: receipt.name, taskId: receipt.taskId, runId: receipt.runId,
    attemptId: receipt.attemptId, workspaceId: receipt.workspaceId, targetId: receipt.targetId,
    terminalStatus: receipt.terminalStatus, publicEntry: receipt.publicEntry,
    cleanupComplete: receipt.cleanupComplete, residue: clone(receipt.residue),
  }))
  const draft = {
    schemaVersion: RECOVERY_RESIDUE_PHASE_D_RECORD_SCHEMA, status: 'passed',
    environmentId: manifest.environmentId, installId: manifest.installId,
    generationId: manifest.generationId, bindingDigest: manifest.bindingDigest,
    tupleIdentity: manifest.tupleIdentity, scenarios, sharedImages: clone(manifest.sharedImages),
    imageReuseFormed: false, finalRecoveryFormed: false,
  }
  return formRecoveryResiduePhaseDRecord(draft)
}

// Compatibility name for the phase-D producer. It deliberately accepts no
// future reuse digest.
export const buildRecoveryResidueRecord = buildRecoveryResiduePhaseDRecord

export function formRecoveryResiduePhaseDRecord(draft) {
  assertPlainObject(draft, 'phase-D recovery/residue record draft')
  assertExactKeys(draft, phaseDRecordKeys.filter((key) => key !== 'recordDigest'),
    'phase-D recovery/residue record draft')
  const record = { ...clone(draft), recordDigest: sha256(canonicalJSONStringify(draft)) }
  return deepFreeze(assertRecoveryResiduePhaseDRecord(record))
}

export function assertRecoveryResiduePhaseDRecord(record) {
  assertPlainObject(record, 'phase-D recovery/residue record')
  assertExactKeys(record, phaseDRecordKeys, 'phase-D recovery/residue record')
  assert(record.schemaVersion === RECOVERY_RESIDUE_PHASE_D_RECORD_SCHEMA && record.status === 'passed',
    'phase-D recovery/residue record schema/status drifted')
  validateSharedIdentity(record, 'phase-D recovery/residue record')
  assert(record.imageReuseFormed === false && record.finalRecoveryFormed === false,
    'phase-D recovery/residue record formed a future component')
  validateRecoveryPublicScenarios(record.scenarios)
  validateSharedImages(record.sharedImages)
  assertDigest(record.recordDigest, 'phase-D recovery/residue record digest')
  const { recordDigest, ...safe } = record
  assert(recordDigest === sha256(canonicalJSONStringify(safe)),
    'phase-D recovery/residue record digest drifted')
  assertSafeEvidenceValue(record, 'phase-D recovery/residue record')
  return record
}

/**
 * E-only pure projection. Every supplied receipt is already authenticated by
 * the D manifest, and the actual reuse.digest is available only after E seals
 * the complete A3 ledger.
 */
export function buildFinalRecoveryResidueRecord(input) {
  assertPlainObject(input, 'final recovery/residue record build input')
  assertExactKeys(input, ['manifest', 'phaseDRecord', 'receipts', 'reuse'],
    'final recovery/residue record build input')
  let { manifest, phaseDRecord } = input
  const { receipts, reuse } = input
  manifest = assertRecoveryResidueManifest(manifest)
  phaseDRecord = assertRecoveryResiduePhaseDRecord(phaseDRecord)
  assertPlainObject(reuse, 'authenticated final image reuse projection')
  assertExactKeys(reuse, [
    'environmentId', 'installId', 'generationId', 'bindingDigest', 'digest',
  ], 'authenticated final image reuse projection')
  validateRecordIdentity(reuse)
  assertDigest(reuse.digest, 'final recovery image reuse digest')
  assertSameIdentity(phaseDRecord, reuse, 'phase-D recovery/final image reuse')
  assert(phaseDRecord.bindingDigest === reuse.bindingDigest,
    'phase-D recovery/final image reuse binding digest drifted')
  assert(Array.isArray(receipts) && receipts.length === RECOVERY_RESIDUE_SCENARIOS.length,
    'final recovery receipts are incomplete')
  const checkedReceipts = receipts.map((receipt, index) =>
    assertRecoveryResidueScenarioReceipt(receipt, RECOVERY_RESIDUE_SCENARIOS[index]))
  validateWholeScenarioCohort(checkedReceipts)
  for (let index = 0; index < checkedReceipts.length; index++) {
    assert(checkedReceipts[index].receiptDigest === manifest.scenarioReceipts[index].semanticDigest,
      `final recovery receipt ${index + 1} was spliced from phase D`)
  }
  assertSameIdentity(manifest, phaseDRecord, 'phase-D manifest/record')
  assert(manifest.tupleIdentity === phaseDRecord.tupleIdentity &&
    canonicalJSONStringify(manifest.sharedImages) === canonicalJSONStringify(phaseDRecord.sharedImages),
  'phase-D manifest/record tuple or images drifted')
  const scenarios = checkedReceipts.map((receipt) => ({
    sequence: receipt.sequence, name: receipt.name, taskId: receipt.taskId, runId: receipt.runId,
    attemptId: receipt.attemptId, workspaceId: receipt.workspaceId, targetId: receipt.targetId,
    terminalStatus: receipt.terminalStatus, publicEntry: receipt.publicEntry,
    cleanupComplete: receipt.cleanupComplete, residue: clone(receipt.residue),
  }))
  assertSameJSON(scenarios, phaseDRecord.scenarios,
    'phase-D public scenarios drifted before final recovery formation')
  return formRecoveryResidueRecord({
    schemaVersion: RECOVERY_RESIDUE_RECORD_SCHEMA, status: 'passed',
    environmentId: phaseDRecord.environmentId, installId: phaseDRecord.installId,
    generationId: phaseDRecord.generationId, bindingDigest: phaseDRecord.bindingDigest,
    imageReuseDigest: reuse.digest, scenarios, sharedImages: clone(phaseDRecord.sharedImages),
  })
}

export function formRecoveryResidueRecord(draft) {
  assertPlainObject(draft, 'final recovery/residue record draft')
  assertExactKeys(draft, finalRecordKeys.filter((key) => key !== 'digest'),
    'final recovery/residue record draft')
  const record = { ...clone(draft), digest: sha256(canonicalJSONStringify(draft)) }
  return deepFreeze(assertRecoveryResidueRecord(record))
}

export function assertRecoveryResidueRecord(record) {
  assertPlainObject(record, 'final recovery/residue record')
  assertExactKeys(record, finalRecordKeys, 'final recovery/residue record')
  assert(record.schemaVersion === RECOVERY_RESIDUE_RECORD_SCHEMA && record.status === 'passed',
    'final recovery/residue record schema/status drifted')
  validateRecordIdentity(record)
  assertDigest(record.imageReuseDigest, 'final recovery/residue image reuse digest')
  validateRecoveryPublicScenarios(record.scenarios)
  validateSharedImages(record.sharedImages)
  assertDigest(record.digest, 'final recovery/residue record digest')
  const { digest, ...safe } = record
  assert(record.digest === sha256(canonicalJSONStringify(safe)), 'final recovery/residue record digest drifted')
  assertSafeEvidenceValue(record, 'final recovery/residue record')
  return record
}

function validateRecoveryPublicScenarios(scenarios) {
  assert(Array.isArray(scenarios) && scenarios.length === RECOVERY_RESIDUE_SCENARIOS.length,
    'recovery/residue public scenario count drifted')
  for (let index = 0; index < RECOVERY_RESIDUE_SCENARIOS.length; index++) {
    const scenario = scenarios[index]
    const contract = RECOVERY_RESIDUE_SCENARIOS[index]
    assertPlainObject(scenario, 'recovery/residue public scenario')
    assertExactKeys(scenario, [
      'sequence', 'name', 'taskId', 'runId', 'attemptId', 'workspaceId', 'targetId',
      'terminalStatus', 'publicEntry', 'cleanupComplete', 'residue',
    ], 'recovery/residue public scenario')
    assert(scenario.sequence === contract.sequence && scenario.name === contract.name &&
      scenario.terminalStatus === contract.terminalStatus && scenario.publicEntry === true &&
      scenario.cleanupComplete === true,
    `recovery ${contract.name} public state drifted`)
    validateProductIDs(scenario, `recovery ${contract.name} public`)
    validateZeroResidue(scenario.residue, `recovery ${contract.name} public residue`)
  }
  for (const field of Object.keys(productIDs)) {
    assertUnique(scenarios.map((scenario) => scenario[field]), `recovery public ${field}`)
  }
}

export async function persistRecoveryResiduePhaseDRecord(record, outputPath, dependencies = undefined) {
  const checked = assertRecoveryResiduePhaseDRecord(record)
  exactAbsolutePath(outputPath, 'phase-D recovery/residue record output')
  assert(basename(outputPath) === 'record.json', 'phase-D recovery/residue record filename drifted')
  await writeExclusiveOwnerReadonlyJSON(checked, outputPath, 'phase-D recovery/residue record',
    dependencies?.repositoryCapture)
  return fileIndexFor(outputPath, checked.recordDigest)
}

export async function persistRecoveryResidueRecord(record, outputPath, dependencies = undefined) {
  const checked = assertRecoveryResidueRecord(record)
  exactAbsolutePath(outputPath, 'recovery/residue record output')
  assert(basename(outputPath) === 'record.json', 'recovery/residue record filename drifted')
  await writeExclusiveOwnerReadonlyJSON(checked, outputPath, 'recovery/residue record',
    dependencies?.repositoryCapture)
  return fileIndexFor(outputPath, checked.digest)
}

/**
 * Publish manifest, public record, and D in that order. D is appended only after
 * reopening and byte-validating both 0400 publications. Pre-append validation
 * failures remove only files created by this call and prove that no D receipt
 * was created. Invoking the locked D appender is the irreversible commit point.
 */
export async function persistRecoveryResidueEvidence({
  manifest, record, paths, manifestFile, recordFile, tupleFile, handoffReceiptDirectory,
  repositoryCapture,
}) {
  repositoryCapture = validateOwnedPathCapability(
    repositoryCapture, 'recovery publication owned-path capability')
  manifest = assertRecoveryResidueManifest(manifest)
  record = assertRecoveryResiduePhaseDRecord(record)
  assert(paths?.manifestFile === manifestFile && paths?.recordFile === recordFile &&
    paths?.tupleFile === tupleFile,
  'recovery publication prepared path binding drifted')
  const beforeD = await phaseDReceiptState(handoffReceiptDirectory)
  assert(beforeD.count === 0, 'phase D already exists')
  // Rebuild from every raw byte and external pin immediately before the first
  // publishable write. This closes the build/persist drift window.
  const rebuiltManifest = await buildRecoveryResidueManifest({
    paths, environmentId: manifest.environmentId, installId: manifest.installId,
    generationId: manifest.generationId, bindingDigest: manifest.bindingDigest,
    tupleIdentity: manifest.tupleIdentity, sharedImages: manifest.sharedImages,
  })
  assertSameJSON(rebuiltManifest, manifest, 'recovery manifest inputs drifted before publication')
  const rebuiltRecord = await buildRecoveryResiduePhaseDRecord({
    manifest, receiptDirectory: paths.receiptDirectory,
  })
  assertSameJSON(rebuiltRecord, record, 'recovery record inputs drifted before publication')
  exactAbsolutePath(manifestFile, 'recovery/residue manifest output')
  exactAbsolutePath(recordFile, 'recovery/residue record output')
  let manifestIdentity = null
  let recordIdentity = null
  let manifestBytes
  let persistedRecord
  try {
    manifestIdentity = await writeExclusiveOwnerReadonlyJSON(manifest, manifestFile,
      'recovery/residue manifest', repositoryCapture)
    manifestBytes = await readExactFile(manifestFile, 'persisted recovery manifest', maxJSON, 0o400)
    assertRecoveryResidueManifest(parseJSON(manifestBytes, 'persisted recovery manifest'))
    recordIdentity = await writeExclusiveOwnerReadonlyJSON(record, recordFile,
      'phase-D recovery/residue record', repositoryCapture)
    const recordBytes = await readExactFile(recordFile, 'persisted recovery record', maxJSON, 0o400)
    persistedRecord = assertRecoveryResiduePhaseDRecord(parseJSON(recordBytes, 'persisted recovery record'))
  } catch (error) {
    const cleanupErrors = []
    for (const [path, identity] of [
      [recordFile, recordIdentity], [manifestFile, manifestIdentity],
    ]) {
      if (identity === null) continue
      try {
        await cleanupOwnedPath({ capability: repositoryCapture, path, identity,
          disposition: 'regular_file' })
      } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    }
    for (const [path, label] of [
      [manifestFile, 'failed recovery manifest'], [recordFile, 'failed recovery record'],
    ]) {
      try { await assertAbsent(path, label) } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    }
    try {
      const afterD = await phaseDReceiptState(handoffReceiptDirectory)
      assert(afterD.count === 0, 'failed recovery publication left a D receipt')
    } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    if (cleanupErrors.length > 0) throw new AggregateError(
      [error, ...cleanupErrors], 'recovery publication and native cleanup failed', { cause: error })
    throw error
  }
  const manifestSha256 = sha256(manifestBytes)
  const recordDigest = persistedRecord.recordDigest
  // appendHandoffReceipt is the commit point. Its own O_EXCL implementation is
  // responsible for atomic failure before publication. Once invoked, this
  // function never removes or rewrites manifest, record, or any receipt and
  // performs no fallible validation after a successful return.
  const receipt = await appendHandoffReceipt({
    tupleFile, receiptDirectory: handoffReceiptDirectory, phase: 'D',
    operationDigest: manifestSha256, observationDigest: recordDigest,
  }, { repositoryCapture })
  return Object.freeze({ manifestSha256, recordDigest, receipt })
}

export async function cleanupFailedRecoveryResiduePublication({ manifestFile, recordFile, receiptDirectory }) {
  const state = await phaseDReceiptState(receiptDirectory)
  assert(state.count === 0, 'failed recovery cleanup found a D receipt')
  // The publication entry point already cleans only identities it captured at
  // creation. This external verifier has no ownership token and therefore
  // never deletes by pathname.
  await assertAbsent(manifestFile, 'failed recovery manifest')
  await assertAbsent(recordFile, 'failed recovery record')
}

export function validateServiceControl(value) {
  assertPlainObject(value, 'service-control proof')
  assertExactKeys(value, [
    'schemaVersion', 'status', 'protocol', 'controllerPathSha256', 'controllerSha256',
    'requestSha256', 'runtimeBindingSha256', 'argvSha256', 'tupleIdentity', 'binarySha256',
    'generationId', 'oldService', 'termination',
    'newService', 'restartReceipt', 'networkDisabled', 'digest',
  ], 'service-control proof')
  assert(value.schemaVersion === RECOVERY_RESIDUE_SERVICE_CONTROL_SCHEMA && value.status === 'passed' &&
    value.protocol === 'pinned_authenticated_service_control_v1' && value.networkDisabled === true,
  'service-control schema/status/protocol/network drifted')
  for (const field of ['controllerPathSha256', 'controllerSha256', 'requestSha256',
    'runtimeBindingSha256', 'argvSha256',
    'tupleIdentity', 'binarySha256', 'digest']) {
    assertDigest(value[field], `service-control ${field}`)
  }
  assertOpaque(value.generationId, opaqueInstallRE, 'service-control generation ID')
  validateServiceIdentity(value.oldService, 'old service')
  validateServiceIdentity(value.newService, 'new service')
  assert(value.oldService.pid !== value.newService.pid &&
    value.oldService.startIdentitySha256 !== value.newService.startIdentitySha256,
  'service-control old/new service identities are not distinct')
  for (const side of ['oldService', 'newService']) {
    const service = value[side]
    assert(service.binarySha256 === value.binarySha256 && service.tupleIdentity === value.tupleIdentity &&
      service.generationId === value.generationId,
    `service-control ${side} candidate binding drifted`)
  }
  assertPlainObject(value.termination, 'service-control termination')
  assertExactKeys(value.termination,
    ['requestedSignal', 'observedSignal', 'exitCode', 'processGroupDead', 'completedAt'],
    'service-control termination')
  assert(value.termination.requestedSignal === 'SIGKILL' && value.termination.observedSignal === 'SIGKILL' &&
    value.termination.exitCode === null && value.termination.processGroupDead === true,
  'service-control does not prove exact SIGKILL and full process-group death')
  assertTimestamp(value.termination.completedAt, 'service-control termination completion')
  assert(value.newService.readiness === 'ready' && value.newService.currentGeneration === true,
    'service-control new service is not ready/current')
  validateFileIndexEntry(value.restartReceipt, 'service-control restart receipt')
  const { digest, ...safe } = value
  assert(value.digest === sha256(canonicalJSONStringify(safe)), 'service-control digest drifted')
  assertSafeEvidenceValue(value, 'service-control proof')
  return value
}

export function validateScreenshotMetadata(value, pngBytes) {
  assert(Buffer.isBuffer(pngBytes) && pngBytes.length >= 20 && pngBytes.length <= maxPNG &&
    pngBytes.subarray(0, 8).equals(pngSignature), 'recovery screenshot is not a bounded PNG')
  const chunkTypes = parsePNGChunkTypes(pngBytes)
  assert(!chunkTypes.some((type) => ['tEXt', 'zTXt', 'iTXt', 'eXIf'].includes(type)),
    'recovery screenshot contains unsafe metadata')
  assertPlainObject(value, 'recovery screenshot metadata')
  assertExactKeys(value, ['schemaVersion', 'pngSha256', 'completed', 'chunkTypes', 'unsafeMatchCount'],
    'recovery screenshot metadata')
  assert(value.schemaVersion === SCREENSHOT_METADATA_SCHEMA && value.pngSha256 === sha256(pngBytes) &&
    value.completed === true && value.unsafeMatchCount === 0,
  'recovery screenshot metadata scan drifted')
  assertSameJSON(value.chunkTypes, chunkTypes, 'recovery screenshot chunk audit drifted')
  return value
}

export function validateScreenshotOCR(value, pngBytes) {
  assertPlainObject(value, 'recovery screenshot OCR')
  assertExactKeys(value, [
    'schemaVersion', 'completed', 'pngSha256', 'engineBindingSha256', 'executableSha256',
    'visionBundleIdentifier', 'visionBundleVersion', 'visionInfoPlistSha256',
    'actualRecognitionRevision', 'recognitionLevel', 'recognitionLanguage',
    'usesLanguageCorrection', 'confidenceThreshold', 'observationCount', 'recognizedText',
    'recognizedTextSha256', 'minimumConfidence', 'averageConfidence',
    'credentialMatches', 'genericSecretMatches', 'privatePathMatches', 'emailMatches',
    'urlMatches', 'modelBytes', 'networkDisabled', 'recordDigest',
  ], 'recovery screenshot OCR')
  assert(value.schemaVersion === SCREENSHOT_OCR_SCHEMA && value.pngSha256 === sha256(pngBytes) &&
    value.completed === true && typeof value.recognizedText === 'string' && value.recognizedText.length > 0 &&
    value.recognizedText.length <= 64 * 1024 &&
    value.recognizedTextSha256 === sha256(Buffer.from(value.recognizedText)) &&
    value.recognitionLevel === 'accurate' && value.recognitionLanguage === 'en-US' &&
    value.usesLanguageCorrection === true && value.modelBytes === 'opaque_unavailable' &&
    value.networkDisabled === true && Number.isSafeInteger(value.actualRecognitionRevision) &&
    value.actualRecognitionRevision > 0 && Number.isSafeInteger(value.observationCount) &&
    value.observationCount > 0 && typeof value.confidenceThreshold === 'number' &&
    value.confidenceThreshold > 0 && value.confidenceThreshold <= 1 &&
    typeof value.minimumConfidence === 'number' && value.minimumConfidence >= value.confidenceThreshold &&
    value.minimumConfidence <= 1 && typeof value.averageConfidence === 'number' &&
    value.averageConfidence >= value.minimumConfidence && value.averageConfidence <= 1,
  'recovery screenshot OCR identity/output drifted')
  for (const field of [
    'credentialMatches', 'genericSecretMatches', 'privatePathMatches', 'emailMatches', 'urlMatches',
  ]) assert(value[field] === 0, `recovery screenshot OCR ${field} is non-zero`)
  for (const field of ['engineBindingSha256', 'executableSha256', 'visionInfoPlistSha256',
    'recognizedTextSha256', 'recordDigest']) assertDigest(value[field], `recovery screenshot OCR ${field}`)
  assert(typeof value.visionBundleIdentifier === 'string' && value.visionBundleIdentifier.length > 0 &&
    typeof value.visionBundleVersion === 'string' && value.visionBundleVersion.length > 0,
  'recovery screenshot OCR Vision identity is invalid')
  const { recordDigest, ...safe } = value
  assert(recordDigest === sha256(canonicalJSONStringify(safe)),
    'recovery screenshot OCR record digest drifted')
  assertSafeEvidenceValue(value, 'recovery screenshot OCR')
  return value
}

/**
 * Execute a pinned external controller through a silent, bounded, detached
 * process protocol. This helper contains no environment-specific controller.
 */
export async function runPinnedServiceController({
  executableFile, executableSha256, requestFile, outputFile, timeoutMs = 30_000,
}) {
  await validatePinnedExecutable(executableFile, executableSha256, 'service controller')
  const protocol = await serviceControlProtocolBinding(requestFile, outputFile)
  await assertAbsent(outputFile, 'service controller output')
  try {
    await runPinnedTool(executableFile, protocol.argv, timeoutMs, 64 * 1024, 'service controller')
    await validatePinnedExecutable(executableFile, executableSha256, 'service controller')
    const output = await semanticJSONIndex(outputFile, 'service controller output', validateServiceControl)
    assertServiceControlProtocolBinding(output.value, protocol)
    return output.value
  } catch (error) {
    try {
      await assertAbsent(outputFile, 'failed service controller output')
    } catch (cleanupError) {
      throw new AggregateError(
        [error, cleanupError],
        'service controller failed and did not prove atomic output cleanup',
        { cause: error },
      )
    }
    throw error
  }
}

/**
 * Read-only completion boundary for the single controller process launched by
 * the authenticated recovery Runner. It never starts, signals, restarts,
 * removes, or rewrites anything.
 */
export async function readPinnedServiceControlProof({
  executableFile, executableSha256, requestFile, outputFile, timeoutMs = 30_000,
}) {
  assert(Number.isSafeInteger(timeoutMs) && timeoutMs >= 100 && timeoutMs <= 60_000,
    'service-control proof timeout is outside the bounded protocol')
  await validatePinnedExecutable(executableFile, executableSha256, 'service controller proof pin')
  const protocol = await serviceControlProtocolBinding(requestFile, outputFile)
  const deadline = Date.now() + timeoutMs
  while (true) {
    try {
      const output = await semanticJSONIndex(outputFile, 'service controller proof', validateServiceControl)
      assertServiceControlProtocolBinding(output.value, protocol)
      await validatePinnedExecutable(executableFile, executableSha256, 'service controller proof pin')
      return output.value
    } catch (error) {
      if (error.code !== 'ENOENT') throw error
      if (Date.now() >= deadline) throw new Error('service controller proof timed out')
      await new Promise((resolvePromise) => setTimeout(resolvePromise, 25))
    }
  }
}

async function serviceControlProtocolBinding(requestFile, outputFile) {
  exactAbsolutePath(requestFile, 'service controller request')
  exactAbsolutePath(outputFile, 'service controller output')
  const requestBytes = await readExactFile(requestFile, 'service controller request', maxJSON, 0o400)
  const argv = [
    '--protocol', RECOVERY_RESIDUE_SERVICE_CONTROL_SCHEMA,
    '--request', requestFile, '--output', outputFile, '--network', 'disabled',
    '--runtime-input', 'stdin',
  ]
  return { requestSha256: sha256(requestBytes), argv,
    argvSha256: sha256(canonicalJSONStringify(argv)) }
}

function assertServiceControlProtocolBinding(value, protocol) {
  assert(value.requestSha256 === protocol.requestSha256,
    'service controller request binding drifted')
}

export async function runPinnedOfflineOCR({
  executableFile, executableSha256, engineBindingFile, engineBindingSha256,
  sandboxExecutable, sandboxExecutableSha256, sandboxProfileFile, sandboxProfileSha256, language,
  pngFile, outputFile, timeoutMs = 30_000,
}) {
  await validatePinnedExecutable(executableFile, executableSha256, 'system-managed OCR executable')
  await validatePinnedSystemExecutable(sandboxExecutable, sandboxExecutableSha256,
    'system-managed OCR sandbox executable')
  const bindingBytes = await readExactFile(engineBindingFile, 'system-managed OCR engine binding',
    maxJSON, 0o400)
  assert(sha256(bindingBytes) === engineBindingSha256, 'OCR engine binding digest drifted')
  const binding = validateOCREngineBinding(parseJSON(bindingBytes, 'system-managed OCR engine binding'), {
    ocrExecutable: executableFile, ocrExecutableSha256: executableSha256,
    ocrEngineBindingFile: engineBindingFile, ocrEngineBindingSha256: engineBindingSha256,
    ocrSandboxExecutable: sandboxExecutable, ocrSandboxExecutableSha256: sandboxExecutableSha256,
    ocrSandboxProfileFile: sandboxProfileFile, ocrSandboxProfileSha256: sandboxProfileSha256,
  })
  const profileBytes = await readExactFile(sandboxProfileFile, 'system-managed OCR sandbox profile',
    maxJSON, 0o400)
  assert(sha256(profileBytes) === sandboxProfileSha256, 'OCR sandbox profile digest drifted')
  validateOCRSandboxProfile(profileBytes)
  assert(language === 'eng', 'system-managed OCR language drifted')
  const pngBytes = await readExactFile(pngFile, 'system-managed OCR PNG', maxPNG, 0o400)
  assert(pngBytes.subarray(0, 8).equals(pngSignature), 'system-managed OCR input is not a PNG')
  exactAbsolutePath(outputFile, 'system-managed OCR output')
  await assertAbsent(outputFile, 'system-managed OCR output')
  try {
    const ocrArgv = [
      '--protocol', RECOVERY_RESIDUE_OCR_PROTOCOL, '--engine-binding', engineBindingFile,
      '--language', language,
      '--input', pngFile, '--output', outputFile, '--network', 'disabled',
    ]
    await runPinnedTool(sandboxExecutable,
      ['-f', sandboxProfileFile, executableFile, ...ocrArgv], timeoutMs, 64 * 1024,
      'system-managed OCR')
    await validatePinnedExecutable(executableFile, executableSha256, 'system-managed OCR executable')
    const bindingAfter = await readExactFile(engineBindingFile,
      'system-managed OCR engine binding after execution', maxJSON, 0o400)
    assert(sha256(bindingAfter) === engineBindingSha256,
      'system-managed OCR engine binding changed during execution')
    const output = await semanticJSONIndex(outputFile, 'system-managed OCR output',
      (value) => validateScreenshotOCR(value, pngBytes))
    assert(output.value.engineBindingSha256 === engineBindingSha256 &&
      output.value.executableSha256 === executableSha256 &&
      output.value.actualRecognitionRevision === binding.vision.requestedRecognitionRevision,
    'system-managed OCR output engine/executable/revision binding drifted')
    return output.value
  } catch (error) {
    try {
      await assertAbsent(outputFile, 'failed system-managed OCR output')
    } catch (cleanupError) {
      throw new AggregateError(
        [error, cleanupError],
        'system-managed OCR failed and did not prove atomic output cleanup',
        { cause: error },
      )
    }
    throw error
  }
}

function validateScenarioStates(receipt, contract) {
  const noResultStates = ['absent', 'not_created']
  if (['failure', 'timeout'].includes(contract.name)) {
    assert(noResultStates.includes(receipt.patchState) && noResultStates.includes(receipt.checkState) &&
      noResultStates.includes(receipt.reviewState),
    `recovery ${contract.name} improperly created Patch/check/review state`)
    return
  }
  if (contract.name === 'cancel') {
    assert(noResultStates.includes(receipt.patchState) && noResultStates.includes(receipt.checkState) &&
      noResultStates.includes(receipt.reviewState),
    'recovery cancel improperly created Patch/check/review state')
    return
  }
  if (contract.name === 'apply') {
    assert(receipt.patchState === 'applied' && receipt.checkState === 'passed' && receipt.reviewState === 'accepted',
      'recovery apply lacks verified accepted Task-owned application')
    return
  }
  assert(receipt.patchState === 'present' && receipt.checkState === 'passed' &&
    ['awaiting_review', 'accepted'].includes(receipt.reviewState),
  `recovery ${contract.name} successful product state drifted`)
}

function validateScenarioProof(receipt) {
  const value = receipt.scenarioProof
  assertPlainObject(value, `recovery ${receipt.name} scenario proof`)
  switch (receipt.name) {
    case 'failure':
      assertExactKeys(value, ['exitCode', 'processTreeDead', 'automaticRetryCount'], 'failure proof')
      assert(Number.isSafeInteger(value.exitCode) && value.exitCode !== 0 && value.processTreeDead === true &&
        value.automaticRetryCount === 0, 'failure proof drifted')
      break
    case 'cancel':
      assertExactKeys(value, ['cancelRequestedThroughPublicUI', 'processTreeDead', 'retryAllowed'], 'cancel proof')
      assert(value.cancelRequestedThroughPublicUI === true && value.processTreeDead === true &&
        value.retryAllowed === true, 'cancel proof drifted')
      break
    case 'timeout':
      assertExactKeys(value, ['deadlineAuthorityConsumed', 'processTreeDead', 'automaticRetryCount'], 'timeout proof')
      assert(value.deadlineAuthorityConsumed === true && value.processTreeDead === true &&
        value.automaticRetryCount === 0, 'timeout proof drifted')
      break
    case 'retry':
      assertExactKeys(value, [
        'predecessorAttemptId', 'predecessorState', 'predecessorReason', 'successorAttemptId',
        'successorState', 'uiRetry',
      ], 'retry proof')
      assertOpaque(value.predecessorAttemptId, productIDs.attemptId, 'retry predecessor Attempt ID')
      assertOpaque(value.successorAttemptId, productIDs.attemptId, 'retry successor Attempt ID')
      assert(value.predecessorAttemptId !== value.successorAttemptId && value.successorAttemptId === receipt.attemptId &&
        value.predecessorState === 'rejected' && value.predecessorReason === 'result_rejected' &&
        value.successorState === 'output_submitted' && value.uiRetry === true,
      'retry predecessor/successor relation drifted')
      break
    case 'restart':
      assertExactKeys(value, ['idleBeforeRestart', 'graceful', 'setupReplayed', 'restartReceiptDigest'], 'restart proof')
      assert(value.idleBeforeRestart === true && value.graceful === true && value.setupReplayed === false,
        'restart was not an idle graceful no-Setup restart')
      assertDigest(value.restartReceiptDigest, 'restart proof receipt digest')
      break
    case 'orphan':
      assertExactKeys(value, [
        'interruptedAttemptId', 'interruptedState', 'recoveredAttemptId', 'recoveredState',
        'authenticatedServiceControl', 'serviceControlDigest',
      ], 'orphan proof')
      assertOpaque(value.interruptedAttemptId, productIDs.attemptId, 'orphan interrupted Attempt ID')
      assertOpaque(value.recoveredAttemptId, productIDs.attemptId, 'orphan recovered Attempt ID')
      assert(value.interruptedAttemptId !== value.recoveredAttemptId && value.recoveredAttemptId === receipt.attemptId &&
        value.interruptedState === 'recovery_required' && value.recoveredState === 'output_submitted' &&
        value.authenticatedServiceControl === true,
      'orphan interrupted/recovered relation drifted')
      assertDigest(value.serviceControlDigest, 'orphan service-control digest')
      break
    case 'reopen':
      assertExactKeys(value, ['rootEntry', 'roomDirectoryEntry', 'deepLinkUsed'], 'reopen proof')
      assert(value.rootEntry === '/' && value.roomDirectoryEntry === true && value.deepLinkUsed === false,
        'reopen did not traverse root/Room directory')
      break
    case 'apply':
      assertExactKeys(value, [
        'reviewKind', 'verificationState', 'applicationState', 'taskOwned', 'targetId',
        'patchDigest', 'payloadSha256', 'disclosureSourceManifestDigest',
      ], 'apply proof')
      assert(value.reviewKind === 'accept' && value.verificationState === 'completed' &&
        value.applicationState === 'applied' && value.taskOwned === true && value.targetId === receipt.targetId,
      'apply proof is not verified, accepted, applied, and Task-owned')
      for (const field of ['patchDigest', 'payloadSha256', 'disclosureSourceManifestDigest']) {
        assertDigest(value[field], `apply proof ${field}`)
      }
      break
    default: throw new Error('unknown recovery scenario')
  }
}

function validateWholeScenarioCohort(receipts) {
  assert(Array.isArray(receipts) && receipts.length === RECOVERY_RESIDUE_SCENARIOS.length,
    'recovery scenario cohort cardinality drifted')
  for (let index = 0; index < receipts.length; index++) {
    assertRecoveryResidueScenarioReceipt(receipts[index], RECOVERY_RESIDUE_SCENARIOS[index])
    if (index > 0) {
      const prior = receipts[index - 1]
      assert(Date.parse(receipts[index].completedAt) >= Date.parse(prior.completedAt),
        'recovery scenario completion order drifted')
      assertSameIdentity(receipts[0], receipts[index], 'recovery scenario cohort')
      assert(receipts[0].tupleIdentity === receipts[index].tupleIdentity &&
        receipts[0].bindingDigest === receipts[index].bindingDigest,
      'recovery scenario tuple/binding drifted')
    }
  }
  for (const field of Object.keys(productIDs)) {
    assertUnique(receipts.map((receipt) => receipt[field]), `recovery cohort ${field}`)
  }
  const retry = receipts[3]
  assert(!receipts.some((receipt, index) => index !== 3 &&
    receipt.attemptId === retry.scenarioProof.predecessorAttemptId),
  'retry predecessor was spliced into another scenario')
  const orphan = receipts[5]
  assert(!receipts.some((receipt, index) => index !== 5 &&
    receipt.attemptId === orphan.scenarioProof.interruptedAttemptId),
  'orphan interrupted Attempt was spliced into another scenario')
  const allAttempts = [
    ...receipts.map(({ attemptId }) => attemptId), retry.scenarioProof.predecessorAttemptId,
    orphan.scenarioProof.interruptedAttemptId,
  ]
  assertUnique(allAttempts, 'recovery explicit predecessor/successor Attempt identities')
}

function assertTupleBinding(tuple, expected) {
  assert(tuple.identity === expected.tupleIdentity && tuple.environmentId === expected.environmentId &&
    tuple.installId === expected.installId && tuple.generationId === expected.generationId,
  'candidate tuple identity/binding drifted')
}

function validatePhaseCReceipt(value) {
  assertPlainObject(value, 'phase-C receipt')
  assertExactKeys(value, [
    'schemaVersion', 'tupleIdentity', 'phase', 'sequence', 'status', 'operationDigest',
    'observationDigest', 'previousReceiptDigest', 'receiptDigest',
  ], 'phase-C receipt')
  const { receiptDigest, ...safe } = value
  assert(value.schemaVersion === 'chora.m1-o4-candidate-phase-receipt.v1' && value.phase === 'C' &&
    value.sequence === 7 && value.status === 'passed' &&
    value.receiptDigest === sha256(canonicalJSONStringify(safe)),
  'phase-C receipt prefix/digest drifted')
  for (const field of ['tupleIdentity', 'operationDigest', 'observationDigest', 'previousReceiptDigest', 'receiptDigest']) {
    assertDigest(value[field], `phase-C receipt ${field}`)
  }
  return value
}

function validateFaultAuthority(value) {
  assertPlainObject(value, 'O4 fault authority')
  assertExactKeys(value, [
    'schemaVersion', 'status', 'scope', 'tupleIdentity', 'generationId', 'environmentId',
    'installId', 'binarySha256', 'sourceAggregateSha256', 'dataRootSha256', 'policySha256',
    'scenarios', 'claimBoundary',
  ], 'O4 fault authority')
  assert(value.schemaVersion === 'chora.m1-o4-fault-authority.v1' && value.status === 'authorized' &&
    value.scope === 'o4_acceptance_only', 'O4 fault authority schema/status/scope drifted')
  assert(Array.isArray(value.scenarios) && value.scenarios.length === 2,
    'O4 fault authority scenario count drifted')
  const expected = [
    ['failure', 'force_exact_managed_attempt_nonzero_after_started_v1'],
    ['timeout', 'accelerated_managed_attempt_deadline_v1'],
  ]
  for (let index = 0; index < 2; index++) {
    const scenario = value.scenarios[index]
    assertPlainObject(scenario, 'O4 fault authority scenario')
    assertExactKeys(scenario, [
      'scenario', 'action', 'taskId', 'snapshotId', 'snapshotDigest', 'attemptSequence',
      'agentExecutionProfile',
    ], 'O4 fault authority scenario')
    assert(scenario.scenario === expected[index][0] && scenario.action === expected[index][1] &&
      scenario.attemptSequence === 1 && scenario.agentExecutionProfile === 'standard',
    'O4 fault authority scenario order/action drifted')
    assertOpaque(scenario.taskId, productIDs.taskId, 'O4 fault authority Task ID')
    assertDigest(scenario.snapshotDigest, 'O4 fault authority Snapshot digest')
  }
  assertUnique(value.scenarios.map(({ taskId }) => taskId), 'O4 fault authority Task IDs')
  assertSameJSON(value.claimBoundary, [
    'typed_failure_projection', 'typed_timeout_projection', 'process_tree_death',
    'no_patch', 'no_automatic_retry', 'resource_cleanup',
  ], 'O4 fault authority claim boundary drifted')
  return value
}

function assertAuthorityBinding(authority, expected) {
  assert(authority.tupleIdentity === expected.tupleIdentity && authority.generationId === expected.generationId &&
    authority.environmentId === expected.environmentId && authority.installId === expected.installId,
  'O4 authority installed identity drifted')
}

function validateRestartReceipt(value, expectedSequence) {
  assertPlainObject(value, 'authorized restart receipt')
  assertExactKeys(value, [
    'schemaVersion', 'tupleIdentity', 'sequence', 'status', 'operationDigest',
    'observationDigest', 'previousReceiptDigest', 'setupReplayed', 'receiptDigest',
  ], 'authorized restart receipt')
  const { receiptDigest, ...safe } = value
  assert(value.schemaVersion === 'chora.m1-o4-candidate-restart-receipt.v1' && value.status === 'passed' &&
    value.sequence === expectedSequence && value.setupReplayed === false &&
    value.receiptDigest === sha256(canonicalJSONStringify(safe)),
  'authorized restart receipt chain/no-Setup drifted')
  return value
}

function validateAuthorityConsumption(value) {
  assertPlainObject(value, 'authority consumption')
  assertExactKeys(value, [
    'schemaVersion', 'status', 'authoritySha256', 'tupleIdentity', 'scenario', 'action',
    'runId', 'attemptId', 'invocationSha256', 'consumptionSha256',
  ], 'authority consumption')
  const { consumptionSha256, ...safe } = value
  assert(value.schemaVersion === 'chora.m1-o4-fault-consumption.v1' && value.status === 'consumed' &&
    value.consumptionSha256 === sha256(JSON.stringify(safe)),
  'authority consumption schema/status/digest drifted')
  for (const field of ['authoritySha256', 'tupleIdentity', 'invocationSha256', 'consumptionSha256']) {
    assertDigest(value[field], `authority consumption ${field}`)
  }
  assertOpaque(value.runId, productIDs.runId, 'authority consumption Run ID')
  assertOpaque(value.attemptId, productIDs.attemptId, 'authority consumption Attempt ID')
  return value
}

function assertConsumptionBindings(consumptions, authority, receipts) {
  assert(consumptions.length === 2, 'authority consumption count drifted')
  const authorityDigest = sha256(`${JSON.stringify(authority)}\n`)
  const expected = [
    ['failure', 'force_exact_managed_attempt_nonzero_after_started_v1', receipts[0]],
    ['timeout', 'accelerated_managed_attempt_deadline_v1', receipts[2]],
  ]
  for (let index = 0; index < 2; index++) {
    const [scenario, action, receipt] = expected[index]
    const consumption = consumptions[index]
    assert(consumption.scenario === scenario && consumption.action === action &&
      consumption.authoritySha256 === authorityDigest && consumption.tupleIdentity === receipt.tupleIdentity &&
      consumption.runId === receipt.runId && consumption.attemptId === receipt.attemptId,
    `authority consumption ${scenario} binding drifted`)
    assert(authority.scenarios[index].taskId === receipt.taskId,
      `authority ${scenario} Task binding drifted`)
  }
}

function validateResourceLedger(value) {
  assertPlainObject(value, 'recovery resource ledger')
  assertExactKeys(value, [
    'schemaVersion', 'status', 'sequence', 'scenario', 'tupleIdentity', 'generationId',
    'taskId', 'runId', 'attemptId', 'workspaceId', 'terminalResidue', 'ledgerDigest',
  ], 'recovery resource ledger')
  const { ledgerDigest, ...safe } = value
  assert(value.schemaVersion === RECOVERY_RESIDUE_RESOURCE_SCHEMA && value.status === 'terminal' &&
    value.ledgerDigest === sha256(canonicalJSONStringify(safe)),
  'recovery resource ledger schema/status/digest drifted')
  validateZeroResidue(value.terminalResidue, 'recovery resource ledger residue')
  return value
}

function assertResourceBindings(resources, expected, receipts) {
  assert(resources.length === receipts.length, 'recovery resource ledger count drifted')
  for (let index = 0; index < receipts.length; index++) {
    const resource = resources[index]
    const receipt = receipts[index]
    assert(resource.sequence === receipt.sequence && resource.scenario === receipt.name &&
      resource.tupleIdentity === expected.tupleIdentity && resource.generationId === expected.generationId &&
      resource.taskId === receipt.taskId && resource.runId === receipt.runId &&
      resource.attemptId === receipt.attemptId && resource.workspaceId === receipt.workspaceId,
    `recovery ${receipt.name} resource ledger binding drifted`)
  }
}

function validateGenerationReference(value) {
  assertPlainObject(value, 'recovery generation reference')
  assertExactKeys(value, [
    'schemaVersion', 'status', 'sequence', 'scenario', 'generationId', 'tupleIdentity',
    'servingCount', 'activeAttemptCount', 'recoverableAttemptCount', 'currentGenerationSha256',
    'referenceDigest',
  ], 'recovery generation reference')
  const { referenceDigest, ...safe } = value
  assert(value.schemaVersion === RECOVERY_RESIDUE_GENERATION_SCHEMA && value.status === 'terminal' &&
    value.servingCount === 1 && value.activeAttemptCount === 0 && value.recoverableAttemptCount === 0 &&
    value.referenceDigest === sha256(canonicalJSONStringify(safe)),
  'recovery generation reference status/count/digest drifted')
  return value
}

function assertGenerationBindings(references, expected, receipts) {
  for (let index = 0; index < receipts.length; index++) {
    const reference = references[index]
    const receipt = receipts[index]
    assert(reference.sequence === receipt.sequence && reference.scenario === receipt.name &&
      reference.generationId === expected.generationId && reference.tupleIdentity === expected.tupleIdentity &&
      reference.currentGenerationSha256 === sha256(expected.generationId),
    `recovery ${receipt.name} generation reference drifted`)
  }
}

function validatePublicResidue(value) {
  assertPlainObject(value, 'public O4 residue')
  assertExactKeys(value, [
    'schemaVersion', 'status', 'authoritySha256', 'tupleIdentity', 'docker',
    'verificationWorkspaces', 'generationReferences',
  ], 'public O4 residue')
  assert(value.schemaVersion === 'chora.m1-o4-residue-proof.v1' && value.status === 'proven_zero',
    'public O4 residue schema/status drifted')
  assertDigest(value.authoritySha256, 'public O4 residue authority digest')
  assertDigest(value.tupleIdentity, 'public O4 residue tuple identity')
  validatePublicResidueCounts(value)
  return value
}

function validatePublicResidueCounts(value) {
  assertPlainObject(value.docker, 'public O4 Docker residue')
  const dockerKeys = [
    'schemaVersion', 'status', 'engineQualificationSha256', 'runtimeScopeSha256',
    'containers', 'verifierContainers', 'networks', 'volumes', 'configs',
    'managedWorkspaces', 'attemptProcessGroups', 'swarmInactive', 'servingProcessExcluded',
    'operationLedgerSettled', 'operationLedgerCount', 'operationLedgerSha256',
  ]
  assertExactKeys(value.docker, dockerKeys, 'public O4 Docker residue')
  assert(value.docker.schemaVersion === 'chora.m1-o4-residue-observation.v1' &&
    value.docker.status === 'proven_zero' && value.docker.swarmInactive === true &&
    value.docker.servingProcessExcluded === true && value.docker.operationLedgerSettled === true &&
    Number.isSafeInteger(value.docker.operationLedgerCount) && value.docker.operationLedgerCount >= 0,
  'public O4 Docker residue proof metadata drifted')
  for (const field of ['engineQualificationSha256', 'runtimeScopeSha256', 'operationLedgerSha256']) {
    assertDigest(value.docker[field], `public O4 Docker ${field}`)
  }
  for (const key of [
    'containers', 'verifierContainers', 'networks', 'volumes', 'configs',
    'managedWorkspaces', 'attemptProcessGroups',
  ]) validateZeroAggregate(value.docker[key], `public O4 Docker ${key}`)
  validateZeroAggregate(value.verificationWorkspaces, 'public O4 Verifier workspaces')
  assertPlainObject(value.generationReferences, 'public O4 generation references')
  assertExactKeys(value.generationReferences, [
    'serving', 'activeAttempts', 'recoverableAttempts', 'currentGenerationSha256',
  ], 'public O4 generation references')
  assert(value.generationReferences.serving?.count === 1,
    'public O4 serving generation count drifted')
  assertDigest(value.generationReferences.serving?.aggregateSha256,
    'public O4 serving generation aggregate')
  validateZeroAggregate(value.generationReferences.activeAttempts, 'public O4 active references')
  validateZeroAggregate(value.generationReferences.recoverableAttempts, 'public O4 recoverable references')
  assertDigest(value.generationReferences.currentGenerationSha256, 'public O4 current generation digest')
}

function validateZeroAggregate(value, label) {
  assertPlainObject(value, label)
  assertExactKeys(value, ['count', 'aggregateSha256'], label)
  assert(value.count === 0, `${label} is non-zero`)
  assertDigest(value.aggregateSha256, `${label} aggregate`)
}

function validateServiceIdentity(value, label) {
  assertPlainObject(value, label)
  assertExactKeys(value, [
    'pid', 'startIdentitySha256', 'binarySha256', 'tupleIdentity', 'generationId',
    'readiness', 'currentGeneration',
  ], label)
  assert(Number.isSafeInteger(value.pid) && value.pid > 1, `${label} PID is invalid`)
  for (const field of ['startIdentitySha256', 'binarySha256', 'tupleIdentity']) {
    assertDigest(value[field], `${label} ${field}`)
  }
  assertOpaque(value.generationId, opaqueInstallRE, `${label} generation ID`)
  assert(['ready', 'terminated'].includes(value.readiness) && typeof value.currentGeneration === 'boolean',
    `${label} readiness/current-generation state is invalid`)
}

function assertServiceControlBinding(control, shared, authorizedRestartInput, orphanRestartInput) {
  assert(control.tupleIdentity === shared.tupleIdentity && control.generationId === shared.generationId,
    'service-control tuple/generation drifted')
  assert(control.restartReceipt.file !== authorizedRestartInput.index.file &&
    control.restartReceipt.rawSha256 !== authorizedRestartInput.index.rawSha256,
  'service-control restart was spliced with the initial authorized restart')
  assertIndexMatches(control.restartReceipt, orphanRestartInput.index,
    'service-control actual orphan restart receipt')
  const orphanDigest = control.digest
  assertDigest(orphanDigest, 'service-control/orphan digest')
}

async function readScreenshotIndexes(paths, receipts) {
  const pngFiles = await exactIndexedFiles(paths.screenshotDirectory,
    RECOVERY_RESIDUE_SCREENSHOT_NAMES, 'recovery screenshot directory', '.png')
  const metadataNames = RECOVERY_RESIDUE_SCREENSHOT_NAMES.map((name) => name.replace(/\.png$/, '-metadata.json'))
  const ocrNames = RECOVERY_RESIDUE_SCREENSHOT_NAMES.map((name) => name.replace(/\.png$/, '-ocr.json'))
  const metadataFiles = await exactIndexedFiles(paths.screenshotMetadataDirectory,
    metadataNames, 'recovery screenshot metadata directory')
  const ocrFiles = await exactIndexedFiles(paths.screenshotOCRDirectory,
    ocrNames, 'recovery screenshot OCR directory')
  const result = []
  for (let index = 0; index < pngFiles.length; index++) {
    const pngBytes = await readExactFile(pngFiles[index], `recovery screenshot ${index + 1}`, maxPNG, 0o400)
    const metadata = await semanticJSONIndex(metadataFiles[index], `recovery screenshot metadata ${index + 1}`,
      (value) => validateScreenshotMetadata(value, pngBytes))
    const ocr = await semanticJSONIndex(ocrFiles[index], `recovery screenshot OCR ${index + 1}`,
      (value) => validateScreenshotOCR(value, pngBytes))
    assert(ocr.value.engineBindingSha256 === paths.ocrEngineBindingSha256 &&
      ocr.value.executableSha256 === paths.ocrExecutableSha256,
    `recovery screenshot OCR ${index + 1} engine/executable pin drifted`)
    assert(ocr.value.recognizedText.includes(receipts[index].name),
      `recovery screenshot OCR ${index + 1} does not bind its visible scenario`)
    result.push({
      png: { file: basename(pngFiles[index]), rawSha256: sha256(pngBytes), semanticDigest: receipts[index].publicObservationDigest },
      metadata: metadata.index, ocr: ocr.index,
      ocrInvocation: ocrInvocationAudit(paths, pngFiles[index], ocrFiles[index]),
    })
  }
  return result
}

function validateScreenshotIndex(value) {
  assert(Array.isArray(value) && value.length === RECOVERY_RESIDUE_SCENARIOS.length,
    'recovery screenshot index count drifted')
  for (let index = 0; index < value.length; index++) {
    const entry = value[index]
    assertPlainObject(entry, 'recovery screenshot index entry')
    assertExactKeys(entry, ['png', 'metadata', 'ocr', 'ocrInvocation'], 'recovery screenshot index entry')
    validateSingleIndex(entry.png, RECOVERY_RESIDUE_SCREENSHOT_NAMES[index], 'recovery screenshot PNG index')
    validateSingleIndex(entry.metadata,
      RECOVERY_RESIDUE_SCREENSHOT_NAMES[index].replace(/\.png$/, '-metadata.json'),
      'recovery screenshot metadata index')
    validateSingleIndex(entry.ocr,
      RECOVERY_RESIDUE_SCREENSHOT_NAMES[index].replace(/\.png$/, '-ocr.json'),
      'recovery screenshot OCR index')
    validateOCRInvocationAudit(entry.ocrInvocation)
  }
  for (const field of [
    'protocol', 'executablePathSha256', 'executableSha256', 'engineBindingPathSha256',
    'engineBindingSha256', 'sandboxExecutablePathSha256', 'sandboxExecutableSha256',
    'sandboxProfilePathSha256', 'sandboxProfileSha256', 'language', 'networkDisabled',
    'sandboxEnforced', 'modelBytes',
  ]) {
    assert(new Set(value.map((entry) => entry.ocrInvocation[field])).size === 1,
      `recovery screenshot OCR ${field} pin drifted across scenarios`)
  }
  assertUnique(value.map((entry) => entry.ocrInvocation.argvSha256),
    'recovery screenshot OCR argv digests')
}

function ocrInvocationAudit(paths, pngFile, outputFile) {
  const ocrArgv = [
    '--protocol', RECOVERY_RESIDUE_OCR_PROTOCOL, '--engine-binding', paths.ocrEngineBindingFile,
    '--language', paths.ocrLanguage, '--input', pngFile, '--output', outputFile,
    '--network', 'disabled',
  ]
  const argv = ['-f', paths.ocrSandboxProfileFile, paths.ocrExecutable, ...ocrArgv]
  return {
    protocol: RECOVERY_RESIDUE_OCR_PROTOCOL,
    executablePathSha256: sha256(paths.ocrExecutable),
    executableSha256: paths.ocrExecutableSha256,
    engineBindingPathSha256: sha256(paths.ocrEngineBindingFile),
    engineBindingSha256: paths.ocrEngineBindingSha256,
    sandboxExecutablePathSha256: sha256(paths.ocrSandboxExecutable),
    sandboxExecutableSha256: paths.ocrSandboxExecutableSha256,
    sandboxProfilePathSha256: sha256(paths.ocrSandboxProfileFile),
    sandboxProfileSha256: paths.ocrSandboxProfileSha256,
    language: paths.ocrLanguage,
    argvSha256: sha256(canonicalJSONStringify(argv)),
    networkDisabled: true, sandboxEnforced: true, modelBytes: 'opaque_unavailable',
  }
}

function validateOCRInvocationAudit(value) {
  assertPlainObject(value, 'recovery screenshot OCR invocation audit')
  assertExactKeys(value, [
    'protocol', 'executablePathSha256', 'executableSha256', 'engineBindingPathSha256',
    'engineBindingSha256', 'sandboxExecutablePathSha256', 'sandboxExecutableSha256',
    'sandboxProfilePathSha256', 'sandboxProfileSha256', 'language', 'argvSha256',
    'networkDisabled', 'sandboxEnforced', 'modelBytes',
  ], 'recovery screenshot OCR invocation audit')
  assert(value.protocol === RECOVERY_RESIDUE_OCR_PROTOCOL && value.language === 'eng' &&
    value.networkDisabled === true && value.sandboxEnforced === true &&
    value.modelBytes === 'opaque_unavailable',
  'recovery screenshot OCR protocol/language/network drifted')
  for (const field of [
    'executablePathSha256', 'executableSha256', 'engineBindingPathSha256',
    'engineBindingSha256', 'sandboxExecutablePathSha256', 'sandboxExecutableSha256',
    'sandboxProfilePathSha256', 'sandboxProfileSha256', 'argvSha256',
  ]) assertDigest(value[field], `recovery screenshot OCR ${field}`)
}

function validateSharedImages(value) {
  assert(Array.isArray(value) && value.length === 2, 'recovery shared image count drifted')
  const expected = [
    ['managed-pi-runtime', ['managed_pi_runtime', 'independent_verifier', 'capability_probe']],
    ['network-boundary', ['network_boundary']],
  ]
  for (let index = 0; index < 2; index++) {
    const image = value[index]
    assertPlainObject(image, 'recovery shared image')
    assertExactKeys(image, [
      'artifactId', 'roles', 'archiveSha256', 'archiveSize', 'dockerConfigImageId', 'present',
    ], 'recovery shared image')
    assert(image.artifactId === expected[index][0] && image.present === true,
      'recovery shared image identity/presence drifted')
    assertSameJSON(image.roles, expected[index][1], 'recovery shared image roles drifted')
    assertDigest(image.archiveSha256, 'recovery shared image archive digest')
    assert(Number.isSafeInteger(image.archiveSize) && image.archiveSize > 0 &&
      imageDigestRE.test(image.dockerConfigImageId), 'recovery shared image binding drifted')
  }
  assert(value[0].dockerConfigImageId !== value[1].dockerConfigImageId,
    'recovery shared managed/network images are not distinct')
}

async function semanticLedgerIndex(path, label, receiptInputs, verifier) {
  const input = await semanticJSONIndex(path, label, (value) => {
    assertPlainObject(value, label)
    assert(value.status === 'recording' || value.status === 'sealed', `${label} status drifted`)
    assert(Array.isArray(value.attempts), `${label} attempts are missing`)
    return value
  })
  const ranges = []
  for (const { value: receipt } of receiptInputs) {
    const range = verifier ? receipt.verifierSource : receipt.sourceA3
    if (range === null) continue
    ranges.push({ scenario: receipt.name, ...clone(range) })
  }
  validateSourceRangesAgainstLedger(ranges, input.value, label)
  return {
    value: input.value,
    index: { ...input.index, ranges },
  }
}

function validateSourceRangesAgainstLedger(ranges, ledger, label) {
  assert(ranges.length > 0, `${label} ranges are missing`)
  let previousEnd = ranges[0].startSequence - 1
  let previousSlice = null
  for (const range of ranges) {
    assert(range.startSequence === previousEnd + 1,
      `${label} ranges overlap, skip, or do not form an exact chain`)
    assert(range.endSequence <= ledger.attempts.length,
      `${label} range exceeds the source ledger`)
    const slice = ledger.attempts.slice(range.startSequence - 1, range.endSequence)
    assert(range.sliceDigest === sha256(canonicalJSONStringify(slice)),
      `${label} range slice digest drifted`)
    assert(range.sliceDigest !== previousSlice, `${label} repeated a source slice`)
    previousEnd = range.endSequence
    previousSlice = range.sliceDigest
  }
  assert(previousEnd === ledger.attempts.length, `${label} ranges do not cover the exact ledger`)
}

function validateRangeIndex(value, label, allowAbsent) {
  if (allowAbsent && value?.absent === true) {
    assertExactKeys(value, ['absent', 'reason'], label)
    assert(value.reason === 'no_verifier_for_terminal_pre_patch_scenarios', `${label} absence reason drifted`)
    return
  }
  assertPlainObject(value, label)
  assertExactKeys(value, [...fileIndexKeys, 'ranges'], label)
  validateFileIndexEntry(value, label, ['ranges'])
  assert(Array.isArray(value.ranges) && value.ranges.length > 0, `${label} ranges are missing`)
  let previousEnd = value.ranges[0].startSequence - 1
  for (const range of value.ranges) {
    assertPlainObject(range, `${label} range`)
    assertExactKeys(range, ['scenario', ...sourceRangeKeys], `${label} range`)
    assert(RECOVERY_RESIDUE_SCENARIOS.some(({ name }) => name === range.scenario),
      `${label} range scenario drifted`)
    validateSourceRange(range, `${label} range`, ['scenario'])
    assert(range.startSequence === previousEnd + 1, `${label} ranges overlap or skip`)
    previousEnd = range.endSequence
  }
}

function validateSingleIndex(value, expectedName, label) {
  validateFileIndexEntry(value, label)
  assert(value.file === expectedName, `${label} filename drifted`)
}

function validateFileIndex(value, expectedNames, label) {
  assert(Array.isArray(value) && value.length === expectedNames.length, `${label} count drifted`)
  for (let index = 0; index < expectedNames.length; index++) {
    validateSingleIndex(value[index], expectedNames[index], `${label} entry`)
  }
  for (const field of fileIndexKeys) assertUnique(value.map((entry) => entry[field]), `${label} ${field}`)
}

function validateFileIndexEntry(value, label, extraKeys = []) {
  assertPlainObject(value, label)
  assertExactKeys(value, [...fileIndexKeys, ...extraKeys], label)
  assert(typeof value.file === 'string' && basename(value.file) === value.file && value.file !== '.' && value.file !== '..',
    `${label} file is invalid`)
  assertDigest(value.rawSha256, `${label} raw SHA-256`)
  assertDigest(value.semanticDigest, `${label} semantic digest`)
}

function assertIndexMatches(actual, expected, label) {
  assertSameJSON(actual, expected, `${label} raw/semantic index drifted`)
}

async function semanticJSONIndex(path, label, validator) {
  const bytes = await readExactFile(path, label, maxJSON, 0o400)
  const value = validator(parseJSON(bytes, label))
  return { value, index: { file: basename(path), rawSha256: sha256(bytes), semanticDigest: semanticDigest(value) } }
}

function semanticDigest(value) {
  for (const field of ['receiptDigest', 'manifestDigest', 'aggregateDigest', 'digest', 'ledgerDigest', 'referenceDigest', 'consumptionSha256']) {
    if (value && digestRE.test(value[field])) return value[field]
  }
  return sha256(canonicalJSONStringify(value))
}

async function fileIndexFor(path, semantic) {
  const bytes = await readExactFile(path, 'indexed recovery file', maxJSON, 0o400)
  assertDigest(semantic, 'indexed recovery semantic digest')
  return Object.freeze({ file: basename(path), rawSha256: sha256(bytes), semanticDigest: semantic })
}

async function readSemanticDirectory(directory, names, label, validator) {
  const files = await exactIndexedFiles(directory, names, `${label} directory`)
  const result = []
  for (let index = 0; index < files.length; index++) {
    result.push(await semanticJSONIndex(files[index], `${label} ${index + 1}`, validator))
  }
  return result
}

async function exactIndexedFiles(directory, names, label, suffix = '.json') {
  await assertOwnerDirectory(directory)
  const actual = (await readdir(directory)).filter((name) => name.endsWith(suffix)).sort()
  assertSameJSON(actual, [...names].sort(), `${label} contains missing or extra files`)
  return names.map((name) => join(directory, name))
}

function validateSourceRange(value, label, extraKeys = []) {
  assertPlainObject(value, label)
  assertExactKeys(value, [...sourceRangeKeys, ...extraKeys], label)
  assert(Number.isSafeInteger(value.startSequence) && value.startSequence > 0 &&
    Number.isSafeInteger(value.endSequence) && value.endSequence >= value.startSequence,
  `${label} range is invalid`)
  assertDigest(value.sliceDigest, `${label} slice digest`)
}

function validateZeroResidue(value, label) {
  assertPlainObject(value, label)
  assertExactKeys(value, zeroResidueKeys, label)
  for (const key of zeroResidueKeys) assert(value[key] === 0, `${label} ${key} is non-zero`)
}

function validateSharedIdentity(value, label) {
  assertOpaque(value.environmentId, environmentRE, `${label} environment ID`)
  assertOpaque(value.installId, opaqueInstallRE, `${label} install ID`)
  assertOpaque(value.generationId, opaqueInstallRE, `${label} generation ID`)
  assertDigest(value.bindingDigest, `${label} binding digest`)
  assertDigest(value.tupleIdentity, `${label} tuple identity`)
}

function validateRecordIdentity(value) {
  assertOpaque(value.environmentId, environmentRE, 'recovery record environment ID')
  assertOpaque(value.installId, opaqueInstallRE, 'recovery record install ID')
  assertOpaque(value.generationId, opaqueInstallRE, 'recovery record generation ID')
  assertDigest(value.bindingDigest, 'recovery record binding digest')
}

function validateProductIDs(value, label) {
  for (const [field, pattern] of Object.entries(productIDs)) {
    assertOpaque(value[field], pattern, `${label} ${field}`)
  }
}

function assertReceiptBinding(receipt, expected) {
  assert(receipt.environmentId === expected.environmentId && receipt.installId === expected.installId &&
    receipt.generationId === expected.generationId && receipt.bindingDigest === expected.bindingDigest &&
    receipt.tupleIdentity === expected.tupleIdentity,
  `recovery ${receipt.name} installed identity drifted`)
}

function assertSameIdentity(left, right, label) {
  for (const field of ['environmentId', 'installId', 'generationId']) {
    assert(left[field] === right[field], `${label} ${field} drifted`)
  }
}

async function validatePinnedExecutable(path, expectedSha256, label) {
  assertDigest(expectedSha256, `${label} expected digest`)
  const bytes = await readExactFile(path, label, 64 * 1024 * 1024)
  const info = await lstat(path)
  assert((info.mode & 0o777) === 0o500, `${label} mode must be 0500`)
  assert(sha256(bytes) === expectedSha256, `${label} digest drifted`)
}

async function validatePinnedSystemExecutable(path, expectedSha256, label) {
  exactAbsolutePath(path, label)
  await assertNoSymlinkAncestors(path)
  assertDigest(expectedSha256, `${label} expected digest`)
  const info = await lstat(path)
  assert(info.isFile() && !info.isSymbolicLink() && info.nlink === 1 && info.uid === 0 &&
    (info.mode & 0o777) === 0o755 && info.size > 0 && info.size <= 2 * 1024 * 1024,
  `${label} is not one pinned root-owned 0755 system executable`)
  const bytes = await readFile(path)
  assert(bytes.length === info.size && sha256(bytes) === expectedSha256,
    `${label} digest drifted`)
  return `${info.dev}:${info.ino}`
}

function validateOCRSandboxProfile(bytes) {
  assert(Buffer.isBuffer(bytes) && bytes.length > 0 && bytes.length <= maxJSON,
    'OCR sandbox profile is invalid')
  const lines = bytes.toString('utf8').split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
  assert(lines[0] === '(version 1)' && lines.includes('(deny network*)') &&
    !lines.some((line) => /^\(allow\s+network/.test(line)),
  'OCR sandbox profile does not enforce deny network*')
}

function validateOCREngineBinding(value, expected) {
  assertPlainObject(value, 'system-managed OCR engine binding')
  assertExactKeys(value, ['schemaVersion', 'status', 'engine', 'modelBytes', 'platform',
    'vision', 'executable', 'sandbox', 'bindingDigest'], 'system-managed OCR engine binding')
  assert(value.schemaVersion === OCR_ENGINE_BINDING_SCHEMA && value.status === 'authorized' &&
    value.engine === 'system_managed_ocr_engine' && value.modelBytes === 'opaque_unavailable',
  'system-managed OCR engine schema/status/model boundary drifted')
  assertPlainObject(value.platform, 'system-managed OCR platform')
  assertExactKeys(value.platform, ['os', 'architecture', 'macOSProductVersion', 'macOSBuildVersion',
    'darwinSysname', 'darwinRelease', 'darwinVersion', 'darwinMachine'],
  'system-managed OCR platform')
  assert(value.platform.os === 'darwin' && value.platform.architecture === 'arm64' &&
    value.platform.darwinSysname === 'Darwin', 'system-managed OCR platform drifted')
  for (const field of ['macOSProductVersion', 'macOSBuildVersion', 'darwinRelease',
    'darwinVersion', 'darwinMachine']) assert(typeof value.platform[field] === 'string' &&
    value.platform[field].length > 0, `system-managed OCR platform ${field} is invalid`)
  assertPlainObject(value.vision, 'system-managed OCR Vision binding')
  assertExactKeys(value.vision, ['bundleIdentifier', 'bundleVersion', 'infoPlistSha256',
    'supportedRecognitionRevisions', 'requestedRecognitionRevision', 'recognitionLevel',
    'recognitionLanguage', 'usesLanguageCorrection', 'confidenceThreshold'],
  'system-managed OCR Vision binding')
  assert(typeof value.vision.bundleIdentifier === 'string' && value.vision.bundleIdentifier.length > 0 &&
    typeof value.vision.bundleVersion === 'string' && value.vision.bundleVersion.length > 0,
  'system-managed OCR Vision bundle identity is invalid')
  assertDigest(value.vision.infoPlistSha256, 'system-managed OCR Vision Info.plist')
  assert(Array.isArray(value.vision.supportedRecognitionRevisions) &&
    value.vision.supportedRecognitionRevisions.length > 0 &&
    value.vision.supportedRecognitionRevisions.every((item, index, values) =>
      Number.isSafeInteger(item) && item > 0 && (index === 0 || item > values[index - 1])) &&
    value.vision.supportedRecognitionRevisions.includes(value.vision.requestedRecognitionRevision) &&
    value.vision.recognitionLevel === 'accurate' && value.vision.recognitionLanguage === 'en-US' &&
    value.vision.usesLanguageCorrection === true && typeof value.vision.confidenceThreshold === 'number' &&
    value.vision.confidenceThreshold > 0 && value.vision.confidenceThreshold <= 1,
  'system-managed OCR Vision settings drifted')
  assertPlainObject(value.executable, 'system-managed OCR executable binding')
  assertExactKeys(value.executable, ['pathSha256', 'sha256'], 'system-managed OCR executable binding')
  assert(value.executable.pathSha256 === sha256(expected.ocrExecutable) &&
    value.executable.sha256 === expected.ocrExecutableSha256,
  'system-managed OCR executable binding drifted')
  assertPlainObject(value.sandbox, 'system-managed OCR sandbox binding')
  assertExactKeys(value.sandbox, ['executableFile', 'executablePathSha256', 'executableSha256',
    'profileFile', 'profilePathSha256', 'profileSha256', 'networkRule'],
  'system-managed OCR sandbox binding')
  assert(value.sandbox.executableFile === expected.ocrSandboxExecutable &&
    value.sandbox.executablePathSha256 === sha256(expected.ocrSandboxExecutable) &&
    value.sandbox.executableSha256 === expected.ocrSandboxExecutableSha256 &&
    value.sandbox.profileFile === expected.ocrSandboxProfileFile &&
    value.sandbox.profilePathSha256 === sha256(expected.ocrSandboxProfileFile) &&
    value.sandbox.profileSha256 === expected.ocrSandboxProfileSha256 &&
    value.sandbox.networkRule === 'deny network*',
  'system-managed OCR sandbox binding drifted')
  assertDigest(value.bindingDigest, 'system-managed OCR engine binding digest')
  const { bindingDigest, ...safe } = value
  assert(bindingDigest === sha256(canonicalJSONStringify(safe)),
    'system-managed OCR engine binding digest drifted')
  return value
}

async function runPinnedTool(executable, argv, timeoutMs, outputLimit, label) {
  assert(Number.isSafeInteger(timeoutMs) && timeoutMs >= 100 && timeoutMs <= 60_000,
    `${label} timeout is outside the bounded protocol`)
  assert(Array.isArray(argv) && argv.every((value) => typeof value === 'string') &&
    argv.includes('--network') && argv.includes('disabled'), `${label} argv/network protocol drifted`)
  await new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(executable, argv, {
      cwd: '/', shell: false, detached: true, stdio: ['ignore', 'pipe', 'pipe'],
      env: { LANG: 'C', LC_ALL: 'C', PATH: '/usr/bin:/bin', CHORA_NETWORK: 'disabled' },
    })
    let stdout = Buffer.alloc(0)
    let stderr = Buffer.alloc(0)
    let failure = null
    let timer
    const fail = (error) => {
      if (failure === null) failure = error
      killProcessGroup(child.pid)
    }
    const append = (current, chunk, stream) => {
      const next = Buffer.concat([current, chunk])
      if (next.length > outputLimit) fail(new Error(`${label} ${stream} exceeded the bounded protocol`))
      return next
    }
    child.stdout.on('data', (chunk) => { stdout = append(stdout, chunk, 'stdout') })
    child.stderr.on('data', (chunk) => { stderr = append(stderr, chunk, 'stderr') })
    child.on('error', fail)
    child.on('close', (code, signal) => {
      clearTimeout(timer)
      void (async () => {
        try {
          if (failure === null && (code !== 0 || signal !== null || stdout.length !== 0 || stderr.length !== 0)) {
            failure = new Error(`${label} did not complete the silent exit-zero protocol`)
          }
          if (failure === null && processGroupExists(child.pid)) {
            failure = new Error(`${label} left a descendant process`)
          }
          if (failure !== null) killProcessGroup(child.pid)
          await waitForProcessGroupDeath(child.pid, label)
        } catch (error) {
          failure = failure === null ? error :
            new Error(`${failure.message}; process-group termination proof failed: ${error.message}`, { cause: error })
        }
        if (failure) rejectPromise(failure)
        else resolvePromise()
      })()
    })
    timer = setTimeout(() => fail(new Error(`${label} timed out`)), timeoutMs)
  })
}

function killProcessGroup(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 1) return
  try { process.kill(-pid, 'SIGKILL') } catch (error) { if (error.code !== 'ESRCH') throw error }
}

function processGroupExists(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 1) return false
  try { process.kill(-pid, 0); return true } catch (error) { if (error.code === 'ESRCH') return false; throw error }
}

async function waitForProcessGroupDeath(pid, label) {
  const deadline = Date.now() + 2_000
  while (processGroupExists(pid)) {
    if (Date.now() >= deadline) throw new Error(`${label} process group did not terminate`)
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 10))
  }
}

async function phaseDReceiptState(receiptDirectory) {
  exactAbsolutePath(receiptDirectory, 'phase receipt directory')
  await assertOwnerDirectory(receiptDirectory)
  const names = await readdir(receiptDirectory)
  return { count: names.filter((name) => /^08-D\.json$/.test(name)).length }
}

async function writeExclusiveOwnerReadonlyJSON(value, path, label, capabilityValue) {
  const capability = validateOwnedPathCapability(capabilityValue, `${label} owned-path capability`)
  exactAbsolutePath(path, label)
  await assertNoSymlinkAncestors(path)
  await assertOwnerDirectory(dirname(path))
  let handle
  let createdIdentity = null
  const bytes = Buffer.from(`${JSON.stringify(value, null, 2)}\n`)
  try {
    handle = await open(path, 'wx', 0o600)
    createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true }))
    await handle.writeFile(bytes)
    await handle.sync()
    await handle.chmod(0o400)
    await handle.sync()
    createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true }))
    await handle.close()
    handle = undefined
    await verifyCreatedReadonlyFile(path, label, bytes, createdIdentity)
    return createdIdentity
  } catch (error) {
    const cleanupErrors = []
    if (handle) {
      try { createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true })) }
      catch (identityError) { cleanupErrors.push(identityError) }
      try { await handle.close() } catch (cleanupError) { cleanupErrors.push(cleanupError) }
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

async function verifyCreatedReadonlyFile(path, label, expectedBytes, identity) {
  const handle = await open(path, safeReadFlags)
  try {
    const before = await handle.stat({ bigint: true })
    assert(before.isFile() && !before.isSymbolicLink() && before.nlink === 1n &&
      before.uid === BigInt(process.getuid()) && Number(before.mode & 0o777n) === 0o400 &&
      before.dev.toString() === identity.dev && before.ino.toString() === identity.ino &&
      Number(before.mode) === identity.mode && Number(before.uid) === identity.uid &&
      Number(before.gid) === identity.gid,
    `${label} final opened identity drifted`)
    const observedBytes = await handle.readFile()
    const after = await handle.stat({ bigint: true })
    const pathAfter = await lstat(path, { bigint: true })
    assert(after.dev === before.dev && after.ino === before.ino && after.mode === before.mode &&
      after.uid === before.uid && after.gid === before.gid && after.nlink === before.nlink &&
      pathAfter.dev === before.dev && pathAfter.ino === before.ino &&
      pathAfter.mode === before.mode && pathAfter.uid === before.uid && pathAfter.gid === before.gid &&
      observedBytes.equals(expectedBytes),
    `${label} final path or bytes changed after creation`)
  } finally { await handle.close() }
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

async function assertExactRegularFile(path, label, maxBytes) {
  await readExactFile(path, label, maxBytes)
  const info = await lstat(path)
  return `${info.dev}:${info.ino}`
}

async function assertOwnerDirectory(path) {
  exactAbsolutePath(path, 'recovery/residue directory')
  await assertNoSymlinkAncestors(path)
  const info = await lstat(path)
  assert(info.isDirectory() && !info.isSymbolicLink() && info.uid === process.getuid() &&
    (info.mode & 0o777) === 0o700,
  'recovery/residue directory must be owner-owned 0700')
  return `${info.dev}:${info.ino}`
}

async function assertNoSymlinkAncestors(path) {
  let current = resolve(path)
  while (true) {
    try {
      const info = await lstat(current)
      assert(!info.isSymbolicLink(), 'recovery/residue path has a symlink ancestor')
    } catch (error) { if (error.code !== 'ENOENT') throw error }
    const parent = dirname(current)
    if (parent === current) return
    current = parent
  }
}

async function assertAbsent(path, label) {
  try { await lstat(path); throw new Error(`${label} already exists`) } catch (error) {
    if (error.code !== 'ENOENT') throw error
  }
}

function assertPairwiseNonOverlapping(paths) {
  assertUnique(paths, 'recovery/residue exact paths')
  for (let left = 0; left < paths.length; left++) {
    for (let right = left + 1; right < paths.length; right++) {
      assert(!pathInside(paths[left], paths[right]) && !pathInside(paths[right], paths[left]),
        'recovery/residue paths overlap')
    }
  }
}

function assertUniqueFileIdentities(identities, label) {
  assertUnique(identities, `${label} device/inode identities`)
}

function pathInside(parent, child) {
  const value = relative(parent, child)
  return value === '' || (!value.startsWith('..') && !isAbsolute(value))
}

function exactAbsolutePath(value, label) {
  assert(typeof value === 'string' && isAbsolute(value) && normalize(value) === value && resolve(value) === value,
    `${label} must be exact and absolute`)
}

function parsePNGChunkTypes(png) {
  const result = []
  let offset = 8
  while (offset + 12 <= png.length) {
    const length = png.readUInt32BE(offset)
    assert(length <= maxPNG && offset + 12 + length <= png.length, 'PNG chunk boundary is invalid')
    const type = png.subarray(offset + 4, offset + 8).toString('ascii')
    assert(/^[A-Za-z]{4}$/.test(type), 'PNG chunk type is invalid')
    result.push(type)
    offset += 12 + length
    if (type === 'IEND') break
  }
  assert(result[0] === 'IHDR' && result.at(-1) === 'IEND' && offset === png.length,
    'PNG chunk stream is incomplete or has trailing bytes')
  return result
}

function assertSafeEvidenceValue(value, label) {
  const text = canonicalJSONStringify(value)
  assert(text.length <= maxJSON, `${label} exceeds the bounded safe contract`)
  const forbidden = [
    /(?:^|[\W_])(fixture|synthetic|mock|fake)(?:[\W_]|$)/i,
    /(?:\/Users\/|\/home\/|\\Users\\)/,
    /https?:\/\/(?!127\.0\.0\.1(?::\d+)?(?:[\/"']|$))/i,
  ]
  assert(!forbidden.some((pattern) => pattern.test(text)), `${label} contains private or synthesized evidence`)
}

function assertTimestamp(value, label) {
  assert(typeof value === 'string' && Number.isFinite(Date.parse(value)), `${label} timestamp is invalid`)
}

function assertOpaque(value, pattern, label) {
  assert(typeof value === 'string' && pattern.test(value), `${label} is invalid`)
}

function assertDigest(value, label) { assert(typeof value === 'string' && digestRE.test(value), `${label} is invalid`) }
function assertUnique(values, label) { assert(new Set(values).size === values.length, `${label} are duplicated`) }
function assertSameJSON(actual, expected, message) { assert(canonicalJSONStringify(actual) === canonicalJSONStringify(expected), message) }
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
function parseJSON(bytes, label) {
  try { return JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) }
}
function clone(value) { return JSON.parse(JSON.stringify(value)) }
function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function assert(condition, message) { if (!condition) throw new Error(message) }
function deepFreeze(value) {
  if (value && typeof value === 'object' && !Object.isFrozen(value)) {
    Object.freeze(value)
    for (const child of Object.values(value)) deepFreeze(child)
  }
  return value
}

export function recoveryResidueNonce() { return randomBytes(32).toString('hex') }
