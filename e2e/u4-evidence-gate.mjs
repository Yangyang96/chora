import { lstat, open, readFile, readdir } from 'node:fs/promises'
import { basename, isAbsolute, join, normalize, posix, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  U4_GROUPS,
  assertDoctorSafeRecord,
  canonicalJSONStringify,
  sha256Hex,
} from './u4-doctor-record.mjs'

export const U4_PUBLICATION_SCHEMA = 'chora.m1-u4-acceptance-evidence.v1'
export const BROWSER_MATRIX_SCHEMA = 'chora.m1-u4-browser-matrix.v1'
export const BROWSER_MATRIX_TEST = 'u4-real-rejection-route-matrix'

export const POSITIVE_ACTIONS = Object.freeze([
  'readiness_refresh', 'room_create', 'first_task_create', 'second_task_create',
  'second_task_revision_submit', 'revision_1_submit', 'request_revision', 'successor_draft_save',
  'revision_2_submit', 'revision_2_accept', 'run_start', 'verification_start', 'result_accept',
])
export const IMPLEMENTATION_ACTIONS = Object.freeze([
  'initial_readiness', 'create_room', 'implementation-gap-retry-primary_create_task',
  'implementation-gap-retry-primary_submit_plan', 'implementation-gap-retry-primary_accept_plan',
  'implementation-gap_start_run', 'predecessor_verification', 'implementation_gap_reject',
  'implementation_gap_retry', 'successor_verification', 'successor_result_accept', 'terminal_readiness',
])
export const REJECTION_ACTIONS = Object.freeze([
  'initial_readiness', 'create_room', 'rejection-route-exclusion-primary_create_task',
  'rejection-route-exclusion-primary_submit_plan', 'rejection-route-exclusion-primary_accept_plan',
  'planning-gap_start_run', 'planning-gap_verification', 'planning_gap_review',
  'rejection-route-exclusion-contract-change_create_task',
  'rejection-route-exclusion-contract-change_submit_plan',
  'rejection-route-exclusion-contract-change_accept_plan', 'contract-change_start_run',
  'contract-change_verification', 'contract_change_review', 'terminal_readiness',
])
export const REJECTION_ROUTES = Object.freeze([
  Object.freeze({ scenario: 'planning_gap', command: 'retry', status: 409, unchanged: true }),
  Object.freeze({ scenario: 'contract_change_required', command: 'retry', status: 409, unchanged: true }),
])

const digestPattern = /^[0-9a-f]{64}$/
const safeIDPattern = /^[A-Za-z0-9_-]{8,160}$/
const safeOutputName = /^[A-Za-z0-9][A-Za-z0-9._-]{0,159}\.json$/
const readinessKeys = Object.freeze([
  'source_baseline', 'pi_image', 'docker_engine', 'colima', 'oauth', 'ca_proxy_model', 'verifier', 'task_worktrees', 'owned_residue',
])
const positiveArtifacts = Object.freeze([
  'journey.json', 'events.json', 'status.json', 'patch.diff', 'restart-reopen.json', 'public-identities.json',
  'u4-observations.json', 'readiness.json', 'residue.json', 'disclosure.json', 'accepted-ui.png',
])
const positiveDirectoryEntries = Object.freeze([...positiveArtifacts, 'artifact-index.json'].sort())
const positiveZeroScans = Object.freeze([
  'disclosureActualCredentialLeaks', 'disclosureGenericSecretMatches', 'disclosureNonPlaceholderPrivacyMatches',
  'disclosurePrivatePathMatches', 'disclosureEnterpriseMatches', 'disclosureForbiddenPathMatches',
  'persistedActualCredentialLeaks', 'persistedGenericSecretMatches', 'persistedPrivatePathLeaks',
  'persistedEmailLeaks', 'persistedEnterpriseLeaks', 'publicHostPathMatches', 'bootstrapRouteMisuse',
])
const faultZeroScans = Object.freeze([
  'disclosureActualCredentialLeaks', 'disclosureGenericSecretMatches', 'disclosurePrivacyMatches',
  'disclosurePrivatePathMatches', 'disclosureEnterpriseMatches', 'disclosureForbiddenPathMatches',
  'persistedActualCredentialLeaks', 'persistedGenericSecretMatches', 'persistedPrivatePathLeaks',
  'persistedEmailLeaks', 'persistedEnterpriseLeaks',
])
const publicPageZeroScans = Object.freeze([
  'knownCredentialMatches', 'internalHostPathMatches', 'privateHomePathMatches',
  'vendorPathMatches', 'unrelatedEnterpriseMatches',
])
const obviousSecretPatterns = Object.freeze([
  /-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----/,
  /\bsk-[A-Za-z0-9_-]{20,}\b/,
  /\bgh[pousr]_[A-Za-z0-9]{30,}\b/,
  /\bAKIA[0-9A-Z]{16}\b/,
  /\bAIza[0-9A-Za-z_-]{30,}\b/,
  /\bxox[baprs]-[0-9A-Za-z-]{20,}\b/,
  /\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b/,
  /(?:password|token|cookie|api[_-]?key|authorization)\s*[:=]\s*["']?[^\s,"']{8,}/i,
])
const privateHomePath = /\/(?:Users|home)\/[^/\s]+(?:\/|$)/
const credentialedURL = /https?:\/\/[^/\s:@]+:[^/@\s]+@/i
const rawRootKey = /(?:^|_)(?:install|data|source|workspace|bundle|evidence|auth|ca)?(?:root|path|dir|directory)$/i

export async function validateU4Evidence({
  positiveDir,
  implementationGapFile,
  rejectionRouteFiles,
  doctorRecordFiles,
  rejectionDoctorRecordFiles,
  browserMatrixFile,
  sourceManifestFile,
  expectedBundleAggregate,
}) {
  const positive = await validatePositiveEvidence(positiveDir)
  const implementationGap = await validateFaultEvidence(
    await readBoundedJSONFile(implementationGapFile, 'implementation-gap evidence'),
    'implementation-gap-retry',
  )
  const source = await validateSourceManifestIdentity({ sourceManifestFile, expectedBundleAggregate })
  assertExactThree(rejectionRouteFiles, 'raw rejection-route evidence files')
  const rejectionRoutes = []
  for (const path of rejectionRouteFiles) {
    rejectionRoutes.push(await validateRejectionRouteEvidenceFile(path))
  }
  assert(Array.isArray(doctorRecordFiles) && doctorRecordFiles.length === 3,
    'exactly three Doctor safe records are required')
  const doctors = await readDoctorRecords(doctorRecordFiles, 'Doctor safe record')
  validateDoctors(doctors)
  assertExactThree(rejectionDoctorRecordFiles, 'rejection-route Doctor safe records')
  const rejectionDoctors = await readDoctorRecords(
    rejectionDoctorRecordFiles, 'rejection-route Doctor safe record')
  validateRejectionDoctors(rejectionDoctors)
  validateIndependentDoctorRuns(doctors, rejectionDoctors)
  const browserMatrixInput = await readBoundedJSONFile(browserMatrixFile, 'browser matrix', 256 * 1024)
  const browserMatrix = validateBrowserMatrix(browserMatrixInput.value)
  assertNoProhibitedEvidence(browserMatrix)
  validateRejectionRunBindings({ rejectionRoutes, rejectionDoctors, browserMatrix, source })

  assert(implementationGap.sourceManifestSha256 === source.sha256,
    'implementation-gap evidence source manifest drifted')
  const implementationDoctor = doctors.find((record) => record.group === 'implementation_gap')
  const rejectionGroupDoctor = doctors.find((record) => record.group === 'rejection_routes')
  assert(implementationDoctor.inputFingerprint === implementationGap.readinessInputFingerprint,
    'implementation-gap Doctor readiness fingerprint drifted')
  assert(rejectionGroupDoctor.digest === rejectionDoctors[0].digest,
    'group rejection-route Doctor is not the first browser-run Doctor')

  validateIndependentAttribution([positive, implementationGap, ...rejectionRoutes])

  const components = {
    positiveArtifactIndexSha256: positive.indexSha256,
    positiveObservationsSha256: positive.observationsSha256,
    implementationGapSha256: implementationGap.sha256,
    rejectionRouteDigests: rejectionRoutes.map(({ sha256 }) => sha256),
    browserMatrixSha256: browserMatrixInput.sha256,
    sourceManifestSha256: source.sha256,
    bundleAggregate: source.aggregateSha256,
    doctorRecordDigests: [...doctors]
      .sort((left, right) => left.group.localeCompare(right.group))
      .map(({ group, digest }) => ({ group, digest })),
    rejectionDoctorRecordDigests: rejectionDoctors.map(({ digest }) => digest),
  }
  const safePublication = {
    schemaVersion: U4_PUBLICATION_SCHEMA,
    status: 'passed',
    components,
  }
  const aggregateDigest = sha256Hex(canonicalJSONStringify(safePublication))
  return Object.freeze({ ...safePublication, aggregateDigest })
}

export async function publishU4Evidence(inputs, outputPath) {
  const publication = await validateU4Evidence(inputs)
  const output = exactAbsolutePath(outputPath, 'U4 publication output')
  assert(safeOutputName.test(basename(output)), 'U4 publication output basename is unsafe')
  const handle = await open(output, 'wx', 0o600)
  try {
    await handle.writeFile(`${JSON.stringify(publication, null, 2)}\n`, 'utf8')
    await handle.sync()
    await handle.chmod(0o600)
  } finally {
    await handle.close()
  }
  return {
    schemaVersion: U4_PUBLICATION_SCHEMA,
    status: 'passed',
    outputBasename: basename(output),
    aggregateDigest: publication.aggregateDigest,
  }
}

export function validateBrowserMatrix(record) {
  assertPlainObject(record, 'browser matrix')
  assertExactKeys(record, ['schemaVersion', 'status', 'retries', 'runs'], 'browser matrix')
  assert(record.schemaVersion === BROWSER_MATRIX_SCHEMA && record.status === 'passed' && record.retries === 0,
    'browser matrix header is not an exact zero-retry pass')
  assert(Array.isArray(record.runs) && record.runs.length === 3,
    'browser matrix must contain exactly three consecutive runs')
  for (let index = 0; index < record.runs.length; index++) {
    const run = record.runs[index]
    assertPlainObject(run, 'browser matrix run')
    assertExactKeys(run, [
      'sequence', 'runId', 'dataRootHash', 'evidenceSha256', 'passed', 'tests', 'coveredActions', 'coveredRoutes',
    ],
      'browser matrix run')
    assert(run.sequence === index + 1 && run.passed === true, 'browser matrix run is not a consecutive pass')
    assertDigest(run.runId, 'browser matrix run ID')
    assertDigest(run.dataRootHash, 'browser matrix data-root identity')
    assertDigest(run.evidenceSha256, 'browser matrix evidence digest')
    assert(run.runId === computeBrowserMatrixRunId(run), 'browser matrix run ID drifted')
    assertSameArray(run.tests, [BROWSER_MATRIX_TEST], 'browser matrix test identity drift')
    assertSameArray(run.coveredActions, REJECTION_ACTIONS, 'browser matrix covered actions drift')
    assertSameArray(run.coveredRoutes, REJECTION_ROUTES, 'browser matrix covered routes drift')
  }
  assertUnique(record.runs.map((run) => run.runId), 'browser matrix run IDs')
  assertUnique(record.runs.map((run) => run.dataRootHash), 'browser matrix data-root identities')
  assertUnique(record.runs.map((run) => run.evidenceSha256), 'browser matrix evidence digests')
  return record
}

export function computeBrowserMatrixRunId({ sequence, evidenceSha256, dataRootHash }) {
  assert(Number.isSafeInteger(sequence) && sequence >= 1 && sequence <= 3,
    'browser matrix run sequence is invalid')
  assertDigest(evidenceSha256, 'browser matrix evidence digest')
  assertDigest(dataRootHash, 'browser matrix data-root identity')
  return sha256Hex(canonicalJSONStringify({ sequence, evidenceSha256, dataRootHash }))
}

export async function validateSourceManifestIdentity({ sourceManifestFile, expectedBundleAggregate }) {
  assertDigest(expectedBundleAggregate, 'expected bundle aggregate')
  const input = await readBoundedJSONFile(sourceManifestFile, 'source manifest', 8 * 1024 * 1024)
  const manifest = input.value
  assertPlainObject(manifest, 'source manifest')
  assertExactKeys(manifest, ['schema_version', 'aggregate_sha256', 'files'], 'source manifest')
  assert(manifest.schema_version === 'chora.local-alpha-source-bundle.v1', 'source manifest schema drifted')
  assertDigest(manifest.aggregate_sha256, 'source manifest aggregate')
  assert(manifest.aggregate_sha256 === expectedBundleAggregate, 'source manifest bundle aggregate drifted')
  assert(Array.isArray(manifest.files) && manifest.files.length > 0, 'source manifest has no files')
  const paths = []
  let aggregateInput = ''
  for (const entry of manifest.files) {
    assertPlainObject(entry, 'source manifest entry')
    assertExactKeys(entry, ['path', 'size', 'mode', 'sha256'], 'source manifest entry')
    assert(typeof entry.path === 'string' && entry.path.length > 0 && !entry.path.includes('\0') &&
      !entry.path.includes('\\') && !entry.path.startsWith('/') &&
      posix.normalize(entry.path) === entry.path && entry.path !== '.' && entry.path !== '..' &&
      !entry.path.startsWith('../'), 'source manifest entry path is invalid')
    assert(Number.isSafeInteger(entry.size) && entry.size >= 0, 'source manifest entry size is invalid')
    assert(typeof entry.mode === 'string' && /^[0-7]{3,4}$/.test(entry.mode),
      'source manifest entry mode is invalid')
    const mode = Number.parseInt(entry.mode, 8)
    assert(mode <= 0o777 && (mode & 0o022) === 0, 'source manifest entry mode is unsafe')
    assertDigest(entry.sha256, 'source manifest entry digest')
    paths.push(entry.path)
    aggregateInput += `${entry.path}\0${entry.mode}\0${entry.size}\0${entry.sha256}\n`
  }
  assertUnique(paths, 'source manifest paths')
  assert(sha256Hex(aggregateInput) === expectedBundleAggregate,
    'source manifest entries do not match the bundle aggregate')
  return Object.freeze({ sha256: input.sha256, aggregateSha256: expectedBundleAggregate })
}

export async function validateRejectionRouteEvidenceFile(path) {
  const summary = validateFaultEvidence(
    await readBoundedJSONFile(path, 'rejection-route evidence'),
    'rejection-route-exclusion',
  )
  return Object.freeze({
    roomIds: Object.freeze([...summary.roomIds]),
    taskIds: Object.freeze([...summary.taskIds]),
    runIds: Object.freeze([...summary.runIds]),
    resultIds: Object.freeze([...summary.resultIds]),
    sha256: summary.sha256,
    sourceManifestSha256: summary.sourceManifestSha256,
    readinessInputFingerprint: summary.readinessInputFingerprint,
  })
}

async function validatePositiveEvidence(directoryPath) {
  const directory = exactAbsolutePath(directoryPath, 'positive evidence directory')
  const directoryInfo = await lstat(directory)
  assert(directoryInfo.isDirectory() && !directoryInfo.isSymbolicLink(),
    'positive evidence directory is not one real directory')
  const names = (await readdir(directory)).sort()
  assertSameArray(names, positiveDirectoryEntries, 'positive evidence directory entries drifted')

  const buffers = new Map()
  for (const name of positiveDirectoryEntries) {
    const path = join(directory, name)
    const info = await lstat(path)
    assert(info.isFile() && !info.isSymbolicLink() && info.size <= 8 * 1024 * 1024,
      'positive evidence contains an invalid artifact')
    const bytes = await readFile(path)
    assertNoProhibitedBytes(bytes)
    buffers.set(name, bytes)
  }

  const artifactIndex = strictJSON(buffers.get('artifact-index.json'), 'positive artifact index')
  const observations = strictJSON(buffers.get('u4-observations.json'), 'positive observations')
  const journey = strictJSON(buffers.get('journey.json'), 'positive journey')
  const restart = strictJSON(buffers.get('restart-reopen.json'), 'positive restart evidence')
  const publicIdentities = strictJSON(buffers.get('public-identities.json'), 'positive public identities')
  for (const value of [artifactIndex, observations, journey, restart, publicIdentities]) {
    assertNoProhibitedEvidence(value)
  }
  validatePositiveArtifactIndex(artifactIndex, buffers)
  validatePositiveObservations(observations)

  assert(artifactIndex.roomId === restart.roomId && artifactIndex.runId === restart.runId,
    'positive artifact identities disagree with restart evidence')
  assertSameArray(artifactIndex.taskIds, restart.taskIds, 'positive Task identities disagree')
  assert(restart.schemaVersion === 'chora.m1-u4-public-restart-reopen.v1' && restart.status === 'passed',
    'positive restart schema is invalid')
  assert(Array.isArray(restart.service?.starts) && restart.service.starts.length >= 2 &&
    Array.isArray(restart.service?.stops) && restart.service.stops.length >= 2,
  'positive evidence lacks a meaningful persisted restart')
  assert(restart.reopen?.entryPath === '/' && restart.reopen?.roomSelectedByName === true &&
    restart.reopen?.taskSelectedByTitle === true && restart.reopen?.serverCurrentAction === 'review_result' &&
    restart.reopen?.composedDeepLinkUsed === false && restart.reopen?.eventsEqual === true &&
    restart.reopen?.patchHeadersEqual === true,
  'positive restart/reopen predicates are incomplete')
  assert(publicIdentities.schemaVersion === 'chora.m1-u4-public-identities.v1' &&
    publicIdentities.roomId === artifactIndex.roomId && publicIdentities.runId === artifactIndex.runId &&
    publicIdentities.bootstrapRoutesClosed === true && publicIdentities.fakeAdapterObserved === false &&
    publicIdentities.hostPathDisplayed === false,
  'positive public identities contain an invalid execution substitute')
  assert(publicIdentities.wrongTaskPlanBinding?.status === 409 &&
    publicIdentities.wrongTaskPlanBinding?.runCreated === false &&
    publicIdentities.wrongTaskPlanBinding?.resourcesCreated === false,
  'positive wrong-binding exclusion is incomplete')

  const resultId = journey.verifiedReview?.resultId
  assertSafeID(artifactIndex.roomId, 'room_', 'positive Room')
  assert(Array.isArray(artifactIndex.taskIds) && artifactIndex.taskIds.length === 2,
    'positive evidence must identify exactly two Tasks')
  for (const taskId of artifactIndex.taskIds) assertSafeID(taskId, 'task_', 'positive Task')
  assertUnique(artifactIndex.taskIds, 'positive Task identities')
  assertSafeID(artifactIndex.runId, 'run_', 'positive Run')
  assertSafeID(resultId, 'result_', 'positive Result')
  assert(journey.id === artifactIndex.runId && journey.room?.id === artifactIndex.roomId &&
    artifactIndex.taskIds.includes(journey.task?.id), 'positive journey attribution drifted')

  return {
    roomIds: [artifactIndex.roomId], taskIds: artifactIndex.taskIds,
    runIds: [artifactIndex.runId], resultIds: [resultId],
    indexSha256: sha256Hex(buffers.get('artifact-index.json')),
    observationsSha256: sha256Hex(buffers.get('u4-observations.json')),
  }
}

function validatePositiveArtifactIndex(index, buffers) {
  assertPlainObject(index, 'positive artifact index')
  assertExactKeys(index, ['schemaVersion', 'roomId', 'taskIds', 'runId', 'artifacts'], 'positive artifact index')
  assert(index.schemaVersion === 'chora.m1-u4-public-artifact-index.v1', 'positive artifact index schema drift')
  assert(Array.isArray(index.artifacts) && index.artifacts.length === positiveArtifacts.length,
    'positive artifact index is incomplete')
  assertSameArray(index.artifacts.map((artifact) => artifact.path).sort(), [...positiveArtifacts].sort(),
    'positive artifact index paths drifted')
  for (const artifact of index.artifacts) {
    assertPlainObject(artifact, 'positive artifact index entry')
    assertExactKeys(artifact, ['path', 'bytes', 'sha256'], 'positive artifact index entry')
    assert(positiveArtifacts.includes(artifact.path), 'positive artifact path is not allowed')
    const bytes = buffers.get(artifact.path)
    assert(Number.isSafeInteger(artifact.bytes) && artifact.bytes === bytes.length,
      'positive artifact byte count drifted')
    assertDigest(artifact.sha256, 'positive artifact digest')
    assert(artifact.sha256 === sha256Hex(bytes), 'positive artifact digest mismatch')
  }
}

function validatePositiveObservations(value) {
  assertPlainObject(value, 'positive observations')
  assert(value.schemaVersion === 'chora.m1-u4-positive-observations.v1' && value.status === 'passed',
    'positive observations schema is not passed')
  const path = value.planRevisionPath
  assert(path?.revisionOne?.immutableThroughTerminalAcceptance === true &&
    path.revisionOne.reviewKind === 'request_revision' &&
    path.successorDraft?.predecessorRevisionId === path.revisionOne.id &&
    path.successorDraft?.exactContentSaved === true && path.revisionTwo?.reviewKind === 'accept' &&
    path.revisionTwo?.exactPredecessorDiffInspected === true &&
    path.revisionTwo?.predecessorRevisionId === path.revisionOne.id &&
    path.revisionTwo?.sourceDraftId === path.successorDraft.id,
  'positive Revision 1 to Revision 2 predicates are incomplete')
  assert(value.restartReentry?.rootEntry === true && value.restartReentry?.roomSelectedByName === true &&
    value.restartReentry?.taskSelectedByTitle === true &&
    value.restartReentry?.serverCurrentAction === 'review_result' &&
    value.restartReentry?.composedDeepLinkUsed === false,
  'positive restart re-entry predicates are incomplete')
  assert(value.executionBinding?.runRevisionTwoBound === true && value.executionBinding?.exactSnapshotBound === true &&
    value.executionBinding?.acceptedRevisionId === path.revisionTwo.id,
  'positive Revision 2 execution binding is incomplete')
  assert(value.publicUI?.allAcceptedMutationsBrowserDriven === true && value.publicUI?.mutationCount === 13,
    'positive UI mutation proof is incomplete')
  assertSameArray(value.publicUI.actions, POSITIVE_ACTIONS, 'positive covered actions drifted')
  assert(value.readiness503?.exercised === false, 'positive evidence contains a readiness block')
  for (const stage of ['initial', 'beforeStart', 'terminal']) validateReadiness(value.readiness?.[stage], stage)
  assert(value.scans?.disclosureStatus === 'passed' && value.scans?.persistedCredentialStatus === 'passed',
    'positive scans are not passed')
  assertZeroFields(value.scans, positiveZeroScans, 'positive scan')
  assert(value.residue?.status === 'absent', 'positive residue status is not absent')
  assertZeroFields(value.residue, [
    'labeledContainers', 'labeledVerifierContainers', 'labeledNetworks', 'agentWorkspaces', 'verifierWorkspaces',
  ], 'positive residue')
}

function validateFaultEvidence(input, scenario) {
  const value = input.value
  assertNoProhibitedEvidence(value)
  assertPlainObject(value, 'fault evidence')
  assert(value.schemaVersion === 'chora.m1-u4-public-real-retry-route-evidence.v1' &&
    value.status === 'passed' && value.scenario === scenario,
  'fault evidence schema/scenario is invalid')
  assertSafeID(value.roomId, 'room_', 'fault Room')
  assertSafeID(value.primaryTaskId, 'task_', 'fault primary Task')
  assertDigest(value.sourceManifestSha256, 'fault source manifest digest')
  assert(value.process?.binary?.startsWith('[INSTALL_ROOT]/') && Array.isArray(value.process?.argv),
    'fault process identity is not safely redacted')
  assert(Array.isArray(value.externalFaults) && value.externalFaults.length === 0,
    'U4 fault evidence contains an external fault')
  assert(Array.isArray(value.publicCommands) && Array.isArray(value.browserMutationAudit),
    'fault public command audits are absent')

  const observations = value.observations
  assertPlainObject(observations, 'fault observations')
  assert(observations.readiness503?.exercised === false, 'fault evidence contains a readiness block')
  assert(observations.readiness?.piEnabled === true && observations.readiness?.verifierEnabled === true,
    'fault evidence did not use real Pi and independent Verifier readiness')
  const initialReadiness = validateReadiness(observations.readinessUI?.initial, 'initial')
  const terminalReadiness = validateReadiness(observations.readinessUI?.terminal, 'terminal')
  assert(initialReadiness.inputFingerprint === terminalReadiness.inputFingerprint,
    'fault readiness fingerprint drifted during the evidence run')
  assert(observations.scans?.disclosureStatus === 'passed' && observations.scans?.persistedStatus === 'passed',
    'fault scans are not passed')
  assertZeroFields(observations.scans, faultZeroScans, 'fault scan')
  assert(observations.publicPageScan?.status === 'passed', 'fault public UI scan is not passed')
  assertZeroFields(observations.publicPageScan, publicPageZeroScans, 'fault public UI scan')
  assert(observations.residue?.status === 'absent', 'fault residue status is not absent')
  assertZeroFields(observations.residue,
    ['labeledContainers', 'labeledNetworks', 'executionWorkspaces'], 'fault residue')
  validateEmptyInventory(observations.after, 'terminal fault inventory')
  const expectedActions = scenario === 'implementation-gap-retry' ? IMPLEMENTATION_ACTIONS : REJECTION_ACTIONS
  assert(observations.publicUI?.allAcceptedMutationsBrowserDriven === true &&
    observations.publicUI?.mutationCount === expectedActions.length,
  'fault public UI publication predicate is incomplete')
  assertSameArray(observations.publicUI.actions, expectedActions, 'fault public UI covered actions drifted')

  if (scenario === 'implementation-gap-retry') {
    assert(value.publicCommands.length === 0, 'implementation-gap evidence contains unexpected public commands')
    validateBrowserAudit(value.browserMutationAudit, IMPLEMENTATION_ACTIONS)
    const evidence = observations.implementationGap
    assert(evidence?.authority === 'implementation_gap' && evidence.freshSuccessor === true &&
      evidence.unchangedFrozenBindings === true && evidence.successorIndependentlyVerified === true &&
      evidence.restartBeforeAcceptance === true && evidence.restartReentry?.rootEntry === true &&
      evidence.restartReentry?.roomSelectedByName === true && evidence.restartReentry?.taskSelectedByTitle === true &&
      evidence.restartReentry?.serverCurrentAction === 'review_result' &&
      evidence.restartReentry?.composedDeepLinkUsed === false &&
      evidence.predecessor?.outcome === 'review_ready' && evidence.successor?.outcome === 'review_ready' &&
      evidence.predecessor?.attemptId !== evidence.successor?.attemptId &&
      evidence.acceptedResultId === evidence.successor?.resultId,
    'implementation-gap successor Verification/restart/acceptance is incomplete')
    assert(evidence.predecessor?.resultId !== evidence.successor?.resultId,
      'implementation-gap successor reused the predecessor Result')
    const retry = value.browserMutationAudit.find((item) => item.action === 'implementation_gap_retry')
    assert(Number.isSafeInteger(retry?.expectedVersion) && retry.expectedVersion > 0,
      'implementation-gap Retry omitted version authority')
    assert(Array.isArray(value.serviceProcesses) && value.serviceProcesses.length >= 2,
      'implementation-gap evidence lacks a meaningful restart')
    const runId = observations.publicUI?.primaryRunId
    assertSafeID(runId, 'run_', 'implementation-gap Run')
    assertSafeID(evidence.predecessor?.resultId, 'result_', 'implementation-gap predecessor Result')
    assertSafeID(evidence.successor?.resultId, 'result_', 'implementation-gap successor Result')
    return {
      roomIds: [value.roomId], taskIds: [value.primaryTaskId], runIds: [runId],
      resultIds: [evidence.predecessor.resultId, evidence.successor.resultId], sha256: input.sha256,
      sourceManifestSha256: value.sourceManifestSha256,
      readinessInputFingerprint: initialReadiness.inputFingerprint,
    }
  }

  validateBrowserAudit(value.browserMutationAudit, REJECTION_ACTIONS)
  const evidence = observations.rejectionRoutes
  assert(evidence?.twoGovernedRealTasks === true && evidence.planningExclusion?.status === 409 &&
    evidence.planningExclusion?.unchanged === true && evidence.contractExclusion?.status === 409 &&
    evidence.contractExclusion?.unchanged === true &&
    evidence.planningRoute?.serverCurrentAction === 'continue_successor_plan' &&
    evidence.planningRoute?.composedDeepLinkUsed === false &&
    evidence.contractRoute?.serverCurrentAction === 'open_related_task' &&
    evidence.contractRoute?.composedDeepLinkUsed === false,
  'rejection-route exclusions/navigation are incomplete')
  assert(Array.isArray(value.publicCommands) && value.publicCommands.length === 2,
    'rejection-route evidence requires exactly two public exclusion commands')
  const planningRunId = evidence.planningGap?.id
  const contractRunId = evidence.contractChangeRequired?.id
  const exactPaths = [`/api/runs/${planningRunId}/retry`, `/api/runs/${contractRunId}/retry`]
  for (let index = 0; index < value.publicCommands.length; index++) {
    const command = value.publicCommands[index]
    assert(command?.method === 'POST' && command.path === exactPaths[index] && command.status === 409 &&
      Number.isSafeInteger(command.expectedVersion) && command.expectedVersion > 0,
    'rejection-route public exclusion command drifted')
  }
  assertSameArray(REJECTION_ROUTES, [
    { scenario: 'planning_gap', command: 'retry', status: evidence.planningExclusion.status,
      unchanged: evidence.planningExclusion.unchanged },
    { scenario: 'contract_change_required', command: 'retry', status: evidence.contractExclusion.status,
      unchanged: evidence.contractExclusion.unchanged },
  ], 'rejection route coverage drifted')
  const planningTaskId = evidence.planningGap?.taskId
  const contractTaskId = evidence.contractChangeRequired?.taskId
  assert(planningTaskId === value.primaryTaskId && planningTaskId !== contractTaskId,
    'rejection-route evidence did not use two distinct real Tasks')
  assert(observations.publicUI?.distinctTasks === true && observations.publicUI?.primaryRunId === planningRunId &&
    observations.publicUI?.secondaryRunId === contractRunId,
  'rejection-route public UI attribution drifted')
  for (const id of [planningTaskId, contractTaskId]) assertSafeID(id, 'task_', 'rejection-route Task')
  for (const id of [planningRunId, contractRunId]) assertSafeID(id, 'run_', 'rejection-route Run')
  return {
    roomIds: [value.roomId], taskIds: [planningTaskId, contractTaskId],
    runIds: [planningRunId, contractRunId], resultIds: [], sha256: input.sha256,
    sourceManifestSha256: value.sourceManifestSha256,
    readinessInputFingerprint: initialReadiness.inputFingerprint,
  }
}

function validateBrowserAudit(audit, expectedActions) {
  assertSameArray(audit.map((item) => item.action), expectedActions, 'fault browser mutation actions drifted')
  for (const item of audit) {
    assertPlainObject(item, 'fault browser mutation')
    assert(typeof item.method === 'string' && ['POST', 'PATCH'].includes(item.method),
      'fault browser mutation method is invalid')
    assert(typeof item.path === 'string' && item.path.startsWith('/api/') && !item.path.includes('..'),
      'fault browser mutation route is invalid')
    assert(Number.isSafeInteger(item.status) && item.status >= 200 && item.status < 300,
      'fault browser mutation did not pass')
  }
}

function validateDoctors(records) {
  assertSameArray([...records].map((record) => record.group).sort(), [...U4_GROUPS].sort(),
    'Doctor safe record groups are missing or duplicated')
  for (const field of ['installRootHash', 'dataRootHash', 'port', 'inputFingerprint']) {
    assertUnique(records.map((record) => record[field]), `Doctor ${field} values`)
  }
  assertUnique(records.flatMap((record) => [record.installRootHash, record.dataRootHash]),
    'Doctor install/data root identities')
}

async function readDoctorRecords(paths, label) {
  const records = []
  for (const path of paths) {
    const { value } = await readBoundedJSONFile(path, label, 256 * 1024)
    assertNoProhibitedEvidence(value)
    records.push(assertDoctorSafeRecord(value))
  }
  return records
}

function validateRejectionDoctors(records) {
  assert(records.every((record) => record.group === 'rejection_routes'),
    'browser-run Doctor group drifted')
  for (const field of ['digest', 'dataRootHash', 'inputFingerprint']) {
    assertUnique(records.map((record) => record[field]), `browser-run Doctor ${field} values`)
  }
}

function validateIndependentDoctorRuns(groupDoctors, rejectionDoctors) {
  const effectiveRecords = [
    groupDoctors.find((record) => record.group === 'positive'),
    groupDoctors.find((record) => record.group === 'implementation_gap'),
    ...rejectionDoctors,
  ]
  assert(effectiveRecords.length === 5 && effectiveRecords.every(Boolean),
    'effective Doctor run set is incomplete')
  for (const field of ['digest', 'installRootHash', 'dataRootHash', 'port', 'inputFingerprint']) {
    assertUnique(effectiveRecords.map((record) => record[field]), `effective Doctor ${field} values`)
  }
  assertUnique(effectiveRecords.flatMap((record) => [record.installRootHash, record.dataRootHash]),
    'effective Doctor install/data root identities')
}

function validateRejectionRunBindings({ rejectionRoutes, rejectionDoctors, browserMatrix, source }) {
  for (let index = 0; index < 3; index++) {
    const route = rejectionRoutes[index]
    const doctor = rejectionDoctors[index]
    const run = browserMatrix.runs[index]
    assert(run.evidenceSha256 === route.sha256,
      `raw rejection-route evidence ${index + 1} is not exact-order bound to the browser matrix`)
    assert(run.runId === computeBrowserMatrixRunId(run),
      `browser-matrix run ${index + 1} identity drifted`)
    assert(doctor.dataRootHash === run.dataRootHash,
      `browser-run Doctor ${index + 1} data-root identity drifted`)
    assert(doctor.inputFingerprint === route.readinessInputFingerprint,
      `browser-run Doctor ${index + 1} readiness fingerprint drifted`)
    assert(route.sourceManifestSha256 === source.sha256,
      `raw rejection-route evidence ${index + 1} source manifest drifted`)
  }
}

function validateIndependentAttribution(values) {
  for (const field of ['roomIds', 'taskIds', 'runIds', 'resultIds']) {
    const identities = values.flatMap((value) => value[field])
    assertUnique(identities, `independent ${field}`)
  }
}

function validateReadiness(value, label) {
  assertPlainObject(value, `${label} readiness`)
  assert(value.allReady === true, `${label} readiness is not ready`)
  assertSameArray(value.itemKeys, readinessKeys, `${label} readiness items drifted`)
  assertDigest(value.inputFingerprint, `${label} readiness fingerprint`)
  return value
}

function validateEmptyInventory(value, label) {
  assertPlainObject(value, label)
  for (const field of ['containers', 'networks', 'workspaces']) {
    assert(Array.isArray(value[field]) && value[field].length === 0, `${label} is not empty`)
  }
}

function assertNoProhibitedBytes(bytes) {
  const text = bytes.toString('utf8')
  assert(!privateHomePath.test(text), 'evidence contains a private home path')
  assert(!credentialedURL.test(text), 'evidence contains URL credentials')
  for (const pattern of obviousSecretPatterns) assert(!pattern.test(text), 'evidence contains obvious secret material')
  assert(!/(?:\/api\/(?:bootstrap|fixtures?)|fixture-only|manual[ _-]?cleanup|fixed[ _-]?identit(?:y|ies))/i.test(text),
    'evidence contains a forbidden bootstrap/fixture/fixed/manual marker')
}

function assertNoProhibitedEvidence(value, key = '') {
  if (Array.isArray(value)) {
    for (const item of value) assertNoProhibitedEvidence(item, key)
    return
  }
  if (value !== null && typeof value === 'object') {
    for (const [name, item] of Object.entries(value)) {
      assert(!/(?:fixture|fixed[ _-]?id|manual[ _-]?cleanup)/i.test(name),
        'evidence contains a forbidden implementation marker field')
      if (/fake/i.test(name)) assert(item === false || item === 0 || item === null,
        'evidence contains Fake substitution')
      if (/bootstrap/i.test(name)) {
        const allowed = name === 'bootstrapRoutesClosed' && item === true || name === 'bootstrapRouteMisuse' && item === 0
        assert(allowed, 'evidence contains bootstrap use')
      }
      assertNoProhibitedEvidence(item, name)
    }
    return
  }
  if (typeof value !== 'string') return
  assertNoProhibitedBytes(Buffer.from(value))
  assert(!/\bfake\b/i.test(value), 'evidence contains Fake substitution')
  if (rawRootKey.test(key) && value.startsWith('/') && !value.startsWith('/api/')) {
    throw new Error('evidence contains an unredacted absolute root field')
  }
}

async function readBoundedJSONFile(path, label, maxBytes = 1024 * 1024) {
  const input = exactAbsolutePath(path, label)
  const info = await lstat(input)
  assert(info.isFile() && !info.isSymbolicLink() && info.size <= maxBytes,
    `${label} must be one bounded regular file`)
  const bytes = await readFile(input)
  assertNoProhibitedBytes(bytes)
  return { value: strictJSON(bytes, label), sha256: sha256Hex(bytes) }
}

function strictJSON(bytes, label) {
  let value
  try { value = JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) }
  return value
}

function exactAbsolutePath(value, label) {
  assert(typeof value === 'string' && value.length > 1 && isAbsolute(value) &&
    normalize(value) === value && resolve(value) === value && !value.includes('\0'),
  `${label} must be an absolute normalized path`)
  return value
}

function assertSafeID(value, prefix, label) {
  assert(typeof value === 'string' && value.startsWith(prefix) && safeIDPattern.test(value), `${label} is invalid`)
}

function assertDigest(value, label) {
  assert(typeof value === 'string' && digestPattern.test(value), `${label} is not a lowercase SHA-256 digest`)
}

function assertZeroFields(value, fields, label) {
  for (const field of fields) assert(value?.[field] === 0, `${label} count is non-zero or absent`)
}

function assertUnique(values, label) {
  assert(new Set(values).size === values.length, `${label} are not unique`)
}

function assertExactThree(values, label) {
  assert(Array.isArray(values) && values.length === 3, `${label} must contain exactly three entries`)
}

function assertPlainObject(value, label) {
  assert(value !== null && typeof value === 'object' && !Array.isArray(value) &&
    (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null),
  `${label} must be a plain object`)
}

function assertExactKeys(value, expected, label) {
  assertSameArray(Object.keys(value).sort(), [...expected].sort(), `${label} fields drifted`)
}

function assertSameArray(actual, expected, message) {
  assert(Array.isArray(actual) && Array.isArray(expected) && actual.length === expected.length &&
    actual.every((value, index) => canonicalJSONStringify(value) === canonicalJSONStringify(expected[index])), message)
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

function parseCLI(argv, env) {
  const flags = [
    '--positive-dir', '--implementation-gap', '--rejection-routes', '--rejection-routes-2',
    '--rejection-routes-3', '--doctor-positive', '--doctor-implementation-gap',
    '--doctor-rejection-routes', '--doctor-rejection-routes-2', '--doctor-rejection-routes-3',
    '--browser-matrix', '--source-manifest', '--bundle-aggregate', '--output',
  ]
  const allowed = new Set(flags)
  const values = new Map()
  for (let index = 0; index < argv.length; index += 2) {
    const flag = argv[index]
    assert(allowed.has(flag) && index + 1 < argv.length && !values.has(flag), 'U4 evidence arguments are invalid')
    values.set(flag, argv[index + 1])
  }
  const envNames = {
    '--positive-dir': 'CHORA_U4_POSITIVE_EVIDENCE_DIR',
    '--implementation-gap': 'CHORA_U4_IMPLEMENTATION_GAP_EVIDENCE',
    '--rejection-routes': 'CHORA_U4_REJECTION_ROUTES_EVIDENCE',
    '--rejection-routes-2': 'CHORA_U4_REJECTION_ROUTES_EVIDENCE_2',
    '--rejection-routes-3': 'CHORA_U4_REJECTION_ROUTES_EVIDENCE_3',
    '--doctor-positive': 'CHORA_U4_DOCTOR_POSITIVE',
    '--doctor-implementation-gap': 'CHORA_U4_DOCTOR_IMPLEMENTATION_GAP',
    '--doctor-rejection-routes': 'CHORA_U4_DOCTOR_REJECTION_ROUTES',
    '--doctor-rejection-routes-2': 'CHORA_U4_DOCTOR_REJECTION_ROUTES_2',
    '--doctor-rejection-routes-3': 'CHORA_U4_DOCTOR_REJECTION_ROUTES_3',
    '--browser-matrix': 'CHORA_U4_BROWSER_MATRIX',
    '--source-manifest': 'CHORA_U4_SOURCE_MANIFEST',
    '--bundle-aggregate': 'CHORA_U4_BUNDLE_AGGREGATE',
    '--output': 'CHORA_U4_OUTPUT',
  }
  const get = (flag) => {
    const value = values.get(flag) ?? env[envNames[flag]]
    assert(typeof value === 'string' && value.length > 0, 'U4 evidence argument is missing')
    return value
  }
  return {
    positiveDir: get('--positive-dir'),
    implementationGapFile: get('--implementation-gap'),
    rejectionRouteFiles: [
      get('--rejection-routes'), get('--rejection-routes-2'), get('--rejection-routes-3'),
    ],
    doctorRecordFiles: [get('--doctor-positive'), get('--doctor-implementation-gap'), get('--doctor-rejection-routes')],
    rejectionDoctorRecordFiles: [
      get('--doctor-rejection-routes'), get('--doctor-rejection-routes-2'), get('--doctor-rejection-routes-3'),
    ],
    browserMatrixFile: get('--browser-matrix'),
    sourceManifestFile: get('--source-manifest'),
    expectedBundleAggregate: get('--bundle-aggregate'),
    outputPath: get('--output'),
  }
}

async function main() {
  try {
    const { outputPath, ...inputs } = parseCLI(process.argv.slice(2), process.env)
    const result = await publishU4Evidence(inputs, outputPath)
    process.stdout.write(`${JSON.stringify(result)}\n`)
  } catch {
    process.stdout.write(`${JSON.stringify({ schemaVersion: U4_PUBLICATION_SCHEMA, status: 'failed' })}\n`)
    process.exitCode = 1
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main()
