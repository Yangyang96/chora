#!/usr/bin/env node
import { createHash, randomBytes } from 'node:crypto'
import { spawn } from 'node:child_process'
import { createServer } from 'node:net'
import { lstat, open, readFile, readdir, readlink, realpath, stat } from 'node:fs/promises'
import { basename, dirname, isAbsolute, join, normalize, relative, resolve, sep } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

import {
  cleanupOwnedPath, createOwnedDirectory, ownedPathIdentity, writeReadonlyOwnedPath,
} from './o4-owned-path-cleanup.mjs'
import { assertO4DarwinUnixSocketPaths, o4ColimaTopology } from './o4-darwin-unix-sockets.mjs'

const TOTAL_SCHEMA = 'chora.m1-o4-total-input.v1'
const SESSION_SCHEMA = 'chora.m1-o4-runner-session-authority.v1'
const COMPLETION_SCHEMA = 'chora.m1-o4-total-completion.v1'
const ENGINE_PREFLIGHT_SCHEMA = 'chora.m1-o4-engine-preflight.v3'
const ENGINE_RESIDUE_SCHEMA = 'chora.m1-o4-fresh-engine-residue.v1'
const ENGINE_BOOTSTRAP_FAILURE_SCHEMA = 'chora.m1-o4-engine-bootstrap-failure.v3'
const ENGINE_PROCESS_IDENTITY_TIMEOUT_MS = 5_000
const ENGINE_PROCESS_IDENTITY_POLL_MS = 25
const LIMA_INSTANCE_FORMAT = '{"name":{{json .Name}},"status":{{json .Status}},"vmType":{{json .VMType}},"hostAgentPID":{{.HostAgentPID}},"driverPID":{{.DriverPID}}}'
const MAX_JSON = 16 * 1024 * 1024
const MAX_PIN = 256 * 1024 * 1024
const digestRE = /^[0-9a-f]{64}$/
const productIDRE = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/
const bootstrapSandboxProfile = `(version 1)
(allow default)
(deny network*)
(allow network* (local unix-socket))
(allow network* (remote unix-socket))
(allow network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
`
const phaseOrder = Object.freeze(['C', 'D', 'E'])
const stageRuntimeRootBindings = Object.freeze([
  ['userRoot', 'user'], ['installRoot', 'install'], ['dataRoot', 'data'],
  ['stateRoot', 'state'], ['receiptRoot', 'receipts'], ['evidenceRoot', 'evidence'],
  ['probeRuntimeRoot', 'probe-runtime'], ['dockerConfigRoot', 'docker-config'],
])
const controlRootBindings = Object.freeze([
  ['runnerControlRoot', 'r'], ['colimaHome', 'c'], ['temporaryRoot', 't'],
])
const driverCreatedRootKeys = Object.freeze([
  'userRoot', 'evidenceRoot', 'runnerControlRoot', 'colimaHome',
  'dockerConfigRoot', 'temporaryRoot', 'probeRuntimeRoot',
])
const receiptPhases = Object.freeze([
  'preflight', 'install', 'product_doctor', 'setup', 'doctor', 'serve', 'C', 'D', 'E',
])
const workspaceRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const executionModuleClosure = Object.freeze([
  'e2e/o4-total-real.mjs',
  'e2e/o4-darwin-unix-sockets.mjs',
  'e2e/o4-candidate-install-orchestrator.mjs',
  'e2e/o4-repository-authority.mjs',
  'e2e/o4-owned-path-cleanup.mjs',
  'e2e/o4-profile-reuse-real.spec.ts',
  'e2e/o4-profile-reuse-record.mjs',
  'e2e/o4-profile-reuse-observer.mjs',
  'e2e/o4-recovery-residue-real.spec.ts',
  'e2e/o4-recovery-residue-record.mjs',
  'e2e/o4-final-evidence-real.spec.ts',
  'e2e/o4-final-evidence-record.mjs',
  'e2e/o4-image-reuse-record.mjs',
  'e2e/o4-installed-evidence-gate.mjs',
  'e2e/o4-installed-doctor-record.mjs',
])
const phaseEnvironment = Object.freeze({
  C: Object.freeze([
    'CHORA_O4_PROFILE_REUSE_BASE_URL', 'CHORA_O4_PROFILE_REUSE_TUPLE_FILE',
    'CHORA_O4_PROFILE_REUSE_SOURCE_A3_LEDGER_FILE', 'CHORA_O4_PROFILE_REUSE_HANDOFF_RECEIPT_DIR',
    'CHORA_O4_PROFILE_REUSE_OBSERVER_FILE', 'CHORA_O4_PROFILE_REUSE_OBSERVER_SHA256',
    'CHORA_O4_PROFILE_REUSE_SUPERVISOR_RESOURCE_DIR', 'CHORA_O4_PROFILE_REUSE_RUNNER_PROTOCOL_DIR',
    'CHORA_O4_PROFILE_REUSE_OUTPUT_DIR', 'CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_FILE',
    'CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_SHA256',
  ]),
  D: Object.freeze([
    'CHORA_O4_RECOVERY_RESIDUE_BASE_URL', 'CHORA_O4_RECOVERY_RESIDUE_TUPLE_FILE',
    'CHORA_O4_RECOVERY_RESIDUE_PHASE_C_RECEIPT_FILE', 'CHORA_O4_RECOVERY_RESIDUE_HANDOFF_RECEIPT_DIR',
    'CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE', 'CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_SHA256',
    'CHORA_O4_RECOVERY_RESIDUE_B2_RUNNER_MODULE_FILE', 'CHORA_O4_RECOVERY_RESIDUE_B2_RUNNER_MODULE_SHA256',
    'CHORA_O4_RECOVERY_RESIDUE_AUTHORITY_FILE', 'CHORA_O4_RECOVERY_RESIDUE_AUTHORIZED_RESTART_RECEIPT_FILE',
    'CHORA_O4_RECOVERY_RESIDUE_ORPHAN_RESTART_RECEIPT_FILE', 'CHORA_O4_RECOVERY_RESIDUE_SOURCE_A3_LEDGER_FILE',
    'CHORA_O4_RECOVERY_RESIDUE_VERIFIER_LEDGER_FILE', 'CHORA_O4_RECOVERY_RESIDUE_AUTHORITY_CONSUMPTION_DIR',
    'CHORA_O4_RECOVERY_RESIDUE_SUPERVISOR_RESOURCE_DIR', 'CHORA_O4_RECOVERY_RESIDUE_GENERATION_REFERENCE_DIR',
    'CHORA_O4_RECOVERY_RESIDUE_PUBLIC_RESIDUE_FILE', 'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROL_FILE',
    'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_FILE', 'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_SHA256',
    'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_REQUEST_FILE', 'CHORA_O4_RECOVERY_RESIDUE_OCR_EXECUTABLE_FILE',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_EXECUTABLE_SHA256',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_ENGINE_BINDING_FILE',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_ENGINE_BINDING_SHA256',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_EXECUTABLE_FILE',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_EXECUTABLE_SHA256',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_PROFILE_FILE',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_PROFILE_SHA256',
    'CHORA_O4_RECOVERY_RESIDUE_OCR_LANGUAGE',
    'CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_DIR', 'CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_METADATA_DIR',
    'CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_OCR_DIR', 'CHORA_O4_RECOVERY_RESIDUE_OUTPUT_DIR',
    'CHORA_O4_RECOVERY_RESIDUE_BINDING_DIGEST',
  ]),
  E: Object.freeze([
    'CHORA_O4_FINAL_TUPLE_FILE', 'CHORA_O4_FINAL_RECEIPT_DIR',
    'CHORA_O4_FINAL_SOURCE_A3_LEDGER_FILE', 'CHORA_O4_FINAL_PHASE_C_MANIFEST_FILE',
    'CHORA_O4_FINAL_PHASE_C_RECORD_FILE', 'CHORA_O4_FINAL_PHASE_C_RECEIPT_DIR',
    'CHORA_O4_FINAL_PHASE_D_MANIFEST_FILE', 'CHORA_O4_FINAL_PHASE_D_RECORD_FILE',
    'CHORA_O4_FINAL_PHASE_D_RECEIPT_DIR', 'CHORA_O4_FINAL_PHASE_D_DISCLOSURE_SOURCE_DIR',
    'CHORA_O4_FINAL_PHASE_D_SCREENSHOT_DIR', 'CHORA_O4_FINAL_PHASE_D_SCREENSHOT_METADATA_DIR',
    'CHORA_O4_FINAL_PHASE_D_SCREENSHOT_OCR_DIR', 'CHORA_O4_FINAL_PRIVATE_MARKER_FILE',
    'CHORA_O4_FINAL_INSTALLED_DOCTOR_FILE', 'CHORA_O4_FINAL_MODEL_OBSERVATION_FILE',
    'CHORA_O4_FINAL_CREDENTIAL_CORPUS_FILE',
    'CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE', 'CHORA_O4_FINAL_SOURCE_MANIFEST_FILE',
    'CHORA_O4_FINAL_SOURCE_BUNDLE_ROOT', 'CHORA_O4_FINAL_BINARY_FILE',
    'CHORA_O4_FINAL_WEB_DIRECTORY', 'CHORA_O4_FINAL_RELEASE_SPEC_FILE',
    'CHORA_O4_FINAL_RELEASE_MANIFEST_FILE', 'CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE',
    'CHORA_O4_FINAL_PATH_PI_PROVENANCE_FILE', 'CHORA_O4_FINAL_PRIVATE_PI_PROVENANCE_FILE',
    'CHORA_O4_FINAL_QUALIFICATION_FILE', 'CHORA_O4_FINAL_ENGINE_ENDPOINT_EVIDENCE_FILE',
    'CHORA_O4_FINAL_B2_RUNNER_MODULE_FILE', 'CHORA_O4_FINAL_OUTPUT_DIR',
    'CHORA_O4_FINAL_STANDARD_1_RESOURCE_LEDGER_FILE', 'CHORA_O4_FINAL_STANDARD_2_RESOURCE_LEDGER_FILE',
    'CHORA_O4_FINAL_STANDARD_3_RESOURCE_LEDGER_FILE', 'CHORA_O4_FINAL_B2_RUNNER_MODULE_SHA256',
    'CHORA_O4_FINAL_CLOSE_TIMEOUT_MS', 'CHORA_O4_FINAL_SERVICE_CONTROLLER_FILE',
    'CHORA_O4_FINAL_SERVICE_CONTROLLER_SHA256',
  ]),
})

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    process.stderr.write(`O4 total real failed: ${error?.message ?? error}\n`)
    process.exitCode = 1
  })
}

async function main() {
  const cli = parseCLI(process.argv.slice(2))
  let input = await preflight(cli)
  let parentRunnerCreated = false
  let phaseECompleted = false
  let engineMutationAttempted = false
  let freshRootIdentities
  let session
  let workflowError
  try {
    await revalidatePrivateEvidenceSnapshot(input.privateEvidenceSnapshot, input.manifest)
    freshRootIdentities = await createFreshRoots(input)
    input = Object.freeze({ ...input, freshRootIdentities })
    await revalidatePrivateEvidenceSnapshot(input.privateEvidenceSnapshot, input.manifest)
    engineMutationAttempted = true
    await bootstrapFreshEngine(input)
    const inspected = await input.candidateModule.inspectAuthenticatedCandidate(input.candidateConfig, {
      repositoryCapture: input.repositoryCapture,
    })
    bindStaticCandidateSnapshot(input.staticInspection, inspected, input.candidateConfig)
    sameJSON(input.candidateConfig.identity, input.manifest.identity,
      'total manifest candidate identity drifted')
    input = Object.freeze({ ...input, inspected })
    await revalidatePrivateEvidenceSnapshot(input.privateEvidenceSnapshot, input.manifest)
    session = await createSessionAuthority(input)
    const evidenceAuthority = await buildProductEvidenceAuthority(input)
    const runnerModule = await importPinnedRunner(input.manifest, input.manifestSha256)
    const runner = await runnerModule.createO4B2Runner({
      view: 'install', manifestFile: cli.manifestFile, manifestSha256: input.manifestSha256,
      sessionAuthorityFile: input.manifest.runner.sessionAuthorityFile,
    })
    parentRunnerCreated = true
    await revalidatePrivateEvidenceSnapshot(input.privateEvidenceSnapshot, input.manifest)
    await input.candidateModule.executeCandidateInstall(input.candidateConfig, {
      runner, o4EvidenceAuthority: evidenceAuthority,
      repositoryCapture: input.repositoryCapture,
    })
    await revalidateBoundary(input, 6)
    for (let index = 0; index < phaseOrder.length; index++) {
      const phase = phaseOrder[index]
      await invokePhase(input, phase)
      await revalidateBoundary(input, 7 + index)
      if (phase === 'E') phaseECompleted = true
    }
    await postflight(input, session)
  } catch (error) {
    workflowError = error
    if (parentRunnerCreated && !phaseECompleted) {
      try { await closeWithoutSeal(input) }
      catch (cleanupError) {
        workflowError = new Error('O4 total failed and parent service cleanup failed', {
          cause: new AggregateError([workflowError, cleanupError]),
        })
      }
    }
  }
  if (!engineMutationAttempted && freshRootIdentities !== undefined) {
    try { await cleanupFreshRootSetup(input, freshRootIdentities) }
    catch (cleanupError) {
      workflowError = new Error('O4 total pre-Engine fresh-root cleanup failed', {
        cause: workflowError ? new AggregateError([workflowError, cleanupError]) : cleanupError,
      })
    }
  }
  let engineResidue
  if (engineMutationAttempted) {
    try { engineResidue = await cleanupFreshEngine(input) }
    catch (cleanupError) {
      workflowError = new Error('O4 total fresh Engine cleanup failed', {
        cause: workflowError ? new AggregateError([workflowError, cleanupError]) : cleanupError,
      })
    }
  }
  if (workflowError) throw workflowError
  await writeCompletion(input, session, engineResidue)
}

function parseCLI(argv) {
  let manifestFile; let manifestSha256
  for (let index = 0; index < argv.length; index += 2) {
    const flag = argv[index]; const value = argv[index + 1]
    assert(value !== undefined, `missing value for ${flag}`)
    if (flag === '--manifest' && manifestFile === undefined) manifestFile = exactAbsolute(value, 'manifest')
    else if (flag === '--manifest-sha256' && manifestSha256 === undefined) manifestSha256 = digest(value, 'manifest SHA-256')
    else throw new Error(`unknown or duplicate O4 total argument ${flag}`)
  }
  assert(argv.length === 4 && manifestFile && manifestSha256,
    'usage: node e2e/o4-total-real.mjs --manifest <0400-file> --manifest-sha256 <digest>')
  return Object.freeze({ manifestFile, manifestSha256 })
}

async function preflight(cli) {
  exactKeys(cli, ['manifestFile', 'manifestSha256'], 'O4 total preflight CLI')
  exactAbsolute(cli.manifestFile, 'manifest')
  digest(cli.manifestSha256, 'manifest SHA-256')
  assert(process.env.DOCKER_HOST === undefined,
    'inherited DOCKER_HOST is forbidden before any fresh Engine mutation')
  const manifestBytes = await read0400(cli.manifestFile, 'O4 total manifest', MAX_JSON)
  assert(sha256(manifestBytes) === cli.manifestSha256, 'O4 total manifest raw digest drifted')
  const manifest = parseJSON(manifestBytes, 'O4 total manifest')
  validateManifest(manifest)
  assert(manifest.acceptanceClass === 'real_acceptance',
    'synthetic_non_acceptance manifests can never execute the real total entrypoint')
  const withNullDigest = { ...manifest, selfDigest: null }
  assert(manifest.selfDigest === sha256(canonicalJSONStringify(withNullDigest)),
    'O4 total manifest self-digest drifted')
  await assertAllPins(manifest)
  const repositoryCapture = Object.freeze({
    executableFile: manifest.serviceController.executableFile,
    executableSha256: manifest.serviceController.sha256,
  })
  const freshRootParents = await validateFreshRoots(manifest)
  const configBytes = await read0400(manifest.candidate.configFile, 'candidate config', MAX_JSON)
  assert(sha256(configBytes) === manifest.candidate.configSha256, 'candidate config pin drifted')
  const candidateConfig = parseJSON(configBytes, 'candidate config')
  bindCandidateRoots(candidateConfig, manifest)
  await validateCandidateDynamicAbsence(candidateConfig, manifest)
  await verifyExecutionModuleClosure(candidateConfig)
  await verifyRunnerSourceBinding(candidateConfig, manifest)
  const candidateModule = await importCandidateModule(candidateConfig)
  const staticInspection = await candidateModule.inspectStaticCandidate(candidateConfig, { repositoryCapture })
  sameJSON(staticInspection.identity, candidateConfig.identity,
    'static candidate inspection identity drifted')
  assert(staticInspection.inputSnapshot && typeof staticInspection.inputSnapshot === 'object' &&
    Object.keys(staticInspection.inputSnapshot).length > 0,
  'static candidate inspection input snapshot is unavailable')
  await absent(manifest.runner.sessionAuthorityFile, 'Runner session authority')
  await absent(manifest.engine.zeroImagePreflightFile, 'fresh Engine zero-image preflight')
  await absent(manifest.engine.cleanupResidueFile, 'fresh Engine cleanup residue')
  await absent(manifest.completionFile, 'O4 total completion')
  const privateEvidenceSnapshot = await readPrivateEvidenceSnapshot(manifest)
  return Object.freeze({ cli, manifest, manifestBytes, manifestSha256: cli.manifestSha256,
    candidateConfig, candidateModule, repositoryCapture, staticInspection, privateEvidenceSnapshot,
    freshRootParents })
}

export async function preflightO4TotalReal(cli) {
  return preflight(cli)
}

function bindStaticCandidateSnapshot(staticInspection, inspected, config) {
  sameJSON(staticInspection.identity, config.identity,
    'pre-bootstrap static candidate identity drifted')
  sameJSON(staticInspection.modelAuthority, inspected.modelAuthority,
    'pre-bootstrap static model authority drifted')
  const staticPaths = Object.keys(staticInspection.inputSnapshot).sort()
  const authenticatedPaths = Object.keys(inspected.inputSnapshot).sort()
  const expectedAuthenticatedPaths = [...staticPaths, config.paths.preflightFile].sort()
  sameJSON(authenticatedPaths, expectedAuthenticatedPaths,
    'authenticated candidate snapshot is not the static snapshot plus exact v2 preflight')
  for (const path of staticPaths) assert(
    inspected.inputSnapshot[path] === staticInspection.inputSnapshot[path],
    `static candidate input changed during fresh Engine bootstrap: ${path}`)
}

function validateManifest(value) {
  exactKeys(value, ['schemaVersion', 'status', 'acceptanceClass', 'selfDigest', 'identity',
    'candidate', 'repository', 'runner', 'serviceController', 'ocr', 'engine', 'playwright', 'roots', 'phases',
    'postflight', 'timeouts', 'completionFile'], 'O4 total manifest')
  assert(value.schemaVersion === TOTAL_SCHEMA && value.status === 'authorized',
    'O4 total manifest schema/status drifted')
  assert(value.acceptanceClass === 'real_acceptance' || value.acceptanceClass === 'synthetic_non_acceptance',
    'O4 total acceptance class is invalid')
  exactKeys(value.identity, ['environmentId', 'installId', 'generationId', 'platform'], 'total identity')
  sameJSON(value.identity.platform, { os: 'darwin', architecture: 'arm64' },
    'O4 total host must be exact darwin/arm64')
  exactKeys(value.candidate, ['configFile', 'configSha256', 'tupleFile', 'tupleIdentity'], 'candidate binding')
  assert(value.candidate.tupleIdentity === null,
    'pre-bootstrap total manifest tuple identity must remain runtime-derived')
  exactKeys(value.repository, ['root', 'commit', 'closureSha256'], 'repository authority')
  assert(/^[0-9a-f]{40}$/.test(value.repository.commit), 'repository commit is invalid')
  digest(value.repository.closureSha256, 'repository closure')
  exactKeys(value.runner, ['moduleFile', 'moduleSha256', 'controlSocketPath', 'sessionAuthorityFile'], 'Runner binding')
  exactKeys(value.serviceController, ['executableFile', 'sha256', 'requestFile'], 'service controller binding')
  exactKeys(value.ocr, ['executableFile', 'executableSha256', 'engineBindingFile',
    'engineBindingSha256', 'sandboxExecutableFile', 'sandboxExecutableSha256',
    'sandboxProfileFile', 'sandboxProfileSha256', 'language'], 'OCR binding')
  exactKeys(value.engine, ['colimaFile', 'colimaSha256', 'limaPrefixRoot',
    'limaPrefixClosureSha256', 'limaInfoDigest', 'limaFile', 'limaSha256', 'limaWrapperFile',
    'limaWrapperSha256', 'limaTemplatesRoot', 'limaTemplatesClosureSha256',
    'limaGuestAgentFile', 'limaGuestAgentSize', 'limaGuestAgentSha256',
    'dockerFile', 'dockerSha256', 'diskImageFile', 'diskImageSize', 'diskImageSha256',
    'sandboxExecutableFile', 'sandboxExecutableSha256', 'sandboxProfileFile',
    'sandboxProfileSha256', 'profileName', 'contextName', 'endpointSocketPath',
    'endpointDigest', 'cpus', 'memoryGiB', 'diskGiB', 'zeroImagePreflightFile',
    'cleanupResidueFile'], 'fresh Engine binding')
  exactKeys(value.playwright, ['nodeFile', 'nodeSha256', 'cliFile', 'cliSha256',
    'packageRoot', 'packageClosureSha256', 'browserRoot', 'browserClosureSha256'],
  'Playwright binding')
  exactKeys(value.roots, ['userRoot', 'installRoot', 'dataRoot', 'stateRoot',
    'receiptRoot', 'evidenceRoot', 'runnerControlRoot', 'probeRuntimeRoot', 'colimaHome',
    'dockerConfigRoot', 'temporaryRoot'], 'fresh roots')
  exactKeys(value.phases, phaseOrder, 'phase bindings')
  for (const phase of phaseOrder) {
    exactKeys(value.phases[phase], ['configFile', 'configSha256', 'environment'], `phase ${phase}`)
    exactKeys(value.phases[phase].environment, phaseEnvironment[phase], `phase ${phase} environment`)
    for (const [name, item] of Object.entries(value.phases[phase].environment)) {
      if (phase === 'D' && name === 'CHORA_O4_RECOVERY_RESIDUE_BINDING_DIGEST') {
        assert(item === null, 'phase D binding digest must remain runtime-derived')
        continue
      }
      assert(typeof item === 'string' && item.length > 0 && !item.includes('\0'), `phase ${phase} environment value is invalid`)
    }
  }
  assert(value.phases.C.environment.CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_FILE ===
    value.serviceController.executableFile &&
    value.phases.C.environment.CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_SHA256 ===
      value.serviceController.sha256 &&
    value.phases.E.environment.CHORA_O4_FINAL_SERVICE_CONTROLLER_FILE ===
      value.serviceController.executableFile &&
    value.phases.E.environment.CHORA_O4_FINAL_SERVICE_CONTROLLER_SHA256 ===
      value.serviceController.sha256,
  'phase C/E owned-path controller binding drifted')
  exactKeys(value.postflight, ['sourceA3LedgerFile', 'finalEvidenceDirectory',
    'phaseCManifestFile', 'phaseCRecordFile', 'phaseDManifestFile', 'phaseDRecordFile',
    'phaseDDisclosureSourceDirectory', 'phaseDScreenshotDirectory',
    'phaseDScreenshotMetadataDirectory', 'phaseDScreenshotOCRDirectory', 'publicResidueFile'],
  'total postflight')
  exactKeys(value.timeouts, ['engineMs', 'phaseMs', 'controllerMs', 'exporterMs', 'closeMs'], 'total timeouts')
  assert(Number.isSafeInteger(value.timeouts.engineMs) && value.timeouts.engineMs >= 1_000 &&
    value.timeouts.engineMs <= 30 * 60_000, 'fresh Engine timeout is invalid')
  assert(Number.isSafeInteger(value.timeouts.phaseMs) && value.timeouts.phaseMs >= 50 &&
    value.timeouts.phaseMs <= 4 * 60 * 60_000, 'total phase timeout is invalid')
  for (const name of ['controllerMs', 'exporterMs', 'closeMs']) assert(
    Number.isSafeInteger(value.timeouts[name]) && value.timeouts[name] >= 50 &&
    value.timeouts[name] <= 120_000, `total timeout ${name} is invalid`)
  assert(value.ocr.language === 'eng', 'OCR language must be exactly eng')
  assert(productIDRE.test(value.engine.profileName) && value.engine.profileName !== 'default' &&
    productIDRE.test(value.engine.contextName) && !['default', 'desktop-linux', 'colima'].includes(value.engine.contextName),
  'fresh Engine profile/context must be unique non-default product identifiers')
  validateFreshEngineContextName(value.engine)
  assert(Number.isSafeInteger(value.engine.diskImageSize) && value.engine.diskImageSize > 0 &&
    value.engine.diskImageSize <= 64 * 1024 * 1024 * 1024,
  'fresh Engine disk image size is invalid')
  for (const name of ['cpus', 'memoryGiB', 'diskGiB']) assert(
    Number.isSafeInteger(value.engine[name]) && value.engine[name] >= 1 && value.engine[name] <= 256,
    `fresh Engine ${name} is invalid`)
  assert(value.engine.diskGiB >= value.engine.memoryGiB,
    'fresh Engine disk must not be smaller than memory')
  assert(value.engine.endpointDigest === sha256(`unix://${value.engine.endpointSocketPath}`),
    'fresh Engine endpoint digest does not bind its exact Unix socket')
  assert(value.engine.limaFile === join(value.engine.limaPrefixRoot, 'bin/limactl') &&
    value.engine.limaWrapperFile === join(value.engine.limaPrefixRoot, 'bin/lima') &&
    value.engine.limaTemplatesRoot === join(value.engine.limaPrefixRoot, 'share/lima/templates') &&
    value.engine.limaGuestAgentFile === join(value.engine.limaPrefixRoot,
      'share/lima/lima-guestagent.Linux-aarch64.gz') &&
    value.engine.limaGuestAgentSize === 7_251_420 && basename(value.engine.dockerFile) === 'docker' &&
    !overlaps(value.engine.limaPrefixRoot, dirname(value.engine.dockerFile)),
  'canonical Engine executable/prefix topology drifted')
  for (const candidate of pathValues(value)) exactAbsolute(candidate, 'total manifest path')
  assertO4DarwinUnixSocketPaths(value)
  for (const candidate of digestValues(value)) digest(candidate, 'total manifest pin')
}

async function assertAllPins(manifest) {
  const pins = [
    ['data', manifest.runner.moduleFile, manifest.runner.moduleSha256, 'B2 Runner module', MAX_JSON],
    ['executable', manifest.serviceController.executableFile, manifest.serviceController.sha256, 'service controller', MAX_PIN],
    ['executable', manifest.ocr.executableFile, manifest.ocr.executableSha256, 'offline OCR executable', MAX_PIN],
    ['data', manifest.ocr.engineBindingFile, manifest.ocr.engineBindingSha256,
      'system-managed OCR engine binding', MAX_JSON],
    ['systemExecutable', manifest.ocr.sandboxExecutableFile, manifest.ocr.sandboxExecutableSha256,
      'OCR sandbox executable', 2 * 1024 * 1024],
    ['data', manifest.ocr.sandboxProfileFile, manifest.ocr.sandboxProfileSha256,
      'OCR sandbox profile', MAX_JSON],
    ['executable', manifest.engine.colimaFile, manifest.engine.colimaSha256, 'Colima executable', MAX_PIN],
    ['executable', manifest.engine.limaFile, manifest.engine.limaSha256, 'canonical limactl executable', MAX_PIN],
    ['executable', manifest.engine.limaWrapperFile,
      manifest.engine.limaWrapperSha256, 'canonical Lima wrapper', MAX_PIN],
    ['data', manifest.engine.limaGuestAgentFile, manifest.engine.limaGuestAgentSha256,
      'canonical Lima Linux-aarch64 guest agent', MAX_PIN],
    ['executable', manifest.engine.dockerFile, manifest.engine.dockerSha256, 'Docker executable', MAX_PIN],
    ['largeData', manifest.engine.diskImageFile, manifest.engine.diskImageSha256,
      'fresh Engine disk image', manifest.engine.diskImageSize],
    ['systemExecutable', manifest.engine.sandboxExecutableFile,
      manifest.engine.sandboxExecutableSha256, 'fresh Engine sandbox executable', 2 * 1024 * 1024],
    ['data', manifest.engine.sandboxProfileFile, manifest.engine.sandboxProfileSha256,
      'fresh Engine bootstrap sandbox profile', MAX_JSON],
    ['executable', manifest.playwright.nodeFile, manifest.playwright.nodeSha256, 'Node executable', MAX_PIN],
    ['data', manifest.playwright.cliFile, manifest.playwright.cliSha256, 'Playwright CLI', MAX_JSON],
    ['executable', manifest.phases.C.environment.CHORA_O4_PROFILE_REUSE_OBSERVER_FILE,
      manifest.phases.C.environment.CHORA_O4_PROFILE_REUSE_OBSERVER_SHA256,
      'profile reuse observer', MAX_JSON],
    ...phaseOrder.map((phase) => ['data', manifest.phases[phase].configFile,
      manifest.phases[phase].configSha256, `Playwright ${phase} config`, MAX_JSON]),
  ]
  for (const [kind, path, expected, label, max] of pins) {
    if (kind === 'largeData') {
      const observed = await stableOwnerFileDigest(path, label, max, 0o400)
      assert(observed.sha256 === expected && observed.size === manifest.engine.diskImageSize,
        'fresh Engine disk image size or SHA-256 drifted')
      continue
    }
    const bytes = kind === 'executable' ? await read0500Executable(path, label, max) :
      kind === 'systemExecutable' ? await read0755SystemExecutable(path, label, max) :
      await read0400(path, label, max)
    assert(sha256(bytes) === expected, `${label} pin drifted`)
    if (label === 'fresh Engine bootstrap sandbox profile') assert(bytes.toString('utf8') === bootstrapSandboxProfile,
      'fresh Engine bootstrap sandbox profile is not the exact local/Unix-only deny-egress policy')
  }
  assert(within(manifest.playwright.packageRoot, manifest.playwright.cliFile),
    'Playwright CLI escapes the pinned package closure')
  assert(!overlaps(manifest.playwright.packageRoot, manifest.playwright.browserRoot),
    'Playwright package and browser closures overlap')
  assert((await stableDirectoryClosure(manifest.playwright.packageRoot,
    'Playwright package closure')).digest === manifest.playwright.packageClosureSha256,
  'Playwright package closure pin drifted')
  assert((await stableDirectoryClosure(manifest.playwright.browserRoot,
    'Playwright browser closure')).digest === manifest.playwright.browserClosureSha256,
  'Playwright browser closure pin drifted')
  const limaPrefix = await stableDirectoryClosure(manifest.engine.limaPrefixRoot,
    'canonical Lima prefix')
  const limaTemplates = await stableDirectoryClosure(manifest.engine.limaTemplatesRoot,
    'canonical Lima templates')
  const prefixFiles = limaPrefix.entries.filter((entry) => entry.type === 'file')
  const prefixDirectories = limaPrefix.entries.filter((entry) => entry.type === 'directory')
  const templateFiles = limaTemplates.entries.filter((entry) => entry.type === 'file')
  const templateDirectories = limaTemplates.entries.filter((entry) => entry.type === 'directory')
  assert(limaPrefix.rootMode === 0o700 &&
    limaPrefix.digest === manifest.engine.limaPrefixClosureSha256 &&
    prefixFiles.length === 124 && prefixDirectories.length === 7 &&
    limaPrefix.totalBytes === 39_872_398 && limaTemplates.rootMode === 0o700 &&
    limaTemplates.digest === manifest.engine.limaTemplatesClosureSha256 &&
    templateFiles.length === 121 && templateDirectories.length === 3 &&
    limaTemplates.totalBytes === 148_059 &&
    limaPrefix.entries.every((entry) => entry.type === 'file' || entry.type === 'directory') &&
    limaTemplates.entries.every((entry) => entry.type === 'file' || entry.type === 'directory') &&
    limaPrefix.entries.every((entry) => entry.mode === (entry.type === 'directory' ? 0o700 :
      entry.path.startsWith('bin/') ? 0o500 : 0o400)) &&
    limaTemplates.entries.every((entry) => entry.mode ===
      (entry.type === 'directory' ? 0o700 : 0o400)),
  'canonical Lima portable prefix closure drifted')
  assert(prefixFiles.some((entry) => entry.path === 'bin/limactl' &&
      entry.sha256 === manifest.engine.limaSha256) &&
    prefixFiles.some((entry) => entry.path === 'bin/lima' &&
      entry.sha256 === manifest.engine.limaWrapperSha256) &&
    prefixFiles.some((entry) => entry.path === 'share/lima/lima-guestagent.Linux-aarch64.gz' &&
      entry.size === manifest.engine.limaGuestAgentSize &&
      entry.sha256 === manifest.engine.limaGuestAgentSha256) &&
    templateFiles.some((entry) => entry.path === 'default.yaml') &&
    templateFiles.some((entry) => entry.path === '_images/ubuntu.yaml') &&
    templateFiles.some((entry) => entry.path === '_default/mounts.yaml'),
  'canonical Lima portable prefix required entries drifted')
  const limaInfoTemplates = templateFiles.filter((entry) => entry.path.endsWith('.yaml'))
    .map((entry) => join(manifest.engine.limaTemplatesRoot, ...entry.path.split('/')))
    .sort()
  assert(limaInfoTemplates.length === 120 && new Set(limaInfoTemplates).size === 120 &&
    sha256(canonicalJSONStringify({
      templates: limaInfoTemplates,
      guestAgent: manifest.engine.limaGuestAgentFile,
      hostOS: 'darwin', hostArch: 'aarch64',
    })) === manifest.engine.limaInfoDigest,
  'canonical Lima info authority drifted')
  const runnerModule = await import(pathToFileURL(manifest.runner.moduleFile).href)
  exactKeys(runnerModule, ['createO4B2Runner'], 'pinned B2 Runner exports')
  assert(typeof runnerModule.createO4B2Runner === 'function', 'B2 Runner factory is unavailable')
  assert(sha256(await read0400(manifest.runner.moduleFile, 'reopened B2 Runner module', MAX_JSON)) ===
    manifest.runner.moduleSha256, 'B2 Runner changed during import')
}

async function validateFreshRoots(manifest) {
  for (const path of freshEnginePreMutationPaths(manifest)) {
    await assertNoSymlinkAncestors(path)
    await absent(path, 'fresh Engine pre-mutation profile/socket/disk/context/store path')
  }
  const roots = Object.values(manifest.roots)
  for (let left = 0; left < roots.length; left++) {
    for (let right = left + 1; right < roots.length; right++) {
      assert(!overlaps(roots[left], roots[right]), 'fresh total roots are not pairwise disjoint')
    }
  }
  const setup = freshRootSetup(manifest)
  await assertNoSymlinkAncestors(setup.runtimeParent)
  await absent(setup.runtimeParent, 'fresh stage runtime parent')
  const stageParentIdentity = await assertOwnerDirectory(
    dirname(setup.runtimeParent), 'fresh stage parent')
  const controlParentIdentity = await assertOwnerDirectory(setup.controlParent, 'fresh control parent')
  for (const root of roots) {
    await assertNoSymlinkAncestors(root)
    await absent(root, 'fresh total root')
  }
  assert(within(manifest.roots.runnerControlRoot, manifest.runner.controlSocketPath) &&
    within(manifest.roots.runnerControlRoot, manifest.runner.sessionAuthorityFile),
  'Runner control files escape the fresh Runner-control root')
  assert(within(manifest.roots.colimaHome, manifest.engine.endpointSocketPath),
    'fresh Engine endpoint escapes COLIMA_HOME')
  assert(within(manifest.roots.evidenceRoot, manifest.postflight.finalEvidenceDirectory) &&
    within(manifest.roots.evidenceRoot, manifest.completionFile) &&
    within(manifest.roots.evidenceRoot, manifest.engine.zeroImagePreflightFile) &&
    within(manifest.roots.evidenceRoot, manifest.engine.cleanupResidueFile),
  'final evidence/completion escapes the fresh evidence root')
  return Object.freeze({ ...setup, stageParentIdentity, controlParentIdentity })
}

function freshRootSetup(manifest) {
  const runtimeParent = dirname(manifest.roots.userRoot)
  const controlParent = dirname(manifest.roots.runnerControlRoot)
  for (const [key, name] of stageRuntimeRootBindings) assert(
    manifest.roots[key] === join(runtimeParent, name),
    `fresh stage-runtime root binding drifted: ${key}`)
  for (const [key, name] of controlRootBindings) assert(
    manifest.roots[key] === join(controlParent, name),
    `fresh control root binding drifted: ${key}`)
  assert(!overlaps(runtimeParent, controlParent), 'fresh setup parents overlap')
  return Object.freeze({ runtimeParent, controlParent })
}

export async function validateO4FreshRoots(manifest) {
  return validateFreshRoots(manifest)
}

function freshEnginePreMutationPaths(manifest) {
  const topology = o4ColimaTopology(manifest)
  const contextDirectory = sha256(manifest.engine.contextName)
  return Object.freeze([
    manifest.engine.endpointSocketPath,
    topology.profileRoot,
    topology.limaInstanceRoot,
    topology.limaDiskRoot,
    topology.storeFile,
    join(manifest.roots.dockerConfigRoot, 'config.json'),
    join(manifest.roots.dockerConfigRoot, 'contexts', 'meta', contextDirectory, 'meta.json'),
  ])
}

function bindCandidateRoots(config, manifest) {
  const bindings = {
    installRoot: 'installRoot', dataRoot: 'dataRoot', stateRoot: 'stateRoot',
    receiptDirectory: 'receiptRoot', probeRuntimeRoot: 'probeRuntimeRoot',
  }
  for (const [candidateKey, rootKey] of Object.entries(bindings)) {
    assert(config.paths?.[candidateKey] === manifest.roots[rootKey], `candidate ${candidateKey} root drifted`)
  }
  assert(config.paths.tupleFile === manifest.candidate.tupleFile,
    'candidate tuple path drifted')
  assert(config.paths.repositoryRoot === manifest.repository.root &&
    config.authority.repositoryCommit === manifest.repository.commit &&
    config.authority.repositoryClosureSha256 === manifest.repository.closureSha256,
  'candidate/total repository authority drifted')
  assert(config.paths.colimaSourceFile === manifest.engine.colimaFile &&
    config.authority.colimaSourceSha256 === manifest.engine.colimaSha256,
  'candidate Colima executable binding drifted')
  assert(config.paths.dockerCLIFile === manifest.engine.dockerFile &&
    config.authority.dockerCLISha256 === manifest.engine.dockerSha256,
  'candidate Docker executable binding drifted')
  assert(config.private.colimaProfile === manifest.engine.profileName &&
    config.authority.dockerContext === manifest.engine.contextName &&
    config.authority.endpointDigest === manifest.engine.endpointDigest,
  'candidate fresh Engine profile/context/endpoint binding drifted')
  assert(config.authority.limaPrefixClosureSha256 === manifest.engine.limaPrefixClosureSha256 &&
    config.authority.limaInfoDigest === manifest.engine.limaInfoDigest &&
    config.authority.limaSha256 === manifest.engine.limaSha256 &&
    config.authority.diskImageSize === manifest.engine.diskImageSize &&
    config.authority.diskImageSha256 === manifest.engine.diskImageSha256 &&
    config.authority.endpointSocketPathSha256 === sha256(manifest.engine.endpointSocketPath) &&
    config.authority.sandboxExecutableSha256 === manifest.engine.sandboxExecutableSha256 &&
    config.authority.sandboxProfileSha256 === manifest.engine.sandboxProfileSha256,
  'candidate pinned fresh Engine closure binding drifted')
  assert(config.paths.preflightFile === manifest.engine.zeroImagePreflightFile,
    'candidate v2 fresh Engine preflight output path drifted')
}

async function validateCandidateDynamicAbsence(config, manifest) {
  const modelRequestBytes = await read0400(config.paths.modelAuthorityFile,
    'static model request authority', MAX_JSON)
  assert(sha256(modelRequestBytes) === config.authority.modelAuthoritySha256,
    'static model request authority pin drifted')
  const modelRequest = parseJSON(modelRequestBytes, 'static model request authority')
  assert(modelRequest.schemaVersion === 'chora.m1-o4-model-request-authority.v2',
    'static model request authority schema drifted')
  for (const [path, label] of [
    [config.paths.preflightFile, 'candidate v2 fresh Engine preflight'],
    [config.paths.qualificationFile, 'candidate Doctor Engine qualification'],
    [config.paths.modelObservationFile, 'candidate Doctor model observation'],
    [config.paths.installedDoctorReportFile, 'candidate installed Doctor report'],
  ]) {
    exactAbsolute(path, label)
    await assertNoSymlinkAncestors(path)
    await absent(path, label)
  }
  assert(within(manifest.roots.evidenceRoot, config.paths.preflightFile),
    'candidate v2 fresh Engine preflight escapes total evidence root')
}

async function createFreshRoots(input) {
  const createdRoots = []
  const identities = {}
  try {
  const setup = input.freshRootParents
  identities.runtimeParent = await createOwnedDirectory({ capability: input.repositoryCapture,
    path: setup.runtimeParent, parentIdentity: setup.stageParentIdentity })
  createdRoots.push([setup.runtimeParent, identities.runtimeParent])
  for (const key of driverCreatedRootKeys) {
    const root = input.manifest.roots[key]
    const parentIdentity = dirname(root) === setup.runtimeParent ?
      identities.runtimeParent : setup.controlParentIdentity
    identities[key] = await createOwnedDirectory({ capability: input.repositoryCapture,
      path: root, parentIdentity })
    createdRoots.push([root, identities[key]])
  }
  const c = input.manifest.phases.C.environment
  const d = input.manifest.phases.D.environment
  const phaseDirectories = [
    c.CHORA_O4_PROFILE_REUSE_SUPERVISOR_RESOURCE_DIR,
    c.CHORA_O4_PROFILE_REUSE_RUNNER_PROTOCOL_DIR,
    d.CHORA_O4_RECOVERY_RESIDUE_SUPERVISOR_RESOURCE_DIR,
    d.CHORA_O4_RECOVERY_RESIDUE_GENERATION_REFERENCE_DIR,
    d.CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_DIR,
    d.CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_METADATA_DIR,
    d.CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_OCR_DIR,
  ]
  assert(new Set(phaseDirectories).size === phaseDirectories.length,
    'O4 product-owned phase directories overlap')
  for (const path of phaseDirectories) {
    exactAbsolute(path, 'O4 product-owned phase directory')
    assert(within(input.manifest.roots.evidenceRoot, path),
      'O4 product-owned phase directory escapes the evidence root')
    assert(dirname(path) === input.manifest.roots.evidenceRoot,
      'O4 product-owned phase directory is not an exact evidence-root child')
    const identity = await createOwnedDirectory({ capability: input.repositoryCapture,
      path, parentIdentity: identities.evidenceRoot })
    identities[`phase:${basename(path)}`] = identity
    createdRoots.push([path, identity])
  }
  return Object.freeze(identities)
  } catch (error) {
    const cleanupErrors = []
    for (const [root, identity] of createdRoots.reverse()) {
      try {
        await cleanupOwnedPath({ capability: input.repositoryCapture, path: root,
          identity, disposition: root === setup.runtimeParent ?
            'empty_directory' : 'recursive_directory' })
      } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    }
    if (cleanupErrors.length > 0) throw new AggregateError(
      [error, ...cleanupErrors], 'fresh-root creation and native cleanup failed', { cause: error })
    throw error
  }
}

async function cleanupFreshRootSetup(input, identities) {
  const cleanupErrors = []
  const phaseIdentities = Object.entries(identities)
    .filter(([key]) => key.startsWith('phase:')).reverse()
  for (const [key, identity] of phaseIdentities) {
    const path = join(input.manifest.roots.evidenceRoot, key.slice('phase:'.length))
    try {
      await cleanupOwnedPath({ capability: input.repositoryCapture,
        path, identity, disposition: 'recursive_directory' })
    } catch (error) { cleanupErrors.push(error) }
  }
  for (const key of [...driverCreatedRootKeys].reverse()) {
    const identity = identities[key]
    if (identity === undefined) continue
    try {
      await cleanupOwnedPath({ capability: input.repositoryCapture,
        path: input.manifest.roots[key], identity, disposition: 'recursive_directory' })
    } catch (error) { cleanupErrors.push(error) }
  }
  if (identities.runtimeParent !== undefined) {
    try {
      await cleanupOwnedPath({ capability: input.repositoryCapture,
        path: freshRootSetup(input.manifest).runtimeParent,
        identity: identities.runtimeParent, disposition: 'empty_directory' })
    } catch (error) { cleanupErrors.push(error) }
  }
  if (cleanupErrors.length > 0) throw new AggregateError(cleanupErrors,
    'O4 dry-run fresh-root cleanup failed')
}

export async function rehearseO4TotalRealSetup(cli) {
  const input = await preflight(cli)
  await revalidatePrivateEvidenceSnapshot(input.privateEvidenceSnapshot, input.manifest)
  let identities
  let setupError
  try {
    identities = await createFreshRoots(input)
    await revalidatePrivateEvidenceSnapshot(input.privateEvidenceSnapshot, input.manifest)
  } catch (error) { setupError = error }
  if (identities !== undefined) {
    try { await cleanupFreshRootSetup(input, identities) }
    catch (cleanupError) {
      setupError = setupError ? new AggregateError([setupError, cleanupError],
        'O4 dry-run setup and cleanup failed', { cause: setupError }) : cleanupError
    }
  }
  if (setupError) throw setupError
  await validateFreshRoots(input.manifest)
  return Object.freeze({ status: 'passed', real: false, engineMutationAttempted: false,
    manifestSha256: input.manifestSha256, runtimeParentCreatedAndRemoved: true,
    allFreshRootsCreatedAndRemoved: true })
}

function freshEngineEnvironment(input) {
  const e = input.manifest.engine
  const roots = input.manifest.roots
  const topology = o4ColimaTopology(input.manifest)
  const path = [...new Set([dirname(e.limaFile), dirname(e.dockerFile), dirname(e.colimaFile),
    '/usr/bin', '/bin', '/usr/sbin', '/sbin'])].join(':')
  const env = Object.freeze({
    COLIMA_HOME: roots.colimaHome,
    LIMA_HOME: topology.limaHome,
    DOCKER_CONFIG: roots.dockerConfigRoot,
    DOCKER_CONTEXT: e.contextName,
    HOME: roots.userRoot,
    TMPDIR: roots.temporaryRoot,
    PATH: path,
    LANG: 'C',
    LC_ALL: 'C',
  })
  assert(!Object.hasOwn(env, 'DOCKER_HOST'), 'DOCKER_HOST is forbidden at the fresh Engine boundary')
  return env
}

function freshEngineStartArgv(input) {
  const e = input.manifest.engine
  return Object.freeze([
    'start', e.profileName,
    '--disk-image', e.diskImageFile,
    '--mount', 'none',
    '--activate=false',
    '--template=false',
    '--ssh-config=false',
    '--ssh-agent=false',
    '--kubernetes=false',
    '--binfmt=false',
    '--vz-rosetta=false',
    '--network-address=false',
    '--network-host-addresses=false',
    '--network-preferred-route=false',
    '--save-config=true',
    '--arch', 'aarch64',
    '--runtime', 'docker',
    '--vm-type', 'vz',
    '--cpus', String(e.cpus),
    '--memory', String(e.memoryGiB),
    '--disk', String(e.diskGiB),
    '--root-disk', '20',
  ])
}

async function bootstrapFreshEngine(input) {
  await assertAllPins(input.manifest)
  for (const key of ['userRoot', 'colimaHome', 'dockerConfigRoot', 'temporaryRoot']) {
    assert((await readdir(input.manifest.roots[key])).length === 0,
      `fresh Engine ${key} was mutated before bootstrap`)
  }
  const sandboxProbe = await verifyBootstrapSandboxSemantics(input)
  const failureFile = join(input.manifest.roots.evidenceRoot, 'engine-bootstrap-failure.json')
  await absent(failureFile, 'fresh Engine bootstrap failure evidence')
  const limaInfoArgv = Object.freeze(['info'])
  let limaInfoResult
  try {
    limaInfoResult = await runFreshEngineCommand(input, input.manifest.engine.limaFile,
      limaInfoArgv, 'canonical Lima portable prefix observation')
  } catch (error) {
    await writeBootstrapFailure(input, 'lima_info', limaInfoArgv, null, error, failureFile)
    throw error
  }
  if (limaInfoResult.code !== 0 || limaInfoResult.signal !== null) {
    await writeBootstrapFailure(input, 'lima_info', limaInfoArgv, limaInfoResult, null, failureFile)
    throw new Error('canonical Lima portable prefix observation failed inside the deny-egress launcher')
  }
  let limaInfo
  try {
    limaInfo = parseJSON(limaInfoResult.stdout, 'canonical Lima portable prefix observation')
    assert(Array.isArray(limaInfo.templates) && limaInfo.templates.length === 120 &&
      limaInfo.templates.every((entry) => entry && typeof entry.location === 'string' &&
        (entry.location === input.manifest.engine.limaTemplatesRoot ||
          within(input.manifest.engine.limaTemplatesRoot, entry.location))) &&
      new Set(limaInfo.templates.map((entry) => entry.location)).size === 120 &&
      limaInfo.templates.some((entry) => entry.location ===
        join(input.manifest.engine.limaTemplatesRoot, 'default.yaml')) &&
      limaInfo.templates.some((entry) => entry.location ===
        join(input.manifest.engine.limaTemplatesRoot, '_images/ubuntu.yaml')) &&
      limaInfo.templates.some((entry) => entry.location ===
        join(input.manifest.engine.limaTemplatesRoot, '_default/mounts.yaml')) &&
      limaInfo.guestAgents && Object.keys(limaInfo.guestAgents).length === 1 &&
      limaInfo.guestAgents.aarch64?.location === input.manifest.engine.limaGuestAgentFile &&
      limaInfo.hostOS === 'darwin' && limaInfo.hostArch === 'aarch64',
    'canonical Lima runtime did not resolve only the pinned portable prefix')
  } catch (error) {
    await writeBootstrapFailure(input, 'lima_info', limaInfoArgv, limaInfoResult, error, failureFile)
    throw error
  }

  const startArgv = freshEngineStartArgv(input)
  let started
  try {
    started = await runFreshEngineCommand(input, input.manifest.engine.colimaFile,
      startArgv, 'fresh Colima bootstrap')
  } catch (error) {
    await writeBootstrapFailure(input, 'colima_start', startArgv, null, error, failureFile)
    throw error
  }
  if (started.code !== 0 || started.signal !== null) {
    await writeBootstrapFailure(input, 'colima_start', startArgv, started, null, failureFile)
    throw new Error('fresh Colima bootstrap failed inside the deny-egress launcher')
  }

  let postStartStage = 'context_inspect'
  let postStartArgv = ['context', 'inspect', '--format',
    '{"name":{{json .Name}},"endpoint":{{json .Endpoints.docker.Host}}}',
    input.manifest.engine.contextName]
  let postStartResult = null
  try {
    postStartResult = await runFreshEngineCommand(input, input.manifest.engine.dockerFile,
      postStartArgv, 'fresh Docker exact context inspection')
    requireSuccessfulFreshEngineResult(postStartResult, 'fresh Docker exact context inspection')
    postStartStage = 'context_parse'
    const contextInspection = parseFreshEngineContextInspection(postStartResult.stdout)
    postStartStage = 'context_binding'
    validateFreshEngineContextInspection(input.manifest.engine, contextInspection)

    postStartStage = 'endpoint_socket'
    postStartArgv = []
    postStartResult = null
    const socketInfo = await lstat(input.manifest.engine.endpointSocketPath)
    assert(socketInfo.isSocket() && !socketInfo.isSymbolicLink() && socketInfo.uid === currentUID(),
      'fresh Docker endpoint is not one owner-controlled Unix socket')

    postStartStage = 'docker_version'
    postStartArgv = ['version', '--format', '{"api_version":{{json .Server.APIVersion}},"server_version":{{json .Server.Version}},"operating_system":{{json .Server.Os}},"architecture":{{json .Server.Arch}}}']
    postStartResult = await runFreshEngineCommand(input, input.manifest.engine.dockerFile,
      postStartArgv, 'fresh Docker version observation')
    requireSuccessfulFreshEngineResult(postStartResult, 'fresh Docker version observation')
    const version = parseJSON(postStartResult.stdout, 'fresh Docker version observation')
    exactKeys(version, ['api_version', 'server_version', 'operating_system', 'architecture'],
      'fresh Docker version observation')

    postStartStage = 'docker_info'
    postStartArgv = ['info', '--format', '{"daemon_id":{{json .ID}},"provider_name":{{json .OperatingSystem}}}']
    postStartResult = await runFreshEngineCommand(input, input.manifest.engine.dockerFile,
      postStartArgv, 'fresh Docker daemon observation')
    requireSuccessfulFreshEngineResult(postStartResult, 'fresh Docker daemon observation')
    const info = parseJSON(postStartResult.stdout, 'fresh Docker daemon observation')
    exactKeys(info, ['daemon_id', 'provider_name'], 'fresh Docker daemon observation')
    assert(typeof info.daemon_id === 'string' && info.daemon_id.length > 0 && info.daemon_id.length <= 256 &&
      typeof version.api_version === 'string' && /^\d+\.\d+$/.test(version.api_version) &&
      version.operating_system === 'linux' && version.architecture === 'arm64' &&
      typeof version.server_version === 'string' && version.server_version.length > 0 &&
      typeof info.provider_name === 'string' && info.provider_name.length > 0,
    'fresh Docker provider/version observation is invalid')

    postStartStage = 'docker_images'
    postStartArgv = ['image', 'ls', '--all', '--no-trunc', '--format', '{{json .ID}}']
    postStartResult = await runFreshEngineCommand(input, input.manifest.engine.dockerFile,
      postStartArgv, 'fresh Docker zero-image observation')
    requireSuccessfulFreshEngineResult(postStartResult, 'fresh Docker zero-image observation')
    const imageLines = postStartResult.stdout.toString('utf8').trim()
    assert(imageLines === '', 'fresh Docker daemon was not zero-image before candidate mutation')

    postStartStage = 'engine_processes'
    postStartArgv = []
    postStartResult = null
    const processes = await observeFreshEngineProcesses(input, true)

    postStartStage = 'engine_instance'
    postStartArgv = ['list', input.manifest.engine.contextName,
      '--format', LIMA_INSTANCE_FORMAT, '--tty=false']
    postStartResult = await runFreshEngineCommand(input, input.manifest.engine.limaFile,
      postStartArgv, 'fresh Lima exact instance inspection')
    requireSuccessfulFreshEngineResult(postStartResult, 'fresh Lima exact instance inspection')
    const limaInstance = parseFreshEngineInstanceInspection(postStartResult.stdout)
    validateFreshEngineInstanceInspection(input.manifest.engine, limaInstance, processes)

    postStartStage = 'preflight_record'
    postStartArgv = []
    postStartResult = null
    const record = buildFreshEnginePreflightAuthority({
      generationId: input.manifest.identity.generationId,
      preMutationPaths: freshEnginePreMutationPaths(input.manifest),
      engine: input.manifest.engine,
      limaInfo,
      docker: {
        daemonId: info.daemon_id, apiVersion: version.api_version,
        serverVersion: version.server_version, operatingSystem: version.operating_system,
        architecture: version.architecture, providerName: info.provider_name,
      },
      limaInstance,
      processes,
      sandbox: { probeDigest: sandboxProbe.digest },
      bootstrap: {
        argv: startArgv, environment: freshEngineEnvironment(input),
        stdout: started.stdout, stderr: started.stderr,
      },
      observedAt: new Date().toISOString(),
    })
    assert(record.limaInfoDigest === input.manifest.engine.limaInfoDigest,
      'canonical Lima runtime info authority drifted')
    await writeTotalStandaloneAuthority(Buffer.from(`${JSON.stringify(record, null, 2)}\n`),
      input.manifest.engine.zeroImagePreflightFile, input.repositoryCapture)
    return record
  } catch (error) {
    await writeBootstrapFailure(input, postStartStage, postStartArgv,
      postStartResult, error, failureFile)
    throw error
  }
}

export function buildFreshEnginePreflightAuthority({
  generationId, preMutationPaths, engine, limaInfo, docker, limaInstance, processes,
  sandbox, bootstrap, observedAt,
}) {
  const draft = {
    schemaVersion: ENGINE_PREFLIGHT_SCHEMA,
    status: 'passed',
    generationId,
    preMutationRootsAbsent: true,
    preMutationAbsenceSha256: sha256(canonicalJSONStringify(preMutationPaths)),
    zeroMutation: true,
    initialImageCount: 0,
    initialImageInventorySha256: sha256(canonicalJSONStringify([])),
    colimaSha256: engine.colimaSha256,
    limaSha256: engine.limaSha256,
    limaPrefixClosureSha256: engine.limaPrefixClosureSha256,
    limaInfoDigest: sha256(canonicalJSONStringify({
      templates: limaInfo.templates.map((entry) => entry.location).sort(),
      guestAgent: limaInfo.guestAgents.aarch64.location,
      hostOS: limaInfo.hostOS, hostArch: limaInfo.hostArch,
    })),
    dockerSha256: engine.dockerSha256,
    diskImageSize: engine.diskImageSize,
    diskImageSha256: engine.diskImageSha256,
    profileName: engine.profileName,
    contextName: engine.contextName,
    endpointSocketPathSha256: sha256(engine.endpointSocketPath),
    endpointDigest: engine.endpointDigest,
    daemonId: docker.daemonId,
    apiVersion: docker.apiVersion,
    serverVersion: docker.serverVersion,
    operatingSystem: docker.operatingSystem,
    architecture: docker.architecture,
    providerName: docker.providerName,
    limaInstance,
    processes,
    sandboxExecutableSha256: engine.sandboxExecutableSha256,
    sandboxProfileSha256: engine.sandboxProfileSha256,
    outboundAcquisitionPolicy: 'os_enforced_deny_remote_allow_local_unix',
    sandboxPolicyProbeDigest: sandbox.probeDigest,
    bootstrapArgvSha256: sha256(canonicalJSONStringify(bootstrap.argv)),
    bootstrapEnvironmentSha256: sha256(canonicalJSONStringify(bootstrap.environment)),
    bootstrapStdoutSha256: sha256(bootstrap.stdout),
    bootstrapStderrSha256: sha256(bootstrap.stderr),
    observedAt,
  }
  return { ...draft, digest: sha256(canonicalJSONStringify(draft)) }
}

export function validateFreshEngineContextName(engine) {
  assert(engine.contextName === `colima-${engine.profileName}`,
    'fresh Engine context must be the exact Colima profile context')
}

export function requireSuccessfulFreshEngineResult(result, label) {
  assert(result && result.code === 0 && result.signal === null, `${label} failed`)
  return result
}

export function parseFreshEngineContextInspection(bytes) {
  const value = parseJSON(Buffer.from(bytes), 'fresh Docker exact context inspection')
  exactKeys(value, ['name', 'endpoint'], 'fresh Docker exact context inspection')
  return value
}

export function validateFreshEngineContextInspection(engine, value) {
  assert(value.name === engine.contextName,
    'fresh Docker inspected context name drifted')
  assert(value.endpoint === `unix://${engine.endpointSocketPath}` &&
    sha256(value.endpoint) === engine.endpointDigest,
  'fresh Docker context does not bind the authorized Unix endpoint')
  return value
}

export function parseFreshEngineInstanceInspection(bytes) {
  const value = parseJSON(Buffer.from(bytes), 'fresh Lima exact instance inspection')
  exactKeys(value, ['name', 'status', 'vmType', 'hostAgentPID', 'driverPID'],
    'fresh Lima exact instance inspection')
  return value
}

export function validateFreshEngineInstanceInspection(engine, value, processes) {
  assert(value.name === engine.contextName && value.status === 'Running' &&
    value.vmType === 'vz' && Number.isSafeInteger(value.hostAgentPID) &&
    value.hostAgentPID > 1 && Number.isSafeInteger(value.driverPID) && value.driverPID > 1,
  'fresh Lima instance identity drifted')
  assert(Array.isArray(processes) && processes.length === 2,
    'fresh Lima instance process authority is incomplete')
  const byRole = new Map()
  for (const processIdentity of processes) {
    exactKeys(processIdentity, ['role', 'pid', 'pidFileSha256'],
      'fresh Lima instance process identity')
    assert(['host_agent', 'vz_driver'].includes(processIdentity.role) &&
      !byRole.has(processIdentity.role) && Number.isSafeInteger(processIdentity.pid) &&
      processIdentity.pid > 1 && digestRE.test(processIdentity.pidFileSha256),
    'fresh Lima instance process authority is incomplete')
    byRole.set(processIdentity.role, processIdentity)
  }
  assert(byRole.get('host_agent')?.pid === value.hostAgentPID &&
    byRole.get('vz_driver')?.pid === value.driverPID,
  'fresh Lima instance identity drifted')
  return value
}

export async function writeBootstrapFailure(
  input, stage, argv, result, error, path, writeDependencies = undefined,
) {
  assert(within(input.manifest.roots.evidenceRoot, path),
    'fresh Engine bootstrap failure evidence escapes the evidence root')
  const draft = {
    schemaVersion: ENGINE_BOOTSTRAP_FAILURE_SCHEMA,
    status: 'failed',
    stage,
    classification: classifyBootstrapFailure(stage, result, error),
    generationId: input.manifest.identity.generationId,
    profileName: input.manifest.engine.profileName,
    contextName: input.manifest.engine.contextName,
    exitCode: Number.isInteger(result?.code) ? result.code : null,
    signal: typeof result?.signal === 'string' ? result.signal : null,
    argvSha256: sha256(canonicalJSONStringify(argv)),
    environmentSha256: sha256(canonicalJSONStringify(freshEngineEnvironment(input))),
    limaPrefixClosureSha256: input.manifest.engine.limaPrefixClosureSha256,
    stdoutByteCount: result ? result.stdout.length : null,
    stdoutSha256: result ? sha256(result.stdout) : null,
    stderrByteCount: result ? result.stderr.length : null,
    stderrSha256: result ? sha256(result.stderr) : null,
    launcherErrorSha256: error ? sha256(String(error?.message ?? error)) : null,
    engineProcessObservation: boundedEngineProcessObservation(error),
    elapsedMs: Number.isSafeInteger(result?.elapsedMs) ? result.elapsedMs :
      Number.isSafeInteger(error?.elapsedMs) ? error.elapsedMs : null,
    observedAt: new Date().toISOString(),
  }
  const record = { ...draft, digest: sha256(canonicalJSONStringify(draft)) }
  await writeTotalStandaloneAuthority(Buffer.from(`${JSON.stringify(record, null, 2)}\n`), path,
    input.repositoryCapture, writeDependencies)
}

function boundedEngineProcessObservation(error) {
  if (error?.code !== 'ENGINE_PROCESS_IDENTITIES_UNAVAILABLE') return null
  const value = error.engineProcessObservation
  try {
    assert(value && typeof value === 'object' && !Array.isArray(value) &&
      Number.isSafeInteger(value.attemptCount) && value.attemptCount > 0 &&
      value.attemptCount <= 1_000_000 && digestRE.test(value.expectedInstanceRootSha256) &&
      Array.isArray(value.roles) && value.roles.length === 2 &&
      Array.isArray(value.partialProcesses) && value.partialProcesses.length <= 2,
    'fresh Engine process observation is invalid')
    const expectedRoles = ['host_agent', 'vz_driver']
    const roles = value.roles.map((entry, index) => {
      assert(entry && typeof entry === 'object' && !Array.isArray(entry) &&
        entry.role === expectedRoles[index] && ['absent', 'valid'].includes(entry.outcome),
      'fresh Engine process observation role is invalid')
      return { role: entry.role, outcome: entry.outcome }
    })
    const seen = new Set()
    const partialProcesses = value.partialProcesses.map((entry) => {
      assert(entry && typeof entry === 'object' && !Array.isArray(entry) &&
        expectedRoles.includes(entry.role) && !seen.has(entry.role) &&
        roles.find((role) => role.role === entry.role)?.outcome === 'valid' &&
        Number.isSafeInteger(entry.pid) && entry.pid > 1 && digestRE.test(entry.pidFileSha256),
      'fresh Engine partial process observation is invalid')
      seen.add(entry.role)
      return { role: entry.role, pid: entry.pid, pidFileSha256: entry.pidFileSha256 }
    })
    return {
      attemptCount: value.attemptCount,
      expectedInstanceRootSha256: value.expectedInstanceRootSha256,
      roles,
      partialProcesses,
    }
  } catch {
    return null
  }
}

function classifyBootstrapFailure(stage, result, error) {
  const stderr = result?.stderr?.toString('utf8') ?? ''
  const message = String(error?.message ?? '')
  if (/template\s+"[^"]+"\s+not found|guest agent binary could not be found/i.test(stderr))
    return 'portable_asset_missing'
  if (/dependency check failed|executable file not found|not found in \$PATH|is required.*not found/i.test(stderr))
    return 'dependency_missing'
  if (/sandbox-exec:.*sandbox_apply|sandbox.*(?:deny|denied)/i.test(`${stderr}\n${message}`))
    return 'sandbox_denied'
  if (/network is unreachable|network unreachable|no route to host|dns lookup failed|connection refused/i.test(stderr))
    return 'network_unavailable_or_denied'
  if (/operation not permitted|permission denied|\bEPERM\b|\bEACCES\b/i.test(`${stderr}\n${message}`))
    return 'host_operation_denied'
  if (/invalid disk image|sha validation failed|unsupported disk image|cannot use diskimage/i.test(stderr))
    return 'disk_image_invalid'
  if (/lima.*version.*(?:lower|incompatible|unsupported)|compatibility.*lima/i.test(stderr))
    return 'lima_version_incompatible'
  if (/error (?:at ['"]creating and starting['"]|starting (?:vm|virtual machine))|virtualization.*(?:failed|error)/i.test(stderr))
    return 'vm_start_failed'
  if (/timed out/i.test(message)) return 'timeout'
  if (stage === 'engine_processes') return error?.code ===
    'ENGINE_PROCESS_IDENTITIES_UNAVAILABLE' ? 'engine_identity_unavailable' :
      'engine_identity_invalid'
  if (stage === 'engine_instance') return result &&
    (result.code !== 0 || result.signal !== null) ? 'engine_identity_unavailable' :
      'engine_identity_invalid'
  if (stage === 'lima_info') return 'portable_asset_missing'
  if (result) return 'child_exit'
  return 'launcher_error'
}

async function verifyBootstrapSandboxSemantics(input) {
  const e = input.manifest.engine
  const env = freshEngineEnvironment(input)
  const connectScript = 'const n=require("node:net");const s=n.connect(process.argv[1],()=>process.exit(0));s.on("error",()=>process.exit(11));setTimeout(()=>process.exit(12),1000)'
  const tcpScript = 'const n=require("node:net");const s=n.connect({host:"127.0.0.1",port:Number(process.argv[1])},()=>process.exit(0));s.on("error",()=>process.exit(13));setTimeout(()=>process.exit(14),1000)'
  const denyScript = 'const n=require("node:net");const s=n.connect({host:"192.0.2.1",port:9},()=>process.exit(20));s.on("error",e=>process.exit(e.code==="EPERM"||e.code==="EACCES"?0:21));setTimeout(()=>process.exit(22),1000)'
  const socketPath = join(input.manifest.roots.temporaryRoot, `sandbox-probe-${process.pid}.sock`)
  const unixServer = createServer((socket) => socket.end())
  const tcpServer = createServer((socket) => socket.end())
  let socketIdentity = null
  let observation
  let operationError = null
  try {
    await listenServer(unixServer, { path: socketPath })
    socketIdentity = ownedPathIdentity(await lstat(socketPath, { bigint: true }))
    const unixArgv = ['-f', e.sandboxProfileFile, input.manifest.playwright.nodeFile,
      '-e', connectScript, socketPath]
    const unix = await runBoundedProcess(e.sandboxExecutableFile, unixArgv,
      env, input.manifest.roots.userRoot, 5_000, input.manifest.timeouts.closeMs,
      'fresh Engine Unix sandbox policy probe')
    assert(unix.code === 0 && unix.signal === null,
      'fresh Engine sandbox policy did not allow the required local Unix transport')
    await closeServer(unixServer)
    await cleanupOwnedPath({ capability: input.repositoryCapture, path: socketPath,
      identity: socketIdentity, disposition: 'unix_socket' })

    await listenServer(tcpServer, { host: '127.0.0.1', port: 0 })
    const address = tcpServer.address()
    assert(address && typeof address === 'object' && address.address === '127.0.0.1',
      'fresh Engine localhost sandbox probe did not bind loopback')
    const localhostArgv = ['-f', e.sandboxProfileFile, input.manifest.playwright.nodeFile,
      '-e', tcpScript, String(address.port)]
    const localhost = await runBoundedProcess(e.sandboxExecutableFile, localhostArgv,
      env, input.manifest.roots.userRoot, 5_000, input.manifest.timeouts.closeMs,
      'fresh Engine localhost sandbox policy probe')
    assert(localhost.code === 0 && localhost.signal === null,
      'fresh Engine sandbox policy did not allow the required localhost transport')
    await closeServer(tcpServer)

    const remoteArgv = ['-f', e.sandboxProfileFile, input.manifest.playwright.nodeFile,
      '-e', denyScript]
    const remote = await runBoundedProcess(e.sandboxExecutableFile, remoteArgv,
      env, input.manifest.roots.userRoot, 5_000, input.manifest.timeouts.closeMs,
      'fresh Engine remote sandbox policy probe')
    assert(remote.code === 0 && remote.signal === null,
      'fresh Engine sandbox policy did not prove OS denial of remote IP egress')
    const draft = {
      sandboxExecutableSha256: e.sandboxExecutableSha256,
      sandboxProfileSha256: e.sandboxProfileSha256,
      unixAllowed: true,
      localhostAllowed: true,
      remoteIPDeniedByOS: true,
      remoteProbeTargetSha256: sha256('192.0.2.1:9'),
      unixProbeArgvSha256: sha256(canonicalJSONStringify(unixArgv)),
      unixProbeOutputSha256: sha256(Buffer.concat([unix.stdout, unix.stderr])),
      localhostProbeArgvSha256: sha256(canonicalJSONStringify(localhostArgv)),
      localhostProbeOutputSha256: sha256(Buffer.concat([localhost.stdout, localhost.stderr])),
      remoteProbeArgvSha256: sha256(canonicalJSONStringify(remoteArgv)),
      remoteProbeOutputSha256: sha256(Buffer.concat([remote.stdout, remote.stderr])),
    }
    observation = Object.freeze({ ...draft, digest: sha256(canonicalJSONStringify(draft)) })
  } catch (error) { operationError = error }
  const cleanupErrors = []
  for (const server of [unixServer, tcpServer]) {
    try { await closeServer(server) } catch (error) { cleanupErrors.push(error) }
  }
  if (socketIdentity !== null) {
    try {
      await cleanupOwnedPath({ capability: input.repositoryCapture, path: socketPath,
        identity: socketIdentity, disposition: 'unix_socket' })
    } catch (error) { cleanupErrors.push(error) }
  }
  if (operationError !== null && cleanupErrors.length > 0) throw new AggregateError(
    [operationError, ...cleanupErrors], 'sandbox probe and native cleanup failed',
    { cause: operationError })
  if (operationError !== null) throw operationError
  if (cleanupErrors.length > 0) throw new AggregateError(cleanupErrors, 'sandbox probe cleanup failed')
  return observation
}

function listenServer(server, options) {
  return new Promise((resolvePromise, reject) => {
    server.once('error', reject)
    server.listen(options, () => { server.removeListener('error', reject); resolvePromise() })
  })
}

function closeServer(server) {
  if (!server.listening) return Promise.resolve()
  return new Promise((resolvePromise, reject) => server.close((error) => error ? reject(error) : resolvePromise()))
}

async function cleanupFreshEngine(input) {
  const topology = o4ColimaTopology(input.manifest)
  const processObservation = await cleanupProcessIdentities(input)
  const processes = processObservation.processes
  const stop = await attemptFreshEngineCleanupCommand(input, input.manifest.engine.colimaFile,
    ['stop', input.manifest.engine.profileName, '--force'], 'fresh Colima stop')
  const removeContext = await attemptFreshEngineCleanupCommand(input, input.manifest.engine.dockerFile,
    ['context', 'rm', '--force', input.manifest.engine.contextName], 'fresh Docker context removal')
  const removeProfile = await attemptFreshEngineCleanupCommand(input, input.manifest.engine.colimaFile,
    ['delete', input.manifest.engine.profileName, '--force', '--data'], 'fresh Colima profile deletion')

  for (const pid of new Set(processes.map((observed) => observed.pid))) await waitForPIDDeath(pid,
    input.manifest.timeouts.closeMs, `fresh Engine PID ${pid}`)
  await absent(input.manifest.engine.endpointSocketPath, 'fresh Engine endpoint socket')
  for (const path of [
    topology.profileRoot,
    topology.limaInstanceRoot,
    topology.limaDiskRoot,
    topology.storeFile,
  ]) await absent(path, 'fresh Engine profile residue')
  const status = await runFreshEngineCommand(input, input.manifest.engine.colimaFile,
    ['status', input.manifest.engine.profileName, '--json'], 'fresh Colima absence observation')
  assert(status.code !== 0, 'deleted fresh Colima profile is still reported running')
  const context = await runFreshEngineCommand(input, input.manifest.engine.dockerFile,
    ['context', 'inspect', input.manifest.engine.contextName], 'fresh Docker context absence observation')
  assert(context.code !== 0, 'deleted fresh Docker context remains observable')

  for (const key of ['colimaHome', 'dockerConfigRoot', 'temporaryRoot']) {
    const root = input.manifest.roots[key]
    const identity = input.freshRootIdentities?.[key]
    assert(identity !== undefined, `fresh Engine cleanup ${key} identity is unavailable`)
    await cleanupOwnedPath({ capability: input.repositoryCapture, path: root,
      identity, disposition: 'recursive_directory' })
  }
  assert(processObservation.errors.length === 0,
    'fresh Engine process identities drifted before cleanup; Engine was removed but acceptance is denied')
  const draft = {
    schemaVersion: ENGINE_RESIDUE_SCHEMA,
    status: 'proven_zero',
    generationId: input.manifest.identity.generationId,
    profileName: input.manifest.engine.profileName,
    contextName: input.manifest.engine.contextName,
    endpointDigest: input.manifest.engine.endpointDigest,
    observedProcessCount: processes.length,
    processIDsSha256: sha256(canonicalJSONStringify(processes.map(({ role, pid }) => ({ role, pid })))),
    allObservedProcessesDead: true,
    endpointSocketAbsent: true,
    profileStateAbsent: true,
    dockerContextAbsent: true,
    colimaHomeAbsent: true,
    dockerConfigRootAbsent: true,
    temporaryRootAbsent: true,
    stopExitCode: stop.code,
    deleteExitCode: removeProfile.code,
    contextRemoveExitCode: removeContext.code,
    stopErrorSha256: stop.errorSha256,
    deleteErrorSha256: removeProfile.errorSha256,
    contextRemoveErrorSha256: removeContext.errorSha256,
    stopOutputSha256: sha256(Buffer.concat([stop.stdout, stop.stderr])),
    deleteOutputSha256: sha256(Buffer.concat([removeProfile.stdout, removeProfile.stderr])),
    contextRemoveOutputSha256: sha256(Buffer.concat([removeContext.stdout, removeContext.stderr])),
    observedAt: new Date().toISOString(),
  }
  const record = { ...draft, digest: sha256(canonicalJSONStringify(draft)) }
  await writeTotalStandaloneAuthority(Buffer.from(`${JSON.stringify(record, null, 2)}\n`),
    input.manifest.engine.cleanupResidueFile, input.repositoryCapture)
  return record
}

async function attemptFreshEngineCleanupCommand(input, file, argv, label) {
  try {
    const result = await runFreshEngineCommand(input, file, argv, label)
    return { ...result, errorSha256: null }
  } catch (error) {
    return { code: null, signal: null, stdout: Buffer.alloc(0), stderr: Buffer.alloc(0),
      errorSha256: sha256(String(error?.message ?? error)) }
  }
}

async function cleanupProcessIdentities(input) {
  const byPID = new Map()
  const errors = []
  try {
    const preflight = await read0400JSON(input.manifest.engine.zeroImagePreflightFile,
      'fresh Engine preflight process identities', MAX_JSON)
    assert(preflight.schemaVersion === ENGINE_PREFLIGHT_SCHEMA && Array.isArray(preflight.processes),
      'fresh Engine preflight process identities are invalid')
    validateFreshEngineInstanceInspection(input.manifest.engine, preflight.limaInstance,
      preflight.processes)
    for (const value of preflight.processes) {
      assert(value && ['host_agent', 'vz_driver'].includes(value.role) &&
        Number.isSafeInteger(value.pid) && value.pid > 1 && digestRE.test(value.pidFileSha256),
      'fresh Engine preflight contains an invalid process identity')
      byPID.set(`${value.role}:${value.pid}`, value)
    }
  } catch (error) {
    if (error.code !== 'ENOENT') errors.push(sha256(String(error?.message ?? error)))
  }
  try {
    for (const value of await observeFreshEngineProcesses(input, false))
      byPID.set(`${value.role}:${value.pid}`, value)
  } catch (error) { errors.push(sha256(String(error?.message ?? error))) }
  return { processes: [...byPID.values()].sort((left, right) =>
    left.role.localeCompare(right.role) || left.pid - right.pid), errors }
}

export async function observeFreshEngineProcesses(input, required, options = {}) {
  const root = o4ColimaTopology(input.manifest).limaInstanceRoot
  const candidates = Object.freeze([
    Object.freeze(['host_agent', join(root, 'ha.pid')]),
    Object.freeze(['vz_driver', join(root, 'vz.pid')]),
  ])
  const timeoutMs = options.timeoutMs ?? ENGINE_PROCESS_IDENTITY_TIMEOUT_MS
  const pollMs = options.pollMs ?? ENGINE_PROCESS_IDENTITY_POLL_MS
  const now = options.now ?? Date.now
  const sleep = options.sleep ?? ((ms) => new Promise((resolvePromise) => setTimeout(resolvePromise, ms)))
  assert(Number.isSafeInteger(timeoutMs) && timeoutMs >= 0 &&
    Number.isSafeInteger(pollMs) && pollMs > 0 && typeof now === 'function' &&
    typeof sleep === 'function', 'fresh Engine process observation options are invalid')
  const deadline = now() + timeoutMs
  let attemptCount = 0
  while (true) {
    attemptCount++
    const observed = []
    const roleOutcomes = []
    for (const [role, path] of candidates) {
      try {
        const stable = await stableOwnerFile(path, `fresh Engine ${role} PID`, 64)
        const raw = stable.bytes.toString('utf8')
        assert(/^[1-9][0-9]{0,9}\n?$/.test(raw),
          `fresh Engine ${role} PID encoding is not canonical`)
        assert((stable.info.mode & 0o022) === 0,
          `fresh Engine ${role} PID file is writable by group or other`)
        const pid = Number(raw.endsWith('\n') ? raw.slice(0, -1) : raw)
        assert(Number.isSafeInteger(pid) && pid > 1 && pid !== process.pid && processExists(pid),
          `fresh Engine ${role} PID is not live`)
        observed.push({ role, pid, pidFileSha256: sha256(stable.bytes) })
        roleOutcomes.push({ role, outcome: 'valid' })
      } catch (error) {
        if (error.code !== 'ENOENT') throw error
        roleOutcomes.push({ role, outcome: 'absent' })
      }
    }
    if (!required || observed.length === candidates.length) return observed
    const current = now()
    if (current >= deadline) {
      const error = new Error(
        'fresh Engine did not expose the exact host-agent and VZ driver identities')
      error.code = 'ENGINE_PROCESS_IDENTITIES_UNAVAILABLE'
      error.engineProcessObservation = Object.freeze({
        attemptCount,
        expectedInstanceRootSha256: sha256(root),
        roles: Object.freeze(roleOutcomes.map((value) => Object.freeze(value))),
        partialProcesses: Object.freeze(observed.map((value) => Object.freeze(value))),
      })
      throw error
    }
    await sleep(Math.min(pollMs, Math.max(1, deadline - current)))
  }
}

async function successfulFreshEngineCommand(input, file, argv, label) {
  const result = await runFreshEngineCommand(input, file, argv, label)
  assert(result.code === 0 && result.signal === null, `${label} failed`)
  return result
}

async function runFreshEngineCommand(input, file, argv, label) {
  assert(file === input.manifest.engine.colimaFile || file === input.manifest.engine.dockerFile ||
    file === input.manifest.engine.limaFile,
    `${label} target is not a pinned Engine executable`)
  const expected = file === input.manifest.engine.colimaFile ?
    input.manifest.engine.colimaSha256 : file === input.manifest.engine.limaFile ?
      input.manifest.engine.limaSha256 : input.manifest.engine.dockerSha256
  assert(sha256(await read0500Executable(file, `${label} pinned target`, MAX_PIN)) === expected,
    `${label} target pin drifted`)
  assert(sha256(await read0755SystemExecutable(input.manifest.engine.sandboxExecutableFile,
    `${label} sandbox executable`, 2 * 1024 * 1024)) === input.manifest.engine.sandboxExecutableSha256,
  `${label} sandbox executable pin drifted`)
  const profile = await read0400(input.manifest.engine.sandboxProfileFile,
    `${label} sandbox profile`, MAX_JSON)
  assert(sha256(profile) === input.manifest.engine.sandboxProfileSha256 &&
    profile.toString('utf8') === bootstrapSandboxProfile,
  `${label} sandbox profile pin drifted`)
  const sandboxArgv = ['-f', input.manifest.engine.sandboxProfileFile, file, ...argv]
  return runBoundedProcess(input.manifest.engine.sandboxExecutableFile, sandboxArgv,
    freshEngineEnvironment(input), input.manifest.roots.userRoot,
    input.manifest.timeouts.engineMs, input.manifest.timeouts.closeMs, label)
}

async function runBoundedProcess(file, argv, env, cwd, timeoutMs, closeTimeoutMs, label) {
  const startedAt = Date.now()
  const child = spawn(file, argv, { shell: false, detached: true, cwd, env,
    stdio: ['ignore', 'pipe', 'pipe'] })
  const limit = 2 * 1024 * 1024
  let stdout = Buffer.alloc(0); let stderr = Buffer.alloc(0); let overflow = false; let timer
  const append = (current, chunk) => {
    const next = Buffer.concat([current, chunk])
    if (next.length > limit) { overflow = true; killProcessGroup(child.pid, 'SIGKILL'); return current }
    return next
  }
  child.stdout.on('data', (chunk) => { stdout = append(stdout, chunk) })
  child.stderr.on('data', (chunk) => { stderr = append(stderr, chunk) })
  try {
    const result = await Promise.race([
      new Promise((resolvePromise, reject) => {
        child.once('error', reject)
        child.once('close', (code, signal) => resolvePromise({ code, signal }))
      }),
      new Promise((_, reject) => { timer = setTimeout(() => {
        killProcessGroup(child.pid, 'SIGKILL'); reject(new Error(`${label} timed out`))
      }, timeoutMs) }),
    ])
    if (processGroupExists(child.pid)) {
      killProcessGroup(child.pid, 'SIGKILL')
      await waitForProcessGroupDeath(child.pid, closeTimeoutMs, label)
      throw new Error(`${label} left a descendant process in its launcher group`)
    }
    assert(!overflow, `${label} output exceeded its bound`)
    return Object.freeze({ ...result, stdout, stderr, elapsedMs: Date.now() - startedAt })
  } catch (error) {
    killProcessGroup(child.pid, 'SIGKILL')
    await waitForProcessGroupDeath(child.pid, closeTimeoutMs, `failed ${label}`)
    if (error && typeof error === 'object' && !Number.isSafeInteger(error.elapsedMs))
      error.elapsedMs = Date.now() - startedAt
    throw error
  } finally { clearTimeout(timer) }
}

async function createSessionAuthority(input) {
  const draft = {
    schemaVersion: SESSION_SCHEMA, status: 'exclusive', manifestSha256: input.manifestSha256,
    runnerSha256: input.manifest.runner.moduleSha256,
    socketPathDigest: sha256(input.manifest.runner.controlSocketPath),
    tupleIdentity: input.inspected.tupleIdentity, identity: input.manifest.identity,
    parentPid: process.pid, sessionId: randomBytes(32).toString('hex'),
    sessionSecret: randomBytes(32).toString('hex'), sequenceStart: 0,
  }
  const authority = Object.freeze({ ...draft, authorityDigest: sha256(canonicalJSONStringify(draft)) })
  await writeTotalStandaloneAuthority(Buffer.from(`${JSON.stringify(authority, null, 2)}\n`),
    input.manifest.runner.sessionAuthorityFile, input.repositoryCapture)
  return authority
}

async function buildProductEvidenceAuthority(input) {
  const c = input.manifest.phases.C.environment
  const d = input.manifest.phases.D.environment
  assert(c.CHORA_O4_PROFILE_REUSE_SOURCE_A3_LEDGER_FILE ===
    d.CHORA_O4_RECOVERY_RESIDUE_SOURCE_A3_LEDGER_FILE,
  'C/D source A3 path drifted')
  const value = {
    sessionAuthorityFile: input.manifest.runner.sessionAuthorityFile,
    sessionAuthoritySha256: sha256(await read0400(
      input.manifest.runner.sessionAuthorityFile, 'Runner session authority', MAX_JSON)),
    tupleIdentity: input.inspected.tupleIdentity,
    sourceA3LedgerFile: c.CHORA_O4_PROFILE_REUSE_SOURCE_A3_LEDGER_FILE,
    verifierLedgerFile: d.CHORA_O4_RECOVERY_RESIDUE_VERIFIER_LEDGER_FILE,
    cResourceDirectory: c.CHORA_O4_PROFILE_REUSE_SUPERVISOR_RESOURCE_DIR,
    dResourceDirectory: d.CHORA_O4_RECOVERY_RESIDUE_SUPERVISOR_RESOURCE_DIR,
  }
  for (const path of [value.sessionAuthorityFile, value.sourceA3LedgerFile,
    value.verifierLedgerFile, value.cResourceDirectory, value.dResourceDirectory]) {
    exactAbsolute(path, 'product evidence authority path')
  }
  return Object.freeze(value)
}

async function importPinnedRunner(manifest) {
  const before = await read0400(manifest.runner.moduleFile, 'pre-import B2 Runner module', MAX_JSON)
  assert(sha256(before) === manifest.runner.moduleSha256, 'B2 Runner pre-import pin drifted')
  const loaded = await import(pathToFileURL(manifest.runner.moduleFile).href)
  exactKeys(loaded, ['createO4B2Runner'], 'B2 Runner module exports')
  assert(sha256(await read0400(manifest.runner.moduleFile, 'post-import B2 Runner module', MAX_JSON)) ===
    manifest.runner.moduleSha256,
    'B2 Runner changed during import')
  return loaded
}

async function invokePhase(input, phase) {
  await assertAllPins(input.manifest)
  await verifyExecutionModuleClosure(input.candidateConfig)
  const binding = input.manifest.phases[phase]
  const runtimePhaseEnvironment = { ...binding.environment }
  if (phase === 'D') {
    const sourceA3 = await read0400JSON(
      input.manifest.phases.C.environment.CHORA_O4_PROFILE_REUSE_SOURCE_A3_LEDGER_FILE,
      'runtime-derived phase-D source A3 binding', MAX_JSON)
    digest(sourceA3.bindingDigest, 'runtime-derived phase-D binding digest')
    runtimePhaseEnvironment.CHORA_O4_RECOVERY_RESIDUE_BINDING_DIGEST = sourceA3.bindingDigest
  }
  const env = Object.freeze({
    ...runtimePhaseEnvironment,
    CHORA_O4_TOTAL_MANIFEST_FILE: input.cli.manifestFile,
    CHORA_O4_TOTAL_MANIFEST_SHA256: input.manifestSha256,
    CHORA_O4_TOTAL_SESSION_AUTHORITY_FILE: input.manifest.runner.sessionAuthorityFile,
    CHORA_O4_TOTAL_TUPLE_IDENTITY: input.inspected.tupleIdentity,
    CHORA_O4_TOTAL_RUNNER_SHA256: input.manifest.runner.moduleSha256,
    PLAYWRIGHT_BROWSERS_PATH: input.manifest.playwright.browserRoot,
    COLIMA_HOME: input.manifest.roots.colimaHome,
    DOCKER_CONFIG: input.manifest.roots.dockerConfigRoot,
    DOCKER_CONTEXT: input.manifest.engine.contextName,
    HOME: input.manifest.roots.userRoot,
    TMPDIR: input.manifest.roots.temporaryRoot,
  })
  const argv = [input.manifest.playwright.cliFile, 'test', '--config', binding.configFile,
    '--workers=1', '--retries=0']
  const result = await runPhase(input.manifest.playwright.nodeFile, argv, env,
    input.manifest.timeouts.phaseMs, input.manifest.timeouts.closeMs)
  assert(result.code === 0 && result.signal === null, `real phase ${phase} failed`)
}

async function runPhase(file, argv, env, timeoutMs, closeTimeoutMs) {
  const child = spawn(file, argv, {
    shell: false, detached: true, cwd: workspaceRoot, env, stdio: 'inherit',
  })
  let timer
  try {
    const result = await Promise.race([
      new Promise((resolvePromise, reject) => {
        child.once('error', reject)
        child.once('exit', (code, signal) => resolvePromise({ code, signal }))
      }),
      new Promise((_, reject) => { timer = setTimeout(() => {
        killProcessGroup(child.pid, 'SIGKILL'); reject(new Error('real Playwright phase timed out'))
      }, timeoutMs) }),
    ])
    if (processGroupExists(child.pid)) {
      killProcessGroup(child.pid, 'SIGKILL')
      await waitForProcessGroupDeath(child.pid, closeTimeoutMs, 'real Playwright phase')
      throw new Error('real Playwright phase left a descendant process')
    }
    return result
  } catch (error) {
    killProcessGroup(child.pid, 'SIGKILL')
    await waitForProcessGroupDeath(child.pid, closeTimeoutMs, 'failed real Playwright phase')
    throw error
  } finally { clearTimeout(timer) }
}

async function revalidateBoundary(input, expectedCount) {
  const currentManifest = await read0400(input.cli.manifestFile, 'reopened total manifest', MAX_JSON)
  assert(sha256(currentManifest) === input.manifestSha256, 'total manifest drifted between phases')
  await revalidatePrivateEvidenceSnapshot(input.privateEvidenceSnapshot, input.manifest)
  await assertAllPins(input.manifest)
  await verifyExecutionModuleClosure(input.candidateConfig)
  await verifyRunnerSourceBinding(input.candidateConfig, input.manifest)
  const handoff = await input.candidateModule.buildHandoff(
    input.manifest.candidate.tupleFile, input.manifest.roots.receiptRoot,
    { repositoryCapture: input.repositoryCapture })
  assert(handoff.tupleIdentity === input.inspected.tupleIdentity &&
    handoff.receiptDigests.length === expectedCount,
  `receipt chain does not contain the exact 01-${String(expectedCount).padStart(2, '0')} prefix`)
  assert(handoff.allowedNextPhases.length === (expectedCount === 9 ? 0 : 1) &&
    (expectedCount === 9 || handoff.allowedNextPhases[0] === receiptPhases[expectedCount]),
  'receipt-chain next phase drifted')
  const names = (await readdir(input.manifest.roots.receiptRoot)).filter((name) => /^\d{2}-/.test(name)).sort()
  assert(JSON.stringify(names) === JSON.stringify(receiptPhases.slice(0, expectedCount).map((phase, index) =>
    `${String(index + 1).padStart(2, '0')}-${phase}.json`)), 'receipt filenames are not exact 01-09')
  return handoff
}

async function postflight(input, session) {
  const p = input.manifest.postflight
  await verifyExecutionModuleClosure(input.candidateConfig)
  const finalModule = await import(pathToFileURL(join(workspaceRoot, 'e2e/o4-final-evidence-record.mjs')).href)
  await verifyExecutionModuleClosure(input.candidateConfig)
  await finalModule.validateFinalEvidenceTree(p.finalEvidenceDirectory)
  const a3 = await read0400JSON(p.sourceA3LedgerFile, 'sealed contiguous A3', MAX_JSON)
  assert(a3.schemaVersion === 'chora.m1-o4-sealed-a3-runner-ledger.v1' &&
    a3.status === 'passed' && a3.complete === true && a3.auditSealed === true &&
    a3.tupleIdentity === input.inspected.tupleIdentity,
  'final A3 is not sealed/complete/session-bound')
  assert(Array.isArray(a3.commandAudit) && a3.commandAudit.length === a3.auditRecordCount &&
    a3.commandAudit.at(-1)?.digest === a3.auditFinalDigest,
  'final A3 is not complete and contiguous')
  const b1 = await read0400JSON(join(p.finalEvidenceDirectory, 'b1-composite.json'), 'B1 composite', MAX_JSON)
  const installed = await read0400JSON(join(p.finalEvidenceDirectory, 'installed-evidence.json'),
    'installed evidence', MAX_JSON)
  assert(b1.schemaVersion === 'chora.m1-o4-b1-composite.v1' &&
    installed.schemaVersion === 'chora.m1-o4-fresh-installed-acceptance.v1',
  'B1/installed evidence schema drifted')
  const profiles = await Promise.all(['minimal.json', 'standard.json', 'trusted-local.json'].map((name) =>
    read0400JSON(join(p.finalEvidenceDirectory, 'profiles', name), `UI profile ${name}`, MAX_JSON)))
  const phaseCRecord = await read0400JSON(p.phaseCRecordFile, 'phase-C public cohort record', MAX_JSON)
  assert(profiles[0].profile === 'minimal' && profiles[1].profile === 'standard' &&
    profiles[2].profile === 'trusted_local' && phaseCRecord.standardReuseCount === 3 &&
    phaseCRecord.journeyReceiptDigests?.length === 5,
  'Minimal/three Standard/Trusted UI evidence is incomplete')
  const recovery = await read0400JSON(join(p.finalEvidenceDirectory, 'recovery.json'), 'D recovery', MAX_JSON)
  assert(Array.isArray(recovery.scenarios) && recovery.scenarios.length === 8,
    'D does not contain exactly eight public UI scenarios')
  await exactCount(p.phaseDScreenshotDirectory, 8, '.png', 'D PNG evidence')
  await exactCount(p.phaseDScreenshotMetadataDirectory, 8, '.json', 'D PNG metadata')
  await exactCount(p.phaseDScreenshotOCRDirectory, 8, '.json', 'D offline OCR')
  const docker = await read0400JSON(join(p.finalEvidenceDirectory, 'raw', 'docker-operation-ledger.json'),
    'Docker operation ledger', MAX_JSON)
  assert(docker.sourceA3LedgerSha256 === sha256(await readFile(p.sourceA3LedgerFile)),
    'Docker ledger is not derived from the exact sealed A3')
  const service = await read0400JSON(join(p.finalEvidenceDirectory, 'raw', 'service-closure.json'),
    'service/process closure', MAX_JSON)
  assert(service.status === 'closed' && service.services.every((value) =>
    value.processGroupDead === true && value.descendantsDead === true),
  'service/process-group residue is nonzero')
  const residue = await read0400JSON(p.publicResidueFile, 'terminal workload residue', MAX_JSON)
  assert(residue.status === 'proven_zero' && exactPublicResidue(residue, input.manifest.identity.generationId),
    'terminal workload residue is nonzero')
  const reuse = await read0400JSON(join(p.finalEvidenceDirectory, 'image-reuse.json'),
    'retained shared-image proof', MAX_JSON)
  assert(reuse.status === 'passed' && Array.isArray(reuse.sharedImages) && reuse.sharedImages.length === 2,
    'retained shared-image proof is incomplete')
  const phaseD = await read0400JSON(p.phaseDManifestFile, 'phase-D manifest', MAX_JSON)
  const runnerPins = collectNamedValues(phaseD, 'runnerModuleSha256')
  assert(runnerPins.length > 0 && runnerPins.every((value) => value === input.manifest.runner.moduleSha256),
    'D exporter Runner SHA differs from the total/E Runner SHA')
  assert(session.authorityDigest === (await read0400JSON(input.manifest.runner.sessionAuthorityFile,
    'reopened Runner session', MAX_JSON)).authorityDigest, 'Runner session authority drifted')
  await syncAndReopenEvidenceTree(p.finalEvidenceDirectory)
}

async function writeCompletion(input, session, engineResidue) {
  const handoff = await revalidateBoundary(input, 9)
  const p = input.manifest.postflight
  const draft = {
    schemaVersion: COMPLETION_SCHEMA, status: 'passed', acceptanceClass: 'real_acceptance',
    manifestSha256: input.manifestSha256, manifestSelfDigest: input.manifest.selfDigest,
    runnerSha256: input.manifest.runner.moduleSha256, sessionAuthoritySha256:
      sha256(await readFile(input.manifest.runner.sessionAuthorityFile)),
    sessionAuthorityDigest: session.authorityDigest,
    tupleIdentity: input.inspected.tupleIdentity,
    receiptDigests: handoff.receiptDigests,
    sealedA3Sha256: sha256(await readFile(p.sourceA3LedgerFile)),
    b1Sha256: sha256(await readFile(join(p.finalEvidenceDirectory, 'b1-composite.json'))),
    installedEvidenceSha256: sha256(await readFile(join(p.finalEvidenceDirectory, 'installed-evidence.json'))),
    freshEnginePreflightSha256: sha256(await readFile(input.manifest.engine.zeroImagePreflightFile)),
    freshEngineResidueSha256: sha256(await readFile(input.manifest.engine.cleanupResidueFile)),
    freshEngineResidueDigest: engineResidue.digest,
  }
  const completion = { ...draft, completionDigest: sha256(canonicalJSONStringify(draft)) }
  await writeTotalStandaloneAuthority(Buffer.from(`${JSON.stringify(completion, null, 2)}\n`),
    input.manifest.completionFile, input.repositoryCapture)
}

async function closeWithoutSeal(input) {
  const loaded = await importPinnedRunner(input.manifest)
  const runner = await loaded.createO4B2Runner({
    view: 'E', manifestFile: input.cli.manifestFile, manifestSha256: input.manifestSha256,
    sessionAuthorityFile: input.manifest.runner.sessionAuthorityFile,
  })
  try {
    await runner.closeAllServices({
      tupleFile: input.manifest.candidate.tupleFile,
      sourceA3LedgerFile: input.manifest.postflight.sourceA3LedgerFile,
      timeoutMs: input.manifest.timeouts.closeMs,
    })
  } finally {
    await absent(input.manifest.runner.controlSocketPath, 'failed total Runner control socket')
  }
}

function exactPublicResidue(value, generationId) {
  const zero = (item) => item && item.count === 0 && digestRE.test(item.aggregateSha256)
  const docker = value.docker
  const references = value.generationReferences
  return docker && ['containers', 'verifierContainers', 'networks', 'volumes', 'configs',
    'managedWorkspaces', 'attemptProcessGroups'].every((name) => zero(docker[name])) &&
    zero(value.verificationWorkspaces) && references?.serving?.count === 1 &&
    digestRE.test(references.serving.aggregateSha256) && zero(references.activeAttempts) &&
    zero(references.recoverableAttempts) && references.currentGenerationSha256 === sha256(generationId)
}

function collectNamedValues(value, name) {
  const result = []
  const visit = (candidate) => {
    if (!candidate || typeof candidate !== 'object') return
    for (const [key, item] of Object.entries(candidate)) {
      if (key === name) result.push(item)
      else visit(item)
    }
  }
  visit(value)
  return result
}

async function exactCount(path, count, suffix, label) {
  const names = await readdir(path)
  assert(names.length === count && names.every((name) => name.endsWith(suffix)),
    `${label} does not contain exactly ${count} pinned entries`)
  for (const name of names) await read0400(join(path, name), `${label} ${name}`, MAX_JSON)
}

async function importCandidateModule(config) {
  await verifyExecutionModuleClosure(config)
  const loaded = await import(pathToFileURL(join(workspaceRoot,
    'e2e/o4-candidate-install-orchestrator.mjs')).href)
  for (const name of ['inspectStaticCandidate', 'inspectAuthenticatedCandidate',
    'executeCandidateInstall', 'buildHandoff']) {
    assert(typeof loaded[name] === 'function', `candidate orchestrator ${name} export is unavailable`)
  }
  await verifyExecutionModuleClosure(config)
  return loaded
}

async function verifyExecutionModuleClosure(config) {
  const manifestBytes = await read0444(config.paths.sourceManifestFile,
    'module-closure source manifest', MAX_JSON)
  assert(sha256(manifestBytes) === config.authority.sourceManifestSha256,
    'module-closure source manifest pin drifted')
  const manifest = parseJSON(manifestBytes, 'module-closure source manifest')
  assert(Array.isArray(manifest.files), 'module-closure source manifest files are invalid')
  for (const moduleRelative of executionModuleClosure) {
    const matches = manifest.files.filter((entry) => entry?.path === moduleRelative ||
      entry?.path?.endsWith(`/${moduleRelative}`))
    assert(matches.length === 1, `module closure ${moduleRelative} is not uniquely manifest-bound`)
    const entry = matches[0]
    exactKeys(entry, ['path', 'size', 'mode', 'sha256'], `module closure ${moduleRelative} entry`)
    assert(typeof entry.path === 'string' && entry.path.split('/').every((part) =>
      part.length > 0 && part !== '.' && part !== '..') && !entry.path.includes('\\') &&
      !isAbsolute(entry.path), `module closure ${moduleRelative} path is unsafe`)
    digest(entry.sha256, `module closure ${moduleRelative} digest`)
    assert(Number.isSafeInteger(entry.size) && entry.size > 0 && entry.size <= MAX_PIN,
      `module closure ${moduleRelative} size is invalid`)
    assert(/^[0-7]{3,4}$/.test(entry.mode), `module closure ${moduleRelative} mode is invalid`)
    const actualPath = join(workspaceRoot, moduleRelative)
    const sourcePath = join(config.paths.sourceBundleRoot, 'source', ...entry.path.split('/'))
    assert(within(workspaceRoot, actualPath) && within(join(config.paths.sourceBundleRoot, 'source'), sourcePath),
      `module closure ${moduleRelative} escaped its authority root`)
    const expectedMode = entry.mode.replace(/^0(?=[0-7]{3}$)/, '')
    const mode = Number.parseInt(expectedMode, 8)
    const actualBytes = await readBoundedRegular(actualPath, `runtime module ${moduleRelative}`, entry.size, mode)
    const sourceBytes = await readBoundedRegular(sourcePath, `source module ${moduleRelative}`, entry.size, mode)
    assert(actualBytes.length === entry.size && sourceBytes.length === entry.size &&
      sha256(actualBytes) === entry.sha256 && sha256(sourceBytes) === entry.sha256 &&
      actualBytes.equals(sourceBytes), `runtime module ${moduleRelative} drifted from its manifest-bound source`)
  }
}

export async function verifyRunnerSourceBinding(config, totalManifest) {
  const manifestBytes = await read0444(config.paths.sourceManifestFile,
    'Runner-binding source manifest', MAX_JSON)
  assert(sha256(manifestBytes) === config.authority.sourceManifestSha256,
    'Runner-binding source manifest pin drifted')
  const sourceManifest = parseJSON(manifestBytes, 'Runner-binding source manifest')
  assert(Array.isArray(sourceManifest.files), 'Runner-binding source manifest files are invalid')
  const runnerRelative = 'e2e/o4-b2-runner.mjs'
  const runnerEntries = sourceManifest.files.filter((entry) => entry?.path === runnerRelative ||
    entry?.path?.endsWith(`/${runnerRelative}`))
  assert(runnerEntries.length === 1 && runnerEntries[0].path === runnerRelative,
    'Runner source manifest requires one unique exact entry')
  const entry = runnerEntries[0]
  exactKeys(entry, ['path', 'size', 'mode', 'sha256'], 'Runner source manifest entry')
  assert(entry.path === runnerRelative && entry.mode === '0644' &&
    Number.isSafeInteger(entry.size) && entry.size > 0 && entry.size <= MAX_JSON,
  'Runner source manifest entry shape drifted')
  digest(entry.sha256, 'Runner source manifest entry digest')
  assert(totalManifest.runner.moduleSha256 === entry.sha256,
    'total Runner artifact pin is not the exact source-manifest Runner digest')

  const workspaceRunnerFile = join(workspaceRoot, runnerRelative)
  const sourceRunnerFile = join(config.paths.sourceBundleRoot, 'source', ...runnerRelative.split('/'))
  assert(within(workspaceRoot, workspaceRunnerFile) &&
    within(join(config.paths.sourceBundleRoot, 'source'), sourceRunnerFile),
  'Runner binding escaped its exact authority root')
  const workspaceBytes = await readBoundedRegular(workspaceRunnerFile,
    'workspace B2 Runner', entry.size, 0o644)
  const sourceBytes = await readBoundedRegular(sourceRunnerFile,
    'source-bundle B2 Runner', entry.size, 0o644)
  const artifactBytes = await read0400(totalManifest.runner.moduleFile,
    'total pinned B2 Runner artifact', entry.size)
  assert(workspaceBytes.length === entry.size && sourceBytes.length === entry.size &&
    artifactBytes.length === entry.size && sha256(workspaceBytes) === entry.sha256 &&
    sha256(sourceBytes) === entry.sha256 && sha256(artifactBytes) === entry.sha256 &&
    workspaceBytes.equals(sourceBytes) && workspaceBytes.equals(artifactBytes),
  'workspace/source-bundle/total-artifact Runner bytes drifted')
  return Object.freeze({ size: entry.size, sha256: entry.sha256 })
}

export async function readPrivateEvidenceSnapshot(totalManifest) {
  try {
    const environment = totalManifest.phases.E.environment
    const markerFile = exactAbsolute(environment.CHORA_O4_FINAL_PRIVATE_MARKER_FILE,
      'private environment marker')
    const corpusFile = exactAbsolute(environment.CHORA_O4_FINAL_CREDENTIAL_CORPUS_FILE,
      'private credential corpus')
    assert(markerFile !== corpusFile, 'private marker and corpus paths overlap')
    await assertOwnerDirectory(dirname(markerFile), 'private marker parent')
    await assertOwnerDirectory(dirname(corpusFile), 'private corpus parent')
    const markerInput = await stableOwnerFile(markerFile, 'private environment marker',
      64 * 1024, 0o400)
    const corpusInput = await stableOwnerFile(corpusFile, 'private credential corpus',
      256 * 1024, 0o400)
    const marker = parseJSON(markerInput.bytes, 'private environment marker')
    const corpus = parseJSON(corpusInput.bytes, 'private credential corpus')
    exactKeys(marker, ['schemaVersion', 'markerNonce', 'environmentId', 'installId',
      'generationId', 'credentialCorpusId', 'credentialCorpusSha256'],
    'private environment marker')
    assert(marker.schemaVersion === 'chora.m1-o4-private-environment-marker.v1' &&
      typeof marker.markerNonce === 'string' && /^[A-Za-z0-9_-]{43}$/.test(marker.markerNonce),
    'private environment marker schema or nonce drifted')
    assert(marker.environmentId ===
      `env_${sha256(`chora:m1-o4:environment:${marker.markerNonce}`).slice(0, 48)}`,
    'private environment marker environment identity drifted')
    assertOpaqueID(marker.installId, 'ins', 'private marker install identity')
    assertOpaqueID(marker.generationId, 'gen', 'private marker generation identity')
    assertOpaqueID(marker.credentialCorpusId, 'cor', 'private marker corpus identity')
    digest(marker.credentialCorpusSha256, 'private marker corpus digest')
    sameJSON({ environmentId: marker.environmentId, installId: marker.installId,
      generationId: marker.generationId }, {
      environmentId: totalManifest.identity.environmentId,
      installId: totalManifest.identity.installId,
      generationId: totalManifest.identity.generationId,
    }, 'private marker identity does not equal total identity')

    exactKeys(corpus, ['schemaVersion', 'corpusId', 'secrets'], 'private credential corpus')
    assert(corpus.schemaVersion === 'chora.m1-o4-private-credential-corpus.v1' &&
      corpus.corpusId === marker.credentialCorpusId &&
      sha256(corpusInput.bytes) === marker.credentialCorpusSha256,
    'private credential corpus identity or raw digest drifted')
    assert(Array.isArray(corpus.secrets) && corpus.secrets.length >= 2 && corpus.secrets.length <= 32,
      'private credential corpus secret set is empty or unbounded')
    const secretNames = new Set()
    const secretDigests = new Set()
    for (const secret of corpus.secrets) {
      exactKeys(secret, ['name', 'value'], 'private credential corpus entry')
      assert(typeof secret.name === 'string' && /^[a-z][a-z0-9_-]{1,63}$/.test(secret.name) &&
        typeof secret.value === 'string' && secret.value.length >= 16 && secret.value.length <= 4096,
      'private credential corpus entry shape drifted')
      const secretDigest = sha256(secret.value)
      assert(!secretNames.has(secret.name) && !secretDigests.has(secretDigest),
        'private credential corpus entries are not unique')
      secretNames.add(secret.name); secretDigests.add(secretDigest)
    }
    return Object.freeze({
      identity: Object.freeze({ environmentId: marker.environmentId,
        installId: marker.installId, generationId: marker.generationId }),
      credentialCorpusId: marker.credentialCorpusId,
      marker: Object.freeze({ path: markerFile, sha256: sha256(markerInput.bytes),
        file: immutableFileIdentity(markerInput.info) }),
      corpus: Object.freeze({ path: corpusFile, sha256: sha256(corpusInput.bytes),
        file: immutableFileIdentity(corpusInput.info) }),
    })
  } catch {
    throw new Error('private evidence snapshot is unavailable or drifted')
  }
}

export async function revalidatePrivateEvidenceSnapshot(expected, totalManifest) {
  const observed = await readPrivateEvidenceSnapshot(totalManifest)
  try {
    sameJSON(observed, expected, 'private evidence snapshot drifted')
  } catch {
    throw new Error('private evidence snapshot is unavailable or drifted')
  }
  return observed
}

function immutableFileIdentity(info) {
  return Object.freeze({ dev: info.dev, ino: info.ino, mode: info.mode & 0o777,
    nlink: info.nlink, uid: info.uid, size: info.size,
    mtimeMs: info.mtimeMs, ctimeMs: info.ctimeMs })
}

async function syncAndReopenEvidenceTree(root) {
  await assertNoSymlinkAncestors(root)
  const visit = async (directory) => {
    const names = (await readdir(directory)).sort()
    for (const name of names) {
      const path = join(directory, name)
      const info = await lstat(path)
      assert(!info.isSymbolicLink() && info.uid === currentUID(), 'final evidence tree ownership/type drifted')
      if (info.isDirectory()) {
        assert((info.mode & 0o777) === 0o700, 'final evidence directory is not owner-only 0700')
        await visit(path)
      }
      else {
        assert(info.isFile() && info.nlink === 1 && (info.mode & 0o777) === 0o400,
          'final evidence file is not one immutable owner file')
        const stable = await stableOwnerFile(path, 'final evidence reopen', MAX_PIN, 0o400)
        assert(sameFileIdentity(stable.info, info), 'final evidence changed while reopening')
      }
    }
    await syncDirectory(directory)
  }
  await visit(root)
}

function pathValues(value) {
  return [
    value.candidate.configFile, value.candidate.tupleFile,
    value.repository.root,
    value.runner.moduleFile, value.runner.controlSocketPath, value.runner.sessionAuthorityFile,
    value.serviceController.executableFile, value.serviceController.requestFile,
    value.ocr.executableFile, value.ocr.engineBindingFile, value.ocr.sandboxExecutableFile,
    value.ocr.sandboxProfileFile,
    value.engine.colimaFile, value.engine.limaPrefixRoot, value.engine.limaFile,
    value.engine.limaWrapperFile, value.engine.limaTemplatesRoot,
    value.engine.limaGuestAgentFile, value.engine.dockerFile,
    value.engine.diskImageFile, value.engine.sandboxExecutableFile,
    value.engine.sandboxProfileFile, value.engine.endpointSocketPath,
    value.engine.zeroImagePreflightFile, value.engine.cleanupResidueFile,
    value.playwright.nodeFile, value.playwright.cliFile,
    value.playwright.packageRoot, value.playwright.browserRoot,
    ...Object.values(value.roots), ...phaseOrder.map((phase) => value.phases[phase].configFile),
    ...Object.values(value.postflight), value.completionFile,
  ]
}

function digestValues(value) {
  return [value.selfDigest, value.candidate.configSha256,
    value.repository.closureSha256,
    value.runner.moduleSha256, value.serviceController.sha256,
    value.ocr.executableSha256, value.ocr.engineBindingSha256,
    value.ocr.sandboxExecutableSha256, value.ocr.sandboxProfileSha256,
    value.engine.colimaSha256, value.engine.limaPrefixClosureSha256,
    value.engine.limaInfoDigest,
    value.engine.limaSha256, value.engine.limaWrapperSha256,
    value.engine.limaTemplatesClosureSha256, value.engine.limaGuestAgentSha256,
    value.engine.dockerSha256,
    value.engine.diskImageSha256, value.engine.sandboxExecutableSha256,
    value.engine.sandboxProfileSha256, value.engine.endpointDigest,
    value.playwright.nodeSha256, value.playwright.cliSha256,
    value.playwright.packageClosureSha256, value.playwright.browserClosureSha256,
    ...phaseOrder.map((phase) => value.phases[phase].configSha256)]
}

async function read0400JSON(path, label, max) { return parseJSON(await read0400(path, label, max), label) }
async function read0400(path, label, max) {
  return (await stableOwnerFile(path, label, max, 0o400)).bytes
}

async function read0444(path, label, max) {
  return (await stableOwnerFile(path, label, max, 0o444)).bytes
}

async function read0500Executable(path, label, max) {
  return (await stableOwnerFile(path, label, max, 0o500)).bytes
}

async function read0755SystemExecutable(path, label, max) {
  exactAbsolute(path, label)
  const before = await lstat(path)
  assert(before.isFile() && !before.isSymbolicLink() && before.nlink === 1 && before.uid === 0 &&
    before.size > 0 && before.size <= max && (before.mode & 0o777) === 0o755,
  `${label} is not one pinned root-owned 0755 system executable`)
  const bytes = await readFile(path)
  const after = await lstat(path)
  assert(sameFileIdentity(before, after) && bytes.length === after.size,
    `${label} changed while read`)
  return bytes
}

async function readBoundedRegular(path, label, max, expectedMode = undefined) {
  return (await stableOwnerFile(path, label, max, expectedMode)).bytes
}

async function stableOwnerFile(path, label, max, expectedMode = undefined) {
  exactAbsolute(path, label); await assertNoSymlinkAncestors(path)
  const handle = await open(path, 'r')
  try {
    const opened = await handle.stat()
    assert(opened.isFile() && opened.nlink === 1 && opened.uid === currentUID() &&
      opened.size > 0 && opened.size <= max, `${label} is not one bounded owner regular file`)
    if (expectedMode !== undefined) assert((opened.mode & 0o777) === expectedMode,
      expectedMode === 0o500 ? `${label} must be exact owner-only executable 0500` :
        expectedMode === 0o400 ? `${label} must be exact owner-only immutable 0400` :
          `${label} mode drifted`)
    const bytes = await handle.readFile()
    const afterRead = await handle.stat()
    assert(sameFileIdentity(opened, afterRead) && bytes.length === opened.size,
      `${label} changed while reading its opened inode`)
    const reopened = await lstat(path)
    assert(reopened.isFile() && !reopened.isSymbolicLink() && sameFileIdentity(opened, reopened),
      `${label} path identity changed after read`)
    return { bytes, info: opened }
  } finally { await handle.close() }
}

async function stableOwnerFileDigest(path, label, exactSize, expectedMode) {
  exactAbsolute(path, label); await assertNoSymlinkAncestors(path)
  const handle = await open(path, 'r')
  try {
    const opened = await handle.stat()
    assert(opened.isFile() && opened.nlink === 1 && opened.uid === currentUID() &&
      opened.size === exactSize && (opened.mode & 0o777) === expectedMode,
    `${label} is not the exact owner-controlled immutable file`)
    const hash = createHash('sha256')
    const buffer = Buffer.allocUnsafe(1024 * 1024)
    let position = 0
    while (position < exactSize) {
      const { bytesRead } = await handle.read(buffer, 0, Math.min(buffer.length, exactSize - position), position)
      assert(bytesRead > 0, `${label} ended before its pinned size`)
      hash.update(buffer.subarray(0, bytesRead)); position += bytesRead
    }
    const afterRead = await handle.stat()
    assert(sameFileIdentity(opened, afterRead), `${label} changed while hashing`)
    const reopened = await lstat(path)
    assert(reopened.isFile() && !reopened.isSymbolicLink() && sameFileIdentity(opened, reopened),
      `${label} path identity changed after hashing`)
    return { size: position, sha256: hash.digest('hex') }
  } finally { await handle.close() }
}

async function stableDirectoryClosure(root, label) {
  exactAbsolute(root, label); await assertNoSymlinkAncestors(root)
  const rootBefore = await lstat(root)
  assert(rootBefore.isDirectory() && !rootBefore.isSymbolicLink() && rootBefore.uid === currentUID() &&
    (rootBefore.mode & 0o022) === 0, `${label} root is not owner-controlled and non-writable by others`)
  const entries = []
  let totalBytes = 0
  const visit = async (directory, prefix) => {
    const directoryBefore = await lstat(directory)
    assert(directoryBefore.isDirectory() && !directoryBefore.isSymbolicLink() &&
      directoryBefore.uid === currentUID() && (directoryBefore.mode & 0o022) === 0,
    `${label} contains an unsafe directory`)
    const names = (await readdir(directory)).sort()
    for (const name of names) {
      assert(name.length > 0 && name !== '.' && name !== '..' && !name.includes('/') && !name.includes('\0'),
        `${label} contains an unsafe entry name`)
      const path = join(directory, name)
      const relativePath = prefix ? `${prefix}/${name}` : name
      const before = await lstat(path)
      assert(before.uid === currentUID(), `${label} contains a foreign entry`)
      if (before.isSymbolicLink()) {
        const target = await readlink(path)
        assert(target.length > 0 && !isAbsolute(target) && !target.includes('\0'),
          `${label} symlink ${relativePath} is unsafe`)
        const resolvedTarget = resolve(dirname(path), target)
        assert(resolvedTarget === root || within(root, resolvedTarget),
          `${label} symlink ${relativePath} escapes root`)
        const canonicalTarget = await realpath(resolvedTarget)
        assert(canonicalTarget === root || within(root, canonicalTarget),
          `${label} symlink ${relativePath} resolves outside root`)
        const after = await lstat(path)
        assert(sameFileIdentity(before, after) && await readlink(path) === target,
          `${label} symlink changed while hashing`)
        entries.push({ path: relativePath, type: 'symlink', mode: before.mode & 0o777, target })
        assert(entries.length <= 100_000, `${label} exceeds its bounded closure`)
      } else if (before.isDirectory()) {
        assert((before.mode & 0o022) === 0, `${label} contains a group/world-writable directory`)
        entries.push({ path: relativePath, type: 'directory', mode: before.mode & 0o777 })
        assert(entries.length <= 100_000, `${label} exceeds its bounded closure`)
        await visit(path, relativePath)
      } else {
        assert(before.isFile() && before.nlink === 1 && before.size >= 0 && (before.mode & 0o022) === 0,
          `${label} contains a non-regular, multiply-linked, or group/world-writable file`)
        totalBytes += before.size
        assert(entries.length < 100_000 && totalBytes <= 2 * 1024 * 1024 * 1024,
          `${label} exceeds its bounded closure`)
        const bytes = await readFile(path)
        const after = await lstat(path)
        assert(sameFileIdentity(before, after) && bytes.length === before.size,
          `${label} entry changed while hashing`)
        entries.push({ path: relativePath, type: 'file', mode: before.mode & 0o777,
          size: before.size, sha256: sha256(bytes) })
      }
    }
    const directoryAfter = await lstat(directory)
    assert(sameDirectoryIdentity(directoryBefore, directoryAfter),
      `${label} directory changed while hashing`)
  }
  await visit(root, '')
  assert(entries.length > 0, `${label} is empty`)
  const rootAfter = await lstat(root)
  assert(sameDirectoryIdentity(rootBefore, rootAfter), `${label} root changed while hashing`)
  return { digest: sha256(canonicalJSONStringify({ rootMode: rootBefore.mode & 0o777, entries })),
    rootMode: rootBefore.mode & 0o777, entries, entryCount: entries.length, totalBytes }
}

export async function writeTotalStandaloneAuthority(
  bytes, path, repositoryCapture, dependencyValue = undefined,
) {
  const dependencies = standaloneAuthorityDependencies(dependencyValue)
  await assertOwnerDirectory(dirname(path), 'exclusive output parent')
  const controllerDependencies = {}
  for (const key of ['spawn', 'beforeSpawn', 'timeoutMs']) {
    if (dependencies[key] !== undefined) controllerDependencies[key] = dependencies[key]
  }
  const receipt = await writeReadonlyOwnedPath({
    capability: repositoryCapture, path, bytes,
  }, Object.keys(controllerDependencies).length === 0 ? undefined : controllerDependencies)
  if (dependencies.afterNativeReceipt !== null) {
    await dependencies.afterNativeReceipt(Object.freeze({ path, receipt }))
  }
  const pathInfo = await lstat(path, { bigint: true })
  assert(matchesNativeReceiptIdentity(pathInfo, receipt.identity),
    'standalone authority path no longer names the native receipt inode')
  const reopened = await stableOwnerFile(path, 'standalone native authority', bytes.length, 0o400)
  const pathAfterRead = await lstat(path, { bigint: true })
  assert(matchesNativeReceiptIdentity(pathAfterRead, receipt.identity) &&
    reopened.bytes.equals(bytes) && sha256(reopened.bytes) === receipt.sha256,
  'standalone authority identity/mode/bytes drifted from native receipt')
  return receipt
}

function standaloneAuthorityDependencies(value) {
  if (value === undefined) return Object.freeze({
    spawn: undefined, beforeSpawn: undefined, afterNativeReceipt: null, timeoutMs: undefined,
  })
  assert(value && typeof value === 'object' && !Array.isArray(value),
    'standalone authority dependencies are not an object')
  const allowed = new Set(['spawn', 'beforeSpawn', 'afterNativeReceipt', 'timeoutMs'])
  assert(Object.keys(value).every((key) => allowed.has(key)),
    'standalone authority dependency keys drifted')
  for (const key of ['spawn', 'beforeSpawn', 'afterNativeReceipt']) {
    assert(value[key] === undefined || typeof value[key] === 'function',
      `standalone authority ${key} dependency is invalid`)
  }
  assert(value.timeoutMs === undefined || (Number.isSafeInteger(value.timeoutMs) &&
    value.timeoutMs > 0 && value.timeoutMs <= 120_000),
    'standalone authority timeout dependency is invalid')
  return Object.freeze({
    spawn: value.spawn, beforeSpawn: value.beforeSpawn,
    afterNativeReceipt: value.afterNativeReceipt ?? null, timeoutMs: value.timeoutMs,
  })
}

function matchesNativeReceiptIdentity(info, identity) {
  return identity !== null && info.isFile() && !info.isSymbolicLink() && info.nlink === 1n &&
    info.dev.toString() === identity.dev && info.ino.toString() === identity.ino &&
    Number(info.uid) === identity.uid && Number(info.gid) === identity.gid &&
    Number(info.mode) === identity.mode && identity.uid === currentUID()
}

async function syncDirectory(path) {
  const handle = await open(path, 'r')
  try { await handle.sync() } finally { await handle.close() }
}

async function assertOwnerDirectory(path, label) {
  exactAbsolute(path, label); await assertNoSymlinkAncestors(path)
  const info = await lstat(path, { bigint: true })
  assert(info.isDirectory() && !info.isSymbolicLink() && info.uid === BigInt(currentUID()) &&
    Number(info.mode & 0o777n) === 0o700, `${label} is not owner-only 0700`)
  return ownedPathIdentity(info)
}

async function assertNoSymlinkAncestors(path) {
  let current = resolve(path)
  while (true) {
    try { const info = await lstat(current); assert(!info.isSymbolicLink(), 'path has a symlink ancestor') }
    catch (error) { if (error.code !== 'ENOENT') throw error }
    const parent = dirname(current); if (parent === current) return; current = parent
  }
}

async function absent(path, label) {
  try { await lstat(path); throw new Error(`${label} already exists`) }
  catch (error) { if (error.code !== 'ENOENT') throw error }
}

function overlaps(left, right) { return left === right || within(left, right) || within(right, left) }
function within(parent, child) { const rel = relative(parent, child); return rel !== '' && rel !== '..' && !rel.startsWith(`..${sep}`) && !isAbsolute(rel) }
function processGroupExists(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 1) return false
  try { process.kill(-pid, 0); return true }
  catch (error) { if (error.code === 'ESRCH') return false; throw error }
}
function processExists(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 1) return false
  try { process.kill(pid, 0); return true }
  catch (error) { if (error.code === 'ESRCH') return false; throw error }
}
function killProcessGroup(pid, signal) {
  if (!Number.isSafeInteger(pid) || pid <= 1) return
  try { process.kill(-pid, signal) }
  catch (error) { if (error.code !== 'ESRCH') throw error }
}
function sameFileIdentity(left, right) {
  return left.dev === right.dev && left.ino === right.ino && left.mode === right.mode &&
    left.nlink === right.nlink && left.uid === right.uid && left.size === right.size &&
    left.mtimeMs === right.mtimeMs && left.ctimeMs === right.ctimeMs
}
function sameDirectoryIdentity(left, right) {
  return left.dev === right.dev && left.ino === right.ino && left.mode === right.mode &&
    left.uid === right.uid && left.mtimeMs === right.mtimeMs && left.ctimeMs === right.ctimeMs
}
async function waitForProcessGroupDeath(pid, timeoutMs, label) {
  if (!Number.isSafeInteger(pid) || pid <= 1) return
  const deadline = Date.now() + timeoutMs
  while (processGroupExists(pid)) {
    if (Date.now() >= deadline) throw new Error(`${label} process group did not reach ESRCH`)
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 10))
  }
}
async function waitForPIDDeath(pid, timeoutMs, label) {
  const deadline = Date.now() + timeoutMs
  while (processExists(pid)) {
    if (Date.now() >= deadline) throw new Error(`${label} PID did not reach ESRCH`)
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 10))
  }
}
function exactAbsolute(value, label) { assert(typeof value === 'string' && isAbsolute(value) && normalize(value) === value && resolve(value) === value && !value.includes('\0'), `${label} must be exact absolute`); return value }
function digest(value, label) { assert(typeof value === 'string' && digestRE.test(value), `${label} is invalid`); return value }
function assertOpaqueID(value, prefix, label) { assert(typeof value === 'string' && new RegExp(`^${prefix}_[A-Za-z0-9_-]{16,64}$`).test(value), `${label} is invalid`) }
function currentUID() { assert(typeof process.getuid === 'function', 'O4 total requires POSIX owner identity'); return process.getuid() }
function exactKeys(value, keys, label) { assert(value && typeof value === 'object' && !Array.isArray(value), `${label} is not an object`); assert(JSON.stringify(Object.keys(value).sort()) === JSON.stringify([...keys].sort()), `${label} keys drifted`) }
function sameJSON(left, right, message) { assert(canonicalJSONStringify(left) === canonicalJSONStringify(right), message) }
function parseJSON(bytes, label) { try { return JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) } }
function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function canonicalJSONStringify(value) { if (value === null || typeof value !== 'object') return JSON.stringify(value); if (Array.isArray(value)) return `[${value.map(canonicalJSONStringify).join(',')}]`; return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSONStringify(value[key])}`).join(',')}}` }
function assert(condition, message) { if (!condition) throw new Error(message) }
