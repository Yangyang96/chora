import { expect, test } from '@playwright/test'

// The API regression covers readiness detection; this contract test covers the
// user's configuration -> restart -> ready guidance in the actual app shell.
test('guides model configuration before asking for a restart', async ({ page }) => {
  let stage = 'unconfigured'
  const command = "'/tmp/chora-pi/bin/pi'"
  await page.route('**/api/pi/discovery', route => route.fulfill({ json: {
    state: stage, readyProviders: stage === 'ready' ? ['example'] : [], notReadyProviders: [],
    configurationAction: stage === 'unconfigured' ? command : undefined,
  } }))
  await page.route('**/api/pi/installation', route => route.fulfill({ json: {
    available: true, active: stage === 'ready', restartRequired: stage === 'restart_required',
    state: { code: 'installed', stateVersion: 1, selectionPresent: true,
      configured: stage !== 'unconfigured', configurationAction: command,
      destinationPath: '/tmp/chora-pi', updatedAt: '2026-09-23T00:00:00Z' },
  } }))
  await page.goto('/')
  await page.locator('.local-pi-status summary').click()
  const status = page.locator('.local-pi-status')
  await expect(status).toContainText('Pi is not configured')
  await expect(status).toContainText(command)
  await expect(status).toContainText('/login')
  await expect(status).toContainText('/model')
  await expect(status).not.toContainText('Restart Chora to activate Pi')
  await expect(page.getByRole('region', { name: 'Pi installation', exact: true })).toContainText('Configure your model before starting a task.')
  await page.getByRole('button', { name: '中文', exact: true }).click()
  await expect(status).toContainText('在终端运行以下命令以配置模型')
  await page.getByRole('button', { name: 'EN', exact: true }).click()
  stage = 'restart_required'
  await page.reload()
  await expect(page.locator('.local-pi-status summary')).toContainText('Restart Chora to activate Pi')
  stage = 'ready'
  await page.reload()
  await expect(page.locator('.local-pi-status summary')).toContainText('Pi ready')
})
