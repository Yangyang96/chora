import { createHash, createHmac, randomBytes } from 'node:crypto'
import { spawn } from 'node:child_process'
import {
  chmod, copyFile, cp, lstat, mkdir, open, readFile, stat,
} from 'node:fs/promises'
import { basename, dirname, isAbsolute, join, normalize, relative, resolve, sep } from 'node:path'
import { request as httpRequest } from 'node:http'
import { createServer, createConnection } from 'node:net'
import { fileURLToPath } from 'node:url'

import {
  cleanupOwnedPath, ownedPathIdentity, replaceOwnedPath, validateOwnedPathCapability,
} from './o4-owned-path-cleanup.mjs'

const TOTAL_SCHEMA = 'chora.m1-o4-total-input.v1'
const SESSION_SCHEMA = 'chora.m1-o4-runner-session-authority.v1'
const REQUEST_SCHEMA = 'chora.m1-o4-runner-control-request.v1'
const RESPONSE_SCHEMA = 'chora.m1-o4-runner-control-response.v1'
const SOURCE_SCHEMA = 'chora.local-alpha-source-bundle.v1'
const INSTALLATION_SCHEMA = 'chora.local-alpha-installation.v1'
const DATA_INSTALLATION_SCHEMA = 'chora.local-alpha-data.v1'
const MAX_JSON = 8 * 1024 * 1024
const MAX_SOURCE_MANIFEST = 16 * 1024 * 1024
const MAX_OUTPUT = 64 * 1024
const digestRE = /^[0-9a-f]{64}$/
const views = Object.freeze(['install', 'D', 'E'])
const viewMethods = Object.freeze({
  install: Object.freeze(['exec', 'installImmutable', 'processBoundary', 'start']),
  D: Object.freeze([
    'exec', 'installImmutable', 'processBoundary', 'start',
    'armAuthenticatedControllerRecovery', 'awaitAuthenticatedControllerProof',
    'exportApplyDisclosureSources', 'abortApplyDisclosureExporter',
  ]),
  E: Object.freeze(['closeAllServices', 'sealA3Ledger']),
})

let parentSupervisor
let syntheticPinHooksEnabled = false

/**
 * The sole public export of the pinned O4 B2 Runner. `install` is available
 * only to the manifest parent PID and owns the control socket. D and E are
 * least-capability authenticated proxy views over that same parent registry.
 */
export async function createO4B2Runner(options) {
  const testTransport = options?.inProcessTestTransport === true
  exactKeys(options, testTransport ? ['view', 'manifestFile', 'manifestSha256',
    'sessionAuthorityFile', 'inProcessTestTransport'] :
    ['view', 'manifestFile', 'manifestSha256', 'sessionAuthorityFile'], 'B2 Runner options')
  assert(views.includes(options.view), 'B2 Runner view must be exactly install, D, or E')
  const binding = await authenticateBinding(options)
  if (testTransport) assert(binding.manifest.acceptanceClass === 'synthetic_non_acceptance',
    'in-process Runner transport is synthetic non-acceptance only')
  if (testTransport) syntheticPinHooksEnabled = true
  if (options.view === 'install') {
    assert(binding.authority.parentPid === process.pid,
      'install Runner view is restricted to the total-entrypoint parent PID')
    assert(parentSupervisor === undefined, 'install Runner parent may be created only once')
    parentSupervisor = await ParentSupervisor.create(binding, testTransport)
    return exactView('install', parentSupervisor.installView())
  }
  if (testTransport) {
    assert(parentSupervisor && parentSupervisor.inProcessTestTransport &&
      parentSupervisor.binding.authority.authorityDigest === binding.authority.authorityDigest,
    'synthetic in-process Runner parent/session is unavailable')
    return exactView(options.view, parentSupervisor.directView(options.view))
  }
  return exactView(options.view, proxyView(binding, options.view))
}

class ParentSupervisor {
  static async create(binding, inProcessTestTransport) {
    const value = new ParentSupervisor(binding, inProcessTestTransport)
    if (!inProcessTestTransport) await value.openControlPlane()
    return value
  }

  constructor(binding, inProcessTestTransport) {
    this.binding = binding
    this.inProcessTestTransport = inProcessTestTransport
    this.server = undefined
    this.controlSocketIdentity = undefined
    this.services = []
    this.serviceHistory = []
    this.exporters = new Map()
    this.sequence = { D: 0, E: 0 }
    this.controllerArmed = false
    this.controllerAdopted = false
    this.controllerArm = null
    this.controllerBoundaryReached = false
    this.pendingController = null
    this.controllerCoreProof = null
    this.controllerCoreProofIdentity = undefined
    this.controllerTermination = null
    this.controllerProof = null
    this.closeAfterResponse = false
    this.closed = false
  }

  installView() {
    return {
      exec: (spec) => this.exec(spec),
      installImmutable: (spec) => this.installImmutable(spec),
      processBoundary: () => this.processBoundary(),
      start: (spec) => this.start(spec),
    }
  }

  directView(view) {
    return Object.fromEntries(viewMethods[view].map((method) => [method, async (...args) => {
      const result = await this[method](...args)
      if (method === 'sealA3Ledger') await this.closeControlPlane()
      return result
    }]))
  }

  async openControlPlane() {
    const socketPath = this.binding.manifest.runner.controlSocketPath
    await assertOwnerDirectory(dirname(socketPath))
    await absent(socketPath, 'Runner control socket')
    this.server = createServer((socket) => this.accept(socket))
    this.server.on('error', () => {})
    await new Promise((resolvePromise, reject) => {
      const onError = (error) => reject(error)
      this.server.once('error', onError)
      this.server.listen(socketPath, () => {
        this.server.off('error', onError)
        resolvePromise()
      })
    })
    await chmod(socketPath, 0o600)
    const info = await lstat(socketPath, { bigint: true })
    assert(info.isSocket() && !info.isSymbolicLink() && info.uid === BigInt(currentUID()) &&
      Number(info.mode & 0o777n) === 0o600, 'Runner control socket is not owner-only')
    this.controlSocketIdentity = ownedPathIdentity(info)
  }

  accept(socket) {
    let bytes = Buffer.alloc(0)
    socket.on('data', (chunk) => {
      bytes = Buffer.concat([bytes, chunk])
      if (bytes.length > MAX_JSON) socket.destroy(new Error('Runner request is oversized'))
      const newline = bytes.indexOf(10)
      if (newline < 0) return
      socket.pause()
      const frame = bytes.subarray(0, newline)
      this.handleFrame(frame).then((result) => {
        socket.end(`${JSON.stringify({ schemaVersion: RESPONSE_SCHEMA, ok: true,
          result: encode(result) })}\n`, () => {
          if (this.closeAfterResponse) this.closeControlPlane().catch(() => {})
        })
      }, (error) => {
        socket.end(`${JSON.stringify({ schemaVersion: RESPONSE_SCHEMA, ok: false,
          error: String(error?.message ?? 'Runner request failed') })}\n`)
      })
    })
  }

  async handleFrame(frame) {
    const request = parseJSON(frame, 'Runner control request')
    exactKeys(request, ['schemaVersion', 'view', 'sequence', 'method', 'args', 'sessionDigest', 'mac'],
      'Runner control request')
    assert(request.schemaVersion === REQUEST_SCHEMA && ['D', 'E'].includes(request.view),
      'Runner request schema/view drifted')
    assert(viewMethods[request.view].includes(request.method), 'Runner method is outside the selected view')
    assert(Number.isSafeInteger(request.sequence) && request.sequence === this.sequence[request.view] + 1,
      'Runner request sequence is not the exact monotonic successor')
    assert(request.sessionDigest === this.binding.authority.authorityDigest,
      'Runner request session drifted')
    const { mac, ...safe } = request
    assert(mac === hmac(this.binding.authority.sessionSecret, safe),
      'Runner request authentication failed')
    this.sequence[request.view] = request.sequence
    const args = decode(request.args)
    assert(Array.isArray(args), 'Runner method arguments are invalid')
    switch (request.method) {
      case 'exec': return this.exec(...args)
      case 'installImmutable': return this.installImmutable(...args)
      case 'processBoundary': return this.processBoundary(...args)
      case 'start': return this.start(...args)
      case 'armAuthenticatedControllerRecovery': return this.armAuthenticatedControllerRecovery(...args)
      case 'awaitAuthenticatedControllerProof': return this.awaitAuthenticatedControllerProof(...args)
      case 'exportApplyDisclosureSources': return this.exportApplyDisclosureSources(...args)
      case 'abortApplyDisclosureExporter': return this.abortApplyDisclosureExporter(...args)
      case 'closeAllServices': return this.closeAllServices(...args)
      case 'sealA3Ledger': return this.sealA3Ledger(...args)
      default: throw new Error('Runner method dispatch is unavailable')
    }
  }

  async exec(spec) {
    assertCommandSpec(spec, 'Runner exec')
    return runBounded(spec.file, spec.argv, spec.maxOutputBytes ?? MAX_OUTPUT,
      runnerChildEnvironment(this.binding))
  }

  async installImmutable(spec) {
    exactKeys(spec, ['sourceBundleRoot', 'sourceManifestFile', 'sourceManifestSha256',
      'sourceAggregateSha256', 'sourceFiles', 'sourceBinary', 'sourceWebDirectory',
      'installRoot', 'installedBinaryFile', 'installedWebDirectory', 'dataRoot', 'shell'],
    'immutable install')
    assert(spec.shell === false, 'immutable install shell must be false')
    for (const key of ['sourceBundleRoot', 'sourceManifestFile', 'sourceBinary',
      'sourceWebDirectory', 'installRoot', 'installedBinaryFile', 'installedWebDirectory',
      'dataRoot']) exactAbsolute(spec[key], `immutable install ${key}`)
    digest(spec.sourceManifestSha256, 'immutable install source manifest digest')
    digest(spec.sourceAggregateSha256, 'immutable install source aggregate')
    assert(Number.isSafeInteger(spec.sourceFiles) && spec.sourceFiles > 0 && spec.sourceFiles <= 10_000,
      'immutable install source file count is invalid')

    const sourceDirectory = join(spec.sourceBundleRoot, 'source')
    const installedSourceDirectory = join(spec.installRoot, 'source')
    const installedSourceManifestFile = join(spec.installRoot, 'source-manifest.json')
    const installationFile = join(spec.installRoot, 'installation.json')
    const dataInstallationFile = join(spec.dataRoot, 'data-installation.json')
    assert(spec.sourceManifestFile === join(spec.sourceBundleRoot, 'source-manifest.json'),
      'immutable install source manifest topology drifted')
    assert(!pathsOverlap(spec.installRoot, spec.dataRoot),
      'immutable install and data roots must be disjoint')
    for (const root of [spec.installRoot, spec.dataRoot]) {
      for (const source of [spec.sourceBundleRoot, spec.sourceBinary, spec.sourceWebDirectory]) {
        assert(!pathsOverlap(root, source), 'immutable install source and target roots overlap')
      }
    }
    assert(pathWithin(spec.installRoot, spec.installedBinaryFile) &&
      pathWithin(spec.installRoot, spec.installedWebDirectory),
    'immutable install targets escape the install root')
    for (const target of [spec.installedBinaryFile, spec.installedWebDirectory]) {
      for (const reserved of [installedSourceDirectory, installedSourceManifestFile, installationFile]) {
        assert(!pathsOverlap(target, reserved), 'immutable install target overlaps installed source identity')
      }
    }
    assert(!pathsOverlap(spec.installedBinaryFile, spec.installedWebDirectory),
      'immutable binary and web targets overlap')
    for (const path of [spec.installRoot, spec.dataRoot, spec.installedBinaryFile,
      spec.installedWebDirectory, sourceDirectory, spec.sourceManifestFile]) {
      await assertNoSymlinkAncestors(path)
    }
    await absent(spec.installRoot, 'fresh install root')
    await absent(spec.dataRoot, 'fresh data root')

    const sourceManifestPin = await stableOwnerFile(spec.sourceManifestFile,
      'immutable source manifest', MAX_SOURCE_MANIFEST, 0o444)
    assert(sha256(sourceManifestPin.bytes) === spec.sourceManifestSha256,
      'immutable source manifest digest drifted')
    const sourceManifest = parseJSON(sourceManifestPin.bytes, 'immutable source manifest')
    exactKeys(sourceManifest, ['schema_version', 'aggregate_sha256', 'source_path_policy',
      'install_only_projection', 'model_readable_projection', 'files'], 'immutable source manifest')
    assert(sourceManifest.schema_version === SOURCE_SCHEMA &&
      sourceManifest.aggregate_sha256 === spec.sourceAggregateSha256 &&
      Array.isArray(sourceManifest.files) && sourceManifest.files.length === spec.sourceFiles,
    'immutable source manifest identity drifted')

    let installIdentity
    let dataIdentity
    try {
      await mkdir(spec.installRoot, { mode: 0o700 })
      installIdentity = await ownedRootIdentity(spec.installRoot, 'installed product root')
      await mkdir(dirname(spec.installedBinaryFile), { recursive: true, mode: 0o700 })
      await copyFile(spec.sourceBinary, spec.installedBinaryFile)
      await chmod(spec.installedBinaryFile, (await stat(spec.sourceBinary)).mode & 0o777)
      await cp(spec.sourceWebDirectory, spec.installedWebDirectory, {
        recursive: true, errorOnExist: true, force: false, verbatimSymlinks: true,
      })
      await cp(sourceDirectory, installedSourceDirectory, {
        recursive: true, errorOnExist: true, force: false, verbatimSymlinks: true,
      })
      await chmod(installedSourceDirectory, 0o755)
      await copyFile(spec.sourceManifestFile, installedSourceManifestFile)
      await chmod(installedSourceManifestFile, 0o444)
      await writeExclusive(Buffer.from(`${JSON.stringify({
        schema_version: INSTALLATION_SCHEMA,
        aggregate_sha256: spec.sourceAggregateSha256,
        files: spec.sourceFiles,
      }, null, 2)}\n`), installationFile, 0o444, this.binding.ownedPathCapability)
      await verifyInstalledIdentity({
        installRoot: spec.installRoot,
        installedSourceDirectory,
        installedSourceManifestFile,
        installationFile,
        sourceManifestBytes: sourceManifestPin.bytes,
        sourceAggregateSha256: spec.sourceAggregateSha256,
        sourceFiles: spec.sourceFiles,
      })
      await syncDirectory(spec.installRoot)

      await mkdir(spec.dataRoot, { mode: 0o700 })
      dataIdentity = await ownedRootIdentity(spec.dataRoot, 'installed data root')
      await writeExclusive(Buffer.from(`${JSON.stringify({
        schema_version: DATA_INSTALLATION_SCHEMA,
        aggregate_sha256: spec.sourceAggregateSha256,
        install_root: spec.installRoot,
      }, null, 2)}\n`), dataInstallationFile, 0o444, this.binding.ownedPathCapability)
      await verifyDataIdentity(dataInstallationFile, spec)
      await syncDirectory(spec.dataRoot)
    } catch (error) {
      const cleanupErrors = []
      for (const [path, identity, label] of [
        [spec.dataRoot, dataIdentity, 'installed data root'],
        [spec.installRoot, installIdentity, 'installed product root'],
      ]) {
        if (!identity) continue
        try { await removeCreatedRoot(path, identity, label, this.binding.ownedPathCapability) } catch (cleanupError) {
          cleanupErrors.push(cleanupError)
        }
      }
      if (cleanupErrors.length > 0) {
        throw new AggregateError([error, ...cleanupErrors],
          'immutable install operation and exact-root cleanup failed')
      }
      throw error
    }
  }

  async processBoundary() {
    assert(!this.closed, 'Runner supervisor is already closed')
    if (!this.controllerArmed) {
      await this.stopServiceGroups()
      return undefined
    }
    assert(!this.controllerAdopted && this.pendingController === null && !this.controllerBoundaryReached,
      'authenticated orphan controller may be adopted exactly once')
    const controller = this.binding.manifest.serviceController
    await validate0500Executable(controller.executableFile, controller.sha256, 'service controller')
    await absent(controllerCoreProofFile(this.binding.manifest), 'service controller core proof')
    await absent(controllerProofFile(this.binding.manifest), 'service controller final proof')
    this.controllerBoundaryReached = true
  }

  async start(spec) {
    assertCommandSpec(spec, 'Runner start')
    assert(!this.closed, 'Runner supervisor is already closed')
    await validate0500Executable(spec.file, undefined, 'installed service executable')
    const serve = validateServeBinding(spec, this.binding)
    if (this.controllerBoundaryReached) {
      assert(this.controllerArmed && this.pendingController === null && !this.controllerAdopted &&
        this.controllerCoreProof === null && this.controllerProof === null,
        'authenticated controller replacement cannot be adopted twice')
      assert(this.services.length === 1, 'authenticated controller requires the exact old service')
      const oldService = this.services[0]
      assert(oldService.child, 'authenticated controller old service is not parent-observable')
      const controller = this.binding.manifest.serviceController
      const outputFile = controllerCoreProofFile(this.binding.manifest)
      const argv = serviceControllerArgv(controller.requestFile, outputFile)
      const runtimeBinding = serviceControllerRuntimeBinding(this.binding, oldService, spec, serve)
      const runtimeBytes = Buffer.from(canonicalJSONStringify(runtimeBinding))
      const oldExit = waitExit(oldService.child, this.binding.manifest.timeouts.controllerMs)
      const child = spawn(controller.executableFile, argv, {
        cwd: '/', shell: false, detached: true, stdio: ['pipe', 'pipe', 'pipe'],
        env: runnerChildEnvironment(this.binding),
      })
      await waitSpawn(child, 'authenticated orphan controller')
      const pending = { child, pid: child.pid, argv, outputFile, runtimeBytes,
        stdout: Buffer.alloc(0), stderr: Buffer.alloc(0), overflow: false }
      this.pendingController = pending
      captureBounded(pending, MAX_OUTPUT)
      child.stdin.end(runtimeBytes)
      let validatedReplacementPID = null
      try {
        const capturedProof = await waitForControllerProof(pending.outputFile,
          this.binding.manifest.timeouts.controllerMs)
        const proof = capturedProof.value
        pending.outputIdentity = capturedProof.identity
        validateServiceControlCoreProof(proof, this.binding, pending.argv, runtimeBinding,
          runtimeBytes, this.controllerArm, pending)
        validatedReplacementPID = proof.newService.pid
        const exit = await waitExit(pending.child, this.binding.manifest.timeouts.controllerMs)
        assert(!pending.overflow && exit.code === 0 && exit.signal === null &&
          pending.stdout.length === 0 && pending.stderr.length === 0,
        'authenticated controller did not complete its silent exit-zero protocol')
        await waitForProcessGroupDeath(pending.pid,
          this.binding.manifest.timeouts.closeMs, 'authenticated controller')
        const oldObserved = await oldExit
        assert(oldObserved.code === null && oldObserved.signal === 'SIGKILL',
          'Runner parent did not observe exact old-service SIGKILL')
        await waitForProcessGroupDeath(oldService.pid, this.binding.manifest.timeouts.closeMs,
          'authenticated old service')
        assert(processGroupExists(proof.newService.pid),
          'authenticated replacement service process group is not live')
        const adopted = registeredExternalService(proof.newService)
        this.services = [adopted]
        this.serviceHistory.push(adopted)
        this.controllerCoreProof = proof
        this.controllerCoreProofIdentity = pending.outputIdentity
        this.controllerTermination = Object.freeze({ requestedSignal: 'SIGKILL',
          observedSignal: 'SIGKILL', exitCode: null, processGroupDead: true,
          completedAt: new Date().toISOString() })
        this.pendingController = null
        this.controllerAdopted = true
        this.controllerArmed = false
        this.controllerBoundaryReached = false
        return Object.freeze({ active: true, serviceIdSha256: adopted.serviceIdSha256,
          processGroupIdentitySha256: adopted.processGroupIdentitySha256, adoptedController: true })
      } catch (error) {
        killProcessGroup(pending.pid, 'SIGKILL')
        await waitForProcessGroupDeath(pending.pid, this.binding.manifest.timeouts.closeMs,
          'failed authenticated controller')
        if (validatedReplacementPID !== null) {
          killProcessGroup(validatedReplacementPID, 'SIGKILL')
          await waitForProcessGroupDeath(validatedReplacementPID,
            this.binding.manifest.timeouts.closeMs, 'failed authenticated replacement service')
        }
        const cleanupErrors = await collectCleanupErrors([
          () => pending.outputIdentity === undefined
            ? requireAbsentOrRetained(pending.outputFile, 'unauthenticated service-controller core proof')
            : cleanupOptionalRegularFile(pending.outputFile,
              this.binding.ownedPathCapability, 'failed service-controller core proof',
              pending.outputIdentity),
        ])
        this.pendingController = null
        this.controllerBoundaryReached = false
        if (cleanupErrors.length > 0) {
          throw new AggregateError([error, ...cleanupErrors],
            'service-controller failure and core-proof cleanup failed')
        }
        throw error
      }
    }
    if (this.inProcessTestTransport) {
      const service = registeredSyntheticProcess(this.serviceHistory.length + 1)
      this.services.push(service)
      this.serviceHistory.push(service)
      return Object.freeze({ active: true, serviceIdSha256: service.serviceIdSha256,
        processGroupIdentitySha256: service.processGroupIdentitySha256, adoptedController: false })
    }
    const child = spawn(spec.file, spec.argv, {
      shell: false, detached: true, stdio: ['ignore', 'ignore', 'ignore'],
      env: runnerChildEnvironment(this.binding),
    })
    await waitSpawn(child, 'installed service')
    const service = registeredProcess(child, 'installed-service')
    try { await awaitLoopbackReadiness(serve.port, serve.generationId,
      this.binding.authority.tupleIdentity, this.binding.manifest.timeouts.controllerMs, spec) }
    catch (error) { await stopRegistered(service, this.binding.manifest.timeouts.closeMs); throw error }
    this.services.push(service)
    this.serviceHistory.push(service)
    return Object.freeze({ active: true, serviceIdSha256: service.serviceIdSha256,
      processGroupIdentitySha256: service.processGroupIdentitySha256, adoptedController: false })
  }

  async armAuthenticatedControllerRecovery(request) {
    exactKeys(request, ['requestFile', 'controllerExecutable', 'controllerSha256',
      'taskId', 'runId', 'attemptId'], 'authenticated controller arm')
    const expected = this.binding.manifest.serviceController
    assert(request.requestFile === expected.requestFile && request.controllerExecutable === expected.executableFile &&
      request.controllerSha256 === expected.sha256, 'service-controller manifest binding drifted')
    await validate0500Executable(request.controllerExecutable, request.controllerSha256, 'service controller')
    const requestBytes = await read0400(request.requestFile, 'service controller request', MAX_JSON)
    const document = parseJSON(requestBytes, 'service controller request')
    exactKeys(document, ['schemaVersion', 'action', 'controllerSha256', 'tupleFile',
      'restartReceiptFile', 'taskId', 'runId', 'interruptedAttemptId'], 'service controller request')
    assert(document.schemaVersion === 'chora.m1-o4-service-control-request.v1' &&
      document.action === 'interrupt_restart_and_prove' &&
      document.controllerSha256 === request.controllerSha256 &&
      document.tupleFile === this.binding.manifest.candidate.tupleFile &&
      document.restartReceiptFile === controllerRestartReceiptFile(this.binding.manifest) &&
      document.taskId === request.taskId && document.runId === request.runId &&
      document.interruptedAttemptId === request.attemptId,
    'service-controller request/task/run/attempt/tuple/restart binding drifted')
    for (const [value, label] of [[document.taskId, 'Task'], [document.runId, 'Run'],
      [document.interruptedAttemptId, 'Attempt']]) assertProductID(value, label)
    assert(this.services.length === 1, 'controller recovery requires exactly one active product service')
    assert(!this.controllerArmed && !this.controllerAdopted,
      'authenticated orphan controller may be armed/adopted only once')
    const tuple = await read0400JSON(document.tupleFile, 'controller-bound tuple', MAX_JSON)
    assert(tuple.identity === this.binding.authority.tupleIdentity &&
      tuple.generationId === this.binding.authority.identity.generationId,
    'controller request tuple/generation drifted')
    this.controllerArm = Object.freeze({ document, requestSha256: sha256(requestBytes),
      oldService: this.services[0] })
    this.controllerArmed = true
    this.controllerBoundaryReached = false
  }

  async awaitAuthenticatedControllerProof(request) {
    exactKeys(request, ['outputFile', 'restartReceiptFile'], 'controller proof wait')
    assert(this.controllerAdopted, 'controller proof requires the one adopted controller')
    assert(request.outputFile === controllerProofFile(this.binding.manifest) &&
      request.restartReceiptFile === controllerRestartReceiptFile(this.binding.manifest),
    'controller proof/restart paths drifted')
    const receiptBytes = await read0400(request.restartReceiptFile, 'controller restart receipt', MAX_JSON)
    const receipt = parseJSON(receiptBytes, 'controller restart receipt')
    assert(this.controllerCoreProof && this.controllerTermination && this.controllerProof === null,
      'controller final proof may be sealed exactly once after adoption')
    const core = this.controllerCoreProof
    const proofDraft = {
      schemaVersion: 'chora.m1-o4-service-control.v1', status: 'passed', protocol: core.protocol,
      controllerPathSha256: core.controllerPathSha256, controllerSha256: core.controllerSha256,
      requestSha256: core.requestSha256, runtimeBindingSha256: core.runtimeBindingSha256,
      argvSha256: core.argvSha256, tupleIdentity: core.tupleIdentity, binarySha256: core.binarySha256,
      generationId: core.generationId, oldService: core.oldService,
      termination: this.controllerTermination, newService: core.newService,
      restartReceipt: { file: basename(request.restartReceiptFile), rawSha256: sha256(receiptBytes),
        semanticDigest: receipt.receiptDigest }, networkDisabled: true,
    }
    const proof = Object.freeze({ ...proofDraft, digest: sha256(canonicalJSONStringify(proofDraft)) })
    let finalProofIdentity
    try {
      validateServiceControlProof(proof, this.binding,
        serviceControllerArgv(this.binding.manifest.serviceController.requestFile,
          controllerCoreProofFile(this.binding.manifest)),
        this.controllerArm, null)
      assert(proof.restartReceipt.file === basename(request.restartReceiptFile) &&
        proof.restartReceipt.rawSha256 === sha256(receiptBytes) &&
        proof.restartReceipt.semanticDigest === receipt.receiptDigest,
      'service controller proof is not bound to the actual restart receipt')
      finalProofIdentity = await writeExclusive0400JSON(request.outputFile, proof, 'service controller final proof',
        this.binding.ownedPathCapability)
      await cleanupOwnedPath({
        capability: this.binding.ownedPathCapability,
        path: controllerCoreProofFile(this.binding.manifest),
        identity: this.controllerCoreProofIdentity,
        disposition: 'regular_file',
      })
      this.controllerProof = proof
      return Object.freeze({ status: 'proven', adoptedExactlyOnce: true })
    } catch (error) {
      const cleanupErrors = await collectCleanupErrors([
        () => finalProofIdentity === undefined
          ? requireAbsentOrRetained(request.outputFile, 'unbound service-controller final proof')
          : cleanupOptionalRegularFile(request.outputFile,
            this.binding.ownedPathCapability, 'failed service-controller final proof',
            finalProofIdentity),
      ])
      if (cleanupErrors.length > 0) {
        throw new AggregateError([error, ...cleanupErrors],
          'service-controller proof sealing and final-proof cleanup failed')
      }
      throw error
    }
  }

  async exportApplyDisclosureSources(request) {
    exactKeys(request, ['requestFile', 'requestSha256', 'responseBody'], 'disclosure export request')
    const requestBytes = await read0400(request.requestFile, 'disclosure export request', MAX_JSON)
    assert(sha256(requestBytes) === request.requestSha256, 'disclosure export request pin drifted')
    const document = parseJSON(requestBytes, 'disclosure export request')
    exactKeys(document, ['schemaVersion', 'status', 'protocol', 'runnerModuleSha256',
      'networkDisabled', 'applyBinding', 'sources', 'outputs', 'limits', 'requestDigest'],
    'disclosure export request')
    assert(document.schemaVersion === 'chora.m1-o4-disclosure-export-request.v2' &&
      document.protocol === 'chora.m1-o4-readonly-disclosure-exporter.v1' &&
      document.status === 'authorized' && document.networkDisabled === true,
    'disclosure export request schema/protocol drifted')
    exactKeys(document.outputs, ['executionLogFile', 'stateDatabaseFile', 'changePatchFile',
      'responsePayloadFile', 'coreAcknowledgementFile', 'acknowledgementFile'],
    'disclosure export outputs')
    await absent(document.outputs.coreAcknowledgementFile, 'disclosure exporter core acknowledgement')
    await absent(document.outputs.acknowledgementFile, 'disclosure exporter final acknowledgement')
    assert(Buffer.isBuffer(request.responseBody) && request.responseBody.length <= 4 * 1024 * 1024,
      'disclosure response body is invalid')
    const controller = this.binding.manifest.serviceController
    await validate0500Executable(controller.executableFile, controller.sha256, 'disclosure exporter executable')
    const child = spawn(controller.executableFile, [
      '--protocol', 'chora.m1-o4-readonly-disclosure-exporter.v1',
      '--request', request.requestFile, '--network', 'disabled',
    ], { shell: false, detached: true, stdio: ['pipe', 'ignore', 'ignore'],
      env: runnerChildEnvironment(this.binding) })
    await waitSpawn(child, 'disclosure exporter')
    const registered = registeredProcess(child, `exporter-${request.requestSha256}`)
    this.exporters.set(request.requestSha256, registered)
    child.stdin.end(request.responseBody)
    const outputIdentities = new Map()
    try {
      const exit = await waitExit(child, this.binding.manifest.timeouts.exporterMs)
      assert(exit.code === 0 && exit.signal === null, 'disclosure exporter failed')
      await waitForProcessGroupDeath(registered.pid, this.binding.manifest.timeouts.closeMs,
        'disclosure exporter')
      for (const [name, path] of Object.entries(document.outputs)) {
        if (name === 'acknowledgementFile') continue
        outputIdentities.set(path, await ownedRegularFileIdentity(path,
          `authenticated disclosure output ${name}`))
      }
      const capturedCore = await stableOwnerFile(document.outputs.coreAcknowledgementFile,
        'disclosure exporter core acknowledgement', MAX_JSON, 0o400)
      assert(sameOwnedPathIdentity(capturedCore.identity,
        outputIdentities.get(document.outputs.coreAcknowledgementFile)),
      'disclosure core acknowledgement identity changed after authentication')
      const core = parseJSON(capturedCore.bytes, 'disclosure exporter core acknowledgement')
      validateDisclosureExporterCore(core, document, request.requestSha256)
      const ackDraft = { schemaVersion: 'chora.m1-o4-disclosure-export-ack.v1', status: 'completed',
        protocol: core.protocol, runnerModuleSha256: core.runnerModuleSha256,
        requestSha256: core.requestSha256, networkDisabled: true,
        applyBinding: core.applyBinding, artifacts: core.artifacts,
        databaseSnapshot: core.databaseSnapshot, processGroupDead: true, descendantsDead: true,
        completedAt: new Date().toISOString() }
      const ack = Object.freeze({ ...ackDraft, ackDigest: sha256(canonicalJSONStringify(ackDraft)) })
      await cleanupOwnedPath({
        capability: this.binding.ownedPathCapability,
        path: document.outputs.coreAcknowledgementFile,
        identity: capturedCore.identity,
        disposition: 'regular_file',
      })
      outputIdentities.delete(document.outputs.coreAcknowledgementFile)
      const acknowledgementIdentity = await writeExclusive0400JSON(
        document.outputs.acknowledgementFile, ack,
        'disclosure exporter final acknowledgement', this.binding.ownedPathCapability)
      outputIdentities.set(document.outputs.acknowledgementFile, acknowledgementIdentity)
      return Object.freeze({ status: 'completed', requestSha256: request.requestSha256 })
    } catch (error) {
      const cleanupErrors = await collectCleanupErrors([
        () => stopRegistered(registered, this.binding.manifest.timeouts.closeMs),
        ...Object.entries(document.outputs).map(([name, path]) => () => {
          const identity = outputIdentities.get(path)
          return identity === undefined
            ? requireAbsentOrRetained(path, `unauthenticated disclosure output ${name}`)
            : cleanupOptionalRegularFile(path, this.binding.ownedPathCapability,
              `failed disclosure output ${name}`, identity)
        }),
      ])
      if (cleanupErrors.length > 0) {
        throw new AggregateError([error, ...cleanupErrors],
          'disclosure export and identity-bound cleanup failed')
      }
      throw error
    } finally { this.exporters.delete(request.requestSha256) }
  }

  async abortApplyDisclosureExporter(request) {
    exactKeys(request, ['requestSha256'], 'disclosure exporter abort')
    const exporter = this.exporters.get(request.requestSha256)
    if (exporter) {
      await stopRegistered(exporter, this.binding.manifest.timeouts.closeMs)
      this.exporters.delete(request.requestSha256)
    }
    return Object.freeze({ status: 'aborted', requestSha256: request.requestSha256,
      processGroupDead: true, descendantsDead: true })
  }

  async closeAllServices(request) {
    exactKeys(request, ['tupleFile', 'sourceA3LedgerFile', 'timeoutMs'], 'service closure request')
    assert(request.tupleFile === this.binding.manifest.candidate.tupleFile,
      'service closure tuple path drifted')
    assert(Number.isSafeInteger(request.timeoutMs) && request.timeoutMs > 0 &&
      request.timeoutMs <= this.binding.manifest.timeouts.closeMs, 'service closure timeout drifted')
    // Resource closure precedes evidence authentication so every authorized
    // pre-E failure still closes parent-owned handles and descendants.
    const active = [...this.services, ...this.exporters.values()]
    for (const service of active) await stopRegistered(service, request.timeoutMs)
    if (this.pendingController) {
      killProcessGroup(this.pendingController.pid, 'SIGKILL')
      await waitForProcessGroupDeath(this.pendingController.pid, request.timeoutMs,
        'pending authenticated controller')
      this.pendingController = null
    }
    for (const service of this.serviceHistory) {
      if (service.synthetic) assert(service.dead === true,
        'synthetic product service lacks deterministic death proof')
      else await waitForProcessGroupDeath(service.pid, request.timeoutMs, 'registered product service')
    }
    this.services = []
    this.exporters.clear()
    this.closed = true
    try {
      const tuple = await read0400JSON(request.tupleFile, 'service closure tuple', MAX_JSON)
      assert(tuple.identity === this.binding.authority.tupleIdentity, 'service closure tuple identity drifted')
      const source = await readJSONAnyMode(request.sourceA3LedgerFile, 'service closure A3', MAX_JSON)
      const services = this.serviceHistory.map((service) => ({
        serviceIdSha256: service.serviceIdSha256,
        processGroupIdentitySha256: service.processGroupIdentitySha256,
        status: 'closed', processGroupDead: true, descendantsDead: true,
      }))
      const draft = {
        schemaVersion: 'chora.m1-o4-service-closure.v1', status: 'closed',
        environmentId: tuple.environmentId, installId: tuple.installId,
        generationId: tuple.generationId, tupleIdentity: tuple.identity,
        bindingDigest: source.bindingDigest,
        runnerModuleSha256: this.binding.manifest.runner.moduleSha256,
        registeredServiceCount: services.length, closedServiceCount: services.length,
        services, processGroupDead: true, descendantsDead: true,
        completedAt: new Date().toISOString(),
      }
      if (services.length === 0) throw new Error('service closure has no registered product service')
      // A pre-E parent failure may stop after closure rather than sealing. The
      // unref lets that parent terminate after unlinking the unreachable socket;
      // normal E immediately reuses the still-open socket for seal delivery.
      this.server?.unref()
      return Object.freeze({ ...draft, digest: sha256(canonicalJSONStringify(draft)) })
    } catch (error) {
      try { await this.closeControlPlane() } catch (cleanupError) {
        throw new AggregateError([error, cleanupError],
          'service closure and control-plane cleanup failed')
      }
      throw error
    }
  }

  async stopServiceGroups() {
    for (const service of this.services) await stopRegistered(service, this.binding.manifest.timeouts.closeMs)
    this.services = []
  }

  async closeControlPlane() {
    if (this.server) {
      const server = this.server
      await new Promise((resolvePromise, rejectPromise) => server.close((error) => {
        if (error) rejectPromise(error)
        else resolvePromise()
      }))
      this.server = undefined
    }
    if (this.inProcessTestTransport) return
    if (this.controlSocketIdentity !== undefined) {
      await cleanupOwnedPath({
        capability: this.binding.ownedPathCapability,
        path: this.binding.manifest.runner.controlSocketPath,
        identity: this.controlSocketIdentity,
        disposition: 'unix_socket',
      })
      this.controlSocketIdentity = undefined
    }
  }

  async sealA3Ledger(request) {
    exactKeys(request, ['tupleIdentity', 'sourceA3LedgerFile', 'expectedUnsealedSha256',
      'expectedAuditRecordCount', 'expectedAuditFinalDigest'], 'A3 seal request')
    assert(this.closed, 'A3 sealing is forbidden before all services close')
    assert(request.tupleIdentity === this.binding.authority.tupleIdentity,
      'A3 seal tuple/session drifted')
    exactAbsolute(request.sourceA3LedgerFile, 'A3 ledger path')
    const { bytes, info: beforeInfo, identity: beforeIdentity } = await stableOwnerFile(request.sourceA3LedgerFile,
      'unsealed A3', MAX_JSON, 0o600)
    assert(sha256(bytes) === request.expectedUnsealedSha256, 'A3 unsealed bytes drifted')
    const value = parseJSON(bytes, 'unsealed A3')
    assert(value.status === 'recording' && value.complete === false && value.auditSealed === false,
      'A3 is not the exact unsealed D boundary')
    assert(value.auditRecordCount === request.expectedAuditRecordCount &&
      value.auditFinalDigest === request.expectedAuditFinalDigest &&
      value.tupleIdentity === request.tupleIdentity, 'A3 seal authority drifted')
    const sealed = { ...value, status: 'passed', complete: true, auditSealed: true }
    const temporary = `${request.sourceA3LedgerFile}.seal-${randomBytes(12).toString('hex')}`
    let temporaryIdentity
    try {
      temporaryIdentity = await writeExclusive(
        Buffer.from(`${JSON.stringify(sealed, null, 2)}\n`), temporary, 0o400,
        this.binding.ownedPathCapability)
      await replaceOwnedPath({
        capability: this.binding.ownedPathCapability,
        sourcePath: temporary,
        targetPath: request.sourceA3LedgerFile,
        sourceIdentity: temporaryIdentity,
        targetIdentity: beforeIdentity,
        sourceDisposition: 'regular_file',
        targetDisposition: 'regular_file',
      })
      const sealedStable = await stableOwnerFile(request.sourceA3LedgerFile,
        'sealed A3 replacement', MAX_JSON, 0o400)
      assert((sealedStable.info.dev !== beforeInfo.dev || sealedStable.info.ino !== beforeInfo.ino) &&
        sha256(sealedStable.bytes) === sha256(Buffer.from(`${JSON.stringify(sealed, null, 2)}\n`)),
      'sealed A3 durable replacement identity/mode/bytes drifted')
    } catch (error) {
      const cleanupErrors = temporaryIdentity === undefined ? [] : await collectCleanupErrors([
        () => cleanupOptionalRegularFile(temporary, this.binding.ownedPathCapability,
          'failed A3 sealed temporary', temporaryIdentity),
      ])
      if (cleanupErrors.length > 0) {
        throw new AggregateError([error, ...cleanupErrors],
          'A3 sealing and temporary cleanup failed')
      }
      throw error
    }
    this.closeAfterResponse = true
    return Object.freeze({ status: 'sealed', beforeSha256: request.expectedUnsealedSha256,
      auditRecordCount: sealed.auditRecordCount, auditFinalDigest: sealed.auditFinalDigest })
  }
}

function proxyView(binding, view) {
  let sequence = 0
  const result = {}
  for (const method of viewMethods[view]) {
    result[method] = async (...args) => {
      sequence += 1
      const safe = {
        schemaVersion: REQUEST_SCHEMA, view, sequence, method, args: encode(args),
        sessionDigest: binding.authority.authorityDigest,
      }
      const request = { ...safe, mac: hmac(binding.authority.sessionSecret, safe) }
      return rpc(binding.manifest.runner.controlSocketPath, request)
    }
  }
  return result
}

async function rpc(socketPath, request) {
  return new Promise((resolvePromise, reject) => {
    const socket = createConnection(socketPath)
    let bytes = Buffer.alloc(0)
    socket.on('connect', () => socket.end(`${JSON.stringify(request)}\n`))
    socket.on('data', (chunk) => {
      bytes = Buffer.concat([bytes, chunk])
      if (bytes.length > MAX_JSON) socket.destroy(new Error('Runner response is oversized'))
    })
    socket.on('error', reject)
    socket.on('end', () => {
      try {
        const response = parseJSON(bytes, 'Runner control response')
        exactKeys(response, response.ok ? ['schemaVersion', 'ok', 'result'] :
          ['schemaVersion', 'ok', 'error'], 'Runner control response')
        assert(response.schemaVersion === RESPONSE_SCHEMA, 'Runner response schema drifted')
        if (!response.ok) throw new Error(response.error)
        resolvePromise(decode(response.result))
      } catch (error) { reject(error) }
    })
  })
}

async function authenticateBinding(options) {
  exactAbsolute(options.manifestFile, 'total manifest file')
  exactAbsolute(options.sessionAuthorityFile, 'session authority file')
  digest(options.manifestSha256, 'total manifest SHA-256')
  const manifestBytes = await read0400(options.manifestFile, 'total manifest', MAX_JSON)
  assert(sha256(manifestBytes) === options.manifestSha256, 'total manifest raw SHA-256 drifted')
  const manifest = parseJSON(manifestBytes, 'total manifest')
  const ownedPathCapability = validateManifestBinding(manifest)
  const self = { ...manifest, selfDigest: null }
  assert(manifest.selfDigest === sha256(canonicalJSONStringify(self)), 'total manifest self-digest drifted')
  assert(manifest.runner.moduleFile === fileURLToPath(import.meta.url), 'Runner module path drifted')
  const runnerBytes = await read0400(manifest.runner.moduleFile, 'pinned B2 Runner', MAX_JSON)
  assert(sha256(runnerBytes) === manifest.runner.moduleSha256, 'Runner module SHA-256 drifted')
  const configBytes = await read0400(manifest.candidate.configFile, 'pinned candidate config', MAX_JSON)
  assert(sha256(configBytes) === manifest.candidate.configSha256, 'candidate config SHA-256 drifted')
  const candidateConfig = parseJSON(configBytes, 'pinned candidate config')
  sameJSON(candidateConfig.identity, manifest.identity, 'candidate/total identity drifted')
  assert(candidateConfig.paths?.repositoryRoot === manifest.repository.root &&
    candidateConfig.authority?.repositoryCommit === manifest.repository.commit &&
    candidateConfig.authority?.repositoryClosureSha256 === manifest.repository.closureSha256,
  'candidate/total repository authority drifted')
  const authority = await read0400JSON(options.sessionAuthorityFile, 'Runner session authority', MAX_JSON)
  validateAuthority(authority, manifest, options.manifestSha256)
  return Object.freeze({ manifest, authority, candidateConfig, ownedPathCapability })
}

function validateManifestBinding(value) {
  assertPlain(value, 'total manifest')
  exactKeys(value, ['schemaVersion', 'status', 'acceptanceClass', 'selfDigest', 'identity',
    'candidate', 'repository', 'runner', 'serviceController', 'ocr', 'engine', 'playwright', 'roots', 'phases',
    'postflight', 'timeouts', 'completionFile'], 'total manifest')
  assert(value.schemaVersion === TOTAL_SCHEMA && value.status === 'authorized',
    'total manifest schema/status drifted')
  assert(['real_acceptance', 'synthetic_non_acceptance'].includes(value.acceptanceClass),
    'total manifest acceptance class is invalid')
  digest(value.runner?.moduleSha256, 'Runner module pin')
  exactAbsolute(value.runner?.moduleFile, 'Runner module file')
  exactAbsolute(value.runner?.controlSocketPath, 'Runner socket path')
  assert(value.candidate?.tupleIdentity === null,
    'pre-bootstrap total manifest tuple identity must remain runtime-derived')
  exactKeys(value.serviceController, ['executableFile', 'sha256', 'requestFile'],
    'Runner service controller')
  const ownedPathCapability = validateOwnedPathCapability({
    executableFile: value.serviceController.executableFile,
    executableSha256: value.serviceController.sha256,
  }, 'Runner owned-path capability')
  exactAbsolute(value.serviceController.requestFile, 'Runner service-controller request')
  exactKeys(value.repository, ['root', 'commit', 'closureSha256'],
    'Runner repository authority')
  exactAbsolute(value.repository.root, 'Runner repository root')
  assert(/^[0-9a-f]{40}$/.test(value.repository.commit),
    'Runner repository commit is invalid')
  digest(value.repository.closureSha256, 'Runner repository closure')
  exactKeys(value.playwright, ['nodeFile', 'nodeSha256', 'cliFile', 'cliSha256',
    'packageRoot', 'packageClosureSha256', 'browserRoot', 'browserClosureSha256'],
  'Runner Playwright binding')
  for (const path of [value.playwright.nodeFile, value.playwright.cliFile,
    value.playwright.packageRoot, value.playwright.browserRoot]) {
    exactAbsolute(path, 'Runner Playwright path')
  }
  for (const pin of [value.playwright.nodeSha256, value.playwright.cliSha256,
    value.playwright.packageClosureSha256, value.playwright.browserClosureSha256]) {
    digest(pin, 'Runner Playwright pin')
  }
  assert(pathWithin(value.playwright.packageRoot, value.playwright.cliFile) &&
    !pathWithin(value.playwright.packageRoot, value.playwright.browserRoot) &&
    !pathWithin(value.playwright.browserRoot, value.playwright.packageRoot),
  'Runner Playwright package/browser closure binding drifted')
  for (const name of ['controllerMs', 'exporterMs', 'closeMs']) {
    assert(Number.isSafeInteger(value.timeouts?.[name]) && value.timeouts[name] >= 50 &&
      value.timeouts[name] <= 120_000, `${name} is invalid`)
  }
  return ownedPathCapability
}

function validateAuthority(value, manifest, manifestSha256) {
  assertPlain(value, 'Runner session authority')
  exactKeys(value, ['schemaVersion', 'status', 'manifestSha256', 'runnerSha256',
    'socketPathDigest', 'tupleIdentity', 'identity', 'parentPid', 'sessionId',
    'sessionSecret', 'sequenceStart', 'authorityDigest'], 'Runner session authority')
  assert(value.schemaVersion === SESSION_SCHEMA && value.status === 'exclusive',
    'Runner session schema/status drifted')
  assert(value.manifestSha256 === manifestSha256 && value.runnerSha256 === manifest.runner.moduleSha256 &&
    value.socketPathDigest === sha256(manifest.runner.controlSocketPath) &&
    digestRE.test(value.tupleIdentity),
  'Runner session manifest/Runner/socket/tuple binding drifted')
  sameJSON(value.identity, manifest.identity, 'Runner session identity drifted')
  assert(Number.isSafeInteger(value.parentPid) && value.parentPid > 1 &&
    digestRE.test(value.sessionId) && digestRE.test(value.sessionSecret) && value.sequenceStart === 0,
  'Runner session authority fields are invalid')
  const { authorityDigest, ...safe } = value
  assert(authorityDigest === sha256(canonicalJSONStringify(safe)),
    'Runner session authority digest drifted')
}

async function runBounded(file, argv, maxOutputBytes, env) {
  assert(Number.isSafeInteger(maxOutputBytes) && maxOutputBytes > 0 && maxOutputBytes <= MAX_OUTPUT,
    'Runner output bound is invalid')
  await validate0500Executable(file, undefined, 'Runner command executable')
  const child = spawn(file, argv, { shell: false, detached: true,
    stdio: ['ignore', 'pipe', 'pipe'], env })
  let stdout = Buffer.alloc(0); let stderr = Buffer.alloc(0); let overflow = false
  child.stdout.on('data', (chunk) => {
    const next = Buffer.concat([stdout, chunk])
    if (next.length > maxOutputBytes) { overflow = true; killProcessGroup(child.pid, 'SIGKILL') } else stdout = next
  })
  child.stderr.on('data', (chunk) => {
    const next = Buffer.concat([stderr, chunk])
    if (next.length > maxOutputBytes) { overflow = true; killProcessGroup(child.pid, 'SIGKILL') } else stderr = next
  })
  const exit = await waitExit(child)
  if (processGroupExists(child.pid)) {
    killProcessGroup(child.pid, 'SIGKILL')
    await waitForProcessGroupDeath(child.pid, 2_000, 'Runner command')
    if (!overflow) throw new Error('Runner command left a descendant process')
  } else {
    await waitForProcessGroupDeath(child.pid, 2_000, 'Runner command')
  }
  assert(!overflow, 'Runner command output exceeded its bound')
  return Object.freeze({ exitCode: exit.code ?? -1, stdout: stdout.toString('utf8'), stderr: stderr.toString('utf8') })
}

function registeredProcess(child, label) {
  const nonce = randomBytes(32).toString('hex')
  return { child, pid: child.pid, processGroupId: child.pid,
    startIdentitySha256: sha256(`start:${label}:${child.pid}:${nonce}`),
    serviceIdSha256: sha256(`${label}:${child.pid}:${nonce}`),
    processGroupIdentitySha256: sha256(`pgid:${child.pid}:${nonce}`) }
}

function registeredExternalService(service) {
  return { child: null, pid: service.pid,
    serviceIdSha256: sha256(`adopted:${service.startIdentitySha256}:${service.pid}`),
    processGroupIdentitySha256: sha256(`pgid:${service.pid}:${service.startIdentitySha256}`) }
}

function registeredSyntheticProcess(sequence) {
  const nonce = sha256(`synthetic-non-acceptance-service:${sequence}`)
  return { child: null, pid: null, synthetic: true, dead: false,
    serviceIdSha256: sha256(`synthetic-service:${nonce}`),
    processGroupIdentitySha256: sha256(`synthetic-process-group:${nonce}`) }
}

function controllerProofFile(manifest) {
  return exactAbsolute(manifest.phases?.D?.environment?.CHORA_O4_RECOVERY_RESIDUE_SERVICE_CONTROL_FILE,
    'service controller proof path')
}

function controllerCoreProofFile(manifest) {
  return join(dirname(exactAbsolute(manifest.runner.controlSocketPath, 'Runner control socket path')),
    'service-controller-core.json')
}

function controllerRestartReceiptFile(manifest) {
  return exactAbsolute(manifest.phases?.D?.environment?.CHORA_O4_RECOVERY_RESIDUE_ORPHAN_RESTART_RECEIPT_FILE,
    'service controller restart receipt path')
}

function serviceControllerArgv(requestFile, outputFile) {
  return ['--protocol', 'chora.m1-o4-service-control.v1', '--request', requestFile,
    '--output', outputFile, '--network', 'disabled', '--runtime-input', 'stdin']
}

function serviceControllerRuntimeBinding(binding, oldService, spec, serve) {
  const draft = {
    schemaVersion: 'chora.m1-o4-service-control-runtime-binding.v1',
    tupleIdentity: binding.authority.tupleIdentity,
    binarySha256: binding.candidateConfig.authority.binarySha256,
    generationId: binding.authority.identity.generationId,
    oldService: { pid: oldService.pid, processGroupId: oldService.processGroupId,
      startIdentitySha256: oldService.startIdentitySha256,
      serviceIdSha256: oldService.serviceIdSha256,
      processGroupIdentitySha256: oldService.processGroupIdentitySha256 },
    restart: { executableFile: spec.file, argv: [...spec.argv], cwd: process.cwd(), port: serve.port,
      environment: runnerChildEnvironment(binding) },
    timeoutMs: binding.manifest.timeouts.controllerMs,
  }
  return Object.freeze({ ...draft, bindingDigest: sha256(canonicalJSONStringify(draft)) })
}

function runnerChildEnvironment(binding) {
  if (binding.manifest.acceptanceClass === 'synthetic_non_acceptance') {
    return Object.freeze({ LANG: 'C', LC_ALL: 'C', PATH: '/usr/bin:/bin',
      CHORA_NETWORK: 'disabled' })
  }
  const roots = binding.manifest.roots
  const engine = binding.manifest.engine
  for (const path of [roots.userRoot, roots.temporaryRoot, roots.dockerConfigRoot,
    roots.colimaHome]) exactAbsolute(path, 'Runner child environment path')
  assert(typeof engine.contextName === 'string' && /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/.test(engine.contextName),
    'Runner Docker context is invalid')
  return Object.freeze({
    LANG: 'C', LC_ALL: 'C', PATH: '/usr/bin:/bin', CHORA_NETWORK: 'disabled',
    HOME: roots.userRoot, TMPDIR: roots.temporaryRoot, COLIMA_HOME: roots.colimaHome,
    DOCKER_CONFIG: roots.dockerConfigRoot, DOCKER_CONTEXT: engine.contextName,
  })
}

function captureBounded(pending, maxBytes) {
  for (const name of ['stdout', 'stderr']) pending.child[name].on('data', (chunk) => {
    const next = Buffer.concat([pending[name], chunk])
    if (next.length > maxBytes) {
      pending.overflow = true
      killProcessGroup(pending.pid, 'SIGKILL')
    } else pending[name] = next
  })
}

async function waitForControllerProof(path, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  while (true) {
    try {
      const captured = await stableOwnerFile(path, 'service controller proof', MAX_JSON, 0o400)
      return Object.freeze({
        value: parseJSON(captured.bytes, 'service controller proof'),
        identity: captured.identity,
      })
    }
    catch (error) {
      if (error.code !== 'ENOENT') throw error
      if (Date.now() >= deadline) throw new Error('service controller proof timed out')
      await delay(25)
    }
  }
}

function validateServiceControlCoreProof(value, binding, argv, runtimeBinding, runtimeBytes, arm, pending) {
  exactKeys(value, ['schemaVersion', 'status', 'protocol', 'controllerPathSha256',
    'controllerSha256', 'requestSha256', 'runtimeBindingSha256', 'argvSha256',
    'tupleIdentity', 'binarySha256', 'generationId', 'oldService', 'termination',
    'newService', 'networkDisabled', 'digest'], 'service controller core proof')
  assert(value.schemaVersion === 'chora.m1-o4-service-control-core.v1' && value.status === 'passed' &&
    value.protocol === 'pinned_authenticated_service_control_v1' && value.networkDisabled === true,
  'service controller core schema/status/protocol/network drifted')
  const controller = binding.manifest.serviceController
  assert(value.controllerPathSha256 === sha256(controller.executableFile) &&
    value.controllerSha256 === controller.sha256 && value.requestSha256 === arm.requestSha256 &&
    value.runtimeBindingSha256 === sha256(runtimeBytes) &&
    runtimeBinding.bindingDigest === sha256(canonicalJSONStringify(
      Object.fromEntries(Object.entries(runtimeBinding).filter(([key]) => key !== 'bindingDigest')))) &&
    value.argvSha256 === sha256(canonicalJSONStringify(argv)) &&
    value.tupleIdentity === binding.authority.tupleIdentity &&
    value.binarySha256 === binding.candidateConfig.authority.binarySha256 &&
    value.generationId === binding.authority.identity.generationId,
  'service controller core pin/request/runtime/argv/tuple/binary/generation binding drifted')
  validateServiceIdentity(value.oldService, 'old service')
  validateServiceIdentity(value.newService, 'new service')
  assert(value.oldService.pid === arm.oldService.pid &&
    value.oldService.startIdentitySha256 === arm.oldService.startIdentitySha256 &&
    value.oldService.pid !== value.newService.pid &&
    value.oldService.startIdentitySha256 !== value.newService.startIdentitySha256,
  'service controller core old/new identity drifted')
  exactKeys(value.termination, ['requestedSignal', 'processGroupDead', 'completedAt'],
    'service controller core termination')
  assert(value.termination.requestedSignal === 'SIGKILL' &&
    value.termination.processGroupDead === true && validTimestamp(value.termination.completedAt),
  'service controller core did not prove requested SIGKILL/group death')
  assert(value.oldService.readiness === 'terminated' && value.oldService.currentGeneration === false &&
    value.newService.readiness === 'ready' && value.newService.currentGeneration === true,
  'service controller core readiness/current generation drifted')
  for (const service of [value.oldService, value.newService]) assert(
    service.binarySha256 === value.binarySha256 && service.tupleIdentity === value.tupleIdentity &&
    service.generationId === value.generationId, 'service controller core candidate binding drifted')
  if (pending) assert(pending.pid !== value.oldService.pid && pending.pid !== value.newService.pid,
    'service controller PID was misregistered as a product service')
  const { digest: actual, ...draft } = value
  assert(actual === sha256(canonicalJSONStringify(draft)), 'service controller core digest drifted')
}

function validateServiceControlProof(value, binding, argv, arm, pending) {
  exactKeys(value, ['schemaVersion', 'status', 'protocol', 'controllerPathSha256',
    'controllerSha256', 'requestSha256', 'runtimeBindingSha256', 'argvSha256', 'tupleIdentity', 'binarySha256',
    'generationId', 'oldService', 'termination', 'newService', 'restartReceipt',
    'networkDisabled', 'digest'], 'service controller proof')
  const controller = binding.manifest.serviceController
  assert(value.schemaVersion === 'chora.m1-o4-service-control.v1' && value.status === 'passed' &&
    value.protocol === 'pinned_authenticated_service_control_v1' && value.networkDisabled === true,
  'service controller schema/status/protocol/network drifted')
  assert(value.controllerPathSha256 === sha256(controller.executableFile) &&
    value.controllerSha256 === controller.sha256 && value.requestSha256 === arm.requestSha256 &&
    value.argvSha256 === sha256(canonicalJSONStringify(argv)) &&
    value.tupleIdentity === binding.authority.tupleIdentity &&
    value.binarySha256 === binding.candidateConfig.authority.binarySha256 &&
    value.generationId === binding.authority.identity.generationId,
  'service controller pin/request/argv/tuple/binary/generation binding drifted')
  for (const field of ['controllerPathSha256', 'controllerSha256', 'requestSha256', 'runtimeBindingSha256',
    'argvSha256', 'tupleIdentity', 'binarySha256', 'digest']) digest(value[field], `service controller ${field}`)
  validateServiceIdentity(value.oldService, 'old service')
  validateServiceIdentity(value.newService, 'new service')
  assert(value.oldService.pid === arm.oldService.pid &&
    value.oldService.startIdentitySha256 === arm.oldService.startIdentitySha256 &&
    value.oldService.pid !== value.newService.pid &&
    value.oldService.startIdentitySha256 !== value.newService.startIdentitySha256,
  'service controller old/new identity drifted')
  for (const service of [value.oldService, value.newService]) assert(
    service.binarySha256 === value.binarySha256 && service.tupleIdentity === value.tupleIdentity &&
    service.generationId === value.generationId, 'service controller service candidate binding drifted')
  exactKeys(value.termination, ['requestedSignal', 'observedSignal', 'exitCode',
    'processGroupDead', 'completedAt'], 'service controller termination')
  assert(value.termination.requestedSignal === 'SIGKILL' &&
    value.termination.observedSignal === 'SIGKILL' && value.termination.exitCode === null &&
    value.termination.processGroupDead === true && validTimestamp(value.termination.completedAt),
  'service controller does not prove exact SIGKILL/group death')
  assert(value.newService.readiness === 'ready' && value.newService.currentGeneration === true,
    'service controller new service is not ready/current')
  exactKeys(value.restartReceipt, ['file', 'rawSha256', 'semanticDigest'],
    'service controller restart receipt')
  assert(typeof value.restartReceipt.file === 'string' && basename(value.restartReceipt.file) === value.restartReceipt.file,
    'service controller restart receipt filename is invalid')
  digest(value.restartReceipt.rawSha256, 'service controller restart receipt raw digest')
  digest(value.restartReceipt.semanticDigest, 'service controller restart receipt semantic digest')
  if (pending) assert(pending.pid !== value.oldService.pid && pending.pid !== value.newService.pid,
    'service controller PID was misregistered as a product service')
  const { digest: actual, ...draft } = value
  assert(actual === sha256(canonicalJSONStringify(draft)), 'service controller proof digest drifted')
}

function validateDisclosureExporterCore(value, request, requestSha256) {
  exactKeys(value, ['schemaVersion', 'status', 'protocol', 'runnerModuleSha256',
    'requestSha256', 'networkDisabled', 'applyBinding', 'artifacts', 'databaseSnapshot',
    'completedAt', 'coreDigest'], 'disclosure exporter core acknowledgement')
  assert(value.schemaVersion === 'chora.m1-o4-disclosure-export-core.v1' &&
    value.status === 'completed' && value.protocol === request.protocol &&
    value.runnerModuleSha256 === request.runnerModuleSha256 &&
    value.requestSha256 === requestSha256 && value.networkDisabled === true &&
    canonicalJSONStringify(value.applyBinding) === canonicalJSONStringify(request.applyBinding) &&
    validTimestamp(value.completedAt), 'disclosure exporter core binding drifted')
  assert(Array.isArray(value.artifacts) && value.artifacts.length === 4,
    'disclosure exporter core artifact count drifted')
  const contracts = [
    ['execution.log', 'log', 'apply_attempt_stdout_stderr', 16 * 1024 * 1024],
    ['state.db', 'database', 'sqlite_online_backup', 64 * 1024 * 1024],
    ['change.patch', 'patch', 'accepted_applied_patch', 16 * 1024 * 1024],
    ['payload.json', 'json', 'apply_response_body', 4 * 1024 * 1024],
  ]
  for (let index = 0; index < contracts.length; index++) {
    const artifact = value.artifacts[index]
    exactKeys(artifact, ['name', 'kind', 'sourceKind', 'bytes', 'rawSha256'],
      `disclosure exporter core artifact ${index + 1}`)
    const [name, kind, sourceKind, max] = contracts[index]
    assert(artifact.name === name && artifact.kind === kind && artifact.sourceKind === sourceKind &&
      Number.isSafeInteger(artifact.bytes) && artifact.bytes > 0 && artifact.bytes <= max,
    `disclosure exporter core artifact ${index + 1} drifted`)
    digest(artifact.rawSha256, `disclosure exporter core artifact ${index + 1}`)
  }
  assert(value.artifacts[2].rawSha256 === request.applyBinding.patchDigest &&
    value.artifacts[3].rawSha256 === request.applyBinding.payloadSha256,
  'disclosure exporter core public digest binding drifted')
  exactKeys(value.databaseSnapshot, ['method', 'sourceIdentitySha256', 'backupIdentitySha256',
    'sourceAndBackupInodesDistinct', 'walConsistent'], 'disclosure exporter core database snapshot')
  assert(value.databaseSnapshot.method === 'sqlite_online_backup' &&
    value.databaseSnapshot.sourceAndBackupInodesDistinct === true &&
    value.databaseSnapshot.walConsistent === true &&
    value.databaseSnapshot.sourceIdentitySha256 !== value.databaseSnapshot.backupIdentitySha256,
  'disclosure exporter core SQLite snapshot drifted')
  digest(value.databaseSnapshot.sourceIdentitySha256, 'disclosure exporter core source identity')
  digest(value.databaseSnapshot.backupIdentitySha256, 'disclosure exporter core backup identity')
  const { coreDigest, ...draft } = value
  assert(coreDigest === sha256(canonicalJSONStringify(draft)),
    'disclosure exporter core digest drifted')
}

function validateServiceIdentity(value, label) {
  exactKeys(value, ['pid', 'startIdentitySha256', 'binarySha256', 'tupleIdentity',
    'generationId', 'readiness', 'currentGeneration'], label)
  assert(Number.isSafeInteger(value.pid) && value.pid > 1, `${label} PID is invalid`)
  for (const field of ['startIdentitySha256', 'binarySha256', 'tupleIdentity']) digest(value[field], `${label} ${field}`)
  assert(/^(?:ins|gen)_[A-Za-z0-9_-]{24,80}$/.test(value.generationId), `${label} generation ID is invalid`)
  assert(['ready', 'terminated'].includes(value.readiness) && typeof value.currentGeneration === 'boolean',
    `${label} readiness/current-generation is invalid`)
}

function validateServeBinding(spec, binding) {
  assert(spec.argv[0] === 'serve', 'Runner start must execute the exact serve command')
  const uniqueValue = (flag) => {
    const indexes = spec.argv.flatMap((item, index) => item === flag ? [index] : [])
    assert(indexes.length === 1 && indexes[0] + 1 < spec.argv.length, `serve ${flag} binding drifted`)
    return spec.argv[indexes[0] + 1]
  }
  const port = Number(uniqueValue('--port'))
  const generationId = uniqueValue('--generation')
  assert(uniqueValue('--active-generation') === generationId &&
    generationId === binding.authority.identity.generationId, 'serve current generation binding drifted')
  assert(spec.file === binding.candidateConfig.paths.installedBinaryFile,
    'serve installed binary path drifted')
  assert(Number.isSafeInteger(port) && port >= 1024 && port <= 65535, 'serve loopback port is invalid')
  return { port, generationId }
}

async function awaitLoopbackReadiness(port, generationId, tupleIdentity, timeoutMs, spec) {
  assert(spec.argv.includes(generationId), 'serve readiness generation binding drifted')
  const deadline = Date.now() + timeoutMs
  while (true) {
    try {
      const value = await loopbackResidue(port, Math.min(500, Math.max(25, deadline - Date.now())))
      if (value.schemaVersion === 'chora.m1-o4-residue-proof.v1' && value.status === 'proven_zero' &&
        value.tupleIdentity === tupleIdentity && value.generationReferences?.serving?.count === 1 &&
        value.generationReferences.currentGenerationSha256 === sha256(generationId)) return
    } catch {}
    if (Date.now() >= deadline) throw new Error('installed service loopback readiness timed out')
    await delay(25)
  }
}

function loopbackResidue(port, timeoutMs) {
  return new Promise((resolvePromise, reject) => {
    const request = httpRequest({ host: '127.0.0.1', port, path: '/api/o4/residue', method: 'GET', timeout: timeoutMs },
      (response) => {
        let bytes = Buffer.alloc(0)
        response.on('data', (chunk) => {
          bytes = Buffer.concat([bytes, chunk])
          if (bytes.length > MAX_JSON) response.destroy(new Error('loopback residue response is oversized'))
        })
        response.once('end', () => {
          if (response.statusCode !== 200) reject(new Error('loopback residue status is not 200'))
          else {
            try { resolvePromise(parseJSON(bytes, 'loopback residue response')) }
            catch (error) { reject(error) }
          }
        })
      })
    request.once('timeout', () => request.destroy(new Error('loopback request timed out')))
    request.once('error', reject)
    request.end()
  })
}

async function validate0500Executable(path, expected, label) {
  const { bytes } = await stableOwnerFile(path, label, 256 * 1024 * 1024, 0o500)
  if (expected !== undefined) assert(sha256(bytes) === expected, `${label} digest drifted`)
  return bytes
}

function assertProductID(value, label) {
  const prefix = label === 'Task' ? 'task' : label === 'Run' ? 'run' : 'attempt'
  assert(new RegExp(`^${prefix}_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).test(value),
    `${label} ID is invalid`)
}

function processGroupExists(pid) {
  try { process.kill(-pid, 0); return true }
  catch (error) { if (error.code === 'ESRCH') return false; throw error }
}

function killProcessGroup(pid, signal) {
  try { process.kill(-pid, signal) }
  catch (error) { if (error.code !== 'ESRCH') throw error }
}

async function waitForProcessGroupDeathOrTimeout(pid, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  while (processGroupExists(pid)) {
    if (Date.now() >= deadline) return false
    await delay(10)
  }
  return true
}

async function waitForProcessGroupDeath(pid, timeoutMs, label) {
  assert(await waitForProcessGroupDeathOrTimeout(pid, timeoutMs), `${label} process group did not reach ESRCH`)
}

async function syncDirectory(path) {
  const handle = await open(path, 'r')
  try { await handle.sync() } finally { await handle.close() }
}

async function writeExclusive0400JSON(path, value, label, capability) {
  exactAbsolute(path, label)
  await assertOwnerDirectory(dirname(path))
  const bytes = Buffer.from(`${JSON.stringify(value, null, 2)}\n`)
  return writeExclusive(bytes, path, 0o400, capability)
}

function validTimestamp(value) { return typeof value === 'string' && Number.isFinite(Date.parse(value)) }
function sameFileIdentity(left, right) {
  return left.dev === right.dev && left.ino === right.ino && left.mode === right.mode &&
    left.nlink === right.nlink && left.uid === right.uid && left.size === right.size
}

function sameBigintFileIdentity(left, right) {
  return left.dev === right.dev && left.ino === right.ino && left.mode === right.mode &&
    left.nlink === right.nlink && left.uid === right.uid && left.gid === right.gid &&
    left.size === right.size && left.mtimeNs === right.mtimeNs && left.ctimeNs === right.ctimeNs
}

async function cleanupOptionalRegularFile(path, capability, label, knownIdentity = undefined) {
  let identity = knownIdentity
  if (identity === undefined) {
    let info
    try { info = await lstat(path, { bigint: true }) }
    catch (error) { if (error.code === 'ENOENT') return; throw error }
    assert(info.isFile() && !info.isSymbolicLink() && info.nlink === 1n &&
      info.uid === BigInt(currentUID()), `${label} is not an exact owned regular file`)
    identity = ownedPathIdentity(info)
  }
  await cleanupOwnedPath({ capability, path, identity, disposition: 'regular_file' })
}

async function ownedRegularFileIdentity(path, label) {
  const info = await lstat(path, { bigint: true })
  assert(info.isFile() && !info.isSymbolicLink() && info.nlink === 1n &&
    info.uid === BigInt(currentUID()) && (info.mode & 0o022n) === 0n,
  `${label} is not an exact safe owned regular file`)
  return ownedPathIdentity(info)
}

async function requireAbsentOrRetained(path, label) {
  try { await lstat(path, { bigint: true }) }
  catch (error) { if (error.code === 'ENOENT') return; throw error }
  throw new Error(`atomic cleanup unavailable for ${label}; residue retained`)
}

function sameOwnedPathIdentity(left, right) {
  return left !== undefined && right !== undefined && left.mode === right.mode &&
    left.uid === right.uid && left.gid === right.gid && left.dev === right.dev &&
    left.ino === right.ino
}

async function collectCleanupErrors(tasks) {
  const errors = []
  for (const task of tasks) {
    try { await task() } catch (error) { errors.push(error) }
  }
  return errors
}

async function stopRegistered(value, timeoutMs) {
  assert(Number.isSafeInteger(timeoutMs) && timeoutMs >= 50, 'process-group close timeout is invalid')
  if (value.synthetic === true) {
    assert(value.child === null && value.pid === null && value.dead === false,
      'synthetic lifecycle state drifted')
    value.dead = true
    return
  }
  const startedAt = Date.now()
  if (processGroupExists(value.pid)) {
    killProcessGroup(value.pid, 'SIGTERM')
    const gracefulMs = Math.max(25, Math.floor(timeoutMs / 2))
    const graceful = await waitForProcessGroupDeathOrTimeout(value.pid, gracefulMs)
    if (!graceful) killProcessGroup(value.pid, 'SIGKILL')
  }
  const remaining = Math.max(1, timeoutMs - (Date.now() - startedAt))
  await waitForProcessGroupDeath(value.pid, remaining, 'registered process')
  if (value.child && value.child.exitCode === null && value.child.signalCode === null) {
    await Promise.race([waitExit(value.child), delay(remaining).then(() => {
      throw new Error('registered process leader exit was not observed')
    })])
  }
}

function waitSpawn(child, label) {
  return new Promise((resolvePromise, reject) => {
    child.once('spawn', resolvePromise)
    child.once('error', (error) => reject(new Error(`${label} failed to spawn`, { cause: error })))
  })
}

function waitExit(child, timeoutMs = undefined) {
  if (child.exitCode !== null || child.signalCode !== null) {
    return Promise.resolve({ code: child.exitCode, signal: child.signalCode })
  }
  const exit = new Promise((resolvePromise, reject) => {
    child.once('error', reject)
    child.once('exit', (code, signal) => resolvePromise({ code, signal }))
  })
  if (timeoutMs === undefined) return exit
  return Promise.race([exit, delay(timeoutMs).then(() => { throw new Error('Runner child timed out') })])
}

function assertCommandSpec(value, label) {
  assertPlain(value, label)
  exactKeys(value, ['file', 'argv', 'shell', 'maxOutputBytes'], label)
  exactAbsolute(value.file, `${label} executable`)
  assert(value.shell === false && Array.isArray(value.argv) &&
    value.argv.every((item) => typeof item === 'string' && !item.includes('\0')),
  `${label} is not a shell-free argv command`)
}

function exactView(view, value) {
  exactKeys(value, viewMethods[view], `${view} Runner view`)
  for (const name of viewMethods[view]) assert(typeof value[name] === 'function', `${view} Runner ${name} is missing`)
  return Object.freeze(value)
}

function encode(value) {
  if (Buffer.isBuffer(value)) return { __o4Buffer: value.toString('base64') }
  if (Array.isArray(value)) return value.map(encode)
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, encode(item)]))
  return value
}

function decode(value) {
  if (Array.isArray(value)) return value.map(decode)
  if (value && typeof value === 'object') {
    if (Object.keys(value).length === 1 && typeof value.__o4Buffer === 'string') return Buffer.from(value.__o4Buffer, 'base64')
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, decode(item)]))
  }
  return value
}

async function pinnedFile(path, expected, label, max, mode = undefined) {
  const bytes = mode === 0o400 ? await read0400(path, label, max) : await readOwnerFile(path, label, max)
  assert(sha256(bytes) === expected, `${label} digest drifted`)
  return bytes
}

async function read0400JSON(path, label, max) { return parseJSON(await read0400(path, label, max), label) }
async function readJSONAnyMode(path, label, max) { return parseJSON(await readOwnerFile(path, label, max), label) }
async function read0400(path, label, max) {
  return (await stableOwnerFile(path, label, max, 0o400)).bytes
}
async function readOwnerFile(path, label, max) {
  return (await stableOwnerFile(path, label, max)).bytes
}
async function stableOwnerFile(path, label, max, expectedMode = undefined) {
  exactAbsolute(path, label)
  await assertNoSymlinkAncestors(path)
  const handle = await open(path, 'r')
  try {
    const opened = await handle.stat()
    assert(opened.isFile() && opened.nlink === 1 && opened.uid === currentUID() &&
      opened.size > 0 && opened.size <= max, `${label} is not one bounded owner file`)
    if (expectedMode !== undefined) assert((opened.mode & 0o777) === expectedMode,
      expectedMode === 0o500 ? `${label} must be exact owner-only executable 0500` :
        expectedMode === 0o400 ? `${label} must be exact owner-only immutable 0400` :
          `${label} mode drifted`)
    const bytes = await handle.readFile()
    const afterRead = await handle.stat()
    assert(sameFileIdentity(opened, afterRead) && bytes.length === opened.size,
      `${label} changed while reading its opened inode`)
    const hook = globalThis[Symbol.for('chora.o4.syntheticPinReadHook')]
    if (syntheticPinHooksEnabled && typeof hook === 'function') await hook(path, label)
    const reopened = await lstat(path)
    assert(reopened.isFile() && !reopened.isSymbolicLink() &&
      sameFileIdentity(opened, reopened), `${label} path identity changed after read`)
    const identityInfo = await handle.stat({ bigint: true })
    const pathIdentityInfo = await lstat(path, { bigint: true })
    assert(sameBigintFileIdentity(identityInfo, pathIdentityInfo),
      `${label} bigint identity changed after read`)
    return { bytes, info: opened, identity: ownedPathIdentity(identityInfo) }
  } finally { await handle.close() }
}

async function writeExclusive(bytes, path, mode, capability) {
  validateOwnedPathCapability(capability, 'exclusive-write cleanup capability')
  let handle; let created = false; let identity
  try {
    handle = await open(path, 'wx', 0o600); created = true
    await handle.writeFile(bytes); await handle.sync(); await handle.chmod(mode); await handle.sync()
    identity = ownedPathIdentity(await handle.stat({ bigint: true }))
    await handle.close(); handle = undefined
    await syncDirectory(dirname(path))
    return identity
  } catch (error) {
    const cleanupErrors = []
    if (handle) {
      if (identity === undefined) {
        try { identity = ownedPathIdentity(await handle.stat({ bigint: true })) } catch (identityError) {
          cleanupErrors.push(new Error('exclusive-write atomic cleanup identity unavailable',
            { cause: identityError }))
        }
      }
      try { await handle.close() } catch (closeError) { cleanupErrors.push(closeError) }
    }
    if (created && identity !== undefined) {
      try {
        await cleanupOwnedPath({ capability, path, identity, disposition: 'regular_file' })
      } catch (cleanupError) { cleanupErrors.push(cleanupError) }
    }
    if (cleanupErrors.length > 0) {
      throw new AggregateError([error, ...cleanupErrors],
        'exclusive write and identity-bound cleanup failed')
    }
    throw error
  }
}

async function ownedRootIdentity(path, label) {
  exactAbsolute(path, label)
  await assertNoSymlinkAncestors(path)
  const info = await lstat(path, { bigint: true })
  assert(info.isDirectory() && !info.isSymbolicLink() && info.uid === BigInt(currentUID()) &&
    (info.mode & 0o777n) === 0o700n, `${label} is not an owner-only real 0700 directory`)
  return ownedPathIdentity(info)
}

async function verifyInstalledIdentity(spec) {
  await ownedRootIdentity(spec.installRoot, 'installed product root')
  const sourceInfo = await lstat(spec.installedSourceDirectory)
  assert(sourceInfo.isDirectory() && !sourceInfo.isSymbolicLink() &&
    sourceInfo.uid === currentUID() && (sourceInfo.mode & 0o777) === 0o755,
  'installed source directory is not exact 0755')
  const installedManifest = await stableOwnerFile(spec.installedSourceManifestFile,
    'installed source manifest', MAX_SOURCE_MANIFEST, 0o444)
  assert(installedManifest.bytes.equals(spec.sourceManifestBytes),
    'installed source manifest identity drifted')
  const marker = parseJSON((await stableOwnerFile(spec.installationFile,
    'installation identity', MAX_JSON, 0o444)).bytes, 'installation identity')
  exactKeys(marker, ['schema_version', 'aggregate_sha256', 'files'], 'installation identity')
  assert(marker.schema_version === INSTALLATION_SCHEMA &&
    marker.aggregate_sha256 === spec.sourceAggregateSha256 && marker.files === spec.sourceFiles,
  'installation identity drifted')
}

async function verifyDataIdentity(path, spec) {
  await ownedRootIdentity(spec.dataRoot, 'installed data root')
  const marker = parseJSON((await stableOwnerFile(path,
    'data installation identity', MAX_JSON, 0o444)).bytes, 'data installation identity')
  exactKeys(marker, ['schema_version', 'aggregate_sha256', 'install_root'],
    'data installation identity')
  assert(marker.schema_version === DATA_INSTALLATION_SCHEMA &&
    marker.aggregate_sha256 === spec.sourceAggregateSha256 &&
    marker.install_root === spec.installRoot, 'data installation identity drifted')
}

async function removeCreatedRoot(path, identity, label, capability) {
  try {
    await cleanupOwnedPath({ capability, path, identity, disposition: 'recursive_directory' })
  } catch (error) {
    throw new Error(`${label} identity-bound cleanup failed`, { cause: error })
  }
}

async function assertOwnerDirectory(path) {
  exactAbsolute(path, 'owner directory')
  await assertNoSymlinkAncestors(path)
  const info = await lstat(path)
  assert(info.isDirectory() && !info.isSymbolicLink() && info.uid === currentUID() &&
    (info.mode & 0o777) === 0o700, 'Runner control directory is not owner-only 0700')
}

async function assertNoSymlinkAncestors(path) {
  let current = resolve(path)
  while (true) {
    try { const info = await lstat(current); assert(!info.isSymbolicLink(), 'path has a symlink ancestor') }
    catch (error) { if (error.code !== 'ENOENT') throw error }
    const parent = dirname(current); if (parent === current) return; current = parent
  }
}

async function absent(path, label) {
  try { await lstat(path); throw new Error(`${label} already exists`) }
  catch (error) { if (error.code !== 'ENOENT') throw error }
}

function hmac(secret, value) { return createHmac('sha256', secret).update(canonicalJSONStringify(value)).digest('hex') }
function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function digest(value, label) { assert(typeof value === 'string' && digestRE.test(value), `${label} is invalid`) }
function currentUID() { assert(typeof process.getuid === 'function', 'Runner requires a POSIX owner identity'); return process.getuid() }
function exactAbsolute(value, label) { assert(typeof value === 'string' && isAbsolute(value) && normalize(value) === value && resolve(value) === value && !value.includes('\0'), `${label} must be exact absolute`); return value }
function pathWithin(parent, child) { const value = relative(parent, child); return value !== '' && value !== '..' && !value.startsWith(`..${sep}`) && !isAbsolute(value) }
function pathsOverlap(left, right) { return left === right || pathWithin(left, right) || pathWithin(right, left) }
function exactKeys(value, keys, label) { assertPlain(value, label); assert(JSON.stringify(Object.keys(value).sort()) === JSON.stringify([...keys].sort()), `${label} keys drifted`) }
function assertPlain(value, label) { assert(value && typeof value === 'object' && !Array.isArray(value), `${label} is not an object`) }
function sameJSON(left, right, label) { assert(canonicalJSONStringify(left) === canonicalJSONStringify(right), label) }
function parseJSON(bytes, label) { try { return JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) } }
function canonicalJSONStringify(value) { if (value === null || typeof value !== 'object') return JSON.stringify(value); if (Array.isArray(value)) return `[${value.map(canonicalJSONStringify).join(',')}]`; return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSONStringify(value[key])}`).join(',')}}` }
function delay(ms) { return new Promise((resolvePromise) => setTimeout(resolvePromise, ms)) }
function assert(condition, message) { if (!condition) throw new Error(message) }
