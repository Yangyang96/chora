import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import { DeliveryDraftEditor, type DeliveryText } from './DeliveryDraftEditor'

const original = 'fix(auth): reject empty tokens\n\nReject empty token values before starting a session.'
const revised = 'fix(auth): validate session tokens\n\nReturn a validation error for empty tokens before creating a session.'
const context = { owner: 'owner-a', fingerprint: 'source-a', templates: [] as string[], warnings: [] }
const empty: DeliveryText = { message: '', title: '', body: '' }
function response(body: unknown, ok = true): Response { return { ok, status: ok ? 200 : 422, json: async () => body } as Response }
const suggestion = (text = original) => ({ message: text, title: '', body: '', provider: 'native', model: 'selected', fingerprint: context.fingerprint })
function props() { return { runId: 'run-a', repoId: 'repo-a', resultDigest: 'result-a', expectedVersion: 2, kind: 'commit' as const, refreshToken: 0, value: empty, disabled: false, disabledReason: 'Preview active', onChange: vi.fn(), onReady: vi.fn() } }
function editor(p = props()) { return render(<LanguageProvider><DeliveryDraftEditor {...p} /></LanguageProvider>) }

describe('DeliveryDraftEditor', () => {
  beforeEach(() => window.localStorage.clear())

  test.each(['commit', 'pr'] as const)('%s revision sends only text fields from a reused delivery draft, including retries', async (kind) => {
    const inherited = { message: original, title: 'feat: existing title', body: '- Existing change', remote: 'upstream' }
    const requests: Array<Record<string, unknown>> = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
      if (url.endsWith('draft-context')) return response(context)
      const body = JSON.parse(String(init.body))
      requests.push(body)
      if (Object.keys(body.current).some((key) => !['message', 'title', 'body'].includes(key))) return response({ error: 'Unexpected draft field' }, false)
      if (requests.length === 1) return response({ error: 'Provider unavailable' }, false)
      return response({ ...suggestion(revised), title: 'feat: revised title', body: '- Short revision' })
    }))
    render(<LanguageProvider><DeliveryDraftEditor {...props()} kind={kind} value={inherited} /></LanguageProvider>)
    await waitFor(() => expect(screen.getByRole('button', { name: kind === 'commit' ? 'Generate commit message' : 'Generate pull request description' })).toBeEnabled())
    await userEvent.click(screen.getByText('Ask AI to revise'))
    await userEvent.type(screen.getByLabelText('Revision instructions'), 'Use one short bullet')
    await userEvent.click(screen.getByRole('button', { name: 'Revise draft' }))
    await screen.findByText('Provider unavailable')
    expect(requests[0].current).toEqual({ message: original, title: inherited.title, body: inherited.body })
    await userEvent.click(screen.getByRole('button', { name: 'Retry draft generation' }))
    await screen.findByRole('group', { name: 'New draft candidate' })
    expect(requests[1]).toEqual(requests[0])
    expect(requests[1].feedback).toBe('Use one short bullet')
    await userEvent.click(screen.getByRole('button', { name: 'Use this draft' }))
    expect(screen.getByLabelText(kind === 'commit' ? 'Commit message' : 'Title')).toHaveValue(kind === 'commit' ? revised : 'feat: revised title')
    expect(inherited.remote).toBe('upstream')
  })

  test('defaults to English in a Chinese UI and restores edited text after reopening without another generation', async () => {
    window.localStorage.setItem('chora.locale', 'zh-CN')
    const requests: Record<string, unknown>[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
      if (url.endsWith('draft-context')) return response(context)
      requests.push(JSON.parse(String(init.body))); return response(suggestion())
    }))
    const view = editor()
    await waitFor(() => expect(screen.getByLabelText('提交说明')).toHaveValue(original))
    expect(requests[0].language).toBe('en')
    await userEvent.clear(screen.getByLabelText('提交说明'))
    await userEvent.type(screen.getByLabelText('提交说明'), 'fix: my carefully edited description\n\nKeep this wording after reopening.')
    view.unmount(); editor()
    await waitFor(() => expect(screen.getByLabelText('提交说明')).toHaveValue('fix: my carefully edited description\n\nKeep this wording after reopening.'))
    expect(requests).toHaveLength(1)
  })

  test('a late response becomes a candidate; adopting and undoing preserve human text', async () => {
    let finish!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => { finish = resolve })
    vi.stubGlobal('fetch', vi.fn(async (url: string) => url.endsWith('draft-context') ? response(context) : pending))
    editor()
    await screen.findByText('Generating a draft from reviewed changes…')
    await userEvent.type(screen.getByLabelText('Commit message'), 'my human draft')
    finish(response(suggestion(revised)))
    await screen.findByRole('group', { name: 'New draft candidate' })
    expect(screen.getByLabelText('Commit message')).toHaveValue('my human draft')
    await userEvent.click(screen.getByRole('button', { name: 'Use this draft' }))
    expect(screen.getByLabelText('Commit message')).toHaveValue(revised)
    await userEvent.click(screen.getByRole('button', { name: 'Undo draft replacement' }))
    expect(screen.getByLabelText('Commit message')).toHaveValue('my human draft')
  })

  test('explicit regeneration has a new attempt and failure retry preserves that attempt', async () => {
    const requests: Record<string, unknown>[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
      if (url.endsWith('draft-context')) return response(context)
      requests.push(JSON.parse(String(init.body)))
      return requests.length === 2 ? response({ error: 'Provider unavailable' }, false) : response(suggestion(requests.length > 1 ? revised : original))
    }))
    editor()
    await waitFor(() => expect(screen.getByLabelText('Commit message')).toHaveValue(original))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Generate commit message' })).toBeEnabled())
    await userEvent.click(screen.getByRole('button', { name: 'Generate commit message' }))
    await screen.findByText('Provider unavailable')
    await userEvent.click(screen.getByRole('button', { name: 'Retry draft generation' }))
    await waitFor(() => expect(screen.getByLabelText('Commit message')).toHaveValue(revised))
    expect(requests[0].generationId).toBe('')
    expect(requests[1].generationId).toBeTruthy()
    expect(requests[2]).toEqual(requests[1])
    await userEvent.click(screen.getByRole('button', { name: 'Undo draft replacement' }))
    expect(screen.getByLabelText('Commit message')).toHaveValue(original)
  })

  test('PR revision updates the AI draft, supports undo, and does not leak feedback into ordinary regeneration', async () => {
    const requests: Record<string, unknown>[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
      if (url.endsWith('draft-context')) return response({ ...context, templates: ['changes.md', 'release.md'] })
      requests.push(JSON.parse(String(init.body)))
      return response({ ...suggestion(''), title: 'fix: reject empty tokens', body: requests.at(-1)?.feedback ? 'Reject empty tokens; tests not run.' : '## Changes\nReject empty tokens.\n\n## Verification\nNot run.' })
    }))
    render(<LanguageProvider><DeliveryDraftEditor {...props()} kind="pr" /></LanguageProvider>)
    await screen.findByLabelText('Pull request template')
    expect(requests).toHaveLength(0)
    await userEvent.selectOptions(screen.getByLabelText('Pull request template'), 'changes.md')
    await waitFor(() => expect(screen.getByLabelText('Title')).toHaveValue('fix: reject empty tokens'))
    expect((screen.getByLabelText('Description') as HTMLTextAreaElement).value).toContain('Not run.')
    expect(requests[0]).toMatchObject({ template: 'changes.md', language: 'en', kind: 'pr' })
    await userEvent.click(screen.getByText('Ask AI to revise'))
    await userEvent.type(screen.getByLabelText('Revision instructions'), '简化成一句话')
    await userEvent.click(screen.getByRole('button', { name: 'Revise draft' }))
    await waitFor(() => expect(screen.getByLabelText('Description')).toHaveValue('Reject empty tokens; tests not run.'))
    expect(screen.queryByRole('group', { name: 'New draft candidate' })).not.toBeInTheDocument()
    expect(requests[1]).toMatchObject({ feedback: '简化成一句话', current: { title: 'fix: reject empty tokens' } })
    await userEvent.click(screen.getByRole('button', { name: 'Generate pull request description' }))
    await waitFor(() => expect(screen.getByLabelText('Description')).toHaveValue('## Changes\nReject empty tokens.\n\n## Verification\nNot run.'))
    expect(requests[2].feedback).toBe('')
    await userEvent.click(screen.getByRole('button', { name: 'Undo draft replacement' }))
    expect(screen.getByLabelText('Description')).toHaveValue('Reject empty tokens; tests not run.')
  })

  test('revision preserves edits made while AI is working and offers a candidate', async () => {
    let finish!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => { finish = resolve })
    let calls = 0
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('draft-context')) return response(context)
      return ++calls === 1 ? response(suggestion()) : pending
    }))
    editor()
    await waitFor(() => expect(screen.getByLabelText('Commit message')).toHaveValue(original))
    await userEvent.click(screen.getByText('Ask AI to revise'))
    await userEvent.type(screen.getByLabelText('Revision instructions'), 'Shorten the body')
    await userEvent.click(screen.getByRole('button', { name: 'Revise draft' }))
    await userEvent.clear(screen.getByLabelText('Commit message'))
    await userEvent.type(screen.getByLabelText('Commit message'), 'my newer manual wording')
    finish(response(suggestion(revised)))
    await screen.findByRole('group', { name: 'New draft candidate' })
    expect(screen.getByLabelText('Commit message')).toHaveValue('my newer manual wording')
    await userEvent.click(screen.getByRole('button', { name: 'Use this draft' }))
    expect(screen.getByLabelText('Commit message')).toHaveValue(revised)
    await userEvent.click(screen.getByRole('button', { name: 'Undo draft replacement' }))
    expect(screen.getByLabelText('Commit message')).toHaveValue('my newer manual wording')
  })

  test('changed sources mark recovered text stale and undo restores the old provenance', async () => {
    let fingerprint = context.fingerprint
    vi.stubGlobal('fetch', vi.fn(async (url: string) => url.endsWith('draft-context') ? response({ ...context, fingerprint }) : response({ ...suggestion(fingerprint === context.fingerprint ? original : revised), fingerprint })))
    const p = props(); const view = editor(p)
    await waitFor(() => expect(screen.getByLabelText('Commit message')).toHaveValue(original))
    fingerprint = 'source-b'
    view.rerender(<LanguageProvider><DeliveryDraftEditor {...p} refreshToken={1} /></LanguageProvider>)
    await screen.findByText('The draft sources changed. Generate a new candidate or review and keep your edited draft.')
    await waitFor(() => expect(p.onReady).toHaveBeenLastCalledWith(false))
    await userEvent.click(screen.getByRole('button', { name: 'Generate commit message' }))
    await screen.findByRole('group', { name: 'New draft candidate' })
    await userEvent.click(screen.getByRole('button', { name: 'Use this draft' }))
    await waitFor(() => expect(p.onReady).toHaveBeenLastCalledWith(true))
    await userEvent.click(screen.getByRole('button', { name: 'Undo draft replacement' }))
    expect(screen.getByLabelText('Commit message')).toHaveValue(original)
    await waitFor(() => expect(p.onReady).toHaveBeenLastCalledWith(false))
  })

  test('does not reuse another repository draft', async () => {
    let calls = 0
    vi.stubGlobal('fetch', vi.fn(async (url: string) => url.endsWith('draft-context') ? response(context) : (calls++, response(suggestion()))))
    const first = editor(); await waitFor(() => expect(screen.getByLabelText('Commit message')).toHaveValue(original)); first.unmount()
    editor({ ...props(), repoId: 'repo-b' })
    await waitFor(() => expect(screen.getByLabelText('Commit message')).toHaveValue(original))
    expect(calls).toBe(2)
  })
})
