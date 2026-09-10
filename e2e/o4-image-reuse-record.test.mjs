import assert from 'node:assert/strict'
import { mkdtemp, readFile, stat, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import {
  PRIVATE_ENVIRONMENT_MARKER_SCHEMA,
  deriveEnvironmentId,
  sha256Hex,
} from './o4-installed-doctor-record.mjs'
import {
  DOCKER_OPERATION_LEDGER_SCHEMA,
  IMAGE_REUSE_RECORD_SCHEMA,
  RESOURCE_LEDGER_SCHEMA,
  SOURCE_A3_LEDGER_SCHEMA,
  assertImageReuseRecord,
  buildImageReuseRecord,
  computeAuditSliceDigest,
  computeCommandAuditRecordDigest,
  computeExecutionIdentityDigest,
  computeImageTupleDigest,
  parseCLI,
  persistImageReuseRecord as persistImageReuseRecordRaw,
} from './o4-image-reuse-record.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'

let captureBuild
test.before(async () => { captureBuild = await buildTestRepositoryCapture() })
test.after(async () => { await captureBuild?.cleanup() })

function persistImageReuseRecord(record, outputPath) {
  return persistImageReuseRecordRaw(record, outputPath, {
    repositoryCapture: captureBuild.capture,
  })
}

const digest = (label) => sha256Hex(`reuse-test:${label}`)
const roles = ['managed_pi_runtime', 'network_boundary', 'independent_verifier', 'capability_probe']
const managedAliasRoles = ['managed_pi_runtime', 'independent_verifier', 'capability_probe']
const nonce = Buffer.alloc(32, 11).toString('base64url')
const productID = (prefix, number) =>
  `${prefix}_01890f12-3456-7${number.toString(16).padStart(3, '0')}-8abc-${number.toString(16).padStart(12, '0')}`
const zeroResidue = () => ({
  ownedContainers: 0, ownedNetworks: 0, ownedVolumes: 0, ownedConfigs: 0,
  ownedWorkspaces: 0, ownedProcessGroups: 0, activeReferences: 0, recoverableReferences: 0,
})
const emptyInventory = () => Object.fromEntries(Object.keys(zeroResidue()).map((key) => [key, []]))

function marker() {
  return {
    schemaVersion: PRIVATE_ENVIRONMENT_MARKER_SCHEMA,
    markerNonce: nonce,
    environmentId: deriveEnvironmentId(nonce),
    installId: `ins_${'i'.repeat(32)}`,
    generationId: `gen_${'g'.repeat(32)}`,
    credentialCorpusId: `cor_${'c'.repeat(32)}`,
    credentialCorpusSha256: digest('corpus'),
  }
}

function roleImages() {
  const managed = {
    artifactId: 'managed-pi-runtime', archiveSha256: digest('managed-archive'),
    archiveSize: 101, dockerConfigImageId: `sha256:${digest('managed-config')}`,
  }
  const boundary = {
    artifactId: 'network-boundary', archiveSha256: digest('boundary-archive'),
    archiveSize: 103, dockerConfigImageId: `sha256:${digest('boundary-config')}`,
  }
  return Object.fromEntries(roles.map((role) => [role, { ...(role === 'network_boundary' ? boundary : managed) }]))
}

function attempt(sequence, resourceLedgerSha256, auditWindow, tupleIdentity = digest('b2-tuple')) {
  const value = {
    sequence,
    taskId: productID('task', sequence),
    runId: productID('run', sequence + 3),
    attemptId: productID('attempt', sequence + 6),
    workspaceIdentity: `sha256:${digest(`workspace:${sequence}`)}`,
    tupleIdentity,
    status: 'succeeded',
    runtimeIdentitySha256: digest(`runtime:${sequence}`),
    resourceLedgerSha256,
    roleTupleDigest: computeImageTupleDigest(roleImages()),
    frameworkRetryCount: 0,
    hiddenRetryCount: 0,
    auditStartSequence: auditWindow[0].sequence,
    auditEndSequence: auditWindow.at(-1).sequence,
    auditSliceDigest: computeAuditSliceDigest(auditWindow),
    operationCounts: { build: 0, pull: 0, load: 0 },
    terminalResidue: zeroResidue(),
  }
  return { ...value, executionIdentityDigest: computeExecutionIdentityDigest(value) }
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

function auditDraft(phase, subsystem, operationClass, safeTargetSha256 = null, result = 'succeeded') {
  return {
    phase, subsystem, invocation: operationClass === 'start' ? 'start' : 'run',
    operationClass, safeTargetSha256, result,
  }
}

function commandAudit(images) {
  const drafts = [
    auditDraft('setup', 'installation', 'context'),
    auditDraft('setup', 'installation', 'load', images.managed_pi_runtime.archiveSha256),
    auditDraft('setup', 'installation', 'inspect', images.managed_pi_runtime.dockerConfigImageId.slice(7)),
    auditDraft('setup', 'installation', 'load', images.network_boundary.archiveSha256),
    auditDraft('setup', 'installation', 'inspect', images.network_boundary.dockerConfigImageId.slice(7)),
  ]
  const appendAttempt = () => drafts.push(
    auditDraft('attempt', 'managed', 'create'),
    auditDraft('attempt', 'managed', 'start'),
    auditDraft('attempt', 'managed', 'exec'),
    auditDraft('attempt', 'managed', 'remove'),
  )
  const appendVerifier = () => drafts.push(
    auditDraft('verifier', 'verifier', 'inspect', images.independent_verifier.dockerConfigImageId.slice(7)),
    auditDraft('verifier', 'verifier', 'create'),
    auditDraft('verifier', 'verifier', 'exec'),
    auditDraft('verifier', 'verifier', 'remove'),
  )
  appendAttempt() // Minimal managed Attempt: present in source A3, never selected into reuse cohort.
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
    drafts.push(auditDraft('recovery', 'managed', 'list', digest(`scenario:${scenario}`)))
    appendAttempt()
  }
  drafts.push(auditDraft('recovery', 'managed', 'list', digest('scenario:retry')))
  appendAttempt()
  appendVerifier()
  drafts.push(auditDraft('recovery', 'managed', 'list'))
  return sealAudit(drafts)
}

function attemptWindows(audit) {
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

function phaseWindows(audit, phase) {
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
    ['setup', 1, phaseWindows(audit, 'setup')[0]],
    ...standardWindows.flatMap((records, index) => [
      ['standard_attempt', index + 1, records],
      ['standard_verifier', index + 1, standardVerifierWindows[index]],
    ]),
  ]
  const ranges = specs.map(([kind, ordinal, records]) => ({
    kind, ordinal, startSequence: records[0].sequence, endSequence: records.at(-1).sequence,
    auditSliceDigest: computeAuditSliceDigest(records),
  }))
  const selected = specs.flatMap(([, , records]) => records)
  return {
    commandAudit: selected,
    selectedA3AuditRanges: ranges,
    selectedA3AuditSequenceList: selected.map(({ sequence }) => sequence),
  }
}

function resealSource(ledger) {
  const drafts = ledger.commandAudit.map(({ sequence, previousDigest, digest: ignored, ...draft }) => {
    void sequence
    void previousDigest
    void ignored
    return draft
  })
  ledger.commandAudit = sealAudit(drafts)
  ledger.auditRecordCount = ledger.commandAudit.length
  ledger.auditFinalDigest = ledger.commandAudit.at(-1)?.digest ?? '0'.repeat(64)
  const windows = attemptWindows(ledger.commandAudit)
  const verifiers = phaseWindows(ledger.commandAudit, 'verifier')
  for (let index = 0; index < Math.min(windows.length, ledger.attempts.length); index++) {
    ledger.attempts[index].auditStartSequence = windows[index][0].sequence
    ledger.attempts[index].auditEndSequence = windows[index].at(-1).sequence
    ledger.attempts[index].auditSliceDigest = computeAuditSliceDigest(windows[index])
    if (ledger.attempts[index].verifierRequired) {
      const verifier = verifiers.find((window) => window[0].sequence === windows[index].at(-1).sequence + 1)
      ledger.attempts[index].verifierStartSequence = verifier[0].sequence
      ledger.attempts[index].verifierEndSequence = verifier.at(-1).sequence
      ledger.attempts[index].verifierSliceDigest = computeAuditSliceDigest(verifier)
    }
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

async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'chora-o4-reuse-'))
  const markerFile = join(root, 'marker.json')
  await writeFile(markerFile, JSON.stringify(marker()), { mode: 0o600 })
  const images = roleImages()
  const audit = commandAudit(images)
  const windows = attemptWindows(audit)
  const verifierWindows = phaseWindows(audit, 'verifier')
  assert.equal(windows.length, 9)
  assert.equal(verifierWindows.length, 5)
  const tupleIdentity = digest('b2-tuple')
  const resourceLedgerFiles = []
  const attempts = []
  for (let sequence = 1; sequence <= 3; sequence++) {
    const draft = attempt(sequence, digest('pending'), windows[sequence], tupleIdentity)
    const resource = {
      schemaVersion: RESOURCE_LEDGER_SCHEMA,
      taskId: draft.taskId,
      runId: draft.runId,
      attemptId: draft.attemptId,
      workspaceIdentity: draft.workspaceIdentity,
      tupleIdentity: draft.tupleIdentity,
      executionIdentityDigest: draft.executionIdentityDigest,
      runtimeIdentitySha256: draft.runtimeIdentitySha256,
      terminalStatus: 'succeeded',
      inventory: emptyInventory(),
    }
    const encoded = JSON.stringify(resource)
    const path = join(root, `resource-${sequence}.json`)
    await writeFile(path, encoded)
    resourceLedgerFiles.push(path)
    attempts.push(attempt(sequence, sha256Hex(encoded), windows[sequence], tupleIdentity))
  }
  const minimalAttempt = attempt(50, digest('minimal-resource-ledger'), windows[0], tupleIdentity)
  const scenarioNames = ['failure', 'cancel', 'timeout', 'orphan', 'retry']
  const scenarioStatuses = ['failed', 'cancelled', 'timed_out', 'orphaned', 'succeeded']
  const scenarioAttempts = scenarioNames.map((scenario, index) => ({
    ...attempt(60 + index, digest(`scenario-resource:${scenario}`), windows[4 + index], tupleIdentity),
    status: scenarioStatuses[index],
  }))
  const sourceLedger = {
    schemaVersion: SOURCE_A3_LEDGER_SCHEMA,
    status: 'passed',
    environmentId: marker().environmentId,
    installId: marker().installId,
    generationId: marker().generationId,
    platform: { os: 'darwin', architecture: 'arm64' },
    bindingDigest: digest('binding'),
    tupleIdentity,
    observationSource: 'exact-runner-command-audit',
    complete: true,
    engineEventsUsed: false,
    roleImages: images,
    commandAudit: audit,
    auditRecordCount: audit.length,
    auditFinalDigest: audit.at(-1).digest,
    auditSealed: true,
    attempts: [
      sourceAttempt(minimalAttempt, 1, 'minimal', null, { verifier: verifierWindows[0] }),
      ...attempts.map((value, index) => sourceAttempt(value, index + 2, 'standard', index + 1,
        { verifier: verifierWindows[index + 1] })),
      ...scenarioAttempts.map((value, index) => sourceAttempt(value, index + 5, 'scenario', null, {
        scenario: scenarioNames[index], verifier: scenarioNames[index] === 'retry' ? verifierWindows[4] : null,
        verifierRequired: scenarioNames[index] === 'retry',
      })),
    ],
  }
  const sourceA3LedgerFile = join(root, 'source-a3.json')
  const sourceA3Bytes = JSON.stringify(sourceLedger)
  await writeFile(sourceA3LedgerFile, sourceA3Bytes)
  const projection = selectedAuditBinding(audit, windows.slice(1, 4), verifierWindows.slice(1, 4))
  const operationLedger = {
    schemaVersion: DOCKER_OPERATION_LEDGER_SCHEMA,
    status: 'passed',
    environmentId: marker().environmentId,
    installId: marker().installId,
    generationId: marker().generationId,
    platform: { os: 'darwin', architecture: 'arm64' },
    bindingDigest: digest('binding'),
    tupleIdentity,
    observationSource: 'e-derived-sealed-a3-ledger-projection',
    complete: true,
    engineEventsUsed: false,
    sourceA3LedgerSha256: sha256Hex(sourceA3Bytes),
    sourceA3AuditRecordCount: audit.length,
    sourceA3AuditFinalDigest: audit.at(-1).digest,
    ...projection,
    roleImages: images,
    setupOperations: [
      {
        sequence: 1, auditSequence: 2, inspectionAuditSequence: 3, phase: 'setup', operationClass: 'load', artifactId: 'managed-pi-runtime',
        roles: managedAliasRoles, ...images.managed_pi_runtime, result: 'succeeded',
      },
      {
        sequence: 2, auditSequence: 4, inspectionAuditSequence: 5, phase: 'setup', operationClass: 'load', artifactId: 'network-boundary',
        roles: ['network_boundary'], ...images.network_boundary, result: 'succeeded',
      },
    ],
    phaseOperationCounts: {
      setup: { build: 0, pull: 0, load: 2 },
      attempt: { build: 0, pull: 0, load: 0 },
      retry: { build: 0, pull: 0, load: 0 },
      restart: { build: 0, pull: 0, load: 0 },
      recovery: { build: 0, pull: 0, load: 0 },
      verifier: { build: 0, pull: 0, load: 0 },
    },
    attempts,
  }
  const operationLedgerFile = join(root, 'operations.json')
  await writeFile(operationLedgerFile, JSON.stringify(operationLedger))
  return {
    root, markerFile, sourceA3LedgerFile, sourceLedger, resourceLedgerFiles,
    operationLedgerFile, operationLedger,
  }
}

test('proves exact Setup acquisition and three consecutive zero-retry image-reuse Attempts', async () => {
  const files = await fixture()
  const record = await buildImageReuseRecord({
    operationLedgerFile: files.operationLedgerFile,
    sourceA3LedgerFile: files.sourceA3LedgerFile,
    resourceLedgerFiles: files.resourceLedgerFiles,
    privateEnvironmentMarkerFile: files.markerFile,
  })
  assert.equal(record.schemaVersion, IMAGE_REUSE_RECORD_SCHEMA)
  assert.equal(record.managedAttempts.length, 3)
  assert.equal(record.setupAcquisition.length, 2)
  assert.deepEqual(record.setupAcquisition[0].roles, managedAliasRoles)
  assert.equal(record.roleImages.managed_pi_runtime.archiveSha256,
    record.roleImages.independent_verifier.archiveSha256)
  assert.equal(record.roleImages.managed_pi_runtime.dockerConfigImageId,
    record.roleImages.capability_probe.dockerConfigImageId)
  assert.equal(record.phaseOperationCounts.attempt.pull, 0)
  assert.equal(record.phaseOperationCounts.verifier.pull, 0)
  assert.equal(record.sourceA3AuditRecordCount, files.sourceLedger.commandAudit.length)
  assert.notEqual(record.commandAudit.length, files.sourceLedger.commandAudit.length)
  assert.equal(record.commandAudit.filter((item) => item.phase === 'verifier').length, 12)
  assert.equal(record.engineEventsUsed, false)
  assert.deepEqual(files.sourceLedger.attempts.map(({ profile }) => profile),
    ['minimal', 'standard', 'standard', 'standard', 'scenario', 'scenario', 'scenario', 'scenario', 'scenario'])
  assert.deepEqual(files.sourceLedger.attempts.slice(4).map(({ status }) => status),
    ['failed', 'cancelled', 'timed_out', 'orphaned', 'succeeded'])
  assert.deepEqual(files.sourceLedger.attempts.slice(4).map(({ attemptState, terminalReason }) =>
    [attemptState, terminalReason]), [
    ['failed', 'runtime_exit_nonzero'], ['cancelled', null], ['failed', 'attempt_timeout'],
    ['interrupted', null], ['output_submitted', null],
  ])
  assert.equal(record.selectedA3AuditRanges.length, 7)
  assert(files.sourceLedger.commandAudit.filter((item) => item.phase === 'recovery').at(-1).sequence >
    files.sourceLedger.attempts.at(-1).auditEndSequence)
  assert.equal(record.selectedA3AuditSequenceList.some((sequence) =>
    sequence >= files.sourceLedger.attempts[0].auditStartSequence &&
    sequence <= files.sourceLedger.attempts[0].auditEndSequence), false)
  assert.equal(record.tupleIdentity, files.operationLedger.tupleIdentity)
  assert.equal(record.sourceA3LedgerSha256, files.operationLedger.sourceA3LedgerSha256)
  assert.deepEqual(record.selectedA3AuditSequenceList, record.commandAudit.map(({ sequence }) => sequence))
  assert.equal(record.managedAttempts[0].executionIdentityDigest,
    computeExecutionIdentityDigest(record.managedAttempts[0]))
  assertImageReuseRecord(record)
})

test('rejects missing, reordered, forged, substituted, and spliced sealed source A3 evidence', async () => {
  for (const mutate of [
    (source) => { source.commandAudit.splice(8, 1) },
    (source) => { [source.commandAudit[1], source.commandAudit[2]] = [source.commandAudit[2], source.commandAudit[1]] },
    (source) => { source.commandAudit[7].digest = digest('forged-source-record') },
    (source) => { source.auditSealed = false },
    (source) => { source.attempts.shift() },
    (source) => { [source.attempts[1], source.attempts[2]] = [source.attempts[2], source.attempts[1]] },
    (source) => { source.attempts[2].cohortSequence = 1 },
    (source) => { source.attempts[1].verifierSliceDigest = digest('wrong-verifier-slice') },
    (source) => {
      source.attempts[1].verifierStartSequence = null
      source.attempts[1].verifierEndSequence = null
      source.attempts[1].verifierSliceDigest = null
    },
    (source) => {
      source.attempts[1].verifierStartSequence = source.attempts[2].verifierStartSequence
      source.attempts[1].verifierEndSequence = source.attempts[2].verifierEndSequence
      source.attempts[1].verifierSliceDigest = source.attempts[2].verifierSliceDigest
    },
    (source) => { source.attempts[1].verifierRequired = false },
    (source) => { source.attempts[4].status = 'succeeded' },
    (source) => { source.attempts[5].status = 'succeeded' },
    (source) => { source.attempts[6].status = 'succeeded' },
    (source) => { source.attempts[4].terminalReason = 'attempt_timeout' },
    (source) => { source.attempts[6].terminalReason = 'runtime_exit_nonzero' },
    (source) => { source.attempts[7].attemptState = 'failed' },
    (source) => { source.attempts[1].attemptState = 'failed' },
    (source) => { delete source.attempts[4].attemptState },
    (source) => { delete source.attempts[6].terminalReason },
    (source) => {
      source.attempts[2].profile = 'scenario'
      source.attempts[2].scenario = 'failure'
      source.attempts[2].cohortSequence = null
      source.attempts[2].status = 'failed'
    },
  ]) {
    const files = await fixture()
    mutate(files.sourceLedger)
    await writeFile(files.sourceA3LedgerFile, JSON.stringify(files.sourceLedger))
    await assert.rejects(buildImageReuseRecord({
      operationLedgerFile: files.operationLedgerFile,
      sourceA3LedgerFile: files.sourceA3LedgerFile,
      resourceLedgerFiles: files.resourceLedgerFiles,
      privateEnvironmentMarkerFile: files.markerFile,
    }))
  }

  const wrongBytes = await fixture()
  await writeFile(wrongBytes.sourceA3LedgerFile, `${JSON.stringify(wrongBytes.sourceLedger)}\n`)
  await assert.rejects(buildImageReuseRecord({
    operationLedgerFile: wrongBytes.operationLedgerFile,
    sourceA3LedgerFile: wrongBytes.sourceA3LedgerFile,
    resourceLedgerFiles: wrongBytes.resourceLedgerFiles,
    privateEnvironmentMarkerFile: wrongBytes.markerFile,
  }), /bytes digest/)

  const splice = await fixture()
  const selectedExec = splice.sourceLedger.commandAudit.find((record) =>
    record.phase === 'attempt' && record.sequence === splice.sourceLedger.attempts[1].auditStartSequence + 2)
  selectedExec.safeTargetSha256 = digest('changed-selected-source-target')
  resealSource(splice.sourceLedger)
  const sourceBytes = JSON.stringify(splice.sourceLedger)
  await writeFile(splice.sourceA3LedgerFile, sourceBytes)
  splice.operationLedger.sourceA3LedgerSha256 = sha256Hex(sourceBytes)
  splice.operationLedger.sourceA3AuditFinalDigest = splice.sourceLedger.auditFinalDigest
  await writeFile(splice.operationLedgerFile, JSON.stringify(splice.operationLedger))
  await assert.rejects(buildImageReuseRecord({
    operationLedgerFile: splice.operationLedgerFile,
    sourceA3LedgerFile: splice.sourceA3LedgerFile,
    resourceLedgerFiles: splice.resourceLedgerFiles,
    privateEnvironmentMarkerFile: splice.markerFile,
  }), /exact source A3|ranges drifted/)
})

test('CLI requires the actual sealed source A3 ledger input', async () => {
  const files = await fixture()
  const output = join(files.root, 'cli-output.json')
  const argv = [
    '--operation-ledger', files.operationLedgerFile,
    '--source-a3-ledger', files.sourceA3LedgerFile,
    ...files.resourceLedgerFiles.flatMap((path) => ['--resource-ledger', path]),
    '--private-marker', files.markerFile,
    '--service-controller', captureBuild.capture.executableFile,
    '--service-controller-sha256', captureBuild.capture.executableSha256,
    '--output', output,
  ]
  const parsed = parseCLI(argv)
  assert.equal(parsed.sourceA3LedgerFile, files.sourceA3LedgerFile)
  assert.equal(parsed.resourceLedgerFiles.length, 3)
  assert.deepEqual(parsed.repositoryCapture, captureBuild.capture)
  const sourceIndex = argv.indexOf('--source-a3-ledger')
  assert.throws(() => parseCLI([...argv.slice(0, sourceIndex), ...argv.slice(sourceIndex + 2)]),
    /incomplete/)
})

test('publishes owner-only with exclusive wx behavior', async () => {
  const files = await fixture()
  const record = await buildImageReuseRecord({
    operationLedgerFile: files.operationLedgerFile,
    sourceA3LedgerFile: files.sourceA3LedgerFile,
    resourceLedgerFiles: files.resourceLedgerFiles,
    privateEnvironmentMarkerFile: files.markerFile,
  })
  const missingCapabilityOutput = join(files.root, 'reuse-missing-capability.json')
  await assert.rejects(persistImageReuseRecordRaw(record, missingCapabilityOutput),
    /owned-path capability/)
  await assert.rejects(stat(missingCapabilityOutput), /ENOENT/)
  const output = join(files.root, 'reuse-safe.json')
  await persistImageReuseRecord(record, output)
  assert.equal((await stat(output)).mode & 0o777, 0o600)
  const before = await readFile(output, 'utf8')
  await assert.rejects(persistImageReuseRecord(record, output), /EEXIST/)
  assert.equal(await readFile(output, 'utf8'), before)
})

test('fails closed for missing/duplicate Attempts, retries, forbidden phase operations, and forged ledgers', async () => {
  for (const mutate of [
    (ledger) => { ledger.attempts.pop() },
    (ledger) => { ledger.attempts[1].attemptId = ledger.attempts[0].attemptId },
    (ledger) => { ledger.attempts[0].taskId = `tsk_${'a'.repeat(32)}` },
    (ledger) => { ledger.attempts[0].workspaceIdentity = `wsp_${'a'.repeat(32)}` },
    (ledger) => { ledger.attempts[0].tupleIdentity = digest('spliced-tuple') },
    (ledger) => { ledger.attempts[0].executionIdentityDigest = digest('forged-execution-identity') },
    (ledger) => { ledger.selectedA3AuditSequenceList.pop() },
    (ledger) => { ledger.selectedA3AuditRanges[1].auditSliceDigest = digest('spliced-a3-range') },
    (ledger) => { ledger.sourceA3LedgerSha256 = 'not-a-digest' },
    (ledger) => { ledger.sourceA3AuditFinalDigest = digest('spliced-source-a3-final') },
    (ledger) => { ledger.observationSource = 'exact-runner-command-audit' },
    (ledger) => { ledger.attempts[1].hiddenRetryCount = 1 },
    (ledger) => { ledger.phaseOperationCounts.retry.pull = 1 },
    (ledger) => {
      const record = ledger.commandAudit.find((item) => item.phase === 'attempt' && item.operationClass === 'exec')
      record.operationClass = 'pull'
      record.safeTargetSha256 = ledger.roleImages.managed_pi_runtime.archiveSha256
      record.result = 'failed'
    },
    (ledger) => { ledger.commandAudit.splice(1, 1) },
    (ledger) => {
      const first = ledger.commandAudit[1]
      ledger.commandAudit[1] = ledger.commandAudit[2]
      ledger.commandAudit[2] = first
    },
    (ledger) => { ledger.commandAudit[3].digest = digest('forged-command-record') },
    (ledger) => { ledger.sourceA3AuditRecordCount-- },
    (ledger) => {
      ledger.commandAudit = ledger.commandAudit.filter((record) => record.phase !== 'verifier')
    },
    ...['build', 'pull', 'load'].map((operationClass) => (ledger) => {
      const record = ledger.commandAudit.find((item) => item.phase === 'verifier' && item.operationClass === 'exec')
      record.operationClass = operationClass
      record.safeTargetSha256 = ledger.roleImages.independent_verifier.archiveSha256
      record.result = 'failed'
    }),
    (ledger) => { ledger.attempts[1].auditStartSequence = ledger.attempts[0].auditEndSequence },
    (ledger) => { ledger.attempts[1].auditSliceDigest = ledger.attempts[0].auditSliceDigest },
    (ledger) => { ledger.setupOperations[0].operationClass = 'build' },
    (ledger) => { ledger.setupOperations.push({ ...ledger.setupOperations[0], sequence: 3 }) },
    (ledger) => { ledger.setupOperations.pop() },
    (ledger) => { ledger.setupOperations.shift() },
    (ledger) => { ledger.setupOperations[1] = { ...ledger.setupOperations[0], sequence: 2 } },
    (ledger) => { ledger.setupOperations[0].archiveSha256 = digest('wrong-archive') },
    (ledger) => { ledger.setupOperations[0].dockerConfigImageId = `sha256:${digest('wrong-config')}` },
    (ledger) => { ledger.setupOperations[0].inspectionAuditSequence = ledger.setupOperations[0].auditSequence },
    (ledger) => { ledger.setupOperations[0].roles = ['managed_pi_runtime', 'independent_verifier'] },
    (ledger) => { ledger.roleImages.capability_probe.dockerConfigImageId = `sha256:${digest('wrong-alias-image')}` },
    (ledger) => { ledger.engineEventsUsed = true },
  ]) {
    const files = await fixture()
    mutate(files.operationLedger)
    await writeFile(files.operationLedgerFile, JSON.stringify(files.operationLedger))
    await assert.rejects(buildImageReuseRecord({
      operationLedgerFile: files.operationLedgerFile,
      sourceA3LedgerFile: files.sourceA3LedgerFile,
      resourceLedgerFiles: files.resourceLedgerFiles,
      privateEnvironmentMarkerFile: files.markerFile,
    }))
  }

  const forged = await fixture()
  const resource = JSON.parse(await readFile(forged.resourceLedgerFiles[0], 'utf8'))
  resource.inventory.ownedContainers.push('container-redacted')
  await writeFile(forged.resourceLedgerFiles[0], JSON.stringify(resource))
  await assert.rejects(buildImageReuseRecord({
    operationLedgerFile: forged.operationLedgerFile,
    sourceA3LedgerFile: forged.sourceA3LedgerFile,
    resourceLedgerFiles: forged.resourceLedgerFiles,
    privateEnvironmentMarkerFile: forged.markerFile,
  }), /inventory|digest/)

  const substituted = await fixture()
  const substitutedResource = JSON.parse(await readFile(substituted.resourceLedgerFiles[0], 'utf8'))
  substitutedResource.workspaceIdentity = `sha256:${digest('substituted-workspace')}`
  await writeFile(substituted.resourceLedgerFiles[0], JSON.stringify(substitutedResource))
  await assert.rejects(buildImageReuseRecord({
    operationLedgerFile: substituted.operationLedgerFile,
    sourceA3LedgerFile: substituted.sourceA3LedgerFile,
    resourceLedgerFiles: substituted.resourceLedgerFiles,
    privateEnvironmentMarkerFile: substituted.markerFile,
  }), /workspaceIdentity|resource ledger digest/)
})

test('rejects symlinked raw ledgers and record tampering', async () => {
  const files = await fixture()
  const linked = join(files.root, 'linked-operations.json')
  await symlink(files.operationLedgerFile, linked)
  await assert.rejects(buildImageReuseRecord({
    operationLedgerFile: linked,
    sourceA3LedgerFile: files.sourceA3LedgerFile,
    resourceLedgerFiles: files.resourceLedgerFiles,
    privateEnvironmentMarkerFile: files.markerFile,
  }), /regular non-symlink/)

  const record = await buildImageReuseRecord({
    operationLedgerFile: files.operationLedgerFile,
    sourceA3LedgerFile: files.sourceA3LedgerFile,
    resourceLedgerFiles: files.resourceLedgerFiles,
    privateEnvironmentMarkerFile: files.markerFile,
  })
  assert.throws(() => assertImageReuseRecord({ ...record, generationId: `gen_${'z'.repeat(32)}` }),
    /digest drifted/)
})
