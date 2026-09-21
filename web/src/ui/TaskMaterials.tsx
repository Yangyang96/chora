import { useEffect, useState } from 'react'
import { api, message } from '../api'
import { useI18n } from '../i18n'
import type { ProjectDocumentRevision, TaskMaterialInput } from '../projectDocumentTypes'

const emptyMaterial = (): TaskMaterialInput => ({ title: '', locator: '', body: '' })

function documentVersion(revision: ProjectDocumentRevision) {
  const match = revision.locator?.match(/\/revisions\/(\d+)\/?$/)
  return match?.[1] ?? revision.revisionNumber ?? revision.id
}

export function TaskMaterials({ roomId, enabled, busy, onMaterialsChange, onRevisionIdsChange, onBlockedChange }: {
  roomId: string
  enabled: boolean
  busy: boolean
  onMaterialsChange: (materials: TaskMaterialInput[]) => void
  onRevisionIdsChange: (revisionIds: string[]) => void
  onBlockedChange: (reason: string) => void
}) {
  const { t } = useI18n()
  const [materials, setMaterials] = useState<TaskMaterialInput[]>([emptyMaterial()])
  const [revisions, setRevisions] = useState<ProjectDocumentRevision[]>([])
  const [selected, setSelected] = useState<string[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    setMaterials([emptyMaterial()]); setSelected([]); setRevisions([]); setLoading(true); setLoadError('')
    onMaterialsChange([]); onRevisionIdsChange([])
    void api<ProjectDocumentRevision[] | { revisions: ProjectDocumentRevision[] }>(`/api/rooms/${encodeURIComponent(roomId)}/revisions`, { signal: controller.signal })
      .then((value) => {
        if (controller.signal.aborted) return
        const all = Array.isArray(value) ? value : value.revisions ?? []
        setRevisions(all.filter((revision) => revision.locator?.startsWith('chora://project-document/')))
      })
      .catch((reason) => { if (!controller.signal.aborted) setLoadError(message(reason)) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [roomId, onMaterialsChange, onRevisionIdsChange])

  useEffect(() => {
    if (!enabled) { onBlockedChange(''); return }
    const complete = materials.filter((material) => material.title.trim() && material.locator.trim() && material.body.trim())
    const partial = materials.some((material) => Boolean(material.title.trim() || material.locator.trim() || material.body.trim()) && !(material.title.trim() && material.locator.trim() && material.body.trim()))
    const encoder = new TextEncoder()
    const locatorTooLarge = materials.some(material => encoder.encode(material.locator).length > 1024)
    const bodiesTooLarge = materials.reduce((total, material) => total + encoder.encode(material.body).length, 0) > 512 * 1024
    onBlockedChange(partial ? t('Complete or remove every supplied material.')
      : complete.length === 0 ? t('Add at least one supplied material.')
      : locatorTooLarge ? t('A source locator exceeds the 1 KiB limit.')
      : bodiesTooLarge ? t('Supplied material exceeds the 512 KiB total limit.') : '')
  }, [enabled, materials, onBlockedChange, t])

  function update(index: number, field: keyof TaskMaterialInput, value: string) {
    const next = materials.map((material, itemIndex) => itemIndex === index ? { ...material, [field]: value } : material)
    setMaterials(next)
    onMaterialsChange(next.filter((material) => material.title.trim() && material.locator.trim() && material.body.trim()).map((material) => ({ title: material.title.trim(), locator: material.locator.trim(), body: material.body })))
  }

  function remove(index: number) {
    const next = materials.filter((_, itemIndex) => itemIndex !== index)
    const retained = next.length ? next : [emptyMaterial()]
    setMaterials(retained)
    onMaterialsChange(retained.filter((material) => material.title.trim() && material.locator.trim() && material.body.trim()).map((material) => ({ title: material.title.trim(), locator: material.locator.trim(), body: material.body })))
  }

  function toggleRevision(id: string, checked: boolean) {
    const next = checked ? [...selected, id] : selected.filter((value) => value !== id)
    setSelected(next); onRevisionIdsChange(next)
  }

  return <section className="task-materials" aria-label={t('Task context')}>
    {enabled && <>
      <h3>{t('Supplied material')}</h3>
      <p className="section-note">{t('Paste Markdown content explicitly. Source locators are references only; Chora will not fetch them or read local paths.')}</p>
      {materials.map((material, index) => <div className="task-resource-card" key={index}>
        <label>{t('Material title')}<input value={material.title} maxLength={160} disabled={busy} onChange={(event) => update(index, 'title', event.target.value)} /></label>
        <label>{t('Source locator')}<input value={material.locator} maxLength={2048} disabled={busy} placeholder="https://…" onChange={(event) => update(index, 'locator', event.target.value)} /></label>
        <label>{t('Markdown content')}<textarea value={material.body} maxLength={262144} rows={8} disabled={busy} onChange={(event) => update(index, 'body', event.target.value)} /></label>
        <button type="button" className="btn-secondary" disabled={busy} onClick={() => remove(index)}>{t('Remove material')}</button>
      </div>)}
      <button type="button" className="btn-secondary" disabled={busy || materials.length >= 16} onClick={() => setMaterials((current) => [...current, emptyMaterial()])}>{t('Add material')}</button>
    </>}
    <h3>{t('Project documents')}</h3>
    <p className="section-note">{t('Select exact accepted Project document revisions to add to this task. None are selected by default.')}</p>
    {loading ? <p>{t('Loading Project documents…')}</p> : loadError ? <p role="alert" className="error-banner">{loadError}</p> : revisions.length === 0 ? <p className="section-note">{t('No accepted Project documents are available.')}</p> : revisions.map((revision) => <label key={revision.id}>
      <input type="checkbox" disabled={busy} checked={selected.includes(revision.id)} onChange={(event) => toggleRevision(revision.id, event.target.checked)} />
      {revision.title} · {t('Revision')} {documentVersion(revision)}
    </label>)}
  </section>
}
