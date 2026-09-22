import { expect, test, type APIRequestContext } from '@playwright/test'
import { execFile } from 'node:child_process'
import { mkdtemp, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)

test.use({ actionTimeout: 20_000 })

test('material-only proposal is reviewed, persisted, and explicitly selected by later coding work', async ({ page, request }) => {
  const created = await request.post('/api/v2/projects', { data: { name: 'Document research journey' } })
  expect(created.status(), await created.text()).toBe(201)
  const project = await created.json() as { id: string; defaultRoomId: string }

  // The public UI starts its accepted Plan automatically. Add only the
  // build-tagged, digest-pinned Fake Pi protocol selector to that request.
  await page.route('**/api/tasks/*/runs', async route => {
    if (route.request().method() !== 'POST') return route.continue()
    if (!route.request().postDataJSON()?.revisionId) return route.continue()
    const input = route.request().postDataJSON() as Record<string, unknown>
    const response = await route.fetch({
      postData: JSON.stringify({ ...input, patchFixture: 'project-document' }),
      headers: { ...route.request().headers(), 'content-type': 'application/json' },
    })
    await route.fulfill({ response })
  })

  await page.goto(`/rooms/${project.defaultRoomId}`)
  await page.getByRole('button', { name: '＋ New Task', exact: true }).first().click()
  await page.getByLabel('What should Chora build?').fill('Write a sourced compatibility proposal')
  await page.getByLabel('Research / write a proposal').check()
  await page.getByLabel('Material title').fill('Migration notes')
  await page.getByLabel('Source locator').fill('https://example.test/migration-notes')
  await page.getByLabel('Markdown content').fill('# Migration notes\n\nThe public API must remain compatible.')
  await page.getByLabel('Override Project defaults for this task').check()
  await page.getByRole('radio', { name: 'Local execution · No Sandbox', exact: true }).check()
  await page.getByRole('dialog', { name: 'Local execution · No Sandbox', exact: true }).getByRole('button', { name: 'Acknowledge and use local execution', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Start', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: 'Start', exact: true }).click()

  await expect(page).toHaveURL(/\/runs\/run_[^/]+$/, { timeout: 120_000 })
  const runID = new URL(page.url()).pathname.split('/').at(-1)!
  const runView = await getJSON<{ task: { id: string } }>(request, `/api/runs/${runID}`)
  const documentPath = `/api/tasks/${runView.task.id}/document`
  const documentPanel = page.getByRole('region', { name: 'Project document', exact: true })
  await expect(documentPanel.getByText('Save agent proposal for review', { exact: true })).toBeVisible({ timeout: 120_000 })
  await documentPanel.getByText('Supplied sources', { exact: true }).click()
  await expect(documentPanel.getByText('https://example.test/migration-notes', { exact: true })).toBeVisible()
  await documentPanel.getByRole('button', { name: 'Save agent proposal for review', exact: true }).click()
  await expect(page.getByText(/SCM|Commit changes|Pull request/)).toHaveCount(0)

  // Exercise an unsaved human edit, then reject the exact persisted proposal
  // with human feedback so the normal retry path remains visible.
  const editor = page.getByRole('textbox', { name: 'Document Markdown', exact: true })
  const initialProposal = await editor.inputValue()
  await editor.fill(`${initialProposal}\n\nHuman draft note.`)
  await expect(documentPanel.getByRole('button', { name: 'Save new revision', exact: true })).toBeEnabled()
  await editor.fill(initialProposal)
  await documentPanel.getByLabel('Feedback or revision note', { exact: true }).fill('Separate compatibility claims from confirmed facts.')
  await documentPanel.getByRole('button', { name: 'Request document revision', exact: true }).click()
  await expect(page.getByText('Changes requested', { exact: true }).first()).toBeVisible()
  await page.getByRole('button', { name: 'Retry Pi', exact: true }).click()

  await expect(documentPanel.getByRole('button', { name: 'Save agent proposal for review', exact: true })).toBeVisible({ timeout: 120_000 })
  await documentPanel.getByRole('button', { name: 'Save agent proposal for review', exact: true }).click()
  await Promise.all([
    page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/document/reviews')),
    documentPanel.getByRole('button', { name: 'Accept this revision', exact: true }).click(),
  ])
  const accepted = await getJSON<{ status: string; revisions: Array<{ number: number; body: string; acceptedContextRevisionId?: string }> }>(request, documentPath)
  expect(accepted.status).toBe('accepted')
  const acceptedRevision = accepted.revisions.at(-1)!
  expect(acceptedRevision.acceptedContextRevisionId).toMatch(/^context_revision_/)
  expect(acceptedRevision.body).toBe(initialProposal)
  const acceptedContextRevisionID = acceptedRevision.acceptedContextRevisionId!

  await page.reload()
  await expect(documentPanel.getByText(/Accepted\. Explicitly select this revision/)).toContainText(acceptedContextRevisionID)

  // Add the repository only after research is accepted. The later coding Task
  // must opt into the exact accepted revision; acceptance never starts it.
  const repository = await createGitRepo()
  const admitted = await request.post(`/api/v2/projects/${project.id}/repositories`, {
    data: { locator: repository },
    headers: { 'Idempotency-Key': 'document-repository' },
  })
  expect(admitted.status(), await admitted.text()).toBe(200)
  const admittedRepository = await admitted.json() as { repository: { name: string } }
  await page.goto(`/rooms/${project.defaultRoomId}`)
  await page.getByRole('button', { name: '＋ New Task', exact: true }).first().click()
  await page.getByLabel('What should Chora build?').fill('Implement the accepted compatibility proposal')
  const acceptedChoice = page.getByRole('checkbox', { name: /Write a sourced compatibility proposal.*Revision/ })
  await expect(acceptedChoice).toBeVisible()
  await acceptedChoice.check()
  await page.getByRole('checkbox', { name: admittedRepository.repository.name, exact: true }).check()
  const targetBranch = page.getByRole('combobox', { name: `${admittedRepository.repository.name} Target branch`, exact: true })
  await expect(targetBranch.locator('option')).toHaveCount(2)
  await targetBranch.selectOption(await targetBranch.locator('option').nth(1).getAttribute('value') ?? '')
  expect(await acceptedChoice.isChecked()).toBe(true)

  const revisions = await getJSON<Array<{ id: string; locator?: string; body: string; digest: string }>>(request, `/api/rooms/${project.defaultRoomId}/revisions`)
  const frozen = revisions.find(revision => revision.id === acceptedContextRevisionID)
  expect(frozen?.locator).toMatch(/^chora:\/\/project-document\//)
  expect(frozen?.body).toBe(initialProposal)

  await page.getByLabel('Override Project defaults for this task').check()
  await page.getByRole('radio', { name: 'Local execution · No Sandbox', exact: true }).check()
  await expect(page.getByRole('button', { name: 'Start', exact: true })).toBeEnabled({ timeout: 30_000 })
  const codingCreation = page.waitForRequest(value => value.method() === 'POST' && value.url().includes(`/api/v2/rooms/${project.defaultRoomId}/tasks`))
  await page.getByRole('button', { name: 'Start', exact: true }).click()
  const codingInput = (await codingCreation).postDataJSON() as { requirement: string; revisionIds: string[]; resources: unknown[] }
  expect(codingInput.requirement).toBe('Implement the accepted compatibility proposal')
  expect(codingInput.revisionIds).toContain(acceptedContextRevisionID)
  expect(codingInput.resources.length).toBeGreaterThan(0)
  await expect(page).toHaveURL(/\/tasks\/task_[^/]+\/runs\/run_[^/]+$/, { timeout: 120_000 })
  const codingTaskID = new URL(page.url()).pathname.match(/task_[^/]+/)?.[0]
  expect(codingTaskID).toMatch(/^task_/)
  const codingTask = await getJSON<{ selection: { selected: Array<{ revisionId: string; digest: string }> } }>(request, `/api/tasks/${codingTaskID}`)
  const selected = codingTask.selection.selected.find(value => value.revisionId === acceptedContextRevisionID)
  expect(selected?.digest).toBe(frozen?.digest)
  const codingRunID = new URL(page.url()).pathname.match(/run_[^/]+/)?.[0]
  if (codingRunID) {
    const codingRun = await getJSON<{ snapshot: { digest: string; selected: Array<{ revisionId: string; digest: string }> } }>(request, `/api/runs/${codingRunID}`)
    expect(codingRun.snapshot.selected).toContainEqual(expect.objectContaining({ revisionId: acceptedContextRevisionID, digest: frozen?.digest }))
  }
  await page.goto(`/rooms/${project.defaultRoomId}/tasks/${runView.task.id}/runs/${runID}`)
  const newer = `${initialProposal}\n\nHuman refinement after coding task creation.`
  await page.getByRole('textbox', { name: 'Document Markdown', exact: true }).fill(newer)
  await documentPanel.getByLabel('Feedback or revision note', { exact: true }).fill('New context for future tasks.')
  await documentPanel.getByRole('button', { name: 'Save new revision', exact: true }).click()
  await documentPanel.getByRole('button', { name: 'Accept this revision', exact: true }).click()
  await expect.poll(async () => (await getJSON<{ status: string }>(request, documentPath)).status).toBe('accepted')
  const after = await getJSON<{ selection: { selected: Array<{ revisionId: string; digest: string }> } }>(request, `/api/tasks/${codingTaskID}`)
  expect(after.selection.selected.find(value => value.revisionId === acceptedContextRevisionID)?.digest).toBe(frozen?.digest)
  expect(after.selection.selected).toHaveLength(codingTask.selection.selected.length)
  // The document fixture owns one fake Pi runtime. Finish the coding Run
  // before another test can replace its runtime sink with a delegation child.
  await expect.poll(async () => {
    const workspace = await getJSON<{ tasks: Array<{ id: string; latestRun?: { status: string } }> }>(request, `/api/rooms/${project.defaultRoomId}/workspace`)
    return workspace.tasks.find(task => task.id === codingTaskID)?.latestRun?.status
  }).not.toBe('running')
  await page.unrouteAll({ behavior: 'wait' })
})

async function createGitRepo() {
  const root = await mkdtemp(join('/private/tmp', 'chora-document-coding-'))
  await execFileAsync('git', ['init', '-b', 'main', root])
  await execFileAsync('git', ['-C', root, 'config', 'user.email', 'e2e@chora.local'])
  await execFileAsync('git', ['-C', root, 'config', 'user.name', 'Chora E2E'])
  await writeFile(join(root, 'README.md'), '# Compatibility fixture\n')
  await execFileAsync('git', ['-C', root, 'add', 'README.md'])
  await execFileAsync('git', ['-C', root, 'commit', '-m', 'initial'])
  return root
}

async function getJSON<T>(request: APIRequestContext, path: string): Promise<T> {
  const response = await request.get(path)
  expect(response.status(), await response.text()).toBe(200)
  return response.json() as Promise<T>
}

test('bounded local delegation runs assignments and leaves final review to the human', async ({ page, request }) => {
  await page.goto('/')
  await page.evaluate(() => localStorage.setItem('chora.locale', 'en'))
  const ack = await request.post('/api/agent-execution/trusted-local-acknowledgements', { headers: { 'Idempotency-Key': 'delegation-e2e-ack' }, data: { policyVersion: 'chora.trusted-local-disclosure.v1' } })
  expect(ack.status(), await ack.text()).toBe(200)

  const projectResponse = await request.post('/api/v2/projects', { data: { name: 'Delegation acceptance' } })
  expect(projectResponse.status()).toBe(201)
  const project = await projectResponse.json()
  const revisions = await getJSON<Array<{ id: string }>>(request, `/api/rooms/${project.defaultRoomId}/revisions`)
  const created = await request.post(`/api/v2/rooms/${project.defaultRoomId}/tasks`, {
    headers: { 'Idempotency-Key': 'delegation-e2e-parent' },
    data: { title: 'Explore compatibility', requirement: 'Research implementation choices and risks.', agentExecutionProfile: 'trusted_local', outcomeKind: 'document', revisionIds: [revisions[0].id], materials: [{ title: 'Compatibility', locator: 'supplied:compatibility', body: 'Preserve existing clients. Deployment order remains unknown.' }] },
  })
  expect(created.status(), await created.text()).toBe(201)
  const parent = await created.json()
  await page.goto(`/rooms/${project.defaultRoomId}/tasks/${parent.id}`)
  const panel = page.getByRole('region', { name: 'Agent delegation', exact: true })
  await panel.getByLabel('Agent role 1', { exact: true }).fill('Designer')
  await panel.getByLabel('Task title 1', { exact: true }).fill('Implementation choices')
  await panel.getByLabel('Assignment instructions 1', { exact: true }).fill('Compare implementation options using the supplied source.')
  await panel.getByRole('button', { name: 'Add assignment', exact: true }).click()
  await panel.getByLabel('Agent role 2', { exact: true }).fill('Reviewer')
  await panel.getByLabel('Task title 2', { exact: true }).fill('Compatibility risks')
  await panel.getByLabel('Assignment instructions 2', { exact: true }).fill('Identify compatibility risks and unknowns in the supplied source.')
  await panel.getByRole('button', { name: 'Start delegation', exact: true }).click()
  await expect(panel.getByText('All assignments finished execution. Review their evidence and results before accepting them.', { exact: true })).toBeVisible({ timeout: 120_000 })
  await expect(panel.getByText('Delegated finding', { exact: true })).toHaveCount(2)
  const endpoint = `/api/tasks/${parent.id}/delegation`
  const view = await getJSON<{ version: number; state: string; children: Array<{ taskId: string; runId: string; resultId: string; resultDigest: string }> }>(request, endpoint)
  expect(view.state).toBe('awaiting_review')
  expect(new Set(view.children.map(child => child.taskId)).size).toBe(2)
  for (const child of view.children) {
    expect(child.resultDigest).toMatch(/^[a-f0-9]{64}$/)
    const document = await getJSON<{ version: number; reviews: unknown[] }>(request, `/api/tasks/${child.taskId}/document`)
    expect(document.version).toBe(0)
    expect(document.reviews).toHaveLength(0)
  }
  await page.reload()
  await expect(panel.getByText('Delegated finding', { exact: true })).toHaveCount(2)
  await panel.getByRole('button', { name: 'Open child task', exact: true }).first().click()
  await expect(page.getByRole('button', { name: 'Open parent task', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Open parent task', exact: true }).click()
  await expect(panel.getByText('Delegated finding', { exact: true })).toHaveCount(2)
  await page.setViewportSize({ width: 390, height: 844 })
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test('Agent-proposed assignments require explicit source-bound start and survive reload', async ({ page, request }) => {
  const acknowledgement = await request.post('/api/agent-execution/trusted-local-acknowledgements', { headers: { 'Idempotency-Key': 'generated-plan-ack' }, data: { policyVersion: 'chora.trusted-local-disclosure.v1' } })
  expect(acknowledgement.ok()).toBeTruthy()
  const created = await request.post('/api/v2/projects', { data: { name: 'Generated delegation plan' } })
  expect(created.status()).toBe(201)
  const project = await created.json()
  await page.route('**/api/tasks/*/runs', async route => {
    const input = route.request().postDataJSON() as Record<string, unknown> | null
    if (route.request().method() !== 'POST' || !input?.revisionId) return route.continue()
    const response = await route.fetch({ postData: JSON.stringify({ ...input, patchFixture: 'delegation-plan' }), headers: { ...route.request().headers(), 'content-type': 'application/json' } })
    await route.fulfill({ response })
  })
  await page.goto(`/rooms/${project.defaultRoomId}`)
  await page.getByRole('button', { name: '＋ New Task', exact: true }).first().click()
  await page.getByLabel('What should Chora build?').fill('Propose bounded research assignments using the supplied design')
  await page.getByLabel('Research / write a proposal').check()
  await page.getByLabel('Material title').fill('Design')
  await page.getByLabel('Source locator').fill('supplied:design')
  await page.getByLabel('Markdown content').fill('Preserve existing clients. The deployment sequence is unknown.')
  await page.getByLabel('Override Project defaults for this task').check()
  await page.getByRole('radio', { name: 'Local execution · No Sandbox', exact: true }).check()
  const disclosure = page.getByRole('dialog', { name: 'Local execution · No Sandbox', exact: true })
  if (await disclosure.isVisible()) await disclosure.getByRole('button', { name: 'Acknowledge and use local execution', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Start', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: 'Start', exact: true }).click()
  await expect(page).toHaveURL(/\/runs\/run_[^/]+$/, { timeout: 120_000 })
  const runID = new URL(page.url()).pathname.split('/').at(-1)!
  const run = await getJSON<{ task: { id: string } }>(request, `/api/runs/${runID}`)
  const endpoint = `/api/tasks/${run.task.id}/delegation`
  await expect(page.getByRole('region', { name: 'Project document', exact: true }).getByRole('button', { name: 'Save agent proposal for review', exact: true })).toBeVisible({ timeout: 120_000 })
  const proposal = await getJSON<{ available: boolean; source: { attemptId: string; resultDigest: string; planDigest: string } }>(request, `${endpoint}/proposal`)
  expect(proposal.available).toBe(true)
  const panel = page.getByRole('region', { name: 'Agent delegation', exact: true })
  await panel.getByRole('button', { name: 'Load Agent plan', exact: true }).click()
  await expect(panel.getByText('Compare supplied options', { exact: false })).toBeVisible()
  expect((await getJSON<{ state: string }>(request, endpoint)).state).toBe('not_started')
  const stale = await request.post(endpoint, { headers: { 'Idempotency-Key': 'stale-generated-plan' }, data: { sourceAttemptId: proposal.source.attemptId, expectedResultDigest: '0'.repeat(64) } })
  expect(stale.status(), await stale.text()).toBe(409)
  expect((await getJSON<{ state: string }>(request, endpoint)).state).toBe('not_started')
  const startRequest = page.waitForRequest(value => value.method() === 'POST' && value.url().endsWith(endpoint))
  await panel.getByRole('button', { name: 'Start proposed delegation', exact: true }).click()
  expect((await startRequest).postDataJSON()).toEqual({ sourceAttemptId: proposal.source.attemptId, expectedResultDigest: proposal.source.resultDigest })
  await expect(panel.getByText('All assignments finished execution. Review their evidence and results before accepting them.', { exact: true })).toBeVisible({ timeout: 120_000 })
  const finished = await getJSON<{ source: { planDigest: string; current: boolean }; children: Array<{ taskId: string; runId: string }> }>(request, endpoint)
  expect(finished.source).toMatchObject({ planDigest: proposal.source.planDigest, current: true })
  expect(finished.children).toHaveLength(2)
  const documentState = await getJSON<{ version: number; reviews: unknown[] }>(request, `/api/tasks/${run.task.id}/document`)
  expect(documentState).toMatchObject({ version: 0, reviews: [] })
  await page.reload()
  expect(await getJSON(request, endpoint)).toEqual(finished)
  await panel.getByText('Plan source', { exact: true }).click()
  await expect(panel.getByText(proposal.source.planDigest, { exact: true })).toBeVisible()
  await page.setViewportSize({ width: 390, height: 844 })
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.unrouteAll({ behavior: 'wait' })
})

for (const synthesize of [false, true]) test(`one research-delegation start plans and executes with synthesis=${synthesize}`, async ({ page, request }) => {
  const acknowledgement = await request.post('/api/agent-execution/trusted-local-acknowledgements', { headers: { 'Idempotency-Key': 'automatic-plan-ack' }, data: { policyVersion: 'chora.trusted-local-disclosure.v1' } })
  expect(acknowledgement.ok()).toBeTruthy()
  const created = await request.post('/api/v2/projects', { data: { name: `Single-start research delegation ${synthesize}` } })
  expect(created.status()).toBe(201)
  const project = await created.json()
  const starts: string[] = []
  page.on('request', req => { if (req.method() === 'POST' && (/\/delegation\/planning$/.test(req.url()) || /\/runs$/.test(req.url()) || /\/delegation$/.test(req.url()))) starts.push(new URL(req.url()).pathname) })
  await page.goto(`/rooms/${project.defaultRoomId}`)
  await page.getByRole('button', { name: '＋ New Task', exact: true }).first().click()
  await page.getByLabel('Plan and delegate research', { exact: true }).check()
  if (synthesize) await page.getByLabel('Generate one synthesis after research', { exact: true }).check()
  await page.getByLabel('What should Chora investigate or document?').fill('Compare compatibility options and restart risks')
  await page.getByLabel('Material title').fill('Design')
  await page.getByLabel('Source locator').fill('supplied:design')
  await page.getByLabel('Markdown content').fill('Preserve existing clients. Server restart must not duplicate work. The deployment sequence is unknown.')
  await page.getByLabel('Override Project defaults for this task').check()
  await page.getByRole('radio', { name: 'Local execution · No Sandbox', exact: true }).check()
  const disclosure = page.getByRole('dialog', { name: 'Local execution · No Sandbox', exact: true })
  if (await disclosure.isVisible()) await disclosure.getByRole('button', { name: 'Acknowledge and use local execution', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Start research delegation', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: 'Start research delegation', exact: true }).click()
  await expect(page).toHaveURL(/\/tasks\/task_[^/]+$/, { timeout: 120_000 })
  const taskID = new URL(page.url()).pathname.split('/').at(-1)!
  const endpoint = `/api/tasks/${taskID}/delegation`
  const panel = page.getByRole('region', { name: 'Agent delegation', exact: true })
  await expect(panel.getByText('All assignments finished execution. Review their evidence and results before accepting them.', { exact: true })).toBeVisible({ timeout: 120_000 })
  expect(starts).toEqual([`${endpoint}/planning`])
  const finished = await getJSON<{ planning: { state: string; runId: string }; source: { runId: string }; children: Array<{ taskId: string }>; synthesis?: { enabled: boolean; state: string; taskId: string; runId: string; resultId: string; current: boolean } }>(request, endpoint)
  expect(finished.planning.state).toBe('imported')
  expect(finished.source.runId).toBe(finished.planning.runId)
  expect(finished.children).toHaveLength(2)
  if (synthesize) {
    expect(finished.synthesis).toMatchObject({ enabled: true, state: 'awaiting_review', current: true })
    expect(finished.synthesis?.taskId).toMatch(/^task_/)
    expect(finished.synthesis?.resultId).toMatch(/^result_/)
    await expect(panel.getByRole('button', { name: 'Open synthesis task', exact: true })).toBeVisible()
  } else expect(finished.synthesis?.enabled).not.toBe(true)
  for (const id of [taskID, ...finished.children.map(child => child.taskId), ...(finished.synthesis?.taskId ? [finished.synthesis.taskId] : [])]) {
    expect(await getJSON(request, `/api/tasks/${id}/document`)).toMatchObject({ version: 0, reviews: [] })
  }
  await expect(panel.getByRole('button', { name: 'Start delegation', exact: true })).toHaveCount(0)
  await expect(panel.getByRole('button', { name: 'Start research delegation', exact: true })).toHaveCount(0)
  await page.reload()
  expect(await getJSON(request, endpoint)).toEqual(finished)
  await panel.getByRole('button', { name: 'Open planning run', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/runs/${finished.planning.runId}$`))
  if (synthesize) {
    await panel.getByRole('button', { name: 'Open synthesis task', exact: true }).click()
    await expect(page).toHaveURL(new RegExp(`/runs/${finished.synthesis!.runId}$`))
    const documentPanel = page.getByRole('region', { name: 'Project document', exact: true })
    await documentPanel.getByRole('button', { name: 'Save agent proposal for review', exact: true }).click()
    await documentPanel.getByLabel('Feedback or revision note', { exact: true }).fill('The report needs clearer evidence; preserve the original sources.')
    await documentPanel.getByRole('button', { name: 'Request document revision', exact: true }).click()
    await expect(page.getByText('Changes requested', { exact: true }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Retry Pi', exact: true })).toHaveCount(0)
    await panel.getByRole('button', { name: 'Open parent task', exact: true }).click()
    await expect(page).toHaveURL(new RegExp(`/tasks/${taskID}$`))
    await expect(panel.getByText('All assignments finished execution. Review their evidence and results before accepting them.', { exact: true })).toHaveCount(0)
    expect(await getJSON(request, endpoint)).toMatchObject({ state: 'needs_attention', synthesis: { state: 'revision_required', current: true, resultId: finished.synthesis!.resultId } })
    await expect(panel.getByText('Read synthesis report', { exact: true })).toBeVisible()
    expect(starts).toEqual([`${endpoint}/planning`])
  }
})
