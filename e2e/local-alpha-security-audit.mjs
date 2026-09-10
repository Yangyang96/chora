import { spawn, execFile } from 'node:child_process'
import { createHash } from 'node:crypto'
import { access, chmod, mkdir, open, readFile, readlink, readdir, stat, writeFile } from 'node:fs/promises'
import { dirname, isAbsolute, relative, resolve } from 'node:path'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)
const taskID = 'task_018f0c4a-1a2c-7c3d-8e4f-1234567890ab'
const snapshotID = 'context_snapshot_018f0c4a-1a2f-7c3d-8e4f-1234567890ab'
const frozenWorkspaceRoot = absoluteRequired('CHORA_FROZEN_WORKSPACE_ROOT')
const maxCommandBytes = 4 << 20
const maxLogBytes = 16 << 20
const maxEvidenceBytes = 128 << 10

const installRoot = absoluteRequired('CHORA_INSTALL_ROOT')
const sourceRoot = absoluteRequired('CHORA_SOURCE_ROOT')
const repositoryRoot = absoluteRequired('CHORA_REPOSITORY_ROOT')
const sourceManifest = absoluteRequired('CHORA_SOURCE_MANIFEST')
const contractRoot = absoluteRequired('CHORA_CONTRACT_ROOT')
const dataRoot = absoluteRequired('CHORA_DATA_ROOT')
const authFile = absoluteRequired('CHORA_AUTH_FILE')
const caFile = absoluteRequired('CHORA_CA_FILE')
const evidenceRoot = absoluteRequired('CHORA_EVIDENCE_DIR')
const unrelatedHostPath = absoluteRequired('CHORA_UNRELATED_HOST_PATH')
const binary = absoluteRequired('CHORA_BINARY')
const bundleAggregate = digestRequired('CHORA_BUNDLE_AGGREGATE')
const preflightFingerprint = digestRequired('CHORA_PREFLIGHT_FINGERPRINT')
const proxyURL = required('CHORA_PROXY_URL')
const modelURL = required('CHORA_MODEL_URL')
const port = integerRequired('CHORA_PORT', 1, 65535)
const docker = process.env.CHORA_DOCKER_BINARY || 'docker'
const baseURL = `http://127.0.0.1:${port}`

assert(pathInside(installRoot, binary), 'CHORA_BINARY must be inside CHORA_INSTALL_ROOT')
assert(disjoint(dataRoot, evidenceRoot), 'CHORA_EVIDENCE_DIR must be outside CHORA_DATA_ROOT')
for (const protectedRoot of [installRoot, sourceRoot, contractRoot, dataRoot, evidenceRoot, authFile, caFile]) {
  assert(!sameOrInside(protectedRoot, unrelatedHostPath), 'CHORA_UNRELATED_HOST_PATH must be outside all declared roots')
}
await access(unrelatedHostPath)
const unrelatedInfo = await stat(unrelatedHostPath)
assert(unrelatedInfo.isFile() && unrelatedInfo.size > 0 && unrelatedInfo.size <= (4 << 20), 'unrelated host sentinel must be a bounded regular file')
const unrelatedBeforeBytes = await readFile(unrelatedHostPath)
const unrelatedBeforeDigest = createHash('sha256').update(unrelatedBeforeBytes).digest('hex')
unrelatedBeforeBytes.fill(0)

const authBytes = await readFile(authFile)
let authDocument
try {
  authDocument = JSON.parse(authBytes.toString('utf8'))
} catch {
  throw new Error('OAuth source is not valid JSON')
} finally {
  authBytes.fill(0)
}
const credentialScalars = []
collectCredentialScalars(authDocument, credentialScalars)
assert(credentialScalars.some((value) => value.length > 0), 'OAuth source has no auditable scalar credential values')
authDocument = undefined

let credentialLeak = false
let service
const serverLogs = []
let serverLogBytes = 0

try {
  const result = await audit()
  const evidenceFile = resolve(evidenceRoot, 'local-alpha-security-audit.json')
  await mkdir(evidenceRoot, { recursive: true, mode: 0o700 })
  await chmod(evidenceRoot, 0o700)
  const evidenceBytes = Buffer.from(`${JSON.stringify(result, null, 2)}\n`)
  assert(evidenceBytes.length <= maxEvidenceBytes, 'security audit evidence exceeds bound')
  observeCredentialSurface(evidenceBytes)
  await writeFile(evidenceFile, evidenceBytes, { flag: 'wx', mode: 0o600 })
  await chmod(evidenceFile, 0o600)
  const evidenceMode = (await stat(evidenceFile)).mode & 0o777
  assert(evidenceMode === 0o600, 'security audit evidence mode is not 0600')
  process.stdout.write(`${JSON.stringify({ status: 'passed', runId: result.run.runId, evidenceFile })}\n`)
} catch (error) {
  process.stderr.write(`security audit failed: ${safeError(error)}\n`)
  process.exitCode = 1
} finally {
  if (service) await stopService(service, false)
  for (const value of credentialScalars) value.fill(0)
}

async function audit() {
  const v4 = parseJSON(await readFile(resolve(contractRoot, 'contracts/g2-m1a/chora-m1-real-task.v4.json')), 'fixed v4 contract')
  const v8 = parseJSON(await readFile(resolve(contractRoot, 'contracts/g2-m1a/chora-m1-real-task.v8.json')), 'fixed v8 contract')
  const doctor = { fingerprint: preflightFingerprint }
  service = await startService(doctor.fingerprint)

  const materialized = await api('POST', '/api/spec-coding/materializations', {
    contract: v4,
    workspaceRoot: frozenWorkspaceRoot,
    agentAdapter: 'pi',
    contextEntryId: 'context_entry_018f0c4a-1a30-7c3d-8e4f-1234567890ab',
    contextRevisionId: 'context_revision_018f0c4a-1a31-7c3d-8e4f-1234567890ab',
    charterId: 'charter_018f0c4a-1a32-7c3d-8e4f-1234567890ab',
    frozenAt: '2026-08-11T00:00:00Z',
  }, 201)
  assert(materialized.taskId === taskID, 'fixed Task identity drift')
  assert(materialized.snapshot?.id === snapshotID, 'fixed Context Snapshot identity drift')
  assert(materialized.snapshot?.digest === v8.execution.input.context_snapshot_digest, 'fixed Context Snapshot digest drift')

  const task = await api('GET', `/api/tasks/${taskID}`, undefined, 200)
  await api('POST', `/api/tasks/${taskID}/plan/review`, {
    expectedVersion: task.plan.version,
    kind: 'accept',
    note: 'The fixed Local Alpha task plan is bounded and ready for the declared security audit.',
  }, 200)
  const registered = await api('POST', '/api/spec-coding/contracts', { contract: v8 }, 200)
  assert(registered.status === 'registered' && registered.snapshot?.id === snapshotID, 'fixed v8 contract registration failed')

  const started = await api('POST', `/api/tasks/${taskID}/runs`, { adapter: 'pi', snapshotId: snapshotID }, 201)
  assertSafeIdentity(started.id, 'Run ID')
  const agentPhase = await waitForAudited(started.id, 'awaiting_verification', 20 * 60_000, captureAgentResources)
  const awaitingVerification = agentPhase.run
  assert(awaitingVerification.attempt === 1, 'hidden Agent Retry detected')
  assert(awaitingVerification.agentReport?.authority === 'non_authoritative_agent_claim', 'AgentReport authority drift')
  await proveAgentCleanup(started.id, agentPhase.audit)

  const verifierWatcher = await armVerifierStartWatcher(started.id, 10 * 60_000)
  let verifierPhase
  try {
    await api('POST', `/api/runs/${started.id}/verification`, {}, 202)
    const verifierAudit = await verifierWatcher.audit
    const awaitingReview = await waitForTarget(started.id, 'awaiting_review', 10 * 60_000)
    verifierPhase = { run: awaitingReview, audit: verifierAudit }
  } finally {
    await verifierWatcher.stop()
  }
  const awaitingReview = verifierPhase.run
  assert(awaitingReview.verification?.attempts?.length === 1, 'hidden Verifier Retry detected')
  const verificationAttempt = awaitingReview.verification.attempts[0]
  assert(verificationAttempt.state === 'completed' && verificationAttempt.evidenceComplete === true && verificationAttempt.cleanupProven === true, 'Verifier evidence or cleanup is incomplete')
  assert(verificationAttempt.commands?.length === 1 && verificationAttempt.commands[0].classification === 'exited' && verificationAttempt.commands[0].exitCode === 0, 'declared verification command did not pass')
  assert(verificationAttempt.checks?.length === 2 && verificationAttempt.checks.every((check) => check.status === 'passed' && check.trust === 'trusted'), 'trusted Acceptance Checks did not pass')
  assert(awaitingReview.verification.result?.outcome === 'review_ready', 'independent Verification is not review-ready')
  assertDigest(awaitingReview.reviewablePatch?.patchDigest, 'Reviewable Patch digest')
  await proveVerifierCleanup(started.id, verifierPhase.audit)

  const beforeRestartIdentity = identityLeaves(awaitingReview)
  assertRequiredIdentityLeaves(beforeRestartIdentity)
  const identityEvidence = summarizeIdentityLeaves(beforeRestartIdentity)
  await stopService(service, true)
  service = undefined
  await scanDataRoot()
  service = await startService(doctor.fingerprint)
  const reopened = await api('GET', `/api/runs/${started.id}`, undefined, 200)
  assert(reopened.status === 'awaiting_review', 'reopened Run did not preserve review-ready state')
  assertIdentityLeaves(beforeRestartIdentity, reopened)

  const patchResponse = await fetch(`${baseURL}/api/runs/${started.id}/patch`)
  const patchBytes = Buffer.from(await patchResponse.arrayBuffer())
  observeCredentialSurface(patchBytes)
  assert(patchResponse.status === 200, 'Patch download failed')
  const patchDigest = createHash('sha256').update(patchBytes).digest('hex')
  assert(patchDigest === awaitingReview.reviewablePatch.patchDigest, 'downloaded Patch digest drift')
  assert(patchResponse.headers.get('x-chora-patch-sha256') === patchDigest, 'Patch response identity header drift')

  const accepted = await api('POST', `/api/runs/${started.id}/review`, {
    expectedVersion: reopened.version,
    kind: 'accept',
    note: 'The fixed Patch and independently verified evidence satisfy the frozen Local Alpha security audit.',
  }, 200, { 'Idempotency-Key': `g3-m2-security-audit-${started.id}` })
  assert(accepted.status === 'accepted', 'formal Review did not reach accepted state')
  assert(accepted.verifiedReview?.kind === 'accept' && accepted.verifiedReview?.patchDigest === patchDigest, 'immutable formal Review identity drift')
  assert(accepted.attempt === 1 && accepted.verification?.attempts?.length === 1, 'accepted journey contains hidden Retry')
  assertIdentityLeaves(beforeRestartIdentity, accepted)

  await api('GET', `/api/runs/${started.id}/events?after=0&limit=1000`, undefined, 200, {}, 'event')
  await api('GET', '/api/status', undefined, 200)
  await stopService(service, true)
  service = undefined
  await scanDataRoot()
	await scanTree(installRoot, installRoot)
  observeCredentialSurface(Buffer.concat(serverLogs))
  assert(!credentialLeak, 'credential value leaked into audited surface')
	const unrelatedAfterBytes = await readFile(unrelatedHostPath)
	const unrelatedAfterDigest = createHash('sha256').update(unrelatedAfterBytes).digest('hex')
	unrelatedAfterBytes.fill(0)
	assert(unrelatedAfterDigest === unrelatedBeforeDigest, 'unrelated host sentinel changed during container probes')

  const verifierAttemptID = verifierPhase.audit.labels['chora.verification_attempt_id']
  const reviewDecisionID = pick(accepted.verifiedReview, 'id', 'decisionId', 'reviewDecisionId')
  assertSafeIdentity(verifierAttemptID, 'Verification Attempt ID')
  assertSafeIdentity(reviewDecisionID, 'Review Decision ID')
  return {
    schemaVersion: 'chora.g3-m2-security-audit.v1',
    status: 'passed',
    redacted: true,
    preflight: { passed: true, resourcesCreated: false, fingerprint: doctor.fingerprint, serviceStarts: 2 },
    run: {
      runId: started.id,
      taskId: taskID,
      snapshotId: snapshotID,
      agentAttemptId: agentPhase.audit.labels['chora.attempt_id'],
      verificationAttemptId: verifierAttemptID,
      reviewDecisionId: reviewDecisionID,
      patchDigest,
      terminalStatus: accepted.status,
    },
    audit: {
      directArgv: true,
      exactLabelFilteredInspection: true,
      dockerInspectionReadOnly: true,
      agent: agentPhase.audit.evidence,
      verifier: verifierPhase.audit.evidence,
	  unrelatedHostPathDenied: true,
	  unrelatedHostDigestUnchanged: unrelatedAfterDigest,
      agentCredentialAttemptScoped: true,
      verifierCredentialAbsent: true,
      noDockerSocketOrHostExecutionFallback: true,
      processTreesBounded: true,
      postAttemptResidueAbsent: true,
      restartReopenIdentityStable: { passed: true, ...identityEvidence },
      credentialLeak: false,
    },
  }
}

async function startService(fingerprint) {
  const args = [
    'serve', '--source', sourceRoot, '--source-manifest', sourceManifest,
    '--bundle-aggregate', bundleAggregate, '--install', installRoot, '--data', dataRoot,
    '--repository', repositoryRoot,
    '--auth', authFile, '--ca', caFile, '--proxy', proxyURL, '--model-url', modelURL,
    '--preflight-fingerprint', fingerprint, '--port', String(port),
  ]
  const child = spawn(binary, args, { env: process.env, stdio: ['ignore', 'pipe', 'pipe'] })
  const capture = (chunk) => {
    serverLogBytes += chunk.length
    if (serverLogBytes <= maxLogBytes) serverLogs.push(Buffer.from(chunk))
  }
  child.stdout.on('data', capture)
  child.stderr.on('data', capture)
  await waitForService(child, 75_000)
  return child
}

async function stopService(child, requireCleanExit) {
  if (!child || child.exitCode !== null) return
  child.kill('SIGTERM')
  const exited = await waitForExit(child, 15_000)
  if (!exited) {
    child.kill('SIGKILL')
    await waitForExit(child, 5_000)
    if (requireCleanExit) throw new Error('production service did not stop within deadline')
  }
  assert(serverLogBytes <= maxLogBytes, 'production service logs exceed audit bound')
  observeCredentialSurface(Buffer.concat(serverLogs))
}

async function waitForService(child, timeout) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error('production service exited before readiness')
    try {
      const response = await fetch(`${baseURL}/api/status`)
      const bytes = Buffer.from(await response.arrayBuffer())
      observeCredentialSurface(bytes)
      if (response.status === 200) return
    } catch {
      // Service is not listening yet.
    }
    await delay(100)
  }
  throw new Error('production service readiness deadline exceeded')
}

async function waitForExit(child, timeout) {
  if (child.exitCode !== null || child.signalCode !== null) return true
  return new Promise((resolveWait) => {
    const timer = setTimeout(() => {
      child.off('exit', onExit)
      resolveWait(false)
    }, timeout)
    const onExit = () => {
      clearTimeout(timer)
      resolveWait(true)
    }
    child.once('exit', onExit)
  })
}

async function waitForAudited(runID, target, timeout, capture) {
  const deadline = Date.now() + timeout
  let latest
  let audit
  while (Date.now() < deadline) {
    latest = await api('GET', `/api/runs/${runID}`, undefined, 200)
    if (!audit) audit = await capture(runID)
    if (latest.status === target) {
      assert(audit, `${target} reached before active resource audit completed`)
      return { run: latest, audit }
    }
    if (['cancelled', 'revision_required', 'recovery_required', 'verification_recovery_required', 'rejected', 'accepted'].includes(latest.status)) {
      throw new Error('Run reached unexpected terminal state')
    }
    await delay(50)
  }
  throw new Error(`Run did not reach ${target}`)
}

async function waitForTarget(runID, target, timeout) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    const latest = await api('GET', `/api/runs/${runID}`, undefined, 200)
    if (latest.status === target) return latest
    if (['cancelled', 'revision_required', 'recovery_required', 'verification_recovery_required', 'rejected', 'accepted'].includes(latest.status)) {
      throw new Error('Run reached unexpected terminal state')
    }
    await delay(50)
  }
  throw new Error(`Run did not reach ${target}`)
}

async function armVerifierStartWatcher(runID, timeout) {
  const notBeforeMilliseconds = Date.now()
  const notBeforeNano = BigInt(notBeforeMilliseconds) * 1_000_000n
  const args = [
    '--context', 'colima', 'events',
    '--since', String(Math.floor(notBeforeMilliseconds / 1000)),
    '--until', new Date(notBeforeMilliseconds + timeout).toISOString(),
    '--filter', 'type=container', '--filter', 'event=start',
    '--filter', `label=chora.run_id=${runID}`, '--filter', `label=chora.task_id=${taskID}`,
    '--format', '{{json .}}',
  ]
  const child = spawn(docker, args, { env: process.env, stdio: ['ignore', 'pipe', 'pipe'] })
  let outputBytes = 0
  let stdout = ''
  let eventSeen = false
  let settled = false
  let stopped = false
  let unexpectedExit
  let resolveAudit
  let rejectAudit
  const audit = new Promise((resolveWatcher, rejectWatcher) => {
    resolveAudit = resolveWatcher
    rejectAudit = rejectWatcher
  })
  audit.catch(() => {})
  const settleFailure = (error) => {
    if (settled) return
    settled = true
    clearTimeout(timer)
    rejectAudit(error)
  }
  const settleSuccess = (value) => {
    if (settled) return
    settled = true
    clearTimeout(timer)
    resolveAudit(value)
  }
  const timer = setTimeout(() => settleFailure(new Error('Verifier container start watcher deadline exceeded')), timeout)
  const observeWatcherOutput = (chunk) => {
    outputBytes += chunk.length
    assert(outputBytes <= maxCommandBytes, 'Docker container start watcher output exceeds bound')
    observeCredentialSurface(chunk)
  }
  const consumeEvent = async (line) => {
    try {
      assert(!eventSeen, 'multiple Verifier container start events observed')
      const event = parseJSON(Buffer.from(line), 'Docker container start event')
      assert((event.Type ?? event.type) === 'container' && (event.Action ?? event.action ?? event.status) === 'start', 'unrelated Docker event observed')
      const actor = event.Actor ?? event.actor
      const id = actor?.ID ?? actor?.id ?? event.id
      assert(typeof id === 'string' && /^[a-f0-9]{64}$/.test(id), 'invalid Verifier container event identity')
      if (event.id !== undefined) assert(event.id === id, 'Verifier container event identity drifted')
      const labels = actor?.Attributes ?? actor?.attributes ?? {}
      assert(labels['chora.run_id'] === runID && labels['chora.task_id'] === taskID, 'unrelated Verifier container start event observed')
      for (const key of ['chora.verification_run_id', 'chora.verification_attempt_id', 'chora.workspace_id', 'chora.verifier_runtime_id']) assertSafeIdentity(labels[key], key)
      const rawTimeNano = event.timeNano ?? event.TimeNano
      assert(typeof rawTimeNano === 'number' || typeof rawTimeNano === 'string', 'Verifier container event timestamp is absent')
      const eventTimeNano = typeof rawTimeNano === 'number' ? BigInt(Math.trunc(rawTimeNano)) : BigInt(rawTimeNano)
      assert(eventTimeNano >= notBeforeNano, 'stale Verifier container start event observed')
      eventSeen = true
      settleSuccess(await captureVerifierResources(runID, id))
    } catch (error) {
      settleFailure(error)
    }
  }
  child.stdout.on('data', (chunk) => {
    try {
      observeWatcherOutput(chunk)
      stdout += chunk.toString('utf8')
      assert(Buffer.byteLength(stdout) <= maxCommandBytes, 'Docker container start watcher line exceeds bound')
      while (stdout.includes('\n')) {
        const newline = stdout.indexOf('\n')
        const line = stdout.slice(0, newline).trim()
        stdout = stdout.slice(newline + 1)
        if (line) void consumeEvent(line)
      }
    } catch (error) {
      settleFailure(error)
    }
  })
  child.stderr.on('data', (chunk) => {
    try {
      observeWatcherOutput(chunk)
    } catch (error) {
      settleFailure(error)
    }
  })
  child.on('error', () => settleFailure(new Error('Docker container start watcher launch failed')))
  child.on('exit', (code) => {
    if (!stopped) {
      unexpectedExit = new Error(code === 0 ? 'Docker container start watcher exited before explicit stop' : `Docker container start watcher exited ${code}`)
      if (!settled) settleFailure(unexpectedExit)
    }
  })
  await new Promise((resolveSpawn, rejectSpawn) => {
    child.once('spawn', resolveSpawn)
    child.once('error', rejectSpawn)
  }).catch(() => {
    throw new Error('Docker container start watcher launch failed')
  })
  return {
    audit,
    stop: async () => {
      if (!unexpectedExit && (child.exitCode !== null || child.signalCode !== null)) {
        unexpectedExit = new Error(child.exitCode === 0 ? 'Docker container start watcher exited before explicit stop' : `Docker container start watcher exited ${child.exitCode ?? child.signalCode}`)
      }
      stopped = true
      clearTimeout(timer)
      if (!settled) settleFailure(new Error('Verifier container start watcher stopped before capture'))
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM')
      let exited = await waitForExit(child, 5_000)
      if (!exited) {
        child.kill('SIGKILL')
        exited = await waitForExit(child, 5_000)
      }
      assert(exited, 'Docker container start watcher did not stop')
      if (unexpectedExit) throw unexpectedExit
    },
  }
}

async function captureAgentResources(runID) {
  const filters = [`label=chora.owner=dockersupervisor`, `label=chora.run_id=${runID}`, `label=chora.task_id=${taskID}`]
  const containerIDs = await listDocker('container', filters)
  const networkIDs = await listDocker('network', filters)
  if (containerIDs.length !== 2 || networkIDs.length !== 2) return undefined
  const containers = await Promise.all(containerIDs.map(inspectContainer))
  const networks = await Promise.all(networkIDs.map(inspectNetwork))
  const byRole = new Map(containers.map((item) => [agentRole(item), item]))
  assert(byRole.size === 2 && byRole.has('agent') && byRole.has('boundary'), 'Agent resource roles are not exact')
  const agent = byRole.get('agent')
  const boundary = byRole.get('boundary')
	const labels = exactCommonLabels([agent, boundary, ...networks], ['chora.owner', 'chora.runtime_scope', 'chora.run_id', 'chora.attempt_id', 'chora.task_id', 'chora.policy_digest'])
  assert(labels['chora.owner'] === 'dockersupervisor' && labels['chora.run_id'] === runID && labels['chora.task_id'] === taskID, 'Agent labels drifted')
  assertSafeIdentity(labels['chora.runtime_scope'], 'Agent runtime scope')
  assertDigest(labels['chora.policy_digest'], 'Agent policy digest')
  assertSafeIdentity(labels['chora.attempt_id'], 'Agent Attempt ID')
  assert(agent.Config.Labels['chora.image_digest'] === agent.Image, 'Agent image label does not match effective image')
  assert(boundary.Config.Labels['chora.image_digest'] === boundary.Image, 'Boundary image label does not match effective image')
  for (const network of networks) assert(network.Labels['chora.image_digest'] === agent.Image, 'Agent network image label drifted')
  assertContainerHardening(agent, { user: '1000:1000', cpus: 2_000_000_000, memory: 4096 << 20, pids: 256, nofile: 1024 })
  assertContainerHardening(boundary, { nonRoot: true, cpus: 250_000_000, memory: 64 << 20, pids: 32, nofile: 1024 })
  assertMounts(agent, ['/input/context', '/workspace'], dataRoot)
  assertMounts(boundary, [], dataRoot)
  assertAgentTmpfs(agent)
  assertNoCredentialEnvironment(boundary)
  const internal = networks.find((item) => item.Internal === true)
  const upstream = networks.find((item) => item.Internal === false)
  assert(internal && upstream, 'Agent internal/upstream networks are not exact')
  assert(agent.HostConfig.NetworkMode === internal.Name, 'Agent network mode is not internal policy network')
  assert(boundary.HostConfig.NetworkMode === upstream.Name, 'Boundary primary network is not upstream policy network')
  assertSetEqual(Object.keys(agent.NetworkSettings.Networks || {}), [internal.Name], 'Agent network membership drifted')
  assertSetEqual(Object.keys(boundary.NetworkSettings.Networks || {}), [internal.Name, upstream.Name], 'Boundary network membership drifted')
  assertSetEqual(Object.keys(internal.Containers || {}), [agent.Id, boundary.Id], 'internal network membership drifted')
  assertSetEqual(Object.keys(upstream.Containers || {}), [boundary.Id], 'upstream network membership drifted')
  await assertProcessTree(agent)
  await assertProcessTree(boundary)
  await dockerTest(agent.Id, ['-s', '/run/chora/pi/auth.json'])
	await assertFileMode(agent.Id, '/run/chora/pi/auth.json', '600')
	await assertNoExecutable(boundary.Id, '/bin/sh')
	await assertNoExecutable(boundary.Id, '/usr/bin/test')
	await assertHostBoundary(agent.Id)
	await assertDirectEgressDenied(agent.Id, 'host.docker.internal', 9981)
	await assertDirectEgressDenied(agent.Id, 'chatgpt.com', 443)
  const workspaceMount = agent.Mounts.find((item) => item.Destination === '/workspace')
  return {
    labels: safeResourceLabels(labels),
    filters,
    runtimeRoot: dirname(workspaceMount.Source),
    evidence: {
      containers: 2,
      networks: 2,
      roles: ['agent', 'boundary'],
      imageIds: { agent: agent.Image, boundary: boundary.Image },
      policyDigest: labels['chora.policy_digest'],
      users: { agent: agent.Config.User, boundary: boundary.Config.User },
      readOnlyRoot: true,
      capabilitiesDropped: 'ALL',
      noNewPrivileges: true,
      resourceLimits: {
        agent: limits(agent),
        boundary: limits(boundary),
      },
      mountDestinations: ['/input/context', '/workspace'],
      network: { internal: true, upstreamBoundaryOnly: true, hostOrBridgeFallback: false },
      auth: { agentAttemptPathPresent: true, mode: '0600', tmpfs: '/run/chora/pi', boundaryAbsent: true },
      boundarySurface: { bindMounts: 0, shellAbsent: true, testUtilityAbsent: true },
    },
  }
}

async function captureVerifierResources(runID, eventContainerID) {
  const filters = [`label=chora.run_id=${runID}`, `label=chora.task_id=${taskID}`]
  const verifier = await inspectContainer(eventContainerID)
  const labels = verifier.Config.Labels || {}
  assert(labels['chora.run_id'] === runID && labels['chora.task_id'] === taskID, 'Verifier labels drifted')
  for (const key of ['chora.verification_run_id', 'chora.verification_attempt_id', 'chora.workspace_id']) assertSafeIdentity(labels[key], key)
  for (const key of ['chora.verifier_policy_digest', 'chora.baseline_digest', 'chora.patch_digest', 'chora.acceptance_contract_digest']) assertDigest(labels[key], key)
  assert(labels['chora.image_digest'] === verifier.Image, 'Verifier image label does not match effective image')
  const safeLabels = safeResourceLabels(labels, [
    'chora.run_id', 'chora.task_id', 'chora.verification_run_id', 'chora.verification_attempt_id', 'chora.workspace_id',
    'chora.verifier_policy_digest', 'chora.baseline_digest', 'chora.patch_digest', 'chora.acceptance_contract_digest',
    'chora.image_digest', 'chora.verifier_runtime_id',
  ])
  await assertProcessTree(verifier)
  const containerIDs = await listDocker('container', filters)
  assert(containerIDs.length === 1 && containerIDs[0] === eventContainerID, 'Verifier exact-label container identity drifted')
  assertContainerHardening(verifier, { nonRoot: true, bounded: true })
  assert(verifier.HostConfig.NetworkMode === 'none', 'Verifier network mode is not none')
  const verifierNetworks = Object.keys(verifier.NetworkSettings.Networks || {})
  assert(verifierNetworks.length === 0 || verifierNetworks.length === 1 && verifierNetworks[0] === 'none', 'Verifier has policy-expanding network membership')
  assertMounts(verifier, ['/workspace'], dataRoot)
  const envNames = (verifier.Config.Env || []).map((item) => item.split('=', 1)[0])
  assert(!envNames.some((name) => /(AUTH|TOKEN|SECRET|PASSWORD|CREDENTIAL|API_KEY)/i.test(name)), 'Verifier environment exposes credential input')
  await dockerTest(verifier.Id, ['!', '-e', '/run/chora/pi/auth.json'])
	await assertHostBoundary(verifier.Id)
	await assertDirectEgressDenied(verifier.Id, 'host.docker.internal', 9981)
	await assertDirectEgressDenied(verifier.Id, 'chatgpt.com', 443)
  const workspaceMount = verifier.Mounts.find((item) => item.Destination === '/workspace')
  return {
    labels: safeLabels,
    filters,
    workspaceRoot: workspaceMount.Source,
    evidence: {
      containers: 1,
      imageId: verifier.Image,
      policyDigest: labels['chora.verifier_policy_digest'],
      user: verifier.Config.User,
      readOnlyRoot: true,
      capabilitiesDropped: 'ALL',
      noNewPrivileges: true,
      resourceLimits: limits(verifier),
      mountDestinations: ['/workspace'],
      networkMode: 'none',
      authAbsent: true,
    },
  }
}

async function proveAgentCleanup(runID, audit) {
  await waitForNoResources('container', audit.filters, 15_000)
  await waitForNoResources('network', audit.filters, 15_000)
  await assertAbsent(audit.runtimeRoot, 'Agent Workspace or auth-copy root remains')
  assert(audit.labels['chora.run_id'] === runID, 'Agent cleanup attribution drifted')
}

async function proveVerifierCleanup(runID, audit) {
  await waitForNoResources('container', audit.filters, 15_000)
  await assertAbsent(audit.workspaceRoot, 'Verifier Workspace remains')
  assert(audit.labels['chora.run_id'] === runID, 'Verifier cleanup attribution drifted')
}

async function waitForNoResources(kind, filters, timeout) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if ((await listDocker(kind, filters)).length === 0) return
    await delay(100)
  }
  throw new Error(`${kind} residue remains after Attempt`)
}

async function listDocker(kind, filters) {
  const noun = kind === 'container' ? ['container', 'ls', '--all'] : ['network', 'ls']
  const args = [...noun, '--quiet', '--no-trunc']
  for (const filter of filters) args.push('--filter', filter)
  const result = await dockerExec(args)
  return result.stdout.toString('utf8').trim().split(/\s+/).filter(Boolean).map((id) => {
    assert(/^[a-f0-9]{64}$/.test(id), `invalid ${kind} identity returned by exact label filter`)
    return id
  })
}

async function inspectContainer(id) {
  const result = await dockerExec(['container', 'inspect', id])
  const documents = parseJSON(result.stdout, 'Docker container inspect')
  assert(Array.isArray(documents) && documents.length === 1 && documents[0].Id === id, 'Docker container inspect identity drift')
  return documents[0]
}

async function inspectNetwork(id) {
  const result = await dockerExec(['network', 'inspect', id])
  const documents = parseJSON(result.stdout, 'Docker network inspect')
  assert(Array.isArray(documents) && documents.length === 1 && documents[0].Id === id, 'Docker network inspect identity drift')
  return documents[0]
}

async function dockerTest(id, testArgs) {
  const result = await dockerExec(['container', 'exec', id, '/usr/bin/test', ...testArgs])
  assert(result.exitCode === 0, 'direct in-container isolation test failed')
}

async function assertFileMode(id, path, mode) {
	const result = await dockerExec(['container', 'exec', id, '/usr/bin/stat', '-c', '%a', path])
	assert(result.stdout.toString('utf8').trim() === mode, `container file ${path} mode drifted`)
}

async function assertNoExecutable(id, path) {
	const result = await directExec(docker, ['--context', 'colima', 'container', 'exec', id, path, '-c', 'exit 0'])
	observeCredentialSurface(result.stdout)
	observeCredentialSurface(result.stderr)
	assert(result.exitCode !== 0, `boundary scratch image unexpectedly exposes ${path}`)
}

async function assertHostBoundary(id) {
	const script = 'set -eu; test ! -e "$1"; if cat "$1" >/dev/null 2>&1; then exit 2; fi; if printf chora-probe >"$1" 2>/dev/null; then exit 3; fi'
	const result = await dockerExec(['container', 'exec', id, '/bin/sh', '-c', script, 'chora-host-boundary', unrelatedHostPath])
	assert(result.exitCode === 0, 'unrelated host path was readable or writable')
}

async function assertDirectEgressDenied(id, host, port) {
	const script = "const net=require('net');let done=false;const finish=(code)=>{if(done)return;done=true;socket.destroy();process.exit(code)};const socket=net.connect({host:process.argv[1],port:Number(process.argv[2])});socket.once('connect',()=>finish(9));socket.once('error',()=>finish(0));setTimeout(()=>finish(0),1500)"
	const result = await dockerExec(['container', 'exec', id, 'node', '-e', script, host, String(port)])
	assert(result.exitCode === 0, 'direct host or model egress bypassed the policy boundary')
}

async function assertProcessTree(container) {
  const result = await dockerExec(['container', 'top', container.Id, '-eo', 'pid,ppid,user,comm'])
  const lines = result.stdout.toString('utf8').trim().split('\n').filter(Boolean)
  const processCount = Math.max(0, lines.length - 1)
  assert(processCount > 0 && processCount <= container.HostConfig.PidsLimit, 'container process tree exceeds effective PID limit')
}

function assertContainerHardening(container, expected) {
  const host = container.HostConfig
  assert(host.ReadonlyRootfs === true && host.Privileged === false, 'container root or privilege boundary drifted')
  assert((host.CapDrop || []).includes('ALL') && !(host.CapAdd || []).length, 'container capability boundary drifted')
  assert((host.SecurityOpt || []).includes('no-new-privileges:true'), 'container no-new-privileges boundary drifted')
  assert(!(host.Devices || []).length && !(host.DeviceRequests || []).length, 'container device boundary drifted')
  assert(host.PidMode !== 'host' && host.IpcMode !== 'host', 'container host namespace fallback detected')
  assert(host.LogConfig?.Type === 'none', 'container Docker logging is not disabled')
  assert(!JSON.stringify(container.Mounts || []).includes('docker.sock'), 'Docker socket mount detected')
  if (expected.user) assert(container.Config.User === expected.user, 'container user identity drifted')
  if (expected.nonRoot) assert(nonRootUser(container.Config.User), 'container effective user is not explicit non-root')
  if (expected.cpus) assert(host.NanoCpus === expected.cpus, 'container CPU limit drifted')
  if (expected.memory) assert(host.Memory === expected.memory && host.MemorySwap === expected.memory, 'container memory limit drifted')
  if (expected.pids) assert(host.PidsLimit === expected.pids, 'container PID limit drifted')
  if (expected.nofile) assertNofile(host.Ulimits, expected.nofile)
  if (expected.bounded) {
    assert(host.NanoCpus > 0 && host.Memory > 0 && host.MemorySwap === host.Memory && host.PidsLimit > 0, 'Verifier resource limits are not bounded')
    assert((host.Ulimits || []).some((item) => item.Name === 'nofile' && item.Soft > 0 && item.Hard === item.Soft), 'Verifier file-descriptor limit is not bounded')
  }
}

function assertAgentTmpfs(agent) {
	const tmpfs = agent.HostConfig.Tmpfs || {}
	const auth = tmpfs['/run/chora/pi'] || ''
	assert(tmpfsOptions(auth, ['rw', 'nosuid', 'nodev', 'noexec', 'uid=1000', 'gid=1000', 'size=16m']),
	  'Agent OAuth root is not exact attempt tmpfs')
}

function assertNoCredentialEnvironment(container) {
	const names = (container.Config.Env || []).map((item) => item.split('=', 1)[0])
	assert(!names.some((name) => /(AUTH|TOKEN|SECRET|PASSWORD|CREDENTIAL|API_KEY)/i.test(name)),
	  'boundary environment exposes credential input')
}

function tmpfsOptions(actual, expected) {
	const values = new Set(String(actual).split(',').filter(Boolean))
	return expected.every((value) => values.has(value))
}

function assertMounts(container, destinations, allowedRoot) {
  const mounts = container.Mounts || []
  assertSetEqual(mounts.map((item) => item.Destination), destinations, 'container mount destinations drifted')
  for (const mount of mounts) {
    assert(mount.Type === 'bind' && pathInside(allowedRoot, mount.Source), 'container bind mount escapes Chora data root')
    assert(!mount.Source.includes('docker.sock') && !mount.Destination.includes('docker.sock'), 'Docker socket mount detected')
    if (mount.Destination === '/input/context') assert(mount.RW === false, 'Agent Context mount is writable')
  }
}

function exactCommonLabels(resources, keys) {
  const first = resources[0].Config?.Labels || resources[0].Labels || {}
  const result = {}
  for (const key of keys) {
    const value = first[key]
    assert(typeof value === 'string' && value !== '', `missing effective label ${key}`)
    for (const resource of resources) {
      const labels = resource.Config?.Labels || resource.Labels || {}
      assert(labels[key] === value, `effective label ${key} is inconsistent`)
    }
    result[key] = value
  }
  return result
}

function safeResourceLabels(labels, keys = ['chora.owner', 'chora.runtime_scope', 'chora.run_id', 'chora.attempt_id', 'chora.task_id', 'chora.policy_digest']) {
	const allowed = new Set(keys)
	const result = {}
	for (const [key, value] of Object.entries(labels || {})) {
	  if (!allowed.has(key)) continue
	  assertSafeIdentity(value, key)
	  result[key] = value
	}
	assert(Object.keys(result).length === allowed.size, 'safe label evidence is incomplete')
	return result
}

function agentRole(container) {
  const name = String(container.Name || '').replace(/^\//, '')
  if (name.endsWith('-attempt')) return 'agent'
  if (name.endsWith('-codex-boundary')) return 'boundary'
  throw new Error('unexpected Agent container role')
}

function limits(container) {
  const host = container.HostConfig
  const nofile = (host.Ulimits || []).find((item) => item.Name === 'nofile')
  return { nanoCpus: host.NanoCpus, memoryBytes: host.Memory, memorySwapBytes: host.MemorySwap, pids: host.PidsLimit, nofile: nofile?.Soft ?? null }
}

function assertNofile(ulimits, value) {
  assert((ulimits || []).some((item) => item.Name === 'nofile' && item.Soft === value && item.Hard === value), 'container file-descriptor limit drifted')
}

function nonRootUser(user) {
  const head = String(user || '').split(':', 1)[0]
  return head !== '' && head !== '0' && head !== 'root'
}

async function dockerExec(args) {
  const result = await directExec(docker, ['--context', 'colima', ...args])
  observeCredentialSurface(result.stdout)
  observeCredentialSurface(result.stderr)
  if (result.exitCode !== 0) throw new Error(`Docker ${args.slice(0, 2).join(' ')} audit exited ${result.exitCode}`)
  return result
}

async function directExec(file, args) {
  try {
    const result = await execFileAsync(file, args, { encoding: 'buffer', maxBuffer: maxCommandBytes, windowsHide: true })
    return { exitCode: 0, stdout: Buffer.from(result.stdout), stderr: Buffer.from(result.stderr) }
  } catch (error) {
    const stdout = Buffer.isBuffer(error.stdout) ? error.stdout : Buffer.from(error.stdout || '')
    const stderr = Buffer.isBuffer(error.stderr) ? error.stderr : Buffer.from(error.stderr || '')
    observeCredentialSurface(stdout)
    observeCredentialSurface(stderr)
    if (Number.isInteger(error.code)) return { exitCode: error.code, stdout, stderr }
    throw new Error('direct child process launch failed')
  }
}

async function api(method, path, body, expected, headers = {}, category = 'api') {
  const response = await fetch(`${baseURL}${path}`, {
    method,
    headers: body === undefined ? headers : { 'Content-Type': 'application/json', ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const bytes = Buffer.from(await response.arrayBuffer())
  observeCredentialSurface(bytes)
  assert(response.status === expected, `${category} request returned unexpected HTTP status`)
  return bytes.length === 0 ? undefined : parseJSON(bytes, `${category} response`)
}

function identityLeaves(document) {
  const leaves = new Map()
  const walk = (value, path = []) => {
    if (Array.isArray(value)) {
      value.forEach((item, index) => walk(item, [...path, String(index)]))
      return
    }
    if (value && typeof value === 'object') {
      for (const [key, item] of Object.entries(value)) walk(item, [...path, key])
      return
    }
    const key = path.at(-1) || ''
    if (/(id|digest|image|policy|identity|adapter|attempt|command)$/i.test(key)) leaves.set(path.join('.'), value)
  }
  walk(document)
  assert(leaves.size > 0, 'Run exposes no immutable identity leaves')
  return leaves
}

function assertIdentityLeaves(expected, actual) {
  const observed = identityLeaves(actual)
  for (const [path, value] of expected) assert(observed.has(path) && observed.get(path) === value, 'immutable Run identity drifted after restart/reopen')
}


function assertRequiredIdentityLeaves(leaves) {
	for (const path of [
	  'id', 'task.id', 'snapshot.id', 'snapshot.digest', 'attemptDetail.id', 'attemptDetail.runtime.sessionId',
	  'verification.id', 'verification.agentAttemptId', 'verification.bindings.baselineDigest',
	  'verification.bindings.patchDigest', 'verification.bindings.contextSnapshotDigest',
	  'verification.bindings.acceptanceContractDigest', 'verification.bindings.verifierPolicyDigest',
	  'verification.attempts.0.id', 'verification.attempts.0.workspaceIdentity', 'verification.result.id',
	  'reviewablePatch.patchDigest', 'reviewablePatch.resultId', 'reviewablePatch.agentAttemptId',
	  'reviewablePatch.verificationAttemptId', 'reviewablePatch.artifactId',
	]) assert(leaves.has(path), `required immutable identity leaf ${path} is absent`)
}

function summarizeIdentityLeaves(leaves) {
	const paths = [...leaves.keys()].sort()
	const encoded = paths.map((path) => `${path}\u0000${JSON.stringify(leaves.get(path))}`).join('\n')
	return { leafCount: paths.length, pathDigest: createHash('sha256').update(encoded).digest('hex'), paths }
}

async function scanDataRoot() {
  await scanTree(dataRoot)
}

async function scanTree(root, allowedSymlinkRoot) {
  for (const entry of await readdir(root, { withFileTypes: true })) {
    const path = resolve(root, entry.name)
    if (entry.isSymbolicLink()) {
	  if (!allowedSymlinkRoot) throw new Error('symlink found in persisted data during credential scan')
	  const target = resolve(dirname(path), await readlink(path))
	  assert(pathInside(allowedSymlinkRoot, target), 'install symlink escapes credential scan root')
	  continue
	}
    if (entry.isDirectory()) await scanTree(path, allowedSymlinkRoot)
    else if (entry.isFile()) await scanFile(path)
  }
}

async function scanFile(path) {
  const handle = await open(path, 'r')
  const chunk = Buffer.alloc(64 << 10)
  let tail = Buffer.alloc(0)
  let maxScalarBytes = 1
  for (const scalar of credentialScalars) maxScalarBytes = Math.max(maxScalarBytes, scalar.length)
  try {
    while (true) {
      const { bytesRead } = await handle.read(chunk, 0, chunk.length, null)
      if (bytesRead === 0) break
      const combined = Buffer.concat([tail, chunk.subarray(0, bytesRead)])
      observeCredentialSurface(combined)
      tail = combined.subarray(Math.max(0, combined.length - maxScalarBytes + 1))
    }
  } finally {
    chunk.fill(0)
    tail.fill(0)
    await handle.close()
  }
}

function collectCredentialScalars(value, output, key = '') {
  if (Array.isArray(value)) {
	for (const item of value) collectCredentialScalars(item, output, key)
    return
  }
  if (value && typeof value === 'object') {
	for (const [name, item] of Object.entries(value)) collectCredentialScalars(item, output, name)
    return
  }
	if (/^(access|refresh)$|(?:access|refresh|id)[_-]?token|token|secret|password|cookie|credential|authorization/i.test(key) && typeof value === 'string' && Buffer.byteLength(value) >= 8) {
	output.push(Buffer.from(value))
	}
}

function observeCredentialSurface(value) {
  const bytes = Buffer.isBuffer(value) ? value : Buffer.from(String(value))
  for (const scalar of credentialScalars) {
    if (scalar.length > 0 && bytes.includes(scalar)) {
      credentialLeak = true
      throw new Error('credential value leaked into audited surface')
    }
  }
}

async function assertAbsent(path, message) {
  try {
    await access(path)
  } catch {
    return
  }
  throw new Error(message)
}

function parseJSON(bytes, label) {
  try {
    return JSON.parse(Buffer.from(bytes).toString('utf8'))
  } catch {
    throw new Error(`${label} is not valid JSON`)
  }
}

function pick(value, ...keys) {
  for (const key of keys) if (Object.hasOwn(value, key)) return value[key]
  return undefined
}

function assertSetEqual(actual, expected, message) {
  const left = [...actual].sort()
  const right = [...expected].sort()
  assert(left.length === right.length && left.every((value, index) => value === right[index]), message)
}

function assertDigest(value, label) {
  assert(typeof value === 'string' && /^[a-f0-9]{64}$/.test(value), `${label} is invalid`)
}

function assertSafeIdentity(value, label) {
  assert(typeof value === 'string' && /^[A-Za-z0-9_.:@+-]{1,256}$/.test(value), `${label} is invalid`)
}

function pathInside(root, child) {
  const rel = relative(resolve(root), resolve(child))
  return rel !== '' && rel !== '..' && !rel.startsWith(`..${process.platform === 'win32' ? '\\' : '/'}`) && !isAbsolute(rel)
}

function sameOrInside(root, child) {
  return resolve(root) === resolve(child) || pathInside(root, child)
}

function disjoint(left, right) {
  return !sameOrInside(left, right) && !sameOrInside(right, left)
}

function absoluteRequired(name) {
  const value = required(name)
  assert(isAbsolute(value), `${name} must be absolute`)
  return resolve(value)
}

function digestRequired(name) {
  const value = required(name)
  assertDigest(value, name)
  return value
}

function integerRequired(name, minimum, maximum) {
  const value = Number(required(name))
  assert(Number.isInteger(value) && value >= minimum && value <= maximum, `${name} is invalid`)
  return value
}

function required(name) {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required`)
  return value
}

function safeError(error) {
  if (!(error instanceof Error)) return 'unexpected internal failure'
  return /^[A-Za-z0-9 _./:@+-]{1,300}$/.test(error.message) ? error.message : 'unexpected internal failure'
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

function delay(milliseconds) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds))
}
