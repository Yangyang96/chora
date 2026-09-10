import assert from 'node:assert/strict'
import test from 'node:test'

import {
  extractLocalMarkdownLinks,
  resolveLocalMarkdownLink,
  validateDocumentationTarget,
} from './documentation-links.mjs'

test('extracts local Markdown links while ignoring external links and code fences', () => {
  const markdown = [
    '[guide](docs/guide.md)',
    '[section](#section)',
    '[web](https://example.com)',
    '```md',
    '[example](missing.md)',
    '```',
    '[space](<docs/with space.md#part>)',
  ].join('\n')
  assert.deepEqual(extractLocalMarkdownLinks(markdown), ['docs/guide.md', 'docs/with space.md#part'])
})

test('resolves repository-local links and rejects escapes or absolute paths', () => {
  const root = '/tmp/chora-doc-links'
  assert.deepEqual(resolveLocalMarkdownLink(root, 'docs/guide.md', '../README.md#start'), {
    target: '/tmp/chora-doc-links/README.md',
    relativePath: 'README.md',
  })
  assert.throws(() => resolveLocalMarkdownLink(root, 'README.md', '../outside.md'), /escapes repository/)
  assert.throws(() => resolveLocalMarkdownLink(root, 'README.md', '/absolute.md'), /repository-relative/)
  assert.throws(() => resolveLocalMarkdownLink(root, 'README.md', '%ZZ'), /invalid percent-encoding/)
})

test('rejects a locally present link target that is excluded from the publication candidate', () => {
  const fileInfo = { isDirectory: () => false }
  const policy = {
    forbidden_prefixes: ['spikes/'],
    allowed_spike_files: [],
    forbidden_suffixes: ['.key'],
  }
  assert.throws(
    () => validateDocumentationTarget(
      'docs/decision.md',
      '../spikes/evidence.json',
      'spikes/evidence.json',
      fileInfo,
      new Set(['README.md', 'docs/decision.md']),
      policy,
    ),
    /excluded from publication/,
  )
  assert.doesNotThrow(() => validateDocumentationTarget(
    'docs/decision.md',
    '../README.md',
    'README.md',
    fileInfo,
    new Set(['README.md', 'docs/decision.md']),
    policy,
  ))
})
