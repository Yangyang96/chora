import { useEffect, useState } from 'react'
import { api } from '../api'
import { useI18n } from '../i18n'
import { taskDisplayStatus } from '../taskStatus'
import type { ProjectView, RoomDirectory, RoomSummary, RoomWorkspace, TaskSummary } from '../types'
import { Icon } from './Icons'

type SidebarProps = {
  activeRoomId?: string
  activeTaskId?: string
  refreshKey?: number
  onSelectRoom: (room: RoomSummary) => void
  onSelectTask: (room: RoomSummary, task: TaskSummary) => void
  onCreateRoom: () => void
  onHome?: () => void
  localWorkbench?: boolean
  project?: ProjectView | null
  onSelectProject?: (project: ProjectView) => void
}

export function Sidebar({ activeRoomId, activeTaskId, refreshKey = 0, onSelectRoom, onSelectTask, onCreateRoom, onHome, localWorkbench = false, project, onSelectProject }: SidebarProps) {
  const { t } = useI18n()
  const [directory, setDirectory] = useState<RoomDirectory | null>(null)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [tasksByRoom, setTasksByRoom] = useState<Record<string, TaskSummary[]>>({})

  useEffect(() => {
    if (activeRoomId && activeTaskId) setExpanded((current) => current.has(activeRoomId) ? current : new Set([...current, activeRoomId]))
  }, [activeRoomId, activeTaskId])

  useEffect(() => {
    let cancelled = false
    let loading = false
    async function refresh() {
      if (loading) return
      loading = true
      try {
        const [next, taskLists] = await Promise.all([
          project ? Promise.resolve(null) : api<RoomDirectory>('/api/rooms'),
          Promise.all(Array.from(expanded).map(async (roomID) => [roomID, (await api<RoomWorkspace>(`/api/rooms/${encodeURIComponent(roomID)}/workspace`)).tasks] as const)),
        ])
        if (cancelled) return
        if (next) setDirectory(next)
        setTasksByRoom((current) => {
          const updated = { ...current }
          for (const [roomID, tasks] of taskLists) updated[roomID] = tasks
          return updated
        })
      } catch {
        // Keep the last persisted status visible while disconnected.
      } finally {
        loading = false
      }
    }
    void refresh()
    const timer = expanded.size > 0 ? window.setInterval(() => void refresh(), 2000) : undefined
    return () => {
      cancelled = true
      if (timer !== undefined) window.clearInterval(timer)
    }
  }, [refreshKey, expanded, project])

  async function toggleRoom(room: RoomSummary) {
    if (expanded.has(room.id)) {
      setExpanded((current) => {
        const next = new Set(current)
        next.delete(room.id)
        return next
      })
      return
    }
    setExpanded((current) => new Set(current).add(room.id))
    if (tasksByRoom[room.id]) return
    try {
      const workspace = await api<RoomWorkspace>(`/api/rooms/${encodeURIComponent(room.id)}/workspace`)
      setTasksByRoom((current) => ({ ...current, [room.id]: workspace.tasks }))
    } catch {
      // Keep the Room expandable even if its Task list fails to load once.
    }
  }

  function renderRoom(room: RoomSummary, archived: boolean) {
    const tasks = tasksByRoom[room.id] ?? []
    return (
      <div key={room.id}>
        <button
          type="button"
          className={`sidebar-room ${!archived && activeRoomId === room.id && !activeTaskId ? 'on' : ''}`}
          onClick={() => {
            onSelectRoom(room)
            void toggleRoom(room)
          }}
        >
          <span className="sidebar-chevron"><Icon name={expanded.has(room.id) ? 'chevron-down' : 'chevron-right'} /></span>
          <Icon name="folder" />
          <span className="sidebar-room-name">{room.name}</span>
          {room.humanActionRequired && <span className="sidebar-room-action" title={t('Human action')}><Icon name="alert" /></span>}
        </button>
        {expanded.has(room.id) &&
          tasks
            .filter((task) => !task.archived)
            .map((task) => {
            const state = taskDisplayStatus(task)
            return (
            <button
              type="button"
              key={task.id}
              className={`sidebar-task ${activeRoomId === room.id && activeTaskId === task.id ? 'on' : ''}`}
              onClick={() => onSelectTask(room, task)}
            >
              <span className="sidebar-task-copy">
                <span className="sidebar-task-title">{task.title}</span>
                <span title={state.detail ? t(state.detail) : undefined} className={`sidebar-task-status task-state tone-${state.tone}`}>{t(state.label, state.values)}</span>
              </span>
            </button>
          )})}
      </div>
    )
  }

  const projectRooms = (project?.rooms ?? []).map((room) => ({ ...room, lastActivityAt: room.lastActivityAt ?? '', taskCounts: room.taskCounts ?? { total: 0, open: 0, terminal: 0 }, humanActionRequired: room.humanActionRequired ?? false }))
  const activeRooms = project ? projectRooms.filter((room) => room.state === 'active') : directory?.activeRooms ?? []
  const archivedRooms = project ? projectRooms.filter((room) => room.state === 'archived') : directory?.archivedRooms ?? []

  return (
    <aside className="sidebar">
      {onHome && <button type="button" className={`sidebar-home ${!activeRoomId ? 'on' : ''}`} onClick={onHome}><Icon name="grid" />{t('All projects')}</button>}
      {project && onSelectProject && <button type="button" className={`sidebar-project ${!activeRoomId ? 'on' : ''}`} onClick={() => onSelectProject(project)}><Icon name="folder" /><span>{project.name}</span></button>}
      <div className="sidebar-head">
        <span>{t(localWorkbench ? 'Project spaces' : 'Rooms')}</span>
        {!localWorkbench && <button type="button" aria-label={t('New Room')} onClick={onCreateRoom}>
          <Icon name="plus" />
        </button>}
      </div>
      <div className="sidebar-tree">
        {localWorkbench && (directory || project) && activeRooms.length === 0 && <p className="sidebar-empty">{t('This Project has no active Rooms.')}</p>}
        {activeRooms.map((room) => renderRoom(room, false))}
        {archivedRooms.length > 0 && <div className="sidebar-section-label">{t('Archived')}</div>}
        {archivedRooms.map((room) => renderRoom(room, true))}
      </div>
      {localWorkbench && <div className="sidebar-footer"><strong>{t('Local workspace')}</strong><span>{t('Review changes. Apply when ready.')}</span></div>}
    </aside>
  )
}
