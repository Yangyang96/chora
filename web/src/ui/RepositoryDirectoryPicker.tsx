import { ActionButton } from './ActionButton'
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, message } from '../api'
import { useI18n } from '../i18n'
import type { RepositoryEntriesView } from '../taskFirstTypes'

type RepositoryDirectoryPickerProps = {
  projectId: string
  repoId: string
  disabled?: boolean
  onSelect: (directory: string) => void
  onClose: () => void
}

export function RepositoryDirectoryPicker({ projectId, repoId, disabled = false, onSelect, onClose }: RepositoryDirectoryPickerProps) {
  const { t } = useI18n()
  const [path, setPath] = useState('.')
  const [searchText, setSearchText] = useState('')
  const [activeQuery, setActiveQuery] = useState('')
  const [entries, setEntries] = useState<RepositoryEntriesView['entries']>([])
  const [nextCursor, setNextCursor] = useState('')
  const [truncated, setTruncated] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | undefined>(undefined)

  const load = useCallback(async (directory: string, cursor = '', filter = '') => {
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    setLoading(true)
    setError('')
    if (!cursor) { setEntries([]); setNextCursor(''); setTruncated(false) }
    try {
      const query = new URLSearchParams({ path: directory, limit: '200' })
      if (filter) query.set('query', filter)
      if (cursor) query.set('cursor', cursor)
      const result = await api<RepositoryEntriesView>(`/api/v2/projects/${encodeURIComponent(projectId)}/repositories/${encodeURIComponent(repoId)}/entries?${query}`, { signal: request.signal })
      if (request.signal.aborted) return
      setEntries((current) => cursor ? [...current, ...(result.entries ?? [])] : result.entries ?? [])
      setNextCursor(result.nextCursor ?? '')
      setTruncated(Boolean(result.truncated))
    } catch (reason) {
      if (!request.signal.aborted) setError(message(reason))
    } finally {
      if (!request.signal.aborted) setLoading(false)
    }
  }, [projectId, repoId])

  useEffect(() => {
    setPath('.')
    setSearchText('')
    setActiveQuery('')
    void load('.')
    return () => controller.current?.abort()
  }, [load])

  function browse(directory: string) {
    setPath(directory)
    setSearchText('')
    setActiveQuery('')
    void load(directory)
  }

  function search() {
    setActiveQuery(searchText)
    void load(path, '', searchText)
  }

  function close() {
    controller.current?.abort()
    onClose()
  }

  const parent = path === '.' ? '' : path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '.'
  const directories = entries.filter((entry) => entry.kind === 'directory')

  return <div className="panel repository-directory-picker" role="dialog" aria-label={t('Choose repository directory')}>
    <div className="title-actions">
      <strong>{t('Choose repository directory')}</strong>
      <ActionButton type="button" className="quiet-action" disabled={disabled} disabledReason={'Wait for the current operation to finish.'} onClick={close}>{t('Close')}</ActionButton>
    </div>
    <p className="entry-help"><code>{path}</code></p>
    <label>{t('Search child directories')}<input type="search" maxLength={256} disabled={disabled} value={searchText} onChange={(event) => setSearchText(event.target.value)} onKeyDown={(event) => {
      if (event.key === 'Enter') { event.preventDefault(); search() }
    }} /></label>
    <ActionButton type="button" className="btn-secondary" disabled={disabled} disabledReason={'Wait for the current operation to finish.'} onClick={search}>{t('Search directories')}</ActionButton>
    {activeQuery && <ActionButton type="button" className="quiet-action" disabled={disabled} disabledReason={'Wait for the current operation to finish.'} onClick={() => browse(path)}>{t('Clear directory search')}</ActionButton>}
    <div className="form-actions">
      <ActionButton type="button" className="btn-secondary" disabled={disabled || loading} disabledReason={disabled ? 'Wait for the current operation to finish.' : 'Wait for the directory list to finish loading.'} onClick={() => onSelect('.')}>{t('Use repository root')}</ActionButton>
      {path !== '.' && <ActionButton type="button" className="btn-secondary" disabled={disabled || loading} disabledReason={disabled ? 'Wait for the current operation to finish.' : 'Wait for the directory list to finish loading.'} onClick={() => onSelect(path)}>{t('Use this directory')}</ActionButton>}
      {parent && <ActionButton type="button" className="quiet-action" disabled={disabled || loading} disabledReason={disabled ? 'Wait for the current operation to finish.' : 'Wait for the directory list to finish loading.'} onClick={() => browse(parent)}>{t('Up one level')}</ActionButton>}
    </div>
    {loading && entries.length === 0 ? <p role="status">{t('Loading directories…')}</p> : directories.length === 0 ? <p>{t('No child directories.')}</p> : <ul>
      {directories.map((entry) => <li key={entry.path}>
        <ActionButton type="button" className="quiet-action" disabled={disabled || loading} disabledReason={disabled ? 'Wait for the current operation to finish.' : 'Wait for the directory list to finish loading.'} onClick={() => browse(entry.path)}>{entry.name}</ActionButton>
      </li>)}
    </ul>}
    {error && <p className="error-banner" role="alert">{error}</p>}
    {(nextCursor || truncated) && <p className="entry-help" role="status">{truncated ? t('Directory results are truncated.') : t('More directories are available.')}</p>}
    {nextCursor && <ActionButton type="button" className="btn-secondary" disabled={disabled || loading} disabledReason={disabled ? 'Wait for the current operation to finish.' : 'Wait for the directory list to finish loading.'} onClick={() => void load(path, nextCursor, activeQuery)}>{loading ? t('Loading…') : t('Load more')}</ActionButton>}
  </div>
}
