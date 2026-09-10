import { createHash } from 'node:crypto'
import { spawn as nodeSpawn } from 'node:child_process'
import { createReadStream } from 'node:fs'
import { readFile as nodeReadFile, stat as nodeStat } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { isAbsolute } from 'node:path'

export const CONFIG_SCHEMA = 'chora.resource-observer-config.v1'
export const FINGERPRINT_SCHEMA = 'chora.resource-fingerprint.v1'
export const OBSERVATION_SCHEMA = 'chora.resource-check-observation.v1'
export const FINGERPRINT_ALGORITHM = 'sha256-resource-patch-v1'

const MAX_JSON_BYTES = 1 << 20
const MAX_HELPER_BYTES = 512 * 1024 * 1024
const DEFAULT_TIMEOUT_MS = 10_000
const OBSERVATION_KEY = 'choraResourceCheckObservation'
const SHA256_HEX = /^[0-9a-f]{64}$/
const ownSourcePath = fileURLToPath(import.meta.url)

function sha256Hex(bytes) {
  return createHash('sha256').update(bytes).digest('hex')
}

function exactKeys(value, keys) {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return false
  const actual = Object.keys(value).sort()
  const expected = [...keys].sort()
  return actual.length === expected.length && actual.every((key, index) => key === expected[index])
}

function validConfig(value) {
  return exactKeys(value, ['schema', 'attemptId', 'taskRoot', 'resources']) &&
    value.schema === CONFIG_SCHEMA &&
    typeof value.attemptId === 'string' && value.attemptId.length > 0 && value.attemptId.length <= 128 &&
    typeof value.taskRoot === 'string' && isAbsolute(value.taskRoot) && value.taskRoot.length <= 4096 &&
    Array.isArray(value.resources)
}

function validRelativeDirectory(value) {
  if (typeof value !== 'string' || value.length === 0 || value.length > 4096 || value.includes('\0') || isAbsolute(value)) {
    return false
  }
  if (value === '.') return true
  if (value.includes('\\')) return false
  const parts = value.split('/')
  return parts.every((part) => part !== '' && part !== '.' && part !== '..')
}

function validFingerprintResponse(value) {
  return exactKeys(value, ['schema', 'algorithm', 'repoId', 'workingDirectory', 'commandDigest', 'fingerprint', 'helperSha256']) &&
    value.schema === FINGERPRINT_SCHEMA && value.algorithm === FINGERPRINT_ALGORITHM &&
    typeof value.repoId === 'string' && value.repoId.length > 0 && value.repoId.length <= 128 &&
    validRelativeDirectory(value.workingDirectory) &&
    typeof value.commandDigest === 'string' && SHA256_HEX.test(value.commandDigest) &&
    typeof value.fingerprint === 'string' && SHA256_HEX.test(value.fingerprint) &&
    typeof value.helperSha256 === 'string' && SHA256_HEX.test(value.helperSha256)
}

function sameFingerprintAuthority(left, right) {
  return left.schema === right.schema && left.algorithm === right.algorithm &&
    left.repoId === right.repoId && left.workingDirectory === right.workingDirectory &&
    left.commandDigest === right.commandDigest && left.helperSha256 === right.helperSha256
}

function canonicalJSON(value, seen = new Set()) {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value)
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new TypeError('non-finite number')
    return JSON.stringify(value)
  }
  if (typeof value !== 'object' || seen.has(value)) throw new TypeError('unsupported input')
  seen.add(value)
  let encoded
  if (Array.isArray(value)) {
    encoded = `[${value.map((item) => canonicalJSON(item, seen)).join(',')}]`
  } else {
    const prototype = Object.getPrototypeOf(value)
    if (prototype !== Object.prototype && prototype !== null) throw new TypeError('unsupported input object')
    encoded = `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key], seen)}`).join(',')}}`
  }
  seen.delete(value)
  return encoded
}

function inputDigest(input) {
  const encoded = canonicalJSON(input)
  if (Buffer.byteLength(encoded) > MAX_JSON_BYTES) throw new RangeError('tool input too large')
  return sha256Hex(encoded)
}

function withoutReservedObservation(details) {
  if (details === undefined) return undefined
  if (details === null || typeof details !== 'object' || Array.isArray(details)) return details
  if (!Object.hasOwn(details, OBSERVATION_KEY)) return details
  const copy = { ...details }
  delete copy[OBSERVATION_KEY]
  return copy
}

function parseBoundedJSON(bytes) {
  if (!Buffer.isBuffer(bytes)) bytes = Buffer.from(bytes)
  if (bytes.length === 0 || bytes.length > MAX_JSON_BYTES) throw new Error('invalid bounded JSON')
  return JSON.parse(bytes.toString('utf8'))
}

export function runFingerprintHelper(helperPath, request, { spawn = nodeSpawn, signal } = {}) {
  if (typeof helperPath !== 'string' || !isAbsolute(helperPath)) return Promise.reject(new Error('invalid helper path'))
  const input = Buffer.from(JSON.stringify(request))
  if (input.length === 0 || input.length > MAX_JSON_BYTES) return Promise.reject(new Error('helper request too large'))

  return new Promise((resolve, reject) => {
    let settled = false
    let overflow = false
    const stdout = []
    let stdoutBytes = 0
    let stderrBytes = 0
    const child = spawn(helperPath, ['internal-resource-fingerprint'], {
      shell: false,
      stdio: ['pipe', 'pipe', 'pipe'],
      signal,
    })
    const finish = (error, value) => {
      if (settled) return
      settled = true
      if (error) reject(error)
      else resolve(value)
    }
    child.once('error', (error) => finish(error))
    child.stdout.on('data', (chunk) => {
      stdoutBytes += chunk.length
      if (stdoutBytes > MAX_JSON_BYTES) {
        overflow = true
        child.kill()
        return
      }
      stdout.push(chunk)
    })
    child.stderr.on('data', (chunk) => {
      stderrBytes += chunk.length
      if (stderrBytes > MAX_JSON_BYTES) {
        overflow = true
        child.kill()
      }
    })
    child.once('close', (code, childSignal) => {
      if (overflow) return finish(new Error('helper output too large'))
      if (code !== 0 || childSignal !== null) return finish(new Error('helper failed'))
      try {
        finish(undefined, parseBoundedJSON(Buffer.concat(stdout)))
      } catch (error) {
        finish(error)
      }
    })
    child.stdin.once('error', (error) => finish(error))
    child.stdin.end(input)
  })
}

export async function streamFileSHA256(path, signal) {
  if (typeof path !== 'string' || !isAbsolute(path) || signal?.aborted) throw new Error('invalid helper file')
  const info = await nodeStat(path)
  if (!info.isFile() || info.size <= 0 || info.size > MAX_HELPER_BYTES || signal?.aborted) throw new Error('invalid helper file')
  const hash = createHash('sha256')
  let size = 0
  for await (const chunk of createReadStream(path, { signal })) {
    size += chunk.length
    if (size > MAX_HELPER_BYTES) throw new Error('helper file too large')
    hash.update(chunk)
  }
  if (size !== info.size || signal?.aborted) throw new Error('helper file changed while hashing')
  return hash.digest('hex')
}

async function boundedInvocation(invoke, request, signal, timeoutMs, helperIdentity) {
  if (signal?.aborted) throw new Error('aborted')
  const controller = new AbortController()
  let timedOut = false
  const abort = () => controller.abort(signal?.reason)
  signal?.addEventListener('abort', abort, { once: true })
  const timer = setTimeout(() => {
    timedOut = true
    controller.abort(new Error('helper timeout'))
  }, timeoutMs)
  try {
    const result = await Promise.race([
      (async () => {
        const before = await helperIdentity.hash(helperIdentity.path, controller.signal)
        if (before !== helperIdentity.expected) throw new Error('helper identity mismatch')
        const value = await invoke(request, controller.signal)
        const after = await helperIdentity.hash(helperIdentity.path, controller.signal)
        if (after !== helperIdentity.expected) throw new Error('helper identity mismatch')
        return value
      })(),
      new Promise((_, reject) => controller.signal.addEventListener('abort', () => reject(new Error(timedOut ? 'helper timeout' : 'aborted')), { once: true })),
    ])
    if (controller.signal.aborted) throw new Error(timedOut ? 'helper timeout' : 'aborted')
    if (!validFingerprintResponse(result) || result.helperSha256 !== helperIdentity.expected) throw new Error('invalid helper response')
    return result
  } finally {
    clearTimeout(timer)
    signal?.removeEventListener('abort', abort)
  }
}

export async function installResourceCheckObserver(pi, options = {}) {
  const env = options.env ?? process.env
  const readFile = options.readFile ?? nodeReadFile
  const sourcePath = options.sourcePath ?? ownSourcePath
  const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS
  const configPath = env.CHORA_CHECK_OBSERVER_CONFIG
  if (!configPath) return false
  if (!isAbsolute(configPath) || !isAbsolute(env.CHORA_CHECK_OBSERVER_HELPER ?? '') ||
      !SHA256_HEX.test(env.CHORA_CHECK_OBSERVER_CONFIG_SHA256 ?? '') ||
      !SHA256_HEX.test(env.CHORA_CHECK_OBSERVER_SHA256 ?? '') ||
      !SHA256_HEX.test(env.CHORA_CHECK_OBSERVER_HELPER_SHA256 ?? '') ||
      !SHA256_HEX.test(env.CHORA_CHECK_OBSERVER_RUNTIME_FINGERPRINT ?? '') ||
      !Number.isInteger(timeoutMs) || timeoutMs <= 0 || timeoutMs > DEFAULT_TIMEOUT_MS) return false

  let configBytes
  let sourceBytes
  try {
    const loaded = await Promise.all([readFile(configPath), readFile(sourcePath)])
    configBytes = loaded[0]
    sourceBytes = loaded[1]
  } catch {
    return false
  }
  if (configBytes.length === 0 || configBytes.length > MAX_JSON_BYTES || sourceBytes.length === 0 || sourceBytes.length > MAX_JSON_BYTES ||
      sha256Hex(configBytes) !== env.CHORA_CHECK_OBSERVER_CONFIG_SHA256 ||
      sha256Hex(sourceBytes) !== env.CHORA_CHECK_OBSERVER_SHA256) return false

  let config
  try {
    config = parseBoundedJSON(configBytes)
  } catch {
    return false
  }
  if (!validConfig(config)) return false

  const invoke = options.invokeHelper ?? ((request, signal) =>
    runFingerprintHelper(env.CHORA_CHECK_OBSERVER_HELPER, request, { signal, spawn: options.spawn ?? nodeSpawn }))
  const helperIdentity = {
    path: env.CHORA_CHECK_OBSERVER_HELPER,
    expected: env.CHORA_CHECK_OBSERVER_HELPER_SHA256,
    hash: options.hashHelperFile ?? streamFileSHA256,
  }
  let turn
  const clear = () => { turn = undefined }

  pi.on('session_start', clear)
  pi.on('session_shutdown', clear)
  pi.on('turn_start', (event) => {
    turn = { turnIndex: event.turnIndex, calls: new Map(), activeIds: new Set(), duplicateIds: new Set(), ambiguous: false }
  })
  pi.on('turn_end', clear)
  pi.on('tool_call', async (event, ctx) => {
    if (!turn) return
    const currentTurn = turn
    const toolCallId = typeof event.toolCallId === 'string' ? event.toolCallId : ''
    for (const activeId of currentTurn.activeIds) {
      const active = currentTurn.calls.get(activeId)
      if (active) active.isolatedWindow = false
    }
    if (!toolCallId || currentTurn.calls.has(toolCallId)) {
      currentTurn.ambiguous = true
      if (toolCallId) {
        currentTurn.duplicateIds.add(toolCallId)
        const duplicate = currentTurn.calls.get(toolCallId)
        if (duplicate) duplicate.isolatedWindow = false
      }
      return
    }
    const record = {
      toolName: event.toolName,
      inputHash: '',
      command: '',
      start: undefined,
      isolatedWindow: currentTurn.activeIds.size === 0 && !currentTurn.ambiguous,
    }
    currentTurn.calls.set(toolCallId, record)
    currentTurn.activeIds.add(toolCallId)
    try {
      record.inputHash = inputDigest(event.input)
      if (event.toolName !== 'bash' || typeof event.input?.command !== 'string' || event.input.command.length === 0) return
      record.command = event.input.command
      record.start = await boundedInvocation(invoke, { config, command: record.command }, ctx?.signal, timeoutMs, helperIdentity)
    } catch {
      record.start = undefined
    }
  })
  pi.on('tool_result', async (event, ctx) => {
    const cleanDetails = withoutReservedObservation(event.details)
    const sanitize = cleanDetails === event.details ? undefined : { details: cleanDetails }
    const currentTurn = turn
    const toolCallId = typeof event.toolCallId === 'string' ? event.toolCallId : ''
    if (!currentTurn || !toolCallId) return sanitize
    const record = currentTurn.calls.get(toolCallId)
    try {
      if (event.toolName !== 'bash' || event.isError === true || currentTurn.ambiguous ||
          currentTurn.duplicateIds.has(toolCallId) || !record || record.toolName !== 'bash' || !record.start ||
          !record.isolatedWindow || currentTurn.activeIds.size !== 1 || !currentTurn.activeIds.has(toolCallId) ||
          typeof event.input?.command !== 'string') return sanitize
      if (event.input.command !== record.command || inputDigest(event.input) !== record.inputHash) return sanitize
      const end = await boundedInvocation(invoke, { config, command: record.command }, ctx?.signal, timeoutMs, helperIdentity)
      if (turn !== currentTurn || !record.isolatedWindow || currentTurn.activeIds.size !== 1 ||
          !currentTurn.activeIds.has(toolCallId) || !sameFingerprintAuthority(record.start, end) ||
          record.start.fingerprint !== end.fingerprint) return sanitize
      if (cleanDetails !== undefined && (cleanDetails === null || typeof cleanDetails !== 'object' || Array.isArray(cleanDetails))) return sanitize
      return {
        details: {
          ...(cleanDetails ?? {}),
          [OBSERVATION_KEY]: {
            schema: OBSERVATION_SCHEMA,
            algorithm: FINGERPRINT_ALGORITHM,
            observerSha256: env.CHORA_CHECK_OBSERVER_SHA256,
            helperSha256: end.helperSha256,
            runtimeFingerprint: env.CHORA_CHECK_OBSERVER_RUNTIME_FINGERPRINT,
            attemptId: config.attemptId,
            repoId: end.repoId,
            toolCallId: event.toolCallId,
            commandDigest: end.commandDigest,
            workingDirectory: end.workingDirectory,
            startFingerprint: record.start.fingerprint,
            endFingerprint: end.fingerprint,
            // Compatibility field: true proves that no other tool overlapped
            // this invocation's start/end fingerprint sampling window. Tools
            // completed earlier or started later in the Pi turn are outside it.
            singleToolBatch: true,
          },
        },
      }
    } catch {
      return sanitize
    } finally {
      currentTurn.activeIds.delete(toolCallId)
    }
  })
  return true
}

export default async function resourceCheckObserver(pi) {
  await installResourceCheckObserver(pi)
}
