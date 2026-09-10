import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import { RepositoryDeliveryDefaults } from './RepositoryDeliveryDefaults'

function response(body: unknown, ok = true, status = 200): Response { return { ok, status, json: async () => body } as Response }

describe('RepositoryDeliveryDefaults', () => {
  test('requires an explicit save before adopting a discovered suggestion', async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/branches?limit=100')) return response({ branches: [{ ref: 'refs/heads/main', commit: 'a'.repeat(40) }, { ref: 'refs/heads/release', commit: 'b'.repeat(40) }], nextCursor: '' })
      if (init?.method === 'PUT') return response({ targetRef: 'refs/heads/main', version: 2, suggestedTargetRef: '', reason: '' })
      return response({ targetRef: '', version: 1, suggestedTargetRef: 'refs/heads/main', reason: 'Remote HEAD identifies main.' })
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><RepositoryDeliveryDefaults repoId="repo-a" /></LanguageProvider>)

    expect(fetchMock).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Configure delivery default' }))
    expect(await screen.findByLabelText('Default target branch')).toHaveValue('refs/heads/main')
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: 'Save delivery default' }))
    await waitFor(() => expect(screen.getByText(/Default target branch/)).toHaveTextContent('main'))
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === 'PUT')
    expect(JSON.parse(String(put?.[1]?.body))).toEqual({ expectedVersion: 1, targetRef: 'refs/heads/main' })
  })
})
