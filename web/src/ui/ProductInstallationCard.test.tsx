import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'
import type { ProductInstallationDoctor } from '../types'
import { ProductInstallationCard } from './ProductInstallationCard'
import { parseProductInstallationDoctor } from './productInstallationContract'

const baseDoctor: ProductInstallationDoctor = {
  schemaVersion: 'chora.product-installation-status/v1',
  status: 'blocked',
  reasonCode: 'authority_required',
  engine: { ready: false, apiVersion: '', operatingSystem: '', architecture: '', contextName: '' },
  activeGenerationId: '',
  candidateGenerationId: '',
  actions: { setup: true, upgrade: true, gc: true, uninstall: true },
  replayed: false,
  restartRequired: false,
}

function response(body: unknown, ok = true, status = ok ? 200 : 500): Response {
  return { ok, status, json: async () => body } as Response
}

function installLifecycleFetch(postResult: ProductInstallationDoctor = baseDoctor, refreshed: ProductInstallationDoctor = baseDoctor) {
  const fetchMock = vi.fn(async (_input: string | URL | Request, init?: RequestInit) => {
    if (init?.method === 'POST') return response(postResult)
    return response(fetchMock.mock.calls.some(([, request]) => request?.method === 'POST') ? refreshed : baseDoctor)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function postCalls(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')
}

describe('ProductInstallationCard', () => {
  test('requires both visible setup confirmations and sends only the frozen setup body', async () => {
    const restarted = { ...baseDoctor, status: 'completed' as const, reasonCode: '' as const, restartRequired: true }
    const fetchMock = installLifecycleFetch(restarted, restarted)
    render(<ProductInstallationCard />)
    await screen.findByText('Installed product Doctor')

    await userEvent.click(screen.getByRole('button', { name: 'Setup' }))
    const dialog = screen.getByRole('dialog', { name: 'Setup installed product' })
    expect(within(dialog).getAllByText(/Colima 0\.10\.3 and Docker CLI 29\.6\.1/)).toHaveLength(2)
    expect(within(dialog).getAllByText(/2 CPU, 4 GiB memory, and 20 GiB disk/)).toHaveLength(2)
    const submit = within(dialog).getByRole('button', { name: 'Confirm setup' })
    expect(submit).toBeDisabled()
    const confirmations = within(dialog).getAllByRole('checkbox')
    expect(confirmations).toHaveLength(2)
    expect(confirmations[0]).not.toBeChecked()
    expect(confirmations[1]).not.toBeChecked()
    await userEvent.click(confirmations[0])
    expect(submit).toBeDisabled()
    await userEvent.click(confirmations[1])
    await userEvent.click(submit)

    await waitFor(() => expect(postCalls(fetchMock)).toHaveLength(1))
    const [path, init] = postCalls(fetchMock)[0]
    expect(path).toBe('/api/product-installation/setup')
    expect(JSON.parse(String(init?.body))).toEqual({ confirmation: 'setup', confirmPinnedEngineTools: true, confirmColimaVM: true })
    expect(init?.headers).toEqual(expect.objectContaining({ 'Idempotency-Key': expect.stringMatching(/^product-setup-/) }))
    expect(await screen.findByText('Action completed. Restart Chora to use the active installation.')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([path]) => path === '/api/product-installation/doctor')).toHaveLength(2)
  })

  test.each([
    ['Upgrade', 'Confirm upgrade', 'upgrade', { confirmation: 'upgrade' }],
    ['Bounded GC', 'Confirm bounded GC', 'gc', { confirmation: 'gc', limit: 8 }],
    ['Uninstall', 'Confirm uninstall', 'uninstall', { confirmation: 'uninstall' }],
  ] as const)('sends only the frozen %s command body and an idempotency key', async (openLabel, confirmLabel, action, expectedBody) => {
    const fetchMock = installLifecycleFetch()
    render(<ProductInstallationCard />)
    await screen.findByText('Installed product Doctor')

    await userEvent.click(screen.getByRole('button', { name: openLabel }))
    const dialog = screen.getByRole('dialog')
    await userEvent.click(within(dialog).getByRole('checkbox'))
    await userEvent.click(within(dialog).getByRole('button', { name: confirmLabel }))
    await waitFor(() => expect(postCalls(fetchMock)).toHaveLength(1))

    const [path, init] = postCalls(fetchMock)[0]
    expect(path).toBe(`/api/product-installation/${action}`)
    expect(JSON.parse(String(init?.body))).toEqual(expectedBody)
    expect(init?.headers).toEqual(expect.objectContaining({ 'Idempotency-Key': expect.stringMatching(new RegExp(`^product-${action}-`)) }))
  })

  test('warns that an interrupted upgrade rolls candidate activation back and preserves the active generation', async () => {
    installLifecycleFetch()
    render(<ProductInstallationCard />)
    await screen.findByText('Installed product Doctor')
    await userEvent.click(screen.getByRole('button', { name: 'Upgrade' }))
    expect(screen.getByText('Upgrade stages and validates a candidate generation before activation. If interrupted or validation fails, candidate activation is rolled back and the current active generation is preserved.')).toBeInTheDocument()
  })

  test('preserves replay and restart results when the following Doctor refresh clears transient flags', async () => {
    const replayed = { ...baseDoctor, status: 'completed' as const, reasonCode: '' as const, replayed: true, restartRequired: true }
    const refreshed = { ...replayed, replayed: false, restartRequired: false }
    installLifecycleFetch(replayed, refreshed)
    render(<ProductInstallationCard />)
    await screen.findByText('Installed product Doctor')

    await userEvent.click(screen.getByRole('button', { name: 'Upgrade' }))
    await userEvent.click(screen.getByRole('checkbox'))
    await userEvent.click(screen.getByRole('button', { name: 'Confirm upgrade' }))

    expect(await screen.findByText('The last lifecycle command was safely replayed.')).toBeInTheDocument()
    expect(screen.getByText('Restart Chora to use the active installation.')).toBeInTheDocument()
  })

  test('does not label a first lifecycle execution as replayed', async () => {
    const first = { ...baseDoctor, status: 'completed' as const, reasonCode: '' as const, replayed: false, restartRequired: true }
    installLifecycleFetch(first, { ...first, restartRequired: false })
    render(<ProductInstallationCard />)
    await screen.findByText('Installed product Doctor')

    await userEvent.click(screen.getByRole('button', { name: 'Upgrade' }))
    await userEvent.click(screen.getByRole('checkbox'))
    await userEvent.click(screen.getByRole('button', { name: 'Confirm upgrade' }))

    await screen.findByText('Restart Chora to use the active installation.')
    expect(screen.queryByText('The last lifecycle command was safely replayed.')).not.toBeInTheDocument()
  })

  test('states that the bounded GC limit counts asset identities and sends only that limit', async () => {
    const fetchMock = installLifecycleFetch()
    render(<ProductInstallationCard />)
    await screen.findByText('Installed product Doctor')
    await userEvent.click(screen.getByRole('button', { name: 'Bounded GC' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText('Bounded garbage collection removes at most the selected number of eligible inactive Chora asset identities. Active, candidate, referenced, and unrelated Engine assets are excluded.')).toBeInTheDocument()
    const limit = within(dialog).getByRole('spinbutton', { name: 'Maximum asset identities' })
    await userEvent.clear(limit)
    await userEvent.type(limit, '64')
    await userEvent.click(within(dialog).getByRole('checkbox'))
    await userEvent.click(within(dialog).getByRole('button', { name: 'Confirm bounded GC' }))
    await waitFor(() => expect(postCalls(fetchMock)).toHaveLength(1))
    expect(JSON.parse(String(postCalls(fetchMock)[0][1]?.body))).toEqual({ confirmation: 'gc', limit: 64 })
  })

  test.each(['Setup', 'Upgrade', 'Bounded GC', 'Uninstall'])('cancelling %s sends no command', async (action) => {
    const fetchMock = installLifecycleFetch()
    render(<ProductInstallationCard />)
    await screen.findByText('Installed product Doctor')
    await userEvent.click(screen.getByRole('button', { name: action }))
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(postCalls(fetchMock)).toHaveLength(0)
  })

  test('states the exact uninstall preservation boundary', async () => {
    installLifecycleFetch()
    render(<ProductInstallationCard />)
    await screen.findByText('Installed product Doctor')
    await userEvent.click(screen.getByRole('button', { name: 'Uninstall' }))
    expect(screen.getByText('Uninstall removes only Chora-owned product assets. User Pi and PATH Pi, worktrees, evidence, and unrelated Engine assets are preserved.')).toBeInTheDocument()
  })

  test('fails closed for aliases, extra secret fields, malformed views, and secret-bearing errors', async () => {
    const hostile = {
      ...baseDoctor,
      engine: { ready: true, apiVersion: '1.48', os: 'linux', architecture: 'arm64', contextName: 'colima' },
      allowedActions: ['setup'],
      credentials: '/Users/alice/.config/pi/token-secret',
    }
    vi.stubGlobal('fetch', vi.fn(async () => response(hostile)))
    const first = render(<ProductInstallationCard />)
    await screen.findByText('Product installation status is unavailable. Actions are disabled.')
    expect(screen.queryByText(/token-secret|\/Users\/alice/)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Setup' })).not.toBeInTheDocument()
    first.unmount()

    vi.stubGlobal('fetch', vi.fn(async () => response({ error: 'credential /Users/alice/.ssh/id_ed25519' }, false, 500)))
    render(<ProductInstallationCard />)
    await screen.findByText('Product installation status is unavailable. Actions are disabled.')
    expect(screen.queryByText(/id_ed25519|credential \/Users/)).not.toBeInTheDocument()
  })

  test('does not render a secret-bearing lifecycle command error', async () => {
    const fetchMock = vi.fn(async (_input: string | URL | Request, init?: RequestInit) => (
      init?.method === 'POST'
        ? response({ error: 'failed with credential /Users/alice/.docker/config.json and grant host:*' }, false, 500)
        : response(baseDoctor)
    ))
    vi.stubGlobal('fetch', fetchMock)
    render(<ProductInstallationCard />)
    await screen.findByText('Installed product Doctor')
    await userEvent.click(screen.getByRole('button', { name: 'Upgrade' }))
    await userEvent.click(screen.getByRole('checkbox'))
    await userEvent.click(screen.getByRole('button', { name: 'Confirm upgrade' }))
    await screen.findByText('The product installation action could not be completed safely.')
    expect(screen.queryByText(/\/Users\/alice|config\.json|host:\*/)).not.toBeInTheDocument()
  })

  test('rejects digests, paths, and unexpected public fields without projecting them', () => {
    expect(parseProductInstallationDoctor({ ...baseDoctor, activeGenerationId: 'a'.repeat(64) })).toBeNull()
    expect(parseProductInstallationDoctor({ ...baseDoctor, candidateGenerationId: '/private/install/g2' })).toBeNull()
    expect(parseProductInstallationDoctor({ ...baseDoctor, engine: { ...baseDoctor.engine, contextName: 'credential token' } })).toBeNull()
    expect(parseProductInstallationDoctor({ ...baseDoctor, grants: ['filesystem'] })).toBeNull()
  })

  test('requires a complete exact Engine identity whenever Engine is ready', () => {
    const ready = {
      ...baseDoctor,
      status: 'ready',
      reasonCode: '',
      engine: { ready: true, apiVersion: '1.47', operatingSystem: 'linux', architecture: 'arm64', contextName: 'colima' },
    }
    expect(parseProductInstallationDoctor(ready)).not.toBeNull()
    expect(parseProductInstallationDoctor({ ...ready, engine: { ...ready.engine, operatingSystem: '' } })).toBeNull()
    const { operatingSystem: _operatingSystem, ...engineWithoutOperatingSystem } = ready.engine
    expect(parseProductInstallationDoctor({ ...ready, engine: { ...engineWithoutOperatingSystem, os: 'linux' } })).toBeNull()
    expect(parseProductInstallationDoctor({ ...ready, engine: { ...ready.engine, endpoint: 'local' } })).toBeNull()
  })
})
