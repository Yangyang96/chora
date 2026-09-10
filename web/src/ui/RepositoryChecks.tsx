import { ActionButton } from './ActionButton'
import { useEffect, useRef, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import type { CheckCommandView, RepositoryResourceView } from '../taskFirstTypes'
import { RepositoryDirectoryPicker } from './RepositoryDirectoryPicker'

type RepositoryChecksProps = {
  projectId: string
  repository: RepositoryResourceView
  disabled?: boolean
}

type CheckDraft = Pick<CheckCommandView, 'id' | 'name' | 'version' | 'command' | 'workingDirectory'>

function newDraft(): CheckDraft {
  return {
    id: commandKey('repository-check'),
    name: '',
    version: 0,
    command: '',
    workingDirectory: '.',
  }
}

export function RepositoryChecks({ projectId, repository, disabled = false }: RepositoryChecksProps) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [checks, setChecks] = useState<CheckCommandView[]>([])
  const [draft, setDraft] = useState<CheckDraft | undefined>()
  const [choosingDirectory, setChoosingDirectory] = useState(false)
  const [error, setError] = useState('')
  const request = useRef<AbortController | undefined>(undefined)
  const endpoint = `/api/v2/projects/${encodeURIComponent(projectId)}/repositories/${encodeURIComponent(repository.repoId)}/checks`

  useEffect(() => () => request.current?.abort(), [])

  async function load() {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    setLoading(true)
    setError('')
    try {
      const result = await api<{ checks: CheckCommandView[] }>(endpoint, { signal: controller.signal })
      if (!controller.signal.aborted) setChecks(result.checks ?? [])
    } catch (reason) {
      if (!controller.signal.aborted) setError(message(reason))
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }

  function show() {
    setOpen(true)
    setDraft(undefined)
    setChoosingDirectory(false)
    void load()
  }

  function close() {
    request.current?.abort()
    setOpen(false)
    setDraft(undefined)
    setChoosingDirectory(false)
    setError('')
  }

  function edit(check: CheckCommandView) {
    setDraft({
      id: check.id,
      name: check.name,
      version: check.version,
      command: check.command,
      workingDirectory: check.workingDirectory || '.',
    })
    setChoosingDirectory(false)
    setError('')
  }

  async function save() {
    if (!draft || !draft.name.trim() || !draft.command.trim()) return
    setSaving(true)
    setError('')
    try {
      const result = await api<{ checks: CheckCommandView[] }>(endpoint, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('save-repository-check') },
        body: JSON.stringify({
          checks: [{
            id: draft.id,
            name: draft.name.trim(),
            version: draft.version,
            command: draft.command.trim(),
            workingDirectory: draft.workingDirectory || '.',
            source: 'user',
          }],
        }),
      })
      setChecks(result.checks ?? [])
      setDraft(undefined)
      setChoosingDirectory(false)
    } catch (reason) {
      setError(message(reason))
    } finally {
      setSaving(false)
    }
  }

  if (!open) {
    return <ActionButton type="button" className="quiet-action" disabled={disabled} disabledReason={'Wait for the current operation to finish.'} onClick={show}>{t('Manage checks')}</ActionButton>
  }

  return <div className="panel repository-checks" role="dialog" aria-label={t('Named checks for {repository}', { repository: repository.name })}>
    <div className="title-actions">
      <strong>{t('Named checks')}</strong>
      <ActionButton type="button" className="quiet-action" disabled={saving} disabledReason={'Saving changes. Please wait.'} onClick={close}>{t('Close')}</ActionButton>
    </div>
    <p className="entry-help">{t('Named checks are available to new tasks. Existing tasks keep their frozen check selection.')}</p>

    {loading ? <p role="status">{t('Loading named checks…')}</p> : <>
      {checks.length === 0 ? <p>{t('No named checks yet.')}</p> : <ul>
        {checks.map((check) => <li key={`${check.id}:${check.version}`}>
          <strong>{check.name}</strong>{' '}
          <code>{check.command}</code>{' '}
          <small>{t('Working directory: {directory}', { directory: check.workingDirectory || '.' })} · v{check.version}</small>{' '}
          <ActionButton type="button" className="quiet-action" disabled={saving || disabled} disabledReason={saving ? 'Saving changes. Please wait.' : 'Wait for the current operation to finish.'} aria-label={`Edit ${check.name}`} onClick={() => edit(check)}>{t('Edit')}</ActionButton>
        </li>)}
      </ul>}
      {!draft && <ActionButton type="button" className="btn-secondary" disabled={saving || disabled} disabledReason={saving ? 'Saving changes. Please wait.' : 'Wait for the current operation to finish.'} onClick={() => { setDraft(newDraft()); setError('') }}>{t('Add named check')}</ActionButton>}
    </>}

    {draft && <form className="inline-edit repository-check-form" onSubmit={(event) => { event.preventDefault(); void save() }}>
      <label>{t('Check name')}<input autoFocus required maxLength={120} disabled={saving || disabled} value={draft.name} onChange={(event) => setDraft((current) => current && { ...current, name: event.target.value })} /></label>
      <label>{t('Command')}<input required maxLength={4096} disabled={saving || disabled} value={draft.command} onChange={(event) => setDraft((current) => current && { ...current, command: event.target.value })} /></label>
      <p className="entry-help">{t('Working directory: {directory}', { directory: draft.workingDirectory || '.' })}</p>
      <ActionButton type="button" className="btn-secondary" disabled={saving || disabled} disabledReason={saving ? 'Saving changes. Please wait.' : 'Wait for the current operation to finish.'} onClick={() => setChoosingDirectory(true)}>{t('Choose check directory…')}</ActionButton>
      <ActionButton type="button" className="btn-secondary" disabled={saving} disabledReason={'Saving changes. Please wait.'} onClick={() => { setDraft(undefined); setChoosingDirectory(false); setError('') }}>{t('Cancel')}</ActionButton>
      <ActionButton type="submit" className="btn-primary" disabled={saving || disabled || !draft.name.trim() || !draft.command.trim()} disabledReason={saving ? 'Saving changes. Please wait.' : disabled ? 'Wait for the current operation to finish.' : !draft.name.trim() ? 'Enter a check name.' : 'Enter the check command.'}>{saving ? t('Saving…') : t('Save check')}</ActionButton>
    </form>}

    {choosingDirectory && draft && <RepositoryDirectoryPicker
      projectId={projectId}
      repoId={repository.repoId}
      disabled={saving || disabled}
      onSelect={(directory) => { setDraft((current) => current && { ...current, workingDirectory: directory }); setChoosingDirectory(false) }}
      onClose={() => setChoosingDirectory(false)}
    />}
    {error && <p className="error-banner" role="alert">{error}</p>}
  </div>
}
