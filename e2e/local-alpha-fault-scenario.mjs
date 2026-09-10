import { createHash } from 'node:crypto'
import { spawn } from 'node:child_process'
import { chmod, mkdir, readFile, readdir, stat, writeFile } from 'node:fs/promises'
import { basename, dirname, relative, resolve, sep } from 'node:path'
import { chromium } from 'playwright'

import { auditDisclosureBoundary, assertNoCredentialLeakInTree } from './disclosure-gate.mjs'

const scenarios = new Set([
  'cancel-retry',
  'external-agent-termination',
  'implementation-gap-retry',
  'rejection-route-exclusion',
])
const scenarioObservationKeys = Object.freeze({
  'cancel-retry': 'cancelRetry',
  'external-agent-termination': 'externalTermination',
  'implementation-gap-retry': 'implementationGap',
  'rejection-route-exclusion': 'rejectionRoutes',
})

const scenario = required('CHORA_FAULT_SCENARIO')
assert(scenarios.has(scenario), `CHORA_FAULT_SCENARIO must be one of: ${[...scenarios].join(', ')}`)

const installRoot = absoluteRequired('CHORA_INSTALL_ROOT')
const dataRoot = absoluteRequired('CHORA_DATA_ROOT')
const sourceRoot = absoluteRequired('CHORA_SOURCE_ROOT')
const repositoryRoot = absoluteRequired('CHORA_REPOSITORY_ROOT')
const sourceManifest = absoluteRequired('CHORA_SOURCE_MANIFEST')
const evidenceRoot = absoluteRequired('CHORA_EVIDENCE_DIR')
const binary = absoluteRequired('CHORA_BINARY')
const authFile = absoluteRequired('CHORA_PI_CODEX_AUTH_FILE')
const caFile = absoluteRequired('CHORA_CA_FILE')
const bundleAggregate = fingerprint(required('CHORA_BUNDLE_AGGREGATE'), 'CHORA_BUNDLE_AGGREGATE')
const preflightFingerprint = fingerprint(required('CHORA_PREFLIGHT_FINGERPRINT'), 'CHORA_PREFLIGHT_FINGERPRINT')
const proxyURL = safeURL(required('CHORA_PROXY_URL'), 'CHORA_PROXY_URL')
const modelURL = safeURL(required('CHORA_MODEL_URL'), 'CHORA_MODEL_URL')
const port = integer(required('CHORA_PORT'), 'CHORA_PORT', 1, 65535)
const baseURL = `http://127.0.0.1:${port}`
const databasePath = resolve(dataRoot, 'chora.db')
const maxLogBytes = 64 * 1024
const isU4Scenario = scenario === 'implementation-gap-retry' || scenario === 'rejection-route-exclusion'
const roomName = `U4 ${scenario}`
const observations = {}
const publicCommands = []
const browserRequests = []
const uiMutations = []
const externalFaults = []
const serviceHistory = []
const knownSecrets = Object.entries(process.env)
  .filter(([name, value]) => /TOKEN|PASSWORD|SECRET|CREDENTIAL|COOKIE|AUTH/i.test(name) && typeof value === 'string' && value.length >= 4)
  .map(([, value]) => value)
  .concat(
    proxyURL.username ? [proxyURL.username] : [], proxyURL.password ? [proxyURL.password] : [],
    modelURL.username ? [modelURL.username] : [], modelURL.password ? [modelURL.password] : [],
    ...urlSecretScalars(proxyURL), ...urlSecretScalars(modelURL),
  )
const serveArgv = [
  'serve', '--source', sourceRoot, '--source-manifest', sourceManifest,
  '--bundle-aggregate', bundleAggregate, '--install', installRoot, '--data', dataRoot,
  '--repository', repositoryRoot,
  '--auth', authFile, '--ca', caFile, '--proxy', proxyURL.origin,
  '--model-url', modelURL.toString(), '--preflight-fingerprint', preflightFingerprint,
  '--db', databasePath, '--port', String(port),
]

class ReadinessBlocked extends Error {}

assert(!insideOrSame(dataRoot, evidenceRoot), 'CHORA_EVIDENCE_DIR must be outside CHORA_DATA_ROOT')
assert(!insideOrSame(installRoot, sourceRoot) && !insideOrSame(sourceRoot, installRoot),
  'CHORA_SOURCE_ROOT and CHORA_INSTALL_ROOT must be disjoint')
for (const root of [sourceRoot, installRoot, dataRoot]) {
  assert(!insideOrSame(root, repositoryRoot) && !insideOrSame(repositoryRoot, root), 'CHORA_REPOSITORY_ROOT must be disjoint from product roots')
}
assert(insideOrSame(sourceRoot, sourceManifest), 'CHORA_SOURCE_MANIFEST must be inside CHORA_SOURCE_ROOT')
assert(insideOrSame(installRoot, binary) && binary !== installRoot, 'CHORA_BINARY must be inside CHORA_INSTALL_ROOT')
assert(insideOrSame(dataRoot, databasePath) && databasePath !== dataRoot, 'database path must be inside CHORA_DATA_ROOT')
await Promise.all([
  assertFile(binary, 'CHORA_BINARY'), assertFile(sourceManifest, 'CHORA_SOURCE_MANIFEST'),
  assertFile(authFile, 'CHORA_PI_CODEX_AUTH_FILE'), assertFile(caFile, 'CHORA_CA_FILE'),
])
const authBytes = await readFile(authFile)
try {
  collectCredentialScalars(JSON.parse(authBytes.toString('utf8')), knownSecrets)
} finally {
  authBytes.fill(0)
}
const disclosureAudit = await auditDisclosureBoundary({
  bundleRoot: sourceRoot, manifestPath: sourceManifest, expectedAggregate: bundleAggregate, authFile,
})

let service
let browser
let page
let stopped = false
try {
  observations.before = await inventory()
  observations.disclosure = disclosureAudit
  assertClean(observations.before, 'pre-existing Chora resources make fault attribution impossible')
  service = await startService()
  const status = await api('GET', '/api/status', undefined, 200)
  observations.readiness = {
    piEnabled: status.pi?.enabled === true,
    verifierEnabled: status.verifier?.enabled === true,
  }

  if (isU4Scenario) {
    browser = await chromium.launch({ headless: true })
    const context = await browser.newContext()
    page = await context.newPage()
    page.on('request', (request) => browserRequests.push({
      method: request.method(), path: new URL(request.url()).pathname,
    }))
    await page.goto(baseURL, { waitUntil: 'networkidle' })
    observations.readinessUI = { initial: readinessObservation(await refreshReadinessThroughUI(page)) }
  }

  const room = isU4Scenario ? await createRoomThroughUI(page) : await command('POST', '/api/rooms', {
    name: `U3 ${scenario}`,
    description: `Public Real Task evidence for ${scenario}.`,
  }, 201, `u3-${scenario}-create-room`)
  assertServerID(room.id, 'room_', 'Room')
  assertServerID(room.initialRevision?.id, 'context_revision_', 'initial Room Revision')

  const primary = isU4Scenario
    ? await createGovernedTaskThroughUI(room, `${scenario}-primary`, false)
    : await createGovernedTask(room, `${scenario}-primary`, true)
  if (!isU4Scenario && (!observations.readiness.piEnabled || !observations.readiness.verifierEnabled)) {
    const runsBefore = await taskRunSignature(room.id, primary.task.id)
    const blocked = await commandRaw('POST', `/api/tasks/${primary.task.id}/runs`, {
      revisionId: primary.binding.revisionId,
    }, `u3-${scenario}-readiness-blocked`, 120_000)
    assert(blocked.status === 503, `unavailable readiness returned HTTP ${blocked.status}, expected 503`)
    assertSameJSON(await taskRunSignature(room.id, primary.task.id), runsBefore,
      '503 readiness failure persisted a Run')
    const afterBlocked = await inventory()
    assertClean(afterBlocked, '503 readiness failure created execution resources')
    observations.readiness503 = { exercised: true, status: blocked.status, resourcesCreated: false }
    throw new ReadinessBlocked('live readiness blocked the selected Real fault scenario with zero resources')
  } else if (!isU4Scenario) {
    observations.readiness503 = {
      exercised: false,
      reason: 'live installed readiness is green; production fault injection is forbidden',
    }
    if (scenario === 'cancel-retry') await cancelRetryJourney(primary)
    if (scenario === 'external-agent-termination') await externalTerminationJourney(primary)
  } else {
    observations.readiness503 = { exercised: false }
    if (scenario === 'implementation-gap-retry') await implementationGapJourney(primary)
    if (scenario === 'rejection-route-exclusion') await rejectionRouteJourney(room, primary)
  }

  observations.after = await waitForCleanInventory(60_000)
  assertClean(observations.after, 'terminal Docker/network/workspace residue remains')
  observations.residue = residueObservation(observations.after)
  if (scenario === 'external-agent-termination' && observations.readiness.piEnabled &&
    observations.readiness.verifierEnabled && !observations.readiness503.exercised) {
    assert(externalFaults.length === 1, `expected exactly one external fault, observed ${externalFaults.length}`)
  } else {
    assert(externalFaults.length === 0, `scenario ${scenario} introduced an external fault`)
  }

  await stopService(service, 'final-stop')
  stopped = true
  observations.persistedCredentialScan = await assertNoCredentialLeakInTree({ root: dataRoot, authFile })
  observations.scans = scanObservation(disclosureAudit, observations.persistedCredentialScan)
  await writeEvidence({ roomId: room.id, primaryTaskId: primary.task.id })
} finally {
  if (browser) await browser.close().catch(() => {})
  if (!stopped && service) await stopService(service, 'harness-finally').catch(() => {})
}

async function createGovernedTask(room, slug, probeUnsupported) {
  const key = `u3-${scenario}-${slug}-create-task`
  const body = realTaskBody(room.initialRevision.id, slug)
  if (probeUnsupported) {
    const tasksBefore = await workspaceTaskSignature(room.id)
    const counterexamples = [
      ['traversal', (input) => { input.realSpecCoding.writableFiles = ['../escape.go'] }],
      ['glob', (input) => { input.realSpecCoding.writableFiles = ['internal/domain/*.go'] }],
      ['shell', (input) => { input.realSpecCoding.verificationCommands = [{ argv: ['sh', '-c', 'go test ./internal/domain'] }] }],
    ]
    for (const [name, mutate] of counterexamples) {
      const unsupported = structuredClone(body)
      mutate(unsupported)
      const rejected = await commandRaw('POST', `/api/rooms/${room.id}/tasks`, unsupported, `${key}-${name}`)
      assert(rejected.status === 422, `${name} unsupported Real Task returned HTTP ${rejected.status}, expected 422`)
      assertSameJSON(await workspaceTaskSignature(room.id), tasksBefore,
        `${name} unsupported input persisted a partial Task`)
    }
    const afterUnsupported = await inventory()
    assertClean(afterUnsupported, '422 unsupported input created execution resources')
    observations.unsupported422 = {
      cases: counterexamples.map(([name]) => name),
      resourcesCreated: false,
      partialTaskCreated: false,
    }
  }
  const task = await command('POST', `/api/rooms/${room.id}/tasks`, body, 201, key)
  assertServerID(task.id, 'task_', 'Task')
  assert(task.roomId === room.id && task.executionProfile === 'real_spec_coding', 'public Real Task ownership/profile drift')
  assert(task.planning?.draft?.id, 'public Real Task omitted its Technical Plan Draft')
  const submitted = await command('POST', `/api/tasks/${task.id}/plan/drafts/${task.planning.draft.id}/submit`, {
    expectedEditVersion: task.planning.draft.editVersion,
    confirmUnchanged: false,
  }, 200, `u3-${scenario}-${slug}-submit-plan`)
  const accepted = await command('POST', `/api/tasks/${task.id}/plan/revisions/${submitted.revision.id}/reviews`, {
    kind: 'accept', note: 'Accept this exact bounded public Real Task plan.',
  }, 200, `u3-${scenario}-${slug}-accept-plan`)
  const current = await api('GET', `/api/rooms/${room.id}/tasks/${task.id}`, undefined, 200)
  const acceptance = accepted.acceptance
  assert(acceptance?.adapterId === 'pi', 'accepted Real Task did not bind Pi')
  for (const [label, value, prefix] of [
    ['Plan Revision', acceptance.revisionId, 'plan_revision_'],
    ['Snapshot', acceptance.snapshotId, 'context_snapshot_'],
    ['Charter', acceptance.charterId, 'charter_'],
  ]) assertServerID(value, prefix, label)
  const binding = {
    revisionId: acceptance.revisionId,
    snapshotId: acceptance.snapshotId,
    snapshotDigest: acceptance.snapshotDigest,
    charterId: acceptance.charterId,
    baselineDigest: current.repository?.baselineDigest,
    contractDigest: sha256(Buffer.from(JSON.stringify(current.criteria))),
  }
  assertFingerprint(binding.snapshotDigest, 'Snapshot digest')
  assertFingerprint(binding.baselineDigest, 'Baseline digest')
  return { room, task: current, binding, slug }
}

async function createRoomThroughUI(currentPage) {
  const response = await uiMutation(currentPage, '/api/rooms', async () => {
    await currentPage.getByRole('button', { name: '＋ New Room' }).click()
    await currentPage.getByLabel('Room name').fill(roomName)
    await currentPage.getByLabel('Description').fill(`Installed public UI evidence for ${scenario}.`)
    await currentPage.getByRole('button', { name: 'Create Room', exact: true }).click()
  }, 30_000, 'create_room')
  const room = await responseJSON(response, 201, 'create Room through installed UI')
  assertCallerIdempotency(response, 'create Room through installed UI')
  await currentPage.waitForURL((url) => url.pathname === `/rooms/${room.id}`)
  return room
}

async function createGovernedTaskThroughUI(room, slug, probeUnsupported) {
  const body = realTaskBody(room.initialRevision.id, slug)
  if (probeUnsupported) await probeUnsupportedRealTasks(room, body)
  if (new URL(page.url()).pathname !== `/rooms/${room.id}`) {
    await navigateToNamedRoom(page, room.name)
  }
  const taskResponse = await uiMutation(page, `/api/rooms/${room.id}/tasks`, async () => {
    await page.getByLabel('Task title').fill(body.title)
    await page.getByLabel('Task execution profile').selectOption('real_spec_coding')
    await page.getByLabel('Requirement').fill(body.realSpecCoding.requirement)
    await page.getByLabel(/Constraints/).fill(body.realSpecCoding.constraints.join('\n'))
    await page.getByLabel(/Out of scope/).fill(body.realSpecCoding.outOfScope.join('\n'))
    await page.getByLabel(/Acceptance criteria · title/).fill(body.realSpecCoding.criteria
      .map((criterion) => `${criterion.title} | ${criterion.description} | ${criterion.verificationCommandIndexes.join(',')}`).join('\n'))
    await page.getByLabel(/Writable files/).fill(body.realSpecCoding.writableFiles.join('\n'))
    await page.getByLabel(/Verification commands/).fill(body.realSpecCoding.verificationCommands
      .map((commandSpec) => JSON.stringify(commandSpec.argv)).join('\n'))
    await page.getByRole('button', { name: 'Create Task' }).click()
  }, 30_000, `${slug}_create_task`)
  const task = await responseJSON(taskResponse, 201, `create ${slug} through installed UI`)
  assertCallerIdempotency(taskResponse, `create ${slug} through installed UI`)
  assertServerID(task.id, 'task_', 'Task')
  assert(task.roomId === room.id && task.executionProfile === 'real_spec_coding', 'public Real Task ownership/profile drift')
  assert(task.planning?.draft?.id, 'public Real Task omitted its Technical Plan Draft')
  await page.waitForURL((url) => url.pathname === `/rooms/${room.id}/tasks/${task.id}`)

  const submitResponse = await uiMutation(page, `/api/tasks/${task.id}/plan/drafts/${task.planning.draft.id}/submit`, async () => {
    await page.getByRole('button', { name: 'Submit Revision' }).click()
  }, 30_000, `${slug}_submit_plan`)
  const submitted = await responseJSON(submitResponse, 200, `submit ${slug} Plan through installed UI`)
  assertCallerIdempotency(submitResponse, `submit ${slug} Plan through installed UI`)
  assertServerID(submitted.revision?.id, 'plan_revision_', 'Plan Revision')
  await page.getByRole('heading', { name: `Review Submitted Revision ${submitted.revision.revisionNumber}` }).waitFor()

  const acceptResponse = await uiMutation(page, `/api/tasks/${task.id}/plan/revisions/${submitted.revision.id}/reviews`, async () => {
    await page.getByLabel('Planning review note').fill('Accept this exact bounded public Real Task plan.')
    await page.getByRole('button', { name: 'Accept Revision' }).click()
  }, 30_000, `${slug}_accept_plan`)
  const accepted = await responseJSON(acceptResponse, 200, `accept ${slug} Plan through installed UI`)
  assertCallerIdempotency(acceptResponse, `accept ${slug} Plan through installed UI`)
  const current = await api('GET', `/api/rooms/${room.id}/tasks/${task.id}`, undefined, 200)
  const acceptance = accepted.acceptance
  assert(acceptance?.adapterId === 'pi', 'accepted Real Task did not bind Pi')
  for (const [label, value, prefix] of [
    ['Plan Revision', acceptance.revisionId, 'plan_revision_'],
    ['Snapshot', acceptance.snapshotId, 'context_snapshot_'],
    ['Charter', acceptance.charterId, 'charter_'],
  ]) assertServerID(value, prefix, label)
  const binding = {
    revisionId: acceptance.revisionId,
    snapshotId: acceptance.snapshotId,
    snapshotDigest: acceptance.snapshotDigest,
    charterId: acceptance.charterId,
    baselineDigest: current.repository?.baselineDigest,
    contractDigest: sha256(Buffer.from(JSON.stringify(current.criteria))),
  }
  assertFingerprint(binding.snapshotDigest, 'Snapshot digest')
  assertFingerprint(binding.baselineDigest, 'Baseline digest')
  return { room, task: current, binding, slug }
}

async function probeUnsupportedRealTasks(room, body) {
  const tasksBefore = await workspaceTaskSignature(room.id)
  const counterexamples = [
    ['traversal', (input) => { input.realSpecCoding.writableFiles = ['../escape.go'] }],
    ['glob', (input) => { input.realSpecCoding.writableFiles = ['internal/domain/*.go'] }],
    ['shell', (input) => { input.realSpecCoding.verificationCommands = [{ argv: ['sh', '-c', 'go test ./internal/domain'] }] }],
  ]
  for (const [name, mutate] of counterexamples) {
    const unsupported = structuredClone(body)
    mutate(unsupported)
    const rejected = await commandRaw('POST', `/api/rooms/${room.id}/tasks`, unsupported,
      `u4-${scenario}-unsupported-${name}`)
    assert(rejected.status === 422, `${name} unsupported Real Task returned HTTP ${rejected.status}, expected 422`)
    assertSameJSON(await workspaceTaskSignature(room.id), tasksBefore,
      `${name} unsupported input persisted a partial Task`)
  }
  const afterUnsupported = await inventory()
  assertClean(afterUnsupported, '422 unsupported input created execution resources')
  observations.unsupported422 = {
    cases: counterexamples.map(([name]) => name), resourcesCreated: false, partialTaskCreated: false,
  }
}

function realTaskBody(revisionId, slug) {
  return {
    title: `Validate Task descriptions (${slug})`,
    executionProfile: 'real_spec_coding',
    revisionIds: [revisionId],
    realSpecCoding: {
      requirement: 'Reject blank Task descriptions with the smallest bounded implementation and test change.',
      constraints: ['Keep existing valid Task behavior unchanged.', 'Do not change public APIs outside Task validation.', 'Never inspect vendor metadata or read or disclose credentials, personal information, non-Chora content, or enterprise-project code.'],
      outOfScope: ['No unrelated refactor.', 'No dependency changes.'],
      criteria: [{
        title: 'Blank descriptions are rejected',
        description: 'A focused test proves that blank Task descriptions fail validation.',
        verificationCommandIndexes: [0],
      }],
      writableFiles: ['internal/domain/task.go', 'internal/domain/task_test.go'],
      verificationCommands: [{ argv: ['go', 'test', './internal/domain'] }],
    },
  }
}

async function startRealRun(context, suffix) {
  const runsBefore = await taskRunSignature(context.room.id, context.task.id)
  const response = await commandRaw('POST', `/api/tasks/${context.task.id}/runs`, {
    revisionId: context.binding.revisionId,
  }, `u3-${scenario}-${suffix}-start-run`, 120_000)
  if (response.status === 503) {
    assertSameJSON(await taskRunSignature(context.room.id, context.task.id), runsBefore,
      '503 live Preflight/readiness failure persisted a Run')
    const afterBlocked = await inventory()
    assertClean(afterBlocked, '503 live Preflight/readiness failure created execution resources')
    observations.readiness503 = { exercised: true, status: response.status, resourcesCreated: false }
    throw new ReadinessBlocked('live readiness blocked the Real Run with zero resources')
  }
  assert(response.status === 201,
    `POST /api/tasks/${context.task.id}/runs: HTTP ${response.status}, expected 201: ${redact(response.text.slice(0, 2000))}`)
  const run = response.value
  assertServerID(run.id, 'run_', 'Run')
  assertRunBinding(run, context)
  assert(run.attempt === 1 && run.attemptHistory?.length === 1, 'initial Real Run did not create exactly one Attempt')
  const resource = await waitForAttemptResource(run.id, run.attemptDetail.id, 60_000)
  return { run, resource, lineage: captureLineage(run, resource) }
}

async function startRealRunThroughUI(context, suffix) {
  const runsBefore = await taskRunSignature(context.room.id, context.task.id)
  const response = await uiMutation(page, `/api/tasks/${context.task.id}/runs`, async () => {
    await page.getByRole('button', { name: 'Start Bound Pi Run' }).click()
  }, 120_000, `${suffix}_start_run`)
  if (response.status() === 503) {
    await responseJSON(response, 503, `${suffix} live readiness block`)
    assertSameJSON(await taskRunSignature(context.room.id, context.task.id), runsBefore,
      '503 live Preflight/readiness failure persisted a Run')
    const afterBlocked = await inventory()
    assertClean(afterBlocked, '503 live Preflight/readiness failure created execution resources')
    observations.readiness503 = { exercised: true, status: response.status(), resourcesCreated: false }
    throw new ReadinessBlocked('live readiness blocked the Real Run with zero resources')
  }
  const run = await responseJSON(response, 201, `${suffix} public UI Start`)
  assertCallerIdempotency(response, `${suffix} public UI Start`)
  assertServerID(run.id, 'run_', 'Run')
  assertRunBinding(run, context)
  assertRealPi(run, `${suffix} public UI Start`)
  assert(run.attempt === 1 && run.attemptHistory?.length === 1,
    'initial Real Run did not create exactly one Attempt')
  await page.waitForURL((url) => url.pathname ===
    `/rooms/${context.room.id}/tasks/${context.task.id}/runs/${run.id}`)
  const resource = await waitForAttemptResource(run.id, run.attemptDetail.id, 60_000)
  return { run, resource, lineage: captureLineage(run, resource) }
}

async function cancelRetryJourney(context) {
  const started = await startRealRun(context, 'cancel')
  const cancelled = await command('POST', `/api/runs/${started.run.id}/cancel`, {
    reason: 'Exercise strict public Cancel and its governed retry authority.',
  }, 200, `u3-${scenario}-cancel-run`, 120_000)
  assert(cancelled.status === 'cancelled', `strict Cancel terminal state is ${cancelled.status}`)
  assert(cancelled.attemptDetail?.state === 'cancelled' && cancelled.attemptDetail?.runtime?.state === 'stopped',
    'strict Cancel did not finalize the Attempt/runtime')
  assert(cancelled.controls?.canRetry === true, 'strict Cancel omitted governed retry authority')
  assert(cancelled.attemptHistory?.length === 1, 'strict Cancel created hidden lineage')
  await proveRunCleanup(started.run.id, 'strict Cancel')
  const retrying = await command('POST', `/api/runs/${started.run.id}/retry`, {
    expectedVersion: cancelled.version,
    instructions: 'Continue only from the accepted frozen bindings in a fresh Sandbox and workspace.',
  }, 200, `u3-${scenario}-retry-run`, 120_000)
  const successorResource = await waitForAttemptResource(retrying.id, retrying.attemptDetail.id, 60_000)
  assertFreshSuccessor(started.lineage, captureLineage(retrying, successorResource), retrying, context)
  const completed = await waitForRun(retrying.id, ['awaiting_verification'], 20 * 60_000)
  assertRunBinding(completed, context)
  await assertTaskBindingUnchanged(context)
  await proveRunCleanup(retrying.id, 'cancel successor')
  observations.cancelRetry = { cancelled: compactRun(cancelled), successor: compactRun(completed) }
}

async function externalTerminationJourney(context) {
  const started = await startRealRun(context, 'external-fault')
  await terminateExactlyLabeledAttempt(started.resource, started.run)
  const recovery = await waitForRun(started.run.id, ['recovery_required'], 5 * 60_000)
  assert(recovery.attempt === 1 && recovery.attemptDetail?.id === started.lineage.attemptId,
    'external termination changed Attempt lineage before recovery')
  assert(recovery.attemptDetail?.runtime?.state === 'stopped' && recovery.controls?.canRetry === true,
    'recovery_required lacks finalized runtime or governed Retry')
  await proveRunCleanup(started.run.id, 'external termination')

  await stopService(service, 'recovery-restart')
  service = await startService()
  const reconciled = await waitForRun(started.run.id, ['recovery_required'], 120_000)
  assert(reconciled.attemptDetail?.id === started.lineage.attemptId && reconciled.attempt === 1,
    'service restart/reconcile substituted the failed Attempt')
  assertRunBinding(reconciled, context)
  const retrying = await command('POST', `/api/runs/${started.run.id}/retry`, {
    expectedVersion: reconciled.version,
    instructions: 'Recover only after prior resources are proven dead and removed; start fresh.',
  }, 200, `u3-${scenario}-retry-run`, 120_000)
  const successorResource = await waitForAttemptResource(retrying.id, retrying.attemptDetail.id, 60_000)
  assertFreshSuccessor(started.lineage, captureLineage(retrying, successorResource), retrying, context)
  const completed = await waitForRun(retrying.id, ['awaiting_verification'], 20 * 60_000)
  await assertTaskBindingUnchanged(context)
  await proveRunCleanup(retrying.id, 'recovered successor')
  observations.externalTermination = {
    recovery: compactRun(recovery), reconciled: compactRun(reconciled), successor: compactRun(completed),
  }
}

async function implementationGapJourney(context) {
  const started = await startRealRunThroughUI(context, 'implementation-gap')
  const awaitingVerification = await waitForRun(started.run.id, ['awaiting_verification'], 20 * 60_000)
  await proveRunCleanup(started.run.id, 'initial Agent terminal')
  assertRealPi(awaitingVerification, 'implementation-gap predecessor Agent')
  await page.reload({ waitUntil: 'networkidle' })
  const predecessorReady = await startVerificationThroughUI(awaitingVerification, 'predecessor_verification')
  const predecessor = reviewReadySignature(predecessorReady)

  const rejectionResponse = await uiMutation(page, `/api/runs/${started.run.id}/review`, async () => {
    await page.getByLabel('Review note').fill('Repair only the bounded implementation gap proven by this exact Result.')
    await page.getByLabel('Rejection class').selectOption('implementation_gap')
    await page.getByRole('button', { name: 'Reject with reason' }).click()
  }, 30_000, 'implementation_gap_reject')
  assertUIExpectedVersion(rejectionResponse, predecessorReady.version, 'implementation-gap Review')
  const revisionRequired = await responseJSON(rejectionResponse, 200, 'implementation-gap human Review')
  assert(revisionRequired.status === 'revision_required' && revisionRequired.verifiedReview?.rejectionClass === 'implementation_gap',
    'human implementation_gap did not authorize revision_required')
  assert(revisionRequired.controls?.canRetry === true, 'human implementation_gap omitted Agent Retry authority')
  const predecessorReview = verifiedReviewSignature(revisionRequired.verifiedReview)
  assert(predecessorReview.resultId === predecessor.resultId && predecessorReview.patchDigest === predecessor.patchDigest,
    'implementation-gap Review did not bind the exact predecessor Result/Patch')

  const retryResponse = await uiMutation(page, `/api/runs/${started.run.id}/retry`, async () => {
    await page.getByLabel('Retry instructions').fill(
      'Repair only the exact trusted implementation gap and preserve every frozen binding.')
    await page.getByRole('button', { name: 'Retry Pi' }).click()
  }, 120_000, 'implementation_gap_retry')
  assertUIExpectedVersion(retryResponse, revisionRequired.version, 'implementation-gap Retry')
  const retrying = await responseJSON(retryResponse, 200, 'implementation-gap public UI Retry')
  const successorResource = await waitForAttemptResource(retrying.id, retrying.attemptDetail.id, 60_000)
  const successor = captureLineage(retrying, successorResource)
  assertFreshSuccessor(started.lineage, successor, retrying, context)
  assert(revisionRequired.attemptHistory?.[0]?.id === started.lineage.attemptId,
    'retry authority was not bound to the predecessor Attempt')
  const successorAwaitingVerification = await waitForRun(retrying.id, ['awaiting_verification'], 20 * 60_000)
  assert(successorAwaitingVerification.attemptHistory?.[1]?.predecessorId === started.lineage.attemptId,
    'successor Attempt omitted its immutable predecessor')
  assertRunBinding(successorAwaitingVerification, context)
  assertRealPi(successorAwaitingVerification, 'implementation-gap successor Agent')
  assertPredecessorHistory(successorAwaitingVerification, predecessor, predecessorReview)
  await assertTaskBindingUnchanged(context)
  await proveRunCleanup(retrying.id, 'implementation-gap successor')
  await page.reload({ waitUntil: 'networkidle' })
  const successorReady = await startVerificationThroughUI(successorAwaitingVerification, 'successor_verification')
  const successorResult = reviewReadySignature(successorReady)
  assert(successorResult.attemptId === successor.attemptId && successorResult.attemptId !== predecessor.attemptId,
    'successor Verification did not bind the fresh successor Agent Attempt')
  assertPredecessorHistory(successorReady, predecessor, predecessorReview)
  const preRestart = immutableRetryLineage(successorReady)

  await stopService(service, 'implementation-gap-review-ready-restart')
  service = await startService()
  await page.goto(baseURL, { waitUntil: 'networkidle' })
  const restartReentry = await followNamedTaskCurrentAction({
    currentPage: page, room: context.room, taskTitle: context.task.title,
    expectedKind: 'review_result', expectedLabel: 'Continue - Review result', expectedRunId: started.run.id,
  })
  const reopened = await api('GET', `/api/rooms/${context.room.id}/tasks/${context.task.id}/runs/${started.run.id}`, undefined, 200)
  assertSameJSON(immutableRetryLineage(reopened), preRestart,
    'restart/Directory re-entry changed predecessor or successor Result/Review/Patch lineage')
  assertPredecessorHistory(reopened, predecessor, predecessorReview)
  await assertTaskBindingUnchanged(context)

  const acceptanceResponse = await uiMutation(page, `/api/runs/${started.run.id}/review`, async () => {
    await page.getByLabel('Review note').fill(
      'Accept the exact independently verified successor Result after persisted restart recovery.')
    await page.getByRole('button', { name: 'Accept verified Result' }).click()
  }, 30_000, 'successor_result_accept')
  assertUIExpectedVersion(acceptanceResponse, reopened.version, 'successor Result acceptance')
  const accepted = await responseJSON(acceptanceResponse, 200, 'accept exact successor Result')
  assert(accepted.status === 'accepted' && accepted.verifiedReview?.kind === 'accept',
    'successor Result acceptance did not reach accepted')
  assert(accepted.verifiedReview.resultId === successorResult.resultId &&
    accepted.verifiedReview.patchDigest === successorResult.patchDigest,
  'terminal Review did not bind the exact successor Result/Patch')
  assertPredecessorHistory(accepted, predecessor, predecessorReview)
  assertSameJSON(reviewReadyCore(accepted), reviewReadyCore(reopened),
    'terminal acceptance changed the successor Result/Patch identity')
  await assertTaskBindingUnchanged(context)
  observations.publicPageScan = await assertSafePublicPage(page)
  await page.getByRole('button', { name: 'Start another Room' }).click()
  await page.waitForURL((url) => url.pathname === '/')
  observations.readinessUI.terminal = readinessObservation(await refreshReadinessThroughUI(page))
  observations.publicUI = assertU4BrowserMutations(context, started.run.id)
  observations.implementationGap = {
    authority: revisionRequired.verifiedReview.rejectionClass,
    predecessor, predecessorReview, successor: successorResult,
    freshSuccessor: true, unchangedFrozenBindings: true,
    successorIndependentlyVerified: true, restartBeforeAcceptance: true,
    restartReentry, acceptedResultId: accepted.verifiedReview.resultId,
  }
}

async function startVerificationThroughUI(run, action) {
  const response = await uiMutation(page, `/api/runs/${run.id}/verification`, async () => {
    await page.getByRole('button', { name: 'Start Independent Verification' }).click()
  }, 120_000, action)
  await responseJSON(response, 202, action)
  const result = await waitForRun(run.id, ['revision_required', 'awaiting_review'], 10 * 60_000)
  await proveRunCleanup(run.id, 'independent Verification terminal')
  assert(result.status === 'awaiting_review' && result.verification?.result?.outcome === 'review_ready',
    `${action} requires one exact review_ready Result; got ${result.status}/${result.verification?.result?.outcome}`)
  assertRealPi(result, action)
  return result
}

async function rejectionRouteJourney(room, planningContext) {
  const planningRun = await runToAwaitingReviewThroughUI(planningContext, 'planning-gap')
  const planningReviewResponse = await rejectResultThroughUI(planningRun, 'planning_gap',
    'The accepted plan omitted a required technical step.', 'planning_gap_review')
  const planningRejected = await responseJSON(planningReviewResponse, 200, 'planning_gap human Review')
  assert(planningRejected.status === 'revision_required' && planningRejected.verifiedReview?.planningDraftId,
    'planning_gap did not terminate the Run and create a successor Plan Draft')
  const planningExclusion = await assertRetryExcluded(planningContext, planningRejected, 'planning_gap')
  const planningRoute = await followNamedTaskCurrentAction({
    currentPage: page, room, taskTitle: planningContext.task.title,
    expectedKind: 'continue_successor_plan', expectedLabel: 'Continue - Successor Plan',
    expectedDraftId: planningRejected.verifiedReview.planningDraftId,
  })
  await page.getByRole('heading', { name: 'Edit Technical Plan Draft' }).waitFor()

  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('button', { name: room.name, exact: true }).click()
  await page.waitForURL((url) => url.pathname === `/rooms/${room.id}`)
  const contractContext = await createGovernedTaskThroughUI(room, `${scenario}-contract-change`, false)
  const contractRun = await runToAwaitingReviewThroughUI(contractContext, 'contract-change')
  const contractReviewResponse = await rejectResultThroughUI(contractRun, 'contract_change_required',
    'The requested outcome changes the accepted contract.', 'contract_change_review')
  const contractRejected = await responseJSON(contractReviewResponse, 200, 'contract_change_required human Review')
  assert(contractRejected.status === 'revision_required' && contractRejected.verifiedReview?.relatedTaskId,
    'contract_change_required did not terminate the Run and create a related Task')
  assert(contractRejected.verifiedReview.relatedTaskId !== contractContext.task.id,
    'contract_change_required reused the source Task')
  const contractExclusion = await assertRetryExcluded(contractContext, contractRejected, 'contract_change_required')
  const contractRoute = await followNamedTaskCurrentAction({
    currentPage: page, room, taskTitle: contractContext.task.title,
    expectedKind: 'open_related_task', expectedLabel: 'Continue - Open related Task',
    expectedTaskId: contractRejected.verifiedReview.relatedTaskId,
  })
  await page.getByRole('heading', { name: 'Technical Plan History', level: 2 }).waitFor()
  observations.publicPageScan = await assertSafePublicPage(page)
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('button', { name: 'Room Directory' }).click()
  await page.waitForURL((url) => url.pathname === '/')
  observations.readinessUI.terminal = readinessObservation(await refreshReadinessThroughUI(page))
  observations.publicUI = assertU4BrowserMutations(planningContext, planningRun.id, contractContext, contractRun.id)
  observations.rejectionRoutes = {
    planningGap: compactRun(planningRejected), contractChangeRequired: compactRun(contractRejected),
    planningExclusion, contractExclusion, planningRoute, contractRoute,
    twoGovernedRealTasks: planningContext.task.id !== contractContext.task.id,
  }
}

async function runToAwaitingReviewThroughUI(context, suffix) {
  const started = await startRealRunThroughUI(context, suffix)
  const awaitingVerification = await waitForRun(started.run.id, ['awaiting_verification'], 20 * 60_000)
  await proveRunCleanup(started.run.id, `${suffix} Agent terminal`)
  assertRealPi(awaitingVerification, `${suffix} Agent`)
  await page.reload({ waitUntil: 'networkidle' })
  return startVerificationThroughUI(awaitingVerification, `${suffix}_verification`)
}

async function rejectResultThroughUI(run, rejectionClass, note, action) {
  const response = await uiMutation(page, `/api/runs/${run.id}/review`, async () => {
    await page.getByLabel('Review note').fill(note)
    await page.getByLabel('Rejection class').selectOption(rejectionClass)
    await page.getByRole('button', { name: 'Reject with reason' }).click()
  }, 30_000, action)
  assertUIExpectedVersion(response, run.version, `${rejectionClass} Review`)
  return response
}

async function assertRetryExcluded(context, run, rejectionClass) {
  const before = await routeExclusionSignature(context, run)
  const response = await commandRaw('POST', `/api/runs/${run.id}/retry`, {
    expectedVersion: run.version,
    instructions: 'This forbidden request must not enter Agent Retry.',
  }, `u4-${scenario}-${rejectionClass}-forbidden-retry`, 120_000)
  assert(response.status === 409, `${rejectionClass} Retry returned HTTP ${response.status}, expected 409`)
  assertSameJSON(response.value, {
    error: `invalid application command: ${rejectionClass} blocks Agent Retry and requires its explicit successor route`,
  },
    `${rejectionClass} forbidden Retry response drifted`)
  const after = await api('GET', `/api/runs/${run.id}`, undefined, 200)
  assertSameJSON(await routeExclusionSignature(context, after), before,
    `${rejectionClass} Retry mutated lineage, state, counts, or resources`)
  await proveRunCleanup(run.id, `${rejectionClass} exclusion`)
  return { status: response.status, exactResponse: response.value, unchanged: true, expectedVersion: run.version }
}

async function uiMutation(currentPage, path, action, timeout, actionName) {
  assert(isU4Scenario, 'Chromium mutation helpers are limited to the U4 scenarios')
  const [response] = await Promise.all([
    currentPage.waitForResponse((candidate) => {
      const url = new URL(candidate.url())
      return url.pathname === path && candidate.request().method() !== 'GET'
    }, { timeout }),
    action(),
  ])
  const request = response.request()
  let expectedVersion
  try {
    const body = request.postDataJSON()
    if (Number.isSafeInteger(body?.expectedVersion)) expectedVersion = body.expectedVersion
  } catch {}
  uiMutations.push({
    action: actionName, method: request.method(), path: new URL(request.url()).pathname,
    status: response.status(), ...(expectedVersion === undefined ? {} : { expectedVersion }),
  })
  return response
}

async function responseJSON(response, expected, label) {
  const text = await response.text()
  assertNoProhibitedPublicContent(text, label)
  assert(response.status() === expected,
    `${label}: HTTP ${response.status()}, expected ${expected}: ${redact(text.slice(0, 2000))}`)
  try { return JSON.parse(text) } catch { throw new Error(`${label}: response is not JSON`) }
}

function assertCallerIdempotency(response, label) {
  const key = response.request().headers()['idempotency-key']
  assert(typeof key === 'string' && /^[A-Za-z0-9._:@/+\-=]{8,256}$/.test(key),
    `${label}: caller Idempotency-Key is absent or unsafe`)
}

function assertUIExpectedVersion(response, expectedVersion, label) {
  let body
  try { body = response.request().postDataJSON() } catch {}
  assert(Number.isSafeInteger(expectedVersion) && expectedVersion > 0,
    `${label}: observed server version is invalid`)
  assert(body?.expectedVersion === expectedVersion,
    `${label}: UI body expectedVersion ${body?.expectedVersion} did not match observed ${expectedVersion}`)
  const audit = [...uiMutations].reverse().find((item) =>
    item.method === response.request().method() && item.path === new URL(response.url()).pathname)
  assert(audit?.expectedVersion === expectedVersion,
    `${label}: UI mutation audit omitted observed expectedVersion`)
}

async function refreshReadinessThroughUI(currentPage) {
  const details = currentPage.locator('details.app-readiness')
  const summary = details.locator('summary')
  const refresh = currentPage.getByRole('button', { name: 'Refresh readiness' })
  if (!await refresh.isVisible()) await summary.click()
  const response = await uiMutation(currentPage, '/api/readiness/refresh', async () => {
    await refresh.click()
  }, 90_000, uiMutations.length === 0 ? 'initial_readiness' : 'terminal_readiness')
  const readiness = await responseJSON(response, 200, 'Refresh readiness through installed UI')
  if (await details.getAttribute('open') !== null) await summary.click()
  assertReady(readiness, 'public UI readiness')
  return readiness
}

function assertReady(readiness, label) {
  const expected = ['source_baseline', 'pi_image', 'docker_engine', 'colima', 'oauth', 'ca_proxy_model', 'verifier', 'task_worktrees', 'owned_residue']
  assert(Array.isArray(readiness.items) && readiness.items.length === expected.length,
    `${label}: readiness item set is incomplete`)
  assertSameJSON(readiness.items.map((item) => item.key), expected, `${label}: readiness item identity drift`)
  assert(readiness.items.every((item) => item.state === 'ready'), `${label}: one or more items are blocked`)
  assertFingerprint(readiness.inputFingerprint, `${label} input fingerprint`)
}

async function assertSafePublicPage(currentPage) {
  const visibleText = await currentPage.locator('body').innerText()
  assertNoProhibitedPublicContent(visibleText, 'installed public UI')
  return {
    status: 'passed', knownCredentialMatches: 0, internalHostPathMatches: 0,
    privateHomePathMatches: 0, vendorPathMatches: 0, unrelatedEnterpriseMatches: 0,
  }
}

function readinessObservation(readiness) {
  assertReady(readiness, 'U4 readiness observation')
  return {
    checkedAt: readiness.checkedAt, inputFingerprint: readiness.inputFingerprint,
    itemKeys: readiness.items.map((item) => item.key), allReady: true,
  }
}

async function navigateToNamedRoom(currentPage, expectedName) {
  await currentPage.goto(baseURL, { waitUntil: 'networkidle' })
  assert(new URL(currentPage.url()).pathname === '/', 'named Room navigation did not begin at Directory root')
  const activeRooms = currentPage.getByRole('region', { name: 'Active Rooms' })
  const card = activeRooms.locator('.room-card').filter({
    has: currentPage.getByRole('heading', { name: expectedName, exact: true }),
  })
  await card.first().waitFor({ state: 'visible', timeout: 30_000 })
  assert(await card.count() === 1, 'Directory did not expose exactly one named Room')
  await card.getByRole('button', { name: 'Open Room' }).click()
  await currentPage.waitForURL((url) => /^\/rooms\/[^/]+$/.test(url.pathname))
}

async function followNamedTaskCurrentAction({ currentPage, room, taskTitle, expectedKind, expectedLabel,
  expectedRunId, expectedDraftId, expectedTaskId }) {
  await navigateToNamedRoom(currentPage, room.name)
  const workspace = await api('GET', `/api/rooms/${room.id}/workspace`, undefined, 200)
  const matches = (workspace.tasks ?? []).filter((item) => item.title === taskTitle)
  assert(matches.length === 1, `Room workspace did not expose exactly one named Task ${taskTitle}`)
  const action = matches[0].currentAction
  assert(action?.kind === expectedKind && typeof action.url === 'string' && action.url.startsWith('/rooms/'),
    `${taskTitle} server Current Action is not ${expectedKind}`)
  if (expectedRunId) assert(action.target?.runId === expectedRunId, 'Current Action targeted the wrong Run')
  if (expectedDraftId) assert(action.target?.draftId === expectedDraftId, 'Current Action targeted the wrong successor Draft')
  if (expectedTaskId) assert(action.target?.taskId === expectedTaskId, 'Current Action targeted the wrong related Task')
  assert(Number.isSafeInteger(action.target?.expectedVersion) && action.target.expectedVersion > 0,
    'server Current Action omitted observed expectedVersion')
  const card = currentPage.locator('.task-card').filter({
    has: currentPage.getByRole('heading', { name: taskTitle, exact: true }),
  })
  await card.first().waitFor({ state: 'visible', timeout: 30_000 })
  assert(await card.count() === 1, 'Room did not expose exactly one named Task card')
  await card.getByRole('button', { name: expectedLabel }).click()
  await currentPage.waitForURL((url) => url.pathname === action.url)
  return {
    rootEntry: true, roomSelectedByName: true, taskSelectedByTitle: true,
    serverCurrentAction: expectedKind, expectedVersion: action.target.expectedVersion,
    composedDeepLinkUsed: false,
  }
}

function assertU4BrowserMutations(primaryContext, primaryRunId, secondaryContext, secondaryRunId) {
  const expectedActions = scenario === 'implementation-gap-retry' ? [
    'initial_readiness', 'create_room', `${primaryContext.slug}_create_task`,
    `${primaryContext.slug}_submit_plan`, `${primaryContext.slug}_accept_plan`,
    'implementation-gap_start_run', 'predecessor_verification', 'implementation_gap_reject',
    'implementation_gap_retry', 'successor_verification', 'successor_result_accept', 'terminal_readiness',
  ] : [
    'initial_readiness', 'create_room', `${primaryContext.slug}_create_task`,
    `${primaryContext.slug}_submit_plan`, `${primaryContext.slug}_accept_plan`,
    'planning-gap_start_run', 'planning-gap_verification', 'planning_gap_review',
    `${secondaryContext.slug}_create_task`, `${secondaryContext.slug}_submit_plan`,
    `${secondaryContext.slug}_accept_plan`, 'contract-change_start_run',
    'contract-change_verification', 'contract_change_review', 'terminal_readiness',
  ]
  assertSameJSON(uiMutations.map((item) => item.action), expectedActions,
    'accepted U4 user mutation sequence was not exactly browser driven')
  const browserMutationPaths = browserRequests.filter((request) => request.method !== 'GET')
  assertSameJSON(browserMutationPaths, uiMutations.map(({ method, path }) => ({ method, path })),
    'browser issued an unobserved public mutation')
  const retry = uiMutations.find((item) => item.action === 'implementation_gap_retry')
  if (scenario === 'implementation-gap-retry') {
    assert(Number.isSafeInteger(retry?.expectedVersion) && retry.expectedVersion > 0,
      'implementation-gap Retry audit omitted observed expectedVersion')
  }
  return {
    allAcceptedMutationsBrowserDriven: true, mutationCount: uiMutations.length,
    actions: expectedActions, primaryRunId,
    ...(secondaryRunId ? { secondaryRunId, distinctTasks: secondaryContext.task.id !== primaryContext.task.id } : {}),
  }
}

function assertRealPi(run, label) {
  assert(run?.adapter === 'pi' && run.adapter !== 'fake', `${label}: Real execution did not use Pi`)
  assert(run.attemptDetail?.sandbox?.status === 'adopted' &&
    run.attemptDetail?.sandbox?.mode === 'attempt-private-codex-only',
  `${label}: adopted real Sandbox identity is absent`)
}

function reviewReadySignature(run) {
  assert(['awaiting_review', 'accepted'].includes(run.status) &&
    run.verification?.result?.outcome === 'review_ready',
  'Run does not retain exact review_ready evidence')
  const patch = run.reviewablePatch
  assertFingerprint(patch?.patchDigest, 'Reviewable Patch digest')
  for (const [value, label] of [
    [run.verification?.bindings?.baselineDigest, 'Verification Baseline digest'],
    [run.verification?.bindings?.patchDigest, 'Verification Patch digest'],
    [run.verification?.bindings?.contextSnapshotDigest, 'Verification Snapshot digest'],
    [run.verification?.bindings?.acceptanceContractDigest, 'Verification contract digest'],
    [run.verification?.bindings?.verifierPolicyDigest, 'Verifier policy digest'],
    [patch?.baselineDigest, 'Reviewable Patch Baseline digest'],
    [patch?.declaredFilesDigest, 'Reviewable Patch declared-files digest'],
  ]) assertFingerprint(value, label)
  assert(patch.patchDigest === run.verification.bindings.patchDigest,
    'Reviewable Patch and Verification Patch digests disagree')
  for (const [value, prefix, label] of [
    [run.attemptDetail?.id, 'attempt_', 'Agent Attempt'],
    [run.agentReport?.id, 'agent_report_', 'Agent Report'],
    [run.verification?.id, 'verification_', 'Verification'],
    [run.verification?.result?.id, 'result_', 'Result'],
    [patch?.artifactId, 'artifact_', 'Patch Artifact'],
  ]) assertServerID(value, prefix, label)
  const verificationAttempt = run.verification.attempts?.find((item) => item.id === run.verification.result.attemptId)
  assert(verificationAttempt?.state === 'completed' && verificationAttempt.evidenceComplete === true &&
    verificationAttempt.cleanupProven === true, 'independent Verification is not complete and cleaned')
  return {
    attemptId: run.attemptDetail.id, agentReportId: run.agentReport.id,
    verificationId: run.verification.id, verificationAttemptId: verificationAttempt.id,
    resultId: run.verification.result.id, resultAttemptId: run.verification.result.attemptId,
    resultCreatedAt: run.verification.result.createdAt, outcome: run.verification.result.outcome,
    baselineDigest: run.verification.bindings?.baselineDigest,
    verificationPatchDigest: run.verification.bindings?.patchDigest,
    contextSnapshotDigest: run.verification.bindings?.contextSnapshotDigest,
    acceptanceContractDigest: run.verification.bindings?.acceptanceContractDigest,
    verifierPolicyDigest: run.verification.bindings?.verifierPolicyDigest,
    patchDigest: patch.patchDigest, patchArtifactId: patch.artifactId,
    patchResultId: patch.resultId, patchAgentAttemptId: patch.agentAttemptId,
    patchVerificationAttemptId: patch.verificationAttemptId,
    patchBaselineDigest: patch.baselineDigest, patchDeclaredFilesDigest: patch.declaredFilesDigest,
  }
}

function verifiedReviewSignature(review) {
  assertServerID(review?.id, 'review_decision_', 'verified Review')
  assertServerID(review?.resultId, 'result_', 'verified Review Result')
  assertFingerprint(review?.patchDigest, 'verified Review Patch digest')
  return {
    id: review.id, resultId: review.resultId, kind: review.kind, reason: review.reason,
    rejectionClass: review.rejectionClass, patchDigest: review.patchDigest,
    actorId: review.actorId, sessionId: review.sessionId, sourceTaskId: review.sourceTaskId,
    planningDraftId: review.planningDraftId, relatedTaskId: review.relatedTaskId,
    decidedAt: review.decidedAt,
  }
}

function assertPredecessorHistory(run, predecessor, predecessorReview) {
  const verification = (run.verificationHistory ?? []).find((item) => item.id === predecessor.verificationId)
  assert(verification?.agentAttemptId === predecessor.attemptId &&
    verification.result?.id === predecessor.resultId && verification.result?.outcome === predecessor.outcome,
  'successor lost the immutable predecessor Result/Verification')
  const review = (run.verifiedReviewHistory ?? []).find((item) => item.id === predecessorReview.id)
  assert(review && JSON.stringify(verifiedReviewSignature(review)) === JSON.stringify(predecessorReview),
    'successor lost or changed the immutable predecessor Review')
  const attempt = (run.attemptHistory ?? []).find((item) => item.id === predecessor.attemptId)
  assert(attempt?.artifacts?.some((artifact) => artifact.id === predecessor.patchArtifactId ||
    artifact.digest === predecessor.patchDigest), 'successor lost the immutable predecessor Patch artifact')
}

function reviewReadyCore(run) {
  return reviewReadySignature(run)
}

function immutableRetryLineage(run) {
  return {
    attempts: (run.attemptHistory ?? []).map((item) => ({
      id: item.id, sequence: item.sequence, predecessorId: item.predecessorId ?? null,
      snapshotId: item.snapshotId, snapshotDigest: item.snapshotDigest,
      artifactIds: (item.artifacts ?? []).map((artifact) => artifact.id),
    })),
    verifications: (run.verificationHistory ?? []).map((item) => ({
      id: item.id, agentAttemptId: item.agentAttemptId,
      result: item.result ? { id: item.result.id, attemptId: item.result.attemptId, outcome: item.result.outcome } : null,
      attemptIds: (item.attempts ?? []).map((attempt) => attempt.id),
    })),
    rejectedReviews: (run.verifiedReviewHistory ?? []).map(verifiedReviewSignature),
    current: reviewReadyCore(run),
  }
}

async function routeExclusionSignature(context, run) {
  const [tasks, runs, resources] = await Promise.all([
    workspaceTaskSignature(context.room.id), taskRunSignature(context.room.id, context.task.id), inventory(),
  ])
  return {
    run: {
      ...lineageSignature(run), id: run.id,
      resultId: run.verification?.result?.id,
      resultOutcome: run.verification?.result?.outcome,
      review: run.verifiedReview ? verifiedReviewSignature(run.verifiedReview) : null,
      verificationCount: run.verificationHistory?.length ?? 0,
      verifiedReviewCount: run.verifiedReviewHistory?.length ?? 0,
    },
    tasks, runs,
    resources: {
      containers: resources.containers.length, networks: resources.networks.length,
      workspaces: resources.workspaces.length,
    },
  }
}

function assertFreshSuccessor(previous, successor, run, context) {
  assert(run.attempt === 2 && run.attemptHistory?.length === 2, 'Retry did not create exactly one successor Attempt')
  assert(successor.attemptId !== previous.attemptId, 'Retry reused the predecessor Attempt')
  assert(successor.runtimeSessionId !== previous.runtimeSessionId, 'Retry resumed the predecessor Pi session')
  assert(successor.containerId !== previous.containerId, 'Retry reused the predecessor Sandbox container')
  assert(successor.workspaceId !== previous.workspaceId && successor.workspaceRoot !== previous.workspaceRoot,
    'Retry reused the predecessor execution workspace')
  assert(successor.runtimeScope === previous.runtimeScope, 'Retry escaped the installed runtime scope')
  assert(run.attemptHistory[1].predecessorId === previous.attemptId, 'Retry predecessor linkage drift')
  for (const attempt of run.attemptHistory) {
    assert(attempt.snapshotId === context.binding.snapshotId && attempt.snapshotDigest === context.binding.snapshotDigest,
      'Retry rebound the immutable Context Snapshot')
  }
  assertRunBinding(run, context)
}

function assertRunBinding(run, context) {
  assert(run?.task?.id === context.task.id && run?.room?.id === context.room.id, 'Run escaped Room-owned Task identity')
  assert(run.adapter === 'pi', `Run substituted adapter ${run.adapter}`)
  assert(run.snapshot?.id === context.binding.snapshotId && run.snapshot?.digest === context.binding.snapshotDigest,
    'Run substituted accepted Snapshot')
  assert(run.attemptDetail?.sandbox?.status === 'adopted' && run.attemptDetail?.sandbox?.mode === 'attempt-private-codex-only',
    'Run omitted adopted private Sandbox identity')
}

async function assertTaskBindingUnchanged(context) {
  const current = await api('GET', `/api/rooms/${context.room.id}/tasks/${context.task.id}`, undefined, 200)
  const acceptance = current.planning?.acceptance
  assert(acceptance?.revisionId === context.binding.revisionId && acceptance.snapshotId === context.binding.snapshotId &&
    acceptance.snapshotDigest === context.binding.snapshotDigest && acceptance.charterId === context.binding.charterId,
  'Retry changed the accepted Plan Revision/Snapshot/Charter binding')
  assert(current.repository?.baselineDigest === context.binding.baselineDigest,
    'Retry changed the installed Source Baseline binding')
  assert(sha256(Buffer.from(JSON.stringify(current.criteria))) === context.binding.contractDigest,
    'Retry changed the public acceptance contract')
}

function captureLineage(run, resource) {
  assert(run.attemptDetail?.id === resource.labels['chora.attempt_id'], 'API/container Attempt identity drift')
  assert(run.attemptDetail?.runtime?.sessionId, 'Runtime Session identity is absent')
  const workspace = resource.mounts.find((item) => item.destination === '/workspace')
  assert(workspace && insideOrSame(dataRoot, workspace.source), 'execution workspace is outside CHORA_DATA_ROOT')
  const workspaceRoot = resolve(workspace.source, '..')
  const workspaceId = resource.labels['chora.workspace_id'] || basename(workspaceRoot)
  assert(/^[A-Za-z0-9_.:@+-]{1,256}$/.test(workspaceId), 'workspace identity is absent or unsafe')
  return {
    attemptId: run.attemptDetail.id,
    runtimeSessionId: run.attemptDetail.runtime.sessionId,
    containerId: resource.id,
    workspaceId,
    workspaceRoot: relative(dataRoot, workspaceRoot),
    runtimeScope: resource.labels['chora.runtime_scope'],
  }
}

async function terminateExactlyLabeledAttempt(resource, run) {
  assert(externalFaults.length === 0, 'more than one external fault was attempted')
  const [current] = await inspectMany([resource.id], 'container')
  assert(current.id === resource.id && current.running, 'external fault target is not the exact live container')
  assert(current.name.endsWith('-attempt') && current.labels['chora.owner'] === 'dockersupervisor',
    'external fault target is outside the owned Attempt boundary')
  assert(current.labels['chora.run_id'] === run.id && current.labels['chora.task_id'] === run.task.id &&
    current.labels['chora.attempt_id'] === run.attemptDetail.id, 'external fault labels drifted')
  await dockerRun(['kill', current.id])
  externalFaults.push({
    kind: 'exactly-labeled-external-container-termination',
    containerId: current.id.slice(0, 12), name: current.name, labels: safeLabels(current.labels),
  })
}

async function command(method, path, body, expected, key, timeout = 30_000) {
  const response = await commandRaw(method, path, body, key, timeout)
  if (response.status !== expected) {
    throw new Error(`${method} ${path}: HTTP ${response.status}, expected ${expected}: ${redact(response.text.slice(0, 2000))}`)
  }
  return response.value
}

async function commandRaw(method, path, body, key, timeout = 30_000) {
  assert(typeof key === 'string' && /^[A-Za-z0-9._:-]{1,160}$/.test(key), 'caller idempotency key is absent or unsafe')
  const response = await apiRaw(method, path, body, timeout, { 'Idempotency-Key': key })
  publicCommands.push({
    method, path, idempotencyKey: key, status: response.status,
    ...(Number.isSafeInteger(body?.expectedVersion) ? { expectedVersion: body.expectedVersion } : {}),
  })
  return response
}

async function api(method, path, body, expected, headers = {}, timeout = 30_000) {
  const response = await apiRaw(method, path, body, timeout, headers)
  if (response.status !== expected) {
    throw new Error(`${method} ${path}: HTTP ${response.status}, expected ${expected}: ${redact(response.text.slice(0, 2000))}`)
  }
  return response.value
}

async function apiRaw(method, path, body, timeout = 30_000, headers = {}) {
  const response = await fetch(`${baseURL}${path}`, {
    method,
    headers: body === undefined ? headers : { 'Content-Type': 'application/json', ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(timeout),
  })
  const text = await response.text()
  assertNoProhibitedPublicContent(text, `${method} ${path}`)
  let value
  if (text !== '') {
    try { value = JSON.parse(text) } catch {}
  }
  return { status: response.status, text, value }
}

async function workspaceTaskSignature(roomId) {
  const workspace = await api('GET', `/api/rooms/${roomId}/workspace`, undefined, 200)
  return (workspace.tasks ?? [])
    .map((task) => ({ id: task.id, runCount: task.runCount }))
    .sort((left, right) => left.id.localeCompare(right.id))
}

async function taskRunSignature(roomId, taskId) {
  const history = await api('GET', `/api/rooms/${roomId}/tasks/${taskId}/runs`, undefined, 200)
  return (history.runs ?? []).map((run) => ({ id: run.id, version: run.version, status: run.status }))
}

function assertSameJSON(actual, expected, message) {
  assert(JSON.stringify(actual) === JSON.stringify(expected),
    `${message}: before=${redact(JSON.stringify(expected))} after=${redact(JSON.stringify(actual))}`)
}

async function waitForRun(id, targets, timeout) {
  const deadline = Date.now() + timeout
  let latest
  while (Date.now() < deadline) {
    latest = await api('GET', `/api/runs/${id}`, undefined, 200, {}, 10_000)
    if (targets.includes(latest.status)) return latest
    if (['accepted', 'rejected', 'cancelled'].includes(latest.status) && !targets.includes(latest.status)) {
      throw new Error(`Run ${id} reached unexpected terminal state ${latest.status}`)
    }
    await delay(250)
  }
  throw new Error(`Run ${id} did not reach ${targets.join(' or ')}; latest=${redact(JSON.stringify(compactRun(latest)))}`)
}

async function waitForAttemptResource(runId, attemptId, timeout) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    const items = await labeledContainers(runId)
    const found = items.find((item) => item.name.endsWith('-attempt') && item.running && item.labels['chora.attempt_id'] === attemptId)
    if (found) return found
    await delay(100)
  }
  throw new Error(`live Attempt resource ${attemptId} for Run ${runId} did not appear`)
}

async function proveRunCleanup(runId, label) {
  const deadline = Date.now() + 60_000
  let latest
  while (Date.now() < deadline) {
    latest = await inventory(runId)
    if (latest.containers.length === 0 && latest.networks.length === 0 && latest.workspaces.length === 0) return latest
    await delay(250)
  }
  throw new Error(`${label}: product cleanup left ${redact(JSON.stringify(latest))}`)
}

async function startService() {
  const child = spawn(binary, serveArgv, { env: process.env, stdio: ['ignore', 'pipe', 'pipe'], shell: false })
  const record = { child, stdout: '', stderr: '', starts: Date.now(), stops: [] }
  child.stdout.on('data', (chunk) => { record.stdout = appendBounded(record.stdout, redact(String(chunk))) })
  child.stderr.on('data', (chunk) => { record.stderr = appendBounded(record.stderr, redact(String(chunk))) })
  child.on('error', (error) => { record.spawnError = redact(error.message) })
  serviceHistory.push(record)
  const deadline = Date.now() + 90_000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`chora serve exited ${child.exitCode}: ${record.stderr}`)
    const response = await apiRaw('GET', '/api/status', undefined, 5_000).catch(() => undefined)
    if (response?.status === 200) return record
    await delay(100)
  }
  throw new Error(`chora serve did not become ready: ${record.stderr}`)
}

async function stopService(record, reason) {
  if (record.child.exitCode !== null) return
  record.child.kill('SIGTERM')
  const exit = await waitChild(record.child, 15_000)
  record.stops.push({ reason, signal: exit.signal, code: exit.code })
}

async function waitChild(child, timeout) {
  if (child.exitCode !== null) return { code: child.exitCode, signal: child.signalCode }
  return new Promise((resolveWait, rejectWait) => {
    const timer = setTimeout(() => rejectWait(new Error('child exit timeout')), timeout)
    child.once('exit', (code, signal) => { clearTimeout(timer); resolveWait({ code, signal }) })
  })
}

async function inventory(runId) {
  const [containers, networks, workspaces] = await Promise.all([
    runId ? labeledContainers(runId) : ownedContainers(),
    runId ? labeledNetworks(runId) : ownedNetworks(),
    workspaceResidue(),
  ])
  return {
    containers: containers.map((item) => ({ id: item.id.slice(0, 12), name: item.name, labels: safeLabels(item.labels) })),
    networks: networks.map((item) => ({ id: item.id.slice(0, 12), name: item.name, labels: safeLabels(item.labels) })),
    workspaces,
  }
}

async function waitForCleanInventory(timeout) {
  const deadline = Date.now() + timeout
  let latest
  while (Date.now() < deadline) {
    latest = await inventory()
    if (latest.containers.length === 0 && latest.networks.length === 0 && latest.workspaces.length === 0) return latest
    await delay(250)
  }
  return latest
}

function assertClean(value, message) {
  assert(value.containers.length === 0 && value.networks.length === 0 && value.workspaces.length === 0,
    `${message}: ${redact(JSON.stringify(value))}`)
}

function residueObservation(value) {
  assertClean(value, 'U4 residue observation is not clean')
  return {
    status: 'absent', labeledContainers: value.containers.length,
    labeledNetworks: value.networks.length, executionWorkspaces: value.workspaces.length,
  }
}

function scanObservation(disclosure, persisted) {
  return {
    disclosureStatus: disclosure.status,
    disclosureActualCredentialLeaks: disclosure.actualOAuthCredentialLeakFiles,
    disclosureGenericSecretMatches: disclosure.genericSecretRuleMatches,
    disclosurePrivacyMatches: disclosure.nonPlaceholderPrivacyMatches,
    disclosurePrivatePathMatches: disclosure.modelReadablePrivateHomePathOccurrences,
    disclosureEnterpriseMatches: disclosure.unrelatedEnterpriseMarkerMatches,
    disclosureForbiddenPathMatches: disclosure.forbiddenPathMatches,
    persistedStatus: persisted.status,
    persistedActualCredentialLeaks: persisted.actualOAuthCredentialLeakFiles,
    persistedGenericSecretMatches: persisted.genericSecretRuleMatches,
    persistedPrivatePathLeaks: persisted.privateHomePathLeakFiles,
    persistedEmailLeaks: persisted.nonPlaceholderEmailLeakFiles,
    persistedEnterpriseLeaks: persisted.unrelatedEnterpriseMarkerLeakFiles,
  }
}

async function ownedContainers() {
  const result = await dockerRun(['ps', '-aq', '--filter', 'label=chora.owner=dockersupervisor'])
  return inspectMany(words(result.stdout), 'container')
}

async function ownedNetworks() {
  const result = await dockerRun(['network', 'ls', '-q', '--filter', 'label=chora.owner=dockersupervisor'])
  return inspectMany(words(result.stdout), 'network')
}

async function labeledContainers(runId) {
  const result = await dockerRun(['ps', '-aq', '--filter', 'label=chora.owner=dockersupervisor', '--filter', `label=chora.run_id=${runId}`])
  return inspectMany(words(result.stdout), 'container')
}

async function labeledNetworks(runId) {
  const result = await dockerRun(['network', 'ls', '-q', '--filter', 'label=chora.owner=dockersupervisor', '--filter', `label=chora.run_id=${runId}`])
  return inspectMany(words(result.stdout), 'network')
}

function words(value) {
  return value.trim().split(/\s+/).filter(Boolean)
}

async function inspectMany(ids, type) {
  return Promise.all(ids.map(async (id) => {
    const result = await dockerRun(type === 'network' ? ['network', 'inspect', id] : ['inspect', '--type', 'container', id])
    const document = JSON.parse(result.stdout)[0]
    return {
      id: document.Id,
      name: String(document.Name ?? '').replace(/^\//, ''),
      labels: document.Config?.Labels ?? document.Labels ?? {},
      running: document.State?.Running === true,
      mounts: (document.Mounts ?? []).map((mount) => ({ source: mount.Source, destination: mount.Destination })),
    }
  }))
}

async function workspaceResidue() {
  const runtimeRoot = resolve(dataRoot, 'runtime')
  const paths = []
  await walk(runtimeRoot, async (path, entry) => {
    const rel = relative(runtimeRoot, path)
    const parts = rel.split(sep)
    if (parts.includes('artifacts') || parts.includes('baseline')) return 'skip'
    if (entry.isDirectory() && (parts[0] === 'pi' && parts.length === 2 ||
      parts[0] === 'verification' && parts[1] === 'workspaces' && parts.length === 3 && entry.name.startsWith('verification-'))) {
      paths.push(rel)
    }
  }).catch((error) => { if (error.code !== 'ENOENT') throw error })
  return paths.sort()
}

async function walk(root, visitor, count = { value: 0 }) {
  const entries = await readdir(root, { withFileTypes: true })
  for (const entry of entries) {
    count.value += 1
    assert(count.value <= 5_000, 'runtime residue inventory exceeded 5000 entries')
    const path = resolve(root, entry.name)
    const decision = await visitor(path, entry)
    if (entry.isDirectory() && decision !== 'skip') await walk(path, visitor, count)
  }
}

function dockerSpawn(args) {
  validateDockerArgs(args)
  return spawn('docker', ['--context', 'colima', ...args], { env: process.env, stdio: ['ignore', 'pipe', 'pipe'], shell: false })
}

async function dockerRun(args, timeout = 30_000) {
  const child = dockerSpawn(args)
  let stdout = ''
  let stderr = ''
  child.stdout.on('data', (chunk) => { stdout = appendBounded(stdout, String(chunk)) })
  child.stderr.on('data', (chunk) => { stderr = appendBounded(stderr, String(chunk)) })
  const exit = await waitChild(child, timeout).catch(async () => {
    child.kill('SIGTERM')
    throw new Error(`docker ${args[0]} timed out`)
  })
  if (exit.code !== 0) throw new Error(`docker ${args[0]} exited ${exit.code}: ${redact(stderr)}`)
  return { stdout, stderr }
}

function validateDockerArgs(args) {
  assert(Array.isArray(args) && args.length > 0 && args.every((value) => typeof value === 'string' && value.length > 0),
    'Docker requires direct argv')
  const allowed = ['ps', 'inspect', 'kill'].includes(args[0]) || args[0] === 'network' && ['inspect', 'ls'].includes(args[1])
  assert(allowed, `Docker command is outside exact fault/read-only inventory boundary: ${args.slice(0, 2).join(' ')}`)
  if (args[0] === 'kill') assert(scenario === 'external-agent-termination' && externalFaults.length === 0,
    'Docker kill is limited to the one external-agent-termination fault')
  assert(!args.includes('rm') && !args.includes('prune'), 'manual Docker cleanup is forbidden')
}

function compactRun(run) {
  return {
    id: run.id, taskId: run.task?.id, status: run.status, version: run.version, attempt: run.attempt,
    attemptIds: (run.attemptHistory ?? []).map((item) => item.id),
    predecessorIds: (run.attemptHistory ?? []).map((item) => item.predecessorId ?? null),
    snapshotIds: (run.attemptHistory ?? []).map((item) => item.snapshotId),
    snapshotDigests: (run.attemptHistory ?? []).map((item) => item.snapshotDigest),
    verificationOutcome: run.verification?.result?.outcome,
    rejectionClass: run.verifiedReview?.rejectionClass,
    planningDraftId: run.verifiedReview?.planningDraftId,
    relatedTaskId: run.verifiedReview?.relatedTaskId,
  }
}

function lineageSignature(run) {
  return {
    status: run.status,
    version: run.version,
    attempt: run.attempt,
    attempts: (run.attemptHistory ?? []).map((item) => ({
      id: item.id, predecessorId: item.predecessorId ?? null, state: item.state,
      snapshotId: item.snapshotId, snapshotDigest: item.snapshotDigest,
    })),
  }
}

function safeLabels(labels) {
  const allowed = [
    'chora.owner', 'chora.runtime_scope', 'chora.run_id', 'chora.attempt_id', 'chora.task_id',
    'chora.workspace_id', 'chora.policy_digest', 'chora.image_digest',
  ]
  return Object.fromEntries(allowed.filter((key) => labels[key]).map((key) => [key, labels[key]]))
}

async function writeEvidence(ids) {
  assertScenarioEvidenceReady()
  const evidence = {
    schemaVersion: isU4Scenario
      ? 'chora.m1-u4-public-real-retry-route-evidence.v1'
      : 'chora.u3-public-real-fault-evidence.v1',
    status: 'passed', scenario, ...ids,
    sourceManifestSha256: sha256(await readFile(sourceManifest)),
    process: { binary: `[INSTALL_ROOT]/${relative(installRoot, binary)}`, argv: redactArgv(serveArgv) },
    externalFaults, publicCommands, browserMutationAudit: isU4Scenario ? uiMutations : [], observations,
    serviceProcesses: serviceHistory.map((record) => ({
      pid: record.child.pid, startedAt: new Date(record.starts).toISOString(), stops: record.stops,
      stdout: record.stdout, stderr: record.stderr,
    })),
  }
  const encoded = `${JSON.stringify(evidence, null, 2)}\n`
  assert(Buffer.byteLength(encoded) <= 512 * 1024, 'evidence exceeded bounded 512 KiB envelope')
  assertNoKnownSecret(encoded)
  await mkdir(evidenceRoot, { recursive: true, mode: 0o700 })
  await chmod(evidenceRoot, 0o700)
  const evidencePath = resolve(evidenceRoot, `${scenario}.json`)
  await writeFile(evidencePath, encoded, { flag: 'wx', mode: 0o600 })
  await chmod(evidencePath, 0o600)
  const publication = `${JSON.stringify({
    status: 'passed', scenario, roomId: ids.roomId, primaryTaskId: ids.primaryTaskId,
    evidenceSha256: sha256(Buffer.from(encoded)),
  })}\n`
  assertNoProhibitedPublicContent(publication, 'stdout publication')
  process.stdout.write(publication)
}

function assertScenarioEvidenceReady() {
  const observationKey = scenarioObservationKeys[scenario]
  assert(typeof observationKey === 'string', `scenario ${scenario} has no publication observation gate`)
  assert(observations.readiness503?.exercised === false,
    `scenario ${scenario} cannot publish PASS after a readiness block`)
  assert(observations[observationKey] && typeof observations[observationKey] === 'object',
    `scenario ${scenario} cannot publish PASS without ${observationKey} evidence`)
  const expectedExternalFaults = scenario === 'external-agent-termination' ? 1 : 0
  assert(externalFaults.length === expectedExternalFaults,
    `scenario ${scenario} cannot publish PASS with ${externalFaults.length} external faults; expected ${expectedExternalFaults}`)
  if (!isU4Scenario) return
  assert(observations.publicUI?.allAcceptedMutationsBrowserDriven === true,
    `scenario ${scenario} cannot publish PASS without exact installed-UI mutation evidence`)
  for (const stage of ['initial', 'terminal']) {
    assert(observations.readinessUI?.[stage]?.allReady === true &&
      observations.readinessUI[stage].itemKeys?.length === 9,
    `scenario ${scenario} cannot publish PASS without complete ${stage} readiness`)
  }
  assert(observations.readiness?.piEnabled === true && observations.readiness?.verifierEnabled === true,
    `scenario ${scenario} cannot publish PASS without real Pi and independent Verifier readiness`)
  assert(observations.residue?.status === 'absent' && observations.residue.labeledContainers === 0 &&
    observations.residue.labeledNetworks === 0 && observations.residue.executionWorkspaces === 0,
  `scenario ${scenario} cannot publish PASS with terminal execution residue`)
  const zeroScanFields = [
    'disclosureActualCredentialLeaks', 'disclosureGenericSecretMatches', 'disclosurePrivacyMatches',
    'disclosurePrivatePathMatches', 'disclosureEnterpriseMatches', 'disclosureForbiddenPathMatches',
    'persistedActualCredentialLeaks', 'persistedGenericSecretMatches', 'persistedPrivatePathLeaks',
    'persistedEmailLeaks', 'persistedEnterpriseLeaks',
  ]
  assert(observations.scans?.disclosureStatus === 'passed' && observations.scans?.persistedStatus === 'passed',
    `scenario ${scenario} cannot publish PASS without completed disclosure/persisted scans`)
  for (const field of zeroScanFields) {
    assert(observations.scans[field] === 0,
      `scenario ${scenario} cannot publish PASS with non-zero ${field}`)
  }
  assert(observations.publicPageScan?.status === 'passed' && [
    'knownCredentialMatches', 'internalHostPathMatches', 'privateHomePathMatches',
    'vendorPathMatches', 'unrelatedEnterpriseMatches',
  ].every((field) => observations.publicPageScan[field] === 0),
  `scenario ${scenario} cannot publish PASS without a clean installed-UI content scan`)
  if (scenario === 'implementation-gap-retry') {
    const evidence = observations.implementationGap
    assert(evidence?.authority === 'implementation_gap' && evidence.freshSuccessor === true &&
      evidence.unchangedFrozenBindings === true && evidence.successorIndependentlyVerified === true &&
      evidence.restartBeforeAcceptance === true && evidence.restartReentry?.rootEntry === true &&
      evidence.restartReentry?.roomSelectedByName === true &&
      evidence.restartReentry?.taskSelectedByTitle === true &&
      evidence.restartReentry?.serverCurrentAction === 'review_result' &&
      evidence.restartReentry?.composedDeepLinkUsed === false &&
      evidence.acceptedResultId === evidence.successor?.resultId,
    'implementation-gap PASS gate lacks successor Verification/restart/re-entry/exact acceptance')
    const retryAudit = uiMutations.find((item) => item.action === 'implementation_gap_retry')
    assert(Number.isSafeInteger(retryAudit?.expectedVersion) && retryAudit.expectedVersion > 0,
      'implementation-gap PASS gate lacks observed Retry expectedVersion')
    assert(serviceHistory.length >= 2, 'implementation-gap PASS gate lacks a meaningful service restart')
  }
  if (scenario === 'rejection-route-exclusion') {
    const evidence = observations.rejectionRoutes
    assert(evidence?.twoGovernedRealTasks === true && evidence.planningExclusion?.status === 409 &&
      evidence.planningExclusion?.unchanged === true && evidence.contractExclusion?.status === 409 &&
      evidence.contractExclusion?.unchanged === true &&
      evidence.planningRoute?.serverCurrentAction === 'continue_successor_plan' &&
      evidence.planningRoute?.composedDeepLinkUsed === false &&
      evidence.contractRoute?.serverCurrentAction === 'open_related_task' &&
      evidence.contractRoute?.composedDeepLinkUsed === false,
    'rejection-route PASS gate lacks exact exclusions or Directory successor navigation')
    const forbidden = publicCommands.filter((item) => item.path.endsWith('/retry'))
    assert(forbidden.length === 2 && forbidden.every((item) => item.status === 409 &&
      Number.isSafeInteger(item.expectedVersion) && item.expectedVersion > 0),
    'rejection-route PASS gate lacks exact versioned forbidden Retry probes')
  }
}

function assertServerID(value, prefix, label) {
  assert(typeof value === 'string' && value.startsWith(prefix) && /^[A-Za-z0-9_-]{8,160}$/.test(value),
    `${label} server identity is absent or unsafe`)
}

function assertFingerprint(value, label) {
  assert(typeof value === 'string' && /^[0-9a-f]{64}$/.test(value), `${label} is not a lowercase SHA-256 digest`)
}

function redactArgv(argv) {
  const sensitiveFlags = new Set(['--auth', '--ca', '--proxy', '--model-url'])
  return argv.map((value, index) => sensitiveFlags.has(argv[index - 1]) ? '[REDACTED]' : redact(value)
	.replaceAll(installRoot, '[INSTALL_ROOT]').replaceAll(dataRoot, '[DATA_ROOT]').replaceAll(sourceRoot, '[SOURCE_ROOT]').replaceAll(repositoryRoot, '[REPOSITORY_ROOT]'))
}

function redact(value) {
  let result = String(value)
  for (const secret of knownSecrets) result = result.replaceAll(secret, '[REDACTED]')
  return result.replace(/(authorization|token|password|secret|cookie)(["'=:\s]+)[^\s,"']+/gi, '$1$2[REDACTED]')
}

function assertNoKnownSecret(value) {
  for (const secret of knownSecrets) assert(!value.includes(secret), 'evidence contains a known credential value')
}

function assertNoProhibitedPublicContent(value, label) {
  const body = String(value)
  assertNoKnownSecret(body)
  for (const path of [installRoot, dataRoot, sourceRoot, repositoryRoot, authFile, caFile]) assert(!body.includes(path), `${label} exposed an internal host path`)
  assert(!/\/(?:Users|home)\/[^/\s]+\//.test(body), `${label} exposed a private home path`)
  assert(!/(?:^|["'\s])vendor\//i.test(body), `${label} exposed vendor source metadata`)
  const enterprise = new RegExp(`(?:${[['ant', 'group'], ['ali', 'pay'], ['ali', 'baba'], ['tao', 'bao']].map((parts) => parts.join('')).join('|')})`, 'i')
  assert(!enterprise.test(body), `${label} exposed unrelated enterprise content`)
}

function collectCredentialScalars(value, output, key = '') {
  if (Array.isArray(value)) {
    for (const item of value) collectCredentialScalars(item, output, key)
    return
  }
  if (value && typeof value === 'object') {
    for (const [name, item] of Object.entries(value)) collectCredentialScalars(item, output, name)
    return
  }
  if (/^(access|refresh|account[_-]?id|user[_-]?id|email|organization|org[_-]?id)$|(?:access|refresh|id)[_-]?token|token|secret|password|cookie|credential|authorization/i.test(key) &&
    typeof value === 'string' && value.length >= 8) output.push(value)
}

function urlSecretScalars(url) {
  return [...url.searchParams.values()].filter((value) => value.length >= 4)
}

function appendBounded(current, addition) {
  const combined = current + addition
  return combined.length <= maxLogBytes ? combined : combined.slice(combined.length - maxLogBytes)
}

function insideOrSame(root, child) {
  const rel = relative(resolve(root), resolve(child))
  return rel === '' || rel !== '..' && !rel.startsWith(`..${sep}`) && !rel.startsWith(sep)
}

async function assertFile(path, name) {
  const info = await stat(path)
  assert(info.isFile(), `${name} must identify a file`)
}

function absoluteRequired(name) {
  const value = required(name)
  assert(resolve(value) === value, `${name} must be an absolute normalized path`)
  return value
}

function required(name) {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} is required`)
  return value
}

function fingerprint(value, name) {
  assert(/^[0-9a-f]{64}$/.test(value), `${name} must be a lowercase SHA-256 digest`)
  return value
}

function safeURL(value, name) {
  const url = new URL(value)
  assert(url.protocol === 'http:' || url.protocol === 'https:', `${name} must use HTTP(S)`)
  return url
}

function integer(value, name, minimum, maximum) {
  assert(/^\d+$/.test(value), `${name} must be an integer`)
  const parsed = Number(value)
  assert(Number.isSafeInteger(parsed) && parsed >= minimum && parsed <= maximum, `${name} is outside ${minimum}..${maximum}`)
  return parsed
}

function sha256(value) {
  return createHash('sha256').update(value).digest('hex')
}

function delay(milliseconds) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds))
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
}
