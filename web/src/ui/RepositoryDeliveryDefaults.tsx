import { ActionButton } from './ActionButton'
import { useEffect, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import type { RepositoryBranchPageView, RepositoryDeliveryDefaultsView } from '../taskFirstTypes'

export function RepositoryDeliveryDefaults({ repoId, disabled = false }: { repoId: string; disabled?: boolean }) {
  const { t } = useI18n()
  const [value, setValue] = useState<RepositoryDeliveryDefaultsView>()
  const [branches, setBranches] = useState<RepositoryBranchPageView>()
  const [selected, setSelected] = useState('')
  const [editing, setEditing] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    setValue(undefined); setBranches(undefined); setSelected(''); setEditing(false); setError('')
  }, [repoId])

  async function edit() {
    setEditing(true); setBusy(true); setError('')
    try {
      const current = value ?? await api<RepositoryDeliveryDefaultsView>(`/api/v2/repositories/${encodeURIComponent(repoId)}/delivery-defaults`)
      setValue(current); setSelected(current.targetRef || current.suggestedTargetRef || '')
      const page = await api<RepositoryBranchPageView>(`/api/v2/repositories/${encodeURIComponent(repoId)}/branches?limit=100`)
      setBranches(page)
    } catch (reason) { setError(message(reason)) }
    finally { setBusy(false) }
  }

  async function save() {
    if (!value || !selected) return
    setBusy(true); setError('')
    try {
      const next = await api<RepositoryDeliveryDefaultsView>(`/api/v2/repositories/${encodeURIComponent(repoId)}/delivery-defaults`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('save-delivery-default') },
        body: JSON.stringify({ expectedVersion: value.version, targetRef: selected }),
      })
      setValue(next); setSelected(next.targetRef); setEditing(false)
    } catch (reason) { setError(message(reason)) }
    finally { setBusy(false) }
  }

  if (!value && !editing && !error) return <ActionButton type="button" className="quiet-action" disabled={disabled} disabledReason={'Wait for the current operation to finish.'} onClick={() => void edit()}>{t('Configure delivery default')}</ActionButton>
  return <div className="repository-delivery-default">
    {value && !editing && <>
      <span>{t('Default target branch')} · <code>{(value.targetRef || value.suggestedTargetRef || t('Not configured')).replace(/^refs\/heads\//, '')}</code>{!value.targetRef && value.suggestedTargetRef ? ` · ${t('Suggested, confirmation required')}` : ''}</span>
      {value.reason && !value.targetRef && <small>{value.reason}</small>}
      <ActionButton type="button" className="quiet-action" disabled={disabled} disabledReason={'Wait for the current operation to finish.'} onClick={() => void edit()}>{t(value.targetRef ? 'Change delivery default' : 'Confirm delivery default')}</ActionButton>
    </>}
    {value && editing && <div className="panel">
      <label>{t('Default target branch')}<select value={selected} disabled={busy} onChange={(event) => setSelected(event.target.value)}>
        {!selected && <option value="">{t('Choose a target branch')}</option>}
        {selected && !(branches?.branches ?? []).some((branch) => branch.ref === selected) && <option value={selected}>{selected.replace(/^refs\/heads\//, '')}</option>}
        {(branches?.branches ?? []).map((branch) => <option key={branch.ref} value={branch.ref}>{branch.ref.replace(/^refs\/heads\//, '')} · {branch.commit.slice(0, 12)}</option>)}
      </select></label>
      <p className="section-note">{t('Saving changes the default for future tasks. Existing tasks and task-specific choices stay unchanged.')}</p>
      <div className="form-actions"><ActionButton type="button" className="btn-secondary" disabled={busy} disabledReason={'Loading or saving the delivery default. Please wait.'} onClick={() => { setEditing(false); setSelected(value.targetRef || value.suggestedTargetRef || '') }}>{t('Cancel')}</ActionButton><ActionButton type="button" className="btn-primary" disabled={busy || !selected} disabledReason={busy ? 'Loading or saving the delivery default. Please wait.' : 'Choose a default target branch.'} onClick={() => void save()}>{busy ? t('Saving…') : t('Save delivery default')}</ActionButton></div>
    </div>}
    {!value && editing && busy && <span role="status">{t('Loading delivery default…')}</span>}
    {error && <p role="alert" className="error-banner">{error}</p>}
  </div>
}
