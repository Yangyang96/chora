import assert from 'node:assert/strict'
import { copyFile, mkdtemp, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { execFile } from 'node:child_process'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import test from 'node:test'

import { copyCandidateFiles, summarizeCandidateEntries } from './publication-candidate-export.mjs'

test('public checkout exports without the excluded maintainer vendor tree', async () => {
  const temporary = await mkdtemp(join(tmpdir(), 'chora-public-export-no-vendor-'))
  const source = dirname(dirname(fileURLToPath(import.meta.url)))
  try {
    await mkdir(join(temporary, 'e2e'))
    await mkdir(join(temporary, 'tools', 'publicationexport'), { recursive: true })
    const files = ['go.mod', 'go.sum', 'e2e/publication-boundary.mjs', 'e2e/publication-candidate-export.mjs']
    for (const name of await readdir(join(source, 'tools', 'publicationexport'))) {
      if (name.endsWith('.go') && !name.endsWith('_test.go')) files.push(`tools/publicationexport/${name}`)
    }
    for (const path of files) await copyFile(join(source, path), join(temporary, path))
    await mkdir(join(temporary, 'input'))
    await writeFile(join(temporary, 'input', 'README.md'), 'public fixture\n')
    await promisify(execFile)('node', ['--input-type=module', '-e',
      "import {copyCandidateFiles} from './e2e/publication-candidate-export.mjs'; await copyCandidateFiles('./input', './candidate', ['README.md'])",
    ], { cwd: temporary, env: process.env, timeout: 120_000 })
    assert.equal(await readFile(join(temporary, 'candidate', 'README.md'), 'utf8'), 'public fixture\n')
    for (const path of ['go.mod', 'go.sum']) assert.deepEqual(await readFile(join(temporary, path)), await readFile(join(source, path)))
  } finally {
    await rm(temporary, { recursive: true, force: true })
  }
})

test('copies only declared files into a new target and returns a deterministic digest', async () => {
  const temporary = await mkdtemp(join(tmpdir(), 'chora-public-export-test-'))
  try {
    const root = join(temporary, 'source')
    await mkdir(join(root, 'docs'), { recursive: true })
    await writeFile(join(root, 'README.md'), 'hello\n')
    await writeFile(join(root, 'docs', 'guide.md'), 'guide\n')
    const result = await copyCandidateFiles(root, join(temporary, 'candidate'), ['docs/guide.md', 'README.md'])
    assert.equal(await readFile(join(result.target, 'README.md'), 'utf8'), 'hello\n')
    assert.equal(await readFile(join(result.target, 'docs', 'guide.md'), 'utf8'), 'guide\n')
    assert.equal(result.files, 2)
    assert.equal(result.digest, summarizeCandidateEntries([...result.entries].reverse()).digest)
  } finally {
    await rm(temporary, { recursive: true, force: true })
  }
})

test('rejects an existing or source-contained target', async () => {
  const temporary = await mkdtemp(join(tmpdir(), 'chora-public-export-negative-'))
  try {
    const root = join(temporary, 'source')
    await mkdir(root)
    await writeFile(join(root, 'README.md'), 'hello\n')
    const existing = join(temporary, 'existing')
    await mkdir(existing)
    await assert.rejects(() => copyCandidateFiles(root, existing, ['README.md']), /already exists/)
    await assert.rejects(() => copyCandidateFiles(root, join(root, 'candidate'), ['README.md']), /outside the source/)
  } finally {
    await rm(temporary, { recursive: true, force: true })
  }
})
