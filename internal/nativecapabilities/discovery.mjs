// Read-only discovery through the installed Provider's own resource resolver.
// This process never loads extensions, connects servers, or installs packages.
import { lstat, readFile, readdir, realpath } from 'node:fs/promises'
import { dirname, extname, join, relative, resolve, sep } from 'node:path'
import { pathToFileURL } from 'node:url'
import { createHash } from 'node:crypto'

function loadProjectMcpConfig(api, path, cwd) {
  if (!path) return { mcpServers: {} }
  const previous = process.env.PI_MCP_CONFIG_MODE
  const previousWarn = console.warn
  const failurePrefix = `Failed to load MCP config from ${resolve(path)}:`
  let failed = false
  process.env.PI_MCP_CONFIG_MODE = 'exclusive'
  console.warn = (...args) => { if (args[0] === failurePrefix) failed = true; previousWarn(...args) }
  let config
  try { config = api.loadMcpConfig(path, cwd) }
  finally {
    console.warn = previousWarn
    if (previous === undefined) delete process.env.PI_MCP_CONFIG_MODE
    else process.env.PI_MCP_CONFIG_MODE = previous
  }
  if (failed) throw new Error('Project MCP configuration is invalid')
  return config
}

function mergeMcpConfigs(inherited, project) {
  const imports = [...new Set([...(inherited.imports ?? []), ...(project.imports ?? [])])]
  return {
    mcpServers: { ...inherited.mcpServers, ...project.mcpServers },
    ...(imports.length ? { imports } : {}),
    ...(inherited.settings || project.settings ? { settings: { ...inherited.settings, ...project.settings } } : {}),
    ...(project.claudePlugins !== undefined ? { claudePlugins: project.claudePlugins } : inherited.claudePlugins !== undefined ? { claudePlugins: inherited.claudePlugins } : {}),
  }
}

async function digestBridgeSources(root, pkg) {
  const maximumFiles = 2048
  const maximumBytes = 64 * 1024 * 1024
  const candidates = new Set(['package.json', 'package-lock.json'])
  for (const entry of Array.isArray(pkg.files) ? pkg.files : []) candidates.add(entry)
  const files = new Set()
  let declaredBytes = 0
  const visit = async path => {
    const rel = relative(root, path)
    if (!rel || rel === '..' || rel.startsWith(`..${sep}`)) throw new Error('Bridge source escaped package root')
    if (rel.split(sep).some(part => part === 'node_modules' || part === '.git')) return
    let info
    try { info = await lstat(path) } catch (error) {
      if (error?.code === 'ENOENT' && rel === 'package-lock.json') return
      throw error
    }
    if (info.isSymbolicLink()) throw new Error('Bridge source contains a symlink')
    if (info.isDirectory()) {
      for (const entry of (await readdir(path)).sort()) await visit(join(path, entry))
      return
    }
    if (!info.isFile()) throw new Error('Bridge source is not a regular file')
    if (rel !== 'package.json' && rel !== 'package-lock.json' && !['.ts', '.js', '.mjs', '.cjs'].includes(extname(path))) return
    files.add(path)
    if (files.size > maximumFiles) throw new Error('Bridge source file limit exceeded')
    declaredBytes += Number(info.size)
    if (declaredBytes > maximumBytes) throw new Error('Bridge source byte limit exceeded')
  }
  for (const entry of [...candidates].sort()) await visit(resolve(root, entry))
  let bytes = 0
  const digests = []
  for (const path of [...files].sort()) {
    const body = await readFile(path)
    bytes += body.length
    if (bytes > maximumBytes) throw new Error('Bridge source byte limit exceeded')
    digests.push([relative(root, path), createHash('sha256').update(body).digest('hex')])
  }
  return digests
}

export async function discover(input) {
  const { DefaultPackageManager, SettingsManager, loadSkills } = await import(pathToFileURL(join(input.piRoot, 'dist/index.js')).href)
  const settingsManager = SettingsManager.create(input.cwd, input.agentDir)
  const manager = new DefaultPackageManager({ cwd: input.cwd, agentDir: input.agentDir, settingsManager })
  const missing = []
  const resources = await manager.resolve(async source => { missing.push(source); return 'skip' })
  const disabledSkills = new Set(input.config.disabledSkillPaths ?? [])
  // loadSkills resolves name collisions first-wins. Chora Project paths must
  // therefore precede inherited Pi paths while the latter remain available.
  const paths = [...(input.config.skillPaths ?? []), ...resources.skills.filter(item => item.enabled).map(item => item.path)]
  const loaded = loadSkills({ cwd: input.cwd, agentDir: input.agentDir, skillPaths: paths, includeDefaults: false })
  const skills = loaded.skills.map(skill => ({ name: skill.name, path: skill.filePath, source: (input.config.skillPaths ?? []).some(path => skill.filePath === path || skill.filePath.startsWith(path + '/')) ? 'project' : skill.sourceInfo?.scope ?? 'user', enabled: !disabledSkills.has(skill.filePath), status: disabledSkills.has(skill.filePath) ? 'disabled' : 'discovered' }))
  for (const skill of skills) {
    if (skill.source !== 'project') skill.source = resources.skills.find(item => skill.path === item.path || skill.path.startsWith(item.path + '/'))?.metadata.scope ?? 'user'
  }
  const diagnostics = loaded.diagnostics.filter(item => item.type !== 'collision' && !loaded.skills.some(skill => skill.filePath === item.path)).map(item => ({ path: item.path, status: 'unavailable' }))
  for (const item of diagnostics) skills.push({ name: item.path.split('/').at(-1), path: item.path, source: (input.config.skillPaths ?? []).includes(item.path) ? 'project' : 'user', enabled: !disabledSkills.has(item.path), status: disabledSkills.has(item.path) ? 'disabled' : 'unavailable' })
  let bridgePath = input.config.bridgePath ?? ''
  const extensions = resources.extensions.filter(item => item.enabled).map(item => item.path)
  const inheritedBridgeExtensions = []
  {
    for (const file of extensions) {
      let root = dirname(file)
      for (let depth = 0; depth < 4; depth++, root = dirname(root)) {
        try { const pkg = JSON.parse(await readFile(join(root, 'package.json'), 'utf8')); if (pkg.name === 'pi-mcp-adapter') { if (!bridgePath) bridgePath = root; inheritedBridgeExtensions.push(file); break } } catch {}
      }
    }
  }
  let bridge = { status: 'missing', path: bridgePath, version: '' }, servers = [], bridgeExtensions = inheritedBridgeExtensions, configurationDigest = '', bridgeSourceDigests = []
  if (bridgePath) {
    try {
      bridgePath = await realpath(bridgePath)
      const pkg = JSON.parse(await readFile(join(bridgePath, 'package.json'), 'utf8'))
      if (pkg.name !== 'pi-mcp-adapter' || pkg.version !== '2.34.0') throw new Error('Unsupported bridge')
      bridgeSourceDigests = await digestBridgeSources(bridgePath, pkg)
      const api = await import(pathToFileURL(join(bridgePath, 'dist/config.js')).href)
      if (input.config.mcpConfigPath) await readFile(input.config.mcpConfigPath, 'utf8')
      const inheritedConfig = api.loadMcpConfig(undefined, input.cwd)
      const projectConfig = loadProjectMcpConfig(api, input.config.mcpConfigPath, input.cwd)
      const config = mergeMcpConfigs(inheritedConfig, projectConfig)
      configurationDigest = createHash('sha256').update(JSON.stringify(config)).digest('hex')
      const inheritedProvenance = api.getServerProvenance(undefined, input.cwd)
      const projectNames = new Set(Object.keys(projectConfig.mcpServers))
      servers = Object.entries(config.mcpServers).map(([name, value]) => ({ name, source: projectNames.has(name) ? 'project' : inheritedProvenance.get(name)?.kind ?? 'user', enabled: value.disabled !== true && !(input.config.disabledMcpServers ?? []).includes(name), status: value.disabled === true || (input.config.disabledMcpServers ?? []).includes(name) ? 'disabled' : 'not-connected' }))
      bridge = { status: 'discovered', path: bridgePath, version: pkg.version }
    } catch { bridge = { status: 'unavailable', path: bridgePath, version: '' } }
  } else if (input.config.mcpConfigPath) bridge = { status: 'unavailable', path: '', version: '' }
  const sourceDigests = []
  for (const path of [...skills.map(item => item.path), ...extensions]) {
    try { sourceDigests.push([path, createHash('sha256').update(await readFile(path)).digest('hex')]) } catch { sourceDigests.push([path, 'unavailable']) }
  }
  for (const [path, digest] of bridgeSourceDigests) sourceDigests.push([join(bridge.path, path), digest])
  const resourceDigest = createHash('sha256').update(JSON.stringify({ sourceDigests, configurationDigest, bridge })).digest('hex')
  return { skills, servers, bridge, configurationDigest, resourceDigest, missingPackages: missing.length, diagnostics, extensionPaths: extensions.filter(path => !bridgeExtensions.includes(path)) }
}

if (process.argv[2] === '--discover') {
  let body = ''
  for await (const part of process.stdin) { body += part; if (body.length > 1024 * 1024) throw new Error('Input exceeds limit') }
  try { process.stdout.write(JSON.stringify(await discover(JSON.parse(body)))) }
  catch { process.stdout.write(JSON.stringify({ error: 'Native capability discovery failed. Check the selected Pi installation and configuration.' })); process.exitCode = 1 }
}
