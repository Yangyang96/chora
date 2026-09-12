import { NewTask } from './NewTask'
import { api } from '../api'
import { render, screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, test, vi } from 'vitest'
import type { AgentExecutionProfile, RunView } from '../types'
import { AgentExecutionProfileSelector, TRUSTED_LOCAL_DISCLOSURE_POLICY, TRUSTED_LOCAL_LABEL } from './AgentExecutionProfile'
import { RunStream } from './RunStream'

function SelectorHarness({ acknowledgement = '', onAcknowledge }: {
  acknowledgement?: string
  onAcknowledge: () => Promise<boolean>
}) {
  const [profile, setProfile] = useState<AgentExecutionProfile>('isolated_local')
  return (
    <AgentExecutionProfileSelector
      value={profile}
      acknowledgedPolicyVersion={acknowledgement}
      onChange={setProfile}
      onAcknowledgeTrustedLocal={onAcknowledge}
    />
  )
}

describe('AgentExecutionProfileSelector', () => {
  test('offers only Isolated Local and Trusted Local while retaining the isolated selection on disclosure cancellation', async () => {
    const acknowledge = vi.fn(async () => true)
    render(<SelectorHarness onAcknowledge={acknowledge} />)

    expect(screen.getByRole('radio', { name: 'Isolated Local' })).toBeChecked()
    expect(screen.queryByRole('radio', { name: 'Standard' })).not.toBeInTheDocument()
    expect(screen.queryByRole('radio', { name: 'Minimal' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL }))

    const disclosure = screen.getByRole('dialog', { name: TRUSTED_LOCAL_LABEL })
    expect(within(disclosure).getByText(TRUSTED_LOCAL_DISCLOSURE_POLICY)).toBeInTheDocument()
    expect(within(disclosure).getByText(/runs the supported local Pi directly as a process on this host/)).toBeInTheDocument()
    expect(within(disclosure).getByText(/configuration, and credentials/)).toBeInTheDocument()
    expect(within(disclosure).getByText(/broader filesystem and network access/)).toBeInTheDocument()
    expect(within(disclosure).getByText(/There is no Sandbox/)).toBeInTheDocument()

    await userEvent.click(within(disclosure).getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('radio', { name: 'Isolated Local' })).toBeChecked()
    expect(acknowledge).not.toHaveBeenCalled()
  })

  test('retains Trusted Local only after current-policy acknowledgement succeeds', async () => {
    const acknowledge = vi.fn(async () => true)
    render(<SelectorHarness onAcknowledge={acknowledge} />)

    await userEvent.click(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL }))
    await userEvent.click(screen.getByRole('button', { name: 'Acknowledge and use Trusted Local' }))
    expect(acknowledge).toHaveBeenCalledOnce()
    expect(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL })).toBeChecked()
  })

  test('fails closed on acknowledgement error and accepts only proven current policy', async () => {
    const rejected = vi.fn(async () => false)
    const { unmount } = render(<SelectorHarness onAcknowledge={rejected} />)
    await userEvent.click(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL }))
    await userEvent.click(screen.getByRole('button', { name: 'Acknowledge and use Trusted Local' }))
    expect(screen.getByRole('radio', { name: 'Isolated Local' })).toBeChecked()
    unmount()

    const proven = vi.fn(async () => true)
    render(<SelectorHarness acknowledgement={TRUSTED_LOCAL_DISCLOSURE_POLICY} onAcknowledge={proven} />)
    await userEvent.click(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(proven).not.toHaveBeenCalled()
    expect(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL })).toBeChecked()
  })
})

describe('Run Agent execution profile surfaces', () => {
  test('keeps Trusted Local · No Sandbox visible through header, activity, retry, review, and audit', async () => {
    const run = trustedLocalRun()
    const retry = vi.fn()
    const switchProfile = vi.fn(async () => true)
    const { container } = render(
      <RunStream
        run={run}
        busy={false}
        onCancel={vi.fn()}
        onReview={vi.fn()}
        onApply={vi.fn()}
        onRetry={retry}
        onSwitchProfile={switchProfile}
        onAcknowledgeTrustedLocal={async () => true}
        onResolveDecision={vi.fn()}
        onRetryVerification={vi.fn()}
        onChangeRequirement={vi.fn()}
      />,
    )

    expect(container.querySelector('.run-profile-header')).toHaveTextContent(TRUSTED_LOCAL_LABEL)
    expect(container.querySelector('.run-profile-activity')).toHaveTextContent(TRUSTED_LOCAL_LABEL)
    expect(container.querySelector('.stream-action .surface-profile')).toHaveTextContent(TRUSTED_LOCAL_LABEL)
    expect(container.querySelector('.result-overview')).toHaveTextContent(TRUSTED_LOCAL_LABEL)
    const audit = container.querySelector('.audit') as HTMLElement
    expect(within(audit).getByText(TRUSTED_LOCAL_LABEL)).toBeInTheDocument()
    expect(within(audit).getByText('No Sandbox')).toBeInTheDocument()
    expect(within(audit).queryByText(/Docker Sandbox/)).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Retry Pi' }))
    expect(retry).toHaveBeenCalledWith('Resolve the rejected acceptance gap and return an updated result.')
    expect(switchProfile).not.toHaveBeenCalled()

    await userEvent.click(screen.getByRole('radio', { name: 'Isolated Local' }))
    await userEvent.click(screen.getByRole('button', { name: 'Create successor with selected profile' }))
    expect(switchProfile).toHaveBeenCalledWith('isolated_local', 'Use this profile for a fresh successor Attempt.')
    expect(retry).toHaveBeenCalledOnce()
  })
})

function trustedLocalRun(): RunView {
  return {
    id: 'run-trusted', status: 'revision_required', version: 8, attempt: 1, adapter: 'pi',
    agentExecution: {
      profile: 'trusted_local', runtimeSource: 'local_pi', executionProvider: 'trusted_host',
      capabilityPolicy: 'pi.native', trustDisclosurePolicy: TRUSTED_LOCAL_DISCLOSURE_POLICY,
      sandboxed: false, disclosureLabel: TRUSTED_LOCAL_LABEL,
    },
    room: { id: 'room-1', name: 'Room', description: '' },
    task: { id: 'task-1', title: 'Trusted task', goal: 'Exercise the local profile' },
    context: [], artifacts: [], unknowns: [], criteria: [],
    timeline: [{ sequence: 1, type: 'run.revision_required', title: 'Revision required', detail: 'A bounded change is needed.', time: '2026-08-27T00:00:00Z' }],
    activity: { phase: 'waiting_for_retry', currentAction: 'Waiting for retry', elapsedMs: 1_000 },
    attemptDetail: {
      id: 'attempt-1', sequence: 1, state: 'revision_required', executionWorkspace: '/tmp/execution',
      agentExecution: {
        profile: 'trusted_local', runtimeSource: 'local_pi', executionProvider: 'trusted_host',
        capabilityPolicy: 'pi.native', trustDisclosurePolicy: TRUSTED_LOCAL_DISCLOSURE_POLICY,
        sandboxed: false, disclosureLabel: TRUSTED_LOCAL_LABEL,
      },
      sandbox: { status: 'not_applicable', provider: 'trusted_host', mode: 'No Sandbox', image: 'not_applicable', policyFingerprint: 'not_applicable' },
    },
    reviewablePatch: {
      patchDigest: 'a'.repeat(64), baselineDigest: 'b'.repeat(64), declaredFilesDigest: 'c'.repeat(64),
      resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: 'verification-1', artifactId: 'artifact-1', rawDownload: '/patch', files: [],
    },
    controls: { canCancel: false, canRetry: true, canReview: false, canSwitchAgentExecutionProfile: true },
  }
}


describe('Local Workbench startup guard', () => {
  test('requires explicit local choice and disclosure; cancel cannot start Standard', async () => {
    const submit = vi.fn()
    render(<NewTask roomName="Local" busy={false} initialRequirement="Add one unit test" trustedLocalSelectionReady
      isolatedLocal={{ state: 'ready', reason: 'Ready', preparationAvailable: true, piVersion: '0.85.1', nodeVersion: '22.19.0', policy: { network: 'restricted', resources: 'selected', files: 'worktree', credentials: 'Codex OAuth' } }} onPrepareIsolatedLocal={vi.fn()}
      piDiscovery={{ phase: 'loaded', discovery: { state: 'ready', readyProviders: ['provider'], notReadyProviders: [] } }}
      onCancel={vi.fn()} onSubmit={submit} onAcknowledgeTrustedLocal={async () => true} />)
    expect(screen.queryByRole('radio', { name: 'Standard' })).not.toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Isolated Local' })).toBeChecked()
    expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled()
    await userEvent.click(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL }))
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('radio', { name: 'Isolated Local' })).toBeChecked()
    expect(submit).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('radio', { name: TRUSTED_LOCAL_LABEL }))
    await userEvent.click(screen.getByRole('button', { name: 'Acknowledge and use Trusted Local' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled())
    await userEvent.click(screen.getByRole('button', { name: 'Start' }))
    expect(submit).toHaveBeenCalledWith('Add one unit test', 'trusted_local')
  })

  test('structured503 preserves the cause and corrective action', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 503, json: async () => ({ failure: { observed: 'Docker unavailable', required: 'Docker Sandbox', action: 'Configure the isolated environment' } }) })))
    try {
      await expect(api('/api/test')).rejects.toThrow('Docker unavailable · Docker Sandbox · Configure the isolated environment')
    } finally { vi.unstubAllGlobals() }
  })
})
