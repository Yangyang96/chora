import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { ReadinessView } from '../types'
import { AppShell } from './AppShell'

const blockedReadiness: ReadinessView = {
  checkedAt: '2026-09-03T12:00:00Z',
  inputFingerprint: '',
  items: [
    {
      key: 'source_baseline',
      state: 'ready',
      observed: 'installed manifest-bound Source Baseline v6 verified',
      required: 'installed manifest-bound Chora Source Baseline v6',
      action: 'No action required.',
    },
    {
      key: 'pi_image',
      state: 'blocked',
      observed: 'Pi Runtime or Sandbox startup dependency unavailable',
      required: 'configured Pi Runtime with the pinned Agent and boundary images',
      action: 'Restore the pinned Pi Runtime, Sandbox, and images; restart Chora; then Refresh readiness.',
    },
    {
      key: 'docker_engine',
      state: 'checking',
      observed: 'Docker engine and context check has not completed',
      required: 'Docker client and server 29.6.1 using context colima',
      action: 'Refresh readiness.',
    },
  ],
}

function response(body: unknown, ok = true): Response {
  return { ok, status: ok ? 200 : 503, json: async () => body } as Response
}

describe('AppShell execution readiness', () => {
  beforeEach(() => window.localStorage.setItem('chora.locale', 'zh-CN'))

  test('shows the actual local Pi state without installer or sandbox readiness checks', () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    render(<LanguageProvider><AppShell sidebar={<aside />} piDiscovery={{ phase: 'loaded', discovery: { state: 'ready', version: '0.85.1', executablePath: '/local/pi', executableSha256: 'a'.repeat(64), readyProviders: ['provider'], notReadyProviders: [] } }}><p>Project</p></AppShell></LanguageProvider>)
    expect(screen.getByText('在本机运行')).toBeInTheDocument()
    expect(screen.getAllByText('Pi 已就绪').length).toBeGreaterThan(0)
    expect(screen.queryByText('Installed product Doctor')).not.toBeInTheDocument()
    expect(screen.queryByText('Docker 引擎')).not.toBeInTheDocument()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  test('keeps missing Pi actionable instead of showing ready', () => {
    vi.stubGlobal('fetch', vi.fn())
    render(<LanguageProvider><AppShell sidebar={<aside />} piDiscovery={{ phase: 'loaded', discovery: { state: 'missing', reason: 'Restore Pi on PATH', readyProviders: [], notReadyProviders: [] } }}><main /></AppShell></LanguageProvider>)
    expect(screen.getAllByText('未找到 Pi').length).toBeGreaterThan(0)
    expect(screen.getByText('Restore Pi on PATH')).toBeInTheDocument()
    expect(screen.queryByText('Pi 已就绪')).not.toBeInTheDocument()
  })

  test('makes a completed blocked source-checkout check distinct from background installation', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
      const path = String(input)
      if (path === '/api/readiness') return response(blockedReadiness)
      return response({}, false)
    }))

    render(
      <LanguageProvider>
        <AppShell sidebar={<aside />}><main /></AppShell>
      </LanguageProvider>,
    )

    expect(await screen.findByText('没有正在后台安装任何内容。')).toBeInTheDocument()
    expect(screen.getByText('源码运行只检查真实执行依赖；它不会自动安装 Docker、Colima、镜像或 OAuth。')).toBeInTheDocument()
    expect(screen.getByText('Pi 镜像')).toBeInTheDocument()
    expect(screen.getByText('Docker 引擎')).toBeInTheDocument()
    expect(screen.getByText('未检查')).toBeInTheDocument()
    expect(screen.getAllByText('当前状态').length).toBeGreaterThan(0)
    expect(screen.getByText('下一步')).toBeInTheDocument()
  })
})
