import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { chmod, link, mkdir, mkdtemp, readFile, realpath, rm, symlink, unlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { basename, dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import test from 'node:test'

import {
  FINAL_EVIDENCE_SCHEMA,
  assertFinalRunnerBindings,
  buildB1Composite,
  deriveDockerOperationLedger,
  finalizeO4Evidence as finalizeO4EvidenceRaw,
  validateFinalEvidenceBindings,
  validateFinalEvidenceTree,
} from './o4-final-evidence-record.mjs'
import { canonicalJSONStringify } from './o4-candidate-install-orchestrator.mjs'
import {
  PROFILE_REUSE_MANIFEST_SCHEMA,
  PROFILE_REUSE_RECORD_SCHEMA,
  computeProfileExecutionIdentityDigest,
  computeProfileRoleTupleDigest,
  formProfileReuseManifest,
  formProfileReuseReceipt,
} from './o4-profile-reuse-record.mjs'
import {
  RECOVERY_RESIDUE_MANIFEST_SCHEMA,
  RECOVERY_RESIDUE_PHASE_D_RECORD_SCHEMA,
  RECOVERY_RESIDUE_RECEIPT_NAMES,
  buildRecoveryResidueManifest,
  buildRecoveryResidueRecord,
  collectRecoveryResidueScenario as collectRecoveryResidueScenarioRaw,
  formRecoveryResidueManifest,
} from './o4-recovery-residue-record.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'

let repositoryCapture
let cleanupRepositoryCapture
test.before(async () => {
  ({ capture: repositoryCapture, cleanup: cleanupRepositoryCapture } =
    await buildTestRepositoryCapture())
})
test.after(async () => { await cleanupRepositoryCapture() })

function finalizeO4Evidence(input, dependencies = {}) {
  return finalizeO4EvidenceRaw(input, { ...dependencies, repositoryCapture })
}

function collectRecoveryResidueScenario(input) {
  return collectRecoveryResidueScenarioRaw({ ...input, repositoryCapture })
}

const D = (value) => createHash('sha256').update(value).digest('hex')

test('derives only setup plus the exact Standard Attempt/Verifier ranges from sealed A3', () => {
  const source = sealedLedger()
  const bytes = Buffer.from(`${JSON.stringify(source)}\n`)
  const ledger = deriveDockerOperationLedger(source, bytes)
  assert.equal(ledger.sourceA3LedgerSha256, D(bytes))
  assert.deepEqual(ledger.selectedA3AuditRanges.map(({ kind, ordinal }) => [kind, ordinal]), [
    ['setup', 1], ['standard_attempt', 1], ['standard_verifier', 1],
    ['standard_attempt', 2], ['standard_verifier', 2],
    ['standard_attempt', 3], ['standard_verifier', 3],
  ])
  assert.equal(ledger.commandAudit.some(({ sequence }) => sequence === 5), false,
    'Minimal Attempt must not be selected into Standard reuse')
  assert.equal(ledger.attempts.length, 3)
  assert.deepEqual(ledger.attempts.map(({ sequence }) => sequence), [1, 2, 3])
})

test('rejects broken, duplicate, skipped, and live A3 command chains', () => {
  for (const mutate of [
    (value) => { value.auditSealed = false },
    (value) => { value.commandAudit[3].sequence = 3 },
    (value) => { value.commandAudit[3].sequence = 5 },
    (value) => { value.commandAudit[3].previousDigest = D('splice') },
    (value) => { value.auditRecordCount-- },
    (value) => { value.auditFinalDigest = D('wrong-final') },
  ]) {
    const value = sealedLedger(); mutate(value)
    assert.throws(() => deriveDockerOperationLedger(value, Buffer.from(JSON.stringify(value))),
      /source A3|command|count|final|sealed/)
  }
})

test('rejects any non-Setup build, pull, or load rather than selecting an engine substitute', () => {
  const value = sealedLedger()
  const target = value.commandAudit.find(({ phase }) => phase === 'attempt')
  target.operationClass = 'build'
  reseal(value)
  assert.throws(() => deriveDockerOperationLedger(value, Buffer.from(JSON.stringify(value))),
    /forbidden image operation/)
})

test('B1 has the frozen exact schema, lowercase phase bindings, and full installed expectation', () => {
  const tuple = { environmentId: id('env'), installId: id('ins'), generationId: id('gen'),
    platform: { os: 'darwin', architecture: 'arm64' }, identity: D('tuple') }
  const committed = {
    a3: { sha256: D('a3'), value: { bindingDigest: D('binding'), auditRecordCount: 19,
      auditFinalDigest: D('a3-final') } },
    cManifest: indexed('c-manifest', { manifestDigest: D('c-manifest-semantic') }),
    cRecord: indexed('c-record', { recordDigest: D('c-record-semantic') }),
    cReceipt: indexed('c-receipt', { receiptDigest: D('c-receipt-semantic') }),
    dManifest: indexed('d-manifest', { aggregateDigest: D('d-manifest-semantic') }),
    dRecord: indexed('d-record', { recordDigest: D('d-record-semantic') }),
    dReceipt: indexed('d-receipt', { receiptDigest: D('d-receipt-semantic') }),
  }
  const indexes = {
    serviceClosure: index('service', D('service-semantic')),
    operationLedger: index('docker'), imageReuse: index('reuse', D('reuse-digest')),
    profileJourneys: ['minimal', 'standard', 'trusted_local'].map((profile) =>
      ({ profile, ...index(profile, D(`${profile}-semantic`)) })),
    recovery: index('recovery', D('recovery-digest')),
    disclosure: index('disclosure', D('disclosure-digest')),
    installedDoctor: index('doctor'), installedEvidence: index('installed'),
  }
  const installed = { schemaVersion: 'chora.m1-o4-fresh-installed-acceptance.v1', status: 'passed',
    environmentId: tuple.environmentId, installId: tuple.installId, generationId: tuple.generationId,
    platform: tuple.platform, bindings: { one: D('one') }, counts: { managedAttempts: 3 },
    components: { imageReuseRecordSha256: indexes.imageReuse.rawSha256 },
    aggregateDigest: D('aggregate') }
  const b1 = buildB1Composite({ tuple, committed, indexes, installed })
  assert.deepEqual(Object.keys(b1), [
    'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
    'bindingDigest', 'tupleIdentity', 'sourceA3LedgerSha256', 'sourceA3AuditRecordCount',
    'sourceA3AuditFinalDigest', 'serviceClosureSha256', 'phases', 'components',
    'installedEvidence', 'digest',
  ])
  assert.equal(b1.schemaVersion, FINAL_EVIDENCE_SCHEMA)
  assert.deepEqual(Object.keys(b1.phases), ['c', 'd'])
  for (const phase of Object.values(b1.phases)) assert.deepEqual(Object.keys(phase), [
    'receiptRawSha256', 'receiptDigest', 'manifestRawSha256', 'manifestDigest',
    'recordRawSha256', 'recordDigest',
  ])
  assert.deepEqual(Object.keys(b1.components), [
    'dockerOperationLedgerSha256', 'imageReuseRecordSha256', 'imageReuseDigest',
    'profileJourneys', 'recoveryRecordSha256', 'recoveryDigest',
    'disclosureRecordSha256', 'disclosureDigest', 'installedDoctorRecordSha256',
  ])
  assert.deepEqual(Object.keys(b1.installedEvidence),
    ['schemaVersion', 'bindings', 'counts', 'components', 'aggregateDigest'])
  const { digest, ...draft } = b1
  assert.equal(digest, D(canonicalJSONStringify(draft)))
  assert.equal(D(canonicalJSONStringify(b1)), D(canonicalJSONStringify(b1)),
    'full B1 canonical observation is stable')
})

test('strict entry point rejects extra callbacks and never invokes a caller Runner on shape failure', async () => {
  let calls = 0
  await assert.rejects(finalizeO4Evidence({
    paths: {}, pins: {}, standardResourceLedgerFiles: [], closeTimeoutMs: 100,
    closeAllServices: async () => { calls++ },
  }, { runner: { closeAllServices: async () => { calls++ }, sealA3Ledger: async () => { calls++ } } }),
  /keys drifted/)
  assert.equal(calls, 0)
})

test('closure and seal acknowledgements are cross-bound to tuple, A3, and pinned Runner', () => {
  const tuple = { environmentId: id('env'), installId: id('ins'), generationId: id('gen'),
    identity: D('tuple') }
  const sealedA3 = { ...tuple, tupleIdentity: tuple.identity, bindingDigest: D('binding'),
    auditRecordCount: 10, auditFinalDigest: D('final') }
  delete sealedA3.identity
  const pin = D('runner')
  const closureDraft = { schemaVersion: 'chora.m1-o4-service-closure.v1', status: 'closed',
    environmentId: tuple.environmentId, installId: tuple.installId, generationId: tuple.generationId,
    bindingDigest: sealedA3.bindingDigest, tupleIdentity: tuple.identity, runnerModuleSha256: pin,
    registeredServiceCount: 1, closedServiceCount: 1,
    services: [{ serviceIdSha256: D('service'), processGroupIdentitySha256: D('group'),
      status: 'closed', processGroupDead: true, descendantsDead: true }],
    processGroupDead: true, descendantsDead: true, completedAt: '2026-08-28T00:00:00.000Z' }
  const closure = { ...closureDraft, digest: D(canonicalJSONStringify(closureDraft)) }
  const sealAck = { status: 'sealed', beforeSha256: D('before'),
    auditRecordCount: sealedA3.auditRecordCount, auditFinalDigest: sealedA3.auditFinalDigest }
  assert.equal(assertFinalRunnerBindings({ closure, sealAck, sealedA3, tuple,
    runnerModuleSha256: pin }), true)
  for (const mutate of [
    (value) => { value.closure = redigestClosure({ ...value.closure, environmentId: id('envx') }) },
    (value) => { value.closure = redigestClosure({ ...value.closure, bindingDigest: D('other-binding') }) },
    (value) => { value.closure = redigestClosure({ ...value.closure, runnerModuleSha256: D('other-runner') }) },
    (value) => { value.sealAck = { ...value.sealAck, auditRecordCount: 11 } },
    (value) => { value.sealAck = { ...value.sealAck, auditFinalDigest: D('forged-final') } },
  ]) {
    const hostile = structuredClone({ closure, sealAck, sealedA3, tuple, runnerModuleSha256: pin })
    mutate(hostile)
    assert.throws(() => assertFinalRunnerBindings(hostile), /binding|pin|reopened sealed A3/)
  }
})

test('preexisting output and symlink output fail before close/seal', async () => {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'chora-o4-e-preflight-')))
  await chmod(root, 0o700)
  const runnerModuleFile = join(root, 'runner.mjs')
  await writeFile(runnerModuleFile, 'export const pinned = true\n', { mode: 0o400 })
  await chmod(runnerModuleFile, 0o400)
  const base = strictInput(root, runnerModuleFile)
  let calls = 0
  const runner = { closeAllServices: async () => { calls++ }, sealA3Ledger: async () => { calls++ } }
  await mkdir(base.paths.outputDirectory, { mode: 0o700 })
  await assert.rejects(finalizeO4Evidence(base, { runner }), /already exists/)
  assert.equal(calls, 0)
})

test('missing/skipped/duplicate D-E prefixes fail before Runner callbacks and clean only E output', async (t) => {
  for (const [name, count] of [['missing D', 7], ['already E', 9]]) {
    const fixture = await phasePrefixFixture(count, name)
    t.after(() => rm(fixture.root, { recursive: true, force: true }))
    let calls = 0
    await assert.rejects(finalizeO4Evidence(fixture.input, { runner: {
      closeAllServices: async () => { calls++ }, sealA3Ledger: async () => { calls++ },
    } }), /phase-D prefix|already finalized|exact phase prefix/)
    assert.equal(calls, 0)
    await assert.rejects(readFile(fixture.input.paths.outputDirectory), /ENOENT|EISDIR/)
  }
})

test('partial/error/timeout closure writes no evidence, seals nothing, and appends no E', async (t) => {
  const modes = ['partial', 'error', 'timeout']
  for (const mode of modes) {
    const fixture = await phasePrefixFixture(8, `close-${mode}`)
    t.after(() => rm(fixture.root, { recursive: true, force: true }))
    let seals = 0
    const runner = {
      async closeAllServices() {
        if (mode === 'error') throw new Error('synthetic close error')
        if (mode === 'timeout') return new Promise(() => {})
        return closureProof(fixture.tuple, fixture.input.pins.runnerModuleSha256,
          { closedServiceCount: 0, processGroupDead: false })
      },
      async sealA3Ledger() { seals++; throw new Error('must not seal') },
    }
    await assert.rejects(finalizeO4Evidence(fixture.input, { runner }),
      /partial|death proof|synthetic close error|timed out/)
    assert.equal(seals, 0)
    assert.equal((await readdirNames(fixture.input.paths.receiptDirectory)).includes('09-E.json'), false)
    await assert.rejects(lstatPath(fixture.input.paths.outputDirectory), /ENOENT/)
  }
})

test('stale phase lock fails closed and concurrent E cannot delete the active owner transaction', async (t) => {
  const stale = await phasePrefixFixture(8, 'stale-lock')
  t.after(() => rm(stale.root, { recursive: true, force: true }))
  await mkdir(join(stale.input.paths.receiptDirectory, '.phase-append.lock'), { mode: 0o700 })
  await assert.rejects(finalizeO4Evidence(stale.input, { runner: {
    closeAllServices: async () => { throw new Error('must not close') },
    sealA3Ledger: async () => { throw new Error('must not seal') },
  } }), /held or stale/)
  assert.equal((await readdirNames(stale.input.paths.receiptDirectory)).includes('.phase-append.lock'), true)
  await assert.rejects(lstatPath(stale.input.paths.outputDirectory), /ENOENT/)

  const concurrent = await phasePrefixFixture(8, 'concurrent')
  t.after(() => rm(concurrent.root, { recursive: true, force: true }))
  let entered
  const atClose = new Promise((resolvePromise) => { entered = resolvePromise })
  let release
  const wait = new Promise((resolvePromise) => { release = resolvePromise })
  const first = finalizeO4Evidence(concurrent.input, { runner: {
    closeAllServices: async () => { entered(); await wait; return closureProof(concurrent.tuple,
      concurrent.input.pins.runnerModuleSha256, { closedServiceCount: 0, processGroupDead: false }) },
    sealA3Ledger: async () => { throw new Error('must not seal') },
  } })
  await atClose
  await assert.rejects(finalizeO4Evidence(concurrent.input, { runner: {
    closeAllServices: async () => { throw new Error('second must not close') },
    sealA3Ledger: async () => { throw new Error('second must not seal') },
  } }), /already exists/)
  assert.equal((await lstatPath(concurrent.input.paths.outputDirectory)).isDirectory(), true,
    'concurrent loser deleted active owner transaction')
  release()
  await assert.rejects(first, /partial|death proof/)
  await assert.rejects(lstatPath(concurrent.input.paths.outputDirectory), /ENOENT/)
})

test('final tree rejects extra entries, writable files, hardlinks, and symlink leaves', async (t) => {
  const attacks = [
    ['root extra', async (root) => write0400(join(root, 'extra'))],
    ['raw extra', async (root) => write0400(join(root, 'raw', 'extra'))],
    ['profile extra', async (root) => write0400(join(root, 'profiles', 'extra'))],
    ['disclosure extra', async (root) => write0400(join(root, 'disclosure', 'extra'))],
    ['writable mode', async (root) => chmod(join(root, 'recovery.json'), 0o600)],
    ['hardlink', async (root) => {
      await unlink(join(root, 'recovery.json'))
      await link(join(root, 'image-reuse.json'), join(root, 'recovery.json'))
    }],
    ['symlink leaf', async (root) => {
      await unlink(join(root, 'recovery.json'))
      await symlink(join(root, 'image-reuse.json'), join(root, 'recovery.json'))
    }],
  ]
  for (const [name, attack] of attacks) {
    const root = await finalTreeFixture(name)
    t.after(() => rm(root, { recursive: true, force: true }))
    await attack(root)
    await assert.rejects(validateFinalEvidenceTree(root), /entries drifted|exact owner file|symlink/,
      name)
  }
})

test('append-bound reindex rejects valid 0400 service and Docker ledger inode swaps', async (t) => {
  for (const target of ['service', 'docker']) {
    const fixture = await boundTreeFixture(`swap-${target}`)
    t.after(() => rm(fixture.root, { recursive: true, force: true }))
    assert.equal(await validateFinalEvidenceBindings({ outputDirectory: fixture.root,
      installedDoctorRecordFile: fixture.doctorFile, expectedIndexes: fixture.indexes }), true)
    const path = target === 'service' ? join(fixture.root, 'raw', 'service-closure.json') :
      join(fixture.root, 'raw', 'docker-operation-ledger.json')
    await unlink(path)
    if (target === 'service') {
      const replacement = structuredClone(fixture.service)
      replacement.completedAt = '2026-08-28T00:00:01.000Z'
      const { digest: ignored, ...draft } = replacement; void ignored
      replacement.digest = D(canonicalJSONStringify(draft))
      await writeJSON0400(path, replacement)
    } else {
      await writeJSON0400(path, { schemaVersion: 'synthetic-ledger-v2', safe: true })
    }
    await assert.rejects(validateFinalEvidenceBindings({ outputDirectory: fixture.root,
      installedDoctorRecordFile: fixture.doctorFile, expectedIndexes: fixture.indexes }),
    /raw\/semantic indexes drifted/)
  }
})

test('module owns the four finalizer callbacks and exposes no append/restart/handoff dependency', async () => {
  const source = await readFile(resolve('e2e/o4-final-evidence-record.mjs'), 'utf8')
  assert.match(source, /finalizeEvidencePhaseE\(\{/)
  for (const callback of ['closeAllServices', 'reopenAndSealA3', 'formCompositeB1', 'validateEvidence']) {
    assert.match(source, new RegExp(`${callback}: async`))
  }
  assert.doesNotMatch(source, /dependencies\?\.(?:append|restart|buildHandoff)/)
  assert.doesNotMatch(source, /appendHandoffReceipt|restartCandidate|buildHandoff/)
})

test('real spec is marker-gated and dedicated config is serial workers=1 retries=0', async () => {
  const spec = await readFile(resolve('e2e/o4-final-evidence-real.spec.ts'), 'utf8')
  const config = await readFile(resolve('playwright.o4-final-evidence.config.ts'), 'utf8')
  assert.match(spec, /if \(process\.env\.CHORA_O4_FINAL_EVIDENCE_REAL === '1'\)/)
  assert.doesNotMatch(spec, /page\.goto|\(\{\s*page|spawn\(|execFile\(|child_process/)
  assert.match(config, /workers:\s*1/)
  assert.match(config, /retries:\s*0/)
  assert.match(config, /fullyParallel:\s*false/)
  assert.match(config, /testMatch:\s*'o4-final-evidence-real\.spec\.ts'/)
})

test('full synthetic C-D-E chain publishes, reopens, and commits one immutable 09-E', async (t) => {
  const fixture = await fullEvidenceFixture(t)
  const result = await finalizeO4Evidence(fixture.input, { runner: fixture.runner })
  assert.equal(result.receipt.phase, 'E')
  assert.equal(result.receipt.sequence, 9)
  assert.equal(result.receipt.operationDigest, result.sourceA3LedgerSha256)
  const b1Bytes = await readFile(join(result.outputDirectory, 'b1-composite.json'))
  const b1 = JSON.parse(b1Bytes)
  assert.equal(result.receipt.observationDigest, D(canonicalJSONStringify(b1)))
  assert.equal(result.b1Sha256, result.receipt.observationDigest)
  assert.equal(await validateFinalEvidenceTree(result.outputDirectory), true)
  for (const path of await finalOutputFiles(result.outputDirectory)) {
    assert.equal((await lstatPath(path)).mode & 0o777, 0o400, `${path} is not immutable`)
  }
  assert.deepEqual((await readdirNames(fixture.input.paths.receiptDirectory))
    .filter((name) => /^09-E\.json$/.test(name)), ['09-E.json'])

  const committed = new Map()
  for (const path of [join(fixture.input.paths.receiptDirectory, '09-E.json'),
    ...await finalOutputFiles(result.outputDirectory)]) committed.set(path, await readFile(path))
  let callbacks = 0
  await assert.rejects(finalizeO4Evidence(fixture.input, { runner: {
    closeAllServices: async () => { callbacks++; throw new Error('duplicate callback') },
    sealA3Ledger: async () => { callbacks++; throw new Error('duplicate callback') },
  } }), /already exists|already finalized|exact phase/)
  assert.equal(callbacks, 0)
  for (const [path, bytes] of committed) assert.deepEqual(await readFile(path), bytes)
})

test('post-seal C/D raw-byte drift retains sealed A3 but cleans only the E transaction', async (t) => {
  const fixture = await fullEvidenceFixture(t)
  const originalSeal = fixture.runner.sealA3Ledger
  fixture.runner.sealA3Ledger = async (request) => {
    const result = await originalSeal(request)
    const path = fixture.input.paths.phaseCRecordFile
    const bytes = await readFile(path)
    await chmod(path, 0o600)
    await writeFile(path, Buffer.concat([bytes, Buffer.from(' ')]), { flag: 'w' })
    await chmod(path, 0o400)
    return result
  }
  await assert.rejects(finalizeO4Evidence(fixture.input, { runner: fixture.runner }),
    /committed C\/D evidence changed while A3 was sealed/)
  const sealed = JSON.parse(await readFile(fixture.input.paths.sourceA3LedgerFile, 'utf8'))
  assert.equal(sealed.auditSealed, true)
  assert.equal((await lstatPath(fixture.input.paths.sourceA3LedgerFile)).mode & 0o777, 0o400)
  await assert.rejects(lstatPath(fixture.input.paths.outputDirectory), /ENOENT/)
  assert.equal((await readdirNames(fixture.input.paths.receiptDirectory)).includes('09-E.json'), false)
})

function sealedLedger() {
  const role = (artifactId, archiveSize) => ({ artifactId,
    archiveSha256: D(`${artifactId}-archive`), archiveSize,
    dockerConfigImageId: `sha256:${D(`${artifactId}-config`)}` })
  const managed = role('managed-pi-runtime', 101)
  const boundary = role('network-boundary', 103)
  const records = []
  const add = (phase, operationClass, target = null) => {
    const draft = { sequence: records.length + 1, phase,
      subsystem: phase === 'setup' ? 'installation' : phase === 'verifier' ? 'verifier' : 'managed',
      invocation: 'run', operationClass, safeTargetSha256: target, result: 'succeeded',
      previousDigest: records.at(-1)?.digest ?? '0'.repeat(64) }
    records.push({ ...draft, digest: D(canonicalJSONStringify(draft)) })
  }
  add('setup', 'load', managed.archiveSha256)
  add('setup', 'inspect', managed.dockerConfigImageId.slice(7))
  add('setup', 'load', boundary.archiveSha256)
  add('setup', 'inspect', boundary.dockerConfigImageId.slice(7))
  add('attempt', 'exec'); add('verifier', 'inspect') // Minimal
  for (let index = 0; index < 3; index++) { add('attempt', 'exec'); add('verifier', 'inspect') }
  const attempt = (ordinal, profile, cohort, start, end, verifierStart, verifierEnd) => ({
    sourceAttemptOrdinal: ordinal, profile, cohortSequence: cohort,
    taskId: `task_00000000-0000-7000-8000-${String(ordinal).padStart(12, '0')}`,
    runId: `run_00000000-0000-7000-8000-${String(ordinal).padStart(12, '0')}`,
    attemptId: `attempt_00000000-0000-7000-8000-${String(ordinal).padStart(12, '0')}`,
    scenario: null, attemptState: 'output_submitted', terminalReason: null,
    verifierRequired: true, verifierStartSequence: verifierStart, verifierEndSequence: verifierEnd,
    verifierSliceDigest: D(canonicalJSONStringify(records.slice(verifierStart - 1, verifierEnd))),
    workspaceIdentity: `sha256:${D(`workspace-${ordinal}`)}`, tupleIdentity: D('tuple'),
    executionIdentityDigest: D(`execution-${ordinal}`), status: 'succeeded',
    runtimeIdentitySha256: D(`runtime-${ordinal}`), resourceLedgerSha256: D(`resource-${ordinal}`),
    roleTupleDigest: D('role-tuple'), frameworkRetryCount: 0, hiddenRetryCount: 0,
    auditStartSequence: start, auditEndSequence: end,
    auditSliceDigest: D(canonicalJSONStringify(records.slice(start - 1, end))),
    operationCounts: { build: 0, pull: 0, load: 0 }, terminalResidue: zeroResidue(),
  })
  const value = {
    schemaVersion: 'chora.m1-o4-sealed-a3-runner-ledger.v1', status: 'passed',
    environmentId: id('env'), installId: id('ins'), generationId: id('gen'),
    platform: { os: 'darwin', architecture: 'arm64' }, bindingDigest: D('binding'), tupleIdentity: D('tuple'),
    observationSource: 'exact-runner-command-audit', complete: true, engineEventsUsed: false,
    roleImages: { managed_pi_runtime: managed, network_boundary: boundary,
      independent_verifier: managed, capability_probe: managed },
    commandAudit: records, auditRecordCount: records.length,
    auditFinalDigest: records.at(-1).digest, auditSealed: true,
    attempts: [attempt(1, 'minimal', null, 5, 5, 6, 6),
      attempt(2, 'standard', 1, 7, 7, 8, 8), attempt(3, 'standard', 2, 9, 9, 10, 10),
      attempt(4, 'standard', 3, 11, 11, 12, 12)],
  }
  return value
}

function reseal(value) {
  let previous = '0'.repeat(64)
  value.commandAudit = value.commandAudit.map((record, index) => {
    const draft = { ...record, sequence: index + 1, previousDigest: previous }; delete draft.digest
    const digest = D(canonicalJSONStringify(draft)); previous = digest
    return { ...draft, digest }
  })
  value.auditRecordCount = value.commandAudit.length
  value.auditFinalDigest = previous
}

function strictInput(root, runnerModuleFile) {
  const names = [
    'tupleFile', 'receiptDirectory', 'sourceA3LedgerFile', 'phaseCManifestFile', 'phaseCRecordFile',
    'phaseCReceiptDirectory', 'phaseDManifestFile', 'phaseDRecordFile', 'phaseDReceiptDirectory',
    'phaseDDisclosureSourceDirectory', 'phaseDScreenshotDirectory',
    'phaseDScreenshotMetadataDirectory', 'phaseDScreenshotOCRDirectory',
    'privateEnvironmentMarkerFile', 'installedDoctorRecordFile', 'modelObservationFile',
    'credentialCorpusFile',
    'candidateManifestFile', 'sourceManifestFile', 'sourceBundleRoot', 'binaryFile', 'webDirectory',
    'releaseSpecFile', 'releaseManifestFile', 'offlineBundleEvidenceFile', 'pathPiProvenanceFile',
    'privatePiProvenanceFile', 'qualificationFile', 'engineEndpointEvidenceFile', 'outputDirectory',
  ]
  const paths = Object.fromEntries(names.map((name) => [name, join(root, name)]))
  paths.runnerModuleFile = runnerModuleFile
  return { paths, pins: { runnerModuleSha256: D('export const pinned = true\n') },
    standardResourceLedgerFiles: [1, 2, 3].map((value) => join(root, `resource-${value}.json`)),
    closeTimeoutMs: 100 }
}

async function phasePrefixFixture(count, suffix) {
  const root = await realpath(await mkdtemp(join(tmpdir(), `chora-o4-e-prefix-${suffix.replaceAll(' ', '-')}-`)))
  await chmod(root, 0o700)
  const receiptDirectory = join(root, 'handoff')
  await mkdir(receiptDirectory, { mode: 0o700 })
  const repositoryRoot = '/private/tmp/chora-o4-final-evidence-repository'
  const repositoryCommit = 'c'.repeat(40)
  const repositoryDraft = {
    schemaVersion: 'chora.m1-o4-repository-authority.v2', repositoryRoot,
    repositoryCommit, headRef: 'refs/heads/main',
    root: { path: repositoryRoot, type: 'directory', mode: 0o700,
      uid: process.getuid(), gid: process.getgid(), dev: '1', ino: '2' },
    gitRoot: { path: `${repositoryRoot}/.git`, type: 'directory', mode: 0o700,
      uid: process.getuid(), gid: process.getgid(), dev: '1', ino: '3' },
    configSha256: D('repository-config'),
    index: [{ flag: 'H', mode: '100644', object: 'd'.repeat(40), stage: 0, path: 'README.md' }],
    refs: [{ name: 'refs/heads/main', object: repositoryCommit }],
    worktree: [{ path: 'README.md', type: 'file', mode: 0o644, sha256: D('README') }],
  }
  const repositoryAuthority = { ...repositoryDraft,
    repositoryClosureSha256: D(canonicalJSONStringify(repositoryDraft)) }
  const digestBindingKeys = [
    'sourceManifestSha256', 'sourceAggregateSha256', 'candidateManifestSha256',
    'binarySha256', 'webAggregateSha256', 'releaseSpecSha256', 'releaseManifestSha256',
    'offlineBundleEvidenceSha256', 'pathPiProvenanceSha256', 'privatePiProvenanceSha256',
    'modelAuthoritySha256', 'endpointEvidenceSha256', 'preflightFileSha256',
    'preflightDigest', 'actualReleaseManifestSha256', 'privatePiManifestSha256',
    'authFileSha256', 'caFileSha256', 'colimaSourceSha256', 'dockerClientSourceSha256',
    'dockerCLISha256', 'dockerContextSha256', 'endpointDigest', 'roleTupleSha256',
    'privateRuntimeConfigSha256',
  ]
  const bindings = Object.fromEntries(digestBindingKeys.map((key) => [key, D(key)]))
  Object.assign(bindings, { repositoryCommit,
    repositoryClosureSha256: repositoryAuthority.repositoryClosureSha256 })
  const tupleDraft = { schemaVersion: 'chora.m1-o4-candidate-install-tuple.v2', status: 'frozen',
    environmentId: id('env'), installId: id('ins'), generationId: id('gen'),
    platform: { os: 'darwin', architecture: 'arm64' },
    roots: Object.fromEntries([
      'installRootSha256', 'dataRootSha256', 'stateRootSha256', 'receiptDirectorySha256',
      'privatePiRootSha256', 'probeRuntimeRootSha256', 'colimaToolRootSha256',
    ].map((key) => [key, D(key)])),
    repositoryAuthority, bindings,
    plan: ['offline-prebuilt-install', 'product-doctor', 'setup-once', 'installed-doctor',
      'process-boundary', 'active-serve'] }
  const tuple = { ...tupleDraft, identity: D(canonicalJSONStringify(tupleDraft)) }
  const tupleFile = join(receiptDirectory, 'tuple.json')
  await writeJSON0400(tupleFile, tuple)
  const phases = ['preflight', 'install', 'product_doctor', 'setup', 'doctor', 'serve', 'C', 'D', 'E']
  let previous = null
  for (let index = 0; index < count; index++) {
    const draft = { schemaVersion: 'chora.m1-o4-candidate-phase-receipt.v1', tupleIdentity: tuple.identity,
      phase: phases[index], sequence: index + 1, status: 'passed', operationDigest: D(`op-${index}`),
      observationDigest: index === 0 ? null : D(`obs-${index}`), previousReceiptDigest: previous }
    const receipt = { ...draft, receiptDigest: D(canonicalJSONStringify(draft)) }
    previous = receipt.receiptDigest
    await writeJSON0400(join(receiptDirectory, `${String(index + 1).padStart(2, '0')}-${phases[index]}.json`), receipt)
  }
  const runnerModuleFile = join(root, 'runner.mjs')
  await writeFile(runnerModuleFile, 'export const pinned = true\n', { flag: 'wx', mode: 0o400 })
  await chmod(runnerModuleFile, 0o400)
  const input = strictInput(root, runnerModuleFile)
  input.paths.tupleFile = tupleFile
  input.paths.receiptDirectory = receiptDirectory
  input.closeTimeoutMs = 50
  return { root, tuple, input }
}

function closureProof(tuple, runnerModuleSha256, overrides = {}) {
  const draft = { schemaVersion: 'chora.m1-o4-service-closure.v1', status: 'closed',
    environmentId: tuple.environmentId, installId: tuple.installId, generationId: tuple.generationId,
    bindingDigest: D('binding'), tupleIdentity: tuple.identity, runnerModuleSha256,
    registeredServiceCount: 1, closedServiceCount: 1,
    services: [{ serviceIdSha256: D('service'), processGroupIdentitySha256: D('group'),
      status: 'closed', processGroupDead: true, descendantsDead: true }],
    processGroupDead: true, descendantsDead: true, completedAt: '2026-08-28T00:00:00.000Z',
    ...overrides }
  return { ...draft, digest: D(canonicalJSONStringify(draft)) }
}

async function writeJSON0400(path, value) { await writeFile(path, `${JSON.stringify(value)}\n`, { flag: 'wx', mode: 0o400 }); await chmod(path, 0o400) }
async function readdirNames(path) { return (await import('node:fs/promises')).readdir(path) }
async function lstatPath(path) { return (await import('node:fs/promises')).lstat(path) }

function zeroResidue() { return { ownedContainers: 0, ownedNetworks: 0, ownedVolumes: 0,
  ownedConfigs: 0, ownedWorkspaces: 0, ownedProcessGroups: 0, activeReferences: 0,
  recoverableReferences: 0 } }
function indexed(seed, value) { return { sha256: D(seed), value } }
function index(seed, semanticDigest = D(`${seed}-semantic`)) { return { file: `${seed}.json`, rawSha256: D(`${seed}-raw`), semanticDigest } }
function id(prefix) { return `${prefix}_${prefix.repeat(24)}` }
function redigestClosure(value) { const draft = { ...value }; delete draft.digest; return { ...draft, digest: D(canonicalJSONStringify(draft)) } }

async function finalTreeFixture(suffix) {
  const parent = await realpath(await mkdtemp(join(tmpdir(), `chora-o4-e-tree-${suffix.replaceAll(' ', '-')}-`)))
  await chmod(parent, 0o700)
  const raw = join(parent, 'raw'); const profiles = join(parent, 'profiles'); const disclosure = join(parent, 'disclosure')
  for (const directory of [raw, profiles, disclosure]) await mkdir(directory, { mode: 0o700 })
  for (const name of ['docker-operation-ledger.json', 'service-closure.json']) await write0400(join(raw, name))
  for (const name of ['minimal.json', 'standard.json', 'trusted-local.json']) await write0400(join(profiles, name))
  for (const name of ['change.patch', 'disclosure.json', 'execution.log', 'payload.json',
    'screen-metadata.json', 'screen-ocr.json', 'screen.png', 'state.db']) await write0400(join(disclosure, name))
  for (const name of ['b1-composite.json', 'image-reuse.json', 'installed-evidence.json', 'recovery.json']) await write0400(join(parent, name))
  assert.equal(await validateFinalEvidenceTree(parent), true)
  return parent
}

async function boundTreeFixture(suffix) {
  const root = await finalTreeFixture(suffix)
  const tuple = { environmentId: id('env'), installId: id('ins'), generationId: id('gen'),
    identity: D('tree-tuple') }
  const pin = D('tree-runner')
  const service = closureProof(tuple, pin)
  const values = new Map([
    [join(root, 'raw', 'service-closure.json'), service],
    [join(root, 'raw', 'docker-operation-ledger.json'), { schemaVersion: 'synthetic-ledger-v1', safe: true }],
    [join(root, 'image-reuse.json'), { digest: D('tree-reuse') }],
    [join(root, 'recovery.json'), { digest: D('tree-recovery') }],
    [join(root, 'disclosure', 'disclosure.json'), { digest: D('tree-disclosure') }],
    [join(root, 'installed-evidence.json'), { aggregateDigest: D('tree-installed') }],
    [join(root, 'profiles', 'minimal.json'), { profile: 'minimal', digest: D('tree-minimal') }],
    [join(root, 'profiles', 'standard.json'), { profile: 'standard', digest: D('tree-standard') }],
    [join(root, 'profiles', 'trusted-local.json'), { profile: 'trusted_local', digest: D('tree-trusted') }],
  ])
  for (const [path, value] of values) {
    await unlink(path)
    await writeJSON0400(path, value)
  }
  const doctorFile = join(dirname(root), `${basename(root)}-doctor.json`)
  const doctor = { safe: true, binding: D('tree-doctor') }
  await writeJSON0400(doctorFile, doctor)
  const idx = async (path, semanticDigest) => ({ file: basename(path),
    rawSha256: D(await readFile(path)), semanticDigest })
  const profiles = []
  for (const [profile, name] of [['minimal', 'minimal.json'], ['standard', 'standard.json'],
    ['trusted_local', 'trusted-local.json']]) {
    profiles.push({ profile, ...await idx(join(root, 'profiles', name), values.get(join(root, 'profiles', name)).digest) })
  }
  const operationValue = values.get(join(root, 'raw', 'docker-operation-ledger.json'))
  const indexes = {
    serviceClosure: await idx(join(root, 'raw', 'service-closure.json'), service.digest),
    operationLedger: await idx(join(root, 'raw', 'docker-operation-ledger.json'),
      D(canonicalJSONStringify(operationValue))),
    imageReuse: await idx(join(root, 'image-reuse.json'), D('tree-reuse')),
    profileJourneys: profiles,
    recovery: await idx(join(root, 'recovery.json'), D('tree-recovery')),
    disclosure: await idx(join(root, 'disclosure', 'disclosure.json'), D('tree-disclosure')),
    installedDoctor: await idx(doctorFile, D(canonicalJSONStringify(doctor))),
    installedEvidence: await idx(join(root, 'installed-evidence.json'), D('tree-installed')),
  }
  return { root, doctorFile, indexes, service }
}

async function write0400(path) { await writeFile(path, 'x', { flag: 'wx', mode: 0o400 }); await chmod(path, 0o400) }

async function fullEvidenceFixture(t) {
  const factories = await loadFixtureFactories(t)
  const authority = await factories.gate.fixture(21)
  t.after(() => rm(authority.root, { recursive: true, force: true }))
  const phaseD = await factories.recovery.diskFixture({ after() {} }, 'e-full-chain', {
    identity: { ...authority.identity, bindingDigest: authority.doctor.bindingDigest },
  })
  t.after(() => rm(phaseD.root, { recursive: true, force: true }))
  const tuple = JSON.parse(await readFile(phaseD.tupleFile, 'utf8'))
  const gate = await factories.gate.fixture(21, {
    tupleIdentity: tuple.identity,
  })
  t.after(() => rm(gate.root, { recursive: true, force: true }))
  for (const file of [gate.inputs.credentialCorpusFile, gate.inputs.doctorRecordFile,
    gate.inputs.privateEnvironmentMarkerFile]) await chmod(file, 0o400)
  const profile = await factories.profile.fixture()
  t.after(() => rm(profile.root, { recursive: true, force: true }))

  const sourceFile = gate.inputs.sourceA3LedgerFile
  const sealedSource = JSON.parse(await readFile(sourceFile, 'utf8'))
  const unsealedSource = { ...sealedSource, status: 'recording', complete: false, auditSealed: false }
  await replaceReadonlyJSON(sourceFile, unsealedSource, 0o600)
  const unsealedBytes = await readFile(sourceFile)
  const bindingDigest = gate.doctor.bindingDigest

  const profileReceipts = profile.receipts.map((original, index) => {
    const value = structuredClone(original)
    Object.assign(value, {
      environmentId: tuple.environmentId, installId: tuple.installId,
      generationId: tuple.generationId, platform: tuple.platform,
      bindingDigest, tupleIdentity: tuple.identity,
    })
    const attempt = index < 4 ? sealedSource.attempts[index] : null
    if (attempt) {
      Object.assign(value, {
        taskId: attempt.taskId, runId: attempt.runId, attemptId: attempt.attemptId,
        workspaceIdentity: attempt.workspaceIdentity,
        runtimeIdentitySha256: attempt.runtimeIdentitySha256,
        resourceLedgerSha256: attempt.resourceLedgerSha256,
        roleTupleDigest: attempt.roleTupleDigest,
        operationCounts: attempt.operationCounts, terminalResidue: attempt.terminalResidue,
        attemptSource: { startSequence: attempt.auditStartSequence,
          endSequence: attempt.auditEndSequence, sliceDigest: attempt.auditSliceDigest },
        verifierSource: { startSequence: attempt.verifierStartSequence,
          endSequence: attempt.verifierEndSequence, sliceDigest: attempt.verifierSliceDigest },
      })
    } else {
      const verifier = sealedSource.attempts.at(-1)
      value.taskId = syntheticProductID('task', 900)
      value.runId = syntheticProductID('run', 910)
      value.attemptId = syntheticProductID('attempt', 920)
      value.workspaceIdentity = `sha256:${D('full-trusted-workspace')}`
      value.verifierSource = { startSequence: verifier.verifierStartSequence,
        endSequence: verifier.verifierEndSequence, sliceDigest: verifier.verifierSliceDigest }
    }
    value.executionIdentityDigest = computeProfileExecutionIdentityDigest(value)
    delete value.receiptDigest
    return formProfileReuseReceipt(value)
  })
  const profileReceiptDirectory = join(profile.root, 'e-receipts')
  await mkdir(profileReceiptDirectory, { mode: 0o700 })
  const profileNames = ['01-minimal.json', '02-standard-1.json', '03-standard-2.json',
    '04-standard-3.json', '05-trusted-local.json']
  for (let index = 0; index < profileNames.length; index++) {
    await writeJSON0400(join(profileReceiptDirectory, profileNames[index]), profileReceipts[index])
  }

  const roleTupleDigest = computeProfileRoleTupleDigest(sealedSource.roleImages)
  const cManifestDraft = structuredClone(profile.manifest)
  delete cManifestDraft.manifestDigest
  Object.assign(cManifestDraft, {
    schemaVersion: PROFILE_REUSE_MANIFEST_SCHEMA,
    environmentId: tuple.environmentId, installId: tuple.installId,
    generationId: tuple.generationId, platform: tuple.platform,
    bindingDigest, tupleIdentity: tuple.identity,
    sourceA3LedgerSha256: D('phase-c-boundary-a3'),
    sourceA3AuditRecordCount: 1,
    sourceA3AuditFinalDigest: sealedSource.commandAudit[0].digest,
    roleImages: sealedSource.roleImages, roleTupleDigest,
  })
  const cManifest = formProfileReuseManifest(cManifestDraft)
  const cManifestFile = join(profile.root, 'e-manifest.json')
  await writeJSON0400(cManifestFile, cManifest)

  const cObservationDigest = D('phase-c-observation')
  const cReceipt = await replacePhaseReceipt(phaseD.handoffReceiptDirectory, tuple, 7, 'C', {
    operationDigest: D('phase-c-operation'), observationDigest: cObservationDigest,
  })
  const trusted = profileReceipts.at(-1)
  const cRecordDraft = {
    schemaVersion: PROFILE_REUSE_RECORD_SCHEMA, status: 'passed',
    environmentId: tuple.environmentId, installId: tuple.installId,
    generationId: tuple.generationId, platform: tuple.platform,
    bindingDigest, tupleIdentity: tuple.identity,
    sourceA3LedgerSha256: cManifest.sourceA3LedgerSha256,
    sourceA3AuditRecordCount: cManifest.sourceA3AuditRecordCount,
    sourceA3AuditFinalDigest: cManifest.sourceA3AuditFinalDigest,
    installedManagedRoleTupleDigest: roleTupleDigest,
    journeyReceiptDigests: profileReceipts.map(({ receiptDigest }) => receiptDigest),
    executionIdentityDigests: profileReceipts.map(({ executionIdentityDigest }) => executionIdentityDigest),
    resourceLedgerDigests: profileReceipts.map(({ resourceLedgerSha256 }) => resourceLedgerSha256),
    trustedHostEvidenceDigest: D(canonicalJSONStringify(trusted.profileEvidence)),
    resourceRequestDigests: profileReceipts.map(({ resourceRequestDigest }) => resourceRequestDigest),
    resourceAckDigests: profileReceipts.map(({ resourceAckDigest }) => resourceAckDigest),
    resourceObserverSha256: profileReceipts[0].resourceObserverSha256,
    standardReuseCount: 3, frameworkRetryCount: 0, hiddenRetryCount: 0,
    a3SealedByPhaseC: false, finalProfileJourneysFormed: false, b1CompositeFormed: false,
    operationDigest: D('phase-c-record-operation'), observationDigest: cObservationDigest,
    handoffReceiptDigest: cReceipt.receiptDigest,
  }
  const cRecord = { ...cRecordDraft, recordDigest: D(canonicalJSONStringify(cRecordDraft)) }
  const cRecordFile = join(profile.root, 'phase-c-record.json')
  await writeJSON0400(cRecordFile, cRecord)

  const sourceRanges = sourceRangesFor(sealedSource.attempts)
  phaseD.receipts = phaseD.receipts.map((original, index) => {
    const value = structuredClone(original)
    Object.assign(value, {
      environmentId: tuple.environmentId, installId: tuple.installId,
      generationId: tuple.generationId, bindingDigest, tupleIdentity: tuple.identity,
      sourceA3: sourceRanges[index],
    })
    delete value.receiptDigest
    return factories.recovery.formReceipt(value)
  })
  const dPaths = await factories.recovery.prepareCompleteFixture(phaseD)
  for (let index = 0; index < phaseD.receipts.length; index++) {
    await collectRecoveryResidueScenario({ receipt: phaseD.receipts[index],
      outputPath: join(dPaths.receiptDirectory, RECOVERY_RESIDUE_RECEIPT_NAMES[index]) })
  }
  const sharedImages = [
    { artifactId: 'managed-pi-runtime',
      roles: ['managed_pi_runtime', 'independent_verifier', 'capability_probe'],
      ...sealedSource.roleImages.managed_pi_runtime, present: true },
    { artifactId: 'network-boundary', roles: ['network_boundary'],
      ...sealedSource.roleImages.network_boundary, present: true },
  ]
  await replaceReadonlyJSON(dPaths.sourceA3LedgerFile, unsealedSource, 0o400)
  let dManifest = await buildRecoveryResidueManifest({
    paths: dPaths, ...phaseD.identity, sharedImages,
  })
  const dManifestDraft = structuredClone(dManifest)
  delete dManifestDraft.aggregateDigest
  dManifestDraft.sourceA3 = {
    file: basename(sourceFile), rawSha256: D(unsealedBytes),
    semanticDigest: D(canonicalJSONStringify(unsealedSource)),
    ranges: sourceRanges.map((range, index) => ({
      scenario: phaseD.receipts[index].name, ...range,
    })),
  }
  dManifest = formRecoveryResidueManifest(dManifestDraft)
  assert.equal(dManifest.schemaVersion, RECOVERY_RESIDUE_MANIFEST_SCHEMA)
  const dRecord = await buildRecoveryResidueRecord({
    manifest: dManifest, receiptDirectory: dPaths.receiptDirectory,
  })
  assert.equal(dRecord.schemaVersion, RECOVERY_RESIDUE_PHASE_D_RECORD_SCHEMA)
  await writeJSON0400(dPaths.manifestFile, dManifest)
  await writeJSON0400(dPaths.recordFile, dRecord)
  await replacePhaseReceipt(phaseD.handoffReceiptDirectory, tuple, 8, 'D', {
    operationDigest: D(await readFile(dPaths.manifestFile)),
    observationDigest: dRecord.recordDigest,
  })

  const outputDirectory = join(phaseD.root, 'phase-e-final')
  const input = {
    paths: {
      tupleFile: phaseD.tupleFile, receiptDirectory: phaseD.handoffReceiptDirectory,
      sourceA3LedgerFile: sourceFile,
      phaseCManifestFile: cManifestFile, phaseCRecordFile: cRecordFile,
      phaseCReceiptDirectory: profileReceiptDirectory,
      phaseDManifestFile: dPaths.manifestFile, phaseDRecordFile: dPaths.recordFile,
      phaseDReceiptDirectory: dPaths.receiptDirectory,
      phaseDDisclosureSourceDirectory: dPaths.disclosureSourceDirectory,
      phaseDScreenshotDirectory: dPaths.screenshotDirectory,
      phaseDScreenshotMetadataDirectory: dPaths.screenshotMetadataDirectory,
      phaseDScreenshotOCRDirectory: dPaths.screenshotOCRDirectory,
      privateEnvironmentMarkerFile: gate.inputs.privateEnvironmentMarkerFile,
      installedDoctorRecordFile: gate.inputs.doctorRecordFile,
      modelObservationFile: gate.inputs.modelObservationFile,
      credentialCorpusFile: gate.inputs.credentialCorpusFile,
      candidateManifestFile: gate.inputs.candidateManifestFile,
      sourceManifestFile: gate.inputs.sourceManifestFile,
      sourceBundleRoot: gate.inputs.sourceBundleRoot,
      binaryFile: gate.inputs.binaryFile, webDirectory: gate.inputs.webDirectory,
      releaseSpecFile: gate.inputs.releaseSpecFile,
      releaseManifestFile: gate.inputs.releaseManifestFile,
      offlineBundleEvidenceFile: gate.inputs.offlineBundleEvidenceFile,
      pathPiProvenanceFile: gate.inputs.pathPiProvenanceFile,
      privatePiProvenanceFile: gate.inputs.privatePiProvenanceFile,
      qualificationFile: gate.inputs.qualificationFile,
      engineEndpointEvidenceFile: gate.inputs.engineEndpointEvidenceFile,
      runnerModuleFile: phaseD.prepareInput.b2RunnerModuleFile, outputDirectory,
    },
    pins: { runnerModuleSha256: phaseD.prepareInput.b2RunnerModuleSha256 },
    standardResourceLedgerFiles: [1, 2, 3].map((index) => join(gate.root, `resource-${index}.json`)),
    closeTimeoutMs: 1_000,
  }
  const runner = {
    async closeAllServices() { return closureProof(tuple, input.pins.runnerModuleSha256, {
      bindingDigest, completedAt: '2026-08-28T12:00:00.000Z',
    }) },
    async sealA3Ledger(request) {
      assert.equal(request.expectedUnsealedSha256, D(unsealedBytes))
      await replaceReadonlyJSON(sourceFile, sealedSource, 0o400)
      return { status: 'sealed', beforeSha256: request.expectedUnsealedSha256,
        auditRecordCount: sealedSource.auditRecordCount,
        auditFinalDigest: sealedSource.auditFinalDigest }
    },
  }
  return { input, runner }
}

function sourceRangesFor(attempts) {
  return attempts.slice(0, 8).map((_, index) => {
    const startSequence = index === 0 ? 1 : index + 2
    const endSequence = index === 0 ? 2 : index + 2
    return { startSequence, endSequence,
      sliceDigest: D(canonicalJSONStringify(attempts.slice(startSequence - 1, endSequence))) }
  })
}

function syntheticProductID(prefix, value) {
  return `${prefix}_00000000-0000-7000-8000-${String(value).padStart(12, '0')}`
}

async function replacePhaseReceipt(directory, tuple, sequence, phase, fields) {
  const previous = sequence === 1 ? null : JSON.parse(await readFile(join(directory,
    `${String(sequence - 1).padStart(2, '0')}-${sequence === 7 ? 'serve' : 'C'}.json`), 'utf8')).receiptDigest
  const draft = { schemaVersion: 'chora.m1-o4-candidate-phase-receipt.v1',
    tupleIdentity: tuple.identity, phase, sequence, status: 'passed', ...fields,
    previousReceiptDigest: previous }
  const receipt = { ...draft, receiptDigest: D(canonicalJSONStringify(draft)) }
  const path = join(directory, `${String(sequence).padStart(2, '0')}-${phase}.json`)
  await replaceReadonlyJSON(path, receipt, 0o400)
  return receipt
}

async function replaceReadonlyJSON(path, value, mode) {
  await chmod(path, 0o600).catch(() => {})
  await writeFile(path, `${JSON.stringify(value)}\n`, { flag: 'w', mode: 0o600 })
  await chmod(path, mode)
}

async function finalOutputFiles(root) {
  return [
    ...['docker-operation-ledger.json', 'service-closure.json'].map((name) => join(root, 'raw', name)),
    ...['minimal.json', 'standard.json', 'trusted-local.json'].map((name) => join(root, 'profiles', name)),
    ...['change.patch', 'disclosure.json', 'execution.log', 'payload.json', 'screen-metadata.json',
      'screen-ocr.json', 'screen.png', 'state.db'].map((name) => join(root, 'disclosure', name)),
    ...['b1-composite.json', 'image-reuse.json', 'installed-evidence.json', 'recovery.json']
      .map((name) => join(root, name)),
  ]
}

async function loadFixtureFactories(t) {
  const scratch = await realpath(await mkdtemp(join(tmpdir(), 'chora-o4-e-fixture-loader-')))
  t.after(() => rm(scratch, { recursive: true, force: true }))
  const e2e = resolve('e2e')
  const load = async (sourceName, transform, exports) => {
    let source = await readFile(join(e2e, sourceName), 'utf8')
    source = source.replace("import test from 'node:test'", `
const __fixtureBeforeHooks = []
const __fixtureAfterHooks = []
const test = Object.assign(() => undefined, {
  before: (callback) => { __fixtureBeforeHooks.push(callback) },
  after: (callback) => { __fixtureAfterHooks.push(callback) },
})`)
    source = source.replace(/from '(\.\/[^']+)'/g, (_, relativePath) =>
      `from '${pathToFileURL(resolve(e2e, relativePath)).href}'`)
    source = transform(source)
    source += `
export { ${exports} }
export async function __runFixtureBeforeHooks() {
  for (const callback of __fixtureBeforeHooks) await callback()
}
export async function __runFixtureAfterHooks() {
  for (const callback of [...__fixtureAfterHooks].reverse()) await callback()
}
`
    const output = join(scratch, sourceName)
    await writeFile(output, source, { flag: 'wx', mode: 0o600 })
    const loaded = await import(`${pathToFileURL(output).href}?fixture=${D(sourceName)}`)
    await loaded.__runFixtureBeforeHooks()
    t.after(() => loaded.__runFixtureAfterHooks())
    return loaded
  }
  const gate = await load('o4-installed-evidence-gate.test.mjs', (source) => source
    .replace('mkdir, mkdtemp, readFile, stat, unlink, writeFile,',
      'mkdir, mkdtemp, readFile, realpath, stat, unlink, writeFile,')
    .replace("const root = await mkdtemp(join(tmpdir(), 'chora-o4-gate-'))",
      "const root = await realpath(await mkdtemp(join(tmpdir(), 'chora-o4-gate-')))" )
    .replace('async function fixture(seed = 5) {', 'async function fixture(seed = 5, override = {}) {')
    .replace("  const identity = {\n    environmentId: deriveEnvironmentId(nonce),\n    installId: opaque('ins', String(seed)),\n    generationId: opaque('gen', String(seed + 1)),\n  }",
      "  const identity = override.identity ?? {\n    environmentId: deriveEnvironmentId(nonce),\n    installId: opaque('ins', String(seed)),\n    generationId: opaque('gen', String(seed + 1)),\n  }")
    .replace("  const tupleIdentity = digest(`b2-tuple:${seed}`)",
      "  const tupleIdentity = override.tupleIdentity ?? digest(`b2-tuple:${seed}`)"),
  'fixture')
  const profile = await load('o4-profile-reuse-record.test.mjs', (source) => source, 'fixture')
  const recovery = await load('o4-recovery-residue-record.test.mjs', (source) =>
    source.replace("platform: { os: 'darwin', arch: 'arm64' }",
      "platform: { os: 'darwin', architecture: 'arm64' }")
      .replace("async function diskFixture(t, suffix = 'happy') {\n  const memory = receiptFixture()",
        "async function diskFixture(t, suffix = 'happy', override = {}) {\n  const memory = receiptFixture()\n  Object.assign(memory.identity, override.identity ?? {})\n  memory.receipts = memory.receipts.map((receipt) => formRecoveryResidueScenarioReceipt({\n    ...without(receipt, 'receiptDigest'),\n    environmentId: memory.identity.environmentId, installId: memory.identity.installId,\n    generationId: memory.identity.generationId, bindingDigest: memory.identity.bindingDigest,\n  }))"),
  'diskFixture, prepareCompleteFixture, formRecoveryResidueScenarioReceipt as formReceipt')
  return { gate, profile, recovery }
}
