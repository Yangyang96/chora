import { defineConfig } from '@playwright/test'

process.env.CHORA_O4_PROFILE_REUSE_REAL = '1'

function required(name: string) {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required for the dedicated O4-C harness`)
  return value
}

const configuredBaseURL = required('CHORA_O4_PROFILE_REUSE_BASE_URL')
const baseURL = new URL(configuredBaseURL)
if (baseURL.protocol !== 'http:' || baseURL.hostname !== '127.0.0.1' || baseURL.pathname !== '/' ||
  baseURL.search !== '' || baseURL.hash !== '' || baseURL.username !== '' || baseURL.password !== '') {
  throw new Error('CHORA_O4_PROFILE_REUSE_BASE_URL must be an exact credential-free loopback HTTP origin')
}

for (const name of [
  'CHORA_O4_PROFILE_REUSE_TUPLE_FILE',
  'CHORA_O4_PROFILE_REUSE_SOURCE_A3_LEDGER_FILE',
  'CHORA_O4_PROFILE_REUSE_HANDOFF_RECEIPT_DIR',
  'CHORA_O4_PROFILE_REUSE_OBSERVER_FILE',
  'CHORA_O4_PROFILE_REUSE_SUPERVISOR_RESOURCE_DIR',
  'CHORA_O4_PROFILE_REUSE_RUNNER_PROTOCOL_DIR',
  'CHORA_O4_PROFILE_REUSE_OUTPUT_DIR',
]) {
  const value = required(name)
  if (!value.startsWith('/') || value.includes('/../') || value.endsWith('/..')) {
    throw new Error(`${name} must be an exact absolute path`)
  }
}

if (!/^[0-9a-f]{64}$/.test(required('CHORA_O4_PROFILE_REUSE_OBSERVER_SHA256'))) {
  throw new Error('CHORA_O4_PROFILE_REUSE_OBSERVER_SHA256 must be one SHA-256 digest')
}

export default defineConfig({
  testDir: './e2e',
  testMatch: 'o4-profile-reuse-real.spec.ts',
  timeout: 90 * 60_000,
  expect: { timeout: 30_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: 'line',
  use: {
    baseURL: baseURL.toString(),
    trace: 'retain-on-failure',
  },
})
