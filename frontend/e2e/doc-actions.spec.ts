import { test, expect } from '@playwright/test'
import { readFileSync } from 'node:fs'

// The ⋯ doc action menu: quick actions (star/clock/trash), copy path, and the
// Export-as flyout. Same CopilotKit overlay suppression as navigation.spec.ts.
async function hideWebInspector(page: import('@playwright/test').Page) {
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })
}

async function openDocMenu(page: import('@playwright/test').Page, title: string) {
  const row = page.locator('nav').getByRole('link', { name: title }).first()
  await row.hover()
  const dots = page.getByTitle(`Actions for ${title}`).first()
  await expect(dots).toBeVisible()
  await dots.click()
  await expect(page.getByRole('menu', { name: `Actions for ${title}` })).toBeVisible()
}

test('menus open on hover and close on Escape or outside click', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)

  await openDocMenu(page, 'Configuration')
  const menu = page.getByRole('menu', { name: 'Actions for Configuration' })
  await expect(menu.getByTitle('Add to favourites')).toBeVisible()
  await expect(menu.getByRole('menuitem', { name: 'Copy path' })).toBeVisible()
  await expect(menu.getByRole('menuitem', { name: 'Export as' })).toBeVisible()
  await expect(menu.getByTitle('Delete')).toBeVisible()

  await page.keyboard.press('Escape')
  await expect(page.getByRole('menu', { name: 'Actions for Configuration' })).toBeHidden()

  await openDocMenu(page, 'Configuration')
  await page.locator('main').click({ position: { x: 40, y: 40 } })
  await expect(page.getByRole('menu', { name: 'Actions for Configuration' })).toBeHidden()
})

test('starring a doc adds it to Favourites and persists across reload', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)
  await expect(page.locator('nav').getByText('Favourites')).toBeHidden()

  await openDocMenu(page, 'Configuration')
  await page.getByTitle('Add to favourites').click()

  // The tree row keeps its place (star badge, no duplicate) and the doc also
  // appears in the Favourites section above the tree.
  const favSection = page.locator('nav').getByText('Favourites')
  await expect(favSection).toBeVisible()
  const favRow = page.locator('nav').getByRole('link', { name: 'Configuration' })
  await expect(favRow).toHaveCount(2)
  await expect(favRow.first().locator('svg.text-amber-400')).toBeVisible()

  await page.reload()
  await expect(page.locator('nav').getByText('Favourites')).toBeVisible()
  await expect(page.locator('nav').getByRole('link', { name: 'Configuration' })).toHaveCount(2)

  // Unstar removes it again.
  await openDocMenu(page, 'Configuration')
  await page.getByTitle('Remove from favourites').click()
  await expect(page.locator('nav').getByText('Favourites')).toBeHidden()
})

test('favourites persist through the backend API', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)
  await openDocMenu(page, 'Configuration')
  await page.getByTitle('Add to favourites').click()

  const apiBase = process.env.DEVTOP_TEST_BACKEND_PORT
    ? `http://127.0.0.1:${process.env.DEVTOP_TEST_BACKEND_PORT}`
    : 'http://127.0.0.1:8134'
  const favs = await page.request.get(`${apiBase}/api/favourites`).then(r => r.json())
  expect(favs).toEqual(['getting-started/configuration'])
})

test('export as Markdown downloads the doc content', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)

  await openDocMenu(page, 'Configuration')
  await page.getByRole('menuitem', { name: 'Export as' }).click()
  const submenu = page.getByRole('menu', { name: 'Export as' })

  // Deferred formats are present but disabled.
  const pdf = submenu.getByRole('menuitem', { name: /PDF/ })
  await expect(pdf).toBeDisabled()
  const word = submenu.getByRole('menuitem', { name: /Word/ })
  await expect(word).toBeDisabled()

  const downloadPromise = page.waitForEvent('download')
  await submenu.getByRole('menuitem', { name: /Markdown/ }).click()
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe('Configuration.mdx')
  const content = await download.path().then(p => readFileSync(p, 'utf-8'))
  expect(content).toContain('Fixture configuration content.')
})

test('copy path writes the docs path to the clipboard', async ({ page }) => {
  const context = page.context()
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.goto('/')
  await hideWebInspector(page)

  await openDocMenu(page, 'Configuration')
  await page.getByRole('menuitem', { name: 'Copy path' }).click()
  const clip = await page.evaluate(() => navigator.clipboard.readText().catch(() => ''))
  expect(clip).toBe('docs/getting-started/configuration')
})