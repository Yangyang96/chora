import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import { ActionButton } from './ActionButton'

test('explains disabled actions on hover, keyboard focus and tap without submitting', async () => {
  const click = vi.fn()
  const submit = vi.fn((event) => event.preventDefault())
  const user = userEvent.setup()
  const view = render(<form onSubmit={submit}><ActionButton type="submit" disabled disabledReason="Enter a commit message." onClick={click}>Commit</ActionButton></form>)
  const button = screen.getByRole('button', { name: 'Commit' })
  const explanation = screen.getByRole('group', { name: 'Commit' })
  expect(button).toBeDisabled()
  expect(button).toHaveAccessibleDescription('Enter a commit message.')
  expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  await user.hover(explanation)
  expect(screen.getByRole('tooltip')).toHaveTextContent('Enter a commit message.')
  await user.unhover(explanation)
  expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  await user.tab()
  expect(explanation).toHaveFocus()
  expect(screen.getByRole('tooltip')).toHaveTextContent('Enter a commit message.')
  await user.keyboard('{Enter} ')
  expect(click).not.toHaveBeenCalled()
  expect(submit).not.toHaveBeenCalled()
  await user.keyboard('{Escape}')
  expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  fireEvent.click(explanation) // Touch activation follows the same click path.
  expect(screen.getByRole('tooltip')).toHaveTextContent('Enter a commit message.')
  expect(click).not.toHaveBeenCalled()
  expect(submit).not.toHaveBeenCalled()
  view.rerender(<form onSubmit={submit}><ActionButton type="submit" disabled={false} disabledReason="" onClick={click}>Commit</ActionButton></form>)
  expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Commit' }))
  expect(click).toHaveBeenCalledTimes(1)
  expect(submit).toHaveBeenCalledTimes(1)
})

test('updates the exact unmet requirement and provides Chinese explanations for icon buttons', async () => {
  window.localStorage.setItem('chora.locale', 'zh-CN')
  const view = render(<LanguageProvider><ActionButton disabled disabledReason="Enter a check name." aria-label="Save"><svg /></ActionButton></LanguageProvider>)
  await userEvent.click(screen.getByRole('group', { name: 'Save' }))
  expect(screen.getByRole('tooltip')).toHaveTextContent('请填写检查名称。')
  view.rerender(<LanguageProvider><ActionButton disabled disabledReason="Enter the check command." aria-label="Save"><svg /></ActionButton></LanguageProvider>)
  expect(screen.getByRole('tooltip')).toHaveTextContent('请填写检查命令。')
  expect(screen.getByRole('button', { name: 'Save' })).toHaveAccessibleDescription('请填写检查命令。')
  window.localStorage.removeItem('chora.locale')
})
