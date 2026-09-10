import { chromium, expect } from '@playwright/test'
import { existsSync, readFileSync, mkdirSync } from 'node:fs'
import { execFileSync } from 'node:child_process'

const [baseURL, route, firstRepoID, secondRepoID, firstWorktree, secondWorktree, firstBaseBranch, secondBaseBranch] = process.argv.slice(2)
if (![baseURL, route, firstRepoID, secondRepoID, firstWorktree, secondWorktree, firstBaseBranch, secondBaseBranch].every(Boolean)) {
  throw new Error('usage: task-delivery-browser.mjs BASE_URL ROUTE FIRST_REPO_ID SECOND_REPO_ID FIRST_WORKTREE SECOND_WORKTREE FIRST_BASE_BRANCH SECOND_BASE_BRANCH')
}

const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage()
  page.on('response', async (response) => {
    if (response.url().includes('/delivery/') && response.status() >= 400) console.error('DELIVERY_HTTP_ERROR', response.status(), await response.text())
  })
  const deliveryMutations = []
  page.on('request', (request) => {
    const path = new URL(request.url()).pathname
    if (request.method() === 'POST' && /\/delivery\/(preview|confirm|refresh)$/.test(path)) deliveryMutations.push(path)
  })

  const card = (repoID) => page.locator(`section.task-delivery article[data-repo-id="${repoID}"]`)
  const overview = async (label) => {
    await expect(page.locator('.stream-head-details > strong').first()).toHaveText(label)
    await expect(page.locator('.sidebar-task.on .sidebar-task-status')).toHaveText(label, { timeout: 10000 })
  }
  const expectText = async (repo, value) => repo.getByText(value, { exact: true }).waitFor()
  const expectPreviewRow = async (repo, label, value) => {
    const escaped = label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    const row = repo.getByRole('group', { name: 'Delivery preview', exact: true }).locator('p').filter({ hasText: new RegExp(`^${escaped} ·`) })
    await row.waitFor()
    const content = await row.textContent()
    if (!content?.includes(value)) throw new Error(`${label} preview row omitted ${value}: ${content}`)
  }
  const previewAndConfirm = async (repo, action, verifyPreview) => {
    const before = deliveryMutations.length
    await repo.getByRole('button', { name: `Preview ${action}`, exact: true }).click()
    await repo.getByRole('button', { name: `Confirm ${action}`, exact: true }).waitFor()
    if (deliveryMutations.length !== before + 1 || !deliveryMutations.at(-1).endsWith('/delivery/preview')) {
      throw new Error(`${action} preview performed an unexpected mutation`)
    }
    await verifyPreview()
    await repo.getByRole('button', { name: `Confirm ${action}`, exact: true }).click()
    if (deliveryMutations.length !== before + 2 || !deliveryMutations.at(-1).endsWith('/delivery/confirm')) {
      throw new Error(`${action} confirmation was not a separate request`)
    }
  }
  const deliver = async (name, reloadAfterCommit = false) => {
    let repo = card(name)
    const branch = (await repo.locator('.delivery-git-details p').filter({ hasText: /^Task branch ·/ }).locator('code').textContent()).trim()
    if (!/^feature\/[0-9a-f]{12}$/.test(branch)) throw new Error(`Task branch is not short: ${branch}`)
    await expect(repo.getByLabel('Commit message', { exact: true })).toHaveValue('fix: update reviewed repository content\n\nUpdate same.txt with the reviewed repository-specific content.', { timeout: 30000 })
    if (reloadAfterCommit) {
      const storedDraft = await page.evaluate((repoID) => Object.entries(localStorage).find(([key]) => key.startsWith('chora.delivery-draft.v1:') && key.includes(`:${repoID}:`) && key.endsWith(':commit'))?.[1], name)
      if (!storedDraft || JSON.parse(storedDraft).value.message !== await repo.getByLabel('Commit message', { exact: true }).inputValue()) throw new Error('Generated commit draft was not persisted before reload')
      const restoredContext = page.waitForResponse((response) => response.url().endsWith('/delivery/draft-context') && response.request().postDataJSON()?.repoId === name)
      await page.reload(); repo = card(name)
      const contextResponse = await restoredContext
      if (!contextResponse.ok()) throw new Error(`Draft context reload failed: ${contextResponse.status()}`)
      await expect(repo.getByLabel('Commit message', { exact: true })).toHaveValue(JSON.parse(storedDraft).value.message)
      const recoveredDraft = await page.evaluate((repoID) => Object.entries(localStorage).find(([key]) => key.startsWith('chora.delivery-draft.v1:') && key.includes(`:${repoID}:`) && key.endsWith(':commit'))?.[1], name)
      if (recoveredDraft !== storedDraft) throw new Error('Reload changed the saved commit draft')
    }
    await previewAndConfirm(repo, 'Commit task branch', async () => {
      await repo.getByText('Reviewed files', { exact: true }).waitFor()
      await repo.getByText('same.txt', { exact: true }).waitFor()
    })
    await expectText(repo, 'Committed locally')
    await overview(name === firstRepoID ? 'Committed 1/2' : 'Cleaned up 1/2')
    if (reloadAfterCommit) {
      await page.reload()
      repo = card(name)
      await expectText(repo, 'Committed locally')
    }

    await previewAndConfirm(repo, 'Push task branch', async () => {
      const preview = repo.getByRole('group', { name: 'Delivery preview', exact: true })
      await preview.getByText('Push task branch', { exact: true }).waitFor()
      await expectPreviewRow(repo, 'Remote', 'origin')
      await expectPreviewRow(repo, 'Destination ref', `refs/heads/${branch}`)
    })
    await expectText(repo, 'Task branch pushed')
    await overview(name === firstRepoID ? 'Pushed 1/2' : 'Cleaned up 1/2')
    await expect(repo.getByLabel('Title', { exact: true })).toHaveValue('fix: update reviewed repository content', { timeout: 30000 })
    await expect(repo.getByLabel('Description', { exact: true })).toHaveValue(/- Update same.txt[\s\S]*Tests not run: no checks were selected./)
    let expectedDescription = 'Tests not run: no checks were selected.'
    if (reloadAfterCommit) {
      const before = deliveryMutations.length
      await repo.getByText('Ask AI to revise', { exact: true }).click()
      const instructions = repo.getByLabel('Revision instructions', { exact: true })
      await instructions.fill('简化成一句话')
      const revise = repo.getByRole('button', { name: 'Revise draft', exact: true })
      const generate = repo.getByRole('button', { name: 'Generate pull request description', exact: true })
      const [inputBox, reviseBox, generateBox] = await Promise.all([instructions.boundingBox(), revise.boundingBox(), generate.boundingBox()])
      if (!inputBox || !reviseBox || !generateBox || Math.abs(inputBox.x + inputBox.width - reviseBox.x - reviseBox.width) > 1 || Math.abs(reviseBox.height - generateBox.height) > 1 || Math.abs(reviseBox.y - inputBox.y - inputBox.height - 8) > 1) {
        throw new Error('Revision button must align right with the input, match other button heights, and have an 8px gap')
      }
      await revise.click()
      expectedDescription = 'Update the reviewed repository content; tests were not run.'
      await expect(repo.getByLabel('Description', { exact: true })).toHaveValue(expectedDescription)
      await expect(repo.getByRole('group', { name: 'New draft candidate', exact: true })).toHaveCount(0)
      await expect(repo.getByLabel('Title', { exact: true })).toHaveValue('fix: update reviewed repository content')
      if (deliveryMutations.length !== before) throw new Error('Revising text performed an SCM operation')
      await repo.getByRole('button', { name: 'Undo draft replacement', exact: true }).click()
      await expect(repo.getByLabel('Description', { exact: true })).toHaveValue(/Tests not run: no checks were selected./)
      await revise.click()
      await expect(repo.getByRole('group', { name: 'New draft candidate', exact: true })).toBeVisible()
      await repo.getByRole('button', { name: 'Use this draft', exact: true }).click()
      await expect(repo.getByLabel('Description', { exact: true })).toHaveValue(expectedDescription)
    }
    if (process.env.CHORA_DELIVERY_SCREENSHOT && reloadAfterCommit) await page.locator('section.task-delivery').screenshot({ path: process.env.CHORA_DELIVERY_SCREENSHOT })
    await previewAndConfirm(repo, 'Create pull request', async () => {
      await repo.getByRole('group', { name: 'Delivery preview', exact: true }).getByText('Create pull request', { exact: true }).waitFor()
      await expectPreviewRow(repo, 'GitHub repository', `browser/${branch.replaceAll('/', '-')}`)
      await expectPreviewRow(repo, 'Head branch', branch)
      await expectPreviewRow(repo, 'Base branch', name === firstRepoID ? firstBaseBranch : secondBaseBranch)
      await expectPreviewRow(repo, 'Title', 'fix: update reviewed repository content')
      await expectPreviewRow(repo, 'Description', expectedDescription)
    })
    await expectText(repo, 'Pull request open')
    await overview(name === firstRepoID ? 'PR opened 1/2' : 'Cleaned up 1/2')

    await page.reload()
    repo = card(name)
    await expectText(repo, 'Pull request open')
    await repo.getByRole('link', { name: 'Open pull request', exact: true }).waitFor()
    await previewAndConfirm(repo, 'Merge pull request', async () => {
      await repo.getByRole('group', { name: 'Delivery preview', exact: true }).getByText('Merge pull request', { exact: true }).waitFor()
      await expectPreviewRow(repo, 'GitHub repository', `browser/${branch.replaceAll('/', '-')}`)
      await expectPreviewRow(repo, 'Merge method', 'merge')
      await expectPreviewRow(repo, 'Pull request number', '#1')
    })
    await expectText(repo, 'Merged')
    await overview(name === firstRepoID ? 'Merged 1/2' : 'Cleaned up 1/2')
    if (!existsSync(name === firstRepoID ? firstWorktree : secondWorktree)) throw new Error('Merge prematurely removed Task worktree')
    await previewAndConfirm(repo, 'Clean up task worktree', async () => {
      await repo.getByRole('group', { name: 'Delivery preview', exact: true }).getByText('Clean up task worktree', { exact: true }).waitFor()
      const worktree = name === firstRepoID ? firstWorktree : secondWorktree
      if (!/\/task-workspaces\/[0-9a-f]{12}\/[0-9a-f]{12}$/.test(worktree)) throw new Error(`Worktree path is not short: ${worktree}`)
      await expectPreviewRow(repo, 'Task worktree', worktree)
    })
    await expectText(repo, 'Task worktree cleaned up')
    await overview(name === firstRepoID ? 'Cleaned up 1/2' : 'Task worktree cleaned up')
  }

  await page.goto(baseURL + route)
  await page.getByRole('heading', { name: 'Repository results', exact: true }).waitFor()
  await page.getByRole('article', { name: firstRepoID, exact: true }).waitFor()
  await page.getByRole('article', { name: secondRepoID, exact: true }).waitFor()
  if (await page.getByRole('button', { name: /Commit|Push|pull request|Merge|Clean up|Apply/i }).count()) {
    throw new Error('delivery or original-checkout Apply action is visible before Review')
  }
  if (deliveryMutations.length) throw new Error('delivery mutation occurred before Review')

  await page.getByRole('button', { name: 'Accept', exact: true }).click()
  await page.getByRole('heading', { name: 'Task branch delivery', exact: true }).waitFor()
  await expectText(card(firstRepoID), 'Ready to commit')
  await expectText(card(secondRepoID), 'Ready to commit')
  await overview('Ready to commit')
  await expect(page.locator('.task-progress-steps > li')).toHaveCount(9)
  if (await page.getByRole('button', { name: /Apply/i }).count()) throw new Error('original-checkout Apply is exposed after Review')

  const secondHead = execFileSync('git', ['-C', secondWorktree, 'rev-parse', 'HEAD'], { encoding: 'utf8' })
  const secondContent = readFileSync(`${secondWorktree}/same.txt`, 'utf8')
  await deliver(firstRepoID, true)
  await expect(page.locator('.task-progress [aria-current="step"]')).toHaveCount(1)
  await expect(page.locator('.task-progress [aria-current="step"]')).toHaveClass(/stage-commit/)
  if (!existsSync(secondWorktree) || existsSync(firstWorktree)) throw new Error('Cleanup removed the wrong worktree')
  if (execFileSync('git', ['-C', secondWorktree, 'rev-parse', 'HEAD'], { encoding: 'utf8' }) !== secondHead || readFileSync(`${secondWorktree}/same.txt`, 'utf8') !== secondContent) throw new Error('First repository delivery altered the second repository')
  await expectText(card(secondRepoID), 'Ready to commit')
  if (await card(secondRepoID).getByRole('button', { name: 'Preview Commit task branch', exact: true }).count() !== 1) {
    throw new Error('first repository delivery advanced the second repository')
  }
  await page.reload()
  await expectText(card(firstRepoID), 'Task worktree cleaned up')
  await expectText(card(secondRepoID), 'Ready to commit')

  await deliver(secondRepoID)
  await page.reload()
  await expectText(card(firstRepoID), 'Task worktree cleaned up')
  await expectText(card(secondRepoID), 'Task worktree cleaned up')
  await overview('Task worktree cleaned up')
  await expect(page.locator('.task-progress-steps > li.is-done')).toHaveCount(8)
  await expect(page.locator('.stage-checks')).toHaveText(/Not run \/ Unverified/)
  await expect(page.getByRole('status').filter({ hasText: 'Task worktrees have been cleaned up. Review and delivery history remain available.' })).toBeVisible()
  await expect(page.getByText(/Task resource workspace is not proven|managed worktree is unavailable/)).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'External tools' }).getByRole('button')).toHaveCount(0)
  if (await page.getByRole('button', { name: /Apply/i }).count()) throw new Error('original-checkout Apply became available after delivery')

  const screenshotDir = process.env.CHORA_PROGRESS_SCREENSHOTS
  if (screenshotDir) mkdirSync(screenshotDir, { recursive: true })
  for (const width of [1440, 1024, 390]) {
    await page.setViewportSize({ width, height: 1000 })
    const progress = page.getByRole('region', { name: 'Task progress', exact: true })
    await progress.scrollIntoViewIfNeeded()
    const layout = await progress.evaluate((element) => {
      const box = element.getBoundingClientRect()
      return { left: box.left, right: box.right, width: box.width, overflow: element.scrollWidth > element.clientWidth,
        steps: [...element.querySelectorAll('.workflow-step')].map((step) => { const b = step.getBoundingClientRect(); return { left: b.left, right: b.right } }) }
    })
    if (layout.left < 0 || layout.right > width + 1 || layout.width > 1121 || layout.overflow || layout.steps.some((step) => step.left < layout.left || step.right > layout.right)) throw new Error(`Workflow overflows at ${width}: ${JSON.stringify(layout)}`)
    if (screenshotDir) await progress.screenshot({ path: `${screenshotDir}/workflow-${width}.png` })
  }
  await page.evaluate(() => localStorage.setItem('chora.locale', 'zh-CN'))
  await page.reload()
  await overview('Task 工作树已清理')
  const chinese = page.getByRole('region', { name: '任务进度', exact: true })
  await expect(chinese.getByText('准备', { exact: true })).toBeVisible()
  if (screenshotDir) {
    await chinese.screenshot({ path: `${screenshotDir}/workflow-390-zh.png` })
    await page.setViewportSize({ width: 1440, height: 1000 })
    await chinese.screenshot({ path: `${screenshotDir}/workflow-1440-zh.png` })
  }
  await page.goto(baseURL + route.split('/tasks/')[0])
  await expect(page.locator('.task-card-main .task-state')).toHaveText('Task 工作树已清理')

  process.stdout.write('TASK_BRANCH_FULL_DELIVERY_BROWSER_PASS\n')
} finally {
  await browser.close()
}
