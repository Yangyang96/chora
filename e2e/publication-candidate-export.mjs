import { createHash } from 'node:crypto'
import { execFile } from 'node:child_process'
import { lstat, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { basename, dirname, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { promisify } from 'node:util'

import { auditPublicationBoundary, candidatePaths } from './publication-boundary.mjs'

const moduleDirectory = dirname(fileURLToPath(import.meta.url))
const repositoryRoot = resolve(moduleDirectory, '..')
const execFileAsync = promisify(execFile)

export function summarizeCandidateEntries(entries) {
  const ordered = [...entries].sort((left, right) => left.path < right.path ? -1 : left.path > right.path ? 1 : 0)
  const canonical = ordered.map(entry => JSON.stringify(entry)).join('\n') + '\n'
  return {
    files: ordered.length,
    bytes: ordered.reduce((sum, entry) => sum + entry.size, 0),
    digest: createHash('sha256').update(canonical).digest('hex'),
  }
}

export async function describeCandidateFiles(root, paths) {
  const exactRoot = await realpath(resolve(root))
  const entries = []
  for (const path of [...paths].sort()) {
    const source = resolve(exactRoot, ...path.split('/'))
    if (!isInside(exactRoot, source)) throw new Error(`candidate path escaped source repository: ${path}`)
    const info = await lstat(source)
    if (!info.isFile() || info.isSymbolicLink()) throw new Error(`candidate path is not a regular file: ${path}`)
    const bytes = await readFile(source)
    entries.push({
      path,
      mode: (info.mode & 0o777).toString(8).padStart(3, '0'),
      size: bytes.length,
      sha256: createHash('sha256').update(bytes).digest('hex'),
    })
  }
  return entries
}

export async function copyCandidateFiles(root, target, paths) {
  const exactRoot = await realpath(resolve(root))
  const targetParent = await realpath(dirname(resolve(target)))
  const exactTarget = join(targetParent, basename(resolve(target)))
  if (exactTarget === exactRoot || isInside(exactRoot, exactTarget)) {
    throw new Error('publication export target must be outside the source repository')
  }
  try {
    await lstat(exactTarget)
    throw new Error('publication export target already exists')
  } catch (error) {
    if (error?.code !== 'ENOENT') throw error
  }
  const inputDirectory = await mkdtemp(join(tmpdir(), 'chora-public-export-input-'))
  try {
    const pathsFile = join(inputDirectory, 'paths.json')
    await writeFile(pathsFile, `${JSON.stringify([...paths].sort())}\n`, { mode: 0o600 })
    // Maintainer exports can use the retained offline vendor tree. Public
    // checkouts deliberately omit it and resolve the fixed module graph instead.
    const helperEnvironment = { ...process.env, GOWORK: 'off', GOFLAGS: '-mod=readonly' }
    try {
      await lstat(join(repositoryRoot, 'vendor', 'modules.txt'))
      Object.assign(helperEnvironment, { GOPROXY: 'off', GOSUMDB: 'off', GOFLAGS: '-mod=vendor' })
    } catch (error) {
      if (error?.code !== 'ENOENT') throw error
    }
    const { stdout } = await execFileAsync('go', [
      'run', './tools/publicationexport', '--source', exactRoot, '--target', exactTarget, '--paths', pathsFile,
    ], {
      cwd: repositoryRoot,
      encoding: 'utf8',
      maxBuffer: 32 * 1024 * 1024,
      env: helperEnvironment,
    })
    const copied = JSON.parse(stdout)
    if (copied.target !== exactTarget || !Array.isArray(copied.entries)) {
      throw new Error('publication export helper returned an invalid result')
    }
    return { target: exactTarget, entries: copied.entries, ...summarizeCandidateEntries(copied.entries) }
  } finally {
    await rm(inputDirectory, { recursive: true, force: true })
  }
}

export async function exportPublicationCandidate(root, target) {
  await auditPublicationBoundary(root)
  const paths = await candidatePaths(resolve(root))
  const result = await copyCandidateFiles(root, target, paths)
  await auditPublicationBoundary(root)
  const finalPaths = await candidatePaths(resolve(root))
  if (JSON.stringify(paths) !== JSON.stringify(finalPaths)) {
    throw new Error('publication candidate path set changed during export')
  }
  const finalEntries = await describeCandidateFiles(root, finalPaths)
  if (JSON.stringify(result.entries) !== JSON.stringify(finalEntries)) {
    throw new Error('publication candidate bytes or modes changed during export')
  }
  return result
}

export async function dryRunPublicationCandidate(root) {
  const temporaryParent = await mkdtemp(join(tmpdir(), 'chora-public-export-dry-run-'))
  try {
    const result = await exportPublicationCandidate(root, join(temporaryParent, 'candidate'))
    return { status: 'passed', files: result.files, bytes: result.bytes, digest: result.digest }
  } finally {
    await rm(temporaryParent, { recursive: true, force: true })
  }
}

function isInside(root, path) {
  const rel = relative(root, path)
  return rel !== '' && rel !== '..' && !rel.startsWith(`..${sep}`)
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const target = process.argv[2]
  if (!target || process.argv.length !== 3) {
    process.stderr.write('usage: node e2e/publication-candidate-export.mjs <new-target-directory>|--dry-run\n')
    process.exitCode = 2
  } else if (target === '--dry-run') {
    const root = resolve(moduleDirectory, '..')
    dryRunPublicationCandidate(root)
      .then(result => process.stdout.write(`${JSON.stringify(result)}\n`))
      .catch(error => {
        process.stderr.write(`${error.message}\n`)
        process.exitCode = 1
      })
  } else {
    const root = resolve(moduleDirectory, '..')
    exportPublicationCandidate(root, target)
      .then(({ target: exactTarget, files, bytes, digest }) => {
        process.stdout.write(`${JSON.stringify({ status: 'passed', target: exactTarget, files, bytes, digest })}\n`)
      })
      .catch(error => {
        process.stderr.write(`${error.message}\n`)
        process.exitCode = 1
      })
  }
}
