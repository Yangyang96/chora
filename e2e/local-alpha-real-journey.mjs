import { spawn } from 'node:child_process'
import { createHash, randomUUID } from 'node:crypto'
import { chmod, mkdir, readFile, readdir, stat, writeFile } from 'node:fs/promises'
import { dirname, join, relative, resolve, sep } from 'node:path'
import { chromium } from 'playwright'

import { auditDisclosureBoundary, assertNoCredentialLeakInTree } from './disclosure-gate.mjs'

const installRoot = absoluteRequired('CHORA_INSTALL_ROOT')
const dataRoot = absoluteRequired('CHORA_DATA_ROOT')
const sourceRoot = absoluteRequired('CHORA_SOURCE_ROOT')
const repositoryRoot = absoluteRequired('CHORA_REPOSITORY_ROOT')
const repositoryParent = dirname(repositoryRoot)
const sourceManifest = absoluteRequired('CHORA_SOURCE_MANIFEST')
const evidenceRoot = absoluteRequired('CHORA_EVIDENCE_DIR')
const binary = absoluteRequired('CHORA_BINARY')
const authFile = absoluteRequired('CHORA_PI_CODEX_AUTH_FILE')
const caFile = absoluteRequired('CHORA_CA_FILE')
const bundleAggregate = digestRequired('CHORA_BUNDLE_AGGREGATE')
const preflightFingerprint = digestRequired('CHORA_PREFLIGHT_FINGERPRINT')
const proxyURL = safeURL(required('CHORA_PROXY_URL'), 'CHORA_PROXY_URL')
const modelURL = safeURL(required('CHORA_MODEL_URL'), 'CHORA_MODEL_URL')
const port = integerRequired('CHORA_PORT', 1, 65535)
const docker = process.env.CHORA_DOCKER_BINARY?.trim() || 'docker'
const baseURL = `http://127.0.0.1:${port}`
const databasePath = resolve(dataRoot, 'chora.db')
const maxLogBytes = 64 * 1024
const eventPageSize = 7
const roomName = 'Public Real Spec Coding Room'
const revisionOneReviewNote = 'Request one exact successor Revision before execution; preserve this submitted Revision and Review as immutable history.'
const revisionTwoReviewNote = 'Accept Revision 2 only after inspecting its exact Diff from Revision 1.'
const serviceHistory = []
const requestAudit = []
const observations = {}
const closedBootstrapRoutes = ['/api/spec-coding/materializations', '/api/spec-coding/contracts']
const knownSecrets = Object.entries(process.env)
  .filter(([name, value]) => /TOKEN|PASSWORD|SECRET|CREDENTIAL|COOKIE|AUTH/i.test(name) && typeof value === 'string' && value.length >= 4)
  .map(([, value]) => value)
  .concat([proxyURL.username, proxyURL.password, modelURL.username, modelURL.password].filter((value) => value.length >= 4))
  .concat(urlSecretScalars(proxyURL), urlSecretScalars(modelURL))

const positiveTask = Object.freeze({
  title: 'Reject blank acceptance criterion descriptions',
  requirement: 'Keep Chora acceptance criteria reviewable by rejecting empty and whitespace-only descriptions while preserving valid criteria.',
  constraints: ['Return domain.ErrInvalidArgument for blank descriptions.', 'Preserve valid title and description values.', 'Never inspect vendor metadata or read or disclose credentials, personal information, non-Chora content, or enterprise-project code.'],
  outOfScope: ['No persistence or API changes.', 'No unrelated refactor.'],
  criteria: [
    'Blank descriptions fail closed | Empty and whitespace-only descriptions return domain.ErrInvalidArgument. | 0',
    'Valid criteria remain stable | A valid title and description are retained without unrelated behavior changes. | 0',
  ],
  writableFiles: ['internal/domain/task.go', 'internal/domain/task_test.go'],
  verificationCommands: [['go', 'test', './internal/domain']],
})
const identityTask = Object.freeze({
  title: 'Keep valid acceptance criterion descriptions stable',
  requirement: 'Add focused coverage that valid acceptance criterion descriptions remain unchanged after construction.',
  constraints: ['Keep the change within the existing domain constructor and tests.', 'Never inspect vendor metadata or read or disclose credentials, personal information, non-Chora content, or enterprise-project code.'],
  outOfScope: ['No runtime, storage, or web changes.'],
  criteria: ['Valid descriptions remain exact | A non-blank description is retained exactly by the acceptance criterion. | 0'],
  writableFiles: ['internal/domain/task.go', 'internal/domain/task_test.go'],
  verificationCommands: [['go', 'test', './internal/domain']],
})

assert(new URL(baseURL).protocol === 'http:' && new URL(baseURL).hostname === '127.0.0.1', 'service URL must be loopback HTTP')
assert(!insideOrSame(dataRoot, evidenceRoot), 'CHORA_EVIDENCE_DIR must be outside CHORA_DATA_ROOT')
assert(!insideOrSame(installRoot, sourceRoot) && !insideOrSame(sourceRoot, installRoot), 'CHORA_SOURCE_ROOT and CHORA_INSTALL_ROOT must be disjoint')
for (const root of [sourceRoot, installRoot, dataRoot]) {
  assert(!insideOrSame(root, repositoryRoot) && !insideOrSame(repositoryRoot, root), 'CHORA_REPOSITORY_ROOT must be disjoint from source, install, and data roots')
}
assert(insideOrSame(sourceRoot, sourceManifest), 'CHORA_SOURCE_MANIFEST must be inside CHORA_SOURCE_ROOT')
assert(insideOrSame(installRoot, binary), 'CHORA_BINARY must be inside CHORA_INSTALL_ROOT')
assert(insideOrSame(dataRoot, databasePath) && databasePath !== dataRoot, 'database path must be inside CHORA_DATA_ROOT')

await Promise.all([
  assertFile(binary, 'CHORA_BINARY', true),
  assertFile(sourceManifest, 'CHORA_SOURCE_MANIFEST'),
  assertFile(authFile, 'CHORA_PI_CODEX_AUTH_FILE'),
  assertFile(caFile, 'CHORA_CA_FILE'),
])
const authBytes = await readFile(authFile)
try {
  collectCredentialScalars(JSON.parse(authBytes.toString('utf8')), knownSecrets)
} finally {
  authBytes.fill(0)
}
const disclosureAudit = await auditDisclosureBoundary({
  bundleRoot: sourceRoot, manifestPath: sourceManifest, expectedAggregate: bundleAggregate, authFile,
})
const binaryDigest = sha256(await readFile(binary))
const serveArgv = Object.freeze([
  'serve', '--source', sourceRoot, '--source-manifest', sourceManifest,
  '--bundle-aggregate', bundleAggregate, '--install', installRoot, '--data', dataRoot,
  '--repository', repositoryRoot,
  '--auth', authFile, '--ca', caFile, '--proxy', proxyURL.origin,
  '--model-url', modelURL.toString(), '--preflight-fingerprint', preflightFingerprint,
  '--db', databasePath, '--port', String(port),
])
const serveArgvDigest = sha256(JSON.stringify(serveArgv))

let service
let browser
let page
let stopped = false
try {
  service = await startService('initial-start')
  const statusBefore = await serviceStatus()
  await assertBootstrapRoutesClosed()

  browser = await chromium.launch({ headless: true })
  const context = await browser.newContext()
  page = await context.newPage()
  page.on('request', (request) => requestAudit.push({ source: 'browser', method: request.method(), path: new URL(request.url()).pathname }))
  await page.goto(baseURL, { waitUntil: 'networkidle' })

  const initialReadiness = await refreshReadinessThroughUI(page)
  assertReady(initialReadiness, 'initial public readiness')
  await assertNoHostPathDisplay(page)

  const roomResponse = await uiMutation(page, '/api/rooms', async () => {
    await page.getByRole('button', { name: '＋ New Room' }).click()
    await page.getByLabel('Room name').fill(roomName)
    await page.getByLabel('Description').fill('Prove two independently identified public Real Spec Coding Tasks and one governed installed-product journey.')
    await page.getByRole('button', { name: 'Create Room', exact: true }).click()
  })
  const room = await responseJSON(roomResponse, 201, 'create Room')
  assertCallerIdempotency(roomResponse, 'create Room')
  assertSafeID(room.id, 'Room identity', 'room_')
  assert(!roomResponse.request().postData()?.includes('workspaceRoot'), 'Room UI submitted a host workspace path')
  assertNoInternalHostPath(room, 'public Room')
  await page.waitForURL(`${baseURL}/rooms/${room.id}`)
  await assertNoHostPathDisplay(page)

  const firstCreation = await createRealTaskThroughUI(page, room.id, positiveTask)
  const firstTask = firstCreation.task
  assertCallerIdempotency(firstCreation.response, 'create first Task')
  assertTaskIdentity(firstTask, room.id, positiveTask)
	const firstWorktreeRoot = taskWorktreePath(firstTask)
  await assertNoHostPathDisplay(page)

  const replayHeaders = firstCreation.response.request().headers()
  const replayKey = replayHeaders['idempotency-key']
  assert(typeof replayKey === 'string' && replayKey.length >= 8, 'Task UI omitted caller idempotency')
  const replayBody = firstCreation.response.request().postDataJSON()
  const replayed = await api('POST', `/api/rooms/${room.id}/tasks`, replayBody, 201, { 'Idempotency-Key': replayKey })
  assert(replayed.id === firstTask.id && replayed.planning?.draft?.id === firstTask.planning.draft.id, 'exact Task command replay changed server identities')
  await api('POST', `/api/rooms/${room.id}/tasks`, {
    ...replayBody,
    realSpecCoding: { ...replayBody.realSpecCoding, requirement: `${replayBody.realSpecCoding.requirement} Changed reuse must conflict.` },
  }, 409, { 'Idempotency-Key': replayKey })

  await page.goto(`${baseURL}/rooms/${room.id}`, { waitUntil: 'networkidle' })
  const secondCreation = await createRealTaskThroughUI(page, room.id, identityTask)
  const secondTask = secondCreation.task
  assertCallerIdempotency(secondCreation.response, 'create second Task')
  assertTaskIdentity(secondTask, room.id, identityTask)
  assertDistinctTaskIdentities(firstTask, secondTask)
	assert(taskWorktreePath(secondTask) !== firstWorktreeRoot, 'two Real Tasks reused one worktree')
  assertDeepEqual(firstTask.repository, secondTask.repository, 'installed repository identity drifted between supported Tasks')
  await assertNoHostPathDisplay(page)

  const nestedFirst = await api('GET', `/api/rooms/${room.id}/tasks/${firstTask.id}`, undefined, 200)
  const nestedSecond = await api('GET', `/api/rooms/${room.id}/tasks/${secondTask.id}`, undefined, 200)
  assert(nestedFirst.goal === positiveTask.requirement && nestedSecond.goal === identityTask.requirement, 'distinct requirements were not persisted')
  await api('GET', `/api/rooms/${mutatedID(room.id)}/tasks/${firstTask.id}`, undefined, 404)

  const secondSubmitResponse = await uiMutation(page, `/api/tasks/${secondTask.id}/plan/drafts/${secondTask.planning.draft.id}/submit`, async () => {
    await page.getByRole('button', { name: 'Submit Revision' }).click()
  })
  const secondSubmitted = await responseJSON(secondSubmitResponse, 200, 'submit second Task Plan Revision')
  assertCallerIdempotency(secondSubmitResponse, 'submit second Task Plan Revision')
  assertSafeID(secondSubmitted.revision?.id, 'second Plan Revision identity', 'plan_revision_')
  assert(secondSubmitted.revision.id !== firstTask.planning?.revisions?.[0]?.id, 'distinct Tasks reused a Plan Revision identity')

  await page.goto(`${baseURL}/rooms/${room.id}/tasks/${firstTask.id}`, { waitUntil: 'networkidle' })
  const submitResponse = await uiMutation(page, `/api/tasks/${firstTask.id}/plan/drafts/${firstTask.planning.draft.id}/submit`, async () => {
    await page.getByRole('button', { name: 'Submit Revision' }).click()
  })
  const submitted = await responseJSON(submitResponse, 200, 'submit Plan Revision')
  assertCallerIdempotency(submitResponse, 'submit Plan Revision')
  const revisionOne = submitted.revision
  assertSafeID(revisionOne?.id, 'Revision 1 identity', 'plan_revision_')
  assert(revisionOne.revisionNumber === 1 && !revisionOne.predecessorRevisionId && revisionOne.unchanged === false,
    'first Plan submission was not exact Revision 1')
  assert(revisionOne.id !== secondSubmitted.revision.id, 'independently identified Tasks reused a Plan Revision identity')
  const revisionOneSubmittedIdentity = immutablePlanRevision(revisionOne, false)

  const requestRevisionResponse = await uiMutation(page, `/api/tasks/${firstTask.id}/plan/revisions/${revisionOne.id}/reviews`, async () => {
    await page.getByLabel('Planning review note').fill(revisionOneReviewNote)
    await page.getByRole('button', { name: 'Request Revision' }).click()
  })
  const requestedRevision = await responseJSON(requestRevisionResponse, 200, 'request Revision 1 successor')
  assertCallerIdempotency(requestRevisionResponse, 'request Revision 1 successor')
  assert(requestedRevision.revisionId === revisionOne.id, 'Request Revision reviewed the wrong predecessor')
  assertPlanReview(requestedRevision.review, 'request_revision', revisionOneReviewNote)
  const successorDraft = requestedRevision.draft
  assertSafeID(successorDraft?.id, 'successor Plan Draft identity', 'plan_draft_')
  assert(successorDraft.id !== submitted.draft.id && successorDraft.predecessorRevisionId === revisionOne.id &&
    successorDraft.nextRevisionNumber === 2, 'Request Revision did not create the exact successor Draft')
  assertDeepEqual(successorDraft.content, revisionOne.content, 'successor Draft did not begin from exact Revision 1 content')
  assert(successorDraft.selectionDigest === revisionOne.selectionDigest, 'successor Draft selection binding drifted')
  await page.getByRole('heading', { name: 'Edit Technical Plan Draft' }).waitFor()
  await page.getByText(`DRAFT · EDIT VERSION ${successorDraft.editVersion}`, { exact: true }).waitFor()

  const afterRequest = await api('GET', `/api/rooms/${room.id}/tasks/${firstTask.id}`, undefined, 200)
  const revisionOneAfterRequest = exactPlanRevision(afterRequest, revisionOne.id, 'after Request Revision')
  assertDeepEqual(revisionOneSubmittedIdentity, immutablePlanRevision(revisionOneAfterRequest, false),
    'Request Revision changed submitted Revision 1 identity or content')
  assertPlanReview(revisionOneAfterRequest.review, 'request_revision', revisionOneReviewNote)
  const immutableRevisionOne = immutablePlanRevision(revisionOneAfterRequest, true)
  assertDeepEqual(successorDraft, afterRequest.planning.draft, 'Task view substituted the exact successor Draft')

  const revisionTwoContent = successorPlanContent(revisionOne.content)
  const saveSuccessorResponse = await uiMutation(page, `/api/tasks/${firstTask.id}/plan/drafts/${successorDraft.id}`, async () => {
    await fillPlanContent(page, revisionTwoContent)
    await page.getByRole('button', { name: 'Save Draft' }).click()
  })
  const savedSuccessor = await responseJSON(saveSuccessorResponse, 200, 'save exact successor Draft')
  assertCallerIdempotency(saveSuccessorResponse, 'save exact successor Draft')
  assert(savedSuccessor.draft.id === successorDraft.id && savedSuccessor.draft.editVersion === successorDraft.editVersion + 1,
    'successor Draft save changed identity or did not advance edit version')
  assert(savedSuccessor.draft.predecessorRevisionId === revisionOne.id && savedSuccessor.draft.nextRevisionNumber === 2,
    'saved successor Draft lost its exact predecessor')
  assertDeepEqual(savedSuccessor.draft.content, revisionTwoContent, 'saved successor Draft content drifted from the UI edit')
  await page.getByText(`DRAFT · EDIT VERSION ${savedSuccessor.draft.editVersion}`, { exact: true }).waitFor()
  assertRevisionOneImmutable(await api('GET', `/api/rooms/${room.id}/tasks/${firstTask.id}`, undefined, 200), revisionOne.id, immutableRevisionOne,
    'successor Draft save')

  const submitRevisionTwoResponse = await uiMutation(page, `/api/tasks/${firstTask.id}/plan/drafts/${successorDraft.id}/submit`, async () => {
    await page.getByRole('button', { name: 'Submit Revision' }).click()
  })
  const submittedRevisionTwo = await responseJSON(submitRevisionTwoResponse, 200, 'submit Revision 2')
  assertCallerIdempotency(submitRevisionTwoResponse, 'submit Revision 2')
  const revisionTwo = submittedRevisionTwo.revision
  assertSafeID(revisionTwo?.id, 'Revision 2 identity', 'plan_revision_')
  assert(revisionTwo.id !== revisionOne.id && revisionTwo.revisionNumber === 2 &&
    revisionTwo.predecessorRevisionId === revisionOne.id && revisionTwo.sourceDraftId === successorDraft.id &&
    revisionTwo.unchanged === false, 'Revision 2 did not preserve exact successor lineage')
  assertDeepEqual(revisionTwo.content, revisionTwoContent, 'Revision 2 content drifted from the saved successor Draft')
  assert(revisionTwo.selectionDigest === revisionOne.selectionDigest, 'Revision 2 selection binding drifted from Revision 1')
  const beforeRevisionTwoAcceptance = await api('GET', `/api/rooms/${room.id}/tasks/${firstTask.id}`, undefined, 200)
  assertRevisionOneImmutable(beforeRevisionTwoAcceptance, revisionOne.id, immutableRevisionOne, 'Revision 2 submission')
  assertDeepEqual(immutablePlanRevision(exactPlanRevision(beforeRevisionTwoAcceptance, revisionTwo.id, 'before Revision 2 acceptance'), false),
    immutablePlanRevision(revisionTwo, false), 'submitted Revision 2 identity or content drifted')
  await inspectExactPredecessorDiff(page, revisionOne, revisionTwo)

  const acceptPlanResponse = await uiMutation(page, `/api/tasks/${firstTask.id}/plan/revisions/${revisionTwo.id}/reviews`, async () => {
    await page.getByLabel('Planning review note').fill(revisionTwoReviewNote)
    await page.getByRole('button', { name: 'Accept Revision' }).click()
  })
  const planAcceptance = await responseJSON(acceptPlanResponse, 200, 'accept Revision 2')
  assertCallerIdempotency(acceptPlanResponse, 'accept Revision 2')
  assertPlanReview(planAcceptance.review, 'accept', revisionTwoReviewNote)
  assertPlanAcceptance(planAcceptance, revisionTwo.id)
  const acceptedTask = await api('GET', `/api/rooms/${room.id}/tasks/${firstTask.id}`, undefined, 200)
  assertDeepEqual(acceptedTask.planning.acceptance, planAcceptance.acceptance, 'nested Task changed accepted Plan binding')
  assert(acceptedTask.frozenSnapshot?.id === planAcceptance.acceptance.snapshotId, 'nested Task omitted the accepted frozen Snapshot')
  assertRevisionOneImmutable(acceptedTask, revisionOne.id, immutableRevisionOne, 'Revision 2 acceptance')
  assertAcceptedRevisionTwo(acceptedTask, revisionTwo, planAcceptance)
  await page.getByText('CURRENT ACCEPTANCE · REVISION 2', { exact: true }).waitFor()

  const runHistoryBeforeWrongBinding = await api('GET', `/api/rooms/${room.id}/tasks/${firstTask.id}/runs`, undefined, 200)
  await api('POST', `/api/tasks/${firstTask.id}/runs`, { revisionId: secondSubmitted.revision.id }, 409,
    { 'Idempotency-Key': `wrong-plan-binding-${firstTask.id}` })
  const runHistoryAfterWrongBinding = await api('GET', `/api/rooms/${room.id}/tasks/${firstTask.id}/runs`, undefined, 200)
  assertDeepEqual(runHistoryAfterWrongBinding, runHistoryBeforeWrongBinding, 'wrong-Task Plan binding persisted a Run')
  await proveTerminalResidueAbsent()

  const startReadiness = await api('POST', '/api/readiness/refresh', {}, 200, {}, 90_000)
  assertReady(startReadiness, 'pre-Start public readiness')
  const startRequestAfter = Date.now()
  const startResponse = await uiMutation(page, `/api/tasks/${firstTask.id}/runs`, async () => {
    await page.getByRole('button', { name: 'Start Bound Pi Run' }).click()
  }, 120_000)
  const readinessCheckedAt = new Date(startReadiness.checkedAt).getTime()
  assert(readinessCheckedAt <= startRequestAfter && readinessCheckedAt >= startRequestAfter - 120_000, 'fresh readiness was not observed before Start')
  const started = await responseJSON(startResponse, 201, 'start bound Pi Run')
  assertCallerIdempotency(startResponse, 'start bound Pi Run')
  assertSafeID(started.id, 'Run identity', 'run_')
  assert(started.adapter === 'pi' && started.adapter !== 'fake', 'Real Task substituted the Fake adapter')
  assert(started.task?.id === firstTask.id && started.room?.id === room.id, 'started Run ownership drifted')
  assert(started.snapshot?.id === planAcceptance.acceptance.snapshotId, 'Start substituted the accepted Snapshot')
  assert(started.attemptDetail?.executionWorkspace === '/workspace/repository', 'Run exposed or substituted the bounded execution workspace')
  await page.waitForURL(`${baseURL}/rooms/${room.id}/tasks/${firstTask.id}/runs/${started.id}`)

  const awaitingVerification = await waitForNestedRun(room.id, firstTask.id, started.id, 'awaiting_verification', 20 * 60_000)
  assertAgentNoRetry(awaitingVerification, 'Agent')
  assert(awaitingVerification.agentReport?.authority === 'non_authoritative_agent_claim', 'AgentReport authority drift')
  assertNoFake(awaitingVerification)
  await page.reload({ waitUntil: 'networkidle' })
  await assertNoHostPathDisplay(page)

  const verificationResponse = await uiMutation(page, `/api/runs/${started.id}/verification`, async () => {
    await page.getByRole('button', { name: 'Start Independent Verification' }).click()
  }, 120_000)
  await responseJSON(verificationResponse, 202, 'start independent Verification')
  assertCallerIdempotency(verificationResponse, 'start independent Verification')
  const awaitingReview = await waitForNestedRun(room.id, firstTask.id, started.id, 'awaiting_review', 10 * 60_000)
  assertReviewReady(awaitingReview)
  assertNoFake(awaitingReview)

  const preRestartTaskBinding = immutableTaskBinding(awaitingReview, acceptedTask)
  const preRestartIdentity = immutableIdentity(awaitingReview, firstTask.id, planAcceptance.acceptance.snapshotId)
  const preRestartPatch = await downloadPatch(started.id, awaitingReview.reviewablePatch)
  const preRestartEvents = await allEvents(started.id, 'verification.review_ready')

  const initialStop = await stopService(service, 'pre-review-restart')
  stopped = true
  service = undefined
  await assertFile(databasePath, 'persisted SQLite database')
  assert(sha256(await readFile(binary)) === binaryDigest, 'CHORA_BINARY changed before restart')
  assert(sha256(JSON.stringify(serveArgv)) === serveArgvDigest, 'serve argv changed before restart')

  service = await startService('review-reopen')
  stopped = false
  assert(serviceHistory.length === 2 && serviceHistory[0].pid !== serviceHistory[1].pid, 'restart did not create a distinct service process')
  const statusAfter = await serviceStatus()
  assertDeepEqual(statusIdentity(statusBefore), statusIdentity(statusAfter), 'service capability identity drift after restart')

  await page.goto(baseURL, { waitUntil: 'networkidle' })
  assert(new URL(page.url()).pathname === '/', 'restart recovery did not begin at the Room Directory root')
  const activeRooms = page.getByRole('region', { name: 'Active Rooms' })
  const namedRoomCard = activeRooms.locator('.room-card').filter({
    has: page.getByRole('heading', { name: roomName, exact: true }),
  })
  assert(await namedRoomCard.count() === 1, 'Room Directory did not expose exactly one named Room')
  await namedRoomCard.getByRole('button', { name: 'Open Room' }).click()
  await page.waitForURL((url) => url.pathname === `/rooms/${room.id}`)
  const namedTaskCard = page.locator('.task-card').filter({
    has: page.getByRole('heading', { name: positiveTask.title, exact: true }),
  })
  await namedTaskCard.first().waitFor({ state: 'visible', timeout: 30_000 })
  assert(await namedTaskCard.count() === 1, 'Room workspace did not expose exactly one named positive Task')
  const currentAction = namedTaskCard.locator('.current-action')
  assert((await currentAction.locator('strong').innerText()).trim() === 'Continue - Review result',
    'Room workspace did not recover the server-selected review action')
  await namedTaskCard.getByRole('button', { name: 'Continue - Review result' }).click()
  await page.waitForURL((url) => url.pathname === `/rooms/${room.id}/tasks/${firstTask.id}/runs/${started.id}`)
  observations.restartReentry = {
    rootEntry: true, roomSelectedByName: true, taskSelectedByTitle: true,
    serverCurrentAction: 'review_result', composedDeepLinkUsed: false,
  }
  const reopenedTask = await api('GET', `/api/rooms/${room.id}/tasks/${firstTask.id}`, undefined, 200)
  const reopenedSecondTask = await api('GET', `/api/rooms/${room.id}/tasks/${secondTask.id}`, undefined, 200)
  const reopened = await api('GET', `/api/rooms/${room.id}/tasks/${firstTask.id}/runs/${started.id}`, undefined, 200)
  assert(reopenedSecondTask.id === secondTask.id && !reopenedSecondTask.planning?.draft &&
    reopenedSecondTask.planning?.revisions?.some((revision) => revision.id === secondSubmitted.revision.id) &&
    !reopenedSecondTask.planning?.acceptance, 'restart changed the second submitted-but-unaccepted Task identity')
  assert(reopened.status === 'awaiting_review' && reopened.version === awaitingReview.version, 'reopened Run state/version drifted')
  assertRevisionOneImmutable(reopenedTask, revisionOne.id, immutableRevisionOne, 'Directory restart re-entry')
  assertAcceptedRevisionTwo(reopenedTask, revisionTwo, planAcceptance)
  assertNoRetry(reopened, 'reopened journey')
  assertDeepEqual(preRestartTaskBinding, immutableTaskBinding(reopened, reopenedTask), 'Task/Plan/Charter/Snapshot binding drift after restart')
  assertDeepEqual(preRestartIdentity, immutableIdentity(reopened, firstTask.id, planAcceptance.acceptance.snapshotId), 'Run/Agent/Verifier/Result/Patch identity drift after restart')
  const reopenedEvents = await allEvents(started.id, 'verification.review_ready')
  assertDeepEqual(preRestartEvents, reopenedEvents, 'paginated Event history drift after restart')
  const reopenedPatch = await downloadPatch(started.id, reopened.reviewablePatch)
  assertPatchEqual(preRestartPatch, reopenedPatch)
  await assertNoHostPathDisplay(page)

  const targetIndexBefore = await gitIndexIdentity(firstWorktreeRoot)
  const targetFilesBefore = await digestTargetFiles(firstWorktreeRoot, positiveTask.writableFiles)
  const reviewResponsePromise = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === `/api/runs/${started.id}/review`)
  const applyResponsePromise = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === `/api/runs/${started.id}/apply`)
  await page.getByLabel('Review note').fill('The independently verified Patch satisfies the exact accepted Plan and frozen Task contract.')
  await page.getByRole('button', { name: 'Accept & Apply' }).click()
  const reviewResponse = await reviewResponsePromise
  const applyResponse = await applyResponsePromise
  const reviewDecision = await responseJSON(reviewResponse, 200, 'accept verified Result')
  assertCallerIdempotency(reviewResponse, 'accept verified Result')
  const accepted = await responseJSON(applyResponse, 200, 'apply accepted Patch')
  assertCallerIdempotency(applyResponse, 'apply accepted Patch')
  assert(reviewDecision.status === 'accepted', `formal Review state is ${reviewDecision.status}`)
  assert(accepted.status === 'accepted' && accepted.patchApplication?.state === 'applied', `accepted Patch application state is ${accepted.patchApplication?.state}`)
  assert(accepted.patchApplication.patchDigest === preRestartPatch.digest, 'applied Patch digest drifted from the reviewed Patch')
  assertDeepEqual(accepted.patchApplication.affectedPaths, positiveTask.writableFiles, 'applied path set drifted')
  assert(await gitIndexIdentity(firstWorktreeRoot) === targetIndexBefore, 'Patch Apply changed the Git index')
  const targetFilesAfter = await digestTargetFiles(firstWorktreeRoot, positiveTask.writableFiles)
  assert(targetFilesAfter.some((digest, index) => digest !== targetFilesBefore[index]), 'Patch Apply did not change any declared target file')
  await page.getByText('Written to repository', { exact: true }).waitFor({ state: 'visible' })
  assertNoRetry(accepted, 'accepted journey')
  assertNoFake(accepted)
  assertDeepEqual(preRestartIdentity, immutableIdentity(accepted, firstTask.id, planAcceptance.acceptance.snapshotId), 'acceptance changed immutable pre-Review identity')
  assert(accepted.verifiedReview?.kind === 'accept' && accepted.verifiedReview?.patchDigest === preRestartPatch.digest, 'immutable human Review binding drift')
  assert(accepted.verifiedReview?.resultId === awaitingReview.verification.result.id, 'human Review Result identity drift')
  assertSafeID(accepted.verifiedReview?.id, 'Review identity', 'review_decision_')

  const postApplyStop = await stopService(service, 'post-apply-restart')
  service = undefined
  service = await startService('post-apply-reopen')
  await page.reload({ waitUntil: 'networkidle' })
  await page.getByText('Written to repository', { exact: true }).waitFor({ state: 'visible' })
  const finalNested = await api('GET', `/api/rooms/${room.id}/tasks/${firstTask.id}/runs/${started.id}`, undefined, 200)
  assert(finalNested.status === 'accepted' && finalNested.verifiedReview?.id === accepted.verifiedReview.id && finalNested.patchApplication?.state === 'applied', 'restarted applied Run drifted')
  const finalTask = await api('GET', `/api/rooms/${room.id}/tasks/${firstTask.id}`, undefined, 200)
  assert(finalTask.state === 'closed', 'Task did not close after proven Patch application')
  assertRevisionOneImmutable(finalTask, revisionOne.id, immutableRevisionOne, 'terminal Result acceptance')
  assertAcceptedRevisionTwo(finalTask, revisionTwo, planAcceptance)
  await api('GET', `/api/rooms/${room.id}/tasks/${secondTask.id}/runs/${started.id}`, undefined, 404)
  const finalEvents = await allEvents(started.id, 'patch_application.applied')
  assertNoInternalHostPath(finalEvents, 'public Event history')
  assertEventPrefix(preRestartEvents, finalEvents)
  assert(finalEvents.length === preRestartEvents.length + 3, 'Review and Patch Apply produced an unexpected Event suffix')

  const finalReadiness = await api('POST', '/api/readiness/refresh', {}, 200, {}, 90_000)
  assertReady(finalReadiness, 'terminal public readiness')
  const residue = await proveTerminalResidueAbsent()
  await assertNoHostPathDisplay(page)
  assertBootstrapAudit()
  const finalStatus = await serviceStatus()
  assertNoInternalHostPath(finalStatus, 'public status')
  const screenshot = await page.screenshot({ fullPage: true })

  const finalStop = await stopService(service, 'final-stop')
  stopped = true
  service = undefined
  const credentialResidue = await assertNoCredentialLeakInTree({ root: dataRoot, authFile })

  observations.schemaVersion = 'chora.m1-u4-positive-observations.v1'
  observations.status = 'passed'
  observations.planRevisionPath = {
    revisionOne: {
      id: revisionOne.id, contentDigest: revisionOne.contentDigest,
      reviewId: revisionOneAfterRequest.review.id, reviewKind: revisionOneAfterRequest.review.kind,
      immutableThroughTerminalAcceptance: true,
    },
    successorDraft: {
      id: successorDraft.id, predecessorRevisionId: successorDraft.predecessorRevisionId,
      savedEditVersion: savedSuccessor.draft.editVersion, exactContentSaved: true,
    },
    revisionTwo: {
      id: revisionTwo.id, predecessorRevisionId: revisionTwo.predecessorRevisionId,
      sourceDraftId: revisionTwo.sourceDraftId,
      contentDigest: revisionTwo.contentDigest, exactPredecessorDiffInspected: true,
      reviewId: planAcceptance.review.id, reviewKind: planAcceptance.review.kind,
    },
  }
  observations.executionBinding = {
    acceptedRevisionId: planAcceptance.acceptance.revisionId,
    snapshotId: planAcceptance.acceptance.snapshotId,
    snapshotDigest: planAcceptance.acceptance.snapshotDigest,
    runId: started.id, runRevisionTwoBound: true, exactSnapshotBound: true,
  }
  observations.publicUI = assertPositiveUIMutationsObserved({
    roomID: room.id, firstTaskID: firstTask.id, secondTaskID: secondTask.id,
    firstDraftID: firstTask.planning.draft.id, secondDraftID: secondTask.planning.draft.id,
    successorDraftID: successorDraft.id, revisionOneID: revisionOne.id, revisionTwoID: revisionTwo.id,
    runID: started.id,
  })
  observations.readiness503 = { exercised: false }
  observations.readiness = {
    initial: readinessObservation(initialReadiness),
    beforeStart: readinessObservation(startReadiness),
    terminal: readinessObservation(finalReadiness),
  }
  observations.scans = {
    disclosureStatus: disclosureAudit.status,
    disclosureActualCredentialLeaks: disclosureAudit.actualOAuthCredentialLeakFiles,
    disclosureGenericSecretMatches: disclosureAudit.genericSecretRuleMatches,
    disclosureNonPlaceholderPrivacyMatches: disclosureAudit.nonPlaceholderPrivacyMatches,
    disclosurePrivatePathMatches: disclosureAudit.modelReadablePrivateHomePathOccurrences,
    disclosureEnterpriseMatches: disclosureAudit.unrelatedEnterpriseMarkerMatches,
    disclosureForbiddenPathMatches: disclosureAudit.forbiddenPathMatches,
    persistedCredentialStatus: credentialResidue.status,
    persistedActualCredentialLeaks: credentialResidue.actualOAuthCredentialLeakFiles,
    persistedGenericSecretMatches: credentialResidue.genericSecretRuleMatches,
    persistedPrivatePathLeaks: credentialResidue.privateHomePathLeakFiles,
    persistedEmailLeaks: credentialResidue.nonPlaceholderEmailLeakFiles,
    persistedEnterpriseLeaks: credentialResidue.unrelatedEnterpriseMarkerLeakFiles,
    publicHostPathMatches: 0, bootstrapRouteMisuse: 0,
  }
  observations.residue = residue

  const restartEvidence = {
    schemaVersion: 'chora.m1-u4-public-restart-reopen.v1', status: 'passed', roomId: room.id,
    taskIds: [firstTask.id, secondTask.id], runId: started.id,
    service: {
      loopbackURL: baseURL, binary: `[INSTALL_ROOT]/${relative(installRoot, binary)}`, binarySha256: binaryDigest,
      argv: safeServeArgv(), argvSha256: serveArgvDigest, database: '[DATA_ROOT]/chora.db',
      starts: serviceHistory.map(({ pid, label, startedAt }) => ({ pid, label, startedAt })), stops: [initialStop, postApplyStop, finalStop],
    },
    reopen: {
      preRestartStatus: awaitingReview.status, reopenedStatus: reopened.status,
      entryPath: '/', roomSelectedByName: true, taskSelectedByTitle: true,
      serverCurrentAction: 'review_result', composedDeepLinkUsed: false,
      taskBindingSha256: sha256(canonicalJSON(preRestartTaskBinding)), identitySha256: sha256(canonicalJSON(preRestartIdentity)),
      eventCount: preRestartEvents.length, lastEventSequence: preRestartEvents.at(-1).sequence,
      eventsEqual: true, patchHeadersEqual: true, patchBodySha256: preRestartPatch.digest,
    },
    review: { terminalStatus: accepted.status, reviewId: accepted.verifiedReview.id, applicationState: accepted.patchApplication.state, gitIndexUnchanged: true, restartAppliedEvidence: true, finalEventCount: finalEvents.length },
  }
  const publicIdentityEvidence = {
    schemaVersion: 'chora.m1-u4-public-identities.v1', roomId: room.id,
    firstTask: compactTask(firstTask), secondTask: compactTask(secondTask),
    acceptedBinding: planAcceptance.acceptance, runId: started.id,
    idempotency: { exactReplay: true, changedReuseConflict: true, keySha256: sha256(replayKey) },
    bootstrapRoutesClosed: true, fakeAdapterObserved: false, hostPathDisplayed: false,
    wrongTaskPlanBinding: { status: 409, runCreated: false, resourcesCreated: false },
  }
  const artifacts = new Map([
    ['journey.json', encodeJSON(accepted)],
    ['events.json', encodeJSON({ runId: started.id, events: finalEvents })],
    ['status.json', encodeJSON(finalStatus)],
    ['patch.diff', preRestartPatch.body],
    ['restart-reopen.json', encodeJSON(restartEvidence)],
    ['public-identities.json', encodeJSON(publicIdentityEvidence)],
    ['u4-observations.json', encodeJSON(observations)],
    ['readiness.json', encodeJSON({ initial: initialReadiness, beforeStart: startReadiness, terminal: finalReadiness })],
    ['residue.json', encodeJSON(residue)],
    ['disclosure.json', encodeJSON({ preflight: disclosureAudit, persistedCredentialScan: credentialResidue })],
    ['accepted-ui.png', screenshot],
  ])
  const artifactIndex = {
    schemaVersion: 'chora.m1-u4-public-artifact-index.v1', roomId: room.id, taskIds: [firstTask.id, secondTask.id], runId: started.id,
    artifacts: [...artifacts].map(([path, body]) => ({ path, bytes: body.length, sha256: sha256(body) })),
  }
  artifacts.set('artifact-index.json', encodeJSON(artifactIndex))
  assertU4PublicationReady({ observations, artifactIndex })
  for (const body of artifacts.values()) {
    assertNoKnownSecret(body)
    if (!Buffer.isBuffer(body)) assertNoInternalHostPath(body, 'U4 evidence artifact')
    else if (body.toString('utf8', 0, 8).startsWith('{')) assertNoInternalHostPath(body.toString('utf8'), 'U4 evidence artifact')
  }
  await persistEvidence(artifacts)

  const publication = `${JSON.stringify({
    status: 'passed', roomId: room.id, taskIds: [firstTask.id, secondTask.id], runId: started.id,
    snapshotId: planAcceptance.acceptance.snapshotId, patchDigest: preRestartPatch.digest,
    artifactIndex: 'artifact-index.json',
  })}\n`
  assertNoInternalHostPath(publication, 'stdout publication')
  process.stdout.write(publication)
} finally {
  if (browser) await browser.close().catch(() => {})
  if (!stopped && service) await forceStopService(service).catch(() => {})
}

async function createRealTaskThroughUI(currentPage, roomID, declaration) {
  const path = `/api/rooms/${roomID}/tasks`
  const response = await uiMutation(currentPage, path, async () => {
    await currentPage.getByLabel('Task title').fill(declaration.title)
    await currentPage.getByLabel('Task execution profile').selectOption('real_spec_coding')
    await currentPage.getByLabel('Requirement').fill(declaration.requirement)
    await currentPage.getByLabel(/Constraints/).fill(declaration.constraints.join('\n'))
    await currentPage.getByLabel(/Out of scope/).fill(declaration.outOfScope.join('\n'))
    await currentPage.getByLabel(/Acceptance criteria · title/).fill(declaration.criteria.join('\n'))
    await currentPage.getByLabel(/Writable files/).fill(declaration.writableFiles.join('\n'))
    await currentPage.getByLabel(/Verification commands/).fill(declaration.verificationCommands.map(JSON.stringify).join('\n'))
    await currentPage.getByRole('button', { name: 'Create Task' }).click()
  })
  const task = await responseJSON(response, 201, `create ${declaration.title}`)
  await currentPage.waitForURL(`${baseURL}/rooms/${roomID}/tasks/${task.id}`)
  return { response, task }
}

function successorPlanContent(predecessor) {
  const successorStep = 'Add focused constructor tests for blank and whitespace-only descriptions while preserving the exact valid-description case.'
  assert(!predecessor.technical_steps.includes(successorStep), 'Revision 1 already contains the U4 successor edit')
  return {
    technical_steps: [...predecessor.technical_steps, successorStep],
    decisions: [...predecessor.decisions],
    risks: [...predecessor.risks],
    unknowns: [...predecessor.unknowns],
  }
}

async function fillPlanContent(currentPage, content) {
  for (const [label, lines] of [
    ['Technical Steps', content.technical_steps], ['Decisions', content.decisions],
    ['Risks', content.risks], ['Unknowns', content.unknowns],
  ]) await currentPage.getByRole('textbox', { name: label, exact: true }).fill(lines.join('\n'))
}

async function inspectExactPredecessorDiff(currentPage, predecessor, successor) {
  await currentPage.getByRole('heading', { name: 'Review Submitted Revision 2' }).waitFor()
  const revisionCard = currentPage.locator('article.plan-revision').filter({
    has: currentPage.getByText(successor.id, { exact: true }),
  })
  assert(await revisionCard.count() === 1, 'UI did not expose exactly one Revision 2 history card')
  await revisionCard.getByRole('heading', { name: 'Diff from predecessor' }).waitFor()
  assert(await revisionCard.locator('.plan-field-diff').count() === 1,
    'Revision 2 UI Diff did not isolate the one changed Plan field')
  const diff = revisionCard.getByRole('group', { name: 'Technical Steps diff' })
  assertDeepEqual(await diff.getByRole('group', { name: 'Before Technical Steps' }).locator('li').allInnerTexts(),
    predecessor.content.technical_steps, 'UI Diff Before side was not exact Revision 1 content')
  assertDeepEqual(await diff.getByRole('group', { name: 'After Technical Steps' }).locator('li').allInnerTexts(),
    successor.content.technical_steps, 'UI Diff After side was not exact Revision 2 content')
  for (const field of ['decisions', 'risks', 'unknowns']) {
    assertDeepEqual(successor.content[field], predecessor.content[field], `Revision 2 unexpectedly changed ${field}`)
  }
}

function immutablePlanRevision(revision, includeReview) {
  assertSafeID(revision?.id, 'Plan Revision identity', 'plan_revision_')
  assertSafeID(revision?.taskId, 'Plan Revision Task identity', 'task_')
  assertSafeID(revision?.sourceDraftId, 'Plan Revision source Draft identity', 'plan_draft_')
  assert(Number.isSafeInteger(revision?.revisionNumber) && revision.revisionNumber > 0, 'Plan Revision number is invalid')
  assertDigest(revision.contentDigest, 'Plan Revision content digest')
  assertDigest(revision.selectionDigest, 'Plan Revision selection digest')
  const immutable = {
    id: revision.id, taskId: revision.taskId, sourceDraftId: revision.sourceDraftId,
    revisionNumber: revision.revisionNumber, predecessorRevisionId: revision.predecessorRevisionId ?? null,
    content: revision.content, contentDigest: revision.contentDigest, selectionDigest: revision.selectionDigest,
    unchanged: revision.unchanged, submittedAt: revision.submittedAt,
  }
  if (includeReview) immutable.review = revision.review
  return immutable
}

function exactPlanRevision(task, revisionID, label) {
  const matches = task.planning?.revisions?.filter((revision) => revision.id === revisionID) ?? []
  assert(matches.length === 1, `${label}: exact Plan Revision is absent or duplicated`)
  return matches[0]
}

function assertPlanReview(review, kind, note) {
  assertSafeID(review?.id, 'Plan Review identity', 'plan_review_')
  assert(review.kind === kind && review.note === note, `Plan Review is not exact ${kind} authority`)
  assert(typeof review.reviewer === 'string' && review.reviewer.length > 0, 'Plan Review reviewer identity is absent')
  assert(typeof review.decidedAt === 'string' && Number.isFinite(new Date(review.decidedAt).getTime()), 'Plan Review decision time is invalid')
}

function assertRevisionOneImmutable(task, revisionID, expected, stage) {
  assertDeepEqual(immutablePlanRevision(exactPlanRevision(task, revisionID, stage), true), expected,
    `${stage}: Revision 1 content, Review, or identity changed`)
}

function assertAcceptedRevisionTwo(task, revision, acceptanceResult) {
  const current = exactPlanRevision(task, revision.id, 'accepted Revision 2 binding')
  assertDeepEqual(immutablePlanRevision(current, false), immutablePlanRevision(revision, false),
    'accepted Revision 2 identity or content changed')
  assert(current.current === true, 'Revision 2 is not the current accepted Plan Revision')
  assertPlanReview(current.review, 'accept', revisionTwoReviewNote)
  assert(task.planning.acceptance?.revisionId === revision.id &&
    task.planning.acceptance.reviewId === current.review.id &&
    task.planning.acceptance.snapshotId === acceptanceResult.acceptance.snapshotId &&
    task.frozenSnapshot?.id === acceptanceResult.acceptance.snapshotId,
  'Task execution authority is not bound to accepted Revision 2 and its exact Snapshot')
}

function assertPositiveUIMutationsObserved(ids) {
  const expected = [
    `POST /api/readiness/refresh`,
    `POST /api/rooms`,
    `POST /api/rooms/${ids.roomID}/tasks`,
    `POST /api/rooms/${ids.roomID}/tasks`,
    `POST /api/tasks/${ids.secondTaskID}/plan/drafts/${ids.secondDraftID}/submit`,
    `POST /api/tasks/${ids.firstTaskID}/plan/drafts/${ids.firstDraftID}/submit`,
    `POST /api/tasks/${ids.firstTaskID}/plan/revisions/${ids.revisionOneID}/reviews`,
    `PATCH /api/tasks/${ids.firstTaskID}/plan/drafts/${ids.successorDraftID}`,
    `POST /api/tasks/${ids.firstTaskID}/plan/drafts/${ids.successorDraftID}/submit`,
    `POST /api/tasks/${ids.firstTaskID}/plan/revisions/${ids.revisionTwoID}/reviews`,
    `POST /api/tasks/${ids.firstTaskID}/runs`,
    `POST /api/runs/${ids.runID}/verification`,
    `POST /api/runs/${ids.runID}/review`,
    `POST /api/runs/${ids.runID}/apply`,
  ].sort()
  const actual = requestAudit.filter((request) => request.source === 'browser' && request.method !== 'GET')
    .map((request) => `${request.method} ${request.path}`).sort()
  assertDeepEqual(actual, expected, 'accepted positive-path user mutations were not exactly UI-driven')
  return {
    allAcceptedMutationsBrowserDriven: true,
    mutationCount: actual.length,
    actions: [
      'readiness_refresh', 'room_create', 'first_task_create', 'second_task_create', 'second_task_revision_submit',
      'revision_1_submit', 'request_revision', 'successor_draft_save', 'revision_2_submit',
      'revision_2_accept', 'run_start', 'verification_start', 'result_accept', 'patch_apply',
    ],
  }
}

function readinessObservation(readiness) {
  assertReady(readiness, 'U4 publication readiness observation')
  return {
    checkedAt: readiness.checkedAt, inputFingerprint: readiness.inputFingerprint,
    itemKeys: readiness.items.map((item) => item.key), allReady: true,
  }
}

function assertU4PublicationReady({ observations: evidence, artifactIndex }) {
  assert(evidence.schemaVersion === 'chora.m1-u4-positive-observations.v1' && evidence.status === 'passed',
    'U4 PASS cannot publish without the exact safe observation schema')
  assert(artifactIndex.schemaVersion === 'chora.m1-u4-public-artifact-index.v1',
    'U4 PASS cannot publish with a non-U4 artifact index')
  assert(evidence.planRevisionPath?.revisionOne?.immutableThroughTerminalAcceptance === true,
    'U4 PASS cannot publish without immutable Revision 1 evidence')
  assert(evidence.planRevisionPath?.revisionOne?.reviewKind === 'request_revision' &&
    evidence.planRevisionPath?.successorDraft?.predecessorRevisionId === evidence.planRevisionPath?.revisionOne?.id &&
    evidence.planRevisionPath?.successorDraft?.exactContentSaved === true &&
    evidence.planRevisionPath?.revisionTwo?.reviewKind === 'accept' &&
    evidence.planRevisionPath?.revisionTwo?.exactPredecessorDiffInspected === true &&
    evidence.planRevisionPath?.revisionTwo?.predecessorRevisionId === evidence.planRevisionPath?.revisionOne?.id &&
    evidence.planRevisionPath?.revisionTwo?.sourceDraftId === evidence.planRevisionPath?.successorDraft?.id,
  'U4 PASS cannot publish without the exact Revision 1 -> Revision 2 UI path')
  assert(evidence.restartReentry?.rootEntry === true && evidence.restartReentry?.roomSelectedByName === true &&
    evidence.restartReentry?.taskSelectedByTitle === true && evidence.restartReentry?.serverCurrentAction === 'review_result' &&
    evidence.restartReentry?.composedDeepLinkUsed === false,
  'U4 PASS cannot publish without Directory-driven restart re-entry')
  assert(evidence.executionBinding?.runRevisionTwoBound === true && evidence.executionBinding?.exactSnapshotBound === true &&
    evidence.executionBinding?.acceptedRevisionId === evidence.planRevisionPath.revisionTwo.id,
  'U4 PASS cannot publish without exact Revision 2 Run/Snapshot binding')
  assert(evidence.publicUI?.allAcceptedMutationsBrowserDriven === true && evidence.publicUI?.mutationCount === 13,
    'U4 PASS cannot publish without all accepted positive-path UI mutations')
  assert(evidence.readiness503?.exercised === false, 'U4 PASS cannot publish after a readiness block')
  for (const stage of ['initial', 'beforeStart', 'terminal']) {
    assert(evidence.readiness?.[stage]?.allReady === true && evidence.readiness[stage].itemKeys.length === 9,
      `U4 PASS cannot publish without complete ${stage} readiness`)
  }
  assert(evidence.scans?.disclosureStatus === 'passed' && evidence.scans?.persistedCredentialStatus === 'passed',
    'U4 PASS cannot publish without passed disclosure and persisted scans')
  for (const field of [
    'disclosureActualCredentialLeaks', 'disclosureGenericSecretMatches', 'disclosureNonPlaceholderPrivacyMatches',
    'disclosurePrivatePathMatches', 'disclosureEnterpriseMatches', 'disclosureForbiddenPathMatches',
    'persistedActualCredentialLeaks', 'persistedGenericSecretMatches', 'persistedPrivatePathLeaks', 'persistedEmailLeaks',
    'persistedEnterpriseLeaks', 'publicHostPathMatches', 'bootstrapRouteMisuse',
  ]) assert(evidence.scans[field] === 0, `U4 PASS cannot publish with non-zero ${field}`)
  assert(evidence.residue?.status === 'absent' && [
    'labeledContainers', 'labeledVerifierContainers', 'labeledNetworks', 'agentWorkspaces', 'verifierWorkspaces',
  ].every((field) => evidence.residue[field] === 0), 'U4 PASS cannot publish with Chora execution residue')
  const requiredArtifacts = [
    'journey.json', 'events.json', 'status.json', 'patch.diff', 'restart-reopen.json', 'public-identities.json',
    'u4-observations.json', 'readiness.json', 'residue.json', 'disclosure.json', 'accepted-ui.png',
  ]
  assert(requiredArtifacts.every((path) => artifactIndex.artifacts.some((artifact) => artifact.path === path)),
    'U4 PASS cannot publish with an incomplete artifact index')
  assertNoInternalHostPath({ evidence, artifactIndex }, 'U4 publication evidence')
}

async function uiMutation(currentPage, path, action, timeout = 30_000) {
  const [response] = await Promise.all([
    currentPage.waitForResponse((candidate) => {
      const url = new URL(candidate.url())
      return url.pathname === path && candidate.request().method() !== 'GET'
    }, { timeout }),
    action(),
  ])
  return response
}

async function responseJSON(response, expected, label) {
  const text = await response.text()
  assertNoProhibitedPublicContent(text, label)
  assert(response.status() === expected, `${label}: HTTP ${response.status()}, expected ${expected}: ${redact(text.slice(0, 2000))}`)
  try { return JSON.parse(text) } catch { throw new Error(`${label}: response is not JSON`) }
}

function assertCallerIdempotency(response, label) {
  const key = response.request().headers()['idempotency-key']
  assert(typeof key === 'string' && /^[A-Za-z0-9._:@/+\-=]{8,256}$/.test(key), `${label}: caller Idempotency-Key is absent or unsafe`)
}

async function refreshReadinessThroughUI(currentPage) {
  const details = currentPage.locator('details.app-readiness')
  const summary = details.locator('summary')
  const refresh = currentPage.getByRole('button', { name: 'Refresh readiness' })
  if (!await refresh.isVisible()) await summary.click()
  const response = await uiMutation(currentPage, '/api/readiness/refresh', async () => {
    await refresh.click()
  }, 90_000)
  const readiness = await responseJSON(response, 200, 'Refresh readiness')
  if (await details.getAttribute('open') !== null) await summary.click()
  return readiness
}

function assertReady(readiness, label) {
  const expected = ['source_baseline', 'pi_image', 'docker_engine', 'colima', 'oauth', 'ca_proxy_model', 'verifier', 'task_worktrees', 'owned_residue']
  assert(Array.isArray(readiness.items) && readiness.items.length === expected.length, `${label}: readiness item set is incomplete`)
  assertDeepEqual(readiness.items.map((item) => item.key), expected, `${label}: readiness item order/identity drift`)
  assert(readiness.items.every((item) => item.state === 'ready'), `${label}: ${redact(JSON.stringify(readiness.items))}`)
  assertDigest(readiness.inputFingerprint, `${label} input fingerprint`)
  assertNoInternalHostPath(readiness, label)
}

function assertTaskIdentity(task, roomID, declaration) {
  assertSafeID(task.id, 'Task identity', 'task_')
  assert(task.roomId === roomID, 'Task nested Room ownership drift')
  assert(task.title === declaration.title && task.goal === declaration.requirement, 'Task declaration drift')
  assert(task.executionProfile === 'real_spec_coding', 'Task is not Real Spec Coding')
  assert(task.repository?.name && task.repository?.sourceRevision, 'Task omitted installed repository identity')
	assert(task.worktree?.state === 'ready' && typeof task.worktree.locator === 'string', 'Task omitted its ready managed worktree')
  assertDigest(task.repository?.baselineDigest, 'Task Baseline digest')
  assert(Array.isArray(task.criteria) && task.criteria.length === declaration.criteria.length, 'Task criterion set drift')
  for (const criterion of task.criteria) assertSafeID(criterion.id, 'Criterion identity', 'criterion_')
  assertSafeID(task.planning?.draft?.id, 'Plan Draft identity', 'plan_draft_')
  assert(!task.planning?.acceptance && !task.frozenSnapshot, 'Task creation prematurely materialized accepted execution authority')
  assertNoInternalHostPath(task, 'public Task')
}

function assertDistinctTaskIdentities(first, second) {
  assert(first.id !== second.id, 'two public Tasks reused a fixed Task identity')
  assert(first.planning.draft.id !== second.planning.draft.id, 'two public Tasks reused a fixed Draft identity')
  const firstCriteria = new Set(first.criteria.map((criterion) => criterion.id))
  assert(second.criteria.every((criterion) => !firstCriteria.has(criterion.id)), 'two public Tasks reused fixed Criterion identities')
}

function assertPlanAcceptance(result, revisionID) {
  const acceptance = result.acceptance
  assert(acceptance?.revisionId === revisionID, 'Plan acceptance substituted the submitted Revision')
  assertSafeID(acceptance.reviewId, 'Plan Review identity', 'plan_review_')
  assertSafeID(acceptance.snapshotId, 'Snapshot identity', 'context_snapshot_')
  assertSafeID(acceptance.charterId, 'Charter identity', 'charter_')
  assertDigest(acceptance.snapshotDigest, 'Snapshot digest')
  assert(acceptance.adapterId === 'pi', 'Real Plan acceptance did not bind Pi')
}

function compactTask(task) {
  return {
    id: task.id, roomId: task.roomId, title: task.title, requirementSha256: sha256(task.goal),
    criterionIds: task.criteria.map((criterion) => criterion.id), draftId: task.planning.draft.id,
    executionProfile: task.executionProfile, repository: task.repository,
  }
}

async function startService(label) {
  const child = spawn(binary, serveArgv, { env: process.env, stdio: ['ignore', 'pipe', 'pipe'], shell: false })
  const record = { child, pid: child.pid, label, startedAt: new Date().toISOString(), stdout: '', stderr: '' }
  child.stdout.on('data', (chunk) => { record.stdout = appendBounded(record.stdout, redact(chunk)) })
  child.stderr.on('data', (chunk) => { record.stderr = appendBounded(record.stderr, redact(chunk)) })
  child.on('error', (error) => { record.spawnError = redact(error.message) })
  serviceHistory.push(record)
  const deadline = Date.now() + 90_000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`chora serve exited ${child.exitCode}: ${record.stderr}`)
    const announced = record.stdout.includes(`Chora Room available at ${baseURL}`)
    const response = announced ? await apiRaw('GET', '/api/status', undefined, 5_000).catch(() => undefined) : undefined
    if (response?.status === 200) {
      await delay(100)
      assert(child.exitCode === null, `spawned chora serve did not retain ${baseURL}`)
      return record
    }
    await delay(100)
  }
  throw new Error(`chora serve did not become ready: ${record.stderr}`)
}

async function stopService(record, reason) {
  assert(record.child.exitCode === null, `service already exited before ${reason}`)
  record.child.kill('SIGTERM')
  let exit
  try {
    exit = await waitChild(record.child, 15_000)
  } catch (error) {
    record.child.kill('SIGKILL')
    await waitChild(record.child, 5_000).catch(() => {})
    throw error
  }
  assert(exit.code === 0 && exit.signal === null, `service did not stop gracefully for ${reason}: code=${exit.code} signal=${exit.signal}`)
  return { reason, code: exit.code, signal: exit.signal, graceful: true }
}

async function forceStopService(record) {
  if (record.child.exitCode !== null) return
  record.child.kill('SIGTERM')
  await waitChild(record.child, 5_000).catch(async () => {
    record.child.kill('SIGKILL')
    await waitChild(record.child, 5_000).catch(() => {})
  })
}

async function waitChild(child, timeout) {
  if (child.exitCode !== null) return { code: child.exitCode, signal: child.signalCode }
  return new Promise((resolveWait, rejectWait) => {
    const timer = setTimeout(() => rejectWait(new Error('child exit timeout')), timeout)
    child.once('exit', (code, signal) => { clearTimeout(timer); resolveWait({ code, signal }) })
  })
}

async function serviceStatus() {
  const status = await api('GET', '/api/status', undefined, 200)
  assert(status.codex?.enabled === false, 'legacy Codex Runtime must remain disabled')
  assert(status.pi?.enabled === true, `Pi is not enabled: ${status.pi?.reason ?? 'unknown reason'}`)
  assert(status.verifier?.enabled === true, `Verifier is not enabled: ${status.verifier?.reason ?? 'unknown reason'}`)
  return status
}

function statusIdentity(status) {
  return {
    codex: { enabled: status.codex.enabled },
    pi: { enabled: status.pi.enabled, hostReadIsolation: status.pi.hostReadIsolation, provider: status.pi.provider, image: status.pi.image, policyFingerprint: status.pi.policyFingerprint },
    verifier: {
      enabled: status.verifier.enabled, mode: status.verifier.mode, policyVersion: status.verifier.policyVersion,
      policyDigest: status.verifier.policyDigest, baselineDigest: status.verifier.baselineDigest,
      image: status.verifier.image, network: status.verifier.network, credentials: status.verifier.credentials,
      resourceBoundary: status.verifier.resourceBoundary,
    },
  }
}

async function assertBootstrapRoutesClosed() {
  for (const path of closedBootstrapRoutes) {
    const response = await apiRaw('POST', path, {}, 30_000, { 'Idempotency-Key': `closed-route-${randomUUID()}` })
    assert(response.status === 404, `${path} is not closed: HTTP ${response.status}`)
  }
}

function assertBootstrapAudit() {
  const requests = requestAudit.filter((request) => closedBootstrapRoutes.includes(request.path))
  assert(requests.length === 2 && requests.every((request) => request.source === 'explicit-404-probe' && request.method === 'POST'), 'bootstrap route was requested outside the explicit 404 probes')
}

async function api(method, path, body, expected, headers = {}, timeout = 30_000) {
  const response = await apiRaw(method, path, body, timeout, headers)
  if (response.status !== expected) throw new Error(`${method} ${path}: HTTP ${response.status}, expected ${expected}: ${redact(response.text.slice(0, 2000))}`)
  if (response.text === '') return undefined
  assert(response.value !== undefined, `${method} ${path}: response is not JSON`)
  return response.value
}

async function apiRaw(method, path, body, timeout = 30_000, headers = {}) {
  const bootstrapProbe = method === 'POST' && closedBootstrapRoutes.includes(path)
  requestAudit.push({ source: bootstrapProbe ? 'explicit-404-probe' : 'node', method, path: new URL(path, baseURL).pathname })
  const response = await fetch(`${baseURL}${path}`, {
    method,
    headers: body === undefined ? headers : { 'Content-Type': 'application/json', ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(timeout),
  })
  const text = await response.text()
  assertNoProhibitedPublicContent(text, `${method} ${path}`)
  let value
  if (text !== '') {
    try { value = JSON.parse(text) } catch { value = undefined }
  }
  return { status: response.status, text, value }
}

async function waitForNestedRun(roomID, taskID, runID, target, timeout) {
  const deadline = Date.now() + timeout
  let latest
  while (Date.now() < deadline) {
    latest = await api('GET', `/api/rooms/${roomID}/tasks/${taskID}/runs/${runID}`, undefined, 200)
    assert(latest.id === runID && latest.room?.id === roomID && latest.task?.id === taskID, 'polled nested Run ownership drift')
    if (latest.status === target) return latest
    if (['cancelled', 'revision_required', 'recovery_required', 'verification_recovery_required', 'rejected', 'accepted'].includes(latest.status)) throw new Error(`Run reached unexpected terminal state ${latest.status}`)
    await delay(250)
  }
  throw new Error(`Run did not reach ${target}; latest=${redact(JSON.stringify(latest))}`)
}

function assertReviewReady(run) {
  assertNoRetry(run, 'review-ready journey')
  const attempt = run.verification?.attempts?.[0]
  assert(attempt?.state === 'completed' && attempt.evidenceComplete === true && attempt.cleanupProven === true, 'Verifier evidence or cleanup is incomplete')
  assert(attempt.commands?.length === 1 && attempt.commands[0].classification === 'exited' && attempt.commands[0].exitCode === 0, 'declared verification command did not pass')
  assert(attempt.checks?.length === run.criteria.length && attempt.checks.every((check) => check.status === 'passed' && check.trust === 'trusted'), 'trusted Acceptance Checks did not pass')
  assert(run.verification.result?.outcome === 'review_ready', 'independent Verification is not review-ready')
  assertDigest(run.reviewablePatch?.patchDigest, 'Reviewable Patch digest')
  const patchPaths = run.reviewablePatch?.files?.map((file) => file.path) ?? []
  assert(patchPaths.length > 0 && patchPaths.every((path) => positiveTask.writableFiles.includes(path)), 'Reviewable Patch escaped the frozen writable-file set')
}

function assertNoRetry(run, label) {
  assertAgentNoRetry(run, label)
  assert(run.verification?.attempts?.length === 1 && run.verification.attempts[0].sequence === 1 && !run.verification.attempts[0].predecessorId, `${label} contains hidden Verifier Retry`)
}

function assertAgentNoRetry(run, label) {
  assert(run.attempt === 1 && run.attemptHistory?.length === 1 && run.attemptHistory[0].sequence === 1, `${label} contains hidden Agent Retry`)
}

function assertNoFake(run) {
  const serialized = canonicalJSON(run)
  assert(run.adapter === 'pi' && run.agent?.profile?.id !== 'agent.fake', 'Fake adapter/profile appeared in the Real journey')
  assert(!serialized.includes('Fake Agent'), 'Fake Agent text appeared in the Real Run')
  assertNoInternalHostPath(run, 'public Run')
}

function immutableTaskBinding(run, task) {
  const acceptance = task.planning?.acceptance
  assert(acceptance && task.frozenSnapshot, 'accepted Task binding is incomplete')
  assert(acceptance.snapshotId === task.frozenSnapshot.id && acceptance.snapshotDigest === task.frozenSnapshot.digest, 'Task Snapshot views disagree')
  assert(run.task?.id === task.id && run.snapshot?.id === acceptance.snapshotId && run.snapshot?.digest === acceptance.snapshotDigest, 'Run substituted Task Snapshot binding')
  const revision = task.planning.revisions.find((item) => item.id === acceptance.revisionId)
  assert(revision?.current === true && revision.review?.id === acceptance.reviewId, 'accepted Plan Revision/Review history is incomplete')
  return {
    task: {
      roomId: task.roomId, taskId: task.id, title: task.title, requirement: task.goal,
      executionProfile: task.executionProfile, repository: task.repository, criteria: task.criteria,
      selection: task.selection,
    },
    plan: { revision, reviewId: acceptance.reviewId },
    execution: {
      charterId: acceptance.charterId, adapterId: acceptance.adapterId,
      snapshotId: acceptance.snapshotId, snapshotDigest: acceptance.snapshotDigest,
      frozenSnapshot: task.frozenSnapshot,
    },
  }
}

function immutableIdentity(run, taskID, snapshotID) {
  assert(run.id && run.task?.id && run.snapshot?.id && run.snapshot?.digest, 'Run/Task/Snapshot identity is incomplete')
  assert(run.agent?.profile?.id && run.agentReport?.id && run.agentReport?.attemptId, 'Agent identity is incomplete')
  assert(run.attemptDetail?.id && run.attemptDetail?.runtime?.sessionId && run.attemptDetail?.runtime?.fingerprint, 'Agent Attempt/Runtime identity is incomplete')
  const verification = run.verification
  const verifierAttempt = verification?.attempts?.[0]
  const result = verification?.result
  const patch = run.reviewablePatch
  assert(verification?.id && verification.agentAttemptId && verifierAttempt?.id && verifierAttempt.workspaceIdentity, 'Verifier identity is incomplete')
  assert(verifierAttempt.commands?.length === 1 && verifierAttempt.commands[0].id && verifierAttempt.commands[0].verifierIdentity && verifierAttempt.commands[0].workspaceIdentity, 'Verifier command identity is incomplete')
  assert(result?.id && result.attemptId, 'Result identity is incomplete')
  assert(patch?.resultId && patch.agentAttemptId && patch.verificationAttemptId && patch.artifactId && patch.rawDownload, 'Patch identity is incomplete')
  for (const [name, value] of Object.entries({
    snapshotDigest: run.snapshot.digest, baselineDigest: patch.baselineDigest, declaredFilesDigest: patch.declaredFilesDigest,
    patchDigest: patch.patchDigest, contextSnapshotDigest: verification.bindings?.contextSnapshotDigest,
    acceptanceContractDigest: verification.bindings?.acceptanceContractDigest, verifierPolicyDigest: verification.bindings?.verifierPolicyDigest,
  })) assertDigest(value, name)
  assert(run.task.id === taskID && run.snapshot.id === snapshotID, 'Run is not bound to discovered Task/Snapshot identities')
  assert(run.agentReport.attemptId === run.attemptDetail.id && verification.agentAttemptId === run.attemptDetail.id, 'Agent/Verifier binding drift')
  assert(result.attemptId === verifierAttempt.id, 'Result/Verifier Attempt identity drift')
  assert(patch.resultId === result.id && patch.agentAttemptId === run.attemptDetail.id && patch.verificationAttemptId === verifierAttempt.id, 'Patch binding identity drift')
  return {
    run: { id: run.id, taskId: run.task.id, adapter: run.adapter }, snapshot: { id: run.snapshot.id, digest: run.snapshot.digest },
    agent: { profileId: run.agent.profile.id, attemptId: run.attemptDetail.id, runtimeSessionId: run.attemptDetail.runtime.sessionId, runtimeFingerprint: run.attemptDetail.runtime.fingerprint, reportId: run.agentReport.id, reportAttemptId: run.agentReport.attemptId },
    verifier: { id: verification.id, agentAttemptId: verification.agentAttemptId, bindings: verification.bindings, attemptId: verifierAttempt.id, workspaceIdentity: verifierAttempt.workspaceIdentity, commandIdentities: verifierAttempt.commands.map(({ id, commandId, verifierIdentity, workspaceIdentity }) => ({ id, commandId, verifierIdentity, workspaceIdentity })), checkIds: verifierAttempt.checks.map(({ id, criterionId }) => ({ id, criterionId })) },
    result: { id: result.id, attemptId: result.attemptId, outcome: result.outcome },
    patch: { patchDigest: patch.patchDigest, baselineDigest: patch.baselineDigest, declaredFilesDigest: patch.declaredFilesDigest, resultId: patch.resultId, agentAttemptId: patch.agentAttemptId, verificationAttemptId: patch.verificationAttemptId, artifactId: patch.artifactId, rawDownload: patch.rawDownload },
  }
}

async function downloadPatch(runID, identity) {
  const response = await fetch(`${baseURL}/api/runs/${runID}/patch`, { signal: AbortSignal.timeout(30_000) })
  const body = Buffer.from(await response.arrayBuffer())
  assert(response.status === 200 && body.length > 0, `Patch download failed: HTTP ${response.status}`)
  const digest = sha256(body)
  const headers = { contentType: response.headers.get('content-type'), contentDisposition: response.headers.get('content-disposition'), etag: response.headers.get('etag'), patchSha256: response.headers.get('x-chora-patch-sha256'), resultId: response.headers.get('x-chora-result-id') }
  assert(headers.contentType === 'text/x-diff; charset=utf-8', 'Patch Content-Type drift')
  assert(headers.contentDisposition === `attachment; filename="chora-${runID}.patch"`, 'Patch Content-Disposition drift')
  assert(headers.etag === `"sha256:${digest}"`, 'Patch ETag drift')
  assert(headers.patchSha256 === digest && digest === identity.patchDigest, 'Patch digest/header identity drift')
  assert(headers.resultId === identity.resultId, 'Patch Result identity header drift')
  return { body, digest, headers }
}

function assertPatchEqual(before, after) {
  assert(before.body.equals(after.body), 'Patch body drift after restart')
  assertDeepEqual(before.headers, after.headers, 'Patch headers drift after restart')
  assert(before.digest === after.digest, 'Patch digest drift after restart')
}

async function allEvents(runID, terminalType) {
  const events = []
  let after = 0
  for (;;) {
    const pageOfEvents = await api('GET', `/api/runs/${runID}/events?after=${after}&limit=${eventPageSize}`, undefined, 200)
    assert(pageOfEvents.runId === runID && Array.isArray(pageOfEvents.events), 'Event page identity is incomplete')
    for (const event of pageOfEvents.events) {
      assert(Number.isSafeInteger(event.sequence) && event.sequence > after, 'Event sequence is incomplete or not strictly increasing')
      assert(typeof event.type === 'string' && event.type && typeof event.source === 'string' && event.source, 'Event type/source identity is incomplete')
      assert(typeof event.occurredAt === 'string' && typeof event.recordedAt === 'string' && event.normalized && typeof event.normalized === 'object', 'Event evidence is incomplete')
      events.push(event)
      after = event.sequence
    }
    if (pageOfEvents.events.length < eventPageSize) break
  }
  assert(events.length > 0 && events.at(-1).type === terminalType, `Event history does not end at ${terminalType}`)
  return events
}

function assertEventPrefix(prefix, complete) {
  assert(complete.length >= prefix.length, 'committed Events disappeared')
  for (let index = 0; index < prefix.length; index += 1) assertDeepEqual(prefix[index], complete[index], `durable Event ${prefix[index].sequence} changed or disappeared`)
}

async function proveTerminalResidueAbsent() {
  const deadline = Date.now() + 30_000
  let latest
  while (Date.now() < deadline) {
    latest = {
      containers: await dockerIDs(['ps', '-a', '--filter', 'label=chora.owner', '--format', '{{.ID}}']),
      verifierContainers: await dockerIDs(['ps', '-a', '--filter', 'label=chora.verifier_runtime_id', '--format', '{{.ID}}']),
      networks: await dockerIDs(['network', 'ls', '--filter', 'label=chora.owner', '--format', '{{.ID}}']),
      agentWorkspaces: await directoryEntries(join(dataRoot, 'runtime', 'pi')),
      verifierWorkspaces: await directoryEntries(join(dataRoot, 'runtime', 'verification', 'workspaces')),
    }
    if (Object.values(latest).every((items) => items.length === 0)) return { schemaVersion: 'chora.m1-u4-terminal-residue.v1', status: 'absent', labeledContainers: 0, labeledVerifierContainers: 0, labeledNetworks: 0, agentWorkspaces: 0, verifierWorkspaces: 0 }
    await delay(250)
  }
  throw new Error(`terminal labeled Docker/network or Agent/Verifier workspace residue remains: ${redact(JSON.stringify(latest))}`)
}

async function dockerIDs(args) {
  const result = await directExec(docker, args, 15_000)
  assert(result.code === 0, `Docker inventory failed: ${result.stderr}`)
  return result.stdout.trim().split(/\s+/).filter(Boolean)
}

async function directExec(executable, args, timeout) {
  const child = spawn(executable, args, { env: process.env, stdio: ['ignore', 'pipe', 'pipe'], shell: false })
  let stdout = ''
  let stderr = ''
  child.stdout.on('data', (chunk) => { stdout = appendBounded(stdout, redact(chunk)) })
  child.stderr.on('data', (chunk) => { stderr = appendBounded(stderr, redact(chunk)) })
  return new Promise((resolveExec, rejectExec) => {
    const timer = setTimeout(() => { child.kill('SIGKILL'); rejectExec(new Error(`${executable} timed out`)) }, timeout)
    child.once('error', (error) => { clearTimeout(timer); rejectExec(error) })
    child.once('exit', (code, signal) => { clearTimeout(timer); resolveExec({ code, signal, stdout, stderr }) })
  })
}

async function gitIndexIdentity(worktreeRoot) {
  const result = await directExec('git', ['-C', worktreeRoot, 'write-tree'], 15_000)
  assert(result.code === 0 && /^[0-9a-f]{40,64}$/.test(result.stdout.trim()), `Git index identity failed: ${result.stderr}`)
  return result.stdout.trim()
}

async function digestTargetFiles(worktreeRoot, paths) {
  return Promise.all(paths.map(async (path) => sha256(await readFile(resolve(worktreeRoot, path)))))
}

function taskWorktreePath(task) {
	const locator = task.worktree?.locator
	assert(typeof locator === 'string' && /^[A-Za-z0-9][A-Za-z0-9._-]*-task_[A-Za-z0-9_-]+-[a-z0-9-]+$/.test(locator), 'Task worktree locator is unsafe')
	const result = resolve(repositoryParent, locator)
	assert(dirname(result) === repositoryParent && result !== repositoryRoot, 'Task worktree is not a direct repository sibling')
	for (const root of [sourceRoot, installRoot, dataRoot, repositoryRoot]) {
		assert(!insideOrSame(root, result) && !insideOrSame(result, root), 'Task worktree overlaps a protected product root')
	}
	return result
}

async function directoryEntries(path) {
  try { return await readdir(path) } catch (error) {
    if (error?.code === 'ENOENT') return []
    throw error
  }
}

async function assertNoHostPathDisplay(currentPage) {
  const body = await currentPage.locator('body').innerText()
  assertNoProhibitedPublicContent(body, 'public UI')
  for (const path of [installRoot, dataRoot, sourceRoot, repositoryRoot, authFile, caFile]) assert(!body.includes(path), 'public UI displayed an internal host path')
}

function assertNoInternalHostPath(value, label) {
  const serialized = typeof value === 'string' ? value : canonicalJSON(value)
  assertNoProhibitedPublicContent(serialized, label)
  for (const path of [installRoot, dataRoot, sourceRoot, repositoryRoot, authFile, caFile]) assert(!serialized.includes(path), `${label} exposed an internal host path`)
}

async function persistEvidence(artifacts) {
  await mkdir(evidenceRoot, { recursive: true, mode: 0o700 })
  await chmod(evidenceRoot, 0o700)
  for (const [name, body] of artifacts) {
    assertNoKnownSecret(body)
    const path = resolve(evidenceRoot, name)
    assert(insideOrSame(evidenceRoot, path) && path !== evidenceRoot, 'evidence path escaped CHORA_EVIDENCE_DIR')
    await writeFile(path, body, { flag: 'wx', mode: 0o600 })
    await chmod(path, 0o600)
  }
}

function encodeJSON(value) { return Buffer.from(`${JSON.stringify(value, null, 2)}\n`) }

function canonicalJSON(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(',')}]`
  if (value && typeof value === 'object') return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(',')}}`
  return JSON.stringify(value)
}

function assertDeepEqual(expected, actual, message) { assert(canonicalJSON(expected) === canonicalJSON(actual), message) }

function redact(value) {
  let output = String(value)
  for (const secret of [...knownSecrets, authFile, caFile, proxyURL.toString(), proxyURL.origin, modelURL.toString()]) if (secret) output = output.replaceAll(secret, '[REDACTED]')
  return output
    .replace(/(authorization|token|password|secret|cookie|api[_ -]?key)(["'=:\s]+)[^\s,"']+/gi, '$1$2[REDACTED]')
    .replace(/\b(?:sk|sess)-[A-Za-z0-9_-]{6,}\b/g, '[REDACTED]')
}

function assertNoKnownSecret(value) {
  const body = Buffer.isBuffer(value) ? value : Buffer.from(String(value))
  for (const secret of knownSecrets) {
    if (secret.length >= 4) assert(!body.includes(Buffer.from(secret)), 'public or persisted evidence contains a known credential value')
  }
}

function assertNoProhibitedPublicContent(value, label) {
  const body = Buffer.isBuffer(value) ? value.toString('utf8') : String(value)
  assertNoKnownSecret(body)
  assert(!/\/(?:Users|home)\/[^/\s]+\//.test(body), `${label} exposed a private home path`)
  assert(!/(?:^|["'\s])vendor\//i.test(body), `${label} exposed vendor source metadata`)
  const enterprise = new RegExp(`(?:${[['ant', 'group'], ['ali', 'pay'], ['ali', 'baba'], ['tao', 'bao']].map((parts) => parts.join('')).join('|')})`, 'i')
  assert(!enterprise.test(body), `${label} exposed unrelated enterprise content`)
}

function collectCredentialScalars(value, output, key = '') {
  if (Array.isArray(value)) {
    for (const item of value) collectCredentialScalars(item, output, key)
    return
  }
  if (value && typeof value === 'object') {
    for (const [name, item] of Object.entries(value)) collectCredentialScalars(item, output, name)
    return
  }
  if (/^(access|refresh|account[_-]?id|user[_-]?id|email|organization|org[_-]?id)$|(?:access|refresh|id)[_-]?token|token|secret|password|cookie|credential|authorization/i.test(key) &&
    typeof value === 'string' && value.length >= 8) output.push(value)
}

function urlSecretScalars(url) {
  return [...url.searchParams.values()].filter((value) => value.length >= 4)
}

function safeServeArgv() {
  const sensitiveFlags = new Set(['--auth', '--ca', '--proxy', '--model-url'])
  return serveArgv.map((value, index) => {
    if (sensitiveFlags.has(serveArgv[index - 1])) return '[REDACTED]'
    return String(value).replaceAll(installRoot, '[INSTALL_ROOT]').replaceAll(dataRoot, '[DATA_ROOT]').replaceAll(sourceRoot, '[SOURCE_ROOT]').replaceAll(repositoryRoot, '[REPOSITORY_ROOT]')
  })
}

function appendBounded(current, addition) {
  const combined = current + addition
  return combined.length <= maxLogBytes ? combined : combined.slice(combined.length - maxLogBytes)
}

function mutatedID(value) {
  const last = value.at(-1)
  return `${value.slice(0, -1)}${last === 'a' ? 'b' : 'a'}`
}

function insideOrSame(root, child) {
  const rel = relative(resolve(root), resolve(child))
  return rel === '' || rel !== '..' && !rel.startsWith(`..${sep}`) && !rel.startsWith(sep)
}

async function assertFile(path, name, executable = false) {
  const info = await stat(path)
  assert(info.isFile(), `${name} must identify a file`)
  if (executable) assert((info.mode & 0o111) !== 0, `${name} must be executable`)
}

function absoluteRequired(name) {
  const value = required(name)
  assert(resolve(value) === value, `${name} must be an absolute normalized path`)
  return value
}

function required(name) {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} is required`)
  return value
}

function digestRequired(name) {
  const value = required(name)
  assertDigest(value, name)
  return value
}

function assertDigest(value, name) { assert(typeof value === 'string' && /^[0-9a-f]{64}$/.test(value), `${name} must be a lowercase SHA-256 digest`) }

function safeURL(value, name) {
  const url = new URL(value)
  assert(url.protocol === 'http:' || url.protocol === 'https:', `${name} must use HTTP(S)`)
  return url
}

function integerRequired(name, minimum, maximum) {
  const value = required(name)
  assert(/^\d+$/.test(value), `${name} must be an integer`)
  const parsed = Number(value)
  assert(Number.isSafeInteger(parsed) && parsed >= minimum && parsed <= maximum, `${name} is outside ${minimum}..${maximum}`)
  return parsed
}

function assertSafeID(value, name, prefix = '') { assert(typeof value === 'string' && value.startsWith(prefix) && /^[A-Za-z0-9_.:@+-]{1,256}$/.test(value), `${name} is absent or unsafe`) }

function sha256(value) { return createHash('sha256').update(value).digest('hex') }

function delay(milliseconds) { return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds)) }

function assert(condition, message) { if (!condition) throw new Error(message) }
