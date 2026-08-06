import { test, expect } from '@playwright/test'

// Regression test for the fullscreen bug where `.relative` won the cascade
// over `.fixed`, leaving the chat panel overflowing the viewport and the
// exit button clipped off-screen.
test('chat fullscreen covers the viewport and keeps the toggle visible', async ({ page }) => {
  await page.goto('/')
  // CopilotKit's <cpk-web-inspector> overlay intercepts pointer events in the
  // chat panel; hide it so the toggle clicks are deterministic.
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })

  const chat = page.locator('aside').filter({ has: page.getByTitle('Toggle Fullscreen') })
  const toggle = chat.getByTitle('Toggle Fullscreen')
  const viewport = page.viewportSize()!

  await toggle.click()

  // Panel is truly fixed and covers the viewport
  await expect
    .poll(() => chat.evaluate((el) => getComputedStyle(el).position))
    .toBe('fixed')
  const panel = (await chat.boundingBox())!
  expect(panel.x).toBeLessThanOrEqual(0)
  expect(panel.y).toBeLessThanOrEqual(0)
  expect(panel.width).toBeGreaterThanOrEqual(viewport.width)
  expect(panel.height).toBeGreaterThanOrEqual(viewport.height)

  // The exit toggle must remain visible inside the viewport
  const button = (await toggle.boundingBox())!
  expect(button.x).toBeGreaterThanOrEqual(0)
  expect(button.y).toBeGreaterThanOrEqual(0)
  expect(button.x + button.width).toBeLessThanOrEqual(viewport.width)
  expect(button.y + button.height).toBeLessThanOrEqual(viewport.height)

  // Exit fullscreen restores the docked panel
  await toggle.click()
  await expect
    .poll(() => chat.evaluate((el) => getComputedStyle(el).position))
    .toBe('relative')
  const docked = (await chat.boundingBox())!
  expect(docked.width).toBeLessThan(viewport.width)
})
