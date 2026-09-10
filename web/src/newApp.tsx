import { ActionButton } from './ui/ActionButton'
import { automaticRetryActive } from './taskStatus'
import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { api, commandKey, message } from './api'
import { LanguageProvider, useI18n } from './i18n'
import { defaultAcceptReviewComment } from './runProvenance'
import type { TaskResourceSelection } from './taskFirstTypes'
import type { AgentExecutionProfile, PiDiscoveryView, ProjectView, RoomRef, RoomWorkspace, RunView, TaskRef, TaskSummary, TrustedLocalAcknowledgement, TrustedLocalAcknowledgementState } from './types'
import { ExternalHandoff } from './ui/ExternalHandoff'
import { AddProject } from './ui/AddProject'
import { AppShell } from './ui/AppShell'
import { CreateRoom } from './ui/CreateRoom'
import { NewTask } from './ui/NewTask'
import { PiInstallation } from './ui/PiInstallation'
import { PiDiscoveryStatus, type PiDiscoveryFetch } from './ui/PiDiscovery'
import { ProjectList } from './ui/ProjectList'
import { ProjectHome } from './ui/ProjectHome'
import { RoomHome } from './ui/RoomHome'
import { RunStream } from './ui/RunStream'
import { TaskProgress } from './ui/TaskProgress'
import { Sidebar } from './ui/Sidebar'
import { AgentExecutionDisclosure, TRUSTED_LOCAL_DISCLOSURE_POLICY } from './ui/AgentExecutionProfile'
import './ui/ui.css'

type Route = { kind: 'directory' } | { kind: 'project'; projectID: string } | { kind: 'room'; roomID: string } | { kind: 'task'; roomID: string; taskID: string } | { kind: 'run'; roomID: string; taskID: string; runID: string }

type TaskResourceSummary = { repoId: string; name?: string }
type ResourceTaskRef = TaskRef & { resourceSnapshot?: { resources: TaskResourceSummary[] } }
type ResourcePreparation = {
  taskID?: string
  startedAt: number
  elapsedMs: number
  repositories: Array<{ repoId: string; name?: string; state: string; reason?: string }>
}
type TaskResourcesProgress = {
  snapshot: { resources: TaskResourceSummary[] }
  worktrees: Array<{ repoId: string; state: string; reason?: string; updatedAt?: string }>
}

function routeFromLocation(): Route {
  const parts = window.location.pathname.split('/').filter(Boolean).map(decodeURIComponent)
  if (parts.length === 2 && parts[0] === 'projects') return { kind: 'project', projectID: parts[1] }
  if (parts.length === 2 && parts[0] === 'rooms') return { kind: 'room', roomID: parts[1] }
  if (parts.length === 4 && parts[0] === 'rooms' && parts[2] === 'tasks') return { kind: 'task', roomID: parts[1], taskID: parts[3] }
  if (parts.length === 6 && parts[0] === 'rooms' && parts[2] === 'tasks' && parts[4] === 'runs') return { kind: 'run', roomID: parts[1], taskID: parts[3], runID: parts[5] }
  return { kind: 'directory' }
}

function NewAppContent() {
  const { t } = useI18n()
  const [route, setRoute] = useState<Route>(routeFromLocation)
  const [roomDetail, setRoomDetail] = useState<RoomRef | null>(null)
  const [project, setProject] = useState<ProjectView | null>(null)
  const [workspace, setWorkspace] = useState<RoomWorkspace | null>(null)
  const [run, setRun] = useState<RunView | null>(null)
  const [task, setTask] = useState<TaskRef | null>(null)
  const [view, setView] = useState<'home' | 'createRoom' | 'newTask' | 'addProject'>('home')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [initialRequirement, setInitialRequirement] = useState('')
  const [directoryRefresh, setDirectoryRefresh] = useState(0)
  const [trustedLocalSelectionReady, setTrustedLocalSelectionReady] = useState(false)
  const [trustedLocalAcknowledgementPolicy, setTrustedLocalAcknowledgementPolicy] = useState('')
  const [piDiscovery, setPiDiscovery] = useState<PiDiscoveryFetch>({ phase: 'loading' })
  const [resourcePreparation, setResourcePreparation] = useState<ResourcePreparation>()
  const automaticVerification = useRef(new Set<string>())
  const taskWorkflow = useRef<AbortController | undefined>(undefined)

  const roomID = route.kind === 'directory' || route.kind === 'project' ? undefined : route.roomID
  const roomName = roomDetail?.name ?? workspace?.room.name
  const routeProject = route.kind === 'project'
    ? project?.id === route.projectID ? project : null
    : roomDetail?.projectId ? project?.id === roomDetail.projectId ? project : null : null

  useEffect(() => {
    const onPopState = () => {
      const next = routeFromLocation()
      // Fragment navigation stays on the same Run. Reloading its data unmounts
      // the anchor target and loses the browser's scroll position.
      setRoute((current) => JSON.stringify(current) === JSON.stringify(next) ? current : next)
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  useEffect(() => {
    let cancelled = false
    api<TrustedLocalAcknowledgementState>('/api/agent-execution/trusted-local-acknowledgements/current')
      .then((current) => {
        if (cancelled) return
        const exactCurrent = current?.acknowledged === true && current.policyVersion === TRUSTED_LOCAL_DISCLOSURE_POLICY
        setTrustedLocalAcknowledgementPolicy(exactCurrent ? TRUSTED_LOCAL_DISCLOSURE_POLICY : '')
      })
      .catch(() => {
        if (!cancelled) setTrustedLocalAcknowledgementPolicy('')
      })
      .finally(() => {
        if (!cancelled) setTrustedLocalSelectionReady(true)
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    api<PiDiscoveryView>('/api/pi/discovery')
      .then((discovery) => {
        if (!cancelled) setPiDiscovery({ phase: 'loaded', discovery })
      })
      .catch(() => {
        if (!cancelled) setPiDiscovery({ phase: 'error' })
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    if (route.kind === 'directory') {
      setRoomDetail(null)
      setProject(null)
      setWorkspace(null)
      setRun(null)
      setTask(null)
      setView('home')
      return
    }
    let cancelled = false
    setError('')
    setProject(null)
    setRoomDetail(null)
    setWorkspace(null)
    setRun(null)
    setTask(null)

    if (route.kind === 'project') {
      api<ProjectView>(`/api/v2/projects/${encodeURIComponent(route.projectID)}`)
        .then((next) => { if (!cancelled) setProject(next) })
        .catch((reason) => { if (!cancelled) setError(message(reason)) })
      return () => { cancelled = true }
    }

    api<RoomRef>(`/api/rooms/${encodeURIComponent(route.roomID)}`)
      .then(async (detail) => {
        if (cancelled) return
        setRoomDetail(detail)
        if (detail.projectId) {
          const owner = await api<ProjectView>(`/api/v2/projects/${encodeURIComponent(detail.projectId)}`)
          if (!cancelled) setProject(owner)
        }
        else setProject(null)
      })
      .catch((reason) => { if (!cancelled) setError(message(reason)) })

    if (route.kind === 'run') {
      api<RunView>(`/api/rooms/${encodeURIComponent(route.roomID)}/tasks/${encodeURIComponent(route.taskID)}/runs/${encodeURIComponent(route.runID)}`)
        .then((next) => {
          if (!cancelled) setRun(next)
        })
        .catch((reason) => {
          if (!cancelled) setError(reason instanceof Error ? reason.message : String(reason))
        })
    } else if (route.kind === 'task') {
      api<TaskRef>(`/api/rooms/${encodeURIComponent(route.roomID)}/tasks/${encodeURIComponent(route.taskID)}`)
        .then((task) => {
          if (!cancelled) setTask(task)
        })
        .catch((reason) => {
          if (!cancelled) setError(reason instanceof Error ? reason.message : String(reason))
        })
    } else {
      api<RoomWorkspace>(`/api/rooms/${encodeURIComponent(route.roomID)}/workspace`)
        .then((ws) => {
          if (cancelled) return
          setWorkspace(ws)
        })
        .catch((reason) => {
          if (!cancelled) setError(reason instanceof Error ? reason.message : String(reason))
        })
    }
    return () => {
      cancelled = true
    }
  }, [route])

  useEffect(() => {
    if (route.kind !== 'room') return
    let cancelled = false
    // Room cards must reflect failures and automatic retries while the user stays here.
    const timer = window.setInterval(() => {
      api<RoomWorkspace>(`/api/rooms/${encodeURIComponent(route.roomID)}/workspace`)
        .then((next) => { if (!cancelled) setWorkspace(next) })
        .catch(() => {})
    }, 2000)
    return () => { cancelled = true; window.clearInterval(timer) }
  }, [route])

  useEffect(() => {
    if (route.kind !== 'run' || !run || run.id !== route.runID) return
    if (run.status === 'awaiting_verification' && run.verificationDisposition?.state === 'not_applicable') return
    if (!['running', 'stopping', 'verifying', 'awaiting_verification'].includes(run.status) && run.patchApplication?.state !== 'applying' && !automaticRetryActive(run.automaticRetry)) return
    let cancelled = false
    const expectedRunID = route.runID
    const timer = window.setInterval(() => {
      api<RunView>(`/api/rooms/${encodeURIComponent(route.roomID)}/tasks/${encodeURIComponent(route.taskID)}/runs/${encodeURIComponent(run.id)}`)
        .then((next) => { if (!cancelled && next.id === expectedRunID) setRun(next) })
        .catch(() => {})
    }, 1000)
    return () => { cancelled = true; window.clearInterval(timer) }
  }, [route, run?.id, run?.status, run?.verificationDisposition?.state, run?.patchApplication?.state, run?.automaticRetry?.state])

  useEffect(() => {
    if (route.kind !== 'run' || !run || run.id !== route.runID || run.status !== 'awaiting_verification' || !run.controls?.canStartVerification) return
    let cancelled = false
    const expectedRunID = route.runID
    const key = `${run.id}:${run.version}`
    if (automaticVerification.current.has(key)) return
    automaticVerification.current.add(key)
    api<RunView>(`/api/runs/${encodeURIComponent(run.id)}/verification`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Idempotency-Key': commandKey('automatic-verification'),
        'X-Chora-Automatic': 'true',
      },
      body: '{}',
    })
      .then((next) => { if (!cancelled && next.id === expectedRunID) setRun(next) })
      .catch(async (reason) => {
        try {
          const current = await api<RunView>(`/api/runs/${encodeURIComponent(run.id)}`)
          if (cancelled || current.id !== expectedRunID) return
          setRun(current)
          if (current.status === 'awaiting_verification' && current.controls?.canStartVerification) {
            setError(reason instanceof Error ? reason.message : String(reason))
            automaticVerification.current.delete(key)
          }
        } catch (reason) {
          if (cancelled) return
          setError(reason instanceof Error ? reason.message : String(reason))
          automaticVerification.current.delete(key)
        }
      })
    return () => { cancelled = true }
  }, [route, run?.id, run?.status, run?.version, run?.controls?.canStartVerification])

  useEffect(() => {
    if (!resourcePreparation) return
    let stopped = false
    const taskID = resourcePreparation.taskID
    const workflowSignal = taskWorkflow.current?.signal
    const updateElapsed = () => {
      setResourcePreparation((current) => current && current.startedAt === resourcePreparation.startedAt
        ? { ...current, elapsedMs: Date.now() - current.startedAt }
        : current)
    }
    const loadProgress = async () => {
      if (stopped || !taskID) return
      try {
        const progress = await api<TaskResourcesProgress>(`/api/v2/tasks/${encodeURIComponent(taskID)}/resources`, { signal: workflowSignal })
        if (stopped) return
        const rows = new Map(progress.worktrees.map((worktree) => [worktree.repoId, worktree]))
        setResourcePreparation((current) => current?.taskID === taskID ? {
          ...current,
          repositories: progress.snapshot.resources.map((resource) => ({
            repoId: resource.repoId,
            name: resource.name,
            state: rows.get(resource.repoId)?.state ?? 'planned',
            reason: rows.get(resource.repoId)?.reason,
          })),
        } : current)
      } catch (reason) {
        if (!workflowSignal?.aborted && !stopped) setError(message(reason))
      }
    }
    updateElapsed()
    void loadProgress()
    const elapsedTimer = window.setInterval(updateElapsed, 250)
    const progressTimer = taskID ? window.setInterval(() => void loadProgress(), 500) : undefined
    return () => {
      stopped = true
      window.clearInterval(elapsedTimer)
      if (progressTimer !== undefined) window.clearInterval(progressTimer)
    }
  }, [resourcePreparation?.taskID, resourcePreparation?.startedAt])

  function navigate(path: string, nextView: 'home' | 'createRoom' | 'newTask' | 'addProject' = 'home') {
    window.history.pushState({}, '', path)
    setView(nextView)
    setRoute(routeFromLocation())
  }

  // Plan default-pass: submit the generated draft, accept the revision, and
  // start the Run automatically so the user lands directly on the execution
  // stream (the plan remains viewable through the audit details).
  async function autoRun(task: TaskRef, signal?: AbortSignal): Promise<RunView | null> {
    let current = task
    const hasResourceSnapshot = Boolean((current as ResourceTaskRef).resourceSnapshot)
    if (current.executionProfile === 'real_spec_coding' && !hasResourceSnapshot && current.worktree?.state !== 'ready') {
      throw new Error(current.worktree?.reason || 'This Task’s managed worktree is not ready. Restart Chora to retry recovery.')
    }
    if (!current.planning?.acceptance && current.planning?.draft) {
      await api<unknown>(`/api/tasks/${encodeURIComponent(current.id)}/plan/drafts/${encodeURIComponent(current.planning.draft.id)}/submit`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('submit-plan') },
        body: JSON.stringify({ expectedEditVersion: current.planning.draft.editVersion, confirmUnchanged: false }),
        signal,
      })
      current = await api<TaskRef>(`/api/tasks/${encodeURIComponent(current.id)}`, { signal })
    }
    if (!current.planning?.acceptance) {
      const revision = [...(current.planning?.revisions ?? [])].reverse().find((item) => !item.review)
      if (revision) {
        await api<unknown>(`/api/tasks/${encodeURIComponent(current.id)}/plan/revisions/${encodeURIComponent(revision.id)}/activate`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('accept-plan') },
          body: '{}',
          signal,
        })
        current = await api<TaskRef>(`/api/tasks/${encodeURIComponent(current.id)}`, { signal })
      }
    }
    const acceptance = current.planning?.acceptance
    if (!acceptance) return null
    return api<RunView>(`/api/tasks/${encodeURIComponent(current.id)}/runs`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('create-run') },
      body: JSON.stringify({ revisionId: acceptance.revisionId }),
      signal,
    })
  }

  function beginTaskWorkflow(resources?: Array<{ repoId: string }>) {
    taskWorkflow.current?.abort()
    const controller = new AbortController()
    taskWorkflow.current = controller
    if (resources) {
      const startedAt = Date.now()
      setResourcePreparation({
        startedAt,
        elapsedMs: 0,
        repositories: resources.map((resource) => ({ repoId: resource.repoId, state: 'planned' })),
      })
    }
    return controller
  }

  function finishTaskWorkflow(controller: AbortController) {
    if (taskWorkflow.current !== controller) return false
    taskWorkflow.current = undefined
    setResourcePreparation(undefined)
    return true
  }

  function cancelTaskWorkflow() {
    taskWorkflow.current?.abort()
    taskWorkflow.current = undefined
    setResourcePreparation(undefined)
    setBusy(false)
    setError('')
    if (view === 'newTask') setView('home')
  }

  async function startExistingTask(current: TaskRef) {
    if (busy || route.kind !== 'task') return
    if (roomDetail === null || (roomDetail.ownershipKind === 'project' && routeProject === null) || routeProject?.state === 'archived' || roomDetail.state === 'archived' || roomDetail.ownershipKind === 'unclassified') {
      setError(t('New work is blocked by the current Project or Room state.'))
      return
    }
    const ownerRoom = route.roomID
    const controller = beginTaskWorkflow((current as ResourceTaskRef).resourceSnapshot?.resources)
    if ((current as ResourceTaskRef).resourceSnapshot) {
      setResourcePreparation((progress) => progress ? { ...progress, taskID: current.id, repositories: (current as ResourceTaskRef).resourceSnapshot!.resources.map((resource) => ({ ...resource, state: 'planned' })) } : progress)
    }
    setBusy(true)
    setError('')
    try {
      const started = await autoRun(current, controller.signal)
      if (started) navigate(`/rooms/${encodeURIComponent(ownerRoom)}/tasks/${encodeURIComponent(current.id)}/runs/${encodeURIComponent(started.id)}`)
    } catch (reason) {
      if (!controller.signal.aborted) setError(message(reason))
    } finally {
      if (finishTaskWorkflow(controller)) setBusy(false)
    }
  }

  async function createRoom(name: string, description: string) {
    setBusy(true)
    setError('')
    try {
      const created = await api<RoomRef>('/api/rooms', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('create-room') },
        body: JSON.stringify({ name, description }),
      })
      setDirectoryRefresh((n) => n + 1)
      navigate(`/rooms/${encodeURIComponent(created.id)}`)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason))
    } finally {
      setBusy(false)
    }
  }

  function addProject(project: ProjectView) {
    setDirectoryRefresh((n) => n + 1)
    navigate(`/projects/${encodeURIComponent(project.id)}`)
  }

  async function acknowledgeTrustedLocal(): Promise<boolean> {
    setBusy(true)
    setError('')
    try {
      const acknowledgement = await api<TrustedLocalAcknowledgement>('/api/agent-execution/trusted-local-acknowledgements', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('acknowledge-trusted-local') },
        body: JSON.stringify({ policyVersion: TRUSTED_LOCAL_DISCLOSURE_POLICY }),
      })
      if (acknowledgement.policyVersion !== TRUSTED_LOCAL_DISCLOSURE_POLICY || !acknowledgement.actorId ||
        !acknowledgement.sessionId || !acknowledgement.acknowledgedAt || typeof acknowledgement.replayed !== 'boolean') {
        throw new Error('Trusted Local acknowledgement did not prove the current disclosure policy.')
      }
      setTrustedLocalAcknowledgementPolicy(acknowledgement.policyVersion)
      return true
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason))
      return false
    } finally {
      setBusy(false)
    }
  }

  async function createTask(requirement: string, agentExecutionProfile: AgentExecutionProfile, projectSettingsVersion?: number, resources?: TaskResourceSelection[]) {
    const briefLocator = roomID ? `room://${roomID}/brief` : ''
    const revisions = roomDetail?.revisions ?? []
    const currentBrief = [...revisions].reverse().find((revision) => revision.locator === briefLocator)
      ?? [...revisions].reverse().find((revision) => !revision.locator && revision.title === 'Room Brief' && revision.provenance.kind === 'human_room')
      ?? roomDetail?.initialRevision
    if (!roomID || !currentBrief) return
    if (roomDetail === null || (roomDetail.ownershipKind === 'project' && routeProject === null) || routeProject?.state === 'archived' || roomDetail.state === 'archived' || roomDetail.ownershipKind === 'unclassified') {
      setError(t('New work is blocked by the current Project or Room state.'))
      return
    }
    const controller = beginTaskWorkflow(resources)
    setBusy(true)
    setError('')
    try {
      const created = await api<TaskRef>(resources ? `/api/v2/rooms/${encodeURIComponent(roomID)}/tasks` : `/api/rooms/${encodeURIComponent(roomID)}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('create-task') },
        body: JSON.stringify(resources ? { requirement, title: requirement.slice(0,96), resources, revisionIds: [currentBrief.id], agentExecutionProfile } : {
          title: requirement.slice(0, 96),
          goal: requirement,
          criteria: ['Requirement satisfied'],
          revisionIds: [currentBrief.id],
          agentExecutionProfile,
          projectSettingsVersion,
        }),
        signal: controller.signal,
      })
      const resourceTask = created as ResourceTaskRef
      if (resources) {
        setResourcePreparation((progress) => progress ? {
          ...progress,
          taskID: created.id,
          repositories: (resourceTask.resourceSnapshot?.resources ?? progress.repositories).map((resource) => ({ ...resource, state: 'planned' })),
        } : progress)
      }
      setDirectoryRefresh((n) => n + 1)
      navigate(`/rooms/${encodeURIComponent(roomID)}/tasks/${encodeURIComponent(created.id)}`)
      const started = await autoRun(created, controller.signal)
      if (started) navigate(`/rooms/${encodeURIComponent(roomID)}/tasks/${encodeURIComponent(created.id)}/runs/${encodeURIComponent(started.id)}`)
    } catch (reason) {
      if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : String(reason))
    } finally {
      if (finishTaskWorkflow(controller)) setBusy(false)
    }
  }

  async function resultClosed() {
    if (!run) return
    try {
      setRun(await api<RunView>(`/api/runs/${encodeURIComponent(run.id)}`))
      await refreshWorkspace()
    } catch (reason) { setError(message(reason)) }
  }

  async function cancelRun() {
    if (!run) return
    setBusy(true)
    try {
      const next = await api<RunView>(`/api/runs/${encodeURIComponent(run.id)}/cancel`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('cancel-run') },
        body: JSON.stringify({ reason: 'Cancelled by the local user.' }),
      })
      setRun(next)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason))
    } finally {
      setBusy(false)
    }
  }

  async function reviewRun(kind: 'accept' | 'reject', comment: string, rejectionClass?: 'implementation_gap' | 'planning_gap' | 'contract_change_required') {
    if (!run) return
    // The immutable decision stays auditable: persisted reviews require a
    // non-empty reason for accept and reject, so an empty comment is auto-filled
    // with a bounded default rather than forcing the user to type one.
    const finalComment = comment.trim() || (kind === 'accept' ? defaultAcceptReviewComment(run) : rejectionClass ? `Rejected: ${rejectionClass}` : 'Rejected by the local user.')
    setBusy(true)
    try {
      let next = await api<RunView>(`${run.resourceResult ? "/api/v2" : "/api"}/runs/${encodeURIComponent(run.id)}/review`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('review-run') },
        body: JSON.stringify({ expectedVersion: run.version, kind, comment: finalComment, rejectionClass: !run.resourceResult && kind === 'reject' ? rejectionClass : undefined, resultDigest: run.resourceResult?.digest }),
      })
      if (kind === 'accept' && next.controls?.canApplyPatch && !next.resourceResult?.repositories?.some((repository) => repository.deliveryMode === 'task_branch')) {
		setRun(next)
		next = await api<RunView>(`${run.resourceResult ? "/api/v2" : "/api"}/runs/${encodeURIComponent(run.id)}/apply`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('apply-run-patch') },
			body: JSON.stringify({ expectedVersion: next.version, resultDigest: next.resourceResult?.digest }),
		})
	  }
      if (kind === 'reject' && rejectionClass === 'implementation_gap' && next.controls?.canRetry) {
        next = await api<RunView>(`/api/runs/${encodeURIComponent(run.id)}/retry`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('review-retry-run') },
          body: JSON.stringify({ expectedVersion: next.version, instructions: finalComment }),
        })
      }
      setRun(next)
      setDirectoryRefresh((n) => n + 1)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason))
    } finally {
      setBusy(false)
    }
  }

  async function applyRunPatch() {
	if (!run) return
	setBusy(true)
	setError('')
	try {
		const next = await api<RunView>(`${run.resourceResult ? "/api/v2" : "/api"}/runs/${encodeURIComponent(run.id)}/apply`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('apply-run-patch') },
			body: JSON.stringify({ expectedVersion: run.version, resultDigest: run.resourceResult?.digest }),
		})
		setRun(next)
		setDirectoryRefresh((n) => n + 1)
	} catch (reason) {
		setError(reason instanceof Error ? reason.message : String(reason))
		try { setRun(await api<RunView>(`/api/runs/${encodeURIComponent(run.id)}`)) } catch { /* keep the last governed view */ }
	} finally {
		setBusy(false)
	}
  }

  async function retryRun(instructions: string) {
    if (!run) return
    setBusy(true)
    try {
      const next = await api<RunView>(`/api/runs/${encodeURIComponent(run.id)}/retry`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('retry-run') },
        body: JSON.stringify({ expectedVersion: run.version, instructions }),
      })
      setRun(next)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason))
    } finally {
      setBusy(false)
    }
  }

  async function switchRunProfile(profile: AgentExecutionProfile, reason: string): Promise<boolean> {
    if (!run) return false
    setBusy(true)
    setError('')
    try {
      const next = await api<RunView>(`/api/runs/${encodeURIComponent(run.id)}/agent-execution-profile`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('switch-agent-execution-profile') },
        body: JSON.stringify({ profile, reason, expectedVersion: run.version }),
      })
      setRun(next)
      setDirectoryRefresh((n) => n + 1)
      return true
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason))
      return false
    } finally {
      setBusy(false)
    }
  }

  async function retryVerification() {
    if (!run) return
    setBusy(true)
    try {
      const next = await api<RunView>(`/api/runs/${encodeURIComponent(run.id)}/verification/retry`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('retry-verification') },
        body: '{}',
      })
      setRun(next)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason))
    } finally {
      setBusy(false)
    }
  }

  async function resolveDecision(gateID: string, optionID: string) {
    setBusy(true)
    try {
      const next = await api<RunView>(`/api/decision-gates/${encodeURIComponent(gateID)}/resolve`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('resolve-decision') },
        body: JSON.stringify({ optionId: optionID, note: 'Continue from the persisted bounded choice.' }),
      })
      setRun(next)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason))
    } finally {
      setBusy(false)
    }
  }

  async function archiveTask(task: TaskSummary) {
    setBusy(true)
    try {
      await api(`/api/tasks/${encodeURIComponent(task.id)}/archive`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('archive-task') },
        body: '{}',
      })
      await refreshWorkspace()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason))
    } finally {
      setBusy(false)
    }
  }

  async function restoreTask(task: TaskSummary) {
    setBusy(true)
    try {
      await api(`/api/tasks/${encodeURIComponent(task.id)}/restore`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('restore-task') },
        body: '{}',
      })
      await refreshWorkspace()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason))
    } finally {
      setBusy(false)
    }
  }

  async function changeRoomState(action: 'archive' | 'restore') {
    if (!roomDetail || !workspace) return
    setBusy(true); setError('')
    try {
      const updated = await api<RoomRef>(`/api/rooms/${encodeURIComponent(roomDetail.id)}/${action}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey(`${action}-room`) },
        body: JSON.stringify({ expectedVersion: roomDetail.version }),
      })
      setRoomDetail(updated)
      setWorkspace({ ...workspace, room: { ...workspace.room, ...updated, state: updated.state ?? workspace.room.state, version: updated.version ?? workspace.room.version, archivedAt: updated.archivedAt ?? workspace.room.archivedAt } })
      if (routeProject) setProject({ ...routeProject, rooms: routeProject.rooms.map((room) => room.id === updated.id ? { ...room, ...updated, state: updated.state ?? room.state, version: updated.version ?? room.version, archivedAt: updated.archivedAt ?? room.archivedAt } : room) })
      setDirectoryRefresh((n) => n + 1)
    } catch (reason) { setError(message(reason)) } finally { setBusy(false) }
  }

  async function refreshWorkspace() {
    if (!roomID) return
    const ws = await api<RoomWorkspace>(`/api/rooms/${encodeURIComponent(roomID)}/workspace`)
    setWorkspace(ws)
    setDirectoryRefresh((n) => n + 1)
  }

  const localWorkbench = piDiscovery.phase !== 'loaded' || piDiscovery.discovery.state !== 'unavailable'
  const sidebar = (
    <Sidebar
      activeRoomId={roomID}
      activeTaskId={route.kind === 'task' || route.kind === 'run' ? route.taskID : undefined}
      refreshKey={directoryRefresh}
      onSelectRoom={(room) => navigate(`/rooms/${encodeURIComponent(room.id)}`)}
      onSelectTask={(room, task) => navigate(task.currentAction.url || `/rooms/${encodeURIComponent(room.id)}/tasks/${encodeURIComponent(task.id)}`)}
      onCreateRoom={() => { if (!localWorkbench) setView('createRoom') }}
      onHome={() => navigate('/')}
      localWorkbench={localWorkbench}
      project={routeProject}
      onSelectProject={(selected) => navigate(`/projects/${encodeURIComponent(selected.id)}`)}
    />
  )

  function changeRequirement() {
    if (!roomID) return
    setInitialRequirement('')
    navigate(`/rooms/${encodeURIComponent(roomID)}`, 'newTask')
  }

  function recoverExecutionChoice() {
    if (!task || !roomID) return
    setInitialRequirement(task.goal || task.title || '')
    setError('')
    navigate(`/rooms/${encodeURIComponent(roomID)}`, 'newTask')
  }

  const unsupportedTaskProfile = localWorkbench && task?.executionProfile === 'real_spec_coding' && task.agentExecutionProfile !== 'trusted_local'
  const workBlocked = route.kind !== 'directory' && route.kind !== 'project' && (roomDetail === null || (roomDetail.ownershipKind === 'project' && routeProject === null) || routeProject?.state === 'archived' || roomDetail.state === 'archived' || roomDetail.ownershipKind === 'unclassified')

  const taskPlan = task?.planning?.draft?.content ?? task?.planning?.revisions.at(-1)?.content
  const routeRun = route.kind === 'run' && run?.id === route.runID ? run : null
  let main: ReactNode
  if (view === 'addProject') {
    main = <AddProject onAdded={addProject} onCancel={() => setView('home')} />
  } else if (view === 'createRoom' && !localWorkbench) {
    main = <CreateRoom busy={busy} onCancel={() => setView('home')} onCreate={createRoom} />
  } else if (route.kind === 'directory') {
    main = <ProjectList onResume={navigate} refreshKey={directoryRefresh} onAddProject={() => setView('addProject')} onOpenProject={(selected) => navigate(`/projects/${encodeURIComponent(selected.id)}`)} />
  } else if (route.kind === 'project') {
    main = routeProject ? <ProjectHome key={routeProject.id} project={routeProject} onChange={(updated) => { setProject(updated); setDirectoryRefresh((n) => n + 1) }} onOpenRoom={(room) => navigate(`/rooms/${encodeURIComponent(room.id)}`)} onResume={navigate} /> : null
  } else if (view === 'newTask' && workspace) {
    main = (
      <NewTask
        roomId={roomID ?? undefined}
        projectId={routeProject?.id}
        initialRequirement={initialRequirement}
        roomName={workspace.room.name}
        busy={busy}
        preparationPending={Boolean(resourcePreparation)}
        trustedLocalSelectionReady={trustedLocalSelectionReady}
        trustedLocalAcknowledgementPolicy={trustedLocalAcknowledgementPolicy}
        piDiscovery={piDiscovery}
        onCancel={resourcePreparation ? cancelTaskWorkflow : () => setView('home')}
        onSubmit={createTask}
        onAcknowledgeTrustedLocal={acknowledgeTrustedLocal}
      />
    )
  } else if (route.kind === 'task' || route.kind === 'run') {
    main = routeRun ? (
      <RunStream run={routeRun} onResultClosed={() => void resultClosed()} busy={busy} trustedLocalSelectionReady={trustedLocalSelectionReady} trustedLocalAcknowledgementPolicy={trustedLocalAcknowledgementPolicy} piDiscovery={piDiscovery} onCancel={cancelRun} onReview={reviewRun} onApply={applyRunPatch} onRetry={retryRun} onSwitchProfile={switchRunProfile} onAcknowledgeTrustedLocal={acknowledgeTrustedLocal} onResolveDecision={resolveDecision} onRetryVerification={retryVerification} onChangeRequirement={changeRequirement} />
    ) : (
      <div className="empty">
        {task && <TaskProgress preparing={busy} />}
        <p>{task ? task.title || t('Task') : t('Loading…')}</p>
        {taskPlan && <section aria-label={t('Plan')}><h2>{t('Plan')}</h2><ol>{taskPlan.technical_steps.map((step, index) => <li key={index}>{step}</li>)}</ol></section>}
        {unsupportedTaskProfile ? <>
          <p role="status">{t('This task selected a Docker Sandbox profile, which this local workbench cannot start. Keep this task and reuse its requirement to explicitly choose Local Connected for a new task.')}</p>
          <ActionButton type="button" className="btn-primary" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={recoverExecutionChoice}>{t('Reuse requirement and choose execution mode')}</ActionButton>
        </> : task && <ActionButton type="button" className="btn-primary" disabled={busy || piDiscovery.phase !== 'loaded' || workBlocked} disabledReason={busy ? 'Wait for the current operation to finish.' : piDiscovery.phase !== 'loaded' ? 'Checking Pi availability. Please wait.' : 'Restore the Project and Room before starting new work.'} onClick={() => void startExistingTask(task)}>{t('Start Run')}</ActionButton>}
        {workBlocked && <p role="status">{t('New work is blocked by the current Project or Room state.')}</p>}
        {task?.agentExecutionProfile && <p>Agent profile · <AgentExecutionDisclosure profile={task.agentExecutionProfile} /></p>}
      </div>
    )
  } else if (workspace) {
    main = <RoomHome room={workspace.room} tasks={workspace.tasks} project={routeProject} onOpenProject={routeProject ? () => navigate(`/projects/${encodeURIComponent(routeProject.id)}`) : undefined} onNewTask={() => { setInitialRequirement(''); setView('newTask') }} onOpenTask={(task) => navigate(task.currentAction.url || `/rooms/${encodeURIComponent(workspace.room.id)}/tasks/${encodeURIComponent(task.id)}`)} onArchiveTask={archiveTask} onRestoreTask={restoreTask} onArchiveRoom={() => void changeRoomState('archive')} onRestoreRoom={() => void changeRoomState('restore')} />
  } else {
    main = null
  }

  return (
    <AppShell roomName={roomName} sidebar={sidebar} piDiscovery={piDiscovery} onHome={() => navigate('/')}>
      {error && <p className="error-banner" role="alert">{error}</p>}
      {routeProject && roomID && <nav className="workspace-breadcrumb" aria-label={t('Breadcrumb')}><button type="button" onClick={() => navigate(`/projects/${encodeURIComponent(routeProject.id)}`)}>{routeProject.name}</button><span aria-hidden="true">/</span><button type="button" onClick={() => navigate(`/rooms/${encodeURIComponent(roomID)}`)}>{roomDetail?.name ?? workspace?.room.name ?? roomID}</button>{(route.kind === 'task' || route.kind === 'run') && <><span aria-hidden="true">/</span><span>{task?.title ?? routeRun?.task.title ?? route.taskID}</span></>}</nav>}
      {routeRun?.agentExecution?.profile === 'trusted_local' && piDiscovery.phase === 'loaded' && piDiscovery.discovery.state !== 'ready' && <PiDiscoveryStatus discovery={piDiscovery.discovery} />}
      {route.kind !== 'directory' && route.kind !== 'project' && <ExternalHandoff roomId={route.roomID} taskId={route.kind === 'task' || route.kind === 'run' ? route.taskID : undefined} />}
      {resourcePreparation && <section className="panel" aria-label={t('Repository preparation')}>
        <h2>{t('Preparing repositories')}</h2>
        <p role="status">{t('Preparing the task workspace · {seconds} s elapsed', { seconds: Math.floor(resourcePreparation.elapsedMs / 1000) })}</p>
        <ul>{resourcePreparation.repositories.map((repository) => <li key={repository.repoId}>
          <strong>{repository.name || repository.repoId}</strong> · {repository.state}{repository.reason ? ` · ${repository.reason}` : ''}
        </li>)}</ul>
        <button type="button" className="btn-secondary" onClick={cancelTaskWorkflow}>{t('Cancel preparation')}</button>
      </section>}
      {localWorkbench && (route.kind === 'directory' || view === 'newTask') && <PiInstallation existing={piDiscovery.phase === 'loaded' ? piDiscovery.discovery : undefined} onChanged={() => { void api<PiDiscoveryView>('/api/pi/discovery').then(discovery => setPiDiscovery({phase:'loaded',discovery})).catch(() => setPiDiscovery({phase:'error'})) }} />}
      {main}
    </AppShell>
  )
}

export default function NewApp() {
  return (
    <LanguageProvider>
      <NewAppContent />
    </LanguageProvider>
  )
}
