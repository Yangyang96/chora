import { ActionButton } from './ActionButton'
import { useEffect, useMemo, useRef, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import type { CheckCommandView, RepositoryBranchPageView, RepositoryDeliveryDefaultsView, RepositoryPageView, RepositoryResourceView, TaskOptionsView, TaskResourceSelection } from '../taskFirstTypes'
import { RepositoryDirectoryPicker } from './RepositoryDirectoryPicker'

type TaskResourcesProps = {
  projectId: string
  roomId: string
  busy: boolean
  onBlockedChange?: (reason: string) => void
  onReady: (resources: TaskResourceSelection[] | undefined) => void
}

type ResourceDraft = {
  selected: boolean
  role: 'write' | 'reference'
  scopeChoice: 'legacy' | 'repository'
  modules: string[]
  checkMode: 'auto' | 'named' | 'none'
  commands: CheckCommandView[]
  customName: string
  customCommand: string
  customDirectory: string
  saveCustom: boolean
  targetRef: string
}

type PickerState = { repoId: string; purpose: 'module' | 'check' }
type LegacyScope = NonNullable<TaskOptionsView['legacyScope']> & { repoId?: string }

function resourceAvailable(availability: TaskOptionsView['repositories'][number]['availability']) {
  return availability === 'ready' || availability === 'legacy_unverified'
}

function stableCustomID(repoId: string, name: string, command: string, directory: string) {
  let hash = 2166136261
  for (const character of `${repoId}\0${name}\0${command}\0${directory}`) {
    hash ^= character.codePointAt(0) ?? 0
    hash = Math.imul(hash, 16777619)
  }
  return `custom-${(hash >>> 0).toString(16).padStart(8, '0')}`
}

function mergeChecks(first: CheckCommandView[], second: CheckCommandView[]) {
  const result: CheckCommandView[] = []
  const seen = new Set<string>()
  for (const check of [...first, ...second]) {
    if (!seen.has(check.id)) {
      seen.add(check.id)
      result.push(check)
    }
  }
  return result
}

function resourceDraft(options: TaskOptionsView, repository: RepositoryResourceView): ResourceDraft {
  const legacy = options.legacyScope as LegacyScope | undefined
  const legacyRepository = legacy?.repoId === repository.repoId
  const mode = legacyRepository ? options.defaultCheckMode : 'auto'
  return {
    selected: (options.selectedRepoIds ?? []).includes(repository.repoId) && repository.state === 'active' && resourceAvailable(repository.availability),
    role: 'write', scopeChoice: legacyRepository ? 'legacy' : 'repository', modules: [], checkMode: mode,
    commands: legacyRepository && mode === 'named' ? [...(options.legacyChecks ?? [])] : [],
    customName: '', customCommand: '', customDirectory: '.', saveCustom: false,
    targetRef: '',
  }
}

function mergeRepositoryPages(first: RepositoryResourceView[], second: RepositoryResourceView[]) {
  const result = [...first]
  const seen = new Set(first.map((repository) => repository.repoId))
  for (const repository of second) {
    if (repository.state === 'active' && !seen.has(repository.repoId)) {
      seen.add(repository.repoId)
      result.push(repository)
    }
  }
  return result
}

export function TaskResources({ projectId, roomId, busy, onReady, onBlockedChange }: TaskResourcesProps) {
  const { t } = useI18n()
  const [options, setOptions] = useState<TaskOptionsView>()
  const [drafts, setDrafts] = useState<Record<string, ResourceDraft>>({})
  const [namedChecks, setNamedChecks] = useState<Record<string, CheckCommandView[]>>({})
  const [namedLoading, setNamedLoading] = useState<Record<string, boolean>>({})
  const [namedErrors, setNamedErrors] = useState<Record<string, string>>({})
  const [checkSaving, setCheckSaving] = useState<Record<string, boolean>>({})
  const [checkSaveErrors, setCheckSaveErrors] = useState<Record<string, string>>({})
  const [picker, setPicker] = useState<PickerState>()
  const [branches, setBranches] = useState<Record<string, RepositoryBranchPageView>>({})
  const [branchLoading, setBranchLoading] = useState<Record<string, boolean>>({})
  const [branchErrors, setBranchErrors] = useState<Record<string, string>>({})
  const [deliveryDefaults, setDeliveryDefaults] = useState<Record<string, RepositoryDeliveryDefaultsView>>({})
  const [defaultLoading, setDefaultLoading] = useState<Record<string, boolean>>({})
  const [defaultErrors, setDefaultErrors] = useState<Record<string, string>>({})
  const [defaultSaving, setDefaultSaving] = useState<Record<string, boolean>>({})
  const [error, setError] = useState('')
  const [pageError, setPageError] = useState('')
  const [pageLoading, setPageLoading] = useState(false)
  const optionsRequest = useRef<AbortController | undefined>(undefined)
  const checkSaveRequests = useRef(new Map<string, AbortController>())
  const resourceContext = useRef({ projectId, roomId })
  resourceContext.current = { projectId, roomId }

  useEffect(() => {
    const saveRequests = checkSaveRequests.current
    optionsRequest.current?.abort()
    const controller = new AbortController()
    optionsRequest.current = controller
    setOptions(undefined)
    setDrafts({})
    setNamedChecks({})
    setNamedLoading({})
    setNamedErrors({})
    setCheckSaving({})
    setCheckSaveErrors({})
    setPicker(undefined)
    setBranches({})
    setBranchLoading({})
    setBranchErrors({})
    setDeliveryDefaults({})
    setDefaultLoading({})
    setDefaultErrors({})
    setDefaultSaving({})
    setError('')
    setPageError('')
    setPageLoading(false)
    onReady(undefined)
    void api<TaskOptionsView>(`/api/v2/rooms/${encodeURIComponent(roomId)}/task-options`, { signal: controller.signal }).then((value) => {
      if (controller.signal.aborted) return
      const initial: Record<string, ResourceDraft> = {}
      for (const repository of value.repositories ?? []) {
        initial[repository.repoId] = resourceDraft(value, repository)
      }
      setOptions(value)
      setDrafts(initial)
    }).catch((reason) => {
      if (!controller.signal.aborted) setError(message(reason))
    })
    return () => {
      controller.abort()
      for (const request of saveRequests.values()) request.abort()
      saveRequests.clear()
    }
  }, [projectId, roomId, onReady])

  async function loadMoreRepositories() {
    if (!options?.nextRepositoryCursor || pageLoading) return
    setPageLoading(true); setPageError('')
    try {
      const page = await api<RepositoryPageView>(`/api/v2/projects/${encodeURIComponent(projectId)}/repositories?cursor=${encodeURIComponent(options.nextRepositoryCursor)}&limit=200`)
      const repositories = mergeRepositoryPages(options.repositories, page.repositories ?? [])
      const nextOptions = { ...options, repositories, nextRepositoryCursor: page.nextCursor ?? '' }
      setDrafts((current) => {
        const next = { ...current }
        for (const repository of repositories) {
          if (!next[repository.repoId]) next[repository.repoId] = resourceDraft(nextOptions, repository)
        }
        return next
      })
      setOptions(nextOptions)
    } catch (reason) { setPageError(message(reason)) } finally { setPageLoading(false) }
  }

  async function loadNamed(repoId: string) {
    if (namedLoading[repoId] || namedChecks[repoId]) return
    setNamedLoading((current) => ({ ...current, [repoId]: true }))
    setNamedErrors((current) => ({ ...current, [repoId]: '' }))
    try {
      const result = await api<{ checks: CheckCommandView[] }>(`/api/v2/projects/${encodeURIComponent(projectId)}/repositories/${encodeURIComponent(repoId)}/checks`)
      setNamedChecks((current) => ({ ...current, [repoId]: result.checks ?? [] }))
    } catch (reason) {
      setNamedErrors((current) => ({ ...current, [repoId]: message(reason) }))
    } finally {
      setNamedLoading((current) => ({ ...current, [repoId]: false }))
    }
  }

  async function loadBranches(repoId: string, after = '') {
    if (branchLoading[repoId]) return
    setBranchLoading((current) => ({ ...current, [repoId]: true }))
    setBranchErrors((current) => ({ ...current, [repoId]: '' }))
    try {
      const suffix = after ? `?after=${encodeURIComponent(after)}&limit=100` : '?limit=100'
      const page = await api<RepositoryBranchPageView>(`/api/v2/repositories/${encodeURIComponent(repoId)}/branches${suffix}`)
      setBranches((current) => {
        const prior = after ? current[repoId]?.branches ?? [] : []
        const known = new Set(prior.map((branch) => branch.ref))
        return { ...current, [repoId]: { branches: [...prior, ...(page.branches ?? []).filter((branch) => !known.has(branch.ref))], nextCursor: page.nextCursor ?? '' } }
      })
    } catch (reason) { setBranchErrors((current) => ({ ...current, [repoId]: message(reason) })) }
    finally { setBranchLoading((current) => ({ ...current, [repoId]: false })) }
  }

  async function loadDeliveryDefault(repoId: string) {
    if (defaultLoading[repoId] || deliveryDefaults[repoId]) return
    setDefaultLoading((current) => ({ ...current, [repoId]: true }))
    setDefaultErrors((current) => ({ ...current, [repoId]: '' }))
    try {
      const value = await api<RepositoryDeliveryDefaultsView>(`/api/v2/repositories/${encodeURIComponent(repoId)}/delivery-defaults`)
      setDeliveryDefaults((current) => ({ ...current, [repoId]: value }))
      if (value.targetRef) setDrafts((current) => current[repoId] && !current[repoId].targetRef ? ({ ...current, [repoId]: { ...current[repoId], targetRef: value.targetRef } }) : current)
    } catch (reason) { setDefaultErrors((current) => ({ ...current, [repoId]: message(reason) })) }
    finally { setDefaultLoading((current) => ({ ...current, [repoId]: false })) }
  }

  async function saveDeliveryDefault(repoId: string) {
    const current = deliveryDefaults[repoId]
    const targetRef = drafts[repoId]?.targetRef
    if (!current || !targetRef || defaultSaving[repoId]) return
    setDefaultSaving((values) => ({ ...values, [repoId]: true })); setDefaultErrors((values) => ({ ...values, [repoId]: '' }))
    try {
      const value = await api<RepositoryDeliveryDefaultsView>(`/api/v2/repositories/${encodeURIComponent(repoId)}/delivery-defaults`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('save-delivery-default') },
        body: JSON.stringify({ expectedVersion: current.version, targetRef }),
      })
      setDeliveryDefaults((values) => ({ ...values, [repoId]: value }))
    } catch (reason) { setDefaultErrors((values) => ({ ...values, [repoId]: message(reason) })) }
    finally { setDefaultSaving((values) => ({ ...values, [repoId]: false })) }
  }

  useEffect(() => {
    for (const [repoId, draft] of Object.entries(drafts)) {
      if (draft.selected && draft.checkMode === 'named' && !namedChecks[repoId] && !namedLoading[repoId] && !namedErrors[repoId]) {
        void loadNamed(repoId)
      }
    }
    // loadNamed is intentionally driven by the frozen draft transition.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [drafts, namedChecks, namedLoading, namedErrors])

  useEffect(() => {
    for (const [repoId, draft] of Object.entries(drafts)) {
      if (draft.selected && draft.role === 'write') {
        if (!branches[repoId] && !branchLoading[repoId] && !branchErrors[repoId]) void loadBranches(repoId)
        if (!deliveryDefaults[repoId] && !defaultLoading[repoId] && !defaultErrors[repoId]) void loadDeliveryDefault(repoId)
      }
    }
    // Branch discovery follows the selected writable repository transition.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [drafts, branches, branchLoading, branchErrors, deliveryDefaults, defaultLoading, defaultErrors])

  const preparation = useMemo(() => {
    const blocked = (reason: string) => ({ resources: undefined, reason })
    if (error) return blocked(t('Could not load task repositories: {reason}', { reason: error }))
    if (!options) return blocked(t('Loading repositories…'))
    if ((options.selectedRepoIds ?? []).some((repoId) => !options.repositories.some((repository) => repository.repoId === repoId))) return blocked(t('Load more repositories to review this Room’s complete default selection.'))
    const legacy = options.legacyScope as LegacyScope | undefined
    const selected = options.repositories.filter((repository) => drafts[repository.repoId]?.selected)
    if (selected.length === 0) return { resources: [] as TaskResourceSelection[], reason: t('Select at least one repository for this task.') }
    if (!selected.some((repository) => drafts[repository.repoId].role === 'write')) return blocked(t('Set at least one selected repository to Can modify.'))
    const resources: TaskResourceSelection[] = []
    for (const repository of selected) {
      const draft = drafts[repository.repoId]
      const repoBlocked = (reason: string) => blocked(`${repository.name}: ${reason}`)
      if (!resourceAvailable(repository.availability) || repository.state !== 'active') return repoBlocked(repository.reason || t('Restore or reselect an available repository.'))
      if (namedLoading[repository.repoId]) return repoBlocked(t('Loading repository checks. Please wait.'))
      if (namedErrors[repository.repoId]) return repoBlocked(namedErrors[repository.repoId])
      if (checkSaving[repository.repoId]) return repoBlocked(t('Saving changes. Please wait.'))
      if (draft.checkMode === 'named' && checkSaveErrors[repository.repoId]) return repoBlocked(checkSaveErrors[repository.repoId])
      if (draft.role === 'write' && !draft.targetRef) return repoBlocked(t('Choose a target branch for this repository.'))
      if (draft.checkMode === 'named' && draft.commands.length === 0) return repoBlocked(t('Select or add at least one check, or choose automatic checks.'))
      const legacyRepository = legacy?.repoId === repository.repoId
      const legacySelected = legacyRepository && draft.scopeChoice === 'legacy'
      const restricted = !legacySelected && draft.modules.length > 0
      resources.push({
        repoId: repository.repoId,
        associationVersion: repository.version,
        role: draft.role,
        ...(draft.role === 'write' ? { targetRef: draft.targetRef } : {}),
        scope: legacySelected ? {
          mode: 'restricted', writableFiles: [...legacy!.writableFiles], writableDirectories: [...legacy!.writableDirectories], protectedDirectories: [], migrationChoice: 'legacy',
        } : restricted ? {
          mode: 'restricted', writableFiles: [], writableDirectories: [...draft.modules].sort(), protectedDirectories: [], migrationChoice: legacyRepository ? 'repository' : 'not_needed',
        } : {
          mode: 'repository', writableFiles: [], writableDirectories: [], protectedDirectories: [], migrationChoice: legacyRepository ? 'repository' : 'not_needed',
        },
        checks: {
          mode: draft.checkMode,
          commands: draft.checkMode === 'named' ? draft.commands : [],
          preparation: [],
          selectionSource: 'user',
        },
      })
    }
    return { resources, reason: '' }
  }, [options, drafts, namedLoading, namedErrors, checkSaving, checkSaveErrors, error, t])

  useEffect(() => { onReady(preparation.resources); onBlockedChange?.(preparation.reason) }, [preparation, onReady, onBlockedChange])

  function update(repoId: string, change: Partial<ResourceDraft>) {
    setDrafts((current) => ({ ...current, [repoId]: { ...current[repoId], ...change } }))
  }

  function updateCustom(repoId: string, change: Partial<ResourceDraft>) {
    update(repoId, change)
    setCheckSaveErrors((current) => ({ ...current, [repoId]: '' }))
  }

  function chooseNamedCheck(repoId: string, check: CheckCommandView, selected: boolean) {
    const draft = drafts[repoId]
    update(repoId, { commands: selected ? mergeChecks(draft.commands, [check]) : draft.commands.filter((candidate) => candidate.id !== check.id) })
  }

  async function addCustomCheck(repoId: string) {
    const draft = drafts[repoId]
    const name = draft.customName.trim()
    const command = draft.customCommand.trim()
    if (!name || !command || checkSaveRequests.current.has(repoId)) return
    const custom: CheckCommandView = {
      id: stableCustomID(repoId, name, command, draft.customDirectory), name, command, workingDirectory: draft.customDirectory,
      version: 0, source: 'user',
    }
    if (!draft.saveCustom) {
      updateCustom(repoId, { commands: mergeChecks(draft.commands, [custom]), customName: '', customCommand: '' })
      return
    }

    const context = { projectId, roomId }
    const controller = new AbortController()
    checkSaveRequests.current.set(repoId, controller)
    setCheckSaving((current) => ({ ...current, [repoId]: true }))
    setCheckSaveErrors((current) => ({ ...current, [repoId]: '' }))
    try {
      const result = await api<{ checks: CheckCommandView[] }>(`/api/v2/projects/${encodeURIComponent(projectId)}/repositories/${encodeURIComponent(repoId)}/checks`, {
        method: 'PUT', body: JSON.stringify({ checks: [custom] }), signal: controller.signal,
      })
      if (controller.signal.aborted || checkSaveRequests.current.get(repoId) !== controller || resourceContext.current.projectId !== context.projectId || resourceContext.current.roomId !== context.roomId) return
      const saved = (result.checks ?? []).find((check) => check.id === custom.id && check.version > 0 && check.source)
      if (!saved) throw new Error(t('Saved check response is incomplete.'))
      setNamedChecks((current) => ({ ...current, [repoId]: result.checks ?? [] }))
      setDrafts((current) => {
        const latest = current[repoId]
        if (!latest) return current
        return { ...current, [repoId]: {
          ...latest, commands: mergeChecks(latest.commands.filter((check) => check.id !== custom.id), [saved]),
          customName: '', customCommand: '', saveCustom: false,
        } }
      })
    } catch (reason) {
      if (!controller.signal.aborted && checkSaveRequests.current.get(repoId) === controller && resourceContext.current.projectId === context.projectId && resourceContext.current.roomId === context.roomId) {
        setCheckSaveErrors((current) => ({ ...current, [repoId]: message(reason) }))
      }
    } finally {
      if (checkSaveRequests.current.get(repoId) === controller) {
        checkSaveRequests.current.delete(repoId)
        if (resourceContext.current.projectId === context.projectId && resourceContext.current.roomId === context.roomId) {
          setCheckSaving((current) => ({ ...current, [repoId]: false }))
        }
      }
    }
  }

  if (!options && !error) return <section aria-label={t('Task repositories and checks')}><p role="status">{t('Loading task resources…')}</p></section>
  if (error) return <section aria-label={t('Task repositories and checks')}><p className="error-banner" role="alert">{error}</p></section>
  if (!options) return null

  const legacy = options.legacyScope as LegacyScope | undefined
  return <section className="task-resources" aria-label={t('Task repositories and checks')}>
    <h2>{t('Repositories for this task')}</h2>
    <p className="entry-help">{t('Only the repositories selected here belong to this task. Multi-repository Projects are never selected all at once automatically.')}</p>
    {(options.selectedRepoIds ?? []).some((repoId) => !options.repositories.some((repository) => repository.repoId === repoId)) && <p className="warning-banner" role="status">{t('Load more repositories to review this Room’s complete default selection.')}</p>}
    {options.repositories.length === 0 && <p className="warning-banner" role="status">{t('No repositories are available for this task.')}</p>}
    {options.repositories.map((repository) => {
      const draft = drafts[repository.repoId]
      if (!draft) return null
      const selectable = repository.state === 'active' && resourceAvailable(repository.availability)
      const resourceBusy = busy || Boolean(checkSaving[repository.repoId])
      const available = mergeChecks(repository.repoId === legacy?.repoId ? options.legacyChecks ?? [] : [], namedChecks[repository.repoId] ?? [])
      return <article className="panel task-resource-card" key={repository.repoId}>
        <label className="task-repository-choice">
          <input type="checkbox" aria-label={repository.name} checked={draft.selected} disabled={resourceBusy || !selectable} onChange={(event) => update(repository.repoId, { selected: event.target.checked })} />
          <span className="task-repository-identity"><strong>{repository.name}</strong><small>{repository.branch || t('Branch unavailable')}</small></span>
        </label>
        {!selectable && <p className="warning-banner">{repository.reason || t('This repository is unavailable for new work.')}</p>}
        {draft.selected && <>
          <fieldset disabled={resourceBusy}>
            <legend>{t('Repository role')}</legend>
            <label><input type="radio" name={`role-${repository.repoId}`} checked={draft.role === 'write'} onChange={() => update(repository.repoId, { role: 'write' })} />{t('Can modify')}</label>
            <label><input type="radio" name={`role-${repository.repoId}`} checked={draft.role === 'reference'} onChange={() => update(repository.repoId, { role: 'reference' })} />{t('Reference only')}</label>
          </fieldset>

          {draft.role === 'write' && <div>
            <label>{t('Target branch')}
              <select aria-label={`${repository.name} ${t('Target branch')}`} value={draft.targetRef} disabled={resourceBusy || branchLoading[repository.repoId] || defaultLoading[repository.repoId]} onChange={(event) => update(repository.repoId, { targetRef: event.target.value })}>
                {!draft.targetRef && <option value="">{t('Choose a target branch')}</option>}
                {draft.targetRef && !(branches[repository.repoId]?.branches ?? []).some((branch) => branch.ref === draft.targetRef) && <option value={draft.targetRef}>{draft.targetRef.replace(/^refs\/heads\//, '')}</option>}
                {(branches[repository.repoId]?.branches ?? []).map((branch) => <option key={branch.ref} value={branch.ref}>{branch.ref.replace(/^refs\/heads\//, '')} · {branch.commit.slice(0, 12)}</option>)}
              </select>
            </label>
            {branchLoading[repository.repoId] && <p role="status">{t('Loading branches…')}</p>}
            {defaultLoading[repository.repoId] && <p role="status">{t('Loading delivery default…')}</p>}
            {branchErrors[repository.repoId] && <p role="alert" className="error-banner">{branchErrors[repository.repoId]}</p>}
            {defaultErrors[repository.repoId] && <p role="alert" className="error-banner">{defaultErrors[repository.repoId]}</p>}
            {branches[repository.repoId]?.nextCursor && <ActionButton type="button" className="btn-secondary" disabled={resourceBusy || branchLoading[repository.repoId]} disabledReason={resourceBusy ? 'Wait for the current operation to finish.' : 'Loading repository branches. Please wait.'} onClick={() => void loadBranches(repository.repoId, branches[repository.repoId].nextCursor)}>{t('Load more branches')}</ActionButton>}
            {!deliveryDefaults[repository.repoId]?.targetRef && deliveryDefaults[repository.repoId]?.suggestedTargetRef && <ActionButton type="button" className="btn-secondary" disabled={resourceBusy || defaultSaving[repository.repoId]} disabledReason={resourceBusy ? 'Wait for the current operation to finish.' : 'Saving the repository default. Please wait.'} onClick={() => update(repository.repoId, { targetRef: deliveryDefaults[repository.repoId].suggestedTargetRef })}>{t('Use suggested branch for this task')}: {deliveryDefaults[repository.repoId].suggestedTargetRef.replace(/^refs\/heads\//, '')}</ActionButton>}
            {deliveryDefaults[repository.repoId] && draft.targetRef && draft.targetRef !== deliveryDefaults[repository.repoId].targetRef && <ActionButton type="button" className="quiet-action" disabled={resourceBusy || defaultSaving[repository.repoId]} disabledReason={resourceBusy ? 'Wait for the current operation to finish.' : 'Saving the repository default. Please wait.'} onClick={() => void saveDeliveryDefault(repository.repoId)}>{defaultSaving[repository.repoId] ? t('Saving…') : t('Save as repository default')}</ActionButton>}
            {deliveryDefaults[repository.repoId]?.reason && !deliveryDefaults[repository.repoId].targetRef && <p className="section-note">{deliveryDefaults[repository.repoId].reason}</p>}
            {!draft.targetRef && !branchLoading[repository.repoId] && <p role="status" className="warning-banner">{t('Choose a target branch for this repository.')}</p>}
            {repository.repoId === legacy?.repoId && <fieldset disabled={resourceBusy}>
              <legend>{t('Migration scope')}</legend>
              <label><input type="radio" name={`scope-${repository.repoId}`} checked={draft.scopeChoice === 'legacy'} onChange={() => update(repository.repoId, { scopeChoice: 'legacy', modules: [] })} />{t('Keep the previous file scope')}</label>
              <label><input type="radio" name={`scope-${repository.repoId}`} checked={draft.scopeChoice === 'repository'} onChange={() => update(repository.repoId, { scopeChoice: 'repository' })} />{t('Use the selected repository')}</label>
            </fieldset>}
            {draft.scopeChoice === 'repository' && <>
              <p className="entry-help">{draft.modules.length === 0 ? t('The selected repository is writable. Add an optional module limit only when needed.') : t('Only these modules are writable: {modules}', { modules: draft.modules.join(', ') })}</p>
              <ActionButton type="button" className="btn-secondary" disabled={resourceBusy} disabledReason={busy ? 'Wait for the current operation to finish.' : 'Saving changes. Please wait.'} onClick={() => setPicker({ repoId: repository.repoId, purpose: 'module' })}>{t('Choose module limit…')}</ActionButton>
              {draft.modules.length > 0 && <ActionButton type="button" className="quiet-action" disabled={resourceBusy} disabledReason={busy ? 'Wait for the current operation to finish.' : 'Saving changes. Please wait.'} onClick={() => update(repository.repoId, { modules: [] })}>{t('Clear module limits')}</ActionButton>}
            </>}
          </div>}

          <fieldset disabled={resourceBusy}>
            <legend>{t('Checks after the task')}</legend>
            <label><input type="radio" name={`checks-${repository.repoId}`} checked={draft.checkMode === 'auto'} onChange={() => update(repository.repoId, { checkMode: 'auto', commands: [] })} />{t('Choose relevant checks automatically')}</label>
            <label><input type="radio" name={`checks-${repository.repoId}`} checked={draft.checkMode === 'named'} onChange={() => update(repository.repoId, { checkMode: 'named' })} />{t('Use selected checks')}</label>
            <label><input type="radio" name={`checks-${repository.repoId}`} checked={draft.checkMode === 'none'} onChange={() => update(repository.repoId, { checkMode: 'none', commands: [] })} />{t('Run no checks · Unverified')}</label>
          </fieldset>

          {draft.checkMode === 'auto' && <p className="entry-help">{t('Automatic discovery supports package scripts, Make targets, Go, pytest, and Maven in committed configuration. The Agent chooses relevant checks and may propose others. Unrecognized or unobserved checks remain unverified; preparation is separate.')}</p>}

          {draft.checkMode === 'named' && <div className="task-named-checks">
            {namedLoading[repository.repoId] && <p role="status">{t('Loading named checks…')}</p>}
            {namedErrors[repository.repoId] && <p className="error-banner" role="alert">{namedErrors[repository.repoId]}</p>}
            {available.map((check) => <label key={check.id}>
              <input type="checkbox" disabled={resourceBusy} checked={draft.commands.some((selected) => selected.id === check.id)} onChange={(event) => chooseNamedCheck(repository.repoId, check, event.target.checked)} />
              <span>{check.name} <code>{check.workingDirectory || '.'}: {check.command}</code></span>
            </label>)}
            {draft.commands.filter((check) => check.source === 'user' && check.version === 0).map((check) => <div key={check.id}>
              <span>{check.name} · <code>{check.workingDirectory}: {check.command}</code></span>
              <ActionButton type="button" className="quiet-action" disabled={resourceBusy} disabledReason={busy ? 'Wait for the current operation to finish.' : 'Saving changes. Please wait.'} onClick={() => chooseNamedCheck(repository.repoId, check, false)}>{t('Remove')}</ActionButton>
            </div>)}
            <label>{t('Custom check name')}<input disabled={resourceBusy} value={draft.customName} onChange={(event) => updateCustom(repository.repoId, { customName: event.target.value })} /></label>
            <label>{t('Command')}<input disabled={resourceBusy} value={draft.customCommand} onChange={(event) => updateCustom(repository.repoId, { customCommand: event.target.value })} placeholder="npm run test" /></label>
            <p>{t('Working directory: {directory}', { directory: draft.customDirectory })}</p>
            <ActionButton type="button" className="btn-secondary" disabled={resourceBusy} disabledReason={busy ? 'Wait for the current operation to finish.' : 'Saving changes. Please wait.'} onClick={() => setPicker({ repoId: repository.repoId, purpose: 'check' })}>{t('Choose check directory…')}</ActionButton>
            <label><input type="checkbox" disabled={resourceBusy} checked={draft.saveCustom} onChange={(event) => updateCustom(repository.repoId, { saveCustom: event.target.checked })} />{t('Save this check to the repository')}</label>
            {checkSaveErrors[repository.repoId] && <p className="error-banner" role="alert">{checkSaveErrors[repository.repoId]}</p>}
            {checkSaving[repository.repoId] && <p role="status">{t('Saving check…')}</p>}
            <ActionButton type="button" className="btn-secondary" disabled={resourceBusy || namedLoading[repository.repoId] || !draft.customName.trim() || !draft.customCommand.trim()} disabledReason={resourceBusy ? 'Wait for the current operation to finish.' : namedLoading[repository.repoId] ? 'Loading repository checks. Please wait.' : !draft.customName.trim() ? 'Enter a check name.' : 'Enter the check command.'} onClick={() => void addCustomCheck(repository.repoId)}>{t('Add custom check')}</ActionButton>
          </div>}

          {picker?.repoId === repository.repoId && <RepositoryDirectoryPicker
            projectId={projectId} repoId={repository.repoId} disabled={resourceBusy}
            onClose={() => setPicker(undefined)}
            onSelect={(directory) => {
              if (picker.purpose === 'module') update(repository.repoId, { modules: directory === '.' ? [] : mergeStrings(draft.modules, directory) })
              else update(repository.repoId, { customDirectory: directory })
              setPicker(undefined)
            }}
          />}
        </>}
      </article>
    })}
    {options.repositories.some((repository) => drafts[repository.repoId]?.selected) && !options.repositories.some((repository) => drafts[repository.repoId]?.selected && drafts[repository.repoId].role === 'write') && <p className="warning-banner" role="status">{t('Coding tasks need at least one repository that Pi can modify.')}</p>}
    {options.repositories.length > 0 && !options.repositories.some((repository) => drafts[repository.repoId]?.selected) && <p className="warning-banner" role="status">{t('Select at least one repository.')}</p>}
    {pageError && <p className="error-banner" role="alert">{pageError}</p>}
    {options.nextRepositoryCursor && <ActionButton type="button" className="btn-secondary" disabled={busy || pageLoading} disabledReason={busy ? 'Wait for the current operation to finish.' : 'Loading repositories…'} onClick={() => void loadMoreRepositories()}>{pageLoading ? t('Loading repositories…') : t('Load more repositories')}</ActionButton>}
  </section>
}

function mergeStrings(values: string[], value: string) {
  return values.includes(value) ? values : [...values, value]
}
