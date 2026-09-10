import { useI18n } from '../i18n'
import type { RunView } from '../types'
import { agentExecutionProfileLabel } from './AgentExecutionProfile'

// Collapsed audit details: identities, digests, and lineage that the main flow
// never shows inline. Everything here is still reachable on demand.
export function AuditDrawer({ run }: { run: RunView }) {
  const { t } = useI18n()
  const attempt = run.attemptDetail
  const execution = run.agentExecution ?? attempt?.agentExecution
  const profileLabel = agentExecutionProfileLabel(execution?.profile)
  const rows: Array<[string, string]> = [
    ['Run', run.id],
    ['Attempt', attempt?.id ?? '—'],
    ['Runtime', attempt?.runtime ? `${attempt.runtime.adapterId} · ${attempt.runtime.kind} · ${attempt.runtime.version}` : '—'],
    ['Session', attempt?.runtime?.sessionId ?? '—'],
    ['Agent profile', profileLabel ?? '—'],
    ['Runtime source', execution?.runtimeSource ?? '—'],
    ['Execution provider', execution?.executionProvider ?? '—'],
    ['Capability policy', execution?.capabilityPolicy ?? '—'],
    ['Trust disclosure', execution?.trustDisclosurePolicy || '—'],
    ['Sandbox', execution?.profile === 'trusted_local' ? 'No Sandbox' : attempt?.sandbox.image ?? '—'],
  ]
  if (run.snapshot) {
    rows.push(['Snapshot', run.snapshot.id], ['Snapshot digest', run.snapshot.digest])
  }
  if (run.verification) {
    rows.push(['Verification', run.verification.state])
    rows.push(['Verifier policy', `${run.verification.bindings.verifierPolicyVersion} · ${run.verification.bindings.verifierPolicyDigest.slice(0, 12)}…`])
  }
  if (run.agentReport) {
    rows.push(['Agent report', run.agentReport.id])
  }
  return (
    <details className="audit">
      <summary>{t('Audit details')}</summary>
      <dl>
        {rows.map(([key, value]) => (
          <div key={key}>
            <dt>{t(key)}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
    </details>
  )
}
