import { createHash, createHmac, randomBytes } from 'node:crypto'
import {
  chmod, lstat, open, readFile, readdir,
} from 'node:fs/promises'
import { basename, dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

import { expect, test, type APIRequestContext, type Page, type Response } from '@playwright/test'

import {
  cleanupOwnedPath,
  ownedPathIdentity,
} from './o4-owned-path-cleanup.mjs'

type JSONMap = Record<string, any>
type PreparedTask = { roomId: string; taskId: string; snapshotId: string; snapshotDigest: string }
type ScenarioObservation = {
  sequence: number
  name: string
  taskId: string
  runId: string
  attemptId: string
  workspaceId: string
  targetId: string
  attemptState: string
  terminalReason: string | null
  terminalStatus: string
  publicEntry: true
  cleanupComplete: true
  patchState: string
  checkState: string
  reviewState: string
  sourceA3: JSONMap
  verifierSource: JSONMap | null
  residue: JSONMap
  scenarioProof: JSONMap
  publicActionDigest: string
  publicObservationDigest: string
  completedAt: string
}

const nativeImport = new Function('specifier', 'return import(specifier)') as
  (specifier: string) => Promise<JSONMap>

// The repository default configuration imports this file but must register no
// mutating test. Only the dedicated config sets this exact marker before import.
if (process.env.CHORA_O4_RECOVERY_RESIDUE_REAL === '1') {
  test.describe.configure({ mode: 'serial' })
  test('real installed recovery/residue sequence publishes one exact phase D', async ({ page, request }) => {
    test.setTimeout(120 * 60_000)
    const env = harnessEnvironment()
    await assertTotalSessionBinding(env, 'D')
    const repositoryCapture = Object.freeze({
      executableFile: env.serviceControllerExecutable,
      executableSha256: requiredDigest(env.serviceControllerSha256, 'service controller SHA-256'),
    })
    const recordModule = await nativeImport(pathToFileURL(
      resolve(process.cwd(), 'e2e/o4-recovery-residue-record.mjs')).href)
    const candidateModule = await nativeImport(pathToFileURL(
      resolve(process.cwd(), 'e2e/o4-candidate-install-orchestrator.mjs')).href)
    const b2Config = await readPinnedJSON(env.b2ConfigFile, env.b2ConfigSha256, 'B2 config')
    const runnerModule = await readPinnedModule(env.b2RunnerModuleFile,
      env.b2RunnerModuleSha256, 'B2 Runner module')
    exactKeys(runnerModule, ['createO4B2Runner'], 'B2 Runner module exports')
    const runner = await runnerModule.createO4B2Runner({
      view: 'D', manifestFile: env.totalManifestFile,
      manifestSha256: env.totalManifestSha256,
      sessionAuthorityFile: env.totalSessionAuthorityFile,
    })
    assertRunner(runner)

    const failedResponses: string[] = []
    page.on('response', (response) => {
      if (response.status() >= 400) failedResponses.push(`${response.status()} ${new URL(response.url()).pathname}`)
    })
    await page.goto('/', { waitUntil: 'networkidle' })
    const room = await createRoomThroughUI(page)

    // D's only startup authorities are prepared after C, before the authorized
    // restart, and remain unexecuted until that restart has bound the database.
    const failurePlan = await createAcceptedStandardPlan(page, room,
      'O4 recovery failure: produce the exact declared nonzero managed failure after runtime start.')
    await returnToRoomThroughDirectory(page, room.name)
    const timeoutPlan = await createAcceptedStandardPlan(page, room,
      'O4 recovery timeout: remain active until the exact accelerated managed deadline terminates the Attempt.')
    const o4Authority = await candidateModule.buildO4FaultAuthority({
      config: b2Config, authorityPath: env.authorityFile, scenarioBindings: [
        { scenario: 'failure', taskId: failurePlan.taskId,
          snapshotId: failurePlan.snapshotId, snapshotDigest: failurePlan.snapshotDigest },
        { scenario: 'timeout', taskId: timeoutPlan.taskId,
          snapshotId: timeoutPlan.snapshotId, snapshotDigest: timeoutPlan.snapshotDigest },
      ],
    }, { repositoryCapture })
    env.generationId = b2Config.identity.generationId
    env.tupleIdentity = o4Authority.candidateTuple
    const productEvidenceAuthority = await readProductEvidenceAuthority(env,
      o4Authority.candidateTuple)
    const authorized = await candidateModule.restartCandidate(b2Config, {
      runner, o4Authority, o4EvidenceAuthority: productEvidenceAuthority, repositoryCapture,
    })
    expect(authorized.restartReceipt).toMatchObject({ sequence: 1, status: 'passed', setupReplayed: false })
    expect(resolve(env.authorizedRestartReceiptFile)).toBe(resolve(dirname(env.authorizedRestartReceiptFile), 'restart-01.json'))

    const tracker = await sourceTracker(env.sourceA3LedgerFile, env.verifierLedgerFile)
    const observations: ScenarioObservation[] = []

    await returnToRoomThroughDirectory(page, room.name)
    observations.push(await runFailure(page, request, failurePlan, tracker, env, recordModule))

    await returnToRoomThroughDirectory(page, room.name)
    observations.push(await runCancel(page, request, room, tracker, env, recordModule))

    await returnToRoomThroughDirectory(page, room.name)
    observations.push(await runTimeout(page, request, timeoutPlan, tracker, env, recordModule))

    await returnToRoomThroughDirectory(page, room.name)
    observations.push(await runRejectedResultRetry(page, request, room, tracker, env, recordModule))

    await assertPublicResidueZero(request)
    const graceful = await candidateModule.restartCandidate(b2Config, {
      runner, o4EvidenceAuthority: productEvidenceAuthority, repositoryCapture,
    })
    expect(graceful.restartReceipt).toMatchObject({ sequence: 2, status: 'passed', setupReplayed: false })
    await returnToRoomThroughDirectory(page, room.name)
    observations.push(await runSuccess(page, request, room, 5, 'restart', tracker, env, recordModule, {
      idleBeforeRestart: true, graceful: true, setupReplayed: false,
      restartReceiptDigest: graceful.restartReceipt.receiptDigest,
    }))

    await returnToRoomThroughDirectory(page, room.name)
    const orphanStarted = await startFreshStandardTask(page, request, room,
      'O4 orphan: stay running until authenticated service control interrupts this exact Attempt.')
    const runningOrphan = await waitForAttemptState(request, orphanStarted.path, ['running'])
    await writeControllerRequest(env.serviceControllerRequestFile, {
      schemaVersion: 'chora.m1-o4-service-control-request.v1', action: 'interrupt_restart_and_prove',
      controllerSha256: env.serviceControllerSha256,
      tupleFile: env.tupleFile, restartReceiptFile: env.orphanRestartReceiptFile,
      taskId: orphanStarted.taskId, runId: orphanStarted.runId,
      interruptedAttemptId: runningOrphan.attemptDetail.id,
    }, repositoryCapture)
    await runner.armAuthenticatedControllerRecovery({
      requestFile: env.serviceControllerRequestFile,
      controllerExecutable: env.serviceControllerExecutable,
      controllerSha256: env.serviceControllerSha256,
      taskId: orphanStarted.taskId, runId: orphanStarted.runId,
      attemptId: requiredString(runningOrphan.attemptDetail?.id, 'orphan running Attempt ID'),
    })
    // restartCandidate now calls the armed Runner's processBoundary and start:
    // the pinned controller performs exact SIGKILL, waits for old group death,
    // starts a distinct service, proves readiness/current generation, and only
    // then lets B2 append the no-Setup restart receipt.
    const orphanRestart = await candidateModule.restartCandidate(b2Config, {
      runner, o4EvidenceAuthority: productEvidenceAuthority, repositoryCapture,
    })
    expect(orphanRestart.restartReceipt).toMatchObject({ sequence: 3, status: 'passed', setupReplayed: false })
    await runner.awaitAuthenticatedControllerProof({
      outputFile: env.serviceControlFile,
      restartReceiptFile: env.orphanRestartReceiptFile,
    })
    const serviceControl = await recordModule.readPinnedServiceControlProof({
      executableFile: env.serviceControllerExecutable,
      executableSha256: env.serviceControllerSha256,
      requestFile: env.serviceControllerRequestFile,
      outputFile: env.serviceControlFile,
    })
    const recoveredOrphan = await recoverOrphanThroughPublicUI(page, request, orphanStarted)
    observations.push(await captureSuccessfulScenario(page, request, recoveredOrphan, 6, 'orphan', tracker, env, recordModule, {
      interruptedAttemptId: runningOrphan.attemptDetail.id,
      interruptedState: 'recovery_required',
      recoveredAttemptId: recoveredOrphan.run.attemptDetail.id,
      recoveredState: 'output_submitted', authenticatedServiceControl: true,
      serviceControlDigest: serviceControl.digest,
    }, 'recovered'))

    await page.goto('/', { waitUntil: 'networkidle' })
    await page.getByText(room.name, { exact: true }).first().click()
    observations.push(await runSuccess(page, request, room, 7, 'reopen', tracker, env, recordModule, {
      rootEntry: '/', roomDirectoryEntry: true, deepLinkUsed: false,
    }))

    await returnToRoomThroughDirectory(page, room.name)
    const applyCapture = await runApply(page, request, room, tracker, env, recordModule)
    observations.push(applyCapture.observation)

    expect(failedResponses.filter((entry) => !entry.startsWith('409 /api/o4/residue')),
      'unexpected failed public response').toEqual([])
    assertScenarioOrderAndIdentity(observations)

    const finalResidue = await assertPublicResidueZero(request)
    await writeExclusive0400(env.publicResidueFile, Buffer.from(`${JSON.stringify(finalResidue)}\n`),
      repositoryCapture)
    const paths = await recordModule.prepareRecoveryResidueEvidencePaths({
      tupleFile: env.tupleFile, phaseCReceiptFile: env.phaseCReceiptFile,
      authorityFile: env.authorityFile, authorizedRestartReceiptFile: env.authorizedRestartReceiptFile,
      orphanRestartReceiptFile: env.orphanRestartReceiptFile,
      sourceA3LedgerFile: env.sourceA3LedgerFile, verifierLedgerFile: env.verifierLedgerFile,
      authorityConsumptionDirectory: env.authorityConsumptionDirectory,
      supervisorResourceDirectory: env.supervisorResourceDirectory,
      generationReferenceDirectory: env.generationReferenceDirectory,
      publicResidueFile: env.publicResidueFile, serviceControlFile: env.serviceControlFile,
      screenshotDirectory: env.screenshotDirectory,
      screenshotMetadataDirectory: env.screenshotMetadataDirectory,
      screenshotOCRDirectory: env.screenshotOCRDirectory,
      serviceControllerExecutable: env.serviceControllerExecutable,
      serviceControllerSha256: env.serviceControllerSha256,
      ocrExecutable: env.ocrExecutable, ocrExecutableSha256: env.ocrExecutableSha256,
      ocrEngineBindingFile: env.ocrEngineBindingFile,
      ocrEngineBindingSha256: env.ocrEngineBindingSha256,
      ocrSandboxExecutable: env.ocrSandboxExecutable,
      ocrSandboxExecutableSha256: env.ocrSandboxExecutableSha256,
      ocrSandboxProfileFile: env.ocrSandboxProfileFile,
      ocrSandboxProfileSha256: env.ocrSandboxProfileSha256,
      ocrLanguage: env.ocrLanguage,
      b2RunnerModuleFile: env.b2RunnerModuleFile,
      b2RunnerModuleSha256: env.b2RunnerModuleSha256,
      outputDirectory: env.outputDirectory,
    })
    const applyObservation = observations[7]
    const applyBinding = {
      taskId: applyObservation.taskId, runId: applyObservation.runId,
      attemptId: applyObservation.attemptId, workspaceId: applyObservation.workspaceId,
      targetId: applyObservation.targetId,
      publicObservationDigest: applyObservation.publicObservationDigest,
      patchDigest: requiredDigest(applyObservation.scenarioProof.patchDigest, 'public Apply patch digest'),
      payloadSha256: requiredDigest(applyObservation.scenarioProof.payloadSha256,
        'exact Apply Response.body digest'),
    }
    const disclosureExecutionSource = join(dirname(env.serviceControllerRequestFile),
      'apply-execution-source.log')
    const disclosurePatchSource = join(dirname(env.serviceControllerRequestFile),
      'apply-change-source.patch')
    let disclosureExecutionIdentity
    let disclosurePatchIdentity
    let disclosureError
    try {
      disclosureExecutionIdentity = await writeExclusive0400(disclosureExecutionSource,
        applyCapture.executionLogBytes, repositoryCapture)
      disclosurePatchIdentity = await writeExclusive0400(disclosurePatchSource,
        applyCapture.patchBytes, repositoryCapture)
      await recordModule.runPinnedDisclosureExporter({
        runner, paths, applyBinding, responseBody: applyCapture.responseBody,
        sources: { executionLogFile: disclosureExecutionSource,
          stateDatabaseFile: b2Config.paths.databaseFile, changePatchFile: disclosurePatchSource },
        repositoryCapture,
      })
    } catch (error) {
      disclosureError = error
      throw error
    } finally {
      const cleanupErrors = []
      for (const [path, identity] of [
        [disclosurePatchSource, disclosurePatchIdentity],
        [disclosureExecutionSource, disclosureExecutionIdentity],
      ] as const) {
        if (identity === undefined) continue
        try {
          await cleanupOwnedPath({ capability: repositoryCapture, path, identity,
            disposition: 'regular_file' })
        } catch (error) { cleanupErrors.push(error) }
      }
      if (cleanupErrors.length > 0) throw new AggregateError(
        disclosureError === undefined ? cleanupErrors : [disclosureError, ...cleanupErrors],
        'disclosure source native cleanup failed',
        disclosureError === undefined ? undefined : { cause: disclosureError },
      )
    }
    const disclosureSourceManifest = await recordModule.buildDisclosureSourceManifest({
      paths,
      identity: {
        environmentId: b2Config.identity.environmentId, installId: b2Config.identity.installId,
        generationId: b2Config.identity.generationId, bindingDigest: env.bindingDigest,
        tupleIdentity: o4Authority.candidateTuple,
      },
      applyBinding,
    })
    await recordModule.persistDisclosureSourceManifest(disclosureSourceManifest,
      paths.disclosureSourceManifestFile, { repositoryCapture })
    applyObservation.scenarioProof.disclosureSourceManifestDigest =
      disclosureSourceManifest.manifestDigest
    for (const observation of observations) {
      const receipt = recordModule.formRecoveryResidueScenarioReceipt({
        schemaVersion: recordModule.RECOVERY_RESIDUE_RECEIPT_SCHEMA, status: 'passed',
        ...observation, environmentId: b2Config.identity.environmentId,
        installId: b2Config.identity.installId, generationId: b2Config.identity.generationId,
        bindingDigest: env.bindingDigest,
        tupleIdentity: o4Authority.candidateTuple,
      })
      await recordModule.collectRecoveryResidueScenario({
        receipt, outputPath: join(paths.receiptDirectory,
          recordModule.RECOVERY_RESIDUE_RECEIPT_NAMES[observation.sequence - 1]),
        repositoryCapture,
      })
    }
    const manifest = await recordModule.buildRecoveryResidueManifest({
      paths, environmentId: b2Config.identity.environmentId, installId: b2Config.identity.installId,
      generationId: b2Config.identity.generationId,
      bindingDigest: env.bindingDigest,
      tupleIdentity: o4Authority.candidateTuple,
      sharedImages: sharedImagesFromB2(b2Config),
    })
    const record = await recordModule.buildRecoveryResidueRecord({
      manifest, receiptDirectory: paths.receiptDirectory,
    })
    const publication = await recordModule.persistRecoveryResidueEvidence({
      manifest, record, paths, manifestFile: paths.manifestFile, recordFile: paths.recordFile,
      tupleFile: env.tupleFile, handoffReceiptDirectory: env.handoffReceiptDirectory,
      repositoryCapture,
    })
    expect(publication.receipt).toMatchObject({
      phase: 'D', operationDigest: publication.manifestSha256,
      observationDigest: record.recordDigest,
    })
  })
}

async function createRoomThroughUI(page: Page) {
  const name = `O4 recovery residue ${Date.now()}`
  const response = await uiMutation(page, /^\/api\/rooms$/, async () => {
    await page.getByRole('button', { name: '＋ New Room' }).click()
    await page.getByLabel('Room name').fill(name)
    await page.getByRole('button', { name: 'Create Room', exact: true }).click()
  })
  expect(response.status()).toBe(201)
  const room = await safeJSON(response, 'Room creation')
  return { id: requiredString(room.id, 'Room ID'), name }
}

async function createAcceptedStandardPlan(page: Page, room: { id: string; name: string }, requirement: string): Promise<PreparedTask> {
  await page.getByRole('button', { name: '＋ New Task' }).first().click()
  await page.getByRole('radio', { name: 'Standard', exact: true }).click()
  const createdResponse = await uiMutation(page, new RegExp(`^/api/rooms/${escapeRE(room.id)}/tasks$`), async () => {
    await page.getByLabel('What should Chora build?').fill(requirement)
    await page.getByRole('button', { name: 'Start', exact: true }).click()
  })
  expect(createdResponse.status()).toBe(201)
  const task = await safeJSON(createdResponse, 'Task creation')
  const taskId = requiredString(task.id, 'Task ID')
  const submittedResponse = await uiMutation(page,
    new RegExp(`^/api/tasks/${escapeRE(taskId)}/plan/drafts/[^/]+/submit$`), async () => {
      await page.getByRole('button', { name: 'Submit Revision' }).click()
    })
  const submitted = await safeJSON(submittedResponse, 'Plan submission')
  const acceptedResponse = await uiMutation(page,
    new RegExp(`^/api/tasks/${escapeRE(taskId)}/plan/revisions/[^/]+/reviews$`), async () => {
      await page.getByLabel('Planning review note').fill('Accept the exact bounded O4-D Standard Plan.')
      await page.getByRole('button', { name: 'Accept Revision' }).click()
    })
  const accepted = await safeJSON(acceptedResponse, 'Plan acceptance')
  expect(accepted.review?.kind).toBe('accept')
  const snapshot = accepted.snapshot ?? accepted.contextSnapshot ?? submitted.snapshot ?? submitted.contextSnapshot
  return { roomId: room.id, taskId,
    snapshotId: requiredString(snapshot?.id ?? accepted.snapshotId, 'accepted Snapshot ID'),
    snapshotDigest: requiredDigest(snapshot?.digest ?? accepted.snapshotDigest, 'accepted Snapshot digest') }
}

async function startAcceptedPlan(page: Page, request: APIRequestContext, plan: PreparedTask) {
  const response = await uiMutation(page, new RegExp(`^/api/tasks/${escapeRE(plan.taskId)}/runs$`), async () => {
    await page.getByRole('button', { name: 'Start Bound Pi Run' }).click()
  }, 120_000)
  expect(response.status()).toBe(201)
  const started = await safeJSON(response, 'Run start')
  const runId = requiredString(started.id, 'Run ID')
  return { taskId: plan.taskId, runId,
    path: `/api/rooms/${encodeURIComponent(plan.roomId)}/tasks/${encodeURIComponent(plan.taskId)}/runs/${encodeURIComponent(runId)}` }
}

async function startFreshStandardTask(page: Page, request: APIRequestContext,
  room: { id: string; name: string }, requirement: string) {
  const plan = await createAcceptedStandardPlan(page, room, requirement)
  return startAcceptedPlan(page, request, plan)
}

async function runFailure(page: Page, request: APIRequestContext, plan: PreparedTask,
  tracker: JSONMap, env: JSONMap, module: JSONMap) {
  const started = await startAcceptedPlan(page, request, plan)
  const run = await waitForRunStatus(request, started.path, ['failed'])
  expect(run.attemptDetail).toMatchObject({ state: 'failed', terminalReason: 'runtime_exit_nonzero' })
  assertNoPatchCheckReview(run, 'failure')
  return captureTerminalScenario(page, request, { ...started, run }, 1, 'failure', tracker, env, module,
    { exitCode: requiredNonzero(run.attemptDetail?.exitCode, 'failure exit code'), processTreeDead: true, automaticRetryCount: 0 },
    'failed')
}

async function runCancel(page: Page, request: APIRequestContext, room: { id: string; name: string },
  tracker: JSONMap, env: JSONMap, module: JSONMap) {
  const started = await startFreshStandardTask(page, request, room,
    'O4 cancel: remain active until the visible user Cancel action is observed.')
  await waitForAttemptState(request, started.path, ['running'])
  const cancel = await uiMutation(page, new RegExp(`^/api/runs/${escapeRE(started.runId)}/cancel$`), async () => {
    await page.getByRole('button', { name: /Cancel Run|Cancel/ }).first().click()
  })
  expect(cancel.ok()).toBeTruthy()
  const run = await waitForRunStatus(request, started.path, ['cancelled'])
  assertNoPatchCheckReview(run, 'cancel')
  return captureTerminalScenario(page, request, { ...started, run }, 2, 'cancel', tracker, env, module,
    { cancelRequestedThroughPublicUI: true, processTreeDead: true, retryAllowed: true }, 'canceled')
}

async function runTimeout(page: Page, request: APIRequestContext, plan: PreparedTask,
  tracker: JSONMap, env: JSONMap, module: JSONMap) {
  const started = await startAcceptedPlan(page, request, plan)
  const run = await waitForRunStatus(request, started.path, ['failed'])
  expect(run.attemptDetail).toMatchObject({ state: 'failed', terminalReason: 'attempt_timeout' })
  assertNoPatchCheckReview(run, 'timeout')
  return captureTerminalScenario(page, request, { ...started, run }, 3, 'timeout', tracker, env, module,
    { deadlineAuthorityConsumed: true, processTreeDead: true, automaticRetryCount: 0 }, 'timed_out')
}

async function runRejectedResultRetry(page: Page, request: APIRequestContext, room: { id: string; name: string },
  tracker: JSONMap, env: JSONMap, module: JSONMap) {
  const started = await startFreshStandardTask(page, request, room,
    'O4 Retry: first produce a reviewable Result that will be rejected, then repair only through visible Retry.')
  const predecessor = await waitForRunStatus(request, started.path, ['awaiting_review'])
  const predecessorAttemptId = requiredString(predecessor.attemptDetail?.id, 'retry predecessor Attempt ID')
  await uiMutation(page, new RegExp(`^/api/runs/${escapeRE(started.runId)}/review$`), async () => {
    await page.getByLabel(/Review note|review note/i).fill('Reject this exact Result so the public Retry successor can prove the relation.')
    await page.getByRole('button', { name: /Request Changes|Reject/ }).click()
  })
  await waitForRunStatus(request, started.path, ['revision_required'])
  await uiMutation(page, new RegExp(`^/api/runs/${escapeRE(started.runId)}/retry$`), async () => {
    await page.getByRole('button', { name: /Retry/ }).click()
    const instructions = page.getByLabel(/Retry instructions|Instructions/i)
    if (await instructions.count()) await instructions.fill('Repair only the rejected Result without hidden or framework retry.')
    const confirm = page.getByRole('button', { name: /Start Retry|Retry now|Retry/, exact: false }).last()
    if (await confirm.count()) await confirm.click()
  }, 120_000)
  const run = await waitForRunStatus(request, started.path, ['awaiting_review'])
  const successorAttemptId = requiredString(run.attemptDetail?.id, 'retry successor Attempt ID')
  expect(successorAttemptId).not.toBe(predecessorAttemptId)
  return captureSuccessfulScenario(page, request, { ...started, run }, 4, 'retry', tracker, env, module, {
    predecessorAttemptId, predecessorState: 'rejected', predecessorReason: 'result_rejected',
    successorAttemptId, successorState: 'output_submitted', uiRetry: true,
  })
}

async function runSuccess(page: Page, request: APIRequestContext, room: { id: string; name: string },
  sequence: number, name: string, tracker: JSONMap, env: JSONMap, module: JSONMap, proof: JSONMap) {
  const started = await startFreshStandardTask(page, request, room, `O4 ${name}: produce one bounded successful Result.`)
  const run = await waitForRunStatus(request, started.path, ['awaiting_review'])
  return captureSuccessfulScenario(page, request, { ...started, run }, sequence, name, tracker, env, module, proof)
}

async function runApply(page: Page, request: APIRequestContext, room: { id: string; name: string },
  tracker: JSONMap, env: JSONMap, module: JSONMap) {
  const started = await startFreshStandardTask(page, request, room,
    'O4 Apply: produce one independently verified bounded patch for visible Accept & Apply.')
  const reviewReady = await waitForRunStatus(request, started.path, ['awaiting_review'])
  const patchPath = requiredString(reviewReady.reviewablePatch?.rawDownload,
    'public reviewable Patch download path')
  const patchResponse = await request.get(patchPath)
  expect(patchResponse.ok()).toBeTruthy()
  const patchBytes = await patchResponse.body()
  assert(patchBytes.length > 0 && patchBytes.length <= 16 * 1024 * 1024 &&
    sha256(patchBytes) === requiredDigest(reviewReady.reviewablePatch?.patchDigest,
      'pre-Apply public Patch digest'), 'public reviewable Patch bytes drifted')
  const executionLogBytes = exactApplyExecutionLog(reviewReady)
  const responses = await Promise.all([
    page.waitForResponse((candidate) => new URL(candidate.url()).pathname === `/api/runs/${started.runId}/review`),
    page.waitForResponse((candidate) => new URL(candidate.url()).pathname === `/api/runs/${started.runId}/apply`),
    page.getByRole('button', { name: 'Accept & Apply' }).click(),
  ])
  expect(responses[0].ok()).toBeTruthy()
  expect(responses[1].ok()).toBeTruthy()
  const responseBody = await responses[1].body()
  assert(responseBody.length > 0 && responseBody.length <= 4 * 1024 * 1024,
    'Accept & Apply Response.body is missing or oversized')
  const run = safeJSONBytes(responseBody, 'Accept & Apply')
  expect(run.verifiedReview?.kind).toBe('accept')
  expect(run.patchApplication).toMatchObject({ state: 'applied' })
  const patchDigest = requiredDigest(
    run.patchApplication?.patchDigest ?? run.reviewablePatch?.patchDigest ?? reviewReady.reviewablePatch?.patchDigest,
    'public accepted/applied Patch digest')
  const observation = await captureTerminalScenario(page, request, { ...started, run }, 8, 'apply', tracker, env, module, {
    reviewKind: 'accept', verificationState: 'completed', applicationState: 'applied',
    taskOwned: true, patchDigest, payloadSha256: sha256(responseBody),
  }, 'succeeded', { patchState: 'applied', reviewState: 'accepted' })
  return { observation, responseBody, patchBytes, executionLogBytes }
}

function exactApplyExecutionLog(run: JSONMap) {
  const attempts = run.verification?.attempts
  assert(Array.isArray(attempts) && attempts.length > 0, 'Apply verification attempts are missing')
  const commands = attempts.flatMap((attempt: JSONMap) => attempt.commands ?? [])
  assert(commands.length > 0, 'Apply verification command logs are missing')
  const streams = commands.map((command: JSONMap) => {
    const result: JSONMap = { commandId: requiredString(command.commandId, 'verification command ID') }
    for (const name of ['stdout', 'stderr']) {
      const stream = command[name]
      assert(stream && stream.truncated === false && typeof stream.retainedBody === 'string' &&
        Buffer.byteLength(stream.retainedBody) === stream.totalBytes &&
        sha256(Buffer.from(stream.retainedBody)) === stream.fullSha256,
      `Apply verification ${name} is not one complete authenticated stream`)
      result[name] = { fullSha256: stream.fullSha256, bytes: stream.totalBytes,
        body: stream.retainedBody, redactionPolicyVersion: stream.redactionPolicyVersion }
    }
    return result
  })
  return Buffer.from(`${JSON.stringify({
    schemaVersion: 'chora.m1-o4-apply-execution-log-source.v1',
    runId: run.id, verificationId: run.verification.id, commands: streams,
  }, null, 2)}\n`)
}

async function recoverOrphanThroughPublicUI(page: Page, request: APIRequestContext, started: JSONMap) {
  const recovery = await waitForRunStatus(request, started.path, ['recovery_required'])
  expect(recovery.attemptDetail?.state).toBe('recovery_required')
  await uiMutation(page, new RegExp(`^/api/runs/${escapeRE(started.runId)}/retry$`), async () => {
    await page.getByRole('button', { name: /Recover|Retry/ }).first().click()
    const instructions = page.getByLabel(/Retry instructions|Instructions/i)
    if (await instructions.count()) await instructions.fill('Recover only the interrupted public Attempt after authenticated restart.')
    const confirm = page.getByRole('button', { name: /Start Retry|Recover|Retry/, exact: false }).last()
    if (await confirm.count()) await confirm.click()
  }, 120_000)
  const run = await waitForRunStatus(request, started.path, ['awaiting_review'])
  return { ...started, run }
}

async function captureSuccessfulScenario(page: Page, request: APIRequestContext, subject: JSONMap,
  sequence: number, name: string, tracker: JSONMap, env: JSONMap, module: JSONMap,
  proof: JSONMap, terminalStatus = 'succeeded') {
  expect(subject.run.attemptDetail?.state).toBe('output_submitted')
  expect(subject.run.verification).toMatchObject({ state: 'completed' })
  return captureTerminalScenario(page, request, subject, sequence, name, tracker, env, module,
    proof, terminalStatus)
}

async function captureTerminalScenario(page: Page, request: APIRequestContext, subject: JSONMap,
  sequence: number, name: string, tracker: JSONMap, env: JSONMap, module: JSONMap,
  proof: JSONMap, terminalStatus: string, overrides: JSONMap = {}) {
  const run = subject.run
  const attemptId = requiredString(run.attemptDetail?.id, `${name} Attempt ID`)
  const publicObservationDigest = productObservationDigest('D', sequence, run)
  await publishProductEvidenceBoundary(request, env, {
    sequence, taskId: subject.taskId, runId: subject.runId, attemptId,
    observationDigest: publicObservationDigest,
  }, name)
  const resourceFile = join(env.supervisorResourceDirectory,
    `${String(sequence).padStart(2, '0')}-${name}-resources.json`)
  const resource = await readOwner0400JSON(resourceFile, `${name} supervisor resource`)
  const sourceA3 = await tracker.nextA3(name)
  const verifierSource = ['failure', 'cancel', 'timeout'].includes(name) ? null : await tracker.nextVerifier(name)
  const residue = await assertPublicResidueZero(request)
  await writeGenerationReference(env, sequence, name, residue)
  await captureAndScanScreenshot(page, env, sequence, name, module)
  const noResult = ['failure', 'cancel', 'timeout'].includes(name)
  const targetId = requiredString(
    run.patchApplication?.targetId ?? run.reviewablePatch?.targetId ?? resource.targetId,
    `${name} target ID`)
  const scenarioProof = name === 'apply' ? { ...proof, targetId } : proof
  return {
    sequence, name, taskId: subject.taskId, runId: subject.runId, attemptId,
    workspaceId: requiredString(resource.workspaceId, `${name} workspace ID`),
    targetId,
    attemptState: run.attemptDetail.state,
    terminalReason: run.attemptDetail.terminalReason ?? null,
    terminalStatus, publicEntry: true, cleanupComplete: true,
    patchState: overrides.patchState ?? (noResult ? 'absent' : 'present'),
    checkState: noResult ? 'absent' : 'passed',
    reviewState: overrides.reviewState ?? (noResult ? 'absent' : 'awaiting_review'),
    sourceA3, verifierSource, residue: zeroResidue(), scenarioProof,
    publicActionDigest: sha256(canonical({ sequence, name, taskId: subject.taskId, runId: subject.runId })),
    publicObservationDigest, completedAt: new Date().toISOString(),
  } as ScenarioObservation
}

async function sourceTracker(a3File: string, verifierFile: string) {
  let a3End = (await readOwner0600JSON(a3File, 'source A3 prefix')).attempts.length
  let verifierEnd = (await readOwner0600JSON(verifierFile, 'Verifier prefix')).attempts.length
  return {
    async nextA3(name: string) {
      const ledger = await readOwner0600JSON(a3File, `source A3 after ${name}`)
      assert(Array.isArray(ledger.attempts) && ledger.attempts.length > a3End, `source A3 did not advance for ${name}`)
      const startSequence = a3End + 1
      const endSequence = ledger.attempts.length
      a3End = endSequence
      return { startSequence, endSequence,
        sliceDigest: sha256(canonical(ledger.attempts.slice(startSequence - 1, endSequence))) }
    },
    async nextVerifier(name: string) {
      const ledger = await readOwner0600JSON(verifierFile, `Verifier after ${name}`)
      assert(Array.isArray(ledger.attempts) && ledger.attempts.length > verifierEnd,
        `Verifier did not advance for ${name}`)
      const startSequence = verifierEnd + 1
      const endSequence = ledger.attempts.length
      verifierEnd = endSequence
      return { startSequence, endSequence,
        sliceDigest: sha256(canonical(ledger.attempts.slice(startSequence - 1, endSequence))) }
    },
  }
}

async function publishProductEvidenceBoundary(request: APIRequestContext, env: JSONMap,
  observation: JSONMap, name: string) {
  const authority = await readOwner0400JSON(env.totalSessionAuthorityFile,
    'O4 evidence Runner session authority')
  expect(authority).toMatchObject({
    schemaVersion: 'chora.m1-o4-runner-session-authority.v1', status: 'exclusive',
    tupleIdentity: env.tupleIdentity,
  })
  expect(authority.sessionId).toMatch(/^[0-9a-f]{64}$/)
  expect(authority.sessionSecret).toMatch(/^[0-9a-f]{64}$/)
  const selectors = {
    phase: 'D', sequence: observation.sequence, taskId: observation.taskId,
    runId: observation.runId, attemptId: observation.attemptId,
    observationDigest: observation.observationDigest,
  }
  const body = {
    ...selectors, sessionId: authority.sessionId, tupleIdentity: env.tupleIdentity,
    generationId: authority.identity?.generationId,
    mac: createHmac('sha256', authority.sessionSecret).update(canonical(selectors)).digest('hex'),
  }
  const response = await request.post('/api/o4/evidence-boundaries', { data: body })
  expect(response.status(), `${name} product evidence boundary publication`).toBe(201)
  const value = await response.json() as JSONMap
  expect(Object.keys(value).sort()).toEqual([
    'schemaVersion', 'status', 'phase', 'sequence', 'observationDigest',
    'operationLedgerSha256', 'operationLedgerCount', 'a3LedgerSha256',
    'verifierLedgerSha256', 'resourcePath', 'resourceSha256',
  ].sort())
  expect(value).toMatchObject({
    schemaVersion: 'chora.m1-o4-evidence-boundary-response.v1', status: 'published',
    phase: 'D', sequence: observation.sequence,
    observationDigest: observation.observationDigest,
    resourcePath: join(env.supervisorResourceDirectory,
      `${String(observation.sequence).padStart(2, '0')}-${name}-resources.json`),
  })
  for (const field of ['operationLedgerSha256', 'a3LedgerSha256',
    'verifierLedgerSha256', 'resourceSha256']) expect(value[field]).toMatch(/^[0-9a-f]{64}$/)
}

function productObservationDigest(phase: 'D', sequence: number, run: JSONMap) {
  const runtime = run.attemptDetail?.runtime
  const projectedRuntime = runtime ? {
    sessionId: String(runtime.sessionId ?? ''), adapterId: String(runtime.adapterId ?? ''),
    kind: String(runtime.kind ?? ''), version: String(runtime.version ?? ''),
    fingerprint: String(runtime.fingerprint ?? ''),
    ...(runtime.externalSession ? { externalSession: String(runtime.externalSession) } : {}),
    state: String(runtime.state ?? ''),
  } : null
  const verification = run.verification ? {
    id: String(run.verification.id ?? ''), state: String(run.verification.state ?? ''),
    bindings: run.verification.bindings,
    attempts: (run.verification.attempts ?? []).map((attempt: JSONMap) => ({
      id: String(attempt.id ?? ''), sequence: attempt.sequence,
      state: String(attempt.state ?? ''), evidenceComplete: attempt.evidenceComplete === true,
      cleanupProven: attempt.cleanupProven === true,
      workspaceIdentity: String(attempt.workspaceIdentity ?? ''), reason: String(attempt.reason ?? ''),
    })),
    result: run.verification.result ? {
      id: String(run.verification.result.id ?? ''),
      attemptId: String(run.verification.result.attemptId ?? ''),
      outcome: String(run.verification.result.outcome ?? ''),
    } : null,
  } : null
  return sha256(canonical({
    schemaVersion: 'chora.m1-o4-product-observation.v1', phase, sequence,
    taskId: String(run.task?.id ?? ''), runId: String(run.id ?? ''),
    attemptId: String(run.attemptDetail?.id ?? ''),
    run: { state: String(run.status ?? ''), version: run.version,
      terminalReason: String(run.terminalReason ?? '') },
    attempt: { sequence: run.attemptDetail?.sequence ?? 0,
      state: String(run.attemptDetail?.state ?? ''),
      profile: String(run.attemptDetail?.agentExecution?.profile ?? ''), runtime: projectedRuntime },
    verification,
    patch: {
      reviewablePatchDigest: String(run.reviewablePatch?.patchDigest ?? ''),
      applicationState: String(run.patchApplication?.state ?? ''),
      applicationPatchDigest: String(run.patchApplication?.patchDigest ?? ''),
      targetIdentity: String(run.patchApplication?.targetIdentity ?? ''),
    },
  }))
}

async function captureAndScanScreenshot(page: Page, env: JSONMap, sequence: number, name: string, module: JSONMap) {
  const stem = `${String(sequence).padStart(2, '0')}-${name}`
  const pngFile = join(env.screenshotDirectory, `${stem}.png`)
  const metadataFile = join(env.screenshotMetadataDirectory, `${stem}-metadata.json`)
  const ocrFile = join(env.screenshotOCRDirectory, `${stem}-ocr.json`)
  const repositoryCapture = Object.freeze({
    executableFile: env.serviceControllerExecutable,
    executableSha256: env.serviceControllerSha256,
  })
  const png = await page.screenshot({ type: 'png', fullPage: true })
  await writeExclusive0400(pngFile, png, repositoryCapture)
  const chunkTypes = pngChunkTypes(png)
  assert(!chunkTypes.some((type) => ['tEXt', 'zTXt', 'iTXt', 'eXIf'].includes(type)),
    `${name} screenshot contains unsafe metadata`)
  await writeExclusive0400(metadataFile, Buffer.from(`${JSON.stringify({
    schemaVersion: module.SCREENSHOT_METADATA_SCHEMA, pngSha256: sha256(png),
    completed: true, chunkTypes, unsafeMatchCount: 0,
  })}\n`), repositoryCapture)
  await module.runPinnedOfflineOCR({
    executableFile: env.ocrExecutable, executableSha256: env.ocrExecutableSha256,
    engineBindingFile: env.ocrEngineBindingFile,
    engineBindingSha256: env.ocrEngineBindingSha256,
    sandboxExecutable: env.ocrSandboxExecutable,
    sandboxExecutableSha256: env.ocrSandboxExecutableSha256,
    sandboxProfileFile: env.ocrSandboxProfileFile,
    sandboxProfileSha256: env.ocrSandboxProfileSha256,
    language: env.ocrLanguage, pngFile, outputFile: ocrFile,
  })
}

async function writeGenerationReference(env: JSONMap, sequence: number, name: string, residue: JSONMap) {
  const safe = { schemaVersion: 'chora.m1-o4-recovery-generation-reference.v1', status: 'terminal',
    sequence, scenario: name, generationId: env.generationId, tupleIdentity: env.tupleIdentity,
    servingCount: residue.generationReferences.serving.count,
    activeAttemptCount: residue.generationReferences.activeAttempts.count,
    recoverableAttemptCount: residue.generationReferences.recoverableAttempts.count,
    currentGenerationSha256: residue.generationReferences.currentGenerationSha256 }
  await writeExclusive0400(join(env.generationReferenceDirectory,
    `${String(sequence).padStart(2, '0')}-${name}-generation.json`),
  Buffer.from(`${JSON.stringify({ ...safe, referenceDigest: sha256(canonical(safe)) })}\n`), {
    executableFile: env.serviceControllerExecutable,
    executableSha256: env.serviceControllerSha256,
  })
}

async function assertPublicResidueZero(request: APIRequestContext) {
  const response = await request.get('/api/o4/residue')
  expect(response.ok()).toBeTruthy()
  const value = await response.json() as JSONMap
  expect(value).toMatchObject({ schemaVersion: 'chora.m1-o4-residue-proof.v1', status: 'proven_zero' })
  for (const key of ['containers', 'verifierContainers', 'networks', 'volumes', 'configs', 'managedWorkspaces', 'attemptProcessGroups']) {
    expect(value.docker?.[key]?.count, `public residue ${key}`).toBe(0)
  }
  expect(value.verificationWorkspaces?.count).toBe(0)
  expect(value.generationReferences).toMatchObject({ serving: { count: 1 },
    activeAttempts: { count: 0 }, recoverableAttempts: { count: 0 } })
  return value
}

async function waitForRunStatus(request: APIRequestContext, path: string, statuses: string[]) {
  const deadline = Date.now() + 30 * 60_000
  while (Date.now() < deadline) {
    const response = await request.get(path)
    expect(response.ok()).toBeTruthy()
    const run = await response.json() as JSONMap
    if (statuses.includes(run.status)) return run
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 1_000))
  }
  throw new Error(`Run did not reach ${statuses.join('/')}`)
}

async function waitForAttemptState(request: APIRequestContext, path: string, states: string[]) {
  const deadline = Date.now() + 10 * 60_000
  while (Date.now() < deadline) {
    const response = await request.get(path)
    expect(response.ok()).toBeTruthy()
    const run = await response.json() as JSONMap
    if (states.includes(run.attemptDetail?.state)) return run
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 500))
  }
  throw new Error(`Attempt did not reach ${states.join('/')}`)
}

async function returnToRoomThroughDirectory(page: Page, roomName: string) {
  await page.goto('/', { waitUntil: 'networkidle' })
  await page.getByText(roomName, { exact: true }).first().click()
  await expect(page.getByRole('button', { name: '＋ New Task' }).first()).toBeVisible()
}

async function uiMutation(page: Page, path: RegExp, action: () => Promise<void>, timeout = 30_000): Promise<Response> {
  const pending = page.waitForResponse((response) => path.test(new URL(response.url()).pathname) &&
    !['GET', 'HEAD'].includes(response.request().method()), { timeout })
  await action()
  return pending
}

function assertNoPatchCheckReview(run: JSONMap, name: string) {
  assert(run.reviewablePatch == null && run.verification == null && run.verifiedReview == null,
    `${name} created Patch/check/review state`)
}

function assertScenarioOrderAndIdentity(observations: ScenarioObservation[]) {
  const names = ['failure', 'cancel', 'timeout', 'retry', 'restart', 'orphan', 'reopen', 'apply']
  expect(observations.map(({ sequence, name }) => [sequence, name])).toEqual(names.map((name, index) => [index + 1, name]))
  for (const field of ['taskId', 'runId', 'attemptId', 'workspaceId', 'targetId'] as const) {
    expect(new Set(observations.map((value) => value[field])).size, `${field} uniqueness`).toBe(8)
  }
}

function sharedImagesFromB2(config: JSONMap) {
  const roles = config.authority.roles
  return [
    { artifactId: 'managed-pi-runtime', roles: ['managed_pi_runtime', 'independent_verifier', 'capability_probe'],
      archiveSha256: roles.managed_pi_runtime.archiveSha256,
      archiveSize: roles.managed_pi_runtime.archiveSize,
      dockerConfigImageId: roles.managed_pi_runtime.dockerConfigImageId, present: true },
    { artifactId: 'network-boundary', roles: ['network_boundary'],
      archiveSha256: roles.network_boundary.archiveSha256,
      archiveSize: roles.network_boundary.archiveSize,
      dockerConfigImageId: roles.network_boundary.dockerConfigImageId, present: true },
  ]
}

function harnessEnvironment() {
  const value = (name: string) => {
    const result = process.env[name]
    if (!result) throw new Error(`${name} is required`)
    return result
  }
  const result: JSONMap = {
    tupleFile: value('CHORA_O4_RECOVERY_RESIDUE_TUPLE_FILE'),
    phaseCReceiptFile: value('CHORA_O4_RECOVERY_RESIDUE_PHASE_C_RECEIPT_FILE'),
    handoffReceiptDirectory: value('CHORA_O4_RECOVERY_RESIDUE_HANDOFF_RECEIPT_DIR'),
    b2ConfigFile: value('CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE'),
    b2ConfigSha256: value('CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_SHA256'),
    b2RunnerModuleFile: value('CHORA_O4_RECOVERY_RESIDUE_B2_RUNNER_MODULE_FILE'),
    b2RunnerModuleSha256: value('CHORA_O4_RECOVERY_RESIDUE_B2_RUNNER_MODULE_SHA256'),
    authorityFile: value('CHORA_O4_RECOVERY_RESIDUE_AUTHORITY_FILE'),
    authorizedRestartReceiptFile: value('CHORA_O4_RECOVERY_RESIDUE_AUTHORIZED_RESTART_RECEIPT_FILE'),
    orphanRestartReceiptFile: value('CHORA_O4_RECOVERY_RESIDUE_ORPHAN_RESTART_RECEIPT_FILE'),
    sourceA3LedgerFile: value('CHORA_O4_RECOVERY_RESIDUE_SOURCE_A3_LEDGER_FILE'),
    verifierLedgerFile: value('CHORA_O4_RECOVERY_RESIDUE_VERIFIER_LEDGER_FILE'),
    authorityConsumptionDirectory: value('CHORA_O4_RECOVERY_RESIDUE_AUTHORITY_CONSUMPTION_DIR'),
    supervisorResourceDirectory: value('CHORA_O4_RECOVERY_RESIDUE_SUPERVISOR_RESOURCE_DIR'),
    generationReferenceDirectory: value('CHORA_O4_RECOVERY_RESIDUE_GENERATION_REFERENCE_DIR'),
    publicResidueFile: value('CHORA_O4_RECOVERY_RESIDUE_PUBLIC_RESIDUE_FILE'),
    serviceControlFile: value('CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROL_FILE'),
    serviceControllerExecutable: value('CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_FILE'),
    serviceControllerSha256: value('CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_SHA256'),
    serviceControllerRequestFile: value('CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROLLER_REQUEST_FILE'),
    ocrExecutable: value('CHORA_O4_RECOVERY_RESIDUE_OCR_EXECUTABLE_FILE'),
    ocrExecutableSha256: value('CHORA_O4_RECOVERY_RESIDUE_OCR_EXECUTABLE_SHA256'),
    ocrEngineBindingFile: value('CHORA_O4_RECOVERY_RESIDUE_OCR_ENGINE_BINDING_FILE'),
    ocrEngineBindingSha256: value('CHORA_O4_RECOVERY_RESIDUE_OCR_ENGINE_BINDING_SHA256'),
    ocrSandboxExecutable: value('CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_EXECUTABLE_FILE'),
    ocrSandboxExecutableSha256: value('CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_EXECUTABLE_SHA256'),
    ocrSandboxProfileFile: value('CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_PROFILE_FILE'),
    ocrSandboxProfileSha256: value('CHORA_O4_RECOVERY_RESIDUE_OCR_SANDBOX_PROFILE_SHA256'),
    ocrLanguage: value('CHORA_O4_RECOVERY_RESIDUE_OCR_LANGUAGE'),
    screenshotDirectory: value('CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_DIR'),
    screenshotMetadataDirectory: value('CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_METADATA_DIR'),
    screenshotOCRDirectory: value('CHORA_O4_RECOVERY_RESIDUE_SCREENSHOT_OCR_DIR'),
    outputDirectory: value('CHORA_O4_RECOVERY_RESIDUE_OUTPUT_DIR'),
    bindingDigest: value('CHORA_O4_RECOVERY_RESIDUE_BINDING_DIGEST'),
    totalManifestFile: value('CHORA_O4_TOTAL_MANIFEST_FILE'),
    totalManifestSha256: value('CHORA_O4_TOTAL_MANIFEST_SHA256'),
    totalSessionAuthorityFile: value('CHORA_O4_TOTAL_SESSION_AUTHORITY_FILE'),
    totalTupleIdentity: value('CHORA_O4_TOTAL_TUPLE_IDENTITY'),
    totalRunnerSha256: value('CHORA_O4_TOTAL_RUNNER_SHA256'),
  }
  return result
}

async function readProductEvidenceAuthority(env: JSONMap, tupleIdentity: string) {
  const manifestBytes = await readOwnerFile(env.totalManifestFile, 'O4 total manifest',
    16 * 1024 * 1024, 0o400)
  assert(sha256(manifestBytes) === env.totalManifestSha256,
    'product evidence total manifest SHA-256 drifted')
  const manifest = safeJSONBytes(manifestBytes, 'product evidence total manifest')
  const c = manifest.phases?.C?.environment
  const d = manifest.phases?.D?.environment
  assert(c && d && c.CHORA_O4_PROFILE_REUSE_SOURCE_A3_LEDGER_FILE === env.sourceA3LedgerFile &&
    d.CHORA_O4_RECOVERY_RESIDUE_SOURCE_A3_LEDGER_FILE === env.sourceA3LedgerFile &&
    d.CHORA_O4_RECOVERY_RESIDUE_VERIFIER_LEDGER_FILE === env.verifierLedgerFile &&
    d.CHORA_O4_RECOVERY_RESIDUE_SUPERVISOR_RESOURCE_DIR === env.supervisorResourceDirectory,
  'product evidence phase path binding drifted')
  const sessionBytes = await readOwnerFile(env.totalSessionAuthorityFile,
    'product evidence Runner session authority', 4 * 1024 * 1024, 0o400)
  return Object.freeze({
    sessionAuthorityFile: env.totalSessionAuthorityFile,
    sessionAuthoritySha256: sha256(sessionBytes),
    tupleIdentity,
    sourceA3LedgerFile: env.sourceA3LedgerFile,
    verifierLedgerFile: env.verifierLedgerFile,
    cResourceDirectory: requiredString(c.CHORA_O4_PROFILE_REUSE_SUPERVISOR_RESOURCE_DIR,
      'phase-C product resource directory'),
    dResourceDirectory: env.supervisorResourceDirectory,
  })
}

async function assertTotalSessionBinding(env: JSONMap, phase: 'D') {
  const manifestBytes = await readOwnerFile(env.totalManifestFile, 'O4 total manifest',
    16 * 1024 * 1024, 0o400)
  expect(sha256(manifestBytes)).toBe(requiredDigest(env.totalManifestSha256, 'total manifest SHA-256'))
  const manifest = safeJSONBytes(manifestBytes, 'O4 total manifest')
  expect(manifest).toMatchObject({ schemaVersion: 'chora.m1-o4-total-input.v1',
    status: 'authorized', acceptanceClass: 'real_acceptance' })
  expect(manifest.selfDigest).toBe(sha256(canonical({ ...manifest, selfDigest: null })))
  expect(manifest.runner?.moduleFile).toBe(env.b2RunnerModuleFile)
  expect(manifest.runner?.moduleSha256).toBe(env.b2RunnerModuleSha256)
  expect(manifest.runner?.moduleSha256).toBe(requiredDigest(env.totalRunnerSha256, 'total Runner SHA-256'))
  expect(manifest.candidate?.tupleIdentity).toBeNull()
  const phaseEnvironment = manifest.phases?.[phase]?.environment
  expect(phaseEnvironment && typeof phaseEnvironment === 'object').toBe(true)
  for (const [name, value] of Object.entries(phaseEnvironment)) {
    if (name === 'CHORA_O4_RECOVERY_RESIDUE_BINDING_DIGEST') {
      expect(value).toBeNull()
      expect(process.env[name]).toBe(env.bindingDigest)
    } else expect(process.env[name]).toBe(value)
  }
  const authority = await readOwner0400JSON(env.totalSessionAuthorityFile, 'O4 Runner session authority')
  expect(authority).toMatchObject({ schemaVersion: 'chora.m1-o4-runner-session-authority.v1',
    status: 'exclusive', manifestSha256: env.totalManifestSha256,
    runnerSha256: env.totalRunnerSha256, tupleIdentity: env.totalTupleIdentity, sequenceStart: 0 })
  expect(authority.socketPathDigest).toBe(sha256(String(manifest.runner.controlSocketPath)))
  const { authorityDigest, ...safe } = authority
  expect(authorityDigest).toBe(sha256(canonical(safe)))
  const tuple = await readOwner0400JSON(env.tupleFile, 'O4 total phase-D tuple')
  expect(tuple.identity).toBe(env.totalTupleIdentity)
}

async function readPinnedJSON(path: string, expected: string, label: string) {
  const bytes = await readOwnerFile(path, label, 4 * 1024 * 1024, 0o400)
  assert(sha256(bytes) === expected, `${label} SHA-256 drifted`)
  try { return JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) }
}

async function readPinnedModule(path: string, expected: string, label: string) {
  const bytes = await readOwnerFile(path, label, 4 * 1024 * 1024, 0o400)
  assert(sha256(bytes) === expected, `${label} SHA-256 drifted`)
  const loaded = await nativeImport(pathToFileURL(path).href)
  assert(sha256(await readFile(path)) === expected, `${label} changed during import`)
  return loaded
}

async function readOwner0400JSON(path: string, label: string) {
  const bytes = await readOwnerFile(path, label, 8 * 1024 * 1024, 0o400)
  try { return JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) }
}

async function readOwner0600JSON(path: string, label: string) {
  const bytes = await readOwnerFile(path, label, 8 * 1024 * 1024, 0o600)
  try { return JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) }
}

async function readOwnerFile(path: string, label: string, max: number, expectedMode: number) {
  const info = await lstat(path)
  assert(info.isFile() && !info.isSymbolicLink() && info.nlink === 1 && info.uid === ownerUID() &&
    info.size > 0 && info.size <= max && (info.mode & 0o777) === expectedMode,
  `${label} is not one owner-only immutable bounded file`)
  return readFile(path)
}

async function writeExclusive0400(path: string, bytes: Buffer,
  repositoryCapture: { executableFile: string; executableSha256: string }) {
  const parent = await lstat(dirname(path))
  assert(parent.isDirectory() && !parent.isSymbolicLink() && parent.uid === ownerUID() &&
    (parent.mode & 0o777) === 0o700, 'raw evidence parent is not owner-only 0700')
  let handle
  let created = false
  let identity
  try {
    handle = await open(path, 'wx', 0o600)
    created = true
    await handle.writeFile(bytes)
    await handle.sync()
    await handle.chmod(0o400)
    await handle.sync()
    identity = ownedPathIdentity(await handle.stat({ bigint: true }))
    await handle.close()
    handle = undefined
    const persisted = await lstat(path, { bigint: true })
    assert(persisted.isFile() && !persisted.isSymbolicLink() && persisted.nlink === 1n &&
      persisted.dev.toString() === identity.dev && persisted.ino.toString() === identity.ino &&
      Number(persisted.mode) === identity.mode && Number(persisted.uid) === identity.uid &&
      Number(persisted.gid) === identity.gid && Number(persisted.mode & 0o777n) === 0o400,
    'raw evidence final path identity drifted')
  } catch (error) {
    const cleanupErrors = []
    if (handle) {
      try { identity = ownedPathIdentity(await handle.stat({ bigint: true })) }
      catch (identityError) { cleanupErrors.push(identityError) }
      try { await handle.close() } catch (closeError) { cleanupErrors.push(closeError) }
      handle = undefined
    }
    if (created && identity !== undefined) {
      try {
        await cleanupOwnedPath({ capability: repositoryCapture, path, identity,
          disposition: 'regular_file' })
      } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    } else if (created) {
      try {
        await lstat(path)
        cleanupErrors.push(new Error('raw evidence creation failed without an exact cleanup identity'))
      } catch (absenceError: any) {
        if (absenceError?.code !== 'ENOENT') cleanupErrors.push(absenceError)
      }
    }
    if (cleanupErrors.length > 0) throw new AggregateError(
      [error, ...cleanupErrors], 'raw evidence creation and native cleanup failed', { cause: error })
    throw error
  }
  return identity
}

async function writeControllerRequest(path: string, value: JSONMap,
  repositoryCapture: { executableFile: string; executableSha256: string }) {
  await writeExclusive0400(path, Buffer.from(`${JSON.stringify(value)}\n`), repositoryCapture)
}

function pngChunkTypes(png: Buffer) {
  assert(png.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])), 'screenshot is not PNG')
  const result: string[] = []
  let offset = 8
  while (offset + 12 <= png.length) {
    const length = png.readUInt32BE(offset)
    assert(offset + 12 + length <= png.length, 'PNG chunk boundary drifted')
    const type = png.subarray(offset + 4, offset + 8).toString('ascii')
    result.push(type)
    offset += 12 + length
    if (type === 'IEND') break
  }
  assert(result[0] === 'IHDR' && result.at(-1) === 'IEND' && offset === png.length, 'PNG stream drifted')
  return result
}

async function safeJSON(response: Response, label: string) {
  try { return await response.json() as JSONMap } catch { throw new Error(`${label} response is not JSON`) }
}
function safeJSONBytes(bytes: Buffer, label: string) {
  try { return JSON.parse(bytes.toString('utf8')) as JSONMap } catch { throw new Error(`${label} response is not JSON`) }
}
function zeroResidue() { return { ownedContainers: 0, ownedNetworks: 0, ownedVolumes: 0,
  ownedConfigs: 0, ownedWorkspaces: 0, ownedProcessGroups: 0, activeReferences: 0, recoverableReferences: 0 } }
function requiredString(value: unknown, label: string) { assert(typeof value === 'string' && value.length > 0, `${label} is missing`); return value }
function requiredDigest(value: unknown, label: string) { assert(typeof value === 'string' && /^[0-9a-f]{64}$/.test(value), `${label} is invalid`); return value }
function requiredNonzero(value: unknown, label: string) { assert(Number.isSafeInteger(value) && value !== 0, `${label} is invalid`); return value as number }
function ownerUID() { assert(typeof process.getuid === 'function', 'owner UID is unavailable'); return process.getuid() }
function assertRunner(value: JSONMap) { exactKeys(value, ['exec', 'installImmutable', 'processBoundary', 'start', 'armAuthenticatedControllerRecovery', 'awaitAuthenticatedControllerProof', 'exportApplyDisclosureSources', 'abortApplyDisclosureExporter'], 'B2 recovery Runner'); for (const key of Object.keys(value)) assert(typeof value[key] === 'function', `Runner ${key} is unavailable`) }
function exactKeys(value: JSONMap, keys: string[], label: string) { assert(value && typeof value === 'object' && !Array.isArray(value), `${label} is not an object`); expect(Object.keys(value).sort(), `${label} fields`).toEqual([...keys].sort()) }
function escapeRE(value: string) { return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') }
function sha256(value: string | Buffer) { return createHash('sha256').update(value).digest('hex') }
function canonical(value: any): string { if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`; if (value && typeof value === 'object') return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`; return JSON.stringify(value) }
function assert(condition: unknown, message: string): asserts condition { if (!condition) throw new Error(message) }
