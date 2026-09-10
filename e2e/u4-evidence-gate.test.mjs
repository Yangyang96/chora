import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import {
  BROWSER_MATRIX_SCHEMA,
  BROWSER_MATRIX_TEST,
  IMPLEMENTATION_ACTIONS,
  POSITIVE_ACTIONS,
  REJECTION_ACTIONS,
  REJECTION_ROUTES,
  U4_PUBLICATION_SCHEMA,
  publishU4Evidence,
  validateU4Evidence,
} from './u4-evidence-gate.mjs'
import { buildDoctorSafeRecord, canonicalJSONStringify, sha256Hex } from './u4-doctor-record.mjs'

const digest = (value) => sha256Hex(`synthetic:${value}`)
const readinessKeys = [
  'source_baseline', 'pi_image', 'docker_engine', 'colima', 'oauth', 'ca_proxy_model', 'verifier', 'task_worktrees', 'owned_residue',
]
const positiveArtifactNames = [
  'journey.json', 'events.json', 'status.json', 'patch.diff', 'restart-reopen.json', 'public-identities.json',
  'u4-observations.json', 'readiness.json', 'residue.json', 'disclosure.json', 'accepted-ui.png',
]
const positiveZeroScans = [
  'disclosureActualCredentialLeaks', 'disclosureGenericSecretMatches', 'disclosureNonPlaceholderPrivacyMatches',
  'disclosurePrivatePathMatches', 'disclosureEnterpriseMatches', 'disclosureForbiddenPathMatches',
  'persistedActualCredentialLeaks', 'persistedGenericSecretMatches', 'persistedPrivatePathLeaks',
  'persistedEmailLeaks', 'persistedEnterpriseLeaks', 'publicHostPathMatches', 'bootstrapRouteMisuse',
]
const faultZeroScans = [
  'disclosureActualCredentialLeaks', 'disclosureGenericSecretMatches', 'disclosurePrivacyMatches',
  'disclosurePrivatePathMatches', 'disclosureEnterpriseMatches', 'disclosureForbiddenPathMatches',
  'persistedActualCredentialLeaks', 'persistedGenericSecretMatches', 'persistedPrivatePathLeaks',
  'persistedEmailLeaks', 'persistedEnterpriseLeaks',
]

test('safe Doctor builder hashes roots and emits only canonical safe fields', () => {
  const installRoot = join(tmpdir(), 'chora-u4-doctor-install')
  const dataRoot = join(tmpdir(), 'chora-u4-doctor-data')
  const record = buildDoctorSafeRecord({
    report: passedDoctor('doctor-builder'), group: 'positive', installRoot, dataRoot, port: 41301,
  })
  const encoded = JSON.stringify(record)
  assert.equal(record.passed, true)
  assert.equal(record.resourcesCreated, false)
  assert.equal(encoded.includes(installRoot), false)
  assert.equal(encoded.includes(dataRoot), false)
  assert.match(record.digest, /^[0-9a-f]{64}$/)
})

test('publishes one owner-only aggregate for complete synthetic U4 evidence', async (context) => {
  const fixture = await makeFixture(context)
  const result = await publishU4Evidence(fixture.inputs, fixture.output)
  assert.deepEqual(Object.keys(result).sort(), ['aggregateDigest', 'outputBasename', 'schemaVersion', 'status'])
  assert.equal(result.schemaVersion, U4_PUBLICATION_SCHEMA)
  assert.equal(result.status, 'passed')
  assert.equal(result.outputBasename, 'u4-pass.json')
  assert.equal((await stat(fixture.output)).mode & 0o777, 0o600)
  const publication = JSON.parse(await readFile(fixture.output, 'utf8'))
  assert.equal(publication.status, 'passed')
  assert.equal(publication.aggregateDigest, result.aggregateDigest)
})

test('rejects a missing Doctor group without publication', async (context) => {
  await expectNoPublication(context, (fixture) => {
    fixture.inputs.doctorRecordFiles.pop()
  })
})

test('rejects a failed disclosure scan without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutatePositive(fixture, 'u4-observations.json', (value) => {
      value.scans.disclosureGenericSecretMatches = 1
    })
  })
})

test('rejects nonzero terminal residue without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutatePositive(fixture, 'u4-observations.json', (value) => {
      value.residue.agentWorkspaces = 1
    })
  })
})

test('rejects a readiness block without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutatePositive(fixture, 'u4-observations.json', (value) => {
      value.readiness503.exercised = true
    })
  })
})

test('rejects duplicate Doctor installation identity without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    const duplicate = buildDoctorSafeRecord({
      report: passedDoctor('implementation-duplicate'), group: 'implementation_gap',
      installRoot: join(fixture.root, 'install-positive'), dataRoot: join(fixture.root, 'data-implementation-unique'),
      port: 41302,
    })
    await writeJSON(fixture.doctorFiles[1], duplicate)
  })
})

test('rejects a browser matrix with only two runs without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.browserMatrixFile, (value) => { value.runs.pop() })
  })
})

test('rejects browser retries without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.browserMatrixFile, (value) => { value.retries = 1 })
  })
})

test('rejects duplicate browser-matrix evidence digests without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.browserMatrixFile, (value) => {
      value.runs[1].evidenceSha256 = value.runs[0].evidenceSha256
    })
  })
})

test('rejects a missing browser-matrix route without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.browserMatrixFile, (value) => {
      value.runs[2].coveredRoutes.pop()
    })
  })
})

test('rejects a raw rejection artifact not linked to the browser matrix without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.browserMatrixFile, (value) => {
      value.runs.forEach((run, index) => { run.evidenceSha256 = digest(`unlinked-evidence-${index}`) })
    })
  })
})

test('rejects a missing raw rejection-route run without publication', async (context) => {
  await expectNoPublication(context, (fixture) => {
    fixture.inputs.rejectionRouteFiles.pop()
  })
})

test('rejects forged raw rejection-route bytes without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    const path = fixture.rejectionRouteFiles[1]
    await writeFile(path, `${await readFile(path, 'utf8')}\n`, { mode: 0o600 })
  })
})

test('rejects raw rejection-route order drift without publication', async (context) => {
  await expectNoPublication(context, (fixture) => {
    ;[fixture.inputs.rejectionRouteFiles[0], fixture.inputs.rejectionRouteFiles[1]] =
      [fixture.inputs.rejectionRouteFiles[1], fixture.inputs.rejectionRouteFiles[0]]
  })
})

test('rejects duplicate Room, Task, or Run identity across aggregate groups', async (context) => {
  for (const identity of ['room', 'task', 'run']) {
    const fixture = await makeFixture(context)
    await mutateJSON(fixture.rejectionRouteFiles[1], (value) => {
      if (identity === 'room') value.roomId = 'room_positive_001'
      if (identity === 'task') {
        value.primaryTaskId = 'task_implementation_001'
        value.observations.rejectionRoutes.planningGap.taskId = 'task_implementation_001'
      }
      if (identity === 'run') {
        const duplicate = 'run_implementation_001'
        value.publicCommands[0].path = `/api/runs/${duplicate}/retry`
        value.observations.publicUI.primaryRunId = duplicate
        value.observations.rejectionRoutes.planningGap.id = duplicate
      }
    })
    await syncBrowserRun(fixture, 1)
    await assert.rejects(publishU4Evidence(fixture.inputs, fixture.output), undefined, identity)
    await assert.rejects(stat(fixture.output), { code: 'ENOENT' })
  }
})

test('rejects rejection-route source-manifest drift without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.rejectionRouteFiles[1], (value) => {
      value.sourceManifestSha256 = digest('forged-source-manifest')
    })
    await syncBrowserRun(fixture, 1)
  })
})

test('rejects source-manifest bundle aggregate drift without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.sourceManifestFile, (value) => {
      value.aggregate_sha256 = digest('forged-bundle-aggregate')
    })
  })
})

test('rejects browser-matrix run ID forgery without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.browserMatrixFile, (value) => {
      value.runs[1].runId = digest('forged-browser-run-id')
    })
  })
})

test('rejects rejection-route Doctor data-root drift without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await replaceRejectionDoctor(fixture, 1, {
      dataRoot: join(fixture.root, 'forged-doctor-data-root'),
      inputFingerprint: fixture.rejectionFingerprints[1],
    })
  })
})

test('rejects rejection-route Doctor readiness-fingerprint drift without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await replaceRejectionDoctor(fixture, 1, {
      dataRoot: fixture.rejectionDataRoots[1],
      inputFingerprint: digest('forged-doctor-readiness'),
    })
  })
})

test('rejects cross-group Doctor identity reuse without publication', async (context) => {
  for (const collision of ['install-root', 'data-root', 'port', 'input-fingerprint', 'install-vs-data-root']) {
    await context.test(collision, async (childContext) => {
      const fixture = await makeFixture(childContext)
      const replacement = {
        installRoot: join(fixture.root, 'install-rejection-2'),
        dataRoot: fixture.rejectionDataRoots[1],
        port: 41304,
        inputFingerprint: fixture.rejectionFingerprints[1],
      }
      if (collision === 'install-root') replacement.installRoot = join(fixture.root, 'install-positive')
      if (collision === 'data-root') {
        replacement.dataRoot = join(fixture.root, 'data-implementation_gap')
        await mutateJSON(fixture.browserMatrixFile, (value) => {
          value.runs[1].dataRootHash = sha256Hex(replacement.dataRoot)
        })
      }
      if (collision === 'port') replacement.port = 41301
      if (collision === 'input-fingerprint') {
        replacement.inputFingerprint = fixture.implementationFingerprint
        await mutateJSON(fixture.rejectionRouteFiles[1], (value) => {
          value.observations.readinessUI.initial.inputFingerprint = replacement.inputFingerprint
          value.observations.readinessUI.terminal.inputFingerprint = replacement.inputFingerprint
        })
      }
      if (collision === 'install-vs-data-root') {
        replacement.installRoot = join(fixture.root, 'data-positive')
      }
      await replaceRejectionDoctor(fixture, 1, replacement)
      await syncBrowserRun(fixture, 1)
      await assert.rejects(publishU4Evidence(fixture.inputs, fixture.output))
      await assert.rejects(stat(fixture.output), { code: 'ENOENT' })
    })
  }
})

test('rejects a missing exact rejection-route observation without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.rejectionFile, (value) => {
      value.observations.rejectionRoutes.contractExclusion.unchanged = false
    })
  })
})

test('rejects Fake, bootstrap, or fixture acceptance markers without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.implementationFile, (value) => {
      value.observations.fixtureUsed = true
    })
  })
})

test('rejects a private home path without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.implementationFile, (value) => {
      value.serviceProcesses[0].stdout = '/Users/example/private-chora-state'
    })
  })
})

test('rejects obvious secret material without publication', async (context) => {
  await expectNoPublication(context, async (fixture) => {
    await mutateJSON(fixture.implementationFile, (value) => {
      value.serviceProcesses[0].stderr = `token=${'q'.repeat(24)}`
    })
  })
})

test('exclusive publication refuses a preexisting output and preserves it', async (context) => {
  const fixture = await makeFixture(context)
  await writeFile(fixture.output, 'existing\n', { mode: 0o600 })
  await assert.rejects(publishU4Evidence(fixture.inputs, fixture.output))
  assert.equal(await readFile(fixture.output, 'utf8'), 'existing\n')
})

test('pure validator returns a stable safe aggregate without writing', async (context) => {
  const fixture = await makeFixture(context)
  const first = await validateU4Evidence(fixture.inputs)
  const second = await validateU4Evidence(fixture.inputs)
  assert.deepEqual(first, second)
  await assert.rejects(stat(fixture.output))
})

async function expectNoPublication(context, mutate) {
  const fixture = await makeFixture(context)
  await mutate(fixture)
  await assert.rejects(publishU4Evidence(fixture.inputs, fixture.output))
  await assert.rejects(stat(fixture.output), { code: 'ENOENT' })
}

async function makeFixture(context) {
  const root = await mkdtemp(join(tmpdir(), 'chora-u4-evidence-'))
  context.after(async () => {
    const { rm } = await import('node:fs/promises')
    await rm(root, { recursive: true, force: true })
  })
  const positiveDir = join(root, 'positive')
  await mkdir(positiveDir, { mode: 0o700 })
  await writePositive(positiveDir)

  const expectedBundleAggregate = sourceAggregate([
    { path: 'go.mod', mode: '0444', size: 10, sha256: digest('source-go-mod') },
  ])
  const sourceManifestFile = join(root, 'source-manifest.json')
  await writeJSON(sourceManifestFile, {
    schema_version: 'chora.local-alpha-source-bundle.v1',
    aggregate_sha256: expectedBundleAggregate,
    files: [{ path: 'go.mod', mode: '0444', size: 10, sha256: digest('source-go-mod') }],
  })
  const sourceManifestSha256 = sha256Hex(await readFile(sourceManifestFile))

  const implementationFile = join(root, 'implementation-gap.json')
  const implementationFingerprint = digest('implementation-readiness')
  await writeJSON(implementationFile, implementationEvidence(sourceManifestSha256, implementationFingerprint))

  const rejectionRouteFiles = []
  const rejectionEvidenceDigests = []
  const rejectionDataRoots = []
  const rejectionFingerprints = []
  for (let sequence = 1; sequence <= 3; sequence++) {
    const path = join(root, `rejection-routes-${sequence}.json`)
    const fingerprint = digest(`rejection-readiness-${sequence}`)
    await writeJSON(path, rejectionEvidence(sequence, sourceManifestSha256, fingerprint))
    rejectionRouteFiles.push(path)
    rejectionEvidenceDigests.push(sha256Hex(await readFile(path)))
    rejectionDataRoots.push(join(root, `data-rejection-${sequence}`))
    rejectionFingerprints.push(fingerprint)
  }
  const rejectionFile = rejectionRouteFiles[0]

  const doctorFiles = []
  const groupDoctors = [
    { group: 'positive', dataRoot: join(root, 'data-positive'), inputFingerprint: digest('doctor-positive') },
    { group: 'implementation_gap', dataRoot: join(root, 'data-implementation_gap'), inputFingerprint: implementationFingerprint },
    { group: 'rejection_routes', dataRoot: rejectionDataRoots[0], inputFingerprint: rejectionFingerprints[0] },
  ]
  for (const [index, doctor] of groupDoctors.entries()) {
    const record = buildDoctorSafeRecord({
      report: passedDoctor(`doctor-${doctor.group}`, doctor.inputFingerprint), group: doctor.group,
      installRoot: join(root, `install-${doctor.group}`), dataRoot: doctor.dataRoot, port: 41301 + index,
    })
    const path = join(root, `doctor-${doctor.group}.json`)
    await writeJSON(path, record)
    doctorFiles.push(path)
  }

  const rejectionDoctorRecordFiles = [doctorFiles[2]]
  for (let index = 1; index < 3; index++) {
    const path = join(root, `doctor-rejection-routes-${index + 1}.json`)
    const record = buildDoctorSafeRecord({
      report: passedDoctor(`doctor-rejection-${index + 1}`, rejectionFingerprints[index]),
      group: 'rejection_routes', installRoot: join(root, `install-rejection-${index + 1}`),
      dataRoot: rejectionDataRoots[index], port: 41303 + index,
    })
    await writeJSON(path, record)
    rejectionDoctorRecordFiles.push(path)
  }

  const browserMatrixFile = join(root, 'browser-matrix.json')
  await writeJSON(browserMatrixFile, browserMatrix(rejectionEvidenceDigests, rejectionDataRoots))
  const output = join(root, 'u4-pass.json')
  const fixture = {
    root, positiveDir, implementationFile, rejectionFile, rejectionRouteFiles, rejectionDataRoots,
    rejectionFingerprints, implementationFingerprint, doctorFiles, rejectionDoctorRecordFiles,
    browserMatrixFile, sourceManifestFile,
    expectedBundleAggregate, output,
    inputs: {
      positiveDir, implementationGapFile: implementationFile, rejectionRouteFiles,
      doctorRecordFiles: doctorFiles, rejectionDoctorRecordFiles,
      browserMatrixFile, sourceManifestFile, expectedBundleAggregate,
    },
  }
  return fixture
}

function passedDoctor(label, inputFingerprint = digest(label)) {
  return {
    status: 'passed', resourcesCreated: false, elapsed: 1000, inputFingerprint,
    modelProbe: {
      maxAttempts: 3,
      attempts: [{ attempt: 1, outcome: 'passed', statusCode: 200, retryable: false, retried: false }],
    },
  }
}

async function writePositive(directory) {
  const roomId = 'room_positive_001'
  const taskIds = ['task_positive_primary_001', 'task_positive_secondary_002']
  const runId = 'run_positive_001'
  const resultId = 'result_positive_001'
  const revisionOne = 'plan_revision_positive_001'
  const successorDraft = 'plan_draft_positive_successor_002'
  const revisionTwo = 'plan_revision_positive_002'
  const readiness = (label) => ({
    checkedAt: `2026-08-18T00:0${label.length}:00Z`, inputFingerprint: digest(`positive-ready-${label}`),
    itemKeys: [...readinessKeys], allReady: true,
  })
  const observations = {
    schemaVersion: 'chora.m1-u4-positive-observations.v1', status: 'passed',
    planRevisionPath: {
      revisionOne: { id: revisionOne, reviewKind: 'request_revision', immutableThroughTerminalAcceptance: true },
      successorDraft: { id: successorDraft, predecessorRevisionId: revisionOne, exactContentSaved: true },
      revisionTwo: {
        id: revisionTwo, predecessorRevisionId: revisionOne, sourceDraftId: successorDraft,
        reviewKind: 'accept', exactPredecessorDiffInspected: true,
      },
    },
    restartReentry: {
      rootEntry: true, roomSelectedByName: true, taskSelectedByTitle: true,
      serverCurrentAction: 'review_result', composedDeepLinkUsed: false,
    },
    executionBinding: {
      runRevisionTwoBound: true, exactSnapshotBound: true, acceptedRevisionId: revisionTwo,
    },
    publicUI: { allAcceptedMutationsBrowserDriven: true, mutationCount: 13, actions: [...POSITIVE_ACTIONS] },
    readiness503: { exercised: false },
    readiness: { initial: readiness('initial'), beforeStart: readiness('before'), terminal: readiness('terminal') },
    scans: { disclosureStatus: 'passed', persistedCredentialStatus: 'passed', ...zeroObject(positiveZeroScans) },
    residue: {
      status: 'absent', labeledContainers: 0, labeledVerifierContainers: 0, labeledNetworks: 0,
      agentWorkspaces: 0, verifierWorkspaces: 0,
    },
  }
  const artifacts = new Map([
    ['journey.json', jsonBytes({
      id: runId, room: { id: roomId }, task: { id: taskIds[0] },
      verifiedReview: { resultId },
    })],
    ['events.json', jsonBytes({ runId, events: [] })],
    ['status.json', jsonBytes({ status: 'passed' })],
    ['patch.diff', Buffer.from('diff --git a/chora.go b/chora.go\n')],
    ['restart-reopen.json', jsonBytes({
      schemaVersion: 'chora.m1-u4-public-restart-reopen.v1', status: 'passed', roomId, taskIds, runId,
      service: { starts: [{ pid: 101 }, { pid: 102 }], stops: [{ code: 0 }, { code: 0 }] },
      reopen: {
        entryPath: '/', roomSelectedByName: true, taskSelectedByTitle: true,
        serverCurrentAction: 'review_result', composedDeepLinkUsed: false, eventsEqual: true, patchHeadersEqual: true,
      },
    })],
    ['public-identities.json', jsonBytes({
      schemaVersion: 'chora.m1-u4-public-identities.v1', roomId, runId,
      bootstrapRoutesClosed: true, fakeAdapterObserved: false, hostPathDisplayed: false,
      wrongTaskPlanBinding: { status: 409, runCreated: false, resourcesCreated: false },
    })],
    ['u4-observations.json', jsonBytes(observations)],
    ['readiness.json', jsonBytes({ status: 'passed' })],
    ['residue.json', jsonBytes(observations.residue)],
    ['disclosure.json', jsonBytes({ status: 'passed' })],
    ['accepted-ui.png', Buffer.from([137, 80, 78, 71, 13, 10])],
  ])
  for (const [name, body] of artifacts) await writeFile(join(directory, name), body, { mode: 0o600 })
  const index = {
    schemaVersion: 'chora.m1-u4-public-artifact-index.v1', roomId, taskIds, runId,
    artifacts: [...artifacts].map(([path, body]) => ({ path, bytes: body.length, sha256: sha256Hex(body) })),
  }
  await writeJSON(join(directory, 'artifact-index.json'), index)
}

function implementationEvidence(sourceManifestSha256, readinessInputFingerprint) {
  const roomId = 'room_implementation_001'
  const taskId = 'task_implementation_001'
  const runId = 'run_implementation_001'
  const predecessorResult = 'result_implementation_predecessor_001'
  const successorResult = 'result_implementation_successor_002'
  return faultEnvelope({
    scenario: 'implementation-gap-retry', roomId, primaryTaskId: taskId,
    sourceManifestSha256, readinessInputFingerprint,
    browserMutationAudit: audit(IMPLEMENTATION_ACTIONS, 'implementation_gap_retry'),
    publicCommands: [], serviceProcesses: [{ stdout: '', stderr: '' }, { stdout: '', stderr: '' }],
    publicUI: {
      primaryRunId: runId, allAcceptedMutationsBrowserDriven: true,
      mutationCount: IMPLEMENTATION_ACTIONS.length, actions: [...IMPLEMENTATION_ACTIONS],
    },
    scenarioObservations: {
      implementationGap: {
        authority: 'implementation_gap',
        predecessor: { resultId: predecessorResult, attemptId: 'attempt_implementation_predecessor_001', outcome: 'review_ready' },
        successor: { resultId: successorResult, attemptId: 'attempt_implementation_successor_002', outcome: 'review_ready' },
        acceptedResultId: successorResult,
        freshSuccessor: true, unchangedFrozenBindings: true, successorIndependentlyVerified: true,
        restartBeforeAcceptance: true,
        restartReentry: {
          rootEntry: true, roomSelectedByName: true, taskSelectedByTitle: true,
          serverCurrentAction: 'review_result', composedDeepLinkUsed: false,
        },
      },
    },
  })
}

function rejectionEvidence(sequence, sourceManifestSha256, readinessInputFingerprint) {
  const roomId = `room_rejection_${sequence}_matrix`
  const planningTask = `task_rejection_${sequence}_planning`
  const contractTask = `task_rejection_${sequence}_contract`
  const planningRun = `run_rejection_${sequence}_planning`
  const contractRun = `run_rejection_${sequence}_contract`
  return faultEnvelope({
    scenario: 'rejection-route-exclusion', roomId, primaryTaskId: planningTask,
    sourceManifestSha256, readinessInputFingerprint,
    browserMutationAudit: audit(REJECTION_ACTIONS),
    publicCommands: [planningRun, contractRun].map((runId, index) => ({
      method: 'POST', path: `/api/runs/${runId}/retry`, idempotencyKey: `u4-rejection-${index}`,
      status: 409, expectedVersion: 5 + index,
    })),
    serviceProcesses: [{ stdout: '', stderr: '' }],
    publicUI: {
      primaryRunId: planningRun, secondaryRunId: contractRun, distinctTasks: true,
      allAcceptedMutationsBrowserDriven: true, mutationCount: REJECTION_ACTIONS.length,
      actions: [...REJECTION_ACTIONS],
    },
    scenarioObservations: {
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
  })
}

function faultEnvelope({
  scenario, roomId, primaryTaskId, browserMutationAudit, publicCommands, serviceProcesses, publicUI,
  scenarioObservations, sourceManifestSha256, readinessInputFingerprint,
}) {
  const ready = (label) => ({
    checkedAt: `2026-08-18T01:0${label.length}:00Z`, inputFingerprint: readinessInputFingerprint,
    itemKeys: [...readinessKeys], allReady: true,
  })
  return {
    schemaVersion: 'chora.m1-u4-public-real-retry-route-evidence.v1', status: 'passed', scenario,
    roomId, primaryTaskId, sourceManifestSha256,
    process: { binary: '[INSTALL_ROOT]/bin/chora', argv: ['serve', '--install', '[INSTALL_ROOT]'] },
    externalFaults: [], publicCommands, browserMutationAudit, serviceProcesses,
    observations: {
      readiness503: { exercised: false }, readiness: { piEnabled: true, verifierEnabled: true },
      readinessUI: { initial: ready('initial'), terminal: ready('terminal') },
      scans: { disclosureStatus: 'passed', persistedStatus: 'passed', ...zeroObject(faultZeroScans) },
      publicPageScan: {
        status: 'passed', knownCredentialMatches: 0, internalHostPathMatches: 0, privateHomePathMatches: 0,
        vendorPathMatches: 0, unrelatedEnterpriseMatches: 0,
      },
      residue: { status: 'absent', labeledContainers: 0, labeledNetworks: 0, executionWorkspaces: 0 },
      after: { containers: [], networks: [], workspaces: [] }, publicUI, ...scenarioObservations,
    },
  }
}

function audit(actions, retryAction = '') {
  return actions.map((action) => ({
    action, method: action.includes('save') ? 'PATCH' : 'POST', path: `/api/u4/${action}`,
    status: 200, ...(action === retryAction ? { expectedVersion: 7 } : {}),
  }))
}

function browserMatrix(rejectionEvidenceDigests, dataRoots) {
  return {
    schemaVersion: BROWSER_MATRIX_SCHEMA, status: 'passed', retries: 0,
    runs: [1, 2, 3].map((sequence) => ({
      sequence,
      runId: matrixRunId(sequence, rejectionEvidenceDigests[sequence - 1], sha256Hex(dataRoots[sequence - 1])),
      dataRootHash: sha256Hex(dataRoots[sequence - 1]),
      evidenceSha256: rejectionEvidenceDigests[sequence - 1],
      passed: true, tests: [BROWSER_MATRIX_TEST], coveredActions: [...REJECTION_ACTIONS],
      coveredRoutes: REJECTION_ROUTES.map((route) => ({ ...route })),
    })),
  }
}

function matrixRunId(sequence, evidenceSha256, dataRootHash) {
  return sha256Hex(canonicalJSONStringify({ sequence, evidenceSha256, dataRootHash }))
}

function sourceAggregate(entries) {
  return sha256Hex(entries.map((entry) =>
    `${entry.path}\0${entry.mode}\0${entry.size}\0${entry.sha256}\n`).join(''))
}

async function syncBrowserRun(fixture, index) {
  const evidenceSha256 = sha256Hex(await readFile(fixture.rejectionRouteFiles[index]))
  await mutateJSON(fixture.browserMatrixFile, (value) => {
    const run = value.runs[index]
    run.evidenceSha256 = evidenceSha256
    run.runId = matrixRunId(run.sequence, run.evidenceSha256, run.dataRootHash)
  })
}

async function replaceRejectionDoctor(fixture, index, {
  dataRoot, inputFingerprint, installRoot = join(fixture.root, `install-rejection-${index + 1}`),
  port = 41303 + index,
}) {
  const record = buildDoctorSafeRecord({
    report: passedDoctor(`replacement-rejection-${index + 1}`, inputFingerprint),
    group: 'rejection_routes', installRoot, dataRoot, port,
  })
  await writeJSON(fixture.rejectionDoctorRecordFiles[index], record)
}

async function mutatePositive(fixture, name, mutator) {
  await mutateJSON(join(fixture.positiveDir, name), mutator)
  const indexPath = join(fixture.positiveDir, 'artifact-index.json')
  const index = JSON.parse(await readFile(indexPath, 'utf8'))
  const bytes = await readFile(join(fixture.positiveDir, name))
  const entry = index.artifacts.find((artifact) => artifact.path === name)
  entry.bytes = bytes.length
  entry.sha256 = sha256Hex(bytes)
  await writeJSON(indexPath, index)
}

async function mutateJSON(path, mutator) {
  const value = JSON.parse(await readFile(path, 'utf8'))
  mutator(value)
  await writeJSON(path, value)
}

function zeroObject(fields) {
  return Object.fromEntries(fields.map((field) => [field, 0]))
}

function jsonBytes(value) {
  return Buffer.from(`${JSON.stringify(value, null, 2)}\n`)
}

async function writeJSON(path, value) {
  await writeFile(path, jsonBytes(value), { mode: 0o600 })
}
