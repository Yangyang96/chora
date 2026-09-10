import { createHash } from 'node:crypto'
import { spawn as nodeSpawn } from 'node:child_process'
import { constants as fsConstants } from 'node:fs'
import { lstat, open, readdir } from 'node:fs/promises'
import { dirname, isAbsolute, relative, resolve, sep } from 'node:path'

const CLEANUP_PROTOCOL = 'chora.m1-o4-owned-path-cleanup.v1'
const CLEANUP_SCHEMA = 'chora.m1-o4-owned-path-cleanup-request.v1'
const PUBLISH_PROTOCOL = 'chora.m1-o4-owned-path-publish.v1'
const PUBLISH_SCHEMA = 'chora.m1-o4-owned-path-publish-request.v1'
const REPLACE_PROTOCOL = 'chora.m1-o4-owned-path-replace.v1'
const REPLACE_SCHEMA = 'chora.m1-o4-owned-path-replace-request.v1'
const DIRECTORY_CREATE_PROTOCOL = 'chora.m1-o4-owned-directory-create.v1'
const DIRECTORY_CREATE_SCHEMA = 'chora.m1-o4-owned-directory-create-request.v1'
const DIRECTORY_CREATE_RECEIPT_SCHEMA = 'chora.m1-o4-owned-directory-create-receipt.v1'
const READONLY_WRITE_PROTOCOL = 'chora.m1-o4-owned-readonly-write.v1'
const READONLY_WRITE_RECEIPT_SCHEMA = 'chora.m1-o4-owned-readonly-write-receipt.v1'
const MAX_REQUEST_BYTES = 64 * 1024
const MAX_OUTPUT_BYTES = 64 * 1024
const MAX_READONLY_BYTES = 8 * 1024 * 1024
const MAX_READONLY_RECEIPT_BYTES = 4 * 1024
const MAX_EXECUTABLE_BYTES = 256 * 1024 * 1024
const TIMEOUT_MS = 120_000
const SAFE_READ_FLAGS = fsConstants.O_RDONLY | (fsConstants.O_NOFOLLOW ?? 0) |
  (fsConstants.O_NONBLOCK ?? 0) | (fsConstants.O_NOCTTY ?? 0)
const digestRE = /^[0-9a-f]{64}$/
const decimalRE = /^(?:0|[1-9][0-9]*)$/
const dispositions = new Set([
  'regular_file', 'empty_directory', 'recursive_directory', 'unix_socket',
])

export function ownedPathIdentity(bigintStats) {
  assert(bigintStats && typeof bigintStats === 'object' &&
    typeof bigintStats.mode === 'bigint' && typeof bigintStats.uid === 'bigint' &&
    typeof bigintStats.gid === 'bigint' && typeof bigintStats.dev === 'bigint' &&
    typeof bigintStats.ino === 'bigint',
  'owned path identity requires bigint filesystem stats')
  const mode = Number(bigintStats.mode)
  const uid = Number(bigintStats.uid)
  const gid = Number(bigintStats.gid)
  assert(Number.isSafeInteger(mode) && mode >= 0 && Number.isSafeInteger(uid) && uid >= 0 &&
    Number.isSafeInteger(gid) && gid >= 0,
  'owned path identity exceeds the exact JSON integer range')
  return Object.freeze({ mode, uid, gid, dev: bigintStats.dev.toString(), ino: bigintStats.ino.toString() })
}

export function validateOwnedPathCapability(value, label = 'owned-path capability') {
  plain(value, label)
  exactKeys(value, ['executableFile', 'executableSha256'], label)
  exactAbsolute(value.executableFile, `${label} executable`)
  assert(digestRE.test(value.executableSha256), `${label} executable digest is invalid`)
  return Object.freeze({
    executableFile: value.executableFile,
    executableSha256: value.executableSha256,
  })
}

export async function cleanupOwnedPath({ capability, path, identity, disposition } = {}, deps = undefined) {
  const exactIdentity = validateIdentity(identity, 'owned cleanup identity')
  const exactDispositionValue = exactDisposition(disposition)
  validateDispositionIdentity(exactDispositionValue, exactIdentity)
  const request = {
    schemaVersion: CLEANUP_SCHEMA,
    action: 'remove_owned_path',
    path: exactAbsolute(path, 'owned cleanup path'),
    disposition: exactDispositionValue,
    expectedIdentity: exactIdentity,
  }
  const executableInsideAffectedPath = pathContains(request.path, capability?.executableFile)
  await invokeController(capability, CLEANUP_PROTOCOL, request, {
    ...validatedDeps(deps), executableInsideAffectedPath,
    before: async () => assertOptionalOwnedPath(request.path, request.expectedIdentity,
      request.disposition, 'owned cleanup path'),
    after: async () => assertAbsent(request.path, 'owned cleanup path after controller'),
  })
}

export async function createOwnedDirectory({ capability, path, parentIdentity } = {}, deps = undefined) {
  const target = exactAbsolute(path, 'owned directory target')
  const exactParentIdentity = validateIdentity(parentIdentity, 'owned directory parent identity')
  const request = {
    schemaVersion: DIRECTORY_CREATE_SCHEMA,
    action: 'create_owned_directory',
    path: target,
    expectedParentIdentity: exactParentIdentity,
  }
  const receiptBytes = await invokeController(capability, DIRECTORY_CREATE_PROTOCOL, request, {
    ...validatedDeps(deps), executableInsideAffectedPath: false,
    before: async () => assertAbsent(target, 'owned directory target before controller'),
    after: async () => {},
    receipt: true,
  })
  const receipt = parseSingleJSONReceipt(receiptBytes, 'owned directory create receipt')
  plain(receipt, 'owned directory create receipt')
  exactKeys(receipt, ['schemaVersion', 'status', 'path', 'identity'],
    'owned directory create receipt')
  assert(receipt.schemaVersion === DIRECTORY_CREATE_RECEIPT_SCHEMA &&
    receipt.status === 'passed' && receipt.path === target,
  'owned directory create receipt drifted')
  const identity = validateIdentity(receipt.identity, 'owned directory created identity')
  await assertOwnedPath(target, identity, 'empty_directory', 'created owned directory')
  return Object.freeze(identity)
}

export async function publishOwnedPath({
  capability, sourcePath, targetPath, identity, disposition,
} = {}, deps = undefined) {
  const exactIdentity = validateIdentity(identity, 'owned publish identity')
  const exactDispositionValue = exactDisposition(disposition)
  validateDispositionIdentity(exactDispositionValue, exactIdentity)
  const request = {
    schemaVersion: PUBLISH_SCHEMA,
    action: 'publish_owned_path',
    sourcePath: exactAbsolute(sourcePath, 'owned publish source path'),
    targetPath: exactAbsolute(targetPath, 'owned publish target path'),
    disposition: exactDispositionValue,
    expectedIdentity: exactIdentity,
  }
  assert(request.sourcePath !== request.targetPath &&
    dirname(request.sourcePath) === dirname(request.targetPath),
  'owned publish paths are not distinct canonical siblings')
  const executableInsideAffectedPath = pathContains(request.sourcePath, capability?.executableFile)
  await invokeController(capability, PUBLISH_PROTOCOL, request, {
    ...validatedDeps(deps), executableInsideAffectedPath,
    before: async () => {
      await assertOwnedPath(request.sourcePath, request.expectedIdentity, request.disposition,
        'owned publish source path')
      await assertAbsent(request.targetPath, 'owned publish target path before controller')
    },
    after: async () => {
      await assertAbsent(request.sourcePath, 'owned publish source path after controller')
      await assertOwnedPath(request.targetPath, request.expectedIdentity, request.disposition,
        'owned publish target path')
    },
  })
}

export async function replaceOwnedPath({
  capability, sourcePath, targetPath, sourceIdentity, targetIdentity,
  sourceDisposition = 'regular_file', targetDisposition = 'regular_file',
} = {}, deps = undefined) {
  const exactSourceIdentity = validateIdentity(sourceIdentity, 'owned replace source identity')
  const exactTargetIdentity = validateIdentity(targetIdentity, 'owned replace target identity')
  const exactSourceDisposition = exactDisposition(sourceDisposition)
  const exactTargetDisposition = exactDisposition(targetDisposition)
  assert(exactSourceDisposition === 'regular_file' && exactTargetDisposition === 'regular_file',
    'owned replace dispositions must both be regular_file')
  validateDispositionIdentity(exactSourceDisposition, exactSourceIdentity)
  validateDispositionIdentity(exactTargetDisposition, exactTargetIdentity)
  const request = {
    schemaVersion: REPLACE_SCHEMA,
    action: 'replace_owned_path',
    sourcePath: exactAbsolute(sourcePath, 'owned replace source path'),
    targetPath: exactAbsolute(targetPath, 'owned replace target path'),
    sourceDisposition: exactSourceDisposition,
    targetDisposition: exactTargetDisposition,
    sourceExpectedIdentity: exactSourceIdentity,
    targetExpectedIdentity: exactTargetIdentity,
  }
  assert(request.sourcePath !== request.targetPath &&
    dirname(request.sourcePath) === dirname(request.targetPath),
  'owned replace paths are not distinct canonical siblings')
  await invokeController(capability, REPLACE_PROTOCOL, request, {
    ...validatedDeps(deps), executableInsideAffectedPath: false,
    before: async () => {
      await assertOwnedPath(request.sourcePath, request.sourceExpectedIdentity,
        request.sourceDisposition, 'owned replace source path')
      await assertOwnedPath(request.targetPath, request.targetExpectedIdentity,
        request.targetDisposition, 'owned replace target path')
    },
    after: async () => {
      await assertAbsent(request.sourcePath, 'owned replace source path after controller')
      await assertOwnedPath(request.targetPath, request.sourceExpectedIdentity,
        request.sourceDisposition, 'owned replace target path')
    },
  })
}

// Writes one final commit-marker file through the pinned native controller.
// The returned identity is the inode published by the controller itself; a
// later pathname observation is accepted only when it still names that exact
// readonly inode and its complete bytes remain digest-exact.
export async function writeReadonlyOwnedPath({ capability, path, bytes } = {}, deps = undefined) {
  const target = exactAbsolute(path, 'owned readonly target')
  assert(Buffer.isBuffer(bytes) && bytes.length > 0 && bytes.length <= MAX_READONLY_BYTES,
    'owned readonly payload is not one bounded Buffer')
  const expectedSha256 = createHash('sha256').update(bytes).digest('hex')
  const exactCapability = validateOwnedPathCapability(
    capability, 'owned readonly controller capability')
  const options = validatedDeps(deps)
  await assertAbsent(target, 'owned readonly target before controller')
  const executable = await openStableExecutable(exactCapability)
  let operationError
  let receiptBytes
  try {
    if (options.beforeSpawn !== undefined) {
      await options.beforeSpawn(Object.freeze({
        executableFile: exactCapability.executableFile,
        protocol: READONLY_WRITE_PROTOCOL,
        target,
        sha256: expectedSha256,
        byteLength: bytes.length,
      }))
    }
    await verifyExecutablePathAndHandle(executable, exactCapability,
      'owned readonly controller changed before spawn')
    const argv = [
      '--protocol', READONLY_WRITE_PROTOCOL,
      '--controller-sha256', exactCapability.executableSha256,
      '--target', target,
      '--expected-sha256', expectedSha256,
      '--expected-byte-length', String(bytes.length),
      '--network', 'disabled',
      '--input', 'stdin',
    ]
    receiptBytes = await runBoundedReceiptProcess(
      exactCapability.executableFile, argv, bytes, options)
  } catch (error) {
    operationError = error
  }

  let verificationError
  try {
    await verifyExecutablePathAndHandle(executable, exactCapability,
      'owned readonly controller changed during execution')
  } catch (error) {
    verificationError = error
  } finally {
    await executable.handle.close()
  }
  if (operationError !== undefined && verificationError !== undefined) {
    throw new AggregateError([operationError, verificationError],
      'owned readonly controller operation and verification both failed')
  }
  if (operationError !== undefined) throw operationError
  if (verificationError !== undefined) throw verificationError

  const receipt = parseReadonlyWriteReceipt(receiptBytes, {
    target, expectedSha256, expectedByteLength: bytes.length,
  })
  await verifyReadonlyTarget(receipt)
  return receipt
}

async function invokeController(capabilityValue, protocol, request, options) {
  const capability = validateOwnedPathCapability(capabilityValue, 'owned-path controller capability')
  const requestBytes = Buffer.from(canonicalJSONStringify(request))
  assert(requestBytes.length > 0 && requestBytes.length <= MAX_REQUEST_BYTES,
    'owned-path controller request exceeds its byte bound')
  await options.before()
  const executable = await openStableExecutable(capability)
  let operationError
  let receiptBytes
  try {
    if (options.beforeSpawn !== undefined) {
      await options.beforeSpawn(Object.freeze({
        executableFile: capability.executableFile,
        protocol,
        request: deepFreeze(structuredClone(request)),
      }))
    }
    await verifyExecutablePathAndHandle(executable, capability,
      'owned-path controller changed before spawn')
    const argv = [
      '--protocol', protocol,
      '--controller-sha256', capability.executableSha256,
      '--network', 'disabled',
      '--request-input', 'stdin',
    ]
    receiptBytes = options.receipt === true ?
      await runBoundedReceiptProcess(capability.executableFile, argv, requestBytes, {
        spawn: options.spawn, timeoutMs: options.timeoutMs,
      }) : await runBoundedProcess(capability.executableFile, argv, requestBytes, {
        spawn: options.spawn, timeoutMs: options.timeoutMs,
      })
  } catch (error) {
    operationError = error
  }

  let verificationError
  try {
    await verifyExecutableHandle(executable, capability,
      'owned-path controller changed during execution',
      operationError === undefined && options.executableInsideAffectedPath)
    if (!options.executableInsideAffectedPath) {
      await verifyExecutablePath(executable, capability,
        'owned-path controller path changed during execution')
    }
    if (operationError === undefined) await options.after()
  } catch (error) {
    verificationError = error
  } finally {
    await executable.handle.close()
  }
  if (operationError !== undefined && verificationError !== undefined) {
    throw new AggregateError([operationError, verificationError],
      'owned-path controller operation and verification both failed')
  }
  if (operationError !== undefined) throw operationError
  if (verificationError !== undefined) throw verificationError
  return receiptBytes
}

function validatedDeps(deps) {
  if (deps === undefined) return { spawn: nodeSpawn, timeoutMs: TIMEOUT_MS }
  plain(deps, 'owned-path controller dependencies')
  const allowed = new Set(['spawn', 'beforeSpawn', 'timeoutMs'])
  assert(Object.keys(deps).every((key) => allowed.has(key)),
    'owned-path controller dependency keys drifted')
  if (deps.spawn !== undefined) assert(typeof deps.spawn === 'function',
    'owned-path controller spawn dependency is invalid')
  if (deps.beforeSpawn !== undefined) assert(typeof deps.beforeSpawn === 'function',
    'owned-path controller beforeSpawn dependency is invalid')
  if (deps.timeoutMs !== undefined) assert(Number.isSafeInteger(deps.timeoutMs) &&
    deps.timeoutMs > 0 && deps.timeoutMs <= TIMEOUT_MS,
  'owned-path controller timeout dependency is invalid')
  return {
    spawn: deps.spawn ?? nodeSpawn,
    beforeSpawn: deps.beforeSpawn,
    timeoutMs: deps.timeoutMs ?? TIMEOUT_MS,
  }
}

async function openStableExecutable(capability) {
  const pathInfo = await lstat(capability.executableFile, { bigint: true })
  assertSafeExecutable(pathInfo)
  const handle = await open(capability.executableFile, SAFE_READ_FLAGS)
  try {
    const handleInfo = await handle.stat({ bigint: true })
    assert(sameStableIdentity(pathInfo, handleInfo),
      'owned-path controller identity changed before open')
    const opened = { handle, identity: stableIdentity(handleInfo) }
    await verifyExecutableHandle(opened, capability, 'owned-path controller changed while hashing')
    return opened
  } catch (error) {
    await handle.close()
    throw error
  }
}

async function verifyExecutablePathAndHandle(executable, capability, label) {
  await verifyExecutablePath(executable, capability, label)
  await verifyExecutableHandle(executable, capability, label)
}

async function verifyExecutablePath(executable, capability, label) {
  const observed = await lstat(capability.executableFile, { bigint: true })
  assertSafeExecutable(observed)
  assert(sameStableIdentity(executable.identity, observed), label)
}

async function verifyExecutableHandle(executable, capability, label, allowUnlinked = false) {
  const before = await executable.handle.stat({ bigint: true })
  assertSafeExecutable(before, allowUnlinked)
  assert(allowUnlinked
    ? sameExecutableObject(executable.identity, before)
    : sameStableIdentity(executable.identity, before), label)
  const actualDigest = await digestHandle(executable.handle, before.size)
  const after = await executable.handle.stat({ bigint: true })
  assert((allowUnlinked ? sameExecutableObject(before, after) : sameStableIdentity(before, after)) &&
    actualDigest === capability.executableSha256, label)
}

async function digestHandle(handle, bigintSize) {
  assert(bigintSize >= 0n && bigintSize <= BigInt(MAX_EXECUTABLE_BYTES),
    'owned-path controller executable exceeds its byte bound')
  const size = Number(bigintSize)
  const hash = createHash('sha256')
  const buffer = Buffer.allocUnsafe(64 * 1024)
  let position = 0
  while (position < size) {
    const { bytesRead } = await handle.read(buffer, 0, Math.min(buffer.length, size - position), position)
    assert(bytesRead > 0, 'owned-path controller executable ended while hashing')
    hash.update(buffer.subarray(0, bytesRead))
    position += bytesRead
  }
  return hash.digest('hex')
}

function runBoundedProcess(executable, argv, requestBytes, { spawn, timeoutMs }) {
  return new Promise((resolvePromise, rejectPromise) => {
    let child
    try {
      child = spawn(executable, argv, {
        shell: false,
        stdio: ['pipe', 'pipe', 'pipe'],
        cwd: '/',
        env: {
          PATH: '/usr/bin:/bin', HOME: '/var/empty', TMPDIR: '/private/tmp',
          LANG: 'C', LC_ALL: 'C', CHORA_NETWORK: 'disabled',
        },
      })
    } catch {
      rejectPromise(new Error('owned-path controller could not start'))
      return
    }
    let stdoutBytes = 0
    let stderrBytes = 0
    let failure
    let settled = false
    const timeout = setTimeout(() => {
      failure ??= new Error('owned-path controller timed out')
      child.kill('SIGKILL')
    }, timeoutMs)
    const failOutput = (stream, chunk) => {
      if (stream === 'stdout') stdoutBytes += chunk.length
      else stderrBytes += chunk.length
      if (!failure && (stdoutBytes > MAX_OUTPUT_BYTES || stderrBytes > MAX_OUTPUT_BYTES)) {
        failure = new Error('owned-path controller output exceeded its bound')
        child.kill('SIGKILL')
      }
    }
    child.stdout.on('data', (chunk) => failOutput('stdout', chunk))
    child.stderr.on('data', (chunk) => failOutput('stderr', chunk))
    child.stdin.once('error', () => {
      failure ??= new Error('owned-path controller request input failed')
      child.kill('SIGKILL')
    })
    child.once('error', () => {
      failure ??= new Error('owned-path controller could not start')
    })
    child.once('close', (code, signal) => {
      if (settled) return
      settled = true
      clearTimeout(timeout)
      if (failure !== undefined) return rejectPromise(failure)
      if (code !== 0 || signal !== null) {
        return rejectPromise(new Error('owned-path controller failed'))
      }
      if (stdoutBytes !== 0 || stderrBytes !== 0) {
        return rejectPromise(new Error('owned-path controller emitted forbidden output'))
      }
      resolvePromise()
    })
    child.stdin.end(requestBytes)
  })
}

function runBoundedReceiptProcess(executable, argv, requestBytes, { spawn, timeoutMs }) {
  return new Promise((resolvePromise, rejectPromise) => {
    let child
    try {
      child = spawn(executable, argv, {
        shell: false,
        stdio: ['pipe', 'pipe', 'pipe'],
        cwd: '/',
        env: {
          PATH: '/usr/bin:/bin', HOME: '/var/empty', TMPDIR: '/private/tmp',
          LANG: 'C', LC_ALL: 'C', CHORA_NETWORK: 'disabled',
        },
      })
    } catch {
      rejectPromise(new Error('owned readonly controller could not start'))
      return
    }
    const stdout = []
    let stdoutBytes = 0
    let stderrBytes = 0
    let failure
    let settled = false
    const timeout = setTimeout(() => {
      failure ??= new Error('owned readonly controller timed out')
      child.kill('SIGKILL')
    }, timeoutMs)
    child.stdout.on('data', (chunk) => {
      stdoutBytes += chunk.length
      if (!failure && stdoutBytes > MAX_READONLY_RECEIPT_BYTES) {
        failure = new Error('owned readonly controller receipt exceeded its bound')
        child.kill('SIGKILL')
        return
      }
      stdout.push(chunk)
    })
    child.stderr.on('data', (chunk) => {
      stderrBytes += chunk.length
      if (!failure && stderrBytes > MAX_OUTPUT_BYTES) {
        failure = new Error('owned readonly controller error output exceeded its bound')
        child.kill('SIGKILL')
      }
    })
    child.stdin.once('error', () => {
      failure ??= new Error('owned readonly controller input failed')
      child.kill('SIGKILL')
    })
    child.once('error', () => {
      failure ??= new Error('owned readonly controller could not start')
    })
    child.once('close', (code, signal) => {
      if (settled) return
      settled = true
      clearTimeout(timeout)
      if (failure !== undefined) return rejectPromise(failure)
      if (code !== 0 || signal !== null) {
        return rejectPromise(new Error('owned readonly controller failed'))
      }
      if (stderrBytes !== 0 || stdoutBytes === 0) {
        return rejectPromise(new Error('owned readonly controller output contract drifted'))
      }
      resolvePromise(Buffer.concat(stdout, stdoutBytes))
    })
    child.stdin.end(requestBytes)
  })
}

function parseReadonlyWriteReceipt(bytes, expected) {
  assert(Buffer.isBuffer(bytes) && bytes.length > 1 &&
    bytes.length <= MAX_READONLY_RECEIPT_BYTES && bytes.at(-1) === 0x0a &&
    bytes.indexOf(0x0a) === bytes.length - 1 && !bytes.includes(0x0d),
  'owned readonly controller receipt framing drifted')
  let receipt
  try { receipt = JSON.parse(bytes.subarray(0, -1).toString('utf8')) } catch {
    throw new Error('owned readonly controller receipt is not JSON')
  }
  plain(receipt, 'owned readonly controller receipt')
  exactKeys(receipt, [
    'schemaVersion', 'status', 'target', 'sha256', 'byteLength', 'identity',
  ], 'owned readonly controller receipt')
  assert(receipt.schemaVersion === READONLY_WRITE_RECEIPT_SCHEMA &&
    receipt.status === 'passed' && receipt.target === expected.target &&
    receipt.sha256 === expected.expectedSha256 &&
    receipt.byteLength === expected.expectedByteLength,
  'owned readonly controller receipt binding drifted')
  const identity = validateIdentity(receipt.identity, 'owned readonly controller identity')
  validateDispositionIdentity('regular_file', identity)
  assert((identity.mode & 0o777) === 0o400 &&
    (typeof process.getuid !== 'function' || identity.uid === process.getuid()),
  'owned readonly controller identity is not owner-readonly')
  return deepFreeze({ ...receipt, identity })
}

function parseSingleJSONReceipt(bytes, label) {
  assert(Buffer.isBuffer(bytes) && bytes.length > 1 &&
    bytes.length <= MAX_READONLY_RECEIPT_BYTES && bytes.at(-1) === 0x0a &&
    bytes.indexOf(0x0a) === bytes.length - 1 && !bytes.includes(0x0d),
  `${label} framing drifted`)
  try { return JSON.parse(bytes.subarray(0, -1).toString('utf8')) }
  catch { throw new Error(`${label} is not JSON`) }
}

async function verifyReadonlyTarget(receipt) {
  const pathInfo = await lstat(receipt.target, { bigint: true })
  assert(pathInfo.isFile() && !pathInfo.isSymbolicLink() && pathInfo.nlink === 1n &&
    pathInfo.size === BigInt(receipt.byteLength) &&
    sameOwnedIdentity(receipt.identity, pathInfo) &&
    Number(pathInfo.mode & 0o777n) === 0o400,
  'owned readonly target identity drifted after controller')
  const before = stableIdentity(pathInfo)
  const handle = await open(receipt.target, SAFE_READ_FLAGS)
  try {
    const opened = await handle.stat({ bigint: true })
    assert(sameStableIdentity(before, opened),
      'owned readonly target changed before verification open')
    const actualDigest = await digestHandle(handle, opened.size)
    const after = await handle.stat({ bigint: true })
    assert(actualDigest === receipt.sha256 && sameStableIdentity(opened, after),
      'owned readonly target bytes changed during verification')
  } finally {
    await handle.close()
  }
  const finalInfo = await lstat(receipt.target, { bigint: true })
  assert(sameStableIdentity(before, finalInfo),
    'owned readonly target path changed after verification')
}

async function assertOptionalOwnedPath(path, identity, disposition, label) {
  try {
    await assertOwnedPath(path, identity, disposition, label)
  } catch (error) {
    if (error?.code !== 'ENOENT') throw error
  }
}

async function assertOwnedPath(path, identity, disposition, label) {
  const info = await lstat(path, { bigint: true })
  assert(!info.isSymbolicLink(), `${label} is a symbolic link`)
  assert(sameOwnedIdentity(identity, info), `${label} identity drifted`)
  if (disposition === 'regular_file') {
    assert(info.isFile() && info.nlink === 1n, `${label} is not a singly-linked regular file`)
  } else if (disposition === 'unix_socket') {
    assert(info.isSocket(), `${label} is not a Unix socket`)
  } else {
    assert(info.isDirectory(), `${label} is not a directory`)
    if (disposition === 'empty_directory') {
      assert((await readdir(path)).length === 0, `${label} is not empty`)
    }
  }
}

async function assertAbsent(path, label) {
  try {
    await lstat(path, { bigint: true })
  } catch (error) {
    if (error?.code === 'ENOENT') return
    throw error
  }
  throw new Error(`${label} is not absent`)
}

function validateIdentity(value, label) {
  plain(value, label)
  exactKeys(value, ['mode', 'uid', 'gid', 'dev', 'ino'], label)
  assert(Number.isSafeInteger(value.mode) && value.mode >= 0 && value.mode <= 0xffff_ffff &&
    Number.isSafeInteger(value.uid) && value.uid >= 0 &&
    Number.isSafeInteger(value.gid) && value.gid >= 0 &&
    decimalRE.test(value.dev) && decimalRE.test(value.ino), `${label} is invalid`)
  return Object.freeze({ mode: value.mode, uid: value.uid, gid: value.gid, dev: value.dev, ino: value.ino })
}

function validateDispositionIdentity(disposition, identity) {
  const type = identity.mode & fsConstants.S_IFMT
  if (disposition === 'regular_file') {
    assert(type === fsConstants.S_IFREG, 'owned path disposition/type mismatch')
  } else if (disposition === 'unix_socket') {
    assert(type === fsConstants.S_IFSOCK, 'owned path disposition/type mismatch')
  } else {
    assert(type === fsConstants.S_IFDIR, 'owned path disposition/type mismatch')
  }
}

function exactDisposition(value) {
  assert(dispositions.has(value), 'owned path disposition is invalid')
  return value
}

function assertSafeExecutable(info, allowUnlinked = false) {
  assert(info.isFile() && !info.isSymbolicLink() &&
    (info.nlink === 1n || (allowUnlinked && info.nlink === 0n)) &&
    info.uid === BigInt(process.getuid()) && Number(info.mode & 0o777n) === 0o500,
  'owned-path controller executable is not an owner-0500 singly-linked regular file')
}

function stableIdentity(info) {
  return {
    mode: info.mode, uid: info.uid, gid: info.gid, dev: info.dev, ino: info.ino,
    nlink: info.nlink, size: info.size, mtimeNs: info.mtimeNs, ctimeNs: info.ctimeNs,
  }
}

function sameOwnedIdentity(identity, info) {
  return identity.mode === Number(info.mode) && identity.uid === Number(info.uid) &&
    identity.gid === Number(info.gid) && identity.dev === info.dev.toString() &&
    identity.ino === info.ino.toString()
}

function sameStableIdentity(left, right) {
  return left.mode === right.mode && left.uid === right.uid && left.gid === right.gid &&
    left.dev === right.dev && left.ino === right.ino && left.nlink === right.nlink &&
    left.size === right.size && left.mtimeNs === right.mtimeNs && left.ctimeNs === right.ctimeNs
}

function sameExecutableObject(left, right) {
  return left.mode === right.mode && left.uid === right.uid && left.gid === right.gid &&
    left.dev === right.dev && left.ino === right.ino && left.size === right.size &&
    left.mtimeNs === right.mtimeNs
}

function pathContains(parent, child) {
  if (typeof parent !== 'string' || typeof child !== 'string' || !isAbsolute(parent) || !isAbsolute(child)) {
    return false
  }
  const suffix = relative(parent, child)
  return suffix === '' || (suffix !== '..' && !suffix.startsWith(`..${sep}`) && !isAbsolute(suffix))
}

function exactAbsolute(value, label) {
  assert(typeof value === 'string' && isAbsolute(value) && resolve(value) === value &&
    !value.includes('\0') && !value.endsWith(sep), `${label} is not exact absolute`)
  return value
}

function canonicalJSONStringify(value) { return JSON.stringify(sortKeys(value)) }
function sortKeys(value) { if (Array.isArray(value)) return value.map(sortKeys); if (value && typeof value === 'object') return Object.fromEntries(Object.keys(value).sort().map((key) => [key, sortKeys(value[key])])); return value }
function deepFreeze(value) { if (!value || typeof value !== 'object' || Object.isFrozen(value)) return value; for (const item of Object.values(value)) deepFreeze(item); return Object.freeze(value) }
function plain(value, label) { assert(value && typeof value === 'object' && !Array.isArray(value), `${label} is not an object`) }
function exactKeys(value, keys, label) { assert(JSON.stringify(Object.keys(value).sort()) === JSON.stringify([...keys].sort()), `${label} keys drifted`) }
function assert(condition, message) { if (!condition) throw new Error(message) }
