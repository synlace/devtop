import { test, expect } from '@playwright/test'

// Dragging the chat resize handle used to start a native text selection, then
// the layout shifted on every setChatWidth re-render so the selection kept
// re-forming and clearing ("highlighted and unhighlighted in quick
// succession"). Playwright's synthetic mouse events don't reproduce native
// drag-to-select, so the test asserts the guard that prevents it: text must be
// non-selectable while the drag is held, and re-enabled on release.
test('dragging the chat resize handle suppresses text selection during the drag', async ({ page }) => {
  await page.goto('/')

  // The hermetic run has no CopilotKit runtime, so a dev web-inspector overlay
  // covers the chat panel and would intercept pointer events. Remove it so the
  // test can reach the real resize handle.
  await page.evaluate(() => {
    document.querySelector('cpk-web-inspector')?.remove()
  })

  const handle = page.getByTestId('chat-resize-handle')
  await expect(handle).toBeVisible()
  const box = await handle.boundingBox()
  expect(box).not.toBeNull()

  const bodyUserSelect = () =>
    page.evaluate(() => getComputedStyle(document.body).userSelect)

  const cx = box!.x + box!.width / 2
  const cy = box!.y + box!.height / 2
  await page.mouse.move(cx, cy)
  await page.mouse.down()

  // While the button is held, text must not be selectable.
  let held = ''
  const steps = 4
  try {
    for (let i = 1; i <= steps; i++) {
      await page.mouse.move(cx - i * 12, cy)
      held = await bodyUserSelect()
      if (held !== 'none') {
        expect(held, 'body must be non-selectable while resizing').toBe('none')
        break
      }
    }
  } finally {
    await page.mouse.up()
  }

  // Resize actually applied, so the guard was exercised on a real drag.
  const widthAfter = (await handle.boundingBox())!.x
  expect(widthAfter, 'resize drag should move the handle').toBeLessThan(box!.x)

  // After release, normal selection is restored.
  await expect.poll(bodyUserSelect).not.toBe('none')
})

// As the panel resizes, re-renders shift elements under the pointer between the
// handle's col-resize cursor and the default/custom cursor on the content it
// passes over, which used to make the cursor flicker. The element under the
// pointer must keep the col-resize cursor for the whole drag — in both
// directions (enlarging drags left over the main pane, shrinking drags right
// over the chat) — and be restored after release.
test('dragging the chat resize handle holds the resize cursor under the pointer', async ({ page }) => {
  await page.goto('/')
  await page.evaluate(() => {
    document.querySelector('cpk-web-inspector')?.remove()
  })
  // Put main-pane content with its own cursor under the drag path so we prove
  // the pointer cursor is forced even over elements that set one themselves.
  await page.getByRole('link', { name: 'Board' }).click()
  await expect(page.getByRole('heading', { name: 'Ticket Board' })).toBeVisible()

  const handle = page.getByTestId('chat-resize-handle')
  await expect(handle).toBeVisible()
  const box = await handle.boundingBox()
  expect(box).not.toBeNull()

  const cursorAt = (x: number, y: number) =>
    page.evaluate(
      ([px, py]) => getComputedStyle(document.elementFromPoint(px, py)!).cursor,
      [x, y],
    )

  // Enlarging drags left (over the main pane); shrinking drags right. The
  // handle moves with the panel, so re-locate it before each drag.
  for (const dir of [-1, 1]) {
    const boxNow = await handle.boundingBox()
    expect(boxNow).not.toBeNull()
    const x = boxNow!.x + boxNow!.width / 2
    const y = boxNow!.y + boxNow!.height / 2
    await page.mouse.move(x, y)
    await page.mouse.down()
    let held = ''
    const steps = 4
    try {
      for (let i = 1; i <= steps; i++) {
        const px = x + dir * i * 12
        await page.mouse.move(px, y)
        // The 1.5px-wide handle tracks the pointer, so the pointer itself stays
        // on a col-resize element even without the guard. The flicker happens at
        // the handle's edges, so also sample just off the handle where the
        // surrounding content's cursor would otherwise show.
        for (const off of [-4, 0, 4]) {
          held = await cursorAt(px + off, y)
          if (held !== 'col-resize') {
            expect(held, `cursor ${off}px from pointer must be col-resize (dir=${dir})`).toBe('col-resize')
            break
          }
        }
      }
    } finally {
      await page.mouse.up()
    }
    // After release the forced cursor is lifted.
    await expect.poll(() => page.evaluate(() => document.documentElement.hasAttribute('data-resizing'))).toBe(false)
  }
})

// The visual flicker is caused by the panel lagging the pointer for a frame:
// with a React state update per mousemove, the width changes asynchronously, so
// the handle and the cursor hit-test at the pointer briefly sit over stale
// content. The panel must track the pointer synchronously within the mousemove
// handler. This dispatches a mousemove in-page and reads the width back in the
// same JS task, before any React commit could run.
test('chat panel width tracks the pointer synchronously during the drag', async ({ page }) => {
  await page.goto('/')
  await page.evaluate(() => {
    document.querySelector('cpk-web-inspector')?.remove()
  })

  const handle = page.getByTestId('chat-resize-handle')
  await expect(handle).toBeVisible()
  const box = await handle.boundingBox()
  expect(box).not.toBeNull()

  const cx = box!.x + box!.width / 2
  const cy = box!.y + box!.height / 2
  await page.mouse.move(cx, cy)
  await page.mouse.down()

  const before = await page.evaluate(() =>
    document.querySelector('[data-testid="copilot-chat-panel"]')!.getBoundingClientRect().width)
  const after = await page.evaluate(
    ({ x, y }) => {
      document.dispatchEvent(new MouseEvent('mousemove', { clientX: x, clientY: y, bubbles: true }))
      // Same JS task: if the width update went through React state it would not
      // have flushed yet and this would still read the pre-drag width.
      return document.querySelector('[data-testid="copilot-chat-panel"]')!.getBoundingClientRect().width
    },
    { x: cx - 24, y: cy },
  )
  await page.mouse.up()

  expect(after, 'panel width must update synchronously with the pointer').toBeGreaterThan(before)
  expect(after).toBeCloseTo(before + 24, 0)
})