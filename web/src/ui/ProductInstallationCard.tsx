import { ActionButton } from './ActionButton'
import { useEffect, useState } from 'react'
import { api, commandKey } from '../api'
import type { ProductInstallationDoctor } from '../types'
import { parseProductInstallationDoctor } from './productInstallationContract'

const DOCTOR_PATH = '/api/product-installation/doctor'

type LifecycleAction = 'setup' | 'upgrade' | 'gc' | 'uninstall'

function installationActionReason(doctor: ProductInstallationDoctor, action: LifecycleAction): string {
  const reasons: Record<string, string> = {
    observation_unavailable: 'Refresh Doctor after restoring the installation status source.',
    identity_mismatch: 'Resolve the installation identity mismatch shown in Doctor.',
    authority_required: 'Complete the installation acknowledgement before continuing.',
    idempotency_conflict: 'Refresh Doctor to reconcile the previous installation request.',
    installation_busy: 'Wait for the current installation operation to finish.',
    capability_probe_failed: 'Resolve the failed Engine capability check shown in Doctor.',
    generation_referenced: 'The installation assets are still in use. Finish the tasks using them first.',
    asset_identity_conflict: 'Resolve the conflicting asset identity shown in Doctor.',
    invalid_request: 'Correct the installation request shown in Doctor and check again.',
    operation_failed: 'Resolve the installation failure shown in Doctor and check again.',
  }
  if (doctor.reasonCode) return reasons[doctor.reasonCode]
  if (!doctor.engine.ready) return 'Complete the Engine readiness checks shown in Doctor.'
  if (action === 'setup' && doctor.activeGenerationId) return 'Chora is already installed. Use Upgrade to change its version.'
  if (action === 'upgrade' && !doctor.candidateGenerationId) return 'No upgrade candidate is available. Refresh Doctor after preparing one.'
  if (action === 'uninstall' && !doctor.activeGenerationId) return 'No installed Chora generation is available to uninstall.'
  return 'Doctor has not enabled this action. Refresh Doctor and resolve the reported readiness checks.'
}

export function ProductInstallationCard() {
  const [doctor, setDoctor] = useState<ProductInstallationDoctor | null>(null)
  const [unavailable, setUnavailable] = useState(false)
  const [busy, setBusy] = useState(false)
  const [dialog, setDialog] = useState<LifecycleAction | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [engineToolsConfirmed, setEngineToolsConfirmed] = useState(false)
  const [colimaVMConfirmed, setColimaVMConfirmed] = useState(false)
  const [gcLimit, setGCLimit] = useState(8)
  const [notice, setNotice] = useState('')

  async function refreshDoctor() {
    const parsed = parseProductInstallationDoctor(await api<unknown>(DOCTOR_PATH))
    if (parsed === null) throw new Error('invalid public Doctor report')
    setDoctor(parsed)
    setUnavailable(false)
    return parsed
  }

  useEffect(() => {
    refreshDoctor().catch(() => {
      setDoctor(null)
      setUnavailable(true)
    })
  }, [])

  function openDialog(action: LifecycleAction) {
    setDialog(action)
    setConfirmed(false)
    setEngineToolsConfirmed(false)
    setColimaVMConfirmed(false)
    setGCLimit(8)
    setNotice('')
  }

  function closeDialog() {
    if (!busy) setDialog(null)
  }

  async function performAction() {
    if (dialog === null || doctor === null || !doctor.actions[dialog]) return
    if (dialog === 'setup' && (!engineToolsConfirmed || !colimaVMConfirmed)) return
    if (dialog !== 'setup' && !confirmed) return
    const body = dialog === 'setup'
      ? { confirmation: 'setup', confirmPinnedEngineTools: true, confirmColimaVM: true }
      : dialog === 'gc'
        ? { confirmation: 'gc', limit: gcLimit }
        : { confirmation: dialog }
    setBusy(true)
    setNotice('')
    try {
      const result = parseProductInstallationDoctor(await api<unknown>(`/api/product-installation/${dialog}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey(`product-${dialog}`) },
        body: JSON.stringify(body),
      }))
      if (result === null) throw new Error('invalid public operation report')
      const refreshed = await refreshDoctor()
      const completed = {
        ...refreshed,
        replayed: result.replayed || refreshed.replayed,
        restartRequired: result.restartRequired || refreshed.restartRequired,
      }
      setDoctor(completed)
      setNotice(completed.restartRequired
        ? 'Action completed. Restart Chora to use the active installation.'
        : 'Action completed. Doctor report refreshed.')
      setDialog(null)
    } catch {
      setNotice('The product installation action could not be completed safely.')
    } finally {
      setBusy(false)
    }
  }

  const stateLabel = doctor === null ? (unavailable ? 'Unavailable' : 'Checking…') : doctor.status === 'ready' ? 'Ready' : doctor.status === 'completed' ? 'Completed' : 'Setup needed'
  return (
    <details className={`product-installation ${doctor?.status === 'blocked' || unavailable ? 'blocked' : ''}`}>
      <summary>Installed product · {stateLabel}</summary>
      <section className="product-installation-card" aria-label="Installed product Doctor">
        <div className="product-installation-heading">
          <strong>Installed product Doctor</strong>
          <ActionButton className="btn-secondary" type="button" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={() => void refreshDoctor().catch(() => { setDoctor(null); setUnavailable(true) })}>Refresh Doctor</ActionButton>
        </div>
        {doctor === null ? (
          <p className="product-installation-unavailable">{unavailable ? 'Product installation status is unavailable. Actions are disabled.' : 'Checking the installed product…'}</p>
        ) : (
          <>
            <dl className="product-installation-facts">
              <div><dt>Status</dt><dd>{stateLabel}</dd></div>
              <div><dt>Reason</dt><dd>{doctor.reasonCode || 'none'}</dd></div>
              <div><dt>Engine</dt><dd>{doctor.engine.ready ? 'Ready' : 'Missing'}</dd></div>
              <div><dt>Engine API</dt><dd>{doctor.engine.apiVersion || 'not observed'}</dd></div>
              <div><dt>Engine system</dt><dd>{[doctor.engine.operatingSystem, doctor.engine.architecture].filter(Boolean).join(' · ') || 'not observed'}</dd></div>
              <div><dt>Engine context</dt><dd>{doctor.engine.contextName || 'not observed'}</dd></div>
              <div><dt>Active generation</dt><dd>{doctor.activeGenerationId || 'none'}</dd></div>
              <div><dt>Candidate generation</dt><dd>{doctor.candidateGenerationId || 'none'}</dd></div>
            </dl>
            {doctor.replayed && <p className="product-installation-note">The last lifecycle command was safely replayed.</p>}
            {doctor.restartRequired && <p className="product-installation-restart" role="status">Restart Chora to use the active installation.</p>}
            <div className="product-installation-actions">
              <ActionButton className="btn-primary" type="button" disabled={busy || !doctor.actions.setup} disabledReason={busy ? 'Wait for the current operation to finish.' : installationActionReason(doctor, 'setup')} onClick={() => openDialog('setup')}>Setup</ActionButton>
              <ActionButton className="btn-secondary" type="button" disabled={busy || !doctor.actions.upgrade} disabledReason={busy ? 'Wait for the current operation to finish.' : installationActionReason(doctor, 'upgrade')} onClick={() => openDialog('upgrade')}>Upgrade</ActionButton>
              <ActionButton className="btn-secondary" type="button" disabled={busy || !doctor.actions.gc} disabledReason={busy ? 'Wait for the current operation to finish.' : installationActionReason(doctor, 'gc')} onClick={() => openDialog('gc')}>Bounded GC</ActionButton>
              <ActionButton className="btn-reject" type="button" disabled={busy || !doctor.actions.uninstall} disabledReason={busy ? 'Wait for the current operation to finish.' : installationActionReason(doctor, 'uninstall')} onClick={() => openDialog('uninstall')}>Uninstall</ActionButton>
            </div>
          </>
        )}
        {notice && <p className="product-installation-note" role="status">{notice}</p>}
      </section>
      {dialog !== null && <LifecycleDialog
        action={dialog}
        busy={busy}
        confirmed={confirmed}
        engineToolsConfirmed={engineToolsConfirmed}
        colimaVMConfirmed={colimaVMConfirmed}
        gcLimit={gcLimit}
        onConfirmed={setConfirmed}
        onEngineToolsConfirmed={setEngineToolsConfirmed}
        onColimaVMConfirmed={setColimaVMConfirmed}
        onGCLimit={setGCLimit}
        onCancel={closeDialog}
        onSubmit={() => void performAction()}
      />}
    </details>
  )
}

function LifecycleDialog({ action, busy, confirmed, engineToolsConfirmed, colimaVMConfirmed, gcLimit, onConfirmed, onEngineToolsConfirmed, onColimaVMConfirmed, onGCLimit, onCancel, onSubmit }: {
  action: LifecycleAction
  busy: boolean
  confirmed: boolean
  engineToolsConfirmed: boolean
  colimaVMConfirmed: boolean
  gcLimit: number
  onConfirmed: (value: boolean) => void
  onEngineToolsConfirmed: (value: boolean) => void
  onColimaVMConfirmed: (value: boolean) => void
  onGCLimit: (value: number) => void
  onCancel: () => void
  onSubmit: () => void
}) {
  const title = action === 'setup' ? 'Setup installed product' : action === 'upgrade' ? 'Upgrade installed product' : action === 'gc' ? 'Run bounded garbage collection' : 'Uninstall Chora product assets'
  const submitLabel = action === 'setup' ? 'Confirm setup' : action === 'upgrade' ? 'Confirm upgrade' : action === 'gc' ? 'Confirm bounded GC' : 'Confirm uninstall'
  const enabled = action === 'setup' ? engineToolsConfirmed && colimaVMConfirmed : confirmed
  return (
    <div className="disclosure-backdrop">
      <section className="product-lifecycle-dialog" role="dialog" aria-modal="true" aria-labelledby="product-lifecycle-title">
        <h2 id="product-lifecycle-title">{title}</h2>
        {action === 'setup' && <>
          <p>A missing Engine requires the pinned Colima 0.10.3 and Docker CLI 29.6.1 toolchain. The pinned Colima VM uses 2 CPU, 4 GiB memory, and 20 GiB disk.</p>
          <label><input type="checkbox" checked={engineToolsConfirmed} onChange={(event) => onEngineToolsConfirmed(event.target.checked)} /> I authorize the pinned Colima 0.10.3 and Docker CLI 29.6.1 Engine tools.</label>
          <label><input type="checkbox" checked={colimaVMConfirmed} onChange={(event) => onColimaVMConfirmed(event.target.checked)} /> I confirm the Colima VM boundary: 2 CPU, 4 GiB memory, and 20 GiB disk.</label>
        </>}
        {action === 'upgrade' && <>
          <p>Upgrade stages and validates a candidate generation before activation. If interrupted or validation fails, candidate activation is rolled back and the current active generation is preserved.</p>
          <Confirmation checked={confirmed} onChange={onConfirmed}>I confirm this installed-product upgrade.</Confirmation>
        </>}
        {action === 'gc' && <>
          <p>Bounded garbage collection removes at most the selected number of eligible inactive Chora asset identities. Active, candidate, referenced, and unrelated Engine assets are excluded.</p>
          <label className="gc-limit">Maximum asset identities<input aria-label="Maximum asset identities" type="number" min={1} max={64} value={gcLimit} onChange={(event) => onGCLimit(Math.min(64, Math.max(1, Number(event.target.value) || 1)))} /></label>
          <Confirmation checked={confirmed} onChange={onConfirmed}>I confirm bounded garbage collection.</Confirmation>
        </>}
        {action === 'uninstall' && <>
          <p>Uninstall removes only Chora-owned product assets. User Pi and PATH Pi, worktrees, evidence, and unrelated Engine assets are preserved.</p>
          <Confirmation checked={confirmed} onChange={onConfirmed}>I confirm this ownership-bounded uninstall.</Confirmation>
        </>}
        <div className="form-actions">
          <ActionButton className="btn-secondary" type="button" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={onCancel}>Cancel</ActionButton>
          <ActionButton className={action === 'uninstall' ? 'btn-reject' : 'btn-primary'} type="button" disabled={busy || !enabled} disabledReason={busy ? 'Wait for the current operation to finish.' : action === 'setup' ? 'Confirm both the Engine tools and Colima VM permissions.' : 'Select the confirmation checkbox before continuing.'} onClick={onSubmit}>{busy ? 'Working…' : submitLabel}</ActionButton>
        </div>
      </section>
    </div>
  )
}

function Confirmation({ checked, onChange, children }: { checked: boolean; onChange: (value: boolean) => void; children: string }) {
  return <label><input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} /> {children}</label>
}
