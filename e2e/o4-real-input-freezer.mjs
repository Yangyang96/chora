#!/usr/bin/env node
import { createHash, randomBytes } from 'node:crypto'
import { createReadStream } from 'node:fs'
import { lstat as lstatNative, mkdir, open, readFile, readdir, readlink, realpath } from 'node:fs/promises'
import { basename, dirname, isAbsolute, join, normalize, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { assertDarwinUnixSocketPath, assertO4DarwinUnixSocketPaths } from './o4-darwin-unix-sockets.mjs'
import { inspectRepositoryAuthority, verifyRepositoryAuthority } from './o4-repository-authority.mjs'
import { cleanupOwnedPath, ownedPathIdentity, publishOwnedPath,
  validateOwnedPathCapability } from './o4-owned-path-cleanup.mjs'

export const FREEZE_REQUEST_SCHEMA = 'chora.m1-o4-real-input-freeze-request.v1'
export const FROZEN_INPUTS_SCHEMA = 'chora.m1-o4-real-frozen-inputs.v1'
export const FINALIZE_REQUEST_SCHEMA = 'chora.m1-o4-real-input-finalization-request.v1'
export const STATIC_REQUEST_SCHEMA = 'chora.m1-o4-static-input-preparation-request.v1'
export const MATERIALIZATION_REQUEST_SCHEMA = 'chora.m1-o4-local-offline-materialization-request.v1'

const digestRE = /^[0-9a-f]{64}$/
const commitRE = /^[0-9a-f]{40}$/
const imageIDRE = /^sha256:[0-9a-f]{64}$/
const productIDRE = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/
const MAX_JSON = 16 * 1024 * 1024
const MAX_EXECUTABLE = 256 * 1024 * 1024
const MAX_DISK = 64 * 1024 * 1024 * 1024
const uid = typeof process.getuid === 'function' ? process.getuid() : null
const ocrProfile = `(version 1)
(allow default)
(deny network*)
`
const engineProfile = `(version 1)
(allow default)
(deny network*)
(allow network* (local unix-socket))
(allow network* (remote unix-socket))
(allow network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
`
const materializerNulls = new Set([
  'candidateManifest.offlineBundleEvidenceSha256',
  'candidateManifest.repositoryCommit',
  'candidateManifest.repositoryClosureSha256',
  'candidateConfig.paths.candidateManifestFile',
  'candidateConfig.paths.offlineBundleEvidenceFile',
  'candidateConfig.authority.candidateManifestSha256',
  'candidateConfig.authority.offlineBundleEvidenceSha256',
  'totalManifest.selfDigest',
  'totalManifest.candidate.configFile',
  'totalManifest.candidate.configSha256',
  'totalManifest.candidate.tupleIdentity',
  'totalManifest.phases.D.environment.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE',
  'totalManifest.phases.D.environment.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_SHA256',
  'totalManifest.phases.D.environment.CHORA_O4_RECOVERY_RESIDUE_BINDING_DIGEST',
  'totalManifest.phases.E.environment.CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE',
  'totalManifest.phases.E.environment.CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE',
])

function fail(message) { throw new Error(message) }
function assert(value, message) { if (!value) fail(message) }
function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function digest(value, label) { assert(typeof value === 'string' && digestRE.test(value), `${label} is invalid`) }
function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`
  if (value && typeof value === 'object') return `{${Object.keys(value).sort().map((key) =>
    `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`
  return JSON.stringify(value)
}
function jsonBytes(value) { return Buffer.from(`${JSON.stringify(value, null, 2)}\n`) }
function deriveLimaInfoDigest(lima, limaTemplates) {
  const templates = limaTemplates.entries
    .filter((entry) => entry.type === 'file' && entry.path.endsWith('.yaml'))
    .map((entry) => join(lima.templatesRoot, ...entry.path.split('/')))
    .sort()
  assert(templates.length === 120 && new Set(templates).size === 120,
    'canonical Lima info template authority is incomplete')
  return sha256(canonical({ templates, guestAgent: lima.guestAgentFile,
    hostOS: 'darwin', hostArch: 'aarch64' }))
}
function exactKeys(value, keys, label) {
  assert(value && typeof value === 'object' && !Array.isArray(value), `${label} must be an object`)
  assert(JSON.stringify(Object.keys(value).sort()) === JSON.stringify([...keys].sort()), `${label} keys drifted`)
}
function absolute(value, label) {
  assert(typeof value === 'string' && value.length > 1 && isAbsolute(value) &&
    normalize(value) === value && resolve(value) === value && !value.includes('\0'),
  `${label} must be exact absolute`)
  return value
}
function within(root, child) {
  const value = relative(root, child)
  return value !== '' && value !== '..' && !value.startsWith(`..${sep}`) && !isAbsolute(value)
}
function overlaps(left, right) { return left === right || within(left, right) || within(right, left) }
function safeRelative(value) {
  return typeof value === 'string' && value.length > 0 && !isAbsolute(value) &&
    normalize(value) === value && value.split(sep).join('/') === value &&
    !value.split('/').includes('..') && !value.includes('\0')
}
function mode(info) {
  return typeof info.mode === 'bigint' ? Number(info.mode & 0o777n) : info.mode & 0o777
}
function owner(info) { return typeof info.uid === 'bigint' ? Number(info.uid) : info.uid }
function sameIdentity(left, right) {
  return left.dev === right.dev && left.ino === right.ino && left.size === right.size &&
    left.mtimeMs === right.mtimeMs && left.ctimeMs === right.ctimeMs && left.mode === right.mode &&
    left.uid === right.uid && left.nlink === right.nlink
}
function sameOwnedDirectory(left, right) {
  return right.isDirectory() && !right.isSymbolicLink() && left.dev === right.dev &&
    left.ino === right.ino && left.uid === right.uid && owner(right) === uid &&
    left.mode === right.mode && mode(right) === 0o700
}
function sameOwnedFileNode(left, right) {
  return right.isFile() && !right.isSymbolicLink() && left.dev === right.dev &&
    left.ino === right.ino && left.uid === right.uid && owner(right) === uid && left.mode === right.mode
}
function clone(value) { return JSON.parse(JSON.stringify(value)) }

async function lstat(path, options = undefined) {
  const info = await lstatNative(path, { bigint: true })
  if (options?.bigint === true) return info
  const numeric = new Set(['mode', 'uid', 'gid', 'nlink', 'size', 'mtimeMs', 'ctimeMs'])
  return new Proxy(info, { get(target, property) {
    if (numeric.has(property)) return Number(target[property])
    const value = Reflect.get(target, property, target)
    return typeof value === 'function' ? value.bind(target) : value
  } })
}

async function noSymlinkAncestors(path) {
  let cursor = path
  while (true) {
    try { assert(!(await lstat(cursor)).isSymbolicLink(), `${path} has a symlink ancestor`) }
    catch (error) { if (error.code !== 'ENOENT') throw error }
    const parent = dirname(cursor)
    if (parent === cursor) return
    cursor = parent
  }
}

async function absent(path, label) {
  try { await lstat(path); fail(`${label} already exists`) }
  catch (error) { if (error.message === `${label} already exists` || error.code !== 'ENOENT') throw error }
}

async function hashStream(path) {
  const hash = createHash('sha256')
  let size = 0
  for await (const chunk of createReadStream(path)) { size += chunk.length; hash.update(chunk) }
  return { sha256: hash.digest('hex'), size }
}

async function secureFile(path, expectedMode, expectedSha256, label, maximum = MAX_JSON,
  expectedSize = undefined, expectedUID = uid) {
  absolute(path, label); digest(expectedSha256, `${label} SHA-256`); await noSymlinkAncestors(path)
  const before = await lstat(path)
  assert(before.isFile() && !before.isSymbolicLink() && before.nlink === 1 &&
    before.uid === expectedUID && mode(before) === expectedMode && before.size >= 0 && before.size <= maximum,
  `${label} must be one exact ${expectedMode.toString(8)} regular file`)
  if (expectedSize !== undefined) assert(before.size === expectedSize, `${label} size drifted`)
  const observed = await hashStream(path)
  const after = await lstat(path)
  assert(sameIdentity(before, after) && observed.size === before.size, `${label} changed while read`)
  assert(observed.sha256 === expectedSha256, `${label} SHA-256 drifted`)
  return { path, snapshot: before, ...observed }
}

async function readJSON(path, expectedMode, expectedSha256, label) {
  const checked = await secureFile(path, expectedMode, expectedSha256, label)
  const bytes = await readFile(path)
  assert(bytes.length === checked.size && sha256(bytes) === expectedSha256 &&
    sameIdentity(checked.snapshot, await lstat(path)), `${label} changed while parsed`)
  rejectDuplicateJSONKeys(bytes, label)
  let value
  try { value = JSON.parse(bytes.toString('utf8')) } catch { fail(`${label} is invalid JSON`) }
  return { ...checked, bytes, value }
}

function rejectDuplicateJSONKeys(bytes, label) {
  const text = bytes.toString('utf8'); let cursor = 0
  const whitespace = () => { while (/\s/.test(text[cursor] ?? '')) cursor++ }
  const string = () => {
    assert(text[cursor] === '"', `${label} JSON string is invalid`); const start = cursor++
    while (cursor < text.length) {
      if (text[cursor] === '\\') { cursor += 2; continue }
      if (text[cursor++] === '"') { try { return JSON.parse(text.slice(start, cursor)) } catch { break } }
    }
    fail(`${label} JSON string is invalid`)
  }
  const value = () => {
    whitespace(); const token = text[cursor]
    if (token === '"') { string(); return }
    if (token === '{') {
      cursor++; whitespace(); const keys = new Set()
      if (text[cursor] === '}') { cursor++; return }
      while (true) {
        whitespace(); const key = string(); assert(!keys.has(key), `${label} has duplicate JSON key ${key}`); keys.add(key)
        whitespace(); assert(text[cursor++] === ':', `${label} JSON object is invalid`); value(); whitespace()
        if (text[cursor] === '}') { cursor++; return }
        assert(text[cursor++] === ',', `${label} JSON object is invalid`)
      }
    }
    if (token === '[') {
      cursor++; whitespace(); if (text[cursor] === ']') { cursor++; return }
      while (true) { value(); whitespace(); if (text[cursor] === ']') { cursor++; return }; assert(text[cursor++] === ',', `${label} JSON array is invalid`) }
    }
    const start = cursor
    while (cursor < text.length && !/[\s,\]}]/.test(text[cursor])) cursor++
    assert(cursor > start, `${label} JSON value is invalid`)
    try { JSON.parse(text.slice(start, cursor)) } catch { fail(`${label} JSON value is invalid`) }
  }
  value(); whitespace(); assert(cursor === text.length, `${label} has trailing JSON data`)
}

async function inspectDirectory(root, label, { allowSymlinks = false, omit = () => false,
  maximumFiles = 100_000, maximumBytes = 32 * 1024 * 1024 * 1024 } = {}) {
  absolute(root, label); await noSymlinkAncestors(root)
  const rootSnapshot = await lstat(root)
  assert(rootSnapshot.isDirectory() && !rootSnapshot.isSymbolicLink() && rootSnapshot.uid === uid &&
    (mode(rootSnapshot) & 0o022) === 0, `${label} root is unsafe`)
  const entries = []; const snapshots = new Map(); let fileCount = 0; let totalBytes = 0
  const visit = async (directory, prefix = '') => {
    for (const name of (await readdir(directory)).sort()) {
      assert(name && name !== '.' && name !== '..' && !name.includes('/') && !name.includes('\0'),
        `${label} has an unsafe name`)
      const relativePath = prefix ? `${prefix}/${name}` : name
      if (omit(relativePath)) continue
      const path = join(directory, name); const before = await lstat(path)
      assert(before.uid === uid, `${label} has a foreign entry`); snapshots.set(path, before)
      if (before.isSymbolicLink()) {
        assert(allowSymlinks, `${label} has unexpected symlink ${relativePath}`)
        const target = await readlink(path)
        assert(target && !isAbsolute(target) && !target.includes('\0'), `${label} symlink is unsafe`)
        const resolved = resolve(dirname(path), target)
        assert(resolved === root || within(root, resolved), `${label} symlink escapes root`)
        const real = await realpath(resolved)
        assert(real === root || within(root, real), `${label} symlink resolves outside root`)
        assert(sameIdentity(before, await lstat(path)) && target === await readlink(path),
          `${label} symlink changed while read`)
        entries.push({ path: relativePath, type: 'symlink', mode: mode(before), target })
      } else if (before.isDirectory()) {
        assert((mode(before) & 0o022) === 0, `${label} has writable directory`)
        entries.push({ path: relativePath, type: 'directory', mode: mode(before) })
        await visit(path, relativePath)
      } else {
        assert(before.isFile() && before.nlink === 1 && (mode(before) & 0o022) === 0,
          `${label} has unsafe file ${relativePath}`)
        fileCount++; totalBytes += before.size
        assert(fileCount <= maximumFiles && totalBytes <= maximumBytes, `${label} exceeds bound`)
        const observed = await hashStream(path)
        assert(observed.size === before.size && sameIdentity(before, await lstat(path)),
          `${label} file changed while read`)
        entries.push({ path: relativePath, type: 'file', mode: mode(before), size: before.size,
          sha256: observed.sha256 })
      }
    }
  }
  await visit(root)
  assert(entries.length > 0 && sameIdentity(rootSnapshot, await lstat(root)), `${label} root changed while read`)
  return { root, rootSnapshot, snapshots, entries, digest: sha256(canonical({
    rootMode: mode(rootSnapshot), entries,
  })) }
}

async function webClosure(root) {
  const inspected = await inspectDirectory(root, 'product web directory')
  const files = inspected.entries.filter((entry) => entry.type === 'file')
  assert(inspected.entries.every((entry) => entry.type === 'file' || entry.type === 'directory'),
    'product web directory must not contain symlinks')
  const digestValue = sha256(files.map((entry) => `${entry.path}\0${entry.size}\0${entry.sha256}\n`).join(''))
  return { ...inspected, digest: digestValue }
}

function validateStageRequest(value) {
  exactKeys(value, ['schemaVersion', 'status', 'identity', 'preparedOutputRoot', 'release', 'source',
    'product', 'pi', 'oauth', 'ca', 'node', 'playwright', 'runner', 'docker', 'colima', 'lima',
    'diskImage', 'sandboxExecutable', 'controller', 'ocr', 'engine', 'configs', 'vision'], 'freeze request')
  assert(value.schemaVersion === FREEZE_REQUEST_SCHEMA && value.status === 'authorized',
    'freeze request schema/status drifted')
  exactKeys(value.identity, ['environmentId', 'installId', 'generationId', 'platform'], 'identity')
  for (const key of ['environmentId', 'installId', 'generationId']) assert(
    typeof value.identity[key] === 'string' && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/.test(value.identity[key]),
    `${key} is invalid`)
  exactKeys(value.identity.platform, ['os', 'architecture'], 'platform')
  assert(value.identity.platform.os === 'darwin' && value.identity.platform.architecture === 'arm64',
    'freeze host must be darwin/arm64')
  absolute(value.preparedOutputRoot, 'prepared output root')
  exactKeys(value.release, ['root', 'manifestFile', 'manifestSha256'], 'release')
  exactKeys(value.source, ['bundleRoot', 'manifestFile', 'manifestSha256'], 'source')
  exactKeys(value.product, ['binaryFile', 'binarySha256', 'webDirectory', 'webClosureSha256'], 'product')
  exactKeys(value.pi, ['packageRoot', 'executableFile', 'packageClosureSha256', 'executableSha256'], 'Pi')
  exactKeys(value.playwright, ['packageRoot', 'packageClosureSha256', 'cliFile', 'cliSha256',
    'browserRoot', 'browserClosureSha256'], 'Playwright')
  for (const key of ['oauth', 'ca', 'node', 'runner', 'docker', 'colima', 'controller', 'ocr'])
    exactKeys(value[key], ['file', 'sha256'], key)
  exactKeys(value.lima, ['prefixRoot', 'prefixClosureSha256', 'infoDigest',
    'limactlFile', 'limactlSha256',
    'limaFile', 'limaSha256', 'templatesRoot', 'templatesClosureSha256', 'guestAgentFile',
    'guestAgentSize', 'guestAgentSha256'], 'Lima portable prefix')
  exactKeys(value.diskImage, ['file', 'size', 'sha256'], 'disk image')
  assert(Number.isSafeInteger(value.diskImage.size) && value.diskImage.size > 0 && value.diskImage.size <= MAX_DISK,
    'disk image size is invalid')
  exactKeys(value.sandboxExecutable, ['file', 'sha256'], 'sandbox executable')
  exactKeys(value.engine, ['profileName', 'contextName', 'endpointSocketPath', 'endpointDigest',
    'cpus', 'memoryGiB', 'diskGiB', 'engineMs'], 'fresh Engine authority')
  exactKeys(value.configs, ['C', 'D', 'E'], 'phase configs')
  for (const phase of ['C', 'D', 'E']) exactKeys(value.configs[phase], ['file', 'sha256'], `phase ${phase}`)
  exactKeys(value.vision, ['platform', 'vision'], 'Vision authority')
  exactKeys(value.vision.platform, ['os', 'architecture', 'macOSProductVersion', 'macOSBuildVersion',
    'darwinSysname', 'darwinRelease', 'darwinVersion', 'darwinMachine'], 'Vision platform')
  exactKeys(value.vision.vision, ['bundleIdentifier', 'bundleVersion', 'infoPlistSha256',
    'supportedRecognitionRevisions', 'requestedRecognitionRevision', 'recognitionLevel',
    'recognitionLanguage', 'usesLanguageCorrection', 'confidenceThreshold'], 'Vision engine')
  assert(value.vision.platform.os === 'darwin' && value.vision.platform.architecture === 'arm64' &&
    value.vision.vision.recognitionLevel === 'accurate' &&
    value.vision.vision.recognitionLanguage === 'en-US' &&
    value.vision.vision.usesLanguageCorrection === true &&
    Number.isInteger(value.vision.vision.requestedRecognitionRevision) &&
    Array.isArray(value.vision.vision.supportedRecognitionRevisions) &&
    value.vision.vision.supportedRecognitionRevisions.includes(value.vision.vision.requestedRecognitionRevision) &&
    value.vision.vision.confidenceThreshold > 0 && value.vision.vision.confidenceThreshold <= 1,
  'Vision authority drifted')
  digest(value.vision.vision.infoPlistSha256, 'Vision Info.plist SHA-256')
  for (const key of ['macOSProductVersion', 'macOSBuildVersion', 'darwinSysname', 'darwinRelease',
    'darwinVersion', 'darwinMachine']) assert(typeof value.vision.platform[key] === 'string' &&
    value.vision.platform[key].length > 0, `Vision platform ${key} is invalid`)
  absolute(value.engine.endpointSocketPath, 'fresh Engine endpoint socket')
  assertDarwinUnixSocketPath(value.engine.endpointSocketPath, 'fresh Engine endpoint socket')
  assert(productIDRE.test(value.engine.profileName) &&
    value.engine.profileName !== 'default' &&
    value.engine.contextName === `colima-${value.engine.profileName}` &&
    value.engine.endpointDigest === sha256(`unix://${value.engine.endpointSocketPath}`),
  'fresh Engine profile/derived Docker context/endpoint drifted')
  for (const key of ['cpus', 'memoryGiB', 'diskGiB']) assert(Number.isSafeInteger(value.engine[key]) &&
    value.engine[key] >= 1 && value.engine[key] <= 256, `fresh Engine ${key} is invalid`)
  assert(value.engine.diskGiB >= value.engine.memoryGiB && Number.isSafeInteger(value.engine.engineMs) &&
    value.engine.engineMs >= 1_000 && value.engine.engineMs <= 30 * 60_000,
  'fresh Engine sizes/timeout are invalid')
  for (const key of ['prefixClosureSha256', 'infoDigest', 'limactlSha256', 'limaSha256',
    'templatesClosureSha256', 'guestAgentSha256']) digest(value.lima[key], `Lima ${key}`)
  for (const key of ['prefixRoot', 'limactlFile', 'limaFile', 'templatesRoot', 'guestAgentFile'])
    absolute(value.lima[key], `Lima ${key}`)
  assert(Number.isSafeInteger(value.lima.guestAgentSize) && value.lima.guestAgentSize === 7_251_420,
    'Lima Linux-aarch64 guest agent size drifted')
  assert(value.lima.limactlFile === join(value.lima.prefixRoot, 'bin/limactl') &&
    value.lima.limaFile === join(value.lima.prefixRoot, 'bin/lima') &&
    value.lima.templatesRoot === join(value.lima.prefixRoot, 'share/lima/templates') &&
    value.lima.guestAgentFile === join(value.lima.prefixRoot,
      'share/lima/lima-guestagent.Linux-aarch64.gz') &&
    basename(value.docker.file) === 'docker' &&
    !overlaps(value.lima.prefixRoot, dirname(value.docker.file)),
  'canonical Engine executable/prefix topology drifted')
  rejectForbidden(value, 'freeze request')
}

function rejectForbidden(value, label, path = '') {
  if (Array.isArray(value)) return value.forEach((item, index) => rejectForbidden(item, label, `${path}[${index}]`))
  if (!value || typeof value !== 'object') {
    if (typeof value === 'string') assert(!/-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----|\bBearer\s+[A-Za-z0-9._~-]+/i.test(value),
      `${label} contains secret bytes at ${path}`)
    return
  }
  for (const [key, item] of Object.entries(value)) {
    assert(!/(?:registry|ocrModel|OCR_MODEL|modelFile|modelPath|clientSecret|accessToken|refreshToken|password|privateKey|authorization|secret)/i.test(key),
      `${label} contains forbidden legacy, OCR model, or secret field ${path ? `${path}.` : ''}${key}`)
    rejectForbidden(item, label, path ? `${path}.${key}` : key)
  }
}

function staticPreparationRequest(request, outputRoot, pins) {
  return {
    schemaVersion: STATIC_REQUEST_SCHEMA, status: 'authorized', identity: request.identity,
    release: request.release, source: request.source, product: request.product, pi: request.pi,
    oauth: request.oauth, ca: request.ca, node: request.node, playwright: request.playwright,
    configs: request.configs,
    runner: { moduleFile: request.runner.file, moduleSha256: request.runner.sha256 },
    serviceController: { executableFile: request.controller.file, sha256: request.controller.sha256 },
    ocr: { executableFile: request.ocr.file, executableSha256: request.ocr.sha256,
      engineBindingFile: join(outputRoot, 'vision-engine-binding.json'),
      engineBindingSha256: pins.engineBindingSha256,
      sandboxExecutableFile: request.sandboxExecutable.file,
      sandboxExecutableSha256: request.sandboxExecutable.sha256,
      sandboxProfileFile: join(outputRoot, 'ocr-deny-network.sb'),
      sandboxProfileSha256: pins.ocrProfileSha256, language: 'eng' },
    engine: { limaPrefixRoot: request.lima.prefixRoot,
      limaPrefixClosureSha256: request.lima.prefixClosureSha256,
      limaInfoDigest: request.lima.infoDigest,
      limaFile: request.lima.limactlFile, limaSha256: request.lima.limactlSha256,
      limaWrapperFile: request.lima.limaFile, limaWrapperSha256: request.lima.limaSha256,
      limaTemplatesRoot: request.lima.templatesRoot,
      limaTemplatesClosureSha256: request.lima.templatesClosureSha256,
      limaGuestAgentFile: request.lima.guestAgentFile,
      limaGuestAgentSize: request.lima.guestAgentSize,
      limaGuestAgentSha256: request.lima.guestAgentSha256,
      diskImageFile: request.diskImage.file, diskImageSize: request.diskImage.size,
      diskImageSha256: request.diskImage.sha256,
      sandboxExecutableFile: request.sandboxExecutable.file,
      sandboxExecutableSha256: request.sandboxExecutable.sha256,
      sandboxProfileFile: join(outputRoot, 'engine-local-only.sb'),
      sandboxProfileSha256: pins.engineProfileSha256, ...request.engine,
      contextName: `colima-${request.engine.profileName}` },
    docker: request.docker, colima: request.colima,
  }
}

async function write0400(root, name, value) {
  const path = join(root, name); const bytes = Buffer.isBuffer(value) ? value : jsonBytes(value)
  const handle = await open(path, 'wx', 0o600)
  try { await handle.writeFile(bytes); await handle.sync(); await handle.chmod(0o400) } finally { await handle.close() }
  const info = await lstat(path)
  assert(info.isFile() && info.uid === uid && info.nlink === 1 && mode(info) === 0o400,
    `generated ${name} is unsafe`)
  assert((await readFile(path)).equals(bytes), `generated ${name} changed after write`)
  return { file: path, sha256: sha256(bytes), bytes }
}

async function validateStageInputs(request) {
  const files = []
  files.push(await secureFile(request.release.manifestFile, 0o444, request.release.manifestSha256, 'release manifest'))
  files.push(await secureFile(request.source.manifestFile, 0o444, request.source.manifestSha256, 'source manifest'))
  files.push(await secureFile(request.product.binaryFile, 0o500, request.product.binarySha256, 'product binary', MAX_EXECUTABLE))
  files.push(await secureFile(request.pi.executableFile, 0o500, request.pi.executableSha256, 'Pi executable', MAX_EXECUTABLE))
  files.push(await secureFile(request.oauth.file, 0o600, request.oauth.sha256, 'OAuth authority'))
  files.push(await secureFile(request.ca.file, 0o400, request.ca.sha256, 'CA authority'))
  for (const key of ['node', 'docker', 'colima', 'controller', 'ocr'])
    files.push(await secureFile(request[key].file, 0o500, request[key].sha256, key, MAX_EXECUTABLE))
  files.push(await secureFile(request.lima.limactlFile, 0o500,
    request.lima.limactlSha256, 'canonical limactl executable', MAX_EXECUTABLE))
  files.push(await secureFile(request.lima.limaFile, 0o500,
    request.lima.limaSha256, 'canonical Lima wrapper', MAX_EXECUTABLE))
  files.push(await secureFile(request.lima.guestAgentFile, 0o400,
    request.lima.guestAgentSha256, 'canonical Lima Linux-aarch64 guest agent', MAX_EXECUTABLE,
    request.lima.guestAgentSize))
  files.push(await secureFile(request.runner.file, 0o400, request.runner.sha256, 'Runner module'))
  for (const phase of ['C', 'D', 'E']) files.push(await secureFile(request.configs[phase].file,
    0o400, request.configs[phase].sha256, `phase ${phase} config`))
  files.push(await secureFile(request.playwright.cliFile, 0o400, request.playwright.cliSha256,
    'Playwright CLI', MAX_EXECUTABLE))
  files.push(await secureFile(request.diskImage.file, 0o400, request.diskImage.sha256,
    'Colima disk image', MAX_DISK, request.diskImage.size))
  files.push(await secureFile(request.sandboxExecutable.file, 0o755,
    request.sandboxExecutable.sha256, 'system sandbox executable', 2 * 1024 * 1024, undefined, 0))
  const identities = new Map()
  for (const item of files) {
    const identity = `${item.snapshot.dev}:${item.snapshot.ino}`
    assert(!identities.has(identity), `frozen files alias ${identities.get(identity)} and ${item.path}`)
    identities.set(identity, item.path)
  }
  const directories = {
    release: await inspectDirectory(request.release.root, 'release root'),
    source: await inspectDirectory(request.source.bundleRoot, 'source bundle'),
    web: await webClosure(request.product.webDirectory),
    pi: await inspectDirectory(request.pi.packageRoot, 'Pi package', { allowSymlinks: true,
      omit: (path) => path === 'node_modules/.bin' || path.startsWith('node_modules/.bin/') }),
    playwright: await inspectDirectory(request.playwright.packageRoot, 'Playwright package', { allowSymlinks: true }),
    browser: await inspectDirectory(request.playwright.browserRoot, 'Playwright browser', { allowSymlinks: true }),
    limaPrefix: await inspectDirectory(request.lima.prefixRoot, 'canonical Lima prefix',
      { maximumFiles: 124, maximumBytes: 39_872_398 }),
    limaTemplates: await inspectDirectory(request.lima.templatesRoot, 'canonical Lima templates',
      { maximumFiles: 121, maximumBytes: 148_059 }),
  }
  assert(request.release.manifestFile === join(request.release.root, 'release-manifest.v1.json'),
    'release manifest topology drifted')
  assert(request.source.manifestFile === join(request.source.bundleRoot, 'source-manifest.json'),
    'source manifest topology drifted')
  assert(request.pi.executableFile === join(request.pi.packageRoot, 'dist/cli.js'), 'Pi executable topology drifted')
  assert(within(request.playwright.packageRoot, request.playwright.cliFile), 'Playwright CLI escapes package')
  assert(directories.web.digest === request.product.webClosureSha256, 'product web closure drifted')
  assert(directories.pi.digest === request.pi.packageClosureSha256, 'Pi closure drifted')
  assert(directories.playwright.digest === request.playwright.packageClosureSha256, 'Playwright package closure drifted')
  assert(directories.browser.digest === request.playwright.browserClosureSha256, 'Playwright browser closure drifted')
  const prefixEntries = directories.limaPrefix.entries
  const prefixFiles = prefixEntries.filter((entry) => entry.type === 'file')
  const prefixDirectories = prefixEntries.filter((entry) => entry.type === 'directory')
  const templateFiles = directories.limaTemplates.entries.filter((entry) => entry.type === 'file')
  const templateDirectories = directories.limaTemplates.entries.filter((entry) => entry.type === 'directory')
  assert(mode(directories.limaPrefix.rootSnapshot) === 0o700 &&
    directories.limaPrefix.digest === request.lima.prefixClosureSha256 &&
    mode(directories.limaTemplates.rootSnapshot) === 0o700 &&
    directories.limaTemplates.digest === request.lima.templatesClosureSha256 &&
    prefixFiles.length === 124 && prefixDirectories.length === 7 &&
    prefixFiles.reduce((sum, entry) => sum + entry.size, 0) === 39_872_398 &&
    templateFiles.length === 121 && templateDirectories.length === 3 &&
    templateFiles.reduce((sum, entry) => sum + entry.size, 0) === 148_059 &&
    prefixEntries.every((entry) => entry.mode === (entry.type === 'directory' ? 0o700 :
      entry.path.startsWith('bin/') ? 0o500 : 0o400)) &&
    directories.limaTemplates.entries.every((entry) => entry.mode ===
      (entry.type === 'directory' ? 0o700 : 0o400)),
  'canonical Lima portable prefix closure drifted')
  assert(prefixFiles.some((entry) => entry.path === 'bin/limactl' &&
      entry.sha256 === request.lima.limactlSha256) &&
    prefixFiles.some((entry) => entry.path === 'bin/lima' &&
      entry.sha256 === request.lima.limaSha256) &&
    prefixFiles.some((entry) => entry.path === 'share/lima/lima-guestagent.Linux-aarch64.gz' &&
      entry.size === request.lima.guestAgentSize && entry.sha256 === request.lima.guestAgentSha256) &&
    templateFiles.some((entry) => entry.path === 'default.yaml') &&
    templateFiles.some((entry) => entry.path === '_images/ubuntu.yaml') &&
    templateFiles.some((entry) => entry.path === '_default/mounts.yaml'),
  'canonical Lima portable prefix required entries drifted')
  assert(request.lima.infoDigest === deriveLimaInfoDigest(request.lima, directories.limaTemplates),
    'canonical Lima info authority drifted')
  const roots = [request.release.root, request.source.bundleRoot, request.product.webDirectory,
    request.pi.packageRoot, request.playwright.packageRoot, request.playwright.browserRoot,
    request.lima.prefixRoot]
  for (let left = 0; left < roots.length; left++) for (let right = left + 1; right < roots.length; right++)
    assert(!overlaps(roots[left], roots[right]), 'frozen input roots overlap')
  return { files, directories }
}

async function revalidateStageInputs(observed) {
  for (const item of observed.files) assert(sameIdentity(item.snapshot, await lstat(item.path)),
    `frozen file TOCTOU drifted: ${item.path}`)
  for (const closure of Object.values(observed.directories)) {
    assert(sameIdentity(closure.rootSnapshot, await lstat(closure.root)),
      `frozen directory TOCTOU drifted: ${closure.root}`)
    for (const [path, snapshot] of closure.snapshots) assert(sameIdentity(snapshot, await lstat(path)),
      `frozen directory entry TOCTOU drifted: ${path}`)
  }
}

export async function stageO4RealInputs({ requestFile, requestSha256, outputRoot }, dependencies = {}) {
  absolute(requestFile, 'freeze request'); digest(requestSha256, 'freeze request SHA-256')
  absolute(outputRoot, 'frozen output root'); await noSymlinkAncestors(outputRoot); await absent(outputRoot, 'frozen output root')
  const parent = dirname(outputRoot); const parentBefore = await lstat(parent)
  assert(parentBefore.isDirectory() && parentBefore.uid === uid && (mode(parentBefore) & 0o022) === 0,
    'frozen output parent is unsafe')
  const requestInput = await readJSON(requestFile, 0o400, requestSha256, 'freeze request')
  validateStageRequest(requestInput.value); const request = requestInput.value
  const controllerCapability = Object.freeze({ executableFile: request.controller.file,
    executableSha256: request.controller.sha256 })
  assert(!overlaps(outputRoot, request.preparedOutputRoot), 'frozen and prepared output roots overlap')
  const inputPaths = [requestFile, request.release.root, request.source.bundleRoot, request.product.binaryFile,
    request.product.webDirectory, request.pi.packageRoot, request.oauth.file, request.ca.file, request.node.file,
    request.playwright.packageRoot, request.playwright.browserRoot, request.runner.file, request.docker.file,
    request.colima.file, request.lima.prefixRoot, request.lima.limactlFile, request.lima.limaFile,
    request.lima.templatesRoot, request.lima.guestAgentFile,
    request.diskImage.file, request.controller.file, request.ocr.file,
    ...Object.values(request.configs).map((item) => item.file)]
  for (const root of inputPaths)
    assert(!overlaps(outputRoot, root) && !overlaps(request.preparedOutputRoot, root), 'output root overlaps input closure')
  const observed = await validateStageInputs(request)
  const staging = join(parent, `.${outputRoot.slice(parent.length + 1)}.stage-${randomBytes(12).toString('hex')}`)
  await absent(staging, 'freeze staging root')
  let stagingIdentity; let stagingSnapshot
  try {
    await mkdir(staging, { mode: 0o700 })
    stagingSnapshot = await lstat(staging, { bigint: true })
    stagingIdentity = ownedPathIdentity(stagingSnapshot)
    assert(stagingSnapshot.isDirectory() && !stagingSnapshot.isSymbolicLink() &&
      stagingSnapshot.uid === BigInt(uid) && Number(stagingSnapshot.mode & 0o777n) === 0o700,
      'freeze staging root must be one exact owner-controlled 0700 directory')
    const parentAfterStaging = await lstat(parent)
    const ocrSandbox = await write0400(staging, 'ocr-deny-network.sb', Buffer.from(ocrProfile))
    const engineSandbox = await write0400(staging, 'engine-local-only.sb', Buffer.from(engineProfile))
    const finalOCRProfile = join(outputRoot, 'ocr-deny-network.sb')
    const bindingWithoutDigest = {
      schemaVersion: 'chora.m1-o4-system-managed-ocr-engine-binding.v1', status: 'authorized',
      engine: 'system_managed_ocr_engine', modelBytes: 'opaque_unavailable',
      platform: request.vision.platform, vision: request.vision.vision,
      executable: { pathSha256: sha256(request.ocr.file), sha256: request.ocr.sha256 },
      sandbox: { executableFile: request.sandboxExecutable.file,
        executablePathSha256: sha256(request.sandboxExecutable.file),
        executableSha256: request.sandboxExecutable.sha256, profileFile: finalOCRProfile,
        profilePathSha256: sha256(finalOCRProfile), profileSha256: ocrSandbox.sha256,
        networkRule: 'deny network*' },
    }
    const engineBinding = await write0400(staging, 'vision-engine-binding.json', {
      ...bindingWithoutDigest, bindingDigest: sha256(canonical(bindingWithoutDigest)),
    })
    const staticRequest = await write0400(staging, 'static-preparation-request.json',
      staticPreparationRequest(request, outputRoot, { engineBindingSha256: engineBinding.sha256,
        ocrProfileSha256: ocrSandbox.sha256, engineProfileSha256: engineSandbox.sha256 }))
    const generated = {
      staticPreparationRequest: { file: join(outputRoot, 'static-preparation-request.json'),
        sha256: staticRequest.sha256 },
      ocrEngineBinding: { file: join(outputRoot, 'vision-engine-binding.json'), sha256: engineBinding.sha256 },
      ocrSandboxProfile: { file: finalOCRProfile, sha256: ocrSandbox.sha256 },
      engineSandboxProfile: { file: join(outputRoot, 'engine-local-only.sb'), sha256: engineSandbox.sha256 },
    }
    const manifestBase = {
      schemaVersion: FROZEN_INPUTS_SCHEMA, status: 'passed', identity: request.identity,
      preparedOutputRoot: request.preparedOutputRoot, networkUsed: false, executablesSpawned: true,
      requestSha256, generated,
      bindings: {
        release: request.release, source: request.source, product: request.product, pi: request.pi,
        oauth: { file: request.oauth.file, opaqueSha256: request.oauth.sha256 }, ca: request.ca,
        node: request.node, playwright: request.playwright, runner: request.runner,
        docker: request.docker, colima: request.colima, lima: request.lima, diskImage: request.diskImage,
        sandboxExecutable: request.sandboxExecutable, controller: request.controller, ocr: request.ocr,
        engine: { ...request.engine, contextName: `colima-${request.engine.profileName}` },
        configs: request.configs,
        closureDigests: Object.fromEntries(Object.entries(observed.directories).map(([key, item]) => [key, item.digest])),
      },
      selfDigest: null,
    }
    const manifest = { ...manifestBase, selfDigest: sha256(canonical(manifestBase)) }
    const frozen = await write0400(staging, 'frozen-inputs.json', manifest)
    if (dependencies.beforeCommit) await dependencies.beforeCommit({ staging, outputRoot })
    await revalidateStageInputs(observed)
    assert(sameIdentity(requestInput.snapshot, await lstat(requestFile)), 'freeze request TOCTOU drifted')
    assert(sameOwnedDirectory(stagingSnapshot, await lstat(staging, { bigint: true })),
      'freeze staging directory identity changed before publication')
    await absent(outputRoot, 'frozen output root')
    assert(sameIdentity(parentAfterStaging, await lstat(parent)), 'frozen output parent TOCTOU drifted')
    await publishOwnedPath({ capability: controllerCapability, sourcePath: staging,
      targetPath: outputRoot, identity: stagingIdentity, disposition: 'recursive_directory' },
    dependencies.publishOwnedPath)
    return Object.freeze({ outputRoot, manifestFile: join(outputRoot, 'frozen-inputs.json'),
      manifestSha256: frozen.sha256, staticPreparationRequestFile: generated.staticPreparationRequest.file,
      staticPreparationRequestSha256: generated.staticPreparationRequest.sha256 })
  } catch (error) {
    let failure = error
    if (stagingIdentity) {
      try { await cleanupOwnedPath({ capability: controllerCapability, path: staging,
        identity: stagingIdentity, disposition: 'recursive_directory' }, dependencies.cleanupOwnedPath) }
      catch (cleanupError) {
        failure = new AggregateError([error, cleanupError], `${error.message}; ${cleanupError.message}`,
          { cause: error })
      }
    }
    try { await absent(outputRoot, 'frozen output root') }
    catch (outputError) {
      const errors = failure instanceof AggregateError ? [...failure.errors, outputError] : [failure, outputError]
      failure = new AggregateError(errors, `${failure.message}; ${outputError.message}`,
        { cause: error })
    }
    throw failure
  }
}

function validateFinalizeRequest(value) {
  exactKeys(value, ['schemaVersion', 'status', 'frozenManifestFile', 'frozenManifestSha256',
    'preparedOutputRoot', 'preparedManifestSha256', 'preparedClosureSha256', 'outputRoot', 'resolutions'],
  'finalization request')
  assert(value.schemaVersion === FINALIZE_REQUEST_SCHEMA && value.status === 'authorized',
    'finalization request schema/status drifted')
  absolute(value.frozenManifestFile, 'frozen manifest'); digest(value.frozenManifestSha256, 'frozen manifest')
  absolute(value.preparedOutputRoot, 'prepared output root'); digest(value.preparedManifestSha256, 'prepared manifest')
  digest(value.preparedClosureSha256, 'prepared closure')
  absolute(value.outputRoot, 'materialization output root')
  assert(!overlaps(value.outputRoot, value.preparedOutputRoot),
    'materialization and prepared output roots overlap')
  exactKeys(value.resolutions, ['candidateConfig', 'totalManifest'], 'template resolutions')
  rejectForbidden(value, 'finalization request')
}

function resolveTemplate(template, resolution, path) {
  if (template === null || template === 'static_unresolved') {
    if (template === null && resolution === undefined && materializerNulls.has(path)) return null
    assert(resolution !== undefined && resolution !== null, `missing dynamic resolution ${path}`)
    return clone(resolution)
  }
  if (Array.isArray(template)) {
    assert(resolution === undefined, `static template array overwrite forbidden at ${path}`)
    return clone(template)
  }
  if (template && typeof template === 'object') {
    const supplied = resolution ?? {}
    assert(supplied && typeof supplied === 'object' && !Array.isArray(supplied),
      `resolution object is invalid at ${path}`)
    for (const key of Object.keys(supplied)) assert(Object.hasOwn(template, key),
      `resolution adds unknown field ${path}.${key}`)
    const output = {}
    for (const [key, value] of Object.entries(template)) output[key] = resolveTemplate(value,
      Object.hasOwn(supplied, key) ? supplied[key] : undefined, `${path}.${key}`)
    return output
  }
  assert(resolution === undefined, `static template overwrite forbidden at ${path}`)
  return template
}

function assertResolved(value, path) {
  if (value === null) { assert(materializerNulls.has(path), `unresolved dynamic value remains at ${path}`); return }
  if (Array.isArray(value)) return value.forEach((item, index) => assertResolved(item, `${path}[${index}]`))
  if (value && typeof value === 'object') for (const [key, item] of Object.entries(value))
    assertResolved(item, `${path}.${key}`)
}

function validateFrozenManifest(value, rawSha256) {
  exactKeys(value, ['schemaVersion', 'status', 'identity', 'preparedOutputRoot', 'networkUsed',
    'executablesSpawned', 'requestSha256', 'generated', 'bindings', 'selfDigest'], 'frozen manifest')
  assert(value.schemaVersion === FROZEN_INPUTS_SCHEMA && value.status === 'passed' &&
    value.networkUsed === false && value.executablesSpawned === true, 'frozen manifest status drifted')
  assert(value.bindings?.engine?.contextName === `colima-${value.bindings?.engine?.profileName}`,
    'frozen Engine Docker context is not exactly derived from its Colima profile')
  const expected = { ...value, selfDigest: null }
  assert(value.selfDigest === sha256(canonical(expected)), 'frozen manifest self-digest drifted')
  digest(rawSha256, 'frozen manifest raw SHA-256')
}

function sameJSON(left, right, label) { assert(canonical(left) === canonical(right), label) }

function bindMaterializedOutputPaths(preparedOutputRoot, outputRoot, candidate, total) {
  const candidateManifestFile = join(outputRoot, 'candidate-manifest.json')
  const offlineBundleEvidenceFile = join(outputRoot, 'offline-bundle-evidence.json')
  const candidateConfigFile = join(outputRoot, 'candidate-config.json')
  const d = total.phases?.D?.environment
  const e = total.phases?.E?.environment
  const sentinelOrCanonical = (value, sentinel, canonical) =>
    value === sentinel || value === canonical
  assert(candidate?.paths && total.candidate && d && e &&
    sentinelOrCanonical(candidate.paths.candidateManifestFile,
      join(preparedOutputRoot, 'candidate-manifest.template.json'), candidateManifestFile) &&
    sentinelOrCanonical(candidate.paths.offlineBundleEvidenceFile, null,
      offlineBundleEvidenceFile) &&
    sentinelOrCanonical(total.candidate.configFile,
      join(preparedOutputRoot, 'candidate-config.template.json'), candidateConfigFile) &&
    sentinelOrCanonical(d.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE, null,
      candidateConfigFile) &&
    sentinelOrCanonical(e.CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE, null,
      candidateManifestFile) &&
    sentinelOrCanonical(e.CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE, null,
      offlineBundleEvidenceFile),
  'materialized output path splice is forbidden')
  candidate.paths.candidateManifestFile = candidateManifestFile
  candidate.paths.offlineBundleEvidenceFile = offlineBundleEvidenceFile
  total.candidate.configFile = candidateConfigFile
  d.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE = candidateConfigFile
  e.CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE = candidateManifestFile
  e.CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE = offlineBundleEvidenceFile
}

function assertMaterializedOutputPaths(outputRoot, candidate, total) {
  assert(candidate.paths.candidateManifestFile === join(outputRoot, 'candidate-manifest.json') &&
    candidate.paths.offlineBundleEvidenceFile === join(outputRoot, 'offline-bundle-evidence.json') &&
    total.candidate.configFile === join(outputRoot, 'candidate-config.json') &&
    total.phases.D.environment.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE ===
      join(outputRoot, 'candidate-config.json') &&
    total.phases.E.environment.CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE ===
      join(outputRoot, 'candidate-manifest.json') &&
    total.phases.E.environment.CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE ===
      join(outputRoot, 'offline-bundle-evidence.json'),
  'materialized output bindings do not match the immutable output root')
}

function validateResolvedBindings(frozen, candidate, total) {
  sameJSON(candidate.identity, frozen.identity, 'candidate identity drifted')
  sameJSON(total.identity, frozen.identity, 'total identity drifted')
  const b = frozen.bindings; const generated = frozen.generated
  assert(b.engine.contextName === `colima-${b.engine.profileName}` &&
    candidate.authority.dockerContext === b.engine.contextName &&
    candidate.private.colimaProfile === b.engine.profileName &&
    total.engine.profileName === b.engine.profileName &&
    total.engine.contextName === b.engine.contextName,
  'resolved Engine profile/derived Docker context binding drifted')
  assert(candidate.paths.sourceBundleRoot === b.source.bundleRoot &&
    candidate.paths.sourceManifestFile === b.source.manifestFile &&
    candidate.paths.binaryFile === b.product.binaryFile && candidate.paths.webDirectory === b.product.webDirectory &&
    candidate.paths.authFile === b.oauth.file && candidate.paths.caFile === b.ca.file &&
    candidate.paths.actualReleaseRoot === b.release.root &&
    candidate.paths.actualReleaseManifestFile === b.release.manifestFile &&
    candidate.paths.dockerCLIFile === b.docker.file && candidate.paths.dockerClientSourceFile === b.docker.file &&
    candidate.paths.colimaSourceFile === b.colima.file, 'candidate static paths drifted')
  assert(candidate.authority.sourceManifestSha256 === b.source.manifestSha256 &&
    candidate.authority.binarySha256 === b.product.binarySha256 &&
    candidate.authority.webAggregateSha256 === b.product.webClosureSha256 &&
    candidate.authority.authFileSha256 === b.oauth.opaqueSha256 &&
    candidate.authority.caFileSha256 === b.ca.sha256 &&
    candidate.authority.dockerCLISha256 === b.docker.sha256 &&
    candidate.authority.dockerClientSourceSha256 === b.docker.sha256 &&
    candidate.authority.colimaSourceSha256 === b.colima.sha256 &&
    candidate.authority.limaPrefixClosureSha256 === b.lima.prefixClosureSha256 &&
    candidate.authority.limaInfoDigest === b.lima.infoDigest &&
    candidate.authority.limaSha256 === b.lima.limactlSha256 &&
    candidate.authority.diskImageSha256 === b.diskImage.sha256 &&
    candidate.authority.diskImageSize === b.diskImage.size &&
    candidate.authority.sandboxExecutableSha256 === b.sandboxExecutable.sha256 &&
    candidate.authority.sandboxProfileSha256 === generated.engineSandboxProfile.sha256,
  'candidate static authority drifted')
  assert(total.runner.moduleFile === b.runner.file && total.runner.moduleSha256 === b.runner.sha256 &&
    total.serviceController.executableFile === b.controller.file &&
    total.serviceController.sha256 === b.controller.sha256 && total.ocr.executableFile === b.ocr.file &&
    total.ocr.executableSha256 === b.ocr.sha256 &&
    total.ocr.engineBindingFile === generated.ocrEngineBinding.file &&
    total.ocr.engineBindingSha256 === generated.ocrEngineBinding.sha256 &&
    total.ocr.sandboxExecutableFile === b.sandboxExecutable.file &&
    total.ocr.sandboxExecutableSha256 === b.sandboxExecutable.sha256 &&
    total.ocr.sandboxProfileFile === generated.ocrSandboxProfile.file &&
    total.ocr.sandboxProfileSha256 === generated.ocrSandboxProfile.sha256 && total.ocr.language === 'eng',
  'total Runner/controller/OCR binding drifted')
  assert(total.engine.colimaFile === b.colima.file && total.engine.colimaSha256 === b.colima.sha256 &&
    total.engine.limaPrefixRoot === b.lima.prefixRoot &&
    total.engine.limaPrefixClosureSha256 === b.lima.prefixClosureSha256 &&
    total.engine.limaInfoDigest === b.lima.infoDigest &&
    total.engine.limaFile === b.lima.limactlFile && total.engine.limaSha256 === b.lima.limactlSha256 &&
    total.engine.limaWrapperFile === b.lima.limaFile &&
    total.engine.limaWrapperSha256 === b.lima.limaSha256 &&
    total.engine.limaTemplatesRoot === b.lima.templatesRoot &&
    total.engine.limaTemplatesClosureSha256 === b.lima.templatesClosureSha256 &&
    total.engine.limaGuestAgentFile === b.lima.guestAgentFile &&
    total.engine.limaGuestAgentSize === b.lima.guestAgentSize &&
    total.engine.limaGuestAgentSha256 === b.lima.guestAgentSha256 &&
    total.engine.dockerFile === b.docker.file && total.engine.dockerSha256 === b.docker.sha256 &&
    total.engine.diskImageFile === b.diskImage.file && total.engine.diskImageSize === b.diskImage.size &&
    total.engine.diskImageSha256 === b.diskImage.sha256 &&
    total.engine.sandboxExecutableFile === b.sandboxExecutable.file &&
    total.engine.sandboxExecutableSha256 === b.sandboxExecutable.sha256 &&
    total.engine.sandboxProfileFile === generated.engineSandboxProfile.file &&
    total.engine.sandboxProfileSha256 === generated.engineSandboxProfile.sha256,
  'total fresh Engine binding drifted')
  assert(total.playwright.nodeFile === b.node.file && total.playwright.nodeSha256 === b.node.sha256 &&
    total.playwright.cliFile === b.playwright.cliFile && total.playwright.cliSha256 === b.playwright.cliSha256 &&
    total.playwright.packageRoot === b.playwright.packageRoot &&
    total.playwright.packageClosureSha256 === b.playwright.packageClosureSha256 &&
    total.playwright.browserRoot === b.playwright.browserRoot &&
    total.playwright.browserClosureSha256 === b.playwright.browserClosureSha256,
  'total Playwright closure drifted')
  for (const phase of ['C', 'D', 'E']) assert(total.phases[phase].configFile === b.configs[phase].file &&
    total.phases[phase].configSha256 === b.configs[phase].sha256, `total phase ${phase} config drifted`)
  assert(candidate.paths.preflightFile === total.engine.zeroImagePreflightFile &&
    candidate.paths.tupleFile === total.candidate.tupleFile &&
    candidate.paths.installRoot === total.roots.installRoot && candidate.paths.dataRoot === total.roots.dataRoot &&
    candidate.paths.stateRoot === total.roots.stateRoot &&
    candidate.paths.receiptDirectory === total.roots.receiptRoot,
  'candidate/total dynamic root or preflight binding drifted')
  exactKeys(total.repository, ['root', 'commit', 'closureSha256'], 'total repository authority')
  absolute(candidate.paths.repositoryRoot, 'candidate repository root')
  assert(commitRE.test(candidate.authority.repositoryCommit),
    'candidate repository commit is invalid')
  digest(candidate.authority.repositoryClosureSha256, 'candidate repository closure')
  assert(candidate.paths.repositoryRoot === total.repository.root &&
    candidate.authority.repositoryCommit === total.repository.commit &&
    candidate.authority.repositoryClosureSha256 === total.repository.closureSha256,
  'candidate/total repository authority drifted')
  assert(total.status === 'authorized' && total.acceptanceClass === 'real_acceptance',
    'total execution authority is not exact real acceptance')
}

export async function publishOwnerOnlyJSON0400({ outputFile, value, capability }, dependencies = {}) {
  const path = outputFile
  const controllerCapability = validateOwnedPathCapability(capability,
    'materialization request publication capability')
  absolute(path, 'materialization request output'); await noSymlinkAncestors(path); await absent(path, 'materialization request output')
  const parent = dirname(path); const parentInfo = await lstat(parent)
  assert(parentInfo.isDirectory() && parentInfo.uid === uid && (mode(parentInfo) & 0o022) === 0,
    'materialization request parent is unsafe')
  const temporary = join(parent, `.${path.slice(parent.length + 1)}.tmp-${randomBytes(12).toString('hex')}`)
  const bytes = jsonBytes(value); let handle; let temporaryIdentity; let temporarySnapshot
  try {
    handle = await open(temporary, 'wx', 0o600); await handle.writeFile(bytes); await handle.sync()
    await handle.chmod(0o400); temporarySnapshot = await handle.stat({ bigint: true })
    temporaryIdentity = ownedPathIdentity(temporarySnapshot)
    assert(temporarySnapshot.isFile() && !temporarySnapshot.isSymbolicLink() &&
      temporarySnapshot.uid === BigInt(uid) && temporarySnapshot.nlink === 1n &&
      Number(temporarySnapshot.mode & 0o777n) === 0o400,
    'materialization request temporary must be one exact owner-controlled 0400 file')
    await handle.close(); handle = undefined
    if (dependencies.beforePublish) await dependencies.beforePublish({ temporary, outputFile: path })
    const beforeLink = await lstat(temporary, { bigint: true })
    assert(sameOwnedFileNode(temporarySnapshot, beforeLink) && beforeLink.nlink === 1n &&
      Number(beforeLink.mode & 0o777n) === 0o400,
      'materialization request temporary identity changed before publication')
    await absent(path, 'materialization request output')
    await publishOwnedPath({ capability: controllerCapability, sourcePath: temporary, targetPath: path,
      identity: temporaryIdentity, disposition: 'regular_file' }, dependencies.publishOwnedPath)
    const info = await lstat(path, { bigint: true })
    assert(sameOwnedFileNode(temporarySnapshot, info) && info.nlink === 1n &&
      Number(info.mode & 0o777n) === 0o400 &&
      sha256(await readFile(path)) === sha256(bytes), 'materialization request publication drifted')
    return { outputFile: path, sha256: sha256(bytes) }
  } catch (error) {
    const cleanupErrors = []
    if (handle) try { await handle.close() } catch (closeError) { cleanupErrors.push(closeError) }
    if (temporaryIdentity) try {
      await cleanupOwnedPath({ capability: controllerCapability, path: path, identity: temporaryIdentity,
        disposition: 'regular_file' }, dependencies.cleanupOwnedPath)
    } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    if (temporaryIdentity) try {
      await cleanupOwnedPath({ capability: controllerCapability, path: temporary, identity: temporaryIdentity,
        disposition: 'regular_file' }, dependencies.cleanupOwnedPath)
    } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    if (cleanupErrors.length) throw new AggregateError([error, ...cleanupErrors],
      `${error.message}; ${cleanupErrors.map((item) => item.message).join('; ')}`, { cause: error })
    throw error
  }
}

function materializationArtifacts(release, frozen) {
  assert(release.schema_version === 'chora.release-assets-manifest.v1' &&
    release.release_id && release.spec_sha256 && Array.isArray(release.roles) &&
    Array.isArray(release.artifacts) && release.artifacts.length === 2,
  'release manifest cannot form materialization artifacts')
  const byRole = new Map(release.roles.map((item) => [item.role, item]))
  return [...release.artifacts].sort((left, right) => left.id.localeCompare(right.id)).map((artifact) => {
    const roles = [...byRole.values()].filter((item) => item.artifact_id === artifact.id)
      .map((item) => item.role).sort()
    const archive = artifact.image?.archive
    assert(['managed-pi-runtime', 'network-boundary'].includes(artifact.id) &&
      archive?.format === 'docker-archive' && safeRelative(archive.path) &&
      Number.isSafeInteger(archive.size) && digestRE.test(archive.sha256) &&
      imageIDRE.test(artifact.image.local_docker_config_image_id), 'release artifact binding is invalid')
    return { artifactId: artifact.id, roles,
      policyDigests: Object.fromEntries(roles.map((role) => [role, byRole.get(role).policy.sha256])),
      archiveFile: join(frozen.bindings.release.root, ...archive.path.split('/')),
      archiveRelativePath: archive.path, archiveMode: 0o400, archiveSize: archive.size,
      archiveSha256: archive.sha256, dockerConfigImageId: artifact.image.local_docker_config_image_id }
  })
}

function frozenAsStageRequest(frozen) {
  const b = frozen.bindings
  return { release: b.release, source: b.source, product: b.product, pi: b.pi,
    oauth: { file: b.oauth.file, sha256: b.oauth.opaqueSha256 }, ca: b.ca, node: b.node,
    playwright: b.playwright, runner: b.runner, docker: b.docker, colima: b.colima, lima: b.lima,
    diskImage: b.diskImage, sandboxExecutable: b.sandboxExecutable, controller: b.controller,
    ocr: b.ocr, configs: b.configs }
}

async function reopenFrozenInputs(frozen, frozenManifestFile) {
  const observed = await validateStageInputs(frozenAsStageRequest(frozen))
  exactKeys(frozen.bindings.closureDigests,
    ['release', 'source', 'web', 'pi', 'playwright', 'browser', 'limaPrefix', 'limaTemplates'],
    'frozen closure digests')
  for (const [key, closure] of Object.entries(observed.directories)) assert(
    closure.digest === frozen.bindings.closureDigests[key], `frozen ${key} closure drifted`)
  const root = dirname(frozenManifestFile)
  const expectedGenerated = {
    staticPreparationRequest: join(root, 'static-preparation-request.json'),
    ocrEngineBinding: join(root, 'vision-engine-binding.json'),
    ocrSandboxProfile: join(root, 'ocr-deny-network.sb'),
    engineSandboxProfile: join(root, 'engine-local-only.sb'),
  }
  for (const [key, path] of Object.entries(expectedGenerated)) {
    assert(frozen.generated[key].file === path, `generated ${key} topology drifted`)
    observed.files.push(await secureFile(path, 0o400, frozen.generated[key].sha256, `generated ${key}`))
  }
  assert((await readFile(expectedGenerated.ocrSandboxProfile, 'utf8')) === ocrProfile,
    'generated OCR sandbox policy drifted')
  assert((await readFile(expectedGenerated.engineSandboxProfile, 'utf8')) === engineProfile,
    'generated Engine sandbox policy drifted')
  return observed
}

export async function digestO4PreparedClosure(preparedOutputRoot) {
  return (await inspectDirectory(preparedOutputRoot, 'prepared static closure',
    { allowSymlinks: false })).digest
}

export async function finalizeO4MaterializationRequest({ requestFile, requestSha256, outputFile }, dependencies = {}) {
  const input = await readJSON(requestFile, 0o400, requestSha256, 'finalization request')
  validateFinalizeRequest(input.value); const request = input.value
  const frozenInput = await readJSON(request.frozenManifestFile, 0o400,
    request.frozenManifestSha256, 'frozen manifest')
  validateFrozenManifest(frozenInput.value, frozenInput.sha256); const frozen = frozenInput.value
  assert(request.preparedOutputRoot === frozen.preparedOutputRoot, 'prepared output root drifted')
  const preparedManifestFile = join(request.preparedOutputRoot, 'static-inputs.json')
  const prepared = await readJSON(preparedManifestFile, 0o400, request.preparedManifestSha256,
    'prepared static inputs')
  assert(prepared.value.schemaVersion === 'chora.m1-o4-static-inputs.v1' &&
    prepared.value.status === 'static_unresolved', 'prepared static result drifted')
  sameJSON(prepared.value.identity, frozen.identity, 'prepared identity drifted')
  assert(prepared.value.opaqueOAuthSha256 === frozen.bindings.oauth.opaqueSha256 &&
    prepared.value.source.manifestSha256 === frozen.bindings.source.manifestSha256 &&
    prepared.value.product.binarySha256 === frozen.bindings.product.binarySha256 &&
    prepared.value.product.webClosureSha256 === frozen.bindings.product.webClosureSha256 &&
    prepared.value.playwright.packageClosureSha256 === frozen.bindings.playwright.packageClosureSha256 &&
    prepared.value.playwright.browserClosureSha256 === frozen.bindings.playwright.browserClosureSha256,
  'prepared static bindings drifted')
  const frozenObserved = await reopenFrozenInputs(frozen, request.frozenManifestFile)
  const preparedClosure = await inspectDirectory(request.preparedOutputRoot, 'prepared static closure',
    { allowSymlinks: false })
  assert(preparedClosure.digest === request.preparedClosureSha256, 'prepared static closure drifted')
  const templateNames = ['candidate-manifest.template.json', 'candidate-config.template.json',
    'total-manifest.template.json']
  const [candidateManifestInput, candidateConfigInput, totalManifestInput] = await Promise.all(
    templateNames.map(async (name) => {
      const path = join(request.preparedOutputRoot, name)
      const bytes = await readFile(path); return readJSON(path, 0o400, sha256(bytes), name)
    }))
  const candidateManifest = candidateManifestInput.value
  assert(Object.hasOwn(candidateManifest, 'repositoryCommit') &&
    Object.hasOwn(candidateManifest, 'repositoryClosureSha256') &&
    candidateManifest.repositoryCommit === null &&
    candidateManifest.repositoryClosureSha256 === null,
  'candidate manifest repository authority null phase drifted')
  const candidateConfig = resolveTemplate(candidateConfigInput.value,
    request.resolutions.candidateConfig, 'candidateConfig')
  const totalManifest = resolveTemplate(totalManifestInput.value,
    request.resolutions.totalManifest, 'totalManifest')
  bindMaterializedOutputPaths(request.preparedOutputRoot, request.outputRoot,
    candidateConfig, totalManifest)
  assertResolved(candidateManifest, 'candidateManifest')
  assertResolved(candidateConfig, 'candidateConfig')
  assertResolved(totalManifest, 'totalManifest')
  assertO4DarwinUnixSocketPaths(totalManifest)
  rejectForbidden({ candidateManifest, candidateConfig, totalManifest }, 'resolved templates')
  assertMaterializedOutputPaths(request.outputRoot, candidateConfig, totalManifest)
  validateResolvedBindings(frozen, candidateConfig, totalManifest)
  const repositoryCapture = Object.freeze({
    executableFile: frozen.bindings.controller.file,
    executableSha256: frozen.bindings.controller.sha256,
  })
  const repositoryAuthority = await inspectRepositoryAuthority({
    repositoryRoot: totalManifest.repository.root,
    expectedCommit: totalManifest.repository.commit,
    expectedClosureSha256: totalManifest.repository.closureSha256,
    capture: repositoryCapture,
  })
  const releaseInput = await secureFile(frozen.bindings.release.manifestFile, 0o444,
    frozen.bindings.release.manifestSha256, 'release manifest at finalize')
  const releaseBytes = await readFile(frozen.bindings.release.manifestFile)
  rejectDuplicateJSONKeys(releaseBytes, 'release manifest at finalize')
  let release
  try { release = JSON.parse(releaseBytes.toString('utf8')) } catch { fail('release manifest is invalid JSON') }
  const materialization = {
    schemaVersion: MATERIALIZATION_REQUEST_SCHEMA, status: 'authorized', identity: frozen.identity,
    outputRoot: request.outputRoot,
    releaseId: release.release_id, releaseSpecSha256: release.spec_sha256,
    releaseManifestFile: frozen.bindings.release.manifestFile, releaseManifestMode: 0o444,
    releaseManifestSha256: frozen.bindings.release.manifestSha256,
    artifacts: materializationArtifacts(release, frozen), candidateManifestTemplate: candidateManifest,
    candidateConfigTemplate: candidateConfig, totalManifestTemplate: totalManifest,
  }
  rejectForbidden(materialization, 'materialization request')
  if (dependencies.beforeCommit) await dependencies.beforeCommit()
  await revalidateStageInputs(frozenObserved)
  await revalidateStageInputs({ files: [], directories: { prepared: preparedClosure } })
  for (const item of [input, frozenInput, prepared, candidateManifestInput, candidateConfigInput,
    totalManifestInput, releaseInput]) assert(sameIdentity(item.snapshot, await lstat(item.path)),
    `finalization TOCTOU drifted: ${item.path}`)
  const result = await publishOwnerOnlyJSON0400({ outputFile, value: materialization,
    capability: repositoryCapture }, {
    beforePublish: async (paths) => {
      await verifyRepositoryAuthority({ ...repositoryAuthority, capture: repositoryCapture })
      if (dependencies.beforePublish) await dependencies.beforePublish(paths)
    },
    publishOwnedPath: dependencies.publishOwnedPath,
    cleanupOwnedPath: dependencies.cleanupOwnedPath,
  })
  return Object.freeze({ ...result, schemaVersion: MATERIALIZATION_REQUEST_SCHEMA, networkUsed: false,
    executablesSpawned: true })
}

function parseCLI(argv) {
  assert(argv.length >= 1, 'usage: stage-static|finalize ...')
  const command = argv[0]; const values = {}
  for (let index = 1; index < argv.length; index += 2) {
    assert(argv[index]?.startsWith('--') && argv[index + 1] && !Object.hasOwn(values, argv[index]),
      'CLI arguments are invalid')
    values[argv[index].slice(2)] = argv[index + 1]
  }
  if (command === 'stage-static') {
    exactKeys(values, ['request', 'request-sha256', 'output-root'], 'stage CLI')
    return { command, input: { requestFile: values.request, requestSha256: values['request-sha256'],
      outputRoot: values['output-root'] } }
  }
  assert(command === 'finalize', 'CLI command is invalid')
  exactKeys(values, ['request', 'request-sha256', 'output-file'], 'finalize CLI')
  return { command, input: { requestFile: values.request, requestSha256: values['request-sha256'],
    outputFile: values['output-file'] } }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const cli = parseCLI(process.argv.slice(2))
  const operation = cli.command === 'stage-static' ? stageO4RealInputs(cli.input) :
    finalizeO4MaterializationRequest(cli.input)
  operation.then((result) => process.stdout.write(`${result.manifestFile ?? result.outputFile}\n`))
    .catch((error) => { process.stderr.write(`O4 real input freezer failed: ${error.message}\n`); process.exitCode = 1 })
}
