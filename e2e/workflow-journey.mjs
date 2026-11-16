// Optional browser + Go fixture journey, launched by TestWorkflowBrowserJourney.
// It uses synthetic research/Pi and must never be described as live-model proof.
import { chromium } from '@playwright/test'

const target = process.env.CHOR50_BROWSER_URL
if (!target || !/^http:\/\/127\.0\.0\.1:\d+\/rooms\//.test(target)) throw new Error('A disposable local fixture URL is required')
const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  await page.addInitScript(() => localStorage.setItem('chora.locale', 'en'))
  await page.goto(target)
  await page.getByRole('button', { name: 'Start research before coding', exact: true }).click()
  const section = page.getByRole('region', { name: 'Research before coding', exact: true })
  await section.getByRole('button', { name: 'Confirm and code' }).waitFor()
  await page.reload()
  await section.getByRole('button', { name: 'Confirm and code' }).waitFor()
  await section.getByRole('textbox').fill('Keep the existing task boundary after reload.')
  await section.getByRole('button', { name: 'Confirm and code' }).click()
  await section.getByRole('button', { name: 'Open coding result' }).waitFor()
  const workflow = await page.evaluate(async () => {
    const task = location.pathname.split('/')[4]
    return (await (await fetch(`/api/tasks/${task}/workflow`)).json()).workflow
  })
  process.stdout.write(`CODING:${workflow.runId}\n`)
  await section.getByText('Ready for your review', { exact: true }).waitFor({ timeout: 30000 })
  if (await section.getByRole('button', { name: 'Confirm and code' }).count()) throw new Error('Confirmation remained active after Run creation')
  await page.setViewportSize({ width: 390, height: 844 })
  await page.reload()
  await section.getByText('Ready for your review', { exact: true }).waitFor()
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 2)
  if (overflow) throw new Error('Narrow viewport overflows horizontally')
  const destination = page.waitForURL(/\/runs\//)
  await section.getByRole('button', { name: 'Open coding result' }).click()
  await destination
  await page.goBack()
  await section.getByText('Ready for your review', { exact: true }).waitFor()
  if (await section.count() !== 1) throw new Error('History navigation duplicated the research panel')
  process.stdout.write('BROWSER_PASS: entry, gate reload, edited confirmation, coding, review, narrow viewport and history\n')
} finally {
  await browser.close()
}
