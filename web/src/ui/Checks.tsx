import { useI18n } from '../i18n'
import { INDEPENDENT_VERIFIER_PREFIX, LEGACY_AGENT_PREFIX } from '../runProvenance'
import type { RunView } from '../types'

type CheckRow = {
  criterionId: string
  title: string
  agent?: { status: string; evidence: string }
  verified?: { status: string; trust: string; evidence: string }
}

function stripPrefix(value: string, prefix: string) {
  return value.slice(prefix.length).replace(/^:?\s*/, '')
}

// Truthful provenance: an "Agent-reported" check comes only from the Agent's own
// claimedChecks or a criterion whose evidence is marked as a legacy Agent claim.
// An "Independently verified" check comes only from the Verifier's attempts or a
// criterion whose evidence is marked as independent Verifier evidence. Never
// invent an independent-verification claim from Agent-only data.
function buildCheckRows(run: RunView): CheckRow[] {
  const criteria = new Map(run.criteria.map((criterion) => [criterion.id, criterion]))
  const claimed = new Map((run.agentReport?.claimedChecks ?? []).map((check) => [check.criterionId, check]))
  const verifiedChecks = (run.verification?.attempts ?? []).flatMap((attempt) => attempt.checks)
  const verified = new Map(verifiedChecks.map((check) => [check.criterionId, check]))

  const ids = new Set<string>()
  for (const criterion of run.criteria) ids.add(criterion.id)
  for (const check of run.agentReport?.claimedChecks ?? []) ids.add(check.criterionId)
  for (const check of verifiedChecks) ids.add(check.criterionId)

  return [...ids].map((id) => {
    const criterion = criteria.get(id)
    const claim = claimed.get(id)
    const check = verified.get(id)

    let agent: CheckRow['agent']
    if (claim) {
      agent = { status: claim.status, evidence: claim.evidence }
    } else if (criterion?.evidence.startsWith(LEGACY_AGENT_PREFIX)) {
      agent = { status: criterion.status, evidence: stripPrefix(criterion.evidence, LEGACY_AGENT_PREFIX) }
    }

    let verifiedCell: CheckRow['verified']
    if (check) {
      verifiedCell = {
        status: check.status,
        trust: check.trust,
        evidence: check.evidenceIds.length > 0 ? check.evidenceIds.join(', ') : '',
      }
    } else if (criterion?.evidence.startsWith(INDEPENDENT_VERIFIER_PREFIX)) {
      verifiedCell = { status: criterion.status, trust: '', evidence: stripPrefix(criterion.evidence, INDEPENDENT_VERIFIER_PREFIX) }
    }

    return { criterionId: id, title: criterion?.title ?? id, agent, verified: verifiedCell }
  })
}

export function ChecksSection({ run }: { run: RunView }) {
  const { t } = useI18n()
  const rows = buildCheckRows(run)
  const hasVerifiedChecks = rows.some((row) => row.verified)
  const runtimeChecks = run.agentExecution?.runtimeSource === 'local_pi'
  const failedChecks = rows.some((row) => ['FAIL', 'FAILED'].includes(row.agent?.status.toUpperCase() ?? ''))
  return (
    <section className="workbench-section" aria-labelledby="checks-title">
      <div className="workbench-section-head">
        <div>
          <span className="eyebrow">{t('Evidence')}</span>
          <h2 id="checks-title">{t('Checks')}</h2>
        </div>
      </div>
      <div className="workbench-section-body">
        {runtimeChecks && <p className="checks-explanation">{t('These are check execution results from Pi, not a verdict on the requested change. Review the diff separately.')}</p>}
        {runtimeChecks && failedChecks && <p className="warning-banner">{t('A check or setup command failed. Inspect recorded activity, fix the dependency/setup issue, then retry. Change Project settings for new tasks if a different command is needed.')}</p>}
        {rows.length === 0 ? (
          <p className="stream-empty">{t('No checks recorded yet.')}</p>
        ) : (
          <div className={`checks-table${hasVerifiedChecks ? '' : ' checks-table-agent-only'}`} role="table" aria-label={t('Checks')}>
            <div className="checks-row checks-row-head" role="row">
              <div role="columnheader">{t(runtimeChecks ? 'Related requirement' : 'Acceptance criterion')}</div>
              <div role="columnheader">{t(runtimeChecks ? 'Pi check results' : 'Agent-reported')}</div>
              {hasVerifiedChecks && <div role="columnheader">{t('Independently verified')}</div>}
            </div>
            {rows.map((row) => (
              <div className="checks-row" role="row" key={row.criterionId}>
                <div className="checks-cell checks-title" role="cell"><strong>{t(runtimeChecks && row.title === 'Requested behavior is implemented' ? 'Requested change' : row.title)}</strong></div>
                <div className="checks-cell checks-agent" role="cell">
                  {row.agent ? (
                    <>
                      <span className={`checks-status checks-status-${row.agent.status}`}>{t(runtimeChecks ? ({ FAIL: 'Check failed', PASS: 'Check passed', UNKNOWN: row.agent.evidence.includes('Not run / Unverified') || row.agent.evidence.includes('was not observed') ? 'Not run / Unverified' : 'Check result unknown', FAILED: 'Check failed', PASSED: 'Check passed' }[row.agent.status.toUpperCase()] ?? row.agent.status) : row.agent.status)}</span>
                      {row.agent.evidence && (runtimeChecks ? <details className="check-evidence"><summary>{t('View recorded evidence')}</summary><p>{row.agent.evidence}</p></details> : <p>{row.agent.evidence}</p>)}
                    </>
                  ) : <span className="checks-none">—</span>}
                </div>
                {hasVerifiedChecks && (
                  <div className="checks-cell checks-verified" role="cell">
                    {row.verified ? (
                      <>
                        <span className={`checks-status checks-status-${row.verified.status}`}>{t(row.verified.status)}</span>
                        {row.verified.trust && <p>{t('Trust: {trust}', { trust: row.verified.trust })}</p>}
                        {row.verified.evidence && <p>{row.verified.evidence}</p>}
                      </>
                    ) : <span className="checks-none">—</span>}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}
