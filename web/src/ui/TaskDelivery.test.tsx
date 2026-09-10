import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { DeliveryOperation, TaskDeliveryView } from '../taskDeliveryTypes'
import { TaskDelivery } from './TaskDelivery'

function response(body: unknown, ok = true, status = 200): Response {
  if (body && typeof body === 'object' && 'provider' in body && 'message' in body) body = { ...body, fingerprint: 'source-1' }
  return { ok, status, json: async () => body } as Response
}
function installFetch(fetcher: (input: string | URL | Request, init?: RequestInit) => Promise<Response>) {
  vi.stubGlobal('fetch', (input: string | URL | Request, init?: RequestInit) => {
    if (String(input).endsWith('/delivery/draft-context')) return Promise.resolve(response({ fingerprint: 'source-1', owner: 'local', templates: [], warnings: [] }))
    return fetcher(input, init)
  })
}
function delivery(overrides: Partial<TaskDeliveryView> = {}): TaskDeliveryView {
  return { capabilities: { hosting: false, createPR: false, merge: false }, repositories: [{ repoId: 'repo-a', name: 'frontend', targetBranch: 'main', taskBranch: 'chora/task-1/repo-a', status: 'uncommitted', operations: [] }], ...overrides }
}
function preview(overrides: Partial<DeliveryOperation> = {}): DeliveryOperation {
  return { id: 'op-1', repoId: 'repo-a', kind: 'commit', status: 'preview', version: 8, message: 'Ship reviewed change', paths: ['src/app.tsx'], head: 'a'.repeat(40), tree: 'b'.repeat(40), ...overrides }
}

describe('TaskDelivery', () => {
  beforeEach(() => window.localStorage.clear())

  test('shows exact commit preview and requires a separate confirmation', async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, _init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/delivery/message')) return response({ message: 'feat(auth): validate login input', provider: 'test', model: 'test' })
      if (path.endsWith('/delivery/preview')) return response(preview())
      if (path.endsWith('/delivery/confirm')) return response(preview({ status: 'succeeded', commit: 'c'.repeat(40) }))
      if (path.endsWith('/delivery/refresh')) return response(delivery({ repositories: [{ ...delivery().repositories[0], status: 'committed', commit: 'c'.repeat(40), operations: [preview({ status: 'succeeded' })] }] }))
      return response(delivery())
    })
    installFetch(fetchMock)
    render(<LanguageProvider><TaskDelivery runId="run-1" expectedVersion={7} resultDigest="digest-1" /></LanguageProvider>)

    const card = await screen.findByRole('article', { name: 'frontend' })
    await waitFor(() => expect(within(card).getByLabelText('Commit message')).toHaveValue('feat(auth): validate login input'))
    await waitFor(() => expect(within(card).getByRole('button', { name: 'Preview Commit task branch' })).toBeEnabled())
    await userEvent.clear(within(card).getByLabelText('Commit message'))
    expect(within(card).getByRole('button', { name: 'Preview Commit task branch' })).toBeDisabled()
    await userEvent.type(within(card).getByLabelText('Commit message'), 'Ship reviewed change')
    await userEvent.click(within(card).getByRole('button', { name: 'Preview Commit task branch' }))
    expect(await within(card).findByText('src/app.tsx')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([path]) => String(path).endsWith('/delivery/confirm'))).toHaveLength(0)

    await userEvent.click(within(card).getByRole('button', { name: 'Confirm Commit task branch' }))
    await waitFor(() => expect(within(card).getByText('Committed locally')).toBeInTheDocument())
    const previewCall = fetchMock.mock.calls.find(([path]) => String(path).endsWith('/delivery/preview'))
    expect(JSON.parse(String(previewCall?.[1]?.body))).toEqual(expect.objectContaining({ repoId: 'repo-a', expectedVersion: 7, resultDigest: 'digest-1', kind: 'commit', message: 'Ship reviewed change' }))
    const confirmCall = fetchMock.mock.calls.find(([path]) => String(path).endsWith('/delivery/confirm'))
    expect(JSON.parse(String(confirmCall?.[1]?.body))).toEqual({ operationId: 'op-1', expectedVersion: 7, resultDigest: 'digest-1' })
    const refreshCall = fetchMock.mock.calls.find(([path]) => String(path).endsWith('/delivery/refresh'))
    expect(JSON.parse(String(refreshCall?.[1]?.body))).toEqual({ repoId: 'repo-a', expectedVersion: 7, resultDigest: 'digest-1' })
  })

  test('does not overwrite edits with a late suggestion or refresh and keeps repositories separate', async () => {
    let finish!: (value: Response) => void
    const late = new Promise<Response>((resolve) => { finish = resolve })
    const twoRepos = delivery({ repositories: [...delivery().repositories, { ...delivery().repositories[0], repoId: 'repo-b', name: 'backend' }] })
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith('/delivery/message')) {
        return JSON.parse(String(init?.body)).repoId === 'repo-a' ? late : response({ message: 'fix(api): reject invalid tokens', provider: 'test', model: 'test' })
      }
      return response(twoRepos)
    })
    installFetch(fetchMock)
    const rendered = render(<LanguageProvider><TaskDelivery runId="run-1" expectedVersion={7} resultDigest="digest-1" /></LanguageProvider>)
    const frontend = await screen.findByRole('article', { name: 'frontend' })
    await userEvent.type(within(frontend).getByLabelText('Commit message'), 'fix(ui): preserve typed input\n\nKeep edits during refresh.')
    finish(response({ message: 'feat(ui): generated draft', provider: 'test', model: 'test' }))
    await waitFor(() => expect(within(frontend).getByRole('button', { name: 'Generate commit message' })).toBeEnabled())
    const expected = 'fix(ui): preserve typed input\n\nKeep edits during refresh.'
    expect(within(frontend).getByLabelText('Commit message')).toHaveValue(expected)
    expect(within(screen.getByRole('article', { name: 'backend' })).getByLabelText('Commit message')).toHaveValue('fix(api): reject invalid tokens')
    await userEvent.click(within(frontend).getByRole('button', { name: 'Refresh status' }))
    rendered.rerender(<LanguageProvider><TaskDelivery runId="run-1" expectedVersion={8} resultDigest="digest-1" /></LanguageProvider>)
    await waitFor(() => expect(within(screen.getByRole('article', { name: 'frontend' })).getByLabelText('Commit message')).toHaveValue(expected))
    expect(fetchMock.mock.calls.filter(([path]) => String(path).endsWith('/delivery/message'))).toHaveLength(2)
  })

  test('generates a new draft when the same repository is opened under a different result', async () => {
    installFetch(vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith('/delivery/message')) return response({ message: JSON.parse(String(init?.body)).resultDigest === 'digest-1' ? 'fix: first change' : 'feat: second change', provider: 'test', model: 'test' })
      return response(delivery())
    }))
    const rendered = render(<LanguageProvider><TaskDelivery runId="run-1" expectedVersion={7} resultDigest="digest-1" /></LanguageProvider>)
    await waitFor(() => expect(screen.getByLabelText('Commit message')).toHaveValue('fix: first change'))
    rendered.rerender(<LanguageProvider><TaskDelivery runId="run-2" expectedVersion={1} resultDigest="digest-2" /></LanguageProvider>)
    await waitFor(() => expect(screen.getByLabelText('Commit message')).toHaveValue('feat: second change'))
  })

  test('keeps a stale preview visible with the server failure and does not claim success', async () => {
    installFetch(vi.fn(async (input: string | URL | Request) => {
      const path = String(input)
      if (path.endsWith('/delivery/preview')) return response(preview())
      if (path.endsWith('/delivery/confirm')) return response({ error: 'preview is stale; refresh required' }, false, 409)
      return response(delivery())
    }))
    render(<LanguageProvider><TaskDelivery runId="run-1" expectedVersion={7} resultDigest="digest-1" /></LanguageProvider>)
    const card = await screen.findByRole('article', { name: 'frontend' })
    await userEvent.type(within(card).getByLabelText('Commit message'), 'Ship reviewed change')
    await userEvent.click(within(card).getByRole('button', { name: 'Preview Commit task branch' }))
    await userEvent.click(await within(card).findByRole('button', { name: 'Confirm Commit task branch' }))
    expect(await within(card).findByText('preview is stale; refresh required')).toBeInTheDocument()
    expect(within(card).getByText('Ready to commit')).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Confirm Commit task branch' })).toBeInTheDocument()
  })

  test('discards an in-flight preview when a different run is loaded', async () => {
    let resolveOld!: (value: Response) => void
    const oldPreview = new Promise<Response>((resolve) => { resolveOld = resolve })
    installFetch(vi.fn(async (input: string | URL | Request) => {
      const path = String(input)
      if (path.endsWith('/delivery/preview')) return oldPreview
      const runTwo = path.includes('/runs/run-2/')
      return response(delivery({ repositories: [{ ...delivery().repositories[0], name: runTwo ? 'run-two-repo' : 'run-one-repo' }] }))
    }))
    const rendered = render(<LanguageProvider><TaskDelivery runId="run-1" expectedVersion={7} resultDigest="digest-1" /></LanguageProvider>)
    const oldCard = await screen.findByRole('article', { name: 'run-one-repo' })
    await userEvent.type(within(oldCard).getByLabelText('Commit message'), 'Old preview')
    await userEvent.click(within(oldCard).getByRole('button', { name: 'Preview Commit task branch' }))
    rendered.rerender(<LanguageProvider><TaskDelivery runId="run-2" expectedVersion={1} resultDigest="digest-2" /></LanguageProvider>)
    expect(await screen.findByRole('article', { name: 'run-two-repo' })).toBeInTheDocument()
    resolveOld(response(preview({ message: 'Old preview', paths: ['stale.ts'] })))
    await waitFor(() => expect(screen.queryByText('stale.ts')).not.toBeInTheDocument())
  })

  test('blocks mutation while an interrupted operation awaits read-only refresh', async () => {
    installFetch(vi.fn(async () => response(delivery({ repositories: [{ ...delivery().repositories[0], operations: [preview({ status: 'writing' })] }] }))))
    render(<LanguageProvider><TaskDelivery runId="run-1" expectedVersion={7} resultDigest="digest-1" /></LanguageProvider>)
    const card = await screen.findByRole('article', { name: 'frontend' })
    await userEvent.type(within(card).getByLabelText('Commit message'), 'Do not replay')
    expect(within(card).getByRole('button', { name: 'Preview Commit task branch' })).toBeDisabled()
    expect(screen.getByText('A delivery operation was interrupted. Refresh its repository before continuing.')).toBeInTheDocument()
  })

  test('allows one repository to advance while another remains unchanged', async () => {
    const base = delivery({ repositories: [
      { repoId: 'repo-a', name: 'frontend', targetBranch: 'main', taskBranch: 'chora/t/a', status: 'committed', operations: [] },
      { repoId: 'repo-b', name: 'backend', targetBranch: 'release', taskBranch: 'chora/t/b', status: 'uncommitted', operations: [] },
    ] })
    installFetch(vi.fn(async (input: string | URL | Request) => String(input).endsWith('/delivery/preview')
      ? response(preview({ kind: 'push', remote: 'origin', remoteRef: 'refs/heads/chora/t/a' })) : response(base)))
    render(<LanguageProvider><TaskDelivery runId="run-1" expectedVersion={7} resultDigest="digest-1" /></LanguageProvider>)
    const frontend = await screen.findByRole('article', { name: 'frontend' })
    const backend = screen.getByRole('article', { name: 'backend' })
    await userEvent.click(within(frontend).getByRole('button', { name: 'Preview Push task branch' }))
    expect(await within(frontend).findByText('refs/heads/chora/t/a')).toBeInTheDocument()
    expect(within(backend).getByText('Ready to commit')).toBeInTheDocument()
    expect(within(backend).getByLabelText('Commit message')).toBeEnabled()
  })

  test('renders legacy and pushed states without inventing hosting support', async () => {
    installFetch(vi.fn(async () => response(delivery({ repositories: [
      { repoId: 'old', name: 'legacy-repo', targetBranch: '', taskBranch: '', status: 'legacy', operations: [] },
      { repoId: 'new', name: 'new-repo', targetBranch: 'main', taskBranch: 'chora/t/new', status: 'pushed', commit: 'c'.repeat(40), operations: [] },
    ] }))))
    render(<LanguageProvider><TaskDelivery runId="run-1" expectedVersion={7} resultDigest="digest-1" /></LanguageProvider>)
    expect(await screen.findByText('Legacy delivery')).toBeInTheDocument()
    expect(screen.getByText('GitHub pull requests are unavailable. Install the GitHub CLI, run gh auth login, and refresh this repository.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /pull request/i })).not.toBeInTheDocument()
    expect(screen.queryByText('Merged')).not.toBeInTheDocument()
  })

  test('advances through GitHub PR, merge, and explicit clean-worktree cleanup', async () => {
    let status: TaskDeliveryView['repositories'][number]['status'] = 'pushed'
    const calls: Array<Record<string, unknown>> = []
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : {}
      if (path.endsWith('/delivery/message')) return response({ message: '', title: 'Ship widgets', body: 'Reviewed changes. Verification: not run.', provider: 'test', model: 'test' })
      if (path.endsWith('/delivery/preview')) {
        calls.push(body)
        const kind = body.kind as DeliveryOperation['kind']
        return response(preview({ id: `preview-${kind}`, kind, title: kind === 'pr' ? String(body.title) : undefined, body: kind === 'pr' ? String(body.body) : undefined, repository: 'acme/widgets', headBranch: 'chora/t/a', baseBranch: 'main', baseHead: 'd'.repeat(40), mergeMethod: kind === 'merge' ? 'merge' : undefined, number: kind === 'merge' ? 42 : undefined, worktreePath: kind === 'cleanup' ? '/tmp/chora/t/a' : undefined }))
      }
      if (path.endsWith('/delivery/confirm')) {
        const kind = String(body.operationId).replace('preview-', '') as DeliveryOperation['kind']
        status = kind === 'pr' ? 'pr_open' : kind === 'merge' ? 'merged' : 'cleaned'
        return response(preview({ id: String(body.operationId), kind, status: 'succeeded' }))
      }
      return response(delivery({ version: 9, capabilities: { hosting: true, createPR: true, merge: true, cleanup: true, provider: 'github' }, repositories: [{ repoId: 'repo-a', name: 'frontend', targetBranch: 'main', taskBranch: 'chora/t/a', status, commit: 'c'.repeat(40), prUrl: status === 'pr_open' || status === 'merged' ? 'https://github.com/acme/widgets/pull/42' : undefined, operations: [] }] }))
    })
    installFetch(fetchMock)
    render(<LanguageProvider><TaskDelivery runId="run-1" expectedVersion={7} resultDigest="digest-1" /></LanguageProvider>)
    const card = await screen.findByRole('article', { name: 'frontend' })

    await waitFor(() => expect(within(card).getByLabelText('Title')).toHaveValue('Ship widgets'))
    await userEvent.click(within(card).getByRole('button', { name: 'Preview Create pull request' }))
    expect(await within(card).findByText('acme/widgets')).toBeInTheDocument()
    await userEvent.click(within(card).getByRole('button', { name: 'Confirm Create pull request' }))
    await waitFor(() => expect(within(card).getByText('Pull request open')).toBeInTheDocument())
    expect(within(card).getByText(/create a new Task from this pull request branch/)).toBeInTheDocument()
    expect(within(card).queryByLabelText('Title')).not.toBeInTheDocument()

    await userEvent.click(within(card).getByRole('button', { name: 'Preview Merge pull request' }))
    expect(await within(card).findByText('#42')).toBeInTheDocument()
    await userEvent.click(within(card).getByRole('button', { name: 'Confirm Merge pull request' }))
    await waitFor(() => expect(within(card).getByText('Merged')).toBeInTheDocument())

    await userEvent.click(within(card).getByRole('button', { name: 'Preview Clean up task worktree' }))
    expect(await within(card).findByText('/tmp/chora/t/a')).toBeInTheDocument()
    await userEvent.click(within(card).getByRole('button', { name: 'Confirm Clean up task worktree' }))
    await waitFor(() => expect(within(card).getByText('Task worktree cleaned up')).toBeInTheDocument())
    expect(calls.find((call) => call.kind === 'merge')).toEqual(expect.not.objectContaining({ title: expect.anything(), body: expect.anything() }))
    expect(calls.map((call) => call.kind)).toEqual(['pr', 'merge', 'cleanup'])
  })
})
