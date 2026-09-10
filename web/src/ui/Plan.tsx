import { useI18n } from '../i18n'
import type { RunView } from '../types'

function List({ items }: { items: string[] }) {
  const { t } = useI18n()
  if (items.length === 0) return <p className="section-empty">{t('None recorded.')}</p>
  return (
    <ul className="plan-list">
      {items.map((item) => <li key={item}>{item}</li>)}
    </ul>
  )
}

export function PlanSection({ run }: { run: RunView }) {
  const { t } = useI18n()
  const plan = run.plan
  return (
    <section className="workbench-section" aria-labelledby="plan-title">
      <div className="workbench-section-head">
        <div>
          <span className="eyebrow">{t('Workbench')}</span>
          <h2 id="plan-title">{t('Plan')}</h2>
          {plan && <p className="section-subtitle">{t('Revision')} {plan.revisionId}</p>}
        </div>
        {plan && <span className="trajectory-count">{t('{count} steps', { count: plan.content.technical_steps.length })}</span>}
      </div>
      <div className="workbench-section-body">
        {run.task.worktree?.baseRevision && <p className="section-note">{t('Task base')}: <code>{run.task.worktree.baseRevision}</code>{' · '}{run.task.worktree.baseRef || t('Historical branch unknown')}</p>}
        {plan ? (
          <>
            <h3>{t('Technical Steps')}</h3>
            {plan.content.technical_steps.length > 0 ? (
              <ol className="plan-list">
                {plan.content.technical_steps.map((step) => <li key={step}>{step}</li>)}
              </ol>
            ) : <p className="section-empty">{t('None recorded.')}</p>}
            <h3>{t('Decisions')}</h3>
            <List items={plan.content.decisions} />
            <h3>{t('Risks')}</h3>
            <List items={plan.content.risks} />
            <h3>{t('Unknowns')}</h3>
            <List items={plan.content.unknowns} />
            <p className="section-note">{t('Automatically activated inside the existing Room boundary.')}</p>
          </>
        ) : <p className="section-empty">{t('No plan recorded yet.')}</p>}
      </div>
    </section>
  )
}
