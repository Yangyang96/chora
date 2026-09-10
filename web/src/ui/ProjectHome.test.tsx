import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { RepositoryResourceView } from '../taskFirstTypes'
import type { ProjectView } from '../types'
import { ProjectHome } from './ProjectHome'

const general = { id: 'room-general', projectId: 'project-1', ownershipKind: 'project' as const, name: 'General', description: 'Daily work', state: 'active' as const, version: 2, archivedAt: '' }
const frontend: RepositoryResourceView = { repoId: 'repo-front', name: 'frontend', localLocator: '/code/frontend', state: 'active', version: 3, availability: 'ready', branch: 'main', head: 'a'.repeat(40), dirty: true }
const backend: RepositoryResourceView = { repoId: 'repo-back', name: 'backend', localLocator: '/code/backend', state: 'active', version: 5, availability: 'unavailable', branch: 'release', head: 'b'.repeat(40), dirty: false, reason: 'Directory is missing.' }
const project: ProjectView = {
  id: 'project-1', name: 'Payments', description: 'Long-lived payment platform resources', state: 'active', version: 4, defaultRoomId: general.id, rooms: [general], room: general,
  repositories: [frontend, backend], lastActivityAt: '', taskCounts: { total: 1, open: 1, terminal: 0 },
}

function response(body: unknown, ok = true, status = 200): Response { return { ok, status, json: async () => body } as Response }

describe('ProjectHome', () => {
  beforeEach(() => { window.localStorage.clear(); vi.unstubAllGlobals() })

  test('shows an empty Project and adds a repository with the native chooser before refreshing v2 state', async () => {
    const empty = { ...project, repositories: [] }
    const refreshed = { ...project, repositories: [frontend] }
    const changed = vi.fn()
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      expect(String(input)).toMatch(/^\/api\/v2\/projects\/project-1/)
      if (init?.method === 'POST') return response({ cancelled: false, repository: frontend })
      return response(refreshed)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><ProjectHome project={empty} onChange={changed} onOpenRoom={() => {}} /></LanguageProvider>)
    expect(screen.getByText(/No repositories yet/)).toBeInTheDocument()
    expect(screen.queryByText('Task scope and checks')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '＋ Add repository' }))
    await waitFor(() => expect(changed).toHaveBeenCalledWith(refreshed))
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
      '/api/v2/projects/project-1/repositories/choose-directory',
      '/api/v2/projects/project-1',
    ])
  })

  test('renders repository cards with path, branch, dirty and availability details', () => {
    const view = render(<LanguageProvider><ProjectHome project={project} onChange={() => {}} onOpenRoom={() => {}} /></LanguageProvider>)
    const projectHeader = view.container.querySelector('.project-home-head') as HTMLElement
    expect(within(projectHeader).getByText('Long-lived payment platform resources')).toBeInTheDocument()
    expect(within(projectHeader).queryByText('Daily work')).not.toBeInTheDocument()
    expect(screen.getByText('/code/frontend')).toBeInTheDocument()
    expect(screen.getByText(/main · aaaaaaa · Uncommitted changes · Repository availability: ready/)).toBeInTheDocument()
    expect(screen.getByText('/code/backend')).toBeInTheDocument()
    expect(screen.getByText(/release · bbbbbbb · Clean · Repository availability: unavailable/)).toBeInTheDocument()
    expect(screen.getByText('Directory is missing.')).toBeInTheDocument()
  })

  test('opens named checks for the selected repository card', async () => {
    const fetchMock = vi.fn(async () => response({ checks: [{
      id: 'unit', name: 'Unit tests', version: 2, command: 'npm test', argv: ['npm', 'test'], workingDirectory: '.', source: 'user',
    }] }))
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><ProjectHome project={project} onChange={() => {}} onOpenRoom={() => {}} /></LanguageProvider>)

    const frontendCard = screen.getByText('/code/frontend').closest('article')!
    await userEvent.click(within(frontendCard).getByRole('button', { name: 'Manage checks' }))

    expect(await screen.findByRole('dialog', { name: 'Named checks for frontend' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Edit Unit tests' })).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledWith('/api/v2/projects/project-1/repositories/repo-front/checks', expect.objectContaining({ signal: expect.any(AbortSignal) }))
  })

  test('loads a later bounded repository page into Project and Room controls', async () => {
    const paged = { ...project, repositories: [frontend], nextRepositoryCursor: 'project-bound-cursor' }
    const fetchMock = vi.fn(async (_input: string | URL | Request) => response({ repositories: [backend], nextCursor: '' }))
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><ProjectHome project={paged} onChange={() => {}} onOpenRoom={() => {}} /></LanguageProvider>)
    expect(screen.queryByText('/code/backend')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Load more repositories' }))
    expect(await screen.findByText('/code/backend')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load more repositories' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Choose common repositories' }))
    expect(await screen.findByRole('checkbox', { name: /backend/ })).toBeInTheDocument()
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
      '/api/v2/projects/project-1/repositories?cursor=project-bound-cursor&limit=200',
      '/api/v2/rooms/room-general/resources',
    ])
  })

  test('removes one repository association with its version and refreshes the Project', async () => {
    const refreshed = { ...project, repositories: [backend] }
    const changed = vi.fn()
    const fetchMock = vi.fn(async (_input: string | URL | Request, init?: RequestInit) => {
      if (init?.method === 'DELETE') return response({})
      return response(refreshed)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><ProjectHome project={project} onChange={changed} onOpenRoom={() => {}} /></LanguageProvider>)
    const frontendCard = screen.getByText('/code/frontend').closest('article')!
    await userEvent.click(within(frontendCard).getByRole('button', { name: 'Remove repository' }))
    await waitFor(() => expect(changed).toHaveBeenCalledWith(refreshed))
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
      '/api/v2/projects/project-1/repositories/repo-front?expectedVersion=3',
      '/api/v2/projects/project-1',
    ])
  })

  test('loads and saves explicit Room repository defaults without selecting every repository', async () => {
    const calls: Array<{ path: string; method: string; body?: unknown }> = []
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      calls.push({ path: String(input), method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (init?.method === 'PUT') return response({ version: 8, repoIds: ['repo-front', 'repo-back'] })
      return response({ version: 7, repoIds: ['repo-back'] })
    }))
    render(<LanguageProvider><ProjectHome project={project} onChange={() => {}} onOpenRoom={() => {}} /></LanguageProvider>)
    await userEvent.click(screen.getByRole('button', { name: 'Choose common repositories' }))
    const front = await screen.findByRole('checkbox', { name: /frontend/ })
    const back = screen.getByRole('checkbox', { name: /backend/ })
    expect(front).not.toBeChecked()
    expect(back).toBeChecked()
    await userEvent.click(front)
    await userEvent.click(screen.getByRole('button', { name: 'Save repository defaults' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Choose common repositories' })).toBeInTheDocument())
    expect(calls).toEqual([
      { path: '/api/v2/rooms/room-general/resources', method: 'GET', body: undefined },
      { path: '/api/v2/rooms/room-general/resources', method: 'PUT', body: { version: 7, repoIds: ['repo-back', 'repo-front'] } },
    ])
  })

  test('creates a topic Room inside the Project without repository input', async () => {
    const changed = vi.fn(); const opened = vi.fn()
    const refund = { ...general, id: 'room-refund', name: 'Refund feature', description: 'Design refund flow', version: 1 }
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      expect(String(input)).toBe('/api/projects/project-1/rooms')
      expect(JSON.parse(String(init?.body))).toEqual({ name: 'Refund feature', description: 'Design refund flow' })
      return response(refund)
    }))
    render(<LanguageProvider><ProjectHome project={project} onChange={changed} onOpenRoom={opened} /></LanguageProvider>)
    await userEvent.click(screen.getByRole('button', { name: '＋ New topic Room' }))
    expect(screen.queryByLabelText('Repository')).not.toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('Room name'), 'Refund feature')
    await userEvent.type(screen.getByLabelText('Room Brief'), 'Design refund flow')
    await userEvent.click(screen.getByRole('button', { name: 'Create Room' }))
    await waitFor(() => expect(opened).toHaveBeenCalledWith(refund))
    expect(changed).toHaveBeenCalledWith(expect.objectContaining({ rooms: [general, refund] }))
  })

  test('renames and archives through v2 Project identity while keeping Room history visible', async () => {
    const renamed = { ...project, name: 'Billing', version: 5 }
    const archived = { ...renamed, state: 'archived' as const, version: 6 }
    const changed = vi.fn()
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => String(input).endsWith('/archive') ? response(archived) : response(renamed)))
    const view = render(<LanguageProvider><ProjectHome project={project} onChange={changed} onOpenRoom={() => {}} /></LanguageProvider>)
    await userEvent.click(screen.getByRole('button', { name: 'Rename project' }))
    await userEvent.clear(screen.getByLabelText('Project name'))
    await userEvent.type(screen.getByLabelText('Project name'), 'Billing')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(changed).toHaveBeenCalledWith(renamed))
    view.rerender(<LanguageProvider><ProjectHome project={renamed} onChange={changed} onOpenRoom={() => {}} /></LanguageProvider>)
    await userEvent.click(screen.getByRole('button', { name: 'Archive Project' }))
    await waitFor(() => expect(changed).toHaveBeenCalledWith(archived))
    expect(screen.getByText('General')).toBeInTheDocument()
  })
})
