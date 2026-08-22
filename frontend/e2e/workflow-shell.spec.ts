import { test, expect } from '@playwright/test'

// The reworked work-items view (mock-v2 shell in prod): two panes, tabs,
// live run log from the engine's SSE /api/events, resizable splitter and
// fullscreen. Runs against the real hermetic Go backend.

test.beforeEach(async ({ page }) => {
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })
})

test('work items render in list + detail panes with tabs', async ({ page }) => {
  await page.goto('/#/pipeline')
  await expect(page.getByRole('heading', { name: 'Work items' })).toBeVisible()

  // Two-pane shell exists.
  await expect(page.locator('.wi-list')).toBeVisible()
  await expect(page.locator('button[data-tab="artifacts"]')).toBeVisible()
  await expect(page.locator('button[data-tab="clarifications"]')).toBeVisible()
  await expect(page.locator('button[data-tab="log"]')).toBeVisible()
  await expect(page.getByRole('button', { name: /Run Log/ })).toBeVisible()

  // Capture a work item so the list has at least one row to drive.
  await page.getByText('+ Capture new work item').click()
  await page.locator('#root input[placeholder*="Title"]').fill('Workflow shell check')
  await page.getByRole('button', { name: 'Add' }).click()
  const row = page.locator('[data-row]').first()
  await expect(row).toBeVisible()
  const title = (await row.locator('.truncate').textContent())?.trim()
  expect(title?.length ?? 0).toBeGreaterThan(0)

  // Selecting a row fills the detail pane.
  await row.click()
  await expect(page.locator('#root').getByText('Workflow shell check').first()).toBeVisible()

  // Derive may fail (no AI key) but must not kill the view.
  await expect(page.getByRole('button', { name: 'Publish ready' })).toBeVisible()
})

test('run log tab consumes the /api/events stream', async ({ page }) => {
  await page.goto('/#/pipeline')
  await page.getByText('+ Capture new work item').click()
  await page.locator('#root input[placeholder*="Title"]').fill('Event log check')
  await page.getByRole('button', { name: 'Add' }).click()
  const row = page.locator('[data-row]').first()
  await expect(row).toBeVisible()
  await row.click()

  await page.getByRole('button', { name: /Run Log/ }).click()
  await expect(page.getByText(/Live/)).toBeVisible()

  // The seed.created event for the new item should arrive over SSE.
  await expect(page.getByText(/seed\.created/).first()).toBeVisible({ timeout: 10_000 })
})

test('narrow list prunes count columns; splitter and fullscreen work', async ({ page }) => {
  await page.goto('/#/pipeline')
  const list = page.locator('.wi-list')
  await expect(list).toBeVisible()

  const countDisplay = () =>
    list.evaluate(el => {
      const cell = el.querySelector('.wi-count')
      return cell ? getComputedStyle(cell).display : 'none'
    })
  const wide = await countDisplay()
  expect(wide).toBe('block')

  // Shrink the list by widening the detail pane (drag the splitter left).
  const splitter = page.locator('div[title="Drag to resize"]')
  const sb = await splitter.boundingBox()
  let y = 0
  if (sb) {
    y = sb.y + 200
    await page.mouse.move(sb.x + sb.width / 2, y)
    await page.mouse.down()
    await page.mouse.move(sb.x - 400, y, { steps: 6 })
    await page.mouse.up()
  }
  await expect.poll(countDisplay).toBe('none')

  // Fullscreen: list disappears, detail fills the row.
  const fullBtn = page.locator('button[data-full]')
  await fullBtn.click()
  await expect(list).toBeHidden()
  await fullBtn.click()
  await expect(list).toBeVisible()
})
