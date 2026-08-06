import { test, expect } from '@playwright/test'

async function hideWebInspector(page: import('@playwright/test').Page) {
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })
}

test('chat panel resize drag changes width and clamps to bounds', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)

  const chat = page.locator('aside').filter({ has: page.getByTitle('Toggle Fullscreen') })
  const handle = chat.locator('.cursor-col-resize')
  await expect(handle).toBeVisible()

  const before = await chat.boundingBox()
  expect(before).not.toBeNull()

  // Drag the left edge further left → panel widens.
  const startY = before!.y + before!.height / 2
  await handle.hover()
  await page.mouse.down()
  await page.mouse.move(before!.x - 80, startY, { steps: 8 })
  await page.mouse.up()

  const widened = await chat.boundingBox()
  expect(widened!.width).toBeGreaterThan(before!.width + 50)

  // Drag way past the left edge → width must clamp so it never exceeds
  // window.innerWidth - 100 and never drops below the 300px minimum.
  await handle.hover()
  await page.mouse.down()
  await page.mouse.move(0, startY, { steps: 8 })
  await page.mouse.up()

  const maxWidth = await page.evaluate(() => window.innerWidth - 100)
  const clamped = await chat.boundingBox()
  expect(clamped!.width).toBeGreaterThanOrEqual(300)
  expect(clamped!.width).toBeLessThanOrEqual(maxWidth)
  expect(clamped!.width).toBeGreaterThan(widened!.width)
})
