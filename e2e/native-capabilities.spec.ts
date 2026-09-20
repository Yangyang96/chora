import { expect, test, type APIRequestContext } from '@playwright/test'
import { resolve } from 'node:path'

type Project = { id: string }
type Config = {
  version: number
  skillPaths: string[]
  disabledSkillPaths: string[]
  bridgePath: string
  mcpConfigPath: string
  disabledMcpServers: string[]
}

async function createProject(request: APIRequestContext, name: string) {
  const response = await request.post('/api/v2/projects', { data: { name } })
  expect(response.status(), await response.text()).toBe(201)
  return response.json() as Promise<Project>
}

async function readConfig(request: APIRequestContext, projectID: string) {
  const response = await request.get(`/api/projects/${projectID}/capabilities/config`)
  expect(response.status(), await response.text()).toBe(200)
  return response.json() as Promise<Config>
}

test('Project Skills and MCP configuration persists, stays scoped and rejects invalid paths', async ({ page, request }) => {
  const first = await createProject(request, 'Native capabilities first')
  const second = await createProject(request, 'Native capabilities second')
  const expected = {
    skillPaths: [resolve('web/src/ui'), resolve('internal/nativecapabilities')],
    disabledSkillPaths: [resolve('internal/nativecapabilities')],
    bridgePath: resolve('web/node_modules'),
    mcpConfigPath: resolve('web/package.json'),
    disabledMcpServers: ['retired-local-server'],
  }

  await page.goto(`/projects/${first.id}`)
  await page.getByText('Skills and MCP', { exact: true }).click()
  await expect(page.getByLabel('Configuration version')).toHaveValue('0')
  await page.getByLabel('Local Skill paths').fill(expected.skillPaths.join('\n'))
  await page.getByLabel('Disabled Skill paths').fill(expected.disabledSkillPaths.join('\n'))
  await page.getByLabel('Installed bridge package path (optional)').fill(expected.bridgePath)
  await page.getByLabel('MCP config path (optional)').fill(expected.mcpConfigPath)
  await page.getByLabel('Disabled MCP server names').fill(expected.disabledMcpServers.join('\n'))
  await page.getByRole('button', { name: 'Save Skills and MCP' }).click()
  await expect(page.getByLabel('Configuration version')).toHaveValue('1')

  expect(await readConfig(request, first.id)).toEqual({ version: 1, ...expected })

  await page.reload()
  await page.getByText('Skills and MCP', { exact: true }).click()
  await expect(page.getByLabel('Configuration version')).toHaveValue('1')
  await expect(page.getByLabel('Local Skill paths')).toHaveValue(expected.skillPaths.join('\n'))
  await expect(page.getByLabel('Disabled Skill paths')).toHaveValue(expected.disabledSkillPaths.join('\n'))
  await expect(page.getByLabel('Installed bridge package path (optional)')).toHaveValue(expected.bridgePath)
  await expect(page.getByLabel('MCP config path (optional)')).toHaveValue(expected.mcpConfigPath)
  await expect(page.getByLabel('Disabled MCP server names')).toHaveValue('retired-local-server')

  await page.goto(`/projects/${second.id}`)
  await page.getByText('Skills and MCP', { exact: true }).click()
  await expect(page.getByLabel('Configuration version')).toHaveValue('0')
  await expect(page.getByLabel('Local Skill paths')).toHaveValue('')
  expect(await readConfig(request, second.id)).toEqual({
    version: 0, skillPaths: [], disabledSkillPaths: [], bridgePath: '', mcpConfigPath: '', disabledMcpServers: [],
  })

  const rejected = await request.put(`/api/projects/${second.id}/capabilities/config`, { data: {
    version: 0, skillPaths: ['relative/skill'], disabledSkillPaths: [], bridgePath: '', mcpConfigPath: '', disabledMcpServers: [],
  } })
  expect(rejected.status(), await rejected.text()).toBe(400)
  expect(await readConfig(request, second.id)).toMatchObject({ version: 0, skillPaths: [] })
  expect(await readConfig(request, first.id)).toEqual({ version: 1, ...expected })
})
