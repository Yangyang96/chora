import { ActionButton } from './ActionButton'
import { useCallback, useEffect, useState } from 'react'
import { useI18n } from '../i18n'
import { agentFailureStatus, automaticRetryActive } from '../taskStatus'
import { hasIndependentVerificationEvidence } from '../runProvenance'
import type { AgentExecutionProfile, RunStatus, RunView } from '../types'
import { AgentExecutionDisclosure, AgentExecutionProfileSelector } from './AgentExecutionProfile'
import { AuditDrawer } from './AuditDrawer'
import { ChangedFiles } from './ChangedFiles'
import { ChecksSection } from './Checks'
import { DiffReview } from './DiffReview'
import type { PiDiscoveryFetch } from './PiDiscovery'
import { PlanSection } from './Plan'
import { ResourceApplyStatus, ResourceResult } from './ResourceResult'
import { TrajectoryLedger } from './TrajectoryLedger'
import { TaskDelivery } from './TaskDelivery'
import { ResultClosure } from './ResultClosure'
import { TaskProgress } from './TaskProgress'
import { ModelProvenance } from './ModelProvenance'
import { deliveryDisplayStatus } from '../deliveryProgress'
import type { DeliverySummary, TaskDeliveryView } from '../taskDeliveryTypes'

type RejectionClass = 'implementation_gap' | 'planning_gap' | 'contract_change_required'

type RunStreamProps = {
  run: RunView
  busy: boolean
  trustedLocalSelectionReady?: boolean
  trustedLocalAcknowledgementPolicy?: string
  piDiscovery?: PiDiscoveryFetch
  onResultClosed?: () => void
  onCancel: () => void
  onReview: (kind: 'accept' | 'reject', comment: string, rejectionClass?: RejectionClass) => void
  onApply: () => void
  onRetry: (instructions: string) => void
  onSwitchProfile: (profile: AgentExecutionProfile, reason: string) => Promise<boolean>
  onAcknowledgeTrustedLocal: () => Promise<boolean>
  onResolveDecision: (gateID: string, optionID: string) => void
  onRetryVerification: () => void
  onChangeRequirement: () => void
}

function adapterName(adapter: string) {
  if (adapter === 'pi') return 'Pi'
  if (adapter === 'fake') return 'Agent'
  return adapter
}

function statusTitle(status: RunStatus, application?: RunView['patchApplication'], applyRequired = false, verificationState?: NonNullable<RunView['verificationDisposition']>['state'], resourceApply?: RunView['resourceApply']): string {
	if (status === 'accepted') {
		if (!applyRequired) return 'Accepted'
		if (resourceApply?.status === 'applied') return 'Written to repositories'
		if (resourceApply?.status === 'partial') return 'Apply partially completed'
		if (resourceApply?.status === 'conflict') return 'Apply conflict'
		if (resourceApply?.status === 'recovery_required') return 'Apply recovery required'
		if (!application) return 'Accepted · Not applied'
		if (application.state === 'applied') return 'Written to repository'
		if (application.state === 'applying') return 'Applying accepted Patch'
		if (application.state === 'conflict') return 'Apply conflict'
		return 'Apply recovery required'
	}
  switch (status) {
    case 'completed': return 'Completed · No changes'
    case 'running':
      return 'Working'
    case 'stopping':
      return 'Stopping'
    case 'awaiting_verification':
      if (verificationState === 'not_applicable') return 'Agent finished'
      if (verificationState === 'unavailable') return 'Verification unavailable'
      return 'Verification pending'
    case 'verifying':
      return 'Verifying'
    case 'awaiting_review':
      return 'Awaiting review'
    case 'revision_required':
      return 'Revision required'
    case 'recovery_required':
    case 'verification_recovery_required':
      return 'Recovery required'
    case 'cancelled':
      return 'Cancelled'
    default:
      return status.replaceAll('_', ' ')
  }
}

function formatElapsed(value: number) {
  const seconds = Math.max(0, Math.floor(value / 1000))
  const minutes = Math.floor(seconds / 60)
  return minutes > 0 ? `${minutes}m ${seconds % 60}s` : `${seconds}s`
}

export function RunStream({ run, busy, onResultClosed, trustedLocalSelectionReady, trustedLocalAcknowledgementPolicy, piDiscovery, onCancel, onReview, onApply, onRetry, onSwitchProfile, onAcknowledgeTrustedLocal, onResolveDecision, onRetryVerification, onChangeRequirement }: RunStreamProps) {
  const { t } = useI18n()
  const [closedKey, setClosedKey] = useState('')
  const hasClosedResult = Boolean(run.resultClosed || closedKey === run.id)
  const [comment, setComment] = useState('')
  const [retryInstructions, setRetryInstructions] = useState('Resolve the rejected acceptance gap and return an updated result.')
  const [switchProfile, setSwitchProfile] = useState<AgentExecutionProfile>(run.agentExecution?.profile ?? 'standard')
  const [switchReason, setSwitchReason] = useState('Use this profile for a fresh successor Attempt.')
  const [switchDisclosurePending, setSwitchDisclosurePending] = useState(false)
  const deliveryKey = `${run.id}:${run.resourceResult?.digest ?? ''}`
  const [deliveryState, setDeliveryState] = useState<{ key: string; value?: DeliverySummary }>()
  const receiveDelivery = useCallback((view: TaskDeliveryView | undefined, unavailable: boolean) => {
    setDeliveryState({ key: deliveryKey, value: unavailable ? { repositories: [], unavailable: true } : view })
  }, [deliveryKey])
  const delivery = deliveryState?.key === deliveryKey ? deliveryState.value : undefined

  const agentName = adapterName(run.adapter)
  const status = run.status
  const retryActive = automaticRetryActive(run.automaticRetry)
  const resumePreparedRetry = status === 'ready' && run.automaticRetry?.state === 'blocked'
  const failureStatus = agentFailureStatus(status, run.automaticRetry, run.terminalReason)
  const applyRequired = Boolean(run.resourceResult || run.verification || run.verifiedReview || run.patchApplication)
  const latestEvent = run.timeline.at(-1)
  const verificationState = run.verificationDisposition?.state
  const verificationNotApplicable = verificationState === 'not_applicable'
  const implementationFinishedWithoutVerification = verificationNotApplicable && ['awaiting_verification', 'awaiting_review', 'accepted', 'completed'].includes(status)
  const verificationUnavailable = status === 'awaiting_verification' && verificationState === 'unavailable'
  const independentlyVerified = hasIndependentVerificationEvidence(run)
  const localPi = run.agentExecution?.runtimeSource === 'local_pi' && !independentlyVerified
  const branchDelivery = run.resourceResult?.repositories?.some((repository) => repository.deliveryMode === 'task_branch')
  const deliveryStatus = status === 'accepted' && branchDelivery ? deliveryDisplayStatus(delivery ?? { repositories: [], unavailable: true }) : undefined
  const checkStatuses = (run.agentReport?.claimedChecks ?? []).map((check) => check.status.toUpperCase())
  const checkSummary = checkStatuses.some((status) => ['FAIL', 'FAILED'].includes(status)) ? 'Checks did not pass.' : checkStatuses.length > 0 && checkStatuses.every((status) => ['PASS', 'PASSED'].includes(status)) ? 'Recorded checks passed.' : 'No complete passing check result is recorded.'
  const patchProvenanceLabel = independentlyVerified ? 'Verified patch' : 'Reviewable patch'

  useEffect(() => {
    setSwitchProfile(run.agentExecution?.profile ?? 'standard')
  }, [run.id, run.attempt, run.agentExecution?.profile])

  async function switchExecutionProfile() {
    if (!run.agentExecution || switchProfile === run.agentExecution.profile || !switchReason.trim()) return
    const switched = await onSwitchProfile(switchProfile, switchReason.trim())
    if (!switched) setSwitchProfile(run.agentExecution.profile)
  }

  return (
    <div className="stream">
      <div className="stream-head">
        <div className="stream-head-details">
          <strong>
            {hasClosedResult && status !== 'accepted' ? t('Remaining results closed') : deliveryStatus ? !delivery ? t('Loading delivery status…') : t(deliveryStatus.label, deliveryStatus.values) : failureStatus ? t(failureStatus.label, failureStatus.values) : t(statusTitle(status, run.patchApplication, applyRequired && !branchDelivery, verificationState, run.resourceApply))}
            {status === 'running' || status === 'stopping' ? ` · ${agentName}` : ''}
          </strong>
          <span>
            {run.task.title}
            {run.activity && !implementationFinishedWithoutVerification ? ` · ${formatElapsed(run.activity.elapsedMs)}` : ''}
          </span>
		  {run.agentExecution && <span className="run-profile-header">{t('Agent profile')} · <AgentExecutionDisclosure agentExecution={run.agentExecution} /></span>}
          {run.agentExecution?.attemptTimeoutSeconds && <p className="section-note">{t('Attempt timeout: {seconds}s', { seconds: run.agentExecution.attemptTimeoutSeconds })} · {run.agentExecution.reportedCost === undefined ? t('Cost: unknown') : t('Pi-reported cost estimate: {cost} (not a bill or budget limit)', { cost: run.agentExecution.reportedCost })}{run.agentExecution.costStatus === 'partial_pi_reported_estimate' && ` · ${t('Partial data')}`}</p>}
		  {run.task.worktree && <span>{t('Workspace · {locator} · {state}', { locator: run.task.worktree.locator, state: t(run.task.worktree.state) })}</span>}
        </div>
        {(status === 'running' || status === 'stopping' || retryActive) && run.controls?.canCancel && (
          <ActionButton type="button" className="btn-reject" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={onCancel}>
            {busy ? t('Cancelling…') : t('Cancel')}
          </ActionButton>
        )}
      </div>

      <TaskProgress run={run} delivery={status === 'accepted' && branchDelivery ? delivery : undefined} />

      <ModelProvenance current={run.attemptDetail?.modelProvenance} history={run.attemptHistory} />

      {implementationFinishedWithoutVerification ? (
        <p className="stream-current">{t('Implementation finished — {reason}', { reason: t(run.verificationDisposition?.reason ?? '') })}</p>
      ) : latestEvent && (
        <p className="stream-current">
          {latestEvent.title} — {latestEvent.detail}
        </p>
      )}

      {run.automaticRetry && failureStatus && (
        <div className="stream-action" role={retryActive ? 'status' : 'alert'}>
          <strong>{t(failureStatus.label, failureStatus.values)}</strong>
          <p>{t('Last failure: {reason}', { reason: t(failureStatus.detail ?? 'Agent execution failed') })}</p>
          <p>{t(retryActive
            ? 'Chora will continue automatically in the same task worktree. No manual retry is needed.'
            : run.automaticRetry.state === 'exhausted'
              ? 'Automatic retries are exhausted. Changes remain in the task worktree. Inspect the failure and retry when ready.'
              : 'Automatic continuation could not start safely. Inspect the failure before retrying.')}</p>
          {run.automaticRetry.state === 'blocked' && Boolean(run.blockers?.length) && <ul>{run.blockers!.map((reason, index) => <li key={index}>{t(reason)}</li>)}</ul>}
        </div>
      )}

      {!run.automaticRetry && status === 'recovery_required' && ['runtime_stream_invalid', 'runtime_output_limit_exceeded'].includes(run.terminalReason ?? '') && (
        <div className="stream-action" role="alert">
          <strong>{t('Agent execution stopped')}</strong>
          <p>{t(run.terminalReason === 'runtime_output_limit_exceeded'
            ? 'Agent output exceeded the log limit. Changes remain in the task worktree. Retry with a smaller verification scope.'
            : 'Agent output was incomplete or invalid. Execution has stopped and changes remain in the task worktree. You can retry this task.')}</p>
        </div>
      )}

      {!run.automaticRetry && status === 'recovery_required' && ['checks_failed', 'checks_incomplete'].includes(run.terminalReason ?? '') && <p role="alert" className="warning-banner">{t(run.terminalReason === 'checks_failed' ? 'No file changes; a configured check failed. This task remains open for recovery.' : 'No file changes; configured checks did not complete. This task remains unverified and open for recovery.')}</p>}
      {!run.automaticRetry && status === 'recovery_required' && ['attempt_timeout', 'runtime_exit_nonzero', 'result_contract_invalid', 'review_patch_materialization_failed'].includes(run.terminalReason ?? '') && <div className="stream-action" role="alert"><strong>{t('Task needs attention')}</strong><p>{t(({ attempt_timeout: 'The attempt reached its displayed timeout. Narrow the task or retry with a smaller scope.', runtime_exit_nonzero: 'Pi exited unsuccessfully. Inspect the recorded tool activity, repair Pi or its dependencies, then retry.', result_contract_invalid: 'Pi did not return a complete result. Check model/network access and retry; no successful result has been recorded.', review_patch_materialization_failed: 'The task output could not become a reviewable Patch. Inspect its scope and unsupported file types, then retry within the frozen scope or create a task with corrected settings.' } as Record<string, string>)[run.terminalReason!])}</p></div>}
	  {!run.automaticRetry && status === 'recovery_required' && run.terminalReason === 'agent_model_error'  && (
		<div className="stream-action" role="alert">
		  <strong>{t('Agent model request failed')}</strong>
		  <p>{t('Pi could not use the configured default model. Check model access or select an available default model in Pi, then Retry.')}</p>
		</div>
	  )}

      {run.agentExecution && (
        <div className="run-profile-activity">
          <span>{t('Activity profile')}</span>
          <AgentExecutionDisclosure agentExecution={run.agentExecution} />
        </div>
      )}

      <section className="workbench-section" aria-labelledby="intent-title">
        <div className="workbench-section-head">
          <div>
            <span className="eyebrow">{t('Workbench')}</span>
            <h2 id="intent-title">{t('Intent')}</h2>
          </div>
        </div>
        <div className="workbench-section-body">
          <p className="intent-goal">{run.task.goal}</p>
        </div>
      </section>

      <PlanSection run={run} />

      <section className="workbench-section" aria-labelledby="progress-title">
        <div className="workbench-section-head">
          <div>
            <span className="eyebrow">{t('Workbench')}</span>
            <h2 id="progress-title">{t('Progress')}</h2>
          </div>
        </div>
        <div className="workbench-section-body workbench-section-flush">
          <TrajectoryLedger run={run} />
        </div>
      </section>

      {run.resourceResult ? <ResourceResult result={run.resourceResult} /> : <>
        <ChangedFiles patch={run.reviewablePatch} provenanceLabel={run.reviewablePatch ? patchProvenanceLabel : undefined} />
        <section className="workbench-section" aria-labelledby="diff-title">
        <div className="workbench-section-head">
          <div>
            <span className="eyebrow">{t(run.reviewablePatch ? patchProvenanceLabel : 'Workbench')}</span>
            <h2 id="diff-title">{t('Diff')}</h2>
          </div>
        </div>
        <div className={`workbench-section-body${run.reviewablePatch?.files.length ? ' workbench-section-flush' : ''}`}>
          {run.reviewablePatch?.files.length
            ? <DiffReview patch={run.reviewablePatch} provenanceLabel={patchProvenanceLabel} />
            : <p className="section-empty">{t('No diff available yet.')}</p>}
        </div>
        </section>

        <ChecksSection run={run} />
      </>}

      <section className="workbench-section" aria-labelledby="review-apply-title">
        <div className="workbench-section-head">
          <div>
            <span className="eyebrow">{t('Workbench')}</span>
            <h2 id="review-apply-title">{t(branchDelivery ? 'Review' : localPi ? 'Changes and next steps' : 'Review & Apply')}</h2>
          </div>
        </div>
        <div className="workbench-section-body">
          {localPi && !run.resourceResult && (
            <div className="result-overview">
              <div>
                <h3>{t('What changed?')}</h3>
                {run.reviewablePatch?.files.length ? <><ul>{run.reviewablePatch.files.map((file) => <li key={file.path}><code>{file.path}</code></li>)}</ul><a href="#diff-title">{t('View changes')}</a></> : <p>{t(status === 'completed' ? 'No changed files.' : 'No diff available yet.')}</p>}
              </div>
              <div>
                <h3>{t('Did checks pass?')}</h3>
                <p>{t(checkSummary)}</p>
                <a href="#checks-title">{t('View check results')}</a>
                <details className="check-evidence">
                  <summary>{t('Check source')}</summary>
                  <p>{t('These results come from Pi. They do not by themselves confirm that the requested change is correct.')}</p>
                  {verificationNotApplicable && <p>{t('Chora did not run a separate check of these changes.')}</p>}
                </details>
              </div>
              <div>
                <h3>{t('Were changes written to the repository?')}</h3>
                {!run.patchApplication && <p>{t(status === 'completed' ? 'There are no changes to write.' : 'No write to the original repository is recorded yet.')}</p>}
                {status === 'awaiting_review' && <p>{t('Review the changes and check results, then decide whether to write them to your repository.')}</p>}
                {run.agentExecution && <p className="surface-profile">{t('Run environment')} · <AgentExecutionDisclosure agentExecution={run.agentExecution} /></p>}
              </div>
            </div>
          )}

          {run.decisionGate && run.decisionGate.status === 'open' && (
            <div className="decision-gate">
              <strong>{t('Decision needed')}</strong>
              <h3>{run.decisionGate.question}</h3>
              <p>{run.decisionGate.context}</p>
              <div className="decision-options">
                {run.decisionGate.options.map((option) => (
                  <ActionButton type="button" key={option.id} className="btn-primary" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={() => onResolveDecision(run.decisionGate!.id, option.id)}>
                    {option.label}
                  </ActionButton>
                ))}
              </div>
              <p className="decision-reco">
                {t('Recommendation: {recommendation} {impact}', { recommendation: run.decisionGate.recommendation, impact: run.decisionGate.impact })}
              </p>
            </div>
          )}

          {implementationFinishedWithoutVerification && !localPi && (
            <div className="stream-action">
              <strong>{t('Independent verification was not run')}</strong>
              <p>{t(run.verificationDisposition?.reason ?? '')}</p>
            </div>
          )}

          {verificationUnavailable && (
            <div className="stream-action">
              <strong>{t('Independent verification is unavailable')}</strong>
              <p>{t(run.verificationDisposition?.reason ?? '')}</p>
            </div>
          )}

          {status === 'awaiting_verification' && verificationState !== 'not_applicable' && verificationState !== 'unavailable' && !run.controls?.canRetryVerification && (
            <div className="stream-action">
              <p>{t('The Agent finished. Independent verification starts automatically.')}</p>
            </div>
          )}

          {(status === 'verification_recovery_required' || (status === 'awaiting_verification' && run.controls?.canRetryVerification)) && (
            <div className="stream-action">
              <p>{t('Verification needs attention. Retry the Verifier without rerunning the Agent.')}</p>
              <ActionButton type="button" className="btn-primary" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={onRetryVerification}>
                {busy ? t('Retrying…') : t('Retry Verifier')}
              </ActionButton>
            </div>
          )}

          {(status === 'revision_required' || status === 'cancelled' || status === 'recovery_required' || run.automaticRetry?.state === 'blocked') && !retryActive && !hasClosedResult && run.controls?.canRetry && (
            <div className="stream-action">
			  {run.agentExecution && <p className="surface-profile">{t('Retry profile')} · <AgentExecutionDisclosure agentExecution={run.agentExecution} /></p>}
              {resumePreparedRetry ? <>
                <p>{t('Continue the prepared attempt with its saved instructions and remaining automatic retry budget.')}</p>
                <ActionButton type="button" className="btn-primary" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={() => onRetry(run.retry?.instructions || 'Continue the prepared automatic retry.')}>
                  {busy ? t('Retrying…') : t('Continue prepared retry')}
                </ActionButton>
              </> : <>
                <label>
                  {t('Retry instructions')}
                  <textarea value={retryInstructions} onChange={(event) => setRetryInstructions(event.target.value)} />
                </label>
                <ActionButton type="button" className="btn-primary" disabled={busy || !retryInstructions.trim()} disabledReason={busy ? 'Wait for the current operation to finish.' : 'Enter instructions for the next attempt.'} onClick={() => onRetry(retryInstructions.trim())}>
                  {busy ? t('Retrying…') : t('Retry {agent}', { agent: agentName })}
                </ActionButton>
              </>}
            </div>
          )}

          {!hasClosedResult && run.controls?.canSwitchAgentExecutionProfile && run.agentExecution && (
            <div className="stream-action profile-switch">
              <strong>{t('Switch Agent profile for a successor Attempt')}</strong>
              <p>{t('Switching creates a fresh successor Attempt and preserves the current Attempt and evidence. Ordinary Retry remains unchanged.')}</p>
              <AgentExecutionProfileSelector
                value={switchProfile}
                disabled={busy}
                trustedLocalSelectable={trustedLocalSelectionReady}
                piDiscovery={piDiscovery}
                acknowledgedPolicyVersion={trustedLocalAcknowledgementPolicy}
                onChange={setSwitchProfile}
                onAcknowledgeTrustedLocal={onAcknowledgeTrustedLocal}
                onDisclosurePendingChange={setSwitchDisclosurePending}
              />
              <label>
                {t('Reason for profile switch')}
                <textarea value={switchReason} onChange={(event) => setSwitchReason(event.target.value)} />
              </label>
              <ActionButton
                type="button"
                className="btn-primary"
                disabled={busy || switchDisclosurePending || switchProfile === run.agentExecution.profile || !switchReason.trim()} disabledReason={busy ? 'Wait for the current operation to finish.' : switchDisclosurePending ? 'Finish the Local Connected acknowledgement first.' : switchProfile === run.agentExecution.profile ? 'Choose a different Agent profile.' : 'Enter a reason for switching the Agent profile.'}
                onClick={switchExecutionProfile}
              >
                {busy ? t('Switching…') : t('Create successor with selected profile')}
              </ActionButton>
            </div>
          )}

          {status === 'completed' && <p className="success-banner" role="status">{t('Completed · No changes')}. {t('The task finished without file changes. Check results remain separate; there is nothing to Apply.')}</p>}
          {run.resourceResult && (status === 'awaiting_review' || status === 'accepted' || status === 'revision_required') && (
            <div className="review-summary">
              <strong>{t('Review each repository result')}</strong>
              <p>{t('Review every repository patch and its final-content check evidence before deciding.')}</p>
              {branchDelivery && status === 'awaiting_review' && <p>{t('Accept the reviewed result to start per-repository task branch delivery.')}</p>}
            </div>
          )}
          {!run.resourceResult && !localPi && (status === 'awaiting_review' || status === 'accepted' || status === 'revision_required') && run.reviewablePatch && (
            <div className="review-summary">
              <strong>{t(independentlyVerified ? 'Review · planned versus actual' : run.agentExecution?.runtimeSource === 'local_pi' ? 'Human patch review' : 'Agent-reported review · planned versus actual')}</strong>
              {run.agentExecution && <p className="surface-profile">{t('Review profile')} · <AgentExecutionDisclosure agentExecution={run.agentExecution} /></p>}
              <p>{run.agentExecution?.runtimeSource === 'local_pi' ? t('Review the diff and check results before deciding whether to apply. Applying a patch does not make failed checks pass.') : run.agentReport?.summary || t(independentlyVerified ? 'The Agent returned a Patch for independent review.' : 'The Agent returned a reviewable Patch with Agent-reported results.')}</p>
              {run.unknowns.length > 0 && <p>{t('Unknowns: {unknowns}', { unknowns: run.unknowns.join(' · ') })}</p>}
            </div>
          )}

          {status === 'awaiting_review' && !hasClosedResult && (
            <ReviewForm comment={comment} busy={busy} plainLanguage={localPi} applyOnAccept={Boolean(run.controls?.canAcceptAndApply)} onComment={setComment} onReview={onReview} onChangeRequirement={onChangeRequirement} />
          )}

          {(run.controls?.canCloseResult || hasClosedResult) && <ResultClosure key={run.id} runId={run.id} version={run.version} closed={hasClosedResult} onClosed={() => { setClosedKey(run.id); onResultClosed?.() }} />}

          {status === 'accepted' && branchDelivery && run.resourceResult && (
            <TaskDelivery key={`${run.id}:${run.resourceResult.digest}:${hasClosedResult}`} runId={run.id} expectedVersion={run.version} resultDigest={run.resourceResult.digest} onStatusChange={receiveDelivery} />
          )}
          {status === 'accepted' && applyRequired && !branchDelivery && (
			run.resourceResult ? <ResourceApplyStatus repositories={run.resourceResult.repositories} apply={run.resourceApply} canApply={Boolean(!hasClosedResult && run.controls?.canApplyPatch)} busy={busy} onApply={onApply} /> : <div className={`application-status application-${run.patchApplication?.state ?? 'pending'}`}>
				<strong>{t(localPi ? ({ applied: 'Written to repository', applying: 'Writing changes to repository…', conflict: 'Cannot write: repository has changed', recovery_required: 'Write interrupted: check the repository before retrying', pending: 'Approved, waiting to write to repository' }[run.patchApplication?.state ?? 'pending']) : statusTitle(status, run.patchApplication, true))}</strong>
				{!run.patchApplication && <p>{t('Your decision is saved. The code has not been written to the target working tree yet.')}</p>}
				{run.patchApplication?.state === 'applying' && <p>{t('The exact accepted Patch is being checked and written. This state survives restart.')}</p>}
				{run.patchApplication?.state === 'applied' && (
					<>
						<p>{t('Chora wrote these changes to your repository without staging or committing them. Later Git commits are not tracked here.')}</p>
						<p><code>{run.patchApplication.affectedPaths.join(' · ')}</code></p>
					</>
				)}
				{(run.patchApplication?.state === 'conflict' || run.patchApplication?.state === 'recovery_required') && (
					<p>{run.patchApplication.reason || t('The accepted Patch is preserved. Resolve target drift and retry Apply.')}</p>
				)}
				{!hasClosedResult && run.controls?.canApplyPatch && (
					<ActionButton type="button" className="btn-accept" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={onApply}>
						{busy ? t('Applying…') : run.patchApplication ? t(localPi ? 'Retry writing to repository' : 'Retry Apply') : t(localPi ? 'Write changes to repository' : 'Apply accepted Patch')}
					</ActionButton>
				)}
			</div>
		  )}
        </div>
      </section>

      <AuditDrawer run={run} />
    </div>
  )
}

function ReviewForm({ comment, busy, applyOnAccept, plainLanguage, onComment, onReview, onChangeRequirement }: {
  comment: string
  busy: boolean
  applyOnAccept: boolean
  plainLanguage?: boolean
  onComment: (value: string) => void
  onReview: (kind: 'accept' | 'reject', comment: string, rejectionClass?: RejectionClass) => void
  onChangeRequirement: () => void
}) {
  const { t } = useI18n()
  return (
    <div className="review-form">
      {!plainLanguage && <p>{t('Review the change and record your decision.')}</p>}
      <label>
        {t('Comment (optional)')}
        <textarea value={comment} onChange={(event) => onComment(event.target.value)} aria-describedby="review-comment-help" placeholder={t('Add a comment…')} />
      </label>
      <p id="review-comment-help" className="section-note">{t('Sent to the Agent when you ask for a fix.')}</p>
      <div className="form-actions">
        <ActionButton type="button" className="btn-accept" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={() => onReview('accept', comment.trim())}>
          {applyOnAccept ? t(plainLanguage ? 'Write changes to repository' : 'Accept & Apply') : t('Accept')}
        </ActionButton>
        <ActionButton type="button" className="btn-secondary" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={() => onReview('reject', comment.trim(), 'implementation_gap')}>
          {t('Ask Agent to fix')}
        </ActionButton>
        <ActionButton type="button" className="btn-secondary" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={onChangeRequirement}>
          {t('Change requirement')}
        </ActionButton>
      </div>
    </div>
  )
}
