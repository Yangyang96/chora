import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import { NativeCapabilityStatus } from './NativeCapabilityStatus'

function response(body: unknown, ok = true, status = 200): Response {
  return { ok, status, json: async () => body } as Response
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

const emptyStatus = { observations: [] }

describe('NativeCapabilityStatus', () => {
  afterEach(() => vi.unstubAllGlobals())

  test('keeps configured discovery distinct from execution connection evidence', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({
      inventory: {
        skills: [{ name: 'planning', path: '/skills/planning', source: 'project', enabled: true, status: 'discovered' }],
        servers: [{ name: 'tickets', source: 'user', enabled: true, status: 'not-connected' }],
        bridge: { status: 'discovered', version: '2.34.0' }, missingPackages: 0, diagnostics: [],
      },
      observations: [{
        attemptID: 'attempt-current', configVersion: 4, observedAt: '2026-09-20T01:02:03Z', running: false,
        skills: [{ name: 'planning-run', path: '/skills/planning', source: 'project', enabled: true, status: 'available' }],
        servers: [{ name: 'tickets-run', source: 'user', enabled: true, status: 'connected', toolCount: 3 }],
      }],
    })))
    render(<LanguageProvider><NativeCapabilityStatus projectId="project-one" configVersion={4} /></LanguageProvider>)

    const configuredSkill = (await screen.findByText('planning')).closest('li')!
    const configuredServer = screen.getByText('tickets').closest('li')!
    expect(configuredSkill).toHaveTextContent('discovered')
    expect(configuredServer).toHaveTextContent('not-connected')
    expect(screen.getByText(/MCP bridge/)).toHaveTextContent('discovered 2.34.0')

    await userEvent.click(screen.getByText(/attempt-current/))
    expect(screen.getByText('planning-run').closest('li')).toHaveTextContent('available')
    expect(screen.getByText('tickets-run').closest('li')).toHaveTextContent('connected')
    expect(screen.getByText('Discovery reads configuration only. Discovered or cached capabilities are not verified connections. Execution observations describe that execution only.')).toBeVisible()
  })

  test('shows actionable warnings for failed and authentication-needed capabilities', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({
      inventory: {
        skills: [],
        servers: [
          { name: 'broken-server', source: 'project', enabled: true, status: 'failed' },
          { name: 'private-server', source: 'user', enabled: true, status: 'needs-auth' },
        ],
        bridge: { status: 'discovered', version: '2.34.0' }, missingPackages: 0, diagnostics: [],
      },
      observations: [],
    })))
    render(<LanguageProvider><NativeCapabilityStatus projectId="project-one" configVersion={1} /></LanguageProvider>)

    expect((await screen.findByText('broken-server')).closest('li')).toHaveTextContent('failed')
    expect(screen.getByText('private-server').closest('li')).toHaveTextContent('needs-auth')
    expect(screen.getAllByText('Unavailable. Repair the existing configuration or authentication, then retry verification. Other capabilities can still be used.')).toHaveLength(2)
  })

  test('posts an explicit verification and renders its connection result', async () => {
    const verification = deferred<Response>()
    const calls: Array<{ path: string; method: string; key?: string }> = []
    vi.stubGlobal('fetch', vi.fn((input: string | URL | Request, init?: RequestInit) => {
      const headers = new Headers(init?.headers)
      calls.push({ path: String(input), method: init?.method ?? 'GET', key: headers.get('Idempotency-Key') ?? undefined })
      if (init?.method === 'POST') return verification.promise
      return Promise.resolve(response(emptyStatus))
    }))
    render(<LanguageProvider><NativeCapabilityStatus projectId="project-one" configVersion={2} /></LanguageProvider>)
    await screen.findByRole('button', { name: 'Verify / retry connections' })

    await userEvent.click(screen.getByRole('button', { name: 'Verify / retry connections' }))
    expect(screen.getByRole('button', { name: 'Verifying…' })).toBeDisabled()
    verification.resolve(response({
      attemptID: 'attempt-verification', configVersion: 2, observedAt: '2026-09-20T02:00:00Z', running: false,
      skills: [], servers: [{ name: 'connected-server', source: 'project', enabled: true, status: 'connected', toolCount: 2 }],
    }))

    const result = await screen.findByRole('region', { name: 'Verification result' })
    expect(within(result).getByText('connected-server').closest('li')).toHaveTextContent('connected')
    expect(calls).toHaveLength(2)
    expect(calls[1]).toMatchObject({ path: '/api/projects/project-one/capabilities/verify', method: 'POST' })
    expect(calls[1].key).toMatch(/^verify-capabilities-/)
  })

  test('ignores an in-flight verification response after the Project changes', async () => {
    const staleVerification = deferred<Response>()
    vi.stubGlobal('fetch', vi.fn((_input: string | URL | Request, init?: RequestInit) => {
      if (init?.method === 'POST') return staleVerification.promise
      return Promise.resolve(response(emptyStatus))
    }))
    const view = render(<LanguageProvider><NativeCapabilityStatus projectId="project-one" configVersion={1} /></LanguageProvider>)
    await screen.findByRole('button', { name: 'Verify / retry connections' })
    await userEvent.click(screen.getByRole('button', { name: 'Verify / retry connections' }))

    view.rerender(<LanguageProvider><NativeCapabilityStatus projectId="project-two" configVersion={0} /></LanguageProvider>)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Verify / retry connections' })).toBeEnabled())
    staleVerification.resolve(response({
      attemptID: 'attempt-stale', configVersion: 1, observedAt: '2026-09-20T03:00:00Z', running: false,
      skills: [], servers: [{ name: 'stale-server', source: 'project', enabled: true, status: 'connected' }],
    }))

    await waitFor(() => expect(screen.queryByText('stale-server')).not.toBeInTheDocument())
    expect(screen.queryByRole('region', { name: 'Verification result' })).not.toBeInTheDocument()
  })
})
