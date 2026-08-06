import { test, expect } from '@playwright/test'

// When the CopilotKit runtime is reachable but has no API key, the chat panel
// shows a provider/key wizard instead of mounting CopilotChat. In hermetic
// mode the real runtime isn't running, so the spec mocks the status endpoint
// to simulate a reachable, unconfigured runtime.
async function mockUnconfiguredRuntime(page: import('@playwright/test').Page) {
  await page.route('**/api/copilotkit/ai-status', route =>
    route.fulfill({ json: { configured: false, remembered: false, baseURL: 'https://openrouter.ai/api/v1', model: 'openai/gpt-4o-mini' } })
  )
}

test('shows the AI provider wizard instead of CopilotChat when unconfigured', async ({ page }) => {
  await mockUnconfiguredRuntime(page)
  await page.goto('/')
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })

  const chat = page.locator('aside').filter({ has: page.getByTitle('Toggle Fullscreen') })
  await expect(chat.getByText('Connect an AI provider')).toBeVisible()
  await expect(chat.getByLabel('AI base URL')).toHaveValue('https://openrouter.ai/api/v1')
  await expect(chat.getByLabel('AI model')).toHaveValue('openai/gpt-4o-mini')
  // The message composer must not be present — CopilotChat is not mounted.
  await expect(chat.getByPlaceholder('Type a message...')).toHaveCount(0)
})

test('provider preset switches base URL and model for LM Studio', async ({ page }) => {
  await mockUnconfiguredRuntime(page)
  await page.goto('/')
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })

  const chat = page.locator('aside').filter({ has: page.getByTitle('Toggle Fullscreen') })
  await chat.getByRole('button', { name: 'LM' }).click()
  await expect(chat.getByLabel('AI base URL')).toHaveValue('http://localhost:1234/v1')
  await expect(chat.getByLabel('AI model')).toHaveValue('lmstudio-community/llama-3.2-3b-instruct')
  // LM Studio prefills a sentinel key so Save is enabled.
  await expect(chat.getByLabel('AI API key')).toHaveValue('lm-studio')
})

test('saving a key sends provider config and hides the wizard', async ({ page }) => {
  await mockUnconfiguredRuntime(page)
  await page.route('**/api/copilotkit/ai-key', async route => {
    const body = route.request().postDataJSON()
    expect(body.key).toBe('sk-test-123')
    expect(body.baseURL).toBe('https://openrouter.ai/api/v1')
    expect(body.model).toBe('openai/gpt-4o-mini')
    await route.fulfill({ json: { configured: true, remembered: false, baseURL: 'https://openrouter.ai/api/v1', model: 'openai/gpt-4o-mini' } })
  })
  await page.goto('/')
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })

  const chat = page.locator('aside').filter({ has: page.getByTitle('Toggle Fullscreen') })
  await chat.getByLabel('AI API key').fill('sk-test-123')
  await chat.getByRole('button', { name: 'Save' }).click()

  // Wizard disappears once configured.
  await expect(chat.getByText('Connect an AI provider')).toHaveCount(0)
})
