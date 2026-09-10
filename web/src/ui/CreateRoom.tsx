import { ActionButton } from './ActionButton'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { useI18n } from '../i18n'

type CreateRoomProps = {
  busy: boolean
  onCancel: () => void
  onCreate: (name: string, description: string) => void
}

export function CreateRoom({ busy, onCancel, onCreate }: CreateRoomProps) {
  const { t } = useI18n()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')

  function submit(event: FormEvent) {
    event.preventDefault()
    if (!name.trim()) return
    onCreate(name.trim(), description.trim())
  }

  return (
    <form className="panel" onSubmit={submit}>
      <h2>{t('Create Room')}</h2>
      <label>
        {t('Room name')}
        <input value={name} onChange={(event) => setName(event.target.value)} placeholder="e.g. my-project" autoFocus required />
      </label>
      <label>
        {t('Description')}
        <input value={description} onChange={(event) => setDescription(event.target.value)} placeholder={t('What is this Room for?')} />
      </label>
      <div className="form-actions">
        <button type="button" className="btn-secondary" onClick={onCancel}>
          {t('Cancel')}
        </button>
        <ActionButton type="submit" className="btn-primary" disabled={busy || !name.trim()} disabledReason={busy ? 'Wait for the current operation to finish.' : 'Enter a Room name.'}>
          {busy ? t('Creating…') : t('Create Room')}
        </ActionButton>
      </div>
    </form>
  )
}
