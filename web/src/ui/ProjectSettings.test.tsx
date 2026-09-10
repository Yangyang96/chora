import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import { ProjectSettings } from './ProjectSettings'

const original = { version: 2, writableFiles: ['README.md'], writableDirectories: ['src'], verificationCommands: [{ argv: ['node', 'check.js'], workingDirectory: 'tools' }], noChecks: false }
function response(body: unknown) { return { ok: true, status: 200, json: async () => body } as Response }

test('persists explicit no-check settings on the actual Project with optimistic version', async () => {
  const writes: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: unknown, init?: RequestInit) => {
    expect(String(input)).toBe('/api/projects/project-1/settings')
    if (init?.method === 'PUT') { const body = JSON.parse(String(init.body)); writes.push(body); return response({ ...body, version: 3 }) }
    return response(original)
  }))
  render(<LanguageProvider><ProjectSettings projectId="project-1" /></LanguageProvider>)
  await screen.findByDisplayValue('README.md')
  await userEvent.click(screen.getByRole('checkbox'))
  await userEvent.click(screen.getByRole('button', { name: 'Save settings' }))
  await screen.findByText('Settings saved. Existing tasks are unchanged.')
  expect(writes).toEqual([{ ...original, noChecks: true, verificationCommands: [] }])
})

test('shows exact argv and working directory before launch without changing settings', async () => {
  const fetcher = vi.fn(async () => response(original)); vi.stubGlobal('fetch', fetcher)
  render(<LanguageProvider><ProjectSettings projectId="project-1" readOnly /></LanguageProvider>)
  await screen.findByText('tools: ["node","check.js"]')
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
  expect(fetcher).toHaveBeenCalledTimes(1)
})

test('discards late settings from a previously selected Project', async () => {
  let resolve!: (value: Response) => void
  vi.stubGlobal('fetch', vi.fn((input: unknown) => String(input).includes('project-1') ? new Promise<Response>((r) => { resolve = r }) : Promise.resolve(response({ ...original, writableFiles: ['new.txt'] }))))
  const view = render(<LanguageProvider><ProjectSettings projectId="project-1" /></LanguageProvider>)
  view.rerender(<LanguageProvider><ProjectSettings projectId="project-2" /></LanguageProvider>)
  await screen.findByDisplayValue('new.txt')
  resolve(response(original))
  await waitFor(() => expect(screen.queryByDisplayValue('README.md')).not.toBeInTheDocument())
})
