import { isAbsolute, join, normalize, resolve } from 'node:path'

export const DARWIN_AF_UNIX_PATH_MAX_BYTES = 103

const limaSocketNames = Object.freeze([
  'ha.sock',
  'ssh.sock',
  // Lima validates this exact OpenSSH ControlMaster temporary form before
  // instance creation/clone; checking only ssh.sock undercounts 17 bytes.
  'ssh.sock.1234567890123456',
  'serial.sock',
  'user-v2.sock',
])

function fail(message) { throw new Error(message) }

function utf8BytesForExactPath(path, label) {
  for (let index = 0; index < path.length; index++) {
    const codeUnit = path.charCodeAt(index)
    if (codeUnit <= 0x1f || codeUnit === 0x7f) fail(
      `${label} contains a C0 or DEL control character`)
    if (codeUnit >= 0xd800 && codeUnit <= 0xdbff) {
      const next = path.charCodeAt(index + 1)
      if (!(next >= 0xdc00 && next <= 0xdfff)) fail(
        `${label} contains an unpaired UTF-16 surrogate`)
      index++
    } else if (codeUnit >= 0xdc00 && codeUnit <= 0xdfff) fail(
      `${label} contains an unpaired UTF-16 surrogate`)
  }

  const encoded = Buffer.from(path, 'utf8')
  if (encoded.toString('utf8') !== path) fail(
    `${label} does not round-trip through UTF-8 exactly`)
  return encoded
}

export function assertDarwinUnixSocketPath(path, label) {
  if (typeof path !== 'string') fail(`${label} must be exact absolute`)
  const encoded = utf8BytesForExactPath(path, label)
  if (!isAbsolute(path) || normalize(path) !== path || resolve(path) !== path)
    fail(`${label} must be exact absolute`)
  const bytes = encoded.length
  if (bytes > DARWIN_AF_UNIX_PATH_MAX_BYTES) fail(
    `${label} is ${bytes} bytes; Darwin AF_UNIX maximum is ${DARWIN_AF_UNIX_PATH_MAX_BYTES} bytes`)
  return Object.freeze({ label, path, bytes })
}

export function o4ColimaTopology(manifest) {
  const colimaHome = manifest?.roots?.colimaHome
  const profileName = manifest?.engine?.profileName
  const contextName = manifest?.engine?.contextName
  if (typeof colimaHome !== 'string' || typeof profileName !== 'string' ||
    typeof contextName !== 'string') fail('O4 Colima topology bindings are incomplete')
  if (contextName !== `colima-${profileName}`) fail(
    'O4 Colima Lima-instance identity is not the exact context name')
  const limaHome = join(colimaHome, '_lima')
  return Object.freeze({
    profileRoot: join(colimaHome, profileName),
    limaHome,
    limaInstanceName: contextName,
    limaInstanceRoot: join(limaHome, contextName),
    limaDiskRoot: join(limaHome, '_disks', contextName),
    storeFile: join(colimaHome, '_store', `${contextName}.json`),
    endpointSocketPath: join(colimaHome, profileName, 'docker.sock'),
  })
}

export function o4DarwinUnixSocketPaths(manifest) {
  const colimaHome = manifest?.roots?.colimaHome
  const temporaryRoot = manifest?.roots?.temporaryRoot
  const profileName = manifest?.engine?.profileName
  const contextName = manifest?.engine?.contextName
  const endpointSocketPath = manifest?.engine?.endpointSocketPath
  const runnerControlSocketPath = manifest?.runner?.controlSocketPath
  if (typeof colimaHome !== 'string' || typeof temporaryRoot !== 'string' ||
    typeof profileName !== 'string' || typeof contextName !== 'string' ||
    typeof endpointSocketPath !== 'string' ||
    typeof runnerControlSocketPath !== 'string') fail('O4 Darwin AF_UNIX bindings are incomplete')

  const topology = o4ColimaTopology(manifest)
  if (endpointSocketPath !== topology.endpointSocketPath) fail(
    'fresh Engine endpoint is not the exact COLIMA_HOME/profile Docker socket')

  return Object.freeze([
    Object.freeze({ label: 'fresh Engine Docker endpoint', path: endpointSocketPath }),
    ...limaSocketNames.map((name) => Object.freeze({
      label: `Lima ${name}`, path: join(topology.limaInstanceRoot, name),
    })),
    Object.freeze({ label: 'O4 Runner control socket', path: runnerControlSocketPath }),
    Object.freeze({
      label: 'O4 sandbox probe socket',
      path: join(temporaryRoot, 'sandbox-probe-2147483647.sock'),
    }),
  ])
}

export function assertO4DarwinUnixSocketPaths(manifest) {
  return Object.freeze(o4DarwinUnixSocketPaths(manifest).map(({ label, path }) =>
    assertDarwinUnixSocketPath(path, label)))
}
