import type { RunView } from './types'

export const LEGACY_AGENT_PREFIX = 'Legacy Agent claim (non-authoritative)'
export const INDEPENDENT_VERIFIER_PREFIX = 'Independent Verifier evidence'

export function hasIndependentVerificationEvidence(run: RunView) {
  if (run.reviewablePatch?.evidenceKind) {
    return run.reviewablePatch.evidenceKind === 'independent_verification'
  }
  const verifierAttemptRan = (run.verification?.attempts ?? []).some((attempt) =>
    attempt.evidenceComplete || attempt.checks.length > 0 || attempt.commands.length > 0,
  )
  const legacyVerifierEvidence = run.criteria.some((criterion) =>
    criterion.evidence.startsWith(INDEPENDENT_VERIFIER_PREFIX),
  )
  return verifierAttemptRan || legacyVerifierEvidence
}

export function defaultAcceptReviewComment(run: RunView) {
  return hasIndependentVerificationEvidence(run)
    ? 'Accepted the complete digest-bound Patch and independent evidence.'
    : 'Accepted the digest-bound reviewable Patch and Agent-reported evidence.'
}
