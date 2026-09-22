import { defineConfig } from '@playwright/test'

const port = Number(process.env.CHORA_E2E_PORT ?? 18787)
const documentPort = Number(process.env.CHORA_E2E_DOCUMENT_PORT ?? port + 1)
if (![port, documentPort].every(value => Number.isInteger(value) && value >= 1 && value <= 65535) || port === documentPort) throw new Error('Browser-test services require two distinct valid TCP ports')
const dedicatedRunnerTests = [
  '**/joined-room.spec.ts',
  '**/project-entry.spec.ts',
  '**/pi-local-connected.spec.ts',
  '**/task-first-real.spec.ts',
]

export default defineConfig({
  testDir: './e2e',
  outputDir: process.env.CHORA_E2E_OUTPUT_DIR ?? 'test-results',
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
    command: `CHORA_E2E_PORT=${port} CHORA_E2E_DOCUMENT_PORT=${documentPort} bash e2e/start-test-server.sh`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: false,
    timeout: 120_000,
    gracefulShutdown: {
      signal: 'SIGTERM',
      timeout: 30_000,
    },
  },
})
