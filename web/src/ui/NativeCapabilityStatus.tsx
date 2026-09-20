import { useEffect, useRef, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'

type Capability = { name: string; path?: string; source: string; enabled: boolean; status: string; toolCount?: number }
type Inventory = { skills: Capability[]; servers: Capability[]; bridge: { status: string; version: string }; missingPackages: number; diagnostics: { path: string; status: string }[] }
type Observation = { attemptID: string; configVersion: number; observedAt: string; running: boolean; skills: Capability[]; servers: Capability[] }
type Status = { inventory?: Inventory; observations: Observation[]; warning?: string }

export function NativeCapabilityStatus({ projectId, configVersion }: { projectId: string; configVersion: number }) {
  const { t } = useI18n()
  const [value, setValue] = useState<Status>()
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  const [verification, setVerification] = useState<Observation>()
  const [verifying, setVerifying] = useState(false)
  const scope = `${projectId}:${configVersion}`
  const currentScope = useRef(scope)
  currentScope.current = scope
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  useEffect(() => {
    let active = true
    const controller = new AbortController()
    setValue(undefined); setError(''); setVerification(undefined); setVerifying(false)
    api<Status>(`/api/projects/${encodeURIComponent(projectId)}/capabilities/status`, { signal: controller.signal }).then(next => { if (active) setValue(next) }).catch(reason => { if (active) setError(message(reason)) })
    return () => { active = false; controller.abort() }
  }, [projectId, configVersion, revision])
  async function verify() {
    const requestedScope = scope
    setVerifying(true); setError('')
    try {
      const next = await api<Observation>(`/api/projects/${encodeURIComponent(projectId)}/capabilities/verify`, { method: 'POST', headers: { 'Idempotency-Key': commandKey('verify-capabilities') } })
      if (mounted.current && currentScope.current === requestedScope) setVerification(next)
    } catch (reason) { if (mounted.current && currentScope.current === requestedScope) setError(message(reason)) }
    finally { if (mounted.current && currentScope.current === requestedScope) setVerifying(false) }
  }
  const rows = (items: Capability[]) => <ul>{items.map(item => <li key={`${item.name}:${item.path ?? ''}`}><strong>{item.name}</strong> — {t(item.status)} · {t(item.source)}{item.path && <small> · {item.path}</small>}{['failed', 'needs-auth', 'unavailable'].includes(item.status) && <p className="warning-banner">{t('Unavailable. Repair the existing configuration or authentication, then retry verification. Other capabilities can still be used.')}</p>}</li>)}</ul>
  return <section aria-label={t('Capability status')}>
    <h3>{t('Capability status')}</h3>
    <p className="entry-help">{t('Discovery reads configuration only. Discovered or cached capabilities are not verified connections. Execution observations describe that execution only.')}</p>
    <button type="button" className="btn-secondary" disabled={verifying} onClick={() => setRevision(value => value + 1)}>{t('Refresh status')}</button>
    <button type="button" className="btn-secondary" disabled={verifying} onClick={() => void verify()}>{t(verifying ? 'Verifying…' : 'Verify / retry connections')}</button>
    <p className="entry-help">{t('Verification starts configured MCP servers in a temporary Pi session without a model call. It does not reload an existing execution.')}</p>
    {error && <p role="alert">{error}</p>}
    {value?.warning && <p className="warning-banner" role="status">{value.warning}</p>}
    {value?.inventory && <>
      <p>{t('MCP bridge')}: {t(value.inventory.bridge.status)} {value.inventory.bridge.version}</p>
      {['missing', 'unavailable'].includes(value.inventory.bridge.status) && <p>{t('Install pi-mcp-adapter 2.34.0 in Pi, then enter its local package path or refresh discovery.')}</p>}
      {rows([...value.inventory.skills, ...value.inventory.servers])}
      {value.inventory.diagnostics.map((item, index) => <p key={index} role="status">{item.path} — {t('unavailable')}</p>)}
    </>}
    {value?.observations.map(item => <details key={item.attemptID}><summary>{item.attemptID} · {t('Configuration version')} {item.configVersion} · {item.observedAt} · {t(item.running ? 'Running' : 'Last observed')}</summary>{rows([...item.skills, ...item.servers])}</details>)}
    {verification && <section aria-label={t('Verification result')}><h4>{t('Verification result')}</h4><p>{verification.observedAt}</p>{rows([...verification.skills, ...verification.servers])}</section>}
  </section>
}
