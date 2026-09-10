import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { CheckCommandView, RepositoryResourceView } from '../taskFirstTypes'
import { RepositoryChecks } from './RepositoryChecks'

const repository: RepositoryResourceView = {
  repoId: 'repo-front', name: 'frontend', localLocator: '/code/frontend', state: 'active', version: 3,
  availability: 'ready', branch: 'main', head: 'a'.repeat(40), dirty: false,
}
const unitCheck: CheckCommandView = {
  id: 'unit', name: 'Unit tests', version: 3, command: 'npm run test -- --watch=false',
  argv: ['npm', 'run', 'test', '--', '--watch=false'], workingDirectory: 'packages/app', source: 'user',
}

function response(body: unknown, ok = true, status = 200): Response {
  return { ok, status, json: async () => body } as Response
}

function renderChecks() {
  return render(<LanguageProvider><RepositoryChecks projectId="project-1" repository={repository} /></LanguageProvider>)
}

describe('RepositoryChecks', () => {
  beforeEach(() => { window.localStorage.clear(); vi.unstubAllGlobals() })

  test('loads and displays persisted named checks only after opening the repository editor', async () => {
    const fetchMock = vi.fn(async () => response({ checks: [unitCheck] }))
    vi.stubGlobal('fetch', fetchMock)
    renderChecks()

    expect(fetchMock).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Manage checks' }))

    const dialog = screen.getByRole('dialog', { name: 'Named checks for frontend' })
    expect(await within(dialog).findByText('Unit tests')).toBeInTheDocument()
    expect(within(dialog).getByText('npm run test -- --watch=false')).toBeInTheDocument()
    expect(within(dialog).getByText('Working directory: packages/app · v3')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Edit Unit tests' })).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledWith('/api/v2/projects/project-1/repositories/repo-front/checks', expect.objectContaining({ signal: expect.any(AbortSignal) }))
  })

  test('adds one named check at version zero and refreshes from the persisted response', async () => {
    const calls: Array<{ path: string; method: string; body?: unknown }> = []
    const saved: CheckCommandView = {
      id: '', name: 'Workspace test', version: 1, command: 'npm test -- --runInBand',
      argv: ['npm', 'test', '--', '--runInBand'], workingDirectory: 'packages', source: 'user',
    }
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      const method = init?.method ?? 'GET'
      calls.push({ path, method, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (path.includes('/entries?')) {
        if (path.includes('path=packages')) return response({ entries: [], nextCursor: '', truncated: false, revision: 'a'.repeat(40) })
        return response({ entries: [{ name: 'packages', path: 'packages', kind: 'directory' }], nextCursor: '', truncated: false, revision: 'a'.repeat(40) })
      }
      if (method === 'PUT') {
        const submitted = (calls.at(-1)?.body as { checks: CheckCommandView[] }).checks[0]
        return response({ checks: [{ ...saved, id: submitted.id }] })
      }
      return response({ checks: [] })
    }))
    renderChecks()

    await userEvent.click(screen.getByRole('button', { name: 'Manage checks' }))
    await screen.findByText('No named checks yet.')
    await userEvent.click(screen.getByRole('button', { name: 'Add named check' }))
    await userEvent.type(screen.getByLabelText('Check name'), 'Workspace test')
    await userEvent.type(screen.getByLabelText('Command'), 'npm test -- --runInBand')
    await userEvent.click(screen.getByRole('button', { name: 'Choose check directory…' }))
    await userEvent.click(await screen.findByRole('button', { name: 'packages' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Use this directory' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save check' }))

    expect(await screen.findByText('Workspace test')).toBeInTheDocument()
    expect(screen.queryByLabelText('Check name')).not.toBeInTheDocument()
    const put = calls.find((call) => call.method === 'PUT')!
    expect(put.path).toBe('/api/v2/projects/project-1/repositories/repo-front/checks')
    expect(put.body).toEqual({ checks: [{
      id: expect.stringMatching(/^repository-check-/),
      name: 'Workspace test',
      version: 0,
      command: 'npm test -- --runInBand',
      workingDirectory: 'packages',
      source: 'user',
    }] })
    expect(screen.getByText('Working directory: packages · v1')).toBeInTheDocument()
  })

  test('edits the same check with its observed version and renders the returned version', async () => {
    let submitted: { checks: CheckCommandView[] } | undefined
    vi.stubGlobal('fetch', vi.fn(async (_input: string | URL | Request, init?: RequestInit) => {
      if (init?.method === 'PUT') {
        submitted = JSON.parse(String(init.body))
        return response({ checks: [{ ...unitCheck, name: 'CI tests', command: 'npm run test:ci', version: 4 }] })
      }
      return response({ checks: [unitCheck] })
    }))
    renderChecks()

    await userEvent.click(screen.getByRole('button', { name: 'Manage checks' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Edit Unit tests' }))
    await userEvent.clear(screen.getByLabelText('Check name'))
    await userEvent.type(screen.getByLabelText('Check name'), 'CI tests')
    await userEvent.clear(screen.getByLabelText('Command'))
    await userEvent.type(screen.getByLabelText('Command'), 'npm run test:ci')
    await userEvent.click(screen.getByRole('button', { name: 'Save check' }))

    await waitFor(() => expect(screen.getByText('Working directory: packages/app · v4')).toBeInTheDocument())
    expect(submitted).toEqual({ checks: [{
      id: 'unit', name: 'CI tests', version: 3, command: 'npm run test:ci', workingDirectory: 'packages/app', source: 'user',
    }] })
    expect(screen.getByRole('button', { name: 'Edit CI tests' })).toBeInTheDocument()
  })

  test('shows a save conflict inline and preserves the unsaved edit for retry', async () => {
    vi.stubGlobal('fetch', vi.fn(async (_input: string | URL | Request, init?: RequestInit) => {
      if (init?.method === 'PUT') return response({ error: 'check version conflict' }, false, 409)
      return response({ checks: [unitCheck] })
    }))
    renderChecks()

    await userEvent.click(screen.getByRole('button', { name: 'Manage checks' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Edit Unit tests' }))
    await userEvent.clear(screen.getByLabelText('Check name'))
    await userEvent.type(screen.getByLabelText('Check name'), 'Unsaved CI tests')
    await userEvent.clear(screen.getByLabelText('Command'))
    await userEvent.type(screen.getByLabelText('Command'), 'npm run test:conflict')
    await userEvent.click(screen.getByRole('button', { name: 'Save check' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('check version conflict')
    expect(screen.getByLabelText('Check name')).toHaveValue('Unsaved CI tests')
    expect(screen.getByLabelText('Command')).toHaveValue('npm run test:conflict')
    expect(screen.getByRole('button', { name: 'Save check' })).toBeEnabled()
  })
})
