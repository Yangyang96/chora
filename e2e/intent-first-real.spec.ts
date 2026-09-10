import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { open, readFile, rename, unlink } from 'node:fs/promises'
import { isAbsolute } from 'node:path'

const baseURL = process.env.CHORA_REAL_BASE_URL
if (!baseURL) throw new Error('CHORA_REAL_BASE_URL is required')

const realPiImage = 'sha256:91698efead5641a633519f5f229373e08a59264045ca27f6d01fc06505deeea7'
const realPiPolicyFingerprint = 'efe8918d0c9c4232c8292941f573fe386d93a3faa051329c883c9b8349b66f04'
const restartPhase = process.env.CHORA_REAL_RESTART_PHASE ?? ''
if (!['', 'before', 'after'].includes(restartPhase)) {
  throw new Error('CHORA_REAL_RESTART_PHASE must be unset, before, or after')
}
const restartCheckpoint = process.env.CHORA_REAL_RESTART_CHECKPOINT
if (restartPhase && (!restartCheckpoint || !isAbsolute(restartCheckpoint))) {
  throw new Error('CHORA_REAL_RESTART_CHECKPOINT must be an absolute path when CHORA_REAL_RESTART_PHASE is set')
}
const resumeRunID = process.env.CHORA_REAL_RESUME_RUN_ID?.trim() ?? ''
if (resumeRunID && restartPhase !== 'before') {
  throw new Error('CHORA_REAL_RESUME_RUN_ID is supported only for the before-restart recovery phase')
}

type JSONMap = Record<string, any>
type PiIdentity = {
  enabled: true
  provider: 'docker'
  image: string
  policyFingerprint: string
  dockerContext: 'colima'
  engineIdentityDigest: string
  contextEndpointDigest: string
  dockerVersion: '29.6.1'
  dockerServerVersion: '29.6.1'
}
type ExecutionProof = {
  roomId: string
  taskId: string
  runId: string
  status: string
  version: number
  attempt: number
  attempts: Array<{ id: string; sequence: number; predecessorId: string | null; state: string }>
  currentAttemptId: string
  runtimeSessionId: string
  agentReportId: string
  agentReportAttemptId: string
  verificationAgentAttemptId: string
  patchAgentAttemptId: string
  patchDigest: string
}
type RestartCheckpoint = {
  schemaVersion: 'chora.intent-first-real-restart-checkpoint/v1'
  piIdentity: PiIdentity
  executionProof: ExecutionProof
}

test('Source-checkout Local Alpha UI completes the real intent-first journey', async ({ page, request }) => {
  test.setTimeout(45 * 60_000)
  if (restartPhase === 'after') {
    const checkpoint = await readRestartCheckpoint(restartCheckpoint!)
    const piIdentity = await getRealPiIdentity(request)
    expect(piIdentity).toEqual(checkpoint.piIdentity)
    const { roomId, taskId, runId } = checkpoint.executionProof
    const reopened = await getOwnedRun(request, roomId, taskId, runId)
    assertRealReviewReadyIdentity(reopened)
    expect(executionProof(reopened)).toEqual(checkpoint.executionProof)
    expect(reopened.patchApplication?.state).toBe('applied')
    await page.goto(`/rooms/${encodeURIComponent(roomId)}/tasks/${encodeURIComponent(taskId)}/runs/${encodeURIComponent(runId)}`)
    await expect(page.getByText('Written to repository', { exact: true }).first()).toBeVisible()
    return
  }

  await getRealPiIdentity(request)
  let roomName: string
  let roomID: string
  let taskID: string
  let runID: string
  let minimumReviewAttempt = 1
  let allowedRecoveryRetries = 0

  if (resumeRunID) {
    const preserved = await getRun(request, resumeRunID)
    expect(preserved.status).toBe('recovery_required')
    roomName = requiredString(preserved.room?.name, 'preserved Room name')
    roomID = requiredString(preserved.room?.id, 'preserved Room ID')
    taskID = requiredString(preserved.task?.id, 'preserved Task ID')
    runID = requiredString(preserved.id, 'preserved Run ID')
    minimumReviewAttempt = requiredNumber(preserved.attempt, 'preserved Attempt sequence') + 1
    allowedRecoveryRetries = 1
    await page.goto(`/rooms/${encodeURIComponent(roomID)}/tasks/${encodeURIComponent(taskID)}/runs/${encodeURIComponent(runID)}`)
  } else {
    const suffix = `${Date.now()}-${test.info().workerIndex}`
    roomName = `Intent-first Real ${suffix}`
    const requirement = 'Reject empty and whitespace-only acceptance criterion descriptions while preserving valid descriptions.'

    await page.goto('/')
    await page.getByText(/Execution ·/).click()
    const readinessResponse = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/readiness/refresh' && response.request().method() === 'POST')
    await page.getByRole('button', { name: 'Refresh readiness' }).click()
    const readiness = await (await readinessResponse).json() as { items: Array<{ state: string }> }
    expect(readiness.items.length).toBeGreaterThan(0)
    expect(readiness.items.every((item) => item.state === 'ready')).toBe(true)
    await page.getByText(/Execution ·/).click()

    await page.getByRole('button', { name: '＋ New Room' }).click()
    await page.getByLabel('Room name').fill(roomName)
    await page.getByLabel('Description').fill('Source-checkout Local Alpha acceptance for the simplified real M1 flow.')
    await page.getByRole('button', { name: 'Create Room', exact: true }).click()
    await expect(page).toHaveURL(/\/rooms\/[^/]+$/)
    roomID = decodeURIComponent(new URL(page.url()).pathname.split('/').at(-1)!)

    const taskResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === `/api/rooms/${roomID}/tasks`)
    await page.getByRole('button', { name: '＋ New Task' }).first().click()
    await page.getByLabel('What should Chora build?').fill(requirement)
    await page.getByRole('button', { name: 'Start', exact: true }).click()
    const createdResponse = await taskResponse
    expect(createdResponse.status()).toBe(201)
    const submittedBody = createdResponse.request().postDataJSON() as Record<string, unknown>
    expect(submittedBody.executionProfile).toBeUndefined()
    const task = await createdResponse.json() as { id: string; executionProfile: string; planning: { draft?: { content: { technical_steps: string[] } } } }
    expect(task.executionProfile).toBe('real_spec_coding')
    expect(task.planning.draft?.content.technical_steps).toHaveLength(3)

    await expect(page).toHaveURL(/\/runs\/[^/]+$/, { timeout: 180_000 })
    const parts = new URL(page.url()).pathname.split('/').filter(Boolean).map(decodeURIComponent)
    taskID = parts[3]
    runID = parts[5]
    expect(taskID).toBe(task.id)
  }
  await expect(page.locator('.trajectory-plan')).toContainText('Plan')
  await expect(page.locator('.trajectory-plan li')).toHaveCount(3)
  await expect(page.getByText('Automatically activated inside the existing Room boundary.')).toBeVisible()

  let review = await waitForReviewReady(page, request, runID, 35 * 60_000, minimumReviewAttempt, allowedRecoveryRetries)
  assertRealReviewReadyIdentity(review)
  expect(review.reviewablePatch?.files?.length).toBeGreaterThan(0)
  await expect(page.getByText('Review · planned versus actual')).toBeVisible({ timeout: 30_000 })
  await expect(page.getByRole('button', { name: 'Accept & Apply', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Ask Agent to fix', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Change requirement', exact: true })).toBeVisible()

  await page.getByLabel('Comment (optional)').fill('Strengthen the focused regression coverage, then return the updated bounded Patch.')
  const successorAttempt = review.attempt + 1
  await page.getByRole('button', { name: 'Ask Agent to fix', exact: true }).click()
  review = await waitForReviewReady(page, request, runID, 35 * 60_000, successorAttempt)
  expect(review.attempt).toBeGreaterThanOrEqual(successorAttempt)
  assertRealReviewReadyIdentity(review)
  await page.getByRole('button', { name: 'Accept & Apply', exact: true }).click()
  await expect(page.getByText('Written to repository', { exact: true }).first()).toBeVisible({ timeout: 30_000 })
  const applied = await getRun(request, runID)
  expect(applied.patchApplication?.state).toBe('applied')
  expect(applied.controls?.canApplyPatch).toBe(false)

  await page.locator('.sidebar-room-name', { hasText: roomName }).click()
  await page.getByRole('button', { name: 'Archive', exact: true }).click()
  await expect(page.getByText(/Archived · 1/)).toBeVisible()
  await page.reload()
  await expect(page.getByText(/Archived · 1/)).toBeVisible()
  await page.getByText(/Archived · 1/).click()
  await page.getByRole('button', { name: 'Restore', exact: true }).click()
  await expect(page.getByText(/Archived · 1/)).toHaveCount(0)

  await page.goto(`/rooms/${roomID}/tasks/${taskID}`)
  await expect(page).toHaveURL(`/rooms/${roomID}/tasks/${taskID}/runs/${runID}`)
  await expect(page.getByText('Written to repository', { exact: true }).first()).toBeVisible()

  if (restartPhase === 'before') {
    const reopened = await getOwnedRun(request, roomID, taskID, runID)
    expect(reopened.patchApplication?.state).toBe('applied')
    await writeRestartCheckpoint(restartCheckpoint!, {
      schemaVersion: 'chora.intent-first-real-restart-checkpoint/v1',
      piIdentity: await getRealPiIdentity(request),
      executionProof: executionProof(reopened),
    })
  }
})

async function getRealPiIdentity(request: APIRequestContext): Promise<PiIdentity> {
  const response = await request.get('/api/status')
  expect(response.status(), await response.text()).toBe(200)
  const status = await response.json() as JSONMap
  const pi = status.pi
  expect(pi?.enabled).toBe(true)
  expect(pi?.provider).toBe('docker')
  expect(pi?.image).toBe(realPiImage)
  expect(pi?.policyFingerprint).toBe(realPiPolicyFingerprint)
  expect(pi?.dockerContext).toBe('colima')
  expect(pi?.engineIdentityDigest).toMatch(/^[0-9a-f]{64}$/)
  expect(pi?.contextEndpointDigest).toMatch(/^[0-9a-f]{64}$/)
  expect(pi?.dockerVersion).toBe('29.6.1')
  expect(pi?.dockerServerVersion).toBe('29.6.1')
  return {
    enabled: true,
    provider: 'docker',
    image: pi.image,
    policyFingerprint: pi.policyFingerprint,
    dockerContext: 'colima',
    engineIdentityDigest: pi.engineIdentityDigest,
    contextEndpointDigest: pi.contextEndpointDigest,
    dockerVersion: '29.6.1',
    dockerServerVersion: '29.6.1',
  }
}

function assertRealReviewReadyIdentity(review: JSONMap) {
  expect(review.adapter).toBe('pi')
  const expectedAgentExecution = {
    profile: 'standard',
    runtimeSource: 'managed_pi_image',
    executionProvider: 'docker',
    capabilityPolicy: 'chora.standard.v1',
    sandboxed: true,
  }
  expect(review.agentExecution).toMatchObject(expectedAgentExecution)
  expect(review.attemptDetail?.agentExecution).toMatchObject(expectedAgentExecution)
  expect(review.attemptDetail?.runtime?.adapterId).toBe('pi')
  expect(review.attemptDetail?.runtime?.version).toBe('0.84.2')
  expect(review.attemptDetail?.runtime?.fingerprint).toMatch(/^[0-9a-f]{64}$/)
  expect(review.attemptDetail?.runtime?.state).toBe('stopped')
  expect(review.attemptDetail?.sandbox).toEqual({
    status: 'adopted',
    provider: 'docker',
    mode: 'attempt-private-codex-only',
    image: realPiImage,
    policyFingerprint: realPiPolicyFingerprint,
  })
  expect(review.verification?.result?.outcome).toBe('review_ready')
}

function executionProof(run: JSONMap): ExecutionProof {
  const attempts = (run.attemptHistory ?? []).map((attempt: JSONMap) => ({
    id: requiredString(attempt.id, 'Attempt ID'),
    sequence: requiredNumber(attempt.sequence, 'Attempt sequence'),
    predecessorId: attempt.predecessorId == null ? null : requiredString(attempt.predecessorId, 'Attempt predecessor ID'),
    state: requiredString(attempt.state, 'Attempt state'),
  })).sort((left: ExecutionProof['attempts'][number], right: ExecutionProof['attempts'][number]) =>
    left.sequence - right.sequence || left.id.localeCompare(right.id))
  const proof = {
    roomId: requiredString(run.room?.id, 'Room ID'),
    taskId: requiredString(run.task?.id, 'Task ID'),
    runId: requiredString(run.id, 'Run ID'),
    status: requiredString(run.status, 'Run status'),
    version: requiredNumber(run.version, 'Run version'),
    attempt: requiredNumber(run.attempt, 'Run attempt'),
    attempts,
    currentAttemptId: requiredString(run.attemptDetail?.id, 'Current Attempt ID'),
    runtimeSessionId: requiredString(run.attemptDetail?.runtime?.sessionId, 'Runtime Session ID'),
    agentReportId: requiredString(run.agentReport?.id, 'Agent Report ID'),
    agentReportAttemptId: requiredString(run.agentReport?.attemptId, 'Agent Report Attempt ID'),
    verificationAgentAttemptId: requiredString(run.verification?.agentAttemptId, 'Verification Agent Attempt ID'),
    patchAgentAttemptId: requiredString(run.reviewablePatch?.agentAttemptId, 'Patch Agent Attempt ID'),
    patchDigest: requiredString(run.reviewablePatch?.patchDigest, 'Patch digest'),
  }
  expect(proof.attempts.length).toBeGreaterThan(0)
  expect(proof.attempts.some((attempt) => attempt.id === proof.currentAttemptId)).toBe(true)
  expect(proof.agentReportAttemptId).toBe(proof.currentAttemptId)
  expect(proof.verificationAgentAttemptId).toBe(proof.currentAttemptId)
  expect(proof.patchAgentAttemptId).toBe(proof.currentAttemptId)
  expect(proof.patchDigest).toMatch(/^[0-9a-f]{64}$/)
  return proof
}

async function getOwnedRun(request: APIRequestContext, roomID: string, taskID: string, runID: string) {
  const path = `/api/rooms/${encodeURIComponent(roomID)}/tasks/${encodeURIComponent(taskID)}/runs/${encodeURIComponent(runID)}`
  const response = await request.get(path)
  expect(response.status(), await response.text()).toBe(200)
  const run = await response.json() as JSONMap
  expect(run.id).toBe(runID)
  expect(run.room?.id).toBe(roomID)
  expect(run.task?.id).toBe(taskID)
  return run
}

async function readRestartCheckpoint(path: string): Promise<RestartCheckpoint> {
  const checkpoint = JSON.parse(await readFile(path, 'utf8')) as RestartCheckpoint
  expect(checkpoint.schemaVersion).toBe('chora.intent-first-real-restart-checkpoint/v1')
  expect(checkpoint.piIdentity).toBeTruthy()
  expect(checkpoint.executionProof).toBeTruthy()
  return checkpoint
}

async function writeRestartCheckpoint(path: string, checkpoint: RestartCheckpoint) {
  const temporaryPath = `${path}.${process.pid}.${Date.now()}.tmp`
  const handle = await open(temporaryPath, 'wx', 0o600)
  try {
    await handle.writeFile(`${JSON.stringify(checkpoint, null, 2)}\n`, 'utf8')
    await handle.sync()
  } finally {
    await handle.close()
  }
  try {
    await rename(temporaryPath, path)
  } catch (error) {
    await unlink(temporaryPath).catch(() => undefined)
    throw error
  }
}

function requiredString(value: unknown, label: string) {
  expect(value, label).toEqual(expect.any(String))
  expect(value, label).not.toBe('')
  return value as string
}

function requiredNumber(value: unknown, label: string) {
  expect(value, label).toEqual(expect.any(Number))
  return value as number
}

async function getRun(request: APIRequestContext, runID: string) {
  const response = await request.get(`/api/runs/${runID}`)
  expect(response.status(), await response.text()).toBe(200)
  return response.json()
}

async function waitForReviewReady(page: Page, request: APIRequestContext, runID: string, timeout: number, minimumAttempt = 1, allowedRecoveryRetries = 0) {
  const deadline = Date.now() + timeout
  let latest: any
  let verifierRepairs = 0
  let recoveryRetries = 0
  while (Date.now() < deadline) {
    const response = await request.get(`/api/runs/${runID}`)
    expect(response.status(), await response.text()).toBe(200)
    latest = await response.json()
    if (latest.status === 'awaiting_review' && latest.attempt >= minimumAttempt) return latest
    if (latest.status === 'revision_required' && latest.attempt >= minimumAttempt && latest.controls?.canRetry) {
      verifierRepairs += 1
      if (verifierRepairs > 3) throw new Error('Run exceeded three bounded verifier-driven Agent repairs')
      await page.reload()
      await expect(page.getByLabel('Retry instructions')).toBeVisible({ timeout: 30_000 })
      await page.getByLabel('Retry instructions').fill('Repair the exact independently verified failing acceptance check, rerun the declared checks, and return the corrected bounded Patch.')
      const retryResponse = page.waitForResponse((candidate) => candidate.request().method() === 'POST' && new URL(candidate.url()).pathname === `/api/runs/${runID}/retry`)
      await page.getByRole('button', { name: 'Retry Pi', exact: true }).click()
      expect((await retryResponse).status()).toBe(200)
      continue
    }
    if (latest.status === 'recovery_required' && latest.controls?.canRetry && recoveryRetries < allowedRecoveryRetries) {
      recoveryRetries += 1
      await page.reload()
      await expect(page.getByLabel('Retry instructions')).toBeVisible({ timeout: 30_000 })
      await page.getByLabel('Retry instructions').fill('Retry the preserved failed Attempt through the repaired managed Runtime identity without changing its frozen Task authority.')
      const retryResponse = page.waitForResponse((candidate) => candidate.request().method() === 'POST' && new URL(candidate.url()).pathname === `/api/runs/${runID}/retry`)
      await page.getByRole('button', { name: 'Retry Pi', exact: true }).click()
      expect((await retryResponse).status()).toBe(200)
      continue
    }
    if (['accepted', 'cancelled', 'recovery_required', 'verification_recovery_required'].includes(latest.status)) {
      throw new Error(`Run reached unexpected state ${latest.status}`)
    }
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  throw new Error(`Run did not reach awaiting_review; latest=${JSON.stringify({ status: latest?.status, attempt: latest?.attempt })}`)
}

async function waitForRun(request: APIRequestContext, runID: string, status: string, timeout: number, minimumAttempt = 1) {
  const deadline = Date.now() + timeout
  let latest: any
  while (Date.now() < deadline) {
    const response = await request.get(`/api/runs/${runID}`)
    expect(response.status(), await response.text()).toBe(200)
    latest = await response.json()
    if (latest.status === status && latest.attempt >= minimumAttempt) return latest
    if (['accepted', 'cancelled', 'recovery_required', 'verification_recovery_required'].includes(latest.status)) {
      throw new Error(`Run reached unexpected state ${latest.status}`)
    }
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  throw new Error(`Run did not reach ${status}; latest=${JSON.stringify({ status: latest?.status, attempt: latest?.attempt })}`)
}
