import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { spawn, type ChildProcess, type Signals } from 'node:child_process'
import { captureJourney, createRegisteredFakeRun, delay } from './verification'

const binary = requiredEnv('CHORA_JOINED_BIN')
const database = requiredEnv('CHORA_JOINED_DB')
const webRoot = requiredEnv('CHORA_JOINED_WEB')
const port = requiredEnv('CHORA_JOINED_PORT')
const baseURL = requiredEnv('CHORA_JOINED_BASE_URL')

let server: ChildProcess | undefined
let serverLog = ''

test.beforeAll(async () => startServer())
test.afterAll(async () => stopServer('SIGTERM'))

test('reopens the exact Patch Review after a same-database restart', async ({ page, request }) => {
  const { run, v8 } = await createRegisteredFakeRun(request, 'g2-m3-authoritative')
  expect(run.status).toBe('awaiting_review')
  await page.goto(runURL(v8, run.id))
  await expect(page.getByText('Awaiting review')).toBeVisible()
  await expect(page.getByText('Review · planned versus actual')).toBeVisible()
  const before = compactReview(run)

  const firstPID = server?.pid
  await stopServer('SIGTERM')
  await startServer()
  expect(server?.pid).toBeTruthy()
  expect(server?.pid).not.toBe(firstPID)

  await page.goto('/')
  await openSidebarTask(page, run.room.name, run.task.title)
  await expect(page).toHaveURL(runURL(v8, run.id))
  await expect(page.getByText('Awaiting review')).toBeVisible()
  const reopened = await getJSON<any>(request, `/api/rooms/${v8.task.room_id}/tasks/${v8.task.id}/runs/${run.id}`)
  expect(compactReview(reopened)).toEqual(before)

  await page.getByRole('button', { name: 'Accept', exact: true }).click()
  await expect(page.getByText('Accepted · Not applied', { exact: true }).first()).toBeVisible()
  const accepted = await getJSON<any>(request, `/api/runs/${run.id}`)
  expect(accepted).toMatchObject({ status: 'accepted', verifiedReview: { kind: 'accept' } })
  await captureJourney('restart-review-accepted', accepted)
})

test('Task Archive and Restore survive a same-database restart', async ({ page, request }) => {
  // Archive a completed, accepted Run. An unbound diagnostic fixture remains
  // awaiting verification and must not bypass the active-work archive guard.
  const { run, v8 } = await createRegisteredFakeRun(request, 'g2-m3-authoritative')
  const roomID = v8.task.room_id
  const taskID = v8.task.id
  const requirement = run.task.title
  await page.goto(runURL(v8, run.id))
  await page.getByRole('button', { name: 'Accept', exact: true }).click()
  await expect(page.getByText('Accepted · Not applied', { exact: true }).first()).toBeVisible()
  const accepted = await getJSON<any>(request, `/api/runs/${run.id}`)
  expect(accepted.status).toBe('accepted')
  const before = compactReview(accepted)
  // Acceptance retains an undelivered result. Close its remaining eligibility
  // explicitly before archiving; neither step may remove Review history.
  const closure = page.getByRole('region', { name: 'Close remaining results', exact: true })
  await closure.getByRole('button', { name: 'Preview close', exact: true }).click()
  await closure.getByRole('button', { name: 'Confirm close', exact: true }).click()
  await expect(closure.getByText('Remaining results closed', { exact: true })).toBeVisible()

  await page.goto(`/rooms/${roomID}`)
  await expect(page.locator('.task-title', { hasText: requirement })).toBeVisible()
  await page.getByRole('button', { name: 'Archive', exact: true }).click()
  await expect(page.getByText(/Archived · 1/)).toBeVisible()
  const archived = await getJSON<any>(request, `/api/rooms/${roomID}/workspace`)
  expect(archived.tasks.find((task: any) => task.id === taskID)?.archived).toBe(true)

  const firstPID = server?.pid
  await stopServer('SIGTERM')
  await startServer()
  expect(server?.pid).not.toBe(firstPID)

  await page.goto(`/rooms/${roomID}`)
  await expect(page.getByText(/Archived · 1/)).toBeVisible()
  await page.getByText(/Archived · 1/).click()
  await expect(page.locator('.task-archived', { hasText: requirement })).toBeVisible()
  await page.getByRole('button', { name: 'Restore', exact: true }).click()
  await expect(page.getByText(/Archived · 1/)).toHaveCount(0)
  const restored = await getJSON<any>(request, `/api/rooms/${roomID}/workspace`)
  expect(restored.tasks.find((task: any) => task.id === taskID)?.archived).toBe(false)
  const reopened = await getJSON<any>(request, `/api/runs/${run.id}`)
  expect(compactReview(reopened)).toEqual(before)
})

async function openSidebarTask(page: Page, roomName: string, taskTitle: string) {
  await page.locator('.sidebar-room-name', { hasText: roomName }).click()
  await page.locator('.sidebar-task-title', { hasText: taskTitle }).click()
}

function compactReview(run: any) {
  return {
    id: run.id,
    status: run.status,
    version: run.version,
    task: run.task,
    plan: run.plan,
    snapshot: run.snapshot,
    agentReport: run.agentReport,
    verification: run.verification,
    reviewablePatch: run.reviewablePatch,
  }
}

async function startServer() {
  if (server) throw new Error('Chora server is already running')
  serverLog = ''
  server = spawn(binary, ['--db', database, '--web', webRoot, '--port', port], {
    env: { ...process.env, CHORA_VERIFIER_POLICY_MODE: 'full' },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  server.stdout?.on('data', (chunk) => { serverLog += chunk.toString() })
  server.stderr?.on('data', (chunk) => { serverLog += chunk.toString() })
  for (let attempt = 0; attempt < 1200; attempt++) {
    if (server.exitCode !== null) throw new Error(`Chora exited before readiness: ${serverLog}`)
    try {
      const response = await fetch(`${baseURL}/api/status`)
      if (response.ok) return
    } catch {
      // The listener may not be ready yet.
    }
    await delay(100)
  }
  throw new Error(`Chora did not become ready: ${serverLog}`)
}

async function stopServer(signal: Signals) {
  const running = server
  server = undefined
  if (!running || running.exitCode !== null) return
  running.kill(signal)
  await Promise.race([
    new Promise<void>((resolveExit, reject) => {
      running.once('exit', () => resolveExit())
      running.once('error', reject)
    }),
    delay(10_000).then(() => { throw new Error(`Chora did not stop after ${signal}: ${serverLog}`) }),
  ])
}

function requiredEnv(name: string) {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required`)
  return value
}

async function getJSON<T>(request: APIRequestContext, path: string): Promise<T> {
  const response = await request.get(path)
  expect(response.status(), await response.text()).toBe(200)
  return response.json() as Promise<T>
}

function runURL(contract: { task: { id: string; room_id: string } }, runID: string) {
  return `/rooms/${encodeURIComponent(contract.task.room_id)}/tasks/${encodeURIComponent(contract.task.id)}/runs/${encodeURIComponent(runID)}`
}
