import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, test, vi } from 'vitest'
import { api } from '../api'
import { LanguageProvider } from '../i18n'
import { TaskMaterials } from './TaskMaterials'

vi.mock('../api', () => ({ api: vi.fn(), message: (reason: unknown) => String(reason) }))
const mockedApi = vi.mocked(api)

beforeEach(() => mockedApi.mockReset())

test('collects explicit material and only accepted Project document revisions', async () => {
  mockedApi.mockResolvedValue([
    { id: 'brief', title: 'Room Brief', body: 'brief', locator: 'room://room-1/brief', confirmedAt: '2026-09-20T00:00:00Z' },
    { id: 'doc-2', title: 'Architecture', body: 'accepted', locator: 'chora://project-document/task-1/revisions/7', revisionNumber: 1, confirmedAt: '2026-09-20T00:00:00Z' },
  ])
  const onMaterialsChange = vi.fn(), onRevisionIdsChange = vi.fn(), onBlockedChange = vi.fn()
  render(<LanguageProvider><TaskMaterials roomId="room-1" enabled busy={false} onMaterialsChange={onMaterialsChange} onRevisionIdsChange={onRevisionIdsChange} onBlockedChange={onBlockedChange} /></LanguageProvider>)

  expect(await screen.findByText(/Architecture/)).toBeInTheDocument()
  expect(screen.getByText(/Revision 7/)).toBeInTheDocument()
  expect(screen.queryByText(/Room Brief/)).not.toBeInTheDocument()
  await userEvent.type(screen.getByLabelText('Material title'), 'Incident notes')
  await userEvent.type(screen.getByLabelText('Source locator'), 'https://example.test/incident')
  await userEvent.type(screen.getByLabelText('Markdown content'), '# Evidence')
  await waitFor(() => expect(onMaterialsChange).toHaveBeenLastCalledWith([{ title: 'Incident notes', locator: 'https://example.test/incident', body: '# Evidence' }]))
  await userEvent.click(screen.getByLabelText(/Architecture/))
  expect(onRevisionIdsChange).toHaveBeenLastCalledWith(['doc-2'])
})

test('requires complete supplied material and limits the list to sixteen', async () => {
  mockedApi.mockResolvedValue({ revisions: [] })
  const onBlockedChange = vi.fn()
  render(<LanguageProvider><TaskMaterials roomId="room-1" enabled busy={false} onMaterialsChange={vi.fn()} onRevisionIdsChange={vi.fn()} onBlockedChange={onBlockedChange} /></LanguageProvider>)
  await waitFor(() => expect(onBlockedChange).toHaveBeenLastCalledWith('Add at least one supplied material.'))
  await userEvent.type(screen.getByLabelText('Material title'), 'Partial')
  await waitFor(() => expect(onBlockedChange).toHaveBeenLastCalledWith('Complete or remove every supplied material.'))
  for (let index = 1; index < 16; index += 1) await userEvent.click(screen.getByRole('button', { name: 'Add material' }))
  expect(screen.getAllByLabelText('Material title')).toHaveLength(16)
  expect(screen.getByRole('button', { name: 'Add material' })).toBeDisabled()
})

test('blocks oversized UTF-8 locators and aggregate material bodies', async () => {
  mockedApi.mockResolvedValue([])
  const onBlockedChange = vi.fn()
  render(<LanguageProvider><TaskMaterials roomId="room-1" enabled busy={false} onMaterialsChange={vi.fn()} onRevisionIdsChange={vi.fn()} onBlockedChange={onBlockedChange} /></LanguageProvider>)
  await userEvent.type(screen.getByLabelText('Material title'), 'Large input')
  await userEvent.type(screen.getByLabelText('Markdown content'), 'content')
  await userEvent.type(screen.getByLabelText('Source locator'), '界'.repeat(342))
  await waitFor(() => expect(onBlockedChange).toHaveBeenLastCalledWith('A source locator exceeds the 1 KiB limit.'))
  await userEvent.clear(screen.getByLabelText('Source locator'))
  await userEvent.type(screen.getByLabelText('Source locator'), 'source://fixture')
  fireEvent.change(screen.getByLabelText('Markdown content'), { target: { value: 'x'.repeat(262144) } })
  await userEvent.click(screen.getByRole('button', { name: 'Add material' }))
  fireEvent.change(screen.getAllByLabelText('Material title')[1], { target: { value: 'Second' } })
  fireEvent.change(screen.getAllByLabelText('Source locator')[1], { target: { value: 'source://second' } })
  const bodies = screen.getAllByLabelText('Markdown content')
  fireEvent.change(bodies[1], { target: { value: 'y'.repeat(262144) } })
  await userEvent.click(screen.getByRole('button', { name: 'Add material' }))
  fireEvent.change(screen.getAllByLabelText('Material title')[2], { target: { value: 'Third' } })
  fireEvent.change(screen.getAllByLabelText('Source locator')[2], { target: { value: 'source://third' } })
  fireEvent.change(screen.getAllByLabelText('Markdown content')[2], { target: { value: 'z' } })
  await waitFor(() => expect(onBlockedChange).toHaveBeenLastCalledWith('Supplied material exceeds the 512 KiB total limit.'))
})
