import { describe, expect, test } from 'vitest'
import type { RunView } from './types'
import { defaultAcceptReviewComment, hasIndependentVerificationEvidence } from './runProvenance'

function run(overrides: Partial<RunView> = {}): RunView {
  return {
    id: 'run-1', status: 'awaiting_review', version: 1, attempt: 1, adapter: 'pi',
    room: { id: 'room-1', name: 'Room', description: '' },
    task: { id: 'task-1', title: 'Task', goal: 'Goal' },
    context: [], timeline: [], artifacts: [], unknowns: [], criteria: [],
    ...overrides,
  }
}

describe('run provenance', () => {
  test('uses Agent-reported acceptance wording when no verifier evidence exists', () => {
    const value = run({
      verificationDisposition: { state: 'not_applicable', reason: 'No verifier required.' },
      reviewablePatch: {
        evidenceKind: 'agent_reported', agentReportId: 'report-1', patchDigest: 'patch', baselineDigest: 'base',
        declaredFilesDigest: 'files', resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: '',
        artifactId: 'artifact-1', rawDownload: '/patch', files: [],
      },
      verification: {
        id: 'verification-1', agentAttemptId: 'attempt-1', state: 'completed',
        bindings: { baselineDigest: 'a', patchDigest: 'b', contextSnapshotDigest: 'c', acceptanceContractDigest: 'd', verifierPolicyVersion: 'v1', verifierPolicyDigest: 'e' },
        attempts: [],
      },
    })

    expect(hasIndependentVerificationEvidence(value)).toBe(false)
    expect(defaultAcceptReviewComment(value)).toBe('Accepted the digest-bound reviewable Patch and Agent-reported evidence.')
  })

  test('preserves the legacy independent-evidence acceptance wording after a verifier ran', () => {
    const value = run({
      verification: {
        id: 'verification-1', agentAttemptId: 'attempt-1', state: 'completed',
        bindings: { baselineDigest: 'a', patchDigest: 'b', contextSnapshotDigest: 'c', acceptanceContractDigest: 'd', verifierPolicyVersion: 'v1', verifierPolicyDigest: 'e' },
        attempts: [{
          id: 'va-1', sequence: 1, state: 'completed', evidenceComplete: true, cleanupProven: true,
          createdAt: '2026-09-03T00:00:00Z', updatedAt: '2026-09-03T00:00:01Z', checks: [], commands: [],
        }],
      },
    })

    expect(hasIndependentVerificationEvidence(value)).toBe(true)
    expect(defaultAcceptReviewComment(value)).toBe('Accepted the complete digest-bound Patch and independent evidence.')
  })

  test('uses the explicit independent-verification discriminator when present', () => {
    const value = run({
      reviewablePatch: {
        evidenceKind: 'independent_verification', patchDigest: 'patch', baselineDigest: 'base', declaredFilesDigest: 'files',
        resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: 'va-1', artifactId: 'artifact-1',
        rawDownload: '/patch', files: [],
      },
    })

    expect(hasIndependentVerificationEvidence(value)).toBe(true)
    expect(defaultAcceptReviewComment(value)).toBe('Accepted the complete digest-bound Patch and independent evidence.')
  })
})
