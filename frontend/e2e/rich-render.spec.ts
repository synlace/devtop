import { test, expect } from '@playwright/test'

test('docs render tables, mermaid, and highlighted code', async ({ page }) => {
  await page.goto('/#/docs/rich-render')

  await expect(page.locator('main').getByRole('heading', { name: 'Rich Rendering' })).toBeVisible()

  // GFM table
  const table = page.locator('.prose-custom table')
  await expect(table).toBeVisible()
  await expect(table.locator('thead tr')).toHaveCount(1)
  await expect(table.locator('tbody td')).toHaveCount(6)

  // Mermaid -> SVG (async client-side render)
  const mermaid = page.locator('.prose-custom .mermaid')
  await expect(mermaid).toBeVisible()
  await expect(mermaid.locator('svg')).toBeVisible()

  // Highlighted fenced code
  const code = page.locator('.prose-custom pre code').first()
  await expect(code).toContainText('func main()')
  await expect(code).toHaveClass(/hljs/)
})