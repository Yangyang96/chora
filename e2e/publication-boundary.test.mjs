import assert from 'node:assert/strict'
import test from 'node:test'

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
