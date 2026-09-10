import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { PiInstallationView } from './PiInstallation'
import { PiInstallation } from './PiInstallation'

function response(body: unknown, ok = true, status = ok ? 200 : 500): Response {
  return { ok, status, json: async () => body } as Response
}

function state(code: PiInstallationView['state'] extends infer _ ? NonNullable<PiInstallationView['state']>['code'] : never, overrides: Partial<NonNullable<PiInstallationView['state']>> = {}): NonNullable<PiInstallationView['state']> {
  return { code, stateVersion: 4, destinationPath: '/private/chora/pi', selectionPresent: code === 'installed', configured: false, updatedAt: '2026-09-09T00:00:00Z', ...overrides }
}

function installation(code: NonNullable<PiInstallationView['state']>['code'], overrides: Partial<PiInstallationView> = {}, stateOverrides: Partial<NonNullable<PiInstallationView['state']>> = {}): PiInstallationView {
  return { available: true, state: state(code, stateOverrides), restartRequired: false, active: false, ...overrides }
}

describe('PiInstallation', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    window.localStorage.removeItem('chora.locale')
  })

  test('never installs on mount and shows fixed metadata before the explicit action', async () => {
    const fetchMock = vi.fn(async (_path: string | URL | Request, _init?: RequestInit) => response(installation('missing', {}, { source: 'chora' })))
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><PiInstallation /></LanguageProvider>)

    expect(await screen.findByText('Pi is not installed.')).toBeInTheDocument()
    expect(screen.getByText('0.85.1')).toBeInTheDocument()
    expect(screen.getByText('chora')).toBeInTheDocument()
    expect(screen.getByText('/private/chora/pi')).toBeInTheDocument()
    expect(screen.getByText('pi')).toBeInTheDocument()
    expect(screen.getByText(/does not collect credentials/)).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][1]?.method).toBeUndefined()
  })

  test('reuses a discovered compatible Pi without offering a replacement', async () => {
    const fetchMock = vi.fn(async () => response(installation('missing')))
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><PiInstallation existing={{state:'ready',executablePath:'/existing/pi',version:'0.85.1',readyProviders:['deepseek'],notReadyProviders:[]}} /></LanguageProvider>)
    expect(await screen.findByText('Using your existing compatible Pi installation.')).toBeInTheDocument()
    expect(screen.queryByRole('button',{name:'Install fixed Pi version'})).not.toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  test('sends only expectedStateVersion with request protection', async () => {
    const fetchMock = vi.fn(async (_path: string | URL | Request, init?: RequestInit) => response(init?.method === 'POST'
      ? installation('installed', { restartRequired: true }, { stateVersion: 6, selectionPresent: true })
      : installation('missing', {}, { stateVersion: 5 })))
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><PiInstallation /></LanguageProvider>)
    await screen.findByText('Pi is not installed.')
    await userEvent.click(screen.getByRole('button', { name: 'Install fixed Pi version' }))

    await screen.findByText('Pi is installed. Restart Chora before using this installation.')
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')
    expect(post?.[0]).toBe('/api/pi/installation')
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ expectedStateVersion: 5 })
    expect(post?.[1]?.headers).toEqual(expect.objectContaining({ 'Idempotency-Key': expect.stringMatching(/^pi-install-/) }))
  })

  test('polls during the long install and cancels the observed operation', async () => {
    let poll: TimerHandler | undefined
    const nativeSetInterval = window.setInterval.bind(window)
    vi.spyOn(window, 'setInterval').mockImplementation((handler: TimerHandler, delay?: number) => {
      if (delay === 2000) {
        poll = handler
        return 9
      }
      return nativeSetInterval(handler, delay)
    })
    let finishInstall: ((value: Response) => void) | undefined
    const installPending = new Promise<Response>((resolve) => { finishInstall = resolve })
    let gets = 0
    const fetchMock = vi.fn(async (path: string | URL | Request, init?: RequestInit) => {
      if (init?.method === 'POST' && String(path).endsWith('/cancel')) return response(installation('cancelled', {}, { stateVersion: 7 }))
      if (init?.method === 'POST') return installPending
      gets += 1
      return response(gets === 1 ? installation('missing') : installation('installing', {}, { stateVersion: 5, operationId: 'operation_12345678' }))
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><PiInstallation /></LanguageProvider>)
    await screen.findByText('Pi is not installed.')
    await userEvent.click(screen.getByRole('button', { name: 'Install fixed Pi version' }))
    expect(poll).toBeTypeOf('function')
    ;(poll as () => void)()
    await screen.findByText('Installing the fixed Pi package…')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel installation' }))
    await screen.findByText('Pi installation was cancelled.')
    const cancel = fetchMock.mock.calls.find(([path, init]) => init?.method === 'POST' && String(path).endsWith('/cancel'))
    expect(JSON.parse(String(cancel?.[1]?.body))).toEqual({ operationId: 'operation_12345678' })
    expect(cancel?.[1]?.headers).toEqual(expect.objectContaining({ 'Idempotency-Key': expect.stringMatching(/^pi-install-cancel-/) }))
    finishInstall?.(response(installation('cancelled', {}, { stateVersion: 7 })))
  })

  test('distinguishes installed restart, active readiness, and native configuration', async () => {
    const views = [
      installation('installed', { restartRequired: true }, { selectionPresent: true, configured: false, configurationAction: 'pi' }),
      installation('installed', { active: true }, { selectionPresent: true, configured: true }),
    ]
    const fetchMock = vi.fn(async () => response(views.shift()))
    vi.stubGlobal('fetch', fetchMock)
    const rendered = render(<LanguageProvider><PiInstallation /></LanguageProvider>)
    await screen.findByText('Pi is installed. Restart Chora before using this installation.')
    expect(screen.getByText('Restart is required. This installation is not active yet.')).toBeInTheDocument()
    expect(screen.getByText('pi')).toBeInTheDocument()
    expect(screen.queryByText('Pi is installed, active, and configured.')).not.toBeInTheDocument()
    rendered.unmount()

    render(<LanguageProvider><PiInstallation /></LanguageProvider>)
    expect(await screen.findByText('Pi is installed, active, and configured.')).toBeInTheDocument()
    expect(screen.queryByText('Restart is required. This installation is not active yet.')).not.toBeInTheDocument()
  })

  test('retries a failed installation from its exact state version', async () => {
    const fetchMock = vi.fn(async (_path: string | URL | Request, init?: RequestInit) => response(init?.method === 'POST'
      ? installation('installed', { restartRequired: true }, { stateVersion: 9, selectionPresent: true })
      : installation('failed', {}, { stateVersion: 8, message: 'installation failed safely' })))
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><PiInstallation /></LanguageProvider>)
    await screen.findByText('Pi installation failed.')
    await userEvent.click(screen.getByRole('button', { name: 'Retry installation' }))
    await screen.findByText('Pi is installed. Restart Chora before using this installation.')
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ expectedStateVersion: 8 })
  })

  test('reloads authoritative state after a stale request', async () => {
    let call = 0
    const fetchMock = vi.fn(async (_path: string | URL | Request, init?: RequestInit) => {
      call += 1
      if (init?.method === 'POST') return response({ error: 'stale state' }, false, 409)
      return response(call === 1 ? installation('failed', {}, { stateVersion: 8, message: 'installation failed safely' }) : installation('cancelled', {}, { stateVersion: 9 }))
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><PiInstallation /></LanguageProvider>)
    await screen.findByText('Pi installation failed.')
    await userEvent.click(screen.getByRole('button', { name: 'Retry installation' }))

    expect(await screen.findByText('Installation state changed. Review the refreshed status and try again.')).toBeInTheDocument()
    expect(screen.getByText('Pi installation was cancelled.')).toBeInTheDocument()
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ expectedStateVersion: 8 })
  })

  test('aborts an in-flight installation when unmounted and does not publish a change', async () => {
    let installSignal: AbortSignal | undefined
    const onChanged = vi.fn()
    const fetchMock = vi.fn(async (_path: string | URL | Request, init?: RequestInit) => {
      if (init?.method === 'POST') {
        installSignal = init.signal as AbortSignal
        return new Promise<Response>(() => {})
      }
      return response(installation('missing'))
    })
    vi.stubGlobal('fetch', fetchMock)
    const rendered = render(<LanguageProvider><PiInstallation onChanged={onChanged} /></LanguageProvider>)
    await screen.findByText('Pi is not installed.')
    await userEvent.click(screen.getByRole('button', { name: 'Install fixed Pi version' }))
    await waitFor(() => expect(installSignal).toBeDefined())
    rendered.unmount()
    expect(installSignal?.aborted).toBe(true)
    expect(onChanged).not.toHaveBeenCalled()
  })
})
