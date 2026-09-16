export type AppPreviewState = 'idle' | 'starting' | 'running' | 'stopped' | 'failed' | 'recovery_required'

export type AppPreviewConfig = {
  command: string
  workingDirectory: string
  port: number
}

export type AppPreviewSuggestion = AppPreviewConfig

export type AppPreviewView = {
  runId: string
  taskId: string
  profile: string
  available: boolean
  reason?: string
  repositories: Array<{ repoId: string; name: string }>
  repoId: string
  preview: {
    state: AppPreviewState
    config?: AppPreviewConfig
    url?: string
    logs?: string
    logTruncated?: boolean
    reason?: string
  }
  suggestions: AppPreviewSuggestion[]
}
