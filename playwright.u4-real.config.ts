import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  testMatch: 'u4-real-rejection-route.spec.ts',
  timeout: 50 * 60 * 1000,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'line',
  use: {
    trace: 'off',
    screenshot: 'off',
    video: 'off',
  },
})
