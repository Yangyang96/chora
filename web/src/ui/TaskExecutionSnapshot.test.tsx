import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test } from 'vitest'
import { LanguageProvider, useI18n } from '../i18n'
import type { TaskExecutionSettingsSnapshot } from '../types'
import { TaskExecutionSnapshot } from './TaskExecutionSnapshot'

const snapshot: TaskExecutionSettingsSnapshot = {
  projectId: 'project-1', projectVersion: 4, agentExecutionProfile: 'trusted_local', environmentSource: 'project', modelSource: 'task',
  modelBinding: { catalog: { agentId: 'pi', runtimeIdentity: 'pi', runtimeVersion: '1', models: [], digest: 'd' }, provider: 'openai', modelId: 'gpt-frozen', selectedAt: '2026-09-20T00:00:00Z', source: 'coding_agent' },
}
function ChineseSnapshot({ value }: { value: TaskExecutionSettingsSnapshot }) { const { setLocale } = useI18n(); return <><button onClick={() => setLocale('zh-CN')}>中文</button><TaskExecutionSnapshot value={value} /></> }

test('shows the persisted Task snapshot as creation-time provenance rather than current Project settings', async () => {
  render(<LanguageProvider><TaskExecutionSnapshot value={snapshot} /></LanguageProvider>)
  await userEvent.click(screen.getByText('Task creation execution settings'))
  expect(screen.getByText(/Frozen when this task was created/)).toHaveTextContent(/Current Project defaults and later successor configuration do not change this record/)
  expect(screen.getByText(/openai · gpt-frozen/)).toHaveTextContent('task override')
  expect(screen.getByText(/Project settings version/)).toHaveTextContent('4')
})

test('translates new execution settings and provenance copy', async () => {
  render(<LanguageProvider><ChineseSnapshot value={{ ...snapshot, modelBinding: null, modelSource: 'default' }} /></LanguageProvider>)
  await userEvent.click(screen.getByRole('button', { name: '中文' }))
  await userEvent.click(screen.getByText('任务创建时的执行设置'))
  expect(screen.getByText(/此记录在任务创建时固定/)).toBeInTheDocument()
  expect(screen.getByText(/运行时默认模型/)).toHaveTextContent('运行时默认模型')
  expect(screen.getByText(/项目设置版本/)).toHaveTextContent('4')
})
