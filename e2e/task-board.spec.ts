import { expect, test, type APIRequestContext } from '@playwright/test'
import { createRegisteredFakeRun } from './verification'
import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

test.use({ actionTimeout: 15_000 })

async function createBoardProject(request: APIRequestContext) {
  const response = await request.post('/api/v2/projects', { data: { name: 'Board browser coverage' } })
  expect(response.status(), await response.text()).toBe(201)
  return response.json() as Promise<{ id: string; defaultRoomId: string }>
}

async function createBoardTask(request: APIRequestContext, roomID: string, title = 'Preserve task board history') {
  const roomResponse = await request.get(`/api/rooms/${roomID}`)
  expect(roomResponse.ok(), await roomResponse.text()).toBeTruthy()
  const room = await roomResponse.json()
  const response = await request.post(`/api/rooms/${roomID}/tasks`, { data: {
    title, goal: title, criteria: ['History is readable'],
    executionProfile: 'diagnostic_fake', revisionIds: room.revisions.map((revision: { id: string }) => revision.id),
  } })
  expect(response.status(), await response.text()).toBe(201)
  return response.json() as Promise<{ id: string }>
}

test('board and list read persisted tasks, retain URL filters and open New Task directly', async ({ page, request }) => {
  const project = await createBoardProject(request)
  const task = await createBoardTask(request, project.defaultRoomId)
  const mutations: string[] = []
  const boardReads: Array<{ bytes: number | null; durationMS: number; bodyUnavailable?: boolean }> = []
  page.on('response', (response) => {
    if (!response.url().includes('/task-board?') || response.status() !== 200) return
    const read = { bytes: null as number | null, durationMS: response.request().timing().responseStart, bodyUnavailable: true as boolean | undefined }
    boardReads.push(read)
    // Navigation may release the response body before this optional measurement.
    // Count the response even when its size cannot be observed.
    void response.body().then((body) => {
      read.bytes = body.byteLength
      read.durationMS = response.request().timing().responseEnd
      delete read.bodyUnavailable
    }).catch(() => { /* Keep the unavailable size explicit. */ })
  })
  page.on('request', (req) => { if (req.url().includes('/api/') && req.method() !== 'GET') mutations.push(req.url()) })
  await page.goto(`/projects/${project.id}/tasks`)
  const board = page.getByRole('region', { name: 'Task board', exact: true })
  await expect(board.getByRole('link', { name: 'Preserve task board history', exact: true })).toBeVisible()
  if (process.env.CHORA_S6A_EVIDENCE_DIR) {
    await mkdir(process.env.CHORA_S6A_EVIDENCE_DIR, { recursive: true })
    await page.screenshot({ path: join(process.env.CHORA_S6A_EVIDENCE_DIR, 'chora-s6a-board-en.png'), fullPage: true })
  }
  // Compare the same saved planning Task: one board action link and one
  // retained Room list button both reach the owning screen in one activation.
  await board.locator('.board-next').click()
  await expect(page).toHaveURL(new RegExp(`/tasks/${task.id}$`))
  await page.goto(`/rooms/${project.defaultRoomId}`)
  await page.locator('.task-card-main', { hasText: 'Preserve task board history' }).click()
  await expect(page).toHaveURL(new RegExp(`/tasks/${task.id}$`))
  await page.goto(`/projects/${project.id}/tasks`)
  await expect(board.locator('.board-card')).toHaveCount(1)
  await board.getByRole('button', { name: 'List', exact: true }).click()
  await board.getByRole('combobox', { name: 'Phase', exact: true }).selectOption('preparing')
  await board.getByLabel('Include archived', { exact: true }).check()
  await expect(page).toHaveURL(/mode=list.*phase=preparing.*archived=include/)
  await page.reload()
  await expect(board.getByRole('button', { name: 'List', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(board.getByRole('combobox', { name: 'Phase', exact: true })).toHaveValue('preparing')
  await expect(board.getByRole('link', { name: 'Preserve task board history', exact: true })).toBeVisible()
  const history = await request.get(`/api/rooms/${project.defaultRoomId}/tasks/${task.id}/runs`)
  expect((await history.json()).runs).toHaveLength(0)
  expect(mutations).toEqual([])
  if (process.env.CHORA_S6A_EVIDENCE_DIR) {
    await writeFile(join(process.env.CHORA_S6A_EVIDENCE_DIR, 'chora-s6a-board-observation.json'), JSON.stringify({
      fixture: 'one persisted diagnostic Task in planning, no attention items or repositories',
      boardActionActivations: 1, retainedRoomListActionActivations: 1,
      boardReads, browserMutations: mutations, missingExpectedTasks: 0,
      limitation: 'No improvement claim; this fixture does not measure attention triage or a large repository set.',
    }, null, 2))
  }
  await page.goto(`/rooms/${project.defaultRoomId}/tasks`)
  await expect(board.getByRole('link', { name: 'Preserve task board history', exact: true })).toBeVisible()
  const newTask = board.getByRole('button', { name: '＋ New Task', exact: true })
  await newTask.focus()
  await page.keyboard.press('Enter')
  await expect(page.getByLabel('What should Chora build?')).toBeVisible()
  expect(mutations).toEqual([])
})

test('board marks failed refresh stale and remains usable at narrow width in both languages', async ({ page, request }) => {
  const project = await createBoardProject(request)
  await createBoardTask(request, project.defaultRoomId)
  await page.goto(`/projects/${project.id}/tasks`)
  const board = page.locator('.task-board')
  await expect(board.locator('.board-card')).toHaveCount(1)
  await page.route(`**/api/v2/projects/${project.id}/task-board?*`, (route) => route.fulfill({ status: 503, json: { error: 'temporary test outage' } }))
  await board.getByRole('button', { name: 'Refresh', exact: true }).click()
  await expect(board.getByRole('alert')).toContainText('Refresh failed. Showing the last observed state.')
  await expect(board.locator('.board-card')).toHaveCount(1)
  await expect(board.locator('.board-next')).toHaveText('View details')
  await page.unroute(`**/api/v2/projects/${project.id}/task-board?*`)
  for (const locale of ['en', 'zh-CN']) {
    await page.evaluate((value) => localStorage.setItem('chora.locale', value), locale)
    await page.reload()
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(board.locator('.board-card')).toHaveCount(1)
    await expect(page.locator('html')).toHaveAttribute('lang', locale)
    await expect(board.getByRole('heading', { name: locale === 'en' ? 'Tasks' : '任务', exact: true })).toBeVisible()
    if (locale === 'zh-CN' && process.env.CHORA_S6A_EVIDENCE_DIR) {
      await page.screenshot({ path: join(process.env.CHORA_S6A_EVIDENCE_DIR, 'chora-s6a-board-zh-narrow.png'), fullPage: true })
    }
    const card = board.locator('.board-card')
    const box = await card.boundingBox()
    const bounds = await board.boundingBox()
    expect(box).not.toBeNull()
    expect(bounds).not.toBeNull()
    expect(box!.x + box!.width).toBeLessThanOrEqual(bounds!.x + bounds!.width + 1)
    const title = card.getByRole('link', { name: 'Preserve task board history', exact: true })
    await title.focus()
    await expect(title).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(page).toHaveURL(/\/rooms\/[^/]+\/tasks\/[^/]+$/)
    await page.goBack()
    await expect(board.locator('.board-card')).toHaveCount(1)
  }
})

// The real registered fixture completes service-owned verification without any
// browser present. A stale awaiting-verification observation then exercises two
// browser views; neither view may authorize another check on mount/navigation.
// Service-level concurrent/disconnected execution is covered independently by
// internal/app/verification_continuity_test.go.
test('two Run views and board navigation do not dispatch or duplicate verification', async ({ page, context, request }) => {
  const project = await createBoardProject(request)
  const { run, v8 } = await createRegisteredFakeRun(request, 'g2-m3-authoritative')
  expect(run.verification?.attempts).toHaveLength(1)
  const path = `/rooms/${v8.task.room_id}/tasks/${v8.task.id}/runs/${run.id}`
  const mutations: string[] = []
  context.on('request', (req) => { if (req.url().includes('/api/') && req.method() !== 'GET') mutations.push(req.url()) })
  await context.route(`**/api${path}`, async (route) => {
    const response = await route.fetch()
    const body = await response.json()
    body.status = 'awaiting_verification'
    body.controls.canStartVerification = true
    await route.fulfill({ response, json: body })
  })
  const second = await context.newPage()
  await Promise.all([page.goto(path), second.goto(path)])
  await expect(page.getByRole('region', { name: 'Plan', exact: true })).toBeVisible()
  await expect(second.getByRole('region', { name: 'Plan', exact: true })).toBeVisible()
  await page.goto(`/projects/${project.id}/tasks`)
  await expect(page.locator('.task-board')).toBeVisible()
  await second.close()
  const after = await request.get(`/api/runs/${run.id}`)
  const persisted = await after.json()
  expect(persisted.status).toBe('awaiting_review')
  expect(persisted.verification.attempts).toHaveLength(1)
  expect(persisted.verification.attempts[0].id).toBe(run.verification?.attempts[0].id)
  expect(mutations).toEqual([])
})

test('multi-Room attention triage finds the same required actions as retained Room lists', async ({ page, request }) => {
  const project = await createBoardProject(request)
  const topicResponse = await request.post(`/api/projects/${project.id}/rooms`, { data: { name: 'Board triage topic', description: 'Second Room for fixed triage fixture' } })
  expect(topicResponse.status(), await topicResponse.text()).toBe(201)
  const topic = await topicResponse.json() as { id: string }
  const generalReview = await createBoardTask(request, project.defaultRoomId, 'Review shared interface plan')
  const topicReview = await createBoardTask(request, topic.id, 'Review topic implementation plan')
  const draft = await createBoardTask(request, topic.id, 'Continue unsubmitted draft')
  const expected = [
    { ...generalReview, roomID: project.defaultRoomId, title: 'Review shared interface plan' },
    { ...topicReview, roomID: topic.id, title: 'Review topic implementation plan' },
  ]
  for (const task of expected) {
    const detailResponse = await request.get(`/api/tasks/${task.id}`)
    expect(detailResponse.ok()).toBeTruthy()
    const detail = await detailResponse.json()
    const submission = await request.post(`/api/tasks/${task.id}/plan/drafts/${detail.planning.draft.id}/submit`, {
      data: { expectedEditVersion: detail.planning.draft.editVersion, confirmUnchanged: false },
      headers: { 'Idempotency-Key': `board-triage-submit-${task.id}` },
    })
    expect(submission.status(), await submission.text()).toBe(200)
  }
  type Read = { path: string; bytes: number | null; durationMS: number; bodyUnavailable?: boolean }
  const measurements: Record<string, { clicks: number; filterChanges: number; elapsedMS: number; reads: Read[] }> = {
    board: { clicks: 0, filterChanges: 0, elapsedMS: 0, reads: [] },
    retainedRoomLists: { clicks: 0, filterChanges: 0, elapsedMS: 0, reads: [] },
  }
  let phase: keyof typeof measurements = 'board'
  const mutations: string[] = []
  page.on('request', (req) => { if (req.url().includes('/api/') && req.method() !== 'GET') mutations.push(req.url()) })
  page.on('response', (response) => {
    if (!response.url().includes('/api/') || response.request().method() !== 'GET') return
    const observation = measurements[phase]
    // Count synchronously: navigation may discard a body or leave its CDP
    // promise unresolved until page teardown. Telemetry must never hold work.
    const read: Read = { path: new URL(response.url()).pathname, bytes: null, durationMS: response.request().timing().responseStart, bodyUnavailable: true }
    observation.reads.push(read)
    void response.body().then((body) => {
      read.bytes = body.byteLength
      read.durationMS = response.request().timing().responseEnd
      delete read.bodyUnavailable
    }).catch(() => { /* Preserve an explicit unavailable measurement. */ })
  })
  let started = Date.now()
  await page.goto(`/projects/${project.id}/tasks`)
  const board = page.locator('.task-board')
  await expect(board.locator('.board-card')).toHaveCount(3)
  await board.getByRole('checkbox', { name: 'Needs attention', exact: true }).check()
  measurements.board.filterChanges++
  await expect(board.locator('.board-card')).toHaveCount(2)
  await expect(board.getByRole('link', { name: 'Continue unsubmitted draft', exact: true })).toHaveCount(0)
  for (const task of expected) {
    const card = board.locator('.board-card', { has: page.getByRole('link', { name: task.title, exact: true }) })
    await expect(card.locator('.board-next')).toHaveText('Review plan')
  }
  if (process.env.CHORA_S6A_EVIDENCE_DIR) {
    await mkdir(process.env.CHORA_S6A_EVIDENCE_DIR, { recursive: true })
    await page.screenshot({ path: join(process.env.CHORA_S6A_EVIDENCE_DIR, 'chora-s6a-board-multi-room-attention.png'), fullPage: true })
  }
  for (const [index, task] of expected.entries()) {
    if (index > 0) { await page.goBack(); measurements.board.clicks++ }
    await board.locator('.board-card', { has: page.getByRole('link', { name: task.title, exact: true }) }).locator('.board-next').click()
    measurements.board.clicks++
    await expect(page).toHaveURL(new RegExp(`/tasks/${task.id}$`))
    await expect(page.getByRole('region', { name: 'Plan', exact: true })).toContainText(task.title)
  }
  measurements.board.elapsedMS = Date.now() - started
  phase = 'retainedRoomLists'
  started = Date.now()
  await page.goto(`/rooms/${project.defaultRoomId}`)
  for (const [index, task] of expected.entries()) {
    if (index > 0) {
      await page.goBack(); measurements.retainedRoomLists.clicks++
      await page.locator('.sidebar-room-name', { hasText: 'Board triage topic' }).click()
      measurements.retainedRoomLists.clicks++
    }
    const card = page.locator('.task-card', { hasText: task.title })
    await expect(card).toContainText('Review plan')
    await card.locator('.task-card-main').click()
    measurements.retainedRoomLists.clicks++
    await expect(page).toHaveURL(new RegExp(`/tasks/${task.id}$`))
    await expect(page.getByRole('region', { name: 'Plan', exact: true })).toContainText(task.title)
  }
  measurements.retainedRoomLists.elapsedMS = Date.now() - started
  expect(mutations).toEqual([])
  for (const task of [...expected, { ...draft, roomID: topic.id }]) {
    const history = await request.get(`/api/rooms/${task.roomID}/tasks/${task.id}/runs`)
    expect(history.ok()).toBeTruthy()
    expect((await history.json()).runs).toHaveLength(0)
  }
  if (process.env.CHORA_S6A_EVIDENCE_DIR) {
    await writeFile(join(process.env.CHORA_S6A_EVIDENCE_DIR, 'chora-s6a-multi-room-observation.json'), JSON.stringify({
      fixture: { rooms: 2, tasks: 3, requiredPlanReviews: 2, unsubmittedDrafts: 1, runs: 0 },
      measurements, browserMutations: mutations, missingRequiredActions: 0, manualStatusEdits: 0,
      boundary: 'Each path starts at its own direct entry URL. Clicks include Back navigation; board filter change is separate. One scripted local fixture, no model calls or improvement claim. Existing Task detail shows the Plan with nonblocking Start Run; this comparison does not claim a separate plan-review form.',
    }, null, 2))
  }
})
