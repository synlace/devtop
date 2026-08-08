import { test, expect } from '@playwright/test'

// When the CopilotKit runtime is reachable but has no API key, the app opens
// the settings dialog focused on the AI/provider pane instead of mounting
// CopilotChat. In hermetic mode the real runtime isn't running, so the spec
// mocks the status endpoint to simulate a reachable, unconfigured runtime.
async function mockUnconfiguredRuntime(page: import('@playwright/test').Page) {
  await page.route('**/api/copilotkit/ai-status', route =>
    route.fulfill({ json: { configured: false, remembered: false, baseURL: 'https://openrouter.ai/api/v1', model: 'openai/gpt-4o-mini' } })
  )
}

async function hideInspector(page: import('@playwright/test').Page) {
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })
}

// The settings dialog auto-opens when unconfigured. Returns the dialog locator.
async function openDialog(page: import('@playwright/test').Page) {
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  return dialog
}

test('opens the settings dialog focused on the AI provider when unconfigured', async ({ page }) => {
  await mockUnconfiguredRuntime(page)
  await page.goto('/')
  await hideInspector(page)

  const dialog = await openDialog(page)
  await expect(dialog.getByText('AI provider')).toBeVisible()
  await expect(dialog.getByLabel('AI base URL')).toHaveValue('https://openrouter.ai/api/v1')
  await expect(dialog.getByLabel('AI model')).toHaveValue('openai/gpt-4o-mini')

  // CopilotChat is not mounted — the chat panel shows the compact not-configured CTA.
  const chat = page.locator('aside').filter({ has: page.getByTitle('Toggle Fullscreen') })
  await expect(chat.getByText('AI assistant not configured')).toBeVisible()
  await expect(page.getByPlaceholder('Type a message...')).toHaveCount(0)
})

test('provider preset switches base URL and model for LM Studio', async ({ page }) => {
  await mockUnconfiguredRuntime(page)
  await page.goto('/')
  await hideInspector(page)

  const dialog = await openDialog(page)
  await dialog.getByRole('button', { name: 'LM' }).click()
  await expect(dialog.getByLabel('AI base URL')).toHaveValue('http://localhost:1234/v1')
  await expect(dialog.getByLabel('AI model')).toHaveValue('lmstudio-community/llama-3.2-3b-instruct')
  // LM Studio prefills a sentinel key so Save is enabled.
  await expect(dialog.getByLabel('AI API key')).toHaveValue('lm-studio')
})

test('saving a key sends provider config and closes the dialog', async ({ page }) => {
  await mockUnconfiguredRuntime(page)
  await page.route('**/api/copilotkit/ai-key', async route => {
    const body = route.request().postDataJSON()
    expect(body.key).toBe('sk-test-123')
    expect(body.baseURL).toBe('https://openrouter.ai/api/v1')
    expect(body.model).toBe('openai/gpt-4o-mini')
    await route.fulfill({ json: { configured: true, remembered: false, baseURL: 'https://openrouter.ai/api/v1', model: 'openai/gpt-4o-mini' } })
  })
  await page.goto('/')
  await hideInspector(page)

  const dialog = await openDialog(page)
  await dialog.getByLabel('AI API key').fill('sk-test-123')
  await dialog.getByRole('button', { name: 'Save' }).click()

  // Dialog closes once configured.
  await expect(dialog).toHaveCount(0)
})