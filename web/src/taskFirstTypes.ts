export type RepositoryResourceView = {
  repoId: string; name: string; localLocator: string; state: 'active' | 'removed'
  version: number; availability: 'ready' | 'unavailable' | 'identity_drift' | 'unborn' | 'legacy_unverified'
  branch: string; head: string; dirty: boolean; reason?: string
}
export type RoomResourcesView = { version: number; repoIds: string[] }
export type TaskScopeSelection = { mode: 'repository' | 'restricted'; writableFiles: string[]; writableDirectories: string[]; protectedDirectories: string[]; migrationChoice: 'not_needed' | 'legacy' | 'repository' }
export type CheckCommandView = { id: string; name: string; version: number; command: string; argv?: string[]; workingDirectory: string; source: string }
export type CheckPolicySelection = { mode: 'auto' | 'named' | 'none'; commands: CheckCommandView[]; preparation: CheckCommandView[]; selectionSource: string }
export type TaskResourceSelection = { repoId: string; associationVersion: number; role: 'write' | 'reference'; targetRef?: string; scope: TaskScopeSelection; checks: CheckPolicySelection }
export type RepositoryPageView = { repositories: RepositoryResourceView[]; nextCursor: string }
export type RepositoryBranchPageView = { branches: Array<{ ref: string; commit: string }>; nextCursor: string }
export type RepositoryDeliveryDefaultsView = { targetRef: string; version: number; suggestedTargetRef: string; reason: string }
export type TaskOptionsView = { repositories: RepositoryResourceView[]; selectedRepoIds: string[]; selectionSource: string; legacyScope?: { repoId: string; writableFiles: string[]; writableDirectories: string[] }; defaultCheckMode: 'auto' | 'named' | 'none'; legacyChecks?: CheckCommandView[]; nextRepositoryCursor?: string }
export type RepositoryEntriesView = { entries: { name: string; path: string; kind: 'directory' | 'file' | 'unsupported' }[]; nextCursor: string; truncated: boolean; revision: string }
export type ResourcePatchLineView = { kind: 'context' | 'added' | 'removed'; oldLine?: number; newLine?: number; text: string }
export type ResourcePatchHunkView = { header: string; oldStart: number; oldCount: number; newStart: number; newCount: number; lines: ResourcePatchLineView[] }
export type ResourcePatchFileView = { kind?: 'added' | 'modified' | 'deleted'; path: string; additions?: number; deletions?: number; hunks?: ResourcePatchHunkView[]; lines: ResourcePatchLineView[] }
export type DerivedResourceCheckView = {
  CheckID: string; Name: string; Version: number; Source?: string; Argv: string[]; WorkingDirectory: string
  ToolCallID: string; CommandDigest: string; ProviderSucceeded: boolean | null; ExitCode: number | null
  ObservedStatus: string; Status: string; Evidence: string; ContentFingerprint: string
  FinalContentFingerprint: string; FinalContentVerified: boolean; Stale: boolean
}
export type ResourceCheckDerivationView = {
  RepositoryID: string; Mode: 'auto' | 'named' | 'none'; Status: string; Explanation: string
  FinalContentVerified: boolean; NoApplicableChecks?: boolean; SelectionSource?: string; Checks: DerivedResourceCheckView[]
}
export type ResourceResultRepositoryView = {
  repoId: string; baseCommit: string; baseTree: string; patchDigest: string; patchLocator: string
  changedPaths: string[]; checks: ResourceCheckDerivationView
}
export type ResourceResultView = {
  repositories?: Array<{ repoId: string; name: string; baseRef: string; deliveryMode?: 'task_branch'; taskBranch?: string; worktreePath?: string }>
  group: {
    schemaVersion: string; id: string; runId: string; taskId: string; attemptId: string; agentReportId: string
    resourceSnapshotDigest: string; contractDigest: string; contextDigest: string; outcome: string
    finalAssistant?: { eventId: string; sequence: number; textDigest: string; complete: boolean }; repositories: ResourceResultRepositoryView[]; createdAt: string
  }
  digest: string
  patches: Array<{ repoId: string; files: ResourcePatchFileView[] }>
}
export type ResourceApplyView = {
  operationId: string
  status: 'pending' | 'partial' | 'applied' | 'conflict' | 'recovery_required' | 'remaining_closed' | 'partial_applied'
  repositories: Array<{ repoId: string; status: string; reason?: string; sequence: number }>
}
