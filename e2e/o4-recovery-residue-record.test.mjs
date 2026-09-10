import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import {
  chmod, link, lstat, mkdir, mkdtemp, readFile, readdir, realpath, rm, symlink, unlink, writeFile,
} from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import test from 'node:test'

import {
  canonicalJSONStringify,
  finalizeEvidencePhaseE as finalizeEvidencePhaseERaw,
} from './o4-candidate-install-orchestrator.mjs'
import { RECOVERY_RECORD_SCHEMA as INSTALLED_GATE_RECOVERY_SCHEMA } from './o4-installed-evidence-gate.mjs'
import {
  RECOVERY_RESIDUE_GENERATION_SCHEMA,
  RECOVERY_RESIDUE_MANIFEST_SCHEMA,
  RECOVERY_RESIDUE_PHASE_D_RECORD_SCHEMA,
  RECOVERY_RESIDUE_RECEIPT_NAMES,
  RECOVERY_RESIDUE_RECEIPT_SCHEMA,
  RECOVERY_RESIDUE_RECORD_SCHEMA,
  RECOVERY_RESIDUE_RESOURCE_NAMES,
  RECOVERY_RESIDUE_RESOURCE_SCHEMA,
  RECOVERY_RESIDUE_SCENARIOS,
  RECOVERY_RESIDUE_SCREENSHOT_NAMES,
  RECOVERY_RESIDUE_SERVICE_CONTROL_SCHEMA,
  SCREENSHOT_METADATA_SCHEMA,
  SCREENSHOT_OCR_SCHEMA,
  assertDisclosureSourceManifest,
  assertRecoveryResidueManifest,
  assertRecoveryResiduePhaseDRecord,
  assertRecoveryResidueRecord,
  assertRecoveryResidueScenarioReceipt,
  buildDisclosureSourceManifest,
  buildFinalRecoveryResidueRecord,
  buildRecoveryResidueManifest,
  buildRecoveryResidueRecord,
  collectRecoveryResidueScenario as collectRecoveryResidueScenarioRaw,
  cleanupFailedRecoveryResiduePublication,
  formRecoveryResidueScenarioReceipt,
  persistDisclosureSourceManifest as persistDisclosureSourceManifestRaw,
  persistRecoveryResidueEvidence as persistRecoveryResidueEvidenceRaw,
  prepareRecoveryResidueEvidencePaths,
  runPinnedDisclosureExporter as runPinnedDisclosureExporterRaw,
  runPinnedServiceController,
  validateServiceControl,
} from './o4-recovery-residue-record.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'

let repositoryCapture
let cleanupRepositoryCapture
test.before(async () => {
  ({ capture: repositoryCapture, cleanup: cleanupRepositoryCapture } =
    await buildTestRepositoryCapture())
})
test.after(async () => { await cleanupRepositoryCapture() })

function finalizeEvidencePhaseE(input) {
  return finalizeEvidencePhaseERaw(input, { repositoryCapture })
}

function persistRecoveryResidueEvidence(input) {
  return persistRecoveryResidueEvidenceRaw({ ...input, repositoryCapture })
}

function collectRecoveryResidueScenario(input) {
  return collectRecoveryResidueScenarioRaw({ ...input, repositoryCapture })
}

function persistDisclosureSourceManifest(value, outputPath) {
  return persistDisclosureSourceManifestRaw(value, outputPath, { repositoryCapture })
}

function runPinnedDisclosureExporter(input) {
  return runPinnedDisclosureExporterRaw({ ...input, repositoryCapture })
}

const D = 'd'.repeat(64)
const E = 'e'.repeat(64)
const F = 'f'.repeat(64)
const EMPTY = sha256('')
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=',
  'base64')
const PATCH = Buffer.from('diff --git a/task.txt b/task.txt\n--- a/task.txt\n+++ b/task.txt\n@@ -0,0 +1 @@\n+applied\n')
const PAYLOAD = Buffer.from('{"status":"applied","result":"accepted"}\n')

test('pure receipt and public record contracts reject field/order/ID/state/relation drift', () => {
  const fixture = receiptFixture()
  for (const receipt of fixture.receipts) assertRecoveryResidueScenarioReceipt(receipt)
  const record = publicRecord(fixture.receipts, fixture.sharedImages)
  assert.equal(assertRecoveryResidueRecord(record).digest, record.digest)

  const mutations = [
    ['added field', (value) => { value.extra = true }],
    ['removed field', (value) => { delete value.status }],
    ['reordered scenarios', (value) => { [value.scenarios[0], value.scenarios[1]] = [value.scenarios[1], value.scenarios[0]] }],
    ['duplicate Task', (value) => { value.scenarios[1].taskId = value.scenarios[0].taskId }],
    ['nonzero residue', (value) => { value.scenarios[2].residue.ownedContainers = 1 }],
    ['wrong terminal status', (value) => { value.scenarios[0].terminalStatus = 'succeeded' }],
    ['shared image removed', (value) => { value.sharedImages.pop() }],
    ['shared image profile drift', (value) => { value.sharedImages[0].roles = ['managed_pi_runtime'] }],
  ]
  for (const [name, mutate] of mutations) {
    const value = clone(record)
    mutate(value)
    redigest(value, 'digest')
    assert.throws(() => assertRecoveryResidueRecord(value), undefined, name)
  }

  const failure = clone(fixture.receipts[0])
  failure.terminalReason = 'attempt_timeout'
  redigest(failure, 'receiptDigest')
  assert.throws(() => assertRecoveryResidueScenarioReceipt(failure), /state\/reason/)
  const timeout = clone(fixture.receipts[2])
  timeout.patchState = 'present'
  redigest(timeout, 'receiptDigest')
  assert.throws(() => assertRecoveryResidueScenarioReceipt(timeout), /Patch\/check\/review/)
  const retry = clone(fixture.receipts[3])
  retry.scenarioProof.successorAttemptId = retry.scenarioProof.predecessorAttemptId
  redigest(retry, 'receiptDigest')
  assert.throws(() => assertRecoveryResidueScenarioReceipt(retry), /relation/)
  const apply = clone(fixture.receipts[7])
  apply.scenarioProof.taskOwned = false
  redigest(apply, 'receiptDigest')
  assert.throws(() => assertRecoveryResidueScenarioReceipt(apply), /Task-owned/)
})

test('complete prepare collect build persist appends exact D only after immutable manifest and record', async (t) => {
  const fixture = await diskFixture(t)
  const paths = await prepareCompleteFixture(fixture)
  assert.equal(assertDisclosureSourceManifest(fixture.disclosureSourceManifest).status, 'complete')
  assert.equal(await mode(paths.disclosureSourceDirectory), 0o700)
  const disclosureFiles = [
    paths.executionLogFile, paths.stateDatabaseFile, paths.changePatchFile,
    paths.responsePayloadFile, paths.disclosureSourceManifestFile,
  ]
  for (const file of disclosureFiles) assert.equal(await mode(file), 0o400)
  const disclosureManifestBytes = await readFile(paths.disclosureSourceManifestFile)
  await assert.rejects(persistDisclosureSourceManifest(fixture.disclosureSourceManifest,
    paths.disclosureSourceManifestFile))
  assert.deepEqual(await readFile(paths.disclosureSourceManifestFile), disclosureManifestBytes)
  for (let index = 0; index < fixture.receipts.length; index++) {
    await collectRecoveryResidueScenario({
      receipt: fixture.receipts[index],
      outputPath: join(paths.receiptDirectory, RECOVERY_RESIDUE_RECEIPT_NAMES[index]),
    })
  }
  const manifest = await buildRecoveryResidueManifest({
    paths, ...fixture.identity, sharedImages: fixture.sharedImages,
  })
  assert.equal(manifest.schemaVersion, RECOVERY_RESIDUE_MANIFEST_SCHEMA)
  for (const screenshot of manifest.screenshots) {
    assert.deepEqual(screenshot.ocrInvocation, {
      protocol: 'chora.m1-o4-system-managed-ocr.v2',
      executablePathSha256: sha256(fixture.prepareInput.ocrExecutable),
      executableSha256: fixture.prepareInput.ocrExecutableSha256,
      engineBindingPathSha256: sha256(fixture.prepareInput.ocrEngineBindingFile),
      engineBindingSha256: fixture.prepareInput.ocrEngineBindingSha256,
      sandboxExecutablePathSha256: sha256(fixture.prepareInput.ocrSandboxExecutable),
      sandboxExecutableSha256: fixture.prepareInput.ocrSandboxExecutableSha256,
      sandboxProfilePathSha256: sha256(fixture.prepareInput.ocrSandboxProfileFile),
      sandboxProfileSha256: fixture.prepareInput.ocrSandboxProfileSha256,
      language: 'eng', argvSha256: screenshot.ocrInvocation.argvSha256,
      networkDisabled: true, sandboxEnforced: true, modelBytes: 'opaque_unavailable',
    })
    assert.match(screenshot.ocrInvocation.argvSha256, /^[0-9a-f]{64}$/)
  }
  const networkDrift = clone(manifest)
  networkDrift.screenshots[0].ocrInvocation.networkDisabled = false
  redigest(networkDrift, 'aggregateDigest')
  assert.throws(() => assertRecoveryResidueManifest(networkDrift), /protocol\/language\/network/)
  const disclosureOrderDrift = clone(manifest)
  ;[disclosureOrderDrift.disclosureSources.artifacts[0], disclosureOrderDrift.disclosureSources.artifacts[1]] =
    [disclosureOrderDrift.disclosureSources.artifacts[1], disclosureOrderDrift.disclosureSources.artifacts[0]]
  redigest(disclosureOrderDrift, 'aggregateDigest')
  assert.throws(() => assertRecoveryResidueManifest(disclosureOrderDrift), /artifact index/)
  await assert.rejects(buildRecoveryResidueRecord({
    manifest, receiptDirectory: paths.receiptDirectory, imageReuseDigest: E,
  }), /build input fields drifted/)
  const record = await buildRecoveryResidueRecord({
    manifest, receiptDirectory: paths.receiptDirectory,
  })
  assert.equal(record.schemaVersion, RECOVERY_RESIDUE_PHASE_D_RECORD_SCHEMA)
  assert.notEqual(record.schemaVersion, INSTALLED_GATE_RECOVERY_SCHEMA)
  assert.equal(record.imageReuseFormed, false)
  assert.equal(record.finalRecoveryFormed, false)
  assert.equal(assertRecoveryResiduePhaseDRecord(record).recordDigest, record.recordDigest)
  for (const mutate of [
    (value) => { value.imageReuseDigest = E },
    (value) => { delete value.finalRecoveryFormed },
    (value) => { value.imageReuseFormed = true },
  ]) {
    const drift = clone(record)
    mutate(drift)
    redigest(drift, 'recordDigest')
    assert.throws(() => assertRecoveryResiduePhaseDRecord(drift))
  }
  assert.throws(() => assertRecoveryResidueRecord(record), /final recovery\/residue record/,
    'phase-D record must not be accepted as the final installed-gate record')
  const finalRecord = buildFinalRecoveryResidueRecord({
    manifest, phaseDRecord: record, receipts: fixture.receipts,
    reuse: { environmentId: record.environmentId, installId: record.installId,
      generationId: record.generationId, bindingDigest: record.bindingDigest, digest: E },
  })
  assert.equal(assertRecoveryResidueRecord(finalRecord).imageReuseDigest, E)
  assert.equal(finalRecord.schemaVersion, INSTALLED_GATE_RECOVERY_SCHEMA)
  assert.throws(() => buildFinalRecoveryResidueRecord({
    manifest, phaseDRecord: record, receipts: fixture.receipts,
    reuse: { environmentId: `env_${'x'.repeat(32)}`, installId: record.installId,
      generationId: record.generationId, bindingDigest: record.bindingDigest, digest: E },
  }), /image reuse/)
  const splicedReceipts = clone(fixture.receipts)
  splicedReceipts[0].publicObservationDigest = F
  redigest(splicedReceipts[0], 'receiptDigest')
  assert.throws(() => buildFinalRecoveryResidueRecord({
    manifest, phaseDRecord: record, receipts: splicedReceipts,
    reuse: { environmentId: record.environmentId, installId: record.installId,
      generationId: record.generationId, bindingDigest: record.bindingDigest, digest: E },
  }), /spliced from phase D/)
  await assert.rejects(persistRecoveryResidueEvidence({
    manifest, record: finalRecord, paths, manifestFile: paths.manifestFile, recordFile: paths.recordFile,
    tupleFile: fixture.tupleFile, handoffReceiptDirectory: fixture.handoffReceiptDirectory,
  }), /phase-D recovery\/residue record/)
  const result = await persistRecoveryResidueEvidence({
    manifest, record, paths, manifestFile: paths.manifestFile, recordFile: paths.recordFile,
    tupleFile: fixture.tupleFile, handoffReceiptDirectory: fixture.handoffReceiptDirectory,
  })
  assert.equal(result.recordDigest, record.recordDigest)
  assert.equal(result.receipt.phase, 'D')
  assert.equal(result.receipt.operationDigest, sha256(await readFile(paths.manifestFile)))
  assert.equal(result.receipt.observationDigest, record.recordDigest)
  assert.equal((await mode(paths.manifestFile)), 0o400)
  assert.equal((await mode(paths.recordFile)), 0o400)
  assert.deepEqual((await readdir(fixture.handoffReceiptDirectory)).filter((name) => name.endsWith('-D.json')), ['08-D.json'])
  const committed = {
    manifest: await readFile(paths.manifestFile),
    record: await readFile(paths.recordFile),
    receipt: await readFile(join(fixture.handoffReceiptDirectory, '08-D.json')),
  }
  await assert.rejects(persistRecoveryResidueEvidence({
    manifest, record, paths, manifestFile: paths.manifestFile, recordFile: paths.recordFile,
    tupleFile: fixture.tupleFile, handoffReceiptDirectory: fixture.handoffReceiptDirectory,
  }), /phase D already exists|exact phase C|transition/)
  await assert.rejects(cleanupFailedRecoveryResiduePublication({
    manifestFile: paths.manifestFile, recordFile: paths.recordFile,
    receiptDirectory: fixture.handoffReceiptDirectory,
  }), /found a D receipt/)
  assert.deepEqual(await readFile(paths.manifestFile), committed.manifest,
    'committed manifest must remain immutable after duplicate publication/cleanup')
  assert.deepEqual(await readFile(paths.recordFile), committed.record,
    'committed record must remain immutable after duplicate publication/cleanup')
  assert.deepEqual(await readFile(join(fixture.handoffReceiptDirectory, '08-D.json')), committed.receipt,
    'committed D receipt must never be unlinked or rewritten')

  const tuple = JSON.parse(await readFile(fixture.tupleFile, 'utf8'))
  const sealedA3 = { sealed: true, sha256: sha256('recovery-final-a3'), tupleIdentity: tuple.identity,
    environmentId: tuple.environmentId, installId: tuple.installId, generationId: tuple.generationId }
  const composite = { schemaVersion: 'chora.m1-o4-b1-composite.v1', tupleIdentity: tuple.identity,
    environmentId: tuple.environmentId, installId: tuple.installId, generationId: tuple.generationId,
    sourceA3LedgerSha256: sealedA3.sha256, projection: sha256('recovery-final-projection') }
  const [finalization, concurrentCleanup] = await Promise.allSettled([
    finalizeEvidencePhaseE({
      tupleFile: fixture.tupleFile, receiptDirectory: fixture.handoffReceiptDirectory,
      closeAllServices: async () => {}, reopenAndSealA3: async () => sealedA3,
      formCompositeB1: async () => composite,
      validateEvidence: async () => ({ status: 'passed', tupleIdentity: tuple.identity,
        environmentId: tuple.environmentId, installId: tuple.installId, generationId: tuple.generationId,
        a3Sha256: sealedA3.sha256, b1Sha256: sha256(canonicalJSONStringify(composite)) }),
    }),
    cleanupFailedRecoveryResiduePublication({
      manifestFile: paths.manifestFile, recordFile: paths.recordFile,
      receiptDirectory: fixture.handoffReceiptDirectory,
    }),
  ])
  assert.equal(finalization.status, 'fulfilled', 'concurrent E finalization must preserve and extend D')
  assert.equal(concurrentCleanup.status, 'rejected', 'cleanup must fail closed once D is committed')
  assert.deepEqual(await readFile(paths.manifestFile), committed.manifest)
  assert.deepEqual(await readFile(paths.recordFile), committed.record)
  assert.deepEqual(await readFile(join(fixture.handoffReceiptDirectory, '08-D.json')), committed.receipt)
  assert.deepEqual((await readdir(fixture.handoffReceiptDirectory)).filter((name) => /-[DE]\.json$/.test(name)),
    ['08-D.json', '09-E.json'])
  assert.throws(() => assertRecoveryResidueManifest({ ...manifest, aggregateDigest: D }), /digest drifted/)
})

test('missing/non-C prefix, append failure, D skip, and duplicate publication leave no partial outputs', async (t) => {
  const missing = await diskFixture(t, 'missing-c')
  const missingPaths = await prepareCompleteFixture(missing)
  for (let index = 0; index < missing.receipts.length; index++) {
    await collectRecoveryResidueScenario({ receipt: missing.receipts[index],
      outputPath: join(missingPaths.receiptDirectory, RECOVERY_RESIDUE_RECEIPT_NAMES[index]) })
  }
  const manifest = await buildRecoveryResidueManifest({ paths: missingPaths, ...missing.identity, sharedImages: missing.sharedImages })
  const record = await buildRecoveryResidueRecord({ manifest, receiptDirectory: missingPaths.receiptDirectory })
  await rm(join(missing.handoffReceiptDirectory, '07-C.json'))
  await assert.rejects(persistRecoveryResidueEvidence({
    manifest, record, paths: missingPaths, manifestFile: missingPaths.manifestFile, recordFile: missingPaths.recordFile,
    tupleFile: missing.tupleFile, handoffReceiptDirectory: missing.handoffReceiptDirectory,
  }))
  await assertAbsent(missingPaths.manifestFile)
  await assertAbsent(missingPaths.recordFile)
  assert.equal((await readdir(missing.handoffReceiptDirectory)).some((name) => name === '08-D.json'), false)

  const blocked = await diskFixture(t, 'append-failure')
  const blockedPaths = await prepareCompleteFixture(blocked)
  for (let index = 0; index < blocked.receipts.length; index++) {
    await collectRecoveryResidueScenario({ receipt: blocked.receipts[index],
      outputPath: join(blockedPaths.receiptDirectory, RECOVERY_RESIDUE_RECEIPT_NAMES[index]) })
  }
  const blockedManifest = await buildRecoveryResidueManifest({ paths: blockedPaths, ...blocked.identity, sharedImages: blocked.sharedImages })
  const blockedRecord = await buildRecoveryResidueRecord({ manifest: blockedManifest,
    receiptDirectory: blockedPaths.receiptDirectory })
  await chmod(blocked.handoffReceiptDirectory, 0o500)
  await assert.rejects(persistRecoveryResidueEvidence({
    manifest: blockedManifest, record: blockedRecord, paths: blockedPaths, manifestFile: blockedPaths.manifestFile,
    recordFile: blockedPaths.recordFile, tupleFile: blocked.tupleFile,
    handoffReceiptDirectory: blocked.handoffReceiptDirectory,
  }))
  await chmod(blocked.handoffReceiptDirectory, 0o700)
  await assertAbsent(blockedPaths.manifestFile)
  await assertAbsent(blockedPaths.recordFile)
  assert.equal((await readdir(blocked.handoffReceiptDirectory)).some((name) => name === '08-D.json'), false)
})

test('manifest semantic validators reject authority/range/resource/controller/OCR/reference drift', async (t) => {
  const cases = [
    ['authority splice', async (fixture) => mutateJSON(fixture.prepareInput.authorityFile,
      (value) => { value.scenarios[0].taskId = taskId(91) })],
    ['consumption splice', async (fixture) => mutateJSON(join(fixture.prepareInput.authorityConsumptionDirectory, 'failure.json'),
      (value) => { value.runId = runId(91); redigest(value, 'consumptionSha256', false) })],
    ['restart Setup replay', async (fixture) => mutateJSON(fixture.prepareInput.authorizedRestartReceiptFile,
      (value) => { value.setupReplayed = true; redigest(value, 'receiptDigest') })],
    ['A3 overlap', async (fixture) => {
      fixture.receipts[1] = mutateReceipt(fixture.receipts[1], (value) => { value.sourceA3.startSequence = 1 })
    }],
    ['Verifier chain drift', async (fixture) => {
      fixture.receipts[4] = mutateReceipt(fixture.receipts[4], (value) => { value.verifierSource.startSequence = 1 })
    }],
    ['resource residue', async (fixture) => mutateJSON(join(fixture.prepareInput.supervisorResourceDirectory,
      RECOVERY_RESIDUE_RESOURCE_NAMES[3]), (value) => { value.terminalResidue.ownedVolumes = 1; redigest(value, 'ledgerDigest') })],
    ['generation reference', async (fixture) => mutateJSON(join(fixture.prepareInput.generationReferenceDirectory,
      '05-restart-generation.json'), (value) => { value.activeAttemptCount = 1; redigest(value, 'referenceDigest') })],
    ['controller old/new splice', async (fixture) => mutateJSON(fixture.prepareInput.serviceControlFile,
      (value) => { value.newService.pid = value.oldService.pid; redigest(value, 'digest') })],
    ['orphan restart receipt splice', async (fixture) => mutateJSON(fixture.prepareInput.serviceControlFile,
      (value) => { value.restartReceipt.rawSha256 = F; redigest(value, 'digest') })],
    ['OCR language/output drift', async (fixture) => mutateJSON(join(fixture.prepareInput.screenshotOCRDirectory,
      '03-timeout-ocr.json'), (value) => { value.recognizedText = 'wrong visible scenario' })],
  ]
  for (const [name, mutate] of cases) {
    await t.test(name, async (t) => {
      const fixture = await diskFixture(t, name.replace(/[^A-Za-z0-9_-]+/g, '-'))
      await mutate(fixture)
      const paths = await prepareCompleteFixture(fixture)
      for (let index = 0; index < fixture.receipts.length; index++) {
        await collectRecoveryResidueScenario({ receipt: fixture.receipts[index],
          outputPath: join(paths.receiptDirectory, RECOVERY_RESIDUE_RECEIPT_NAMES[index]) })
      }
      await assert.rejects(buildRecoveryResidueManifest({ paths, ...fixture.identity, sharedImages: fixture.sharedImages }))
      await assertAbsent(paths.manifestFile)
      await assertAbsent(paths.recordFile)
      assert.equal((await readdir(fixture.handoffReceiptDirectory)).some((entry) => entry === '08-D.json'), false)
    })
  }
})

test('recovery manifest exact-rejects rehashed stale v1 and repository/roots/bindings/plan tuple drift', async (t) => {
  for (const [name, mutate, message] of [
    ['stale v1', (value) => { value.schemaVersion = 'chora.m1-o4-candidate-install-tuple.v1' },
      /schema\/status drifted/],
    ['repository closure', (value) => {
      value.repositoryAuthority.repositoryClosureSha256 = sha256('spliced-repository')
      value.bindings.repositoryClosureSha256 = value.repositoryAuthority.repositoryClosureSha256
    }, /repository authority snapshot digest drifted/],
    ['partial roots', (value) => { delete value.roots.stateRootSha256 }, /roots fields drifted/],
    ['partial bindings', (value) => { delete value.bindings.roleTupleSha256 }, /bindings fields drifted/],
    ['reordered plan', (value) => {
      [value.plan[0], value.plan[1]] = [value.plan[1], value.plan[0]]
    }, /plan\/order drifted/],
  ]) await t.test(name, async (t) => {
    const fixture = await diskFixture(t, `tuple-${name.replaceAll(' ', '-')}`)
    const paths = await prepareCompleteFixture(fixture)
    for (let index = 0; index < fixture.receipts.length; index++) {
      await collectRecoveryResidueScenario({ receipt: fixture.receipts[index],
        outputPath: join(paths.receiptDirectory, RECOVERY_RESIDUE_RECEIPT_NAMES[index]) })
    }
    await mutateJSON(fixture.tupleFile, (value) => {
      mutate(value)
      delete value.identity
      value.identity = sha256(canonicalJSONStringify(value))
    })
    await assert.rejects(buildRecoveryResidueManifest({
      paths, ...fixture.identity, sharedImages: fixture.sharedImages,
    }), message)
    await assertAbsent(paths.manifestFile)
  })
})

test('disclosure raw builder rejects missing/extra/mode/link/content/SQLite/inode/exporter acknowledgement drift', async (t) => {
  const cases = [
    ['missing artifact', async (paths) => unlink(paths.executionLogFile), /entries drifted/],
    ['extra artifact', async (paths) => write0400(join(paths.disclosureSourceDirectory, 'extra.bin'), Buffer.from('extra'), true, true), /entries drifted/],
    ['mode drift', async (paths) => chmod(paths.executionLogFile, 0o600), /mode drifted/],
    ['symlink artifact', async (paths) => { await unlink(paths.changePatchFile); await symlink(paths.responsePayloadFile, paths.changePatchFile) }, /linked|symlink/],
    ['hardlink artifact', async (paths) => { await unlink(paths.executionLogFile); await link(paths.stateDatabaseFile, paths.executionLogFile) }, /non-linked/],
    ['SQLite header drift', async (paths) => {
      const bytes = Buffer.from('not-a-sqlite-online-backup')
      await replace0400(paths.stateDatabaseFile, bytes)
      await rewriteDisclosureAckArtifact(paths, 1, bytes, true)
    }, /SQLite online backup/],
    ['patch bytes drift', async (paths) => {
      const bytes = Buffer.from('different accepted patch bytes')
      await replace0400(paths.changePatchFile, bytes)
      await rewriteDisclosureAckArtifact(paths, 2, bytes)
    }, /patch digest binding drifted/],
    ['payload bytes drift', async (paths) => {
      const bytes = Buffer.from('{"status":"different"}\n')
      await replace0400(paths.responsePayloadFile, bytes)
      await rewriteDisclosureAckArtifact(paths, 3, bytes)
    }, /payload digest binding drifted/],
    ['backup inode drift', async (paths) => mutateJSON(paths.disclosureExporterAckFile, (value) => {
      value.databaseSnapshot.backupIdentitySha256 = F
      redigest(value, 'ackDigest')
    }), /backup inode identity/],
    ['ack request splice', async (paths) => mutateJSON(paths.disclosureExporterAckFile, (value) => {
      value.requestSha256 = F
      redigest(value, 'ackDigest')
    }), /authority\/pin/],
  ]
  for (const [name, mutate, pattern] of cases) {
    await t.test(name, async (t) => {
      const fixture = await diskFixture(t, `disclosure-raw-${name.replace(/[^a-z]+/gi, '-')}`)
      const { paths, applyBinding } = await prepareExporterFixture(fixture)
      await mutate(paths)
      await assert.rejects(buildDisclosureSourceManifest({
        paths, identity: fixture.identity, applyBinding,
      }), pattern)
      await assertAbsent(paths.disclosureSourceManifestFile)
      await assertAbsent(paths.manifestFile)
      await assertAbsent(paths.recordFile)
      assert.equal((await readdir(fixture.handoffReceiptDirectory)).includes('08-D.json'), false)
    })
  }
})

test('disclosure source manifest rejects field/order/source/exporter/Apply/screenshot splices before D', async (t) => {
  const cases = [
    ['extra field', (value) => { value.extra = true }],
    ['artifact order', (value) => { [value.artifacts[0], value.artifacts[1]] = [value.artifacts[1], value.artifacts[0]] }],
    ['source kind', (value) => { value.artifacts[0].sourceKind = 'harness_generated_log' }],
    ['exporter protocol', (value) => { value.exporter.protocol = 'untrusted_exporter_v0' }],
    ['exporter network', (value) => { value.exporter.networkDisabled = false }],
    ['Apply identity', (value) => { value.applyBinding.attemptId = attemptId(93) }],
    ['screenshot raw digest', (value) => { value.screenshot.png.rawSha256 = F }],
  ]
  for (const [name, mutate] of cases) {
    await t.test(name, async (t) => {
      const fixture = await diskFixture(t, `disclosure-manifest-${name.replace(/[^a-z]+/gi, '-')}`)
      const paths = await prepareCompleteFixture(fixture)
      await mutateJSON(paths.disclosureSourceManifestFile, (value) => {
        mutate(value)
        redigest(value, 'manifestDigest')
      })
      const changed = JSON.parse(await readFile(paths.disclosureSourceManifestFile, 'utf8'))
      fixture.receipts[7] = mutateReceipt(fixture.receipts[7], (value) => {
        value.scenarioProof.disclosureSourceManifestDigest = changed.manifestDigest
      })
      await collectAllReceipts(paths, fixture.receipts)
      await assert.rejects(buildRecoveryResidueManifest({
        paths, ...fixture.identity, sharedImages: fixture.sharedImages,
      }))
      await assertAbsent(paths.manifestFile)
      await assertAbsent(paths.recordFile)
      assert.equal((await readdir(fixture.handoffReceiptDirectory)).includes('08-D.json'), false)
    })
  }
})

test('bounded disclosure exporter aborts timeout, oversize, and delayed descendant without late publication', async (t) => {
  for (const modeName of ['timeout', 'oversize', 'descendant']) {
    await t.test(modeName, async (t) => {
      const fixture = await diskFixture(t, `disclosure-executor-${modeName}`)
      const script = join(fixture.root, `hostile-disclosure-${modeName}`)
      await writeDisclosureHostileExecutable(script, modeName)
      const runner = disclosureHostileRunner(script)
      await assert.rejects(prepareExporterFixture(fixture, runner, modeName === 'timeout' ? 100 : 5_000),
        (error) => {
          if (modeName === 'timeout') {
            assert.match(error.message, /disclosure exporter timed out/)
            return true
          }
          if (modeName === 'descendant') {
            assert.match(error.message, /ENOENT/)
            return true
          }
          assert(error instanceof AggregateError)
          assert.equal(error.message, 'disclosure exporter and fail-closed cleanup failed')
          assert.equal(error.errors.length, 2)
          assert.equal(error.cause, error.errors[0])
          assert.match(error.errors[0].message,
            /disclosure source execution\.log is not one bounded owner-only non-linked 0400 file/)
          assert.match(error.errors[1].message,
            /failed disclosure exporter output already exists/)
          return true
        })
      const output = fixture.prepareInput.outputDirectory
      const disclosure = join(output, 'disclosure-sources')
      if (modeName === 'oversize') {
        assert.deepEqual(await readdir(disclosure), ['execution.log'])
        const residue = await lstat(join(disclosure, 'execution.log'))
        assert(residue.isFile() && !residue.isSymbolicLink() && residue.nlink === 1 &&
          residue.uid === process.getuid() && (residue.mode & 0o777) === 0o400 &&
          residue.size === 17 * 1024 * 1024,
        'fail-closed cleanup must preserve the exact unowned oversize output')
      } else assert.deepEqual(await readdir(disclosure), [])
      for (const file of [
        'disclosure-export-request.json', 'disclosure-export-ack.json', 'manifest.json', 'record.json',
      ]) await assertAbsent(join(output, file))
      assert.equal((await readdir(fixture.handoffReceiptDirectory)).includes('08-D.json'), false)
      await new Promise((resolvePromise) => setTimeout(resolvePromise, 450))
      assert.deepEqual(await readdir(disclosure), modeName === 'oversize' ? ['execution.log'] : [],
        'hostile descendant changed the fail-closed disclosure residue')
    })
  }
})

test('pinned tool path, executable, engine, sandbox, and language drift fail before publication', async (t) => {
  for (const [name, mutate] of [
    ['B2 Runner module SHA', (input) => { input.b2RunnerModuleSha256 = F }],
    ['controller SHA', (input) => { input.serviceControllerSha256 = F }],
    ['OCR executable SHA', (input) => { input.ocrExecutableSha256 = F }],
    ['OCR engine SHA', (input) => { input.ocrEngineBindingSha256 = F }],
    ['OCR sandbox profile SHA', (input) => { input.ocrSandboxProfileSha256 = F }],
    ['OCR language', (input) => { input.ocrLanguage = 'deu' }],
  ]) {
    await t.test(name, async (t) => {
      const fixture = await diskFixture(t, `pin-${name.replaceAll(' ', '-')}`)
      const input = { ...fixture.prepareInput }
      mutate(input)
      await assert.rejects(prepareRecoveryResidueEvidencePaths(input))
      await assertAbsent(input.outputDirectory)
      assert.equal((await readdir(fixture.handoffReceiptDirectory)).includes('08-D.json'), false)
    })
  }
})

test('real spec/config statically lock marker, exact call keys, D parameter, and controller-causal restart', async () => {
  const spec = await readFile(new URL('./o4-recovery-residue-real.spec.ts', import.meta.url), 'utf8')
  const recordModule = await readFile(new URL('./o4-recovery-residue-record.mjs', import.meta.url), 'utf8')
  const config = await readFile(new URL('../playwright.o4-recovery-residue.config.ts', import.meta.url), 'utf8')
  assert.equal((spec.match(/\btest\('/g) ?? []).length, 1, 'real spec must register exactly one test')
  assert.match(spec, /process\.env\.CHORA_O4_RECOVERY_RESIDUE_REAL === '1'/)
  assert.match(config, /process\.env\.CHORA_O4_RECOVERY_RESIDUE_REAL = '1'/)
  assert.match(config, /workers:\s*1/)
  assert.match(config, /retries:\s*0/)
  assert.match(config, /fullyParallel:\s*false/)
  assert.match(spec, /orphanRestartReceiptFile:\s*env\.orphanRestartReceiptFile/)
  assert.match(spec, /handoffReceiptDirectory:\s*env\.handoffReceiptDirectory/)
  assert.match(spec, /manifest,\s*record,\s*paths,\s*manifestFile:/)
  assert.doesNotMatch(spec, /CHORA_O4_RECOVERY_RESIDUE_IMAGE_REUSE_DIGEST|imageReuseDigest/)
  assert.match(config, /CHORA_O4_RECOVERY_RESIDUE_IMAGE_REUSE_DIGEST is forbidden before phase E/)
  assert.doesNotMatch(config, /'CHORA_O4_RECOVERY_RESIDUE_IMAGE_REUSE_DIGEST',/)
  const forbiddenFuture = spawnSync(process.execPath, ['--input-type=module', '-e',
    `process.env.CHORA_O4_RECOVERY_RESIDUE_BASE_URL='http://127.0.0.1:4173/';` +
    `process.env.CHORA_O4_RECOVERY_RESIDUE_IMAGE_REUSE_DIGEST='${D}';` +
    `await import(${JSON.stringify(new URL('../playwright.o4-recovery-residue.config.ts', import.meta.url).href)})`],
  { cwd: process.cwd(), encoding: 'utf8' })
  assert.notEqual(forbiddenFuture.status, 0, 'future image reuse digest environment variable must be rejected')
  assert.match(forbiddenFuture.stderr, /IMAGE_REUSE_DIGEST is forbidden before phase E/)
  assert.match(spec, /responseBody\s*=\s*await responses\[1\]\.body\(\)/)
  assert.match(spec, /runPinnedDisclosureExporter/)
  assert.match(spec, /buildDisclosureSourceManifest/)
  assert.match(spec, /persistDisclosureSourceManifest/)
  assert.match(spec,
    /runPinnedDisclosureExporter\(\{[\s\S]*?sources:[\s\S]*?repositoryCapture,\s*\}\)/,
  'real disclosure exporter call must carry the owned-path capability')
  assert.match(spec,
    /persistDisclosureSourceManifest\(disclosureSourceManifest,\s*paths\.disclosureSourceManifestFile,\s*\{ repositoryCapture \}\)/,
  'real disclosure manifest publication must carry the owned-path capability')
  assert.match(spec,
    /collectRecoveryResidueScenario\(\{[\s\S]*?RECOVERY_RESIDUE_RECEIPT_NAMES[\s\S]*?repositoryCapture,\s*\}\)/,
  'real scenario publication must carry the owned-path capability')
  assert.match(spec,
    /persistRecoveryResidueEvidence\(\{[\s\S]*?handoffReceiptDirectory:[\s\S]*?repositoryCapture,\s*\}\)/,
  'real phase-D publication must carry the owned-path capability')
  assert.match(spec, /exportApplyDisclosureSources/)
  assert.match(spec, /abortApplyDisclosureExporter/)
  assert.match(spec, /observationDigest:\s*record\.recordDigest/)
  const request = spec.indexOf('await writeControllerRequest(env.serviceControllerRequestFile')
  const arm = spec.indexOf('await runner.armAuthenticatedControllerRecovery')
  const restart = spec.indexOf('const orphanRestart = await candidateModule.restartCandidate', arm)
  const wait = spec.indexOf('await runner.awaitAuthenticatedControllerProof', restart)
  const proof = spec.indexOf('const serviceControl = await recordModule.readPinnedServiceControlProof', wait)
  const recovery = spec.indexOf('const recoveredOrphan = await recoverOrphanThroughPublicUI', proof)
  assert(request > 0 && request < arm && arm < restart && restart < wait && wait < proof && proof < recovery,
    'orphan controller request/arm/restart/wait/read/recovery causal order drifted')
  assert.doesNotMatch(spec, /runPinnedServiceController/,
    'real orphan flow must not execute the pinned controller a second time')
  assert.doesNotMatch(recordModule, /removeMatchingFailedDReceipt/,
    'D receipt rollback helper must not exist after the immutable commit point')
  assert.doesNotMatch(recordModule, /unlink\([^\n]*08-D\.json/,
    'D receipt must never be unlinked')
  const append = recordModule.indexOf('const receipt = await appendHandoffReceipt')
  const immediateReturn = recordModule.indexOf('return Object.freeze({ manifestSha256, recordDigest, receipt })', append)
  const nextExport = recordModule.indexOf('\nexport ', append)
  assert(append > 0 && immediateReturn > append && nextExport > immediateReturn &&
    !recordModule.slice(append, immediateReturn).includes('assert('),
  'D append must be the final fallible commit action followed by an immediate return')
  assert.match(spec, /sequence:\s*3,\s*status:\s*'passed',\s*setupReplayed:\s*false/)
  assert.match(config, /CHORA_O4_RECOVERY_RESIDUE_ORPHAN_RESTART_RECEIPT_FILE/)
  assert.doesNotMatch(config, /CHORA_O4_RECOVERY_RESIDUE_OCR_MODEL_(?:FILE|SHA256)/)
  for (const name of [
    'CHORA_O4_RECOVERY_RESIDUE_OCR_ENGINE_BINDING_FILE',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_ENGINE_BINDING_SHA256',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_EXECUTABLE_FILE',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_EXECUTABLE_SHA256',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_PROFILE_FILE',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_PROFILE_SHA256',
    'DOCKER_CONFIG', 'DOCKER_CONTEXT', 'PLAYWRIGHT_BROWSERS_PATH', 'DOCKER_HOST',
  ]) assert.match(config, new RegExp(name), `dedicated D config lacks ${name}`)
  assert.match(config, /exactAbsolute\('DOCKER_CONFIG'\)/)
  assert.match(config, /exactAbsolute\('PLAYWRIGHT_BROWSERS_PATH'\)/)
  assert.match(config, /DOCKER_HOST is forbidden in the dedicated O4-D harness/)
})

test('path safety rejects symlink, hardlink, unsafe mode, overlap, and overwrite', async (t) => {
  await t.test('symlink', async (t) => {
    const fixture = await diskFixture(t, 'symlink')
    const alias = join(fixture.root, 'tuple-link.json')
    await symlink(fixture.tupleFile, alias)
    await assert.rejects(prepareRecoveryResidueEvidencePaths({ ...fixture.prepareInput, tupleFile: alias }), /symlink/)
  })
  await t.test('hardlink', async (t) => {
    const fixture = await diskFixture(t, 'hardlink')
    const alias = join(fixture.root, 'tuple-hard.json')
    await link(fixture.tupleFile, alias)
    await assert.rejects(prepareRecoveryResidueEvidencePaths({ ...fixture.prepareInput,
      publicResidueFile: alias }), /non-linked/)
  })
  await t.test('mode', async (t) => {
    const fixture = await diskFixture(t, 'mode')
    await chmod(fixture.prepareInput.screenshotDirectory, 0o755)
    await assert.rejects(prepareRecoveryResidueEvidencePaths(fixture.prepareInput), /0700/)
  })
  await t.test('overlap', async (t) => {
    const fixture = await diskFixture(t, 'overlap')
    await assert.rejects(prepareRecoveryResidueEvidencePaths({ ...fixture.prepareInput,
      outputDirectory: join(fixture.prepareInput.screenshotDirectory, 'out') }), /overlap/)
  })
  await t.test('overwrite', async (t) => {
    const fixture = await diskFixture(t, 'overwrite')
    await mkdir(fixture.prepareInput.outputDirectory, { mode: 0o700 })
    await assert.rejects(prepareRecoveryResidueEvidencePaths(fixture.prepareInput), /already exists/)
  })
})

test('service-control contract rejects pin/protocol/network/model-style fixture claims', () => {
  const fixture = receiptFixture()
  const valid = serviceControl(fixture.identity, fixture.restartReceipt, fixture.receipts[5])
  assert.equal(validateServiceControl(valid).status, 'passed')
  const mutations = [
    (value) => { value.protocol = 'changed' },
    (value) => { value.networkDisabled = false },
    (value) => { value.newService.generationId = `gen_${'x'.repeat(24)}` },
    (value) => { value.fixture = true },
  ]
  for (const mutate of mutations) {
    const value = clone(valid)
    mutate(value)
    if (!('fixture' in value)) redigest(value, 'digest')
    assert.throws(() => validateServiceControl(value))
  }
})

test('pinned controller kills timeout, oversize, and delayed-output descendant process groups', async (t) => {
  const canonicalTmp = await realpath(tmpdir())
  const root = await mkdtemp(join(canonicalTmp, 'chora-o4-d-hostile-tools-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  await chmod(root, 0o700)
  const requestFile = join(root, 'request.json')
  await write0400(requestFile, { schemaVersion: 'test-only-hostile-request.v1' })

  for (const behavior of ['timeout', 'oversize', 'descendant']) {
    await t.test(behavior, async () => {
      const executable = join(root, `controller-${behavior}`)
      const outputFile = join(root, `controller-${behavior}-output.json`)
      await writeHostileExecutable(executable, behavior)
      const executableSha256 = sha256(await readFile(executable))
      const timeoutMs = behavior === 'timeout' ? 2_000 : 10_000
      await assert.rejects(runPinnedServiceController({
        executableFile: executable, executableSha256, requestFile, outputFile, timeoutMs,
      }), (error) => {
        if (behavior === 'timeout') {
          assert(error instanceof AggregateError)
          assert.equal(error.message,
            'service controller failed and did not prove atomic output cleanup')
          assert.equal(error.errors.length, 2)
          assert.equal(error.cause, error.errors[0])
          assert.equal(error.errors[0].message, 'service controller timed out')
          assert.equal(error.errors[1].message,
            'failed service controller output already exists')
          return true
        }
        assert.match(error.message,
          behavior === 'oversize' ? /bounded protocol/ : /descendant process/)
        return true
      })
      if (behavior === 'timeout') {
        assert.equal(await readFile(outputFile, 'utf8'), '{"partial":true}\n',
          'parent must preserve an unowned partial output instead of deleting by pathname')
      } else await assertAbsent(outputFile)
      // A surviving descendant would recreate the path at 350 ms. Waiting
      // beyond that boundary proves the detached process group was killed.
      await new Promise((resolvePromise) => setTimeout(resolvePromise, 500))
      if (behavior === 'timeout') {
        assert.equal(await readFile(outputFile, 'utf8'), '{"partial":true}\n')
      } else await assertAbsent(outputFile)
    })
  }
})

function receiptFixture() {
  const identity = {
    environmentId: `env_${'a'.repeat(24)}`,
    installId: `ins_${'b'.repeat(24)}`,
    generationId: `gen_${'c'.repeat(24)}`,
    bindingDigest: D,
    tupleIdentity: E,
  }
  const a3Attempts = RECOVERY_RESIDUE_SCENARIOS.map(({ name }) => ({ scenario: name, digest: sha256(`a3-${name}`) }))
  const verifierAttempts = RECOVERY_RESIDUE_SCENARIOS.slice(3).map(({ name }) => ({ scenario: name, digest: sha256(`verifier-${name}`) }))
  const sharedImages = [
    { artifactId: 'managed-pi-runtime', roles: ['managed_pi_runtime', 'independent_verifier', 'capability_probe'],
      archiveSha256: D, archiveSize: 101, dockerConfigImageId: `sha256:${'2'.repeat(64)}`, present: true },
    { artifactId: 'network-boundary', roles: ['network_boundary'], archiveSha256: E,
      archiveSize: 103, dockerConfigImageId: `sha256:${'4'.repeat(64)}`, present: true },
  ]
  const restartSafe = { schemaVersion: 'chora.m1-o4-candidate-restart-receipt.v1', tupleIdentity: E,
    sequence: 1, status: 'passed', operationDigest: D, observationDigest: E,
    previousReceiptDigest: null, setupReplayed: false }
  const restartReceipt = { ...restartSafe, receiptDigest: sha256(canonicalJSONStringify(restartSafe)) }
  const service = serviceControl(identity, restartReceipt)
  const receipts = RECOVERY_RESIDUE_SCENARIOS.map((contract, index) => {
    const a3 = { startSequence: index + 1, endSequence: index + 1,
      sliceDigest: sha256(canonicalJSONStringify([a3Attempts[index]])) }
    const verifierIndex = index - 3
    const verifierSource = verifierIndex < 0 ? null : { startSequence: verifierIndex + 1,
      endSequence: verifierIndex + 1,
      sliceDigest: sha256(canonicalJSONStringify([verifierAttempts[verifierIndex]])) }
    const draft = {
      schemaVersion: RECOVERY_RESIDUE_RECEIPT_SCHEMA, status: 'passed', ...contract, ...identity,
      taskId: taskId(index + 1), runId: runId(index + 1), attemptId: attemptId(index + 1),
      workspaceId: `wsp_${String(index + 1).padStart(24, '0')}`,
      targetId: `tgt_${String(index + 1).padStart(24, '0')}`,
      publicEntry: true, cleanupComplete: true,
      patchState: ['failure', 'cancel', 'timeout'].includes(contract.name) ? 'absent' : contract.name === 'apply' ? 'applied' : 'present',
      checkState: ['failure', 'cancel', 'timeout'].includes(contract.name) ? 'absent' : 'passed',
      reviewState: ['failure', 'cancel', 'timeout'].includes(contract.name) ? 'absent' : contract.name === 'apply' ? 'accepted' : 'awaiting_review',
      sourceA3: a3, verifierSource, residue: zeroResidue(),
      scenarioProof: scenarioProof(contract.name, index + 1, restartReceipt, service),
      publicActionDigest: sha256(`action-${contract.name}`),
      publicObservationDigest: sha256(`observation-${contract.name}`),
      completedAt: new Date(Date.UTC(2026, 7, 28, 0, index)).toISOString(),
    }
    return formRecoveryResidueScenarioReceipt(draft)
  })
  return { identity, a3Attempts, verifierAttempts, sharedImages, restartReceipt, service, receipts }
}

function scenarioProof(name, n, restartReceipt, service) {
  switch (name) {
    case 'failure': return { exitCode: 7, processTreeDead: true, automaticRetryCount: 0 }
    case 'cancel': return { cancelRequestedThroughPublicUI: true, processTreeDead: true, retryAllowed: true }
    case 'timeout': return { deadlineAuthorityConsumed: true, processTreeDead: true, automaticRetryCount: 0 }
    case 'retry': return { predecessorAttemptId: attemptId(40), predecessorState: 'rejected',
      predecessorReason: 'result_rejected', successorAttemptId: attemptId(n),
      successorState: 'output_submitted', uiRetry: true }
    case 'restart': return { idleBeforeRestart: true, graceful: true, setupReplayed: false,
      restartReceiptDigest: restartReceipt.receiptDigest }
    case 'orphan': return { interruptedAttemptId: attemptId(60), interruptedState: 'recovery_required',
      recoveredAttemptId: attemptId(n), recoveredState: 'output_submitted',
      authenticatedServiceControl: true, serviceControlDigest: service.digest }
    case 'reopen': return { rootEntry: '/', roomDirectoryEntry: true, deepLinkUsed: false }
    case 'apply': return { reviewKind: 'accept', verificationState: 'completed',
      applicationState: 'applied', taskOwned: true, targetId: `tgt_${String(n).padStart(24, '0')}`,
      patchDigest: sha256(PATCH), payloadSha256: sha256(PAYLOAD),
      disclosureSourceManifestDigest: D }
  }
}

function publicRecord(receipts, sharedImages) {
  const safe = {
    schemaVersion: RECOVERY_RESIDUE_RECORD_SCHEMA, status: 'passed',
    environmentId: receipts[0].environmentId, installId: receipts[0].installId,
    generationId: receipts[0].generationId, bindingDigest: receipts[0].bindingDigest,
    imageReuseDigest: E,
    scenarios: receipts.map((receipt) => ({ sequence: receipt.sequence, name: receipt.name,
      taskId: receipt.taskId, runId: receipt.runId, attemptId: receipt.attemptId,
      workspaceId: receipt.workspaceId, targetId: receipt.targetId,
      terminalStatus: receipt.terminalStatus, publicEntry: true, cleanupComplete: true,
      residue: zeroResidue() })),
    sharedImages: clone(sharedImages),
  }
  return { ...safe, digest: sha256(canonicalJSONStringify(safe)) }
}

async function prepareCompleteFixture(fixture) {
  const { paths, applyBinding } = await prepareExporterFixture(fixture)
  const sourceManifest = await buildDisclosureSourceManifest({
    paths, identity: fixture.identity, applyBinding,
  })
  await persistDisclosureSourceManifest(sourceManifest, paths.disclosureSourceManifestFile)
  fixture.disclosureSourceManifest = sourceManifest
  fixture.receipts[7] = mutateReceipt(fixture.receipts[7], (value) => {
    value.scenarioProof.patchDigest = applyBinding.patchDigest
    value.scenarioProof.payloadSha256 = applyBinding.payloadSha256
    value.scenarioProof.disclosureSourceManifestDigest = sourceManifest.manifestDigest
  })
  return paths
}

async function prepareExporterFixture(fixture, runnerOverride = undefined, timeoutMs = 1_000) {
  const paths = await prepareRecoveryResidueEvidencePaths(fixture.prepareInput)
  const apply = fixture.receipts[7]
  const applyBinding = {
    taskId: apply.taskId, runId: apply.runId, attemptId: apply.attemptId,
    workspaceId: apply.workspaceId, targetId: apply.targetId,
    publicObservationDigest: apply.publicObservationDigest,
    patchDigest: sha256(PATCH), payloadSha256: sha256(PAYLOAD),
  }
  const sources = {
    executionLogFile: join(fixture.root, 'apply-execution-source.json'),
    stateDatabaseFile: join(fixture.root, 'apply-state-source.db'),
    changePatchFile: join(fixture.root, 'apply-change-source.patch'),
  }
  await write0400(sources.executionLogFile,
    Buffer.from('{"commands":[{"exitCode":0,"stdout":"Apply completed","stderr":""}]}\n'), true, true)
  await write0400(sources.stateDatabaseFile,
    Buffer.concat([Buffer.from('SQLite format 3\0', 'binary'), Buffer.alloc(96, 2)]), true, true)
  await write0400(sources.changePatchFile, PATCH, true, true)
  const runner = runnerOverride ?? {
    async exportApplyDisclosureSources({ requestFile, requestSha256, responseBody }) {
      const request = JSON.parse(await readFile(requestFile, 'utf8'))
      const sqlite = Buffer.concat([Buffer.from('SQLite format 3\0', 'binary'), Buffer.alloc(96, 1)])
      const outputs = [
        [request.outputs.executionLogFile, Buffer.from('stdout: Apply completed\nstderr: \n')],
        [request.outputs.stateDatabaseFile, sqlite],
        [request.outputs.changePatchFile, PATCH],
        [request.outputs.responsePayloadFile, responseBody],
      ]
      for (const [path, bytes] of outputs) await write0400(path, bytes, true, true)
      const artifacts = [
        { name: 'execution.log', kind: 'log', sourceKind: 'apply_attempt_stdout_stderr' },
        { name: 'state.db', kind: 'database', sourceKind: 'sqlite_online_backup' },
        { name: 'change.patch', kind: 'patch', sourceKind: 'accepted_applied_patch' },
        { name: 'payload.json', kind: 'json', sourceKind: 'apply_response_body' },
      ].map((value, index) => ({ ...value, bytes: outputs[index][1].length,
        rawSha256: sha256(outputs[index][1]) }))
      const databaseInfo = await lstat(request.outputs.stateDatabaseFile)
      const safe = {
        schemaVersion: 'chora.m1-o4-disclosure-export-ack.v1', status: 'completed',
        protocol: 'chora.m1-o4-readonly-disclosure-exporter.v1',
        runnerModuleSha256: fixture.prepareInput.b2RunnerModuleSha256,
        requestSha256, networkDisabled: true, applyBinding, artifacts,
        databaseSnapshot: { method: 'sqlite_online_backup',
          sourceIdentitySha256: sha256('live-product-database-inode'),
          backupIdentitySha256: filesystemIdentity(databaseInfo),
          sourceAndBackupInodesDistinct: true, walConsistent: true },
        processGroupDead: true, descendantsDead: true,
        completedAt: '2026-08-28T00:08:30.000Z',
      }
      await write0400(request.outputs.acknowledgementFile,
        { ...safe, ackDigest: sha256(canonicalJSONStringify(safe)) })
      return { status: 'completed', requestSha256 }
    },
    async abortApplyDisclosureExporter({ requestSha256 }) {
      return { status: 'aborted', requestSha256, processGroupDead: true, descendantsDead: true }
    },
  }
  await runPinnedDisclosureExporter({ runner, paths, applyBinding, responseBody: PAYLOAD,
    sources, timeoutMs })
  return { paths, applyBinding }
}

function filesystemIdentity(info) {
  return sha256(canonicalJSONStringify({ device: String(info.dev), inode: String(info.ino) }))
}

async function diskFixture(t, suffix = 'happy') {
  const memory = receiptFixture()
  const canonicalTmp = await realpath(tmpdir())
  const root = await mkdtemp(join(canonicalTmp, `chora-o4-d-${suffix}-`))
  t.after(() => rm(root, { recursive: true, force: true }))
  await chmod(root, 0o700)
  const handoffReceiptDirectory = join(root, 'handoff')
  const authorityConsumptionDirectory = join(root, 'consumptions')
  const supervisorResourceDirectory = join(root, 'resources')
  const generationReferenceDirectory = join(root, 'generations')
  const screenshotDirectory = join(root, 'screenshots')
  const screenshotMetadataDirectory = join(root, 'metadata')
  const screenshotOCRDirectory = join(root, 'ocr')
  for (const directory of [handoffReceiptDirectory, authorityConsumptionDirectory,
    supervisorResourceDirectory, generationReferenceDirectory, screenshotDirectory,
    screenshotMetadataDirectory, screenshotOCRDirectory]) await mkdir(directory, { mode: 0o700 })

  const tupleFile = join(handoffReceiptDirectory, 'tuple.json')
  const repositoryRoot = '/private/tmp/chora-o4-recovery-repository'
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
  const bindings = Object.fromEntries(digestBindingKeys.map((key) => [key, sha256(key)]))
  Object.assign(bindings, { repositoryCommit,
    repositoryClosureSha256: repositoryAuthority.repositoryClosureSha256 })
  const tupleSafe = { schemaVersion: 'chora.m1-o4-candidate-install-tuple.v2', status: 'frozen',
    environmentId: memory.identity.environmentId, installId: memory.identity.installId,
    generationId: memory.identity.generationId, platform: { os: 'darwin', architecture: 'arm64' },
    roots: Object.fromEntries([
      'installRootSha256', 'dataRootSha256', 'stateRootSha256', 'receiptDirectorySha256',
      'privatePiRootSha256', 'probeRuntimeRootSha256', 'colimaToolRootSha256',
    ].map((key) => [key, sha256(key)])),
    repositoryAuthority, bindings,
    plan: ['offline-prebuilt-install', 'product-doctor', 'setup-once', 'installed-doctor',
      'process-boundary', 'active-serve'] }
  const tuple = { ...tupleSafe, identity: sha256(canonicalJSONStringify(tupleSafe)) }
  memory.identity.tupleIdentity = tuple.identity
  for (let index = 0; index < memory.receipts.length; index++) {
    const old = memory.receipts[index]
    memory.receipts[index] = formRecoveryResidueScenarioReceipt({ ...without(old, 'receiptDigest'), tupleIdentity: tuple.identity })
  }
  await write0400(tupleFile, tuple)
  const phases = ['preflight', 'install', 'product_doctor', 'setup', 'doctor', 'serve', 'C']
  let previous = null
  for (let index = 0; index < phases.length; index++) {
    const safe = { schemaVersion: 'chora.m1-o4-candidate-phase-receipt.v1', tupleIdentity: tuple.identity,
      phase: phases[index], sequence: index + 1, status: 'passed', operationDigest: sha256(`op-${phases[index]}`),
      observationDigest: index === 0 ? null : sha256(`obs-${phases[index]}`), previousReceiptDigest: previous }
    const value = { ...safe, receiptDigest: sha256(canonicalJSONStringify(safe)) }
    previous = value.receiptDigest
    await write0400(join(handoffReceiptDirectory, `${String(index + 1).padStart(2, '0')}-${phases[index]}.json`), value)
  }
  const restarts = join(handoffReceiptDirectory, 'restarts')
  await mkdir(restarts, { mode: 0o700 })
  const initialRestartSafe = { ...without(memory.restartReceipt, 'receiptDigest'), tupleIdentity: tuple.identity }
  const initialRestart = { ...initialRestartSafe, receiptDigest: sha256(canonicalJSONStringify(initialRestartSafe)) }
  memory.restartReceipt = initialRestart
  await write0400(join(restarts, 'restart-01.json'), initialRestart)
  const gracefulRestartSafe = { ...without(initialRestart, 'receiptDigest'), sequence: 2,
    previousReceiptDigest: initialRestart.receiptDigest, operationDigest: sha256('graceful-restart'),
    observationDigest: sha256('graceful-ready') }
  const gracefulRestart = { ...gracefulRestartSafe,
    receiptDigest: sha256(canonicalJSONStringify(gracefulRestartSafe)) }
  await write0400(join(restarts, 'restart-02.json'), gracefulRestart)
  const orphanRestartSafe = { ...without(gracefulRestart, 'receiptDigest'), sequence: 3,
    previousReceiptDigest: gracefulRestart.receiptDigest, operationDigest: sha256('orphan-restart'),
    observationDigest: sha256('orphan-ready') }
  const orphanRestart = { ...orphanRestartSafe,
    receiptDigest: sha256(canonicalJSONStringify(orphanRestartSafe)) }
  const orphanRestartReceiptFile = join(restarts, 'restart-03.json')
  await write0400(orphanRestartReceiptFile, orphanRestart)
  memory.receipts[4] = mutateReceipt(memory.receipts[4],
    (value) => { value.scenarioProof.restartReceiptDigest = gracefulRestart.receiptDigest })

  const authorityFile = join(root, 'o4-fault-authority.json')
  const authority = faultAuthority(memory, tuple.identity)
  await write0400(authorityFile, authority, false)
  const authoritySha256 = sha256(await readFile(authorityFile))
  for (const [scenario, receiptIndex, action] of [
    ['failure', 0, 'force_exact_managed_attempt_nonzero_after_started_v1'],
    ['timeout', 2, 'accelerated_managed_attempt_deadline_v1'],
  ]) {
    const receipt = memory.receipts[receiptIndex]
    const safe = { schemaVersion: 'chora.m1-o4-fault-consumption.v1', status: 'consumed',
      authoritySha256, tupleIdentity: tuple.identity, scenario, action, runId: receipt.runId,
      attemptId: receipt.attemptId, invocationSha256: sha256(`invocation-${scenario}`) }
    await write0400(join(authorityConsumptionDirectory, `${scenario}.json`),
      { ...safe, consumptionSha256: sha256(JSON.stringify(safe)) }, false)
  }

  const sourceA3LedgerFile = join(root, 'source-a3.json')
  const verifierLedgerFile = join(root, 'verifier.json')
  await write0400(sourceA3LedgerFile, { schemaVersion: 'chora.m1-o4-sealed-a3-runner-ledger.v1',
    status: 'recording', attempts: memory.a3Attempts })
  await write0400(verifierLedgerFile, { schemaVersion: 'chora.m1-o4-verifier-ledger.v1',
    status: 'recording', attempts: memory.verifierAttempts })

  const serviceControllerExecutable = join(root, 'service-controller')
  const ocrExecutable = join(root, 'offline-ocr')
  const ocrEngineBindingFile = join(root, 'ocr-engine-binding.json')
  const ocrSandboxExecutable = '/usr/bin/sandbox-exec'
  const ocrSandboxProfileFile = join(root, 'ocr-deny-network.sb')
  const b2RunnerModuleFile = join(root, 'pinned-b2-runner.mjs')
  await writeFile(serviceControllerExecutable, Buffer.from('pinned-service-controller-v1'),
    { flag: 'wx', mode: 0o500 })
  await chmod(serviceControllerExecutable, 0o500)
  await writeFile(ocrExecutable, Buffer.from('pinned-system-managed-ocr-v2'),
    { flag: 'wx', mode: 0o500 })
  await chmod(ocrExecutable, 0o500)
  await write0400(ocrSandboxProfileFile,
    Buffer.from('(version 1)\n(allow default)\n(deny network*)\n'), true, true)
  await write0400(b2RunnerModuleFile, Buffer.from('export const pinned = true\n'), true, true)
  const serviceControllerSha256 = sha256(await readFile(serviceControllerExecutable))
  const ocrExecutableSha256 = sha256(await readFile(ocrExecutable))
  const ocrSandboxExecutableSha256 = sha256(await readFile(ocrSandboxExecutable))
  const ocrSandboxProfileSha256 = sha256(await readFile(ocrSandboxProfileFile))
  const b2RunnerModuleSha256 = sha256(await readFile(b2RunnerModuleFile))
  const engineBinding = systemManagedEngineBinding({ ocrExecutable, ocrExecutableSha256,
    ocrSandboxExecutable, ocrSandboxExecutableSha256,
    ocrSandboxProfileFile, ocrSandboxProfileSha256 })
  await write0400(ocrEngineBindingFile, engineBinding)
  const ocrEngineBindingSha256 = sha256(await readFile(ocrEngineBindingFile))

  for (let index = 0; index < memory.receipts.length; index++) {
    const receipt = memory.receipts[index]
    const resourceSafe = { schemaVersion: RECOVERY_RESIDUE_RESOURCE_SCHEMA, status: 'terminal',
      sequence: receipt.sequence, scenario: receipt.name, tupleIdentity: tuple.identity,
      generationId: memory.identity.generationId, taskId: receipt.taskId, runId: receipt.runId,
      attemptId: receipt.attemptId, workspaceId: receipt.workspaceId, terminalResidue: zeroResidue() }
    await write0400(join(supervisorResourceDirectory, RECOVERY_RESIDUE_RESOURCE_NAMES[index]),
      { ...resourceSafe, ledgerDigest: sha256(canonicalJSONStringify(resourceSafe)) })
    const generationSafe = { schemaVersion: RECOVERY_RESIDUE_GENERATION_SCHEMA, status: 'terminal',
      sequence: receipt.sequence, scenario: receipt.name, generationId: memory.identity.generationId,
      tupleIdentity: tuple.identity, servingCount: 1, activeAttemptCount: 0, recoverableAttemptCount: 0,
      currentGenerationSha256: sha256(memory.identity.generationId) }
    await write0400(join(generationReferenceDirectory,
      `${String(index + 1).padStart(2, '0')}-${receipt.name}-generation.json`),
    { ...generationSafe, referenceDigest: sha256(canonicalJSONStringify(generationSafe)) })
    await write0400(join(screenshotDirectory, RECOVERY_RESIDUE_SCREENSHOT_NAMES[index]), PNG, true, true)
    const metadata = { schemaVersion: SCREENSHOT_METADATA_SCHEMA, pngSha256: sha256(PNG),
      completed: true, chunkTypes: ['IHDR', 'IDAT', 'IEND'], unsafeMatchCount: 0 }
    await write0400(join(screenshotMetadataDirectory,
      RECOVERY_RESIDUE_SCREENSHOT_NAMES[index].replace(/\.png$/, '-metadata.json')), metadata)
    const ocr = screenshotOCRRecord(`visible O4 ${receipt.name}`,
      ocrEngineBindingSha256, ocrExecutableSha256)
    await write0400(join(screenshotOCRDirectory,
      RECOVERY_RESIDUE_SCREENSHOT_NAMES[index].replace(/\.png$/, '-ocr.json')), ocr)
  }

  const publicResidueFile = join(root, 'residue.json')
  await write0400(publicResidueFile, publicResidue(tuple.identity, authoritySha256, memory.identity.generationId))
  const orphanRestartIndex = { file: 'restart-03.json',
    rawSha256: sha256(await readFile(orphanRestartReceiptFile)),
    semanticDigest: orphanRestart.receiptDigest }
  const service = serviceControl({ ...memory.identity, tupleIdentity: tuple.identity }, orphanRestartIndex)
  service.controllerSha256 = serviceControllerSha256
  service.controllerPathSha256 = sha256(serviceControllerExecutable)
  redigest(service, 'digest')
  const serviceControlFile = join(root, 'service-control.json')
  await write0400(serviceControlFile, service)
  memory.service = service
  memory.receipts[5] = mutateReceipt(memory.receipts[5],
    (value) => { value.scenarioProof.serviceControlDigest = service.digest })

  const phaseCReceiptFile = join(handoffReceiptDirectory, '07-C.json')
  const authorizedRestartReceiptFile = join(restarts, 'restart-01.json')
  return {
    ...memory, root, tupleFile, handoffReceiptDirectory,
    prepareInput: { tupleFile, phaseCReceiptFile, authorityFile, authorizedRestartReceiptFile,
      orphanRestartReceiptFile,
      sourceA3LedgerFile, verifierLedgerFile, authorityConsumptionDirectory,
      supervisorResourceDirectory, generationReferenceDirectory, publicResidueFile,
      serviceControlFile, screenshotDirectory, screenshotMetadataDirectory,
      screenshotOCRDirectory, serviceControllerExecutable, serviceControllerSha256,
      ocrExecutable, ocrExecutableSha256, ocrEngineBindingFile, ocrEngineBindingSha256,
      ocrSandboxExecutable, ocrSandboxExecutableSha256,
      ocrSandboxProfileFile, ocrSandboxProfileSha256, ocrLanguage: 'eng',
      b2RunnerModuleFile, b2RunnerModuleSha256,
      outputDirectory: join(root, 'phase-d-output') },
  }
}

function faultAuthority(memory, tupleIdentity) {
  return { schemaVersion: 'chora.m1-o4-fault-authority.v1', status: 'authorized', scope: 'o4_acceptance_only',
    tupleIdentity, generationId: memory.identity.generationId, environmentId: memory.identity.environmentId,
    installId: memory.identity.installId, binarySha256: D, sourceAggregateSha256: E,
    dataRootSha256: F, policySha256: sha256('policy'), scenarios: [
      { scenario: 'failure', action: 'force_exact_managed_attempt_nonzero_after_started_v1',
        taskId: memory.receipts[0].taskId, snapshotId: `context_snapshot_${uuid(81)}`,
        snapshotDigest: D, attemptSequence: 1, agentExecutionProfile: 'standard' },
      { scenario: 'timeout', action: 'accelerated_managed_attempt_deadline_v1',
        taskId: memory.receipts[2].taskId, snapshotId: `context_snapshot_${uuid(82)}`,
        snapshotDigest: E, attemptSequence: 1, agentExecutionProfile: 'standard' },
    ], claimBoundary: ['typed_failure_projection', 'typed_timeout_projection', 'process_tree_death',
      'no_patch', 'no_automatic_retry', 'resource_cleanup'] }
}

function serviceControl(identity, restartReceipt) {
  const index = 'file' in restartReceipt ? restartReceipt : {
    file: 'restart-03.json', rawSha256: sha256(JSON.stringify(restartReceipt)),
    semanticDigest: restartReceipt.receiptDigest,
  }
  const oldService = { pid: 4101, startIdentitySha256: sha256('old-start'), binarySha256: D,
    tupleIdentity: identity.tupleIdentity, generationId: identity.generationId,
    readiness: 'terminated', currentGeneration: true }
  const newService = { pid: 4202, startIdentitySha256: sha256('new-start'), binarySha256: D,
    tupleIdentity: identity.tupleIdentity, generationId: identity.generationId,
    readiness: 'ready', currentGeneration: true }
  const safe = { schemaVersion: RECOVERY_RESIDUE_SERVICE_CONTROL_SCHEMA, status: 'passed',
    protocol: 'pinned_authenticated_service_control_v1', controllerPathSha256: sha256('/pinned/controller'),
    controllerSha256: E, requestSha256: sha256('controller-request'),
    runtimeBindingSha256: sha256('controller-runtime-binding'),
    argvSha256: sha256('controller-argv'), tupleIdentity: identity.tupleIdentity, binarySha256: D,
    generationId: identity.generationId, oldService,
    termination: { requestedSignal: 'SIGKILL', observedSignal: 'SIGKILL', exitCode: null,
      processGroupDead: true, completedAt: '2026-08-28T00:06:00.000Z' },
    newService, restartReceipt: index, networkDisabled: true }
  return { ...safe, digest: sha256(canonicalJSONStringify(safe)) }
}

function publicResidue(tupleIdentity, authoritySha256, generationId) {
  const count = () => ({ count: 0, aggregateSha256: EMPTY })
  return { schemaVersion: 'chora.m1-o4-residue-proof.v1', status: 'proven_zero',
    authoritySha256, tupleIdentity, docker: { schemaVersion: 'chora.m1-o4-residue-observation.v1',
      status: 'proven_zero', engineQualificationSha256: D, runtimeScopeSha256: E,
      containers: count(), verifierContainers: count(), networks: count(), volumes: count(),
      configs: count(), managedWorkspaces: count(), attemptProcessGroups: count(),
      swarmInactive: true, servingProcessExcluded: true, operationLedgerSettled: true,
      operationLedgerCount: 8, operationLedgerSha256: F }, verificationWorkspaces: count(),
    generationReferences: { serving: { count: 1, aggregateSha256: sha256(`${generationId}\n`) },
      activeAttempts: count(), recoverableAttempts: count(), currentGenerationSha256: sha256(generationId) } }
}

function zeroResidue() {
  return { ownedContainers: 0, ownedNetworks: 0, ownedVolumes: 0, ownedConfigs: 0,
    ownedWorkspaces: 0, ownedProcessGroups: 0, activeReferences: 0, recoverableReferences: 0 }
}
function systemManagedEngineBinding({ ocrExecutable, ocrExecutableSha256,
  ocrSandboxExecutable, ocrSandboxExecutableSha256,
  ocrSandboxProfileFile, ocrSandboxProfileSha256 }) {
  const safe = {
    schemaVersion: 'chora.m1-o4-system-managed-ocr-engine-binding.v1', status: 'authorized',
    engine: 'system_managed_ocr_engine', modelBytes: 'opaque_unavailable',
    platform: { os: 'darwin', architecture: 'arm64', macOSProductVersion: 'fixture-only',
      macOSBuildVersion: 'fixture-only', darwinSysname: 'Darwin', darwinRelease: 'fixture-only',
      darwinVersion: 'fixture-only', darwinMachine: 'arm64' },
    vision: { bundleIdentifier: 'com.apple.Vision', bundleVersion: 'fixture-only',
      infoPlistSha256: sha256('fixture Vision Info.plist'), supportedRecognitionRevisions: [1],
      requestedRecognitionRevision: 1, recognitionLevel: 'accurate', recognitionLanguage: 'en-US',
      usesLanguageCorrection: true, confidenceThreshold: 0.5 },
    executable: { pathSha256: sha256(ocrExecutable), sha256: ocrExecutableSha256 },
    sandbox: { executableFile: ocrSandboxExecutable,
      executablePathSha256: sha256(ocrSandboxExecutable), executableSha256: ocrSandboxExecutableSha256,
      profileFile: ocrSandboxProfileFile, profilePathSha256: sha256(ocrSandboxProfileFile),
      profileSha256: ocrSandboxProfileSha256, networkRule: 'deny network*' },
  }
  return { ...safe, bindingDigest: sha256(canonicalJSONStringify(safe)) }
}
function screenshotOCRRecord(recognizedText, engineBindingSha256, executableSha256) {
  const safe = { schemaVersion: SCREENSHOT_OCR_SCHEMA, completed: true, pngSha256: sha256(PNG),
    engineBindingSha256, executableSha256, visionBundleIdentifier: 'com.apple.Vision',
    visionBundleVersion: '1.0', visionInfoPlistSha256: sha256('Vision Info.plist'),
    actualRecognitionRevision: 1, recognitionLevel: 'accurate', recognitionLanguage: 'en-US',
    usesLanguageCorrection: true, confidenceThreshold: 0.5, observationCount: 1,
    recognizedText, recognizedTextSha256: sha256(recognizedText), minimumConfidence: 0.9,
    averageConfidence: 0.9, credentialMatches: 0, genericSecretMatches: 0,
    privatePathMatches: 0, emailMatches: 0, urlMatches: 0,
    modelBytes: 'opaque_unavailable', networkDisabled: true }
  return { ...safe, recordDigest: sha256(canonicalJSONStringify(safe)) }
}
function uuid(n) { return `00000000-0000-7000-8000-${String(n).padStart(12, '0')}` }
function taskId(n) { return `task_${uuid(n)}` }
function runId(n) { return `run_${uuid(n)}` }
function attemptId(n) { return `attempt_${uuid(n)}` }
function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function clone(value) { return JSON.parse(JSON.stringify(value)) }
function without(value, field) { const copy = clone(value); delete copy[field]; return copy }
function redigest(value, field, canonical = true) {
  const safe = without(value, field)
  value[field] = sha256(canonical ? canonicalJSONStringify(safe) : JSON.stringify(safe))
}
function mutateReceipt(receipt, mutate) {
  const value = clone(receipt)
  mutate(value)
  redigest(value, 'receiptDigest')
  return value
}
async function mutateJSON(path, mutate) {
  const value = JSON.parse(await readFile(path, 'utf8'))
  mutate(value)
  await chmod(path, 0o600)
  await writeFile(path, `${JSON.stringify(value)}\n`, { flag: 'w' })
  await chmod(path, 0o400)
}
async function replace0400(path, bytes) {
  await chmod(path, 0o600)
  await writeFile(path, bytes, { flag: 'w' })
  await chmod(path, 0o400)
}
async function rewriteDisclosureAckArtifact(paths, index, bytes, updateBackupIdentity = false) {
  const value = JSON.parse(await readFile(paths.disclosureExporterAckFile, 'utf8'))
  value.artifacts[index].bytes = bytes.length
  value.artifacts[index].rawSha256 = sha256(bytes)
  if (updateBackupIdentity) value.databaseSnapshot.backupIdentitySha256 = filesystemIdentity(await lstat(paths.stateDatabaseFile))
  redigest(value, 'ackDigest')
  await replace0400(paths.disclosureExporterAckFile, Buffer.from(`${JSON.stringify(value)}\n`))
}
async function collectAllReceipts(paths, receipts) {
  for (let index = 0; index < receipts.length; index++) {
    await collectRecoveryResidueScenario({ receipt: receipts[index],
      outputPath: join(paths.receiptDirectory, RECOVERY_RESIDUE_RECEIPT_NAMES[index]) })
  }
}
async function write0400(path, value, compact = true, raw = false) {
  const bytes = raw ? value : `${JSON.stringify(value, null, compact ? 0 : 0)}\n`
  await writeFile(path, bytes, { flag: 'wx', mode: 0o600 })
  await chmod(path, 0o400)
}
async function writeHostileExecutable(path, behavior) {
  const source = `#!${process.execPath}\n` +
    `const { spawn } = require('node:child_process')\n` +
    `const { writeFileSync } = require('node:fs')\n` +
    `const argv = process.argv.slice(2)\n` +
    `const output = argv[argv.indexOf('--output') + 1]\n` +
    (behavior === 'timeout'
      ? `writeFileSync(output, '{"partial":true}\\n'); setInterval(() => {}, 1000)\n`
      : behavior === 'oversize'
        ? `process.stdout.write('x'.repeat(70 * 1024)); setInterval(() => {}, 1000)\n`
        : `spawn(process.execPath, ['-e', "setTimeout(() => require('node:fs').writeFileSync(process.argv[1], '{\\\"late\\\":true}\\\\n'), 350)", output], { stdio: 'ignore' }); process.exit(0)\n`)
  await writeFile(path, source, { flag: 'wx', mode: 0o500 })
  await chmod(path, 0o500)
}
async function writeDisclosureHostileExecutable(path, behavior) {
  const source = `#!${process.execPath}\n` +
    `const { spawn } = require('node:child_process')\n` +
    `const { chmodSync, readFileSync, writeFileSync } = require('node:fs')\n` +
    `const request = JSON.parse(readFileSync(process.argv[2], 'utf8'))\n` +
    (behavior === 'timeout'
      ? `writeFileSync(request.outputs.executionLogFile, 'partial'); setInterval(() => {}, 1000)\n`
      : behavior === 'oversize'
        ? `writeFileSync(request.outputs.executionLogFile, Buffer.alloc(17 * 1024 * 1024, 120)); chmodSync(request.outputs.executionLogFile, 0o400); process.exit(0)\n`
        : `spawn(process.execPath, ['-e', "setTimeout(() => require('node:fs').writeFileSync(process.argv[1], 'late'), 350)", request.outputs.executionLogFile], { stdio: 'ignore' }); process.exit(0)\n`)
  await writeFile(path, source, { flag: 'wx', mode: 0o500 })
  await chmod(path, 0o500)
}
function disclosureHostileRunner(executable) {
  let child
  return {
    exportApplyDisclosureSources({ requestFile, requestSha256 }) {
      child = spawn(executable, [requestFile], { detached: true, stdio: 'ignore' })
      return new Promise((resolvePromise, reject) => {
        child.once('error', reject)
        child.once('close', () => resolvePromise({ status: 'completed', requestSha256 }))
      })
    },
    async abortApplyDisclosureExporter({ requestSha256 }) {
      if (child?.pid) {
        try { process.kill(-child.pid, 'SIGKILL') } catch (error) { if (error.code !== 'ESRCH') throw error }
        const deadline = Date.now() + 2_000
        while (true) {
          try { process.kill(-child.pid, 0) } catch (error) {
            if (error.code === 'ESRCH') break
            throw error
          }
          if (Date.now() >= deadline) throw new Error('hostile disclosure process group survived abort')
          await new Promise((resolvePromise) => setTimeout(resolvePromise, 10))
        }
      }
      return { status: 'aborted', requestSha256, processGroupDead: true, descendantsDead: true }
    },
  }
}
async function mode(path) { return (await import('node:fs/promises')).stat(path).then((info) => info.mode & 0o777) }
async function assertAbsent(path) { await assert.rejects(readFile(path), (error) => error.code === 'ENOENT') }
