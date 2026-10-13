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

// The login page (setup-token screen and login form) is the first surface an
// operator sees; it must expose the switcher too, not just the post-login topbar.
// skipAutoLogin stops the fixture from auto-filling the login form the moment
// it renders.
test.describe('Language switcher on the login page', () => {
  test.use({ skipAutoLogin: true })

  test('switches language before authentication and persists after reload', async ({ page }) => {
    await page.goto('/login')

    await expect(page.getByTestId('language-switcher-trigger')).toContainText('Language')
    await page.getByTestId('language-switcher-trigger').click()
    await page.getByTestId('language-switcher-option-zh-CN').click()

    await expect(page.getByTestId('language-switcher-trigger')).toContainText('语言')

    await page.reload()
    await expect(page.getByTestId('language-switcher-trigger')).toContainText('语言')
  })
})
