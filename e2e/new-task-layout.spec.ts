import { expect, test } from '@playwright/test'

test('task-level checkboxes stay beside their labels on desktop and mobile', async ({ page, request }) => {
  const response = await request.post('/api/v2/projects', { data: { name: 'Task layout' } })
  expect(response.status()).toBe(201)
  const project = await response.json()
  await page.goto(`/rooms/${project.defaultRoomId}`)
  await page.getByRole('button', { name: '＋ New Task', exact: true }).first().click()
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 })
    for (const locale of ['EN', '中文']) {
      await page.getByRole('button', { name: locale, exact: true }).click()
      const labels = locale === 'EN'
        ? ['Work with supplied material only', 'Override Project defaults for this task']
        : ['仅使用明确提供的材料', '为此任务覆盖项目默认值']
      for (const name of labels) {
        const checkbox = page.getByRole('checkbox', { name, exact: true })
        await expect(checkbox).toBeVisible()
        const layout = await checkbox.evaluate(input => {
          const label = input.closest('label')!
          const box = input.getBoundingClientRect()
          const range = document.createRange()
          range.selectNodeContents(label.lastChild!)
          const text = range.getClientRects()[0]
          return { direction: getComputedStyle(label).flexDirection, width: box.width,
            beside: text.left >= box.right && Math.abs(text.top - box.top) < 8 }
        })
        expect(layout.direction).toBe('row')
        expect(layout.width).toBeLessThanOrEqual(20)
        expect(layout.beside).toBe(true)
      }
    }
  }
})
