// Pi remains responsible for the model loop, MCP transport and authentication.
import { readFileSync, writeFileSync, renameSync } from 'node:fs'
import { createRequire } from 'node:module'
import { join, resolve } from 'node:path'
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

export default async function capabilities(pi) {
  const manifestPath = process.env.CHORA_NATIVE_CAPABILITIES_MANIFEST
  if (!manifestPath) return
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
  const state = { version: 1, projectID: manifest.projectID, configVersion: manifest.config.version, attemptID: process.env.CHORA_ATTEMPT_ID, observedAt: '', skills: manifest.inventory.skills, servers: manifest.inventory.servers, bridge: manifest.inventory.bridge, running: false }
  const publish = () => {
    state.observedAt = new Date().toISOString()
    const output = process.env.CHORA_NATIVE_CAPABILITIES_STATUS
    writeFileSync(output + '.tmp', JSON.stringify(state), { mode: 0o600 })
    renameSync(output + '.tmp', output)
  }
  const warn = (ctx, message) => { ctx.ui.notify(message, 'warning') }
  const observeSkills = () => {
    const commands = pi.getCommands().filter(item => item.source === 'skill')
    const loaded = new Set(commands.map(item => item.sourceInfo?.path))
    state.skills = state.skills.map(item => ({ ...item, status: !item.enabled ? 'disabled' : item.status === 'unavailable' ? 'unavailable' : loaded.has(item.path) ? 'available' : 'discovered' }))
  }
  pi.events.on('pi-mcp-adapter/status/v1', snapshot => {
    if (snapshot?.version !== 1 || !Array.isArray(snapshot.servers)) return
    // The bridge publishes an empty handoff/shutdown snapshot as well. Retain
    // last-seen per-server facts and let session lifecycle mark freshness.
    if (!snapshot.servers.length && state.servers.length) return
    const previous = new Map(state.servers.map(item => [item.name, item]))
    state.servers = snapshot.servers.map(item => ({ name: item.name, source: previous.get(item.name)?.source ?? 'user', enabled: !item.disabled, status: item.status, toolCount: item.status === 'connected' ? item.toolCount : 0 }))
    publish()
  })
  if (manifest.inventory.bridge.status === 'discovered') {
    try {
      const require = createRequire(join(manifest.piRoot, 'package.json'))
      const { createJiti } = require('jiti')
      const jiti = createJiti(import.meta.url, { interopDefault: false, alias: { '@earendil-works/pi-coding-agent': join(manifest.piRoot, 'dist/index.js') } })
      const { createMcpAdapter } = await jiti.import(join(manifest.inventory.bridge.path, 'index.ts'))
      const api = await import(join(manifest.inventory.bridge.path, 'dist/config.js'))
      const inheritedConfig = api.loadMcpConfig(undefined, manifest.cwd)
      const projectConfig = loadProjectMcpConfig(api, manifest.config.mcpConfigPath, manifest.cwd)
      const config = mergeMcpConfigs(inheritedConfig, projectConfig)
      if (createHash('sha256').update(JSON.stringify(config)).digest('hex') !== manifest.inventory.configurationDigest) throw new Error('MCP configuration changed after preparation')
      config.settings = { ...config.settings, autoAuth: false, scriptMode: false, sampling: false }
      for (const name of manifest.config.disabledMcpServers ?? []) if (config.mcpServers[name]) config.mcpServers[name].disabled = true
      createMcpAdapter({ config })(pi)
    } catch {
      state.bridge.status = 'unavailable'
      state.servers = state.servers.map(item => ({ ...item, status: item.enabled ? 'failed' : 'disabled', toolCount: 0 }))
    }
  }
  pi.on('session_start', (_event, ctx) => {
    state.running = true
    observeSkills()
    if (state.bridge.status === 'unavailable') warn(ctx, 'MCP bridge could not load. Its capabilities are unavailable; this task can continue with other tools.')
    publish()
  })
  pi.on('before_agent_start', (_event, ctx) => {
    observeSkills()
    const unavailable = [...state.skills, ...state.servers].filter(item => ['failed', 'needs-auth', 'unavailable'].includes(item.status))
    if (unavailable.length) warn(ctx, `${unavailable.length} configured capabilities are unavailable. Inspect Skills and MCP for this execution.`)
    publish()
  })
  pi.on('session_shutdown', () => { state.running = false; publish() })
}
