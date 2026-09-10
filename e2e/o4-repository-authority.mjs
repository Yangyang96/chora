import { createHash } from 'node:crypto'
import { spawn } from 'node:child_process'
import { constants as fsConstants } from 'node:fs'
import { lstat, mkdtemp, open, realpath } from 'node:fs/promises'
import { dirname, isAbsolute, join, resolve, sep } from 'node:path'

import { cleanupOwnedPath, ownedPathIdentity } from './o4-owned-path-cleanup.mjs'

const MAX_ENTRIES = 50_000
const MAX_TOTAL_BYTES = 512 * 1024 * 1024
const MAX_FILE_BYTES = 128 * 1024 * 1024
const MAX_PATH_BYTES = 4 * 1024 * 1024
const MAX_PATH_DEPTH = 64
const MAX_GIT_OUTPUT = 32 * 1024 * 1024
const MAX_SNAPSHOT_BYTES = 1024 * 1024
const MAX_SNAPSHOT_ENTRIES = 10_000
const MAX_LINKED_WORKTREES = 256
const MAX_LINKED_WORKTREE_ENTRIES = 20_000
const MAX_CAPTURE_OUTPUT = 64 * 1024
const MAX_REPOSITORY_PROTOCOL_OUTPUT = 32 * 1024 * 1024
const MAX_REPOSITORY_READ_FILES = 256
const MAX_REPOSITORY_READ_PATH_BYTES = 1024 * 1024
const MAX_REPOSITORY_READ_FILE_BYTES = 8 * 1024 * 1024
const MAX_REPOSITORY_READ_TOTAL_BYTES = 23 * 1024 * 1024
const CAPTURE_TIMEOUT_MS = 120_000
const CAPTURE_PROTOCOL = 'chora.m1-o4-repository-capture.v1'
const CAPTURE_RECEIPT_SCHEMA = 'chora.m1-o4-repository-capture-receipt.v1'
const REPOSITORY_SCAN_PROTOCOL = 'chora.m1-o4-repository-scan.v1'
const REPOSITORY_SCAN_REQUEST_SCHEMA = 'chora.m1-o4-repository-scan-request.v1'
const REPOSITORY_SCAN_RESPONSE_SCHEMA = 'chora.m1-o4-repository-scan-manifest.v1'
const REPOSITORY_READ_PROTOCOL = 'chora.m1-o4-repository-read-files.v1'
const REPOSITORY_READ_REQUEST_SCHEMA = 'chora.m1-o4-repository-read-files-request.v1'
const REPOSITORY_READ_RESPONSE_SCHEMA = 'chora.m1-o4-repository-read-files-response.v1'
const SNAPSHOT_SCHEMA = 'chora.m1-o4-repository-authority.v2'
const SAFE_READ_FLAGS = fsConstants.O_RDONLY | (fsConstants.O_NOFOLLOW ?? 0) |
  (fsConstants.O_NONBLOCK ?? 0) | (fsConstants.O_NOCTTY ?? 0)
const digestRE = /^[0-9a-f]{64}$/
const commitRE = /^[0-9a-f]{40}$/
const uuidV7 = '[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}'
const taskWorktreeRE = new RegExp(`^chora-task_${uuidV7}-([a-z0-9]+(?:-[a-z0-9]+)*)$`)

export async function inspectRepositoryAuthority({
  repositoryRoot, expectedCommit, expectedClosureSha256, capture,
} = {}) {
  const root = exactAbsolute(repositoryRoot, 'repository root')
  optionalCommit(expectedCommit)
  optionalDigest(expectedClosureSha256)
  validateCaptureCapability(capture)
  const first = await inspectOnce(root, capture)
  validateRepositoryAuthoritySnapshot(first.snapshot, {
    repositoryRoot: root, repositoryCommit: first.repositoryCommit,
    repositoryClosureSha256: first.repositoryClosureSha256,
  })
  const second = await inspectOnce(root, capture)
  validateRepositoryAuthoritySnapshot(second.snapshot, {
    repositoryRoot: root, repositoryCommit: second.repositoryCommit,
    repositoryClosureSha256: second.repositoryClosureSha256,
  })
  assert(sameSnapshot(first.snapshot, second.snapshot), 'repository changed during authority inspection')
  assert(first.repositoryCommit === second.repositoryCommit &&
    first.repositoryClosureSha256 === second.repositoryClosureSha256,
  'repository authority changed during inspection')
  if (expectedCommit !== undefined) assert(second.repositoryCommit === expectedCommit,
    'repository commit does not match expected authority')
  if (expectedClosureSha256 !== undefined) assert(second.repositoryClosureSha256 === expectedClosureSha256,
    'repository closure does not match expected authority')
  return deepFreeze(second)
}

export async function verifyRepositoryAuthority({
  repositoryRoot, repositoryCommit, repositoryClosureSha256, snapshot, capture,
} = {}) {
  const root = exactAbsolute(repositoryRoot, 'repository root')
  commit(repositoryCommit, 'repository commit')
  digest(repositoryClosureSha256, 'repository closure')
  validateRepositoryAuthoritySnapshot(snapshot, {
    repositoryRoot: root, repositoryCommit, repositoryClosureSha256,
  })
  const inspected = await inspectRepositoryAuthority({
    repositoryRoot: root, expectedCommit: repositoryCommit,
    expectedClosureSha256: repositoryClosureSha256, capture,
  })
  assert(sameSnapshot(snapshot, inspected.snapshot), 'repository identity snapshot drifted')
  return inspected
}

export function validateRepositoryAuthoritySnapshot(snapshot, expected = undefined) {
  plain(snapshot, 'repository authority snapshot')
  exactKeys(snapshot, ['schemaVersion', 'repositoryRoot', 'repositoryCommit', 'headRef',
    'root', 'gitRoot', 'configSha256', 'index', 'refs', 'worktree',
    'repositoryClosureSha256'],
  'repository authority snapshot')
  assert(snapshot.schemaVersion === SNAPSHOT_SCHEMA,
    'repository snapshot schema drifted')
  exactAbsolute(snapshot.repositoryRoot, 'repository snapshot root')
  commit(snapshot.repositoryCommit, 'repository snapshot commit')
  digest(snapshot.repositoryClosureSha256, 'repository snapshot closure')
  assert(/^refs\/heads\/[A-Za-z0-9][A-Za-z0-9._\/-]*$/.test(snapshot.headRef),
    'repository snapshot HEAD ref is invalid')
  validateStableIdentity(snapshot.root, snapshot.repositoryRoot, 'repository root snapshot')
  validateStableIdentity(snapshot.gitRoot, join(snapshot.repositoryRoot, '.git'),
    'repository Git-root snapshot')
  digest(snapshot.configSha256, 'repository config digest')
  validateIndexEntries(snapshot.index)
  validateRefs(snapshot.refs, snapshot.headRef, snapshot.repositoryCommit)
  validateSemanticEntries(snapshot.worktree, 'repository worktree snapshot')
  assert(snapshot.worktree.length + snapshot.index.length + snapshot.refs.length <= MAX_SNAPSHOT_ENTRIES &&
    Buffer.byteLength(canonicalJSONStringify(snapshot)) <= MAX_SNAPSHOT_BYTES,
  'repository authority snapshot exceeds its tuple bound')
  const { repositoryClosureSha256, ...closure } = snapshot
  assert(repositoryClosureSha256 === sha256(canonicalJSONStringify(closure)),
    'repository authority snapshot digest drifted')
  if (expected !== undefined) {
    plain(expected, 'expected repository authority')
    exactKeys(expected, ['repositoryRoot', 'repositoryCommit', 'repositoryClosureSha256'],
      'expected repository authority')
    assert(snapshot.repositoryRoot === expected.repositoryRoot &&
      snapshot.repositoryCommit === expected.repositoryCommit &&
      snapshot.repositoryClosureSha256 === expected.repositoryClosureSha256,
    'repository snapshot/config authority drifted')
  }
  return deepFreeze(snapshot)
}

export async function verifyRestartRepositoryAuthority({
  repositoryRoot, repositoryCommit, repositoryClosureSha256, snapshot, capture,
} = {}) {
  const root = exactAbsolute(repositoryRoot, 'restart repository root')
  commit(repositoryCommit, 'restart repository commit')
  digest(repositoryClosureSha256, 'restart repository closure')
  validateRepositoryAuthoritySnapshot(snapshot, {
    repositoryRoot: root, repositoryCommit, repositoryClosureSha256,
  })
  validateCaptureCapability(capture)
  const inspected = await inspectRepositoryAuthorityForRestart(root, capture)
  assert(sameSnapshot(snapshot, inspected.snapshot), 'restart repository semantic authority drifted')
  assert(inspected.repositoryCommit === repositoryCommit &&
    inspected.repositoryClosureSha256 === repositoryClosureSha256,
  'restart repository authority changed')
  return deepFreeze({ repositoryRoot: root, repositoryCommit, repositoryClosureSha256,
    linkedWorktreeCount: inspected.linkedWorktreeCount })
}

async function inspectRepositoryAuthorityForRestart(root, capture) {
  const first = await inspectRestartOnce(root, capture)
  const second = await inspectRestartOnce(root, capture)
  assert(sameSnapshot(first.snapshot, second.snapshot),
    'restart repository changed during authority inspection')
  assert(first.linkedWorktreeCount === second.linkedWorktreeCount,
    'restart linked worktree cardinality changed during authority inspection')
  return second
}

async function inspectRestartOnce(sourceRoot, capture) {
  return withCapturedRepository(sourceRoot, capture, async (captured) => {
    const authority = await inspectCapturedOnce(sourceRoot, captured,
      { allowLinkedWorktrees: true })
    const linkedWorktreeCount = await validateLinkedWorktrees(
      sourceRoot, authority.repositoryCommit, authority.captureManifest, captured, capture)
    const { captureManifest, ...publicAuthority } = authority
    return deepFreeze({ ...publicAuthority, linkedWorktreeCount })
  })
}

async function inspectOnce(sourceRoot, capture, options = {}) {
  return withCapturedRepository(sourceRoot, capture, async (captured) => {
    const { captureManifest, ...authority } = await inspectCapturedOnce(
      sourceRoot, captured, options)
    return deepFreeze(authority)
  })
}

async function inspectCapturedOnce(sourceRoot, captured, { allowLinkedWorktrees = false } = {}) {
  const { captureRoot: root, receipt } = captured
  const beforeManifest = await scanCapturedRepository(captured, captured.capture)
  const gitRoot = join(root, '.git')
  const gitRootEntry = manifestEntry(beforeManifest, '.git', 'repository .git root')
  assert(gitRootEntry.kind === 'directory', 'repository .git root is not a directory')
  validateGitTopologyFromManifest(beforeManifest, { allowLinkedWorktrees })
  const controlBytes = (await readCapturedFiles(
    captured, captured.capture, beforeManifest, ['.git/config'])).get('.git/config')
  validateRawConfig(controlBytes)
  const topology = gitLines(await git(root, [
    'rev-parse', '--absolute-git-dir', '--git-common-dir', '--show-toplevel', '--is-bare-repository',
  ]), 'repository topology')
  assert(topology.length === 4 && topology[0] === gitRoot &&
    resolve(root, topology[1]) === gitRoot && topology[2] === root && topology[3] === 'false',
  'repository Git/common/worktree/bare topology drifted')
  const head = gitLines(await git(root, ['rev-parse', '--verify', 'HEAD']), 'repository HEAD')
  const symbolicHead = gitLines(await git(root, ['symbolic-ref', '-q', 'HEAD']), 'repository HEAD ref')
  assert(head.length === 1 && symbolicHead.length === 1, 'repository HEAD output is invalid')
  const [repositoryCommit] = head; const [symbolic] = symbolicHead
  commit(repositoryCommit, 'repository HEAD')
  assert(/^refs\/heads\/[A-Za-z0-9][A-Za-z0-9._\/-]*$/.test(symbolic) &&
    !symbolic.split('/').includes('..'), 'repository HEAD is detached or unsafe')

  const index = parseIndex(await git(root, ['ls-files', '-v', '--stage', '-z']))
  assert(index.length > 0, 'repository has no tracked files')
  const tree = parseTree(await git(root, ['ls-tree', '-rz', '--full-tree', repositoryCommit]))
  assert(index.length === tree.length,
    'repository index/tree cardinality drifted')
  const trackedPaths = new Set()
  const worktreeEntries = []
  for (let indexPosition = 0; indexPosition < tree.length; indexPosition += 1) {
    const indexEntry = index[indexPosition]
    const treeEntry = tree[indexPosition]
    assert(indexEntry.path === treeEntry.path && indexEntry.mode === treeEntry.mode &&
      indexEntry.object === treeEntry.object && indexEntry.stage === 0 && treeEntry.type === 'blob',
    'repository index does not exactly match the pinned HEAD tree')
    assert(indexEntry.flag === 'H', 'repository index contains assume-unchanged or skip-worktree flags')
    assert(treeEntry.mode === '100644' || treeEntry.mode === '100755',
      'repository contains a symlink, gitlink, or unsupported tracked mode')
    assert(treeEntry.path !== '.gitmodules', 'repository .gitmodules topology is forbidden')
    trackedPaths.add(treeEntry.path)
    const file = manifestEntry(beforeManifest, treeEntry.path,
      `tracked repository file ${treeEntry.path}`)
    assert(file.kind === 'regular_file', `tracked repository file is not regular: ${treeEntry.path}`)
    const expectedMode = treeEntry.mode === '100755' ? 0o755 : 0o644
    assert((file.mode & 0o777) === expectedMode,
      `tracked repository file ${treeEntry.path} mode drifted from the Git tree`)
    assert(file.gitBlobSha1 === treeEntry.object,
      `tracked repository bytes drifted from HEAD: ${treeEntry.path}`)
    worktreeEntries.push(manifestSemanticEntry(file))
  }

  const worktreeClosure = beforeManifest.entries.filter((entry) =>
    entry.path !== '.git' && !entry.path.startsWith('.git/')).map(manifestSemanticEntry)
  const worktreeFiles = worktreeClosure.filter((entry) => entry.type === 'file')
  const expectedDirectories = new Set()
  for (const path of trackedPaths) {
    const parts = path.split('/')
    for (let index = 1; index < parts.length; index += 1) expectedDirectories.add(parts.slice(0, index).join('/'))
  }
  const actualDirectories = worktreeClosure.filter((entry) => entry.type === 'directory')
    .map((entry) => entry.path)
  assert(worktreeFiles.length === worktreeEntries.length &&
    worktreeFiles.every((entry, position) => sameSemanticEntry(entry, worktreeEntries[position])) &&
    actualDirectories.length === expectedDirectories.size &&
    actualDirectories.every((path) => expectedDirectories.has(path)),
  'repository worktree closure contains an untracked or unstable entry')
  const refs = parseRefs(await git(root, ['for-each-ref', '--format=%(refname) %(objectname)']))
  validateRefs(refs, symbolic, repositoryCommit)
  assert(!refs.some((entry) => entry.name.startsWith('refs/replace/')),
    'repository contains replacement refs')
  await git(root, ['fsck', '--strict', '--full', '--no-dangling', '--no-reflogs'])
  const afterManifest = await scanCapturedRepository(captured, captured.capture)
  assert(sameProtocolIdentity(beforeManifest.rootIdentity, afterManifest.rootIdentity) &&
    beforeManifest.manifestDigest === afterManifest.manifestDigest,
  'repository capture changed during semantic Git inspection')
  const closure = {
    schemaVersion: SNAPSHOT_SCHEMA, repositoryRoot: sourceRoot,
    repositoryCommit, headRef: symbolic,
    root: receiptIdentity(sourceRoot, receipt.sourceRootIdentity),
    gitRoot: receiptIdentity(join(sourceRoot, '.git'), receipt.sourceGitRootIdentity),
    configSha256: sha256(controlBytes), index, refs,
    worktree: worktreeClosure,
  }
  const repositoryClosureSha256 = sha256(canonicalJSONStringify(closure))
  const snapshot = deepFreeze({ ...closure, repositoryClosureSha256 })
  return deepFreeze({
    repositoryRoot: sourceRoot, repositoryCommit, repositoryClosureSha256, snapshot,
    captureManifest: afterManifest,
  })
}

async function withCapturedRepository(sourceRoot, capture, operation) {
  let captured
  let operationError
  try {
    captured = await createRepositoryCapture(sourceRoot, capture)
    return await operation(captured)
  } catch (error) {
    operationError = error
    throw error
  } finally {
    if (captured?.sessionRoot !== undefined) {
      try {
        await cleanupCaptureSession(captured.sessionRoot, captured.sessionIdentity, capture)
      } catch (cleanupError) {
        if (operationError !== undefined) {
          throw new AggregateError([operationError, cleanupError],
            'repository capture operation and cleanup both failed')
        }
        throw cleanupError
      }
    }
  }
}

async function createRepositoryCapture(sourceRoot, capture) {
  validateCaptureCapability(capture)
  const executable = await stableFile(capture.executableFile, 'repository capture executable', 0o500)
  assert(sha256(executable.bytes) === capture.executableSha256,
    'repository capture executable digest drifted')
  const sourceRootBefore = await safeDirectory(sourceRoot, 'repository capture source root')
  assert((await realpath(sourceRoot)) === sourceRoot,
    'repository capture source root is not canonical')
  const sourceGitPath = join(sourceRoot, '.git')
  const sourceGitBefore = await safeCaptureGitEntry(sourceGitPath,
    'repository capture source .git')
  let sessionRoot
  let sessionIdentity
  try {
    sessionRoot = await realpath(await mkdtemp('/private/tmp/chora-o4-repository-capture-'))
    assert(dirname(sessionRoot) === '/private/tmp',
      'repository capture session escaped its private parent')
    const sessionStats = await lstat(sessionRoot, { bigint: true })
    assert(sessionStats.isDirectory() && !sessionStats.isSymbolicLink() &&
      sessionStats.uid === BigInt(process.getuid()) &&
      Number(sessionStats.mode & 0o777n) === 0o700,
    'repository capture session is not an owner-0700 directory')
    sessionIdentity = ownedPathIdentity(sessionStats)
    const captureRoot = join(sessionRoot, 'capture')
    const receiptPath = join(sessionRoot, 'receipt.json')
    await mustBeAbsent(captureRoot, 'repository capture output before controller execution')
    await mustBeAbsent(receiptPath, 'repository capture receipt before controller execution')
    await runCaptureController(capture, sourceRoot, captureRoot, receiptPath, sessionRoot)

    const executableAfter = await lstat(capture.executableFile, { bigint: true })
    assert(sameIdentity(executable.info, executableAfter),
      'repository capture executable identity changed during execution')
    const sourceRootAfter = await lstat(sourceRoot, { bigint: true })
    const sourceGitAfter = await lstat(sourceGitPath, { bigint: true })
    assert(sameIdentity(sourceRootBefore, sourceRootAfter) &&
      sameIdentity(sourceGitBefore, sourceGitAfter),
    'repository capture source identity changed during execution')

    const receiptFile = await stableFile(receiptPath, 'repository capture receipt', 0o400)
    assert(receiptFile.bytes.length > 0 && receiptFile.bytes.length <= 64 * 1024,
      'repository capture receipt exceeds its byte bound')
    const receipt = parseCaptureReceipt(receiptFile.bytes, {
      sourceRoot, captureRoot, sourceRootInfo: sourceRootAfter, sourceGitInfo: sourceGitAfter,
    })
    const capturedRootInfo = await safeDirectory(captureRoot, 'captured repository root')
    const rootIdentity = ownedPathIdentity(capturedRootInfo)
    return {
      sessionRoot, sessionIdentity, captureRoot, receipt, rootIdentity,
      controllerInfo: executable.info, capture,
    }
  } catch (operationError) {
    if (sessionRoot === undefined || sessionIdentity === undefined) throw operationError
    try {
      await cleanupCaptureSession(sessionRoot, sessionIdentity, capture)
    } catch (cleanupError) {
      throw new AggregateError([operationError, cleanupError],
        'repository capture creation and cleanup both failed')
    }
    throw operationError
  }
}

function validateCaptureCapability(capture) {
  plain(capture, 'repository capture capability')
  exactKeys(capture, ['executableFile', 'executableSha256'], 'repository capture capability')
  exactAbsolute(capture.executableFile, 'repository capture executable')
  digest(capture.executableSha256, 'repository capture executable digest')
  return capture
}

async function safeCaptureGitEntry(path, label) {
  const info = await lstat(path, { bigint: true })
  safeOwnerMode(info, label)
  assert(!info.isSymbolicLink() && (info.isDirectory() || info.isFile()),
    `${label} is not a directory or regular file`)
  if (info.isFile()) assert(info.nlink === 1n, `${label} is hard-linked`)
  return info
}

async function runCaptureController(capture, sourceRoot, captureRoot, receiptPath, sessionRoot) {
  const argv = [
    '--protocol', CAPTURE_PROTOCOL,
    '--source-root', sourceRoot,
    '--capture-root', captureRoot,
    '--receipt', receiptPath,
    '--controller-sha256', capture.executableSha256,
    '--network', 'disabled',
  ]
  await runBoundedProcess(capture.executableFile, argv, {
    timeoutMs: CAPTURE_TIMEOUT_MS,
    maxOutput: MAX_CAPTURE_OUTPUT,
    label: 'repository capture controller',
    env: {
      PATH: '/usr/bin:/bin', HOME: '/var/empty', TMPDIR: sessionRoot,
      LANG: 'C', LC_ALL: 'C', CHORA_NETWORK: 'disabled',
    },
  })
}

function runBoundedProcess(executable, argv, { timeoutMs, maxOutput, label, env }) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(executable, argv, {
      shell: false, stdio: ['ignore', 'pipe', 'pipe'], env,
    })
    let stdoutBytes = 0; let stderrBytes = 0; let failure = null; let settled = false
    const timeout = setTimeout(() => {
      failure = new Error(`${label} timed out`)
      child.kill('SIGKILL')
    }, timeoutMs)
    const collect = (chunk, stream) => {
      if (stream === 'stdout') stdoutBytes += chunk.length; else stderrBytes += chunk.length
      if (!failure && (stdoutBytes > maxOutput || stderrBytes > maxOutput)) {
        failure = new Error(`${label} output exceeded its bound`)
        child.kill('SIGKILL')
      }
    }
    child.stdout.on('data', (chunk) => collect(chunk, 'stdout'))
    child.stderr.on('data', (chunk) => collect(chunk, 'stderr'))
    child.once('error', () => {
      failure = new Error(`${label} could not start`)
    })
    child.once('close', (code, signal) => {
      if (settled) return
      settled = true; clearTimeout(timeout)
      if (failure) return rejectPromise(failure)
      if (code !== 0 || signal !== null) return rejectPromise(new Error(`${label} failed`))
      if (stdoutBytes !== 0 || stderrBytes !== 0) {
        return rejectPromise(new Error(`${label} emitted forbidden output`))
      }
      resolvePromise()
    })
  })
}

function parseCaptureReceipt(bytes, expected) {
  const text = bytes.toString('utf8')
  assert(Buffer.from(text).equals(bytes) && !text.includes('\0'),
    'repository capture receipt is not valid UTF-8 JSON')
  let receipt
  try { receipt = JSON.parse(text) } catch { throw new Error('repository capture receipt is invalid JSON') }
  plain(receipt, 'repository capture receipt')
  exactKeys(receipt, ['schemaVersion', 'status', 'sourceRoot', 'captureRoot',
    'sourceRootIdentity', 'sourceGitRootIdentity', 'entryCount', 'totalBytes', 'networkUsed'],
  'repository capture receipt')
  assert(receipt.schemaVersion === CAPTURE_RECEIPT_SCHEMA && receipt.status === 'passed' &&
    receipt.sourceRoot === expected.sourceRoot && receipt.captureRoot === expected.captureRoot &&
    receipt.networkUsed === false,
  'repository capture receipt binding drifted')
  validateCaptureReceiptIdentity(receipt.sourceRootIdentity, expected.sourceRootInfo,
    'repository capture source-root receipt identity')
  validateCaptureReceiptIdentity(receipt.sourceGitRootIdentity, expected.sourceGitInfo,
    'repository capture source-Git receipt identity')
  assert(Number.isSafeInteger(receipt.entryCount) && receipt.entryCount > 0 &&
    receipt.entryCount <= MAX_ENTRIES && Number.isSafeInteger(receipt.totalBytes) &&
    receipt.totalBytes >= 0 && receipt.totalBytes <= MAX_TOTAL_BYTES,
  'repository capture receipt bounds drifted')
  return deepFreeze(receipt)
}

function validateCaptureReceiptIdentity(value, info, label) {
  plain(value, label)
  exactKeys(value, ['mode', 'uid', 'gid', 'dev', 'ino'], label)
  assert(Number.isSafeInteger(value.mode) && value.mode === Number(info.mode & 0o7777n) &&
    Number.isSafeInteger(value.uid) && value.uid === Number(info.uid) &&
    Number.isSafeInteger(value.gid) && value.gid === Number(info.gid) &&
    value.dev === info.dev.toString() && value.ino === info.ino.toString(),
  `${label} drifted`)
}

function receiptIdentity(path, value) {
  return {
    path, type: 'directory', mode: value.mode & 0o777, uid: value.uid, gid: value.gid,
    dev: value.dev, ino: value.ino,
  }
}

async function cleanupCaptureSession(sessionRoot, identity, capture) {
  await cleanupOwnedPath({
    capability: capture,
    path: sessionRoot,
    identity,
    disposition: 'recursive_directory',
  })
}

async function scanCapturedRepository(captured, capture) {
  const request = {
    schemaVersion: REPOSITORY_SCAN_REQUEST_SCHEMA,
    action: 'scan_repository_capture',
    captureRoot: captured.captureRoot,
    expectedRootIdentity: captured.rootIdentity,
  }
  const bytes = await invokeRepositoryProtocol(
    captured, capture, REPOSITORY_SCAN_PROTOCOL, request, 'repository native scan')
  return validateRepositoryScanResponse(bytes, captured.captureRoot, captured.rootIdentity)
}

async function readCapturedFiles(captured, capture, manifest, paths) {
  assert(Array.isArray(paths) && paths.length > 0 && paths.length <= MAX_REPOSITORY_READ_FILES,
    'repository native read path cardinality is invalid')
  const requested = [...paths].sort(byteSort)
  assert(new Set(requested).size === requested.length &&
    requested.every(safeRelative) &&
    requested.reduce((total, path) => total + Buffer.byteLength(path), 0) <=
      MAX_REPOSITORY_READ_PATH_BYTES,
  'repository native read paths are unsafe, duplicate, or oversized')
  const request = {
    schemaVersion: REPOSITORY_READ_REQUEST_SCHEMA,
    action: 'read_repository_files',
    captureRoot: captured.captureRoot,
    expectedRootIdentity: captured.rootIdentity,
    paths: requested,
  }
  const bytes = await invokeRepositoryProtocol(
    captured, capture, REPOSITORY_READ_PROTOCOL, request, 'repository native read-files')
  return validateRepositoryReadResponse(
    bytes, captured.captureRoot, captured.rootIdentity, manifest, requested)
}

async function invokeRepositoryProtocol(captured, capture, protocol, request, label) {
  await assertCaptureControllerStable(captured, capture, `${label} before execution`)
  let result
  let primary
  try {
    result = await runRepositoryProtocolProcess(capture.executableFile, [
      '--protocol', protocol,
      '--controller-sha256', capture.executableSha256,
      '--network', 'disabled',
      '--request-input', 'stdin',
    ], Buffer.from(JSON.stringify(request)), label)
  } catch (error) {
    primary = error
  }
  try {
    await assertCaptureControllerStable(captured, capture, `${label} after execution`)
  } catch (afterError) {
    if (primary !== undefined) throw new AggregateError([primary, afterError],
      `${label} and controller revalidation both failed`)
    throw afterError
  }
  if (primary !== undefined) throw primary
  return result
}

async function assertCaptureControllerStable(captured, capture, label) {
  const executable = await stableFile(capture.executableFile, label, 0o500)
  assert(sameIdentity(captured.controllerInfo, executable.info) &&
    sha256(executable.bytes) === capture.executableSha256,
  'repository capture executable identity or digest changed during native protocol')
}

function runRepositoryProtocolProcess(executable, argv, request, label) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(executable, argv, {
      shell: false, stdio: ['pipe', 'pipe', 'pipe'], env: {
        PATH: '/usr/bin:/bin', HOME: '/var/empty', TMPDIR: dirname(executable),
        LANG: 'C', LC_ALL: 'C', CHORA_NETWORK: 'disabled',
      },
    })
    const stdout = []; let stdoutBytes = 0; let stderrBytes = 0
    let failure = null; let settled = false
    const timeout = setTimeout(() => {
      failure = new Error(`${label} timed out`)
      child.kill('SIGKILL')
    }, CAPTURE_TIMEOUT_MS)
    child.stdout.on('data', (chunk) => {
      stdoutBytes += chunk.length
      if (!failure && stdoutBytes > MAX_REPOSITORY_PROTOCOL_OUTPUT) {
        failure = new Error(`${label} output exceeded its bound`)
        child.kill('SIGKILL')
      } else if (!failure) stdout.push(chunk)
    })
    child.stderr.on('data', (chunk) => {
      stderrBytes += chunk.length
      if (!failure && stderrBytes > MAX_REPOSITORY_PROTOCOL_OUTPUT) {
        failure = new Error(`${label} error output exceeded its bound`)
        child.kill('SIGKILL')
      }
    })
    child.stdin.once('error', () => {})
    child.once('error', () => { failure = new Error(`${label} could not start`) })
    child.once('close', (code, signal) => {
      if (settled) return
      settled = true; clearTimeout(timeout)
      if (failure) return rejectPromise(failure)
      if (code !== 0 || signal !== null) return rejectPromise(new Error(`${label} failed`))
      if (stderrBytes !== 0) return rejectPromise(new Error(`${label} emitted forbidden error output`))
      resolvePromise(Buffer.concat(stdout))
    })
    child.stdin.end(request)
  })
}

function validateRepositoryScanResponse(bytes, captureRoot, rootIdentity) {
  const value = parseExactProtocolJSON(bytes, 'repository native scan response')
  plain(value, 'repository native scan response')
  exactKeys(value, ['schemaVersion', 'status', 'captureRoot', 'rootIdentity', 'entries',
    'entryCount', 'totalBytes', 'manifestDigest'], 'repository native scan response')
  assert(value.schemaVersion === REPOSITORY_SCAN_RESPONSE_SCHEMA && value.status === 'passed' &&
    value.captureRoot === captureRoot, 'repository native scan response binding drifted')
  validateProtocolIdentity(value.rootIdentity, rootIdentity, 'repository native scan root identity')
  assert(Array.isArray(value.entries) && Number.isSafeInteger(value.entryCount) &&
    value.entryCount === value.entries.length && value.entryCount > 0 &&
    value.entryCount <= MAX_ENTRIES,
  'repository native scan entry count drifted')
  let previous = ''; let pathBytes = 0; let totalBytes = 0
  for (const entry of value.entries) {
    plain(entry, 'repository native scan entry')
    const file = entry.kind === 'regular_file'
    exactKeys(entry, file
      ? ['path', 'kind', 'mode', 'uid', 'gid', 'size', 'sha256', 'gitBlobSha1']
      : ['path', 'kind', 'mode', 'uid', 'gid', 'size'], 'repository native scan entry')
    assert(safeRelative(entry.path) && entry.path.split('/').length <= MAX_PATH_DEPTH + 1 &&
      byteSort(previous, entry.path) < 0,
    'repository native scan paths are unsafe, duplicate, unsorted, or too deep')
    pathBytes += Buffer.byteLength(entry.path)
    assert(pathBytes <= MAX_PATH_BYTES, 'repository native scan path budget drifted')
    assert((file || entry.kind === 'directory') && Number.isSafeInteger(entry.mode) &&
      entry.mode >= 0 && Number.isSafeInteger(entry.uid) && entry.uid === process.getuid() &&
      Number.isSafeInteger(entry.gid) && entry.gid >= 0 && (entry.mode & 0o022) === 0 &&
      Number.isSafeInteger(entry.size) && entry.size >= 0,
    'repository native scan entry scalar fields drifted')
    assert((entry.mode & 0o170000) === (file ? 0o100000 : 0o040000),
      'repository native scan entry kind/mode drifted')
    if (file) {
      assert(entry.size <= MAX_FILE_BYTES, 'repository native scan file byte bound drifted')
      digest(entry.sha256, 'repository native scan file digest')
      assert(commitRE.test(entry.gitBlobSha1), 'repository native scan Git blob digest is invalid')
      totalBytes += entry.size
    }
    previous = entry.path
  }
  assert(Number.isSafeInteger(value.totalBytes) && value.totalBytes === totalBytes &&
    value.totalBytes <= MAX_TOTAL_BYTES,
  'repository native scan total byte count drifted')
  digest(value.manifestDigest, 'repository native scan manifest digest')
  const core = {
    schemaVersion: value.schemaVersion, status: value.status, captureRoot: value.captureRoot,
    rootIdentity: value.rootIdentity, entries: value.entries,
    entryCount: value.entryCount, totalBytes: value.totalBytes,
  }
  assert(sha256(canonicalJSONStringify(core)) === value.manifestDigest,
    'repository native scan manifest digest drifted')
  return deepFreeze(value)
}

function validateRepositoryReadResponse(bytes, captureRoot, rootIdentity, manifest, paths) {
  const value = parseExactProtocolJSON(bytes, 'repository native read-files response')
  plain(value, 'repository native read-files response')
  exactKeys(value, ['schemaVersion', 'status', 'captureRoot', 'rootIdentity', 'files',
    'fileCount', 'totalBytes', 'responseDigest'], 'repository native read-files response')
  assert(value.schemaVersion === REPOSITORY_READ_RESPONSE_SCHEMA && value.status === 'passed' &&
    value.captureRoot === captureRoot, 'repository native read-files response binding drifted')
  validateProtocolIdentity(value.rootIdentity, rootIdentity,
    'repository native read-files root identity')
  assert(Array.isArray(value.files) && value.files.length === paths.length &&
    value.fileCount === value.files.length && Number.isSafeInteger(value.fileCount),
  'repository native read-files count drifted')
  const result = new Map(); let totalBytes = 0
  for (let position = 0; position < paths.length; position += 1) {
    const file = value.files[position]
    plain(file, 'repository native read-files item')
    exactKeys(file, ['path', 'bytesBase64', 'sha256'], 'repository native read-files item')
    assert(file.path === paths[position] && typeof file.bytesBase64 === 'string',
      'repository native read-files order or encoding drifted')
    const decoded = Buffer.from(file.bytesBase64, 'base64')
    assert(decoded.toString('base64') === file.bytesBase64 &&
      decoded.length <= MAX_REPOSITORY_READ_FILE_BYTES,
    'repository native read-files base64 is noncanonical or oversized')
    digest(file.sha256, 'repository native read-files digest')
    const scanned = manifestEntry(manifest, file.path, 'repository native read manifest binding')
    assert(scanned.kind === 'regular_file' && file.sha256 === scanned.sha256 &&
      sha256(decoded) === file.sha256 && decoded.length === scanned.size,
    'repository native read-files content drifted from scan manifest')
    totalBytes += decoded.length
    result.set(file.path, decoded)
  }
  assert(Number.isSafeInteger(value.totalBytes) && value.totalBytes === totalBytes &&
    value.totalBytes <= MAX_REPOSITORY_READ_TOTAL_BYTES,
  'repository native read-files total byte count drifted')
  digest(value.responseDigest, 'repository native read-files response digest')
  const core = {
    schemaVersion: value.schemaVersion, status: value.status, captureRoot: value.captureRoot,
    rootIdentity: value.rootIdentity, files: value.files,
    fileCount: value.fileCount, totalBytes: value.totalBytes,
  }
  assert(sha256(canonicalJSONStringify(core)) === value.responseDigest,
    'repository native read-files response digest drifted')
  return result
}

function parseExactProtocolJSON(bytes, label) {
  assert(Buffer.isBuffer(bytes) && bytes.length > 1 &&
    bytes.length <= MAX_REPOSITORY_PROTOCOL_OUTPUT && bytes.at(-1) === 0x0a,
  `${label} framing is invalid`)
  const body = bytes.subarray(0, -1)
  const text = body.toString('utf8')
  assert(Buffer.from(text).equals(body) && !text.includes('\0') && !text.includes('\n'),
    `${label} is not exact UTF-8 JSON`)
  let value
  try { value = JSON.parse(text) } catch { throw new Error(`${label} is invalid JSON`) }
  assert(Buffer.from(`${canonicalJSONStringify(value)}\n`).equals(bytes),
    `${label} is not canonical JSON`)
  return value
}

function validateProtocolIdentity(value, expected, label) {
  plain(value, label)
  exactKeys(value, ['mode', 'uid', 'gid', 'dev', 'ino'], label)
  assert(Number.isSafeInteger(value.mode) && Number.isSafeInteger(value.uid) &&
    Number.isSafeInteger(value.gid) && typeof value.dev === 'string' &&
    typeof value.ino === 'string' && sameProtocolIdentity(value, expected), `${label} drifted`)
}

function sameProtocolIdentity(left, right) {
  return left.mode === right.mode && left.uid === right.uid && left.gid === right.gid &&
    left.dev === right.dev && left.ino === right.ino
}

function manifestEntry(manifest, path, label) {
  const entry = manifest.entries.find((candidate) => candidate.path === path)
  assert(entry !== undefined, `${label} is absent from the native scan manifest`)
  return entry
}

function manifestSemanticEntry(entry) {
  return {
    path: entry.path, type: entry.kind === 'regular_file' ? 'file' : 'directory',
    mode: entry.mode & 0o777, sha256: entry.kind === 'regular_file' ? entry.sha256 : null,
  }
}

function validateGitTopologyFromManifest(manifest, { allowLinkedWorktrees = false } = {}) {
  const paths = new Set(manifest.entries.map((entry) => entry.path))
  for (const name of ['commondir', 'shallow']) assert(!paths.has(`.git/${name}`),
    `unsupported repository ${name} is forbidden`)
  assert(!paths.has('.git/info/grafts'), 'unsupported repository grafts is forbidden')
  assert(!paths.has('.git/refs/replace'), 'repository replacement topology is forbidden')
  assert(!paths.has('.git/modules'), 'repository submodule topology is forbidden')
  if (!allowLinkedWorktrees) assert(!paths.has('.git/worktrees'),
    'initial repository worktrees topology is forbidden')
  for (const name of ['attributes', 'sparse-checkout']) assert(!paths.has(`.git/info/${name}`),
    `unsupported repository info/${name} is forbidden`)
  for (const name of ['alternates', 'http-alternates']) {
    assert(!paths.has(`.git/objects/info/${name}`), `unsupported repository ${name} is forbidden`)
  }
  assert(!manifest.entries.some((entry) =>
    /^\.git\/objects\/pack\/[^/]+\.promisor$/.test(entry.path)),
  'repository promisor object topology is forbidden')
  assert(!manifest.entries.some((entry) =>
    /^\.git\/hooks\/[^/]+$/.test(entry.path) && !entry.path.endsWith('.sample')),
  'repository active hook topology is forbidden')
}

function validateRawConfig(bytes) {
  const text = bytes.toString('utf8')
  assert(!/\0/.test(text) && Buffer.from(text).equals(bytes), 'repository config is not UTF-8 text')
  let section = null
  const entries = new Map()
  for (const rawLine of text.split('\n')) {
    const line = rawLine.trim()
    if (line === '') continue
    const sectionMatch = /^\[([A-Za-z0-9.-]+)\]$/.exec(line)
    if (sectionMatch) { section = sectionMatch[1].toLowerCase(); continue }
    const keyMatch = /^([A-Za-z0-9.-]+)\s*=\s*(.*?)$/.exec(line)
    assert(section !== null && keyMatch, 'repository config syntax is unsupported')
    const key = `${section}.${keyMatch[1].toLowerCase()}`
    assert(!entries.has(key), 'repository config contains a duplicate key')
    entries.set(key, keyMatch[2].toLowerCase())
  }
  const expected = new Map([
    ['core.repositoryformatversion', '0'], ['core.filemode', 'true'], ['core.bare', 'false'],
    ['core.logallrefupdates', 'true'], ['core.ignorecase', 'true'],
    ['core.precomposeunicode', 'true'],
  ])
  assert(entries.size === expected.size && [...expected].every(([key, value]) => entries.get(key) === value),
    'repository config is not the exact inert generation config')
}

async function git(root, args) {
  const safeArgs = [
    '--no-replace-objects', '-c', 'core.hooksPath=/dev/null', '-c', 'core.fsmonitor=false',
    '-c', 'protocol.allow=never', '-c', 'gc.auto=0', '-C', root, ...args,
  ]
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn('/usr/bin/git', safeArgs, {
      shell: false, stdio: ['ignore', 'pipe', 'pipe'], env: {
        PATH: '/usr/bin:/bin', HOME: '/var/empty', GIT_CONFIG_NOSYSTEM: '1',
        GIT_CONFIG_SYSTEM: '/dev/null', GIT_CONFIG_GLOBAL: '/dev/null',
        GIT_OPTIONAL_LOCKS: '0', GIT_NO_LAZY_FETCH: '1', GIT_TERMINAL_PROMPT: '0',
        GIT_ASKPASS: '/usr/bin/false', SSH_ASKPASS: '/usr/bin/false',
      },
    })
    const stdout = []; const stderr = []; let stdoutBytes = 0; let stderrBytes = 0
    let failure = null; let settled = false
    const timeout = setTimeout(() => {
      failure = new Error('isolated repository Git inspection timed out')
      child.kill('SIGKILL')
    }, 10_000)
    const collect = (chunks, chunk, stream) => {
      if (failure) return
      if (stream === 'stdout') stdoutBytes += chunk.length; else stderrBytes += chunk.length
      if (stdoutBytes > MAX_GIT_OUTPUT || stderrBytes > MAX_GIT_OUTPUT) {
        failure = new Error('isolated repository Git output exceeded its bound')
        child.kill('SIGKILL'); return
      }
      chunks.push(chunk)
    }
    child.stdout.on('data', (chunk) => collect(stdout, chunk, 'stdout'))
    child.stderr.on('data', (chunk) => collect(stderr, chunk, 'stderr'))
    child.once('error', () => { failure = new Error('isolated repository inspection could not start Git') })
    child.once('close', (code, signal) => {
      if (settled) return
      settled = true; clearTimeout(timeout)
      if (failure) return rejectPromise(failure)
      if (code !== 0 || signal !== null) return rejectPromise(new Error('isolated repository Git inspection failed'))
      resolvePromise(Buffer.concat(stdout))
    })
  })
}

async function stableFile(path, label, expectedMode = undefined) {
  const before = await lstat(path, { bigint: true })
  assert(before.isFile() && !before.isSymbolicLink(), `${label} is not a regular file`)
  safeOwnerMode(before, label)
  assert(before.nlink === 1n, `${label} is hard-linked`)
  assert(before.size >= 0n && before.size <= BigInt(MAX_FILE_BYTES), `${label} exceeds its byte bound`)
  if (expectedMode !== undefined) assert(Number(before.mode & 0o777n) === expectedMode,
    `${label} mode drifted from the Git tree`)
  const handle = await open(path, SAFE_READ_FLAGS)
  let bytes; let opened
  try {
    opened = await handle.stat({ bigint: true })
    assert(sameIdentity(before, opened), `${label} identity changed before read`)
    bytes = Buffer.alloc(Number(opened.size))
    let offset = 0
    while (offset < bytes.length) {
      const result = await handle.read(bytes, offset, bytes.length - offset, offset)
      assert(result.bytesRead > 0, `${label} was truncated during read`)
      offset += result.bytesRead
    }
    const afterRead = await handle.stat({ bigint: true })
    assert(sameIdentity(opened, afterRead), `${label} changed during read`)
  } finally { await handle.close() }
  const after = await lstat(path, { bigint: true })
  assert(sameIdentity(opened, after), `${label} path identity changed after read`)
  return { bytes, info: after }
}

async function safeDirectory(path, label) {
  const info = await lstat(path, { bigint: true })
  assert(info.isDirectory() && !info.isSymbolicLink(), `${label} is not a directory`)
  safeOwnerMode(info, label)
  assert((info.mode & 0o100n) !== 0n, `${label} is not owner-traversable`)
  return info
}

function safeOwnerMode(info, label) {
  assert(info.uid === BigInt(process.getuid()), `${label} is not owner-owned`)
  assert((info.mode & 0o022n) === 0n, `${label} is group/world writable`)
}

async function mustBeAbsent(path, label) {
  try { await lstat(path); throw new Error(`${label} is forbidden`) }
  catch (error) { if (error?.code !== 'ENOENT') throw error }
}

async function validateLinkedWorktrees(
  repositoryRoot, repositoryCommit, manifest, captured, capture,
) {
  const sourceGitRoot = join(repositoryRoot, '.git')
  const worktreesEntry = manifest.entries.find((entry) => entry.path === '.git/worktrees')
  const adminNames = manifest.entries.filter((entry) => entry.kind === 'directory' &&
    /^\.git\/worktrees\/[^/]+$/.test(entry.path))
    .map((entry) => entry.path.slice('.git/worktrees/'.length))
  const worktreeEntries = manifest.entries.filter((entry) =>
    entry.path === '.git/worktrees' || entry.path.startsWith('.git/worktrees/'))
  if (worktreesEntry === undefined) {
    assert(worktreeEntries.length === 0, 'linked worktree administration is incomplete')
    return 0
  }
  assert(worktreesEntry.kind === 'directory' && adminNames.length > 0 &&
    adminNames.length <= MAX_LINKED_WORKTREES,
  'linked worktree cardinality is invalid')
  assert(new Set(adminNames).size === adminNames.length,
    'linked worktree administration names are duplicated')
  let totalEntries = 0
  for (const name of adminNames.sort(byteSort)) {
    const match = taskWorktreeRE.exec(name)
    assert(match && Buffer.byteLength(match[1]) <= 64,
      'linked worktree name does not match Chora Task grammar')
    const sourceAdminRoot = join(sourceGitRoot, 'worktrees', name)
    const siblingRoot = join(dirname(repositoryRoot), name)
    assert(dirname(siblingRoot) === dirname(repositoryRoot) && siblingRoot !== repositoryRoot,
      'linked worktree is not a direct repository sibling')
    const adminPrefix = `.git/worktrees/${name}`
    const adminChildren = directManifestChildNames(manifest, adminPrefix)
    assert(canonicalJSONStringify(adminChildren) === canonicalJSONStringify(
      ['HEAD', 'ORIG_HEAD', 'commondir', 'gitdir', 'index', 'logs', 'refs'].sort(byteSort)),
    'linked worktree admin layout drifted')
    assert(directManifestChildNames(manifest, `${adminPrefix}/refs`).length === 0,
      'linked worktree private refs are forbidden')
    const logChildren = directManifestChildNames(manifest, `${adminPrefix}/logs`)
    assert(canonicalJSONStringify(logChildren) === canonicalJSONStringify(['HEAD']),
      'linked worktree log layout drifted')
    assert(manifestEntry(manifest, adminPrefix, `linked worktree admin ${name}`).kind === 'directory' &&
      manifestEntry(manifest, `${adminPrefix}/logs`, `linked worktree logs ${name}`).kind === 'directory' &&
      manifestEntry(manifest, `${adminPrefix}/refs`, `linked worktree refs ${name}`).kind === 'directory',
    'linked worktree administration directory kind drifted')
    const metadataPaths = [
      `${adminPrefix}/HEAD`, `${adminPrefix}/ORIG_HEAD`, `${adminPrefix}/commondir`,
      `${adminPrefix}/gitdir`, `${adminPrefix}/index`, `${adminPrefix}/logs/HEAD`,
    ].sort(byteSort)
    const metadata = await readCapturedFiles(captured, capture, manifest, metadataPaths)
    const expectedCommitBytes = Buffer.from(`${repositoryCommit}\n`)
    for (const file of ['HEAD', 'ORIG_HEAD']) {
      const observed = metadata.get(`${adminPrefix}/${file}`)
      assert(observed.equals(expectedCommitBytes),
        `linked worktree ${file} is not the frozen detached commit`)
    }
    const common = metadata.get(`${adminPrefix}/commondir`)
    assert(common.equals(Buffer.from('../..\n')),
      'linked worktree common directory drifted')
    const adminGitdir = metadata.get(`${adminPrefix}/gitdir`)
    assert(adminGitdir.equals(Buffer.from(`${join(siblingRoot, '.git')}\n`)),
      'linked worktree admin-to-sibling link drifted')
    const index = metadata.get(`${adminPrefix}/index`)
    assert(index.length > 0 && index.length <= MAX_REPOSITORY_READ_FILE_BYTES,
      'linked worktree index size is invalid')
    const log = metadata.get(`${adminPrefix}/logs/HEAD`)
    assert(log.length > 0 && log.length <= 1024 * 1024 &&
      !log.includes(0), 'linked worktree HEAD log is invalid')

    await withCapturedRepository(siblingRoot, capture, async (siblingCaptured) => {
      const before = await scanCapturedRepository(siblingCaptured, capture)
      const siblingGitEntry = manifestEntry(before, '.git',
        `linked worktree captured .git ${name}`)
      assert(siblingGitEntry.kind === 'regular_file',
        'linked worktree captured .git is not a regular pointer')
      const siblingGitdir = (await readCapturedFiles(
        siblingCaptured, capture, before, ['.git'])).get('.git')
      assert(siblingGitdir.equals(Buffer.from(`gitdir: ${sourceAdminRoot}\n`)),
        'linked worktree sibling-to-admin link drifted')
      const after = await scanCapturedRepository(siblingCaptured, capture)
      assert(before.manifestDigest === after.manifestDigest &&
        sameProtocolIdentity(before.rootIdentity, after.rootIdentity),
      'linked worktree capture changed during inspection')
      totalEntries += after.entries.length
      assert(totalEntries <= MAX_LINKED_WORKTREE_ENTRIES,
        'linked worktree content entry bound exceeded')
      assert(after.entries.filter((entry) => entry.path === '.git' &&
        entry.kind === 'regular_file').length === 1 &&
        after.entries.every((entry) => entry.path === '.git' ||
          (entry.path !== '.git' && !entry.path.startsWith('.git/'))),
      'linked worktree contains nested Git control topology')
    })

    const allowedAdminPaths = new Set([
      adminPrefix, `${adminPrefix}/HEAD`, `${adminPrefix}/ORIG_HEAD`,
      `${adminPrefix}/commondir`, `${adminPrefix}/gitdir`, `${adminPrefix}/index`,
      `${adminPrefix}/logs`, `${adminPrefix}/logs/HEAD`, `${adminPrefix}/refs`,
    ])
    assert(worktreeEntries.filter((entry) => entry.path.startsWith(adminPrefix))
      .every((entry) => allowedAdminPaths.has(entry.path)),
    'linked worktree administration contains an unexpected entry')
  }
  assert(worktreeEntries.every((entry) => entry.path === '.git/worktrees' ||
    adminNames.some((name) => entry.path === `.git/worktrees/${name}` ||
      entry.path.startsWith(`.git/worktrees/${name}/`))),
  'linked worktree administration contains an unbound entry')
  return adminNames.length
}

function directManifestChildNames(manifest, parent) {
  const prefix = `${parent}/`
  return manifest.entries.filter((entry) => entry.path.startsWith(prefix) &&
    !entry.path.slice(prefix.length).includes('/'))
    .map((entry) => entry.path.slice(prefix.length)).sort(byteSort)
}

function parseIndex(output) {
  return parseNul(output).map((entry) => {
    const match = /^(.?) (100644|100755|120000|160000) ([0-9a-f]{40}) ([0-3])\t([^\0]+)$/.exec(entry)
    assert(match && safeRelative(match[5]), 'repository index entry is invalid')
    return { flag: match[1], mode: match[2], object: match[3], stage: Number(match[4]), path: match[5] }
  }).sort(pathSort)
}

function parseTree(output) {
  return parseNul(output).map((entry) => {
    const match = /^(100644|100755|120000|160000) (blob|commit) ([0-9a-f]{40})\t([^\0]+)$/.exec(entry)
    assert(match && safeRelative(match[4]), 'repository HEAD tree entry is invalid')
    return { mode: match[1], type: match[2], object: match[3], path: match[4] }
  }).sort(pathSort)
}

function parseRefs(output) {
  const value = output.toString('utf8')
  assert(Buffer.from(value).equals(output) && !value.includes('\0'),
    'repository refs output is invalid')
  if (value === '') return []
  assert(value.endsWith('\n'), 'repository refs output is not newline terminated')
  return value.slice(0, -1).split('\n').map((line) => {
    const match = /^(refs\/[A-Za-z0-9][A-Za-z0-9._\/-]*) ([0-9a-f]{40})$/.exec(line)
    assert(match && !match[1].split('/').includes('..'), 'repository ref entry is invalid')
    return { name: match[1], object: match[2] }
  }).sort((left, right) => byteSort(left.name, right.name))
}

function parseNul(output) {
  assert(Buffer.isBuffer(output), 'repository Git output is invalid')
  if (output.length === 0) return []
  assert(output.at(-1) === 0, 'repository Git output is not NUL terminated')
  const body = output.subarray(0, -1)
  const text = body.toString('utf8')
  assert(Buffer.from(text).equals(body), 'repository Git output is not valid UTF-8')
  return text.split('\0')
}

function gitLines(output, label) {
  const value = output.toString('utf8')
  assert(Buffer.from(value).equals(output) && value.endsWith('\n') && !value.includes('\0'),
    `${label} output is invalid`)
  return value.slice(0, -1).split('\n')
}

function sameSemanticEntry(left, right) {
  return canonicalJSONStringify(left) === canonicalJSONStringify(right)
}
function sameSnapshot(left, right) { return canonicalJSONStringify(left) === canonicalJSONStringify(right) }
function sameIdentity(left, right) { return left.dev === right.dev && left.ino === right.ino && left.mode === right.mode && left.uid === right.uid && left.gid === right.gid && left.nlink === right.nlink && left.size === right.size && left.mtimeNs === right.mtimeNs && left.ctimeNs === right.ctimeNs }
function pathSort(left, right) { return byteSort(left.path, right.path) }
function byteSort(left, right) { return Buffer.compare(Buffer.from(left), Buffer.from(right)) }
function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function canonicalJSONStringify(value) { return JSON.stringify(sortKeys(value)) }
function sortKeys(value) { if (Array.isArray(value)) return value.map(sortKeys); if (value && typeof value === 'object') return Object.fromEntries(Object.keys(value).sort().map((key) => [key, sortKeys(value[key])])); return value }
function deepFreeze(value) { if (!value || typeof value !== 'object' || Object.isFrozen(value)) return value; for (const item of Object.values(value)) deepFreeze(item); return Object.freeze(value) }
function validateStableIdentity(value, path, label) {
  plain(value, label)
  exactKeys(value, ['path', 'type', 'mode', 'uid', 'gid', 'dev', 'ino'], label)
  assert(value.path === path && value.type === 'directory' && isAbsolute(value.path) &&
    Number.isSafeInteger(value.mode) && value.mode >= 0 && value.mode <= 0o777 &&
    Number.isSafeInteger(value.uid) && value.uid === process.getuid() &&
    Number.isSafeInteger(value.gid) && value.gid >= 0,
  `${label} scalar fields are invalid`)
  for (const key of ['dev', 'ino']) assert(typeof value[key] === 'string' &&
    /^(?:0|[1-9][0-9]*)$/.test(value[key]), `${label} ${key} is invalid`)
}
function validateSemanticEntries(value, label) {
  assert(Array.isArray(value), `${label} is not an array`)
  let previous = ''
  for (const entry of value) {
    plain(entry, `${label} entry`)
    exactKeys(entry, ['path', 'type', 'mode', 'sha256'], `${label} entry`)
    assert(safeRelative(entry.path) && byteSort(previous, entry.path) < 0,
      `${label} paths are unsafe, duplicate, or unsorted`)
    assert((entry.type === 'file' || entry.type === 'directory') &&
      Number.isSafeInteger(entry.mode) && entry.mode >= 0 && entry.mode <= 0o777,
    `${label} entry scalar fields are invalid`)
    if (entry.type === 'file') digest(entry.sha256, `${label} entry digest`)
    else assert(entry.sha256 === null, `${label} directory digest is invalid`)
    previous = entry.path
  }
}
function validateIndexEntries(value) {
  assert(Array.isArray(value) && value.length > 0, 'repository index snapshot is invalid')
  let previous = ''
  for (const entry of value) {
    plain(entry, 'repository index snapshot entry')
    exactKeys(entry, ['flag', 'mode', 'object', 'stage', 'path'],
      'repository index snapshot entry')
    assert(entry.flag === 'H' && (entry.mode === '100644' || entry.mode === '100755') &&
      commitRE.test(entry.object) && entry.stage === 0 && safeRelative(entry.path) &&
      byteSort(previous, entry.path) < 0,
    'repository index snapshot entry drifted')
    previous = entry.path
  }
}
function validateRefs(value, headRef, repositoryCommit) {
  assert(Array.isArray(value) && value.length > 0, 'repository refs snapshot is invalid')
  let previous = ''; let headMatches = false
  for (const entry of value) {
    plain(entry, 'repository refs snapshot entry')
    exactKeys(entry, ['name', 'object'], 'repository refs snapshot entry')
    assert(/^refs\/[A-Za-z0-9][A-Za-z0-9._\/-]*$/.test(entry.name) &&
      !entry.name.split('/').includes('..') && commitRE.test(entry.object) &&
      byteSort(previous, entry.name) < 0,
    'repository refs snapshot entry drifted')
    if (entry.name === headRef && entry.object === repositoryCommit) headMatches = true
    previous = entry.name
  }
  assert(headMatches, 'repository HEAD ref does not resolve to the pinned commit')
}
function plain(value, label) { assert(value && typeof value === 'object' && !Array.isArray(value), `${label} is not an object`) }
function exactKeys(value, keys, label) { assert(JSON.stringify(Object.keys(value).sort()) === JSON.stringify([...keys].sort()), `${label} keys drifted`) }
function safeRelative(value) { return typeof value === 'string' && value.length > 0 && Buffer.byteLength(value) <= 4096 && !isAbsolute(value) && !value.includes('\\') && !value.includes('\0') && value.split('/').every((part) => part && part !== '.' && part !== '..') }
function exactAbsolute(value, label) { assert(typeof value === 'string' && isAbsolute(value) && resolve(value) === value && !value.includes('\0') && !value.endsWith(sep), `${label} is not exact absolute`); return value }
function optionalCommit(value) { if (value !== undefined) commit(value, 'expected repository commit') }
function optionalDigest(value) { if (value !== undefined) digest(value, 'expected repository closure') }
function commit(value, label) { assert(commitRE.test(value), `${label} is invalid`); return value }
function digest(value, label) { assert(digestRE.test(value), `${label} is invalid`); return value }
function assert(condition, message) { if (!condition) throw new Error(message) }
