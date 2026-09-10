export type DeliveryKind = 'commit' | 'push' | 'pr' | 'merge' | 'cleanup'
export type DeliveryStatus = 'closed' | 'legacy' | 'no_change' | 'awaiting_review' | 'uncommitted' | 'committed' | 'pushed' | 'pr_open' | 'pr_closed' | 'merged' | 'cleaned' | 'recovery_required'
export type DeliveryOperationStatus = 'preview' | 'writing' | 'succeeded' | 'failed' | 'recovery_required'
export type DeliverySummary = { repositories: Array<Pick<DeliveryRepository, 'repoId' | 'name' | 'status'>>; unavailable?: boolean }

export type DeliveryOperation = {
  id: string
  repoId: string
  kind: DeliveryKind
  status: DeliveryOperationStatus
  version: number
  message?: string
  paths?: string[]
  head?: string
  tree?: string
  remote?: string
  url?: string
  remoteRef?: string
  remoteHead?: string
  targetHead?: string
  commit?: string
  prUrl?: string
  title?: string
  body?: string
  repository?: string
  headBranch?: string
  baseBranch?: string
  baseHead?: string
  mergeMethod?: string
  number?: number
  worktreePath?: string
  reason?: string
}

export type DeliveryRepository = {
  repoId: string
  name: string
  targetBranch: string
  taskBranch: string
  status: DeliveryStatus
  commit?: string
  remote?: string
  url?: string
  prUrl?: string
  repository?: string
  headBranch?: string
  baseBranch?: string
  baseHead?: string
  mergeMethod?: string
  number?: number
  worktreePath?: string
  reason?: string
  operations: DeliveryOperation[]
}

export type DeliveryCapabilities = { hosting?: boolean; createPR?: boolean; merge?: boolean; cleanup?: boolean; provider?: string; reason?: string }
export type TaskDeliveryView = { version?: number; capabilities?: DeliveryCapabilities; repositories: DeliveryRepository[] }
