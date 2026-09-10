import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { RepositoryResourceView } from '../taskFirstTypes'
import type { ProjectView } from '../types'
import { ProjectList } from './ProjectList'

function repository(repoId: string, name: string): RepositoryResourceView {
  return { repoId, name, localLocator: `/tmp/${name}`, state: 'active', version: 1, availability: 'ready', branch: 'main', head: 'a'.repeat(40), dirty: false }
}

function project(id: string, name: string, repositories: RepositoryResourceView[] = []): ProjectView {
  return {
    id: `project-${id}`, name, state: 'active', version: 1, defaultRoomId: `room-${id}`, repositories,
    rooms: [{ id: `room-${id}`, projectId: `project-${id}`, ownershipKind: 'project', name: 'General', description: '', state: 'active', version: 1, archivedAt: '' }],
    room: { id: `room-${id}`, projectId: `project-${id}`, ownershipKind: 'project', name: 'General', description: '', state: 'active', version: 1, archivedAt: '' },
    lastActivityAt: '2026-09-03T00:00:00Z', taskCounts: { total: 0, open: 0, terminal: 0 },
  }
}

function response(body: unknown, ok = true, status = 200): Response {
  return { ok, status, json: async () => body } as Response
}

function stateFrom(input: string | URL | Request) {
  return new URL(String(input), 'http://localhost').searchParams.get('state')
}

describe('ProjectList', () => {
  beforeEach(() => { window.localStorage.removeItem('chora.locale'); vi.unstubAllGlobals() })

  test('shows a loading state then empty Project creation', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ projects: [], nextCursor: '' })))
    render(<LanguageProvider><ProjectList onOpenProject={() => {}} onAddProject={() => {}} /></LanguageProvider>)
    expect(screen.getByText('Loading projects…')).toBeInTheDocument()
    expect(await screen.findByText(/Create an empty Project/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create Project' })).toBeInTheDocument()
  })

  test('renders empty, single and multi-repository Projects without using a first repository for the multi summary', async () => {
    const items = [
      project('empty', 'Planning'),
      project('single', 'Frontend', [repository('repo-front', 'frontend')]),
      project('multi', 'Platform', [repository('repo-a', 'frontend-api'), repository('repo-b', 'backend-api')]),
    ]
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => response({ projects: stateFrom(input) === 'active' ? items : [], nextCursor: '' })))
    render(<LanguageProvider><ProjectList onOpenProject={() => {}} onAddProject={() => {}} /></LanguageProvider>)
    expect(await screen.findByText('Planning')).toBeInTheDocument()
    expect(screen.getByText('No repositories')).toBeInTheDocument()
    expect(screen.getByText('/tmp/frontend')).toBeInTheDocument()
    expect(screen.getByText('2 repositories')).toBeInTheDocument()
    expect(screen.queryByText('/tmp/frontend-api')).not.toBeInTheDocument()
    expect(screen.queryByText('/tmp/backend-api')).not.toBeInTheDocument()
  })

  test('loads every page for active and archived Projects', async () => {
    const alpha = project('a', 'alpha')
    const beta = project('b', 'beta')
    const retired = { ...project('old', 'retired'), state: 'archived' as const }
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const url = new URL(String(input), 'http://localhost')
      if (url.searchParams.get('state') === 'archived') return response({ projects: [retired], nextCursor: '' })
      if (url.searchParams.get('cursor') === 'page-2') return response({ projects: [beta], nextCursor: '' })
      return response({ projects: [alpha], nextCursor: 'page-2' })
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><ProjectList onOpenProject={() => {}} onAddProject={() => {}} /></LanguageProvider>)
    expect(await screen.findByText('beta')).toBeInTheDocument()
    expect(screen.getByText('Archived Projects · 1')).toBeInTheDocument()
    expect(screen.getByText('retired')).toBeInTheDocument()
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toContain('/api/v2/projects?state=active&cursor=page-2')
  })

  test('filters Project names locally without changing identity queries', async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request) => response({ projects: stateFrom(input) === 'active' ? [project('a', 'alpha'), project('b', 'beta')] : [], nextCursor: '' }))
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><ProjectList onOpenProject={() => {}} onAddProject={() => {}} /></LanguageProvider>)
    expect(await screen.findByText('beta')).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('Filter projects'), 'alpha')
    await waitFor(() => expect(screen.queryByText('beta')).not.toBeInTheDocument())
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  test('retains scalar legacy fixture display when repositories is absent', async () => {
    const legacy = project('legacy', 'Legacy')
    delete legacy.repositories
    legacy.repositoryBinding = {
      roomId: 'room-legacy', name: 'legacy-repo', localLocator: '/tmp/legacy-repo', sourceKind: 'open', cloneUrl: '',
      admittedBase: 'a'.repeat(40), baseIdentity: 'identity', targetWorktree: '/tmp/legacy-repo', dirtyAdmitted: true,
      state: 'active', version: 1, createdAt: '', updatedAt: '',
    }
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => response({ projects: stateFrom(input) === 'active' ? [legacy] : [], nextCursor: '' })))
    render(<LanguageProvider><ProjectList onOpenProject={() => {}} onAddProject={() => {}} /></LanguageProvider>)
    expect(await screen.findByText('/tmp/legacy-repo')).toBeInTheDocument()
    expect(screen.getByText('Dirty')).toBeInTheDocument()
  })

  test('surfaces a list error and keeps Project creation available', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ error: 'project repository source is unavailable' }, false, 503)))
    render(<LanguageProvider><ProjectList onOpenProject={() => {}} onAddProject={() => {}} /></LanguageProvider>)
    expect(await screen.findByRole('alert')).toHaveTextContent('project repository source is unavailable')
    expect(screen.getByRole('button', { name: 'Create Project' })).toBeInTheDocument()
  })

  test('opens a Project and resumes its persisted action independently', async () => {
    const item = project('a', 'alpha')
    item.currentAction = { kind: 'apply_patch', target: { roomId: 'room-a', taskId: 'task-2', runId: 'run-3' }, url: '/rooms/room-a/tasks/task-2/runs/run-3', reason: 'Apply conflict' }
    item.currentTaskTitle = 'Fix login'
    const resume = vi.fn(); const open = vi.fn()
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => response({ projects: stateFrom(input) === 'active' ? [item] : [], nextCursor: '' })))
    render(<LanguageProvider><ProjectList onOpenProject={open} onAddProject={() => {}} onResume={resume} /></LanguageProvider>)
    await userEvent.click(await screen.findByRole('button', { name: 'Resume · Fix login' }))
    expect(resume).toHaveBeenCalledWith('/rooms/room-a/tasks/task-2/runs/run-3')
    expect(open).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: /alpha/ }))
    expect(open).toHaveBeenCalledWith(item)
  })
})
