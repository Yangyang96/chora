import { execFile } from 'node:child_process'
import { lstat, readFile } from 'node:fs/promises'
import { dirname, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)
const moduleDirectory = dirname(fileURLToPath(import.meta.url))

const secretRules = [
  ['private key', /-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----/],
  ['OpenAI key', /\bsk-[A-Za-z0-9_-]{20,}\b/],
  ['GitHub token', /\bgh[pousr]_[A-Za-z0-9]{30,}\b/],
  ['AWS access key', /\bAKIA[0-9A-Z]{16}\b/],
  ['Google API key', /\bAIza[0-9A-Za-z_-]{30,}\b/],
  ['Slack token', /\bxox[baprs]-[0-9A-Za-z-]{20,}\b/],
  ['JWT', /\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b/],
]

export function validatePublicationPath(path, policy) {
  assertSafeRelativePath(path)
  for (const prefix of policy.forbidden_prefixes) {
    if (path.startsWith(prefix)) throw new Error(`publication candidate contains excluded path: ${path}`)
  }
  if (path.startsWith('spikes/') && !policy.allowed_spike_files.includes(path)) {
    throw new Error(`publication candidate contains an unreviewed spike: ${path}`)
  }
  const lower = path.toLowerCase()
  for (const suffix of policy.forbidden_suffixes) {
    if (lower.endsWith(suffix)) throw new Error(`publication candidate contains certificate or key material: ${path}`)
  }
}

export function scanPublicationText(path, text, privateHome) {
  if (text.includes(privateHome)) throw new Error(`publication candidate contains a developer home path: ${path}`)
  for (const [name, pattern] of secretRules) {
    if (pattern.test(text)) throw new Error(`publication candidate contains ${name}: ${path}`)
  }
}

export async function auditPublicationBoundary(root) {
  const exactRoot = resolve(root)
  const policy = JSON.parse(await readFile(join(exactRoot, '.github/publication-policy.json'), 'utf8'))
  validatePolicy(policy)
  const paths = await candidatePaths(exactRoot)
  const pathSet = new Set(paths)
  for (const required of policy.required_files) {
    if (!pathSet.has(required)) throw new Error(`publication candidate is missing required file: ${required}`)
  }

  const privateHome = developerHome(exactRoot)
  let scannedFiles = 0
  for (const path of paths) {
    validatePublicationPath(path, policy)
    const fullPath = resolve(exactRoot, ...path.split('/'))
    if (!inside(exactRoot, fullPath)) throw new Error(`publication path escaped repository root: ${path}`)
    const info = await lstat(fullPath)
    if (!info.isFile() || info.isSymbolicLink()) throw new Error(`publication entry is not a regular file: ${path}`)
    if (info.size > 8 * 1024 * 1024) throw new Error(`publication entry exceeds 8 MiB review limit: ${path}`)
    const bytes = await readFile(fullPath)
    if (!bytes.includes(0)) scanPublicationText(path, bytes.toString('utf8'), privateHome)
    scannedFiles++
  }

  const packageDocument = JSON.parse(await readFile(join(exactRoot, 'package.json'), 'utf8'))
  const goModule = await readFile(join(exactRoot, 'go.mod'), 'utf8')
  if (packageDocument.license !== policy.license) {
    throw new Error('package license metadata does not match publication policy')
  }
  if (!goModule.startsWith(`module ${policy.repository}\n`)) throw new Error('Go module does not match public repository identity')
  return { status: 'passed', repository: policy.repository, license: policy.license, scannedFiles }
}

export async function candidatePaths(root) {
  const { stdout } = await execFileAsync('git', [
    '-C', root, 'ls-files', '--cached', '--others', '--exclude-standard', '-z',
  ], { encoding: 'buffer', maxBuffer: 16 * 1024 * 1024 })
  const paths = stdout.toString('utf8').split('\0').filter(Boolean).sort()
  const present = []
  for (const path of paths) {
    try {
      await lstat(join(root, ...path.split('/')))
      present.push(path)
    } catch (error) {
      if (error?.code !== 'ENOENT') throw error
    }
  }
  return present
}

function validatePolicy(policy) {
  if (policy?.schema_version !== 'chora.publication-boundary.v1' ||
      policy.repository !== 'github.com/Yangyang96/chora' || policy.license !== 'AGPL-3.0-only') {
    throw new Error('publication policy identity drifted')
  }
  for (const key of ['required_files', 'forbidden_prefixes', 'allowed_spike_files', 'forbidden_suffixes']) {
    if (!Array.isArray(policy[key]) || policy[key].length === 0 || new Set(policy[key]).size !== policy[key].length) {
      throw new Error(`publication policy ${key} is invalid`)
    }
  }
}

function assertSafeRelativePath(path) {
  if (!path || path.startsWith('/') || path.includes('\\') || path.split('/').some(part => !part || part === '.' || part === '..')) {
    throw new Error(`unsafe publication path: ${path}`)
  }
}

function developerHome(root) {
  const parts = root.split(sep)
  if (parts[1] === 'Users' && parts[2]) return `${sep}Users${sep}${parts[2]}${sep}`
  if (parts[1] === 'home' && parts[2]) return `${sep}home${sep}${parts[2]}${sep}`
  return `${root}${sep}`
}

function inside(root, path) {
  const rel = relative(root, path)
  return rel !== '' && rel !== '..' && !rel.startsWith(`..${sep}`)
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const root = resolve(moduleDirectory, '..')
  auditPublicationBoundary(root)
    .then(result => process.stdout.write(`${JSON.stringify(result)}\n`))
    .catch(error => {
      process.stderr.write(`${error.message}\n`)
      process.exitCode = 1
    })
}
