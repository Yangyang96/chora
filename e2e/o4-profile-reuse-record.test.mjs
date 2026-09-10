import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import {
  chmod, link, lstat, mkdir, mkdtemp, readFile, readdir, realpath, rename, stat, symlink,
  writeFile,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import { canonicalJSONStringify } from './o4-candidate-install-orchestrator.mjs'
import {
  PROFILE_REUSE_MANIFEST_SCHEMA,
  PROFILE_REUSE_OBSERVER_ACK_SCHEMA,
  PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA,
  PROFILE_REUSE_RECEIPT_SCHEMA,
  PROFILE_REUSE_RECORD_SCHEMA,
  PROFILE_REUSE_RESOURCE_SCHEMA,
  assertProfileReuseManifest,
  assertProfileReuseReceipt,
  buildProfileReuseRecord as buildProfileReuseRecordRaw,
  collectProfileReuseResourceAtBoundary as collectProfileReuseResourceAtBoundaryRaw,
  computeProfileExecutionIdentityDigest,
  computeProfileRoleTupleDigest,
  formProfileReuseManifest,
  formProfileReuseReceipt,
  persistProfileReuseManifest as persistProfileReuseManifestRaw,
  persistProfileReuseReceipt as persistProfileReuseReceiptRaw,
  persistProfileReuseRecord as persistProfileReuseRecordRaw,
  prepareProfileReuseEvidencePaths,
} from './o4-profile-reuse-record.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'

let repositoryCapture
let cleanupRepositoryCapture
test.before(async () => {
  ({ capture: repositoryCapture, cleanup: cleanupRepositoryCapture } =
    await buildTestRepositoryCapture())
})
test.after(async () => { await cleanupRepositoryCapture() })

function buildProfileReuseRecord(input, dependencies = {}) {
  return buildProfileReuseRecordRaw(
    { ...input, repositoryCapture }, { ...dependencies, repositoryCapture })
}

function collectProfileReuseResourceAtBoundary(input) {
  return collectProfileReuseResourceAtBoundaryRaw({ ...input, repositoryCapture })
}

function persistProfileReuseManifest(value, path, dependencies = {}) {
  return persistProfileReuseManifestRaw(value, path, { ...dependencies, repositoryCapture })
}

function persistProfileReuseReceipt(value, path, dependencies = {}) {
  return persistProfileReuseReceiptRaw(value, path, { ...dependencies, repositoryCapture })
}

function persistProfileReuseRecord(value, path, dependencies = {}) {
  return persistProfileReuseRecordRaw(value, path, { ...dependencies, repositoryCapture })
}

const environmentId = `env_${'1'.repeat(48)}`
const installId = `ins_${'a'.repeat(32)}`
const generationId = `gen_${'b'.repeat(32)}`
const platform = Object.freeze({ os: 'darwin', architecture: 'arm64' })
const phaseNames = ['preflight', 'install', 'product_doctor', 'setup', 'doctor', 'serve']

function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function minimalCapabilityEnforcementDigest() {
  return sha256(canonicalJSONStringify({
    schemaVersion: 'chora.minimal-capability-enforcement.v1',
    profile: 'minimal',
    capabilityPolicy: 'chora.minimal.v1',
    capabilityBundleId: 'chora.minimal-core.v1',
    allowedTools: ['read', 'bash', 'edit', 'write'],
    prohibitedTools: ['grep', 'find', 'ls'],
    runtimeFingerprint: `sha256:${sha256('managed-pi-runtime-source')}`,
    enforcementBoundary: 'adapter_and_docker_supervisor_before_model',
  }))
}
function id(prefix, number) {
  const hex = number.toString(16).padStart(12, '0')
  return `${prefix}_00000000-0000-7000-8000-${hex}`
}
function image(seed, artifactId) {
  return {
    artifactId,
    archiveSha256: sha256(`archive:${seed}`),
    archiveSize: 100 + seed.length,
    dockerConfigImageId: `sha256:${sha256(`config:${seed}`)}`,
  }
}
function roleImages() {
  const managed = image('managed', 'managed-pi-runtime')
  return {
    managed_pi_runtime: managed,
    network_boundary: image('boundary', 'network-boundary'),
    independent_verifier: { ...managed },
    capability_probe: { ...managed },
  }
}
function emptyResidue() {
  return {
    ownedContainers: 0, ownedNetworks: 0, ownedVolumes: 0, ownedConfigs: 0,
    ownedWorkspaces: 0, ownedProcessGroups: 0, activeReferences: 0, recoverableReferences: 0,
  }
}
function emptyInventory() {
  return Object.fromEntries(Object.keys(emptyResidue()).map((key) => [key, []]))
}

function commandAudit() {
  const drafts = []
  for (let journey = 1; journey <= 4; journey++) {
    drafts.push({ phase: 'attempt', subsystem: 'managed', invocation: 'run', operationClass: 'exec', safeTargetSha256: sha256(`attempt:${journey}`), result: 'succeeded' })
    drafts.push({ phase: 'verifier', subsystem: 'verifier', invocation: 'run', operationClass: 'inspect', safeTargetSha256: sha256(`verifier:${journey}`), result: 'succeeded' })
  }
  drafts.push({ phase: 'verifier', subsystem: 'verifier', invocation: 'run', operationClass: 'inspect', safeTargetSha256: sha256('verifier:trusted-local'), result: 'succeeded' })
  let previousDigest = '0'.repeat(64)
  return drafts.map((draft, index) => {
    const withoutDigest = { sequence: index + 1, ...draft, previousDigest }
    const digest = sha256(canonicalJSONStringify(withoutDigest))
    previousDigest = digest
    return { ...withoutDigest, digest }
  })
}

async function writeReadonlyJSON(path, value) {
  await writeFile(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600, flag: 'wx' })
  await chmod(path, 0o400)
}

async function createPinnedObserver(root, source = '#!/bin/sh\nexit 0\n') {
  const observerFile = join(root, 'profile-reuse-observer')
  await writeFile(observerFile, source, { mode: 0o500, flag: 'wx' })
  await chmod(observerFile, 0o500)
  return { observerFile, observerSha256: sha256(await readFile(observerFile)) }
}

function syntheticObserverProgram({ forgeAck = false, spliceResourceAck = false, substituteResource = false, mutateA3 = false, fail = false, hang = false, hostileDescendant = null } = {}) {
  return `#!${process.execPath}
import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { appendFile, chmod, readFile, writeFile } from 'node:fs/promises'
const argumentsList = process.argv.slice(2)
const options = {}
for (let index = 0; index < argumentsList.length; index += 2) options[argumentsList[index]] = argumentsList[index + 1]
const request = JSON.parse(await readFile(options['--request'], 'utf8'))
${hostileDescendant ? `const descendantSource = ${JSON.stringify(`
  const { writeFileSync } = require('node:fs')
  const [resourceFile, ackFile] = process.argv.slice(1)
  setTimeout(() => {
    writeFileSync(resourceFile, 'late descendant resource')
    writeFileSync(ackFile, 'late descendant acknowledgement')
  }, 1500)
  setTimeout(() => process.exit(0), 1800)
`)}
spawn(process.execPath, ['-e', descendantSource,
  options['--attested-resource-output'], options['--ack-output']], { stdio: 'ignore' })
${hostileDescendant === 'oversize' ? "process.stdout.write('x'.repeat(70 * 1024))" : ''}
await new Promise((resolve) => setTimeout(resolve, 60_000))` : ''}
const digest = (value) => createHash('sha256').update(value).digest('hex')
const canonical = (value) => {
  if (value === null || typeof value !== 'object') return JSON.stringify(value)
  if (Array.isArray(value)) return '[' + value.map(canonical).join(',') + ']'
  return '{' + Object.keys(value).sort().map((key) => JSON.stringify(key) + ':' + canonical(value[key])).join(',') + '}'
}
const observedAt = new Date(Math.max(Date.now(), Date.parse(request.requestedAt))).toISOString()
let resourceBytes = await readFile(options['--resource-input'])
${substituteResource ? "resourceBytes = Buffer.from(resourceBytes.toString('utf8') + ' ')" : ''}
await writeFile(options['--attested-resource-output'], resourceBytes, { flag: 'wx', mode: 0o600 })
await chmod(options['--attested-resource-output'], 0o400)
const ackDraft = {
  schemaVersion: '${PROFILE_REUSE_OBSERVER_ACK_SCHEMA}', status: 'observed',
  sequence: request.sequence, journey: request.journey,
  requestDigest: ${forgeAck ? "digest('forged-request')" : 'request.requestDigest'},
  resourceSha256: ${spliceResourceAck ? "digest('spliced-resource')" : 'digest(resourceBytes)'}, observerSha256: request.observerSha256, observedAt,
}
const ack = { ...ackDraft, ackDigest: digest(canonical(ackDraft)) }
await writeFile(options['--ack-output'], JSON.stringify(ack, null, 2) + '\\n', { flag: 'wx', mode: 0o600 })
await chmod(options['--ack-output'], 0o400)
${mutateA3 ? "await appendFile(options['--source-a3-ledger-file'], ' ' )" : ''}
${fail ? 'process.exit(17)' : ''}
${hang ? 'await new Promise((resolve) => setTimeout(resolve, 60_000))' : ''}
`
}

function tuple() {
  const repositoryRoot = '/private/tmp/chora-o4-profile-reuse-repository'
  const repositoryCommit = 'c'.repeat(40)
  const repositoryDraft = {
    schemaVersion: 'chora.m1-o4-repository-authority.v2', repositoryRoot,
    repositoryCommit, headRef: 'refs/heads/main',
    root: { path: repositoryRoot, type: 'directory', mode: 0o700,
      uid: process.getuid(), gid: process.getgid(), dev: '1', ino: '2' },
    gitRoot: { path: `${repositoryRoot}/.git`, type: 'directory', mode: 0o700,
      uid: process.getuid(), gid: process.getgid(), dev: '1', ino: '3' },
    configSha256: sha256('repository-config'),
    index: [{ flag: 'H', mode: '100644', object: 'd'.repeat(40), stage: 0, path: 'README.md' }],
    refs: [{ name: 'refs/heads/main', object: repositoryCommit }],
    worktree: [{ path: 'README.md', type: 'file', mode: 0o644, sha256: sha256('README') }],
  }
  const repositoryAuthority = { ...repositoryDraft,
    repositoryClosureSha256: sha256(canonicalJSONStringify(repositoryDraft)) }
  const digestBindings = [
    'sourceManifestSha256', 'sourceAggregateSha256', 'candidateManifestSha256',
    'binarySha256', 'webAggregateSha256', 'releaseSpecSha256', 'releaseManifestSha256',
    'offlineBundleEvidenceSha256', 'pathPiProvenanceSha256', 'privatePiProvenanceSha256',
    'modelAuthoritySha256', 'endpointEvidenceSha256', 'preflightFileSha256',
    'preflightDigest', 'actualReleaseManifestSha256', 'privatePiManifestSha256',
    'authFileSha256', 'caFileSha256', 'colimaSourceSha256', 'dockerClientSourceSha256',
    'dockerCLISha256', 'dockerContextSha256', 'endpointDigest', 'roleTupleSha256',
    'privateRuntimeConfigSha256',
  ]
  const bindings = Object.fromEntries(digestBindings.map((key) => [key, sha256(key)]))
  Object.assign(bindings, { repositoryCommit,
    repositoryClosureSha256: repositoryAuthority.repositoryClosureSha256 })
  const safe = {
    schemaVersion: 'chora.m1-o4-candidate-install-tuple.v2',
    status: 'frozen', environmentId, installId, generationId, platform,
    roots: Object.fromEntries([
      'installRootSha256', 'dataRootSha256', 'stateRootSha256', 'receiptDirectorySha256',
      'privatePiRootSha256', 'probeRuntimeRootSha256', 'colimaToolRootSha256',
    ].map((key) => [key, sha256(key)])),
    repositoryAuthority, bindings,
    plan: ['offline-prebuilt-install', 'product-doctor', 'setup-once', 'installed-doctor',
      'process-boundary', 'active-serve'],
  }
  return { ...safe, identity: sha256(canonicalJSONStringify(safe)) }
}

function shared(candidateTuple) {
  return {
    environmentId, installId, generationId, platform,
    bindingDigest: sha256(canonicalJSONStringify(candidateTuple.bindings)),
    tupleIdentity: candidateTuple.identity,
  }
}

async function createHandoffChain(directory, candidateTuple) {
  let previousReceiptDigest = null
  for (let index = 0; index < phaseNames.length; index++) {
    const phase = phaseNames[index]
    const safe = {
      schemaVersion: 'chora.m1-o4-candidate-phase-receipt.v1',
      tupleIdentity: candidateTuple.identity,
      phase, sequence: index + 1, status: 'passed',
      operationDigest: sha256(`operation:${phase}`),
      observationDigest: phase === 'install' ? null : sha256(`observation:${phase}`),
      previousReceiptDigest,
    }
    const receipt = { ...safe, receiptDigest: sha256(canonicalJSONStringify(safe)) }
    await writeReadonlyJSON(join(directory, `${String(index + 1).padStart(2, '0')}-${phase}.json`), receipt)
    previousReceiptDigest = receipt.receiptDigest
  }
}

function profileEvidence(sequence) {
  if (sequence === 1) return {
    bashObserved: true,
    editObserved: true,
    prohibitedCapabilityDenialCount: 1,
    prohibitedCapabilityDenialDigest: minimalCapabilityEnforcementDigest(),
    independentVerification: true,
  }
  if (sequence <= 4) {
    const applies = sequence === 2
    return {
      fullRepositoryPolicy: true,
      securityPolicy: true,
      independentVerification: true,
      acceptedReview: applies,
      taskOwnedApply: applies,
      patchDigest: applies ? sha256('accepted-patch') : null,
      applyTargetDigest: applies ? sha256('task-owned-target') : null,
    }
  }
  const policy = 'chora.trusted-local-disclosure.v1'
  const terminalInventory = emptyInventory()
  return {
    resolutionOrder: 'path_first_private_fallback',
    selectedSource: 'path',
    acknowledgementPolicyVersion: policy,
    acknowledgementPolicySha256: sha256(policy),
    acknowledgedAt: '2026-08-28T08:00:00.000Z',
    acknowledgementCurrent: true,
    executionTarget: 'trusted-host',
    noSandbox: true,
    choraResultSemantics: true,
    selectedPiPath: '/private/tmp/chora-o4/pi/bin/pi',
    selectedPiSourceRoot: '/private/tmp/chora-o4/pi/source',
    piExecutableSha256: sha256('trusted-pi-executable'),
    piSourceProvenanceSha256: sha256('trusted-pi-source'),
    runtimeFingerprint: sha256('trusted-runtime-fingerprint'),
    processGroupIdentitySha256: sha256('trusted-process-group'),
    sessionIdentitySha256: sha256('trusted-session'),
    processStartedAt: '2026-08-28T08:05:00.000Z',
    processExitedAt: '2026-08-28T08:05:30.000Z',
    processGroupTerminated: true,
    sessionClosed: true,
    terminalCleanupDigest: sha256(canonicalJSONStringify(terminalInventory)),
  }
}

test('real Minimal journey cannot substitute model-authored denial text for boundary enforcement', async () => {
  const source = await readFile(new URL('./o4-profile-reuse-real.spec.ts', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /const denials = trajectory\.filter/)
  assert.doesNotMatch(source, /requirement:.*(?:prohibited[- _]?capability|capability denial)/i)
  assert.match(source, /chora\.minimal-capability-enforcement\.v1/)
  assert.match(source, /adapter_and_docker_supervisor_before_model/)
  assert.match(source, /model-authored denial text is not capability-enforcement evidence/)
})

function receiptDraft(sequence, candidateTuple, resourceLedgerSha256, audit, attestation) {
  const profile = sequence === 1 ? 'minimal' : sequence <= 4 ? 'standard' : 'trusted_local'
  const journey = sequence === 1 ? 'minimal' : sequence <= 4 ? `standard_${sequence - 1}` : 'trusted_local'
  const managed = profile !== 'trusted_local'
  const identity = {
    tupleIdentity: candidateTuple.identity,
    taskId: id('task', sequence),
    runId: id('run', sequence + 10),
    attemptId: id('attempt', sequence + 20),
    workspaceIdentity: `sha256:${sha256(`workspace:${sequence}`)}`,
  }
  const evidence = profileEvidence(sequence)
  const attemptIndex = (sequence - 1) * 2
  const verifierIndex = sequence === 5 ? 8 : attemptIndex + 1
  const started = new Date(Date.UTC(2026, 7, 27, 8, sequence, 0)).toISOString()
  const completed = new Date(Date.UTC(2026, 7, 27, 8, sequence, 30)).toISOString()
  const runtimeIdentitySha256 = managed ? sha256(`runtime:${sequence}`) :
    sha256(canonicalJSONStringify({
      runtimeFingerprint: evidence.runtimeFingerprint,
      processGroupIdentitySha256: evidence.processGroupIdentitySha256,
      sessionIdentitySha256: evidence.sessionIdentitySha256,
    }))
  return {
    schemaVersion: PROFILE_REUSE_RECEIPT_SCHEMA,
    status: 'passed', sequence, journey, profile,
    cohortSequence: profile === 'standard' ? sequence - 1 : null,
    ...shared(candidateTuple),
    ...identity,
    executionIdentityDigest: computeProfileExecutionIdentityDigest(identity),
    attemptSequence: 1,
    attemptState: 'output_submitted',
    terminalReason: null,
    runtimeAdapterId: 'pi',
    runtimeSource: managed ? 'managed_pi_image' : 'local_pi',
    executionProvider: managed ? 'docker' : 'trusted_host',
    capabilityPolicy: profile === 'minimal' ? 'chora.minimal.v1' : profile === 'standard' ? 'chora.standard.v1' : 'pi.native',
    sandboxState: managed ? 'managed' : 'none',
    managedSandbox: managed,
    managedFallbackUsed: false,
    frameworkRetryCount: 0,
    hiddenRetryCount: 0,
    attemptSource: managed ? {
      startSequence: attemptIndex + 1,
      endSequence: attemptIndex + 1,
      sliceDigest: sha256(canonicalJSONStringify([audit[attemptIndex]])),
    } : null,
    verifierSource: {
      startSequence: verifierIndex + 1,
      endSequence: verifierIndex + 1,
      sliceDigest: sha256(canonicalJSONStringify([audit[verifierIndex]])),
    },
    runtimeIdentitySha256,
    resourceLedgerSha256,
    resourceRequestDigest: attestation.request.requestDigest,
    resourceAckDigest: attestation.ack.ackDigest,
    resourceObserverSha256: attestation.request.observerSha256,
    roleTupleDigest: managed ? computeProfileRoleTupleDigest(roleImages()) : null,
    operationCounts: managed ? { build: 0, pull: 0, load: 0 } : null,
    terminalResidue: emptyResidue(),
    publicActionDigest: sha256(`public-actions:${sequence}`),
    publicObservationDigest: sha256(`public-observations:${sequence}`),
    publicStartedAt: started,
    publicCompletedAt: completed,
    profileEvidence: evidence,
  }
}

function resourceLedger(sequence, candidateTuple) {
  const profile = sequence === 1 ? 'minimal' : sequence <= 4 ? 'standard' : 'trusted_local'
  const identity = {
    tupleIdentity: candidateTuple.identity,
    taskId: id('task', sequence),
    runId: id('run', sequence + 10),
    attemptId: id('attempt', sequence + 20),
    workspaceIdentity: `sha256:${sha256(`workspace:${sequence}`)}`,
  }
  const managed = profile !== 'trusted_local'
  const trustedEvidence = managed ? null : profileEvidence(sequence)
  const runtimeIdentitySha256 = managed ? sha256(`runtime:${sequence}`) :
    sha256(canonicalJSONStringify({
      runtimeFingerprint: trustedEvidence.runtimeFingerprint,
      processGroupIdentitySha256: trustedEvidence.processGroupIdentitySha256,
      sessionIdentitySha256: trustedEvidence.sessionIdentitySha256,
    }))
  return {
    schemaVersion: PROFILE_REUSE_RESOURCE_SCHEMA,
    status: 'terminal', profile,
    ...identity,
    executionIdentityDigest: computeProfileExecutionIdentityDigest(identity),
    runtimeIdentitySha256,
    managedInventory: managed ? emptyInventory() : null,
    trustedHost: managed ? null : {
      selectedPiPath: trustedEvidence.selectedPiPath,
      selectedPiSourceRoot: trustedEvidence.selectedPiSourceRoot,
      selectedSource: trustedEvidence.selectedSource,
      piExecutableSha256: trustedEvidence.piExecutableSha256,
      piSourceProvenanceSha256: trustedEvidence.piSourceProvenanceSha256,
      runtimeFingerprint: trustedEvidence.runtimeFingerprint,
      processGroupIdentitySha256: trustedEvidence.processGroupIdentitySha256,
      sessionIdentitySha256: trustedEvidence.sessionIdentitySha256,
      processStartedAt: trustedEvidence.processStartedAt,
      processExitedAt: trustedEvidence.processExitedAt,
      processGroupTerminated: trustedEvidence.processGroupTerminated,
      sessionClosed: trustedEvidence.sessionClosed,
      terminalCleanupDigest: trustedEvidence.terminalCleanupDigest,
      terminalInventory: emptyInventory(),
      verifierSource: {
        startSequence: 9,
        endSequence: 9,
        sliceDigest: sha256(canonicalJSONStringify([commandAudit()[8]])),
      },
    },
  }
}

function observerEvidence(sequence, candidateTuple, resourceSha256, observerSha256 = sha256('pinned-profile-reuse-observer')) {
  const profile = sequence === 1 ? 'minimal' : sequence <= 4 ? 'standard' : 'trusted_local'
  const identity = {
    tupleIdentity: candidateTuple.identity,
    taskId: id('task', sequence),
    runId: id('run', sequence + 10),
    attemptId: id('attempt', sequence + 20),
    workspaceIdentity: `sha256:${sha256(`workspace:${sequence}`)}`,
  }
  const requestDraft = {
    schemaVersion: PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA,
    status: 'requested', sequence,
    journey: sequence === 1 ? 'minimal' : sequence <= 4 ? `standard_${sequence - 1}` : 'trusted_local',
    profile, ...identity,
    executionIdentityDigest: computeProfileExecutionIdentityDigest(identity),
    publicObservationDigest: sha256(`public-observations:${sequence}`),
    publicCompletedAt: new Date(Date.UTC(2026, 7, 27, 8, sequence, 30)).toISOString(),
    rawResourceSha256: resourceSha256,
    requestedAt: new Date(Date.UTC(2026, 7, 28, 8, sequence, 31)).toISOString(),
    requestNonce: sha256(`observer-nonce:${sequence}`),
    observerSha256, ownedPathController: repositoryCapture,
  }
  const request = { ...requestDraft, requestDigest: sha256(canonicalJSONStringify(requestDraft)) }
  const ackDraft = {
    schemaVersion: PROFILE_REUSE_OBSERVER_ACK_SCHEMA,
    status: 'observed', sequence, journey: request.journey,
    requestDigest: request.requestDigest, resourceSha256, observerSha256,
    observedAt: new Date(Date.UTC(2026, 7, 28, 8, sequence, 32)).toISOString(),
  }
  return {
    request,
    ack: { ...ackDraft, ackDigest: sha256(canonicalJSONStringify(ackDraft)) },
  }
}

function sourceAttempt(receipt, ordinal) {
  return {
    sourceAttemptOrdinal: ordinal,
    profile: receipt.profile,
    cohortSequence: receipt.cohortSequence,
    taskId: receipt.taskId,
    runId: receipt.runId,
    attemptId: receipt.attemptId,
    scenario: null,
    attemptState: 'output_submitted',
    terminalReason: null,
    verifierRequired: true,
    verifierStartSequence: receipt.verifierSource.startSequence,
    verifierEndSequence: receipt.verifierSource.endSequence,
    verifierSliceDigest: receipt.verifierSource.sliceDigest,
    workspaceIdentity: receipt.workspaceIdentity,
    tupleIdentity: receipt.tupleIdentity,
    executionIdentityDigest: receipt.executionIdentityDigest,
    status: 'succeeded',
    runtimeIdentitySha256: receipt.runtimeIdentitySha256,
    resourceLedgerSha256: receipt.resourceLedgerSha256,
    roleTupleDigest: receipt.roleTupleDigest,
    frameworkRetryCount: 0,
    hiddenRetryCount: 0,
    auditStartSequence: receipt.attemptSource.startSequence,
    auditEndSequence: receipt.attemptSource.endSequence,
    auditSliceDigest: receipt.attemptSource.sliceDigest,
    operationCounts: receipt.operationCounts,
    terminalResidue: receipt.terminalResidue,
  }
}

async function fixture() {
  const root = await mkdtemp(join(await realpath(tmpdir()), 'chora-o4-profile-reuse-'))
  await chmod(root, 0o700)
  const receiptDirectory = join(root, 'receipts')
  const resourceLedgerDirectory = join(root, 'resources')
  const observerRequestDirectory = join(root, 'observer-requests')
  const observerAckDirectory = join(root, 'observer-acks')
  const handoffReceiptDirectory = join(root, 'handoff')
  await Promise.all([
    mkdir(receiptDirectory, { mode: 0o700 }),
    mkdir(resourceLedgerDirectory, { mode: 0o700 }),
    mkdir(observerRequestDirectory, { mode: 0o700 }),
    mkdir(observerAckDirectory, { mode: 0o700 }),
    mkdir(handoffReceiptDirectory, { mode: 0o700 }),
  ])
  const candidateTuple = tuple()
  const tupleFile = join(root, 'tuple.json')
  await writeReadonlyJSON(tupleFile, candidateTuple)
  await createHandoffChain(handoffReceiptDirectory, candidateTuple)
  const audit = commandAudit()
  const images = roleImages()
  const sourceA3LedgerFile = join(root, 'source-a3.json')

  const receipts = []
  const receiptIndex = []
  const resourceIndex = []
  const observerAttestations = []
  for (let sequence = 1; sequence <= 5; sequence++) {
    const journey = sequence === 1 ? 'minimal' : sequence <= 4 ? `standard-${sequence - 1}` : 'trusted-local'
    const resourceName = `${String(sequence).padStart(2, '0')}-${journey}-resources.json`
    const resourcePath = join(resourceLedgerDirectory, resourceName)
    const resource = resourceLedger(sequence, candidateTuple)
    await writeReadonlyJSON(resourcePath, resource)
    const resourceBytes = await readFile(resourcePath)
    const resourceSha256 = sha256(resourceBytes)
    resourceIndex.push({ sequence, file: resourceName, sha256: resourceSha256 })
    const attestation = observerEvidence(sequence, candidateTuple, resourceSha256)
    const requestName = `${String(sequence).padStart(2, '0')}-${journey}-request.json`
    const ackName = `${String(sequence).padStart(2, '0')}-${journey}-ack.json`
    const requestPath = join(observerRequestDirectory, requestName)
    const ackPath = join(observerAckDirectory, ackName)
    await writeReadonlyJSON(requestPath, attestation.request)
    await writeReadonlyJSON(ackPath, attestation.ack)
    observerAttestations.push({
      sequence,
      requestFile: requestName,
      requestSha256: sha256(await readFile(requestPath)),
      requestDigest: attestation.request.requestDigest,
      ackFile: ackName,
      ackSha256: sha256(await readFile(ackPath)),
      ackDigest: attestation.ack.ackDigest,
      observerSha256: attestation.request.observerSha256,
      resourceSha256,
    })

    const receipt = formProfileReuseReceipt(receiptDraft(
      sequence, candidateTuple, resourceSha256, audit, attestation))
    const receiptName = `${String(sequence).padStart(2, '0')}-${journey}.json`
    const receiptPath = join(receiptDirectory, receiptName)
    await persistProfileReuseReceipt(receipt, receiptPath)
    receiptIndex.push({ sequence, file: receiptName, sha256: sha256(await readFile(receiptPath)) })
    receipts.push(receipt)
  }
  const sourceA3 = {
    schemaVersion: 'chora.m1-o4-sealed-a3-runner-ledger.v1',
    status: 'recording',
    ...shared(candidateTuple),
    observationSource: 'exact-runner-command-audit',
    complete: false,
    engineEventsUsed: false,
    roleImages: images,
    commandAudit: audit,
    auditRecordCount: audit.length,
    auditFinalDigest: audit.at(-1).digest,
    auditSealed: false,
    attempts: receipts.slice(0, 4).map((receipt, index) => sourceAttempt(receipt, index + 1)),
  }
  await writeFile(sourceA3LedgerFile, `${JSON.stringify(sourceA3, null, 2)}\n`, { mode: 0o600 })
  const sourceBytes = await readFile(sourceA3LedgerFile)
  const manifest = formProfileReuseManifest({
    schemaVersion: PROFILE_REUSE_MANIFEST_SCHEMA,
    status: 'complete',
    ...shared(candidateTuple),
    sourceA3LedgerSha256: sha256(sourceBytes),
    sourceA3AuditRecordCount: audit.length,
    sourceA3AuditFinalDigest: audit.at(-1).digest,
    roleImages: images,
    roleTupleDigest: computeProfileRoleTupleDigest(images),
    receipts: receiptIndex,
    resourceLedgers: resourceIndex,
    observerAttestations,
  })
  const manifestFile = join(root, 'manifest.json')
  const recordFile = join(root, 'phase-c-output', 'phase-c-record.json')
  await persistProfileReuseManifest(manifest, manifestFile)
  return {
    root, receiptDirectory, resourceLedgerDirectory, observerRequestDirectory,
    observerAckDirectory, handoffReceiptDirectory,
    tupleFile, sourceA3LedgerFile, sourceA3, manifestFile, manifest, receipts, recordFile,
  }
}

async function build(files, dependencies = {}) {
  return buildProfileReuseRecord({
    manifestFile: files.manifestFile,
    receiptDirectory: files.receiptDirectory,
    resourceLedgerDirectory: files.resourceLedgerDirectory,
    observerRequestDirectory: files.observerRequestDirectory,
    observerAckDirectory: files.observerAckDirectory,
    sourceA3LedgerFile: files.sourceA3LedgerFile,
    tupleFile: files.tupleFile,
    handoffReceiptDirectory: files.handoffReceiptDirectory,
    recordFile: files.recordFile,
  }, dependencies)
}

async function writeBoundarySource(files, attemptCount) {
  const source = structuredClone(files.sourceA3)
  source.attempts = source.attempts.slice(0, attemptCount)
  await chmod(files.sourceA3LedgerFile, 0o600)
  await writeFile(files.sourceA3LedgerFile, `${JSON.stringify(source, null, 2)}\n`)
}

async function stageSupervisorResource(files, supervisorResourceDirectory, sequence) {
  const journey = sequence === 1 ? 'minimal' : sequence <= 4 ? `standard-${sequence - 1}` : 'trusted-local'
  const name = `${String(sequence).padStart(2, '0')}-${journey}-resources.json`
  const bytes = await readFile(join(files.resourceLedgerDirectory, name))
  const path = join(supervisorResourceDirectory, name)
  await writeFile(path, bytes, { flag: 'wx', mode: 0o600 })
  await chmod(path, 0o400)
  return path
}

async function prepareProtocol(files, observerSource, attemptCount = 1, observerTimeoutMs = 60_000) {
  const runnerProtocolDirectory = join(files.root, 'observer-protocol')
  const supervisorResourceDirectory = join(files.root, 'supervisor-resources')
  await mkdir(runnerProtocolDirectory, { mode: 0o700 })
  await mkdir(supervisorResourceDirectory, { mode: 0o700 })
  await writeBoundarySource(files, attemptCount)
  const observer = await createPinnedObserver(files.root, observerSource)
  const paths = await prepareProfileReuseEvidencePaths({
    tupleFile: files.tupleFile,
    sourceA3LedgerFile: files.sourceA3LedgerFile,
    handoffReceiptDirectory: files.handoffReceiptDirectory,
    ...observer,
    supervisorResourceDirectory,
    runnerProtocolDirectory,
    outputDirectory: join(files.root, 'real-output'),
    observerTimeoutMs,
  })
  return { paths, observer, supervisorResourceDirectory }
}

async function collectFixtureBoundary(files, protocol, sequence) {
  const receipt = files.receipts[sequence - 1]
  await stageSupervisorResource(files, protocol.supervisorResourceDirectory, sequence)
  return collectProfileReuseResourceAtBoundary({
    paths: protocol.paths,
    sequence,
    journey: receipt.journey,
    profile: receipt.profile,
    tupleIdentity: receipt.tupleIdentity,
    taskId: receipt.taskId,
    runId: receipt.runId,
    attemptId: receipt.attemptId,
    workspaceIdentity: receipt.workspaceIdentity,
    publicObservationDigest: receipt.publicObservationDigest,
    publicCompletedAt: receipt.publicCompletedAt,
    runtimeFingerprint: receipt.profile === 'trusted_local' ?
      receipt.profileEvidence.runtimeFingerprint : undefined,
    requestNonce: sha256(`live-protocol-nonce:${sequence}`),
  })
}

async function copyReadonly(source, target) {
  await writeFile(target, await readFile(source), { flag: 'wx', mode: 0o600 })
  await chmod(target, 0o400)
}

async function runCompleteProtocolCohort() {
  const files = await fixture()
  const protocol = await prepareProtocol(files, syntheticObserverProgram(), 1)
  const results = []
  for (let sequence = 1; sequence <= 5; sequence++) {
    await writeBoundarySource(files, Math.min(sequence, 4))
    const before = sha256(await readFile(files.sourceA3LedgerFile))
    const result = await collectFixtureBoundary(files, protocol, sequence)
    assert.equal(sha256(await readFile(files.sourceA3LedgerFile)), before,
      'observer mutated A3 during the successful protocol')
    results.push(result)
  }

  const receiptIndex = []
  const resourceIndex = []
  const observerAttestations = []
  for (let index = 0; index < results.length; index++) {
    const sequence = index + 1
    const result = results[index]
    const baseReceipt = structuredClone(files.receipts[index])
    delete baseReceipt.receiptDigest
    baseReceipt.resourceRequestDigest = result.requestDigest
    baseReceipt.resourceAckDigest = result.ackDigest
    baseReceipt.resourceObserverSha256 = result.observerSha256
    const receipt = formProfileReuseReceipt(baseReceipt)
    const receiptName = receiptNamesForTest()[index]
    const resourceName = resourceNamesForTest()[index]
    const requestName = result.requestFile.split('/').at(-1)
    const ackName = result.ackFile.split('/').at(-1)
    const receiptPath = join(protocol.paths.receiptDirectory, receiptName)
    const resourcePath = join(protocol.paths.resourceLedgerDirectory, resourceName)
    const requestPath = join(protocol.paths.observerRequestDirectory, requestName)
    const ackPath = join(protocol.paths.observerAckDirectory, ackName)
    await persistProfileReuseReceipt(receipt, receiptPath)
    await copyReadonly(result.resourceFile, resourcePath)
    await copyReadonly(result.requestFile, requestPath)
    await copyReadonly(result.ackFile, ackPath)
    receiptIndex.push({ sequence, file: receiptName, sha256: sha256(await readFile(receiptPath)) })
    resourceIndex.push({ sequence, file: resourceName, sha256: result.resourceSha256 })
    observerAttestations.push({
      sequence, requestFile: requestName, requestSha256: result.requestSha256,
      requestDigest: result.requestDigest, ackFile: ackName, ackSha256: result.ackSha256,
      ackDigest: result.ackDigest, observerSha256: result.observerSha256,
      resourceSha256: result.resourceSha256,
    })
  }
  const sourceBytes = await readFile(files.sourceA3LedgerFile)
  const manifest = formProfileReuseManifest({
    schemaVersion: PROFILE_REUSE_MANIFEST_SCHEMA,
    status: 'complete',
    ...shared(JSON.parse(await readFile(files.tupleFile, 'utf8'))),
    sourceA3LedgerSha256: sha256(sourceBytes),
    sourceA3AuditRecordCount: files.sourceA3.auditRecordCount,
    sourceA3AuditFinalDigest: files.sourceA3.auditFinalDigest,
    roleImages: files.sourceA3.roleImages,
    roleTupleDigest: computeProfileRoleTupleDigest(files.sourceA3.roleImages),
    receipts: receiptIndex,
    resourceLedgers: resourceIndex,
    observerAttestations,
  })
  const manifestFile = join(protocol.paths.outputDirectory, 'manifest.json')
  await persistProfileReuseManifest(manifest, manifestFile)
  const record = await buildProfileReuseRecord({
    manifestFile,
    receiptDirectory: protocol.paths.receiptDirectory,
    resourceLedgerDirectory: protocol.paths.resourceLedgerDirectory,
    observerRequestDirectory: protocol.paths.observerRequestDirectory,
    observerAckDirectory: protocol.paths.observerAckDirectory,
    sourceA3LedgerFile: files.sourceA3LedgerFile,
    tupleFile: files.tupleFile,
    handoffReceiptDirectory: files.handoffReceiptDirectory,
    recordFile: join(protocol.paths.outputDirectory, 'phase-c-record.json'),
  })
  return { files, protocol, results, manifest, record }
}

function receiptNamesForTest() {
  return ['01-minimal.json', '02-standard-1.json', '03-standard-2.json',
    '04-standard-3.json', '05-trusted-local.json']
}

function resourceNamesForTest() {
  return ['01-minimal-resources.json', '02-standard-1-resources.json',
    '03-standard-2-resources.json', '04-standard-3-resources.json',
    '05-trusted-local-resources.json']
}

test('validates the exact Minimal -> Standard 1/2/3 -> Trusted Local phase-C sequence', async () => {
  const files = await fixture()
  const record = await build(files)
  assert.equal(record.schemaVersion, PROFILE_REUSE_RECORD_SCHEMA)
  assert.equal(record.standardReuseCount, 3)
  assert.equal(record.journeyReceiptDigests.length, 5)
  assert.equal(record.a3SealedByPhaseC, false)
  assert.equal(record.finalProfileJourneysFormed, false)
  assert.equal(record.b1CompositeFormed, false)
  assert.equal(record.operationDigest.length, 64)
  assert.equal(record.observationDigest.length, 64)
  assert.equal(record.handoffReceiptDigest.length, 64)
  assert.equal(record.recordDigest.length, 64)
  assert.equal(files.sourceA3.auditSealed, false)
  const phaseC = JSON.parse(await readFile(join(files.handoffReceiptDirectory, '07-C.json'), 'utf8'))
  assert.equal(phaseC.phase, 'C')
  assert.equal(phaseC.operationDigest, record.operationDigest)
  assert.equal(phaseC.observationDigest, record.observationDigest)
  assert.equal((await stat(join(files.handoffReceiptDirectory, '07-C.json'))).mode & 0o777, 0o400)
  assert.equal((await stat(files.recordFile)).mode & 0o777, 0o400)
  assert.deepEqual(JSON.parse(await readFile(files.recordFile, 'utf8')), record)
  await assert.rejects(persistProfileReuseRecord(record, files.recordFile), /EEXIST/)
})

test('phase C prepares and reopens the exact record before publishing its sole commit receipt', async () => {
  const files = await fixture()
  let inspected = false
  const record = await build(files, {
    beforeReceiptPublish: async ({ handoffReceipt, record: prepared, recordFile }) => {
      inspected = true
      assert.equal(recordFile, files.recordFile)
      assert.equal((await stat(recordFile)).mode & 0o777, 0o400)
      assert.deepEqual(JSON.parse(await readFile(recordFile, 'utf8')), prepared)
      assert.equal(prepared.handoffReceiptDigest, handoffReceipt.receiptDigest)
      await assert.rejects(lstat(join(files.handoffReceiptDirectory, '07-C.json')), /ENOENT/)
    },
  })
  assert.equal(inspected, true)
  const phaseC = JSON.parse(await readFile(join(files.handoffReceiptDirectory, '07-C.json'), 'utf8'))
  assert.equal(record.handoffReceiptDigest, phaseC.receiptDigest)
  assert.equal(record.operationDigest, phaseC.operationDigest)
  assert.equal(record.observationDigest, phaseC.observationDigest)
})

test('phase C record EEXIST and record writer failure leave the C commit receipt absent', async (t) => {
  await t.test('EEXIST', async () => {
    const files = await fixture()
    await mkdir(join(files.root, 'phase-c-output'), { mode: 0o700 })
    await writeReadonlyJSON(files.recordFile, { foreign: true })
    await assert.rejects(build(files), /EEXIST/)
    assert.deepEqual(JSON.parse(await readFile(files.recordFile, 'utf8')), { foreign: true })
    await assert.rejects(lstat(join(files.handoffReceiptDirectory, '07-C.json')), /ENOENT/)
  })

  await t.test('injected writer failure', async () => {
    const files = await fixture()
    await assert.rejects(build(files, {
      recordWriteStep: async ({ stage }) => {
        if (stage === 'post-chmod') throw new Error('injected phase-C record failure')
      },
    }), /injected phase-C record failure/)
    await assert.rejects(lstat(files.recordFile), /ENOENT/)
    await assert.rejects(lstat(join(files.handoffReceiptDirectory, '07-C.json')), /ENOENT/)
  })
})

test('phase C record parent drift leaves only uncommitted pre-content and no C receipt', async () => {
  const files = await fixture()
  const recordDirectory = join(files.root, 'phase-c-output')
  const displacedDirectory = join(files.root, 'phase-c-output.displaced')
  let drifted = false
  await assert.rejects(build(files, {
    recordWriteStep: async ({ stage }) => {
      if (stage !== 'post-close') return
      await rename(recordDirectory, displacedDirectory)
      await mkdir(recordDirectory, { mode: 0o700 })
      drifted = true
    },
  }))
  assert.equal(drifted, true)
  assert.equal((await stat(join(displacedDirectory, 'phase-c-record.json'))).mode & 0o777, 0o400)
  await assert.rejects(lstat(join(files.handoffReceiptDirectory, '07-C.json')), /ENOENT/)
})

test('phase C receipt publication failure preserves a causally marked uncommitted record', async () => {
  const files = await fixture()
  await assert.rejects(build(files, {
    receiptWriteStep: async ({ stage }) => {
      if (stage === 'before-spawn') throw new Error('injected phase-C receipt failure')
    },
  }), (error) => {
    assert.match(error.message, /prepared output remains uncommitted/)
    assert.match(error.cause?.message ?? '', /injected phase-C receipt failure/)
    return true
  })
  const record = JSON.parse(await readFile(files.recordFile, 'utf8'))
  assert.equal(record.status, 'passed')
  await assert.rejects(lstat(join(files.handoffReceiptDirectory, '07-C.json')), /ENOENT/)
})

test('raw receipts and manifest use exclusive owner-only immutable creation', async () => {
  const files = await fixture()
  assert.equal((await stat(files.manifestFile)).mode & 0o777, 0o400)
  assert.equal((await stat(join(files.receiptDirectory, '01-minimal.json'))).mode & 0o777, 0o400)
  await assert.rejects(persistProfileReuseManifest(files.manifest, files.manifestFile), /EEXIST/)
  await assert.rejects(persistProfileReuseReceipt(files.receipts[0],
    join(files.receiptDirectory, '01-minimal.json')), /EEXIST/)
})

test('profile writer refreshes the final 0400 identity before post-chmod cleanup', async () => {
  const files = await fixture()
  const outputPath = join(files.root, 'post-chmod', '01-minimal.json')
  await assert.rejects(persistProfileReuseReceipt(files.receipts[0], outputPath, {
    writeStep: async ({ stage }) => {
      if (stage === 'post-chmod') throw new Error('injected profile post-chmod failure')
    },
  }), /injected profile post-chmod failure/)
  await assert.rejects(lstat(outputPath), /ENOENT/)
})

test('profile writer rejects a post-close swap and preserves the foreign inode', async () => {
  const files = await fixture()
  const outputPath = join(files.root, 'post-close-swap', '01-minimal.json')
  const displacedPath = join(files.root, 'post-close-swap', 'owned-displaced.json')
  const foreignBytes = Buffer.from('foreign profile output\n')
  await assert.rejects(persistProfileReuseReceipt(files.receipts[0], outputPath, {
    writeStep: async ({ stage, path }) => {
      if (stage !== 'post-close') return
      assert.equal(path, outputPath)
      await rename(outputPath, displacedPath)
      await writeFile(outputPath, foreignBytes, { flag: 'wx', mode: 0o400 })
      await chmod(outputPath, 0o400)
    },
  }), (error) => {
    assert.equal(error instanceof AggregateError, true)
    assert.equal(error.errors.length >= 2, true)
    assert.match(error.errors[0].message, /immutable creation failed/)
    assert.equal(error.cause, error.errors[0])
    return true
  })
  assert.deepEqual(await readFile(outputPath), foreignBytes)
  assert.equal((await lstat(displacedPath)).isFile(), true)
})

test('rejects drifted profile, retry, Attempt state, acknowledgement, Apply, and identity fields', async () => {
  const cases = [
    (receipt) => { receipt.profile = 'standard' },
    (receipt) => { receipt.taskId = 'task_legacy' },
    (receipt) => { receipt.attemptSequence = 2 },
    (receipt) => { receipt.attemptState = 'succeeded' },
    (receipt) => { receipt.terminalReason = 'runtime_exit_nonzero' },
    (receipt) => { receipt.frameworkRetryCount = 1 },
    (receipt) => { receipt.hiddenRetryCount = 1 },
    (receipt) => { receipt.operationCounts.pull = 1 },
    (receipt) => { receipt.terminalResidue.ownedContainers = 1 },
    (receipt) => { receipt.profileEvidence.prohibitedCapabilityDenialCount = 2 },
  ]
  for (const mutate of cases) {
    const files = await fixture()
    const receipt = structuredClone(files.receipts[0])
    mutate(receipt)
    assert.throws(() => assertProfileReuseReceipt(receipt))
  }

  for (const mutate of [
    (receipt) => { receipt.managedSandbox = true },
    (receipt) => { receipt.managedFallbackUsed = true },
    (receipt) => { receipt.sandboxState = 'managed' },
    (receipt) => { receipt.profileEvidence.acknowledgementCurrent = false },
    (receipt) => { receipt.profileEvidence.acknowledgementPolicyVersion = 'chora.trusted-local-disclosure.v0' },
    (receipt) => { receipt.profileEvidence.choraResultSemantics = false },
  ]) {
    const files = await fixture()
    const receipt = structuredClone(files.receipts[4])
    mutate(receipt)
    assert.throws(() => assertProfileReuseReceipt(receipt))
  }

  for (const [index, mutate] of [
    [1, (receipt) => { receipt.profileEvidence.taskOwnedApply = false }],
    [2, (receipt) => { receipt.profileEvidence.taskOwnedApply = true }],
    [3, (receipt) => { receipt.profileEvidence.patchDigest = sha256('forged') }],
  ]) {
    const files = await fixture()
    const receipt = structuredClone(files.receipts[index])
    mutate(receipt)
    assert.throws(() => assertProfileReuseReceipt(receipt))
  }
})

test('Trusted Local cannot claim Docker evidence and requires exact host lifecycle/cleanup proof', async () => {
  for (const mutate of [
    (receipt) => { receipt.attemptSource = { startSequence: 9, endSequence: 9, sliceDigest: sha256('fake-attempt') } },
    (receipt) => { receipt.roleTupleDigest = computeProfileRoleTupleDigest(roleImages()) },
    (receipt) => { receipt.operationCounts = { build: 0, pull: 0, load: 0 } },
    (receipt) => { receipt.managedSandbox = true },
    (receipt) => { receipt.sandboxState = 'managed' },
    (receipt) => { receipt.runtimeSource = 'managed_pi_image' },
    (receipt) => { delete receipt.profileEvidence.selectedPiPath },
    (receipt) => { receipt.profileEvidence.selectedPiPath = 'relative/pi' },
    (receipt) => { receipt.profileEvidence.piExecutableSha256 = sha256('forged-pi') },
    (receipt) => { receipt.profileEvidence.processGroupIdentitySha256 = sha256('forged-process-group') },
    (receipt) => { receipt.profileEvidence.processExitedAt = '2026-08-28T08:04:00.000Z' },
    (receipt) => { receipt.profileEvidence.processGroupTerminated = false },
    (receipt) => { receipt.profileEvidence.sessionClosed = false },
  ]) {
    const files = await fixture()
    const receipt = structuredClone(files.receipts[4])
    mutate(receipt)
    assert.throws(() => assertProfileReuseReceipt(receipt))
  }

  for (const mutate of [
    (resource) => { resource.managedInventory = emptyInventory() },
    (resource) => { resource.trustedHost.sessionIdentitySha256 = sha256('other-session') },
    (resource) => { resource.trustedHost.processGroupTerminated = false },
    (resource) => { resource.trustedHost.terminalInventory.ownedProcessGroups.push('still-running') },
    (resource) => { resource.trustedHost.terminalCleanupDigest = sha256('forged-cleanup') },
    (resource) => { resource.trustedHost.verifierSource.sliceDigest = sha256('forged-verifier') },
  ]) {
    const files = await fixture()
    const path = join(files.resourceLedgerDirectory, '05-trusted-local-resources.json')
    const resource = JSON.parse(await readFile(path, 'utf8'))
    mutate(resource)
    await chmod(path, 0o600)
    await writeFile(path, `${JSON.stringify(resource, null, 2)}\n`)
    await chmod(path, 0o400)
    await assert.rejects(build(files), /resource ledger 5 bytes digest drifted/)
  }
})

test('fails before phase C on missing, extra, reordered, duplicated, or spliced raw evidence', async () => {
  for (const mutate of [
    (manifest) => { manifest.receipts.pop() },
    (manifest) => { [manifest.receipts[0], manifest.receipts[1]] = [manifest.receipts[1], manifest.receipts[0]] },
    (manifest) => { manifest.receipts[1].sha256 = manifest.receipts[0].sha256 },
    (manifest) => { manifest.roleImages.managed_pi_runtime.dockerConfigImageId = `sha256:${sha256('spliced-image')}` },
  ]) {
    const files = await fixture()
    const draft = structuredClone(files.manifest)
    delete draft.manifestDigest
    mutate(draft)
    assert.throws(() => formProfileReuseManifest(draft))
  }

  const resourceIndexSplice = await fixture()
  const resourceIndexDraft = structuredClone(resourceIndexSplice.manifest)
  delete resourceIndexDraft.manifestDigest
  resourceIndexDraft.resourceLedgers[0].sha256 = sha256('spliced-resource')
  assert.throws(() => formProfileReuseManifest(resourceIndexDraft), /attestation index/)

  for (const mutate of [
    (manifest) => { manifest.sourceA3AuditRecordCount-- },
    (manifest) => { manifest.sourceA3AuditFinalDigest = sha256('spliced-final') },
    (manifest) => { manifest.tupleIdentity = sha256('spliced-tuple') },
  ]) {
    const files = await fixture()
    const draft = structuredClone(files.manifest)
    delete draft.manifestDigest
    mutate(draft)
    const changed = formProfileReuseManifest(draft)
    await chmod(files.manifestFile, 0o600)
    await writeFile(files.manifestFile, `${JSON.stringify(changed, null, 2)}\n`)
    await chmod(files.manifestFile, 0o400)
    await assert.rejects(build(files))
  }

  const extra = await fixture()
  await writeReadonlyJSON(join(extra.receiptDirectory, '99-extra.json'), { unexpected: true })
  await assert.rejects(build(extra), /missing or extra/)
  await assert.rejects(stat(join(extra.handoffReceiptDirectory, '07-C.json')))

  const sourceSplice = await fixture()
  const source = JSON.parse(await readFile(sourceSplice.sourceA3LedgerFile, 'utf8'))
  source.commandAudit[0].safeTargetSha256 = sha256('spliced-command')
  await chmod(sourceSplice.sourceA3LedgerFile, 0o600)
  await writeFile(sourceSplice.sourceA3LedgerFile, `${JSON.stringify(source, null, 2)}\n`)
  await assert.rejects(build(sourceSplice), /bytes digest|command record digest/)

  const resourceSplice = await fixture()
  const path = join(resourceSplice.resourceLedgerDirectory, '01-minimal-resources.json')
  const resource = JSON.parse(await readFile(path, 'utf8'))
  resource.managedInventory.ownedContainers.push('opaque-container')
  await chmod(path, 0o600)
  await writeFile(path, `${JSON.stringify(resource, null, 2)}\n`)
  await chmod(path, 0o400)
  await assert.rejects(build(resourceSplice), /bytes digest|non-empty/)
})

test('profile record exact-rejects rehashed stale v1 and repository/roots/bindings/plan tuple drift', async () => {
  for (const [mutate, message] of [
    [(value) => { value.schemaVersion = 'chora.m1-o4-candidate-install-tuple.v1' },
      /schema\/status drifted/],
    [(value) => {
      value.repositoryAuthority.repositoryClosureSha256 = sha256('spliced-repository')
      value.bindings.repositoryClosureSha256 = value.repositoryAuthority.repositoryClosureSha256
    }, /repository authority snapshot digest drifted/],
    [(value) => { delete value.roots.stateRootSha256 }, /roots fields drifted/],
    [(value) => { delete value.bindings.roleTupleSha256 }, /bindings fields drifted/],
    [(value) => { [value.plan[0], value.plan[1]] = [value.plan[1], value.plan[0]] },
      /plan\/order drifted/],
  ]) {
    const files = await fixture()
    const value = JSON.parse(await readFile(files.tupleFile, 'utf8'))
    mutate(value)
    delete value.identity
    value.identity = sha256(canonicalJSONStringify(value))
    await chmod(files.tupleFile, 0o600)
    await writeFile(files.tupleFile, `${JSON.stringify(value, null, 2)}\n`)
    await chmod(files.tupleFile, 0o400)
    await assert.rejects(build(files), message)
  }
})

test('rejects symlink, hardlink, mode drift, duplicate phase C, and a non-serve B2 prefix', async () => {
  const linked = await fixture()
  const alias = join(linked.root, 'manifest-link.json')
  await symlink(linked.manifestFile, alias)
  await assert.rejects(buildProfileReuseRecord({
    manifestFile: alias,
    receiptDirectory: linked.receiptDirectory,
    resourceLedgerDirectory: linked.resourceLedgerDirectory,
    sourceA3LedgerFile: linked.sourceA3LedgerFile,
    tupleFile: linked.tupleFile,
    handoffReceiptDirectory: linked.handoffReceiptDirectory,
    recordFile: linked.recordFile,
  }), /symlink/)

  const hardlinked = await fixture()
  const hardlinkPath = join(hardlinked.root, 'manifest-hardlink.json')
  await link(hardlinked.manifestFile, hardlinkPath)
  await assert.rejects(build(hardlinked), /non-linked/)

  const wrongMode = await fixture()
  await chmod(wrongMode.manifestFile, 0o600)
  await assert.rejects(build(wrongMode), /mode drifted/)

  const duplicate = await fixture()
  await build(duplicate)
  await assert.rejects(build(duplicate), /completed B2 install chain/)

  const incomplete = await fixture()
  const serve = join(incomplete.handoffReceiptDirectory, '06-serve.json')
  await chmod(serve, 0o600)
  await writeFile(serve, '{}')
  await chmod(serve, 0o400)
  await assert.rejects(build(incomplete), /B2 phase receipt/)
})

test('manifest and receipt schemas reject extra or missing fields', async () => {
  const files = await fixture()
  const receipt = structuredClone(files.receipts[0])
  receipt.unexpected = true
  assert.throws(() => assertProfileReuseReceipt(receipt), /fields drifted/)
  const manifest = structuredClone(files.manifest)
  delete manifest.roleTupleDigest
  assert.throws(() => assertProfileReuseManifest(manifest), /fields drifted/)
})

test('real producer preflight rejects stale, linked, overlapping, missing, and preexisting evidence paths', async () => {
  const clean = await fixture()
  const cleanRunner = join(clean.root, 'clean-runner')
  const cleanSupervisor = join(clean.root, 'clean-supervisor')
  await mkdir(cleanRunner, { mode: 0o700 })
  await mkdir(cleanSupervisor, { mode: 0o700 })
  const cleanObserver = await createPinnedObserver(clean.root)
  const output = join(clean.root, 'real-output')
  const prepared = await prepareProfileReuseEvidencePaths({
    tupleFile: clean.tupleFile,
    sourceA3LedgerFile: clean.sourceA3LedgerFile,
    handoffReceiptDirectory: clean.handoffReceiptDirectory,
    ...cleanObserver,
    supervisorResourceDirectory: cleanSupervisor,
    runnerProtocolDirectory: cleanRunner,
    outputDirectory: output,
  })
  assert.equal((await stat(prepared.outputDirectory)).mode & 0o777, 0o700)
  assert.equal((await stat(prepared.receiptDirectory)).mode & 0o777, 0o700)
  assert.equal((await stat(prepared.resourceLedgerDirectory)).mode & 0o777, 0o700)

  const preexisting = await fixture()
  const preexistingRunner = join(preexisting.root, 'clean-runner')
  const preexistingOutput = join(preexisting.root, 'real-output')
  const preexistingSupervisor = join(preexisting.root, 'clean-supervisor')
  await mkdir(preexistingRunner, { mode: 0o700 })
  await mkdir(preexistingSupervisor, { mode: 0o700 })
  await mkdir(preexistingOutput, { mode: 0o700 })
  const preexistingObserver = await createPinnedObserver(preexisting.root)
  await assert.rejects(prepareProfileReuseEvidencePaths({
    tupleFile: preexisting.tupleFile,
    sourceA3LedgerFile: preexisting.sourceA3LedgerFile,
    handoffReceiptDirectory: preexisting.handoffReceiptDirectory,
    ...preexistingObserver,
    supervisorResourceDirectory: preexistingSupervisor,
    runnerProtocolDirectory: preexistingRunner,
    outputDirectory: preexistingOutput,
  }), /already exists/)

  const stale = await fixture()
  const staleRunner = join(stale.root, 'clean-runner')
  const staleSupervisor = join(stale.root, 'clean-supervisor')
  await mkdir(staleRunner, { mode: 0o700 })
  await mkdir(staleSupervisor, { mode: 0o700 })
  await writeReadonlyJSON(join(staleRunner, 'stale.json'), { stale: true })
  const staleObserver = await createPinnedObserver(stale.root)
  await assert.rejects(prepareProfileReuseEvidencePaths({
    tupleFile: stale.tupleFile,
    sourceA3LedgerFile: stale.sourceA3LedgerFile,
    handoffReceiptDirectory: stale.handoffReceiptDirectory,
    ...staleObserver,
    supervisorResourceDirectory: staleSupervisor,
    runnerProtocolDirectory: staleRunner,
    outputDirectory: join(stale.root, 'real-output'),
  }), /not clean/)

  const linked = await fixture()
  const linkedRunner = join(linked.root, 'clean-runner')
  const linkedTuple = join(linked.root, 'linked-tuple.json')
  const linkedSupervisor = join(linked.root, 'clean-supervisor')
  await mkdir(linkedRunner, { mode: 0o700 })
  await mkdir(linkedSupervisor, { mode: 0o700 })
  await symlink(linked.tupleFile, linkedTuple)
  const linkedObserver = await createPinnedObserver(linked.root)
  await assert.rejects(prepareProfileReuseEvidencePaths({
    tupleFile: linkedTuple,
    sourceA3LedgerFile: linked.sourceA3LedgerFile,
    handoffReceiptDirectory: linked.handoffReceiptDirectory,
    ...linkedObserver,
    supervisorResourceDirectory: linkedSupervisor,
    runnerProtocolDirectory: linkedRunner,
    outputDirectory: join(linked.root, 'real-output'),
  }), /symlink/)

  const overlap = await fixture()
  const overlapRunner = join(overlap.root, 'clean-runner')
  const overlapSupervisor = join(overlap.root, 'clean-supervisor')
  await mkdir(overlapRunner, { mode: 0o700 })
  await mkdir(overlapSupervisor, { mode: 0o700 })
  const overlapObserver = await createPinnedObserver(overlap.root)
  await assert.rejects(prepareProfileReuseEvidencePaths({
    tupleFile: overlap.tupleFile,
    sourceA3LedgerFile: overlap.sourceA3LedgerFile,
    handoffReceiptDirectory: overlap.handoffReceiptDirectory,
    ...overlapObserver,
    supervisorResourceDirectory: overlapSupervisor,
    runnerProtocolDirectory: overlapRunner,
    outputDirectory: join(overlapRunner, 'output'),
  }), /overlap/)

  const missing = await fixture()
  const missingRunner = join(missing.root, 'clean-runner')
  const missingSupervisor = join(missing.root, 'clean-supervisor')
  await mkdir(missingRunner, { mode: 0o700 })
  await mkdir(missingSupervisor, { mode: 0o700 })
  const missingObserver = await createPinnedObserver(missing.root)
  await assert.rejects(prepareProfileReuseEvidencePaths({
    tupleFile: missing.tupleFile,
    sourceA3LedgerFile: join(missing.root, 'missing-a3.json'),
    handoffReceiptDirectory: missing.handoffReceiptDirectory,
    ...missingObserver,
    supervisorResourceDirectory: missingSupervisor,
    runnerProtocolDirectory: missingRunner,
    outputDirectory: join(missing.root, 'real-output'),
  }), /ENOENT/)
})

test('runner observer protocol emits one request-bound resource and acknowledgement at the journey boundary', async () => {
  const files = await fixture()
  const runnerProtocolDirectory = join(files.root, 'observer-protocol')
  const supervisorResourceDirectory = join(files.root, 'supervisor-resources')
  await mkdir(runnerProtocolDirectory, { mode: 0o700 })
  await mkdir(supervisorResourceDirectory, { mode: 0o700 })
  await writeBoundarySource(files, 1)
  const observer = await createPinnedObserver(files.root, syntheticObserverProgram())
  const paths = await prepareProfileReuseEvidencePaths({
    tupleFile: files.tupleFile,
    sourceA3LedgerFile: files.sourceA3LedgerFile,
    handoffReceiptDirectory: files.handoffReceiptDirectory,
    ...observer,
    supervisorResourceDirectory,
    runnerProtocolDirectory,
    outputDirectory: join(files.root, 'real-output'),
  })
  const rawResourceFile = await stageSupervisorResource(files, supervisorResourceDirectory, 1)
  const receipt = files.receipts[0]
  const result = await collectProfileReuseResourceAtBoundary({
    paths, sequence: 1, journey: 'minimal', profile: 'minimal',
    tupleIdentity: receipt.tupleIdentity, taskId: receipt.taskId, runId: receipt.runId,
    attemptId: receipt.attemptId, workspaceIdentity: receipt.workspaceIdentity,
    publicObservationDigest: receipt.publicObservationDigest,
    publicCompletedAt: receipt.publicCompletedAt,
    requestNonce: sha256('one-shot-request-nonce'),
  })
  for (const file of [result.requestFile, result.resourceFile, result.ackFile]) {
    assert.equal((await stat(file)).mode & 0o777, 0o400)
  }
  assert.deepEqual(await readFile(result.resourceFile), await readFile(rawResourceFile))
  assert.equal(result.resourceSha256, files.sourceA3.attempts[0].resourceLedgerSha256)
  await assert.rejects(collectProfileReuseResourceAtBoundary({
    paths, sequence: 1, journey: 'minimal', profile: 'minimal',
    tupleIdentity: receipt.tupleIdentity, taskId: receipt.taskId, runId: receipt.runId,
    attemptId: receipt.attemptId, workspaceIdentity: receipt.workspaceIdentity,
    publicObservationDigest: receipt.publicObservationDigest,
    publicCompletedAt: receipt.publicCompletedAt,
    requestNonce: sha256('duplicate-request-nonce'),
  }), /EEXIST/)
})

test('runner observer protocol rejects a forged acknowledgement before evidence production', async () => {
  const files = await fixture()
  const runnerProtocolDirectory = join(files.root, 'observer-protocol')
  const supervisorResourceDirectory = join(files.root, 'supervisor-resources')
  await mkdir(runnerProtocolDirectory, { mode: 0o700 })
  await mkdir(supervisorResourceDirectory, { mode: 0o700 })
  await writeBoundarySource(files, 1)
  const observer = await createPinnedObserver(files.root, syntheticObserverProgram({ forgeAck: true }))
  const paths = await prepareProfileReuseEvidencePaths({
    tupleFile: files.tupleFile,
    sourceA3LedgerFile: files.sourceA3LedgerFile,
    handoffReceiptDirectory: files.handoffReceiptDirectory,
    ...observer,
    supervisorResourceDirectory,
    runnerProtocolDirectory,
    outputDirectory: join(files.root, 'real-output'),
  })
  await stageSupervisorResource(files, supervisorResourceDirectory, 1)
  const receipt = files.receipts[0]
  await assert.rejects(collectProfileReuseResourceAtBoundary({
    paths, sequence: 1, journey: 'minimal', profile: 'minimal',
    tupleIdentity: receipt.tupleIdentity, taskId: receipt.taskId, runId: receipt.runId,
    attemptId: receipt.attemptId, workspaceIdentity: receipt.workspaceIdentity,
    publicObservationDigest: receipt.publicObservationDigest,
    publicCompletedAt: receipt.publicCompletedAt,
    requestNonce: sha256('forged-ack-request-nonce'),
  }), (error) => {
    assert.equal(error instanceof AggregateError, true)
    assert.match(error.errors[0].message, /acknowledgement binding drifted/)
    assert.match(error.errors[1].message, /left a publishable resource\/ACK path/)
    assert.equal(error.cause, error.errors[0])
    return true
  })
  assert.deepEqual((await readdir(paths.runnerResourceDirectory)).sort(),
    ['01-minimal-resources.json'])
  assert.deepEqual((await readdir(paths.runnerAckDirectory)).sort(), ['01-minimal-ack.json'])
})

test('full supervisor protocol composes unchanged raw bytes through receipts, manifest, builder, and phase C', async () => {
  const completed = await runCompleteProtocolCohort()
  assert.equal(completed.record.status, 'passed')
  assert.deepEqual(completed.record.resourceLedgerDigests,
    completed.results.map(({ resourceSha256 }) => resourceSha256))
  assert.deepEqual(completed.record.resourceRequestDigests,
    completed.results.map(({ requestDigest }) => requestDigest))
  assert.deepEqual(completed.record.resourceAckDigests,
    completed.results.map(({ ackDigest }) => ackDigest))
  assert.equal(completed.record.resourceObserverSha256,
    completed.results[0].observerSha256)
  for (let index = 0; index < completed.results.length; index++) {
    const raw = join(completed.protocol.supervisorResourceDirectory,
      resourceNamesForTest()[index])
    assert.deepEqual(await readFile(completed.results[index].resourceFile), await readFile(raw))
  }
})

test('observer rejects invalid evidence and preserves unowned residue without deleting by pathname', async () => {
  for (const [options, message, residueCount] of [
    [{ substituteResource: true }, /generated or substituted resource bytes/, 1],
    [{ mutateA3: true }, /mutated source A3/, 1],
    [{ spliceResourceAck: true }, /acknowledgement binding drifted/, 1],
    [{ fail: true }, /silent exit-zero protocol/, 1],
    [{ hang: true }, /timed out/, 0],
  ]) {
    const files = await fixture()
    const protocol = await prepareProtocol(files, syntheticObserverProgram(options), 1,
      options.hang ? 100 : 60_000)
    await assert.rejects(collectFixtureBoundary(files, protocol, 1), (error) => {
      if (residueCount === 0) assert.match(error.message, message)
      else {
        assert.equal(error instanceof AggregateError, true)
        assert.match(error.errors[0].message, message)
        assert.match(error.errors[1].message, /left a publishable resource\/ACK path/)
        assert.equal(error.cause, error.errors[0])
      }
      return true
    })
    assert.equal((await readdir(protocol.paths.runnerResourceDirectory)).length, residueCount)
    assert.equal((await readdir(protocol.paths.runnerAckDirectory)).length, residueCount)
  }
})

test('timeout and oversize kill the observer process group before a hostile descendant can rebuild evidence', async () => {
  for (const [hostileDescendant, message] of [
    ['timeout', /timed out/],
    ['oversize', /stdout exceeded the bounded protocol/],
  ]) {
    const files = await fixture()
    const protocol = await prepareProtocol(files,
      syntheticObserverProgram({ hostileDescendant }), 1,
      hostileDescendant === 'oversize' ? 5_000 : 100)
    await assert.rejects(collectFixtureBoundary(files, protocol, 1), message)
    assert.deepEqual(await readdir(protocol.paths.runnerResourceDirectory), [])
    assert.deepEqual(await readdir(protocol.paths.runnerAckDirectory), [])
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 2_000))
    assert.deepEqual(await readdir(protocol.paths.runnerResourceDirectory), [])
    assert.deepEqual(await readdir(protocol.paths.runnerAckDirectory), [])
  }
})

test('observer hash drift between boundaries fails before the changed executable runs', async () => {
  const files = await fixture()
  const protocol = await prepareProtocol(files, syntheticObserverProgram(), 1)
  await collectFixtureBoundary(files, protocol, 1)
  await writeBoundarySource(files, 2)
  await chmod(protocol.observer.observerFile, 0o600)
  await writeFile(protocol.observer.observerFile,
    `${await readFile(protocol.observer.observerFile, 'utf8')}\n// drift\n`)
  await chmod(protocol.observer.observerFile, 0o500)
  await assert.rejects(collectFixtureBoundary(files, protocol, 2),
    /observer changed before or during invocation/)
  assert.deepEqual((await readdir(protocol.paths.runnerAckDirectory)).sort(),
    ['01-minimal-ack.json'])
  assert.deepEqual((await readdir(protocol.paths.runnerResourceDirectory)).sort(),
    ['01-minimal-resources.json'])
})

test('Trusted Local rejects a mismatched pre-existing supervisor lifecycle proof before observer invocation', async () => {
  const files = await fixture()
  const protocol = await prepareProtocol(files, syntheticObserverProgram(), 4)
  for (let sequence = 1; sequence <= 4; sequence++) {
    await stageSupervisorResource(files, protocol.supervisorResourceDirectory, sequence)
  }
  const trustedPath = join(files.resourceLedgerDirectory, '05-trusted-local-resources.json')
  const trusted = JSON.parse(await readFile(trustedPath, 'utf8'))
  trusted.trustedHost.sessionIdentitySha256 = sha256('mismatched-supervisor-session')
  await chmod(trustedPath, 0o600)
  await writeFile(trustedPath, `${JSON.stringify(trusted, null, 2)}\n`)
  await chmod(trustedPath, 0o400)
  await stageSupervisorResource(files, protocol.supervisorResourceDirectory, 5)
  const receipt = files.receipts[4]
  await assert.rejects(collectProfileReuseResourceAtBoundary({
    paths: protocol.paths, sequence: 5, journey: 'trusted_local', profile: 'trusted_local',
    tupleIdentity: receipt.tupleIdentity, taskId: receipt.taskId, runId: receipt.runId,
    attemptId: receipt.attemptId, workspaceIdentity: receipt.workspaceIdentity,
    publicObservationDigest: receipt.publicObservationDigest,
    publicCompletedAt: receipt.publicCompletedAt,
    runtimeFingerprint: receipt.profileEvidence.runtimeFingerprint,
    requestNonce: sha256('trusted-mismatch-nonce'),
  }), /runtime\/process\/session binding drifted/)
  assert.deepEqual(await readdir(protocol.paths.runnerRequestDirectory), [])
})
