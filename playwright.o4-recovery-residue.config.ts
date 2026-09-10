import { isAbsolute, normalize, resolve } from 'node:path'

import { defineConfig } from '@playwright/test'

// The marker is set before Playwright imports the real spec. No other config,
// including the repository default, may register this mutating journey.
process.env.CHORA_O4_RECOVERY_RESIDUE_REAL = '1'

function required(name: string) {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required for the dedicated O4-D harness`)
  return value
}

function exactAbsolute(name: string) {
  const value = required(name)
  if (!isAbsolute(value) || normalize(value) !== value || resolve(value) !== value) {
    throw new Error(`${name} must be an exact absolute path`)
  }
  return value
}

function digest(name: string) {
  const value = required(name)
  if (!/^[0-9a-f]{64}$/.test(value)) throw new Error(`${name} must be one lowercase SHA-256 digest`)
  return value
}

const configuredBaseURL = required('CHORA_O4_RECOVERY_RESIDUE_BASE_URL')
const baseURL = new URL(configuredBaseURL)
if (baseURL.protocol !== 'http:' || baseURL.hostname !== '127.0.0.1' ||
  baseURL.pathname !== '/' || baseURL.search !== '' || baseURL.hash !== '' ||
  baseURL.username !== '' || baseURL.password !== '' || baseURL.port === '') {
  throw new Error('CHORA_O4_RECOVERY_RESIDUE_BASE_URL must be an exact credential-free 127.0.0.1 HTTP origin with an explicit port')
}

if (process.env.CHORA_O4_RECOVERY_RESIDUE_IMAGE_REUSE_DIGEST !== undefined) {
  throw new Error('CHORA_O4_RECOVERY_RESIDUE_IMAGE_REUSE_DIGEST is forbidden before phase E')
}

for (const name of [
  'CHORA_O4_RECOVERY_RESIDUE_TUPLE_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_PHASE_C_RECEIPT_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_HANDOFF_RECEIPT_DIR',
  'CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_B2_RUNNER_MODULE_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_AUTHORITY_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_AUTHORIZED_RESTART_RECEIPT_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_ORPHAN_RESTART_RECEIPT_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_SOURCE_A3_LEDGER_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_VERIFIER_LEDGER_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_AUTHORITY_CONSUMPTION_DIR',
  'CHORA_O4_RECOVERY_RESIDUE_SUPERVISOR_RESOURCE_DIR',
  'CHORA_O4_RECOVERY_RESIDUE_GENERATION_REFERENCE_DIR',
  'CHORA_O4_RECOVERY_RESIDUE_PUBLIC_RESIDUE_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROL_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_REQUEST_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_OCR_EXECUTABLE_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_OCR_ENGINE_BINDING_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_EXECUTABLE_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_PROFILE_FILE',
  'CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_DIR',
  'CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_METADATA_DIR',
  'CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_OCR_DIR',
  'CHORA_O4_RECOVERY_RESIDUE_OUTPUT_DIR',
]) exactAbsolute(name)

for (const name of [
  'CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_SHA256',
  'CHORA_O4_RECOVERY_RESIDUE_B2_RUNNER_MODULE_SHA256',
  'CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_SHA256',
  'CHORA_O4_RECOVERY_RESIDUE_OCR_EXECUTABLE_SHA256',
  'CHORA_O4_RECOVERY_RESIDUE_OCR_ENGINE_BINDING_SHA256',
  'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_EXECUTABLE_SHA256',
  'CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_PROFILE_SHA256',
  'CHORA_O4_RECOVERY_RESIDUE_BINDING_DIGEST',
]) digest(name)

exactAbsolute('DOCKER_CONFIG')
exactAbsolute('PLAYWRIGHT_BROWSERS_PATH')
if (!/^[a-z0-9][a-z0-9._-]{0,127}$/.test(required('DOCKER_CONTEXT'))) {
  throw new Error('DOCKER_CONTEXT must be one exact fresh Engine context')
}
if (process.env.DOCKER_HOST !== undefined) {
  throw new Error('DOCKER_HOST is forbidden in the dedicated O4-D harness')
}

if (required('CHORA_O4_RECOVERY_RESIDUE_OCR_LANGUAGE') !== 'eng') {
  throw new Error('CHORA_O4_RECOVERY_RESIDUE_OCR_LANGUAGE must be exactly eng')
}

export default defineConfig({
  testDir: './e2e',
  testMatch: 'o4-recovery-residue-real.spec.ts',
  timeout: 120 * 60_000,
  expect: { timeout: 30_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: 'line',
  use: {
    baseURL: baseURL.toString(),
    trace: 'retain-on-failure',
    screenshot: 'off',
    video: 'off',
  },
})
