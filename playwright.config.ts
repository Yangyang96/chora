import { defineConfig } from '@playwright/test'

const port = 18787

export default defineConfig({
  testDir: './e2e',
  testMatch: '**/*.spec.ts',
  testIgnore: [
    '**/joined-room.spec.ts',
    '**/u4-real-rejection-route.spec.ts',
    '**/intent-first-real.spec.ts',
    '**/o4-profile-reuse-real.spec.ts',
    '**/o4-recovery-residue-real.spec.ts',
    '**/o4-final-evidence-real.spec.ts',
    // These suites boot their own Workbench and use dedicated runner inputs.
    '**/project-entry.spec.ts',
    '**/pi-local-connected.spec.ts',
    '**/task-first-real.spec.ts',
  ],
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
