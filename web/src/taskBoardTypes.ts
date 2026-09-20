export const boardPhases = ['preparing', 'working', 'review', 'delivery', 'finished'] as const
export type BoardPhase = typeof boardPhases[number]
export type BoardOption = { id: string; name: string }
export type BoardCard = {
  projectId: string; roomId: string; roomName: string; taskId: string; title: string
  repositories: { repoId: string; name: string; status?: string; checks?: string; deliveryObservedAt?: string; achievements?: string[] }[]
  phase: BoardPhase | null; substate: string; outcome: string | null
  attention: { state: 'none' | 'required' | 'unknown'; reasons: { code: string; summary?: string; targetId?: string; expectedVersion?: number }[] }
  nextAction: { kind: string; url: string; availabilityReason?: string }
  visibility: { taskArchived: boolean; roomArchived: boolean; projectArchived: boolean; readOnly: boolean }
  health: 'current' | 'stale' | 'unavailable' | 'inconsistent'
  lastActivityAt: string
  source: { latestRunId?: string; latestRunVersion?: number; latestAttemptId?: string; observedAt?: string }
}
export type TaskBoardPage = {
  snapshot: string; cards: BoardCard[]; total: number; nextCursor?: string; observedAt: string
  optionsSnapshot?: string; nextOptionsCursor?: string
  counts: { phase: Record<string, number>; attention: number; reconciliation: number }
  rooms: { roomId: string; name: string }[]; repositories: { repoId: string; name: string }[]
}
