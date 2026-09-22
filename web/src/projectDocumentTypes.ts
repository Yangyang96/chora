export type TaskMaterialInput = {
  title: string
  locator: string
  body: string
}

export type ProjectDocumentRevision = {
  id: string
  title: string
  body: string
  locator?: string
  revisionNumber?: number
  confirmedAt: string
}

export type TaskContextSelection = {
  synthesize?: boolean
  outcomeKind?: 'document'
  materials?: TaskMaterialInput[]
  revisionIds?: string[]
}
