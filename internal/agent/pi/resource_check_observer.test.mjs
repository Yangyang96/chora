import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { chmod, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import {
  FINGERPRINT_ALGORITHM,
  FINGERPRINT_SCHEMA,
  OBSERVATION_SCHEMA,
  installResourceCheckObserver,
  runFingerprintHelper,
  streamFileSHA256,
} from './resource_check_observer.mjs'

const digest = (value) => createHash('sha256').update(value).digest('hex')
const fingerprintA = 'a'.repeat(64)
const fingerprintB = 'b'.repeat(64)
const commandDigest = 'c'.repeat(64)
const helperSha256 = 'e'.repeat(64)
const runtimeFingerprint = 'f'.repeat(64)

function mockPi() {
  const handlers = new Map()
  return {
    handlers,
    on(name, handler) {
      const list = handlers.get(name) ?? []
      list.push(handler)
      handlers.set(name, list)
    },
    async emit(name, event, ctx = {}) {
      let result
      for (const handler of handlers.get(name) ?? []) {
        const next = await handler(event, ctx)
        if (next !== undefined) result = next
      }
      return result
    },
  }
}

async function fixture(invokeHelper, overrides = {}) {
  const config = {
    schema: 'chora.resource-observer-config.v1',
    attemptId: 'attempt-1',
    taskRoot: '/task',
    resources: [{ repoId: 'repo-1' }],
    ...overrides.config,
  }
  const configBytes = Buffer.from(JSON.stringify(config))
  const sourceBytes = Buffer.from('observer source')
  const env = {
    CHORA_CHECK_OBSERVER_CONFIG: '/private/config.json',
    CHORA_CHECK_OBSERVER_CONFIG_SHA256: digest(configBytes),
    CHORA_CHECK_OBSERVER_HELPER: '/private/chora',
    CHORA_CHECK_OBSERVER_HELPER_SHA256: helperSha256,
    CHORA_CHECK_OBSERVER_SHA256: digest(sourceBytes),
    CHORA_CHECK_OBSERVER_RUNTIME_FINGERPRINT: runtimeFingerprint,
    ...overrides.env,
  }
  const pi = mockPi()
  const installed = await installResourceCheckObserver(pi, {
    env,
    sourcePath: '/private/observer.mjs',
    timeoutMs: overrides.timeoutMs ?? 100,
    readFile: async (path) => path === env.CHORA_CHECK_OBSERVER_CONFIG ? configBytes : sourceBytes,
    invokeHelper,
    hashHelperFile: overrides.hashHelperFile ?? (async () => helperSha256),
  })
  return { pi, installed, config, env }
}

function helperResult(fingerprint = fingerprintA, values = {}) {
  return {
    schema: FINGERPRINT_SCHEMA,
    algorithm: FINGERPRINT_ALGORITHM,
    repoId: 'repo-1',
    workingDirectory: '.',
    commandDigest,
    fingerprint,
    helperSha256,
    ...values,
  }
}

async function observe(pi, options = {}) {
  const input = options.input ?? { command: "go test './...'", timeout: 30 }
  await pi.emit('turn_start', { turnIndex: 4 })
  await pi.emit('tool_call', { toolName: 'bash', toolCallId: 'call-1', input }, options.ctx)
  return pi.emit('tool_result', {
    toolName: 'bash', toolCallId: 'call-1', input: options.resultInput ?? input,
    content: [], details: options.details ?? { exitCode: 0 }, isError: options.isError ?? false,
  }, options.ctx)
}

test('emits a bounded single-tool observation and preserves bash details', async () => {
  const requests = []
  const { pi, installed, config, env } = await fixture(async (request) => {
    requests.push(request)
    return helperResult()
  })
  assert.equal(installed, true)
  const patch = await observe(pi)
  assert.equal(requests.length, 2)
  assert.deepEqual(requests[0], { config, command: "go test './...'" })
  assert.equal(patch.details.exitCode, 0)
  assert.deepEqual(patch.details.choraResourceCheckObservation, {
    schema: OBSERVATION_SCHEMA,
    algorithm: FINGERPRINT_ALGORITHM,
    observerSha256: env.CHORA_CHECK_OBSERVER_SHA256,
    helperSha256,
    runtimeFingerprint,
    attemptId: 'attempt-1',
    repoId: 'repo-1',
    toolCallId: 'call-1',
    commandDigest,
    workingDirectory: '.',
    startFingerprint: fingerprintA,
    endFingerprint: fingerprintA,
    singleToolBatch: true,
  })
})

test('changed contents produce no proof and remove a preexisting reserved claim', async () => {
  let call = 0
  const { pi } = await fixture(async () => helperResult(call++ === 0 ? fingerprintA : fingerprintB))
  const patch = await observe(pi, { details: { exitCode: 0, choraResourceCheckObservation: { forged: true } } })
  assert.deepEqual(patch, { details: { exitCode: 0 } })
})

test('sequential read and write tools before an isolated check remain eligible', async () => {
  const { pi } = await fixture(async () => helperResult())
  await pi.emit('turn_start', { turnIndex: 1 })
  await pi.emit('tool_call', { toolName: 'read', toolCallId: 'read-before', input: { path: 'README.md' } })
  await pi.emit('tool_result', { toolName: 'read', toolCallId: 'read-before', input: { path: 'README.md' }, details: {}, isError: false })
  await pi.emit('tool_call', { toolName: 'write', toolCallId: 'write-before', input: { path: 'README.md', content: 'changed' } })
  await pi.emit('tool_result', { toolName: 'write', toolCallId: 'write-before', input: { path: 'README.md', content: 'changed' }, details: {}, isError: false })
  const input = { command: 'go test ./...' }
  await pi.emit('tool_call', { toolName: 'bash', toolCallId: 'check', input })
  const result = await pi.emit('tool_result', { toolName: 'bash', toolCallId: 'check', input, details: { exitCode: 0 }, isError: false })
  assert.equal(result.details.choraResourceCheckObservation.singleToolBatch, true)

  // A later edit is outside the proven interval. The application separately
  // requires its final fingerprint to match this proof before reporting PASS.
  await pi.emit('tool_call', { toolName: 'write', toolCallId: 'write-after', input: { path: 'later.txt', content: 'later' } })
  await pi.emit('tool_result', { toolName: 'write', toolCallId: 'write-after', input: { path: 'later.txt', content: 'later' }, details: {}, isError: false })
})

test('a sibling tool overlapping check execution makes the fingerprint window ineligible', async () => {
  const { pi } = await fixture(async () => helperResult())
  await pi.emit('turn_start', { turnIndex: 1 })
  const input = { command: 'go test ./...' }
  await pi.emit('tool_call', { toolName: 'bash', toolCallId: 'check', input })
  await pi.emit('tool_call', { toolName: 'read', toolCallId: 'sibling', input: { path: 'README.md' } })
  const result = await pi.emit('tool_result', { toolName: 'bash', toolCallId: 'check', input, details: { exitCode: 0 }, isError: false })
  assert.equal(result, undefined)
})

test('a sibling tool starting during end fingerprint sampling makes the window ineligible', async () => {
  let helperCalls = 0
  let resolveEnd
  let announceEnd
  const endStarted = new Promise((resolve) => { announceEnd = resolve })
  const { pi } = await fixture(async () => {
    helperCalls++
    if (helperCalls === 1) return helperResult()
    announceEnd()
    return new Promise((resolve) => { resolveEnd = () => resolve(helperResult()) })
  })
  await pi.emit('turn_start', { turnIndex: 1 })
  const input = { command: 'go test ./...' }
  await pi.emit('tool_call', { toolName: 'bash', toolCallId: 'check', input })
  const resultPromise = pi.emit('tool_result', { toolName: 'bash', toolCallId: 'check', input, details: { exitCode: 0 }, isError: false })
  await endStarted
  await pi.emit('tool_call', { toolName: 'read', toolCallId: 'overlap', input: { path: 'README.md' } })
  resolveEnd()
  assert.equal(await resultPromise, undefined)
  assert.equal(helperCalls, 2)
})

test('duplicate tool call IDs never produce proof', async () => {
  const { pi } = await fixture(async () => helperResult())
  await pi.emit('turn_start', { turnIndex: 1 })
  const input = { command: 'go test ./...' }
  await pi.emit('tool_call', { toolName: 'bash', toolCallId: 'same', input })
  await pi.emit('tool_call', { toolName: 'bash', toolCallId: 'same', input })
  const result = await pi.emit('tool_result', { toolName: 'bash', toolCallId: 'same', input, details: {}, isError: false })
  assert.equal(result, undefined)
})

test('helper timeout returns promptly without proof', async () => {
  const { pi } = await fixture(() => new Promise(() => {}), { timeoutMs: 15 })
  const before = Date.now()
  const result = await observe(pi)
  assert.equal(result, undefined)
  assert.ok(Date.now() - before < 250)
})

test('config digest drift installs no handlers', async () => {
  const { pi, installed } = await fixture(async () => helperResult(), {
    env: { CHORA_CHECK_OBSERVER_CONFIG_SHA256: 'd'.repeat(64) },
  })
  assert.equal(installed, false)
  assert.equal(pi.handlers.size, 0)
})

test('wrong helper SHA in the helper response is fail-closed', async () => {
  const { pi } = await fixture(async () => helperResult(fingerprintA, { helperSha256: '9'.repeat(64) }))
  assert.equal(await observe(pi), undefined)
})

test('a helper binary upgrade between end sampling and the post-spawn hash is fail-closed', async () => {
  const hashes = [helperSha256, helperSha256, helperSha256, '8'.repeat(64)]
  let helperCalls = 0
  const { pi } = await fixture(async () => {
    helperCalls++
    return helperResult()
  }, {
    hashHelperFile: async () => hashes.shift(),
  })
  assert.equal(await observe(pi), undefined)
  assert.equal(helperCalls, 2)
  assert.deepEqual(hashes, [])
})

test('malformed helper authority and changed input remain unproved', async (t) => {
  await t.test('malformed response', async () => {
    const { pi } = await fixture(async () => ({ schema: FINGERPRINT_SCHEMA }))
    assert.equal(await observe(pi), undefined)
  })
  await t.test('input changed after tool_call', async () => {
    const { pi } = await fixture(async () => helperResult())
    const original = { command: 'go test ./...', timeout: 30 }
    const changed = { command: 'go test ./...', timeout: 31 }
    assert.equal(await observe(pi, { input: original, resultInput: changed }), undefined)
  })
  await t.test('tool error', async () => {
    const { pi } = await fixture(async () => helperResult())
    assert.equal(await observe(pi, { isError: true }), undefined)
  })
})

test('turn and session boundaries discard incomplete observations', async () => {
  const { pi } = await fixture(async () => helperResult())
  const input = { command: 'go test ./...' }
  await pi.emit('turn_start', { turnIndex: 1 })
  await pi.emit('tool_call', { toolName: 'bash', toolCallId: 'old', input })
  await pi.emit('turn_end', { turnIndex: 1 })
  assert.equal(await pi.emit('tool_result', { toolName: 'bash', toolCallId: 'old', input, details: {}, isError: false }), undefined)
  await pi.emit('turn_start', { turnIndex: 2 })
  await pi.emit('tool_call', { toolName: 'bash', toolCallId: 'new', input })
  await pi.emit('session_shutdown', {})
  assert.equal(await pi.emit('tool_result', { toolName: 'bash', toolCallId: 'new', input, details: {}, isError: false }), undefined)
})

test('native helper transport sends bounded JSON to the fixed subcommand', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'chora-observer-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const helper = join(root, 'helper.mjs')
  const helperSource = `#!/usr/bin/env node
let input = ''
process.stdin.setEncoding('utf8')
process.stdin.on('data', chunk => { input += chunk })
process.stdin.on('end', () => {
  const request = JSON.parse(input)
  if (process.argv[2] !== 'internal-resource-fingerprint' || request.command !== 'go test ./...') process.exit(9)
  process.stdout.write(JSON.stringify(${JSON.stringify(helperResult())}))
})
`
  await writeFile(helper, helperSource)
  await chmod(helper, 0o700)
  assert.equal(await streamFileSHA256(helper), digest(helperSource))
  const response = await runFingerprintHelper(helper, { config: { schema: 'test' }, command: 'go test ./...' })
  assert.deepEqual(response, helperResult())
})

test('native helper transport rejects oversized output', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'chora-observer-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const helper = join(root, 'oversized.mjs')
  await writeFile(helper, "#!/usr/bin/env node\nprocess.stdin.resume(); process.stdin.on('end', () => process.stdout.write('x'.repeat(1048577)))\n")
  await chmod(helper, 0o700)
  await assert.rejects(runFingerprintHelper(helper, { config: {}, command: 'true' }), /output too large/)
})

 test('explicit isolated-copy config remains bound to helper observations', async () => {
  const seen=[]
  const { installed,config }=await fixture(async (request)=>{seen.push(request);return {}}, {config:{repositoryLayout:'isolated_copy'}})
  assert.equal(installed,true)
  assert.equal(config.repositoryLayout,'isolated_copy')
  const invalid=await fixture(async()=>({}),{config:{repositoryLayout:'arbitrary'}})
  assert.equal(invalid.installed,false)
 })
