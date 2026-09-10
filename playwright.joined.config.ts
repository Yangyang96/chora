import { defineConfig } from '@playwright/test'

const baseURL = process.env.CHORA_JOINED_BASE_URL
if (!baseURL) throw new Error('CHORA_JOINED_BASE_URL is required')

export default defineConfig({
  testDir: './e2e',
  testMatch: 'joined-room.spec.ts',
  timeout: 300_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'line',
  use: {
    baseURL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
})
