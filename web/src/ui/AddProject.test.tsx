import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { ProjectView } from '../types'
import { AddProject } from './AddProject'

const emptyProject: ProjectView = {
  id: 'project-1', name: 'Platform', state: 'active', version: 1, defaultRoomId: 'room-1', repositories: [],
  rooms: [{ id: 'room-1', projectId: 'project-1', ownershipKind: 'project', name: 'General', description: 'Shared work', state: 'active', version: 1, archivedAt: '' }],
  room: { id: 'room-1', projectId: 'project-1', ownershipKind: 'project', name: 'General', description: 'Shared work', state: 'active', version: 1, archivedAt: '' },
  lastActivityAt: '2026-09-08T00:00:00Z', taskCounts: { total: 0, open: 0, terminal: 0 },
}

function response(body: unknown, ok = true, status = 200): Response {
  return { ok, status, json: async () => body } as Response
}

describe('AddProject', () => {
  beforeEach(() => { window.localStorage.removeItem('chora.locale'); vi.unstubAllGlobals() })

  function show(onAdded = vi.fn()) {
    render(<LanguageProvider><AddProject onAdded={onAdded} onCancel={() => {}} /></LanguageProvider>)
    return onAdded
  }

  test('offers empty Project creation and a native repository shortcut without a path input', () => {
    show()
    expect(screen.getByLabelText('Project name')).toBeInTheDocument()
    expect(screen.getByLabelText('Project description')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create empty Project' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Choose repository folder…' })).toBeEnabled()
    expect(screen.queryByText(/^\/tmp\//)).not.toBeInTheDocument()
  })

  test('creates an empty Project with name and description through v2', async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      expect(String(input)).toBe('/api/v2/projects')
      expect(init?.method).toBe('POST')
      expect(JSON.parse(String(init?.body))).toEqual({ name: 'Platform', description: 'Frontend and backend' })
      return response(emptyProject)
    })
    vi.stubGlobal('fetch', fetchMock)
    const onAdded = show()
    await userEvent.type(screen.getByLabelText('Project name'), ' Platform ')
    await userEvent.type(screen.getByLabelText('Project description'), ' Frontend and backend ')
    await userEvent.click(screen.getByRole('button', { name: 'Create empty Project' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith(emptyProject))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  test('opens precisely the chosen folder through the existing Git shortcut', async () => {
    const locator = '/tmp/my project $(literal)'
    const linked = { ...emptyProject, repositories: undefined, repositoryBinding: {
      roomId: 'room-1', name: 'my project $(literal)', localLocator: locator, sourceKind: 'open' as const, cloneUrl: '',
      admittedBase: 'a'.repeat(40), baseIdentity: 'identity', targetWorktree: locator, dirtyAdmitted: false,
      state: 'active' as const, version: 1, createdAt: '', updatedAt: '',
    } }
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input) === '/api/projects/choose-directory') return response({ cancelled: false, locator })
      expect(String(input)).toBe('/api/projects')
      expect(JSON.parse(String(init?.body))).toEqual({ locator, name: 'My stack' })
      return response(linked)
    })
    vi.stubGlobal('fetch', fetchMock)
    const onAdded = show()
    await userEvent.click(screen.getByRole('button', { name: 'Choose repository folder…' }))
    expect(await screen.findByText(locator)).toBeInTheDocument()
    await userEvent.clear(screen.getByLabelText('Project name'))
    await userEvent.type(screen.getByLabelText('Project name'), 'My stack')
    await userEvent.click(screen.getByRole('button', { name: 'Create with repository' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith(linked))
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  test('keeps entered metadata when the native chooser is cancelled', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ cancelled: true, locator: '' })))
    const onAdded = show()
    await userEvent.type(screen.getByLabelText('Project name'), 'Chosen name')
    await userEvent.type(screen.getByLabelText('Project description'), 'Keep this')
    await userEvent.click(screen.getByRole('button', { name: 'Choose repository folder…' }))
    expect(screen.getByLabelText('Project name')).toHaveValue('Chosen name')
    expect(screen.getByLabelText('Project description')).toHaveValue('Keep this')
    expect(onAdded).not.toHaveBeenCalled()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  test('reports chooser failures and remains usable', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ error: 'chooser unavailable' }, false, 503)))
    show()
    await userEvent.click(screen.getByRole('button', { name: 'Choose repository folder…' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('chooser unavailable')
    expect(screen.getByRole('button', { name: 'Choose repository folder…' })).toBeEnabled()
  })
})
