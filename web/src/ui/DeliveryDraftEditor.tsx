import { useEffect, useId, useRef, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import { ActionButton } from './ActionButton'

export type DeliveryText = { message: string; title: string; body: string }
type DraftContext = { fingerprint: string; owner: string; templates: string[]; warnings: string[] }
type Suggestion = DeliveryText & { fingerprint: string; provider: string; model: string }
type PriorDraft = { value: DeliveryText; fingerprint: string }
type SavedDraft = { value: DeliveryText; fingerprint: string; template: string; source: string; previous?: PriorDraft }
type Props = {
  runId: string; repoId: string; resultDigest: string; expectedVersion: number; kind: 'commit' | 'pr'; refreshToken: number
  value: DeliveryText; disabled: boolean; disabledReason: string
  onChange: (value: DeliveryText) => void; onReady: (ready: boolean) => void
}
const empty: DeliveryText = { message: '', title: '', body: '' }
function validText(value: unknown): value is DeliveryText {
  if (!value || typeof value !== 'object') return false
  const v = value as DeliveryText
  return typeof v.message === 'string' && v.message.length <= 8192 && typeof v.title === 'string' && v.title.length <= 256 && typeof v.body === 'string' && v.body.length <= 32768
}
function hasText(kind: Props['kind'], value: DeliveryText) { return Boolean(kind === 'commit' ? value.message.trim() : value.title.trim() || value.body.trim()) }

export function DeliveryDraftEditor(props: Props) {
  const { t } = useI18n()
  const id = useId()
  const latest = useRef(props); latest.current = props
  const [value, setValue] = useState<DeliveryText>(props.value)
  const current = useRef(value)
  const [context, setContext] = useState<DraftContext>()
  const contextRef = useRef<DraftContext | undefined>(undefined)
  const [loading, setLoading] = useState(true)
  const [generating, setGenerating] = useState(false)
  const [error, setError] = useState('')
  const [storageError, setStorageError] = useState(false)
  const [stale, setStale] = useState(false)
  const staleRef = useRef(false)
  const [source, setSource] = useState('')
  const [template, setTemplate] = useState('')
  const templateRef = useRef('')
  const [feedback, setFeedback] = useState('')
  const [candidate, setCandidate] = useState<Suggestion>()
  const [previous, setPrevious] = useState<PriorDraft>()
  const saved = useRef<SavedDraft>({ value: props.value, fingerprint: '', template: '', source: '' })
  const storageKey = useRef('')
  const edits = useRef(0)
  const request = useRef<AbortController | undefined>(undefined)
  const retry = useRef<{ generationId: string; feedback: string; current: DeliveryText; template: string } | undefined>(undefined)

  function persist() {
    if (!storageKey.current) return
    try { window.localStorage.setItem(storageKey.current, JSON.stringify(saved.current)); setStorageError(false) }
    catch { setStorageError(true) }
  }
  function update(value: DeliveryText, source: string, fingerprint = saved.current.fingerprint) {
    current.current = value; setValue(value); setSource(source)
    saved.current = { ...saved.current, value, source, fingerprint, template: templateRef.current }
    latest.current.onChange(value); persist()
  }
  function markStale(value: boolean) { staleRef.current = value; setStale(value) }
  function change(change: Partial<DeliveryText>) { edits.current += 1; update({ ...current.current, ...change }, '') }

  async function generate(ctx: DraftContext, options?: { regenerate?: boolean; revise?: boolean; retry?: boolean }) {
    request.current?.abort()
    const controller = new AbortController(); request.current = controller
    const version = edits.current
    const wasEmpty = !hasText(props.kind, current.current)
    const replaceable = Boolean(saved.current.source) && !staleRef.current
    const parameters = options?.retry && retry.current ? retry.current : {
      generationId: options?.regenerate ? commandKey('delivery-draft') : '', feedback: options?.revise ? feedback.trim() : '',
      current: wasEmpty ? empty : { message: current.current.message, title: current.current.title, body: current.current.body }, template: templateRef.current,
    }
    retry.current = parameters
    setGenerating(true); setError(''); setCandidate(undefined)
    try {
      const suggestion = await api<Suggestion>(`/api/v2/runs/${encodeURIComponent(props.runId)}/delivery/message`, {
        method: 'POST', signal: controller.signal, headers: { 'Content-Type': 'application/json', 'Idempotency-Key': parameters.generationId || commandKey('delivery-draft-auto') },
        body: JSON.stringify({ repoId: props.repoId, expectedVersion: latest.current.expectedVersion, resultDigest: props.resultDigest,
          kind: props.kind, language: 'en', fingerprint: ctx.fingerprint, ...parameters }),
      })
      if (controller.signal.aborted) return
      const text = { message: suggestion.message ?? '', title: suggestion.title ?? '', body: suggestion.body ?? '' }
      if (!validText(text) || !hasText(props.kind, text) || suggestion.fingerprint !== ctx.fingerprint) throw new Error('Could not load a complete draft for the current changes.')
      const result = { ...suggestion, ...text }
      retry.current = undefined
      if (edits.current === version && (wasEmpty || replaceable) && !latest.current.disabled && !staleRef.current) {
        if (!wasEmpty) {
          const prior = { value: { ...current.current }, fingerprint: saved.current.fingerprint }
          setPrevious(prior); saved.current.previous = prior
        }
        update(text, [suggestion.provider, suggestion.model].filter(Boolean).join(' / '), ctx.fingerprint); markStale(false)
      } else { setCandidate(result) }
    } catch (reason) { if (!controller.signal.aborted) setError(message(reason)) }
    finally { if (!controller.signal.aborted) setGenerating(false) }
  }

  useEffect(() => {
    const controller = new AbortController()
    request.current?.abort(); request.current = controller
    setLoading(true); setGenerating(false); setError(''); setCandidate(undefined)
    const editVersion = edits.current
    void api<DraftContext>(`/api/v2/runs/${encodeURIComponent(props.runId)}/delivery/draft-context`, {
      method: 'POST', signal: controller.signal, headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('delivery-draft-context') },
      body: JSON.stringify({ repoId: props.repoId, expectedVersion: latest.current.expectedVersion, resultDigest: props.resultDigest, kind: props.kind }),
    }).then((ctx) => {
      if (controller.signal.aborted) return
      if (!ctx.fingerprint || !ctx.owner || !Array.isArray(ctx.templates)) throw new Error('Could not load the current draft sources.')
      contextRef.current = ctx; setContext(ctx)
      storageKey.current = `chora.delivery-draft.v1:${ctx.owner}:${props.runId}:${props.repoId}:${props.resultDigest}:${props.kind}`
      if (edits.current === editVersion && !hasText(props.kind, current.current)) {
        try {
          const raw = window.localStorage.getItem(storageKey.current)
          const stored = raw ? JSON.parse(raw) as SavedDraft : undefined
          if (stored && validText(stored.value) && typeof stored.fingerprint === 'string' && typeof stored.template === 'string') {
            saved.current = stored
            if (stored.previous && validText(stored.previous.value) && typeof stored.previous.fingerprint === 'string') setPrevious(stored.previous)
            templateRef.current = ctx.templates.includes(stored.template) ? stored.template : ''
            update(stored.value, stored.source || '', stored.fingerprint)
          }
        } catch { setStorageError(true) }
      }
      if (!templateRef.current && ctx.templates.length === 1) templateRef.current = ctx.templates[0]
      setTemplate(templateRef.current)
      const outdated = hasText(props.kind, current.current) && Boolean(saved.current.fingerprint) && saved.current.fingerprint !== ctx.fingerprint
      markStale(outdated)
      if (!saved.current.fingerprint) update(current.current, '', ctx.fingerprint)
      setLoading(false)
      if (!hasText(props.kind, current.current) && (props.kind !== 'pr' || ctx.templates.length <= 1)) void generate(ctx)
    }).catch((reason) => { if (!controller.signal.aborted) { contextRef.current = undefined; setContext(undefined); setError(message(reason)) } })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => { controller.abort(); request.current?.abort() }
    // Locale changes and polling must not discard edits or regenerate text.
  }, [props.runId, props.repoId, props.resultDigest, props.kind, props.refreshToken])

  useEffect(() => { latest.current.onReady(Boolean(context) && !loading && !generating && !stale) }, [context, loading, generating, stale])

  function adopt() {
    if (!candidate || !contextRef.current) return
    const prior = { value: { ...current.current }, fingerprint: saved.current.fingerprint }; setPrevious(prior); saved.current.previous = prior
    edits.current += 1
    update({ message: candidate.message, title: candidate.title, body: candidate.body }, [candidate.provider, candidate.model].filter(Boolean).join(' / '), candidate.fingerprint)
    markStale(false); setCandidate(undefined)
  }
  const blocked = props.disabled || loading || generating || !context || (props.kind === 'pr' && context.templates.length > 1 && !template)
  const reason = props.disabled ? props.disabledReason : loading || generating ? 'Preparing the delivery draft. Please wait.' : !context ? 'Refresh to load the current draft sources.' : 'Choose a pull request template.'
  const generateLabel = props.kind === 'commit' ? 'Generate commit message' : 'Generate pull request description'
  return <div className="commit-message-editor delivery-draft-editor">
    <p className="section-note">{t('AI drafts in English. You can edit everything before confirming.')}</p>
    {props.kind === 'commit' ? <div><label htmlFor={`${id}-message`}>{t('Commit message')}</label><textarea id={`${id}-message`} rows={8} maxLength={8192} value={value.message} disabled={props.disabled} onChange={(event) => change({ message: event.target.value })} /></div> : <>
      <div><label htmlFor={`${id}-title`}>{t('Title')}</label><input id={`${id}-title`} style={{ width: '100%', boxSizing: 'border-box' }} maxLength={256} value={value.title} disabled={props.disabled} onChange={(event) => change({ title: event.target.value })} /></div>
      <div><label htmlFor={`${id}-body`}>{t('Description')}</label><textarea id={`${id}-body`} rows={12} maxLength={32768} value={value.body} disabled={props.disabled} onChange={(event) => change({ body: event.target.value })} /></div>
    </>}
    {props.kind === 'pr' && Boolean(context?.templates.length) && <label>{t('Pull request template')}<select value={template} disabled={props.disabled || generating} onChange={(event) => {
      templateRef.current = event.target.value; setTemplate(event.target.value); saved.current.template = event.target.value; persist()
      setCandidate(undefined); retry.current = undefined
      if (hasText(props.kind, current.current)) markStale(true)
      else if (contextRef.current && event.target.value) void generate(contextRef.current)
    }}><option value="">{t('Choose a pull request template.')}</option>{context?.templates.map((name) => <option key={name} value={name}>{name}</option>)}</select></label>}
    <div className="form-actions">
      <ActionButton type="button" className="btn-secondary" disabled={blocked} disabledReason={reason} onClick={() => context && void generate(context, { regenerate: true })}>{t(generateLabel)}</ActionButton>
      {previous && <ActionButton type="button" className="delivery-text-action" disabled={props.disabled || generating} disabledReason={reason} onClick={() => { edits.current += 1; update(previous.value, '', previous.fingerprint); markStale(previous.fingerprint !== context?.fingerprint); setPrevious(undefined); delete saved.current.previous; persist() }}>{t('Undo draft replacement')}</ActionButton>}
    </div>
    <details><summary>{t('Ask AI to revise')}</summary><div className="delivery-revision-fields"><label htmlFor={`${id}-feedback`}>{t('Revision instructions')}</label><textarea id={`${id}-feedback`} rows={2} maxLength={4096} value={feedback} disabled={props.disabled || generating} onChange={(event) => setFeedback(event.target.value)} placeholder={t('For example: emphasize compatibility changes')} />
      <div className="form-actions"><ActionButton type="button" className="btn-secondary" disabled={blocked || !feedback.trim()} disabledReason={blocked ? reason : 'Enter revision instructions.'} onClick={() => context && void generate(context, { regenerate: true, revise: true })}>{t('Revise draft')}</ActionButton></div>
    </div>
    </details>
    {(loading || generating) && <small role="status">{t(generating ? 'Generating a draft from reviewed changes…' : 'Loading draft sources…')}</small>}
    {source && !generating && <small className="section-note">{t('Drafted from reviewed changes · {source}', { source })}</small>}
    {storageError && <small role="status">{t('Draft could not be saved in this browser. Keep this page open or copy your text.')}</small>}
    {context?.warnings.map((warning) => <small className="warning-banner" key={warning}>{warning}</small>)}
    {error && <div role="alert" className="warning-banner"><p>{error}</p>{context && retry.current && <ActionButton type="button" disabled={blocked} disabledReason={reason} onClick={() => void generate(context, { retry: true })}>{t('Retry draft generation')}</ActionButton>}</div>}
    {stale && <div role="status" className="warning-banner"><p>{t('The draft sources changed. Generate a new candidate or review and keep your edited draft.')}</p>
      <ActionButton type="button" disabled={props.disabled || generating || !context} disabledReason={reason} onClick={() => { update(current.current, '', context!.fingerprint); markStale(false) }}>{t('Keep my draft for these changes')}</ActionButton>
    </div>}
    {candidate && <div className="delivery-preview" role="group" aria-label={t('New draft candidate')}>
      <strong>{t('New draft candidate')}</strong><pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{props.kind === 'commit' ? candidate.message : `${candidate.title}\n\n${candidate.body}`}</pre>
      <div className="form-actions"><ActionButton type="button" disabled={props.disabled} disabledReason={props.disabledReason} onClick={adopt}>{t('Use this draft')}</ActionButton><button type="button" disabled={props.disabled} onClick={() => setCandidate(undefined)}>{t('Keep current draft')}</button></div>
    </div>}
  </div>
}
