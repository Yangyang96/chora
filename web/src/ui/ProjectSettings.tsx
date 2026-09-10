import { ActionButton } from './ActionButton'
import { useEffect, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'

export type ProjectSettingsValue = {
  version: number
  writableFiles: string[]
  writableDirectories: string[]
  verificationCommands: { argv: string[]; workingDirectory?: string }[]
  noChecks: boolean
}

export function ProjectSettings({ projectId, readOnly = false, onReady }: { projectId: string; readOnly?: boolean; onReady?: (version: number | undefined) => void }) {
  const { t } = useI18n()
  const [value, setValue] = useState<ProjectSettingsValue>()
  const [files, setFiles] = useState('')
  const [directories, setDirectories] = useState('')
  const [commands, setCommands] = useState('')
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const endpoint = `/api/projects/${encodeURIComponent(projectId)}/settings`
  function adopt(next: ProjectSettingsValue) {
    setValue(next)
    setFiles((next.writableFiles ?? []).join('\n'))
    setDirectories((next.writableDirectories ?? []).join('\n'))
    setCommands(JSON.stringify(next.verificationCommands ?? [], null, 2))
  }
  useEffect(() => {
    let active = true
    setValue(undefined); setError(''); setSaved(false); onReady?.(undefined)
    api<ProjectSettingsValue>(endpoint).then((next) => { if (active) { adopt(next); onReady?.(next.version) } }).catch((reason) => { if (active) setError(message(reason)) })
    return () => { active = false }
  }, [endpoint, onReady])
  async function save() {
    if (!value) return
    setBusy(true); setError(''); setSaved(false)
    try {
      const parsed: unknown = value.noChecks ? [] : JSON.parse(commands)
      if (!Array.isArray(parsed) || parsed.some((item) => !item || !Array.isArray(item.argv) || item.argv.some((arg: unknown) => typeof arg !== 'string'))) throw new Error(t('Checks must be a JSON array of commands with argv and workingDirectory.'))
      const lines = (text: string) => text.split('\n').map((line) => line.trim()).filter(Boolean)
      const next = await api<ProjectSettingsValue>(endpoint, { method: 'PUT', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('project-settings') }, body: JSON.stringify({ version: value.version, writableFiles: lines(files), writableDirectories: lines(directories), verificationCommands: parsed, noChecks: value.noChecks }) })
      adopt(next); setSaved(true)
    } catch (reason) { setError(message(reason)) } finally { setBusy(false) }
  }
  return <section className="panel project-settings" aria-label={t('Task scope and checks')}>
    <h2>{t('Task scope and checks')}</h2>
    <p className="section-note">{t('These Project defaults are frozen when a task is created. Changes affect new tasks in every Room; existing tasks keep their original settings.')}</p>
    {error && <p role="alert" className="error-banner">{error}</p>}
    {!value && !error && <p role="status">{t('Loading…')}</p>}
    {value && (readOnly ? <>
      <p>{t('Writable files')}: <code>{value.writableFiles?.join(', ') || '—'}</code></p>
      <p>{t('Writable directories')}: <code>{value.writableDirectories?.join(', ') || '—'}</code></p>
      <p>{value.noChecks ? t('No checks · Not run / Unverified') : t('Checks to run')}</p>
      {!value.noChecks && <pre>{(value.verificationCommands ?? []).map((command) => `${command.workingDirectory || '.'}: ${JSON.stringify(command.argv)}`).join('\n')}</pre>}
    </> : <>
      <label>{t('Writable files')}<textarea value={files} onChange={(event) => setFiles(event.target.value)} placeholder="README.md" /></label>
      <label>{t('Writable directories')}<textarea value={directories} onChange={(event) => setDirectories(event.target.value)} placeholder={'src\ntests\ndocs'} /></label>
      <p className="section-note">{t('One repository-relative path per line. Directories permit new files within that scope. Repository root, .git and symlinks are not supported. This scope is not a Sandbox.')}</p>
      <label><input type="checkbox" checked={value.noChecks} onChange={(event) => setValue({ ...value, noChecks: event.target.checked })} />{t('Explicitly run no checks (Unverified)')}</label>
      {!value.noChecks && <label>{t('Checks to run')}<textarea value={commands} onChange={(event) => setCommands(event.target.value)} spellCheck={false} /></label>}
      <p className="section-note">{t('Declare each command as argv and a repository-relative workingDirectory. Dependencies are not copied or installed automatically; include any required setup command explicitly.')}</p>
      <ActionButton type="button" className="btn-primary" disabled={busy} disabledReason={'Saving changes. Please wait.'} onClick={() => void save()}>{busy ? t('Saving…') : t('Save settings')}</ActionButton>
      {saved && <p role="status">{t('Settings saved. Existing tasks are unchanged.')}</p>}
    </>)}
  </section>
}
