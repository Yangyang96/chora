import { lstat, readFile } from 'node:fs/promises'
import { dirname, relative, resolve, sep } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

import { candidatePaths, validatePublicationPath } from './publication-boundary.mjs'

const moduleDirectory = dirname(fileURLToPath(import.meta.url))

export function extractLocalMarkdownLinks(markdown) {
  const withoutFences = markdown
    .replace(/```[\s\S]*?```/g, '')
    .replace(/~~~[\s\S]*?~~~/g, '')
  const destinations = []
  const pattern = /!?\[[^\]\n]*\]\(([^)\n]+)\)/g
  for (const match of withoutFences.matchAll(pattern)) {
    const raw = match[1].trim()
    const destination = raw.startsWith('<') && raw.includes('>')
      ? raw.slice(1, raw.indexOf('>'))
      : raw.split(/\s+/u, 1)[0]
    if (!destination || destination.startsWith('#') || destination.startsWith('//') || /^[a-z][a-z0-9+.-]*:/iu.test(destination)) {
      continue
    }
    destinations.push(destination)
  }
  return destinations
}

export function resolveLocalMarkdownLink(root, documentPath, destination) {
  let decoded
  try {
    decoded = decodeURIComponent(destination.split('#', 1)[0].split('?', 1)[0])
  } catch {
    throw new Error(`${documentPath}: invalid percent-encoding in link: ${destination}`)
  }
  if (!decoded) return null
  if (decoded.startsWith('/') || decoded.includes('\\')) {
    throw new Error(`${documentPath}: local link must be repository-relative: ${destination}`)
  }
  const target = resolve(root, dirname(documentPath), decoded)
  const rel = relative(root, target)
  if (rel === '..' || rel.startsWith(`..${sep}`)) {
    throw new Error(`${documentPath}: local link escapes repository: ${destination}`)
  }
  return { target, relativePath: rel.split(sep).join('/') }
}

export function validateDocumentationTarget(documentPath, destination, relativePath, info, candidateSet, policy) {
  try {
    validatePublicationPath(relativePath, policy)
  } catch {
    throw new Error(`${documentPath}: local link target is excluded from publication: ${destination} (${relativePath})`)
  }
  const included = info.isDirectory()
    ? [...candidateSet].some(path => path.startsWith(`${relativePath}/`))
    : candidateSet.has(relativePath)
  if (!included) {
    throw new Error(`${documentPath}: local link target is outside the publication candidate: ${destination} (${relativePath})`)
  }
}

export async function auditDocumentationLinks(root) {
  const exactRoot = resolve(root)
  const policy = JSON.parse(await readFile(resolve(exactRoot, '.github/publication-policy.json'), 'utf8'))
  const candidates = await candidatePaths(exactRoot)
  const candidateSet = new Set(candidates)
  const documents = []
  for (const path of candidates) {
    if (!path.endsWith('.md')) continue
    try {
      validatePublicationPath(path, policy)
    } catch {
      continue
    }
    documents.push(path)
  }

  let checkedLinks = 0
  for (const documentPath of documents) {
    const markdown = await readFile(resolve(exactRoot, documentPath), 'utf8')
    for (const destination of extractLocalMarkdownLinks(markdown)) {
      const resolved = resolveLocalMarkdownLink(exactRoot, documentPath, destination)
      if (!resolved) continue
      let info
      try {
        info = await lstat(resolved.target)
      } catch (error) {
        if (error?.code === 'ENOENT') {
          throw new Error(`${documentPath}: missing local link target ${destination} (${resolved.relativePath})`)
        }
        throw error
      }
      validateDocumentationTarget(documentPath, destination, resolved.relativePath, info, candidateSet, policy)
      checkedLinks++
    }
  }
  return { status: 'passed', scannedDocuments: documents.length, checkedLinks }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const root = resolve(moduleDirectory, '..')
  auditDocumentationLinks(root)
    .then(result => process.stdout.write(`${JSON.stringify(result)}\n`))
    .catch(error => {
      process.stderr.write(`${error.message}\n`)
      process.exitCode = 1
    })
}
