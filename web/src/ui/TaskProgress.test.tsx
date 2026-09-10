import { render, screen, within } from '@testing-library/react'
import { expect, test } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { RunView } from '../types'
import { TaskProgress, workflowSteps } from './TaskProgress'

const accepted = { status: 'accepted', resourceResult: { repositories: [{ repoId: 'a', deliveryMode: 'task_branch' }], group: { repositories: [] } } } as unknown as RunView
test('renders nine readable stages in Chinese with partial delivery counts', () => {
  window.localStorage.setItem('chora.locale', 'zh-CN')
  render(<LanguageProvider><TaskProgress run={accepted} delivery={{ repositories: [{ repoId: 'a', name: '前端', status: 'cleaned' }, { repoId: 'b', name: '后端', status: 'uncommitted' }] }} /></LanguageProvider>)
  const flow = screen.getByRole('region', { name: '任务进度' })
  expect(within(flow).getByText('开发')).toBeInTheDocument()
  expect(within(flow).getAllByText('已完成 1/2 个仓库')).toHaveLength(4)
  expect(within(flow).getByText('已完成 1/2 · 待操作')).toBeInTheDocument()
  expect(within(flow).getByText('当前阶段：提交')).toBeInTheDocument()
  expect(flow.querySelectorAll('[aria-current="step"]')).toHaveLength(1)
  expect(flow.querySelector('[aria-current="step"]')).toHaveClass('stage-commit')
  expect(within(flow).getByText('未运行 / 未验证')).toBeInTheDocument()
  window.localStorage.clear()
})

test('cleaned delivery never invents passing check evidence', () => {
  const steps = workflowSteps(accepted, { repositories: [{ repoId: 'a', name: 'Frontend', status: 'cleaned' }] })
  expect(steps.slice(4).every((step) => step.state === 'done')).toBe(true)
  expect(steps[2]).toMatchObject({ state: 'skipped', detail: 'Not run / Unverified' })
})

test('no-change completion skips Review and all Git actions', () => {
  const steps = workflowSteps({ status: 'completed' } as RunView)
  expect(steps.slice(3).every((step) => step.state === 'skipped')).toBe(true)
})

test('closed PR never completes Merge or Cleanup', () => {
  const steps = workflowSteps(accepted, { repositories: [{ repoId: 'a', name: 'Frontend', status: 'pr_closed' }] })
  expect(steps[7]).toMatchObject({ state: 'attention', detail: 'PR closed · Not merged' })
  expect(steps[8].state).toBe('pending')
})

test('unavailable state does not mark any delivery stage done', () => {
  const steps = workflowSteps(accepted, { repositories: [], unavailable: true })
  expect(steps[4].state).toBe('attention')
  expect(steps.slice(4).some((step) => step.state === 'done')).toBe(false)
})

test.each(['cancelled', 'recovery_required', 'revision_required'] as const)('%s does not advance Review or delivery', (status) => {
  const steps = workflowSteps({ status } as RunView)
  expect(steps[1].state).toBe('attention')
  expect(steps.slice(3).every((step) => step.state === 'pending')).toBe(true)
})

test('legacy Apply has its own ending without invented PR milestones', () => {
  const steps = workflowSteps({ status: 'accepted', patchApplication: { state: 'applied' } } as RunView)
  expect(steps.map((step) => step.id)).toEqual(['prepare', 'develop', 'checks', 'review', 'apply'])
  expect(steps.at(-1)?.detail).toBe('Applied')
})

test('terminal states take priority over stale activity and decision metadata', () => {
  const stale = { activity: { phase: 'preparing_context' }, decisionGate: { status: 'open' } }
  const complete = workflowSteps({ status: 'completed', ...stale } as RunView)
  expect(complete.slice(3).every((step) => step.state === 'skipped')).toBe(true)
  const reviewed = workflowSteps({ ...accepted, ...stale } as RunView)
  expect(reviewed[3].state).toBe('done')
})

test.each(['checks_failed', 'checks_incomplete'] as const)('locates %s recovery at Checks, after development', (terminalReason) => {
  const steps = workflowSteps({ status: 'recovery_required', terminalReason } as RunView)
  expect(steps[1].state).toBe('done')
  expect(steps[2]).toMatchObject({ state: 'attention', detail: terminalReason === 'checks_failed' ? 'Checks failed' : 'Checks incomplete' })
  expect(steps.slice(3).every((step) => step.state === 'pending')).toBe(true)
})

test('legacy acceptance before any Apply operation never waits for branch delivery', () => {
  const steps = workflowSteps({ status: 'accepted', reviewablePatch: { files: [] } } as unknown as RunView)
  expect(steps.map((step) => step.id)).toEqual(['prepare', 'develop', 'checks', 'review', 'apply'])
  expect(steps.at(-1)?.detail).toBe('Waiting to apply')
})
