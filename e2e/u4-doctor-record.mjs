import { createHash } from 'node:crypto'
import { lstat, open, readFile } from 'node:fs/promises'
import { basename, isAbsolute, normalize, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const DOCTOR_RECORD_SCHEMA = 'chora.m1-u4-doctor-safe-record.v1'
export const U4_GROUPS = Object.freeze(['positive', 'implementation_gap', 'rejection_routes'])

const digestPattern = /^[0-9a-f]{64}$/
const safeOutputName = /^[A-Za-z0-9][A-Za-z0-9._-]{0,159}$/
const allowedDoctorFields = new Set([
  'status', 'failure', 'resourcesCreated', 'elapsed', 'inputFingerprint', 'modelProbe',
])
const recordFields = Object.freeze([
  'schemaVersion', 'group', 'inputFingerprint', 'installRootHash', 'dataRootHash',
  'port', 'passed', 'resourcesCreated', 'digest',
])

export function buildDoctorSafeRecord({ report, group, installRoot, dataRoot, port }) {
  assertPlainObject(report, 'doctor report')
  assertExactGroup(group)
  const install = exactAbsoluteRoot(installRoot, 'install root')
  const data = exactAbsoluteRoot(dataRoot, 'data root')
  assert(install !== data, 'doctor roots must be distinct')
  const loopbackPort = exactPort(port)

  for (const key of Object.keys(report)) {
    assert(allowedDoctorFields.has(key), 'doctor report contains an unexpected field')
  }
  assert(report.status === 'passed', 'doctor status is not passed')
  assert(report.resourcesCreated === false, 'doctor created resources')
  assertDigest(report.inputFingerprint, 'doctor input fingerprint')
  assert(report.failure === undefined || report.failure === null, 'passed doctor report contains a failure')
  if (report.elapsed !== undefined) {
    assert(typeof report.elapsed === 'number' && Number.isFinite(report.elapsed) && report.elapsed >= 0,
      'doctor elapsed value is invalid')
  }
  if (report.modelProbe !== undefined) validateModelProbe(report.modelProbe)

  const safe = {
    schemaVersion: DOCTOR_RECORD_SCHEMA,
    group,
    inputFingerprint: report.inputFingerprint,
    installRootHash: sha256Hex(install),
    dataRootHash: sha256Hex(data),
    port: loopbackPort,
    passed: true,
    resourcesCreated: false,
  }
  return Object.freeze({ ...safe, digest: sha256Hex(canonicalJSONStringify(safe)) })
}

export function assertDoctorSafeRecord(record) {
  assertPlainObject(record, 'Doctor safe record')
  assertExactKeys(record, recordFields, 'Doctor safe record')
  assert(record.schemaVersion === DOCTOR_RECORD_SCHEMA, 'Doctor safe record schema drift')
  assertExactGroup(record.group)
  for (const field of ['inputFingerprint', 'installRootHash', 'dataRootHash', 'digest']) {
    assertDigest(record[field], `Doctor safe record ${field}`)
  }
  assert(record.installRootHash !== record.dataRootHash, 'Doctor safe record roots are not distinct')
  exactPort(record.port)
  assert(record.passed === true && record.resourcesCreated === false,
    'Doctor safe record is not a zero-resource pass')
  const { digest, ...safe } = record
  assert(digest === sha256Hex(canonicalJSONStringify(safe)), 'Doctor safe record digest drift')
  return record
}

export async function persistDoctorSafeRecord(record, outputPath) {
  assertDoctorSafeRecord(record)
  const output = exactAbsoluteFile(outputPath, 'Doctor record output')
  assert(safeOutputName.test(basename(output)), 'Doctor record output basename is unsafe')
  const handle = await open(output, 'wx', 0o600)
  try {
    await handle.writeFile(`${JSON.stringify(record, null, 2)}\n`, 'utf8')
    await handle.sync()
    await handle.chmod(0o600)
  } finally {
    await handle.close()
  }
}

export function canonicalJSONStringify(value) {
  return JSON.stringify(canonicalValue(value))
}

export function sha256Hex(value) {
  return createHash('sha256').update(value).digest('hex')
}

function canonicalValue(value) {
  if (value === null || typeof value === 'boolean' || typeof value === 'string') return value
  if (typeof value === 'number') {
    assert(Number.isFinite(value), 'canonical JSON contains a non-finite number')
    return value
  }
  if (Array.isArray(value)) return value.map(canonicalValue)
  assertPlainObject(value, 'canonical JSON value')
  return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canonicalValue(value[key])]))
}

function validateModelProbe(value) {
  assertPlainObject(value, 'doctor model probe')
  assertExactKeys(value, ['maxAttempts', 'attempts'], 'doctor model probe')
  assert(Number.isSafeInteger(value.maxAttempts) && value.maxAttempts >= 1 && value.maxAttempts <= 3,
    'doctor model probe max attempts is invalid')
  assert(Array.isArray(value.attempts) && value.attempts.length >= 1 && value.attempts.length <= value.maxAttempts,
    'doctor model probe attempts are invalid')
  for (const attempt of value.attempts) {
    assertPlainObject(attempt, 'doctor model probe attempt')
    const keys = Object.keys(attempt).sort()
    const expected = attempt.statusCode === undefined
      ? ['attempt', 'outcome', 'retried', 'retryable']
      : ['attempt', 'outcome', 'retried', 'retryable', 'statusCode']
    assertSameArray(keys, expected, 'doctor model probe attempt fields drifted')
    assert(Number.isSafeInteger(attempt.attempt) && attempt.attempt >= 1 && attempt.attempt <= value.maxAttempts,
      'doctor model probe attempt number is invalid')
    assert(typeof attempt.outcome === 'string' && /^[a-z][a-z0-9_-]{0,63}$/.test(attempt.outcome),
      'doctor model probe outcome is unsafe')
    assert(typeof attempt.retryable === 'boolean' && typeof attempt.retried === 'boolean',
      'doctor model probe retry audit is invalid')
    if (attempt.statusCode !== undefined) {
      assert(Number.isSafeInteger(attempt.statusCode) && attempt.statusCode >= 100 && attempt.statusCode <= 599,
        'doctor model probe status code is invalid')
    }
  }
}

function exactAbsoluteRoot(value, label) {
  const root = exactAbsoluteFile(value, label)
  assert(root !== '/', `${label} cannot be the filesystem root`)
  return root
}

function exactAbsoluteFile(value, label) {
  assert(typeof value === 'string' && value.length > 1 && isAbsolute(value) && normalize(value) === value && resolve(value) === value,
    `${label} must be an absolute normalized path`)
  assert(!value.includes('\0'), `${label} contains a null byte`)
  return value
}

function exactPort(value) {
  const port = typeof value === 'string' && /^[0-9]+$/.test(value) ? Number(value) : value
  assert(Number.isSafeInteger(port) && port >= 1 && port <= 65535, 'loopback port is invalid')
  return port
}

function assertExactGroup(value) {
  assert(U4_GROUPS.includes(value), 'Doctor evidence group is invalid')
}

function assertDigest(value, label) {
  assert(typeof value === 'string' && digestPattern.test(value), `${label} is not a lowercase SHA-256 digest`)
}

function assertPlainObject(value, label) {
  assert(value !== null && typeof value === 'object' && !Array.isArray(value) &&
    (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null),
  `${label} must be a plain object`)
}

function assertExactKeys(value, expected, label) {
  assertSameArray(Object.keys(value).sort(), [...expected].sort(), `${label} fields drifted`)
}

function assertSameArray(actual, expected, message) {
  assert(actual.length === expected.length && actual.every((value, index) => value === expected[index]), message)
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

async function strictJSONFile(path, label) {
  const input = exactAbsoluteFile(path, label)
  const info = await lstat(input)
  assert(info.isFile() && !info.isSymbolicLink() && info.size <= 256 * 1024, `${label} must be one bounded regular file`)
  let value
  try { value = JSON.parse(await readFile(input, 'utf8')) } catch { throw new Error(`${label} is not JSON`) }
  return value
}

function parseCLI(argv, env) {
  const allowed = new Set(['--doctor', '--group', '--install-root', '--data-root', '--port', '--output'])
  const values = new Map()
  for (let index = 0; index < argv.length; index += 2) {
    const flag = argv[index]
    assert(allowed.has(flag) && index + 1 < argv.length && !values.has(flag), 'Doctor record arguments are invalid')
    values.set(flag, argv[index + 1])
  }
  const get = (flag, name, required = true) => {
    const value = values.get(flag) ?? env[name]
    if (required) assert(typeof value === 'string' && value.length > 0, 'Doctor record argument is missing')
    return value
  }
  return {
    doctor: get('--doctor', 'CHORA_U4_DOCTOR_JSON'),
    group: get('--group', 'CHORA_U4_GROUP'),
    installRoot: get('--install-root', 'CHORA_U4_INSTALL_ROOT'),
    dataRoot: get('--data-root', 'CHORA_U4_DATA_ROOT'),
    port: get('--port', 'CHORA_U4_PORT'),
    output: get('--output', 'CHORA_U4_DOCTOR_RECORD_OUTPUT', false),
  }
}

async function main() {
  try {
    const input = parseCLI(process.argv.slice(2), process.env)
    const report = await strictJSONFile(input.doctor, 'Doctor JSON input')
    const record = buildDoctorSafeRecord({ report, ...input })
    if (input.output !== undefined) await persistDoctorSafeRecord(record, input.output)
    process.stdout.write(`${JSON.stringify(record)}\n`)
  } catch {
    process.stdout.write(`${JSON.stringify({ schemaVersion: DOCTOR_RECORD_SCHEMA, passed: false })}\n`)
    process.exitCode = 1
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main()
