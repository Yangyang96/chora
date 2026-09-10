import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import { sha256Hex } from './u4-doctor-record.mjs'
import {
  BROWSER_MATRIX_SCHEMA,
  BROWSER_MATRIX_TEST,
  REJECTION_ACTIONS,
  REJECTION_ROUTES,
  validateBrowserMatrix,
} from './u4-evidence-gate.mjs'
import { buildBrowserMatrixRecord, publishBrowserMatrixRecord } from './u4-browser-matrix-record.mjs'

const readinessKeys = [
  'source_baseline', 'pi_image', 'docker_engine', 'colima', 'oauth', 'ca_proxy_model', 'verifier', 'task_worktrees', 'owned_residue',
]
const faultZeroScans = [
  'disclosureActualCredentialLeaks', 'disclosureGenericSecretMatches', 'disclosurePrivacyMatches',
  'disclosurePrivatePathMatches', 'disclosureEnterpriseMatches', 'disclosureForbiddenPathMatches',
  'persistedActualCredentialLeaks', 'persistedGenericSecretMatches', 'persistedPrivatePathLeaks',
  'persistedEmailLeaks', 'persistedEnterpriseLeaks',
]

test('builds and publishes one exact safe three-run browser matrix', async (context) => {
  const fixture = await makeFixture(context, 3)
  const record = await buildBrowserMatrixRecord(fixture.inputs)
  assert.equal(validateBrowserMatrix(record), record)
  assert.equal(record.schemaVersion, BROWSER_MATRIX_SCHEMA)
  assert.equal(record.retries, 0)
  assert.deepEqual(record.runs.map((run) => run.sequence), [1, 2, 3])
  assert(record.runs.every((run) => run.tests[0] === BROWSER_MATRIX_TEST))
  assert(record.runs.every((run) => /^[0-9a-f]{64}$/.test(run.runId)))

  const publication = await publishBrowserMatrixRecord(fixture.inputs, fixture.output)
  assert.deepEqual(Object.keys(publication).sort(), ['digest', 'outputBasename', 'schemaVersion', 'status'])
  assert.equal(publication.outputBasename, 'u4-browser-matrix.json')
  assert.equal(JSON.stringify(publication).includes(fixture.root), false)
  assert.equal((await stat(fixture.output)).mode & 0o777, 0o600)
  const persisted = await readFile(fixture.output, 'utf8')
  assert.equal(persisted.includes(fixture.root), false)
  assert.deepEqual(JSON.parse(persisted), record)
})

test('rejects duplicate normalized data-root identities', async (context) => {
  const fixture = await makeFixture(context, 3)
  fixture.inputs.dataRoots[2] = fixture.inputs.dataRoots[0]
  await assert.rejects(buildBrowserMatrixRecord(fixture.inputs))
})

test('rejects duplicate validated evidence bytes', async (context) => {
  const fixture = await makeFixture(context, 3)
  fixture.inputs.rejectionEvidenceFiles[2] = fixture.inputs.rejectionEvidenceFiles[0]
  await assert.rejects(buildBrowserMatrixRecord(fixture.inputs))
})

test('rejects raw source-manifest drift while building the matrix', async (context) => {
  const fixture = await makeFixture(context, 3)
  await mutateJSON(fixture.inputs.rejectionEvidenceFiles[1], (value) => {
    value.sourceManifestSha256 = sha256Hex('forged-matrix-source-manifest')
  })
  await assert.rejects(buildBrowserMatrixRecord(fixture.inputs))
})

test('rejects duplicate raw Room identity while building the matrix', async (context) => {
  const fixture = await makeFixture(context, 3)
  await mutateJSON(fixture.inputs.rejectionEvidenceFiles[1], (value) => {
    value.roomId = 'room_rejection_1_matrix'
  })
  await assert.rejects(buildBrowserMatrixRecord(fixture.inputs))
})

test('rejects source-manifest aggregate drift while building the matrix', async (context) => {
  const fixture = await makeFixture(context, 3)
  await mutateJSON(fixture.sourceManifestFile, (value) => {
    value.aggregate_sha256 = sha256Hex('forged-matrix-bundle-aggregate')
  })
  await assert.rejects(buildBrowserMatrixRecord(fixture.inputs))
})

test('rejects a two-run matrix before reading evidence', async (context) => {
  const fixture = await makeFixture(context, 2)
  await assert.rejects(buildBrowserMatrixRecord(fixture.inputs))
})

test('refuses to overwrite a preexisting output', async (context) => {
  const fixture = await makeFixture(context, 3)
  await writeFile(fixture.output, 'sentinel\n', { mode: 0o600 })
  await assert.rejects(publishBrowserMatrixRecord(fixture.inputs, fixture.output))
  assert.equal(await readFile(fixture.output, 'utf8'), 'sentinel\n')
})

test('publishes only a basename when the output directory is private', async (context) => {
  const fixture = await makeFixture(context, 3)
  const privateDirectory = join(fixture.root, 'private-local-output-directory')
  await mkdir(privateDirectory, { mode: 0o700 })
  const output = join(privateDirectory, 'matrix-safe.json')
  const publication = await publishBrowserMatrixRecord(fixture.inputs, output)
  assert.equal(publication.outputBasename, 'matrix-safe.json')
  assert.equal(JSON.stringify(publication).includes(privateDirectory), false)
})

test('rejects a hidden output basename', async (context) => {
  const fixture = await makeFixture(context, 3)
  const output = join(fixture.root, '.private.json')
  await assert.rejects(publishBrowserMatrixRecord(fixture.inputs, output))
  await assert.rejects(stat(output))
})

async function makeFixture(context, count) {
  const root = await mkdtemp(join(tmpdir(), 'chora-u4-browser-record-'))
  context.after(async () => { await rm(root, { recursive: true, force: true }) })
  const sourceEntry = { path: 'go.mod', mode: '0444', size: 10, sha256: sha256Hex('matrix-source-go-mod') }
  const expectedBundleAggregate = sha256Hex(
    `${sourceEntry.path}\0${sourceEntry.mode}\0${sourceEntry.size}\0${sourceEntry.sha256}\n`)
  const sourceManifestFile = join(root, 'source-manifest.json')
  await writeFile(sourceManifestFile, `${JSON.stringify({
    schema_version: 'chora.local-alpha-source-bundle.v1',
    aggregate_sha256: expectedBundleAggregate,
    files: [sourceEntry],
  }, null, 2)}\n`, { mode: 0o600 })
  const sourceManifestSha256 = sha256Hex(await readFile(sourceManifestFile))
  const rejectionEvidenceFiles = []
  const dataRoots = []
  for (let sequence = 1; sequence <= count; sequence++) {
    const evidence = join(root, `rejection-evidence-${sequence}.json`)
    await writeFile(evidence, `${JSON.stringify(rejectionEvidence(sequence, sourceManifestSha256), null, 2)}\n`, { mode: 0o600 })
    rejectionEvidenceFiles.push(evidence)
    const dataRoot = join(root, `fresh-data-${sequence}`)
    await mkdir(dataRoot, { mode: 0o700 })
    dataRoots.push(dataRoot)
  }
  return {
    root,
    sourceManifestFile,
    output: join(root, 'u4-browser-matrix.json'),
    inputs: { rejectionEvidenceFiles, dataRoots, sourceManifestFile, expectedBundleAggregate },
  }
}

function rejectionEvidence(sequence, sourceManifestSha256) {
  const roomId = `room_rejection_${sequence}_matrix`
  const planningTask = `task_rejection_${sequence}_planning`
  const contractTask = `task_rejection_${sequence}_contract`
  const planningRun = `run_rejection_${sequence}_planning`
  const contractRun = `run_rejection_${sequence}_contract`
  const ready = (stage) => ({
    checkedAt: `2026-08-18T0${sequence}:00:00Z`,
    inputFingerprint: sha256Hex(`matrix-${sequence}-readiness`),
    itemKeys: [...readinessKeys],
    allReady: true,
  })
  return {
    schemaVersion: 'chora.m1-u4-public-real-retry-route-evidence.v1',
    status: 'passed',
    scenario: 'rejection-route-exclusion',
    roomId,
    primaryTaskId: planningTask,
    sourceManifestSha256,
    process: { binary: '[INSTALL_ROOT]/bin/chora', argv: ['serve', '--install', '[INSTALL_ROOT]'] },
    externalFaults: [],
    publicCommands: [planningRun, contractRun].map((runId, index) => ({
      method: 'POST',
      path: `/api/runs/${runId}/retry`,
      idempotencyKey: `u4-rejection-${sequence}-${index}`,
      status: 409,
      expectedVersion: 5 + index,
    })),
    browserMutationAudit: REJECTION_ACTIONS.map((action) => ({
      action,
      method: 'POST',
      path: `/api/u4/${action}`,
      status: 200,
    })),
    serviceProcesses: [{ stdout: '', stderr: '' }],
    observations: {
      readiness503: { exercised: false },
      readiness: { piEnabled: true, verifierEnabled: true },
      readinessUI: { initial: ready('initial'), terminal: ready('terminal') },
      scans: { disclosureStatus: 'passed', persistedStatus: 'passed', ...zeroObject(faultZeroScans) },
      publicPageScan: {
        status: 'passed', knownCredentialMatches: 0, internalHostPathMatches: 0, privateHomePathMatches: 0,
        vendorPathMatches: 0, unrelatedEnterpriseMatches: 0,
      },
      residue: { status: 'absent', labeledContainers: 0, labeledNetworks: 0, executionWorkspaces: 0 },
      after: { containers: [], networks: [], workspaces: [] },
      publicUI: {
        primaryRunId: planningRun, secondaryRunId: contractRun, distinctTasks: true,
        allAcceptedMutationsBrowserDriven: true, mutationCount: REJECTION_ACTIONS.length,
        actions: [...REJECTION_ACTIONS],
      },
      rejectionRoutes: {
        planningGap: { id: planningRun, taskId: planningTask },
        contractChangeRequired: { id: contractRun, taskId: contractTask },
        planningExclusion: { status: 409, unchanged: true },
        contractExclusion: { status: 409, unchanged: true },
        planningRoute: { serverCurrentAction: 'continue_successor_plan', composedDeepLinkUsed: false },
        contractRoute: { serverCurrentAction: 'open_related_task', composedDeepLinkUsed: false },
        twoGovernedRealTasks: true,
      },
    },
  }
}

function zeroObject(fields) {
  return Object.fromEntries(fields.map((field) => [field, 0]))
}

async function mutateJSON(path, mutator) {
  const value = JSON.parse(await readFile(path, 'utf8'))
  mutator(value)
  await writeFile(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 })
}
