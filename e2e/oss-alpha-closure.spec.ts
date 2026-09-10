import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

const trustedLocalPolicy = 'chora.trusted-local-disclosure.v1'
const fixtureAcknowledgement = '/__e2e/oss-alpha-closure/acknowledgement-state'

test.skip(process.env.CHORA_E2E_SCENARIO !== 'oss-alpha-closure', 'requires the explicit in-memory OSS Alpha Closure fixture')

type LifecycleRequest = {
  action: string
  body: unknown
  idempotencyKey: string
}

test('OSS Alpha public journey is explicit, fail-closed, replay-safe, and executor-free', async ({ page, request }) => {
  const lifecycleRequests: LifecycleRequest[] = []
  const acknowledgementRequests: Array<{ body: unknown; idempotencyKey: string }> = []
  page.on('request', (observed) => {
    const url = new URL(observed.url())
    if (observed.method() !== 'POST') return
    if (url.pathname.startsWith('/api/product-installation/')) {
      lifecycleRequests.push({
        action: url.pathname.slice('/api/product-installation/'.length),
        body: observed.postDataJSON(),
        idempotencyKey: observed.headers()['idempotency-key'] ?? '',
      })
    }
    if (url.pathname === '/api/agent-execution/trusted-local-acknowledgements') {
      acknowledgementRequests.push({
        body: observed.postDataJSON(),
        idempotencyKey: observed.headers()['idempotency-key'] ?? '',
      })
    }
  })

  await setAcknowledgementMode(request, 'missing')
  await page.goto('/')
  await createRoomAndOpenTaskComposer(page)
  await assertSingleStandardProfileSelector(page)
  await proveTrustedLocalFailsClosed(page)

  await setAcknowledgementMode(request, 'stale')
  await page.reload()
  await page.getByRole('button', { name: '＋ New Task' }).first().click()
  await assertSingleStandardProfileSelector(page)
  await proveTrustedLocalFailsClosed(page)

  await page.getByRole('radio', { name: 'Trusted Local · No Sandbox' }).click()
  const disclosure = page.getByRole('dialog', { name: 'Trusted Local · No Sandbox' })
  await expect(disclosure).toContainText(trustedLocalPolicy)
  await disclosure.getByRole('button', { name: 'Acknowledge and use Trusted Local' }).click()
  await expect(page.getByRole('radio', { name: 'Trusted Local · No Sandbox' })).toBeChecked()
  await expect.poll(() => acknowledgementRequests.length).toBe(1)
  expect(acknowledgementRequests[0]).toEqual({
    body: { policyVersion: trustedLocalPolicy },
    idempotencyKey: expect.stringMatching(/^acknowledge-trusted-local-[0-9a-f-]+$/),
  })
  const currentAcknowledgement = await request.get('/api/agent-execution/trusted-local-acknowledgements/current')
  expect(currentAcknowledgement.ok()).toBeTruthy()
  expect(await currentAcknowledgement.json()).toMatchObject({ acknowledged: true, policyVersion: trustedLocalPolicy })

  const initialDoctorResponse = await request.get('/api/product-installation/doctor')
  expect(initialDoctorResponse.ok()).toBeTruthy()
  const initialDoctor = await initialDoctorResponse.json()
  expect(Object.keys(initialDoctor).sort()).toEqual([
    'actions', 'activeGenerationId', 'candidateGenerationId', 'engine', 'reasonCode', 'replayed', 'restartRequired', 'schemaVersion', 'status',
  ].sort())
  expect(Object.keys(initialDoctor.engine).sort()).toEqual(['apiVersion', 'architecture', 'contextName', 'operatingSystem', 'ready'].sort())
  expect(Object.keys(initialDoctor.actions).sort()).toEqual(['gc', 'setup', 'uninstall', 'upgrade'].sort())
  expect(initialDoctor).toMatchObject({
    status: 'blocked', reasonCode: 'authority_required', activeGenerationId: '', candidateGenerationId: '',
    engine: { ready: false }, actions: { setup: true }, replayed: false, restartRequired: false,
  })
  expect(JSON.stringify(initialDoctor)).not.toMatch(/\/Users\/|\/private\/|sha256:|credential|grant|daemon|endpoint|docker (pull|load|build)/i)

  await page.getByText(/^Installed product · Setup needed$/).click()
  const doctor = page.getByRole('region', { name: 'Installed product Doctor' })
  await expect(doctor).toContainText('Active generationnone')
  await doctor.getByRole('button', { name: 'Setup' }).click()
  const setup = page.getByRole('dialog', { name: 'Setup installed product' })
  const setupSubmit = setup.getByRole('button', { name: 'Confirm setup' })
  await expect(setupSubmit).toBeDisabled()
  await setup.getByLabel(/I authorize the pinned Colima 0\.10\.3 and Docker CLI 29\.6\.1/).check()
  await expect(setupSubmit).toBeDisabled()
  await setup.getByLabel(/I confirm the Colima VM boundary/).check()
  await setupSubmit.click()
  await expect(doctor).toContainText('Active generationgeneration-1')
  await expect(doctor.getByText('Restart Chora to use the active installation.', { exact: true })).toBeVisible()

  await doctor.getByRole('button', { name: 'Upgrade' }).click()
  const upgrade = page.getByRole('dialog', { name: 'Upgrade installed product' })
  await upgrade.getByLabel('I confirm this installed-product upgrade.').check()
  await upgrade.getByRole('button', { name: 'Confirm upgrade' }).click()
  await expect(doctor.getByText('The last lifecycle command was safely replayed.')).toHaveCount(0)
  await expect(doctor.getByText('Restart Chora to use the active installation.', { exact: true })).toBeVisible()

  await expect.poll(() => lifecycleRequests.length).toBe(2)
  const firstUpgrade = lifecycleRequests[1]
  expect(firstUpgrade.action).toBe('upgrade')
  const replayResponse = await request.post('/api/product-installation/upgrade', {
    headers: { 'Idempotency-Key': firstUpgrade.idempotencyKey },
    data: firstUpgrade.body,
  })
  expect(replayResponse.ok()).toBeTruthy()
  const replay = await replayResponse.json()
  expect(replay).toMatchObject({ status: 'completed', replayed: true, restartRequired: true })
  expect(Object.keys(replay).sort()).toEqual(Object.keys(initialDoctor).sort())

  await doctor.getByRole('button', { name: 'Bounded GC' }).click()
  const gc = page.getByRole('dialog', { name: 'Run bounded garbage collection' })
  await gc.getByRole('spinbutton', { name: 'Maximum asset identities' }).fill('7')
  await gc.getByLabel('I confirm bounded garbage collection.').check()
  await gc.getByRole('button', { name: 'Confirm bounded GC' }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)

  await expect.poll(() => lifecycleRequests.length).toBe(3)
  const firstGC = lifecycleRequests[2]
  const conflictResponse = await request.post('/api/product-installation/gc', {
    headers: { 'Idempotency-Key': firstGC.idempotencyKey },
    data: { confirmation: 'gc', limit: 8 },
  })
  expect(conflictResponse.status()).toBe(409)
  expect(await conflictResponse.json()).toEqual({ error: 'product installation state conflicts with the request' })

  await doctor.getByRole('button', { name: 'Uninstall' }).click()
  const uninstall = page.getByRole('dialog', { name: 'Uninstall Chora product assets' })
  await expect(uninstall).toContainText('User Pi and PATH Pi, worktrees, evidence, and unrelated Engine assets are preserved.')
  await uninstall.getByLabel('I confirm this ownership-bounded uninstall.').check()
  await uninstall.getByRole('button', { name: 'Confirm uninstall' }).click()
  await expect(doctor).toContainText('Active generationnone')

  await expect.poll(() => lifecycleRequests.length).toBe(4)
  expect(lifecycleRequests).toEqual([
    {
      action: 'setup',
      body: { confirmation: 'setup', confirmPinnedEngineTools: true, confirmColimaVM: true },
      idempotencyKey: expect.stringMatching(/^product-setup-[0-9a-f-]+$/),
    },
    {
      action: 'upgrade', body: { confirmation: 'upgrade' },
      idempotencyKey: expect.stringMatching(/^product-upgrade-[0-9a-f-]+$/),
    },
    {
      action: 'gc', body: { confirmation: 'gc', limit: 7 },
      idempotencyKey: expect.stringMatching(/^product-gc-[0-9a-f-]+$/),
    },
    {
      action: 'uninstall', body: { confirmation: 'uninstall' },
      idempotencyKey: expect.stringMatching(/^product-uninstall-[0-9a-f-]+$/),
    },
  ])

  const auditResponse = await request.get('/__e2e/oss-alpha-closure/audit')
  expect(auditResponse.ok()).toBeTruthy()
  expect(await auditResponse.json()).toEqual({
    scenario: 'oss-alpha-closure',
    externalOperationCount: 0,
    productionExecutorPresent: false,
    mutations: [
      { action: 'setup', outcome: 'first', idempotencySlot: 1, idempotencyKeyPresent: true, confirmPinnedEngineTools: true, confirmColimaVM: true, limit: 0 },
      { action: 'upgrade', outcome: 'first', idempotencySlot: 2, idempotencyKeyPresent: true, confirmPinnedEngineTools: false, confirmColimaVM: false, limit: 0 },
      { action: 'upgrade', outcome: 'replay', idempotencySlot: 2, idempotencyKeyPresent: true, confirmPinnedEngineTools: false, confirmColimaVM: false, limit: 0 },
      { action: 'gc', outcome: 'first', idempotencySlot: 3, idempotencyKeyPresent: true, confirmPinnedEngineTools: false, confirmColimaVM: false, limit: 7 },
      { action: 'gc', outcome: 'conflict', idempotencySlot: 3, idempotencyKeyPresent: true, confirmPinnedEngineTools: false, confirmColimaVM: false, limit: 8 },
      { action: 'uninstall', outcome: 'first', idempotencySlot: 4, idempotencyKeyPresent: true, confirmPinnedEngineTools: false, confirmColimaVM: false, limit: 0 },
    ],
  })
})

async function createRoomAndOpenTaskComposer(page: Page) {
  await page.getByRole('button', { name: 'New Room', exact: true }).click()
  await page.getByLabel('Room name').fill('OSS Alpha Closure')
  await page.getByRole('button', { name: 'Create Room', exact: true }).click()
  await expect(page).toHaveURL(/\/rooms\//)
  await page.getByRole('button', { name: '＋ New Task' }).first().click()
}

async function assertSingleStandardProfileSelector(page: Page) {
  const selector = page.getByRole('group', { name: 'Agent profile' })
  await expect(selector).toHaveCount(1)
  await expect(selector.getByRole('radio')).toHaveCount(3)
  await expect(selector.getByRole('radio', { name: 'Standard' })).toBeChecked()
}

async function proveTrustedLocalFailsClosed(page: Page) {
  await page.getByRole('radio', { name: 'Trusted Local · No Sandbox' }).click()
  const disclosure = page.getByRole('dialog', { name: 'Trusted Local · No Sandbox' })
  await expect(disclosure).toContainText('There is no Sandbox.')
  await disclosure.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.getByRole('radio', { name: 'Standard' })).toBeChecked()
  await expect(page.getByRole('radio', { name: 'Trusted Local · No Sandbox' })).not.toBeChecked()
}

async function setAcknowledgementMode(request: APIRequestContext, mode: 'missing' | 'stale') {
  const response = await request.post(fixtureAcknowledgement, { data: { mode } })
  expect(response.ok()).toBeTruthy()
  expect(await response.json()).toEqual({ mode })
}
