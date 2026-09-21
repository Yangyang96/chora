import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, test, vi } from 'vitest'
import { api } from '../api'
import { LanguageProvider } from '../i18n'
import type { RunView } from '../types'
import { ProjectDocument, type ProjectDocumentView } from './ProjectDocument'

vi.mock('../api', () => ({ api: vi.fn(), commandKey: () => 'key', message: (reason: unknown) => String(reason) }))
const mockedApi = vi.mocked(api)
const run = { id: 'run-1', task: { id: 'task-1' }, status: 'awaiting_review', outcomeKind: 'document', materials: [{ title: 'Workflow', locator: 'docs/workflows.md', body: 'Supplied material', digest: 'source-hash' }], resourceResult: { group: { attemptId: 'attempt-1', markdown: '# Proposal' } } } as RunView
const empty: ProjectDocumentView = { taskId: 'task-1', roomId: 'room-1', version: 0, status: 'empty', revisions: [], reviews: [] }
const pending: ProjectDocumentView = { ...empty, version: 1, status: 'pending', revisions: [{ id: 'revision-1', number: 1, kind: 'agent_initial', body: '# Proposal', bodyDigest: 'body-hash', source: { runId: 'run-1', attemptId: 'attempt-1', resultId: 'result-1', agentReportId: 'report-1', eventId: 'event-1', resultDigest: 'result-hash', sourceTextDigest: 'text-hash' }, editNote: '', actorId: 'local-human', createdAt: '2026-09-21', status: 'pending' }] }

beforeEach(() => mockedApi.mockReset())

test('reading never imports or accepts a proposal; explicit acceptance binds the displayed revision', async () => {
  mockedApi.mockResolvedValueOnce(empty).mockResolvedValueOnce(pending).mockResolvedValueOnce({ ...pending, status: 'accepted', revisions: [{ ...pending.revisions[0], status: 'accepted', acceptedContextRevisionId: 'context-1' }] })
  const onChanged = vi.fn()
  render(<LanguageProvider><ProjectDocument run={run} onChanged={onChanged} /></LanguageProvider>)
  const save = await screen.findByRole('button', { name: 'Save agent proposal for review' })
  expect(mockedApi).toHaveBeenCalledTimes(1)
  await userEvent.click(save)
  expect(JSON.parse(mockedApi.mock.calls[1][1]!.body as string)).toEqual({ expectedVersion: 0, note: '', sourceAttemptId: 'attempt-1' })
  await userEvent.click(await screen.findByRole('button', { name: 'Accept this revision' }))
  expect(mockedApi.mock.calls[2][0]).toBe('/api/tasks/task-1/document/reviews')
  expect(JSON.parse(mockedApi.mock.calls[2][1]!.body as string)).toEqual({ expectedVersion: 1, note: '', kind: 'accept' })
  expect(await screen.findByRole('status')).toHaveTextContent('context-1')
  expect(onChanged).toHaveBeenCalledTimes(2)
})

test('unsaved edits block acceptance and stale failures preserve the edit', async () => {
  mockedApi.mockResolvedValueOnce(pending).mockRejectedValueOnce(new Error('document version conflict'))
  render(<LanguageProvider><ProjectDocument run={run} /></LanguageProvider>)
  const editor = await screen.findByLabelText('Document Markdown')
  await userEvent.clear(editor)
  await userEvent.type(editor, '# Human revision')
  expect(screen.getByRole('button', { name: 'Accept this revision' })).toBeDisabled()
  await userEvent.click(screen.getByRole('button', { name: 'Save new revision' }))
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('document version conflict'))
  expect(editor).toHaveValue('# Human revision')
  expect(JSON.parse(mockedApi.mock.calls[1][1]!.body as string)).toEqual({ expectedVersion: 1, note: '', body: '# Human revision' })
})
