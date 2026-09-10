import { useI18n } from '../i18n'
import type { ModelProvenance as ModelProvenanceValue, RunView } from '../types'

type Props = {
  current?: ModelProvenanceValue
  history?: NonNullable<RunView['attemptHistory']>
}

export function ModelProvenance({ current, history = [] }: Props) {
  const { locale } = useI18n()
  const copy = locale === 'zh-CN'
    ? { title: '模型来源', current: '当前尝试', attempt: '尝试', unknown: '未知', provider: '提供方未知', reason: '此尝试未观察到模型身份。' }
    : { title: 'Model provenance', current: 'Current Attempt', attempt: 'Attempt', unknown: 'Unknown', provider: 'Unknown provider', reason: 'No model identity was observed for this Attempt.' }
  const entries = history.length > 0 ? history : current ? [{ id: 'current', sequence: 0, modelProvenance: current }] : []
  if (entries.length === 0) return null
  return (
    <section className="workbench-section" aria-labelledby="model-provenance-title">
      <div className="workbench-section-head"><div><span className="eyebrow">{copy.current}</span><h2 id="model-provenance-title">{copy.title}</h2></div></div>
      <div className="workbench-section-body">
        <ul className="model-provenance-list">
          {entries.map((entry) => {
            const provenance = entry.modelProvenance ?? { status: 'unknown' as const, identities: [], reason: copy.reason }
            return <li key={entry.id}>
              <strong>{entry.sequence > 0 ? `${copy.attempt} ${entry.sequence}` : copy.current}</strong>
              {provenance.status === 'observed' && provenance.identities.length > 0
                ? <ul>{provenance.identities.map((identity) => <li key={`${identity.provider}:${identity.modelId}`}><span>{identity.provider === 'unknown' ? copy.provider : identity.provider}</span> · <code>{identity.modelId}</code></li>)}</ul>
                : <p>{copy.unknown} · {locale === 'zh-CN' ? copy.reason : (provenance.reason ?? copy.reason)}</p>}
            </li>
          })}
        </ul>
      </div>
    </section>
  )
}
