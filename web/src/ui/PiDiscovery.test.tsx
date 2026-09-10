import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { PiDiscoveryView } from '../types'
import { PiDiscovery } from './PiDiscovery'

function response(body: unknown, ok = true, status = 200): Response {
  return { ok, status, json: async () => body } as Response
}

function readyDiscovery(): PiDiscoveryView {
  return {
    state: 'ready',
    executablePath: '/Users/demo/.local/bin/pi',
    version: '0.84.2',
    executableSha256: 'a'.repeat(64),
    readyProviders: ['ollama'],
    notReadyProviders: ['google'],
  }
}

describe('PiDiscovery', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    window.localStorage.removeItem('chora.locale')
  })

  test('fetches /api/pi/discovery and renders a ready Pi with version and providers', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
      expect(String(input)).toBe('/api/pi/discovery')
      return response(readyDiscovery())
    }))

    render(<LanguageProvider><PiDiscovery /></LanguageProvider>)

    expect(screen.getByRole('status')).toHaveTextContent('Checking Local Connected…')
    expect(await screen.findByText('Pi ready')).toBeInTheDocument()
    expect(screen.getByText('Version 0.84.2')).toBeInTheDocument()
    expect(screen.getByText(/ollama/)).toBeInTheDocument()
  })

  test('renders the fixed message for unavailable servers', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ state: 'unavailable', reason: 'Local Connected Pi discovery is not configured', readyProviders: [], notReadyProviders: [] })))

    render(<LanguageProvider><PiDiscovery /></LanguageProvider>)

    expect(await screen.findByText('Local Connected is not available on this server')).toBeInTheDocument()
  })

  test.each<[PiDiscoveryView['state'], string, string]>([
    ['missing', 'pi was not found on PATH', 'Pi not found'],
    ['not_executable', 'Pi path is not an executable regular file', 'Pi is not executable'],
    ['incompatible_version', 'Pi version 0.83.0 is below the required 0.84.2', 'Pi version is incompatible'],
    ['unconfigured', 'no configured Pi provider answered ready; run `pi auth` to configure one', 'Pi is not configured'],
  ])('renders %s state with its reason verbatim', async (state, reason, label) => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ state, reason, readyProviders: [], notReadyProviders: [] })))

    render(<LanguageProvider><PiDiscovery /></LanguageProvider>)

    expect(await screen.findByText(label)).toBeInTheDocument()
    expect(screen.getByText(reason)).toBeInTheDocument()
  })

  test('renders the error state and retries on refresh', async () => {
    let calls = 0
    vi.stubGlobal('fetch', vi.fn(async () => {
      calls += 1
      if (calls === 1) return response({ error: 'discovery unavailable' }, false, 503)
      return response(readyDiscovery())
    }))

    render(<LanguageProvider><PiDiscovery /></LanguageProvider>)

    expect(await screen.findByText('Local Connected check failed')).toBeInTheDocument()
    expect(screen.getByText('discovery unavailable')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    expect(await screen.findByText('Pi ready')).toBeInTheDocument()
  })
})
