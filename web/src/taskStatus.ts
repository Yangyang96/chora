import type { AutomaticRetry, CurrentActionKind, RunStatus, TaskSummary } from './types'
import { deliveryDisplayStatus } from './deliveryProgress'

export type TaskStateTone = 'working' | 'attention' | 'success' | 'ready' | 'muted'

export type TaskDisplayStatus = {
  label: string
  action: string
  values?: Record<string, string | number>
  detail?: string
  tone: TaskStateTone
}

const actionLabels: Record<CurrentActionKind, string> = {
  edit_plan: 'Edit plan',
  review_plan: 'Review plan',
  start_run: 'Start run',
  start_attempt: 'Start attempt',
  monitor_run: 'Monitor run',
  recover_run: 'Recover run',
  recover_verification: 'Recover verification',
  review_result: 'Review result',
  apply_patch: 'Apply accepted changes',
  retry_implementation: 'Retry implementation',
  continue_successor_plan: 'Continue successor plan',
  open_related_task: 'Open related task',
  view_terminal: 'View history',
  none: 'No action needed',
}

export function taskActionLabel(kind: CurrentActionKind): string {
  return actionLabels[kind]
}

export function agentFailureLabel(reason?: string): string {
  return ({
    runtime_output_limit_exceeded: 'Agent output limit exceeded',
    runtime_stream_invalid: 'Agent output was invalid',
    attempt_timeout: 'Agent execution timed out',
    runtime_start_failed: 'Agent could not start',
    runtime_exit_nonzero: 'Agent exited with an error',
    agent_model_error: 'Agent model request failed',
    result_contract_invalid: 'Agent result was incomplete',
    review_patch_materialization_failed: 'Could not prepare changes for review',
    runtime_policy_violation: 'Execution policy violation',
    context_consumption_invalid: 'Agent context validation failed',
    checks_failed: 'Checks failed',
    checks_incomplete: 'Checks incomplete',
  } as Record<string, string>)[reason ?? ''] ?? 'Agent execution failed'
}

export function automaticRetryActive(retry?: AutomaticRetry): boolean {
  return retry?.state === 'pending' || retry?.state === 'retrying'
}

export function agentFailureStatus(status: RunStatus, retry?: AutomaticRetry, reason?: string): TaskDisplayStatus | undefined {
  if (status === 'stopping' || status === 'cancelled') return undefined
  if (retry && ['ready', 'running', 'recovery_required'].includes(status)) {
    const detail = agentFailureLabel(retry.state === 'blocked' && reason ? reason : retry.lastFailureReason)
    if (automaticRetryActive(retry)) return {
      label: retry.state === 'pending' ? 'Waiting to retry {current}/{max}' : 'Automatically retrying {current}/{max}',
      values: { current: retry.state === 'pending' ? retry.retriesUsed + 1 : retry.retriesUsed, max: retry.maxRetries },
      action: 'Monitor run', tone: 'working', detail,
    }
    if (retry.state === 'exhausted') return {
      label: 'Failed {count} times · Needs attention', values: { count: retry.retriesUsed + 1 },
      action: 'Recover run', tone: 'attention', detail,
    }
    if (retry.state === 'blocked') return { label: 'Automatic retry blocked · Needs attention', action: 'Recover run', tone: 'attention', detail }
  }
  if (status === 'recovery_required' && reason) return { label: 'Agent failed · Needs attention', action: 'Recover run', tone: 'attention', detail: agentFailureLabel(reason) }
  return undefined
}

export function taskDisplayStatus(task: TaskSummary): TaskDisplayStatus {
  const action = taskActionLabel(task.currentAction.kind)
  const run = task.latestRun

  if (!run) {
    if (task.status !== 'open') return { label: 'Closed', action, tone: 'muted' }
    return { label: 'Ready', action, tone: 'ready' }
  }

  if (task.resultClosed && run.status !== 'accepted') return { label: 'Remaining results closed', action: 'View history', tone: 'muted' }

  const failure = agentFailureStatus(run.status, run.automaticRetry, run.terminalReason)
  if (failure) return failure

  switch (run.status) {
    case 'running':
      return { label: 'Working', action, tone: 'working' }
    case 'stopping':
      return { label: 'Stopping', action, tone: 'working' }
    case 'awaiting_verification':
    case 'verifying':
      return { label: 'Verifying', action, tone: 'working' }
    case 'awaiting_review':
      return { label: 'Waiting for review', action, tone: 'attention' }
    case 'revision_required':
      return { label: 'Changes requested', action, tone: 'attention' }
    case 'recovery_required':
    case 'verification_recovery_required':
      return { label: 'Recovery needed', action, tone: 'attention' }
    case 'completed':
      return { label: 'Completed · No changes', action, tone: 'success' }
    case 'accepted':
      if (task.delivery) return deliveryDisplayStatus(task.delivery)
      if (run.patchApplicationState === 'applied') return { label: 'Applied', action, tone: 'success' }
      if (run.patchApplicationState === 'applying') return { label: 'Applying', action, tone: 'working' }
      if (['conflict', 'failed', 'recovery_required'].includes(run.patchApplicationState ?? '')) return { label: 'Recovery needed', action, tone: 'attention' }
      return { label: 'Accepted', action, tone: 'success' }
    case 'cancelled':
      return { label: 'Cancelled', action, tone: 'muted' }
    default:
      return { label: task.status === 'open' ? 'Ready' : 'Closed', action, tone: task.status === 'open' ? 'ready' : 'muted' }
  }
}
