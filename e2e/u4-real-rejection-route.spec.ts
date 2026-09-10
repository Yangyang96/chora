import { spawn } from 'node:child_process'
import { resolve } from 'node:path'

import { expect, test } from '@playwright/test'

const scenario = 'rejection-route-exclusion'
const maxCapturedBytes = 64 * 1024
const childTimeoutMilliseconds = 45 * 60 * 1000
const safeID = /^[A-Za-z0-9_-]{8,160}$/
const digest = /^[0-9a-f]{64}$/
const script = resolve(process.cwd(), 'e2e', 'local-alpha-fault-scenario.mjs')

test('u4-real-rejection-route-matrix', async () => {
  const result = await runRealRejectionRouteHarness()
  if (result.spawnFailed) throw new Error('real rejection-route harness failed (spawn)')
  if (result.timedOut) throw new Error('real rejection-route harness failed (timeout)')
  if (result.overflowed) throw new Error('real rejection-route harness failed (bounded output)')
  if (result.signal !== null) throw new Error('real rejection-route harness failed (signal)')
  if (result.code !== 0) throw new Error('real rejection-route harness failed (exit)')

  const lines = result.stdout.trim().split(/\r?\n/).filter(Boolean)
  if (lines.length !== 1) throw new Error('real rejection-route harness failed (stdout schema)')
  let publication: unknown
  try {
    publication = JSON.parse(lines[0])
  } catch {
    throw new Error('real rejection-route harness failed (stdout schema)')
  }
  assertSafePublication(publication)
  expect(publication).toMatchObject({ status: 'passed', scenario })
})

function runRealRejectionRouteHarness(): Promise<{
  stdout: string
  stderr: string
  code: number | null
  signal: NodeJS.Signals | null
  overflowed: boolean
  timedOut: boolean
  spawnFailed: boolean
}> {
  return new Promise((resolveResult) => {
    const child = spawn(process.execPath, [script], {
      env: { ...process.env, CHORA_FAULT_SCENARIO: scenario },
      shell: false,
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    let stdout = ''
    let stderr = ''
    let overflowed = false
    let timedOut = false
    let spawnFailed = false
    let completed = false

    const capture = (current: string, chunk: Buffer) => {
      const remaining = maxCapturedBytes - Buffer.byteLength(current)
      if (remaining <= 0 || chunk.length > remaining) {
        overflowed = true
        return current
      }
      return current + chunk.toString('utf8')
    }
    child.stdout.on('data', (chunk: Buffer) => {
      stdout = capture(stdout, chunk)
      if (overflowed) child.kill('SIGTERM')
    })
    child.stderr.on('data', (chunk: Buffer) => {
      stderr = capture(stderr, chunk)
      if (overflowed) child.kill('SIGTERM')
    })
    child.on('error', () => {
      spawnFailed = true
    })
    const timeout = setTimeout(() => {
      timedOut = true
      child.kill('SIGTERM')
    }, childTimeoutMilliseconds)
    child.on('close', (code, signal) => {
      if (completed) return
      completed = true
      clearTimeout(timeout)
      resolveResult({ stdout, stderr, code, signal, overflowed, timedOut, spawnFailed })
    })
  })
}

function assertSafePublication(value: unknown): asserts value is {
  status: 'passed'
  scenario: typeof scenario
  roomId: string
  primaryTaskId: string
  evidenceSha256: string
} {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('real rejection-route harness failed (stdout schema)')
  }
  const record = value as Record<string, unknown>
  const keys = Object.keys(record).sort()
  const expectedKeys = ['evidenceSha256', 'primaryTaskId', 'roomId', 'scenario', 'status']
  if (JSON.stringify(keys) !== JSON.stringify(expectedKeys) ||
      record.status !== 'passed' || record.scenario !== scenario ||
      typeof record.roomId !== 'string' || !record.roomId.startsWith('room_') || !safeID.test(record.roomId) ||
      typeof record.primaryTaskId !== 'string' || !record.primaryTaskId.startsWith('task_') || !safeID.test(record.primaryTaskId) ||
      typeof record.evidenceSha256 !== 'string' || !digest.test(record.evidenceSha256)) {
    throw new Error('real rejection-route harness failed (stdout schema)')
  }
}
