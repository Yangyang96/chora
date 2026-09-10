import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { chmod, copyFile, link, lstat, mkdir, mkdtemp, readFile, readdir, readlink,
  realpath, rename, rm, stat, symlink, unlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'

import {
  MATERIALIZATION_REQUEST_SCHEMA,
  materializeO4OfflineRelease,
} from './o4-local-offline-materializer.mjs'
import { inspectRepositoryAuthority } from './o4-repository-authority.mjs'
import { buildTestRepositoryCapture } from './o4-repository-capture-test-helper.mjs'

const sha256 = (value) => createHash('sha256').update(value).digest('hex')
const execFileAsync = promisify(execFile)
const image = (label) => `sha256:${sha256(label)}`
const LIMA_SOURCE_ROOT = '/opt/homebrew/Cellar/lima/2.1.4'
const LIMACTL_SHA256 = '3957f467a116b4adb093cecef7af320b9d4ddec81eb95b613b95ad3c76821320'
const LIMA_WRAPPER_SHA256 = '88aeac60dbcb69ec675c0ce9af24d5f66255f4899fa73320402db57925500832'
const LIMA_GUEST_AGENT_SHA256 = 'f515357036e1b3bc9777c06a35fef60f07242e5ae67a9bd272345a32c5511900'
let captureBuild

test.before(async () => { captureBuild = await buildTestRepositoryCapture() })
test.after(async () => { await captureBuild?.cleanup() })

async function pinned(path, bytes, mode) {
  await writeFile(path, bytes, { mode })
  return sha256(bytes)
}

async function repositoryAuthority(root, capture) {
  const repositoryRoot = join(root, 'repository')
  await mkdir(repositoryRoot, { mode: 0o700 })
  await pinned(join(repositoryRoot, 'README.md'), 'synthetic repository\n', 0o644)
  const git = async (...args) => execFileAsync('/usr/bin/git', ['-C', repositoryRoot, ...args], {
    env: { PATH: '/usr/bin:/bin', HOME: root, GIT_CONFIG_NOSYSTEM: '1',
      GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' },
  })
  await git('init', '-q', '-b', 'main')
  await git('add', '--', 'README.md')
  await git('-c', 'user.name=O4 Synthetic', '-c', 'user.email=chora@example.test',
    '-c', 'commit.gpgSign=false', 'commit', '-q', '-m', 'synthetic authority')
  return inspectRepositoryAuthority({ repositoryRoot, capture })
}

async function directoryClosureSha256(root) {
  const rootInfo = await lstat(root)
  const entries = []
  const visit = async (directory, prefix) => {
    for (const name of (await readdir(directory)).sort()) {
      const path = join(directory, name)
      const relativePath = prefix ? `${prefix}/${name}` : name
      const info = await lstat(path)
      if (info.isSymbolicLink()) {
        entries.push({ path: relativePath, type: 'symlink', mode: info.mode & 0o777,
          target: await readlink(path) })
      } else if (info.isDirectory()) {
        entries.push({ path: relativePath, type: 'directory', mode: info.mode & 0o777 })
        await visit(path, relativePath)
      } else {
        const bytes = await readFile(path)
        entries.push({ path: relativePath, type: 'file', mode: info.mode & 0o777,
          size: info.size, sha256: sha256(bytes) })
      }
    }
  }
  await visit(root, '')
  return sha256(canonical({ rootMode: rootInfo.mode & 0o777, entries }))
}

async function portableLimaPrefix(root) {
  const prefixRoot = join(root, 'lima-prefix'); const binRoot = join(prefixRoot, 'bin')
  const shareRoot = join(prefixRoot, 'share'); const limaShareRoot = join(shareRoot, 'lima')
  const templatesRoot = join(limaShareRoot, 'templates')
  for (const path of [prefixRoot, binRoot, shareRoot, limaShareRoot, templatesRoot,
    join(templatesRoot, '_images'), join(templatesRoot, '_default'), join(templatesRoot, 'extras')]) {
    await mkdir(path, { mode: 0o700 }); await chmod(path, 0o700)
  }
  const limactlFile = join(binRoot, 'limactl'); const limaFile = join(binRoot, 'lima')
  const guestAgentFile = join(limaShareRoot, 'lima-guestagent.Linux-aarch64.gz')
  await copyFile(join(LIMA_SOURCE_ROOT, 'bin/limactl'), limactlFile); await chmod(limactlFile, 0o500)
  await copyFile(join(LIMA_SOURCE_ROOT, 'bin/lima'), limaFile); await chmod(limaFile, 0o500)
  await copyFile(join(LIMA_SOURCE_ROOT, 'share/lima/lima-guestagent.Linux-aarch64.gz'),
    guestAgentFile); await chmod(guestAgentFile, 0o400)
  const required = [['README.md', 'README\n'], ['default.yaml', 'default\n'],
    ['_images/ubuntu.yaml', 'ubuntu\n'], ['_default/mounts.yaml', 'mounts\n']]
  const templateLocations = required.filter(([relative]) => relative.endsWith('.yaml'))
    .map(([relative]) => join(templatesRoot, relative))
  let templateBytes = 0
  for (const [relative, bytes] of required) {
    templateBytes += Buffer.byteLength(bytes); await pinned(join(templatesRoot, relative), bytes, 0o400)
  }
  for (let index = 0; index < 116; index++) {
    const path = join(templatesRoot, 'extras',
      `asset-${String(index).padStart(3, '0')}.yaml`)
    templateLocations.push(path); templateBytes += 1; await pinned(path, 'x', 0o400)
  }
  const paddingSize = 148_059 - templateBytes
  assert.equal(required.length + 116 + 1, 121); assert.equal(templateBytes + paddingSize, 148_059)
  const paddingFile = join(templatesRoot, 'extras/padding.yaml')
  templateLocations.push(paddingFile)
  await pinned(paddingFile, Buffer.alloc(paddingSize, 0x70), 0o400)
  const limactlSha256 = sha256(await readFile(limactlFile)); const limaSha256 = sha256(await readFile(limaFile))
  const guestAgentSha256 = sha256(await readFile(guestAgentFile)); const guestAgentSize = (await lstat(guestAgentFile)).size
  assert.equal(limactlSha256, LIMACTL_SHA256); assert.equal(limaSha256, LIMA_WRAPPER_SHA256)
  assert.notEqual(limactlSha256, limaSha256); assert.equal(guestAgentSha256, LIMA_GUEST_AGENT_SHA256)
  assert.equal(guestAgentSize, 7_251_420)
  return { prefixRoot, prefixClosureSha256: await directoryClosureSha256(prefixRoot),
    limactlFile, limactlSha256, limaFile, limaSha256, templatesRoot,
    templatesClosureSha256: await directoryClosureSha256(templatesRoot), guestAgentFile,
    guestAgentSize, guestAgentSha256, templateLocations: templateLocations.sort(),
    infoDigest: sha256(canonical({ templates: templateLocations.sort(), guestAgent: guestAgentFile,
      hostOS: 'darwin', hostArch: 'aarch64' })) }
}

function canonical(value) {
  if (value === null || typeof value !== 'object') return JSON.stringify(value)
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`
}

async function fixture() {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'chora-o4-offline-materializer-')))
  await mkdir(join(root, 'archives'))
  const engineToolRoot = join(root, 'engine-tools')
  await mkdir(engineToolRoot, { mode: 0o700 })
  const packageRoot = join(root, 'playwright-package')
  const browserRoot = join(root, 'playwright-browsers')
  await mkdir(packageRoot, { mode: 0o700 })
  await mkdir(browserRoot, { mode: 0o700 })
  const files = {}
  for (const [name, mode] of [['runner', 0o400], ['ocr', 0o500],
    ['engine', 0o400], ['profile', 0o400],
    ['node', 0o500], ['c', 0o400], ['d', 0o400], ['e', 0o400]]) {
    const path = join(root, name); files[name] = { path, sha256: await pinned(path, `${name}\n`, mode) }
  }
  files.controller = { path: join(root, 'controller'),
    sha256: await pinned(join(root, 'controller'),
      await readFile(captureBuild.capture.executableFile), 0o500) }
  assert.equal(files.controller.sha256, captureBuild.capture.executableSha256)
  files.docker = { path: join(engineToolRoot, 'docker'),
    sha256: await pinned(join(engineToolRoot, 'docker'), 'docker\n', 0o500) }
  files.colima = { path: join(engineToolRoot, 'colima'),
    sha256: await pinned(join(engineToolRoot, 'colima'), 'colima\n', 0o500) }
  const lima = await portableLimaPrefix(root)
  const bootstrapProfileBytes = `(version 1)\n(allow default)\n(deny network*)\n(allow network* (local unix-socket))\n(allow network* (remote unix-socket))\n(allow network-inbound (local ip "localhost:*"))\n(allow network-outbound (remote ip "localhost:*"))\n`
  const bootstrapProfilePath = join(root, 'bootstrap-profile.sb')
  files.bootstrapProfile = { path: bootstrapProfilePath,
    sha256: await pinned(bootstrapProfilePath, bootstrapProfileBytes, 0o400) }
  const diskImagePath = join(root, 'pinned-colima-disk.img')
  const diskImageBytes = Buffer.from('pinned-local-colima-disk-image\n')
  files.diskImage = { path: diskImagePath, size: diskImageBytes.length,
    sha256: await pinned(diskImagePath, diskImageBytes, 0o400) }
  const cliPath = join(packageRoot, 'cli.js')
  files.cli = { path: cliPath, sha256: await pinned(cliPath, 'cli\n', 0o400) }
  await pinned(join(packageRoot, 'package.json'), '{"name":"@playwright/test"}\n', 0o400)
  await pinned(join(browserRoot, 'chromium'), 'pinned-browser-closure\n', 0o500)
  const packageClosureSha256 = await directoryClosureSha256(packageRoot)
  const browserClosureSha256 = await directoryClosureSha256(browserRoot)
  const sandboxExecutableFile = '/usr/bin/sandbox-exec'
  const sandboxExecutableSha256 = sha256(await readFile(sandboxExecutableFile))
  const archive = async (artifactId, fill, roles, size) => {
    const archiveRelativePath = `archives/${artifactId}.tar`
    const archiveFile = join(root, archiveRelativePath)
    const bytes = Buffer.alloc(size, fill)
    await writeFile(archiveFile, bytes, { mode: 0o400 })
    return { artifactId, roles, policyDigests: Object.fromEntries(roles.map((role) => [role, sha256(role)])),
      archiveFile, archiveRelativePath, archiveMode: 0o400,
      archiveSize: bytes.length, archiveSha256: sha256(bytes), dockerConfigImageId: image(artifactId) }
  }
  const artifacts = [
    await archive('managed-pi-runtime', 0x6d,
      ['capability_probe', 'independent_verifier', 'managed_pi_runtime'], 101),
    await archive('network-boundary', 0x62, ['network_boundary'], 103),
  ]
  const releaseId = 'chora-m1-alpha'
  const releaseSpecSha256 = sha256('release-spec')
  const roleBinding = (role, artifactId, aliasOfRole = undefined) => ({
    role, artifact_id: artifactId, ...(aliasOfRole === undefined ? {} : { alias_of_role: aliasOfRole }),
    policy: { id: `${role.replaceAll('_', '-')}-policy`, path: `policies/${role}.json`,
      sha256: artifacts.find((artifact) => artifact.artifactId === artifactId).policyDigests[role] },
  })
  const fileBinding = (name) => ({ path: `${name}.json`, sha256: sha256(name) })
  const releaseManifest = {
    schema_version: 'chora.release-assets-manifest.v1', spec_sha256: releaseSpecSha256,
    release_id: releaseId, platform: { os: 'linux', architecture: 'arm64' },
    roles: [
      roleBinding('managed_pi_runtime', 'managed-pi-runtime'),
      roleBinding('network_boundary', 'network-boundary'),
      roleBinding('independent_verifier', 'managed-pi-runtime', 'managed_pi_runtime'),
      roleBinding('capability_probe', 'managed-pi-runtime', 'managed_pi_runtime'),
    ],
    artifacts: artifacts.map((artifact) => ({
      id: artifact.artifactId, recipe: fileBinding(`${artifact.artifactId}-recipe`),
      context: fileBinding(`${artifact.artifactId}-context`), recipe_provenance: [],
      build_inputs: [], entrypoint: ['/entrypoint'],
      license_inventory: fileBinding(`${artifact.artifactId}-licenses`),
      sbom: fileBinding(`${artifact.artifactId}-sbom`),
      image: { local_docker_config_image_id: artifact.dockerConfigImageId,
        archive: { format: 'docker-archive', path: artifact.archiveRelativePath,
          size: artifact.archiveSize, sha256: artifact.archiveSha256 } },
    })),
  }
  const releaseManifestFile = join(root, 'release-manifest.json')
  const releaseManifestBytes = Buffer.from(`${JSON.stringify(releaseManifest)}\n`)
  await writeFile(releaseManifestFile, releaseManifestBytes, { mode: 0o444 })
  const socketRoot = `/private/tmp/o4m-${sha256(root).slice(0, 16)}`
  const colimaHome = join(socketRoot, 'c')
  const endpointSocketPath = join(colimaHome, 'chora-o4-test', 'docker.sock')
  const outputRoot = join(root, 'materialized')
  const repository = await repositoryAuthority(root, {
    executableFile: files.controller.path,
    executableSha256: files.controller.sha256,
  })
  const request = {
    schemaVersion: MATERIALIZATION_REQUEST_SCHEMA, status: 'authorized',
    outputRoot,
    identity: { environmentId: `env_${'e'.repeat(32)}`, installId: `ins_${'i'.repeat(32)}`,
      generationId: `gen_${'g'.repeat(32)}`, platform: { os: 'darwin', architecture: 'arm64' } },
    releaseId, releaseSpecSha256, releaseManifestFile, releaseManifestMode: 0o444,
    releaseManifestSha256: sha256(releaseManifestBytes), artifacts,
    candidateManifestTemplate: { schemaVersion: 'chora.m1-o4-candidate-manifest.v1',
      generationId: `gen_${'g'.repeat(32)}`, offlineBundleEvidenceSha256: null,
      repositoryCommit: null, repositoryClosureSha256: null },
    candidateConfigTemplate: { paths: { candidateManifestFile: join(outputRoot, 'candidate-manifest.json'),
      offlineBundleEvidenceFile: join(outputRoot, 'offline-bundle-evidence.json'),
      colimaSourceFile: files.colima.path,
      dockerCLIFile: files.docker.path,
      repositoryRoot: repository.repositoryRoot,
      preflightFile: join(root, 'evidence', 'engine-preflight.json') }, authority: {
      candidateManifestSha256: null, offlineBundleEvidenceSha256: null,
      colimaSourceSha256: files.colima.sha256, dockerCLISha256: files.docker.sha256,
      limaPrefixClosureSha256: lima.prefixClosureSha256, limaInfoDigest: lima.infoDigest,
      limaSha256: lima.limactlSha256, diskImageSha256: files.diskImage.sha256,
      diskImageSize: files.diskImage.size,
      endpointSocketPathSha256: sha256(endpointSocketPath),
      repositoryCommit: repository.repositoryCommit,
      repositoryClosureSha256: repository.repositoryClosureSha256,
      sandboxExecutableSha256, sandboxProfileSha256: files.bootstrapProfile.sha256,
      dockerContext: 'colima-chora-o4-test',
      endpointDigest: sha256(`unix://${endpointSocketPath}`) },
      private: { colimaProfile: 'chora-o4-test' } },
    totalManifestTemplate: { schemaVersion: 'chora.m1-o4-total-input.v1', selfDigest: null,
      candidate: { configFile: join(outputRoot, 'candidate-config.json'), configSha256: null },
      repository: { root: repository.repositoryRoot, commit: repository.repositoryCommit,
        closureSha256: repository.repositoryClosureSha256 },
      runner: { moduleFile: files.runner.path, moduleSha256: files.runner.sha256,
        controlSocketPath: join(socketRoot, 'r', 'o4-runner.sock') },
      serviceController: { executableFile: files.controller.path, sha256: files.controller.sha256 },
      ocr: { executableFile: files.ocr.path, executableSha256: files.ocr.sha256,
        engineBindingFile: files.engine.path, engineBindingSha256: files.engine.sha256,
        sandboxExecutableFile, sandboxExecutableSha256,
        sandboxProfileFile: files.profile.path, sandboxProfileSha256: files.profile.sha256 },
      engine: { colimaFile: files.colima.path, colimaSha256: files.colima.sha256,
        limaPrefixRoot: lima.prefixRoot, limaPrefixClosureSha256: lima.prefixClosureSha256,
        limaInfoDigest: lima.infoDigest,
        limaFile: lima.limactlFile, limaSha256: lima.limactlSha256,
        limaWrapperFile: lima.limaFile, limaWrapperSha256: lima.limaSha256,
        limaTemplatesRoot: lima.templatesRoot,
        limaTemplatesClosureSha256: lima.templatesClosureSha256,
        limaGuestAgentFile: lima.guestAgentFile, limaGuestAgentSize: lima.guestAgentSize,
        limaGuestAgentSha256: lima.guestAgentSha256,
        dockerFile: files.docker.path, dockerSha256: files.docker.sha256,
        diskImageFile: files.diskImage.path, diskImageSize: files.diskImage.size,
        diskImageSha256: files.diskImage.sha256,
        sandboxExecutableFile, sandboxExecutableSha256,
        sandboxProfileFile: files.bootstrapProfile.path,
        sandboxProfileSha256: files.bootstrapProfile.sha256,
        profileName: 'chora-o4-test', contextName: 'colima-chora-o4-test',
        endpointSocketPath,
        endpointDigest: sha256(`unix://${endpointSocketPath}`),
        cpus: 2, memoryGiB: 4, diskGiB: 20,
        zeroImagePreflightFile: join(root, 'evidence', 'engine-preflight.json'),
        cleanupResidueFile: join(root, 'evidence', 'engine-residue.json') },
      playwright: { nodeFile: files.node.path, nodeSha256: files.node.sha256,
        cliFile: files.cli.path, cliSha256: files.cli.sha256,
        packageRoot, packageClosureSha256, browserRoot, browserClosureSha256 },
      roots: { colimaHome, temporaryRoot: join(socketRoot, 't') },
      phases: { C: { configFile: files.c.path, configSha256: files.c.sha256 },
        D: { configFile: files.d.path, configSha256: files.d.sha256, environment: {
          CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE: join(outputRoot, 'candidate-config.json'),
          CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_SHA256: null } },
        E: { configFile: files.e.path, configSha256: files.e.sha256, environment: {
          CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE: join(outputRoot, 'candidate-manifest.json'),
          CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE:
            join(outputRoot, 'offline-bundle-evidence.json') } } },
    },
  }
  const requestFile = join(root, 'request.json')
  const requestBytes = Buffer.from(`${JSON.stringify(request)}\n`)
  await writeFile(requestFile, requestBytes, { mode: 0o400 })
  return { root, request, requestFile, requestSha256: sha256(requestBytes),
    releaseManifest, releaseManifestFile,
    packageRoot, browserRoot,
    outputRoot,
    engineTools: { root: engineToolRoot, docker: files.docker.path }, lima, repository }
}

async function persistRequest(f) {
  await chmod(f.requestFile, 0o600)
  const bytes = Buffer.from(`${JSON.stringify(f.request)}\n`)
  await writeFile(f.requestFile, bytes)
  await chmod(f.requestFile, 0o400)
  f.requestSha256 = sha256(bytes)
}

async function persistReleaseManifest(f) {
  await chmod(f.releaseManifestFile, 0o600)
  const bytes = Buffer.from(`${JSON.stringify(f.releaseManifest)}\n`)
  await writeFile(f.releaseManifestFile, bytes)
  await chmod(f.releaseManifestFile, 0o444)
  f.request.releaseManifestSha256 = sha256(bytes)
  await persistRequest(f)
}

function bindOutputRoot(f, outputRoot) {
  f.outputRoot = outputRoot
  f.request.outputRoot = outputRoot
  f.request.candidateConfigTemplate.paths.candidateManifestFile =
    join(outputRoot, 'candidate-manifest.json')
  f.request.candidateConfigTemplate.paths.offlineBundleEvidenceFile =
    join(outputRoot, 'offline-bundle-evidence.json')
  f.request.totalManifestTemplate.candidate.configFile = join(outputRoot, 'candidate-config.json')
  f.request.totalManifestTemplate.phases.D.environment.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE =
    join(outputRoot, 'candidate-config.json')
  f.request.totalManifestTemplate.phases.E.environment.CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE =
    join(outputRoot, 'candidate-manifest.json')
  f.request.totalManifestTemplate.phases.E.environment.CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE =
    join(outputRoot, 'offline-bundle-evidence.json')
}

test('materializes one local-only owner-private offline closure without Docker or publication claims', async () => {
  const f = await fixture()
  assert.equal(f.request.totalManifestTemplate.engine.limaFile, f.lima.limactlFile)
  assert.equal(f.request.totalManifestTemplate.engine.limaWrapperFile, f.lima.limaFile)
  assert.equal(f.request.totalManifestTemplate.engine.dockerFile, f.engineTools.docker)
  const limaInfo = await lstat(f.lima.limaFile)
  const limactlInfo = await lstat(f.lima.limactlFile)
  for (const path of [f.lima.limaFile, f.lima.limactlFile, f.engineTools.docker]) {
    const info = await lstat(path)
    assert.equal(info.isFile(), true); assert.equal(info.mode & 0o777, 0o500); assert.equal(info.nlink, 1)
  }
  assert.equal(sha256(await readFile(f.lima.limactlFile)), LIMACTL_SHA256)
  assert.equal(sha256(await readFile(f.lima.limaFile)), LIMA_WRAPPER_SHA256)
  assert.notEqual(sha256(await readFile(f.lima.limaFile)), sha256(await readFile(f.lima.limactlFile)))
  assert.notEqual(limaInfo.ino, limactlInfo.ino)
  const result = await materializeO4OfflineRelease(f)
  assert.equal(result.status, 'passed')
  assert.equal(result.networkUsed, false)
  assert.equal(result.dockerInvoked, false)
  assert.equal(result.publicationClaimed, false)
  for (const name of ['offline-bundle-evidence.json', 'candidate-manifest.json', 'candidate-config.json',
    'total-manifest.json', 'materialization-receipt.json']) {
    assert.equal((await stat(join(f.outputRoot, name))).mode & 0o777, 0o400)
  }
  const total = JSON.parse(await readFile(join(f.outputRoot, 'total-manifest.json'), 'utf8'))
  const candidate = JSON.parse(await readFile(join(f.outputRoot, 'candidate-config.json'), 'utf8'))
  const manifest = JSON.parse(await readFile(join(f.outputRoot, 'candidate-manifest.json'), 'utf8'))
  assert.deepEqual(total.repository, { root: f.repository.repositoryRoot,
    commit: f.repository.repositoryCommit,
    closureSha256: f.repository.repositoryClosureSha256 })
  assert.equal(candidate.paths.repositoryRoot, total.repository.root)
  assert.equal(candidate.authority.repositoryCommit, total.repository.commit)
  assert.equal(candidate.authority.repositoryClosureSha256, total.repository.closureSha256)
  assert.equal(candidate.authority.limaPrefixClosureSha256,
    total.engine.limaPrefixClosureSha256)
  assert.equal(candidate.authority.limaInfoDigest, total.engine.limaInfoDigest)
  assert.equal(total.engine.limaInfoDigest, f.lima.infoDigest)
  assert.equal(manifest.repositoryCommit, total.repository.commit)
  assert.equal(manifest.repositoryClosureSha256, total.repository.closureSha256)
  assert.equal(total.phases.E.environment.CHORA_O4_FINAL_OFFLINE_BUNDLE_EVIDENCE_FILE,
    join(f.outputRoot, 'offline-bundle-evidence.json'))
  assert.equal(total.phases.D.environment.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_SHA256,
    total.candidate.configSha256)
  assert.equal('CHORA_O4_FINAL_REGISTRY_EVIDENCE_FILE' in total.phases.E.environment, false)
  assert.match(total.selfDigest, /^[0-9a-f]{64}$/)
})

test('binds CLI and every materialized candidate/D/E path to the immutable output root', async (t) => {
  await t.test('CLI root mismatch', async () => {
    const f = await fixture()
    await assert.rejects(materializeO4OfflineRelease({ ...f, outputRoot: join(f.root, 'other-output') }),
      /CLI materialization output root does not match/)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
  await t.test('candidate path splice', async () => {
    const f = await fixture()
    f.request.candidateConfigTemplate.paths.candidateManifestFile =
      join(f.root, 'retired-candidate', 'candidate-manifest.json')
    await persistRequest(f)
    await assert.rejects(materializeO4OfflineRelease(f), /materialized output paths/)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
  await t.test('phase D path splice', async () => {
    const f = await fixture()
    f.request.totalManifestTemplate.phases.D.environment.CHORA_O4_RECOVERY_RESIDUE_B2_CONFIG_FILE =
      join(f.root, 'other-candidate-config.json')
    await persistRequest(f)
    await assert.rejects(materializeO4OfflineRelease(f), /materialized output paths/)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
  await t.test('phase E path splice', async () => {
    const f = await fixture()
    f.request.totalManifestTemplate.phases.E.environment.CHORA_O4_FINAL_CANDIDATE_MANIFEST_FILE =
      join(f.root, 'retired-candidate-manifest.json')
    await persistRequest(f)
    await assert.rejects(materializeO4OfflineRelease(f), /materialized output paths/)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
  await t.test('request schema drift', async () => {
    const f = await fixture()
    f.request.unboundOutputRoot = f.outputRoot
    await persistRequest(f)
    await assert.rejects(materializeO4OfflineRelease(f), /materialization request keys drifted/)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
})

test('publishes atomically from an owner-0700 sibling and leaves final absent on partial failure', async (t) => {
  await t.test('atomic success', async () => {
    const f = await fixture()
    let observedStaging
    const result = await materializeO4OfflineRelease(f, { beforeCommit: async ({ staging }) => {
      observedStaging = staging
      await assert.rejects(stat(f.outputRoot), /ENOENT/)
      const info = await lstat(staging)
      assert.equal(info.mode & 0o777, 0o700)
      assert.equal(info.uid, process.getuid())
      assert.equal(dirname(staging), f.root)
    } })
    assert.equal(result.status, 'passed')
    assert.equal((await stat(f.outputRoot)).isDirectory(), true)
    await assert.rejects(stat(observedStaging), /ENOENT/)
  })
  await t.test('partial failure', async () => {
    const f = await fixture()
    await assert.rejects(materializeO4OfflineRelease(f, { beforeCommit: async () => {
      await assert.rejects(stat(f.outputRoot), /ENOENT/)
      throw new Error('synthetic partial failure')
    } }), /synthetic partial failure/)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
    assert.deepEqual((await readdir(f.root)).filter((name) => name.includes('.materializing-')), [])
  })
  await t.test('repository mutation after inspection', async () => {
    const f = await fixture(); const tracked = join(f.repository.repositoryRoot, 'README.md')
    await assert.rejects(materializeO4OfflineRelease(f, { beforeCommit: async () => {
      await chmod(tracked, 0o600); await writeFile(tracked, 'mutated before publication\n')
      await chmod(tracked, 0o644)
    } }), /repository .*drifted|repository.*does not match|Git inspection failed/i)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
    assert.deepEqual((await readdir(f.root)).filter((name) => name.includes('.materializing-')), [])
  })
  await t.test('foreign-swapped staging after the JavaScript publish check is preserved and reported',
    async () => {
      const f = await fixture()
      let displaced
      let foreignStaging
      await assert.rejects(materializeO4OfflineRelease(f, {
        publishOwnedPath: { beforeSpawn: async ({ protocol, request }) => {
          assert.equal(protocol, 'chora.m1-o4-owned-path-publish.v1')
          displaced = `${request.sourcePath}-owned`
          await rename(request.sourcePath, displaced)
          await mkdir(request.sourcePath, { mode: 0o700 })
          await chmod(request.sourcePath, 0o700)
          await writeFile(join(request.sourcePath, 'foreign-marker.txt'),
            'foreign replacement\n', { mode: 0o600 })
          foreignStaging = request.sourcePath
        } },
      }), (error) => {
        assert.equal(error instanceof AggregateError, true)
        assert.match(error.cause?.message ?? '', /owned-path controller failed/)
        assert.equal(error.errors[0], error.cause)
        return true
      })
      assert.equal(await readFile(join(foreignStaging, 'foreign-marker.txt'), 'utf8'),
        'foreign replacement\n')
      await assert.rejects(stat(f.outputRoot), /ENOENT/)
      await rm(foreignStaging, { recursive: true })
      await rm(displaced, { recursive: true })
    })
  await t.test('racing output target after the JavaScript publish check is never replaced', async () => {
    const f = await fixture()
    await assert.rejects(materializeO4OfflineRelease(f, {
      publishOwnedPath: { beforeSpawn: async ({ request }) => {
        await mkdir(request.targetPath, { mode: 0o700 })
        await writeFile(join(request.targetPath, 'foreign-marker.txt'),
          'foreign target\n', { mode: 0o600 })
      } },
    }), /owned-path controller failed/)
    assert.equal(await readFile(join(f.outputRoot, 'foreign-marker.txt'), 'utf8'), 'foreign target\n')
    assert.deepEqual((await readdir(f.root)).filter((name) => name.includes('.materializing-')), [])
  })
})

test('rejects repository authority omission and splice before publication', async (t) => {
  const cases = [
    ['prefix authority field', async (f) => {
      const drift = sha256('prefix-drift')
      f.request.totalManifestTemplate.engine.limaPrefixClosureSha256 = drift
      f.request.candidateConfigTemplate.authority.limaPrefixClosureSha256 = drift
      await persistRequest(f)
    }],
    ['info authority field', async (f) => {
      f.request.candidateConfigTemplate.authority.limaInfoDigest = sha256('info-drift')
      await persistRequest(f)
    }],
    ['info README inclusion', async (f) => {
      const drift = sha256(canonical({
        templates: [...f.lima.templateLocations, join(f.lima.templatesRoot, 'README.md')].sort(),
        guestAgent: f.lima.guestAgentFile, hostOS: 'darwin', hostArch: 'aarch64',
      }))
      f.request.totalManifestTemplate.engine.limaInfoDigest = drift
      f.request.candidateConfigTemplate.authority.limaInfoDigest = drift
      await persistRequest(f)
    }],
    ['info template set drift', async (f) => {
      const drift = sha256(canonical({
        templates: [...f.lima.templateLocations.slice(0, -1),
          join(f.lima.templatesRoot, 'extras/not-authorized.yaml')].sort(),
        guestAgent: f.lima.guestAgentFile, hostOS: 'darwin', hostArch: 'aarch64',
      }))
      f.request.totalManifestTemplate.engine.limaInfoDigest = drift
      f.request.candidateConfigTemplate.authority.limaInfoDigest = drift
      await persistRequest(f)
    }],
    ['candidate commit splice', async (f) => {
      f.request.candidateConfigTemplate.authority.repositoryCommit = '0'.repeat(40)
    }, /candidate\/total repository authority drifted/],
    ['total closure splice', async (f) => {
      f.request.totalManifestTemplate.repository.closureSha256 = '0'.repeat(64)
    }, /candidate\/total repository authority drifted/],
    ['candidate manifest omission', async (f) => {
      delete f.request.candidateManifestTemplate.repositoryCommit
    }, /candidate manifest repository authority null phase drifted/],
    ['total repository extra field', async (f) => {
      f.request.totalManifestTemplate.repository.unbound = 'splice'
    }, /total repository authority keys drifted/],
  ]
  for (const [name, mutate, pattern] of cases) await t.test(name, async () => {
    const f = await fixture(); await mutate(f); await persistRequest(f)
    await assert.rejects(materializeO4OfflineRelease(f), pattern)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
})

test('rejects unsafe output parents and parent/input TOCTOU without broad deletion', async (t) => {
  await t.test('group/world-writable parent', async () => {
    const f = await fixture(); const parent = join(f.root, 'unsafe-parent')
    await mkdir(parent, { mode: 0o700 }); await chmod(parent, 0o777)
    bindOutputRoot(f, join(parent, 'materialized')); await persistRequest(f)
    await assert.rejects(materializeO4OfflineRelease(f), /output parent must be owner-controlled/)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
  await t.test('symlink parent', async () => {
    const f = await fixture(); const actual = join(f.root, 'actual-parent')
    const alias = join(f.root, 'alias-parent')
    await mkdir(actual, { mode: 0o700 }); await symlink(actual, alias)
    bindOutputRoot(f, join(alias, 'materialized')); await persistRequest(f)
    await assert.rejects(materializeO4OfflineRelease(f), /symlink ancestor/)
    await assert.rejects(stat(join(actual, 'materialized')), /ENOENT/)
  })
  await t.test('input TOCTOU', async () => {
    const f = await fixture()
    await assert.rejects(materializeO4OfflineRelease(f, { beforeCommit: async () => {
      await chmod(f.requestFile, 0o600)
    } }), /request identity changed/)
    await chmod(f.requestFile, 0o400)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
  await t.test('parent TOCTOU', async () => {
    const f = await fixture()
    await assert.rejects(materializeO4OfflineRelease(f, { beforeCommit: async () => {
      await chmod(f.root, 0o755)
    } }), /output parent changed/)
    await chmod(f.root, 0o700)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
  await t.test('preexisting final is preserved', async () => {
    const f = await fixture(); await mkdir(f.outputRoot, { mode: 0o700 })
    const sentinel = join(f.outputRoot, 'sentinel'); await pinned(sentinel, 'owned elsewhere\n', 0o400)
    await assert.rejects(materializeO4OfflineRelease(f), /already exists/)
    assert.equal(await readFile(sentinel, 'utf8'), 'owned elsewhere\n')
  })
  await t.test('racing final is preserved', async () => {
    const f = await fixture(); const sentinel = join(f.outputRoot, 'sentinel')
    await assert.rejects(materializeO4OfflineRelease(f, { beforeCommit: async () => {
      await mkdir(f.outputRoot, { mode: 0o700 }); await pinned(sentinel, 'racing owner\n', 0o400)
    } }), /output parent changed|already exists/)
    assert.equal(await readFile(sentinel, 'utf8'), 'racing owner\n')
    assert.deepEqual((await readdir(f.root)).filter((name) => name.includes('.materializing-')), [])
  })
})

test('fails closed on portable Lima prefix drift before materialized output', async (t) => {
  const cases = [
    ['missing', async (f) => unlink(join(f.lima.templatesRoot, 'default.yaml'))],
    ['guest-agent missing', async (f) => unlink(f.lima.guestAgentFile)],
    ['extra', async (f) => pinned(join(f.lima.templatesRoot, 'extra.yaml'), 'x', 0o400)],
    ['link', async (f) => symlink('default.yaml', join(f.lima.templatesRoot, 'linked.yaml'))],
    ['mode', async (f) => chmod(join(f.lima.templatesRoot, 'default.yaml'), 0o600)],
    ['hash', async (f) => {
      const path = join(f.lima.templatesRoot, 'extras/asset-000.yaml')
      await chmod(path, 0o600); await writeFile(path, 'y'); await chmod(path, 0o400)
    }],
    ['topology', async (f) => {
      f.request.totalManifestTemplate.engine.limaWrapperFile = f.lima.limactlFile
      await persistRequest(f)
    }],
  ]
  for (const [name, mutate] of cases) await t.test(name, async () => {
    const f = await fixture(); await mutate(f)
    await assert.rejects(materializeO4OfflineRelease(f),
      /canonical|prefix|template|topology|symlink|SHA-256|ENOENT|info/i)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
  const source = await readFile(new URL('./o4-local-offline-materializer.mjs', import.meta.url), 'utf8')
  assert.match(source, /directory changed while hashing/)
  assert.match(source, /root changed while hashing/)
  assert.doesNotMatch(source, /node:child_process|\bspawn\s*\(/)
})

test('fails closed on legacy Registry fields before creating the output root', async () => {
  const f = await fixture()
  f.request.candidateConfigTemplate.paths.registryEvidenceFile = '/legacy/registry.json'
  await persistRequest(f)
  await assert.rejects(materializeO4OfflineRelease(f),
    /legacy Registry field/)
  await assert.rejects(stat(f.outputRoot), /ENOENT/)
})

test('rejects release manifest raw, mode, identity, role-policy, and archive splices before output', async () => {
  const cases = [
    ['raw SHA drift', async (f) => {
      await chmod(f.releaseManifestFile, 0o600)
      await writeFile(f.releaseManifestFile, `${JSON.stringify(f.releaseManifest)} `)
      await chmod(f.releaseManifestFile, 0o444)
    }, /raw SHA-256 drifted/],
    ['mode drift', async (f) => { await chmod(f.releaseManifestFile, 0o400) },
      /owner-controlled immutable file/],
    ['release identity splice', async (f) => {
      f.releaseManifest.release_id = 'other-release'; await persistReleaseManifest(f)
    }, /top-level identity drifted/],
    ['role policy splice', async (f) => {
      f.releaseManifest.roles[0].policy.sha256 = sha256('spliced-policy'); await persistReleaseManifest(f)
    }, /role binding drifted/],
    ['archive path splice', async (f) => {
      f.releaseManifest.artifacts[0].image.archive.path = 'archives/other.tar'; await persistReleaseManifest(f)
    }, /artifact\/archive binding drifted/],
    ['config ID splice', async (f) => {
      f.releaseManifest.artifacts[1].image.local_docker_config_image_id = image('spliced-config')
      await persistReleaseManifest(f)
    }, /artifact\/archive binding drifted/],
  ]
  for (const [label, mutate, pattern] of cases) {
    const f = await fixture(); await mutate(f)
    await assert.rejects(materializeO4OfflineRelease(f), pattern, label)
    await assert.rejects(stat(f.outputRoot), /ENOENT/, label)
  }
})

test('fails closed on archive byte drift and leaves no partial closure', async () => {
  const f = await fixture()
  const path = f.request.artifacts[0].archiveFile
  await chmod(path, 0o600); await writeFile(path, Buffer.alloc(101, 0xff)); await chmod(path, 0o400)
  await assert.rejects(materializeO4OfflineRelease(f), /archive bytes drifted/)
  await assert.rejects(stat(f.outputRoot), /ENOENT/)
})

test('fails closed on fresh Engine disk-image byte or bootstrap-policy drift before output', async (t) => {
  await t.test('candidate closure splice', async () => {
    const f = await fixture()
    f.request.candidateConfigTemplate.authority.limaSha256 = sha256('spliced-lima')
    await persistRequest(f)
    await assert.rejects(materializeO4OfflineRelease(f), /pinned fresh Engine closure drifted/)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
  await t.test('disk image bytes', async () => {
    const f = await fixture()
    const path = f.request.totalManifestTemplate.engine.diskImageFile
    await chmod(path, 0o600); await writeFile(path, Buffer.alloc(
      f.request.totalManifestTemplate.engine.diskImageSize, 0x44)); await chmod(path, 0o400)
    await assert.rejects(materializeO4OfflineRelease(f), /disk image SHA-256 drifted/)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
  await t.test('bootstrap sandbox policy', async () => {
    const f = await fixture()
    const path = f.request.totalManifestTemplate.engine.sandboxProfileFile
    await chmod(path, 0o600); await writeFile(path, '(version 1)\n(allow default)\n'); await chmod(path, 0o400)
    f.request.totalManifestTemplate.engine.sandboxProfileSha256 = sha256(await readFile(path))
    f.request.candidateConfigTemplate.authority.sandboxProfileSha256 =
      f.request.totalManifestTemplate.engine.sandboxProfileSha256
    await persistRequest(f)
    await assert.rejects(materializeO4OfflineRelease(f), /exact local\/Unix-only deny-egress policy/)
    await assert.rejects(stat(f.outputRoot), /ENOENT/)
  })
})

test('fails closed on Playwright closure byte, mode, and symlink drift', async (t) => {
  const cases = [
    ['browser bytes', async (f) => {
      const path = join(f.browserRoot, 'chromium')
      await chmod(path, 0o700); await writeFile(path, 'changed-browser\n'); await chmod(path, 0o500)
    }, /browser closure SHA-256 drifted/],
    ['package mode', async (f) => { await chmod(join(f.packageRoot, 'package.json'), 0o620) },
      /group\/world-writable file/],
    ['browser symlink', async (f) => {
      await symlink('../playwright-package/package.json', join(f.browserRoot, 'linked-package'))
    }, /symlink .* escapes root/],
  ]
  for (const [name, mutate, pattern] of cases) {
    await t.test(name, async () => {
      const f = await fixture(); await mutate(f)
      await assert.rejects(materializeO4OfflineRelease(f), pattern)
      await assert.rejects(stat(f.outputRoot), /ENOENT/)
    })
  }
})

test('accepts a digest-bound internal Playwright browser symlink', async () => {
  const f = await fixture()
  await symlink('chromium', join(f.browserRoot, 'current-browser'))
  f.request.totalManifestTemplate.playwright.browserClosureSha256 =
    await directoryClosureSha256(f.browserRoot)
  await persistRequest(f)
  const result = await materializeO4OfflineRelease(f)
  assert.equal(result.status, 'passed')
})
