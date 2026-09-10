import { defineConfig } from '@playwright/test'

const baseURL = process.env.CHORA_REAL_BASE_URL
if (!baseURL) throw new Error('CHORA_REAL_BASE_URL is required')

export default defineConfig({
  testDir: './e2e',
  testMatch: 'intent-first-real.spec.ts',
  timeout: 45 * 60_000,
  expect: { timeout: 30_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'line',
  use: {
    baseURL,
    actionTimeout: 30_000,
    navigationTimeout: 30_000,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
})
