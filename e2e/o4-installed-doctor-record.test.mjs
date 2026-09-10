import assert from 'node:assert/strict'
import { chmod, mkdtemp, readFile, readdir, rename, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'

import {
  ENGINE_QUALIFICATION_SCHEMA, INSTALLED_DOCTOR_REPORT_SCHEMA, MODEL_OBSERVATION_SCHEMA,
  buildInstalledDoctorEvidence, materializeInstalledDoctorEvidence,
  sha256Hex, validateInstalledDoctorEvidenceFiles,
} from './o4-installed-doctor-record.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'

const d = (name) => sha256Hex(`dynamic:${name}`)
const image = (name) => `sha256:${d(`image:${name}`)}`
const identity = { environmentId: `env_${'e'.repeat(48)}`, installId: `ins_${'i'.repeat(32)}`, generationId: `gen_${'g'.repeat(32)}` }
const platform = { os: 'darwin', architecture: 'arm64' }
let captureBuild
const createdRoots = new Set()

test.before(async () => { captureBuild = await buildTestRepositoryCapture() })
test.after(async () => {
  await captureBuild?.cleanup()
  for (const root of createdRoots) await rm(root, { recursive: true, force: true })
})

function roles() {
  const managed = { artifactId: 'managed-pi-runtime', archiveSha256: d('managed-archive'), archiveSize: 101,
    dockerConfigImageId: image('managed'), policyDigest: d('managed-policy') }
  const boundary = { artifactId: 'network-boundary', archiveSha256: d('boundary-archive'), archiveSize: 103,
    dockerConfigImageId: image('boundary'), policyDigest: d('boundary-policy') }
  return { managed_pi_runtime: managed, network_boundary: boundary,
    independent_verifier: { ...managed, policyDigest: d('verifier-policy') },
    capability_probe: { ...managed, policyDigest: d('probe-policy') } }
}

function bindings() {
  return {
    candidate: { candidateManifestSha256: d('candidate'), sourceManifestSha256: d('source-manifest'), sourceAggregateSha256: d('source') },
    product: { binarySha256: d('binary'), webAggregateSha256: d('web') },
    release: { releaseSpecSha256: d('release-spec'), releaseManifestSha256: d('release-manifest'), offlineBundleEvidenceSha256: d('bundle') },
    pi: { selected: 'private', pathProvenanceSha256: d('path-pi'), privateProvenanceSha256: d('private-pi') },
    engine: { qualificationSha256: '0'.repeat(64), endpointEvidenceSha256: d('endpoint-evidence') }, roles: roles(),
  }
}

function authority() {
  return { authFileSha256: d('auth'), caFileSha256: d('ca'), proxyURLSha256: d('proxy'), modelURLSha256: d('url') }
}

function proof(overrides = {}) {
  const r = roles(); const a = authority()
  const engine_identity = { schema_version: 'chora.docker-engine-identity/v1', daemon_id: 'daemon', api_version: '1.47',
    operating_system: 'linux', architecture: 'arm64', context_endpoint_digest: d('endpoint'), provider_name: 'Docker',
    engine_version: '29.6.1', context_name: 'chora-o4', identity_digest: d('engine-identity') }
  const capability_contract = { schema_version: 'chora.docker-capability-probe-contract/v1', contract_version: 1,
    minimum_api_version: '1.44', operating_system: 'linux', architecture: 'arm64',
    probe_image_id: r.capability_probe.dockerConfigImageId, sandbox_policy_digest: r.capability_probe.policyDigest,
    capabilities: ['exact-preexisting-image', 'zero-residue'], contract_digest: d('contract') }
  const engine_qualification = { schema_version: 'chora.docker-engine-qualification/v1',
    engine_identity_digest: engine_identity.identity_digest, probe_contract_digest: capability_contract.contract_digest,
    probe_image_id: capability_contract.probe_image_id, sandbox_policy_digest: capability_contract.sandbox_policy_digest,
    completed_at: '2026-08-30T00:00:00Z', qualification_digest: d('qualification') }
  const role_images = Object.entries(r).map(([role, value]) => ({ role, artifact_id: value.artifactId,
    config_image_id: value.dockerConfigImageId, archive_sha256: value.archiveSha256,
    archive_size: value.archiveSize, policy_sha256: value.policyDigest }))
  return { schema_version: 'chora.installed-product-proof/v1', status: 'passed', generation_id: identity.generationId,
    release_id: 'o4-release', manifest_sha256: d('actual-manifest'), setup_receipt_sha256: d('setup'),
    model_request_authority_sha256: d('model-authority'), docker_operations_read_only: true,
    engine_mutations_attempted: 0, resources_created: false, model_authenticated: true,
    input_binding: { auth_file_sha256: a.authFileSha256, ca_file_sha256: a.caFileSha256,
      proxy_url_sha256: a.proxyURLSha256, model_url_sha256: a.modelURLSha256 },
    engine_identity, capability_contract, engine_qualification, role_images,
    doctor: { status: 'passed', resourcesCreated: false, elapsed: 1, inputFingerprint: d('fingerprint'),
      modelProbe: { maxAttempts: 3, attempts: [{ attempt: 1, outcome: 'authenticated_schema', statusCode: 400, retryable: false, retried: false }] },
      inputEvidence: { authFileSha256: a.authFileSha256, caFileSha256: a.caFileSha256,
        proxyUrlSha256: a.proxyURLSha256, modelUrlSha256: a.modelURLSha256 } }, ...overrides }
}

function request(productProof = proof()) {
  return { productProof, identity, platform, bindings: bindings(), setupReceiptSha256: d('setup'),
    modelRequestAuthoritySha256: d('model-authority'), modelRequestAuthority: authority(),
    releaseId: 'o4-release', manifestSha256: d('actual-manifest'), endpointDigest: d('endpoint'),
    repositoryCapture: captureBuild.capture }
}

async function paths() {
  const root = await mkdtemp(join(tmpdir(), 'chora-dynamic-'))
  createdRoots.add(root)
  return { engineQualificationFile: join(root, 'engine-qualification.json'),
    modelObservationFile: join(root, 'model-observation.json'),
    installedDoctorReportFile: join(root, 'installed-doctor-report.json') }
}

test('projects one strict product proof into three cross-bound 0400 evidence documents', async () => {
  const output = await paths()
  const records = await materializeInstalledDoctorEvidence({ ...request(), outputs: output })
  assert.equal(records.engineQualification.schemaVersion, ENGINE_QUALIFICATION_SCHEMA)
  assert.equal(records.modelObservation.schemaVersion, MODEL_OBSERVATION_SCHEMA)
  assert.equal(records.installedDoctorReport.schemaVersion, INSTALLED_DOCTOR_REPORT_SCHEMA)
  for (const path of [output.engineQualificationFile, output.modelObservationFile, output.installedDoctorReportFile])
    assert.equal((await stat(path)).mode & 0o777, 0o400)
  await validateInstalledDoctorEvidenceFiles(output)
  const publicBytes = Buffer.concat(await Promise.all([output.engineQualificationFile, output.modelObservationFile,
    output.installedDoctorReportFile].map((path) => readFile(path)))).toString('utf8')
  assert.equal(publicBytes.includes('sk-'), false)
  assert.equal(publicBytes.includes('/Users/'), false)
})

test('rejects receipt/model splices and cross-generation replay before any output exists', async () => {
  for (const forged of [
    { setup_receipt_sha256: d('other-setup') },
    { model_request_authority_sha256: d('other-model') },
    { generation_id: `gen_${'x'.repeat(32)}` },
  ]) {
    const output = await paths()
    await assert.rejects(materializeInstalledDoctorEvidence({ ...request(proof(forged)), outputs: output }), /binding drifted/)
    for (const path of [output.engineQualificationFile, output.modelObservationFile, output.installedDoctorReportFile])
      await assert.rejects(stat(path), /ENOENT/)
  }
})

test('requires an exact repository capture before creating any evidence output', async () => {
  const output = await paths()
  const withoutCapture = { ...request(), outputs: output }
  delete withoutCapture.repositoryCapture
  await assert.rejects(materializeInstalledDoctorEvidence(withoutCapture),
    /installed Doctor repository capture/)
  assert.deepEqual(await readdir(dirname(output.engineQualificationFile)), [])
})

test('rejects mutable proof and mutable outputs, and cleans a partial publication', async () => {
  assert.throws(() => buildInstalledDoctorEvidence(request(proof({ resources_created: true }))), /zero-resource/)
  const output = await paths()
  await writeFile(output.modelObservationFile, '{}\n', { mode: 0o400 })
  await assert.rejects(materializeInstalledDoctorEvidence({ ...request(), outputs: output }), /EEXIST/)
  await assert.rejects(stat(output.engineQualificationFile), /ENOENT/)
  const valid = await paths()
  await materializeInstalledDoctorEvidence({ ...request(), outputs: valid })
  await chmod(valid.modelObservationFile, 0o600)
  await assert.rejects(validateInstalledDoctorEvidenceFiles(valid), /0400 evidence/)
})

test('native cleanup preserves a foreign-swapped output and reports the primary failure', async () => {
  const output = await paths()
  const root = dirname(output.engineQualificationFile)
  const displaced = join(root, 'model-observation.owned.json')
  const markerBytes = 'foreign replacement\n'
  try {
    await assert.rejects(materializeInstalledDoctorEvidence({ ...request(), outputs: output }, {
      beforeValidation: async ({ created }) => {
        assert.deepEqual(created.map(({ path }) => path), [output.engineQualificationFile,
          output.modelObservationFile, output.installedDoctorReportFile])
        await rename(output.modelObservationFile, displaced)
        await writeFile(output.modelObservationFile, markerBytes, { mode: 0o600 })
        throw new Error('injected failure after foreign output swap')
      },
    }), (error) => {
      assert.equal(error instanceof AggregateError, true)
      assert.equal(error.cause?.message, 'injected failure after foreign output swap')
      assert.equal(error.errors[0], error.cause)
      assert.equal(error.errors.some((item) => /owned cleanup path identity drifted/.test(item.message)), true)
      return true
    })
    assert.equal(await readFile(output.modelObservationFile, 'utf8'), markerBytes)
    assert.equal((await stat(displaced)).isFile(), true)
    await assert.rejects(stat(output.engineQualificationFile), /ENOENT/)
    await assert.rejects(stat(output.installedDoctorReportFile), /ENOENT/)
  } finally {
    await rm(root, { recursive: true, force: true })
  }
})
