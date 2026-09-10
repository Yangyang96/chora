import { ActionButton } from './ActionButton'
import { useEffect, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import type { RepositoryPageView, RepositoryResourceView, RoomResourcesView } from '../taskFirstTypes'
import type { ProjectRoomView, ProjectView } from '../types'
import { RepositoryChecks } from './RepositoryChecks'
import { RepositoryDeliveryDefaults } from './RepositoryDeliveryDefaults'

type ProjectHomeProps = {
  project: ProjectView
  onChange: (project: ProjectView) => void
  onOpenRoom: (room: ProjectRoomView) => void
  onResume?: (url: string) => void
}

type DisplayRepository = RepositoryResourceView & { legacy?: boolean }

function projectRepositories(project: ProjectView): DisplayRepository[] {
  if (project.repositories) return project.repositories
  const binding = project.repositoryBinding
  if (!binding) return []
  return [{
    repoId: '', name: binding.name, localLocator: binding.localLocator, state: binding.state,
    version: binding.version, availability: binding.state === 'active' ? 'legacy_unverified' : 'unavailable',
    branch: '', head: binding.admittedBase, dirty: binding.dirtyAdmitted, legacy: true,
  }]
}

function shortRevision(value: string) {
  return value ? value.slice(0, 7) : '—'
}

function mergeRepositories(first: DisplayRepository[], second: RepositoryResourceView[]) {
  const repositories = [...first]
  const seen = new Set(first.map((repository) => repository.repoId))
  for (const repository of second) {
    if (!seen.has(repository.repoId)) {
      seen.add(repository.repoId)
      repositories.push(repository)
    }
  }
  return repositories
}

function RoomResourceEditor({ room, repositories }: { room: ProjectRoomView; repositories: RepositoryResourceView[] }) {
  const { t } = useI18n()
  const [editing, setEditing] = useState(false)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [version, setVersion] = useState(0)
  const [selected, setSelected] = useState<string[]>([])
  const [error, setError] = useState('')

  async function open() {
    setEditing(true); setLoading(true); setError('')
    try {
      const current = await api<RoomResourcesView>(`/api/v2/rooms/${encodeURIComponent(room.id)}/resources`)
      setVersion(current.version)
      setSelected(current.repoIds ?? [])
    } catch (reason) {
      setError(message(reason))
    } finally {
      setLoading(false)
    }
  }

  async function save() {
    setSaving(true); setError('')
    try {
      const updated = await api<RoomResourcesView>(`/api/v2/rooms/${encodeURIComponent(room.id)}/resources`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('save-room-resources') },
        body: JSON.stringify({ version, repoIds: selected }),
      })
      setVersion(updated.version)
      setSelected(updated.repoIds ?? [])
      setEditing(false)
    } catch (reason) {
      setError(message(reason))
    } finally {
      setSaving(false)
    }
  }

  if (!editing) return <button type="button" className="quiet-action" onClick={() => void open()}>{t('Choose common repositories')}</button>

  return <div className="panel room-resource-editor">
    <strong>{t('Common repositories')}</strong>
    <p className="entry-help">{t('These are visible defaults for new tasks in this Room. Each task confirms its own repository selection.')}</p>
    {loading ? <p>{t('Loading repositories…')}</p> : repositories.length === 0 ? <p>{t('No repositories have been added to this Project.')}</p> : repositories.map((repository) => <label key={repository.repoId}>
      <input
        type="checkbox"
        checked={selected.includes(repository.repoId)}
        disabled={repository.state !== 'active' || saving}
        onChange={(event) => setSelected((current) => event.target.checked ? [...current, repository.repoId] : current.filter((id) => id !== repository.repoId))}
      />
      {repository.name} <small>{repository.localLocator}</small>
    </label>)}
    {!loading && selected.length === 0 && <p className="entry-help">{t('No common repositories selected.')}</p>}
    {error && <p className="error-banner" role="alert">{error}</p>}
    <div className="form-actions">
      <ActionButton type="button" className="btn-secondary" disabled={saving} disabledReason={'Saving changes. Please wait.'} onClick={() => setEditing(false)}>{t('Cancel')}</ActionButton>
      <ActionButton type="button" className="btn-primary" disabled={loading || saving || Boolean(error)} disabledReason={loading ? 'Loading data. Please wait.' : saving ? 'Saving changes. Please wait.' : error} onClick={() => void save()}>{saving ? t('Saving…') : t('Save repository defaults')}</ActionButton>
    </div>
  </div>
}

export function ProjectHome({ project, onChange, onOpenRoom, onResume }: ProjectHomeProps) {
  const { t } = useI18n()
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [editingProject, setEditingProject] = useState(false)
  const [projectName, setProjectName] = useState(project.name)
  const [creatingRoom, setCreatingRoom] = useState(false)
  const [roomName, setRoomName] = useState('')
  const [roomDescription, setRoomDescription] = useState('')
  const [editingRoomId, setEditingRoomId] = useState('')
  const [editRoomName, setEditRoomName] = useState('')
  const [editRoomDescription, setEditRoomDescription] = useState('')
  const [repositoryPages, setRepositoryPages] = useState<RepositoryResourceView[]>([])
  const [repositoryCursor, setRepositoryCursor] = useState(project.nextRepositoryCursor ?? '')
  const [repositoryPageLoading, setRepositoryPageLoading] = useState(false)
  const repositories = mergeRepositories(projectRepositories(project), repositoryPages)
  const v2Repositories = repositories.filter((repository): repository is RepositoryResourceView => !repository.legacy)

  useEffect(() => {
    setRepositoryPages([])
    setRepositoryCursor(project.nextRepositoryCursor ?? '')
    setRepositoryPageLoading(false)
  }, [project.id, project.repositories, project.nextRepositoryCursor])

  async function refreshProject() {
    const updated = await api<ProjectView>(`/api/v2/projects/${encodeURIComponent(project.id)}`)
    onChange(updated)
  }

  async function addRepository() {
    setBusy('repository-add'); setError('')
    try {
      const selected = await api<{ cancelled: boolean; repository?: RepositoryResourceView }>(`/api/v2/projects/${encodeURIComponent(project.id)}/repositories/choose-directory`, {
        method: 'POST', body: '{}',
      })
      if (!selected.cancelled) await refreshProject()
    } catch (reason) { setError(message(reason)) } finally { setBusy('') }
  }

  async function loadMoreRepositories() {
    if (!repositoryCursor || repositoryPageLoading) return
    setRepositoryPageLoading(true); setError('')
    try {
      const page = await api<RepositoryPageView>(`/api/v2/projects/${encodeURIComponent(project.id)}/repositories?cursor=${encodeURIComponent(repositoryCursor)}&limit=200`)
      setRepositoryPages((current) => mergeRepositories(current, page.repositories))
      setRepositoryCursor(page.nextCursor ?? '')
    } catch (reason) { setError(message(reason)) } finally { setRepositoryPageLoading(false) }
  }

  async function removeRepository(repository: RepositoryResourceView) {
    setBusy(`repository-remove-${repository.repoId}`); setError('')
    try {
      await api<unknown>(`/api/v2/projects/${encodeURIComponent(project.id)}/repositories/${encodeURIComponent(repository.repoId)}?expectedVersion=${repository.version}`, {
        method: 'DELETE', headers: { 'Idempotency-Key': commandKey('remove-project-repository') },
      })
      await refreshProject()
    } catch (reason) { setError(message(reason)) } finally { setBusy('') }
  }

  async function renameProject() {
    if (!projectName.trim()) return
    setBusy('project-rename'); setError('')
    try {
      const updated = await api<ProjectView>(`/api/v2/projects/${encodeURIComponent(project.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('rename-project') },
        body: JSON.stringify({ name: projectName.trim(), expectedVersion: project.version }),
      })
      onChange(updated); setEditingProject(false); setProjectName(updated.name)
    } catch (reason) { setError(message(reason)) } finally { setBusy('') }
  }

  async function changeProjectState(action: 'archive' | 'restore') {
    setBusy(`project-${action}`); setError('')
    try {
      const updated = await api<ProjectView>(`/api/v2/projects/${encodeURIComponent(project.id)}/${action}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey(`${action}-project`) },
        body: JSON.stringify({ expectedVersion: project.version }),
      })
      onChange(updated)
    } catch (reason) { setError(message(reason)) } finally { setBusy('') }
  }

  async function createRoom() {
    if (!roomName.trim()) return
    setBusy('room-create'); setError('')
    try {
      const created = await api<ProjectRoomView>(`/api/projects/${encodeURIComponent(project.id)}/rooms`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('create-project-room') },
        body: JSON.stringify({ name: roomName.trim(), description: roomDescription.trim() }),
      })
      onChange({ ...project, rooms: [...project.rooms, created] })
      setCreatingRoom(false); setRoomName(''); setRoomDescription('')
      onOpenRoom(created)
    } catch (reason) { setError(message(reason)) } finally { setBusy('') }
  }

  async function renameRoom(room: ProjectRoomView) {
    if (!editRoomName.trim()) return
    setBusy(`room-rename-${room.id}`); setError('')
    try {
      const updated = await api<ProjectRoomView>(`/api/rooms/${encodeURIComponent(room.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('rename-project-room') },
        body: JSON.stringify({ name: editRoomName.trim(), description: editRoomDescription.trim(), expectedVersion: room.version }),
      })
      onChange({ ...project, rooms: project.rooms.map((item) => item.id === updated.id ? updated : item), room: project.room.id === updated.id ? updated : project.room })
      setEditingRoomId('')
    } catch (reason) { setError(message(reason)) } finally { setBusy('') }
  }

  async function changeRoomState(room: ProjectRoomView, action: 'archive' | 'restore') {
    setBusy(`${action}-${room.id}`); setError('')
    try {
      const updated = await api<ProjectRoomView>(`/api/rooms/${encodeURIComponent(room.id)}/${action}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey(`${action}-room`) },
        body: JSON.stringify({ expectedVersion: room.version }),
      })
      onChange({ ...project, rooms: project.rooms.map((item) => item.id === updated.id ? updated : item), room: project.room.id === updated.id ? updated : project.room })
    } catch (reason) { setError(message(reason)) } finally { setBusy('') }
  }

  const activeRooms = project.rooms.filter((room) => room.state === 'active')
  const archivedRooms = project.rooms.filter((room) => room.state === 'archived')

  return <div className="project-home">
    <header className="project-home-head">
      <div>
        <span className="page-eyebrow">{t('PROJECT')}</span>
        {editingProject ? <form className="inline-edit" onSubmit={(event) => { event.preventDefault(); void renameProject() }}>
          <label>{t('Project name')}<input autoFocus maxLength={120} value={projectName} onChange={(event) => setProjectName(event.target.value)} /></label>
          <ActionButton className="btn-primary" disabled={busy !== '' || !projectName.trim()} disabledReason={busy !== '' ? 'Wait for the current operation to finish.' : 'Enter a Project name.'}>{t('Save')}</ActionButton>
          <button type="button" className="btn-secondary" onClick={() => { setEditingProject(false); setProjectName(project.name) }}>{t('Cancel')}</button>
        </form> : <div className="title-actions"><h1>{project.name}</h1>{project.state === 'active' && <button type="button" className="quiet-action" onClick={() => setEditingProject(true)}>{t('Rename project')}</button>}</div>}
        {project.description && <p>{project.description}</p>}
        <p>{t('{count} repositories · {rooms} Rooms', { count: repositories.length, rooms: project.rooms.length })}</p>
        {project.state === 'archived' && <p className="warning-banner" role="status">{t('This Project is archived. Rooms and history remain readable; restore the Project to start new work.')}</p>}
      </div>
      <div className="project-home-actions">
        {project.state === 'active' ? <>
          <ActionButton type="button" className="btn-primary" disabled={busy !== ''} disabledReason={'Wait for the current operation to finish.'} onClick={() => setCreatingRoom(true)}>{t('＋ New topic Room')}</ActionButton>
          <ActionButton type="button" className="btn-secondary" disabled={busy !== ''} disabledReason={'Wait for the current operation to finish.'} onClick={() => void changeProjectState('archive')}>{t('Archive Project')}</ActionButton>
        </> : <ActionButton type="button" className="btn-primary" disabled={busy !== ''} disabledReason={'Wait for the current operation to finish.'} onClick={() => void changeProjectState('restore')}>{t('Restore Project')}</ActionButton>}
      </div>
    </header>

    {error && <p className="error-banner" role="alert">{error}</p>}

    <section className="project-rooms" aria-label={t('Repositories')}>
      <div className="title-actions"><h2>{t('Repositories')}</h2>{project.state === 'active' && <ActionButton type="button" className="btn-secondary" disabled={busy !== ''} disabledReason={'Wait for the current operation to finish.'} onClick={() => void addRepository()}>{busy === 'repository-add' ? t('Waiting for folder selection…') : t('＋ Add repository')}</ActionButton>}</div>
      {repositories.length === 0 ? <div className="empty"><p>{t('No repositories yet. Add a local Git repository before starting a coding task.')}</p></div> : repositories.map((repository) => <article className={`project-room-card ${repository.state === 'removed' ? 'archived' : ''}`} key={repository.repoId || `legacy:${repository.localLocator}`}>
        <div className="project-room-main">
          <strong>{repository.name}</strong>
          <span><code>{repository.localLocator}</code></span>
          <small>{repository.branch || t('Branch unavailable')} · {shortRevision(repository.head)} · {repository.dirty ? t('Uncommitted changes') : t('Clean')} · {t(`Repository availability: ${repository.availability}`)}</small>
          {repository.reason && <small>{repository.reason}</small>}
        </div>
        {!repository.legacy && repository.state === 'active' && project.state === 'active' && <div className="project-room-actions">
          <RepositoryDeliveryDefaults repoId={repository.repoId} disabled={busy !== ''} />
          <RepositoryChecks projectId={project.id} repository={repository} disabled={busy !== ''} />
          <ActionButton type="button" className="quiet-action" disabled={busy !== ''} disabledReason={'Wait for the current operation to finish.'} onClick={() => void removeRepository(repository)}>{busy === `repository-remove-${repository.repoId}` ? t('Removing…') : t('Remove repository')}</ActionButton>
        </div>}
      </article>)}
      {repositoryCursor && <ActionButton type="button" className="btn-secondary" disabled={repositoryPageLoading || busy !== ''} disabledReason={repositoryPageLoading ? 'Loading repositories…' : 'Wait for the current operation to finish.'} onClick={() => void loadMoreRepositories()}>{repositoryPageLoading ? t('Loading repositories…') : t('Load more repositories')}</ActionButton>}
    </section>

    {creatingRoom && <form className="panel topic-room-form" onSubmit={(event) => { event.preventDefault(); void createRoom() }}>
      <h2>{t('Create a topic Room')}</h2>
      <label>{t('Room name')}<input autoFocus required maxLength={120} value={roomName} onChange={(event) => setRoomName(event.target.value)} /></label>
      <label>{t('Room Brief')}<textarea value={roomDescription} onChange={(event) => setRoomDescription(event.target.value)} /></label>
      <p className="entry-help">{t('After creating the Room, choose any common repositories you want to suggest for its tasks.')}</p>
      <div className="form-actions"><button type="button" className="btn-secondary" onClick={() => setCreatingRoom(false)}>{t('Cancel')}</button><ActionButton className="btn-primary" disabled={busy !== '' || !roomName.trim()} disabledReason={busy !== '' ? 'Wait for the current operation to finish.' : 'Enter a Room name.'}>{busy === 'room-create' ? t('Creating Room…') : t('Create Room')}</ActionButton></div>
    </form>}

    <section className="project-rooms" aria-label={t('Topic Rooms')}>
      <h2>{t('Topic Rooms')}</h2>
      {[...activeRooms, ...archivedRooms].map((room) => <article className={`project-room-card ${room.state === 'archived' ? 'archived' : ''}`} key={room.id}>
        {editingRoomId === room.id ? <form className="inline-edit room-edit" onSubmit={(event) => { event.preventDefault(); void renameRoom(room) }}>
          <label>{t('Room name')}<input autoFocus value={editRoomName} onChange={(event) => setEditRoomName(event.target.value)} /></label>
          <label>{t('Room Brief')}<textarea value={editRoomDescription} onChange={(event) => setEditRoomDescription(event.target.value)} /></label>
          <ActionButton className="btn-primary" disabled={busy !== '' || !editRoomName.trim()} disabledReason={busy !== '' ? 'Wait for the current operation to finish.' : 'Enter a Room name.'}>{t('Save')}</ActionButton><button type="button" className="btn-secondary" onClick={() => setEditingRoomId('')}>{t('Cancel')}</button>
        </form> : <>
          <button type="button" className="project-room-main" onClick={() => onOpenRoom(room)}><strong>{room.name}</strong><span>{room.description || t('No description.')}</span><small>{room.id === project.defaultRoomId ? t('Default Room') : t('Topic Room')}{room.state === 'archived' ? ` · ${t('Archived')}` : ''}</small></button>
          <div className="project-room-actions">
            {project.state === 'active' && room.state === 'active' && <RoomResourceEditor room={room} repositories={v2Repositories.filter((repository) => repository.state === 'active')} />}
            {project.state === 'active' && room.state === 'active' && <button type="button" className="quiet-action" onClick={() => { setEditingRoomId(room.id); setEditRoomName(room.name); setEditRoomDescription(room.description) }}>{t('Rename Room')}</button>}
            {room.state === 'active' ? <ActionButton type="button" className="quiet-action" disabled={busy !== ''} disabledReason={'Wait for the current operation to finish.'} onClick={() => void changeRoomState(room, 'archive')}>{t('Archive Room')}</ActionButton> : <ActionButton type="button" className="quiet-action" disabled={busy !== '' || project.state === 'archived'} disabledReason={busy !== '' ? 'Wait for the current operation to finish.' : 'Restore the Project before restoring this Room.'} onClick={() => void changeRoomState(room, 'restore')}>{t('Restore Room')}</ActionButton>}
          </div>
        </>}
      </article>)}
    </section>

    {project.currentAction?.url && onResume && <section className="project-resume"><h2>{t('Continue work')}</h2><p>{t('Room')}: <strong>{project.rooms.find((room) => room.id === project.currentAction?.target.roomId)?.name ?? project.currentAction.target.roomId}</strong></p><button type="button" className="btn-primary" onClick={() => onResume(project.currentAction!.url)}>{t('Resume')} · {project.currentTaskTitle}</button></section>}
  </div>
}
