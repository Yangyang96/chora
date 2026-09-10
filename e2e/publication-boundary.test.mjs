import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

import { scanPublicationText, validatePublicationPath } from './publication-boundary.mjs'

const policy = {
  forbidden_prefixes: ['vendor/', 'docs/validation/'],
  allowed_spike_files: ['spikes/runtime-boundary/probe.go'],
  forbidden_suffixes: ['.key', '.pem'],
}

test('publication path policy excludes private categories and admits the reviewed probe', () => {
  assert.doesNotThrow(() => validatePublicationPath('internal/app/service.go', policy))
  assert.doesNotThrow(() => validatePublicationPath('spikes/runtime-boundary/probe.go', policy))
  assert.throws(() => validatePublicationPath('vendor/example/code.go', policy), /excluded path/)
  assert.throws(() => validatePublicationPath('docs/validation/raw.md', policy), /excluded path/)
  assert.throws(() => validatePublicationPath('spikes/experiment/main.go', policy), /unreviewed spike/)
  assert.throws(() => validatePublicationPath('fixtures/server.pem', policy), /certificate or key/)
  assert.throws(() => validatePublicationPath('../escape', policy), /unsafe publication path/)
})

test('publication text scan rejects developer paths and representative secrets', () => {
  assert.doesNotThrow(() => scanPublicationText('README.md', '/Users/example/project', '/Users/maintainer/'))
  assert.throws(() => scanPublicationText('README.md', '/Users/maintainer/private', '/Users/maintainer/'), /developer home path/)
  assert.throws(() => scanPublicationText('config.txt', `token gh${'p_123456789012345678901234567890'}`, '/Users/maintainer/'), /GitHub token/)
  assert.throws(() => scanPublicationText('config.txt', ['-----BEGIN ', 'PRIVATE KEY-----'].join(''), '/Users/maintainer/'), /private key/)
})

test('repository publication policy excludes private campaigns but keeps current browser tests', async () => {
  const actual = JSON.parse(await readFile(new URL('../.github/publication-policy.json', import.meta.url), 'utf8'))
  for (const path of [
    'e2e/o4-final-evidence-real.spec.ts', 'e2e/u4-evidence-gate.mjs',
    'e2e/local-alpha-real-journey.mjs', 'e2e/prepare-local-alpha-install.sh',
    'e2e/intent-first-real.spec.ts', 'playwright.o4-profile-reuse.config.ts',
    'playwright.u4-real.config.ts', 'playwright.intent-real.config.ts',
    'tools/o4visionocr/main.swift', 'test-results/trace.zip',
    'playwright-report/index.html', 'blob-report/report.zip', 'playwright/.cache/state',
  ]) assert.throws(() => validatePublicationPath(path, actual), /excluded path/)
  for (const path of [
    'e2e/project-entry.spec.ts', 'e2e/task-first-real.spec.ts',
    'e2e/pi-local-connected.spec.ts', 'e2e/public-journey.mjs',
    'e2e/task-delivery-browser.mjs', 'playwright.config.ts',
  ]) assert.doesNotThrow(() => validatePublicationPath(path, actual))
})
