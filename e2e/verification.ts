import { expect, type APIRequestContext } from '@playwright/test'
import { execFile } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)
export const authoritativePatchSHA256 = 'bedbeee71ba1bb7dc9f4d2aa86a90454f45620784d5f995ec89297948f2f39b6'

export type VerificationRunView = {
  id: string
  status: string
  attempt: number
  agentReport?: { id: string; attemptId: string; authority: string; claimedChecks: Array<{ status: string }> }
  artifacts: Array<{ locator: string; digest?: string }>
  controls?: { canStartVerification?: boolean; canCancelVerification?: boolean; canRetryVerification?: boolean; canReview?: boolean; canRetry?: boolean }
  verification?: {
    id: string
    agentAttemptId: string
    state: string
    bindings: { baselineDigest: string; patchDigest: string; acceptanceContractDigest: string; verifierPolicyVersion: string; verifierPolicyDigest: string }
    attempts: Array<{
      id: string
      sequence: number
      predecessorId?: string
      state: string
      evidenceComplete: boolean
      cleanupProven: boolean
      commands: Array<{ commandId: string; argv: string[]; classification: string; exitCode?: number; stdout: { fullSha256: string; totalBytes: number; truncated: boolean }; stderr: { fullSha256: string; totalBytes: number; truncated: boolean } }>
      checks: Array<{ criterionId: string; status: string; trust: string; evidenceIds: string[] }>
    }>
    result?: { id: string; attemptId: string; outcome: string }
  }
  verificationHistory?: Array<{ id: string; agentAttemptId: string; result?: { id: string; outcome: string } }>
  reviewablePatch?: {
    patchDigest: string
    resultId: string
    agentAttemptId: string
    verificationAttemptId: string
    rawDownload: string
    files: Array<{ path: string; lines: Array<{ kind: string; oldLine?: number; newLine?: number; text: string }> }>
  }
  verifiedReview?: { id: string; resultId: string; kind: string; reason: string; rejectionClass?: string; patchDigest: string; planningDraftId?: string; relatedTaskId?: string }
  verifiedReviewHistory?: Array<{ id: string; resultId: string; kind: string; reason: string; rejectionClass?: string; patchDigest: string; planningDraftId?: string; relatedTaskId?: string }>
  scm?: { configured: boolean; canCreateDraftMR: boolean; reason: string }
}

export async function createRegisteredFakeRun(request: APIRequestContext, patchFixture?: 'g2-m3-authoritative') {
  const v4 = JSON.parse(await readFile(resolve(process.cwd(), 'contracts', 'g2-m1a', 'chora-m1-real-task.v4.json'), 'utf8'))
  const workspaceRoot = resolve('/tmp', `chora-g2-m4-production-fake-${randomBytes(12).toString('hex')}`)
  v4.task.id = entityID('task')
  v4.task.room_id = entityID('room')
  v4.acceptance.criteria[0].id = entityID('criterion')
  v4.acceptance.criteria[1].id = entityID('criterion')
  v4.execution.input.context_snapshot_id = entityID('context_snapshot')

  const materialization = await request.post('/api/spec-coding/materializations', {
    data: {
      contract: v4,
      workspaceRoot,
      agentAdapter: 'pi',
      contextEntryId: entityID('context_entry'),
      contextRevisionId: entityID('context_revision'),
      charterId: entityID('charter'),
      frozenAt: new Date().toISOString(),
    },
  })
  expect(materialization.status(), await materialization.text()).toBe(201)
  const binding = await materialization.json() as { taskId: string; snapshot: { id: string; digest: string } }

  const v8 = structuredClone(v4)
  v8.schema_version = 'chora.spec-coding-core.v8'
  v8.revision = 8
  v8.execution.input.context_snapshot_digest = binding.snapshot.digest
  v8.acceptance.verification_commands = [{ id: 'domain-tests', argv: ['go', 'test', './internal/domain'] }]
  for (const criterion of v8.acceptance.criteria) criterion.verification_command_ids = ['domain-tests']
  const registration = await request.post('/api/spec-coding/contracts', { data: { contract: v8 } })
  expect(registration.status(), await registration.text()).toBe(200)

  const taskResponse = await request.get(`/api/tasks/${binding.taskId}`)
  expect(taskResponse.status(), await taskResponse.text()).toBe(200)
  const task = await taskResponse.json() as { planning: { draft: { id: string; editVersion: number } } }
  const submission = await request.post(`/api/tasks/${binding.taskId}/plan/drafts/${task.planning.draft.id}/submit`, {
    data: { expectedEditVersion: task.planning.draft.editVersion, confirmUnchanged: false },
    headers: { 'Idempotency-Key': `submit-${binding.taskId}` },
  })
  expect(submission.status(), await submission.text()).toBe(200)
  const submitted = await submission.json() as { revision: { id: string } }
  const revisionId = submitted.revision.id
  const planning = await request.post(`/api/tasks/${binding.taskId}/plan/revisions/${revisionId}/reviews`, {
    data: { kind: 'accept', note: 'The frozen G2-M4 verification contract is bounded and ready.' },
    headers: { 'Idempotency-Key': `accept-${revisionId}` },
  })
  expect(planning.status(), await planning.text()).toBe(200)

  const data: Record<string, string> = { revisionId }
  if (patchFixture) data.patchFixture = patchFixture
  const started = await request.post(`/api/tasks/${binding.taskId}/runs`, { data })
  expect(started.status(), await started.text()).toBe(201)
  const run = await started.json() as VerificationRunView
  // Verification auto-starts once the Agent completes, so the run settles at
  // awaiting_review rather than lingering at awaiting_verification.
  return { run: await waitForRunStatus(request, run.id, 'awaiting_review'), v8 }
}

export async function waitForRunStatus(request: APIRequestContext, runID: string, status: string, timeout = 120_000, minimumAttempt = 0): Promise<VerificationRunView> {
  const deadline = Date.now() + timeout
  let latest: VerificationRunView | undefined
  while (Date.now() < deadline) {
    const response = await request.get(`/api/runs/${runID}`)
    if (response.ok()) {
      latest = await response.json() as VerificationRunView
      if (latest.status === status && latest.attempt >= minimumAttempt) return latest
    }
    await delay(100)
  }
  throw new Error(`Run ${runID} did not reach ${status}; latest=${JSON.stringify(latest)}`)
}

export async function waitForVerifierContainer(attemptID: string, present: boolean, timeout = 30_000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    const { stdout } = await execFileAsync('docker', ['container', 'ls', '--all', '--quiet', '--no-trunc', '--filter', `label=chora.verification_attempt_id=${attemptID}`])
    if ((stdout.trim() !== '') === present) return
    await delay(50)
  }
  throw new Error(`Verifier container presence for ${attemptID} did not become ${present}`)
}

export async function waitForRunningVerifierContainer(attemptID: string, timeout = 30_000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    const { stdout } = await execFileAsync('docker', ['container', 'ls', '--quiet', '--no-trunc', '--filter', `label=chora.verification_attempt_id=${attemptID}`])
    if (stdout.trim() !== '') return
    await delay(50)
  }
  throw new Error(`Verifier container ${attemptID} did not reach running state`)
}

export async function captureJourney(name: string, run: VerificationRunView) {
  const root = process.env.CHORA_G2M4_EVIDENCE_DIR
  if (!root) return
  await mkdir(root, { recursive: true })
  await writeFile(resolve(root, `${name}.json`), `${JSON.stringify(run, null, 2)}\n`, { mode: 0o600 })
}

function entityID(prefix: string) {
  const bytes = randomBytes(16)
  let timestamp = BigInt(Date.now())
  for (let index = 5; index >= 0; index--) {
    bytes[index] = Number(timestamp & 0xffn)
    timestamp >>= 8n
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x70
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = bytes.toString('hex')
  return `${prefix}_${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

export function delay(milliseconds: number) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds))
}
