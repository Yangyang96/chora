import { useEffect, useMemo, useState } from 'react'
import { useI18n } from '../i18n'
import type { RunView, TrajectoryRecord } from '../types'

function formatTime(value: string) {
  return new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(new Date(value))
}

function formatDuration(milliseconds: number | undefined, notRecorded: string) {
  if (milliseconds === undefined) return notRecorded
  if (milliseconds < 1000) return `${milliseconds} ms`
  return `${(milliseconds / 1000).toFixed(milliseconds < 10_000 ? 1 : 0)} s`
}

function fallbackTrajectory(run: RunView): TrajectoryRecord[] {
  return run.timeline.map((event) => ({
    sequence: event.sequence,
    source: 'Chora',
    kind: 'system',
    title: event.title,
    summary: event.detail,
    status: 'completed',
    startedAt: event.time,
    details: [{ label: 'Event', value: event.type }],
  }))
}

function stage(record: TrajectoryRecord, branchDelivery: boolean) {
  if (record.kind === 'check') return 'Independent verification'
  if (record.kind === 'decision' || record.kind === 'apply') return branchDelivery ? 'Review & delivery' : 'Review & apply'
  if (record.kind === 'result') return 'Agent result'
  return 'Implementation'
}

export function TrajectoryLedger({ run }: { run: RunView }) {
  const { t } = useI18n()
  const records = useMemo(() => run.trajectory?.length ? run.trajectory : fallbackTrajectory(run), [run])
  const [selectedSequence, setSelectedSequence] = useState<number | null>(() => records.at(-1)?.sequence ?? null)

  useEffect(() => {
    setSelectedSequence(records.at(-1)?.sequence ?? null)
  }, [run.id, run.attempt])

  useEffect(() => {
    if (!records.some((record) => record.sequence === selectedSequence)) {
      setSelectedSequence(records.at(-1)?.sequence ?? null)
    }
  }, [records, selectedSequence])

  const selected = records.find((record) => record.sequence === selectedSequence) ?? records.at(-1)
  const agentReply = run.agentReport?.finalText.trim() ? run.agentReport.finalText : run.agentReport?.summary
  const longReply = (agentReply?.length ?? 0) > 600
  const claims = run.agentReport?.claimedChecks ?? []
  const failedChecks = claims.filter((check) => ['FAIL', 'FAILED'].includes(check.status.toUpperCase())).length
  const unknownChecks = claims.filter((check) => !['PASS', 'PASSED', 'FAIL', 'FAILED'].includes(check.status.toUpperCase())).length
  const repositories = run.resourceResult?.group.repositories
  const changedFileCount = repositories?.reduce((count, repository) => count + repository.changedPaths.length, 0) ?? run.reviewablePatch?.files.length
  const repositoryFailures = repositories?.filter((repository) => ['FAIL', 'FAILED'].includes(repository.checks.Status.toUpperCase())).length ?? 0
  const repositoryPasses = repositories?.filter((repository) => repository.checks.Status.toUpperCase() === 'PASS' && repository.checks.FinalContentVerified).length ?? 0
  const branchDelivery = run.resourceResult?.repositories?.some((repository) => repository.deliveryMode === 'task_branch')
  const applySummary = branchDelivery ? (run.status === 'accepted' ? 'Task branch delivery is tracked per repository below.' : 'Changes are ready for review in the Task worktree.') : run.resourceApply ? ({ remaining_closed: 'Remaining results closed', partial_applied: 'Some repositories were applied; remaining changes closed', applied: 'All repository changes have been applied.', partial: 'Some repositories were applied; recovery is still needed.', conflict: 'Apply blocked by a conflict.', pending: 'Apply is in progress.', recovery_required: 'Apply outcome needs recovery and confirmation.' })[run.resourceApply.status] : run.patchApplication ? ({ applied: 'Patch applied.', conflict: 'Apply blocked by a conflict.', applying: 'Apply is in progress.', recovery_required: 'Apply outcome needs recovery and confirmation.' })[run.patchApplication.state] : 'No Apply result recorded.'
  let previousStage = ''

  return (
    <section className="trajectory" aria-labelledby="trajectory-title">
      <div className="trajectory-head">
        <div>
          <span className="eyebrow">{t('Agent trajectory')}</span>
          <h2 id="trajectory-title">{t('What happened')}</h2>
        </div>
        <span className="trajectory-count">{t('{count} observable steps', { count: records.length })}</span>
      </div>

      <section className="agent-conversation" aria-labelledby="agent-conversation-title">
        <div className="agent-conversation-head">
          <div>
            <h3 id="agent-conversation-title">{t('Visible conversation')}</h3>
            <p>{t('User instruction and the Agent’s persisted final response.')}</p>
          </div>
        </div>
        <div className="conversation-message conversation-user">
          <span>{t('You')}</span>
          <p>{run.task.goal}</p>
        </div>
        <div className="conversation-message conversation-agent">
          <span>{t(longReply ? 'Chora result summary' : 'Agent')}</span>
          {longReply ? <>
            <ul className="reply-result-summary">
              <li>{changedFileCount !== undefined ? t('Reviewable changes: {count} files.', { count: changedFileCount }) : t('No reviewable patch recorded.')}</li>
              <li>{repositories ? t('Repository checks: {failed} failed, {unknown} unverified, {passed} passed for final contents.', { failed: repositoryFailures, unknown: repositories.length - repositoryFailures - repositoryPasses, passed: repositoryPasses }) : claims.length ? t('Reported checks: {failed} failed, {unknown} unknown, {passed} passed.', { failed: failedChecks, unknown: unknownChecks, passed: claims.length - failedChecks - unknownChecks }) : t('No check results recorded.')}</li>
              <li>{t(applySummary)}</li>
            </ul>
            <details key={`${run.id}:${run.attempt}`} className="agent-reply-details">
              <summary>{t('View full Agent reply')}</summary>
              <p>{agentReply}</p>
            </details>
          </> : <p>{agentReply || t('The Agent’s persisted final response will appear here when available.')}</p>}
        </div>
        <p className="conversation-boundary">{t('This is the visible exchange, not a chain-of-thought transcript. Tool activity remains in the trajectory below.')}</p>
      </section>

      <div className="trajectory-workbench">
        <div className="trajectory-ledger" role="list" aria-label={t('Agent trajectory records')}>
          {records.map((record, index) => {
            const recordStage = stage(record, Boolean(branchDelivery))
            const showStage = recordStage !== previousStage
            previousStage = recordStage
            return (
              <div key={record.sequence} className="trajectory-row-wrap">
                {showStage && <div className="trajectory-stage">{t(recordStage)}</div>}
                <button
                  type="button"
                  role="listitem"
                  aria-label={`${t(record.title)}: ${t(record.summary)}`}
                  className={`trajectory-row ${selected?.sequence === record.sequence ? 'selected' : ''}`}
                  aria-pressed={selected?.sequence === record.sequence}
                  onClick={() => setSelectedSequence(record.sequence)}
                >
                  <span className="trajectory-index">#{index + 1}</span>
                  <span className={`trajectory-kind kind-${record.kind}`}>{t(record.kind)}</span>
                  <span className="trajectory-copy">
                    <strong>{t(record.title)}</strong>
                    <span>{t(record.summary)}</span>
                  </span>
                  <span className="trajectory-trailing">
                    <span className={`trajectory-status status-${record.status}`}>{t(record.status)}</span>
                    <time>{formatTime(record.startedAt)}</time>
                  </span>
                </button>
              </div>
            )
          })}
          {records.length === 0 && <p className="stream-empty">{t('No observable steps yet.')}</p>}
        </div>

        {selected && (
          <aside className="trajectory-inspector" aria-label={t('Selected trajectory step details')}>
            <span className={`trajectory-kind kind-${selected.kind}`}>{t(selected.kind)}</span>
            <h3>{t(selected.title)}</h3>
            <p>{t(selected.summary)}</p>
            <dl>
              <div><dt>{t('Source')}</dt><dd>{t(selected.source)}</dd></div>
              <div><dt>{t('Status')}</dt><dd>{t(selected.status)}</dd></div>
              <div><dt>{t('Started')}</dt><dd>{formatTime(selected.startedAt)}</dd></div>
              <div><dt>{t('Duration')}</dt><dd>{formatDuration(selected.durationMs, t('Not recorded'))}</dd></div>
              {selected.details.map((detail) => (
                <div key={`${detail.label}:${detail.value}`}><dt>{t(detail.label)}</dt><dd><code>{detail.value}</code></dd></div>
              ))}
            </dl>
          </aside>
        )}
      </div>
      <p className="trajectory-boundary">{t('Shows Chora-normalized activity and governed evidence. Prompts and hidden reasoning are never displayed.')}</p>
    </section>
  )
}
