import { createHash } from 'node:crypto'
import {
  lstat, mkdir, open, readFile, readdir, stat,
} from 'node:fs/promises'
import { basename, dirname, isAbsolute, join, normalize, relative, resolve, sep } from 'node:path'
import { materializeInstalledDoctorEvidence, validateInstalledDoctorEvidenceFiles } from './o4-installed-doctor-record.mjs'
import {
  inspectRepositoryAuthority, validateRepositoryAuthoritySnapshot,
  verifyRepositoryAuthority, verifyRestartRepositoryAuthority,
} from './o4-repository-authority.mjs'
import {
  cleanupOwnedPath, ownedPathIdentity, writeReadonlyOwnedPath,
} from './o4-owned-path-cleanup.mjs'

export const CANDIDATE_TUPLE_SCHEMA = 'chora.m1-o4-candidate-install-tuple.v2'
export const PHASE_RECEIPT_SCHEMA = 'chora.m1-o4-candidate-phase-receipt.v1'
export const RESTART_RECEIPT_SCHEMA = 'chora.m1-o4-candidate-restart-receipt.v1'
export const HANDOFF_SCHEMA = 'chora.m1-o4-candidate-install-handoff.v1'
export const MODEL_AUTHORITY_SCHEMA = 'chora.m1-o4-model-request-authority.v2'
export const ENGINE_PREFLIGHT_SCHEMA = 'chora.m1-o4-engine-preflight.v3'
export const O4_FAULT_AUTHORITY_SCHEMA = 'chora.m1-o4-fault-authority.v1'

const SOURCE_SCHEMA = 'chora.local-alpha-source-bundle.v1'
const INSTALLATION_SCHEMA = 'chora.local-alpha-installation.v1'
const DATA_INSTALLATION_SCHEMA = 'chora.local-alpha-data.v1'
const CANDIDATE_SCHEMA = 'chora.m1-o4-candidate-manifest.v1'
const RELEASE_SPEC_SCHEMA = 'chora.m1-o4-release-spec.v1'
const RELEASE_MANIFEST_SCHEMA = 'chora.m1-o4-release-manifest.v1'
const OFFLINE_BUNDLE_SCHEMA = 'chora.m1-o4-offline-release-bundle-evidence.v1'
const PI_SCHEMA = 'chora.m1-o4-pi-provenance.v1'
const ENDPOINT_SCHEMA = 'chora.m1-o4-engine-endpoint-evidence.v1'
const O4_ROLES = Object.freeze([
  'managed_pi_runtime', 'network_boundary', 'independent_verifier', 'capability_probe',
])
const MAX_JSON = 2 * 1024 * 1024
const MAX_SOURCE_MANIFEST = 16 * 1024 * 1024
const MAX_BINARY = 256 * 1024 * 1024
const MAX_OUTPUT = 64 * 1024
const digestRE = /^[0-9a-f]{64}$/
const imageDigestRE = /^sha256:[0-9a-f]{64}$/
const opaqueIDRE = /^(?:env|ins|gen)_[A-Za-z0-9_-]{24,80}$/
const productIDRE = /^[a-z0-9][a-z0-9._-]{0,127}$/
const releaseIdentifierRE = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/
const ociPullRE = /^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:\/[a-z0-9]+(?:[._-][a-z0-9]+)*)*@sha256:[a-f0-9]{64}$/
const versionRE = /^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$/
const phases = Object.freeze(['preflight', 'install', 'product_doctor', 'setup', 'doctor', 'serve', 'C', 'D', 'E'])
const PHASE_APPEND_LOCK = '.phase-append.lock'
const O4_AUTHORITY_STATUS = 'authorized'
const O4_AUTHORITY_SCOPE = 'o4_acceptance_only'
const O4_POLICY_SHA256 = 'efe8918d0c9c4232c8292941f573fe386d93a3faa051329c883c9b8349b66f04'
const O4_CLAIM_BOUNDARY = Object.freeze([
  'typed_failure_projection', 'typed_timeout_projection', 'process_tree_death',
  'no_patch', 'no_automatic_retry', 'resource_cleanup',
])
const O4_SCENARIOS = Object.freeze([
  Object.freeze({ scenario: 'failure', action: 'force_exact_managed_attempt_nonzero_after_started_v1' }),
  Object.freeze({ scenario: 'timeout', action: 'accelerated_managed_attempt_deadline_v1' }),
])
const PRODUCT_HOST_PLATFORM = Object.freeze({ os: 'darwin', architecture: 'arm64' })
const ENGINE_PLATFORM = Object.freeze({ operatingSystem: 'linux', architecture: 'arm64' })
const RELEASE_IMAGE_PLATFORM = Object.freeze({ os: 'linux', architecture: 'arm64' })
const uuidV7 = '[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}'
const taskIDRE = new RegExp(`^task_${uuidV7}$`)
const snapshotIDRE = new RegExp(`^context_snapshot_${uuidV7}$`)

const configKeys = ['identity', 'paths', 'authority', 'private', 'setup']
const identityKeys = ['environmentId', 'installId', 'generationId', 'platform']
const pathKeys = [
  'sourceBundleRoot', 'sourceManifestFile', 'candidateManifestFile', 'binaryFile', 'webDirectory',
  'releaseSpecFile', 'releaseManifestFile', 'offlineBundleEvidenceFile', 'pathPiProvenanceFile',
  'privatePiProvenanceFile', 'modelAuthorityFile', 'qualificationFile', 'modelObservationFile',
  'installedDoctorReportFile', 'endpointEvidenceFile',
  'preflightFile', 'dockerCLIFile', 'installRoot', 'installedBinaryFile', 'installedWebDirectory',
  'installedSourceRoot', 'installedSourceManifestFile', 'dataRoot', 'databaseFile', 'repositoryRoot',
  'authFile', 'caFile', 'stateRoot', 'ledgerFile', 'receiptDirectory', 'privatePiRoot',
  'privatePiSourceRoot', 'privatePiManifestFile', 'actualReleaseRoot', 'actualReleaseManifestFile',
  'probeRuntimeRoot', 'colimaToolRoot', 'colimaSourceFile', 'dockerClientSourceFile', 'tupleFile',
]
const authorityKeys = [
  'sourceManifestSha256', 'sourceAggregateSha256', 'candidateManifestSha256', 'binarySha256',
  'webAggregateSha256', 'releaseSpecSha256', 'releaseManifestSha256', 'offlineBundleEvidenceSha256',
  'pathPiProvenanceSha256', 'privatePiProvenanceSha256', 'modelAuthoritySha256',
  'endpointEvidenceSha256', 'dockerCLISha256', 'limaPrefixClosureSha256', 'limaInfoDigest',
  'limaSha256', 'diskImageSha256', 'diskImageSize',
  'endpointSocketPathSha256', 'sandboxExecutableSha256', 'sandboxProfileSha256',
  'actualReleaseManifestSha256', 'privatePiManifestSha256', 'authFileSha256', 'caFileSha256',
  'colimaSourceSha256', 'dockerClientSourceSha256', 'repositoryCommit', 'repositoryClosureSha256',
  'dockerContext', 'endpointDigest', 'roles',
]
const setupKeys = ['key', 'grant', 'actor', 'session', 'releaseId', 'releaseManifestRelativePath']
const privateKeys = ['proxyURL', 'modelURL', 'port', 'colimaProfile']
const o4EvidenceAuthorityKeys = [
  'sessionAuthorityFile', 'sessionAuthoritySha256', 'tupleIdentity',
  'sourceA3LedgerFile', 'verifierLedgerFile', 'cResourceDirectory', 'dResourceDirectory',
]
const candidateTupleKeys = [
  'schemaVersion', 'status', ...identityKeys, 'roots', 'repositoryAuthority',
  'bindings', 'plan', 'identity',
]
const candidateTupleRootKeys = [
  'installRootSha256', 'dataRootSha256', 'stateRootSha256', 'receiptDirectorySha256',
  'privatePiRootSha256', 'probeRuntimeRootSha256', 'colimaToolRootSha256',
]
const candidateTupleBindingKeys = [
  'sourceManifestSha256', 'sourceAggregateSha256', 'candidateManifestSha256',
  'binarySha256', 'webAggregateSha256', 'releaseSpecSha256', 'releaseManifestSha256',
  'offlineBundleEvidenceSha256', 'pathPiProvenanceSha256', 'privatePiProvenanceSha256',
  'modelAuthoritySha256', 'endpointEvidenceSha256', 'preflightFileSha256',
  'preflightDigest', 'actualReleaseManifestSha256', 'privatePiManifestSha256',
  'authFileSha256', 'caFileSha256', 'colimaSourceSha256', 'dockerClientSourceSha256',
  'repositoryCommit', 'repositoryClosureSha256', 'dockerCLISha256',
  'dockerContextSha256', 'endpointDigest', 'roleTupleSha256',
  'privateRuntimeConfigSha256',
]
const candidateTuplePlan = Object.freeze([
  'offline-prebuilt-install', 'product-doctor', 'setup-once', 'installed-doctor',
  'process-boundary', 'active-serve',
])

export async function inspectAuthenticatedCandidate(config, options = undefined) {
  const repositoryCapture = validateRepositoryCapture(options?.repositoryCapture)
  validateConfigShape(config)
  await validatePathTopology(config.paths)
  const frozenRepositoryAuthority = options?.frozenRepositoryAuthority
  const repositorySnapshot = frozenRepositoryAuthority === undefined ? null :
    validateRepositoryAuthoritySnapshot(frozenRepositoryAuthority, {
      repositoryRoot: config.paths.repositoryRoot,
      repositoryCommit: config.authority.repositoryCommit,
      repositoryClosureSha256: config.authority.repositoryClosureSha256,
    })
  const repositoryAuthority = repositorySnapshot === null ?
    await inspectRepositoryAuthority({ repositoryRoot: config.paths.repositoryRoot,
      expectedCommit: config.authority.repositoryCommit,
      expectedClosureSha256: config.authority.repositoryClosureSha256,
      capture: repositoryCapture }) :
    Object.freeze({ repositoryRoot: config.paths.repositoryRoot,
      repositoryCommit: config.authority.repositoryCommit,
      repositoryClosureSha256: config.authority.repositoryClosureSha256,
      snapshot: repositorySnapshot })
  const allowDynamicEvidence = options?.allowDynamicEvidence === true
  const inputs = await readAuthorityInputs(config, allowDynamicEvidence, true)
  validateAuthorityDocuments(config, inputs, true)
  const tuple = buildTuple(config, inputs, repositoryAuthority.snapshot)
  return Object.freeze({
    tuple: deepFreeze(tuple),
    tupleIdentity: tuple.identity,
    inputSnapshot: Object.freeze(inputs.snapshot),
    sourceManifest: deepFreeze(clone(inputs.sourceManifest.value)),
    preflightAuthority: deepFreeze(clone(inputs.preflight.value)),
    modelAuthority: deepFreeze(clone(inputs.model.value)),
    installedDoctorReport: inputs.dynamicEvidence === null ? null :
      deepFreeze(clone(inputs.dynamicEvidence.installedDoctorReport)),
    repositoryAuthority,
  })
}

export async function inspectStaticCandidate(config, options = undefined) {
  const repositoryCapture = validateRepositoryCapture(options?.repositoryCapture)
  validateConfigShape(config)
  await validatePathTopology(config.paths)
  const repositoryAuthority = await inspectRepositoryAuthority({
    repositoryRoot: config.paths.repositoryRoot,
    expectedCommit: config.authority.repositoryCommit,
    expectedClosureSha256: config.authority.repositoryClosureSha256,
    capture: repositoryCapture,
  })
  const inputs = await readAuthorityInputs(config, false, false)
  validateAuthorityDocuments(config, inputs, false)
  return Object.freeze({ identity: deepFreeze(clone(config.identity)),
    inputSnapshot: Object.freeze(inputs.snapshot), modelAuthority: deepFreeze(clone(inputs.model.value)),
    repositoryAuthority })
}

export async function executeCandidateInstall(config, dependencies) {
  const repositoryCapture = validateRepositoryCapture(dependencies?.repositoryCapture)
  const markerWriter = validateMarkerWriterDependencies(dependencies?.markerWriter)
  const runner = validateRunner(dependencies?.runner)
  const inspected = await inspectAuthenticatedCandidate(config, { repositoryCapture })
  const evidenceAuthorityInput = validateO4EvidenceAuthority(
    dependencies?.o4EvidenceAuthority, inspected.tupleIdentity)

  // A stopped Engine is observed through the immutable candidate before any
  // tuple/install/state/ledger/root mutation. The local observation is only
  // compared with the already-authenticated preflight authority.
  await rehashInputs(inspected.inputSnapshot)
  const preflightSpec = productDoctorSpec(config.paths.binaryFile, config)
  const preflightResult = await executeJSON(runner, preflightSpec, 'candidate Engine Doctor')
  assertPreflightObservation(preflightResult, config, inspected.preflightAuthority)

  await freezeTuple(inspected.tuple, config.paths.tupleFile, repositoryCapture, markerWriter)
  const receipts = []
  receipts.push(await appendReceipt(config.paths.tupleFile, config.paths.receiptDirectory, {
    phase: 'preflight', sequence: 1, previousReceiptDigest: null,
    operationDigest: commandDigest(preflightSpec), observationDigest: sha256(canonicalJSONStringify(preflightResult)),
  }, repositoryCapture, markerWriter))

  await rehashInputs(inspected.inputSnapshot)
  const installSpec = Object.freeze({
    sourceBundleRoot: config.paths.sourceBundleRoot, sourceManifestFile: config.paths.sourceManifestFile,
    sourceManifestSha256: config.authority.sourceManifestSha256,
    sourceAggregateSha256: config.authority.sourceAggregateSha256,
    sourceFiles: inspected.sourceManifest.files.length,
    sourceBinary: config.paths.binaryFile, sourceWebDirectory: config.paths.webDirectory,
    installRoot: config.paths.installRoot, installedBinaryFile: config.paths.installedBinaryFile,
    installedWebDirectory: config.paths.installedWebDirectory, dataRoot: config.paths.dataRoot,
    shell: false,
  })
  await runner.installImmutable(installSpec)
  await validateInstalledTopology(config, inspected.sourceManifest)
  receipts.push(await nextReceipt(config, receipts, 'install',
    sha256(canonicalJSONStringify(publicInstallSpec(installSpec))), null,
    repositoryCapture, markerWriter))

  await rehashInputs(inspected.inputSnapshot)
  const productDoctor = productDoctorSpec(config.paths.installedBinaryFile, config)
  const productDoctorResult = await executeJSON(runner, productDoctor, 'installed Engine Doctor')
  assertPreflightObservation(productDoctorResult, config, inspected.preflightAuthority)
  receipts.push(await nextReceipt(config, receipts, 'product_doctor', commandDigest(productDoctor),
    sha256(canonicalJSONStringify(productDoctorResult)), repositoryCapture, markerWriter))

  await rehashInputs(inspected.inputSnapshot)
  const setup = setupSpec(config)
  assertSetupCommand(setup)
  const setupResult = await executeJSON(runner, setup, 'Setup')
  assertPassedJSON(setupResult, 'Setup')
  const setupReceipt = await nextReceipt(config, receipts, 'setup', commandDigest(setup),
    sha256(canonicalJSONStringify(setupResult)), repositoryCapture, markerWriter)
  receipts.push(setupReceipt)
  const setupReceiptPath = join(config.paths.receiptDirectory, receiptName(setupReceipt.sequence, setupReceipt.phase))
  const setupReceiptFileSha256 = sha256(await readExactFile(setupReceiptPath,
    'exact Setup receipt', MAX_JSON, { mode: 0o400 }))

  await rehashInputs(inspected.inputSnapshot)
  const doctorSpec = installedDoctorSpec(config, { path: setupReceiptPath, sha256: setupReceiptFileSha256 })
  const productProof = await executeJSON(runner, doctorSpec, 'installed Doctor')
  const dynamic = await materializeInstalledDoctorEvidence({
    repositoryCapture,
    productProof,
    identity: { environmentId: config.identity.environmentId, installId: config.identity.installId,
      generationId: config.identity.generationId },
    platform: config.identity.platform,
    bindings: installedBindings(config),
    setupReceiptSha256: setupReceiptFileSha256,
    modelRequestAuthoritySha256: config.authority.modelAuthoritySha256,
    modelRequestAuthority: inspected.modelAuthority,
    releaseId: config.setup.releaseId,
    manifestSha256: config.authority.actualReleaseManifestSha256,
    endpointDigest: config.authority.endpointDigest,
    outputs: {
      engineQualificationFile: config.paths.qualificationFile,
      modelObservationFile: config.paths.modelObservationFile,
      installedDoctorReportFile: config.paths.installedDoctorReportFile,
    },
  })
  receipts.push(await nextReceipt(config, receipts, 'doctor', commandDigest(doctorSpec),
    sha256(canonicalJSONStringify(productProof)), repositoryCapture, markerWriter))

  await rehashInputs(inspected.inputSnapshot)
  await runner.processBoundary()
  const evidenceAuthority = await bindO4InstalledDoctorEvidence(
    evidenceAuthorityInput, config.paths.installedDoctorReportFile)
  const serve = serveSpec(config, null, dynamic.installedDoctorReport.inputFingerprint,
    evidenceAuthority)
  await verifyRepositoryAuthority({ ...inspected.repositoryAuthority, capture: repositoryCapture })
  const service = await runner.start(serve)
  assert(service && service.active === true, 'serve did not cross into an active process')
  receipts.push(await nextReceipt(config, receipts, 'serve', commandDigest(serve),
    sha256(canonicalJSONStringify({ active: true })), repositoryCapture, markerWriter))

  return Object.freeze({ service, handoff: await buildHandoff(
    config.paths.tupleFile, config.paths.receiptDirectory, { repositoryCapture }) })
}

export async function restartCandidate(config, dependencies) {
  const repositoryCapture = validateRepositoryCapture(dependencies?.repositoryCapture)
  const markerWriter = validateMarkerWriterDependencies(dependencies?.markerWriter)
  const runner = validateRunner(dependencies?.runner)
  const frozen = await readAndValidateTuple(config.paths.tupleFile)
  const inspected = await inspectAuthenticatedCandidate(config, {
    allowDynamicEvidence: true, frozenRepositoryAuthority: frozen.repositoryAuthority,
    repositoryCapture,
  })
  assert(frozen.identity === inspected.tupleIdentity, 'restart tuple drifted')
  const evidenceAuthorityInput = validateO4EvidenceAuthority(
    dependencies?.o4EvidenceAuthority, frozen.identity)
  await rehashInputs(inspected.inputSnapshot)
  const result = await withPhaseAppendLock(
    config.paths.tupleFile, config.paths.receiptDirectory, repositoryCapture, async () => {
    const receipts = await readReceiptChain(config.paths.tupleFile, config.paths.receiptDirectory)
    assert(receipts.filter((item) => item.phase === 'setup').length === 1,
      'restart requires the exact one-Setup phase prefix')
    const lastPhase = receipts.at(-1)?.phase
    const authority = dependencies?.o4Authority === undefined ? null :
      await readAndValidateO4Authority(config, frozen, dependencies.o4Authority)
    if (authority === null) {
      assert(['setup', 'serve', 'C'].includes(lastPhase),
        'ordinary restart is unavailable outside the allowed pre-D phase states')
    } else {
      assert(lastPhase === 'C', 'O4-authorized restart requires exact phase C and is forbidden after D/E')
    }
    await runner.processBoundary()
    const evidenceAuthority = await bindO4InstalledDoctorEvidence(
      evidenceAuthorityInput, config.paths.installedDoctorReportFile)
    const serve = serveSpec(config, authority, inspected.installedDoctorReport.inputFingerprint,
      evidenceAuthority)
    await verifyRestartRepositoryAuthority({
      repositoryRoot: config.paths.repositoryRoot,
      repositoryCommit: config.authority.repositoryCommit,
      repositoryClosureSha256: config.authority.repositoryClosureSha256,
      snapshot: frozen.repositoryAuthority,
      capture: repositoryCapture,
    })
    const service = await runner.start(serve)
    assert(service && service.active === true, 'restart did not produce an active process')
    const restartReceipt = await appendRestartReceiptLocked(config.paths.tupleFile,
      config.paths.receiptDirectory, commandDigest(serve),
      sha256(canonicalJSONStringify({ active: true })), repositoryCapture, markerWriter)
    return { service, restartReceipt }
  })
  return Object.freeze({ ...result, handoff: await buildHandoff(
    config.paths.tupleFile, config.paths.receiptDirectory, { repositoryCapture }) })
}

export async function appendHandoffReceipt(
  { tupleFile, receiptDirectory, phase, operationDigest, observationDigest }, dependencies = undefined,
) {
  const repositoryCapture = validateRepositoryCapture(dependencies?.repositoryCapture)
  const markerWriter = validateMarkerWriterDependencies(dependencies?.markerWriter)
  assert(phase === 'C' || phase === 'D', 'only later C/D phases may use the public handoff appender')
  digest(operationDigest, 'handoff operation digest')
  digest(observationDigest, 'handoff observation digest')
  return withPhaseAppendLock(tupleFile, receiptDirectory, repositoryCapture, async () => {
    const chain = await readReceiptChain(tupleFile, receiptDirectory)
    assert(chain.length > 0 && chain.at(-1).phase !== 'E', 'receipt chain is empty or already finalized')
    return appendReceiptLocked(tupleFile, receiptDirectory, {
      phase, sequence: chain.length + 1, previousReceiptDigest: chain.at(-1).receiptDigest,
      operationDigest, observationDigest,
    }, repositoryCapture, markerWriter)
  })
}

export async function commitHandoffPhaseC(
  { tupleFile, receiptDirectory, operationDigest, observationDigest }, dependencies = undefined,
) {
  const repositoryCapture = validateRepositoryCapture(dependencies?.repositoryCapture)
  const markerWriter = validateMarkerWriterDependencies(dependencies?.markerWriter)
  const prepareCommit = dependencies?.prepareCommit
  const beforeReceiptPublish = dependencies?.beforeReceiptPublish ?? null
  const receiptWriteStep = dependencies?.receiptWriteStep ?? null
  assert(typeof prepareCommit === 'function', 'phase C prepare callback is missing')
  assert(beforeReceiptPublish === null || typeof beforeReceiptPublish === 'function',
    'phase C before-receipt callback is invalid')
  assert(receiptWriteStep === null || typeof receiptWriteStep === 'function',
    'phase C receipt write-step callback is invalid')
  digest(operationDigest, 'handoff operation digest')
  digest(observationDigest, 'handoff observation digest')
  return withPhaseAppendLock(tupleFile, receiptDirectory, repositoryCapture, async () => {
    const chain = await readReceiptChain(tupleFile, receiptDirectory)
    assert(chain.length > 0 && chain.at(-1).phase !== 'E',
      'receipt chain is empty or already finalized')
    const handoffReceipt = await prepareReceiptLocked(tupleFile, receiptDirectory, {
      phase: 'C', sequence: chain.length + 1,
      previousReceiptDigest: chain.at(-1).receiptDigest,
      operationDigest, observationDigest,
    })
    const prepared = await prepareCommit(Object.freeze({ handoffReceipt }))
    try {
      if (beforeReceiptPublish !== null) {
        await beforeReceiptPublish(Object.freeze({ handoffReceipt, prepared }))
      }
      const published = await publishPreparedReceiptLocked(
        tupleFile, receiptDirectory, handoffReceipt, repositoryCapture,
        withMarkerBeforeSpawn(markerWriter, receiptWriteStep))
      return Object.freeze({ handoffReceipt: published, prepared })
    } catch (error) {
      throw new Error(
        'phase C record preparation completed but commit receipt publication failed; prepared output remains uncommitted',
        { cause: error },
      )
    }
  })
}

export async function finalizeEvidencePhaseE(options, dependencies = undefined) {
  const repositoryCapture = validateRepositoryCapture(dependencies?.repositoryCapture)
  const markerWriter = validateMarkerWriterDependencies(dependencies?.markerWriter)
  exactKeys(options, [
    'tupleFile', 'receiptDirectory', 'closeAllServices', 'validateEvidence',
    'reopenAndSealA3', 'formCompositeB1',
  ], 'E finalization')
  for (const name of ['closeAllServices', 'validateEvidence', 'reopenAndSealA3', 'formCompositeB1']) {
    assert(typeof options[name] === 'function', `E finalization ${name} is missing`)
  }
  // Reject a self-consistently rehashed unsupported product-host tuple without
  // creating the phase lock or invoking any closure/evidence callback. The
  // tuple is authenticated again inside the lock to preserve the final gate.
  await readAndValidateTuple(options.tupleFile)
  return withPhaseAppendLock(
    options.tupleFile, options.receiptDirectory, repositoryCapture, async () => {
    const tuple = await readAndValidateTuple(options.tupleFile)
    const before = await readReceiptChain(options.tupleFile, options.receiptDirectory)
    assert(before.at(-1)?.phase === 'D', 'E requires the exact phase-D prefix')
    await options.closeAllServices()
    const sealedA3 = await options.reopenAndSealA3(tuple)
    assertFinalIdentityBinding(sealedA3, tuple, 'sealed A3')
    assert(sealedA3.sealed === true && digestRE.test(sealedA3.sha256),
      'E did not seal an exact A3 ledger')
    const composite = await options.formCompositeB1({ tuple, sealedA3 })
    assert(composite?.schemaVersion === 'chora.m1-o4-b1-composite.v1',
      'raw A3 JSON is not the B1 composite projection')
    assertFinalIdentityBinding(composite, tuple, 'B1 composite')
    assert(composite.sourceA3LedgerSha256 === sealedA3.sha256,
      'B1 composite is not bound to the exact sealed A3 ledger')
    const observationDigest = sha256(canonicalJSONStringify(composite))
    const validation = await options.validateEvidence({ tuple, sealedA3, composite })
    assertFinalValidation(validation, tuple, sealedA3.sha256, observationDigest)
    return appendReceiptLocked(options.tupleFile, options.receiptDirectory, {
      phase: 'E', sequence: before.length + 1, previousReceiptDigest: before.at(-1).receiptDigest,
      operationDigest: sealedA3.sha256, observationDigest,
    }, repositoryCapture, markerWriter)
  })
}

export async function buildO4FaultAuthority({ config, authorityPath, scenarioBindings }, dependencies = undefined) {
  const repositoryCapture = validateRepositoryCapture(dependencies?.repositoryCapture)
  validateConfigShape(config)
  absolute(authorityPath, 'O4 fault authority path')
  assert(within(config.paths.dataRoot, authorityPath), 'O4 fault authority must be inside the exact data root')
  validateO4ScenarioBindings(scenarioBindings)
  const durability = authorityDurabilityDependencies(dependencies)
  const inspected = await inspectAuthenticatedCandidate(config, {
    allowDynamicEvidence: true, repositoryCapture,
  })
  const tuple = await readAndValidateTuple(config.paths.tupleFile)
  assert(tuple.identity === inspected.tupleIdentity, 'O4 authority tuple/config drifted')
  await rehashInputs(inspected.inputSnapshot)
  const binary = await readExactFile(config.paths.installedBinaryFile, 'installed O4 binary', MAX_BINARY)
  assert(sha256(binary) === tuple.bindings.binarySha256, 'installed O4 binary identity drifted')
  await ensureOwnerDirectory(config.paths.dataRoot)
  return withPhaseAppendLock(
    config.paths.tupleFile, config.paths.receiptDirectory, repositoryCapture, async () => {
    const chain = await readReceiptChain(config.paths.tupleFile, config.paths.receiptDirectory)
    assert(chain.at(-1)?.phase === 'C', 'O4 fault authority requires exact phase C')
    const document = o4AuthorityDocument(config, tuple, scenarioBindings, binary)
    const bytes = Buffer.from(`${JSON.stringify(document)}\n`)
    await writeDurableAuthority(bytes, authorityPath, config.paths.dataRoot, durability)
    return deepFreeze({
      authorityPath, authoritySha256: sha256(bytes), candidateTuple: tuple.identity,
      document: clone(document),
    })
  })
}

export async function buildHandoff(tupleFile, receiptDirectory, dependencies = undefined) {
  const repositoryCapture = validateRepositoryCapture(dependencies?.repositoryCapture)
  return withPhaseAppendLock(tupleFile, receiptDirectory, repositoryCapture,
    () => buildHandoffLocked(tupleFile, receiptDirectory))
}

async function buildHandoffLocked(tupleFile, receiptDirectory) {
  const tuple = await readAndValidateTuple(tupleFile)
  const receipts = await readReceiptChain(tupleFile, receiptDirectory)
  const restarts = await readRestartReceipts(tupleFile, receiptDirectory)
  const safe = {
    schemaVersion: HANDOFF_SCHEMA, tupleIdentity: tuple.identity,
    receiptDigests: receipts.map(({ receiptDigest }) => receiptDigest),
    restartReceiptDigests: restarts.map(({ receiptDigest }) => receiptDigest),
    allowedNextPhases: receipts.length === phases.length ? [] : [phases[receipts.length]],
    a3SealAuthority: 'E-only-after-all-service-closes',
    rawA3IsB1Composite: false,
    downstreamSourceManifestFact: 'B1 validator repair is required because it rejects the authenticated full production source manifest; normalization or splicing is forbidden',
  }
  return deepFreeze({ ...safe, digest: sha256(canonicalJSONStringify(safe)) })
}

async function readAuthorityInputs(config, allowDynamicEvidence, requirePreflight) {
  const p = config.paths
  const files = {
    sourceManifest: [p.sourceManifestFile, MAX_SOURCE_MANIFEST], candidateManifest: [p.candidateManifestFile, MAX_JSON],
    binary: [p.binaryFile, MAX_BINARY], releaseSpec: [p.releaseSpecFile, MAX_JSON],
    releaseManifest: [p.releaseManifestFile, MAX_JSON], offlineBundle: [p.offlineBundleEvidenceFile, MAX_JSON],
    pathPi: [p.pathPiProvenanceFile, MAX_JSON], privatePi: [p.privatePiProvenanceFile, MAX_JSON],
    model: [p.modelAuthorityFile, MAX_JSON],
    endpoint: [p.endpointEvidenceFile, MAX_JSON],
    dockerCLI: [p.dockerCLIFile, MAX_BINARY],
    actualRelease: [p.actualReleaseManifestFile, 4 * 1024 * 1024],
    privatePiManifest: [p.privatePiManifestFile, 16 * 1024 * 1024],
    authFile: [p.authFile, MAX_JSON], caFile: [p.caFile, MAX_JSON],
    colimaSource: [p.colimaSourceFile, MAX_BINARY], dockerClientSource: [p.dockerClientSourceFile, MAX_BINARY],
  }
  if (requirePreflight) files.preflight = [p.preflightFile, MAX_JSON]
  else await assertAbsent(p.preflightFile, 'pre-bootstrap Engine preflight')
  const values = {}
  const snapshot = {}
  const dynamicPaths = [p.qualificationFile, p.modelObservationFile, p.installedDoctorReportFile]
  let dynamicEvidence = null
  if (allowDynamicEvidence) {
    dynamicEvidence = await validateInstalledDoctorEvidenceFiles({ engineQualificationFile: p.qualificationFile,
      modelObservationFile: p.modelObservationFile, installedDoctorReportFile: p.installedDoctorReportFile })
    for (const path of dynamicPaths) snapshot[path] = sha256(await readExactFile(path, 'dynamic evidence', MAX_JSON, { mode: 0o400 }))
  } else {
    for (const path of dynamicPaths) await assertAbsent(path, 'pre-Setup dynamic evidence')
  }
  for (const [name, [path, max]] of Object.entries(files)) {
    const bytes = await readExactFile(path, name, max,
      ['model', 'preflight'].includes(name) ? { mode: 0o400 } : undefined)
    const hash = sha256(bytes)
    snapshot[path] = hash
    values[name] = ['binary', 'dockerCLI', 'authFile', 'caFile', 'colimaSource', 'dockerClientSource'].includes(name) ? { bytes, sha256: hash } :
      { value: parseJSON(bytes, name), sha256: hash }
  }
  const sourceTree = await inspectSourceTree(p.sourceBundleRoot, values.sourceManifest.value)
  const web = await directoryAggregateWithSnapshot(p.webDirectory)
  const privatePiTree = await inspectPrivatePiTree(p.privatePiSourceRoot, values.privatePiManifest.value)
  const releaseTree = await inspectActualReleaseTree(p.actualReleaseRoot, values.actualRelease.value)
  Object.assign(snapshot, sourceTree.snapshot, web.snapshot, privatePiTree.snapshot, releaseTree.snapshot)
  return { ...values, sourceTree, webAggregate: web.aggregate, dynamicEvidence, snapshot }
}

function validateAuthorityDocuments(config, input, requirePreflight) {
  const { identity, authority: a } = config
  const digestBindings = {
    sourceManifestSha256: input.sourceManifest.sha256, candidateManifestSha256: input.candidateManifest.sha256,
    binarySha256: input.binary.sha256, webAggregateSha256: input.webAggregate,
    releaseSpecSha256: input.releaseSpec.sha256, releaseManifestSha256: input.releaseManifest.sha256,
    offlineBundleEvidenceSha256: input.offlineBundle.sha256, pathPiProvenanceSha256: input.pathPi.sha256,
    privatePiProvenanceSha256: input.privatePi.sha256, modelAuthoritySha256: input.model.sha256,
    endpointEvidenceSha256: input.endpoint.sha256, dockerCLISha256: input.dockerCLI.sha256,
    actualReleaseManifestSha256: input.actualRelease.sha256, privatePiManifestSha256: input.privatePiManifest.sha256,
    authFileSha256: input.authFile.sha256, caFileSha256: input.caFile.sha256,
    colimaSourceSha256: input.colimaSource.sha256, dockerClientSourceSha256: input.dockerClientSource.sha256,
  }
  for (const [key, actual] of Object.entries(digestBindings)) assert(a[key] === actual, `${key} drifted`)
  assert(input.dockerCLI.sha256 === input.dockerClientSource.sha256 && input.dockerCLI.bytes.equals(input.dockerClientSource.bytes),
    'Docker CLI/source identity drifted')
  assert(a.sourceAggregateSha256 === input.sourceTree.aggregate, 'sourceAggregateSha256 drifted')

  validateSourceManifest(input.sourceManifest.value, input.sourceTree)
  validateRoles(a.roles)
  validateCandidate(input.candidateManifest.value, config)
  validateRelease(input.releaseSpec.value, input.releaseManifest.value, input.offlineBundle.value, config)
  validatePi(input.pathPi.value, 'path', identity.generationId)
  validatePi(input.privatePi.value, 'private', identity.generationId)
  assert(a.pathPiProvenanceSha256 !== a.privatePiProvenanceSha256, 'PATH/private Pi authority was spliced')
  validateModel(input.model.value, config)
  validateEndpoint(input.endpoint.value, config)
  if (requirePreflight) validatePreflight(input.preflight.value, config)
  validateActualReleaseManifest(input.actualRelease.value, config)
  validatePrivatePiManifest(input.privatePiManifest.value, config)
}

function validateConfigShape(config) {
  plain(config, 'orchestrator config'); exactKeys(config, configKeys, 'orchestrator config')
  plain(config.identity, 'identity'); exactKeys(config.identity, identityKeys, 'identity')
  for (const name of ['environmentId', 'installId', 'generationId']) assert(opaqueIDRE.test(config.identity[name]), `${name} is invalid`)
  validateProductHostPlatform(config.identity.platform)
  plain(config.paths, 'paths'); exactKeys(config.paths, pathKeys, 'paths')
  for (const [name, value] of Object.entries(config.paths)) absolute(value, name)
  plain(config.authority, 'authority'); exactKeys(config.authority, authorityKeys, 'authority')
  for (const key of authorityKeys.filter((key) => key.endsWith('Sha256') || key === 'endpointDigest')) digest(config.authority[key], key)
  assert(Number.isSafeInteger(config.authority.diskImageSize) && config.authority.diskImageSize > 0,
    'disk image size is invalid')
  assert(productIDRE.test(config.authority.dockerContext), 'Docker context is invalid')
  assert(config.paths.dockerCLIFile === join(config.paths.colimaToolRoot, 'docker'),
    'Docker CLI topology drifted')
  validateRoles(config.authority.roles)
  plain(config.private, 'private runtime config'); exactKeys(config.private, privateKeys, 'private runtime config')
  assert(config.private.proxyURL === 'http://host.docker.internal:9981', 'proxy URL is invalid')
  assert(typeof config.private.modelURL === 'string' && /^https:\/\//.test(config.private.modelURL), 'model URL is invalid')
  assert(Number.isSafeInteger(config.private.port) && config.private.port >= 1 && config.private.port <= 65535, 'port is invalid')
  assert(productIDRE.test(config.private.colimaProfile), 'Colima profile is invalid')
  assert(config.authority.dockerContext === `colima-${config.private.colimaProfile}`,
    'Docker context is not the exact Colima-backed Lima instance name')
  plain(config.setup, 'Setup authority'); exactKeys(config.setup, setupKeys, 'Setup authority')
  for (const key of ['key', 'grant', 'actor', 'session', 'releaseId']) assert(productIDRE.test(config.setup[key]), `Setup ${key} is invalid`)
  assert(typeof config.setup.releaseManifestRelativePath === 'string' && config.setup.releaseManifestRelativePath.length > 0 &&
    !isAbsolute(config.setup.releaseManifestRelativePath) && normalize(config.setup.releaseManifestRelativePath) === config.setup.releaseManifestRelativePath &&
    !config.setup.releaseManifestRelativePath.split(/[\\/]/).includes('..'), 'Setup release manifest relative path is invalid')
}

async function validatePathTopology(p) {
  for (const path of Object.values(p)) await assertNoSymlinkAncestors(path)
  const sources = pathKeys.filter((key) => /(?:File|Directory|Root)$/.test(key) &&
    !['installRoot', 'installedBinaryFile', 'installedWebDirectory', 'installedSourceRoot', 'installedSourceManifestFile',
      'dataRoot', 'databaseFile', 'stateRoot', 'ledgerFile', 'receiptDirectory', 'privatePiRoot',
      'tupleFile', 'qualificationFile', 'modelObservationFile', 'installedDoctorReportFile'].includes(key))
    .map((key) => p[key])
  const roots = [p.installRoot, p.dataRoot, p.stateRoot, p.receiptDirectory, p.privatePiRoot]
  for (let i = 0; i < roots.length; i++) for (let j = i + 1; j < roots.length; j++) {
    assert(!overlaps(roots[i], roots[j]), 'mutable roots overlap')
  }
  for (const root of roots) for (const source of sources) assert(!overlaps(root, source), 'source and mutable root overlap')
  assert(within(p.installRoot, p.installedBinaryFile) && within(p.installRoot, p.installedWebDirectory), 'installed targets escape install root')
  assert(p.installedSourceRoot === p.installRoot,
    'installed source root must be the exact install root')
  assert(p.installedSourceManifestFile === join(p.installRoot, 'source-manifest.json'),
    'installed source manifest topology drifted')
  assert(within(p.dataRoot, p.databaseFile), 'database escapes data root')
  assert(within(p.stateRoot, p.ledgerFile), 'ledger escapes state root')
  assert(dirname(p.tupleFile) === p.receiptDirectory, 'tuple file must be directly inside receipt directory')
  assert(basename(p.qualificationFile) === 'engine-qualification.json' &&
    basename(p.modelObservationFile) === 'model-observation.json' &&
    basename(p.installedDoctorReportFile) === 'installed-doctor-report.json',
  'dynamic evidence output names drifted')
  assert(new Set([p.qualificationFile, p.modelObservationFile, p.installedDoctorReportFile]).size === 3,
    'dynamic evidence output paths overlap')
}

function buildTuple(config, input, repositoryAuthority) {
  const roots = {}
  for (const key of ['installRoot', 'dataRoot', 'stateRoot', 'receiptDirectory', 'privatePiRoot', 'probeRuntimeRoot', 'colimaToolRoot']) roots[`${key}Sha256`] = sha256(config.paths[key])
  const safe = {
    schemaVersion: CANDIDATE_TUPLE_SCHEMA, status: 'frozen', ...clone(config.identity), roots,
    repositoryAuthority: clone(repositoryAuthority),
    bindings: {
      sourceManifestSha256: input.sourceManifest.sha256, sourceAggregateSha256: input.sourceTree.aggregate,
      candidateManifestSha256: input.candidateManifest.sha256, binarySha256: input.binary.sha256,
      webAggregateSha256: input.webAggregate, releaseSpecSha256: input.releaseSpec.sha256,
      releaseManifestSha256: input.releaseManifest.sha256,
      offlineBundleEvidenceSha256: input.offlineBundle.sha256,
      pathPiProvenanceSha256: input.pathPi.sha256, privatePiProvenanceSha256: input.privatePi.sha256,
      modelAuthoritySha256: input.model.sha256,
      endpointEvidenceSha256: input.endpoint.sha256, preflightFileSha256: input.preflight.sha256,
      preflightDigest: input.preflight.value.digest,
      actualReleaseManifestSha256: input.actualRelease.sha256, privatePiManifestSha256: input.privatePiManifest.sha256,
      authFileSha256: input.authFile.sha256, caFileSha256: input.caFile.sha256,
      colimaSourceSha256: input.colimaSource.sha256, dockerClientSourceSha256: input.dockerClientSource.sha256,
      repositoryCommit: config.authority.repositoryCommit,
      repositoryClosureSha256: config.authority.repositoryClosureSha256,
      dockerCLISha256: input.dockerCLI.sha256, dockerContextSha256: sha256(config.authority.dockerContext),
      endpointDigest: config.authority.endpointDigest, roleTupleSha256: sha256(canonicalJSONStringify(config.authority.roles)),
      privateRuntimeConfigSha256: sha256(canonicalJSONStringify(config.private)),
    },
    plan: [...candidateTuplePlan],
  }
  return { ...safe, identity: sha256(canonicalJSONStringify(safe)) }
}

async function freezeTuple(tuple, path, repositoryCapture, markerWriter) {
  await ensureOwnerDirectory(dirname(path))
  await writeNativeJSONMarker(tuple, path, 'candidate tuple', repositoryCapture, markerWriter)
  const reopened = await readAndValidateTuple(path)
  sameJSON(reopened, tuple, 'candidate tuple changed after native publication')
  return reopened
}

async function readAndValidateTuple(path) {
  const input = await readExactFile(path, 'candidate tuple', MAX_JSON, { mode: 0o400 })
  const value = parseJSON(input, 'candidate tuple')
  return validateCandidateTuple(value)
}

export function validateCandidateTuple(value) {
  plain(value, 'candidate tuple')
  exactKeys(value, candidateTupleKeys, 'candidate tuple')
  assert(value.schemaVersion === CANDIDATE_TUPLE_SCHEMA && value.status === 'frozen', 'candidate tuple schema/status drifted')
  assert(/^env_[A-Za-z0-9_-]{24,80}$/.test(value.environmentId),
    'candidate tuple environmentId is invalid')
  assert(/^ins_[A-Za-z0-9_-]{24,80}$/.test(value.installId),
    'candidate tuple installId is invalid')
  assert(/^gen_[A-Za-z0-9_-]{24,80}$/.test(value.generationId),
    'candidate tuple generationId is invalid')
  validateProductHostPlatform(value.platform)
  plain(value.roots, 'candidate tuple roots')
  exactKeys(value.roots, candidateTupleRootKeys, 'candidate tuple roots')
  for (const key of candidateTupleRootKeys) digest(value.roots[key], `candidate tuple roots ${key}`)
  plain(value.bindings, 'candidate tuple bindings')
  exactKeys(value.bindings, candidateTupleBindingKeys, 'candidate tuple bindings')
  for (const key of candidateTupleBindingKeys) {
    if (key === 'repositoryCommit') {
      assert(/^[0-9a-f]{40}$/.test(value.bindings[key]),
        'candidate tuple repository commit is invalid')
    } else digest(value.bindings[key], `candidate tuple bindings ${key}`)
  }
  const repositoryAuthority = validateRepositoryAuthoritySnapshot(clone(value.repositoryAuthority), {
    repositoryRoot: value.repositoryAuthority?.repositoryRoot,
    repositoryCommit: value.bindings.repositoryCommit,
    repositoryClosureSha256: value.bindings.repositoryClosureSha256,
  })
  assert(repositoryAuthority.repositoryCommit === value.bindings.repositoryCommit &&
    repositoryAuthority.repositoryClosureSha256 === value.bindings.repositoryClosureSha256,
  'candidate tuple repository authority binding drifted')
  assert(Array.isArray(value.plan) && value.plan.length === candidateTuplePlan.length &&
    value.plan.every((step, index) => step === candidateTuplePlan[index]),
  'candidate tuple plan/order drifted')
  const { identity, ...safe } = value
  digest(identity, 'tuple identity')
  assert(identity === sha256(canonicalJSONStringify(safe)), 'candidate tuple identity drifted')
  return value
}

async function nextReceipt(
  config, receipts, phase, operationDigest, observationDigest, repositoryCapture, markerWriter,
) {
  const previous = receipts.at(-1)
  return appendReceipt(config.paths.tupleFile, config.paths.receiptDirectory, {
    phase, sequence: receipts.length + 1, previousReceiptDigest: previous.receiptDigest,
    operationDigest, observationDigest,
  }, repositoryCapture, markerWriter)
}

async function appendReceipt(tupleFile, directory, draft, repositoryCapture, markerWriter) {
  return withPhaseAppendLock(tupleFile, directory, repositoryCapture,
    () => appendReceiptLocked(tupleFile, directory, draft, repositoryCapture, markerWriter))
}

async function appendReceiptLocked(
  tupleFile, directory, draft, repositoryCapture, markerWriter,
) {
  const receipt = await prepareReceiptLocked(tupleFile, directory, draft)
  return publishPreparedReceiptLocked(
    tupleFile, directory, receipt, repositoryCapture, markerWriter)
}

async function prepareReceiptLocked(tupleFile, directory, draft) {
  const tuple = await readAndValidateTuple(tupleFile)
  assert(phases.includes(draft.phase), 'phase is invalid')
  assert(Number.isSafeInteger(draft.sequence) && draft.sequence > 0 && draft.sequence <= phases.length, 'phase sequence is invalid')
  digest(draft.operationDigest, 'phase operation digest')
  if (draft.observationDigest !== null) digest(draft.observationDigest, 'phase observation digest')
  if (draft.previousReceiptDigest !== null) digest(draft.previousReceiptDigest, 'previous receipt digest')
  const chain = await readReceiptChain(tupleFile, directory)
  assert(chain.length + 1 === draft.sequence, 'phase receipt sequence drifted')
  assert(phases[chain.length] === draft.phase, 'phase receipt transition is not the exact next phase')
  assert((chain.at(-1)?.receiptDigest ?? null) === draft.previousReceiptDigest, 'phase receipt chain was spliced')
  const safe = {
    schemaVersion: PHASE_RECEIPT_SCHEMA, tupleIdentity: tuple.identity,
    phase: draft.phase, sequence: draft.sequence, status: 'passed',
    operationDigest: draft.operationDigest, observationDigest: draft.observationDigest,
    previousReceiptDigest: draft.previousReceiptDigest,
  }
  const receipt = { ...safe, receiptDigest: sha256(canonicalJSONStringify(safe)) }
  return deepFreeze(receipt)
}

async function publishPreparedReceiptLocked(
  tupleFile, directory, preparedReceipt, repositoryCapture, markerWriter,
) {
  const expected = await prepareReceiptLocked(tupleFile, directory, {
    phase: preparedReceipt.phase,
    sequence: preparedReceipt.sequence,
    previousReceiptDigest: preparedReceipt.previousReceiptDigest,
    operationDigest: preparedReceipt.operationDigest,
    observationDigest: preparedReceipt.observationDigest,
  })
  sameJSON(preparedReceipt, expected, 'prepared phase receipt drifted before publication')
  const path = join(directory, receiptName(expected.sequence, expected.phase))
  await writeNativeJSONMarker(expected, path, 'phase receipt', repositoryCapture, markerWriter)
  const reopened = await readReceiptChain(tupleFile, directory)
  assert(reopened.length === expected.sequence,
    'native phase receipt did not reopen as the exact chain tip')
  const published = reopened.at(-1)
  sameJSON(published, expected, 'phase receipt changed after native publication')
  return deepFreeze(published)
}

async function readReceiptChain(tupleFile, directory) {
  const tuple = await readAndValidateTuple(tupleFile)
  await assertOwnerDirectory(directory)
  const entries = await readdir(directory)
  const allowedReceipts = new Set(phases.map((phase, index) => receiptName(index + 1, phase)))
  const allowedOther = new Set([basename(tupleFile), 'restarts', PHASE_APPEND_LOCK])
  assert(entries.every((name) => allowedReceipts.has(name) || allowedOther.has(name)),
    'phase receipt namespace contains an unrelated entry')
  const names = entries.filter((name) => allowedReceipts.has(name)).sort()
  const result = []
  for (const name of names) {
    const bytes = await readExactFile(join(directory, name), 'phase receipt', MAX_JSON, { mode: 0o400 })
    const receipt = parseJSON(bytes, 'phase receipt')
    exactKeys(receipt, ['schemaVersion', 'tupleIdentity', 'phase', 'sequence', 'status', 'operationDigest', 'observationDigest', 'previousReceiptDigest', 'receiptDigest'], 'phase receipt')
    const { receiptDigest, ...safe } = receipt
    assert(receipt.schemaVersion === PHASE_RECEIPT_SCHEMA && receipt.status === 'passed' && receipt.tupleIdentity === tuple.identity, 'phase receipt identity drifted')
    assert(receipt.sequence === result.length + 1 && name === receiptName(receipt.sequence, receipt.phase), 'phase receipt sequence drifted')
    assert(receipt.phase === phases[result.length], 'phase receipt chain is not the sole exact phase prefix')
    assert(receipt.previousReceiptDigest === (result.at(-1)?.receiptDigest ?? null), 'phase receipt chain was spliced')
    assert(receiptDigest === sha256(canonicalJSONStringify(safe)), 'phase receipt digest drifted')
    result.push(receipt)
  }
  return result
}

function receiptName(sequence, phase) { return `${String(sequence).padStart(2, '0')}-${phase}.json` }

async function appendRestartReceiptLocked(
  tupleFile, receiptDirectory, operationDigest, observationDigest, repositoryCapture, markerWriter,
) {
  const tuple = await readAndValidateTuple(tupleFile)
  digest(operationDigest, 'restart operation digest'); digest(observationDigest, 'restart observation digest')
  const directory = join(receiptDirectory, 'restarts')
  await ensureOwnerDirectory(directory)
  const prior = await readRestartReceipts(tupleFile, receiptDirectory)
  assert(prior.length < 32, 'restart receipt cardinality exceeded')
  const safe = {
    schemaVersion: RESTART_RECEIPT_SCHEMA, tupleIdentity: tuple.identity, sequence: prior.length + 1,
    status: 'passed', operationDigest, observationDigest,
    previousReceiptDigest: prior.at(-1)?.receiptDigest ?? null, setupReplayed: false,
  }
  const receipt = { ...safe, receiptDigest: sha256(canonicalJSONStringify(safe)) }
  const path = join(directory, `restart-${String(safe.sequence).padStart(2, '0')}.json`)
  await writeNativeJSONMarker(receipt, path, 'restart receipt', repositoryCapture, markerWriter)
  const reopened = await readRestartReceipts(tupleFile, receiptDirectory)
  assert(reopened.length === safe.sequence,
    'native restart receipt did not reopen as the exact chain tip')
  const published = reopened.at(-1)
  sameJSON(published, receipt, 'restart receipt changed after native publication')
  return deepFreeze(published)
}

async function readRestartReceipts(tupleFile, receiptDirectory) {
  const tuple = await readAndValidateTuple(tupleFile)
  const directory = join(receiptDirectory, 'restarts')
  let names
  try { names = (await readdir(directory)).sort() } catch (error) { if (error.code === 'ENOENT') return []; throw error }
  await assertOwnerDirectory(directory)
  assert(names.length <= 32 && names.every((name) => /^restart-\d{2}\.json$/.test(name)), 'restart receipt namespace drifted')
  const receipts = []
  for (const name of names) {
    const value = parseJSON(await readExactFile(join(directory, name), 'restart receipt', MAX_JSON, { mode: 0o400 }), 'restart receipt')
    exactKeys(value, ['schemaVersion', 'tupleIdentity', 'sequence', 'status', 'operationDigest', 'observationDigest', 'previousReceiptDigest', 'setupReplayed', 'receiptDigest'], 'restart receipt')
    const { receiptDigest, ...safe } = value
    assert(value.schemaVersion === RESTART_RECEIPT_SCHEMA && value.tupleIdentity === tuple.identity && value.status === 'passed' && value.setupReplayed === false, 'restart receipt identity drifted')
    assert(value.sequence === receipts.length + 1 && name === `restart-${String(value.sequence).padStart(2, '0')}.json`, 'restart receipt sequence drifted')
    assert(value.previousReceiptDigest === (receipts.at(-1)?.receiptDigest ?? null) && receiptDigest === sha256(canonicalJSONStringify(safe)), 'restart receipt chain drifted')
    receipts.push(value)
  }
  return receipts
}

function productDoctorSpec(binary, config) {
  return commandSpec(binary, ['product', 'doctor', '--docker-cli', config.paths.dockerCLIFile,
    '--docker-context', config.authority.dockerContext, '--endpoint-digest', config.authority.endpointDigest, '--json'])
}

function installedDoctorSpec(config, setupReceipt) {
  const p = config.paths
  return commandSpec(p.installedBinaryFile, [
    'installed-doctor', '--state-root', p.stateRoot, '--generation', config.identity.generationId,
    '--docker-cli', p.dockerCLIFile, '--docker-context', config.authority.dockerContext,
    '--endpoint-digest', config.authority.endpointDigest,
    '--release-root', p.actualReleaseRoot, '--release-manifest', config.setup.releaseManifestRelativePath,
    '--manifest-sha256', config.authority.actualReleaseManifestSha256, '--release-id', config.setup.releaseId,
    '--private-pi-root', p.privatePiRoot, '--private-pi-source', p.privatePiSourceRoot,
    '--private-pi-manifest', p.privatePiManifestFile,
    '--private-pi-manifest-sha256', config.authority.privatePiManifestSha256,
    '--source', p.installedSourceRoot, '--source-manifest', p.installedSourceManifestFile,
    '--bundle-aggregate', config.authority.sourceAggregateSha256, '--install', p.installRoot,
    '--data', p.dataRoot, '--auth', p.authFile, '--ca', p.caFile,
    '--proxy', config.private.proxyURL, '--model-url', config.private.modelURL,
    '--port', String(config.private.port),
    '--setup-receipt', setupReceipt.path,
    '--setup-receipt-sha256', setupReceipt.sha256,
    '--model-request-authority', p.modelAuthorityFile,
    '--model-request-authority-sha256', config.authority.modelAuthoritySha256,
  ])
}

function serveSpec(config, o4Authority = null, inputFingerprint = null, o4EvidenceAuthority = null) {
  digest(inputFingerprint, 'installed Doctor fingerprint')
  const p = config.paths
  const argv = [
    'serve', '--db', p.databaseFile, '--web', p.installedWebDirectory, '--port', String(config.private.port),
    '--source', p.installedSourceRoot, '--source-manifest', p.installedSourceManifestFile,
    '--bundle-aggregate', config.authority.sourceAggregateSha256, '--install', p.installRoot,
    '--data', p.dataRoot, '--repository', p.repositoryRoot, '--auth', p.authFile, '--ca', p.caFile,
    '--proxy', config.private.proxyURL, '--model-url', config.private.modelURL,
    '--preflight-fingerprint', inputFingerprint, '--installation-state-root', p.stateRoot,
    '--generation', config.identity.generationId, '--docker-cli', p.dockerCLIFile,
    '--docker-context', config.authority.dockerContext, '--endpoint-digest', config.authority.endpointDigest,
    '--release-root', p.actualReleaseRoot, '--release-manifest', config.setup.releaseManifestRelativePath,
    '--manifest-sha256', config.authority.actualReleaseManifestSha256, '--release-id', config.setup.releaseId,
    '--private-pi-root', p.privatePiRoot, '--private-pi-source', p.privatePiSourceRoot,
    '--private-pi-manifest', p.privatePiManifestFile,
    '--private-pi-manifest-sha256', config.authority.privatePiManifestSha256,
    '--probe-runtime-root', p.probeRuntimeRoot, '--colima-tool-root', p.colimaToolRoot,
    '--colima-profile', config.private.colimaProfile, '--colima-source', p.colimaSourceFile,
    '--docker-client-source', p.dockerClientSourceFile, '--active-generation', config.identity.generationId,
    '--active-release-root', p.actualReleaseRoot, '--active-release-manifest', config.setup.releaseManifestRelativePath,
    '--active-manifest-sha256', config.authority.actualReleaseManifestSha256,
    '--active-release-id', config.setup.releaseId,
  ]
  if (o4Authority !== null) argv.push(
    '--o4-acceptance-authority', o4Authority.authorityPath,
    '--o4-acceptance-authority-sha256', o4Authority.authoritySha256,
    '--o4-candidate-tuple', o4Authority.candidateTuple,
  )
  if (o4EvidenceAuthority !== null) argv.push(
    '--o4-runner-session-authority', o4EvidenceAuthority.sessionAuthorityFile,
    '--o4-runner-session-authority-sha256', o4EvidenceAuthority.sessionAuthoritySha256,
    '--o4-installed-doctor-report', o4EvidenceAuthority.installedDoctorReportFile,
    '--o4-installed-doctor-report-sha256', o4EvidenceAuthority.installedDoctorReportSha256,
    '--o4-evidence-tuple', o4EvidenceAuthority.tupleIdentity,
    '--o4-a3-ledger', o4EvidenceAuthority.sourceA3LedgerFile,
    '--o4-verifier-ledger', o4EvidenceAuthority.verifierLedgerFile,
    '--o4-c-resource-dir', o4EvidenceAuthority.cResourceDirectory,
    '--o4-d-resource-dir', o4EvidenceAuthority.dResourceDirectory,
  )
  return commandSpec(p.installedBinaryFile, argv)
}

function validateO4EvidenceAuthority(value, tupleIdentity) {
  if (value === undefined || value === null) return null
  plain(value, 'O4 evidence authority')
  exactKeys(value, o4EvidenceAuthorityKeys, 'O4 evidence authority')
  digest(value.sessionAuthoritySha256, 'O4 evidence session authority SHA-256')
  digest(value.tupleIdentity, 'O4 evidence tuple identity')
  assert(value.tupleIdentity === tupleIdentity, 'O4 evidence tuple identity drifted')
  const paths = [value.sessionAuthorityFile, value.sourceA3LedgerFile, value.verifierLedgerFile,
    value.cResourceDirectory, value.dResourceDirectory]
  for (const path of paths) absolute(path, 'O4 evidence authority path')
  for (let left = 0; left < paths.length; left++) {
    for (let right = left + 1; right < paths.length; right++) {
      assert(!overlaps(paths[left], paths[right]), 'O4 evidence authority paths overlap')
    }
  }
  return deepFreeze(clone(value))
}

async function bindO4InstalledDoctorEvidence(value, installedDoctorReportFile) {
  if (value === null) return null
  absolute(installedDoctorReportFile, 'installed Doctor evidence report')
  const paths = [value.sessionAuthorityFile, value.sourceA3LedgerFile, value.verifierLedgerFile,
    value.cResourceDirectory, value.dResourceDirectory]
  assert(!paths.some((path) => overlaps(path, installedDoctorReportFile)),
    'installed Doctor evidence report overlaps O4 evidence output authority')
  const reportBytes = await readExactFile(installedDoctorReportFile,
    'installed Doctor evidence report', MAX_JSON, { mode: 0o400 })
  return deepFreeze({ ...clone(value), installedDoctorReportFile,
    installedDoctorReportSha256: sha256(reportBytes) })
}

function installedBindings(config) {
  const a = config.authority
  return {
    candidate: { candidateManifestSha256: a.candidateManifestSha256,
      sourceManifestSha256: a.sourceManifestSha256, sourceAggregateSha256: a.sourceAggregateSha256 },
    product: { binarySha256: a.binarySha256, webAggregateSha256: a.webAggregateSha256 },
    release: { releaseSpecSha256: a.releaseSpecSha256, releaseManifestSha256: a.releaseManifestSha256,
      offlineBundleEvidenceSha256: a.offlineBundleEvidenceSha256 },
    pi: { selected: 'private', pathProvenanceSha256: a.pathPiProvenanceSha256,
      privateProvenanceSha256: a.privatePiProvenanceSha256 },
    engine: { qualificationSha256: '0'.repeat(64), endpointEvidenceSha256: a.endpointEvidenceSha256 },
    roles: clone(a.roles),
  }
}

function setupSpec(config) {
  return commandSpec(config.paths.installedBinaryFile, [
    'setup', '--docker-cli', config.paths.dockerCLIFile, '--docker-context', config.authority.dockerContext,
    '--endpoint-digest', config.authority.endpointDigest, '--state-root', config.paths.stateRoot,
    '--key', config.setup.key, '--grant', config.setup.grant, '--actor', config.setup.actor,
    '--session', config.setup.session, '--authorize', 'setup', '--release-root', config.paths.actualReleaseRoot,
    '--release-manifest', config.setup.releaseManifestRelativePath, '--manifest-sha256', config.authority.actualReleaseManifestSha256,
    '--release-id', config.setup.releaseId, '--generation', config.identity.generationId,
    '--probe-runtime-root', config.paths.probeRuntimeRoot, '--private-pi-root', config.paths.privatePiRoot,
    '--private-pi-source', config.paths.privatePiSourceRoot, '--private-pi-manifest', config.paths.privatePiManifestFile,
    '--private-pi-manifest-sha256', config.authority.privatePiManifestSha256, '--json',
  ])
}

function assertSetupCommand(spec) {
  assert(spec.argv[0] === 'setup', 'Setup command cardinality drifted')
  for (const flag of ['--bootstrap-colima', '--authorize-colima-download', '--authorize-colima-vm'])
    assert(!spec.argv.includes(flag), 'Setup command contains bootstrap authority')
  assert(!spec.argv.some((value) => ['npm', 'go', 'build', 'pull', 'load'].includes(value)),
    'Setup command contains build/acquisition authority')
}

function commandSpec(file, argv) {
  return Object.freeze({ file, argv: Object.freeze([...argv]), shell: false, maxOutputBytes: MAX_OUTPUT })
}

async function executeJSON(runner, spec, label) {
  assert(spec.shell === false && Array.isArray(spec.argv) && spec.argv.every((item) => typeof item === 'string'), `${label} spawn is unsafe`)
  let result
  try { result = await runner.exec(spec) } catch { throw new Error(`${label} failed`) }
  plain(result, `${label} result`)
  exactKeys(result, ['exitCode', 'stdout', 'stderr'], `${label} result`)
  assert(Number.isSafeInteger(result.exitCode), `${label} exit code is invalid`)
  const stdout = boundedText(result.stdout, `${label} stdout`)
  boundedText(result.stderr, `${label} stderr`)
  assert(result.exitCode === 0, `${label} failed`)
  return parseJSON(Buffer.from(stdout), label)
}

function assertPreflightObservation(value, config, expected) {
  plain(value, 'Engine Doctor observation')
  exactKeys(value, ['schema_version', 'status', 'action', 'context_name', 'endpoint_digest', 'daemon_id', 'api_version', 'operating_system', 'architecture'], 'Engine Doctor observation')
  assert(value.status === 'passed' && value.action === 'doctor', 'Engine Doctor is stopped or failed')
  const observed = {
    contextName: value.context_name, endpointDigest: value.endpoint_digest, daemonId: value.daemon_id,
    apiVersion: value.api_version, operatingSystem: value.operating_system, architecture: value.architecture,
  }
  for (const [key, actual] of Object.entries(observed)) assert(actual === expected[key], `Engine Doctor ${key} drifted`)
  validateEnginePlatform({ operatingSystem: value.operating_system, architecture: value.architecture })
  assert(value.context_name === config.authority.dockerContext && value.endpoint_digest === config.authority.endpointDigest,
    'Engine Doctor identity drifted')
}

function assertPassedJSON(value, label) { plain(value, label); assert(value.status === 'passed', `${label} failed`) }

function validateSourceManifest(value, actual) {
  plain(value, 'source manifest')
  exactKeys(value, ['schema_version', 'aggregate_sha256', 'source_path_policy', 'install_only_projection', 'model_readable_projection', 'files'], 'source manifest')
  assert(value.schema_version === SOURCE_SCHEMA && value.aggregate_sha256 === actual.aggregate, 'source manifest identity drifted')
  plain(value.source_path_policy, 'source path policy identity'); exactKeys(value.source_path_policy, ['path', 'sha256'], 'source path policy identity')
  assert(value.source_path_policy.path === 'distribution/v1/policies/source-path-policy.v1.json', 'source path policy path drifted'); digest(value.source_path_policy.sha256, 'source path policy digest')
  for (const key of ['install_only_projection', 'model_readable_projection']) {
    plain(value[key], key); exactKeys(value[key], ['files', 'paths_sha256', 'aggregate_sha256'], key)
    assert(Number.isSafeInteger(value[key].files) && value[key].files >= 0, `${key} count invalid`)
    digest(value[key].paths_sha256, `${key} paths digest`); digest(value[key].aggregate_sha256, `${key} aggregate`)
  }
  assert(Array.isArray(value.files) && value.files.length > 0 && value.files.length <= 10000, 'source manifest file set is invalid')
}

async function inspectSourceTree(root, manifest) {
  plain(manifest, 'source manifest')
  assert(Array.isArray(manifest.files), 'source manifest files missing')
  const sourceRoot = join(root, 'source')
  const framing = []
  const snapshot = {}
  let previous = ''
  for (const entry of manifest.files) {
    plain(entry, 'source entry'); exactKeys(entry, ['path', 'size', 'mode', 'sha256'], 'source entry')
    assert(safeRelative(entry.path) && entry.path > previous, 'source paths are unsafe or unsorted'); previous = entry.path
    assert(Number.isSafeInteger(entry.size) && entry.size >= 0, 'source size invalid')
    assert(/^[0-7]{3,4}$/.test(entry.mode), 'source mode invalid'); digest(entry.sha256, 'source entry digest')
    const path = join(sourceRoot, ...entry.path.split('/'))
    const bytes = await readExactFile(path, `source ${entry.path}`, Math.max(entry.size, 1))
    const info = await stat(path)
    assert(bytes.length === entry.size && sha256(bytes) === entry.sha256, 'source entry drifted')
    snapshot[path] = entry.sha256
    const mode = (info.mode & 0o777).toString(8).padStart(3, '0')
    assert(mode === entry.mode.replace(/^0(?=\d{3}$)/, ''), 'source entry mode drifted')
    framing.push(`${entry.path}\0${entry.mode}\0${entry.size}\0${entry.sha256}\n`)
  }
  return { aggregate: sha256(framing.join('')), snapshot }
}

async function validateInstalledTopology(config, sourceManifest) {
  const p = config.paths
  const a = config.authority
  await assertOwnerDirectory(p.installRoot, 'installed product root')
  await assertOwnerDirectory(p.dataRoot, 'installed data root')

  const installedBinary = await readExactFile(p.installedBinaryFile,
    'installed immutable binary', MAX_BINARY)
  assert(sha256(installedBinary) === a.binarySha256, 'installed binary identity drifted')
  assert(await directoryAggregate(p.installedWebDirectory) === a.webAggregateSha256,
    'installed web identity drifted')
  const installedManifest = await readExactFile(p.installedSourceManifestFile,
    'installed source manifest', MAX_SOURCE_MANIFEST, { mode: 0o444 })
  const sourceManifestBytes = await readExactFile(p.sourceManifestFile,
    'source manifest', MAX_SOURCE_MANIFEST, { mode: 0o444 })
  assert(installedManifest.equals(sourceManifestBytes) &&
    sha256(installedManifest) === a.sourceManifestSha256,
  'installed source manifest drifted')
  const installedSource = await inspectSourceTree(p.installedSourceRoot, sourceManifest)
  assert(installedSource.aggregate === a.sourceAggregateSha256,
    'installed source tree drifted')

  const installationFile = join(p.installRoot, 'installation.json')
  const installation = parseJSON(await readExactFile(installationFile,
    'installation identity', MAX_JSON, { mode: 0o444 }), 'installation identity')
  plain(installation, 'installation identity')
  exactKeys(installation, ['schema_version', 'aggregate_sha256', 'files'],
    'installation identity')
  assert(installation.schema_version === INSTALLATION_SCHEMA &&
    installation.aggregate_sha256 === a.sourceAggregateSha256 &&
    installation.files === sourceManifest.files.length,
  'installation identity drifted')

  const dataInstallationFile = join(p.dataRoot, 'data-installation.json')
  const dataInstallation = parseJSON(await readExactFile(dataInstallationFile,
    'data installation identity', MAX_JSON, { mode: 0o444 }), 'data installation identity')
  plain(dataInstallation, 'data installation identity')
  exactKeys(dataInstallation, ['schema_version', 'aggregate_sha256', 'install_root'],
    'data installation identity')
  assert(dataInstallation.schema_version === DATA_INSTALLATION_SCHEMA &&
    dataInstallation.aggregate_sha256 === a.sourceAggregateSha256 &&
    dataInstallation.install_root === p.installRoot,
  'data installation identity drifted')
  const dataEntries = await readdir(p.dataRoot, { withFileTypes: true })
  assert(dataEntries.length === 1 && dataEntries[0].name === 'data-installation.json' &&
    dataEntries[0].isFile() && !dataEntries[0].isSymbolicLink(),
  'fresh installed data topology drifted')
}

function validateCandidate(value, config) {
  plain(value, 'candidate manifest')
  exactKeys(value, ['schemaVersion', ...identityKeys.slice(0, 3), 'platform', 'sourceManifestSha256', 'sourceAggregateSha256', 'binarySha256', 'webAggregateSha256', 'releaseSpecSha256', 'releaseManifestSha256', 'offlineBundleEvidenceSha256', 'repositoryCommit', 'repositoryClosureSha256'], 'candidate manifest')
  assert(value.schemaVersion === CANDIDATE_SCHEMA, 'candidate schema drifted')
  for (const key of identityKeys.slice(0, 3)) assert(value[key] === config.identity[key], 'candidate identity drifted')
  sameJSON(value.platform, config.identity.platform, 'candidate platform drifted')
  for (const key of ['sourceManifestSha256', 'sourceAggregateSha256', 'binarySha256', 'webAggregateSha256', 'releaseSpecSha256', 'releaseManifestSha256', 'offlineBundleEvidenceSha256']) assert(value[key] === config.authority[key], `candidate ${key} drifted`)
  assert(value.repositoryCommit === config.authority.repositoryCommit &&
    value.repositoryClosureSha256 === config.authority.repositoryClosureSha256,
  'candidate repository authority drifted')
}

function validateRelease(spec, manifest, offlineBundle, config) {
  validateRoleDocument(spec, RELEASE_SPEC_SCHEMA, ['schemaVersion', 'generationId', 'platform', 'roles'], config)
  validateRoleDocument(manifest, RELEASE_MANIFEST_SCHEMA, ['schemaVersion', 'generationId', 'releaseSpecSha256', 'roles'], config)
  assert(manifest.releaseSpecSha256 === config.authority.releaseSpecSha256, 'release manifest/spec splice')
  validateOfflineBundleEvidence(offlineBundle, config)
  assert(offlineBundle.releaseManifestSha256 === config.authority.actualReleaseManifestSha256,
    'offline bundle/release splice')
}

function validateRoleDocument(value, schema, keys, config) {
  plain(value, 'release authority'); exactKeys(value, keys, 'release authority')
  assert(value.schemaVersion === schema && value.generationId === config.identity.generationId, 'release authority identity drifted')
  if ('platform' in value) sameJSON(value.platform, config.identity.platform, 'release platform drifted')
  validateRoleArray(value.roles, config.authority.roles)
}

function validateRoleArray(value, roles) {
  assert(Array.isArray(value) && value.length === 4, 'release roles incomplete')
  const names = []
  for (const entry of value) {
    plain(entry, 'release role')
    exactKeys(entry, ['role', 'artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId', 'policyDigest'], 'release role')
    assert(O4_ROLES.includes(entry.role), 'release role invalid')
    for (const key of ['artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId', 'policyDigest']) assert(entry[key] === roles[entry.role][key], 'release role drifted')
    names.push(entry.role)
  }
  assert(new Set(names).size === 4, 'release roles duplicated')
}

function validateOfflineBundleEvidence(value, config) {
  plain(value, 'offline bundle evidence')
  exactKeys(value, ['schemaVersion', 'generationId', 'releaseManifestSha256', 'platform', 'artifacts'],
    'offline bundle evidence')
  assert(value.schemaVersion === OFFLINE_BUNDLE_SCHEMA &&
    value.generationId === config.identity.generationId,
  'offline bundle evidence identity drifted')
  validateReleaseImagePlatform(value.platform, 'offline bundle platform')
  assert(Array.isArray(value.artifacts) && value.artifacts.length === 2,
    'offline bundle must contain exactly two artifacts')
  let previousArtifact = ''
  const seenRoles = []
  for (const artifact of value.artifacts) {
    plain(artifact, 'offline bundle artifact')
    exactKeys(artifact, ['artifactId', 'roles', 'policyDigests', 'archive', 'expectedDockerConfigImageId'],
      'offline bundle artifact')
    assert(['managed-pi-runtime', 'network-boundary'].includes(artifact.artifactId) &&
      artifact.artifactId > previousArtifact, 'offline bundle artifacts are unsafe, unsorted, or duplicated')
    previousArtifact = artifact.artifactId
    assert(Array.isArray(artifact.roles) && artifact.roles.length > 0 &&
      artifact.roles.every((role, index) => O4_ROLES.includes(role) &&
        (index === 0 || role > artifact.roles[index - 1])),
    'offline bundle artifact roles are unsafe, unsorted, or duplicated')
    plain(artifact.policyDigests, 'offline bundle policy digests')
    exactKeys(artifact.policyDigests, artifact.roles, 'offline bundle policy digests')
    plain(artifact.archive, 'offline bundle archive')
    exactKeys(artifact.archive, ['path', 'format', 'size', 'sha256'], 'offline bundle archive')
    assert(safeRelative(artifact.archive.path) && artifact.archive.format === 'docker-archive' &&
      Number.isSafeInteger(artifact.archive.size) && artifact.archive.size > 0,
    'offline bundle archive binding is invalid')
    digest(artifact.archive.sha256, 'offline bundle archive digest')
    imageDigest(artifact.expectedDockerConfigImageId, 'offline bundle expected Docker config image ID')
    for (const role of artifact.roles) {
      const expected = config.authority.roles[role]
      assert(expected.artifactId === artifact.artifactId &&
        expected.archiveSha256 === artifact.archive.sha256 &&
        expected.archiveSize === artifact.archive.size &&
        expected.dockerConfigImageId === artifact.expectedDockerConfigImageId &&
        expected.policyDigest === artifact.policyDigests[role],
      `offline bundle role binding drifted for ${role}`)
      seenRoles.push(role)
    }
  }
  assert(JSON.stringify([...seenRoles].sort()) === JSON.stringify([...O4_ROLES].sort()),
    'offline bundle roles are incomplete or duplicated')
}

function validateRoles(roles) {
  plain(roles, 'role bindings'); exactKeys(roles, O4_ROLES, 'role bindings')
  for (const role of O4_ROLES) {
    const value = roles[role]; plain(value, `${role} binding`)
    exactKeys(value, ['artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId', 'policyDigest'], `${role} binding`)
    assert(['managed-pi-runtime', 'network-boundary'].includes(value.artifactId), `${role} artifact invalid`)
    digest(value.archiveSha256, `${role} archive`); assert(Number.isSafeInteger(value.archiveSize) && value.archiveSize > 0, `${role} archive size invalid`); imageDigest(value.dockerConfigImageId, `${role} Docker config image ID`); digest(value.policyDigest, `${role} policy`)
  }
  for (const role of ['independent_verifier', 'capability_probe']) for (const key of ['artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId']) assert(roles[role][key] === roles.managed_pi_runtime[key], `${role} managed alias drifted`)
  assert(roles.network_boundary.artifactId === 'network-boundary' && roles.managed_pi_runtime.artifactId === 'managed-pi-runtime', 'role artifact identities drifted')
}

function validatePi(value, kind, generation) {
  plain(value, `${kind} Pi provenance`); exactKeys(value, ['schemaVersion', 'kind', 'generationId', 'sourceIdentitySha256', 'binarySha256'], `${kind} Pi provenance`)
  assert(value.schemaVersion === PI_SCHEMA && value.kind === kind && value.generationId === generation, `${kind} Pi identity drifted`)
  digest(value.sourceIdentitySha256, `${kind} Pi source`); digest(value.binarySha256, `${kind} Pi binary`)
}

function validateModel(value, config) {
  plain(value, 'model authority'); exactKeys(value, ['schemaVersion', 'status', 'generationId', 'providerIdentitySha256', 'modelIdentitySha256', 'policySha256', 'proxyURLSha256', 'modelURLSha256', 'authFileSha256', 'caFileSha256', 'authenticationRequired'], 'model authority')
  assert(value.schemaVersion === MODEL_AUTHORITY_SCHEMA && value.status === 'authorized' &&
    value.authenticationRequired === true && value.generationId === config.identity.generationId,
  'model request authority unavailable')
  for (const key of ['providerIdentitySha256', 'modelIdentitySha256', 'policySha256', 'proxyURLSha256', 'modelURLSha256', 'authFileSha256', 'caFileSha256']) digest(value[key], `model ${key}`)
  assert(value.proxyURLSha256 === sha256(config.private.proxyURL) && value.modelURLSha256 === sha256(config.private.modelURL) &&
    value.authFileSha256 === config.authority.authFileSha256 && value.caFileSha256 === config.authority.caFileSha256,
  'private model/credential authority drifted')
}

function validateEndpoint(value, config) {
  plain(value, 'endpoint evidence'); exactKeys(value, ['schemaVersion', 'status', 'generationId', 'platform', 'contextIdentitySha256', 'endpointIdentitySha256'], 'endpoint evidence')
  assert(value.schemaVersion === ENDPOINT_SCHEMA && value.status === 'passed' && value.generationId === config.identity.generationId, 'endpoint evidence unavailable')
  sameJSON(value.platform, config.identity.platform, 'endpoint platform drifted')
  assert(value.contextIdentitySha256 === sha256(config.authority.dockerContext) && value.endpointIdentitySha256 === config.authority.endpointDigest, 'endpoint/context identity drifted')
}

function validateQualification(value, config) {
  plain(value, 'qualification'); exactKeys(value, ['schemaVersion', 'status', 'generationId', 'platform', 'endpointEvidenceSha256', 'roles'], 'qualification')
  assert(value.schemaVersion === QUALIFICATION_SCHEMA && value.status === 'passed' && value.generationId === config.identity.generationId && value.endpointEvidenceSha256 === config.authority.endpointEvidenceSha256, 'qualification unavailable')
  sameJSON(value.platform, config.identity.platform, 'qualification platform drifted'); validateRoleArray(value.roles, config.authority.roles, false)
}

export function validateEngineProcessAuthority(processes, limaInstance) {
  assert(Array.isArray(processes) && processes.length === 2 && limaInstance &&
    typeof limaInstance === 'object' && !Array.isArray(limaInstance),
  'Engine preflight process authority is invalid')
  const byRole = new Map()
  for (const value of processes) {
    plain(value, 'Engine preflight process identity')
    exactKeys(value, ['role', 'pid', 'pidFileSha256'], 'Engine preflight process identity')
    assert(['host_agent', 'vz_driver'].includes(value.role) && !byRole.has(value.role) &&
      Number.isSafeInteger(value.pid) && value.pid > 1 && digestRE.test(value.pidFileSha256),
    'Engine preflight process authority is invalid')
    byRole.set(value.role, value)
  }
  assert(byRole.size === 2 && byRole.get('host_agent')?.pid === limaInstance.hostAgentPID &&
    byRole.get('vz_driver')?.pid === limaInstance.driverPID,
  'Engine preflight process authority is invalid')
  return processes
}

function validatePreflight(value, config) {
  plain(value, 'Engine preflight')
  exactKeys(value, ['schemaVersion', 'status', 'generationId', 'preMutationRootsAbsent',
    'preMutationAbsenceSha256', 'zeroMutation', 'initialImageCount', 'initialImageInventorySha256',
    'colimaSha256', 'limaSha256', 'limaPrefixClosureSha256', 'limaInfoDigest',
    'dockerSha256', 'diskImageSize', 'diskImageSha256', 'profileName', 'contextName',
    'endpointSocketPathSha256', 'endpointDigest', 'daemonId', 'apiVersion', 'serverVersion',
    'operatingSystem', 'architecture', 'providerName', 'limaInstance', 'processes',
    'sandboxExecutableSha256',
    'sandboxProfileSha256', 'outboundAcquisitionPolicy', 'sandboxPolicyProbeDigest',
    'bootstrapArgvSha256', 'bootstrapEnvironmentSha256', 'bootstrapStdoutSha256',
    'bootstrapStderrSha256', 'observedAt', 'digest'], 'Engine preflight')
  assert(value.schemaVersion === ENGINE_PREFLIGHT_SCHEMA && value.status === 'passed' &&
    value.preMutationRootsAbsent === true && value.zeroMutation === true && value.initialImageCount === 0 &&
    value.initialImageInventorySha256 === sha256(canonicalJSONStringify([])) &&
    value.generationId === config.identity.generationId,
  'Engine preflight is not the exact fresh zero-image authority')
  assert(value.dockerSha256 === config.authority.dockerCLISha256 &&
    value.colimaSha256 === config.authority.colimaSourceSha256 &&
    value.limaPrefixClosureSha256 === config.authority.limaPrefixClosureSha256 &&
    value.limaInfoDigest === config.authority.limaInfoDigest &&
    value.limaSha256 === config.authority.limaSha256 &&
    value.diskImageSize === config.authority.diskImageSize &&
    value.diskImageSha256 === config.authority.diskImageSha256 &&
    value.endpointSocketPathSha256 === config.authority.endpointSocketPathSha256 &&
    value.sandboxExecutableSha256 === config.authority.sandboxExecutableSha256 &&
    value.sandboxProfileSha256 === config.authority.sandboxProfileSha256 &&
    value.profileName === config.private.colimaProfile &&
    value.contextName === config.authority.dockerContext && value.endpointDigest === config.authority.endpointDigest,
  'Engine preflight identity drifted')
  for (const key of ['preMutationAbsenceSha256', 'colimaSha256', 'limaSha256',
    'limaPrefixClosureSha256', 'limaInfoDigest', 'dockerSha256',
    'diskImageSha256', 'endpointSocketPathSha256', 'sandboxExecutableSha256', 'sandboxProfileSha256',
    'sandboxPolicyProbeDigest', 'bootstrapArgvSha256', 'bootstrapEnvironmentSha256',
    'bootstrapStdoutSha256', 'bootstrapStderrSha256']) digest(value[key], `Engine preflight ${key}`)
  assert(typeof value.daemonId === 'string' && value.daemonId.length > 0 && value.daemonId.length <= 256 &&
    typeof value.apiVersion === 'string' && /^\d+\.\d+$/.test(value.apiVersion) &&
    typeof value.serverVersion === 'string' && value.serverVersion.length > 0 &&
    typeof value.providerName === 'string' && value.providerName.length > 0 &&
    Number.isSafeInteger(value.diskImageSize) && value.diskImageSize > 0 &&
    typeof value.observedAt === 'string' && Number.isFinite(Date.parse(value.observedAt)),
  'Engine preflight observation is invalid')
  plain(value.limaInstance, 'Engine preflight Lima instance')
  exactKeys(value.limaInstance, ['name', 'status', 'vmType', 'hostAgentPID', 'driverPID'],
    'Engine preflight Lima instance')
  assert(value.limaInstance.name === config.authority.dockerContext &&
    value.limaInstance.status === 'Running' && value.limaInstance.vmType === 'vz' &&
    Number.isSafeInteger(value.limaInstance.hostAgentPID) && value.limaInstance.hostAgentPID > 1 &&
    Number.isSafeInteger(value.limaInstance.driverPID) && value.limaInstance.driverPID > 1,
  'Engine preflight Lima instance identity drifted')
  validateEngineProcessAuthority(value.processes, value.limaInstance)
  validateEnginePlatform({ operatingSystem: value.operatingSystem, architecture: value.architecture })
  const { digest: recordDigest, ...safe } = value
  assert(recordDigest === sha256(canonicalJSONStringify(safe)), 'Engine preflight digest drifted')
}

function validateActualReleaseManifest(value, config) {
  plain(value, 'actual release manifest')
  exactKeys(value, ['schema_version', 'spec_sha256', 'release_id', 'platform', 'roles', 'artifacts'], 'actual release manifest')
  assert(value.schema_version === 'chora.release-assets-manifest.v1' && value.release_id === config.setup.releaseId &&
    validReleaseIdentifier(value.release_id),
    'actual release manifest identity drifted')
  digest(value.spec_sha256, 'actual release spec digest')
  assert(value.spec_sha256 === sha256(JSON.stringify(actualReleaseSpecProjection(value))), 'actual release spec digest drifted')
  validateReleaseImagePlatform(value.platform, 'actual release-image platform')
  assert(Array.isArray(value.roles) && value.roles.length === O4_ROLES.length, 'actual release roles incomplete')
  const byArtifact = new Map()
  for (let index = 0; index < value.roles.length; index++) {
    const role = value.roles[index]
    plain(role, 'actual release role')
    const wanted = role.alias_of_role === undefined ? ['role', 'artifact_id', 'policy'] : ['role', 'artifact_id', 'alias_of_role', 'policy']
    exactKeys(role, wanted, 'actual release role')
    assert(role.role === O4_ROLES[index] && validReleaseIdentifier(role.artifact_id), 'actual release role order/identity invalid')
    plain(role.policy, 'actual release policy'); exactKeys(role.policy, ['id', 'path', 'sha256'], 'actual release policy')
    assert(validReleaseIdentifier(role.policy.id) && safeRelative(role.policy.path), 'actual release policy identity invalid'); digest(role.policy.sha256, 'actual release policy digest')
    const list = byArtifact.get(role.artifact_id) ?? []; list.push(role); byArtifact.set(role.artifact_id, list)
  }
  assert(Array.isArray(value.artifacts) && value.artifacts.length > 0 && value.artifacts.length <= 16, 'actual release artifacts invalid')
  const artifacts = new Map()
  let previousArtifact = ''
  for (const artifact of value.artifacts) {
    plain(artifact, 'actual release artifact')
    const artifactKeys = ['id', 'recipe', 'context', 'recipe_provenance', 'build_inputs', 'entrypoint', 'license_inventory', 'sbom', 'image']
    if (artifact.runtime !== undefined) artifactKeys.push('runtime')
    exactKeys(artifact, artifactKeys, 'actual release artifact')
    assert(validReleaseIdentifier(artifact.id) && artifact.id > previousArtifact && !artifacts.has(artifact.id),
      'actual release artifacts are unsorted or duplicated')
    previousArtifact = artifact.id; artifacts.set(artifact.id, artifact)
    for (const key of ['recipe', 'license_inventory', 'sbom']) validateFileBinding(artifact[key], `actual release ${key}`)
    plain(artifact.context, 'actual release context'); exactKeys(artifact.context, ['path', 'sha256'], 'actual release context'); assert(safeRelative(artifact.context.path), 'actual release context path invalid'); digest(artifact.context.sha256, 'actual release context digest')
    assert(Array.isArray(artifact.recipe_provenance) && artifact.recipe_provenance.length > 0 &&
      Array.isArray(artifact.build_inputs) && artifact.build_inputs.length > 0 &&
      Array.isArray(artifact.entrypoint) && artifact.entrypoint.length > 0 && artifact.entrypoint.length <= 32,
    'actual release artifact closure invalid')
    let previousPath = ''
    for (const item of artifact.recipe_provenance) {
      validateFileBinding(item, 'actual release provenance'); assert(item.path > previousPath, 'actual release provenance is unsorted or duplicated'); previousPath = item.path
    }
    let previousInput = ''
    for (const input of artifact.build_inputs) {
      plain(input, 'actual release build input'); exactKeys(input, ['name', 'oci_pull_reference'], 'actual release build input')
      assert(/^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*$/.test(input.name) && input.name > previousInput && ociPullRE.test(input.oci_pull_reference),
        'actual release build inputs are invalid, unsorted, or duplicated')
      previousInput = input.name
    }
    for (const argument of artifact.entrypoint) assert(typeof argument === 'string' && argument.length > 0 && argument.length <= 1024 && argument.trim() === argument && !/[\r\n\0]/.test(argument), 'actual release entrypoint invalid')
    plain(artifact.image, 'actual release image'); exactKeys(artifact.image, ['local_docker_config_image_id', 'archive'], 'actual release image')
    imageDigest(artifact.image.local_docker_config_image_id, 'actual release config image')
    plain(artifact.image.archive, 'actual release archive')
    exactKeys(artifact.image.archive, ['format', 'path', 'size', 'sha256'], 'actual release archive')
    assert(artifact.image.archive.format === 'docker-archive' && safeRelative(artifact.image.archive.path) &&
      Number.isSafeInteger(artifact.image.archive.size) && artifact.image.archive.size > 0,
    'actual release archive binding is invalid')
    digest(artifact.image.archive.sha256, 'actual release archive digest')
  }
  assert(byArtifact.size === artifacts.size && [...artifacts.keys()].every((id) => byArtifact.has(id)),
    'actual release contains an unbound or unknown artifact')
  for (const [artifactId, bindings] of byArtifact) {
    assert(artifacts.has(artifactId), 'actual release role references unknown artifact')
    const primaries = bindings.filter((role) => role.alias_of_role === undefined)
    assert(primaries.length === 1, 'actual release shared artifact must have one primary role')
    for (const role of bindings) {
      if (bindings.length === 1) assert(role.alias_of_role === undefined, 'actual release role has a needless alias')
      else if (role.role !== primaries[0].role) assert(role.alias_of_role === primaries[0].role, 'actual release alias drifted')
    }
  }
  const managedArtifactID = value.roles[0].artifact_id
  for (const artifact of value.artifacts) {
    if (artifact.id === managedArtifactID) validateReleaseRuntime(artifact.runtime)
    else assert(artifact.runtime === undefined, 'actual release runtime is placed on a non-managed artifact')
  }
  for (const role of value.roles) {
    const expected = config.authority.roles[role.role]
    const artifact = artifacts.get(role.artifact_id)
    assert(role.artifact_id === expected.artifactId && role.policy.sha256 === expected.policyDigest &&
      artifact.image.archive.sha256 === expected.archiveSha256 &&
      artifact.image.archive.size === expected.archiveSize &&
      artifact.image.local_docker_config_image_id === expected.dockerConfigImageId,
    `actual release role/image binding drifted for ${role.role}`)
  }
}

function validateReleaseRuntime(value) {
  plain(value, 'actual managed runtime'); exactKeys(value, ['pi', 'node'], 'actual managed runtime')
  plain(value.pi, 'actual runtime Pi'); exactKeys(value.pi, ['name', 'version', 'npm_integrity'], 'actual runtime Pi')
  plain(value.node, 'actual runtime Node'); exactKeys(value.node, ['version', 'executable', 'version_output'], 'actual runtime Node')
  const integrity = typeof value.pi.npm_integrity === 'string' && value.pi.npm_integrity.startsWith('sha512-') ? value.pi.npm_integrity.slice(7) : ''
  assert(typeof value.pi.name === 'string' && value.pi.name.startsWith('@') && value.pi.name.includes('/') &&
    versionRE.test(value.pi.version) && /^[A-Za-z0-9+/]+={0,2}$/.test(integrity) && Buffer.from(integrity, 'base64').length === 64,
  'actual managed Pi runtime invalid')
  assert(versionRE.test(value.node.version) && isAbsolute(value.node.executable) && value.node.version_output === `v${value.node.version}`,
    'actual managed Node runtime invalid')
}

function validReleaseIdentifier(value) { return typeof value === 'string' && value.length <= 128 && releaseIdentifierRE.test(value) }

export function validatePrivatePiManifest(value, config) {
  plain(value, 'actual private Pi manifest')
  exactKeys(value, ['schema_version', 'platform', 'package_name', 'version', 'executable', 'files', 'closure_sha256'], 'actual private Pi manifest')
  assert(value.schema_version === 'chora.pi-private-asset.v1' && value.package_name === '@earendil-works/pi-coding-agent' &&
    value.version === '0.84.2' && safeRelative(value.executable), 'actual private Pi identity drifted')
  sameJSON(value.platform, config.identity.platform, 'actual private Pi platform drifted')
  digest(value.closure_sha256, 'actual private Pi closure digest')
  assert(Array.isArray(value.files) && value.files.length >= 2 && value.files.length <= 15_000,
    'actual private Pi files invalid')
  let previous = ''
  for (const file of value.files) {
    plain(file, 'actual private Pi file'); exactKeys(file, ['path', 'mode', 'size', 'sha256'], 'actual private Pi file')
    assert(safeRelative(file.path) && file.path > previous, 'actual private Pi paths unsafe or unsorted'); previous = file.path
    assert(Number.isSafeInteger(file.mode) && file.mode > 0 && file.mode <= 0o777 && (file.mode & 0o022) === 0, 'actual private Pi mode invalid')
    assert(Number.isSafeInteger(file.size) && file.size >= 0, 'actual private Pi size invalid'); digest(file.sha256, 'actual private Pi file digest')
  }
  assert(value.files.some(({ path }) => path === 'package.json') && value.files.some(({ path }) => path === value.executable), 'actual private Pi executable/package missing')
  assert(value.closure_sha256 === privatePiClosureDigest(value.files), 'actual private Pi closure digest drifted')
}

function validateFileBinding(value, label) {
  plain(value, label); exactKeys(value, ['path', 'sha256'], label); assert(safeRelative(value.path), `${label} path invalid`); digest(value.sha256, `${label} digest`)
}

function actualReleaseSpecProjection(value) {
  return {
    schema_version: 'chora.release-assets-spec.v1', release_id: value.release_id,
    platform: value.platform,
    roles: value.roles.map((role) => ({ role: role.role, artifact_id: role.artifact_id,
      ...(role.alias_of_role === undefined ? {} : { alias_of_role: role.alias_of_role }), policy: role.policy })),
    artifacts: value.artifacts.map((artifact) => ({
      id: artifact.id, recipe: artifact.recipe, context: artifact.context,
      recipe_provenance: artifact.recipe_provenance, build_inputs: artifact.build_inputs,
      ...(artifact.runtime === undefined ? {} : { runtime: artifact.runtime }), entrypoint: artifact.entrypoint,
      license_inventory: artifact.license_inventory, sbom: artifact.sbom,
    })),
  }
}

function privatePiClosureDigest(files) {
  const hash = createHash('sha256')
  const frame = (value) => {
    const bytes = Buffer.isBuffer(value) ? value : Buffer.from(value)
    const length = Buffer.alloc(8); length.writeBigUInt64BE(BigInt(bytes.length)); hash.update(length); hash.update(bytes)
  }
  frame('chora.pi-package-closure.v1')
  for (const file of files) {
    frame(file.path); frame(file.mode.toString(8).padStart(4, '0')); frame(String(file.size)); frame(Buffer.from(file.sha256, 'hex'))
  }
  return hash.digest('hex')
}

async function directoryAggregate(root) {
  return (await directoryAggregateWithSnapshot(root)).aggregate
}

async function inspectPrivatePiTree(root, manifest) {
  const snapshot = {}
  for (const file of manifest.files ?? []) {
    const path = join(root, ...(file.path ?? '').split('/'))
    const bytes = await readExactFile(path, 'actual private Pi file', Math.max(file.size ?? 0, 1))
    assert(bytes.length === file.size && sha256(bytes) === file.sha256, 'actual private Pi file drifted')
    const info = await stat(path)
    assert((info.mode & 0o777) === file.mode, 'actual private Pi file mode drifted')
    snapshot[path] = file.sha256
  }
  return { snapshot }
}

async function inspectActualReleaseTree(root, manifest) {
  const bindings = []
  for (const role of manifest.roles ?? []) bindings.push(role.policy)
  for (const artifact of manifest.artifacts ?? []) {
    bindings.push(artifact.recipe, artifact.license_inventory, artifact.sbom, ...(artifact.recipe_provenance ?? []))
  }
  const snapshot = {}
  const bytesByPath = new Map()
  for (const binding of bindings) {
    if (!binding || typeof binding.path !== 'string') continue
    const path = join(root, ...binding.path.split('/'))
    const bytes = await readExactFile(path, 'actual release bound file', MAX_JSON)
    assert(sha256(bytes) === binding.sha256, 'actual release bound file drifted')
    snapshot[path] = binding.sha256
    bytesByPath.set(binding.path, bytes)
  }
  for (const artifact of manifest.artifacts ?? []) {
    const recipe = bytesByPath.get(artifact.recipe?.path)
    assert(recipe !== undefined, 'actual release recipe is unavailable')
    validateDockerfileContract(artifact, recipe.toString('utf8'))
    const context = await releaseDirectoryDigest(root, artifact.context?.path)
    assert(context.digest === artifact.context?.sha256, 'actual release context digest drifted')
    Object.assign(snapshot, context.snapshot)
    plain(artifact.image, 'actual release image')
    exactKeys(artifact.image, ['local_docker_config_image_id', 'archive'], 'actual release image')
    plain(artifact.image.archive, 'actual release archive')
    const archivePath = join(root, ...artifact.image.archive.path.split('/'))
    const archiveBytes = await readExactFile(archivePath, 'actual release Docker archive', MAX_BINARY)
    assert(archiveBytes.length === artifact.image.archive.size &&
      sha256(archiveBytes) === artifact.image.archive.sha256,
    'actual release Docker archive bytes drifted')
    snapshot[archivePath] = artifact.image.archive.sha256
  }
  return { snapshot }
}

function validateDockerfileContract(artifact, recipe) {
  const lines = recipe.split('\n').map((line) => line.trim())
  for (const input of artifact.build_inputs ?? []) {
    const prefix = `ARG ${input.name}`
    const candidates = lines.filter((line) => line === prefix || line.startsWith(`${prefix}=`) || line.startsWith(`${prefix} `))
    assert(candidates.length === 1 && candidates[0] === `${prefix}=${input.oci_pull_reference}`,
      'actual Dockerfile build input is not exactly pinned once')
    const from = `FROM \${${input.name}}`
    assert(lines.filter((line) => line === from || line.startsWith(`${from} `)).length === 1,
      'actual Dockerfile build input is not consumed by exactly one FROM')
  }
  assert(lines.includes(`ENTRYPOINT ${JSON.stringify(artifact.entrypoint)}`), 'actual Dockerfile entrypoint drifted')
  if (artifact.runtime !== undefined) {
    const expected = [
      ['PI_PACKAGE', artifact.runtime.pi.name], ['PI_VERSION', artifact.runtime.pi.version],
      ['PI_INTEGRITY', artifact.runtime.pi.npm_integrity],
    ]
    for (const [name, value] of expected) {
      const prefix = `ARG ${name}`; const candidates = lines.filter((line) => line === prefix || line.startsWith(`${prefix}=`) || line.startsWith(`${prefix} `))
      assert(candidates.length === 1 && candidates[0] === `${prefix}=${value}`, `actual Dockerfile ${name} drifted`)
    }
    assert(recipe.includes(`test "$(node --version)" = "${artifact.runtime.node.version_output}";`),
      'actual Dockerfile Node identity drifted')
  }
}

async function releaseDirectoryDigest(root, relativePath) {
  assert(safeRelative(relativePath), 'actual release context path invalid')
  const directory = join(root, ...relativePath.split('/'))
  const entries = []; const snapshot = {}
  async function walk(current, prefix = '') {
    // Match Go's sort.Strings bytewise ordering used by releaseassets.DirectoryDigest.
    // localeCompare is locale-sensitive and orders lower-case context entries ahead
    // of upper-case Dockerfile/LICENSES entries on macOS, producing a false drift.
    const items = (await readdir(current, { withFileTypes: true })).sort((a, b) =>
      a.name < b.name ? -1 : a.name > b.name ? 1 : 0)
    for (const item of items) {
      assert(!item.isSymbolicLink(), 'actual release context contains symlink')
      const path = join(current, item.name); const name = prefix ? `${prefix}/${item.name}` : item.name
      if (item.isDirectory()) await walk(path, name)
      else {
        assert(item.isFile(), 'actual release context contains non-file')
        const bytes = await readExactFile(path, 'actual release context file', MAX_JSON)
        const info = await stat(path); const mode = (info.mode & 0o777).toString(8).padStart(4, '0'); const fileDigest = sha256(bytes)
        entries.push({ path: name, mode, size: bytes.length, digest: fileDigest }); snapshot[path] = fileDigest
      }
    }
  }
  await walk(directory)
  assert(entries.length > 0, 'actual release context is empty')
  const hash = createHash('sha256')
  for (const entry of entries) {
    for (const value of [entry.path, entry.mode, String(entry.size), entry.digest]) hash.update(`${Buffer.byteLength(value)}:${value}`)
  }
  return { digest: hash.digest('hex'), snapshot }
}

async function directoryAggregateWithSnapshot(root) {
  const entries = []
  const snapshot = {}
  async function walk(directory, prefix = '') {
    const names = (await readdir(directory, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))
    for (const item of names) {
      const path = join(directory, item.name); const name = prefix ? `${prefix}/${item.name}` : item.name
      assert(!item.isSymbolicLink(), 'web tree contains symlink')
      if (item.isDirectory()) await walk(path, name)
      else {
        assert(item.isFile(), 'web tree contains non-file')
        const bytes = await readExactFile(path, 'web artifact', 32 * 1024 * 1024)
        snapshot[path] = sha256(bytes)
        entries.push(`${name}\0${bytes.length}\0${sha256(bytes)}\n`)
      }
    }
  }
  await walk(root)
  assert(entries.length > 0 && entries.length <= 10000, 'web artifact set is empty or unbounded')
  return { aggregate: sha256(entries.join('')), snapshot }
}

async function readExactFile(path, label, max, options = {}) {
  absolute(path, label); await assertNoSymlinkAncestors(path)
  const info = await lstat(path)
  assert(info.isFile() && !info.isSymbolicLink() && info.nlink === 1 && info.size >= 0 && info.size <= max, `${label} must be one bounded immutable regular file`)
  if (options.mode !== undefined) assert((info.mode & 0o777) === options.mode, `${label} mode drifted`)
  return readFile(path)
}

async function assertAbsent(path, label) {
  try { await lstat(path) } catch (error) { if (error.code === 'ENOENT') return; throw error }
  throw new Error(`${label} must be absent`)
}

async function writeNativeJSONMarker(value, path, label, repositoryCapture, markerWriter) {
  const bytes = Buffer.from(`${JSON.stringify(value, null, 2)}\n`)
  const receipt = await writeReadonlyOwnedPath({
    capability: repositoryCapture, path, bytes,
  }, markerWriter)
  assert(receipt.target === path && receipt.sha256 === sha256(bytes) &&
    receipt.byteLength === bytes.length,
  `${label} native publication receipt drifted`)
  return receipt
}

async function writeExclusiveReadonlyBytes(
  bytes, path, label, writeStep = null, repositoryCapture = undefined,
) {
  const cleanupCapability = validateRepositoryCapture(repositoryCapture)
  await assertNoSymlinkAncestors(path)
  let handle
  let createdIdentity = null
  try {
    handle = await open(path, 'wx', 0o600)
    const createdInfo = await handle.stat({ bigint: true })
    assert(createdInfo.isFile() && createdInfo.nlink === 1n &&
      createdInfo.uid === BigInt(process.getuid()),
      `${label} exclusive creation failed`)
    createdIdentity = ownedPathIdentity(createdInfo)
    if (writeStep !== null) await writeStep({ stage: 'write', path })
    await handle.writeFile(bytes)
    await handle.sync()
    if (writeStep !== null) await writeStep({ stage: 'chmod', path })
    await handle.chmod(0o400)
    await handle.sync()
    createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true }))
    if (writeStep !== null) await writeStep({ stage: 'post-chmod', path })
    await handle.close(); handle = undefined
    if (writeStep !== null) await writeStep({ stage: 'validate', path })
    const info = await lstat(path, { bigint: true })
    assert(matchesOwnedPath(info, createdIdentity, 'file') && info.nlink === 1n &&
      Number(info.mode & 0o777n) === 0o400, `${label} immutable creation failed`)
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
        await cleanupOwnedPath({ capability: cleanupCapability, path,
          identity: createdIdentity, disposition: 'regular_file' })
      }
      catch (cleanupError) { cleanupErrors.push(cleanupError) }
    }
    throwWithCleanup(error, cleanupErrors, `${label} creation and cleanup failed`)
  }
}

function authorityDurabilityDependencies(dependencies) {
  plain(dependencies, 'O4 authority durability dependencies')
  const allowed = new Set(['repositoryCapture', 'syncDirectory', 'authorityWriteStep'])
  assert(Object.keys(dependencies).every((key) => allowed.has(key)),
    'O4 authority durability dependencies fields drifted')
  assert(dependencies.syncDirectory === undefined || typeof dependencies.syncDirectory === 'function',
    'O4 authority directory sync dependency is missing')
  assert(dependencies.authorityWriteStep === undefined ||
    typeof dependencies.authorityWriteStep === 'function',
    'O4 authority write-step dependency is invalid')
  return Object.freeze({ syncDirectory: dependencies.syncDirectory ?? syncOwnerDirectory,
    authorityWriteStep: dependencies.authorityWriteStep ?? null,
    repositoryCapture: validateRepositoryCapture(dependencies.repositoryCapture) })
}

async function writeDurableAuthority(bytes, authorityPath, dataRoot, dependencies) {
  const ownedIdentity = await writeExclusiveReadonlyBytes(bytes, authorityPath,
    'O4 fault authority', dependencies.authorityWriteStep, dependencies.repositoryCapture)
  try {
    await dependencies.syncDirectory(dataRoot)
  } catch (error) {
    const cleanupErrors = []
    try {
      await cleanupOwnedPath({ capability: dependencies.repositoryCapture,
        path: authorityPath, identity: ownedIdentity, disposition: 'regular_file' })
    } catch (cleanupError) {
      cleanupErrors.push(cleanupError)
    }
    throwWithCleanup(error, cleanupErrors,
      'O4 fault authority durability sync and cleanup failed')
  }
}

function matchesOwnedPath(info, identity, type) {
  const typeMatches = type === 'file' ? info.isFile() && !info.isSymbolicLink() :
    info.isDirectory() && !info.isSymbolicLink()
  return typeMatches && info.dev.toString() === identity.dev &&
    info.ino.toString() === identity.ino && Number(info.uid) === identity.uid &&
    Number(info.gid) === identity.gid && Number(info.mode) === identity.mode &&
    identity.uid === process.getuid()
}

function throwWithCleanup(primaryError, cleanupErrors, message) {
  if (cleanupErrors.length === 0) throw primaryError
  throw new AggregateError([primaryError, ...cleanupErrors], message, { cause: primaryError })
}

async function syncOwnerDirectory(path) {
  await assertOwnerDirectory(path)
  const handle = await open(path, 'r')
  try {
    await handle.sync()
  } finally {
    await handle.close()
  }
}

async function withPhaseAppendLock(tupleFile, receiptDirectory, repositoryCapture, callback) {
  const cleanupCapability = validateRepositoryCapture(repositoryCapture)
  absolute(tupleFile, 'phase-lock tuple file')
  absolute(receiptDirectory, 'phase-lock receipt directory')
  assert(typeof callback === 'function', 'phase lock callback is missing')
  await assertOwnerDirectory(receiptDirectory)
  const lockPath = join(receiptDirectory, PHASE_APPEND_LOCK)
  try {
    await mkdir(lockPath, { recursive: false, mode: 0o700 })
  } catch (error) {
    if (error.code === 'EEXIST') throw new Error('phase append lock is held or stale')
    throw error
  }
  const lockIdentity = await captureOwnedLockDirectory(lockPath)
  let value
  let callbackError = null
  try {
    value = await callback()
  } catch (error) {
    callbackError = error
  }
  let releaseError = null
  try {
    await cleanupOwnedPath({ capability: cleanupCapability, path: lockPath,
      identity: lockIdentity, disposition: 'empty_directory' })
  }
  catch (error) { releaseError = error }
  if (callbackError !== null && releaseError !== null) {
    throw new AggregateError([callbackError, releaseError],
      'phase append callback and lock release both failed', { cause: callbackError })
  }
  if (callbackError !== null) throw callbackError
  if (releaseError !== null) throw releaseError
  return value
}

async function captureOwnedLockDirectory(path) {
  await assertNoSymlinkAncestors(path)
  const info = await lstat(path, { bigint: true })
  assert(info.isDirectory() && !info.isSymbolicLink() &&
    info.uid === BigInt(process.getuid()) && Number(info.mode & 0o777n) === 0o700,
  'phase append lock must be owner-owned 0700')
  return ownedPathIdentity(info)
}

function assertFinalIdentityBinding(value, tuple, label) {
  plain(value, label)
  assert(value.tupleIdentity === tuple.identity && value.environmentId === tuple.environmentId &&
    value.installId === tuple.installId && value.generationId === tuple.generationId,
  `${label} final tuple identity binding drifted`)
}

function assertFinalValidation(value, tuple, a3Sha256, b1Sha256) {
  plain(value, 'E final evidence validation')
  exactKeys(value, [
    'status', 'tupleIdentity', 'environmentId', 'installId', 'generationId', 'a3Sha256', 'b1Sha256',
  ], 'E final evidence validation')
  assert(value.status === 'passed' && value.tupleIdentity === tuple.identity &&
    value.environmentId === tuple.environmentId && value.installId === tuple.installId &&
    value.generationId === tuple.generationId && value.a3Sha256 === a3Sha256 && value.b1Sha256 === b1Sha256,
  'E final tuple/A3/B1 binding validation failed')
}

function validateO4ScenarioBindings(value) {
  assert(Array.isArray(value) && value.length === O4_SCENARIOS.length,
    'O4 authority requires exactly failure and timeout bindings')
  const tasks = new Set()
  const snapshots = new Set()
  for (let index = 0; index < value.length; index++) {
    const binding = value[index]
    plain(binding, `O4 authority scenario ${index + 1}`)
    exactKeys(binding, ['scenario', 'taskId', 'snapshotId', 'snapshotDigest'], `O4 authority scenario ${index + 1}`)
    assert(binding.scenario === O4_SCENARIOS[index].scenario,
      'O4 authority scenario order drifted')
    assert(taskIDRE.test(binding.taskId), 'O4 authority Task ID is invalid')
    assert(snapshotIDRE.test(binding.snapshotId), 'O4 authority Snapshot ID is invalid')
    digest(binding.snapshotDigest, 'O4 authority Snapshot digest')
    assert(!tasks.has(binding.taskId) && !snapshots.has(binding.snapshotId),
      'O4 authority Task/Snapshot bindings must be distinct')
    tasks.add(binding.taskId)
    snapshots.add(binding.snapshotId)
  }
}

function o4AuthorityDocument(config, tuple, scenarioBindings, binary) {
  return {
    schemaVersion: O4_FAULT_AUTHORITY_SCHEMA,
    status: O4_AUTHORITY_STATUS,
    scope: O4_AUTHORITY_SCOPE,
    tupleIdentity: tuple.identity,
    generationId: tuple.generationId,
    environmentId: tuple.environmentId,
    installId: tuple.installId,
    binarySha256: sha256(binary),
    sourceAggregateSha256: tuple.bindings.sourceAggregateSha256,
    dataRootSha256: sha256(normalize(config.paths.dataRoot)),
    policySha256: O4_POLICY_SHA256,
    scenarios: scenarioBindings.map((binding, index) => ({
      scenario: binding.scenario,
      action: O4_SCENARIOS[index].action,
      taskId: binding.taskId,
      snapshotId: binding.snapshotId,
      snapshotDigest: binding.snapshotDigest,
      attemptSequence: 1,
      agentExecutionProfile: 'standard',
    })),
    claimBoundary: [...O4_CLAIM_BOUNDARY],
  }
}

async function readAndValidateO4Authority(config, tuple, binding) {
  plain(binding, 'O4 restart authority')
  exactKeys(binding, ['authorityPath', 'authoritySha256', 'candidateTuple', 'document'], 'O4 restart authority')
  absolute(binding.authorityPath, 'O4 restart authority path')
  digest(binding.authoritySha256, 'O4 restart authority digest')
  digest(binding.candidateTuple, 'O4 restart candidate tuple')
  assert(binding.candidateTuple === tuple.identity && within(config.paths.dataRoot, binding.authorityPath),
    'O4 restart authority tuple/path drifted')
  const bytes = await readExactFile(binding.authorityPath, 'O4 fault authority', MAX_JSON, { mode: 0o400 })
  assert(sha256(bytes) === binding.authoritySha256, 'O4 fault authority bytes were tampered')
  let document
  try { document = JSON.parse(bytes.toString('utf8')) } catch { throw new Error('O4 fault authority is not JSON') }
  validateO4AuthorityDocument(document, config, tuple)
  assert(bytes.equals(Buffer.from(`${JSON.stringify(document)}\n`)), 'O4 fault authority bytes are not canonical')
  sameJSON(document, binding.document, 'O4 restart authority document drifted')
  return binding
}

function validateO4AuthorityDocument(document, config, tuple) {
  plain(document, 'O4 fault authority')
  exactKeys(document, [
    'schemaVersion', 'status', 'scope', 'tupleIdentity', 'generationId', 'environmentId', 'installId',
    'binarySha256', 'sourceAggregateSha256', 'dataRootSha256', 'policySha256', 'scenarios', 'claimBoundary',
  ], 'O4 fault authority')
  assert(document.schemaVersion === O4_FAULT_AUTHORITY_SCHEMA && document.status === O4_AUTHORITY_STATUS &&
    document.scope === O4_AUTHORITY_SCOPE && document.tupleIdentity === tuple.identity &&
    document.generationId === tuple.generationId && document.environmentId === tuple.environmentId &&
    document.installId === tuple.installId && document.binarySha256 === tuple.bindings.binarySha256 &&
    document.sourceAggregateSha256 === tuple.bindings.sourceAggregateSha256 &&
    document.dataRootSha256 === sha256(normalize(config.paths.dataRoot)) &&
    document.policySha256 === O4_POLICY_SHA256,
  'O4 fault authority frozen tuple/config binding drifted')
  sameJSON(document.claimBoundary, O4_CLAIM_BOUNDARY, 'O4 fault authority claim boundary drifted')
  assert(Array.isArray(document.scenarios) && document.scenarios.length === O4_SCENARIOS.length,
    'O4 fault authority scenario cardinality drifted')
  validateO4ScenarioBindings(document.scenarios.map(({ scenario, taskId, snapshotId, snapshotDigest }) =>
    ({ scenario, taskId, snapshotId, snapshotDigest })))
  for (let index = 0; index < document.scenarios.length; index++) {
    const scenario = document.scenarios[index]
    exactKeys(scenario, [
      'scenario', 'action', 'taskId', 'snapshotId', 'snapshotDigest', 'attemptSequence', 'agentExecutionProfile',
    ], `O4 fault authority scenario ${index + 1}`)
    assert(scenario.action === O4_SCENARIOS[index].action && scenario.attemptSequence === 1 &&
      scenario.agentExecutionProfile === 'standard', 'O4 fault authority scenario contract drifted')
  }
}

async function ensureOwnerDirectory(path) {
  try { await mkdir(path, { recursive: false, mode: 0o700 }) } catch (error) { if (error.code !== 'EEXIST') throw error }
  await assertOwnerDirectory(path)
}

async function assertOwnerDirectory(path, label = 'receipt directory') {
  await assertNoSymlinkAncestors(path)
  const info = await lstat(path)
  assert(info.isDirectory() && !info.isSymbolicLink() && info.uid === process.getuid() && (info.mode & 0o777) === 0o700,
    `${label} must be owner-owned 0700`)
}

async function assertNoSymlinkAncestors(path) {
  absolute(path, 'path')
  let current = path
  while (true) {
    try { const info = await lstat(current); assert(!info.isSymbolicLink(), 'path has a symlink ancestor') } catch (error) { if (error.code !== 'ENOENT') throw error }
    const parent = dirname(current); if (parent === current) break; current = parent
  }
}

async function rehashInputs(snapshot) {
  for (const [path, expected] of Object.entries(snapshot)) {
    const bytes = await readExactFile(path, 'frozen authority input', MAX_BINARY)
    assert(sha256(bytes) === expected, 'frozen authority input drifted')
  }
}

function validateRepositoryCapture(value) {
  plain(value, 'repository capture capability')
  exactKeys(value, ['executableFile', 'executableSha256'], 'repository capture capability')
  absolute(value.executableFile, 'repository capture executable')
  digest(value.executableSha256, 'repository capture executable digest')
  return Object.freeze({ executableFile: value.executableFile,
    executableSha256: value.executableSha256 })
}

function validateMarkerWriterDependencies(value) {
  if (value === undefined) return undefined
  plain(value, 'candidate marker writer dependencies')
  const allowed = new Set(['spawn', 'beforeSpawn', 'timeoutMs'])
  assert(Object.keys(value).every((key) => allowed.has(key)),
    'candidate marker writer dependency keys drifted')
  assert(value.spawn === undefined || typeof value.spawn === 'function',
    'candidate marker writer spawn dependency is invalid')
  assert(value.beforeSpawn === undefined || typeof value.beforeSpawn === 'function',
    'candidate marker writer beforeSpawn dependency is invalid')
  assert(value.timeoutMs === undefined || Number.isSafeInteger(value.timeoutMs) &&
    value.timeoutMs > 0 && value.timeoutMs <= 120_000,
  'candidate marker writer timeout dependency is invalid')
  return Object.freeze({
    ...(value.spawn === undefined ? {} : { spawn: value.spawn }),
    ...(value.beforeSpawn === undefined ? {} : { beforeSpawn: value.beforeSpawn }),
    ...(value.timeoutMs === undefined ? {} : { timeoutMs: value.timeoutMs }),
  })
}

function withMarkerBeforeSpawn(markerWriter, writeStep) {
  if (writeStep === null) return markerWriter
  return Object.freeze({
    ...(markerWriter ?? {}),
    beforeSpawn: async (details) => {
      if (markerWriter?.beforeSpawn !== undefined) await markerWriter.beforeSpawn(details)
      await writeStep(Object.freeze({ stage: 'before-spawn', path: details.target }))
    },
  })
}

function validateRunner(runner) {
  plain(runner, 'runner')
  for (const name of ['exec', 'installImmutable', 'processBoundary', 'start']) assert(typeof runner[name] === 'function', `runner ${name} is missing`)
  return runner
}

function publicInstallSpec(spec) {
  return {
    sourceManifestFileSha256: sha256(spec.sourceManifestFile),
    sourceManifestSha256: spec.sourceManifestSha256,
    sourceAggregateSha256: spec.sourceAggregateSha256,
    sourceFiles: spec.sourceFiles,
    sourceBinarySha256: sha256(spec.sourceBinary),
    sourceWebDirectorySha256: sha256(spec.sourceWebDirectory),
    installRootSha256: sha256(spec.installRoot),
    dataRootSha256: sha256(spec.dataRoot),
    shell: false,
  }
}

function commandDigest(spec) { return sha256(canonicalJSONStringify({ executableSha256: sha256(spec.file), argvShape: spec.argv.map((item, index) => index % 2 === 0 ? item : sha256(item)), shell: spec.shell })) }
function boundedText(value, label) { assert(typeof value === 'string' && Buffer.byteLength(value) <= MAX_OUTPUT, `${label} is oversized`); return value }
function parseJSON(bytes, label) { try { return JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) } }
function safeRelative(value) { return typeof value === 'string' && value.length > 0 && !value.includes('\\') && !value.startsWith('/') && !value.split('/').includes('..') && normalize(value) === value }
function within(parent, child) { const rel = relative(parent, child); return rel !== '' && !rel.startsWith(`..${sep}`) && rel !== '..' && !isAbsolute(rel) }
function overlaps(a, b) { return a === b || within(a, b) || within(b, a) }
function absolute(value, label) { assert(typeof value === 'string' && isAbsolute(value) && normalize(value) === value && resolve(value) === value && !value.includes('\0'), `${label} must be an exact absolute path`); return value }
function validateProductHostPlatform(value) {
  plain(value, 'product host platform')
  exactKeys(value, ['os', 'architecture'], 'product host platform')
  sameJSON(value, PRODUCT_HOST_PLATFORM, 'product host platform unsupported; exact darwin/arm64 is required')
}
function validateEnginePlatform(value) {
  plain(value, 'Engine platform')
  exactKeys(value, ['operatingSystem', 'architecture'], 'Engine platform')
  sameJSON(value, ENGINE_PLATFORM, 'Engine platform unsupported; exact linux/arm64 is required')
}
function validateReleaseImagePlatform(value, label) {
  plain(value, label)
  exactKeys(value, ['os', 'architecture'], label)
  sameJSON(value, RELEASE_IMAGE_PLATFORM, `${label} unsupported; exact linux/arm64 is required`)
}
function digest(value, label) { assert(typeof value === 'string' && digestRE.test(value), `${label} is not SHA-256`) }
function imageDigest(value, label) { assert(typeof value === 'string' && imageDigestRE.test(value), `${label} is not OCI SHA-256`) }
function exactKeys(value, wanted, label) { const actual = Object.keys(value).sort(); const expected = [...wanted].sort(); assert(actual.length === expected.length && actual.every((key, i) => key === expected[i]), `${label} fields drifted`) }
function plain(value, label) { assert(value !== null && typeof value === 'object' && !Array.isArray(value) && (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null), `${label} must be a plain object`) }
function sameJSON(a, b, label) { assert(canonicalJSONStringify(a) === canonicalJSONStringify(b), label) }
function clone(value) { return JSON.parse(JSON.stringify(value)) }
function deepFreeze(value) { if (value && typeof value === 'object' && !Object.isFrozen(value)) { for (const child of Object.values(value)) deepFreeze(child); Object.freeze(value) } return value }
function assert(condition, message) { if (!condition) throw new Error(message) }
export function sha256(value) { return createHash('sha256').update(value).digest('hex') }
export function canonicalJSONStringify(value) { return JSON.stringify(canonical(value)) }
function canonical(value) { if (value === null || typeof value === 'string' || typeof value === 'boolean') return value; if (typeof value === 'number') { assert(Number.isFinite(value), 'non-finite canonical JSON'); return value } if (Array.isArray(value)) return value.map(canonical); plain(value, 'canonical value'); return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canonical(value[key])])) }
