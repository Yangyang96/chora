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
