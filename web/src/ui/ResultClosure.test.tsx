import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import { api } from '../api'
import { ResultClosure } from './ResultClosure'
import { ResourceApplyStatus } from './ResourceResult'

vi.mock('../api', () => ({ api: vi.fn(), commandKey: () => 'closure-key', message: (e: Error) => e.message }))
afterEach(() => vi.clearAllMocks())

const preview = { resultId: 'result_1', resultDigest: 'digest-1', previewDigest: 'preview-1', runVersion: 3, entries: [
  { repoId: 'repo_a', name: 'API', status: 'retained' },
  { repoId: 'repo_b', name: 'Web', status: 'eligible' },
] }

const closed = { ...preview, closedAt: '2026-09-09T00:00:00Z', entries: [
  { repoId: 'repo_a', name: 'API', status: 'retained', canCleanup: true },
  { repoId: 'repo_b', name: 'Web', status: 'closed', canCleanup: true },
  { repoId: 'repo_c', name: 'Docs', status: 'closed', canCleanup: false },
] }
const cleanupPreview = { id: 'delivery_cleanup_1', repoId: 'repo_b', kind: 'cleanup', status: 'preview', version: 1, worktreePath: '.chora/tasks/web', paths: ['src/new.ts', 'notes.txt'] }

test('preview distinguishes kept work and confirmation binds exact result evidence', async () => {
  localStorage.setItem('chora.language', 'en')
  const user = userEvent.setup()
  const onClosed = vi.fn()
  vi.mocked(api).mockResolvedValueOnce(preview).mockResolvedValueOnce({ ...preview, closedAt: '2026-09-09T00:00:00Z' })
  render(<LanguageProvider><ResultClosure runId="run_1" version={3} closed={false} onClosed={onClosed} /></LanguageProvider>)
  expect(screen.queryByRole('button', { name: 'Confirm close' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Preview close' }))
  expect(await screen.findByText('API · Delivered work kept')).toBeInTheDocument()
  expect(screen.getByText('Web · Will close')).toBeInTheDocument()
  expect(onClosed).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Confirm close' }))
  expect(JSON.parse(vi.mocked(api).mock.calls[1][1]!.body as string)).toEqual({ expectedVersion: 3, resultDigest: 'digest-1', previewDigest: 'preview-1' })
  expect(onClosed).toHaveBeenCalledTimes(1)
  expect(screen.queryByRole('button', { name: 'Confirm close' })).not.toBeInTheDocument()
})

test('stale confirmation requires a new preview and keeps the result open', async () => {
  const user = userEvent.setup()
  const onClosed = vi.fn()
  vi.mocked(api).mockResolvedValueOnce(preview).mockRejectedValueOnce(new Error('Result changed'))
  render(<LanguageProvider><ResultClosure runId="run_1" version={3} closed={false} onClosed={onClosed} /></LanguageProvider>)
  await user.click(screen.getByRole('button', { name: 'Preview close' }))
  await user.click(await screen.findByRole('button', { name: 'Confirm close' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Result changed')
  expect(screen.getByRole('button', { name: 'Preview close' })).toBeInTheDocument()
  expect(onClosed).not.toHaveBeenCalled()
})

test('partial Apply with closed remainder preserves achievement without offering another write', () => {
  render(<LanguageProvider><ResourceApplyStatus apply={{ operationId: 'apply_1', status: 'partial_applied', repositories: [
    { repoId: 'repo_a', status: 'applied', sequence: 3 }, { repoId: 'repo_b', status: 'closed', sequence: 2 },
  ] }} canApply busy={false} onApply={vi.fn()} /></LanguageProvider>)
  expect(screen.getByText('Some repositories were applied; remaining changes closed')).toBeInTheDocument()
  expect(screen.queryByText('Applied to all selected repositories')).not.toBeInTheDocument()
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
})

test('loading a closed result never starts cleanup and offers it only for closed eligible entries', async () => {
  vi.mocked(api).mockResolvedValueOnce(closed)
  render(<LanguageProvider><ResultClosure runId="run_1" version={3} closed onClosed={vi.fn()} /></LanguageProvider>)
  expect(await screen.findByText('Web · Closed')).toBeInTheDocument()
  expect(screen.getAllByRole('button', { name: 'Preview cleanup' })).toHaveLength(1)
  expect(screen.getByText('API · Delivered work kept')).toBeInTheDocument()
  expect(screen.getByText('Docs · Closed')).toBeInTheDocument()
  expect(vi.mocked(api)).toHaveBeenCalledTimes(1)
  expect(vi.mocked(api).mock.calls[0][0]).toBe('/api/v2/runs/run_1/closure')
})

test('previews retained files before sending the exact cleanup confirmation', async () => {
  const user = userEvent.setup()
  const onClosed = vi.fn()
  const succeeded = { ...cleanupPreview, status: 'succeeded' }
  vi.mocked(api).mockResolvedValueOnce(closed).mockResolvedValueOnce(cleanupPreview).mockResolvedValueOnce(succeeded).mockResolvedValueOnce({
    ...closed, entries: closed.entries.map((entry) => entry.repoId === 'repo_b' ? { ...entry, cleanup: succeeded } : entry),
  })
  render(<LanguageProvider><ResultClosure runId="run_1" version={3} closed onClosed={onClosed} /></LanguageProvider>)
  await user.click(await screen.findByRole('button', { name: 'Preview cleanup' }))
  expect(await screen.findByText('.chora/tasks/web')).toBeInTheDocument()
  expect(screen.getByText('src/new.ts')).toBeInTheDocument()
  expect(screen.getByText(/Original repositories, branches, commits, and result history are kept/)).toBeInTheDocument()
  expect(JSON.parse(vi.mocked(api).mock.calls[1][1]!.body as string)).toEqual({ expectedVersion: 3, resultDigest: 'digest-1', repoId: 'repo_b' })

  await user.click(screen.getByRole('button', { name: 'Confirm cleanup' }))
  await screen.findByText('Web · Cleaned')
  expect(JSON.parse(vi.mocked(api).mock.calls[2][1]!.body as string)).toEqual({ expectedVersion: 3, resultDigest: 'digest-1', repoId: 'repo_b', operationId: 'delivery_cleanup_1' })
  expect(vi.mocked(api).mock.calls[2][1]!.headers).toEqual(expect.objectContaining({ 'Idempotency-Key': 'closure-key' }))
  expect(onClosed).toHaveBeenCalledTimes(1)
})

test('failed cleanup retains its evidence and requires a fresh preview', async () => {
  const user = userEvent.setup()
  const failed = { ...cleanupPreview, status: 'failed', reason: 'extra retained file prevents cleanup' }
  const failedClosure = { ...closed, entries: closed.entries.map((entry) => entry.repoId === 'repo_b' ? { ...entry, cleanup: failed } : entry) }
  vi.mocked(api).mockResolvedValueOnce(failedClosure).mockResolvedValueOnce({ ...cleanupPreview, id: 'delivery_cleanup_2' })
  render(<LanguageProvider><ResultClosure runId="run_1" version={3} closed onClosed={vi.fn()} /></LanguageProvider>)
  expect(await screen.findByText('extra retained file prevents cleanup')).toBeInTheDocument()
  expect(screen.getByText('.chora/tasks/web')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Confirm cleanup' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Preview cleanup again' }))
  expect(JSON.parse(vi.mocked(api).mock.calls[1][1]!.body as string)).toEqual({ expectedVersion: 3, resultDigest: 'digest-1', repoId: 'repo_b' })
  expect(await screen.findByRole('button', { name: 'Confirm cleanup' })).toBeInTheDocument()
})

test('refresh observes a succeeded cleanup without calling confirm again', async () => {
  const user = userEvent.setup()
  const interrupted = { ...cleanupPreview, status: 'recovery_required', reason: 'outcome requires observation' }
  const initial = { ...closed, entries: closed.entries.map((entry) => entry.repoId === 'repo_b' ? { ...entry, cleanup: interrupted } : entry) }
  const succeeded = { ...cleanupPreview, status: 'succeeded' }
  const refreshed = { ...closed, entries: closed.entries.map((entry) => entry.repoId === 'repo_b' ? { ...entry, cleanup: succeeded } : entry) }
  vi.mocked(api).mockResolvedValueOnce(initial).mockResolvedValueOnce(refreshed)
  render(<LanguageProvider><ResultClosure runId="run_1" version={3} closed onClosed={vi.fn()} /></LanguageProvider>)
  expect(await screen.findByRole('button', { name: 'Observe cleanup' })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(await screen.findByText('Web · Cleaned')).toBeInTheDocument()
  expect(vi.mocked(api).mock.calls).toHaveLength(2)
  expect(vi.mocked(api).mock.calls.every(([, init]) => init?.method !== 'POST')).toBe(true)
})

test('stale and unmounted history requests cannot replace the current run', async () => {
  let resolveOld: ((value: ClosureForTest) => void) | undefined
  let resolveCurrent: ((value: ClosureForTest) => void) | undefined
  type ClosureForTest = typeof closed
  vi.mocked(api).mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve }))
    .mockImplementationOnce(() => new Promise((resolve) => { resolveCurrent = resolve }))
  const rendered = render(<LanguageProvider><ResultClosure runId="run_old" version={3} closed onClosed={vi.fn()} /></LanguageProvider>)
  rendered.rerender(<LanguageProvider><ResultClosure runId="run_current" version={4} closed onClosed={vi.fn()} /></LanguageProvider>)
  resolveCurrent?.({ ...closed, entries: [{ repoId: 'repo_current', name: 'Current', status: 'closed', canCleanup: true }] })
  expect(await screen.findByText('Current · Closed')).toBeInTheDocument()
  resolveOld?.({ ...closed, entries: [{ repoId: 'repo_old', name: 'Old', status: 'closed', canCleanup: true }] })
  await Promise.resolve()
  expect(screen.queryByText('Old · Closed')).not.toBeInTheDocument()
  rendered.unmount()

  let unmountedSignal: AbortSignal | undefined
  vi.mocked(api).mockImplementationOnce((_path, init) => {
    unmountedSignal = init?.signal as AbortSignal
    return new Promise(() => {})
  })
  const pending = render(<LanguageProvider><ResultClosure runId="run_unmounted" version={5} closed onClosed={vi.fn()} /></LanguageProvider>)
  pending.unmount()
  expect(unmountedSignal?.aborted).toBe(true)
})
