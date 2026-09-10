import { expect, test, type APIRequestContext } from '@playwright/test'
import { execFile, spawn, type ChildProcess, type Signals } from 'node:child_process'
import { mkdtemp, readFile, realpath, writeFile, readdir, rename, mkdir } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { promisify } from 'node:util'
import { createHash } from 'node:crypto'

const execFileAsync = promisify(execFile)

const binary = requiredEnv('CHORA_WORKBENCH_BIN')
const sourceRoot = requiredEnv('CHORA_WORKBENCH_SOURCE')
const dataRoot = requiredEnv('CHORA_WORKBENCH_DATA')
const webRoot = requiredEnv('CHORA_WORKBENCH_WEB')
const port = requiredEnv('CHORA_WORKBENCH_PORT')
const baseURL = requiredEnv('CHORA_WORKBENCH_BASE_URL')

type PiDiscoveryView = {
  state: 'ready' | 'missing' | 'not_executable' | 'incompatible_version' | 'unconfigured' | 'unavailable'
  executablePath?: string
  version?: string
  executableSha256?: string
  readyProviders: string[]
  notReadyProviders: string[]
  reason?: string
}

let server: ChildProcess | undefined
let serverLog = ''
const runSuffix = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`

test.beforeAll(async () => {
  await startServer()
})

test.afterAll(async () => {
  await stopServer('SIGTERM')
})

// Acceptance mode must be selected explicitly on a clean source checkout with
// a prepared real coding repository. All product mutations use the public UI;
// GETs and filesystem reads only collect identity and verify the resulting work.
test('M2-S1-5 real public-UI Golden Path from ready Home through restart and Apply', async ({ page, request }, testInfo) => {
  test.skip(!process.env.CHORA_GOLDEN_PROJECT, 'Requires the explicit clean-source Golden Path acceptance inputs')
  test.setTimeout(600_000)
  const repo = await realpath(requiredEnv('CHORA_GOLDEN_PROJECT'))
  const evidencePath = requiredEnv('CHORA_GOLDEN_EVIDENCE')
  const git = async (cwd: string, args: string[]) => (await execFileAsync('git', args, { cwd })).stdout.trim()
  const sha256 = (bytes: Buffer) => createHash('sha256').update(bytes).digest('hex')
  expect(await git(sourceRoot, ['status', '--porcelain'])).toBe('')
  expect(await git(repo, ['status', '--porcelain'])).toBe('')
  const sourceRevision = await git(sourceRoot, ['rev-parse', 'HEAD'])
  const targetRevision = await git(repo, ['rev-parse', 'HEAD'])
  const originalProgram = await readFile(join(repo, 'src/headings.js'), 'utf8')
  const initialTree = await git(repo, ['write-tree'])
  const discovery = await getJSON<PiDiscoveryView>(request, '/api/pi/discovery')
  expect(discovery.state, discovery.reason).toBe('ready')
  expect(discovery.executableSha256).toMatch(/^[0-9a-f]{64}$/)
  expect(discovery.readyProviders.length).toBeGreaterThan(0)

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible()
  await expect(page.getByText(/No projects yet/)).toBeVisible()
  const homeReadyAt = new Date().toISOString()
  const timerStart = performance.now()
  await page.getByRole('button', { name: 'Create Project', exact: true }).click()
  // Only the OS chooser is stubbed; native macOS selection has separate UX-2 evidence.
  await page.route('**/api/projects/choose-directory', route => route.fulfill({ json: { cancelled: false, locator: repo } }))
  await page.getByRole('button', { name: 'Choose repository folder…', exact: true }).click()
  await page.getByRole('button', { name: 'Create with repository', exact: true }).click()
  await expect(page).toHaveURL(/\/projects\/project_[^/]+$/)
  await page.locator('.project-room-card', { hasText: 'General' }).locator('.project-room-main').click()
  await expect(page).toHaveURL(/\/rooms\/[^/]+$/)
  const roomId = new URL(page.url()).pathname.split('/').at(-1)!
  await page.getByRole('button', { name: '＋ New Task' }).first().click()
  const requirement = 'Implement collectHeadings(markdown) in src/headings.js for this Markdown outline library. Return ordered {level, text} objects for ATX headings (# through ###### followed by a space), trim the heading text, ignore ordinary lines and headings inside fenced code blocks (backticks or tildes). Preserve the existing exported API. Add meaningful node:test coverage including empty input, all six heading levels, non-headings, whitespace trimming, and both fence types. Run npm test. Change only src/headings.js and test/headings.test.js. Do not commit or change package.json or README.md.'
  await page.getByLabel('What should Chora build?').fill(requirement)
  await page.getByRole('radio', { name: 'Trusted Local · No Sandbox' }).click()
  const disclosure = page.getByRole('dialog', { name: 'Trusted Local · No Sandbox' })
  await expect(disclosure).toBeVisible()
  await disclosure.getByRole('button', { name: 'Acknowledge and use Trusted Local' }).click()
  await expect(page.getByRole('radio', { name: 'Trusted Local · No Sandbox' })).toBeChecked()
  await page.getByRole('button', { name: 'Start', exact: true }).click()
  await expect(page).toHaveURL(/\/rooms\/[^/]+\/tasks\/[^/]+\/runs\/[^/]+$/)
  const runURL = page.url()
  const runId = new URL(runURL).pathname.split('/').at(-1)!
  const runPath = `/api/runs/${runId}`
  let launched: any
  await expect.poll(async () => {
    launched = await getJSON<any>(request, runPath)
    if (['recovery_required', 'cancelled', 'revision_required'].includes(launched.status)) throw new Error(`Launch failed: ${launched.terminalReason || launched.status}`)
    return Boolean(launched.attemptDetail?.runtime?.sessionId && ['running', 'awaiting_review'].includes(launched.status))
  }, { timeout: 60_000 }).toBe(true)
  const firstTaskElapsedMs = Math.round(performance.now() - timerStart)
  expect(firstTaskElapsedMs).toBeLessThanOrEqual(300_000)
  expect(launched.adapter).toBe('pi')
  await expect(page.getByText('Trusted Local · No Sandbox', { exact: true }).first()).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('started.png'), fullPage: true })

  const waitReview = async (attempt: number) => {
    let state: any
    await expect.poll(async () => {
      state = await getJSON<any>(request, runPath)
      if (state.attempt >= attempt && ['recovery_required', 'cancelled', 'revision_required'].includes(state.status)) throw new Error(`Real Pi requires recovery: ${state.terminalReason || state.status}`)
      return `${state.attempt}:${state.status}`
    }, { timeout: 240_000 }).toBe(`${attempt}:awaiting_review`)
    await expect(page.getByText('Awaiting review').first()).toBeVisible()
    return state
  }
  const review = await waitReview(1)
  const task = await getJSON<any>(request, `/api/rooms/${roomId}/tasks/${review.task.id}`)
  expect(task.worktree.state).toBe('ready')
  const worktree = await realpath(join(dirname(repo), task.worktree.locator))
  expect(worktree).not.toBe(repo)
  expect(await readFile(join(repo, 'src/headings.js'), 'utf8')).toBe(originalProgram)
  expect(await git(repo, ['status', '--porcelain'])).toBe('')
  expect(await git(repo, ['write-tree'])).toBe(initialTree)
  const firstChecks = await execFileAsync('npm', ['test'], { cwd: worktree })
  await writeFile(testInfo.outputPath('first-checks.txt'), firstChecks.stdout)
  for (const heading of ['Intent', 'Plan', 'Progress', 'Changed files', 'Diff', 'Checks']) {
    await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible()
  }
  await expect(page.getByRole('columnheader', { name: 'Agent-reported', exact: true })).toBeVisible()
  await expect(page.getByText('Check source')).toBeVisible()
  expect(review.attemptDetail.runtime.externalSession).toMatch(/^[a-f0-9-]{36}$/)
  await page.screenshot({ path: testInfo.outputPath('review-ready.png'), fullPage: true })

  const firstPID = server?.pid
  await stopServer('SIGTERM')
  await startServer()
  expect(server?.pid).not.toBe(firstPID)
  await resumeFromHome(page, runURL)
  const resumedReview = await getJSON<any>(request, runPath)
  expect(resumedReview.id).toBe(review.id)
  expect(resumedReview.attemptDetail.id).toBe(review.attemptDetail.id)
  expect(resumedReview.status).toBe('awaiting_review')

  await page.getByLabel('Comment (optional)').fill('Also handle Windows CRLF input. Add a regression asserting that collecting "# Windows\\r\\n## Child\\r\\n" returns levels 1/2 and texts Windows/Child without carriage returns. Keep the same two allowed paths and run npm test again. Do not commit.')
  await page.getByRole('button', { name: 'Ask Agent to fix', exact: true }).click()
  const corrected = await waitReview(2)
  expect(corrected.attemptDetail.id).not.toBe(review.attemptDetail.id)
  expect(await git(repo, ['status', '--porcelain'])).toBe('')
  const correctedChecks = await execFileAsync('npm', ['test'], { cwd: worktree })
  await writeFile(testInfo.outputPath('corrected-checks.txt'), correctedChecks.stdout)
  const changed = (await git(worktree, ['diff', '--name-only', targetRevision])).split('\n').filter(Boolean)
  const untracked = (await git(worktree, ['ls-files', '--others', '--exclude-standard'])).split('\n').filter(Boolean)
  expect([...new Set([...changed, ...untracked])].sort()).toEqual(['src/headings.js', 'test/headings.test.js'])
  const postState = sha256(await readFile(join(worktree, 'src/headings.js')))
  await page.getByRole('button', { name: 'Write changes to repository', exact: true }).click()
  await expect(page.getByText('Written to repository').first()).toBeVisible({ timeout: 30_000 })
  const applied = await getJSON<any>(request, runPath)
  expect(applied.patchApplication.state).toBe('applied')
  expect(applied.controls.canApplyPatch).toBe(false)
  expect(sha256(await readFile(join(repo, 'src/headings.js')))).toBe(postState)
  expect(await git(repo, ['rev-parse', 'HEAD'])).toBe(targetRevision)
  const finalChecks = await execFileAsync('npm', ['test'], { cwd: repo })
  await writeFile(testInfo.outputPath('applied-checks.txt'), finalChecks.stdout)

  // This independent acceptance probe is evidence only, not product verifier authority.
  const probe = `import assert from 'node:assert/strict'; import {collectHeadings} from './src/headings.js';
    assert.deepEqual(collectHeadings('# One\\nbody\\n## Two\\n\\n~~~js\\n# hidden\\n~~~\\n### Three'), [{level:1,text:'One'},{level:2,text:'Two'},{level:3,text:'Three'}]);
    assert.deepEqual(collectHeadings('# Windows\\r\\n## Child\\r\\n'), [{level:1,text:'Windows'},{level:2,text:'Child'}]);
    assert.deepEqual(collectHeadings(''), []); console.log('Independent acceptance probe PASS');`
  const probeResult = await execFileAsync('node', ['--input-type=module', '-e', probe], { cwd: repo })
  await writeFile(testInfo.outputPath('acceptance-probe.txt'), probeResult.stdout)
  await stopServer('SIGTERM')
  await startServer()
  await resumeFromHome(page, runURL)
  await expect(page.getByText('Written to repository').first()).toBeVisible()
  const resumedApplied = await getJSON<any>(request, runPath)
  expect(resumedApplied.patchApplication).toEqual(applied.patchApplication)
  await page.screenshot({ path: testInfo.outputPath('applied-after-restart.png'), fullPage: true })
  expect(await git(sourceRoot, ['status', '--porcelain'])).toBe('')
  const guardLog = requiredEnv('CHORA_GOLDEN_DOCKER_GUARD_LOG')
  expect(await readFile(guardLog, 'utf8')).toBe('')
  const record = {
    schema: 'chora.m2-s1-5.acceptance.v1', result: 'PASS', sourceRevision,
    sourceRoot, binarySha256: sha256(await readFile(binary)), targetRevision, repo, worktree,
    homeReadyAt, firstTaskElapsedMs, timer: 'Ready Home to persisted real Pi launch; excludes source/dependency/build setup',
    discovery, runURL, roomId, taskId: review.task.id, runId,
    firstAttempt: review.attemptDetail, correctedAttempt: corrected.attemptDetail,
    patchApplication: applied.patchApplication, snapshot: applied.snapshot,
    originalUnchangedBeforeApply: true, onlyAllowedFilesChanged: true, originalHeadPreserved: true,
    readOnlyEvidenceGETs: true, allProductMutationsViaUI: true, noRouteMocks: true,
    dockerGuardCalls: 0, externalChecks: 'Acceptance evidence only; product checks remain Agent-reported',
    sourceCleanBeforeAndAfter: true, screenshotsAndChecks: testInfo.outputDir,
  }
  await writeFile(evidencePath, JSON.stringify(record, null, 2) + '\n')
})

test('GET /api/pi/discovery reports ready with providers when a compatible Pi is on PATH', async ({ request }) => {
  const response = await request.get('/api/pi/discovery')
  expect(response.status(), await response.text()).toBe(200)
  const discovery = await response.json() as PiDiscoveryView
  test.skip(discovery.state !== 'ready', `Pi discovery is ${discovery.state}${discovery.reason ? ` (${discovery.reason})` : ''}`)

  expect(discovery.state).toBe('ready')
  expect(discovery.readyProviders.length).toBeGreaterThan(0)
  expect(discovery.version).toMatch(/^\d+\.\d+\.\d+$/)
  expect(discovery.executablePath).toBeTruthy()
  expect(discovery.executableSha256).toMatch(/^[0-9a-f]{64}$/)
})

test('Local Connected is selectable and its disclosure acknowledges when discovery is ready', async ({ page, request }) => {
  const discovery = await getJSON<PiDiscoveryView>(request, '/api/pi/discovery')
  test.skip(discovery.state !== 'ready', `Pi discovery is ${discovery.state}; Local Connected selection is gated off`)

  const repo = await createGitRepo(`pi-${runSuffix}`)
  const roomId = (await addProject(request, repo)).repositoryBinding.roomId

  await page.goto(`/rooms/${encodeURIComponent(roomId)}`)
  await expect(page.getByText('This Room has no Tasks yet.')).toBeVisible()
  await page.getByRole('button', { name: '＋ New Task' }).first().click()

  const trusted = page.getByRole('radio', { name: 'Trusted Local · No Sandbox' })
  await expect(trusted).toBeEnabled()
  await trusted.click()

  const disclosure = page.getByRole('dialog', { name: 'Trusted Local · No Sandbox' })
  await expect(disclosure).toBeVisible()
  await disclosure.getByRole('button', { name: 'Acknowledge and use Trusted Local' }).click()

  await expect(trusted).toBeChecked()
  const acknowledgement = await getJSON<{ acknowledged: boolean; policyVersion: string }>(request, '/api/agent-execution/trusted-local-acknowledgements/current')
  expect(acknowledgement.acknowledged).toBe(true)
  expect(acknowledgement.policyVersion).toBe('chora.trusted-local-disclosure.v1')
})

test('real Pi works in the Task worktree, then human Accept & Apply updates only the original checkout', async ({ page, request }) => {
  test.setTimeout(360_000)
  const discovery = await getJSON<PiDiscoveryView>(request, '/api/pi/discovery')
  test.skip(discovery.state !== 'ready', `Pi discovery is ${discovery.state}; the real Local Connected journey requires a ready user-installed Pi`)

  const repo = await createReviewFixture(`apply-${runSuffix}`)
  const run = await startRealPiTask(page, request, repo, 'Implement the requested README marker exactly, do not change the Makefile, and run make test.')

  expect(await readFile(join(repo, 'README.md'), 'utf8')).toBe(run.originalReadme)
  expect(await readFile(join(run.worktree, 'README.md'), 'utf8')).toContain(reviewMarker)
  await execFileAsync('make', ['test'], { cwd: run.worktree })

  await expect(page.getByRole('heading', { name: 'Intent', exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Plan', exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Progress', exact: true })).toBeVisible()
  expect(run.repoId).toBe('legacy')
  await expect(page.getByRole('heading', { name: 'Changed files', exact: true })).toBeVisible()
  await expect(page.getByRole('table', { name: 'README.md hunk 1', exact: true })).toContainText(reviewMarker)
  await expect(page.getByRole('heading', { name: 'Checks', exact: true })).toBeVisible()
  const checks = page.getByRole('table', { name: 'Checks', exact: true })
  await expect(checks).toContainText('Check passed')
  await checks.getByText('View recorded evidence', { exact: true }).click()
  await expect(checks).toContainText('according to Pi')

  const reviewURL = page.url()
  await stopServer('SIGTERM')
  await startServer()
  await resumeFromHome(page, reviewURL)
  await expect(page.getByText('Awaiting review').first()).toBeVisible()
  const runPath = `/api/runs/${new URL(page.url()).pathname.split('/').at(-1)}`
  const correctionMarker = 'Human correction reviewed.'
  await page.getByLabel('Comment (optional)').fill(`Keep the requested marker and append the exact standalone line "${correctionMarker}" to README.md. Do not change Makefile. Run make test again in its own standalone shell invocation, without chaining other commands.`)
  await page.getByRole('button', { name: 'Ask Agent to fix', exact: true }).click()
  await expect.poll(async () => {
    const state = await getJSON<{ status: string; attempt: number }>(request, runPath)
    if (state.status === 'recovery_required') throw new Error('Pi correction requires recovery')
    return `${state.attempt}:${state.status}`
  }, { timeout: 120_000 }).toBe('2:awaiting_review')
  expect(await readFile(join(run.worktree, 'README.md'), 'utf8')).toContain(correctionMarker)
  expect(await readFile(join(repo, 'README.md'), 'utf8')).toBe(run.originalReadme)

  await page.getByRole('button', { name: 'Write changes to repository' }).click()
  await expect(page.getByText(/Applied to all selected repositories|Written to repository/).first()).toBeVisible({ timeout: 30_000 })
  expect(await readFile(join(repo, 'README.md'), 'utf8')).toContain(reviewMarker)

  const runURL = page.url()
  const firstPID = server?.pid
  await stopServer('SIGTERM')
  await startServer()
  expect(server?.pid).toBeTruthy()
  expect(server?.pid).not.toBe(firstPID)
  await resumeFromHome(page, runURL)
  await expect(page.getByText(/Applied to all selected repositories|Written to repository/).first()).toBeVisible({ timeout: 30_000 })
  await expect(page.getByText('README.md').first()).toBeVisible()
  const segments = new URL(runURL).pathname.split('/')
  const workspace = await getJSON<{ tasks: Array<{ id: string; latestRun: { status: string; patchApplicationState?: string } }> }>(request, `/api/rooms/${segments[2]}/workspace`)
  expect(workspace.tasks.find(task => task.id === segments[4])?.latestRun.patchApplicationState).toBe('applied')
  await page.goto(`/rooms/${segments[2]}`)
  await page.getByRole('button', { name: '中文', exact: true }).click()
  await expect(page.locator('.task-card .task-state').filter({ hasText: '已应用' }).first()).toBeVisible()
})

test('P2 successive tasks keep their own committed bases through Apply and restart', async ({ page, request }) => {
  test.setTimeout(600_000)
  const discovery = await getJSON<PiDiscoveryView>(request, '/api/pi/discovery')
  expect(discovery.state, 'P2 acceptance requires real installed Pi; a skip is not acceptance').toBe('ready')
  const repo = await createReviewFixture(`p2-successive-${runSuffix}`)
  const firstHead = (await execFileAsync('git', ['-C', repo, 'rev-parse', 'HEAD'])).stdout.trim()
  await startRealPiTask(page, request, repo, 'Change only README.md; keep Makefile unchanged and run make test.')
  const firstURL = page.url()
  const firstParts = new URL(firstURL).pathname.split('/')
  const roomId = firstParts[2]
  const firstTaskPath = `/api/tasks/${firstParts[4]}`
  const firstTask = await getTaskBase(request, firstTaskPath)
  expect(firstTask.baseRevision).toBe(firstHead)
  await page.getByRole('button', { name: 'Write changes to repository' }).click()
  await expect(page.getByText(/Applied to all selected repositories|Written to repository/).first()).toBeVisible({ timeout: 30_000 })
  await execFileAsync('git', ['-C', repo, 'add', 'README.md'])
  await execFileAsync('git', ['-C', repo, 'commit', '-m', 'explicit external commit after Task A'])
  const secondHead = (await execFileAsync('git', ['-C', repo, 'rev-parse', 'HEAD'])).stdout.trim()
  expect(secondHead).not.toBe(firstHead)
  // No second admission/removal of the Project; reuse the existing room.
  await startRealPiTask(page, request, repo, 'Also append the exact standalone line "P2 second task completed." to README.md. Keep Makefile unchanged and run make test.', true, roomId)
  const secondURL = page.url()
  const secondTask = await getTaskBase(request, `/api/tasks/${new URL(secondURL).pathname.split('/')[4]}`)
  expect(secondTask.baseRevision).toBe(secondHead)
  expect(secondTask.baseTree).not.toBe(firstTask.baseTree)
  expect(await getTaskBase(request, firstTaskPath)).toEqual(firstTask)
  await page.getByRole('button', { name: 'Write changes to repository' }).click()
  await expect(page.getByText(/Applied to all selected repositories|Written to repository/).first()).toBeVisible({ timeout: 30_000 })
  expect(await readFile(join(repo, 'README.md'), 'utf8')).toContain('P2 second task completed.')
  await stopServer('SIGTERM')
  await startServer()
  await page.goto(firstURL)
  await expect(page.getByText(/Applied to all selected repositories|Written to repository/).first()).toBeVisible()
  expect(await getTaskBase(request, firstTaskPath)).toEqual(firstTask)
  await resumeFromHome(page, secondURL)
  await expect(page.getByText(/Applied to all selected repositories|Written to repository/).first()).toBeVisible()
  expect((await getJSON<{ tasks: unknown[] }>(request, `/api/rooms/${roomId}/workspace`)).tasks).toHaveLength(2)
})

test('P2A real Pi tasks in distinct Rooms share a Project and preserve P2 bases', async ({ page, request }) => {
  test.setTimeout(600_000)
  const discovery = await getJSON<PiDiscoveryView>(request, '/api/pi/discovery')
  expect(discovery.state, 'P2 acceptance requires real installed Pi; a skip is not acceptance').toBe('ready')
  const repo = await createReviewFixture(`p2-successive-${runSuffix}`)
  const firstHead = (await execFileAsync('git', ['-C', repo, 'rev-parse', 'HEAD'])).stdout.trim()
  await startRealPiTask(page, request, repo, 'Change only README.md; keep Makefile unchanged and run make test.')
  const firstURL = page.url()
  const firstParts = new URL(firstURL).pathname.split('/')
  const roomId = firstParts[2]
  const firstTaskPath = `/api/tasks/${firstParts[4]}`
  const firstTask = await getTaskBase(request, firstTaskPath)
  expect(firstTask.baseRevision).toBe(firstHead)
  await page.getByRole('button', { name: 'Write changes to repository' }).click()
  await expect(page.getByText(/Applied to all selected repositories|Written to repository/).first()).toBeVisible({ timeout: 30_000 })
  await execFileAsync('git', ['-C', repo, 'add', 'README.md'])
  await execFileAsync('git', ['-C', repo, 'commit', '-m', 'explicit external commit after Task A'])
  const secondHead = (await execFileAsync('git', ['-C', repo, 'rev-parse', 'HEAD'])).stdout.trim()
  expect(secondHead).not.toBe(firstHead)
  // Create a topic Room through Project Home without admitting the repository again.
  const project = await getJSON<{id:string}>(request, `/api/projects/${roomId}`)
  await page.goto(`/projects/${project.id}`)
  await page.getByRole('button', { name: '＋ New topic Room', exact: true }).click()
  await page.getByLabel('Room name', { exact: true }).fill('Second topic')
  await page.getByLabel('Room Brief', { exact: true }).fill('Task B belongs only to this topic.')
  await page.getByRole('button', { name: 'Create Room', exact: true }).click()
  await expect(page).toHaveURL(/\/rooms\/room_[^/]+$/)
  const secondRoomId = new URL(page.url()).pathname.split('/')[2]
  expect(secondRoomId).not.toBe(roomId)
  await startRealPiTask(page, request, repo, 'Also append the exact standalone line "P2 second task completed." to README.md. Keep Makefile unchanged and run make test.', true, secondRoomId)
  const secondURL = page.url()
  const secondTask = await getTaskBase(request, `/api/tasks/${new URL(secondURL).pathname.split('/')[4]}`)
  expect(secondTask.baseRevision).toBe(secondHead)
  expect(secondTask.baseTree).not.toBe(firstTask.baseTree)
  expect(await getTaskBase(request, firstTaskPath)).toEqual(firstTask)
  await page.getByRole('button', { name: 'Write changes to repository' }).click()
  await expect(page.getByText(/Applied to all selected repositories|Written to repository/).first()).toBeVisible({ timeout: 30_000 })
  expect(await readFile(join(repo, 'README.md'), 'utf8')).toContain('P2 second task completed.')
  await stopServer('SIGTERM')
  await startServer()
  await page.goto(firstURL)
  await expect(page.getByText(/Applied to all selected repositories|Written to repository/).first()).toBeVisible()
  expect(await getTaskBase(request, firstTaskPath)).toEqual(firstTask)
  await resumeFromHome(page, secondURL)
  await expect(page.getByText(/Applied to all selected repositories|Written to repository/).first()).toBeVisible()
  expect((await getJSON<{ tasks: unknown[] }>(request, `/api/rooms/${roomId}/workspace`)).tasks).toHaveLength(1)
  expect((await getJSON<{ tasks: unknown[] }>(request, `/api/rooms/${secondRoomId}/workspace`)).tasks).toHaveLength(1)
  expect((await getJSON<{rooms:unknown[]}>(request, `/api/projects/${project.id}`)).rooms).toHaveLength(2)
})

test('P3/P4 real Pi adds a new file with custom checks and completes a no-change task after restart', async ({ page, request }, testInfo) => {
  test.skip(process.env.CHORA_P3_P4_ACCEPTANCE !== '1', 'Select the P3/P4 real Pi acceptance explicitly')
  test.setTimeout(600_000)
  const discovery = await getJSON<PiDiscoveryView>(request, '/api/pi/discovery')
  expect(discovery.state, discovery.reason).toBe('ready')
  const repo = await createGitRepo(`p3-p4-${runSuffix}`)
  await mkdir(join(repo, 'checks'))
  await writeFile(join(repo, 'checks', 'custom.sh'), 'echo "Setup unavailable: configure project dependencies before retrying." >&2\nexit 7\n')
  await execFileAsync('git', ['add', 'checks/custom.sh'], { cwd: repo })
  await execFileAsync('git', ['commit', '-m', 'explicit failing check fixture'], { cwd: repo })
  const created = await request.post('/api/projects', { data: { locator: repo } })
  expect(created.ok(), await created.text()).toBeTruthy()
  const project = await created.json()
  const settingsPath = `/api/projects/${project.id}/settings`
  const putSettings = async (data: unknown) => {
    const response = await request.put(settingsPath, { data, headers: { 'Idempotency-Key': `settings-${Date.now()}` } })
    expect(response.ok(), await response.text()).toBeTruthy()
    return response.json()
  }
  const settings = await putSettings({ version: 0, writableFiles: ['README.md'], writableDirectories: ['docs'], verificationCommands: [{ argv: ['grep', '-Fx', 'P3 new file accepted.', 'new note.txt'], workingDirectory: '.' }], noChecks: false })
  // Use a custom command at the repository root; filename is explicit in scope below.
  const scoped = await putSettings({ ...settings, writableFiles: ['README.md', 'new note.txt'], verificationCommands: [{ argv: ['grep', '-Fx', 'P3 new file accepted.', 'new note.txt'], workingDirectory: '.' }] })
  const first = await startRealPiTask(page, request, repo, `Create a new ordinary UTF-8 file at repository root named "new note.txt" containing exactly "P3 new file accepted." followed by a newline. Append "P4 custom check." to README.md. Do not create or change anything else. Run exactly this single declared command as its own bash tool call, without appending echo or any other command: grep -Fx 'P3 new file accepted.' 'new note.txt'. It prints the matching line; its output is already sufficient.`, true, project.room.id, true)
  const firstURL = page.url()
  const runPath = `/api/runs/${new URL(firstURL).pathname.split('/').at(-1)}`
  const reviewed = await getJSON<any>(request, runPath)
  expect(reviewed.reviewablePatch.files).toEqual(expect.arrayContaining([expect.objectContaining({ path: 'new note.txt', kind: 'added' }), expect.objectContaining({ path: 'README.md', kind: 'modified' })]))
  expect(reviewed.agentReport.claimedChecks[0].status).toBe('PASS')
  await expect(page.getByText('Added', { exact: true })).toBeVisible()
  await expect(readFile(join(repo, 'new note.txt'))).rejects.toThrow()
  expect(await readFile(join(first.worktree, 'new note.txt'), 'utf8')).toBe('P3 new file accepted.\n')
  const noCheckSettings = await putSettings({ ...scoped, verificationCommands: [], noChecks: true })
  await stopServer('SIGTERM'); await startServer(); await page.goto(firstURL)
  expect((await getJSON<any>(request, runPath)).agentReport.claimedChecks).toEqual(reviewed.agentReport.claimedChecks)
  await page.getByRole('button', { name: 'Write changes to repository' }).click()
  await expect(page.getByText('Written to repository').first()).toBeVisible({ timeout: 30_000 })
  expect(await readFile(join(repo, 'new note.txt'), 'utf8')).toBe('P3 new file accepted.\n')
  await execFileAsync('git', ['add', 'README.md', 'new note.txt'], { cwd: repo })
  await execFileAsync('git', ['commit', '-m', 'P3 reviewed change'], { cwd: repo })
  await startRealPiTask(page, request, repo, 'Read README.md and summarize its current contents. Make no file changes. Do not run any checks or commands; this task explicitly selects no checks. Finish with a short completed summary.', false, project.room.id, true)
  const secondURL = page.url()
  const secondRunPath = `/api/runs/${new URL(secondURL).pathname.split('/').at(-1)}`
  await expect.poll(async () => {
    const state = await getJSON<any>(request, secondRunPath)
    if (state.status === 'recovery_required') throw new Error(JSON.stringify(state))
    return state.status
  }, { timeout: 180_000 }).toBe('completed')
  const completed = await getJSON<any>(request, secondRunPath)
  expect(completed.result.outcome).toBe('completed_no_change')
  expect(completed.agentReport.claimedChecks[0].status).toBe('UNKNOWN')
  expect(completed.agentReport.claimedChecks[0].evidence).toContain('Not run / Unverified')
  expect(completed.controls.canAcceptAndApply).toBe(false)
  await stopServer('SIGTERM'); await startServer(); await page.goto(secondURL)
  await expect(page.getByText('Completed · No changes').first()).toBeVisible()
  await expect(page.getByRole('button', { name: 'Write changes to repository', exact: true })).toHaveCount(0)
  const restored = await getJSON<any>(request, secondRunPath)
  expect(restored.result).toEqual(completed.result)
  expect((await execFileAsync('git', ['status', '--porcelain'], { cwd: repo })).stdout).toBe('')
  await putSettings({ ...noCheckSettings, verificationCommands: [{ argv: ['sh', 'custom.sh'], workingDirectory: 'checks' }], noChecks: false })
  await startRealPiTask(page, request, repo, 'Do not modify any file. Run the declared check exactly as a separate bash call: `cd checks && sh custom.sh` (no extra arguments or commands). The script deliberately reports missing setup and exits 7; do not attempt installs or repairs. Report the failed setup/check and its recovery action, then finish.', false, project.room.id, true)
  const failedCheckPath = `/api/runs/${new URL(page.url()).pathname.split('/').at(-1)}`
  await expect.poll(async () => (await getJSON<any>(request, failedCheckPath)).status, { timeout: 180_000 }).toBe('recovery_required')
  const failedCheck = await getJSON<any>(request, failedCheckPath)
  expect(failedCheck.result.outcome).toBe('checks_failed')
  expect(failedCheck.agentReport.claimedChecks[0].status).toBe('FAIL')
  await expect(page.getByText('Check failed', { exact: true })).toBeVisible()
  const failedURL = page.url()
  await stopServer('SIGTERM'); await startServer(); await page.goto(failedURL)
  expect((await getJSON<any>(request, failedCheckPath)).result).toEqual(failedCheck.result)
  await expect(page.getByRole('alert').filter({ hasText: 'Last failure: Checks failed' })).toContainText('Automatic continuation could not start safely')
  await expect(page.getByRole('region', { name: 'Changed files', exact: true })).toContainText('No changed files.')
  await expect(page.getByText('Check failed', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Write changes to repository', exact: true })).toHaveCount(0)
  await testInfo.attach('P3-P4-real-Pi-results', { body: JSON.stringify({ projectId: project.id, first: reviewed, completed: restored, failedCheck }), contentType: 'application/json' })
})

test('human Apply fails closed when the original checkout HEAD drifts after real Pi review', async ({ page, request }) => {
  test.setTimeout(360_000)
  const discovery = await getJSON<PiDiscoveryView>(request, '/api/pi/discovery')
  test.skip(discovery.state !== 'ready', `Pi discovery is ${discovery.state}; the real Local Connected journey requires a ready user-installed Pi`)

  const repo = await createReviewFixture(`drift-${runSuffix}`)
  const run = await startRealPiTask(page, request, repo, 'Implement the requested README marker exactly, do not change the Makefile, and run make test.')
  expect(await readFile(join(repo, 'README.md'), 'utf8')).toBe(run.originalReadme)

  const drifted = `${run.originalReadme.trimEnd()}\n\nowner changed the original checkout\n`
  await writeFile(join(repo, 'README.md'), drifted)
  await execFileAsync('git', ['-C', repo, 'add', 'README.md'])
  await execFileAsync('git', ['-C', repo, 'commit', '-m', 'owner drift after review'])

  await page.getByRole('button', { name: 'Write changes to repository' }).click()
  await expect(page.getByText('Apply conflict').first()).toBeVisible({ timeout: 30_000 })
  await expect(page.getByText('Cannot write: repository has changed', { exact: true })).toBeVisible()
  await expect(page.getByText('The original checkout HEAD drifted from the admitted Task base.', { exact: true })).toBeVisible()
  expect(await readFile(join(repo, 'README.md'), 'utf8')).toBe(drifted)
  const conflictURL = page.url()
  await stopServer('SIGTERM')
  await startServer()
  await resumeFromHome(page, conflictURL)
  await expect(page.getByText('Apply conflict').first()).toBeVisible({ timeout: 30_000 })
  await expect(page.getByText('Cannot write: repository has changed', { exact: true })).toBeVisible()
  await expect(page.getByText('The original checkout HEAD drifted from the admitted Task base.', { exact: true })).toBeVisible()
  expect(await readFile(join(repo, 'README.md'), 'utf8')).toBe(drifted)
})

test('backend crash reaps live Pi and explicitly resumes the exact persisted session', async ({ page, request }) => {
  test.setTimeout(360_000)
  const discovery = await getJSON<PiDiscoveryView>(request, '/api/pi/discovery')
  test.skip(discovery.state !== 'ready', `Pi discovery is ${discovery.state}`)
  const witness = await realpath(await mkdtemp(join(tmpdir(), 'chora-continuity-witness-')))
  const repo = await createReviewFixture(`continuity-${runSuffix}`)
  await writeFile(join(repo, 'Makefile'), `test:
\t@printf 'started\\n' >> '${witness}/count'
\t@if [ ! -f '${witness}/released' ]; then sleep 120; fi
\t@grep -Fxq '${reviewMarker}' README.md
`)
  await execFileAsync('git', ['-C', repo, 'add', 'Makefile'])
  await execFileAsync('git', ['-C', repo, 'commit', '-m', 'bounded continuity witness'])
  const run = await startRealPiTask(page, request, repo, 'Change only README.md, then run make test exactly once. The test may take two minutes; wait for it.', false)
  const runURL = page.url()
  const runPath = `/api/runs/${new URL(runURL).pathname.split('/').at(-1)}`
  type State = { status: string; attempt: number; version: number; attemptDetail: { state: string; runtime: { externalSession: string } }; controls: { canRetry: boolean } }
  await expect.poll(async () => readFile(join(witness, 'count'), 'utf8').catch(() => ''), { timeout: 120_000 }).toBe('started\n')
  const before = await getJSON<State>(request, runPath)
  expect(before.status).toBe('running')
  expect(before.attemptDetail.runtime.externalSession).toMatch(/^[a-f0-9-]{36}$/)
  const records = await readdir(join(dataRoot, 'runtime', 'pi-trusted'))
  let pid = 0
  for (const entry of records.filter((name) => name.startsWith('attempt-'))) {
    const record = JSON.parse(await readFile(join(dataRoot, 'runtime', 'pi-trusted', entry, 'process.json'), 'utf8'))
    if (record.workspace === run.executionRoot && record.state === 'running') pid = record.pid
  }
  expect(pid).toBeGreaterThan(0)
  expect(processAlive(pid)).toBe(true)
  await stopServer('SIGKILL')
  await startServer({ PATH: '/usr/bin:/bin' })
  await resumeFromHome(page, runURL)
  await expect(page.getByRole('main').getByText('Pi not found', { exact: true })).toBeVisible()
  const unavailable = await getJSON<State>(request, runPath)
  expect(unavailable.status).toBe('recovery_required')
  expect(unavailable.controls.canRetry).toBe(false)
  expect(await readFile(join(witness, 'count'), 'utf8')).toBe('started\n')
  await stopServer('SIGTERM')
  await startServer()
  await expect.poll(() => processAlive(pid), { timeout: 20_000 }).toBe(false)
  const recovered = await getJSON<State>(request, runPath)
  expect(recovered.status).toBe('recovery_required')
  expect(recovered.attemptDetail.state).toBe('interrupted')
  expect(recovered.controls.canRetry).toBe(true)
  await resumeFromHome(page, runURL)
  expect(await readFile(join(witness, 'count'), 'utf8')).toBe('started\n')
  expect(await readFile(join(repo, 'README.md'), 'utf8')).toBe(run.originalReadme)
  await writeFile(join(witness, 'released'), 'ready\n')
  await page.getByLabel('Retry instructions').fill('The backend interrupted the prior check. Inspect the current README, preserve the requested marker, and now run make test exactly once. Do not repeat any prior write automatically. Return the reviewable result.')
  await page.getByRole('button', { name: 'Retry Pi', exact: true }).click()
  await expect.poll(async () => {
    const state = await getJSON<State>(request, runPath)
    if (state.attempt > 1 && state.status === 'recovery_required') throw new Error(JSON.stringify(state))
    return `${state.attempt}:${state.status}`
  }, { timeout: 120_000 }).toBe('2:awaiting_review')
  const resumed = await getJSON<State>(request, runPath)
  expect(resumed.attemptDetail.runtime.externalSession).toBe(before.attemptDetail.runtime.externalSession)
  expect(await readFile(join(witness, 'count'), 'utf8')).toBe('started\nstarted\n')
  expect(await readFile(join(repo, 'README.md'), 'utf8')).toBe(run.originalReadme)

  // Missing Task worktree remains readable and gives recovery instructions.
  await rename(run.executionRoot, `${run.executionRoot}-held`)
  try {
    await page.reload()
    await expect(page.getByLabel('External tools').getByRole('alert')).toHaveText(/Task worktree is unavailable; restore/)
    await expect(page.getByRole('button', { name: 'Open in Terminal' })).toBeDisabled()
  } finally { await rename(`${run.executionRoot}-held`, run.executionRoot) }
  await page.getByLabel('Open with…', { exact: true }).click()
  await page.getByRole('button', { name: 'Check again', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Open in Terminal' })).toBeEnabled()
})

function processAlive(pid: number) {
  try { process.kill(pid, 0); return true } catch { return false }
}

async function resumeFromHome(page: import('@playwright/test').Page, runURL: string) {
  const path = new URL(runURL).pathname
  const roomId = path.split('/')[2]
  await page.goto('/')
  const projects = await page.request.get('/api/projects').then((response) => response.json())
  const project = projects.projects.find((item: { rooms: { id: string }[] }) => item.rooms.some((room) => room.id === roomId))
  expect(project.currentAction.url).toBe(path)
  await page.locator('.project-card', { hasText: project.name }).getByRole('button', { name: /^Resume/ }).click()
  await expect(page).toHaveURL(runURL)
}

const reviewMarker = 'M2-S1-3 real Pi workbench accepted.'

type StartedRealPiTask = { originalReadme: string; worktree: string; executionRoot: string; repoId: string }

type TaskBaseEvidence = { repoId: string; baseRevision: string; baseTree: string; baseRef: string; startPolicy?: string }

async function getTaskBase(request: APIRequestContext, path: string): Promise<TaskBaseEvidence> {
  const task = await getJSON<{
    resourceSnapshot?: { resources: Array<{ repoId: string; baseCommit: string; baseTree: string; baseRef: string }> }
    repository?: { id?: string; sourceRevision: string }
    worktree?: { baseRevision: string; baseTree: string; baseRef: string; startPolicy: string }
  }>(request, path)
  if (task.resourceSnapshot) {
    expect(task.resourceSnapshot.resources).toHaveLength(1)
    const resource = task.resourceSnapshot.resources[0]
    return { repoId: resource.repoId, baseRevision: resource.baseCommit, baseTree: resource.baseTree, baseRef: resource.baseRef }
  }
  expect(task.worktree?.startPolicy).toBe('current_committed_head')
  expect(task.repository?.sourceRevision).toBe(task.worktree?.baseRevision)
  return { repoId: task.repository?.id ?? 'legacy', baseRevision: task.worktree!.baseRevision, baseTree: task.worktree!.baseTree, baseRef: task.worktree!.baseRef, startPolicy: task.worktree!.startPolicy }
}

async function startRealPiTask(page: import('@playwright/test').Page, request: APIRequestContext, repo: string, requirement: string, awaitReview = true, existingRoomId?: string, exactRequirement = false): Promise<StartedRealPiTask> {
  const originalReadme = await readFile(join(repo, 'README.md'), 'utf8')
  const roomId = existingRoomId ?? (await addProject(request, repo)).repositoryBinding.roomId
  const directory = await getJSON<{ projects: Array<{ id: string; rooms: Array<{ id: string }> }> }>(request, '/api/projects')
  const project = directory.projects.find((item) => item.rooms.some((room) => room.id === roomId))
  expect(project, 'Legacy compatibility fixture must retain its admitted Project').toBeTruthy()
  const settings = await getJSON<{ version: number }>(request, `/api/projects/${project!.id}/settings`)
  const room = await getJSON<{ revisions: Array<{ id: string; locator: string }> }>(request, `/api/rooms/${roomId}`)
  const brief = [...room.revisions].reverse().find((revision) => revision.locator === `room://${roomId}/brief`)
  expect(brief, 'Legacy fixture requires the exact Room brief revision').toBeTruthy()

  await page.goto(`/rooms/${encodeURIComponent(roomId)}`)
  await page.getByRole('button', { name: '＋ New Task' }).first().click()
  await page.getByLabel('What should Chora build?').fill(
    exactRequirement ? requirement : `Update README.md so it contains the exact standalone line "${reviewMarker}". ${requirement} Execute each declared check in its own standalone shell invocation, without chaining other commands. This legacy check recorder requires the exact declared command.`,
  )
  const trusted = page.getByRole('radio', { name: 'Trusted Local · No Sandbox' })
  await expect(trusted).toBeEnabled()
  await trusted.click()
  const disclosure = page.getByRole('dialog', { name: 'Trusted Local · No Sandbox' })
  if (await disclosure.count()) {
    await disclosure.getByRole('button', { name: 'Acknowledge and use Trusted Local' }).click()
  }
  await expect(trusted).toBeChecked()
  // Seed a retained scalar Task through its real compatibility API. Modern
  // Task creation is qualified separately; no response or snapshot is rewritten.
  const goal = await page.getByLabel('What should Chora build?').inputValue()
  const created = await request.post(`${baseURL}/api/rooms/${roomId}/tasks`, {
    headers: { 'Idempotency-Key': `legacy-fixture-${Date.now()}-${Math.random()}` },
    data: { title: goal.slice(0, 96), goal, criteria: ['Requirement satisfied'],
      revisionIds: [brief!.id], agentExecutionProfile: 'trusted_local',
      projectSettingsVersion: settings.version },
  })
  expect(created.status(), await created.text()).toBe(201)
  const taskFixture = await created.json()
  expect(taskFixture.resourceSnapshot).toBeUndefined()
  expect(taskFixture.worktree.state).toBe('ready')
  await page.goto(`/rooms/${roomId}/tasks/${taskFixture.id}`)
  const start = page.getByRole('button', { name: 'Start Run', exact: true })
  await expect(start).toBeEnabled()
  await start.click()

  await expect(page).toHaveURL(/\/rooms\/[^/]+\/tasks\/[^/]+\/runs\/[^/]+$/, { timeout: 60_000 })
  const parts = new URL(page.url()).pathname.split('/').filter(Boolean)
  const taskId = decodeURIComponent(parts[3])
  const runId = decodeURIComponent(parts[5])
  const runPath = `/api/rooms/${encodeURIComponent(roomId)}/tasks/${encodeURIComponent(taskId)}/runs/${encodeURIComponent(runId)}`
  const deadline = Date.now() + 300_000
  while (awaitReview) {
    const state = await getJSON<{ status: string; terminalReason?: string }>(request, runPath)
    if (state.status === 'awaiting_review') break
    if (state.status === 'recovery_required' || state.status === 'cancelled' || state.status === 'revision_required') {
      throw new Error(`Real Pi ended in ${state.status}${state.terminalReason ? ` (${state.terminalReason})` : ''}`)
    }
    if (Date.now() >= deadline) throw new Error(`Real Pi did not reach awaiting_review; last status was ${state.status}`)
    await delay(500)
  }
  if (awaitReview) await expect(page.getByText('Awaiting review').first()).toBeVisible({ timeout: 10_000 })

  const task = await getJSON<{
    resourceSnapshot?: { resources: Array<{ repoId: string; role: string; baseCommit: string; baseTree: string; baseRef: string }> }
    worktree?: { locator: string; state: string }
  }>(request, `/api/rooms/${encodeURIComponent(roomId)}/tasks/${encodeURIComponent(taskId)}`)
  if (task.resourceSnapshot) {
    expect(task.resourceSnapshot.resources).toHaveLength(1)
    const resource = task.resourceSnapshot.resources[0]
    expect(resource.role).toBe('write')
    const preparation = await getJSON<{ worktrees: Array<{ repoId: string; state: string; reason: string }> }>(request, `/api/v2/tasks/${encodeURIComponent(taskId)}/resources`)
    expect(preparation.worktrees).toEqual([expect.objectContaining({ repoId: resource.repoId, state: 'ready' })])
    const worktree = await realpath(join(dataRoot, 'task-workspaces', taskId, resource.repoId))
    expect(worktree).not.toBe(repo)
    return { originalReadme, worktree, executionRoot: dirname(worktree), repoId: resource.repoId }
  }
  // Historical v11 fixtures retain their scalar worktree evidence.
  expect(task.worktree?.state).toBe('ready')
  expect(task.worktree?.locator).toBeTruthy()
  const worktree = join(dirname(repo), task.worktree!.locator)
  return { originalReadme, worktree, executionRoot: worktree, repoId: 'legacy' }
}

async function addProject(request: APIRequestContext, locator: string) {
  const response = await request.post('/api/projects', { data: { locator } })
  expect(response.status(), await response.text()).toBe(200)
  return response.json() as Promise<{ repositoryBinding: { name: string; roomId: string } }>
}

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

async function createReviewFixture(label: string) {
  const root = await mkdtemp(join(tmpdir(), `chora-${label}-`))
  const canonical = await realpath(root)
  await execFileAsync('git', ['init', '-b', 'main', canonical])
  await execFileAsync('git', ['-C', canonical, 'config', 'user.email', 'e2e@chora.local'])
  await execFileAsync('git', ['-C', canonical, 'config', 'user.name', 'Chora E2E'])
  await writeFile(join(canonical, 'README.md'), `# ${label}\n\nThis repository has not been changed by Pi.\n`)
  await writeFile(join(canonical, 'Makefile'), `test:\n\t@grep -Fxq '${reviewMarker}' README.md\n`)
  await execFileAsync('git', ['-C', canonical, 'add', 'README.md', 'Makefile'])
  await execFileAsync('git', ['-C', canonical, 'commit', '-m', 'initial review fixture'])
  return canonical
}

async function startServer(environment: NodeJS.ProcessEnv = {}) {
  if (server) throw new Error('Chora workbench is already running')
  serverLog = ''
  server = spawn(binary, ['workbench', '--source', sourceRoot, '--data', dataRoot, '--web', webRoot, '--port', port], {
    stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, ...environment },
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
  if (!value) throw new Error(`${name} is required; run e2e/run-pi-local-connected.sh`)
  return value
}

function delay(milliseconds: number) {
  return new Promise<void>((resolveDelay) => setTimeout(resolveDelay, milliseconds))
}
