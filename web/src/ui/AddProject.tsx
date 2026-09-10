import { ActionButton } from './ActionButton'
import { useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import type { ProjectView } from '../types'

type AddProjectProps = {
  onAdded: (project: ProjectView) => void
  onCancel: () => void
}

export function AddProject({ onAdded, onCancel }: AddProjectProps) {
  const { t } = useI18n()
  const [busy, setBusy] = useState<'choosing' | 'creating-empty' | 'creating-repository' | null>(null)
  const [error, setError] = useState('')
  const [locator, setLocator] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')

  async function chooseProject() {
    setBusy('choosing')
    setError('')
    try {
      const selection = await api<{ cancelled: boolean; locator: string }>('/api/projects/choose-directory', {
        method: 'POST', body: '{}',
      })
      if (selection.cancelled) return
      if (!selection.locator) throw new Error(t('No folder was selected. Please try again.'))
      setLocator(selection.locator)
      if (!name.trim()) setName(selection.locator.split('/').filter(Boolean).at(-1)?.slice(0, 120) ?? '')
    } catch (reason) {
      setError(message(reason))
    } finally {
      setBusy(null)
    }
  }

  async function createEmptyProject() {
    if (busy || !name.trim()) return
    setBusy('creating-empty')
    setError('')
    try {
      const project = await api<ProjectView>('/api/v2/projects', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('create-empty-project') },
        body: JSON.stringify({ name: name.trim(), description: description.trim() }),
      })
      onAdded(project)
    } catch (reason) {
      setError(message(reason))
    } finally {
      setBusy(null)
    }
  }

  async function createProjectFromRepository() {
    if (busy || !locator || !name.trim()) return
    setBusy('creating-repository')
    setError('')
    try {
      const project = await api<ProjectView>('/api/projects', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('open-project') },
        body: JSON.stringify({ locator, name: name.trim() }),
      })
      onAdded(project)
    } catch (reason) {
      setError(message(reason))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="project-entry">
      <div className="page-heading">
        <span className="page-eyebrow">{t('LOCAL WORKSPACE')}</span>
        <h1>{t('Create a Project')}</h1>
        <p>{t('Create a collaboration space now, then add one or more local Git repositories when you need them.')}</p>
      </div>
      <form className="panel project-entry-form" onSubmit={(event) => { event.preventDefault(); void createEmptyProject() }}>
        <label>
          {t('Project name')}
          <input autoFocus required maxLength={120} value={name} disabled={busy !== null} onChange={(event) => setName(event.target.value)} />
        </label>
        <label>
          {t('Project description')}
          <textarea maxLength={1000} value={description} disabled={busy !== null} onChange={(event) => setDescription(event.target.value)} />
        </label>
        <p className="entry-help">{t('You can start with an empty Project or choose a local Git repository as a shortcut.')}</p>
        {locator && <p className="entry-help project-selected-repository">{t('Selected repository')}: <code>{locator}</code></p>}
        {error && <p className="error-banner" role="alert">{error}</p>}
        <div className="form-actions">
          <ActionButton type="button" className="btn-secondary" disabled={busy !== null} disabledReason={busy === 'choosing' ? 'Finish or cancel the folder chooser first.' : 'Creating the Project. Please wait.'} onClick={onCancel}>{t('Cancel')}</ActionButton>
          <ActionButton type="button" className="btn-secondary" disabled={busy !== null} disabledReason={busy === 'choosing' ? 'Finish or cancel the folder chooser first.' : 'Creating the Project. Please wait.'} onClick={() => void chooseProject()}>{busy === 'choosing' ? t('Waiting for folder selection…') : t('Choose repository folder…')}</ActionButton>
          {locator && <ActionButton type="button" className="btn-secondary" disabled={busy !== null || !name.trim()} disabledReason={busy !== null ? (busy === 'choosing' ? 'Finish or cancel the folder chooser first.' : 'Creating the Project. Please wait.') : 'Enter a Project name.'} onClick={() => void createProjectFromRepository()}>{busy === 'creating-repository' ? t('Creating project…') : t('Create with repository')}</ActionButton>}
          <ActionButton type="submit" className="btn-primary" disabled={busy !== null || !name.trim()} disabledReason={busy !== null ? (busy === 'choosing' ? 'Finish or cancel the folder chooser first.' : 'Creating the Project. Please wait.') : 'Enter a Project name.'}>{busy === 'creating-empty' ? t('Creating project…') : t('Create empty Project')}</ActionButton>
        </div>
      </form>
      <section className="local-workflow" aria-label={t('How local work happens')}>
        <h2>{t('Your code, your final decision')}</h2>
        <ol>
          <li><span>01</span><div><strong>{t('Describe a task')}</strong><p>{t('Choose the repositories that this task may use.')}</p></div></li>
          <li><span>02</span><div><strong>{t('Review the changes')}</strong><p>{t('Inspect each repository’s code diff and checks before deciding what to keep.')}</p></div></li>
          <li><span>03</span><div><strong>{t('Apply to your local project')}</strong><p>{t('Apply writes the reviewed changes to the selected local repositories. Commit and push remain yours.')}</p></div></li>
        </ol>
        <p className="local-boundary-note">{t('Local Connected runs Pi on this computer without a sandbox. You acknowledge this before starting a task.')}</p>
      </section>
    </div>
  )
}
