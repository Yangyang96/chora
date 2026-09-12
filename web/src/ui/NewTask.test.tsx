import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test, vi } from 'vitest'
import type { IsolatedLocalState } from '../types'
import { NewTask } from './NewTask'

const isolatedReady = {
 state: 'ready', reason: 'Ready', preparationAvailable: true, imageId: 'sha256:pinned',
 piVersion: '0.85.1', nodeVersion: '22.19.0',
 policy: { network: 'restricted', resources: 'selected repositories', files: 'task worktrees', credentials: 'OpenAI Codex OAuth only' },
} as const

test('Start explains missing task requirements, blocks submission and clears when ready', async () => {
 const onSubmit = vi.fn()
 render(<NewTask roomName="Demo" busy={false} isolatedLocal={isolatedReady} onPrepareIsolatedLocal={vi.fn()} onCancel={vi.fn()} onSubmit={onSubmit} onAcknowledgeTrustedLocal={vi.fn()} />)
 expect(screen.getByRole('button', {name:'Start'})).toBeDisabled()
 await userEvent.click(screen.getByRole('group', {name:'Start'}))
 expect(screen.getByRole('tooltip')).toHaveTextContent('Enter the task requirement.')
 expect(onSubmit).not.toHaveBeenCalled()
 await userEvent.type(screen.getByRole('textbox'), 'Fix the parser')
 expect(screen.getByRole('button', {name:'Start'})).toBeEnabled()
 await userEvent.click(screen.getByRole('button', {name:'Start'}))
 expect(onSubmit).toHaveBeenCalledWith('Fix the parser', 'isolated_local')
})

test.each(['not_prepared', 'preparing', 'failed', 'restart_required'] satisfies IsolatedLocalState[])('%s Isolated Local cannot start a task', async (state) => {
 const onSubmit = vi.fn()
 render(<NewTask roomName="Demo" busy={false} initialRequirement="Fix the parser" isolatedLocal={{ ...isolatedReady, state }} onPrepareIsolatedLocal={vi.fn()} onCancel={vi.fn()} onSubmit={onSubmit} onAcknowledgeTrustedLocal={vi.fn()} />)
 expect(screen.getByRole('radio', { name: 'Isolated Local' })).toBeChecked()
 expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
 expect(onSubmit).not.toHaveBeenCalled()
})
