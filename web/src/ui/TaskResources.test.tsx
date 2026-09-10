import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { RepositoryResourceView, TaskOptionsView, TaskResourceSelection } from '../taskFirstTypes'
import { TaskResources } from './TaskResources'

function repository(repoId: string, name: string): RepositoryResourceView {
  return { repoId, name, localLocator: `/code/${name}`, state: 'active', version: 3, availability: 'ready', branch: 'main', head: 'a'.repeat(40), dirty: false }
}
function response(body: unknown, ok = true, status = 200): Response { return { ok, status, json: async () => body } as Response }
function options(overrides: Partial<TaskOptionsView> = {}): TaskOptionsView {
  return { repositories: [repository('repo-one', 'frontend')], selectedRepoIds: ['repo-one'], selectionSource: 'single_repository', defaultCheckMode: 'auto', ...overrides }
}
function auxiliary(input: string | URL | Request): Response | undefined {
  const path = String(input)
  if (path.endsWith('/branches?limit=100')) return response({ branches: [{ ref: 'refs/heads/main', commit: 'a'.repeat(40) }], nextCursor: '' })
  if (path.endsWith('/delivery-defaults')) return response({ targetRef: 'refs/heads/main', version: 1, suggestedTargetRef: '', reason: '' })
}

describe('TaskResources', () => {
  beforeEach(() => window.localStorage.removeItem('chora.locale'))

  function show(value: TaskOptionsView, onReady = vi.fn()) {
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => auxiliary(input) ?? response(value)))
    render(<LanguageProvider><TaskResources projectId="project-1" roomId="room-1" busy={false} onReady={onReady} /></LanguageProvider>)
    return onReady
  }

  test('uses only server-selected defaults and never implicitly selects every repository', async () => {
    const onReady = show(options({ repositories: [repository('repo-one', 'frontend'), repository('repo-two', 'backend')], selectedRepoIds: ['repo-one'], selectionSource: 'room' }))
    expect(await screen.findByLabelText('frontend')).toBeChecked()
    expect(screen.getByLabelText('backend')).not.toBeChecked()
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith([expect.objectContaining({ repoId: 'repo-one', role: 'write', scope: expect.objectContaining({ mode: 'repository' }), checks: expect.objectContaining({ mode: 'auto' }) })]))
  })

  test('uses a saved delivery default, while an unsaved suggestion requires explicit task choice and save', async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/branches?limit=100')) return response({ branches: [{ ref: 'refs/heads/main', commit: 'a'.repeat(40) }], nextCursor: '' })
      if (path.endsWith('/delivery-defaults') && init?.method === 'PUT') return response({ targetRef: 'refs/heads/main', version: 2, suggestedTargetRef: '', reason: '' })
      if (path.endsWith('/delivery-defaults')) return response({ targetRef: '', version: 1, suggestedTargetRef: 'refs/heads/main', reason: 'Remote HEAD identifies main.' })
      return response(options())
    })
    vi.stubGlobal('fetch', fetchMock)
    const onReady = vi.fn()
    render(<LanguageProvider><TaskResources projectId="project-1" roomId="room-1" busy={false} onReady={onReady} /></LanguageProvider>)
    expect(await screen.findByText('Remote HEAD identifies main.')).toBeInTheDocument()
    expect(onReady).toHaveBeenLastCalledWith(undefined)
    expect(screen.getByLabelText('frontend Target branch')).toHaveValue('')

    await userEvent.click(screen.getByRole('button', { name: /Use suggested branch for this task/ }))
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith([expect.objectContaining({ targetRef: 'refs/heads/main' })]))
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: 'Save as repository default' }))
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === 'PUT')
    expect(JSON.parse(String(put?.[1]?.body))).toEqual({ expectedVersion: 1, targetRef: 'refs/heads/main' })
  })

  test('keeps readiness closed until a selected repository on a later page is visible', async () => {
    const initial = options({ repositories: [repository('repo-one', 'frontend')], selectedRepoIds: ['repo-late'], selectionSource: 'room_selection', nextRepositoryCursor: 'room-project-cursor' })
    const late = repository('repo-late', 'late-service')
    const fetchMock = vi.fn(async (input: string | URL | Request) => auxiliary(input) ?? (String(input).includes('/task-options')
      ? response(initial)
      : response({ repositories: [late], nextCursor: '' })))
    vi.stubGlobal('fetch', fetchMock)
    const onReady = vi.fn()
    render(<LanguageProvider><TaskResources projectId="project-1" roomId="room-1" busy={false} onReady={onReady} /></LanguageProvider>)
    expect(await screen.findByText(/Load more repositories to review/)).toBeInTheDocument()
    expect(onReady).toHaveBeenLastCalledWith(undefined)
    await userEvent.click(screen.getByRole('button', { name: 'Load more repositories' }))
    expect(await screen.findByLabelText('late-service')).toBeChecked()
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith([expect.objectContaining({ repoId: 'repo-late', role: 'write' })]))
    expect(fetchMock.mock.calls.map(([input]) => String(input)).filter((path) => !path.includes('/branches?') && !path.endsWith('/delivery-defaults'))).toEqual([
      '/api/v2/rooms/room-1/task-options',
      '/api/v2/projects/project-1/repositories?cursor=room-project-cursor&limit=200',
    ])
  })

  test('reports an explicit empty selection and reference-only invalid state', async () => {
    const onReady = show(options())
    await screen.findByLabelText('frontend')
    await userEvent.click(screen.getByLabelText('frontend'))
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith([]))
    await userEvent.click(screen.getByLabelText('frontend'))
    await userEvent.click(screen.getByLabelText('Reference only'))
    expect(await screen.findByText('Coding tasks need at least one repository that Pi can modify.')).toBeInTheDocument()
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith(undefined))
  })

  test('preserves legacy scope and checks only for the migrated original repository', async () => {
    const old = repository('repo-old', 'old')
    const modern = repository('repo-new', 'new')
    const legacyCheck = { id: 'legacy-test', name: 'Legacy test', version: 4, command: 'go test ./...', argv: ['go', 'test', './...'], workingDirectory: '.', source: 'legacy' }
    const onReady = show(options({
      repositories: [modern, old], selectedRepoIds: ['repo-old', 'repo-new'], defaultCheckMode: 'named', legacyChecks: [legacyCheck],
      legacyScope: { repoId: 'repo-old', writableFiles: ['README.md'], writableDirectories: ['src'] },
    } as TaskOptionsView))
    await screen.findByLabelText('old')
    await waitFor(() => {
      const resources = onReady.mock.calls.at(-1)?.[0] as TaskResourceSelection[]
      expect(resources.find((item) => item.repoId === 'repo-old')).toEqual(expect.objectContaining({ scope: expect.objectContaining({ migrationChoice: 'legacy', writableFiles: ['README.md'] }), checks: expect.objectContaining({ mode: 'named', commands: [legacyCheck] }) }))
      expect(resources.find((item) => item.repoId === 'repo-new')).toEqual(expect.objectContaining({ scope: expect.objectContaining({ migrationChoice: 'not_needed', mode: 'repository' }), checks: expect.objectContaining({ mode: 'auto' }) }))
    })
    expect(screen.getByLabelText('Keep the previous file scope')).toBeChecked()
    await userEvent.click(screen.getByLabelText('Use the selected repository'))
    await waitFor(() => expect((onReady.mock.calls.at(-1)?.[0] as TaskResourceSelection[]).find((item) => item.repoId === 'repo-old')?.scope).toEqual(expect.objectContaining({ mode: 'repository', migrationChoice: 'repository' })))
  })

  test('supports automatic, named, and explicit no-check modes without fallback', async () => {
    const named = { id: 'unit', name: 'Unit tests', version: 2, command: 'npm run test', argv: ['npm', 'run', 'test'], workingDirectory: '.', source: 'user' }
    const fetcher = vi.fn(async (input: string | URL | Request) => auxiliary(input) ?? (String(input).endsWith('/checks') ? response({ checks: [named] }) : response(options())))
    vi.stubGlobal('fetch', fetcher)
    const onReady = vi.fn()
    render(<LanguageProvider><TaskResources projectId="project-1" roomId="room-1" busy={false} onReady={onReady} /></LanguageProvider>)
    await userEvent.click(await screen.findByLabelText('Use selected checks'))
    await userEvent.click(await screen.findByLabelText(/Unit tests/))
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith([expect.objectContaining({ checks: expect.objectContaining({ mode: 'named', commands: [named] }) })]))
    await userEvent.click(screen.getByLabelText('Run no checks · Unverified'))
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith([expect.objectContaining({ checks: expect.objectContaining({ mode: 'none', commands: [] }) })]))
  })

  test('adds a plain custom command with a spaced directory selected from the browser', async () => {
    const fetcher = vi.fn(async (input: string | URL | Request, _init?: RequestInit) => {
      const extra = auxiliary(input); if (extra) return extra
      if (String(input).endsWith('/checks')) return response({ checks: [] })
      if (String(input).includes('/entries?')) return response({ entries: [{ name: 'client tests', path: 'web/client tests', kind: 'directory' }], nextCursor: '', truncated: false, revision: 'a'.repeat(40) })
      return response(options())
    })
    vi.stubGlobal('fetch', fetcher)
    const onReady = vi.fn()
    render(<LanguageProvider><TaskResources projectId="project-1" roomId="room-1" busy={false} onReady={onReady} /></LanguageProvider>)
    await userEvent.click(await screen.findByLabelText('Use selected checks'))
    await userEvent.type(screen.getByLabelText('Custom check name'), 'Focused tests')
    await userEvent.type(screen.getByLabelText('Command'), `npm run 'unit tests'`)
    await userEvent.click(screen.getByRole('button', { name: 'Choose check directory…' }))
    const picker = await screen.findByRole('dialog', { name: 'Choose repository directory' })
    await userEvent.click(within(picker).getByRole('button', { name: 'client tests' }))
    await userEvent.click(within(picker).getByRole('button', { name: 'Use this directory' }))
    await userEvent.click(screen.getByRole('button', { name: 'Add custom check' }))
    await waitFor(() => {
      const check = (onReady.mock.calls.at(-1)?.[0] as TaskResourceSelection[])[0].checks.commands[0]
      expect(check).toEqual(expect.objectContaining({ name: 'Focused tests', command: `npm run 'unit tests'`, workingDirectory: 'web/client tests', source: 'user', version: 0 }))
      expect(check.id).toMatch(/^custom-/)
    })
    expect(screen.getByLabelText('Save this check to the repository')).not.toBeChecked()
    expect(fetcher.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
  })

  test('explicitly saves a custom check and uses the returned repository version', async () => {
    const existing = { id: 'unit', name: 'Unit tests', version: 2, command: 'npm run test', argv: ['npm', 'run', 'test'], workingDirectory: '.', source: 'user' }
    let resolveSave!: (value: Response) => void
    const saveResponse = new Promise<Response>((resolve) => { resolveSave = resolve })
    const fetcher = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const extra = auxiliary(input); if (extra) return extra
      if (String(input).endsWith('/checks') && init?.method === 'PUT') return saveResponse
      if (String(input).endsWith('/checks')) return response({ checks: [existing] })
      return response(options())
    })
    vi.stubGlobal('fetch', fetcher)
    const onReady = vi.fn()
    render(<LanguageProvider><TaskResources projectId="project-1" roomId="room-1" busy={false} onReady={onReady} /></LanguageProvider>)
    await userEvent.click(await screen.findByLabelText('Use selected checks'))
    await userEvent.click(await screen.findByLabelText(/Unit tests/))
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith([expect.objectContaining({ checks: expect.objectContaining({ commands: [existing] }) })]))

    await userEvent.type(screen.getByLabelText('Custom check name'), 'Focused tests')
    await userEvent.type(screen.getByLabelText('Command'), 'npm run focused')
    await userEvent.click(screen.getByLabelText('Save this check to the repository'))
    const add = screen.getByRole('button', { name: 'Add custom check' })
    await userEvent.click(add)
    expect(await screen.findByText('Saving check…')).toBeInTheDocument()
    expect(add).toBeDisabled()
    await userEvent.click(add)
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith(undefined))

    const puts = fetcher.mock.calls.filter(([, init]) => init?.method === 'PUT')
    expect(puts).toHaveLength(1)
    expect(String(puts[0][0])).toBe('/api/v2/projects/project-1/repositories/repo-one/checks')
    const submitted = JSON.parse(String(puts[0][1]?.body)).checks[0]
    expect(submitted).toEqual(expect.objectContaining({ name: 'Focused tests', command: 'npm run focused', version: 0, source: 'user' }))
    const saved = { ...submitted, argv: ['npm', 'run', 'focused'], version: 1, source: 'user' }
    resolveSave(response({ checks: [existing, saved] }))

    await waitFor(() => {
      const resources = onReady.mock.calls.at(-1)?.[0] as TaskResourceSelection[]
      expect(resources[0].checks.commands).toEqual([existing, saved])
    })
    expect(screen.getByLabelText('Custom check name')).toHaveValue('')
    expect(screen.getByLabelText('Command')).toHaveValue('')
    expect(screen.getByLabelText('Save this check to the repository')).not.toBeChecked()
  })

  test('keeps the custom draft and readiness closed when repository save conflicts', async () => {
    const existing = { id: 'unit', name: 'Unit tests', version: 2, command: 'npm run test', argv: ['npm', 'run', 'test'], workingDirectory: '.', source: 'user' }
    const fetcher = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const extra = auxiliary(input); if (extra) return extra
      if (String(input).endsWith('/checks') && init?.method === 'PUT') return response({ error: 'check version conflict' }, false, 409)
      if (String(input).endsWith('/checks')) return response({ checks: [existing] })
      return response(options())
    })
    vi.stubGlobal('fetch', fetcher)
    const onReady = vi.fn()
    render(<LanguageProvider><TaskResources projectId="project-1" roomId="room-1" busy={false} onReady={onReady} /></LanguageProvider>)
    await userEvent.click(await screen.findByLabelText('Use selected checks'))
    await userEvent.click(await screen.findByLabelText(/Unit tests/))
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith(expect.any(Array)))
    await userEvent.type(screen.getByLabelText('Custom check name'), 'Focused tests')
    await userEvent.type(screen.getByLabelText('Command'), 'npm run focused')
    await userEvent.click(screen.getByLabelText('Save this check to the repository'))
    await userEvent.click(screen.getByRole('button', { name: 'Add custom check' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('check version conflict')
    expect(screen.getByLabelText('Custom check name')).toHaveValue('Focused tests')
    expect(screen.getByLabelText('Command')).toHaveValue('npm run focused')
    expect(screen.getByLabelText('Save this check to the repository')).toBeChecked()
    expect(screen.getByRole('button', { name: 'Add custom check' })).toBeEnabled()
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith(undefined))
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1)
    await userEvent.click(screen.getByLabelText('Run no checks · Unverified'))
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith([expect.objectContaining({ checks: expect.objectContaining({ mode: 'none', commands: [] }) })]))
  })

  test('ignores a save response after switching to another Room and repository', async () => {
    let resolveOldSave!: (value: Response) => void
    const oldSave = new Promise<Response>((resolve) => { resolveOldSave = resolve })
    const fetcher = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      const extra = auxiliary(input); if (extra) return extra
      if (path.includes('/repo-one/checks') && init?.method === 'PUT') return oldSave
      if (path.endsWith('/checks')) return response({ checks: [] })
      if (path.includes('/rooms/room-2/')) return response(options({ repositories: [repository('repo-two', 'backend')], selectedRepoIds: ['repo-two'] }))
      return response(options())
    })
    vi.stubGlobal('fetch', fetcher)
    const onReady = vi.fn()
    const view = render(<LanguageProvider><TaskResources projectId="project-1" roomId="room-1" busy={false} onReady={onReady} /></LanguageProvider>)
    await userEvent.click(await screen.findByLabelText('Use selected checks'))
    await userEvent.type(screen.getByLabelText('Custom check name'), 'Old Room check')
    await userEvent.type(screen.getByLabelText('Command'), 'npm run old')
    await userEvent.click(screen.getByLabelText('Save this check to the repository'))
    await userEvent.click(screen.getByRole('button', { name: 'Add custom check' }))
    expect(await screen.findByText('Saving check…')).toBeInTheDocument()

    view.rerender(<LanguageProvider><TaskResources projectId="project-2" roomId="room-2" busy={false} onReady={onReady} /></LanguageProvider>)
    expect(await screen.findByLabelText('backend')).toBeChecked()
    resolveOldSave(response({ checks: [{ id: 'old', name: 'Old Room check', version: 1, command: 'npm run old', argv: ['npm', 'run', 'old'], workingDirectory: '.', source: 'user' }] }))
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith([expect.objectContaining({ repoId: 'repo-two' })]))
    expect(screen.queryByText(/Old Room check/)).not.toBeInTheDocument()
  })

  test('holds readiness during loading and errors, and disables selection while busy', async () => {
    let resolve!: (value: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((done) => { resolve = done })))
    const onReady = vi.fn()
    const view = render(<LanguageProvider><TaskResources projectId="project-1" roomId="room-1" busy={false} onReady={onReady} /></LanguageProvider>)
    expect(screen.getByRole('status')).toHaveTextContent('Loading task resources…')
    expect(onReady).toHaveBeenLastCalledWith(undefined)
    resolve(response(options()))
    expect(await screen.findByLabelText('frontend')).toBeEnabled()
    view.rerender(<LanguageProvider><TaskResources projectId="project-1" roomId="room-1" busy onReady={onReady} /></LanguageProvider>)
    expect(screen.getByLabelText('frontend')).toBeDisabled()

    vi.stubGlobal('fetch', vi.fn(async () => response({ error: 'options unavailable' }, false, 503)))
    view.rerender(<LanguageProvider><TaskResources projectId="project-1" roomId="room-2" busy={false} onReady={onReady} /></LanguageProvider>)
    expect(await screen.findByRole('alert')).toHaveTextContent('options unavailable')
    expect(onReady).toHaveBeenLastCalledWith(undefined)
  })
})

test('reports which repository prerequisite prevents starting and clears it after completion', async () => {
  const onReady = vi.fn()
  const onBlockedChange = vi.fn()
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
    if (String(input).endsWith('/delivery-defaults')) return response({ targetRef: '', suggestedTargetRef: '', version: 1 })
    return auxiliary(input) ?? response(options())
  }))
  render(<LanguageProvider><TaskResources projectId="project-1" roomId="room-1" busy={false} onReady={onReady} onBlockedChange={onBlockedChange} /></LanguageProvider>)
  await screen.findByRole('combobox', { name: 'frontend Target branch' })
  await waitFor(() => expect(onBlockedChange).toHaveBeenLastCalledWith('frontend: Choose a target branch for this repository.'))
  expect(onReady).toHaveBeenLastCalledWith(undefined)
  await userEvent.selectOptions(screen.getByRole('combobox'), 'refs/heads/main')
  await waitFor(() => expect(onBlockedChange).toHaveBeenLastCalledWith(''))
  await userEvent.click(screen.getByRole('radio', { name: 'Reference only' }))
  await waitFor(() => expect(onBlockedChange).toHaveBeenLastCalledWith('Set at least one selected repository to Can modify.'))
  expect(onReady).toHaveBeenLastCalledWith(undefined)
})
