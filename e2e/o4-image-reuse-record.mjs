import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

import {
  MANAGED_IMAGE_ALIAS_ROLES,
  NETWORK_BOUNDARY_ROLE,
  O4_ROLES,
  assertDigest,
  assertEnvironmentID,
  assertImageDigest,
  assertMarkerIdentity,
  assertSafePublicValue,
  canonicalJSONStringify,
  readExactBoundedFile,
  readPrivateEnvironmentMarker,
  sha256Hex,
  validatePlatform,
  writeExclusiveOwnerOnlyJSON,
} from './o4-installed-doctor-record.mjs'

export const DOCKER_OPERATION_LEDGER_SCHEMA = 'chora.m1-o4-docker-operation-ledger.v1'
export const SOURCE_A3_LEDGER_SCHEMA = 'chora.m1-o4-sealed-a3-runner-ledger.v1'
export const RESOURCE_LEDGER_SCHEMA = 'chora.m1-o4-attempt-resource-ledger.v1'
export const IMAGE_REUSE_RECORD_SCHEMA = 'chora.m1-o4-image-reuse-record.v1'

const phaseNames = Object.freeze(['setup', 'attempt', 'retry', 'restart', 'recovery', 'verifier'])
const subsystemNames = Object.freeze(['installation', 'managed', 'verifier'])
const invocationNames = Object.freeze(['run', 'start'])
const operationClasses = Object.freeze([
  'build', 'pull', 'load', 'inspect', 'create', 'start', 'exec', 'remove',
  'list', 'context', 'version', 'other',
])
const operationResults = Object.freeze(['succeeded', 'failed', 'cancelled'])
const zeroDigest = '0'.repeat(64)
const maxAuditRecords = 4096
const zeroCountKeys = Object.freeze([
  'ownedContainers', 'ownedNetworks', 'ownedVolumes', 'ownedConfigs', 'ownedWorkspaces',
  'ownedProcessGroups', 'activeReferences', 'recoverableReferences',
])
const uuidV7 = '[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}'
const attemptIDPatterns = Object.freeze({
  taskId: new RegExp(`^task_${uuidV7}$`),
  runId: new RegExp(`^run_${uuidV7}$`),
  attemptId: new RegExp(`^attempt_${uuidV7}$`),
  workspaceIdentity: /^sha256:[0-9a-f]{64}$/,
})

export async function buildImageReuseRecord({
  operationLedgerFile, sourceA3LedgerFile, resourceLedgerFiles, privateEnvironmentMarkerFile,
}) {
  assert(Array.isArray(resourceLedgerFiles) && resourceLedgerFiles.length === 3,
    'exactly three terminal resource ledgers are required')
  const marker = await readPrivateEnvironmentMarker(privateEnvironmentMarkerFile)
  const sourceBytes = await readExactBoundedFile(sourceA3LedgerFile, 'sealed source A3 ledger', 4 * 1024 * 1024)
  let source
  try { source = JSON.parse(sourceBytes.toString('utf8')) } catch { throw new Error('sealed source A3 ledger is not JSON') }
  const sourceAudit = validateSourceA3Ledger(source, marker)
  const ledgerBytes = await readExactBoundedFile(operationLedgerFile, 'Docker operation ledger', 2 * 1024 * 1024)
  let ledger
  try { ledger = JSON.parse(ledgerBytes.toString('utf8')) } catch { throw new Error('Docker operation ledger is not JSON') }
  const resourceInputs = []
  for (const path of resourceLedgerFiles) {
    const bytes = await readExactBoundedFile(path, 'terminal resource ledger', 256 * 1024)
    let value
    try { value = JSON.parse(bytes.toString('utf8')) } catch { throw new Error('terminal resource ledger is not JSON') }
    resourceInputs.push({ value, sha256: sha256Hex(bytes) })
  }
  const audit = validateOperationLedger(ledger, marker, resourceInputs, source, sourceAudit, sha256Hex(sourceBytes))

  const safe = {
    schemaVersion: IMAGE_REUSE_RECORD_SCHEMA,
    status: 'passed',
    environmentId: ledger.environmentId,
    installId: ledger.installId,
    generationId: ledger.generationId,
    platform: clone(ledger.platform),
    bindingDigest: ledger.bindingDigest,
    tupleIdentity: ledger.tupleIdentity,
    observationSource: 'e-derived-sealed-a3-ledger-projection',
    complete: true,
    engineEventsUsed: false,
    sourceA3LedgerSha256: sha256Hex(sourceBytes),
    sourceA3AuditRecordCount: source.auditRecordCount,
    sourceA3AuditFinalDigest: source.auditFinalDigest,
    sourceMinimalExecutionIdentityDigest: sourceAudit.minimalAttempt.executionIdentityDigest,
    sourceMinimalVerifierSliceDigest: sourceAudit.minimalVerifier.sliceDigest,
    selectedA3AuditRanges: clone(ledger.selectedA3AuditRanges),
    selectedA3AuditSequenceList: clone(ledger.selectedA3AuditSequenceList),
    operationLedgerSha256: sha256Hex(ledgerBytes),
    commandAudit: clone(ledger.commandAudit),
    roleImages: clone(ledger.roleImages),
    roleTupleDigest: computeImageTupleDigest(ledger.roleImages),
    setupAcquisition: audit.setupOperations.map((operation) => clone(operation)),
    phaseOperationCounts: clone(ledger.phaseOperationCounts),
    managedAttempts: ledger.attempts.map((attempt) => ({
      sequence: attempt.sequence,
      taskId: attempt.taskId,
      runId: attempt.runId,
      attemptId: attempt.attemptId,
      workspaceIdentity: attempt.workspaceIdentity,
      tupleIdentity: attempt.tupleIdentity,
      executionIdentityDigest: attempt.executionIdentityDigest,
      status: attempt.status,
      runtimeIdentitySha256: attempt.runtimeIdentitySha256,
      resourceLedgerSha256: attempt.resourceLedgerSha256,
      roleTupleDigest: attempt.roleTupleDigest,
      frameworkRetryCount: 0,
      hiddenRetryCount: 0,
      auditStartSequence: attempt.auditStartSequence,
      auditEndSequence: attempt.auditEndSequence,
      auditSliceDigest: attempt.auditSliceDigest,
      operationCounts: clone(attempt.operationCounts),
      terminalResidue: clone(attempt.terminalResidue),
    })),
  }
  return Object.freeze({ ...safe, digest: sha256Hex(canonicalJSONStringify(safe)) })
}

export function assertImageReuseRecord(record) {
  assertPlainObject(record, 'image reuse record')
  assertExactKeys(record, [
    'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
    'bindingDigest', 'tupleIdentity', 'observationSource', 'complete', 'engineEventsUsed', 'roleImages',
    'sourceA3LedgerSha256', 'sourceA3AuditRecordCount', 'sourceA3AuditFinalDigest',
    'sourceMinimalExecutionIdentityDigest', 'sourceMinimalVerifierSliceDigest', 'selectedA3AuditRanges',
    'selectedA3AuditSequenceList',
    'operationLedgerSha256', 'commandAudit', 'roleTupleDigest', 'setupAcquisition', 'phaseOperationCounts',
    'managedAttempts', 'digest',
  ], 'image reuse record')
  assert(record.schemaVersion === IMAGE_REUSE_RECORD_SCHEMA && record.status === 'passed',
    'image reuse record schema/status drifted')
  assertEnvironmentID(record.environmentId, 'image reuse environment ID')
  assertOpaqueAttemptID(record.installId, /^ins_[A-Za-z0-9_-]{24,80}$/, 'image reuse install ID')
  assertOpaqueAttemptID(record.generationId, /^gen_[A-Za-z0-9_-]{24,80}$/, 'image reuse generation ID')
  validatePlatform(record.platform)
  assertDigest(record.bindingDigest, 'image reuse binding digest')
  assertDigest(record.tupleIdentity, 'image reuse B2 tuple identity')
  assert(record.observationSource === 'e-derived-sealed-a3-ledger-projection' && record.complete === true &&
    record.engineEventsUsed === false, 'image reuse record is not a complete exact Runner ledger')
  assertDigest(record.sourceA3LedgerSha256, 'source A3 ledger digest')
  assert(Number.isSafeInteger(record.sourceA3AuditRecordCount) && record.sourceA3AuditRecordCount > 0,
    'source A3 audit record count is invalid')
  assertDigest(record.sourceA3AuditFinalDigest, 'source A3 audit final digest')
  assertDigest(record.sourceMinimalExecutionIdentityDigest,
    'source A3 Minimal execution identity digest')
  assertDigest(record.sourceMinimalVerifierSliceDigest, 'source A3 Minimal Verifier slice digest')
  assertDigest(record.operationLedgerSha256, 'exact Runner operation ledger digest')
  validateRoleImages(record.roleImages)
  const audit = validateProjectionRecord(record)
  assertDigest(record.roleTupleDigest, 'image reuse role tuple digest')
  assert(record.roleTupleDigest === computeImageTupleDigest(record.roleImages),
    'image reuse role tuple digest drifted')
  validateSetupOperations(record.setupAcquisition, record.roleImages, audit)
  validatePhaseCounts(record.phaseOperationCounts, audit.phaseOperationCounts)
  validatePublicAttempts(record.managedAttempts, record.tupleIdentity, record.roleTupleDigest, audit)
  assertDigest(record.digest, 'image reuse record digest')
  const { digest, ...safe } = record
  assert(digest === sha256Hex(canonicalJSONStringify(safe)), 'image reuse record digest drifted')
  assertSafePublicValue(record)
  return record
}

export async function validateImageReuseRecordSource(record, sourceA3LedgerFile, marker) {
  const checked = assertImageReuseRecord(record)
  const sourceBytes = await readExactBoundedFile(sourceA3LedgerFile,
    'sealed source A3 ledger', 4 * 1024 * 1024)
  let source
  try { source = JSON.parse(sourceBytes.toString('utf8')) } catch { throw new Error('sealed source A3 ledger is not JSON') }
  const sourceAudit = validateSourceA3Ledger(source, marker)
  assert(sha256Hex(sourceBytes) === checked.sourceA3LedgerSha256,
    'image reuse source A3 bytes digest drifted')
  assert(source.auditRecordCount === checked.sourceA3AuditRecordCount &&
    source.auditFinalDigest === checked.sourceA3AuditFinalDigest,
  'image reuse source A3 audit identity drifted')
  assertSameIdentity(checked, source, 'image reuse/source A3')
  assert(checked.bindingDigest === source.bindingDigest && checked.tupleIdentity === source.tupleIdentity,
    'image reuse/source A3 binding drifted')
  assertSameJSON(checked.roleImages, source.roleImages, 'image reuse/source A3 role images drifted')
  assert(checked.sourceMinimalExecutionIdentityDigest === sourceAudit.minimalAttempt.executionIdentityDigest,
    'image reuse source Minimal Attempt identity drifted')
  assert(checked.sourceMinimalVerifierSliceDigest === sourceAudit.minimalVerifier.sliceDigest,
    'image reuse source Minimal Verifier identity drifted')
  const audit = validateProjectionAgainstSource(checked, source, sourceAudit)
  validateSetupOperations(checked.setupAcquisition, checked.roleImages, audit)
  validatePhaseCounts(checked.phaseOperationCounts, audit.phaseOperationCounts)
  for (let index = 0; index < checked.managedAttempts.length; index++) {
    assertSameJSON(checked.managedAttempts[index], sourceAudit.standardAttempts[index].projected,
      `image reuse Standard Attempt ${index + 1} is not exact source A3 evidence`)
  }
  return checked
}

export async function persistImageReuseRecord(record, outputPath, dependencies = undefined) {
  assertImageReuseRecord(record)
  await writeExclusiveOwnerOnlyJSON(record, outputPath, 'image reuse record output',
    dependencies?.repositoryCapture)
}

function validateOperationLedger(ledger, marker, resourceInputs, source, sourceAudit, sourceSha256) {
  assertPlainObject(ledger, 'Docker operation ledger')
  assertExactKeys(ledger, [
    'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
    'bindingDigest', 'tupleIdentity', 'observationSource', 'complete', 'engineEventsUsed', 'roleImages',
    'sourceA3LedgerSha256', 'sourceA3AuditRecordCount', 'sourceA3AuditFinalDigest', 'selectedA3AuditRanges',
    'selectedA3AuditSequenceList',
    'commandAudit', 'setupOperations', 'phaseOperationCounts', 'attempts',
  ], 'Docker operation ledger')
  assert(ledger.schemaVersion === DOCKER_OPERATION_LEDGER_SCHEMA && ledger.status === 'passed',
    'Docker operation ledger schema/status drifted')
  assertMarkerIdentity(ledger, marker, 'Docker operation ledger')
  validatePlatform(ledger.platform)
  assertDigest(ledger.bindingDigest, 'Docker ledger binding digest')
  assertDigest(ledger.tupleIdentity, 'Docker ledger B2 tuple identity')
  assert(ledger.observationSource === 'e-derived-sealed-a3-ledger-projection' && ledger.complete === true &&
    ledger.engineEventsUsed === false,
  'Docker evidence must be an E-derived projection from one sealed A3 Runner ledger')
  assertDigest(ledger.sourceA3LedgerSha256, 'source A3 ledger digest')
  assert(ledger.sourceA3LedgerSha256 === sourceSha256, 'E projection source A3 bytes digest drifted')
  assert(ledger.sourceA3AuditRecordCount === source.auditRecordCount,
    'E projection source A3 audit count drifted')
  assertDigest(ledger.sourceA3AuditFinalDigest, 'source A3 audit final digest')
  assert(ledger.sourceA3AuditFinalDigest === source.auditFinalDigest,
    'E projection source A3 audit final digest drifted')
  assertSameIdentity(ledger, source, 'E projection/source A3')
  assert(ledger.bindingDigest === source.bindingDigest && ledger.tupleIdentity === source.tupleIdentity,
    'E projection/source A3 binding drifted')
  assertSameJSON(ledger.roleImages, source.roleImages, 'E projection/source A3 role images drifted')
  validateRoleImages(ledger.roleImages)
  const audit = validateProjectionAgainstSource(ledger, source, sourceAudit)
  validateSetupOperations(ledger.setupOperations, ledger.roleImages, audit)
  validatePhaseCounts(ledger.phaseOperationCounts, audit.phaseOperationCounts)
  const roleTupleDigest = computeImageTupleDigest(ledger.roleImages)
  validateRawAttempts(ledger.attempts, ledger.tupleIdentity, roleTupleDigest,
    resourceInputs, audit, sourceAudit.standardAttempts)
  assertSafePublicValue(ledger)
  return audit
}

function validateSourceA3Ledger(source, marker) {
  assertPlainObject(source, 'sealed source A3 ledger')
  assertExactKeys(source, [
    'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
    'bindingDigest', 'tupleIdentity', 'observationSource', 'complete', 'engineEventsUsed',
    'roleImages', 'commandAudit', 'auditRecordCount', 'auditFinalDigest', 'auditSealed', 'attempts',
  ], 'sealed source A3 ledger')
  assert(source.schemaVersion === SOURCE_A3_LEDGER_SCHEMA && source.status === 'passed',
    'sealed source A3 ledger schema/status drifted')
  assertMarkerIdentity(source, marker, 'sealed source A3 ledger')
  validatePlatform(source.platform)
  assertDigest(source.bindingDigest, 'sealed source A3 binding digest')
  assertDigest(source.tupleIdentity, 'sealed source A3 B2 tuple identity')
  assert(source.observationSource === 'exact-runner-command-audit' && source.complete === true &&
    source.engineEventsUsed === false, 'source A3 is not a complete exact Runner audit')
  validateRoleImages(source.roleImages)
  const audit = validateSourceCommandAudit(source.commandAudit, source.roleImages, source)
  const attempts = validateSourceAttempts(source.attempts, source.tupleIdentity,
    computeImageTupleDigest(source.roleImages), audit.attemptWindows, audit.verifierWindows)
  audit.minimalAttempt = attempts.minimalAttempt
  audit.minimalVerifier = attempts.minimalVerifier
  audit.standardAttempts = attempts.standardAttempts
  assertSafePublicValue(source)
  return audit
}

function validateRoleImages(roleImages) {
  assertPlainObject(roleImages, 'role image set')
  assertExactKeys(roleImages, O4_ROLES, 'role image set')
  for (const role of O4_ROLES) {
    const value = roleImages[role]
    assertPlainObject(value, `${role} exact image binding`)
    assertExactKeys(value,
      ['artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId'], `${role} exact image binding`)
    assert(['managed-pi-runtime', 'network-boundary'].includes(value.artifactId),
      `${role} exact artifact ID is invalid`)
    assertDigest(value.archiveSha256, `${role} exact archive digest`)
    assert(Number.isSafeInteger(value.archiveSize) && value.archiveSize > 0,
      `${role} exact archive size is invalid`)
    assertImageDigest(value.dockerConfigImageId, `${role} exact Docker config image ID`)
  }
  const managed = roleImages.managed_pi_runtime
  for (const role of MANAGED_IMAGE_ALIAS_ROLES) {
    for (const field of ['artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId']) {
      assert(roleImages[role][field] === managed[field], `${role} exact image ${field} alias drifted`)
    }
  }
  assert(managed.artifactId === 'managed-pi-runtime', 'managed exact artifact ID drifted')
  const boundary = roleImages[NETWORK_BOUNDARY_ROLE]
  assert(boundary.artifactId === 'network-boundary', 'boundary exact artifact ID drifted')
  for (const field of ['artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId']) {
    assert(boundary[field] !== managed[field], `managed/boundary exact image ${field} is not distinct`)
  }
}

function validateSourceCommandAudit(records, roleImages, metadata) {
  assert(Array.isArray(records) && records.length > 0 && records.length <= maxAuditRecords,
    'Runner command audit must be non-empty and bounded')
  assert(Number.isSafeInteger(metadata.auditRecordCount) && metadata.auditRecordCount === records.length,
    'Runner command audit record count drifted')
  assert(metadata.auditSealed === true, 'Runner command audit is not sealed')
  assertDigest(metadata.auditFinalDigest, 'Runner command audit final digest')

  const phaseOperationCounts = Object.fromEntries(phaseNames.map((phase) => [phase, {
    build: 0, pull: 0, load: 0,
  }]))
  let previousDigest = zeroDigest
  for (let index = 0; index < records.length; index++) {
    const record = records[index]
    validateCommandAuditRecord(record)
    assert(record.sequence === index + 1, 'Runner command audit sequence is not contiguous')
    assert(record.previousDigest === previousDigest, 'Runner command audit hash chain is broken')
    previousDigest = record.digest
    if (['build', 'pull', 'load'].includes(record.operationClass)) {
      phaseOperationCounts[record.phase][record.operationClass]++
    }
  }
  assert(metadata.auditFinalDigest === previousDigest, 'Runner command audit final digest drifted')

  const setupLoads = records.filter((record) => record.phase === 'setup' && record.operationClass === 'load')
  assert(setupLoads.length === 2, 'Setup audit must contain exactly two unique artifact loads')
  const expectedSetup = [
    { artifactId: 'managed-pi-runtime', roles: MANAGED_IMAGE_ALIAS_ROLES, binding: roleImages.managed_pi_runtime },
    { artifactId: 'network-boundary', roles: [NETWORK_BOUNDARY_ROLE], binding: roleImages.network_boundary },
  ]
  const setupOperations = setupLoads.map((record, index) => {
    const expected = expectedSetup[index]
    assert(record.result === 'succeeded', 'Setup artifact load did not succeed')
    assert(record.safeTargetSha256 === expected.binding.archiveSha256,
      'Setup load safe target does not bind the exact archive')
    const inspection = records.find((candidate) => candidate.sequence > record.sequence &&
      candidate.phase === 'setup' && candidate.operationClass === 'inspect' &&
      candidate.safeTargetSha256 === expected.binding.dockerConfigImageId.slice(7))
    assert(inspection?.result === 'succeeded',
      'Setup load lacks a subsequent exact config-ID inspection')
    return {
      sequence: index + 1,
      auditSequence: record.sequence,
      inspectionAuditSequence: inspection.sequence,
      phase: 'setup',
      operationClass: 'load',
      artifactId: expected.artifactId,
      roles: [...expected.roles],
      archiveSha256: expected.binding.archiveSha256,
      archiveSize: expected.binding.archiveSize,
      dockerConfigImageId: expected.binding.dockerConfigImageId,
      result: 'succeeded',
    }
  })

  const attemptWindows = contiguousPhaseWindows(records, 'attempt')
  assert(attemptWindows.length >= 4, 'source A3 audit lacks Minimal plus the Standard reuse cohort')
  for (const [index, window] of attemptWindows.entries()) {
    assertOrderedLifecycle(window.records, ['create', 'start', 'exec', 'remove'],
      `Managed Attempt ${index + 1}`)
  }

  const verifierWindows = contiguousPhaseWindows(records, 'verifier')
  assert(verifierWindows.length > 0, 'Runner command audit verifier phase is empty')
  for (const [index, window] of verifierWindows.entries()) {
    const checkIndex = window.records.findIndex((record) =>
      ['inspect', 'list', 'context', 'version'].includes(record.operationClass))
    assert(checkIndex >= 0, `Verifier ${index + 1} audit lacks an exact Runner check`)
    assertOrderedLifecycle(window.records.slice(checkIndex), ['create', 'exec', 'remove'],
      `Verifier ${index + 1}`)
  }

  return { phaseOperationCounts, setupOperations, attemptWindows, verifierWindows }
}

function validateCommandAuditRecord(record) {
  assertPlainObject(record, 'Runner command audit record')
  assertExactKeys(record, [
    'sequence', 'phase', 'subsystem', 'invocation', 'operationClass', 'safeTargetSha256',
    'result', 'previousDigest', 'digest',
  ], 'Runner command audit record')
  assert(Number.isSafeInteger(record.sequence) && record.sequence > 0,
    'Runner command audit sequence is invalid')
  assert(phaseNames.includes(record.phase), 'Runner command audit phase is invalid')
  assert(subsystemNames.includes(record.subsystem), 'Runner command audit subsystem is invalid')
  assert(invocationNames.includes(record.invocation), 'Runner command audit invocation is invalid')
  assert(operationClasses.includes(record.operationClass), 'Runner command audit operation class is invalid')
  assert(operationResults.includes(record.result), 'Runner command audit result is invalid')
  const expectedSubsystem = record.phase === 'setup' ? 'installation' :
    record.phase === 'verifier' ? 'verifier' : 'managed'
  assert(record.subsystem === expectedSubsystem, 'Runner command audit phase/subsystem binding drifted')
  if (['build', 'pull', 'load'].includes(record.operationClass)) {
    assertDigest(record.safeTargetSha256, 'Runner image-operation safe target digest')
  } else {
    assert(record.safeTargetSha256 === null ||
      typeof record.safeTargetSha256 === 'string' && /^[0-9a-f]{64}$/.test(record.safeTargetSha256),
    'Runner command audit safe target is invalid')
  }
  assertDigest(record.previousDigest, 'Runner command audit previous digest')
  assertDigest(record.digest, 'Runner command audit record digest')
  assert(record.digest === computeCommandAuditRecordDigest(withoutDigest(record)),
    'Runner command audit record digest drifted')
}

function validateSourceAttempts(attempts, tupleIdentity, roleTupleDigest, attemptWindows, verifierWindows) {
  assert(Array.isArray(attempts) && attempts.length === attemptWindows.length,
    'source A3 Attempt metadata does not cover every source Attempt window')
  const validated = []
  for (let index = 0; index < attempts.length; index++) {
    const sourceAttempt = attempts[index]
    assertPlainObject(sourceAttempt, 'source A3 Attempt')
    assertExactKeys(sourceAttempt, [
      'sourceAttemptOrdinal', 'profile', 'cohortSequence', 'taskId', 'runId', 'attemptId',
      'scenario', 'attemptState', 'terminalReason',
      'verifierRequired', 'verifierStartSequence', 'verifierEndSequence',
      'verifierSliceDigest',
      'workspaceIdentity', 'tupleIdentity', 'executionIdentityDigest', 'status',
      'runtimeIdentitySha256', 'resourceLedgerSha256', 'roleTupleDigest', 'frameworkRetryCount',
      'hiddenRetryCount', 'auditStartSequence', 'auditEndSequence', 'auditSliceDigest',
      'operationCounts', 'terminalResidue',
    ], 'source A3 Attempt')
    assert(sourceAttempt.sourceAttemptOrdinal === index + 1,
      'source A3 Attempt ordinal is not contiguous')
    assert(['minimal', 'standard', 'scenario'].includes(sourceAttempt.profile),
      'source A3 Attempt profile is invalid')
    const sequence = sourceAttempt.profile === 'standard' ? sourceAttempt.cohortSequence : index + 1
    const {
      sourceAttemptOrdinal, profile, cohortSequence, scenario, attemptState, terminalReason, verifierRequired,
      verifierStartSequence, verifierEndSequence, verifierSliceDigest, ...attempt
    } = sourceAttempt
    void sourceAttemptOrdinal
    void profile
    void cohortSequence
    void scenario
    void attemptState
    void terminalReason
    void verifierRequired
    void verifierStartSequence
    void verifierEndSequence
    void verifierSliceDigest
    const projected = { sequence, ...attempt }
    const sourceTruth = sourceAttempt.profile === 'scenario' ? {
      failure: { status: 'failed', attemptState: 'failed', terminalReason: 'runtime_exit_nonzero' },
      cancel: { status: 'cancelled', attemptState: 'cancelled', terminalReason: null },
      timeout: { status: 'timed_out', attemptState: 'failed', terminalReason: 'attempt_timeout' },
      orphan: { status: 'orphaned', attemptState: 'interrupted', terminalReason: null },
      retry: { status: 'succeeded', attemptState: 'output_submitted', terminalReason: null },
    }[sourceAttempt.scenario] : {
      status: 'succeeded', attemptState: 'output_submitted', terminalReason: null,
    }
    assert(sourceTruth && sourceAttempt.attemptState === sourceTruth.attemptState &&
      sourceAttempt.terminalReason === sourceTruth.terminalReason,
    'source A3 actual Attempt state or terminal reason drifted')
    validateAttempt(projected, sequence, tupleIdentity, roleTupleDigest, attemptWindows[index], sourceTruth.status)
    const verifier = validateSourceAttemptVerifier(sourceAttempt, attemptWindows[index], verifierWindows)
    validated.push({ source: sourceAttempt, projected, window: attemptWindows[index], verifier })
  }
  validateAttemptUniqueness(validated.map(({ projected }) => projected))
  const minimal = validated.filter(({ source }) => source.profile === 'minimal')
  const standard = validated.filter(({ source }) => source.profile === 'standard')
  assert(minimal.length === 1 && minimal[0].source.cohortSequence === null &&
    minimal[0].source.scenario === null && minimal[0].source.verifierRequired === true,
    'source A3 must contain one real Minimal Attempt')
  assert(standard.length === 3 && standard.every(({ source }, index) =>
    source.cohortSequence === index + 1 && source.scenario === null && source.verifierRequired === true),
    'source A3 does not contain the exact Standard reuse cohort')
  const standardIndexes = standard.map(({ source }) => source.sourceAttemptOrdinal - 1)
  assert(standardIndexes[1] === standardIndexes[0] + 1 && standardIndexes[2] === standardIndexes[1] + 1,
    'source A3 Standard reuse cohort is not consecutive')
  assert(minimal[0].source.sourceAttemptOrdinal < standard[0].source.sourceAttemptOrdinal,
    'source A3 Minimal Attempt is not before the Standard cohort')
  const scenarios = validated.filter(({ source }) => source.profile === 'scenario')
  assert(scenarios.every(({ source }) => source.cohortSequence === null &&
    source.sourceAttemptOrdinal > standard[2].source.sourceAttemptOrdinal),
  'source A3 C/D Attempt window was spliced into the Standard cohort')
  assertUnique(scenarios.map(({ source }) => source.scenario), 'source A3 scenario names')
  assert(scenarios.filter(({ source }) => source.scenario === 'retry').every(({ source }) =>
    source.verifierRequired === true), 'source A3 retry scenario lacks required Verification')
  const boundVerifierStarts = validated.filter(({ verifier }) => verifier !== null)
    .map(({ verifier }) => verifier.startSequence)
  assertUnique(boundVerifierStarts, 'source A3 Attempt-bound Verifier windows')
  assert(boundVerifierStarts.length === verifierWindows.length,
    'source A3 has an unbound or multiply-bound Verifier window')
  return {
    minimalAttempt: minimal[0].projected,
    minimalVerifier: minimal[0].verifier,
    standardAttempts: standard.map(({ projected, verifier }) => ({ projected, verifier })),
  }
}

function validateSourceAttemptVerifier(sourceAttempt, attemptWindow, verifierWindows) {
  assert(typeof sourceAttempt.verifierRequired === 'boolean',
    'source A3 Attempt verifier requirement is invalid')
  if (!sourceAttempt.verifierRequired) {
    assert(sourceAttempt.verifierStartSequence === null && sourceAttempt.verifierEndSequence === null &&
      sourceAttempt.verifierSliceDigest === null,
    'source A3 non-verified Attempt carries verifier evidence')
    return null
  }
  const verifier = verifierWindows.find((window) =>
    window.startSequence === sourceAttempt.verifierStartSequence &&
    window.endSequence === sourceAttempt.verifierEndSequence)
  assert(verifier && verifier.startSequence === attemptWindow.endSequence + 1,
    'source A3 Attempt/Verifier order or range binding drifted')
  assert(sourceAttempt.verifierSliceDigest === verifier.sliceDigest,
    'source A3 Attempt Verifier slice digest drifted')
  return verifier
}

function validateProjectionAgainstSource(value, source, sourceAudit) {
  const setupWindows = contiguousPhaseWindows(source.commandAudit, 'setup')
  assert(setupWindows.length === 1, 'source A3 setup range is ambiguous')
  const expectedRanges = [
    projectionRange('setup', 1, setupWindows[0]),
    ...sourceAudit.standardAttempts.flatMap(({ projected, verifier }, index) => [
      projectionRange('standard_attempt', index + 1, sourceAudit.attemptWindows.find((window) =>
        window.startSequence === projected.auditStartSequence && window.endSequence === projected.auditEndSequence)),
      projectionRange('standard_verifier', index + 1, verifier),
    ]),
  ]
  assertSameJSON(value.selectedA3AuditRanges, expectedRanges,
    'E projection selected source A3 ranges drifted or were spliced')
  const selectedRecords = expectedRanges.flatMap((range) => source.commandAudit.filter((record) =>
    record.sequence >= range.startSequence && record.sequence <= range.endSequence))
  const selectedSequences = selectedRecords.map(({ sequence }) => sequence)
  assertSameArray(value.selectedA3AuditSequenceList, selectedSequences,
    'E projection selected source A3 sequence list drifted')
  assertSameJSON(value.commandAudit, selectedRecords,
    'E projection records are not exact source A3 sequence/digest records')
  const audit = {
    phaseOperationCounts: sourceAudit.phaseOperationCounts,
    setupOperations: sourceAudit.setupOperations,
    attemptWindows: sourceAudit.standardAttempts.map(({ projected }) => sourceAudit.attemptWindows.find((window) =>
      window.startSequence === projected.auditStartSequence && window.endSequence === projected.auditEndSequence)),
  }
  validateProjectionRecord(value)
  return audit
}

function validateProjectionRecord(value) {
  assert(Array.isArray(value.commandAudit) && value.commandAudit.length > 0 &&
    value.commandAudit.length <= maxAuditRecords, 'E projection command selection is empty or unbounded')
  let priorSequence = 0
  for (const record of value.commandAudit) {
    validateCommandAuditRecord(record)
    assert(record.sequence > priorSequence, 'E projection source sequences are reordered or duplicated')
    priorSequence = record.sequence
  }
  assert(Array.isArray(value.selectedA3AuditRanges) && value.selectedA3AuditRanges.length === 7,
    'E projection must bind setup and three Standard Attempt/Verifier pairs')
  const expectedKinds = [
    ['setup', 1], ['standard_attempt', 1], ['standard_verifier', 1],
    ['standard_attempt', 2], ['standard_verifier', 2],
    ['standard_attempt', 3], ['standard_verifier', 3],
  ]
  let rangeCursor = 0
  for (let index = 0; index < value.selectedA3AuditRanges.length; index++) {
    const range = value.selectedA3AuditRanges[index]
    assertPlainObject(range, 'E projection source range')
    assertExactKeys(range, ['kind', 'ordinal', 'startSequence', 'endSequence', 'auditSliceDigest'],
      'E projection source range')
    assert(range.kind === expectedKinds[index][0] && range.ordinal === expectedKinds[index][1],
      'E projection source range kind/ordinal drifted')
    assert(Number.isSafeInteger(range.startSequence) && Number.isSafeInteger(range.endSequence) &&
      range.startSequence > rangeCursor && range.endSequence >= range.startSequence,
    'E projection source ranges overlap or are reordered')
    const records = value.commandAudit.filter((record) =>
      record.sequence >= range.startSequence && record.sequence <= range.endSequence)
    assert(records.length === range.endSequence - range.startSequence + 1 &&
      records.every((record, offset) => record.sequence === range.startSequence + offset),
    'E projection source range is missing a source record')
    assert(range.auditSliceDigest === computeAuditSliceDigest(records),
      'E projection source range slice digest drifted')
    const expectedPhase = range.kind === 'standard_attempt' ? 'attempt' :
      range.kind === 'standard_verifier' ? 'verifier' : range.kind
    assert(records.every((record) => record.phase === expectedPhase),
      'E projection source range phase drifted')
    rangeCursor = range.endSequence
  }
  assertSameArray(value.selectedA3AuditSequenceList, value.commandAudit.map(({ sequence }) => sequence),
    'E projection selected source sequence list does not match its records')
  return {
    phaseOperationCounts: value.phaseOperationCounts,
    setupOperations: value.setupAcquisition ?? value.setupOperations,
    attemptWindows: value.selectedA3AuditRanges.filter(({ kind }) => kind === 'standard_attempt').map((range) => {
      const records = value.commandAudit.filter((record) =>
        record.sequence >= range.startSequence && record.sequence <= range.endSequence)
      return {
        startSequence: range.startSequence, endSequence: range.endSequence,
        sliceDigest: range.auditSliceDigest, operationCounts: countImageOperations(records), records,
      }
    }),
  }
}

function projectionRange(kind, ordinal, window) {
  assert(window, `source A3 ${kind} window is missing`)
  return {
    kind, ordinal, startSequence: window.startSequence, endSequence: window.endSequence,
    auditSliceDigest: window.sliceDigest,
  }
}

function countImageOperations(records) {
  const counts = { build: 0, pull: 0, load: 0 }
  for (const record of records) if (Object.hasOwn(counts, record.operationClass)) counts[record.operationClass]++
  return counts
}

function validateSetupOperations(operations, roleImages, audit) {
  assert(Array.isArray(operations) && operations.length === 2,
    'Setup must contain exactly two unique artifact loads')
  const expected = [
    { artifactId: 'managed-pi-runtime', roles: MANAGED_IMAGE_ALIAS_ROLES, binding: roleImages.managed_pi_runtime },
    { artifactId: 'network-boundary', roles: [NETWORK_BOUNDARY_ROLE], binding: roleImages.network_boundary },
  ]
  for (let index = 0; index < operations.length; index++) {
    const operation = operations[index]
    assertPlainObject(operation, 'Setup acquisition')
    assertExactKeys(operation, [
      'sequence', 'auditSequence', 'inspectionAuditSequence', 'phase', 'operationClass', 'artifactId',
      'roles', 'archiveSha256', 'archiveSize', 'dockerConfigImageId', 'result',
    ], 'Setup acquisition')
    assert(operation.sequence === index + 1 && operation.phase === 'setup' && operation.operationClass === 'load' &&
      operation.result === 'succeeded' && Number.isSafeInteger(operation.inspectionAuditSequence) &&
      operation.inspectionAuditSequence > operation.auditSequence,
    'Setup acquisition is not an exact successful load followed by inspection')
    const wanted = expected[index]
    assert(operation.artifactId === wanted.artifactId, 'Setup acquisition artifact ID drifted')
    assertSameArray(operation.roles, wanted.roles, 'Setup acquisition role aliases drifted')
    for (const field of ['archiveSha256', 'archiveSize', 'dockerConfigImageId']) {
      assert(operation[field] === wanted.binding[field], `Setup acquisition ${field} drifted`)
    }
    assertSameJSON(operation, audit.setupOperations[index], 'Setup acquisition was not derived from command audit')
  }
}

function validatePhaseCounts(counts, derivedCounts) {
  assertPlainObject(counts, 'phase operation counts')
  assertExactKeys(counts, phaseNames, 'phase operation counts')
  assertSameJSON(counts, derivedCounts, 'phase operation counts were not derived from command audit')
  for (const phase of phaseNames) {
    const value = counts[phase]
    assertPlainObject(value, `${phase} operation counts`)
    assertExactKeys(value, ['build', 'pull', 'load'], `${phase} operation counts`)
    const expected = phase === 'setup' ? { build: 0, pull: 0, load: 2 } : { build: 0, pull: 0, load: 0 }
    assert(value.build === expected.build && value.pull === expected.pull && value.load === expected.load,
      `${phase} contains a forbidden or missing image operation`)
  }
}

function validateRawAttempts(attempts, tupleIdentity, roleTupleDigest, resourceInputs, audit, sourceAttempts) {
  assert(Array.isArray(attempts) && attempts.length === 3,
    'exactly three consecutive Managed Attempts are required')
  assert(resourceInputs.length === 3, 'exactly three resource ledger inputs are required')
  const publicAttempts = []
  for (let index = 0; index < attempts.length; index++) {
    const attempt = attempts[index]
    assertPlainObject(attempt, 'Managed Attempt ledger entry')
    assertExactKeys(attempt, [
      'sequence', 'taskId', 'runId', 'attemptId', 'workspaceIdentity', 'tupleIdentity',
      'executionIdentityDigest', 'status', 'runtimeIdentitySha256', 'resourceLedgerSha256', 'roleTupleDigest',
      'frameworkRetryCount', 'hiddenRetryCount', 'auditStartSequence', 'auditEndSequence',
      'auditSliceDigest', 'operationCounts', 'terminalResidue',
    ], 'Managed Attempt ledger entry')
    validateAttempt(attempt, index + 1, tupleIdentity, roleTupleDigest, audit.attemptWindows[index])
    assertSameJSON(attempt, sourceAttempts[index].projected,
      `Managed Attempt ${index + 1} was not selected exactly from source A3`)
    const resource = resourceInputs[index]
    validateResourceLedger(resource.value, attempt)
    assert(attempt.resourceLedgerSha256 === resource.sha256,
      `Managed Attempt ${index + 1} resource ledger digest drifted`)
    publicAttempts.push(attempt)
  }
  validateAttemptUniqueness(publicAttempts)
}

function validatePublicAttempts(attempts, tupleIdentity, roleTupleDigest, audit) {
  assert(Array.isArray(attempts) && attempts.length === 3,
    'image reuse record requires exactly three Managed Attempts')
  for (let index = 0; index < attempts.length; index++) {
    validateAttempt(attempts[index], index + 1, tupleIdentity,
      roleTupleDigest, audit.attemptWindows[index])
  }
  validateAttemptUniqueness(attempts)
}

function validateAttempt(attempt, sequence, tupleIdentity, roleTupleDigest, auditWindow,
  expectedStatus = 'succeeded') {
  assertPlainObject(attempt, 'Managed Attempt')
  assertExactKeys(attempt, [
    'sequence', 'taskId', 'runId', 'attemptId', 'workspaceIdentity', 'tupleIdentity',
    'executionIdentityDigest', 'status', 'runtimeIdentitySha256', 'resourceLedgerSha256', 'roleTupleDigest',
    'frameworkRetryCount', 'hiddenRetryCount', 'auditStartSequence', 'auditEndSequence',
    'auditSliceDigest', 'operationCounts', 'terminalResidue',
  ], 'Managed Attempt')
  assert(attempt.sequence === sequence && attempt.status === expectedStatus,
    'Managed Attempt sequence or terminal status drifted')
  for (const [field, pattern] of Object.entries(attemptIDPatterns)) {
    assertOpaqueAttemptID(attempt[field], pattern, `Managed Attempt ${field}`)
  }
  assert(attempt.tupleIdentity === tupleIdentity, 'Managed Attempt B2 tuple identity drifted')
  assertDigest(attempt.executionIdentityDigest, 'Managed Attempt execution identity digest')
  assert(attempt.executionIdentityDigest === computeExecutionIdentityDigest(attempt),
    'Managed Attempt execution identity digest drifted')
  assertDigest(attempt.runtimeIdentitySha256, 'Managed Attempt runtime identity digest')
  assertDigest(attempt.resourceLedgerSha256, 'Managed Attempt resource ledger digest')
  assertDigest(attempt.roleTupleDigest, 'Managed Attempt role tuple digest')
  assert(attempt.roleTupleDigest === roleTupleDigest, 'Managed Attempt role image tuple drifted')
  assert(attempt.frameworkRetryCount === 0 && attempt.hiddenRetryCount === 0,
    'Managed Attempt contains a framework or hidden retry')
  assert(Number.isSafeInteger(attempt.auditStartSequence) &&
    attempt.auditStartSequence === auditWindow.startSequence &&
    Number.isSafeInteger(attempt.auditEndSequence) && attempt.auditEndSequence === auditWindow.endSequence,
  'Managed Attempt audit range was spliced, overlapped, or left a window uncovered')
  assertDigest(attempt.auditSliceDigest, 'Managed Attempt audit slice digest')
  assert(attempt.auditSliceDigest === auditWindow.sliceDigest,
    'Managed Attempt audit slice digest drifted')
  assertSameJSON(attempt.operationCounts, auditWindow.operationCounts,
    'Managed Attempt operation counts were not derived from its audit range')
  validateZeroOperationCounts(attempt.operationCounts, 'Managed Attempt')
  validateZeroResidueCounts(attempt.terminalResidue, 'Managed Attempt terminal residue')
}

export function computeCommandAuditRecordDigest(recordWithoutDigest) {
  return sha256Hex(canonicalJSONStringify(recordWithoutDigest))
}

export function computeAuditSliceDigest(records) {
  assert(Array.isArray(records) && records.length > 0, 'command audit slice is empty')
  return sha256Hex(canonicalJSONStringify(records))
}

function withoutDigest(record) {
  const { digest, ...value } = record
  void digest
  return value
}

function contiguousPhaseWindows(records, phase) {
  const windows = []
  let current = []
  const flush = () => {
    if (current.length === 0) return
    const operationCounts = { build: 0, pull: 0, load: 0 }
    for (const record of current) {
      if (Object.hasOwn(operationCounts, record.operationClass)) operationCounts[record.operationClass]++
    }
    windows.push({
      startSequence: current[0].sequence,
      endSequence: current.at(-1).sequence,
      sliceDigest: computeAuditSliceDigest(current),
      operationCounts,
      records: current,
    })
    current = []
  }
  for (const record of records) {
    if (record.phase === phase) current.push(record)
    else flush()
  }
  flush()
  return windows
}

function assertOrderedLifecycle(records, operationOrder, label) {
  let cursor = -1
  for (const operationClass of operationOrder) {
    cursor = records.findIndex((record, index) => index > cursor && record.operationClass === operationClass)
    assert(cursor >= 0, `${label} audit lacks ordered ${operationClass} lifecycle evidence`)
  }
}

function validateResourceLedger(resource, attempt) {
  assertPlainObject(resource, 'terminal resource ledger')
  assertExactKeys(resource,
    ['schemaVersion', 'taskId', 'runId', 'attemptId', 'workspaceIdentity', 'tupleIdentity',
      'executionIdentityDigest', 'runtimeIdentitySha256', 'terminalStatus', 'inventory'],
    'terminal resource ledger')
  assert(resource.schemaVersion === RESOURCE_LEDGER_SCHEMA && resource.terminalStatus === 'succeeded',
    'terminal resource ledger schema/status drifted')
  for (const field of ['taskId', 'runId', 'attemptId', 'workspaceIdentity', 'tupleIdentity',
    'executionIdentityDigest']) {
    assert(resource[field] === attempt[field], `terminal resource ledger ${field} drifted`)
  }
  assert(resource.attemptId === attempt.attemptId &&
    resource.runtimeIdentitySha256 === attempt.runtimeIdentitySha256,
  'terminal resource ledger identity drifted')
  assertPlainObject(resource.inventory, 'terminal resource inventory')
  assertExactKeys(resource.inventory, zeroCountKeys, 'terminal resource inventory')
  for (const key of zeroCountKeys) {
    assert(Array.isArray(resource.inventory[key]) && resource.inventory[key].length === 0,
      `terminal resource inventory ${key} is not empty`)
  }
}

function validateZeroOperationCounts(value, label) {
  assertPlainObject(value, `${label} operation counts`)
  assertExactKeys(value, ['build', 'pull', 'load'], `${label} operation counts`)
  assert(value.build === 0 && value.pull === 0 && value.load === 0,
    `${label} contains an Attempt-time image operation`)
}

function validateZeroResidueCounts(value, label) {
  assertPlainObject(value, label)
  assertExactKeys(value, zeroCountKeys, label)
  for (const key of zeroCountKeys) assert(value[key] === 0, `${label} ${key} is non-zero`)
}

function validateAttemptUniqueness(attempts) {
  for (const field of Object.keys(attemptIDPatterns)) {
    assertUnique(attempts.map((attempt) => attempt[field]), `Managed Attempt ${field} values`)
  }
  assertUnique(attempts.map((attempt) => attempt.runtimeIdentitySha256),
    'Managed Attempt runtime identities')
  assertUnique(attempts.map((attempt) => attempt.resourceLedgerSha256),
    'Managed Attempt resource ledgers')
}

export function computeImageTupleDigest(roleImages) {
  validateRoleImages(roleImages)
  return sha256Hex(canonicalJSONStringify(roleImages))
}

export function computeExecutionIdentityDigest(value) {
  const identity = {
    tupleIdentity: value.tupleIdentity,
    taskId: value.taskId,
    runId: value.runId,
    attemptId: value.attemptId,
    workspaceIdentity: value.workspaceIdentity,
  }
  assertDigest(identity.tupleIdentity, 'execution B2 tuple identity')
  for (const [field, pattern] of Object.entries(attemptIDPatterns)) {
    assertOpaqueAttemptID(identity[field], pattern, `execution ${field}`)
  }
  return sha256Hex(canonicalJSONStringify(identity))
}

function assertOpaqueAttemptID(value, pattern, label) {
  assert(typeof value === 'string' && pattern.test(value), `${label} is invalid`)
}

function clone(value) {
  return JSON.parse(JSON.stringify(value))
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
  assert(actual.length === expected.length && actual.every((item, index) => item === expected[index]), message)
}

function assertSameJSON(actual, expected, message) {
  assert(canonicalJSONStringify(actual) === canonicalJSONStringify(expected), message)
}

function assertSameIdentity(actual, expected, label) {
  for (const field of ['environmentId', 'installId', 'generationId']) {
    assert(actual[field] === expected[field], `${label} ${field} drifted`)
  }
  assertSameJSON(actual.platform, expected.platform, `${label} platform drifted`)
}

function assertUnique(values, label) {
  assert(new Set(values).size === values.length, `${label} are not unique`)
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

export function parseCLI(argv) {
  const resources = []
  let operationLedgerFile
  let sourceA3LedgerFile
  let privateEnvironmentMarkerFile
  let outputPath
  let serviceControllerFile
  let serviceControllerSha256
  for (let index = 0; index < argv.length; index += 2) {
    const flag = argv[index]
    const value = argv[index + 1]
    assert(typeof value === 'string' && value.length > 0, 'image reuse record arguments are invalid')
    if (flag === '--operation-ledger' && operationLedgerFile === undefined) operationLedgerFile = value
    else if (flag === '--source-a3-ledger' && sourceA3LedgerFile === undefined) sourceA3LedgerFile = value
    else if (flag === '--resource-ledger') resources.push(value)
    else if (flag === '--private-marker' && privateEnvironmentMarkerFile === undefined) privateEnvironmentMarkerFile = value
    else if (flag === '--output' && outputPath === undefined) outputPath = value
    else if (flag === '--service-controller' && serviceControllerFile === undefined) serviceControllerFile = value
    else if (flag === '--service-controller-sha256' && serviceControllerSha256 === undefined) serviceControllerSha256 = value
    else throw new Error('image reuse record arguments are invalid')
  }
  assert(operationLedgerFile && sourceA3LedgerFile && privateEnvironmentMarkerFile && outputPath &&
    serviceControllerFile && serviceControllerSha256 && resources.length === 3,
    'image reuse record arguments are incomplete')
  return {
    operationLedgerFile, sourceA3LedgerFile, resourceLedgerFiles: resources,
    privateEnvironmentMarkerFile, outputPath,
    repositoryCapture: { executableFile: serviceControllerFile,
      executableSha256: serviceControllerSha256 },
  }
}

async function main() {
  try {
    const { outputPath, repositoryCapture, ...inputs } = parseCLI(process.argv.slice(2))
    const record = await buildImageReuseRecord(inputs)
    await persistImageReuseRecord(record, outputPath, { repositoryCapture })
    process.stdout.write(`${JSON.stringify({ schemaVersion: IMAGE_REUSE_RECORD_SCHEMA, status: 'passed', digest: record.digest })}\n`)
  } catch {
    process.stdout.write(`${JSON.stringify({ schemaVersion: IMAGE_REUSE_RECORD_SCHEMA, status: 'failed' })}\n`)
    process.exitCode = 1
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main()
