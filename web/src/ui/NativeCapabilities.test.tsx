import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import { NativeCapabilities } from './NativeCapabilities'

function response(body: unknown, ok = true, status = 200): Response {
  return { ok, status, json: async () => body } as Response
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

describe('NativeCapabilities', () => {
  afterEach(() => vi.unstubAllGlobals())

  test('loads and saves all Project fields with the observed version', async () => {
    const calls: Array<{ path: string; method: string; body?: unknown }> = []
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      calls.push({ path: String(input), method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (String(input).endsWith('/capabilities/status')) return response({ observations: [] })
      if (init?.method === 'PUT') return response({ version: 4, skillPaths: ['/skills/a', '/skills/b'], disabledSkillPaths: ['/skills/off'], bridgePath: '/bridge', mcpConfigPath: '/config/mcp.json', disabledMcpServers: ['old-server'] })
      return response({ version: 3, skillPaths: ['/skills/a'], disabledSkillPaths: [], bridgePath: '', mcpConfigPath: '', disabledMcpServers: [] })
    }))
    render(<LanguageProvider><NativeCapabilities projectId="project-1" editable /></LanguageProvider>)

    await userEvent.click(screen.getByText('Skills and MCP'))
    expect(await screen.findByDisplayValue('/skills/a')).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('Local Skill paths'), '{End}{Enter}/skills/b')
    await userEvent.type(screen.getByLabelText('Disabled Skill paths'), '/skills/off')
    await userEvent.type(screen.getByLabelText('Installed bridge package path (optional)'), '/bridge')
    await userEvent.type(screen.getByLabelText('MCP config path (optional)'), '/config/mcp.json')
    await userEvent.type(screen.getByLabelText('Disabled MCP server names'), 'old-server')
    await userEvent.click(screen.getByRole('button', { name: 'Save Skills and MCP' }))

    await waitFor(() => expect(screen.getByLabelText('Configuration version')).toHaveValue('4'))
    expect(calls.filter(({ path }) => path.endsWith('/capabilities/config'))).toEqual([
      { path: '/api/projects/project-1/capabilities/config', method: 'GET', body: undefined },
      { path: '/api/projects/project-1/capabilities/config', method: 'PUT', body: {
        version: 3, skillPaths: ['/skills/a', '/skills/b'], disabledSkillPaths: ['/skills/off'],
        bridgePath: '/bridge', mcpConfigPath: '/config/mcp.json', disabledMcpServers: ['old-server'],
      } },
    ])
  })

  test('ignores a stale response after switching Projects', async () => {
    const first = deferred<Response>()
    const second = deferred<Response>()
    vi.stubGlobal('fetch', vi.fn((input: string | URL | Request) => {
      if (String(input).endsWith('/capabilities/status')) return Promise.resolve(response({ observations: [] }))
      return String(input).includes('project-one') ? first.promise : second.promise
    }))
    const view = render(<LanguageProvider><NativeCapabilities projectId="project-one" editable /></LanguageProvider>)
    await userEvent.click(screen.getByText('Skills and MCP'))
    expect(screen.getByLabelText('Local Skill paths')).toBeDisabled()
    view.rerender(<LanguageProvider><NativeCapabilities projectId="project-two" editable /></LanguageProvider>)
    second.resolve(response({ version: 2, skillPaths: ['/new-project'], disabledSkillPaths: [], bridgePath: '', mcpConfigPath: '', disabledMcpServers: [] }))
    expect(await screen.findByDisplayValue('/new-project')).toBeInTheDocument()
    first.resolve(response({ version: 9, skillPaths: ['/stale-project'], disabledSkillPaths: [], bridgePath: '', mcpConfigPath: '', disabledMcpServers: [] }))
    await waitFor(() => expect(screen.queryByDisplayValue('/stale-project')).not.toBeInTheDocument())
    expect(screen.getByDisplayValue('/new-project')).toBeInTheDocument()
  })
})
