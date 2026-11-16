import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import { ResearchWorkflow, type ResearchExecution } from './ResearchWorkflow'

const workflow: ResearchExecution = { id: 'workflow-1', taskId: 'task-1', projectId: 'project-1', version: 2, status: 'waiting', stopRequested: false, gate: { id: 'gate-1', version: 2, inputDigest: 'digest-1', report: { summary: 'Use the existing task boundary.', citations: ['readme'], no_answer: false } }, projection: { evidence: { readme: { url: 'https://example.org/readme', digest: 'doc-1' } } } }
afterEach(() => vi.unstubAllGlobals())
function show(){return render(<LanguageProvider><ResearchWorkflow taskId="task-1" roomId="room-1" onNavigate={vi.fn()} /></LanguageProvider>)}

test('confirm binds the displayed gate and edited instructions', async () => {
  const fetcher = vi.fn().mockResolvedValueOnce({ ok: true, json: async () => ({ enabled: true, workflow }) }).mockResolvedValueOnce({ ok: true, json: async () => ({ ...workflow, version: 3, status: 'confirming' }) })
  vi.stubGlobal('fetch', fetcher); show()
  await screen.findByText('Use the existing task boundary.')
  expect(screen.getByRole('link', { name: 'readme' })).toHaveAttribute('href', 'https://example.org/readme')
  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Preserve the current schema.' } })
  fireEvent.click(screen.getByRole('button', { name: 'Confirm and code' }))
  await screen.findByText('Saving your decision')
  const [path, init] = fetcher.mock.calls[1]
  expect(path).toBe('/api/projects/project-1/workflows/workflow-1/decision')
  expect(JSON.parse(init.body)).toEqual({ version: 2, gateId: 'gate-1', inputDigest: 'digest-1', decision: 'accept', instructions: 'Preserve the current schema.' })
  expect(init.headers['Idempotency-Key']).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Confirm and code' })).not.toBeInTheDocument()
})

test('no-answer and unresolved states require human attention', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ enabled: true, workflow: { ...workflow, gate: { ...workflow.gate, report: { ...workflow.gate!.report, no_answer: true } } } }) }))
  const view = show()
  expect(await screen.findByRole('button', { name: 'Confirm and code' })).toBeDisabled()
  expect(screen.getByText('The available sources cannot answer this question.')).toBeInTheDocument()
  view.unmount()
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ workflow: { ...workflow, status: 'unresolved' } }) }))
  show()
  expect(await screen.findByRole('button', { name: 'Reconcile existing operation' })).toBeEnabled()
})

test('SSE ignores older projections and reconnects to persisted history', async () => {
  let update: (event: MessageEvent) => void = () => {}
  const close = vi.fn()
  const sources: string[] = []
  class Source { constructor(url: string){ sources.push(url) } addEventListener(_name: string, listener: typeof update){ update = listener } close = close }
  vi.stubGlobal('EventSource', Source)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ workflow }) }))
  const view = show()
  await screen.findByText('Review the research proposal')
  update({ data: JSON.stringify({ data: { ...workflow, version: 4, status: 'stopped', stopRequested: true } }) } as MessageEvent)
  await screen.findByText('Stopped')
  update({ data: JSON.stringify({ data: { ...workflow, version: 3, status: 'coding' } }) } as MessageEvent)
  await waitFor(() => expect(screen.getByText('Stopped')).toBeInTheDocument())
  expect(sources[0]).toBe('/api/projects/project-1/workflows/workflow-1/events')
  view.unmount(); expect(close).toHaveBeenCalled()
})

test('existing task offers an explicit research entry without starting Pi', async () => {
  const fetcher = vi.fn().mockResolvedValueOnce({ ok: true, json: async () => ({ enabled: true, eligible: true, projectId: 'project-1', question: 'Research Chora', workflow: null }) }).mockResolvedValueOnce({ ok: true, json: async () => ({ ...workflow, status: 'researching', gate: undefined }) })
  vi.stubGlobal('fetch', fetcher);show()
  fireEvent.click(await screen.findByRole('button', { name: 'Start research before coding' }))
  await screen.findByText('Researching')
  expect(fetcher.mock.calls[1][0]).toBe('/api/tasks/task-1/workflow')
  expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({ projectId: 'project-1', question: 'Research Chora' })
})
