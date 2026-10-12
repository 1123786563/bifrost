import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('Language switcher', () => {
  test('switches the UI to Chinese and persists after reload', async ({ page }) => {
    await page.goto('/')
    await page.getByTestId('language-switcher-trigger').click()
    await page.getByTestId('language-switcher-option-zh-CN').click()

    // The trigger's sr-only label is translated from common:language.
    await expect(page.getByTestId('language-switcher-trigger')).toContainText('语言')

    await page.reload()
    await expect(page.getByTestId('language-switcher-trigger')).toContainText('语言')
  })

  test('defaults to English when no preference is stored', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByTestId('language-switcher-trigger')).toContainText('Language')
  })
})
