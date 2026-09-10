import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

import { parseGoModuleInventory, preflightDependencyMetadata, summarizeNpmSbom } from './dependency-metadata-preflight.mjs'

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')

test('summarizes a versioned CycloneDX npm document', () => {
  assert.deepEqual(summarizeNpmSbom({
    bomFormat: 'CycloneDX',
    specVersion: '1.6',
    metadata: { component: {
      'bom-ref': 'chora@0.0.0',
      name: 'workspace',
      version: '0.0.0',
      purl: 'pkg:npm/chora@0.0.0',
    } },
    components: [{ name: 'react' }],
    dependencies: [{ ref: 'chora' }],
  }), {
    format: 'CycloneDX',
    specVersion: '1.6',
    root: 'chora@0.0.0',
    components: 1,
    dependencyNodes: 1,
  })
  assert.throws(() => summarizeNpmSbom({ bomFormat: 'SPDX' }), /versioned CycloneDX/)
})

test('parses stable Go module fields and requires the Chora root', () => {
  const text = [
    'github.com/Yangyang96/chora\t\t\t\t\t',
    'example.com/module\tv1.2.3\th1:sum\t\t\t',
  ].join('\n')
  assert.equal(parseGoModuleInventory(text).length, 2)
  assert.throws(() => parseGoModuleInventory('example.com/module\tv1.0.0\th1:sum\t\t\t'), /Chora root/)
  assert.throws(() => parseGoModuleInventory('github.com/Yangyang96/chora\ttoo-short'), /invalid Go module inventory/)
})

test('ignores a conflicting ambient Go workspace', async () => {
  const temporary = await mkdtemp(join(tmpdir(), 'chora-dependency-workspace-test-'))
  const previous = process.env.GOWORK
  try {
    const foreign = join(temporary, 'foreign')
    await mkdir(foreign)
    await writeFile(join(foreign, 'go.mod'), 'module example.com/foreign\n\ngo 1.26.0\n')
    const workspace = join(temporary, 'go.work')
    await writeFile(workspace, 'go 1.26.0\n\nuse ./foreign\n')
    process.env.GOWORK = workspace
    const result = await preflightDependencyMetadata(repositoryRoot)
    assert.equal(result.status, 'passed')
    assert.ok(result.goModules > 1)
  } finally {
    if (previous === undefined) delete process.env.GOWORK
    else process.env.GOWORK = previous
    await rm(temporary, { recursive: true, force: true })
  }
})
