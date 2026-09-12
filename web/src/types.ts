import type { ResourceApplyView, ResourcePatchFileView, ResourceResultView } from './taskFirstTypes'

export type Provenance = { kind: string; candidate_id?: string; candidate_decision_id?: string; source_run_id?: string; source_review_id?: string; source_artifact_id?: string; actor: string }
export type RevisionView = { id: string; title: string; body: string; locator?: string; revisionNumber?: number; digest: string; provenance: Provenance; confirmedAt: string }
export type SelectionItem = { revisionId: string; digest: string; provenance: Provenance; reason?: string }
export type SelectionView = { taskId: string; selected: SelectionItem[]; excluded: SelectionItem[]; createdAt: string }
export type RoomState = 'active' | 'archived'
export type RoomOwnershipKind = 'project' | 'legacy_standalone' | 'unclassified'
export type RoomRef = { id: string; projectId?: string; ownershipKind?: RoomOwnershipKind; name: string; description: string; state?: RoomState; version?: number; archivedAt?: string; initialRevision?: RevisionView; revisions?: RevisionView[] }
export type RoomSummary = Required<Pick<RoomRef, 'id' | 'name' | 'description'>> & {
  projectId?: string; ownershipKind?: RoomOwnershipKind
  state: RoomState; version: number; archivedAt: string; lastActivityAt: string
  taskCounts: { total: number; open: number; terminal: number }
  humanActionRequired: boolean
}
export type CurrentActionKind = 'edit_plan' | 'review_plan' | 'start_run' | 'start_attempt' | 'monitor_run' | 'recover_run' | 'recover_verification' | 'review_result' | 'apply_patch' | 'retry_implementation' | 'continue_successor_plan' | 'open_related_task' | 'view_terminal' | 'none'
export type CurrentAction = {
  kind: CurrentActionKind
  target: { roomId?: string; taskId?: string; runId?: string; draftId?: string; revisionId?: string; planReviewId?: string; resultId?: string; resultReviewId?: string; expectedVersion?: number }
  url: string
  reason: string
}
export type AutomaticRetry = { state: 'pending' | 'retrying' | 'exhausted' | 'blocked'; retriesUsed: number; maxRetries: number; lastFailureReason: string }
export type RunSummary = { automaticRetry?: AutomaticRetry; terminalReason?: string; patchApplicationState?: string; id: string; taskId: string; status: RunStatus; version: number; attempt: number; createdAt: string; updatedAt: string; startedAt: string; reviewRequestedAt: string; terminalAt: string }
export type TaskSummary = { resultClosed?: boolean; id: string; title: string; status: string; archived: boolean; lastActivityAt: string; currentPlanRevision: { id: string; revisionNumber: number } | null; latestRun: RunSummary | null; runCount: number; currentAction: CurrentAction; delivery?: import('./taskDeliveryTypes').DeliverySummary }
export type RoomWorkspace = { room: RoomSummary; tasks: TaskSummary[] }
export type RoomDirectory = { activeRooms: RoomSummary[]; archivedRooms: RoomSummary[] }
export type RunHistorySummary = RunSummary & { eventCount: number }
export type TaskRunHistory = { roomId: string; taskId: string; runs: RunHistorySummary[] }
export type TechnicalPlanContent = { technical_steps: string[]; decisions: string[]; risks: string[]; unknowns: string[] }
export type TechnicalPlanDraft = {
  id: string; taskId: string; editVersion: number; predecessorRevisionId?: string; nextRevisionNumber: number; selectionDigest: string
  content: TechnicalPlanContent
  createdAt: string
  updatedAt: string
}
export type TechnicalPlanReview = { id: string; kind: 'accept' | 'request_revision'; reviewer: string; note: string; decidedAt: string }
export type TechnicalPlanRevision = {
  id: string; taskId: string; sourceDraftId: string; revisionNumber: number; predecessorRevisionId?: string
  content: TechnicalPlanContent; contentDigest: string; selectionDigest: string; unchanged: boolean; submittedAt: string
  review?: TechnicalPlanReview; current: boolean
}
export type TechnicalPlanAcceptance = { revisionId: string; reviewId: string; snapshotId: string; snapshotDigest: string; charterId: string; adapterId: 'fake' | 'pi'; boundAt: string }
export type TechnicalPlanning = { draft?: TechnicalPlanDraft; revisions: TechnicalPlanRevision[]; acceptance?: TechnicalPlanAcceptance }
export type FrozenSnapshotRef = { id: string; digest: string; status: 'materialized' | 'registered' }
export type TaskExecutionProfile = 'diagnostic_fake' | 'real_spec_coding'
export type AgentExecutionProfile = 'minimal' | 'standard' | 'isolated_local' | 'trusted_local'
export type IsolatedLocalState = 'not_prepared' | 'preparing' | 'ready' | 'failed' | 'restart_required'
export type IsolatedLocalView = {
  state: IsolatedLocalState
  reason: string
  preparationAvailable: boolean
  imageId?: string
  piVersion: '0.85.1'
  nodeVersion: '22.19.0'
  policy: { network: string; resources: string; files: string; credentials: string }
}
export type AgentExecution = {
  attemptTimeoutSeconds?: number
  costStatus?: string
  reportedCost?: number
  profile: AgentExecutionProfile
  runtimeSource: string
  executionProvider: string
  capabilityPolicy: string
  trustDisclosurePolicy: string
  sandboxed: boolean
  disclosureLabel: string
}
export type PiDiscoveryState = 'restart_required' | 'installing' | 'drifted' | 'ready' | 'missing' | 'not_executable' | 'incompatible_version' | 'unconfigured' | 'unavailable'
export type PiDiscoveryView = {
  state: PiDiscoveryState
  executablePath?: string
  version?: string
  executableSha256?: string
  readyProviders: string[]
  notReadyProviders: string[]
  reason?: string
}
export type TrustedLocalAcknowledgement = {
  policyVersion: string
  actorId: string
  sessionId: string
  acknowledgedAt: string
  replayed: boolean
}
export type TrustedLocalAcknowledgementState = {
  acknowledged: boolean
  policyVersion: string
  acknowledgedAt?: string
}
export type ProjectRoomView = { id: string; projectId?: string; ownershipKind?: RoomOwnershipKind; name: string; description: string; workspaceRoot?: string; state: RoomState; version: number; archivedAt: string; lastActivityAt?: string; taskCounts?: { total: number; open: number; terminal: number }; humanActionRequired?: boolean }
export type RepositoryBindingView = {
  roomId: string; name: string; localLocator: string; sourceKind: 'open' | 'clone'; cloneUrl: string
  admittedBase: string; baseIdentity: string; targetWorktree: string; dirtyAdmitted: boolean
  state: 'active' | 'removed'; version: number; createdAt: string; updatedAt: string
}
export type ProjectState = 'active' | 'archived'
export type ProjectView = {
  id: string; name: string; description?: string; state: ProjectState; version: number; defaultRoomId: string; rooms: ProjectRoomView[]
  currentAction?: CurrentAction | null; currentTaskTitle?: string; room: ProjectRoomView; repositoryBinding?: RepositoryBindingView; repositories?: import('./taskFirstTypes').RepositoryResourceView[]; nextRepositoryCursor?: string
  lastActivityAt: string; taskCounts: { total: number; open: number; terminal: number }
}
export type ProjectDirectory = { projects: ProjectView[] }
export type RepositoryIdentity = { name: string; sourceRevision: string; baselineDigest: string }
export type TaskWorktree = { baseRevision?: string; baseTree?: string; baseRef?: string; startPolicy?: string; locator: string; state: 'provisioning' | 'ready' | 'recovery_required'; reason?: string }
export type RealSpecCodingInput = {
  requirement: string; constraints: string[]; outOfScope: string[]
  criteria: Array<{ title: string; description: string; verificationCommandIndexes: number[] }>
  writableFiles: string[]; verificationCommands: Array<{ argv: string[] }>
}
export type TaskCreationInput = { title: string; executionProfile: TaskExecutionProfile; agentExecutionProfile: AgentExecutionProfile; revisionIds: string[]; goal?: string; criteria?: string[]; realSpecCoding?: RealSpecCodingInput }
export type TaskRef = { id: string; roomId?: string; title?: string; goal?: string; executionProfile?: TaskExecutionProfile; agentExecutionProfile?: AgentExecutionProfile; repository?: RepositoryIdentity; worktree?: TaskWorktree; criteria?: Array<{ id: string; title: string; description?: string }>; selection?: SelectionView; planning?: TechnicalPlanning; frozenSnapshot?: FrozenSnapshotRef }
export type AdapterStatus = { enabled: boolean; reason: string; hostReadIsolation: string }
export type RuntimeStatus = { codex: AdapterStatus; pi?: AdapterStatus & { provider: string; image: string; policyFingerprint: string; dockerVersion: string; colimaVersion: string; engineIdentityDigest?: string; dockerContext?: string; contextEndpointDigest?: string; dockerServerVersion?: string }; verifier?: { enabled: boolean; reason: string; mode: string; policyVersion: string; policyDigest: string; baselineDigest: string; image: string; network: string; credentials: string; resourceBoundary: string } }
export type ReadinessItem = { key: string; state: 'checking' | 'ready' | 'blocked'; observed: string; required: string; action: string }
export type ReadinessView = { checkedAt: string; inputFingerprint: string; items: ReadinessItem[] }
export type ProductInstallationDoctor = {
  schemaVersion: 'chora.product-installation-status/v1'
  status: 'ready' | 'blocked' | 'completed'
  reasonCode: '' | 'observation_unavailable' | 'identity_mismatch' | 'authority_required' | 'idempotency_conflict' | 'installation_busy' | 'capability_probe_failed' | 'generation_referenced' | 'asset_identity_conflict' | 'invalid_request' | 'operation_failed'
  engine: {
    ready: boolean
    apiVersion: string
    operatingSystem: string
    architecture: string
    contextName: string
  }
  activeGenerationId: string
  candidateGenerationId: string
  actions: { setup: boolean; upgrade: boolean; gc: boolean; uninstall: boolean }
  replayed: boolean
  restartRequired: boolean
}
export type RunStatus = 'completed' | 'draft' | 'ready' | 'running' | 'stopping' | 'awaiting_verification' | 'verifying' | 'awaiting_review' | 'accepted' | 'revision_required' | 'recovery_required' | 'verification_recovery_required' | 'cancelled'
export type VerificationDisposition = {
  state: 'available' | 'not_applicable' | 'unavailable'
  reason: string
}
export type PatchApplication = {
  state: 'applying' | 'applied' | 'conflict' | 'recovery_required'
  version: number; patchDigest: string; targetIdentity: string; baseRevision: string; affectedPaths: string[]
  preStateDigest: string; postStateDigest?: string; reason?: string; startedAt: string; updatedAt: string; appliedAt?: string
}
export type AgentPresence = 'available' | 'active' | 'waiting' | 'unresponsive' | 'offline'
export type AgentPhase = 'preparing_context' | 'executing' | 'waiting_for_decision' | 'stopping' | 'awaiting_verification' | 'verifying' | 'review_ready' | 'waiting_for_retry' | 'recovery' | 'verification_recovery_required' | 'completed' | 'cancelled'
export type TrajectoryRecord = {
  sequence: number
  endSequence?: number
  source: string
  kind: 'system' | 'agent' | 'read' | 'edit' | 'file' | 'command' | 'tool' | 'result' | 'check' | 'decision' | 'apply' | 'error'
  title: string
  summary: string
  status: 'running' | 'completed' | 'failed' | 'waiting' | 'cancelled' | 'interrupted'
  startedAt: string
  endedAt?: string
  durationMs?: number
  details: Array<{ label: string; value: string }>
}
export type DecisionGate = {
  id: string
  kind: string
  status: 'open' | 'resolved'
  question: string
  context: string
  options: Array<{ id: string; label: string; impact: string }>
  recommendation: string
  impact: string
  selectedOptionId?: string
  note?: string
  actorId?: string
  sessionId?: string
  requestedAt: string
  resolvedAt?: string
}

export type ModelProvenance = {
  status: 'observed' | 'unknown'
  identities: Array<{ provider: string; modelId: string }>
  reason?: string
}

export type RunView = {
  resultClosed?: boolean
  result?: { id: string; attemptId: string; outcome: string; createdAt: string }
  id: string
  status: RunStatus
  automaticRetry?: AutomaticRetry
  terminalReason?: string
  version: number
  attempt: number
  adapter: string
  agentExecution?: AgentExecution
  room: { id: string; name: string; description: string }
  task: { id: string; title: string; goal: string; worktree?: TaskWorktree }
  plan?: { revisionId: string; content: TechnicalPlanContent; activatedBy: string }
  context: Array<{ kind: string; title: string; body: string }>
  timeline: Array<{ sequence: number; type: string; title: string; detail: string; time: string }>
  trajectory?: TrajectoryRecord[]
  artifacts: Array<{ id: string; kind: string; locator: string; digest?: string; mediaType?: string; description: string }>
  unknowns: string[]
  criteria: Array<{ id: string; title: string; status: string; evidence: string }>
  review?: { kind: string; comment: string; decidedAt: string }
  retry?: { rejectionNote: string; unmetCriterionIds: string[]; instructions: string }
  cancelReason?: string
  candidates?: Array<{ id: string; title: string; body: string; state: 'pending' | 'confirmed' | 'dismissed'; version: number; sourceRunId: string; sourceArtifactId: string; decision?: { id: string; kind: string; note: string; actorId: string; sessionId: string; decidedAt: string }; revision?: RevisionView }>
  revisions?: RevisionView[]
  selection?: SelectionView
  snapshot?: { id: string; digest: string; selected: SelectionItem[]; excluded: SelectionItem[] }
  agent?: {
    profile: { id: string; name: string; role: string; capabilities: string[] }
    presence: AgentPresence
    runState: RunStatus
  }
  attemptDetail?: {
    id: string
    sequence: number
    state: string
    executionWorkspace: string
    runtime?: { sessionId: string; adapterId: string; kind: string; version: string; fingerprint: string; externalSession?: string; state: string }
    agentExecution?: AgentExecution
    modelProvenance?: ModelProvenance
    sandbox: { status: string; provider: string; mode: string; image: string; policyFingerprint: string }
  }
  activity?: {
    phase: AgentPhase
    currentAction: string
    elapsedMs: number
    latestEvent?: { sequence: number; type: string; title: string; detail: string; time: string }
  }
  blockers?: string[]
  decisionGate?: DecisionGate
  decisionHistory?: DecisionGate[]
  attemptHistory?: Array<{ id: string; sequence: number; predecessorId?: string; state: string; snapshotId: string; snapshotDigest: string; agentExecution?: AgentExecution; modelProvenance?: ModelProvenance; summary?: string; artifacts: Array<{ id: string; kind: string; locator: string; digest?: string; mediaType?: string; description: string }>; unknowns: string[] }>
  reviewHistory?: Array<{ kind: string; comment: string; decidedAt: string }>
  contextConsumption?: { snapshotId: string; snapshotDigest: string; candidateRevisionId: string; includedRevisionIds: string[]; excludedRevisionIds: string[]; derivation: string; excludedContentObserved: boolean }
  agentReport?: { id: string; attemptId: string; summary: string; finalText: string; completedAt: string; authority: string; claimedChecks: Array<{ criterionId: string; status: string; evidence: string }> }
  verificationDisposition?: VerificationDisposition
  verification?: {
    id: string
    agentAttemptId: string
    state: string
    bindings: { baselineDigest: string; patchDigest: string; contextSnapshotDigest: string; acceptanceContractDigest: string; verifierPolicyVersion: string; verifierPolicyDigest: string }
    attempts: Array<{ id: string; sequence: number; predecessorId?: string; state: string; evidenceComplete: boolean; cleanupProven: boolean; workspaceIdentity?: string; reason?: string; createdAt: string; updatedAt: string; checks: Array<{ id: string; criterionId: string; status: string; trust: string; evidenceIds: string[] }>; commands: Array<{ id: string; commandId: string; criterionIds: string[]; argv: string[]; classification: string; exitCode?: number; verifierIdentity: string; workspaceIdentity: string; startedAt: string; endedAt: string; stdout: { fullSha256: string; totalBytes: number; retainedBody: string; truncated: boolean; truncationBoundary: number; redactionPolicyVersion: string }; stderr: { fullSha256: string; totalBytes: number; retainedBody: string; truncated: boolean; truncationBoundary: number; redactionPolicyVersion: string } }> }>
    result?: { id: string; attemptId: string; outcome: 'review_ready' | 'needs_revision'; createdAt: string }
  }
  verificationHistory?: Array<NonNullable<RunView['verification']>>
  reviewablePatch?: {
    evidenceKind?: 'agent_reported' | 'independent_verification'
    agentReportId?: string
    patchDigest: string
    baselineDigest: string
    declaredFilesDigest: string
    resultId: string
    agentAttemptId: string
    verificationAttemptId: string
    artifactId: string
    rawDownload: string
    files: ResourcePatchFileView[]
  }
  verifiedReview?: { id: string; resultId: string; kind: 'accept' | 'reject'; reason: string; rejectionClass?: 'implementation_gap' | 'planning_gap' | 'contract_change_required'; actorId: string; sessionId: string; patchDigest: string; sourceTaskId?: string; planningDraftId?: string; relatedTaskId?: string; decidedAt: string }
  verifiedReviewHistory?: Array<NonNullable<RunView['verifiedReview']>>
  patchApplication?: PatchApplication
  resourceResult?: ResourceResultView
  resourceApply?: ResourceApplyView
  scm?: { configured: boolean; canCreateDraftMR: boolean; reason: string }
  controls?: { canCloseResult?: boolean; canCancel: boolean; canRetry: boolean; canReview: boolean; canAcceptAndApply?: boolean; canStartVerification?: boolean; canCancelVerification?: boolean; canRetryVerification?: boolean; canApplyPatch?: boolean; canCreateDraftMR?: boolean; canSwitchAgentExecutionProfile?: boolean }
}

export const defaultReviewNote = 'The structured Room evidence is sufficient to make a decision without opening a raw transcript.'
export const defaultRetryInstructions = 'Resolve the rejected acceptance gap and return only the updated structured result.'
export const defaultCancelReason = 'Cancelled by the local user.'
