import { createHash } from 'node:crypto'
import {
  lstat, mkdir, open, readdir,
} from 'node:fs/promises'
import { basename, dirname, isAbsolute, join, normalize, relative, resolve, sep } from 'node:path'

import {
  canonicalJSONStringify,
  finalizeEvidencePhaseE,
} from './o4-candidate-install-orchestrator.mjs'
import {
  cleanupOwnedPath,
  ownedPathIdentity,
  validateOwnedPathCapability,
} from './o4-owned-path-cleanup.mjs'
import {
  DOCKER_OPERATION_LEDGER_SCHEMA,
  buildImageReuseRecord,
  computeAuditSliceDigest,
} from './o4-image-reuse-record.mjs'
import {
  assertProfileReuseManifest,
  assertProfileReuseReceipt,
  assertProfileReuseRecord,
} from './o4-profile-reuse-record.mjs'
import {
  DISCLOSURE_SOURCE_ARTIFACTS,
  RECOVERY_RESIDUE_RECEIPT_NAMES,
  RECOVERY_RESIDUE_SCENARIOS,
  RECOVERY_RESIDUE_SCREENSHOT_NAMES,
  assertDisclosureSourceManifest,
  assertRecoveryResidueManifest,
  assertRecoveryResiduePhaseDRecord,
  assertRecoveryResidueScenarioReceipt,
  buildFinalRecoveryResidueRecord,
} from './o4-recovery-residue-record.mjs'
import {
  DISCLOSURE_RECORD_SCHEMA,
  PROFILE_JOURNEY_SCHEMA,
  assertInstalledEvidencePublication,
  validateInstalledEvidence,
} from './o4-installed-evidence-gate.mjs'

export const FINAL_EVIDENCE_SCHEMA = 'chora.m1-o4-b1-composite.v1'
export const SERVICE_CLOSURE_SCHEMA = 'chora.m1-o4-service-closure.v1'

const digestRE = /^[0-9a-f]{64}$/
const uid = typeof process.getuid === 'function' ? process.getuid() : null
const maxJSON = 4 * 1024 * 1024
const profileReceiptNames = Object.freeze([
  '01-minimal.json', '02-standard-1.json', '03-standard-2.json',
  '04-standard-3.json', '05-trusted-local.json',
])
const outputEntries = Object.freeze([
  'b1-composite.json', 'disclosure', 'image-reuse.json', 'installed-evidence.json',
  'profiles', 'raw', 'recovery.json',
])
const rawEntries = Object.freeze(['docker-operation-ledger.json', 'service-closure.json'])
const disclosureEntries = Object.freeze([
  'change.patch', 'disclosure.json', 'execution.log', 'payload.json',
  'screen-metadata.json', 'screen-ocr.json', 'screen.png', 'state.db',
])
const profileEntries = Object.freeze(['minimal.json', 'standard.json', 'trusted-local.json'])

const pathKeys = Object.freeze([
  'tupleFile', 'receiptDirectory', 'sourceA3LedgerFile',
  'phaseCManifestFile', 'phaseCRecordFile', 'phaseCReceiptDirectory',
  'phaseDManifestFile', 'phaseDRecordFile', 'phaseDReceiptDirectory',
  'phaseDDisclosureSourceDirectory', 'phaseDScreenshotDirectory',
  'phaseDScreenshotMetadataDirectory', 'phaseDScreenshotOCRDirectory',
  'privateEnvironmentMarkerFile', 'installedDoctorRecordFile', 'modelObservationFile',
  'credentialCorpusFile',
  'candidateManifestFile', 'sourceManifestFile', 'sourceBundleRoot', 'binaryFile',
  'webDirectory', 'releaseSpecFile', 'releaseManifestFile', 'offlineBundleEvidenceFile',
  'pathPiProvenanceFile', 'privatePiProvenanceFile', 'qualificationFile',
  'engineEndpointEvidenceFile', 'runnerModuleFile', 'outputDirectory',
])

/**
 * The sole phase-E entry point. The only executable dependency is a pinned B2
 * Runner with two narrow capabilities. All callbacks passed to the legacy
 * ordering controller are closed over here and are never caller supplied.
 */
export async function finalizeO4Evidence(input, dependencies) {
  plain(input, 'O4 final evidence input')
  exactKeys(input, ['paths', 'pins', 'standardResourceLedgerFiles', 'closeTimeoutMs'],
    'O4 final evidence input')
  plain(input.paths, 'O4 final evidence paths')
  exactKeys(input.paths, pathKeys, 'O4 final evidence paths')
  plain(input.pins, 'O4 final evidence pins')
  exactKeys(input.pins, ['runnerModuleSha256'], 'O4 final evidence pins')
  digest(input.pins.runnerModuleSha256, 'B2 Runner module pin')
  assert(Array.isArray(input.standardResourceLedgerFiles) && input.standardResourceLedgerFiles.length === 3,
    'exactly three Standard resource ledgers are required')
  assert(Number.isSafeInteger(input.closeTimeoutMs) && input.closeTimeoutMs >= 50 &&
    input.closeTimeoutMs <= 120_000, 'service close timeout is outside the bounded contract')
  plain(dependencies, 'phase-E dependencies')
  exactKeys(dependencies, ['runner', 'repositoryCapture'], 'phase-E dependencies')
  const repositoryCapture = validateOwnedPathCapability(
    dependencies?.repositoryCapture, 'phase-E owned-path capability')
  const runner = validateRunner(dependencies?.runner)
  await preflightPaths(input)

  const transaction = await createTransaction(input.paths.outputDirectory)
  let appended = false
  let sealed = false
  let sealAttempted = false
  try {
    const state = { transaction, sealedA3: null, products: null }
    const receipt = await finalizeEvidencePhaseE({
      tupleFile: input.paths.tupleFile,
      receiptDirectory: input.paths.receiptDirectory,
      closeAllServices: async () => {
        const proof = await boundedClose(runner, input.closeTimeoutMs, {
          tupleFile: input.paths.tupleFile,
          sourceA3LedgerFile: input.paths.sourceA3LedgerFile,
        })
        state.serviceClosure = assertServiceClosure(proof)
      },
      reopenAndSealA3: async (tuple) => {
        const before = await authenticateCommittedInputs(input, tuple, false)
        state.committedBeforeSeal = committedRawIndexes(before)
        sealAttempted = true
        const seal = await runner.sealA3Ledger({
          tupleIdentity: tuple.identity,
          sourceA3LedgerFile: input.paths.sourceA3LedgerFile,
          expectedUnsealedSha256: before.a3.sha256,
          expectedAuditRecordCount: before.a3.value.auditRecordCount,
          expectedAuditFinalDigest: before.a3.value.auditFinalDigest,
        })
        state.sealedA3 = await authenticateSealedA3(input.paths.sourceA3LedgerFile, tuple, before)
        sealed = true
        assertSealAck(seal, before.a3)
        state.sealAck = seal
        assertFinalRunnerBindings({ closure: state.serviceClosure, sealAck: seal,
          sealedA3: state.sealedA3.value, tuple,
          runnerModuleSha256: input.pins.runnerModuleSha256 })
        return {
          sealed: true, sha256: state.sealedA3.sha256, tupleIdentity: tuple.identity,
          environmentId: tuple.environmentId, installId: tuple.installId,
          generationId: tuple.generationId,
        }
      },
      formCompositeB1: async ({ tuple, sealedA3 }) => {
        const committed = await authenticateCommittedInputs(input, tuple, true)
        assertCommittedRawIndexes(committed, state.committedBeforeSeal,
          'committed C/D evidence changed while A3 was sealed')
        assert(committed.a3.sha256 === sealedA3.sha256, 'sealed A3 changed before final formation')
        assertFinalRunnerBindings({ closure: state.serviceClosure, sealAck: state.sealAck,
          sealedA3: committed.a3.value, tuple,
          runnerModuleSha256: input.pins.runnerModuleSha256 })
        state.products = await formAndPublishProducts(input, transaction, tuple,
          state.serviceClosure, committed)
        return state.products.b1
      },
      validateEvidence: async ({ tuple, sealedA3, composite }) => {
        await reopenFinalTree(input, transaction, state.products, composite)
        return {
          status: 'passed', tupleIdentity: tuple.identity,
          environmentId: tuple.environmentId, installId: tuple.installId,
          generationId: tuple.generationId, a3Sha256: sealedA3.sha256,
          b1Sha256: sha256(canonicalJSONStringify(composite)),
        }
      },
    }, { repositoryCapture })
    appended = true
    return Object.freeze({
      receipt, outputDirectory: transaction.root,
      b1Sha256: sha256(canonicalJSONStringify(state.products.b1)),
      sourceA3LedgerSha256: state.sealedA3.sha256,
      installedEvidenceSha256: state.products.indexes.installedEvidence.rawSha256,
    })
  } catch (error) {
    if (appended) throw error
    try {
      await cleanupOwnedTransaction(transaction, repositoryCapture)
    } catch (cleanupError) {
      throw new Error('phase E failed and its owned transaction cleanup failed; root retained fail-closed', {
        cause: new AggregateError([error, cleanupError]),
      })
    }
    if (sealed || sealAttempted && await isActuallySealed(input.paths.sourceA3LedgerFile)) {
      // Sealing is intentionally durable. Cleanup is restricted to E output.
      await authenticateSealedA3(input.paths.sourceA3LedgerFile, null, null)
    }
    throw error
  }
}

export function deriveDockerOperationLedger(source, sourceBytes) {
  validateSealedLedgerShape(source)
  const standard = source.attempts.filter((attempt) => attempt.profile === 'standard')
  assert(standard.length === 3 && standard.every((attempt, index) => attempt.cohortSequence === index + 1),
    'sealed A3 lacks the exact Standard cohort')
  const setup = phaseWindows(source.commandAudit, 'setup')
  assert(setup.length === 1, 'sealed A3 setup range is ambiguous')
  const ranges = [{ kind: 'setup', ordinal: 1, ...rangeProjection(setup[0]) }]
  for (let index = 0; index < standard.length; index++) {
    const attempt = standard[index]
    const attemptRecords = source.commandAudit.filter(({ sequence }) =>
      sequence >= attempt.auditStartSequence && sequence <= attempt.auditEndSequence)
    const verifierRecords = source.commandAudit.filter(({ sequence }) =>
      sequence >= attempt.verifierStartSequence && sequence <= attempt.verifierEndSequence)
    ranges.push({ kind: 'standard_attempt', ordinal: index + 1,
      ...rangeProjection({ records: attemptRecords }) })
    ranges.push({ kind: 'standard_verifier', ordinal: index + 1,
      ...rangeProjection({ records: verifierRecords }) })
  }
  const selected = ranges.flatMap((range) => source.commandAudit.filter(({ sequence }) =>
    sequence >= range.startSequence && sequence <= range.endSequence))
  const phaseOperationCounts = {}
  for (const phase of ['setup', 'attempt', 'retry', 'restart', 'recovery', 'verifier']) {
    phaseOperationCounts[phase] = { build: 0, pull: 0, load: 0 }
    for (const record of source.commandAudit.filter((record) => record.phase === phase)) {
      if (Object.hasOwn(phaseOperationCounts[phase], record.operationClass)) {
        phaseOperationCounts[phase][record.operationClass]++
      }
    }
  }
  assert(phaseOperationCounts.setup.build === 0 && phaseOperationCounts.setup.pull === 0 &&
    phaseOperationCounts.setup.load === 2, 'sealed A3 Setup acquisition drifted')
  for (const phase of ['attempt', 'retry', 'restart', 'recovery', 'verifier']) {
    assert(Object.values(phaseOperationCounts[phase]).every((count) => count === 0),
      `sealed A3 ${phase} contains a forbidden image operation or engine substitute`)
  }
  const setupLoads = source.commandAudit.filter((record) =>
    record.phase === 'setup' && record.operationClass === 'load')
  assert(setupLoads.length === 2, 'sealed A3 must contain exactly two Setup loads')
  const roles = [
    ['managed-pi-runtime', ['managed_pi_runtime', 'independent_verifier', 'capability_probe'],
      source.roleImages.managed_pi_runtime],
    ['network-boundary', ['network_boundary'], source.roleImages.network_boundary],
  ]
  return {
    schemaVersion: DOCKER_OPERATION_LEDGER_SCHEMA, status: 'passed',
    environmentId: source.environmentId, installId: source.installId,
    generationId: source.generationId, platform: clone(source.platform),
    bindingDigest: source.bindingDigest, tupleIdentity: source.tupleIdentity,
    observationSource: 'e-derived-sealed-a3-ledger-projection', complete: true,
    engineEventsUsed: false, roleImages: clone(source.roleImages),
    sourceA3LedgerSha256: sha256(sourceBytes), sourceA3AuditRecordCount: source.auditRecordCount,
    sourceA3AuditFinalDigest: source.auditFinalDigest,
    selectedA3AuditRanges: ranges,
    selectedA3AuditSequenceList: selected.map(({ sequence }) => sequence),
    commandAudit: clone(selected),
    setupOperations: setupLoads.map((record, index) => {
      const binding = roles[index][2]
      const inspection = source.commandAudit.find((candidate) => candidate.sequence > record.sequence &&
        candidate.phase === 'setup' && candidate.operationClass === 'inspect' &&
        candidate.safeTargetSha256 === binding.dockerConfigImageId.slice(7))
      assert(inspection?.result === 'succeeded',
        'sealed A3 Setup load lacks a subsequent exact config-ID inspection')
      return {
        sequence: index + 1, auditSequence: record.sequence,
        inspectionAuditSequence: inspection.sequence, phase: 'setup',
        operationClass: 'load', artifactId: roles[index][0], roles: roles[index][1],
        archiveSha256: binding.archiveSha256, archiveSize: binding.archiveSize,
        dockerConfigImageId: binding.dockerConfigImageId, result: 'succeeded',
      }
    }),
    phaseOperationCounts,
    attempts: standard.map((attempt, index) => projectAttempt(attempt, index + 1)),
  }
}

export function buildFinalProfileJourneys({ receipts, reuse, sourceA3 }) {
  assert(Array.isArray(receipts) && receipts.length === 5, 'phase-C receipt cohort is incomplete')
  receipts.forEach((receipt, index) => assertProfileReuseReceipt(receipt, {
    sequence: index + 1,
    journey: ['minimal', 'standard_1', 'standard_2', 'standard_3', 'trusted_local'][index],
    profile: ['minimal', 'standard', 'standard', 'standard', 'trusted_local'][index],
    cohortSequence: [null, 1, 2, 3, null][index],
  }))
  const selected = [receipts[0], receipts[1], receipts[4]]
  return selected.map((receipt, index) => {
    const profile = ['minimal', 'standard', 'trusted_local'][index]
    const base = {
      schemaVersion: PROFILE_JOURNEY_SCHEMA, status: 'passed', sequence: index + 1, profile,
      environmentId: receipt.environmentId, installId: receipt.installId,
      generationId: receipt.generationId, bindingDigest: receipt.bindingDigest,
      publicProductEntry: true, journeyId: `jny_${sha256(receipt.receiptDigest).slice(0, 48)}`,
      taskId: receipt.taskId, runId: receipt.runId, attemptId: receipt.attemptId,
      workspaceIdentity: receipt.workspaceIdentity, tupleIdentity: receipt.tupleIdentity,
      executionIdentityDigest: receipt.executionIdentityDigest,
      sourceA3LedgerSha256: reuse.sourceA3LedgerSha256,
      sourceA3AuditFinalDigest: reuse.sourceA3AuditFinalDigest,
      profileEvidence: profileEvidence(profile, receipt),
    }
    if (profile === 'minimal') {
      assert(base.executionIdentityDigest === reuse.sourceMinimalExecutionIdentityDigest &&
        receipt.verifierSource.sliceDigest === reuse.sourceMinimalVerifierSliceDigest,
      'Minimal journey was not selected from sealed A3')
    }
    if (profile === 'standard') {
      for (const key of ['taskId', 'runId', 'attemptId', 'workspaceIdentity',
        'tupleIdentity', 'executionIdentityDigest']) {
        assert(base[key] === reuse.managedAttempts[0][key], 'Standard journey was not selected from cohort 1')
      }
    }
    assert(sourceA3.auditFinalDigest === base.sourceA3AuditFinalDigest,
      'profile journey source A3 final digest drifted')
    return Object.freeze({ ...base, digest: sha256(canonicalJSONStringify(base)) })
  })
}

export function buildB1Composite(input) {
  plain(input, 'B1 formation input')
  const phases = {
    c: phaseBinding(input.committed.cManifest, input.committed.cRecord, input.committed.cReceipt),
    d: phaseBinding(input.committed.dManifest, input.committed.dRecord, input.committed.dReceipt),
  }
  const components = {
    dockerOperationLedgerSha256: input.indexes.operationLedger.rawSha256,
    imageReuseRecordSha256: input.indexes.imageReuse.rawSha256,
    imageReuseDigest: input.indexes.imageReuse.semanticDigest,
    profileJourneys: input.indexes.profileJourneys.map(({ profile, rawSha256 }) => ({
      profile, sha256: rawSha256,
    })),
    recoveryRecordSha256: input.indexes.recovery.rawSha256,
    recoveryDigest: input.indexes.recovery.semanticDigest,
    disclosureRecordSha256: input.indexes.disclosure.rawSha256,
    disclosureDigest: input.indexes.disclosure.semanticDigest,
    installedDoctorRecordSha256: input.indexes.installedDoctor.rawSha256,
  }
  const draft = {
    schemaVersion: FINAL_EVIDENCE_SCHEMA, status: 'passed',
    environmentId: input.tuple.environmentId, installId: input.tuple.installId,
    generationId: input.tuple.generationId, platform: clone(input.tuple.platform),
    bindingDigest: input.committed.a3.value.bindingDigest, tupleIdentity: input.tuple.identity,
    sourceA3LedgerSha256: input.committed.a3.sha256,
    sourceA3AuditRecordCount: input.committed.a3.value.auditRecordCount,
    sourceA3AuditFinalDigest: input.committed.a3.value.auditFinalDigest,
    serviceClosureSha256: input.indexes.serviceClosure.rawSha256,
    phases, components,
    installedEvidence: {
      schemaVersion: input.installed.schemaVersion,
      bindings: clone(input.installed.bindings), counts: clone(input.installed.counts),
      components: clone(input.installed.components),
      aggregateDigest: input.installed.aggregateDigest,
    },
  }
  return Object.freeze({ ...draft, digest: sha256(canonicalJSONStringify(draft)) })
}

export function assertFinalRunnerBindings(input) {
  plain(input, 'final Runner binding input')
  exactKeys(input, ['closure', 'sealAck', 'sealedA3', 'tuple', 'runnerModuleSha256'],
    'final Runner binding input')
  const closure = assertServiceClosure(input.closure)
  const { tuple, sealedA3 } = input
  digest(input.runnerModuleSha256, 'final Runner module pin')
  for (const field of ['environmentId', 'installId', 'generationId']) {
    assert(closure[field] === tuple[field] && closure[field] === sealedA3[field],
      `service closure ${field} binding drifted`)
  }
  assert(closure.tupleIdentity === tuple.identity && closure.tupleIdentity === sealedA3.tupleIdentity,
    'service closure tuple binding drifted')
  assert(closure.bindingDigest === sealedA3.bindingDigest,
    'service closure A3 binding digest drifted')
  assert(closure.runnerModuleSha256 === input.runnerModuleSha256,
    'service closure Runner module pin drifted')
  assertSealAck(input.sealAck, { sha256: input.sealAck.beforeSha256,
    value: { auditRecordCount: 1 } })
  assert(input.sealAck.auditRecordCount === sealedA3.auditRecordCount &&
    input.sealAck.auditFinalDigest === sealedA3.auditFinalDigest,
  'A3 seal acknowledgement does not match reopened sealed A3')
  return true
}

export async function validateFinalEvidenceTree(outputDirectory) {
  exactAbsolute(outputDirectory, 'phase-E output directory')
  await exactDirectoryEntries(outputDirectory, outputEntries, 'phase-E output')
  const rawDirectory = join(outputDirectory, 'raw')
  const profileDirectory = join(outputDirectory, 'profiles')
  const disclosureDirectory = join(outputDirectory, 'disclosure')
  await exactDirectoryEntries(rawDirectory, rawEntries, 'phase-E raw evidence')
  await exactDirectoryEntries(profileDirectory, profileEntries, 'phase-E profiles')
  await exactDirectoryEntries(disclosureDirectory, disclosureEntries, 'phase-E disclosure')
  const files = [
    ...rawEntries.map((name) => join(rawDirectory, name)),
    ...profileEntries.map((name) => join(profileDirectory, name)),
    ...disclosureEntries.map((name) => join(disclosureDirectory, name)),
    ...['b1-composite.json', 'image-reuse.json', 'installed-evidence.json', 'recovery.json']
      .map((name) => join(outputDirectory, name)),
  ]
  for (const file of files) await read0400(file, `phase-E output ${basename(file)}`, 128 * 1024 * 1024)
  return true
}

export async function validateFinalEvidenceBindings({
  outputDirectory, installedDoctorRecordFile, expectedIndexes,
}) {
  await validateFinalEvidenceTree(outputDirectory)
  const rawDirectory = join(outputDirectory, 'raw')
  const profileDirectory = join(outputDirectory, 'profiles')
  const disclosureDirectory = join(outputDirectory, 'disclosure')
  const service = await readJSON0400(join(rawDirectory, 'service-closure.json'),
    'reopened service closure')
  assertServiceClosure(service.value)
  const operation = await readJSON0400(join(rawDirectory, 'docker-operation-ledger.json'),
    'reopened Docker operation ledger')
  const reuse = await readJSON0400(join(outputDirectory, 'image-reuse.json'),
    'reopened image reuse')
  const recovery = await readJSON0400(join(outputDirectory, 'recovery.json'),
    'reopened recovery')
  const disclosure = await readJSON0400(join(disclosureDirectory, 'disclosure.json'),
    'reopened disclosure')
  const installed = await readJSON0400(join(outputDirectory, 'installed-evidence.json'),
    'reopened installed evidence')
  const doctor = await readJSON0400(installedDoctorRecordFile, 'reopened installed Doctor')
  const profiles = []
  for (const [profile, name] of [['minimal', 'minimal.json'], ['standard', 'standard.json'],
    ['trusted_local', 'trusted-local.json']]) {
    const input = await readJSON0400(join(profileDirectory, name), `reopened ${profile} profile`)
    assert(input.value.profile === profile && digestRE.test(input.value.digest),
      `reopened ${profile} profile identity/digest drifted`)
    profiles.push({ profile, file: name, rawSha256: input.sha256,
      semanticDigest: input.value.digest })
  }
  for (const [value, field, label] of [
    [reuse.value, 'digest', 'image reuse'], [recovery.value, 'digest', 'recovery'],
    [disclosure.value, 'digest', 'disclosure'],
    [installed.value, 'aggregateDigest', 'installed evidence'],
  ]) digest(value[field], `reopened ${label} semantic digest`)
  const actual = {
    serviceClosure: { file: 'service-closure.json', rawSha256: service.sha256,
      semanticDigest: service.value.digest },
    operationLedger: { file: 'docker-operation-ledger.json', rawSha256: operation.sha256,
      semanticDigest: sha256(canonicalJSONStringify(operation.value)) },
    imageReuse: { file: 'image-reuse.json', rawSha256: reuse.sha256,
      semanticDigest: reuse.value.digest },
    profileJourneys: profiles,
    recovery: { file: 'recovery.json', rawSha256: recovery.sha256,
      semanticDigest: recovery.value.digest },
    disclosure: { file: 'disclosure.json', rawSha256: disclosure.sha256,
      semanticDigest: disclosure.value.digest },
    installedDoctor: { file: basename(installedDoctorRecordFile), rawSha256: doctor.sha256,
      semanticDigest: sha256(canonicalJSONStringify(doctor.value)) },
    installedEvidence: { file: 'installed-evidence.json', rawSha256: installed.sha256,
      semanticDigest: installed.value.aggregateDigest },
  }
  assert(canonicalJSONStringify(actual) === canonicalJSONStringify(expectedIndexes),
    'reopened phase-E component raw/semantic indexes drifted')
  return true
}

async function formAndPublishProducts(input, transaction, tuple, serviceClosure, committed) {
  const p = input.paths
  const rawDirectory = join(transaction.root, 'raw')
  const serviceClosureFile = join(rawDirectory, 'service-closure.json')
  const operationLedgerFile = join(rawDirectory, 'docker-operation-ledger.json')
  const imageReuseFile = join(transaction.root, 'image-reuse.json')
  const profileDirectory = join(transaction.root, 'profiles')
  const recoveryFile = join(transaction.root, 'recovery.json')
  const disclosureDirectory = join(transaction.root, 'disclosure')
  const b1File = join(transaction.root, 'b1-composite.json')
  const installedEvidenceFile = join(transaction.root, 'installed-evidence.json')
  await mkdir(rawDirectory, { recursive: false, mode: 0o700 })
  await mkdir(profileDirectory, { recursive: false, mode: 0o700 })
  await mkdir(disclosureDirectory, { recursive: false, mode: 0o700 })
  await write0400JSON(serviceClosureFile, serviceClosure)

  const operationLedger = deriveDockerOperationLedger(committed.a3.value, committed.a3.bytes)
  await write0400JSON(operationLedgerFile, operationLedger)
  const reuse = await buildImageReuseRecord({ operationLedgerFile,
    sourceA3LedgerFile: p.sourceA3LedgerFile,
    resourceLedgerFiles: input.standardResourceLedgerFiles,
    privateEnvironmentMarkerFile: p.privateEnvironmentMarkerFile })
  await write0400JSON(imageReuseFile, reuse)
  await assert0400(imageReuseFile, 'image reuse output')

  const profileReceipts = await readExactJSONDirectory(p.phaseCReceiptDirectory,
    profileReceiptNames, 'phase-C receipts', (value, index) => assertProfileReuseReceipt(value, {
      sequence: index + 1,
      journey: ['minimal', 'standard_1', 'standard_2', 'standard_3', 'trusted_local'][index],
      profile: ['minimal', 'standard', 'standard', 'standard', 'trusted_local'][index],
      cohortSequence: [null, 1, 2, 3, null][index],
    }))
  const journeys = buildFinalProfileJourneys({ receipts: profileReceipts.map(({ value }) => value),
    reuse, sourceA3: committed.a3.value })
  const profilePaths = [join(profileDirectory, 'minimal.json'), join(profileDirectory, 'standard.json'),
    join(profileDirectory, 'trusted-local.json')]
  for (let index = 0; index < journeys.length; index++) await write0400JSON(profilePaths[index], journeys[index])

  const dReceipts = await readExactJSONDirectory(p.phaseDReceiptDirectory,
    RECOVERY_RESIDUE_RECEIPT_NAMES, 'phase-D receipts', (value, index) =>
      assertRecoveryResidueScenarioReceipt(value, RECOVERY_RESIDUE_SCENARIOS[index]))
  const recovery = buildFinalRecoveryResidueRecord({
    manifest: committed.dManifest.value, phaseDRecord: committed.dRecord.value,
    receipts: dReceipts.map(({ value }) => value),
    reuse: { environmentId: reuse.environmentId, installId: reuse.installId,
      generationId: reuse.generationId, bindingDigest: reuse.bindingDigest, digest: reuse.digest },
  })
  await write0400JSON(recoveryFile, recovery)
  const disclosure = await copyAndFormDisclosure(input, committed, disclosureDirectory)

  const gateInput = gateInputs(input, {
    imageReuseFile, profileDirectory, recoveryFile, disclosureDirectory,
  })
  const installed = await validateInstalledEvidence(gateInput)
  await write0400JSON(installedEvidenceFile, installed)
  assertInstalledEvidencePublication(installed)

  const indexes = {
    serviceClosure: await indexJSON(serviceClosureFile, serviceClosure.digest),
    operationLedger: await indexJSON(operationLedgerFile),
    imageReuse: await indexJSON(imageReuseFile, reuse.digest),
    profileJourneys: await Promise.all(profilePaths.map((path, index) =>
      indexJSON(path, journeys[index].digest).then((entry) => ({ profile: journeys[index].profile, ...entry })))),
    recovery: await indexJSON(recoveryFile, recovery.digest),
    disclosure: await indexJSON(join(disclosureDirectory, 'disclosure.json'), disclosure.digest),
    installedDoctor: await indexJSON(p.installedDoctorRecordFile),
    installedEvidence: await indexJSON(installedEvidenceFile, installed.aggregateDigest),
  }
  const b1 = buildB1Composite({ tuple, committed, indexes, installed })
  await write0400JSON(b1File, b1)
  return { b1, installed, indexes, files: { serviceClosureFile, operationLedgerFile,
    rawDirectory, imageReuseFile, profileDirectory, recoveryFile, disclosureDirectory, b1File,
    installedEvidenceFile }, committedRawIndexes: committedRawIndexes(committed) }
}

async function copyAndFormDisclosure(input, committed, target) {
  const sourceManifest = committed.disclosureManifest.value
  const sourceFiles = DISCLOSURE_SOURCE_ARTIFACTS.map(({ name }) => name)
  const scans = []
  const corpus = await readJSON0400(input.paths.credentialCorpusFile, 'credential corpus')
  assert(Array.isArray(corpus.value.secrets), 'credential corpus secrets are missing')
  const secrets = corpus.value.secrets.map(({ value }) => Buffer.from(value, 'utf8'))
  for (let index = 0; index < sourceFiles.length; index++) {
    const name = sourceFiles[index]
    const sourcePath = join(input.paths.phaseDDisclosureSourceDirectory, name)
    const bytes = await read0400(sourcePath, `D disclosure ${name}`, 64 * 1024 * 1024)
    const expected = sourceManifest.artifacts[index]
    assert(expected.name === name && expected.rawSha256 === sha256(bytes) && expected.bytes === bytes.length,
      `D disclosure ${name} bytes drifted`)
    await write0400(join(target, name), bytes)
    scans.push({ name, kind: expected.kind, bytes: bytes.length, sha256: sha256(bytes), ...scan(bytes, secrets) })
  }
  const screenIndex = committed.dManifest.value.screenshots[7]
  const triples = [
    ['screen.png', join(input.paths.phaseDScreenshotDirectory, screenIndex.png.file), screenIndex.png],
    ['screen-metadata.json', join(input.paths.phaseDScreenshotMetadataDirectory, screenIndex.metadata.file), screenIndex.metadata],
    ['screen-ocr.json', join(input.paths.phaseDScreenshotOCRDirectory, screenIndex.ocr.file), screenIndex.ocr],
  ]
  const tripleBytes = []
  for (const [targetName, sourcePath, expected] of triples) {
    const bytes = await read0400(sourcePath, `D disclosure ${targetName}`, 16 * 1024 * 1024)
    assert(sha256(bytes) === expected.rawSha256, `D disclosure ${targetName} bytes drifted`)
    await write0400(join(target, targetName), bytes)
    tripleBytes.push(bytes)
    scans.push({ name: targetName,
      kind: targetName === 'screen.png' ? 'png' : targetName.includes('metadata') ? 'png_metadata' : 'png_ocr',
      bytes: bytes.length, sha256: sha256(bytes), ...scan(bytes, secrets) })
  }
  for (const item of scans) assert(Object.entries(item).filter(([key]) => key.endsWith('Matches'))
    .every(([, value]) => value === 0), `disclosure ${item.kind} contains private material`)
  const metadata = parseJSON(tripleBytes[1], 'D screen metadata')
  const ocr = parseJSON(tripleBytes[2], 'D screen OCR')
  const draft = {
    schemaVersion: DISCLOSURE_RECORD_SCHEMA, status: 'passed',
    environmentId: committed.a3.value.environmentId, installId: committed.a3.value.installId,
    generationId: committed.a3.value.generationId,
    credentialCorpusId: corpus.value.corpusId, artifacts: scans,
    screenshot: {
      pngSha256: sha256(tripleBytes[0]),
      metadataSha256: sha256(canonicalJSONStringify(metadata)),
      ocrSha256: sha256(canonicalJSONStringify(ocr)),
      metadataUnsafeMatches: metadata.unsafeMatchCount,
      ocrUnsafeMatches: ['credentialMatches', 'genericSecretMatches', 'privatePathMatches',
        'emailMatches', 'urlMatches'].reduce((sum, key) => sum + ocr[key], 0),
    },
  }
  assert(draft.screenshot.metadataUnsafeMatches === 0 && draft.screenshot.ocrUnsafeMatches === 0,
    'D screen disclosure scan is unsafe')
  const record = { ...draft, digest: sha256(canonicalJSONStringify(draft)) }
  await write0400JSON(join(target, 'disclosure.json'), record)
  return record
}

async function authenticateCommittedInputs(input, tuple, requireSealed) {
  const p = input.paths
  const [cManifest, cRecord, dManifest, dRecord, cReceipt, dReceipt, disclosureManifest] = await Promise.all([
    readJSON0400(p.phaseCManifestFile, 'committed C manifest'),
    readJSON0400(p.phaseCRecordFile, 'committed C record'),
    readJSON0400(p.phaseDManifestFile, 'committed D manifest'),
    readJSON0400(p.phaseDRecordFile, 'committed D record'),
    readJSON0400(join(p.receiptDirectory, '07-C.json'), 'committed C phase receipt'),
    readJSON0400(join(p.receiptDirectory, '08-D.json'), 'committed D phase receipt'),
    readJSON0400(join(p.phaseDDisclosureSourceDirectory, 'manifest.json'), 'D disclosure source manifest'),
  ])
  assertProfileReuseManifest(cManifest.value)
  assertProfileReuseRecord(cRecord.value)
  assertRecoveryResidueManifest(dManifest.value)
  assertRecoveryResiduePhaseDRecord(dRecord.value)
  assertDisclosureSourceManifest(disclosureManifest.value)
  validateCDReceipts(cReceipt.value, dReceipt.value, tuple)
  assert(cReceipt.value.observationDigest === cRecord.value.observationDigest &&
    cReceipt.value.receiptDigest === cRecord.value.handoffReceiptDigest,
  'committed C receipt/record bytes were spliced')
  assert(dReceipt.value.operationDigest === dManifest.sha256 &&
    dReceipt.value.observationDigest === dRecord.value.recordDigest &&
    dReceipt.value.previousReceiptDigest === cReceipt.value.receiptDigest,
  'committed D receipt/manifest/record bytes were spliced')
  assert(dManifest.value.disclosureSources.manifest.rawSha256 === disclosureManifest.sha256 &&
    dManifest.value.disclosureSources.manifest.semanticDigest === disclosureManifest.value.manifestDigest,
  'D disclosure source manifest was spliced')
  assert(canonicalJSONStringify(dManifest.value.disclosureSources.screenshot) ===
    canonicalJSONStringify(disclosureManifest.value.screenshot) &&
    canonicalJSONStringify(dManifest.value.screenshots[7].png) ===
    canonicalJSONStringify(disclosureManifest.value.screenshot.png) &&
    canonicalJSONStringify(dManifest.value.screenshots[7].metadata) ===
    canonicalJSONStringify(disclosureManifest.value.screenshot.metadata) &&
    canonicalJSONStringify(dManifest.value.screenshots[7].ocr) ===
    canonicalJSONStringify(disclosureManifest.value.screenshot.ocr),
  'D disclosure source/screenshot triple was spliced')
  await exactDirectoryEntries(p.phaseDDisclosureSourceDirectory,
    [...DISCLOSURE_SOURCE_ARTIFACTS.map(({ name }) => name), 'manifest.json'],
    'D disclosure source directory')
  await exactDirectoryEntries(p.phaseDScreenshotDirectory, RECOVERY_RESIDUE_SCREENSHOT_NAMES,
    'D screenshot directory')
  await exactDirectoryEntries(p.phaseDScreenshotMetadataDirectory,
    RECOVERY_RESIDUE_SCREENSHOT_NAMES.map((name) => name.replace(/\.png$/, '-metadata.json')),
    'D screenshot metadata directory')
  await exactDirectoryEntries(p.phaseDScreenshotOCRDirectory,
    RECOVERY_RESIDUE_SCREENSHOT_NAMES.map((name) => name.replace(/\.png$/, '-ocr.json')),
    'D screenshot OCR directory')
  const a3 = await readJSONAnyMode(p.sourceA3LedgerFile, 'source A3 ledger')
  validateLedgerChain(a3.value, requireSealed)
  if (!requireSealed) {
    assert(a3.sha256 === dManifest.value.sourceA3.rawSha256,
      'current unsealed A3 is not the D-bound raw file')
  }
  assert(a3.value.tupleIdentity === tuple.identity && a3.value.environmentId === tuple.environmentId &&
    a3.value.installId === tuple.installId && a3.value.generationId === tuple.generationId,
  'A3/tuple installed identity drifted')
  if (requireSealed) validateCRangeAndDFullCoverage(a3.value, cManifest.value, dManifest.value)
  return { cManifest, cRecord, dManifest, dRecord, cReceipt, dReceipt, disclosureManifest, a3 }
}

async function authenticateSealedA3(path, tuple, before) {
  const result = await readJSON0400(path, 'sealed source A3 ledger')
  validateLedgerChain(result.value, true)
  if (tuple) assert(result.value.tupleIdentity === tuple.identity, 'sealed A3 tuple drifted')
  if (before) {
    assert(result.value.commandAudit.length >= before.a3.value.commandAudit.length &&
      canonicalJSONStringify(result.value.commandAudit.slice(0, before.a3.value.commandAudit.length)) ===
      canonicalJSONStringify(before.a3.value.commandAudit), 'A3 D-bound prefix changed while sealing')
    assert(canonicalJSONStringify(result.value.attempts) === canonicalJSONStringify(before.a3.value.attempts),
      'A3 D-bound Attempt metadata changed while sealing')
    for (const field of ['environmentId', 'installId', 'generationId', 'platform', 'bindingDigest',
      'tupleIdentity', 'observationSource', 'engineEventsUsed', 'roleImages']) {
      assert(canonicalJSONStringify(result.value[field]) === canonicalJSONStringify(before.a3.value[field]),
        `A3 D-bound ${field} changed while sealing`)
    }
  }
  return result
}

async function isActuallySealed(path) {
  try {
    const input = await readJSONAnyMode(path, 'post-seal source A3 ledger')
    return input.value?.auditSealed === true && input.value?.complete === true && input.value?.status === 'passed'
  } catch { return false }
}

function validateLedgerChain(value, sealed) {
  validateSealedLedgerShape(value, false)
  assert(value.auditSealed === sealed && value.complete === sealed &&
    value.status === (sealed ? 'passed' : 'recording'),
  `source A3 is not ${sealed ? 'sealed' : 'unsealed'}`)
  let previous = '0'.repeat(64)
  for (let index = 0; index < value.commandAudit.length; index++) {
    const record = value.commandAudit[index]
    assert(record.sequence === index + 1 && record.previousDigest === previous,
      'source A3 command chain is non-contiguous')
    const { digest: actual, ...draft } = record
    assert(actual === sha256(canonicalJSONStringify(draft)), 'source A3 command digest drifted')
    previous = actual
  }
  assert(value.auditRecordCount === value.commandAudit.length && value.auditFinalDigest === previous,
    'source A3 count/final digest drifted')
}

function validateSealedLedgerShape(value, requireSealed = true) {
  plain(value, 'source A3 ledger')
  exactKeys(value, ['schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
    'bindingDigest', 'tupleIdentity', 'observationSource', 'complete', 'engineEventsUsed',
    'roleImages', 'commandAudit', 'auditRecordCount', 'auditFinalDigest', 'auditSealed', 'attempts'],
  'source A3 ledger')
  assert(value.schemaVersion === 'chora.m1-o4-sealed-a3-runner-ledger.v1' &&
    value.observationSource === 'exact-runner-command-audit' && value.engineEventsUsed === false,
  'source A3 schema/source drifted')
  if (requireSealed) validateLedgerChain(value, true)
}

function validateCRangeAndDFullCoverage(a3, cManifest, dManifest) {
  assert(cManifest.sourceA3AuditRecordCount < a3.auditRecordCount &&
    a3.commandAudit[cManifest.sourceA3AuditRecordCount - 1]?.digest === cManifest.sourceA3AuditFinalDigest,
  'sealed A3 does not retain the exact C prefix')
  const ranges = dManifest.sourceA3.ranges
  assert(Array.isArray(ranges) && ranges.length > 0, 'D A3 ranges are missing')
  let cursor = ranges[0].startSequence - 1
  for (const range of ranges) {
    assert(range.startSequence === cursor + 1, 'D A3 ranges skip or overlap')
    const slice = a3.attempts.slice(range.startSequence - 1, range.endSequence)
    assert(range.sliceDigest === sha256(canonicalJSONStringify(slice)), 'D A3 range digest drifted')
    cursor = range.endSequence
  }
  assert(cursor === a3.attempts.length, 'D A3 ranges do not cover every Attempt')
}

function validateCDReceipts(c, d, tuple) {
  for (const [receipt, phase, sequence] of [[c, 'C', 7], [d, 'D', 8]]) {
    plain(receipt, `phase ${phase} receipt`)
    exactKeys(receipt, ['schemaVersion', 'tupleIdentity', 'phase', 'sequence', 'status',
      'operationDigest', 'observationDigest', 'previousReceiptDigest', 'receiptDigest'],
    `phase ${phase} receipt`)
    const { receiptDigest, ...draft } = receipt
    assert(receipt.schemaVersion === 'chora.m1-o4-candidate-phase-receipt.v1' &&
      receipt.phase === phase && receipt.sequence === sequence && receipt.status === 'passed' &&
      receipt.tupleIdentity === tuple.identity && receiptDigest === sha256(canonicalJSONStringify(draft)),
    `phase ${phase} receipt drifted`)
  }
}

function assertServiceClosure(value) {
  plain(value, 'service closure proof')
  exactKeys(value, ['schemaVersion', 'status', 'environmentId', 'installId', 'generationId',
    'bindingDigest', 'tupleIdentity', 'runnerModuleSha256', 'registeredServiceCount',
    'closedServiceCount', 'services', 'processGroupDead', 'descendantsDead', 'completedAt', 'digest'],
  'service closure proof')
  assert(value.schemaVersion === SERVICE_CLOSURE_SCHEMA && value.status === 'closed' &&
    Number.isSafeInteger(value.registeredServiceCount) && value.registeredServiceCount > 0 &&
    Number.isSafeInteger(value.closedServiceCount) &&
    value.closedServiceCount === value.registeredServiceCount &&
    value.processGroupDead === true && value.descendantsDead === true,
  'service closure is partial or lacks full process-group-death proof')
  assert(Array.isArray(value.services) && value.services.length === value.registeredServiceCount,
    'service closure service count drifted')
  for (const service of value.services) {
    plain(service, 'service closure entry')
    exactKeys(service, ['serviceIdSha256', 'processGroupIdentitySha256', 'status',
      'processGroupDead', 'descendantsDead'], 'service closure entry')
    assert(service.status === 'closed' && service.processGroupDead === true && service.descendantsDead === true,
      'service closure entry is live or partial')
    digest(service.serviceIdSha256, 'service identity'); digest(service.processGroupIdentitySha256, 'process-group identity')
  }
  assert(new Set(value.services.map(({ serviceIdSha256 }) => serviceIdSha256)).size === value.services.length &&
    new Set(value.services.map(({ processGroupIdentitySha256 }) => processGroupIdentitySha256)).size === value.services.length,
  'service closure contains duplicate service/process-group identities')
  assert(typeof value.completedAt === 'string' && Number.isFinite(Date.parse(value.completedAt)),
    'service closure completion time is invalid')
  const { digest: actual, ...draft } = value
  assert(actual === sha256(canonicalJSONStringify(draft)), 'service closure digest drifted')
  return Object.freeze(clone(value))
}

async function boundedClose(runner, timeoutMs, paths) {
  let timer
  try {
    return await Promise.race([
      Promise.resolve().then(() => runner.closeAllServices({ ...paths, timeoutMs })),
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('service close timed out')), timeoutMs) }),
    ])
  } finally { clearTimeout(timer) }
}

function assertSealAck(value, before) {
  plain(value, 'A3 seal acknowledgement')
  exactKeys(value, ['status', 'beforeSha256', 'auditRecordCount', 'auditFinalDigest'],
    'A3 seal acknowledgement')
  assert(value.status === 'sealed' && value.beforeSha256 === before.sha256 &&
    Number.isSafeInteger(value.auditRecordCount) && value.auditRecordCount > 0 &&
    value.auditRecordCount >= before.value.auditRecordCount && digestRE.test(value.auditFinalDigest),
  'A3 seal acknowledgement drifted')
}

function validateRunner(runner) {
  plain(runner, 'pinned B2 final Runner')
  exactKeys(runner, ['closeAllServices', 'sealA3Ledger'], 'pinned B2 final Runner')
  assert(typeof runner.closeAllServices === 'function' && typeof runner.sealA3Ledger === 'function',
    'pinned B2 final Runner capabilities are missing')
  return runner
}

async function preflightPaths(input) {
  const all = [...Object.values(input.paths), ...input.standardResourceLedgerFiles]
  for (const path of all) exactAbsolute(path, 'phase-E path')
  const output = input.paths.outputDirectory
  for (const source of all.filter((path) => path !== output)) {
    assert(output !== source && !within(output, source) && !within(source, output),
      'phase-E input/output paths overlap')
  }
  await assertNoSymlinkAncestors(input.paths.outputDirectory)
  await assertOwnerDirectory(dirname(input.paths.outputDirectory))
  await absent(input.paths.outputDirectory, 'phase-E output')
  const runnerBytes = await read0400(input.paths.runnerModuleFile, 'pinned B2 Runner module', maxJSON)
  assert(sha256(runnerBytes) === input.pins.runnerModuleSha256, 'pinned B2 Runner module digest drifted')
}

async function createTransaction(root) {
  await mkdir(root, { recursive: false, mode: 0o700 })
  await assertOwnerDirectory(root)
  const info = await lstat(root, { bigint: true })
  assert(info.isDirectory() && !info.isSymbolicLink() &&
    info.uid === BigInt(process.getuid()) && Number(info.mode & 0o777n) === 0o700,
  'phase-E transaction ownership changed after creation')
  return Object.freeze({ root, identity: ownedPathIdentity(info) })
}

async function cleanupOwnedTransaction(transaction, repositoryCapture) {
  await cleanupOwnedPath({
    capability: repositoryCapture,
    path: transaction.root,
    identity: transaction.identity,
    disposition: 'recursive_directory',
  })
}

async function reopenFinalTree(input, transaction, products, composite) {
  const root = await lstat(transaction.root, { bigint: true })
  assert(root.dev.toString() === transaction.identity.dev &&
    root.ino.toString() === transaction.identity.ino,
  'phase-E transaction inode changed')
  await validateFinalEvidenceTree(transaction.root)
  await validateFinalEvidenceBindings({ outputDirectory: transaction.root,
    installedDoctorRecordFile: input.paths.installedDoctorRecordFile,
    expectedIndexes: products.indexes })
  const reopened = await readJSON0400(products.files.b1File, 'published B1')
  assert(canonicalJSONStringify(reopened.value) === canonicalJSONStringify(composite) &&
    reopened.value.digest === composite.digest, 'published B1 bytes drifted')
  const committed = await authenticateCommittedInputs(input, {
    identity: composite.tupleIdentity, environmentId: composite.environmentId,
    installId: composite.installId, generationId: composite.generationId,
  }, true)
  assertCommittedRawIndexes(committed, products.committedRawIndexes,
    'committed C/D evidence changed before E append')
  const installed = await validateInstalledEvidence(gateInputs(input, products.files))
  assert(canonicalJSONStringify(installed) === canonicalJSONStringify(products.installed),
    'reopened installed evidence drifted')
}

function committedRawIndexes(committed) {
  return Object.fromEntries(['cManifest', 'cRecord', 'cReceipt', 'dManifest', 'dRecord',
    'dReceipt', 'disclosureManifest'].map((name) => [name, committed[name].sha256]))
}

function assertCommittedRawIndexes(committed, expected, message) {
  plain(expected, 'committed raw evidence snapshot')
  const actual = committedRawIndexes(committed)
  assert(canonicalJSONStringify(actual) === canonicalJSONStringify(expected), message)
}

function gateInputs(input, files) {
  const p = input.paths
  return {
    privateEnvironmentMarkerFile: p.privateEnvironmentMarkerFile,
    doctorRecordFile: p.installedDoctorRecordFile,
    imageReuseRecordFile: files.imageReuseFile,
    sourceA3LedgerFile: p.sourceA3LedgerFile,
    profileJourneyDirectory: files.profileDirectory,
    recoveryRecordFile: files.recoveryFile,
    disclosureDirectory: files.disclosureDirectory,
    credentialCorpusFile: p.credentialCorpusFile,
    candidateManifestFile: p.candidateManifestFile,
    sourceManifestFile: p.sourceManifestFile, sourceBundleRoot: p.sourceBundleRoot,
    binaryFile: p.binaryFile, webDirectory: p.webDirectory,
    releaseSpecFile: p.releaseSpecFile, releaseManifestFile: p.releaseManifestFile,
    offlineBundleEvidenceFile: p.offlineBundleEvidenceFile,
    pathPiProvenanceFile: p.pathPiProvenanceFile,
    privatePiProvenanceFile: p.privatePiProvenanceFile,
    qualificationFile: p.qualificationFile,
    modelObservationFile: p.modelObservationFile,
    engineEndpointEvidenceFile: p.engineEndpointEvidenceFile,
  }
}

function profileEvidence(profile, receipt) {
  if (profile === 'minimal') return {
    runtimeAdapterId: 'pi', runtimeSource: 'managed_pi_image', executionProvider: 'docker',
    capabilityPolicy: 'chora.minimal.v1', managedSandbox: true, bashObserved: true,
    editObserved: true, prohibitedCapabilityDenialCount: 1, prohibitedCapabilityDenied: true,
    prohibitedCapabilityEvidenceSha256: receipt.profileEvidence.prohibitedCapabilityDenialDigest,
    attemptTerminalStatus: 'succeeded', independentVerification: true,
    independentVerificationEvidenceSha256: receipt.verifierSource.sliceDigest,
    managedFallback: false,
  }
  if (profile === 'standard') return {
    runtimeAdapterId: 'pi', runtimeSource: 'managed_pi_image', executionProvider: 'docker',
    capabilityPolicy: 'chora.standard.v1', fullRepository: true, securityPolicyEnforced: true,
    independentVerification: true, managedSandbox: true, taskOwnedApply: true,
  }
  return {
    runtimeAdapterId: 'pi', runtimeSource: 'local_pi', capabilityPolicy: 'pi.native',
    acknowledgementCurrent: true,
    acknowledgementPolicySha256: receipt.profileEvidence.acknowledgementPolicySha256,
    executionTarget: 'trusted-host', managedFallback: false, sandboxClaimed: false,
  }
}

function projectAttempt(attempt, sequence) {
  const { sourceAttemptOrdinal, profile, cohortSequence, scenario, attemptState, terminalReason,
    verifierRequired, verifierStartSequence, verifierEndSequence, verifierSliceDigest, ...rest } = attempt
  void sourceAttemptOrdinal; void profile; void cohortSequence; void scenario; void attemptState
  void terminalReason; void verifierRequired; void verifierStartSequence; void verifierEndSequence
  void verifierSliceDigest
  return { sequence, ...clone(rest) }
}

function phaseWindows(records, phase) {
  const result = []
  let current = []
  for (const record of records) {
    if (record.phase === phase) current.push(record)
    else if (current.length) { result.push({ records: current }); current = [] }
  }
  if (current.length) result.push({ records: current })
  return result
}

function rangeProjection(window) {
  assert(window.records.length > 0, 'A3 range is empty')
  return { startSequence: window.records[0].sequence,
    endSequence: window.records.at(-1).sequence,
    auditSliceDigest: computeAuditSliceDigest(window.records) }
}

function phaseBinding(manifest, record, receipt) {
  return {
    receiptRawSha256: receipt.sha256, receiptDigest: receipt.value.receiptDigest,
    manifestRawSha256: manifest.sha256,
    manifestDigest: manifest.value.manifestDigest ?? manifest.value.aggregateDigest,
    recordRawSha256: record.sha256,
    recordDigest: record.value.recordDigest ?? record.value.digest,
  }
}

function scan(bytes, secrets) {
  const text = bytes.toString('utf8')
  return {
    credentialMatches: secrets.reduce((count, secret) => count + countBuffer(bytes, secret), 0),
    genericSecretMatches: countPatterns(text, [
      /-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----/g,
      /\bsk-[A-Za-z0-9_-]{20,}\b/g, /\bgh[pousr]_[A-Za-z0-9]{30,}\b/g,
      /(?:password|token|cookie|api[_-]?key|authorization)\s*[:=]\s*["']?[^\s,"']{8,}/gi,
    ]),
    privatePathMatches: countPatterns(text, [/(?:\/Users\/|\/home\/)[^/\s]+/g, /[A-Za-z]:\\Users\\[^\\\s]+/g]),
    emailMatches: countPatterns(text, [/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/gi]),
    urlMatches: countPatterns(text, [/\bhttps?:\/\/[^\s"']+/gi]),
  }
}

function countBuffer(bytes, needle) { let count = 0; let offset = 0; while ((offset = bytes.indexOf(needle, offset)) !== -1) { count++; offset += Math.max(needle.length, 1) } return count }
function countPatterns(text, patterns) { return patterns.reduce((sum, pattern) => sum + [...text.matchAll(pattern)].length, 0) }

async function readExactJSONDirectory(directory, names, label, validator) {
  await exactDirectoryEntries(directory, [...names].sort(), label)
  const result = []
  for (let index = 0; index < names.length; index++) {
    const input = await readJSON0400(join(directory, names[index]), `${label} ${index + 1}`)
    result.push({ ...input, value: validator(input.value, index) })
  }
  return result
}

async function indexJSON(path, semanticDigest = undefined) {
  const input = await readJSON0400(path, `indexed ${basename(path)}`)
  return { file: basename(path), rawSha256: input.sha256,
    semanticDigest: semanticDigest ?? sha256(canonicalJSONStringify(input.value)) }
}

async function write0400JSON(path, value) {
  return write0400(path, Buffer.from(`${JSON.stringify(value)}\n`))
}

async function write0400(path, bytes) {
  await assertOwnerDirectory(dirname(path))
  let handle
  try {
    handle = await open(path, 'wx', 0o600)
    await handle.writeFile(bytes); await handle.sync(); await handle.chmod(0o400); await handle.sync()
    const opened = await handle.stat()
    assert(opened.isFile() && opened.nlink === 1 && (opened.mode & 0o777) === 0o400,
      'phase-E output is not one owner immutable file')
    await handle.close(); handle = undefined
  } catch (error) {
    if (handle) await handle.close().catch(() => {})
    throw error
  }
}

async function readJSON0400(path, label) { const bytes = await read0400(path, label, maxJSON); return { bytes, sha256: sha256(bytes), value: parseJSON(bytes, label) } }
async function readJSONAnyMode(path, label) { const bytes = await readOwnerFile(path, label, maxJSON, null); return { bytes, sha256: sha256(bytes), value: parseJSON(bytes, label) } }
async function read0400(path, label, max) { return readOwnerFile(path, label, max, 0o400) }
async function readOwnerFile(path, label, max, mode) {
  exactAbsolute(path, label); await assertNoSymlinkAncestors(path)
  const before = await lstat(path)
  assert(before.isFile() && !before.isSymbolicLink() && before.nlink === 1 &&
    (uid === null || before.uid === uid) && before.size > 0 && before.size <= max &&
    (mode === null || (before.mode & 0o777) === mode), `${label} is not an exact owner file`)
  const handle = await open(path, 'r')
  try {
    const opened = await handle.stat()
    assert(opened.dev === before.dev && opened.ino === before.ino, `${label} inode changed before open`)
    const bytes = await handle.readFile()
    const after = await lstat(path)
    assert(after.dev === opened.dev && after.ino === opened.ino && after.size === opened.size,
      `${label} inode changed while reading`)
    return bytes
  } finally { await handle.close() }
}

async function assert0400(path, label) { await read0400(path, label, 128 * 1024 * 1024) }
async function exactDirectoryEntries(path, names, label) { await assertOwnerDirectory(path); const actual = (await readdir(path)).sort(); assert(JSON.stringify(actual) === JSON.stringify([...names].sort()), `${label} entries drifted`) }
async function assertOwnerDirectory(path) { exactAbsolute(path, 'owner directory'); await assertNoSymlinkAncestors(path); const info = await lstat(path); assert(info.isDirectory() && !info.isSymbolicLink() && (uid === null || info.uid === uid) && (info.mode & 0o777) === 0o700, 'directory is not owner 0700'); return info }
async function assertNoSymlinkAncestors(path) { let current = path; while (true) { try { const info = await lstat(current); assert(!info.isSymbolicLink(), 'path has symlink ancestor') } catch (error) { if (error.code !== 'ENOENT') throw error } const parent = dirname(current); if (parent === current) break; current = parent } }
async function absent(path, label) { try { await lstat(path); throw new Error(`${label} already exists`) } catch (error) { if (error.message === `${label} already exists`) throw error; if (error.code !== 'ENOENT') throw error } }

function exactAbsolute(path, label) { assert(typeof path === 'string' && isAbsolute(path) && normalize(path) === path && resolve(path) === path && !path.includes('\0'), `${label} is not an exact absolute path`); return path }
function within(parent, child) { const value = relative(parent, child); return value !== '' && value !== '..' && !value.startsWith(`..${sep}`) && !isAbsolute(value) }
function parseJSON(bytes, label) { try { return JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) } }
function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function digest(value, label) { assert(typeof value === 'string' && digestRE.test(value), `${label} is invalid`) }
function plain(value, label) { assert(value && typeof value === 'object' && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype, `${label} is not a plain object`) }
function exactKeys(value, keys, label) { plain(value, label); assert(JSON.stringify(Object.keys(value).sort()) === JSON.stringify([...keys].sort()), `${label} keys drifted`) }
function assert(condition, message) { if (!condition) throw new Error(message) }
function clone(value) { return structuredClone(value) }
