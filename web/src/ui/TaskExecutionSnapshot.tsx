import { useI18n } from '../i18n'
import type { TaskExecutionSettingsSnapshot } from '../types'
import { AgentExecutionDisclosure } from './AgentExecutionProfile'

const sourceKey = { project: 'Project default', default: 'runtime default', task: 'task override' } as const

export function TaskExecutionSnapshot({ value }: { value: TaskExecutionSettingsSnapshot }) {
  const { t } = useI18n()
  const model = value.modelBinding ? `${value.modelBinding.provider} · ${value.modelBinding.modelId}` : t('runtime default')
  return <details className="panel project-settings" aria-label={t('Task creation execution settings')}>
    <summary><strong>{t('Task creation execution settings')}</strong></summary>
    <p className="section-note">{t('Frozen when this task was created. Current Project defaults and later successor configuration do not change this record.')}</p>
    <p>{t('Project settings version')} · <code>{value.projectVersion}</code></p>
    <p>{t('Execution environment')} · <AgentExecutionDisclosure profile={value.agentExecutionProfile} /> · {t(sourceKey[value.environmentSource])}</p>
    <p>{t('Model')} · {model} · {t(sourceKey[value.modelSource])}</p>
  </details>
}
