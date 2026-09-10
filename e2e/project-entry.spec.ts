import { expect, test, type APIRequestContext } from '@playwright/test'
import { execFile, spawn, type ChildProcess, type Signals } from 'node:child_process'
import { existsSync } from 'node:fs'
import { mkdtemp, mkdir, realpath, writeFile, rename } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)

const binary = requiredEnv('CHORA_WORKBENCH_BIN')
const sourceRoot = requiredEnv('CHORA_WORKBENCH_SOURCE')
const dataRoot = requiredEnv('CHORA_WORKBENCH_DATA')
const webRoot = requiredEnv('CHORA_WORKBENCH_WEB')
const port = requiredEnv('CHORA_WORKBENCH_PORT')
const baseURL = requiredEnv('CHORA_WORKBENCH_BASE_URL')

let server: ChildProcess | undefined
let serverLog = ''
let gitAvailable = false
const runSuffix = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`

test.beforeAll(async () => {
  gitAvailable = await hasGit()
  await startServer()
})

test.afterAll(async () => {
  await stopServer('SIGTERM')
})

test('native entry creates a Project with a default Room and reopens without duplication', async ({ page }) => {
  const repo = await createGitRepo(`open-${runSuffix}`)

  await page.goto('/')
  await expect(page.getByText(/No projects yet/)).toBeVisible()

  await page.getByRole('button', { name: 'Create Project', exact: true }).click()
  // Native dialog interaction is witnessed separately; only OS selection is stubbed here.
  await page.route('**/api/projects/choose-directory', route => route.fulfill({ json: { cancelled: false, locator: repo } }))
  await page.getByRole('button', { name: 'Choose repository folder…', exact: true }).click()

  await expect(page.getByLabel('Project name')).toHaveValue(basename(repo))
  expect((await getJSON(page.request, '/api/projects')).projects).toHaveLength(0)
  await page.getByLabel('Project name').fill('电商平台')
  await page.getByRole('button', { name: 'Create with repository', exact: true }).click()

  // Confirmation creates distinct Project/resource/default Room identities atomically.
  await expect(page).toHaveURL(/\/projects\/project_[^/]+$/)
  await expect(page.locator('.project-room-card', { hasText: 'General' })).toBeVisible()

  const roomURL = page.url()
  const created = await getJSON(page.request, '/api/projects')
  expect(created.projects[0].name).toBe('电商平台')
  expect(created.projects[0].room.name).toBe('General')
  expect(created.projects[0].id).not.toBe(created.projects[0].room.id)
  expect(created.projects[0].room.projectId).toBe(created.projects[0].id)
  expect(created.projects[0].repositoryBinding.name).toBe('电商平台')

  // Back on Home the project card appears and is clickable.
  await page.goto('/')
  const card = page.locator('.project-card', { hasText: '电商平台' })
  await expect(card).toBeVisible()
  await expect(card.locator('.project-local-path')).toContainText(repo)

  await card.locator('.project-card-main').click()
  await expect(page).toHaveURL(/\/projects\/project_[^/]+$/)
  await expect(page.locator('.project-room-card', { hasText: 'General' })).toBeVisible()

  // Selecting the same repository reopens its Room without renaming/duplicating it.
  await page.goto('/')
  await page.getByRole('button', { name: 'Create Project', exact: true }).click()
  await page.getByRole('button', { name: 'Choose repository folder…', exact: true }).click()
  await page.getByLabel('Project name').fill('Ignored replacement')
  await page.getByRole('button', { name: 'Create with repository', exact: true }).click()
  await expect(page).toHaveURL(roomURL)
  const reopened = await getJSON(page.request, '/api/projects')
  expect(reopened.projects).toHaveLength(1)
  expect(reopened.projects[0].name).toBe('电商平台')
  expect(reopened.projects[0].rooms).toHaveLength(1)
})

test('Project Home owns multiple topic Rooms with independent names and archive state', async ({ page, request }) => {
  const repo = await createGitRepo(`topics-${runSuffix}`)
  const created = await addProject(request, repo)
  const projectURL = `/projects/${created.id}`
  await page.goto(projectURL)
  await expect(page.getByRole('button', { name: 'New Room', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '＋ New topic Room', exact: true }).click()
  await page.getByLabel('Room name', { exact: true }).fill('Refund feature')
  await page.getByLabel('Room Brief', { exact: true }).fill('Refunds only; independent topic context.')
  await page.getByRole('button', { name: 'Create Room', exact: true }).click()
  await expect(page).toHaveURL(/\/rooms\/room_[^/]+$/)
  const topicURL = page.url()
  const topicID = new URL(topicURL).pathname.split('/')[2]
  const topic = await getJSON(request, `/api/rooms/${topicID}`)
  expect(topic.projectId).toBe(created.id)
  expect(topic.id).not.toBe(created.defaultRoomId)
  await page.goto(projectURL)
  await expect(page.getByLabel('Topic Rooms').locator('.project-room-card')).toHaveCount(2)
  await page.getByRole('button', { name: 'Rename project', exact: true }).click()
  await page.getByLabel('Project name', { exact: true }).fill('Independent Project name')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Independent Project name', exact: true })).toBeVisible()
  const card = page.locator('.project-room-card', { hasText: 'Refund feature' })
  await card.getByRole('button', { name: 'Rename Room', exact: true }).click()
  await page.getByLabel('Room name', { exact: true }).fill('Refund UX')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.locator('.project-room-card', { hasText: 'Refund UX' })).toBeVisible()
  await page.locator('.project-room-card', { hasText: 'General' }).getByRole('button', { name: 'Archive Room', exact: true }).click()
  await expect(page.locator('.project-room-card', { hasText: 'General' }).getByRole('button', { name: 'Restore Room', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Archive Project', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Restore Project', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Restore Project', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Archive Project', exact: true })).toBeVisible()
  await expect(page.locator('.project-room-card', { hasText: 'General' }).getByRole('button', { name: 'Restore Room', exact: true })).toBeVisible()
  await stopServer('SIGTERM')
  await startServer()
  await page.goto(projectURL)
  await expect(page.getByRole('heading', { name: 'Independent Project name', exact: true })).toBeVisible()
  await expect(page.locator('.project-room-card', { hasText: 'Refund UX' })).toBeVisible()
  await page.goto(topicURL)
  await expect(page.getByText('This Room has no Tasks yet.')).toBeVisible()
  expect((await getJSON(request, `/api/projects/${created.id}`)).rooms).toHaveLength(2)
})

test('native project entry fits a narrow workbench and cancellation adds nothing', async ({ page, request }, testInfo) => {
  const before = await getJSON(request, '/api/projects')
  await page.setViewportSize({ width: 440, height: 860 })
  await page.goto('/')
  await page.getByRole('button', { name: 'Create Project', exact: true }).click()
  const choose = page.getByRole('button', { name: 'Choose repository folder…', exact: true })
  await expect(choose).toBeVisible()
  const bounds = await choose.boundingBox()
  expect(bounds!.width).toBeGreaterThan(100)
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(440)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(440)
  await expect(page.getByLabel('Project name')).toBeVisible()
  await expect(page.getByLabel('Project description')).toBeVisible()
  await expect(page.getByText('Selected repository', { exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Clone repository' })).toHaveCount(0)
  await page.route('**/api/projects/choose-directory', route => route.fulfill({ json: { cancelled: true, locator: '' } }))
  await choose.click()
  await expect(choose).toBeEnabled()
  expect(await getJSON(request, '/api/projects')).toEqual(before)

  await page.route('**/api/projects/choose-directory', route => route.fulfill({ json: { cancelled: false, locator: `/tmp/${'long-repository-'.repeat(12)}/frontend` } }))
  await choose.click()
  await expect(page.getByLabel('Project name')).toHaveValue('frontend')
  await expect(page.getByRole('button', { name: 'Create with repository', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(440)
  await page.getByRole('button', { name: '中文', exact: true }).click()
  await page.getByLabel('项目名称').fill('电商平台')
  await expect(page.getByRole('button', { name: '连同仓库一起创建', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(440)
  await page.screenshot({ path: testInfo.outputPath('project-room-confirmation-zh.png'), fullPage: true })
  await page.getByRole('button', { name: '取消', exact: true }).click()
  expect(await getJSON(request, '/api/projects')).toEqual(before)
})

test('name filter hides and shows project cards', async ({ page, request }) => {
  const alpha = await createGitRepo(`filter-alpha-${runSuffix}`)
  const beta = await createGitRepo(`filter-beta-${runSuffix}`)
  await addProject(request, alpha)
  await addProject(request, beta)

  await page.goto('/')
  await expect(page.locator('.project-card', { hasText: basename(alpha) })).toBeVisible()
  await expect(page.locator('.project-card', { hasText: basename(beta) })).toBeVisible()

  await page.getByLabel('Filter projects').fill(basename(alpha))
  await expect(page.locator('.project-card', { hasText: basename(alpha) })).toBeVisible()
  await expect(page.locator('.project-card', { hasText: basename(beta) })).toHaveCount(0)

  await page.getByLabel('Filter projects').fill('')
  await expect(page.locator('.project-card', { hasText: basename(alpha) })).toBeVisible()
  await expect(page.locator('.project-card', { hasText: basename(beta) })).toBeVisible()
})

test('archiving a Project removes it from active Home and preserves repository files', async ({ page, request }) => {
  const repo = await createGitRepo(`remove-${runSuffix}`)
  await addProject(request, repo)

  await page.goto('/')
  const card = page.locator('.project-card', { hasText: basename(repo) })
  await expect(card).toBeVisible()
  await card.locator('.project-card-main').click()
  await page.getByRole('button', { name: 'Archive Project', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Restore Project', exact: true })).toBeVisible()
  await page.goto('/')
  await expect(card).toBeHidden()

  expect(existsSync(repo)).toBe(true)
})

test('a project survives a same-data-root server restart', async ({ page, request }) => {
  const repo = await createGitRepo(`restart-${runSuffix}`)
  const opened = await addProject(request, repo)
  const name = opened.repositoryBinding.name

  await page.goto('/')
  await expect(page.locator('.project-card', { hasText: name })).toBeVisible()

  const firstPID = server?.pid
  await stopServer('SIGTERM')
  await startServer()
  expect(server?.pid).toBeTruthy()
  expect(server?.pid).not.toBe(firstPID)

  await page.goto('/')
  await expect(page.locator('.project-card', { hasText: name })).toBeVisible()
})

test('missing Project stays readable and external opening waits for the exact directory', async ({ page, request }) => {
  const repo = await createGitRepo(`missing-${runSuffix}`)
  const opened = await addProject(request, repo)
  const continuityURL = `/api/projects/${opened.repositoryBinding.roomId}/continuity`
  const applications = (await getJSON(request, continuityURL)).applications ?? []
  await rename(repo, `${repo}-held`)
  try {
    await stopServer('SIGTERM')
    await startServer()
    await page.goto(`/rooms/${opened.repositoryBinding.roomId}`)
    await expect(page.getByLabel('External tools').getByRole('alert')).toHaveText(/Project repository is unavailable; restore it/)
    const unavailable = await getJSON(request, continuityURL)
    expect(unavailable.ready).toBe(false)
    await page.getByLabel('Open with…', { exact: true }).click()
    for (const application of applications) {
      await expect(page.getByRole('button', { name: application.name, exact: true })).toBeDisabled()
    }
    if (applications.length === 0) {
      await expect(page.getByText('No supported applications found', { exact: true })).toBeVisible()
    }
    await page.getByLabel('Open with…', { exact: true }).click()
  } finally { await rename(`${repo}-held`, repo) }
  await page.getByLabel('Open with…', { exact: true }).click()
  await page.getByRole('button', { name: 'Check again', exact: true }).click()
  await expect(page.getByLabel('External tools').getByRole('alert')).toHaveCount(0)
  const restored = await getJSON(request, continuityURL)
  expect(restored.ready).toBe(true)
  expect(restored.path).toBe(repo)
  await page.getByLabel('Open with…', { exact: true }).click()
  for (const application of applications) {
    await expect(page.getByRole('button', { name: application.name, exact: true })).toBeEnabled()
  }
  if (applications.length === 0) {
    await expect(page.getByText('No supported applications found', { exact: true })).toBeVisible()
  }
})

test('untrusted web requests cannot bypass native project selection', async ({ request }) => {
  const before = await getJSON(request, '/api/projects')
  const repo = await createGitRepo(`untrusted-${runSuffix}`)
  for (const headers of [
    { Origin: 'https://evil.example', 'Content-Type': 'text/plain' },
    { Origin: 'https://evil.example', 'Content-Type': 'application/json' },
    { 'Content-Type': 'text/plain' },
  ]) {
    const response = await request.post('/api/projects', { headers, data: JSON.stringify({ locator: repo }) })
    expect([403, 415]).toContain(response.status())
  }
  expect(await getJSON(request, '/api/projects')).toEqual(before)
})

test('retired clone endpoint cannot create a new project', async ({ request }) => {
  const before = await getJSON(request, '/api/projects')
  const result = await request.post('/api/projects/clone', { data: { url: 'https://example.invalid/repository.git' } })
  expect(result.status()).toBe(410)
  expect(await getJSON(request, '/api/projects')).toEqual(before)
})

async function addProject(request: APIRequestContext, locator: string) {
  const response = await request.post('/api/projects', { data: { locator } })
  expect(response.status(), await response.text()).toBe(200)
  return response.json() as Promise<{ id:string; defaultRoomId:string; repositoryBinding: { name: string; roomId: string } }>
}

test('repository named checks use searchable directories and persist edits after reload', async ({ page, request }) => {
  const repo = await createGitRepo(`checks-${runSuffix}`)
  await mkdir(join(repo, 'module space'))
  await writeFile(join(repo, 'module space', 'check.txt'), 'fixture\n')
  await execFileAsync('git', ['-C', repo, 'add', '.'])
  await execFileAsync('git', ['-C', repo, 'commit', '-m', 'add check directory'])
  const project = await addProject(request, repo)
  const resources = await getJSON<{ repositories: Array<{ repoId: string }> }>(request, `/api/v2/projects/${project.id}/repositories`)
  const checkURL = `/api/v2/projects/${project.id}/repositories/${resources.repositories[0].repoId}/checks`
  await page.goto(`/projects/${project.id}`)
  await page.getByRole('button', { name: 'Manage checks', exact: true }).click()
  await page.getByRole('button', { name: 'Add named check', exact: true }).click()
  await page.getByLabel('Check name', { exact: true }).fill('Quoted check')
  await page.getByLabel('Command', { exact: true }).fill('node -e "process.exit(0)"')
  await page.getByRole('button', { name: 'Choose check directory…', exact: true }).click()
  await page.getByRole('searchbox', { name: 'Search child directories' }).fill('module')
  await page.getByRole('button', { name: 'Search directories', exact: true }).click()
  await page.getByRole('button', { name: 'module space', exact: true }).click()
  await page.getByRole('button', { name: 'Use this directory', exact: true }).click()
  const firstSave = page.waitForResponse((response) => response.url().endsWith(checkURL) && response.request().method() === 'PUT')
  await page.getByRole('button', { name: 'Save check', exact: true }).click()
  const savedResponse = await firstSave
  expect(savedResponse.status(), await savedResponse.text()).toBe(200)
  await expect(page.getByRole('button', { name: 'Edit Quoted check', exact: true })).toBeVisible()
  const first = await getJSON<{ checks: Array<{ id: string; version: number; command: string; workingDirectory: string }> }>(request, checkURL)
  expect(first.checks).toHaveLength(1)
  expect(first.checks[0]).toMatchObject({ version: 1, workingDirectory: 'module space', command: 'node -e "process.exit(0)"' })
  const verified = await getJSON<{ repositories: Array<{ repoId: string; availability: string }> }>(request, `/api/v2/projects/${project.id}/repositories`)
  expect(verified.repositories[0]).toMatchObject({ repoId: resources.repositories[0].repoId, availability: 'ready' })
  await page.getByRole('button', { name: 'Edit Quoted check', exact: true }).click()
  await page.getByLabel('Command', { exact: true }).fill('node -e "process.exit(1)"')
  await page.getByRole('button', { name: 'Save check', exact: true }).click()
  await expect.poll(async () => (await getJSON<{ checks: Array<{ version: number }> }>(request, checkURL)).checks[0].version).toBe(2)
  await page.reload()
  await page.getByRole('button', { name: 'Manage checks', exact: true }).click()
  await page.getByRole('button', { name: 'Edit Quoted check', exact: true }).click()
  await expect(page.getByLabel('Command', { exact: true })).toHaveValue('node -e "process.exit(1)"')
  const persisted = await getJSON<{ checks: Array<{ id: string; version: number }> }>(request, checkURL)
  expect(persisted.checks[0]).toMatchObject({ id: first.checks[0].id, version: 2 })
  const original = await execFileAsync('git', ['-C', repo, 'status', '--porcelain'])
  expect(original.stdout).toBe('')
})

test('task-first Room prompts for a Task instead of exposing a legacy API error', async ({ page, request }) => {
  const created = await request.post('/api/v2/projects', { data: { name: 'Task-first continuity' } })
  expect(created.status(), await created.text()).toBe(201)
  const project = await created.json()
  await page.goto(`/rooms/${project.defaultRoomId}`)
  await expect(page.getByRole('status').filter({ hasText: 'Open a task to use its worktree' })).toBeVisible()
  await expect(page.getByText(/upgrade_required/)).toHaveCount(0)
  await expect(page.getByRole('button', { name: '＋ New Task', exact: true }).first()).toBeEnabled()
  await page.getByRole('button', { name: '＋ New Task', exact: true }).first().click()
  await expect(page.getByLabel('What should Chora build?')).toBeVisible()
  await expect(page.getByText(/upgrade_required/)).toHaveCount(0)
})

async function createGitRepo(label: string) {
  const root = await mkdtemp(join(tmpdir(), `chora-${label}-`))
  const canonical = await realpath(root)
  await execFileAsync('git', ['init', '-b', 'main', canonical])
  await execFileAsync('git', ['-C', canonical, 'config', 'user.email', 'e2e@chora.local'])
  await execFileAsync('git', ['-C', canonical, 'config', 'user.name', 'Chora E2E'])
  await writeFile(join(canonical, 'README.md'), `# ${label}\n`)
  await execFileAsync('git', ['-C', canonical, 'add', 'README.md'])
  await execFileAsync('git', ['-C', canonical, 'commit', '-m', 'initial'])
  return canonical
}

async function hasGit() {
  try {
    await execFileAsync('git', ['--version'])
    return true
  } catch {
    return false
  }
}

async function startServer() {
  if (server) throw new Error('Chora workbench is already running')
  serverLog = ''
  server = spawn(binary, ['workbench', '--source', sourceRoot, '--data', dataRoot, '--web', webRoot, '--port', port], {
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  server.stdout?.on('data', (chunk) => { serverLog += chunk.toString() })
  server.stderr?.on('data', (chunk) => { serverLog += chunk.toString() })
  for (let attempt = 0; attempt < 1200; attempt++) {
    if (server.exitCode !== null) throw new Error(`Chora workbench exited before readiness: ${serverLog}`)
    try {
      const response = await fetch(`${baseURL}/api/status`)
      if (response.ok) return
    } catch {
      // The listener may not be ready yet.
    }
    await delay(100)
  }
  throw new Error(`Chora workbench did not become ready: ${serverLog}`)
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
    delay(10_000).then(() => { throw new Error(`Chora workbench did not stop after ${signal}: ${serverLog}`) }),
  ])
}

async function getJSON<T>(request: APIRequestContext, path: string): Promise<T> {
  const response = await request.get(path)
  expect(response.status(), await response.text()).toBe(200)
  return response.json() as Promise<T>
}

function requiredEnv(name: string) {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required; run e2e/run-project-entry.sh`)
  return value
}

function basename(path: string) {
  return path.split('/').filter(Boolean).at(-1) ?? path
}

function delay(milliseconds: number) {
  return new Promise<void>((resolveDelay) => setTimeout(resolveDelay, milliseconds))
}
