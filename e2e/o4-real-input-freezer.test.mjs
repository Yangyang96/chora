import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { EventEmitter } from 'node:events'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { chmod, copyFile, link, lstat, mkdir, mkdtemp, readFile, readdir, readlink, rename, rm,
  stat, realpath, symlink, unlink, writeFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'
import { PassThrough } from 'node:stream'
import {
  FINALIZE_REQUEST_SCHEMA,
  FREEZE_REQUEST_SCHEMA,
  digestO4PreparedClosure,
  finalizeO4MaterializationRequest,
  publishOwnerOnlyJSON0400,
  stageO4RealInputs,
} from './o4-real-input-freezer.mjs'
import { prepareO4StaticInputs } from './o4-inputs-preparer.mjs'
import { materializeO4OfflineRelease } from './o4-local-offline-materializer.mjs'
import { inspectRepositoryAuthority } from './o4-repository-authority.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'

const hash = (value) => createHash('sha256').update(value).digest('hex')
const execFileAsync = promisify(execFile)
const LIMA_SOURCE_ROOT = '/opt/homebrew/Cellar/lima/2.1.4'
const LIMACTL_SHA256 = '3957f467a116b4adb093cecef7af320b9d4ddec81eb95b613b95ad3c76821320'
const LIMA_WRAPPER_SHA256 = '88aeac60dbcb69ec675c0ce9af24d5f66255f4899fa73320402db57925500832'
const LIMA_GUEST_AGENT_SHA256 = 'f515357036e1b3bc9777c06a35fef60f07242e5ae67a9bd272345a32c5511900'
const canonical = (value) => Array.isArray(value) ? `[${value.map(canonical).join(',')}]` :
  value && typeof value === 'object' ? `{${Object.keys(value).sort().map((key) =>
    `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}` : JSON.stringify(value)
const socketRoot = (root) => `/private/tmp/o4s-${hash(root).slice(0, 16)}`
let captureBuild

test.before(async () => { captureBuild = await buildTestRepositoryCapture() })
test.after(async () => { await captureBuild?.cleanup() })

function syntheticVersionSpawn() {
  return () => {
    const child = new EventEmitter(); child.stdout = new PassThrough(); child.stderr = new PassThrough()
    child.kill = () => true
    queueMicrotask(() => { child.stdout.end('0.84.2\n'); child.stderr.end(); child.emit('close', 0, null) })
    return child
  }
}

async function file(path, value, mode) {
  const bytes = Buffer.isBuffer(value) ? value : Buffer.from(value)
  await writeFile(path, bytes, { mode }); await chmod(path, mode)
  return hash(bytes)
}

async function repositoryAuthority(root, capture) {
  const repositoryRoot = join(root, 'repository')
  await mkdir(repositoryRoot, { mode: 0o700 })
  await file(join(repositoryRoot, 'README.md'), 'synthetic repository\n', 0o644)
  const git = async (...args) => execFileAsync('/usr/bin/git', ['-C', repositoryRoot, ...args], {
    env: { PATH: '/usr/bin:/bin', HOME: root, GIT_CONFIG_NOSYSTEM: '1',
      GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' },
  })
  await git('init', '-q', '-b', 'main')
  await git('add', '--', 'README.md')
  await git('-c', 'user.name=O4 Synthetic', '-c', 'user.email=chora@example.test',
    '-c', 'commit.gpgSign=false', 'commit', '-q', '-m', 'synthetic authority')
  return inspectRepositoryAuthority({ repositoryRoot, capture })
}

async function directoryClosure(root, omit = () => false) {
  const entries = []
  const visit = async (directory, prefix = '') => {
    for (const name of (await readdir(directory)).sort()) {
      const path = join(directory, name); const relative = prefix ? `${prefix}/${name}` : name
      if (omit(relative)) continue
      const info = await lstat(path)
      if (info.isSymbolicLink()) entries.push({ path: relative, type: 'symlink',
        mode: info.mode & 0o777, target: await readlink(path) })
      else if (info.isDirectory()) {
        entries.push({ path: relative, type: 'directory', mode: info.mode & 0o777 })
        await visit(path, relative)
      } else {
        const bytes = await readFile(path)
        entries.push({ path: relative, type: 'file', mode: info.mode & 0o777,
          size: bytes.length, sha256: hash(bytes) })
      }
    }
  }
  await visit(root)
  return hash(canonical({ rootMode: (await lstat(root)).mode & 0o777, entries }))
}

async function portableLimaPrefix(root) {
  const prefixRoot = join(root, 'lima-prefix'); const binRoot = join(prefixRoot, 'bin')
  const shareRoot = join(prefixRoot, 'share'); const limaShareRoot = join(shareRoot, 'lima')
  const templatesRoot = join(limaShareRoot, 'templates')
  for (const path of [prefixRoot, binRoot, shareRoot, limaShareRoot, templatesRoot,
    join(templatesRoot, '_images'), join(templatesRoot, '_default'), join(templatesRoot, 'extras')]) {
    await mkdir(path, { mode: 0o700 }); await chmod(path, 0o700)
  }
  const limactlFile = join(binRoot, 'limactl'); const limaFile = join(binRoot, 'lima')
  const guestAgentFile = join(limaShareRoot, 'lima-guestagent.Linux-aarch64.gz')
  await copyFile(join(LIMA_SOURCE_ROOT, 'bin/limactl'), limactlFile); await chmod(limactlFile, 0o500)
  await copyFile(join(LIMA_SOURCE_ROOT, 'bin/lima'), limaFile); await chmod(limaFile, 0o500)
  await copyFile(join(LIMA_SOURCE_ROOT, 'share/lima/lima-guestagent.Linux-aarch64.gz'),
    guestAgentFile); await chmod(guestAgentFile, 0o400)
  const required = [['README.md', 'README\n'], ['default.yaml', 'default\n'],
    ['_images/ubuntu.yaml', 'ubuntu\n'], ['_default/mounts.yaml', 'mounts\n']]
  const templateLocations = required.filter(([relative]) => relative.endsWith('.yaml'))
    .map(([relative]) => join(templatesRoot, relative))
  let templateBytes = 0
  for (const [relative, bytes] of required) {
    templateBytes += Buffer.byteLength(bytes); await file(join(templatesRoot, relative), bytes, 0o400)
  }
  for (let index = 0; index < 116; index++) {
    const path = join(templatesRoot, 'extras',
      `asset-${String(index).padStart(3, '0')}.yaml`)
    templateLocations.push(path); templateBytes += 1; await file(path, 'x', 0o400)
  }
  const paddingSize = 148_059 - templateBytes
  assert.equal(required.length + 116 + 1, 121); assert.equal(templateBytes + paddingSize, 148_059)
  const paddingFile = join(templatesRoot, 'extras/padding.yaml')
  templateLocations.push(paddingFile)
  await file(paddingFile, Buffer.alloc(paddingSize, 0x70), 0o400)
  const limactlSha256 = hash(await readFile(limactlFile)); const limaSha256 = hash(await readFile(limaFile))
  const guestAgentSha256 = hash(await readFile(guestAgentFile)); const guestAgentSize = (await lstat(guestAgentFile)).size
  assert.equal(limactlSha256, LIMACTL_SHA256); assert.equal(limaSha256, LIMA_WRAPPER_SHA256)
  assert.notEqual(limactlSha256, limaSha256); assert.equal(guestAgentSha256, LIMA_GUEST_AGENT_SHA256)
  assert.equal(guestAgentSize, 7_251_420)
  return { prefixRoot, prefixClosureSha256: await directoryClosure(prefixRoot),
    limactlFile, limactlSha256, limaFile, limaSha256, templatesRoot,
    templatesClosureSha256: await directoryClosure(templatesRoot), guestAgentFile,
    guestAgentSize, guestAgentSha256, templateLocations: templateLocations.sort(),
    infoDigest: hash(canonical({ templates: templateLocations.sort(), guestAgent: guestAgentFile,
      hostOS: 'darwin', hostArch: 'aarch64' })) }
}

async function webClosure(root) {
  const entries = []
  const visit = async (directory, prefix = '') => {
    for (const name of (await readdir(directory)).sort()) {
      const path = join(directory, name); const relative = prefix ? `${prefix}/${name}` : name
      const info = await lstat(path)
      if (info.isDirectory()) await visit(path, relative)
      else { const bytes = await readFile(path); entries.push(`${relative}\0${bytes.length}\0${hash(bytes)}\n`) }
    }
  }
  await visit(root)
  return hash(entries.join(''))
}

async function fixture(t) {
  const root = await realpath(resolve(await mkdtemp(join(tmpdir(), 'o4-real-freezer-')))); await chmod(root, 0o700)
  t.after(() => rm(root, { recursive: true, force: true }))
  const releaseRoot = join(root, 'release'); const sourceRoot = join(root, 'source-bundle')
  const webRoot = join(root, 'web'); const piRoot = join(root, 'pi')
  const playwrightRoot = join(root, 'playwright'); const browserRoot = join(root, 'browser')
  const inputsRoot = join(root, 'inputs'); const engineToolRoot = join(inputsRoot, 'engine-tools')
  for (const path of [releaseRoot, sourceRoot, webRoot, piRoot, playwrightRoot, browserRoot,
    inputsRoot, engineToolRoot])
    await mkdir(path, { mode: 0o700 })

  await mkdir(join(releaseRoot, 'archives'), { mode: 0o700 })
  const archiveA = Buffer.from('managed archive'); const archiveB = Buffer.from('network archive')
  const archiveASha = await file(join(releaseRoot, 'archives/managed.tar'), archiveA, 0o400)
  const archiveBSha = await file(join(releaseRoot, 'archives/network.tar'), archiveB, 0o400)
  const policies = Object.fromEntries(['managed_pi_runtime', 'network_boundary',
    'independent_verifier', 'capability_probe'].map((role) => [role, hash(role)]))
  const release = {
    schema_version: 'chora.release-assets-manifest.v1', spec_sha256: hash('spec'),
    release_id: 'chora-m1-alpha', platform: { os: 'linux', architecture: 'arm64' },
    roles: [
      ['managed_pi_runtime', 'managed-pi-runtime'], ['network_boundary', 'network-boundary'],
      ['independent_verifier', 'managed-pi-runtime', 'managed_pi_runtime'],
      ['capability_probe', 'managed-pi-runtime', 'managed_pi_runtime'],
    ].map(([role, artifact_id, alias_of_role]) => ({ role, artifact_id,
      ...(alias_of_role ? { alias_of_role } : {}),
      policy: { id: `${role}-policy`, path: `policies/${role}.json`, sha256: policies[role] } })),
    artifacts: [
      ['managed-pi-runtime', 'archives/managed.tar', archiveA, archiveASha, `sha256:${'a'.repeat(64)}`],
      ['network-boundary', 'archives/network.tar', archiveB, archiveBSha, `sha256:${'b'.repeat(64)}`],
    ].map(([id, path, bytes, sha256, image]) => ({ id, recipe: {}, context: {},
      recipe_provenance: [], build_inputs: [], entrypoint: ['/entrypoint'], license_inventory: {}, sbom: {},
      image: { local_docker_config_image_id: image,
        archive: { format: 'docker-archive', path, size: bytes.length, sha256 } } })),
  }
  const releaseFile = join(releaseRoot, 'release-manifest.v1.json')
  const releaseBytes = Buffer.from(`${JSON.stringify(release, null, 2)}\n`)
  await file(releaseFile, releaseBytes, 0o444)

  await mkdir(join(sourceRoot, 'source'), { mode: 0o700 })
  const sourceBytes = Buffer.from('package main\n'); const sourceSha = await file(
    join(sourceRoot, 'source/main.go'), sourceBytes, 0o400)
  const sourceAggregate = hash(`main.go\0${'0400'}\0${sourceBytes.length}\0${sourceSha}\n`)
  const sourceManifest = { schema_version: 'chora.local-alpha-source-bundle.v1',
    aggregate_sha256: sourceAggregate,
    source_path_policy: { path: 'distribution/v1/policies/source-path-policy.v1.json', sha256: hash('policy') },
    install_only_projection: { files: 1, paths_sha256: hash('install-paths'), aggregate_sha256: hash('install') },
    model_readable_projection: { files: 1, paths_sha256: hash('model-paths'), aggregate_sha256: hash('model') },
    files: [{ path: 'main.go', size: sourceBytes.length, mode: '0400', sha256: sourceSha }] }
  const sourceFile = join(sourceRoot, 'source-manifest.json')
  const sourceManifestBytes = Buffer.from(`${JSON.stringify(sourceManifest, null, 2)}\n`)
  await file(sourceFile, sourceManifestBytes, 0o444)

  await mkdir(join(webRoot, 'assets'), { mode: 0o700 })
  await file(join(webRoot, 'index.html'), '<html/>', 0o400)
  await file(join(webRoot, 'assets/app.js'), 'app', 0o400)
  await mkdir(join(piRoot, 'dist'), { mode: 0o700 }); await mkdir(join(piRoot, 'lib'), { mode: 0o700 })
  await mkdir(join(piRoot, 'node_modules'), { mode: 0o700 })
  await file(join(piRoot, 'package.json'), JSON.stringify({ name: '@earendil-works/pi-coding-agent', version: '0.84.2' }), 0o400)
  const piExecutable = join(piRoot, 'dist/cli.js'); await file(piExecutable, '#!/usr/bin/env node\n', 0o500)
  await file(join(piRoot, 'lib/runtime.js'), 'export {}\n', 0o400)
  await symlink('../dist', join(piRoot, 'node_modules/.bin'))
  const playwrightCLI = join(playwrightRoot, 'cli.js'); await file(playwrightCLI, 'playwright', 0o400)
  await file(join(playwrightRoot, 'driver.js'), 'driver', 0o400); await symlink('driver.js', join(playwrightRoot, 'driver-link'))
  await mkdir(join(browserRoot, 'chromium'), { mode: 0o700 })
  await file(join(browserRoot, 'chromium/browser'), 'browser', 0o500)
  await symlink('browser', join(browserRoot, 'chromium/current'))

  const pins = {}
  const pinned = async (name, bytes, mode) => {
    const path = join(inputsRoot, name); const sha256 = await file(path, bytes, mode)
    pins[name] = { file: path, sha256 }; return pins[name]
  }
  const product = await pinned('chora', 'product', 0o500)
  const oauthSecret = 'opaque-oauth-secret-never-emitted'; const oauth = await pinned('oauth.json', oauthSecret, 0o600)
  const ca = await pinned('ca.pem', 'CA', 0o400); const node = await pinned('node', 'node', 0o500)
  const runner = await pinned('runner.mjs', 'export {}\n', 0o400)
  const docker = await pinned('engine-tools/docker', 'docker', 0o500)
  const colima = await pinned('engine-tools/colima', 'colima', 0o500)
  const lima = await portableLimaPrefix(root)
  const controller = await pinned('controller',
    await readFile(captureBuild.capture.executableFile), 0o500)
  assert.equal(controller.sha256, captureBuild.capture.executableSha256)
  const ocr = await pinned('vision-ocr', 'ocr', 0o500); const diskImage = await pinned('disk.img', 'disk-image', 0o400)
  const configs = {}
  for (const phase of ['C', 'D', 'E']) configs[phase] = await pinned(`${phase}.config.ts`, `export default '${phase}'\n`, 0o400)
  const sandboxExecutable = { file: '/usr/bin/sandbox-exec', sha256: hash(await readFile('/usr/bin/sandbox-exec')) }
  const preparedOutputRoot = join(root, 'prepared'); const frozenOutputRoot = join(root, 'frozen')
  const endpointSocketPath = join(socketRoot(root), 'c', 'chora-o4-real', 'docker.sock')
  const request = {
    schemaVersion: FREEZE_REQUEST_SCHEMA, status: 'authorized',
    identity: { environmentId: 'env-1', installId: 'install-1', generationId: 'generation-1',
      platform: { os: 'darwin', architecture: 'arm64' } }, preparedOutputRoot,
    release: { root: releaseRoot, manifestFile: releaseFile, manifestSha256: hash(releaseBytes) },
    source: { bundleRoot: sourceRoot, manifestFile: sourceFile, manifestSha256: hash(sourceManifestBytes) },
    product: { binaryFile: product.file, binarySha256: product.sha256, webDirectory: webRoot,
      webClosureSha256: await webClosure(webRoot) },
    pi: { packageRoot: piRoot, executableFile: piExecutable,
      packageClosureSha256: await directoryClosure(piRoot,
        (path) => path === 'node_modules/.bin' || path.startsWith('node_modules/.bin/')),
      executableSha256: hash(await readFile(piExecutable)) },
    oauth, ca, node,
    playwright: { packageRoot: playwrightRoot, packageClosureSha256: await directoryClosure(playwrightRoot),
      cliFile: playwrightCLI, cliSha256: hash(await readFile(playwrightCLI)), browserRoot,
      browserClosureSha256: await directoryClosure(browserRoot) },
    runner, docker, colima, lima: { prefixRoot: lima.prefixRoot,
      prefixClosureSha256: lima.prefixClosureSha256, infoDigest: lima.infoDigest,
      limactlFile: lima.limactlFile,
      limactlSha256: lima.limactlSha256, limaFile: lima.limaFile, limaSha256: lima.limaSha256,
      templatesRoot: lima.templatesRoot, templatesClosureSha256: lima.templatesClosureSha256,
      guestAgentFile: lima.guestAgentFile, guestAgentSize: lima.guestAgentSize,
      guestAgentSha256: lima.guestAgentSha256 },
    diskImage: { ...diskImage, size: (await stat(diskImage.file)).size }, sandboxExecutable,
    controller, ocr,
    engine: { profileName: 'chora-o4-real', contextName: 'colima-chora-o4-real', endpointSocketPath,
      endpointDigest: hash(`unix://${endpointSocketPath}`), cpus: 2, memoryGiB: 4, diskGiB: 20,
      engineMs: 60_000 }, configs,
    vision: { platform: { os: 'darwin', architecture: 'arm64', macOSProductVersion: '15.0',
      macOSBuildVersion: '24A1', darwinSysname: 'Darwin', darwinRelease: '24.0.0',
      darwinVersion: 'Darwin Kernel Version', darwinMachine: 'arm64' },
      vision: { bundleIdentifier: 'com.apple.Vision', bundleVersion: '1', infoPlistSha256: hash('vision'),
        supportedRecognitionRevisions: [1, 2, 3], requestedRecognitionRevision: 3,
        recognitionLevel: 'accurate', recognitionLanguage: 'en-US', usesLanguageCorrection: true,
        confidenceThreshold: 0.5 } },
  }
  const requestFile = join(root, 'freeze-request.json')
  const persist = async (value = request, path = requestFile) => {
    const bytes = Buffer.from(`${JSON.stringify(value, null, 2)}\n`)
    await rm(path, { force: true }); await file(path, bytes, 0o400)
    return hash(bytes)
  }
  const repository = await repositoryAuthority(root, {
    executableFile: controller.file, executableSha256: controller.sha256,
  })
  return { root, request, requestFile, persist, frozenOutputRoot, preparedOutputRoot,
    oauthSecret, pins, product, materializedRoot: join(root, 'materialized'),
    engineTools: { root: engineToolRoot, docker: docker.file }, lima, repository }
}

function dynamicValue(path, root, repository) {
  const key = path.split('.').at(-1)
  if (path === 'candidateConfig.paths.repositoryRoot' ||
    path === 'totalManifest.repository.root') return repository.repositoryRoot
  if (path === 'candidateConfig.authority.repositoryCommit' ||
    path === 'totalManifest.repository.commit') return repository.repositoryCommit
  if (path === 'candidateConfig.authority.repositoryClosureSha256' ||
    path === 'totalManifest.repository.closureSha256') return repository.repositoryClosureSha256
  if (path === 'totalManifest.runner.controlSocketPath')
    return join(socketRoot(root), 'r', 'o4-runner.sock')
  if (path === 'totalManifest.runner.sessionAuthorityFile')
    return join(socketRoot(root), 'r', 'session-authority.json')
  if (key === 'runnerControlRoot') return join(socketRoot(root), 'r')
  if (key === 'colimaHome') return join(socketRoot(root), 'c')
  if (key === 'temporaryRoot') return join(socketRoot(root), 't')
  if (key === 'preflightFile' || key === 'zeroImagePreflightFile')
    return join(root, 'dynamic', 'engine-preflight.json')
  if (key === 'tupleFile') return join(root, 'dynamic', 'tuple.json')
  if (key === 'installRoot') return join(root, 'dynamic', 'install')
  if (key === 'dataRoot') return join(root, 'dynamic', 'data')
  if (key === 'stateRoot') return join(root, 'dynamic', 'state')
  if (key === 'receiptDirectory' || key === 'receiptRoot') return join(root, 'dynamic', 'receipts')
  if (key === 'acceptanceClass') return 'real_acceptance'
  if (key === 'status') return 'authorized'
  if (key === 'modelURL') return 'https://model.invalid/v1'
  if (key === 'port') return 8443
  if (key.endsWith('Ms')) return 5_000
  if (/Sha256|Digest$/i.test(key)) return hash(path)
  if (key === 'configSha256') return null
  if (/^(?:cpus|memoryGiB|diskGiB)$/.test(key)) return 2
  if (/File$|Root$|Directory$|Path$/.test(key) || key.includes('Socket'))
    return join(root, 'dynamic', path.replaceAll('.', '-'))
  if (key.includes('BASE_URL')) return 'http://127.0.0.1:9999'
  return `value-${hash(path).slice(0, 24)}`
}

function resolutionsFor(template, path, root, repository) {
  if (template === null || template === 'static_unresolved') {
    if (path === 'candidateConfig.paths.candidateManifestFile' ||
      path === 'candidateConfig.paths.offlineBundleEvidenceFile' ||
      path === 'candidateConfig.authority.candidateManifestSha256' ||
      path === 'candidateConfig.authority.offlineBundleEvidenceSha256' ||
      path === 'totalManifest.selfDigest' || path === 'totalManifest.candidate.configSha256' ||
      path === 'totalManifest.candidate.tupleIdentity' ||
      path === 'totalManifest.phases.D.environment.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE' ||
      path === 'totalManifest.phases.D.environment.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_SHA256' ||
      path === 'totalManifest.phases.D.environment.CHORA_O4_RECOVERY_RESIDUE_BINDING_DIGEST' ||
      path === 'totalManifest.phases.E.environment.CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE' ||
      path === 'totalManifest.phases.E.environment.CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE') return undefined
    return dynamicValue(path, root, repository)
  }
  if (!template || typeof template !== 'object' || Array.isArray(template)) return undefined
  const result = {}
  for (const [key, value] of Object.entries(template)) {
    const resolved = resolutionsFor(value, `${path}.${key}`, root, repository)
    if (resolved !== undefined) result[key] = resolved
  }
  return Object.keys(result).length ? result : undefined
}

test('stage-static emits exact owner-only bindings without secret, registry, model, network, or execution claims', async (t) => {
  const fx = await fixture(t); const requestSha256 = await fx.persist()
  assert.equal(fx.request.lima.limactlFile, fx.lima.limactlFile)
  assert.equal(fx.request.lima.limaFile, fx.lima.limaFile)
  assert.equal(fx.request.docker.file, fx.engineTools.docker)
  const limaInfo = await lstat(fx.lima.limaFile)
  const limactlInfo = await lstat(fx.lima.limactlFile)
  for (const path of [fx.lima.limaFile, fx.lima.limactlFile, fx.engineTools.docker]) {
    const info = await lstat(path)
    assert.equal(info.isFile(), true); assert.equal(info.mode & 0o777, 0o500); assert.equal(info.nlink, 1)
  }
  assert.equal(hash(await readFile(fx.lima.limactlFile)), LIMACTL_SHA256)
  assert.equal(hash(await readFile(fx.lima.limaFile)), LIMA_WRAPPER_SHA256)
  assert.notEqual(hash(await readFile(fx.lima.limaFile)), hash(await readFile(fx.lima.limactlFile)))
  assert.notEqual(limaInfo.ino, limactlInfo.ino)
  const result = await stageO4RealInputs({ requestFile: fx.requestFile, requestSha256,
    outputRoot: fx.frozenOutputRoot })
  assert.equal((await stat(fx.frozenOutputRoot)).mode & 0o777, 0o700)
  for (const name of ['frozen-inputs.json', 'static-preparation-request.json',
    'vision-engine-binding.json', 'ocr-deny-network.sb', 'engine-local-only.sb'])
    assert.equal((await stat(join(fx.frozenOutputRoot, name))).mode & 0o777, 0o400)
  const frozenBytes = await readFile(result.manifestFile); const frozen = JSON.parse(frozenBytes)
  assert.equal(result.manifestSha256, hash(frozenBytes)); assert.equal(frozen.networkUsed, false)
  assert.equal(frozen.executablesSpawned, true)
  assert.equal(JSON.stringify(frozen).includes(fx.oauthSecret), false)
  const binding = JSON.parse(await readFile(frozen.generated.ocrEngineBinding.file, 'utf8'))
  assert.equal(binding.modelBytes, 'opaque_unavailable'); assert.equal(binding.engine, 'system_managed_ocr_engine')
  const withoutDigest = { ...binding }; delete withoutDigest.bindingDigest
  assert.equal(binding.bindingDigest, hash(canonical(withoutDigest)))
  const staticRequest = JSON.parse(await readFile(result.staticPreparationRequestFile, 'utf8'))
  assert.equal(frozen.bindings.engine.contextName, `colima-${frozen.bindings.engine.profileName}`)
  assert.equal(staticRequest.engine.contextName, `colima-${staticRequest.engine.profileName}`)
  assert.equal(staticRequest.engine.limaPrefixClosureSha256, fx.lima.prefixClosureSha256)
  assert.equal(staticRequest.engine.limaInfoDigest, fx.lima.infoDigest)
  assert.deepEqual(staticRequest.ocr, { executableFile: fx.request.ocr.file,
    executableSha256: fx.request.ocr.sha256, engineBindingFile: binding.sandbox.profileFile.replace(
      'ocr-deny-network.sb', 'vision-engine-binding.json'), engineBindingSha256: hash(await readFile(
      join(fx.frozenOutputRoot, 'vision-engine-binding.json'))),
    sandboxExecutableFile: fx.request.sandboxExecutable.file,
    sandboxExecutableSha256: fx.request.sandboxExecutable.sha256,
    sandboxProfileFile: join(fx.frozenOutputRoot, 'ocr-deny-network.sb'),
    sandboxProfileSha256: hash(await readFile(join(fx.frozenOutputRoot, 'ocr-deny-network.sb'))), language: 'eng' })
  const source = await readFile(new URL('./o4-real-input-freezer.mjs', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /node:child_process|\bspawn\s*\(/)
})

test('stage-static rejects portable Lima prefix drift before output', async (t) => {
  const cases = [
    ['prefix authority field', async (fx) => { fx.request.lima.prefixClosureSha256 = hash('drift') }],
    ['info README inclusion', async (fx) => {
      fx.request.lima.infoDigest = hash(canonical({
        templates: [...fx.lima.templateLocations, join(fx.lima.templatesRoot, 'README.md')].sort(),
        guestAgent: fx.lima.guestAgentFile, hostOS: 'darwin', hostArch: 'aarch64',
      }))
    }],
    ['info template set drift', async (fx) => {
      fx.request.lima.infoDigest = hash(canonical({
        templates: [...fx.lima.templateLocations.slice(0, -1),
          join(fx.lima.templatesRoot, 'extras/not-authorized.yaml')].sort(),
        guestAgent: fx.lima.guestAgentFile, hostOS: 'darwin', hostArch: 'aarch64',
      }))
    }],
    ['missing', async (fx) => unlink(join(fx.lima.templatesRoot, 'default.yaml'))],
    ['guest-agent missing', async (fx) => unlink(fx.lima.guestAgentFile)],
    ['extra', async (fx) => file(join(fx.lima.templatesRoot, 'extra.yaml'), 'x', 0o400)],
    ['link', async (fx) => symlink('default.yaml', join(fx.lima.templatesRoot, 'linked.yaml'))],
    ['mode', async (fx) => chmod(join(fx.lima.templatesRoot, 'default.yaml'), 0o600)],
    ['hash', async (fx) => {
      const path = join(fx.lima.templatesRoot, 'extras/asset-000.yaml')
      await chmod(path, 0o600); await writeFile(path, 'y'); await chmod(path, 0o400)
    }],
    ['topology', async (fx) => { fx.request.lima.limaFile = fx.lima.limactlFile }],
  ]
  for (const [name, mutate] of cases) await t.test(name, async (t) => {
    const fx = await fixture(t); await mutate(fx); const sha = await fx.persist()
    await assert.rejects(stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot }),
    /canonical|prefix|template|topology|symlink|SHA-256|ENOENT|info/i)
    await assert.rejects(lstat(fx.frozenOutputRoot), { code: 'ENOENT' })
  })
  await t.test('TOCTOU', async (t) => {
    const fx = await fixture(t); const sha = await fx.persist()
    await assert.rejects(stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot }, { beforeCommit: async () => chmod(
        join(fx.lima.templatesRoot, 'default.yaml'), 0o600) }), /frozen directory entry TOCTOU drifted/)
    await assert.rejects(lstat(fx.frozenOutputRoot), { code: 'ENOENT' })
  })
})

test('stage-static rejects non-derived Docker contexts before freezing bindings', async (t) => {
  const cases = [
    ['plain profile', (fx) => { fx.request.engine.contextName = fx.request.engine.profileName }],
    ['arbitrary context', (fx) => { fx.request.engine.contextName = 'colima-unrelated-profile' }],
  ]
  for (const [name, mutate] of cases) await t.test(name, async (t) => {
    const fx = await fixture(t); mutate(fx); const sha = await fx.persist()
    await assert.rejects(stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot }), /derived Docker context/)
    await assert.rejects(lstat(fx.frozenOutputRoot), { code: 'ENOENT' })
  })
})

test('stage-static is deterministic and fails cleanly on mode, hash, TOCTOU, legacy, secret, and overwrite drift', async (t) => {
  await t.test('deterministic', async (t) => {
    const fx = await fixture(t); const sha = await fx.persist()
    const first = await stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot }); const bytes = await readFile(first.manifestFile)
    await rm(fx.frozenOutputRoot, { recursive: true })
    const second = await stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot })
    assert.deepEqual(await readFile(second.manifestFile), bytes)
  })
  const cases = [
    ['mode', async (fx) => chmod(fx.request.controller.file, 0o700), /controller must be one exact 500/],
    ['hash', async (fx) => { fx.request.diskImage.sha256 = hash('wrong'); await fx.persist() }, /disk image SHA-256 drifted/],
    ['uppercase profile', async (fx) => {
      fx.request.engine.profileName = 'Chora-o4-real'
      fx.request.engine.contextName = 'colima-Chora-o4-real'
      await fx.persist()
    }, /profile\/derived Docker context\/endpoint drifted/],
    ['legacy', async (fx) => { fx.request.registryEvidenceFile = '/legacy'; await fx.persist() }, /keys drifted|forbidden legacy/],
    ['secret', async (fx) => { fx.request.vision.platform.darwinVersion = 'Bearer exposed'; await fx.persist() }, /contains secret bytes/],
  ]
  for (const [name, mutate, pattern] of cases) await t.test(name, async (t) => {
    const fx = await fixture(t); await mutate(fx); const sha = await fx.persist()
    await assert.rejects(stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot }), pattern)
    await assert.rejects(lstat(fx.frozenOutputRoot), { code: 'ENOENT' })
  })
  await t.test('TOCTOU', async (t) => {
    const fx = await fixture(t); const sha = await fx.persist()
    await assert.rejects(stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot }, { beforeCommit: async () => chmod(fx.request.controller.file, 0o700) }),
    /TOCTOU drifted/)
    await assert.rejects(lstat(fx.frozenOutputRoot), { code: 'ENOENT' })
  })
  await t.test('ordinary partial failure removes owned staging', async (t) => {
    const fx = await fixture(t); const sha = await fx.persist(); let staging
    await assert.rejects(stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot }, { beforeCommit: async (paths) => {
        staging = paths.staging
        throw new Error('injected partial failure')
      } }), /injected partial failure/)
    await assert.rejects(lstat(staging), { code: 'ENOENT' })
    await assert.rejects(lstat(fx.frozenOutputRoot), { code: 'ENOENT' })
  })
  await t.test('cleanup refuses a replaced staging directory and preserves both failures', async (t) => {
    const fx = await fixture(t); const sha = await fx.persist(); let staging; let displaced
    await assert.rejects(stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot }, { beforeCommit: async (paths) => {
        staging = paths.staging; displaced = `${staging}.owned`
        await rename(staging, displaced)
        await mkdir(staging, { mode: 0o700 })
        await writeFile(join(staging, 'foreign-marker'), 'foreign\n', { mode: 0o600 })
        throw new Error('injected failure after staging replacement')
      } }), (error) => {
      assert.equal(error instanceof AggregateError, true)
      assert.match(error.message, /owned cleanup path identity drifted/)
      assert.equal(error.errors.some((item) => /injected failure after staging replacement/.test(item.message)), true)
      assert.match(error.cause?.message ?? '', /injected failure after staging replacement/)
      return true
    })
    assert.equal(await readFile(join(staging, 'foreign-marker'), 'utf8'), 'foreign\n')
    assert.equal((await lstat(displaced)).isDirectory(), true)
    await assert.rejects(lstat(fx.frozenOutputRoot), { code: 'ENOENT' })
  })
  await t.test('publication refuses a swapped staging source', async (t) => {
    const fx = await fixture(t); const sha = await fx.persist(); let staging; let displaced
    await assert.rejects(stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot }, { publishOwnedPath: { beforeSpawn: async ({ request }) => {
        staging = request.sourcePath; displaced = `${staging}.owned`
        await rename(staging, displaced); await mkdir(staging, { mode: 0o700 })
        await writeFile(join(staging, 'foreign-marker'), 'foreign\n', { mode: 0o600 })
      } } }), (error) => {
      assert.equal(error instanceof AggregateError, true)
      assert.match(error.errors[0]?.message ?? '', /owned-path controller failed/)
      assert.match(error.errors[1]?.message ?? '', /owned cleanup path identity drifted/)
      assert.match(error.cause?.message ?? '', /owned-path controller failed/)
      return true
    })
    assert.equal(await readFile(join(staging, 'foreign-marker'), 'utf8'), 'foreign\n')
    assert.equal((await lstat(displaced)).isDirectory(), true)
    await assert.rejects(lstat(fx.frozenOutputRoot), { code: 'ENOENT' })
  })
  await t.test('publication refuses a racing output target and cleans owned staging', async (t) => {
    const fx = await fixture(t); const sha = await fx.persist(); let staging
    await assert.rejects(stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot }, { publishOwnedPath: { beforeSpawn: async ({ request }) => {
        staging = request.sourcePath; await mkdir(request.targetPath, { mode: 0o700 })
        await writeFile(join(request.targetPath, 'foreign-marker'), 'foreign\n', { mode: 0o600 })
      } } }), /owned-path controller failed/)
    await assert.rejects(lstat(staging), { code: 'ENOENT' })
    assert.equal(await readFile(join(fx.frozenOutputRoot, 'foreign-marker'), 'utf8'), 'foreign\n')
  })
  await t.test('overwrite', async (t) => {
    const fx = await fixture(t); const sha = await fx.persist(); await mkdir(fx.frozenOutputRoot, { mode: 0o700 })
    await assert.rejects(stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: sha,
      outputRoot: fx.frozenOutputRoot }), /already exists/)
  })
})

test('owner-only JSON publication closes source and target races at the native spawn boundary', async (t) => {
  const root = await realpath(resolve(await mkdtemp(join(tmpdir(), 'o4-json-publisher-'))))
  await chmod(root, 0o700); t.after(() => rm(root, { recursive: true, force: true }))
  const publish = (name, dependencies) => publishOwnerOnlyJSON0400({
    outputFile: join(root, `${name}.json`), value: { status: 'test' },
    capability: captureBuild.capture,
  }, dependencies)

  let temporary; let displacedTemporary
  await assert.rejects(publish('source-after-check-swap', {
    publishOwnedPath: { beforeSpawn: async ({ protocol, request }) => {
      assert.equal(protocol, 'chora.m1-o4-owned-path-publish.v1')
      temporary = request.sourcePath; displacedTemporary = `${temporary}.owned`
      await rename(temporary, displacedTemporary)
      await writeFile(temporary, 'foreign temporary after JavaScript check\n', { mode: 0o600 })
    } },
  }), (error) => {
    assert.equal(error instanceof AggregateError, true)
    assert.match(error.cause?.message ?? '', /owned-path controller failed/)
    return true
  })
  assert.equal(await readFile(temporary, 'utf8'), 'foreign temporary after JavaScript check\n')
  assert.equal((await lstat(displacedTemporary)).isFile(), true)
  await assert.rejects(lstat(join(root, 'source-after-check-swap.json')), { code: 'ENOENT' })

  const racedOutput = join(root, 'target-race.json')
  let racedSource
  await assert.rejects(publish('target-race', {
    publishOwnedPath: { beforeSpawn: async ({ request }) => {
      racedSource = request.sourcePath
      await writeFile(request.targetPath, 'foreign target won race\n', { mode: 0o600 })
    } },
  }), (error) => {
    assert.equal(error instanceof AggregateError, true)
    assert.match(error.cause?.message ?? '', /owned-path controller failed/)
    return true
  })
  assert.equal(await readFile(racedOutput, 'utf8'), 'foreign target won race\n')
  await assert.rejects(lstat(racedSource), { code: 'ENOENT' })
})

test('finalize accepts exact canonical pre-bound output slots and materializer accepts locally', async (t) => {
  const fx = await fixture(t); const freezeSha = await fx.persist()
  const frozen = await stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: freezeSha,
    outputRoot: fx.frozenOutputRoot })
  const prepared = await prepareO4StaticInputs({ requestFile: frozen.staticPreparationRequestFile,
    requestSha256: frozen.staticPreparationRequestSha256, outputRoot: fx.preparedOutputRoot },
  { spawnVersionProbe: syntheticVersionSpawn() })
  const candidateTemplate = JSON.parse(await readFile(join(fx.preparedOutputRoot,
    'candidate-config.template.json'), 'utf8'))
  const totalTemplate = JSON.parse(await readFile(join(fx.preparedOutputRoot,
    'total-manifest.template.json'), 'utf8'))
  const finalization = { schemaVersion: FINALIZE_REQUEST_SCHEMA, status: 'authorized',
    frozenManifestFile: frozen.manifestFile, frozenManifestSha256: frozen.manifestSha256,
    preparedOutputRoot: fx.preparedOutputRoot,
    preparedManifestSha256: hash(await readFile(prepared.manifestFile)),
    preparedClosureSha256: await digestO4PreparedClosure(fx.preparedOutputRoot),
    outputRoot: fx.materializedRoot,
    resolutions: { candidateConfig: resolutionsFor(candidateTemplate, 'candidateConfig', fx.root,
      fx.repository), totalManifest: resolutionsFor(totalTemplate, 'totalManifest', fx.root,
      fx.repository) } }
  finalization.resolutions.candidateConfig.paths.offlineBundleEvidenceFile =
    join(fx.materializedRoot, 'offline-bundle-evidence.json')
  finalization.resolutions.totalManifest.phases.D.environment
    .CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE = join(fx.materializedRoot, 'candidate-config.json')
  finalization.resolutions.totalManifest.phases.E.environment
    .CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE = join(fx.materializedRoot, 'candidate-manifest.json')
  finalization.resolutions.totalManifest.phases.E.environment
    .CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE =
      join(fx.materializedRoot, 'offline-bundle-evidence.json')
  const finalizationFile = join(fx.root, 'finalization.json'); const finalizationBytes = Buffer.from(
    `${JSON.stringify(finalization, null, 2)}\n`); await file(finalizationFile, finalizationBytes, 0o400)
  const outputFile = join(fx.root, 'materialization-request.json')
  const result = await finalizeO4MaterializationRequest({ requestFile: finalizationFile,
    requestSha256: hash(finalizationBytes), outputFile })
  assert.equal(result.networkUsed, false); assert.equal(result.executablesSpawned, true)
  assert.equal((await stat(outputFile)).mode & 0o777, 0o400)
  assert.equal((await readFile(outputFile, 'utf8')).includes(fx.oauthSecret), false)
  const materializationRequest = JSON.parse(await readFile(outputFile, 'utf8'))
  assert.equal(materializationRequest.outputRoot, fx.materializedRoot)
  assert.equal(materializationRequest.candidateConfigTemplate.paths.candidateManifestFile,
    join(fx.materializedRoot, 'candidate-manifest.json'))
  assert.equal(materializationRequest.candidateConfigTemplate.paths.offlineBundleEvidenceFile,
    join(fx.materializedRoot, 'offline-bundle-evidence.json'))
  assert.equal(materializationRequest.totalManifestTemplate.candidate.configFile,
    join(fx.materializedRoot, 'candidate-config.json'))
  assert.equal(materializationRequest.totalManifestTemplate.phases.D.environment
    .CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE, join(fx.materializedRoot, 'candidate-config.json'))
  assert.equal(materializationRequest.totalManifestTemplate.phases.E.environment
    .CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE, join(fx.materializedRoot, 'candidate-manifest.json'))
  assert.equal(materializationRequest.totalManifestTemplate.phases.E.environment
    .CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE,
  join(fx.materializedRoot, 'offline-bundle-evidence.json'))
  assert.deepEqual(materializationRequest.totalManifestTemplate.repository, {
    root: fx.repository.repositoryRoot, commit: fx.repository.repositoryCommit,
    closureSha256: fx.repository.repositoryClosureSha256,
  })
  assert.equal(materializationRequest.candidateConfigTemplate.paths.repositoryRoot,
    fx.repository.repositoryRoot)
  assert.equal(materializationRequest.candidateConfigTemplate.authority.repositoryCommit,
    fx.repository.repositoryCommit)
  assert.equal(materializationRequest.candidateConfigTemplate.authority.repositoryClosureSha256,
    fx.repository.repositoryClosureSha256)
  assert.equal(materializationRequest.candidateManifestTemplate.repositoryCommit, null)
  assert.equal(materializationRequest.candidateManifestTemplate.repositoryClosureSha256, null)
  const materialized = await materializeO4OfflineRelease({ requestFile: outputFile,
    requestSha256: result.sha256, outputRoot: fx.materializedRoot })
  assert.equal(materialized.status, 'passed')
  await assert.rejects(finalizeO4MaterializationRequest({ requestFile: finalizationFile,
    requestSha256: hash(finalizationBytes), outputFile }), /already exists/)
})

test('finalize rejects static overwrite, unknown resolution, secret, and TOCTOU before publication', async (t) => {
  const fx = await fixture(t); const freezeSha = await fx.persist()
  const frozen = await stageO4RealInputs({ requestFile: fx.requestFile, requestSha256: freezeSha,
    outputRoot: fx.frozenOutputRoot })
  const prepared = await prepareO4StaticInputs({ requestFile: frozen.staticPreparationRequestFile,
    requestSha256: frozen.staticPreparationRequestSha256, outputRoot: fx.preparedOutputRoot },
  { spawnVersionProbe: syntheticVersionSpawn() })
  const candidate = JSON.parse(await readFile(join(fx.preparedOutputRoot, 'candidate-config.template.json')))
  const total = JSON.parse(await readFile(join(fx.preparedOutputRoot, 'total-manifest.template.json')))
  const base = { schemaVersion: FINALIZE_REQUEST_SCHEMA, status: 'authorized',
    frozenManifestFile: frozen.manifestFile, frozenManifestSha256: frozen.manifestSha256,
    preparedOutputRoot: fx.preparedOutputRoot, preparedManifestSha256: hash(await readFile(prepared.manifestFile)),
    preparedClosureSha256: await digestO4PreparedClosure(fx.preparedOutputRoot),
    outputRoot: fx.materializedRoot,
    resolutions: { candidateConfig: resolutionsFor(candidate, 'candidateConfig', fx.root,
      fx.repository), totalManifest: resolutionsFor(total, 'totalManifest', fx.root,
      fx.repository) } }
  const run = async (value, name, dependencies = {}) => {
    const path = join(fx.root, `${name}.json`); const bytes = Buffer.from(`${JSON.stringify(value, null, 2)}\n`)
    await file(path, bytes, 0o400)
    return finalizeO4MaterializationRequest({ requestFile: path, requestSha256: hash(bytes),
      outputFile: join(fx.root, `${name}-output.json`) }, dependencies)
  }
  const overwrite = structuredClone(base); overwrite.resolutions.totalManifest.playwright = { nodeFile: '/splice' }
  await assert.rejects(run(overwrite, 'overwrite'), /static template overwrite forbidden/)
  const unknown = structuredClone(base); unknown.resolutions.candidateConfig.unknown = 'x'
  await assert.rejects(run(unknown, 'unknown'), /unknown field/)
  const repositoryOmission = structuredClone(base)
  delete repositoryOmission.resolutions.candidateConfig.authority.repositoryCommit
  await assert.rejects(run(repositoryOmission, 'repository-omission'),
    /missing dynamic resolution candidateConfig.authority.repositoryCommit/)
  const repositorySplice = structuredClone(base)
  repositorySplice.resolutions.totalManifest.repository.commit = '0'.repeat(40)
  await assert.rejects(run(repositorySplice, 'repository-splice'),
    /candidate\/total repository authority drifted/)
  const repositoryUnknown = structuredClone(base)
  repositoryUnknown.resolutions.totalManifest.repository.unbound = 'splice'
  await assert.rejects(run(repositoryUnknown, 'repository-unknown'), /unknown field/)
  const outputSplice = structuredClone(base)
  outputSplice.resolutions.totalManifest.phases.D.environment
    .CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE = join(fx.root, 'retired-candidate-config.json')
  await assert.rejects(run(outputSplice, 'output-splice'), /materialized output path splice/)
  const secret = structuredClone(base); secret.resolutions.candidateConfig.private.modelURL = 'Bearer leaked'
  await assert.rejects(run(secret, 'secret'), /contains secret bytes/)
  const toctou = structuredClone(base); const toctouFile = join(fx.root, 'toctou.json')
  const toctouBytes = Buffer.from(`${JSON.stringify(toctou, null, 2)}\n`); await file(toctouFile, toctouBytes, 0o400)
  await assert.rejects(finalizeO4MaterializationRequest({ requestFile: toctouFile,
    requestSha256: hash(toctouBytes), outputFile: join(fx.root, 'toctou-output.json') }, {
    beforeCommit: async () => chmod(toctouFile, 0o600),
  }), /TOCTOU drifted/)
  await assert.rejects(lstat(join(fx.root, 'toctou-output.json')), { code: 'ENOENT' })
  const repositoryTOCTOU = structuredClone(base)
  const repositoryFile = join(fx.repository.repositoryRoot, 'README.md')
  await assert.rejects(run(repositoryTOCTOU, 'repository-toctou', { beforeCommit: async () => {
    await chmod(repositoryFile, 0o600)
    await writeFile(repositoryFile, 'repository changed after inspection\n')
    await chmod(repositoryFile, 0o644)
  } }), /repository .*drifted|repository.*does not match|Git inspection failed/i)
  await assert.rejects(lstat(join(fx.root, 'repository-toctou-output.json')), { code: 'ENOENT' })
})
