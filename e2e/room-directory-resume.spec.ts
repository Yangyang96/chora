import { expect, test } from '@playwright/test'

// Sidebar navigation, deep-link resume, and Task-level archive/restore
// (Task archive replaced the old Room archive in the UI; Room archive
// remains a backend capability covered by Go tests).
test('resumes a Room from the sidebar and archives/restores a Task', async ({ page, request }) => {
  await page.goto('/')

  await page.getByRole('button', { name: 'New Room', exact: true }).click()
  await page.getByLabel('Room name').fill('Sidebar Room')
  await page.getByRole('button', { name: 'Create Room', exact: true }).click()
  await expect(page).toHaveURL(/\/rooms\//)
  await expect(page.getByText('This Room has no Tasks yet.')).toBeVisible()

  const roomID = new URL(page.url()).pathname.split('/').filter(Boolean)[1]
  const roomResponse = await request.get(`/api/rooms/${encodeURIComponent(roomID)}`)
  expect(roomResponse.ok()).toBeTruthy()
  const room = await roomResponse.json()
  const taskResponse = await request.post(`/api/rooms/${encodeURIComponent(roomID)}/tasks`, {
    data: {
      title: 'Add a sort button', goal: 'Add a sort button', criteria: ['Requirement satisfied'],
      revisionIds: [room.revisions[0].id], executionProfile: 'diagnostic_fake',
    },
  })
  expect(taskResponse.ok()).toBeTruthy()
  const task = await taskResponse.json()
  await page.goto(`/rooms/${encodeURIComponent(roomID)}/tasks/${encodeURIComponent(task.id)}`)
  await expect(page).toHaveURL(new RegExp(`/tasks/${task.id}$`))

  // A saved, inactive Task can be archived and restored through the sidebar.
  await page.locator('.sidebar-room-name', { hasText: 'Sidebar Room' }).click()
  await expect(page.locator('.task-title', { hasText: 'Add a sort button' })).toBeVisible()
  await page.getByRole('button', { name: 'Archive', exact: true }).click()
  await expect(page.getByText(/Archived · 1/)).toBeVisible()

  // Expand the archived section and restore.
  await page.getByText(/Archived · 1/).click()
  await page.getByRole('button', { name: 'Restore', exact: true }).click()
  await expect(page.getByText(/Archived · 1/)).toHaveCount(0)

  // Starting the diagnostic Task creates active work. It must then refuse
  // archive; this fixture deliberately has no real verification contract.
  await page.goto(`/rooms/${encodeURIComponent(roomID)}/tasks/${encodeURIComponent(task.id)}`)
  await page.getByRole('button', { name: 'Start Run', exact: true }).click()
  await expect(page).toHaveURL(/\/runs\//)
  await page.locator('.sidebar-room-name', { hasText: 'Sidebar Room' }).click()
  await page.getByRole('button', { name: 'Archive', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('store room state forbids mutation')
  await expect(page.getByText(/Archived · 1/)).toHaveCount(0)
  await expect(page.locator('.task-title', { hasText: 'Add a sort button' })).toBeVisible()
})
