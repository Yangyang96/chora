import { ActionButton } from './ActionButton'
import { useI18n } from '../i18n'
import { taskDisplayStatus } from '../taskStatus'
import type { ProjectView, RoomSummary, TaskSummary } from '../types'

type RoomHomeProps = {
  room: RoomSummary
  tasks: TaskSummary[]
  onNewTask: () => void
  onOpenTask: (task: TaskSummary) => void
  onArchiveTask: (task: TaskSummary) => void
  onRestoreTask: (task: TaskSummary) => void
  project?: ProjectView | null
  onOpenProject?: () => void
  onArchiveRoom?: () => void
  onRestoreRoom?: () => void
}

export function RoomHome({ room, tasks, onNewTask, onOpenTask, onArchiveTask, onRestoreTask, project, onOpenProject, onArchiveRoom, onRestoreRoom }: RoomHomeProps) {
  const { t } = useI18n()
  const active = tasks.filter((task) => !task.archived)
  const archived = tasks.filter((task) => task.archived)
  const unowned = room.ownershipKind === 'unclassified'
  const ownerUnresolved = room.ownershipKind === 'project' && (!project || project.id !== room.projectId)
  const readOnly = room.state === 'archived' || project?.state === 'archived' || unowned || ownerUnresolved
  const repositorySummary = project?.repositories
    ? project.repositories.length === 0
      ? t('No repositories in this Project.')
      : t('{count} repositories: {names}', { count: project.repositories.length, names: project.repositories.map((repository) => repository.name).join(', ') })
    : project?.repositoryBinding
      ? `${t('Repository')}: ${project.repositoryBinding.name}`
      : ''

  return (
    <div className="room-home">
      <div className="room-home-head">
        <div>
          {project && <nav className="breadcrumb" aria-label={t('Breadcrumb')}><button type="button" onClick={onOpenProject}>{project.name}</button><span aria-hidden="true">/</span><span>{room.name}</span></nav>}
          <h1>{room.name}</h1>
          <p>{room.description ? t(room.description) : t('No description.')}</p>
          {repositorySummary && <p className="room-repository">{repositorySummary}</p>}
        </div>
        <div className="room-home-actions">
          <ActionButton type="button" className="btn-primary" disabled={readOnly} disabledReason={project?.state === 'archived' ? 'Restore the Project before creating a task.' : room.state === 'archived' ? 'Restore the Room before creating a task.' : unowned ? 'Assign this Room to a Project before creating a task.' : 'Wait for the owning Project to load.'} onClick={onNewTask}>
            {t('＋ New Task')}
          </ActionButton>
          {room.state === 'archived' ? <ActionButton type="button" className="btn-secondary" disabled={project?.state === 'archived'} disabledReason={'Restore the Project before restoring this Room.'} onClick={onRestoreRoom}>{t('Restore Room')}</ActionButton> : project && <button type="button" className="btn-secondary" onClick={onArchiveRoom}>{t('Archive Room')}</button>}
        </div>
      </div>

      {project?.state === 'archived' && <p className="warning-banner" role="status">{t('This Project is archived. Rooms and history remain readable; restore the Project to start new work.')}</p>}
      {room.state === 'archived' && <p className="warning-banner" role="status">{t('This Room is archived and read-only.')}</p>}
      {unowned && <p className="error-banner" role="alert">{t('This Workbench Room has no verified Project owner. History is readable, but new work is blocked.')}</p>}
      {ownerUnresolved && <p className="error-banner" role="alert">{t('This Room’s Project owner could not be resolved. History is readable, but new work is blocked.')}</p>}

      {active.length === 0 && archived.length === 0 ? (
        <div className="empty">
          <p>{t('This Room has no Tasks yet.')}</p>
          <ActionButton type="button" className="btn-primary" disabled={readOnly} disabledReason={project?.state === 'archived' ? 'Restore the Project before creating a task.' : room.state === 'archived' ? 'Restore the Room before creating a task.' : unowned ? 'Assign this Room to a Project before creating a task.' : 'Wait for the owning Project to load.'} onClick={onNewTask}>
            {t('＋ New Task')}
          </ActionButton>
        </div>
      ) : (
        <>
          {active.length > 0 && (
            <div className="task-list">
              {active.map((task) => {
                const state = taskDisplayStatus(task)
                return <div className="task-card" key={task.id}>
                  <button type="button" className="task-card-main" onClick={() => onOpenTask(task)}>
                    <span className="task-card-copy">
                      <span className="task-title">{task.title}</span>
                      {state.detail && <span className="task-current-action">{t(state.detail)}</span>}
                      <span className="task-current-action">{t('Next: {action}', { action: t(state.action) })}</span>
                    </span>
                    <span className={`task-state tone-${state.tone}`}>{t(state.label, state.values)}</span>
                  </button>
                  <button type="button" className="task-archive" onClick={() => onArchiveTask(task)}>
                    {t('Archive')}
                  </button>
                </div>
              })}
            </div>
          )}
          {archived.length > 0 && (
            <details className="archived-tasks">
              <summary>{t('Archived · {count}', { count: archived.length })}</summary>
              <div className="task-list">
                {archived.map((task) => {
                  const state = taskDisplayStatus(task)
                  return <div className="task-card task-archived" key={task.id}>
                    <span className="task-title">{task.title}</span>
                      {state.detail && <span className="task-current-action">{t(state.detail)}</span>}
                    <span className={`task-state tone-${state.tone}`}>{t(state.label, state.values)}</span>
                    <button type="button" className="task-archive" onClick={() => onRestoreTask(task)}>
                      {t('Restore')}
                    </button>
                  </div>
                })}
              </div>
            </details>
          )}
        </>
      )}
    </div>
  )
}
