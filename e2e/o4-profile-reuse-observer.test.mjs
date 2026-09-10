import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import {
  chmod, lstat, mkdir, mkdtemp, readFile, realpath, rename, stat, symlink, writeFile,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

import {
  CANDIDATE_TUPLE_SCHEMA,
  canonicalJSONStringify,
} from './o4-candidate-install-orchestrator.mjs'
import {
  PROFILE_REUSE_OBSERVER_ACK_SCHEMA,
  PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA,
  PROFILE_REUSE_RESOURCE_SCHEMA,
  computeProfileExecutionIdentityDigest,
} from './o4-profile-reuse-record.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'
import { runProfileReuseObserver } from './o4-profile-reuse-observer.mjs'

const observerFile = fileURLToPath(new URL('./o4-profile-reuse-observer.mjs', import.meta.url))
const environmentId = `env_${'1'.repeat(48)}`
const installId = `ins_${'a'.repeat(32)}`
const generationId = `gen_${'b'.repeat(32)}`
const platform = Object.freeze({ os: 'darwin', architecture: 'arm64' })

let repositoryCapture
let cleanupRepositoryCapture
test.before(async () => {
  ({ capture: repositoryCapture, cleanup: cleanupRepositoryCapture } =
    await buildTestRepositoryCapture())
})
test.after(async () => { await cleanupRepositoryCapture() })

function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function id(prefix, number) {
  return `${prefix}_00000000-0000-7000-8000-${number.toString(16).padStart(12, '0')}`
}
function emptyInventory() {
  return {
    ownedContainers: [], ownedNetworks: [], ownedVolumes: [], ownedConfigs: [],
    ownedWorkspaces: [], ownedProcessGroups: [], activeReferences: [], recoverableReferences: [],
  }
}
function candidateTuple() {
  const repositoryRoot = '/private/tmp/chora-o4-profile-observer-repository'
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
  const draft = {
    schemaVersion: CANDIDATE_TUPLE_SCHEMA,
    status: 'frozen',
    environmentId,
    installId,
    generationId,
    platform,
    roots: Object.fromEntries([
      'installRootSha256', 'dataRootSha256', 'stateRootSha256', 'receiptDirectorySha256',
      'privatePiRootSha256', 'probeRuntimeRootSha256', 'colimaToolRootSha256',
    ].map((key) => [key, sha256(key)])),
    repositoryAuthority, bindings,
    plan: [
      'offline-prebuilt-install', 'product-doctor', 'setup-once', 'installed-doctor',
      'process-boundary', 'active-serve',
    ],
  }
  return { ...draft, identity: sha256(canonicalJSONStringify(draft)) }
}

async function write0400(path, value) {
  const bytes = Buffer.isBuffer(value) ? value : Buffer.from(`${JSON.stringify(value, null, 2)}\n`)
  await writeFile(path, bytes, { flag: 'wx', mode: 0o600 })
  await chmod(path, 0o400)
  return bytes
}

async function replace0400(path, value) {
  await chmod(path, 0o600)
  const bytes = Buffer.isBuffer(value) ? value : Buffer.from(`${JSON.stringify(value, null, 2)}\n`)
  await writeFile(path, bytes)
  await chmod(path, 0o400)
}

async function fixture() {
  const root = await mkdtemp(join(await realpath(tmpdir()), 'chora-o4-profile-observer-'))
  await chmod(root, 0o700)
  const tuple = candidateTuple()
  const identity = {
    tupleIdentity: tuple.identity,
    taskId: id('task', 1),
    runId: id('run', 11),
    attemptId: id('attempt', 21),
    workspaceIdentity: `sha256:${sha256('workspace:1')}`,
  }
  const resource = {
    schemaVersion: PROFILE_REUSE_RESOURCE_SCHEMA,
    status: 'terminal',
    profile: 'minimal',
    taskId: identity.taskId,
    runId: identity.runId,
    attemptId: identity.attemptId,
    workspaceIdentity: identity.workspaceIdentity,
    tupleIdentity: identity.tupleIdentity,
    executionIdentityDigest: computeProfileExecutionIdentityDigest(identity),
    runtimeIdentitySha256: sha256('managed-runtime'),
    managedInventory: emptyInventory(),
    trustedHost: null,
  }
  const tupleFile = join(root, 'tuple.json')
  const resourceInputFile = join(root, 'raw-resource.json')
  const sourceA3LedgerFile = join(root, 'source-a3.json')
  const requestFile = join(root, '01-minimal-request.json')
  const resourceOutputFile = join(root, '01-minimal-resources.json')
  const ackOutputFile = join(root, '01-minimal-ack.json')
  const tupleBytes = await write0400(tupleFile, tuple)
  const resourceBytes = await write0400(resourceInputFile, resource)
  const sourceA3 = {
    schemaVersion: 'chora.m1-o4-sealed-a3-runner-ledger.v1',
    status: 'recording',
    environmentId,
    installId,
    generationId,
    platform,
    bindingDigest: sha256(canonicalJSONStringify(tuple.bindings)),
    tupleIdentity: tuple.identity,
    observationSource: 'exact-runner-command-audit',
    complete: false,
    engineEventsUsed: false,
    roleImages: {},
    commandAudit: [],
    auditRecordCount: 0,
    auditFinalDigest: '0'.repeat(64),
    auditSealed: false,
    attempts: [{
      profile: 'minimal',
      taskId: identity.taskId,
      runId: identity.runId,
      attemptId: identity.attemptId,
      workspaceIdentity: identity.workspaceIdentity,
      tupleIdentity: identity.tupleIdentity,
      executionIdentityDigest: resource.executionIdentityDigest,
      resourceLedgerSha256: sha256(resourceBytes),
    }],
  }
  const sourceBytes = await write0400(sourceA3LedgerFile, sourceA3)
  await chmod(sourceA3LedgerFile, 0o600)
  const observerSha256 = sha256(await readFile(observerFile))
  const requestDraft = {
    schemaVersion: PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA,
    status: 'requested',
    sequence: 1,
    journey: 'minimal',
    profile: 'minimal',
    ...identity,
    executionIdentityDigest: resource.executionIdentityDigest,
    publicObservationDigest: sha256('public-observation'),
    publicCompletedAt: '2026-08-28T08:00:00.000Z',
    rawResourceSha256: sha256(resourceBytes),
    requestedAt: '2026-08-28T08:00:01.000Z',
    requestNonce: sha256('request-nonce'),
    observerSha256,
    ownedPathController: repositoryCapture,
  }
  const request = {
    ...requestDraft,
    requestDigest: sha256(canonicalJSONStringify(requestDraft)),
  }
  const requestBytes = await write0400(requestFile, request)
  const argv = [
    '--protocol', PROFILE_REUSE_OBSERVER_REQUEST_SCHEMA,
    '--request', requestFile,
    '--resource-input', resourceInputFile,
    '--attested-resource-output', resourceOutputFile,
    '--ack-output', ackOutputFile,
    '--tuple-file', tupleFile,
    '--source-a3-ledger-file', sourceA3LedgerFile,
  ]
  return {
    root, tuple, tupleFile, tupleBytes, resource, resourceInputFile, resourceBytes,
    sourceA3, sourceA3LedgerFile, sourceBytes, request, requestFile, requestBytes,
    resourceOutputFile, ackOutputFile, observerSha256, argv,
  }
}

async function runObserver(argv) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(observerFile, argv, {
      shell: false,
      stdio: ['ignore', 'pipe', 'pipe'],
      env: {
        ...process.env,
        PATH: `${dirname(process.execPath)}:${process.env.PATH ?? ''}`,
      },
    })
    const stdout = []
    const stderr = []
    child.stdout.on('data', (chunk) => stdout.push(chunk))
    child.stderr.on('data', (chunk) => stderr.push(chunk))
    child.on('error', rejectPromise)
    child.on('close', (code, signal) => resolvePromise({
      code,
      signal,
      stdout: Buffer.concat(stdout).toString('utf8'),
      stderr: Buffer.concat(stderr).toString('utf8'),
    }))
  })
}

async function assertNoOutputs(files) {
  await assert.rejects(lstat(files.resourceOutputFile), /ENOENT/)
  await assert.rejects(lstat(files.ackOutputFile), /ENOENT/)
}

test('owner-only observer byte-copies one request-bound resource and emits the canonical ACK', async () => {
  const files = await fixture()
  const before = await Promise.all([
    files.requestFile, files.resourceInputFile, files.tupleFile, files.sourceA3LedgerFile,
  ].map((path) => readFile(path)))
  const result = await runObserver(files.argv)
  assert.deepEqual(result, { code: 0, signal: null, stdout: '', stderr: '' })
  assert.deepEqual(await readFile(files.resourceOutputFile), files.resourceBytes)
  assert.equal((await stat(files.resourceOutputFile)).mode & 0o777, 0o400)
  assert.equal((await stat(files.ackOutputFile)).mode & 0o777, 0o400)
  const ack = JSON.parse(await readFile(files.ackOutputFile, 'utf8'))
  assert.deepEqual(Object.keys(ack).sort(), [
    'schemaVersion', 'status', 'sequence', 'journey', 'requestDigest',
    'resourceSha256', 'observerSha256', 'observedAt', 'ackDigest',
  ].sort())
  assert.deepEqual(ack, {
    schemaVersion: PROFILE_REUSE_OBSERVER_ACK_SCHEMA,
    status: 'observed',
    sequence: 1,
    journey: 'minimal',
    requestDigest: files.request.requestDigest,
    resourceSha256: sha256(files.resourceBytes),
    observerSha256: files.observerSha256,
    observedAt: ack.observedAt,
    ackDigest: ack.ackDigest,
  })
  const { ackDigest, ...draft } = ack
  assert.equal(ackDigest, sha256(canonicalJSONStringify(draft)))
  assert.ok(Date.parse(ack.observedAt) >= Date.parse(files.request.requestedAt))
  const after = await Promise.all([
    files.requestFile, files.resourceInputFile, files.tupleFile, files.sourceA3LedgerFile,
  ].map((path) => readFile(path)))
  assert.deepEqual(after, before)
})

test('observer fails closed on resource, tuple, source-A3, mode, and argv substitution', async (t) => {
  await t.test('resource bytes substitution', async () => {
    const files = await fixture()
    await replace0400(files.resourceInputFile,
      Buffer.from(`${JSON.stringify({ ...files.resource, substituted: true })}\n`))
    const result = await runObserver(files.argv)
    assert.equal(result.code, 1)
    assert.match(result.stderr, /fields drifted|request\/resource bytes binding drifted/)
    await assertNoOutputs(files)
  })

  await t.test('tuple substitution', async () => {
    const files = await fixture()
    const substituted = candidateTuple()
    substituted.bindings.binarySha256 = sha256('substituted-binary')
    const { identity: ignored, ...draft } = substituted
    substituted.identity = sha256(canonicalJSONStringify(draft))
    await replace0400(files.tupleFile, substituted)
    const result = await runObserver(files.argv)
    assert.equal(result.code, 1)
    assert.match(result.stderr, /request\/B2 tuple identity drifted/)
    await assertNoOutputs(files)
  })

  await t.test('source-A3 prebinding substitution', async () => {
    const files = await fixture()
    const substituted = structuredClone(files.sourceA3)
    substituted.attempts[0].resourceLedgerSha256 = sha256('substituted-resource')
    await replace0400(files.sourceA3LedgerFile, substituted)
    await chmod(files.sourceA3LedgerFile, 0o600)
    const result = await runObserver(files.argv)
    assert.equal(result.code, 1)
    assert.match(result.stderr, /did not pre-bind/)
    await assertNoOutputs(files)
  })

  await t.test('owner input mode drift', async () => {
    const files = await fixture()
    await chmod(files.sourceA3LedgerFile, 0o400)
    const result = await runObserver(files.argv)
    assert.equal(result.code, 1)
    assert.match(result.stderr, /mode drifted/)
    await assertNoOutputs(files)
  })

  await t.test('extra argv', async () => {
    const files = await fixture()
    const result = await runObserver([...files.argv, '--extra', 'value'])
    assert.equal(result.code, 1)
    assert.match(result.stderr, /exact bounded argv contract/)
    await assertNoOutputs(files)
  })
})

test('standalone observer exact-rejects rehashed stale v1 and repository/roots/bindings/plan drift', async (t) => {
  for (const [name, mutate, message] of [
    ['stale v1', (value) => { value.schemaVersion = 'chora.m1-o4-candidate-install-tuple.v1' },
      /schema\/status drifted/],
    ['repository closure', (value) => {
      value.repositoryAuthority.repositoryClosureSha256 = sha256('spliced-repository')
      value.bindings.repositoryClosureSha256 = value.repositoryAuthority.repositoryClosureSha256
    }, /repository authority closure drifted/],
    ['partial roots', (value) => { delete value.roots.stateRootSha256 }, /roots fields drifted/],
    ['partial bindings', (value) => { delete value.bindings.roleTupleSha256 }, /bindings fields drifted/],
    ['reordered plan', (value) => {
      [value.plan[0], value.plan[1]] = [value.plan[1], value.plan[0]]
    }, /plan\/order drifted/],
  ]) await t.test(name, async () => {
    const files = await fixture()
    const drifted = structuredClone(files.tuple)
    mutate(drifted)
    delete drifted.identity
    drifted.identity = sha256(canonicalJSONStringify(drifted))
    await replace0400(files.tupleFile, drifted)
    const result = await runObserver(files.argv)
    assert.equal(result.code, 1)
    assert.match(result.stderr, message)
    await assertNoOutputs(files)
  })
})

test('observer rejects linked inputs and cleans a partial pair when exclusive ACK creation fails', async () => {
  const linked = await fixture()
  const linkedResource = join(linked.root, 'linked-resource.json')
  await symlink(linked.resourceInputFile, linkedResource)
  const linkedArgv = [...linked.argv]
  linkedArgv[linkedArgv.indexOf('--resource-input') + 1] = linkedResource
  const linkedResult = await runObserver(linkedArgv)
  assert.equal(linkedResult.code, 1)
  assert.match(linkedResult.stderr, /symlink/)
  await assertNoOutputs(linked)

  const preexisting = await fixture()
  const sentinel = await write0400(preexisting.ackOutputFile, { sentinel: true })
  const preexistingResult = await runObserver(preexisting.argv)
  assert.equal(preexistingResult.code, 1)
  assert.match(preexistingResult.stderr, /EEXIST/)
  await assert.rejects(lstat(preexisting.resourceOutputFile), /ENOENT/)
  assert.deepEqual(await readFile(preexisting.ackOutputFile), sentinel)
})

test('observer refreshes the final 0400 identity before post-chmod cleanup', async () => {
  const files = await fixture()
  await assert.rejects(runProfileReuseObserver(files.argv, observerFile, {
    outputWriteStep: async ({ stage, path }) => {
      if (stage === 'post-chmod' && path === files.resourceOutputFile) {
        throw new Error('injected observer post-chmod failure')
      }
    },
  }), /injected observer post-chmod failure/)
  await assertNoOutputs(files)
})

test('observer rejects a post-close output swap and preserves the foreign inode', async () => {
  const files = await fixture()
  const displacedPath = join(files.root, 'observer-owned-displaced.json')
  const foreignBytes = Buffer.from('foreign observer output\n')
  await assert.rejects(runProfileReuseObserver(files.argv, observerFile, {
    outputWriteStep: async ({ stage, path }) => {
      if (stage !== 'post-close' || path !== files.resourceOutputFile) return
      await rename(path, displacedPath)
      await writeFile(path, foreignBytes, { flag: 'wx', mode: 0o400 })
      await chmod(path, 0o400)
    },
  }), (error) => {
    const nested = [error, ...(error.errors ?? [])]
    assert.equal(nested.some((item) => /path identity changed after close/.test(item.message)), true)
    assert.equal(nested.some((item) => /identity changed|cleanup refused/.test(item.message)), true)
    return true
  })
  assert.deepEqual(await readFile(files.resourceOutputFile), foreignBytes)
  assert.equal((await lstat(displacedPath)).isFile(), true)
  await assert.rejects(lstat(files.ackOutputFile), /ENOENT/)
})

test('observer implementation has no shell or network execution surface', async () => {
  const source = await readFile(observerFile, 'utf8')
  assert.doesNotMatch(source, /node:child_process|node:(?:net|http|https|tls|dgram)/)
  assert.doesNotMatch(source, /\b(?:spawn|exec|fork)\s*\(/)
  assert.match(source, /from '\.\/o4-owned-path-cleanup\.mjs'/)
  assert.match(source, /O_NOFOLLOW/)
  assert.match(source, /O_EXCL/)
})
