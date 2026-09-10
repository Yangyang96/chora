import { expect, type Page } from '@playwright/test'

export async function acceptPlanningDraft(page: Page, note = 'The generated plan is bounded and ready for execution.') {
  await expect(page.getByRole('heading', { name: 'Edit Technical Plan Draft' })).toBeVisible()
  await page.getByRole('button', { name: 'Submit Revision' }).click()
  await expect(page.getByRole('heading', { name: /Review Submitted Revision/ })).toBeVisible()
  await page.getByLabel('Planning review note').fill(note)
  await page.getByRole('button', { name: 'Accept Revision' }).click()
  await expect(page.getByRole('heading', { name: 'Accepted Technical Plan' })).toBeVisible()
}
