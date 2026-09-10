import { ActionButton } from './ActionButton'
import { useI18n } from '../i18n'
import { DiffReview } from './DiffReview'
import type { DerivedResourceCheckView, ResourceApplyView, ResourceCheckDerivationView, ResourceResultView } from '../taskFirstTypes'

function abbreviated(value: string) {
  return value.length > 12 ? value.slice(0, 12) : value
}

function checkLabel(check: DerivedResourceCheckView) {
  if (check.Status.toUpperCase() === 'FAIL') return 'Failed'
  if (check.Status.toUpperCase() === 'PASS' && check.FinalContentVerified) return 'Passed for final contents'
  return 'Unverified for final contents'
}

function aggregateLabel(checks: ResourceCheckDerivationView) {
  if (checks.Mode === 'none') return 'No checks selected · Unverified'
  if (checks.Checks.length === 0) return checks.NoApplicableChecks ? 'No applicable checks discovered · Unverified' : 'No checks observed · Unverified'
  if (checks.Status.toUpperCase() === 'FAIL') return 'Checks failed'
  if (checks.Status.toUpperCase() === 'PASS' && checks.FinalContentVerified) return 'Checks passed for final repository contents'
  return 'Checks unverified for final repository contents'
}

function ResourceChecks({ checks }: { checks: ResourceCheckDerivationView }) {
  const { t } = useI18n()
  return <section className="resource-checks" aria-label={t('Repository checks')}>
    <h4>{t('Checks')}</h4>
    <p className={checks.Status.toUpperCase() === 'FAIL' ? 'warning-banner' : 'section-note'}><strong>{t(aggregateLabel(checks))}</strong></p>
    {checks.Explanation && <p>{t(checks.Explanation)}</p>}
    {checks.Checks.map((check) => <details className="check-evidence" key={check.CheckID}>
      <summary>{check.Name} · {t(checkLabel(check))}</summary>
      <p><code>{check.WorkingDirectory || '.'}: {check.Argv.join(' ')}</code></p>
      {check.Source && <p>{t('Check selection source')}: {check.Source}</p>}
      <p>{check.ExitCode === null || check.ExitCode === undefined ? t('Numeric exit code unavailable.') : t('Exit code: {code}', { code: check.ExitCode })}</p>
      <p>{check.ProviderSucceeded === true ? t('Pi reported command success.') : check.ProviderSucceeded === false ? t('Pi reported a command error.') : t('Provider outcome unavailable.')}</p>
      <p>{check.FinalContentVerified ? t('Final repository contents match the check observation.') : check.Stale ? t('This check is stale because repository contents changed afterward.') : t('Final repository contents were not proven for this check.')}</p>
      {check.Evidence && <p>{check.Evidence}</p>}
    </details>)}
  </section>
}

export function ResourceResult({ result }: { result: ResourceResultView }) {
  const { t } = useI18n()
  const patches = new Map(result.patches.map((patch) => [patch.repoId, patch]))
  return <section className="workbench-section resource-result" aria-labelledby="repository-results-title">
    <div className="workbench-section-head">
      <div>
        <span className="eyebrow">{t('Reviewable result')}</span>
        <h2 id="repository-results-title">{t('Repository results')}</h2>
      </div>
    </div>
    <div className="workbench-section-body">
      <details className="check-evidence">
        <summary>{t('Result evidence')}</summary>
        <p>{t('Agent report')} · <code>{result.group.agentReportId}</code></p>
        <p>{t('Result digest')} · <code>{result.digest}</code></p>
        <p>{t('Resource snapshot digest')} · <code>{result.group.resourceSnapshotDigest}</code></p>
      </details>
      {result.group.repositories.map((repository) => {
        const patch = patches.get(repository.repoId)
        const identity = result.repositories?.find((item) => item.repoId === repository.repoId)
        const baseRef = identity?.baseRef
        const branch = baseRef?.startsWith('refs/heads/') ? baseRef.slice(11) : undefined
        return <article className="panel resource-result-repository" aria-label={repository.repoId} key={repository.repoId}>
          <h3>{identity?.name || repository.repoId}</h3>
          <p className="repository-base-summary">{t(identity?.deliveryMode === 'task_branch' ? 'Target branch' : 'Base branch')} <code>{branch || (baseRef === 'HEAD' ? t('Detached HEAD') : baseRef || t('Unavailable'))}</code> · {t('Base commit')} <code title={repository.baseCommit}>{abbreviated(repository.baseCommit)}</code></p>
          {identity?.deliveryMode === 'task_branch' && identity.taskBranch && <p>{t('Task branch')} · <code>{identity.taskBranch}</code></p>}
          {identity?.deliveryMode === 'task_branch' && identity.worktreePath && <p>{t('Task worktree')} · <code>{identity.worktreePath}</code></p>}
          <details className="check-evidence">
            <summary>{t('Git details')}</summary>
            <p>{t('Base captured when this Task started; branch names may move afterward.')}</p>
            <p>{t('Repository ID')} · <code>{repository.repoId}</code></p>
            <p>{t('Base ref')} · <code>{baseRef || t('Unavailable')}</code></p>
            <p>{t('Base tree')} · <code>{repository.baseTree}</code></p>
          </details>
          {repository.changedPaths.length === 0 ? <p className="section-empty">{t('No changes in this repository.')}</p> : <>
            {patch?.files.length ? <DiffReview patch={{ files: patch.files }} provenanceLabel="Task changes" /> : <>
              <ul className="changed-files">{repository.changedPaths.map((path) => <li key={path}><code>{path}</code></li>)}</ul>
              <p className="warning-banner">{t('Patch details are unavailable for these recorded paths.')}</p>
            </>}
            <details className="check-evidence">
              <summary>{t('Patch evidence')}</summary>
              <p>{t('Patch digest')} · <code>{repository.patchDigest}</code></p>
            </details>
          </>}
          {repository.checks.RepositoryID === repository.repoId
            ? <ResourceChecks checks={repository.checks} />
            : <p className="warning-banner">{t('Check evidence is not bound to this repository.')}</p>}
        </article>
      })}
    </div>
  </section>
}

function applyTitle(status: ResourceApplyView['status'] | undefined) {
  switch (status) {
    case 'applied': return 'Applied to all selected repositories'
    case 'partial': return 'Some repositories were applied'
    case 'partial_applied': return 'Some repositories were applied; remaining changes closed'
    case 'remaining_closed': return 'Remaining results closed'
    case 'conflict': return 'Apply conflict'
    case 'recovery_required': return 'Apply recovery required'
    default: return 'Reviewed changes are waiting to be applied'
  }
}

function repositoryApplyLabel(status: string) {
  switch (status) {
    case 'applied': return 'Written to repository'
    case 'closed': return 'Closed'
    case 'no_change': return 'No changes to write'
    case 'conflict': return 'Write conflict'
    case 'recovery_required': return 'Recovery required'
    case 'pending': return 'Waiting to write'
    default: return 'Write incomplete'
  }
}

export function ResourceApplyStatus({ apply, repositories, canApply, busy, onApply }: { apply?: ResourceApplyView; repositories?: ResourceResultView['repositories']; canApply: boolean; busy: boolean; onApply: () => void }) {
  const { t } = useI18n()
  const terminal = ['applied', 'partial_applied', 'remaining_closed'].includes(apply?.status ?? '')
  const continuing = apply && apply.status !== 'pending'
  return <div className={`application-status application-${apply?.status ?? 'pending'}`}>
    <strong>{t(applyTitle(apply?.status))}</strong>
    {apply?.repositories.length ? <ul>{apply.repositories.map((repository) => <li key={repository.repoId}>
      <strong>{repositories?.find((item) => item.repoId === repository.repoId)?.name || t('Repository')}</strong> · {t(repositoryApplyLabel(repository.status))}{repository.reason ? ` · ${t(repository.reason)}` : ''}
    </li>)}</ul> : <p>{t('No repository write has been recorded yet.')}</p>}
    {canApply && !terminal && <ActionButton type="button" className="btn-accept" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={onApply}>
      {busy ? t('Applying…') : t(continuing ? 'Continue remaining repositories' : 'Apply reviewed changes')}
    </ActionButton>}
  </div>
}
