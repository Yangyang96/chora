import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'
import NewApp from './newApp'
import { TRUSTED_LOCAL_DISCLOSURE_POLICY, TRUSTED_LOCAL_LABEL } from './ui/AgentExecutionProfile'
import type { PiInstallationView } from './ui/PiInstallation'

function jsonResponse(body: unknown) {
  return { ok: true, status: 200, json: async () => body }
}

function errorResponse(status: number, error: string) {
  return { ok: false, status, json: async () => ({ error }) }
}

const roomSummary = {
  id: 'room-1', name: 'Lane Room', description: '', state: 'active', version: 1, archivedAt: '', lastActivityAt: '2026-08-20T00:00:00Z',
  taskCounts: { total: 0, open: 0, terminal: 0 }, humanActionRequired: false,
}
const roomRef = {
  ...roomSummary,
  revisions: [{ id: 'rev-1', title: 'Room Brief', body: '', digest: 'd1', provenance: { kind: 'human_room', actor: 'local' }, confirmedAt: '2026-08-20T00:00:00Z' }],
}
const taskSummary = (archived = false) => ({
  id: 'task-1', title: 'Add a sort button', status: 'open', archived, lastActivityAt: '2026-08-20T00:00:00Z',
  currentPlanRevision: null, latestRun: null, runCount: 1,
  currentAction: { kind: 'none', target: {}, url: '', reason: '' },
})

const piDiscoveryReady = {
  state: 'ready', version: '0.84.2', executablePath: '/usr/local/bin/pi', executableSha256: 'a'.repeat(64),
  readyProviders: ['ollama'], notReadyProviders: ['google'],
}

const piInstallationMissing = {
  available: true, active: false, restartRequired: false,
  state: { code: 'missing', stateVersion: 0, destinationPath: '/fixture/data/pi', selectionPresent: false, configured: false, updatedAt: '2026-09-09T00:00:00Z' },
} satisfies PiInstallationView

describe('NewApp', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    window.localStorage.removeItem('chora.locale')
  })

  test('shows the Project home and creates a raw Room through the sidebar', async () => {
    window.history.replaceState({}, '', '/')
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'GET' && path === '/api/v2/projects?state=active') return jsonResponse({ projects: [] })
      if (method === 'GET' && path === '/api/v2/projects?state=archived') return jsonResponse({ projects: [] })
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [], archivedRooms: [] })
      if (method === 'POST' && path === '/api/rooms') return jsonResponse(roomRef)
      if (method === 'GET' && path === '/api/rooms/room-1') return jsonResponse(roomRef)
      if (method === 'GET' && path === '/api/rooms/room-1/workspace') return jsonResponse({ room: roomSummary, tasks: [] })
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)

    expect(await screen.findByText(/No projects yet/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'New Room' }))
    await userEvent.type(screen.getByLabelText('Room name'), 'Lane Room')
    await userEvent.click(screen.getByRole('button', { name: 'Create Room' }))
    expect(await screen.findByText('This Room has no Tasks yet.')).toBeInTheDocument()
  })

  test('adds a project and navigates into its room', async () => {
    window.history.replaceState({}, '', '/')
    const projectView = {
      id: 'project-demo', name: 'demo', state: 'active', version: 1, defaultRoomId: 'room-project',
      rooms: [{ id: 'room-project', projectId: 'project-demo', ownershipKind: 'project', name: 'General', description: '', state: 'active', version: 1, archivedAt: '' }],
      room: { id: 'room-project', projectId: 'project-demo', ownershipKind: 'project', name: 'General', description: '', workspaceRoot: '/tmp/demo', state: 'active', version: 1, archivedAt: '' },
      repositoryBinding: {
        roomId: 'room-project', name: 'demo', localLocator: '/tmp/demo', sourceKind: 'open', cloneUrl: '',
        admittedBase: 'a'.repeat(40), baseIdentity: `sha:${'a'.repeat(40)}:tree:${'b'.repeat(40)}`, targetWorktree: '/tmp/demo',
        dirtyAdmitted: false, state: 'active', version: 1, createdAt: '2026-09-03T00:00:00Z', updatedAt: '2026-09-03T00:00:00Z',
      },
      lastActivityAt: '2026-09-03T00:00:00Z',
      taskCounts: { total: 0, open: 0, terminal: 0 },
    }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'GET' && path === '/api/v2/projects?state=active') return jsonResponse({ projects: [] })
      if (method === 'GET' && path === '/api/v2/projects?state=archived') return jsonResponse({ projects: [] })
      if (method === 'POST' && path === '/api/projects/choose-directory') return jsonResponse({ cancelled: false, locator: '/tmp/demo' })
      if (method === 'POST' && path === '/api/projects') return jsonResponse(projectView)
      if (method === 'GET' && path === '/api/v2/projects/project-demo') return jsonResponse(projectView)
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-project') return jsonResponse({ id: 'room-project', name: 'demo', description: '', state: 'active', version: 1, archivedAt: '', revisions: [] })
      if (method === 'GET' && path === '/api/rooms/room-project/workspace') return jsonResponse({ room: { ...roomSummary, id: 'room-project', name: 'demo' }, tasks: [] })
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)

    await screen.findByText(/No projects yet/)
    await userEvent.click(screen.getByRole('button', { name: 'Create Project' }))
    await userEvent.click(screen.getByRole('button', { name: 'Choose repository folder…' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Create with repository' }))

    expect((await screen.findAllByText('General')).length).toBeGreaterThan(0)
    expect(window.location.pathname).toBe('/projects/project-demo')
  })

  test('archives and restores a Task', async () => {
    window.history.replaceState({}, '', '/rooms/room-1')
    let archived = false
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1') return jsonResponse(roomRef)
      if (method === 'GET' && path === '/api/rooms/room-1/workspace') return jsonResponse({ room: roomSummary, tasks: [taskSummary(archived)] })
      if (method === 'POST' && path === '/api/tasks/task-1/archive') {
        archived = true
        return jsonResponse({ id: 'task-1', archived: true, archivedAt: '2026-08-20T00:00:00Z' })
      }
      if (method === 'POST' && path === '/api/tasks/task-1/restore') {
        archived = false
        return jsonResponse({ id: 'task-1', archived: false, archivedAt: '' })
      }
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)

    expect(await screen.findByText('Add a sort button')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Archive' }))
    expect(await screen.findByText(/Archived · 1/)).toBeInTheDocument()

    await userEvent.click(screen.getByText(/Archived · 1/))
    await userEvent.click(screen.getByRole('button', { name: 'Restore' }))
    await waitFor(() => expect(screen.queryByText(/Archived · 1/)).not.toBeInTheDocument())
  })

  test('creates normal Tasks with the Standard Agent profile and never exposes the diagnostic selector', async () => {
    window.history.replaceState({}, '', '/rooms/room-1')
    const requests: Array<{ method: string; path: string; body?: Record<string, unknown> }> = []
    const createdTask = { id: 'task-profile', agentExecutionProfile: 'standard', planning: { revisions: [] } }
    const updatedBriefRoom = { ...roomRef, revisions: [
      { ...roomRef.revisions[0], locator: 'room://room-1/brief', revisionNumber: 1 },
      { ...roomRef.revisions[0], id: 'rev-2', body: 'Newest topic context', locator: 'room://room-1/brief', revisionNumber: 2 },
      { ...roomRef.revisions[0], id: 'candidate-3', title: 'Reviewed idea', provenance: { kind: 'candidate', actor: 'local' }, revisionNumber: 3 },
    ] }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
      requests.push({ method, path, body })
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1') return jsonResponse(updatedBriefRoom)
      if (method === 'GET' && path === '/api/rooms/room-1/workspace') return jsonResponse({ room: roomSummary, tasks: [] })
      if (method === 'POST' && path === '/api/rooms/room-1/tasks') return jsonResponse(createdTask)
      if (method === 'GET' && path === '/api/rooms/room-1/tasks/task-profile') return jsonResponse(createdTask)
      if (method === 'GET' && path === '/api/rooms/room-1/tasks/task-profile/runs') return jsonResponse({ roomId: 'room-1', taskId: 'task-profile', runs: [] })
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)
    await userEvent.click((await screen.findAllByRole('button', { name: '＋ New Task' }))[0])
    expect(screen.getByRole('radio', { name: 'Standard' })).toBeChecked()
    expect(screen.queryByText('Diagnostic Fake')).not.toBeInTheDocument()
    expect(screen.queryByText('Real Spec Coding')).not.toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('What should Chora build?'), 'Implement the frozen profile contract')
    await userEvent.click(screen.getByRole('button', { name: 'Start' }))

    await waitFor(() => expect(requests.some((request) => request.method === 'POST' && request.path === '/api/rooms/room-1/tasks')).toBe(true))
    const create = requests.find((request) => request.method === 'POST' && request.path === '/api/rooms/room-1/tasks')
    expect(create?.body).toEqual({
      title: 'Implement the frozen profile contract',
      goal: 'Implement the frozen profile contract',
      criteria: ['Requirement satisfied'],
      revisionIds: ['rev-2'],
      agentExecutionProfile: 'standard',
    })
    expect(create?.body).not.toHaveProperty('executionProfile')
  })

  test('reload uses only an exact public current acknowledgement to suppress disclosure', async () => {
    window.history.replaceState({}, '', '/rooms/room-1')
    const posts: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'POST') posts.push(path)
      if (method === 'GET' && path === '/api/agent-execution/trusted-local-acknowledgements/current') {
        return jsonResponse({ acknowledged: true, policyVersion: TRUSTED_LOCAL_DISCLOSURE_POLICY, acknowledgedAt: '2026-08-27T00:00:00Z' })
      }
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (method === 'GET' && path === '/api/pi/discovery') return jsonResponse(piDiscoveryReady)
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1') return jsonResponse(roomRef)
      if (method === 'GET' && path === '/api/rooms/room-1/workspace') return jsonResponse({ room: roomSummary, tasks: [] })
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)
    await userEvent.click((await screen.findAllByRole('button', { name: '＋ New Task' }))[0])
    const trusted = screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL })
    await waitFor(() => expect(trusted).toBeEnabled())
    await userEvent.click(trusted)

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(trusted).toBeChecked()
    expect(posts).toHaveLength(0)
  })

  test.each([
    ['missing acknowledgement', jsonResponse({ acknowledged: false, policyVersion: TRUSTED_LOCAL_DISCLOSURE_POLICY })],
    ['stale acknowledgement after a policy bump', jsonResponse({ acknowledged: true, policyVersion: 'chora.trusted-local-disclosure.v0', acknowledgedAt: '2026-08-26T00:00:00Z' })],
    ['malformed acknowledgement', jsonResponse({ acknowledged: true })],
    ['current-state request error', errorResponse(503, 'acknowledgement state unavailable')],
  ])('%s remains fail-closed and prompts', async (_name, currentResponse) => {
    window.history.replaceState({}, '', '/rooms/room-1')
    const posts: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'POST') posts.push(path)
      if (method === 'GET' && path === '/api/agent-execution/trusted-local-acknowledgements/current') return currentResponse
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (method === 'GET' && path === '/api/pi/discovery') return jsonResponse(piDiscoveryReady)
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1') return jsonResponse(roomRef)
      if (method === 'GET' && path === '/api/rooms/room-1/workspace') return jsonResponse({ room: roomSummary, tasks: [] })
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)
    await userEvent.click((await screen.findAllByRole('button', { name: '＋ New Task' }))[0])
    const trusted = screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL })
    await waitFor(() => expect(trusted).toBeEnabled())
    await userEvent.click(trusted)

    expect(screen.getByRole('dialog', { name: TRUSTED_LOCAL_LABEL })).toBeInTheDocument()
    expect(posts).toHaveLength(0)
  })

  test('acknowledges the exact Trusted Local disclosure before creating that profile', async () => {
    window.history.replaceState({}, '', '/rooms/room-1')
    const requests: Array<{ method: string; path: string; body?: Record<string, unknown>; headers?: HeadersInit }> = []
    const createdTask = { id: 'task-trusted', agentExecutionProfile: 'trusted_local', planning: { revisions: [] } }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
      requests.push({ method, path, body, headers: init?.headers })
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (method === 'GET' && path === '/api/pi/discovery') return jsonResponse(piDiscoveryReady)
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1') return jsonResponse(roomRef)
      if (method === 'GET' && path === '/api/rooms/room-1/workspace') return jsonResponse({ room: roomSummary, tasks: [] })
      if (method === 'POST' && path === '/api/agent-execution/trusted-local-acknowledgements') {
        return jsonResponse({ policyVersion: TRUSTED_LOCAL_DISCLOSURE_POLICY, actorId: 'owner', sessionId: 'session', acknowledgedAt: '2026-08-27T00:00:00Z', replayed: false })
      }
      if (method === 'POST' && path === '/api/rooms/room-1/tasks') return jsonResponse(createdTask)
      if (method === 'GET' && path === '/api/rooms/room-1/tasks/task-trusted') return jsonResponse(createdTask)
      if (method === 'GET' && path === '/api/rooms/room-1/tasks/task-trusted/runs') return jsonResponse({ roomId: 'room-1', taskId: 'task-trusted', runs: [] })
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)
    await userEvent.click((await screen.findAllByRole('button', { name: '＋ New Task' }))[0])
    await userEvent.type(screen.getByLabelText('What should Chora build?'), 'Use the local Pi profile')
    await waitFor(() => expect(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL })).toBeEnabled())
    await userEvent.click(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL }))
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(requests.filter((request) => request.method === 'POST')).toHaveLength(0)
    expect(screen.getByRole('radio', { name: 'Standard' })).not.toBeChecked()

    await userEvent.click(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL }))
    await userEvent.click(screen.getByRole('button', { name: 'Acknowledge and use Trusted Local' }))
    expect(await screen.findByRole('radio', { name: TRUSTED_LOCAL_LABEL })).toBeChecked()
    await userEvent.click(screen.getByRole('radio', { name: 'Standard' }))
    await userEvent.click(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(requests.filter((request) => request.path === '/api/agent-execution/trusted-local-acknowledgements')).toHaveLength(1)
    await userEvent.click(screen.getByRole('button', { name: 'Start' }))

    await waitFor(() => expect(requests.filter((request) => request.method === 'POST')).toHaveLength(2))
    const acknowledgement = requests.find((request) => request.path === '/api/agent-execution/trusted-local-acknowledgements')
    expect(acknowledgement?.body).toEqual({ policyVersion: TRUSTED_LOCAL_DISCLOSURE_POLICY })
    expect(acknowledgement?.headers).toEqual(expect.objectContaining({ 'Idempotency-Key': expect.any(String) }))
    expect(requests.find((request) => request.path === '/api/rooms/room-1/tasks')?.body).toEqual(expect.objectContaining({ agentExecutionProfile: 'trusted_local' }))
  })

  test('acknowledgement conflict leaves local execution unselected and performs no Task create', async () => {
    window.history.replaceState({}, '', '/rooms/room-1')
    const posts: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'POST') posts.push(path)
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (method === 'GET' && path === '/api/pi/discovery') return jsonResponse(piDiscoveryReady)
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1') return jsonResponse(roomRef)
      if (method === 'GET' && path === '/api/rooms/room-1/workspace') return jsonResponse({ room: roomSummary, tasks: [] })
      if (method === 'POST' && path === '/api/agent-execution/trusted-local-acknowledgements') return errorResponse(409, 'disclosure policy conflict')
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)
    await userEvent.click((await screen.findAllByRole('button', { name: '＋ New Task' }))[0])
    await waitFor(() => expect(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL })).toBeEnabled())
    await userEvent.click(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL }))
    await userEvent.click(screen.getByRole('button', { name: 'Acknowledge and use Trusted Local' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('disclosure policy conflict')
    expect(screen.getByRole('radio', { name: 'Standard' })).not.toBeChecked()
    expect(posts).toEqual(['/api/agent-execution/trusted-local-acknowledgements'])
  })

  test('keeps Trusted Local disabled and shows the reason when Pi discovery is not ready', async () => {
    window.history.replaceState({}, '', '/rooms/room-1')
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'GET' && path === '/api/agent-execution/trusted-local-acknowledgements/current') {
        return jsonResponse({ acknowledged: false, policyVersion: TRUSTED_LOCAL_DISCLOSURE_POLICY })
      }
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (method === 'GET' && path === '/api/pi/discovery') {
        return jsonResponse({ state: 'unconfigured', reason: 'no configured Pi provider answered ready', readyProviders: [], notReadyProviders: ['ollama'] })
      }
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1') return jsonResponse(roomRef)
      if (method === 'GET' && path === '/api/rooms/room-1/workspace') return jsonResponse({ room: roomSummary, tasks: [] })
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)
    await userEvent.click((await screen.findAllByRole('button', { name: '＋ New Task' }))[0])

    const trusted = screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL })
    await waitFor(() => expect(trusted).toBeDisabled())
    expect(await within(screen.getByRole('main')).findByText('Pi is not configured')).toBeInTheDocument()
    expect(within(screen.getByRole('main')).getByText('no configured Pi provider answered ready')).toBeInTheDocument()
    // Unconfigured local Pi must not fall back to Docker profiles.
    expect(screen.getByRole('radio', { name: 'Minimal' })).toBeDisabled()
    expect(screen.getByRole('radio', { name: 'Standard' })).toBeDisabled()
    expect(screen.getByRole('radio', { name: 'Standard' })).not.toBeChecked()
  })

  test('profile-switch conflict preserves the current Run and never turns into Retry', async () => {
    window.history.replaceState({}, '', '/rooms/room-1/tasks/task-1/runs/run-profile')
    const run = {
      id: 'run-profile', status: 'revision_required', version: 11, attempt: 2, adapter: 'pi',
      agentExecution: {
        profile: 'standard', runtimeSource: 'managed_pi_image', executionProvider: 'docker', capabilityPolicy: 'chora.standard.v1',
        trustDisclosurePolicy: '', sandboxed: true, disclosureLabel: 'Standard',
      },
      room: { id: 'room-1', name: 'Lane Room', description: '' },
      task: { id: 'task-1', title: 'Profile switch', goal: 'Switch only through a successor' },
      context: [], timeline: [], artifacts: [], unknowns: [], criteria: [], trajectory: [],
      controls: { canCancel: false, canRetry: true, canReview: false, canSwitchAgentExecutionProfile: true },
    }
    const posts: Array<{ path: string; body: Record<string, unknown>; headers?: HeadersInit }> = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1/tasks/task-1/runs/run-profile') return jsonResponse(run)
      if (method === 'POST') {
        posts.push({ path, body: JSON.parse(String(init?.body)), headers: init?.headers })
        if (path === '/api/runs/run-profile/agent-execution-profile') return errorResponse(409, 'stale profile version')
      }
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)
    expect((await screen.findAllByText('Standard')).length).toBeGreaterThan(0)
    await userEvent.click(screen.getByRole('radio', { name: 'Minimal' }))
    await userEvent.click(screen.getByRole('button', { name: 'Create successor with selected profile' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('stale profile version')
    expect(posts).toEqual([expect.objectContaining({
      path: '/api/runs/run-profile/agent-execution-profile',
      body: { profile: 'minimal', reason: 'Use this profile for a fresh successor Attempt.', expectedVersion: 11 },
      headers: expect.objectContaining({ 'Idempotency-Key': expect.any(String) }),
    })])
    expect(screen.getByRole('radio', { name: 'Standard' })).toBeChecked()
    expect(posts.some((request) => request.path.endsWith('/retry'))).toBe(false)
  })

  test('accepts and applies a Local Connected Agent-reported result without claiming verification', async () => {
    window.history.replaceState({}, '', '/rooms/room-1/tasks/task-1/runs/run-1')
    const baseRun = {
      id: 'run-1', status: 'awaiting_review', version: 6, attempt: 1, adapter: 'pi',
      room: { id: 'room-1', name: 'Lane Room', description: '' },
      task: { id: 'task-1', title: 'Add a sort button', goal: 'Add a sort button', worktree: { locator: 'chora/task-1-add-a-sort-button', state: 'ready' } },
      context: [], timeline: [], artifacts: [], unknowns: [],
      verificationDisposition: { state: 'not_applicable', reason: 'Local Connected uses Agent-reported checks for this review.' },
      agentReport: {
        id: 'report-1', attemptId: 'attempt-1', summary: 'The Agent reports that sorting works.', finalText: 'Done',
        completedAt: '2026-09-03T00:00:00Z', authority: 'non_authoritative_agent_claim',
        claimedChecks: [{ criterionId: 'criterion-1', status: 'passed', evidence: 'The Agent ran the focused sort test.' }],
      },
      criteria: [{ id: 'criterion-1', title: 'Sort works', status: 'passed', evidence: 'Legacy Agent claim (non-authoritative): focused sort test passed' }],
      reviewablePatch: {
        evidenceKind: 'agent_reported', agentReportId: 'report-1',
        patchDigest: 'a'.repeat(64), baselineDigest: 'b'.repeat(64), declaredFilesDigest: 'c'.repeat(64),
        resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: 'verification-attempt-1', artifactId: 'artifact-1', rawDownload: '/patch',
        files: [{ path: 'internal/domain/task.go', lines: [{ kind: 'added', newLine: 1, text: 'change' }] }],
      },
      controls: { canCancel: false, canRetry: false, canReview: true, canAcceptAndApply: true, canApplyPatch: false },
    }
    const accepted = { ...baseRun, status: 'accepted', version: 7, controls: { ...baseRun.controls, canReview: false, canApplyPatch: true } }
    const applied = {
      ...accepted,
      patchApplication: {
        state: 'applied', version: 2, patchDigest: 'a'.repeat(64), targetIdentity: `sha256:${'d'.repeat(64)}`,
        baseRevision: '67b83d9', affectedPaths: ['internal/domain/task.go'], preStateDigest: 'e'.repeat(64), postStateDigest: 'f'.repeat(64),
        startedAt: '2026-08-24T00:00:00Z', updatedAt: '2026-08-24T00:00:01Z', appliedAt: '2026-08-24T00:00:01Z',
      },
      controls: { ...accepted.controls, canApplyPatch: false },
    }
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1/tasks/task-1/runs/run-1') return jsonResponse(baseRun)
      if (method === 'POST' && path === '/api/runs/run-1/review') return jsonResponse(accepted)
      if (method === 'POST' && path === '/api/runs/run-1/apply') return jsonResponse(applied)
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    })
    vi.stubGlobal('fetch', fetchMock)

	render(<NewApp />)

	expect(await screen.findByText(/chora\/task-1-add-a-sort-button/)).toBeInTheDocument()
	expect(screen.getByText('Independent verification was not run')).toBeInTheDocument()
	expect(screen.getAllByText('Reviewable patch').length).toBeGreaterThan(0)
	expect(screen.queryByText('Verified patch')).not.toBeInTheDocument()
	expect(screen.getByText('Agent-reported review · planned versus actual')).toBeInTheDocument()
	await userEvent.click(await screen.findByRole('button', { name: 'Accept & Apply' }))
    expect((await screen.findAllByText('Written to repository')).length).toBeGreaterThan(0)
    expect(screen.getByText(/without staging or committing/)).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledWith('/api/runs/run-1/review', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ expectedVersion: 6, kind: 'accept', comment: 'Accepted the digest-bound reviewable Patch and Agent-reported evidence.' }),
    }))
    expect(fetchMock).toHaveBeenCalledWith('/api/runs/run-1/apply', expect.objectContaining({ method: 'POST' }))
  })

  test('rejects a Run and retries the Agent with the recorded rejection instructions', async () => {
    window.history.replaceState({}, '', '/rooms/room-1/tasks/task-1/runs/run-1')
    const baseRun = {
      id: 'run-1', status: 'awaiting_review', version: 6, attempt: 1, adapter: 'pi',
      room: { id: 'room-1', name: 'Lane Room', description: '' },
      task: { id: 'task-1', title: 'Add a sort button', goal: 'Add a sort button' },
      context: [], timeline: [], artifacts: [], unknowns: [],
      criteria: [{ id: 'criterion-1', title: 'Sort works', status: 'passed', evidence: 'Independent Verifier evidence: ev-1' }],
      reviewablePatch: {
        patchDigest: 'a'.repeat(64), baselineDigest: 'b'.repeat(64), declaredFilesDigest: 'c'.repeat(64),
        resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: 'verification-attempt-1', artifactId: 'artifact-1', rawDownload: '/patch',
        files: [{ path: 'internal/domain/task.go', lines: [{ kind: 'added', newLine: 1, text: 'change' }] }],
      },
      controls: { canCancel: false, canRetry: false, canReview: true, canAcceptAndApply: false },
    }
    const rejected = { ...baseRun, status: 'revision_required', version: 7, controls: { ...baseRun.controls, canReview: false, canRetry: true } }
    const retried = { ...rejected, status: 'running', version: 8, controls: { ...rejected.controls, canRetry: false } }
    const posts: Array<{ path: string; body: Record<string, unknown> }> = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1/tasks/task-1/runs/run-1') return jsonResponse(baseRun)
      if (method === 'POST' && path === '/api/runs/run-1/review') {
        posts.push({ path, body: JSON.parse(String(init?.body)) })
        return jsonResponse(rejected)
      }
      if (method === 'POST' && path === '/api/runs/run-1/retry') {
        posts.push({ path, body: JSON.parse(String(init?.body)) })
        return jsonResponse(retried)
      }
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)

    await userEvent.click(await screen.findByRole('button', { name: 'Ask Agent to fix' }))

    await waitFor(() => expect(posts.some((post) => post.path === '/api/runs/run-1/retry')).toBe(true))
    const review = posts.find((post) => post.path === '/api/runs/run-1/review')
    const retry = posts.find((post) => post.path === '/api/runs/run-1/retry')
    expect(review?.body).toEqual({ expectedVersion: 6, kind: 'reject', comment: 'Rejected: implementation_gap', rejectionClass: 'implementation_gap' })
    expect(retry?.body).toEqual({ expectedVersion: 7, instructions: 'Rejected: implementation_gap' })
  })

  test('finishes diagnostic Fake Runs without attempting independent verification', async () => {
    window.history.replaceState({}, '', '/rooms/room-1/tasks/task-1/runs/run-1')
    const run = {
      id: 'run-1', status: 'awaiting_verification', version: 4, attempt: 1, adapter: 'fake',
      room: { id: 'room-1', name: 'Lane Room', description: '' },
      task: { id: 'task-1', title: 'Diagnostic task', goal: 'Inspect local behavior' },
      context: [], artifacts: [], unknowns: [], criteria: [], trajectory: [],
      timeline: [{ sequence: 8, type: 'run.awaiting_verification', title: 'Awaiting verification', detail: 'No Result exists yet.', time: '2026-08-25T04:17:22Z' }],
      activity: { phase: 'awaiting_verification', currentAction: 'Waiting', elapsedMs: 240_000 },
      verificationDisposition: {
        state: 'not_applicable',
        reason: 'Independent verification applies only to registered Spec Coding Tasks; this diagnostic Agent Run is complete without a Verification Result.',
      },
      controls: { canCancel: false, canRetry: false, canReview: false, canStartVerification: false },
    }
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1/tasks/task-1/runs/run-1') return jsonResponse(run)
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<NewApp />)

    expect(await screen.findByText('Agent finished')).toBeInTheDocument()
    expect(screen.getByText('Independent verification was not run')).toBeInTheDocument()
    expect(screen.queryByText('Independent verification starts automatically.')).not.toBeInTheDocument()
    expect(screen.queryByText(/240/)).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([, init]) => (init?.method ?? 'GET').toUpperCase() === 'POST')).toBe(false)
  })

  test('reviews one changed file at a time and inspects observable Agent steps', async () => {
    window.history.replaceState({}, '', '/rooms/room-1/tasks/task-1/runs/run-1')
    const run = {
      id: 'run-1', status: 'awaiting_review', version: 6, attempt: 1, adapter: 'pi',
      room: { id: 'room-1', name: 'Lane Room', description: '' },
      task: { id: 'task-1', title: 'hello', goal: 'hello' },
      context: [], artifacts: [], unknowns: [], criteria: [],
      timeline: [],
      agentReport: {
        id: 'report-1', attemptId: 'attempt-1', summary: 'Implemented the greeting and its focused test.',
        finalText: 'Added Hello and a focused test. The domain package passes.', completedAt: '2026-08-24T10:14:40Z',
        authority: 'non_authoritative', claimedChecks: [],
      },
      trajectory: [
        { sequence: 5, endSequence: 7, source: 'Agent', kind: 'read', title: 'Read file', summary: 'internal/domain/task.go', status: 'completed', startedAt: '2026-08-24T10:14:03Z', endedAt: '2026-08-24T10:14:04Z', durationMs: 1000, details: [{ label: 'File', value: 'internal/domain/task.go' }, { label: 'Tool', value: 'read' }] },
        { sequence: 15, endSequence: 16, source: 'Agent', kind: 'command', title: 'Ran command', summary: 'go test ./internal/domain', status: 'completed', startedAt: '2026-08-24T10:14:19Z', endedAt: '2026-08-24T10:14:33Z', durationMs: 14000, details: [{ label: 'Command', value: 'go test ./internal/domain' }] },
        { sequence: 20, source: 'Verifier', kind: 'check', title: 'Independent verification passed', summary: 'All criterion-bound checks passed; the exact Patch is ready for review.', status: 'completed', startedAt: '2026-08-24T10:14:54Z', details: [{ label: 'Outcome', value: 'review_ready' }] },
      ],
      reviewablePatch: {
        patchDigest: 'a'.repeat(64), baselineDigest: 'b'.repeat(64), declaredFilesDigest: 'c'.repeat(64),
        resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: 'verification-attempt-1', artifactId: 'artifact-1', rawDownload: '/patch',
        files: [
          {
            path: 'internal/domain/task.go', additions: 3, deletions: 0,
            hunks: [{ header: '@@ -22,3 +22,6 @@', oldStart: 22, oldCount: 3, newStart: 22, newCount: 6, lines: [
              { kind: 'context', oldLine: 22, newLine: 22, text: 'func (criterion AcceptanceCriterion) Description() string' },
              { kind: 'added', newLine: 23, text: '// Hello returns the canonical greeting.' },
              { kind: 'added', newLine: 24, text: 'func Hello() string { return "hello" }' },
            ] }],
            lines: [{ kind: 'added', newLine: 24, text: 'func Hello() string { return "hello" }' }],
          },
          {
            path: 'internal/domain/task_test.go', additions: 6, deletions: 0,
            hunks: [{ header: '@@ -6,3 +6,9 @@', oldStart: 6, oldCount: 3, newStart: 6, newCount: 9, lines: [
              { kind: 'context', oldLine: 6, newLine: 6, text: ')' },
              { kind: 'added', newLine: 7, text: 'func TestHello(t *testing.T) {' },
              { kind: 'added', newLine: 8, text: '\tif Hello() != "hello" { t.Fatal("wrong greeting") }' },
            ] }],
            lines: [{ kind: 'added', newLine: 7, text: 'func TestHello(t *testing.T) {' }],
          },
        ],
      },
      controls: { canCancel: false, canRetry: false, canReview: true, canAcceptAndApply: false },
    }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      const method = (init?.method ?? 'GET').toUpperCase()
      if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
      if (method === 'GET' && path === '/api/rooms/room-1/tasks/task-1/runs/run-1') return jsonResponse(run)
      throw new Error(`Unexpected fetch: ${method} ${path}`)
    }))

    render(<NewApp />)

    const conversation = await screen.findByRole('region', { name: 'Visible conversation' })
    expect(within(conversation).getByText('hello')).toBeInTheDocument()
    expect(within(conversation).getByText('Added Hello and a focused test. The domain package passes.')).toBeInTheDocument()
    expect(within(conversation).getByText(/not a chain-of-thought transcript/)).toBeInTheDocument()

    expect(await screen.findByText('func Hello() string { return "hello" }')).toBeInTheDocument()
    expect(screen.queryByText('func TestHello(t *testing.T) {')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /task_test\.go/ }))
    expect(screen.getByText('func TestHello(t *testing.T) {')).toBeInTheDocument()
    expect(screen.queryByText('func Hello() string { return "hello" }')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Split' }))
    expect(screen.getByText('Before')).toBeInTheDocument()
    expect(screen.getByText('After')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('listitem', { name: /Read file/ }))
    const inspector = screen.getByLabelText('Selected trajectory step details')
    expect(within(inspector).getByText('Agent')).toBeInTheDocument()
    expect(within(inspector).getAllByText('internal/domain/task.go')).toHaveLength(2)
    expect(within(inspector).getByText('1.0 s')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '中文' }))
    expect(window.localStorage.getItem('chora.locale')).toBe('zh-CN')
    expect(screen.getByText('发生了什么')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: '可见对话' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '并排视图' })).toBeInTheDocument()
    expect(screen.getByText('修改前')).toBeInTheDocument()
    expect(screen.getByText('修改后')).toBeInTheDocument()
  })
})

 test('opening an unstarted Task after reload is read-only until explicit Start Run', async () => {
    window.history.replaceState({}, '', '/rooms/room-1/tasks/task-1')
    const posts: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
      if (init?.method === 'POST') posts.push(path)
      if (path.includes('/continuity')) return jsonResponse({ available: false })
      if (path === '/api/rooms') return jsonResponse({ activeRooms: [], archivedRooms: [] })
      if (path.endsWith('/tasks/task-1/runs')) return jsonResponse({ roomId: 'room-1', taskId: 'task-1', runs: [] })
      if (path === '/api/rooms/room-1') return jsonResponse({ ...roomRef, ownershipKind: 'legacy_standalone' })
      if (path.endsWith('/tasks/task-1')) return jsonResponse({ id: 'task-1', roomId: 'room-1', title: 'Saved task', planning: { revisions: [], draft: { id: 'draft-1', editVersion: 1, content: { technical_steps: ['Saved step'], decisions: [], risks: [], unknowns: [] } } } })
      return init?.method === 'POST' ? jsonResponse({}) : errorResponse(404, 'unavailable test surface')
    }))
    render(<NewApp />)
    expect(await screen.findByRole('button', { name: 'Start Run' })).toBeEnabled()
    expect(posts).toEqual([])
    await userEvent.click(screen.getByRole('button', { name: 'Start Run' }))
    expect(posts).toContain('/api/tasks/task-1/plan/drafts/draft-1/submit')
  })

 test('accepted successor Plan Resume stays on its Task URL despite prior Run history', async () => {
    const path = '/rooms/room-1/tasks/task-1'
    window.history.replaceState({}, '', path)
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/pi/installation') return jsonResponse(piInstallationMissing)
      if (url === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
      calls.push(url)
      if (url.includes('/continuity')) return jsonResponse({ available: false })
      if (url === '/api/rooms') return jsonResponse({ activeRooms: [], archivedRooms: [] })
      if (url === '/api/rooms/room-1') return jsonResponse({ ...roomRef, ownershipKind: 'legacy_standalone' })
      if (url === '/api/rooms/room-1/tasks/task-1') return jsonResponse({ id: 'task-1', planning: {
        acceptance: { revisionId: 'successor-plan' },
        revisions: [{ id: 'successor-plan', review: { kind: 'accept' }, content: { technical_steps: ['New step'], decisions: [], risks: [], unknowns: [] } }],
      } })
      if (url.endsWith('/runs')) return jsonResponse({ runs: [{ id: 'old-run', createdAt: '2026-09-03T00:00:00Z' }] })
      return errorResponse(404, 'unavailable test surface')
    }))
    render(<NewApp />)
    expect(await screen.findByRole('button', { name: 'Start Run' })).toBeEnabled()
    expect(window.location.pathname).toBe(path)
    expect(calls.some((url) => url.endsWith('/runs') || url.includes('old-run'))).toBe(false)
  })


test.each([{ state: 'ready', readyProviders: ['provider'], notReadyProviders: [] }, {}])('blocked Standard task recovers without execution for discovery %j', async (discovery) => {
  window.history.replaceState({}, '', '/rooms/room-1/tasks/blocked-task')
  const posts: string[] = []
  const requirement = 'Add coverage for only one existing file'
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    if (init?.method === 'POST') posts.push(path)
    if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
    if (path === '/api/pi/discovery') return jsonResponse(discovery)
    if (path.includes('/continuity')) return jsonResponse({ available: false })
    if (path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
    if (path === '/api/rooms/room-1/tasks/blocked-task') return jsonResponse({ id: 'blocked-task', title: 'Short title', goal: requirement, executionProfile: 'real_spec_coding', agentExecutionProfile: 'standard', planning: { revisions: [] } })
    if (path === '/api/rooms/room-1') return jsonResponse(roomRef)
    if (path === '/api/rooms/room-1/workspace') return jsonResponse({ room: roomSummary, tasks: [] })
    return errorResponse(404, 'fixture unavailable')
  }))
  render(<NewApp />)
  await userEvent.click(await screen.findByRole('button', { name: 'Reuse requirement and choose execution mode' }))
  expect(await screen.findByLabelText('What should Chora build?')).toHaveValue(requirement)
  expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
  expect(posts).toEqual([])
})

test('cross-Project navigation never exposes the previous Project while the next owner is delayed or fails', async () => {
  window.history.replaceState({}, '', '/projects/project-a')
  const room = { id: 'room-a', projectId: 'project-a', ownershipKind: 'project', name: 'General', description: '', state: 'active', version: 1, archivedAt: '' }
  const projectA = {
    id: 'project-a', name: 'Project A', state: 'active', version: 1, defaultRoomId: room.id, rooms: [room], room,
    repositoryBinding: { roomId: room.id, name: 'repo-a', localLocator: '/tmp/a', sourceKind: 'open', cloneUrl: '', admittedBase: 'a'.repeat(40), baseIdentity: 'a', targetWorktree: '/tmp/a', dirtyAdmitted: false, state: 'active', version: 1, createdAt: '', updatedAt: '' },
    lastActivityAt: '', taskCounts: { total: 0, open: 0, terminal: 0 },
  }
  let finishProjectB!: (response: ReturnType<typeof jsonResponse>) => void
  const delayedProjectB = new Promise<ReturnType<typeof jsonResponse>>((resolve) => { finishProjectB = resolve })
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input)
    if (path === '/api/v2/projects/project-a') return jsonResponse(projectA)
    if (path === '/api/v2/projects/project-b') return delayedProjectB
    if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
    if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
    if (path.includes('/continuity')) return jsonResponse({ available: false })
    return errorResponse(404, 'fixture unavailable')
  }))

  render(<NewApp />)
  expect(await screen.findByRole('heading', { name: 'Project A' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Archive Project' })).toBeInTheDocument()

  window.history.pushState({}, '', '/projects/project-b')
  window.dispatchEvent(new PopStateEvent('popstate'))
  await waitFor(() => expect(window.location.pathname).toBe('/projects/project-b'))
  expect(screen.queryByRole('heading', { name: 'Project A' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Archive Project' })).not.toBeInTheDocument()

  finishProjectB(errorResponse(503, 'Project B unavailable'))
  expect(await screen.findByRole('alert')).toHaveTextContent('Project B unavailable')
  expect(screen.queryByRole('heading', { name: 'Project A' })).not.toBeInTheDocument()
})

test('a delayed Run poll cannot overwrite the Run opened after navigation', async () => {
  window.history.replaceState({}, '', '/rooms/room-a/tasks/task-a/runs/run-a')
  const runView = (id: string, roomID: string, taskID: string, title: string) => ({
    id, status: 'running', version: 1, attempt: 1, adapter: 'pi',
    room: { id: roomID, name: roomID, description: '' }, task: { id: taskID, title, goal: title },
    context: [], timeline: [], artifacts: [], unknowns: [], criteria: [], controls: { canCancel: false, canRetry: false, canReview: false },
  })
  const runA = runView('run-a', 'room-a', 'task-a', 'Run A task')
  const staleA = { ...runA, version: 2, task: { ...runA.task, title: 'STALE Run A task' } }
  const runB = runView('run-b', 'room-b', 'task-b', 'Run B task')
  let finishPollA!: (response: ReturnType<typeof jsonResponse>) => void
  const delayedPollA = new Promise<ReturnType<typeof jsonResponse>>((resolve) => { finishPollA = resolve })
  let runARequests = 0
  let pollCallback: TimerHandler | undefined
  const interval = vi.spyOn(window, 'setInterval').mockImplementation((handler: TimerHandler) => { pollCallback = handler; return 99 })
  vi.spyOn(window, 'clearInterval').mockImplementation(() => {})
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input)
    if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
    if (path === '/api/pi/discovery') return jsonResponse({ state: 'unavailable' })
    if (path.includes('/continuity')) return jsonResponse({ available: false })
    if (path === '/api/rooms') return jsonResponse({ activeRooms: [], archivedRooms: [] })
    if (path === '/api/rooms/room-a') return jsonResponse({ ...roomRef, id: 'room-a', ownershipKind: 'legacy_standalone' })
    if (path === '/api/rooms/room-b') return jsonResponse({ ...roomRef, id: 'room-b', ownershipKind: 'legacy_standalone' })
    if (path.endsWith('/rooms/room-a/tasks/task-a/runs/run-a')) return ++runARequests === 1 ? jsonResponse(runA) : delayedPollA
    if (path.endsWith('/rooms/room-b/tasks/task-b/runs/run-b')) return jsonResponse(runB)
    return errorResponse(404, 'fixture unavailable')
  }))

  render(<NewApp />)
  expect((await screen.findAllByText('Run A task')).length).toBeGreaterThan(0)
  await waitFor(() => expect(typeof pollCallback).toBe('function'))
  if (typeof pollCallback === 'function') pollCallback()

  window.history.pushState({}, '', '/rooms/room-b/tasks/task-b/runs/run-b')
  window.dispatchEvent(new PopStateEvent('popstate'))
  expect((await screen.findAllByText('Run B task')).length).toBeGreaterThan(0)
  finishPollA(jsonResponse(staleA))
  await waitFor(() => expect(screen.queryByText('STALE Run A task')).not.toBeInTheDocument())
  expect(screen.getAllByText('Run B task').length).toBeGreaterThan(0)
  interval.mockRestore()
})

test('starts a v2 resource Task without a scalar worktree and makes preparation cancellable', async () => {
  window.history.replaceState({}, '', '/rooms/room-1/tasks/task-v2')
  const resourceSnapshot = { resources: [
    { repoId: 'repo-front', name: 'frontend' },
    { repoId: 'repo-back', name: 'backend' },
  ] }
  const planContent = { technical_steps: ['Prepare both repositories'], decisions: [], risks: [], unknowns: [] }
  const draftTask = {
    id: 'task-v2', roomId: 'room-1', title: 'Resource task', executionProfile: 'real_spec_coding', agentExecutionProfile: 'trusted_local',
    resourceSnapshot,
    planning: { revisions: [], draft: { id: 'draft-v2', editVersion: 1, content: planContent } },
  }
  const submittedTask = {
    ...draftTask,
    planning: { revisions: [{ id: 'plan-v2', content: planContent }], draft: undefined },
  }
  const acceptedTask = {
    ...submittedTask,
    planning: { ...submittedTask.planning, acceptance: { revisionId: 'plan-v2' } },
  }
  const workflowSignals: AbortSignal[] = []
  let runSignal: AbortSignal | undefined
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    const method = (init?.method ?? 'GET').toUpperCase()
    if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
    if (path === '/api/pi/discovery') return jsonResponse(piDiscoveryReady)
    if (path.includes('/continuity')) return jsonResponse({ available: false })
    if (path === '/api/agent-execution/trusted-local-acknowledgements/current') return jsonResponse({ acknowledged: true, policyVersion: TRUSTED_LOCAL_DISCLOSURE_POLICY })
    if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [roomSummary], archivedRooms: [] })
    if (method === 'GET' && path === '/api/rooms/room-1') return jsonResponse({ ...roomRef, ownershipKind: 'legacy_standalone' })
    if (method === 'GET' && path === '/api/rooms/room-1/tasks/task-v2') return jsonResponse(draftTask)
    if (method === 'POST' && path === '/api/tasks/task-v2/plan/drafts/draft-v2/submit') {
      workflowSignals.push(init!.signal!)
      return jsonResponse({})
    }
    if (method === 'GET' && path === '/api/tasks/task-v2') {
      workflowSignals.push(init!.signal!)
      return jsonResponse(workflowSignals.length < 4 ? submittedTask : acceptedTask)
    }
    if (method === 'POST' && path === '/api/tasks/task-v2/plan/revisions/plan-v2/activate') {
      workflowSignals.push(init!.signal!)
      return jsonResponse({})
    }
    if (method === 'GET' && path === '/api/v2/tasks/task-v2/resources') return jsonResponse({
      snapshot: resourceSnapshot,
      worktrees: [
        { repoId: 'repo-front', state: 'ready', reason: '' },
        { repoId: 'repo-back', state: 'preparing', reason: 'Creating task worktree' },
      ],
    })
    if (method === 'POST' && path === '/api/tasks/task-v2/runs') {
      workflowSignals.push(init!.signal!)
      runSignal = init!.signal!
      return new Promise<ReturnType<typeof jsonResponse>>((_, reject) => {
        runSignal!.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      })
    }
    throw new Error(`Unexpected fetch: ${method} ${path}`)
  })
  vi.stubGlobal('fetch', fetchMock)

  render(<NewApp />)
  await userEvent.click(await screen.findByRole('button', { name: 'Start Run' }))

  const preparation = await screen.findByRole('region', { name: 'Repository preparation' })
  expect(preparation).toBeInTheDocument()
  expect(screen.getByRole('status')).toHaveTextContent(/elapsed/)
  await waitFor(() => expect(within(preparation).getByText('frontend').closest('li')).toHaveTextContent('ready'))
  expect(within(preparation).getByText('backend').closest('li')).toHaveTextContent('preparing · Creating task worktree')
  await waitFor(() => expect(runSignal).toBeDefined())
  expect(fetchMock.mock.calls.some(([input, init]) => String(input) === '/api/tasks/task-v2/runs' && init?.method === 'POST')).toBe(true)
  expect(workflowSignals).toHaveLength(5)
  expect(workflowSignals.every((signal) => signal === workflowSignals[0])).toBe(true)

  await userEvent.click(screen.getByRole('button', { name: 'Cancel preparation' }))
  expect(runSignal?.aborted).toBe(true)
  await waitFor(() => expect(screen.queryByRole('region', { name: 'Repository preparation' })).not.toBeInTheDocument())
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

test('can cancel resource preparation before Task creation returns an ID', async () => {
  window.history.replaceState({}, '', '/rooms/room-project')
  const projectRoom = { ...roomRef, id: 'room-project', projectId: 'project-1', ownershipKind: 'project', name: 'General' }
  const projectView = {
    id: 'project-1', name: 'Workspace', state: 'active', version: 1, defaultRoomId: 'room-project', rooms: [projectRoom], room: projectRoom,
    lastActivityAt: '', taskCounts: { total: 0, open: 0, terminal: 0 },
  }
  let createSignal: AbortSignal | undefined
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    const method = (init?.method ?? 'GET').toUpperCase()
    if (path === '/api/pi/installation') return jsonResponse(piInstallationMissing)
    if (path === '/api/pi/discovery') return jsonResponse(piDiscoveryReady)
    if (path.includes('/continuity')) return jsonResponse({ available: false })
    if (path === '/api/agent-execution/trusted-local-acknowledgements/current') return jsonResponse({ acknowledged: true, policyVersion: TRUSTED_LOCAL_DISCLOSURE_POLICY })
    if (method === 'GET' && path === '/api/rooms') return jsonResponse({ activeRooms: [], archivedRooms: [] })
    if (method === 'GET' && path === '/api/rooms/room-project') return jsonResponse(projectRoom)
    if (method === 'GET' && path === '/api/v2/projects/project-1') return jsonResponse(projectView)
    if (method === 'GET' && path === '/api/rooms/room-project/workspace') return jsonResponse({ room: projectRoom, tasks: [] })
    if (method === 'GET' && path === '/api/v2/rooms/room-project/task-options') return jsonResponse({
      repositories: [{ repoId: 'repo-one', name: 'frontend', localLocator: '/code/frontend', state: 'active', version: 1, availability: 'ready', branch: 'main', head: 'a'.repeat(40), dirty: false }],
      selectedRepoIds: ['repo-one'], selectionSource: 'single_repository', defaultCheckMode: 'auto',
    })
    if (method === 'GET' && path === '/api/v2/repositories/repo-one/branches?limit=100') return jsonResponse({ branches: [{ ref: 'refs/heads/main', commit: 'a'.repeat(40) }], nextCursor: '' })
    if (method === 'GET' && path === '/api/v2/repositories/repo-one/delivery-defaults') return jsonResponse({ targetRef: 'refs/heads/main', version: 1, suggestedTargetRef: '', reason: '' })
    if (method === 'POST' && path === '/api/v2/rooms/room-project/tasks') {
      createSignal = init!.signal!
      return new Promise<ReturnType<typeof jsonResponse>>((_, reject) => {
        createSignal!.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      })
    }
    throw new Error(`Unexpected fetch: ${method} ${path}`)
  })
  vi.stubGlobal('fetch', fetchMock)

  render(<NewApp />)
  await userEvent.click((await screen.findAllByRole('button', { name: '＋ New Task' }))[0])
  await userEvent.type(await screen.findByLabelText('What should Chora build?'), 'Update the frontend')
  await userEvent.click(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL }))
  await userEvent.click(screen.getByRole('button', { name: 'Start' }))

  const preparation = await screen.findByRole('region', { name: 'Repository preparation' })
  expect(within(preparation).getByRole('status')).toHaveTextContent(/elapsed/)
  expect(within(preparation).getByText('repo-one').closest('li')).toHaveTextContent('planned')
  await waitFor(() => expect(createSignal).toBeDefined())
  expect(fetchMock.mock.calls.map(([input]) => String(input)).filter((path) => path.includes('/api/v2/tasks/') && path.endsWith('/resources'))).toEqual([])

  await userEvent.click(within(preparation).getByRole('button', { name: 'Cancel preparation' }))
  expect(createSignal?.aborted).toBe(true)
  await waitFor(() => expect(screen.queryByRole('region', { name: 'Repository preparation' })).not.toBeInTheDocument())
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})
