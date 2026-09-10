import { useI18n } from '../i18n'
import { deliveryRank, deliveryStatusLabel } from '../deliveryProgress'
import type { DeliverySummary } from '../taskDeliveryTypes'
import type { RunView } from '../types'

type StepState = 'done' | 'current' | 'pending' | 'attention' | 'skipped' | 'partial'
type Step = { id: string; label: string; state: StepState; detail: string; current?: boolean; values?: Record<string, number> }
const stages = [
  ['prepare', 'Preparation'], ['develop', 'Development'], ['checks', 'Checks'], ['review', 'Code review'],
  ['commit', 'Commit'], ['push', 'Push branch'], ['pr', 'Pull request'], ['merge', 'Merge PR'], ['cleanup', 'Cleanup'],
] as const

export function workflowSteps(run?: RunView, delivery?: DeliverySummary, preparing = false): Step[] {
  const status = run?.status
  const checkRecovery = status === 'recovery_required' && ['checks_failed', 'checks_incomplete'].includes(run?.terminalReason ?? '')
  const branchDelivery = run?.resourceResult?.repositories?.some((repo) => repo.deliveryMode === 'task_branch')
  const legacy = Boolean(run?.resourceResult?.repositories?.length && !branchDelivery)
    || Boolean(run && !run.resourceResult && (run.patchApplication || run.verification || run.reviewablePatch))
    || (status === 'accepted' && !branchDelivery)
  const definitions: ReadonlyArray<readonly [string, string]> = legacy ? [...stages.slice(0, 4), ['apply', 'Apply changes']] : stages
  let current = !run || status === 'draft' || status === 'ready' || (status === 'running' && run.activity?.phase === 'preparing_context') ? 0
    : status === 'awaiting_verification' || status === 'verifying' || status === 'verification_recovery_required' || checkRecovery ? 2
      : status === 'awaiting_review' ? 3 : status === 'accepted' ? 4 : status === 'completed' ? definitions.length : 1
  const steps: Step[] = definitions.map(([id, label], index) => ({ id, label, state: index < current ? 'done' : index === current ? 'current' : 'pending', detail: index < current ? 'Completed' : index === current ? 'In progress' : 'Not started' }))
  if (!run) { steps[0].detail = preparing ? 'Preparing workspace' : 'Ready to start'; return steps }
  if (status === 'ready' || status === 'draft') steps[0].detail = 'Ready to start'
  if (status === 'awaiting_review') steps[3].detail = 'Waiting for review'
  if (status === 'revision_required') { steps[1].state = 'attention'; steps[1].detail = 'Changes requested' }
  if (['recovery_required', 'verification_recovery_required', 'stopping', 'cancelled'].includes(status!)) {
    steps[current].state = 'attention'
    steps[current].detail = status === 'cancelled' ? 'Cancelled' : status === 'stopping' ? 'Stopping' : 'Recovery needed'
  }
  if (checkRecovery) steps[2].detail = run.terminalReason === 'checks_failed' ? 'Checks failed' : 'Checks incomplete'
  if (run.automaticRetry && ['pending', 'retrying', 'blocked', 'exhausted'].includes(run.automaticRetry.state) && ['ready', 'running', 'recovery_required'].includes(status!)) {
    steps[current].detail = ['blocked', 'exhausted'].includes(run.automaticRetry.state) ? 'Recovery needed' : 'Retrying development'
  }
  if (run.decisionGate?.status === 'open' && ['ready', 'running', 'verifying', 'awaiting_verification'].includes(status!)) { steps[current].state = 'attention'; steps[current].detail = 'Needs your input' }

  // Reaching Review is not proof that tests passed. Retain their actual evidence.
  if (current >= 3 || status === 'completed') {
    const checks = run.resourceResult?.group.repositories.map((repo) => repo.checks) ?? []
    const observed = checks.flatMap((check) => check.Checks)
    const failed = checks.some((check) => check.Status.toUpperCase() === 'FAIL')
    const passed = checks.length > 0 && checks.every((check) => check.Status.toUpperCase() === 'PASS' && check.FinalContentVerified && check.Checks.length > 0)
    const verified = run.verification?.result?.outcome === 'review_ready'
    const claimed = run.agentReport?.claimedChecks ?? []
    const claimedFailure = claimed.some((check) => ['FAIL', 'FAILED'].includes(check.status.toUpperCase()))
    steps[2].state = passed || (!checks.length && verified) ? 'done' : failed || claimedFailure ? 'attention' : 'skipped'
    steps[2].detail = passed || (!checks.length && verified) ? 'Checks passed' : failed || claimedFailure ? 'Checks failed' : observed.length || claimed.length ? 'Check results unverified' : 'Not run / Unverified'
  }
  if (status === 'completed') {
    for (const step of steps.slice(3)) { step.state = 'skipped'; step.detail = 'Not needed · No changes' }
    return steps
  }
  if (run.resultClosed && (status !== 'accepted' || legacy)) {
    for (const step of steps.slice(status === 'accepted' ? 4 : 3)) { step.state = 'skipped'; step.detail = 'Remaining results closed' }
    return steps
  }
  if (status !== 'accepted') return steps
  if (legacy) {
    const applied = run.resourceApply?.status ?? run.patchApplication?.state
    steps[4].state = applied === 'applied' ? 'done' : ['conflict', 'failed', 'recovery_required', 'partial'].includes(applied ?? '') ? 'attention' : 'current'
    steps[4].detail = applied === 'applied' ? 'Applied' : applied === 'applying' ? 'Applying' : steps[4].state === 'attention' ? 'Recovery needed' : 'Waiting to apply'
    return steps
  }
  if (!delivery || delivery.unavailable || !delivery.repositories.length) {
    steps[4].state = delivery?.unavailable ? 'attention' : 'current'
    steps[4].detail = delivery?.unavailable ? 'Status unavailable' : 'Loading status…'
    return steps
  }
  const repos = delivery.repositories.filter((repo) => repo.status !== 'no_change')
  const unavailable = repos.some((repo) => repo.status === 'recovery_required' || repo.status === 'legacy' || repo.status === 'awaiting_review')
  const openRepos = repos.filter(repo => repo.status !== 'closed')
  current = 4 + Math.min(...openRepos.map((repo) => deliveryRank(repo.status)))
  for (let index = 4; index < steps.length; index += 1) {
    const done = repos.filter((repo) => deliveryRank(repo.status) >= index - 3).length
    const step = steps[index]
    if (!repos.length) { step.state = 'skipped'; step.detail = 'Not needed · No changes' }
    else if (!openRepos.length) { step.state = 'skipped'; step.detail = 'Remaining results closed' }
    else if (unavailable) { step.state = 'attention'; step.detail = 'Status needs attention' }
    else {
      step.state = done === repos.length ? 'done' : done > 0 ? 'partial' : index === current ? 'current' : 'pending'
      step.detail = step.state === 'done' ? 'Completed' : step.state === 'partial' ? '{count}/{total} repositories' : step.state === 'current' ? 'Awaiting action' : 'Not started'
      step.current = index === current
      if (step.state === 'partial' && step.current) step.detail = '{count}/{total} repositories · Awaiting action'
      if (step.state === 'partial') step.values = { count: done, total: repos.length }
      if (index === 7 && repos.some((repo) => repo.status === 'pr_closed')) { step.state = 'attention'; step.detail = 'PR closed · Not merged' }
    }
  }
  return steps
}

export function TaskProgress({ run, delivery, preparing }: { run?: RunView; delivery?: DeliverySummary; preparing?: boolean }) {
  const { t } = useI18n()
  const steps = workflowSteps(run, delivery, preparing)
  const current = steps.find((step) => step.current || step.state === 'current')
  return <section className="task-progress" aria-label={t('Task progress')}>
    <div className="task-progress-heading"><h2>{t('Task progress')}</h2>{current && <span>{t('Current stage: {stage}', { stage: t(current.label) })}</span>}</div>
    <ol className="task-progress-steps">
      {steps.map((step, index) => <li key={step.id} className={`workflow-step stage-${step.id} is-${step.state}${step.current ? ' is-active' : ''}`} aria-current={step.current || step.state === 'current' ? 'step' : undefined}>
        <span className="workflow-step-marker" aria-hidden="true">{step.state === 'done' ? '✓' : step.state === 'attention' ? '!' : step.state === 'skipped' ? '–' : index + 1}</span>
        <span className="workflow-step-copy"><span className="workflow-step-label">{t(step.label)}</span><span className="workflow-step-detail">{t(step.detail, step.values)}</span></span>
      </li>)}
    </ol>
    {delivery && delivery.repositories.length > 1 && <details className="workflow-repositories">
      <summary>{t('Repository progress · {count}', { count: delivery.repositories.length })}</summary>
      <ul>{delivery.repositories.map((repo) => <li key={repo.repoId}><span>{repo.name}</span><span className={`delivery-status delivery-stage-${repo.status}`}>{t(deliveryStatusLabel(repo.status))}</span></li>)}</ul>
    </details>}
  </section>
}
