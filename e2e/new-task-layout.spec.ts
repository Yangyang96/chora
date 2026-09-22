import { expect, test } from '@playwright/test'

test('task purpose preserves drafts and choice layouts across languages and widths', async ({ page, request }) => {
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
        ? ['Override Project defaults for this task']
        : ['为此任务覆盖项目默认值']
      const codeName = locale === 'EN' ? 'Develop code' : '开发代码'
      const docName = locale === 'EN' ? 'Research / write a proposal' : '调研／写方案'
      const code = page.getByRole('radio', { name: codeName, exact: true })
      const docChoice = page.getByRole('radio', { name: docName, exact: true })
      await expect(code).toBeChecked()
      await page.getByRole('textbox').first().fill('Keep this requirement')
      await docChoice.check()
      await expect(docChoice).toBeChecked()
      await expect(page.getByRole('textbox').first()).toHaveValue('Keep this requirement')
      const materialTitle = page.getByLabel(locale === 'EN' ? 'Material title' : '材料标题')
      await materialTitle.fill('Keep this material')
      await code.check()
      await expect(materialTitle).toHaveCount(0)
      await docChoice.check()
      await expect(materialTitle).toHaveValue('Keep this material')
      for (const radio of [code, docChoice]) {
        const dimensions = await radio.evaluate(input => {
          const box = input.getBoundingClientRect()
          const label = input.closest('label')!.getBoundingClientRect()
          return { width: box.width, inside: box.x >= label.x && box.right <= label.right }
        })
        expect(dimensions.width).toBe(16)
        expect(dimensions.inside).toBe(true)
      }
      await code.check()
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
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
