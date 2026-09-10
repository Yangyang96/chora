import { open } from 'node:fs/promises'
import { basename, isAbsolute, normalize, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { sha256Hex } from './u4-doctor-record.mjs'
import {
  BROWSER_MATRIX_SCHEMA,
  BROWSER_MATRIX_TEST,
  computeBrowserMatrixRunId,
  REJECTION_ACTIONS,
  REJECTION_ROUTES,
  validateBrowserMatrix,
  validateRejectionRouteEvidenceFile,
  validateSourceManifestIdentity,
} from './u4-evidence-gate.mjs'

const safeOutputName = /^[A-Za-z0-9][A-Za-z0-9._-]{0,159}\.json$/

export async function buildBrowserMatrixRecord({
  rejectionEvidenceFiles, dataRoots, sourceManifestFile, expectedBundleAggregate,
}) {
  assertExactThree(rejectionEvidenceFiles, 'rejection evidence files')
  assertExactThree(dataRoots, 'data roots')
  const normalizedRoots = dataRoots.map((root) => exactAbsolutePath(root, 'data root'))
  const source = await validateSourceManifestIdentity({ sourceManifestFile, expectedBundleAggregate })
  const summaries = []
  for (const path of rejectionEvidenceFiles) {
    summaries.push(await validateRejectionRouteEvidenceFile(path))
  }
  for (const summary of summaries) {
    assert(summary.sourceManifestSha256 === source.sha256,
      'rejection evidence source manifest drifted')
  }
  for (const field of ['roomIds', 'taskIds', 'runIds', 'resultIds']) {
    assertUnique(summaries.flatMap((summary) => summary[field]), `rejection evidence ${field}`)
  }
  const runs = summaries.map((summary, index) => {
    const sequence = index + 1
    const dataRootHash = sha256Hex(normalizedRoots[index])
    const evidenceSha256 = summary.sha256
    const runId = computeBrowserMatrixRunId({ sequence, evidenceSha256, dataRootHash })
    return {
      sequence,
      runId,
      dataRootHash,
      evidenceSha256,
      passed: true,
      tests: [BROWSER_MATRIX_TEST],
      coveredActions: [...REJECTION_ACTIONS],
      coveredRoutes: REJECTION_ROUTES.map((route) => ({ ...route })),
    }
  })
  return validateBrowserMatrix({ schemaVersion: BROWSER_MATRIX_SCHEMA, status: 'passed', retries: 0, runs })
}

export async function publishBrowserMatrixRecord(inputs, outputPath) {
  const output = exactAbsolutePath(outputPath, 'browser matrix output')
  assert(safeOutputName.test(basename(output)), 'browser matrix output basename is unsafe')
  const record = await buildBrowserMatrixRecord(inputs)
  const encoded = `${JSON.stringify(record, null, 2)}\n`
  const handle = await open(output, 'wx', 0o600)
  try {
    await handle.writeFile(encoded, 'utf8')
    await handle.sync()
    await handle.chmod(0o600)
  } finally {
    await handle.close()
  }
  return Object.freeze({
    schemaVersion: BROWSER_MATRIX_SCHEMA,
    status: 'passed',
    outputBasename: basename(output),
    digest: sha256Hex(encoded),
  })
}

function parseCLI(argv) {
  assert(Array.isArray(argv) && argv.length === 18, 'browser matrix arguments are invalid')
  const rejectionEvidenceFiles = []
  const dataRoots = []
  let outputPath
  let sourceManifestFile
  let expectedBundleAggregate
  for (let index = 0; index < argv.length; index += 2) {
    const flag = argv[index]
    const value = argv[index + 1]
    assert(typeof value === 'string' && value.length > 0, 'browser matrix arguments are invalid')
    if (flag === '--evidence') rejectionEvidenceFiles.push(value)
    else if (flag === '--data-root') dataRoots.push(value)
    else if (flag === '--output' && outputPath === undefined) outputPath = value
    else if (flag === '--source-manifest' && sourceManifestFile === undefined) sourceManifestFile = value
    else if (flag === '--bundle-aggregate' && expectedBundleAggregate === undefined) expectedBundleAggregate = value
    else throw new Error('browser matrix arguments are invalid')
  }
  assertExactThree(rejectionEvidenceFiles, 'rejection evidence files')
  assertExactThree(dataRoots, 'data roots')
  assert(typeof outputPath === 'string', 'browser matrix output is missing')
  assert(typeof sourceManifestFile === 'string', 'browser matrix source manifest is missing')
  assert(typeof expectedBundleAggregate === 'string', 'browser matrix bundle aggregate is missing')
  return { rejectionEvidenceFiles, dataRoots, sourceManifestFile, expectedBundleAggregate, outputPath }
}

function assertExactThree(values, label) {
  assert(Array.isArray(values) && values.length === 3, `${label} must contain exactly three entries`)
}

function assertUnique(values, label) {
  assert(new Set(values).size === values.length, `${label} are not unique`)
}

function exactAbsolutePath(value, label) {
  assert(typeof value === 'string' && value.length > 1 && isAbsolute(value) &&
    normalize(value) === value && resolve(value) === value && !value.includes('\0'),
  `${label} must be an absolute normalized path`)
  return value
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

async function main() {
  try {
    const { outputPath, ...inputs } = parseCLI(process.argv.slice(2))
    const result = await publishBrowserMatrixRecord(inputs, outputPath)
    process.stdout.write(`${JSON.stringify(result)}\n`)
  } catch {
    process.stdout.write(`${JSON.stringify({ schemaVersion: BROWSER_MATRIX_SCHEMA, status: 'failed' })}\n`)
    process.exitCode = 1
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main()
