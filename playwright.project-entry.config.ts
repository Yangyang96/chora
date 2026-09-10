import { defineConfig } from '@playwright/test'

// The spec boots and restarts the `chora workbench` server itself (see
// e2e/project-entry.spec.ts), so there is no webServer here. Run through:
//   npm run test:e2e:project-entry
// or directly via e2e/run-project-entry.sh.
const port = process.env.CHORA_WORKBENCH_PORT ?? '18910'

export default defineConfig({
  testDir: './e2e',
  testMatch: '**/project-entry.spec.ts',
  timeout: 180_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'line',
  use: {
    baseURL: process.env.CHORA_WORKBENCH_BASE_URL ?? `http://127.0.0.1:${port}`,
    trace: 'retain-on-failure',
  },
})
