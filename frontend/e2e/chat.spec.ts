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

test('composer is covered while the thread list is open', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)

  const chat = page.locator('aside').filter({ has: page.getByTitle('Toggle Fullscreen') })
  const composer = chat.getByPlaceholder('Type a message...')
  // A thread is auto-selected on load, so the composer is present.
  await expect(composer).toBeVisible()

  await chat.getByTitle(/Thread List/).click()
  await expect(chat.getByRole('button', { name: 'New conversation' })).toBeVisible()

  // The list is an overlay; CopilotKit paints its input at z-20 (above the old
  // overlay z-10), so the composer stays laid out but must not be the topmost
  // element at its location. Assert paint order, not display visibility.
  await expect.poll(async () =>
    page.evaluate(() => {
      const input = document.querySelector('textarea')
      if (!input) return true
      const r = input.getBoundingClientRect()
      if (r.width === 0 && r.height === 0) return true
      const el = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)
      return !input.contains(el)
    })
  ).toBe(true)
})

test('thread list shows a functional toggle, not a dead back button', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)

  const chat = page.locator('aside').filter({ has: page.getByTitle('Toggle Fullscreen') })
  // A thread is auto-selected on load, so the back button is shown.
  await expect(chat.getByTitle('Back to Thread List')).toBeVisible()

  // Open the thread list. The back button must disappear (it was a no-op
  // pointing at the list we're already on) and the toggle must remain.
  await chat.getByTitle('Back to Thread List').click()
  await expect(chat.getByRole('button', { name: 'New conversation' })).toBeVisible()
  await expect(chat.getByTitle('Back to Thread List')).toHaveCount(0)
  await expect(chat.getByTitle('Thread List')).toBeVisible()

  // The toggle returns to the conversation.
  await chat.getByTitle('Thread List').click()
  await expect(chat.getByTitle('Back to Thread List')).toBeVisible()
})

test('chat inline code renders without duplicate backticks', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)

  // CopilotKit scopes Tailwind Typography to `.cpk\:prose`, which paints
  // literal backtick glyphs around inline code via ::before/::after — the
  // same bug the doc panel had. Inject the two shapes CopilotKit produces
  // (`.cpk\:prose code` and its streaming `data-streamdown="inline-code"`)
  // and assert neither shows a backtick pseudo-element.
  const pseudo = await page.evaluate(() => {
    const probe = document.createElement('div')
    probe.className = 'cpk:prose'
    probe.innerHTML = '<p><code>just devtop push</code></p>'
    document.body.appendChild(probe)

    const viaProse = probe.querySelector('code') as HTMLSpanElement
    const proseBefore = getComputedStyle(viaProse, '::before').content
    const proseAfter = getComputedStyle(viaProse, '::after').content

    const stream = document.createElement('code')
    stream.setAttribute('data-streamdown', 'inline-code')
    stream.textContent = '.github'
    document.body.appendChild(stream)
    const sBefore = getComputedStyle(stream, '::before').content
    const sAfter = getComputedStyle(stream, '::after').content

    probe.remove()
    stream.remove()
    return { proseBefore, proseAfter, sBefore, sAfter }
  })

  expect(pseudo.proseBefore).toBe('none')
  expect(pseudo.proseAfter).toBe('none')
  expect(pseudo.sBefore).toBe('none')
  expect(pseudo.sAfter).toBe('none')
})
