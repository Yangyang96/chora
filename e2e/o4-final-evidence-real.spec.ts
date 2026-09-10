import { createHash } from 'node:crypto'
import { lstat, readFile } from 'node:fs/promises'
import { isAbsolute, normalize, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

import { expect, test } from '@playwright/test'

type JSONMap = Record<string, any>
const nativeImport = new Function('specifier', 'return import(specifier)') as
  (specifier: string) => Promise<JSONMap>

// The repository default may import this file but must register zero tests.
// Only the dedicated config sets this marker before Playwright discovery.
if (process.env.CHORA_O4_FINAL_EVIDENCE_REAL === '1') {
  test.describe.configure({ mode: 'serial' })
  test('forms the final aggregate from committed C/D and one pinned B2 closure/seal boundary', async () => {
    test.setTimeout(30 * 60_000)
    const env = harnessEnvironment()
    await assertTotalSessionBinding(env, 'E')
    const recordModule = await nativeImport(pathToFileURL(
      resolve(process.cwd(), 'e2e/o4-final-evidence-record.mjs')).href)
    const runnerModule = await readPinnedModule(env.runnerModuleFile,
      env.runnerModuleSha256, 'B2 final Runner module')
    exactKeys(runnerModule, ['createO4B2Runner'], 'B2 final Runner module exports')
    const runner = await runnerModule.createO4B2Runner({
      view: 'E', manifestFile: env.totalManifestFile,
      manifestSha256: env.totalManifestSha256,
      sessionAuthorityFile: env.totalSessionAuthorityFile,
    })
    exactKeys(runner, ['closeAllServices', 'sealA3Ledger'], 'B2 final Runner capabilities')

    // E is intentionally read-only with respect to product state. It runs no
    // C/D/UI/OCR journey, starts no infrastructure, and only lets the pinned
    // Runner close registered services and seal the already-recorded A3.
    const result = await recordModule.finalizeO4Evidence({
      paths: {
        tupleFile: env.tupleFile, receiptDirectory: env.receiptDirectory,
        sourceA3LedgerFile: env.sourceA3LedgerFile,
        phaseCManifestFile: env.phaseCManifestFile, phaseCRecordFile: env.phaseCRecordFile,
        phaseCReceiptDirectory: env.phaseCReceiptDirectory,
        phaseDManifestFile: env.phaseDManifestFile, phaseDRecordFile: env.phaseDRecordFile,
        phaseDReceiptDirectory: env.phaseDReceiptDirectory,
        phaseDDisclosureSourceDirectory: env.phaseDDisclosureSourceDirectory,
        phaseDScreenshotDirectory: env.phaseDScreenshotDirectory,
        phaseDScreenshotMetadataDirectory: env.phaseDScreenshotMetadataDirectory,
        phaseDScreenshotOCRDirectory: env.phaseDScreenshotOCRDirectory,
        privateEnvironmentMarkerFile: env.privateEnvironmentMarkerFile,
        installedDoctorRecordFile: env.installedDoctorRecordFile,
        modelObservationFile: env.modelObservationFile,
        credentialCorpusFile: env.credentialCorpusFile,
        candidateManifestFile: env.candidateManifestFile,
        sourceManifestFile: env.sourceManifestFile, sourceBundleRoot: env.sourceBundleRoot,
        binaryFile: env.binaryFile, webDirectory: env.webDirectory,
        releaseSpecFile: env.releaseSpecFile, releaseManifestFile: env.releaseManifestFile,
        offlineBundleEvidenceFile: env.offlineBundleEvidenceFile,
        pathPiProvenanceFile: env.pathPiProvenanceFile,
        privatePiProvenanceFile: env.privatePiProvenanceFile,
        qualificationFile: env.qualificationFile,
        engineEndpointEvidenceFile: env.engineEndpointEvidenceFile,
        runnerModuleFile: env.runnerModuleFile, outputDirectory: env.outputDirectory,
      },
      pins: { runnerModuleSha256: env.runnerModuleSha256 },
      standardResourceLedgerFiles: env.standardResourceLedgerFiles,
      closeTimeoutMs: env.closeTimeoutMs,
    }, { runner, repositoryCapture: {
      executableFile: env.serviceControllerFile,
      executableSha256: env.serviceControllerSha256,
    } })
    expect(result.receipt).toMatchObject({ phase: 'E', sequence: 9, status: 'passed',
      operationDigest: result.sourceA3LedgerSha256, observationDigest: result.b1Sha256 })
  })
}

function harnessEnvironment() {
  const path = (name: string) => exactAbsolute(required(name), name)
  const result: JSONMap = {
    tupleFile: path('CHORA_O4_FINAL_TUPLE_FILE'),
    receiptDirectory: path('CHORA_O4_FINAL_RECEIPT_DIR'),
    sourceA3LedgerFile: path('CHORA_O4_FINAL_SOURCE_A3_LEDGER_FILE'),
    phaseCManifestFile: path('CHORA_O4_FINAL_PHASE_C_MANIFEST_FILE'),
    phaseCRecordFile: path('CHORA_O4_FINAL_PHASE_C_RECORD_FILE'),
    phaseCReceiptDirectory: path('CHORA_O4_FINAL_PHASE_C_RECEIPT_DIR'),
    phaseDManifestFile: path('CHORA_O4_FINAL_PHASE_D_MANIFEST_FILE'),
    phaseDRecordFile: path('CHORA_O4_FINAL_PHASE_D_RECORD_FILE'),
    phaseDReceiptDirectory: path('CHORA_O4_FINAL_PHASE_D_RECEIPT_DIR'),
    phaseDDisclosureSourceDirectory: path('CHORA_O4_FINAL_PHASE_D_DISCLOSURE_SOURCE_DIR'),
    phaseDScreenshotDirectory: path('CHORA_O4_FINAL_PHASE_D_SCREENSHOT_DIR'),
    phaseDScreenshotMetadataDirectory: path('CHORA_O4_FINAL_PHASE_D_SCREENSHOT_METADATA_DIR'),
    phaseDScreenshotOCRDirectory: path('CHORA_O4_FINAL_PHASE_D_SCREENSHOT_OCR_DIR'),
    privateEnvironmentMarkerFile: path('CHORA_O4_FINAL_PRIVATE_MARKER_FILE'),
    installedDoctorRecordFile: path('CHORA_O4_FINAL_INSTALLED_DOCTOR_FILE'),
    modelObservationFile: path('CHORA_O4_FINAL_MODEL_OBSERVATION_FILE'),
    credentialCorpusFile: path('CHORA_O4_FINAL_CREDENTIAL_CORPUS_FILE'),
    candidateManifestFile: path('CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE'),
    sourceManifestFile: path('CHORA_O4_FINAL_SOURCE_MANIFEST_FILE'),
    sourceBundleRoot: path('CHORA_O4_FINAL_SOURCE_BUNDLE_ROOT'),
    binaryFile: path('CHORA_O4_FINAL_BINARY_FILE'),
    webDirectory: path('CHORA_O4_FINAL_WEB_DIRECTORY'),
    releaseSpecFile: path('CHORA_O4_FINAL_RELEASE_SPEC_FILE'),
    releaseManifestFile: path('CHORA_O4_FINAL_RELEASE_MANIFEST_FILE'),
    offlineBundleEvidenceFile: path('CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE'),
    pathPiProvenanceFile: path('CHORA_O4_FINAL_PATH_PI_PROVENANCE_FILE'),
    privatePiProvenanceFile: path('CHORA_O4_FINAL_PRIVATE_PI_PROVENANCE_FILE'),
    qualificationFile: path('CHORA_O4_FINAL_QUALIFICATION_FILE'),
    engineEndpointEvidenceFile: path('CHORA_O4_FINAL_ENGINE_ENDPOINT_EVIDENCE_FILE'),
    runnerModuleFile: path('CHORA_O4_FINAL_B2_RUNNER_MODULE_FILE'),
    outputDirectory: path('CHORA_O4_FINAL_OUTPUT_DIR'),
    runnerModuleSha256: digest(required('CHORA_O4_FINAL_B2_RUNNER_MODULE_SHA256')),
    serviceControllerFile: path('CHORA_O4_FINAL_SERVICE_CONTROLLER_FILE'),
    serviceControllerSha256: digest(required('CHORA_O4_FINAL_SERVICE_CONTROLLER_SHA256')),
    standardResourceLedgerFiles: [1, 2, 3].map((value) =>
      path(`CHORA_O4_FINAL_STANDARD_${value}_RESOURCE_LEDGER_FILE`)),
    closeTimeoutMs: Number.parseInt(required('CHORA_O4_FINAL_CLOSE_TIMEOUT_MS'), 10),
    totalManifestFile: path('CHORA_O4_TOTAL_MANIFEST_FILE'),
    totalManifestSha256: digest(required('CHORA_O4_TOTAL_MANIFEST_SHA256')),
    totalSessionAuthorityFile: path('CHORA_O4_TOTAL_SESSION_AUTHORITY_FILE'),
    totalTupleIdentity: digest(required('CHORA_O4_TOTAL_TUPLE_IDENTITY')),
    totalRunnerSha256: digest(required('CHORA_O4_TOTAL_RUNNER_SHA256')),
  }
  if (!Number.isSafeInteger(result.closeTimeoutMs) || result.closeTimeoutMs < 50 ||
    result.closeTimeoutMs > 120_000) throw new Error('CHORA_O4_FINAL_CLOSE_TIMEOUT_MS is invalid')
  return result
}

async function assertTotalSessionBinding(env: JSONMap, phase: 'E') {
  const manifestBytes = await readPinnedFile(env.totalManifestFile, env.totalManifestSha256,
    'O4 total manifest', 16 * 1024 * 1024)
  const manifest = parseJSON(manifestBytes, 'O4 total manifest')
  expect(manifest).toMatchObject({ schemaVersion: 'chora.m1-o4-total-input.v1',
    status: 'authorized', acceptanceClass: 'real_acceptance' })
  expect(manifest.selfDigest).toBe(sha256Text(canonical({ ...manifest, selfDigest: null })))
  expect(manifest.runner?.moduleFile).toBe(env.runnerModuleFile)
  expect(manifest.runner?.moduleSha256).toBe(env.runnerModuleSha256)
  expect(manifest.runner?.moduleSha256).toBe(env.totalRunnerSha256)
  expect(manifest.candidate?.tupleIdentity).toBeNull()
  const phaseEnvironment = manifest.phases?.[phase]?.environment
  expect(phaseEnvironment && typeof phaseEnvironment === 'object').toBe(true)
  for (const [name, value] of Object.entries(phaseEnvironment)) expect(process.env[name]).toBe(value)
  const authorityBytes = await readPinnedFile(env.totalSessionAuthorityFile, undefined,
    'O4 Runner session authority', 4 * 1024 * 1024)
  const authority = parseJSON(authorityBytes, 'O4 Runner session authority')
  expect(authority).toMatchObject({ schemaVersion: 'chora.m1-o4-runner-session-authority.v1',
    status: 'exclusive', manifestSha256: env.totalManifestSha256,
    runnerSha256: env.totalRunnerSha256, tupleIdentity: env.totalTupleIdentity, sequenceStart: 0 })
  expect(authority.socketPathDigest).toBe(sha256Text(String(manifest.runner.controlSocketPath)))
  const { authorityDigest, ...safe } = authority
  expect(authorityDigest).toBe(sha256Text(canonical(safe)))
  const tupleBytes = await readPinnedFile(env.tupleFile, undefined, 'O4 total phase-E tuple', 4 * 1024 * 1024)
  expect(parseJSON(tupleBytes, 'O4 total phase-E tuple').identity).toBe(env.totalTupleIdentity)
}

async function readPinnedFile(path: string, expected: string | undefined, label: string, max: number) {
  const before = await lstat(path)
  if (!before.isFile() || before.isSymbolicLink() || before.nlink !== 1 ||
    before.size <= 0 || before.size > max || (before.mode & 0o777) !== 0o400) {
    throw new Error(`${label} is not one immutable bounded file`)
  }
  const bytes = await readFile(path)
  if (expected !== undefined && sha256(bytes) !== expected) throw new Error(`${label} SHA-256 drifted`)
  return bytes
}

function parseJSON(bytes: Buffer, label: string) {
  try { return JSON.parse(bytes.toString('utf8')) as JSONMap } catch { throw new Error(`${label} is not JSON`) }
}

function canonical(value: any): string {
  if (value === null || typeof value !== 'object') return JSON.stringify(value)
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`
}

function sha256Text(value: string) { return createHash('sha256').update(value).digest('hex') }

async function readPinnedModule(path: string, expected: string, label: string) {
  const before = await lstat(path)
  if (!before.isFile() || before.isSymbolicLink() || before.nlink !== 1 ||
    (before.mode & 0o777) !== 0o400) throw new Error(`${label} is not one immutable file`)
  const bytes = await readFile(path)
  if (sha256(bytes) !== expected) throw new Error(`${label} SHA-256 drifted`)
  const loaded = await nativeImport(pathToFileURL(path).href)
  if (sha256(await readFile(path)) !== expected) throw new Error(`${label} changed during import`)
  return loaded
}

function required(name: string) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value }
function exactAbsolute(value: string, name: string) { if (!isAbsolute(value) || normalize(value) !== value || resolve(value) !== value) throw new Error(`${name} must be exact absolute`); return value }
function digest(value: string) { if (!/^[0-9a-f]{64}$/.test(value)) throw new Error('Runner SHA-256 is invalid'); return value }
function exactKeys(value: JSONMap, keys: string[], label: string) { if (!value || typeof value !== 'object' || JSON.stringify(Object.keys(value).sort()) !== JSON.stringify([...keys].sort())) throw new Error(`${label} keys drifted`) }
function sha256(value: Buffer) { return createHash('sha256').update(value).digest('hex') }
