import { createHash, createHmac, randomBytes } from 'node:crypto'
import {
  chmod, lstat, mkdir, open, readFile, readdir, stat,
} from 'node:fs/promises'
import { dirname, isAbsolute, join, normalize, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

import { expect, test, type APIRequestContext, type Page, type Response } from '@playwright/test'

import {
  cleanupOwnedPath,
  ownedPathIdentity,
} from './o4-owned-path-cleanup.mjs'

type Profile = 'minimal' | 'standard' | 'trusted_local'
type JSONMap = Record<string, any>
const minimalToolAllowlist = Object.freeze(['read', 'bash', 'edit', 'write'])
const minimalProhibitedTools = Object.freeze(['grep', 'find', 'ls'])
const minimalCapabilityBundleId = 'chora.minimal-core.v1'
const nativeImport = new Function('specifier', 'return import(specifier)') as
  (specifier: string) => Promise<JSONMap>
let recordModulePromise: Promise<JSONMap> | undefined

function loadRecordModule() {
  recordModulePromise ??= nativeImport(pathToFileURL(
    resolve(process.cwd(), 'e2e/o4-profile-reuse-record.mjs')).href)
  return recordModulePromise
}

// The shared Playwright configuration must not register or execute this real,
// environment-mutating journey. The dedicated configuration sets this exact
// process marker before Playwright loads the spec.
if (process.env.CHORA_O4_PROFILE_REUSE_REAL === '1') {
  test('real installed profile sequence records one no-retry phase-C cohort', async ({ page, request }) => {
    test.setTimeout(90 * 60_000)
    await assertTotalSessionBinding('C')
    const evidencePaths = await prepareEvidencePaths()
    const failedResponses: string[] = []
    const mutations: Array<{ method: string; path: string; bodySha256: string }> = []
    page.on('request', (candidate) => {
      const method = candidate.method()
      if (method === 'GET' || method === 'HEAD') return
      const path = new URL(candidate.url()).pathname
      mutations.push({ method, path, bodySha256: sha256(candidate.postData() ?? '') })
    })
    page.on('response', (response) => {
      if (response.status() >= 400) failedResponses.push(`${response.status()} ${new URL(response.url()).pathname}`)
    })

    await page.goto('/', { waitUntil: 'networkidle' })
    await expect(page).toHaveURL(/\/$/)
    const room = await createRoomThroughUI(page)

    const observations: JSONMap[] = []
    const minimal = await runTaskThroughUI(page, request, room, {
      profile: 'minimal',
      sequence: 1,
      requirement: 'O4 Minimal: make one bounded documentation edit using Bash and the structured file editor; report only work actually executed.',
      apply: false,
    })
    await publishProductEvidenceBoundary(request, evidencePaths, minimal)
    await collectResourceBoundary(evidencePaths, minimal)
    observations.push(minimal)
    for (let standard = 1; standard <= 3; standard++) {
      await returnToRoomThroughDirectory(page, room.name)
      const observation = await runTaskThroughUI(page, request, room, {
        profile: 'standard',
        sequence: standard + 1,
        requirement: `O4 Standard ${standard}: make one distinct bounded repository change under the full repository and security policy.`,
        apply: standard === 1,
      })
      await publishProductEvidenceBoundary(request, evidencePaths, observation)
      await collectResourceBoundary(evidencePaths, observation)
      observations.push(observation)
    }

    await returnToRoomThroughDirectory(page, room.name)
    const acknowledgementBefore = await readCurrentAcknowledgement(request)
    const trusted = await runTaskThroughUI(page, request, room, {
      profile: 'trusted_local',
      sequence: 5,
      requirement: 'O4 Trusted Local: run the bounded task with compatible local Pi and return one governed Chora Result with No Sandbox.',
      apply: false,
      acknowledgementBefore,
    })
    await publishProductEvidenceBoundary(request, evidencePaths, trusted)
    await collectResourceBoundary(evidencePaths, trusted)
    observations.push(trusted)

    expect(failedResponses, 'any failed UI/public request aborts the environment').toEqual([])
    assertMutationBoundary(mutations, room.id, observations)
    assertWholeSequence(observations)
    await producePhaseCEvidence(evidencePaths, observations)
  })
}

async function createRoomThroughUI(page: Page) {
  const name = `O4 profile reuse ${Date.now()}`
  const response = await uiMutation(page, /^\/api\/rooms$/, async () => {
    await page.getByRole('button', { name: '＋ New Room' }).click()
    await page.getByLabel('Room name').fill(name)
    const description = page.getByLabel('Description')
    if (await description.count()) await description.fill('One continuous installed O4-C profile/reuse cohort.')
    await page.getByRole('button', { name: 'Create Room', exact: true }).click()
  })
  expect(response.status()).toBe(201)
  const room = await safeJSON(response, 'Room creation')
  expect(room.id).toMatch(/^room_/)
  await expect(page).toHaveURL(/\/rooms\//)
  return { id: String(room.id), name }
}

async function returnToRoomThroughDirectory(page: Page, roomName: string) {
  // Re-enter from the product root and choose the Room by visible identity.
  // No previously captured Task/Run ID is composed into a deep link.
  await page.goto('/', { waitUntil: 'networkidle' })
  await page.getByText(roomName, { exact: true }).first().click()
  await expect(page.getByRole('button', { name: '＋ New Task' }).first()).toBeVisible()
}

async function runTaskThroughUI(page: Page, request: APIRequestContext, room: { id: string; name: string }, input: {
  profile: Profile
  sequence: number
  requirement: string
  apply: boolean
  acknowledgementBefore?: JSONMap
}) {
  await page.getByRole('button', { name: '＋ New Task' }).first().click()
  const profileLabel = input.profile === 'minimal' ? 'Minimal' :
    input.profile === 'standard' ? 'Standard' : 'Trusted Local · No Sandbox'
  if (input.profile === 'trusted_local') {
    assertCurrentAcknowledgementState(input.acknowledgementBefore)
    await page.getByRole('radio', { name: profileLabel }).click()
    const disclosure = page.getByRole('dialog', { name: 'Trusted Local · No Sandbox' })
    if (await disclosure.count()) {
      throw new Error('Trusted Local disclosure reopened after the current acknowledgement was read')
    }
    const acknowledgementAfter = await readCurrentAcknowledgement(request)
    expect(acknowledgementAfter.acknowledged).toBe(true)
    expect(acknowledgementAfter.policyVersion).toBe(input.acknowledgementBefore?.policyVersion)
    expect(acknowledgementAfter.acknowledgedAt).toBe(input.acknowledgementBefore?.acknowledgedAt)
  } else {
    await page.getByRole('radio', { name: profileLabel, exact: true }).click()
  }
  await expect(page.getByRole('radio', { name: profileLabel, exact: input.profile !== 'trusted_local' })).toBeChecked()

  const create = await uiMutation(page, new RegExp(`^/api/rooms/${escapeRE(room.id)}/tasks$`), async () => {
    await page.getByLabel('What should Chora build?').fill(input.requirement)
    await page.getByRole('button', { name: 'Start', exact: true }).click()
  })
  expect(create.status()).toBe(201)
  const task = await safeJSON(create, `Task ${input.sequence} creation`)
  expect(task.id).toMatch(/^task_[0-9a-f-]+$/)
  expect(task.agentExecutionProfile?.profile ?? task.agentExecutionProfile).toBe(input.profile)

  const submit = await uiMutation(page, new RegExp(`^/api/tasks/${escapeRE(task.id)}/plan/drafts/[^/]+/submit$`), async () => {
    await page.getByRole('button', { name: 'Submit Revision' }).click()
  })
  expect(submit.status()).toBe(200)
  const submitted = await safeJSON(submit, `Task ${input.sequence} Plan submission`)
  expect(submitted.revision?.revisionNumber).toBe(1)

  const acceptPlan = await uiMutation(page,
    new RegExp(`^/api/tasks/${escapeRE(task.id)}/plan/revisions/[^/]+/reviews$`), async () => {
      await page.getByLabel('Planning review note').fill('Accept this exact bounded O4-C profile task without requesting a revision.')
      await page.getByRole('button', { name: 'Accept Revision' }).click()
    })
  expect(acceptPlan.status()).toBe(200)
  const acceptedPlan = await safeJSON(acceptPlan, `Task ${input.sequence} Plan acceptance`)
  expect(acceptedPlan.review?.kind).toBe('accept')

  const startRun = await uiMutation(page, new RegExp(`^/api/tasks/${escapeRE(task.id)}/runs$`), async () => {
    await page.getByRole('button', { name: 'Start Bound Pi Run' }).click()
  }, 120_000)
  expect(startRun.status()).toBe(201)
  const started = await safeJSON(startRun, `Task ${input.sequence} Run start`)
  expect(started.id).toMatch(/^run_[0-9a-f-]+$/)
  expect(started.attempt).toBe(1)

  const runPath = `/api/rooms/${encodeURIComponent(room.id)}/tasks/${encodeURIComponent(task.id)}/runs/${encodeURIComponent(started.id)}`
  const reviewReady = await waitForRun(request, runPath, input.sequence)
  assertProfileView(reviewReady, input.profile)
  expect(reviewReady.attempt).toBe(1)
  expect(reviewReady.attemptDetail?.sequence).toBe(1)
  expect(reviewReady.attemptDetail?.state).toBe('output_submitted')
  expect(reviewReady.verification?.state).toBe('completed')
  expect(reviewReady.verification?.attempts).toHaveLength(1)
  expect(reviewReady.verification.attempts[0]).toMatchObject({ sequence: 1, state: 'completed', evidenceComplete: true, cleanupProven: true })
  expect(reviewReady.attemptHistory ?? []).toHaveLength(1)
  expect(reviewReady.reviewablePatch?.patchDigest).toMatch(/^[0-9a-f]{64}$/)
  expect(reviewReady.agentReport?.attemptId).toBe(reviewReady.attemptDetail.id)
  expect(String(reviewReady.agentReport?.finalText ?? '')).not.toHaveLength(0)
  const minimalDenialDigest = input.profile === 'minimal' ? assertMinimalEvidence(reviewReady) : null

  let terminal = reviewReady
  if (input.apply) {
    const acceptAndApply = await Promise.all([
      page.waitForResponse((candidate) => new URL(candidate.url()).pathname === `/api/runs/${started.id}/review`),
      page.waitForResponse((candidate) => new URL(candidate.url()).pathname === `/api/runs/${started.id}/apply`),
      page.getByRole('button', { name: 'Accept & Apply' }).click(),
    ])
    expect(acceptAndApply[0].status()).toBe(200)
    expect(acceptAndApply[1].status()).toBe(200)
    terminal = await safeJSON(acceptAndApply[1], 'Standard 1 Task-owned Apply')
    expect(terminal.status).toBe('accepted')
    expect(terminal.verifiedReview?.kind).toBe('accept')
    expect(terminal.patchApplication).toMatchObject({ state: 'applied', patchDigest: reviewReady.reviewablePatch.patchDigest })
    expect(terminal.patchApplication?.targetIdentity).toMatch(/^sha256:[0-9a-f]{64}$/)
  }

  return {
    sequence: input.sequence,
    profile: input.profile,
    taskId: task.id,
    runId: started.id,
    attemptId: reviewReady.attemptDetail.id,
    attemptSequence: reviewReady.attemptDetail.sequence,
    attemptState: reviewReady.attemptDetail.state,
    workspaceIdentity: reviewReady.verification.attempts[0].workspaceIdentity,
    verificationAttemptId: reviewReady.verification.attempts[0].id,
    runtimeFingerprint: reviewReady.attemptDetail.runtime?.fingerprint,
    publicStartedAt: publicExecutionTimes(reviewReady).startedAt,
    publicCompletedAt: publicExecutionTimes(reviewReady).completedAt,
    agentExecution: reviewReady.agentExecution,
    sandbox: reviewReady.attemptDetail.sandbox,
    minimalDenialDigest,
    acknowledgement: input.profile === 'trusted_local' ? input.acknowledgementBefore : null,
    patchDigest: reviewReady.reviewablePatch.patchDigest,
    applied: input.apply,
    applyTargetIdentity: terminal.patchApplication?.targetIdentity ?? null,
    actionDigest: sha256(JSON.stringify({ task: input.requirement, profile: input.profile, applied: input.apply })),
    observationDigest: productObservationDigest('C', input.sequence, terminal),
  }
}

async function waitForRun(request: APIRequestContext, path: string, sequence: number) {
  const deadline = Date.now() + 30 * 60_000
  while (Date.now() < deadline) {
    const response = await request.get(path)
    expect(response.ok()).toBeTruthy()
    const run = await response.json() as JSONMap
    if (['revision_required', 'verification_recovery_required', 'recovery_required', 'cancelled', 'failed'].includes(run.status)) {
      throw new Error(`Task ${sequence} entered forbidden no-retry state ${run.status}`)
    }
    if ((run.attempt ?? 0) !== 1 || (run.attemptHistory?.length ?? 1) !== 1) {
      throw new Error(`Task ${sequence} acquired an unexpected Attempt/retry`)
    }
    if (run.status === 'awaiting_review') return run
    await new Promise((resolve) => setTimeout(resolve, 1_000))
  }
  throw new Error(`Task ${sequence} did not reach independently verified review readiness`)
}

async function readCurrentAcknowledgement(request: APIRequestContext) {
  const response = await request.get('/api/agent-execution/trusted-local-acknowledgements/current')
  expect(response.ok()).toBeTruthy()
  return response.json() as Promise<JSONMap>
}

function assertCurrentAcknowledgementState(value: JSONMap | undefined) {
  expect(value).toBeTruthy()
  expect(value?.acknowledged, 'missing or stale Trusted Local acknowledgement aborts phase C').toBe(true)
  expect(value?.policyVersion).toMatch(/^chora\.trusted-local-disclosure\.v[1-9][0-9]*$/)
  expect(Date.parse(String(value?.acknowledgedAt))).toBeGreaterThan(0)
}

function assertMinimalEvidence(run: JSONMap) {
  const trajectory = run.trajectory ?? []
  expect(trajectory.some((step: JSONMap) => step.kind === 'command' &&
    /(?:^|\s)(?:bash|sh)(?:\s|$)/i.test(JSON.stringify(step.details ?? step.summary ?? ''))),
  'Minimal did not expose one observed Bash operation').toBe(true)
  expect(trajectory.some((step: JSONMap) => step.kind === 'edit'),
    'Minimal did not expose one observed edit').toBe(true)
  const modelVisible = JSON.stringify({ trajectory, finalText: run.agentReport?.finalText ?? '' })
  expect(modelVisible, 'model-authored denial text is not capability-enforcement evidence')
    .not.toMatch(/prohibited[- _]?capability|capability denied/i)
  const observedTools = trajectory.flatMap((step: JSONMap) =>
    (step.details ?? []).filter((detail: JSONMap) => detail.label === 'Tool')
      .map((detail: JSONMap) => String(detail.value).toLowerCase()))
  expect(observedTools.length, 'Minimal trajectory did not expose tool identity').toBeGreaterThan(0)
  expect(observedTools.every((tool: string) => minimalToolAllowlist.includes(tool)),
    `Minimal exposed a tool outside its exact allowlist: ${observedTools.join(',')}`).toBe(true)
  expect(observedTools.some((tool: string) => tool === 'bash')).toBe(true)
  expect(observedTools.some((tool: string) => ['edit', 'write'].includes(tool))).toBe(true)
  expect(observedTools.some((tool: string) => minimalProhibitedTools.includes(tool))).toBe(false)
  const fingerprint = String(run.attemptDetail?.runtime?.fingerprint ?? '')
  expect(fingerprint).toMatch(/^[0-9a-f]{64}$/)
  return sha256(canonicalJSONStringify({
    schemaVersion: 'chora.minimal-capability-enforcement.v1',
    profile: run.agentExecution?.profile,
    capabilityPolicy: run.agentExecution?.capabilityPolicy,
    capabilityBundleId: minimalCapabilityBundleId,
    allowedTools: minimalToolAllowlist,
    prohibitedTools: minimalProhibitedTools,
    runtimeFingerprint: fingerprint,
    enforcementBoundary: 'adapter_and_docker_supervisor_before_model',
  }))
}

function publicExecutionTimes(run: JSONMap) {
  const values = (run.trajectory ?? []).flatMap((step: JSONMap) =>
    [step.startedAt, step.endedAt, step.time].filter((value) =>
      typeof value === 'string' && Number.isFinite(Date.parse(value))))
  expect(values.length, 'public trajectory lacks execution timestamps').toBeGreaterThan(0)
  const timestamps = values.map((value: string) => Date.parse(value))
  return {
    startedAt: new Date(Math.min(...timestamps)).toISOString(),
    completedAt: new Date(Math.max(...timestamps)).toISOString(),
  }
}

function assertProfileView(run: JSONMap, profile: Profile) {
  const managed = profile !== 'trusted_local'
  expect(run.adapter).toBe('pi')
  expect(run.agentExecution).toMatchObject({
    profile,
    runtimeSource: managed ? 'managed_pi_image' : 'local_pi',
    executionProvider: managed ? 'docker' : 'trusted_host',
    capabilityPolicy: profile === 'minimal' ? 'chora.minimal.v1' : profile === 'standard' ? 'chora.standard.v1' : 'pi.native',
    sandboxed: managed,
  })
  expect(run.attemptDetail?.sandbox?.status).toBe(managed ? 'managed' : 'unavailable')
  if (managed) {
    expect(run.attemptDetail.executionWorkspace).toBe('/workspace/repository')
    expect(run.attemptDetail.sandbox.provider).toBe('docker')
  } else {
    expect(run.attemptDetail.executionWorkspace).toBe('trusted local workspace (path hidden)')
    expect(JSON.stringify(run)).not.toMatch(/managed_pi_image|\/workspace\/repository/)
  }
}

function assertWholeSequence(observations: JSONMap[]) {
  expect(observations.map(({ profile }) => profile)).toEqual([
    'minimal', 'standard', 'standard', 'standard', 'trusted_local',
  ])
  expect(observations.map(({ sequence }) => sequence)).toEqual([1, 2, 3, 4, 5])
  for (const field of ['taskId', 'runId', 'attemptId', 'workspaceIdentity', 'actionDigest', 'observationDigest']) {
    expect(new Set(observations.map((item) => item[field])).size, `${field} must be distinct`).toBe(5)
  }
  expect(new Set(observations.slice(0, 4).map((item) => item.runtimeFingerprint)).size,
    'managed profiles must reuse one source-only Pi Runtime fingerprint').toBe(1)
  expect(observations[4].runtimeFingerprint,
    'Trusted Local must bind its selected local Pi source independently').not.toBe(observations[0].runtimeFingerprint)
  expect(observations.every(({ attemptSequence, attemptState }) =>
    attemptSequence === 1 && attemptState === 'output_submitted')).toBe(true)
  expect(observations.filter(({ applied }) => applied).map(({ sequence }) => sequence)).toEqual([2])
  expect(observations[1].applyTargetIdentity).toMatch(/^sha256:/)
}

function assertMutationBoundary(mutations: Array<{ method: string; path: string }>, roomId: string, observations: JSONMap[]) {
  const ids = new Set(observations.flatMap(({ taskId, runId }) => [taskId, runId]))
  for (const mutation of mutations) {
    expect(mutation.path).not.toMatch(/^\/__e2e\//)
    expect(mutation.path).not.toMatch(/\/retry(?:\/|$)|agent-execution-profile/)
    const allowed = mutation.path === '/api/rooms' ||
      mutation.path === `/api/rooms/${roomId}/tasks` ||
      /\/plan\/(?:drafts\/[^/]+\/(?:submit)|revisions\/[^/]+\/reviews)$/.test(mutation.path) ||
      /\/api\/tasks\/[^/]+\/runs$/.test(mutation.path) ||
      /\/api\/runs\/[^/]+\/(?:verification|review|apply)$/.test(mutation.path)
    expect(allowed, `unexpected UI mutation ${mutation.method} ${mutation.path}`).toBe(true)
    const pathIDs = mutation.path.split('/').filter((part) => /^(?:task|run)_/.test(part))
    expect(pathIDs.every((id) => ids.has(id)), `mutation reused an unknown Task/Run identity`).toBe(true)
  }
  expect(mutations.filter(({ path }) => path.endsWith('/apply'))).toHaveLength(1)
  expect(mutations.filter(({ path }) => path.endsWith('/review'))).toHaveLength(1)
}

type EvidencePaths = {
  tupleFile: string
  sourceA3LedgerFile: string
  handoffReceiptDirectory: string
  observerFile: string
  observerSha256: string
  supervisorResourceDirectory: string
  runnerProtocolDirectory: string
  observerRequestDirectory: string
  observerAckDirectory: string
  runnerRequestDirectory: string
  runnerAckDirectory: string
  runnerResourceDirectory: string
  outputDirectory: string
  receiptDirectory: string
  resourceLedgerDirectory: string
  tupleIdentity: string
  repositoryCapture: { executableFile: string; executableSha256: string }
}

const resourceNames = [
  '01-minimal-resources.json',
  '02-standard-1-resources.json',
  '03-standard-2-resources.json',
  '04-standard-3-resources.json',
  '05-trusted-local-resources.json',
]
const receiptNames = [
  '01-minimal.json', '02-standard-1.json', '03-standard-2.json',
  '04-standard-3.json', '05-trusted-local.json',
]

async function prepareEvidencePaths(): Promise<EvidencePaths> {
  const recordModule = await loadRecordModule()
  const tupleFile = requiredAbsolutePath('CHORA_O4_PROFILE_REUSE_TUPLE_FILE')
  const sourceA3LedgerFile = requiredAbsolutePath('CHORA_O4_PROFILE_REUSE_SOURCE_A3_LEDGER_FILE')
  const handoffReceiptDirectory = requiredAbsolutePath('CHORA_O4_PROFILE_REUSE_HANDOFF_RECEIPT_DIR')
  const observerFile = requiredAbsolutePath('CHORA_O4_PROFILE_REUSE_OBSERVER_FILE')
  const observerSha256 = requiredDigest('CHORA_O4_PROFILE_REUSE_OBSERVER_SHA256')
  const supervisorResourceDirectory = requiredAbsolutePath('CHORA_O4_PROFILE_REUSE_SUPERVISOR_RESOURCE_DIR')
  const runnerProtocolDirectory = requiredAbsolutePath('CHORA_O4_PROFILE_REUSE_RUNNER_PROTOCOL_DIR')
  const outputDirectory = requiredAbsolutePath('CHORA_O4_PROFILE_REUSE_OUTPUT_DIR')
  const repositoryCapture = Object.freeze({
    executableFile: requiredAbsolutePath('CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_FILE'),
    executableSha256: requiredDigest('CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_SHA256'),
  })
  const prepared = await recordModule.prepareProfileReuseEvidencePaths({
    tupleFile, sourceA3LedgerFile, handoffReceiptDirectory, observerFile, observerSha256,
    supervisorResourceDirectory, runnerProtocolDirectory, outputDirectory,
    requireAbsentSourceA3: true,
  })
  const tuple = await readJSONInput(tupleFile, 'B2 tuple boundary identity', 4 * 1024 * 1024, 0o400)
  expect(tuple.value.identity).toMatch(/^[0-9a-f]{64}$/)
  return Object.freeze({ ...prepared, tupleIdentity: tuple.value.identity,
    repositoryCapture }) as EvidencePaths
}

async function collectResourceBoundary(paths: EvidencePaths, observation: JSONMap) {
  const recordModule = await loadRecordModule()
  observation.attestation = await recordModule.collectProfileReuseResourceAtBoundary({
    paths,
    sequence: observation.sequence,
    journey: observation.sequence === 1 ? 'minimal' :
      observation.sequence <= 4 ? `standard_${observation.sequence - 1}` : 'trusted_local',
    profile: observation.profile,
    tupleIdentity: paths.tupleIdentity,
    taskId: observation.taskId,
    runId: observation.runId,
    attemptId: observation.attemptId,
    workspaceIdentity: observation.workspaceIdentity,
    publicObservationDigest: observation.observationDigest,
    publicCompletedAt: observation.publicCompletedAt,
    runtimeFingerprint: observation.runtimeFingerprint,
    requestNonce: randomBytes(32).toString('hex'),
    repositoryCapture: paths.repositoryCapture,
  })
}

async function publishProductEvidenceBoundary(request: APIRequestContext, paths: EvidencePaths,
  observation: JSONMap) {
  const authorityFile = requiredAbsolutePath('CHORA_O4_TOTAL_SESSION_AUTHORITY_FILE')
  const authorityInput = await readJSONInput(authorityFile, 'O4 evidence Runner session authority',
    4 * 1024 * 1024, 0o400)
  const authority = authorityInput.value
  expect(authority).toMatchObject({
    schemaVersion: 'chora.m1-o4-runner-session-authority.v1', status: 'exclusive',
    tupleIdentity: paths.tupleIdentity,
  })
  expect(authority.sessionId).toMatch(/^[0-9a-f]{64}$/)
  expect(authority.sessionSecret).toMatch(/^[0-9a-f]{64}$/)
  const selectors = {
    phase: 'C', sequence: observation.sequence, taskId: observation.taskId,
    runId: observation.runId, attemptId: observation.attemptId,
    observationDigest: observation.observationDigest,
  }
  const body = {
    ...selectors, sessionId: authority.sessionId, tupleIdentity: paths.tupleIdentity,
    generationId: authority.identity?.generationId,
    mac: createHmac('sha256', authority.sessionSecret)
      .update(canonicalJSONStringify(selectors)).digest('hex'),
  }
  const response = await request.post('/api/o4/evidence-boundaries', { data: body })
  expect(response.status(), 'product evidence boundary publication').toBe(201)
  const value = await response.json() as JSONMap
  expect(Object.keys(value).sort()).toEqual([
    'schemaVersion', 'status', 'phase', 'sequence', 'observationDigest',
    'operationLedgerSha256', 'operationLedgerCount', 'a3LedgerSha256',
    'verifierLedgerSha256', 'resourcePath', 'resourceSha256',
  ].sort())
  expect(value).toMatchObject({
    schemaVersion: 'chora.m1-o4-evidence-boundary-response.v1', status: 'published',
    phase: 'C', sequence: observation.sequence,
    observationDigest: observation.observationDigest,
    resourcePath: join(paths.supervisorResourceDirectory,
      resourceNames[observation.sequence - 1]),
  })
  for (const field of ['operationLedgerSha256', 'a3LedgerSha256',
    'verifierLedgerSha256', 'resourceSha256']) expect(value[field]).toMatch(/^[0-9a-f]{64}$/)
  observation.evidenceBoundary = value
}

function productObservationDigest(phase: 'C', sequence: number, run: JSONMap) {
  const runtime = run.attemptDetail?.runtime
  const projectedRuntime = runtime ? {
    sessionId: String(runtime.sessionId ?? ''), adapterId: String(runtime.adapterId ?? ''),
    kind: String(runtime.kind ?? ''), version: String(runtime.version ?? ''),
    fingerprint: String(runtime.fingerprint ?? ''),
    ...(runtime.externalSession ? { externalSession: String(runtime.externalSession) } : {}),
    state: String(runtime.state ?? ''),
  } : null
  const verification = run.verification ? {
    id: String(run.verification.id ?? ''), state: String(run.verification.state ?? ''),
    bindings: run.verification.bindings,
    attempts: (run.verification.attempts ?? []).map((attempt: JSONMap) => ({
      id: String(attempt.id ?? ''), sequence: attempt.sequence,
      state: String(attempt.state ?? ''), evidenceComplete: attempt.evidenceComplete === true,
      cleanupProven: attempt.cleanupProven === true,
      workspaceIdentity: String(attempt.workspaceIdentity ?? ''), reason: String(attempt.reason ?? ''),
    })),
    result: run.verification.result ? {
      id: String(run.verification.result.id ?? ''),
      attemptId: String(run.verification.result.attemptId ?? ''),
      outcome: String(run.verification.result.outcome ?? ''),
    } : null,
  } : null
  return sha256(canonicalJSONStringify({
    schemaVersion: 'chora.m1-o4-product-observation.v1', phase, sequence,
    taskId: String(run.task?.id ?? ''), runId: String(run.id ?? ''),
    attemptId: String(run.attemptDetail?.id ?? ''),
    run: { state: String(run.status ?? ''), version: run.version,
      terminalReason: String(run.terminalReason ?? '') },
    attempt: { sequence: run.attemptDetail?.sequence ?? 0,
      state: String(run.attemptDetail?.state ?? ''),
      profile: String(run.attemptDetail?.agentExecution?.profile ?? ''), runtime: projectedRuntime },
    verification,
    patch: {
      reviewablePatchDigest: String(run.reviewablePatch?.patchDigest ?? ''),
      applicationState: String(run.patchApplication?.state ?? ''),
      applicationPatchDigest: String(run.patchApplication?.patchDigest ?? ''),
      targetIdentity: String(run.patchApplication?.targetIdentity ?? ''),
    },
  }))
}

async function producePhaseCEvidence(paths: EvidencePaths, observations: JSONMap[]) {
  const {
    PROFILE_REUSE_MANIFEST_SCHEMA, PROFILE_REUSE_RECEIPT_SCHEMA,
    buildProfileReuseRecord, computeProfileExecutionIdentityDigest,
    computeProfileRoleTupleDigest, formProfileReuseManifest, formProfileReuseReceipt,
    persistProfileReuseManifest, persistProfileReuseReceipt,
  } = await loadRecordModule()
  const tupleInput = await readJSONInput(paths.tupleFile, 'B2 tuple', 4 * 1024 * 1024, 0o400)
  const sourceInput = await readJSONInput(paths.sourceA3LedgerFile, 'current source A3', 4 * 1024 * 1024)
  const tuple = tupleInput.value
  const source = sourceInput.value
  expect(tuple.identity).toMatch(/^[0-9a-f]{64}$/)
  expect(source).toMatchObject({
    status: 'recording', complete: false, auditSealed: false,
    environmentId: tuple.environmentId, installId: tuple.installId,
    generationId: tuple.generationId, tupleIdentity: tuple.identity,
  })
  expect(source.commandAudit).toHaveLength(source.auditRecordCount)
  expect(source.commandAudit.at(-1)?.digest).toBe(source.auditFinalDigest)
  expect(source.attempts).toHaveLength(4)
  expect(source.attempts.map((attempt: JSONMap) => attempt.profile)).toEqual([
    'minimal', 'standard', 'standard', 'standard',
  ])

  const actualResourceNames = (await readdir(paths.runnerResourceDirectory)).sort()
  expect(actualResourceNames).toEqual([...resourceNames].sort())
  const requestNames = observations.map(({ sequence }) =>
    `${String(sequence).padStart(2, '0')}-${sequence === 1 ? 'minimal' : sequence <= 4 ? `standard-${sequence - 1}` : 'trusted-local'}-request.json`)
  const ackNames = requestNames.map((name) => name.replace(/-request\.json$/, '-ack.json'))
  expect((await readdir(paths.runnerRequestDirectory)).sort()).toEqual([...requestNames].sort())
  expect((await readdir(paths.runnerAckDirectory)).sort()).toEqual([...ackNames].sort())
  const resources: Array<{ value: JSONMap; sha256: string }> = []
  const attestations: JSONMap[] = []
  for (let index = 0; index < resourceNames.length; index++) {
    const inputPath = join(paths.runnerResourceDirectory, resourceNames[index])
    const resource = await readJSONInput(inputPath, `Runner resource ${index + 1}`, 512 * 1024, 0o400)
    const outputPath = join(paths.resourceLedgerDirectory, resourceNames[index])
    await writeExclusiveReadonlyBytes(resource.bytes, outputPath, `raw resource ${index + 1}`,
      paths.repositoryCapture)
    const outputBytes = await readExactFile(outputPath, `raw resource ${index + 1}`, 512 * 1024, 0o400)
    expect(sha256Bytes(outputBytes)).toBe(resource.sha256)
    resources.push(resource)
    const requestInputPath = join(paths.runnerRequestDirectory, requestNames[index])
    const ackInputPath = join(paths.runnerAckDirectory, ackNames[index])
    const request = await readJSONInput(requestInputPath, `Runner observer request ${index + 1}`, 128 * 1024, 0o400)
    const ack = await readJSONInput(ackInputPath, `Runner observer acknowledgement ${index + 1}`, 128 * 1024, 0o400)
    const requestOutputPath = join(paths.observerRequestDirectory, requestNames[index])
    const ackOutputPath = join(paths.observerAckDirectory, ackNames[index])
    await writeExclusiveReadonlyBytes(request.bytes, requestOutputPath,
      `raw observer request ${index + 1}`, paths.repositoryCapture)
    await writeExclusiveReadonlyBytes(ack.bytes, ackOutputPath,
      `raw observer acknowledgement ${index + 1}`, paths.repositoryCapture)
    attestations.push({ request, ack })
  }

  const roleTupleDigest = computeProfileRoleTupleDigest(source.roleImages)
  const receiptIndex: Array<{ sequence: number; file: string; sha256: string }> = []
  const resourceIndex: Array<{ sequence: number; file: string; sha256: string }> = []
  for (let index = 0; index < observations.length; index++) {
    const sequence = index + 1
    const observation = observations[index]
    const resource = resources[index]
    const attestation = attestations[index]
    expect(observation.attestation).toMatchObject({
      resourceSha256: resource.sha256,
      requestSha256: attestation.request.sha256,
      ackSha256: attestation.ack.sha256,
      observerSha256: paths.observerSha256,
      requestDigest: attestation.request.value.requestDigest,
      ackDigest: attestation.ack.value.ackDigest,
    })
    expect(resource.value).toMatchObject({
      taskId: observation.taskId,
      runId: observation.runId,
      attemptId: observation.attemptId,
      tupleIdentity: tuple.identity,
      profile: observation.profile,
    })
    const managed = observation.profile !== 'trusted_local'
    const sourceAttempt = managed ? source.attempts[index] : null
    if (managed) {
      expect(sourceAttempt).toMatchObject({
        taskId: observation.taskId,
        runId: observation.runId,
        attemptId: observation.attemptId,
        attemptState: 'output_submitted',
        terminalReason: null,
        frameworkRetryCount: 0,
        hiddenRetryCount: 0,
        resourceLedgerSha256: resource.sha256,
      })
    }
    const identity = managed ? sourceAttempt : resource.value
    const profileEvidence = managed ? managedProfileEvidence(observation, sourceAttempt) :
      trustedProfileEvidence(observation, resource.value)
    const receipt = formProfileReuseReceipt({
      schemaVersion: PROFILE_REUSE_RECEIPT_SCHEMA,
      status: 'passed',
      sequence,
      journey: sequence === 1 ? 'minimal' : sequence <= 4 ? `standard_${sequence - 1}` : 'trusted_local',
      profile: observation.profile,
      cohortSequence: observation.profile === 'standard' ? sequence - 1 : null,
      environmentId: tuple.environmentId,
      installId: tuple.installId,
      generationId: tuple.generationId,
      platform: tuple.platform,
      bindingDigest: source.bindingDigest,
      tupleIdentity: tuple.identity,
      taskId: observation.taskId,
      runId: observation.runId,
      attemptId: observation.attemptId,
      workspaceIdentity: identity.workspaceIdentity,
      executionIdentityDigest: computeProfileExecutionIdentityDigest({
        tupleIdentity: tuple.identity,
        taskId: observation.taskId,
        runId: observation.runId,
        attemptId: observation.attemptId,
        workspaceIdentity: identity.workspaceIdentity,
      }),
      attemptSequence: 1,
      attemptState: 'output_submitted',
      terminalReason: null,
      runtimeAdapterId: 'pi',
      runtimeSource: observation.agentExecution.runtimeSource,
      executionProvider: observation.agentExecution.executionProvider,
      capabilityPolicy: observation.agentExecution.capabilityPolicy,
      sandboxState: managed ? 'managed' : 'none',
      managedSandbox: managed,
      managedFallbackUsed: false,
      frameworkRetryCount: 0,
      hiddenRetryCount: 0,
      attemptSource: managed ? {
        startSequence: sourceAttempt.auditStartSequence,
        endSequence: sourceAttempt.auditEndSequence,
        sliceDigest: sourceAttempt.auditSliceDigest,
      } : null,
      verifierSource: managed ? {
        startSequence: sourceAttempt.verifierStartSequence,
        endSequence: sourceAttempt.verifierEndSequence,
        sliceDigest: sourceAttempt.verifierSliceDigest,
      } : resource.value.trustedHost.verifierSource,
      runtimeIdentitySha256: identity.runtimeIdentitySha256,
      resourceLedgerSha256: resource.sha256,
      resourceRequestDigest: attestation.request.value.requestDigest,
      resourceAckDigest: attestation.ack.value.ackDigest,
      resourceObserverSha256: paths.observerSha256,
      roleTupleDigest: managed ? roleTupleDigest : null,
      operationCounts: managed ? sourceAttempt.operationCounts : null,
      terminalResidue: numericInventory(managed ? resource.value.managedInventory :
        resource.value.trustedHost.terminalInventory),
      publicActionDigest: observation.actionDigest,
      publicObservationDigest: observation.observationDigest,
      publicStartedAt: observation.publicStartedAt,
      publicCompletedAt: observation.publicCompletedAt,
      profileEvidence,
    })
    const outputPath = join(paths.receiptDirectory, receiptNames[index])
    await persistProfileReuseReceipt(receipt, outputPath,
      { repositoryCapture: paths.repositoryCapture })
    receiptIndex.push({ sequence, file: receiptNames[index], sha256: sha256Bytes(await readFile(outputPath)) })
    resourceIndex.push({ sequence, file: resourceNames[index], sha256: resource.sha256 })
  }

  const manifest = formProfileReuseManifest({
    schemaVersion: PROFILE_REUSE_MANIFEST_SCHEMA,
    status: 'complete',
    environmentId: tuple.environmentId,
    installId: tuple.installId,
    generationId: tuple.generationId,
    platform: tuple.platform,
    bindingDigest: source.bindingDigest,
    tupleIdentity: tuple.identity,
    sourceA3LedgerSha256: sourceInput.sha256,
    sourceA3AuditRecordCount: source.auditRecordCount,
    sourceA3AuditFinalDigest: source.auditFinalDigest,
    roleImages: source.roleImages,
    roleTupleDigest,
    receipts: receiptIndex,
    resourceLedgers: resourceIndex,
    observerAttestations: attestations.map(({ request, ack }, index) => ({
      sequence: index + 1,
      requestFile: requestNames[index],
      requestSha256: request.sha256,
      requestDigest: request.value.requestDigest,
      ackFile: ackNames[index],
      ackSha256: ack.sha256,
      ackDigest: ack.value.ackDigest,
      observerSha256: paths.observerSha256,
      resourceSha256: resources[index].sha256,
    })),
  })
  const manifestFile = join(paths.outputDirectory, 'manifest.json')
  await persistProfileReuseManifest(manifest, manifestFile,
    { repositoryCapture: paths.repositoryCapture })
  const record = await buildProfileReuseRecord({
    manifestFile,
    receiptDirectory: paths.receiptDirectory,
    resourceLedgerDirectory: paths.resourceLedgerDirectory,
    observerRequestDirectory: paths.observerRequestDirectory,
    observerAckDirectory: paths.observerAckDirectory,
    sourceA3LedgerFile: paths.sourceA3LedgerFile,
    tupleFile: paths.tupleFile,
    handoffReceiptDirectory: paths.handoffReceiptDirectory,
    recordFile: join(paths.outputDirectory, 'phase-c-record.json'),
    repositoryCapture: paths.repositoryCapture,
  })
  expect((await stat(join(paths.outputDirectory, 'phase-c-record.json'))).mode & 0o777).toBe(0o400)
  expect(JSON.parse(await readFile(join(paths.outputDirectory, 'phase-c-record.json'), 'utf8')))
    .toEqual(record)
}

function managedProfileEvidence(observation: JSONMap, sourceAttempt: JSONMap) {
  if (observation.profile === 'minimal') {
    expect(observation.minimalDenialDigest).toMatch(/^[0-9a-f]{64}$/)
    return {
      bashObserved: true,
      editObserved: true,
      prohibitedCapabilityDenialCount: 1,
      prohibitedCapabilityDenialDigest: observation.minimalDenialDigest,
      independentVerification: true,
    }
  }
  const applies = observation.sequence === 2
  return {
    fullRepositoryPolicy: true,
    securityPolicy: true,
    independentVerification: true,
    acceptedReview: applies,
    taskOwnedApply: applies,
    patchDigest: applies ? observation.patchDigest : null,
    applyTargetDigest: applies ? String(observation.applyTargetIdentity).replace(/^sha256:/, '') : null,
  }
}

function trustedProfileEvidence(observation: JSONMap, resource: JSONMap) {
  const host = resource.trustedHost
  const acknowledgement = observation.acknowledgement
  expect(host).toBeTruthy()
  expect(acknowledgement).toMatchObject({ acknowledged: true })
  expect(observation.agentExecution).toMatchObject({
    profile: 'trusted_local', runtimeSource: 'local_pi', executionProvider: 'trusted_host',
    capabilityPolicy: 'pi.native', sandboxed: false,
  })
  return {
    resolutionOrder: 'path_first_private_fallback',
    selectedSource: host.selectedSource,
    acknowledgementPolicyVersion: acknowledgement.policyVersion,
    acknowledgementPolicySha256: sha256(String(acknowledgement.policyVersion)),
    acknowledgedAt: acknowledgement.acknowledgedAt,
    acknowledgementCurrent: true,
    executionTarget: 'trusted-host',
    noSandbox: true,
    choraResultSemantics: true,
    selectedPiPath: host.selectedPiPath,
    selectedPiSourceRoot: host.selectedPiSourceRoot,
    piExecutableSha256: host.piExecutableSha256,
    piSourceProvenanceSha256: host.piSourceProvenanceSha256,
    runtimeFingerprint: host.runtimeFingerprint,
    processGroupIdentitySha256: host.processGroupIdentitySha256,
    sessionIdentitySha256: host.sessionIdentitySha256,
    processStartedAt: host.processStartedAt,
    processExitedAt: host.processExitedAt,
    processGroupTerminated: host.processGroupTerminated,
    sessionClosed: host.sessionClosed,
    terminalCleanupDigest: host.terminalCleanupDigest,
  }
}

function numericInventory(inventory: JSONMap) {
  const result: JSONMap = {}
  for (const field of [
    'ownedContainers', 'ownedNetworks', 'ownedVolumes', 'ownedConfigs', 'ownedWorkspaces',
    'ownedProcessGroups', 'activeReferences', 'recoverableReferences',
  ]) {
    expect(Array.isArray(inventory?.[field])).toBe(true)
    result[field] = inventory[field].length
  }
  return result
}

async function readJSONInput(path: string, label: string, max: number, mode?: number) {
  const bytes = await readExactFile(path, label, max, mode)
  let value: JSONMap
  try { value = JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) }
  return { bytes, value, sha256: sha256Bytes(bytes) }
}

async function assertTotalSessionBinding(phase: 'C') {
  const manifestFile = requiredAbsolutePath('CHORA_O4_TOTAL_MANIFEST_FILE')
  const manifestSha256 = requiredDigest('CHORA_O4_TOTAL_MANIFEST_SHA256')
  const authorityFile = requiredAbsolutePath('CHORA_O4_TOTAL_SESSION_AUTHORITY_FILE')
  const tupleIdentity = requiredDigest('CHORA_O4_TOTAL_TUPLE_IDENTITY')
  const runnerSha256 = requiredDigest('CHORA_O4_TOTAL_RUNNER_SHA256')
  const manifestInput = await readJSONInput(manifestFile, 'O4 total manifest', 16 * 1024 * 1024, 0o400)
  expect(manifestInput.sha256).toBe(manifestSha256)
  const manifest = manifestInput.value
  expect(manifest).toMatchObject({ schemaVersion: 'chora.m1-o4-total-input.v1',
    status: 'authorized', acceptanceClass: 'real_acceptance' })
  expect(manifest.selfDigest).toBe(sha256(canonicalJSONStringify({ ...manifest, selfDigest: null })))
  expect(manifest.runner?.moduleSha256).toBe(runnerSha256)
  expect(manifest.candidate?.tupleIdentity).toBeNull()
  const phaseEnvironment = manifest.phases?.[phase]?.environment
  expect(phaseEnvironment && typeof phaseEnvironment === 'object').toBe(true)
  for (const [name, value] of Object.entries(phaseEnvironment)) expect(process.env[name]).toBe(value)
  const authorityInput = await readJSONInput(authorityFile, 'O4 Runner session authority', 4 * 1024 * 1024, 0o400)
  const authority = authorityInput.value
  expect(authority).toMatchObject({ schemaVersion: 'chora.m1-o4-runner-session-authority.v1',
    status: 'exclusive', manifestSha256, runnerSha256, tupleIdentity, sequenceStart: 0 })
  expect(authority.socketPathDigest).toBe(sha256(String(manifest.runner.controlSocketPath)))
  const { authorityDigest, ...safe } = authority
  expect(authorityDigest).toBe(sha256(canonicalJSONStringify(safe)))
  const tuple = await readJSONInput(requiredAbsolutePath('CHORA_O4_PROFILE_REUSE_TUPLE_FILE'),
    'O4 total phase-C tuple', 4 * 1024 * 1024, 0o400)
  expect(tuple.value.identity).toBe(tupleIdentity)
}

async function readExactFile(path: string, label: string, max: number, mode?: number) {
  exactAbsolutePath(path, label)
  await assertNoSymlinkAncestors(path)
  const info = await lstat(path)
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.uid !== currentUID() ||
    info.size <= 0 || info.size > max) throw new Error(`${label} is not one bounded owner file`)
  if (mode !== undefined && (info.mode & 0o777) !== mode) throw new Error(`${label} mode drifted`)
  return readFile(path)
}

async function writeExclusiveReadonlyBytes(bytes: Buffer, path: string, label: string,
  repositoryCapture: { executableFile: string; executableSha256: string }) {
  exactAbsolutePath(path, label)
  await assertNoSymlinkAncestors(path)
  let handle
  let created = false
  let identity
  try {
    handle = await open(path, 'wx', 0o600)
    created = true
    await handle.writeFile(bytes)
    await handle.sync()
    await handle.chmod(0o400)
    await handle.sync()
    identity = ownedPathIdentity(await handle.stat({ bigint: true }))
    await handle.close()
    handle = undefined
    const persisted = await lstat(path, { bigint: true })
    if (!persisted.isFile() || persisted.isSymbolicLink() || persisted.nlink !== 1n ||
      persisted.dev.toString() !== identity.dev || persisted.ino.toString() !== identity.ino ||
      Number(persisted.mode) !== identity.mode || Number(persisted.uid) !== identity.uid ||
      Number(persisted.gid) !== identity.gid || Number(persisted.mode & 0o777n) !== 0o400) {
      throw new Error(`${label} final path identity drifted`)
    }
  } catch (error) {
    const cleanupErrors = []
    if (handle) {
      try { identity = ownedPathIdentity(await handle.stat({ bigint: true })) }
      catch (identityError) { cleanupErrors.push(identityError) }
      try { await handle.close() } catch (closeError) { cleanupErrors.push(closeError) }
      handle = undefined
    }
    if (created && identity !== undefined) {
      try {
        await cleanupOwnedPath({ capability: repositoryCapture, path, identity,
          disposition: 'regular_file' })
      } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    } else if (created) {
      try {
        await lstat(path)
        cleanupErrors.push(new Error(`${label} creation failed without an exact cleanup identity`))
      } catch (absenceError: any) {
        if (absenceError?.code !== 'ENOENT') cleanupErrors.push(absenceError)
      }
    }
    if (cleanupErrors.length > 0) throw new AggregateError(
      [error, ...cleanupErrors], `${label} creation and native cleanup failed`, { cause: error })
    throw error
  }
  return identity
}

async function assertOwnerDirectory(path: string, label: string) {
  exactAbsolutePath(path, label)
  await assertNoSymlinkAncestors(path)
  const info = await lstat(path)
  if (!info.isDirectory() || info.isSymbolicLink() || info.uid !== currentUID() ||
    (info.mode & 0o777) !== 0o700) throw new Error(`${label} is not owner-only 0700`)
}

function currentUID() {
  if (typeof process.getuid !== 'function') throw new Error('O4-C requires an owner-identity-capable POSIX host')
  return process.getuid()
}

async function assertNoSymlinkAncestors(path: string) {
  let current = resolve(path)
  while (true) {
    try {
      const info = await lstat(current)
      if (info.isSymbolicLink()) throw new Error('O4-C evidence path has a symlink ancestor')
    } catch (error: any) {
      if (error.code !== 'ENOENT') throw error
    }
    const parent = dirname(current)
    if (parent === current) return
    current = parent
  }
}

function requiredAbsolutePath(name: string) {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required`)
  exactAbsolutePath(value, name)
  return value
}

function requiredDigest(name: string) {
  const value = process.env[name]
  if (!value || !/^[0-9a-f]{64}$/.test(value)) throw new Error(`${name} must be one SHA-256 digest`)
  return value
}

function exactAbsolutePath(value: string, label: string) {
  if (!isAbsolute(value) || normalize(value) !== value || resolve(value) !== value) {
    throw new Error(`${label} must be an exact absolute path`)
  }
}

function insideOrSame(parent: string, child: string) {
  const prefix = parent.endsWith('/') ? parent : `${parent}/`
  return child === parent || child.startsWith(prefix)
}

function sha256Bytes(value: Buffer) { return createHash('sha256').update(value).digest('hex') }

async function uiMutation(page: Page, path: RegExp, action: () => Promise<void>, timeout = 30_000) {
  const [response] = await Promise.all([
    page.waitForResponse((candidate) => path.test(new URL(candidate.url()).pathname) && candidate.request().method() !== 'GET', { timeout }),
    action(),
  ])
  return response
}

async function safeJSON(response: Response, label: string) {
  const text = await response.text()
  expect(text, `${label} leaked a host path`).not.toMatch(/\/(?:Users|home|private|var\/folders)\//)
  return JSON.parse(text) as JSONMap
}

function escapeRE(value: string) { return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') }
function sha256(value: string) { return createHash('sha256').update(value).digest('hex') }
function canonicalJSONStringify(value: unknown): string {
  if (value === null || typeof value !== 'object') return JSON.stringify(value)
  if (Array.isArray(value)) return `[${value.map(canonicalJSONStringify).join(',')}]`
  const object = value as JSONMap
  return `{${Object.keys(object).sort().map((key) =>
    `${JSON.stringify(key)}:${canonicalJSONStringify(object[key])}`).join(',')}}`
}
