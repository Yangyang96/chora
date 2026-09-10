#!/usr/bin/env node

import { createHash } from 'node:crypto'
import { constants as fsConstants } from 'node:fs'
import { lstat, open } from 'node:fs/promises'
import { dirname, isAbsolute, join, normalize, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

import {
  cleanupOwnedPath,
  ownedPathIdentity,
  validateOwnedPathCapability,
} from './o4-owned-path-cleanup.mjs'

const CANDIDATE_TUPLE_SCHEMA = 'chora.m1-o4-candidate-install-tuple.v2'
const REPOSITORY_AUTHORITY_SCHEMA = 'chora.m1-o4-repository-authority.v2'
const PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA = 'chora.m1-o4-profile-reuse-observer-request.v1'
const PROFILE_REUSE_OBSERVER_ACK_SCHEMA = 'chora.m1-o4-profile-reuse-observer-ack.v1'
const PROFILE_REUSE_RESOURCE_SCHEMA = 'chora.m1-o4-profile-reuse-resource-ledger.v1'
const SOURCE_A3_SCHEMA = 'chora.m1-o4-sealed-a3-runner-ledger.v1'
const MAX_REQUEST_BYTES = 128 * 1024
const MAX_RESOURCE_BYTES = 512 * 1024
const MAX_TUPLE_BYTES = 4 * 1024 * 1024
const MAX_SOURCE_A3_BYTES = 4 * 1024 * 1024
const MAX_EXECUTABLE_BYTES = 16 * 1024 * 1024
const INPUT_MODE = 0o400
const LIVE_LEDGER_MODE = 0o600
const EXECUTABLE_MODE = 0o500
const OUTPUT_MODE = 0o400
const OWNER_DIRECTORY_MODE = 0o700
const MAX_REPOSITORY_SNAPSHOT_BYTES = 1024 * 1024
const MAX_REPOSITORY_SNAPSHOT_ENTRIES = 10_000
const commitRE = /^[0-9a-f]{40}$/
const opaqueInstallRE = /^(?:ins|gen)_[A-Za-z0-9_-]{24,80}$/
const journeys = Object.freeze([
  { sequence: 1, journey: 'minimal', profile: 'minimal' },
  { sequence: 2, journey: 'standard_1', profile: 'standard' },
  { sequence: 3, journey: 'standard_2', profile: 'standard' },
  { sequence: 4, journey: 'standard_3', profile: 'standard' },
  { sequence: 5, journey: 'trusted_local', profile: 'trusted_local' },
])
const requestKeys = Object.freeze([
  'schemaVersion', 'status', 'sequence', 'journey', 'profile', 'tupleIdentity',
  'taskId', 'runId', 'attemptId', 'workspaceIdentity', 'executionIdentityDigest',
  'publicObservationDigest', 'publicCompletedAt', 'rawResourceSha256', 'requestedAt',
  'requestNonce', 'observerSha256', 'ownedPathController', 'requestDigest',
])
const resourceKeys = Object.freeze([
  'schemaVersion', 'status', 'profile', 'taskId', 'runId', 'attemptId',
  'workspaceIdentity', 'tupleIdentity', 'executionIdentityDigest', 'runtimeIdentitySha256',
  'managedInventory', 'trustedHost',
])
const tupleKeys = Object.freeze([
  'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
  'roots', 'repositoryAuthority', 'bindings', 'plan', 'identity',
])
const tupleRootKeys = Object.freeze([
  'installRootSha256', 'dataRootSha256', 'stateRootSha256', 'receiptDirectorySha256',
  'privatePiRootSha256', 'probeRuntimeRootSha256', 'colimaToolRootSha256',
])
const tupleBindingKeys = Object.freeze([
  'sourceManifestSha256', 'sourceAggregateSha256', 'candidateManifestSha256',
  'binarySha256', 'webAggregateSha256', 'releaseSpecSha256', 'releaseManifestSha256',
  'offlineBundleEvidenceSha256', 'pathPiProvenanceSha256', 'privatePiProvenanceSha256',
  'modelAuthoritySha256', 'endpointEvidenceSha256', 'preflightFileSha256',
  'preflightDigest', 'actualReleaseManifestSha256', 'privatePiManifestSha256',
  'authFileSha256', 'caFileSha256', 'colimaSourceSha256', 'dockerClientSourceSha256',
  'repositoryCommit', 'repositoryClosureSha256', 'dockerCLISha256',
  'dockerContextSha256', 'endpointDigest', 'roleTupleSha256',
  'privateRuntimeConfigSha256',
])
const tuplePlan = Object.freeze([
  'offline-prebuilt-install', 'product-doctor', 'setup-once', 'installed-doctor',
  'process-boundary', 'active-serve',
])
const repositoryAuthorityKeys = Object.freeze([
  'schemaVersion', 'repositoryRoot', 'repositoryCommit', 'headRef', 'root', 'gitRoot',
  'configSha256', 'index', 'refs', 'worktree', 'repositoryClosureSha256',
])
const sourceKeys = Object.freeze([
  'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
  'bindingDigest', 'tupleIdentity', 'observationSource', 'complete', 'engineEventsUsed',
  'roleImages', 'commandAudit', 'auditRecordCount', 'auditFinalDigest', 'auditSealed', 'attempts',
])
const inventoryKeys = Object.freeze([
  'ownedContainers', 'ownedNetworks', 'ownedVolumes', 'ownedConfigs', 'ownedWorkspaces',
  'ownedProcessGroups', 'activeReferences', 'recoverableReferences',
])

export async function runProfileReuseObserver(
  argumentsList, executableFile = process.argv[1], dependencies = undefined,
) {
  const outputWriteStep = observerOutputWriteStep(dependencies)
  const paths = parseArguments(argumentsList)
  exactAbsolutePath(executableFile, 'profile reuse observer executable')
  const allPaths = [
    executableFile, paths.requestFile, paths.resourceInputFile, paths.tupleFile,
    paths.sourceA3LedgerFile, paths.resourceOutputFile, paths.ackOutputFile,
  ]
  assert(new Set(allPaths).size === allPaths.length,
    'profile reuse observer paths must be pairwise distinct')

  const [executableInput, requestInput, resourceInput, tupleInput, sourceInput] = await Promise.all([
    readStableOwnerFile(executableFile, 'profile reuse observer executable',
      MAX_EXECUTABLE_BYTES, EXECUTABLE_MODE),
    readStableOwnerFile(paths.requestFile, 'profile reuse observer request',
      MAX_REQUEST_BYTES, INPUT_MODE),
    readStableOwnerFile(paths.resourceInputFile, 'profile reuse observer resource input',
      MAX_RESOURCE_BYTES, INPUT_MODE),
    readStableOwnerFile(paths.tupleFile, 'profile reuse observer tuple',
      MAX_TUPLE_BYTES, INPUT_MODE),
    readStableOwnerFile(paths.sourceA3LedgerFile, 'profile reuse observer source A3',
      MAX_SOURCE_A3_BYTES, LIVE_LEDGER_MODE),
  ])
  const request = parseJSON(requestInput.bytes, 'profile reuse observer request')
  const resource = parseJSON(resourceInput.bytes, 'profile reuse observer resource input')
  const tuple = parseJSON(tupleInput.bytes, 'profile reuse observer tuple')
  const sourceA3 = parseJSON(sourceInput.bytes, 'profile reuse observer source A3')
  const executableSha256 = sha256(executableInput.bytes)
  validateTuple(tuple)
  validateRequest(request, tuple, executableSha256)
  validateResource(resource, request, resourceInput.sha256)
  validateBoundarySourceA3(sourceA3, tuple, request, resourceInput.sha256)
  validateOutputNames(paths, request)

  const observedAt = new Date(Math.max(Date.now(), Date.parse(request.requestedAt))).toISOString()
  const ackDraft = {
    schemaVersion: PROFILE_REUSE_OBSERVER_ACK_SCHEMA,
    status: 'observed',
    sequence: request.sequence,
    journey: request.journey,
    requestDigest: request.requestDigest,
    resourceSha256: resourceInput.sha256,
    observerSha256: executableSha256,
    observedAt,
  }
  const ack = { ...ackDraft, ackDigest: sha256(canonicalJSONStringify(ackDraft)) }
  const ackBytes = Buffer.from(`${JSON.stringify(ack, null, 2)}\n`)
  assert(ackBytes.length <= MAX_REQUEST_BYTES,
    'profile reuse observer acknowledgement exceeded the bounded protocol')

  let resourceIdentity = null
  let ackIdentity = null
  try {
    resourceIdentity = await writeExclusiveOwnerReadonly(paths.resourceOutputFile,
      resourceInput.bytes, 'profile reuse observer attested resource',
      request.ownedPathController, outputWriteStep)
    ackIdentity = await writeExclusiveOwnerReadonly(paths.ackOutputFile, ackBytes,
      'profile reuse observer acknowledgement', request.ownedPathController, outputWriteStep)
    await Promise.all([
      assertInputUnchanged(executableFile, executableInput,
        'profile reuse observer executable'),
      assertInputUnchanged(paths.requestFile, requestInput,
        'profile reuse observer request'),
      assertInputUnchanged(paths.resourceInputFile, resourceInput,
        'profile reuse observer resource input'),
      assertInputUnchanged(paths.tupleFile, tupleInput,
        'profile reuse observer tuple'),
      assertInputUnchanged(paths.sourceA3LedgerFile, sourceInput,
        'profile reuse observer source A3'),
    ])
  } catch (error) {
    const cleanupErrors = []
    for (const [path, identity] of [
      [paths.ackOutputFile, ackIdentity], [paths.resourceOutputFile, resourceIdentity],
    ]) {
      if (identity === null) continue
      try {
        await cleanupOwnedPath({ capability: request.ownedPathController, path,
          identity, disposition: 'regular_file' })
      } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    }
    if (cleanupErrors.length > 0) throw new AggregateError(
      [error, ...cleanupErrors], 'profile observer and native cleanup failed', { cause: error })
    throw error
  }
  return Object.freeze({ ack, resourceSha256: resourceInput.sha256 })
}

function observerOutputWriteStep(dependencies) {
  if (dependencies === undefined) return null
  assertPlainObject(dependencies, 'profile reuse observer dependencies')
  assertExactKeys(dependencies, ['outputWriteStep'], 'profile reuse observer dependencies')
  assert(typeof dependencies.outputWriteStep === 'function',
    'profile reuse observer output write-step dependency is invalid')
  return dependencies.outputWriteStep
}

function parseArguments(argumentsList) {
  const flags = [
    '--protocol', '--request', '--resource-input', '--attested-resource-output',
    '--ack-output', '--tuple-file', '--source-a3-ledger-file',
  ]
  assert(Array.isArray(argumentsList) && argumentsList.length === flags.length * 2,
    'profile reuse observer requires the exact bounded argv contract')
  const values = {}
  for (let index = 0; index < flags.length; index++) {
    assert(argumentsList[index * 2] === flags[index],
      'profile reuse observer argv order or fields drifted')
    const value = argumentsList[index * 2 + 1]
    assert(typeof value === 'string' && value.length > 0,
      'profile reuse observer argv contains an empty value')
    values[flags[index]] = value
  }
  assert(values['--protocol'] === PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA,
    'profile reuse observer protocol drifted')
  const paths = {
    requestFile: values['--request'],
    resourceInputFile: values['--resource-input'],
    resourceOutputFile: values['--attested-resource-output'],
    ackOutputFile: values['--ack-output'],
    tupleFile: values['--tuple-file'],
    sourceA3LedgerFile: values['--source-a3-ledger-file'],
  }
  for (const [label, path] of Object.entries(paths)) exactAbsolutePath(path, label)
  return paths
}

function validateTuple(tuple) {
  assertPlainObject(tuple, 'candidate B2 tuple')
  assertExactKeys(tuple, tupleKeys, 'candidate B2 tuple')
  assert(tuple.schemaVersion === CANDIDATE_TUPLE_SCHEMA && tuple.status === 'frozen',
    'candidate B2 tuple schema/status drifted')
  assert(typeof tuple.environmentId === 'string' && /^env_[A-Za-z0-9_-]{24,80}$/.test(tuple.environmentId),
    'candidate B2 tuple environment ID is invalid')
  assert(typeof tuple.installId === 'string' && /^ins_[A-Za-z0-9_-]{24,80}$/.test(tuple.installId),
    'candidate B2 tuple install ID is invalid')
  assert(typeof tuple.generationId === 'string' && /^gen_[A-Za-z0-9_-]{24,80}$/.test(tuple.generationId),
    'candidate B2 tuple generation ID is invalid')
  validatePlatform(tuple.platform)
  assertPlainObject(tuple.roots, 'candidate B2 tuple roots')
  assertExactKeys(tuple.roots, tupleRootKeys, 'candidate B2 tuple roots')
  for (const key of tupleRootKeys) assertDigest(tuple.roots[key], `candidate B2 tuple roots ${key}`)
  assertPlainObject(tuple.bindings, 'candidate B2 tuple bindings')
  assertExactKeys(tuple.bindings, tupleBindingKeys, 'candidate B2 tuple bindings')
  for (const key of tupleBindingKeys) {
    if (key === 'repositoryCommit') {
      assert(typeof tuple.bindings[key] === 'string' && commitRE.test(tuple.bindings[key]),
        'candidate B2 tuple repository commit is invalid')
    } else assertDigest(tuple.bindings[key], `candidate B2 tuple bindings ${key}`)
  }
  validateRepositoryAuthority(tuple.repositoryAuthority, tuple.bindings)
  assert(Array.isArray(tuple.plan) && tuple.plan.length === tuplePlan.length &&
    tuple.plan.every((step, index) => step === tuplePlan[index]),
  'candidate B2 tuple plan/order drifted')
  assertDigest(tuple.identity, 'candidate B2 tuple identity')
  const { identity, ...draft } = tuple
  assert(identity === sha256(canonicalJSONStringify(draft)),
    'candidate B2 tuple identity drifted')
}

function validateRepositoryAuthority(value, bindings) {
  assertPlainObject(value, 'candidate B2 repository authority')
  assertExactKeys(value, repositoryAuthorityKeys, 'candidate B2 repository authority')
  assert(value.schemaVersion === REPOSITORY_AUTHORITY_SCHEMA,
    'candidate B2 repository authority schema drifted')
  exactAbsolutePath(value.repositoryRoot, 'candidate B2 repository root')
  assert(typeof value.repositoryCommit === 'string' && commitRE.test(value.repositoryCommit),
    'candidate B2 repository commit is invalid')
  assertDigest(value.repositoryClosureSha256, 'candidate B2 repository closure')
  assert(/^refs\/heads\/[A-Za-z0-9][A-Za-z0-9._\/-]*$/.test(value.headRef) &&
    !value.headRef.split('/').includes('..'), 'candidate B2 repository HEAD ref is invalid')
  validateStableRepositoryIdentity(value.root, value.repositoryRoot, 'candidate B2 repository root identity')
  validateStableRepositoryIdentity(value.gitRoot, join(value.repositoryRoot, '.git'),
    'candidate B2 repository Git-root identity')
  assertDigest(value.configSha256, 'candidate B2 repository config digest')
  validateRepositoryIndex(value.index)
  validateRepositoryRefs(value.refs, value.headRef, value.repositoryCommit)
  validateRepositoryWorktree(value.worktree)
  assert(value.index.length + value.refs.length + value.worktree.length <= MAX_REPOSITORY_SNAPSHOT_ENTRIES &&
    Buffer.byteLength(canonicalJSONStringify(value)) <= MAX_REPOSITORY_SNAPSHOT_BYTES,
  'candidate B2 repository authority exceeds its tuple bound')
  const { repositoryClosureSha256, ...closure } = value
  assert(repositoryClosureSha256 === sha256(canonicalJSONStringify(closure)),
    'candidate B2 repository authority closure drifted')
  assert(value.repositoryCommit === bindings.repositoryCommit &&
    value.repositoryClosureSha256 === bindings.repositoryClosureSha256,
  'candidate B2 repository authority binding drifted')
}

function validateStableRepositoryIdentity(value, path, label) {
  assertPlainObject(value, label)
  assertExactKeys(value, ['path', 'type', 'mode', 'uid', 'gid', 'dev', 'ino'], label)
  assert(value.path === path && value.type === 'directory' &&
    Number.isSafeInteger(value.mode) && value.mode >= 0 && value.mode <= 0o777 &&
    Number.isSafeInteger(value.uid) && typeof process.getuid === 'function' &&
    value.uid === process.getuid() && Number.isSafeInteger(value.gid) && value.gid >= 0,
  `${label} scalar fields are invalid`)
  for (const key of ['dev', 'ino']) assert(typeof value[key] === 'string' &&
    /^(?:0|[1-9][0-9]*)$/.test(value[key]), `${label} ${key} is invalid`)
}

function validateRepositoryIndex(value) {
  assert(Array.isArray(value) && value.length > 0, 'candidate B2 repository index is invalid')
  let previous = ''
  for (const entry of value) {
    assertPlainObject(entry, 'candidate B2 repository index entry')
    assertExactKeys(entry, ['flag', 'mode', 'object', 'stage', 'path'],
      'candidate B2 repository index entry')
    assert(entry.flag === 'H' && (entry.mode === '100644' || entry.mode === '100755') &&
      typeof entry.object === 'string' && commitRE.test(entry.object) && entry.stage === 0 &&
      safeRepositoryRelative(entry.path) && byteCompare(previous, entry.path) < 0,
    'candidate B2 repository index entry drifted')
    previous = entry.path
  }
}

function validateRepositoryRefs(value, headRef, repositoryCommit) {
  assert(Array.isArray(value) && value.length > 0, 'candidate B2 repository refs are invalid')
  let previous = ''; let headMatches = false
  for (const entry of value) {
    assertPlainObject(entry, 'candidate B2 repository ref')
    assertExactKeys(entry, ['name', 'object'], 'candidate B2 repository ref')
    assert(typeof entry.name === 'string' && /^refs\/[A-Za-z0-9][A-Za-z0-9._\/-]*$/.test(entry.name) &&
      !entry.name.split('/').includes('..') && typeof entry.object === 'string' &&
      commitRE.test(entry.object) && byteCompare(previous, entry.name) < 0,
    'candidate B2 repository ref drifted')
    if (entry.name === headRef && entry.object === repositoryCommit) headMatches = true
    previous = entry.name
  }
  assert(headMatches, 'candidate B2 repository HEAD ref does not resolve to the pinned commit')
}

function validateRepositoryWorktree(value) {
  assert(Array.isArray(value), 'candidate B2 repository worktree is invalid')
  let previous = ''
  for (const entry of value) {
    assertPlainObject(entry, 'candidate B2 repository worktree entry')
    assertExactKeys(entry, ['path', 'type', 'mode', 'sha256'],
      'candidate B2 repository worktree entry')
    assert(safeRepositoryRelative(entry.path) && byteCompare(previous, entry.path) < 0 &&
      (entry.type === 'file' || entry.type === 'directory') &&
      Number.isSafeInteger(entry.mode) && entry.mode >= 0 && entry.mode <= 0o777,
    'candidate B2 repository worktree entry drifted')
    if (entry.type === 'file') assertDigest(entry.sha256, 'candidate B2 repository worktree digest')
    else assert(entry.sha256 === null, 'candidate B2 repository directory digest is invalid')
    previous = entry.path
  }
}

function safeRepositoryRelative(value) {
  return typeof value === 'string' && value.length > 0 && Buffer.byteLength(value) <= 4096 &&
    !isAbsolute(value) && !value.includes('\\') && !value.includes('\0') &&
    value.split('/').every((part) => part && part !== '.' && part !== '..')
}

function byteCompare(left, right) { return Buffer.compare(Buffer.from(left), Buffer.from(right)) }

function validateRequest(request, tuple, executableSha256) {
  assertPlainObject(request, 'profile reuse observer request')
  assertExactKeys(request, requestKeys, 'profile reuse observer request')
  const expectedJourney = journeys[request.sequence - 1]
  assert(request.schemaVersion === PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA &&
    request.status === 'requested' && expectedJourney &&
    request.journey === expectedJourney.journey && request.profile === expectedJourney.profile,
  'profile reuse observer request schema/status/order/profile drifted')
  assert(request.tupleIdentity === tuple.identity,
    'profile reuse observer request/B2 tuple identity drifted')
  const executionIdentityDigest = computeExecutionIdentityDigest(request)
  assert(request.executionIdentityDigest === executionIdentityDigest,
    'profile reuse observer request execution identity drifted')
  for (const field of [
    'publicObservationDigest', 'rawResourceSha256', 'requestNonce', 'observerSha256',
    'requestDigest',
  ]) assertDigest(request[field], `profile reuse observer request ${field}`)
  assert(request.observerSha256 === executableSha256,
    'profile reuse observer request executable identity drifted')
  validateOwnedPathCapability(request.ownedPathController,
    'profile reuse observer owned-path controller')
  const publicCompletedAt = Date.parse(request.publicCompletedAt)
  const requestedAt = Date.parse(request.requestedAt)
  assert(Number.isFinite(publicCompletedAt) && Number.isFinite(requestedAt) &&
    requestedAt >= publicCompletedAt,
  'profile reuse observer request terminal boundary time drifted')
  const { requestDigest, ...draft } = request
  assert(requestDigest === sha256(canonicalJSONStringify(draft)),
    'profile reuse observer request digest drifted')
}

function computeExecutionIdentityDigest(value) {
  const identity = {
    tupleIdentity: value.tupleIdentity,
    taskId: value.taskId,
    runId: value.runId,
    attemptId: value.attemptId,
    workspaceIdentity: value.workspaceIdentity,
  }
  assertDigest(identity.tupleIdentity, 'profile execution tuple identity')
  const uuidV7 = '[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}'
  const patterns = {
    taskId: new RegExp(`^task_${uuidV7}$`),
    runId: new RegExp(`^run_${uuidV7}$`),
    attemptId: new RegExp(`^attempt_${uuidV7}$`),
    workspaceIdentity: /^sha256:[0-9a-f]{64}$/,
  }
  for (const [field, pattern] of Object.entries(patterns)) {
    assert(typeof identity[field] === 'string' && pattern.test(identity[field]),
      `profile execution ${field} is not a UUIDv7/sha256 identity`)
  }
  return sha256(canonicalJSONStringify(identity))
}

function validateResource(resource, request, resourceSha256) {
  assertPlainObject(resource, 'supervisor raw resource ledger')
  assertExactKeys(resource, resourceKeys, 'supervisor raw resource ledger')
  assert(resource.schemaVersion === PROFILE_REUSE_RESOURCE_SCHEMA && resource.status === 'terminal',
    'supervisor raw resource schema/status drifted')
  for (const field of [
    'profile', 'taskId', 'runId', 'attemptId', 'workspaceIdentity', 'tupleIdentity',
    'executionIdentityDigest',
  ]) assert(resource[field] === request[field], `supervisor raw resource ${field} drifted`)
  assert(request.rawResourceSha256 === resourceSha256,
    'profile reuse observer request/resource bytes binding drifted')
  assertDigest(resource.runtimeIdentitySha256, 'supervisor raw resource runtime identity')
  if (request.profile !== 'trusted_local') {
    assert(resource.trustedHost === null,
      'managed supervisor resource carries Trusted Host proof')
    validateEmptyInventory(resource.managedInventory, 'managed supervisor raw resource inventory')
    return
  }
  assert(resource.managedInventory === null,
    'Trusted supervisor raw resource carries managed inventory')
  const trusted = resource.trustedHost
  assertPlainObject(trusted, 'Trusted supervisor raw resource proof')
  assertExactKeys(trusted, [
    'selectedPiPath', 'selectedPiSourceRoot', 'selectedSource', 'piExecutableSha256',
    'piSourceProvenanceSha256', 'runtimeFingerprint', 'processGroupIdentitySha256',
    'sessionIdentitySha256', 'processStartedAt', 'processExitedAt',
    'processGroupTerminated', 'sessionClosed', 'terminalCleanupDigest', 'terminalInventory',
    'verifierSource',
  ], 'Trusted supervisor raw resource proof')
  exactAbsolutePath(trusted.selectedPiPath, 'Trusted supervisor selected Pi')
  exactAbsolutePath(trusted.selectedPiSourceRoot, 'Trusted supervisor Pi source root')
  assert(trusted.selectedPiPath !== trusted.selectedPiSourceRoot &&
    ['path', 'private_fallback'].includes(trusted.selectedSource),
  'Trusted supervisor selected runtime topology drifted')
  for (const field of [
    'piExecutableSha256', 'piSourceProvenanceSha256', 'runtimeFingerprint',
    'processGroupIdentitySha256', 'sessionIdentitySha256', 'terminalCleanupDigest',
  ]) assertDigest(trusted[field], `Trusted supervisor ${field}`)
  const processStartedAt = Date.parse(trusted.processStartedAt)
  const processExitedAt = Date.parse(trusted.processExitedAt)
  assert(Number.isFinite(processStartedAt) && Number.isFinite(processExitedAt) &&
    processExitedAt >= processStartedAt && trusted.processGroupTerminated === true &&
    trusted.sessionClosed === true,
  'Trusted supervisor lifecycle is not terminal')
  validateEmptyInventory(trusted.terminalInventory, 'Trusted supervisor terminal inventory')
  assert(trusted.terminalCleanupDigest === sha256(canonicalJSONStringify(trusted.terminalInventory)),
    'Trusted supervisor cleanup digest drifted')
  assert(resource.runtimeIdentitySha256 === sha256(canonicalJSONStringify({
    runtimeFingerprint: trusted.runtimeFingerprint,
    processGroupIdentitySha256: trusted.processGroupIdentitySha256,
    sessionIdentitySha256: trusted.sessionIdentitySha256,
  })), 'Trusted supervisor runtime/process/session binding drifted')
  validateSourceRange(trusted.verifierSource, 'Trusted supervisor Verifier source')
}

function validateBoundarySourceA3(source, tuple, request, resourceSha256) {
  assertPlainObject(source, 'source A3 at resource boundary')
  assertExactKeys(source, sourceKeys, 'source A3 at resource boundary')
  assert(source.schemaVersion === SOURCE_A3_SCHEMA && source.status === 'recording' &&
    source.observationSource === 'exact-runner-command-audit' && source.complete === false &&
    source.engineEventsUsed === false && source.auditSealed === false,
  'source A3 boundary schema/status drifted')
  for (const field of ['environmentId', 'installId', 'generationId']) {
    assert(source[field] === tuple[field], `source A3/B2 tuple ${field} drifted`)
  }
  assert(canonicalJSONStringify(source.platform) === canonicalJSONStringify(tuple.platform) &&
    source.tupleIdentity === tuple.identity &&
    source.bindingDigest === sha256(canonicalJSONStringify(tuple.bindings)),
  'source A3/B2 tuple binding drifted')
  assert(Array.isArray(source.attempts), 'source A3 attempts are missing')
  const expectedCount = request.profile === 'trusted_local' ? 4 : request.sequence
  assert(source.attempts.length === expectedCount,
    'source A3 managed Attempt boundary count drifted')
  if (request.profile === 'trusted_local') {
    assert(!source.attempts.some((attempt) => attempt?.taskId === request.taskId ||
      attempt?.runId === request.runId || attempt?.attemptId === request.attemptId),
    'Trusted Local was inserted into source A3 as a managed Attempt')
    return
  }
  const attempt = source.attempts[request.sequence - 1]
  assertPlainObject(attempt, 'source A3 boundary managed Attempt')
  for (const field of [
    'profile', 'taskId', 'runId', 'attemptId', 'workspaceIdentity', 'tupleIdentity',
    'executionIdentityDigest',
  ]) assert(attempt[field] === request[field], `source A3 managed Attempt ${field} drifted`)
  assert(attempt.resourceLedgerSha256 === resourceSha256,
    'source A3 did not pre-bind the supervisor raw resource bytes')
}

function validateEmptyInventory(inventory, label) {
  assertPlainObject(inventory, label)
  assertExactKeys(inventory, inventoryKeys, label)
  for (const key of inventoryKeys) {
    assert(Array.isArray(inventory[key]) && inventory[key].length === 0,
      `${label} ${key} is non-empty`)
  }
}

function validateSourceRange(value, label) {
  assertPlainObject(value, label)
  assertExactKeys(value, ['startSequence', 'endSequence', 'sliceDigest'], label)
  assert(Number.isSafeInteger(value.startSequence) && value.startSequence > 0 &&
    Number.isSafeInteger(value.endSequence) && value.endSequence >= value.startSequence,
  `${label} range is invalid`)
  assertDigest(value.sliceDigest, `${label} digest`)
}

function validateOutputNames(paths, request) {
  const stem = `${String(request.sequence).padStart(2, '0')}-${request.journey.replaceAll('_', '-')}`
  assert(paths.requestFile.endsWith(`/${stem}-request.json`) &&
    paths.resourceOutputFile.endsWith(`/${stem}-resources.json`) &&
    paths.ackOutputFile.endsWith(`/${stem}-ack.json`),
  'profile reuse observer protocol filenames drifted')
}

async function readStableOwnerFile(path, label, maxBytes, mode) {
  exactAbsolutePath(path, label)
  await assertNoSymlinkAncestors(path)
  const beforePath = await lstat(path)
  validateOwnerFileInfo(beforePath, label, maxBytes, mode)
  let handle
  try {
    handle = await open(path, fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW)
    const before = await handle.stat()
    validateOwnerFileInfo(before, label, maxBytes, mode)
    assertSameIdentity(beforePath, before, label)
    const bytes = await handle.readFile()
    const after = await handle.stat()
    validateOwnerFileInfo(after, label, maxBytes, mode)
    assertStableInfo(before, after, label)
    assert(bytes.length === after.size, `${label} changed during read`)
    return Object.freeze({ bytes, sha256: sha256(bytes), info: snapshotInfo(after) })
  } finally {
    if (handle) await handle.close().catch(() => {})
  }
}

async function assertInputUnchanged(path, expected, label) {
  const actual = await readStableOwnerFile(path, label, expected.info.maxBytes, expected.info.mode)
  assert(actual.sha256 === expected.sha256 && sameSnapshot(actual.info, expected.info),
    `${label} changed during observer execution`)
}

function validateOwnerFileInfo(info, label, maxBytes, mode) {
  assert(info.isFile() && !info.isSymbolicLink() && info.nlink === 1 &&
    info.size > 0 && info.size <= maxBytes,
  `${label} must be one bounded non-linked regular file`)
  assert(typeof process.getuid === 'function' && info.uid === process.getuid(),
    `${label} must be owner-owned`)
  assert((info.mode & 0o777) === mode, `${label} mode drifted`)
}

function snapshotInfo(info) {
  return Object.freeze({
    dev: info.dev, ino: info.ino, size: info.size, mtimeMs: info.mtimeMs,
    ctimeMs: info.ctimeMs, mode: info.mode & 0o777, uid: info.uid,
    nlink: info.nlink,
    maxBytes: info.size <= MAX_REQUEST_BYTES ? MAX_REQUEST_BYTES :
      info.size <= MAX_RESOURCE_BYTES ? MAX_RESOURCE_BYTES :
        info.size <= MAX_TUPLE_BYTES ? MAX_TUPLE_BYTES : MAX_EXECUTABLE_BYTES,
  })
}

function sameSnapshot(left, right) {
  return ['dev', 'ino', 'size', 'mtimeMs', 'ctimeMs', 'mode', 'uid', 'nlink']
    .every((field) => left[field] === right[field])
}

function assertStableInfo(before, after, label) {
  assert(sameSnapshot(snapshotInfo(before), snapshotInfo(after)), `${label} changed during read`)
}

function assertSameIdentity(pathInfo, handleInfo, label) {
  assert(pathInfo.dev === handleInfo.dev && pathInfo.ino === handleInfo.ino,
    `${label} path changed before open`)
}

async function writeExclusiveOwnerReadonly(
  path, bytes, label, capabilityValue, writeStep = null,
) {
  const capability = validateOwnedPathCapability(capabilityValue, `${label} owned-path capability`)
  assert(writeStep === null || typeof writeStep === 'function',
    `${label} write-step dependency is invalid`)
  exactAbsolutePath(path, label)
  await assertOwnerDirectory(dirname(path))
  let handle
  let createdIdentity = null
  try {
    handle = await open(path,
      fsConstants.O_WRONLY | fsConstants.O_CREAT | fsConstants.O_EXCL | fsConstants.O_NOFOLLOW,
      0o600)
    createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true }))
    await handle.writeFile(bytes)
    await handle.sync()
    await handle.chmod(OUTPUT_MODE)
    await handle.sync()
    createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true }))
    if (writeStep !== null) await writeStep({ stage: 'post-chmod', path, label })
    const info = await handle.stat()
    validateOwnerFileInfo(info, label, bytes.length, OUTPUT_MODE)
    assert(info.size === bytes.length, `${label} byte count drifted`)
    await handle.close()
    handle = undefined
    if (writeStep !== null) await writeStep({ stage: 'post-close', path, label })
    const pathInfo = await lstat(path, { bigint: true })
    assert(matchesOwnedFileIdentity(pathInfo, createdIdentity),
      `${label} path identity changed after close`)
    const persisted = await readStableOwnerFile(path, label, bytes.length, OUTPUT_MODE)
    const pathAfterRead = await lstat(path, { bigint: true })
    assert(matchesOwnedFileIdentity(pathAfterRead, createdIdentity),
      `${label} path identity changed after persistence read`)
    assert(persisted.sha256 === sha256(bytes), `${label} bytes drifted after creation`)
    return createdIdentity
  } catch (error) {
    const cleanupErrors = []
    if (handle) {
      try { createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true })) }
      catch (cleanupError) { cleanupErrors.push(cleanupError) }
      try { await handle.close() } catch (cleanupError) { cleanupErrors.push(cleanupError) }
      handle = undefined
    }
    if (createdIdentity !== null) {
      try {
        await cleanupOwnedPath({ capability, path, identity: createdIdentity,
          disposition: 'regular_file' })
      } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    }
    if (cleanupErrors.length > 0) throw new AggregateError(
      [error, ...cleanupErrors], `${label} write and native cleanup failed`, { cause: error })
    throw error
  }
}

function matchesOwnedFileIdentity(info, identity) {
  return identity !== null && info.isFile() && !info.isSymbolicLink() && info.nlink === 1n &&
    info.dev.toString() === identity.dev && info.ino.toString() === identity.ino &&
    Number(info.uid) === identity.uid && Number(info.gid) === identity.gid &&
    Number(info.mode) === identity.mode && identity.uid === process.getuid()
}

async function assertOwnerDirectory(path) {
  exactAbsolutePath(path, 'profile reuse observer output directory')
  await assertNoSymlinkAncestors(path)
  const info = await lstat(path)
  assert(info.isDirectory() && !info.isSymbolicLink() &&
    typeof process.getuid === 'function' && info.uid === process.getuid() &&
    (info.mode & 0o777) === OWNER_DIRECTORY_MODE,
  'profile reuse observer output directory must be owner-owned 0700')
}

async function assertNoSymlinkAncestors(path) {
  let current = resolve(path)
  while (true) {
    try {
      const info = await lstat(current)
      assert(!info.isSymbolicLink(), 'profile reuse observer path has a symlink ancestor')
    } catch (error) {
      if (error.code !== 'ENOENT') throw error
    }
    const parent = dirname(current)
    if (parent === current) return
    current = parent
  }
}

function parseJSON(bytes, label) {
  try {
    return JSON.parse(bytes.toString('utf8'))
  } catch {
    throw new Error(`${label} is not JSON`)
  }
}

function exactAbsolutePath(value, label) {
  assert(typeof value === 'string' && isAbsolute(value) && normalize(value) === value &&
    resolve(value) === value && !value.includes('\0'), `${label} path must be exact and absolute`)
}

function assertPlainObject(value, label) {
  assert(value !== null && typeof value === 'object' && !Array.isArray(value) &&
    Object.getPrototypeOf(value) === Object.prototype, `${label} must be a plain object`)
}

function assertExactKeys(value, expected, label) {
  const actual = Object.keys(value).sort()
  const wanted = [...expected].sort()
  assert(actual.length === wanted.length && actual.every((key, index) => key === wanted[index]),
    `${label} fields drifted`)
}

function validatePlatform(platform) {
  assertPlainObject(platform, 'product host platform binding')
  assertExactKeys(platform, ['os', 'architecture'], 'product host platform binding')
  assert(platform.os === 'darwin' && platform.architecture === 'arm64',
    'product host platform is unsupported; exact darwin/arm64 is required')
}

function assertEnvironmentID(value, label) {
  assert(typeof value === 'string' && /^env_[0-9a-f]{48}$/.test(value), `${label} is invalid`)
}

function assertDigest(value, label) {
  assert(typeof value === 'string' && /^[0-9a-f]{64}$/.test(value),
    `${label} is not a canonical lowercase SHA-256 digest`)
}

function canonicalJSONStringify(value) { return JSON.stringify(canonical(value)) }
function canonical(value) {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return value
  if (typeof value === 'number') {
    assert(Number.isFinite(value), 'non-finite canonical JSON')
    return value
  }
  if (Array.isArray(value)) return value.map(canonical)
  assertPlainObject(value, 'canonical value')
  return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canonical(value[key])]))
}

function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function assert(condition, message) { if (!condition) throw new Error(message) }

function isMainModule() {
  return typeof process.argv[1] === 'string' && isAbsolute(process.argv[1]) &&
    pathToFileURL(process.argv[1]).href === import.meta.url
}

if (isMainModule()) {
  try {
    await runProfileReuseObserver(process.argv.slice(2))
  } catch (error) {
    const message = error instanceof Error ? error.message : 'profile reuse observer failed'
    process.stderr.write(`${message.slice(0, 2048)}\n`)
    process.exitCode = 1
  }
}
