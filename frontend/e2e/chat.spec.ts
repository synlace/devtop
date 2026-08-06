import { test, expect } from '@playwright/test'

// CopilotKit auto-attaches its <cpk-web-inspector> overlay, which can swallow
// pointer events in the chat panel. Tests only exercise the shell UI, so the
// inspector is hidden to keep clicks deterministic.
async function hideWebInspector(page: import('@playwright/test').Page) {
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })
}

test('chat panel shell renders thread list', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)

  const chat = page.locator('aside').filter({ has: page.getByTitle('Toggle Fullscreen') })
  await expect(chat).toBeVisible()
  await expect(chat.getByTitle('Toggle Fullscreen')).toBeVisible()

  // The app auto-creates a thread on first load, so the header label is either
  // "Threads" or "Conversation" — don't assert it. Open the thread list via
  // whichever button is present ("Thread List" or "Back to Thread List").
  await chat.getByTitle(/Thread List/).click()
  await expect(chat.getByRole('button', { name: 'New conversation' })).toBeVisible()
  // NOTE: not asserting thread-list content — the Go backend auto-seeds
  // welcome threads per context, so it is nondeterministic.
})
