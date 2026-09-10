import assert from 'node:assert/strict'
import { execFile as execFileCallback } from 'node:child_process'
import {
  access,
  chmod,
  lstat,
  mkdir,
  mkdtemp,
  readdir,
  readFile,
  readlink,
  realpath,
  rm,
  symlink,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { basename, join, resolve } from 'node:path'
import test from 'node:test'
import { promisify } from 'node:util'

const root = resolve(import.meta.dirname, '../..')
const execFile = promisify(execFileCallback)
const isDarwin = process.platform === 'darwin'
let buildRoot
let executable
let canonicalTempRoot

test.before(async () => {
  if (!isDarwin) return
  canonicalTempRoot = await realpath(tmpdir())
  buildRoot = await mkdtemp(join(tmpdir(), 'chora-o4visionocr-atomic-test-'))
  const clangCache = join(buildRoot, 'clang-cache')
  const swiftCache = join(buildRoot, 'swift-cache')
  await mkdir(clangCache, { mode: 0o700 })
  await mkdir(swiftCache, { mode: 0o700 })
  executable = join(buildRoot, 'o4visionocr-atomic-test')
  const args = ['-D', 'O4VISIONOCR_ATOMIC_WRITER_TESTING']
  const compatibleSDK = '/Library/Developer/CommandLineTools/SDKs/MacOSX15.4.sdk'
  try {
    await access(compatibleSDK)
    args.push('-sdk', compatibleSDK)
  } catch {
    // A matching default SDK is the normal case outside this pinned builder.
  }
  args.push(resolve(import.meta.dirname, 'main.swift'), '-o', executable)
  await execFile('swiftc', args, {
    env: {
      ...process.env,
      CLANG_MODULE_CACHE_PATH: clangCache,
      SWIFT_MODULE_CACHE_PATH: swiftCache,
    },
  })
})

test.after(async () => {
  if (buildRoot) await rm(buildRoot, { recursive: true, force: true })
})

test('native OCR source performs Vision recognition and binds the actual revision', async () => {
  const source = await readFile(resolve(import.meta.dirname, 'main.swift'), 'utf8')
  assert.match(source, /import Vision/)
  assert.match(source, /VNRecognizeTextRequest\(\)/)
  assert.match(source, /request\.recognitionLevel = \.accurate/)
  assert.match(source, /request\.recognitionLanguages = \["en-US"\]/)
  assert.match(source, /request\.usesLanguageCorrection = true/)
  assert.match(source, /try handler\.perform\(\[request\]\)/)
  assert.match(source, /let actualRevision = Int\(request\.revision\)/)
  assert.match(source, /"modelBytes": "opaque_unavailable"/)
  assert.doesNotMatch(source, /modelFile|modelSha256|descriptor[_ -]as[_ -]model/i)
})

test('OCR CLI and Runner launcher are exact and the OS profile denies all network access', async () => {
  const source = await readFile(resolve(import.meta.dirname, 'main.swift'), 'utf8')
  const profile = await readFile(resolve(import.meta.dirname, 'deny-network.sb'), 'utf8')
  const runner = await readFile(resolve(root, 'e2e/o4-recovery-residue-record.mjs'), 'utf8')
  assert.match(source, /let flags = \["--protocol", "--engine-binding", "--language", "--input", "--output", "--network"\]/)
  assert.match(source, /chora\.m1-o4-system-managed-ocr\.v2/)
  assert.equal(profile, '(version 1)\n(allow default)\n(deny network*)\n')
  assert.match(runner, /await runPinnedTool\(sandboxExecutable,/)
  assert.match(runner, /\['-f', sandboxProfileFile, executableFile, \.\.\.ocrArgv\]/)
  assert.match(runner, /'--protocol', RECOVERY_RESIDUE_OCR_PROTOCOL, '--engine-binding', engineBindingFile/)
  assert.doesNotMatch(runner, /runPinnedTool\(executableFile,\s*ocrArgv/)
})

test('engine JSON is strict, owner-bound, digest-bound, and rejects network allow rules', async () => {
  const source = await readFile(resolve(import.meta.dirname, 'main.swift'), 'utf8')
  assert.match(source, /try validateBindingShape\(bindingData\)/)
  assert.match(source, /ownerUID: 0/)
  assert.match(source, /profileLines\?\.contains\(where: \{ \$0\.hasPrefix\("\(allow network"\) \}\) == false/)
  assert.match(source, /binding\.bindingDigest == sha256\(try canonicalJSON\(dictionary\)\)/)
  assert.match(source, /O_CREAT \| O_EXCL \| O_WRONLY \| O_CLOEXEC \| O_NOFOLLOW/)
  assert.match(source, /fchmod\(descriptor, 0o400\)/)
})

test('atomic writer is descriptor-relative, identity-bound, and no-replace', async () => {
  const source = await readFile(resolve(import.meta.dirname, 'main.swift'), 'utf8')
  assert.match(source, /open\("\/", O_RDONLY \| O_DIRECTORY \| O_CLOEXEC \| O_NOFOLLOW\)/)
  assert.match(source, /openat\(current, component, O_RDONLY \| O_DIRECTORY \| O_CLOEXEC \| O_NOFOLLOW\)/)
  assert.match(source, /openat\(parent\.descriptor, stagingName,/)
  assert.match(source, /fstat\(descriptor, &finished\)/)
  assert.match(source, /fstatat\(parentDescriptor, name, &info, AT_SYMLINK_NOFOLLOW\)/)
  assert.match(source, /renameatx_np\(parent\.descriptor, stagingName,[\s\S]*parent\.descriptor, parent\.finalName,[\s\S]*UInt32\(RENAME_EXCL\)\)/)
  assert.match(source, /unlinkat\(quarantine\.descriptor, "entry", 0\)/)
  assert.match(source, /sameExactIdentity\(expected\.value, moved\)/)
  assert.doesNotMatch(source, /open\(url\.path/)
  assert.doesNotMatch(source, /unlink\(url\.path\)/)
  assert.doesNotMatch(source, /FileManager\.default\.removeItem/)
})

test('final output stays absent while staging is written and appears atomically readonly',
  { skip: !isDarwin }, async () => {
    const parent = await privateTestParent('success')
    try {
      const output = join(parent, 'record.json')
      await runMode('observe-during-write', output)
      const info = await lstat(output)
      assert.equal(info.isFile(), true)
      assert.equal(info.mode & 0o7777, 0o400)
      assert.equal(info.nlink, 1)
      assert.equal(info.size, 1024 * 1024)
      assert.deepEqual(await readdir(parent), ['record.json'])
    } finally {
      await rm(parent, { recursive: true, force: true })
    }
  })

test('write failure removes only the exact private staging inode',
  { skip: !isDarwin }, async () => {
    const parent = await privateTestParent('failure')
    try {
      await runModeFails('fail-after-write', join(parent, 'record.json'))
      assert.deepEqual(await readdir(parent), [])
    } finally {
      await rm(parent, { recursive: true, force: true })
    }
  })

test('cleanup after-check swap preserves the foreign inode in quarantine',
  { skip: !isDarwin }, async () => {
    const parent = await privateTestParent('cleanup-swap')
    try {
      const output = join(parent, 'record.json')
      await runModeFails('cleanup-swap-after-check', output)
      await assert.rejects(lstat(output), { code: 'ENOENT' })
      const backup = join(parent, '.chora-o4visionocr-test-owned-backup')
      assert.equal((await lstat(backup)).size, 1024 * 1024)
      const quarantined = await exactQuarantineEntry(parent)
      assert.equal(await readFile(quarantined, 'utf8'), 'foreign-inode')
      assert.notEqual((await lstat(backup)).ino, (await lstat(quarantined)).ino)
      assert.equal((await entriesWithPrefix(parent, '.chora-o4visionocr-stage-')).length, 0)
    } finally {
      await rm(parent, { recursive: true, force: true })
    }
  })

test('publish after-check swap restores final absence and preserves the foreign inode',
  { skip: !isDarwin }, async () => {
    const parent = await privateTestParent('publish-swap')
    try {
      const output = join(parent, 'record.json')
      await runModeFails('publish-swap-after-check', output)
      await assert.rejects(lstat(output), { code: 'ENOENT' })
      const backup = join(parent, '.chora-o4visionocr-test-owned-backup')
      assert.equal((await lstat(backup)).size, 1024 * 1024)
      const quarantined = await exactQuarantineEntry(parent)
      assert.equal(await readFile(quarantined, 'utf8'), 'foreign-inode')
      assert.equal((await entriesWithPrefix(parent, '.chora-o4visionocr-stage-')).length, 0)
    } finally {
      await rm(parent, { recursive: true, force: true })
    }
  })

test('no-replace publication preserves a final path created after the absence check',
  { skip: !isDarwin }, async () => {
    const parent = await privateTestParent('no-replace')
    try {
      const output = join(parent, 'record.json')
      await runModeFails('occupy-final-after-check', output)
      assert.equal(await readFile(output, 'utf8'), 'foreign-inode')
      assert.equal((await lstat(output)).mode & 0o7777, 0o400)
      assert.deepEqual(await readdir(parent), ['record.json'])
    } finally {
      await rm(parent, { recursive: true, force: true })
    }
  })

test('hardlinked staging is rejected without unlinking either inode name',
  { skip: !isDarwin }, async () => {
    const parent = await privateTestParent('hardlink')
    try {
      const output = join(parent, 'record.json')
      await runModeFails('hardlink-after-check', output)
      await assert.rejects(lstat(output), { code: 'ENOENT' })
      const backup = join(parent, '.chora-o4visionocr-test-owned-backup')
      const quarantined = await exactQuarantineEntry(parent)
      const backupInfo = await lstat(backup)
      const quarantineInfo = await lstat(quarantined)
      assert.equal(backupInfo.ino, quarantineInfo.ino)
      assert.equal(backupInfo.nlink, 2)
      assert.equal(quarantineInfo.nlink, 2)
    } finally {
      await rm(parent, { recursive: true, force: true })
    }
  })

test('symlink-swapped staging is rejected and preserved outside the final name',
  { skip: !isDarwin }, async () => {
    const parent = await privateTestParent('symlink')
    try {
      const output = join(parent, 'record.json')
      await runModeFails('symlink-after-check', output)
      await assert.rejects(lstat(output), { code: 'ENOENT' })
      const quarantined = await exactQuarantineEntry(parent)
      assert.equal((await lstat(quarantined)).isSymbolicLink(), true)
      assert.equal(await readlink(quarantined), '.chora-o4visionocr-test-owned-backup')
    } finally {
      await rm(parent, { recursive: true, force: true })
    }
  })

test('non-0700 and symlink parents fail before staging creation',
  { skip: !isDarwin }, async () => {
    const nonPrivate = await privateTestParent('non-private')
    const linkRoot = await privateTestParent('parent-link')
    try {
      await chmod(nonPrivate, 0o755)
      await runModeFails('observe-during-write', join(nonPrivate, 'record.json'))
      assert.deepEqual(await readdir(nonPrivate), [])

      const realParent = join(linkRoot, 'real')
      const linkedParent = join(linkRoot, 'linked')
      await mkdir(realParent, { mode: 0o700 })
      await symlink(realParent, linkedParent)
      await runModeFails('observe-during-write', join(linkedParent, 'record.json'))
      assert.deepEqual(await readdir(realParent), [])
    } finally {
      await chmod(nonPrivate, 0o700)
      await rm(nonPrivate, { recursive: true, force: true })
      await rm(linkRoot, { recursive: true, force: true })
    }
  })

async function privateTestParent(label) {
  const parent = await mkdtemp(join(canonicalTempRoot, `chora-o4visionocr-${label}-`))
  await chmod(parent, 0o700)
  return parent
}

async function runMode(mode, output) {
  await execFile(executable, ['--atomic-writer-self-test', mode, output])
}

async function runModeFails(mode, output) {
  await assert.rejects(runMode(mode, output), (error) => error.code === 1)
}

async function entriesWithPrefix(parent, prefix) {
  return (await readdir(parent)).filter((name) => name.startsWith(prefix)).sort()
}

async function exactQuarantineEntry(parent) {
  const quarantines = await entriesWithPrefix(parent, '.chora-o4visionocr-quarantine-')
  assert.equal(quarantines.length, 1)
  const quarantine = join(parent, quarantines[0])
  assert.equal((await lstat(quarantine)).mode & 0o7777, 0o700)
  assert.deepEqual(await readdir(quarantine), ['entry'])
  assert.equal(basename(join(quarantine, 'entry')), 'entry')
  return join(quarantine, 'entry')
}
