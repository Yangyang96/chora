import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { ProjectView } from '../types'
import type { BoardCard, TaskBoardPage } from '../taskBoardTypes'
import { TaskBoard } from './TaskBoard'

const project = { id: 'p1', name: 'Example', state: 'active', defaultRoomId: 'r1', rooms: [{ id: 'r1', name: 'First', state: 'active' }, { id: 'r2', name: 'Second', state: 'active' }] } as ProjectView
const baseCard: BoardCard = {
  projectId: 'p1', roomId: 'r1', roomName: 'First', taskId: 't1', title: 'Ship the change', repositories: [{ repoId: 'repo1', name: 'Frontend' }],
  phase: 'delivery', substate: 'accepted', outcome: null, attention: { state: 'required', reasons: [{ code: 'delivery_pending' }] },
  nextAction: { kind: 'view_terminal', url: '/rooms/r1/tasks/t1/runs/run1' }, visibility: { taskArchived: false, roomArchived: false, projectArchived: false, readOnly: false },
  health: 'current', lastActivityAt: '2026-09-18T01:00:00Z', source: { latestRunId: 'run1', latestRunVersion: 3 },
}
function result(cards = [baseCard], extra: Partial<TaskBoardPage> = {}): TaskBoardPage {
  return { snapshot: 's1', cards, total: cards.length, observedAt: '2026-09-18T01:00:00Z', counts: { phase: { delivery: cards.length }, attention: cards.length, reconciliation: 0 }, rooms: [{ roomId: 'r1', name: 'First' }, { roomId: 'r2', name: 'Second' }], repositories: [{ repoId: 'repo1', name: 'Frontend' }], ...extra }
}
const response = (data: unknown, status = 200) => Promise.resolve({ ok: status < 400, status, json: async () => data } as Response)
function mount(props: Partial<React.ComponentProps<typeof TaskBoard>> = {}) {
  const onNavigate = vi.fn(), onNewTask = vi.fn()
  const view = render(<LanguageProvider><TaskBoard project={project} onNavigate={onNavigate} onNewTask={onNewTask} {...props} /></LanguageProvider>)
  return { ...view, onNavigate, onNewTask }
}
beforeEach(() => { localStorage.clear(); window.history.replaceState({}, '', '/projects/p1/tasks'); vi.stubGlobal('fetch', vi.fn(() => response(result()))) })
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe('Task board shared read view', () => {
  test('shows derived delivery, never claims accepted is finished, and only navigates', async () => {
    const { onNavigate } = mount()
    await screen.findByRole('link', { name: 'Ship the change' })
    expect(screen.getByText('Accepted · Delivery pending')).toBeVisible()
    fireEvent.click(screen.getByRole('link', { name: 'View details' }))
    expect(onNavigate).toHaveBeenCalledWith('/rooms/r1/tasks/t1/runs/run1')
    expect(vi.mocked(fetch).mock.calls.every(([, options]) => !options?.method || options.method === 'GET')).toBe(true)
  })
  test('shows the current Gate question as text, without making the board an answer command', async () => {
    vi.mocked(fetch).mockImplementation(() => response(result([{ ...baseCard, phase: 'working', substate: 'waiting_gate', attention: { state: 'required', reasons: [{ code: 'blocking_gate', targetId: 'gate-current', summary: 'Choose the target branch <script>not executable</script>' }] }, nextAction: { kind: 'answer_gate', url: '/rooms/r1/tasks/t1/runs/run1' } }])))
    const { onNavigate } = mount()
    expect(await screen.findByText('Choose the target branch <script>not executable</script>')).toBeVisible()
    expect(document.querySelector('.board-attention script')).toBeNull()
    fireEvent.click(screen.getByRole('link', { name: 'Review required decision' }))
    expect(onNavigate).toHaveBeenCalledWith('/rooms/r1/tasks/t1/runs/run1')
    expect(vi.mocked(fetch).mock.calls.every(([, options]) => !options?.method || options.method === 'GET')).toBe(true)
  })
  test('distinguishes delivery evidence age from read freshness without live polling announcements', async () => {
    vi.mocked(fetch).mockImplementation(() => response(result([{ ...baseCard, repositories: [{ repoId: 'repo1', name: 'Frontend', status: 'pending_merge', deliveryObservedAt: '2026-09-01T00:00:00Z', achievements: ['commit', 'push'] }] }])))
    mount()
    const evidence = await screen.findByText(/^Delivery observed at /)
    expect(evidence).toHaveAttribute('datetime', '2026-09-01T00:00:00Z')
    const freshness = screen.getByText(/Read at /)
    expect(freshness).not.toHaveAttribute('role', 'status')
    expect(screen.getByText(/Recorded achievements/)).toHaveTextContent('Committed, Pushed')
  })
  test('preserves filters and presentation in URL and restores browser back navigation', async () => {
    mount(); await screen.findByText('Ship the change')
    fireEvent.change(screen.getByLabelText('Room'), { target: { value: 'r2' } })
    await waitFor(() => expect(String(vi.mocked(fetch).mock.calls.at(-1)?.[0])).toContain('roomId=r2'))
    fireEvent.click(screen.getByRole('button', { name: 'List' }))
    expect(window.location.search).toContain('mode=list')
    window.history.replaceState({}, '', '/projects/p1/tasks?repoId=repo1&attention=required')
    fireEvent.popState(window)
    expect(screen.getByLabelText('Needs attention')).toBeChecked()
    expect(screen.getByRole('button', { name: 'Board' })).toHaveAttribute('aria-pressed', 'true')
    await waitFor(() => expect(String(vi.mocked(fetch).mock.calls.at(-1)?.[0])).toContain('repoId=repo1'))
  })
  test('retains cards on failed refresh, labels stale state and keeps only detail navigation', async () => {
    mount(); await screen.findByText('Ship the change')
    vi.mocked(fetch).mockImplementationOnce(() => Promise.reject(new Error('offline')))
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('last observed state')
    expect(screen.getByText('Ship the change')).toBeVisible()
    fireEvent(window, new Event('online'))
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
  })
  test('distinguishes unavailable from an empty project', async () => {
    vi.mocked(fetch).mockRejectedValue(new Error('offline'))
    mount()
    expect(await screen.findByRole('alert')).toHaveTextContent('Task board unavailable')
    expect(screen.queryByText('No tasks match these filters.')).not.toBeInTheDocument()
  })
  test('isolates late responses after a filter change and aborts the old request', async () => {
    let resolve!: (r: Response) => void
    vi.mocked(fetch).mockImplementationOnce(() => new Promise((r) => { resolve = r }))
    mount()
    const signal = vi.mocked(fetch).mock.calls[0][1]?.signal
    fireEvent.change(screen.getByLabelText('Room'), { target: { value: 'r2' } })
    await screen.findByText('Ship the change')
    await act(async () => resolve(await response(result([{ ...baseCard, title: 'Old scope' }]))))
    expect(signal?.aborted).toBe(true)
    expect(screen.queryByText('Old scope')).not.toBeInTheDocument()
  })
  test('keeps unknown records visible in reconciliation without false success', async () => {
    vi.mocked(fetch).mockImplementation(() => response(result([{ ...baseCard, phase: null, substate: 'unknown', attention: { state: 'unknown', reasons: [] } }], { counts: { phase: {}, attention: 0, reconciliation: 1 } })))
    mount()
    expect(await screen.findByRole('region', { name: 'Needs reconciliation' })).toHaveTextContent('Ship the change')
    expect(screen.queryByText('Delivered')).not.toBeInTheDocument()
  })
  test('uses the same projection for a Room, keeps archived work read-only, and localizes', async () => {
    localStorage.setItem('chora.locale', 'zh-CN')
    mount({ roomId: 'r1', project: { ...project, state: 'archived' } })
    await screen.findByText('Ship the change')
    expect(screen.getByText('已接受 · 尚待交付')).toBeVisible()
    expect(screen.getByRole('button', { name: '＋ 新建任务' })).toBeDisabled()
    expect(screen.queryByLabelText('Room')).not.toBeInTheDocument()
    expect(String(vi.mocked(fetch).mock.calls[0][0])).toContain('roomId=r1')
  })
  test('deduplicates pages, retains full totals and loaded pages during unchanged refresh', async () => {
    const page1 = result([baseCard], { total: 2, nextCursor: 'next', counts: { phase: { delivery: 2 }, attention: 2, reconciliation: 0 } })
    vi.mocked(fetch).mockImplementation((path) => response(String(path).includes('cursor=') ? result([baseCard, { ...baseCard, taskId: 't2', title: 'Second task' }], { total: 2 }) : page1))
    mount(); await screen.findByText('1 of 2 tasks', { exact: false })
    fireEvent.click(screen.getByRole('button', { name: 'Load more tasks' }))
    await screen.findByText('Second task')
    expect(screen.getAllByText('Ship the change')).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh' })).not.toBeDisabled())
    expect(screen.getByText('Second task')).toBeVisible()
  })
  test('restarts stale pagination instead of appending a changed snapshot', async () => {
    vi.mocked(fetch).mockImplementationOnce(() => response(result([baseCard], { nextCursor: 'old' })))
    mount(); await screen.findByText('Ship the change')
    vi.mocked(fetch).mockImplementationOnce(() => response({ error: 'Snapshot changed' }, 409))
    fireEvent.click(screen.getByRole('button', { name: 'Load more tasks' }))
    await screen.findByRole('alert')
    expect(screen.queryByText('Ship the change')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await screen.findByText('Ship the change')
  })
  test('loads filter options independently and preserves them across unchanged polling', async () => {
    const first = result([baseCard], { total: 2, nextCursor: 'tasks-next', optionsSnapshot: 'choices-1', nextOptionsCursor: 'choices-next', rooms: [{ roomId: 'r1', name: 'First' }] })
    vi.mocked(fetch).mockImplementation((path) => response(String(path).includes('optionsCursor=')
      ? result([{ ...baseCard, title: 'Must not replace the task page' }], { optionsSnapshot: 'choices-1', rooms: [{ roomId: 'r2', name: 'Second' }], repositories: [{ repoId: 'repo1', name: 'Frontend' }, { repoId: 'repo2', name: 'Backend' }] })
      : first))
    mount(); await screen.findByText('Ship the change')
    fireEvent.click(screen.getByRole('button', { name: 'Load more filters' }))
    await screen.findByRole('option', { name: 'Backend' })
    expect(screen.getAllByRole('option', { name: 'Frontend' })).toHaveLength(1)
    expect(screen.getByRole('option', { name: 'Second' })).toBeInTheDocument()
    expect(screen.queryByText('Must not replace the task page')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Load more tasks' })).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh' })).not.toBeDisabled())
    expect(screen.getByRole('option', { name: 'Backend' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load more filters' })).not.toBeInTheDocument()
    vi.mocked(fetch).mockImplementation(() => response(result([baseCard], { optionsSnapshot: 'choices-2', repositories: [{ repoId: 'repo1', name: 'Renamed frontend' }] })))
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await screen.findByRole('option', { name: 'Renamed frontend' })
    expect(screen.queryByRole('option', { name: 'Backend' })).not.toBeInTheDocument()
  })
  test('a stale filter-options cursor retains loaded Tasks and restarts only the choices', async () => {
    vi.mocked(fetch).mockImplementation(() => response(result([baseCard, { ...baseCard, taskId: 't2', title: 'Second task' }], { nextCursor: 'tasks-next', optionsSnapshot: 'choices-1', nextOptionsCursor: 'choices-next' })))
    mount(); await screen.findByText('Second task')
    vi.mocked(fetch).mockImplementationOnce(() => response({ error: 'Filter options changed' }, 409))
    fireEvent.click(screen.getByRole('button', { name: 'Load more filters' }))
    await screen.findByRole('alert')
    expect(screen.getByText('Ship the change')).toBeVisible()
    expect(screen.getByText('Second task')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Load more tasks' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Load more filters' })).toBeVisible()
  })
  test('restores a selected option supplied outside the first filter page', async () => {
    window.history.replaceState({}, '', '/projects/p1/tasks?repoId=repo501')
    vi.mocked(fetch).mockImplementation(() => response(result([baseCard], { optionsSnapshot: 'choices-1', nextOptionsCursor: 'choices-next', repositories: [{ repoId: 'repo1', name: 'Frontend' }, { repoId: 'repo501', name: 'Selected repository' }] })))
    mount(); await screen.findByRole('option', { name: 'Selected repository' })
    expect(screen.getByLabelText('Repository')).toHaveValue('repo501')
    expect(String(vi.mocked(fetch).mock.calls[0][0])).toContain('repoId=repo501')
  })
  test('pauses hidden polling and immediately refreshes on visibility; refresh retains focus', async () => {
    mount(); await screen.findByText('Ship the change')
    const control = screen.getByRole('button', { name: 'List' })
    control.focus()
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    fireEvent(document, new Event('visibilitychange'))
    fireEvent(window, new Event('focus'))
    expect(fetch).toHaveBeenCalledTimes(1)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    fireEvent(document, new Event('visibilitychange'))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2))
    expect(document.activeElement).toBe(control)
  })
})
