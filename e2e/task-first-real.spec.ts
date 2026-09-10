import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { createHash } from 'node:crypto'
import { execFile, spawn, type ChildProcess } from 'node:child_process'
import { readFile, writeFile } from 'node:fs/promises'
import { basename, join } from 'node:path'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)

const binary = requiredEnv('CHORA_WORKBENCH_BIN')
const sourceRoot = requiredEnv('CHORA_WORKBENCH_SOURCE')
const dataRoot = requiredEnv('CHORA_WORKBENCH_DATA')
const webRoot = requiredEnv('CHORA_WORKBENCH_WEB')
const port = requiredEnv('CHORA_WORKBENCH_PORT')
const baseURL = requiredEnv('CHORA_WORKBENCH_BASE_URL')
const repoA = requiredEnv('CHORA_TASK_FIRST_REPO_A')
const repoB = requiredEnv('CHORA_TASK_FIRST_REPO_B')
const evidencePath = requiredEnv('CHORA_TASK_FIRST_EVIDENCE')

type PiDiscoveryView = {
  state: string
  executablePath?: string
  version?: string
  executableSha256?: string
  readyProviders?: string[]
  reason?: string
}

type RepositoryView = {
  repoId: string
  name: string
  localLocator: string
  state: string
  version: number
  availability: string
  branch: string
  head: string
  dirty: boolean
}

type ProjectView = {
  id: string
  name: string
  defaultRoomId: string
  repositories: RepositoryView[]
  rooms: Array<{ id: string; name: string }>
}

type ResourceResult = {
  repositories: Array<{ repoId: string; deliveryMode: string; worktreePath: string }>
  digest: string
  group: {
    id: string
    runId: string
    taskId: string
    attemptId: string
    outcome: string
    resourceSnapshotDigest: string
    repositories: Array<{
      repoId: string
      baseCommit: string
      baseTree: string
      changedPaths: string[]
      checks: {
        RepositoryID: string
        Mode: string
        SelectionSource: string
        Status: string
        FinalContentVerified: boolean
        Checks: Array<{
          Argv: string[]
          WorkingDirectory: string
          ProviderSucceeded: boolean | null
          ExitCode: number | null
          ObservedStatus: string
          Status: string
        }>
      }
    }>
  }
  patches: Array<{ repoId: string; files: Array<{ path: string; lines?: Array<{ kind: string; text: string }>; hunks?: Array<{ lines: Array<{ kind: string; text: string }> }> }> }>
}

let server: ChildProcess | undefined
let serverLog = ''
let activeRunID = ''

test.beforeAll(async () => {
  await startServer()
})

test.afterAll(async () => {
  await stopServer('SIGTERM')
})

test.afterEach(async ({ request }, testInfo) => {
  if (testInfo.status === testInfo.expectedStatus) return
  await testInfo.attach('server-log', { body: Buffer.from(serverLog.slice(-1024 * 1024)), contentType: 'text/plain' })
  if (!activeRunID) return
  for (const [name, path] of [['run', `/api/runs/${activeRunID}`], ['result', `/api/v2/runs/${activeRunID}/result`]]) {
    const response = await request.get(path, { timeout: 10000 }).catch(() => undefined)
    if (response) await testInfo.attach(name, { body: await response.body(), contentType: 'application/json' })
  }
})

test('task-first real Pi changes two explicitly selected repositories, retains reviewed worktrees, and survives restart', async ({ page, request }, testInfo) => {
  test.setTimeout(900_000)

  const sourceBefore = await observeSource()
  const discovery = await getJSON<PiDiscoveryView>(request, '/api/pi/discovery')
  if (discovery.state !== 'ready') {
    throw new Error(`BLOCKED: native Pi discovery is ${discovery.state}${discovery.reason ? ` (${discovery.reason})` : ''}; no fake provider is permitted`)
  }
  expect(discovery.executablePath).toBeTruthy()
  expect(discovery.executableSha256).toMatch(/^[0-9a-f]{64}$/)
  expect(discovery.readyProviders?.length).toBeGreaterThan(0)

  const initial = await Promise.all([observeRepository(repoA), observeRepository(repoB)])
  for (const repository of initial) expect(repository.status).toBe('')

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Create Project', exact: true }).click()
  await page.getByLabel('Project name').fill('Task-first dual repository acceptance')
  await page.getByLabel('Project description').fill('Real Pi task-first acceptance fixture')
  await page.getByRole('button', { name: 'Create empty Project', exact: true }).click()
  await expect(page).toHaveURL(/\/projects\/project_[^/]+$/)
  const projectId = decodeURIComponent(new URL(page.url()).pathname.split('/').at(-1)!)

  // This route substitutes only the native OS folder chooser. The UI click still
  // initiates each product mutation, and the intercepted chooser request is
  // forwarded to the normal repository-admission endpoint.
  const chooserQueue = [repoA, repoB]
  await page.route(`**/api/v2/projects/${projectId}/repositories/choose-directory`, async (route) => {
    const locator = chooserQueue.shift()
    if (!locator) throw new Error('Unexpected third repository chooser request')
    const response = await route.fetch({
      url: `${baseURL}/api/v2/projects/${encodeURIComponent(projectId)}/repositories`,
      method: 'POST',
      headers: { 'content-type': 'application/json', 'idempotency-key': `task-first-add-${basename(locator)}` },
      postData: JSON.stringify({ locator }),
    })
    const text = await response.text()
    if (!response.ok()) throw new Error(`Repository admission failed (${response.status()}): ${text}`)
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ cancelled: false, ...JSON.parse(text) }) })
  })

  for (const repository of [repoA, repoB]) {
    await page.getByRole('button', { name: '＋ Add repository', exact: true }).click()
    await expect(page.locator('section[aria-label="Repositories"] code', { hasText: repository })).toBeVisible()
  }
  expect(chooserQueue).toHaveLength(0)

  const project = await getJSON<ProjectView>(request, `/api/v2/projects/${encodeURIComponent(projectId)}`)
  expect(project.repositories).toHaveLength(2)
  expect(new Set(project.repositories.map((repository) => repository.repoId)).size).toBe(2)
  expect(project.repositories.map((repository) => repository.localLocator).sort()).toEqual([repoA, repoB].sort())
  for (const repository of project.repositories) {
    expect(repository.state).toBe('active')
    expect(repository.availability).toBe('ready')
    expect(repository.dirty).toBe(false)
    expect(repository.head).toMatch(/^[0-9a-f]{40}$/)
  }

  await page.locator('section[aria-label="Topic Rooms"] .project-room-card', { hasText: 'General' }).locator('button.project-room-main').click()
  await expect(page).toHaveURL(/\/rooms\/room_[^/]+$/)
  const roomId = decodeURIComponent(new URL(page.url()).pathname.split('/').at(-1)!)
  expect(roomId).toBe(project.defaultRoomId)
  await page.getByRole('button', { name: '＋ New Task', exact: true }).first().click()

  for (const repository of project.repositories) {
    const card = page.locator('.task-resource-card', { hasText: repository.name })
    await card.getByRole('checkbox', { name: repository.name, exact: true }).check()
    await expect(card.getByRole('radio', { name: 'Can modify', exact: true })).toBeChecked()
    const target = card.getByRole('combobox', { name: `${repository.name} Target branch`, exact: true })
    await expect(target).toBeEnabled()
    await target.selectOption('refs/heads/main')
    await expect(card.getByRole('radio', { name: 'Choose relevant checks automatically', exact: true })).toBeChecked()
  }

  const byLocator = new Map(project.repositories.map((repository) => [repository.localLocator, repository]))
  const resourceA = byLocator.get(repoA)!
  const resourceB = byLocator.get(repoB)!
  const requirement = [
    'Implement the packageIdentity API in both selected Node.js packages. Work only in the two repository worktrees listed below and do not commit.',
    `In ${resourceA.repoId} (${resourceA.name}), update src/shared.js while preserving normalizeLabel(value), and export packageIdentity(value) that returns "alpha:" followed by the normalized label. Create test/task-first.test.js with meaningful node:test coverage for trimming and the alpha prefix.`,
    `In ${resourceB.repoId} (${resourceB.name}), update src/shared.js while preserving normalizeLabel(value), and export packageIdentity(value) that returns "beta:" followed by the normalized label. Create test/task-first.test.js with meaningful node:test coverage for trimming and the beta prefix.`,
    'Run the relevant existing test command in each repository after making its changes, and report both results truthfully.',
    'Do not change package.json, the existing test/shared.test.js, or fixture.bin. Finish with a concise summary of both repositories and the checks actually run.',
  ].join('\n')
  await page.getByLabel('What should Chora build?').fill(requirement)
  await page.getByRole('radio', { name: 'Trusted Local · No Sandbox', exact: true }).click()
  const disclosure = page.getByRole('dialog', { name: 'Trusted Local · No Sandbox' })
  if (await disclosure.count()) await disclosure.getByRole('button', { name: 'Acknowledge and use Trusted Local' }).click()
  await expect(page.getByRole('radio', { name: 'Trusted Local · No Sandbox', exact: true })).toBeChecked()
  await page.getByRole('button', { name: 'Start', exact: true }).click()
  await expect(page).toHaveURL(/\/rooms\/[^/]+\/tasks\/[^/]+\/runs\/[^/]+$/, { timeout: 120_000 })

  const runURL = page.url()
  const segments = new URL(runURL).pathname.split('/').filter(Boolean)
  const taskId = decodeURIComponent(segments[3])
  const runId = decodeURIComponent(segments[5])
  activeRunID = runId
  const runPath = `/api/runs/${encodeURIComponent(runId)}`

  let review: any
  await expect.poll(async () => {
    review = await getJSON<any>(request, runPath)
    if (['recovery_required', 'cancelled', 'revision_required'].includes(review.status)) {
      throw new Error(`Real Pi ended in ${review.status}: ${review.terminalReason ?? 'no terminal reason'}`)
    }
    return review.status
  }, { timeout: 600_000, intervals: [500, 1_000, 2_000] }).toBe('awaiting_review')
  const observedModel = review.attemptDetail.modelProvenance
  expect(observedModel.status).toBe('observed')
  expect(observedModel.identities.length).toBeGreaterThan(0)
  for (const identity of observedModel.identities) {
    expect(identity.modelId).toBeTruthy()
    expect(identity.provider).toBeTruthy()
    expect(identity.provider).not.toBe('unknown')
  }
  await page.reload()
  await expect(page.getByRole('heading', { name: 'Repository results', exact: true })).toBeVisible()

  const taskResources = await getJSON<any>(request, `/api/v2/tasks/${encodeURIComponent(taskId)}/resources`)
  expect(taskResources.snapshot.schemaVersion).toBe('chora.task-resources.v2')
  expect(taskResources.snapshot.taskId).toBe(taskId)
  expect(taskResources.snapshot.projectId).toBe(projectId)
  expect(taskResources.snapshot.roomId).toBe(roomId)
  expect(taskResources.snapshot.resources).toHaveLength(2)
  expect(taskResources.worktrees).toHaveLength(2)
  expect(taskResources.worktrees.every((worktree: any) => worktree.state === 'ready')).toBe(true)
  for (const resource of taskResources.snapshot.resources) {
    const admitted = project.repositories.find((repository) => repository.repoId === resource.repoId)
    expect(admitted).toBeTruthy()
    expect(resource.checkout).toBe(admitted!.localLocator)
    expect(resource.baseCommit).toBe(admitted!.head)
    expect(resource.physicalIdentity).toMatch(/^[0-9a-f]{64}$/)
    expect(resource.role).toBe('write')
    expect(resource.scope.mode).toBe('repository')
    expect(resource.scope.writableFiles).toEqual([])
    expect(resource.scope.writableDirectories).toEqual([])
    expect(resource.checks.mode).toBe('auto')
    expect(resource.checks.selectionSource).toBe('committed_configuration')
  }

  const result = await getJSON<ResourceResult>(request, `/api/v2/runs/${encodeURIComponent(runId)}/result`)
  expect(result.group.runId).toBe(runId)
  expect(result.group.taskId).toBe(taskId)
  expect(result.group.repositories).toHaveLength(2)
  expect(result.patches).toHaveLength(2)
  for (const repository of result.group.repositories) {
    expect([...repository.changedPaths].sort()).toEqual(['src/shared.js', 'test/task-first.test.js'])
    expect(repository.checks.RepositoryID).toBe(repository.repoId)
    expect(repository.checks.Mode).toBe('auto')
    expect(repository.checks.SelectionSource).toBe('agent_proposed')
    const frozen = taskResources.snapshot.resources.find((resource: any) => resource.repoId === repository.repoId)
    expect(frozen.checks.commands).toHaveLength(1)
    const frozenArgv = frozen.checks.commands[0].argv as string[]
    const observedCheck = repository.checks.Checks.find((check) => check.Argv.join('\0') === frozenArgv.join('\0'))
    expect(observedCheck, `missing frozen automatic check evidence for ${repository.repoId}`).toBeTruthy()
    expect(observedCheck!.WorkingDirectory).toBe(frozen.checks.commands[0].workingDirectory)
    expect(observedCheck!.ProviderSucceeded).toBe(true)
    expect([null, 0]).toContain(observedCheck!.ExitCode)
    expect(observedCheck!.ObservedStatus.toUpperCase()).toBe('PASS')
    expect(observedCheck!.Status.toUpperCase()).toBe('PASS')
    expect(repository.checks.Status.toUpperCase()).toBe('PASS')
    expect(repository.checks.FinalContentVerified).toBe(true)
    const patch = result.patches.find((candidate) => candidate.repoId === repository.repoId)
    expect(patch?.files.map((file) => file.path).sort()).toEqual(['src/shared.js', 'test/task-first.test.js'])
    const expectedPrefix = repository.repoId === resourceA.repoId ? 'alpha:' : 'beta:'
    expect(JSON.stringify(patch)).toContain(expectedPrefix)
    const card = page.locator(`article[aria-label="${repository.repoId}"]`)
    await expect(card.getByText('src/shared.js', { exact: true }).first()).toBeVisible()
    await expect(card.getByRole('table', { name: 'src/shared.js hunk 1', exact: true })).toContainText(expectedPrefix)
    await card.getByRole('navigation', { name: 'Changed files', exact: true }).getByRole('button', { name: /^task-first\.test\.js test / }).click()
    await expect(card.getByText('test/task-first.test.js', { exact: true }).first()).toBeVisible()
    await expect(card.getByRole('table', { name: 'test/task-first.test.js hunk 1', exact: true })).toContainText(expectedPrefix)
  }
  await page.screenshot({ path: testInfo.outputPath('dual-repository-review.png'), fullPage: true })

  const beforeReview = await Promise.all([observeRepository(repoA), observeRepository(repoB)])
  expect(beforeReview).toEqual(initial)

  await page.getByRole('button', { name: 'Accept', exact: true }).click()
  await expect(page.getByRole('region', { name: 'Task branch delivery', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Apply reviewed changes', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Write changes to repository', exact: true })).toHaveCount(0)
  const accepted = await getJSON<any>(request, runPath)
  expect(accepted.status).toBe('accepted')
  expect(accepted.resourceApply).toBeUndefined()
  const delivery = await getJSON<any>(request, `/api/v2/runs/${runId}/delivery`)
  expect(delivery.repositories).toHaveLength(2)
  for (const repository of delivery.repositories) expect(repository.status).toBe('uncommitted')

  // Review retains changes in each Task worktree. It never updates originals.
  const afterReview = await Promise.all([observeRepository(repoA), observeRepository(repoB)])
  expect(afterReview).toEqual(initial)
  expect(result.repositories).toHaveLength(2)
  for (const repository of result.repositories) {
    expect(repository.deliveryMode).toBe('task_branch')
    expect(repository.worktreePath).toBeTruthy()
    expect([repoA, repoB]).not.toContain(repository.worktreePath)
    const expectedPrefix = repository.repoId === resourceA.repoId ? 'alpha:' : 'beta:'
    expect(await readFile(join(repository.worktreePath, 'src/shared.js'), 'utf8')).toContain(expectedPrefix)
    const check = await execFileAsync('npm', ['test'], { cwd: repository.worktreePath })
    await writeFile(testInfo.outputPath(`${repository.repoId}-task-npm-test.txt`), check.stdout)
  }

  const serverPID = server?.pid
  await stopServer('SIGTERM')
  await startServer()
  expect(server?.pid).not.toBe(serverPID)
  await page.goto(runURL)
  await expect(page.getByRole('heading', { name: 'Repository results', exact: true })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Task branch delivery', exact: true })).toBeVisible()
  const persistedResult = await getJSON<ResourceResult>(request, `/api/v2/runs/${encodeURIComponent(runId)}/result`)
  expect(persistedResult).toEqual(result)
  const persistedDelivery = await getJSON<any>(request, `/api/v2/runs/${runId}/delivery`)
  expect(persistedDelivery).toEqual(delivery)
  const persistedRun = await getJSON<any>(request, runPath)
  expect(persistedRun.attemptDetail.modelProvenance).toEqual(observedModel)
  expect(await Promise.all([observeRepository(repoA), observeRepository(repoB)])).toEqual(initial)
  await page.screenshot({ path: testInfo.outputPath('review-retained-after-restart.png'), fullPage: true })

  const sourceAfter = await observeSource()
  expect(sourceAfter).toEqual(sourceBefore)
  const record = {
    schema: 'chora.task-first.real-pi.v2',
    result: 'PASS',
    source: { before: sourceBefore, after: sourceAfter },
    binarySha256: sha256(await readFile(binary)),
    discovery,
    project: { id: projectId, roomId, repositories: project.repositories },
    task: { id: taskId, resources: taskResources },
    run: { id: runId, url: runURL, attemptId: result.group.attemptId, modelProvenance: observedModel },
    resultGroup: result.group,
    delivery,
    qualification: 'modern dual-repository real Pi, final-content checks, Review retention and restart; SCM and legacy Apply use separate evidence',
    repositories: { before: initial, beforeReview, afterReview },
    evidenceGETs: ['/api/pi/discovery', `/api/v2/projects/${projectId}`, `/api/v2/tasks/${taskId}/resources`, `/api/runs/${runId}`, `/api/v2/runs/${runId}/result`],
    allProductMutationGesturesViaPublicUI: true,
    planApprovalInteraction: false,
    nativeFolderChooserOnlyStubbed: true,
    noFakeAgent: true,
    sourceCleanlinessWasNotRequired: true,
    screenshotsAndChecks: testInfo.outputDir,
  }
  await writeFile(evidencePath, `${JSON.stringify(record, null, 2)}\n`)
})

async function observeRepository(root: string) {
  return {
    root,
    head: await git(root, ['rev-parse', 'HEAD']),
    tree: await git(root, ['write-tree']),
    status: await git(root, ['status', '--porcelain=v1', '-uall']),
    sharedSha256: sha256(await readFile(join(root, 'src/shared.js'))),
    binarySha256: sha256(await readFile(join(root, 'fixture.bin'))),
  }
}

async function observeSource() {
  const exported = await execFileAsync(process.execPath, ['e2e/publication-candidate-export.mjs', '--dry-run'], { cwd: sourceRoot })
  const candidateIdentity = JSON.parse(exported.stdout)
  expect(candidateIdentity.status).toBe('passed')
  const revision = await git(sourceRoot, ['rev-parse', 'HEAD'])
  const status = await git(sourceRoot, ['status', '--porcelain=v1', '-uall'])
  const trackedDiff = await execFileAsync('git', ['diff', '--binary', '--no-ext-diff', 'HEAD'], { cwd: sourceRoot, maxBuffer: 64 * 1024 * 1024 })
  const statusSha256 = sha256(Buffer.from(status))
  const trackedDiffSha256 = sha256(Buffer.from(trackedDiff.stdout))
  return {
    candidateIdentity, revision, status, statusSha256, trackedDiffSha256,
    observationSha256: sha256(Buffer.from(`${revision}\0${statusSha256}\0${trackedDiffSha256}`)),
  }
}

async function git(cwd: string, args: string[]) {
  return (await execFileAsync('git', args, { cwd })).stdout.trimEnd()
}

function sha256(bytes: Buffer) {
  return createHash('sha256').update(bytes).digest('hex')
}

async function startServer() {
  if (server) throw new Error('Chora workbench is already running')
  serverLog = ''
  server = spawn(binary, ['workbench', '--source', sourceRoot, '--data', dataRoot, '--web', webRoot, '--port', port], {
    stdio: ['ignore', 'pipe', 'pipe'], env: process.env,
  })
  server.stdout?.on('data', (chunk) => { serverLog += chunk.toString() })
  server.stderr?.on('data', (chunk) => { serverLog += chunk.toString() })
  for (let attempt = 0; attempt < 1_200; attempt++) {
    if (server.exitCode !== null) throw new Error(`Chora workbench exited before readiness: ${serverLog}`)
    try {
      const response = await fetch(`${baseURL}/api/status`)
      if (response.ok) return
    } catch {
      // The dedicated listener may not be ready yet.
    }
    await delay(100)
  }
  throw new Error(`Chora workbench did not become ready: ${serverLog}`)
}

async function stopServer(signal: NodeJS.Signals) {
  const running = server
  server = undefined
  if (!running || running.exitCode !== null) return
  running.kill(signal)
  await Promise.race([
    new Promise<void>((resolve, reject) => {
      running.once('exit', () => resolve())
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
  if (!value) throw new Error(`${name} is required; run e2e/run-task-first-real.sh`)
  return value
}

function delay(milliseconds: number) {
  return new Promise<void>((resolve) => setTimeout(resolve, milliseconds))
}
