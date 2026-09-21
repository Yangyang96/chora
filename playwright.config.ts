import { defineConfig } from '@playwright/test'

const port = 18787
const documentPort = 18788
const dedicatedRunnerTests = [
  '**/joined-room.spec.ts',
  '**/project-entry.spec.ts',
  '**/pi-local-connected.spec.ts',
  '**/task-first-real.spec.ts',
]

export default defineConfig({
  testDir: './e2e',
  testMatch: '**/*.spec.ts',
  // These suites boot their own Workbench and use dedicated runner inputs.
  testIgnore: dedicatedRunnerTests,
  timeout: 180_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'line',
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'standard', testIgnore: [...dedicatedRunnerTests, '**/project-document.spec.ts'] },
    { name: 'project-document', testMatch: '**/project-document.spec.ts', testIgnore: dedicatedRunnerTests, use: { baseURL: `http://127.0.0.1:${documentPort}` } },
  ],
  webServer: {
    command: `CHORA_E2E_PORT=${port} bash e2e/start-test-server.sh`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: false,
    timeout: 120_000,
    gracefulShutdown: {
      signal: 'SIGTERM',
      timeout: 30_000,
    },
  },
})
