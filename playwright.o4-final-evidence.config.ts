import { isAbsolute, normalize, resolve } from 'node:path'

import { defineConfig } from '@playwright/test'

// This marker is set before Playwright imports the spec. The repository default
// therefore discovers zero real E tests even before its shared ignore is added.
process.env.CHORA_O4_FINAL_EVIDENCE_REAL = '1'

function required(name: string) {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required for the dedicated O4-E harness`)
  return value
}

function exactAbsolute(name: string) {
  const value = required(name)
  if (!isAbsolute(value) || normalize(value) !== value || resolve(value) !== value) {
    throw new Error(`${name} must be an exact absolute path`)
  }
  return value
}

for (const name of [
  'CHORA_O4_FINAL_TUPLE_FILE', 'CHORA_O4_FINAL_RECEIPT_DIR',
  'CHORA_O4_FINAL_SOURCE_A3_LEDGER_FILE', 'CHORA_O4_FINAL_PHASE_C_MANIFEST_FILE',
  'CHORA_O4_FINAL_PHASE_C_RECORD_FILE', 'CHORA_O4_FINAL_PHASE_C_RECEIPT_DIR',
  'CHORA_O4_FINAL_PHASE_D_MANIFEST_FILE', 'CHORA_O4_FINAL_PHASE_D_RECORD_FILE',
  'CHORA_O4_FINAL_PHASE_D_RECEIPT_DIR', 'CHORA_O4_FINAL_PHASE_D_DISCLOSURE_SOURCE_DIR',
  'CHORA_O4_FINAL_PHASE_D_SCREENSHOT_DIR', 'CHORA_O4_FINAL_PHASE_D_SCREENSHOT_METADATA_DIR',
  'CHORA_O4_FINAL_PHASE_D_SCREENSHOT_OCR_DIR', 'CHORA_O4_FINAL_PRIVATE_MARKER_FILE',
  'CHORA_O4_FINAL_INSTALLED_DOCTOR_FILE', 'CHORA_O4_FINAL_CREDENTIAL_CORPUS_FILE',
  'CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE', 'CHORA_O4_FINAL_SOURCE_MANIFEST_FILE',
  'CHORA_O4_FINAL_SOURCE_BUNDLE_ROOT', 'CHORA_O4_FINAL_BINARY_FILE',
  'CHORA_O4_FINAL_WEB_DIRECTORY', 'CHORA_O4_FINAL_RELEASE_SPEC_FILE',
  'CHORA_O4_FINAL_RELEASE_MANIFEST_FILE', 'CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE',
  'CHORA_O4_FINAL_PATH_PI_PROVENANCE_FILE', 'CHORA_O4_FINAL_PRIVATE_PI_PROVENANCE_FILE',
  'CHORA_O4_FINAL_QUALIFICATION_FILE', 'CHORA_O4_FINAL_ENGINE_ENDPOINT_EVIDENCE_FILE',
  'CHORA_O4_FINAL_B2_RUNNER_MODULE_FILE', 'CHORA_O4_FINAL_OUTPUT_DIR',
  'CHORA_O4_FINAL_STANDARD_1_RESOURCE_LEDGER_FILE',
  'CHORA_O4_FINAL_STANDARD_2_RESOURCE_LEDGER_FILE',
  'CHORA_O4_FINAL_STANDARD_3_RESOURCE_LEDGER_FILE',
]) exactAbsolute(name)

if (!/^[0-9a-f]{64}$/.test(required('CHORA_O4_FINAL_B2_RUNNER_MODULE_SHA256'))) {
  throw new Error('CHORA_O4_FINAL_B2_RUNNER_MODULE_SHA256 must be one lowercase SHA-256')
}
const closeTimeout = Number.parseInt(required('CHORA_O4_FINAL_CLOSE_TIMEOUT_MS'), 10)
if (!Number.isSafeInteger(closeTimeout) || closeTimeout < 50 || closeTimeout > 120_000) {
  throw new Error('CHORA_O4_FINAL_CLOSE_TIMEOUT_MS must be within 50..120000')
}

export default defineConfig({
  testDir: './e2e',
  testMatch: 'o4-final-evidence-real.spec.ts',
  timeout: 30 * 60_000,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: 'line',
  use: { trace: 'retain-on-failure', screenshot: 'off', video: 'off' },
})
