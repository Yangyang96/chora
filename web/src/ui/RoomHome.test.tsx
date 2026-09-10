import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { ProjectView, RoomSummary, TaskSummary } from '../types'
import { RoomHome } from './RoomHome'

const room: RoomSummary = {
  id: 'room-1', name: 'Kusion', description: 'Project workspace', state: 'active', version: 1,
  archivedAt: '', lastActivityAt: '', taskCounts: { total: 1, open: 1, terminal: 0 }, humanActionRequired: false,
}

function acceptedTask(application?: string): TaskSummary {
  return {
    id: 'task-1', title: '完成 Kusion CLI', status: 'open', archived: false, lastActivityAt: '',
    currentPlanRevision: null, runCount: 1,
    currentAction: { kind: 'apply_patch', target: {}, url: '', reason: '' },
    latestRun: { id: 'run-1', taskId: 'task-1', status: 'accepted', patchApplicationState: application, version: 1, attempt: 1, createdAt: '', updatedAt: '', startedAt: '', reviewRequestedAt: '', terminalAt: '' },
  }
}

describe('RoomHome task state', () => {
  beforeEach(() => window.localStorage.clear())

  test('shows accepted state and a concrete next action under the title', () => {
    render(<LanguageProvider><RoomHome room={room} tasks={[acceptedTask()]} onNewTask={vi.fn()} onOpenTask={vi.fn()} onArchiveTask={vi.fn()} onRestoreTask={vi.fn()} /></LanguageProvider>)
    expect(screen.getByText('Accepted')).toBeInTheDocument()
    expect(screen.getByText('Next: Apply accepted changes')).toBeInTheDocument()
    expect(screen.queryByText('Applied')).not.toBeInTheDocument()
  })

  test('renders confirmed application status and action in Chinese', () => {
    window.localStorage.setItem('chora.locale', 'zh-CN')
    render(<LanguageProvider><RoomHome room={room} tasks={[acceptedTask('applied')]} onNewTask={vi.fn()} onOpenTask={vi.fn()} onArchiveTask={vi.fn()} onRestoreTask={vi.fn()} /></LanguageProvider>)
    expect(screen.getByText('已应用')).toBeInTheDocument()
    expect(screen.getByText('下一步：应用已接受的改动')).toBeInTheDocument()
  })

  test('translates the generated local project description', () => {
    window.localStorage.setItem('chora.locale', 'zh-CN')
    render(<LanguageProvider><RoomHome room={{ ...room, description: 'Admitted local Git repository' }} tasks={[]} onNewTask={vi.fn()} onOpenTask={vi.fn()} onArchiveTask={vi.fn()} onRestoreTask={vi.fn()} /></LanguageProvider>)
    expect(screen.getByText('已接入的本地 Git 仓库')).toBeInTheDocument()
  })

  test('keeps a Project-scoped Room read-only until its exact owner resolves', () => {
    render(<LanguageProvider><RoomHome room={{ ...room, projectId: 'project-1', ownershipKind: 'project' }} tasks={[]} project={null} onNewTask={vi.fn()} onOpenTask={vi.fn()} onArchiveTask={vi.fn()} onRestoreTask={vi.fn()} /></LanguageProvider>)
    expect(screen.getByRole('alert')).toHaveTextContent('Project owner could not be resolved')
    for (const button of screen.getAllByRole('button', { name: '＋ New Task' })) expect(button).toBeDisabled()
  })

  test('summarizes the Project repository collection without choosing one for the Room', () => {
    const project: ProjectView = {
      id: 'project-1', name: 'Platform', state: 'active', version: 1, defaultRoomId: room.id,
      rooms: [{ ...room, projectId: 'project-1', ownershipKind: 'project' }], room: { ...room, projectId: 'project-1', ownershipKind: 'project' },
      repositories: [
        { repoId: 'repo-front', name: 'frontend', localLocator: '/code/frontend', state: 'active', version: 1, availability: 'ready', branch: 'main', head: 'a'.repeat(40), dirty: false },
        { repoId: 'repo-back', name: 'backend', localLocator: '/code/backend', state: 'active', version: 1, availability: 'ready', branch: 'main', head: 'b'.repeat(40), dirty: false },
      ],
      lastActivityAt: '', taskCounts: { total: 0, open: 0, terminal: 0 },
    }
    render(<LanguageProvider><RoomHome room={{ ...room, projectId: project.id, ownershipKind: 'project' }} tasks={[]} project={project} onNewTask={vi.fn()} onOpenTask={vi.fn()} onArchiveTask={vi.fn()} onRestoreTask={vi.fn()} /></LanguageProvider>)
    expect(screen.getByText('2 repositories: frontend, backend')).toBeInTheDocument()
    expect(screen.queryByText('/code/frontend')).not.toBeInTheDocument()
  })

  test('retains the scalar repository summary for legacy Project fixtures', () => {
    const project: ProjectView = {
      id: 'project-1', name: 'Legacy', state: 'active', version: 1, defaultRoomId: room.id,
      rooms: [{ ...room, projectId: 'project-1', ownershipKind: 'project' }], room: { ...room, projectId: 'project-1', ownershipKind: 'project' },
      repositoryBinding: { roomId: room.id, name: 'legacy-repo', localLocator: '/code/legacy', sourceKind: 'open', cloneUrl: '', admittedBase: 'a'.repeat(40), baseIdentity: 'identity', targetWorktree: '/code/legacy', dirtyAdmitted: false, state: 'active', version: 1, createdAt: '', updatedAt: '' },
      lastActivityAt: '', taskCounts: { total: 0, open: 0, terminal: 0 },
    }
    render(<LanguageProvider><RoomHome room={{ ...room, projectId: project.id, ownershipKind: 'project' }} tasks={[]} project={project} onNewTask={vi.fn()} onOpenTask={vi.fn()} onArchiveTask={vi.fn()} onRestoreTask={vi.fn()} /></LanguageProvider>)
    expect(screen.getByText('Repository: legacy-repo')).toBeInTheDocument()
  })
})


test('shows a failed task and its cause directly in the Room', () => {
  const task = acceptedTask()
  task.latestRun!.status = 'recovery_required'
  task.latestRun!.automaticRetry = { state: 'exhausted', retriesUsed: 2, maxRetries: 2, lastFailureReason: 'runtime_output_limit_exceeded' }
  render(<LanguageProvider><RoomHome room={room} tasks={[task]} onNewTask={vi.fn()} onOpenTask={vi.fn()} onArchiveTask={vi.fn()} onRestoreTask={vi.fn()} /></LanguageProvider>)
  expect(screen.getByText('Failed 3 times · Needs attention')).toBeInTheDocument()
  expect(screen.getByText('Agent output limit exceeded')).toBeInTheDocument()
  expect(screen.getByText('Next: Recover run')).toBeInTheDocument()
})
