import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'
import { IsolatedLocal } from './IsolatedLocal'
import type { IsolatedLocalState, IsolatedLocalView } from '../types'

function state(value: IsolatedLocalState, preparationAvailable = true): IsolatedLocalView {
  return { state: value, reason: `${value} reason`, preparationAvailable, imageId: value === 'ready' ? 'sha256:pinned' : undefined,
    piVersion: '0.85.1', nodeVersion: '22.19.0', policy: { network: 'restricted', resources: 'selected repositories', files: 'task worktrees', credentials: 'Selected DeepSeek API key only' } }
}

describe('IsolatedLocal', () => {
  test.each([
    ['ready', 'Isolated Local is ready'],
    ['preparing', 'Preparing Isolated Local…'],
    ['failed', 'Isolated Local preparation failed'],
    ['restart_required', 'Restart Chora to finish preparation'],
  ] as const)('renders %s without offering a host fallback', (value, message) => {
    render(<IsolatedLocal value={state(value)} onPrepare={vi.fn()} />)
    expect(screen.getByText(message)).toBeInTheDocument()
    expect(screen.queryByText(/fallback/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/use Trusted Local/i)).not.toBeInTheDocument()
  })

  test('starts preparation only when the API allows it', async () => {
    const prepare = vi.fn(async () => {})
    const { rerender } = render(<IsolatedLocal value={state('not_prepared')} onPrepare={prepare} />)
    await userEvent.click(screen.getByRole('button', { name: 'Prepare Isolated Local' }))
    expect(prepare).toHaveBeenCalledOnce()
    rerender(<IsolatedLocal value={state('not_prepared', false)} onPrepare={prepare} />)
    expect(screen.queryByRole('button', { name: 'Prepare Isolated Local' })).not.toBeInTheDocument()
  })
})
