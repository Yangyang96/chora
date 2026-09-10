import { expect, test } from '@playwright/test'
import { authoritativePatchSHA256, captureJourney, createRegisteredFakeRun, waitForRunStatus } from './verification'

// Verification auto-starts once the Agent completes, so these tests land on
// the review gate without a manual "Start Verification" step. SCM and raw
// Patch behavior are asserted through the API; the Docker-specific verifier
// container checks are covered by the real Pi/Docker acceptance path.
test('authoritative G2-M3 Patch is auto-verified and accepted', async ({ page, request }) => {
  const { run, v8 } = await createRegisteredFakeRun(request, 'g2-m3-authoritative')
  expect(run.verification?.result?.outcome).toBe('review_ready')
  expect(run.verification?.bindings.patchDigest).toBe(authoritativePatchSHA256)
  expect(run.reviewablePatch?.patchDigest).toBe(authoritativePatchSHA256)

  await page.goto(runURL(v8, run.id))
  await expect(page.getByText('Awaiting review')).toBeVisible()
  await expect(page.getByText('Audit details')).toBeVisible()

  await page.getByRole('button', { name: 'Accept', exact: true }).click()
  await expect(page.getByText('Accepted · Not applied', { exact: true }).first()).toBeVisible()

  const accepted = await waitForRunStatus(request, run.id, 'accepted')
  expect(accepted.verifiedReview).toMatchObject({ kind: 'accept', patchDigest: authoritativePatchSHA256 })
  expect(accepted.reviewablePatch?.patchDigest).toBe(authoritativePatchSHA256)
  await captureJourney('g2-m5-accepted', accepted)
})

test('Ask Agent to fix produces a successor Attempt and re-verifies', async ({ page, request }) => {
  const { run, v8 } = await createRegisteredFakeRun(request, 'g2-m3-authoritative')
  await page.goto(runURL(v8, run.id))
  await expect(page.getByText('Awaiting review')).toBeVisible()

  await page.getByRole('button', { name: 'Ask Agent to fix', exact: true }).click()
  const successor = await waitForRunStatus(request, run.id, 'awaiting_review', 120_000, 2)
  expect(successor.attempt).toBe(2)
  expect(successor.verifiedReviewHistory?.at(-1)?.rejectionClass).toBe('implementation_gap')
  expect(successor.verification?.result?.outcome).toBe('review_ready')
  await captureJourney('g2-m5-rejected-retried', successor)
})

test('Change requirement returns to the one-sentence Task composer', async ({ page, request }) => {
  const { run, v8 } = await createRegisteredFakeRun(request, 'g2-m3-authoritative')
  await page.goto(runURL(v8, run.id))
  await expect(page.getByText('Awaiting review')).toBeVisible()
  await page.getByRole('button', { name: 'Change requirement', exact: true }).click()
  await expect(page).toHaveURL(`/rooms/${v8.task.room_id}`)
  await expect(page.getByLabel('What should Chora build?')).toBeVisible()
})

function runURL(contract: { task: { id: string; room_id: string } }, runID: string) {
  return `/rooms/${encodeURIComponent(contract.task.room_id)}/tasks/${encodeURIComponent(contract.task.id)}/runs/${encodeURIComponent(runID)}`
}
