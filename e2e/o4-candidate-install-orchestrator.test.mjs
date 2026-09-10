import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { createHash, randomBytes } from 'node:crypto'
import { EventEmitter } from 'node:events'
import {
  chmod, copyFile, link, lstat, mkdir, mkdtemp, readFile, readdir, realpath, rename, rm, rmdir, stat, symlink, unlink, writeFile,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { basename, dirname, join } from 'node:path'
import { PassThrough, Writable } from 'node:stream'
import test from 'node:test'
import { pathToFileURL } from 'node:url'
import { promisify } from 'node:util'

import {
  CANDIDATE_TUPLE_SCHEMA,
  MODEL_AUTHORITY_SCHEMA,
  appendHandoffReceipt as appendHandoffReceiptRaw,
  buildHandoff as buildHandoffRaw,
  buildO4FaultAuthority as buildO4FaultAuthorityRaw,
  canonicalJSONStringify,
  executeCandidateInstall as executeCandidateInstallRaw,
  finalizeEvidencePhaseE as finalizeEvidencePhaseERaw,
  inspectAuthenticatedCandidate as inspectAuthenticatedCandidateRaw,
  inspectStaticCandidate as inspectStaticCandidateRaw,
  restartCandidate as restartCandidateRaw,
  sha256,
  validateEngineProcessAuthority,
  validateCandidateTuple,
  validatePrivatePiManifest,
} from './o4-candidate-install-orchestrator.mjs'
import { inspectRepositoryAuthority as inspectRepositoryAuthorityRaw } from './o4-repository-authority.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'
import { ownedPathIdentity } from './o4-owned-path-cleanup.mjs'
import { buildFreshEnginePreflightAuthority } from './o4-total-real.mjs'

const execFileAsync = promisify(execFile)

let repositoryCapture
let cleanupRepositoryCapture
test.before(async () => {
  ({ capture: repositoryCapture, cleanup: cleanupRepositoryCapture } =
    await buildTestRepositoryCapture())
})
test.after(async () => { await cleanupRepositoryCapture() })

function inspectRepositoryAuthority(options) {
  return inspectRepositoryAuthorityRaw({ ...options, capture: repositoryCapture })
}

function inspectAuthenticatedCandidate(config, options = {}) {
  return inspectAuthenticatedCandidateRaw(config, { ...options, repositoryCapture })
}

function inspectStaticCandidate(config, options = {}) {
  return inspectStaticCandidateRaw(config, { ...options, repositoryCapture })
}

function executeCandidateInstall(config, dependencies = {}) {
  return executeCandidateInstallRaw(config, { ...dependencies, repositoryCapture })
}

function restartCandidate(config, dependencies = {}) {
  return restartCandidateRaw(config, { ...dependencies, repositoryCapture })
}

function buildO4FaultAuthority(options, dependencies = {}) {
  return buildO4FaultAuthorityRaw(options, { ...dependencies, repositoryCapture })
}

function appendHandoffReceipt(options, dependencies = {}) {
  return appendHandoffReceiptRaw(options, { ...dependencies, repositoryCapture })
}

function finalizeEvidencePhaseE(options, dependencies = {}) {
  return finalizeEvidencePhaseERaw(options, { ...dependencies, repositoryCapture })
}

function buildHandoff(tupleFile, receiptDirectory, dependencies = {}) {
  return buildHandoffRaw(tupleFile, receiptDirectory, { ...dependencies, repositoryCapture })
}

const d = (name) => sha256(`o4-orchestrator-test:${name}`)
const image = (name) => `sha256:${d(`image:${name}`)}`
const identity = {
  environmentId: `env_${'e'.repeat(48)}`, installId: `ins_${'i'.repeat(32)}`,
  generationId: `gen_${'g'.repeat(32)}`, platform: { os: 'darwin', architecture: 'arm64' },
}
const unsupportedProductHosts = Object.freeze([
  Object.freeze({ os: 'darwin', architecture: 'amd64' }),
  Object.freeze({ os: 'linux', architecture: 'arm64' }),
  Object.freeze({ os: 'linux', architecture: 'amd64' }),
])

const scenarioBindings = Object.freeze([
  Object.freeze({
    scenario: 'failure', taskId: 'task_018f0c4a-1a2f-7c3d-8e4f-1234567890ab',
    snapshotId: 'context_snapshot_018f0c4a-1a2f-7c3d-8e4f-1234567890ac',
    snapshotDigest: d('failure-snapshot'),
  }),
  Object.freeze({
    scenario: 'timeout', taskId: 'task_018f0c4a-1a2f-7c3d-8e4f-1234567890ad',
    snapshotId: 'context_snapshot_018f0c4a-1a2f-7c3d-8e4f-1234567890ae',
    snapshotDigest: d('timeout-snapshot'),
  }),
])

function finalA3(tuple, overrides = {}) {
  return {
    sealed: true, sha256: d('a3'), tupleIdentity: tuple.identity,
    environmentId: tuple.environmentId, installId: tuple.installId,
    generationId: tuple.generationId, ...overrides,
  }
}

function finalB1(tuple, sealedA3, overrides = {}) {
  return {
    schemaVersion: 'chora.m1-o4-b1-composite.v1', tupleIdentity: tuple.identity,
    environmentId: tuple.environmentId, installId: tuple.installId,
    generationId: tuple.generationId, sourceA3LedgerSha256: sealedA3.sha256,
    projection: d('projection'), ...overrides,
  }
}

function assertAggregateMessages(error, expected) {
  assert.equal(error instanceof AggregateError, true)
  const messages = error.errors.map((item) => item?.message)
  for (const pattern of expected) assert.equal(messages.some((message) => pattern.test(message)), true,
    `missing ${pattern} in ${JSON.stringify(messages)}`)
  return true
}

function finalValidation(tuple, sealedA3, composite, overrides = {}) {
  return {
    status: 'passed', tupleIdentity: tuple.identity, environmentId: tuple.environmentId,
    installId: tuple.installId, generationId: tuple.generationId,
    a3Sha256: sealedA3.sha256, b1Sha256: sha256(canonicalJSONStringify(composite)),
    ...overrides,
  }
}

function roles() {
  const managed = { artifactId: 'managed-pi-runtime', archiveSha256: d('managed-archive'), archiveSize: 97, dockerConfigImageId: image('managed-config') }
  const boundary = { artifactId: 'network-boundary', archiveSha256: d('boundary-archive'), archiveSize: 101, dockerConfigImageId: image('boundary-config') }
  return {
    managed_pi_runtime: { ...managed, policyDigest: d('managed-policy') },
    network_boundary: { ...boundary, policyDigest: d('boundary-policy') },
    independent_verifier: { ...managed, policyDigest: d('verifier-policy') },
    capability_probe: { ...managed, policyDigest: d('probe-policy') },
  }
}

function roleArray(bindings) {
  return Object.entries(bindings).map(([role, value]) => ({ role, ...value }))
}

async function writeJSON(path, value, mode = 0o600) {
  const text = `${JSON.stringify(value, null, 2)}\n`
  await writeFile(path, text, { mode })
  return sha256(text)
}

function fakeReadonlyMarkerSpawn(action) {
  return (file, argv, options) => {
    const child = new EventEmitter()
    child.stdout = new PassThrough()
    child.stderr = new PassThrough()
    const chunks = []
    child.stdin = new Writable({
      write(chunk, _encoding, callback) { chunks.push(Buffer.from(chunk)); callback() },
      final(callback) {
        Promise.resolve().then(async () => {
          const bytes = Buffer.concat(chunks)
          const result = await action({ file, argv: [...argv], options, bytes })
          if (result?.stdout !== undefined) child.stdout.write(result.stdout)
          if (result?.stderr !== undefined) child.stderr.write(result.stderr)
          callback()
          queueMicrotask(() => child.emit('close', result?.code ?? 0, result?.signal ?? null))
        }).catch((error) => {
          callback(error)
          queueMicrotask(() => child.emit('close', 1, null))
        })
      },
    })
    child.kill = () => queueMicrotask(() => child.emit('close', null, 'SIGKILL'))
    return child
  }
}

function readonlyMarkerReceipt(target, bytes, identity, extra = {}) {
  return `${JSON.stringify({
    schemaVersion: 'chora.m1-o4-owned-readonly-write-receipt.v1',
    status: 'passed', target, sha256: sha256(bytes), byteLength: bytes.length,
    identity, ...extra,
  })}\n`
}

async function syntheticNonAcceptanceInstallRunner(root, mutations = {}) {
  const controlRoot = join(root, 'runner-control')
  const playwrightPackageRoot = join(root, 'playwright-package')
  const playwrightBrowserRoot = join(root, 'playwright-browsers')
  await mkdir(controlRoot, { mode: 0o700 })
  await mkdir(playwrightPackageRoot, { mode: 0o700 })
  await mkdir(playwrightBrowserRoot, { mode: 0o700 })
  const runnerFile = join(root, 'o4-b2-runner.mjs')
  await copyFile(join(import.meta.dirname, 'o4-b2-runner.mjs'), runnerFile)
  await chmod(runnerFile, 0o400)
  const ownedPathFile = join(root, 'o4-owned-path-cleanup.mjs')
  await copyFile(join(import.meta.dirname, 'o4-owned-path-cleanup.mjs'), ownedPathFile)
  await chmod(ownedPathFile, 0o400)
  const runnerSha256 = sha256(await readFile(runnerFile))
  const nodeFile = join(root, 'synthetic-node')
  await writeFile(nodeFile, '#!/bin/sh\nexit 0\n', { mode: 0o500, flag: 'wx' })
  await chmod(nodeFile, 0o500)
  const cliFile = join(playwrightPackageRoot, 'cli.js')
  await writeFile(cliFile, 'process.exit(0)\n', { mode: 0o400, flag: 'wx' })
  await chmod(cliFile, 0o400)
  const candidateConfigFile = join(root, 'candidate-config.json')
  const runnerIdentity = {
    environmentId: `env_${'r'.repeat(32)}`,
    installId: `ins_${'r'.repeat(32)}`,
    generationId: `gen_${'r'.repeat(32)}`,
    platform: { os: 'darwin', architecture: 'arm64' },
  }
  const repository = { root: join(root, 'repository'), commit: 'a'.repeat(40),
    closureSha256: d('synthetic-runner-repository') }
  const candidateConfig = { identity: runnerIdentity,
    paths: { repositoryRoot: repository.root },
    authority: { repositoryCommit: repository.commit,
      repositoryClosureSha256: repository.closureSha256 } }
  mutations.candidateConfig?.(candidateConfig)
  const candidateConfigSha256 = await writeJSON(candidateConfigFile, candidateConfig, 0o400)
  const manifestFile = join(root, 'total-manifest.json')
  const sessionAuthorityFile = join(controlRoot, 'session.json')
  const controlSocketPath = join(controlRoot, 'runner.sock')
  const manifestDraft = {
    schemaVersion: 'chora.m1-o4-total-input.v1',
    status: 'authorized',
    acceptanceClass: 'synthetic_non_acceptance',
    selfDigest: null,
    identity: runnerIdentity,
    candidate: { tupleIdentity: null, configFile: candidateConfigFile,
      configSha256: candidateConfigSha256 },
    repository,
    runner: { moduleFile: runnerFile, moduleSha256: runnerSha256,
      controlSocketPath, sessionAuthorityFile },
    serviceController: {
      executableFile: repositoryCapture.executableFile,
      sha256: repositoryCapture.executableSha256,
      requestFile: join(controlRoot, 'service-controller-request.json'),
    },
    ocr: {}, engine: {},
    playwright: {
      nodeFile, nodeSha256: sha256(await readFile(nodeFile)),
      cliFile, cliSha256: sha256(await readFile(cliFile)),
      packageRoot: playwrightPackageRoot, packageClosureSha256: d('playwright-package'),
      browserRoot: playwrightBrowserRoot, browserClosureSha256: d('playwright-browsers'),
    },
    roots: {}, phases: {}, postflight: {},
    timeouts: { controllerMs: 1_000, exporterMs: 1_000, closeMs: 1_000 },
    completionFile: join(root, 'completion.json'),
  }
  mutations.manifest?.(manifestDraft)
  const manifest = { ...manifestDraft,
    selfDigest: sha256(canonicalJSONStringify(manifestDraft)) }
  const manifestSha256 = await writeJSON(manifestFile, manifest, 0o400)
  const authorityDraft = {
    schemaVersion: 'chora.m1-o4-runner-session-authority.v1', status: 'exclusive',
    manifestSha256, runnerSha256, socketPathDigest: sha256(controlSocketPath),
    tupleIdentity: d('synthetic-runner-tuple'), identity: runnerIdentity,
    parentPid: process.pid, sessionId: d('synthetic-runner-session'),
    sessionSecret: d('synthetic-runner-secret'), sequenceStart: 0,
  }
  await writeJSON(sessionAuthorityFile, { ...authorityDraft,
    authorityDigest: sha256(canonicalJSONStringify(authorityDraft)) }, 0o400)
  const loaded = await import(`${pathToFileURL(runnerFile).href}?install=${randomBytes(8).toString('hex')}`)
  return loaded.createO4B2Runner({
    view: 'install', manifestFile, manifestSha256, sessionAuthorityFile,
    inProcessTestTransport: true,
  })
}

function privatePiClosure(files) {
  const hash = createHash('sha256')
  const frame = (value) => { const bytes = Buffer.isBuffer(value) ? value : Buffer.from(value); const length = Buffer.alloc(8); length.writeBigUInt64BE(BigInt(bytes.length)); hash.update(length); hash.update(bytes) }
  frame('chora.pi-package-closure.v1')
  for (const file of files) { frame(file.path); frame(file.mode.toString(8).padStart(4, '0')); frame(String(file.size)); frame(Buffer.from(file.sha256, 'hex')) }
  return hash.digest('hex')
}

async function releaseContextDigest(root, relativePath) {
  const entries = []
  async function walk(directory, prefix = '') {
    const items = (await readdir(directory, { withFileTypes: true })).sort((a, b) =>
      a.name < b.name ? -1 : a.name > b.name ? 1 : 0)
    for (const item of items) {
      const path = join(directory, item.name); const name = prefix ? `${prefix}/${item.name}` : item.name
      if (item.isDirectory()) await walk(path, name)
      else {
        const bytes = await readFile(path); const info = await stat(path)
        entries.push({ path: name, mode: (info.mode & 0o777).toString(8).padStart(4, '0'), size: bytes.length, digest: sha256(bytes) })
      }
    }
  }
  await walk(join(root, ...relativePath.split('/')))
  const hash = createHash('sha256')
  for (const entry of entries) for (const value of [entry.path, entry.mode, String(entry.size), entry.digest]) hash.update(`${Buffer.byteLength(value)}:${value}`)
  return hash.digest('hex')
}

function actualSpecProjection(value) {
  return {
    schema_version: 'chora.release-assets-spec.v1', release_id: value.release_id, platform: value.platform,
    roles: value.roles.map((role) => ({ role: role.role, artifact_id: role.artifact_id,
      ...(role.alias_of_role === undefined ? {} : { alias_of_role: role.alias_of_role }), policy: role.policy })),
    artifacts: value.artifacts.map((artifact) => ({ id: artifact.id, recipe: artifact.recipe, context: artifact.context,
      recipe_provenance: artifact.recipe_provenance, build_inputs: artifact.build_inputs,
      ...(artifact.runtime === undefined ? {} : { runtime: artifact.runtime }), entrypoint: artifact.entrypoint,
      license_inventory: artifact.license_inventory, sbom: artifact.sbom })),
  }
}

async function rewriteActualManifest(config, mutate) {
  const path = config.paths.actualReleaseManifestFile
  const value = JSON.parse(await readFile(path, 'utf8'))
  await mutate(value)
  value.spec_sha256 = sha256(JSON.stringify(actualSpecProjection(value)))
  await chmod(path, 0o600)
  const digest = await writeJSON(path, value)
  await chmod(path, 0o444)
  config.authority.actualReleaseManifestSha256 = digest
  const offlinePath = config.paths.offlineBundleEvidenceFile
  const offline = JSON.parse(await readFile(offlinePath, 'utf8'))
  offline.releaseManifestSha256 = digest
  await chmod(offlinePath, 0o600)
  const offlineDigest = await writeJSON(offlinePath, offline)
  await chmod(offlinePath, 0o400)
  config.authority.offlineBundleEvidenceSha256 = offlineDigest
  const candidatePath = config.paths.candidateManifestFile
  const candidate = JSON.parse(await readFile(candidatePath, 'utf8'))
  candidate.offlineBundleEvidenceSha256 = offlineDigest
  await chmod(candidatePath, 0o600)
  config.authority.candidateManifestSha256 = await writeJSON(candidatePath, candidate)
  await chmod(candidatePath, 0o400)
  return value
}

async function fixture() {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'chora-o4-orchestrator-')))
  const inputs = join(root, 'inputs'); const sourceBundleRoot = join(inputs, 'bundle'); const sourceRoot = join(sourceBundleRoot, 'source')
  const webDirectory = join(inputs, 'web'); const releaseRoot = join(inputs, 'release-evidence')
  const actualReleaseRoot = join(inputs, 'actual-release'); const privatePiSourceRoot = join(inputs, 'private-pi-source')
  const repositoryRoot = join(inputs, 'repository'); const probeRuntimeRoot = join(inputs, 'probe-runtime')
  const colimaToolRoot = join(inputs, 'colima-tools')
  for (const path of [sourceRoot, webDirectory, releaseRoot, actualReleaseRoot, privatePiSourceRoot, repositoryRoot, probeRuntimeRoot, colimaToolRoot]) await mkdir(path, { recursive: true })

  await execFileAsync('/usr/bin/git', ['-C', repositoryRoot, 'init', '-q'])
  await writeFile(join(repositoryRoot, 'README.md'), 'synthetic repository\n', { mode: 0o644 })
  await execFileAsync('/usr/bin/git', ['-C', repositoryRoot, 'add', 'README.md'])
  await execFileAsync('/usr/bin/git', ['-C', repositoryRoot,
    '-c', 'user.name=O4 Fixture', '-c', 'user.email=chora@example.test',
    'commit', '-qm', 'fixture'])
  const repositoryAuthority = await inspectRepositoryAuthority({ repositoryRoot })

  const sourceFile = join(sourceRoot, 'README.md'); const sourceBytes = Buffer.from('immutable source\n')
  await writeFile(sourceFile, sourceBytes, { mode: 0o444 })
  const sourceEntry = { path: 'README.md', size: sourceBytes.length, mode: '444', sha256: sha256(sourceBytes) }
  const sourceAggregate = sha256(`${sourceEntry.path}\0${sourceEntry.mode}\0${sourceEntry.size}\0${sourceEntry.sha256}\n`)
  const sourceManifest = {
    schema_version: 'chora.local-alpha-source-bundle.v1', aggregate_sha256: sourceAggregate,
    source_path_policy: { path: 'distribution/v1/policies/source-path-policy.v1.json', sha256: d('source-policy') },
    install_only_projection: { files: 1, paths_sha256: d('install-paths'), aggregate_sha256: sourceAggregate },
    model_readable_projection: { files: 0, paths_sha256: d('model-paths'), aggregate_sha256: d('model-aggregate') },
    files: [sourceEntry],
  }
  const sourceManifestFile = join(sourceBundleRoot, 'source-manifest.json')
  const sourceManifestSha256 = await writeJSON(sourceManifestFile, sourceManifest, 0o444)

  const binaryFile = join(inputs, 'chora-prebuilt'); const binaryBytes = Buffer.from('prebuilt-chora-binary\n')
  await writeFile(binaryFile, binaryBytes, { mode: 0o500 }); const binarySha256 = sha256(binaryBytes)
  const webFile = join(webDirectory, 'index.html'); const webBytes = Buffer.from('<main>immutable</main>\n')
  await writeFile(webFile, webBytes, { mode: 0o444 })
  const webAggregateSha256 = sha256(`index.html\0${webBytes.length}\0${sha256(webBytes)}\n`)
  const dockerCLIFile = join(colimaToolRoot, 'docker'); const dockerBytes = Buffer.from('immutable-docker-cli\n')
  await writeFile(dockerCLIFile, dockerBytes, { mode: 0o500 }); const dockerCLISha256 = sha256(dockerBytes)
  const colimaSourceFile = join(inputs, 'colima-source'); await writeFile(colimaSourceFile, 'immutable-colima\n', { mode: 0o500 }); const colimaSourceSha256 = sha256('immutable-colima\n')
  const dockerClientSourceFile = join(inputs, 'docker-client-source'); await writeFile(dockerClientSourceFile, dockerBytes, { mode: 0o500 }); const dockerClientSourceSha256 = dockerCLISha256
  const authFile = join(inputs, 'auth.json'); await writeFile(authFile, '{"private":"oauth"}\n', { mode: 0o600 }); const authFileSha256 = sha256('{"private":"oauth"}\n')
  const caFile = join(inputs, 'enterprise-ca.pem'); await writeFile(caFile, 'private enterprise ca\n', { mode: 0o600 }); const caFileSha256 = sha256('private enterprise ca\n')
  const proxyURL = 'http://host.docker.internal:9981'; const modelURL = 'https://model.private.invalid/v1'

  async function boundFile(name, text = `${name}\n`) {
    const path = join(actualReleaseRoot, name); await mkdir(dirname(path), { recursive: true }); await writeFile(path, text, { mode: 0o444 })
    return { path: name, sha256: sha256(text) }
  }
  const roleNames = ['managed_pi_runtime', 'network_boundary', 'independent_verifier', 'capability_probe']
  const policyBindings = {}
  for (const role of roleNames) policyBindings[role] = { id: `${role.replaceAll('_', '-')}-policy`, ...await boundFile(`policies/${role}.json`) }
  const actualRoles = [
    { role: 'managed_pi_runtime', artifact_id: 'managed-pi-runtime', policy: policyBindings.managed_pi_runtime },
    { role: 'network_boundary', artifact_id: 'network-boundary', policy: policyBindings.network_boundary },
    { role: 'independent_verifier', artifact_id: 'managed-pi-runtime', alias_of_role: 'managed_pi_runtime', policy: policyBindings.independent_verifier },
    { role: 'capability_probe', artifact_id: 'managed-pi-runtime', alias_of_role: 'managed_pi_runtime', policy: policyBindings.capability_probe },
  ]
  const managedRuntime = {
    pi: { name: '@earendil-works/pi-coding-agent', version: '0.84.2', npm_integrity: `sha512-${Buffer.alloc(64, 7).toString('base64')}` },
    node: { version: '22.19.0', executable: '/usr/local/bin/node', version_output: 'v22.19.0' },
  }
  async function actualArtifact(id, archiveSize, runtime) {
    const contextPath = `artifacts/${id}`
    const baseReference = `registry.invalid/base/${id}@sha256:${d(`${id}-base`)}`
    const entrypoint = id === 'managed-pi-runtime' ? ['pi'] : ['/boundary']
    let recipeText = `ARG BASE_PULL_REFERENCE=${baseReference}\nFROM \${BASE_PULL_REFERENCE}\n`
    if (runtime) recipeText += `ARG PI_PACKAGE=${runtime.pi.name}\nARG PI_VERSION=${runtime.pi.version}\nARG PI_INTEGRITY=${runtime.pi.npm_integrity}\nRUN test "$(node --version)" = "${runtime.node.version_output}"; true\n`
    recipeText += `ENTRYPOINT ${JSON.stringify(entrypoint)}\n`
    const recipe = await boundFile(`${contextPath}/Dockerfile`, recipeText)
    const license_inventory = await boundFile(`${contextPath}/LICENSES.json`)
    const sbom = await boundFile(`${contextPath}/sbom.json`)
    if (id === 'network-boundary') {
      await writeFile(join(actualReleaseRoot, contextPath, 'codex-boundary.go'),
        'package main\n', { mode: 0o644 })
    }
    const local_docker_config_image_id = image(`${id}-config`)
    const archiveBytes = Buffer.alloc(archiveSize, id === 'managed-pi-runtime' ? 0x6d : 0x62)
    const archivePath = `archives/${id}.tar`
    await mkdir(dirname(join(actualReleaseRoot, archivePath)), { recursive: true })
    await writeFile(join(actualReleaseRoot, archivePath), archiveBytes, { mode: 0o444 })
    return {
      id, recipe, context: { path: contextPath, sha256: await releaseContextDigest(actualReleaseRoot, contextPath) },
      recipe_provenance: [recipe], build_inputs: [{ name: 'BASE_PULL_REFERENCE', oci_pull_reference: baseReference }],
      ...(runtime ? { runtime } : {}), entrypoint, license_inventory, sbom,
      image: { local_docker_config_image_id, archive: {
        format: 'docker-archive', path: archivePath, size: archiveBytes.length, sha256: sha256(archiveBytes),
      } },
    }
  }
  const actualArtifacts = [await actualArtifact('managed-pi-runtime', 97, managedRuntime), await actualArtifact('network-boundary', 101, null)]
  const artifactMap = Object.fromEntries(actualArtifacts.map((artifact) => [artifact.id, artifact]))
  const r = Object.fromEntries(actualRoles.map((role) => {
    const artifact = artifactMap[role.artifact_id]
    return [role.role, {
      artifactId: role.artifact_id, archiveSha256: artifact.image.archive.sha256,
      archiveSize: artifact.image.archive.size,
      dockerConfigImageId: artifact.image.local_docker_config_image_id, policyDigest: role.policy.sha256,
    }]
  }))
  const actualReleaseSpec = { schema_version: 'chora.release-assets-spec.v1', release_id: 'o4-release', platform: { os: 'linux', architecture: 'arm64' }, roles: actualRoles, artifacts: actualArtifacts.map(({ image: _image, ...artifact }) => artifact) }
  const actualReleaseManifest = { schema_version: 'chora.release-assets-manifest.v1', spec_sha256: sha256(JSON.stringify(actualReleaseSpec)), release_id: 'o4-release', platform: { os: 'linux', architecture: 'arm64' }, roles: actualRoles, artifacts: actualArtifacts }
  const actualReleaseManifestFile = join(actualReleaseRoot, 'release-assets.json')
  const actualReleaseManifestSha256 = await writeJSON(actualReleaseManifestFile, actualReleaseManifest, 0o444)

  const releaseSpecFile = join(releaseRoot, 'release-spec.json')
  const releaseSpecSha256 = await writeJSON(releaseSpecFile, { schemaVersion: 'chora.m1-o4-release-spec.v1', generationId: identity.generationId, platform: identity.platform, roles: roleArray(r) })
  const releaseManifestFile = join(releaseRoot, 'release-manifest.json')
  const releaseManifestSha256 = await writeJSON(releaseManifestFile, { schemaVersion: 'chora.m1-o4-release-manifest.v1', generationId: identity.generationId, releaseSpecSha256, roles: roleArray(r) })
  const offlineBundleEvidenceFile = join(releaseRoot, 'offline-bundle-evidence.json')
  const offlineArtifacts = actualArtifacts.map((artifact) => {
    const boundRoles = actualRoles.filter((role) => role.artifact_id === artifact.id)
      .map((role) => role.role).sort()
    return {
      artifactId: artifact.id, roles: boundRoles,
      policyDigests: Object.fromEntries(boundRoles.map((role) => [role, r[role].policyDigest])),
      archive: { ...artifact.image.archive },
      expectedDockerConfigImageId: artifact.image.local_docker_config_image_id,
    }
  })
  const offlineBundleEvidenceSha256 = await writeJSON(offlineBundleEvidenceFile, {
    schemaVersion: 'chora.m1-o4-offline-release-bundle-evidence.v1',
    generationId: identity.generationId, releaseManifestSha256: actualReleaseManifestSha256,
    platform: { os: 'linux', architecture: 'arm64' }, artifacts: offlineArtifacts,
  })

  const piPackage = Buffer.from('{"name":"@earendil-works/pi-coding-agent","version":"0.84.2"}\n')
  const piExecutable = Buffer.from('#!/bin/sh\n')
  await writeFile(join(privatePiSourceRoot, 'package.json'), piPackage, { mode: 0o444 })
  await mkdir(join(privatePiSourceRoot, 'bin')); await writeFile(join(privatePiSourceRoot, 'bin/pi'), piExecutable, { mode: 0o555 })
  const piFiles = [
    { path: 'bin/pi', mode: 0o555, size: piExecutable.length, sha256: sha256(piExecutable) },
    { path: 'package.json', mode: 0o444, size: piPackage.length, sha256: sha256(piPackage) },
  ]
  const privatePiManifestFile = join(inputs, 'private-pi-manifest.json')
  const privatePiManifestSha256 = await writeJSON(privatePiManifestFile, { schema_version: 'chora.pi-private-asset.v1', platform: identity.platform, package_name: '@earendil-works/pi-coding-agent', version: '0.84.2', executable: 'bin/pi', files: piFiles, closure_sha256: privatePiClosure(piFiles) })

  const pathPiProvenanceFile = join(inputs, 'path-pi.json')
  const pathPiProvenanceSha256 = await writeJSON(pathPiProvenanceFile, { schemaVersion: 'chora.m1-o4-pi-provenance.v1', kind: 'path', generationId: identity.generationId, sourceIdentitySha256: d('path-source'), binarySha256: d('path-binary') })
  const privatePiProvenanceFile = join(inputs, 'private-pi.json')
  const privatePiProvenanceSha256 = await writeJSON(privatePiProvenanceFile, { schemaVersion: 'chora.m1-o4-pi-provenance.v1', kind: 'private', generationId: identity.generationId, sourceIdentitySha256: d('private-source'), binarySha256: d('private-binary') })
  const modelAuthorityFile = join(inputs, 'model.json')
  const modelAuthoritySha256 = await writeJSON(modelAuthorityFile, { schemaVersion: MODEL_AUTHORITY_SCHEMA, status: 'authorized', generationId: identity.generationId, providerIdentitySha256: d('provider'), modelIdentitySha256: d('model'), policySha256: d('model-policy'), proxyURLSha256: sha256(proxyURL), modelURLSha256: sha256(modelURL), authFileSha256, caFileSha256, authenticationRequired: true })
  await chmod(modelAuthorityFile, 0o400)
  const endpointEvidenceFile = join(inputs, 'endpoint.json'); const dockerContext = 'colima-chora-o4'; const endpointDigest = d('endpoint')
  const endpointEvidenceSha256 = await writeJSON(endpointEvidenceFile, { schemaVersion: 'chora.m1-o4-engine-endpoint-evidence.v1', status: 'passed', generationId: identity.generationId, platform: identity.platform, contextIdentitySha256: sha256(dockerContext), endpointIdentitySha256: endpointDigest })
  const evidenceRoot = join(root, 'evidence'); await mkdir(evidenceRoot)
  const qualificationFile = join(evidenceRoot, 'engine-qualification.json')
  const modelObservationFile = join(evidenceRoot, 'model-observation.json')
  const installedDoctorReportFile = join(evidenceRoot, 'installed-doctor-report.json')
  const preflightFile = join(inputs, 'preflight.json')
  const endpointSocketPath = join(root, 'engine', 'docker.sock')
  const limaPrefixClosureSha256 = d('lima-prefix-closure')
  const preflightAuthority = buildFreshEnginePreflightAuthority({
    generationId: identity.generationId,
    preMutationPaths: [join(root, 'engine')],
    engine: {
      colimaSha256: colimaSourceSha256, limaSha256: d('lima'),
      limaPrefixClosureSha256, dockerSha256: dockerCLISha256,
      diskImageSize: 1024, diskImageSha256: d('disk'), profileName: 'chora-o4',
      contextName: dockerContext, endpointSocketPath, endpointDigest,
      sandboxExecutableSha256: d('sandbox-executable'),
      sandboxProfileSha256: d('sandbox-profile'),
    },
    limaInfo: {
      templates: Array.from({ length: 120 }, (_, index) => ({
        location: join(root, 'lima', 'templates', `template-${index}.yaml`),
      })),
      guestAgents: { aarch64: { location: join(root, 'lima', 'guest-agent') } },
      hostOS: 'darwin', hostArch: 'aarch64',
    },
    docker: { daemonId: 'private-daemon-id', apiVersion: '1.47',
      serverVersion: '29.6.1', operatingSystem: 'linux', architecture: 'arm64',
      providerName: 'Docker' },
    limaInstance: { name: dockerContext, status: 'Running', vmType: 'vz',
      hostAgentPID: 4242, driverPID: 4242 },
    processes: [
      { role: 'host_agent', pid: 4242, pidFileSha256: d('ha-pid') },
      { role: 'vz_driver', pid: 4242, pidFileSha256: d('vz-pid') },
    ],
    sandbox: { probeDigest: d('sandbox-probe') },
    bootstrap: { argv: ['start'], environment: { PATH: '/private/empty' },
      stdout: Buffer.from('started'), stderr: Buffer.alloc(0) },
    observedAt: '2026-08-30T00:00:00.000Z',
  })
  await writeJSON(preflightFile, preflightAuthority)
  await chmod(preflightFile, 0o400)

  const candidateManifestFile = join(inputs, 'candidate.json')
  const candidateManifestSha256 = await writeJSON(candidateManifestFile, {
    schemaVersion: 'chora.m1-o4-candidate-manifest.v1', ...identity,
    sourceManifestSha256, sourceAggregateSha256: sourceAggregate, binarySha256, webAggregateSha256,
    releaseSpecSha256, releaseManifestSha256, offlineBundleEvidenceSha256,
    repositoryCommit: repositoryAuthority.repositoryCommit,
    repositoryClosureSha256: repositoryAuthority.repositoryClosureSha256,
  })
  const installRoot = join(root, 'installed'); const dataRoot = join(root, 'data'); const stateRoot = join(root, 'state'); const receiptDirectory = join(root, 'receipts')
  const config = {
    identity: structuredClone(identity),
    paths: {
      sourceBundleRoot, sourceManifestFile, candidateManifestFile, binaryFile, webDirectory,
      releaseSpecFile, releaseManifestFile, offlineBundleEvidenceFile, pathPiProvenanceFile,
      privatePiProvenanceFile, modelAuthorityFile, qualificationFile, modelObservationFile,
      installedDoctorReportFile, endpointEvidenceFile,
      preflightFile, dockerCLIFile, installRoot, installedBinaryFile: join(installRoot, 'bin', 'chora'),
      installedWebDirectory: join(installRoot, 'build', 'web-workspace', 'web', 'dist'),
      installedSourceRoot: installRoot, installedSourceManifestFile: join(installRoot, 'source-manifest.json'),
      dataRoot, databaseFile: join(dataRoot, 'chora.db'), repositoryRoot, authFile, caFile,
      stateRoot, ledgerFile: join(stateRoot, 'docker-ledger.json'), receiptDirectory,
      privatePiRoot: join(root, 'private-pi'), privatePiSourceRoot, privatePiManifestFile,
      actualReleaseRoot, actualReleaseManifestFile, probeRuntimeRoot, colimaToolRoot,
      colimaSourceFile, dockerClientSourceFile,
      tupleFile: join(receiptDirectory, 'candidate-tuple.json'),
    },
    authority: {
      sourceManifestSha256, sourceAggregateSha256: sourceAggregate, candidateManifestSha256, binarySha256,
      webAggregateSha256, releaseSpecSha256, releaseManifestSha256, offlineBundleEvidenceSha256,
      pathPiProvenanceSha256, privatePiProvenanceSha256, modelAuthoritySha256,
      endpointEvidenceSha256, dockerCLISha256, limaSha256: d('lima'),
      limaPrefixClosureSha256, limaInfoDigest: preflightAuthority.limaInfoDigest,
      diskImageSha256: d('disk'), diskImageSize: 1024,
      endpointSocketPathSha256: sha256(endpointSocketPath),
      sandboxExecutableSha256: d('sandbox-executable'), sandboxProfileSha256: d('sandbox-profile'),
      actualReleaseManifestSha256,
      privatePiManifestSha256, authFileSha256, caFileSha256, colimaSourceSha256, dockerClientSourceSha256,
      repositoryCommit: repositoryAuthority.repositoryCommit,
      repositoryClosureSha256: repositoryAuthority.repositoryClosureSha256,
      dockerContext, endpointDigest, roles: r,
    },
    private: { proxyURL, modelURL, port: 8787, colimaProfile: 'chora-o4' },
    setup: { key: 'o4-setup', grant: 'o4-grant', actor: 'o4-actor', session: 'o4-session', releaseId: 'o4-release', releaseManifestRelativePath: basename(actualReleaseManifestFile) },
  }
  return { root, config }
}

async function expandRepositorySnapshotPastTupleBound(config) {
  const count = 4_500
  const suffix = 'x'.repeat(190)
  for (let offset = 0; offset < count; offset += 100) {
    await Promise.all(Array.from({ length: Math.min(100, count - offset) }, (_, index) =>
      writeFile(join(config.paths.repositoryRoot,
        `oversized-${String(offset + index).padStart(5, '0')}-${suffix}`), '', { mode: 0o644 })))
  }
  await execFileAsync('/usr/bin/git', ['-C', config.paths.repositoryRoot, 'add', '--all'])
  await execFileAsync('/usr/bin/git', ['-C', config.paths.repositoryRoot,
    '-c', 'user.name=O4 Fixture', '-c', 'user.email=chora@example.test',
    'commit', '-qm', 'oversized snapshot'])
}

function doctorResult(config, status = 'passed') {
  return { schema_version: 'chora.product-command-result/v1', status, action: 'doctor', context_name: config.authority.dockerContext, endpoint_digest: config.authority.endpointDigest, daemon_id: 'private-daemon-id', api_version: '1.47', operating_system: 'linux', architecture: 'arm64' }
}

function installedProof(config, overrides = {}) {
  const a = config.authority; const roles = a.roles
  const engine_identity = { schema_version: 'chora.docker-engine-identity/v1', daemon_id: 'private-daemon-id',
    api_version: '1.47', operating_system: 'linux', architecture: 'arm64',
    context_endpoint_digest: a.endpointDigest, provider_name: 'Docker', engine_version: '29.6.1',
    context_name: a.dockerContext, identity_digest: d('installed-engine') }
  const capability_contract = { schema_version: 'chora.docker-capability-probe-contract/v1', contract_version: 1,
    minimum_api_version: '1.44', operating_system: 'linux', architecture: 'arm64',
    probe_image_id: roles.capability_probe.dockerConfigImageId,
    sandbox_policy_digest: roles.capability_probe.policyDigest,
    capabilities: ['exact-preexisting-image', 'zero-residue'], contract_digest: d('capability-contract') }
  return { schema_version: 'chora.installed-product-proof/v1', status: 'passed',
    generation_id: config.identity.generationId, release_id: config.setup.releaseId,
    manifest_sha256: a.actualReleaseManifestSha256, setup_receipt_sha256: overrides.setupReceiptSha256,
    model_request_authority_sha256: a.modelAuthoritySha256, docker_operations_read_only: true,
    engine_mutations_attempted: 0, resources_created: false, model_authenticated: true,
    input_binding: { auth_file_sha256: a.authFileSha256, ca_file_sha256: a.caFileSha256,
      proxy_url_sha256: sha256(config.private.proxyURL), model_url_sha256: sha256(config.private.modelURL) },
    engine_identity, capability_contract,
    engine_qualification: { schema_version: 'chora.docker-engine-qualification/v1',
      engine_identity_digest: engine_identity.identity_digest,
      probe_contract_digest: capability_contract.contract_digest,
      probe_image_id: capability_contract.probe_image_id,
      sandbox_policy_digest: capability_contract.sandbox_policy_digest,
      completed_at: '2026-08-30T00:00:00Z', qualification_digest: d('engine-qualification') },
    role_images: Object.entries(roles).map(([role, binding]) => ({ role, artifact_id: binding.artifactId,
      config_image_id: binding.dockerConfigImageId, archive_sha256: binding.archiveSha256,
      archive_size: binding.archiveSize, policy_sha256: binding.policyDigest })),
    doctor: { status: 'passed', resourcesCreated: false, elapsed: 1,
      inputFingerprint: d('installed-doctor-fingerprint'),
      modelProbe: { maxAttempts: 3, attempts: [{ attempt: 1, outcome: 'authenticated_schema',
        statusCode: 400, retryable: false, retried: false }] },
      inputEvidence: { authFileSha256: a.authFileSha256, caFileSha256: a.caFileSha256,
        proxyUrlSha256: sha256(config.private.proxyURL), modelUrlSha256: sha256(config.private.modelURL) } },
    ...overrides.proof }
}

function fakeRunner(config, options = {}) {
  const events = []
  let productDoctors = 0
  return {
    events,
    async exec(spec) {
      events.push({ kind: 'exec', file: spec.file, argv: [...spec.argv], shell: spec.shell })
      if (spec.argv[0] === 'product' && spec.argv[1] === 'doctor') {
        productDoctors++
        const status = options.stopped && productDoctors === 1 ? 'failed' : 'passed'
        const observation = { ...doctorResult(config, status), ...(productDoctors === 1 ? options.engineDrift : {}) }
        return { exitCode: status === 'passed' ? 0 : 1, stdout: `${JSON.stringify(observation)}\n`, stderr: options.secretError ?? '' }
      }
      if (spec.argv[0] === 'setup') return { exitCode: 0, stdout: '{"status":"passed","action":"setup"}\n', stderr: '' }
      if (spec.argv[0] === 'installed-doctor') {
        const setupIndex = spec.argv.indexOf('--setup-receipt-sha256')
        const output = installedProof(config, { setupReceiptSha256: spec.argv[setupIndex + 1], proof: options.installedProof })
        return { exitCode: 0, stdout: `${JSON.stringify(output)}\n`, stderr: '' }
      }
      throw new Error('unexpected command')
    },
    async installImmutable(spec) {
      events.push({ kind: 'install', spec: structuredClone(spec), shell: spec.shell })
      let installCreated = false
      let dataCreated = false
      try {
        await mkdir(spec.installRoot, { mode: 0o700 }); installCreated = true
        await mkdir(dirname(spec.installedBinaryFile), { recursive: true })
        await mkdir(spec.installedWebDirectory, { recursive: true })
        await mkdir(join(spec.installRoot, 'source'), { mode: 0o755 })
        await copyFile(spec.sourceBinary, spec.installedBinaryFile)
        await copyFile(join(spec.sourceWebDirectory, 'index.html'), join(spec.installedWebDirectory, 'index.html'))
        await copyFile(spec.sourceManifestFile, join(spec.installRoot, 'source-manifest.json'))
        await chmod(join(spec.installRoot, 'source-manifest.json'), 0o444)
        await copyFile(join(spec.sourceBundleRoot, 'source', 'README.md'), join(spec.installRoot, 'source', 'README.md'))
        await writeFile(join(spec.installRoot, 'installation.json'), `${JSON.stringify({
          schema_version: 'chora.local-alpha-installation.v1',
          aggregate_sha256: spec.sourceAggregateSha256,
          files: spec.sourceFiles,
        }, null, 2)}\n`, { mode: 0o444, flag: 'wx' })
        await chmod(join(spec.installRoot, 'installation.json'), 0o444)
        await mkdir(spec.dataRoot, { mode: 0o700 }); dataCreated = true
        await writeFile(join(spec.dataRoot, 'data-installation.json'), `${JSON.stringify({
          schema_version: 'chora.local-alpha-data.v1',
          aggregate_sha256: spec.sourceAggregateSha256,
          install_root: spec.installRoot,
        }, null, 2)}\n`, { mode: 0o444, flag: 'wx' })
        await chmod(join(spec.dataRoot, 'data-installation.json'), 0o444)
        if (options.installFailure === true) throw new Error('synthetic immutable install failure')
      } catch (error) {
        if (dataCreated) await rm(spec.dataRoot, { recursive: true, force: true })
        if (installCreated) await rm(spec.installRoot, { recursive: true, force: true })
        throw error
      }
    },
    async processBoundary() {
      events.push({ kind: 'boundary' })
      if (options.boundaryMutation) await options.boundaryMutation()
    },
    async start(spec) { events.push({ kind: 'start', file: spec.file, argv: [...spec.argv], shell: spec.shell }); return { active: true, close: async () => {} } },
  }
}

function o4EvidenceAuthority(config, tupleIdentity) {
  const root = dirname(config.paths.dataRoot)
  return {
    sessionAuthorityFile: join(root, 'runner-session.json'),
    sessionAuthoritySha256: d('runner-session'),
    tupleIdentity,
    sourceA3LedgerFile: join(root, 'evidence', 'source-a3.json'),
    verifierLedgerFile: join(root, 'evidence', 'verifier.json'),
    cResourceDirectory: join(root, 'evidence-c'),
    dResourceDirectory: join(root, 'evidence-d'),
  }
}

test('freezes a canonical full-authority tuple with exact immutable identity', async () => {
  const { config } = await fixture()
  const first = await inspectAuthenticatedCandidate(config); const second = await inspectAuthenticatedCandidate(structuredClone(config))
  assert.equal(first.tupleIdentity, second.tupleIdentity)
  assert.equal(first.tuple.schemaVersion, CANDIDATE_TUPLE_SCHEMA)
  assert.deepEqual(first.tuple.repositoryAuthority, first.repositoryAuthority.snapshot)
  assert.ok(Buffer.byteLength(JSON.stringify(first.tuple.repositoryAuthority)) < 1024 * 1024)
  assert.equal(first.tuple.identity, sha256(canonicalJSONStringify(Object.fromEntries(Object.entries(first.tuple).filter(([key]) => key !== 'identity')))))
  assert.deepEqual(first.tuple.plan, ['offline-prebuilt-install', 'product-doctor', 'setup-once', 'installed-doctor', 'process-boundary', 'active-serve'])
})

test('tuple v2 validator rejects stale schema and rehashed repository/roots/bindings/plan drift', async () => {
  const { config } = await fixture()
  const { tuple } = await inspectAuthenticatedCandidate(config)
  assert.equal(validateCandidateTuple(structuredClone(tuple)).identity, tuple.identity)
  for (const [name, mutate, message] of [
    ['stale v1', (value) => { value.schemaVersion = 'chora.m1-o4-candidate-install-tuple.v1' },
      /schema\/status drifted/],
    ['repository closure', (value) => {
      value.repositoryAuthority.repositoryClosureSha256 = d('spliced-repository-closure')
      value.bindings.repositoryClosureSha256 = value.repositoryAuthority.repositoryClosureSha256
    }, /repository authority snapshot digest drifted/],
    ['partial roots', (value) => { delete value.roots.stateRootSha256 }, /roots fields drifted/],
    ['partial bindings', (value) => { delete value.bindings.roleTupleSha256 }, /bindings fields drifted/],
    ['reordered plan', (value) => {
      [value.plan[0], value.plan[1]] = [value.plan[1], value.plan[0]]
    }, /plan\/order drifted/],
  ]) {
    const value = structuredClone(tuple)
    mutate(value)
    delete value.identity
    value.identity = sha256(canonicalJSONStringify(value))
    assert.throws(() => validateCandidateTuple(value), message, name)
  }
})

test('Engine preflight requires the exact host-agent and in-process VZ-driver authority', () => {
  const pid = 4242
  const limaInstance = {
    name: 'colima-chora-o4', status: 'Running', vmType: 'vz',
    hostAgentPID: pid, driverPID: pid,
  }
  const processes = [
    { role: 'host_agent', pid, pidFileSha256: d('ha-pid') },
    { role: 'vz_driver', pid, pidFileSha256: d('vz-pid') },
  ]
  assert.deepEqual(validateEngineProcessAuthority(processes, limaInstance), processes)
  for (const invalid of [
    [],
    processes.slice(0, 1),
    [processes[0], processes[0]],
    [processes[0], { ...processes[1], role: 'virtual_machine' }],
    [processes[0], { ...processes[1], pid: pid + 1 }],
  ]) assert.throws(() => validateEngineProcessAuthority(invalid, limaInstance),
    /Engine preflight process authority is invalid/)
})

test('authenticated inspection consumes the real v3 producer shape and binds both Lima authorities', async (t) => {
  const accepted = await fixture()
  const produced = JSON.parse(await readFile(accepted.config.paths.preflightFile, 'utf8'))
  assert.equal(produced.limaPrefixClosureSha256,
    accepted.config.authority.limaPrefixClosureSha256)
  assert.equal(produced.limaInfoDigest, accepted.config.authority.limaInfoDigest)
  assert.match(produced.limaPrefixClosureSha256, /^[0-9a-f]{64}$/)
  assert.match(produced.limaInfoDigest, /^[0-9a-f]{64}$/)
  await inspectAuthenticatedCandidate(accepted.config)

  for (const field of ['limaPrefixClosureSha256', 'limaInfoDigest']) await t.test(field, async () => {
    const { config } = await fixture()
    const value = JSON.parse(await readFile(config.paths.preflightFile, 'utf8'))
    value[field] = d(`${field}-drift`)
    delete value.digest
    value.digest = sha256(canonicalJSONStringify(value))
    await chmod(config.paths.preflightFile, 0o600)
    await writeJSON(config.paths.preflightFile, value)
    await chmod(config.paths.preflightFile, 0o400)
    await assert.rejects(inspectAuthenticatedCandidate(config), /Engine preflight identity drifted/)
  })
})

test('static pre-bootstrap inspection has no dynamic preflight self-reference', async () => {
  const { config } = await fixture()
  assert.equal('preflightSha256' in config.authority, false)
  await unlink(config.paths.preflightFile)
  const inspected = await inspectStaticCandidate(config)
  assert.deepEqual(inspected.identity, config.identity)
  await writeFile(config.paths.qualificationFile, '{}\n', { mode: 0o400 })
  await assert.rejects(inspectStaticCandidate(config), /pre-Setup dynamic evidence must be absent/)
})

test('candidate repository capture capability is mandatory and exact before mutation', async (t) => {
  const invalid = [
    ['omitted', undefined],
    ['extra key', { executableFile: '/bin/false', executableSha256: 'a'.repeat(64), extra: true }],
    ['relative executable', { executableFile: 'capture', executableSha256: 'a'.repeat(64) }],
    ['invalid digest', { executableFile: '/bin/false', executableSha256: 'not-a-digest' }],
  ]
  for (const [name, repositoryCapture] of invalid) await t.test(name, async () => {
    const { config } = await fixture()
    const options = repositoryCapture === undefined ? undefined : { repositoryCapture }
    await assert.rejects(inspectStaticCandidateRaw(config, options), /repository capture/)
    await assert.rejects(inspectAuthenticatedCandidateRaw(config, options), /repository capture/)
    const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstallRaw(config, { runner, repositoryCapture }),
      /repository capture/)
    await assert.rejects(restartCandidateRaw(config, { runner, repositoryCapture }),
      /repository capture/)
    await assert.rejects(appendHandoffReceiptRaw({
      tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory,
      phase: 'C', operationDigest: d('missing-capture-operation'),
      observationDigest: d('missing-capture-observation'),
    }, { repositoryCapture }), /repository capture/)
    await assert.rejects(buildHandoffRaw(
      config.paths.tupleFile, config.paths.receiptDirectory, { repositoryCapture }),
    /repository capture/)
    await assert.rejects(finalizeEvidencePhaseERaw({}, { repositoryCapture }),
      /repository capture/)
    await assert.rejects(buildO4FaultAuthorityRaw({
      config, authorityPath: join(config.paths.dataRoot, 'missing-capture.json'), scenarioBindings,
    }, { repositoryCapture }), /repository capture/)
    assert.deepEqual(runner.events, [])
    await assert.rejects(stat(config.paths.installRoot), /ENOENT/)
    await assert.rejects(stat(config.paths.dataRoot), /ENOENT/)
  })
})

test('private Pi manifest accepts the observed 13,181 files within the preparer 15,000-file maximum', () => {
  const files = [
    { path: 'bin/pi', mode: 0o555, size: 1, sha256: d('pi-bin') },
    ...Array.from({ length: 13_179 }, (_, index) => ({
      path: `node_modules/pkg-${String(index).padStart(5, '0')}.js`, mode: 0o444, size: index,
      sha256: d(`pi-file-${index}`),
    })),
    { path: 'package.json', mode: 0o444, size: 1, sha256: d('pi-package') },
  ]
  const manifest = { schema_version: 'chora.pi-private-asset.v1', platform: identity.platform,
    package_name: '@earendil-works/pi-coding-agent', version: '0.84.2', executable: 'bin/pi', files,
    closure_sha256: privatePiClosure(files) }
  assert.equal(files.length, 13_181)
  validatePrivatePiManifest(manifest, { identity })
  const oversized = structuredClone(manifest)
  oversized.files = Array.from({ length: 15_001 }, (_, index) => ({
    path: `f/${String(index).padStart(5, '0')}`, mode: 0o444, size: 0, sha256: d(`oversized-${index}`),
  }))
  assert.throws(() => validatePrivatePiManifest(oversized, { identity }), /private Pi files invalid/)
})

test('Runner authenticates the present repository binding and rejects omission or candidate splice', async (t) => {
  await t.test('present', async (t) => {
    const root = await realpath(await mkdtemp(join(tmpdir(), 'chora-o4-runner-repository-present-')))
    t.after(() => rm(root, { recursive: true, force: true }))
    const runner = await syntheticNonAcceptanceInstallRunner(root)
    assert.equal(typeof runner.installImmutable, 'function')
  })
  await t.test('missing', async (t) => {
    const root = await realpath(await mkdtemp(join(tmpdir(), 'chora-o4-runner-repository-missing-')))
    t.after(() => rm(root, { recursive: true, force: true }))
    await assert.rejects(syntheticNonAcceptanceInstallRunner(root, {
      manifest: (manifest) => { delete manifest.repository },
    }), /total manifest keys drifted/)
  })
  await t.test('spliced', async (t) => {
    const root = await realpath(await mkdtemp(join(tmpdir(), 'chora-o4-runner-repository-spliced-')))
    t.after(() => rm(root, { recursive: true, force: true }))
    await assert.rejects(syntheticNonAcceptanceInstallRunner(root, {
      candidateConfig: (config) => { config.authority.repositoryCommit = 'b'.repeat(40) },
    }), /candidate\/total repository authority drifted/)
  })
})

test('synthetic non-acceptance Runner creates and cleans the exact immutable install/data topology', async () => {
  const { root, config } = await fixture()
  const runner = await syntheticNonAcceptanceInstallRunner(root)
  const sourceManifest = JSON.parse(await readFile(config.paths.sourceManifestFile, 'utf8'))
  const installSpecAt = (installRoot, dataRoot) => ({
    sourceBundleRoot: config.paths.sourceBundleRoot,
    sourceManifestFile: config.paths.sourceManifestFile,
    sourceManifestSha256: config.authority.sourceManifestSha256,
    sourceAggregateSha256: config.authority.sourceAggregateSha256,
    sourceFiles: sourceManifest.files.length,
    sourceBinary: config.paths.binaryFile,
    sourceWebDirectory: config.paths.webDirectory,
    installRoot,
    installedBinaryFile: join(installRoot, 'bin', 'chora'),
    installedWebDirectory: join(installRoot, 'build', 'web-workspace', 'web', 'dist'),
    dataRoot,
    shell: false,
  })
  const spec = installSpecAt(config.paths.installRoot, config.paths.dataRoot)
  await runner.installImmutable(spec)

  assert.equal((await stat(spec.installRoot)).mode & 0o777, 0o700)
  assert.equal((await stat(spec.dataRoot)).mode & 0o777, 0o700)
  assert.equal((await stat(join(spec.installRoot, 'source'))).mode & 0o777, 0o755)
  assert.equal((await stat(join(spec.installRoot, 'source-manifest.json'))).mode & 0o777, 0o444)
  assert.equal((await stat(join(spec.installRoot, 'installation.json'))).mode & 0o777, 0o444)
  assert.equal((await stat(join(spec.dataRoot, 'data-installation.json'))).mode & 0o777, 0o444)
  assert.deepEqual(JSON.parse(await readFile(join(spec.installRoot, 'installation.json'), 'utf8')), {
    schema_version: 'chora.local-alpha-installation.v1',
    aggregate_sha256: spec.sourceAggregateSha256,
    files: spec.sourceFiles,
  })
  assert.deepEqual(JSON.parse(await readFile(join(spec.dataRoot, 'data-installation.json'), 'utf8')), {
    schema_version: 'chora.local-alpha-data.v1',
    aggregate_sha256: spec.sourceAggregateSha256,
    install_root: spec.installRoot,
  })
  assert.deepEqual(await readdir(spec.dataRoot), ['data-installation.json'])
  assert.deepEqual(await readFile(join(spec.installRoot, 'source-manifest.json')),
    await readFile(spec.sourceManifestFile))
  assert.equal(await readFile(join(spec.installRoot, 'source', 'README.md'), 'utf8'),
    await readFile(join(spec.sourceBundleRoot, 'source', 'README.md'), 'utf8'))

  const failedInstallRoot = join(root, 'failed-installed')
  const failedDataRoot = join(root, 'failed-data')
  const failedSpec = installSpecAt(failedInstallRoot, failedDataRoot)
  const unrelated = join(root, 'unrelated-sentinel')
  await writeFile(unrelated, 'preserve\n', { mode: 0o444, flag: 'wx' })
  const hook = Symbol.for('chora.o4.syntheticPinReadHook')
  globalThis[hook] = async (path, label) => {
    if (label === 'data installation identity' &&
      path === join(failedDataRoot, 'data-installation.json')) await chmod(path, 0o600)
  }
  try {
    await assert.rejects(runner.installImmutable(failedSpec), /data installation identity path identity changed/)
  } finally {
    delete globalThis[hook]
  }
  await assert.rejects(stat(failedInstallRoot), /ENOENT/)
  await assert.rejects(stat(failedDataRoot), /ENOENT/)
  assert.equal(await readFile(unrelated, 'utf8'), 'preserve\n')

  const preexistingDataRoot = join(root, 'preexisting-data')
  await mkdir(preexistingDataRoot, { mode: 0o700 })
  const preexistingSentinel = join(preexistingDataRoot, 'operator-owned')
  await writeFile(preexistingSentinel, 'keep\n', { mode: 0o600, flag: 'wx' })
  const rejectedInstallRoot = join(root, 'rejected-installed')
  await assert.rejects(runner.installImmutable(
    installSpecAt(rejectedInstallRoot, preexistingDataRoot)), /fresh data root already exists/)
  await assert.rejects(stat(rejectedInstallRoot), /ENOENT/)
  assert.equal(await readFile(preexistingSentinel, 'utf8'), 'keep\n')

  const overlappingInstallRoot = join(root, 'overlapping-installed')
  await assert.rejects(runner.installImmutable(
    installSpecAt(overlappingInstallRoot, join(overlappingInstallRoot, 'data'))), /must be disjoint/)
  await assert.rejects(stat(overlappingInstallRoot), /ENOENT/)

  const wrongManifestInstallRoot = join(root, 'wrong-manifest-installed')
  const wrongManifestDataRoot = join(root, 'wrong-manifest-data')
  await assert.rejects(runner.installImmutable({
    ...installSpecAt(wrongManifestInstallRoot, wrongManifestDataRoot),
    sourceManifestSha256: d('wrong-source-manifest'),
  }), /source manifest digest drifted/)
  await assert.rejects(stat(wrongManifestInstallRoot), /ENOENT/)
  await assert.rejects(stat(wrongManifestDataRoot), /ENOENT/)

  const badTargetInstallRoot = join(root, 'bad-target-installed')
  const badTargetSpec = installSpecAt(badTargetInstallRoot, join(root, 'bad-target-data'))
  badTargetSpec.installedBinaryFile = join(badTargetInstallRoot, 'source', 'chora')
  await assert.rejects(runner.installImmutable(badTargetSpec),
    /target overlaps installed source identity/)
  await assert.rejects(stat(badTargetSpec.installRoot), /ENOENT/)
  await assert.rejects(stat(badTargetSpec.dataRoot), /ENOENT/)
})

test('orchestrator rejects any installed source root or manifest topology drift before Runner mutation', async () => {
  for (const mutate of [
    (config) => { config.paths.installedSourceRoot = join(config.paths.installRoot, 'source') },
    (config) => { config.paths.installedSourceManifestFile = join(config.paths.installRoot, 'source', 'source-manifest.json') },
  ]) {
    const { config } = await fixture()
    mutate(config)
    const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstall(config, { runner }),
      /installed source (?:root must be the exact install root|manifest topology drifted)/)
    assert.deepEqual(runner.events, [])
    await assert.rejects(stat(config.paths.installRoot), /ENOENT/)
    await assert.rejects(stat(config.paths.dataRoot), /ENOENT/)
  }
})

test('runs exact public plan with shell=false and exactly one Setup', async () => {
  const { config } = await fixture(); const runner = fakeRunner(config)
  const result = await executeCandidateInstall(config, { runner })
  assert.deepEqual(runner.events.map((event) => event.kind === 'exec' ? event.argv.slice(0, 2).join(' ') : event.kind), [
    'product doctor', 'install', 'product doctor', 'setup --docker-cli', 'installed-doctor --state-root', 'boundary', 'start',
  ])
  assert.equal(runner.events.filter((event) => event.kind === 'exec' && event.argv[0] === 'setup').length, 1)
  assert.equal(runner.events.every((event) => event.shell !== true), true)
  const setup = runner.events.find((event) => event.kind === 'exec' && event.argv[0] === 'setup').argv
  assert.equal(setup.includes('--bootstrap-colima'), false)
  assert.equal(setup.some((item) => ['npm', 'go', 'build', 'pull', 'load'].includes(item)), false)
  assert.equal(result.handoff.rawA3IsB1Composite, false)
  assert.match(result.handoff.downstreamSourceManifestFact, /validator repair is required/)

  const p = config.paths; const a = config.authority; const secret = config.private
  const installEvent = runner.events.find((event) => event.kind === 'install')
  assert.deepEqual(Object.keys(installEvent.spec).sort(), [
    'sourceBundleRoot', 'sourceManifestFile', 'sourceManifestSha256',
    'sourceAggregateSha256', 'sourceFiles', 'sourceBinary', 'sourceWebDirectory',
    'installRoot', 'installedBinaryFile', 'installedWebDirectory', 'dataRoot', 'shell',
  ].sort())
  assert.equal(installEvent.spec.sourceManifestSha256, a.sourceManifestSha256)
  assert.equal(installEvent.spec.sourceAggregateSha256, a.sourceAggregateSha256)
  assert.equal(installEvent.spec.sourceFiles, 1)
  assert.equal(installEvent.spec.installRoot, p.installRoot)
  assert.equal(installEvent.spec.dataRoot, p.dataRoot)
  assert.equal(p.installedSourceRoot, p.installRoot)
  assert.equal(p.installedSourceManifestFile, join(p.installRoot, 'source-manifest.json'))
  assert.equal((await stat(p.installRoot)).mode & 0o777, 0o700)
  assert.equal((await stat(p.dataRoot)).mode & 0o777, 0o700)
  assert.equal((await stat(p.installedSourceManifestFile)).mode & 0o777, 0o444)
  assert.equal((await stat(join(p.installRoot, 'installation.json'))).mode & 0o777, 0o444)
  assert.equal((await stat(join(p.dataRoot, 'data-installation.json'))).mode & 0o777, 0o444)
  assert.deepEqual(JSON.parse(await readFile(join(p.installRoot, 'installation.json'), 'utf8')), {
    schema_version: 'chora.local-alpha-installation.v1',
    aggregate_sha256: a.sourceAggregateSha256,
    files: 1,
  })
  assert.deepEqual(JSON.parse(await readFile(join(p.dataRoot, 'data-installation.json'), 'utf8')), {
    schema_version: 'chora.local-alpha-data.v1',
    aggregate_sha256: a.sourceAggregateSha256,
    install_root: p.installRoot,
  })
  const installReceipt = JSON.parse(await readFile(join(p.receiptDirectory, '02-install.json'), 'utf8'))
  assert.equal(installReceipt.operationDigest, sha256(canonicalJSONStringify({
    sourceManifestFileSha256: sha256(installEvent.spec.sourceManifestFile),
    sourceManifestSha256: a.sourceManifestSha256,
    sourceAggregateSha256: a.sourceAggregateSha256,
    sourceFiles: 1,
    sourceBinarySha256: sha256(installEvent.spec.sourceBinary),
    sourceWebDirectorySha256: sha256(installEvent.spec.sourceWebDirectory),
    installRootSha256: sha256(p.installRoot),
    dataRootSha256: sha256(p.dataRoot),
    shell: false,
  })))
  const executions = runner.events.filter((event) => event.kind === 'exec')
  assert.deepEqual(executions[0].argv, ['product', 'doctor', '--docker-cli', p.dockerCLIFile, '--docker-context', a.dockerContext, '--endpoint-digest', a.endpointDigest, '--json'])
  assert.deepEqual(executions[1].argv, ['product', 'doctor', '--docker-cli', p.dockerCLIFile, '--docker-context', a.dockerContext, '--endpoint-digest', a.endpointDigest, '--json'])
  assert.deepEqual(executions[2].argv, ['setup', '--docker-cli', p.dockerCLIFile, '--docker-context', a.dockerContext, '--endpoint-digest', a.endpointDigest, '--state-root', p.stateRoot, '--key', config.setup.key, '--grant', config.setup.grant, '--actor', config.setup.actor, '--session', config.setup.session, '--authorize', 'setup', '--release-root', p.actualReleaseRoot, '--release-manifest', config.setup.releaseManifestRelativePath, '--manifest-sha256', a.actualReleaseManifestSha256, '--release-id', config.setup.releaseId, '--generation', config.identity.generationId, '--probe-runtime-root', p.probeRuntimeRoot, '--private-pi-root', p.privatePiRoot, '--private-pi-source', p.privatePiSourceRoot, '--private-pi-manifest', p.privatePiManifestFile, '--private-pi-manifest-sha256', a.privatePiManifestSha256, '--json'])
  const installedDoctor = executions[3].argv
  assert.equal(installedDoctor[0], 'installed-doctor')
  assert.equal(installedDoctor[installedDoctor.indexOf('--source') + 1], p.installRoot)
  assert.equal(installedDoctor[installedDoctor.indexOf('--source-manifest') + 1],
    p.installedSourceManifestFile)
  assert.equal(installedDoctor[installedDoctor.indexOf('--install') + 1], p.installRoot)
  assert.equal(installedDoctor[installedDoctor.indexOf('--data') + 1], p.dataRoot)
  assert.equal(installedDoctor[installedDoctor.indexOf('--setup-receipt') + 1], join(p.receiptDirectory, '04-setup.json'))
  assert.equal(installedDoctor[installedDoctor.indexOf('--model-request-authority') + 1], p.modelAuthorityFile)
  assert.equal((await stat(join(p.receiptDirectory, '04-setup.json'))).mode & 0o777, 0o400)
  const serve = runner.events.find((event) => event.kind === 'start').argv
  assert.equal(serve[serve.indexOf('--source') + 1], p.installRoot)
  assert.equal(serve[serve.indexOf('--source-manifest') + 1], p.installedSourceManifestFile)
  assert.equal(serve[serve.indexOf('--install') + 1], p.installRoot)
  assert.equal(serve[serve.indexOf('--data') + 1], p.dataRoot)
  assert.equal(serve[serve.indexOf('--preflight-fingerprint') + 1], d('installed-doctor-fingerprint'))
})

test('final repository reinspection runs after processBoundary and prevents first serve start', async () => {
  const { config } = await fixture()
  const runner = fakeRunner(config, { boundaryMutation: () =>
    writeFile(join(config.paths.repositoryRoot, 'README.md'), 'mutated at boundary\n') })
  await assert.rejects(executeCandidateInstall(config, { runner }), /tracked repository bytes drifted/)
  assert.equal(runner.events.filter((event) => event.kind === 'boundary').length, 1)
  assert.equal(runner.events.filter((event) => event.kind === 'start').length, 0)
})

test('candidate config and candidate manifest reject repository authority schema splices', async () => {
  {
    const { config } = await fixture(); config.authority.repositoryCommit = '0'.repeat(40)
    const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstall(config, { runner }), /expected authority/)
    assert.deepEqual(runner.events, [])
  }
  {
    const { config } = await fixture()
    const candidate = JSON.parse(await readFile(config.paths.candidateManifestFile, 'utf8'))
    candidate.repositoryClosureSha256 = d('spliced-repository')
    await chmod(config.paths.candidateManifestFile, 0o600)
    config.authority.candidateManifestSha256 = await writeJSON(config.paths.candidateManifestFile, candidate)
    await chmod(config.paths.candidateManifestFile, 0o400)
    const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstall(config, { runner }), /candidate repository authority drifted/)
    assert.deepEqual(runner.events, [])
  }
})

test('generated oversized initial repository snapshot fails before tuple publication and start', async () => {
  const { config } = await fixture()
  await expandRepositorySnapshotPastTupleBound(config)
  const runner = fakeRunner(config)
  await assert.rejects(executeCandidateInstall(config, { runner }), /snapshot exceeds its tuple bound/)
  assert.equal(runner.events.filter((event) => event.kind === 'start').length, 0)
  assert.deepEqual(runner.events, [])
  await assert.rejects(stat(config.paths.tupleFile), /ENOENT/)
})

test('authenticated O4 evidence authority adds only the complete installed report/session/output flag group', async () => {
  const { config } = await fixture()
  const tupleIdentity = (await inspectAuthenticatedCandidate(config)).tupleIdentity
  const authority = o4EvidenceAuthority(config, tupleIdentity)
  const runner = fakeRunner(config)
  await executeCandidateInstall(config, { runner, o4EvidenceAuthority: authority })
  const serve = runner.events.find((event) => event.kind === 'start').argv
  const expected = [
    ['--o4-runner-session-authority', authority.sessionAuthorityFile],
    ['--o4-runner-session-authority-sha256', authority.sessionAuthoritySha256],
    ['--o4-installed-doctor-report', config.paths.installedDoctorReportFile],
    ['--o4-installed-doctor-report-sha256', sha256(await readFile(config.paths.installedDoctorReportFile))],
    ['--o4-evidence-tuple', tupleIdentity],
    ['--o4-a3-ledger', authority.sourceA3LedgerFile],
    ['--o4-verifier-ledger', authority.verifierLedgerFile],
    ['--o4-c-resource-dir', authority.cResourceDirectory],
    ['--o4-d-resource-dir', authority.dResourceDirectory],
  ]
  for (const [flag, value] of expected) {
    assert.equal(serve.filter((item) => item === flag).length, 1)
    assert.equal(serve[serve.indexOf(flag) + 1], value)
  }
  await assert.rejects(executeCandidateInstall((await fixture()).config, {
    runner: fakeRunner(config), o4EvidenceAuthority: { ...authority, tupleIdentity: d('wrong-tuple') },
  }), /tuple identity drifted/)
})

test('restart reuses tuple and roots, crosses a process boundary, and never reruns Setup', async () => {
  const { config } = await fixture(); const initial = fakeRunner(config)
  await executeCandidateInstall(config, { runner: initial })
  const taskWorktree = join(dirname(config.paths.repositoryRoot),
    'chora-task_018f1e2d-3c4b-7abc-8def-0123456789ab-accepted-change')
  await execFileAsync('/usr/bin/git', ['-C', config.paths.repositoryRoot, 'worktree', 'add',
    '--detach', taskWorktree, config.authority.repositoryCommit])
  await writeFile(join(taskWorktree, 'README.md'), 'accepted dirty task bytes\n')
  const restartMarkers = []
  const markerWriter = { beforeSpawn: async ({ target }) => restartMarkers.push(target) }
  const restart = fakeRunner(config)
  const first = await restartCandidate(config, { runner: restart, markerWriter })
  assert.deepEqual(restart.events.map(({ kind }) => kind), ['boundary', 'start'])
  assert.equal(restart.events.some((event) => event.argv?.[0] === 'setup'), false)
  assert.equal(first.restartReceipt.sequence, 1)
  assert.equal(first.restartReceipt.setupReplayed, false)
  assert.equal((await stat(join(config.paths.receiptDirectory, 'restarts', 'restart-01.json'))).mode & 0o777, 0o400)
  const secondRunner = fakeRunner(config)
  const second = await restartCandidate(config, { runner: secondRunner, markerWriter })
  assert.equal(second.restartReceipt.sequence, 2)
  assert.equal(second.restartReceipt.previousReceiptDigest, first.restartReceipt.receiptDigest)
  assert.deepEqual(second.handoff.restartReceiptDigests, [first.restartReceipt.receiptDigest, second.restartReceipt.receiptDigest])
  assert.deepEqual(restartMarkers, [
    join(config.paths.receiptDirectory, 'restarts', 'restart-01.json'),
    join(config.paths.receiptDirectory, 'restarts', 'restart-02.json'),
  ])
})

for (const [name, mutate, pattern] of [
  ['config', async (config) => execFileAsync('/usr/bin/git', ['-C', config.paths.repositoryRoot,
    'config', 'user.name', 'Mutated']), /repository config/],
  ['hook', async (config) => writeFile(join(config.paths.repositoryRoot, '.git', 'hooks', 'pre-commit'),
    '#!/bin/sh\nexit 1\n', { mode: 0o755 }), /active hook topology/],
  ['alternate', async (config) => writeFile(join(config.paths.repositoryRoot, '.git', 'objects',
    'info', 'alternates'), '/tmp/foreign-objects\n'), /alternates.*forbidden/],
  ['root tracked byte', async (config) => writeFile(join(config.paths.repositoryRoot, 'README.md'),
    'drifted root bytes\n'), /tracked repository bytes drifted/],
  ...['objects', 'HEAD', 'refs'].map((control) => [
    `symlinked ${control}`,
    async (config, t) => {
      const externalRoot = await realpath(await mkdtemp(join(tmpdir(),
        'chora-o4-external-git-control-')))
      t.after(() => rm(externalRoot, { recursive: true, force: true }))
      const source = join(config.paths.repositoryRoot, '.git', control)
      const external = join(externalRoot, control)
      await rename(source, external)
      await symlink(external, source)
    },
    /repository capture controller failed/,
  ]),
]) test(`restart repository verifier rejects ${name} drift after processBoundary with startCount=0`,
  async (t) => {
    const { config } = await fixture()
    await executeCandidateInstall(config, { runner: fakeRunner(config) })
    const restart = fakeRunner(config, { boundaryMutation: () => mutate(config, t) })
    await assert.rejects(restartCandidate(config, { runner: restart }), pattern)
    assert.equal(restart.events.filter((event) => event.kind === 'boundary').length, 1)
    assert.equal(restart.events.filter((event) => event.kind === 'start').length, 0)
  })

test('each authenticated authority class is mandatory before any runner or root mutation', async () => {
  const missing = [
    ['source', 'sourceManifestSha256'], ['candidate', 'candidateManifestSha256'], ['product', 'binarySha256'],
    ['release', 'offlineBundleEvidenceSha256'], ['PATH/private Pi', 'privatePiProvenanceSha256'],
    ['model', 'modelAuthoritySha256'],
    ['Docker CLI', 'dockerCLISha256'], ['roles', 'roles'],
  ]
  for (const [label, key] of missing) {
    const { config } = await fixture(); delete config.authority[key]
    const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstall(config, { runner }), /fields drifted/)
    assert.deepEqual(runner.events, [], label)
    await assert.rejects(stat(config.paths.installRoot), /ENOENT/)
    await assert.rejects(stat(config.paths.tupleFile), /ENOENT/)
  }
})

test('rejects every pre-existing dynamic evidence file and forged pre-Setup pass authority', async () => {
  for (const key of ['qualificationFile', 'modelObservationFile', 'installedDoctorReportFile']) {
    const { config } = await fixture(); await writeFile(config.paths[key], '{}\n', { mode: 0o400 })
    const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstall(config, { runner }), /pre-Setup dynamic evidence must be absent/)
    assert.deepEqual(runner.events, [])
  }
  const { config } = await fixture()
  const model = JSON.parse(await readFile(config.paths.modelAuthorityFile, 'utf8'))
  model.status = 'passed'
  await chmod(config.paths.modelAuthorityFile, 0o600)
  config.authority.modelAuthoritySha256 = await writeJSON(config.paths.modelAuthorityFile, model)
  await chmod(config.paths.modelAuthorityFile, 0o400)
  const runner = fakeRunner(config)
  await assert.rejects(executeCandidateInstall(config, { runner }), /model request authority unavailable/)
  assert.deepEqual(runner.events, [])
})

test('rejects post-Setup receipt/model splice and generation replay without publishing dynamic evidence', async () => {
  for (const forged of [
    { setup_receipt_sha256: d('spliced-setup') },
    { model_request_authority_sha256: d('spliced-model') },
    { generation_id: `gen_${'x'.repeat(32)}` },
  ]) {
    const { config } = await fixture(); const runner = fakeRunner(config, { installedProof: forged })
    await assert.rejects(executeCandidateInstall(config, { runner }), /binding drifted/)
    for (const key of ['qualificationFile', 'modelObservationFile', 'installedDoctorReportFile'])
      await assert.rejects(stat(config.paths[key]), /ENOENT/)
    assert.deepEqual((await readdir(config.paths.receiptDirectory)).filter((name) => /^\d/.test(name)).sort(),
      ['01-preflight.json', '02-install.json', '03-product_doctor.json', '04-setup.json'])
  }
})

test('rejects every unsupported product host before Runner/root mutation while Engine and OCI images remain linux/arm64', async () => {
  for (const platform of unsupportedProductHosts) {
    const { config } = await fixture()
    const enginePreflight = JSON.parse(await readFile(config.paths.preflightFile, 'utf8'))
    const releaseManifest = JSON.parse(await readFile(config.paths.actualReleaseManifestFile, 'utf8'))
    assert.deepEqual({ operatingSystem: enginePreflight.operatingSystem, architecture: enginePreflight.architecture },
      { operatingSystem: 'linux', architecture: 'arm64' })
    assert.deepEqual(releaseManifest.platform, { os: 'linux', architecture: 'arm64' })
    config.identity.platform = structuredClone(platform)
    const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstall(config, { runner }),
      /product host platform unsupported; exact darwin\/arm64 is required/)
    assert.deepEqual(runner.events, [], `${platform.os}/${platform.architecture}`)
    for (const path of [config.paths.installRoot, config.paths.stateRoot, config.paths.ledgerFile,
      config.paths.tupleFile, config.paths.receiptDirectory]) {
      await assert.rejects(stat(path), /ENOENT/)
    }
  }
})

test('stopped Engine Doctor proves zero Setup/install/state/ledger/tuple/root mutation', async () => {
  const { config } = await fixture(); const runner = fakeRunner(config, { stopped: true })
  await assert.rejects(executeCandidateInstall(config, { runner }), /Engine Doctor failed/)
  assert.equal(runner.events.length, 1)
  assert.equal(runner.events[0].argv.slice(0, 2).join(' '), 'product doctor')
  for (const path of [config.paths.installRoot, config.paths.stateRoot, config.paths.ledgerFile, config.paths.tupleFile, config.paths.receiptDirectory]) await assert.rejects(stat(path), /ENOENT/)
})

test('authenticated private Engine Doctor identity rejects every observed field drift before mutation', async () => {
  const drifts = {
    context_name: 'other-context', endpoint_digest: d('other-endpoint'), daemon_id: 'other-daemon',
    api_version: '1.46', operating_system: 'windows', architecture: 'amd64',
  }
  for (const [field, value] of Object.entries(drifts)) {
    const { config } = await fixture(); const runner = fakeRunner(config, { engineDrift: { [field]: value } })
    await assert.rejects(executeCandidateInstall(config, { runner }), /Engine Doctor .* drifted/)
    assert.equal(runner.events.length, 1, field)
    await assert.rejects(stat(config.paths.tupleFile), /ENOENT/)
    await assert.rejects(stat(config.paths.installRoot), /ENOENT/)
  }
})

test('rejects an existing non-owner-private receipt directory before install', async () => {
  const { config } = await fixture()
  await mkdir(config.paths.receiptDirectory, { mode: 0o755 })
  const runner = fakeRunner(config)
  await assert.rejects(executeCandidateInstall(config, { runner }), /owner-owned 0700/)
  assert.deepEqual(runner.events.map(({ kind }) => kind), ['exec'])
  await assert.rejects(stat(config.paths.tupleFile), /ENOENT/)
  await assert.rejects(stat(config.paths.installRoot), /ENOENT/)
})

test('production-invalid release closure fails before Runner or root mutation', async () => {
  const mutations = [
    (manifest) => { manifest.roles[2].alias_of_role = 'network_boundary' },
    (manifest) => { manifest.artifacts.reverse() },
    (manifest) => { manifest.artifacts[0].recipe_provenance = [] },
    (manifest) => { manifest.artifacts[0].build_inputs = [] },
    (manifest) => { manifest.artifacts[1].runtime = structuredClone(manifest.artifacts[0].runtime) },
  ]
  for (const mutate of mutations) {
    const { config } = await fixture(); await rewriteActualManifest(config, mutate); const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstall(config, { runner }), /actual (?:release|Dockerfile)/)
    assert.deepEqual(runner.events, [])
    await assert.rejects(stat(config.paths.tupleFile), /ENOENT/)
    await assert.rejects(stat(config.paths.installRoot), /ENOENT/)
  }
})

test('legacy Registry shape, archive byte drift, and Dockerfile drift fail before mutation', async () => {
  {
    const { config } = await fixture()
    await rewriteActualManifest(config, (manifest) => {
      manifest.artifacts[0].image = {
        oci_pull_reference: `registry.invalid/chora/managed@sha256:${d('legacy')}`,
        local_docker_config_image_id: image('managed-pi-runtime-config'),
        registry_verification: { method: 'authenticated_registry_manifest_inspection',
          evidence: { path: 'legacy.json', sha256: d('legacy-evidence') } },
      }
    })
    const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstall(config, { runner }), /actual release image fields drifted/)
    assert.deepEqual(runner.events, [])
  }
  {
    const { config } = await fixture()
    const manifest = JSON.parse(await readFile(config.paths.actualReleaseManifestFile, 'utf8'))
    const archive = join(config.paths.actualReleaseRoot, manifest.artifacts[0].image.archive.path)
    await chmod(archive, 0o600); await writeFile(archive, Buffer.alloc(97, 0xff)); await chmod(archive, 0o444)
    const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstall(config, { runner }), /archive bytes drifted/)
    assert.deepEqual(runner.events, [])
  }
  {
    const { config } = await fixture()
    await rewriteActualManifest(config, async (manifest) => {
      const artifact = manifest.artifacts[0]; const recipePath = join(config.paths.actualReleaseRoot, artifact.recipe.path)
      const changed = (await readFile(recipePath, 'utf8')).replace('FROM ${BASE_PULL_REFERENCE}', 'FROM scratch')
      await chmod(recipePath, 0o600); await writeFile(recipePath, changed); await chmod(recipePath, 0o444)
      const recipeDigest = sha256(changed); artifact.recipe.sha256 = recipeDigest; artifact.recipe_provenance[0].sha256 = recipeDigest
      artifact.context.sha256 = await releaseContextDigest(config.paths.actualReleaseRoot, artifact.context.path)
    })
    const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstall(config, { runner }), /Dockerfile/)
    assert.deepEqual(runner.events, [])
  }
})

test('actual role/image mismatch fails before Runner or root mutation', async () => {
  const { config } = await fixture()
  await rewriteActualManifest(config, async (manifest) => {
    const artifact = manifest.artifacts[0]
    artifact.image.local_docker_config_image_id = image('substituted-config')
  })
  const runner = fakeRunner(config)
  await assert.rejects(executeCandidateInstall(config, { runner }), /role\/image binding drifted/)
  assert.deepEqual(runner.events, [])
  await assert.rejects(stat(config.paths.tupleFile), /ENOENT/)
  await assert.rejects(stat(config.paths.installRoot), /ENOENT/)
})

test('Docker CLI topology and authenticated source mismatches fail before mutation', async () => {
  {
    const { config } = await fixture()
    config.authority.dockerContext = 'chora-o4'
    const runner = fakeRunner(config)
    await assert.rejects(executeCandidateInstall(config, { runner }),
      /exact Colima-backed Lima instance name/)
    assert.deepEqual(runner.events, [])
  }
  {
    const { config } = await fixture(); const wrong = join(config.paths.colimaToolRoot, 'docker-wrong')
    await copyFile(config.paths.dockerCLIFile, wrong); config.paths.dockerCLIFile = wrong
    const runner = fakeRunner(config); await assert.rejects(executeCandidateInstall(config, { runner }), /Docker CLI topology drifted/)
    assert.deepEqual(runner.events, [])
  }
  {
    const { config } = await fixture(); const changed = Buffer.from('different authenticated client\n')
    await chmod(config.paths.dockerClientSourceFile, 0o700); await writeFile(config.paths.dockerClientSourceFile, changed)
    config.authority.dockerClientSourceSha256 = sha256(changed)
    const runner = fakeRunner(config); await assert.rejects(executeCandidateInstall(config, { runner }), /Docker CLI\/source identity drifted/)
    assert.deepEqual(runner.events, [])
    await assert.rejects(stat(config.paths.tupleFile), /ENOENT/)
  }
})

test('rejects tamper, unknown fields, and oversized authenticated input before mutation', async () => {
  {
    const { config } = await fixture(); await chmod(config.paths.binaryFile, 0o700); await writeFile(config.paths.binaryFile, 'tampered')
    await assert.rejects(inspectAuthenticatedCandidate(config), /binarySha256 drifted/)
  }
  {
    const { config } = await fixture(); const value = JSON.parse(await readFile(config.paths.modelAuthorityFile)); value.extra = true
    await chmod(config.paths.modelAuthorityFile, 0o600); await writeJSON(config.paths.modelAuthorityFile, value)
    await chmod(config.paths.modelAuthorityFile, 0o400); config.authority.modelAuthoritySha256 = sha256(`${JSON.stringify(value, null, 2)}\n`)
    await assert.rejects(inspectAuthenticatedCandidate(config), /model authority fields drifted/)
  }
  {
    const { config } = await fixture(); await chmod(config.paths.preflightFile, 0o600)
    await writeFile(config.paths.preflightFile, Buffer.alloc(2 * 1024 * 1024 + 1))
    await assert.rejects(inspectAuthenticatedCandidate(config), /bounded immutable regular file/)
  }
})

test('rejects symlink input and every symlink ancestor', async () => {
  {
    const { root, config } = await fixture(); const linkPath = join(root, 'candidate-link.json')
    await symlink(config.paths.candidateManifestFile, linkPath); config.paths.candidateManifestFile = linkPath
    await assert.rejects(inspectAuthenticatedCandidate(config), /symlink/)
  }
  {
    const { root, config } = await fixture(); const actual = join(root, 'actual-root'); const linked = join(root, 'linked-root')
    await mkdir(actual); await symlink(actual, linked); config.paths.installRoot = join(linked, 'install'); config.paths.installedBinaryFile = join(config.paths.installRoot, 'bin/chora'); config.paths.installedWebDirectory = join(config.paths.installRoot, 'web')
    await assert.rejects(inspectAuthenticatedCandidate(config), /symlink ancestor/)
  }
})

test('rejects hard-linked authority input and overlapping roots', async () => {
  {
    const { root, config } = await fixture(); await link(config.paths.binaryFile, join(root, 'binary-hardlink'))
    await assert.rejects(inspectAuthenticatedCandidate(config), /bounded immutable regular file/)
  }
  {
    const { config } = await fixture(); config.paths.stateRoot = join(config.paths.installRoot, 'state'); config.paths.ledgerFile = join(config.paths.stateRoot, 'ledger.json')
    await assert.rejects(inspectAuthenticatedCandidate(config), /mutable roots overlap/)
  }
})

test('rehashes frozen inputs before every mutation phase and fails before Setup on drift', async () => {
  const { config } = await fixture(); const runner = fakeRunner(config)
  const originalInstall = runner.installImmutable
  runner.installImmutable = async (spec) => { await originalInstall(spec); await chmod(config.paths.releaseSpecFile, 0o600); await writeFile(config.paths.releaseSpecFile, 'tampered') }
  await assert.rejects(executeCandidateInstall(config, { runner }), /frozen authority input drifted/)
  assert.equal(runner.events.some((event) => event.argv?.[0] === 'setup'), false)
})

test('bounds and redacts runner output without leaking secrets or paths into errors/receipts', async () => {
  const { config } = await fixture(); const runner = fakeRunner(config)
  runner.exec = async () => ({ exitCode: 1, stdout: '', stderr: `/Users/alice/token sk-${'x'.repeat(32)}` })
  await assert.rejects(executeCandidateInstall(config, { runner }), (error) => {
    assert.equal(error.message, 'candidate Engine Doctor failed'); assert.equal(error.message.includes('/Users/'), false); assert.equal(error.message.includes('sk-'), false); return true
  })
  const oversized = fakeRunner(config); oversized.exec = async () => ({ exitCode: 0, stdout: 'x'.repeat(64 * 1024 + 1), stderr: '' })
  await assert.rejects(executeCandidateInstall(config, { runner: oversized }), /oversized/)
})

test('tuple and receipts are exclusive immutable 0400 files and reject tamper/partial replacement', async () => {
  const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
  assert.equal((await stat(config.paths.tupleFile)).mode & 0o777, 0o400)
  const receiptNames = (await readdir(config.paths.receiptDirectory)).filter((name) => /^\d/.test(name))
  assert.equal(receiptNames.length, 6)
  for (const name of receiptNames) assert.equal((await stat(join(config.paths.receiptDirectory, name))).mode & 0o777, 0o400)
  await assert.rejects(executeCandidateInstall(config, { runner: fakeRunner(config) }), /pre-Setup dynamic evidence must be absent/)
  const first = join(config.paths.receiptDirectory, receiptNames.sort()[0]); await chmod(first, 0o600)
  const value = JSON.parse(await readFile(first)); value.operationDigest = d('spliced'); await writeJSON(first, value, 0o400)
  await assert.rejects(buildHandoff(config.paths.tupleFile, config.paths.receiptDirectory), /(?:mode|digest) drifted/)
})

test('tuple and the complete initial phase prefix publish only through the pinned native marker writer', async () => {
  const { config } = await fixture()
  const markers = []
  await executeCandidateInstall(config, {
    runner: fakeRunner(config),
    markerWriter: {
      beforeSpawn: async ({ protocol, target, sha256: digest, byteLength }) => {
        markers.push({ protocol, target, digest, byteLength })
      },
    },
  })
  assert.deepEqual(markers.map(({ target }) => target), [
    config.paths.tupleFile,
    ...['preflight', 'install', 'product_doctor', 'setup', 'doctor', 'serve'].map((phase, index) =>
      join(config.paths.receiptDirectory, `${String(index + 1).padStart(2, '0')}-${phase}.json`)),
  ])
  assert.equal(markers.every(({ protocol }) =>
    protocol === 'chora.m1-o4-owned-readonly-write.v1'), true)
  assert.equal(markers.every(({ digest, byteLength }) =>
    /^[0-9a-f]{64}$/.test(digest) && Number.isSafeInteger(byteLength) && byteLength > 0), true)
})

test('native phase marker rejects EEXIST, a bad receipt, and a foreign swap without claiming either path', async (t) => {
  await t.test('EEXIST no-replace', async () => {
    const { config } = await fixture()
    await executeCandidateInstall(config, { runner: fakeRunner(config) })
    const target = join(config.paths.receiptDirectory, '07-C.json')
    const foreign = Buffer.from('foreign phase marker\n')
    await assert.rejects(appendHandoffReceipt({
      tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory,
      phase: 'C', operationDigest: d('eexist-c-op'),
      observationDigest: d('eexist-c-observation'),
    }, { markerWriter: { beforeSpawn: async ({ target: observed }) => {
      assert.equal(observed, target)
      await writeFile(target, foreign, { flag: 'wx', mode: 0o400 })
    } } }), /owned readonly controller failed/)
    assert.deepEqual(await readFile(target), foreign)
    assert.equal((await stat(target)).mode & 0o777, 0o400)
  })

  await t.test('malformed native receipt', async () => {
    const { config } = await fixture()
    await executeCandidateInstall(config, { runner: fakeRunner(config) })
    const target = join(config.paths.receiptDirectory, '07-C.json')
    let published
    await assert.rejects(appendHandoffReceipt({
      tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory,
      phase: 'C', operationDigest: d('bad-receipt-c-op'),
      observationDigest: d('bad-receipt-c-observation'),
    }, { markerWriter: { spawn: fakeReadonlyMarkerSpawn(async ({ argv, bytes }) => {
      assert.equal(argv[argv.indexOf('--target') + 1], target)
      await writeFile(target, bytes, { flag: 'wx', mode: 0o400 })
      published = Buffer.from(bytes)
      const identity = ownedPathIdentity(await lstat(target, { bigint: true }))
      return { stdout: readonlyMarkerReceipt(target, bytes, identity, { extra: true }) }
    }) } }), /keys drifted/)
    assert.deepEqual(await readFile(target), published)
    assert.equal((await stat(target)).mode & 0o777, 0o400)
  })

  await t.test('foreign post-publication swap', async () => {
    const { config } = await fixture()
    await executeCandidateInstall(config, { runner: fakeRunner(config) })
    const target = join(config.paths.receiptDirectory, '07-C.json')
    const displaced = join(config.paths.receiptDirectory, '07-C.owned-displaced')
    const foreign = Buffer.from('foreign swapped phase marker\n')
    let published
    await assert.rejects(appendHandoffReceipt({
      tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory,
      phase: 'C', operationDigest: d('swap-c-op'),
      observationDigest: d('swap-c-observation'),
    }, { markerWriter: { spawn: fakeReadonlyMarkerSpawn(async ({ bytes }) => {
      await writeFile(target, bytes, { flag: 'wx', mode: 0o400 })
      published = Buffer.from(bytes)
      const identity = ownedPathIdentity(await lstat(target, { bigint: true }))
      await rename(target, displaced)
      await writeFile(target, foreign, { flag: 'wx', mode: 0o400 })
      return { stdout: readonlyMarkerReceipt(target, bytes, identity) }
    }) } }), /target identity drifted/)
    assert.deepEqual(await readFile(target), foreign)
    assert.deepEqual(await readFile(displaced), published)
    assert.equal((await stat(target)).mode & 0o777, 0o400)
    assert.equal((await stat(displaced)).mode & 0o777, 0o400)
  })
})

test('phase appends enforce the sole exact sequence and compute one exact handoff phase', async () => {
  const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
  const laterMarkers = []
  const markerWriter = { beforeSpawn: async ({ target }) => laterMarkers.push(target) }
  assert.deepEqual((await buildHandoff(config.paths.tupleFile, config.paths.receiptDirectory)).allowedNextPhases, ['C'])
  await assert.rejects(appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'D', operationDigest: d('skip-op'), observationDigest: d('skip-observation') }), /exact next phase/)
  const c = await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('c-op'), observationDigest: d('c-observation') }, { markerWriter })
  assert.deepEqual((await buildHandoff(config.paths.tupleFile, config.paths.receiptDirectory)).allowedNextPhases, ['D'])
  await assert.rejects(appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('repeat-c-op'), observationDigest: d('repeat-c-observation') }), /exact next phase/)
  const dReceipt = await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'D', operationDigest: d('d-op'), observationDigest: d('d-observation') }, { markerWriter })
  assert.equal(dReceipt.previousReceiptDigest, c.receiptDigest)
  assert.deepEqual((await buildHandoff(config.paths.tupleFile, config.paths.receiptDirectory)).allowedNextPhases, ['E'])
  assert.deepEqual(laterMarkers, [
    join(config.paths.receiptDirectory, '07-C.json'),
    join(config.paths.receiptDirectory, '08-D.json'),
  ])
  await assert.rejects(appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('backtrack-op'), observationDigest: d('backtrack-observation') }), /exact next phase/)
  await assert.rejects(appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'D', operationDigest: d('repeat-d-op'), observationDigest: d('repeat-d-observation') }), /exact next phase/)
  await writeFile(join(config.paths.receiptDirectory, '07-unrelated.json'), '{}\n', { mode: 0o400 })
  await assert.rejects(buildHandoff(config.paths.tupleFile, config.paths.receiptDirectory), /unrelated entry/)
})

test('concurrent C/D and duplicate C appends fail closed with only the exact C prefix', async () => {
  for (let iteration = 0; iteration < 20; iteration++) {
    const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
    const results = await Promise.allSettled(['C', 'D'].map((phase, index) => appendHandoffReceipt({
      tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase,
      operationDigest: d(`${iteration}-${phase}-${index}-operation`),
      observationDigest: d(`${iteration}-${phase}-${index}-observation`),
    })))
    assert.equal(results.filter(({ status, value }) => status === 'fulfilled' && value.phase === 'D').length, 0)
    assert.equal((await readdir(config.paths.receiptDirectory)).some((name) => name.endsWith('-D.json')), false)
    const afterRace = await buildHandoff(config.paths.tupleFile, config.paths.receiptDirectory)
    assert.equal([['C'], ['D']].some((allowed) => JSON.stringify(allowed) === JSON.stringify(afterRace.allowedNextPhases)), true)
    if (afterRace.allowedNextPhases[0] === 'C') {
      await appendHandoffReceipt({
        tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C',
        operationDigest: d(`${iteration}-serial-c-operation`),
        observationDigest: d(`${iteration}-serial-c-observation`),
      })
    }
    assert.deepEqual((await buildHandoff(config.paths.tupleFile, config.paths.receiptDirectory)).allowedNextPhases, ['D'])
  }
  const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
  const duplicates = await Promise.allSettled([0, 1].map((index) => appendHandoffReceipt({
    tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C',
    operationDigest: d(`duplicate-c-${index}-operation`),
    observationDigest: d(`duplicate-c-${index}-observation`),
  })))
  assert.equal(duplicates.filter(({ status }) => status === 'fulfilled').length, 1)
  assert.equal(duplicates.filter(({ status }) => status === 'rejected').length, 1)
  assert.deepEqual((await buildHandoff(config.paths.tupleFile, config.paths.receiptDirectory)).allowedNextPhases, ['D'])
})

test('stale and contended phase locks fail closed and never append a receipt', async () => {
  {
    const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
    const lock = join(config.paths.receiptDirectory, '.phase-append.lock')
    await mkdir(lock, { mode: 0o700 })
    await assert.rejects(appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('stale-op'), observationDigest: d('stale-observation') }), /held or stale/)
    await assert.rejects(buildHandoff(config.paths.tupleFile, config.paths.receiptDirectory), /held or stale/)
    assert.deepEqual((await readdir(config.paths.receiptDirectory)).filter((name) => name.endsWith('-C.json')), [])
    await rmdir(lock)
  }
  {
    const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
    await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('c-op'), observationDigest: d('c-observation') })
    await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'D', operationDigest: d('d-op'), observationDigest: d('d-observation') })
    const tuple = JSON.parse(await readFile(config.paths.tupleFile, 'utf8'))
    let releaseSeal
    const sealing = new Promise((resolve) => { releaseSeal = resolve })
    const finalizing = finalizeEvidencePhaseE({
      tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory,
      closeAllServices: async () => {},
      reopenAndSealA3: async () => { await sealing; return finalA3(tuple) },
      formCompositeB1: async ({ sealedA3 }) => finalB1(tuple, sealedA3),
      validateEvidence: async ({ sealedA3, composite }) => finalValidation(tuple, sealedA3, composite),
    })
    while (!(await readdir(config.paths.receiptDirectory)).includes('.phase-append.lock')) await new Promise((resolve) => setImmediate(resolve))
    await assert.rejects(appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'D', operationDigest: d('contended-op'), observationDigest: d('contended-observation') }), /held or stale/)
    await assert.rejects(buildHandoff(config.paths.tupleFile, config.paths.receiptDirectory), /held or stale/)
    releaseSeal()
    await finalizing
  }
})

test('phase-lock release preserves a swapped foreign empty lock and aggregates callback/release failures', async () => {
  const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('c-op'), observationDigest: d('c-observation') })
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'D', operationDigest: d('d-op'), observationDigest: d('d-observation') })
  const tuple = JSON.parse(await readFile(config.paths.tupleFile, 'utf8'))
  const lockPath = join(config.paths.receiptDirectory, '.phase-append.lock')
  const displacedLockPath = join(config.paths.receiptDirectory, '.phase-append.owned-displaced')
  let foreignIdentity
  await assert.rejects(finalizeEvidencePhaseE({
    tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory,
    closeAllServices: async () => {
      await rename(lockPath, displacedLockPath)
      await mkdir(lockPath, { mode: 0o700 })
      foreignIdentity = await stat(lockPath)
      throw new Error('injected phase callback failure')
    },
    reopenAndSealA3: async () => finalA3(tuple),
    formCompositeB1: async ({ sealedA3 }) => finalB1(tuple, sealedA3),
    validateEvidence: async ({ sealedA3, composite }) => finalValidation(tuple, sealedA3, composite),
  }), (error) => assertAggregateMessages(error, [
    /injected phase callback failure/,
    /owned cleanup path identity drifted/,
  ]))
  const preserved = await stat(lockPath)
  assert.equal(preserved.dev, foreignIdentity.dev)
  assert.equal(preserved.ino, foreignIdentity.ino)
  assert.deepEqual(await readdir(lockPath), [])
  assert.equal((await stat(displacedLockPath)).isDirectory(), true)
})

test('E requires D and executes close, seal, B1 formation, final validation, append in order', async () => {
  const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
  const tuple = JSON.parse(await readFile(config.paths.tupleFile, 'utf8'))
  let callbacks = 0
  const options = {
    tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory,
    closeAllServices: async () => { callbacks++ },
    reopenAndSealA3: async () => { callbacks++; return finalA3(tuple) },
    formCompositeB1: async ({ sealedA3 }) => { callbacks++; return finalB1(tuple, sealedA3) },
    validateEvidence: async ({ sealedA3, composite }) => { callbacks++; return finalValidation(tuple, sealedA3, composite) },
  }
  await assert.rejects(finalizeEvidencePhaseE(options), /exact phase-D prefix/)
  assert.equal(callbacks, 0)
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('c-op'), observationDigest: d('c-observation') })
  await assert.rejects(finalizeEvidencePhaseE(options), /exact phase-D prefix/)
  assert.equal(callbacks, 0)
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'D', operationDigest: d('d-op'), observationDigest: d('d-observation') })
  const order = []
  const finalMarkers = []
  const final = await finalizeEvidencePhaseE({
    tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory,
    closeAllServices: async () => order.push('close'),
    reopenAndSealA3: async () => { order.push('seal'); return finalA3(tuple) },
    formCompositeB1: async ({ sealedA3 }) => { order.push('composite'); return finalB1(tuple, sealedA3) },
    validateEvidence: async ({ sealedA3, composite }) => { order.push('validate'); return finalValidation(tuple, sealedA3, composite) },
  }, { markerWriter: { beforeSpawn: async ({ target }) => finalMarkers.push(target) } })
  assert.deepEqual(order, ['close', 'seal', 'composite', 'validate'])
  assert.equal(final.phase, 'E')
  assert.deepEqual(finalMarkers, [join(config.paths.receiptDirectory, '09-E.json')])
  assert.deepEqual((await buildHandoff(config.paths.tupleFile, config.paths.receiptDirectory)).allowedNextPhases, [])
})

test('E callback failure or final B1 binding drift leaves no E receipt and releases the lock', async () => {
  const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('c-op'), observationDigest: d('c-observation') })
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'D', operationDigest: d('d-op'), observationDigest: d('d-observation') })
  const tuple = JSON.parse(await readFile(config.paths.tupleFile, 'utf8'))
  const base = {
    tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory,
    closeAllServices: async () => {}, reopenAndSealA3: async () => finalA3(tuple),
  }
  await assert.rejects(finalizeEvidencePhaseE({
    ...base, formCompositeB1: async () => { throw new Error('B1 callback failed') },
    validateEvidence: async () => { throw new Error('must not run') },
  }), /B1 callback failed/)
  assert.equal((await readdir(config.paths.receiptDirectory)).some((name) => name.endsWith('-E.json')), false)
  assert.equal((await readdir(config.paths.receiptDirectory)).includes('.phase-append.lock'), false)
  await assert.rejects(finalizeEvidencePhaseE({
    ...base,
    formCompositeB1: async ({ sealedA3 }) => ({
      ...finalB1(tuple, sealedA3), schemaVersion: 'chora.m1-o4-sealed-a3-runner-ledger.v1',
    }),
    validateEvidence: async () => { throw new Error('must not run') },
  }), /raw A3 JSON/)
  await assert.rejects(finalizeEvidencePhaseE({
    ...base, formCompositeB1: async ({ sealedA3 }) => finalB1(tuple, sealedA3, { sourceA3LedgerSha256: d('wrong-a3') }),
    validateEvidence: async () => { throw new Error('must not run') },
  }), /exact sealed A3/)
  assert.equal((await readdir(config.paths.receiptDirectory)).some((name) => name.endsWith('-E.json')), false)
  await assert.rejects(finalizeEvidencePhaseE({
    ...base, formCompositeB1: async ({ sealedA3 }) => finalB1(tuple, sealedA3),
    validateEvidence: async ({ sealedA3, composite }) => finalValidation(tuple, sealedA3, composite, { b1Sha256: d('wrong-b1') }),
  }), /tuple\/A3\/B1 binding/)
  assert.equal((await readdir(config.paths.receiptDirectory)).some((name) => name.endsWith('-E.json')), false)
})

test('E rejects every self-consistently rehashed unsupported product-host tuple with zero finalization side effects', async () => {
  for (const platform of unsupportedProductHosts) {
    const { config } = await fixture()
    await executeCandidateInstall(config, { runner: fakeRunner(config) })
    await appendHandoffReceipt({
      tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C',
      operationDigest: d(`platform-${platform.os}-${platform.architecture}-c-op`),
      observationDigest: d(`platform-${platform.os}-${platform.architecture}-c-observation`),
    })
    await appendHandoffReceipt({
      tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'D',
      operationDigest: d(`platform-${platform.os}-${platform.architecture}-d-op`),
      observationDigest: d(`platform-${platform.os}-${platform.architecture}-d-observation`),
    })
    const tuple = JSON.parse(await readFile(config.paths.tupleFile, 'utf8'))
    tuple.platform = structuredClone(platform)
    delete tuple.identity
    tuple.identity = sha256(canonicalJSONStringify(tuple))
    await chmod(config.paths.tupleFile, 0o600)
    await writeFile(config.paths.tupleFile, `${JSON.stringify(tuple, null, 2)}\n`)
    await chmod(config.paths.tupleFile, 0o400)
    const before = await readdir(config.paths.receiptDirectory)
    const callbacks = []
    await assert.rejects(finalizeEvidencePhaseE({
      tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory,
      closeAllServices: async () => callbacks.push('close'),
      reopenAndSealA3: async () => { callbacks.push('seal'); return finalA3(tuple) },
      formCompositeB1: async ({ sealedA3 }) => { callbacks.push('composite'); return finalB1(tuple, sealedA3) },
      validateEvidence: async ({ sealedA3, composite }) => {
        callbacks.push('validate'); return finalValidation(tuple, sealedA3, composite)
      },
    }), /product host platform unsupported; exact darwin\/arm64 is required/)
    assert.deepEqual(callbacks, [], `${platform.os}/${platform.architecture}`)
    assert.deepEqual(await readdir(config.paths.receiptDirectory), before)
  }
})

test('builds exact canonical 0400 O4 authority and restarts with only three added flags and no Setup', async () => {
  const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('c-op'), observationDigest: d('c-observation') })
  const authorityPath = join(config.paths.dataRoot, 'o4-fault-authority.json')
  const authority = await buildO4FaultAuthority({ config, authorityPath, scenarioBindings })
  const bytes = await readFile(authorityPath)
  assert.equal((await stat(authorityPath)).mode & 0o777, 0o400)
  assert.equal(bytes.toString('utf8'), `${JSON.stringify(authority.document)}\n`)
  assert.equal(sha256(bytes), authority.authoritySha256)
  assert.deepEqual(Object.keys(authority.document), [
    'schemaVersion', 'status', 'scope', 'tupleIdentity', 'generationId', 'environmentId', 'installId',
    'binarySha256', 'sourceAggregateSha256', 'dataRootSha256', 'policySha256', 'scenarios', 'claimBoundary',
  ])
  assert.deepEqual(authority.document.scenarios.map(({ scenario, action, attemptSequence, agentExecutionProfile }) =>
    ({ scenario, action, attemptSequence, agentExecutionProfile })), [
    { scenario: 'failure', action: 'force_exact_managed_attempt_nonzero_after_started_v1', attemptSequence: 1, agentExecutionProfile: 'standard' },
    { scenario: 'timeout', action: 'accelerated_managed_attempt_deadline_v1', attemptSequence: 1, agentExecutionProfile: 'standard' },
  ])
  assert.notEqual(authority.document.scenarios[0].taskId, authority.document.scenarios[1].taskId)
  assert.notEqual(authority.document.scenarios[0].snapshotId, authority.document.scenarios[1].snapshotId)
  const baseRunner = fakeRunner(config); await restartCandidate(config, { runner: baseRunner })
  const baseServe = baseRunner.events.find(({ kind }) => kind === 'start').argv
  const runner = fakeRunner(config)
  await restartCandidate(config, { runner, o4Authority: authority })
  assert.deepEqual(runner.events.map(({ kind }) => kind), ['boundary', 'start'])
  assert.equal(runner.events.some((event) => event.argv?.[0] === 'setup'), false)
  const serve = runner.events.find(({ kind }) => kind === 'start').argv
  assert.deepEqual(serve.slice(0, baseServe.length), baseServe)
  assert.deepEqual(serve.slice(baseServe.length), [
    '--o4-acceptance-authority', authority.authorityPath,
    '--o4-acceptance-authority-sha256', authority.authoritySha256,
    '--o4-candidate-tuple', authority.candidateTuple,
  ])
})

test('O4 authority parent-directory sync failure removes the unusable file before any runner event', async () => {
  const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('c-op'), observationDigest: d('c-observation') })
  const authorityPath = join(config.paths.dataRoot, 'o4-fault-authority.json')
  const runner = fakeRunner(config)
  let syncCalls = 0
  await assert.rejects(buildO4FaultAuthority({ config, authorityPath, scenarioBindings }, {
    syncDirectory: async (path) => {
      syncCalls++
      assert.equal(path, config.paths.dataRoot)
      assert.equal((await stat(authorityPath)).mode & 0o777, 0o400)
      throw new Error('injected data-root sync failure')
    },
  }), /injected data-root sync failure/)
  assert.equal(syncCalls, 1)
  await assert.rejects(stat(authorityPath), /ENOENT/)
  assert.deepEqual(runner.events, [])
  const recovered = await buildO4FaultAuthority({ config, authorityPath, scenarioBindings })
  assert.equal((await stat(recovered.authorityPath)).mode & 0o777, 0o400)
})

test('exclusive O4 authority refreshes its 0400 identity before post-chmod cleanup', async () => {
  const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('post-chmod-c-op'), observationDigest: d('post-chmod-c-observation') })
  const authorityPath = join(config.paths.dataRoot, 'o4-fault-authority.json')
  await assert.rejects(buildO4FaultAuthority({ config, authorityPath, scenarioBindings }, {
    syncDirectory: async () => {},
    authorityWriteStep: async ({ stage }) => {
      if (stage === 'post-chmod') throw new Error('injected post-chmod authority failure')
    },
  }), /injected post-chmod authority failure/)
  await assert.rejects(stat(authorityPath), /ENOENT/)
})

test('exclusive O4 authority cleanup preserves a foreign swap at every write boundary', async (t) => {
  for (const stage of ['write', 'chmod', 'post-chmod', 'validate']) await t.test(stage, async () => {
    const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
    await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d(`${stage}-c-op`), observationDigest: d(`${stage}-c-observation`) })
    const authorityPath = join(config.paths.dataRoot, 'o4-fault-authority.json')
    const displacedPath = join(config.paths.dataRoot, `o4-fault-authority.${stage}.owned`)
    const foreignBytes = Buffer.from(`foreign-${stage}\n`)
    let swapped = false
    await assert.rejects(buildO4FaultAuthority({ config, authorityPath, scenarioBindings }, {
      syncDirectory: async () => {},
      authorityWriteStep: async ({ stage: currentStage, path }) => {
        assert.equal(path, authorityPath)
        if (currentStage !== stage || swapped) return
        swapped = true
        await rename(authorityPath, displacedPath)
        await writeFile(authorityPath, foreignBytes, { flag: 'wx', mode: 0o400 })
        await chmod(authorityPath, 0o400)
        if (stage !== 'validate') throw new Error(`injected ${stage} failure`)
      },
    }), (error) => assertAggregateMessages(error, [
      stage === 'validate' ? /immutable creation failed/ : new RegExp(`injected ${stage} failure`),
      /owned cleanup path identity drifted/,
    ]))
    assert.equal(swapped, true)
    assert.deepEqual(await readFile(authorityPath), foreignBytes)
    assert.equal((await stat(authorityPath)).mode & 0o777, 0o400)
    assert.equal((await stat(displacedPath)).isFile(), true)
  })
})

test('O4 authority sync cleanup preserves a swapped foreign authority and aggregates both failures', async () => {
  const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('sync-swap-c-op'), observationDigest: d('sync-swap-c-observation') })
  const authorityPath = join(config.paths.dataRoot, 'o4-fault-authority.json')
  const displacedPath = join(config.paths.dataRoot, 'o4-fault-authority.sync-owned')
  const foreignBytes = Buffer.from('foreign-sync\n')
  let foreignIdentity
  await assert.rejects(buildO4FaultAuthority({ config, authorityPath, scenarioBindings }, {
    syncDirectory: async () => {
      await rename(authorityPath, displacedPath)
      await writeFile(authorityPath, foreignBytes, { flag: 'wx', mode: 0o400 })
      await chmod(authorityPath, 0o400)
      foreignIdentity = await stat(authorityPath)
      throw new Error('injected authority sync swap failure')
    },
  }), (error) => assertAggregateMessages(error, [
    /injected authority sync swap failure/,
    /owned cleanup path identity drifted/,
  ]))
  const preserved = await stat(authorityPath)
  assert.equal(preserved.dev, foreignIdentity.dev)
  assert.equal(preserved.ino, foreignIdentity.ino)
  assert.deepEqual(await readFile(authorityPath), foreignBytes)
  assert.equal((await stat(displacedPath)).isFile(), true)
})

test('O4 authority rejects reordered/duplicate bindings, partial restart bindings, tamper, and D/E restart', async () => {
  const { config } = await fixture(); await executeCandidateInstall(config, { runner: fakeRunner(config) })
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'C', operationDigest: d('c-op'), observationDigest: d('c-observation') })
  await assert.rejects(buildO4FaultAuthority({ config, authorityPath: join(config.paths.dataRoot, 'reordered.json'), scenarioBindings: [...scenarioBindings].reverse() }), /scenario order/)
  const duplicate = scenarioBindings.map((value) => ({ ...value })); duplicate[1].taskId = duplicate[0].taskId
  await assert.rejects(buildO4FaultAuthority({ config, authorityPath: join(config.paths.dataRoot, 'duplicate.json'), scenarioBindings: duplicate }), /must be distinct/)
  const authorityPath = join(config.paths.dataRoot, 'o4-fault-authority.json')
  const authority = await buildO4FaultAuthority({ config, authorityPath, scenarioBindings })
  for (const field of ['authorityPath', 'authoritySha256', 'candidateTuple']) {
    const partial = structuredClone(authority); delete partial[field]
    const runner = fakeRunner(config)
    await assert.rejects(restartCandidate(config, { runner, o4Authority: partial }), /fields drifted/)
    assert.deepEqual(runner.events, [])
  }
  const canonicalAuthority = await readFile(authorityPath)
  await chmod(authorityPath, 0o600)
  const tampered = Buffer.from(canonicalAuthority); tampered[0] = 0x20
  await writeFile(authorityPath, tampered); await chmod(authorityPath, 0o400)
  const tamperRunner = fakeRunner(config)
  await assert.rejects(restartCandidate(config, { runner: tamperRunner, o4Authority: authority }), /tampered/)
  assert.deepEqual(tamperRunner.events, [])
  await chmod(authorityPath, 0o600); await writeFile(authorityPath, canonicalAuthority); await chmod(authorityPath, 0o400)
  await appendHandoffReceipt({ tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory, phase: 'D', operationDigest: d('d-op'), observationDigest: d('d-observation') })
  const afterD = fakeRunner(config)
  await assert.rejects(restartCandidate(config, { runner: afterD }), /pre-D phase states/)
  assert.deepEqual(afterD.events, [])
  const authorizedAfterD = fakeRunner(config)
  await assert.rejects(restartCandidate(config, { runner: authorizedAfterD, o4Authority: authority }), /exact phase C/)
  assert.deepEqual(authorizedAfterD.events, [])
  const tuple = JSON.parse(await readFile(config.paths.tupleFile, 'utf8'))
  await finalizeEvidencePhaseE({
    tupleFile: config.paths.tupleFile, receiptDirectory: config.paths.receiptDirectory,
    closeAllServices: async () => {}, reopenAndSealA3: async () => finalA3(tuple),
    formCompositeB1: async ({ sealedA3 }) => finalB1(tuple, sealedA3),
    validateEvidence: async ({ sealedA3, composite }) => finalValidation(tuple, sealedA3, composite),
  })
  const authorizedAfterE = fakeRunner(config)
  await assert.rejects(restartCandidate(config, { runner: authorizedAfterE, o4Authority: authority }), /exact phase C/)
  assert.deepEqual(authorizedAfterE.events, [])
})
