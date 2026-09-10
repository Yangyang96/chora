import type { FormEvent } from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import { RepositoryDirectoryPicker } from './RepositoryDirectoryPicker'

function response(body: unknown): Response { return { ok: true, status: 200, json: async () => body } as Response }

test('browses direct directories, exposes root, and loads explicit next pages', async () => {
  const fetcher = vi.fn(async (input: string | URL | Request) => {
    const url = new URL(String(input), 'http://chora.test')
    if (url.searchParams.get('cursor')) return response({ entries: [{ name: 'two', path: 'packages/two', kind: 'directory' }], nextCursor: '', truncated: false, revision: 'a'.repeat(40) })
    if (url.searchParams.get('path') === 'packages') return response({ entries: [{ name: 'one', path: 'packages/one', kind: 'directory' }], nextCursor: 'next', truncated: true, revision: 'a'.repeat(40) })
    return response({ entries: [{ name: 'packages', path: 'packages', kind: 'directory' }, { name: 'README', path: 'README', kind: 'file' }], nextCursor: '', truncated: false, revision: 'a'.repeat(40) })
  })
  vi.stubGlobal('fetch', fetcher)
  const selected = vi.fn()
  render(<LanguageProvider><RepositoryDirectoryPicker projectId="project-1" repoId="repo-1" onSelect={selected} onClose={() => {}} /></LanguageProvider>)
  await userEvent.click(await screen.findByRole('button', { name: 'packages' }))
  expect(await screen.findByRole('button', { name: 'one' })).toBeInTheDocument()
  expect(screen.getByText('Directory results are truncated.')).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
  expect(await screen.findByRole('button', { name: 'two' })).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Use this directory' }))
  expect(selected).toHaveBeenCalledWith('packages')
  await userEvent.click(screen.getByRole('button', { name: 'Use repository root' }))
  expect(selected).toHaveBeenCalledWith('.')
  expect(fetcher).toHaveBeenCalledWith(expect.stringContaining('limit=200'), expect.objectContaining({ signal: expect.any(AbortSignal) }))
})

test('aborts an outstanding page when closed', async () => {
  let signal: AbortSignal | undefined
  vi.stubGlobal('fetch', vi.fn((_input: unknown, init?: RequestInit) => {
    signal = init?.signal ?? undefined
    return new Promise<Response>(() => {})
  }))
  const close = vi.fn()
  render(<LanguageProvider><RepositoryDirectoryPicker projectId="project-1" repoId="repo-1" onSelect={() => {}} onClose={close} /></LanguageProvider>)
  await waitFor(() => expect(signal).toBeDefined())
  await userEvent.click(screen.getByRole('button', { name: 'Close' }))
  expect(signal?.aborted).toBe(true)
  expect(close).toHaveBeenCalledOnce()
})

test('reports directory errors and disables controls while busy', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 503, json: async () => ({ error: 'directory unavailable' }) } as Response)))
  render(<LanguageProvider><RepositoryDirectoryPicker projectId="project-1" repoId="repo-1" disabled onSelect={() => {}} onClose={() => {}} /></LanguageProvider>)
  expect(await screen.findByRole('alert')).toHaveTextContent('directory unavailable')
  expect(screen.getByRole('button', { name: 'Close' })).toBeDisabled()
})

test('searches the current directory and keeps the query on subsequent pages', async () => {
  const urls: URL[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
    const url = new URL(String(input), 'http://chora.test'); urls.push(url)
    if (url.searchParams.get('path') === 'module one') return response({ entries: [], nextCursor: '' })
    if (url.searchParams.get('query') === 'module') {
      return response(url.searchParams.get('cursor')
        ? { entries: [{ name: 'module two', path: 'module two', kind: 'directory' }], nextCursor: '' }
        : { entries: [{ name: 'module one', path: 'module one', kind: 'directory' }], nextCursor: 'filtered-page' })
    }
    return response({ entries: [{ name: 'unrelated', path: 'unrelated', kind: 'directory' }], nextCursor: '' })
  }))
  const submit = vi.fn((event: FormEvent) => event.preventDefault())
  render(<LanguageProvider><form onSubmit={submit}><RepositoryDirectoryPicker projectId="project-1" repoId="repo-1" onSelect={() => {}} onClose={() => {}} /></form></LanguageProvider>)
  await screen.findByRole('button', { name: 'unrelated' })
  await userEvent.type(screen.getByRole('searchbox', { name: 'Search child directories' }), 'module{Enter}')
  expect(await screen.findByRole('button', { name: 'module one' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'unrelated' })).not.toBeInTheDocument()
  expect(submit).not.toHaveBeenCalled()
  await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
  expect(await screen.findByRole('button', { name: 'module two' })).toBeInTheDocument()
  expect(urls.at(-1)?.searchParams.get('query')).toBe('module')
  expect(urls.at(-1)?.searchParams.get('cursor')).toBe('filtered-page')
  await userEvent.click(screen.getByRole('button', { name: 'module one' }))
  await screen.findByText('No child directories.')
  expect(urls.at(-1)?.searchParams.get('path')).toBe('module one')
  expect(urls.at(-1)?.searchParams.has('query')).toBe(false)
  expect(screen.getByRole('searchbox')).toHaveValue('')
})

test('a new search cancels the previous query and clearing restores browsing', async () => {
  let pending: AbortSignal | undefined
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input), 'http://chora.test')
    if (url.searchParams.get('query') === 'slow') {
      pending = init?.signal ?? undefined
      return new Promise<Response>(() => {})
    }
    return response({ entries: [{ name: 'visible', path: 'visible', kind: 'directory' }], nextCursor: '' })
  }))
  render(<LanguageProvider><RepositoryDirectoryPicker projectId="project-1" repoId="repo-1" onSelect={() => {}} onClose={() => {}} /></LanguageProvider>)
  await screen.findByRole('button', { name: 'visible' })
  await userEvent.type(screen.getByRole('searchbox'), 'slow')
  await userEvent.click(screen.getByRole('button', { name: 'Search directories' }))
  await waitFor(() => expect(pending).toBeDefined())
  await userEvent.click(screen.getByRole('button', { name: 'Clear directory search' }))
  expect(pending?.aborted).toBe(true)
  expect(await screen.findByRole('button', { name: 'visible' })).toBeInTheDocument()
  expect(screen.getByRole('searchbox')).toHaveValue('')
})
