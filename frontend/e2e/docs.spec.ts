import { test, expect } from '@playwright/test'

test('sidebar doc links load doc bodies via hash routing', async ({ page }) => {
  await page.goto('/')

  await page.getByRole('link', { name: 'Configuration' }).click()
  await expect(page).toHaveURL(/#\/docs\/getting-started\/configuration/)
  await expect(page.locator('main').getByRole('heading', { name: 'Configuration' })).toBeVisible()
  await expect(page.getByText('Fixture configuration content.', { exact: true })).toBeVisible()

  await page.getByRole('link', { name: 'Architecture Overview' }).click()
  await expect(page).toHaveURL(/#\/docs\/architecture\/overview/)
  await expect(page.locator('main').getByRole('heading', { name: 'Architecture Overview' })).toBeVisible()
})
