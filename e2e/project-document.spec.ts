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
  await page.getByLabel('Work with supplied material only').check()
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
  await expect(page).toHaveURL(/\/tasks\/task_[^/]+(?:\/runs\/run_[^/]+)?$/, { timeout: 120_000 })
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
