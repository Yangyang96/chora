import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { EventEmitter } from 'node:events'
import { PassThrough, Writable } from 'node:stream'
import { createServer } from 'node:net'
import {
  chmod, copyFile, lstat, mkdir, mkdtemp, readFile, realpath, rename, rm, writeFile,
} from 'node:fs/promises'
import { join } from 'node:path'
import test from 'node:test'

import {
  cleanupOwnedPath, createOwnedDirectory, ownedPathIdentity, publishOwnedPath, replaceOwnedPath,
  validateOwnedPathCapability, writeReadonlyOwnedPath,
} from './o4-owned-path-cleanup.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'

async function controllerFixture(t) {
  const root = await mkdtemp('/private/tmp/chora-o4-owned-path-unit-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const executableFile = join(root, 'controller')
  await copyFile(process.execPath, executableFile)
  await chmod(executableFile, 0o500)
  const executableSha256 = createHash('sha256').update(await readFile(executableFile)).digest('hex')
  return Object.freeze({ executableFile, executableSha256 })
}

function fakeSpawn(calls, action = async () => {}, result = {}) {
  return (file, argv, options) => {
    const child = new EventEmitter()
    child.stdout = new PassThrough()
    child.stderr = new PassThrough()
    const chunks = []
    child.stdin = new Writable({
      write(chunk, _encoding, callback) { chunks.push(Buffer.from(chunk)); callback() },
      final(callback) {
        Promise.resolve().then(async () => {
          const stdin = Buffer.concat(chunks)
          calls.push({ file, argv: [...argv], options, stdin })
          await action(JSON.parse(stdin.toString('utf8')))
          if (result.stdout !== undefined) child.stdout.write(result.stdout)
          if (result.stderr !== undefined) child.stderr.write(result.stderr)
          callback()
          queueMicrotask(() => child.emit('close', result.code ?? 0, result.signal ?? null))
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

function fakeRawSpawn(calls, action) {
  return (file, argv, options) => {
    const child = new EventEmitter()
    child.stdout = new PassThrough()
    child.stderr = new PassThrough()
    const chunks = []
    child.stdin = new Writable({
      write(chunk, _encoding, callback) { chunks.push(Buffer.from(chunk)); callback() },
      final(callback) {
        Promise.resolve().then(async () => {
          const stdin = Buffer.concat(chunks)
          calls.push({ file, argv: [...argv], options, stdin })
          const result = await action(stdin)
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

test('ownedPathIdentity preserves the exact raw bigint stat identity', async (t) => {
  const root = await mkdtemp('/private/tmp/chora-o4-owned-path-identity-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const info = await lstat(root, { bigint: true })
  assert.deepEqual(ownedPathIdentity(info), {
    mode: Number(info.mode), uid: Number(info.uid), gid: Number(info.gid),
    dev: info.dev.toString(), ino: info.ino.toString(),
  })
  assert.equal(Object.isFrozen(ownedPathIdentity(info)), true)
})

test('real pinned controller creates a directory beneath the exact bound parent', async (t) => {
  const fixture = await buildTestRepositoryCapture()
  t.after(() => fixture.cleanup())
  const parent = await mkdtemp('/private/tmp/chora-o4-owned-directory-create-')
  t.after(() => rm(parent, { recursive: true, force: true }))
  await chmod(parent, 0o700)
  const target = join(parent, 'created')
  const parentIdentity = ownedPathIdentity(await lstat(parent, { bigint: true }))
  const identity = await createOwnedDirectory({
    capability: fixture.capture, path: target, parentIdentity,
  })
  const targetInfo = await lstat(target, { bigint: true })
  assert.equal(targetInfo.isDirectory(), true)
  assert.equal(Number(targetInfo.mode & 0o777n), 0o700)
  assert.deepEqual(identity, ownedPathIdentity(targetInfo))
  await assert.rejects(createOwnedDirectory({
    capability: fixture.capture, path: join(parent, 'wrong-parent'),
    parentIdentity: { ...parentIdentity, ino: String(BigInt(parentIdentity.ino) + 1n) },
  }), /controller failed/)
})

test('cleanup sends the exact eight argv and canonical stdin request with no shell', async (t) => {
  const capability = await controllerFixture(t)
  const target = join('/private/tmp', `chora-o4-already-absent-${process.pid}-${Date.now()}`)
  const fixture = await mkdtemp('/private/tmp/chora-o4-owned-path-identity-source-')
  t.after(() => rm(fixture, { recursive: true, force: true }))
  const identity = ownedPathIdentity(await lstat(fixture, { bigint: true }))
  const calls = []
  await cleanupOwnedPath({
    capability, path: target, identity, disposition: 'recursive_directory',
  }, { spawn: fakeSpawn(calls) })
  assert.equal(calls.length, 1)
  assert.deepEqual(calls[0].argv, [
    '--protocol', 'chora.m1-o4-owned-path-cleanup.v1',
    '--controller-sha256', capability.executableSha256,
    '--network', 'disabled',
    '--request-input', 'stdin',
  ])
  assert.equal(calls[0].options.shell, false)
  assert.deepEqual(calls[0].options.stdio, ['pipe', 'pipe', 'pipe'])
  assert.equal(calls[0].options.env.CHORA_NETWORK, 'disabled')
  assert.equal(calls[0].stdin.toString('utf8'), JSON.stringify({
    action: 'remove_owned_path',
    disposition: 'recursive_directory',
    expectedIdentity: {
      dev: identity.dev, gid: identity.gid, ino: identity.ino, mode: identity.mode, uid: identity.uid,
    },
    path: target,
    schemaVersion: 'chora.m1-o4-owned-path-cleanup-request.v1',
  }))
})

test('publish sends canonical stdin and accepts only the identity-preserving move', async (t) => {
  const capability = await controllerFixture(t)
  const root = await mkdtemp('/private/tmp/chora-o4-owned-path-publish-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const sourcePath = join(root, 'source')
  const targetPath = join(root, 'target')
  await writeFile(sourcePath, 'published\n', { mode: 0o600 })
  const identity = ownedPathIdentity(await lstat(sourcePath, { bigint: true }))
  const calls = []
  await publishOwnedPath({
    capability, sourcePath, targetPath, identity, disposition: 'regular_file',
  }, { spawn: fakeSpawn(calls, () => rename(sourcePath, targetPath)) })
  assert.equal(await readFile(targetPath, 'utf8'), 'published\n')
  assert.deepEqual(calls[0].argv, [
    '--protocol', 'chora.m1-o4-owned-path-publish.v1',
    '--controller-sha256', capability.executableSha256,
    '--network', 'disabled',
    '--request-input', 'stdin',
  ])
  assert.equal(calls[0].stdin.toString('utf8'), JSON.stringify({
    action: 'publish_owned_path',
    disposition: 'regular_file',
    expectedIdentity: {
      dev: identity.dev, gid: identity.gid, ino: identity.ino, mode: identity.mode, uid: identity.uid,
    },
    schemaVersion: 'chora.m1-o4-owned-path-publish-request.v1',
    sourcePath,
    targetPath,
  }))
})

test('replace sends the exact canonical two-identity request', async (t) => {
  const capability = await controllerFixture(t)
  const root = await mkdtemp('/private/tmp/chora-o4-owned-path-replace-unit-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const sourcePath = join(root, 'source')
  const targetPath = join(root, 'target')
  await writeFile(sourcePath, 'new\n', { mode: 0o400 })
  await writeFile(targetPath, 'old\n', { mode: 0o600 })
  const sourceIdentity = ownedPathIdentity(await lstat(sourcePath, { bigint: true }))
  const targetIdentity = ownedPathIdentity(await lstat(targetPath, { bigint: true }))
  const calls = []
  await replaceOwnedPath({ capability, sourcePath, targetPath, sourceIdentity, targetIdentity }, {
    spawn: fakeSpawn(calls, async () => {
      await rm(targetPath)
      await rename(sourcePath, targetPath)
    }),
  })
  assert.deepEqual(calls[0].argv, [
    '--protocol', 'chora.m1-o4-owned-path-replace.v1',
    '--controller-sha256', capability.executableSha256,
    '--network', 'disabled',
    '--request-input', 'stdin',
  ])
  assert.equal(calls[0].stdin.toString('utf8'), JSON.stringify({
    action: 'replace_owned_path',
    schemaVersion: 'chora.m1-o4-owned-path-replace-request.v1',
    sourceDisposition: 'regular_file',
    sourceExpectedIdentity: {
      dev: sourceIdentity.dev, gid: sourceIdentity.gid, ino: sourceIdentity.ino,
      mode: sourceIdentity.mode, uid: sourceIdentity.uid,
    },
    sourcePath,
    targetDisposition: 'regular_file',
    targetExpectedIdentity: {
      dev: targetIdentity.dev, gid: targetIdentity.gid, ino: targetIdentity.ino,
      mode: targetIdentity.mode, uid: targetIdentity.uid,
    },
    targetPath,
  }))
})

test('real pinned controller returns and revalidates the exact readonly inode receipt', async (t) => {
  const fixture = await buildTestRepositoryCapture()
  t.after(() => fixture.cleanup())
  const root = await mkdtemp('/private/tmp/chora-o4-owned-readonly-write-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const target = join(root, 'commit-marker.json')
  const bytes = Buffer.from('{"status":"passed"}\n')
  const receipt = await writeReadonlyOwnedPath({
    capability: fixture.capture, path: target, bytes,
  })
  const info = await lstat(target, { bigint: true })
  assert.equal(receipt.schemaVersion, 'chora.m1-o4-owned-readonly-write-receipt.v1')
  assert.equal(receipt.status, 'passed')
  assert.equal(receipt.target, target)
  assert.equal(receipt.sha256, createHash('sha256').update(bytes).digest('hex'))
  assert.equal(receipt.byteLength, bytes.length)
  assert.deepEqual(receipt.identity, ownedPathIdentity(info))
  assert.equal(Number(info.mode & 0o777n), 0o400)
  assert.equal(await readFile(target, 'utf8'), bytes.toString('utf8'))
  await assert.rejects(writeReadonlyOwnedPath({
    capability: fixture.capture, path: target, bytes: Buffer.from('replacement\n'),
  }), /not absent/)
  assert.equal(await readFile(target, 'utf8'), bytes.toString('utf8'))
})

test('readonly writer sends raw bytes with exact argv and trusts only the native identity receipt', async (t) => {
  const capability = await controllerFixture(t)
  const root = await mkdtemp('/private/tmp/chora-o4-owned-readonly-unit-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const target = join(root, 'terminal.json')
  const bytes = Buffer.from('{"terminal":true}\n')
  const sha256 = createHash('sha256').update(bytes).digest('hex')
  const calls = []
  const receipt = await writeReadonlyOwnedPath({ capability, path: target, bytes }, {
    spawn: fakeRawSpawn(calls, async (stdin) => {
      await writeFile(target, stdin, { flag: 'wx', mode: 0o400 })
      const identity = ownedPathIdentity(await lstat(target, { bigint: true }))
      return { stdout: `${JSON.stringify({
        schemaVersion: 'chora.m1-o4-owned-readonly-write-receipt.v1',
        status: 'passed', target, sha256, byteLength: stdin.length, identity,
      })}\n` }
    }),
  })
  assert.equal(receipt.sha256, sha256)
  assert.equal(calls.length, 1)
  assert.deepEqual(calls[0].argv, [
    '--protocol', 'chora.m1-o4-owned-readonly-write.v1',
    '--controller-sha256', capability.executableSha256,
    '--target', target,
    '--expected-sha256', sha256,
    '--expected-byte-length', String(bytes.length),
    '--network', 'disabled',
    '--input', 'stdin',
  ])
  assert.deepEqual(calls[0].stdin, bytes)
  assert.equal(calls[0].options.shell, false)
})

test('readonly writer rejects a malformed success receipt without pathname cleanup', async (t) => {
  const capability = await controllerFixture(t)
  const root = await mkdtemp('/private/tmp/chora-o4-owned-readonly-receipt-reject-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const target = join(root, 'retained.json')
  const bytes = Buffer.from('{"retained":true}\n')
  await assert.rejects(writeReadonlyOwnedPath({ capability, path: target, bytes }, {
    spawn: fakeRawSpawn([], async (stdin) => {
      await writeFile(target, stdin, { flag: 'wx', mode: 0o400 })
      const identity = ownedPathIdentity(await lstat(target, { bigint: true }))
      return { stdout: `${JSON.stringify({
        schemaVersion: 'chora.m1-o4-owned-readonly-write-receipt.v1',
        status: 'passed', target,
        sha256: createHash('sha256').update(stdin).digest('hex'),
        byteLength: stdin.length, identity, extra: true,
      })}\n` }
    }),
  }), /keys drifted/)
  assert.deepEqual(await readFile(target), bytes)
})

test('beforeSpawn executable replacement fails closed before fake spawn', async (t) => {
  const capability = await controllerFixture(t)
  const replacement = `${capability.executableFile}.replacement`
  await copyFile(capability.executableFile, replacement)
  await chmod(replacement, 0o500)
  const target = await mkdtemp('/private/tmp/chora-o4-owned-path-swap-target-')
  t.after(() => rm(target, { recursive: true, force: true }))
  const identity = ownedPathIdentity(await lstat(target, { bigint: true }))
  let spawned = false
  await assert.rejects(cleanupOwnedPath({
    capability, path: target, identity, disposition: 'recursive_directory',
  }, {
    spawn: () => { spawned = true; throw new Error('must not spawn') },
    beforeSpawn: () => rename(replacement, capability.executableFile),
  }), (error) => {
    assert.equal(error instanceof AggregateError, true)
    assert.equal(error.errors.length, 2)
    assert.match(error.errors[0].message, /before spawn/)
    assert.match(error.errors[1].message, /during execution|owner-0500/)
    return true
  })
  assert.equal(spawned, false)
})

test('rejects nonexact capability, identity, disposition, output, and process failure', async (t) => {
  const capability = await controllerFixture(t)
  assert.throws(() => validateOwnedPathCapability({ ...capability, extra: true }), /keys drifted/)
  const root = await mkdtemp('/private/tmp/chora-o4-owned-path-reject-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const identity = ownedPathIdentity(await lstat(root, { bigint: true }))
  await assert.rejects(cleanupOwnedPath({
    capability, path: root, identity: { ...identity, extra: true }, disposition: 'recursive_directory',
  }, { spawn: fakeSpawn([]) }), /identity keys drifted/)
  await assert.rejects(cleanupOwnedPath({
    capability, path: root, identity, disposition: 'directory',
  }, { spawn: fakeSpawn([]) }), /disposition/)
  await assert.rejects(cleanupOwnedPath({
    capability, path: root, identity, disposition: 'recursive_directory',
  }, { spawn: fakeSpawn([], async () => {}, { stdout: 'forbidden' }) }), /forbidden output/)
  await assert.rejects(cleanupOwnedPath({
    capability, path: root, identity, disposition: 'recursive_directory',
  }, { spawn: fakeSpawn([], async () => {}, { code: 1 }) }), /controller failed/)
})

test('real pinned controller atomically cleans and publishes every disposition', async (t) => {
  const fixture = await buildTestRepositoryCapture()
  t.after(() => fixture.cleanup())
  const root = await mkdtemp('/private/tmp/chora-o4-owned-path-integration-')
  t.after(() => rm(root, { recursive: true, force: true }))

  const regularFile = join(root, 'regular')
  await writeFile(regularFile, 'regular\n', { mode: 0o600 })
  await cleanupOwnedPath({
    capability: fixture.capture,
    path: regularFile,
    identity: ownedPathIdentity(await lstat(regularFile, { bigint: true })),
    disposition: 'regular_file',
  })
  await assert.rejects(lstat(regularFile), { code: 'ENOENT' })

  const emptyDirectory = join(root, 'empty')
  await mkdir(emptyDirectory, { mode: 0o700 })
  await cleanupOwnedPath({
    capability: fixture.capture,
    path: emptyDirectory,
    identity: ownedPathIdentity(await lstat(emptyDirectory, { bigint: true })),
    disposition: 'empty_directory',
  })
  await assert.rejects(lstat(emptyDirectory), { code: 'ENOENT' })

  const recursiveDirectory = join(root, 'recursive')
  await mkdir(join(recursiveDirectory, 'nested'), { recursive: true, mode: 0o700 })
  await writeFile(join(recursiveDirectory, 'nested', 'file'), 'nested\n')
  await cleanupOwnedPath({
    capability: fixture.capture,
    path: recursiveDirectory,
    identity: ownedPathIdentity(await lstat(recursiveDirectory, { bigint: true })),
    disposition: 'recursive_directory',
  })
  await assert.rejects(lstat(recursiveDirectory), { code: 'ENOENT' })

  const sourcePath = join(root, 'source')
  const targetPath = join(root, 'target')
  await mkdir(sourcePath, { mode: 0o700 })
  const publishIdentity = ownedPathIdentity(await lstat(sourcePath, { bigint: true }))
  await publishOwnedPath({
    capability: fixture.capture, sourcePath, targetPath,
    identity: publishIdentity, disposition: 'empty_directory',
  })
  assert.deepEqual(ownedPathIdentity(await lstat(targetPath, { bigint: true })), publishIdentity)

  await t.test('unix socket cleanup when the host sandbox permits bind', async (socketTest) => {
    const socketRoot = await realpath(await mkdtemp('/private/tmp/chora-o4-owned-socket-'))
    socketTest.after(() => rm(socketRoot, { recursive: true, force: true }))
    const socketPath = join(socketRoot, 'owned.sock')
    const server = createServer()
    try {
      await new Promise((resolvePromise, rejectPromise) => {
        server.once('error', rejectPromise)
        server.listen(socketPath, resolvePromise)
      })
    } catch (error) {
      if (error?.code === 'EPERM') {
        socketTest.skip('current command sandbox forbids Unix socket bind')
        return
      }
      throw error
    }
    await chmod(socketPath, 0o600)
    const socketIdentity = ownedPathIdentity(await lstat(socketPath, { bigint: true }))
    await new Promise((resolvePromise, rejectPromise) => server.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    }))
    await cleanupOwnedPath({
      capability: fixture.capture, path: socketPath,
      identity: socketIdentity, disposition: 'unix_socket',
    })
    await assert.rejects(lstat(socketPath), { code: 'ENOENT' })
  })

  const replaceSource = join(root, 'replace-source')
  const replaceTarget = join(root, 'replace-target')
  await writeFile(replaceSource, 'replacement\n', { mode: 0o400 })
  await writeFile(replaceTarget, 'authenticated old target\n', { mode: 0o600 })
  const replaceSourceIdentity = ownedPathIdentity(await lstat(replaceSource, { bigint: true }))
  const replaceTargetIdentity = ownedPathIdentity(await lstat(replaceTarget, { bigint: true }))
  await replaceOwnedPath({
    capability: fixture.capture,
    sourcePath: replaceSource,
    targetPath: replaceTarget,
    sourceIdentity: replaceSourceIdentity,
    targetIdentity: replaceTargetIdentity,
  })
  assert.equal(await readFile(replaceTarget, 'utf8'), 'replacement\n')
  assert.deepEqual(ownedPathIdentity(await lstat(replaceTarget, { bigint: true })),
    replaceSourceIdentity)
})

test('real controller rejects a deterministic pre-spawn replace target swap', async (t) => {
  const fixture = await buildTestRepositoryCapture()
  t.after(() => fixture.cleanup())
  const root = await mkdtemp('/private/tmp/chora-o4-owned-path-replace-swap-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const sourcePath = join(root, 'source')
  const targetPath = join(root, 'target')
  const foreignPath = join(root, 'foreign')
  await writeFile(sourcePath, 'new\n', { mode: 0o400 })
  await writeFile(targetPath, 'old\n', { mode: 0o600 })
  await writeFile(foreignPath, 'foreign\n', { mode: 0o600 })
  const sourceIdentity = ownedPathIdentity(await lstat(sourcePath, { bigint: true }))
  const targetIdentity = ownedPathIdentity(await lstat(targetPath, { bigint: true }))
  await assert.rejects(replaceOwnedPath({
    capability: fixture.capture, sourcePath, targetPath, sourceIdentity, targetIdentity,
  }, { beforeSpawn: () => rename(foreignPath, targetPath) }), /owned-path controller failed/)
  assert.equal(await readFile(sourcePath, 'utf8'), 'new\n')
  assert.equal(await readFile(targetPath, 'utf8'), 'foreign\n')
})
