import { test, expect, type Page } from '@playwright/test'
import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'

// Real-AI specs: opt-in, live model calls, serial (see `just test ai`).
const aiEnabled = process.env.DEVTOP_AI_TESTS === '1'

async function hideWebInspector(page: Page) {
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })
}

// Send a message and return the latest assistant message locator.
async function ask(page: Page, prompt: string) {
  const input = page.getByTestId('copilot-chat-textarea')
  await expect(input).toBeVisible()
  await input.fill(prompt)
  await page.keyboard.press('Enter')

  const userMessage = page.getByTestId('copilot-user-message').last()
  await expect(userMessage).toContainText(prompt, { timeout: 30_000 })

  return page.getByTestId('copilot-assistant-message').last()
}

// The Go backend persists threads as JSON files under the AI runtime fixture;
// `listThreads` returns them sorted by updated_at descending.
function runtimeThreadsDir() {
  return path.resolve(process.cwd(), 'e2e', 'fixtures', '.devtop-ai-runtime', 'threads')
}

// Make the thread list visible, whichever header state the page restored.
async function ensureThreadList(page: Page) {
  if (await page.getByText('New conversation').isVisible().catch(() => false)) return
  const back = page.getByTitle('Back to Thread List')
  if (await back.isVisible().catch(() => false)) {
    await back.click()
  } else {
    await page.getByTitle('Thread List').click()
  }
  await expect(page.getByText('New conversation')).toBeVisible()
}

// Start a fresh thread. After the switch the chat remounts and reconnects; an
// immediately submitted message is dropped by the connect/run race, so let the
// new chat settle before asking.
async function startNewThread(page: Page) {
  await ensureThreadList(page)
  await page.getByText('New conversation').click()
  await expect(page.getByTestId('copilot-chat-textarea')).toBeVisible()
  await page.waitForTimeout(1_000)
}

test('@ai re-entering a thread does not duplicate the conversation', async ({ page }) => {
  test.skip(!aiEnabled, 'AI tests opt-in: run with `just test ai`')
  test.setTimeout(180_000)

  await page.goto('/')
  await hideWebInspector(page)

  const reply = await ask(page, 'Which tickets are currently open?')
  await expect(reply).toContainText('Fix the login button', { timeout: 120_000 })
  await page.waitForTimeout(3_000) // let streaming settle

  // Leave the thread and re-enter it (first row = most recently updated).
  const countReplyText = async () =>
    (await page.getByTestId('copilot-chat').innerText()).split('Fix the login button').length - 1

  const before = await countReplyText()
  expect(before).toBeGreaterThanOrEqual(1)

  await page.getByTitle('Back to Thread List').click()
  await page
    .locator('aside div.group')
    .filter({ hasText: 'messages' })
    .first()
    .click()
  await expect(page.getByTestId('copilot-chat-textarea')).toBeVisible()
  await page.waitForTimeout(10_000) // window for any duplicate to render

  // Bug 1: re-entry appends another welcome/response each time.
  expect(await countReplyText()).toBe(before)
})

test('@ai conversation messages survive a server restart', async ({ page }) => {
  test.skip(!aiEnabled, 'AI tests opt-in: run with `just test ai`')
  test.setTimeout(180_000)

  await page.goto('/')
  await hideWebInspector(page)

  // Unique token so we can prove this exact message was persisted.
  const token = `persist-probe-${Date.now()}`
  const reply = await ask(page, `Reply with the exact text: ${token}`)
  await expect(reply).toContainText(token, { timeout: 120_000 })

  // The conversation must be written to the thread store on disk. Bug 2: the
  // CopilotKit runtime keeps messages in memory only, so a server restart
  // wipes them and no thread file ever contains the message. The write lands
  // just after the run completes, so poll for it.
  const dir = runtimeThreadsDir()
  await expect
    .poll(
      () => {
        const files = readdirSync(dir).filter((f) => f.endsWith('.json'))
        return files.some((f) => readFileSync(path.join(dir, f), 'utf8').includes(token))
      },
      { timeout: 15_000 }
    )
    .toBe(true)
})

test('@ai thread list message count reflects the conversation', async ({ page }) => {
  test.skip(!aiEnabled, 'AI tests opt-in: run with `just test ai`')
  test.setTimeout(180_000)

  await page.goto('/')
  await hideWebInspector(page)

  const reply = await ask(page, 'Which tickets are currently open?')
  await expect(reply).toContainText('Fix the login button', { timeout: 120_000 })

  // Open the thread list. Bug 3: every thread file only contains the "Ready to
  // help" placeholder written at creation, so message_count is always 1.
  await page.getByTitle('Back to Thread List').click()
  const rows = await page
    .locator('aside div.group')
    .filter({ hasText: 'messages' })
    .allTextContents()
  const anyCountAboveOne = rows.some((row) => /([2-9]\d*) messages/.test(row))
  expect(anyCountAboveOne).toBe(true)
})

test('@ai thread list message count updates when returning from a conversation', async ({ page }) => {
  test.skip(!aiEnabled, 'AI tests opt-in: run with `just test ai`')
  test.setTimeout(180_000)

  await page.goto('/')
  await hideWebInspector(page)

  // Start a fresh thread so its message count starts at exactly 1.
  await startNewThread(page)

  const reply = await ask(page, 'Which tickets are currently open?')
  await expect(reply).toContainText('Fix the login button', { timeout: 120_000 })
  await page.waitForTimeout(2_000) // let persistence land before checking

  // Return to the thread list. The current thread (most recently updated, so
  // first row) must show the real message count without a page reload.
  // Bug 1: opening the list does not refetch, so it shows the stale count (1).
  await page.getByTitle('Back to Thread List').click()
  await expect(page
    .locator('aside div.group')
    .filter({ hasText: 'messages' })
    .first()
  ).toContainText(/([2-9]\d*) messages/)
})

// The chat's real scroll container is an internal div without a distinctive
// class — the `.cpk:*` scrollers around it grow with content instead of
// constraining — so locate it by computed overflow plus actual overflow.
// Optionally set scrollTop (target) and return the resulting state.
async function chatScroller(page: Page, target?: number) {
  return page.getByTestId('copilot-chat').evaluate((root, t) => {
    for (const el of root.querySelectorAll('*')) {
      const cs = getComputedStyle(el)
      if ((cs.overflowY === 'auto' || cs.overflowY === 'scroll') && el.scrollHeight > el.clientHeight + 1) {
        if (typeof t === 'number') el.scrollTop = t
        return {
          scrollHeight: el.scrollHeight,
          clientHeight: el.clientHeight,
          scrollTop: el.scrollTop,
          maxScroll: el.scrollHeight - el.clientHeight,
        }
      }
    }
    return null
  }, target)
}

test('@ai re-entering a thread resumes the chat scroll position', async ({ page }) => {
  test.skip(!aiEnabled, 'AI tests opt-in: run with `just test ai`')
  test.setTimeout(180_000)

  await page.goto('/')
  await hideWebInspector(page)

  await startNewThread(page)

  // Build a conversation long enough to overflow the chat scroll area.
  for (const { text, expectText } of [
    { text: 'Which tickets are currently open?', expectText: 'Fix the login button' },
    { text: 'List every ticket with its status, priority and assignee.', expectText: 'Fix the login button' },
    { text: 'Describe the fix needed for the login button ticket in detail.', expectText: /login button/i },
  ]) {
    const reply = await ask(page, text)
    await expect(reply).toContainText(expectText, { timeout: 120_000 })
  }
  await page.waitForTimeout(2_000) // let streaming settle

  // Scroll the chat away from the bottom and record the position.
  const chatScroll = await chatScroller(page)
  expect(chatScroll).not.toBeNull()
  expect(chatScroll.maxScroll).toBeGreaterThan(0) // must be scrollable
  const target = Math.min(120, Math.max(1, chatScroll.maxScroll - 40))
  const beforeState = await chatScroller(page, target)
  expect(beforeState.scrollTop).toBeGreaterThan(0)
  const before = beforeState.scrollTop

  // Let the stick-to-bottom library register the programmatic scroll as a
  // user "escape" from the bottom lock — otherwise its isAtBottom state is
  // still true and re-entry can re-animate to the bottom.
  await page.waitForTimeout(500)

  // Return to the list and re-enter the same thread (first row).
  await page.getByTitle('Back to Thread List').click()
  await page
    .locator('aside div.group')
    .filter({ hasText: 'messages' })
    .first()
    .click()
  await expect(page.getByTestId('copilot-chat-textarea')).toBeVisible()

  // Bug 2: re-entry used to trigger a smooth scroll-to-bottom animation, so the
  // chat jumped to the bottom (~maxScroll) instead of resuming from the
  // recorded position. The fix keeps the chat mounted under the thread list, so
  // the position is preserved. A generous tolerance absorbs the occasional
  // CopilotKit replay re-render (a ~60px shift), while a jump to the bottom
  // (maxScroll ~1700px) still fails decisively.
  await page.waitForTimeout(1_500)
  const afterState = await chatScroller(page)
  expect(afterState).not.toBeNull()
  expect(Math.abs(afterState.scrollTop - before)).toBeLessThan(150)
})
