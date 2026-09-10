import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFile } from 'node:child_process'
import {
  chmod, copyFile, link, lstat, mkdir, mkdtemp, readFile, readdir, realpath, rename, rm,
  symlink, utimes, writeFile,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { basename, dirname, join } from 'node:path'
import test from 'node:test'
import { promisify } from 'node:util'

import {
  inspectRepositoryAuthority as inspectRepositoryAuthorityWithCapture,
  validateRepositoryAuthoritySnapshot,
  verifyRepositoryAuthority as verifyRepositoryAuthorityWithCapture,
  verifyRestartRepositoryAuthority as verifyRestartRepositoryAuthorityWithCapture,
} from './o4-repository-authority.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'

const execFileAsync = promisify(execFile)
let captureFixture

test.before(async () => { captureFixture = await buildTestRepositoryCapture() })
test.after(async () => { await captureFixture?.cleanup() })

function inspectRepositoryAuthority(input) {
  return inspectRepositoryAuthorityWithCapture({ ...input, capture: captureFixture.capture })
}

function verifyRepositoryAuthority(authority) {
  return verifyRepositoryAuthorityWithCapture({ ...authority, capture: captureFixture.capture })
}

function verifyRestartRepositoryAuthority(authority) {
  return verifyRestartRepositoryAuthorityWithCapture({ ...authority, capture: captureFixture.capture })
}

async function copiedCapture(t, mode = 0o500) {
  const root = await realpath(await mkdtemp('/private/tmp/chora-o4-capture-copy-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const executableFile = join(root, 'controller')
  await copyFile(captureFixture.capture.executableFile, executableFile)
  await chmod(executableFile, mode)
  return {
    executableFile,
    executableSha256: createHash('sha256').update(await readFile(executableFile)).digest('hex'),
  }
}

async function swappingCaptureController(t) {
  const root = await realpath(await mkdtemp('/private/tmp/chora-o4-swapping-capture-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const executableFile = join(root, 'controller.mjs')
  const logPath = join(root, 'swap.json')
  const source = `#!${process.execPath}\n` +
    `import { mkdirSync, renameSync, writeFileSync } from 'node:fs'\n` +
    `import { dirname } from 'node:path'\n` +
    `const values = Object.fromEntries(Array.from({ length: process.argv.length - 2 }, ` +
    `(_, index) => index).filter((index) => index % 2 === 0).map((index) => ` +
    `[process.argv[index + 2], process.argv[index + 3]]))\n` +
    `const sessionRoot = dirname(values['--capture-root'])\n` +
    `const displaced = sessionRoot + '-displaced'\n` +
    `renameSync(sessionRoot, displaced)\n` +
    `mkdirSync(sessionRoot, { mode: 0o700 })\n` +
    `writeFileSync(${JSON.stringify(logPath)}, JSON.stringify({ sessionRoot, displaced }))\n` +
    `process.exit(1)\n`
  await writeFile(executableFile, source, { mode: 0o500 })
  return {
    logPath,
    capture: {
      executableFile,
      executableSha256: createHash('sha256').update(await readFile(executableFile)).digest('hex'),
    },
  }
}

async function protocolFaultController(t, protocol, fault) {
  const root = await realpath(await mkdtemp('/private/tmp/chora-o4-protocol-fault-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const executableFile = join(root, 'controller.mjs')
  const source = [
    `#!${process.execPath}`,
    `import { readFileSync, writeSync } from 'node:fs'`,
    `import { spawnSync } from 'node:child_process'`,
    `import { createHash } from 'node:crypto'`,
    `const real = ${JSON.stringify(captureFixture.capture.executableFile)}`,
    `const realDigest = ${JSON.stringify(captureFixture.capture.executableSha256)}`,
    `const targetProtocol = ${JSON.stringify(protocol)}`,
    `const fault = ${JSON.stringify(fault)}`,
    `const argv = process.argv.slice(2)`,
    `const protocolAt = argv.indexOf('--protocol')`,
    `const observedProtocol = protocolAt < 0 ? '' : argv[protocolAt + 1]`,
    `const input = readFileSync(0)`,
    `if (observedProtocol === targetProtocol && fault === 'nonzero') process.exit(17)`,
    `if (observedProtocol === targetProtocol && fault === 'malformed') { process.stdout.write('{]\\n'); process.exit(0) }`,
    `if (observedProtocol === targetProtocol && fault === 'oversize') { const chunk = Buffer.alloc(1024 * 1024, 0x78); for (let index = 0; index < 33; index += 1) writeSync(1, chunk); process.exit(0) }`,
    `const delegated = [...argv]`,
    `const digestAt = delegated.indexOf('--controller-sha256')`,
    `if (digestAt >= 0) delegated[digestAt + 1] = realDigest`,
    `const result = spawnSync(real, delegated, { input, env: process.env, maxBuffer: 40 * 1024 * 1024 })`,
    `if (result.error) throw result.error`,
    `if (result.status !== 0 || result.signal !== null) { process.stderr.write(result.stderr); process.exit(result.status ?? 1) }`,
    `let output = result.stdout`,
    `const sortKeys = (value) => Array.isArray(value) ? value.map(sortKeys) : value && typeof value === 'object' ? Object.fromEntries(Object.keys(value).sort().map((key) => [key, sortKeys(value[key])])) : value`,
    `const canonical = (value) => JSON.stringify(sortKeys(value))`,
    `if (observedProtocol === targetProtocol && (fault === 'root' || fault === 'digest')) {`,
    `  const value = JSON.parse(output.toString('utf8'))`,
    `  if (fault === 'root') {`,
    `    value.rootIdentity.ino += '1'`,
    `    const { manifestDigest, responseDigest, ...core } = value`,
    `    const digest = createHash('sha256').update(canonical(core)).digest('hex')`,
    `    if (manifestDigest !== undefined) value.manifestDigest = digest`,
    `    if (responseDigest !== undefined) value.responseDigest = digest`,
    `  } else if (value.manifestDigest !== undefined) value.manifestDigest = '0'.repeat(64)`,
    `  else value.responseDigest = '0'.repeat(64)`,
    `  output = Buffer.from(canonical(value) + '\\n')`,
    `}`,
    `process.stdout.write(output)`,
  ].join('\n') + '\n'
  await writeFile(executableFile, source, { mode: 0o500 })
  return {
    executableFile,
    executableSha256: createHash('sha256').update(await readFile(executableFile)).digest('hex'),
  }
}

function aggregateContains(error, patterns) {
  if (!(error instanceof AggregateError)) return false
  const messages = error.errors.map((item) => item?.message ?? String(item))
  return patterns.every((pattern) => messages.some((message) => pattern.test(message)))
}

async function git(root, ...args) {
  return execFileAsync('/usr/bin/git', ['-C', root,
    '-c', 'user.name=O4 Fixture', '-c', 'user.email=chora@example.test', ...args], {
    env: { PATH: '/usr/bin:/bin', HOME: '/var/empty', GIT_CONFIG_NOSYSTEM: '1',
      GIT_CONFIG_SYSTEM: '/dev/null', GIT_CONFIG_GLOBAL: '/dev/null', GIT_OPTIONAL_LOCKS: '0' },
  })
}

async function refreshingGit(root, ...args) {
  return execFileAsync('/usr/bin/git', ['-C', root, ...args], {
    env: { PATH: '/usr/bin:/bin', HOME: '/var/empty', GIT_CONFIG_NOSYSTEM: '1',
      GIT_CONFIG_SYSTEM: '/dev/null', GIT_CONFIG_GLOBAL: '/dev/null' },
  })
}

async function repository(t, files = { 'README.md': 'hello\n', 'docs/guide.md': 'guide\n' }) {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'chora-o4-repository-authority-')))
  t.after(() => rm(root, { recursive: true, force: true }))
  await git(root, 'init', '-q')
  for (const [path, bytes] of Object.entries(files)) {
    await mkdir(dirname(join(root, path)), { recursive: true })
    await writeFile(join(root, path), bytes, { mode: 0o644 })
  }
  await git(root, 'add', '--', ...Object.keys(files))
  await git(root, 'commit', '-qm', 'fixture')
  return root
}

async function externalizeGitControl(t, root, relativePath) {
  const externalRoot = await realpath(await mkdtemp(join(tmpdir(), 'chora-o4-external-git-control-')))
  t.after(() => rm(externalRoot, { recursive: true, force: true }))
  const source = join(root, '.git', ...relativePath.split('/'))
  const external = join(externalRoot, basename(relativePath))
  await rename(source, external)
  await symlink(external, source)
}

test('inspects and exactly reverifies a bounded nested repository authority', async (t) => {
  const root = await repository(t)
  const authority = await inspectRepositoryAuthority({ repositoryRoot: root })
  assert.deepEqual(Object.keys(authority), [
    'repositoryRoot', 'repositoryCommit', 'repositoryClosureSha256', 'snapshot',
  ])
  assert.match(authority.repositoryCommit, /^[0-9a-f]{40}$/)
  assert.match(authority.repositoryClosureSha256, /^[0-9a-f]{64}$/)
  assert.equal(authority.snapshot.schemaVersion, 'chora.m1-o4-repository-authority.v2')
  assert.equal(authority.snapshot.repositoryRoot, root)
  assert.equal(authority.snapshot.root.path, root)
  assert.equal(authority.snapshot.gitRoot.path, join(root, '.git'))
  assert.equal(JSON.stringify(authority).includes('/chora-o4-repository-capture-'), false)
  assert.equal(Object.isFrozen(authority.snapshot), true)
  const verified = await verifyRepositoryAuthority(authority)
  assert.equal(verified.repositoryClosureSha256, authority.repositoryClosureSha256)
  await assert.rejects(verifyRepositoryAuthorityWithCapture(authority), /capture capability/)
  await assert.rejects(verifyRestartRepositoryAuthorityWithCapture(authority), /capture capability/)
})

test('requires an exact pinned repository capture capability', async (t) => {
  const root = await repository(t)
  await assert.rejects(inspectRepositoryAuthorityWithCapture({ repositoryRoot: root }),
    /capture capability/)
  await assert.rejects(inspectRepositoryAuthorityWithCapture({
    repositoryRoot: root, capture: { ...captureFixture.capture, extra: true },
  }), /capture capability keys drifted/)
  await assert.rejects(inspectRepositoryAuthorityWithCapture({
    repositoryRoot: root,
    capture: { ...captureFixture.capture, executableSha256: '0'.repeat(64) },
  }), /capture executable digest drifted/)
})

test('native repository protocol canonical digest covers literal angle/ampersand paths', async (t) => {
  const root = await repository(t, { 'README.md': 'hello\n', 'docs/<&>.md': '<&>\n' })
  const authority = await inspectRepositoryAuthority({ repositoryRoot: root })
  assert.equal(authority.snapshot.worktree.some((entry) => entry.path === 'docs/<&>.md'), true)
})

for (const [fault, pattern] of [
  ['malformed', /native scan response.*(?:JSON|framing)/],
  ['oversize', /native scan output exceeded its bound/],
  ['root', /native scan root identity drifted/],
  ['digest', /native scan manifest digest drifted/],
  ['nonzero', /native scan failed/],
]) test(`native repository scan fails closed on ${fault} controller output`, async (t) => {
  const root = await repository(t)
  const capture = await protocolFaultController(
    t, 'chora.m1-o4-repository-scan.v1', fault)
  await assert.rejects(inspectRepositoryAuthorityWithCapture({ repositoryRoot: root, capture }), pattern)
})

test('native repository read-files fails closed on a digest-mismatched response', async (t) => {
  const root = await repository(t)
  const capture = await protocolFaultController(
    t, 'chora.m1-o4-repository-read-files.v1', 'digest')
  await assert.rejects(inspectRepositoryAuthorityWithCapture({ repositoryRoot: root, capture }),
    /native read-files response digest drifted/)
})

test('production authority source does not pathname-traverse or reopen capture contents', async () => {
  const source = await readFile(new URL('./o4-repository-authority.mjs', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /\breaddir\b|walkClosure|validateGitControlTreeSafety|rejectUnsupportedGitTopology/)
  assert.doesNotMatch(source,
    /stableFile\(join\((?:root|captureRoot|gitRoot|adminRoot)|open\(join\((?:root|captureRoot|gitRoot|adminRoot)/)
  assert.match(source, /--protocol', protocol,[\s\S]*--controller-sha256'[\s\S]*--network', 'disabled',[\s\S]*--request-input', 'stdin'/)
})

test('rejects capture executable mode drift before execution', async (t) => {
  const root = await repository(t)
  const capture = await copiedCapture(t, 0o550)
  await assert.rejects(inspectRepositoryAuthorityWithCapture({ repositoryRoot: root, capture }),
    /mode drifted/)
})

test('rejects capture executable path replacement during execution', async (t) => {
  const root = await repository(t)
  const capture = await copiedCapture(t)
  const replacement = `${capture.executableFile}.replacement`
  await copyFile(capture.executableFile, replacement)
  await chmod(replacement, 0o500)
  const inspection = inspectRepositoryAuthorityWithCapture({ repositoryRoot: root, capture })
  const replacementTask = new Promise((resolvePromise, rejectPromise) => {
    setTimeout(() => rename(replacement, capture.executableFile)
      .then(resolvePromise, rejectPromise), 20)
  })
  await assert.rejects(inspection, /capture executable|capture controller/)
  await replacementTask
})

test('capture failure cleanup refuses a foreign session replacement', async (t) => {
  const root = await repository(t)
  const swapping = await swappingCaptureController(t)
  await assert.rejects(inspectRepositoryAuthorityWithCapture({
    repositoryRoot: root, capture: swapping.capture,
  }), (error) => aggregateContains(error, [
    /repository capture controller failed/,
    /owned cleanup path identity drifted/,
  ]))
  const paths = JSON.parse(await readFile(swapping.logPath, 'utf8'))
  try {
    assert.deepEqual(await readdir(paths.sessionRoot), [],
      'capture cleanup removed or populated the foreign session replacement')
    assert.deepEqual(await readdir(paths.displaced), [],
      'capture cleanup mutated the displaced owned session')
  } finally {
    await rm(paths.sessionRoot, { recursive: true, force: true })
    await rm(paths.displaced, { recursive: true, force: true })
  }
})

test('semantic authority survives a clean Git status that refreshes raw index stat-cache bytes', async (t) => {
  const root = await repository(t)
  const authority = await inspectRepositoryAuthority({ repositoryRoot: root })
  const indexPath = join(root, '.git', 'index')
  const before = await readFile(indexPath)
  await utimes(join(root, 'README.md'), new Date('2000-01-01T00:00:00Z'),
    new Date('2000-01-01T00:00:00Z'))
  const status = await refreshingGit(root, 'status', '--short')
  assert.equal(status.stdout, '')
  const after = await readFile(indexPath)
  assert.notEqual(Buffer.compare(before, after), 0, 'fixture did not refresh raw index bytes')
  const verified = await verifyRepositoryAuthority(authority)
  assert.equal(verified.repositoryClosureSha256, authority.repositoryClosureSha256)
  const restarted = await verifyRestartRepositoryAuthority(authority)
  assert.equal(restarted.repositoryClosureSha256, authority.repositoryClosureSha256)
})

test('semantic authority survives object repacking and commit-graph cache churn', async (t) => {
  const root = await repository(t)
  const authority = await inspectRepositoryAuthority({ repositoryRoot: root })
  await git(root, 'repack', '-ad')
  await git(root, 'pack-refs', '--all')
  await git(root, 'commit-graph', 'write', '--reachable')
  const verified = await verifyRepositoryAuthority(authority)
  assert.equal(verified.repositoryClosureSha256, authority.repositoryClosureSha256)
})

test('restart authority permits only a recognized Chora linked worktree', async (t) => {
  const root = await repository(t)
  const authority = await inspectRepositoryAuthority({ repositoryRoot: root })
  const sibling = join(dirname(root),
    `chora-task_018f3f4a-7b2c-7def-8abc-0123456789ab-review-${basename(root).toLowerCase()}`)
  t.after(() => rm(sibling, { recursive: true, force: true }))
  await git(root, 'worktree', 'add', '--detach', sibling, authority.repositoryCommit)
  const restarted = await verifyRestartRepositoryAuthority(authority)
  assert.equal(restarted.linkedWorktreeCount, 1)
})

for (const control of ['objects', 'HEAD', 'refs']) test(
  `initial authority rejects symlinked Git control outside the repository: ${control}`,
  async (t) => {
    const root = await repository(t)
    await externalizeGitControl(t, root, control)
    await assert.rejects(inspectRepositoryAuthority({ repositoryRoot: root }),
      /Git control contains a symlink|capture controller/)
  })

for (const control of ['objects', 'HEAD', 'refs']) test(
  `ordinary and restart authority reject post-binding symlinked Git control: ${control}`,
  async (t) => {
    const root = await repository(t)
    const authority = await inspectRepositoryAuthority({ repositoryRoot: root })
    await externalizeGitControl(t, root, control)
    await assert.rejects(verifyRepositoryAuthority(authority),
      /Git control contains a symlink|capture controller/)
    await assert.rejects(verifyRestartRepositoryAuthority(authority),
      /Git control contains a symlink|capture controller/)
  })

test('capture rejects a source directory change during inspection', async (t) => {
  const root = await repository(t)
  const inspection = inspectRepositoryAuthority({ repositoryRoot: root })
  await new Promise((resolvePromise) => setTimeout(resolvePromise, 20))
  await mkdir(join(root, 'appeared'))
  await assert.rejects(inspection, /repository|capture/)
})

test('capture rejects a source file change during inspection', async (t) => {
  const root = await repository(t)
  const inspection = inspectRepositoryAuthority({ repositoryRoot: root })
  await new Promise((resolvePromise) => setTimeout(resolvePromise, 20))
  await writeFile(join(root, 'README.md'), 'changed during capture\n')
  await assert.rejects(inspection, /repository|tracked|capture/)
})

for (const [name, createSpecial] of [
  ['FIFO', async (path) => execFileAsync('/usr/bin/mkfifo', [path])],
  ['symlink', async (path) => symlink('README.md', path)],
]) test(`capture rejects a source ${name} without hanging`, async (t) => {
  const root = await repository(t)
  await createSpecial(join(root, 'special'))
  const started = Date.now()
  await assert.rejects(inspectRepositoryAuthority({ repositoryRoot: root }),
    /capture controller|symlink|special/)
  assert(Date.now() - started < 5_000, `${name} rejection exceeded its nonblocking bound`)
})

test('initial snapshot validation is exact-keyed and tuple-size bounded', async (t) => {
  const root = await repository(t)
  const authority = await inspectRepositoryAuthority({ repositoryRoot: root })
  const unknown = structuredClone(authority.snapshot); unknown.untrusted = true
  assert.throws(() => validateRepositoryAuthoritySnapshot(unknown), /keys drifted/)
  const oversized = structuredClone(authority.snapshot)
  const template = oversized.worktree.find((entry) => entry.type === 'file')
  oversized.worktree = Array.from({ length: 10_001 }, (_, index) => ({
    ...template, path: `bounded/${String(index).padStart(5, '0')}`,
  }))
  assert.throws(() => validateRepositoryAuthoritySnapshot(oversized), /tuple bound/)
})

for (const [name, flag] of [
  ['assume-unchanged', '--assume-unchanged'],
  ['skip-worktree', '--skip-worktree'],
]) test(`rejects ${name} index concealment`, async (t) => {
  const root = await repository(t)
  await git(root, 'update-index', flag, 'README.md')
  await assert.rejects(inspectRepositoryAuthority({ repositoryRoot: root }),
    /assume-unchanged or skip-worktree/)
})

for (const [name, flag] of [
  ['assume-unchanged', '--assume-unchanged'],
  ['skip-worktree', '--skip-worktree'],
]) test(`ordinary and restart verification reject post-binding ${name}`, async (t) => {
  const root = await repository(t)
  const authority = await inspectRepositoryAuthority({ repositoryRoot: root })
  await git(root, 'update-index', flag, 'README.md')
  await assert.rejects(verifyRepositoryAuthority(authority),
    /assume-unchanged or skip-worktree/)
  await assert.rejects(verifyRestartRepositoryAuthority(authority),
    /assume-unchanged or skip-worktree/)
})

for (const [name, mutate, pattern] of [
  ['tracked-byte mutation', async (root) => writeFile(join(root, 'README.md'), 'changed\n'), /bytes drifted/],
  ['tracked-mode mutation', async (root) => chmod(join(root, 'README.md'), 0o755), /mode drifted/],
  ['untracked file', async (root) => writeFile(join(root, 'untracked'), 'x\n'), /untracked/],
  ['ignored file', async (root) => {
    await writeFile(join(root, '.gitignore'), 'ignored\n')
    await git(root, 'add', '.gitignore'); await git(root, 'commit', '-qm', 'ignore')
    await writeFile(join(root, 'ignored'), 'x\n')
  }, /untracked/],
  ['untracked symlink', async (root) => symlink('README.md', join(root, 'link')), /symlink/],
  ['tracked hardlink', async (root) => link(join(root, 'README.md'), join(root, 'alias')),
    /hard-linked/],
]) test(`rejects ${name}`, async (t) => {
  const root = await repository(t)
  await mutate(root)
  await assert.rejects(inspectRepositoryAuthority({ repositoryRoot: root }),
    new RegExp(`${pattern.source}|capture controller`))
})

for (const [name, key, value] of [
  ['external worktree', 'core.worktree', '/tmp/elsewhere'],
  ['SSH command', 'core.sshCommand', '/tmp/command'],
  ['alternate refs command', 'core.alternateRefsCommand', '/tmp/command'],
  ['promisor remote', 'remote.origin.promisor', 'true'],
  ['URL rewrite', 'url.file:///tmp/.insteadOf', 'https://'],
  ['clean filter', 'filter.o4.clean', '/tmp/command'],
  ['external diff', 'diff.o4.command', '/tmp/command'],
  ['submodule command topology', 'submodule.o4.url', '/tmp/repository'],
]) test(`rejects dangerous local config: ${name}`, async (t) => {
  const root = await repository(t)
  await git(root, 'config', key, value)
  await assert.rejects(inspectRepositoryAuthority({ repositoryRoot: root }),
    /repository config/)
})

for (const [name, mutate, pattern] of [
  ['config include', async (root) => writeFile(join(root, '.git', 'config'),
    `${await readFile(join(root, '.git', 'config'), 'utf8')}\n[include]\n\tpath = /tmp/forbidden\n`),
  /repository config/],
  ['commondir topology', async (root) => writeFile(join(root, '.git', 'commondir'), '../common\n'),
    /commondir.*forbidden/],
  ['alternates topology', async (root) => {
    await mkdir(join(root, '.git', 'objects', 'info'), { recursive: true })
    await writeFile(join(root, '.git', 'objects', 'info', 'alternates'), '/tmp/objects\n')
  }, /alternates.*forbidden/],
  ['grafts topology', async (root) => {
    await mkdir(join(root, '.git', 'info'), { recursive: true })
    await writeFile(join(root, '.git', 'info', 'grafts'), 'forbidden\n')
  }, /grafts.*forbidden/],
  ['promisor topology', async (root) => {
    await mkdir(join(root, '.git', 'objects', 'pack'), { recursive: true })
    await writeFile(join(root, '.git', 'objects', 'pack', 'pack-test.promisor'), '')
  }, /promisor object topology/],
  ['replace topology', async (root) => {
    const commit = (await git(root, 'rev-parse', 'HEAD')).stdout.trim()
    await mkdir(join(root, '.git', 'refs', 'replace'), { recursive: true })
    await writeFile(join(root, '.git', 'refs', 'replace', commit), `${commit}\n`)
  }, /replacement topology/],
  ['info attributes', async (root) => {
    await mkdir(join(root, '.git', 'info'), { recursive: true })
    await writeFile(join(root, '.git', 'info', 'attributes'), '* filter=external\n')
  }, /info\/attributes/],
]) test(`rejects ${name}`, async (t) => {
  const root = await repository(t)
  await mutate(root)
  await assert.rejects(inspectRepositoryAuthority({ repositoryRoot: root }), pattern)
})

for (const [name, mutate] of [
  ['worktree', async (root) => writeFile(join(root, 'README.md'), 'later\n')],
  ['mode', async (root) => chmod(join(root, 'README.md'), 0o755)],
  ['config', async (root) => git(root, 'config', 'user.name', 'Mutated')],
  ['index', async (root) => git(root, 'update-index', '--assume-unchanged', 'README.md')],
  ['ref', async (root) => {
    await writeFile(join(root, 'new'), 'new\n'); await git(root, 'add', 'new'); await git(root, 'commit', '-qm', 'new')
  }],
  ['object', async (root) => {
    const object = (await git(root, 'rev-parse', 'HEAD:README.md')).stdout.trim()
    const path = join(root, '.git', 'objects', object.slice(0, 2), object.slice(2))
    const bytes = await readFile(path); bytes[bytes.length - 1] ^= 1
    await chmod(path, 0o644); await writeFile(path, bytes)
  }],
]) test(`verify rejects post-publication ${name} drift`, async (t) => {
  const root = await repository(t)
  const authority = await inspectRepositoryAuthority({ repositoryRoot: root })
  await mutate(root)
  await assert.rejects(verifyRepositoryAuthority(authority), /repository|tracked|Git/)
})

test('verify rejects an isolated extra ref without worktree or HEAD drift', async (t) => {
  const root = await repository(t)
  const authority = await inspectRepositoryAuthority({ repositoryRoot: root })
  await git(root, 'update-ref', 'refs/heads/unexpected', authority.repositoryCommit)
  await assert.rejects(verifyRepositoryAuthority(authority), /closure|snapshot|authority/)
  await assert.rejects(verifyRestartRepositoryAuthority(authority), /semantic authority/)
})

test('rejects symlink and gitlink entries from the pinned HEAD tree', async (t) => {
  const root = await repository(t, { 'README.md': 'hello\n' })
  await symlink('README.md', join(root, 'tracked-link'))
  await git(root, 'add', 'tracked-link'); await git(root, 'commit', '-qm', 'symlink')
  await assert.rejects(inspectRepositoryAuthority({ repositoryRoot: root }),
    /symlink, gitlink|capture controller/)
})

test('rejects standalone tracked .gitmodules before any product Git consumer', async (t) => {
  const root = await repository(t, { 'README.md': 'hello\n' })
  await writeFile(join(root, '.gitmodules'), '[submodule "unused"]\n\tpath = unused\n\turl = /tmp/unused\n')
  await git(root, 'add', '.gitmodules'); await git(root, 'commit', '-qm', 'gitmodules')
  await assert.rejects(inspectRepositoryAuthority({ repositoryRoot: root }), /.gitmodules topology/)
})

test('rejects initial linked-worktree control topology', async (t) => {
  const root = await repository(t)
  await mkdir(join(root, '.git', 'worktrees'), { recursive: true })
  await writeFile(join(root, '.git', 'worktrees', 'unexpected'), 'x\n')
  await assert.rejects(inspectRepositoryAuthority({ repositoryRoot: root }), /initial repository worktrees/)
})

test('rejects active hooks but permits inert sample hooks', async (t) => {
  const root = await repository(t)
  await writeFile(join(root, '.git', 'hooks', 'pre-commit'), '#!/bin/sh\nexit 1\n', { mode: 0o755 })
  await assert.rejects(inspectRepositoryAuthority({ repositoryRoot: root }), /active hook topology/)
})

test('closure counts all Git control/object/ref bytes and rejects unexpected empty worktree topology', async (t) => {
  const root = await repository(t)
  await mkdir(join(root, 'empty'))
  await assert.rejects(inspectRepositoryAuthority({ repositoryRoot: root }), /untracked/)
  const objectDirectories = (await readdir(join(root, '.git', 'objects'))).filter((name) => /^[0-9a-f]{2}$/.test(name))
  assert(objectDirectories.length > 0)
})

test('capture helper cleanup refuses a foreign build-root replacement', async () => {
  const helper = await buildTestRepositoryCapture()
  const buildRoot = dirname(helper.capture.executableFile)
  const displaced = `${buildRoot}-displaced`
  await rename(buildRoot, displaced)
  await mkdir(buildRoot, { mode: 0o700 })
  try {
    await assert.rejects(helper.cleanup(), /owned cleanup path identity drifted/)
    assert.equal((await readdir(buildRoot)).length, 0,
      'identity-bound cleanup removed or populated the foreign replacement')
  } finally {
    await rm(buildRoot, { recursive: true, force: true })
    await rm(displaced, { recursive: true, force: true })
  }
})

test('capture helper build-failure cleanup refuses a foreign build-root replacement', async () => {
  let paths
  await assert.rejects(buildTestRepositoryCapture({
    afterBuildRootCreated: async ({ buildRoot }) => {
      const displaced = `${buildRoot}-displaced`
      await rename(buildRoot, displaced)
      await mkdir(buildRoot, { mode: 0o700 })
      paths = { buildRoot, displaced }
      throw new Error('injected capture-controller build failure')
    },
  }), (error) => aggregateContains(error, [
    /injected capture-controller build failure/,
    /atomic cleanup unavailable/,
  ]))
  try {
    assert.deepEqual(await readdir(paths.buildRoot), [],
      'build-failure cleanup removed or populated the foreign replacement')
    assert.deepEqual(await readdir(paths.displaced), [],
      'build-failure cleanup mutated the displaced owned root')
  } finally {
    await rm(paths.buildRoot, { recursive: true, force: true })
    await rm(paths.displaced, { recursive: true, force: true })
  }
})

test('capture helper uses an explicit bootstrap capability after a pre-build failure', async () => {
  let buildRoot
  await assert.rejects(buildTestRepositoryCapture({
    bootstrapCleanupCapability: captureFixture.capture,
    afterBuildRootCreated: async (value) => {
      buildRoot = value.buildRoot
      throw new Error('injected pre-build failure with bootstrap cleanup')
    },
  }), /injected pre-build failure with bootstrap cleanup/)
  await assert.rejects(lstat(buildRoot), { code: 'ENOENT' })
})

test('capture helper bootstrap cleanup preserves a swapped foreign build root', async () => {
  let paths
  await assert.rejects(buildTestRepositoryCapture({
    bootstrapCleanupCapability: captureFixture.capture,
    afterBuildRootCreated: async ({ buildRoot }) => {
      const displaced = `${buildRoot}-displaced`
      await rename(buildRoot, displaced)
      await mkdir(buildRoot, { mode: 0o700 })
      paths = { buildRoot, displaced }
      throw new Error('injected swapped pre-build failure')
    },
  }), (error) => aggregateContains(error, [
    /injected swapped pre-build failure/,
    /owned cleanup path identity drifted/,
  ]))
  try {
    assert.deepEqual(await readdir(paths.buildRoot), [],
      'bootstrap cleanup removed or populated the foreign build root')
    assert.deepEqual(await readdir(paths.displaced), [],
      'bootstrap cleanup mutated the displaced owned build root')
  } finally {
    await rm(paths.buildRoot, { recursive: true, force: true })
    await rm(paths.displaced, { recursive: true, force: true })
  }
})
