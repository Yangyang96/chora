import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import type { AppPreviewView } from '../appPreviewTypes'
import { LanguageProvider } from '../i18n'
import { AppPreview } from './AppPreview'

function response(body: unknown, ok = true, status = 200): Response {
  return { ok, status, json: async () => body } as Response
}

function preview(overrides: Partial<AppPreviewView> = {}): AppPreviewView {
  return {
    runId: 'run-1', taskId: 'task-1', profile: 'isolated_local', available: true,
    repositories: [{ repoId: 'repo-a', name: 'frontend' }], repoId: 'repo-a',
    preview: { state: 'idle', config: { command: 'npm run dev', workingDirectory: '.', port: 4173 } },
    suggestions: [], ...overrides,
  }
}

describe('AppPreview', () => {
  beforeEach(() => { window.localStorage.clear(); vi.restoreAllMocks() })

  test('is optional and collapsed without issuing a request', async () => {
    const fetcher = vi.fn()
    vi.stubGlobal('fetch', fetcher)
    render(<LanguageProvider><AppPreview runId="run-1" expectedVersion={4} /></LanguageProvider>)
    expect(screen.getByText('App preview (optional)').closest('details')).not.toHaveAttribute('open')
    expect(fetcher).not.toHaveBeenCalled()

    await userEvent.click(screen.getByText('App preview (optional)'))
    expect(fetcher).toHaveBeenCalledOnce()
  })

  test('starts only after the explicit start action with the edited configuration', async () => {
    const calls: Array<{ path: string; init?: RequestInit }> = []
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input); calls.push({ path, init })
      if (path.endsWith('/start')) return response(preview({ preview: { state: 'starting', config: { command: 'npm run demo', workingDirectory: 'web', port: 4321 } } }))
      return response(preview())
    }))
    render(<LanguageProvider><AppPreview runId="run-1" expectedVersion={4} /></LanguageProvider>)
    await userEvent.click(screen.getByText('App preview (optional)'))
    await screen.findByDisplayValue('npm run dev')
    expect(calls).toHaveLength(1)

    const command = screen.getByRole('textbox', { name: 'Command' })
    const directory = screen.getByRole('textbox', { name: 'Working directory (relative)' })
    await userEvent.clear(command); await userEvent.type(command, 'npm run demo')
    await userEvent.clear(directory); await userEvent.type(directory, 'web')
    const port = screen.getByRole('spinbutton', { name: 'App port' })
    await userEvent.clear(port); await userEvent.type(port, '4321')
    expect(calls).toHaveLength(1)
    await userEvent.click(screen.getByRole('button', { name: 'Start preview' }))

    await waitFor(() => expect(calls).toHaveLength(2))
    expect(calls[1].path).toBe('/api/v2/runs/run-1/app-preview/start')
    expect(JSON.parse(String(calls[1].init?.body))).toEqual({ repoId: 'repo-a', expectedVersion: 4, config: { command: 'npm run demo', workingDirectory: 'web', port: 4321 } })
  })

  test('uses discovery when persisted config is empty and accepts a distinct published loopback port', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response(preview({
      preview: { state: 'running', config: { command: '', workingDirectory: '', port: 0 }, url: 'http://127.0.0.1:54321/' },
      suggestions: [{ command: 'node preview.cjs', workingDirectory: '.', port: 4173 }],
    }))))
    render(<LanguageProvider><AppPreview runId="run-1" expectedVersion={4} /></LanguageProvider>)
    await userEvent.click(screen.getByText('App preview (optional)'))
    expect(await screen.findByDisplayValue('node preview.cjs')).toBeInTheDocument()
    expect(screen.getByRole('spinbutton', { name: 'App port' })).toHaveValue(4173)
    expect(screen.getByRole('link', { name: 'Open app preview' })).toHaveAttribute('href', 'http://127.0.0.1:54321/')
  })

  test('keeps unavailable and request errors inside the optional panel', async () => {
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response(preview({ profile: 'local_connected', available: false, reason: 'Local execution previews are unavailable.', preview: { state: 'idle' } })))
      .mockResolvedValueOnce(response({ error: 'preview service unavailable' }, false, 503))
    vi.stubGlobal('fetch', fetcher)
    const rendered = render(<LanguageProvider><AppPreview runId="run-1" expectedVersion={1} /></LanguageProvider>)
    await userEvent.click(screen.getByText('App preview (optional)'))
    expect(await screen.findByText('Local execution · No Sandbox')).toBeInTheDocument()
    expect(screen.getByText('Local execution previews are unavailable.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Start preview' })).not.toBeInTheDocument()
    expect(screen.getByText(/does not block code review or delivery/)).toBeInTheDocument()

    rendered.rerender(<LanguageProvider><AppPreview runId="run-2" expectedVersion={2} /></LanguageProvider>)
    await userEvent.click(screen.getByText('App preview (optional)'))
    expect(await screen.findByRole('alert')).toHaveTextContent('preview service unavailable')
    expect(screen.getByText(/does not block code review or delivery/)).toBeInTheDocument()
  })

  test.each([
    ['running', ['Stop preview']],
    ['recovery_required', ['Stop preview', 'Clean up preview']],
  ] as const)('keeps process controls available when the service reports unavailable in %s', async (state, controls) => {
    vi.stubGlobal('fetch', vi.fn(async () => response(preview({ available: false, reason: 'Workspace is archived.', preview: { state } }))))
    render(<LanguageProvider><AppPreview runId="run-1" expectedVersion={1} /></LanguageProvider>)
    await userEvent.click(screen.getByText('App preview (optional)'))
    for (const control of controls) expect(await screen.findByRole('button', { name: control })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Start preview' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save configuration' })).not.toBeInTheDocument()
  })

  test('ignores a stale repository response and aborts requests on run change and unmount', async () => {
    let resolveRepo!: (value: Response) => void
    let repoSignal: AbortSignal | undefined
    const repoRequest = new Promise<Response>((resolve) => { resolveRepo = resolve })
    const signals: AbortSignal[] = []
    vi.stubGlobal('fetch', vi.fn((input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      if (init?.signal) signals.push(init.signal)
      if (path.includes('repoId=repo-b')) { repoSignal = init?.signal ?? undefined; return repoRequest }
      return Promise.resolve(response(preview({ repositories: [{ repoId: 'repo-a', name: 'frontend' }, { repoId: 'repo-b', name: 'backend' }] })))
    }))
    const rendered = render(<LanguageProvider><AppPreview runId="run-1" expectedVersion={1} /></LanguageProvider>)
    await userEvent.click(screen.getByText('App preview (optional)'))
    await userEvent.selectOptions(await screen.findByRole('combobox', { name: 'Repository' }), 'repo-b')
    await waitFor(() => expect(repoSignal).toBeDefined())

    rendered.rerender(<LanguageProvider><AppPreview runId="run-2" expectedVersion={2} /></LanguageProvider>)
    expect(repoSignal?.aborted).toBe(true)
    resolveRepo(response(preview({ runId: 'run-1', repoId: 'repo-b', preview: { state: 'running', logs: 'stale output' } })))
    await Promise.resolve()
    expect(screen.queryByText('stale output')).not.toBeInTheDocument()
    expect(screen.getByText('App preview (optional)').closest('details')).not.toHaveAttribute('open')

    await userEvent.click(screen.getByText('App preview (optional)'))
    await waitFor(() => expect(signals.length).toBeGreaterThan(2))
    const lastSignal = signals.at(-1)
    rendered.unmount()
    expect(lastSignal?.aborted).toBe(true)
  })

  test('shows explicit stop and cleanup recovery controls and only safe loopback links', async () => {
    const actions: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
      const path = String(input)
      if (path.endsWith('/stop')) { actions.push('stop'); return response(preview({ preview: { state: 'recovery_required', url: 'http://localhost:4173/result', reason: 'Confirm process state.' } })) }
      if (path.endsWith('/cleanup')) { actions.push('cleanup'); return response(preview({ preview: { state: 'idle' } })) }
      return response(preview({ preview: { state: 'recovery_required', config: { command: 'npm run dev', workingDirectory: '.', port: 4173 }, url: 'http://example.test:4173/', logs: 'last output', logTruncated: true, reason: 'Confirm process state.' } }))
    }))
    render(<LanguageProvider><AppPreview runId="run-1" expectedVersion={8} /></LanguageProvider>)
    await userEvent.click(screen.getByText('App preview (optional)'))
    expect(await screen.findByRole('button', { name: 'Stop preview' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Clean up preview' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Open app preview' })).not.toBeInTheDocument()
    expect(screen.getByLabelText('Preview logs')).toHaveTextContent('last output')
    expect(screen.getByText('Earlier app logs were truncated.')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Stop preview' }))
    await waitFor(() => expect(actions).toEqual(['stop']))
    const link = screen.getByRole('link', { name: 'Open app preview' })
    expect(link).toHaveAttribute('href', 'http://localhost:4173/result')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    await userEvent.click(screen.getByRole('button', { name: 'Clean up preview' }))
    await waitFor(() => expect(actions).toEqual(['stop', 'cleanup']))
  })
})
