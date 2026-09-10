import { useEffect, useState } from 'react'
import { api, message } from '../api'
import { useI18n } from '../i18n'
import type { ProjectView } from '../types'
import { Icon } from './Icons'

type ProjectListProps = {
  onOpenProject: (project: ProjectView) => void
  onAddProject: () => void
  onResume?: (url: string) => void
  refreshKey?: number
}

type ProjectPage = { projects: ProjectView[]; nextCursor?: string }

function formatActivity(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

async function loadProjects(state: 'active' | 'archived') {
  const projects: ProjectView[] = []
  let cursor = ''
  do {
    const query = new URLSearchParams({ state })
    if (cursor) query.set('cursor', cursor)
    const page = await api<ProjectPage>(`/api/v2/projects?${query}`)
    projects.push(...page.projects)
    cursor = page.nextCursor ?? ''
  } while (cursor)
  return projects
}

function repositorySummary(project: ProjectView) {
  if (project.repositories) {
    if (project.repositories.length === 0) return { primary: 'No repositories', detail: '' }
    if (project.repositories.length > 1) return { primary: `${project.repositories.length} repositories`, detail: '' }
    const repository = project.repositories[0]
    return {
      primary: repository.localLocator,
      detail: [repository.branch, repository.dirty ? 'Dirty' : '', `Repository availability: ${repository.availability}`].filter(Boolean).join(' · '),
    }
  }
  const binding = project.repositoryBinding
  if (!binding) return { primary: 'No repositories', detail: '' }
  return { primary: binding.localLocator, detail: binding.dirtyAdmitted ? 'Dirty' : '' }
}

export function ProjectList({ onOpenProject, onAddProject, onResume, refreshKey = 0 }: ProjectListProps) {
  const { t } = useI18n()
  const [projects, setProjects] = useState<ProjectView[] | null>(null)
  const [error, setError] = useState('')
  const [filter, setFilter] = useState('')
  const [archivedProjects, setArchivedProjects] = useState<ProjectView[]>([])

  useEffect(() => {
    let cancelled = false
    setError('')
    Promise.all([loadProjects('active'), loadProjects('archived')])
      .then(([active, archived]) => {
        if (!cancelled) { setProjects(active.filter((project) => project.state !== 'archived')); setArchivedProjects(archived.filter((project) => project.state === 'archived')) }
      })
      .catch((reason) => {
        if (!cancelled) setError(message(reason))
      })
    return () => { cancelled = true }
  }, [refreshKey])

  const loading = projects === null && !error
  const visibleProjects = (projects ?? []).filter((project) => project.name.toLocaleLowerCase().includes(filter.trim().toLocaleLowerCase()))

  return (
    <div className="project-list">
      <div className="project-list-head">
        <div>
          <span className="page-eyebrow">{t('LOCAL WORKSPACE')}</span>
          <h1>{t('Projects')}</h1>
          <p className="page-description">{t('Organize Rooms, local repositories, tasks and review history in one Project.')}</p>
        </div>
        <div className="project-list-actions">
          <label className="project-filter">
            <span>{t('Filter projects')}</span>
            <input value={filter} onChange={(event) => setFilter(event.target.value)} placeholder={t('Filter projects')} />
          </label>
          <button type="button" className="btn-primary" onClick={onAddProject}>
            <Icon name="plus" />{t('Create Project')}
          </button>
        </div>
      </div>

      {error && <p className="error-banner" role="alert">{error}</p>}

      {loading ? (
        <div className="empty"><p>{t('Loading projects…')}</p></div>
      ) : !error && visibleProjects.length === 0 ? (
        <div className="project-empty">
          <span className="empty-project-mark" aria-hidden="true"><Icon name="folder" /></span>
          <h2>{t(filter.trim() ? 'No matching projects' : 'Create your first project')}</h2>
          <p>{t(filter.trim() ? 'Try another name or clear the filter.' : 'No projects yet. Create an empty Project or start from a local Git repository.')}</p>
          {!filter.trim() && <div className="empty-workflow"><span>{t('Create a Project')}</span><span aria-hidden="true">→</span><span>{t('Add repositories')}</span><span aria-hidden="true">→</span><span>{t('Work with Pi')}</span></div>}
        </div>
      ) : (
        <div className="project-cards">
          {visibleProjects.map((project) => {
            const repositories = repositorySummary(project)
            return (
              <div className="project-card" key={project.id}>
                <button type="button" className="project-card-main" onClick={() => onOpenProject(project)}>
                  <span className="project-name">{project.name}</span>
                  <span className="project-local-path">{t(repositories.primary)}</span>
                  <span className="project-meta">
                    {repositories.detail && <span className="project-badge">{repositories.detail.split(' · ').map((part) => t(part)).join(' · ')}</span>}
                    <span>{t('{count} Rooms', { count: project.rooms.length })}</span>
                    <span className="project-activity">{t('Last activity')}: {formatActivity(project.lastActivityAt)}</span>
                  </span>
                </button>
                {project.currentAction?.url && onResume && <div className="project-card-resume"><small>{t('Room')}: {project.rooms.find((room) => room.id === project.currentAction?.target.roomId)?.name ?? project.currentAction.target.roomId}</small><button type="button" className="btn-secondary" onClick={() => onResume(project.currentAction!.url)}>{t('Resume')} · {project.currentTaskTitle}</button></div>}
              </div>
            )
          })}
        </div>
      )}
      {archivedProjects.length > 0 && <details className="archived-projects"><summary>{t('Archived Projects')} · {archivedProjects.length}</summary><div className="project-cards">{archivedProjects.map((project) => {
        const repositories = repositorySummary(project)
        return <button type="button" className="project-card project-card-main" key={project.id} onClick={() => onOpenProject(project)}><span className="project-name">{project.name}</span><span className="project-local-path">{t(repositories.primary)}</span></button>
      })}</div></details>}
    </div>
  )
}
