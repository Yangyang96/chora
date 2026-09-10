import type { DeliveryStatus, DeliverySummary } from './taskDeliveryTypes'
import type { TaskDisplayStatus } from './taskStatus'

export const deliveryStages = ['uncommitted', 'committed', 'pushed', 'pr_open', 'merged', 'cleaned'] as const
export function deliveryRank(status: DeliveryStatus): number {
  // A closed, unmerged PR never counts as a merge.
  return status === 'pr_closed' ? 3 : deliveryStages.indexOf(status as typeof deliveryStages[number])
}

export function deliveryStatusLabel(status: DeliveryStatus): string {
  return ({ closed: 'Remaining results closed', legacy: 'Legacy delivery', no_change: 'No changes', awaiting_review: 'Awaiting review', uncommitted: 'Ready to commit',
    committed: 'Committed locally', pushed: 'Task branch pushed', pr_open: 'Pull request open', pr_closed: 'Pull request closed',
    merged: 'Merged', cleaned: 'Task worktree cleaned up', recovery_required: 'Recovery required' })[status]
}

export function deliveryDisplayStatus(delivery: DeliverySummary): TaskDisplayStatus {
  if (delivery.unavailable || !delivery.repositories.length) return { label: 'Delivery status unavailable', action: 'View delivery details', tone: 'attention' }
  const repos = delivery.repositories.filter((repo) => repo.status !== 'no_change')
  if (!repos.length) return { label: 'No changes to deliver', action: 'View history', tone: 'success' }
  if (repos.every(repo => repo.status === 'closed')) return { label: 'Remaining results closed', action: 'View history', tone: 'ready' }
  if (repos.some(repo => repo.status === 'closed')) return { label: 'Some results closed', action: 'View delivery details', tone: 'attention' }
  if (repos.some((repo) => repo.status === 'recovery_required')) return { label: 'Delivery needs attention', action: 'View delivery details', tone: 'attention' }
  if (repos.some((repo) => repo.status === 'pr_closed')) return { label: 'PR closed · Not merged', action: 'View delivery details', tone: 'attention' }
  if (repos.some((repo) => repo.status === 'legacy')) return { label: 'Local delivery', action: 'View delivery details', tone: 'ready' }
  if (repos.some((repo) => repo.status === 'awaiting_review')) return { label: 'Waiting for review', action: 'Review result', tone: 'attention' }
  const ranks = repos.map((repo) => deliveryRank(repo.status))
  const least = Math.min(...ranks)
  const most = Math.max(...ranks)
  const action = ['Commit task branch', 'Push task branch', 'Create pull request', 'Review pull request', 'Clean up task worktree', 'View history'][least]
  if (least !== most) {
    const label = ['Ready to commit', 'Committed {count}/{total}', 'Pushed {count}/{total}', 'PR opened {count}/{total}', 'Merged {count}/{total}', 'Cleaned up {count}/{total}'][most]
    return { label, values: { count: ranks.filter((rank) => rank >= most).length, total: repos.length }, action, tone: 'attention' }
  }
  return { label: deliveryStatusLabel(deliveryStages[least]), action, tone: least >= 4 ? 'success' : least === 0 || least === 3 ? 'attention' : 'ready' }
}
