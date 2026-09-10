import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import test from 'node:test'
import {
  DARWIN_AF_UNIX_PATH_MAX_BYTES,
  assertDarwinUnixSocketPath,
  assertO4DarwinUnixSocketPaths,
  o4ColimaTopology,
} from './o4-darwin-unix-sockets.mjs'

function manifest(socketParent = '/private/tmp/chora-o4-sockets-01234567') {
  const profileName = 'chora-o4-r4'
  const colimaHome = join(socketParent, 'c')
  return {
    engine: {
      profileName,
      contextName: `colima-${profileName}`,
      endpointSocketPath: join(colimaHome, profileName, 'docker.sock'),
    },
    runner: { controlSocketPath: join(socketParent, 'r', 'o4-runner.sock') },
    roots: { colimaHome, temporaryRoot: join(socketParent, 't') },
  }
}

test('accepts one short manifest-bound socket topology including fixed Lima paths', () => {
  const value = manifest()
  const before = structuredClone(value)
  const observed = assertO4DarwinUnixSocketPaths(value)
  assert.deepEqual(observed.map((item) => item.label), [
    'fresh Engine Docker endpoint', 'Lima ha.sock', 'Lima ssh.sock',
    'Lima ssh.sock.1234567890123456', 'Lima serial.sock', 'Lima user-v2.sock',
    'O4 Runner control socket', 'O4 sandbox probe socket',
  ])
  assert.deepEqual(observed.map((item) => item.path), [
    value.engine.endpointSocketPath,
    ...['ha.sock', 'ssh.sock', 'ssh.sock.1234567890123456', 'serial.sock', 'user-v2.sock'].map((name) =>
      join(value.roots.colimaHome, '_lima', value.engine.contextName, name)),
    value.runner.controlSocketPath,
    join(value.roots.temporaryRoot, 'sandbox-probe-2147483647.sock'),
  ])
  assert.equal(observed.every((item) => item.bytes <= DARWIN_AF_UNIX_PATH_MAX_BYTES), true)
  assert.equal(Object.isFrozen(observed), true)
  assert.equal(observed.every(Object.isFrozen), true)
  assert.deepEqual(value, before)
})

test('derives every Colima/Lima profile path from its authoritative identity', () => {
  const value = manifest('/private/tmp/chora-o4-topology')
  assert.deepEqual(o4ColimaTopology(value), {
    profileRoot: join(value.roots.colimaHome, value.engine.profileName),
    limaHome: join(value.roots.colimaHome, '_lima'),
    limaInstanceName: value.engine.contextName,
    limaInstanceRoot: join(value.roots.colimaHome, '_lima', value.engine.contextName),
    limaDiskRoot: join(value.roots.colimaHome, '_lima', '_disks', value.engine.contextName),
    storeFile: join(value.roots.colimaHome, '_store', `${value.engine.contextName}.json`),
    endpointSocketPath: value.engine.endpointSocketPath,
  })
})

test('accepts exactly 103 UTF-8 bytes and rejects exactly 104', () => {
  const exact = `/${'x'.repeat(102)}`
  const observed = assertDarwinUnixSocketPath(exact, 'exact-limit socket')
  assert.equal(observed.path, exact)
  assert.equal(observed.bytes, 103)
  assert.throws(() => assertDarwinUnixSocketPath(`/${'x'.repeat(103)}`, 'over-limit socket'),
    /over-limit socket is 104 bytes; Darwin AF_UNIX maximum is 103 bytes/)
})

test('counts UTF-8 bytes and preserves well-formed JavaScript strings exactly', () => {
  const decomposedAndSupplementary = `/private/tmp/e\u0301-\u{1f642}.sock`
  const observed = assertDarwinUnixSocketPath(decomposedAndSupplementary, 'Unicode socket')
  assert.equal(observed.path, decomposedAndSupplementary)
  assert.equal(Buffer.from(observed.path, 'utf8').toString('utf8'), decomposedAndSupplementary)

  assert.throws(() => assertO4DarwinUnixSocketPaths(manifest(
    `/private/tmp/${'x'.repeat(88)}`)), /Darwin AF_UNIX maximum is 103 bytes/)
  const multibyte = `/private/tmp/${'界'.repeat(31)}.sock`
  assert.equal(multibyte.length < DARWIN_AF_UNIX_PATH_MAX_BYTES, true)
  assert.throws(() => assertDarwinUnixSocketPath(multibyte, 'multibyte socket'),
    /Darwin AF_UNIX maximum is 103 bytes/)
})

test('rejects NUL, every other C0 control, and DEL before the byte limit', () => {
  const controls = [...Array.from({ length: 32 }, (_, codePoint) => codePoint), 0x7f]
  for (const codePoint of controls) {
    const path = `/${'x'.repeat(103)}${String.fromCharCode(codePoint)}`
    assert.throws(() => assertDarwinUnixSocketPath(path,
      `U+${codePoint.toString(16).padStart(4, '0')} socket`),
    /contains a C0 or DEL control character/)
  }
})

test('rejects every form of unpaired UTF-16 surrogate before the byte limit', () => {
  for (const suffix of ['\ud800', '\ud800x', '\udfff', '\udfff\ud800']) {
    assert.throws(() => assertDarwinUnixSocketPath(`/${'x'.repeat(103)}${suffix}`,
      'unpaired-surrogate socket'), /contains an unpaired UTF-16 surrogate/)
  }
})

test('rejects non-canonical socket paths', () => {
  for (const path of [
    'private/tmp/o4.sock',
    '/private/tmp/../tmp/o4.sock',
    '/private//tmp/o4.sock',
    '/private/tmp/o4.sock/',
  ]) assert.throws(() => assertDarwinUnixSocketPath(path, 'non-canonical socket'),
    /must be exact absolute/)
})

test('rejects a claimed endpoint that is not COLIMA_HOME/profile/docker.sock', () => {
  const value = manifest()
  value.engine.endpointSocketPath = join(value.roots.colimaHome, 'docker.sock')
  assert.throws(() => assertO4DarwinUnixSocketPaths(value),
    /exact COLIMA_HOME\/profile Docker socket/)
})

test('rejects unsafe values after they are spliced into derived paths', () => {
  const unsafeProfile = manifest()
  unsafeProfile.engine.profileName = `chora-o4-\ud800`
  unsafeProfile.engine.contextName = `colima-${unsafeProfile.engine.profileName}`
  unsafeProfile.engine.endpointSocketPath = join(unsafeProfile.roots.colimaHome,
    unsafeProfile.engine.profileName, 'docker.sock')
  assert.throws(() => assertO4DarwinUnixSocketPaths(unsafeProfile),
    /fresh Engine Docker endpoint contains an unpaired UTF-16 surrogate/)

  const unsafeTemporaryRoot = manifest()
  unsafeTemporaryRoot.roots.temporaryRoot += '\u007f'
  assert.throws(() => assertO4DarwinUnixSocketPaths(unsafeTemporaryRoot),
    /O4 sandbox probe socket contains a C0 or DEL control character/)
})

test('accounts for Lima OpenSSH ControlMaster temporary suffix', () => {
  const value = manifest('/private/tmp')
  const fixedPrefixBytes = Buffer.byteLength(join(value.roots.colimaHome, '_lima',
    value.engine.contextName), 'utf8') + 1
  const profileGrowth = DARWIN_AF_UNIX_PATH_MAX_BYTES - fixedPrefixBytes -
    Buffer.byteLength('ssh.sock', 'utf8')
  value.engine.profileName += 'x'.repeat(profileGrowth)
  value.engine.contextName = `colima-${value.engine.profileName}`
  value.engine.endpointSocketPath = join(value.roots.colimaHome, value.engine.profileName,
    'docker.sock')

  assert.equal(Buffer.byteLength(join(value.roots.colimaHome, '_lima', value.engine.contextName,
    'ssh.sock'), 'utf8'), DARWIN_AF_UNIX_PATH_MAX_BYTES)
  assert.throws(() => assertO4DarwinUnixSocketPaths(value),
    /Lima ssh\.sock\.1234567890123456 is 120 bytes; Darwin AF_UNIX maximum is 103 bytes/)
})

test('freeze, prepare, materialize, and total execution boundaries all invoke the shared gate', async () => {
  const cases = [
    ['o4-real-input-freezer.mjs', /assertDarwinUnixSocketPath/, /assertO4DarwinUnixSocketPaths/],
    ['o4-inputs-preparer.mjs', /assertDarwinUnixSocketPath/],
    ['o4-local-offline-materializer.mjs', /assertO4DarwinUnixSocketPaths/],
    ['o4-total-real.mjs', /assertO4DarwinUnixSocketPaths/],
  ]
  for (const [name, ...patterns] of cases) {
    const source = await readFile(new URL(name, import.meta.url), 'utf8')
    for (const pattern of patterns) assert.match(source, pattern, `${name} lacks ${pattern}`)
  }
})
