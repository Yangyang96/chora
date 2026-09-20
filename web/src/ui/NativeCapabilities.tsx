import { useEffect, useRef, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import { ActionButton } from './ActionButton'
import { NativeCapabilityStatus } from './NativeCapabilityStatus'

export type NativeCapabilitiesConfig = {
  version: number
  skillPaths: string[]
  disabledSkillPaths: string[]
  bridgePath: string
  mcpConfigPath: string
  disabledMcpServers: string[]
}

const emptyConfig = (): NativeCapabilitiesConfig => ({
  version: 0,
  skillPaths: [],
  disabledSkillPaths: [],
  bridgePath: '',
  mcpConfigPath: '',
  disabledMcpServers: [],
})

function splitLines(value: string) {
  return value.split('\n').map((item) => item.trim()).filter(Boolean)
}

export function NativeCapabilities({ projectId, editable }: { projectId: string; editable: boolean }) {
  const { t } = useI18n()
  const currentProjectId = useRef(projectId)
  currentProjectId.current = projectId
  const [open, setOpen] = useState(false)
  const [config, setConfig] = useState<NativeCapabilitiesConfig>(emptyConfig)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    setConfig(emptyConfig())
    setLoaded(false)
    setLoading(false)
    setSaving(false)
    setError('')
  }, [projectId])

  useEffect(() => {
    if (!open) return
    const requestedProjectId = projectId
    const controller = new AbortController()
    let cancelled = false
    setLoading(true)
    setError('')
    void api<NativeCapabilitiesConfig>(`/api/projects/${encodeURIComponent(requestedProjectId)}/capabilities/config`, { signal: controller.signal })
      .then((next) => {
        if (!cancelled && currentProjectId.current === requestedProjectId) {
          setConfig({ ...emptyConfig(), ...next })
          setLoaded(true)
        }
      })
      .catch((reason) => {
        if (!cancelled && currentProjectId.current === requestedProjectId) setError(message(reason))
      })
      .finally(() => {
        if (!cancelled && currentProjectId.current === requestedProjectId) setLoading(false)
      })
    return () => {
      cancelled = true
      controller.abort()
    }
  }, [open, projectId])

  async function save() {
    const requestedProjectId = projectId
    setSaving(true)
    setError('')
    try {
      const next = await api<NativeCapabilitiesConfig>(`/api/projects/${encodeURIComponent(requestedProjectId)}/capabilities/config`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('save-native-capabilities') },
        body: JSON.stringify({ ...config,
          skillPaths: splitLines(config.skillPaths.join('\n')),
          disabledSkillPaths: splitLines(config.disabledSkillPaths.join('\n')),
          disabledMcpServers: splitLines(config.disabledMcpServers.join('\n')),
        }),
      })
      if (currentProjectId.current === requestedProjectId) {
        setConfig({ ...emptyConfig(), ...next })
        setLoaded(true)
      }
    } catch (reason) {
      if (currentProjectId.current === requestedProjectId) setError(message(reason))
    } finally {
      if (currentProjectId.current === requestedProjectId) setSaving(false)
    }
  }

  const update = <K extends keyof NativeCapabilitiesConfig,>(key: K, value: NativeCapabilitiesConfig[K]) => {
    setConfig((current) => ({ ...current, [key]: value }))
  }

  return <details className="panel topic-room-form" open={open} onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary><strong>{t('Skills and MCP')}</strong> · {t('Local Connected only')}</summary>
    <p className="entry-help">{t('New Local Connected executions inherit global Pi resources unless this Project overrides them here. Existing executions stay unchanged.')}</p>
    <p className="entry-help">{t('Enter references to existing local resources. Chora stores paths and server names only; it does not install packages or store credentials here.')}</p>
    {loading && <p role="status">{t('Loading native capabilities…')}</p>}
    {error && <p className="error-banner" role="alert">{error}</p>}
    <label>{t('Configuration version')}<input readOnly value={config.version} aria-label={t('Configuration version')} /></label>
    <label>{t('Local Skill paths')}<textarea value={config.skillPaths.join('\n')} disabled={!editable || !loaded || loading || saving} onChange={(event) => update('skillPaths', event.target.value.split('\n'))} placeholder="/absolute/path/to/skill" /></label>
    <label>{t('Disabled Skill paths')}<textarea value={config.disabledSkillPaths.join('\n')} disabled={!editable || !loaded || loading || saving} onChange={(event) => update('disabledSkillPaths', event.target.value.split('\n'))} placeholder="/absolute/path/to/skill" /></label>
    <label>{t('Installed bridge package path (optional)')}<input value={config.bridgePath} maxLength={4096} disabled={!editable || !loaded || loading || saving} onChange={(event) => update('bridgePath', event.target.value)} placeholder="/absolute/path/to/bridge" /></label>
    <label>{t('MCP config path (optional)')}<input value={config.mcpConfigPath} maxLength={4096} disabled={!editable || !loaded || loading || saving} onChange={(event) => update('mcpConfigPath', event.target.value)} placeholder="/absolute/path/to/mcp.json" /></label>
    <label>{t('Disabled MCP server names')}<textarea value={config.disabledMcpServers.join('\n')} disabled={!editable || !loaded || loading || saving} onChange={(event) => update('disabledMcpServers', event.target.value.split('\n'))} placeholder="server-name" /></label>
    {!editable && <p className="entry-help">{t('Restore the Project before editing native capabilities.')}</p>}
    <div className="form-actions">
      <ActionButton type="button" className="btn-primary" disabled={!editable || loading || saving || !loaded} disabledReason={!editable ? 'Restore the Project before editing native capabilities.' : loading || !loaded ? 'Loading data. Please wait.' : 'Saving changes. Please wait.'} onClick={() => void save()}>{saving ? t('Saving…') : t('Save Skills and MCP')}</ActionButton>
    </div>
    {open && loaded && <NativeCapabilityStatus projectId={projectId} configVersion={config.version} />}
  </details>
}
