import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { EventEmitter } from 'node:events'
import { chmod, copyFile, link, lstat, mkdir, mkdtemp, readFile, readdir, readlink,
  realpath, rename, rm, symlink, unlink, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { PassThrough } from 'node:stream'
import { prepareO4StaticInputs, REQUEST_SCHEMA, RESULT_SCHEMA } from './o4-inputs-preparer.mjs'
import { materializeO4OfflineRelease, MATERIALIZATION_REQUEST_SCHEMA } from './o4-local-offline-materializer.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'

const hash = (bytes) => createHash('sha256').update(bytes).digest('hex')
const LIMA_SOURCE_ROOT = '/opt/homebrew/Cellar/lima/2.1.4'
const LIMACTL_SHA256 = '3957f467a116b4adb093cecef7af320b9d4ddec81eb95b613b95ad3c76821320'
const LIMA_WRAPPER_SHA256 = '88aeac60dbcb69ec675c0ce9af24d5f66255f4899fa73320402db57925500832'
const LIMA_GUEST_AGENT_SHA256 = 'f515357036e1b3bc9777c06a35fef60f07242e5ae67a9bd272345a32c5511900'
const canonical = (value) => Array.isArray(value) ? `[${value.map(canonical).join(',')}]` :
  value && typeof value === 'object' ? `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}` : JSON.stringify(value)
let captureBuild

test.before(async () => { captureBuild = await buildTestRepositoryCapture() })
test.after(async () => { await captureBuild?.cleanup() })

function syntheticVersionSpawn({ stdout = '0.84.2\n', stderr = '', code = 0,
  onSpawn = () => {}, beforeClose = async () => {}, onClose = () => {},
  autoClose = true, emitError } = {}) {
  return (executable, args, options) => {
    onSpawn(executable, args, options)
    const child = new EventEmitter(); child.stdout = new PassThrough(); child.stderr = new PassThrough()
    let closed = false; let closing = false
    const close = (exitCode, signal = null) => {
      if (closed) return
      closed = true; onClose(exitCode, signal); child.emit('close', exitCode, signal)
    }
    const finishChild = async (exitCode, signal = null) => {
      if (closed || closing) return
      closing = true
      try { await beforeClose(executable, args, options) }
      catch (error) { child.emit('error', error) }
      close(exitCode, signal)
    }
    child.kill = (signal) => {
      queueMicrotask(() => { void finishChild(null, signal) }); return true
    }
    queueMicrotask(() => {
      if (emitError) { child.emit('error', emitError); return }
      if (!autoClose) return
      child.stdout.end(stdout); child.stderr.end(stderr); void finishChild(code)
    })
    return child
  }
}

async function file(path, bytes, mode) {
  await mkdir(join(path, '..'), { recursive: true, mode: 0o700 }).catch(() => {})
  await writeFile(path, bytes, { mode }); await chmod(path, mode)
  return hash(Buffer.from(bytes))
}

async function directoryClosure(root, { omit = () => false } = {}) {
  const rootInfo = await lstat(root); const entries = []
  const visit = async (directory, prefix = '') => {
    for (const name of (await readdir(directory)).sort()) {
      const relative = prefix ? `${prefix}/${name}` : name
      if (omit(relative)) continue
      const path = join(directory, name); const info = await lstat(path)
      if (info.isSymbolicLink()) entries.push({ path: relative, type: 'symlink', mode: info.mode & 0o777, target: await readlink(path) })
      else if (info.isDirectory()) { entries.push({ path: relative, type: 'directory', mode: info.mode & 0o777 }); await visit(path, relative) }
      else { const bytes = await readFile(path); entries.push({ path: relative, type: 'file', mode: info.mode & 0o777, size: bytes.length, sha256: hash(bytes) }) }
    }
  }
  await visit(root)
  return hash(canonical({ rootMode: rootInfo.mode & 0o777, entries }))
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
    guestAgentSize, guestAgentSha256,
    templateLocations: templateLocations.sort(),
    infoDigest: hash(canonical({ templates: templateLocations.sort(), guestAgent: guestAgentFile,
      hostOS: 'darwin', hostArch: 'aarch64' })) }
}

async function webAggregate(root) {
  const entries = []
  const visit = async (directory, prefix = '') => {
    for (const name of (await readdir(directory)).sort()) {
      const path = join(directory, name); const relative = prefix ? `${prefix}/${name}` : name; const info = await lstat(path)
      if (info.isDirectory()) await visit(path, relative)
      else { const bytes = await readFile(path); entries.push(`${relative}\0${bytes.length}\0${hash(bytes)}\n`) }
    }
  }
  await visit(root); return hash(entries.join(''))
}

async function fixture(t) {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'chora-o4-inputs-'))); await chmod(root, 0o700)
  t.after(() => rm(root, { recursive: true, force: true }))
  const releaseRoot = join(root, 'release'); const sourceRoot = join(root, 'source-bundle'); const productRoot = join(root, 'product')
  const piRoot = join(root, 'pi'); const playwrightRoot = join(root, 'playwright'); const browserRoot = join(root, 'browser'); const inputsRoot = join(root, 'inputs')
  const engineToolRoot = join(inputsRoot, 'engine-tools')
  for (const path of [releaseRoot, sourceRoot, productRoot, piRoot, playwrightRoot, browserRoot,
    inputsRoot, engineToolRoot]) await mkdir(path, { mode: 0o700 })

  const archiveA = Buffer.from('managed archive'); const archiveB = Buffer.from('network archive')
  await mkdir(join(releaseRoot, 'archives'), { mode: 0o700 })
  await file(join(releaseRoot, 'archives/managed.tar'), archiveA, 0o400); await file(join(releaseRoot, 'archives/network.tar'), archiveB, 0o400)
  const imageA = `sha256:${'a'.repeat(64)}`; const imageB = `sha256:${'b'.repeat(64)}`
  const releaseManifest = {
    schema_version: 'chora.release-assets-manifest.v1', spec_sha256: '1'.repeat(64), release_id: 'test-release', platform: { os: 'linux', architecture: 'arm64' },
    roles: [
      { role: 'managed_pi_runtime', artifact_id: 'managed-pi-runtime', policy: { id: 'managed', path: 'policies/managed.json', sha256: '2'.repeat(64) } },
      { role: 'network_boundary', artifact_id: 'network-boundary', policy: { id: 'network', path: 'policies/network.json', sha256: '3'.repeat(64) } },
      { role: 'independent_verifier', artifact_id: 'managed-pi-runtime', alias_of_role: 'managed_pi_runtime', policy: { id: 'verify', path: 'policies/verify.json', sha256: '4'.repeat(64) } },
      { role: 'capability_probe', artifact_id: 'managed-pi-runtime', alias_of_role: 'managed_pi_runtime', policy: { id: 'probe', path: 'policies/probe.json', sha256: '5'.repeat(64) } },
    ],
    artifacts: [
      { id: 'managed-pi-runtime', recipe: {}, context: {}, recipe_provenance: [], build_inputs: [], runtime: {}, entrypoint: ['pi'], license_inventory: {}, sbom: {}, image: { local_docker_config_image_id: imageA, archive: { format: 'docker-archive', path: 'archives/managed.tar', size: archiveA.length, sha256: hash(archiveA) } } },
      { id: 'network-boundary', recipe: {}, context: {}, recipe_provenance: [], build_inputs: [], entrypoint: ['boundary'], license_inventory: {}, sbom: {}, image: { local_docker_config_image_id: imageB, archive: { format: 'docker-archive', path: 'archives/network.tar', size: archiveB.length, sha256: hash(archiveB) } } },
    ],
  }
  const releaseBytes = Buffer.from(`${JSON.stringify(releaseManifest, null, 2)}\n`); const releaseFile = join(releaseRoot, 'release-manifest.v1.json'); await file(releaseFile, releaseBytes, 0o444)

  await mkdir(join(sourceRoot, 'source'), { mode: 0o700 }); const sourceBytes = Buffer.from('package main\n'); const sourceHash = await file(join(sourceRoot, 'source/main.go'), sourceBytes, 0o400)
  const aggregate = hash(`main.go\0${'0400'}\0${sourceBytes.length}\0${sourceHash}\n`)
  const sourceManifest = { schema_version: 'chora.local-alpha-source-bundle.v1', aggregate_sha256: aggregate, source_path_policy: { path: 'distribution/v1/policies/source-path-policy.v1.json', sha256: '6'.repeat(64) }, install_only_projection: { files: 1, paths_sha256: '7'.repeat(64), aggregate_sha256: '8'.repeat(64) }, model_readable_projection: { files: 1, paths_sha256: '9'.repeat(64), aggregate_sha256: '0'.repeat(64) }, files: [{ path: 'main.go', size: sourceBytes.length, mode: '0400', sha256: sourceHash }] }
  const sourceManifestBytes = Buffer.from(`${JSON.stringify(sourceManifest, null, 2)}\n`); const sourceFile = join(sourceRoot, 'source-manifest.json'); await file(sourceFile, sourceManifestBytes, 0o444)

  const binaryFile = join(productRoot, 'chora'); const binarySha256 = await file(binaryFile, 'chora-bin', 0o500)
  const webRoot = join(productRoot, 'web'); await mkdir(join(webRoot, 'assets'), { recursive: true, mode: 0o700 }); await file(join(webRoot, 'index.html'), '<html/>', 0o400); await file(join(webRoot, 'assets/app.js'), 'ok', 0o400)

  await mkdir(join(piRoot, 'dist'), { mode: 0o700 }); await mkdir(join(piRoot, 'lib'), { mode: 0o700 }); await mkdir(join(piRoot, 'node_modules'), { mode: 0o700 })
  await file(join(piRoot, 'package.json'), JSON.stringify({ name: '@earendil-works/pi-coding-agent', version: '0.84.2' }), 0o400)
  const piExecutable = join(piRoot, 'dist/cli.js'); const piExecutableSha256 = await file(piExecutable, '#!/usr/bin/env node\n', 0o500); await file(join(piRoot, 'lib/runtime.js'), 'export {}\n', 0o400)
  await symlink('../dist', join(piRoot, 'node_modules/.bin'))

  await file(join(playwrightRoot, 'cli.js'), 'playwright', 0o400); await file(join(playwrightRoot, 'driver.js'), 'driver', 0o400); await symlink('driver.js', join(playwrightRoot, 'driver-link.js'))
  await mkdir(join(browserRoot, 'chromium'), { mode: 0o700 }); await file(join(browserRoot, 'chromium/browser'), 'browser', 0o500); await symlink('browser', join(browserRoot, 'chromium/current'))
  const oauthSecret = 'SUPER-SECRET-AUTH-MATERIAL'; const oauthFile = join(inputsRoot, 'auth.json'); const oauthSha256 = await file(oauthFile, oauthSecret, 0o600)
  const caFile = join(inputsRoot, 'ca.pem'); const caSha256 = await file(caFile, 'CA', 0o400)
  const nodeFile = join(inputsRoot, 'node'); const nodeSha256 = await file(nodeFile, 'node', 0o500)
  const dockerFile = join(engineToolRoot, 'docker'); const dockerSha256 = await file(dockerFile, 'docker', 0o500)
  const colimaFile = join(engineToolRoot, 'colima'); const colimaSha256 = await file(colimaFile, 'colima', 0o500)
  const lima = await portableLimaPrefix(root)
  const runnerFile = join(inputsRoot, 'runner.mjs'); const runnerSha256 = await file(runnerFile, 'export {}\n', 0o400)
  const controllerFile = join(inputsRoot, 'controller')
  const controllerSha256 = await file(controllerFile,
    await readFile(captureBuild.capture.executableFile), 0o500)
  assert.equal(controllerSha256, captureBuild.capture.executableSha256)
  const ocrExecutableFile = join(inputsRoot, 'vision-ocr'); const ocrExecutableSha256 = await file(ocrExecutableFile, 'vision-ocr', 0o500)
  const ocrProfileFile = join(inputsRoot, 'ocr-deny-network.sb'); const ocrProfileSha256 = await file(ocrProfileFile, '(version 1)\n(allow default)\n(deny network*)\n', 0o400)
  const engineProfileBytes = `(version 1)\n(allow default)\n(deny network*)\n(allow network* (local unix-socket))\n(allow network* (remote unix-socket))\n(allow network-inbound (local ip "localhost:*"))\n(allow network-outbound (remote ip "localhost:*"))\n`
  const engineProfileFile = join(inputsRoot, 'engine-bootstrap.sb'); const engineProfileSha256 = await file(engineProfileFile, engineProfileBytes, 0o400)
  const diskImageFile = join(inputsRoot, 'colima-disk.img'); const diskImageBytes = Buffer.from('pinned-local-disk-image'); const diskImageSha256 = await file(diskImageFile, diskImageBytes, 0o400)
  const sandboxExecutableFile = '/usr/bin/sandbox-exec'; const sandboxExecutableSha256 = hash(await readFile(sandboxExecutableFile))
  const ocrBindingWithoutDigest = {
    schemaVersion: 'chora.m1-o4-system-managed-ocr-engine-binding.v1', status: 'authorized', engine: 'system_managed_ocr_engine', modelBytes: 'opaque_unavailable',
    platform: { os: 'darwin', architecture: 'arm64', macOSProductVersion: 'test-product', macOSBuildVersion: 'test-build', darwinSysname: 'Darwin', darwinRelease: 'test-release', darwinVersion: 'test-version', darwinMachine: 'arm64' },
    vision: { bundleIdentifier: 'com.apple.Vision', bundleVersion: 'test-version', infoPlistSha256: 'a'.repeat(64), supportedRecognitionRevisions: [1, 2], requestedRecognitionRevision: 2, recognitionLevel: 'accurate', recognitionLanguage: 'en-US', usesLanguageCorrection: true, confidenceThreshold: 0.5 },
    executable: { pathSha256: hash(ocrExecutableFile), sha256: ocrExecutableSha256 },
    sandbox: { executableFile: sandboxExecutableFile, executablePathSha256: hash(sandboxExecutableFile), executableSha256: sandboxExecutableSha256, profileFile: ocrProfileFile, profilePathSha256: hash(ocrProfileFile), profileSha256: ocrProfileSha256, networkRule: 'deny network*' },
  }
  const ocrBinding = { ...ocrBindingWithoutDigest, bindingDigest: hash(canonical(ocrBindingWithoutDigest)) }
  const ocrBindingFile = join(inputsRoot, 'vision-engine-binding.json'); const ocrBindingSha256 = await file(ocrBindingFile, `${JSON.stringify(ocrBinding, null, 2)}\n`, 0o400)
  const endpointSocketPath = join(`/private/tmp/o4i-${hash(root).slice(0, 16)}`,
    'c', 'chora-o4-test', 'docker.sock')
  const configs = {}
  for (const phase of ['C', 'D', 'E']) { const path = join(inputsRoot, `${phase}.json`); const sha256 = await file(path, `{ "phase": "${phase}" }\n`, 0o400); configs[phase] = { file: path, sha256 } }

  const request = {
    schemaVersion: REQUEST_SCHEMA, status: 'authorized', identity: { environmentId: 'env-1', installId: 'install-1', generationId: 'generation-1', platform: { os: 'darwin', architecture: 'arm64' } },
    release: { root: releaseRoot, manifestFile: releaseFile, manifestSha256: hash(releaseBytes) }, source: { bundleRoot: sourceRoot, manifestFile: sourceFile, manifestSha256: hash(sourceManifestBytes) },
    product: { binaryFile, binarySha256, webDirectory: webRoot, webClosureSha256: await webAggregate(webRoot) },
    pi: { packageRoot: piRoot, executableFile: piExecutable, packageClosureSha256: await directoryClosure(piRoot, { omit: (path) => path === 'node_modules/.bin' || path.startsWith('node_modules/.bin/') }), executableSha256: piExecutableSha256 },
    oauth: { file: oauthFile, sha256: oauthSha256 }, ca: { file: caFile, sha256: caSha256 }, node: { file: nodeFile, sha256: nodeSha256 },
    playwright: { packageRoot: playwrightRoot, packageClosureSha256: await directoryClosure(playwrightRoot), cliFile: join(playwrightRoot, 'cli.js'), cliSha256: hash('playwright'), browserRoot, browserClosureSha256: await directoryClosure(browserRoot) },
    configs,
    runner: { moduleFile: runnerFile, moduleSha256: runnerSha256 }, serviceController: { executableFile: controllerFile, sha256: controllerSha256 },
    ocr: { executableFile: ocrExecutableFile, executableSha256: ocrExecutableSha256, engineBindingFile: ocrBindingFile, engineBindingSha256: ocrBindingSha256, sandboxExecutableFile, sandboxExecutableSha256, sandboxProfileFile: ocrProfileFile, sandboxProfileSha256: ocrProfileSha256, language: 'eng' },
    engine: { limaPrefixRoot: lima.prefixRoot, limaPrefixClosureSha256: lima.prefixClosureSha256,
      limaInfoDigest: lima.infoDigest,
      limaFile: lima.limactlFile, limaSha256: lima.limactlSha256,
      limaWrapperFile: lima.limaFile, limaWrapperSha256: lima.limaSha256,
      limaTemplatesRoot: lima.templatesRoot, limaTemplatesClosureSha256: lima.templatesClosureSha256,
      limaGuestAgentFile: lima.guestAgentFile, limaGuestAgentSize: lima.guestAgentSize,
      limaGuestAgentSha256: lima.guestAgentSha256,
      diskImageFile, diskImageSize: diskImageBytes.length, diskImageSha256, sandboxExecutableFile,
      sandboxExecutableSha256, sandboxProfileFile: engineProfileFile,
      sandboxProfileSha256: engineProfileSha256, profileName: 'chora-o4-test',
      contextName: 'colima-chora-o4-test', endpointSocketPath,
      endpointDigest: hash(`unix://${endpointSocketPath}`), cpus: 2, memoryGiB: 4,
      diskGiB: 20, engineMs: 60_000 },
    docker: { file: dockerFile, sha256: dockerSha256 }, colima: { file: colimaFile, sha256: colimaSha256 },
  }
  const requestFile = join(root, 'request.json'); const outputRoot = join(root, 'prepared')
  const saveRequest = async () => { const bytes = Buffer.from(`${JSON.stringify(request, null, 2)}\n`); await rm(requestFile, { force: true }); await file(requestFile, bytes, 0o400); return hash(bytes) }
  return { root, request, requestFile, outputRoot, oauthSecret, saveRequest, releaseManifest,
    releaseFile, paths: { releaseRoot, sourceRoot, piRoot, playwrightRoot, browserRoot, oauthFile },
    engineTools: { root: engineToolRoot, docker: dockerFile }, lima }
}

async function prepare(fx, dependencies = {}) {
  const requestSha256 = await fx.saveRequest()
  return prepareO4StaticInputs({ requestFile: fx.requestFile, requestSha256,
    outputRoot: fx.outputRoot }, { spawnVersionProbe: syntheticVersionSpawn(), ...dependencies })
}

async function assertFailClean(fx, pattern, dependencies = {}) {
  await assert.rejects(prepare(fx, dependencies), pattern)
  await assert.rejects(lstat(fx.outputRoot), { code: 'ENOENT' })
  assert.equal((await readdir(fx.root)).some((name) => name.startsWith('.prepared.stage-')), false)
}

test('prepares deterministic static bindings without disclosing OAuth bytes', async (t) => {
  const fx = await fixture(t); const result = await prepare(fx)
  assert.equal(fx.request.engine.limaFile, fx.lima.limactlFile)
  assert.equal(fx.request.engine.limaWrapperFile, fx.lima.limaFile)
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
  assert.equal(result.result.schemaVersion, RESULT_SCHEMA); assert.equal(result.result.status, 'static_unresolved')
  assert.deepEqual(result.result.release.roles.map((role) => role.role), ['managed_pi_runtime', 'network_boundary', 'independent_verifier', 'capability_probe'])
  assert.equal(result.result.release.roles[2].archiveSha256, result.result.release.roles[0].archiveSha256)
  assert.equal((await lstat(fx.outputRoot)).mode & 0o777, 0o700)
  const names = await readdir(fx.outputRoot)
  for (const name of names.filter((name) => name.endsWith('.json'))) assert.equal((await lstat(join(fx.outputRoot, name))).mode & 0o777, 0o400)
  const privateManifest = JSON.parse(await readFile(join(fx.outputRoot, 'private-pi-manifest.json'), 'utf8'))
  assert.equal(privateManifest.schema_version, 'chora.pi-private-asset.v1'); assert.equal(privateManifest.version, '0.84.2'); assert.match(privateManifest.closure_sha256, /^[0-9a-f]{64}$/)
  await assert.rejects(lstat(join(fx.outputRoot, 'private-pi-source/node_modules/.bin')), { code: 'ENOENT' })
  let published = ''
  const collect = async (directory) => { for (const name of await readdir(directory)) { const path = join(directory, name); const info = await lstat(path); if (info.isDirectory()) await collect(path); else published += (await readFile(path)).toString('utf8') } }
  await collect(fx.outputRoot); assert.equal(published.includes(fx.oauthSecret), false)
  const candidate = JSON.parse(await readFile(join(fx.outputRoot, 'candidate-config.template.json'), 'utf8'))
  assert.equal(candidate.paths.tupleFile, null); assert.equal(candidate.authority.modelAuthoritySha256, null)
  assert.equal(candidate.authority.repositoryCommit, null)
  assert.equal(candidate.authority.repositoryClosureSha256, null)
  assert.equal(candidate.authority.dockerContext, `colima-${fx.request.engine.profileName}`)
  const total = JSON.parse(await readFile(join(fx.outputRoot, 'total-manifest.template.json'), 'utf8'))
  assert.equal(total.engine.contextName, `colima-${total.engine.profileName}`)
  assert.notEqual(candidate.authority.pathPiProvenanceSha256, candidate.authority.privatePiProvenanceSha256)
})

test('refuses to clean a replaced staging directory and preserves both failures', async (t) => {
  const fx = await fixture(t); const displaced = join(fx.root, 'displaced-owned-staging')
  let foreignMarker
  let rejection
  await assert.rejects(prepare(fx, {
    beforeCommit: async ({ staging }) => {
      await rename(staging, displaced)
      await mkdir(staging, { mode: 0o700 }); await chmod(staging, 0o700)
      foreignMarker = join(staging, 'foreign-marker')
      await file(foreignMarker, 'foreign tree\n', 0o400)
      throw new Error('injected preparation failure')
    },
  }), (error) => { rejection = error; return true })
  assert.ok(rejection instanceof AggregateError)
  assert.match(rejection.message, /staging cleanup failed.*owned cleanup path identity drifted/i)
  assert.equal(rejection.cause?.message, 'injected preparation failure')
  assert.equal(rejection.errors[0]?.message, 'injected preparation failure')
  assert.match(rejection.errors[1]?.message, /owned cleanup path identity drifted/i)
  assert.equal(await readFile(foreignMarker, 'utf8'), 'foreign tree\n')
  assert.equal((await lstat(displaced)).isDirectory(), true)
  await assert.rejects(lstat(fx.outputRoot), { code: 'ENOENT' })
})

test('treats an already absent owned staging directory as clean after failure', async (t) => {
  const fx = await fixture(t)
  await assertFailClean(fx, /injected failure after staging removal/, {
    beforeCommit: async ({ staging }) => {
      await rm(staging, { recursive: true })
      throw new Error('injected failure after staging removal')
    },
  })
})

test('refuses to publish a replaced staging directory and preserves the foreign tree', async (t) => {
  const fx = await fixture(t); const displaced = join(fx.root, 'displaced-before-publication')
  let foreignMarker
  let rejection
  await assert.rejects(prepare(fx, {
    publishOwnedPath: { beforeSpawn: async ({ protocol, request }) => {
      assert.equal(protocol, 'chora.m1-o4-owned-path-publish.v1')
      await rename(request.sourcePath, displaced)
      await mkdir(request.sourcePath, { mode: 0o700 }); await chmod(request.sourcePath, 0o700)
      foreignMarker = join(request.sourcePath, 'foreign-marker')
      await file(foreignMarker, 'must not publish\n', 0o400)
    } },
  }), (error) => { rejection = error; return true })
  assert.ok(rejection instanceof AggregateError)
  assert.match(rejection.cause?.message, /owned-path controller failed/i)
  assert.equal(rejection.errors[0], rejection.cause)
  assert.equal(await readFile(foreignMarker, 'utf8'), 'must not publish\n')
  assert.equal((await lstat(displaced)).isDirectory(), true)
  await assert.rejects(lstat(fx.outputRoot), { code: 'ENOENT' })
})

test('refuses a racing output target without deleting it and safely cleans owned staging', async (t) => {
  const fx = await fixture(t); const marker = join(fx.outputRoot, 'foreign-marker')
  let rejection
  await assert.rejects(prepare(fx, {
    publishOwnedPath: { beforeSpawn: async ({ request }) => {
      assert.equal(request.targetPath, fx.outputRoot)
      await mkdir(request.targetPath, { mode: 0o700 }); await chmod(request.targetPath, 0o700)
      await file(marker, 'must remain foreign\n', 0o400)
    } },
  }), (error) => { rejection = error; return true })
  assert.ok(rejection instanceof AggregateError)
  assert.match(rejection.cause?.message, /owned-path controller failed/i)
  assert.equal(await readFile(marker, 'utf8'), 'must remain foreign\n')
  assert.deepEqual(await readdir(fx.outputRoot), ['foreign-marker'])
  assert.equal((await readdir(fx.root)).some((name) => name.startsWith('.prepared.stage-')), false)
})

test('rejects non-derived Docker contexts before static preparation', async (t) => {
  const cases = [
    ['plain profile', (fx) => { fx.request.engine.contextName = fx.request.engine.profileName }],
    ['arbitrary context', (fx) => { fx.request.engine.contextName = 'colima-unrelated-profile' }],
  ]
  for (const [name, mutate] of cases) await t.test(name, async (t) => {
    const fx = await fixture(t); mutate(fx)
    await assertFailClean(fx, /Docker context must be exactly derived from its Colima profile/)
  })
})

test('runs the copied Pi version probe as one exactly isolated sandbox child', async (t) => {
  const fx = await fixture(t); let invocation
  await prepare(fx, { spawnVersionProbe: syntheticVersionSpawn({
    onSpawn: (executable, args, options) => { invocation = { executable, args, options } },
  }) })
  assert.ok(invocation)
  assert.equal(invocation.executable, '/usr/bin/sandbox-exec')
  assert.equal(invocation.args[0], '-p')
  assert.equal(invocation.args[1], `(version 1)
(allow default)
(deny network*)
(deny file-write*)
(deny process-fork)
`)
  assert.equal(invocation.args[2], fx.request.node.file)
  assert.equal(invocation.args[3], join(invocation.options.cwd, 'dist/cli.js'))
  assert.equal(invocation.args[4], '--version')
  assert.deepEqual(invocation.args.slice(5), [])
  assert.equal(invocation.options.shell, false)
  assert.deepEqual(invocation.options.stdio, ['ignore', 'pipe', 'pipe'])
  assert.equal(Object.getPrototypeOf(invocation.options.env), null)
  assert.deepEqual(Object.keys(invocation.options.env), [])
})

test('rejects every pre-spawn nested copied Pi closure drift without spawning or publishing', async (t) => {
  const cases = [
    ['bytes', async (path) => {
      await chmod(path, 0o600); await writeFile(path, 'tampered bytes\n'); await chmod(path, 0o400)
    }],
    ['inode', async (path) => {
      const replacement = join(path, '..', 'replacement.js')
      await file(replacement, 'replacement\n', 0o400); await rename(replacement, path)
    }],
    ['path', async (path) => rename(path, join(path, '..', 'moved.js'))],
    ['mode', async (path) => chmod(path, 0o600)],
    ['symlink', async (path) => { await unlink(path); await symlink('../package.json', path) }],
    ['hardlink', async (path) => link(path, join(path, '..', 'alias.js'))],
  ]
  for (const [name, mutate] of cases) await t.test(name, async (t) => {
    const fx = await fixture(t); let spawnCount = 0
    const dependencies = {
        beforeVersionProbeValidation: async (binding) => {
          await mutate(join(binding.packageRoot, 'lib/runtime.js'))
        },
        spawnVersionProbe: syntheticVersionSpawn({ onSpawn: () => { spawnCount += 1 } }),
      }
    const primaryPattern = /Pi version package closure|writable|multiply-linked|unexpected symlink/i
    if (name === 'symlink' || name === 'hardlink') {
      let rejection
      await assert.rejects(prepare(fx, dependencies), (error) => {
        rejection = error
        return true
      })
      assert.ok(rejection instanceof AggregateError)
      assert.match(rejection.cause?.message ?? '', primaryPattern)
      assert.match(rejection.message, /staging cleanup failed.*owned-path controller failed/i)
      await assert.rejects(lstat(fx.outputRoot), { code: 'ENOENT' })
      assert.equal((await readdir(fx.root)).filter(
        (entry) => entry.startsWith('.prepared.stage-')).length, 1)
    } else {
      await assertFailClean(fx, primaryPattern, dependencies)
    }
    assert.equal(spawnCount, 0)
  })
})

test('rejects nested copied Pi TOCTOU drift while the sandbox child is active', async (t) => {
  const fx = await fixture(t); let spawnCount = 0
  await assertFailClean(fx, /Pi version package closure.*drifted/i, {
    spawnVersionProbe: syntheticVersionSpawn({
      onSpawn: () => { spawnCount += 1 },
      beforeClose: async (_executable, _args, options) => {
        const nested = join(options.cwd, 'lib/runtime.js')
        await chmod(nested, 0o600); await writeFile(nested, 'during probe\n'); await chmod(nested, 0o400)
      },
    }),
  })
  assert.equal(spawnCount, 1)
})

test('rejects Pi probe authority drift before spawning or publishing output', async (t) => {
  const cases = [
    ['sandbox path', async (fx) => {
      fx.request.ocr.sandboxExecutableFile = '/bin/sh'
      fx.request.engine.sandboxExecutableFile = '/bin/sh'
    }],
    ['sandbox hash', async (fx) => {
      fx.request.ocr.sandboxExecutableSha256 = '0'.repeat(64)
      fx.request.engine.sandboxExecutableSha256 = '0'.repeat(64)
    }],
    ['Node mode', async (fx) => chmod(fx.request.node.file, 0o700)],
    ['Node hash', async (fx) => { fx.request.node.sha256 = '0'.repeat(64) }],
    ['Pi path', async (fx) => { fx.request.pi.executableFile = join(fx.paths.piRoot, 'lib/runtime.js') }],
  ]
  for (const [name, mutate] of cases) await t.test(name, async (t) => {
    const fx = await fixture(t); await mutate(fx); let spawnCount = 0
    await assertFailClean(fx, /sandbox|Node|Pi executable|binding|SHA-256|identity/i, {
      spawnVersionProbe: syntheticVersionSpawn({ onSpawn: () => { spawnCount += 1 } }),
    })
    assert.equal(spawnCount, 0)
  })
})

test('waits for child close before rejecting Pi probe terminal failures', async (t) => {
  const cases = [
    ['overflow', /version probe output exceeded bound/,
      { stdout: Buffer.alloc(4097, 0x78) }, {}],
    ['error', /synthetic Pi child error/,
      { autoClose: false, emitError: new Error('synthetic Pi child error') }, {}],
    ['timeout', /version probe timed out/, { autoClose: false },
      { scheduleVersionProbeTimeout: (callback) => { queueMicrotask(callback) } }],
  ]
  for (const [name, pattern, spawnOptions, dependencies] of cases) {
    await t.test(name, async (t) => {
      const fx = await fixture(t); let active = false; let closeCount = 0
      await assertFailClean(fx, pattern, {
        ...dependencies,
        spawnVersionProbe: syntheticVersionSpawn({ ...spawnOptions,
          onSpawn: () => { active = true },
          onClose: () => { active = false; closeCount += 1 },
        }),
      })
      assert.equal(active, false); assert.equal(closeCount, 1)
    })
  }
})

test('performs post-close closure validation on every forced probe termination', async (t) => {
  const cases = [
    ['overflow', { stdout: Buffer.alloc(4097, 0x78) }, {}],
    ['error', { autoClose: false, emitError: new Error('synthetic Pi child error') }, {}],
    ['timeout', { autoClose: false },
      { scheduleVersionProbeTimeout: (callback) => { queueMicrotask(callback) } }],
  ]
  for (const [name, spawnOptions, dependencies] of cases) {
    await t.test(name, async (t) => {
      const fx = await fixture(t); let active = false
      await assertFailClean(fx, /Pi version package closure.*drifted/i, {
        ...dependencies,
        spawnVersionProbe: syntheticVersionSpawn({ ...spawnOptions,
          onSpawn: () => { active = true }, onClose: () => { active = false },
          beforeClose: async (_executable, _args, options) => {
            const nested = join(options.cwd, 'lib/runtime.js')
            await chmod(nested, 0o600); await writeFile(nested, `${name} drift\n`)
            await chmod(nested, 0o400)
          },
        }),
      })
      assert.equal(active, false)
    })
  }
})

test('rejects portable Lima prefix drift before preparing output', async (t) => {
  const cases = [
    ['missing', async (fx) => unlink(join(fx.lima.templatesRoot, 'default.yaml'))],
    ['guest-agent missing', async (fx) => unlink(fx.lima.guestAgentFile)],
    ['extra', async (fx) => file(join(fx.lima.templatesRoot, 'extra.yaml'), 'x', 0o400)],
    ['link', async (fx) => symlink('default.yaml', join(fx.lima.templatesRoot, 'linked.yaml'))],
    ['mode', async (fx) => chmod(join(fx.lima.templatesRoot, 'default.yaml'), 0o600)],
    ['hash', async (fx) => {
      const path = join(fx.lima.templatesRoot, 'extras/asset-000.yaml')
      await chmod(path, 0o600); await writeFile(path, 'y'); await chmod(path, 0o400)
    }],
    ['topology', async (fx) => { fx.request.engine.limaWrapperFile = fx.lima.limactlFile }],
  ]
  for (const [name, mutate] of cases) await t.test(name, async (t) => {
    const fx = await fixture(t); await mutate(fx)
    let versionProbeCount = 0
    await assertFailClean(fx, /canonical|prefix|template|topology|symlink|SHA-256|ENOENT/i,
      { spawnVersionProbe: syntheticVersionSpawn({ onSpawn: () => { versionProbeCount += 1 } }) })
    assert.equal(versionProbeCount, 0)
  })
  await t.test('TOCTOU', async (t) => {
    const fx = await fixture(t); const path = join(fx.lima.templatesRoot, 'default.yaml')
    await assertFailClean(fx, /canonical Lima (?:prefix|templates) entry TOCTOU drifted/, {
      beforeCommit: async () => chmod(path, 0o600),
    })
  })
})

test('rejects Lima info authorities that include README or drift the authenticated YAML set', async (t) => {
  for (const [name, templates] of [
    ['README inclusion', (fx) => [...fx.lima.templateLocations,
      join(fx.lima.templatesRoot, 'README.md')].sort()],
    ['template set drift', (fx) => [...fx.lima.templateLocations.slice(0, -1),
      join(fx.lima.templatesRoot, 'extras/not-authorized.yaml')].sort()],
  ]) await t.test(name, async (t) => {
    const fx = await fixture(t)
    fx.request.engine.limaInfoDigest = hash(canonical({ templates: templates(fx),
      guestAgent: fx.lima.guestAgentFile, hostOS: 'darwin', hostArch: 'aarch64' }))
    await assertFailClean(fx, /canonical Lima info authority drifted/)
  })
})

test('emits the current causal skeleton but materializer rejects it before dynamic finalization', async (t) => {
  const fx = await fixture(t); await prepare(fx)
  const candidateManifestTemplate = JSON.parse(await readFile(join(fx.outputRoot, 'candidate-manifest.template.json'), 'utf8'))
  const candidateConfigTemplate = JSON.parse(await readFile(join(fx.outputRoot, 'candidate-config.template.json'), 'utf8'))
  const totalManifestTemplate = JSON.parse(await readFile(join(fx.outputRoot, 'total-manifest.template.json'), 'utf8'))
  const pathKeys = ['sourceBundleRoot', 'sourceManifestFile', 'candidateManifestFile', 'binaryFile', 'webDirectory', 'releaseSpecFile', 'releaseManifestFile', 'offlineBundleEvidenceFile', 'pathPiProvenanceFile', 'privatePiProvenanceFile', 'modelAuthorityFile', 'qualificationFile', 'modelObservationFile', 'installedDoctorReportFile', 'endpointEvidenceFile', 'preflightFile', 'dockerCLIFile', 'installRoot', 'installedBinaryFile', 'installedWebDirectory', 'installedSourceRoot', 'installedSourceManifestFile', 'dataRoot', 'databaseFile', 'repositoryRoot', 'authFile', 'caFile', 'stateRoot', 'ledgerFile', 'receiptDirectory', 'privatePiRoot', 'privatePiSourceRoot', 'privatePiManifestFile', 'actualReleaseRoot', 'actualReleaseManifestFile', 'probeRuntimeRoot', 'colimaToolRoot', 'colimaSourceFile', 'dockerClientSourceFile', 'tupleFile']
  const authorityKeys = ['sourceManifestSha256', 'sourceAggregateSha256', 'candidateManifestSha256', 'binarySha256', 'webAggregateSha256', 'releaseSpecSha256', 'releaseManifestSha256', 'offlineBundleEvidenceSha256', 'pathPiProvenanceSha256', 'privatePiProvenanceSha256', 'modelAuthoritySha256', 'endpointEvidenceSha256', 'dockerCLISha256', 'limaPrefixClosureSha256', 'limaInfoDigest', 'limaSha256', 'diskImageSha256', 'diskImageSize', 'endpointSocketPathSha256', 'sandboxExecutableSha256', 'sandboxProfileSha256', 'actualReleaseManifestSha256', 'privatePiManifestSha256', 'authFileSha256', 'caFileSha256', 'colimaSourceSha256', 'dockerClientSourceSha256', 'dockerContext', 'endpointDigest', 'repositoryCommit', 'repositoryClosureSha256', 'roles']
  assert.deepEqual(Object.keys(candidateConfigTemplate.paths).sort(), pathKeys.sort())
  assert.deepEqual(Object.keys(candidateConfigTemplate.authority).sort(), authorityKeys.sort())
  assert.deepEqual(candidateManifestTemplate.repositoryCommit, null)
  assert.deepEqual(candidateManifestTemplate.repositoryClosureSha256, null)
  assert.deepEqual(totalManifestTemplate.repository, { root: null, commit: null, closureSha256: null })
  assert.deepEqual(Object.keys(candidateConfigTemplate.private).sort(), ['proxyURL', 'modelURL', 'port', 'colimaProfile'].sort())
  assert.equal(candidateConfigTemplate.paths.preflightFile, null)
  assert.equal(candidateConfigTemplate.paths.modelObservationFile, null)
  assert.equal(candidateConfigTemplate.paths.installedDoctorReportFile, null)
  assert.deepEqual(Object.keys(totalManifestTemplate.engine).sort(), ['colimaFile', 'colimaSha256',
    'limaPrefixRoot', 'limaPrefixClosureSha256', 'limaInfoDigest', 'limaFile', 'limaSha256',
    'limaWrapperFile', 'limaWrapperSha256', 'limaTemplatesRoot',
    'limaTemplatesClosureSha256', 'limaGuestAgentFile', 'limaGuestAgentSize',
    'limaGuestAgentSha256', 'dockerFile', 'dockerSha256', 'diskImageFile', 'diskImageSize',
    'diskImageSha256', 'sandboxExecutableFile', 'sandboxExecutableSha256',
    'sandboxProfileFile', 'sandboxProfileSha256', 'profileName', 'contextName',
    'endpointSocketPath', 'endpointDigest', 'cpus', 'memoryGiB', 'diskGiB',
    'zeroImagePreflightFile', 'cleanupResidueFile'].sort())
  assert.equal(candidateConfigTemplate.authority.limaPrefixClosureSha256,
    totalManifestTemplate.engine.limaPrefixClosureSha256)
  assert.equal(candidateConfigTemplate.authority.limaInfoDigest,
    totalManifestTemplate.engine.limaInfoDigest)
  assert.match(candidateConfigTemplate.authority.limaInfoDigest, /^[0-9a-f]{64}$/)
  assert.deepEqual(Object.keys(totalManifestTemplate.roots).sort(), ['userRoot', 'installRoot', 'dataRoot', 'stateRoot', 'receiptRoot', 'evidenceRoot', 'runnerControlRoot', 'probeRuntimeRoot', 'colimaHome', 'dockerConfigRoot', 'temporaryRoot'].sort())
  assert.deepEqual(Object.keys(totalManifestTemplate.timeouts).sort(), ['engineMs', 'phaseMs', 'controllerMs', 'exporterMs', 'closeMs'].sort())
  assert.equal(totalManifestTemplate.engine.zeroImagePreflightFile, null)
  assert.equal(totalManifestTemplate.engine.cleanupResidueFile, null)
  assert.equal(totalManifestTemplate.ocr.language, 'eng')
  assert.equal(totalManifestTemplate.ocr.engineBindingFile, fx.request.ocr.engineBindingFile)
  assert.equal(totalManifestTemplate.phases.C.environment
    .CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_FILE,
  fx.request.serviceController.executableFile)
  assert.equal(totalManifestTemplate.phases.C.environment
    .CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_SHA256,
  fx.request.serviceController.sha256)
  assert.equal(totalManifestTemplate.phases.E.environment
    .CHORA_O4_FINAL_SERVICE_CONTROLLER_FILE,
  fx.request.serviceController.executableFile)
  assert.equal(totalManifestTemplate.phases.E.environment
    .CHORA_O4_FINAL_SERVICE_CONTROLLER_SHA256,
  fx.request.serviceController.sha256)
  assert.equal(totalManifestTemplate.status, 'static_unresolved')
  assert.equal(totalManifestTemplate.acceptanceClass, null)
  assert.equal(totalManifestTemplate.roots.probeRuntimeRoot, null)
  assert.ok(Object.values(totalManifestTemplate.roots).every((value) => value === null))
  assert.ok(['phaseMs', 'controllerMs', 'exporterMs', 'closeMs'].every((key) => totalManifestTemplate.timeouts[key] === null))
  assert.ok(Object.values(candidateConfigTemplate.setup).every((value) => value === null))
  const serialized = JSON.stringify({ candidateManifestTemplate, candidateConfigTemplate, totalManifestTemplate })
  assert.doesNotMatch(serialized, /OCR_MODEL|modelFile|modelSha256|preflightSha256|registry/i)
  assert.doesNotMatch(serialized, /"status":"passed"/)

  const artifacts = fx.releaseManifest.artifacts.map((artifact) => {
    const roleBindings = fx.releaseManifest.roles.filter((role) => role.artifact_id === artifact.id)
    const roles = roleBindings.map((role) => role.role).sort()
    return { artifactId: artifact.id, roles, policyDigests: Object.fromEntries(roleBindings.map((role) => [role.role, role.policy.sha256])), archiveFile: join(fx.paths.releaseRoot, ...artifact.image.archive.path.split('/')), archiveRelativePath: artifact.image.archive.path, archiveMode: 0o400, archiveSize: artifact.image.archive.size, archiveSha256: artifact.image.archive.sha256, dockerConfigImageId: artifact.image.local_docker_config_image_id }
  })
  const outputRoot = join(fx.root, 'materialized')
  candidateConfigTemplate.paths.candidateManifestFile = join(outputRoot, 'candidate-manifest.json')
  candidateConfigTemplate.paths.offlineBundleEvidenceFile = join(outputRoot, 'offline-bundle-evidence.json')
  totalManifestTemplate.candidate.configFile = join(outputRoot, 'candidate-config.json')
  totalManifestTemplate.phases.D.environment.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE =
    join(outputRoot, 'candidate-config.json')
  totalManifestTemplate.phases.E.environment.CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE =
    join(outputRoot, 'candidate-manifest.json')
  totalManifestTemplate.phases.E.environment.CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE =
    join(outputRoot, 'offline-bundle-evidence.json')
  const materializationRequest = { schemaVersion: MATERIALIZATION_REQUEST_SCHEMA, status: 'authorized',
    identity: fx.request.identity, outputRoot, releaseId: fx.releaseManifest.release_id,
    releaseSpecSha256: fx.releaseManifest.spec_sha256, releaseManifestFile: fx.releaseFile,
    releaseManifestMode: 0o444, releaseManifestSha256: fx.request.release.manifestSha256,
    artifacts, candidateManifestTemplate, candidateConfigTemplate, totalManifestTemplate }
  const materializationRequestFile = join(fx.root, 'materialization-request.json')
  const materializationBytes = Buffer.from(`${JSON.stringify(materializationRequest, null, 2)}\n`)
  await file(materializationRequestFile, materializationBytes, 0o400)
  await assert.rejects(materializeO4OfflineRelease({ requestFile: materializationRequestFile,
    requestSha256: hash(materializationBytes), outputRoot }),
  /total repository root must be exact absolute|Darwin AF_UNIX bindings are incomplete/)
  await assert.rejects(lstat(outputRoot), { code: 'ENOENT' })
})

test('rejects unexpected private Pi links and remains fail-clean', async (t) => {
  const fx = await fixture(t); await symlink('package.json', join(fx.paths.piRoot, 'unexpected-link'))
  fx.request.pi.packageClosureSha256 = await directoryClosure(fx.paths.piRoot, { omit: (path) => path === 'node_modules/.bin' || path.startsWith('node_modules/.bin/') })
  await assertFailClean(fx, /unexpected symlink/)
})

test('rejects private Pi version drift', async (t) => {
  const fx = await fixture(t); await assertFailClean(fx, /version probe rejected/,
    { spawnVersionProbe: syntheticVersionSpawn({ stdout: '0.84.3\n' }) })
})

test('rejects an external OCR-model boundary even when its rewritten binding is re-pinned', async (t) => {
  const fx = await fixture(t)
  const binding = JSON.parse(await readFile(fx.request.ocr.engineBindingFile, 'utf8'))
  binding.modelBytes = 'external_model_file'; delete binding.bindingDigest
  binding.bindingDigest = hash(canonical(binding))
  const bytes = Buffer.from(`${JSON.stringify(binding, null, 2)}\n`)
  await chmod(fx.request.ocr.engineBindingFile, 0o600); await writeFile(fx.request.ocr.engineBindingFile, bytes); await chmod(fx.request.ocr.engineBindingFile, 0o400)
  fx.request.ocr.engineBindingSha256 = hash(bytes)
  await assertFailClean(fx, /system-managed OCR boundary drifted/)
})

test('rejects escaping Playwright symlink even when the closure pin matches', async (t) => {
  const fx = await fixture(t); await symlink('../../outside', join(fx.paths.playwrightRoot, 'escape'))
  fx.request.playwright.packageClosureSha256 = await directoryClosure(fx.paths.playwrightRoot)
  await assertFailClean(fx, /escapes root/)
})

test('rejects Playwright and browser closure drift', async (t) => {
  const playwright = await fixture(t); await file(join(playwright.paths.playwrightRoot, 'drift'), 'x', 0o400); await assertFailClean(playwright, /Playwright package closure drifted/)
  const browser = await fixture(t); await file(join(browser.paths.browserRoot, 'drift'), 'x', 0o400); await assertFailClean(browser, /browser closure drifted/)
})

test('rejects release/source overlap and path splice', async (t) => {
  const overlap = await fixture(t); overlap.request.source.bundleRoot = overlap.paths.releaseRoot; overlap.request.source.manifestFile = join(overlap.paths.releaseRoot, 'source-manifest.json'); await assertFailClean(overlap, /release and source roots must be distinct/)
  const splice = await fixture(t); splice.request.release.manifestFile = join(splice.root, 'elsewhere.json'); await assertFailClean(splice, /release manifest path is spliced/)
})

test('rejects unsafe modes and hardlinks', async (t) => {
  const unsafeMode = await fixture(t); await chmod(unsafeMode.paths.oauthFile, 0o644); await assertFailClean(unsafeMode, /OAuth file is not one owner-controlled/)
  const hardlinked = await fixture(t); await link(join(hardlinked.paths.piRoot, 'lib/runtime.js'), join(hardlinked.paths.piRoot, 'lib/duplicate.js'))
  hardlinked.request.pi.packageClosureSha256 = await directoryClosure(hardlinked.paths.piRoot, { omit: (path) => path === 'node_modules/.bin' || path.startsWith('node_modules/.bin/') })
  await assertFailClean(hardlinked, /multiply-linked/)
})

test('detects post-validation TOCTOU drift before publication', async (t) => {
  const fx = await fixture(t)
  await assertFailClean(fx, /browser closure entry TOCTOU drifted/, { beforeCommit: async () => { await chmod(join(fx.paths.browserRoot, 'chromium/browser'), 0o400) } })
})
