import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createHash, randomBytes } from 'node:crypto'
import { EventEmitter, once } from 'node:events'
import { chmod, copyFile, link, lstat, mkdir, mkdtemp, readFile, rename, rm, symlink, unlink, writeFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { PassThrough, Writable } from 'node:stream'
import test from 'node:test'
import { pathToFileURL } from 'node:url'

const workspace = resolve(import.meta.dirname, '..')
const runnerSourceFile = join(import.meta.dirname, 'o4-b2-runner.mjs')
const ownedPathSourceFile = join(import.meta.dirname, 'o4-owned-path-cleanup.mjs')
const totalSourceFile = join(import.meta.dirname, 'o4-total-real.mjs')
const { buildTestRepositoryCapture } = await import('./o4-repository-capture-test-helper.mjs')
const { ownedPathIdentity } = await import('./o4-owned-path-cleanup.mjs')
const totalTestHooks = await import(
  `${pathToFileURL(totalSourceFile).href}?synthetic=${randomBytes(8).toString('hex')}`)

async function fakeReadonlyController(t) {
  const root = await mkdtemp('/private/tmp/chora-o4-total-readonly-controller-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const executableFile = join(root, 'controller')
  await writeFile(executableFile, '#!/bin/sh\nexit 1\n', { mode: 0o500 })
  await chmod(executableFile, 0o500)
  return {
    executableFile,
    executableSha256: sha256(await readFile(executableFile)),
  }
}

function fakeRawSpawn(calls, action) {
  return (file, argv, options) => {
    const child = new EventEmitter()
    child.stdout = new PassThrough()
    child.stderr = new PassThrough()
    const chunks = []
    child.stdin = new Writable({
      write(chunk, _encoding, callback) { chunks.push(Buffer.from(chunk)); callback() },
      final(callback) {
        Promise.resolve().then(async () => {
          const stdin = Buffer.concat(chunks)
          calls.push({ file, argv: [...argv], options, stdin })
          const result = await action(stdin)
          if (result?.stdout !== undefined) child.stdout.write(result.stdout)
          if (result?.stderr !== undefined) child.stderr.write(result.stderr)
          callback()
          queueMicrotask(() => child.emit('close', result?.code ?? 0, result?.signal ?? null))
        }).catch((error) => {
          callback(error)
          queueMicrotask(() => child.emit('close', 1, null))
        })
      },
    })
    child.kill = () => queueMicrotask(() => child.emit('close', null, 'SIGKILL'))
    return child
  }
}

function readonlyReceipt(target, bytes, identity) {
  return `${JSON.stringify({
    schemaVersion: 'chora.m1-o4-owned-readonly-write-receipt.v1',
    status: 'passed', target, sha256: sha256(bytes), byteLength: bytes.length, identity,
  })}\n`
}

async function spawnLiveChild(t) {
  const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], {
    stdio: 'ignore',
  })
  await once(child, 'spawn')
  t.after(async () => {
    child.kill('SIGTERM')
    if (child.exitCode === null && child.signalCode === null) await once(child, 'exit')
  })
  return child
}

test('pinned B2 Runner has the identical sole factory export required by D and E', async () => {
  const loaded = await import(`${pathToFileURL(runnerSourceFile).href}?static=${randomBytes(8).toString('hex')}`)
  assert.deepEqual(Object.keys(loaded), ['createO4B2Runner'])
  assert.equal(typeof loaded.createO4B2Runner, 'function')
  const recovery = await readFile(join(import.meta.dirname, 'o4-recovery-residue-real.spec.ts'), 'utf8')
  const final = await readFile(join(import.meta.dirname, 'o4-final-evidence-real.spec.ts'), 'utf8')
  for (const source of [recovery, final]) {
    assert.match(source, /exactKeys\(runnerModule, \['createO4B2Runner'\]/)
    assert.doesNotMatch(source, /createO4(?:Recovery|FinalEvidence)Runner/)
  }
})

test('real fresh-root validation owns the absent stage runtime parent and requires exact control parent', async (t) => {
  const stageRoot = await mkdtemp('/private/tmp/chora-o4-total-root-contract-')
  const controlParent = await mkdtemp('/private/tmp/o4rc-')
  t.after(() => rm(stageRoot, { recursive: true, force: true }))
  t.after(() => rm(controlParent, { recursive: true, force: true }))
  await chmod(stageRoot, 0o700)
  await chmod(controlParent, 0o700)
  const runtime = join(stageRoot, 'runtime')
  const roots = {
    userRoot: join(runtime, 'user'), installRoot: join(runtime, 'install'),
    dataRoot: join(runtime, 'data'), stateRoot: join(runtime, 'state'),
    receiptRoot: join(runtime, 'receipts'), evidenceRoot: join(runtime, 'evidence'),
    runnerControlRoot: join(controlParent, 'r'), probeRuntimeRoot: join(runtime, 'probe-runtime'),
    colimaHome: join(controlParent, 'c'), dockerConfigRoot: join(runtime, 'docker-config'),
    temporaryRoot: join(controlParent, 't'),
  }
  const profileName = 'o4as-contract'
  const manifest = {
    roots,
    runner: { controlSocketPath: join(roots.runnerControlRoot, 'o4-runner.sock'),
      sessionAuthorityFile: join(roots.runnerControlRoot, 'session-authority.json') },
    engine: { profileName, contextName: `colima-${profileName}`,
      endpointSocketPath: join(roots.colimaHome, profileName, 'docker.sock'),
      zeroImagePreflightFile: join(roots.evidenceRoot, 'engine-preflight.json'),
      cleanupResidueFile: join(roots.evidenceRoot, 'engine-residue.json') },
    postflight: { finalEvidenceDirectory: join(roots.evidenceRoot, 'final') },
    completionFile: join(roots.evidenceRoot, 'completion.json'),
  }

  await totalTestHooks.validateO4FreshRoots(manifest)
  await assert.rejects(lstat(runtime), { code: 'ENOENT' })

  await chmod(controlParent, 0o755)
  await assert.rejects(totalTestHooks.validateO4FreshRoots(manifest),
    /fresh control parent is not owner-only 0700/)
  await chmod(controlParent, 0o700)

  const drifted = structuredClone(manifest)
  drifted.roots.dockerConfigRoot = join(stageRoot, 'wrong-parent', 'docker-config')
  await assert.rejects(totalTestHooks.validateO4FreshRoots(drifted),
    /fresh stage-runtime root binding drifted/)
})

test('B2 cleanup and A3 publication are exclusively native identity-bound protocols', async () => {
  const source = await readFile(runnerSourceFile, 'utf8')
  assert.match(source,
    /cleanupOwnedPath, ownedPathIdentity, replaceOwnedPath, validateOwnedPathCapability/)
  assert.match(source,
    /executableFile: value\.serviceController\.executableFile,[\s\S]*?executableSha256: value\.serviceController\.sha256/)
  assert.match(source, /disposition: 'recursive_directory'/)
  assert.match(source, /disposition: 'regular_file'/)
  assert.match(source, /disposition: 'unix_socket'/)
  assert.match(source, /replaceOwnedPath\(\{[\s\S]*?sourceIdentity:[\s\S]*?targetIdentity:/)
  assert.match(source, /atomic cleanup unavailable for \$\{label\}; residue retained/)
  assert.match(source, /new AggregateError\(\[error, \.\.\.cleanupErrors\]/)
  assert.match(source, /ownedPathIdentity\(await handle\.stat\(\{ bigint: true \}\)\)/)
  assert.doesNotMatch(source, /\b(?:unlink|rm|rmdir|rename)\s*\(/)
  assert.doesNotMatch(source,
    /from 'node:fs\/promises'[\s\S]{0,160}\b(?:unlink|rm|rmdir|rename)\b/)
})

test('synthetic-only in-process transport executes D arm then exact closure-to-seal state machine', async (t) => {
  // This fixture is explicitly synthetic_non_acceptance. It exercises the same
  // sole factory and supervisor state machine without opening a Unix socket;
  // it never invokes the real total driver or creates acceptance evidence.
  const root = await mkdtemp('/private/tmp/chora-o4-total-synthetic-non-acceptance-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const controlRoot = join(root, 'control')
  const playwrightPackageRoot = join(root, 'playwright-package')
  const playwrightBrowserRoot = join(root, 'playwright-browsers')
  await mkdir(controlRoot, { mode: 0o700 })
  await mkdir(playwrightPackageRoot, { mode: 0o700 })
  await mkdir(playwrightBrowserRoot, { mode: 0o700 })
  const runnerFile = join(root, 'o4-b2-runner.mjs')
  const ownedPathFile = join(root, 'o4-owned-path-cleanup.mjs')
  await copyFile(runnerSourceFile, runnerFile)
  await copyFile(ownedPathSourceFile, ownedPathFile)
  await chmod(runnerFile, 0o400)
  await chmod(ownedPathFile, 0o400)
  const runnerSha256 = sha256(await readFile(runnerFile))
  const playwrightCLIFile = join(playwrightPackageRoot, 'cli.js')
  await writeFile(playwrightCLIFile, 'process.exit(0)\n', { mode: 0o400, flag: 'wx' })
  const tupleIdentity = sha256('synthetic tuple identity; never acceptance')
  const tupleFile = join(root, 'tuple.json')
  const sourceA3LedgerFile = join(root, 'source-a3.json')
  const installedBinaryFile = join(root, 'synthetic-service.mjs')
  await writeFile(installedBinaryFile, '#!/usr/bin/env node\nsetInterval(() => {}, 1000)\n',
    { mode: 0o500, flag: 'wx' })
  await chmod(installedBinaryFile, 0o500)
  const binarySha256 = sha256(await readFile(installedBinaryFile))
  const controllerFixture = await buildTestRepositoryCapture()
  t.after(() => controllerFixture.cleanup())
  const controllerFile = controllerFixture.capture.executableFile
  const controllerSha256 = controllerFixture.capture.executableSha256
  const generationId = 'gen_abcdefghijklmnopqrstuvwxyz123456'
  const environmentId = 'env_abcdefghijklmnopqrstuvwxyz123456'
  const installId = 'ins_abcdefghijklmnopqrstuvwxyz123456'
  await write0400(tupleFile, {
    identity: tupleIdentity, environmentId, installId, generationId,
  })
  const a3 = { schemaVersion: 'synthetic-non-acceptance-a3.v1', status: 'recording', complete: false,
    auditSealed: false, tupleIdentity, bindingDigest: sha256('synthetic binding'),
    auditRecordCount: 1, auditFinalDigest: sha256('synthetic audit') }
  await writeFile(sourceA3LedgerFile, `${JSON.stringify(a3)}\n`, { mode: 0o600, flag: 'wx' })
  await chmod(sourceA3LedgerFile, 0o600)
  const unsealedSha256 = sha256(await readFile(sourceA3LedgerFile))
  const unsealedInfo = await lstat(sourceA3LedgerFile)
  const configFile = join(root, 'candidate-config.json')
  const identity = { environmentId, installId, generationId,
    platform: { os: 'darwin', architecture: 'arm64' } }
  const repository = { root: join(root, 'repository'), commit: 'a'.repeat(40),
    closureSha256: sha256('synthetic repository closure') }
  await write0400(configFile, { identity,
    paths: { installedBinaryFile, repositoryRoot: repository.root },
    authority: { binarySha256, repositoryCommit: repository.commit,
      repositoryClosureSha256: repository.closureSha256 } })
  const configSha256 = sha256(await readFile(configFile))
  const controllerRequestFile = join(root, 'controller-request.json')
  const controllerProofFile = join(root, 'controller-proof.json')
  const restartReceiptFile = join(root, '03-restart.json')
  const manifestFile = join(root, 'total-manifest.json')
  const authorityFile = join(controlRoot, 'session.json')
  const socketPath = join(controlRoot, 'runner.sock')
  const manifestDraft = {
    schemaVersion: 'chora.m1-o4-total-input.v1', status: 'authorized',
    acceptanceClass: 'synthetic_non_acceptance', selfDigest: null,
    identity,
    candidate: { tupleIdentity: null, tupleFile, configFile, configSha256 },
    repository,
    runner: { moduleFile: runnerFile, moduleSha256: runnerSha256,
      controlSocketPath: socketPath, sessionAuthorityFile: authorityFile },
    serviceController: { executableFile: controllerFile,
      sha256: controllerSha256, requestFile: controllerRequestFile },
    ocr: {}, engine: {}, playwright: { nodeFile: installedBinaryFile, nodeSha256: binarySha256,
      cliFile: playwrightCLIFile, cliSha256: sha256(await readFile(playwrightCLIFile)),
      packageRoot: playwrightPackageRoot, packageClosureSha256: sha256('synthetic package closure'),
      browserRoot: playwrightBrowserRoot, browserClosureSha256: sha256('synthetic browser closure') },
    roots: {}, phases: { D: { environment: {
      CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROL_FILE: controllerProofFile,
      CHORA_O4_RECOVERY_RESIDUE_ORPHAN_RESTART_RECEIPT_FILE: restartReceiptFile,
    } } }, postflight: {},
    timeouts: { phaseMs: 1_000, controllerMs: 1_000, exporterMs: 1_000, closeMs: 1_000 },
    completionFile: join(root, 'completion.json'),
  }
  const manifest = { ...manifestDraft, selfDigest: sha256(canonical(manifestDraft)) }
  await write0400(manifestFile, manifest)
  const manifestSha256 = sha256(await readFile(manifestFile))
  const authorityDraft = {
    schemaVersion: 'chora.m1-o4-runner-session-authority.v1', status: 'exclusive',
    manifestSha256, runnerSha256, socketPathDigest: sha256(socketPath), tupleIdentity,
    identity: manifest.identity, parentPid: process.pid,
    sessionId: sha256('synthetic session'), sessionSecret: sha256('synthetic secret'), sequenceStart: 0,
  }
  await write0400(authorityFile, { ...authorityDraft, authorityDigest: sha256(canonical(authorityDraft)) })
  const loaded = await import(`${pathToFileURL(runnerFile).href}?fixture=${randomBytes(8).toString('hex')}`)
  const missingRepositoryFile = join(root, 'missing-repository-total-manifest.json')
  const missingRepositoryDraft = structuredClone(manifest)
  delete missingRepositoryDraft.repository
  missingRepositoryDraft.selfDigest = null
  const missingRepositoryManifest = { ...missingRepositoryDraft,
    selfDigest: sha256(canonical(missingRepositoryDraft)) }
  await write0400(missingRepositoryFile, missingRepositoryManifest)
  await assert.rejects(loaded.createO4B2Runner({ view: 'install',
    manifestFile: missingRepositoryFile,
    manifestSha256: sha256(await readFile(missingRepositoryFile)),
    sessionAuthorityFile: authorityFile, inProcessTestTransport: true }),
  /total manifest keys drifted/)
  const realAuthorityFile = join(controlRoot, 'real-session.json')
  const realManifestFile = join(root, 'real-total-manifest.json')
  const realDraft = { ...manifest, acceptanceClass: 'real_acceptance', selfDigest: null,
    runner: { ...manifest.runner, sessionAuthorityFile: realAuthorityFile } }
  const realManifest = { ...realDraft, selfDigest: sha256(canonical(realDraft)) }
  await write0400(realManifestFile, realManifest)
  const realManifestSha256 = sha256(await readFile(realManifestFile))
  const realAuthorityDraft = { ...authorityDraft, manifestSha256: realManifestSha256,
    socketPathDigest: sha256(realManifest.runner.controlSocketPath), authorityDigest: undefined }
  delete realAuthorityDraft.authorityDigest
  await write0400(realAuthorityFile, { ...realAuthorityDraft,
    authorityDigest: sha256(canonical(realAuthorityDraft)) })
  await assert.rejects(loaded.createO4B2Runner({ view: 'install', manifestFile: realManifestFile,
    manifestSha256: realManifestSha256, sessionAuthorityFile: realAuthorityFile,
    inProcessTestTransport: true }), /synthetic non-acceptance only/)
  const options = { manifestFile, manifestSha256, sessionAuthorityFile: authorityFile,
    inProcessTestTransport: true }
  const install = await loaded.createO4B2Runner({ view: 'install', ...options })
  const invalid0400 = join(root, 'invalid-data-not-executable.mjs')
  await copyFile(installedBinaryFile, invalid0400); await chmod(invalid0400, 0o400)
  await assert.rejects(install.exec({ file: invalid0400, argv: [], shell: false,
    maxOutputBytes: 1024 }), /exact owner-only executable 0500/)
  const invalid0700 = join(root, 'invalid-owner-writable.mjs')
  await copyFile(installedBinaryFile, invalid0700); await chmod(invalid0700, 0o700)
  await assert.rejects(install.exec({ file: invalid0700, argv: [], shell: false,
    maxOutputBytes: 1024 }), /exact owner-only executable 0500/)
  const linkedExecutable = join(root, 'linked-executable.mjs')
  await link(installedBinaryFile, linkedExecutable)
  await assert.rejects(install.exec({ file: linkedExecutable, argv: [], shell: false,
    maxOutputBytes: 1024 }), /not one bounded owner file/)
  await unlink(linkedExecutable)
  const symlinkExecutable = join(root, 'symlink-executable.mjs')
  await symlink(installedBinaryFile, symlinkExecutable)
  await assert.rejects(install.exec({ file: symlinkExecutable, argv: [], shell: false,
    maxOutputBytes: 1024 }), /symlink ancestor/)
  const replacementVictim = join(root, 'replacement-victim.mjs')
  const replacementCandidate = join(root, 'replacement-candidate.mjs')
  await copyFile(installedBinaryFile, replacementVictim); await chmod(replacementVictim, 0o500)
  await writeFile(replacementCandidate, '#!/usr/bin/env node\nprocess.exit(19)\n',
    { mode: 0o500, flag: 'wx' })
  const hookSymbol = Symbol.for('chora.o4.syntheticPinReadHook')
  globalThis[hookSymbol] = async (path) => {
    if (path === replacementVictim) await rename(replacementCandidate, replacementVictim)
  }
  try {
    await assert.rejects(install.exec({ file: replacementVictim, argv: [], shell: false,
      maxOutputBytes: 1024 }), /path identity changed after read/)
  } finally { delete globalThis[hookSymbol] }
  await install.start({ file: installedBinaryFile, argv: ['serve', '--port', '45678',
    '--generation', generationId, '--active-generation', generationId], shell: false,
  maxOutputBytes: 1024 })
  const D = await loaded.createO4B2Runner({ view: 'D', ...options })
  assert.deepEqual(Object.keys(D).sort(), [
    'exec', 'installImmutable', 'processBoundary', 'start',
    'armAuthenticatedControllerRecovery', 'awaitAuthenticatedControllerProof',
    'exportApplyDisclosureSources', 'abortApplyDisclosureExporter',
  ].sort())
  const taskId = 'task_018f1e2d-3c4b-7abc-8def-0123456789ab'
  const runId = 'run_018f1e2d-3c4b-7abc-8def-0123456789ac'
  const attemptId = 'attempt_018f1e2d-3c4b-7abc-8def-0123456789ad'
  await write0400(controllerRequestFile, {
    schemaVersion: 'chora.m1-o4-service-control-request.v1', action: 'interrupt_restart_and_prove',
    controllerSha256, tupleFile, restartReceiptFile, taskId, runId, interruptedAttemptId: attemptId,
  })
  await D.armAuthenticatedControllerRecovery({ requestFile: controllerRequestFile,
    controllerExecutable: controllerFile, controllerSha256, taskId, runId, attemptId })
  const E = await loaded.createO4B2Runner({ view: 'E', ...options })
  assert.deepEqual(Object.keys(E).sort(), ['closeAllServices', 'sealA3Ledger'].sort())
  const closure = await E.closeAllServices({ tupleFile, sourceA3LedgerFile, timeoutMs: 1_000 })
  assert.deepEqual(Object.keys(closure).sort(), ['schemaVersion', 'status', 'environmentId', 'installId',
    'generationId', 'bindingDigest', 'tupleIdentity', 'runnerModuleSha256', 'registeredServiceCount',
    'closedServiceCount', 'services', 'processGroupDead', 'descendantsDead', 'completedAt', 'digest'].sort())
  assert.equal(closure.registeredServiceCount, 1)
  assert.equal(closure.closedServiceCount, 1)
  assert.equal(closure.processGroupDead, true)
  assert.equal(closure.descendantsDead, true)
  assert.equal(closure.digest, sha256(canonical(Object.fromEntries(
    Object.entries(closure).filter(([key]) => key !== 'digest')))))
  const seal = await E.sealA3Ledger({ tupleIdentity, sourceA3LedgerFile,
    expectedUnsealedSha256: unsealedSha256, expectedAuditRecordCount: 1,
    expectedAuditFinalDigest: a3.auditFinalDigest })
  assert.deepEqual(seal, { status: 'sealed', beforeSha256: unsealedSha256,
    auditRecordCount: 1, auditFinalDigest: a3.auditFinalDigest })
  const sealedInfo = await lstat(sourceA3LedgerFile)
  assert.equal(sealedInfo.mode & 0o777, 0o400)
  assert.notEqual(sealedInfo.ino, unsealedInfo.ino)
  const sealed = JSON.parse(await readFile(sourceA3LedgerFile, 'utf8'))
  assert.equal(sealed.status, 'passed')
  assert.equal(sealed.complete, true)
  assert.equal(sealed.auditSealed, true)
  const runnerSource = await readFile(runnerFile, 'utf8')
  assert.match(runnerSource, /install: Object\.freeze\(\['exec', 'installImmutable', 'processBoundary', 'start'\]\)/)
  assert.match(runnerSource, /replaceOwnedPath\(/)
  assert.match(runnerSource, /disposition: 'unix_socket'/)
  assert.doesNotMatch(runnerSource, /\b(?:unlink|rm|rmdir|rename)\s*\(/)
  t.diagnostic('synthetic_non_acceptance: no Docker, Colima, network, install, product, UI, OCR, or evidence action ran')
})

test('real acceptance manifest fails closed if synthetic in-process transport is requested', async () => {
  const source = await readFile(runnerSourceFile, 'utf8')
  assert.match(source, /binding\.manifest\.acceptanceClass === 'synthetic_non_acceptance'/)
  assert.match(source, /in-process Runner transport is synthetic non-acceptance only/)
})

test('private phase-E marker/corpus form one stable secret-free pre-mutation snapshot', async (t) => {
  const fixture = await makePrivateEvidenceFixture(t)
  const snapshot = await totalTestHooks.readPrivateEvidenceSnapshot(fixture.manifest)
  assert.deepEqual(Object.keys(snapshot).sort(),
    ['corpus', 'credentialCorpusId', 'identity', 'marker'].sort())
  assert.equal(JSON.stringify(snapshot).includes(fixture.secrets[0]), false)
  assert.equal(JSON.stringify(snapshot).includes(fixture.secrets[1]), false)
  await totalTestHooks.revalidatePrivateEvidenceSnapshot(snapshot, fixture.manifest)
})

test('private phase-E snapshot rejects content tamper without disclosing paths or secrets', async (t) => {
  const fixture = await makePrivateEvidenceFixture(t)
  const snapshot = await totalTestHooks.readPrivateEvidenceSnapshot(fixture.manifest)
  await chmod(fixture.corpusFile, 0o600)
  await writeFile(fixture.corpusFile, `${JSON.stringify({
    ...fixture.corpus, corpusId: `cor_${'x'.repeat(32)}`,
  }, null, 2)}\n`)
  await chmod(fixture.corpusFile, 0o400)
  const error = await rejectionOf(() =>
    totalTestHooks.revalidatePrivateEvidenceSnapshot(snapshot, fixture.manifest))
  assert.equal(error.message, 'private evidence snapshot is unavailable or drifted')
  assert.equal(error.message.includes(fixture.corpusFile), false)
  assert.equal(fixture.secrets.some((secret) => error.message.includes(secret)), false)
})

test('private phase-E snapshot rejects same-byte inode splice', async (t) => {
  const fixture = await makePrivateEvidenceFixture(t)
  const snapshot = await totalTestHooks.readPrivateEvidenceSnapshot(fixture.manifest)
  const replacement = join(fixture.privateRoot, 'corpus-replacement.json')
  await writeRawMode(replacement, await readFile(fixture.corpusFile), 0o400)
  await rename(replacement, fixture.corpusFile)
  await assert.rejects(
    totalTestHooks.revalidatePrivateEvidenceSnapshot(snapshot, fixture.manifest),
    /private evidence snapshot is unavailable or drifted/)
})

test('private phase-E preflight rejects mode drift, symlinks, and total identity mismatch', async (t) => {
  await t.test('mode', async (st) => {
    const fixture = await makePrivateEvidenceFixture(st)
    await chmod(fixture.markerFile, 0o600)
    await assert.rejects(totalTestHooks.readPrivateEvidenceSnapshot(fixture.manifest),
      /private evidence snapshot is unavailable or drifted/)
  })
  await t.test('symlink', async (st) => {
    const fixture = await makePrivateEvidenceFixture(st)
    const target = join(fixture.privateRoot, 'credential-corpus-target.json')
    await rename(fixture.corpusFile, target)
    await symlink(target, fixture.corpusFile)
    await assert.rejects(totalTestHooks.readPrivateEvidenceSnapshot(fixture.manifest),
      /private evidence snapshot is unavailable or drifted/)
  })
  await t.test('identity mismatch', async (st) => {
    const fixture = await makePrivateEvidenceFixture(st)
    const mismatched = { ...fixture.manifest, identity: { ...fixture.manifest.identity,
      generationId: `gen_${'z'.repeat(32)}` } }
    await assert.rejects(totalTestHooks.readPrivateEvidenceSnapshot(mismatched),
      /private evidence snapshot is unavailable or drifted/)
  })
})

test('Runner binding requires exact workspace/source-manifest/source-bytes/0400-artifact equality', async (t) => {
  const fixture = await makeRunnerBindingFixture(t)
  assert.deepEqual(await totalTestHooks.verifyRunnerSourceBinding(fixture.config, fixture.manifest),
    { size: fixture.runnerBytes.length, sha256: sha256(fixture.runnerBytes) })
})

test('Runner binding rejects manifest path splice, artifact mode drift, and Runner byte drift', async (t) => {
  await t.test('manifest path splice', async (st) => {
    const fixture = await makeRunnerBindingFixture(st, { pathSplice: true })
    await assert.rejects(totalTestHooks.verifyRunnerSourceBinding(fixture.config, fixture.manifest),
      /one unique exact entry/)
  })
  await t.test('artifact mode', async (st) => {
    const fixture = await makeRunnerBindingFixture(st)
    await chmod(fixture.artifactFile, 0o600)
    await assert.rejects(totalTestHooks.verifyRunnerSourceBinding(fixture.config, fixture.manifest),
      /exact owner-only immutable 0400/)
  })
  await t.test('source bytes drift', async (st) => {
    const fixture = await makeRunnerBindingFixture(st)
    await chmod(fixture.sourceRunnerFile, 0o600)
    await writeFile(fixture.sourceRunnerFile, Buffer.concat([fixture.runnerBytes, Buffer.from('\n// drift\n')]))
    await chmod(fixture.sourceRunnerFile, 0o644)
    await assert.rejects(totalTestHooks.verifyRunnerSourceBinding(fixture.config, fixture.manifest),
      /bounded owner regular file|Runner bytes drifted/)
  })
})

test('total source has one real serial manifest-bound command and fail-closed completion ordering', async () => {
  const source = await readFile(totalSourceFile, 'utf8')
  const candidateSource = await readFile(
    join(import.meta.dirname, 'o4-candidate-install-orchestrator.mjs'), 'utf8')
  const recoverySource = await readFile(
    join(import.meta.dirname, 'o4-recovery-residue-real.spec.ts'), 'utf8')
  assert.match(source, /usage: node e2e\/o4-total-real\.mjs --manifest <0400-file> --manifest-sha256 <digest>/)
  assert.match(source, /chora\.m1-o4-total-input\.v1/)
  assert.match(source, /acceptanceClass === 'real_acceptance'/)
  assert.ok(source.indexOf('let input = await preflight(cli)') <
    source.indexOf('await createFreshRoots(input)'))
  const preflightCall = source.indexOf('let input = await preflight(cli)')
  const privateRootBoundary = source.indexOf(
    'await revalidatePrivateEvidenceSnapshot(input.privateEvidenceSnapshot, input.manifest)',
    preflightCall)
  const rootCreationCall = source.indexOf('await createFreshRoots(input)', preflightCall)
  const privateBootstrapBoundary = source.indexOf(
    'await revalidatePrivateEvidenceSnapshot(input.privateEvidenceSnapshot, input.manifest)',
    rootCreationCall)
  const bootstrapCall = source.indexOf('await bootstrapFreshEngine(input)', rootCreationCall)
  const privateSessionBoundary = source.indexOf(
    'await revalidatePrivateEvidenceSnapshot(input.privateEvidenceSnapshot, input.manifest)',
    bootstrapCall)
  const sessionCall = source.indexOf('session = await createSessionAuthority(input)', bootstrapCall)
  const privateInstallBoundary = source.indexOf(
    'await revalidatePrivateEvidenceSnapshot(input.privateEvidenceSnapshot, input.manifest)',
    sessionCall)
  const candidateInstallCall = source.indexOf(
    'await input.candidateModule.executeCandidateInstall', rootCreationCall)
  assert.ok(preflightCall >= 0 && preflightCall < privateRootBoundary &&
    privateRootBoundary < rootCreationCall && rootCreationCall < privateBootstrapBoundary &&
    privateBootstrapBoundary < bootstrapCall && bootstrapCall < privateSessionBoundary &&
    privateSessionBoundary < sessionCall && sessionCall < privateInstallBoundary &&
    privateInstallBoundary < candidateInstallCall,
  'private fixed inputs must be revalidated before every root/bootstrap/session/install mutation boundary')
  const candidateInstallStart = candidateSource.indexOf(
    'export async function executeCandidateInstall')
  const candidateInstallEnd = candidateSource.indexOf(
    '\nexport async function restartCandidate', candidateInstallStart)
  assert.ok(candidateInstallStart >= 0 && candidateInstallEnd > candidateInstallStart)
  assert.match(candidateSource.slice(candidateInstallStart, candidateInstallEnd),
    /const setup = setupSpec\(config\)[\s\S]*?await executeJSON\(runner, setup, 'Setup'\)/)

  const manifestValidationStart = source.indexOf('function validateManifest(')
  const manifestValidationEnd = source.indexOf(
    '\nasync function assertAllPins(', manifestValidationStart)
  const manifestValidation = source.slice(manifestValidationStart, manifestValidationEnd)
  assert.match(manifestValidation,
    /exactKeys\(value\.repository, \['root', 'commit', 'closureSha256'\], 'repository authority'\)/)
  assert.match(manifestValidation, /\^\[0-9a-f\]\{40\}\$/)
  assert.match(manifestValidation,
    /exactKeys\(value\.roots, \['userRoot',[\s\S]*?'probeRuntimeRoot'[\s\S]*?\], 'fresh roots'\)/)

  const freshRootValidationStart = source.indexOf('async function validateFreshRoots(')
  const freshRootValidationEnd = source.indexOf(
    '\nfunction freshEnginePreMutationPaths(', freshRootValidationStart)
  const freshRootValidation = source.slice(freshRootValidationStart, freshRootValidationEnd)
  assert.match(freshRootValidation, /const roots = Object\.values\(manifest\.roots\)/)
  assert.match(freshRootValidation, /fresh total roots are not pairwise disjoint/)
  assert.match(freshRootValidation, /await absent\(root, 'fresh total root'\)/)
  assert.match(freshRootValidation, /await absent\(setup\.runtimeParent, 'fresh stage runtime parent'\)/)
  assert.match(freshRootValidation,
    /stageParentIdentity = await assertOwnerDirectory\([\s\S]*?dirname\(setup\.runtimeParent\), 'fresh stage parent'\)/)
  assert.match(freshRootValidation,
    /controlParentIdentity = await assertOwnerDirectory\(setup\.controlParent, 'fresh control parent'\)/)
  assert.match(freshRootValidation, /fresh stage-runtime root binding drifted/)
  assert.match(freshRootValidation, /fresh control root binding drifted/)

  const candidateRootBindingStart = source.indexOf('function bindCandidateRoots(')
  const candidateRootBindingEnd = source.indexOf(
    '\nasync function validateCandidateDynamicAbsence(', candidateRootBindingStart)
  assert.match(source.slice(candidateRootBindingStart, candidateRootBindingEnd),
    /probeRuntimeRoot: 'probeRuntimeRoot'/)

  const rootCreationStart = source.indexOf('async function createFreshRoots(')
  const rootCreationEnd = source.indexOf('\nfunction freshEngineEnvironment(', rootCreationStart)
  const rootCreation = source.slice(rootCreationStart, rootCreationEnd)
  assert.match(rootCreation, /for \(const key of driverCreatedRootKeys\)/)
  assert.match(rootCreation,
    /createOwnedDirectory\(\{ capability: input\.repositoryCapture,[\s\S]*?path: setup\.runtimeParent, parentIdentity: setup\.stageParentIdentity \}\)[\s\S]*?const root = input\.manifest\.roots\[key\][\s\S]*?createOwnedDirectory/)
  assert.match(rootCreation,
    /parentIdentity = dirname\(root\) === setup\.runtimeParent[\s\S]*?identities\.runtimeParent : setup\.controlParentIdentity/)
  assert.match(rootCreation, /parentIdentity: identities\.evidenceRoot/)
  assert.match(rootCreation, /root === setup\.runtimeParent \?[\s\S]*?'empty_directory'/)
  assert.match(source, /!engineMutationAttempted && freshRootIdentities !== undefined[\s\S]*?cleanupFreshRootSetup/)
  assert.match(source, /const repositoryCapture = Object\.freeze\(\{\s*executableFile: manifest\.serviceController\.executableFile,\s*executableSha256: manifest\.serviceController\.sha256,\s*\}\)/)
  assert.doesNotMatch(source, /CHORA_.*REPOSITORY_CAPTURE|process\.env\..*REPOSITORY_CAPTURE/)
  const pinValidation = source.indexOf('await assertAllPins(manifest)')
  const captureDerivation = source.indexOf('const repositoryCapture = Object.freeze({')
  const staticCaptureInspection = source.indexOf(
    'candidateModule.inspectStaticCandidate(candidateConfig, { repositoryCapture })')
  assert.ok(pinValidation >= 0 && pinValidation < captureDerivation &&
    captureDerivation < staticCaptureInspection,
  'repository capture must derive only from the already-pin-validated service controller')
  assert.match(source,
    /const staticInspection = await candidateModule\.inspectStaticCandidate\(candidateConfig, \{ repositoryCapture \}\)/)
  assert.ok(source.indexOf('await bootstrapFreshEngine(input)') <
    source.indexOf('await input.candidateModule.inspectAuthenticatedCandidate(input.candidateConfig, {'))
  assert.match(source,
    /inspectAuthenticatedCandidate\(input\.candidateConfig, \{\s*repositoryCapture: input\.repositoryCapture,\s*\}\)/)
  assert.match(source,
    /executeCandidateInstall\(input\.candidateConfig, \{\s*runner, o4EvidenceAuthority: evidenceAuthority,\s*repositoryCapture: input\.repositoryCapture,\s*\}\)/)
  assert.match(recoverySource,
    /const repositoryCapture = Object\.freeze\(\{\s*executableFile: env\.serviceControllerExecutable,\s*executableSha256: requiredDigest\(env\.serviceControllerSha256, 'service controller SHA-256'\),\s*\}\)/)
  assert.ok((recoverySource.match(/repositoryCapture/g)?.length ?? 0) >= 5)
  assert.match(source, /bindStaticCandidateSnapshot\(input\.staticInspection, inspected, input\.candidateConfig\)/)
  assert.match(source, /authenticated candidate snapshot is not the static snapshot plus exact v2 preflight/)
  assert.ok(source.indexOf('await input.candidateModule.executeCandidateInstall') < source.indexOf('for (let index = 0;'))
  assert.match(source, /const phaseOrder = Object\.freeze\(\['C', 'D', 'E'\]\)/)
  assert.match(source, /shell: false/)
  assert.match(source, /'--workers=1', '--retries=0'/)
  assert.match(source, /await revalidateBoundary\(input, 6\)/)
  assert.match(source, /await revalidateBoundary\(input, 7 \+ index\)/)
  const boundaryStart = source.indexOf('async function revalidateBoundary(')
  const boundaryEnd = source.indexOf('\nasync function postflight(', boundaryStart)
  const boundarySource = source.slice(boundaryStart, boundaryEnd)
  assert.match(boundarySource,
    /revalidatePrivateEvidenceSnapshot\(input\.privateEvidenceSnapshot, input\.manifest\)/)
  assert.match(boundarySource,
    /verifyRunnerSourceBinding\(input\.candidateConfig, input\.manifest\)/)
  const preflightStart = source.indexOf('async function preflight(')
  const preflightEnd = source.indexOf('\nfunction bindStaticCandidateSnapshot(', preflightStart)
  const preflightSource = source.slice(preflightStart, preflightEnd)
  assert.match(preflightSource, /verifyRunnerSourceBinding\(candidateConfig, manifest\)/)
  assert.match(preflightSource, /readPrivateEvidenceSnapshot\(manifest\)/)
  assert.ok(source.indexOf('await postflight(input, session)') <
    source.indexOf('await writeCompletion(input, session, engineResidue)'))
  assert.match(source, /writeReadonlyOwnedPath\(\{\s*capability: repositoryCapture, path, bytes,/)
  assert.equal(source.match(/await writeTotalStandaloneAuthority\(/g)?.length, 5)
  assert.doesNotMatch(source, /open\(path, 'wx'|handle\.chmod\(0o400\)|post-chmod|post-close/)
  assert.doesNotMatch(source, /exclusiveWriteDependencies|exclusive output and native cleanup failed/)
  assert.match(source, /const workspaceRoot = resolve\(dirname\(fileURLToPath\(import\.meta\.url\)\), '\.\.'\)/)
  assert.match(source, /executionModuleClosure/)
  assert.match(source, /'e2e\/o4-total-real\.mjs'/)
  assert.match(source, /'e2e\/o4-repository-authority\.mjs'/)
  assert.match(source, /'e2e\/o4-owned-path-cleanup\.mjs'/)
  assert.match(source, /verifyExecutionModuleClosure\(input\.candidateConfig\)/)
  assert.match(source.slice(candidateRootBindingStart, candidateRootBindingEnd),
    /config\.paths\.repositoryRoot === manifest\.repository\.root/)
  assert.match(source.slice(candidateRootBindingStart, candidateRootBindingEnd),
    /config\.authority\.repositoryClosureSha256 === manifest\.repository\.closureSha256/)
  assert.match(source, /const opened = await handle\.stat\(\)/)
  assert.match(source, /sameFileIdentity\(opened, afterRead\)/)
  assert.match(source, /sameFileIdentity\(opened, reopened\)/)
  assert.equal(source.match(/CHORA_O4_TOTAL_MANIFEST_SHA256/g)?.length, 1)
  assert.match(source, /CHORA_O4_TOTAL_MANIFEST_SHA256: input\.manifestSha256/)
  assert.match(source, /PLAYWRIGHT_BROWSERS_PATH: input\.manifest\.playwright\.browserRoot/)
  assert.match(source, /packageClosureSha256/)
  assert.match(source, /browserClosureSha256/)
  assert.match(source, /stableDirectoryClosure\(manifest\.playwright\.packageRoot/)
  assert.match(source, /stableDirectoryClosure\(manifest\.playwright\.browserRoot/)
  assert.match(source, /symlink \$\{relativePath\} escapes root/)
  assert.match(source, /chora\.m1-o4-engine-preflight\.v3/)
  assert.match(source, /buildFreshEnginePreflightAuthority/)
  assert.match(source, /limaInfoTemplates\.length === 120/)
  assert.match(source, /canonical Lima info authority drifted/)
  assert.match(source, /record\.limaInfoDigest === input\.manifest\.engine\.limaInfoDigest/)
  assert.match(source, /zeroMutation: true/)
  assert.match(source, /initialImageCount: 0/)
  assert.match(source, /initialImageInventorySha256/)
  assert.match(source, /await bootstrapFreshEngine\(input\)/)
  assert.ok(source.indexOf('let input = await preflight(cli)') <
    source.indexOf('await bootstrapFreshEngine(input)'))
  assert.match(source,
    /value\.engine\.limaFile === join\(value\.engine\.limaPrefixRoot, 'bin\/limactl'\)/)
  assert.match(source,
    /value\.engine\.limaWrapperFile === join\(value\.engine\.limaPrefixRoot, 'bin\/lima'\)/)
  assert.match(source,
    /value\.engine\.limaTemplatesRoot === join\(value\.engine\.limaPrefixRoot,\s*'share\/lima\/templates'\)/)
  assert.match(source, /lima-guestagent\.Linux-aarch64\.gz/)
  assert.match(source, /value\.engine\.limaGuestAgentSize === 7_251_420/)
  assert.match(source, /basename\(value\.engine\.dockerFile\) === 'docker'/)
  assert.match(source, /!overlaps\(value\.engine\.limaPrefixRoot, dirname\(value\.engine\.dockerFile\)\)/)
  assert.match(source, /prefixFiles\.length === 124 && prefixDirectories\.length === 7/)
  assert.match(source, /templateFiles\.length === 121 && templateDirectories\.length === 3/)
  assert.match(source, /limaTemplates\.totalBytes === 148_059/)
  assert.match(source, /async function preflight\([^)]*\) \{[\s\S]*?await assertAllPins\(/)
  const enginePath = source.indexOf(
    'const path = [...new Set([dirname(e.limaFile), dirname(e.dockerFile), dirname(e.colimaFile),')
  assert.ok(enginePath >= 0)
  assert.ok(enginePath < source.indexOf('PATH: path,', enginePath))
  const limaInfo = source.indexOf("const limaInfoArgv = Object.freeze(['info'])")
  assert.ok(limaInfo >= 0 && limaInfo < source.indexOf('const startArgv = freshEngineStartArgv(input)', limaInfo))
  assert.match(source, /limaInfo\.templates\.length === 120/)
  assert.match(source, /new Set\(limaInfo\.templates\.map\(\(entry\) => entry\.location\)\)\.size === 120/)
  assert.match(source, /writeBootstrapFailure\(input, 'lima_info'/)
  assert.match(source, /await cleanupFreshEngine\(input\)/)
  assert.match(source, /'--disk-image', e\.diskImageFile/)
  assert.doesNotMatch(source, /'--force-disk-image'/)
  assert.match(source, /'--mount', 'none'/)
  assert.match(source, /'--root-disk', '20'/)
  assert.match(source, /'--network-host-addresses=false'/)
  assert.match(source, /'--network-preferred-route=false'/)
  assert.match(source, /\['delete', input\.manifest\.engine\.profileName, '--force', '--data'\]/)
  assert.match(source, /DOCKER_CONFIG: input\.manifest\.roots\.dockerConfigRoot/)
  assert.match(source, /DOCKER_CONTEXT: input\.manifest\.engine\.contextName/)
  assert.match(source, /COLIMA_HOME: input\.manifest\.roots\.colimaHome/)
  assert.match(source, /LIMA_HOME: topology\.limaHome/)
  assert.match(source, /inherited DOCKER_HOST is forbidden/)
  assert.doesNotMatch(source, /\['context', 'show'\]/)
  assert.match(source, /\['context', 'inspect', '--format'/)
  assert.match(source, /\{"name":\{\{json \.Name\}\},"endpoint":\{\{json \.Endpoints\.docker\.Host\}\}\}/)
  assert.match(source, /remoteIPDeniedByOS: true/)
  assert.match(source, /writeBootstrapFailure/)
  assert.match(source, /chora\.m1-o4-engine-bootstrap-failure\.v3/)
  assert.match(source, /join\([^,\n]*evidenceRoot, 'engine-bootstrap-failure\.json'\)/)
  for (const field of ['classification', 'exitCode', 'signal', 'stdoutByteCount', 'stdoutSha256',
    'stderrByteCount', 'stderrSha256', 'launcherErrorSha256', 'argvSha256',
    'environmentSha256', 'limaPrefixClosureSha256', 'elapsedMs', 'digest']) {
    assert.match(source, new RegExp(`\\b${field}\\b`), `bootstrap failure evidence lacks ${field}`)
  }
  for (const classification of ['dependency_missing', 'portable_asset_missing', 'sandbox_denied',
    'network_unavailable_or_denied', 'host_operation_denied', 'disk_image_invalid',
    'lima_version_incompatible', 'vm_start_failed', 'timeout', 'child_exit', 'launcher_error',
    'engine_identity_unavailable', 'engine_identity_invalid']) {
    assert.match(source, new RegExp(`'${classification}'`))
  }
  const runnerSource = await readFile(runnerSourceFile, 'utf8')
  assert.match(runnerSource, /function runnerChildEnvironment\(binding\)/)
  assert.match(runnerSource, /DOCKER_CONFIG: roots\.dockerConfigRoot/)
  assert.match(runnerSource, /DOCKER_CONTEXT: engine\.contextName/)
  assert.match(runnerSource, /COLIMA_HOME: roots\.colimaHome/)
  assert.match(runnerSource, /environment: runnerChildEnvironment\(binding\)/)
  assert.doesNotMatch(source, /execSync|spawnSync|shell:\s*true/)
})

test('bootstrap child failures map to bounded allowlisted classes without retaining raw output', async () => {
  const source = await readFile(totalSourceFile, 'utf8')
  const start = source.indexOf('function classifyBootstrapFailure(')
  const end = source.indexOf('\nasync function verifyBootstrapSandboxSemantics(', start)
  assert.ok(start >= 0 && end > start)
  const classifierSource = source.slice(start, end)
  const classify = Function(`${classifierSource}; return classifyBootstrapFailure`)()
  const failure = (stderr, message = '') => classify('colima_start', {
    stderr: Buffer.from(stderr), code: 1, signal: null,
  }, message ? new Error(message) : undefined)
  assert.equal(failure('sandbox-exec: sandbox_apply: Operation not permitted'), 'sandbox_denied')
  assert.equal(failure('dial tcp: network is unreachable'), 'network_unavailable_or_denied')
  assert.equal(failure('error starting vm: operation not permitted'), 'host_operation_denied')
  assert.equal(failure('invalid disk image: SHA validation failed'), 'disk_image_invalid')
  assert.equal(failure('lima development version 2.0 is lower than compatible version 2.1'),
    'lima_version_incompatible')
  assert.equal(failure("error at 'creating and starting': failed"), 'vm_start_failed')
  assert.equal(failure('unrecognized bounded child output'), 'child_exit')
  assert.equal(classify('colima_start', null, new Error('bootstrap timed out')), 'timeout')
  const identityError = new Error('fresh Engine process identities unavailable')
  identityError.code = 'ENGINE_PROCESS_IDENTITIES_UNAVAILABLE'
  assert.equal(classify('engine_processes', null, identityError), 'engine_identity_unavailable')
  const writerStart = source.indexOf('async function writeBootstrapFailure(')
  const writerEnd = source.indexOf('\nfunction classifyBootstrapFailure(', writerStart)
  const writerSource = source.slice(writerStart, writerEnd)
  assert.doesNotMatch(writerSource, /\bstdout\s*:/)
  assert.doesNotMatch(writerSource, /\bstderr\s*:/)
  assert.match(writerSource, /stdoutByteCount:/)
  assert.match(writerSource, /stdoutSha256:/)
  assert.match(writerSource, /stderrByteCount:/)
  assert.match(writerSource, /stderrSha256:/)
})

test('fresh Engine context name is exactly derived from its Colima profile', () => {
  assert.doesNotThrow(() => totalTestHooks.validateFreshEngineContextName({
    profileName: 'chora-o4', contextName: 'colima-chora-o4',
  }))
  assert.throws(() => totalTestHooks.validateFreshEngineContextName({
    profileName: 'chora-o4', contextName: 'chora-o4',
  }), /exact Colima profile context/)
})

test('fresh Engine process identities use the exact Colima-backed Lima instance root', async (t) => {
  const root = await mkdtemp('/private/tmp/chora-o4-engine-process-root-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const child = await spawnLiveChild(t)

  const profileName = 'chora-o4-process-fixture'
  const contextName = `colima-${profileName}`
  const instanceRoot = join(root, '_lima', contextName)
  await mkdir(instanceRoot, { recursive: true, mode: 0o700 })
  for (const name of ['ha.pid', 'vz.pid']) {
    await writeFile(join(instanceRoot, name), `${child.pid}\n`, { mode: 0o600, flag: 'wx' })
  }

  const observed = await totalTestHooks.observeFreshEngineProcesses({
    manifest: {
      roots: { colimaHome: root },
      engine: { profileName, contextName },
    },
  }, true)
  assert.deepEqual(observed, [
    { role: 'host_agent', pid: child.pid, pidFileSha256: sha256(`${child.pid}\n`) },
    { role: 'vz_driver', pid: child.pid, pidFileSha256: sha256(`${child.pid}\n`) },
  ])
})

test('fresh Engine process identity observation waits only for bounded delayed PID publication', async (t) => {
  const root = await mkdtemp('/private/tmp/chora-o4-engine-process-delay-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const child = await spawnLiveChild(t)
  const profileName = 'chora-o4-delayed-process-fixture'
  const contextName = `colima-${profileName}`
  const instanceRoot = join(root, '_lima', contextName)
  await mkdir(instanceRoot, { recursive: true, mode: 0o700 })
  let clock = 0
  let publications = 0
  const observed = await totalTestHooks.observeFreshEngineProcesses({
    manifest: { roots: { colimaHome: root }, engine: { profileName, contextName } },
  }, true, {
    timeoutMs: 10,
    pollMs: 5,
    now: () => clock,
    sleep: async (ms) => {
      clock += ms
      if (publications++ === 0) {
        for (const name of ['ha.pid', 'vz.pid']) {
          await writeFile(join(instanceRoot, name), `${child.pid}\n`, { mode: 0o600, flag: 'wx' })
        }
      }
    },
  })
  assert.deepEqual(observed.map(({ role, pid }) => ({ role, pid })), [
    { role: 'host_agent', pid: child.pid },
    { role: 'vz_driver', pid: child.pid },
  ])
  assert.equal(publications, 1)
})

test('fresh Engine process identity rejects non-canonical PID bytes and writable PID files', async (t) => {
  for (const variant of ['non-canonical', 'writable']) {
    const root = await mkdtemp(`/private/tmp/chora-o4-engine-process-${variant}-`)
    t.after(() => rm(root, { recursive: true, force: true }))
    const child = await spawnLiveChild(t)
    const profileName = `chora-o4-${variant}-process-fixture`
    const contextName = `colima-${profileName}`
    const instanceRoot = join(root, '_lima', contextName)
    await mkdir(instanceRoot, { recursive: true, mode: 0o700 })
    for (const name of ['ha.pid', 'vz.pid']) {
      const value = variant === 'non-canonical' ? ` ${child.pid}\n` : `${child.pid}\n`
      const path = join(instanceRoot, name)
      await writeFile(path, value, { mode: 0o600, flag: 'wx' })
      if (variant === 'writable') await chmod(path, 0o622)
    }
    await assert.rejects(totalTestHooks.observeFreshEngineProcesses({
      manifest: { roots: { colimaHome: root }, engine: { profileName, contextName } },
    }, true), variant === 'non-canonical' ? /PID encoding is not canonical/ : /PID file is writable/)
  }
})

test('fresh Engine process identity timeout retains bounded role outcomes for diagnosis', async (t) => {
  const root = await mkdtemp('/private/tmp/chora-o4-engine-process-timeout-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const profileName = 'chora-o4-timeout-process-fixture'
  const contextName = `colima-${profileName}`
  await mkdir(join(root, '_lima', contextName), { recursive: true, mode: 0o700 })
  await assert.rejects(totalTestHooks.observeFreshEngineProcesses({
    manifest: { roots: { colimaHome: root }, engine: { profileName, contextName } },
  }, true, { timeoutMs: 0, pollMs: 1, now: () => 0, sleep: async () => {} }), (error) => {
    assert.equal(error.code, 'ENGINE_PROCESS_IDENTITIES_UNAVAILABLE')
    assert.deepEqual(error.engineProcessObservation.roles, [
      { role: 'host_agent', outcome: 'absent' },
      { role: 'vz_driver', outcome: 'absent' },
    ])
    assert.deepEqual(error.engineProcessObservation.partialProcesses, [])
    assert.equal(error.engineProcessObservation.attemptCount, 1)
    assert.match(error.engineProcessObservation.expectedInstanceRootSha256, /^[0-9a-f]{64}$/)
    return true
  })
})

test('missing exact Docker context fails the executable post-start result gate', () => {
  const result = { code: 1, signal: null, stdout: Buffer.alloc(0),
    stderr: Buffer.from('context not found') }
  assert.throws(() => totalTestHooks.requireSuccessfulFreshEngineResult(
    result, 'fresh Docker exact context inspection'),
  /fresh Docker exact context inspection failed/)
})

test('exact Docker context inspection rejects name and endpoint binding mismatches', () => {
  const endpointSocketPath = '/private/tmp/chora-o4-engine/docker.sock'
  const expectedEndpoint = `unix://${endpointSocketPath}`
  const engine = { contextName: 'colima-chora-o4', endpointSocketPath,
    endpointDigest: sha256(expectedEndpoint) }
  const parsed = totalTestHooks.parseFreshEngineContextInspection(Buffer.from(JSON.stringify({
    name: engine.contextName, endpoint: expectedEndpoint,
  })))
  assert.deepEqual(totalTestHooks.validateFreshEngineContextInspection(engine, parsed), parsed)
  assert.throws(() => totalTestHooks.validateFreshEngineContextInspection(engine,
    { ...parsed, name: 'colima-wrong-profile' }), /inspected context name drifted/)
  assert.throws(() => totalTestHooks.validateFreshEngineContextInspection(engine,
    { ...parsed, endpoint: 'unix:///private/tmp/wrong.sock' }),
  /does not bind the authorized Unix endpoint/)
})

test('exact Lima instance inspection binds the Colima context and in-process VZ driver', () => {
  const pid = 4242
  const engine = { profileName: 'chora-o4', contextName: 'colima-chora-o4' }
  const parsed = totalTestHooks.parseFreshEngineInstanceInspection(Buffer.from(JSON.stringify({
    name: engine.contextName,
    status: 'Running',
    vmType: 'vz',
    hostAgentPID: pid,
    driverPID: pid,
  })))
  const processes = [
    { role: 'host_agent', pid, pidFileSha256: sha256(`${pid}\n`) },
    { role: 'vz_driver', pid, pidFileSha256: sha256(`${pid}\n`) },
  ]
  assert.deepEqual(totalTestHooks.validateFreshEngineInstanceInspection(
    engine, parsed, processes), parsed)
  for (const drift of [
    { name: 'colima-other' },
    { status: 'Broken' },
    { vmType: 'qemu' },
    { hostAgentPID: pid + 1 },
    { driverPID: pid + 1 },
  ]) assert.throws(() => totalTestHooks.validateFreshEngineInstanceInspection(
    engine, { ...parsed, ...drift }, processes), /Lima instance identity drifted/)
  assert.throws(() => totalTestHooks.validateFreshEngineInstanceInspection(
    engine, parsed, processes.slice(0, 1)), /Lima instance process authority is incomplete/)
})

test('post-start bootstrap failure artifact persists only bounded hashes and counts', async (t) => {
  const root = await mkdtemp('/private/tmp/chora-o4-post-start-failure-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const controllerFixture = await buildTestRepositoryCapture()
  t.after(() => controllerFixture.cleanup())
  const evidenceRoot = join(root, 'evidence')
  await mkdir(evidenceRoot, { mode: 0o700 })
  const failureFile = join(evidenceRoot, 'engine-bootstrap-failure.json')
  const secret = 'synthetic-secret-must-not-be-retained'
  const stdout = Buffer.from(`daemon output ${secret}`)
  const stderr = Buffer.from(`context diagnostic ${secret}`)
  const input = { repositoryCapture: controllerFixture.capture, manifest: {
    identity: { generationId: `gen_${'a'.repeat(32)}` },
    engine: {
      profileName: 'chora-o4', contextName: 'colima-chora-o4',
      limaFile: join(root, 'bin', 'limactl'), dockerFile: join(root, 'bin', 'docker'),
      colimaFile: join(root, 'bin', 'colima'), limaPrefixClosureSha256: 'b'.repeat(64),
    },
    roots: {
      evidenceRoot, colimaHome: join(root, 'colima-home'),
      dockerConfigRoot: join(root, 'docker-config'), userRoot: join(root, 'user'),
      temporaryRoot: join(root, 'tmp'),
    },
  } }
  const result = { code: 17, signal: null, stdout, stderr, elapsedMs: 23 }
  await totalTestHooks.writeBootstrapFailure(input, 'docker_version', ['version'], result,
    new Error(`post-start ${secret}`), failureFile)
  const raw = await readFile(failureFile, 'utf8')
  const record = JSON.parse(raw)
  assert.equal(raw.includes(secret), false)
  assert.equal(record.stage, 'docker_version')
  assert.equal(record.exitCode, 17)
  assert.equal(record.stdoutByteCount, stdout.length)
  assert.equal(record.stdoutSha256, sha256(stdout))
  assert.equal(record.stderrByteCount, stderr.length)
  assert.equal(record.stderrSha256, sha256(stderr))
  assert.equal(record.launcherErrorSha256, sha256(`post-start ${secret}`))
  assert.equal(record.digest, sha256(canonical(Object.fromEntries(
    Object.entries(record).filter(([key]) => key !== 'digest')))))
  assert.equal((await lstat(failureFile)).mode & 0o777, 0o400)

  const identityFailureFile = join(evidenceRoot, 'engine-process-failure.json')
  const identityError = new Error('fresh Engine process identities unavailable')
  identityError.code = 'ENGINE_PROCESS_IDENTITIES_UNAVAILABLE'
  identityError.engineProcessObservation = {
    attemptCount: 201,
    expectedInstanceRootSha256: sha256(join(root, 'colima-home', '_lima', 'colima-chora-o4')),
    roles: [
      { role: 'host_agent', outcome: 'valid', unsafeDetail: secret },
      { role: 'vz_driver', outcome: 'absent' },
    ],
    partialProcesses: [
      { role: 'host_agent', pid: 4242, pidFileSha256: sha256('4242\n'), unsafeRaw: secret },
    ],
    unsafeExtra: secret,
  }
  await totalTestHooks.writeBootstrapFailure(input, 'engine_processes', [], null,
    identityError, identityFailureFile)
  const identityRecord = JSON.parse(await readFile(identityFailureFile, 'utf8'))
  assert.equal(identityRecord.classification, 'engine_identity_unavailable')
  assert.deepEqual(identityRecord.engineProcessObservation, {
    attemptCount: 201,
    expectedInstanceRootSha256: identityError.engineProcessObservation.expectedInstanceRootSha256,
    roles: [
      { role: 'host_agent', outcome: 'valid' },
      { role: 'vz_driver', outcome: 'absent' },
    ],
    partialProcesses: [
      { role: 'host_agent', pid: 4242, pidFileSha256: sha256('4242\n') },
    ],
  })
  assert.equal((await readFile(identityFailureFile, 'utf8')).includes(secret), false)
  assert.equal(identityRecord.digest, sha256(canonical(Object.fromEntries(
    Object.entries(identityRecord).filter(([key]) => key !== 'digest')))))
  assert.equal((await lstat(identityFailureFile)).mode & 0o777, 0o400)
})

test('all five standalone authorities use the exact native readonly protocol and remain byte-exact 0400 JSON', async (t) => {
  const root = await mkdtemp('/private/tmp/chora-o4-total-output-identity-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const evidenceRoot = join(root, 'evidence')
  await mkdir(evidenceRoot, { mode: 0o700 })
  const capability = await fakeReadonlyController(t)
  const calls = []
  const spawn = fakeRawSpawn(calls, async (stdin) => {
    const call = calls.length - 1
    const target = join(evidenceRoot, `${call}.json`)
    await writeFile(target, stdin, { flag: 'wx', mode: 0o400 })
    await chmod(target, 0o400)
    return { stdout: readonlyReceipt(target, stdin,
      ownedPathIdentity(await lstat(target, { bigint: true }))) }
  })
  const schemas = [
    'chora.m1-o4-engine-preflight.v3',
    'chora.m1-o4-engine-bootstrap-failure.v3',
    'chora.m1-o4-fresh-engine-residue.v1',
    'chora.m1-o4-runner-session-authority.v1',
    'chora.m1-o4-total-completion.v1',
  ]
  for (let index = 0; index < schemas.length; index += 1) {
    const target = join(evidenceRoot, `${index}.json`)
    const bytes = Buffer.from(`${JSON.stringify({ schemaVersion: schemas[index], status: 'passed' }, null, 2)}\n`)
    const stages = []
    const receipt = await totalTestHooks.writeTotalStandaloneAuthority(
      bytes, target, capability, {
        spawn,
        beforeSpawn: ({ protocol }) => stages.push(protocol),
        afterNativeReceipt: ({ receipt: observed }) => stages.push(observed.schemaVersion),
      })
    assert.equal(receipt.target, target)
    assert.deepEqual(stages, [
      'chora.m1-o4-owned-readonly-write.v1',
      'chora.m1-o4-owned-readonly-write-receipt.v1',
    ])
    assert.deepEqual(await readFile(target), bytes)
    assert.equal(JSON.parse(await readFile(target, 'utf8')).schemaVersion, schemas[index])
    assert.equal((await lstat(target)).mode & 0o777, 0o400)
  }
  assert.equal(calls.length, 5)
  for (const [index, call] of calls.entries()) {
    const target = join(evidenceRoot, `${index}.json`)
    assert.deepEqual(call.argv, [
      '--protocol', 'chora.m1-o4-owned-readonly-write.v1',
      '--controller-sha256', capability.executableSha256,
      '--target', target,
      '--expected-sha256', sha256(call.stdin),
      '--expected-byte-length', String(call.stdin.length),
      '--network', 'disabled',
      '--input', 'stdin',
    ])
    assert.equal(call.options.shell, false)
  }
})

test('standalone authority failures preserve existing, malformed-receipt, and foreign files without cleanup', async (t) => {
  const root = await mkdtemp('/private/tmp/chora-o4-total-output-fail-closed-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const capability = await fakeReadonlyController(t)
  const oldFile = join(root, 'existing.json')
  const oldBytes = Buffer.from('{"old":true}\n')
  await writeFile(oldFile, oldBytes, { flag: 'wx', mode: 0o400 })
  await chmod(oldFile, 0o400)
  let spawned = false
  await assert.rejects(totalTestHooks.writeTotalStandaloneAuthority(
    Buffer.from('{"new":true}\n'), oldFile, capability, {
      spawn: () => { spawned = true; throw new Error('must not spawn') },
    }), /not absent/)
  assert.equal(spawned, false)
  assert.deepEqual(await readFile(oldFile), oldBytes)

  const malformedFile = join(root, 'malformed.json')
  const malformedBytes = Buffer.from('{"retained":true}\n')
  await assert.rejects(totalTestHooks.writeTotalStandaloneAuthority(
    malformedBytes, malformedFile, capability, {
      spawn: fakeRawSpawn([], async (stdin) => {
        await writeFile(malformedFile, stdin, { flag: 'wx', mode: 0o400 })
        await chmod(malformedFile, 0o400)
        return { stdout: '{]\n' }
      }),
    }), /receipt.*JSON/)
  assert.deepEqual(await readFile(malformedFile), malformedBytes)

  const badReceiptFile = join(root, 'bad-receipt.json')
  const badReceiptBytes = Buffer.from('{"retained":"bad-receipt"}\n')
  await assert.rejects(totalTestHooks.writeTotalStandaloneAuthority(
    badReceiptBytes, badReceiptFile, capability, {
      spawn: fakeRawSpawn([], async (stdin) => {
        await writeFile(badReceiptFile, stdin, { flag: 'wx', mode: 0o400 })
        await chmod(badReceiptFile, 0o400)
        const identity = ownedPathIdentity(await lstat(badReceiptFile, { bigint: true }))
        return { stdout: readonlyReceipt(badReceiptFile, stdin,
          { ...identity, ino: `${identity.ino}1` }) }
      }),
    }), /target identity drifted/)
  assert.deepEqual(await readFile(badReceiptFile), badReceiptBytes)

  const swappedFile = join(root, 'post-controller-swap.json')
  const displacedFile = join(root, 'post-controller-owned.json')
  const foreignBytes = Buffer.from('foreign total output\n')
  const ownedBytes = Buffer.from('{"owned":true}\n')
  const calls = []
  await assert.rejects(totalTestHooks.writeTotalStandaloneAuthority(
    ownedBytes, swappedFile, capability, {
      spawn: fakeRawSpawn(calls, async (stdin) => {
        await writeFile(swappedFile, stdin, { flag: 'wx', mode: 0o400 })
        await chmod(swappedFile, 0o400)
        return { stdout: readonlyReceipt(swappedFile, stdin,
          ownedPathIdentity(await lstat(swappedFile, { bigint: true }))) }
      }),
      afterNativeReceipt: async ({ path }) => {
        assert.equal(path, swappedFile)
        await rename(path, displacedFile)
        await writeFile(path, foreignBytes, { flag: 'wx', mode: 0o400 })
        await chmod(path, 0o400)
      },
    }), /no longer names the native receipt inode/)
  assert.equal(calls.length, 1, 'foreign swap must not invoke a cleanup protocol')
  assert.deepEqual(await readFile(swappedFile), foreignBytes)
  assert.deepEqual(await readFile(displacedFile), ownedBytes)
})

test('all three real public journeys reopen one total manifest, session, tuple, and Runner pin', async () => {
  for (const name of [
    'o4-profile-reuse-real.spec.ts', 'o4-recovery-residue-real.spec.ts',
    'o4-final-evidence-real.spec.ts',
  ]) {
    const source = await readFile(join(import.meta.dirname, name), 'utf8')
    for (const marker of [
      'CHORA_O4_TOTAL_MANIFEST_FILE', 'CHORA_O4_TOTAL_MANIFEST_SHA256',
      'CHORA_O4_TOTAL_SESSION_AUTHORITY_FILE', 'CHORA_O4_TOTAL_TUPLE_IDENTITY',
      'CHORA_O4_TOTAL_RUNNER_SHA256', 'assertTotalSessionBinding',
    ]) assert.match(source, new RegExp(marker), `${name} lacks ${marker}`)
  }
})

test('real total driver is absent from default npm scripts and synthetic tests never spawn it', async () => {
  const packageJSON = JSON.parse(await readFile(join(workspace, 'package.json'), 'utf8'))
  assert.equal(Object.values(packageJSON.scripts).some((value) => String(value).includes('o4-total-real.mjs')), false)
  const self = await readFile(import.meta.filename, 'utf8')
  assert.doesNotMatch(self, /spawn\([^\n]*o4-total-real|execFile\([^\n]*o4-total-real/)
})

test('standard Playwright discovery excludes Node TAP suites from the browser runner', async () => {
  const config = await readFile(join(workspace, 'playwright.config.ts'), 'utf8')
  assert.match(config, /testMatch:\s*['"]\*\*\/\*\.spec\.ts['"]/)
})

async function makePrivateEvidenceFixture(t) {
  const root = await mkdtemp('/private/tmp/chora-o4-total-private-input-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const privateRoot = join(root, 'private')
  await mkdir(privateRoot, { mode: 0o700 })
  const markerNonce = randomBytes(32).toString('base64url')
  const identity = {
    environmentId: `env_${sha256(`chora:m1-o4:environment:${markerNonce}`).slice(0, 48)}`,
    installId: `ins_${randomBytes(24).toString('base64url')}`,
    generationId: `gen_${randomBytes(24).toString('base64url')}`,
    platform: { os: 'darwin', architecture: 'arm64' },
  }
  const corpusId = `cor_${randomBytes(24).toString('base64url')}`
  // The pinned OAuth access token used by the local offline acceptance input is
  // currently 1,788 bytes; keep this fixture at that exact realistic boundary.
  const secrets = [`access_${'a'.repeat(1781)}`,
    `refresh_${randomBytes(32).toString('base64url')}`]
  const corpus = { schemaVersion: 'chora.m1-o4-private-credential-corpus.v1', corpusId,
    secrets: [{ name: 'openai_codex_access', value: secrets[0] },
      { name: 'openai_codex_refresh', value: secrets[1] }] }
  const corpusFile = join(privateRoot, 'credential-corpus.json')
  await writeJSONMode(corpusFile, corpus, 0o400)
  const markerFile = join(privateRoot, 'private-environment-marker.json')
  await writeJSONMode(markerFile, {
    schemaVersion: 'chora.m1-o4-private-environment-marker.v1', markerNonce,
    environmentId: identity.environmentId, installId: identity.installId,
    generationId: identity.generationId, credentialCorpusId: corpusId,
    credentialCorpusSha256: sha256(await readFile(corpusFile)),
  }, 0o400)
  const manifest = { identity, phases: { E: { environment: {
    CHORA_O4_FINAL_PRIVATE_MARKER_FILE: markerFile,
    CHORA_O4_FINAL_CREDENTIAL_CORPUS_FILE: corpusFile,
  } } } }
  return { root, privateRoot, markerFile, corpusFile, identity, corpus, secrets, manifest }
}

async function makeRunnerBindingFixture(t, options = {}) {
  const root = await mkdtemp('/private/tmp/chora-o4-total-runner-binding-')
  t.after(() => rm(root, { recursive: true, force: true }))
  const bundleRoot = join(root, 'bundle')
  const sourceE2E = join(bundleRoot, 'source', 'e2e')
  await mkdir(sourceE2E, { recursive: true, mode: 0o700 })
  const runnerBytes = await readFile(runnerSourceFile)
  const runnerEntry = { path: 'e2e/o4-b2-runner.mjs', size: runnerBytes.length,
    mode: '0644', sha256: sha256(runnerBytes) }
  const files = [runnerEntry]
  if (options.pathSplice) files.push({ ...runnerEntry,
    path: 'splice/e2e/o4-b2-runner.mjs' })
  const sourceManifestFile = join(bundleRoot, 'source-manifest.json')
  await writeJSONMode(sourceManifestFile, { files }, 0o444)
  const sourceRunnerFile = join(sourceE2E, 'o4-b2-runner.mjs')
  await writeRawMode(sourceRunnerFile, runnerBytes, 0o644)
  const artifactFile = join(root, 'o4-b2-runner-artifact.mjs')
  await writeRawMode(artifactFile, runnerBytes, 0o400)
  const config = { paths: { sourceManifestFile, sourceBundleRoot: bundleRoot },
    authority: { sourceManifestSha256: sha256(await readFile(sourceManifestFile)) } }
  const manifest = { runner: { moduleFile: artifactFile, moduleSha256: sha256(runnerBytes) } }
  return { root, bundleRoot, sourceManifestFile, sourceRunnerFile, artifactFile,
    runnerBytes, config, manifest }
}

async function writeJSONMode(path, value, mode) {
  await writeRawMode(path, Buffer.from(`${JSON.stringify(value, null, 2)}\n`), mode)
}

async function writeRawMode(path, bytes, mode) {
  await writeFile(path, bytes, { flag: 'wx', mode: 0o600 })
  await chmod(path, mode)
}

async function rejectionOf(operation) {
  try { await operation() } catch (error) { return error }
  throw new Error('operation unexpectedly succeeded')
}

async function write0400(path, value) {
  await writeFile(path, `${JSON.stringify(value, null, 2)}\n`, { flag: 'wx', mode: 0o600 })
  await chmod(path, 0o400)
}

function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function canonical(value) {
  if (value === null || typeof value !== 'object') return JSON.stringify(value)
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`
}
