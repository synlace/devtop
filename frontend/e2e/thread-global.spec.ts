import { test, expect } from '@playwright/test'

// Threads are repo-global: navigating between pages (kinds and individual
// docs) must never switch the active thread or spawn auto-created shells.
// Regression guard for the per-page contextKey wiring.
test('threads are repo-global across page navigation', async ({ page }) => {
  await page.goto('/')
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })

  const chat = page.locator('aside').filter({ has: page.getByTitle('Toggle Fullscreen') })
  await expect(chat.getByTitle(/Thread List|Back to Thread List/)).toBeVisible({ timeout: 45_000 })

  // Land in an active conversation deterministically: auto-create can race
  // stale viewstate, so if there is no composer yet, open the thread list and
  // start a new conversation.
  const composer = chat.getByPlaceholder('Type a message...')
  if (!(await composer.isVisible().catch(() => false))) {
    if (!(await chat.getByTitle('Thread List').isVisible().catch(() => false))) {
      await chat.getByTitle('Back to Thread List').click()
    }
    await chat.getByTitle('Thread List').click()
    const newBtn = chat.getByRole('button', { name: 'New conversation' })
    await expect(newBtn).toBeVisible({ timeout: 15_000 })
    await newBtn.click()
  }
  await expect(composer).toBeVisible()

  // The initial entry auto-created exactly one thread; wait for it.
  await page.waitForFunction(async () => {
    try {
      const r = await fetch('/api/threads')
      if (!r.ok) return false
      const t = await r.json()
      return Array.isArray(t) && t.length >= 1
    } catch {
      return false
    }
  }, undefined, { timeout: 30_000 })

  const listThreads = async () => {
    const r = await page.request.get('/api/threads')
    expect(r.ok()).toBe(true)
    return (await r.json()) as { id: string; context?: string; title?: string }[]
  }

  const before = await listThreads()
  const beforeIds = before.map((t) => t.id).sort()

  // Hop across kinds and into a subpage: none may add a thread.
  for (const [name, hash] of [
    ['Board', /#\/tickets/],
    ['PRDs', /#\/prds/],
    ['Architecture Overview', /#\/docs\/architecture\/overview/],
  ] as const) {
    await page.locator('nav').getByRole('link', { name }).first().click()
    await expect(page).toHaveURL(hash, { timeout: 15_000 })
    await page.waitForTimeout(400)
  }
  // Give any stray auto-create timer room to fire before the assertion.
  await page.waitForTimeout(1500)

  // The chat is still on the same conversation, not reset to a new context.
  await expect(chat.getByTitle('Back to Thread List')).toBeVisible({ timeout: 15_000 })

  const after = await listThreads()
  expect(after.map((t) => t.id).sort()).toEqual(beforeIds)
})