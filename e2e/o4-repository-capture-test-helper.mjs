import { createHash } from 'node:crypto'
import { execFile } from 'node:child_process'
import { chmod, lstat, mkdir, mkdtemp, readFile, realpath } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

import { cleanupOwnedPath, ownedPathIdentity } from './o4-owned-path-cleanup.mjs'

const execFileAsync = promisify(execFile)
const repositoryRoot = fileURLToPath(new URL('..', import.meta.url)).replace(/\/$/, '')

export async function buildTestRepositoryCapture(testHooks = undefined) {
  const buildRoot = await realpath(await mkdtemp('/private/tmp/chora-o4-capture-controller-'))
  const initialStats = await lstat(buildRoot, { bigint: true })
  const initialIdentity = ownedPathIdentity(initialStats)
  assert(dirname(buildRoot) === '/private/tmp' && initialStats.isDirectory() &&
    !initialStats.isSymbolicLink() && initialStats.uid === BigInt(process.getuid()) &&
    Number(initialStats.mode & 0o777n) === 0o700,
  'capture-controller build root is not an owner-0700 private temporary directory')
  const executableFile = join(buildRoot, 'o4-service-controller')
  const goCache = join(buildRoot, 'go-cache')
  let complete = false
  let operationError
  try {
    if (testHooks?.afterBuildRootCreated !== undefined) {
      assert(typeof testHooks.afterBuildRootCreated === 'function',
        'capture-controller test hook is invalid')
      await testHooks.afterBuildRootCreated({ buildRoot })
    }
    await mkdir(goCache, { mode: 0o700 })
    await execFileAsync('/opt/homebrew/bin/go', [
      'build', '-mod=vendor', '-trimpath', '-o', executableFile, './cmd/o4-service-controller',
    ], {
      cwd: repositoryRoot,
      env: {
        PATH: '/opt/homebrew/bin:/usr/bin:/bin', HOME: '/var/empty', GOCACHE: goCache,
        GOENV: 'off', GOTOOLCHAIN: 'local', CGO_ENABLED: '0',
      },
      timeout: 120_000,
      maxBuffer: 8 * 1024 * 1024,
    })
    await chmod(executableFile, 0o500)
    const info = await lstat(executableFile, { bigint: true })
    assert(info.isFile() && !info.isSymbolicLink() && info.nlink === 1n &&
      info.uid === BigInt(process.getuid()) && Number(info.mode & 0o777n) === 0o500,
    'capture-controller test executable is not an owner-0500 regular file')
    const executableSha256 = createHash('sha256').update(await readFile(executableFile)).digest('hex')
    complete = true
    let cleaned = false
    return {
      capture: Object.freeze({ executableFile, executableSha256 }),
      cleanup: async () => {
        if (cleaned) return
        await cleanupBuildRoot(buildRoot, initialIdentity,
          Object.freeze({ executableFile, executableSha256 }))
        cleaned = true
      },
    }
  } catch (error) {
    operationError = error
    throw error
  } finally {
    if (!complete) {
      const bootstrap = testHooks?.bootstrapCleanupCapability
      try {
        if (bootstrap === undefined) {
          throw new Error(`atomic cleanup unavailable; retained capture-controller build residue: ${buildRoot}`)
        }
        await cleanupBuildRoot(buildRoot, initialIdentity, bootstrap)
      } catch (cleanupError) {
        if (operationError !== undefined) {
          throw new AggregateError([operationError, cleanupError],
            'capture-controller build and identity-bound cleanup both failed')
        }
        throw cleanupError
      }
    }
  }
}

async function cleanupBuildRoot(buildRoot, identity, capability) {
  await cleanupOwnedPath({
    capability,
    path: buildRoot,
    identity,
    disposition: 'recursive_directory',
  })
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
}
