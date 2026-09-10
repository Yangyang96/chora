import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test, vi } from 'vitest'
import { NewTask } from './NewTask'

test('Start explains missing task requirements, blocks submission and clears when ready', async () => {
 const onSubmit = vi.fn()
 render(<NewTask roomName="Demo" busy={false} onCancel={vi.fn()} onSubmit={onSubmit} onAcknowledgeTrustedLocal={vi.fn()} />)
 expect(screen.getByRole('button', {name:'Start'})).toBeDisabled()
 await userEvent.click(screen.getByRole('group', {name:'Start'}))
 expect(screen.getByRole('tooltip')).toHaveTextContent('Enter the task requirement.')
 expect(onSubmit).not.toHaveBeenCalled()
 await userEvent.type(screen.getByRole('textbox'), 'Fix the parser')
 expect(screen.getByRole('button', {name:'Start'})).toBeEnabled()
 await userEvent.click(screen.getByRole('button', {name:'Start'}))
 expect(onSubmit).toHaveBeenCalledWith('Fix the parser', 'standard')
})
