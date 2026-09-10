#!/usr/bin/env node
import { createHash, randomBytes } from 'node:crypto'
import { lstat as lstatNative, mkdir, open, readFile, readdir, readlink, realpath } from 'node:fs/promises'
import { basename, dirname, isAbsolute, join, normalize, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { assertO4DarwinUnixSocketPaths } from './o4-darwin-unix-sockets.mjs'
import { inspectRepositoryAuthority, verifyRepositoryAuthority } from './o4-repository-authority.mjs'
import { cleanupOwnedPath, ownedPathIdentity, publishOwnedPath } from './o4-owned-path-cleanup.mjs'

export const MATERIALIZATION_REQUEST_SCHEMA = 'chora.m1-o4-local-offline-materialization-request.v1'
export const OFFLINE_BUNDLE_EVIDENCE_SCHEMA = 'chora.m1-o4-offline-release-bundle-evidence.v1'
export const MATERIALIZATION_RECEIPT_SCHEMA = 'chora.m1-o4-local-offline-materialization-receipt.v1'
const TOTAL_SCHEMA = 'chora.m1-o4-total-input.v1'
const CANDIDATE_SCHEMA = 'chora.m1-o4-candidate-manifest.v1'
const digestRE = /^[0-9a-f]{64}$/
const imageIDRE = /^sha256:[0-9a-f]{64}$/
const bootstrapSandboxProfile = `(version 1)
(allow default)
(deny network*)
(allow network* (local unix-socket))
(allow network* (remote unix-socket))
(allow network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
`
const roles = Object.freeze(['managed_pi_runtime', 'network_boundary', 'independent_verifier', 'capability_probe'])
const uid = typeof process.getuid === 'function' ? process.getuid() : null

async function lstat(path, options = undefined) {
  const info = await lstatNative(path, { bigint: true })
  if (options?.bigint === true) return info
  return compatibleStats(info)
}

function compatibleStats(info) {
  const numeric = new Set(['mode', 'uid', 'gid', 'nlink', 'size', 'mtimeMs', 'ctimeMs'])
  return new Proxy(info, { get(target, property) {
    if (numeric.has(property)) return Number(target[property])
    const value = Reflect.get(target, property, target)
    return typeof value === 'function' ? value.bind(target) : value
  } })
}

export async function materializeO4OfflineRelease({ requestFile, requestSha256, outputRoot }, dependencies = {}) {
  exactAbsolute(requestFile, 'materialization request')
  exactAbsolute(outputRoot, 'materialization output root')
  digest(requestSha256, 'materialization request SHA-256')
  const requestBytes = await stableFile(requestFile, 'materialization request', 0o400, 16 * 1024 * 1024)
  assert(sha256(requestBytes) === requestSha256, 'materialization request SHA-256 drifted')
  const request = parseJSON(requestBytes, 'materialization request')
  validateRequest(request)
  assert(outputRoot === request.outputRoot,
    'CLI materialization output root does not match the immutable request')
  const repositoryCapture = Object.freeze({
    executableFile: request.totalManifestTemplate.serviceController.executableFile,
    executableSha256: request.totalManifestTemplate.serviceController.sha256,
  })
  const repositoryAuthority = await inspectRepositoryAuthority({
    repositoryRoot: request.totalManifestTemplate.repository.root,
    expectedCommit: request.totalManifestTemplate.repository.commit,
    expectedClosureSha256: request.totalManifestTemplate.repository.closureSha256,
    capture: repositoryCapture,
  })
  const requestIdentity = await fileIdentity(requestFile)
  const releaseManifestBytes = await stableFile(request.releaseManifestFile,
    'release manifest', request.releaseManifestMode, 16 * 1024 * 1024)
  assert(sha256(releaseManifestBytes) === request.releaseManifestSha256,
    'release manifest raw SHA-256 drifted')
  validateReleaseManifest(parseJSON(releaseManifestBytes, 'release manifest'), request)
  const releaseManifestIdentity = await fileIdentity(request.releaseManifestFile)

  const archives = []
  for (const artifact of request.artifacts) {
    const bytes = await stableFile(artifact.archiveFile, `${artifact.artifactId} Docker archive`,
      artifact.archiveMode, artifact.archiveSize)
    assert(bytes.length === artifact.archiveSize && sha256(bytes) === artifact.archiveSha256,
      `${artifact.artifactId} Docker archive bytes drifted`)
    archives.push({ artifact, identity: await fileIdentity(artifact.archiveFile) })
  }
  const pinnedSnapshots = await validatePinnedInputs(request)
  const { parent, snapshot: initialParentSnapshot } = await safeOutputParent(outputRoot)

  let staging
  let stagingIdentity; let stagingSnapshot
  try {
    staging = join(parent, `.${basename(outputRoot)}.materializing-${randomBytes(12).toString('hex')}`)
    await mkdir(staging, { mode: 0o700 })
    stagingSnapshot = await lstat(staging, { bigint: true })
    stagingIdentity = ownedPathIdentity(stagingSnapshot)
    assert(stagingSnapshot.isDirectory() && !stagingSnapshot.isSymbolicLink() &&
      (uid === null || stagingSnapshot.uid === BigInt(uid)) &&
      Number(stagingSnapshot.mode & 0o777n) === 0o700,
    'materialization staging directory is unsafe')
    const parentSnapshot = await lstat(parent)
    assert(sameDirectoryNode(initialParentSnapshot, parentSnapshot),
      'materialization output parent identity changed while staging was created')
    const evidenceFile = join(outputRoot, 'offline-bundle-evidence.json')
    const candidateManifestFile = join(outputRoot, 'candidate-manifest.json')
    const candidateConfigFile = join(outputRoot, 'candidate-config.json')
    const totalManifestFile = join(outputRoot, 'total-manifest.json')
    const receiptFile = join(outputRoot, 'materialization-receipt.json')

    const evidence = formEvidence(request)
    const evidenceSha256 = await staged0400(staging, basename(evidenceFile), evidence)
    const candidateManifest = clone(request.candidateManifestTemplate)
    rejectLegacyRegistryShape(candidateManifest)
    assert(candidateManifest.schemaVersion === CANDIDATE_SCHEMA &&
      candidateManifest.offlineBundleEvidenceSha256 === null &&
      Object.hasOwn(candidateManifest, 'repositoryCommit') &&
      Object.hasOwn(candidateManifest, 'repositoryClosureSha256') &&
      candidateManifest.repositoryCommit === null &&
      candidateManifest.repositoryClosureSha256 === null,
    'candidate manifest template schema/dynamic digest drifted')
    candidateManifest.offlineBundleEvidenceSha256 = evidenceSha256
    candidateManifest.repositoryCommit = repositoryAuthority.repositoryCommit
    candidateManifest.repositoryClosureSha256 = repositoryAuthority.repositoryClosureSha256
    delete candidateManifest.registryEvidenceSha256
    const candidateManifestSha256 = await staged0400(staging, basename(candidateManifestFile), candidateManifest)

    const candidateConfig = clone(request.candidateConfigTemplate)
    rejectLegacyRegistryShape(candidateConfig)
    assert(candidateConfig?.paths && candidateConfig?.authority &&
      candidateConfig.paths.candidateManifestFile === candidateManifestFile &&
      candidateConfig.paths.offlineBundleEvidenceFile === evidenceFile &&
      candidateConfig.authority.candidateManifestSha256 === null &&
      candidateConfig.authority.offlineBundleEvidenceSha256 === null &&
      candidateConfig.paths.repositoryRoot === repositoryAuthority.repositoryRoot &&
      candidateConfig.authority.repositoryCommit === repositoryAuthority.repositoryCommit &&
      candidateConfig.authority.repositoryClosureSha256 ===
        repositoryAuthority.repositoryClosureSha256,
    'candidate config materialized output binding drifted')
    delete candidateConfig.paths.registryEvidenceFile
    candidateConfig.authority.candidateManifestSha256 = candidateManifestSha256
    candidateConfig.authority.offlineBundleEvidenceSha256 = evidenceSha256
    delete candidateConfig.authority.registryEvidenceSha256
    const candidateConfigSha256 = await staged0400(staging, basename(candidateConfigFile), candidateConfig)

    const total = clone(request.totalManifestTemplate)
    rejectLegacyRegistryShape(total)
    assert(total.schemaVersion === TOTAL_SCHEMA && total.selfDigest === null,
      'total manifest template schema/self digest drifted')
    exactKeys(total.repository, ['root', 'commit', 'closureSha256'], 'total repository authority')
    assert(total.repository.root === repositoryAuthority.repositoryRoot &&
      total.repository.commit === repositoryAuthority.repositoryCommit &&
      total.repository.closureSha256 === repositoryAuthority.repositoryClosureSha256,
    'total repository authority drifted')
    assert(total.candidate?.configFile === candidateConfigFile && total.candidate.configSha256 === null,
      'total manifest candidate config binding does not match the immutable output root')
    total.candidate.configSha256 = candidateConfigSha256
    const d = total.phases?.D?.environment
    assert(d && typeof d === 'object' &&
      d.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE === candidateConfigFile &&
      d.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_SHA256 === null,
    'total manifest phase D candidate-config binding is missing')
    d.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_SHA256 = candidateConfigSha256
    const e = total.phases?.E?.environment
    assert(e && typeof e === 'object' &&
      e.CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE === candidateManifestFile &&
      e.CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE === evidenceFile,
    'total manifest phase E materialized output binding drifted')
    delete e.CHORA_O4_FINAL_REGISTRY_EVIDENCE_FILE
    total.selfDigest = sha256(canonical({ ...total, selfDigest: null }))
    const totalManifestSha256 = await staged0400(staging, basename(totalManifestFile), total)

    for (const { artifact, identity } of archives) {
      assert(sameIdentity(identity, await fileIdentity(artifact.archiveFile)),
        `${artifact.artifactId} Docker archive identity changed during materialization`)
    }
    assert(sameIdentity(releaseManifestIdentity, await fileIdentity(request.releaseManifestFile)),
      'release manifest identity changed during materialization')
    const receipt = {
      schemaVersion: MATERIALIZATION_RECEIPT_SCHEMA, status: 'passed', localOnly: true,
      networkUsed: false, publicationClaimed: false, dockerInvoked: false,
      requestSha256, releaseManifestSha256: request.releaseManifestSha256,
      offlineBundleEvidenceSha256: evidenceSha256,
      candidateManifestSha256, candidateConfigSha256, totalManifestSha256,
      totalManifestFile: basename(totalManifestFile),
    }
    await staged0400(staging, basename(receiptFile), receipt)
    await syncDirectory(staging)
    if (dependencies.beforeCommit) await dependencies.beforeCommit({ staging, outputRoot })
    assert(sameIdentity(requestIdentity, await fileIdentity(requestFile)),
      'materialization request identity changed during materialization')
    for (const { artifact, identity } of archives) assert(
      sameIdentity(identity, await fileIdentity(artifact.archiveFile)),
      `${artifact.artifactId} Docker archive identity changed before publication`)
    assert(sameIdentity(releaseManifestIdentity, await fileIdentity(request.releaseManifestFile)),
      'release manifest identity changed before publication')
    await revalidateSnapshots(pinnedSnapshots)
    assert(sameDirectoryIdentity(parentSnapshot, await lstat(parent)),
      'materialization output parent changed before publication')
    assert(sameOwnedDirectory(stagingSnapshot, await lstat(staging, { bigint: true })),
      'materialization staging directory identity changed before publication')
    await absent(outputRoot, 'materialization output root')
    await verifyRepositoryAuthority({ ...repositoryAuthority, capture: repositoryCapture })
    if (dependencies.afterRepositoryVerify) {
      assert(typeof dependencies.afterRepositoryVerify === 'function',
        'materialization post-repository verification hook is invalid')
      await dependencies.afterRepositoryVerify({ staging, outputRoot })
    }
    assert(sameIdentity(requestIdentity, await fileIdentity(requestFile)),
      'materialization request identity changed after repository verification')
    for (const { artifact, identity } of archives) assert(
      sameIdentity(identity, await fileIdentity(artifact.archiveFile)),
      `${artifact.artifactId} Docker archive identity changed after repository verification`)
    assert(sameIdentity(releaseManifestIdentity, await fileIdentity(request.releaseManifestFile)),
      'release manifest identity changed after repository verification')
    await revalidateSnapshots(pinnedSnapshots)
    assert(sameDirectoryIdentity(parentSnapshot, await lstat(parent)),
      'materialization output parent changed after repository verification')
    assert(sameOwnedDirectory(stagingSnapshot, await lstat(staging, { bigint: true })),
      'materialization staging directory identity changed after repository verification')
    await absent(outputRoot, 'materialization output root')
    await publishOwnedPath({ capability: repositoryCapture, sourcePath: staging,
      targetPath: outputRoot, identity: stagingIdentity, disposition: 'recursive_directory' },
    dependencies.publishOwnedPath)
    staging = undefined
    const published = await lstat(outputRoot, { bigint: true })
    assert(sameOwnedDirectory(stagingSnapshot, published),
      'materialization output publication identity drifted')
    await syncDirectory(parent)
    return Object.freeze({ ...receipt, totalManifestFile, receiptFile })
  } catch (error) {
    if (staging && stagingIdentity) {
      try {
        await cleanupOwnedPath({ capability: repositoryCapture, path: staging,
          identity: stagingIdentity, disposition: 'recursive_directory' }, dependencies.cleanupOwnedPath)
      } catch (cleanupError) {
        throw new AggregateError([error, cleanupError],
          `materialization failed: ${error.message}; identity-bound cleanup failed: ${cleanupError.message}`,
          { cause: error })
      }
    }
    throw error
  }
}

function validateRequest(value) {
  exactKeys(value, ['schemaVersion', 'status', 'identity', 'releaseId', 'releaseSpecSha256',
    'outputRoot', 'releaseManifestFile', 'releaseManifestMode', 'releaseManifestSha256', 'artifacts',
    'candidateManifestTemplate', 'candidateConfigTemplate', 'totalManifestTemplate'], 'materialization request')
  assert(value.schemaVersion === MATERIALIZATION_REQUEST_SCHEMA && value.status === 'authorized',
    'materialization request schema/status drifted')
  exactKeys(value.identity, ['environmentId', 'installId', 'generationId', 'platform'], 'materialization identity')
  exactAbsolute(value.outputRoot, 'immutable materialization output root')
  assert(typeof value.releaseId === 'string' && /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/.test(value.releaseId),
    'release ID is invalid')
  digest(value.releaseSpecSha256, 'release spec SHA-256')
  exactAbsolute(value.releaseManifestFile, 'release manifest file')
  assert(value.releaseManifestMode === 0o444, 'release manifest mode must be exact 0444')
  digest(value.releaseManifestSha256, 'release manifest SHA-256')
  assert(Array.isArray(value.artifacts) && value.artifacts.length === 2,
    'materialization requires exactly two release archives')
  let previous = ''
  const seen = []
  for (const artifact of value.artifacts) {
    exactKeys(artifact, ['artifactId', 'roles', 'policyDigests', 'archiveFile', 'archiveRelativePath',
      'archiveMode', 'archiveSize', 'archiveSha256', 'dockerConfigImageId'], 'materialization artifact')
    assert(['managed-pi-runtime', 'network-boundary'].includes(artifact.artifactId) &&
      artifact.artifactId > previous, 'materialization artifacts are unsorted or duplicated')
    previous = artifact.artifactId
    exactAbsolute(artifact.archiveFile, 'materialization archive file')
    assert(safeRelative(artifact.archiveRelativePath) && artifact.archiveMode === 0o400 &&
      Number.isSafeInteger(artifact.archiveSize) && artifact.archiveSize > 0,
    'materialization archive binding is invalid')
    digest(artifact.archiveSha256, 'materialization archive SHA-256')
    assert(imageIDRE.test(artifact.dockerConfigImageId), 'materialization Docker config image ID is invalid')
    assert(Array.isArray(artifact.roles) && artifact.roles.length > 0 && artifact.roles.every((role, index) =>
      roles.includes(role) && (index === 0 || role > artifact.roles[index - 1])),
    'materialization roles are unsorted or duplicated')
    exactKeys(artifact.policyDigests, artifact.roles, 'materialization policy digests')
    for (const role of artifact.roles) { digest(artifact.policyDigests[role], `${role} policy digest`); seen.push(role) }
  }
  assert(JSON.stringify([...seen].sort()) === JSON.stringify([...roles].sort()),
    'materialization roles are incomplete or duplicated')
  exactKeys(value.totalManifestTemplate?.repository,
    ['root', 'commit', 'closureSha256'], 'total repository authority')
  exactAbsolute(value.totalManifestTemplate.repository.root, 'total repository root')
  assert(/^[0-9a-f]{40}$/.test(value.totalManifestTemplate.repository.commit),
    'repository commit is invalid')
  digest(value.totalManifestTemplate.repository.closureSha256, 'repository closure')
  assert(value.candidateConfigTemplate?.paths?.repositoryRoot ===
      value.totalManifestTemplate.repository.root &&
    value.candidateConfigTemplate?.authority?.repositoryCommit ===
      value.totalManifestTemplate.repository.commit &&
    value.candidateConfigTemplate?.authority?.repositoryClosureSha256 ===
      value.totalManifestTemplate.repository.closureSha256,
  'candidate/total repository authority drifted')
  assert(Object.hasOwn(value.candidateManifestTemplate ?? {}, 'repositoryCommit') &&
    Object.hasOwn(value.candidateManifestTemplate ?? {}, 'repositoryClosureSha256') &&
    value.candidateManifestTemplate.repositoryCommit === null &&
    value.candidateManifestTemplate.repositoryClosureSha256 === null,
  'candidate manifest repository authority null phase drifted')
  assertMaterializedOutputPaths(value)
  assert(!overlaps(value.outputRoot, value.totalManifestTemplate?.repository?.root),
    'materialization output root overlaps repository authority')
  assertO4DarwinUnixSocketPaths(value.totalManifestTemplate)
  rejectLegacyRegistryShape(value)
}

function validateReleaseManifest(value, request) {
  exactKeys(value, ['schema_version', 'spec_sha256', 'release_id', 'platform', 'roles', 'artifacts'],
    'release manifest')
  assert(value.schema_version === 'chora.release-assets-manifest.v1' &&
    value.spec_sha256 === request.releaseSpecSha256 && value.release_id === request.releaseId,
  'release manifest top-level identity drifted')
  assert(canonical(value.platform) === canonical({ os: 'linux', architecture: 'arm64' }),
    'release manifest platform drifted')
  assert(Array.isArray(value.roles) && value.roles.length === roles.length,
    'release manifest role set is incomplete')
  const requestByRole = new Map()
  for (const artifact of request.artifacts) {
    for (const role of artifact.roles) requestByRole.set(role, { artifact, policyDigest: artifact.policyDigests[role] })
  }
  const aliasByRole = {
    managed_pi_runtime: undefined, network_boundary: undefined,
    independent_verifier: 'managed_pi_runtime', capability_probe: 'managed_pi_runtime',
  }
  for (let index = 0; index < value.roles.length; index++) {
    const role = value.roles[index]
    const name = roles[index]
    const expected = requestByRole.get(name)
    exactKeys(role, aliasByRole[name] === undefined ? ['role', 'artifact_id', 'policy'] :
      ['role', 'artifact_id', 'alias_of_role', 'policy'], 'release manifest role')
    exactKeys(role.policy, ['id', 'path', 'sha256'], 'release manifest role policy')
    assert(role.role === name && role.artifact_id === expected.artifact.artifactId &&
      role.policy.sha256 === expected.policyDigest && role.alias_of_role === aliasByRole[name],
    `release manifest role binding drifted for ${name}`)
  }
  assert(Array.isArray(value.artifacts) && value.artifacts.length === request.artifacts.length,
    'release manifest artifact set is incomplete')
  for (let index = 0; index < value.artifacts.length; index++) {
    const artifact = value.artifacts[index]
    const expected = request.artifacts[index]
    const artifactKeys = ['id', 'recipe', 'context', 'recipe_provenance', 'build_inputs',
      'entrypoint', 'license_inventory', 'sbom', 'image']
    if (artifact.runtime !== undefined) artifactKeys.push('runtime')
    exactKeys(artifact, artifactKeys, 'release manifest artifact')
    exactKeys(artifact.image, ['local_docker_config_image_id', 'archive'], 'release manifest image')
    exactKeys(artifact.image.archive, ['format', 'path', 'size', 'sha256'], 'release manifest archive')
    assert(artifact.id === expected.artifactId &&
      artifact.image.local_docker_config_image_id === expected.dockerConfigImageId &&
      artifact.image.archive.format === 'docker-archive' &&
      artifact.image.archive.path === expected.archiveRelativePath &&
      artifact.image.archive.size === expected.archiveSize &&
      artifact.image.archive.sha256 === expected.archiveSha256,
    `release manifest artifact/archive binding drifted for ${expected.artifactId}`)
    assert(expected.archiveFile === join(dirname(request.releaseManifestFile),
      ...expected.archiveRelativePath.split('/')),
    `release manifest archive path splice for ${expected.artifactId}`)
  }
}

async function validatePinnedInputs(request) {
  const total = request.totalManifestTemplate
  const snapshots = []
  validateCandidateEngineTemplate(request.candidateConfigTemplate, total)
  assert(total.engine?.limaFile === join(total.engine?.limaPrefixRoot, 'bin/limactl') &&
    total.engine?.limaWrapperFile === join(total.engine?.limaPrefixRoot, 'bin/lima') &&
    total.engine?.limaTemplatesRoot === join(total.engine?.limaPrefixRoot, 'share/lima/templates') &&
    total.engine?.limaGuestAgentFile === join(total.engine?.limaPrefixRoot,
      'share/lima/lima-guestagent.Linux-aarch64.gz') &&
    total.engine?.limaGuestAgentSize === 7_251_420 &&
    basename(total.engine?.dockerFile) === 'docker' &&
    !overlaps(total.engine.limaPrefixRoot, dirname(total.engine.dockerFile)),
  'canonical Engine executable/prefix topology drifted')
  const pins = [
    [total.runner?.moduleFile, total.runner?.moduleSha256, 0o400, 'Runner module'],
    [total.serviceController?.executableFile, total.serviceController?.sha256, 0o500, 'service controller'],
    [total.ocr?.executableFile, total.ocr?.executableSha256, 0o500, 'OCR executable'],
    [total.ocr?.engineBindingFile, total.ocr?.engineBindingSha256, 0o400, 'OCR engine binding'],
    [total.ocr?.sandboxProfileFile, total.ocr?.sandboxProfileSha256, 0o400, 'OCR sandbox profile'],
    [total.engine?.colimaFile, total.engine?.colimaSha256, 0o500, 'Colima executable'],
    [total.engine?.limaFile, total.engine?.limaSha256, 0o500, 'canonical limactl executable'],
    [total.engine?.limaWrapperFile, total.engine?.limaWrapperSha256, 0o500,
      'canonical Lima wrapper'],
    [total.engine?.limaGuestAgentFile, total.engine?.limaGuestAgentSha256, 0o400,
      'canonical Lima Linux-aarch64 guest agent'],
    [total.engine?.dockerFile, total.engine?.dockerSha256, 0o500, 'Docker executable'],
    [total.engine?.sandboxProfileFile, total.engine?.sandboxProfileSha256, 0o400,
      'fresh Engine bootstrap sandbox profile'],
    [total.playwright?.nodeFile, total.playwright?.nodeSha256, 0o500, 'Node executable'],
    [total.playwright?.cliFile, total.playwright?.cliSha256, 0o400, 'Playwright CLI'],
    ...['C', 'D', 'E'].map((phase) => [total.phases?.[phase]?.configFile,
      total.phases?.[phase]?.configSha256, 0o400, `Playwright ${phase} config`]),
  ]
  for (const [path, expected, mode, label] of pins) {
    exactAbsolute(path, label); digest(expected, `${label} SHA-256`)
    assert(sha256(await stableFile(path, label, mode, 256 * 1024 * 1024, snapshots)) === expected,
      `${label} SHA-256 drifted`)
  }
  for (const [value, label] of [
    [total.engine?.limaPrefixClosureSha256, 'canonical Lima prefix closure'],
    [total.engine?.limaInfoDigest, 'canonical Lima info authority'],
    [total.engine?.limaTemplatesClosureSha256, 'canonical Lima templates closure'],
  ]) digest(value, `${label} SHA-256`)
  const limaPrefix = await stableDirectoryClosure(total.engine.limaPrefixRoot,
    'canonical Lima prefix')
  const limaTemplates = await stableDirectoryClosure(total.engine.limaTemplatesRoot,
    'canonical Lima templates')
  const prefixFiles = limaPrefix.entries.filter((entry) => entry.type === 'file')
  const prefixDirectories = limaPrefix.entries.filter((entry) => entry.type === 'directory')
  const templateFiles = limaTemplates.entries.filter((entry) => entry.type === 'file')
  const templateDirectories = limaTemplates.entries.filter((entry) => entry.type === 'directory')
  assert(limaPrefix.rootMode === 0o700 && limaPrefix.digest === total.engine.limaPrefixClosureSha256 &&
    prefixFiles.length === 124 && prefixDirectories.length === 7 &&
    limaPrefix.totalBytes === 39_872_398 && limaTemplates.rootMode === 0o700 &&
    limaTemplates.digest === total.engine.limaTemplatesClosureSha256 &&
    templateFiles.length === 121 && templateDirectories.length === 3 &&
    limaTemplates.totalBytes === 148_059 &&
    limaPrefix.entries.every((entry) => entry.type === 'file' || entry.type === 'directory') &&
    limaTemplates.entries.every((entry) => entry.type === 'file' || entry.type === 'directory') &&
    limaPrefix.entries.every((entry) => entry.mode === (entry.type === 'directory' ? 0o700 :
      entry.path.startsWith('bin/') ? 0o500 : 0o400)) &&
    limaTemplates.entries.every((entry) => entry.mode ===
      (entry.type === 'directory' ? 0o700 : 0o400)),
  'canonical Lima portable prefix closure drifted')
  assert(prefixFiles.some((entry) => entry.path === 'bin/limactl' &&
      entry.sha256 === total.engine.limaSha256) &&
    prefixFiles.some((entry) => entry.path === 'bin/lima' &&
      entry.sha256 === total.engine.limaWrapperSha256) &&
    prefixFiles.some((entry) => entry.path === 'share/lima/lima-guestagent.Linux-aarch64.gz' &&
      entry.size === total.engine.limaGuestAgentSize &&
      entry.sha256 === total.engine.limaGuestAgentSha256) &&
    templateFiles.some((entry) => entry.path === 'default.yaml') &&
    templateFiles.some((entry) => entry.path === '_images/ubuntu.yaml') &&
    templateFiles.some((entry) => entry.path === '_default/mounts.yaml'),
  'canonical Lima portable prefix required entries drifted')
  const limaInfoTemplates = templateFiles.filter((entry) => entry.path.endsWith('.yaml'))
    .map((entry) => join(total.engine.limaTemplatesRoot, ...entry.path.split('/')))
    .sort()
  assert(limaInfoTemplates.length === 120 && new Set(limaInfoTemplates).size === 120,
    'canonical Lima info template authority is incomplete')
  const limaInfoDigest = sha256(canonical({ templates: limaInfoTemplates,
    guestAgent: total.engine.limaGuestAgentFile, hostOS: 'darwin', hostArch: 'aarch64' }))
  assert(limaInfoDigest === total.engine.limaInfoDigest,
    'canonical Lima info authority drifted')
  exactAbsolute(total.engine?.diskImageFile, 'fresh Engine disk image')
  digest(total.engine?.diskImageSha256, 'fresh Engine disk image SHA-256')
  assert(Number.isSafeInteger(total.engine?.diskImageSize) && total.engine.diskImageSize > 0 &&
    total.engine.diskImageSize <= 64 * 1024 * 1024 * 1024,
  'fresh Engine disk image size is invalid')
  const disk = await stableFileDigest(total.engine.diskImageFile, 'fresh Engine disk image',
    0o400, total.engine.diskImageSize, snapshots)
  assert(disk.sha256 === total.engine.diskImageSha256,
    'fresh Engine disk image SHA-256 drifted')
  const bootstrapProfile = await stableFile(total.engine.sandboxProfileFile,
    'fresh Engine bootstrap sandbox profile', 0o400, 16 * 1024, snapshots)
  assert(bootstrapProfile.toString('utf8') === bootstrapSandboxProfile,
    'fresh Engine bootstrap sandbox profile is not the exact local/Unix-only deny-egress policy')
  exactAbsolute(total.ocr?.sandboxExecutableFile, 'OCR sandbox executable')
  digest(total.ocr?.sandboxExecutableSha256, 'OCR sandbox executable SHA-256')
  const sandbox = await stableSystemFile(total.ocr.sandboxExecutableFile,
    'OCR sandbox executable', 0o755, 2 * 1024 * 1024, snapshots)
  assert(sha256(sandbox) === total.ocr.sandboxExecutableSha256,
    'OCR sandbox executable SHA-256 drifted')
  exactAbsolute(total.engine?.sandboxExecutableFile, 'fresh Engine sandbox executable')
  digest(total.engine?.sandboxExecutableSha256, 'fresh Engine sandbox executable SHA-256')
  const engineSandbox = await stableSystemFile(total.engine.sandboxExecutableFile,
    'fresh Engine sandbox executable', 0o755, 2 * 1024 * 1024, snapshots)
  assert(sha256(engineSandbox) === total.engine.sandboxExecutableSha256,
    'fresh Engine sandbox executable SHA-256 drifted')
  exactAbsolute(total.playwright?.packageRoot, 'Playwright package root')
  exactAbsolute(total.playwright?.browserRoot, 'Playwright browser root')
  digest(total.playwright?.packageClosureSha256, 'Playwright package closure SHA-256')
  digest(total.playwright?.browserClosureSha256, 'Playwright browser closure SHA-256')
  assert(within(total.playwright.packageRoot, total.playwright.cliFile),
    'Playwright CLI escapes the pinned package closure')
  assert(!overlaps(total.playwright.packageRoot, total.playwright.browserRoot),
    'Playwright package and browser closures overlap')
  const playwrightPackage = await stableDirectoryClosure(total.playwright.packageRoot,
    'Playwright package closure')
  const playwrightBrowser = await stableDirectoryClosure(total.playwright.browserRoot,
    'Playwright browser closure')
  assert(playwrightPackage.digest === total.playwright.packageClosureSha256,
  'Playwright package closure SHA-256 drifted')
  assert(playwrightBrowser.digest === total.playwright.browserClosureSha256,
  'Playwright browser closure SHA-256 drifted')
  snapshots.push(...limaPrefix.snapshots, ...limaTemplates.snapshots,
    ...playwrightPackage.snapshots, ...playwrightBrowser.snapshots)
  return snapshots
}

function assertMaterializedOutputPaths(request) {
  const root = request.outputRoot
  const candidate = request.candidateConfigTemplate
  const total = request.totalManifestTemplate
  assert(candidate?.paths?.candidateManifestFile === join(root, 'candidate-manifest.json') &&
    candidate.paths.offlineBundleEvidenceFile === join(root, 'offline-bundle-evidence.json') &&
    total?.candidate?.configFile === join(root, 'candidate-config.json') &&
    total.phases?.D?.environment?.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE ===
      join(root, 'candidate-config.json') &&
    total.phases?.E?.environment?.CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE ===
      join(root, 'candidate-manifest.json') &&
    total.phases?.E?.environment?.CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE ===
      join(root, 'offline-bundle-evidence.json'),
  'materialized output paths do not match the immutable output root')
}

function validateCandidateEngineTemplate(candidate, total) {
  assert(candidate?.paths && candidate?.authority && candidate?.private,
    'candidate config template fresh Engine binding is incomplete')
  assert(candidate.paths.colimaSourceFile === total.engine?.colimaFile &&
    candidate.authority.colimaSourceSha256 === total.engine?.colimaSha256,
  'candidate config template Colima binding drifted')
  assert(candidate.paths.dockerCLIFile === total.engine?.dockerFile &&
    candidate.authority.dockerCLISha256 === total.engine?.dockerSha256,
  'candidate config template Docker binding drifted')
  assert(candidate.paths.preflightFile === total.engine?.zeroImagePreflightFile,
    'candidate config template v2 preflight path drifted')
  digest(candidate.authority.limaPrefixClosureSha256,
    'candidate config template canonical Lima prefix closure')
  digest(candidate.authority.limaInfoDigest,
    'candidate config template canonical Lima info authority')
  assert(candidate.authority.limaPrefixClosureSha256 === total.engine?.limaPrefixClosureSha256 &&
    candidate.authority.limaInfoDigest === total.engine?.limaInfoDigest &&
    candidate.authority.limaSha256 === total.engine?.limaSha256 &&
    candidate.authority.diskImageSize === total.engine?.diskImageSize &&
    candidate.authority.diskImageSha256 === total.engine?.diskImageSha256 &&
    candidate.authority.endpointSocketPathSha256 === sha256(total.engine?.endpointSocketPath) &&
    candidate.authority.sandboxExecutableSha256 === total.engine?.sandboxExecutableSha256 &&
    candidate.authority.sandboxProfileSha256 === total.engine?.sandboxProfileSha256,
  'candidate config template pinned fresh Engine closure drifted')
  assert(candidate.private.colimaProfile === total.engine?.profileName &&
    candidate.authority.dockerContext === total.engine?.contextName &&
    candidate.authority.endpointDigest === total.engine?.endpointDigest,
  'candidate config template fresh Engine identity drifted')
  exactKeys(total.repository, ['root', 'commit', 'closureSha256'], 'total repository authority')
  exactAbsolute(total.repository.root, 'total repository root')
  assert(candidate.paths.repositoryRoot === total.repository.root &&
    candidate.authority.repositoryCommit === total.repository.commit &&
    candidate.authority.repositoryClosureSha256 === total.repository.closureSha256,
  'candidate/total repository authority drifted')
  assert(/^[0-9a-f]{40}$/.test(total.repository.commit), 'repository commit is invalid')
  digest(total.repository.closureSha256, 'repository closure')
}

function formEvidence(request) {
  return {
    schemaVersion: OFFLINE_BUNDLE_EVIDENCE_SCHEMA,
    generationId: request.identity.generationId,
    releaseManifestSha256: request.releaseManifestSha256,
    platform: { os: 'linux', architecture: 'arm64' },
    artifacts: request.artifacts.map((artifact) => ({
      artifactId: artifact.artifactId, roles: [...artifact.roles],
      policyDigests: clone(artifact.policyDigests),
      archive: { path: artifact.archiveRelativePath, format: 'docker-archive',
        size: artifact.archiveSize, sha256: artifact.archiveSha256 },
      expectedDockerConfigImageId: artifact.dockerConfigImageId,
    })),
  }
}

async function staged0400(staging, target, value) {
  const bytes = Buffer.from(`${JSON.stringify(value, null, 2)}\n`)
  assert(target === basename(target) && target.length > 0, 'staged output name is unsafe')
  const path = join(staging, target)
  const handle = await open(path, 'wx', 0o600)
  try { await handle.writeFile(bytes); await handle.chmod(0o400); await handle.sync() }
  finally { await handle.close() }
  return sha256(bytes)
}

async function stableFile(path, label, mode, max, snapshots = undefined) {
  exactAbsolute(path, label); await assertNoSymlinkAncestors(path)
  const before = await lstat(path)
  assert(before.isFile() && !before.isSymbolicLink() && before.nlink === 1 &&
    (uid === null || before.uid === uid) && (before.mode & 0o777) === mode &&
    before.size > 0 && before.size <= max, `${label} is not one owner-controlled immutable file`)
  const bytes = await readFile(path)
  const after = await lstat(path)
  assert(sameIdentity(before, after) && after.size === bytes.length, `${label} changed while read`)
  if (snapshots) snapshots.push({ path, snapshot: before, label })
  return bytes
}

async function stableFileDigest(path, label, mode, exactSize, snapshots = undefined) {
  exactAbsolute(path, label); await assertNoSymlinkAncestors(path)
  const handle = await open(path, 'r')
  try {
    const before = compatibleStats(await handle.stat({ bigint: true }))
    assert(before.isFile() && before.nlink === 1 && (uid === null || before.uid === uid) &&
      (before.mode & 0o777) === mode && before.size === exactSize,
    `${label} is not one exact owner-controlled immutable file`)
    const hash = createHash('sha256')
    const buffer = Buffer.allocUnsafe(1024 * 1024)
    let position = 0
    while (position < exactSize) {
      const { bytesRead } = await handle.read(buffer, 0, Math.min(buffer.length, exactSize - position), position)
      assert(bytesRead > 0, `${label} ended before its pinned size`)
      hash.update(buffer.subarray(0, bytesRead)); position += bytesRead
    }
    const after = compatibleStats(await handle.stat({ bigint: true }))
    assert(sameIdentity(before, after), `${label} changed while hashing`)
    const reopened = await lstat(path)
    assert(reopened.isFile() && !reopened.isSymbolicLink() && sameIdentity(before, reopened),
      `${label} path identity changed after hashing`)
    if (snapshots) snapshots.push({ path, snapshot: before, label })
    return { size: position, sha256: hash.digest('hex') }
  } finally { await handle.close() }
}

async function stableSystemFile(path, label, mode, max, snapshots = undefined) {
  exactAbsolute(path, label); await assertNoSymlinkAncestors(path)
  const before = await lstat(path)
  assert(before.isFile() && !before.isSymbolicLink() && before.nlink === 1 && before.uid === 0 &&
    (before.mode & 0o777) === mode && before.size > 0 && before.size <= max,
  `${label} is not one pinned root-owned system file`)
  const bytes = await readFile(path)
  const after = await lstat(path)
  assert(sameIdentity(before, after) && after.size === bytes.length, `${label} changed while read`)
  if (snapshots) snapshots.push({ path, snapshot: before, label })
  return bytes
}

async function stableDirectoryClosure(root, label) {
  exactAbsolute(root, label); await assertNoSymlinkAncestors(root)
  const rootBefore = await lstat(root)
  assert(rootBefore.isDirectory() && !rootBefore.isSymbolicLink() &&
    (uid === null || rootBefore.uid === uid) && (rootBefore.mode & 0o022) === 0,
  `${label} root is not owner-controlled and non-writable by others`)
  const entries = []
  const snapshots = new Map([[root, { path: root, snapshot: rootBefore, label }]])
  let totalBytes = 0
  const visit = async (directory, prefix) => {
    const directoryBefore = await lstat(directory)
    assert(directoryBefore.isDirectory() && !directoryBefore.isSymbolicLink() &&
      (uid === null || directoryBefore.uid === uid) && (directoryBefore.mode & 0o022) === 0,
    `${label} contains an unsafe directory`)
    const names = (await readdir(directory)).sort()
    for (const name of names) {
      assert(name.length > 0 && name !== '.' && name !== '..' && !name.includes('/') && !name.includes('\0'),
        `${label} contains an unsafe entry name`)
      const path = join(directory, name)
      const relativePath = prefix ? `${prefix}/${name}` : name
      const before = await lstat(path)
      snapshots.set(path, { path, snapshot: before, label })
      assert(uid === null || before.uid === uid, `${label} contains a foreign entry`)
      if (before.isSymbolicLink()) {
        const target = await readlink(path)
        assert(target.length > 0 && !isAbsolute(target) && !target.includes('\0'),
          `${label} symlink ${relativePath} is unsafe`)
        const resolvedTarget = resolve(dirname(path), target)
        assert(resolvedTarget === root || within(root, resolvedTarget),
          `${label} symlink ${relativePath} escapes root`)
        const canonicalTarget = await realpath(resolvedTarget)
        assert(canonicalTarget === root || within(root, canonicalTarget),
          `${label} symlink ${relativePath} resolves outside root`)
        const after = await lstat(path)
        assert(sameIdentity(before, after) && await readlink(path) === target,
          `${label} symlink changed while hashing`)
        entries.push({ path: relativePath, type: 'symlink', mode: before.mode & 0o777, target })
        assert(entries.length <= 100_000, `${label} exceeds its bounded closure`)
      } else if (before.isDirectory()) {
        assert((before.mode & 0o022) === 0, `${label} contains a group/world-writable directory`)
        entries.push({ path: relativePath, type: 'directory', mode: before.mode & 0o777 })
        assert(entries.length <= 100_000, `${label} exceeds its bounded closure`)
        await visit(path, relativePath)
      } else {
        assert(before.isFile() && before.nlink === 1 && before.size >= 0 && (before.mode & 0o022) === 0,
          `${label} contains a non-regular, multiply-linked, or group/world-writable file`)
        totalBytes += before.size
        assert(entries.length < 100_000 && totalBytes <= 2 * 1024 * 1024 * 1024,
          `${label} exceeds its bounded closure`)
        const bytes = await readFile(path)
        const after = await lstat(path)
        assert(sameIdentity(before, after) && bytes.length === before.size,
          `${label} entry changed while hashing`)
        entries.push({ path: relativePath, type: 'file', mode: before.mode & 0o777,
          size: before.size, sha256: sha256(bytes) })
      }
    }
    const directoryAfter = await lstat(directory)
    assert(sameDirectoryIdentity(directoryBefore, directoryAfter),
      `${label} directory changed while hashing`)
  }
  await visit(root, '')
  assert(entries.length > 0, `${label} is empty`)
  const rootAfter = await lstat(root)
  assert(sameDirectoryIdentity(rootBefore, rootAfter), `${label} root changed while hashing`)
  return { digest: sha256(canonical({ rootMode: rootBefore.mode & 0o777, entries })),
    rootMode: rootBefore.mode & 0o777, entries, entryCount: entries.length, totalBytes,
    snapshots: [...snapshots.values()] }
}

async function fileIdentity(path) { return lstat(path) }
function sameIdentity(a, b) { return a.dev === b.dev && a.ino === b.ino && a.size === b.size && a.mtimeMs === b.mtimeMs && a.ctimeMs === b.ctimeMs && a.mode === b.mode && a.uid === b.uid && a.nlink === b.nlink }
function sameDirectoryIdentity(a, b) { return a.dev === b.dev && a.ino === b.ino && a.mode === b.mode && a.uid === b.uid && a.mtimeMs === b.mtimeMs && a.ctimeMs === b.ctimeMs }
function sameDirectoryNode(a, b) { return b.isDirectory() && !b.isSymbolicLink() && a.dev === b.dev && a.ino === b.ino && a.mode === b.mode && a.uid === b.uid }
function sameOwnedDirectory(a, b) {
  const owner = typeof b.uid === 'bigint' ? Number(b.uid) : b.uid
  const mode = typeof b.mode === 'bigint' ? Number(b.mode & 0o777n) : b.mode & 0o777
  return sameDirectoryNode(a, b) && (uid === null || owner === uid) && mode === 0o700
}
async function assertNoSymlinkAncestors(path) { let current = path; while (true) { try { assert(!(await lstat(current)).isSymbolicLink(), 'path has symlink ancestor') } catch (error) { if (error.code !== 'ENOENT') throw error } const parent = dirname(current); if (parent === current) break; current = parent } }
async function absent(path, label) { try { await lstat(path); throw new Error(`${label} already exists`) } catch (error) { if (error.message === `${label} already exists`) throw error; if (error.code !== 'ENOENT') throw error } }
async function safeOutputParent(outputRoot) {
  await assertNoSymlinkAncestors(outputRoot)
  await absent(outputRoot, 'materialization output root')
  const parent = dirname(outputRoot)
  const snapshot = await lstat(parent)
  assert(snapshot.isDirectory() && !snapshot.isSymbolicLink() &&
    (uid === null || snapshot.uid === uid) && (snapshot.mode & 0o022) === 0,
  'materialization output parent must be owner-controlled and non-writable by group/world')
  return { parent, snapshot }
}
async function syncDirectory(path) {
  const handle = await open(path, 'r')
  try { await handle.sync() } finally { await handle.close() }
}
async function revalidateSnapshots(snapshots) {
  for (const { path, snapshot, label } of snapshots) assert(
    sameIdentity(snapshot, await lstat(path)), `${label} input TOCTOU drifted: ${path}`)
}
function rejectLegacyRegistryShape(value, key = '') { if (Array.isArray(value)) return value.forEach((item) => rejectLegacyRegistryShape(item, key)); if (!value || typeof value !== 'object') return; for (const [name, item] of Object.entries(value)) { assert(!/(?:registry|ociReference|imageDigest|configDigest)/i.test(name), `legacy Registry field ${name} is forbidden`); rejectLegacyRegistryShape(item, name) } }
function safeRelative(value) { return typeof value === 'string' && value.length > 0 && !isAbsolute(value) && normalize(value) === value && !value.split(/[\\/]/).includes('..') && value.split(sep).join('/') === value }
function within(parent, child) { const rel = relative(parent, child); return rel !== '' && rel !== '..' && !rel.startsWith(`..${sep}`) && !isAbsolute(rel) }
function overlaps(left, right) { return left === right || within(left, right) || within(right, left) }
function exactAbsolute(value, label) { assert(typeof value === 'string' && isAbsolute(value) && normalize(value) === value && resolve(value) === value && !value.includes('\0'), `${label} must be exact absolute`); return value }
function exactKeys(value, keys, label) { assert(value && typeof value === 'object' && !Array.isArray(value) && JSON.stringify(Object.keys(value).sort()) === JSON.stringify([...keys].sort()), `${label} keys drifted`) }
function digest(value, label) { assert(typeof value === 'string' && digestRE.test(value), `${label} is invalid`) }
function parseJSON(bytes, label) { try { return JSON.parse(bytes.toString('utf8')) } catch { throw new Error(`${label} is not JSON`) } }
function canonical(value) { if (value === null || typeof value !== 'object') return JSON.stringify(value); if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`; return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}` }
function sha256(value) { return createHash('sha256').update(value).digest('hex') }
function clone(value) { return structuredClone(value) }
function assert(condition, message) { if (!condition) throw new Error(message) }

async function main() {
  const args = process.argv.slice(2); const values = {}
  for (let index = 0; index < args.length; index += 2) values[args[index]] = args[index + 1]
  assert(args.length === 6 && values['--request'] && values['--request-sha256'] && values['--output-root'],
    'usage: o4-local-offline-materializer --request <0400-json> --request-sha256 <digest> --output-root <fresh>')
  const result = await materializeO4OfflineRelease({ requestFile: values['--request'],
    requestSha256: values['--request-sha256'], outputRoot: values['--output-root'] })
  process.stdout.write(`${JSON.stringify(result)}\n`)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => { process.stderr.write(`O4 local offline materialization failed: ${error.message}\n`); process.exitCode = 1 })
}
