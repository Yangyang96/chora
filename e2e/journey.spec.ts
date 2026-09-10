import { expect, test } from '@playwright/test'

// Core development flow: opening a saved Task is read-only; explicit Start
// activates its non-blocking Plan. The fixture-only diagnostic Task intentionally has no
// real Spec Coding binding, so real Verification/Review belongs to the installed
// public-UI acceptance rather than this deterministic smoke test.
test('resumes a saved Task without execution and explicitly starts the diagnostic fixture', async ({ page, request }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'New Room', exact: true }).click()
  await page.getByLabel('Room name').fill('Journey Room')
  await page.getByRole('button', { name: 'Create Room', exact: true }).click()
  await expect(page).toHaveURL(/\/rooms\//)
  await expect(page.getByText('This Room has no Tasks yet.')).toBeVisible()

  const roomID = new URL(page.url()).pathname.split('/').filter(Boolean)[1]
  const roomResponse = await request.get(`/api/rooms/${encodeURIComponent(roomID)}`)
  expect(roomResponse.ok()).toBeTruthy()
  const room = await roomResponse.json()
  const taskResponse = await request.post(`/api/rooms/${encodeURIComponent(roomID)}/tasks`, {
    data: {
      title: 'Add a sort button', goal: 'Add a sort button', criteria: ['Requirement satisfied'],
      revisionIds: [room.revisions[0].id], executionProfile: 'diagnostic_fake',
    },
  })
  expect(taskResponse.ok()).toBeTruthy()
  const task = await taskResponse.json()
  await page.goto(`/rooms/${encodeURIComponent(roomID)}/tasks/${encodeURIComponent(task.id)}`)

  // Resume displays the saved Plan and leaves execution to explicit Start.
  await expect(page.getByRole('region', { name: 'Plan', exact: true })).toBeVisible()
  await expect(page).toHaveURL(new RegExp(`/tasks/${task.id}$`))
  const history = await request.get(`/api/rooms/${encodeURIComponent(roomID)}/tasks/${encodeURIComponent(task.id)}/runs`)
  expect(history.ok()).toBeTruthy()
  expect((await history.json()).runs).toHaveLength(0)
  await page.getByRole('button', { name: 'Start Run', exact: true }).click()

  await expect(page).toHaveURL(/\/runs\//)
  await expect(page.getByRole('region', { name: 'Plan', exact: true })).toContainText('Plan')
  await expect(page.getByText('Automatically activated inside the existing Room boundary.')).toBeVisible()
  await expect(page.getByText('Decision needed')).toBeVisible({ timeout: 60_000 })
  const gate = page.locator('.decision-gate h3')
  await expect(gate).toContainText('Should the Agent continue')
  await page.getByRole('button', { name: 'Continue', exact: true }).click()

  await expect(page.getByText('Agent finished')).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText('Independent verification was not run')).toBeVisible()
  await expect(page.getByRole('region', { name: 'Visible conversation' })).toContainText('Add a sort button')

  // A long reply must not auto-place its details in the narrow speaker column.
  // Mock presentation data only; the diagnostic fixture's persisted Run stays intact.
  const runAPI = `/api${new URL(page.url()).pathname}`
  const longReply = 'Original Agent reply with preserved information. '.repeat(30)
  let runReads = 0
  await page.route(`**${runAPI}`, async (route) => {
    runReads += 1
    const response = await route.fetch()
    const body = await response.json()
    body.agentReport = { id: 'layout-report', attemptId: 'layout-attempt', summary: 'Done', finalText: longReply, completedAt: '2026-09-07T00:00:00Z', authority: 'non_authoritative_agent_claim', claimedChecks: [] }
    body.agentExecution = { profile: 'trusted_local', runtimeSource: 'local_pi', sandboxed: false, disclosureLabel: 'Trusted Local · No Sandbox' }
    body.reviewablePatch = { patchDigest: 'a', baselineDigest: 'b', declaredFilesDigest: 'c', resultId: 'result', agentAttemptId: 'attempt', verificationAttemptId: '', artifactId: 'artifact', rawDownload: '/patch', files: [{ path: 'README.md', lines: [{ kind: 'added', newLine: 1, text: 'Task complete' }] }] }
    body.status = 'accepted'
    body.agentReport.claimedChecks = [{ criterionId: 'check-1', status: 'FAIL', evidence: 'Recorded check failed' }]
    body.patchApplication = { state: 'applied', affectedPaths: ['README.md'] }
    await route.fulfill({ response, json: body })
  })
  await page.reload()
  const outcome = page.getByRole('region', { name: 'Changes and next steps' })
  await expect(outcome.getByText('Checks did not pass.')).toBeVisible()
  await expect(outcome.getByText('Written to repository', { exact: true })).toBeVisible()
  await expect(outcome.getByText(/Later Git commits are not tracked here/)).toBeVisible()
  await expect(outcome.getByText('Human patch review')).toHaveCount(0)
  await expect(outcome.getByText('Trusted Local · No Sandbox')).toBeVisible()
  await outcome.getByText('Check source', { exact: true }).click()
  await expect(outcome.getByText('Chora did not run a separate check of these changes.')).toBeVisible()
  for (const [link, target] of [['View changes', 'diff-title'], ['View check results', 'checks-title']]) {
    const readsBefore = runReads
    await outcome.getByRole('link', { name: link, exact: true }).click()
    await expect(page).toHaveURL(new RegExp(`#${target}$`))
    await expect(page.locator(`#${target}`)).toBeInViewport()
    expect(runReads).toBe(readsBefore)
  }
  const details = page.locator('.agent-reply-details')
  await expect(details).toBeVisible()
  await details.locator('summary').click()
  await expect(details.locator('p')).toHaveText(longReply)
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 })
    const box = await details.locator('p').boundingBox()
    const card = await page.locator('.conversation-agent').boundingBox()
    const summary = await details.locator('summary').boundingBox()
    expect(box).not.toBeNull()
    expect(card).not.toBeNull()
    expect(summary).not.toBeNull()
    expect(box!.width).toBeGreaterThan(card!.width * 0.85)
    expect(box!.y).toBeGreaterThanOrEqual(summary!.y + summary!.height)
    expect(box!.x + box!.width).toBeLessThanOrEqual(card!.x + card!.width)
  }
})
