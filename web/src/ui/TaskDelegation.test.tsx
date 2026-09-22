import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import { TaskDelegation } from './TaskDelegation'

const initial = { parentTaskId: 'task-parent', version: 0, state: 'not_started', eligible: true, assignments: [], children: [] }
afterEach(() => vi.unstubAllGlobals())

it('requires explicit bounded assignments before starting and navigates to child evidence', async () => {
  let current = initial as unknown as Record<string, unknown>
  const requests: { path: string; body: Record<string, unknown> }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path, init) => {
    if (init?.method === 'POST') {
      const body = JSON.parse(init.body)
      requests.push({ path: String(path), body })
      current = { ...initial, version: 1, state: 'running', assignments: body.assignments, children: [{ position: 0, role: 'Reviewer', title: 'Review compatibility', state: 'running', url: '/rooms/r/tasks/child/runs/run' }] }
    }
    return new Response(JSON.stringify(current), { status: 200 })
  }))
  const navigate = vi.fn()
  render(<LanguageProvider><TaskDelegation taskId="task-parent" onNavigate={navigate} /></LanguageProvider>)
  const start = await screen.findByRole('button', { name: 'Start delegation' })
  expect(start).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Agent role 1'), { target: { value: 'Reviewer' } })
  fireEvent.change(screen.getByLabelText('Task title 1'), { target: { value: 'Review compatibility' } })
  fireEvent.change(screen.getByLabelText('Assignment instructions 1'), { target: { value: 'Find compatibility risks.' } })
  expect(start).toBeEnabled()
  expect(requests).toHaveLength(0)
  fireEvent.click(start)
  await screen.findByRole('button', { name: 'Open child task' })
  expect(requests).toHaveLength(1)
  expect(requests[0].body).toEqual({ assignments: [{ role: 'Reviewer', title: 'Review compatibility', requirement: 'Find compatibility risks.' }] })
  fireEvent.click(screen.getByRole('button', { name: 'Open child task' }))
  expect(navigate).toHaveBeenCalledWith('/rooms/r/tasks/child/runs/run')
})

it('shows sourced output as text and never offers automatic acceptance', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ...initial, version: 2, state: 'awaiting_review', children: [{ position: 0, role: 'Researcher', title: 'Finding', state: 'awaiting_review', markdown: '<script>unsafe()</script>', resultId: 'result-1', resultDigest: 'abc' }] }))))
  render(<LanguageProvider><TaskDelegation taskId="task-parent" onNavigate={vi.fn()} /></LanguageProvider>)
  await screen.findByText('Delegated finding')
  expect(screen.getByText('<script>unsafe()</script>')).toBeInTheDocument()
  expect(document.querySelector('script')).toBeNull()
  expect(screen.queryByRole('button', { name: /accept/i })).toBeNull()
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Start delegation' })).toBeNull())
})

const source = { runId: 'run-parent', attemptId: 'attempt-parent', resultId: 'result-parent', resultDigest: 'a'.repeat(64), planDigest: 'b'.repeat(64), current: true }
const proposed = { available: true, source, assignments: [{ role: 'Designer', title: 'Compare options', requirement: 'Compare supplied options, keeping unknowns explicit.' }] }

it('previews an Agent plan without execution and starts using only its source binding', async () => {
  const writes: { path: string; body: unknown }[] = []
  let view: Record<string, unknown> = initial
  vi.stubGlobal('fetch', vi.fn(async (path, init) => {
    if (init?.method === 'POST') {
      writes.push({ path: String(path), body: JSON.parse(init.body) })
      view = { ...initial, version: 1, state: 'running', source, assignments: proposed.assignments }
    }
    return new Response(JSON.stringify(String(path).endsWith('/proposal') ? proposed : view))
  }))
  render(<LanguageProvider><TaskDelegation taskId="task-parent" onNavigate={vi.fn()} /></LanguageProvider>)
  fireEvent.click(await screen.findByRole('button', { name: 'Load Agent plan' }))
  expect(await screen.findByText(/Compare options/)).toBeInTheDocument()
  expect(screen.getByText('result-parent')).toBeInTheDocument()
  expect(writes).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: 'Start proposed delegation' }))
  await waitFor(() => expect(writes).toEqual([{ path: '/api/tasks/task-parent/delegation', body: { sourceAttemptId: source.attemptId, expectedResultDigest: source.resultDigest } }]))
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Start proposed delegation' })).toBeNull())
  expect(screen.getByText('result-parent')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /accept/i })).toBeNull()
})

it('keeps source-conflict feedback visible after successful polling', async () => {
  let polls = 0
  vi.stubGlobal('fetch', vi.fn(async (path, init) => {
    if (init?.method === 'POST') return new Response(JSON.stringify({ error: 'The parent result changed. Reload its plan.' }), { status: 409 })
    if (String(path).endsWith('/proposal')) return new Response(JSON.stringify(proposed))
    polls++
    return new Response(JSON.stringify(initial))
  }))
  render(<LanguageProvider><TaskDelegation taskId="task-parent" onNavigate={vi.fn()} /></LanguageProvider>)
  fireEvent.click(await screen.findByRole('button', { name: 'Load Agent plan' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Start proposed delegation' }))
  await screen.findByRole('alert')
  await waitFor(() => expect(polls).toBeGreaterThan(1))
  expect(screen.getByRole('alert')).toHaveTextContent('The parent result changed. Reload its plan.')
})

it('retains original plan provenance when the parent result is superseded', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ...initial, version: 1, state: 'running', source: { ...source, current: false } }))))
  render(<LanguageProvider><TaskDelegation taskId="task-parent" onNavigate={vi.fn()} /></LanguageProvider>)
  await screen.findByText('The parent result has changed. This delegation keeps its original plan and source.')
  expect(screen.getByText('result-parent')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Start proposed delegation' })).toBeNull()
})
