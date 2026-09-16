import { chromium, expect } from '@playwright/test'

const [baseURL, route, configuredPortValue] = process.argv.slice(2)
const configuredPort = Number(configuredPortValue)
if (!baseURL || !route || !Number.isInteger(configuredPort) || configuredPort < 1 || configuredPort > 65535) {
  throw new Error('usage: app-preview-browser.mjs BASE_URL ROUTE PORT')
}

async function expectUnreachable(url) {
  const deadline = Date.now() + 5000
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(500) })
      await response.body?.cancel()
    } catch { return }
    await new Promise(resolve => setTimeout(resolve, 50))
  }
  throw new Error(`stopped preview remains reachable at ${url}`)
}

const browser = await chromium.launch({ headless: true })
const context = await browser.newContext()
try {
  const page = await context.newPage()
  const previewRequests = []
  page.on('request', request => {
    if (new URL(request.url()).pathname.includes('/app-preview')) {
      previewRequests.push({ method: request.method(), url: request.url(), body: request.postDataJSON() })
    }
  })
  page.on('response', async response => {
    if (response.url().includes('/app-preview') && response.status() >= 400) {
      console.error('APP_PREVIEW_HTTP_ERROR', response.status(), await response.text())
    }
  })

  await page.goto(baseURL + route)
  await expect(page.getByRole('heading', { name: 'Repository results', exact: true })).toBeVisible()
  await expect(page.locator('.stream-head-details > strong').first()).toHaveText('Awaiting review')
  const summary = page.getByText('App preview (optional)', { exact: true })
  await expect(summary).toBeVisible()
  await page.waitForTimeout(250)
  if (previewRequests.length !== 0) throw new Error('collapsed app preview issued a network request')

  await summary.click()
  const panel = page.locator('details.app-preview')
  await expect(panel).toHaveAttribute('open', '')
  await expect(panel.getByLabel('Command', { exact: true })).toBeVisible()
  if (previewRequests.length !== 1 || previewRequests[0].method !== 'GET') {
    throw new Error(`expansion did not issue exactly one read: ${JSON.stringify(previewRequests)}`)
  }

  await panel.getByLabel('Command', { exact: true }).fill('node preview.cjs')
  await panel.getByLabel('Working directory (relative)', { exact: true }).fill('.')
  await panel.getByLabel('App port', { exact: true }).fill(String(configuredPort))
  await panel.getByRole('button', { name: 'Save configuration', exact: true }).click()
  await expect(panel.getByRole('status')).toContainText('idle')
  const mutationsAfterSave = previewRequests.filter(request => request.method === 'POST')
  if (mutationsAfterSave.length !== 1 || !new URL(mutationsAfterSave[0].url).pathname.endsWith('/app-preview/save')) {
    throw new Error(`save performed an unexpected operation: ${JSON.stringify(mutationsAfterSave)}`)
  }
  if (mutationsAfterSave[0].body?.config?.command !== 'node preview.cjs' || mutationsAfterSave[0].body?.config?.workingDirectory !== '.' || mutationsAfterSave[0].body?.config?.port !== configuredPort) {
    throw new Error(`saved configuration drifted: ${JSON.stringify(mutationsAfterSave[0].body)}`)
  }
  await expectUnreachable(`http://127.0.0.1:${configuredPort}/`)

  await panel.getByRole('button', { name: 'Start preview', exact: true }).click()
  await expect(panel.getByRole('status')).toContainText('running', { timeout: 10000 })
  const startRequest = previewRequests.find(request => request.method === 'POST' && new URL(request.url).pathname.endsWith('/app-preview/start'))
  if (!startRequest) throw new Error('explicit Start preview did not issue a start request')
  const previewLink = panel.getByRole('link', { name: 'Open app preview', exact: true })
  await expect(previewLink).toBeVisible()
  const previewURL = await previewLink.getAttribute('href')
  if (!previewURL) throw new Error('running preview omitted its loopback URL')
  const parsedPreviewURL = new URL(previewURL)
  if (parsedPreviewURL.protocol !== 'http:' || !['127.0.0.1', 'localhost'].includes(parsedPreviewURL.hostname) || !parsedPreviewURL.port) {
    throw new Error(`preview exposed a non-loopback URL: ${previewURL}`)
  }

  const opened = context.waitForEvent('page')
  await previewLink.click()
  const previewPage = await opened
  await previewPage.waitForLoadState('domcontentloaded')
  await expect(previewPage.locator('body')).toHaveText('Task preview content')
  await previewPage.close()

  await page.goto(baseURL)
  await page.goBack()
  await expect(page.getByRole('heading', { name: 'Repository results', exact: true })).toBeVisible()
  const resumedSummary = page.getByText('App preview (optional)', { exact: true })
  await resumedSummary.click()
  const resumedPanel = page.locator('details.app-preview')
  await expect(resumedPanel.getByRole('status')).toContainText('running', { timeout: 10000 })
  await expect(resumedPanel.getByLabel('Preview logs')).toContainText('preview fixture ready', { timeout: 10000 })

  await resumedPanel.getByRole('button', { name: 'Stop preview', exact: true }).click()
  await expect(resumedPanel.getByRole('status')).toContainText('stopped', { timeout: 10000 })
  await expectUnreachable(previewURL)
  await resumedPanel.getByRole('button', { name: 'Clean up preview', exact: true }).click()
  await expect(resumedPanel.getByRole('status')).toContainText('idle')
  await expect(resumedPanel.getByLabel('Command', { exact: true })).toHaveValue('node preview.cjs')
  await expect(resumedPanel.getByLabel('Working directory (relative)', { exact: true })).toHaveValue('.')
  await expect(resumedPanel.getByLabel('App port', { exact: true })).toHaveValue(String(configuredPort))

  await expect(page.locator('.stream-head-details > strong').first()).toHaveText('Awaiting review')
  await expect(page.getByRole('button', { name: 'Accept', exact: true })).toBeVisible()
  process.stdout.write('APP_PREVIEW_BROWSER_PASS\n')
} finally {
  await browser.close()
}
