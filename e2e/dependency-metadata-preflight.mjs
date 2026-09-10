import { execFile } from 'node:child_process'
import { readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)
const moduleDirectory = dirname(fileURLToPath(import.meta.url))

export function summarizeNpmSbom(document) {
  const component = document?.metadata?.component
  if (document?.bomFormat !== 'CycloneDX' || component?.['bom-ref'] !== 'chora@0.0.0' || component?.purl !== 'pkg:npm/chora@0.0.0') {
    throw new Error('npm SBOM did not produce a versioned CycloneDX root component')
  }
  if (!Array.isArray(document.components) || !Array.isArray(document.dependencies)) {
    throw new Error('npm SBOM is missing component or dependency arrays')
  }
  return {
    format: document.bomFormat,
    specVersion: document.specVersion,
    root: component['bom-ref'],
    components: document.components.length,
    dependencyNodes: document.dependencies.length,
  }
}

export function parseGoModuleInventory(text) {
  const normalized = text.replace(/\r?\n$/u, '')
  const modules = normalized.split(/\r?\n/u).filter(Boolean).map((line, index) => {
    const fields = line.split('\t')
    if (fields.length !== 6 || !fields[0]) throw new Error(`invalid Go module inventory line ${index + 1}`)
    return {
      path: fields[0],
      version: fields[1],
      sum: fields[2],
      replacementPath: fields[3],
      replacementVersion: fields[4],
      replacementSum: fields[5],
    }
  })
  if (modules.length === 0 || modules[0].path !== 'github.com/Yangyang96/chora') {
    throw new Error('Go module inventory is missing the Chora root module')
  }
  return modules
}

export async function preflightDependencyMetadata(root) {
  const exactRoot = resolve(root)
  const packageDocument = JSON.parse(await readFile(resolve(exactRoot, 'package.json'), 'utf8'))
  if (packageDocument.private !== true || packageDocument.version !== '0.0.0' || packageDocument.license !== 'AGPL-3.0-only') {
    throw new Error('root npm metadata must remain private AGPL-3.0-only development version 0.0.0')
  }

  const npmResult = await execFileAsync('npm', [
    'sbom', '--package-lock-only', '--sbom-format', 'cyclonedx', '--sbom-type', 'application',
  ], { cwd: exactRoot, encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 })
  const npm = summarizeNpmSbom(JSON.parse(npmResult.stdout))

  const goResult = await execFileAsync('go', [
    'list', '-m', '-f', '{{.Path}}\t{{.Version}}\t{{.Sum}}\t{{if .Replace}}{{.Replace.Path}}\t{{.Replace.Version}}\t{{.Replace.Sum}}{{else}}\t\t{{end}}', 'all',
  ], {
    cwd: exactRoot,
    encoding: 'utf8',
    maxBuffer: 16 * 1024 * 1024,
    env: { ...process.env, GOWORK: 'off', GOPROXY: 'off', GOSUMDB: 'off', GOFLAGS: '-mod=readonly' },
  })
  const go = parseGoModuleInventory(goResult.stdout)
  return { status: 'passed', npm, goModules: go.length }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const root = resolve(moduleDirectory, '..')
  preflightDependencyMetadata(root)
    .then(result => process.stdout.write(`${JSON.stringify(result)}\n`))
    .catch(error => {
      process.stderr.write(`${error.message}\n`)
      process.exitCode = 1
    })
}
