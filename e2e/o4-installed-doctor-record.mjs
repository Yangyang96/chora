import { createHash } from 'node:crypto'
import { lstat, open, readFile } from 'node:fs/promises'
import { basename, isAbsolute, normalize, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { cleanupOwnedPath, ownedPathIdentity,
  validateOwnedPathCapability } from './o4-owned-path-cleanup.mjs'

export const PRODUCT_INSTALLED_PROOF_SCHEMA = 'chora.installed-product-proof/v1'
export const ENGINE_QUALIFICATION_SCHEMA = 'chora.m1-o4-engine-qualification.v2'
export const MODEL_OBSERVATION_SCHEMA = 'chora.m1-o4-model-observation.v1'
export const INSTALLED_DOCTOR_REPORT_SCHEMA = 'chora.m1-o4-installed-doctor-report.v2'
// The installed Doctor report is the sole downstream record. Keep the old
// export name so final-evidence consumers cannot accidentally select a second
// representation of the same proof.
export const INSTALLED_DOCTOR_RECORD_SCHEMA = INSTALLED_DOCTOR_REPORT_SCHEMA
export const PRIVATE_ENVIRONMENT_MARKER_SCHEMA = 'chora.m1-o4-private-environment-marker.v1'
export const O4_ROLES = Object.freeze([
  'managed_pi_runtime', 'network_boundary', 'independent_verifier', 'capability_probe',
])
export const MANAGED_IMAGE_ALIAS_ROLES = Object.freeze([
  'managed_pi_runtime', 'independent_verifier', 'capability_probe',
])
export const NETWORK_BOUNDARY_ROLE = 'network_boundary'

const digestPattern = /^[0-9a-f]{64}$/
const imageDigestPattern = /^sha256:[0-9a-f]{64}$/
const noncePattern = /^[A-Za-z0-9_-]{43}$/
const environmentIDPattern = /^env_[0-9a-f]{48}$/
const opaqueIDPattern = /^(?:ins|gen|cor)_[A-Za-z0-9_-]{24,80}$/
const safeOutputName = /^[A-Za-z0-9][A-Za-z0-9._-]{0,159}\.json$/
const maxJSONBytes = 1024 * 1024
const PRODUCT_HOST_PLATFORM = Object.freeze({ os: 'darwin', architecture: 'arm64' })

const recordKeys = Object.freeze([
  'schemaVersion', 'status', 'environmentId', 'installId', 'generationId', 'platform',
  'bindings', 'bindingDigest', 'setupReceiptSha256', 'modelRequestAuthoritySha256',
  'engineQualificationSha256', 'modelObservationSha256', 'inputFingerprint',
  'readOnly', 'modelAuthenticated', 'resourcesCreated', 'mutationsAttempted', 'digest',
])

export function buildInstalledDoctorEvidence({ productProof, identity, platform, bindings,
  setupReceiptSha256, modelRequestAuthoritySha256, modelRequestAuthority,
  releaseId, manifestSha256, endpointDigest }) {
  validateProductProof(productProof, { identity, bindings, setupReceiptSha256, modelRequestAuthoritySha256,
    modelRequestAuthority, releaseId, manifestSha256, endpointDigest })
  validateProductHostPlatform(platform)
  assertPlainObject(identity, 'installed Doctor identity')
  assertExactKeys(identity, ['environmentId', 'installId', 'generationId'], 'installed Doctor identity')
  assertEnvironmentID(identity.environmentId, 'installed Doctor environment ID')
  assertOpaqueID(identity.installId, 'ins', 'installed Doctor install ID')
  assertOpaqueID(identity.generationId, 'gen', 'installed Doctor generation ID')
  validateBindings(bindings)

  const qualificationSafe = {
    schemaVersion: ENGINE_QUALIFICATION_SCHEMA, status: 'passed', generationId: identity.generationId,
    releaseId: productProof.release_id, manifestSha256: productProof.manifest_sha256,
    setupReceiptSha256, roles: clone(bindings.roles),
    engineIdentity: clone(productProof.engine_identity),
    capabilityContract: clone(productProof.capability_contract),
    engineQualification: clone(productProof.engine_qualification),
    dockerOperationsReadOnly: true, engineMutationsAttempted: 0, resourcesCreated: false,
  }
  const engineQualification = Object.freeze({ ...qualificationSafe,
    digest: sha256Hex(canonicalJSONStringify(qualificationSafe)) })

  const modelSafe = {
    schemaVersion: MODEL_OBSERVATION_SCHEMA, status: 'passed', generationId: identity.generationId,
    setupReceiptSha256, modelRequestAuthoritySha256,
    inputFingerprint: productProof.doctor.inputFingerprint,
    inputBinding: {
      authFileSha256: productProof.input_binding.auth_file_sha256,
      caFileSha256: productProof.input_binding.ca_file_sha256,
      proxyURLSha256: productProof.input_binding.proxy_url_sha256,
      modelURLSha256: productProof.input_binding.model_url_sha256,
    },
    modelProbe: clone(productProof.doctor.modelProbe), authenticated: true, resourcesCreated: false,
  }
  const modelObservation = Object.freeze({ ...modelSafe,
    digest: sha256Hex(canonicalJSONStringify(modelSafe)) })
  const qualificationBytes = encodedJSON(engineQualification)
  const modelBytes = encodedJSON(modelObservation)
  const installedBindings = clone(bindings)
  installedBindings.engine.qualificationSha256 = sha256Hex(qualificationBytes)
  validateBindings(installedBindings)
  const safe = {
    schemaVersion: INSTALLED_DOCTOR_REPORT_SCHEMA, status: 'passed', ...clone(identity),
    platform: clone(platform), bindings: installedBindings,
    bindingDigest: sha256Hex(canonicalJSONStringify(installedBindings)),
    setupReceiptSha256, modelRequestAuthoritySha256,
    engineQualificationSha256: sha256Hex(qualificationBytes),
    modelObservationSha256: sha256Hex(modelBytes), inputFingerprint: productProof.doctor.inputFingerprint,
    readOnly: true, modelAuthenticated: true, resourcesCreated: false, mutationsAttempted: 0,
  }
  const installedDoctorReport = Object.freeze({ ...safe, digest: sha256Hex(canonicalJSONStringify(safe)) })
  assertEngineQualification(engineQualification)
  assertModelObservation(modelObservation)
  assertInstalledDoctorRecord(installedDoctorReport)
  return Object.freeze({ engineQualification, modelObservation, installedDoctorReport })
}

export async function materializeInstalledDoctorEvidence(options, dependencies = {}) {
  const repositoryCapture = validateOwnedPathCapability(options?.repositoryCapture,
    'installed Doctor repository capture')
  assertPlainObject(dependencies, 'installed Doctor materialization dependencies')
  assertExactKeys(dependencies, dependencies.beforeValidation === undefined ? [] : ['beforeValidation'],
    'installed Doctor materialization dependencies')
  assert(dependencies.beforeValidation === undefined ||
    typeof dependencies.beforeValidation === 'function',
  'installed Doctor before-validation dependency is invalid')
  const records = buildInstalledDoctorEvidence(options)
  const paths = options.outputs
  assertPlainObject(paths, 'installed Doctor evidence outputs')
  assertExactKeys(paths, ['engineQualificationFile', 'modelObservationFile', 'installedDoctorReportFile'],
    'installed Doctor evidence outputs')
  assert(basename(paths.engineQualificationFile) === 'engine-qualification.json' &&
    basename(paths.modelObservationFile) === 'model-observation.json' &&
    basename(paths.installedDoctorReportFile) === 'installed-doctor-report.json',
  'installed Doctor evidence output names drifted')
  const entries = [
    [paths.engineQualificationFile, records.engineQualification, 'Engine qualification output'],
    [paths.modelObservationFile, records.modelObservation, 'model observation output'],
    [paths.installedDoctorReportFile, records.installedDoctorReport, 'installed Doctor report output'],
  ]
  const created = []
  try {
    for (const [path, value, label] of entries) {
      const identity = await writeExclusiveOwnerReadOnlyJSON(value, path, label, repositoryCapture)
      created.push({ path, identity })
    }
    if (dependencies.beforeValidation !== undefined) {
      await dependencies.beforeValidation(Object.freeze({
        created: Object.freeze(created.map(({ path, identity }) =>
          Object.freeze({ path, identity }))),
      }))
    }
    await validateInstalledDoctorEvidenceFiles(paths)
  } catch (error) {
    const cleanupErrors = []
    for (const { path, identity } of [...created].reverse()) {
      try {
        await cleanupOwnedPath({ capability: repositoryCapture, path, identity,
          disposition: 'regular_file' })
      } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    }
    if (cleanupErrors.length > 0) throw new AggregateError([error, ...cleanupErrors],
      `installed Doctor materialization and identity-bound cleanup failed: ${error.message}; ${cleanupErrors.map((item) => item.message).join('; ')}`,
    { cause: error })
    throw error
  }
  return records
}

export async function validateInstalledDoctorEvidenceFiles(paths) {
  const qualificationInput = await readExactOwnerReadOnlyJSON(paths.engineQualificationFile, 'Engine qualification')
  const modelInput = await readExactOwnerReadOnlyJSON(paths.modelObservationFile, 'model observation')
  const reportInput = await readExactOwnerReadOnlyJSON(paths.installedDoctorReportFile, 'installed Doctor report')
  assertEngineQualification(qualificationInput.value)
  assertModelObservation(modelInput.value)
  const report = assertInstalledDoctorRecord(reportInput.value)
  assert(report.engineQualificationSha256 === qualificationInput.sha256,
    'installed Doctor/qualification cross-digest drifted')
  assert(report.modelObservationSha256 === modelInput.sha256,
    'installed Doctor/model cross-digest drifted')
  assert(report.setupReceiptSha256 === qualificationInput.value.setupReceiptSha256 &&
    report.setupReceiptSha256 === modelInput.value.setupReceiptSha256,
  'installed Doctor Setup binding drifted')
  assert(report.modelRequestAuthoritySha256 === modelInput.value.modelRequestAuthoritySha256 &&
    report.inputFingerprint === modelInput.value.inputFingerprint,
  'installed Doctor model binding drifted')
  return Object.freeze({ engineQualification: qualificationInput.value,
    modelObservation: modelInput.value, installedDoctorReport: report })
}

// Compatibility helper: it now accepts only the exact materialized v2 report.
export async function buildInstalledDoctorRecord({ doctorReportFile, privateEnvironmentMarkerFile }) {
  const marker = await readPrivateEnvironmentMarker(privateEnvironmentMarkerFile)
  const report = await readExactBoundedJSONFile(doctorReportFile, 'installed Doctor report')
  assertInstalledDoctorRecord(report)
  assertMarkerIdentity(report, marker, 'installed Doctor report')
  return Object.freeze(report)
}

function validateProductProof(proof, expected) {
  assertPlainObject(proof, 'installed product proof')
  assertExactKeys(proof, [
    'schema_version', 'status', 'generation_id', 'release_id', 'manifest_sha256',
    'setup_receipt_sha256', 'model_request_authority_sha256',
    'docker_operations_read_only', 'engine_mutations_attempted', 'resources_created',
    'model_authenticated', 'input_binding', 'engine_identity', 'capability_contract',
    'engine_qualification', 'role_images', 'doctor',
  ], 'installed product proof')
  assert(proof.schema_version === PRODUCT_INSTALLED_PROOF_SCHEMA && proof.status === 'passed',
    'installed product proof schema/status drifted')
  assert(proof.generation_id === expected.identity.generationId && proof.release_id === expected.releaseId &&
    proof.manifest_sha256 === expected.manifestSha256,
  'installed product proof generation/release binding drifted')
  assert(proof.setup_receipt_sha256 === expected.setupReceiptSha256 &&
    proof.model_request_authority_sha256 === expected.modelRequestAuthoritySha256,
  'installed product proof input authority binding drifted')
  assert(proof.docker_operations_read_only === true && proof.engine_mutations_attempted === 0 &&
    proof.resources_created === false && proof.model_authenticated === true,
  'installed product proof is not a read-only authenticated zero-resource pass')
  validateInputBinding(proof.input_binding, expected.modelRequestAuthority)
  validateEngineIdentity(proof.engine_identity, expected.endpointDigest)
  validateCapabilityContract(proof.capability_contract, expected.bindings.roles)
  validateEngineQualificationRecord(proof.engine_qualification, proof.engine_identity, proof.capability_contract)
  validateProofRoles(proof.role_images, expected.bindings.roles)
  validateDoctor(proof.doctor, proof.input_binding)
  assertSafePublicValue(proof)
}

function validateInputBinding(value, authority) {
  assertPlainObject(authority, 'model request authority')
  assertPlainObject(value, 'installed Doctor input binding')
  assertExactKeys(value, ['auth_file_sha256', 'ca_file_sha256', 'proxy_url_sha256', 'model_url_sha256'],
    'installed Doctor input binding')
  const pairs = [
    ['auth_file_sha256', 'authFileSha256'], ['ca_file_sha256', 'caFileSha256'],
    ['proxy_url_sha256', 'proxyURLSha256'], ['model_url_sha256', 'modelURLSha256'],
  ]
  for (const [proofKey, authorityKey] of pairs) {
    assertDigest(value[proofKey], `installed Doctor ${proofKey}`)
    assert(value[proofKey] === authority[authorityKey], `installed Doctor ${proofKey} authority drifted`)
  }
}

function validateEngineIdentity(value, endpointDigest) {
  assertPlainObject(value, 'installed Engine identity')
  assertExactKeys(value, ['schema_version', 'daemon_id', 'api_version', 'operating_system', 'architecture',
    'context_endpoint_digest', 'provider_name', 'engine_version', 'context_name', 'identity_digest'],
  'installed Engine identity')
  assert(value.schema_version === 'chora.docker-engine-identity/v1' &&
    value.operating_system === 'linux' && value.architecture === 'arm64',
  'installed Engine identity contract drifted')
  for (const key of ['daemon_id', 'api_version', 'provider_name', 'engine_version', 'context_name']) {
    assert(typeof value[key] === 'string' && value[key].length > 0 && value[key].length <= 256,
      `installed Engine ${key} is invalid`)
  }
  assertDigest(value.context_endpoint_digest, 'installed Engine endpoint digest')
  assertDigest(value.identity_digest, 'installed Engine identity digest')
  assert(value.context_endpoint_digest === endpointDigest, 'installed Engine endpoint binding drifted')
}

function validateCapabilityContract(value, roles) {
  assertPlainObject(value, 'installed capability contract')
  assertExactKeys(value, ['schema_version', 'contract_version', 'minimum_api_version', 'operating_system',
    'architecture', 'probe_image_id', 'sandbox_policy_digest', 'capabilities', 'contract_digest'],
  'installed capability contract')
  assert(value.schema_version === 'chora.docker-capability-probe-contract/v1' &&
    value.contract_version === 1 && value.minimum_api_version === '1.44' &&
    value.operating_system === 'linux' && value.architecture === 'arm64',
  'installed capability contract identity drifted')
  assertImageDigest(value.probe_image_id, 'installed capability Probe image')
  assert(value.probe_image_id === roles.capability_probe.dockerConfigImageId,
    'installed capability Probe image binding drifted')
  assertDigest(value.sandbox_policy_digest, 'installed capability policy digest')
  assert(value.sandbox_policy_digest === roles.capability_probe.policyDigest,
    'installed capability policy binding drifted')
  assert(Array.isArray(value.capabilities) && value.capabilities.length > 0 && value.capabilities.length <= 32 &&
    value.capabilities.every((item) => typeof item === 'string' && /^[a-z0-9-]+$/.test(item)) &&
    new Set(value.capabilities).size === value.capabilities.length,
  'installed capability set is invalid')
  assertDigest(value.contract_digest, 'installed capability contract digest')
}

function validateEngineQualificationRecord(value, identity, contract) {
  assertPlainObject(value, 'installed Engine qualification')
  assertExactKeys(value, ['schema_version', 'engine_identity_digest', 'probe_contract_digest',
    'probe_image_id', 'sandbox_policy_digest', 'completed_at', 'qualification_digest'],
  'installed Engine qualification')
  assert(value.schema_version === 'chora.docker-engine-qualification/v1',
    'installed Engine qualification schema drifted')
  for (const key of ['engine_identity_digest', 'probe_contract_digest', 'sandbox_policy_digest', 'qualification_digest'])
    assertDigest(value[key], `installed Engine qualification ${key}`)
  assertImageDigest(value.probe_image_id, 'installed Engine qualification Probe image')
  assert(value.engine_identity_digest === identity.identity_digest &&
    value.probe_contract_digest === contract.contract_digest &&
    value.probe_image_id === contract.probe_image_id &&
    value.sandbox_policy_digest === contract.sandbox_policy_digest,
  'installed Engine qualification is not valid for the observed identity/contract')
  assert(typeof value.completed_at === 'string' && value.completed_at.endsWith('Z') &&
    Number.isFinite(Date.parse(value.completed_at)), 'installed Engine qualification completion is invalid')
}

function validateProofRoles(values, expected) {
  assert(Array.isArray(values) && values.length === O4_ROLES.length,
    'installed product proof role cardinality drifted')
  for (const [index, role] of O4_ROLES.entries()) {
    const value = values[index]
    assertPlainObject(value, `${role} installed role`)
    assertExactKeys(value, ['role', 'artifact_id', 'config_image_id', 'archive_sha256', 'archive_size', 'policy_sha256'],
      `${role} installed role`)
    const binding = expected[role]
    assert(value.role === role && value.artifact_id === binding.artifactId &&
      value.config_image_id === binding.dockerConfigImageId && value.archive_sha256 === binding.archiveSha256 &&
      value.archive_size === binding.archiveSize && value.policy_sha256 === binding.policyDigest,
    `${role} installed role binding drifted`)
  }
}

function validateDoctor(value, inputBinding) {
  assertPlainObject(value, 'installed Doctor observation')
  assertExactKeys(value, ['status', 'resourcesCreated', 'elapsed', 'inputFingerprint', 'modelProbe', 'inputEvidence'],
    'installed Doctor observation')
  assert(value.status === 'passed' && value.resourcesCreated === false &&
    Number.isSafeInteger(value.elapsed) && value.elapsed >= 0,
  'installed Doctor observation did not pass without resources')
  assertDigest(value.inputFingerprint, 'installed Doctor input fingerprint')
  assertPlainObject(value.inputEvidence, 'installed Doctor input evidence')
  assertExactKeys(value.inputEvidence, ['authFileSha256', 'caFileSha256', 'proxyUrlSha256', 'modelUrlSha256'],
    'installed Doctor input evidence')
  assert(value.inputEvidence.authFileSha256 === inputBinding.auth_file_sha256 &&
    value.inputEvidence.caFileSha256 === inputBinding.ca_file_sha256 &&
    value.inputEvidence.proxyUrlSha256 === inputBinding.proxy_url_sha256 &&
    value.inputEvidence.modelUrlSha256 === inputBinding.model_url_sha256,
  'installed Doctor input evidence drifted')
  validateModelProbe(value.modelProbe)
}

function validateModelProbe(value) {
  assertPlainObject(value, 'installed Doctor model Probe')
  assertExactKeys(value, ['maxAttempts', 'attempts'], 'installed Doctor model Probe')
  assert(Number.isSafeInteger(value.maxAttempts) && value.maxAttempts >= 1 && value.maxAttempts <= 3 &&
    Array.isArray(value.attempts) && value.attempts.length >= 1 && value.attempts.length <= value.maxAttempts,
  'installed Doctor model Probe bound drifted')
  for (const [index, attempt] of value.attempts.entries()) {
    assertPlainObject(attempt, 'installed Doctor model Probe attempt')
    const keys = Object.keys(attempt).sort()
    const withoutStatus = ['attempt', 'outcome', 'retried', 'retryable'].sort()
    const withStatus = [...withoutStatus, 'statusCode'].sort()
    assert((keys.length === withoutStatus.length && keys.every((key, i) => key === withoutStatus[i])) ||
      (keys.length === withStatus.length && keys.every((key, i) => key === withStatus[i])),
    'installed Doctor model Probe attempt fields drifted')
    assert(attempt.attempt === index + 1 && typeof attempt.outcome === 'string' && attempt.outcome.length > 0 &&
      typeof attempt.retryable === 'boolean' && typeof attempt.retried === 'boolean',
    'installed Doctor model Probe attempt is invalid')
    if ('statusCode' in attempt) assert(Number.isSafeInteger(attempt.statusCode) && attempt.statusCode >= 100 && attempt.statusCode <= 599,
      'installed Doctor model Probe status is invalid')
  }
}

export function assertEngineQualification(value) {
  assertPlainObject(value, 'Engine qualification evidence')
  assertExactKeys(value, ['schemaVersion', 'status', 'generationId', 'releaseId', 'manifestSha256',
    'setupReceiptSha256', 'roles', 'engineIdentity', 'capabilityContract', 'engineQualification',
    'dockerOperationsReadOnly', 'engineMutationsAttempted', 'resourcesCreated', 'digest'],
  'Engine qualification evidence')
  assert(value.schemaVersion === ENGINE_QUALIFICATION_SCHEMA && value.status === 'passed',
    'Engine qualification evidence schema/status drifted')
  assertOpaqueID(value.generationId, 'gen', 'Engine qualification generation ID')
  assert(typeof value.releaseId === 'string' && /^[a-z0-9][a-z0-9._-]{0,127}$/.test(value.releaseId),
    'Engine qualification release ID is invalid')
  assertDigest(value.manifestSha256, 'Engine qualification manifest digest')
  assertDigest(value.setupReceiptSha256, 'Engine qualification Setup receipt digest')
  validateBindings({ candidate: { candidateManifestSha256: '0'.repeat(64), sourceManifestSha256: '1'.repeat(64), sourceAggregateSha256: '2'.repeat(64) }, product: { binarySha256: '3'.repeat(64), webAggregateSha256: '4'.repeat(64) }, release: { releaseSpecSha256: '5'.repeat(64), releaseManifestSha256: '6'.repeat(64), offlineBundleEvidenceSha256: '7'.repeat(64) }, pi: { selected: 'path', pathProvenanceSha256: '8'.repeat(64), privateProvenanceSha256: '9'.repeat(64) }, engine: { qualificationSha256: 'a'.repeat(64), endpointEvidenceSha256: 'b'.repeat(64) }, roles: value.roles })
  validateEngineIdentity(value.engineIdentity, value.engineIdentity.context_endpoint_digest)
  validateCapabilityContract(value.capabilityContract, value.roles)
  validateEngineQualificationRecord(value.engineQualification, value.engineIdentity, value.capabilityContract)
  assert(value.dockerOperationsReadOnly === true && value.engineMutationsAttempted === 0 && value.resourcesCreated === false,
    'Engine qualification evidence is mutable')
  assertSelfDigest(value, 'Engine qualification evidence')
  assertSafePublicValue(value)
  return value
}

export function assertModelObservation(value) {
  assertPlainObject(value, 'model observation evidence')
  assertExactKeys(value, ['schemaVersion', 'status', 'generationId', 'setupReceiptSha256',
    'modelRequestAuthoritySha256', 'inputFingerprint', 'inputBinding', 'modelProbe',
    'authenticated', 'resourcesCreated', 'digest'], 'model observation evidence')
  assert(value.schemaVersion === MODEL_OBSERVATION_SCHEMA && value.status === 'passed' &&
    value.authenticated === true && value.resourcesCreated === false,
  'model observation evidence schema/status drifted')
  assertOpaqueID(value.generationId, 'gen', 'model observation generation ID')
  for (const key of ['setupReceiptSha256', 'modelRequestAuthoritySha256', 'inputFingerprint'])
    assertDigest(value[key], `model observation ${key}`)
  validateInputBinding({ auth_file_sha256: value.inputBinding.authFileSha256,
    ca_file_sha256: value.inputBinding.caFileSha256, proxy_url_sha256: value.inputBinding.proxyURLSha256,
    model_url_sha256: value.inputBinding.modelURLSha256 }, value.inputBinding)
  validateModelProbe(value.modelProbe)
  assertSelfDigest(value, 'model observation evidence')
  assertSafePublicValue(value)
  return value
}

function assertSelfDigest(value, label) {
  assertDigest(value.digest, `${label} digest`)
  const { digest, ...safe } = value
  assert(value.digest === sha256Hex(canonicalJSONStringify(safe)), `${label} digest drifted`)
}

export function assertInstalledDoctorRecord(record) {
  assertPlainObject(record, 'installed Doctor record')
  assertExactKeys(record, recordKeys, 'installed Doctor record')
  assert(record.schemaVersion === INSTALLED_DOCTOR_RECORD_SCHEMA && record.status === 'passed',
    'installed Doctor record schema/status drifted')
  assertEnvironmentID(record.environmentId, 'installed Doctor environment ID')
  assertOpaqueID(record.installId, 'ins', 'installed Doctor install ID')
  assertOpaqueID(record.generationId, 'gen', 'installed Doctor generation ID')
  validateProductHostPlatform(record.platform)
  validateBindings(record.bindings)
  assertDigest(record.bindingDigest, 'installed Doctor binding digest')
  assert(record.bindingDigest === sha256Hex(canonicalJSONStringify(record.bindings)),
    'installed Doctor binding digest drifted')
  for (const key of ['setupReceiptSha256', 'modelRequestAuthoritySha256', 'engineQualificationSha256',
    'modelObservationSha256', 'inputFingerprint']) assertDigest(record[key], `installed Doctor ${key}`)
  assert(record.readOnly === true && record.modelAuthenticated === true &&
    record.resourcesCreated === false && record.mutationsAttempted === 0,
  'installed Doctor record is not a read-only authenticated zero-resource pass')
  assertDigest(record.digest, 'installed Doctor record digest')
  const { digest, ...safe } = record
  assert(digest === sha256Hex(canonicalJSONStringify(safe)), 'installed Doctor record digest drifted')
  assertSafePublicValue(record)
  return record
}

export async function persistInstalledDoctorRecord(record, outputPath, dependencies = undefined) {
  assertInstalledDoctorRecord(record)
  await writeExclusiveOwnerReadOnlyJSON(record, outputPath, 'installed Doctor record output',
    dependencies?.repositoryCapture)
}

async function readExactOwnerReadOnlyJSON(path, label) {
  const bytes = await readExactBoundedFile(path, label)
  const info = await lstat(path, { bigint: true })
  const mode = Number(info.mode & 0o777n)
  assert(mode === 0o400 && info.nlink === 1n &&
    (typeof process.getuid !== 'function' || info.uid === BigInt(process.getuid())),
    `${label} must be exact owner/single-link 0400 evidence`)
  let value
  try { value = JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) }
  return { value, sha256: sha256Hex(bytes) }
}

export async function readPrivateEnvironmentMarker(path) {
  const marker = await readExactBoundedJSONFile(path, 'private environment marker', 64 * 1024)
  assertPlainObject(marker, 'private environment marker')
  assertExactKeys(marker, [
    'schemaVersion', 'markerNonce', 'environmentId', 'installId', 'generationId',
    'credentialCorpusId', 'credentialCorpusSha256',
  ], 'private environment marker')
  assert(marker.schemaVersion === PRIVATE_ENVIRONMENT_MARKER_SCHEMA, 'private environment marker schema drifted')
  assert(typeof marker.markerNonce === 'string' && noncePattern.test(marker.markerNonce),
    'private environment marker nonce is not a 256-bit base64url value')
  assert(marker.environmentId === deriveEnvironmentId(marker.markerNonce),
    'private environment marker does not bind the random opaque environment ID')
  assertOpaqueID(marker.installId, 'ins', 'private marker install ID')
  assertOpaqueID(marker.generationId, 'gen', 'private marker generation ID')
  assertOpaqueID(marker.credentialCorpusId, 'cor', 'private marker credential corpus ID')
  assertDigest(marker.credentialCorpusSha256, 'private marker credential corpus digest')
  return Object.freeze(marker)
}

export function deriveEnvironmentId(markerNonce) {
  assert(typeof markerNonce === 'string' && noncePattern.test(markerNonce),
    'environment marker nonce is not a 256-bit base64url value')
  return `env_${sha256Hex(`chora:m1-o4:environment:${markerNonce}`).slice(0, 48)}`
}

export function validateBindings(bindings) {
  assertPlainObject(bindings, 'installed binding set')
  assertExactKeys(bindings, ['candidate', 'product', 'release', 'pi', 'engine', 'roles'], 'installed binding set')

  assertPlainObject(bindings.candidate, 'candidate binding')
  assertExactKeys(bindings.candidate,
    ['candidateManifestSha256', 'sourceManifestSha256', 'sourceAggregateSha256'], 'candidate binding')
  for (const [name, value] of Object.entries(bindings.candidate)) assertDigest(value, `candidate ${name}`)

  assertPlainObject(bindings.product, 'product binding')
  assertExactKeys(bindings.product, ['binarySha256', 'webAggregateSha256'], 'product binding')
  assertDigest(bindings.product.binarySha256, 'product binary digest')
  assertDigest(bindings.product.webAggregateSha256, 'product web aggregate')

  assertPlainObject(bindings.release, 'release binding')
  assertExactKeys(bindings.release,
    ['releaseSpecSha256', 'releaseManifestSha256', 'offlineBundleEvidenceSha256'], 'release binding')
  for (const [name, value] of Object.entries(bindings.release)) assertDigest(value, `release ${name}`)

  assertPlainObject(bindings.pi, 'Pi provenance binding')
  assertExactKeys(bindings.pi,
    ['selected', 'pathProvenanceSha256', 'privateProvenanceSha256'], 'Pi provenance binding')
  assert(['path', 'private'].includes(bindings.pi.selected), 'Pi provenance selection is invalid')
  assertDigest(bindings.pi.pathProvenanceSha256, 'PATH Pi provenance digest')
  assertDigest(bindings.pi.privateProvenanceSha256, 'private Pi provenance digest')
  assert(bindings.pi.pathProvenanceSha256 !== bindings.pi.privateProvenanceSha256,
    'PATH and private Pi provenance must remain distinct')

  assertPlainObject(bindings.engine, 'Engine binding')
  assertExactKeys(bindings.engine, ['qualificationSha256', 'endpointEvidenceSha256'], 'Engine binding')
  assertDigest(bindings.engine.qualificationSha256, 'qualification evidence digest')
  assertDigest(bindings.engine.endpointEvidenceSha256, 'Engine endpoint evidence digest')
  assert(bindings.engine.qualificationSha256 !== bindings.engine.endpointEvidenceSha256,
    'qualification and Engine endpoint evidence must remain distinct')

  assertPlainObject(bindings.roles, 'role binding set')
  assertExactKeys(bindings.roles, O4_ROLES, 'role binding set')
  for (const role of O4_ROLES) {
    const value = bindings.roles[role]
    assertPlainObject(value, `${role} binding`)
    assertExactKeys(value, [
      'artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId', 'policyDigest',
    ], `${role} binding`)
    assert(['managed-pi-runtime', 'network-boundary'].includes(value.artifactId),
      `${role} artifact ID is invalid`)
    assertDigest(value.archiveSha256, `${role} archive digest`)
    assert(Number.isSafeInteger(value.archiveSize) && value.archiveSize > 0,
      `${role} archive size is invalid`)
    assertImageDigest(value.dockerConfigImageId, `${role} Docker config image ID`)
    assertDigest(value.policyDigest, `${role} policy digest`)
  }
  assertRoleAssetAliases(bindings.roles)
  return bindings
}

export function assertRoleAssetAliases(roles) {
  const managed = roles.managed_pi_runtime
  for (const role of MANAGED_IMAGE_ALIAS_ROLES) {
    const value = roles[role]
    for (const field of ['artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId']) {
      assert(value[field] === managed[field], `${role} does not alias the exact managed artifact ${field}`)
    }
  }
  assert(managed.artifactId === 'managed-pi-runtime', 'managed role artifact ID drifted')
  const boundary = roles[NETWORK_BOUNDARY_ROLE]
  assert(boundary.artifactId === 'network-boundary', 'network boundary artifact ID drifted')
  for (const field of ['artifactId', 'archiveSha256', 'archiveSize', 'dockerConfigImageId']) {
    assert(boundary[field] !== managed[field], `managed and boundary ${field} must remain distinct`)
  }
  return roles
}

export function computeRoleTupleDigest(roles) {
  validateBindings({
    candidate: { candidateManifestSha256: '0'.repeat(64), sourceManifestSha256: '1'.repeat(64), sourceAggregateSha256: '2'.repeat(64) },
    product: { binarySha256: '3'.repeat(64), webAggregateSha256: '4'.repeat(64) },
    release: { releaseSpecSha256: '5'.repeat(64), releaseManifestSha256: '6'.repeat(64), offlineBundleEvidenceSha256: '7'.repeat(64) },
    pi: { selected: 'path', pathProvenanceSha256: '8'.repeat(64), privateProvenanceSha256: '9'.repeat(64) },
    engine: { qualificationSha256: 'a'.repeat(64), endpointEvidenceSha256: 'b'.repeat(64) },
    roles,
  })
  return sha256Hex(canonicalJSONStringify(roles))
}

export function validateProductHostPlatform(platform) {
  assertPlainObject(platform, 'product host platform binding')
  assertExactKeys(platform, ['os', 'architecture'], 'product host platform binding')
  assert(platform.os === PRODUCT_HOST_PLATFORM.os && platform.architecture === PRODUCT_HOST_PLATFORM.architecture,
    'product host platform is unsupported; exact darwin/arm64 is required')
  return platform
}

// Compatibility export for existing O4 evidence modules. This validator now
// has the exact product-host semantics above; Engine and OCI image platforms
// must use their separately named validators at their own boundaries.
export const validatePlatform = validateProductHostPlatform

export function assertMarkerIdentity(value, marker, label) {
  assert(value.environmentId === marker.environmentId && value.installId === marker.installId &&
    value.generationId === marker.generationId, `${label} identity is not bound to the private marker`)
}

export function assertEnvironmentID(value, label) {
  assert(typeof value === 'string' && environmentIDPattern.test(value), `${label} is invalid`)
}

export function assertOpaqueID(value, prefix, label) {
  assert(typeof value === 'string' && value.startsWith(`${prefix}_`) && opaqueIDPattern.test(value), `${label} is invalid`)
}

export function assertDigest(value, label) {
  assert(typeof value === 'string' && digestPattern.test(value), `${label} is not a canonical lowercase SHA-256 digest`)
}

export function assertImageDigest(value, label) {
  assert(typeof value === 'string' && imageDigestPattern.test(value), `${label} is not a canonical OCI SHA-256 digest`)
}

export function assertSafePublicValue(value, key = '') {
  if (Array.isArray(value)) {
    for (const item of value) assertSafePublicValue(item, key)
    return
  }
  if (value !== null && typeof value === 'object') {
    for (const [name, item] of Object.entries(value)) {
      assert(!/^(?:argv|url|daemonId|email|credential|token)(?:Digest|Sha256)?$/i.test(name),
        'public evidence contains a prohibited field')
      assert(!/(?:^|_)(?:root|directory|rawPath|privatePath)$/i.test(name),
        'public evidence contains a raw/private location field')
      assertSafePublicValue(item, name)
    }
    return
  }
  if (typeof value !== 'string') return
  assert(!/(?:\/Users\/|\/home\/|[A-Za-z]:\\Users\\)/.test(value),
    'public evidence contains a private home path')
  assert(!/\bhttps?:\/\//i.test(value), 'public evidence contains a URL')
  assert(!/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/i.test(value), 'public evidence contains an email address')
  assert(!/-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----/.test(value),
    'public evidence contains private key material')
  assert(!/\b(?:sk-|gh[pousr]_|xox[baprs]-)[A-Za-z0-9_-]{16,}\b/.test(value),
    'public evidence contains credential material')
  void key
}

export async function readExactBoundedJSONFile(path, label, maxBytes = maxJSONBytes) {
  const bytes = await readExactBoundedFile(path, label, maxBytes)
  let value
  try { value = JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) }
  return value
}

export async function readExactBoundedFile(path, label, maxBytes = maxJSONBytes) {
  const input = exactAbsolutePath(path, label)
  const info = await lstat(input, { bigint: true })
  assert(info.isFile() && !info.isSymbolicLink() && info.size >= 0n &&
    info.size <= BigInt(maxBytes),
    `${label} must be one bounded regular non-symlink file`)
  return readFile(input)
}

export function exactAbsolutePath(value, label) {
  assert(typeof value === 'string' && value.length > 1 && isAbsolute(value) && normalize(value) === value &&
    resolve(value) === value && !value.includes('\0'), `${label} must be an exact absolute normalized path`)
  return value
}

export async function writeExclusiveOwnerOnlyJSON(value, outputPath, label, repositoryCapture) {
  return writeExclusiveJSON(value, outputPath, label, 0o600, repositoryCapture)
}

async function writeExclusiveOwnerReadOnlyJSON(value, outputPath, label, repositoryCapture) {
  return writeExclusiveJSON(value, outputPath, label, 0o400, repositoryCapture)
}

async function writeExclusiveJSON(value, outputPath, label, finalMode, repositoryCapture) {
  const output = exactAbsolutePath(outputPath, label)
  repositoryCapture = validateOwnedPathCapability(repositoryCapture,
    `${label} owned-path capability`)
  assert(safeOutputName.test(basename(output)), `${label} basename is unsafe`)
  let handle
  let createdIdentity
  try {
    handle = await open(output, 'wx', 0o600)
    createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true }))
    await handle.writeFile(`${JSON.stringify(value, null, 2)}\n`, 'utf8')
    await handle.sync()
    await handle.chmod(finalMode)
    await handle.sync()
    const finalStats = await handle.stat({ bigint: true })
    createdIdentity = ownedPathIdentity(finalStats)
    assert(finalStats.isFile() && !finalStats.isSymbolicLink() && finalStats.nlink === 1n &&
      (typeof process.getuid !== 'function' || finalStats.uid === BigInt(process.getuid())) &&
      Number(finalStats.mode & 0o777n) === finalMode,
    `${label} final owner-only file identity drifted`)
    await handle.close()
    handle = undefined
    const persistedStats = await lstat(output, { bigint: true })
    assert(persistedStats.isFile() && !persistedStats.isSymbolicLink() &&
      persistedStats.nlink === 1n && persistedStats.dev === finalStats.dev &&
      persistedStats.ino === finalStats.ino && persistedStats.mode === finalStats.mode &&
      persistedStats.uid === finalStats.uid && persistedStats.gid === finalStats.gid,
    `${label} final path identity drifted`)
    return createdIdentity
  } catch (error) {
    const cleanupErrors = []
    if (handle !== undefined) {
      try { createdIdentity = ownedPathIdentity(await handle.stat({ bigint: true })) }
      catch (identityError) { cleanupErrors.push(identityError) }
      try { await handle.close() } catch (closeError) { cleanupErrors.push(closeError) }
    }
    if (createdIdentity !== undefined) {
      try {
        await cleanupOwnedPath({ capability: repositoryCapture, path: output,
          identity: createdIdentity, disposition: 'regular_file' })
      } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    }
    if (cleanupErrors.length > 0) throw new AggregateError([error, ...cleanupErrors],
      `${label} write and identity-bound cleanup failed: ${error.message}; ${cleanupErrors.map((item) => item.message).join('; ')}`,
    { cause: error })
    throw error
  }
}

function encodedJSON(value) { return Buffer.from(`${JSON.stringify(value, null, 2)}\n`, 'utf8') }

export function canonicalJSONStringify(value) {
  return JSON.stringify(canonicalValue(value))
}

export function sha256Hex(value) {
  return createHash('sha256').update(value).digest('hex')
}

function canonicalValue(value) {
  if (value === null || typeof value === 'boolean' || typeof value === 'string') return value
  if (typeof value === 'number') {
    assert(Number.isFinite(value), 'canonical JSON contains a non-finite number')
    return value
  }
  if (Array.isArray(value)) return value.map(canonicalValue)
  assertPlainObject(value, 'canonical JSON value')
  return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canonicalValue(value[key])]))
}

function clone(value) {
  return JSON.parse(JSON.stringify(value))
}

function assertPlainObject(value, label) {
  assert(value !== null && typeof value === 'object' && !Array.isArray(value) &&
    (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null),
  `${label} must be a plain object`)
}

function assertExactKeys(value, expected, label) {
  const actual = Object.keys(value).sort()
  const wanted = [...expected].sort()
  assert(actual.length === wanted.length && actual.every((item, index) => item === wanted[index]),
    `${label} fields drifted`)
}

function assertUnique(values, label) {
  assert(new Set(values).size === values.length, `${label} are not unique`)
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

function parseCLI(argv) {
  const allowed = new Set([
    '--doctor-report', '--private-marker', '--output',
    '--service-controller', '--service-controller-sha256',
  ])
  const values = new Map()
  for (let index = 0; index < argv.length; index += 2) {
    const flag = argv[index]
    assert(allowed.has(flag) && index + 1 < argv.length && !values.has(flag),
      'installed Doctor record arguments are invalid')
    values.set(flag, argv[index + 1])
  }
  for (const flag of allowed) assert(typeof values.get(flag) === 'string', 'installed Doctor record argument is missing')
  return {
    doctorReportFile: values.get('--doctor-report'),
    privateEnvironmentMarkerFile: values.get('--private-marker'),
    outputPath: values.get('--output'),
    repositoryCapture: {
      executableFile: values.get('--service-controller'),
      executableSha256: values.get('--service-controller-sha256'),
    },
  }
}

async function main() {
  try {
    const { outputPath, repositoryCapture, ...inputs } = parseCLI(process.argv.slice(2))
    const record = await buildInstalledDoctorRecord(inputs)
    await persistInstalledDoctorRecord(record, outputPath, { repositoryCapture })
    process.stdout.write(`${JSON.stringify({ schemaVersion: INSTALLED_DOCTOR_RECORD_SCHEMA, status: 'passed', digest: record.digest })}\n`)
  } catch {
    process.stdout.write(`${JSON.stringify({ schemaVersion: INSTALLED_DOCTOR_RECORD_SCHEMA, status: 'failed' })}\n`)
    process.exitCode = 1
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main()
