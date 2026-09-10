import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import { ExternalHandoff } from './ExternalHandoff'

test('cleaned worktrees show neutral history status without external open controls', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ available: true, ready: false, cleanedUp: true }) })))
  render(<LanguageProvider><ExternalHandoff roomId="room-1" taskId="task-2" /></LanguageProvider>)
  expect(await screen.findByRole('status')).toHaveTextContent('Task worktrees have been cleaned up. Review and delivery history remain available.')
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
})

test('rechecks unavailable worktree and submits only IDs and the selected application', async () => {
  let ready = false
  const posts: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (_input: unknown, init?: RequestInit) => {
    if (init?.method === 'POST') { posts.push(JSON.parse(String(init.body))); return { ok: true, json: async () => ({ opened: true }) } }
    return { ok: true, json: async () => ({ available: true, ready, path: ready ? '/tmp/a b' : undefined, reason: ready ? undefined : 'Restore the worktree', applications: [{ id: 'terminal', name: 'Terminal', kind: 'terminal' }, { id: 'cursor', name: 'Cursor', kind: 'editor' }] }) }
  }))
  render(<LanguageProvider><ExternalHandoff roomId="room-1" taskId="task-2" /></LanguageProvider>)
  expect(await screen.findByRole('alert')).toHaveTextContent('Restore the worktree')
  expect(screen.getByRole('button', { name: 'Open in Terminal' })).toBeDisabled()
  ready = true
  await userEvent.click(screen.getByLabelText('Open with…'))
  await userEvent.click(screen.getByRole('button', { name: 'Check again' }))
  await screen.findByText('/tmp/a b')
  await userEvent.click(screen.getByRole('button', { name: 'Open in Terminal' }))
  expect(posts).toEqual([{ application: 'terminal', taskId: 'task-2' }])
})

test('does not advertise apps that are not installed', async () => {
 vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ available: true, ready: true, path: '/tmp/repo', applications: [{id:'cursor',name:'Cursor',kind:'editor'}] }) })))
 render(<LanguageProvider><ExternalHandoff roomId="room-1" /></LanguageProvider>)
 expect(await screen.findByRole('button', {name:'Open in Cursor'})).toBeEnabled()
 expect(screen.queryByRole('button', {name:'Open in VS Code'})).not.toBeInTheDocument()
 expect(screen.queryByRole('button', {name:'Open in Terminal'})).not.toBeInTheDocument()
})

test('task-first Room shows a selection hint and Task still loads its own worktree', async () => {
  const fetcher = vi.fn(async (url: string) => ({ ok: true, json: async () => url.includes('taskId=')
    ? { available: true, ready: true, path: '/tmp/task-root', applications: [{ id: 'terminal', name: 'Terminal', kind: 'terminal' }] }
    : { available: true, ready: false, selectionRequired: true } }))
  vi.stubGlobal('fetch', fetcher)
  const view = render(<LanguageProvider><ExternalHandoff roomId="room-1" /></LanguageProvider>)
  expect(await screen.findByRole('status')).toHaveTextContent('Open a task to use its worktree')
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(screen.queryByLabelText('Open with…')).not.toBeInTheDocument()
  view.rerender(<LanguageProvider><ExternalHandoff roomId="room-1" taskId="task-2" /></LanguageProvider>)
  expect(await screen.findByRole('button', { name: 'Open in Terminal' })).toBeEnabled()
  expect(screen.getByText('/tmp/task-root')).toBeInTheDocument()
})

test('preparation is a status and automatically becomes ready without opening anything', async () => {
  let ready = false
  const fetcher = vi.fn(async () => ({ ok: true, json: async () => ready
    ? { available: true, ready: true, path: '/tmp/prepared-task', applications: [{ id: 'terminal', name: 'Terminal', kind: 'terminal' }] }
    : { available: true, ready: false, preparing: true, reason: 'Preparing task worktree…', applications: [{ id: 'terminal', name: 'Terminal', kind: 'terminal' }] } }))
  vi.stubGlobal('fetch', fetcher)
  render(<LanguageProvider><ExternalHandoff roomId="room-1" taskId="task-2" /></LanguageProvider>)
  expect(await screen.findByRole('status')).toHaveTextContent('Preparing task worktree…')
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Open in Terminal' })).toBeDisabled()
  ready = true
  expect(await screen.findByText('/tmp/prepared-task', {}, { timeout: 2500 })).toBeInTheDocument()
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Open in Terminal' })).toBeEnabled()
  expect(fetcher.mock.calls.length).toBe(2)
})
