import { test, expect } from '@playwright/test'

test('ToolCallView renders a named tool call card', async ({ page }) => {
  await page.goto('/toolcall-test.html')
  await expect(page.getByText('write_doc', { exact: true })).toBeVisible()

  // Expand to reveal the arguments and result.
  await page.getByText('write_doc', { exact: true }).click()
  await expect(page.getByText('test-docs/test_doc_2.mdx', { exact: true })).toBeVisible()
  await expect(page.getByText('Written to docs/test-docs/test_doc_2.mdx', { exact: true })).toBeVisible()
})
