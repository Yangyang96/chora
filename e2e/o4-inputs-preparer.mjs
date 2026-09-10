#!/usr/bin/env node
import { createHash, randomBytes } from 'node:crypto'
import { constants as fsConstants } from 'node:fs'
import { access, chmod, lstat as lstatNative, mkdir, open, readFile, readdir, readlink, realpath, stat, writeFile } from 'node:fs/promises'
import { basename, dirname, isAbsolute, join, normalize, relative, resolve, sep } from 'node:path'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { assertDarwinUnixSocketPath } from './o4-darwin-unix-sockets.mjs'
import { cleanupOwnedPath, ownedPathIdentity, publishOwnedPath } from './o4-owned-path-cleanup.mjs'

export const REQUEST_SCHEMA = 'chora.m1-o4-static-input-preparation-request.v1'
export const RESULT_SCHEMA = 'chora.m1-o4-static-inputs.v1'
const RELEASE_SPEC_SCHEMA = 'chora.m1-o4-release-spec.v1'
const RELEASE_MANIFEST_SCHEMA = 'chora.m1-o4-release-manifest.v1'
const CANDIDATE_SCHEMA = 'chora.m1-o4-candidate-manifest.v1'
const PI_PROVENANCE_SCHEMA = 'chora.m1-o4-pi-provenance.v1'
const PI_MANIFEST_SCHEMA = 'chora.pi-private-asset.v1'
const PI_CLOSURE_SCHEMA = 'chora.pi-package-closure.v1'
const PI_SELECTION_SCHEMA = 'chora.pi-distribution-selection.v1'
const DIRECTORY_CLOSURE_SCHEMA = 'chora.m1-o4-directory-closure.v1'
const OCR_ENGINE_BINDING_SCHEMA = 'chora.m1-o4-system-managed-ocr-engine-binding.v1'
const EXPECTED_PI_NAME = '@earendil-works/pi-coding-agent'
const EXPECTED_PI_VERSION = '0.84.2'
const EXPECTED_PI_EXECUTABLE = 'dist/cli.js'
const ROLES = ['managed_pi_runtime', 'network_boundary', 'independent_verifier', 'capability_probe']
const digestRE = /^[0-9a-f]{64}$/
const imageIDRE = /^sha256:[0-9a-f]{64}$/
const idRE = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/
const productIDRE = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/
const MAX_JSON = 16 * 1024 * 1024
const MAX_FILES = 100_000
const MAX_BYTES = 2 * 1024 * 1024 * 1024
const MAX_PI_ENTRIES = 30_000
const MAX_PI_FILES = 15_000
const MAX_PI_VERSION_OUTPUT = 4096
const PI_VERSION_TIMEOUT_MS = 15_000
const PI_VERSION_SANDBOX_PROFILE = `(version 1)
(allow default)
(deny network*)
(deny file-write*)
(deny process-fork)
`
const ENGINE_BOOTSTRAP_SANDBOX_PROFILE = `(version 1)
(allow default)
(deny network*)
(allow network* (local unix-socket))
(allow network* (remote unix-socket))
(allow network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
`
const PHASE_ENVIRONMENT = Object.freeze({
  C: ['CHORA_O4_PROFILE_REUSE_BASE_URL', 'CHORA_O4_PROFILE_REUSE_TUPLE_FILE', 'CHORA_O4_PROFILE_REUSE_SOURCE_A3_LEDGER_FILE', 'CHORA_O4_PROFILE_REUSE_HANDOFF_RECEIPT_DIR', 'CHORA_O4_PROFILE_REUSE_OBSERVER_FILE', 'CHORA_O4_PROFILE_REUSE_OBSERVER_SHA256', 'CHORA_O4_PROFILE_REUSE_SUPERVISOR_RESOURCE_DIR', 'CHORA_O4_PROFILE_REUSE_RUNNER_PROTOCOL_DIR', 'CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_FILE', 'CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_SHA256', 'CHORA_O4_PROFILE_REUSE_OUTPUT_DIR'],
  D: ['CHORA_O4_RECOVERY_RESIDUE_BASE_URL', 'CHORA_O4_RECOVERY_RESIDUE_TUPLE_FILE', 'CHORA_O4_RECOVERY_RESIDUE_PHASE_C_RECEIPT_FILE', 'CHORA_O4_RECOVERY_RESIDUE_HANDOFF_RECEIPT_DIR', 'CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE', 'CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_SHA256', 'CHORA_O4_RECOVERY_RESIDUE_B2_RUNNER_MODULE_FILE', 'CHORA_O4_RECOVERY_RESIDUE_B2_RUNNER_MODULE_SHA256', 'CHORA_O4_RECOVERY_RESIDUE_AUTHORITY_FILE', 'CHORA_O4_RECOVERY_RESIDUE_AUTHORIZED_RESTART_RECEIPT_FILE', 'CHORA_O4_RECOVERY_RESIDUE_ORPHAN_RESTART_RECEIPT_FILE', 'CHORA_O4_RECOVERY_RESIDUE_SOURCE_A3_LEDGER_FILE', 'CHORA_O4_RECOVERY_RESIDUE_VERIFIER_LEDGER_FILE', 'CHORA_O4_RECOVERY_RESIDUE_AUTHORITY_CONSUMPTION_DIR', 'CHORA_O4_RECOVERY_RESIDUE_SUPERVISOR_RESOURCE_DIR', 'CHORA_O4_RECOVERY_RESIDUE_GENERATION_REFERENCE_DIR', 'CHORA_O4_RECOVERY_RESIDUE_PUBLIC_RESIDUE_FILE', 'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROL_FILE', 'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_FILE', 'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_SHA256', 'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_REQUEST_FILE', 'CHORA_O4_RECOVERY_RESIDUE_OCR_EXECUTABLE_FILE', 'CHORA_O4_RECOVERY_RESIDUE_OCR_EXECUTABLE_SHA256', 'CHORA_O4_RECOVERY_RESIDUE_OCR_ENGINE_BINDING_FILE', 'CHORA_O4_RECOVERY_RESIDUE_OCR_ENGINE_BINDING_SHA256', 'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_EXECUTABLE_FILE', 'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_EXECUTABLE_SHA256', 'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_PROFILE_FILE', 'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_PROFILE_SHA256', 'CHORA_O4_RECOVERY_RESIDUE_OCR_LANGUAGE', 'CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_DIR', 'CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_METADATA_DIR', 'CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_OCR_DIR', 'CHORA_O4_RECOVERY_RESIDUE_OUTPUT_DIR', 'CHORA_O4_RECOVERY_RESIDUE_BINDING_DIGEST'],
  E: ['CHORA_O4_FINAL_TUPLE_FILE', 'CHORA_O4_FINAL_RECEIPT_DIR', 'CHORA_O4_FINAL_SOURCE_A3_LEDGER_FILE', 'CHORA_O4_FINAL_PHASE_C_MANIFEST_FILE', 'CHORA_O4_FINAL_PHASE_C_RECORD_FILE', 'CHORA_O4_FINAL_PHASE_C_RECEIPT_DIR', 'CHORA_O4_FINAL_PHASE_D_MANIFEST_FILE', 'CHORA_O4_FINAL_PHASE_D_RECORD_FILE', 'CHORA_O4_FINAL_PHASE_D_RECEIPT_DIR', 'CHORA_O4_FINAL_PHASE_D_DISCLOSURE_SOURCE_DIR', 'CHORA_O4_FINAL_PHASE_D_SCREENSHOT_DIR', 'CHORA_O4_FINAL_PHASE_D_SCREENSHOT_METADATA_DIR', 'CHORA_O4_FINAL_PHASE_D_SCREENSHOT_OCR_DIR', 'CHORA_O4_FINAL_PRIVATE_MARKER_FILE', 'CHORA_O4_FINAL_INSTALLED_DOCTOR_FILE', 'CHORA_O4_FINAL_MODEL_OBSERVATION_FILE', 'CHORA_O4_FINAL_CREDENTIAL_CORPUS_FILE', 'CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE', 'CHORA_O4_FINAL_SOURCE_MANIFEST_FILE', 'CHORA_O4_FINAL_SOURCE_BUNDLE_ROOT', 'CHORA_O4_FINAL_BINARY_FILE', 'CHORA_O4_FINAL_WEB_DIRECTORY', 'CHORA_O4_FINAL_RELEASE_SPEC_FILE', 'CHORA_O4_FINAL_RELEASE_MANIFEST_FILE', 'CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE', 'CHORA_O4_FINAL_PATH_PI_PROVENANCE_FILE', 'CHORA_O4_FINAL_PRIVATE_PI_PROVENANCE_FILE', 'CHORA_O4_FINAL_QUALIFICATION_FILE', 'CHORA_O4_FINAL_ENGINE_ENDPOINT_EVIDENCE_FILE', 'CHORA_O4_FINAL_B2_RUNNER_MODULE_FILE', 'CHORA_O4_FINAL_SERVICE_CONTROLLER_FILE', 'CHORA_O4_FINAL_SERVICE_CONTROLLER_SHA256', 'CHORA_O4_FINAL_OUTPUT_DIR', 'CHORA_O4_FINAL_STANDARD_1_RESOURCE_LEDGER_FILE', 'CHORA_O4_FINAL_STANDARD_2_RESOURCE_LEDGER_FILE', 'CHORA_O4_FINAL_STANDARD_3_RESOURCE_LEDGER_FILE', 'CHORA_O4_FINAL_B2_RUNNER_MODULE_SHA256', 'CHORA_O4_FINAL_CLOSE_TIMEOUT_MS'],
})

function fail(message) { throw new Error(message) }
function assert(condition, message) { if (!condition) fail(message) }
function sha256(bytes) { return createHash('sha256').update(bytes).digest('hex') }
function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`
  if (value && typeof value === 'object') return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`
  return JSON.stringify(value)
}
function jsonBytes(value) { return Buffer.from(`${JSON.stringify(value, null, 2)}\n`) }
function exactKeys(value, keys, label) {
  assert(value && typeof value === 'object' && !Array.isArray(value), `${label} must be an object`)
  assert(JSON.stringify(Object.keys(value).sort()) === JSON.stringify([...keys].sort()), `${label} keys drifted`)
}
function digest(value, label) { assert(typeof value === 'string' && digestRE.test(value), `${label} is invalid`) }
function absolute(value, label) {
  assert(typeof value === 'string' && value.length > 1 && isAbsolute(value) && normalize(value) === value && resolve(value) === value && !value.includes('\0'), `${label} must be exact absolute`)
  return value
}
function safeRelative(value) {
  return typeof value === 'string' && value.length > 0 && !isAbsolute(value) && normalize(value) === value &&
    value.split(sep).join('/') === value && !value.split('/').includes('..') && !value.includes('\0')
}
function within(root, child) {
  const rel = relative(root, child)
  return rel !== '' && rel !== '..' && !rel.startsWith(`..${sep}`) && !isAbsolute(rel)
}
function overlaps(left, right) { return left === right || within(left, right) || within(right, left) }
function sameFile(a, b) { return a.dev === b.dev && a.ino === b.ino && a.size === b.size && a.mtimeMs === b.mtimeMs && a.ctimeMs === b.ctimeMs && a.mode === b.mode && a.uid === b.uid }
function sameDirectory(a, b) { return a.dev === b.dev && a.ino === b.ino && a.mtimeMs === b.mtimeMs && a.ctimeMs === b.ctimeMs && a.mode === b.mode && a.uid === b.uid }
function mode(info) {
  return typeof info.mode === 'bigint' ? Number(info.mode & 0o777n) : info.mode & 0o777
}
function owner(info) { return typeof info.uid === 'bigint' ? Number(info.uid) : info.uid }
function sameOwnedDirectory(identity, current) {
  return identity.isDirectory() && !identity.isSymbolicLink() &&
    current.isDirectory() && !current.isSymbolicLink() &&
    identity.dev === current.dev && identity.ino === current.ino &&
    identity.uid === current.uid && identity.mode === current.mode &&
    owner(current) === process.getuid() && mode(current) === 0o700
}

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
        whitespace(); const key = string(); assert(!keys.has(key), `${label} contains duplicate JSON key ${key}`); keys.add(key)
        whitespace(); assert(text[cursor++] === ':', `${label} JSON object is invalid`); value(); whitespace()
        if (text[cursor] === '}') { cursor++; return }
        assert(text[cursor++] === ',', `${label} JSON object is invalid`)
      }
    }
    if (token === '[') {
      cursor++; whitespace(); if (text[cursor] === ']') { cursor++; return }
      while (true) { value(); whitespace(); if (text[cursor] === ']') { cursor++; return }; assert(text[cursor++] === ',', `${label} JSON array is invalid`) }
    }
    const start = cursor; while (cursor < text.length && !/[\s,\]}]/.test(text[cursor])) cursor++
    assert(cursor > start, `${label} JSON value is invalid`)
    try { JSON.parse(text.slice(start, cursor)) } catch { fail(`${label} JSON value is invalid`) }
  }
  value(); whitespace(); assert(cursor === text.length, `${label} contains trailing JSON data`)
}

async function noSymlinkAncestors(path) {
  let cursor = path
  while (true) {
    try { assert(!(await lstat(cursor)).isSymbolicLink(), `${path} has a symlink ancestor`) }
    catch (error) { if (error.code !== 'ENOENT') throw error }
    const parent = dirname(cursor)
    if (parent === cursor) break
    cursor = parent
  }
}

async function expectAbsent(path, label) {
  try { await lstat(path); fail(`${label} already exists`) }
  catch (error) { if (error.message === `${label} already exists` || error.code !== 'ENOENT') throw error }
}

async function secureFile(path, expectedMode, expectedSha, label, max = MAX_JSON) {
  absolute(path, label); digest(expectedSha, `${label} SHA-256`); await noSymlinkAncestors(path)
  const before = await lstat(path)
  assert(before.isFile() && !before.isSymbolicLink() && before.uid === process.getuid() && before.nlink === 1 && mode(before) === expectedMode && before.size >= 0 && before.size <= max, `${label} is not one owner-controlled ${expectedMode.toString(8)} regular file`)
  const bytes = await readFile(path)
  const after = await lstat(path)
  assert(sameFile(before, after) && bytes.length === before.size, `${label} changed while read`)
  assert(sha256(bytes) === expectedSha, `${label} SHA-256 drifted`)
  return { bytes, snapshot: before }
}

async function secureSystemFile(path, expectedSha, label) {
  absolute(path, label); digest(expectedSha, `${label} SHA-256`); await noSymlinkAncestors(path)
  const before = await lstat(path)
  assert(before.isFile() && !before.isSymbolicLink() && before.uid === 0 && before.nlink === 1 && mode(before) === 0o755 && before.size > 0 && before.size <= 2 * 1024 * 1024, `${label} is not one root-owned 0755 system file`)
  const bytes = await readFile(path); const after = await lstat(path)
  assert(sameFile(before, after) && bytes.length === before.size && sha256(bytes) === expectedSha, `${label} identity drifted`)
  return { bytes, snapshot: before }
}

async function secureLargeFile(path, expectedSize, expectedSha, label) {
  absolute(path, label); digest(expectedSha, `${label} SHA-256`); await noSymlinkAncestors(path)
  assert(Number.isSafeInteger(expectedSize) && expectedSize > 0 && expectedSize <= 64 * 1024 * 1024 * 1024, `${label} size is invalid`)
  const before = await lstat(path)
  assert(before.isFile() && !before.isSymbolicLink() && before.uid === process.getuid() && before.nlink === 1 && mode(before) === 0o400 && before.size === expectedSize, `${label} is not one pinned owner-0400 file`)
  const handle = await open(path, 'r'); const hash = createHash('sha256'); const buffer = Buffer.allocUnsafe(4 * 1024 * 1024); let position = 0
  try {
    while (position < expectedSize) {
      const { bytesRead } = await handle.read(buffer, 0, Math.min(buffer.length, expectedSize - position), position)
      assert(bytesRead > 0, `${label} ended before its pinned size`); hash.update(buffer.subarray(0, bytesRead)); position += bytesRead
    }
  } finally { await handle.close() }
  const after = await lstat(path)
  assert(sameFile(before, after) && position === expectedSize && hash.digest('hex') === expectedSha, `${label} identity or SHA-256 drifted`)
  return { snapshot: before }
}

async function parseJSONFile(path, expectedMode, expectedSha, label, max = MAX_JSON) {
  const result = await secureFile(path, expectedMode, expectedSha, label, max)
  rejectDuplicateJSONKeys(result.bytes, label)
  let value
  try { value = JSON.parse(result.bytes.toString('utf8')) } catch { fail(`${label} is invalid JSON`) }
  return { ...result, value }
}

function validateRequest(request) {
  exactKeys(request, ['schemaVersion', 'status', 'identity', 'release', 'source', 'product', 'pi', 'oauth', 'ca', 'node', 'playwright', 'configs', 'runner', 'serviceController', 'ocr', 'engine', 'docker', 'colima'], 'request')
  assert(request.schemaVersion === REQUEST_SCHEMA && request.status === 'authorized', 'request schema/status drifted')
  exactKeys(request.identity, ['environmentId', 'installId', 'generationId', 'platform'], 'identity')
  for (const name of ['environmentId', 'installId', 'generationId']) assert(idRE.test(request.identity[name]), `${name} is invalid`)
  exactKeys(request.identity.platform, ['os', 'architecture'], 'host platform')
  assert(request.identity.platform.os === 'darwin' && request.identity.platform.architecture === 'arm64', 'host platform must be darwin/arm64')
  exactKeys(request.release, ['root', 'manifestFile', 'manifestSha256'], 'release input')
  exactKeys(request.source, ['bundleRoot', 'manifestFile', 'manifestSha256'], 'source input')
  exactKeys(request.product, ['binaryFile', 'binarySha256', 'webDirectory', 'webClosureSha256'], 'product input')
  exactKeys(request.pi, ['packageRoot', 'executableFile', 'packageClosureSha256', 'executableSha256'], 'Pi input')
  exactKeys(request.oauth, ['file', 'sha256'], 'OAuth input')
  exactKeys(request.ca, ['file', 'sha256'], 'CA input')
  exactKeys(request.node, ['file', 'sha256'], 'Node input')
  exactKeys(request.playwright, ['packageRoot', 'packageClosureSha256', 'cliFile', 'cliSha256', 'browserRoot', 'browserClosureSha256'], 'Playwright input')
  exactKeys(request.configs, ['C', 'D', 'E'], 'phase configs')
  for (const phase of ['C', 'D', 'E']) exactKeys(request.configs[phase], ['file', 'sha256'], `phase ${phase} input`)
  exactKeys(request.runner, ['moduleFile', 'moduleSha256'], 'Runner input')
  exactKeys(request.serviceController, ['executableFile', 'sha256'], 'service controller input')
  exactKeys(request.ocr, ['executableFile', 'executableSha256', 'engineBindingFile', 'engineBindingSha256', 'sandboxExecutableFile', 'sandboxExecutableSha256', 'sandboxProfileFile', 'sandboxProfileSha256', 'language'], 'system-managed OCR input')
  exactKeys(request.engine, ['limaPrefixRoot', 'limaPrefixClosureSha256', 'limaInfoDigest',
    'limaFile', 'limaSha256',
    'limaWrapperFile', 'limaWrapperSha256', 'limaTemplatesRoot', 'limaTemplatesClosureSha256',
    'limaGuestAgentFile', 'limaGuestAgentSize', 'limaGuestAgentSha256', 'diskImageFile',
    'diskImageSize', 'diskImageSha256', 'sandboxExecutableFile', 'sandboxExecutableSha256',
    'sandboxProfileFile', 'sandboxProfileSha256', 'profileName', 'contextName',
    'endpointSocketPath', 'endpointDigest', 'cpus', 'memoryGiB', 'diskGiB', 'engineMs'],
  'fresh Engine input')
  exactKeys(request.docker, ['file', 'sha256'], 'Docker input'); exactKeys(request.colima, ['file', 'sha256'], 'Colima input')
  for (const [label, value] of [
    ['release root', request.release.root], ['release manifest', request.release.manifestFile], ['source bundle root', request.source.bundleRoot], ['source manifest', request.source.manifestFile],
    ['product binary', request.product.binaryFile], ['web directory', request.product.webDirectory], ['Pi package root', request.pi.packageRoot], ['Pi executable', request.pi.executableFile],
    ['OAuth file', request.oauth.file], ['CA file', request.ca.file], ['Node file', request.node.file], ['Playwright package root', request.playwright.packageRoot], ['Playwright CLI', request.playwright.cliFile],
    ['browser root', request.playwright.browserRoot], ...['C', 'D', 'E'].map((p) => [`phase ${p} config`, request.configs[p].file]),
    ['Runner module', request.runner.moduleFile], ['service controller', request.serviceController.executableFile], ['OCR executable', request.ocr.executableFile], ['OCR engine binding', request.ocr.engineBindingFile], ['OCR sandbox executable', request.ocr.sandboxExecutableFile], ['OCR sandbox profile', request.ocr.sandboxProfileFile],
    ['Lima prefix', request.engine.limaPrefixRoot], ['Lima file', request.engine.limaFile],
    ['Lima wrapper', request.engine.limaWrapperFile], ['Lima templates', request.engine.limaTemplatesRoot],
    ['Lima guest agent', request.engine.limaGuestAgentFile],
    ['Engine disk image', request.engine.diskImageFile], ['Engine sandbox executable', request.engine.sandboxExecutableFile], ['Engine sandbox profile', request.engine.sandboxProfileFile], ['Engine endpoint socket', request.engine.endpointSocketPath],
    ['Docker file', request.docker.file], ['Colima file', request.colima.file],
  ]) absolute(value, label)
  for (const [label, value] of [
    ['release manifest', request.release.manifestSha256], ['source manifest', request.source.manifestSha256], ['binary', request.product.binarySha256], ['web closure', request.product.webClosureSha256],
    ['Pi closure', request.pi.packageClosureSha256], ['Pi executable', request.pi.executableSha256], ['OAuth', request.oauth.sha256], ['CA', request.ca.sha256], ['Node', request.node.sha256],
    ['Playwright closure', request.playwright.packageClosureSha256], ['Playwright CLI', request.playwright.cliSha256], ['browser closure', request.playwright.browserClosureSha256],
    ...['C', 'D', 'E'].map((p) => [`phase ${p}`, request.configs[p].sha256]), ['Runner module', request.runner.moduleSha256], ['service controller', request.serviceController.sha256],
    ['OCR executable', request.ocr.executableSha256], ['OCR engine binding', request.ocr.engineBindingSha256], ['OCR sandbox executable', request.ocr.sandboxExecutableSha256], ['OCR sandbox profile', request.ocr.sandboxProfileSha256],
    ['Lima prefix', request.engine.limaPrefixClosureSha256],
    ['Lima info', request.engine.limaInfoDigest], ['Lima', request.engine.limaSha256],
    ['Lima wrapper', request.engine.limaWrapperSha256],
    ['Lima templates', request.engine.limaTemplatesClosureSha256],
    ['Lima guest agent', request.engine.limaGuestAgentSha256],
    ['Engine disk image', request.engine.diskImageSha256], ['Engine sandbox executable', request.engine.sandboxExecutableSha256], ['Engine sandbox profile', request.engine.sandboxProfileSha256], ['Engine endpoint', request.engine.endpointDigest],
    ['Docker', request.docker.sha256], ['Colima', request.colima.sha256],
  ]) digest(value, `${label} SHA-256`)
  assert(request.release.manifestFile === join(request.release.root, 'release-manifest.v1.json'), 'release manifest path is spliced')
  assert(request.source.manifestFile === join(request.source.bundleRoot, 'source-manifest.json'), 'source manifest path is spliced')
  assert(!overlaps(request.release.root, request.source.bundleRoot), 'release and source roots must be distinct')
  assert(request.pi.executableFile === join(request.pi.packageRoot, EXPECTED_PI_EXECUTABLE), 'Pi executable path is spliced')
  assert(within(request.playwright.packageRoot, request.playwright.cliFile), 'Playwright CLI is outside its package root')
  assert(request.engine.limaFile === join(request.engine.limaPrefixRoot, 'bin/limactl') &&
    request.engine.limaWrapperFile === join(request.engine.limaPrefixRoot, 'bin/lima') &&
    request.engine.limaTemplatesRoot === join(request.engine.limaPrefixRoot, 'share/lima/templates') &&
    request.engine.limaGuestAgentFile === join(request.engine.limaPrefixRoot,
      'share/lima/lima-guestagent.Linux-aarch64.gz') &&
    request.engine.limaGuestAgentSize === 7_251_420 && basename(request.docker.file) === 'docker' &&
    !overlaps(request.engine.limaPrefixRoot, dirname(request.docker.file)),
  'canonical Engine executable/prefix topology drifted')
  assert(request.ocr.language === 'eng', 'system-managed OCR language must be eng')
  assert(request.ocr.sandboxExecutableFile === '/usr/bin/sandbox-exec' && request.ocr.sandboxExecutableFile === request.engine.sandboxExecutableFile && request.ocr.sandboxExecutableSha256 === request.engine.sandboxExecutableSha256, 'OCR and Engine sandbox executable binding drifted')
  assert(productIDRE.test(request.engine.profileName) && request.engine.profileName !== 'default' &&
    request.engine.contextName === `colima-${request.engine.profileName}`,
  'fresh Engine Docker context must be exactly derived from its Colima profile')
  assert(request.engine.endpointDigest === sha256(`unix://${request.engine.endpointSocketPath}`), 'fresh Engine endpoint digest drifted')
  assertDarwinUnixSocketPath(request.engine.endpointSocketPath, 'fresh Engine endpoint socket')
  assert(Number.isSafeInteger(request.engine.diskImageSize) && request.engine.diskImageSize > 0 && request.engine.diskImageSize <= 64 * 1024 * 1024 * 1024, 'fresh Engine disk size is invalid')
  for (const key of ['cpus', 'memoryGiB', 'diskGiB']) assert(Number.isSafeInteger(request.engine[key]) && request.engine[key] >= 1 && request.engine[key] <= 256, `fresh Engine ${key} is invalid`)
  assert(request.engine.diskGiB >= request.engine.memoryGiB, 'fresh Engine disk must not be smaller than memory')
  assert(Number.isSafeInteger(request.engine.engineMs) && request.engine.engineMs >= 1_000 && request.engine.engineMs <= 30 * 60_000, 'fresh Engine timeout is invalid')
  const closureRoots = [request.release.root, request.source.bundleRoot, request.pi.packageRoot,
    request.playwright.packageRoot, request.playwright.browserRoot, request.product.webDirectory,
    request.engine.limaPrefixRoot]
  for (let left = 0; left < closureRoots.length; left++) for (let right = left + 1; right < closureRoots.length; right++) assert(!overlaps(closureRoots[left], closureRoots[right]), 'input closure roots overlap')
  rejectForbiddenContractKeys(request)
}

function rejectForbiddenContractKeys(value) {
  if (Array.isArray(value)) return value.forEach(rejectForbiddenContractKeys)
  if (!value || typeof value !== 'object') return
  for (const [key, item] of Object.entries(value)) {
    assert(!/^(?:OCR_MODEL|modelFile|modelSha256|preflightSha256)$/i.test(key) && !/registry/i.test(key), `forbidden legacy field ${key}`)
    rejectForbiddenContractKeys(item)
  }
}

function validateOCRBinding(value, request) {
  exactKeys(value, ['schemaVersion', 'status', 'engine', 'modelBytes', 'platform', 'vision', 'executable', 'sandbox', 'bindingDigest'], 'system-managed OCR engine binding')
  assert(value.schemaVersion === OCR_ENGINE_BINDING_SCHEMA && value.status === 'authorized' && value.engine === 'system_managed_ocr_engine' && value.modelBytes === 'opaque_unavailable', 'system-managed OCR boundary drifted')
  exactKeys(value.platform, ['os', 'architecture', 'macOSProductVersion', 'macOSBuildVersion', 'darwinSysname', 'darwinRelease', 'darwinVersion', 'darwinMachine'], 'system-managed OCR platform')
  assert(value.platform.os === 'darwin' && value.platform.architecture === 'arm64' && value.platform.darwinSysname === 'Darwin', 'system-managed OCR platform drifted')
  for (const key of ['macOSProductVersion', 'macOSBuildVersion', 'darwinRelease', 'darwinVersion', 'darwinMachine']) assert(typeof value.platform[key] === 'string' && value.platform[key].length > 0, `system-managed OCR platform ${key} is invalid`)
  exactKeys(value.vision, ['bundleIdentifier', 'bundleVersion', 'infoPlistSha256', 'supportedRecognitionRevisions', 'requestedRecognitionRevision', 'recognitionLevel', 'recognitionLanguage', 'usesLanguageCorrection', 'confidenceThreshold'], 'system-managed OCR Vision binding')
  assert(typeof value.vision.bundleIdentifier === 'string' && value.vision.bundleIdentifier.length > 0 && typeof value.vision.bundleVersion === 'string' && value.vision.bundleVersion.length > 0, 'system-managed OCR Vision identity is invalid')
  digest(value.vision.infoPlistSha256, 'system-managed OCR Vision Info.plist')
  assert(Array.isArray(value.vision.supportedRecognitionRevisions) && value.vision.supportedRecognitionRevisions.length > 0 && value.vision.supportedRecognitionRevisions.every((revision, index, revisions) => Number.isSafeInteger(revision) && revision > 0 && (index === 0 || revision > revisions[index - 1])) && value.vision.supportedRecognitionRevisions.includes(value.vision.requestedRecognitionRevision) && value.vision.recognitionLevel === 'accurate' && value.vision.recognitionLanguage === 'en-US' && value.vision.usesLanguageCorrection === true && typeof value.vision.confidenceThreshold === 'number' && value.vision.confidenceThreshold > 0 && value.vision.confidenceThreshold <= 1, 'system-managed OCR Vision settings drifted')
  exactKeys(value.executable, ['pathSha256', 'sha256'], 'system-managed OCR executable binding')
  assert(value.executable.pathSha256 === sha256(request.ocr.executableFile) && value.executable.sha256 === request.ocr.executableSha256, 'system-managed OCR executable binding drifted')
  exactKeys(value.sandbox, ['executableFile', 'executablePathSha256', 'executableSha256', 'profileFile', 'profilePathSha256', 'profileSha256', 'networkRule'], 'system-managed OCR sandbox binding')
  assert(value.sandbox.executableFile === request.ocr.sandboxExecutableFile && value.sandbox.executablePathSha256 === sha256(request.ocr.sandboxExecutableFile) && value.sandbox.executableSha256 === request.ocr.sandboxExecutableSha256 && value.sandbox.profileFile === request.ocr.sandboxProfileFile && value.sandbox.profilePathSha256 === sha256(request.ocr.sandboxProfileFile) && value.sandbox.profileSha256 === request.ocr.sandboxProfileSha256 && value.sandbox.networkRule === 'deny network*', 'system-managed OCR sandbox binding drifted')
  digest(value.bindingDigest, 'system-managed OCR binding digest')
  const { bindingDigest, ...withoutDigest } = value
  assert(bindingDigest === sha256(canonical(withoutDigest)), 'system-managed OCR binding digest drifted')
}

function validateOCRSandboxProfile(bytes) {
  const lines = bytes.toString('utf8').split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
  assert(lines[0] === '(version 1)' && lines.includes('(deny network*)') && !lines.some((line) => /^\(allow\s+network/.test(line)), 'system-managed OCR sandbox profile does not deny network')
}

async function inspectDirectory(root, label, { allowSymlinks = false, omit = () => false, maxEntries = MAX_FILES, maxFiles = MAX_FILES, maxBytes = MAX_BYTES } = {}) {
  absolute(root, label); await noSymlinkAncestors(root)
  const rootBefore = await lstat(root)
  assert(rootBefore.isDirectory() && !rootBefore.isSymbolicLink() && rootBefore.uid === process.getuid() && (mode(rootBefore) & 0o022) === 0, `${label} root is unsafe`)
  const entries = []; const snapshots = new Map(); let totalBytes = 0; let fileCount = 0
  const visit = async (directory, prefix) => {
    const names = (await readdir(directory)).sort()
    for (const name of names) {
      assert(name && name !== '.' && name !== '..' && !name.includes('/') && !name.includes('\0'), `${label} contains an unsafe name`)
      const relativePath = prefix ? `${prefix}/${name}` : name
      if (omit(relativePath)) continue
      const path = join(directory, name)
      const before = await lstat(path)
      assert(before.uid === process.getuid(), `${label} contains a foreign entry`)
      assert(entries.length < maxEntries, `${label} exceeds bounded closure`)
      snapshots.set(path, before)
      if (before.isSymbolicLink()) {
        assert(allowSymlinks, `${label} contains unexpected symlink ${relativePath}`)
        const target = await readlink(path)
        assert(target.length > 0 && !isAbsolute(target) && !target.includes('\0'), `${label} symlink ${relativePath} is unsafe`)
        const resolvedTarget = resolve(dirname(path), target)
        assert(resolvedTarget === root || within(root, resolvedTarget), `${label} symlink ${relativePath} escapes root`)
        await access(resolvedTarget, fsConstants.F_OK)
        const canonicalTarget = await realpath(resolvedTarget)
        assert(canonicalTarget === root || within(root, canonicalTarget), `${label} symlink ${relativePath} resolves outside root`)
        const after = await lstat(path)
        assert(sameFile(before, after) && await readlink(path) === target, `${label} symlink changed while read`)
        entries.push({ path: relativePath, type: 'symlink', mode: mode(before), target })
      } else if (before.isDirectory()) {
        assert((mode(before) & 0o022) === 0, `${label} contains a writable directory`)
        entries.push({ path: relativePath, type: 'directory', mode: mode(before) })
        await visit(path, relativePath)
      } else {
        assert(before.isFile() && before.nlink === 1 && (mode(before) & 0o022) === 0, `${label} contains non-regular, writable, or multiply-linked file ${relativePath}`)
        totalBytes += before.size; fileCount++
        assert(fileCount <= maxFiles && totalBytes <= maxBytes, `${label} exceeds bounded closure`)
        const bytes = await readFile(path); const after = await lstat(path)
        assert(sameFile(before, after) && bytes.length === before.size, `${label} entry ${relativePath} changed while read`)
        entries.push({ path: relativePath, type: 'file', mode: mode(before), size: before.size, sha256: sha256(bytes) })
      }
    }
  }
  await visit(root, '')
  assert(entries.length > 0, `${label} is empty`)
  const rootAfter = await lstat(root); assert(sameDirectory(rootBefore, rootAfter), `${label} root changed while read`)
  const closureDigest = sha256(canonical({ rootMode: mode(rootBefore), entries }))
  return { rootMode: mode(rootBefore), entries, digest: closureDigest, snapshots, rootSnapshot: rootBefore, fileCount, totalBytes }
}

async function revalidateDirectory(root, closure, label) {
  const now = await lstat(root); assert(sameDirectory(closure.rootSnapshot, now), `${label} root TOCTOU drifted`)
  for (const [path, expected] of closure.snapshots) {
    const current = await lstat(path); assert(sameFile(expected, current), `${label} entry TOCTOU drifted`)
  }
}

async function revalidateExactDirectoryClosure(root, closure, label) {
  assert(closure && Array.isArray(closure.entries) && closure.snapshots instanceof Map,
    `${label} authority is invalid`)
  const observed = await inspectDirectory(root, label,
    { maxEntries: MAX_PI_ENTRIES, maxFiles: MAX_PI_FILES })
  await revalidateDirectory(root, observed, `${label} observation`)
  assert(sameDirectory(closure.rootSnapshot, observed.rootSnapshot),
    `${label} root identity drifted`)
  assert(observed.digest === closure.digest &&
    canonical(observed.entries) === canonical(closure.entries),
  `${label} topology, type, mode, size, or content drifted`)
  assert(observed.snapshots.size === closure.snapshots.size,
    `${label} snapshot topology drifted`)
  for (const [path, expected] of closure.snapshots) {
    const current = observed.snapshots.get(path)
    assert(current && sameFile(expected, current), `${label} entry identity drifted`)
  }
  return observed
}

async function inspectWeb(root) {
  absolute(root, 'web directory'); await noSymlinkAncestors(root)
  const rootBefore = await lstat(root)
  assert(rootBefore.isDirectory() && !rootBefore.isSymbolicLink() && rootBefore.uid === process.getuid() && (mode(rootBefore) & 0o022) === 0, 'web root is unsafe')
  const entries = []; const snapshots = new Map()
  const visit = async (directory, prefix = '') => {
    for (const name of (await readdir(directory)).sort()) {
      const path = join(directory, name); const relativePath = prefix ? `${prefix}/${name}` : name
      const before = await lstat(path)
      assert(!before.isSymbolicLink() && before.uid === process.getuid() && (mode(before) & 0o022) === 0, 'web tree contains an unsafe entry')
      if (before.isDirectory()) await visit(path, relativePath)
      else {
        assert(before.isFile() && before.nlink === 1 && before.size <= 32 * 1024 * 1024, 'web tree contains a non-regular or unbounded file')
        const bytes = await readFile(path); const after = await lstat(path)
        assert(sameFile(before, after), 'web file changed while read'); const hash = sha256(bytes)
        entries.push(`${relativePath}\0${bytes.length}\0${hash}\n`); snapshots.set(path, before)
      }
    }
  }
  await visit(root); assert(entries.length > 0 && entries.length <= 10_000, 'web tree is empty or unbounded')
  assert(sameDirectory(rootBefore, await lstat(root)), 'web root changed while read')
  return { digest: sha256(entries.join('')), rootSnapshot: rootBefore, snapshots }
}

async function validateSource(request) {
  const parsed = await parseJSONFile(request.source.manifestFile, 0o444, request.source.manifestSha256, 'source manifest')
  const value = parsed.value
  exactKeys(value, ['schema_version', 'aggregate_sha256', 'source_path_policy', 'install_only_projection', 'model_readable_projection', 'files'], 'source manifest')
  assert(value.schema_version === 'chora.local-alpha-source-bundle.v1' && Array.isArray(value.files) && value.files.length > 0 && value.files.length <= 10_000, 'source manifest schema/files drifted')
  digest(value.aggregate_sha256, 'source aggregate')
  exactKeys(value.source_path_policy, ['path', 'sha256'], 'source path policy')
  assert(value.source_path_policy.path === 'distribution/v1/policies/source-path-policy.v1.json', 'source path policy drifted'); digest(value.source_path_policy.sha256, 'source path policy')
  for (const projection of ['install_only_projection', 'model_readable_projection']) {
    exactKeys(value[projection], ['files', 'paths_sha256', 'aggregate_sha256'], projection)
    assert(Number.isSafeInteger(value[projection].files) && value[projection].files >= 0, `${projection} file count invalid`)
    digest(value[projection].paths_sha256, `${projection} paths`); digest(value[projection].aggregate_sha256, `${projection} aggregate`)
  }
  let previous = ''; const framing = []; const snapshots = new Map()
  for (const entry of value.files) {
    exactKeys(entry, ['path', 'size', 'mode', 'sha256'], 'source entry')
    assert(safeRelative(entry.path) && entry.path > previous, 'source paths are unsafe, unsorted, or duplicated'); previous = entry.path
    assert(Number.isSafeInteger(entry.size) && entry.size >= 0 && /^[0-7]{3,4}$/.test(entry.mode), 'source entry size/mode invalid'); digest(entry.sha256, 'source entry digest')
    const path = join(request.source.bundleRoot, 'source', ...entry.path.split('/'))
    const expectedMode = Number.parseInt(entry.mode, 8)
    const file = await secureFile(path, expectedMode, entry.sha256, `source ${entry.path}`, Math.max(entry.size, 1))
    assert(file.bytes.length === entry.size, `source ${entry.path} size drifted`); snapshots.set(path, file.snapshot)
    framing.push(`${entry.path}\0${entry.mode}\0${entry.size}\0${entry.sha256}\n`)
  }
  assert(sha256(framing.join('')) === value.aggregate_sha256, 'source aggregate drifted')
  const tree = await inspectDirectory(join(request.source.bundleRoot, 'source'), 'source tree')
  const treeFiles = tree.entries.filter((entry) => entry.type === 'file').map((entry) => entry.path).sort()
  assert(JSON.stringify(treeFiles) === JSON.stringify(value.files.map((entry) => entry.path)), 'source tree contains missing or extra files')
  snapshots.set(request.source.manifestFile, parsed.snapshot)
  return { value, parsed, snapshots, tree, aggregate: value.aggregate_sha256 }
}

function validateReleaseManifest(value, request) {
  exactKeys(value, ['schema_version', 'spec_sha256', 'release_id', 'platform', 'roles', 'artifacts'], 'release manifest')
  assert(value.schema_version === 'chora.release-assets-manifest.v1' && idRE.test(value.release_id), 'release manifest identity drifted')
  digest(value.spec_sha256, 'release spec'); exactKeys(value.platform, ['os', 'architecture'], 'release platform')
  assert(value.platform.os === 'linux' && value.platform.architecture === 'arm64', 'release platform drifted')
  assert(Array.isArray(value.artifacts) && value.artifacts.length === 2 && Array.isArray(value.roles) && value.roles.length === 4, 'release closure cardinality drifted')
  const artifacts = new Map()
  for (const artifact of value.artifacts) {
    assert(artifact && typeof artifact === 'object' && typeof artifact.id === 'string' && !artifacts.has(artifact.id), 'release artifact duplicated')
    exactKeys(artifact.image, ['local_docker_config_image_id', 'archive'], `artifact ${artifact.id} image`)
    assert(imageIDRE.test(artifact.image.local_docker_config_image_id), `artifact ${artifact.id} image ID invalid`)
    exactKeys(artifact.image.archive, ['format', 'path', 'size', 'sha256'], `artifact ${artifact.id} archive`)
    assert(artifact.image.archive.format === 'docker-archive' && safeRelative(artifact.image.archive.path) && Number.isSafeInteger(artifact.image.archive.size) && artifact.image.archive.size > 0, `artifact ${artifact.id} archive invalid`)
    digest(artifact.image.archive.sha256, `artifact ${artifact.id} archive`)
    artifacts.set(artifact.id, artifact)
  }
  assert([...artifacts.keys()].sort().join(',') === 'managed-pi-runtime,network-boundary', 'release artifacts drifted')
  const roles = []; const names = new Set()
  for (const role of value.roles) {
    assert(role && typeof role === 'object' && ROLES.includes(role.role) && !names.has(role.role), 'release role invalid or duplicated'); names.add(role.role)
    const expectedAlias = ['independent_verifier', 'capability_probe'].includes(role.role) ? 'managed_pi_runtime' : undefined
    assert(role.alias_of_role === expectedAlias, `${role.role} alias drifted`)
    assert(artifacts.has(role.artifact_id), `${role.role} artifact missing`)
    assert(role.policy && typeof role.policy === 'object' && digestRE.test(role.policy.sha256), `${role.role} policy invalid`)
    if (expectedAlias) assert(role.artifact_id === 'managed-pi-runtime', `${role.role} alias artifact drifted`)
    const image = artifacts.get(role.artifact_id).image
    roles.push({ role: role.role, artifactId: role.artifact_id, archiveSha256: image.archive.sha256, archiveSize: image.archive.size, dockerConfigImageId: image.local_docker_config_image_id, policyDigest: role.policy.sha256 })
  }
  assert(ROLES.every((role) => names.has(role)), 'release roles incomplete')
  return roles.sort((a, b) => ROLES.indexOf(a.role) - ROLES.indexOf(b.role))
}

async function validateRelease(request) {
  const parsed = await parseJSONFile(request.release.manifestFile, 0o444, request.release.manifestSha256, 'release manifest', 4 * 1024 * 1024)
  const roles = validateReleaseManifest(parsed.value, request)
  const snapshots = new Map([[request.release.manifestFile, parsed.snapshot]])
  const uniqueArtifacts = new Map()
  for (const role of roles) uniqueArtifacts.set(role.artifactId, role)
  for (const binding of uniqueArtifacts.values()) {
    const artifact = parsed.value.artifacts.find((item) => item.id === binding.artifactId)
    const archivePath = join(request.release.root, ...artifact.image.archive.path.split('/'))
    const archive = await secureFile(archivePath, 0o400, binding.archiveSha256, `${binding.artifactId} archive`, binding.archiveSize)
    assert(archive.bytes.length === binding.archiveSize, `${binding.artifactId} archive size drifted`); snapshots.set(archivePath, archive.snapshot)
  }
  return { parsed, roles, snapshots }
}

function writeFrame(hash, bytes) { const length = Buffer.alloc(8); length.writeBigUInt64BE(BigInt(bytes.length)); hash.update(length); hash.update(bytes) }
function piClosureDigest(files) {
  const hash = createHash('sha256'); writeFrame(hash, Buffer.from(PI_CLOSURE_SCHEMA))
  for (const file of files) {
    writeFrame(hash, Buffer.from(file.path)); writeFrame(hash, Buffer.from(file.mode.toString(8).padStart(4, '0')))
    writeFrame(hash, Buffer.from(String(file.size))); writeFrame(hash, Buffer.from(file.sha256, 'hex'))
  }
  return hash.digest('hex')
}
function piSelectionIdentity(kind, path, resolvedPath, packageRoot, executableSha256, closureSha256) {
  const hash = createHash('sha256')
  for (const bytes of [Buffer.from(PI_SELECTION_SCHEMA), Buffer.from(kind), Buffer.from(path), Buffer.from(resolvedPath), Buffer.from(packageRoot), Buffer.from(EXPECTED_PI_VERSION), Buffer.from(executableSha256, 'hex'), Buffer.from(closureSha256, 'hex')]) writeFrame(hash, bytes)
  return hash.digest('hex')
}

async function copyPrivatePi(request, stagingRoot, outputRoot, runVersion) {
  const closure = await inspectDirectory(request.pi.packageRoot, 'Pi package', { omit: (path) => path === 'node_modules/.bin' || path.startsWith('node_modules/.bin/'), maxEntries: MAX_PI_ENTRIES, maxFiles: MAX_PI_FILES })
  assert(closure.digest === request.pi.packageClosureSha256, 'Pi source closure drifted')
  const packageEntry = closure.entries.find((entry) => entry.path === 'package.json' && entry.type === 'file')
  const executableEntry = closure.entries.find((entry) => entry.path === EXPECTED_PI_EXECUTABLE && entry.type === 'file')
  assert(packageEntry && executableEntry && (executableEntry.mode & 0o100) !== 0 && executableEntry.sha256 === request.pi.executableSha256, 'Pi package/executable binding drifted')
  const packageBytes = await readFile(join(request.pi.packageRoot, 'package.json')); rejectDuplicateJSONKeys(packageBytes, 'Pi package.json')
  const packageJSON = JSON.parse(packageBytes.toString('utf8'))
  assert(packageJSON.name === EXPECTED_PI_NAME && packageJSON.version === EXPECTED_PI_VERSION, 'Pi package identity drifted')
  const targetRoot = join(stagingRoot, 'private-pi-source'); await mkdir(targetRoot, { mode: closure.rootMode }); await chmod(targetRoot, closure.rootMode)
  for (const entry of closure.entries) {
    const target = join(targetRoot, ...entry.path.split('/'))
    if (entry.type === 'directory') { await mkdir(target, { mode: entry.mode }); await chmod(target, entry.mode) }
    else if (entry.type === 'file') {
      await mkdir(dirname(target), { recursive: true, mode: 0o700 })
      const bytes = await readFile(join(request.pi.packageRoot, ...entry.path.split('/')))
      assert(sha256(bytes) === entry.sha256, `Pi ${entry.path} drifted during copy`)
      await writeFile(target, bytes, { mode: entry.mode, flag: 'wx' }); await chmod(target, entry.mode)
    } else fail(`Pi package contains unexpected symlink ${entry.path}`)
  }
  const copied = await inspectDirectory(targetRoot, 'copied Pi package')
  assert(copied.digest === closure.digest, 'copied Pi closure drifted')
  const copiedExecutable = join(targetRoot, EXPECTED_PI_EXECUTABLE)
  const version = await runVersion({
    sandboxExecutableFile: request.ocr.sandboxExecutableFile,
    sandboxExecutableSha256: request.ocr.sandboxExecutableSha256,
    nodeFile: request.node.file,
    nodeSha256: request.node.sha256,
    packageRoot: targetRoot,
    packageClosure: copied,
    executableFile: copiedExecutable,
    executableMode: executableEntry.mode,
    executableSha256: request.pi.executableSha256,
  })
  assert(version === EXPECTED_PI_VERSION, 'copied Pi version probe rejected')
  const files = closure.entries.filter((entry) => entry.type === 'file').map(({ path, mode, size, sha256 }) => ({ path, mode, size, sha256 })).sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0)
  const manifest = { schema_version: PI_MANIFEST_SCHEMA, platform: { os: 'darwin', architecture: 'arm64' }, package_name: EXPECTED_PI_NAME, version: EXPECTED_PI_VERSION, executable: EXPECTED_PI_EXECUTABLE, files, closure_sha256: piClosureDigest(files) }
  const finalRoot = join(outputRoot, 'private-pi-source'); const finalExecutable = join(finalRoot, EXPECTED_PI_EXECUTABLE)
  const pathIdentity = piSelectionIdentity('path', request.pi.executableFile, request.pi.executableFile, request.pi.packageRoot, request.pi.executableSha256, manifest.closure_sha256)
  const privateIdentity = piSelectionIdentity('private', finalExecutable, finalExecutable, finalRoot, request.pi.executableSha256, manifest.closure_sha256)
  return { closure, copied, manifest, pathProvenance: { schemaVersion: PI_PROVENANCE_SCHEMA, kind: 'path', generationId: request.identity.generationId, sourceIdentitySha256: pathIdentity, binarySha256: request.pi.executableSha256 }, privateProvenance: { schemaVersion: PI_PROVENANCE_SCHEMA, kind: 'private', generationId: request.identity.generationId, sourceIdentitySha256: privateIdentity, binarySha256: request.pi.executableSha256 } }
}

function defaultVersionRunner(binding, spawnProcess = spawn, beforeValidation,
  scheduleTimeout = setTimeout) {
  exactKeys(binding, ['sandboxExecutableFile', 'sandboxExecutableSha256', 'nodeFile',
    'nodeSha256', 'packageRoot', 'packageClosure', 'executableFile',
    'executableMode', 'executableSha256'], 'Pi version probe binding')
  absolute(binding.sandboxExecutableFile, 'Pi version sandbox executable')
  absolute(binding.nodeFile, 'Pi version Node executable')
  absolute(binding.packageRoot, 'Pi version package root')
  absolute(binding.executableFile, 'Pi version executable')
  digest(binding.sandboxExecutableSha256, 'Pi version sandbox executable SHA-256')
  digest(binding.nodeSha256, 'Pi version Node executable SHA-256')
  digest(binding.executableSha256, 'Pi version executable SHA-256')
  assert(binding.sandboxExecutableFile === '/usr/bin/sandbox-exec', 'Pi version sandbox path drifted')
  assert(binding.executableFile === join(binding.packageRoot, EXPECTED_PI_EXECUTABLE), 'Pi version executable path drifted')
  assert(Number.isSafeInteger(binding.executableMode) && (binding.executableMode & 0o100) !== 0 &&
    (binding.executableMode & 0o022) === 0, 'Pi version executable mode drifted')
  return new Promise((resolvePromise, reject) => {
    let child; let timer; let stdout = Buffer.alloc(0); let outputBytes = 0
    let settled = false; let closeSeen = false; let killRequested = false
    let terminalError
    const finish = (error, value) => {
      if (settled) return
      settled = true; if (timer) clearTimeout(timer)
      if (error) reject(error); else resolvePromise(value)
    }
    const terminateAfterClose = (error) => {
      if (!terminalError) terminalError = error
      if (!killRequested) { killRequested = true; child.kill('SIGKILL') }
    }
    const launch = async () => {
      if (beforeValidation) await beforeValidation(binding)
      const sandbox = await secureSystemFile(binding.sandboxExecutableFile,
        binding.sandboxExecutableSha256, 'Pi version sandbox executable')
      const node = await secureFile(binding.nodeFile, 0o500, binding.nodeSha256,
        'Pi version Node executable', 256 * 1024 * 1024)
      const executable = await secureFile(binding.executableFile, binding.executableMode,
        binding.executableSha256, 'Pi version executable', 256 * 1024 * 1024)
      await revalidateExactDirectoryClosure(binding.packageRoot, binding.packageClosure,
        'Pi version package closure')
      const emptyEnvironment = Object.create(null)
      child = spawnProcess(binding.sandboxExecutableFile,
        ['-p', PI_VERSION_SANDBOX_PROFILE, binding.nodeFile, binding.executableFile, '--version'],
        { shell: false, cwd: binding.packageRoot, env: emptyEnvironment,
          stdio: ['ignore', 'pipe', 'pipe'] })
      assert(child && child.stdout && child.stderr && typeof child.kill === 'function' &&
        typeof child.on === 'function', 'Pi version probe child contract drifted')
      timer = scheduleTimeout(() => {
        terminateAfterClose(new Error('Pi version probe timed out'))
      }, PI_VERSION_TIMEOUT_MS)
      const append = (current, chunk) => {
        outputBytes += chunk.length
        if (outputBytes > MAX_PI_VERSION_OUTPUT) {
          terminateAfterClose(new Error('Pi version probe output exceeded bound'))
          return current
        }
        return Buffer.concat([current, chunk])
      }
      child.stdout.on('data', (chunk) => { stdout = append(stdout, chunk) })
      child.stderr.on('data', (chunk) => { append(Buffer.alloc(0), chunk) })
      child.on('error', (error) => terminateAfterClose(error))
      child.on('close', (code) => {
        if (closeSeen) return
        closeSeen = true
        if (timer) { clearTimeout(timer); timer = undefined }
        void (async () => {
          let validationError
          const validate = async (check) => {
            try { await check() } catch (error) { if (!validationError) validationError = error }
          }
          await validate(() => revalidateExactDirectoryClosure(binding.packageRoot,
            binding.packageClosure, 'Pi version package closure'))
          await validate(async () => assert(sameFile(sandbox.snapshot,
            await lstat(binding.sandboxExecutableFile)),
          'Pi version sandbox executable TOCTOU drifted'))
          await validate(async () => assert(sameFile(node.snapshot,
            await lstat(binding.nodeFile)), 'Pi version Node executable TOCTOU drifted'))
          await validate(async () => assert(sameFile(executable.snapshot,
            await lstat(binding.executableFile)), 'Pi version executable TOCTOU drifted'))
          if (validationError) finish(validationError)
          else if (terminalError) finish(terminalError)
          else if (code !== 0) finish(new Error('Pi version probe failed'))
          else finish(null, stdout.toString('utf8').trim())
        })()
      })
    }
    void launch().catch((error) => finish(error))
  })
}

async function write0400(root, name, value) {
  assert(safeRelative(name), 'output name is unsafe'); const path = join(root, ...name.split('/'))
  const bytes = Buffer.isBuffer(value) ? value : jsonBytes(value)
  await mkdir(dirname(path), { recursive: true, mode: 0o700 })
  let handle
  try {
    handle = await open(path, 'wx', 0o600); await handle.writeFile(bytes); await handle.sync(); await handle.chmod(0o400); await handle.close(); handle = undefined
  } catch (error) { if (handle) await handle.close().catch(() => {}); throw error }
  const info = await lstat(path)
  assert(info.isFile() && !info.isSymbolicLink() && info.nlink === 1 && info.uid === process.getuid() && mode(info) === 0o400 && info.size === bytes.length, `output ${name} is unsafe`)
  const reopened = await readFile(path); assert(reopened.equals(bytes), `output ${name} changed after write`)
  return { path, sha256: sha256(reopened) }
}

function candidateTemplate(request, derived) {
  const out = derived.output
  const nullAuthority = Object.fromEntries(['candidateManifestSha256', 'offlineBundleEvidenceSha256',
    'modelAuthoritySha256', 'endpointEvidenceSha256', 'repositoryCommit',
    'repositoryClosureSha256'].map((key) => [key, null]))
  return {
    identity: request.identity,
    paths: {
      sourceBundleRoot: request.source.bundleRoot, sourceManifestFile: request.source.manifestFile, candidateManifestFile: join(out, 'candidate-manifest.template.json'), binaryFile: request.product.binaryFile,
      webDirectory: request.product.webDirectory, releaseSpecFile: join(out, 'release-spec.json'), releaseManifestFile: join(out, 'release-manifest.json'), offlineBundleEvidenceFile: null,
      pathPiProvenanceFile: join(out, 'path-pi-provenance.json'), privatePiProvenanceFile: join(out, 'private-pi-provenance.json'), modelAuthorityFile: null, qualificationFile: null, modelObservationFile: null,
      installedDoctorReportFile: null, endpointEvidenceFile: null, preflightFile: null, dockerCLIFile: request.docker.file, installRoot: null, installedBinaryFile: null, installedWebDirectory: null, installedSourceRoot: null,
      installedSourceManifestFile: null, dataRoot: null, databaseFile: null, repositoryRoot: null, authFile: request.oauth.file, caFile: request.ca.file, stateRoot: null, ledgerFile: null,
      receiptDirectory: null, privatePiRoot: null, privatePiSourceRoot: join(out, 'private-pi-source'), privatePiManifestFile: join(out, 'private-pi-manifest.json'), actualReleaseRoot: request.release.root,
      actualReleaseManifestFile: request.release.manifestFile, probeRuntimeRoot: null, colimaToolRoot: dirname(request.docker.file), colimaSourceFile: request.colima.file, dockerClientSourceFile: request.docker.file, tupleFile: null,
    },
    authority: {
      sourceManifestSha256: request.source.manifestSha256, sourceAggregateSha256: derived.sourceAggregate, binarySha256: request.product.binarySha256, webAggregateSha256: request.product.webClosureSha256,
      releaseSpecSha256: derived.releaseSpecSha256, releaseManifestSha256: derived.releaseManifestSha256, pathPiProvenanceSha256: derived.pathPiSha256, privatePiProvenanceSha256: derived.privatePiSha256,
      dockerCLISha256: request.docker.sha256, actualReleaseManifestSha256: request.release.manifestSha256, privatePiManifestSha256: derived.privatePiManifestSha256,
      authFileSha256: request.oauth.sha256, caFileSha256: request.ca.sha256, colimaSourceSha256: request.colima.sha256, dockerClientSourceSha256: request.docker.sha256,
      limaPrefixClosureSha256: request.engine.limaPrefixClosureSha256,
      limaInfoDigest: derived.limaInfoDigest,
      limaSha256: request.engine.limaSha256, diskImageSha256: request.engine.diskImageSha256, diskImageSize: request.engine.diskImageSize,
      endpointSocketPathSha256: sha256(request.engine.endpointSocketPath), sandboxExecutableSha256: request.engine.sandboxExecutableSha256, sandboxProfileSha256: request.engine.sandboxProfileSha256,
      dockerContext: request.engine.contextName, endpointDigest: request.engine.endpointDigest,
      roles: Object.fromEntries(derived.roles.map((role) => [role.role, { artifactId: role.artifactId, archiveSha256: role.archiveSha256, archiveSize: role.archiveSize, dockerConfigImageId: role.dockerConfigImageId, policyDigest: role.policyDigest }])), ...nullAuthority,
    },
    private: { proxyURL: 'http://host.docker.internal:9981', modelURL: null, port: null, colimaProfile: request.engine.profileName },
    setup: { key: null, grant: null, actor: null, session: null, releaseId: null, releaseManifestRelativePath: null },
  }
}

function candidateManifestTemplate(request, derived) {
  return { schemaVersion: CANDIDATE_SCHEMA, environmentId: request.identity.environmentId, installId: request.identity.installId, generationId: request.identity.generationId, platform: request.identity.platform,
    sourceManifestSha256: request.source.manifestSha256, sourceAggregateSha256: derived.sourceAggregate, binarySha256: request.product.binarySha256, webAggregateSha256: request.product.webClosureSha256,
    releaseSpecSha256: derived.releaseSpecSha256, releaseManifestSha256: derived.releaseManifestSha256,
    offlineBundleEvidenceSha256: null, repositoryCommit: null, repositoryClosureSha256: null }
}

function totalTemplate(request, derived) {
  const serviceControllerEnvironment = Object.freeze({
    CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_FILE: request.serviceController.executableFile,
    CHORA_O4_PROFILE_REUSE_SERVICE_CONTROLLER_SHA256: request.serviceController.sha256,
    CHORA_O4_FINAL_SERVICE_CONTROLLER_FILE: request.serviceController.executableFile,
    CHORA_O4_FINAL_SERVICE_CONTROLLER_SHA256: request.serviceController.sha256,
  })
  return {
    schemaVersion: 'chora.m1-o4-total-input.v1', status: 'static_unresolved', acceptanceClass: null, selfDigest: null, identity: request.identity,
    candidate: { configFile: join(derived.output, 'candidate-config.template.json'), configSha256: null, tupleFile: null, tupleIdentity: null },
    repository: { root: null, commit: null, closureSha256: null },
    runner: { moduleFile: request.runner.moduleFile, moduleSha256: request.runner.moduleSha256, controlSocketPath: null, sessionAuthorityFile: null }, serviceController: { executableFile: request.serviceController.executableFile, sha256: request.serviceController.sha256, requestFile: null },
    ocr: { ...request.ocr },
    engine: { colimaFile: request.colima.file, colimaSha256: request.colima.sha256,
      limaPrefixRoot: request.engine.limaPrefixRoot,
      limaPrefixClosureSha256: request.engine.limaPrefixClosureSha256,
      limaInfoDigest: derived.limaInfoDigest,
      limaFile: request.engine.limaFile, limaSha256: request.engine.limaSha256,
      limaWrapperFile: request.engine.limaWrapperFile,
      limaWrapperSha256: request.engine.limaWrapperSha256,
      limaTemplatesRoot: request.engine.limaTemplatesRoot,
      limaTemplatesClosureSha256: request.engine.limaTemplatesClosureSha256,
      limaGuestAgentFile: request.engine.limaGuestAgentFile,
      limaGuestAgentSize: request.engine.limaGuestAgentSize,
      limaGuestAgentSha256: request.engine.limaGuestAgentSha256,
      dockerFile: request.docker.file, dockerSha256: request.docker.sha256,
      diskImageFile: request.engine.diskImageFile, diskImageSize: request.engine.diskImageSize,
      diskImageSha256: request.engine.diskImageSha256,
      sandboxExecutableFile: request.engine.sandboxExecutableFile,
      sandboxExecutableSha256: request.engine.sandboxExecutableSha256,
      sandboxProfileFile: request.engine.sandboxProfileFile,
      sandboxProfileSha256: request.engine.sandboxProfileSha256,
      profileName: request.engine.profileName, contextName: request.engine.contextName,
      endpointSocketPath: request.engine.endpointSocketPath,
      endpointDigest: request.engine.endpointDigest, cpus: request.engine.cpus,
      memoryGiB: request.engine.memoryGiB, diskGiB: request.engine.diskGiB,
      zeroImagePreflightFile: null, cleanupResidueFile: null },
    playwright: { nodeFile: request.node.file, nodeSha256: request.node.sha256, cliFile: request.playwright.cliFile, cliSha256: request.playwright.cliSha256, packageRoot: request.playwright.packageRoot, packageClosureSha256: request.playwright.packageClosureSha256, browserRoot: request.playwright.browserRoot, browserClosureSha256: request.playwright.browserClosureSha256 },
    roots: { userRoot: null, installRoot: null, dataRoot: null, stateRoot: null, receiptRoot: null, evidenceRoot: null, runnerControlRoot: null, probeRuntimeRoot: null, colimaHome: null, dockerConfigRoot: null, temporaryRoot: null },
    phases: Object.fromEntries(['C', 'D', 'E'].map((phase) => [phase, { configFile: request.configs[phase].file, configSha256: request.configs[phase].sha256, environment: Object.fromEntries(PHASE_ENVIRONMENT[phase].map((name) => [name, serviceControllerEnvironment[name] ?? null])) }])),
    postflight: { sourceA3LedgerFile: null, finalEvidenceDirectory: null, phaseCManifestFile: null, phaseCRecordFile: null, phaseDManifestFile: null, phaseDRecordFile: null, phaseDDisclosureSourceDirectory: null, phaseDScreenshotDirectory: null, phaseDScreenshotMetadataDirectory: null, phaseDScreenshotOCRDirectory: null, publicResidueFile: null },
    timeouts: { engineMs: request.engine.engineMs, phaseMs: null, controllerMs: null, exporterMs: null, closeMs: null }, completionFile: null,
  }
}

function deriveLimaInfoDigest(request, limaTemplates) {
  const templates = limaTemplates.entries
    .filter((entry) => entry.type === 'file' && entry.path.endsWith('.yaml'))
    .map((entry) => join(request.engine.limaTemplatesRoot, ...entry.path.split('/')))
    .sort()
  assert(templates.length === 120 && new Set(templates).size === 120,
    'canonical Lima info template authority is incomplete')
  return sha256(canonical({
    templates,
    guestAgent: request.engine.limaGuestAgentFile,
    hostOS: 'darwin',
    hostArch: 'aarch64',
  }))
}

function rejectSameFileBindings(bindings) {
  const seen = new Map()
  for (const [path, info] of bindings) {
    const identity = `${info.dev}:${info.ino}`
    assert(!seen.has(identity), `input files ${seen.get(identity)} and ${path} are the same file`)
    seen.set(identity, path)
  }
}

async function validateFixedInputs(request) {
  const files = []
  const add = async (path, expectedMode, expectedSha, label, max = MAX_JSON) => {
    const observed = await secureFile(path, expectedMode, expectedSha, label, max)
    files.push({ path, snapshot: observed.snapshot, label }); return observed
  }
  await add(request.product.binaryFile, 0o500, request.product.binarySha256, 'product binary', 256 * 1024 * 1024)
  await add(request.oauth.file, 0o600, request.oauth.sha256, 'OAuth file')
  await add(request.ca.file, 0o400, request.ca.sha256, 'CA file', 4 * 1024 * 1024)
  await add(request.node.file, 0o500, request.node.sha256, 'Node executable', 256 * 1024 * 1024)
  await add(request.playwright.cliFile, 0o400, request.playwright.cliSha256, 'Playwright CLI', 64 * 1024 * 1024)
  for (const phase of ['C', 'D', 'E']) await add(request.configs[phase].file, 0o400, request.configs[phase].sha256, `phase ${phase} config`)
  await add(request.runner.moduleFile, 0o400, request.runner.moduleSha256, 'Runner module')
  await add(request.serviceController.executableFile, 0o500, request.serviceController.sha256, 'service controller', 256 * 1024 * 1024)
  await add(request.ocr.executableFile, 0o500, request.ocr.executableSha256, 'system-managed OCR executable', 256 * 1024 * 1024)
  const binding = await parseJSONFile(request.ocr.engineBindingFile, 0o400, request.ocr.engineBindingSha256, 'system-managed OCR engine binding', 4 * 1024 * 1024)
  files.push({ path: request.ocr.engineBindingFile, snapshot: binding.snapshot, label: 'system-managed OCR engine binding' }); validateOCRBinding(binding.value, request)
  const ocrProfile = await add(request.ocr.sandboxProfileFile, 0o400, request.ocr.sandboxProfileSha256, 'system-managed OCR sandbox profile')
  validateOCRSandboxProfile(ocrProfile.bytes)
  const systemSandbox = await secureSystemFile(request.ocr.sandboxExecutableFile, request.ocr.sandboxExecutableSha256, 'system sandbox executable')
  files.push({ path: request.ocr.sandboxExecutableFile, snapshot: systemSandbox.snapshot, label: 'system sandbox executable' })
  await add(request.engine.limaFile, 0o500, request.engine.limaSha256, 'canonical limactl executable', 256 * 1024 * 1024)
  await add(request.engine.limaWrapperFile, 0o500, request.engine.limaWrapperSha256,
    'canonical Lima wrapper', 256 * 1024 * 1024)
  await add(request.engine.limaGuestAgentFile, 0o400, request.engine.limaGuestAgentSha256,
    'canonical Lima Linux-aarch64 guest agent', 256 * 1024 * 1024)
  const limaPrefix = await inspectDirectory(request.engine.limaPrefixRoot,
    'canonical Lima prefix', { maxEntries: 131, maxFiles: 124, maxBytes: 39_872_398 })
  const limaTemplates = await inspectDirectory(request.engine.limaTemplatesRoot,
    'canonical Lima templates', { maxEntries: 124, maxFiles: 121, maxBytes: 148_059 })
  const prefixFiles = limaPrefix.entries.filter((entry) => entry.type === 'file')
  const prefixDirectories = limaPrefix.entries.filter((entry) => entry.type === 'directory')
  const templateFiles = limaTemplates.entries.filter((entry) => entry.type === 'file')
  const templateDirectories = limaTemplates.entries.filter((entry) => entry.type === 'directory')
  assert(limaPrefix.rootMode === 0o700 && limaPrefix.digest === request.engine.limaPrefixClosureSha256 &&
    limaPrefix.fileCount === 124 && limaPrefix.totalBytes === 39_872_398 &&
    prefixDirectories.length === 7 &&
    limaTemplates.rootMode === 0o700 &&
    limaTemplates.digest === request.engine.limaTemplatesClosureSha256 &&
    limaTemplates.fileCount === 121 && limaTemplates.totalBytes === 148_059 &&
    templateDirectories.length === 3 &&
    limaPrefix.entries.every((entry) => entry.mode === (entry.type === 'directory' ? 0o700 :
      entry.path.startsWith('bin/') ? 0o500 : 0o400)) &&
    limaTemplates.entries.every((entry) => entry.mode ===
      (entry.type === 'directory' ? 0o700 : 0o400)),
  'canonical Lima portable prefix closure drifted')
  assert(prefixFiles.some((entry) => entry.path === 'bin/limactl' &&
      entry.sha256 === request.engine.limaSha256) &&
    prefixFiles.some((entry) => entry.path === 'bin/lima' &&
      entry.sha256 === request.engine.limaWrapperSha256) &&
    prefixFiles.some((entry) => entry.path === 'share/lima/lima-guestagent.Linux-aarch64.gz' &&
      entry.size === request.engine.limaGuestAgentSize &&
      entry.sha256 === request.engine.limaGuestAgentSha256) &&
    templateFiles.some((entry) => entry.path === 'default.yaml') &&
    templateFiles.some((entry) => entry.path === '_images/ubuntu.yaml') &&
    templateFiles.some((entry) => entry.path === '_default/mounts.yaml'),
  'canonical Lima portable prefix required entries drifted')
  const disk = await secureLargeFile(request.engine.diskImageFile, request.engine.diskImageSize, request.engine.diskImageSha256, 'fresh Engine disk image')
  files.push({ path: request.engine.diskImageFile, snapshot: disk.snapshot, label: 'fresh Engine disk image' })
  const engineProfile = await add(request.engine.sandboxProfileFile, 0o400, request.engine.sandboxProfileSha256, 'fresh Engine sandbox profile', 16 * 1024)
  assert(engineProfile.bytes.toString('utf8') === ENGINE_BOOTSTRAP_SANDBOX_PROFILE, 'fresh Engine sandbox profile is not the exact local/Unix-only policy')
  await add(request.docker.file, 0o500, request.docker.sha256, 'Docker executable', 256 * 1024 * 1024)
  await add(request.colima.file, 0o500, request.colima.sha256, 'Colima executable', 256 * 1024 * 1024)
  return { files, limaPrefix, limaTemplates }
}

async function revalidateFiles(bindings) {
  for (const [path, expected, label] of bindings) assert(sameFile(expected, await lstat(path)), `${label} TOCTOU drifted`)
}

export async function prepareO4StaticInputs({ requestFile, requestSha256, outputRoot }, dependencies = {}) {
  absolute(requestFile, 'request file'); digest(requestSha256, 'request SHA-256'); absolute(outputRoot, 'output root')
  await noSymlinkAncestors(outputRoot); await expectAbsent(outputRoot, 'output root')
  const parent = dirname(outputRoot); const parentInfo = await lstat(parent)
  assert(parentInfo.isDirectory() && !parentInfo.isSymbolicLink() && parentInfo.uid === process.getuid() && (mode(parentInfo) & 0o022) === 0, 'output parent is unsafe')
  let staging = join(parent, `.${relative(parent, outputRoot)}.stage-${randomBytes(12).toString('hex')}`)
  let stagingIdentity; let stagingSnapshot; let request; let ownedPathCapability
  await expectAbsent(staging, 'staging root')
  try {
    const requestInput = await parseJSONFile(requestFile, 0o400, requestSha256, 'request')
    validateRequest(requestInput.value); request = requestInput.value
    ownedPathCapability = Object.freeze({
      executableFile: request.serviceController.executableFile,
      executableSha256: request.serviceController.sha256,
    })
    for (const root of [request.release.root, request.source.bundleRoot, request.pi.packageRoot,
      request.playwright.packageRoot, request.playwright.browserRoot, request.product.webDirectory,
      request.engine.limaPrefixRoot]) assert(!overlaps(outputRoot, root), 'output root overlaps an input root')
    const release = await validateRelease(request); const source = await validateSource(request)
    const fixedValidation = await validateFixedInputs(request); const fixed = fixedValidation.files
    rejectSameFileBindings([[requestFile, requestInput.snapshot], [request.source.manifestFile, source.parsed.snapshot], [request.release.manifestFile, release.parsed.snapshot], ...fixed.map((item) => [item.path, item.snapshot])])
    const web = await inspectWeb(request.product.webDirectory); assert(web.digest === request.product.webClosureSha256, 'web closure drifted')
    const playwright = await inspectDirectory(request.playwright.packageRoot, 'Playwright package', { allowSymlinks: true }); assert(playwright.digest === request.playwright.packageClosureSha256, 'Playwright package closure drifted')
    const browser = await inspectDirectory(request.playwright.browserRoot, 'browser closure', { allowSymlinks: true }); assert(browser.digest === request.playwright.browserClosureSha256, 'browser closure drifted')
    await mkdir(staging, { mode: 0o700 })
    stagingSnapshot = await lstat(staging, { bigint: true })
    stagingIdentity = ownedPathIdentity(stagingSnapshot)
    assert(sameOwnedDirectory(stagingSnapshot, stagingSnapshot), 'staging root is unsafe')
    const parentAfterStaging = await lstat(parent)
    const pi = await copyPrivatePi(request, staging, outputRoot,
      (binding) => defaultVersionRunner(binding, dependencies.spawnVersionProbe ?? spawn,
        dependencies.beforeVersionProbeValidation,
        dependencies.scheduleVersionProbeTimeout ?? setTimeout))
    const releaseSpec = { schemaVersion: RELEASE_SPEC_SCHEMA, generationId: request.identity.generationId, platform: request.identity.platform, roles: release.roles }
    const releaseSpecResult = await write0400(staging, 'release-spec.json', releaseSpec)
    const releaseManifest = { schemaVersion: RELEASE_MANIFEST_SCHEMA, generationId: request.identity.generationId, releaseSpecSha256: releaseSpecResult.sha256, roles: release.roles }
    const releaseManifestResult = await write0400(staging, 'release-manifest.json', releaseManifest)
    const pathPi = await write0400(staging, 'path-pi-provenance.json', pi.pathProvenance)
    const privatePi = await write0400(staging, 'private-pi-provenance.json', pi.privateProvenance)
    const privatePiManifest = await write0400(staging, 'private-pi-manifest.json', pi.manifest)
    await write0400(staging, 'pi-source-closure.json', { schemaVersion: DIRECTORY_CLOSURE_SCHEMA, kind: 'pi-source', root: request.pi.packageRoot, rootMode: pi.closure.rootMode, digest: pi.closure.digest, entries: pi.closure.entries })
    await write0400(staging, 'playwright-package-closure.json', { schemaVersion: DIRECTORY_CLOSURE_SCHEMA, kind: 'playwright-package', root: request.playwright.packageRoot, rootMode: playwright.rootMode, digest: playwright.digest, entries: playwright.entries })
    await write0400(staging, 'playwright-browser-closure.json', { schemaVersion: DIRECTORY_CLOSURE_SCHEMA, kind: 'playwright-browser', root: request.playwright.browserRoot, rootMode: browser.rootMode, digest: browser.digest, entries: browser.entries })
    const derivedLimaInfoDigest = deriveLimaInfoDigest(request, fixedValidation.limaTemplates)
    assert(derivedLimaInfoDigest === request.engine.limaInfoDigest,
      'canonical Lima info authority drifted')
    const derived = { output: outputRoot, roles: release.roles, sourceAggregate: source.aggregate, releaseSpecSha256: releaseSpecResult.sha256, releaseManifestSha256: releaseManifestResult.sha256, pathPiSha256: pathPi.sha256, privatePiSha256: privatePi.sha256, privatePiManifestSha256: privatePiManifest.sha256, limaInfoDigest: derivedLimaInfoDigest }
    await write0400(staging, 'candidate-manifest.template.json', candidateManifestTemplate(request, derived))
    await write0400(staging, 'candidate-config.template.json', candidateTemplate(request, derived))
    await write0400(staging, 'total-manifest.template.json', totalTemplate(request, derived))
    const result = { schemaVersion: RESULT_SCHEMA, status: 'static_unresolved', identity: request.identity, release: { actualManifestSha256: request.release.manifestSha256, projectedSpecSha256: derived.releaseSpecSha256, projectedManifestSha256: derived.releaseManifestSha256, roles: release.roles }, source: { manifestSha256: request.source.manifestSha256, aggregateSha256: source.aggregate }, product: { binarySha256: request.product.binarySha256, webClosureSha256: request.product.webClosureSha256 }, pi: { packageClosureSha256: request.pi.packageClosureSha256, productClosureSha256: pi.manifest.closure_sha256, privateManifestSha256: privatePiManifest.sha256, pathProvenanceSha256: pathPi.sha256, privateProvenanceSha256: privatePi.sha256 }, playwright: { packageClosureSha256: playwright.digest, browserClosureSha256: browser.digest }, opaqueOAuthSha256: request.oauth.sha256, dynamicFields: ['candidate.offlineBundleEvidenceSha256', 'candidate.modelAuthorityFile', 'candidate.qualificationFile', 'candidate.modelObservationFile', 'candidate.installedDoctorReportFile', 'candidate.endpointEvidenceFile', 'candidate.preflightFile', 'candidate.mutableRoots', 'candidate.tupleIdentity', 'modelRoute', 'setupAuthority', 'engine.zeroImagePreflightFile', 'engine.cleanupResidueFile', 'runnerControl', 'serviceController.requestFile', 'freshRoots', 'phaseEnvironments', 'postflight', 'timeouts.phaseControllerExporterClose', 'completionFile'] }
    await write0400(staging, 'static-inputs.json', result)
    if (dependencies.beforeCommit) await dependencies.beforeCommit({ staging, outputRoot })
    await revalidateFiles([[requestFile, requestInput.snapshot, 'request'], ...fixed.map((item) => [item.path, item.snapshot, item.label]), ...release.snapshots.entries()].map((entry) => entry.length === 2 ? [entry[0], entry[1], 'release input'] : entry))
    for (const [path, snapshot] of source.snapshots) assert(sameFile(snapshot, await lstat(path)), 'source TOCTOU drifted')
    await revalidateDirectory(join(request.source.bundleRoot, 'source'), source.tree, 'source tree')
    await revalidateDirectory(request.product.webDirectory, web, 'web directory'); await revalidateDirectory(request.playwright.packageRoot, playwright, 'Playwright package'); await revalidateDirectory(request.playwright.browserRoot, browser, 'browser closure'); await revalidateDirectory(request.pi.packageRoot, pi.closure, 'Pi package')
    await revalidateDirectory(request.engine.limaPrefixRoot, fixedValidation.limaPrefix,
      'canonical Lima prefix')
    await revalidateDirectory(request.engine.limaTemplatesRoot, fixedValidation.limaTemplates,
      'canonical Lima templates')
    assert(sameOwnedDirectory(stagingSnapshot, await lstat(staging, { bigint: true })),
      'static-input staging directory identity changed before publication')
    await expectAbsent(outputRoot, 'output root')
    assert(sameDirectory(parentAfterStaging, await lstat(parent)), 'output parent TOCTOU drifted')
    await publishOwnedPath({ capability: ownedPathCapability, sourcePath: staging,
      targetPath: outputRoot, identity: stagingIdentity, disposition: 'recursive_directory' },
    dependencies.publishOwnedPath)
    staging = null
    return Object.freeze({ outputRoot, manifestFile: join(outputRoot, 'static-inputs.json'), result })
  } catch (error) {
    const secondary = []
    let cleanupError
    if (staging && stagingIdentity) {
      try { await cleanupOwnedPath({ capability: ownedPathCapability, path: staging,
        identity: stagingIdentity, disposition: 'recursive_directory' }, dependencies.cleanupOwnedPath) }
      catch (failure) { cleanupError = failure; secondary.push(failure) }
    }
    let outputError
    try { await expectAbsent(outputRoot, 'output root') }
    catch (failure) { outputError = failure; secondary.push(failure) }
    if (secondary.length > 0) {
      const details = [cleanupError && `staging cleanup failed: ${cleanupError.message}`,
        outputError && `output absence verification failed: ${outputError.message}`].filter(Boolean).join('; ')
      throw new AggregateError([error, ...secondary],
        `O4 static input preparation failed; ${details}`, { cause: error })
    }
    throw error
  }
}

function parseCLI(argv) {
  const values = {}; for (let i = 0; i < argv.length; i += 2) { assert(argv[i]?.startsWith('--') && argv[i + 1], 'usage: --request FILE --request-sha256 HEX --output-root DIR'); values[argv[i].slice(2)] = argv[i + 1] }
  exactKeys(values, ['request', 'request-sha256', 'output-root'], 'CLI arguments')
  return { requestFile: values.request, requestSha256: values['request-sha256'], outputRoot: values['output-root'] }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  prepareO4StaticInputs(parseCLI(process.argv.slice(2))).then(({ manifestFile }) => process.stdout.write(`${manifestFile}\n`)).catch((error) => { process.stderr.write(`O4 static input preparation failed: ${error.message}\n`); process.exitCode = 1 })
}
